package thumbs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"shelf/internal/ids"
	"shelf/internal/index"
	"shelf/internal/openpool"
	"shelf/internal/source"
	"shelf/internal/testutil"
	"shelf/internal/userdata"
)

// ---------------------------------------------------------------------------
// Fixture
//
// One root, one series, two books and a loose cover file — the smallest shape
// that exercises every branch this package has: a ZIP book (98.7 % of the
// reference collection), a directory book (FR-SRV-005), a landscape page
// (FR-VWR-004) and a `cover_kind='file'` cover (arch §4.10 step 1).
// ---------------------------------------------------------------------------

const (
	testRoot     = "media"
	zipRelPath   = "series/vol01.zip"
	dirRelPath   = "series/vol02"
	coverRelPath = "series/[cover].jpg"
)

// pageGeometry is the fixture's page sizes. Page 2 is deliberately landscape:
// FR-VWR-004 auto-splits a page with w > h in spread mode, and that decision is
// made from the dimensions this package records.
var pageGeometry = [][2]int{
	{320, 480},
	{800, 400},
	{64, 96},
}

type harness struct {
	t        testing.TB
	rootPath string
	cacheDir string
	idx      *index.DB
	roots    *source.RootSet
	pool     *openpool.Pool
	factory  *source.Factory
	svc      *Service

	seriesID string
	zipBook  string
	dirBook  string
	zipCV    string
	dirCV    string
	coverCV  string
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// newHarness builds the fixture and returns a running Service. opts is applied
// on top of the defaults, so a test tunes only what it cares about.
func newHarness(t testing.TB, tune func(*Options)) *harness {
	t.Helper()
	ctx := t.Context()

	base := t.TempDir()
	h := &harness{
		t:        t,
		rootPath: filepath.Join(base, "media"),
		cacheDir: filepath.Join(base, "cache"),
	}
	mustMkdir(t, filepath.Join(h.rootPath, "series"))
	mustMkdir(t, filepath.Join(h.rootPath, dirRelPath))

	// The ZIP book: three deflated JPEGs, which is what a real 권 looks like.
	entries := make([]testutil.Entry, 0, len(pageGeometry))
	for i, g := range pageGeometry {
		entries = append(entries, testutil.Entry{
			Name:   fmt.Sprintf("%03d.jpg", i+1),
			Data:   testutil.TinyJPEG(t, g[0], g[1]),
			Method: testutil.MethodDeflate,
		})
	}
	writeFile(t, filepath.Join(h.rootPath, zipRelPath), testutil.BuildZIP(t, testutil.ZIPSpec{Entries: entries}))

	// The directory book (FR-SRV-005) and the loose cover file (arch §4.10).
	for i, g := range pageGeometry {
		writeFile(t, filepath.Join(h.rootPath, dirRelPath, fmt.Sprintf("%03d.jpg", i+1)), testutil.TinyJPEG(t, g[0], g[1]))
	}
	writeFile(t, filepath.Join(h.rootPath, coverRelPath), testutil.TinyJPEG(t, 600, 900))

	// Storage.
	ud, err := userdata.Open(ctx, userdata.Options{Path: filepath.Join(base, "user.db"), Logger: quietLogger()})
	if err != nil {
		t.Fatalf("userdata.Open: %v", err)
	}
	t.Cleanup(func() { _ = ud.Close() })
	h.idx, err = index.Open(ctx, index.Options{
		Path:     filepath.Join(base, "index.db"),
		UserPath: filepath.Join(base, "user.db"),
		Logger:   quietLogger(),
	})
	if err != nil {
		t.Fatalf("index.Open: %v", err)
	}
	t.Cleanup(func() { _ = h.idx.Close() })

	// Media access, exactly as the composition root wires it.
	h.roots, err = source.NewRootSet(ctx, map[string]string{testRoot: h.rootPath}, quietLogger())
	if err != nil {
		t.Fatalf("source.NewRootSet: %v", err)
	}
	t.Cleanup(func() { _ = h.roots.Close() })
	h.pool = openpool.New(openpool.Options{Open: h.roots.PoolOpener(), Logger: quietLogger()})
	t.Cleanup(func() { _ = h.pool.Close() })
	h.factory = source.NewFactory(source.Options{Roots: h.roots, Pool: h.pool, Logger: quietLogger()})

	h.seriesID = ids.SeriesID(testRoot, "series")
	h.zipBook = ids.BookID(testRoot, zipRelPath)
	h.dirBook = ids.BookID(testRoot, dirRelPath)
	h.zipCV = contentVersion(t, "zip", filepath.Join(h.rootPath, zipRelPath))
	h.dirCV = contentVersion(t, "dir", filepath.Join(h.rootPath, dirRelPath))
	h.coverCV = contentVersion(t, "file", filepath.Join(h.rootPath, coverRelPath))
	h.seedIndex(ctx)

	opts := Options{
		CacheDir:    h.cacheDir,
		Index:       h.idx,
		Sources:     h.factory,
		Roots:       h.roots,
		Workers:     2,
		AVIFEnabled: true,
		Logger:      quietLogger(),
	}
	if tune != nil {
		tune(&opts)
	}
	h.svc, err = New(ctx, opts)
	if err != nil {
		t.Fatalf("thumbs.New: %v", err)
	}
	t.Cleanup(func() { _ = h.svc.Close() })
	return h
}

// seedIndex writes the rows a scan would have written. The page rows come from
// the real source listing rather than from hand-computed offsets, so a test
// exercises the same local-header offsets serving does.
func (h *harness) seedIndex(ctx context.Context) {
	h.t.Helper()
	if err := h.idx.UpsertRoot(ctx, index.Root{
		Name: testRoot, Path: h.rootPath, Label: testRoot, Enabled: true,
	}); err != nil {
		h.t.Fatalf("UpsertRoot: %v", err)
	}

	w := h.idx.Writer(index.WriterOptions{})
	defer func() { _ = w.Close() }()

	if err := w.UpsertSeries(ctx, index.Series{
		ID: h.seriesID, RootName: testRoot, RelPath: "series", DisplayName: "series",
		SortKey: []byte("series"), SearchKey: "series", ChoseongKey: "", Kind: "folder",
		Mtime: 1, AddedAt: 1, Status: "ok", ScanGen: 1,
		CoverKind: "file", CoverRelPath: coverRelPath,
	}); err != nil {
		h.t.Fatalf("UpsertSeries: %v", err)
	}

	for i, b := range []struct {
		id   string
		rel  string
		kind source.Kind
		cv   string
	}{
		{h.zipBook, zipRelPath, source.KindZIP, h.zipCV},
		{h.dirBook, dirRelPath, source.KindDir, h.dirCV},
	} {
		fi, err := os.Stat(filepath.Join(h.rootPath, filepath.FromSlash(b.rel)))
		if err != nil {
			h.t.Fatalf("stat %s: %v", b.rel, err)
		}
		book := source.Book{
			ID: b.id, Kind: b.kind, RootName: testRoot, RelPath: b.rel,
			FileSize: fi.Size(), FileMtime: fi.ModTime().Unix(),
		}
		if b.kind == source.KindDir {
			book.FileSize, book.FileMtime = 0, 0
		}
		bs, err := h.factory.Open(ctx, book)
		if err != nil {
			h.t.Fatalf("opening %s: %v", b.rel, err)
		}
		listing, err := bs.List(ctx)
		if err != nil {
			h.t.Fatalf("listing %s: %v", b.rel, err)
		}
		_ = bs.Close()

		if err := w.UpsertBook(ctx, index.Book{
			ID: b.id, SeriesID: h.seriesID, RootName: testRoot, RelPath: b.rel,
			DisplayName: filepath.Base(b.rel), SortKey: []byte(b.rel), Ord: i,
			Kind: string(b.kind), PageCount: int64(len(listing.Pages)),
			TotalBytes: listing.TotalBytes, FileSize: book.FileSize, FileMtime: book.FileMtime,
			ContentVersion: b.cv, DimsState: "none", Status: "ok", ScanGen: 1,
		}); err != nil {
			h.t.Fatalf("UpsertBook: %v", err)
		}
		rows := make([]index.Page, 0, len(listing.Pages))
		for _, p := range listing.Pages {
			rows = append(rows, index.Page{
				BookID: b.id, PageNo: p.No, Name: p.Name, EntryPath: p.EntryPath, Ext: p.Ext,
				Size: p.Size, CompSize: p.CompSize, Method: int(p.Method),
				LocalHdrOff: p.LocalHdrOff, CRC32: p.CRC32, Mtime: p.Mtime,
			})
		}
		if err := w.ReplacePages(ctx, b.id, rows); err != nil {
			h.t.Fatalf("ReplacePages: %v", err)
		}
	}
	if err := w.Flush(ctx); err != nil {
		h.t.Fatalf("Flush: %v", err)
	}
}

// pageReq is the ordinary page-thumbnail request for the ZIP book.
func (h *harness) pageReq(pageNo, width int) Request {
	return Request{ID: h.zipBook, PageNo: pageNo, Width: width, ContentVersion: h.zipCV}
}

// coverReq is the loose-file cover request of arch §4.10 step 1.
func (h *harness) coverReq(width int) Request {
	return Request{
		ID: h.seriesID, Width: width, ContentVersion: h.coverCV,
		RootName: testRoot, RelPath: coverRelPath, Priority: PriorityCover,
	}
}

// drain waits for the workers, failing the test rather than hanging forever.
func (h *harness) drain() {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(h.t.Context(), 30*time.Second)
	defer cancel()
	if err := h.svc.Drain(ctx); err != nil {
		h.t.Fatalf("Drain: %v", err)
	}
}

// getReady polls Get until it stops answering ErrQueued.
func (h *harness) getReady(req Request) Result {
	h.t.Helper()
	res, err := h.svc.Get(h.t.Context(), req)
	if err == nil {
		return res
	}
	if !errors.Is(err, ErrQueued) {
		h.t.Fatalf("Get: %v", err)
	}
	h.drain()
	res, err = h.svc.Get(h.t.Context(), req)
	if err != nil {
		h.t.Fatalf("Get after Drain: %v", err)
	}
	return res
}

// fakeClock drives the two time-based behaviours — the negative memo (10 min)
// and the usage-walk memo (60 s) — without a sleep.
type fakeClock struct {
	mu sync.Mutex
	at time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.at.IsZero() {
		c.at = time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	}
	return c.at
}

func (c *fakeClock) advance(d time.Duration) {
	now := c.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = now.Add(d)
}

func mustMkdir(t testing.TB, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
}

func writeFile(t testing.TB, path string, data []byte) {
	t.Helper()
	mustMkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// contentVersion reproduces arch §5.3's books.content_version — the first 16
// hex characters of SHA-256(kind ‖ 0 ‖ size ‖ 0 ‖ mtime).
//
// It is written out here rather than imported because the scanner (WP-08) owns
// the production copy; what these tests need is proof that a value derived from
// (size, mtime) reaches the cache key, which is FR-THM-006.
func contentVersion(t testing.TB, kind, path string) string {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	sum := sha256.Sum256([]byte(kind + "\x00" +
		strconv.FormatInt(fi.Size(), 10) + "\x00" +
		strconv.FormatInt(fi.ModTime().Unix(), 10)))
	return hex.EncodeToString(sum[:])[:16]
}

// decodeJPEG reads a published thumbnail and returns its geometry, failing on
// anything that is not a complete JPEG.
func decodeJPEG(t testing.TB, path string) image.Config {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("opening thumbnail: %v", err)
	}
	defer func() { _ = f.Close() }()
	cfg, format, err := image.DecodeConfig(f)
	if err != nil {
		t.Fatalf("decoding thumbnail %s: %v", path, err)
	}
	if format != "jpeg" {
		t.Fatalf("thumbnail format = %q, want jpeg (CON-003)", format)
	}
	return cfg
}

// ---------------------------------------------------------------------------
// FR-THM-001 / FR-THM-002 — the cache layout
// ---------------------------------------------------------------------------

// The path is the one thing in this package that is a compatibility surface: a
// change to it silently orphans every cached file. It is asserted against the
// arch §5.6 hash input rebuilt here from literals, not against ids.ThumbKey, so
// the two implementations have to keep agreeing.
func TestCachePath_isTheArchSpecHash_withTwoLevelFanout(t *testing.T) {
	t.Parallel()
	c, err := newCache(filepath.Join(t.TempDir(), "cache"), "jpeg", 82)
	if err != nil {
		t.Fatalf("newCache: %v", err)
	}

	const (
		bookID = "yvtfrny77ehkt2we"
		pageNo = 7
		width  = 240
		cv     = "0123456789abcdef"
	)

	// "shelf-thumb/1" ‖ 0 ‖ book ‖ 0 ‖ page ‖ 0 ‖ width ‖ 0 ‖ format ‖ 0 ‖ quality ‖ 0 ‖ cv
	input := strings.Join([]string{
		"shelf-thumb/1", bookID, strconv.Itoa(pageNo), strconv.Itoa(width), "jpeg", "82", cv,
	}, "\x00")
	sum := sha256.Sum256([]byte(input))
	want := base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding).EncodeToString(sum[:10])

	got := c.key(bookID, pageNo, width, cv)
	if got != want {
		t.Fatalf("key = %q, want %q (arch §5.6 hash input)", got, want)
	}
	if len(got) != 16 {
		t.Fatalf("key length = %d, want 16", len(got))
	}

	wantPath := filepath.Join(c.dir, "thumbs", got[0:2], got[2:4], got+".jpg")
	if p := c.path(KindThumbs, got); p != wantPath {
		t.Fatalf("path = %q, want %q (FR-THM-002 ca/che/<hash>.jpg)", p, wantPath)
	}
}

