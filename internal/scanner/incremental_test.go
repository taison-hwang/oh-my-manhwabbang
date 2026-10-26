package scanner

import (
	"fmt"
	"testing"
	"time"

	"shelf/internal/archive"
	"shelf/internal/index"
	"shelf/internal/source"
	"shelf/internal/testutil"
)

// The fingerprint has to move when any tuple field moves — that is the whole
// contract, since a directory's own mtime does not change when a nested file is
// rewritten in place (arch §4.6).
func TestFingerprintChildren_movesWithEveryTupleField(t *testing.T) {
	t.Parallel()
	base := []childInfo{
		{name: "001.jpg", size: 120, mtime: 1_500_000_000},
		{name: "002.jpg", size: 340, mtime: 1_500_000_001},
	}
	want := fingerprintChildren(base)
	if len(want) != 16 {
		t.Fatalf("fingerprint %q is %d chars, want the 16 hex of arch §3.5", want, len(want))
	}
	if got := fingerprintChildren(base); got != want {
		t.Fatalf("fingerprint is not deterministic: %q then %q", want, got)
	}

	mutate := map[string]func([]childInfo) []childInfo{
		"a renamed child": func(c []childInfo) []childInfo { c[0].name = "0001.jpg"; return c },
		"a resized child": func(c []childInfo) []childInfo { c[0].size++; return c },
		"a touched child": func(c []childInfo) []childInfo { c[0].mtime++; return c },
		"a file that became a directory": func(c []childInfo) []childInfo {
			c[0].isDir = true
			return c
		},
		"a removed child": func(c []childInfo) []childInfo { return c[:1] },
		"an added child": func(c []childInfo) []childInfo {
			return append(c, childInfo{name: "003.jpg", size: 1, mtime: 2})
		},
	}
	for name, fn := range mutate {
		clone := append([]childInfo(nil), base...)
		if got := fingerprintChildren(fn(clone)); got == want {
			t.Errorf("%s did not change the fingerprint (%q)", name, got)
		}
	}
}

// content_version is what makes FR-THM-006 structural (arch §5.6): it moves when
// the source moves, so a stale thumbnail key can never be produced.
func TestContentVersion_movesWithSizeAndMtimeAndUsesTheFingerprintForDirectories(t *testing.T) {
	t.Parallel()
	cv := contentVersion(source.KindZIP, 1024, 1_500_000_000, "")
	if len(cv) != 16 {
		t.Fatalf("content_version %q is %d chars, want 16 (arch §3.5)", cv, len(cv))
	}
	if contentVersion(source.KindZIP, 1024, 1_500_000_000, "") != cv {
		t.Fatal("content_version is not deterministic")
	}
	if contentVersion(source.KindZIP, 1025, 1_500_000_000, "") == cv {
		t.Error("a changed size left content_version alone")
	}
	if contentVersion(source.KindZIP, 1024, 1_500_000_001, "") == cv {
		t.Error("a changed mtime left content_version alone (FR-THM-006)")
	}
	// A directory has no meaningful container size or mtime, so it carries the
	// digest of the thing that does determine its content.
	fp := fingerprintChildren([]childInfo{{name: "001.jpg", size: 1, mtime: 2}})
	if got := contentVersion(source.KindDir, 0, 0, fp); got != fp {
		t.Errorf("directory content_version = %q, want the fingerprint %q", got, fp)
	}
}

