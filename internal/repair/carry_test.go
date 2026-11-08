package repair

import (
	"testing"

	"shelf/internal/ids"
	"shelf/internal/index"
	"shelf/internal/userdata"
)

// ---------------------------------------------------------------------------
// E-69 §2.11(b) — a chosen cover through the split reattach
// ---------------------------------------------------------------------------

// coverOrphan builds a cover row that passes gate 1 by construction, so a test
// aimed at a later gate cannot be failing the first one instead.
func coverOrphan(path string, page, baseline int) index.CoverOrphan {
	return index.CoverOrphan{
		BookID:    ids.BookID(testRoot, path),
		SeriesID:  "wjryecomlbu4prta",
		RootName:  testRoot,
		BookPath:  path,
		Page:      page,
		PageCount: baseline,
		X:         0.125, Y: 0.25, W: 0.5, H: 0.625,
		UpdatedAt: 3000,
	}
}

func planOneCover(t *testing.T, o index.CoverOrphan,
	found map[index.Location][]index.SplitVolume) userdata.CoverMove {
	t.Helper()
	moves, skipped := PlanCovers([]index.CoverOrphan{o}, testRoots, found)
	if len(skipped) != 0 {
		t.Fatalf("declined %v, want a move", skipped)
	}
	if len(moves) != 1 {
		t.Fatalf("got %d moves, want 1", len(moves))
	}
	return moves[0]
}

// The arithmetic at every seam: the first page, the last page of a volume
// (P = end(v)), the first page of the next, and the last page of the book.
// Volumes of 10, 20 and 5 pages: ranges [1,10], [11,30], [31,35].
func TestPlanCovers_mapsThePageAcrossTheSplit(t *testing.T) {
	for _, c := range []struct {
		page      int
		wantBook  string
		wantPage  int
		wantCount int
	}{
		{1, "a", 1, 10},
		{10, "a", 10, 10},
		{11, "b", 1, 20},
		{30, "b", 20, 20},
		{31, "c", 1, 5},
		{35, "c", 5, 5},
	} {
		o := coverOrphan(testPath, c.page, 35)
		m := planOneCover(t, o, at(testRoot, testPath, vols(10, 20, 5)))
		if m.OldBookID != o.BookID {
			t.Errorf("page %d: retires %q, want the orphan %q", c.page, m.OldBookID, o.BookID)
		}
		r := m.Row
		if r.BookID != c.wantBook || r.Page != c.wantPage || r.PageCount != c.wantCount {
			t.Errorf("page %d -> book %q page %d of %d, want book %q page %d of %d",
				c.page, r.BookID, r.Page, r.PageCount, c.wantBook, c.wantPage, c.wantCount)
		}
	}
}

// A zero-length volume owns no page (arch §4.11), so page 11 of 10+0+20 is the
// first page of the THIRD volume, not of the empty second one.
func TestPlanCovers_zeroLengthVolumeOwnsNoPage(t *testing.T) {
	m := planOneCover(t, coverOrphan(testPath, 11, 30), at(testRoot, testPath, vols(10, 0, 20)))
	if m.Row.BookID != "c" || m.Row.Page != 1 || m.Row.PageCount != 20 {
		t.Errorf("page 11 -> %q page %d of %d, want c page 1 of 20", m.Row.BookID, m.Row.Page, m.Row.PageCount)
	}
}

// The region is in page coordinates and the page is the same page, so the
// region is carried untouched; the series is the destination's (a renamed
// file is in a renamed series); the time is the reader's own. The root and
// path are the destination's too — the location the gate actually chose, here
// the tag-stripped path under ANOTHER root. A cover filed under the orphan's
// old root would be purged as "vanished" by a walk of that root while its real
// root sat unwalked (U1 codex cycle 1, finding 5).
func TestPlanCovers_carriesTheRegionAndFilesUnderTheDestination(t *testing.T) {
	o := coverOrphan(tagPath, 12, 35)
	r := planOneCover(t, o, at(otherRoot, testPath, vols(10, 20, 5))).Row
	if r.X != o.X || r.Y != o.Y || r.W != o.W || r.H != o.H {
		t.Errorf("region (%v,%v,%v,%v), want (%v,%v,%v,%v) unchanged", r.X, r.Y, r.W, r.H, o.X, o.Y, o.W, o.H)
	}
	if r.SeriesID != destSeries {
		t.Errorf("filed under %q, want the destination series %q", r.SeriesID, destSeries)
	}
	if r.UpdatedAt != o.UpdatedAt {
		t.Errorf("updated_at %d, want the reader's %d", r.UpdatedAt, o.UpdatedAt)
	}
	if r.RootName != otherRoot || r.BookPath != testPath {
		t.Errorf("identity (%q, %q), want the chosen destination's (%q, %q)",
			r.RootName, r.BookPath, otherRoot, testPath)
	}
}

