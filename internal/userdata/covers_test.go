package userdata_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"shelf/internal/userdata"
)

// ------------------------------------------------------------ schema v3 --

// E-69 §2.5: two tables appended at schema version 3, no foreign keys into
// index.db, and the series_id index the post-scan refile needs.
func TestOpen_schemaV3_addsBookCoversAndSeriesPrefs(t *testing.T) {
	t.Parallel()
	db, _, dir := newDB(t)
	if v, err := db.SchemaVersion(t.Context()); err != nil || v != 3 {
		t.Fatalf("schema version = %d (%v), want 3 (E-69 §2.5)", v, err)
	}
	raw := openRaw(t, filepath.Join(dir, "user.db"))
	want := map[string][]string{
		"book_covers": {"book_id", "series_id", "root_name", "book_path", "page_no", "page_count",
			"x", "y", "w", "h", "updated_at"},
		"series_prefs": {"series_id", "root_name", "series_path", "reading_dir", "updated_at"},
	}
	for table, cols := range want {
		if got := tableColumns(t, raw, table); !equalStrings(got, cols) {
			t.Errorf("table %s columns = %v, want %v", table, got, cols)
		}
		var n int
		if err := raw.QueryRow(
			`SELECT count(*) FROM pragma_foreign_key_list(?)`, table).Scan(&n); err != nil {
			t.Fatalf("reading foreign keys of %s: %v", table, err)
		}
		if n != 0 {
			t.Errorf("table %s declares %d foreign keys, want 0 (E-69 §2.5)", table, n)
		}
	}
	var n int
	if err := raw.QueryRow(`SELECT count(*) FROM sqlite_master
		WHERE type='index' AND tbl_name='book_covers' AND sql LIKE '%series_id%'`).Scan(&n); err != nil {
		t.Fatalf("looking up the book_covers series index: %v", err)
	}
	if n != 1 {
		t.Errorf("book_covers has %d indexes on series_id, want 1", n)
	}
}

