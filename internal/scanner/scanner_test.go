package scanner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"shelf/internal/config"
	"shelf/internal/ids"
	"shelf/internal/index"
	"shelf/internal/openpool"
	"shelf/internal/source"
	"shelf/internal/testutil"
	"shelf/internal/userdata"
)

// ---------------------------------------------------------------- fixtures --

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// jpegZIP builds an archive whose entries are all tiny JPEGs, with the UTF-8
// flag set. It is the "ordinary volume" of the collection.
func jpegZIP(t testing.TB, names ...string) []byte {
	t.Helper()
	entries := make([]testutil.Entry, 0, len(names))
	for _, n := range names {
		entries = append(entries, testutil.Entry{
			Name: n, Data: testutil.TinyJPEG(t, 8, 12), Flags: testutil.FlagUTF8,
		})
	}
	return testutil.BuildZIP(t, testutil.ZIPSpec{Entries: entries})
}

func jpeg(t testing.TB) []byte { return testutil.TinyJPEG(t, 8, 12) }

// imageDir is a directory of loose images, i.e. prd §2.2's "sub-folder holding
// images" book.
func imageDir(t testing.TB, names ...string) map[string]any {
	t.Helper()
	out := map[string]any{}
	for _, n := range names {
		out[n] = testutil.TinyJPEG(t, 8, 12)
	}
	return out
}

// ------------------------------------------------------------------ harness --

// countingLister wraps the real source factory so a test can see exactly which
// books were opened and listed — which is the only honest way to assert that
// FR-IDX-003 skipped something.
type countingLister struct {
	inner BookLister

	mu       sync.Mutex
	opened   []string
	listed   []string
	panicOn  map[string]bool
	failWith map[string]error
}

func (c *countingLister) Open(ctx context.Context, b source.Book) (source.BookSource, error) {
	c.mu.Lock()
	c.opened = append(c.opened, b.RelPath)
	shouldPanic := c.panicOn[b.RelPath]
	failure := c.failWith[b.RelPath]
	c.mu.Unlock()
	if failure != nil {
		return nil, failure
	}
	src, err := c.inner.Open(ctx, b)
	if err != nil {
		return nil, err
	}
	return &countingSource{BookSource: src, owner: c, rel: b.RelPath, boom: shouldPanic}, nil
}

func (c *countingLister) listedPaths() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := append([]string(nil), c.listed...)
	sort.Strings(out)
	return out
}

func (c *countingLister) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.opened, c.listed = nil, nil
}

type countingSource struct {
	source.BookSource
	owner *countingLister
	rel   string
	boom  bool
}

func (c *countingSource) List(ctx context.Context) (*source.Listing, error) {
	c.owner.mu.Lock()
	c.owner.listed = append(c.owner.listed, c.rel)
	c.owner.mu.Unlock()
	if c.boom {
		panic("synthetic failure inside a per-book unit")
	}
	return c.BookSource.List(ctx)
}

// fakeCoverQueue records what the scanner enqueued (FR-THM-003).
type fakeCoverQueue struct {
	mu   sync.Mutex
	reqs []CoverRequest
	// onEnqueue, when set, runs inside EnqueueCover — i.e. at the exact instant
	// the scanner publishes the cover. It is how the enqueue *timing* is
	// observable at all; see the AfterCommit regression test below.
	onEnqueue func(CoverRequest)
}

func (q *fakeCoverQueue) EnqueueCover(_ context.Context, r CoverRequest) {
	q.mu.Lock()
	q.reqs = append(q.reqs, r)
	fn := q.onEnqueue
	q.mu.Unlock()
	if fn != nil {
		fn(r)
	}
}

func (q *fakeCoverQueue) all() []CoverRequest {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]CoverRequest(nil), q.reqs...)
}

// countingFile counts every byte the scanner reads out of a container, which is
// how the FR-IDX-002 test proves no payload was decompressed.
type countingFile struct {
	openpool.File
	read *atomic.Int64
}

func (f *countingFile) ReadAt(p []byte, off int64) (int, error) {
	n, err := f.File.ReadAt(p, off)
	f.read.Add(int64(n))
	return n, err
}

type harness struct {
	t        testing.TB
	idx      *index.DB
	ud       *userdata.DB
	dataDir  string
	rootDirs map[string]string
	cfgRoots []config.Root
	scanCfg  config.Scan
	covers   *fakeCoverQueue
	lister   *countingLister
	readMeta *atomic.Int64
	// clock overrides the scanner's clock. nil leaves it at time.Now; the
	// first-sighting tests set one and call build() again (see seen_test.go).
	clock func() time.Time

	pool    *openpool.Pool
	rootSet *source.RootSet
	scanner *Scanner
}

func newHarness(t testing.TB, layout map[string]any, tweak ...func(*config.Scan)) *harness {
	t.Helper()
	return newHarnessAt(t, map[string]string{"manga": testutil.BuildTree(t, layout)}, tweak...)
}

