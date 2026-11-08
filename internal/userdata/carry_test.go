package userdata_test

import (
	"errors"
	"testing"
	"time"

	"shelf/internal/userdata"
)

// The writes of the post-scan cleanup for the E-69 tables (§2.11, §2.12). The
// planners in internal/repair decide what moves; these tests pin how a move
// lands: newer updated_at wins at the destination (equal keeps the local row),
// the old row is retired either way, and the whole carry is one transaction.

func cover(t *testing.T, db *userdata.DB, bookID string, page, count int) userdata.Cover {
	t.Helper()
	c, err := db.PutCover(t.Context(), userdata.CoverPut{
		BookID: bookID, SeriesID: "s-old", RootName: "manga", BookPath: bookID + ".zip",
		PageCount: count, Region: userdata.CoverRegion{Page: page, X: 0.1, Y: 0.2, W: 0.3, H: 0.4},
	})
	if err != nil {
		t.Fatalf("PutCover %s: %v", bookID, err)
	}
	return c
}

func prefs(t *testing.T, db *userdata.DB, bookID, dir string) userdata.Prefs {
	t.Helper()
	p, err := db.PutPrefs(t.Context(), bookID, userdata.PrefsPatch{
		ReadingDir: userdata.SetPatch(dir), DisplayMode: userdata.SetPatch("spread"),
	})
	if err != nil {
		t.Fatalf("PutPrefs %s: %v", bookID, err)
	}
	return p
}

func hasPrefs(t *testing.T, db *userdata.DB, id string) (userdata.Prefs, bool) {
	t.Helper()
	m, err := db.GetPrefsMany(t.Context(), []string{id})
	if err != nil {
		t.Fatalf("GetPrefsMany: %v", err)
	}
	p, ok := m[id]
	return p, ok
}

func hasCover(t *testing.T, db *userdata.DB, id string) (userdata.Cover, bool) {
	t.Helper()
	c, ok, err := db.GetCover(t.Context(), id)
	if err != nil {
		t.Fatalf("GetCover: %v", err)
	}
	return c, ok
}

func seriesRow(t *testing.T, db *userdata.DB, id string) (userdata.SeriesPrefs, bool) {
	t.Helper()
	m, err := db.GetSeriesPrefsMany(t.Context(), []string{id})
	if err != nil {
		t.Fatalf("GetSeriesPrefsMany: %v", err)
	}
	p, ok := m[id]
	return p, ok
}

func seriesPrefs(t *testing.T, db *userdata.DB, id, dir string) userdata.SeriesPrefs {
	t.Helper()
	p, err := db.PutSeriesPrefs(t.Context(), userdata.SeriesPrefsPut{
		SeriesID: id, RootName: "manga", SeriesPath: "path/" + id, ReadingDir: &dir,
	})
	if err != nil {
		t.Fatalf("PutSeriesPrefs %s: %v", id, err)
	}
	return p
}

func movedCover(c userdata.Cover, newID string) userdata.Cover {
	c.BookID, c.SeriesID, c.RootName, c.BookPath = newID, "s-new", "books", "moved/"+newID+".zip"
	return c
}

// ------------------------------------------------------------------ covers --

func TestApplyCarry_coverMovesAndTheOldRowIsRetired(t *testing.T) {
	t.Parallel()
	db, _, _ := newDB(t)
	old := cover(t, db, "old", 3, 9)

	res, err := db.ApplyCarry(t.Context(), userdata.Carry{
		Covers: []userdata.CoverMove{{OldBookID: "old", Row: movedCover(old, "new")}},
	})
	if err != nil {
		t.Fatalf("ApplyCarry: %v", err)
	}
	if res.Covers != (userdata.RepairResult{Retired: 1, Written: 1}) {
		t.Errorf("result %+v, want 1 retired, 1 written", res.Covers)
	}
	got, ok := hasCover(t, db, "new")
	if !ok || got != movedCover(old, "new") {
		t.Errorf("destination (%+v, %v), want %+v", got, ok, movedCover(old, "new"))
	}
	if _, ok := hasCover(t, db, "old"); ok {
		t.Error("the old cover row survived the move")
	}
}

