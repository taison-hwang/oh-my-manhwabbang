package scanner

import (
	"context"
	"maps"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"shelf/internal/ids"
	"shelf/internal/index"
	"shelf/internal/testutil"
	"shelf/internal/userdata"
)

// Amendment A-8 (ruling E-9). The scanner is the only writer of
// user.db.series_seen: one row per series, written the first time a run ever
// lists it and never again. That is what makes 최근 추가 mean "new to me"
// instead of "since the last time somebody rebuilt the index"
// (NFR-DAT-004, AC-005, AC-006).

// The scanner's clock is progress_test.go's fakeClock, started at a chosen
// instant: first_seen_at is the run's start time, so a test that cannot say when
// a run started cannot assert anything exact.
func newFakeClock(at time.Time) *fakeClock { return &fakeClock{at: at} }

func (c *fakeClock) unix() int64 { return c.Now().Unix() }

// freezeClock rebinds the harness's scanner to a controllable clock.
func (h *harness) freezeClock(at time.Time) *fakeClock {
	h.t.Helper()
	clk := newFakeClock(at)
	h.clock = clk.Now
	h.build()
	return clk
}

func (h *harness) firstSeen() map[string]int64 {
	h.t.Helper()
	got, err := h.ud.SeriesFirstSeen(h.t.Context(), nil)
	if err != nil {
		h.t.Fatalf("SeriesFirstSeen: %v", err)
	}
	return got
}

func (h *harness) firstSeenOf(root, rel string) int64 {
	h.t.Helper()
	at, ok := h.firstSeen()[ids.SeriesID(root, rel)]
	if !ok {
		h.t.Fatalf("series %q of root %q has no first-sighting row", rel, root)
	}
	return at
}

// recentlyAdded is GET /api/series?scope=added: the names of the series inside
// the window, plus the `total` the sidebar badge reads from ?limit=1.
func (h *harness) recentlyAdded(cutoff int64) ([]string, int) {
	h.t.Helper()
	f := index.SeriesFilter{
		Scope: index.ScopeAdded, RecentlyAddedCutoff: cutoff,
		Status: "all", IncludeDisabledRoots: true, Limit: 200,
	}
	list, err := h.idx.ListSeries(h.t.Context(), f)
	if err != nil {
		h.t.Fatalf("listing scope=added: %v", err)
	}
	badge := f
	badge.Limit = 1
	one, err := h.idx.ListSeries(h.t.Context(), badge)
	if err != nil {
		h.t.Fatalf("listing scope=added with limit=1: %v", err)
	}
	if one.Total != list.Total {
		h.t.Errorf("badge total = %d but the full list totals %d", one.Total, list.Total)
	}
	names := make([]string, 0, len(list.Items))
	for _, s := range list.Items {
		names = append(names, s.DisplayName)
	}
	sort.Strings(names)
	return names, one.Total
}

func twoSeries(t testing.TB) map[string]any {
	return map[string]any{
		"[만화] 오래된 시리즈": map[string]any{"01권.zip": jpegZIP(t, "001.jpg", "002.jpg")},
		"[만화] 새 시리즈":   map[string]any{"01권.zip": jpegZIP(t, "001.jpg")},
	}
}