func newHarnessAt(t testing.TB, roots map[string]string, tweak ...func(*config.Scan)) *harness {
	t.Helper()
	ctx := t.Context()
	dir := t.TempDir()

	ud, err := userdata.Open(ctx, userdata.Options{
		Path: filepath.Join(dir, "user.db"), Logger: quietLogger(),
	})
	if err != nil {
		t.Fatalf("opening user.db: %v", err)
	}
	t.Cleanup(func() { _ = ud.Close() })

	idx, err := index.Open(ctx, index.Options{
		Path: filepath.Join(dir, "index.db"), UserPath: ud.Path(), Logger: quietLogger(),
	})
	if err != nil {
		t.Fatalf("opening index.db: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })

	h := &harness{
		t: t, idx: idx, ud: ud, dataDir: dir,
		rootDirs: roots,
		scanCfg:  config.Scan{MaxDepth: 3, CoverMaxLooseImages: 3},
		readMeta: &atomic.Int64{},
	}
	names := make([]string, 0, len(roots))
	for name := range roots {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		h.cfgRoots = append(h.cfgRoots, config.Root{Name: name, Path: roots[name], Enabled: true})
	}
	for _, fn := range tweak {
		fn(&h.scanCfg)
	}
	h.build()
	return h
}

// build wires a fresh RootSet, handle pool and Scanner over the harness's
// current roots. Calling it again rebinds the same index to a different set of
// directories, which is how the sweep tests move a library without ever writing
// to one (FR-CFG-005 forbids this package from deleting a fixture file).
func (h *harness) build() {
	h.t.Helper()
	ctx := h.t.Context()

	if h.pool != nil {
		_ = h.pool.Close()
	}
	if h.rootSet != nil {
		_ = h.rootSet.Close()
	}
	if h.scanner != nil {
		_ = h.scanner.Close()
	}

	rs, err := source.OpenRoots(ctx, h.cfgRoots, quietLogger())
	if err != nil {
		h.t.Fatalf("opening roots: %v", err)
	}
	h.rootSet = rs

	base := rs.PoolOpener()
	h.pool = openpool.New(openpool.Options{
		Logger: quietLogger(),
		Open: func(p string) (openpool.File, error) {
			f, err := base(p)
			if err != nil {
				return nil, err
			}
			return &countingFile{File: f, read: h.readMeta}, nil
		},
	})
	factory := source.NewFactory(source.Options{
		Roots: rs, Pool: h.pool, Logger: quietLogger(),
	})
	h.lister = &countingLister{
		inner:    factory,
		panicOn:  map[string]bool{},
		failWith: map[string]error{},
	}
	h.covers = &fakeCoverQueue{}

	sc, err := New(Options{
		Index: h.idx, Books: h.lister, Roots: rs,
		ConfigRoots: h.cfgRoots, Scan: h.scanCfg,
		Covers: h.covers, Seen: h.ud, Logger: quietLogger(),
		Now: h.clock,
	})
	if err != nil {
		h.t.Fatalf("constructing the scanner: %v", err)
	}
	h.scanner = sc
	h.t.Cleanup(func() {
		_ = sc.Close()
		_ = h.pool.Close()
		_ = rs.Close()
	})
}

func (h *harness) run(req Request) *Result {
	h.t.Helper()
	res, err := h.scanner.Run(h.t.Context(), req)
	if err != nil {
		h.t.Fatalf("scan: %v", err)
	}
	return res
}

// series returns every indexed series, natural-sorted, disabled roots included.
func (h *harness) series() []index.SeriesRow {
	h.t.Helper()
	list, err := h.idx.ListSeries(h.t.Context(), index.SeriesFilter{
		Status: "all", IncludeDisabledRoots: true, Limit: 200,
	})
	if err != nil {
		h.t.Fatalf("listing series: %v", err)
	}
	return list.Items
}

func (h *harness) seriesAt(root, rel string) index.SeriesDetail {
	h.t.Helper()
	d, err := h.idx.GetSeries(h.t.Context(), ids.SeriesID(root, rel))
	if err != nil {
		h.t.Fatalf("series %q of root %q: %v", rel, root, err)
	}
	return d
}

func (h *harness) books(root, rel string) []index.BookRow {
	h.t.Helper()
	return h.seriesAt(root, rel).Books
}

func (h *harness) pages(bookID string) []index.Page {
	h.t.Helper()
	p, err := h.idx.ListPages(h.t.Context(), bookID)
	if err != nil {
		h.t.Fatalf("listing pages of %q: %v", bookID, err)
	}
	return p
}

func (h *harness) logs() []index.LogEntry {
	h.t.Helper()
	entries, err := h.idx.ListLog(h.t.Context(), index.LogFilter{Limit: 1000})
	if err != nil {
		h.t.Fatalf("listing the scan log: %v", err)
	}
	return entries
}

func bookNames(books []index.BookRow) []string {
	out := make([]string, 0, len(books))
	for _, b := range books {
		out = append(out, b.DisplayName)
	}
	return out
}

func bookRels(books []index.BookRow) []string {
	out := make([]string, 0, len(books))
	for _, b := range books {
		out = append(out, b.RelPath)
	}
	return out
}

func pageNames(pages []index.Page) []string {
	out := make([]string, 0, len(pages))
	for _, p := range pages {
		out = append(out, p.Name)
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// snapshotTree records everything about a tree that a write would disturb.
type treeSnapshot map[string]string

func snapshotTree(t *testing.T, root string) treeSnapshot {
	t.Helper()
	out := treeSnapshot{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		out[rel] = fmt.Sprintf("dir=%t size=%d mode=%s mtime=%d",
			d.IsDir(), fi.Size(), fi.Mode(), fi.ModTime().UnixNano())
		return nil
	})
	if err != nil {
		t.Fatalf("snapshotting %q: %v", root, err)
	}
	return out
}

func (s treeSnapshot) diff(other treeSnapshot) []string {
	var out []string
	for k, v := range s {
		ov, ok := other[k]
		switch {
		case !ok:
			out = append(out, "removed: "+k)
		case ov != v:
			out = append(out, fmt.Sprintf("changed: %s (%s -> %s)", k, v, ov))
		}
	}
	for k := range other {
		if _, ok := s[k]; !ok {
			out = append(out, "created: "+k)
		}
	}
	sort.Strings(out)
	return out
}

// ------------------------------------------------------------------- tests --

// FR-IDX-001: a scan builds series, books and pages for every root.
func TestScan_coldRun_indexesEverySeriesBookAndPage(t *testing.T) {
	t.Parallel()
	h := newHarness(t, map[string]any{
		"[만화] Clover 클로버 (총4권)": map[string]any{
			"클로버 1권.zip": jpegZIP(t, "001.jpg", "002.jpg", "010.jpg"),
			"클로버 2권.zip": jpegZIP(t, "001.jpg", "002.jpg"),
		},
		"[만화] 바퀴.zip": jpegZIP(t, "01.jpg"),
	})

	res := h.run(Request{})
	series, books, pages, _, errs := res.Totals()
	if series != 2 || books != 3 || pages != 6 || errs != 0 {
		t.Fatalf("totals = series %d books %d pages %d errors %d; want 2/3/6/0", series, books, pages, errs)
	}

	got := h.series()
	if len(got) != 2 {
		t.Fatalf("indexed %d series, want 2", len(got))
	}
	clover := h.seriesAt("manga", "[만화] Clover 클로버 (총4권)")
	if clover.Kind != SeriesFolder || clover.BookCount != 2 || clover.PageCount != 5 {
		t.Errorf("clover = kind %q books %d pages %d; want folder/2/5",
			clover.Kind, clover.BookCount, clover.PageCount)
	}
	if got := pageNames(h.pages(clover.Books[0].ID)); !equalStrings(got, []string{"001.jpg", "002.jpg", "010.jpg"}) {
		t.Errorf("page order = %v; want natural order 001, 002, 010 (FR-IDX-007)", got)
	}

	// prd §2.2 row 4 and the domain separation of arch §3.4: a single-file
	// series is its own only book, and the two ids must differ.
	single := h.seriesAt("manga", "[만화] 바퀴.zip")
	if single.Kind != SeriesZIP || len(single.Books) != 1 {
		t.Fatalf("single-zip series = kind %q with %d books; want zip/1", single.Kind, len(single.Books))
	}
	if single.ID == single.Books[0].ID {
		t.Errorf("series id and book id collided at %q", single.ID)
	}
	if single.Books[0].ID != ids.BookID("manga", "[만화] 바퀴.zip") {
		t.Errorf("book id = %q; want the path-derived id (FR-CFG-004)", single.Books[0].ID)
	}
}

// FR-IDX-010, exercised over every pathological shape data-survey §7 records
// plus the two that only exist synthetically (D-3, D-4): the scan completes and
// each failure is reported individually.
func TestScan_errorIsolation_everyPathologicalCase_completesAndReportsEachFailure(t *testing.T) {
	t.Parallel()

	good := jpegZIP(t, "001.jpg", "002.jpg")
	truncated := testutil.BuildZIP(t, testutil.ZIPSpec{
		Entries:      []testutil.Entry{{Name: "001.jpg", Data: jpeg(t), Flags: testutil.FlagUTF8}},
		TruncateTail: 8,
	})
	noEOCD := testutil.BuildZIP(t, testutil.ZIPSpec{
		Entries:              []testutil.Entry{{Name: "001.jpg", Data: jpeg(t), Flags: testutil.FlagUTF8}},
		CorruptEOCDSignature: true,
	})
	encrypted := testutil.BuildZIP(t, testutil.ZIPSpec{
		Entries: []testutil.Entry{
			{Name: "001.jpg", Data: jpeg(t), Flags: testutil.FlagUTF8 | testutil.FlagEncrypted},
		},
	})
	// data-survey §7 "Embedded Sub-ZIP Architecture": a container of archives,
	// zero image entries. Decision D-10 requires 'empty', not an error.
	containerOfZips := testutil.BuildZIP(t, testutil.ZIPSpec{
		Entries: []testutil.Entry{{Name: "vol01.zip", Data: good, Flags: testutil.FlagUTF8}},
	})

	h := newHarness(t, map[string]any{
		"성한 시리즈": map[string]any{
			"01권.zip": good,
			"02권.zip": good,
		},
		"망가진 시리즈": map[string]any{
			"07권.zip":           truncated,
			"08권.zip":           noEOCD,
			"D.N.Angel 09권.zip": []byte{}, // the real 0-byte archive
			"암호화 10권.zip":       encrypted,
			"엔젤하트 전32권 완결.zip":  containerOfZips,
			"정상 11권.zip":        good,
		},
		// Ruling E-14: the same container of nested archives, alone in its own
		// series. The book is `empty`; the series must read `error`.
		"엔젤하트 시리즈": map[string]any{
			"엔젤하트 전32권 완결.zip": containerOfZips,
		},
		"텍스트만 있는 시리즈": map[string]any{
			"설명.txt": "이 폴더에는 이미지가 없습니다",
			"목록.hv3": "x",
		},
	})

	res := h.run(Request{})
	if res.Cancelled {
		t.Fatal("the scan reported itself cancelled")
	}
	if len(res.Roots) != 1 || res.Roots[0].Err != nil {
		t.Fatalf("root result = %+v; the scan must not abort (FR-IDX-010)", res.Roots)
	}
	// Three broken archives plus one encrypted one. The container of nested
	// archives is 'empty', which is a legitimate outcome (D-10) and must not
	// inflate the error count the settings screen shows.
	if got := res.Roots[0].Errors; got != 4 {
		t.Errorf("reported %d errors, want 4 — 'empty' is not a failure", got)
	}
	if st := h.scanner.Status(); st.Errors != 4 {
		t.Errorf("ScanStatus.errors = %d, want 4", st.Errors)
	}

	byRel := map[string]index.BookRow{}
	for _, b := range h.books("manga", "망가진 시리즈") {
		byRel[filepath.Base(b.RelPath)] = b
	}
	want := map[string]string{
		"07권.zip":           StatusError,
		"08권.zip":           StatusError,
		"D.N.Angel 09권.zip": StatusError,
		"암호화 10권.zip":       StatusEncrypted,
		"엔젤하트 전32권 완결.zip":  StatusEmpty,
		"정상 11권.zip":        StatusOK,
	}
	if len(byRel) != len(want) {
		t.Fatalf("indexed %d books, want %d: %v", len(byRel), len(want), bookRels(h.books("manga", "망가진 시리즈")))
	}
	for name, wantStatus := range want {
		b, ok := byRel[name]
		if !ok {
			t.Errorf("%s was not indexed at all", name)
			continue
		}
		if b.Status != wantStatus {
			t.Errorf("%s status = %q, want %q (error %q)", name, b.Status, wantStatus, b.Error)
		}
		if wantStatus != StatusOK && b.Error == "" {
			t.Errorf("%s has status %q with no reason recorded", name, b.Status)
		}
		if wantStatus != StatusOK && strings.Contains(b.Error, b.ID) {
			t.Errorf("%s error leaks the opaque book id into the UI message: %q", name, b.Error)
		}
	}

	// The healthy series next door is untouched: isolation, not collateral.
	if ok := h.seriesAt("manga", "성한 시리즈"); ok.Status != StatusOK || ok.PageCount != 4 {
		t.Errorf("healthy series = status %q pages %d; want ok/4", ok.Status, ok.PageCount)
	}
	// D-7: a directory of .txt files is listed as empty, never dropped.
	// `empty` survives ONLY for this row — a series with nothing *in* it.
	if txt := h.seriesAt("manga", "텍스트만 있는 시리즈"); txt.Status != StatusEmpty || txt.BookCount != 0 {
		t.Errorf("text-only series = status %q books %d; want empty/0", txt.Status, txt.BookCount)
	}
	// Ruling E-14 / D-10: one book, `empty`, so the reader cannot open a single
	// page — the SERIES is `error` with that book's reason, while the BOOK above
	// stays `empty` and is still not counted as a scan failure.
	angel := h.seriesAt("manga", "엔젤하트 시리즈")
	if angel.Status != StatusError {
		t.Errorf("a series whose only book is empty = status %q, want %q (E-14)",
			angel.Status, StatusError)
	}
	if angel.Error == "" {
		t.Errorf("series %q has status %q with no reason (arch §7.3: error is non-null whenever status != ok)",
			angel.DisplayName, angel.Status)
	}
	if angel.BookCount != 1 {
		t.Errorf("엔젤하트 시리즈 book_count = %d, want 1", angel.BookCount)
	}

	// FR-IDX-010 also asks for one scan_log warn row per isolated failure.
	warned := map[string]bool{}
	for _, e := range h.logs() {
		if e.Level == index.LevelWarn {
			warned[filepath.Base(e.RelPath)] = true
		}
	}
	for name, wantStatus := range want {
		if wantStatus == StatusOK {
			continue
		}
		if !warned[name] {
			t.Errorf("no scan_log warn row for %s", name)
		}
	}
}

// `series.total_bytes` is the 용량 the product shows (prd FR-LIB-003 list
// column, FR-LIB-009 volume metadata, UI-002 series header) and the key
// FR-LIB-004's `sort=size` orders by, so it has to be what the series occupies
// on disk.
//
// The regression: it used to be the sum of `books.total_bytes`, which arch §4.4
// defines as the sum of *uncompressed page* bytes. That is 0 for a PDF (pages
// are rendered, not stored) and 0 for any book with no readable pages, so whole
// series read `0 KB` — including the 1.44 GB 엔젤하트 container that impl-plan
// §6.3 step 6.2 requires to sort FIRST by 용량.
func TestScan_seriesTotalBytesIsTheOnDiskFootprint(t *testing.T) {
	t.Parallel()
	good := jpegZIP(t, "001.jpg", "002.jpg")
	// D-10's shape again: a container of archives, zero image entries, so every
	// page-derived total below it is zero.
	container := testutil.BuildZIP(t, testutil.ZIPSpec{
		Entries: []testutil.Entry{{Name: "vol01.zip", Data: good, Flags: testutil.FlagUTF8}},
	})
	h := newHarness(t, map[string]any{
		"압축 시리즈": map[string]any{
			"01권.zip": good,
			"02권.zip": good,
		},
		"폴더 시리즈": map[string]any{
			"01권": map[string]any{"001.jpg": jpeg(t), "002.jpg": jpeg(t)},
		},
		"페이지 없는 시리즈": map[string]any{
			"엔젤하트 전32권 완결.zip": container,
		},
	})
	h.run(Request{})

	onDisk := func(rel string) int64 {
		t.Helper()
		fi, err := os.Stat(filepath.Join(h.rootDirs["manga"], filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("stat %s: %v", rel, err)
		}
		return fi.Size()
	}

	// zip books: the container size, not the sum of the entries inside it.
	zips := onDisk("압축 시리즈/01권.zip") + onDisk("압축 시리즈/02권.zip")
	if got := h.seriesAt("manga", "압축 시리즈").TotalBytes; got != zips {
		t.Errorf("archive series total_bytes = %d, want %d (the two containers on disk)", got, zips)
	}

	// dir books: `file_size` is 0 by definition (arch §4.4), and there the page
	// bytes ARE the bytes on disk.
	dirs := onDisk("폴더 시리즈/01권/001.jpg") + onDisk("폴더 시리즈/01권/002.jpg")
	folder := h.seriesAt("manga", "폴더 시리즈")
	if len(folder.Books) != 1 || folder.Books[0].FileSize != 0 {
		t.Fatalf("expected one dir book with file_size 0, got %+v", folder.Books)
	}
	if folder.TotalBytes != dirs {
		t.Errorf("folder series total_bytes = %d, want %d (its images on disk)", folder.TotalBytes, dirs)
	}

	// The decisive case: a book whose uncompressed-page total is genuinely 0.
	empty := h.seriesAt("manga", "페이지 없는 시리즈")
	if len(empty.Books) != 1 || empty.Books[0].TotalBytes != 0 {
		t.Fatalf("expected one book with total_bytes 0, got %+v", empty.Books)
	}
	if want := onDisk("페이지 없는 시리즈/엔젤하트 전32권 완결.zip"); empty.TotalBytes != want {
		t.Errorf("page-less series total_bytes = %d, want %d — a series with no page rows still occupies its bytes",
			empty.TotalBytes, want)
	}
}

// FR-IDX-010's last line: a panic inside a per-book unit is recovered and
// becomes a status, not a dead process.
func TestScan_panicInsideAPerBookUnit_isRecoveredAsAnErrorBook(t *testing.T) {
	t.Parallel()
	h := newHarness(t, map[string]any{
		"시리즈": map[string]any{
			"01권.zip": jpegZIP(t, "001.jpg"),
			"02권.zip": jpegZIP(t, "001.jpg", "002.jpg"),
		},
	})
	h.lister.panicOn["시리즈/01권.zip"] = true

	res := h.run(Request{})
	if _, books, _, _, errs := res.Totals(); books != 2 || errs != 1 {
		t.Fatalf("totals = books %d errors %d; want 2/1", books, errs)
	}
	books := h.books("manga", "시리즈")
	if len(books) != 2 {
		t.Fatalf("indexed %d books, want 2", len(books))
	}
	if books[0].Status != StatusError || !strings.Contains(books[0].Error, "panic") {
		t.Errorf("panicking book = status %q error %q; want error/panic…", books[0].Status, books[0].Error)
	}
	if books[1].Status != StatusOK || books[1].PageCount != 2 {
		t.Errorf("neighbour = status %q pages %d; want ok/2 — a panic must not spread",
			books[1].Status, books[1].PageCount)
	}
}

// FR-IDX-002: indexing reads the central directory and nothing else. A scan that
// decompressed would read at least the payload; this archive's payload is two
// orders of magnitude bigger than the ceiling asserted here.
func TestScan_readsTheCentralDirectoryOnly_neverAnEntryPayload(t *testing.T) {
	t.Parallel()
	payload := make([]byte, 512*1024)
	for i := range payload {
		payload[i] = byte(i)
	}
	archive := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
		{Name: "001.jpg", Data: payload, Method: testutil.MethodStore, Flags: testutil.FlagUTF8},
		{Name: "002.jpg", Data: payload, Method: testutil.MethodStore, Flags: testutil.FlagUTF8},
	}})
	h := newHarness(t, map[string]any{"시리즈": map[string]any{"01권.zip": archive}})

	h.readMeta.Store(0)
	h.run(Request{})

	read := h.readMeta.Load()
	if read == 0 {
		t.Fatal("no bytes were read at all; the counter is not wired to the scan")
	}
	const ceiling = 64 * 1024
	if read > ceiling {
		t.Errorf("scan read %d bytes of a %d-byte archive; FR-IDX-002 allows the central directory only (<%d)",
			read, len(archive), ceiling)
	}
	if pages := h.pages(h.books("manga", "시리즈")[0].ID); len(pages) != 2 {
		t.Fatalf("indexed %d pages, want 2", len(pages))
	}
}

// FR-IDX-008 / AC-002 surfaced through the scanner: a flagless CP949 entry name
// reaches pages.name as readable Hangul.
func TestScan_flaglessCP949EntryNames_areDecodedIntoPageNames(t *testing.T) {
	t.Parallel()
	raw := testutil.CP949(t, "군계 001.jpg")
	archive := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
		{RawName: raw, Data: jpeg(t)}, // no FlagUTF8: this is the 2014-2018 shape
	}})
	h := newHarness(t, map[string]any{"[만화] 군계 1~25": map[string]any{"01권.zip": archive}})
	h.run(Request{})

	pages := h.pages(h.books("manga", "[만화] 군계 1~25")[0].ID)
	if len(pages) != 1 {
		t.Fatalf("indexed %d pages, want 1", len(pages))
	}
	if pages[0].Name != "군계 001.jpg" {
		t.Errorf("page name = %q, want %q", pages[0].Name, "군계 001.jpg")
	}
	if strings.ContainsRune(pages[0].Name, '�') {
		t.Errorf("page name contains U+FFFD: %q", pages[0].Name)
	}
}