// Each of the E-48 §3 gates, broken one at a time, against the cover's OWN
// baseline. A row that fails any of them is left exactly where it is.
func TestPlanCovers_declinesWhatItCannotDerive(t *testing.T) {
	okDest := at(testRoot, testPath, vols(10, 20, 5))
	twoPlaces := map[index.Location][]index.SplitVolume{
		{RootName: testRoot, RelPath: testPath}:  vols(10, 20, 5),
		{RootName: otherRoot, RelPath: testPath}: vols(35),
	}
	notContainer := coverOrphan(testPath, 3, 35)
	notContainer.BookID = ids.NestedBookID(testRoot, testPath, "01권")

	for _, c := range []struct {
		name  string
		o     index.CoverOrphan
		found map[index.Location][]index.SplitVolume
		want  SkipReason
	}{
		{"gate 1: a volume inside a container", notContainer, okDest, SkipNotAContainer},
		{"gate 2: nowhere", coverOrphan(testPath, 3, 35), nil, SkipUnresolved},
		{"gate 2: two places", coverOrphan(testPath, 3, 35), twoPlaces, SkipAmbiguous},
		{"gate 3: sum is not the baseline", coverOrphan(testPath, 3, 36), okDest, SkipLengthMismatch},
		{"gate 4: page 0", coverOrphan(testPath, 0, 35), okDest, SkipPageOutOfRange},
		{"gate 4: past the end", coverOrphan(testPath, 36, 35), okDest, SkipPageOutOfRange},
	} {
		t.Run(c.name, func(t *testing.T) {
			moves, skipped := PlanCovers([]index.CoverOrphan{c.o}, testRoots, c.found)
			if len(moves) != 0 {
				t.Fatalf("got moves %+v, want none", moves)
			}
			if len(skipped) != 1 || skipped[0].Reason != c.want {
				t.Fatalf("declines %+v, want one %s", skipped, c.want)
			}
		})
	}
}

// The cover goes through the same candidate rules as progress — the identity
// location first, then the tag-stripped path — and needs no progress row: its
// baseline is its own page_count.
func TestPlanCovers_candidatesIncludeTheTagStrippedPath(t *testing.T) {
	o := coverOrphan(tagPath, 12, 35)
	locs := CoverCandidates([]index.CoverOrphan{o}, testRoots)
	if len(locs) == 0 || locs[0] != (index.Location{RootName: testRoot, RelPath: tagPath}) {
		t.Fatalf("candidates %v, want the identity location first", locs)
	}
	stripped := index.Location{RootName: testRoot, RelPath: testPath}
	listed := false
	for _, l := range locs {
		listed = listed || l == stripped
	}
	if !listed {
		t.Fatalf("candidates %v do not include the tag-stripped path %v", locs, stripped)
	}
	// What BooksAt would answer for exactly these candidates, against a library
	// that holds the book at the stripped path only. A candidate list that lost
	// the stripped path finds nothing here, and the plan below fails.
	library := at(testRoot, testPath, vols(10, 20, 5))
	found := map[index.Location][]index.SplitVolume{}
	for _, l := range locs {
		if v, ok := library[l]; ok {
			found[l] = v
		}
	}
	m := planOneCover(t, o, found)
	if m.Row.BookID != "b" || m.Row.Page != 2 {
		t.Errorf("got %q page %d, want b page 2", m.Row.BookID, m.Row.Page)
	}
}