// The skip rule of arch §4.6, plus the one documented refinement.
func TestUnchanged_appliesTheArchSkipRule(t *testing.T) {
	t.Parallel()
	zipUnit := bookUnit{kind: source.KindZIP, size: 1024, mtime: 1_500_000_000}
	dirUnit := bookUnit{kind: source.KindDir, fingerprint: "aaaaaaaaaaaaaaaa"}

	cases := []struct {
		name  string
		unit  bookUnit
		prior index.Book
		full  bool
		want  bool
	}{
		{name: "archive, size and mtime both equal", unit: zipUnit,
			prior: index.Book{Status: StatusOK, NameEncoding: "cp949", FileSize: 1024, FileMtime: 1_500_000_000}, want: true},
		{name: "archive, size moved", unit: zipUnit,
			prior: index.Book{Status: StatusOK, NameEncoding: "cp949", FileSize: 1025, FileMtime: 1_500_000_000}},
		{name: "archive, mtime moved", unit: zipUnit,
			prior: index.Book{Status: StatusOK, NameEncoding: "cp949", FileSize: 1024, FileMtime: 1_500_000_001}},
		{name: "a full scan never skips", unit: zipUnit, full: true,
			prior: index.Book{Status: StatusOK, NameEncoding: "cp949", FileSize: 1024, FileMtime: 1_500_000_000}},
		{name: "directory, fingerprint equal", unit: dirUnit,
			prior: index.Book{Status: StatusOK, NameEncoding: "cp949", DirFingerprint: "aaaaaaaaaaaaaaaa"}, want: true},
		{name: "directory, fingerprint moved", unit: dirUnit,
			prior: index.Book{Status: StatusOK, NameEncoding: "cp949", DirFingerprint: "bbbbbbbbbbbbbbbb"}},
		{name: "directory with no recorded fingerprint", unit: dirUnit,
			prior: index.Book{Status: StatusOK, NameEncoding: "cp949"}},
		{
			// Ruling E-39 (draft). 'error' is not reliably a property of the
			// file — a transient read failure and a listing taken from a
			// replaced inode both look exactly like this — so the timestamps
			// may not settle it and the book is re-derived.
			name: "a book recorded broken is re-examined", unit: zipUnit,
			prior: index.Book{NameEncoding: "cp949", Status: StatusError, FileSize: 1024, FileMtime: 1_500_000_000},
		},
		{
			name: "a book recorded empty is re-examined", unit: zipUnit,
			prior: index.Book{NameEncoding: "cp949", Status: StatusEmpty, FileSize: 1024, FileMtime: 1_500_000_000},
		},
		{
			name: "a book recorded encrypted is re-examined", unit: dirUnit,
			prior: index.Book{NameEncoding: "cp949", Status: StatusEncrypted, DirFingerprint: "aaaaaaaaaaaaaaaa"},
		},
		{
			// 'unsupported' says "this BUILD cannot read it" — a PDF under
			// -tags nopdf. A differently-built binary must be free to reach a
			// different answer, so the file's timestamps do not settle it.
			// This was the only exception before E-39; it is now the general
			// rule's first instance rather than a special case.
			name: "an unsupported book is always re-examined", unit: zipUnit,
			prior: index.Book{NameEncoding: "cp949", Status: StatusUnsupported, FileSize: 1024, FileMtime: 1_500_000_000},
		},
		{
			// A row with no status at all (a partially written book) is not
			// evidence of a successful read either.
			name: "a book with no recorded status is re-examined", unit: zipUnit,
			prior: index.Book{NameEncoding: "cp949", FileSize: 1024, FileMtime: 1_500_000_000},
		},
		{
			// Ruling E-56. Schema v3 added books.name_encoding NULL for every
			// existing row; an ok book is otherwise re-read only when its file
			// changes, so an unchanged library would keep the NULL — and
			// SeriesDetail.encoding null — for ever. The book is re-listed once.
			name: "an ok archive with no recorded name encoding is re-listed", unit: zipUnit,
			prior: index.Book{Status: StatusOK, FileSize: 1024, FileMtime: 1_500_000_000},
		},
		{
			name: "an ok directory with no recorded name encoding is re-listed", unit: dirUnit,
			prior: index.Book{Status: StatusOK, DirFingerprint: "aaaaaaaaaaaaaaaa"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := unchanged(tc.unit, tc.prior, tc.full); got != tc.want {
				t.Errorf("unchanged = %v, want %v", got, tc.want)
			}
		})
	}
}