// arch §4.9: rows the filesystem no longer has are deleted, and the ones it
// still has survive with byte-identical ids.
func TestScan_generationSweep_removesVanishedSeriesAndKeepsTheRest(t *testing.T) {
	t.Parallel()
	before := testutil.BuildTree(t, map[string]any{
		"머무는 시리즈": map[string]any{"01권.zip": jpegZIP(t, "001.jpg")},
		"사라질 시리즈": map[string]any{"01권.zip": jpegZIP(t, "001.jpg")},
	})
	h := newHarnessAt(t, map[string]string{"manga": before})
	h.run(Request{})
	if got := len(h.series()); got != 2 {
		t.Fatalf("first scan indexed %d series, want 2", got)
	}
	staying := h.seriesAt("manga", "머무는 시리즈").ID

	// Rebind the same root NAME to a directory that no longer holds the second
	// series. Ids hash (root name, rel path), never the absolute path, so this
	// is exactly "one series was deleted" as far as the index is concerned —
	// and it lets the test avoid a write primitive, which FR-CFG-005's lint
	// guard forbids anywhere in this package.
	after := testutil.BuildTree(t, map[string]any{
		"머무는 시리즈": map[string]any{"01권.zip": jpegZIP(t, "001.jpg")},
	})
	h.rootDirs["manga"] = after
	h.cfgRoots[0].Path = after
	h.build()

	res := h.run(Request{})
	if res.Roots[0].Swept.Series != 1 {
		t.Errorf("swept %d series, want 1 (%s)", res.Roots[0].Swept.Series, res.Roots[0].SweepNote)
	}
	got := h.series()
	if len(got) != 1 || got[0].ID != staying {
		t.Fatalf("after the sweep: %d series, first id %q; want only %q",
			len(got), func() string {
				if len(got) == 0 {
					return ""
				}
				return got[0].ID
			}(), staying)
	}
	if _, err := h.idx.GetSeries(h.t.Context(), ids.SeriesID("manga", "사라질 시리즈")); !errors.Is(err, index.ErrNotFound) {
		t.Errorf("the vanished series is still indexed (err=%v)", err)
	}
	if n, err := h.idx.CountOrphanPages(h.t.Context()); err != nil || n != 0 {
		t.Errorf("orphan pages = %d (err %v), want 0", n, err)
	}
}