// Newer wins, and equal keeps the local row (the progress rule, E-69 §2.11(a)).
// The old row is retired in both cases: it names an id that is no book.
func TestApplyCarry_coverCollisionNewerWins(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name     string
		destLead time.Duration // how much later than the old row the destination was written
		wantPage int
		want     userdata.RepairResult
	}{
		{"destination newer", time.Second, 7, userdata.RepairResult{Retired: 1, Kept: 1}},
		{"same second", 0, 7, userdata.RepairResult{Retired: 1, Kept: 1}},
		{"destination older", -time.Second, 3, userdata.RepairResult{Retired: 1, Written: 1}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			db, clk, _ := newDB(t)
			if c.destLead < 0 {
				cover(t, db, "new", 7, 9)
				clk.Advance(-c.destLead)
			}
			old := cover(t, db, "old", 3, 9)
			if c.destLead >= 0 {
				clk.Advance(c.destLead)
				cover(t, db, "new", 7, 9)
			}
			res, err := db.ApplyCarry(t.Context(), userdata.Carry{
				Covers: []userdata.CoverMove{{OldBookID: "old", Row: movedCover(old, "new")}},
			})
			if err != nil {
				t.Fatalf("ApplyCarry: %v", err)
			}
			if res.Covers != c.want {
				t.Errorf("result %+v, want %+v", res.Covers, c.want)
			}
			if got, _ := hasCover(t, db, "new"); got.Page != c.wantPage {
				t.Errorf("destination at page %d, want %d", got.Page, c.wantPage)
			}
			if _, ok := hasCover(t, db, "old"); ok {
				t.Error("the old cover row survived")
			}
		})
	}
}

// ------------------------------------------------------------------- prefs --

func TestApplyCarry_prefsMoveToTheNewIDAndTheOldRowIsRetired(t *testing.T) {
	t.Parallel()
	db, _, _ := newDB(t)
	old := prefs(t, db, "old", "rtl")

	res, err := db.ApplyCarry(t.Context(), userdata.Carry{
		Prefs: []userdata.PrefsMove{{OldBookID: "old", To: []string{"new"}}},
	})
	if err != nil {
		t.Fatalf("ApplyCarry: %v", err)
	}
	if res.Prefs != (userdata.RepairResult{Retired: 1, Written: 1}) {
		t.Errorf("result %+v, want 1 retired, 1 written", res.Prefs)
	}
	got, ok := hasPrefs(t, db, "new")
	if !ok || *got.ReadingDir != "rtl" || *got.DisplayMode != "spread" || got.FitMode != nil ||
		got.UpdatedAt != old.UpdatedAt {
		t.Errorf("destination (%+v, %v), want rtl/spread/nil at %d", got, ok, old.UpdatedAt)
	}
	if _, ok := hasPrefs(t, db, "old"); ok {
		t.Error("the old prefs row survived the move")
	}
}

func TestApplyCarry_prefsCollisionNewerWins(t *testing.T) {
	t.Parallel()
	t.Run("destination newer is kept", func(t *testing.T) {
		db, clk, _ := newDB(t)
		prefs(t, db, "old", "rtl")
		clk.Advance(time.Second)
		prefs(t, db, "new", "ltr")
		res, err := db.ApplyCarry(t.Context(), userdata.Carry{
			Prefs: []userdata.PrefsMove{{OldBookID: "old", To: []string{"new"}}},
		})
		if err != nil {
			t.Fatalf("ApplyCarry: %v", err)
		}
		if got, _ := hasPrefs(t, db, "new"); *got.ReadingDir != "ltr" {
			t.Errorf("destination %q, want the newer local ltr", *got.ReadingDir)
		}
		if res.Prefs != (userdata.RepairResult{Retired: 1, Kept: 1}) {
			t.Errorf("result %+v", res.Prefs)
		}
	})
	t.Run("destination older is replaced", func(t *testing.T) {
		db, clk, _ := newDB(t)
		prefs(t, db, "new", "ltr")
		clk.Advance(time.Second)
		prefs(t, db, "old", "rtl")
		if _, err := db.ApplyCarry(t.Context(), userdata.Carry{
			Prefs: []userdata.PrefsMove{{OldBookID: "old", To: []string{"new"}}},
		}); err != nil {
			t.Fatalf("ApplyCarry: %v", err)
		}
		if got, _ := hasPrefs(t, db, "new"); *got.ReadingDir != "rtl" {
			t.Errorf("destination %q, want the newer carried rtl", *got.ReadingDir)
		}
	})
}