// The upgrade an installed v2 copy performs: append the two tables, touch
// nothing authored. A rung that recreated an existing table would pass every
// fresh-file test while destroying history; this is the test that fails.
func TestOpen_upgradeFromV2_addsTheV3TablesAndKeepsEveryAuthoredRow(t *testing.T) {
	t.Parallel()
	db, clk, dir := newDB(t)
	ctx := t.Context()

	if _, err := db.PutProgress(ctx, update("b1", "s1", 7, 20)); err != nil {
		t.Fatalf("PutProgress: %v", err)
	}
	if _, err := db.PutPrefs(ctx, "b1", userdata.PrefsPatch{
		ReadingDir: userdata.SetPatch("rtl")}); err != nil {
		t.Fatalf("PutPrefs: %v", err)
	}
	if err := db.MarkSeriesSeen(ctx, []userdata.SeriesSeen{seenRow("s1", 1_600_000_000)}); err != nil {
		t.Fatalf("MarkSeriesSeen: %v", err)
	}
	before, err := db.GetProgress(ctx, "b1")
	if err != nil {
		t.Fatalf("GetProgress: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	raw := openRaw(t, filepath.Join(dir, "user.db"))
	for _, stmt := range []string{
		`DROP TABLE book_covers`,
		`DROP TABLE series_prefs`,
		`UPDATE meta SET value = '2' WHERE key = 'schema_version'`,
	} {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("rewinding to v2 (%s): %v", stmt, err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("closing the raw handle: %v", err)
	}

	up := openIn(t, dir, clk)
	if v, err := up.SchemaVersion(ctx); err != nil || v != 3 {
		t.Fatalf("schema version after the upgrade = %d (%v), want 3", v, err)
	}
	if after, err := up.GetProgress(ctx, "b1"); err != nil || after != before {
		t.Errorf("progress after the upgrade = %+v (%v), want %+v", after, err, before)
	}
	if p, err := up.GetPrefs(ctx, "b1"); err != nil || p.ReadingDir == nil || *p.ReadingDir != "rtl" {
		t.Errorf("prefs after the upgrade = %+v (%v), want reading_dir rtl", p, err)
	}
	if at, ok := firstSeen(t, up, "s1"); !ok || at != 1_600_000_000 {
		t.Errorf("series_seen after the upgrade = %d (present %v)", at, ok)
	}
	// The new tables are usable.
	if _, err := up.PutCover(ctx, coverPut("b1", 3, 10, 0, 0, 0.5, 0.75)); err != nil {
		t.Errorf("PutCover after the upgrade: %v", err)
	}
	if _, err := up.PutSeriesPrefs(ctx, seriesPut("s1", ptr("ltr"))); err != nil {
		t.Errorf("PutSeriesPrefs after the upgrade: %v", err)
	}
}

// ------------------------------------------------------------- fixtures --

func ptr(s string) *string { return &s }

func coverPut(bookID string, page, count int, x, y, w, h float64) userdata.CoverPut {
	return userdata.CoverPut{
		BookID: bookID, SeriesID: "s1", RootName: "manga", BookPath: "s1/" + bookID + ".zip",
		PageCount: count,
		Region:    userdata.CoverRegion{Page: page, X: x, Y: y, W: w, H: h},
	}
}

func seriesPut(seriesID string, dir *string) userdata.SeriesPrefsPut {
	return userdata.SeriesPrefsPut{
		SeriesID: seriesID, RootName: "manga", SeriesPath: seriesID, ReadingDir: dir,
	}
}

// ------------------------------------------------------------ validation --

// E-69 §2.4, verbatim: 0 ≤ x, 0 ≤ y, w ≥ 0.05, h ≥ 0.05, x + w ≤ 1,
// y + h ≤ 1, page ∈ [1, page_count]. Each boundary is pinned on both sides.
func TestValidateCoverRegion_boundariesOfE69(t *testing.T) {
	t.Parallel()
	nan, inf := math.NaN(), math.Inf(1)
	ok := func(p int, x, y, w, h float64) userdata.CoverRegion {
		return userdata.CoverRegion{Page: p, X: x, Y: y, W: w, H: h}
	}
	cases := []struct {
		name  string
		r     userdata.CoverRegion
		count int
		legal bool
	}{
		{"whole page", ok(1, 0, 0, 1, 1), 10, true},
		{"w exactly 0.05", ok(1, 0, 0, 0.05, 0.5), 10, true},
		{"h exactly 0.05", ok(1, 0, 0, 0.5, 0.05), 10, true},
		{"w below 0.05", ok(1, 0, 0, 0.0499, 0.5), 10, false},
		{"h below 0.05", ok(1, 0, 0, 0.5, 0.0499), 10, false},
		{"x + w exactly 1", ok(1, 0.5, 0, 0.5, 0.5), 10, true},
		{"y + h exactly 1", ok(1, 0, 0.25, 0.5, 0.75), 10, true},
		{"x + w over 1", ok(1, 0.51, 0, 0.5, 0.5), 10, false},
		{"y + h over 1", ok(1, 0, 0.26, 0.5, 0.75), 10, false},
		{"x negative", ok(1, -0.01, 0, 0.5, 0.5), 10, false},
		{"y negative", ok(1, 0, -0.01, 0.5, 0.5), 10, false},
		{"page = page_count", ok(10, 0, 0, 1, 1), 10, true},
		{"page = 1", ok(1, 0, 0, 1, 1), 1, true},
		{"page = 0", ok(0, 0, 0, 1, 1), 10, false},
		{"page = page_count + 1", ok(11, 0, 0, 1, 1), 10, false},
		{"page negative", ok(-1, 0, 0, 1, 1), 10, false},
		{"unknown length", ok(1, 0, 0, 1, 1), 0, false},
		{"negative length", ok(1, 0, 0, 1, 1), -1, false},
		{"x NaN", ok(1, nan, 0, 0.5, 0.5), 10, false},
		{"y NaN", ok(1, 0, nan, 0.5, 0.5), 10, false},
		{"w NaN", ok(1, 0, 0, nan, 0.5), 10, false},
		{"h NaN", ok(1, 0, 0, 0.5, nan), 10, false},
		{"w +Inf", ok(1, 0, 0, inf, 0.5), 10, false},
		{"x -Inf", ok(1, -inf, 0, 0.5, 0.5), 10, false},
		{"h +Inf", ok(1, 0, 0, 0.5, inf), 10, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := userdata.ValidateCoverRegion(tc.r, tc.count)
			if tc.legal && err != nil {
				t.Errorf("ValidateCoverRegion(%+v, %d) = %v, want nil", tc.r, tc.count, err)
			}
			if !tc.legal && !errors.Is(err, userdata.ErrInvalidArgument) {
				t.Errorf("ValidateCoverRegion(%+v, %d) = %v, want ErrInvalidArgument", tc.r, tc.count, err)
			}
		})
	}
}

// The rule compares exactly; no tolerance is invented. These are the shapes a
// client produces when it pins the frame to the right or bottom edge
// (x = 1 − w), plus the fractions a 2:3 frame yields on common page sizes. The
// ones listed here must pass as IEEE doubles; anything that does not is a
// finding, not a tolerance to add.
func TestValidateCoverRegion_edgePinnedFramesPassExactly(t *testing.T) {
	t.Parallel()
	for _, r := range []userdata.CoverRegion{
		{Page: 1, X: 0.95, Y: 0, W: 0.05, H: 0.05},
		{Page: 1, X: 0.9, Y: 0.9, W: 0.1, H: 0.1},
		{Page: 1, X: 0.7, Y: 0.3, W: 0.3, H: 0.7},
		{Page: 1, X: 0.4, Y: 0.6, W: 0.6, H: 0.4},
		{Page: 1, X: 1 - 0.3, Y: 1 - 0.7, W: 0.3, H: 0.7},
		{Page: 1, X: 1 - 2.0/3, Y: 0, W: 2.0 / 3, H: 1},
		{Page: 1, X: 1 - 1.0/3, Y: 1 - 0.5, W: 1.0 / 3, H: 0.5},
		{Page: 1, X: 1 - 0.45, Y: 0, W: 0.45, H: 1},
		{Page: 1, X: 0.5, Y: 0, W: 0.5, H: 1},
	} {
		if err := userdata.ValidateCoverRegion(r, 1); err != nil {
			t.Errorf("edge-pinned frame %+v refused: %v", r, err)
		}
	}
}

// ---------------------------------------------------------------- CRUD --

func TestCover_putGetDeleteRoundTrip(t *testing.T) {
	t.Parallel()
	db, clk, _ := newDB(t)
	ctx := t.Context()

	if _, ok, err := db.GetCover(ctx, "b1"); err != nil || ok {
		t.Fatalf("GetCover on an empty table = ok %v, %v; want absent, nil", ok, err)
	}
	got, err := db.PutCover(ctx, coverPut("b1", 3, 10, 0.5, 0, 0.5, 0.75))
	if err != nil {
		t.Fatalf("PutCover: %v", err)
	}
	want := userdata.Cover{
		BookID: "b1", SeriesID: "s1", RootName: "manga", BookPath: "s1/b1.zip",
		Page: 3, PageCount: 10, X: 0.5, Y: 0, W: 0.5, H: 0.75, UpdatedAt: clk.Now().Unix(),
	}
	if got != want {
		t.Errorf("PutCover = %+v, want %+v", got, want)
	}
	if c, ok, err := db.GetCover(ctx, "b1"); err != nil || !ok || c != want {
		t.Errorf("GetCover = %+v (ok %v, %v), want %+v", c, ok, err, want)
	}

	// A second PUT replaces the row and moves updated_at; the baseline
	// page_count is the one given with this designation.
	clk.Advance(time.Minute)
	next := coverPut("b1", 1, 12, 0, 0, 0.5, 0.75)
	next.SeriesID, next.BookPath = "s2", "s2/b1.zip"
	got, err = db.PutCover(ctx, next)
	if err != nil {
		t.Fatalf("second PutCover: %v", err)
	}
	if got.Page != 1 || got.PageCount != 12 || got.X != 0 || got.SeriesID != "s2" ||
		got.BookPath != "s2/b1.zip" || got.UpdatedAt != clk.Now().Unix() {
		t.Errorf("second PutCover = %+v", got)
	}

	if err := db.DeleteCover(ctx, "b1"); err != nil {
		t.Fatalf("DeleteCover: %v", err)
	}
	if _, ok, _ := db.GetCover(ctx, "b1"); ok {
		t.Errorf("cover still present after DeleteCover")
	}
	// DELETE /api/books/{bid}/cover is always 204: no row is not an error.
	if err := db.DeleteCover(ctx, "b1"); err != nil {
		t.Errorf("DeleteCover with no row = %v, want nil", err)
	}
}

func TestPutCover_refusesWhatTheValidatorRefuses_andWritesNothing(t *testing.T) {
	t.Parallel()
	db, _, _ := newDB(t)
	ctx := t.Context()

	for name, p := range map[string]userdata.CoverPut{
		"empty book id":   coverPut("", 1, 10, 0, 0, 1, 1),
		"page past end":   coverPut("b1", 11, 10, 0, 0, 1, 1),
		"region too thin": coverPut("b1", 1, 10, 0, 0, 0.01, 1),
		"unknown length":  coverPut("b1", 1, 0, 0, 0, 1, 1),
	} {
		if _, err := db.PutCover(ctx, p); !errors.Is(err, userdata.ErrInvalidArgument) {
			t.Errorf("%s: PutCover = %v, want ErrInvalidArgument", name, err)
		}
	}
	if all, err := db.ListCovers(ctx); err != nil || len(all) != 0 {
		t.Errorf("rows after refused PUTs = %d (%v), want 0", len(all), err)
	}
	if _, _, err := db.GetCover(ctx, ""); !errors.Is(err, userdata.ErrInvalidArgument) {
		t.Errorf("GetCover(\"\") = %v, want ErrInvalidArgument", err)
	}
	if err := db.DeleteCover(ctx, ""); !errors.Is(err, userdata.ErrInvalidArgument) {
		t.Errorf("DeleteCover(\"\") = %v, want ErrInvalidArgument", err)
	}
}

// chunkedIDs is a lookup list long enough to take several of the
// placeholder-bounded batches GetCovers, GetSeriesPrefsMany and GetPrefsMany
// split it into (forEachIDChunk, 400 ids each), with the stored ids at the
// head of the first batch, well inside a later one, and as the very last id.
// A stored id only at the head is found by a lookup that reads one batch and
// stops (U1 codex cycle 2, finding A3).
func chunkedIDs(first, middle, last string) []string {
	ids := []string{first}
	for i := 0; len(ids) < 1500; i++ {
		if len(ids) == 650 {
			ids = append(ids, middle)
		}
		ids = append(ids, fmt.Sprintf("absent-%d", i))
	}
	return append(ids, last)
}

func TestGetCovers_keysByBookAndAcceptsUnboundedLists(t *testing.T) {
	t.Parallel()
	db, _, _ := newDB(t)
	ctx := t.Context()
	for i, id := range []string{"b1", "b3", "b5"} {
		if _, err := db.PutCover(ctx, coverPut(id, i+2, 10, 0, 0, 1, 1)); err != nil {
			t.Fatalf("PutCover %s: %v", id, err)
		}
	}
	got, err := db.GetCovers(ctx, append(chunkedIDs("b1", "b3", "b5"), "b2"))
	if err != nil {
		t.Fatalf("GetCovers: %v", err)
	}
	if len(got) != 3 || got["b1"].Page != 2 || got["b3"].Page != 3 || got["b5"].Page != 4 ||
		got["b3"].BookID != "b3" {
		t.Errorf("GetCovers = %+v, want b1, b3 and b5 from the first, a middle and the last batch", got)
	}
	if _, ok := got["b2"]; ok {
		t.Errorf("GetCovers invented a row for b2")
	}
	if empty, err := db.GetCovers(ctx, nil); err != nil || len(empty) != 0 {
		t.Errorf("GetCovers(no ids) = (%+v, %v), want an empty result", empty, err)
	}
}

func TestListCovers_ordersByBookID(t *testing.T) {
	t.Parallel()
	db, _, _ := newDB(t)
	ctx := t.Context()
	for _, id := range []string{"c", "a", "b"} {
		if _, err := db.PutCover(ctx, coverPut(id, 1, 5, 0, 0, 1, 1)); err != nil {
			t.Fatalf("PutCover %s: %v", id, err)
		}
	}
	all, err := db.ListCovers(ctx)
	if err != nil {
		t.Fatalf("ListCovers: %v", err)
	}
	var ids []string
	for _, c := range all {
		ids = append(ids, c.BookID)
	}
	if !equalStrings(ids, []string{"a", "b", "c"}) {
		t.Errorf("ListCovers order = %v, want [a b c]", ids)
	}
}

// ------------------------------------------------------- export/import /2 --

func v2(covers []userdata.ExportCover, sp []userdata.ExportSeriesPref) userdata.Export {
	return userdata.Export{Format: "shelf-progress/2", IDVersion: "shelf-id/1",
		Covers: covers, SeriesPrefs: sp}
}

func exportCover(bookID string, page int, x float64, updatedAt int64) userdata.ExportCover {
	return userdata.ExportCover{BookID: bookID, SeriesID: "s1", RootName: "manga",
		BookPath: "s1/" + bookID + ".zip", Page: page, PageCount: 10,
		X: x, Y: 0, W: 0.5, H: 0.75, UpdatedAt: updatedAt}
}

// E-69 §2.9: the two arrays, their field names and order, and [] rather than
// null when empty (the Items/Prefs convention).
func TestExport_v2_wireShapeOfTheNewArrays(t *testing.T) {
	t.Parallel()
	db, clk, _ := newDB(t)
	ctx := t.Context()

	empty, err := db.Export(ctx)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	raw, _ := json.Marshal(empty)
	for _, want := range []string{`"format":"shelf-progress/2"`, `"covers":[]`, `"series_prefs":[]`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("empty export %s lacks %s", raw, want)
		}
	}

	if _, err := db.PutCover(ctx, coverPut("b1", 3, 10, 0.5, 0, 0.5, 0.75)); err != nil {
		t.Fatalf("PutCover: %v", err)
	}
	if _, err := db.PutSeriesPrefs(ctx, seriesPut("s1", ptr("rtl"))); err != nil {
		t.Fatalf("PutSeriesPrefs: %v", err)
	}
	doc, err := db.Export(ctx)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	gotCovers, _ := json.Marshal(doc.Covers)
	wantCovers := fmt.Sprintf(`[{"book_id":"b1","series_id":"s1","root_name":"manga",`+
		`"book_path":"s1/b1.zip","page":3,"page_count":10,"x":0.5,"y":0,"w":0.5,"h":0.75,`+
		`"updated_at":%d}]`, clk.Now().Unix())
	if string(gotCovers) != wantCovers {
		t.Errorf("covers =\n %s\nwant\n %s", gotCovers, wantCovers)
	}
	gotSP, _ := json.Marshal(doc.SeriesPrefs)
	wantSP := `[{"series_id":"s1","root_name":"manga","series_path":"s1","reading_direction":"rtl"}]`
	if string(gotSP) != wantSP {
		t.Errorf("series_prefs =\n %s\nwant\n %s", gotSP, wantSP)
	}
}

func TestExport_v2_roundTripsCoversAndSeriesPrefs(t *testing.T) {
	t.Parallel()
	src, _, _ := newDB(t)
	ctx := t.Context()
	if _, err := src.PutCover(ctx, coverPut("b1", 3, 10, 0.5, 0, 0.5, 0.75)); err != nil {
		t.Fatalf("PutCover: %v", err)
	}
	if _, err := src.PutSeriesPrefs(ctx, seriesPut("s1", ptr("ltr"))); err != nil {
		t.Fatalf("PutSeriesPrefs: %v", err)
	}
	doc, err := src.Export(ctx)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	dstClock := newClock()
	dstClock.Advance(24 * time.Hour)
	dst := openIn(t, t.TempDir(), dstClock)
	res, err := dst.Import(ctx, doc, userdata.StrategyMerge)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	// E-69 §2.9: neither array is counted.
	if res != (userdata.ImportResult{}) {
		t.Errorf("import result = %+v, want all zero (covers/series_prefs are not counted)", res)
	}
	back, err := dst.Export(ctx)
	if err != nil {
		t.Fatalf("Export from the destination: %v", err)
	}
	if len(back.Covers) != 1 || back.Covers[0] != doc.Covers[0] {
		t.Errorf("covers after the round trip = %+v, want %+v", back.Covers, doc.Covers)
	}
	sp, err := dst.GetSeriesPrefs(ctx, "s1")
	if err != nil || deref(sp.ReadingDir) != "ltr" || sp.RootName != "manga" || sp.SeriesPath != "s1" {
		t.Errorf("series prefs after the round trip = %+v (%v)", sp, err)
	}
	// A-17: the stored updated_at of an imported prefs row is the import time.
	if sp.UpdatedAt != dstClock.Now().Unix() {
		t.Errorf("series prefs updated_at = %d, want the import time %d", sp.UpdatedAt, dstClock.Now().Unix())
	}
}

// Import accepts /1 and /2 (E-69 §2.9). A /1 document has no covers and no
// series_prefs: one that carries them was not written by any SHELF, and a
// pre-E-69 server would have refused the unknown keys, so it is refused here —
// whole, before anything is written.
func TestImport_v1EnvelopeIsAcceptedButMayNotCarryTheV2Arrays(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	item := userdata.ExportItem{BookID: "b1", SeriesID: "s1", RootName: "m", BookPath: "p",
		LastPage: 1, PageCount: 5, UpdatedAt: 10}

	db, _, _ := newDB(t)
	if _, err := db.Import(ctx, userdata.Export{Format: "shelf-progress/1", IDVersion: "shelf-id/1",
		Items: []userdata.ExportItem{item}}, userdata.StrategyMerge); err != nil {
		t.Fatalf("plain /1 import = %v, want nil", err)
	}

	for name, doc := range map[string]userdata.Export{
		"covers": {Format: "shelf-progress/1", IDVersion: "shelf-id/1",
			Items:  []userdata.ExportItem{item},
			Covers: []userdata.ExportCover{exportCover("b9", 1, 0, 10)}},
		"series_prefs": {Format: "shelf-progress/1", IDVersion: "shelf-id/1",
			Items:       []userdata.ExportItem{item},
			SeriesPrefs: []userdata.ExportSeriesPref{{SeriesID: "s9", ReadingDir: ptr("rtl")}}},
	} {
		fresh, _, _ := newDB(t)
		if _, err := fresh.Import(ctx, doc, userdata.StrategyMerge); !errors.Is(err, userdata.ErrInvalidArgument) {
			t.Errorf("/1 with %s = %v, want ErrInvalidArgument", name, err)
		}
		if n, _ := fresh.CountProgress(ctx); n != 0 {
			t.Errorf("/1 with %s wrote %d progress rows, want 0", name, n)
		}
	}
	if _, err := db.Import(ctx, userdata.Export{Format: "shelf-progress/3"}, userdata.StrategyMerge); !errors.Is(err, userdata.ErrInvalidArgument) {
		t.Errorf("/3 = %v, want ErrInvalidArgument", err)
	}
}

// covers merge like items (E-69 §2.9): newer updated_at wins, a tie keeps the
// local row; replace overwrites. None of it is counted.
func TestImport_coversMergeOnUpdatedAt(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	cases := []struct {
		name     string
		strategy userdata.ImportStrategy
		delta    int64
		wantX    float64
	}{
		{"merge, incoming newer", userdata.StrategyMerge, +1, 0.25},
		{"merge, tie keeps local", userdata.StrategyMerge, 0, 0.5},
		{"merge, incoming older", userdata.StrategyMerge, -1, 0.5},
		{"replace, incoming older", userdata.StrategyReplace, -1, 0.25},
		{"replace, tie", userdata.StrategyReplace, 0, 0.25},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, _, _ := newDB(t)
			local, err := db.PutCover(ctx, coverPut("b1", 3, 10, 0.5, 0, 0.5, 0.75))
			if err != nil {
				t.Fatalf("PutCover: %v", err)
			}
			in := exportCover("b1", 2, 0.25, local.UpdatedAt+tc.delta)
			res, err := db.Import(ctx, v2([]userdata.ExportCover{in}, nil), tc.strategy)
			if err != nil {
				t.Fatalf("Import: %v", err)
			}
			if res != (userdata.ImportResult{}) {
				t.Errorf("result = %+v, want zero", res)
			}
			got, _, _ := db.GetCover(ctx, "b1")
			if got.X != tc.wantX {
				t.Errorf("x = %v, want %v", got.X, tc.wantX)
			}
			if tc.wantX == 0.25 && (got.Page != 2 || got.UpdatedAt != in.UpdatedAt) {
				t.Errorf("written row = %+v, want page 2, updated_at %d", got, in.UpdatedAt)
			}
		})
	}

	// A book without a local cover takes the incoming one under either strategy.
	for _, s := range []userdata.ImportStrategy{userdata.StrategyMerge, userdata.StrategyReplace} {
		db, _, _ := newDB(t)
		if _, err := db.Import(ctx, v2([]userdata.ExportCover{exportCover("b2", 1, 0, 5)}, nil), s); err != nil {
			t.Fatalf("%s Import: %v", s, err)
		}
		if _, ok, _ := db.GetCover(ctx, "b2"); !ok {
			t.Errorf("%s: absent local cover was not filled", s)
		}
	}
}

