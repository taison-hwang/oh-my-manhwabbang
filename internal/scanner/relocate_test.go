package scanner

import (
	"testing"
	"time"

	"shelf/internal/index"
	"shelf/internal/testutil"
)

// A rename is expressed by rebinding the harness from one tree to another
// rather than by moving a file: FR-CFG-005 forbids a filesystem mutation
// primitive in a media-reading package, tests included, and `make lint` greps
// this directory for one. Rebinding loses nothing, because the two trees are the
// same library before and after and that is all the index can see anyway — it
// never watches a move happen, it compares two walks.
//
// `pinned` fixes the mtime, and fixing it is the point rather than tidiness:
// content_version is FNV-1a over (size, mtime) with no path in it, so the same
// bytes written at the same instant under a different name are, to the index,
// one file that moved. A test that let the filesystem choose the mtime would
// assert nothing about relocation and everything about how fast it ran.
var pinned = time.Unix(1_700_000_000, 0).UTC()

func file(data []byte) testutil.File { return testutil.File{Data: data, ModTime: pinned} }

// relocations flattens what a run observed, across roots.
func relocations(res *Result) []index.Relocation {
	var out []index.Relocation
	for _, rr := range res.Roots {
		out = append(out, rr.Relocations...)
	}
	return out
}

// rebind points the harness's root at a different tree and rewires everything
// that reads from it, exactly as the sweep tests do.
func rebind(t *testing.T, h *harness, dir string) {
	t.Helper()
	h.rootDirs["manga"] = dir
	h.cfgRoots[0].Path = dir
	h.build()
}

// The whole mechanism: no filename rule, no similarity, just the content hash
// the scanner already computes for cache busting. The old row and the new one
// exist together for exactly one moment — after the walk, before the sweep —
// and that is where the pairing is made.
func TestScan_seesARenameAsOneBookMoving(t *testing.T) {
	t.Parallel()
	monster := jpegZIP(t, "01.jpg", "02.jpg", "03.jpg")
	other := jpegZIP(t, "a.jpg")

	before := testutil.BuildTree(t, map[string]any{
		"[만화] 몬스터 1~18권.zip": file(monster),
		"별개의 책.zip":           file(other),
	})
	after := testutil.BuildTree(t, map[string]any{
		"몬스터 1~18권.zip": file(monster),
		"별개의 책.zip":     file(other),
	})

	h := newHarnessAt(t, map[string]string{"manga": before})
	if got := relocations(h.run(Request{})); len(got) != 0 {
		t.Fatalf("a first scan reported %d relocations, want none", len(got))
	}
	old := h.books("manga", "[만화] 몬스터 1~18권.zip")
	if len(old) != 1 {
		t.Fatalf("fixture has %d books, want 1", len(old))
	}

	rebind(t, h, after)
	got := relocations(h.run(Request{}))
	if len(got) != 1 {
		t.Fatalf("got %d relocations, want 1: %+v", len(got), got)
	}
	r := got[0]
	now := h.books("manga", "몬스터 1~18권.zip")
	if len(now) != 1 {
		t.Fatalf("the renamed file indexed as %d books, want 1", len(now))
	}
	if r.OldBookID != old[0].ID || r.NewBookID != now[0].ID {
		t.Errorf("paired %s -> %s, want %s -> %s", r.OldBookID, r.NewBookID, old[0].ID, now[0].ID)
	}
	if r.OldRelPath != "[만화] 몬스터 1~18권.zip" || r.NewRelPath != "몬스터 1~18권.zip" {
		t.Errorf("paths %q -> %q, want the tagged name to the stripped one", r.OldRelPath, r.NewRelPath)
	}
	if r.NewPageCount != 3 || r.NewRootName != "manga" {
		t.Errorf("destination %d pages in root %q, want 3 in manga", r.NewPageCount, r.NewRootName)
	}
	// The series id is carried because progress keys on it too: the shelf's
	// percentage, 읽는 중 and 완독 all group by series, and a row moved with the
	// old one is reachable and invisible at once.
	if r.NewSeriesID == "" || r.NewSeriesID != now[0].SeriesID {
		t.Errorf("new series %q, want the destination's %q", r.NewSeriesID, now[0].SeriesID)
	}
}