// The split rule (E-69 §2.12): copy to every volume that has no row of its own,
// whatever the ages; a volume with a row keeps it; and the container's own row
// stays — "복사한다. 원본은 남긴다" (the split reattach also runs at start-up,
// where nothing may be deleted, E-50 §2).
func TestApplyCarry_prefsOnlyIfAbsentLeavesEveryExistingRow(t *testing.T) {
	t.Parallel()
	db, clk, _ := newDB(t)
	prefs(t, db, "v2", "ltr") // older than the container's row, kept anyway
	clk.Advance(time.Second)
	prefs(t, db, "container", "rtl")

	res, err := db.ApplyCarry(t.Context(), userdata.Carry{
		Prefs: []userdata.PrefsMove{{OldBookID: "container", To: []string{"v1", "v2", "v3"}, OnlyIfAbsent: true}},
	})
	if err != nil {
		t.Fatalf("ApplyCarry: %v", err)
	}
	if res.Prefs != (userdata.RepairResult{Written: 2, Kept: 1}) {
		t.Errorf("result %+v, want 0 retired, 2 written, 1 kept", res.Prefs)
	}
	for id, want := range map[string]string{"v1": "rtl", "v2": "ltr", "v3": "rtl", "container": "rtl"} {
		if got, ok := hasPrefs(t, db, id); !ok || *got.ReadingDir != want {
			t.Errorf("%s = (%+v, %v), want %s", id, got, ok, want)
		}
	}
}

// U1 codex cycle 1, finding 4: every volume already has its own row, so the
// split copies nothing — and must not delete the container's row either. A
// "copy" that deleted the source when there was nowhere to copy to would be a
// purge at start-up.
func TestApplyCarry_splitPrefsWithNowhereToCopyDeletesNothing(t *testing.T) {
	t.Parallel()
	db, clk, _ := newDB(t)
	prefs(t, db, "v1", "ltr")
	prefs(t, db, "v2", "ltr")
	clk.Advance(time.Second)
	prefs(t, db, "container", "rtl")

	res, err := db.ApplyCarry(t.Context(), userdata.Carry{
		Prefs: []userdata.PrefsMove{{OldBookID: "container", To: []string{"v1", "v2"}, OnlyIfAbsent: true}},
	})
	if err != nil {
		t.Fatalf("ApplyCarry: %v", err)
	}
	if res.Prefs != (userdata.RepairResult{Kept: 2}) {
		t.Errorf("result %+v, want 2 kept and nothing retired", res.Prefs)
	}
	if got, ok := hasPrefs(t, db, "container"); !ok || *got.ReadingDir != "rtl" {
		t.Errorf("container = (%+v, %v), want its rtl row kept", got, ok)
	}
}

// A move planned for a book that has no prefs is a no-op, not an error: the
// planner does not read book_prefs, so it plans one for every proven pairing.
func TestApplyCarry_prefsMoveWithoutARowDoesNothing(t *testing.T) {
	t.Parallel()
	db, _, _ := newDB(t)
	prefs(t, db, "new", "ltr")
	res, err := db.ApplyCarry(t.Context(), userdata.Carry{
		Prefs: []userdata.PrefsMove{{OldBookID: "old", To: []string{"new"}}},
	})
	if err != nil {
		t.Fatalf("ApplyCarry: %v", err)
	}
	if res.Prefs != (userdata.RepairResult{}) {
		t.Errorf("result %+v, want nothing", res.Prefs)
	}
	if got, ok := hasPrefs(t, db, "new"); !ok || *got.ReadingDir != "ltr" {
		t.Errorf("destination (%+v, %v) changed", got, ok)
	}
}

