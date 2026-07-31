package index_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"shelf/internal/index"
	"shelf/internal/userdata"
)

// ---------------------------------------------------------------- fixtures --

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// testClock is the injectable clock userdata.Options.Now accepts. Progress
// timestamps are Unix *seconds*, so tests that care about ordering must drive
// the clock rather than sleep for a second at a time.
type testClock struct {
	mu sync.Mutex
	at time.Time
}

func newClock() *testClock {
	return &testClock{at: time.Unix(1_700_000_000, 0).UTC()}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

// newDBs opens a user.db and an index.db in a fresh temp directory, in that
// order — index.Open requires an initialised user database because it attaches
// it on every connection.
func newDBs(t *testing.T) (*index.DB, *userdata.DB, string) {
	t.Helper()
	idx, ud, _, dir := newDBsAt(t)
	return idx, ud, dir
}

func newDBsAt(t *testing.T) (*index.DB, *userdata.DB, *testClock, string) {
	t.Helper()
	dir := t.TempDir()
	clk := newClock()
	idx, ud, _ := openDBsIn(t, dir, clk)
	return idx, ud, clk, dir
}

func openDBsIn(t *testing.T, dir string, clk *testClock) (*index.DB, *userdata.DB, string) {
	t.Helper()
	ctx := t.Context()

	opts := userdata.Options{
		Path:   filepath.Join(dir, "user.db"),
		Logger: quietLogger(),
	}
	if clk != nil {
		opts.Now = clk.Now
	}
	ud, err := userdata.Open(ctx, opts)
	if err != nil {
		t.Fatalf("userdata.Open: %v", err)
	}
	t.Cleanup(func() { _ = ud.Close() })

	idx, err := index.Open(ctx, index.Options{
		Path:     filepath.Join(dir, "index.db"),
		UserPath: filepath.Join(dir, "user.db"),
		Logger:   quietLogger(),
	})
	if err != nil {
		t.Fatalf("index.Open: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })

	return idx, ud, dir
}

// sortKeyOf is a stand-in for natsort.Key (WP-02). The storage layer never
// computes sort keys — it stores whatever BLOB the scanner hands it and lets
// SQLite's BINARY collation do the ordering — so the tests deliberately use
// keys that DISAGREE with the display name. If a query ever sorted by name in
// Go, TestListSeries_sortName_ordersByStoredBlob would fail.
func sortKeyOf(s string) []byte { return []byte(s) }

type seedSeries struct {
	id         string
	name       string
	sortKey    string
	searchKey  string
	choseong   string
	kind       string
	mtime      int64
	addedAt    int64
	totalBytes int64
	status     string
	books      []seedBook
}

type seedBook struct {
	id    string
	name  string
	ord   int
	kind  string
	pages int
}

// seed writes a whole small library through the single-writer API.
func seed(t *testing.T, idx *index.DB, rootName string, all []seedSeries) {
	t.Helper()
	ctx := t.Context()

	if err := idx.UpsertRoot(ctx, index.Root{
		Name: rootName, Path: "/media/" + rootName, Label: rootName, Enabled: true,
	}); err != nil {
		t.Fatalf("UpsertRoot: %v", err)
	}

	w := idx.Writer(index.WriterOptions{})
	defer func() {
		if err := w.Close(); err != nil {
			t.Fatalf("writer.Close: %v", err)
		}
	}()

	for _, s := range all {
		status := s.status
		if status == "" {
			status = "ok"
		}
		kind := s.kind
		if kind == "" {
			kind = "folder"
		}
		var books, pages, bytes int64
		for _, b := range s.books {
			books++
			pages += int64(b.pages)
			bytes += int64(b.pages) * 1000
		}
		if s.totalBytes != 0 {
			bytes = s.totalBytes
		}
		cover := ""
		coverBook := ""
		if len(s.books) > 0 {
			cover, coverBook = "page", s.books[0].id
		}
		err := w.UpsertSeries(ctx, index.Series{
			ID: s.id, RootName: rootName, RelPath: s.name, DisplayName: s.name,
			SortKey: sortKeyOf(s.sortKey), SearchKey: s.searchKey, ChoseongKey: s.choseong,
			Kind: kind, BookCount: books, PageCount: pages, TotalBytes: bytes,
			Mtime: s.mtime, AddedAt: s.addedAt,
			CoverKind: cover, CoverBookID: coverBook, CoverPageNo: 1,
			Status: status, ScanGen: 1,
		})
		if err != nil {
			t.Fatalf("UpsertSeries %s: %v", s.id, err)
		}
		for _, b := range s.books {
			bk := index.Book{
				ID: b.id, SeriesID: s.id, RootName: rootName,
				RelPath: s.name + "/" + b.name, DisplayName: b.name,
				SortKey: sortKeyOf(b.name), Ord: b.ord, Kind: b.kind,
				PageCount: int64(b.pages), TotalBytes: int64(b.pages) * 1000,
				FileSize: int64(b.pages) * 900, FileMtime: s.mtime,
				ContentVersion: "cv" + b.id, Status: "ok", ScanGen: 1,
			}
			if bk.Kind == "" {
				bk.Kind = "zip"
			}
			if err := w.UpsertBook(ctx, bk); err != nil {
				t.Fatalf("UpsertBook %s: %v", b.id, err)
			}
			pgs := make([]index.Page, 0, b.pages)
			for i := 1; i <= b.pages; i++ {
				pgs = append(pgs, index.Page{
					PageNo: i, Name: fmt.Sprintf("%03d.jpg", i),
					EntryPath: fmt.Sprintf("%s/%03d.jpg", b.name, i), Ext: ".jpg",
					Size: 4096, CompSize: 4000, Method: 8,
					LocalHdrOff: int64(i) * 4096, CRC32: 0xdeadbeef,
				})
			}
			if err := w.ReplacePages(ctx, b.id, pgs); err != nil {
				t.Fatalf("ReplacePages %s: %v", b.id, err)
			}
		}
	}
}

// library is the fixture most listing tests share: five series with sort keys
// chosen so BLOB order differs from display-name order.
func library() []seedSeries {
	return []seedSeries{
		{id: "aaaaaaaaaaaaaaaa", name: "군계", sortKey: "1-gungye", searchKey: "군계",
			choseong: "ㄱㄱ", mtime: 300, addedAt: 30, totalBytes: 5000,
			books: []seedBook{{id: "b1aaaaaaaaaaaaaa", name: "01권.zip", ord: 0, kind: "zip", pages: 5},
				{id: "b2aaaaaaaaaaaaaa", name: "02권.zip", ord: 1, kind: "zip", pages: 7}}},
		{id: "bbbbbbbbbbbbbbbb", name: "강철의 연금술사", sortKey: "2-gangcheol",
			searchKey: "강철의 연금술사", choseong: "ㄱㅊㅇ ㅇㄱㅅㅅ", mtime: 500, addedAt: 10,
			books: []seedBook{{id: "b3bbbbbbbbbbbbbb", name: "01.zip", ord: 0, kind: "zip", pages: 3}}},
		{id: "cccccccccccccccc", name: "Attack on Titan", sortKey: "3-attack",
			searchKey: "attack on titan", choseong: "attack on titan", mtime: 100, addedAt: 50,
			kind:  "zip",
			books: []seedBook{{id: "b4cccccccccccccc", name: "vol1.zip", ord: 0, kind: "zip", pages: 11}}},
		{id: "dddddddddddddddd", name: "빈 시리즈", sortKey: "4-empty", searchKey: "빈 시리즈",
			choseong: "ㅂ ㅅㄹㅈ", mtime: 200, addedAt: 20, status: "empty"},
		{id: "eeeeeeeeeeeeeeee", name: "20세기소년", sortKey: "5-20segi", searchKey: "20세기소년",
			choseong: "20ㅅㄱㅅㄴ", mtime: 400, addedAt: 40,
			books: []seedBook{{id: "b5eeeeeeeeeeeeee", name: "1.zip", ord: 0, kind: "zip", pages: 2}}},
	}
}

func ids(list []index.SeriesRow) []string {
	out := make([]string, len(list))
	for i, s := range list {
		out[i] = s.ID
	}
	return out
}

func names(list []index.SeriesRow) []string {
	out := make([]string, len(list))
	for i, s := range list {
		out[i] = s.DisplayName
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

// ------------------------------------------------------------------ schema --

func TestOpen_freshDirectory_appliesSchemaInWALMode(t *testing.T) {
	t.Parallel()
	idx, _, dir := newDBs(t)
	ctx := t.Context()

	// NFR-DAT-003: both databases run in WAL.
	for _, name := range []string{"index.db", "user.db"} {
		mode := journalMode(t, filepath.Join(dir, name))
		if !strings.EqualFold(mode, "wal") {
			t.Errorf("%s journal_mode = %q, want wal", name, mode)
		}
	}

	// A write must materialise the -wal sidecar; that is what FR-IDX-005's
	// hard-coded delete list is aimed at.
	seed(t, idx, "manga", library()[:1])
	if _, err := os.Stat(filepath.Join(dir, "index.db-wal")); err != nil {
		t.Errorf("index.db-wal not present after a write: %v", err)
	}

	got, err := idx.SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if got != 1 {
		t.Errorf("schema version = %d, want 1", got)
	}
	if v, ok, _ := idx.Meta(ctx, "id_version"); !ok || v != "shelf-id/1" {
		t.Errorf("meta id_version = %q (present %v), want shelf-id/1", v, ok)
	}
}

// TestOpen_schema_matchesArchDDL pins every table, column and index of
// arch-backend §3.5. A dropped column or index is a silent behaviour change
// somewhere else in the system, so it is asserted structurally here.
func TestOpen_schema_matchesArchDDL(t *testing.T) {
	t.Parallel()
	_, _, dir := newDBs(t)

	raw := openRaw(t, filepath.Join(dir, "index.db"))

	wantColumns := map[string][]string{
		"meta":     {"key", "value"},
		"roots":    {"name", "path", "label", "enabled", "series_count", "book_count", "page_count", "total_bytes", "last_scan_start", "last_scan_end", "last_scan_error"},
		"series":   {"id", "root_name", "rel_path", "display_name", "sort_key", "search_key", "choseong_key", "kind", "book_count", "page_count", "total_bytes", "mtime", "added_at", "cover_kind", "cover_book_id", "cover_page_no", "cover_rel_path", "status", "error", "scan_gen"},
		"books":    {"id", "series_id", "root_name", "rel_path", "display_name", "sort_key", "ord", "kind", "page_count", "total_bytes", "file_size", "file_mtime", "dir_fingerprint", "content_version", "dims_state", "status", "error", "scan_gen"},
		"pages":    {"book_id", "page_no", "name", "entry_path", "ext", "size", "comp_size", "method", "local_hdr_off", "crc32", "mtime", "width", "height"},
		"scan_log": {"id", "ts", "run_id", "level", "root_name", "rel_path", "message"},
	}
	for table, want := range wantColumns {
		got := tableColumns(t, raw, table)
		if !equalStrings(got, want) {
			t.Errorf("table %s columns = %v, want %v", table, got, want)
		}
	}

	wantIndices := []string{
		"ix_series_root_sort", "ix_series_sort", "ix_series_mtime", "ix_series_added",
		"ix_series_bytes", "ix_series_books", "ix_series_search", "ix_series_gen",
		"ix_books_series", "ix_books_gen", "ix_books_status",
		"ix_scanlog_ts", "ix_scanlog_run",
	}
	have := objectNames(t, raw, "index")
	for _, want := range wantIndices {
		if !have[want] {
			t.Errorf("index %s is missing", want)
		}
	}

	// D-15/AC-008: the pages primary key IS the storage order.
	var ddl string
	if err := raw.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type='table' AND name='pages'`).Scan(&ddl); err != nil {
		t.Fatalf("reading pages DDL: %v", err)
	}
	if !strings.Contains(strings.ToUpper(ddl), "WITHOUT ROWID") {
		t.Errorf("pages is not WITHOUT ROWID:\n%s", ddl)
	}
	if !strings.Contains(strings.ToUpper(ddl), "PRIMARY KEY (BOOK_ID, PAGE_NO)") {
		t.Errorf("pages primary key is not (book_id, page_no):\n%s", ddl)
	}
}

func TestOpen_reopen_keepsRowsAndVersion(t *testing.T) {
	t.Parallel()
	idx, _, dir := newDBs(t)
	seed(t, idx, "manga", library())
	if err := idx.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	idx2, _, _ := openDBsIn(t, dir, nil)
	got, err := idx2.ListSeries(t.Context(), index.SeriesFilter{})
	if err != nil {
		t.Fatalf("ListSeries after reopen: %v", err)
	}
	if got.Total != 5 {
		t.Errorf("total after reopen = %d, want 5", got.Total)
	}
}

func TestOpen_futureSchemaVersion_isRefused(t *testing.T) {
	t.Parallel()
	idx, _, dir := newDBs(t)
	if err := idx.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	raw := openRaw(t, filepath.Join(dir, "index.db"))
	if _, err := raw.Exec(`UPDATE meta SET value = '99' WHERE key = 'schema_version'`); err != nil {
		t.Fatalf("forging schema version: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("closing raw handle: %v", err)
	}

	_, err := index.Open(t.Context(), index.Options{
		Path:     filepath.Join(dir, "index.db"),
		UserPath: filepath.Join(dir, "user.db"),
		Logger:   quietLogger(),
	})
	if !errors.Is(err, index.ErrSchemaTooNew) {
		t.Fatalf("Open on a v99 index = %v, want ErrSchemaTooNew", err)
	}
	if !strings.Contains(err.Error(), "99") {
		t.Errorf("error %q does not name the offending version", err)
	}
}

// The derived index is disposable, so a change of identifier scheme rebuilds it
// rather than failing. user.db does the opposite — see userdata_test.go.
func TestOpen_foreignIDVersion_rebuildsIndex(t *testing.T) {
	t.Parallel()
	idx, _, dir := newDBs(t)
	seed(t, idx, "manga", library())
	if err := idx.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	raw := openRaw(t, filepath.Join(dir, "index.db"))
	if _, err := raw.Exec(`UPDATE meta SET value = 'shelf-id/9' WHERE key = 'id_version'`); err != nil {
		t.Fatalf("forging id version: %v", err)
	}
	_ = raw.Close()

	idx2, err := index.Open(t.Context(), index.Options{
		Path:     filepath.Join(dir, "index.db"),
		UserPath: filepath.Join(dir, "user.db"),
		Logger:   quietLogger(),
	})
	if err != nil {
		t.Fatalf("Open after id scheme change: %v", err)
	}
	defer func() { _ = idx2.Close() }()

	list, err := idx2.ListSeries(t.Context(), index.SeriesFilter{})
	if err != nil {
		t.Fatalf("ListSeries: %v", err)
	}
	if list.Total != 0 {
		t.Errorf("total after id-scheme rebuild = %d, want 0 (index must be discarded)", list.Total)
	}
	if v, _, _ := idx2.Meta(t.Context(), "id_version"); v != "shelf-id/1" {
		t.Errorf("id_version = %q, want shelf-id/1", v)
	}
}

func TestOpen_corruptFile_isCleanError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ud, err := userdata.Open(t.Context(), userdata.Options{
		Path: filepath.Join(dir, "user.db"), Logger: quietLogger()})
	if err != nil {
		t.Fatalf("userdata.Open: %v", err)
	}
	defer func() { _ = ud.Close() }()

	garbage := make([]byte, 8192)
	for i := range garbage {
		garbage[i] = byte(i%251) ^ 0x5a
	}
	if err := os.WriteFile(filepath.Join(dir, "index.db"), garbage, 0o600); err != nil {
		t.Fatalf("writing garbage: %v", err)
	}

	_, err = index.Open(t.Context(), index.Options{
		Path:     filepath.Join(dir, "index.db"),
		UserPath: filepath.Join(dir, "user.db"),
		Logger:   quietLogger(),
	})
	if err == nil {
		t.Fatal("Open on a corrupt file returned no error")
	}
	if !strings.Contains(err.Error(), "index.db") && !strings.Contains(err.Error(), "not a database") {
		t.Errorf("error %q names neither the file nor the cause", err)
	}
}

func TestOpen_uninitialisedUserDB_isCleanError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, err := index.Open(t.Context(), index.Options{
		Path:     filepath.Join(dir, "index.db"),
		UserPath: filepath.Join(dir, "user.db"),
		Logger:   quietLogger(),
	})
	if !errors.Is(err, index.ErrUserDBNotReady) {
		t.Fatalf("Open with no user.db = %v, want ErrUserDBNotReady", err)
	}
}

// ------------------------------------------------------------------- roots --

func TestRoots_lifecycle(t *testing.T) {
	t.Parallel()
	idx, _, _ := newDBs(t)
	ctx := t.Context()
	seed(t, idx, "manga", library())

	got, err := idx.GetRoot(ctx, "manga")
	if err != nil {
		t.Fatalf("GetRoot: %v", err)
	}
	if got.Path != "/media/manga" || !got.Enabled || got.Label != "manga" {
		t.Errorf("root = %+v", got)
	}
	if got.LastScanStart != nil || got.LastScanEnd != nil || got.LastScanError != "" {
		t.Errorf("a never-scanned root reports scan times: %+v", got)
	}
	if _, err := idx.GetRoot(ctx, "nope"); !errors.Is(err, index.ErrNotFound) {
		t.Errorf("GetRoot(unknown) = %v, want ErrNotFound", err)
	}

	if err := idx.MarkRootScanStart(ctx, "manga", 1000); err != nil {
		t.Fatalf("MarkRootScanStart: %v", err)
	}
	if err := idx.MarkRootScanEnd(ctx, "manga", 1030, "drive went away"); err != nil {
		t.Fatalf("MarkRootScanEnd: %v", err)
	}
	got, _ = idx.GetRoot(ctx, "manga")
	if got.LastScanStart == nil || *got.LastScanStart != 1000 ||
		got.LastScanEnd == nil || *got.LastScanEnd != 1030 ||
		got.LastScanError != "drive went away" {
		t.Errorf("after a failed scan the root reads %+v", got)
	}
	// A new run clears the previous error.
	if err := idx.MarkRootScanStart(ctx, "manga", 2000); err != nil {
		t.Fatalf("MarkRootScanStart: %v", err)
	}
	if got, _ = idx.GetRoot(ctx, "manga"); got.LastScanError != "" {
		t.Errorf("last_scan_error survived a new run: %q", got.LastScanError)
	}

	if err := idx.RecountRoot(ctx, "manga"); err != nil {
		t.Fatalf("RecountRoot: %v", err)
	}
	got, _ = idx.GetRoot(ctx, "manga")
	if got.SeriesCount != 5 || got.BookCount != 5 || got.PageCount != 28 {
		t.Errorf("counts = %d series, %d books, %d pages; want 5, 5, 28",
			got.SeriesCount, got.BookCount, got.PageCount)
	}

	all, err := idx.ListRoots(ctx)
	if err != nil {
		t.Fatalf("ListRoots: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("roots = %d, want 1", len(all))
	}

	// Deleting a root cascades to its series and books, and takes the page rows
	// with it even though `pages` declares no foreign key.
	if err := idx.DeleteRoot(ctx, "manga"); err != nil {
		t.Fatalf("DeleteRoot: %v", err)
	}
	if list, _ := idx.ListSeries(ctx, index.SeriesFilter{IncludeDisabledRoots: true}); list.Total != 0 {
		t.Errorf("series survived DeleteRoot: %d", list.Total)
	}
	if n, _ := idx.CountOrphanPages(ctx); n != 0 {
		t.Errorf("orphan pages after DeleteRoot = %d, want 0", n)
	}
}

// ----------------------------------------------------------------- listing --

func TestListSeries_filters(t *testing.T) {
	t.Parallel()
	idx, ud, _ := newDBs(t)
	ctx := t.Context()
	seed(t, idx, "manga", library())
	seed(t, idx, "novels", []seedSeries{
		{id: "ffffffffffffffff", name: "소설", sortKey: "1-novel", searchKey: "소설",
			choseong: "ㅅㅅ", mtime: 700, addedAt: 70,
			books: []seedBook{{id: "b6ffffffffffffff", name: "n1.zip", ord: 0, kind: "zip", pages: 9}}},
	})

	// 군계 book 1 in progress, book 2 finished; 20세기소년 finished outright.
	put := func(bookID, seriesID string, page, total int, done bool) {
		t.Helper()
		if _, err := ud.PutProgress(ctx, userdata.ProgressUpdate{
			BookID: bookID, SeriesID: seriesID, RootName: "manga", BookPath: "x",
			Page: page, PageCount: total, Completed: &done,
		}); err != nil {
			t.Fatalf("PutProgress %s: %v", bookID, err)
		}
	}
	put("b1aaaaaaaaaaaaaa", "aaaaaaaaaaaaaaaa", 3, 5, false)
	put("b2aaaaaaaaaaaaaa", "aaaaaaaaaaaaaaaa", 7, 7, true)
	put("b5eeeeeeeeeeeeee", "eeeeeeeeeeeeeeee", 2, 2, true)

	tests := []struct {
		name   string
		filter index.SeriesFilter
		want   []string
	}{
		{"no filter lists every enabled root", index.SeriesFilter{},
			[]string{"aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb", "cccccccccccccccc", "dddddddddddddddd", "eeeeeeeeeeeeeeee", "ffffffffffffffff"}},
		{"root filter (FR-LIB-005)", index.SeriesFilter{Roots: []string{"novels"}},
			[]string{"ffffffffffffffff"}},
		{"two roots", index.SeriesFilter{Roots: []string{"novels", "manga"}, Limit: 200},
			[]string{"aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb", "cccccccccccccccc", "dddddddddddddddd", "eeeeeeeeeeeeeeee", "ffffffffffffffff"}},
		{"status=empty (D-7)", index.SeriesFilter{Status: "empty"}, []string{"dddddddddddddddd"}},
		{"status=ok hides the empty one", index.SeriesFilter{Status: "ok"},
			[]string{"aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb", "cccccccccccccccc", "eeeeeeeeeeeeeeee", "ffffffffffffffff"}},
		{"q matches the name substring", index.SeriesFilter{Query: "연금술"}, []string{"bbbbbbbbbbbbbbbb"}},
		{"q is case-insensitive ASCII", index.SeriesFilter{Query: "ATTACK"}, []string{"cccccccccccccccc"}},
		{"q jamo matches choseong (FR-LIB-006)", index.SeriesFilter{Query: "ㄱㅊ"}, []string{"bbbbbbbbbbbbbbbb"}},
		{"q jamo ㄱㄱ matches 군계", index.SeriesFilter{Query: "ㄱㄱ"}, []string{"aaaaaaaaaaaaaaaa"}},
		{"q with syllables does not match choseong", index.SeriesFilter{Query: "군계"}, []string{"aaaaaaaaaaaaaaaa"}},
		{"q wildcard is escaped, not interpreted", index.SeriesFilter{Query: "%"}, nil},
		{"progress=reading (A-4)", index.SeriesFilter{Progress: "reading"}, []string{"aaaaaaaaaaaaaaaa"}},
		{"progress=done (A-4)", index.SeriesFilter{Progress: "done"}, []string{"eeeeeeeeeeeeeeee"}},
		{"progress=unread (A-4)", index.SeriesFilter{Progress: "unread"},
			[]string{"bbbbbbbbbbbbbbbb", "cccccccccccccccc", "dddddddddddddddd", "ffffffffffffffff"}},
		{"progress=any is the default", index.SeriesFilter{Progress: "any"},
			[]string{"aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb", "cccccccccccccccc", "dddddddddddddddd", "eeeeeeeeeeeeeeee", "ffffffffffffffff"}},
		{"filters compose", index.SeriesFilter{Roots: []string{"manga"}, Progress: "unread", Status: "ok"},
			[]string{"bbbbbbbbbbbbbbbb", "cccccccccccccccc"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := idx.ListSeries(ctx, tc.filter)
			if err != nil {
				t.Fatalf("ListSeries: %v", err)
			}
			// These cases assert membership; ordering has its own tests.
			gotIDs := ids(got.Items)
			sort.Strings(gotIDs)
			if len(tc.want) == 0 && len(gotIDs) == 0 {
				return
			}
			if !equalStrings(gotIDs, tc.want) {
				t.Errorf("ids = %v, want %v", gotIDs, tc.want)
			}
			if got.Total != len(tc.want) {
				t.Errorf("total = %d, want %d", got.Total, len(tc.want))
			}
		})
	}
}

func TestListSeries_disabledRoot_isHiddenButKept(t *testing.T) {
	t.Parallel()
	idx, _, _ := newDBs(t)
	ctx := t.Context()
	seed(t, idx, "manga", library()[:2])

	if err := idx.UpsertRoot(ctx, index.Root{
		Name: "manga", Path: "/media/manga", Label: "manga", Enabled: false}); err != nil {
		t.Fatalf("disabling root: %v", err)
	}

	got, err := idx.ListSeries(ctx, index.SeriesFilter{})
	if err != nil {
		t.Fatalf("ListSeries: %v", err)
	}
	if got.Total != 0 {
		t.Errorf("disabled root still listed: total = %d", got.Total)
	}

	// arch §3.2: the rows survive, so re-enabling loses nothing.
	kept, err := idx.ListSeries(ctx, index.SeriesFilter{IncludeDisabledRoots: true})
	if err != nil {
		t.Fatalf("ListSeries(IncludeDisabledRoots): %v", err)
	}
	if kept.Total != 2 {
		t.Errorf("rows of a disabled root = %d, want 2", kept.Total)
	}
}

// sort=name must be answered by the stored BLOB under BINARY collation (D-31).
// The fixture's sort keys deliberately contradict the display names, so a Go-
// side or display-name sort produces a different order and fails.
func TestListSeries_sortName_ordersByStoredBlob(t *testing.T) {
	t.Parallel()
	idx, _, _ := newDBs(t)
	seed(t, idx, "manga", library())

	got, err := idx.ListSeries(t.Context(), index.SeriesFilter{Sort: index.SortName})
	if err != nil {
		t.Fatalf("ListSeries: %v", err)
	}
	want := []string{"군계", "강철의 연금술사", "Attack on Titan", "빈 시리즈", "20세기소년"}
	if !equalStrings(names(got.Items), want) {
		t.Errorf("sort=name order = %v, want %v (the stored sort_key order)", names(got.Items), want)
	}

	desc, err := idx.ListSeries(t.Context(), index.SeriesFilter{Sort: index.SortName, Order: "desc"})
	if err != nil {
		t.Fatalf("ListSeries desc: %v", err)
	}
	for i := range want {
		if desc.Items[i].DisplayName != want[len(want)-1-i] {
			t.Fatalf("sort=name&order=desc = %v, want the reverse of %v", names(desc.Items), want)
		}
	}
}

func TestListSeries_sorts(t *testing.T) {
	t.Parallel()
	idx, ud, clk, _ := newDBsAt(t)
	ctx := t.Context()
	seed(t, idx, "manga", library())

	// Only two series have ever been read, so "recent" must put the other three
	// last regardless of direction (arch §7.5).
	mustPut(t, ud, "b3bbbbbbbbbbbbbb", "bbbbbbbbbbbbbbbb", 1, 3, false)
	clk.Advance(time.Minute) // updated_at is Unix seconds, so drive the clock
	mustPut(t, ud, "b4cccccccccccccc", "cccccccccccccccc", 1, 11, false)

	// mtime:  b 500, e 400, a 300, d 200, c 100
	// added:  f— a 30, b 10, c 50, d 20, e 40
	// books:  a 2, b 1, c 1, e 1, d 0
	// bytes:  a 5000 (explicit), c 11000, b 3000, e 2000, d 0
	tests := []struct {
		sort, order string
		want        []string
	}{
		{index.SortMtime, "", []string{"bbbbbbbbbbbbbbbb", "eeeeeeeeeeeeeeee", "aaaaaaaaaaaaaaaa", "dddddddddddddddd", "cccccccccccccccc"}},
		{index.SortMtime, "asc", []string{"cccccccccccccccc", "dddddddddddddddd", "aaaaaaaaaaaaaaaa", "eeeeeeeeeeeeeeee", "bbbbbbbbbbbbbbbb"}},
		{index.SortAdded, "", []string{"cccccccccccccccc", "eeeeeeeeeeeeeeee", "aaaaaaaaaaaaaaaa", "dddddddddddddddd", "bbbbbbbbbbbbbbbb"}},
		{index.SortAdded, "asc", []string{"bbbbbbbbbbbbbbbb", "dddddddddddddddd", "aaaaaaaaaaaaaaaa", "eeeeeeeeeeeeeeee", "cccccccccccccccc"}},
		{index.SortBooks, "", []string{"aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb", "cccccccccccccccc", "eeeeeeeeeeeeeeee", "dddddddddddddddd"}},
		{index.SortSize, "", []string{"cccccccccccccccc", "aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb", "eeeeeeeeeeeeeeee", "dddddddddddddddd"}},
	}
	for _, tc := range tests {
		t.Run(tc.sort+"/"+tc.order, func(t *testing.T) {
			got, err := idx.ListSeries(ctx, index.SeriesFilter{Sort: tc.sort, Order: tc.order})
			if err != nil {
				t.Fatalf("ListSeries: %v", err)
			}
			if !equalStrings(ids(got.Items), tc.want) {
				t.Errorf("order = %v, want %v", ids(got.Items), tc.want)
			}
		})
	}

	for _, order := range []string{"desc", "asc"} {
		got, err := idx.ListSeries(ctx, index.SeriesFilter{Sort: index.SortRecent, Order: order})
		if err != nil {
			t.Fatalf("ListSeries recent/%s: %v", order, err)
		}
		read := ids(got.Items)[:2]
		if order == "desc" && !equalStrings(read, []string{"cccccccccccccccc", "bbbbbbbbbbbbbbbb"}) {
			t.Errorf("recent desc head = %v, want the most recently read first", read)
		}
		if order == "asc" && !equalStrings(read, []string{"bbbbbbbbbbbbbbbb", "cccccccccccccccc"}) {
			t.Errorf("recent asc head = %v", read)
		}
		for _, s := range got.Items[2:] {
			if s.Progress.LastReadAt != nil {
				t.Errorf("series %s has been read but sorted after the unread ones", s.ID)
			}
		}
	}
}

func TestListSeries_paging_totalIsCountedBeforeLimit(t *testing.T) {
	t.Parallel()
	idx, _, _ := newDBs(t)
	ctx := t.Context()
	seed(t, idx, "manga", library())

	page, err := idx.ListSeries(ctx, index.SeriesFilter{Sort: index.SortAdded, Order: "asc", Offset: 1, Limit: 2})
	if err != nil {
		t.Fatalf("ListSeries: %v", err)
	}
	if page.Total != 5 {
		t.Errorf("total = %d, want 5 (unpaged)", page.Total)
	}
	if len(page.Items) != 2 || page.Offset != 1 || page.Limit != 2 {
		t.Errorf("page = %d items at offset %d limit %d", len(page.Items), page.Offset, page.Limit)
	}
	if !equalStrings(ids(page.Items), []string{"dddddddddddddddd", "aaaaaaaaaaaaaaaa"}) {
		t.Errorf("page ids = %v", ids(page.Items))
	}

	past, err := idx.ListSeries(ctx, index.SeriesFilter{Offset: 500, Limit: 10})
	if err != nil {
		t.Fatalf("ListSeries past the end: %v", err)
	}
	if len(past.Items) != 0 || past.Total != 5 {
		t.Errorf("offset past the end = %d items, total %d", len(past.Items), past.Total)
	}
}

func TestListSeries_invalidParameters_areRejected(t *testing.T) {
	t.Parallel()
	idx, _, _ := newDBs(t)

	for _, f := range []index.SeriesFilter{
		{Sort: "wat"},
		{Order: "sideways"},
		{Status: "borked"},
		{Progress: "maybe"},
		{Limit: 201},
		{Limit: -1},
		{Offset: -1},
	} {
		_, err := idx.ListSeries(t.Context(), f)
		if !errors.Is(err, index.ErrInvalidFilter) {
			t.Errorf("ListSeries(%+v) = %v, want ErrInvalidFilter", f, err)
		}
	}
}

func TestListSeries_progressJoin_reportsSeriesRollup(t *testing.T) {
	t.Parallel()
	idx, ud, clk, _ := newDBsAt(t)
	ctx := t.Context()
	seed(t, idx, "manga", library())

	done := true
	if _, err := ud.PutProgress(ctx, userdata.ProgressUpdate{
		BookID: "b1aaaaaaaaaaaaaa", SeriesID: "aaaaaaaaaaaaaaaa", RootName: "manga",
		BookPath: "군계/01권.zip", Page: 5, PageCount: 5, Completed: &done}); err != nil {
		t.Fatalf("PutProgress: %v", err)
	}
	clk.Advance(time.Minute)
	mustPut(t, ud, "b2aaaaaaaaaaaaaa", "aaaaaaaaaaaaaaaa", 4, 7, false)

	got, err := idx.ListSeries(ctx, index.SeriesFilter{Roots: []string{"manga"}, Query: "군계"})
	if err != nil {
		t.Fatalf("ListSeries: %v", err)
	}
	if len(got.Items) != 1 {
		t.Fatalf("got %d series, want 1", len(got.Items))
	}
	s := got.Items[0]
	if s.Progress.BooksTotal != 2 || s.Progress.BooksCompleted != 1 || s.Progress.BooksStarted != 1 {
		t.Errorf("rollup = total %d completed %d started %d, want 2/1/1",
			s.Progress.BooksTotal, s.Progress.BooksCompleted, s.Progress.BooksStarted)
	}
	if s.Progress.LastBookID != "b2aaaaaaaaaaaaaa" {
		t.Errorf("last book = %q, want the most recently updated one", s.Progress.LastBookID)
	}
	if s.Progress.LastPage != 4 {
		t.Errorf("last page = %d, want 4", s.Progress.LastPage)
	}
	if s.CoverCV != "cvb1aaaaaaaaaaaaaa" {
		t.Errorf("cover cv = %q, want the cover book's content version", s.CoverCV)
	}
}

// ------------------------------------------------------------ books, pages --

func TestGetSeries_returnsBooksInOrdOrder(t *testing.T) {
	t.Parallel()
	idx, _, _ := newDBs(t)
	ctx := t.Context()
	seed(t, idx, "manga", library())

	d, err := idx.GetSeries(ctx, "aaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatalf("GetSeries: %v", err)
	}
	if len(d.Books) != 2 || d.Books[0].Ord != 0 || d.Books[1].Ord != 1 {
		t.Fatalf("books = %+v", d.Books)
	}
	if d.Books[0].ContentVersion == "" {
		t.Error("book content version is empty; page URLs cannot be versioned")
	}

	if _, err := idx.GetSeries(ctx, "nosuchseriesxxxx"); !errors.Is(err, index.ErrNotFound) {
		t.Errorf("GetSeries(unknown) = %v, want ErrNotFound", err)
	}
}

func TestNeighbours_walksTheSeriesByOrd(t *testing.T) {
	t.Parallel()
	idx, _, _ := newDBs(t)
	ctx := t.Context()
	seed(t, idx, "manga", library())

	prev, next, err := idx.Neighbours(ctx, "b1aaaaaaaaaaaaaa")
	if err != nil {
		t.Fatalf("Neighbours: %v", err)
	}
	if prev != "" || next != "b2aaaaaaaaaaaaaa" {
		t.Errorf("first book neighbours = (%q, %q), want ('', b2…)", prev, next)
	}

	prev, next, err = idx.Neighbours(ctx, "b2aaaaaaaaaaaaaa")
	if err != nil {
		t.Fatalf("Neighbours: %v", err)
	}
	if prev != "b1aaaaaaaaaaaaaa" || next != "" {
		t.Errorf("last book neighbours = (%q, %q), want (b1…, '')", prev, next)
	}

	if _, _, err := idx.Neighbours(ctx, "nosuchbookxxxxxx"); !errors.Is(err, index.ErrNotFound) {
		t.Errorf("Neighbours(unknown) = %v, want ErrNotFound", err)
	}
}

func TestPages_rangeReadAndDimensions(t *testing.T) {
	t.Parallel()
	idx, _, _ := newDBs(t)
	ctx := t.Context()
	seed(t, idx, "manga", library())

	all, err := idx.ListPages(ctx, "b2aaaaaaaaaaaaaa")
	if err != nil {
		t.Fatalf("ListPages: %v", err)
	}
	if len(all) != 7 {
		t.Fatalf("pages = %d, want 7", len(all))
	}
	for i, p := range all {
		if p.PageNo != i+1 {
			t.Fatalf("page %d has page_no %d; numbering must be 1-based and dense", i, p.PageNo)
		}
	}
	if all[0].CRC32 != 0xdeadbeef {
		t.Errorf("crc32 = %#x, want 0xdeadbeef (unsigned round-trip)", all[0].CRC32)
	}
	if all[3].LocalHdrOff == 0 {
		t.Error("local header offset is 0; FR-SRV-002 cannot seek")
	}

	rng, err := idx.PageRange(ctx, "b2aaaaaaaaaaaaaa", 3, 5)
	if err != nil {
		t.Fatalf("PageRange: %v", err)
	}
	if len(rng) != 3 || rng[0].PageNo != 3 || rng[2].PageNo != 5 {
		t.Errorf("range 3..5 = %d rows starting at %d", len(rng), rng[0].PageNo)
	}

	if _, err := idx.GetPage(ctx, "b2aaaaaaaaaaaaaa", 99); !errors.Is(err, index.ErrNotFound) {
		t.Errorf("GetPage out of range = %v, want ErrNotFound", err)
	}

	// Dimension fill is partial, then complete, and dims_state follows.
	if err := idx.UpdateDims(ctx, "b2aaaaaaaaaaaaaa", []index.PageDims{{PageNo: 1, Width: 800, Height: 1200}}); err != nil {
		t.Fatalf("UpdateDims: %v", err)
	}
	b, err := idx.GetBook(ctx, "b2aaaaaaaaaaaaaa")
	if err != nil {
		t.Fatalf("GetBook: %v", err)
	}
	if b.DimsState != "partial" {
		t.Errorf("dims_state = %q, want partial", b.DimsState)
	}
	rest := make([]index.PageDims, 0, 6)
	for i := 2; i <= 7; i++ {
		rest = append(rest, index.PageDims{PageNo: i, Width: 800, Height: 1200})
	}
	if err := idx.UpdateDims(ctx, "b2aaaaaaaaaaaaaa", rest); err != nil {
		t.Fatalf("UpdateDims: %v", err)
	}
	b, _ = idx.GetBook(ctx, "b2aaaaaaaaaaaaaa")
	if b.DimsState != "done" {
		t.Errorf("dims_state = %q, want done", b.DimsState)
	}
	p, err := idx.GetPage(ctx, "b2aaaaaaaaaaaaaa", 1)
	if err != nil {
		t.Fatalf("GetPage: %v", err)
	}
	if p.Width == nil || *p.Width != 800 {
		t.Errorf("width = %v, want 800", p.Width)
	}
}

func TestListContinue_joinsBothDatabases(t *testing.T) {
	t.Parallel()
	idx, ud, _ := newDBs(t)
	ctx := t.Context()
	seed(t, idx, "manga", library())

	mustPut(t, ud, "b1aaaaaaaaaaaaaa", "aaaaaaaaaaaaaaaa", 3, 5, false)
	mustPut(t, ud, "b4cccccccccccccc", "cccccccccccccccc", 9, 11, true)

	items, err := idx.ListContinue(ctx, 10)
	if err != nil {
		t.Fatalf("ListContinue: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("continue items = %d, want 1 (finished books are excluded)", len(items))
	}
	if items[0].Book.ID != "b1aaaaaaaaaaaaaa" || items[0].SeriesName != "군계" {
		t.Errorf("item = %+v", items[0])
	}
	if items[0].Book.Progress == nil || items[0].Book.Progress.LastPage != 3 {
		t.Errorf("progress not joined: %+v", items[0].Book.Progress)
	}
}

// -------------------------------------------------------------- the writer --

func TestWriter_replacePages_isAtomicAndRenumbers(t *testing.T) {
	t.Parallel()
	idx, _, _ := newDBs(t)
	ctx := t.Context()
	seed(t, idx, "manga", library()[:1])

	w := idx.Writer(index.WriterOptions{})
	pages := make([]index.Page, 3)
	for i := range pages {
		pages[i] = index.Page{Name: fmt.Sprintf("new%d.jpg", i), Ext: ".jpg", Size: 10}
	}
	if err := w.ReplacePages(ctx, "b1aaaaaaaaaaaaaa", pages); err != nil {
		t.Fatalf("ReplacePages: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("writer.Close: %v", err)
	}

	got, err := idx.ListPages(ctx, "b1aaaaaaaaaaaaaa")
	if err != nil {
		t.Fatalf("ListPages: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("pages after replace = %d, want 3 (the old five must be gone)", len(got))
	}
	for i, p := range got {
		if p.PageNo != i+1 || p.Name != fmt.Sprintf("new%d.jpg", i) {
			t.Errorf("page %d = %+v", i, p)
		}
	}
}

// Pages are inserted 50 rows per statement, so the boundary between the chunked
// and the single-row path is where an off-by-one would hide. 137 = 2 chunks + 37.
func TestWriter_replacePages_chunkBoundary(t *testing.T) {
	t.Parallel()
	idx, _, _ := newDBs(t)
	ctx := t.Context()
	seed(t, idx, "manga", library()[:1])

	for _, n := range []int{1, 49, 50, 51, 100, 137} {
		pages := make([]index.Page, n)
		for i := range pages {
			pages[i] = index.Page{
				Name: fmt.Sprintf("%04d.jpg", i+1), EntryPath: fmt.Sprintf("d/%04d.jpg", i+1),
				Ext: ".jpg", Size: int64(i + 1), CompSize: int64(i), Method: 8,
				LocalHdrOff: int64(i) * 100, CRC32: uint32(i + 1),
			}
		}
		w := idx.Writer(index.WriterOptions{})
		if err := w.ReplacePages(ctx, "b1aaaaaaaaaaaaaa", pages); err != nil {
			t.Fatalf("ReplacePages(%d): %v", n, err)
		}
		if err := w.Close(); err != nil {
			t.Fatalf("writer.Close: %v", err)
		}

		got, err := idx.ListPages(ctx, "b1aaaaaaaaaaaaaa")
		if err != nil {
			t.Fatalf("ListPages: %v", err)
		}
		if len(got) != n {
			t.Fatalf("%d pages in, %d out", n, len(got))
		}
		for i, p := range got {
			if p.PageNo != i+1 || p.Name != fmt.Sprintf("%04d.jpg", i+1) ||
				p.Size != int64(i+1) || p.CRC32 != uint32(i+1) ||
				p.LocalHdrOff != int64(i)*100 {
				t.Fatalf("n=%d page %d = %+v", n, i+1, p)
			}
		}
	}
}

func TestWriter_batching_commitsEveryNBooks(t *testing.T) {
	t.Parallel()
	idx, _, _ := newDBs(t)
	ctx := t.Context()
	if err := idx.UpsertRoot(ctx, index.Root{Name: "manga", Path: "/m", Enabled: true}); err != nil {
		t.Fatalf("UpsertRoot: %v", err)
	}

	w := idx.Writer(index.WriterOptions{BatchBooks: 4, BatchAge: time.Hour})
	if err := w.UpsertSeries(ctx, index.Series{
		ID: "s0000000000000000"[:16], RootName: "manga", RelPath: "s", DisplayName: "s",
		SortKey: sortKeyOf("s"), Kind: "folder", Mtime: 1, AddedAt: 1, Status: "ok", ScanGen: 1,
	}); err != nil {
		t.Fatalf("UpsertSeries: %v", err)
	}
	for i := range 4 {
		if err := w.UpsertBook(ctx, index.Book{
			ID: fmt.Sprintf("bk%014d", i), SeriesID: "s0000000000000000"[:16], RootName: "manga",
			RelPath: fmt.Sprintf("s/%d.zip", i), DisplayName: fmt.Sprintf("%d.zip", i),
			SortKey: sortKeyOf("x"), Ord: i, Kind: "zip", ContentVersion: "cv", Status: "ok", ScanGen: 1,
		}); err != nil {
			t.Fatalf("UpsertBook %d: %v", i, err)
		}
	}
	// The fourth book hits the batch size, so the rows are visible to a reader
	// on another connection without an explicit Flush.
	books, err := idx.ListBooks(ctx, "s0000000000000000"[:16])
	if err != nil {
		t.Fatalf("ListBooks: %v", err)
	}
	if len(books) != 4 {
		t.Errorf("books visible before Close = %d, want 4 (batch of 4 must have committed)", len(books))
	}
	if err := w.Close(); err != nil {
		t.Fatalf("writer.Close: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Errorf("second Close = %v, want nil", err)
	}
	err = w.UpsertBook(ctx, index.Book{ID: "after-close", SeriesID: "s0000000000000000"[:16]})
	if !errors.Is(err, index.ErrWriterClosed) {
		t.Errorf("writing through a closed writer = %v, want ErrWriterClosed", err)
	}
}

// Writer.AfterCommit is the primitive behind FR-THM-003's cover enqueue: work
// published *about* rows must not run while those rows are invisible to every
// other connection. The whole contract in one test — deferred while the batch
// is open, run once it commits, and able to see the rows when it runs.
func TestWriter_afterCommit_runsOnlyWhenTheRowsAreReadable(t *testing.T) {
	t.Parallel()
	idx, _, _ := newDBs(t)
	ctx := t.Context()
	if err := idx.UpsertRoot(ctx, index.Root{Name: "manga", Path: "/m", Enabled: true}); err != nil {
		t.Fatalf("UpsertRoot: %v", err)
	}
	sid := "s0000000000000000"[:16]

	// A batch far larger than this test writes, so nothing commits by accident.
	w := idx.Writer(index.WriterOptions{BatchBooks: 1000, BatchAge: time.Hour})

	// No batch is open yet, so there is nothing to wait for: the callback runs
	// straight away rather than being stranded until the next commit.
	immediate := 0
	w.AfterCommit(func() { immediate++ })
	if immediate != 1 {
		t.Errorf("AfterCommit with no open batch ran %d times, want 1", immediate)
	}

	if err := w.UpsertSeries(ctx, index.Series{
		ID: sid, RootName: "manga", RelPath: "s", DisplayName: "s",
		SortKey: sortKeyOf("s"), Kind: "folder", Mtime: 1, AddedAt: 1, Status: "ok", ScanGen: 1,
	}); err != nil {
		t.Fatalf("UpsertSeries: %v", err)
	}
	if err := w.UpsertBook(ctx, index.Book{
		ID: "bk00000000000000", SeriesID: sid, RootName: "manga",
		RelPath: "s/1.zip", DisplayName: "1.zip", SortKey: sortKeyOf("x"),
		Ord: 0, Kind: "zip", ContentVersion: "cv", Status: "ok", ScanGen: 1,
	}); err != nil {
		t.Fatalf("UpsertBook: %v", err)
	}

	var order []string
	var sawBook error
	w.AfterCommit(func() {
		// The reader pool, never the writer connection — exactly what
		// internal/thumbs uses to resolve a cover.
		_, sawBook = idx.GetBook(ctx, "bk00000000000000")
		order = append(order, "first")
	})
	w.AfterCommit(func() { order = append(order, "second") })
	w.AfterCommit(nil) // a nil hook is ignored, not a panic

	if len(order) != 0 {
		t.Fatalf("hooks ran %v before the batch committed; they must wait", order)
	}
	if _, err := idx.GetBook(ctx, "bk00000000000000"); !errors.Is(err, index.ErrNotFound) {
		t.Fatalf("mid-batch GetBook = %v, want ErrNotFound (the premise of this test)", err)
	}

	if err := w.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if len(order) != 2 || order[0] != "first" || order[1] != "second" {
		t.Errorf("hooks ran %v, want [first second] in registration order", order)
	}
	if sawBook != nil {
		t.Errorf("the hook could not read the row it was queued behind: %v", sawBook)
	}

	// A committed batch clears its hooks: a second Flush re-runs nothing.
	if err := w.Flush(ctx); err != nil {
		t.Fatalf("second Flush: %v", err)
	}
	if len(order) != 2 {
		t.Errorf("hooks ran again on an empty Flush: %v", order)
	}

	// A batch that never commits drops its hooks with its rows. UpsertBook with
	// a series_id no series owns violates the foreign key, which rolls the whole
	// batch back.
	rolled := 0
	if err := w.UpsertSeries(ctx, index.Series{
		ID: "s1000000000000000"[:16], RootName: "manga", RelPath: "t", DisplayName: "t",
		SortKey: sortKeyOf("t"), Kind: "folder", Mtime: 1, AddedAt: 1, Status: "ok", ScanGen: 1,
	}); err != nil {
		t.Fatalf("UpsertSeries: %v", err)
	}
	w.AfterCommit(func() { rolled++ })
	if err := w.UpsertBook(ctx, index.Book{
		ID: "bk00000000000001", SeriesID: "nosuchseries0000", RootName: "manga",
		RelPath: "t/1.zip", DisplayName: "1.zip", SortKey: sortKeyOf("x"),
		Ord: 0, Kind: "zip", ContentVersion: "cv", Status: "ok", ScanGen: 1,
	}); err == nil {
		t.Fatal("UpsertBook with a dangling series_id succeeded; PRAGMA foreign_keys must be ON")
	}
	if err := w.Close(); err != nil {
		t.Fatalf("writer.Close: %v", err)
	}
	if rolled != 0 {
		t.Errorf("a rolled-back batch ran %d hooks; they describe rows that no longer exist", rolled)
	}
}

func TestWriter_stampAndSweep_removesOnlyStaleRowsOfOneRoot(t *testing.T) {
	t.Parallel()
	idx, _, _ := newDBs(t)
	ctx := t.Context()
	seed(t, idx, "manga", library())
	seed(t, idx, "novels", []seedSeries{
		{id: "ffffffffffffffff", name: "소설", sortKey: "1", searchKey: "소설", choseong: "ㅅㅅ",
			mtime: 1, addedAt: 1,
			books: []seedBook{{id: "b6ffffffffffffff", name: "n1.zip", ord: 0, kind: "zip", pages: 4}}},
	})

	gen, err := idx.NextScanGen(ctx)
	if err != nil {
		t.Fatalf("NextScanGen: %v", err)
	}
	if gen != 1 {
		t.Fatalf("first generation = %d, want 1", gen)
	}
	gen, _ = idx.NextScanGen(ctx)
	if gen != 2 {
		t.Fatalf("second generation = %d, want 2", gen)
	}

	// Only 군계 survives this run of the manga root.
	w := idx.Writer(index.WriterOptions{})
	if err := w.StampGen(ctx, gen,
		[]string{"aaaaaaaaaaaaaaaa"},
		[]string{"b1aaaaaaaaaaaaaa", "b2aaaaaaaaaaaaaa"}); err != nil {
		t.Fatalf("StampGen: %v", err)
	}
	res, err := w.SweepRoot(ctx, "manga", gen)
	if err != nil {
		t.Fatalf("SweepRoot: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("writer.Close: %v", err)
	}

	if res.Series != 4 || res.Books != 3 {
		t.Errorf("sweep removed %d series and %d books, want 4 and 3", res.Series, res.Books)
	}

	left, err := idx.ListSeries(ctx, index.SeriesFilter{Roots: []string{"manga"}})
	if err != nil {
		t.Fatalf("ListSeries: %v", err)
	}
	if !equalStrings(ids(left.Items), []string{"aaaaaaaaaaaaaaaa"}) {
		t.Errorf("manga survivors = %v, want only 군계", ids(left.Items))
	}

	// The other root is untouched — an unmounted drive must not erase a library.
	other, err := idx.ListSeries(ctx, index.SeriesFilter{Roots: []string{"novels"}})
	if err != nil {
		t.Fatalf("ListSeries(novels): %v", err)
	}
	if other.Total != 1 {
		t.Errorf("novels total = %d, want 1", other.Total)
	}

	// pages has no foreign key, so the sweep must remove page rows by hand.
	orphans, err := idx.CountOrphanPages(ctx)
	if err != nil {
		t.Fatalf("CountOrphanPages: %v", err)
	}
	if orphans != 0 {
		t.Errorf("orphan page rows after sweep = %d, want 0", orphans)
	}
	if res.Pages != 16 {
		t.Errorf("sweep removed %d page rows, want 16", res.Pages)
	}
}

// StampGen is handed one id per unchanged book, so its id list is bounded by the
// size of the library and nothing else. An oversized IN(?,…) list fails with
// "too many SQL variables" — and because SweepRoot then deletes every row still
// at the older generation, a caller that logged and carried on would erase the
// root it had just confirmed unchanged. This test asserts the consequence, not
// only the call: after stamping 45 000 ids the sweep must remove nothing.
func TestWriter_stampGen_chunksUnboundedIDLists(t *testing.T) {
	t.Parallel()
	idx, _, _ := newDBs(t)
	ctx := t.Context()

	const (
		realBooks = 120
		totalIDs  = 45_000
	)
	if err := idx.UpsertRoot(ctx, index.Root{Name: "manga", Path: "/m", Enabled: true}); err != nil {
		t.Fatalf("UpsertRoot: %v", err)
	}
	// Both database-level writes happen before the batch opens: they take the
	// same write permit the Writer holds (see the Writer docs).
	gen, err := idx.NextScanGen(ctx)
	if err != nil {
		t.Fatalf("NextScanGen: %v", err)
	}
	seriesID := "cccccccccccccccc"
	w := idx.Writer(index.WriterOptions{})
	if err := w.UpsertSeries(ctx, index.Series{
		ID: seriesID, RootName: "manga", RelPath: "big", DisplayName: "big",
		SortKey: sortKeyOf("big"), Kind: "folder", Mtime: 1, AddedAt: 1, Status: "ok", ScanGen: 1,
	}); err != nil {
		t.Fatalf("UpsertSeries: %v", err)
	}
	bookIDs := make([]string, 0, realBooks)
	for i := range realBooks {
		id := fmt.Sprintf("real%012d", i)
		if err := w.UpsertBook(ctx, index.Book{
			ID: id, SeriesID: seriesID, RootName: "manga",
			RelPath: fmt.Sprintf("big/%d.zip", i), DisplayName: fmt.Sprintf("%d.zip", i),
			SortKey: sortKeyOf("x"), Ord: i, Kind: "zip", ContentVersion: "cv",
			Status: "ok", ScanGen: 1,
		}); err != nil {
			t.Fatalf("UpsertBook %d: %v", i, err)
		}
		bookIDs = append(bookIDs, id)
	}

	// The real ids are spread across the whole list, so a chunking bug that
	// dropped any single batch shows up as deleted rows.
	stamp := make([]string, 0, totalIDs)
	every := totalIDs / realBooks
	for i := range totalIDs {
		if i%every == 0 && len(bookIDs) > 0 {
			stamp = append(stamp, bookIDs[0])
			bookIDs = bookIDs[1:]
			continue
		}
		stamp = append(stamp, fmt.Sprintf("ghost%011d", i))
	}
	stamp = append(stamp, bookIDs...)

	if err := w.StampGen(ctx, gen, []string{seriesID}, stamp); err != nil {
		t.Fatalf("StampGen with %d ids: %v", len(stamp), err)
	}
	res, err := w.SweepRoot(ctx, "manga", gen)
	if err != nil {
		t.Fatalf("SweepRoot: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("writer.Close: %v", err)
	}
	if res.Books != 0 || res.Series != 0 || res.Pages != 0 {
		t.Errorf("sweep after a full stamp removed %+v, want nothing", res)
	}
	books, err := idx.ListBooks(ctx, seriesID)
	if err != nil {
		t.Fatalf("ListBooks: %v", err)
	}
	if len(books) != realBooks {
		t.Errorf("books surviving the sweep = %d, want %d", len(books), realBooks)
	}
	for _, b := range books {
		if b.ScanGen != gen {
			t.Fatalf("book %s is still at generation %d, want %d", b.ID, b.ScanGen, gen)
		}
	}
}

func TestDeleteSeries_removesPagesToo(t *testing.T) {
	t.Parallel()
	idx, _, _ := newDBs(t)
	ctx := t.Context()
	seed(t, idx, "manga", library())

	if err := idx.DeleteSeries(ctx, "aaaaaaaaaaaaaaaa"); err != nil {
		t.Fatalf("DeleteSeries: %v", err)
	}
	orphans, err := idx.CountOrphanPages(ctx)
	if err != nil {
		t.Fatalf("CountOrphanPages: %v", err)
	}
	if orphans != 0 {
		t.Errorf("orphan pages after DeleteSeries = %d, want 0", orphans)
	}
	if n, _ := idx.CountPages(ctx, "b1aaaaaaaaaaaaaa"); n != 0 {
		t.Errorf("pages of a deleted book = %d, want 0", n)
	}
}

// ------------------------------------------------------------------- log ----

func TestScanLog_appendListAndTrim(t *testing.T) {
	t.Parallel()
	idx, _, _ := newDBs(t)
	ctx := t.Context()

	entries := make([]index.LogEntry, 0, 30)
	for i := range 30 {
		level := index.LevelInfo
		if i%3 == 0 {
			level = index.LevelWarn
		}
		entries = append(entries, index.LogEntry{
			TS: int64(1000 + i), RunID: "run-a", Level: level,
			Root: "manga", RelPath: fmt.Sprintf("s/%d.zip", i),
			Message: fmt.Sprintf("entry %d", i),
		})
	}
	if err := idx.AppendLog(ctx, entries...); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}
	if err := idx.AppendLog(ctx, index.LogEntry{
		TS: 2000, RunID: "run-b", Level: index.LevelError, Message: "boom"}); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}

	newest, err := idx.ListLog(ctx, index.LogFilter{Limit: 3})
	if err != nil {
		t.Fatalf("ListLog: %v", err)
	}
	if len(newest) != 3 || newest[0].Message != "boom" {
		t.Errorf("newest first = %+v", newest)
	}

	warns, err := idx.ListLog(ctx, index.LogFilter{Level: index.LevelWarn, Limit: 100})
	if err != nil {
		t.Fatalf("ListLog(warn): %v", err)
	}
	if len(warns) != 10 {
		t.Errorf("warn rows = %d, want 10", len(warns))
	}

	byRun, err := idx.ListLog(ctx, index.LogFilter{RunID: "run-b", Limit: 100})
	if err != nil {
		t.Fatalf("ListLog(run): %v", err)
	}
	if len(byRun) != 1 {
		t.Errorf("run-b rows = %d, want 1", len(byRun))
	}

	// since_id is an ascending incremental cursor.
	since, err := idx.ListLog(ctx, index.LogFilter{SinceID: newest[2].ID, Limit: 100})
	if err != nil {
		t.Fatalf("ListLog(since): %v", err)
	}
	if len(since) != 2 || since[0].ID >= since[1].ID {
		t.Errorf("since_id result = %+v, want 2 rows in ascending id order", since)
	}

	if _, err := idx.ListLog(ctx, index.LogFilter{Level: "shout"}); !errors.Is(err, index.ErrInvalidFilter) {
		t.Errorf("ListLog(bad level) = %v, want ErrInvalidFilter", err)
	}

	deleted, err := idx.TrimLog(ctx, 5)
	if err != nil {
		t.Fatalf("TrimLog: %v", err)
	}
	if deleted != 26 {
		t.Errorf("trim removed %d rows, want 26", deleted)
	}
	left, _ := idx.ListLog(ctx, index.LogFilter{Limit: 100})
	if len(left) != 5 {
		t.Errorf("rows after trim = %d, want 5", len(left))
	}
	// AUTOINCREMENT: ids never come back after a trim, so a poller's cursor is
	// still meaningful.
	if left[0].ID <= newest[2].ID {
		t.Errorf("ids were reused after the trim: %d", left[0].ID)
	}
}

// FR-IDX-010 puts one warn row per isolated failure in the middle of a scan,
// i.e. inside an open Writer batch, on a scan context that carries no deadline.
// Through DB.AppendLog that is a permanent deadlock on the write permit — the
// batch is only committed by the very goroutine that would be blocked. The
// Writer therefore owns the in-batch route, and this test drives exactly the
// FR-IDX-010 shape: log, keep writing, flush, read back.
func TestWriter_appendLog_insideAnOpenBatch(t *testing.T) {
	t.Parallel()
	idx, _, _ := newDBs(t)
	ctx := t.Context()
	if err := idx.UpsertRoot(ctx, index.Root{Name: "manga", Path: "/m", Enabled: true}); err != nil {
		t.Fatalf("UpsertRoot: %v", err)
	}

	// BatchAge is an hour and BatchBooks 100, so the batch stays open for the
	// whole of the body below — the hazardous window, deliberately widened.
	w := idx.Writer(index.WriterOptions{BatchBooks: 100, BatchAge: time.Hour})
	seriesID := "dddddddddddddddd"

	done := make(chan error, 1)
	go func() {
		// context.Background(), not t.Context(): a deadline would mask the bug
		// as a timeout, and a scanner has no deadline to offer.
		bg := context.Background()
		if err := w.UpsertSeries(bg, index.Series{
			ID: seriesID, RootName: "manga", RelPath: "s", DisplayName: "s",
			SortKey: sortKeyOf("s"), Kind: "folder", Mtime: 1, AddedAt: 1, Status: "ok", ScanGen: 1,
		}); err != nil {
			done <- err
			return
		}
		if err := w.UpsertBook(bg, index.Book{
			ID: "broken0000000000", SeriesID: seriesID, RootName: "manga",
			RelPath: "s/07.zip", DisplayName: "07.zip", SortKey: sortKeyOf("07"),
			Kind: "zip", ContentVersion: "cv", Status: "error",
			Error: "truncated central directory", ScanGen: 1,
		}); err != nil {
			done <- err
			return
		}
		if err := w.AppendLog(bg, index.LogEntry{
			TS: 1000, RunID: "run-a", Level: index.LevelWarn, Root: "manga",
			RelPath: "s/07.zip", Message: "truncated central directory",
		}); err != nil {
			done <- err
			return
		}
		// The batch must still be usable afterwards.
		if err := w.UpsertBook(bg, index.Book{
			ID: "fine000000000000", SeriesID: seriesID, RootName: "manga",
			RelPath: "s/08.zip", DisplayName: "08.zip", SortKey: sortKeyOf("08"),
			Kind: "zip", ContentVersion: "cv", Status: "ok", ScanGen: 1,
		}); err != nil {
			done <- err
			return
		}
		done <- w.Close()
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("scan-shaped batch with an in-batch log append: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("logging a scan failure from inside an open batch never returned: " +
			"the write permit is held by the goroutine that would release it")
	}

	rows, err := idx.ListLog(ctx, index.LogFilter{Level: index.LevelWarn, Limit: 10})
	if err != nil {
		t.Fatalf("ListLog: %v", err)
	}
	if len(rows) != 1 || rows[0].Message != "truncated central directory" ||
		rows[0].RelPath != "s/07.zip" || rows[0].Root != "manga" {
		t.Errorf("scan log after the batch = %+v, want one warn row for s/07.zip", rows)
	}
	books, err := idx.ListBooks(ctx, seriesID)
	if err != nil {
		t.Fatalf("ListBooks: %v", err)
	}
	if len(books) != 2 {
		t.Errorf("books after the batch = %d, want 2 (the scan continues past an error)", len(books))
	}
}

// ------------------------------------------------------------ concurrency --

// impl-plan WP-03 acceptance 2: 64 goroutines against an 8-connection pool doing
// cross-database joins, zero errors.
func TestAttach_64GoroutinesOn8Connections_crossDatabaseJoin(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ctx := t.Context()

	ud, err := userdata.Open(ctx, userdata.Options{
		Path: filepath.Join(dir, "user.db"), Logger: quietLogger()})
	if err != nil {
		t.Fatalf("userdata.Open: %v", err)
	}
	defer func() { _ = ud.Close() }()

	idx, err := index.Open(ctx, index.Options{
		Path:         filepath.Join(dir, "index.db"),
		UserPath:     filepath.Join(dir, "user.db"),
		MaxOpenConns: 8,
		Logger:       quietLogger(),
	})
	if err != nil {
		t.Fatalf("index.Open: %v", err)
	}
	defer func() { _ = idx.Close() }()

	seed(t, idx, "manga", library())
	mustPut(t, ud, "b1aaaaaaaaaaaaaa", "aaaaaaaaaaaaaaaa", 2, 5, false)

	var wg sync.WaitGroup
	errCh := make(chan error, 64*4)
	for g := range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 8 {
				f := index.SeriesFilter{Sort: index.SortRecent}
				if (g+i)%3 == 0 {
					f.Progress = index.ProgressReading
				}
				if _, err := idx.ListSeries(ctx, f); err != nil {
					errCh <- fmt.Errorf("goroutine %d: ListSeries: %w", g, err)
					return
				}
				if _, err := idx.ListContinue(ctx, 5); err != nil {
					errCh <- fmt.Errorf("goroutine %d: ListContinue: %w", g, err)
					return
				}
				if _, err := idx.GetSeries(ctx, "aaaaaaaaaaaaaaaa"); err != nil {
					errCh <- fmt.Errorf("goroutine %d: GetSeries: %w", g, err)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
}

func TestConcurrentReaders_duringASustainedWrite(t *testing.T) {
	t.Parallel()
	idx, _, _ := newDBs(t)
	ctx := t.Context()
	seed(t, idx, "manga", library())

	done := make(chan struct{})
	errCh := make(chan error, 32)

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
				}
				if _, err := idx.ListSeries(ctx, index.SeriesFilter{}); err != nil {
					errCh <- fmt.Errorf("read during write: %w", err)
					return
				}
				if _, err := idx.ListPages(ctx, "b2aaaaaaaaaaaaaa"); err != nil {
					errCh <- fmt.Errorf("page read during write: %w", err)
					return
				}
			}
		}()
	}

	w := idx.Writer(index.WriterOptions{BatchBooks: 10, BatchAge: 50 * time.Millisecond})
	for i := range 300 {
		err := w.UpsertBook(ctx, index.Book{
			ID: fmt.Sprintf("wb%014d", i), SeriesID: "aaaaaaaaaaaaaaaa", RootName: "manga",
			RelPath: fmt.Sprintf("군계/w%d.zip", i), DisplayName: fmt.Sprintf("w%d.zip", i),
			SortKey: sortKeyOf("w"), Ord: i + 2, Kind: "zip", ContentVersion: "cv",
			Status: "ok", ScanGen: 1,
		})
		if err != nil {
			t.Errorf("UpsertBook %d: %v", i, err)
			break
		}
	}
	if err := w.Close(); err != nil {
		t.Errorf("writer.Close: %v", err)
	}
	close(done)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}

	books, err := idx.ListBooks(ctx, "aaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatalf("ListBooks: %v", err)
	}
	if len(books) != 302 {
		t.Errorf("books after the write = %d, want 302", len(books))
	}
}

// An open scan batch on the index connection must not stop the API from writing
// reading progress. It did, until the index connection stopped using
// BEGIN IMMEDIATE: that starts a write transaction on every ATTACHed database,
// so index.db's writer held user.db's WAL writer lock for the whole batch and
// every page turn during a scan failed with SQLITE_BUSY.
func TestWriter_openBatch_doesNotLockTheUserDatabase(t *testing.T) {
	t.Parallel()
	idx, ud, _ := newDBs(t)
	ctx := t.Context()
	seed(t, idx, "manga", library()[:1])

	// A long batch: neither threshold can fire while progress is written.
	w := idx.Writer(index.WriterOptions{BatchBooks: 1_000_000, BatchAge: time.Hour})
	defer func() { _ = w.Close() }()
	if err := w.UpsertBook(ctx, index.Book{
		ID: "b9aaaaaaaaaaaaaa", SeriesID: "aaaaaaaaaaaaaaaa", RootName: "manga",
		RelPath: "군계/09권.zip", DisplayName: "09권.zip", SortKey: sortKeyOf("09"),
		Ord: 9, Kind: "zip", ContentVersion: "cv", Status: "ok", ScanGen: 1,
	}); err != nil {
		t.Fatalf("UpsertBook: %v", err)
	}

	writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := ud.PutProgress(writeCtx, userdata.ProgressUpdate{
		BookID: "b1aaaaaaaaaaaaaa", SeriesID: "aaaaaaaaaaaaaaaa", RootName: "manga",
		BookPath: "군계/01권.zip", Page: 2, PageCount: 5,
	}); err != nil {
		t.Fatalf("writing progress while a scan batch is open: %v", err)
	}

	// And the index can still be read across both databases mid-batch.
	if _, err := idx.ListSeries(ctx, index.SeriesFilter{Progress: index.ProgressReading}); err != nil {
		t.Fatalf("cross-database read during a scan batch: %v", err)
	}
}

// ----------------------------------------------------- rebuild / AC-006 -----

// AC-006 and FR-IDX-005, the whole point of two files: delete index.db
// entirely, rebuild it from scratch, and prove the authored data survived.
func TestRebuildIndex_destroysDerivedDataAndPreservesUserData(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ctx := t.Context()
	idx, ud, _ := openDBsIn(t, dir, newClock())
	seed(t, idx, "manga", library())

	mustPut(t, ud, "b1aaaaaaaaaaaaaa", "aaaaaaaaaaaaaaaa", 3, 5, false)
	done := true
	if _, err := ud.PutProgress(ctx, userdata.ProgressUpdate{
		BookID: "b2aaaaaaaaaaaaaa", SeriesID: "aaaaaaaaaaaaaaaa", RootName: "manga",
		BookPath: "군계/02권.zip", Page: 7, PageCount: 7, Completed: &done}); err != nil {
		t.Fatalf("PutProgress: %v", err)
	}
	if _, err := ud.PutPrefs(ctx, "b1aaaaaaaaaaaaaa", userdata.PrefsPatch{
		ReadingDir: userdata.SetPatch("rtl")}); err != nil {
		t.Fatalf("PutPrefs: %v", err)
	}
	if err := ud.Settings().Put(ctx, "theme", `"dark"`); err != nil {
		t.Fatalf("Settings.Put: %v", err)
	}

	// Close only the index — the rebuild does not stop the user database.
	if err := idx.Close(); err != nil {
		t.Fatalf("index.Close: %v", err)
	}

	// FR-IDX-005: delete index.db and its sidecars, and nothing else.
	before := dirEntries(t, dir)
	if err := index.Destroy(filepath.Join(dir, "index.db")); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	for _, f := range index.DBFiles(filepath.Join(dir, "index.db")) {
		if _, err := os.Stat(f); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s still exists after Destroy", filepath.Base(f))
		}
	}
	after := dirEntries(t, dir)
	for name := range before {
		if strings.HasPrefix(name, "index.db") {
			continue
		}
		if !after[name] {
			t.Errorf("Destroy removed %s, which is not an index file", name)
		}
	}
	if !after["user.db"] {
		t.Fatal("user.db was deleted by the index rebuild")
	}

	// Rebuild: a fresh index, rescanned. The ids are path-derived, so the
	// scanner reproduces them byte for byte (arch §3.4).
	idx2, err := index.Open(ctx, index.Options{
		Path:     filepath.Join(dir, "index.db"),
		UserPath: filepath.Join(dir, "user.db"),
		Logger:   quietLogger(),
	})
	if err != nil {
		t.Fatalf("reopening a destroyed index: %v", err)
	}
	defer func() { _ = idx2.Close() }()

	if list, err := idx2.ListSeries(ctx, index.SeriesFilter{}); err != nil {
		t.Fatalf("ListSeries on the empty index: %v", err)
	} else if list.Total != 0 {
		t.Fatalf("rebuilt index is not empty: total = %d", list.Total)
	}

	// The authored data is still there, before any rescan.
	p, err := ud.GetProgress(ctx, "b1aaaaaaaaaaaaaa")
	if err != nil {
		t.Fatalf("GetProgress after rebuild: %v", err)
	}
	if p.LastPage != 3 || p.PageCount != 5 || p.Completed {
		t.Errorf("progress after rebuild = %+v, want last_page 3 of 5, not completed", p)
	}
	prefs, err := ud.GetPrefs(ctx, "b1aaaaaaaaaaaaaa")
	if err != nil {
		t.Fatalf("GetPrefs after rebuild: %v", err)
	}
	if prefs.ReadingDir == nil || *prefs.ReadingDir != "rtl" {
		t.Errorf("reading direction after rebuild = %v, want rtl", prefs.ReadingDir)
	}
	if v, ok, _ := ud.Settings().Get(ctx, "theme"); !ok || v != `"dark"` {
		t.Errorf("setting after rebuild = %q (present %v), want \"dark\"", v, ok)
	}

	// And a rescan re-joins it, because the ids did not move.
	seed(t, idx2, "manga", library())
	d, err := idx2.GetSeries(ctx, "aaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatalf("GetSeries after rescan: %v", err)
	}
	if len(d.Books) != 2 {
		t.Fatalf("books after rescan = %d", len(d.Books))
	}
	if d.Books[0].Progress == nil || d.Books[0].Progress.LastPage != 3 {
		t.Errorf("progress did not rejoin the rebuilt index: %+v", d.Books[0].Progress)
	}
	if d.Progress.BooksCompleted != 1 {
		t.Errorf("series rollup after rebuild = %d completed, want 1", d.Progress.BooksCompleted)
	}
}

func TestReset_emptiesTheIndexWithoutTouchingUserData(t *testing.T) {
	t.Parallel()
	idx, ud, _ := newDBs(t)
	ctx := t.Context()
	seed(t, idx, "manga", library())
	mustPut(t, ud, "b1aaaaaaaaaaaaaa", "aaaaaaaaaaaaaaaa", 4, 5, false)

	if err := idx.Reset(ctx); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	list, err := idx.ListSeries(ctx, index.SeriesFilter{})
	if err != nil {
		t.Fatalf("ListSeries after Reset: %v", err)
	}
	if list.Total != 0 {
		t.Errorf("series after Reset = %d, want 0", list.Total)
	}
	if v, err := idx.SchemaVersion(ctx); err != nil || v != 1 {
		t.Errorf("schema version after Reset = %d (%v), want 1", v, err)
	}
	if _, err := ud.GetProgress(ctx, "b1aaaaaaaaaaaaaa"); err != nil {
		t.Errorf("Reset destroyed reading progress: %v", err)
	}
}

func TestDestroy_missingFiles_isNotAnError(t *testing.T) {
	t.Parallel()
	if err := index.Destroy(filepath.Join(t.TempDir(), "nothing-here.db")); err != nil {
		t.Errorf("Destroy on a missing file = %v, want nil", err)
	}
}

// ------------------------------------------------------------- benchmarks --

// impl-plan WP-03 acceptance 4: 1 000 series with the progress join under 20 ms.
func BenchmarkListSeries_1000WithProgressJoin(b *testing.B) {
	dir := b.TempDir()
	ctx := b.Context()
	ud, err := userdata.Open(ctx, userdata.Options{Path: filepath.Join(dir, "user.db"), Logger: quietLogger()})
	if err != nil {
		b.Fatalf("userdata.Open: %v", err)
	}
	defer func() { _ = ud.Close() }()
	idx, err := index.Open(ctx, index.Options{
		Path: filepath.Join(dir, "index.db"), UserPath: filepath.Join(dir, "user.db"),
		Logger: quietLogger()})
	if err != nil {
		b.Fatalf("index.Open: %v", err)
	}
	defer func() { _ = idx.Close() }()

	if err := idx.UpsertRoot(ctx, index.Root{Name: "manga", Path: "/m", Enabled: true}); err != nil {
		b.Fatalf("UpsertRoot: %v", err)
	}
	w := idx.Writer(index.WriterOptions{})
	for i := range 1000 {
		sid := fmt.Sprintf("s%015d", i)
		if err := w.UpsertSeries(ctx, index.Series{
			ID: sid, RootName: "manga", RelPath: fmt.Sprintf("series %d", i),
			DisplayName: fmt.Sprintf("series %d", i), SortKey: []byte(fmt.Sprintf("%08d", i)),
			SearchKey: fmt.Sprintf("series %d", i), ChoseongKey: "s", Kind: "folder",
			BookCount: 3, PageCount: 300, TotalBytes: int64(i) * 1000,
			Mtime: int64(i), AddedAt: int64(i), Status: "ok", ScanGen: 1,
		}); err != nil {
			b.Fatalf("UpsertSeries: %v", err)
		}
		for j := range 3 {
			bid := fmt.Sprintf("b%09d%05d", i, j)
			if err := w.UpsertBook(ctx, index.Book{
				ID: bid, SeriesID: sid, RootName: "manga",
				RelPath: fmt.Sprintf("series %d/%d.zip", i, j), DisplayName: "x.zip",
				SortKey: []byte("x"), Ord: j, Kind: "zip", PageCount: 100,
				ContentVersion: "cv", Status: "ok", ScanGen: 1,
			}); err != nil {
				b.Fatalf("UpsertBook: %v", err)
			}
			if j == 0 {
				if _, err := ud.PutProgress(ctx, userdata.ProgressUpdate{
					BookID: bid, SeriesID: sid, RootName: "manga", BookPath: "x",
					Page: 50, PageCount: 100}); err != nil {
					b.Fatalf("PutProgress: %v", err)
				}
			}
		}
	}
	if err := w.Close(); err != nil {
		b.Fatalf("writer.Close: %v", err)
	}

	// name is the default and the index-driven path; recent is the worst case
	// because it must sort on the joined progress column.
	for _, sortKey := range []string{index.SortName, index.SortRecent} {
		b.Run(sortKey, func(b *testing.B) {
			for b.Loop() {
				if _, err := idx.ListSeries(ctx, index.SeriesFilter{Sort: sortKey, Limit: 60}); err != nil {
					b.Fatalf("ListSeries: %v", err)
				}
			}
		})
	}
	b.Run("reading-filter", func(b *testing.B) {
		for b.Loop() {
			if _, err := idx.ListSeries(ctx, index.SeriesFilter{
				Progress: index.ProgressReading, Limit: 60}); err != nil {
				b.Fatalf("ListSeries: %v", err)
			}
		}
	})
}

func BenchmarkWriter_ReplacePages_1000(b *testing.B) {
	dir := b.TempDir()
	ctx := b.Context()
	ud, err := userdata.Open(ctx, userdata.Options{Path: filepath.Join(dir, "user.db"), Logger: quietLogger()})
	if err != nil {
		b.Fatalf("userdata.Open: %v", err)
	}
	defer func() { _ = ud.Close() }()
	idx, err := index.Open(ctx, index.Options{
		Path: filepath.Join(dir, "index.db"), UserPath: filepath.Join(dir, "user.db"),
		Logger: quietLogger()})
	if err != nil {
		b.Fatalf("index.Open: %v", err)
	}
	defer func() { _ = idx.Close() }()

	if err := idx.UpsertRoot(ctx, index.Root{Name: "m", Path: "/m", Enabled: true}); err != nil {
		b.Fatalf("UpsertRoot: %v", err)
	}
	w := idx.Writer(index.WriterOptions{})
	defer func() { _ = w.Close() }()
	if err := w.UpsertSeries(ctx, index.Series{
		ID: "sssssssssssssss1", RootName: "m", RelPath: "s", DisplayName: "s",
		SortKey: []byte("s"), Kind: "folder", Mtime: 1, AddedAt: 1, Status: "ok", ScanGen: 1}); err != nil {
		b.Fatalf("UpsertSeries: %v", err)
	}
	if err := w.UpsertBook(ctx, index.Book{
		ID: "bbbbbbbbbbbbbbb1", SeriesID: "sssssssssssssss1", RootName: "m", RelPath: "s/b.zip",
		DisplayName: "b.zip", SortKey: []byte("b"), Kind: "zip", ContentVersion: "cv",
		Status: "ok", ScanGen: 1}); err != nil {
		b.Fatalf("UpsertBook: %v", err)
	}
	pages := make([]index.Page, 1000)
	for i := range pages {
		pages[i] = index.Page{Name: fmt.Sprintf("%04d.jpg", i), EntryPath: fmt.Sprintf("%04d.jpg", i),
			Ext: ".jpg", Size: 4096, CompSize: 4000, Method: 8, LocalHdrOff: int64(i) * 4096}
	}

	// One iteration = a 1 000-page archive re-indexed end to end: delete the old
	// rows, insert 1 000 new ones in 50-row statements, commit.
	for b.Loop() {
		if err := w.ReplacePages(ctx, "bbbbbbbbbbbbbbb1", pages); err != nil {
			b.Fatalf("ReplacePages: %v", err)
		}
		if err := w.Flush(ctx); err != nil {
			b.Fatalf("Flush: %v", err)
		}
	}
}

// ----------------------------------------------------------------- helpers --

func mustPut(t *testing.T, ud *userdata.DB, bookID, seriesID string, page, total int, completed bool) {
	t.Helper()
	if _, err := ud.PutProgress(t.Context(), userdata.ProgressUpdate{
		BookID: bookID, SeriesID: seriesID, RootName: "manga", BookPath: bookID,
		Page: page, PageCount: total, Completed: &completed,
	}); err != nil {
		t.Fatalf("PutProgress %s: %v", bookID, err)
	}
}

// openRaw opens a database with no schema handling, so the tests can inspect and
// forge what the package under test refuses to.
func openRaw(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("opening %s raw: %v", path, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func journalMode(t *testing.T, path string) string {
	t.Helper()
	var mode string
	if err := openRaw(t, path).QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("reading journal mode of %s: %v", path, err)
	}
	return mode
}

func tableColumns(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?) ORDER BY cid`, table)
	if err != nil {
		t.Fatalf("reading columns of %s: %v", table, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scanning column: %v", err)
		}
		out = append(out, name)
	}
	return out
}

func objectNames(t *testing.T, db *sql.DB, objType string) map[string]bool {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = ?`, objType)
	if err != nil {
		t.Fatalf("reading %s names: %v", objType, err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scanning name: %v", err)
		}
		out[name] = true
	}
	return out
}

func dirEntries(t *testing.T, dir string) map[string]bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	out := map[string]bool{}
	for _, e := range entries {
		out[e.Name()] = true
	}
	return out
}
