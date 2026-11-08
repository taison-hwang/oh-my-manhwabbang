package index_test

import (
	"testing"

	"shelf/internal/index"
)

// OldSeriesID is what lets the post-scan cleanup decide a series-level 1:1
// move (ruling E-69 §2.11(e)): the books of one old series, all gone to one
// new series. It is observed in the same statement and the same moment as the
// rest of the pairing — after the walk, before the sweep — because the old
// series row is swept in the same transaction and nothing knows it afterwards.
//
// The fixture has no progress at all, on purpose. A series nobody has read a
// page of can still carry a reading direction the reader set on it, and that
// series is the actual subject of §2.11(e); a test that only paired books with
// progress would be checking the old mechanism.
func TestSweepRoot_relocationCarriesTheOldSeries(t *testing.T) {
	t.Parallel()
	idx, _, _ := newDBs(t)
	ctx := t.Context()
	if err := idx.UpsertRoot(ctx, index.Root{
		Name: "manga", Path: "/media/manga", Label: "manga", Enabled: true,
	}); err != nil {
		t.Fatalf("UpsertRoot: %v", err)
	}
	w := idx.Writer(index.WriterOptions{})
	series := func(id, path string, gen int64) {
		t.Helper()
		if err := w.UpsertSeries(ctx, index.Series{
			ID: id, RootName: "manga", RelPath: path, DisplayName: path,
			SortKey: []byte(path), Kind: "folder", BookCount: 1, PageCount: 9,
			Status: "ok", ScanGen: gen,
		}); err != nil {
			t.Fatalf("UpsertSeries %s: %v", id, err)
		}
	}
	book := func(id, sid, path, cv string, gen int64) {
		t.Helper()
		if err := w.UpsertBook(ctx, index.Book{
			ID: id, SeriesID: sid, RootName: "manga", RelPath: path,
			DisplayName: path, SortKey: []byte(path), Ord: 0, Kind: "zip",
			PageCount: 9, ContentVersion: cv, Status: "ok", ScanGen: gen,
		}); err != nil {
			t.Fatalf("UpsertBook %s: %v", id, err)
		}
	}
	// Generation 1: the series as it was. Generation 2: the walk after the
	// directory was renamed, which wrote the same file under a new series.
	series("ser-old", "옛 이름", 1)
	book("book-old", "ser-old", "옛 이름/01권.zip", "cv-moved", 1)
	series("ser-new", "새 이름", 2)
	book("book-new", "ser-new", "새 이름/01권.zip", "cv-moved", 2)

	_, moved, err := w.SweepRoot(ctx, "manga", 2)
	if err != nil {
		t.Fatalf("SweepRoot: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("writer.Close: %v", err)
	}
	if len(moved) != 1 {
		t.Fatalf("got %d relocations, want 1: %+v", len(moved), moved)
	}
	r := moved[0]
	if r.OldBookID != "book-old" || r.NewBookID != "book-new" {
		t.Fatalf("paired %s -> %s, want book-old -> book-new", r.OldBookID, r.NewBookID)
	}
	if r.OldSeriesID != "ser-old" {
		t.Errorf("OldSeriesID = %q, want ser-old (the series the vanishing row belonged to)", r.OldSeriesID)
	}
	if r.NewSeriesID != "ser-new" {
		t.Errorf("NewSeriesID = %q, want ser-new", r.NewSeriesID)
	}
}