// arch §4.9's hard rule: an unmounted drive must not silently erase a third of
// the library.
func TestScan_unreachableRoot_isRecordedAndNeverSwept(t *testing.T) {
	t.Parallel()
	live := testutil.BuildTree(t, map[string]any{
		"시리즈": map[string]any{"01권.zip": jpegZIP(t, "001.jpg")},
	})
	h := newHarnessAt(t, map[string]string{"manga": live})
	h.run(Request{})
	if got := len(h.series()); got != 1 {
		t.Fatalf("first scan indexed %d series, want 1", got)
	}

	// The drive goes away: the configured path no longer exists.
	h.cfgRoots[0].Path = filepath.Join(t.TempDir(), "unmounted")
	h.build()

	res := h.run(Request{})
	if len(res.Roots) != 1 || res.Roots[0].Err == nil {
		t.Fatalf("root result = %+v; want a recorded error", res.Roots)
	}
	if !errors.Is(res.Roots[0].Err, errRootUnreachable) {
		t.Errorf("root error = %v; want errRootUnreachable", res.Roots[0].Err)
	}
	if res.Roots[0].Swept != (index.SweepResult{}) {
		t.Errorf("an unreachable root was swept: %+v", res.Roots[0].Swept)
	}
	if got := len(h.series()); got != 1 {
		t.Fatalf("the library lost %d series to an unmounted drive", 1-got)
	}
	root, err := h.idx.GetRoot(h.t.Context(), "manga")
	if err != nil {
		t.Fatalf("reading the root row: %v", err)
	}
	if root.LastScanError == "" {
		t.Error("roots.last_scan_error is empty; arch §4.9 requires it to carry the failure")
	}
}

