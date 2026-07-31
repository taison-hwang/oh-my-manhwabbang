package userdata_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"shelf/internal/userdata"
)

// series_seen — amendment A-8 (ruling E-9). first_seen_at is written once, on
// first sighting, and never again: it is what makes 최근 추가 survive
// --rebuild-index (NFR-DAT-004, AC-005/AC-006).

func seenRow(id string, at int64) userdata.SeriesSeen {
	return userdata.SeriesSeen{
		SeriesID: id, RootName: "manga", SeriesPath: "[만화] " + id, FirstSeenAt: at,
	}
}

func firstSeen(t *testing.T, db *userdata.DB, id string) (int64, bool) {
	t.Helper()
	m, err := db.SeriesFirstSeen(t.Context(), []string{id})
	if err != nil {
		t.Fatalf("SeriesFirstSeen(%q): %v", id, err)
	}
	at, ok := m[id]
	return at, ok
}

func TestMarkSeriesSeen_secondSighting_doesNotMoveTheTimestamp(t *testing.T) {
	t.Parallel()
	db, _, _ := newDB(t)
	ctx := t.Context()

	const t0 = int64(1_700_000_000)
	if err := db.MarkSeriesSeen(ctx, []userdata.SeriesSeen{seenRow("s1", t0)}); err != nil {
		t.Fatalf("first MarkSeriesSeen: %v", err)
	}
	// A later scan, and an earlier one: neither may win. The row is the first
	// sighting, not the most recent and not the smallest.
	for _, at := range []int64{t0 + 86_400*30, t0 - 86_400*30} {
		if err := db.MarkSeriesSeen(ctx, []userdata.SeriesSeen{
			{SeriesID: "s1", RootName: "other", SeriesPath: "moved", FirstSeenAt: at},
		}); err != nil {
			t.Fatalf("MarkSeriesSeen at %d: %v", at, err)
		}
	}

	got, ok := firstSeen(t, db, "s1")
	if !ok || got != t0 {
		t.Errorf("first_seen_at = %d (present %v), want %d unchanged", got, ok, t0)
	}
	if n, err := db.CountSeriesSeenSince(ctx, 0); err != nil || n != 1 {
		t.Errorf("rows = %d (%v), want exactly 1 — a second sighting must not insert", n, err)
	}
	// The whole row is write-once, not just the timestamp: root_name and
	// series_path are what `shelf migrate-root` will rewrite ids from.
	var root, path string
	raw := openRaw(t, db.Path())
	if err := raw.QueryRow(
		`SELECT root_name, series_path FROM series_seen WHERE series_id = 's1'`).Scan(&root, &path); err != nil {
		t.Fatalf("reading the row back: %v", err)
	}
	if root != "manga" || path != "[만화] s1" {
		t.Errorf("row = (%q, %q), want the values of the first sighting", root, path)
	}
}

func TestMarkSeriesSeen_concurrentFirstSighting_writesOneRow(t *testing.T) {
	t.Parallel()
	db, _, _ := newDB(t)
	ctx := t.Context()

	const goroutines = 16
	stamps := make(map[int64]bool, goroutines)
	rows := make([]userdata.SeriesSeen, 0, goroutines)
	for i := range goroutines {
		at := int64(1_700_000_000 + i)
		stamps[at] = true
		rows = append(rows, seenRow("contended", at))
	}

	var wg sync.WaitGroup
	errs := make([]error, goroutines)
	start := make(chan struct{})
	for i := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			// Each goroutine also offers an id of its own, so the batch path is
			// contended too and not just the single row.
			errs[i] = db.MarkSeriesSeen(ctx, []userdata.SeriesSeen{
				rows[i], seenRow(fmt.Sprintf("own-%02d", i), rows[i].FirstSeenAt),
			})
		}()
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: MarkSeriesSeen: %v", i, err)
		}
	}

	// One row for the contended id — a race between two workers must not be a
	// second row, and must not be an error either.
	at, ok := firstSeen(t, db, "contended")
	if !ok {
		t.Fatal("the contended series has no row at all")
	}
	if !stamps[at] {
		t.Errorf("first_seen_at = %d, which no caller offered", at)
	}
	all, err := db.SeriesFirstSeen(ctx, nil)
	if err != nil {
		t.Fatalf("SeriesFirstSeen(all): %v", err)
	}
	if len(all) != goroutines+1 {
		t.Errorf("rows = %d, want %d: one per distinct series id, whatever the interleaving",
			len(all), goroutines+1)
	}
}