// arch §3.6 rule 6. The first scan of a pre-existing collection must not stamp
// the whole library as "added today" — a badge reading 963 next to the word
// "새로 추가" is the same class of wrong number ruling E-9 was raised about.
func TestScan_firstSeen_bootstrapRunUsesTheOlderOfRunStartAndSeriesMtime(t *testing.T) {
	t.Parallel()
	h := newHarness(t, twoSeries(t))
	// The material has been on disk for over a year; only the scan is new. The
	// archive first, then the directory it lives in — writing the archive is
	// what set the directory's own mtime.
	old := filepath.Join(h.rootDirs["manga"], "[만화] 오래된 시리즈")
	testutil.Touch(t, filepath.Join(old, "01권.zip"), -400*24*time.Hour)
	testutil.Touch(t, old, -400*24*time.Hour)

	clk := h.freezeClock(time.Now())
	h.run(Request{})

	oldSeen := h.firstSeenOf("manga", "[만화] 오래된 시리즈")
	fresh := h.firstSeenOf("manga", "[만화] 새 시리즈")
	if want := h.seriesAt("manga", "[만화] 오래된 시리즈").Mtime; oldSeen != want {
		t.Errorf("the old series was first seen at %d, want its mtime %d "+
			"(a bootstrap run stamps min(run start, series mtime))", oldSeen, want)
	}
	if oldSeen >= clk.unix()-14*86400 {
		t.Errorf("the old series' stamp %d is inside the 14-day window ending %d",
			oldSeen, clk.unix())
	}
	if fresh > clk.unix() || fresh < clk.unix()-14*86400 {
		t.Errorf("the fresh series was first seen at %d, want a value inside the "+
			"14-day window ending at the run start %d", fresh, clk.unix())
	}

	names, total := h.recentlyAdded(clk.unix() - 14*86400)
	if total != 1 || len(names) != 1 || names[0] != "[만화] 새 시리즈" {
		t.Errorf("최근 추가 = %v (total %d), want only the fresh series", names, total)
	}

	// The marker closes bootstrapping for good, so the next run stamps its own
	// start time even for material that predates it.
	if v, ok, err := h.ud.Meta(t.Context(), "first_seen_bootstrap"); err != nil || !ok {
		t.Errorf("meta first_seen_bootstrap = %q (present %v, %v), want the run start", v, ok, err)
	}
	if need, err := h.ud.FirstSeenBootstrapNeeded(t.Context()); err != nil || need {
		t.Errorf("FirstSeenBootstrapNeeded after the first run = %v (%v), want false", need, err)
	}
}

// The rule the amendment exists for: written once, never overwritten — across a
// plain rescan, across a full rescan, and across --rebuild-index, which is the
// one that would reset the whole library if the stamp lived in index.db.
func TestScan_firstSeen_survivesRescanAndRebuildIndex(t *testing.T) {
	t.Parallel()
	h := newHarness(t, twoSeries(t))
	clk := h.freezeClock(time.Now())
	h.run(Request{})

	original := maps.Clone(h.firstSeen())
	if len(original) != 2 {
		t.Fatalf("first sightings after the first scan = %v, want one per series", original)
	}
	if _, total := h.recentlyAdded(clk.unix() - 14*86400); total != 2 {
		t.Fatalf("최근 추가 right after the first scan = %d, want both series", total)
	}

	// (1) An incremental rescan a month later. Nothing changed on disk, so
	// nothing may change here either — and the window has moved past both.
	clk.advance(30 * 24 * time.Hour)
	h.run(Request{})
	if got := h.firstSeen(); !maps.Equal(got, original) {
		t.Errorf("an incremental rescan moved first_seen_at: %v, want %v", got, original)
	}
	if names, total := h.recentlyAdded(clk.unix() - 14*86400); total != 0 {
		t.Errorf("최근 추가 30 days later = %v (total %d), want empty: the window is "+
			"evaluated per request, with no scan and no restart", names, total)
	}

	// (2) A forced full rescan. `Full` bypasses every incremental skip, so every
	// series is re-read and re-upserted — and still not re-stamped.
	clk.advance(24 * time.Hour)
	h.run(Request{Full: true})
	if got := h.firstSeen(); !maps.Equal(got, original) {
		t.Errorf("a full rescan moved first_seen_at: %v, want %v", got, original)
	}

	// (3) --rebuild-index: the derived catalogue is destroyed and rebuilt from
	// the filesystem. index.series.added_at is now "today" for every row; the
	// authored stamp is not, which is the whole point of putting it in user.db.
	clk.advance(24 * time.Hour)
	if err := h.idx.Reset(t.Context()); err != nil {
		t.Fatalf("rebuilding the index: %v", err)
	}
	if got := len(h.series()); got != 0 {
		t.Fatalf("the index was not emptied: %d series", got)
	}
	h.run(Request{Full: true})

	if got := len(h.series()); got != 2 {
		t.Fatalf("the rebuilt index holds %d series, want 2", got)
	}
	if got := h.firstSeen(); !maps.Equal(got, original) {
		t.Errorf("--rebuild-index moved first_seen_at: %v, want %v", got, original)
	}
	for _, s := range h.series() {
		if s.AddedAt != original[s.ID] {
			t.Errorf("%s added_at = %d after the rebuild, want the user.db stamp %d",
				s.DisplayName, s.AddedAt, original[s.ID])
		}
	}
	if names, total := h.recentlyAdded(clk.unix() - 14*86400); total != 0 {
		t.Errorf("최근 추가 after --rebuild-index = %v (total %d), want empty; a rebuild "+
			"must not make an old library look new (AC-006)", names, total)
	}
}