// FR-CFG-002: a disabled root keeps its rows — disabling hides a root, it never
// destroys the progress joined onto it.
func TestScan_disabledRoot_keepsItsRowsAndIsNotScanned(t *testing.T) {
	t.Parallel()
	h := newHarness(t, map[string]any{
		"시리즈": map[string]any{"01권.zip": jpegZIP(t, "001.jpg")},
	})
	h.run(Request{})
	if got := len(h.series()); got != 1 {
		t.Fatalf("first scan indexed %d series, want 1", got)
	}

	h.cfgRoots[0].Enabled = false
	h.build()
	h.lister.reset()

	res := h.run(Request{})
	if len(res.Roots) != 0 {
		t.Fatalf("a disabled root was scanned: %+v", res.Roots)
	}
	if got := h.lister.listedPaths(); len(got) != 0 {
		t.Errorf("a disabled root's books were read: %v", got)
	}
	if got := len(h.series()); got != 1 {
		t.Errorf("disabling a root deleted %d series", 1-got)
	}
	root, err := h.idx.GetRoot(h.t.Context(), "manga")
	if err != nil {
		t.Fatalf("reading the root row: %v", err)
	}
	if root.Enabled {
		t.Error("roots.enabled is still 1 after the root was disabled in the config")
	}
	// A named request for a disabled root is a no-op, not an error.
	if res := h.run(Request{Roots: []string{"manga"}}); len(res.Roots) != 0 {
		t.Errorf("an explicit request scanned a disabled root: %+v", res.Roots)
	}
	if _, err := h.scanner.Run(h.t.Context(), Request{Roots: []string{"nope"}}); !errors.Is(err, ErrUnknownRoot) {
		t.Errorf("unknown root error = %v; want ErrUnknownRoot", err)
	}
}

// arch §4.1: a cancelled scan commits what it has — and, because absence of a
// row is no longer evidence of absence on disk, deletes nothing.
func TestScan_cancel_commitsWhatItHasAndSweepsNothing(t *testing.T) {
	t.Parallel()
	before := testutil.BuildTree(t, map[string]any{
		"시리즈 a": map[string]any{"01권.zip": jpegZIP(t, "001.jpg")},
		"시리즈 b": map[string]any{"01권.zip": jpegZIP(t, "001.jpg")},
	})
	h := newHarnessAt(t, map[string]string{"manga": before})
	h.run(Request{})
	if got := len(h.series()); got != 2 {
		t.Fatalf("first scan indexed %d series, want 2", got)
	}

	// Rebind to an empty tree, then cancel before anything is walked. If a
	// cancelled run swept, both series would vanish.
	empty := testutil.BuildTree(t, map[string]any{"자리표시자.txt": "x"})
	h.rootDirs["manga"] = empty
	h.cfgRoots[0].Path = empty
	h.build()

	ctx, cancel := context.WithCancel(h.t.Context())
	cancel()
	res, err := h.scanner.Run(ctx, Request{})
	if err != nil {
		t.Fatalf("a cancelled scan must return cleanly, got %v", err)
	}
	if !res.Cancelled {
		t.Error("Result.Cancelled is false after a cancelled run")
	}
	for _, rr := range res.Roots {
		if rr.Swept != (index.SweepResult{}) {
			t.Errorf("a cancelled run swept %+v", rr.Swept)
		}
	}
	if got := len(h.series()); got != 2 {
		t.Fatalf("a cancelled scan destroyed %d series", 2-got)
	}
	if st := h.scanner.Status(); st.State != PhaseIdle || st.FinishedAt == nil {
		t.Errorf("status after cancel = state %q finished %v; want idle with a finish time", st.State, st.FinishedAt)
	}
}