func TestSeriesFirstSeen_missingRowsAreAbsentNotZero(t *testing.T) {
	t.Parallel()
	db, _, _ := newDB(t)
	ctx := t.Context()

	if err := db.MarkSeriesSeen(ctx, []userdata.SeriesSeen{seenRow("known", 1_700_000_000)}); err != nil {
		t.Fatalf("MarkSeriesSeen: %v", err)
	}
	got, err := db.SeriesFirstSeen(ctx, []string{"known", "never-scanned"})
	if err != nil {
		t.Fatalf("SeriesFirstSeen: %v", err)
	}
	if len(got) != 1 || got["known"] != 1_700_000_000 {
		t.Errorf("SeriesFirstSeen = %v, want only the known series", got)
	}
	if _, present := got["never-scanned"]; present {
		t.Error("a series with no row must be absent, not zero: NULL >= cutoff is not true (arch §3.6 rule 7)")
	}
}

func TestSeriesFirstSeen_acceptsUnboundedIDLists(t *testing.T) {
	t.Parallel()
	db, _, _ := newDB(t)
	ctx := t.Context()

	// Past SQLite's bound-variable ceiling in older builds (999) and well past
	// the 400-id chunk this package splits at.
	const n = 1500
	rows := make([]userdata.SeriesSeen, 0, n)
	ids := make([]string, 0, n)
	for i := range n {
		id := fmt.Sprintf("s%04d", i)
		ids = append(ids, id)
		rows = append(rows, seenRow(id, int64(1_700_000_000+i)))
	}
	if err := db.MarkSeriesSeen(ctx, rows); err != nil {
		t.Fatalf("MarkSeriesSeen(%d rows): %v", n, err)
	}
	got, err := db.SeriesFirstSeen(ctx, ids)
	if err != nil {
		t.Fatalf("SeriesFirstSeen(%d ids): %v", n, err)
	}
	if len(got) != n {
		t.Errorf("read back %d of %d rows", len(got), n)
	}
}

// The count and the list must agree for the same window, or the sidebar badge
// and the list it opens would disagree (arch §7.5).
func TestCountSeriesSeenSince_matchesTheRowsInTheSameWindow(t *testing.T) {
	t.Parallel()
	db, _, _ := newDB(t)
	ctx := t.Context()

	const now = int64(1_700_000_000)
	const day = int64(86_400)
	rows := []userdata.SeriesSeen{
		seenRow("today", now),
		seenRow("yesterday", now-day),
		seenRow("thirteen-days", now-13*day),
		seenRow("exactly-fourteen", now-14*day), // the boundary is inclusive: >=
		seenRow("fifteen-days", now-15*day),
		seenRow("ancient", now-4000*day),
	}
	if err := db.MarkSeriesSeen(ctx, rows); err != nil {
		t.Fatalf("MarkSeriesSeen: %v", err)
	}

	for _, days := range []int64{1, 7, 14, 15, 3650} {
		cutoff := now - days*day
		want := 0
		for _, r := range rows {
			if r.FirstSeenAt >= cutoff {
				want++
			}
		}
		got, err := db.CountSeriesSeenSince(ctx, cutoff)
		if err != nil {
			t.Fatalf("CountSeriesSeenSince(%d): %v", cutoff, err)
		}
		if int(got) != want {
			t.Errorf("%d-day window: count = %d, want %d", days, got, want)
		}
	}

	// The 14-day default is the one the product ships with, so pin it: the
	// series first seen exactly 14 days ago is inside the half-open window.
	if got, _ := db.CountSeriesSeenSince(ctx, now-14*day); got != 4 {
		t.Errorf("14-day count = %d, want 4 (the boundary row is included, `>=`)", got)
	}
}