// A rendered PDF page shares the scheme but not the domain, so a 640 px
// thumbnail and a 640 px render of the same page cannot collide (arch §5.6).
func TestPDFPagePath_usesTheOtherDomainAndDirectory(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)

	thumbKey := h.svc.cache.key(h.zipBook, 1, 640, h.zipCV)
	pdfKey, pdfPath := h.svc.PDFPagePath(h.zipBook, 1, 640, h.zipCV)
	if pdfKey == thumbKey {
		t.Fatal("pdf render and thumbnail share a cache key; the domain tag is missing")
	}
	wantPrefix := filepath.Join(h.cacheDir, "pdf") + string(filepath.Separator)
	if !strings.HasPrefix(pdfPath, wantPrefix) {
		t.Fatalf("pdf path = %q, want it under %q", pdfPath, wantPrefix)
	}

	if _, ok := h.svc.LookupPDFPage(h.zipBook, 1, 640, h.zipCV); ok {
		t.Fatal("LookupPDFPage reported a hit on an empty cache")
	}
	res, err := h.svc.StorePDFPage(h.zipBook, 1, 640, h.zipCV, testutil.TinyJPEG(t, 8, 8))
	if err != nil {
		t.Fatalf("StorePDFPage: %v", err)
	}
	if res.Path != pdfPath {
		t.Fatalf("stored at %q, want %q", res.Path, pdfPath)
	}
	if _, ok := h.svc.LookupPDFPage(h.zipBook, 1, 640, h.zipCV); !ok {
		t.Fatal("LookupPDFPage missed a page it had just stored")
	}
}