// A targeted rescan (`POST /api/series/{sid}/rescan`) touches one series and
// never sweeps.
func TestScan_targetedSeries_rescansOnlyThatSeriesAndSweepsNothing(t *testing.T) {
	t.Parallel()
	h := newHarness(t, map[string]any{
		"시리즈 a": map[string]any{"01권.zip": jpegZIP(t, "001.jpg")},
		"시리즈 b": map[string]any{"01권.zip": jpegZIP(t, "001.jpg")},
	})
	h.run(Request{})
	h.lister.reset()

	res := h.run(Request{Full: true, Series: []SeriesRef{{Root: "manga", RelPath: "시리즈 a"}}})
	if got := h.lister.listedPaths(); !equalStrings(got, []string{"시리즈 a/01권.zip"}) {
		t.Errorf("a targeted rescan read %v; want only 시리즈 a/01권.zip", got)
	}
	if res.Roots[0].Swept != (index.SweepResult{}) {
		t.Errorf("a targeted run swept %+v", res.Roots[0].Swept)
	}
	if got := len(h.series()); got != 2 {
		t.Errorf("a targeted run left %d series, want 2", got)
	}
}

// FR-CFG-005 / NFR-DAT-002: the scanner never creates, modifies, moves or
// deletes anything under a root. This is the behavioural half of the guarantee;
// the `make lint` grep guard is the static half.
func TestScan_neverWritesAnythingUnderARoot(t *testing.T) {
	t.Parallel()
	h := newHarness(t, map[string]any{
		"폴더 시리즈": map[string]any{
			"01권.zip":   jpegZIP(t, "001.jpg"),
			"02권":       imageDir(t, "001.jpg", "002.jpg"),
			"cover.jpg": jpeg(t),
		},
		"단일.zip":  jpegZIP(t, "001.jpg"),
		"망가짐.zip": []byte{},
	})
	root := h.rootDirs["manga"]

	before := snapshotTree(t, root)
	h.run(Request{})
	h.run(Request{Full: true})
	after := snapshotTree(t, root)

	if diff := before.diff(after); len(diff) != 0 {
		t.Errorf("the scan modified the media volume:\n  %s", strings.Join(diff, "\n  "))
	}
}

// FR-IDX-005 + AC-006 + FR-STT-003: rebuilding the index from empty reproduces
// every id, and the reading progress in the physically separate user.db
// reattaches to the rebuilt rows.
func TestScan_rebuildIndex_reproducesIdsAndPreservesReadingProgress(t *testing.T) {
	t.Parallel()
	h := newHarness(t, map[string]any{
		"[만화] 군계 1~25": map[string]any{
			"군계(軍鷄) 01권.zip": jpegZIP(t, "001.jpg", "002.jpg", "003.jpg"),
		},
	})
	h.run(Request{})

	book := h.books("manga", "[만화] 군계 1~25")[0]
	if _, err := h.ud.PutProgress(h.t.Context(), userdata.ProgressUpdate{
		BookID: book.ID, SeriesID: book.SeriesID, RootName: "manga",
		BookPath: book.RelPath, Page: 2, PageCount: 3,
	}); err != nil {
		t.Fatalf("writing reading progress: %v", err)
	}

	// This is exactly what `--rebuild-index` does in process: drop every index
	// table, re-apply the schema, rescan. user.db is not part of the DDL.
	if err := h.idx.Reset(h.t.Context()); err != nil {
		t.Fatalf("resetting the index: %v", err)
	}
	if got := len(h.series()); got != 0 {
		t.Fatalf("the index still holds %d series after Reset", got)
	}

	h.run(Request{Full: true})

	rebuilt := h.books("manga", "[만화] 군계 1~25")[0]
	if rebuilt.ID != book.ID {
		t.Fatalf("book id changed across a rebuild: %q -> %q", book.ID, rebuilt.ID)
	}
	if rebuilt.Progress == nil {
		t.Fatal("reading progress did not survive the rebuild (AC-006)")
	}
	if rebuilt.Progress.LastPage != 2 {
		t.Errorf("last_page = %d after the rebuild, want 2", rebuilt.Progress.LastPage)
	}
}