func TestMarkSeriesSeen_rejectsUnusableRows(t *testing.T) {
	t.Parallel()
	db, _, _ := newDB(t)
	ctx := t.Context()

	cases := map[string]userdata.SeriesSeen{
		"empty series id": {SeriesID: "", RootName: "manga", FirstSeenAt: 1},
		"zero timestamp":  {SeriesID: "s1", RootName: "manga", FirstSeenAt: 0},
		"negative":        {SeriesID: "s1", RootName: "manga", FirstSeenAt: -5},
	}
	for name, row := range cases {
		if err := db.MarkSeriesSeen(ctx, []userdata.SeriesSeen{row}); !errors.Is(err, userdata.ErrInvalidArgument) {
			t.Errorf("%s: MarkSeriesSeen = %v, want ErrInvalidArgument", name, err)
		}
	}
	// The batch is rejected as a whole, so a bad row cannot half-write one.
	if err := db.MarkSeriesSeen(ctx, []userdata.SeriesSeen{
		seenRow("good", 1_700_000_000), {SeriesID: "", FirstSeenAt: 1},
	}); !errors.Is(err, userdata.ErrInvalidArgument) {
		t.Errorf("mixed batch = %v, want ErrInvalidArgument", err)
	}
	if n, err := db.CountSeriesSeenSince(ctx, 0); err != nil || n != 0 {
		t.Errorf("rows after rejected batches = %d (%v), want 0", n, err)
	}
	if err := db.MarkSeriesSeen(ctx, nil); err != nil {
		t.Errorf("MarkSeriesSeen(nil) = %v, want nil: an empty batch is not an error", err)
	}
}

// The bootstrap run of arch §3.6 rule 6 — the reason a fresh install of a
// 963-series collection does not put 963 in a badge that means "new".
func TestFirstSeenBootstrap_isDecidedOnceAndThenNeverAgain(t *testing.T) {
	t.Parallel()
	db, _, _ := newDB(t)
	ctx := t.Context()

	need, err := db.FirstSeenBootstrapNeeded(ctx)
	if err != nil || !need {
		t.Fatalf("a fresh user.db: FirstSeenBootstrapNeeded = %v (%v), want true", need, err)
	}

	if err := db.MarkSeriesSeen(ctx, []userdata.SeriesSeen{seenRow("s1", 1_600_000_000)}); err != nil {
		t.Fatalf("MarkSeriesSeen: %v", err)
	}
	// Ruling E-16: rows alone do NOT end it. Only the marker does, and only a
	// bootstrap run that finished the job writes one — see the interrupted-run
	// test below for why the difference is the whole point.
	if need, err := db.FirstSeenBootstrapNeeded(ctx); err != nil || !need {
		t.Errorf("with rows present but no marker: FirstSeenBootstrapNeeded = %v (%v), want true", need, err)
	}

	if err := db.CompleteFirstSeenBootstrap(ctx, 1_700_000_000); err != nil {
		t.Fatalf("CompleteFirstSeenBootstrap: %v", err)
	}
	if v, ok, err := db.Meta(ctx, "first_seen_bootstrap"); err != nil || !ok || v != "1700000000" {
		t.Errorf("meta first_seen_bootstrap = %q (present %v, %v), want the run start", v, ok, err)
	}
	if need, err := db.FirstSeenBootstrapNeeded(ctx); err != nil || need {
		t.Errorf("with the marker written: FirstSeenBootstrapNeeded = %v (%v), want false", need, err)
	}
	// Stamped once, like the rows it describes.
	if err := db.CompleteFirstSeenBootstrap(ctx, 1_800_000_000); err != nil {
		t.Fatalf("second CompleteFirstSeenBootstrap: %v", err)
	}
	if v, _, _ := db.Meta(ctx, "first_seen_bootstrap"); v != "1700000000" {
		t.Errorf("meta first_seen_bootstrap = %q after a second call, want the original", v)
	}
	if err := db.CompleteFirstSeenBootstrap(ctx, 0); !errors.Is(err, userdata.ErrInvalidArgument) {
		t.Errorf("CompleteFirstSeenBootstrap(0) = %v, want ErrInvalidArgument", err)
	}
}