// series_prefs follow A-17's prefs rule (E-69 §2.9): merge keeps a local
// choice, replace overwrites, a null row is ignored under both — it does not
// clear a local choice.
func TestImport_seriesPrefsFollowTheA17PrefsRule(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	cases := []struct {
		name     string
		strategy userdata.ImportStrategy
		incoming *string
		want     string
	}{
		{"merge keeps the local choice", userdata.StrategyMerge, ptr("ltr"), "rtl"},
		{"replace overwrites it", userdata.StrategyReplace, ptr("ltr"), "ltr"},
		{"merge ignores null", userdata.StrategyMerge, nil, "rtl"},
		{"replace ignores null", userdata.StrategyReplace, nil, "rtl"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, clk, _ := newDB(t)
			local, err := db.PutSeriesPrefs(ctx, seriesPut("s1", ptr("rtl")))
			if err != nil {
				t.Fatalf("PutSeriesPrefs: %v", err)
			}
			clk.Advance(time.Hour)
			in := userdata.ExportSeriesPref{SeriesID: "s1", RootName: "other", SeriesPath: "elsewhere",
				ReadingDir: tc.incoming}
			res, err := db.Import(ctx, v2(nil, []userdata.ExportSeriesPref{in}), tc.strategy)
			if err != nil {
				t.Fatalf("Import: %v", err)
			}
			if res != (userdata.ImportResult{}) {
				t.Errorf("result = %+v, want zero", res)
			}
			got, _ := db.GetSeriesPrefs(ctx, "s1")
			if deref(got.ReadingDir) != tc.want {
				t.Errorf("reading_dir = %s, want %s", deref(got.ReadingDir), tc.want)
			}
			kept := tc.want == "rtl"
			if kept && got.UpdatedAt != local.UpdatedAt {
				t.Errorf("a kept local row moved updated_at %d -> %d", local.UpdatedAt, got.UpdatedAt)
			}
			if !kept && (got.UpdatedAt != clk.Now().Unix() || got.SeriesPath != "elsewhere") {
				t.Errorf("overwritten row = %+v, want import time %d and the incoming path",
					got, clk.Now().Unix())
			}
		})
	}

	// No local row: merge takes the incoming choice; a null row creates nothing.
	db, _, _ := newDB(t)
	if _, err := db.Import(ctx, v2(nil, []userdata.ExportSeriesPref{
		{SeriesID: "s2", RootName: "m", SeriesPath: "s2", ReadingDir: ptr("ltr")},
		{SeriesID: "s3", RootName: "m", SeriesPath: "s3"},
	}), userdata.StrategyMerge); err != nil {
		t.Fatalf("Import: %v", err)
	}
	all, _ := db.ListSeriesPrefs(ctx)
	if len(all) != 1 || all[0].SeriesID != "s2" || deref(all[0].ReadingDir) != "ltr" {
		t.Errorf("series prefs = %+v, want s2 ltr only", all)
	}
}