// FR-IDX-003, the headline case: touch one file and exactly one book is
// re-indexed. Everything else is stamped forward without being opened.
func TestScan_incremental_touchOneArchive_reindexesExactlyThatBook(t *testing.T) {
	t.Parallel()
	h := newHarness(t, map[string]any{
		"시리즈 a": map[string]any{
			"01권.zip": jpegZIP(t, "001.jpg"),
			"02권.zip": jpegZIP(t, "001.jpg", "002.jpg"),
			"03권.zip": jpegZIP(t, "001.jpg"),
		},
		"시리즈 b": map[string]any{
			"01권.zip": jpegZIP(t, "001.jpg"),
			"02권":     imageDir(t, "001.jpg", "002.jpg"),
		},
	})
	h.run(Request{})
	if got := len(h.lister.listedPaths()); got != 5 {
		t.Fatalf("the cold scan read %d books, want 5", got)
	}

	// A rescan with nothing changed must read nothing at all.
	h.lister.reset()
	res := h.run(Request{})
	if got := h.lister.listedPaths(); len(got) != 0 {
		t.Fatalf("a no-change rescan read %v; FR-IDX-003 requires it to read nothing", got)
	}
	if _, books, _, skipped, _ := res.Totals(); skipped != books || books != 5 {
		t.Errorf("no-change rescan: %d of %d books skipped, want 5 of 5", skipped, books)
	}

	testutil.Touch(t, h.rootDirs["manga"]+"/시리즈 a/02권.zip", 3*time.Second)

	h.lister.reset()
	res = h.run(Request{})
	if got := h.lister.listedPaths(); !equalStrings(got, []string{"시리즈 a/02권.zip"}) {
		t.Errorf("after touching one file the scan read %v, want only [시리즈 a/02권.zip]", got)
	}
	if _, books, _, skipped, _ := res.Totals(); skipped != 4 || books != 5 {
		t.Errorf("%d of %d books skipped, want 4 of 5", skipped, books)
	}
	// The re-read book keeps its content, and so does everything else.
	if got := h.books("manga", "시리즈 a")[1].PageCount; got != 2 {
		t.Errorf("re-indexed book has %d pages, want 2", got)
	}
	for _, b := range h.books("manga", "시리즈 b") {
		if b.PageCount == 0 {
			t.Errorf("skipped book %q lost its pages", b.DisplayName)
		}
	}
}

// A directory book's contents can change without its own mtime moving, which is
// exactly why the fingerprint exists (arch §4.6).
func TestScan_incremental_directoryBook_noticesANestedChange(t *testing.T) {
	t.Parallel()
	root := testutil.BuildTree(t, map[string]any{
		"시리즈": map[string]any{
			"01권": imageDir(t, "001.jpg", "002.jpg"),
			"02권": imageDir(t, "001.jpg"),
		},
	})
	h := newHarnessAt(t, map[string]string{"manga": root})
	h.run(Request{})

	h.lister.reset()
	h.run(Request{})
	if got := h.lister.listedPaths(); len(got) != 0 {
		t.Fatalf("a no-change rescan of directory books read %v", got)
	}

	// Rewrite one page in place: the file's mtime moves, the directory's does
	// not on most filesystems, and a (size, mtime) test on the directory would
	// miss it entirely.
	testutil.Touch(t, root+"/시리즈/01권/002.jpg", 5*time.Second)

	h.lister.reset()
	h.run(Request{})
	if got := h.lister.listedPaths(); !equalStrings(got, []string{"시리즈/01권"}) {
		t.Errorf("after a nested change the scan read %v, want only [시리즈/01권]", got)
	}
}

// `full: true` is `--rebuild-index` and `POST /api/scan {"full": true}`: it
// bypasses every skip.
func TestScan_full_neverSkipsAnything(t *testing.T) {
	t.Parallel()
	h := newHarness(t, map[string]any{
		"시리즈": map[string]any{
			"01권.zip": jpegZIP(t, "001.jpg"),
			"02권.zip": jpegZIP(t, "001.jpg"),
		},
	})
	h.run(Request{})

	h.lister.reset()
	res := h.run(Request{Full: true})
	if got := len(h.lister.listedPaths()); got != 2 {
		t.Errorf("a full rescan read %d books, want 2", got)
	}
	if _, _, _, skipped, _ := res.Totals(); skipped != 0 {
		t.Errorf("a full rescan skipped %d books, want 0", skipped)
	}
}

