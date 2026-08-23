package index_test

import (
	"testing"

	"shelf/internal/index"
	"shelf/internal/userdata"
)

// splitFixture writes one series whose single container was split into volumes,
// plus one ordinary series whose book is its own file. `seed` cannot express
// inner_path, and inner_path is the whole subject here, so the fixture is
// written straight through the writer.
func splitFixture(t *testing.T, idx *index.DB) {
	t.Helper()
	ctx := t.Context()
	if err := idx.UpsertRoot(ctx, index.Root{
		Name: "manga", Path: "/media/manga", Label: "manga", Enabled: true,
	}); err != nil {
		t.Fatalf("UpsertRoot: %v", err)
	}
	// A second root, so a relocation between roots has somewhere to have gone.
	if err := idx.UpsertRoot(ctx, index.Root{
		Name: "books", Path: "/media/books", Label: "books", Enabled: true,
	}); err != nil {
		t.Fatalf("UpsertRoot books: %v", err)
	}
	w := idx.Writer(index.WriterOptions{})
	defer func() {
		if err := w.Close(); err != nil {
			t.Fatalf("writer.Close: %v", err)
		}
	}()

	if err := w.UpsertSeries(ctx, index.Series{
		ID: "ser-split", RootName: "manga", RelPath: "c.zip", DisplayName: "c.zip",
		SortKey: []byte("c"), Kind: "zip", BookCount: 3, PageCount: 35,
		Status: "ok", ScanGen: 1,
	}); err != nil {
		t.Fatalf("UpsertSeries: %v", err)
	}
	// Two things are deliberate. The rows are upserted out of order, and the
	// inner paths sort in the exact REVERSE of `ord` — z, m, a for volumes 1, 2,
	// 3. A fixture whose names agree with `ord` cannot catch a missing ORDER BY:
	// SQLite serves this filter from the UNIQUE(root_name, rel_path, inner_path)
	// index and hands back inner-path order for free, so the ordering assertion
	// passes on a query that never sorted at all. This fixture had that defect
	// until a mutation run said so, and the reversal is what closes it.
	for _, v := range []struct {
		id    string
		inner string
		ord   int
		pages int
	}{
		{"vol-c", "a-3권", 2, 5},
		{"vol-a", "z-1권", 0, 10},
		{"vol-b", "m-2권", 1, 20},
	} {
		if err := w.UpsertBook(ctx, index.Book{
			ID: v.id, SeriesID: "ser-split", RootName: "manga", RelPath: "c.zip",
			InnerPath: v.inner, DisplayName: v.inner, SortKey: []byte(v.inner),
			Ord: v.ord, Kind: "nesteddir", PageCount: int64(v.pages),
			ContentVersion: "cv" + v.id, Status: "ok", ScanGen: 1,
		}); err != nil {
			t.Fatalf("UpsertBook %s: %v", v.id, err)
		}
	}

	if err := w.UpsertSeries(ctx, index.Series{
		ID: "ser-plain", RootName: "manga", RelPath: "plain", DisplayName: "plain",
		SortKey: []byte("plain"), Kind: "folder", BookCount: 1, PageCount: 7,
		Status: "ok", ScanGen: 1,
	}); err != nil {
		t.Fatalf("UpsertSeries plain: %v", err)
	}
	if err := w.UpsertBook(ctx, index.Book{
		ID: "book-plain", SeriesID: "ser-plain", RootName: "manga",
		RelPath: "plain/v1.zip", DisplayName: "v1.zip", SortKey: []byte("v1"),
		Ord: 0, Kind: "zip", PageCount: 7, ContentVersion: "cvplain",
		Status: "ok", ScanGen: 1,
	}); err != nil {
		t.Fatalf("UpsertBook plain: %v", err)
	}
}

func putProgress(t *testing.T, u *userdata.DB, bookID, seriesID, path string, page, count int) {
	t.Helper()
	if _, err := u.PutProgress(t.Context(), userdata.ProgressUpdate{
		BookID: bookID, SeriesID: seriesID, RootName: "manga", BookPath: path,
		Page: page, PageCount: count,
	}); err != nil {
		t.Fatalf("PutProgress %s: %v", bookID, err)
	}
}

func TestSplitOrphans_listsOnlyRowsTheIndexCannotResolve(t *testing.T) {
	t.Parallel()
	idx, u, _ := newDBs(t)
	splitFixture(t, idx)

	// The row D-73 orphaned: it names the container, which stopped being a book.
	putProgress(t, u, "old-container", "ser-split", "c.zip", 11, 35)
	// A row that still resolves. It must not appear.
	putProgress(t, u, "book-plain", "ser-plain", "plain/v1.zip", 3, 7)

	got, err := idx.SplitOrphans(t.Context())
	if err != nil {
		t.Fatalf("SplitOrphans: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d orphans, want 1 (the resolvable row must not be listed): %+v", len(got), got)
	}
	o := got[0]
	if o.BookID != "old-container" || o.BookPath != "c.zip" || o.RootName != "manga" {
		t.Errorf("orphan identity %+v, want the container row", o)
	}
	if o.LastPage != 11 || o.PageCount != 35 || o.SeriesID != "ser-split" {
		t.Errorf("orphan payload %+v, want page 11 of baseline 35 in ser-split", o)
	}
	if o.Completed {
		t.Error("orphan reported completed; the row was written at page 11 of 35")
	}
}

