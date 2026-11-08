package app

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"shelf/internal/config"
	"shelf/internal/ids"
	"shelf/internal/index"
	"shelf/internal/testutil"
	"shelf/internal/userdata"
)

// These tests drive the post-scan cleanup the way the product does: a real
// index, a real user.db, a real scan over a temporary library, and the
// AfterScan hook app.New wires. A unit test of one planner cannot see a step
// that was never called, and that is how E-69 §2.12 shipped — every function
// that moves progress was tested, and none of them was ever handed a prefs row.
//
// The library is mutated with os.Rename / os.Remove between scans. That is
// allowed here (this is not a media-reading package, and the tree is a
// t.TempDir) and it is what a reader does with a file manager. A rename keeps
// size and mtime, so content_version — and with it the relocation proof — is
// preserved exactly as it is on a real disk.

var cleanupMtime = time.Unix(1_700_000_000, 0).UTC()

// cleanupLibrary is two series of ZIPs plus one loose ZIP. Every book has a
// distinct page count so that a row landing on the wrong book is visible as a
// wrong number, and distinct bytes so no two share a content_version (a shared
// one is declined by design, E-49 §3).
func cleanupLibrary(t *testing.T) string {
	t.Helper()
	zip := func(n int) testutil.File {
		entries := make([]testutil.Entry, n)
		for i := range entries {
			entries[i] = testutil.Entry{
				Name: fmt.Sprintf("%03d.jpg", i+1),
				Data: testutil.TinyJPEG(t, 8+n, 12),
			}
		}
		return testutil.File{Data: testutil.BuildZIP(t, testutil.ZIPSpec{Entries: entries}), ModTime: cleanupMtime}
	}
	return testutil.BuildTree(t, map[string]any{
		"옮길 시리즈": map[string]any{
			"01권.zip": zip(3),
			"02권.zip": zip(4),
		},
		"남을 시리즈": map[string]any{
			"01권.zip": zip(5),
		},
		"낱권.zip": zip(6),
	})
}

// bookAt is the one book the index holds at rel, failing the test otherwise.
func bookAt(t *testing.T, a *App, rel string) index.SplitVolume {
	t.Helper()
	loc := index.Location{RootName: "fx", RelPath: rel}
	got, err := a.idx.BooksAt(t.Context(), []index.Location{loc})
	if err != nil {
		t.Fatalf("BooksAt %q: %v", rel, err)
	}
	if len(got[loc]) != 1 {
		t.Fatalf("index holds %d books at %q, want 1", len(got[loc]), rel)
	}
	return got[loc][0]
}

func noBookAt(t *testing.T, a *App, rel string) {
	t.Helper()
	loc := index.Location{RootName: "fx", RelPath: rel}
	got, err := a.idx.BooksAt(t.Context(), []index.Location{loc})
	if err != nil {
		t.Fatalf("BooksAt %q: %v", rel, err)
	}
	if len(got[loc]) != 0 {
		t.Fatalf("index still holds %q after the scan; the fixture did not move it", rel)
	}
}

func setDir(t *testing.T, a *App, bookID, dir string) {
	t.Helper()
	if _, err := a.user.PutPrefs(t.Context(), bookID, userdata.PrefsPatch{
		ReadingDir: userdata.SetPatch(dir),
		FitMode:    userdata.SetPatch("height"),
	}); err != nil {
		t.Fatalf("PutPrefs %s: %v", bookID, err)
	}
}

func readTo(t *testing.T, a *App, b index.SplitVolume, rel string, page int) {
	t.Helper()
	if _, err := a.user.PutProgress(t.Context(), userdata.ProgressUpdate{
		BookID: b.BookID, SeriesID: b.SeriesID, RootName: "fx", BookPath: rel,
		Page: page, PageCount: b.PageCount,
	}); err != nil {
		t.Fatalf("PutProgress %s: %v", b.BookID, err)
	}
}

// prefsRow reports whether book_prefs has a row for id, and its direction.
func prefsRow(t *testing.T, a *App, id string) (string, bool) {
	t.Helper()
	m, err := a.user.GetPrefsMany(t.Context(), []string{id})
	if err != nil {
		t.Fatalf("GetPrefsMany: %v", err)
	}
	p, ok := m[id]
	if !ok {
		return "", false
	}
	if p.ReadingDir == nil {
		return "", true
	}
	return *p.ReadingDir, true
}

func progressRow(t *testing.T, a *App, id string) (userdata.Progress, bool) {
	t.Helper()
	m, err := a.user.GetProgressMany(t.Context(), []string{id})
	if err != nil {
		t.Fatalf("GetProgressMany: %v", err)
	}
	p, ok := m[id]
	return p, ok
}

func mustRename(t *testing.T, media, from, to string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(media, to)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(media, from), filepath.Join(media, to)); err != nil {
		t.Fatalf("renaming %q to %q: %v", from, to, err)
	}
}

func mustRemove(t *testing.T, media, rel string) {
	t.Helper()
	if err := os.RemoveAll(filepath.Join(media, rel)); err != nil {
		t.Fatalf("removing %q: %v", rel, err)
	}
}

// ---------------------------------------------------------------------------
// E-69 §2.12 — the shipped defect, reproduced end to end
// ---------------------------------------------------------------------------

