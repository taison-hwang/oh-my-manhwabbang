package userdata_test

import (
	"testing"
	"time"

	"shelf/internal/userdata"
)

// container is the orphaned row: a progress row naming a book the index no
// longer has, because the container it named was split into volumes.
func container(t *testing.T, db *userdata.DB, bookID string, page, count int) {
	t.Helper()
	if _, err := db.PutProgress(t.Context(), userdata.ProgressUpdate{
		BookID: bookID, SeriesID: "s1", RootName: "manga",
		BookPath: "c.zip", Page: page, PageCount: count,
	}); err != nil {
		t.Fatalf("seeding %q: %v", bookID, err)
	}
}

func volumeRow(bookID string, page, count int, updatedAt int64) userdata.ExportItem {
	return userdata.ExportItem{
		BookID: bookID, SeriesID: "s1", RootName: "manga", BookPath: "c.zip",
		LastPage: page, PageCount: count, StartedAt: updatedAt, UpdatedAt: updatedAt,
	}
}

func TestRepairSplit_movesTheRowAndRetiresTheOrphan(t *testing.T) {
	t.Parallel()
	db, _, _ := newDB(t)
	container(t, db, "old", 11, 30)

	res, err := db.RepairSplit(t.Context(), []userdata.SplitMove{{
		OldBookID: "old",
		Rows:      []userdata.ExportItem{volumeRow("vol2", 1, 20, 500)},
	}})
	if err != nil {
		t.Fatalf("RepairSplit: %v", err)
	}
	if res.Retired != 1 || res.Written != 1 || res.Kept != 0 {
		t.Errorf("result %+v, want 1 retired, 1 written, 0 kept", res)
	}

	if _, err := db.GetProgress(t.Context(), "old"); err == nil {
		t.Error("the orphaned row survived; it can only ever contribute 0 to the rollup")
	}
	got, err := db.GetProgress(t.Context(), "vol2")
	if err != nil {
		t.Fatalf("the destination row was not written: %v", err)
	}
	if got.LastPage != 1 || got.PageCount != 20 {
		t.Errorf("destination at page %d of %d, want page 1 of 20", got.LastPage, got.PageCount)
	}
}

// The orphan is the older record by construction — it was written before the
// split. A reader who has since opened the split volume must not be walked
// backwards to where they were under the old shape.
func TestRepairSplit_keepsANewerLocalRow(t *testing.T) {
	t.Parallel()
	db, clk, _ := newDB(t)
	container(t, db, "old", 19, 260)
	clk.Advance(time.Hour)
	container(t, db, "vol1", 13, 260) // the reader opened the volume after the split

	before, err := db.GetProgress(t.Context(), "vol1")
	if err != nil {
		t.Fatalf("seeding vol1: %v", err)
	}

	res, err := db.RepairSplit(t.Context(), []userdata.SplitMove{{
		OldBookID: "old",
		Rows:      []userdata.ExportItem{volumeRow("vol1", 19, 260, before.UpdatedAt-1)},
	}})
	if err != nil {
		t.Fatalf("RepairSplit: %v", err)
	}
	if res.Kept != 1 || res.Written != 0 {
		t.Errorf("result %+v, want the newer local row kept and nothing written", res)
	}
	after, _ := db.GetProgress(t.Context(), "vol1")
	if after.LastPage != 13 {
		t.Errorf("local row moved to page %d, want it left at 13", after.LastPage)
	}
	// The orphan still goes: keeping it would leave the 0 % in place, which is
	// the whole defect, and the volume it pointed into is already covered.
	if res.Retired != 1 {
		t.Errorf("retired %d, want the orphan retired even though its row was kept", res.Retired)
	}
	if _, err := db.GetProgress(t.Context(), "old"); err == nil {
		t.Error("the orphaned row survived a kept destination")
	}
}

