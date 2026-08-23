package repair

import (
	"testing"

	"shelf/internal/ids"
	"shelf/internal/index"
	"shelf/internal/userdata"
)

const (
	testRoot = "manga"
	testPath = "에버그린 01~23 (완).zip"
	// tagPath is the same file under the name it had before the `[만화] ` tag
	// was stripped from the library.
	tagPath   = "[만화] " + testPath
	otherRoot = "root"
)

var testRoots = []string{otherRoot, testRoot, "root-2"}

// orphan builds a row that passes gate 1 by construction — its id is the id a
// whole file at `path` would have — so a test that wants to fail a *later* gate
// is not silently failing the first one instead.
func orphan(path string, lastPage, baseline int, completed bool) index.SplitOrphan {
	return index.SplitOrphan{
		BookID:    ids.BookID(testRoot, path),
		SeriesID:  "wjryecomlbu4prta",
		RootName:  testRoot,
		BookPath:  path,
		LastPage:  lastPage,
		PageCount: baseline,
		Completed: completed,
		StartedAt: 1000,
		UpdatedAt: 2000,
	}
}

// destSeries is the series the destination books belong to. It is deliberately
// NOT the orphan's own series id: a renamed file belongs to a renamed series,
// and the row has to follow both or it lands reachable and invisible.
const destSeries = "ser-destination"

func vols(counts ...int) []index.SplitVolume {
	out := make([]index.SplitVolume, len(counts))
	for i, c := range counts {
		out[i] = index.SplitVolume{
			BookID: string(rune('a' + i)), SeriesID: destSeries, Ord: i, PageCount: c,
		}
	}
	return out
}

// at is what BooksAt would have answered: books sitting at one Location.
func at(root, path string, v []index.SplitVolume) map[index.Location][]index.SplitVolume {
	return map[index.Location][]index.SplitVolume{{RootName: root, RelPath: path}: v}
}

// pagesRead is the E-47 rollup, on one series' worth of rows: a completed book
// counts its whole length, a started one its last read page. This is the number
// the shelf divides by the series length to get the percentage the reader sees,
// so it is the number a repair has to preserve — not the volume it happens to
// land in.
func pagesRead(rows []userdata.ExportItem) int {
	n := 0
	for _, r := range rows {
		if r.Completed {
			n += r.PageCount
			continue
		}
		n += r.LastPage
	}
	return n
}

func planOneOrphan(t *testing.T, o index.SplitOrphan,
	found map[index.Location][]index.SplitVolume) []userdata.ExportItem {
	t.Helper()
	moves, skipped := Plan([]index.SplitOrphan{o}, testRoots, found)
	if len(skipped) != 0 {
		t.Fatalf("declined %v, want a move", skipped)
	}
	if len(moves) != 1 {
		t.Fatalf("got %d moves, want 1", len(moves))
	}
	return moves[0].Rows
}

// The arithmetic, at every boundary a page can sit on. A partition is only
// correct if it has no holes and no overlaps, and the way both defects show up
// is at the seam between two volumes — so the seam is what is asserted, not a
// comfortable page in the middle of volume two.
//
// The position is the LAST row of a move: the rows before it are the volumes
// the reader already turned past.
func TestPlan_mapsEveryPageToExactlyOneVolume(t *testing.T) {
	v := vols(10, 20, 5) // pages 1-10, 11-30, 31-35
	cases := []struct {
		page      int
		wantVol   string
		wantLocal int
		wantRows  int
	}{
		{1, "a", 1, 1},   // first page of the container
		{10, "a", 10, 1}, // last page of the first volume
		{11, "b", 1, 2},  // first page of the second — the seam
		{30, "b", 20, 2}, // last page of the second
		{31, "c", 1, 3},  // the second seam
		{35, "c", 5, 3},  // last page of the container
	}
	for _, c := range cases {
		rows := planOneOrphan(t, orphan(testPath, c.page, 35, false), at(testRoot, testPath, v))
		if len(rows) != c.wantRows {
			t.Fatalf("page %d: got %d rows, want %d (the volumes read through, plus the one stopped in)",
				c.page, len(rows), c.wantRows)
		}
		got := rows[len(rows)-1]
		if got.BookID != c.wantVol || got.LastPage != c.wantLocal {
			t.Errorf("page %d -> volume %q page %d, want volume %q page %d",
				c.page, got.BookID, got.LastPage, c.wantVol, c.wantLocal)
		}
		if got.PageCount != v[c.wantVol[0]-'a'].PageCount {
			t.Errorf("page %d: baseline %d, want the volume's own length %d",
				c.page, got.PageCount, v[c.wantVol[0]-'a'].PageCount)
		}
		// The property, not the placement: the reader is exactly as far in as
		// they were. Without the completed rows for the volumes behind them,
		// page 35 of 35 would roll up as 5 of 35.
		if n := pagesRead(rows); n != c.page {
			t.Errorf("page %d: rolls up to %d pages read, want %d", c.page, n, c.page)
		}
		for i, r := range rows[:len(rows)-1] {
			if !r.Completed {
				t.Errorf("page %d: volume %d was read through but is not completed", c.page, i)
			}
		}
		wantDone := c.wantLocal == v[c.wantVol[0]-'a'].PageCount
		if got.Completed != wantDone {
			t.Errorf("page %d: position completed=%v, want %v", c.page, got.Completed, wantDone)
		}
	}
}