// One bad row refuses the whole document (E-69 §2.9): the region gate is the
// PUT's (ValidateCoverRegion against the row's own page_count), the direction
// gate is the frozen enum. Nothing — not even the valid items — is written.
func TestImport_oneBadV2RowRefusesTheWholeDocument(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	good := exportCover("ok", 1, 0, 10)
	bad := func(mut func(*userdata.ExportCover)) userdata.ExportCover {
		c := exportCover("bad", 1, 0, 10)
		mut(&c)
		return c
	}
	cases := map[string]userdata.Export{
		"w below 0.05":           v2([]userdata.ExportCover{good, bad(func(c *userdata.ExportCover) { c.W = 0.04 })}, nil),
		"x + w over 1":           v2([]userdata.ExportCover{good, bad(func(c *userdata.ExportCover) { c.X = 0.6 })}, nil),
		"page past page_count":   v2([]userdata.ExportCover{good, bad(func(c *userdata.ExportCover) { c.Page = 11 })}, nil),
		"page 0":                 v2([]userdata.ExportCover{good, bad(func(c *userdata.ExportCover) { c.Page = 0 })}, nil),
		"page_count 0":           v2([]userdata.ExportCover{good, bad(func(c *userdata.ExportCover) { c.PageCount = 0 })}, nil),
		"NaN":                    v2([]userdata.ExportCover{good, bad(func(c *userdata.ExportCover) { c.H = math.NaN() })}, nil),
		"empty book id":          v2([]userdata.ExportCover{good, bad(func(c *userdata.ExportCover) { c.BookID = "" })}, nil),
		"bad direction":          v2([]userdata.ExportCover{good}, []userdata.ExportSeriesPref{{SeriesID: "s1", ReadingDir: ptr("ttb")}}),
		"empty series id":        v2([]userdata.ExportCover{good}, []userdata.ExportSeriesPref{{SeriesID: "", ReadingDir: ptr("rtl")}}),
		"page = page_count (ok)": v2([]userdata.ExportCover{good, bad(func(c *userdata.ExportCover) { c.Page = 10 })}, nil),
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			db, _, _ := newDB(t)
			doc.Items = []userdata.ExportItem{{BookID: "b1", SeriesID: "s1", RootName: "m",
				BookPath: "p", LastPage: 1, PageCount: 5, UpdatedAt: 10}}
			_, err := db.Import(ctx, doc, userdata.StrategyMerge)
			if strings.HasSuffix(name, "(ok)") {
				if err != nil {
					t.Fatalf("Import = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, userdata.ErrInvalidArgument) {
				t.Fatalf("Import = %v, want ErrInvalidArgument", err)
			}
			if n, _ := db.CountProgress(ctx); n != 0 {
				t.Errorf("progress rows after a refused import = %d, want 0", n)
			}
			if all, _ := db.ListCovers(ctx); len(all) != 0 {
				t.Errorf("cover rows after a refused import = %d, want 0", len(all))
			}
		})
	}
}