// Moving a file into a subdirectory is the same event: the id is derived from
// the whole root-relative path, so a new parent is a new book to the index.
func TestScan_seesAMoveIntoADirectory(t *testing.T) {
	t.Parallel()
	book := jpegZIP(t, "01.jpg", "02.jpg")

	before := testutil.BuildTree(t, map[string]any{"떠도는 책.zip": file(book)})
	after := testutil.BuildTree(t, map[string]any{
		"보관함": map[string]any{"떠도는 책.zip": file(book)},
	})

	h := newHarnessAt(t, map[string]string{"manga": before})
	h.run(Request{})
	old := h.books("manga", "떠도는 책.zip")
	if len(old) != 1 {
		t.Fatalf("fixture has %d books, want 1", len(old))
	}

	rebind(t, h, after)
	got := relocations(h.run(Request{}))
	if len(got) != 1 {
		t.Fatalf("got %d relocations, want 1: %+v", len(got), got)
	}
	if got[0].OldBookID != old[0].ID {
		t.Errorf("old id %s, want %s", got[0].OldBookID, old[0].ID)
	}
	if got[0].NewRelPath != "보관함/떠도는 책.zip" {
		t.Errorf("new path %q, want it under 보관함", got[0].NewRelPath)
	}
}

// Two files with identical content are what makes the 1:1 rule necessary rather
// than defensive, and a library assembled by hand is full of them. When one of a
// duplicated pair moves, nothing can say which one did, so the scan reports
// neither and the reader's place stays where it is.
func TestScan_refusesToPairDuplicatedContent(t *testing.T) {
	t.Parallel()
	same := jpegZIP(t, "01.jpg", "02.jpg")

	before := testutil.BuildTree(t, map[string]any{
		"사본 A.zip": file(same),
		"사본 B.zip": file(same),
	})
	after := testutil.BuildTree(t, map[string]any{
		"사본 C.zip": file(same), // A renamed…
		"사본 B.zip": file(same), // …and its twin still here
	})

	h := newHarnessAt(t, map[string]string{"manga": before})
	h.run(Request{})
	rebind(t, h, after)

	if got := relocations(h.run(Request{})); len(got) != 0 {
		t.Errorf("paired %+v; two files share this content, so no pairing is provable", got)
	}
}

// A file that is simply gone has no surviving twin, so there is nothing to pair.
// This is the case the design must never guess at, and the sweep still does its
// half of the job.
func TestScan_deletedFileIsNotARelocation(t *testing.T) {
	t.Parallel()
	doomed := jpegZIP(t, "01.jpg", "02.jpg", "03.jpg", "04.jpg")
	keeper := jpegZIP(t, "a.jpg")

	before := testutil.BuildTree(t, map[string]any{
		"사라질 책.zip": file(doomed),
		"남을 책.zip":  file(keeper),
	})
	after := testutil.BuildTree(t, map[string]any{"남을 책.zip": file(keeper)})

	h := newHarnessAt(t, map[string]string{"manga": before})
	h.run(Request{})
	rebind(t, h, after)

	res := h.run(Request{})
	if got := relocations(res); len(got) != 0 {
		t.Errorf("a deletion was reported as %+v", got)
	}
	for _, s := range h.series() {
		if s.RelPath == "사라질 책.zip" {
			t.Errorf("the deleted file is still indexed as series %q", s.ID)
		}
	}
	if res.Roots[0].Swept.Books == 0 {
		t.Error("the sweep removed no books; the deletion was not reconciled")
	}
}

// A scan where nothing moved reports nothing, so the mechanism costs one query
// per swept root and adds no noise to the ordinary path.
func TestScan_quietWhenNothingMoved(t *testing.T) {
	t.Parallel()
	h := newHarness(t, map[string]any{"가만히 있는 책.zip": jpegZIP(t, "01.jpg", "02.jpg")})
	h.run(Request{})
	if got := relocations(h.run(Request{})); len(got) != 0 {
		t.Errorf("an unchanged library reported %+v", got)
	}
}