// The live case this package was written for, kept as a literal so a refactor
// that quietly changes the arithmetic has to change a number a human recognises.
func TestPlan_theLiveEvergreenRow(t *testing.T) {
	// The 24 volumes D-73 made of `에버그린 01~23 (완).zip`, in `ord` order, with
	// the page counts the live index holds. The reader stopped on absolute
	// page 531 of 768 and the shelf drew 0 %.
	counts := []int{32, 38, 30, 49, 39, 42, 43, 32, 38, 31, 40, 37,
		42, 35, 21, 24, 25, 21, 28, 27, 27, 19, 18, 30}
	total := 0
	for _, c := range counts {
		total += c
	}
	if total != 768 {
		t.Fatalf("fixture sums to %d, want the 768 pages the series reports", total)
	}
	rows := planOneOrphan(t, orphan(testPath, 531, 768, false), at(testRoot, testPath, vols(counts...)))
	// Volumes 1..14 cover pages 1..528, so 531 is page 3 of `에버그린 14-2`
	// (ord 14, the fifteenth volume, 21 pages), with the fourteen behind it
	// finished.
	if len(rows) != 15 {
		t.Fatalf("got %d rows, want 15 (14 read through, plus 14-2)", len(rows))
	}
	got := rows[len(rows)-1]
	if got.BookID != "o" || got.LastPage != 3 || got.PageCount != 21 {
		t.Errorf("page 531 -> volume %q page %d of %d, want volume %q page %d of %d",
			got.BookID, got.LastPage, got.PageCount, "o", 3, 21)
	}
	// 531 of 768 is 69.1 %. The shelf showed 0 % before the repair and would
	// show 0.4 % if the volumes behind the reader were left unwritten.
	if n := pagesRead(rows); n != 531 {
		t.Errorf("rolls up to %d pages read of 768, want 531 (69.1 %%)", n)
	}
}

// A rename is the one-volume case of the same walk, and the point of the design:
// no second code path, so the rename cannot drift from the split.
func TestPlan_renameIsTheOneVolumeCase(t *testing.T) {
	// The row was written when the file was `[만화] …`; the index has it under
	// the stripped name with 7,480 pages, exactly as `이누야샤` does live.
	rows := planOneOrphan(t, orphan(tagPath, 4055, 7480, false),
		at(testRoot, testPath, []index.SplitVolume{{BookID: "same", Ord: 0, PageCount: 7480}}))
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	got := rows[0]
	if got.BookID != "same" || got.LastPage != 4055 || got.PageCount != 7480 {
		t.Errorf("row %+v, want the same page 4055 of 7480 on book %q", got, "same")
	}
	if got.Completed {
		t.Error("a reader 54 %% in was marked as finished")
	}
	if n := pagesRead(rows); n != 4055 {
		t.Errorf("rolls up to %d pages read, want 4055", n)
	}
}