// FR-THM-001: the thumbnail is a file, in the cache directory, and nowhere else.
func TestGet_generatedThumbnail_livesUnderTheCacheDirectory(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	before := treeSnapshot(t, h.rootPath)

	res := h.getReady(h.pageReq(1, 240))
	if !strings.HasPrefix(res.Path, h.cacheDir+string(filepath.Separator)) {
		t.Fatalf("thumbnail at %q, want it under %q", res.Path, h.cacheDir)
	}
	if res.Size <= 0 {
		t.Fatalf("thumbnail size = %d", res.Size)
	}
	cfg := decodeJPEG(t, res.Path)
	if cfg.Width != 240 {
		t.Fatalf("thumbnail width = %d, want 240", cfg.Width)
	}
	// 320×480 scaled to 240 wide is 360 high; the aspect ratio must survive.
	if cfg.Height != 360 {
		t.Fatalf("thumbnail height = %d, want 360 (aspect ratio preserved)", cfg.Height)
	}

	// FR-CFG-005 / NFR-DAT-002: the media volume is untouched. The snapshot was
	// taken before the generation, so this compares the tree against itself
	// rather than against a wall clock.
	assertMediaUnchanged(t, h.rootPath, before)
}

// treeSnapshot records the size and mtime of everything under dir.
func treeSnapshot(t testing.TB, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(dir, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		out[path] = fmt.Sprintf("%d/%d", fi.Size(), fi.ModTime().UnixNano())
		return nil
	})
	if err != nil {
		t.Fatalf("walking the media root: %v", err)
	}
	return out
}

// assertMediaUnchanged is the unit-test half of FR-CFG-005: nothing under a
// media root may be created, removed or rewritten by anything this package
// does.
func assertMediaUnchanged(t testing.TB, dir string, before map[string]string) {
	t.Helper()
	after := treeSnapshot(t, dir)
	for path, was := range before {
		now, ok := after[path]
		if !ok {
			t.Errorf("media volume was written: %s disappeared", path)
			continue
		}
		if now != was {
			t.Errorf("media volume was written: %s changed from %s to %s", path, was, now)
		}
	}
	for path := range after {
		if _, ok := before[path]; !ok {
			t.Errorf("media volume was written: %s appeared", path)
		}
	}
}

// ---------------------------------------------------------------------------
// FR-THM-006 — invalidation is structural
// ---------------------------------------------------------------------------

// D-19: there is no invalidation code. Changing content_version alone changes
// the path, so a source file whose mtime moved cannot be served from the old
// rendering however hard anyone tries.
func TestCacheKey_contentVersionAlone_changesThePath(t *testing.T) {
	t.Parallel()
	c, err := newCache(filepath.Join(t.TempDir(), "cache"), "jpeg", 82)
	if err != nil {
		t.Fatalf("newCache: %v", err)
	}
	a := c.key("yvtfrny77ehkt2we", 1, 240, "aaaaaaaaaaaaaaaa")
	b := c.key("yvtfrny77ehkt2we", 1, 240, "bbbbbbbbbbbbbbbb")
	if a == b {
		t.Fatal("content_version is not a hash input: FR-THM-006 would need invalidation code")
	}
	if c.path(KindThumbs, a) == c.path(KindThumbs, b) {
		t.Fatal("two different keys resolved to one path")
	}
}