// An unchanged book that MOVED still needs its `ord` rewritten, or inserting one
// volume would misorder every later one while every later one is "unchanged".
func TestScan_incremental_insertedVolume_rewritesTheOrdOfLaterBooks(t *testing.T) {
	t.Parallel()
	// Every mtime is pinned. The skip rule compares (size, mtime) in whole
	// seconds (arch §4.6), and the two trees below are written at two different
	// instants: left to the filesystem, a run that straddled a second boundary
	// re-read both "unchanged" volumes, so the skip path this test exists for
	// went unexercised — a mutation dropping keep()'s page count survived 1 run
	// in 40. Distinct instants per volume keep any two of them from sharing a
	// content_version (test audit, 10-26).
	v1, v2, v3 := jpegZIP(t, "001.jpg"), jpegZIP(t, "001.jpg", "002.jpg"), jpegZIP(t, "001.jpg")
	at := func(data []byte, sec int64) testutil.File {
		return testutil.File{Data: data, ModTime: time.Unix(1_700_000_000+sec, 0)}
	}
	before := testutil.BuildTree(t, map[string]any{
		"시리즈": map[string]any{
			"01권.zip": at(v1, 1),
			"03권.zip": at(v3, 3),
		},
	})
	h := newHarnessAt(t, map[string]string{"manga": before})
	h.run(Request{})
	if got := bookNames(h.books("manga", "시리즈")); !equalStrings(got, []string{"01권.zip", "03권.zip"}) {
		t.Fatalf("first scan books = %v", got)
	}

	// The same two files plus a new one in the middle. Rebinding the root name
	// to a second tree is how this package's tests change a library without
	// calling a write primitive (FR-CFG-005 lint guard).
	after := testutil.BuildTree(t, map[string]any{
		"시리즈": map[string]any{
			"01권.zip": at(v1, 1),
			"02권.zip": at(v2, 2),
			"03권.zip": at(v3, 3),
		},
	})
	h.rootDirs["manga"] = after
	h.cfgRoots[0].Path = after
	h.build()
	h.run(Request{})
	// The premise, asserted rather than assumed: only the new volume is read,
	// so 01권 and 03권 reach the index through keep() (test audit, 10-26).
	if got := h.lister.listedPaths(); !equalStrings(got, []string{"시리즈/02권.zip"}) {
		t.Fatalf("the rescan read %v, want only [시리즈/02권.zip] — the unchanged volumes must be skipped "+
			"for this test to mean anything", got)
	}

	books := h.books("manga", "시리즈")
	if got := bookNames(books); !equalStrings(got, []string{"01권.zip", "02권.zip", "03권.zip"}) {
		t.Fatalf("books = %v", got)
	}
	for i, b := range books {
		if b.Ord != i {
			t.Errorf("book %q has ord %d at position %d; an inserted volume must renumber the rest",
				b.DisplayName, b.Ord, i)
		}
	}
	// The two unchanged volumes kept their pages: their `ord` was rewritten,
	// their page rows were not re-read.
	for _, i := range []int{0, 2} {
		if books[i].PageCount != 1 {
			t.Errorf("unchanged book %q has %d pages, want 1", books[i].DisplayName, books[i].PageCount)
		}
	}
	// The series row is a roll-up of all three volumes, two of them skipped, and
	// it must be rewritten: book_count, page_count and total_bytes (arch §3.5 series
	// DDL; total_bytes is the containers on disk, ruling E-11). Stamped forward as
	// "unchanged", it would keep the cold scan's 2 books, which the library grid
	// and the 용량 sort read straight off the row (test audit, 10-26).
	assertSeriesRollUp(t, h.seriesAt("manga", "시리즈"), 3, 1+2+1, int64(len(v1)+len(v2)+len(v3)))
}

// assertSeriesRollUp checks the three aggregates writeSeries derives from a
// series' books: how many, how many pages, and the bytes they occupy on disk
// (test audit, 10-26).
func assertSeriesRollUp(t *testing.T, s index.SeriesDetail, books, pages, bytes int64) {
	t.Helper()
	if s.BookCount != books || s.PageCount != pages || s.TotalBytes != bytes {
		t.Errorf("series %q = %d books / %d pages / %d bytes, want %d / %d / %d",
			s.DisplayName, s.BookCount, s.PageCount, s.TotalBytes, books, pages, bytes)
	}
}