// Ruling E-16, and the real-world path: a first scan of 414 GB gets interrupted.
//
// The run walks root A, commits its sightings, and is then cancelled before root
// B. internal/scanner already does the right thing at that moment — it withholds
// `meta.first_seen_bootstrap`, because a cancelled run has not dated the whole
// collection — but rule 6 used to *also* require `series_seen` to be empty, and
// root A's rows had already landed. So the recovering run was not a bootstrap
// run, and root B's decade-old series were stamped with that run's wall clock:
// the entire second half of the library reported as 최근 추가. That is exactly
// the wrong number ruling E-9 exists to prevent, produced by the fix for it.
//
// The marker alone now decides, so the interrupted run's rows are irrelevant and
// the recovering run finishes the job it started.
func TestFirstSeenBootstrap_interruptedAfterRowsLanded_stillBootstrapsTheRest(t *testing.T) {
	t.Parallel()
	db, _, _ := newDB(t)
	ctx := t.Context()

	// A collection that has been on the disk for years, in two roots.
	const runOneStart = int64(1_700_000_000)
	const mtimeA = runOneStart - 900*86400 // root A: ~2.5 years old
	const mtimeB = runOneStart - 400*86400 // root B: ~13 months old

	// --- run 1: the bootstrap run, cancelled after root A committed ---------
	need, err := db.FirstSeenBootstrapNeeded(ctx)
	if err != nil || !need {
		t.Fatalf("run 1 on a fresh user.db: FirstSeenBootstrapNeeded = %v (%v), want true", need, err)
	}
	// A bootstrap run stamps min(run start, series mtime) — arch §3.6 rule 6,
	// ruling E-18.
	if err := db.MarkSeriesSeen(ctx, []userdata.SeriesSeen{seenRow("a", min(runOneStart, mtimeA))}); err != nil {
		t.Fatalf("run 1 MarkSeriesSeen: %v", err)
	}
	// Cancelled here. finishSeen leaves the marker unset (arch §3.6 rule 6), so
	// root B has never been dated by anything.

	// --- run 2: the recovering run -----------------------------------------
	const runTwoStart = runOneStart + 3600
	need, err = db.FirstSeenBootstrapNeeded(ctx)
	if err != nil || !need {
		t.Fatalf("run 2 after an interrupted bootstrap that committed root A: "+
			"FirstSeenBootstrapNeeded = %v (%v), want true — the marker is unset, so the "+
			"bootstrap is unfinished and root B must still be dated from its mtime", need, err)
	}
	at := runTwoStart
	if need {
		at = min(runTwoStart, mtimeB)
	}
	if err := db.MarkSeriesSeen(ctx, []userdata.SeriesSeen{seenRow("b", at)}); err != nil {
		t.Fatalf("run 2 MarkSeriesSeen: %v", err)
	}
	if err := db.CompleteFirstSeenBootstrap(ctx, runTwoStart); err != nil {
		t.Fatalf("CompleteFirstSeenBootstrap: %v", err)
	}

	// Root B is dated from the filesystem, not from run 2's clock.
	if got, ok := firstSeen(t, db, "b"); !ok || got != mtimeB {
		t.Errorf("the recovering run stamped root B's series at %d (present %v), want its mtime %d: "+
			"the interrupted run must not have consumed the bootstrap", got, ok, mtimeB)
	}
	// Root A keeps what run 1 gave it — first_seen_at is write-once.
	if got, ok := firstSeen(t, db, "a"); !ok || got != mtimeA {
		t.Errorf("root A's series = %d (present %v), want %d", got, ok, mtimeA)
	}
	// AC-006: nothing in a years-old library is "recently added" on day one.
	since, err := db.CountSeriesSeenSince(ctx, runTwoStart-14*86400)
	if err != nil {
		t.Fatalf("CountSeriesSeenSince: %v", err)
	}
	if since != 0 {
		t.Errorf("최근 추가 holds %d series after an interrupted first scan, want 0", since)
	}

	// And the completed bootstrap closes bootstrapping for good.
	if need, err := db.FirstSeenBootstrapNeeded(ctx); err != nil || need {
		t.Errorf("after the recovering run finished: FirstSeenBootstrapNeeded = %v (%v), want false", need, err)
	}
}