func TestSplitOrphans_cleanLibraryReturnsNothing(t *testing.T) {
	t.Parallel()
	idx, u, _ := newDBs(t)
	splitFixture(t, idx)
	putProgress(t, u, "book-plain", "ser-plain", "plain/v1.zip", 3, 7)
	putProgress(t, u, "vol-b", "ser-split", "c.zip", 2, 20)

	got, err := idx.SplitOrphans(t.Context())
	if err != nil {
		t.Fatalf("SplitOrphans: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %+v, want no orphans", got)
	}
}

// The volumes of a container come back in reading order — not in the order the
// rows were written, and not in the order their names sort.
func TestBooksAt_returnsVolumesInReadingOrder(t *testing.T) {
	t.Parallel()
	idx, _, _ := newDBs(t)
	splitFixture(t, idx)

	loc := index.Location{RootName: "manga", RelPath: "c.zip"}
	got, err := idx.BooksAt(t.Context(), []index.Location{loc})
	if err != nil {
		t.Fatalf("BooksAt: %v", err)
	}
	vols := got[loc]
	if len(vols) != 3 {
		t.Fatalf("got %d volumes, want 3", len(vols))
	}
	wantIDs := []string{"vol-a", "vol-b", "vol-c"}
	wantPages := []int{10, 20, 5}
	for i, v := range vols {
		if v.BookID != wantIDs[i] || v.Ord != i || v.PageCount != wantPages[i] {
			t.Errorf("volume %d = %+v, want id %q ord %d pages %d",
				i, v, wantIDs[i], i, wantPages[i])
		}
	}
}

// An ordinary book is the answer for a rename, so it must not be filtered out.
// An `inner_path <> ''` filter would look like a safety rail and would silently
// make every renamed row unrepairable.
func TestBooksAt_includesAnOrdinaryBook(t *testing.T) {
	t.Parallel()
	idx, _, _ := newDBs(t)
	splitFixture(t, idx)

	loc := index.Location{RootName: "manga", RelPath: "plain/v1.zip"}
	got, err := idx.BooksAt(t.Context(), []index.Location{loc})
	if err != nil {
		t.Fatalf("BooksAt: %v", err)
	}
	vols := got[loc]
	if len(vols) != 1 || vols[0].BookID != "book-plain" || vols[0].PageCount != 7 {
		t.Fatalf("got %+v, want the one ordinary book at 7 pages", vols)
	}
}

// A Location with nothing at it is absent from the map, not present and empty,
// so a caller can tell "nothing there" from "I did not ask" — which is what
// lets the plan count candidate hits without a second lookup.
func TestBooksAt_absentLocationIsAbsentFromTheMap(t *testing.T) {
	t.Parallel()
	idx, _, _ := newDBs(t)
	splitFixture(t, idx)

	gone := index.Location{RootName: "manga", RelPath: "vanished.zip"}
	elsewhere := index.Location{RootName: "books", RelPath: "c.zip"}
	got, err := idx.BooksAt(t.Context(), []index.Location{gone, elsewhere})
	if err != nil {
		t.Fatalf("BooksAt: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %+v, want an empty map", got)
	}
}

// The same path under a different root is a different book, because the root
// name is hashed into the id. BooksAt must key on both.
func TestBooksAt_rootNameIsPartOfTheKey(t *testing.T) {
	t.Parallel()
	idx, _, _ := newDBs(t)
	splitFixture(t, idx)

	mine := index.Location{RootName: "manga", RelPath: "c.zip"}
	theirs := index.Location{RootName: "books", RelPath: "c.zip"}
	got, err := idx.BooksAt(t.Context(), []index.Location{mine, theirs})
	if err != nil {
		t.Fatalf("BooksAt: %v", err)
	}
	if len(got[mine]) != 3 {
		t.Errorf("manga/c.zip has %d books, want 3", len(got[mine]))
	}
	if _, present := got[theirs]; present {
		t.Error("books/c.zip answered; nothing was written under that root")
	}
}

func TestBooksAt_noLocations(t *testing.T) {
	t.Parallel()
	idx, _, _ := newDBs(t)
	splitFixture(t, idx)
	got, err := idx.BooksAt(t.Context(), nil)
	if err != nil {
		t.Fatalf("BooksAt(nil): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %+v, want nothing", got)
	}
}

func TestRootNames_sorted(t *testing.T) {
	t.Parallel()
	idx, _, _ := newDBs(t)
	splitFixture(t, idx)
	got, err := idx.RootNames(t.Context())
	if err != nil {
		t.Fatalf("RootNames: %v", err)
	}
	if len(got) != 2 || got[0] != "books" || got[1] != "manga" {
		t.Errorf("got %v, want [books manga]", got)
	}
}