// The other direction of the same roll-up: a volume leaves a series the index
// already holds. The sweep deletes the vanished book row and never touches the
// series row (index.Writer.SweepRoot), so writeSeries rewriting the row from
// the books that remain is the only thing that brings book_count down — and the
// two survivors are skipped, so nothing else about the run looks changed
// (FR-IDX-003, arch §4.9; test audit, 10-26).
func TestScan_incremental_removedVolume_rewritesTheSeriesRollUp(t *testing.T) {
	t.Parallel()
	v1, v2, v3 := jpegZIP(t, "001.jpg"), jpegZIP(t, "001.jpg", "002.jpg"), jpegZIP(t, "001.jpg")
	at := func(data []byte, sec int64) testutil.File {
		return testutil.File{Data: data, ModTime: time.Unix(1_700_000_000+sec, 0)}
	}
	before := testutil.BuildTree(t, map[string]any{
		"시리즈": map[string]any{
			"01권.zip": at(v1, 1),
			"02권.zip": at(v2, 2),
			"03권.zip": at(v3, 3),
		},
	})
	h := newHarnessAt(t, map[string]string{"manga": before})
	h.run(Request{})
	if s := h.seriesAt("manga", "시리즈"); s.BookCount != 3 || s.PageCount != 4 {
		t.Fatalf("cold scan: %d books / %d pages, want 3 / 4", s.BookCount, s.PageCount)
	}

	after := testutil.BuildTree(t, map[string]any{
		"시리즈": map[string]any{
			"01권.zip": at(v1, 1),
			"03권.zip": at(v3, 3),
		},
	})
	h.rootDirs["manga"] = after
	h.cfgRoots[0].Path = after
	h.build()
	h.run(Request{})
	if got := h.lister.listedPaths(); len(got) != 0 {
		t.Fatalf("the rescan read %v; both survivors must be skipped for this test to mean anything", got)
	}
	if got := bookNames(h.books("manga", "시리즈")); !equalStrings(got, []string{"01권.zip", "03권.zip"}) {
		t.Fatalf("books = %v, want 02권.zip swept", got)
	}
	assertSeriesRollUp(t, h.seriesAt("manga", "시리즈"), 2, 1+1, int64(len(v1)+len(v3)))
}

// arch §4.9 + FR-IDX-003 together: an unchanged book must be stamped forward, or
// the sweep at the end of the very next scan would delete it.
func TestScan_incremental_stampsUnchangedRowsForwardSoTheSweepSparesThem(t *testing.T) {
	t.Parallel()
	h := newHarness(t, map[string]any{
		"시리즈": map[string]any{
			"01권.zip": jpegZIP(t, "001.jpg"),
			"02권":     imageDir(t, "001.jpg"),
		},
	})
	first := h.run(Request{})

	second := h.run(Request{})
	if second.ScanGen <= first.ScanGen {
		t.Fatalf("scan_gen did not advance: %d then %d", first.ScanGen, second.ScanGen)
	}
	if second.Roots[0].Swept != (index.SweepResult{}) {
		t.Fatalf("a no-change rescan swept %+v", second.Roots[0].Swept)
	}
	// A no-change rescan also sees nothing move. The counters above cannot say
	// so: a relocation removes a row and adds one, so it is a sweep of exactly
	// the shape this assertion already tolerates when the numbers happen to
	// cancel.
	if got := second.Roots[0].Relocations; len(got) != 0 {
		t.Fatalf("a no-change rescan reported %+v as moved", got)
	}
	for _, b := range h.books("manga", "시리즈") {
		if b.ScanGen != second.ScanGen {
			t.Errorf("book %q carries scan_gen %d, want %d — it would be swept next time",
				b.DisplayName, b.ScanGen, second.ScanGen)
		}
	}
	if s := h.seriesAt("manga", "시리즈"); s.ScanGen != second.ScanGen {
		t.Errorf("series carries scan_gen %d, want %d", s.ScanGen, second.ScanGen)
	}

	// A third scan proves the stamping is not a one-off: nothing is ever swept
	// while nothing changes.
	third := h.run(Request{})
	if third.Roots[0].Swept != (index.SweepResult{}) {
		t.Errorf("the third no-change rescan swept %+v", third.Roots[0].Swept)
	}
	if got := len(h.books("manga", "시리즈")); got != 2 {
		t.Fatalf("the library shrank to %d books over three no-change scans", got)
	}
}