// A book the reader set a direction on but never turned a page of has a
// book_prefs row and no progress row. The relocation carry used to look up
// progress for the old ids and stop there, so the prefs stayed on an id that is
// no longer any book.
func TestAfterScan_prefsFollowAMovedBookThatWasNeverRead(t *testing.T) {
	media := cleanupLibrary(t)
	a := newTestApp(t, testConfig(t, media), false)
	scanNow(t, a, false)

	old := bookAt(t, a, "낱권.zip")
	setDir(t, a, old.BookID, "rtl")

	mustRename(t, media, "낱권.zip", "보관함/낱권 (완).zip")
	scanNow(t, a, false)
	noBookAt(t, a, "낱권.zip")
	moved := bookAt(t, a, "보관함/낱권 (완).zip")

	if dir, ok := prefsRow(t, a, moved.BookID); !ok || dir != "rtl" {
		t.Errorf("moved book has prefs (%q, %v), want the reader's rtl to have followed it", dir, ok)
	}
	if _, ok := prefsRow(t, a, old.BookID); ok {
		t.Errorf("the old id %s still has a prefs row; the carry must retire it", old.BookID)
	}
}

// A book with no progress and no cover whose file is gone from a root this scan
// walked end to end. book_prefs has no root column, so nothing proves which
// root the row came from, and E-69 §2.11(d) (revised after the U1 review)
// keeps it: "progress도 표지도 없는 book_prefs 단독 행은 … 지우지 않는다". The
// row is harmless, and a book that comes back at the same path picks it up.
func TestAfterScan_keepsAPrefsOnlyRowEvenAfterAFullWalk(t *testing.T) {
	media := cleanupLibrary(t)
	a := newTestApp(t, testConfig(t, media), false)
	scanNow(t, a, false)

	gone := bookAt(t, a, "낱권.zip")
	setDir(t, a, gone.BookID, "rtl")

	mustRemove(t, media, "낱권.zip")
	scanNow(t, a, false)
	noBookAt(t, a, "낱권.zip")

	if _, ok := prefsRow(t, a, gone.BookID); !ok {
		t.Errorf("prefs-only row for %s was deleted; nothing proves which root it came from", gone.BookID)
	}
}

// The ids the purge collects come from the tables that do say where a book
// was — progress and book_covers — and the book_prefs of those ids go with
// them. Here there is no progress at all: the cover places the book in the
// walked root, so the cover and the prefs are both deleted (§2.11(d), §2.12).
func TestAfterScan_purgesTheCoverAndPrefsOfAVanishedBookThatWasNeverRead(t *testing.T) {
	media := cleanupLibrary(t)
	a := newTestApp(t, testConfig(t, media), false)
	scanNow(t, a, false)

	gone := bookAt(t, a, "낱권.zip")
	setDir(t, a, gone.BookID, "rtl")
	chooseCover(t, a, gone, "낱권.zip", 2)

	mustRemove(t, media, "낱권.zip")
	scanNow(t, a, false)

	if _, ok := coverRow(t, a, gone.BookID); ok {
		t.Errorf("cover row for the deleted book survived a completed walk of its root")
	}
	if _, ok := prefsRow(t, a, gone.BookID); ok {
		t.Errorf("prefs row placed by the book's cover survived it")
	}
}

// The case that made "every root the index knows was walked" unsound (U1 codex
// cycle 1, finding 1): root A is taken out of the configuration — which A-11
// says is non-destructive, adding the directory back reattaches everything —
// and the index is rebuilt, so the index no longer knows A at all. Walking the
// one remaining root end to end then proves nothing about A's books.
func TestAfterScan_keepsPrefsOfARootThatLeftTheConfigurationAcrossARebuild(t *testing.T) {
	cfg := testConfig(t, cleanupLibrary(t))
	cfg.Roots = append(cfg.Roots, config.Root{Name: "other", Label: "other", Path: cleanupLibrary(t), Enabled: true})
	first := newTestApp(t, cfg, false)
	scanNow(t, first, false)
	b := bookAt(t, first, "낱권.zip") // in fx
	setDir(t, first, b.BookID, "rtl")
	if err := first.Close(); err != nil {
		t.Fatalf("closing: %v", err)
	}

	withoutFx := *cfg
	withoutFx.Roots = []config.Root{cfg.Roots[1]}
	second := newTestApp(t, &withoutFx, true /* --rebuild-index */)
	res := scanNow(t, second, false)
	if len(res.Roots) != 1 || res.Roots[0].Name != "other" || res.Roots[0].Err != nil || res.Roots[0].SweepNote != "" {
		t.Fatalf("fixture: the scan did not walk only root other end to end: %+v", res.Roots)
	}

	if dir, ok := prefsRow(t, second, b.BookID); !ok || dir != "rtl" {
		t.Errorf("prefs of a book in the removed root fx = (%q, %v), want rtl kept", dir, ok)
	}
}

// injectUserDBFailure makes every INSERT into table fail, from a second
// connection to the app's own user.db: the narrowest real failure — the write
// reaches SQLite and is refused there, so the transaction rolls back exactly as
// it would on a full disk, with no seam in the product code.
func injectUserDBFailure(t *testing.T, cfg *config.Config, table string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(cfg.Storage.DataDir, "user.db")+
		"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("opening user.db: %v", err)
	}
	defer db.Close()
	if _, err := db.ExecContext(t.Context(), `CREATE TRIGGER inject_failure_`+table+
		` BEFORE INSERT ON `+table+` BEGIN SELECT RAISE(ABORT, 'injected by the test'); END`); err != nil {
		t.Fatalf("creating the failure trigger: %v", err)
	}
}