// ---------------------------------------------------------------------------
// E-69 §2.12 — book_prefs through the split reattach
// ---------------------------------------------------------------------------

// Only when the PROGRESS row passed the gates, and to every volume — the
// unstarted ones and the zero-length one included. prefs have no baseline of
// their own, so the progress row's arithmetic is their only proof.
func TestPlanSplitPrefs_followsAProgressRowThatPassedTheGates(t *testing.T) {
	o := orphan(testPath, 12, 35, false)
	got := PlanSplitPrefs([]index.SplitOrphan{o}, testRoots, at(testRoot, testPath, vols(10, 0, 25)))
	if len(got) != 1 {
		t.Fatalf("got %+v, want one prefs move", got)
	}
	m := got[0]
	if m.OldBookID != o.BookID || !m.OnlyIfAbsent {
		t.Errorf("move %+v, want from %q with only-if-absent", m, o.BookID)
	}
	if len(m.To) != 3 || m.To[0] != "a" || m.To[1] != "b" || m.To[2] != "c" {
		t.Errorf("to %v, want every volume [a b c]", m.To)
	}
}

func TestPlanSplitPrefs_nothingWhenTheProgressRowIsDeclined(t *testing.T) {
	for _, o := range []index.SplitOrphan{
		orphan(testPath, 12, 36, false), // gate 3
		orphan(testPath, 0, 35, false),  // gate 4
	} {
		if got := PlanSplitPrefs([]index.SplitOrphan{o}, testRoots,
			at(testRoot, testPath, vols(10, 20, 5))); len(got) != 0 {
			t.Errorf("orphan at page %d of %d: got %+v, want nothing", o.LastPage, o.PageCount, got)
		}
	}
	if got := PlanSplitPrefs([]index.SplitOrphan{orphan(testPath, 3, 35, false)}, testRoots, nil); len(got) != 0 {
		t.Errorf("unresolved orphan: got %+v, want nothing", got)
	}
}

// ---------------------------------------------------------------------------
// E-69 §2.11(a)(e), §2.12 — what a proven move carries besides progress
// ---------------------------------------------------------------------------

func relocFrom(oldID, newID, oldSeries, newSeries string, pages int) index.Relocation {
	r := reloc(oldID, newID, pages)
	r.OldSeriesID, r.NewSeriesID = oldSeries, newSeries
	return r
}

func coverOn(id string, page, baseline int) userdata.Cover {
	return userdata.Cover{
		BookID: id, SeriesID: "old-series", RootName: testRoot, BookPath: tagPath,
		Page: page, PageCount: baseline, X: 0.1, Y: 0.2, W: 0.3, H: 0.4, UpdatedAt: 4000,
	}
}

func onlyPrefs(t *testing.T, c userdata.Carry, from, to string) {
	t.Helper()
	if len(c.Prefs) != 1 || c.Prefs[0].OldBookID != from || len(c.Prefs[0].To) != 1 ||
		c.Prefs[0].To[0] != to || c.Prefs[0].OnlyIfAbsent {
		t.Errorf("prefs moves %+v, want one newer-wins move %s -> %s", c.Prefs, from, to)
	}
}

// §2.12: a book nobody read still carries its prefs. The pairing is proved by
// content; there is no baseline to check, so the proof is the content alone.
func TestPlanCarry_prefsFollowABookWithNoProgress(t *testing.T) {
	c, skipped := PlanCarry([]index.Relocation{reloc("old", "new", 94)}, nil, nil, nil)
	if len(skipped) != 0 {
		t.Fatalf("declined %+v", skipped)
	}
	if len(c.Progress) != 0 || len(c.Covers) != 0 {
		t.Errorf("progress %+v covers %+v, want none for a book with neither", c.Progress, c.Covers)
	}
	onlyPrefs(t, c, "old", "new")
}