// arch §3.6 rules 4 and 5: the row outlives the series. A drive that was
// unplugged for a week, or a backup restored, must not light 최근 추가 up again.
func TestScan_firstSeen_seriesThatVanishesAndReturns_keepsItsOriginalStamp(t *testing.T) {
	t.Parallel()
	full := twoSeries(t)
	h := newHarnessAt(t, map[string]string{"manga": testutil.BuildTree(t, full)})
	clk := h.freezeClock(time.Now())
	h.run(Request{})
	original := h.firstSeenOf("manga", "[만화] 새 시리즈")

	// Rebind the root name to a tree without that series — ids hash (root name,
	// rel path), never the absolute path, so this is exactly "the series was
	// deleted" as far as the index is concerned, without a write primitive this
	// package is allowed to contain (FR-CFG-005).
	without := map[string]any{"[만화] 오래된 시리즈": full["[만화] 오래된 시리즈"]}
	gone := testutil.BuildTree(t, without)
	h.rootDirs["manga"], h.cfgRoots[0].Path = gone, gone
	clk.advance(60 * 24 * time.Hour)
	h.build()
	if res := h.run(Request{}); res.Roots[0].Swept.Series != 1 {
		t.Fatalf("the sweep removed %d series, want 1 (%s)",
			res.Roots[0].Swept.Series, res.Roots[0].SweepNote)
	}
	if len(h.series()) != 1 {
		t.Fatalf("the index still holds %d series, want 1", len(h.series()))
	}
	// The sweep is an index.db statement and has no user.db counterpart: rows
	// for series that no longer exist are deliberately kept, exactly like
	// orphaned progress rows.
	if at, ok := h.firstSeen()[ids.SeriesID("manga", "[만화] 새 시리즈")]; !ok || at != original {
		t.Errorf("first_seen_at of the vanished series = %d (present %v), want %d kept",
			at, ok, original)
	}

	// And now it comes back — same root name, same relative path, so the
	// scanner recomputes the same series_id and the insert finds a row.
	back := testutil.BuildTree(t, full)
	h.rootDirs["manga"], h.cfgRoots[0].Path = back, back
	clk.advance(24 * time.Hour)
	h.build()
	h.run(Request{})

	if len(h.series()) != 2 {
		t.Fatalf("the returned series was not re-indexed: %d series", len(h.series()))
	}
	if got := h.firstSeenOf("manga", "[만화] 새 시리즈"); got != original {
		t.Errorf("first_seen_at after the series returned = %d, want %d unchanged",
			got, original)
	}
	if names, total := h.recentlyAdded(clk.unix() - 14*86400); total != 0 {
		t.Errorf("최근 추가 after a series came back = %v (total %d), want empty: "+
			"remounting a drive is not an addition", names, total)
	}
}

// countingSeen counts what the scanner actually writes.
type countingSeen struct {
	SeriesSeenWriter
	calls atomic.Int64
	rows  atomic.Int64
}