// U1 codex cycle 1, finding 2: the carry of a rename fails (the write is
// refused and rolls back), and the purge of the same run must not then delete
// the originals as "vanished" — they were explained, the explanation just did
// not land. Without the guard, one transient write error destroys the book's
// progress, cover and prefs together.
func TestAfterScan_aFailedCarrySkipsThePurgeOfTheSameRun(t *testing.T) {
	media := cleanupLibrary(t)
	cfg := testConfig(t, media)
	a := newTestApp(t, cfg, false)
	scanNow(t, a, false)

	old := bookAt(t, a, "낱권.zip")
	readTo(t, a, old, "낱권.zip", 4)
	setDir(t, a, old.BookID, "rtl")
	chooseCover(t, a, old, "낱권.zip", 2)
	injectUserDBFailure(t, cfg, "progress")

	mustRename(t, media, "낱권.zip", "보관함/낱권 (완).zip")
	scanNow(t, a, false)
	moved := bookAt(t, a, "보관함/낱권 (완).zip")
	if _, ok := coverRow(t, a, moved.BookID); ok {
		t.Fatalf("fixture: the carry landed; the injected failure did not reach it")
	}

	if p, ok := progressRow(t, a, old.BookID); !ok || p.LastPage != 4 {
		t.Errorf("progress of the moved book = (%+v, %v) after a failed carry, want page 4 kept", p, ok)
	}
	if _, ok := coverRow(t, a, old.BookID); !ok {
		t.Errorf("cover of the moved book was purged after a failed carry")
	}
	if _, ok := prefsRow(t, a, old.BookID); !ok {
		t.Errorf("prefs of the moved book were purged after a failed carry")
	}
}

// U1 codex cycle 1, finding 3, end to end: a progress row whose baseline is
// older than the file (the file changed length after the reader last stopped)
// refuses the progress carry — and only the progress carry. The move is proved
// by content_version, which is §2.11(a)'s one gate, so the cover (its own stale
// baseline untouched, §2.5) and the prefs follow it.
func TestAfterScan_aRefusedProgressCarryDoesNotHoldBackTheCoverAndPrefs(t *testing.T) {
	media := cleanupLibrary(t)
	a := newTestApp(t, testConfig(t, media), false)
	scanNow(t, a, false)

	old := bookAt(t, a, "낱권.zip")
	stale := old
	stale.PageCount = old.PageCount + 3
	readTo(t, a, stale, "낱권.zip", 2)
	setDir(t, a, old.BookID, "rtl")
	want := chooseCover(t, a, stale, "낱권.zip", 8)

	mustRename(t, media, "낱권.zip", "보관함/낱권 (완).zip")
	scanNow(t, a, false)
	moved := bookAt(t, a, "보관함/낱권 (완).zip")

	if _, ok := progressRow(t, a, moved.BookID); ok {
		t.Errorf("progress was carried across a pairing its baseline refuses (the existing PlanRelocations rule)")
	}
	got, ok := coverRow(t, a, moved.BookID)
	if !ok {
		t.Fatalf("the cover did not follow a proven move because the progress row refused it")
	}
	if got.Page != want.Page || got.PageCount != want.PageCount || got.BookPath != "보관함/낱권 (완).zip" {
		t.Errorf("carried cover %+v, want page %d of %d (as stored) under the new path", got, want.Page, want.PageCount)
	}
	if dir, ok := prefsRow(t, a, moved.BookID); !ok || dir != "rtl" {
		t.Errorf("prefs on the moved book = (%q, %v), want rtl", dir, ok)
	}
}

// The case that looked fixed: progress follows a rename, and the shelf shows
// the right percentage — but the direction, display and fit the reader chose
// went back to the defaults, because only progress was carried.
func TestAfterScan_prefsFollowAMovedBookAlongWithItsProgress(t *testing.T) {
	media := cleanupLibrary(t)
	a := newTestApp(t, testConfig(t, media), false)
	scanNow(t, a, false)

	old := bookAt(t, a, "낱권.zip")
	readTo(t, a, old, "낱권.zip", 4)
	setDir(t, a, old.BookID, "rtl")

	mustRename(t, media, "낱권.zip", "낱권 (완).zip")
	scanNow(t, a, false)
	moved := bookAt(t, a, "낱권 (완).zip")

	if p, ok := progressRow(t, a, moved.BookID); !ok || p.LastPage != 4 {
		t.Fatalf("progress on the moved book = (%+v, %v), want page 4 (the existing carry)", p, ok)
	}
	if dir, ok := prefsRow(t, a, moved.BookID); !ok || dir != "rtl" {
		t.Errorf("moved book has prefs (%q, %v), want rtl carried with the progress", dir, ok)
	}
	if _, ok := prefsRow(t, a, old.BookID); ok {
		t.Errorf("the old id %s still has a prefs row", old.BookID)
	}
}

// ---------------------------------------------------------------------------
// E-69 §2.11 — book_covers and series_prefs through the same cleanup
// ---------------------------------------------------------------------------

func chooseCover(t *testing.T, a *App, b index.SplitVolume, rel string, page int) userdata.Cover {
	t.Helper()
	c, err := a.user.PutCover(t.Context(), userdata.CoverPut{
		BookID: b.BookID, SeriesID: b.SeriesID, RootName: "fx", BookPath: rel,
		PageCount: b.PageCount,
		Region:    userdata.CoverRegion{Page: page, X: 0.25, Y: 0.1, W: 0.5, H: 0.75},
	})
	if err != nil {
		t.Fatalf("PutCover %s: %v", b.BookID, err)
	}
	return c
}