// The marker survives an empty table: a user.db whose series all vanished (an
// unplugged drive) must not be treated as a fresh install on the next scan.
func TestFirstSeenBootstrap_markerOutlivesTheRows(t *testing.T) {
	t.Parallel()
	db, _, dir := newDB(t)
	ctx := t.Context()

	if err := db.CompleteFirstSeenBootstrap(ctx, 1_700_000_000); err != nil {
		t.Fatalf("CompleteFirstSeenBootstrap: %v", err)
	}
	if need, err := db.FirstSeenBootstrapNeeded(ctx); err != nil || need {
		t.Errorf("FirstSeenBootstrapNeeded = %v (%v), want false with no rows but a marker", need, err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// And across a restart, which is when the question is actually asked.
	reopened := openIn(t, dir, nil)
	if need, err := reopened.FirstSeenBootstrapNeeded(ctx); err != nil || need {
		t.Errorf("after reopening: FirstSeenBootstrapNeeded = %v (%v), want false", need, err)
	}
}

// NFR-DAT-004 / AC-006 at this layer: --rebuild-index removes index.db and its
// two sidecars by name and never opens user.db, so the stamps outlive it.
func TestSeriesSeen_survivesDeletionOfTheIndexFiles(t *testing.T) {
	t.Parallel()
	db, _, dir := newDB(t)
	ctx := t.Context()

	const t0 = int64(1_600_000_000)
	if err := db.MarkSeriesSeen(ctx, []userdata.SeriesSeen{seenRow("s1", t0), seenRow("s2", t0+1)}); err != nil {
		t.Fatalf("MarkSeriesSeen: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	for _, name := range []string{"index.db", "index.db-wal", "index.db-shm"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("derived"), 0o600); err != nil {
			t.Fatalf("creating %s: %v", name, err)
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			t.Fatalf("removing %s: %v", name, err)
		}
	}

	reopened := openIn(t, dir, nil)
	got, err := reopened.SeriesFirstSeen(ctx, nil)
	if err != nil {
		t.Fatalf("SeriesFirstSeen after the rebuild: %v", err)
	}
	if len(got) != 2 || got["s1"] != t0 || got["s2"] != t0+1 {
		t.Errorf("first sightings after --rebuild-index = %v, want both, unchanged", got)
	}
}

// arch §3.6 rule 8, and the reason it is a rule: the v1→v2 rung is an
// APPEND-ONLY migration. `user.db` is the one file in SHELF that is authored
// rather than derived (NFR-DAT-004), so the upgrade every existing installation
// actually performs — schema_version '1', no `series_seen` — has to leave every
// authored row exactly as it found it.
//
// Nothing else in this package ever opens a version-1 file: the other Open tests
// build a fresh one (0→2 in a single Open), forge version 42, or forge a foreign
// id scheme. A rung that dropped and recreated `progress` on the way past would
// therefore pass the entire suite while silently destroying every reader's
// history. This is the test that would fail.
func TestOpen_upgradeFromV1_addsSeriesSeenAndKeepsEveryAuthoredRow(t *testing.T) {
	t.Parallel()
	db, clk, dir := newDB(t)
	ctx := t.Context()

	// Author one row in every table version 1 knew about.
	if _, err := db.PutProgress(ctx, update("b1", "s1", 7, 20)); err != nil {
		t.Fatalf("PutProgress: %v", err)
	}
	if _, err := db.PutPrefs(ctx, "b1", userdata.PrefsPatch{
		FitMode: userdata.SetPatch("height")}); err != nil {
		t.Fatalf("PutPrefs: %v", err)
	}
	if err := db.Settings().Put(ctx, "prefetch", "4"); err != nil {
		t.Fatalf("Settings.Put: %v", err)
	}
	if err := db.ViewState().Put(ctx, "library.sort", "recent"); err != nil {
		t.Fatalf("ViewState.Put: %v", err)
	}
	before, err := db.GetProgress(ctx, "b1")
	if err != nil {
		t.Fatalf("GetProgress: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Rewind the file to exactly what a pre-A-8 build left behind: the version-1
	// schema, stamped '1', with no series_seen and no bootstrap marker.
	raw := openRaw(t, filepath.Join(dir, "user.db"))
	for _, stmt := range []string{
		`DROP TABLE series_seen`,
		`UPDATE meta SET value = '1' WHERE key = 'schema_version'`,
	} {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("rewinding to v1 (%s): %v", stmt, err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("closing the raw handle: %v", err)
	}

	// The upgrade an installed copy performs on its next start.
	up := openIn(t, dir, clk)
	if v, err := up.SchemaVersion(ctx); err != nil || v != 2 {
		t.Fatalf("schema version after the upgrade = %d (%v), want 2", v, err)
	}

	// (1) Nothing authored was touched.
	after, err := up.GetProgress(ctx, "b1")
	if err != nil {
		t.Fatalf("GetProgress after the upgrade: %v", err)
	}
	if after != before {
		t.Errorf("the upgrade changed the progress row:\n got %+v\nwant %+v", after, before)
	}
	if n, err := up.CountProgress(ctx); err != nil || n != 1 {
		t.Errorf("progress rows after the upgrade = %d (%v), want 1", n, err)
	}
	pr, err := up.GetPrefs(ctx, "b1")
	if err != nil || pr.FitMode == nil || *pr.FitMode != "height" {
		t.Errorf("prefs after the upgrade = %+v (%v), want fit_mode height", pr, err)
	}
	if v, ok, err := up.Settings().Get(ctx, "prefetch"); err != nil || !ok || v != "4" {
		t.Errorf("settings.prefetch after the upgrade = %q (present %v, %v), want 4", v, ok, err)
	}
	if v, ok, err := up.ViewState().Get(ctx, "library.sort"); err != nil || !ok || v != "recent" {
		t.Errorf("view_state.library.sort after the upgrade = %q (present %v, %v), want recent",
			v, ok, err)
	}

	// (2) The new table exists, is empty, and the upgraded file is therefore due
	// a bootstrap run (rule 6) rather than a library stamped "added today".
	if need, err := up.FirstSeenBootstrapNeeded(ctx); err != nil || !need {
		t.Errorf("FirstSeenBootstrapNeeded on a freshly upgraded file = %v (%v), want true",
			need, err)
	}
	const t0 = int64(1_700_000_000)
	if err := up.MarkSeriesSeen(ctx, []userdata.SeriesSeen{seenRow("s1", t0)}); err != nil {
		t.Fatalf("MarkSeriesSeen after the upgrade: %v", err)
	}
	if at, ok := firstSeen(t, up, "s1"); !ok || at != t0 {
		t.Errorf("first_seen_at after the upgrade = %d (present %v), want %d", at, ok, t0)
	}
}
