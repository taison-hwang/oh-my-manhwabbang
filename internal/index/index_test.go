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
	id   string
	name string
	ord  int
	kind string
	// pages is both the books.page_count and the number of page rows written.
	pages int
	// status is books.status (arch §4.11: "ok", "error", "encrypted", "empty",
	// "unsupported"). Empty means "ok", so every fixture written before broken
	// books mattered keeps its meaning.
	status string
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
			bookStatus := b.status
			if bookStatus == "" {
				bookStatus = "ok"
			}
			bk := index.Book{
				ID: b.id, SeriesID: s.id, RootName: rootName,
				RelPath: s.name + "/" + b.name, DisplayName: b.name,
				SortKey: sortKeyOf(b.name), Ord: b.ord, Kind: b.kind,
				PageCount: int64(b.pages), TotalBytes: int64(b.pages) * 1000,
				FileSize: int64(b.pages) * 900, FileMtime: s.mtime,
				ContentVersion: "cv" + b.id, Status: bookStatus, ScanGen: 1,
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
	if got != 2 {
		t.Errorf("schema version = %d, want 2", got)
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
		"books":    {"id", "series_id", "root_name", "rel_path", "inner_path", "display_name", "sort_key", "ord", "kind", "page_count", "total_bytes", "file_size", "file_mtime", "dir_fingerprint", "content_version", "dims_state", "status", "error", "scan_gen"},
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
	// E-47's two columns. 군계 is a 5-page volume the reader finished and a
	// 7-page one they are on page 4 of, so the rollup reads 5 + 4 of 12 — a
	// number the books-only rollup could not express (it says "one of two").
	if s.Progress.PagesRead != 9 || s.Progress.PagesTotal != 12 {
		t.Errorf("pages = %d/%d, want 9/12 (5 completed + 4 read, of 5+7)",
			s.Progress.PagesRead, s.Progress.PagesTotal)
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

// E-47's two edges, both of which the real library produces.
//
// The rollup reads `books.page_count` — the index's *current* length — and not
// the progress row's own `page_count`, which is E-45 §6's stale-detection
// baseline and is allowed to disagree with the file in both directions. And it
// LEFT JOINs, so a progress row whose book is no longer in the index (the thing
// that makes a reading position reattach after a rescan) contributes nothing
// rather than dropping the whole row — including out of `books_completed`,
// which this ruling does not touch.
func TestListSeries_progressJoin_pagesReadFollowsTheIndexNotTheBaseline(t *testing.T) {
	t.Parallel()
	idx, ud, _, _ := newDBsAt(t)
	ctx := t.Context()
	seed(t, idx, "manga", library())

	// 군계 01권 is 5 pages in the index. The reader is on page 99 of a file that
	// used to be 190 — a shrunk archive, clamped by the server to what exists.
	mustPut(t, ud, "b1aaaaaaaaaaaaaa", "aaaaaaaaaaaaaaaa", 99, 190, false)
	// …and a row for a book the index no longer has at all.
	mustPut(t, ud, "b9zzzzzzzzzzzzzz", "aaaaaaaaaaaaaaaa", 4, 7, false)

	got, err := idx.ListSeries(ctx, index.SeriesFilter{Roots: []string{"manga"}, Query: "군계"})
	if err != nil {
		t.Fatalf("ListSeries: %v", err)
	}
	s := got.Items[0]
	// 5, not 99 and not 190: the whole of the volume that exists.
	if s.Progress.PagesRead != 5 {
		t.Errorf("pages_read = %d, want 5 (clamped to the index's page_count)", s.Progress.PagesRead)
	}
	if s.Progress.PagesTotal != 12 {
		t.Errorf("pages_total = %d, want 12", s.Progress.PagesTotal)
	}
	// The orphan row still counts as a started book — this ruling changed the
	// pages, not the book tally.
	if s.Progress.BooksStarted != 2 || s.Progress.BooksCompleted != 0 {
		t.Errorf("rollup = completed %d started %d, want 0/2",
			s.Progress.BooksCompleted, s.Progress.BooksStarted)
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

// ------------------------------------ the shelf shows one card per series --
//
// 이어보기 carries AT MOST ONE card per series, and the survivor is the LATER
// volume (뒷화 우선): a readable volume first of all, then greatest `ord`, then
// greatest `id` to break a tie. The tests below pin each part of that
// separately, because "one per series", "the later one" and "the readable one"
// fail independently — a `GROUP BY series_id` with no ordering passes the first
// and flunks the rest, and an `ord`-only ranking passes the first two.

// continueIDs is the book ids of a shelf, in shelf order.
func continueIDs(items []index.ContinueItem) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Book.ID
	}
	return out
}

// continueSeries is the distinct series ids on a shelf, in shelf order.
func continueSeries(items []index.ContinueItem) []string {
	seen := map[string]bool{}
	var out []string
	for _, it := range items {
		if !seen[it.SeriesID] {
			seen[it.SeriesID] = true
			out = append(out, it.SeriesID)
		}
	}
	return out
}

// (a) Two unfinished books in one series collapse to one card, and it is the
// higher-`ord` one. `library()`'s 군계 is the fixture: 01권 is ord 0, 02권 ord 1.
func TestListContinue_oneCardPerSeries_keepsTheLaterVolume(t *testing.T) {
	t.Parallel()
	idx, ud, _ := newDBs(t)
	ctx := t.Context()
	seed(t, idx, "manga", library())

	mustPut(t, ud, "b1aaaaaaaaaaaaaa", "aaaaaaaaaaaaaaaa", 3, 5, false) // ord 0
	mustPut(t, ud, "b2aaaaaaaaaaaaaa", "aaaaaaaaaaaaaaaa", 2, 7, false) // ord 1

	items, err := idx.ListContinue(ctx, 10)
	if err != nil {
		t.Fatalf("ListContinue: %v", err)
	}
	if got := continueIDs(items); !equalStrings(got, []string{"b2aaaaaaaaaaaaaa"}) {
		t.Fatalf("shelf = %v, want [b2aaaaaaaaaaaaaa] — one card per series, the later volume", got)
	}
	if items[0].Book.Ord != 1 {
		t.Errorf("ord = %d, want 1", items[0].Book.Ord)
	}
	// The surviving row must carry ITS OWN progress, not the loser's.
	if p := items[0].Book.Progress; p == nil || p.LastPage != 2 || p.PageCount != 7 {
		t.Errorf("progress = %+v, want the 02권 row (page 2 of 7)", p)
	}
}

// (b) The reported bug, exactly: 「사랑」 07권 sat at page 1 of 113 while 01권 sat
// at page 24 of 116 and had been read more recently. The shelf showed both;
// 뒷화 우선 says it must show 07권 alone. So the LOWER-`ord` book here is given
// every advantage the losing rule would reward — a later `updated_at` and more
// pages read — and must still lose.
func TestListContinue_laterVolumeWins_evenWhenTheEarlierIsFresherAndFurther(t *testing.T) {
	t.Parallel()
	idx, ud, clk, _ := newDBsAt(t)
	ctx := t.Context()
	seed(t, idx, "manga", library())

	// 02권 (ord 1) — barely started, and read FIRST.
	mustPut(t, ud, "b2aaaaaaaaaaaaaa", "aaaaaaaaaaaaaaaa", 1, 7, false)
	clk.Advance(time.Hour)
	// 01권 (ord 0) — nearly finished, and read LAST.
	mustPut(t, ud, "b1aaaaaaaaaaaaaa", "aaaaaaaaaaaaaaaa", 4, 5, false)

	items, err := idx.ListContinue(ctx, 10)
	if err != nil {
		t.Fatalf("ListContinue: %v", err)
	}
	if got := continueIDs(items); !equalStrings(got, []string{"b2aaaaaaaaaaaaaa"}) {
		t.Fatalf("shelf = %v, want [b2aaaaaaaaaaaaaa]: 뒷화 우선 outranks both a more "+
			"recent updated_at and greater progress", got)
	}
	// Guard the pair the rule is defined against: if these were equal the test
	// would pass for the wrong reason.
	lower, err := idx.GetBook(ctx, "b1aaaaaaaaaaaaaa")
	if err != nil {
		t.Fatalf("GetBook: %v", err)
	}
	if lower.Progress.UpdatedAt <= items[0].Book.Progress.UpdatedAt {
		t.Fatalf("the fixture is wrong: the loser's updated_at (%d) must be the LATER one (%d)",
			lower.Progress.UpdatedAt, items[0].Book.Progress.UpdatedAt)
	}
	if lower.Progress.LastPage <= items[0].Book.Progress.LastPage {
		t.Fatalf("the fixture is wrong: the loser must be read FURTHER (%d vs %d)",
			lower.Progress.LastPage, items[0].Book.Progress.LastPage)
	}
}

// (b′) The election ranks by `ord`, and `ord` is a key of its own — not a
// synonym for the id that happens to sit beside it.
//
// EVERY other fixture in this file gives the higher-`ord` volume the higher id
// (`b1…`/`b2…`, `brk01…`/`brk02…`, `abk01…`/`abk02…`, `act%02d…`,
// `bk%05d%09d`), so "greatest ord" and "greatest id" name the same row and the
// two keys cannot be told apart. Real ids are nothing like that: `ids.BookID` is
// 80 bits of SHA-256 rendered in base32 (`internal/ids/ids.go`), keyed on the
// root name and the ROOT-relative path, so a volume's id carries no information
// about its position in its series. Measured before this test existed: deleting
// `b2.ord DESC` from the election left all of `TestListContinue` — and all of
// `internal/httpapi` — green, so `ORDER BY (b2.status='ok') DESC, b2.id DESC`
// would have shipped an effectively arbitrary volume per series with nothing to
// catch it. 뒷화 우선 is the user's requirement (E-37); it needs a test that can
// fail.
//
// The fixture therefore makes the two keys disagree in BOTH directions at once.
// Four volumes, digest-shaped ids, and the `ord` winner's id is neither the
// greatest nor the least of the four — so `id DESC` picks 02권, `id ASC` picks
// 03권, `ord ASC` picks 01권, and only `ord DESC` picks 04권.
func TestListContinue_electionRanksByOrd_whenTheIDOrderDisagrees(t *testing.T) {
	t.Parallel()
	// 16 characters of `ids.Alphabet` each, like the ids the scanner writes —
	// deliberately NOT the readable `xxNN…` shape the rest of this file uses,
	// because that shape is the thing that hid the defect.
	const (
		sid   = "dgstser4mkqz7vhn"
		vol01 = "m4k7qz2vhbn6rdcs" // ord 0
		vol02 = "z6pd3nkxqm2vwhtj" // ord 1 — the GREATEST id
		vol03 = "c2vhq7mz4kbnrsdx" // ord 2 — the LEAST id
		vol04 = "q5nbmz3kwhdvrtc7" // ord 3 — the greatest ord, and neither of those
	)
	byOrd := []string{vol01, vol02, vol03, vol04}

	// The write order must not matter either: an un-keyed sort can land on the
	// right answer by insertion accident, which is how the `b2.id DESC`
	// tie-break nearly escaped its own test (see the note above (d)).
	for _, w := range []struct {
		name  string
		order []string
	}{
		{"written in ord order", []string{vol01, vol02, vol03, vol04}},
		{"written in reverse ord order", []string{vol04, vol03, vol02, vol01}},
		{"written in id order", []string{vol03, vol01, vol04, vol02}},
	} {
		t.Run(w.name, func(t *testing.T) {
			t.Parallel()
			idx, ud, clk, _ := newDBsAt(t)
			ctx := t.Context()

			books := make([]seedBook, 0, len(byOrd))
			for k, id := range byOrd {
				books = append(books, seedBook{
					id: id, name: fmt.Sprintf("%02d권.zip", k+1), ord: k, kind: "zip", pages: 20,
				})
			}
			seed(t, idx, "manga", []seedSeries{{
				id: sid, name: "id가 ord를 따르지 않는 시리즈", sortKey: "dgst", searchKey: "dgst",
				mtime: 1, addedAt: 1, books: books,
			}})
			for i, id := range w.order {
				if i > 0 {
					clk.Advance(time.Hour)
				}
				mustPut(t, ud, id, sid, 1, 20, false)
			}

			// Guard the fixture: if the two orders agreed, or if the `ord`
			// winner also held an extreme id, the test would pass for an
			// id-only rule and prove nothing.
			byID := append([]string(nil), byOrd...)
			sort.Strings(byID)
			if equalStrings(byID, byOrd) {
				t.Fatalf("the fixture is wrong: id order %v must DISAGREE with ord order %v",
					byID, byOrd)
			}
			if byID[0] == vol04 || byID[len(byID)-1] == vol04 {
				t.Fatalf("the fixture is wrong: the ord winner %s must be neither the least nor "+
					"the greatest id (sorted: %v), or an id-only ranking would pick it by accident",
					vol04, byID)
			}

			items, err := idx.ListContinue(ctx, 10)
			if err != nil {
				t.Fatalf("ListContinue: %v", err)
			}
			if got := continueIDs(items); !equalStrings(got, []string{vol04}) {
				t.Fatalf("shelf = %v, want [%s] — the election ranks by `ord` (뒷화 우선), and this "+
					"series' ids run in a different order from its volumes", got, vol04)
			}
			if items[0].Book.Ord != len(byOrd)-1 {
				t.Errorf("ord = %d, want %d", items[0].Book.Ord, len(byOrd)-1)
			}
		})
	}
}

// (c) `limit` counts SERIES, because de-duplication happens before it. Five
// series each with three unfinished volumes, asked for three, must answer three
// cards from three different series — not three volumes of the first one, and
// not fewer than three because duplicates were dropped after the LIMIT.
func TestListContinue_limitCountsDistinctSeries(t *testing.T) {
	t.Parallel()
	idx, ud, clk, _ := newDBsAt(t)
	ctx := t.Context()

	const nSeries, nBooks = 5, 3
	fixture := make([]seedSeries, 0, nSeries)
	for s := 0; s < nSeries; s++ {
		sid := fmt.Sprintf("ser%013d", s)
		books := make([]seedBook, 0, nBooks)
		for k := 0; k < nBooks; k++ {
			books = append(books, seedBook{
				id: fmt.Sprintf("bk%05d%09d", s, k), name: fmt.Sprintf("%02d권.zip", k+1),
				ord: k, kind: "zip", pages: 10,
			})
		}
		fixture = append(fixture, seedSeries{
			id: sid, name: fmt.Sprintf("시리즈 %d", s), sortKey: sid, searchKey: sid,
			mtime: int64(s), addedAt: int64(s), books: books,
		})
	}
	seed(t, idx, "manga", fixture)

	for s := 0; s < nSeries; s++ {
		for k := 0; k < nBooks; k++ {
			mustPut(t, ud, fmt.Sprintf("bk%05d%09d", s, k), fmt.Sprintf("ser%013d", s), 1, 10, false)
			clk.Advance(time.Minute)
		}
	}

	items, err := idx.ListContinue(ctx, 3)
	if err != nil {
		t.Fatalf("ListContinue: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("limit=3 returned %d cards (%v); de-duplicating after the LIMIT would "+
			"have shrunk the shelf", len(items), continueIDs(items))
	}
	if got := continueSeries(items); len(got) != 3 {
		t.Fatalf("limit=3 covered %d distinct series (%v), want 3", len(got), got)
	}
	for _, it := range items {
		if it.Book.Ord != nBooks-1 {
			t.Errorf("series %s kept ord %d, want %d (the last volume)",
				it.SeriesID, it.Book.Ord, nBooks-1)
		}
	}

	// The whole shelf is one card per series, no more and no less.
	all, err := idx.ListContinue(ctx, 50)
	if err != nil {
		t.Fatalf("ListContinue: %v", err)
	}
	if len(all) != nSeries {
		t.Fatalf("unlimited shelf = %d cards (%v), want %d — one per series",
			len(all), continueIDs(all), nSeries)
	}
}

// (d) De-duplication is PER SERIES, not global: two different series both keep
// their card. A `GROUP BY` written one column too wide would collapse them.
func TestListContinue_differentSeriesEachKeepTheirCard(t *testing.T) {
	t.Parallel()
	idx, ud, clk, _ := newDBsAt(t)
	ctx := t.Context()
	seed(t, idx, "manga", library())

	mustPut(t, ud, "b1aaaaaaaaaaaaaa", "aaaaaaaaaaaaaaaa", 3, 5, false) // 군계, ord 0
	clk.Advance(time.Hour)
	mustPut(t, ud, "b3bbbbbbbbbbbbbb", "bbbbbbbbbbbbbbbb", 1, 3, false) // 강철의 연금술사
	clk.Advance(time.Hour)
	mustPut(t, ud, "b5eeeeeeeeeeeeee", "eeeeeeeeeeeeeeee", 1, 2, false) // 20세기소년

	items, err := idx.ListContinue(ctx, 10)
	if err != nil {
		t.Fatalf("ListContinue: %v", err)
	}
	// Shelf order is unchanged: most recently read first.
	want := []string{"b5eeeeeeeeeeeeee", "b3bbbbbbbbbbbbbb", "b1aaaaaaaaaaaaaa"}
	if got := continueIDs(items); !equalStrings(got, want) {
		t.Fatalf("shelf = %v, want %v (three series, three cards, newest first)", got, want)
	}
}

// A series whose LAST volume is FINISHED still belongs on the shelf, showing
// the latest volume that is not.
//
// This is the failure mode of electing the winner without repeating
// `completed = 0` inside the de-duplication: 군계 would elect 02권, 02권 would
// then be filtered out as finished, and 군계 would disappear from 이어보기
// altogether even though 01권 is half-read. Losing a card is worse than the
// duplicate this change set out to fix, so it gets its own test.
func TestListContinue_seriesSurvivesWhenItsLastVolumeIsFinished(t *testing.T) {
	t.Parallel()
	idx, ud, _ := newDBs(t)
	ctx := t.Context()
	seed(t, idx, "manga", library())

	mustPut(t, ud, "b1aaaaaaaaaaaaaa", "aaaaaaaaaaaaaaaa", 3, 5, false) // ord 0, reading
	mustPut(t, ud, "b2aaaaaaaaaaaaaa", "aaaaaaaaaaaaaaaa", 7, 7, true)  // ord 1, done

	items, err := idx.ListContinue(ctx, 10)
	if err != nil {
		t.Fatalf("ListContinue: %v", err)
	}
	if got := continueIDs(items); !equalStrings(got, []string{"b1aaaaaaaaaaaaaa"}) {
		t.Fatalf("shelf = %v, want [b1aaaaaaaaaaaaaa] — the finished later volume must not "+
			"take the series' card with it", got)
	}
}

// A tie in `ord` is RARE, not routine, and the tie-break still has to exist.
//
// `ord` is `INTEGER NOT NULL` and the scanner assigns it as a 0-based dense rank
// over the series' books, in the natural-sort order collect.go materialises
// (`for ord := range t.results` in scanner.go's series writer). So after any
// scan that ran to completion the values are strictly distinct within a series
// and this fixture is unreachable. What makes it reachable is a scan whose
// generation sweep was BLOCKED — scanner/gen.go's `decideSweep` refuses to sweep
// a targeted run, so `POST /api/series/{sid}/rescan` after a volume was deleted
// from disk re-ranks the surviving books from 0 while the deleted book's row
// keeps its old `ord` at the previous generation. Two rows of one series then
// carry the same `ord` until a full scan sweeps the stale one away. The shelf
// must be deterministic in that window, not
// whichever-row-SQLite-reached-first.
//
// All four arrangements of (write order × which row is fresher) are run, and
// that is the whole point of the test rather than thoroughness for its own
// sake. With the `b2.id DESC` tie-break deleted the query still answers
// correctly in two of these four — SQLite's untie-broken sort happens to
// surface the greater id when the greater id was written last or read more
// recently — so a single-arrangement test passes against a query that has no
// tie-break at all. Measured: deleting `, b2.id DESC` leaves cases 1 and 4
// green and turns cases 2 and 3 red.
func TestListContinue_tiedOrd_breaksOnTheGreaterID(t *testing.T) {
	t.Parallel()
	const (
		lo = "tieaaaaaaaaaaaaa"
		hi = "tiezzzzzzzzzzzzz"
	)
	cases := []struct {
		name    string
		order   []string // the order progress is written in
		advance bool     // whether the second write also gets a later updated_at
	}{
		{"greater id written first", []string{hi, lo}, false},
		{"lesser id written first", []string{lo, hi}, false},
		{"lesser id read more recently", []string{hi, lo}, true},
		{"greater id read more recently", []string{lo, hi}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			idx, ud, clk, _ := newDBsAt(t)
			ctx := t.Context()
			seed(t, idx, "manga", []seedSeries{{
				id: "tie0000000000000", name: "동률", sortKey: "tie", searchKey: "tie",
				books: []seedBook{
					{id: lo, name: "a.zip", ord: 4, kind: "zip", pages: 6},
					{id: hi, name: "z.zip", ord: 4, kind: "zip", pages: 6},
				},
			}})
			for i, id := range c.order {
				if i > 0 && c.advance {
					clk.Advance(time.Hour)
				}
				mustPut(t, ud, id, "tie0000000000000", 1, 6, false)
			}

			items, err := idx.ListContinue(ctx, 10)
			if err != nil {
				t.Fatalf("ListContinue: %v", err)
			}
			if got := continueIDs(items); !equalStrings(got, []string{hi}) {
				t.Fatalf("shelf = %v, want [%s] — equal ord breaks on the greater id, "+
					"whatever order the progress rows were written in", got, hi)
			}
		})
	}
}

// ------------------------------- the shelf's ORDER agrees with its CHOICE --

// The shelf ranks a series by the series' own most recent reading, not by the
// card it happens to show.
//
// Once one card per series is elected by `ord`, the elected card's `updated_at`
// stops being a statement about the SERIES. A reader who peeked at 07권 a month
// ago and read 01권 five minutes ago is actively reading that series; ranking it
// by 07권's month-old timestamp sends it to the BOTTOM of 이어보기, and at the
// shelf's cap (ui-spec §4.3 shows five cards, useLibrary.ts asks for limit=5) it
// falls off the shelf altogether. The card shown stays the 뒷화 winner — only
// the ordering key is the series'.
func TestListContinue_ranksBySeriesActivity_notByTheElectedCard(t *testing.T) {
	t.Parallel()
	idx, ud, clk, _ := newDBsAt(t)
	ctx := t.Context()

	const (
		seriesA = "act0000000000000"
		vol01   = "act00aaaaaaaaaaa"
		vol07   = "act06aaaaaaaaaaa"
	)

	// 「사랑」: seven volumes. Only 01권 and 07권 are ever opened.
	active := []seedBook{}
	for k := 0; k < 7; k++ {
		active = append(active, seedBook{
			id:   fmt.Sprintf("act%02daaaaaaaaaaa", k),
			name: fmt.Sprintf("%02d권.zip", k+1), ord: k, kind: "zip", pages: 110 + k,
		})
	}
	fixture := []seedSeries{{
		id: seriesA, name: "사랑", sortKey: "act", searchKey: "사랑",
		mtime: 1, addedAt: 1, books: active,
	}}
	// Five more in-progress series, so the shelf is over its five-card cap.
	for s := 0; s < 5; s++ {
		sid := fmt.Sprintf("oth%013d", s)
		fixture = append(fixture, seedSeries{
			id: sid, name: fmt.Sprintf("다른 시리즈 %d", s), sortKey: sid, searchKey: sid,
			mtime: int64(s), addedAt: int64(s),
			books: []seedBook{{
				id: fmt.Sprintf("oth%05d00000000", s), name: "01권.zip",
				ord: 0, kind: "zip", pages: 20,
			}},
		})
	}
	seed(t, idx, "manga", fixture)

	// A month ago: a peek at 07권.
	mustPut(t, ud, vol07, seriesA, 1, 113, false)
	clk.Advance(30 * 24 * time.Hour)
	// Then five other series, one an hour.
	for s := 0; s < 5; s++ {
		mustPut(t, ud, fmt.Sprintf("oth%05d00000000", s), fmt.Sprintf("oth%013d", s), 3, 20, false)
		clk.Advance(time.Hour)
	}
	// Five minutes ago: back to 01권 of 「사랑」. The series is the freshest thing
	// on the shelf.
	mustPut(t, ud, vol01, seriesA, 24, 116, false)

	// Guard the fixture: the elected card really is the STALE one, so the test
	// cannot pass by accident.
	winner, err := idx.GetBook(ctx, vol07)
	if err != nil {
		t.Fatalf("GetBook: %v", err)
	}
	fresh, err := idx.GetBook(ctx, vol01)
	if err != nil {
		t.Fatalf("GetBook: %v", err)
	}
	if winner.Progress.UpdatedAt >= fresh.Progress.UpdatedAt {
		t.Fatalf("the fixture is wrong: 07권's updated_at (%d) must be OLDER than 01권's (%d)",
			winner.Progress.UpdatedAt, fresh.Progress.UpdatedAt)
	}

	items, err := idx.ListContinue(ctx, 10)
	if err != nil {
		t.Fatalf("ListContinue: %v", err)
	}
	if len(items) != 6 {
		t.Fatalf("shelf = %d cards (%v), want 6 — one per in-progress series",
			len(items), continueIDs(items))
	}
	if items[0].SeriesID != seriesA {
		t.Fatalf("shelf = %v, want 「사랑」 (%s) first: it was read five minutes ago, and the "+
			"shelf ranks a series by ITS most recent unfinished-book activity, not by the "+
			"elected card's own updated_at", continueSeries(items), seriesA)
	}
	// The ordering key moved; the CHOICE did not. 뒷화 우선 still elects 07권.
	if items[0].Book.ID != vol07 || items[0].Book.Ord != 6 {
		t.Fatalf("card for 「사랑」 = %s (ord %d), want %s (ord 6) — ranking by the series must "+
			"not change which volume the card shows", items[0].Book.ID, items[0].Book.Ord, vol07)
	}

	// The reachable half of the defect: at the shelf's real cap the series that
	// is being read right now must not be the one that falls off.
	capped, err := idx.ListContinue(ctx, 5)
	if err != nil {
		t.Fatalf("ListContinue: %v", err)
	}
	if len(capped) != 5 {
		t.Fatalf("limit=5 returned %d cards (%v), want 5", len(capped), continueIDs(capped))
	}
	if capped[0].SeriesID != seriesA || capped[0].Book.ID != vol07 {
		t.Fatalf("limit=5 shelf = %v (books %v); 「사랑」's 07권 card must survive the cap at "+
			"position 0, not be pushed off by five series read less recently",
			continueSeries(capped), continueIDs(capped))
	}
}

// The shelf's ordering key counts UNFINISHED reading and nothing else. A volume
// the reader FINISHED must not lift its series up the shelf.
//
// That is what `p3.completed = 0` inside `seriesActivity` buys, and deleting it
// left every other test in this file — and all of `internal/httpapi` — green,
// even though the behaviour change is real and points the wrong way. Finishing
// 03권 is the reader saying they are DONE with that volume; 이어보기 is the shelf
// of what is left unfinished, so a series where the newest thing that happened
// was a completion must rank BELOW a series the reader is in the middle of.
// Without the clause the finished volume's timestamp — the newest in the whole
// fixture — becomes the series' ordering key and lifts it to the top.
//
// The fixture is also arranged so that removing the ordering key altogether
// fails here: `fin…` sorts before `oth…`, so the `b.id ASC` tie-break alone
// would produce the same wrong answer as the mutation.
func TestListContinue_seriesActivity_ignoresFinishedVolumes(t *testing.T) {
	t.Parallel()
	idx, ud, clk, _ := newDBsAt(t)
	ctx := t.Context()

	const (
		finished = "fin0000000000000"
		fin01    = "fin01aaaaaaaaaaa"
		fin02    = "fin02aaaaaaaaaaa"
		fin03    = "fin03aaaaaaaaaaa"
		other    = "oth0000000000000"
		oth01    = "oth01aaaaaaaaaaa"
	)
	seed(t, idx, "manga", []seedSeries{
		{id: finished, name: "마지막 권을 완독한 시리즈", sortKey: "fin", searchKey: "fin",
			mtime: 1, addedAt: 1, books: []seedBook{
				{id: fin01, name: "01권.zip", ord: 0, kind: "zip", pages: 30},
				{id: fin02, name: "02권.zip", ord: 1, kind: "zip", pages: 30},
				{id: fin03, name: "03권.zip", ord: 2, kind: "zip", pages: 30},
			}},
		{id: other, name: "읽는 중인 시리즈", sortKey: "oth", searchKey: "oth",
			mtime: 2, addedAt: 2, books: []seedBook{
				{id: oth01, name: "01권.zip", ord: 0, kind: "zip", pages: 30},
			}},
	})

	// Three progress rows on the first series, and the ONLY finished one is the
	// most recent thing the reader did anywhere.
	mustPut(t, ud, fin01, finished, 5, 30, false)
	clk.Advance(time.Hour)
	mustPut(t, ud, fin02, finished, 9, 30, false) // the series' real activity
	clk.Advance(time.Hour)
	mustPut(t, ud, oth01, other, 4, 30, false) // read AFTER that
	clk.Advance(time.Hour)
	mustPut(t, ud, fin03, finished, 30, 30, true) // finished, and newest of all

	// Guard the fixture on both halves: the completed row must really be
	// completed, and it must really be the newest timestamp in the database.
	done, err := idx.GetBook(ctx, fin03)
	if err != nil {
		t.Fatalf("GetBook: %v", err)
	}
	live, err := idx.GetBook(ctx, oth01)
	if err != nil {
		t.Fatalf("GetBook: %v", err)
	}
	if done.Progress == nil || !done.Progress.Completed {
		t.Fatalf("the fixture is wrong: 03권's progress = %+v; want a COMPLETED row", done.Progress)
	}
	if done.Progress.UpdatedAt <= live.Progress.UpdatedAt {
		t.Fatalf("the fixture is wrong: the completed 03권 (%d) must be NEWER than the other "+
			"series' unfinished row (%d), or the mutation has nothing to lift",
			done.Progress.UpdatedAt, live.Progress.UpdatedAt)
	}

	items, err := idx.ListContinue(ctx, 10)
	if err != nil {
		t.Fatalf("ListContinue: %v", err)
	}
	want := []string{oth01, fin02}
	if got := continueIDs(items); !equalStrings(got, want) {
		t.Fatalf("shelf = %v, want %v — a series' place on 이어보기 is its most recent UNFINISHED "+
			"reading; the volume it finished must not carry it up the shelf", got, want)
	}
	// The election is unaffected: 뒷화 우선 over the unfinished volumes is 02권,
	// not the finished 03권 and not the older 01권.
	if items[1].Book.ID != fin02 || items[1].Book.Ord != 1 {
		t.Errorf("card for the first series = %s (ord %d), want %s (ord 1)",
			items[1].Book.ID, items[1].Book.Ord, fin02)
	}
}

// A book on a DISABLED root is off the shelf.
//
// `r.enabled = 1` in the outer WHERE is the only thing that removes it, and
// deleting it left both `internal/index` and `internal/httpapi` green. The
// clause is reasoned about at length in `latestPerSeries`' comment — which
// explains why it is deliberately NOT repeated inside the election — while
// nothing pinned the outer one it depends on. arch §3.2 keeps a disabled root's
// rows so that re-enabling loses nothing, which is exactly why every read path
// has to filter them on the way out.
func TestListContinue_disabledRoot_isOffTheShelf(t *testing.T) {
	t.Parallel()
	idx, ud, _ := newDBs(t)
	ctx := t.Context()

	const (
		usbSeries = "usb0000000000000"
		usbBook   = "usb01aaaaaaaaaaa"
	)
	seed(t, idx, "manga", library()[:1])
	seed(t, idx, "usb", []seedSeries{{
		id: usbSeries, name: "뽑아 둔 드라이브", sortKey: "usb", searchKey: "usb",
		mtime: 1, addedAt: 1,
		books: []seedBook{{id: usbBook, name: "01권.zip", ord: 0, kind: "zip", pages: 9}},
	}})

	mustPut(t, ud, "b1aaaaaaaaaaaaaa", "aaaaaaaaaaaaaaaa", 3, 5, false)
	mustPut(t, ud, usbBook, usbSeries, 2, 9, false)

	// Both roots enabled: both cards. Without this half the assertion below
	// would also pass against a query that returned nothing at all.
	before, err := idx.ListContinue(ctx, 10)
	if err != nil {
		t.Fatalf("ListContinue: %v", err)
	}
	if len(before) != 2 {
		t.Fatalf("shelf with both roots enabled = %v, want both cards", continueIDs(before))
	}

	if err := idx.UpsertRoot(ctx, index.Root{
		Name: "usb", Path: "/media/usb", Label: "usb", Enabled: false}); err != nil {
		t.Fatalf("disabling root: %v", err)
	}

	after, err := idx.ListContinue(ctx, 10)
	if err != nil {
		t.Fatalf("ListContinue: %v", err)
	}
	if got := continueIDs(after); !equalStrings(got, []string{"b1aaaaaaaaaaaaaa"}) {
		t.Fatalf("shelf = %v, want [b1aaaaaaaaaaaaaa] — a book whose root is disabled must not "+
			"appear on 이어보기, even though its rows are deliberately kept", got)
	}
}

// ------------------------------ a broken volume must not hide a good one --

// A book whose `status` is not "ok" loses the election to a readable volume of
// the same series.
//
// 02권 is `status='error', page_count=0` — the shape httpapi/progress.go
// documents as a supported PUT ("length unknown"). Such a row can never become
// `completed` on its own: userdata.PutProgress only auto-completes when
// `PageCount > 0`. So without a readability key the broken 02권 wins its
// partition FOREVER and the 01권 the reader is actually on disappears from
// 이어보기.
func TestListContinue_readableVolumeWins_overABrokenLaterOne(t *testing.T) {
	t.Parallel()
	idx, ud, clk, _ := newDBsAt(t)
	ctx := t.Context()

	const (
		sid   = "brk0000000000000"
		vol01 = "brk01aaaaaaaaaaa"
		vol02 = "brk02aaaaaaaaaaa"
	)
	seed(t, idx, "manga", []seedSeries{{
		id: sid, name: "부서진 권이 있는 시리즈", sortKey: "brk", searchKey: "brk",
		mtime: 1, addedAt: 1,
		books: []seedBook{
			{id: vol01, name: "01권.zip", ord: 0, kind: "zip", pages: 10},
			{id: vol02, name: "02권.zip", ord: 1, kind: "zip", pages: 0, status: "error"},
		},
	}})

	// Opened the broken 02권 once — page 1 of "length unknown".
	mustPut(t, ud, vol02, sid, 1, 0, false)
	clk.Advance(time.Hour)
	// Then went back to 01권, which is what the reader is on.
	mustPut(t, ud, vol01, sid, 3, 10, false)

	// Guard the fixture: the broken row is PERMANENTLY unfinished, so it is a
	// permanent competitor rather than a transient one.
	broken, err := idx.GetBook(ctx, vol02)
	if err != nil {
		t.Fatalf("GetBook: %v", err)
	}
	if broken.Status != "error" || broken.PageCount != 0 {
		t.Fatalf("the fixture is wrong: 02권 = status %q, page_count %d; want error/0",
			broken.Status, broken.PageCount)
	}
	if broken.Progress == nil || broken.Progress.Completed {
		t.Fatalf("the fixture is wrong: 02권's progress = %+v; want a row that is not completed",
			broken.Progress)
	}

	items, err := idx.ListContinue(ctx, 10)
	if err != nil {
		t.Fatalf("ListContinue: %v", err)
	}
	if got := continueIDs(items); !equalStrings(got, []string{vol01}) {
		t.Fatalf("shelf = %v, want [%s] — a book that cannot be read must not win its series' "+
			"card and hide the volume actually being read", got, vol01)
	}
	if items[0].Book.PageCount != 10 || items[0].Book.Status != "ok" {
		t.Errorf("card = status %q, %d pages; want the readable ok/10 volume",
			items[0].Book.Status, items[0].Book.PageCount)
	}
}

// Readability PREFERS, it does not EXCLUDE: a series whose started books are ALL
// non-"ok" keeps its card and shows its latest volume.
//
// This is the property that rules out the one-line fix for the defect above.
// `AND b2.status = 'ok'` inside the election would make this series match
// nothing and vanish from 이어보기 entirely — trading one silent behaviour
// change for another, and losing a card the reader put there.
func TestListContinue_seriesWithOnlyBrokenBooks_stillKeepsItsCard(t *testing.T) {
	t.Parallel()
	idx, ud, clk, _ := newDBsAt(t)
	ctx := t.Context()

	const (
		sid   = "abk0000000000000"
		vol01 = "abk01aaaaaaaaaaa"
		vol02 = "abk02aaaaaaaaaaa"
	)
	seed(t, idx, "manga", []seedSeries{{
		id: sid, name: "전부 부서진 시리즈", sortKey: "abk", searchKey: "abk",
		mtime: 1, addedAt: 1,
		books: []seedBook{
			{id: vol01, name: "01권.zip", ord: 0, kind: "zip", pages: 0, status: "error"},
			{id: vol02, name: "02권.zip", ord: 1, kind: "zip", pages: 0, status: "encrypted"},
		},
	}})

	mustPut(t, ud, vol01, sid, 1, 0, false)
	clk.Advance(time.Hour)
	mustPut(t, ud, vol02, sid, 1, 0, false)

	items, err := idx.ListContinue(ctx, 10)
	if err != nil {
		t.Fatalf("ListContinue: %v", err)
	}
	if got := continueIDs(items); !equalStrings(got, []string{vol02}) {
		t.Fatalf("shelf = %v, want [%s] — when nothing in the series is readable the shelf "+
			"still shows its latest volume; readability prefers, it never excludes", got, vol02)
	}
}

// Two series last read in the SAME SECOND are ordered by the card's `id`, so the
// shelf does not shuffle between two reloads that read the same rows.
//
// progress timestamps are Unix *seconds* and the reader debounces page turns to
// about one, so closing one book and opening another inside the same second is
// ordinary. Ranking by the series' MAX(updated_at) rather than by the elected
// card's own makes the tie MORE reachable, not less: two series now collide
// whenever ANY of their unfinished volumes share a second, not just the two
// elected ones.
//
// HONEST LIMIT OF THIS TEST. It does not kill the mutation that deletes
// `, b.id ASC` from the shelf's ORDER BY, and four arrangements — card ids with
// and against the series ids, in both write orders — were tried to make it.
// SQLite lands on the same answer without the tie-break in all four, even
// though the election list demonstrably feeds the sort in the OTHER order
// (measured against the raw query: the list emits [hi, lo] and the un-tie-broken
// sort still returns [lo, hi]). That is coincidence rather than the tie-break
// being redundant — asking for `b.id DESC` instead does change the answer — so
// the clause stays: the shelf's determinism must not rest on which row SQLite
// happens to emit first. What this test does pin is the observable contract,
// which a future rewrite of the query could break in a way SQLite would not
// silently repair: two series read in the same second both appear, in card-id
// order, whatever their series ids or write order.
func TestListContinue_tiedSeriesActivity_breaksOnTheCardID(t *testing.T) {
	t.Parallel()
	const (
		lo   = "sameaaaaaaaaaaaa"
		hi   = "samezzzzzzzzzzzz"
		ser1 = "same000000000001"
		ser2 = "same000000000002"
	)
	layouts := []struct {
		name string
		// which book id lives in the LOWER series id
		inSer1, inSer2 string
	}{
		{"card ids agree with series ids", lo, hi},
		{"card ids oppose series ids", hi, lo},
	}
	for _, l := range layouts {
		for _, first := range []string{lo, hi} {
			t.Run(fmt.Sprintf("%s, %s written first", l.name, first), func(t *testing.T) {
				t.Parallel()
				idx, ud, _ := newDBs(t) // no clock advance: both writes share a second
				ctx := t.Context()
				seed(t, idx, "manga", []seedSeries{
					{id: ser1, name: "동시 1", sortKey: "s1", searchKey: "s1",
						books: []seedBook{{id: l.inSer1, name: "01권.zip", ord: 0, kind: "zip", pages: 6}}},
					{id: ser2, name: "동시 2", sortKey: "s2", searchKey: "s2",
						books: []seedBook{{id: l.inSer2, name: "01권.zip", ord: 0, kind: "zip", pages: 6}}},
				})
				seriesOf := map[string]string{l.inSer1: ser1, l.inSer2: ser2}
				second := lo
				if first == lo {
					second = hi
				}
				for _, id := range []string{first, second} {
					mustPut(t, ud, id, seriesOf[id], 1, 6, false)
				}

				items, err := idx.ListContinue(ctx, 10)
				if err != nil {
					t.Fatalf("ListContinue: %v", err)
				}
				if len(items) != 2 {
					t.Fatalf("shelf = %v, want both series", continueIDs(items))
				}
				if items[0].Book.Progress.UpdatedAt != items[1].Book.Progress.UpdatedAt {
					t.Fatalf("the fixture is wrong: the two rows must share an updated_at (%d vs %d)",
						items[0].Book.Progress.UpdatedAt, items[1].Book.Progress.UpdatedAt)
				}
				if got := continueIDs(items); !equalStrings(got, []string{lo, hi}) {
					t.Fatalf("shelf = %v, want [%s %s] — series read in the same second are ordered "+
						"by the card's id, whatever the series ids or the write order say", got, lo, hi)
				}
			})
		}
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
	res, _, err := w.SweepRoot(ctx, "manga", gen)
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
	res, _, err := w.SweepRoot(ctx, "manga", gen)
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
	if v, err := idx.SchemaVersion(ctx); err != nil || v != 2 {
		t.Errorf("schema version after Reset = %d (%v), want 2", v, err)
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