func coverRow(t *testing.T, a *App, id string) (userdata.Cover, bool) {
	t.Helper()
	c, ok, err := a.user.GetCover(t.Context(), id)
	if err != nil {
		t.Fatalf("GetCover: %v", err)
	}
	return c, ok
}

func seriesDir(t *testing.T, a *App, sid string) (userdata.SeriesPrefs, bool) {
	t.Helper()
	m, err := a.user.GetSeriesPrefsMany(t.Context(), []string{sid})
	if err != nil {
		t.Fatalf("GetSeriesPrefsMany: %v", err)
	}
	p, ok := m[sid]
	return p, ok
}

func chooseSeriesDir(t *testing.T, a *App, sid, path, dir string) {
	t.Helper()
	if _, err := a.user.PutSeriesPrefs(t.Context(), userdata.SeriesPrefsPut{
		SeriesID: sid, RootName: "fx", SeriesPath: path, ReadingDir: &dir,
	}); err != nil {
		t.Fatalf("PutSeriesPrefs %s: %v", sid, err)
	}
}

// §2.11(a): a chosen cover follows a proven move with its page and region
// unchanged and its identity columns rewritten — and, like prefs, whether or
// not the book was ever read.
func TestAfterScan_coverFollowsAMovedBook(t *testing.T) {
	media := cleanupLibrary(t)
	a := newTestApp(t, testConfig(t, media), false)
	scanNow(t, a, false)

	old := bookAt(t, a, "낱권.zip")
	want := chooseCover(t, a, old, "낱권.zip", 5)

	mustRename(t, media, "낱권.zip", "보관함/낱권 (완).zip")
	scanNow(t, a, false)
	moved := bookAt(t, a, "보관함/낱권 (완).zip")

	got, ok := coverRow(t, a, moved.BookID)
	if !ok {
		t.Fatalf("the moved book has no cover row; the choice did not follow it")
	}
	if got.Page != 5 || got.PageCount != want.PageCount ||
		got.X != want.X || got.Y != want.Y || got.W != want.W || got.H != want.H {
		t.Errorf("carried cover %+v, want page 5 and the region of %+v unchanged", got, want)
	}
	if got.SeriesID != moved.SeriesID || got.BookPath != "보관함/낱권 (완).zip" || got.RootName != "fx" {
		t.Errorf("carried cover filed as (%q, %q, %q), want the destination's (%q, %q, fx)",
			got.SeriesID, got.BookPath, got.RootName, moved.SeriesID, "보관함/낱권 (완).zip")
	}
	if got.UpdatedAt != want.UpdatedAt {
		t.Errorf("updated_at moved %d -> %d; a carry is not something the reader did", want.UpdatedAt, got.UpdatedAt)
	}
	if _, ok := coverRow(t, a, old.BookID); ok {
		t.Errorf("the old id still has a cover row")
	}
}

// §2.11(d): the purge collects ids from book_covers too, so a chosen cover on a
// book nobody read is deleted with the book.
func TestAfterScan_purgesTheCoverOfAVanishedBook(t *testing.T) {
	media := cleanupLibrary(t)
	a := newTestApp(t, testConfig(t, media), false)
	scanNow(t, a, false)

	gone := bookAt(t, a, "낱권.zip")
	chooseCover(t, a, gone, "낱권.zip", 2)
	keep := bookAt(t, a, "남을 시리즈/01권.zip")
	chooseCover(t, a, keep, "남을 시리즈/01권.zip", 1)

	mustRemove(t, media, "낱권.zip")
	scanNow(t, a, false)

	if _, ok := coverRow(t, a, gone.BookID); ok {
		t.Errorf("cover row for the deleted book survived a completed walk of its root")
	}
	if _, ok := coverRow(t, a, keep.BookID); !ok {
		t.Errorf("the purge took the cover of a book that is still there")
	}
}

// §2.11(e): renaming a series directory moves every book of it to one new
// series, and the old series is gone from the index — the series-level 1:1.
func TestAfterScan_seriesPrefsFollowARenamedSeries(t *testing.T) {
	media := cleanupLibrary(t)
	a := newTestApp(t, testConfig(t, media), false)
	scanNow(t, a, false)

	oldSID := bookAt(t, a, "옮길 시리즈/01권.zip").SeriesID
	chooseSeriesDir(t, a, oldSID, "옮길 시리즈", "rtl")
	before, _ := seriesDir(t, a, oldSID)

	mustRename(t, media, "옮길 시리즈", "옮긴 시리즈")
	scanNow(t, a, false)
	newSID := bookAt(t, a, "옮긴 시리즈/01권.zip").SeriesID
	if newSID == oldSID {
		t.Fatalf("the fixture did not change the series id")
	}

	got, ok := seriesDir(t, a, newSID)
	if !ok || got.ReadingDir == nil || *got.ReadingDir != "rtl" {
		t.Fatalf("new series prefs (%+v, %v), want rtl carried from the renamed series", got, ok)
	}
	if got.SeriesPath != "옮긴 시리즈" || got.RootName != "fx" || got.UpdatedAt != before.UpdatedAt {
		t.Errorf("carried series prefs %+v, want path 옮긴 시리즈 in fx and updated_at %d", got, before.UpdatedAt)
	}
	if _, ok := seriesDir(t, a, oldSID); ok {
		t.Errorf("the old series id still has a prefs row")
	}
}