func TestRepairSplit_completedContainerWritesEveryVolume(t *testing.T) {
	t.Parallel()
	db, _, _ := newDB(t)
	container(t, db, "old", 35, 35)

	rows := []userdata.ExportItem{
		volumeRow("v1", 10, 10, 500), volumeRow("v2", 20, 20, 500), volumeRow("v3", 5, 5, 500),
	}
	for i := range rows {
		rows[i].Completed = true
	}
	res, err := db.RepairSplit(t.Context(), []userdata.SplitMove{{OldBookID: "old", Rows: rows}})
	if err != nil {
		t.Fatalf("RepairSplit: %v", err)
	}
	if res.Written != 3 {
		t.Fatalf("wrote %d rows, want one per volume (3)", res.Written)
	}
	for _, id := range []string{"v1", "v2", "v3"} {
		got, err := db.GetProgress(t.Context(), id)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if !got.Completed {
			t.Errorf("%s is not completed; the 완독 scope counts rows, so the series would leave the shelf", id)
		}
	}
}

// started_at is when the reader first opened the book. A volume they had
// already started keeps the earlier of the two beginnings.
func TestRepairSplit_keepsTheEarlierStart(t *testing.T) {
	t.Parallel()
	db, clk, _ := newDB(t)
	container(t, db, "old", 5, 30)
	clk.Advance(time.Hour)
	container(t, db, "vol1", 2, 10)
	local, _ := db.GetProgress(t.Context(), "vol1")

	row := volumeRow("vol1", 5, 10, local.UpdatedAt+1)
	row.StartedAt = local.StartedAt - 3600
	if _, err := db.RepairSplit(t.Context(), []userdata.SplitMove{{
		OldBookID: "old", Rows: []userdata.ExportItem{row},
	}}); err != nil {
		t.Fatalf("RepairSplit: %v", err)
	}
	got, _ := db.GetProgress(t.Context(), "vol1")
	if got.StartedAt != local.StartedAt-3600 {
		t.Errorf("started_at %d, want the earlier %d", got.StartedAt, local.StartedAt-3600)
	}
	if got.LastPage != 5 {
		t.Errorf("last_page %d, want the newer incoming 5", got.LastPage)
	}
}

// A half-moved reading history is worse than an unmoved one: one bad row must
// take the whole repair down and leave user.db exactly as it was.
func TestRepairSplit_isOneTransaction(t *testing.T) {
	t.Parallel()
	db, _, _ := newDB(t)
	container(t, db, "old1", 5, 30)
	container(t, db, "old2", 5, 30)

	_, err := db.RepairSplit(t.Context(), []userdata.SplitMove{
		{OldBookID: "old1", Rows: []userdata.ExportItem{volumeRow("good", 1, 10, 500)}},
		{OldBookID: "old2", Rows: []userdata.ExportItem{volumeRow("", 1, 10, 500)}},
	})
	if err == nil {
		t.Fatal("a row with an empty book id was accepted")
	}
	for _, id := range []string{"old1", "old2"} {
		if _, err := db.GetProgress(t.Context(), id); err != nil {
			t.Errorf("%s was retired by a failed repair: %v", id, err)
		}
	}
	if _, err := db.GetProgress(t.Context(), "good"); err == nil {
		t.Error("a destination row from a failed repair was committed")
	}
}

func TestRepairSplit_emptyInputDoesNothing(t *testing.T) {
	t.Parallel()
	db, _, _ := newDB(t)
	res, err := db.RepairSplit(t.Context(), nil)
	if err != nil {
		t.Fatalf("RepairSplit(nil): %v", err)
	}
	if res != (userdata.RepairResult{}) {
		t.Errorf("result %+v, want the zero value", res)
	}
}

// The refile writes series_id and nothing else. updated_at especially must not
// move: 이어보기 sorts by it, and a correction is not something the reader did.
func TestRefileProgress_movesOnlyTheSeries(t *testing.T) {
	t.Parallel()
	db, _, _ := newDB(t)
	container(t, db, "b1", 5, 30)
	before, err := db.GetProgress(t.Context(), "b1")
	if err != nil {
		t.Fatalf("seeding: %v", err)
	}

	n, err := db.RefileProgress(t.Context(), []userdata.Refile{{BookID: "b1", SeriesID: "s-correct"}})
	if err != nil {
		t.Fatalf("RefileProgress: %v", err)
	}
	if n != 1 {
		t.Errorf("refiled %d rows, want 1", n)
	}
	after, _ := db.GetProgress(t.Context(), "b1")
	if after.SeriesID != "s-correct" {
		t.Errorf("series %q, want s-correct", after.SeriesID)
	}
	if after.LastPage != before.LastPage || after.PageCount != before.PageCount ||
		after.Completed != before.Completed || after.StartedAt != before.StartedAt ||
		after.UpdatedAt != before.UpdatedAt || after.BookPath != before.BookPath {
		t.Errorf("the refile changed more than the series:\n before %+v\n after  %+v", before, after)
	}
}