// sameSeriesRow is what chooses between rewriting a series row and stamping it
// forward, so a column it forgets to compare is a column no rescan can change:
// the row keeps the old value and is stamped as current. The three aggregates
// are why this test exists. On disk they move together — a volume that arrives
// or leaves changes the count, the pages and the bytes at once — so the roll-up
// tests above notice all three comparisons going missing but not one of them.
// Here each column moves alone, as TestFingerprintChildren_movesWithEveryTupleField
// does for the fingerprint (test audit, 10-26).
func TestSameSeriesRow_movesWithEveryColumnButTheGenerationAndAddedAt(t *testing.T) {
	t.Parallel()
	row := func() index.Series {
		return index.Series{
			ID: "s1", RootName: "manga", RelPath: "시리즈", DisplayName: "시리즈",
			SortKey: []byte("시리즈"), SearchKey: "시리즈", ChoseongKey: "ㅅㄹㅈ", Kind: "folder",
			BookCount: 3, PageCount: 4, TotalBytes: 2934, Mtime: 1_700_000_003, AddedAt: 1_700_000_100,
			CoverKind: "page", CoverBookID: "b1", CoverPageNo: 1, CoverRelPath: "시리즈/01권.zip",
			Status: StatusOK, ScanGen: 7,
		}
	}
	if !sameSeriesRow(row(), row()) {
		t.Fatal("an identical row is not recognised as unchanged; no series would ever be stamped forward")
	}
	if sameSeriesRow(index.Series{}, index.Series{}) {
		t.Error("an empty prior row matched — a series the index never held must be written, not stamped")
	}

	// Left out on purpose (gen.go): the generation is what the stamp writes, and
	// index.Writer never moves added_at forward.
	for name, move := range map[string]func(*index.Series){
		"scan_gen": func(s *index.Series) { s.ScanGen++ },
		"added_at": func(s *index.Series) { s.AddedAt++ },
	} {
		next := row()
		move(&next)
		if !sameSeriesRow(row(), next) {
			t.Errorf("a row whose %s alone moved is rewritten; that column is not part of the comparison", name)
		}
	}

	for name, move := range map[string]func(*index.Series){
		"book_count":     func(s *index.Series) { s.BookCount++ },
		"page_count":     func(s *index.Series) { s.PageCount++ },
		"total_bytes":    func(s *index.Series) { s.TotalBytes++ },
		"id":             func(s *index.Series) { s.ID = "s2" },
		"root_name":      func(s *index.Series) { s.RootName = "manga2" },
		"rel_path":       func(s *index.Series) { s.RelPath = "시리즈2" },
		"display_name":   func(s *index.Series) { s.DisplayName = "시리즈2" },
		"sort_key":       func(s *index.Series) { s.SortKey = []byte("시리즈2") },
		"search_key":     func(s *index.Series) { s.SearchKey = "시리즈2" },
		"choseong_key":   func(s *index.Series) { s.ChoseongKey = "ㅅㄹㅈ2" },
		"kind":           func(s *index.Series) { s.Kind = "zip" },
		"mtime":          func(s *index.Series) { s.Mtime++ },
		"cover_kind":     func(s *index.Series) { s.CoverKind = "file" },
		"cover_book_id":  func(s *index.Series) { s.CoverBookID = "b2" },
		"cover_page_no":  func(s *index.Series) { s.CoverPageNo++ },
		"cover_rel_path": func(s *index.Series) { s.CoverRelPath = "시리즈/02권.zip" },
		"status":         func(s *index.Series) { s.Status = StatusError },
		"error":          func(s *index.Series) { s.Error = "읽을 수 있는 페이지가 없습니다" },
	} {
		next := row()
		move(&next)
		if sameSeriesRow(row(), next) {
			t.Errorf("a row whose %s alone moved is stamped forward instead of rewritten", name)
		}
	}
}

// A book that is still broken keeps its status and its reason — but it keeps
// them because the next scan looked again and got the same answer, not because
// the answer was remembered (FR-IDX-010 ∩ ruling E-39, draft).
func TestScan_incremental_brokenBook_keepsItsStatusByBeingReExamined(t *testing.T) {
	t.Parallel()
	h := newHarness(t, map[string]any{
		"시리즈": map[string]any{"07권.zip": []byte("not a zip at all")},
	})
	h.run(Request{})
	first := h.books("manga", "시리즈")[0]
	if first.Status != StatusError || first.Error == "" {
		t.Fatalf("first scan status = %q error %q, want error with a reason", first.Status, first.Error)
	}

	h.lister.reset()
	h.run(Request{})
	if got := h.lister.listedPaths(); !equalStrings(got, []string{"시리즈/07권.zip"}) {
		t.Errorf("the broken book was read %v, want [시리즈/07권.zip] — E-39 re-examines it", got)
	}
	second := h.books("manga", "시리즈")[0]
	if second.Status != first.Status || second.Error != first.Error {
		t.Errorf("status/error changed on a re-read of the same broken bytes: %q/%q -> %q/%q",
			first.Status, first.Error, second.Status, second.Error)
	}
}