// A row filed under the wrong series is the failure that looks like nothing is
// wrong: the book resolves, so 이어보기 shows it, while every count that groups
// by series_id — the shelf percentage, 읽는 중, 완독 — cannot see it.
func TestMisfiledProgress_findsARowUnderTheWrongSeries(t *testing.T) {
	t.Parallel()
	idx, u, _ := newDBs(t)
	splitFixture(t, idx)

	// The book is real; the series recorded beside it is not the one it is in.
	if _, err := u.PutProgress(t.Context(), userdata.ProgressUpdate{
		BookID: "vol-b", SeriesID: "ser-somewhere-else", RootName: "manga",
		BookPath: "c.zip", Page: 2, PageCount: 20,
	}); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	// And one filed correctly, which must not be reported.
	if _, err := u.PutProgress(t.Context(), userdata.ProgressUpdate{
		BookID: "book-plain", SeriesID: "ser-plain", RootName: "manga",
		BookPath: "plain/v1.zip", Page: 3, PageCount: 7,
	}); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	got, err := idx.MisfiledProgress(t.Context())
	if err != nil {
		t.Fatalf("MisfiledProgress: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d misfiled rows, want 1: %+v", len(got), got)
	}
	if got[0].BookID != "vol-b" || got[0].ActualSeria != "ser-split" ||
		got[0].StoredSeria != "ser-somewhere-else" {
		t.Errorf("got %+v, want vol-b stored under ser-somewhere-else, actually ser-split", got[0])
	}
}

// An orphan is not misfiled: its book is not in the index at all, so the join
// finds nothing and there is no disagreement to report. The two repairs must not
// both claim the same row.
func TestMisfiledProgress_ignoresOrphans(t *testing.T) {
	t.Parallel()
	idx, u, _ := newDBs(t)
	splitFixture(t, idx)
	putProgress(t, u, "old-container", "ser-split", "c.zip", 11, 35)

	got, err := idx.MisfiledProgress(t.Context())
	if err != nil {
		t.Fatalf("MisfiledProgress: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %+v, want nothing — an orphan has no book to disagree with", got)
	}
}

func TestMisfiledProgress_cleanLibraryReturnsNothing(t *testing.T) {
	t.Parallel()
	idx, u, _ := newDBs(t)
	splitFixture(t, idx)
	putProgress(t, u, "vol-a", "ser-split", "c.zip", 2, 10)
	putProgress(t, u, "book-plain", "ser-plain", "plain/v1.zip", 1, 7)

	got, err := idx.MisfiledProgress(t.Context())
	if err != nil {
		t.Fatalf("MisfiledProgress: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %+v, want nothing", got)
	}
}

// The catastrophic case, asserted first: no root finished cleanly must never
// read as "every reading position is stale". An empty list returns nothing, and
// the caller cannot get everything by asking for nothing.
func TestVanishedProgress_noRootsMeansNothing(t *testing.T) {
	t.Parallel()
	idx, u, _ := newDBs(t)
	splitFixture(t, idx)
	putProgress(t, u, "long-gone", "ser-split", "vanished.zip", 4, 100)

	got, err := idx.VanishedProgress(t.Context(), nil)
	if err != nil {
		t.Fatalf("VanishedProgress(nil): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %+v for an empty root list; it must never mean 'all of them'", got)
	}
}

// Only the roots the caller walked. A row belonging to a root that failed, was
// cancelled or was never visited is not evidence of anything.
func TestVanishedProgress_onlyTheRootsAskedAbout(t *testing.T) {
	t.Parallel()
	idx, u, _ := newDBs(t)
	splitFixture(t, idx)
	putProgress(t, u, "gone-manga", "ser-split", "vanished.zip", 4, 100)
	if _, err := u.PutProgress(t.Context(), userdata.ProgressUpdate{
		BookID: "gone-books", SeriesID: "ser-x", RootName: "books",
		BookPath: "elsewhere.zip", Page: 2, PageCount: 50,
	}); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	got, err := idx.VanishedProgress(t.Context(), []string{"manga"})
	if err != nil {
		t.Fatalf("VanishedProgress: %v", err)
	}
	if len(got) != 1 || got[0].BookID != "gone-manga" {
		t.Fatalf("got %+v, want only the manga row", got)
	}
	if got[0].BookPath != "vanished.zip" || got[0].LastPage != 4 || got[0].PageCount != 100 {
		t.Errorf("row %+v, want the path and position carried for the log line", got[0])
	}
}

// A book that resolves is not vanished, whatever else is true of it.
func TestVanishedProgress_ignoresRowsThatResolve(t *testing.T) {
	t.Parallel()
	idx, u, _ := newDBs(t)
	splitFixture(t, idx)
	putProgress(t, u, "vol-a", "ser-split", "c.zip", 2, 10)
	putProgress(t, u, "book-plain", "ser-plain", "plain/v1.zip", 1, 7)

	got, err := idx.VanishedProgress(t.Context(), []string{"manga", "books"})
	if err != nil {
		t.Fatalf("VanishedProgress: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %+v, want nothing", got)
	}
}