// A file that moved to another root is the same relocation with a different
// rule, and it composes with a split: the destination may be volumes.
func TestPlan_rootChangeComposesWithASplit(t *testing.T) {
	o := orphan(testPath, 11, 35, false)
	rows := planOneOrphan(t, o, at(otherRoot, testPath, vols(10, 20, 5)))
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2 (volume 1 read through, plus volume 2)", len(rows))
	}
	if last := rows[len(rows)-1]; last.BookID != "b" || last.LastPage != 1 {
		t.Errorf("page 11 -> volume %q page %d, want volume %q page %d", last.BookID, last.LastPage, "b", 1)
	}
	if n := pagesRead(rows); n != 11 {
		t.Errorf("rolls up to %d pages read, want 11", n)
	}
	// The row keeps the root the reader's history was written under; the index
	// is the authority on where the file is now, and the next write refreshes it.
	if rows[0].RootName != testRoot {
		t.Errorf("root_name %q, want the orphan's own %q", rows[0].RootName, testRoot)
	}
}

// A finished container is a finished *series*, and the 완독 scope counts rows,
// not pages: `count(completed = 1) >= book_count`. Writing only the volume the
// last page falls in would take a series the reader finished out of the 완독
// shelf, which is a worse lie than the 0 % this package exists to fix.
func TestPlan_completedContainerFinishesEveryVolume(t *testing.T) {
	rows := planOneOrphan(t, orphan(testPath, 35, 35, true), at(testRoot, testPath, vols(10, 20, 5)))
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want one per volume (3)", len(rows))
	}
	for i, r := range rows {
		if !r.Completed {
			t.Errorf("volume %d not marked completed", i)
		}
		if r.LastPage != r.PageCount {
			t.Errorf("volume %d at page %d of %d, want its last page", i, r.LastPage, r.PageCount)
		}
	}
}

// Each gate, refused for its own reason. The reason is asserted and not just
// the refusal, because a gate that fires for the wrong reason still passes a
// test that only counts declines.
func TestPlan_declinesWhatItCannotDerive(t *testing.T) {
	cases := []struct {
		name  string
		in    index.SplitOrphan
		found map[index.Location][]index.SplitVolume
		want  SkipReason
	}{
		{
			name: "id is not a whole file's — the row was a volume inside one",
			in: func() index.SplitOrphan {
				o := orphan(testPath, 5, 35, false)
				o.BookID = ids.NestedBookID(testRoot, testPath, "01")
				return o
			}(),
			found: at(testRoot, testPath, vols(10, 20, 5)),
			want:  SkipNotAContainer,
		},
		{
			name:  "nothing at any candidate location",
			in:    orphan(testPath, 5, 35, false),
			found: nil,
			want:  SkipUnresolved,
		},
		{
			name: "two candidate locations both hold books",
			in:   orphan(tagPath, 5, 35, false),
			found: map[index.Location][]index.SplitVolume{
				{RootName: testRoot, RelPath: tagPath}:  vols(35),
				{RootName: testRoot, RelPath: testPath}: vols(10, 20, 5),
			},
			want: SkipAmbiguous,
		},
		{
			name:  "destination is not a partition of the pages the row saw",
			in:    orphan(testPath, 5, 35, false),
			found: at(testRoot, testPath, vols(10, 20)), // sums to 30, baseline 35
			want:  SkipLengthMismatch,
		},
		{
			name:  "page below the file",
			in:    orphan(testPath, 0, 35, false),
			found: at(testRoot, testPath, vols(10, 20, 5)),
			want:  SkipPageOutOfRange,
		},
		{
			name:  "page past the file",
			in:    orphan(testPath, 36, 35, false),
			found: at(testRoot, testPath, vols(10, 20, 5)),
			want:  SkipPageOutOfRange,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			moves, skipped := Plan([]index.SplitOrphan{c.in}, testRoots, c.found)
			if len(moves) != 0 {
				t.Fatalf("got %d moves, want none", len(moves))
			}
			if len(skipped) != 1 {
				t.Fatalf("got %d declines, want 1", len(skipped))
			}
			if skipped[0].Reason != c.want {
				t.Errorf("reason %q, want %q", skipped[0].Reason, c.want)
			}
		})
	}
}