// A row already filed correctly is not rewritten, so a healthy library costs
// nothing and no timestamp churns.
func TestRefileProgress_noOpWhenAlreadyRight(t *testing.T) {
	t.Parallel()
	db, _, _ := newDB(t)
	container(t, db, "b1", 5, 30)
	n, err := db.RefileProgress(t.Context(), []userdata.Refile{{BookID: "b1", SeriesID: "s1"}})
	if err != nil {
		t.Fatalf("RefileProgress: %v", err)
	}
	if n != 0 {
		t.Errorf("refiled %d rows, want 0 — the row was already right", n)
	}
}

func TestRefileProgress_rejectsEmptyIDs(t *testing.T) {
	t.Parallel()
	db, _, _ := newDB(t)
	container(t, db, "b1", 5, 30)
	if _, err := db.RefileProgress(t.Context(), []userdata.Refile{
		{BookID: "b1", SeriesID: "ok"}, {BookID: "", SeriesID: "x"},
	}); err == nil {
		t.Fatal("an empty book id was accepted")
	}
	got, _ := db.GetProgress(t.Context(), "b1")
	if got.SeriesID != "s1" {
		t.Errorf("series %q; a failed refile must leave the row alone", got.SeriesID)
	}
}

// "관련된 모든 정보" — the reading position and the per-book preferences filed
// under the same id. A reading direction left behind for a book nobody can open
// is the same orphan by another name.
func TestPurgeProgress_removesProgressAndPrefs(t *testing.T) {
	t.Parallel()
	db, _, _ := newDB(t)
	container(t, db, "gone", 5, 30)
	container(t, db, "kept", 2, 30)
	if _, err := db.PutPrefs(t.Context(), "gone",
		userdata.PrefsPatch{ReadingDir: userdata.SetPatch("rtl")}); err != nil {
		t.Fatalf("seeding prefs: %v", err)
	}

	progress, prefs, err := db.PurgeProgress(t.Context(), []string{"gone"})
	if err != nil {
		t.Fatalf("PurgeProgress: %v", err)
	}
	if progress != 1 || prefs != 1 {
		t.Errorf("deleted %d progress and %d prefs, want 1 and 1", progress, prefs)
	}
	if _, err := db.GetProgress(t.Context(), "gone"); err == nil {
		t.Error("the purged row survived")
	}
	if _, err := db.GetProgress(t.Context(), "kept"); err != nil {
		t.Errorf("an untouched row was deleted: %v", err)
	}
}

func TestPurgeProgress_emptyInputDeletesNothing(t *testing.T) {
	t.Parallel()
	db, _, _ := newDB(t)
	container(t, db, "b1", 5, 30)
	p, pr, err := db.PurgeProgress(t.Context(), nil)
	if err != nil || p != 0 || pr != 0 {
		t.Fatalf("got (%d, %d, %v), want (0, 0, nil)", p, pr, err)
	}
	if _, err := db.GetProgress(t.Context(), "b1"); err != nil {
		t.Errorf("a row was deleted by an empty purge: %v", err)
	}
}

// One bad id takes the whole purge down rather than deleting half a list: this
// is the operation that cannot be undone, so a partial one is the worst outcome.
func TestPurgeProgress_isOneTransaction(t *testing.T) {
	t.Parallel()
	db, _, _ := newDB(t)
	container(t, db, "b1", 5, 30)
	container(t, db, "b2", 5, 30)

	if _, _, err := db.PurgeProgress(t.Context(), []string{"b1", "", "b2"}); err == nil {
		t.Fatal("an empty book id was accepted")
	}
	for _, id := range []string{"b1", "b2"} {
		if _, err := db.GetProgress(t.Context(), id); err != nil {
			t.Errorf("%s was deleted by a failed purge: %v", id, err)
		}
	}
}