// Every other field of the arch §5.6 input matters too: the width because two
// sizes must not overwrite each other, the quality and the format because D-18
// makes a re-encode a pure cache-invalidation event.
func TestCacheKey_everyDocumentedFieldIsAnInput(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	base, err := newCache(dir, "jpeg", 82)
	if err != nil {
		t.Fatalf("newCache: %v", err)
	}
	other, err := newCache(dir, "jpeg", 70)
	if err != nil {
		t.Fatalf("newCache: %v", err)
	}
	ref := base.key("yvtfrny77ehkt2we", 1, 240, "cv")

	for name, got := range map[string]string{
		"book":    base.key("ruzwlotzngls2ua5", 1, 240, "cv"),
		"page":    base.key("yvtfrny77ehkt2we", 2, 240, "cv"),
		"width":   base.key("yvtfrny77ehkt2we", 1, 400, "cv"),
		"quality": other.key("yvtfrny77ehkt2we", 1, 240, "cv"),
		"cv":      base.key("yvtfrny77ehkt2we", 1, 240, "cv2"),
	} {
		if got == ref {
			t.Errorf("changing the %s left the cache key unchanged", name)
		}
	}
}

// The end-to-end form of FR-THM-006: touch the archive, and the thumbnail
// regenerates because the version the scanner derives from (size, mtime) is a
// different string.
func TestGet_sourceMtimeChanged_regeneratesUnderANewPath(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)

	before := h.getReady(h.pageReq(1, 240))

	zipPath := filepath.Join(h.rootPath, filepath.FromSlash(zipRelPath))
	newTime := time.Now().Add(48 * time.Hour)
	if err := os.Chtimes(zipPath, newTime, newTime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	changedCV := contentVersion(t, "zip", zipPath)
	if changedCV == h.zipCV {
		t.Fatal("the fixture's content version did not change; the test proves nothing")
	}

	req := h.pageReq(1, 240)
	req.ContentVersion = changedCV
	after := h.getReady(req)

	if after.Path == before.Path {
		t.Fatal("a changed source mtime reused the old cache path (FR-THM-006)")
	}
	if _, err := os.Stat(after.Path); err != nil {
		t.Fatalf("the new thumbnail was not written: %v", err)
	}
	// The superseded file is simply unreferenced; nothing deletes it eagerly,
	// which is why FR-THM-008's purge exists.
	if _, err := os.Stat(before.Path); err != nil {
		t.Fatalf("the superseded thumbnail vanished: %v", err)
	}
}

// ---------------------------------------------------------------------------
// FR-THM-003 / FR-THM-004 — the two queues
// ---------------------------------------------------------------------------

// Covers jump the queue however long the lazy backlog is (FR-THM-003).
func TestQueues_coverJobsAreTakenBeforePageJobs(t *testing.T) {
	t.Parallel()
	h := newHarness(t, func(o *Options) { o.noWorkers = true })

	page := func(n int) job {
		j, err := h.svc.prepare(h.pageReq(n, 240))
		if err != nil {
			t.Fatalf("prepare: %v", err)
		}
		return j
	}
	cover, err := h.svc.prepare(h.coverReq(400))
	if err != nil {
		t.Fatalf("prepare cover: %v", err)
	}

	for _, j := range []job{page(1), page(2)} {
		if err := h.svc.enqueue(j); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}
	if err := h.svc.enqueue(cover); err != nil {
		t.Fatalf("enqueue cover: %v", err)
	}
	if err := h.svc.enqueue(page(3)); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	first, ok := h.svc.take()
	if !ok {
		t.Fatal("take on a non-empty queue returned nothing")
	}
	if first.key != cover.key {
		t.Fatal("a page job was taken before a queued cover (FR-THM-003)")
	}
	for i, want := range []job{page(1), page(2), page(3)} {
		got, ok := h.svc.take()
		if !ok {
			t.Fatalf("take %d returned nothing", i)
		}
		if got.key != want.key {
			t.Fatalf("page job %d out of order", i)
		}
	}
	if _, ok := h.svc.take(); ok {
		t.Fatal("take returned a job from an empty queue")
	}
}

// FR-THM-003 end to end: the scanner enqueues a series cover as each series
// completes and the picture is on disk without anyone having asked for it. Both
// shapes of the arch §4.10 ladder go through the same call.
func TestEnqueue_coversAreGeneratedEagerly(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)

	fileCover := h.coverReq(400)
	pageCover := h.pageReq(1, 400) // cover_kind='page': page 1 of the first book
	pageCover.Priority = PriorityCover

	for _, req := range []Request{fileCover, pageCover} {
		if err := h.svc.Enqueue(req); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
	}
	h.drain()

	for name, req := range map[string]Request{"file cover": fileCover, "page cover": pageCover} {
		res, err := h.svc.Get(t.Context(), req)
		if err != nil {
			t.Fatalf("%s was not generated: %v", name, err)
		}
		decodeJPEG(t, res.Path)
	}

	// Enqueuing a cover that is already cached is a no-op, so a rescan does not
	// re-render every cover in the library.
	before := h.svc.Stats().Queued
	if err := h.svc.Enqueue(fileCover); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if after := h.svc.Stats().Queued; after != before {
		t.Fatalf("a cached cover was queued again (%d → %d)", before, after)
	}
}

// FR-THM-004's queue is bounded and drops the OLDEST request, because the
// oldest lazy thumbnail is the one the reader has already scrolled past.
func TestQueues_pageQueueIsBounded_andDropsTheOldest(t *testing.T) {
	t.Parallel()
	h := newHarness(t, func(o *Options) { o.noWorkers = true; o.PageQueue = 4 })

	keys := make([]string, 0, 8)
	for i := range 8 {
		// Distinct widths keep the keys distinct without needing eight pages.
		j, err := h.svc.prepare(Request{ID: h.zipBook, PageNo: 1, Width: 100 + i, ContentVersion: h.zipCV})
		if err != nil {
			t.Fatalf("prepare: %v", err)
		}
		j.key = fmt.Sprintf("%016d", i) // stand-in keys, so the order is readable
		if err := h.svc.enqueue(j); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		keys = append(keys, j.key)
	}

	h.svc.mu.Lock()
	depth := len(h.svc.pageQ)
	h.svc.mu.Unlock()
	if depth != 4 {
		t.Fatalf("page queue depth = %d, want the configured bound 4", depth)
	}
	if dropped := h.svc.Stats().Dropped; dropped != 4 {
		t.Fatalf("dropped = %d, want 4", dropped)
	}
	got, ok := h.svc.take()
	if !ok || got.key != keys[4] {
		t.Fatalf("head of the queue = %q, want %q (the oldest four were dropped)", got.key, keys[4])
	}
}