func (c *countingSeen) MarkSeriesSeen(ctx context.Context, rows []userdata.SeriesSeen) error {
	c.calls.Add(1)
	c.rows.Add(int64(len(rows)))
	return c.SeriesSeenWriter.MarkSeriesSeen(ctx, rows)
}

// NFR-PRF-004 — a no-change rescan of 1 000 series has 30 s. The first-sighting
// pass may not put a write transaction on user.db into that path: after the
// first run every series is already recorded, so the batch must resolve to
// reads and stop.
func TestScan_firstSeen_incrementalRescan_writesNothing(t *testing.T) {
	t.Parallel()
	h := newHarness(t, twoSeries(t))
	counter := &countingSeen{SeriesSeenWriter: h.ud}
	sc, err := New(Options{
		Index: h.idx, Books: h.lister, Roots: h.rootSet,
		ConfigRoots: h.cfgRoots, Scan: h.scanCfg, Seen: counter,
		Logger: quietLogger(),
	})
	if err != nil {
		t.Fatalf("constructing the scanner: %v", err)
	}
	t.Cleanup(func() { _ = sc.Close() })

	if _, err := sc.Run(t.Context(), Request{}); err != nil {
		t.Fatalf("first scan: %v", err)
	}
	if got := counter.rows.Load(); got != 2 {
		t.Fatalf("the first scan recorded %d sightings, want 2", got)
	}

	before := counter.calls.Load()
	for range 3 {
		if _, err := sc.Run(t.Context(), Request{}); err != nil {
			t.Fatalf("rescan: %v", err)
		}
	}
	if got := counter.calls.Load(); got != before {
		t.Errorf("%d no-change rescans issued %d writes to user.db, want 0",
			3, got-before)
	}
	if got := len(h.firstSeen()); got != 2 {
		t.Errorf("first-sighting rows = %d, want 2", got)
	}
}

// Two processes scanning the same library into different indexes — a
// `shelf --rebuild-index` run started against a live server is the real case.
// The row is a primary key with ON CONFLICT DO NOTHING, so whoever gets there
// first wins and the loser is not an error.
func TestScan_firstSeen_twoConcurrentScans_writeOneRowPerSeries(t *testing.T) {
	t.Parallel()
	h := newHarness(t, twoSeries(t))
	ctx := t.Context()

	// Close bootstrapping first, so both runs stamp their own start time and the
	// assertion below can be exact.
	base := time.Now().Truncate(time.Second)
	if err := h.ud.CompleteFirstSeenBootstrap(ctx, base.Unix()); err != nil {
		t.Fatalf("CompleteFirstSeenBootstrap: %v", err)
	}

	other, err := index.Open(ctx, index.Options{
		Path: filepath.Join(t.TempDir(), "index.db"), UserPath: h.ud.Path(), Logger: quietLogger(),
	})
	if err != nil {
		t.Fatalf("opening the second index: %v", err)
	}
	t.Cleanup(func() { _ = other.Close() })

	clocks := []*fakeClock{newFakeClock(base.Add(time.Hour)), newFakeClock(base.Add(2 * time.Hour))}
	indexes := []*index.DB{h.idx, other}
	scanners := make([]*Scanner, len(clocks))
	for i := range scanners {
		sc, err := New(Options{
			Index: indexes[i], Books: h.lister, Roots: h.rootSet,
			ConfigRoots: h.cfgRoots, Scan: h.scanCfg, Seen: h.ud,
			Now: clocks[i].Now, Logger: quietLogger(),
		})
		if err != nil {
			t.Fatalf("constructing scanner %d: %v", i, err)
		}
		t.Cleanup(func() { _ = sc.Close() })
		scanners[i] = sc
	}

	var wg sync.WaitGroup
	errs := make([]error, len(scanners))
	start := make(chan struct{})
	for i, sc := range scanners {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, errs[i] = sc.Run(ctx, Request{})
		}()
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("scanner %d: %v", i, err)
		}
	}

	got := h.firstSeen()
	if len(got) != 2 {
		t.Fatalf("first-sighting rows = %v, want exactly one per series, not one per scan", got)
	}
	allowed := map[int64]bool{clocks[0].unix(): true, clocks[1].unix(): true}
	for _, rel := range []string{"[만화] 오래된 시리즈", "[만화] 새 시리즈"} {
		at := got[ids.SeriesID("manga", rel)]
		if !allowed[at] {
			t.Errorf("%s was first seen at %d, which is neither run's start time", rel, at)
		}
	}
	// Both indexes agree about the library, and both read the same stamp.
	for i, idx := range indexes {
		list, err := idx.ListSeries(ctx, index.SeriesFilter{Status: "all", Limit: 200})
		if err != nil {
			t.Fatalf("listing index %d: %v", i, err)
		}
		for _, s := range list.Items {
			if s.AddedAt != got[s.ID] {
				t.Errorf("index %d reports added_at %d for %s, want the shared stamp %d",
					i, s.AddedAt, s.DisplayName, got[s.ID])
			}
		}
	}
}