// §2.11(e), the refusal: the books of one series went to two different series.
// There is no single destination to give the direction to, so it is not
// moved — and, because the old series is gone from a root walked end to end,
// it is deleted with the rest of what that series was.
func TestAfterScan_seriesPrefsDoNotFollowASeriesThatSplitApart(t *testing.T) {
	media := cleanupLibrary(t)
	a := newTestApp(t, testConfig(t, media), false)
	scanNow(t, a, false)

	oldSID := bookAt(t, a, "옮길 시리즈/01권.zip").SeriesID
	keepSID := bookAt(t, a, "남을 시리즈/01권.zip").SeriesID
	chooseSeriesDir(t, a, oldSID, "옮길 시리즈", "rtl")

	mustRename(t, media, "옮길 시리즈/01권.zip", "남을 시리즈/00권.zip")
	mustRename(t, media, "옮길 시리즈/02권.zip", "새 시리즈/02권.zip")
	mustRemove(t, media, "옮길 시리즈")
	scanNow(t, a, false)
	newSID := bookAt(t, a, "새 시리즈/02권.zip").SeriesID

	for _, sid := range []string{keepSID, newSID} {
		if p, ok := seriesDir(t, a, sid); ok {
			t.Errorf("series %s received prefs %+v from a series whose books went two ways", sid, p)
		}
	}
	if _, ok := seriesDir(t, a, oldSID); ok {
		t.Errorf("the vanished series' prefs survived a completed walk of its root")
	}
}

// §2.11(e): a series that is still in the index has not moved, however many
// of its books did. Its prefs stay where they are.
func TestAfterScan_seriesPrefsStayWithASeriesThatIsStillThere(t *testing.T) {
	media := cleanupLibrary(t)
	a := newTestApp(t, testConfig(t, media), false)
	scanNow(t, a, false)

	sid := bookAt(t, a, "옮길 시리즈/01권.zip").SeriesID
	chooseSeriesDir(t, a, sid, "옮길 시리즈", "rtl")

	mustRename(t, media, "옮길 시리즈/01권.zip", "새 시리즈/01권.zip")
	scanNow(t, a, false)
	newSID := bookAt(t, a, "새 시리즈/01권.zip").SeriesID

	if _, ok := seriesDir(t, a, sid); !ok {
		t.Errorf("prefs of a series that is still indexed were moved or deleted")
	}
	if _, ok := seriesDir(t, a, newSID); ok {
		t.Errorf("a series received prefs from one that has not gone anywhere")
	}
}

// E-50 §2: nothing has been walked at start-up, so start-up moves and deletes
// nothing. --rebuild-index is the sharpest form of that: the index is empty, so
// every user.db row names an id it does not have, and a purge or a guess run at
// this point would take all of them.
func TestApp_startup_neitherCarriesNorPurgesTheNewTables(t *testing.T) {
	media := cleanupLibrary(t)
	cfg := testConfig(t, media)
	first := newTestApp(t, cfg, false)
	scanNow(t, first, false)

	b := bookAt(t, first, "낱권.zip")
	setDir(t, first, b.BookID, "rtl")
	chooseCover(t, first, b, "낱권.zip", 2)
	sid := bookAt(t, first, "옮길 시리즈/01권.zip").SeriesID
	chooseSeriesDir(t, first, sid, "옮길 시리즈", "ltr")
	if err := first.Close(); err != nil {
		t.Fatalf("closing: %v", err)
	}

	second := newTestApp(t, cfg, true /* --rebuild-index */)
	if _, ok := prefsRow(t, second, b.BookID); !ok {
		t.Errorf("start-up deleted a prefs row")
	}
	if c, ok := coverRow(t, second, b.BookID); !ok || c.Page != 2 {
		t.Errorf("start-up changed or deleted a cover row: (%+v, %v)", c, ok)
	}
	if _, ok := seriesDir(t, second, sid); !ok {
		t.Errorf("start-up deleted a series prefs row")
	}
}

// §2.11(b) and §2.12 through the wiring: rows named after a path the library
// no longer has — here a `[만화] ` tag stripped before relocations were
// observed, the one-volume case of the split arithmetic — are reattached at
// start-up (step 5a). The cover passes the gates on its own baseline and is
// filed under the destination it was reattached to; the prefs are COPIED
// because the progress row passed them, and the container's row stays (§2.12:
// "원본은 남긴다" — this runs at start-up, where nothing is deleted, E-50 §2).
func TestApp_startup_reattachesAnOrphanedCoverAndPrefs(t *testing.T) {
	media := cleanupLibrary(t)
	cfg := testConfig(t, media)
	first := newTestApp(t, cfg, false)
	scanNow(t, first, false)
	b := bookAt(t, first, "낱권.zip")

	const tagged = "[만화] 낱권.zip"
	orphan := index.SplitVolume{BookID: ids.BookID("fx", tagged), SeriesID: "old-series", PageCount: b.PageCount}
	readTo(t, first, orphan, tagged, 2)
	setDir(t, first, orphan.BookID, "rtl")
	chooseCover(t, first, orphan, tagged, 4)
	if err := first.Close(); err != nil {
		t.Fatalf("closing: %v", err)
	}

	second := newTestApp(t, cfg, false) // step 5a runs in New
	if p, ok := progressRow(t, second, b.BookID); !ok || p.LastPage != 2 {
		t.Fatalf("progress (%+v, %v), want page 2 reattached (the existing repair)", p, ok)
	}
	c, ok := coverRow(t, second, b.BookID)
	if !ok || c.Page != 4 || c.PageCount != b.PageCount || c.SeriesID != b.SeriesID {
		t.Errorf("cover (%+v, %v), want page 4 of %d under %s", c, ok, b.PageCount, b.SeriesID)
	}
	if c.RootName != "fx" || c.BookPath != "낱권.zip" {
		t.Errorf("cover filed as (%q, %q), want the destination it was reattached to (fx, 낱권.zip)",
			c.RootName, c.BookPath)
	}
	if dir, ok := prefsRow(t, second, b.BookID); !ok || dir != "rtl" {
		t.Errorf("prefs (%q, %v), want rtl copied onto the volume", dir, ok)
	}
	if _, ok := coverRow(t, second, orphan.BookID); ok {
		t.Errorf("the orphan's cover row was not retired")
	}
	if dir, ok := prefsRow(t, second, orphan.BookID); !ok || dir != "rtl" {
		t.Errorf("the orphan's prefs row = (%q, %v), want it kept: a split copies prefs, it does not move them", dir, ok)
	}
}