// The queue never holds one key twice, so a viewer that re-requests a pending
// thumbnail every second cannot make the backlog grow.
func TestEnqueue_sameKeyTwice_isOneJob(t *testing.T) {
	t.Parallel()
	h := newHarness(t, func(o *Options) { o.noWorkers = true })

	j, err := h.svc.prepare(h.pageReq(1, 240))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	for range 5 {
		if err := h.svc.enqueue(j); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}
	h.svc.mu.Lock()
	depth := len(h.svc.pageQ)
	h.svc.mu.Unlock()
	if depth != 1 {
		t.Fatalf("queue depth = %d after five identical requests, want 1", depth)
	}
}

// The request path is non-blocking: a cold miss answers immediately so the HTTP
// layer can return `202 + Retry-After` (impl-plan §4 point 3).
func TestGet_coldMiss_returnsErrQueuedWithoutGenerating(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)

	res, err := h.svc.Get(t.Context(), h.pageReq(2, 400))
	if !errors.Is(err, ErrQueued) {
		t.Fatalf("Get on a cold cache = (%v, %v), want ErrQueued", res, err)
	}
	if res.Path != "" {
		t.Fatalf("a queued result carried a path: %q", res.Path)
	}

	h.drain()
	res, err = h.svc.Get(t.Context(), h.pageReq(2, 400))
	if err != nil {
		t.Fatalf("Get after the worker ran: %v", err)
	}
	if _, err := os.Stat(res.Path); err != nil {
		t.Fatalf("the queued thumbnail was never written: %v", err)
	}
	// Page 2 is the landscape one: 800×400 at w=400 becomes 400×200.
	if cfg := decodeJPEG(t, res.Path); cfg.Width != 400 || cfg.Height != 200 {
		t.Fatalf("thumbnail = %dx%d, want 400x200", cfg.Width, cfg.Height)
	}
}

// ---------------------------------------------------------------------------
// FR-THM-005 — bounded workers, single flight, atomic publish
// ---------------------------------------------------------------------------

// impl-plan WP-07 acceptance 4: N concurrent misses for one key perform exactly
// one decode. Anything else is wasted CPU and, worse, N writers racing for one
// path.
func TestGenerate_concurrentMissesForOneKey_decodeExactlyOnce(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)

	const racers = 16
	var wg sync.WaitGroup
	results := make([]Result, racers)
	errs := make([]error, racers)
	start := make(chan struct{})
	for i := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results[i], errs[i] = h.svc.Generate(t.Context(), h.pageReq(1, 640))
		}()
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("racer %d: %v", i, err)
		}
		if results[i].Path != results[0].Path || results[i].Key != results[0].Key {
			t.Fatalf("racer %d saw a different cache entry", i)
		}
	}
	if got := h.svc.Stats().Decodes; got != 1 {
		t.Fatalf("decodes = %d for %d concurrent requests, want exactly 1", got, racers)
	}
	if got := h.svc.Stats().Generated; got != 1 {
		t.Fatalf("generated = %d, want 1", got)
	}
	decodeJPEG(t, results[0].Path)
}

// FR-THM-005: `thumbnails.workers` is a real bound, not a suggestion — and it
// belongs to the SERVICE, not to the worker pool.
//
// Both drivers are exercised because sizing the pool alone leaves the direct
// path unbounded: Service.Generate is offered to "the eager cover pass when it
// wants to know the outcome", and the scanner runs that pass at `scan.workers`.
// Measured before the permit existed: 24 concurrent decodes at Workers=2, i.e.
// ~600 MiB of RGBA against arch §5.4's ~25 MiB × workers.
func TestWorkers_concurrentDecodes_neverExceedTheConfiguredBound(t *testing.T) {
	t.Parallel()
	const workers = 2

	// The 12 distinct keys every driver produces: 3 loose cover files × 4 widths.
	//
	// Loose files rather than ZIP pages on purpose. Reading a page goes through
	// the index and one shared archive handle, and those serialise the READ
	// stage — which would let a completely unbounded implementation still
	// measure a peak of 2, because only one goroutine at a time can reach a
	// decode. A `cover_kind='file'` cover (arch §4.10 step 1) shares nothing, so
	// the peak measures the decode bound and nothing else. It is also the exact
	// shape the eager cover pass generates.
	requests := func(h *harness) []Request {
		reqs := make([]Request, 0, 12)
		for i := range 3 {
			base := h.writeLoose(fmt.Sprintf("bound%d.jpg", i), testutil.TinyJPEG(h.t, 60+i, 90))
			for _, width := range []int{120, 240, 400, 640} {
				req := base
				req.Width = width
				reqs = append(reqs, req)
			}
		}
		return reqs
	}

	for _, driver := range []struct {
		name string
		run  func(t *testing.T, h *harness)
	}{
		{"queued_through_the_pool", func(t *testing.T, h *harness) {
			for _, req := range requests(h) {
				if err := h.svc.Enqueue(req); err != nil {
					t.Fatalf("Enqueue: %v", err)
				}
			}
			h.drain()
		}},
		{"direct_through_Generate", func(t *testing.T, h *harness) {
			reqs := requests(h)
			errs := make([]error, len(reqs))
			var wg sync.WaitGroup
			start := make(chan struct{})
			for i, req := range reqs {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					_, errs[i] = h.svc.Generate(t.Context(), req)
				}()
			}
			close(start)
			wg.Wait()
			for i, err := range errs {
				if err != nil {
					t.Fatalf("Generate(%d): %v", i, err)
				}
			}
		}},
	} {
		t.Run(driver.name, func(t *testing.T) {
			t.Parallel()
			var inFlight, peak atomic.Int64
			// A CYCLIC barrier, not a one-shot gate: it keeps blocking for the
			// whole run, releasing decodes in groups of exactly `workers`.
			//
			// That is what makes the assertion load-bearing in both directions. A
			// one-shot gate stops blocking the moment it opens, so an unbounded
			// implementation just streams through it and the peak never rises —
			// verified: removing the permit did not turn a one-shot version of
			// this test red. With the barrier, an implementation that ignores the
			// bound piles every goroutine into hookDecode at once and the peak
			// sees all of them; an implementation that never parallelises cannot
			// form a group at all and trips the lower bound.
			b := &decodeBarrier{size: workers}
			h := newHarness(t, func(o *Options) {
				// noWorkers for the direct driver: it proves the bound is the
				// service's, held by whichever goroutine is producing, and not a
				// side effect of there being only two worker goroutines.
				o.noWorkers = driver.name == "direct_through_Generate"
				o.Workers = workers
				o.hookDecode = func() {
					n := inFlight.Add(1)
					for {
						p := peak.Load()
						if n <= p || peak.CompareAndSwap(p, n) {
							break
						}
					}
					b.wait(5 * time.Second)
					inFlight.Add(-1)
				}
			})

			driver.run(t, h)

			if got := peak.Load(); got > workers {
				t.Fatalf("peak concurrent decodes = %d, want at most the configured %d", got, workers)
			}
			if got := peak.Load(); got < workers {
				t.Fatalf("peak concurrent decodes = %d: never reached the bound, so the bound is untested", got)
			}
			if got := h.svc.Stats().Generated; got != 12 {
				t.Fatalf("generated = %d, want 12", got)
			}
		})
	}
}