// The progress part is PlanRelocations' output, unchanged.
func TestPlanCarry_progressIsPlanRelocations(t *testing.T) {
	relocs := []index.Relocation{reloc("old", "new", 94), reloc("old2", "new2", 40)}
	progress := map[string]userdata.Progress{
		"old": progressOn("old", 12, 94), "old2": progressOn("old2", 3, 94),
	}
	want, wantSkip := PlanRelocations(relocs, progress)
	c, skipped := PlanCarry(relocs, progress, nil, nil)
	if len(c.Progress) != len(want) || len(c.Progress) != 1 || c.Progress[0].Rows[0] != want[0].Rows[0] {
		t.Errorf("progress %+v, want PlanRelocations' %+v", c.Progress, want)
	}
	if len(skipped) != len(wantSkip) || skipped[0] != wantSkip[0] {
		t.Errorf("skipped %+v, want PlanRelocations' %+v", skipped, wantSkip)
	}
}

// §2.11(a): the cover moves to the new id with the new identity columns and
// the page and region it had.
func TestPlanCarry_coverFollowsWithTheNewIdentity(t *testing.T) {
	rl := relocFrom("old", "new", "old-series", "new-series", 94)
	c, skipped := PlanCarry([]index.Relocation{rl}, nil,
		map[string]userdata.Cover{"old": coverOn("old", 7, 94)}, nil)
	if len(skipped) != 0 {
		t.Fatalf("declined %+v", skipped)
	}
	if len(c.Covers) != 1 {
		t.Fatalf("covers %+v, want one", c.Covers)
	}
	m := c.Covers[0]
	want := coverOn("old", 7, 94)
	want.BookID, want.SeriesID, want.RootName, want.BookPath = "new", "new-series", rl.NewRootName, rl.NewRelPath
	if m.OldBookID != "old" || m.Row != want {
		t.Errorf("cover move %+v, want old -> %+v", m, want)
	}
	onlyPrefs(t, c, "old", "new")
}

// E-69 §2.11: "(a)의 관문은 `content_version` 증명 하나다. progress 행의 길이
// 불일치로 이관이 거절되는 것은 progress에만 걸리고, 표지·prefs·시리즈 1:1 판단을
// 막지 않는다." The progress row is refused exactly as PlanRelocations refuses
// it; the cover and the prefs follow the proven move regardless.
func TestPlanCarry_aProgressRefusalHoldsBackOnlyTheProgress(t *testing.T) {
	c, skipped := PlanCarry([]index.Relocation{reloc("old", "new", 40)},
		map[string]userdata.Progress{"old": progressOn("old", 12, 94)},
		map[string]userdata.Cover{"old": coverOn("old", 7, 40)}, nil)
	if len(c.Progress) != 0 {
		t.Errorf("progress %+v, want the refused row left where it is", c.Progress)
	}
	if len(skipped) != 1 || skipped[0].Reason != SkipLengthMismatch {
		t.Errorf("skipped %+v, want the one progress refusal", skipped)
	}
	if len(c.Covers) != 1 || c.Covers[0].Row.BookID != "new" {
		t.Errorf("covers %+v, want the cover carried to new", c.Covers)
	}
	onlyPrefs(t, c, "old", "new")
}

// The cover's own baseline is not a gate on a proven move: the same content
// is the same pages, and a baseline that disagrees only says the row is older
// than the file (§2.5 keeps such a row; it does not repair or drop it). The
// baseline check is the split reattach's (§2.11(b)), not the relocation's.
func TestPlanCarry_theCoverBaselineIsNotAGateOnAProvenMove(t *testing.T) {
	for _, cv := range []userdata.Cover{coverOn("old", 7, 94), coverOn("old", 41, 41)} {
		c, skipped := PlanCarry([]index.Relocation{reloc("old", "new", 40)}, nil,
			map[string]userdata.Cover{"old": cv}, nil)
		if len(skipped) != 0 {
			t.Errorf("cover page %d of %d: declined %+v, want nothing declined", cv.Page, cv.PageCount, skipped)
		}
		if len(c.Covers) != 1 || c.Covers[0].Row.Page != cv.Page || c.Covers[0].Row.PageCount != cv.PageCount {
			t.Errorf("cover page %d of %d: carried %+v, want it carried as stored", cv.Page, cv.PageCount, c.Covers)
		}
		onlyPrefs(t, c, "old", "new")
	}
}