// ------------------------------------------------------------ series prefs --

func TestApplyCarry_seriesPrefsMoveUnderTheNewIdentity(t *testing.T) {
	t.Parallel()
	db, _, _ := newDB(t)
	old := seriesPrefs(t, db, "S", "rtl")

	res, err := db.ApplyCarry(t.Context(), userdata.Carry{Series: []userdata.SeriesPrefsMove{{
		OldSeriesID: "S", NewSeriesID: "T", RootName: "books", SeriesPath: "새 이름",
	}}})
	if err != nil {
		t.Fatalf("ApplyCarry: %v", err)
	}
	if res.Series != (userdata.RepairResult{Retired: 1, Written: 1}) {
		t.Errorf("result %+v", res.Series)
	}
	got, ok := seriesRow(t, db, "T")
	if !ok || *got.ReadingDir != "rtl" || got.RootName != "books" || got.SeriesPath != "새 이름" ||
		got.UpdatedAt != old.UpdatedAt {
		t.Errorf("destination (%+v, %v), want rtl at books/새 이름 with updated_at %d", got, ok, old.UpdatedAt)
	}
	if _, ok := seriesRow(t, db, "S"); ok {
		t.Error("the old series row survived")
	}
}

func TestApplyCarry_seriesPrefsCollisionNewerWins(t *testing.T) {
	t.Parallel()
	db, clk, _ := newDB(t)
	seriesPrefs(t, db, "S", "rtl")
	clk.Advance(time.Second)
	seriesPrefs(t, db, "T", "ltr")

	res, err := db.ApplyCarry(t.Context(), userdata.Carry{Series: []userdata.SeriesPrefsMove{{
		OldSeriesID: "S", NewSeriesID: "T", RootName: "manga", SeriesPath: "t",
	}}})
	if err != nil {
		t.Fatalf("ApplyCarry: %v", err)
	}
	if got, _ := seriesRow(t, db, "T"); *got.ReadingDir != "ltr" {
		t.Errorf("destination %q, want the newer local ltr", *got.ReadingDir)
	}
	if res.Series != (userdata.RepairResult{Retired: 1, Kept: 1}) {
		t.Errorf("result %+v", res.Series)
	}
	if _, ok := seriesRow(t, db, "S"); ok {
		t.Error("the old series row survived")
	}
}

// ------------------------------------------------------------- one carry --

// One transaction for all four tables: an invalid move anywhere leaves every
// table as it was. A half-carried book is worse than an uncarried one.
func TestApplyCarry_isOneTransaction(t *testing.T) {
	t.Parallel()
	db, _, _ := newDB(t)
	old := cover(t, db, "old", 3, 9)
	prefs(t, db, "old", "rtl")
	container(t, db, "old", 2, 9)

	_, err := db.ApplyCarry(t.Context(), userdata.Carry{
		Progress: []userdata.SplitMove{{OldBookID: "old", Rows: []userdata.ExportItem{volumeRow("new", 2, 9, 500)}}},
		Covers:   []userdata.CoverMove{{OldBookID: "old", Row: movedCover(old, "new")}},
		Prefs:    []userdata.PrefsMove{{OldBookID: "old", To: []string{"new"}}},
		Series:   []userdata.SeriesPrefsMove{{OldSeriesID: "", NewSeriesID: "T"}},
	})
	if !errors.Is(err, userdata.ErrInvalidArgument) {
		t.Fatalf("err = %v, want ErrInvalidArgument", err)
	}
	if _, ok := hasCover(t, db, "old"); !ok {
		t.Error("the cover moved although the carry failed")
	}
	if _, ok := hasPrefs(t, db, "old"); !ok {
		t.Error("the prefs moved although the carry failed")
	}
	if _, err := db.GetProgress(t.Context(), "old"); err != nil {
		t.Error("the progress moved although the carry failed")
	}
}