// decodeBarrier releases its callers in groups of exactly `size` and then
// re-arms, so every decode after the first group blocks again.
//
// The timeout is a safety valve, not part of the mechanism: 12 jobs divide
// evenly into groups of 2, so a correct run never reaches it, and a run that
// leaves a partial group finishes late instead of hanging the suite.
type decodeBarrier struct {
	mu   sync.Mutex
	size int
	n    int
	ch   chan struct{}
}

func (b *decodeBarrier) wait(timeout time.Duration) {
	b.mu.Lock()
	if b.ch == nil {
		b.ch = make(chan struct{})
	}
	ch := b.ch
	b.n++
	if b.n >= b.size {
		b.n, b.ch = 0, nil
		b.mu.Unlock()
		close(ch)
		return
	}
	b.mu.Unlock()
	select {
	case <-ch:
	case <-time.After(timeout):
	}
}

// Stats().Active and Stats().Inflight are the two halves of Service.idle() that
// the snapshot used to leave out, and they exist so that something outside this
// process can ask "is every queued thumbnail on disk?" and get an answer that is
// true rather than nearly true.
//
// The scan's own `covers_done == covers_total` is not that answer:
// internal/app/covers.go derives `done` from the queue depth, so the up-to-
// `Workers` decodes still running count as finished. Measured on the E2E's
// curated subset at `thumbnails.workers: 4` — the phase reported `idle 36/36`
// with 32-33 of the 36 files present. scripts/e2e-assert.py now waits on all
// four work-in-progress fields instead of sleeping a second and hoping.
//
// The assertion that matters is the middle one: with two workers parked inside
// a decode and two more jobs still queued, the snapshot must read
// active=2, inflight=3, cover_depth=2. A Stats() that forgot the new fields
// reads 0/0 there and calls the service idle while four files are missing —
// which is exactly the state the old sleep was papering over.
//
// The third flight is not decoration. With only the parked workers in play the
// two fields are numerically equal, so `Active: inflight, Inflight: active`
// passes every assertion here and the name of this test promises a
// discrimination it cannot make — the §6.5 shape, in the very code added to
// close a §6.5 gap. A direct [Service.Generate] blocked on the decode bound is
// what pulls them apart; see the comment at its call site.
func TestStats_activeAndInflight_separateWorkInProgressFromWorkNotYetStarted(t *testing.T) {
	t.Parallel()
	const workers = 2
	const jobs = 4

	// Signalled once per decode, before the decode blocks. Buffered for every
	// generation — the `jobs` queued ones plus the direct Generate below — so
	// the ones that run after the release never block on a reader.
	entered := make(chan struct{}, jobs+1)
	release := make(chan struct{})
	h := newHarness(t, func(o *Options) {
		o.Workers = workers
		o.hookDecode = func() {
			entered <- struct{}{}
			<-release
		}
	})

	// Loose cover files, one per job: they share no archive handle, so the two
	// workers really do sit in two concurrent decodes rather than serialising on
	// a read. Four distinct sources give four distinct cache keys, so nothing
	// coalesces in singleflight and `inflight` counts jobs, not callers.
	reqs := make([]Request, 0, jobs)
	for i := range jobs {
		req := h.writeLoose(fmt.Sprintf("wip%d.jpg", i), testutil.TinyJPEG(t, 60+i, 90))
		req.Priority = PriorityCover
		reqs = append(reqs, req)
		if err := h.svc.Enqueue(req); err != nil {
			t.Fatalf("Enqueue(%d): %v", i, err)
		}
	}

	// Both workers are now inside hookDecode, and both are past `active++`
	// (worker.go, under s.mu before the unlock) and past the singleflight claim
	// that registers the key in `inflight`.
	for i := range workers {
		select {
		case <-entered:
		case <-time.After(30 * time.Second):
			t.Fatalf("only %d of %d workers reached a decode", i, workers)
		}
	}
	// One more flight than there are worker turns, so the two fields differ and
	// a transposition is observable. Service.Generate claims the singleflight
	// key (worker.go, `s.inflight[j.key] = f`) BEFORE it queues for a decode
	// permit (`s.acquireDecode`) and never touches `s.active`; the permit pool
	// is FR-THM-005's bound, sized to `workers` and entirely held by the two
	// parked decodes. So this call parks in `inflight` and stays out of
	// `active`: inflight=3, active=2.
	extra := h.writeLoose("wip-direct.jpg", testutil.TinyJPEG(t, 61, 91))
	direct := make(chan error, 1)
	go func() {
		_, err := h.svc.Generate(t.Context(), extra)
		direct <- err
	}()

	// The wait condition is the SUM, which is invariant under a transposition
	// of the two fields; the assertion after it is the discriminator. Waiting on
	// `Inflight == workers+1` would move the discrimination into the rendezvous,
	// where a transposed Stats() shows up as a 30 s timeout instead of a verdict.
	deadline := time.Now().Add(30 * time.Second)
	var busy Stats
	for {
		busy = h.svc.Stats()
		if busy.Active+busy.Inflight == 2*workers+1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the direct Generate never registered its flight: %+v", busy)
		}
		time.Sleep(time.Millisecond)
	}
	if busy.Active != workers || busy.Inflight != workers+1 {
		t.Errorf("active=%d inflight=%d, want %d and %d: `active` counts worker turns only, "+
			"so the direct Generate waiting for a decode permit belongs to `inflight` and "+
			"to nothing else",
			busy.Active, busy.Inflight, workers, workers+1)
	}
	if busy.CoverDepth != jobs-workers || busy.PageDepth != 0 {
		t.Errorf("cover_depth=%d page_depth=%d, want %d and 0 — the queue depth must not"+
			" be counting the jobs the workers already took",
			busy.CoverDepth, busy.PageDepth, jobs-workers)
	}

	close(release)
	if err := <-direct; err != nil {
		t.Fatalf("the direct Generate: %v", err)
	}
	reqs = append(reqs, extra)
	h.drain()

	idle := h.svc.Stats()
	if idle.Active != 0 || idle.Inflight != 0 || idle.CoverDepth != 0 || idle.PageDepth != 0 {
		t.Errorf("after Drain: active=%d inflight=%d cover_depth=%d page_depth=%d, want all zero",
			idle.Active, idle.Inflight, idle.CoverDepth, idle.PageDepth)
	}
	if idle.Generated != jobs+1 {
		t.Fatalf("generated = %d, want %d (the %d queued jobs plus the direct Generate)",
			idle.Generated, jobs+1, jobs)
	}
	// All four zero has to mean the bytes are on disk and readable, not merely
	// that the bookkeeping settled — that is the whole claim the E2E gate rests
	// on. Get answers from the cache without enqueuing anything, so a file that
	// had not been renamed into place yet would come back as ErrQueued here.
	for i, req := range reqs {
		res, err := h.svc.Get(t.Context(), req)
		if err != nil {
			t.Fatalf("job %d after an idle snapshot: Get = %v, want a cached thumbnail", i, err)
		}
		decodeJPEG(t, res.Path)
	}
}