// A wrong verdict must not be permanent — ruling E-39 (draft).
//
// Measured on the real library: `궁 24.zip` was repaired on disk and stayed
// `비어 있음 / 읽을 수 있는 페이지가 없습니다` through every later 재스캔, because
// arch §4.6's skip rule only ever re-examined 'unsupported'. An 'empty' or an
// 'error' whose (size, mtime) already match the disk was skipped for ever, and
// the only escape was `full: true` — which the 재스캔 button does not send.
//
// The two failures here are the two shapes that are *not* properties of the
// file: a listing derived from the wrong inode (defect ①, which produced this
// exact 'empty'), and a transient I/O error. Neither moves a byte on disk, so
// under the old rule neither could ever be revisited.
func TestScan_incremental_aWrongVerdictIsNotPermanent(t *testing.T) {
	t.Parallel()
	h := newHarness(t, map[string]any{
		"시리즈": map[string]any{
			"01권.zip": jpegZIP(t, "001.jpg", "002.jpg"),
			"02권.zip": jpegZIP(t, "001.jpg"),
			"03권.zip": jpegZIP(t, "001.jpg"),
		},
	})

	// The scan that got it wrong. The archives on disk are perfectly readable
	// throughout — only this run's answer is bad.
	h.lister.failWith["시리즈/01권.zip"] = fmt.Errorf("listing: %w", source.ErrNoPages)
	h.lister.failWith["시리즈/02권.zip"] = fmt.Errorf("listing: %w", archive.ErrCorrupt)
	h.run(Request{})

	books := h.books("manga", "시리즈")
	if len(books) != 3 {
		t.Fatalf("indexed %d books, want 3", len(books))
	}
	if books[0].Status != StatusEmpty || books[1].Status != StatusError || books[2].Status != StatusOK {
		t.Fatalf("first-scan statuses = %q/%q/%q, want empty/error/ok",
			books[0].Status, books[1].Status, books[2].Status)
	}

	// Nothing on disk changes. Not one byte, not one timestamp — that is the
	// whole point: (size, mtime) cannot tell these books apart from the healthy
	// one, so the *status* has to.
	before := snapshotTree(t, h.rootDirs["manga"])
	clear(h.lister.failWith)
	h.lister.reset()
	res := h.run(Request{}) // an ordinary rescan, exactly what 재스캔 sends
	if diff := before.diff(snapshotTree(t, h.rootDirs["manga"])); len(diff) != 0 {
		t.Fatalf("the fixture moved between the two scans, so this proves nothing: %v", diff)
	}

	books = h.books("manga", "시리즈")
	for i, b := range books {
		if b.Status != StatusOK || b.Error != "" {
			t.Errorf("book %d (%s) = %q/%q after the rescan, want ok with no error",
				i+1, b.RelPath, b.Status, b.Error)
		}
	}
	if books[0].PageCount != 2 || books[1].PageCount != 1 {
		t.Errorf("recovered page counts = %d/%d, want 2/1 — the rows were healed but not re-read",
			books[0].PageCount, books[1].PageCount)
	}
	if got := len(h.pages(books[0].ID)); got != 2 {
		t.Errorf("the recovered book has %d page rows, want 2", got)
	}

	// And the rule stays a skip rule: the healthy volume was still not opened.
	if got := h.lister.listedPaths(); !equalStrings(got, []string{"시리즈/01권.zip", "시리즈/02권.zip"}) {
		t.Errorf("the rescan read %v, want only the two non-ok books", got)
	}
	if _, _, _, skipped, _ := res.Totals(); skipped != 1 {
		t.Errorf("the rescan skipped %d books, want 1 (the ok one)", skipped)
	}
}