func TestApplyCarry_emptyIsANoOp(t *testing.T) {
	t.Parallel()
	db, _, _ := newDB(t)
	res, err := db.ApplyCarry(t.Context(), userdata.Carry{})
	if err != nil || res != (userdata.CarryResult{}) {
		t.Errorf("ApplyCarry(empty) = %+v, %v", res, err)
	}
}

// ------------------------------------------------------------------- purge --

func TestPurge_removesEveryTableForTheGivenIDs(t *testing.T) {
	t.Parallel()
	db, _, _ := newDB(t)
	container(t, db, "gone", 2, 9)
	prefs(t, db, "gone", "rtl")
	cover(t, db, "gone", 1, 9)
	prefs(t, db, "gone-prefs-only", "rtl")
	cover(t, db, "gone-cover-only", 1, 9)
	seriesPrefs(t, db, "S-gone", "rtl")
	prefs(t, db, "kept", "ltr")
	cover(t, db, "kept", 1, 9)
	seriesPrefs(t, db, "S-kept", "rtl")

	res, err := db.Purge(t.Context(), userdata.PurgeSet{
		Books:  []string{"gone", "gone-prefs-only", "gone-cover-only"},
		Series: []string{"S-gone"},
	})
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if res != (userdata.PurgeResult{Progress: 1, Prefs: 2, Covers: 2, Series: 1}) {
		t.Errorf("result %+v, want 1 progress, 2 prefs, 2 covers, 1 series", res)
	}
	for _, id := range []string{"gone", "gone-prefs-only"} {
		if _, ok := hasPrefs(t, db, id); ok {
			t.Errorf("prefs of %s survived", id)
		}
	}
	for _, id := range []string{"gone", "gone-cover-only"} {
		if _, ok := hasCover(t, db, id); ok {
			t.Errorf("cover of %s survived", id)
		}
	}
	if _, ok := seriesRow(t, db, "S-gone"); ok {
		t.Error("series prefs of S-gone survived")
	}
	if _, ok := hasPrefs(t, db, "kept"); !ok {
		t.Error("purge took prefs it was not given")
	}
	if _, ok := hasCover(t, db, "kept"); !ok {
		t.Error("purge took a cover it was not given")
	}
	if _, ok := seriesRow(t, db, "S-kept"); !ok {
		t.Error("purge took series prefs it was not given")
	}
}

func TestPurge_refusesAnEmptyID(t *testing.T) {
	t.Parallel()
	db, _, _ := newDB(t)
	prefs(t, db, "kept", "ltr")
	for _, set := range []userdata.PurgeSet{
		{Books: []string{"kept", ""}},
		{Series: []string{""}},
	} {
		if _, err := db.Purge(t.Context(), set); !errors.Is(err, userdata.ErrInvalidArgument) {
			t.Errorf("Purge(%+v) err = %v, want ErrInvalidArgument", set, err)
		}
	}
	if _, ok := hasPrefs(t, db, "kept"); !ok {
		t.Error("a refused purge deleted a row")
	}
}

// ------------------------------------------------------------------ refile --

func TestRefileCovers_movesOnlyTheSeriesColumn(t *testing.T) {
	t.Parallel()
	db, clk, _ := newDB(t)
	before := cover(t, db, "b", 3, 9)
	clk.Advance(time.Hour)

	n, err := db.RefileCovers(t.Context(), []userdata.Refile{
		{BookID: "b", SeriesID: "s-right"},
		{BookID: "absent", SeriesID: "s-right"},
	})
	if err != nil {
		t.Fatalf("RefileCovers: %v", err)
	}
	if n != 1 {
		t.Errorf("refiled %d rows, want 1", n)
	}
	got, _ := hasCover(t, db, "b")
	want := before
	want.SeriesID = "s-right"
	if got != want {
		t.Errorf("row %+v, want %+v (series only; updated_at must not move)", got, want)
	}
}