// A zero-length volume owns no pages: `page_count = 0` is "length unknown"
// (arch §4.11), what the scanner leaves behind when a file goes bad. It cannot
// be read through, so it gets no row at all — stamping it finished would put a
// completion on a book nobody can open, and counting it would corrupt the sum.
func TestPlan_zeroLengthVolumeOwnsNoPage(t *testing.T) {
	rows := planOneOrphan(t, orphan(testPath, 11, 30, false), at(testRoot, testPath, vols(10, 0, 20)))
	for _, r := range rows {
		if r.BookID == "b" {
			t.Errorf("the zero-length volume got a row: %+v", r)
		}
	}
	got := rows[len(rows)-1]
	if got.BookID != "c" || got.LastPage != 1 {
		t.Errorf("page 11 -> volume %q page %d, want volume %q page %d", got.BookID, got.LastPage, "c", 1)
	}
	if n := pagesRead(rows); n != 11 {
		t.Errorf("rolls up to %d pages read, want 11", n)
	}
}

// The times on the destination rows are the times the reader made, not the
// times the repair ran. 이어보기 sorts by updated_at, so a repair that stamped
// `now` would jump fifty series to the front of the shelf.
func TestPlan_carriesTheReadersOwnTimes(t *testing.T) {
	rows := planOneOrphan(t, orphan(testPath, 5, 35, false), at(testRoot, testPath, vols(10, 20, 5)))
	got := rows[len(rows)-1]
	if got.StartedAt != 1000 || got.UpdatedAt != 2000 {
		t.Errorf("times (%d, %d), want the orphan's own (1000, 2000)", got.StartedAt, got.UpdatedAt)
	}
	if got.RootName != testRoot || got.BookPath != testPath {
		t.Errorf("row identity %+v, want the orphan's own root and path", got)
	}
}

// The series a row is filed under has to follow the book, and it is a separate
// id from the book's: `progress` carries both, 이어보기 resolves the book, and
// the shelf percentage, the 읽는 중 filter and the 완독 scope all group by the
// series. A carry that moves one and not the other leaves the row reachable and
// invisible at the same time — 27 rows on the live library, after a first cut of
// this package shipped without this line.
func TestPlan_filesTheRowUnderTheDestinationSeries(t *testing.T) {
	rows := planOneOrphan(t, orphan(tagPath, 4055, 7480, false),
		at(testRoot, testPath, []index.SplitVolume{
			{BookID: "same", SeriesID: destSeries, Ord: 0, PageCount: 7480},
		}))
	if got := rows[0].SeriesID; got != destSeries {
		t.Errorf("filed under series %q, want the destination's %q", got, destSeries)
	}
	if rows[0].SeriesID == "wjryecomlbu4prta" {
		t.Error("the orphan's own series id survived the move")
	}
}

// A mixed batch keeps going: one undecidable row must not cost the others their
// repair, and the decline must still be reported.
func TestPlan_oneDeclineDoesNotStopTheRest(t *testing.T) {
	good := orphan(testPath, 5, 35, false)
	bad := orphan(testPath, 5, 99, false) // baseline disagrees with the destination
	moves, skipped := Plan([]index.SplitOrphan{bad, good, bad}, testRoots,
		at(testRoot, testPath, vols(10, 20, 5)))
	if len(moves) != 1 {
		t.Errorf("got %d moves, want 1", len(moves))
	}
	if len(skipped) != 2 {
		t.Errorf("got %d declines, want 2", len(skipped))
	}
}

func TestPlan_emptyInput(t *testing.T) {
	moves, skipped := Plan(nil, testRoots, nil)
	if moves != nil || skipped != nil {
		t.Errorf("got (%v, %v), want (nil, nil)", moves, skipped)
	}
}

// Candidates is what the index is asked to resolve. Identity first, so an
// unmoved container costs no extra lookup; deduplicated, so N orphans in one
// renamed directory are one query each and not N.
func TestCandidates_identityFirstAndDeduplicated(t *testing.T) {
	o := orphan(tagPath, 5, 35, false)
	got := Candidates([]index.SplitOrphan{o, o}, testRoots)
	if len(got) == 0 || got[0] != (index.Location{RootName: testRoot, RelPath: tagPath}) {
		t.Fatalf("first candidate is %+v, want the path as written", got)
	}
	seen := map[index.Location]int{}
	for _, l := range got {
		seen[l]++
	}
	for l, n := range seen {
		if n > 1 {
			t.Errorf("%+v proposed %d times, want once", l, n)
		}
	}
	want := index.Location{RootName: testRoot, RelPath: testPath}
	if _, ok := seen[want]; !ok {
		t.Errorf("the tag-stripped path %+v was never proposed; got %+v", want, got)
	}
}