// A scanner with nowhere to record sightings indexes exactly as before: the
// mechanism is additive, and the scanner's own older tests construct it that way.
func TestScan_firstSeen_withoutAUserDatabase_scansNormally(t *testing.T) {
	t.Parallel()
	h := newHarness(t, twoSeries(t))
	sc, err := New(Options{
		Index: h.idx, Books: h.lister, Roots: h.rootSet,
		ConfigRoots: h.cfgRoots, Scan: h.scanCfg, Logger: quietLogger(),
	})
	if err != nil {
		t.Fatalf("constructing the scanner: %v", err)
	}
	t.Cleanup(func() { _ = sc.Close() })

	if _, err := sc.Run(t.Context(), Request{}); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if got := len(h.series()); got != 2 {
		t.Errorf("indexed %d series, want 2", got)
	}
	if got := h.firstSeen(); len(got) != 0 {
		t.Errorf("first-sighting rows = %v, want none", got)
	}
	// With no rows at all, scope=added is empty rather than everything.
	if names, total := h.recentlyAdded(0); total != 0 {
		t.Errorf("최근 추가 = %v (total %d), want empty: a series with no row is excluded", names, total)
	}
}

// arch §3.6 rule 6, the other half. `meta.first_seen_bootstrap` is a claim —
// "this collection has been dated from the filesystem evidence, everything from
// here on is genuinely new". Only a bootstrap run that finished the job may make
// it. Stamping after a run that recorded nothing is strictly worse than not
// stamping at all: rule 6's own precondition (series_seen empty AND the marker
// unset) still holds, so an unset marker lets the next run bootstrap, while a
// premature one hands the next run a decade-old library to date with its own
// wall clock — the wrong number ruling E-9 was raised about.
func TestScan_firstSeen_cancelledBootstrapRun_leavesTheMarkerForTheNextRun(t *testing.T) {
	t.Parallel()
	h := newHarness(t, map[string]any{
		"[만화] 오래된 시리즈": map[string]any{"01권.zip": jpegZIP(t, "001.jpg")},
	})
	old := filepath.Join(h.rootDirs["manga"], "[만화] 오래된 시리즈")
	testutil.Touch(t, filepath.Join(old, "01권.zip"), -400*24*time.Hour)
	testutil.Touch(t, old, -400*24*time.Hour)

	clk := h.freezeClock(time.Now())
	// freezeClock rebuilds the scanner, so the lister is wrapped afterwards.
	plain := h.lister.inner
	parked := make(chan struct{})
	h.lister.inner = &parkingLister{
		inner:  plain,
		park:   map[string]bool{"[만화] 오래된 시리즈/01권.zip": true},
		parked: parked,
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := h.scanner.Run(t.Context(), Request{}); err != nil {
			t.Errorf("a cancelled scan must return cleanly, got %v", err)
		}
	}()
	select {
	case <-parked:
	case <-time.After(30 * time.Second):
		t.Fatal("the scan never reached the parked book")
	}
	if !h.scanner.Cancel() {
		t.Fatal("Cancel reported that nothing was running")
	}
	<-done

	if got := h.firstSeen(); len(got) != 0 {
		t.Fatalf("the cancelled run recorded %v; it never committed a series", got)
	}
	if v, ok, err := h.ud.Meta(t.Context(), "first_seen_bootstrap"); err != nil || ok {
		t.Fatalf("meta first_seen_bootstrap = %q (present %v, %v) after a run that recorded "+
			"nothing; it must stay unset so the next run still bootstraps", v, ok, err)
	}
	if need, err := h.ud.FirstSeenBootstrapNeeded(t.Context()); err != nil || !need {
		t.Fatalf("FirstSeenBootstrapNeeded after a cancelled first run = %v (%v), want true", need, err)
	}

	// The next run is therefore still the bootstrap run: the 400-day-old series
	// is dated from its mtime, not from today, and 최근 추가 stays empty.
	h.lister.inner = plain
	h.run(Request{})

	seen := h.firstSeenOf("manga", "[만화] 오래된 시리즈")
	if want := h.seriesAt("manga", "[만화] 오래된 시리즈").Mtime; seen != want {
		t.Errorf("the recovering run stamped the old series at %d, want its mtime %d: "+
			"the cancelled run must not have consumed the bootstrap", seen, want)
	}
	if names, total := h.recentlyAdded(clk.unix() - 14*86400); total != 0 {
		t.Errorf("최근 추가 = %v (total %d), want empty: a cancelled first scan must not "+
			"make a year-old library look new (AC-006)", names, total)
	}
	if v, ok, err := h.ud.Meta(t.Context(), "first_seen_bootstrap"); err != nil || !ok || v == "" {
		t.Errorf("meta first_seen_bootstrap = %q (present %v, %v) after the run that did "+
			"finish; a completed bootstrap must close bootstrapping", v, ok, err)
	}
}