// decodeStrict reads a document the way the HTTP layer does (arch §7.1:
// json.Decoder with DisallowUnknownFields, dto.go `progressExport`). A /1
// document's forbidden keys can only be tested this way: a struct built in Go
// cannot say whether a key was present, and E-69 §2.9 asks exactly that.
func decodeStrict(t *testing.T, body string) (userdata.Export, error) {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(body))
	dec.DisallowUnknownFields()
	var doc userdata.Export
	err := dec.Decode(&doc)
	return doc, err
}

// E-69 §2.9: "`format`이 /1인 파일에 `covers`나 `series_prefs`가 있으면 400이다.
// 빈 배열(`[]`)이어도 같다 … 엄격 디코딩은 키가 있는지만 본다." The key's
// presence is the test — `[]` and `null` included — and the refusal writes
// nothing, not even the document's valid items.
func TestImport_v1RefusesTheV2KeysWhateverTheyHold(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	const item = `"items":[{"book_id":"b1","series_id":"s1","root_name":"m","book_path":"p",` +
		`"last_page":1,"page_count":5,"completed":false,"started_at":10,"updated_at":10}],"prefs":[]`
	for name, extra := range map[string]string{
		"covers: []":         `,"covers":[]`,
		"covers: null":       `,"covers":null`,
		"series_prefs: []":   `,"series_prefs":[]`,
		"series_prefs: null": `,"series_prefs":null`,
	} {
		t.Run(name, func(t *testing.T) {
			doc, err := decodeStrict(t, `{"format":"shelf-progress/1","exported_at":1,`+
				`"id_version":"shelf-id/1",`+item+extra+`}`)
			if err != nil {
				t.Fatalf("decoding: %v (the key is known to the type; refusing it is Import's job)", err)
			}
			db, _, _ := newDB(t)
			if _, err := db.Import(ctx, doc, userdata.StrategyMerge); !errors.Is(err, userdata.ErrInvalidArgument) {
				t.Fatalf("/1 with %s = %v, want ErrInvalidArgument", name, err)
			}
			if n, _ := db.CountProgress(ctx); n != 0 {
				t.Errorf("a refused /1 document wrote %d progress rows, want 0", n)
			}
		})
	}

	// The controls: /1 without the keys imports, and /2 (or no format) with
	// the same `[]` / `null` imports too — the refusal is about /1 only.
	for name, body := range map[string]string{
		"/1 without the keys":  `{"format":"shelf-progress/1","exported_at":1,"id_version":"shelf-id/1",` + item + `}`,
		"/2 with covers: null": `{"format":"shelf-progress/2","exported_at":1,"id_version":"shelf-id/1",` + item + `,"covers":null,"series_prefs":[]}`,
		"no format, null":      `{"format":"","exported_at":1,"id_version":"shelf-id/1",` + item + `,"covers":[],"series_prefs":null}`,
	} {
		t.Run(name, func(t *testing.T) {
			doc, err := decodeStrict(t, body)
			if err != nil {
				t.Fatalf("decoding: %v", err)
			}
			db, _, _ := newDB(t)
			if _, err := db.Import(ctx, doc, userdata.StrategyMerge); err != nil {
				t.Fatalf("Import = %v, want nil", err)
			}
			if n, _ := db.CountProgress(ctx); n != 1 {
				t.Errorf("progress rows = %d, want the one item", n)
			}
		})
	}
}