// impl-plan WP-07 acceptance 3: readers racing a writer never observe a
// truncated JPEG. The property comes from write-to-temp + rename(2), which is
// atomic on every supported platform.
func TestPublish_readersRacingAWriter_neverSeeAPartialFile(t *testing.T) {
	t.Parallel()
	c, err := newCache(filepath.Join(t.TempDir(), "cache"), "jpeg", 82)
	if err != nil {
		t.Fatalf("newCache: %v", err)
	}
	key := c.key("yvtfrny77ehkt2we", 1, 640, "cv")
	path := c.path(KindThumbs, key)

	// A payload big enough that a non-atomic write would be observably partial.
	payload := testutil.TinyJPEG(t, 900, 1400)
	if len(payload) < 32<<10 {
		t.Fatalf("fixture is only %d bytes; the race would be unobservable", len(payload))
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	var seen atomic.Int64
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				b, err := os.ReadFile(path)
				if err != nil {
					continue // not published yet, or between renames
				}
				if _, err := jpeg.DecodeConfig(bytes.NewReader(b)); err != nil {
					t.Errorf("read a partial thumbnail (%d bytes): %v", len(b), err)
					return
				}
				if len(b) != len(payload) {
					t.Errorf("read %d bytes, want %d", len(b), len(payload))
					return
				}
				seen.Add(1)
			}
		}()
	}
	// Forty publishes is the floor — the race has to be exercised repeatedly, not
	// once — but on a loaded machine under -race the readers can be starved for
	// the whole of it, and "the race was not exercised" is then a scheduler
	// verdict rather than a defect. Observed doing exactly that once in a full
	// -race run. So: keep publishing until a reader has genuinely seen a
	// complete file, bounded by a deadline so a real regression still fails.
	deadline := time.Now().Add(10 * time.Second)
	for i := 0; i < 40 || (seen.Load() == 0 && time.Now().Before(deadline)); i++ {
		if _, err := c.publish(KindThumbs, key, payload); err != nil {
			t.Fatalf("publish: %v", err)
		}
	}
	close(stop)
	wg.Wait()

	if seen.Load() == 0 {
		t.Fatal("no reader ever observed the published file; the race was not exercised")
	}
	// A killed process must leave a .tmp file, never a half-written .jpg. After
	// a clean run there is no debris at all.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("temporary file %q survived a successful publish", e.Name())
		}
	}
}

// ---------------------------------------------------------------------------
// FR-THM-007 / AC-005 — the cache is deletable at any moment
// ---------------------------------------------------------------------------

// AC-005 literally: delete the whole cache directory while the server runs and
// the system keeps working, regenerating what it needs.
func TestCacheDirectoryDeleted_isSelfHealing(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)

	first := h.getReady(h.pageReq(1, 240))
	if _, err := os.Stat(first.Path); err != nil {
		t.Fatalf("stat before the deletion: %v", err)
	}

	if err := os.RemoveAll(h.cacheDir); err != nil {
		t.Fatalf("removing the cache directory: %v", err)
	}
	if _, err := os.Stat(h.cacheDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the cache directory survived removal: %v", err)
	}

	// Usage still answers, with zeros rather than an error.
	usage, err := h.svc.Usage(t.Context())
	if err != nil {
		t.Fatalf("Usage on a missing cache directory: %v", err)
	}
	if usage.TotalBytes != 0 || usage.TotalFiles != 0 {
		t.Fatalf("usage of a deleted cache = %+v, want zeros", usage)
	}

	// And the same request regenerates, at the same path, with the same bytes.
	second := h.getReady(h.pageReq(1, 240))
	if second.Path != first.Path {
		t.Fatalf("regenerated at %q, want the stable path %q", second.Path, first.Path)
	}
	if second.Size != first.Size {
		t.Fatalf("regenerated thumbnail is %d bytes, was %d", second.Size, first.Size)
	}
	decodeJPEG(t, second.Path)
}

// The harder half of FR-THM-007: delete the directory WHILE the workers are
// publishing into it. Losing a rendering is acceptable; failing is not.
func TestCacheDirectoryDeletedMidRun_costsLatencyNotCorrectness(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)

	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
			}
			_ = os.RemoveAll(h.cacheDir)
			time.Sleep(time.Millisecond)
		}
	}()

	for range 6 {
		for _, width := range []int{120, 240, 400, 640} {
			for page := 1; page <= 3; page++ {
				req := h.pageReq(page, width)
				if _, err := h.svc.Generate(t.Context(), req); err != nil {
					close(done)
					wg.Wait()
					t.Fatalf("Generate against a vanishing cache directory: %v", err)
				}
			}
		}
	}
	close(done)
	wg.Wait()

	if failed := h.svc.Stats().Failed; failed != 0 {
		t.Fatalf("%d generations failed while the cache was being deleted", failed)
	}
	// One last request must still land on disk.
	res := h.getReady(h.pageReq(1, 120))
	decodeJPEG(t, res.Path)
}