// §2.11(c): a cover row filed under a series its book is not in is refiled.
func TestAfterScan_refilesAMisfiledCover(t *testing.T) {
	media := cleanupLibrary(t)
	a := newTestApp(t, testConfig(t, media), false)
	scanNow(t, a, false)
	b := bookAt(t, a, "낱권.zip")
	wrong := b
	wrong.SeriesID = "not-its-series"
	before := chooseCover(t, a, wrong, "낱권.zip", 3)

	scanNow(t, a, false)
	got, ok := coverRow(t, a, b.BookID)
	if !ok || got.SeriesID != b.SeriesID {
		t.Fatalf("cover (%+v, %v), want it refiled under %s", got, ok, b.SeriesID)
	}
	if got.UpdatedAt != before.UpdatedAt {
		t.Errorf("updated_at moved %d -> %d; a refile is not something the reader did", before.UpdatedAt, got.UpdatedAt)
	}
}

// The other half of the prefs purge rule. book_prefs has no root column, so a
// prefs row with no progress or cover beside it cannot say where its book was,
// and it stays — here one filed under a root this run did not walk. A prefs row
// that shares its id with a progress row in the walked root does go.
func TestAfterScan_keepsUnrootedPrefsWhileSomeRootWasNotWalked(t *testing.T) {
	media := cleanupLibrary(t)
	a := newTestApp(t, testConfig(t, media), false)
	if err := a.idx.UpsertRoot(t.Context(), index.Root{Name: "unplugged", Path: "/nowhere", Enabled: true}); err != nil {
		t.Fatalf("seeding a root outside the configuration: %v", err)
	}
	scanNow(t, a, false)

	elsewhere := ids.BookID("unplugged", "어딘가/책.zip")
	setDir(t, a, elsewhere, "rtl")
	gone := bookAt(t, a, "낱권.zip")
	readTo(t, a, gone, "낱권.zip", 2)
	setDir(t, a, gone.BookID, "rtl")

	mustRemove(t, media, "낱권.zip")
	scanNow(t, a, false)

	if _, ok := prefsRow(t, a, elsewhere); !ok {
		t.Errorf("prefs of a book that may be on an unwalked root were deleted")
	}
	// Placed by its progress row in the walked root, the other one does go.
	if _, ok := prefsRow(t, a, gone.BookID); ok {
		t.Errorf("prefs placed by a vanished progress row survived")
	}
}