// FR-IDX-004: the snapshot carries the target count, the completed count, the
// current item, start and end times and a per-root breakdown, and it is safe to
// read while the scan runs.
func TestScan_progressSnapshot_reportsCountsCurrentItemAndTimes(t *testing.T) {
	t.Parallel()
	layout := map[string]any{}
	for i := range 24 {
		layout[fmt.Sprintf("시리즈 %02d", i)] = map[string]any{
			"01권.zip": jpegZIP(t, "001.jpg", "002.jpg"),
		}
	}
	h := newHarness(t, layout)

	if idle := h.scanner.Status(); idle.State != PhaseIdle || idle.StartedAt != nil {
		t.Fatalf("status before any scan = %+v; want idle with no start time", idle)
	}

	stop := make(chan struct{})
	var polls atomic.Int64
	var sawItem atomic.Bool
	go func() {
		defer close(stop)
		for {
			st := h.scanner.Status()
			polls.Add(1)
			if st.CurrentItem != "" {
				sawItem.Store(true)
			}
			if st.State == PhaseIdle && st.FinishedAt != nil {
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()

	res := h.run(Request{})
	<-stop

	if polls.Load() < 2 {
		t.Fatalf("the poller ran %d times; it is not exercising the concurrent read", polls.Load())
	}
	st := h.scanner.Status()
	if st.State != PhaseIdle {
		t.Errorf("final state = %q, want idle", st.State)
	}
	if st.StartedAt == nil || st.FinishedAt == nil {
		t.Fatalf("final snapshot has start %v finish %v; both are required", st.StartedAt, st.FinishedAt)
	}
	if *st.FinishedAt < *st.StartedAt {
		t.Errorf("finished_at %d precedes started_at %d", *st.FinishedAt, *st.StartedAt)
	}
	if st.Total != 24 || st.Done != 24 {
		t.Errorf("total/done = %d/%d, want 24/24", st.Total, st.Done)
	}
	if st.RunID != res.RunID || st.RunID == "" {
		t.Errorf("snapshot run id %q, result run id %q", st.RunID, res.RunID)
	}
	if !equalStrings(st.Roots, []string{"manga"}) {
		t.Errorf("snapshot roots = %v, want [manga]", st.Roots)
	}
	if len(st.PerRoot) != 1 || st.PerRoot[0].Name != "manga" || st.PerRoot[0].Series != 24 {
		t.Errorf("per-root breakdown = %+v; want one entry for manga with 24 series", st.PerRoot)
	}
	if !st.PerRoot[0].Done {
		t.Error("the per-root breakdown never marked the root finished")
	}
	if !sawItem.Load() {
		t.Error("current_item was never populated during the run (FR-IDX-004)")
	}
}

// arch §7.10: a second scan while one runs is a conflict, not a second scan.
func TestScan_whileOneIsRunning_returnsErrBusy(t *testing.T) {
	t.Parallel()
	h := newHarness(t, map[string]any{
		"시리즈": map[string]any{"01권.zip": jpegZIP(t, "001.jpg")},
	})

	release := make(chan struct{})
	h.lister.inner = blockingLister{inner: h.lister.inner, gate: release}

	runID, err := h.scanner.Start(h.t.Context(), Request{})
	if err != nil {
		t.Fatalf("starting the scan: %v", err)
	}
	if runID == "" {
		t.Fatal("Start returned an empty run id")
	}
	// Wait until the run has actually taken the permit.
	deadline := time.Now().Add(5 * time.Second)
	for h.scanner.Status().State == PhaseIdle {
		if time.Now().After(deadline) {
			t.Fatal("the background scan never left the idle state")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := h.scanner.Run(h.t.Context(), Request{}); !errors.Is(err, ErrBusy) {
		t.Errorf("second Run error = %v, want ErrBusy", err)
	}
	if _, err := h.scanner.Start(h.t.Context(), Request{}); !errors.Is(err, ErrBusy) {
		t.Errorf("second Start error = %v, want ErrBusy", err)
	}
	close(release)
	h.scanner.Wait()
	if got := len(h.series()); got != 1 {
		t.Errorf("the background scan indexed %d series, want 1", got)
	}
}

// Start must publish this run's status before it returns, or the whole run can
// be invisible.
//
// `POST /api/scan` answers 202 the moment Start returns; the client invalidates
// ['scan','status'] and polls once. Its refetchInterval returns false while the
// snapshot says idle, refetchOnWindowFocus is off, and the query is mounted at
// the router root — so a poll that lands on the *previous* run's idle snapshot
// is the last poll of the run. The UI then reads `스캔 대기` for the entire scan
// and there is no second chance. Start returning before progress.begin left
// exactly that window open.
//
// The loop is the measurement rather than decoration: the window was a goroutine
// scheduling delay, so one sample proves nothing about whether it is closed.
func TestScan_start_publishesThisRunsStatusBeforeItReturns(t *testing.T) {
	t.Parallel()
	h := newHarness(t, map[string]any{
		"시리즈": map[string]any{"01권.zip": jpegZIP(t, "001.jpg")},
	})

	// The run cannot finish while the gate is shut, so an idle snapshot after
	// Start can only be the stale one.
	release := make(chan struct{})
	defer close(release)
	h.lister.inner = blockingLister{inner: h.lister.inner, gate: release}

	for i := range 64 {
		runID, err := h.scanner.Start(h.t.Context(), Request{})
		if err != nil {
			t.Fatalf("iteration %d: Start: %v", i, err)
		}
		// Exactly what a client polling on the 202 sees.
		st := h.scanner.Status()
		if st.RunID != runID || st.State == PhaseIdle {
			t.Fatalf("iteration %d: Start returned with the published snapshot still %s/%q; "+
				"want run %q in a non-idle state — a client polling on the 202 stops polling "+
				"on idle and would show nothing for the whole run",
				i, st.State, st.RunID, runID)
		}
		h.scanner.Cancel()
		h.scanner.Wait()
	}
}

// A run that cannot be started publishes nothing: Start reports the bad request
// to its caller instead of leaving a status that never finishes behind.
func TestScan_start_unknownRoot_isReportedToTheCallerAndPublishesNothing(t *testing.T) {
	t.Parallel()
	h := newHarness(t, map[string]any{
		"시리즈": map[string]any{"01권.zip": jpegZIP(t, "001.jpg")},
	})
	h.run(Request{}) // so there is a finished run to be confused with

	before := h.scanner.Status()
	if _, err := h.scanner.Start(h.t.Context(), Request{Roots: []string{"nope"}}); !errors.Is(err, ErrUnknownRoot) {
		// httpapi.scanStartError has always mapped this to `400 bad_param`; before
		// the run id was published synchronously, Start could only log it.
		t.Fatalf("Start with an unknown root = %v, want ErrUnknownRoot", err)
	}
	after := h.scanner.Status()
	if after.State != PhaseIdle || after.RunID != before.RunID {
		t.Errorf("a refused Start moved the status to %s/%q from %s/%q",
			after.State, after.RunID, before.State, before.RunID)
	}
	// And the permit is free again.
	if _, err := h.scanner.Run(h.t.Context(), Request{}); err != nil {
		t.Errorf("a scan after a refused Start: %v", err)
	}
}

type blockingLister struct {
	inner BookLister
	gate  chan struct{}
}

func (b blockingLister) Open(ctx context.Context, bk source.Book) (source.BookSource, error) {
	select {
	case <-b.gate:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return b.inner.Open(ctx, bk)
}

// FR-THM-003 + arch §4.10: the cover ladder, rung by rung.
func TestScan_coverLadder_walksTheArchFourRungsInOrder(t *testing.T) {
	t.Parallel()
	h := newHarness(t, map[string]any{
		// Rung 1: a named cover file wins over everything, including the four
		// other loose images that would otherwise be a "(loose pages)" book.
		"명시적 커버": map[string]any{
			"01권.zip":               jpegZIP(t, "001.jpg"),
			"강철의 연금술사 00 Cover.jpg": jpeg(t),
			"a.jpg":                 jpeg(t),
			"b.jpg":                 jpeg(t),
			"c.jpg":                 jpeg(t),
			"d.jpg":                 jpeg(t),
		},
		// Rung 2: the D-5 shape — N archives plus exactly one unnamed image.
		"단일 후보": map[string]any{
			"01권.zip": jpegZIP(t, "001.jpg"),
			"표지.jpg":  jpeg(t),
		},
		// Rung 3: no loose image at all, so page 1 of the first ok book.
		"페이지 커버": map[string]any{
			"01권.zip": jpegZIP(t, "001.jpg", "002.jpg"),
		},
		// Rung 3 again, but the first volume is broken: the cover comes from the
		// first book that is actually readable.
		"첫권 손상": map[string]any{
			"01권.zip": []byte{},
			"02권.zip": jpegZIP(t, "001.jpg"),
		},
		// Rung 4: nothing to show. FR-LIB-008's placeholder is the frontend's
		// job; the API must not fabricate an image.
		"커버 없음": map[string]any{"설명.txt": "x"},
	})
	h.run(Request{})

	type want struct {
		kind    string
		relPath string
		bookRel string
	}
	cases := map[string]want{
		"명시적 커버": {kind: CoverFile, relPath: "명시적 커버/강철의 연금술사 00 Cover.jpg"},
		"단일 후보":  {kind: CoverFile, relPath: "단일 후보/표지.jpg"},
		"페이지 커버": {kind: CoverPage, bookRel: "페이지 커버/01권.zip"},
		"첫권 손상":  {kind: CoverPage, bookRel: "첫권 손상/02권.zip"},
		"커버 없음":  {kind: ""},
	}
	for rel, w := range cases {
		s := h.seriesAt("manga", rel)
		if s.CoverKind != w.kind {
			t.Errorf("%s cover_kind = %q, want %q", rel, s.CoverKind, w.kind)
			continue
		}
		switch w.kind {
		case CoverFile:
			if s.CoverRelPath != w.relPath {
				t.Errorf("%s cover_rel_path = %q, want %q", rel, s.CoverRelPath, w.relPath)
			}
		case CoverPage:
			if s.CoverBookID != ids.BookID("manga", w.bookRel) || s.CoverPageNo != 1 {
				t.Errorf("%s cover = book %q page %d, want %q page 1",
					rel, s.CoverBookID, s.CoverPageNo, w.bookRel)
			}
		}
	}

	// FR-THM-003: they are enqueued during the scan, one per series that has one.
	enq := map[string]CoverRequest{}
	for _, r := range h.covers.all() {
		enq[r.SeriesRelPath] = r
	}
	if len(enq) != 4 {
		t.Errorf("enqueued %d covers, want 4 (the fifth series has none): %v", len(enq), enq)
	}
	if r, ok := enq["페이지 커버"]; !ok || r.Kind != CoverPage || r.ContentVersion == "" {
		t.Errorf("page cover request = %+v; want a page request carrying a content_version", r)
	}
	if st := h.scanner.Status(); st.CoversTotal != 4 {
		t.Errorf("status covers_total = %d, want 4", st.CoversTotal)
	}
}

// FR-THM-003, and the reason writeSeries defers the enqueue to
// index.Writer.AfterCommit.
//
// A `page` cover names a (book_id, page_no) that internal/thumbs resolves by
// reading `books` and `pages` back — through the *reader* pool, never the write
// connection. Enqueued from inside the still-open scan batch those rows do not
// exist there, so every page cover failed with `thumbs: no such book, page or
// cover file`, once per configured width, and the worker does not retry:
// covers came back only when a browser asked for them one by one. Only the
// loose-file rung survived, because it needs no lookup — which is exactly the
// shape the E2E round-2 run measured (8 cached files = the 2 file-cover series
// × 4 widths, 28 WARN rows for the other 7).
//
// This test does at enqueue time what the worker does later, so the window is
// closed rather than made narrower.
func TestScan_coverEnqueue_seesTheRowsItNames(t *testing.T) {
	t.Parallel()
	h := newHarness(t, map[string]any{
		// Three page-cover series: the rung that needs the index.
		"페이지 커버 1": map[string]any{"01권.zip": jpegZIP(t, "001.jpg", "002.jpg")},
		"페이지 커버 2": map[string]any{"01권.zip": jpegZIP(t, "001.jpg")},
		"페이지 커버 3": map[string]any{
			"01권.zip": []byte{}, // broken: the cover comes from the second book
			"02권.zip": jpegZIP(t, "001.jpg"),
		},
		// One file-cover series, which needs no lookup and must keep working.
		"파일 커버": map[string]any{
			"01권.zip":   jpegZIP(t, "001.jpg"),
			"cover.jpg": jpeg(t),
		},
	})

	ctx := t.Context()
	var (
		mu       sync.Mutex
		resolved int
		failures []string
	)
	h.covers.onEnqueue = func(r CoverRequest) {
		if r.Kind != CoverPage {
			return
		}
		var bad []string
		if _, err := h.idx.GetBook(ctx, r.BookID); err != nil {
			bad = append(bad, fmt.Sprintf("GetBook: %v", err))
		}
		if _, err := h.idx.GetPage(ctx, r.BookID, r.PageNo); err != nil {
			bad = append(bad, fmt.Sprintf("GetPage: %v", err))
		}
		mu.Lock()
		resolved++
		if len(bad) > 0 {
			failures = append(failures, fmt.Sprintf("%s → %s", r.SeriesRelPath, strings.Join(bad, "; ")))
		}
		mu.Unlock()
	}

	h.run(Request{})

	mu.Lock()
	defer mu.Unlock()
	// Without this the test would pass vacuously if the enqueue disappeared.
	if resolved != 3 {
		t.Fatalf("resolved %d page covers, want 3", resolved)
	}
	if len(failures) > 0 {
		t.Errorf("%d of 3 page covers were enqueued before their rows were readable:\n  %s",
			len(failures), strings.Join(failures, "\n  "))
	}
	// The whole scan is one batch here (default 200 books / 2 s), so every cover
	// must still have been published by the time Run returned — deferring must
	// not mean dropping.
	if got := len(h.covers.all()); got != 4 {
		t.Errorf("enqueued %d covers by the end of the scan, want 4", got)
	}
	if st := h.scanner.Status(); st.CoversTotal != 4 {
		t.Errorf("status covers_total = %d, want 4", st.CoversTotal)
	}
}

// The natural sort of FR-IDX-007 materialised into books.ord, so the API never
// re-sorts (arch §3.5).
func TestScan_bookOrder_isNaturalAndMaterialisedIntoOrd(t *testing.T) {
	t.Parallel()
	h := newHarness(t, map[string]any{
		"[만화] 자살도114-122": map[string]any{
			"10권.zip":     jpegZIP(t, "1.jpg", "10.jpg", "100.jpg", "2.jpg"),
			"2권.zip":      jpegZIP(t, "001.jpg"),
			"1권.zip":      jpegZIP(t, "001.jpg"),
			"25권.zip":     jpegZIP(t, "001.jpg"),
			"01권 (완).zip": jpegZIP(t, "001.jpg"),
		},
	})
	h.run(Request{})

	books := h.books("manga", "[만화] 자살도114-122")
	want := []string{"1권.zip", "01권 (완).zip", "2권.zip", "10권.zip", "25권.zip"}
	if got := bookNames(books); !equalStrings(got, want) {
		t.Errorf("book order = %v, want %v", got, want)
	}
	for i, b := range books {
		if b.Ord != i {
			t.Errorf("book %q has ord %d at position %d", b.DisplayName, b.Ord, i)
		}
	}
	pages := pageNames(h.pages(books[3].ID))
	if !equalStrings(pages, []string{"1.jpg", "2.jpg", "10.jpg", "100.jpg"}) {
		t.Errorf("page order = %v; mixed zero-padding must still sort naturally (FR-IDX-007)", pages)
	}
}