// Progress and cover both present, progress carried: a cover chosen before the
// file changed length is carried as it is (§2.5: the row is kept, not
// repaired), next to the progress.
func TestPlanCarry_aCoverWithAnOlderBaselineIsCarriedAsStored(t *testing.T) {
	c, skipped := PlanCarry([]index.Relocation{reloc("old", "new", 94)},
		map[string]userdata.Progress{"old": progressOn("old", 12, 94)},
		map[string]userdata.Cover{"old": coverOn("old", 7, 90)}, nil)
	if len(skipped) != 0 || len(c.Covers) != 1 || c.Covers[0].Row.PageCount != 90 {
		t.Errorf("covers %+v skipped %+v, want the cover carried unchanged", c.Covers, skipped)
	}
}

func seriesNow(entries ...string) map[string]index.Location {
	out := map[string]index.Location{}
	for _, id := range entries {
		out[id] = index.Location{RootName: testRoot, RelPath: "path of " + id}
	}
	return out
}

// §2.11(e): every move out of the old series went to one new series, and the
// old series is no longer in the index.
func TestPlanCarry_seriesPrefsFollowASeriesLevelOneToOne(t *testing.T) {
	relocs := []index.Relocation{
		relocFrom("o1", "n1", "S", "T", 9),
		relocFrom("o2", "n2", "S", "T", 9),
	}
	c, _ := PlanCarry(relocs, nil, nil, seriesNow("T"))
	if len(c.Series) != 1 {
		t.Fatalf("series moves %+v, want one", c.Series)
	}
	want := userdata.SeriesPrefsMove{OldSeriesID: "S", NewSeriesID: "T", RootName: testRoot, SeriesPath: "path of T"}
	if c.Series[0] != want {
		t.Errorf("series move %+v, want %+v", c.Series[0], want)
	}
}

func TestPlanCarry_seriesPrefsRefuseAnythingButOneToOne(t *testing.T) {
	for _, c := range []struct {
		name   string
		relocs []index.Relocation
		now    map[string]index.Location
	}{
		{"old series still indexed",
			[]index.Relocation{relocFrom("o1", "n1", "S", "T", 9)}, seriesNow("S", "T")},
		{"old series went two ways",
			[]index.Relocation{relocFrom("o1", "n1", "S", "T", 9), relocFrom("o2", "n2", "S", "U", 9)},
			seriesNow("T", "U")},
		{"two old series into one",
			[]index.Relocation{relocFrom("o1", "n1", "S", "T", 9), relocFrom("o2", "n2", "R", "T", 9)},
			seriesNow("T")},
		{"same series", []index.Relocation{relocFrom("o1", "n1", "S", "S", 9)}, seriesNow("S")},
		{"no old series recorded", []index.Relocation{relocFrom("o1", "n1", "", "T", 9)}, seriesNow("T")},
		{"destination not indexed", []index.Relocation{relocFrom("o1", "n1", "S", "T", 9)}, seriesNow()},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, _ := PlanCarry(c.relocs, nil, nil, c.now)
			if len(got.Series) != 0 {
				t.Errorf("series moves %+v, want none", got.Series)
			}
		})
	}
}

// A progress refusal is about the progress row, not the pairing (E-69 §2.11):
// the move is proven by content, so a series whose every book went to one new
// series moved 1:1 even when one book's progress was not carried.
func TestPlanCarry_seriesPrefsFollowEvenWhenAProgressRowWasRefused(t *testing.T) {
	relocs := []index.Relocation{
		relocFrom("o1", "n1", "S", "T", 9),
		relocFrom("o2", "n2", "S", "T", 40),
	}
	c, skipped := PlanCarry(relocs, map[string]userdata.Progress{"o2": progressOn("o2", 3, 94)}, nil, seriesNow("T"))
	if len(skipped) != 1 {
		t.Fatalf("fixture: skipped %+v, want o2's progress refused", skipped)
	}
	if len(c.Series) != 1 || c.Series[0].OldSeriesID != "S" || c.Series[0].NewSeriesID != "T" {
		t.Errorf("series moves %+v, want S -> T", c.Series)
	}
}