// U1 codex cycle 2, finding B1. A split reattachment across roots leaves one
// book id with two sources: the progress row keeps the orphan's root and path
// (repair.row, unchanged), while the cover is filed under the destination it
// was reattached to. Take the other root out of the configuration, rebuild the
// index and walk only the remaining root, and one table says "vanished from a
// walked root" while the other names a root nobody walked. Deleting the id on
// the first table's word took the second table's row with it; an id is
// deleted only when every row it has names a root this run walked end to end
// (§2.11(d), E-50 §1).
//
// Both directions: the progress row names the walked root and the cover the
// removed one, and the reverse (the id then reaches the purge through
// VanishedCovers alone, and the destination file is gone too).
func TestAfterScan_keepsAVanishedIDWhoseOtherRowNamesARootThatWasNotWalked(t *testing.T) {
	for _, tc := range []struct {
		name, orphanRoot, destRoot, path string
	}{
		{"progress names the walked root, cover the removed one", "fx", "other", "다른 곳에만.zip"},
		{"cover names the walked root, progress the removed one", "other", "fx", "낱권.zip"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			other := testutil.BuildTree(t, map[string]any{
				"다른 곳에만.zip": testutil.File{
					Data: testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
						{Name: "001.jpg", Data: testutil.TinyJPEG(t, 20, 12)},
						{Name: "002.jpg", Data: testutil.TinyJPEG(t, 21, 12)},
						{Name: "003.jpg", Data: testutil.TinyJPEG(t, 22, 12)},
						{Name: "004.jpg", Data: testutil.TinyJPEG(t, 23, 12)},
						{Name: "005.jpg", Data: testutil.TinyJPEG(t, 24, 12)},
						{Name: "006.jpg", Data: testutil.TinyJPEG(t, 25, 12)},
						{Name: "007.jpg", Data: testutil.TinyJPEG(t, 26, 12)},
					}}),
					ModTime: cleanupMtime,
				},
			})
			media := cleanupLibrary(t)
			cfg := testConfig(t, media)
			cfg.Roots = append(cfg.Roots, config.Root{Name: "other", Label: "other", Path: other, Enabled: true})
			first := newTestApp(t, cfg, false)
			scanNow(t, first, false)

			loc := index.Location{RootName: tc.destRoot, RelPath: tc.path}
			found, err := first.idx.BooksAt(t.Context(), []index.Location{loc})
			if err != nil || len(found[loc]) != 1 {
				t.Fatalf("fixture: index holds %v at %v (%v), want one book", found[loc], loc, err)
			}
			dest := found[loc][0]
			if _, ok := found[index.Location{RootName: tc.orphanRoot, RelPath: tc.path}]; ok {
				t.Fatalf("fixture: the orphan's own root has %q, so the reattachment would be ambiguous", tc.path)
			}

			// A book that was at tc.path in orphanRoot, read and given a cover
			// there, and is now only at the same path in destRoot.
			orphan := ids.BookID(tc.orphanRoot, tc.path)
			if _, err := first.user.PutProgress(t.Context(), userdata.ProgressUpdate{
				BookID: orphan, SeriesID: "old-series", RootName: tc.orphanRoot, BookPath: tc.path,
				Page: 2, PageCount: dest.PageCount,
			}); err != nil {
				t.Fatalf("PutProgress: %v", err)
			}
			if _, err := first.user.PutCover(t.Context(), userdata.CoverPut{
				BookID: orphan, SeriesID: "old-series", RootName: tc.orphanRoot, BookPath: tc.path,
				PageCount: dest.PageCount,
				Region:    userdata.CoverRegion{Page: 3, X: 0.25, Y: 0.1, W: 0.5, H: 0.75},
			}); err != nil {
				t.Fatalf("PutCover: %v", err)
			}
			setDir(t, first, orphan, "rtl")
			if err := first.Close(); err != nil {
				t.Fatalf("closing: %v", err)
			}

			// Start-up step 5a reattaches all three onto the destination.
			second := newTestApp(t, cfg, false)
			p, ok := progressRow(t, second, dest.BookID)
			if !ok || p.RootName != tc.destRoot {
				t.Fatalf("fixture: progress on the destination = (%+v, %v), want it reattached under %s", p, ok, tc.destRoot)
			}
			c, ok := coverRow(t, second, dest.BookID)
			if !ok || c.RootName != tc.destRoot {
				t.Fatalf("fixture: cover on the destination = (%+v, %v), want it reattached under %s", c, ok, tc.destRoot)
			}
			if err := second.Close(); err != nil {
				t.Fatalf("closing: %v", err)
			}

			// Legacy rows may disagree about their source; keep the union guard covered.
			raw, err := sql.Open("sqlite", "file:"+filepath.Join(cfg.Storage.DataDir, "user.db"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := raw.ExecContext(t.Context(), `UPDATE progress SET root_name=? WHERE book_id=?`, tc.orphanRoot, dest.BookID); err != nil {
				t.Fatal(err)
			}
			if err := raw.Close(); err != nil {
				t.Fatal(err)
			}

			// fx stays and other leaves, so exactly one of the two rows names
			// the walked root. When the destination is in fx its file goes
			// too, so either way the id is not in the rebuilt index.
			if tc.destRoot == "fx" {
				mustRemove(t, media, tc.path)
			}
			onlyFx := *cfg
			onlyFx.Roots = []config.Root{cfg.Roots[0]}
			third := newTestApp(t, &onlyFx, true /* --rebuild-index */)
			res := scanNow(t, third, false)
			if len(res.Roots) != 1 || res.Roots[0].Name != "fx" || res.Roots[0].Err != nil || res.Roots[0].SweepNote != "" {
				t.Fatalf("fixture: the scan did not walk only root fx end to end: %+v", res.Roots)
			}

			if got, ok := coverRow(t, third, dest.BookID); !ok || got.RootName != tc.destRoot {
				t.Errorf("cover filed under %s = (%+v, %v), want it kept: root other was not walked", tc.destRoot, got, ok)
			}
			if got, ok := progressRow(t, third, dest.BookID); !ok || got.LastPage != 2 {
				t.Errorf("progress filed under %s = (%+v, %v), want it kept with its id", tc.orphanRoot, got, ok)
			}
			if dir, ok := prefsRow(t, third, dest.BookID); !ok || dir != "rtl" {
				t.Errorf("prefs = (%q, %v), want rtl kept with its id", dir, ok)
			}
		})
	}
}