// The tag rule is the only rename this package will undo. A path that merely
// contains brackets somewhere is not a tagged path, and a rule that matched it
// would start proposing locations nobody renamed.
func TestCandidates_onlyStripsALeadingTag(t *testing.T) {
	o := orphan("Mix 1~19화 [아다치 미츠루]/MIX 01.zip", 5, 35, false)
	for _, l := range Candidates([]index.SplitOrphan{o}, testRoots) {
		if l.RelPath != o.BookPath {
			t.Errorf("proposed a rewritten path %q for a name whose brackets are not a leading tag", l.RelPath)
		}
	}
}

// ------------------------------------------------------- relocations --

func reloc(old, nu string, pages int) index.Relocation {
	return index.Relocation{
		OldBookID: old, NewBookID: nu, OldRelPath: tagPath, NewRelPath: testPath,
		NewRootName: testRoot, NewSeriesID: destSeries, NewPageCount: pages,
	}
}

func progressOn(bookID string, page, count int) userdata.Progress {
	return userdata.Progress{
		BookID: bookID, SeriesID: "wjryecomlbu4prta", RootName: testRoot,
		BookPath: tagPath, LastPage: page, PageCount: count,
		StartedAt: 1000, UpdatedAt: 2000,
	}
}

// The evidence path: the scan proved the move, so there is no arithmetic to do
// beyond carrying the page across and no opinion about filenames anywhere.
func TestPlanRelocations_carriesTheReadersPlace(t *testing.T) {
	moves, skipped := PlanRelocations(
		[]index.Relocation{reloc("old", "new", 94)},
		map[string]userdata.Progress{"old": progressOn("old", 12, 94)})
	if len(skipped) != 0 {
		t.Fatalf("declined %v", skipped)
	}
	if len(moves) != 1 || len(moves[0].Rows) != 1 {
		t.Fatalf("got %+v, want one move of one row", moves)
	}
	got := moves[0].Rows[0]
	if moves[0].OldBookID != "old" || got.BookID != "new" || got.LastPage != 12 {
		t.Errorf("row %+v (retiring %q), want page 12 on %q", got, moves[0].OldBookID, "new")
	}
	if got.SeriesID != destSeries {
		t.Errorf("filed under %q, want the destination series %q", got.SeriesID, destSeries)
	}
	if got.BookPath != testPath || got.RootName != testRoot {
		t.Errorf("row points at %q in %q, want the new path", got.BookPath, got.RootName)
	}
	if got.StartedAt != 1000 || got.UpdatedAt != 2000 {
		t.Errorf("times (%d, %d), want the reader's own", got.StartedAt, got.UpdatedAt)
	}
}

// A book that moved but was never opened has nothing to carry, and writing a
// row for it would invent a reading position out of a filesystem event.
func TestPlanRelocations_ignoresBooksNobodyRead(t *testing.T) {
	moves, skipped := PlanRelocations(
		[]index.Relocation{reloc("old", "new", 94)}, map[string]userdata.Progress{})
	if len(moves) != 0 || len(skipped) != 0 {
		t.Errorf("got moves %+v skipped %+v, want both empty", moves, skipped)
	}
}

// content_version is (size, mtime), so a genuine move cannot change the length.
// A disagreement means the pairing is not what it claims.
func TestPlanRelocations_declinesALengthChange(t *testing.T) {
	moves, skipped := PlanRelocations(
		[]index.Relocation{reloc("old", "new", 40)},
		map[string]userdata.Progress{"old": progressOn("old", 12, 94)})
	if len(moves) != 0 {
		t.Fatalf("got %+v, want no moves", moves)
	}
	if len(skipped) != 1 || skipped[0].Reason != SkipLengthMismatch {
		t.Fatalf("declines %+v, want one length-mismatch", skipped)
	}
}

func TestRelocationIDs(t *testing.T) {
	got := RelocationIDs([]index.Relocation{reloc("a", "x", 1), reloc("b", "y", 1)})
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("got %v, want [a b]", got)
	}
}