// The same rule for a run that covered only part of the library. A root that was
// not mounted, or a `--root nas` run, leaves the rest of the collection undated;
// closing bootstrapping there would date it from the next run's clock.
func TestScan_firstSeen_partialBootstrapRun_doesNotCloseBootstrapping(t *testing.T) {
	t.Parallel()
	one := map[string]any{"[만화] 시리즈": map[string]any{"01권.zip": jpegZIP(t, "001.jpg")}}
	build := func(t *testing.T) *harness {
		t.Helper()
		return newHarnessAt(t, map[string]string{
			"manga": testutil.BuildTree(t, one),
			"nas":   testutil.BuildTree(t, one),
		})
	}

	for _, tc := range []struct {
		name string
		prep func(t *testing.T, h *harness) Request
	}{{
		name: "a root that is not mounted",
		prep: func(t *testing.T, h *harness) Request {
			t.Helper()
			h.cfgRoots[1].Path = filepath.Join(h.dataDir, "not-mounted")
			h.build()
			return Request{}
		},
	}, {
		name: "a run restricted to one root",
		prep: func(t *testing.T, h *harness) Request { return Request{Roots: []string{"manga"}} },
	}, {
		name: "a run restricted to one series",
		prep: func(t *testing.T, h *harness) Request {
			return Request{Series: []SeriesRef{{Root: "manga", RelPath: "[만화] 시리즈"}}}
		},
	}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := build(t)
			req := tc.prep(t, h)
			h.run(req)

			if v, ok, err := h.ud.Meta(t.Context(), "first_seen_bootstrap"); err != nil || ok {
				t.Errorf("meta first_seen_bootstrap = %q (present %v, %v); a run that saw only "+
					"part of the library has not dated the rest of it", v, ok, err)
			}
		})
	}
}