func TestAfterScan_failedCarryRetriesOnNextScan(t *testing.T) {
	for _, scenario := range []struct{ restart, failPending bool }{{false, false}, {true, false}, {false, true}} {
		restart := scenario.restart
		t.Run(fmt.Sprintf("restart=%v, pendingFailure=%v", restart, scenario.failPending), func(t *testing.T) {
			media := cleanupLibrary(t)
			cfg := testConfig(t, media)
			a := newTestApp(t, cfg, false)
			scanNow(t, a, false)
			old := bookAt(t, a, "낱권.zip")
			readTo(t, a, old, "낱권.zip", 4)
			setDir(t, a, old.BookID, "rtl")
			chooseCover(t, a, old, "낱권.zip", 2)
			injectUserDBFailure(t, cfg, "progress")
			if scenario.failPending {
				injectUserDBFailure(t, cfg, "pending_relocations")
			}
			mustRename(t, media, "낱권.zip", "보관함/낱권 (완).zip")
			scanNow(t, a, false)
			moved := bookAt(t, a, "보관함/낱권 (완).zip")
			if _, ok := progressRow(t, a, old.BookID); !ok {
				t.Fatal("fixture: first failed scan lost original")
			}
			db, err := sql.Open("sqlite", "file:"+filepath.Join(cfg.Storage.DataDir, "user.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.ExecContext(t.Context(), "DROP TRIGGER inject_failure_progress"); err != nil {
				t.Fatal(err)
			}
			if scenario.failPending {
				if len(a.pendingRelocations) == 0 {
					t.Fatal("no memory fallback after two failed writes")
				}
				if _, err := db.ExecContext(t.Context(), "DROP TRIGGER inject_failure_pending_relocations"); err != nil {
					t.Fatal(err)
				}
			}
			if restart {
				if pairs, err := a.user.PendingRelocations(t.Context()); err != nil || len(pairs) != 1 {
					t.Fatalf("pending evidence=%v, err=%v", pairs, err)
				}
				if err := a.Close(); err != nil {
					t.Fatal(err)
				}
				a = newTestApp(t, cfg, false)
				if _, ok := progressRow(t, a, old.BookID); !ok {
					t.Fatal("startup consumed pending evidence")
				}
			}
			scanNow(t, a, false)
			_, oldP := progressRow(t, a, old.BookID)
			_, newP := progressRow(t, a, moved.BookID)
			_, oldC := coverRow(t, a, old.BookID)
			_, newC := coverRow(t, a, moved.BookID)
			_, oldF := prefsRow(t, a, old.BookID)
			_, newF := prefsRow(t, a, moved.BookID)
			if !newP || !newC || !newF {
				t.Fatalf("after recovery scan: progress old/new=%v/%v cover old/new=%v/%v prefs old/new=%v/%v; expected new rows", oldP, newP, oldC, newC, oldF, newF)
			}
			if pairs, err := a.user.PendingRelocations(t.Context()); err != nil || len(pairs) != 0 {
				t.Fatalf("retired evidence=%v, err=%v", pairs, err)
			}
		})
	}
}

func TestApp_reattachedProgressSurvivesRemovingDestinationRoot(t *testing.T) {
	media := testutil.BuildTree(t, map[string]any{})
	other := cleanupLibrary(t)
	cfg := testConfig(t, media)
	cfg.Roots = append(cfg.Roots, config.Root{Name: "other", Label: "other", Path: other, Enabled: true})
	first := newTestApp(t, cfg, false)
	scanNow(t, first, false)
	loc := index.Location{RootName: "other", RelPath: "낱권.zip"}
	found, err := first.idx.BooksAt(t.Context(), []index.Location{loc})
	if err != nil {
		t.Fatal(err)
	}
	dest := found[loc][0]
	orphan := index.SplitVolume{BookID: ids.BookID("fx", "낱권.zip"), SeriesID: "old-series", PageCount: dest.PageCount}
	readTo(t, first, orphan, "낱권.zip", 4)
	setDir(t, first, orphan.BookID, "rtl")
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second := newTestApp(t, cfg, false)
	if p, ok := progressRow(t, second, dest.BookID); !ok || p.LastPage != 4 {
		t.Fatalf("fixture reattach failed: %+v %v", p, ok)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	onlyFx := *cfg
	onlyFx.Roots = []config.Root{cfg.Roots[0]}
	third := newTestApp(t, &onlyFx, true)
	scanNow(t, third, false)
	_, progress := progressRow(t, third, dest.BookID)
	_, prefs := prefsRow(t, third, dest.BookID)
	if !progress || !prefs {
		t.Fatalf("removed root's untouched book: progress=%v prefs=%v; expected both retained", progress, prefs)
	}
}

func TestAfterScan_movedCoverIsPregeneratedAfterUserDataFollows(t *testing.T) {
	for _, chosen := range []bool{false, true} {
		t.Run(fmt.Sprintf("chosen=%v", chosen), func(t *testing.T) {
			media := cleanupLibrary(t)
			cfg := testConfig(t, media)
			a := newTestApp(t, cfg, false)
			stop := runApp(t, a)
			defer stop()
			scanNow(t, a, false)
			old := bookAt(t, a, "낱권.zip")
			if chosen {
				chooseCover(t, a, old, "낱권.zip", 2)
			} else {
				setDir(t, a, old.BookID, "rtl")
			}
			mustRename(t, media, "낱권.zip", "새폴더/낱권.zip")
			scanNow(t, a, false)
			moved := bookAt(t, a, "새폴더/낱권.zip")
			deadline := time.Now().Add(5 * time.Second)
			for {
				st := a.thumb.Stats()
				if st.CoverDepth == 0 && st.PageDepth == 0 && st.Active == 0 && st.Inflight == 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("thumbnail queue did not drain")
				}
				time.Sleep(10 * time.Millisecond)
			}
			var detail struct {
				CoverCV string `json:"cover_cv"`
			}
			if status := getJSON(t, a, "/api/books/"+moved.BookID, &detail); status != 200 {
				t.Fatalf("detail status=%d", status)
			}
			for _, w := range cfg.Thumbnails.Widths {
				status := getJSON(t, a, fmt.Sprintf("/api/books/%s/cover?w=%d&v=%s", moved.BookID, w, detail.CoverCV), nil)
				if status != 200 {
					t.Errorf("after complete scan and drained queue: current cover width=%d HTTP=%d; want pregenerated 200", w, status)
				}
			}
		})
	}
}