// ---------------------------------------------------------------------------
// Request validation and width snapping
// ---------------------------------------------------------------------------

// impl-plan §0.4: the server snaps UP, so a client sending a CSS-pixel size
// still gets a picture that is never softer than its box.
func TestSnapWidth_roundsUpToTheConfiguredLadder(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	for _, tc := range []struct{ ask, want int }{
		{0, 120},    // the API default (amendment A-6)
		{48, 120},   // list-row thumb at 2× DPR
		{96, 120},   // viewer strip
		{120, 120},  // exact
		{132, 240},  // continue-card thumb
		{136, 240},  // slider preview
		{256, 400},  // volume tile
		{356, 400},  // grid cover at ≥1440
		{448, 640},  // grid cover at ≤768
		{641, 640},  // clamped to the largest
		{9000, 640}, // and stays clamped
	} {
		if got := h.svc.snapWidth(tc.ask); got != tc.want {
			t.Errorf("snapWidth(%d) = %d, want %d", tc.ask, got, tc.want)
		}
	}
}

func TestNormaliseWidths_sortsDeduplicatesAndDefaults(t *testing.T) {
	t.Parallel()
	if got := normaliseWidths(nil); fmt.Sprint(got) != "[120 240 400 640]" {
		t.Fatalf("normaliseWidths(nil) = %v, want amendment A-1's ladder", got)
	}
	if got := normaliseWidths([]int{640, 120, 640, 0, -3, 240}); fmt.Sprint(got) != "[120 240 640]" {
		t.Fatalf("normaliseWidths = %v, want [120 240 640]", got)
	}
}

func TestGet_malformedRequests_areRejectedWithoutTouchingTheDisk(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)

	for name, req := range map[string]Request{
		"empty id":       {PageNo: 1, Width: 240},
		"page zero":      {ID: h.zipBook, PageNo: 0, Width: 240},
		"negative page":  {ID: h.zipBook, PageNo: -1, Width: 240},
		"file, no root":  {ID: h.seriesID, RelPath: coverRelPath, Width: 240},
		"escaping path":  {ID: h.seriesID, RootName: testRoot, RelPath: "../../etc/passwd", Width: 240},
		"absolute path":  {ID: h.seriesID, RootName: testRoot, RelPath: "/etc/passwd", Width: 240},
		"windows escape": {ID: h.seriesID, RootName: testRoot, RelPath: `..\..\windows\win.ini`, Width: 240},
	} {
		if _, err := h.svc.Get(t.Context(), req); !errors.Is(err, ErrBadRequest) {
			t.Errorf("%s: err = %v, want ErrBadRequest", name, err)
		}
	}
}

func TestGet_unknownBookOrPage_isNotFound(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)

	unknown := Request{ID: "aaaaaaaaaaaaaaaa", PageNo: 1, Width: 240, ContentVersion: "cv"}
	if _, err := h.svc.Generate(t.Context(), unknown); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown book: err = %v, want ErrNotFound", err)
	}
	if _, err := h.svc.Generate(t.Context(), h.pageReq(99, 240)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("page past the end: err = %v, want ErrNotFound", err)
	}
}

// ---------------------------------------------------------------------------
// The three book shapes and the loose-file cover
// ---------------------------------------------------------------------------

// AC-003: a directory book thumbnails exactly like an archive book.
func TestGenerate_directoryBook_thumbnailsLikeAnArchive(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)

	res, err := h.svc.Generate(t.Context(), Request{
		ID: h.dirBook, PageNo: 2, Width: 240, ContentVersion: h.dirCV,
	})
	if err != nil {
		t.Fatalf("Generate for a dir book: %v", err)
	}
	if cfg := decodeJPEG(t, res.Path); cfg.Width != 240 || cfg.Height != 120 {
		t.Fatalf("thumbnail = %dx%d, want 240x120", cfg.Width, cfg.Height)
	}
}

// arch §4.10 step 1: the series cover is often a loose `[cover].jpg` beside the
// volumes rather than a page inside one.
func TestGenerate_looseFileCover_readsThroughTheRoot(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)

	res, err := h.svc.Generate(t.Context(), h.coverReq(400))
	if err != nil {
		t.Fatalf("Generate for a loose-file cover: %v", err)
	}
	if cfg := decodeJPEG(t, res.Path); cfg.Width != 400 || cfg.Height != 600 {
		t.Fatalf("cover = %dx%d, want 400x600", cfg.Width, cfg.Height)
	}
	// A loose file has no page, so it is keyed at page 0 whatever the caller
	// says — one file cannot be cached under two keys.
	withPage := h.coverReq(400)
	withPage.PageNo = 7
	res2, err := h.svc.Generate(t.Context(), withPage)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if res2.Path != res.Path {
		t.Fatalf("the same cover file cached twice: %q and %q", res.Path, res2.Path)
	}
}

func TestGenerate_missingCoverFile_isNotFound(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)

	req := h.coverReq(240)
	req.RelPath = "series/does-not-exist.jpg"
	if _, err := h.svc.Generate(t.Context(), req); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

func TestClose_isIdempotentAndStopsAcceptingWork(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)

	if err := h.svc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := h.svc.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := h.svc.Get(t.Context(), h.pageReq(1, 240)); !errors.Is(err, ErrClosed) {
		t.Fatalf("Get after Close: err = %v, want ErrClosed", err)
	}
}

func TestNew_rejectsAnEmptyCacheDirAndANonJPEGFormat(t *testing.T) {
	t.Parallel()
	if _, err := New(t.Context(), Options{}); err == nil {
		t.Fatal("New accepted an empty CacheDir")
	}
	// CON-003: the format string is a cache-hash input, so a value the encoder
	// does not implement would make every path a lie.
	if _, err := New(t.Context(), Options{CacheDir: t.TempDir(), Format: "webp"}); err == nil {
		t.Fatal("New accepted format \"webp\"; CON-003 pins v1 to jpeg")
	}
}
