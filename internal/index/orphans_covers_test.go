package index_test

import (
	"testing"

	"shelf/internal/index"
	"shelf/internal/userdata"
)

// The E-69 §2.11 half of orphans.go: the same three questions — what does the
// index not resolve, what is filed under the wrong series, what has a walked
// root lost — asked of book_covers, book_prefs and series_prefs.

func putCover(t *testing.T, u *userdata.DB, bookID, seriesID, root, path string, page, count int) {
	t.Helper()
	if _, err := u.PutCover(t.Context(), userdata.CoverPut{
		BookID: bookID, SeriesID: seriesID, RootName: root, BookPath: path, PageCount: count,
		Region: userdata.CoverRegion{Page: page, X: 0.1, Y: 0.2, W: 0.3, H: 0.4},
	}); err != nil {
		t.Fatalf("PutCover %s: %v", bookID, err)
	}
}

func putSeriesDir(t *testing.T, u *userdata.DB, seriesID, root, path string) {
	t.Helper()
	dir := "rtl"
	if _, err := u.PutSeriesPrefs(t.Context(), userdata.SeriesPrefsPut{
		SeriesID: seriesID, RootName: root, SeriesPath: path, ReadingDir: &dir,
	}); err != nil {
		t.Fatalf("PutSeriesPrefs %s: %v", seriesID, err)
	}
}

func TestCoverOrphans_listsOnlyRowsTheIndexCannotResolve(t *testing.T) {
	t.Parallel()
	idx, u, _ := newDBs(t)
	splitFixture(t, idx)
	putCover(t, u, "old-container", "ser-split", "manga", "c.zip", 12, 35)
	putCover(t, u, "book-plain", "ser-plain", "manga", "plain/v1.zip", 3, 7)

	got, err := idx.CoverOrphans(t.Context())
	if err != nil {
		t.Fatalf("CoverOrphans: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %+v, want only the container's cover", got)
	}
	o := got[0]
	want := index.CoverOrphan{
		BookID: "old-container", SeriesID: "ser-split", RootName: "manga", BookPath: "c.zip",
		Page: 12, PageCount: 35, X: 0.1, Y: 0.2, W: 0.3, H: 0.4, UpdatedAt: o.UpdatedAt,
	}
	if o != want || o.UpdatedAt == 0 {
		t.Errorf("orphan %+v, want %+v with its updated_at", o, want)
	}
}

func TestMisfiledCovers_findsARowUnderTheWrongSeries(t *testing.T) {
	t.Parallel()
	idx, u, _ := newDBs(t)
	splitFixture(t, idx)
	putCover(t, u, "vol-b", "ser-somewhere-else", "manga", "c.zip", 2, 20)
	putCover(t, u, "book-plain", "ser-plain", "manga", "plain/v1.zip", 3, 7)
	putCover(t, u, "old-container", "ser-gone", "manga", "c.zip", 2, 35) // an orphan is not misfiled

	got, err := idx.MisfiledCovers(t.Context())
	if err != nil {
		t.Fatalf("MisfiledCovers: %v", err)
	}
	if len(got) != 1 || got[0].BookID != "vol-b" || got[0].ActualSeria != "ser-split" ||
		got[0].StoredSeria != "ser-somewhere-else" {
		t.Errorf("got %+v, want vol-b stored under ser-somewhere-else, actually ser-split", got)
	}
}

func TestVanishedCovers_onlyTheRootsAskedAbout(t *testing.T) {
	t.Parallel()
	idx, u, _ := newDBs(t)
	splitFixture(t, idx)
	putCover(t, u, "gone-manga", "ser-split", "manga", "vanished.zip", 4, 100)
	putCover(t, u, "gone-books", "ser-x", "books", "elsewhere.zip", 2, 50)
	putCover(t, u, "book-plain", "ser-plain", "manga", "plain/v1.zip", 3, 7)

	if got, err := idx.VanishedCovers(t.Context(), nil); err != nil || len(got) != 0 {
		t.Fatalf("VanishedCovers(nil) = %+v, %v; an empty root list must never mean 'all of them'", got, err)
	}
	got, err := idx.VanishedCovers(t.Context(), []string{"manga"})
	if err != nil {
		t.Fatalf("VanishedCovers: %v", err)
	}
	if len(got) != 1 || got[0].BookID != "gone-manga" || got[0].BookPath != "vanished.zip" ||
		got[0].Page != 4 || got[0].RootName != "manga" {
		t.Errorf("got %+v, want only the manga row with its path and page", got)
	}
}

func TestVanishedSeriesPrefs_onlyGoneSeriesOfWalkedRoots(t *testing.T) {
	t.Parallel()
	idx, u, _ := newDBs(t)
	splitFixture(t, idx)
	putSeriesDir(t, u, "ser-plain", "manga", "plain")      // still indexed
	putSeriesDir(t, u, "ser-gone", "manga", "gone")        // gone, walked
	putSeriesDir(t, u, "ser-gone-books", "books", "gone2") // gone, not walked

	if got, err := idx.VanishedSeriesPrefs(t.Context(), nil); err != nil || len(got) != 0 {
		t.Fatalf("VanishedSeriesPrefs(nil) = %+v, %v; want nothing", got, err)
	}
	got, err := idx.VanishedSeriesPrefs(t.Context(), []string{"manga"})
	if err != nil {
		t.Fatalf("VanishedSeriesPrefs: %v", err)
	}
	if len(got) != 1 || got[0].SeriesID != "ser-gone" || got[0].SeriesPath != "gone" || got[0].RootName != "manga" {
		t.Errorf("got %+v, want only ser-gone with its path", got)
	}
}

func TestSeriesAt_answersOnlyForIndexedSeries(t *testing.T) {
	t.Parallel()
	idx, _, _ := newDBs(t)
	splitFixture(t, idx)

	got, err := idx.SeriesAt(t.Context(), []string{"ser-plain", "ser-gone", "ser-plain"})
	if err != nil {
		t.Fatalf("SeriesAt: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %+v, want one entry", got)
	}
	if loc := got["ser-plain"]; loc != (index.Location{RootName: "manga", RelPath: "plain"}) {
		t.Errorf("ser-plain at %+v, want manga/plain", loc)
	}
	if empty, err := idx.SeriesAt(t.Context(), nil); err != nil || len(empty) != 0 {
		t.Errorf("SeriesAt(nil) = %+v, %v", empty, err)
	}
}
