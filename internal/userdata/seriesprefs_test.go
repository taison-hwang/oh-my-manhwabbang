package userdata_test

import (
	"errors"
	"testing"
	"time"

	"shelf/internal/userdata"
)

func deref(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

// ---------------------------------------------------------- series prefs --

func TestSeriesPrefs_putGetClear(t *testing.T) {
	t.Parallel()
	db, clk, _ := newDB(t)
	ctx := t.Context()

	// No row is the normal case: zero value, nil direction, no error.
	got, err := db.GetSeriesPrefs(ctx, "s1")
	if err != nil || got.ReadingDir != nil || got.UpdatedAt != 0 {
		t.Fatalf("GetSeriesPrefs on an empty table = %+v (%v), want zero", got, err)
	}

	put, err := db.PutSeriesPrefs(ctx, seriesPut("s1", ptr("rtl")))
	if err != nil {
		t.Fatalf("PutSeriesPrefs: %v", err)
	}
	want := userdata.SeriesPrefs{SeriesID: "s1", RootName: "manga", SeriesPath: "s1",
		ReadingDir: ptr("rtl"), UpdatedAt: clk.Now().Unix()}
	if put.SeriesID != want.SeriesID || put.RootName != want.RootName ||
		put.SeriesPath != want.SeriesPath || deref(put.ReadingDir) != "rtl" ||
		put.UpdatedAt != want.UpdatedAt {
		t.Errorf("PutSeriesPrefs = %+v, want %+v", put, want)
	}
	got, err = db.GetSeriesPrefs(ctx, "s1")
	if err != nil || deref(got.ReadingDir) != "rtl" || got.SeriesPath != "s1" ||
		got.UpdatedAt != want.UpdatedAt {
		t.Errorf("GetSeriesPrefs = %+v (%v), want rtl", got, err)
	}

	clk.Advance(time.Minute)
	if put, err = db.PutSeriesPrefs(ctx, seriesPut("s1", ptr("ltr"))); err != nil ||
		deref(put.ReadingDir) != "ltr" || put.UpdatedAt != clk.Now().Unix() {
		t.Errorf("second PutSeriesPrefs = %+v (%v)", put, err)
	}

	// null clears the override and leaves no row (PutPrefs' "no residue").
	put, err = db.PutSeriesPrefs(ctx, seriesPut("s1", nil))
	if err != nil || put.ReadingDir != nil {
		t.Fatalf("PutSeriesPrefs(nil) = %+v (%v)", put, err)
	}
	all, err := db.ListSeriesPrefs(ctx)
	if err != nil || len(all) != 0 {
		t.Errorf("rows after clearing = %d (%v), want 0", len(all), err)
	}
	// Clearing again is not an error.
	if _, err := db.PutSeriesPrefs(ctx, seriesPut("s1", nil)); err != nil {
		t.Errorf("clearing an absent row = %v", err)
	}
}

func TestPutSeriesPrefs_rejectsValuesOutsideTheFrozenEnumAndEmptyIDs(t *testing.T) {
	t.Parallel()
	db, _, _ := newDB(t)
	ctx := t.Context()
	for name, p := range map[string]userdata.SeriesPrefsPut{
		"unknown direction": seriesPut("s1", ptr("ttb")),
		"empty string":      seriesPut("s1", ptr("")),
		"empty series id":   seriesPut("", ptr("rtl")),
	} {
		if _, err := db.PutSeriesPrefs(ctx, p); !errors.Is(err, userdata.ErrInvalidArgument) {
			t.Errorf("%s: PutSeriesPrefs = %v, want ErrInvalidArgument", name, err)
		}
	}
	if all, _ := db.ListSeriesPrefs(ctx); len(all) != 0 {
		t.Errorf("refused writes left %d rows", len(all))
	}
	if _, err := db.GetSeriesPrefs(ctx, ""); !errors.Is(err, userdata.ErrInvalidArgument) {
		t.Errorf("GetSeriesPrefs(\"\") = %v, want ErrInvalidArgument", err)
	}
}

func TestGetSeriesPrefsMany_andListOrder(t *testing.T) {
	t.Parallel()
	db, _, _ := newDB(t)
	ctx := t.Context()
	for _, id := range []string{"s3", "s1", "s5"} {
		if _, err := db.PutSeriesPrefs(ctx, seriesPut(id, ptr("rtl"))); err != nil {
			t.Fatalf("PutSeriesPrefs %s: %v", id, err)
		}
	}
	got, err := db.GetSeriesPrefsMany(ctx, append(chunkedIDs("s1", "s3", "s5"), "s2"))
	if err != nil {
		t.Fatalf("GetSeriesPrefsMany: %v", err)
	}
	if len(got) != 3 || deref(got["s1"].ReadingDir) != "rtl" || got["s3"].SeriesID != "s3" ||
		got["s5"].SeriesID != "s5" {
		t.Errorf("GetSeriesPrefsMany = %+v, want s1, s3 and s5 from the first, a middle and the last batch", got)
	}
	if empty, err := db.GetSeriesPrefsMany(ctx, nil); err != nil || len(empty) != 0 {
		t.Errorf("GetSeriesPrefsMany(no ids) = (%+v, %v), want an empty result", empty, err)
	}
	all, err := db.ListSeriesPrefs(ctx)
	if err != nil || len(all) != 3 || all[0].SeriesID != "s1" || all[1].SeriesID != "s3" || all[2].SeriesID != "s5" {
		t.Errorf("ListSeriesPrefs = %+v (%v), want [s1 s3 s5]", all, err)
	}
}

// ---------------------------------------------------------------- ladder --

// E-69 §2.3: book → series → global, field by field. A book row whose
// reading_dir is NULL falls through to the series; a series choice survives a
// change of the global default.
func TestEffectiveReadingDir_isFieldByField(t *testing.T) {
	t.Parallel()
	cases := []struct {
		book, series *string
		global, want string
	}{
		{nil, nil, "ltr", "ltr"},
		{nil, nil, "rtl", "rtl"},
		{nil, ptr("rtl"), "ltr", "rtl"},
		{nil, ptr("ltr"), "rtl", "ltr"},
		{ptr("ltr"), ptr("rtl"), "rtl", "ltr"},
		{ptr("rtl"), nil, "ltr", "rtl"},
	}
	for _, tc := range cases {
		if got := userdata.EffectiveReadingDir(tc.book, tc.series, tc.global); got != tc.want {
			t.Errorf("EffectiveReadingDir(%s, %s, %s) = %s, want %s",
				deref(tc.book), deref(tc.series), tc.global, got, tc.want)
		}
	}
}

// The DB-side half: a book row that overrides only display mode has a NULL
// reading_dir and must yield the series direction, not "no override" — the
// seed loss E-69 §2.3 names.
func TestReadingDirLadder_readsBookThenSeries(t *testing.T) {
	t.Parallel()
	db, _, _ := newDB(t)
	ctx := t.Context()

	if _, err := db.PutSeriesPrefs(ctx, seriesPut("s1", ptr("rtl"))); err != nil {
		t.Fatalf("PutSeriesPrefs: %v", err)
	}
	if _, err := db.PutPrefs(ctx, "b1", userdata.PrefsPatch{
		DisplayMode: userdata.SetPatch("spread")}); err != nil {
		t.Fatalf("PutPrefs b1: %v", err)
	}
	if _, err := db.PutPrefs(ctx, "b2", userdata.PrefsPatch{
		ReadingDir: userdata.SetPatch("ltr")}); err != nil {
		t.Fatalf("PutPrefs b2: %v", err)
	}

	cases := []struct {
		book, series string
		wantBook     string
		wantSeries   string
		effective    string
	}{
		{"b1", "s1", "<nil>", "rtl", "rtl"}, // display-only book row falls through
		{"b2", "s1", "ltr", "rtl", "ltr"},   // book choice wins
		{"b3", "s1", "<nil>", "rtl", "rtl"}, // no book row
		{"b3", "s9", "<nil>", "<nil>", "ltr"},
	}
	for _, tc := range cases {
		book, series, err := db.ReadingDirLadder(ctx, tc.book, tc.series)
		if err != nil {
			t.Fatalf("ReadingDirLadder(%s, %s): %v", tc.book, tc.series, err)
		}
		if deref(book) != tc.wantBook || deref(series) != tc.wantSeries {
			t.Errorf("ReadingDirLadder(%s, %s) = (%s, %s), want (%s, %s)",
				tc.book, tc.series, deref(book), deref(series), tc.wantBook, tc.wantSeries)
		}
		if got := userdata.EffectiveReadingDir(book, series, "ltr"); got != tc.effective {
			t.Errorf("effective for %s/%s = %s, want %s", tc.book, tc.series, got, tc.effective)
		}
	}

	// is_override's meaning is unchanged: b1 still has a row.
	p, err := db.GetPrefs(ctx, "b1")
	if err != nil || p.ReadingDir != nil || deref(p.DisplayMode) != "spread" || p.UpdatedAt == 0 {
		t.Errorf("GetPrefs(b1) = %+v (%v)", p, err)
	}
}

func TestGetPrefsMany_keysByBook(t *testing.T) {
	t.Parallel()
	db, _, _ := newDB(t)
	ctx := t.Context()
	for _, id := range []string{"b1", "b3", "b5"} {
		if _, err := db.PutPrefs(ctx, id, userdata.PrefsPatch{
			ReadingDir: userdata.SetPatch("rtl")}); err != nil {
			t.Fatalf("PutPrefs %s: %v", id, err)
		}
	}
	got, err := db.GetPrefsMany(ctx, append(chunkedIDs("b1", "b3", "b5"), "b2"))
	if err != nil {
		t.Fatalf("GetPrefsMany: %v", err)
	}
	if len(got) != 3 || deref(got["b1"].ReadingDir) != "rtl" || got["b3"].BookID != "b3" ||
		got["b5"].BookID != "b5" {
		t.Errorf("GetPrefsMany = %+v, want b1, b3 and b5 from the first, a middle and the last batch", got)
	}
	if empty, err := db.GetPrefsMany(ctx, nil); err != nil || len(empty) != 0 {
		t.Errorf("GetPrefsMany(no ids) = (%+v, %v), want an empty result", empty, err)
	}
}