// Whatever keeps the /1 keys' presence must not loosen strict decoding inside
// the rows (arch §7.1): an unknown field in a covers or series_prefs row is
// still a decode error naming the field.
func TestExport_strictDecodingStillReachesInsideTheV2Rows(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"covers row": `{"format":"shelf-progress/2","covers":[{"book_id":"b","series_id":"s","root_name":"m",` +
			`"book_path":"p","page":1,"page_count":2,"x":0,"y":0,"w":1,"h":1,"updated_at":1,"zzz":1}]}`,
		"series_prefs row": `{"format":"shelf-progress/2","series_prefs":[{"series_id":"s","root_name":"m",` +
			`"series_path":"p","reading_direction":null,"zzz":1}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := decodeStrict(t, body)
			if err == nil || !strings.Contains(err.Error(), `unknown field "zzz"`) {
				t.Fatalf("decoding = %v, want the unknown field \"zzz\" refused", err)
			}
		})
	}
	// And a row of the wrong type is still a type error naming the field by its
	// full path (the HTTP layer puts that path in the 400), not a silent zero.
	_, err := decodeStrict(t, `{"covers":[{"page":"one"}]}`)
	var typeErr *json.UnmarshalTypeError
	if !errors.As(err, &typeErr) || typeErr.Field != "covers.page" {
		t.Fatalf(`decoding {"covers":[{"page":"one"}]} = %v, want a type error on covers.page`, err)
	}
}

// The export's own shape is unchanged by any of it: empty arrays are `[]`,
// and a document read back strictly round-trips to the same bytes.
func TestExport_v2_reDecodesToTheSameBytes(t *testing.T) {
	t.Parallel()
	db, _, _ := newDB(t)
	ctx := t.Context()
	if _, err := db.PutCover(ctx, coverPut("b1", 3, 10, 0.5, 0, 0.5, 0.75)); err != nil {
		t.Fatalf("PutCover: %v", err)
	}
	doc, err := db.Export(ctx)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	first, _ := json.Marshal(doc)
	back, err := decodeStrict(t, string(first))
	if err != nil {
		t.Fatalf("decoding the export: %v", err)
	}
	second, _ := json.Marshal(back)
	if string(first) != string(second) || !strings.Contains(string(first), `"series_prefs":[]`) {
		t.Errorf("export %s\nre-decoded %s\nwant identical, with series_prefs []", first, second)
	}
}

// The other direction of the same distinction: a document built in Go without
// the /2 arrays (nil) marshals with no key, so it reads back as the /1 it is;
// Export's empty arrays still marshal as `[]`.
func TestExport_aNilArrayMarshalsAsNoKey(t *testing.T) {
	t.Parallel()
	raw, err := json.Marshal(userdata.Export{Format: "shelf-progress/1", IDVersion: "shelf-id/1",
		Items: []userdata.ExportItem{}, Prefs: []userdata.ExportPref{}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "covers") || strings.Contains(string(raw), "series_prefs") {
		t.Errorf("a /1 document built without the arrays marshals as %s, want neither key", raw)
	}
	doc, err := decodeStrict(t, string(raw))
	if err != nil {
		t.Fatalf("decoding: %v", err)
	}
	db, _, _ := newDB(t)
	if _, err := db.Import(t.Context(), doc, userdata.StrategyMerge); err != nil {
		t.Errorf("re-imported /1 document = %v, want nil", err)
	}
}

// E-69 §2.4: "경계는 허용오차 없이 부동소수 그대로 비교한다." The 0.01-sized
// refusals above would survive a small tolerance (x + w > 1 + 1e-9, w < 0.05 −
// 1e-12); these are one unit in the last place past each bound, so any
// tolerance at all lets one of them through.
func TestValidateCoverRegion_oneULPPastEachBoundIsRefused(t *testing.T) {
	t.Parallel()
	up := func(f float64) float64 { return math.Nextafter(f, 2) }
	overOne := up(1)
	cases := []struct {
		name       string
		r          userdata.CoverRegion
		sum        float64 // the IEEE sum the case is about, checked first
		checkSumOf string
	}{
		{"x + w one ulp over 1 (x = 0)", userdata.CoverRegion{Page: 1, X: 0, Y: 0, W: overOne, H: 0.5}, overOne, "x+w"},
		{"x + w one ulp over 1 (x = 0.5)", userdata.CoverRegion{Page: 1, X: 0.5, Y: 0, W: up(up(0.5)), H: 0.5}, overOne, "x+w"},
		{"y + h one ulp over 1 (y = 0)", userdata.CoverRegion{Page: 1, X: 0, Y: 0, W: 0.5, H: overOne}, overOne, "y+h"},
		{"y + h one ulp over 1 (y = 0.5)", userdata.CoverRegion{Page: 1, X: 0, Y: 0.5, W: 0.5, H: up(up(0.5))}, overOne, "y+h"},
		{"w one ulp under 0.05", userdata.CoverRegion{Page: 1, X: 0, Y: 0, W: math.Nextafter(0.05, 0), H: 0.5}, 0, ""},
		{"h one ulp under 0.05", userdata.CoverRegion{Page: 1, X: 0, Y: 0, W: 0.5, H: math.Nextafter(0.05, 0)}, 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			switch tc.checkSumOf {
			case "x+w":
				if got := tc.r.X + tc.r.W; got != tc.sum {
					t.Fatalf("fixture: x + w = %v, want exactly %v", got, tc.sum)
				}
			case "y+h":
				if got := tc.r.Y + tc.r.H; got != tc.sum {
					t.Fatalf("fixture: y + h = %v, want exactly %v", got, tc.sum)
				}
			}
			if err := userdata.ValidateCoverRegion(tc.r, 10); !errors.Is(err, userdata.ErrInvalidArgument) {
				t.Errorf("ValidateCoverRegion(%+v) = %v, want ErrInvalidArgument", tc.r, err)
			}
		})
	}
}

// E-69 §2.9: "`covers` 행은 병합 판단보다 먼저 검증한다. 그래서 로컬이 더
// 새로워 건너뛸 행이라도 잘못되었으면 파일 전체를 거절한다." The local cover is
// newer, so merge would skip the incoming one — and the incoming one is
// invalid, so the whole document is refused: the local cover stays exactly as
// it was, and the document's other changes (an item, another cover) roll back.
func TestImport_aBadCoverIsRefusedEvenWhenMergeWouldSkipIt(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	db, _, _ := newDB(t)
	local, err := db.PutCover(ctx, coverPut("b1", 3, 10, 0.5, 0, 0.5, 0.75))
	if err != nil {
		t.Fatalf("PutCover: %v", err)
	}
	stale := exportCover("b1", 2, 0.25, local.UpdatedAt-10)
	stale.W = 0.04 // below the 0.05 floor
	other := exportCover("b2", 1, 0, local.UpdatedAt)
	doc := v2([]userdata.ExportCover{other, stale}, nil)
	doc.Items = []userdata.ExportItem{{BookID: "i1", SeriesID: "s1", RootName: "m",
		BookPath: "p", LastPage: 1, PageCount: 5, UpdatedAt: 10}}

	if _, err := db.Import(ctx, doc, userdata.StrategyMerge); !errors.Is(err, userdata.ErrInvalidArgument) {
		t.Fatalf("Import = %v, want ErrInvalidArgument for the stale, invalid cover", err)
	}
	got, ok, _ := db.GetCover(ctx, "b1")
	if !ok || got != local {
		t.Errorf("local cover after the refusal = (%+v, %v), want %+v untouched", got, ok, local)
	}
	if _, ok, _ := db.GetCover(ctx, "b2"); ok {
		t.Error("the document's valid cover was written; the refusal must roll it back")
	}
	if n, _ := db.CountProgress(ctx); n != 0 {
		t.Errorf("progress rows = %d, want 0 (rolled back)", n)
	}
}

// U1 codex cycle 2, finding A2. E-69 §2.9 checks an imported cover "the same
// as the PUT", and §2.6 makes every field of that body required — "빠진 `x`가
// 0으로 통과하지 않게". encoding/json leaves a missing or null number at zero,
// and x = 0, y = 0 are valid, so without a presence check a row with no `x`
// is stored as a cover at the left edge. Each of the region's five fields and
// the row's page_count baseline (0 already means "length unknown" and is
// refused; absent is the same thing said less clearly) is refused when absent
// or null, with the document's valid item, and nothing is written.
//
// The document is decoded from bytes the way the HTTP layer reads it: a struct
// built in Go cannot leave a key out. Whichever stage refuses, decoding or
// Import, the contract is the same — ErrInvalidArgument (400) naming the field,
// and no row written.
func TestImport_aCoverRowMissingARegionFieldIsRefused(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	fields := []struct{ key, value string }{
		{"page", "2"}, {"page_count", "10"}, {"x", "0.25"}, {"y", "0.1"}, {"w", "0.5"}, {"h", "0.75"},
	}
	row := func(skip, null string) string {
		parts := []string{`"book_id":"c1"`, `"series_id":"s1"`, `"root_name":"m"`, `"book_path":"p"`}
		for _, f := range fields {
			switch f.key {
			case skip:
				continue
			case null:
				parts = append(parts, fmt.Sprintf("%q:null", f.key))
			default:
				parts = append(parts, fmt.Sprintf("%q:%s", f.key, f.value))
			}
		}
		parts = append(parts, `"updated_at":10`)
		return "{" + strings.Join(parts, ",") + "}"
	}
	doc := func(cover string) string {
		return `{"format":"shelf-progress/2","exported_at":1,"id_version":"shelf-id/1",` +
			`"items":[{"book_id":"b1","series_id":"s1","root_name":"m","book_path":"p",` +
			`"last_page":1,"page_count":5,"completed":false,"started_at":10,"updated_at":10}],` +
			`"prefs":[],"covers":[` + cover + `],"series_prefs":[]}`
	}
	importBytes := func(t *testing.T, db *userdata.DB, body string) error {
		t.Helper()
		d, err := decodeStrict(t, body)
		if err != nil {
			return err // the HTTP layer answers 400 and never calls Import
		}
		_, err = db.Import(ctx, d, userdata.StrategyMerge)
		return err
	}

	t.Run("control: every field present", func(t *testing.T) {
		db, _, _ := newDB(t)
		if err := importBytes(t, db, doc(row("", ""))); err != nil {
			t.Fatalf("import of a complete row = %v, want nil", err)
		}
		if c, ok, _ := db.GetCover(ctx, "c1"); !ok || c.X != 0.25 || c.Y != 0.1 || c.PageCount != 10 {
			t.Fatalf("cover = (%+v, %v), want the row as sent", c, ok)
		}
	})
	for _, f := range fields {
		for _, how := range []string{"missing", "null"} {
			t.Run(f.key+" "+how, func(t *testing.T) {
				db, _, _ := newDB(t)
				body := doc(row(f.key, ""))
				if how == "null" {
					body = doc(row("", f.key))
				}
				err := importBytes(t, db, body)
				if !errors.Is(err, userdata.ErrInvalidArgument) {
					t.Fatalf("cover row with %s %s = %v, want ErrInvalidArgument", f.key, how, err)
				}
				if !strings.Contains(err.Error(), fmt.Sprintf("%q", f.key)) {
					t.Errorf("refusal %q does not name the field %q", err, f.key)
				}
				if all, _ := db.ListCovers(ctx); len(all) != 0 {
					t.Errorf("a refused document wrote covers %+v", all)
				}
				if n, _ := db.CountProgress(ctx); n != 0 {
					t.Errorf("a refused document wrote %d progress rows, want 0", n)
				}
			})
		}
	}
}
