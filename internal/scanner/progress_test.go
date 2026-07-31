package scanner

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"shelf/internal/config"
	"shelf/internal/index"
	"shelf/internal/source"
)

// The publish cadence of arch §4.12: at most every 200 ms, except where a
// reader polling at 1 Hz would otherwise see a stale picture.
func TestProgress_publishesAtMostEveryTwoHundredMillisecondsButAlwaysOnAPhaseChange(t *testing.T) {
	t.Parallel()
	clock := &fakeClock{at: time.Unix(1_700_000_000, 0)}
	p := newProgress(clock.Now)

	if got := p.Status(); got.State != PhaseIdle {
		t.Fatalf("a fresh progress reports %q, want idle", got.State)
	}

	p.begin("run-1", false, []string{"manga"}, clock.Now())
	p.beginRoot("manga")
	clock.advance(publishInterval)
	p.discovered("manga", 1, 10)
	if got := p.Status().Total; got != 10 {
		t.Fatalf("total = %d after the first publish, want 10", got)
	}

	// Inside the window: the working copy moves, the snapshot does not.
	clock.advance(50 * time.Millisecond)
	p.discovered("manga", 1, 10)
	if got := p.Status().Total; got != 10 {
		t.Errorf("total = %d 50 ms after a publish, want the previous snapshot's 10", got)
	}

	// Past the window: the next mutation republishes.
	clock.advance(200 * time.Millisecond)
	p.discovered("manga", 1, 10)
	if got := p.Status().Total; got != 30 {
		t.Errorf("total = %d after the window elapsed, want 30", got)
	}

	// A phase change never waits for the window.
	clock.advance(time.Millisecond)
	p.setPhase(PhaseIndexing)
	if got := p.Status().State; got != PhaseIndexing {
		t.Errorf("state = %q, want a phase change to publish immediately", got)
	}

	// The first completed item never waits either — it is what tells a poller
	// the run is alive rather than stuck.
	p.bookDone("manga", 4, false, StatusOK, "")
	if got := p.Status().Done; got != 1 {
		t.Errorf("done = %d after the first book, want 1", got)
	}
	// Nor does the last one, so the indexing phase never finishes reading
	// "29 / 30" while the covers are generated.
	for range 29 {
		p.bookDone("manga", 1, true, StatusOK, "")
	}
	final := p.Status()
	if final.Done != 30 || final.Skipped != 29 {
		t.Errorf("done/skipped = %d/%d, want 30/29 published without waiting for the window",
			final.Done, final.Skipped)
	}
	if len(final.PerRoot) != 1 || final.PerRoot[0].Books != 30 || final.PerRoot[0].Pages != 33 {
		t.Errorf("per-root = %+v, want manga with 30 books and 33 pages", final.PerRoot)
	}
}

// A published snapshot is immutable: a reader holding one must never see it
// change underneath.
func TestProgress_publishedSnapshots_areIndependentOfLaterUpdates(t *testing.T) {
	t.Parallel()
	clock := &fakeClock{at: time.Unix(1_700_000_000, 0)}
	p := newProgress(clock.Now)
	p.begin("run-1", true, []string{"manga", "novel"}, clock.Now())
	p.beginRoot("manga")
	p.discovered("manga", 2, 5)

	held := p.Status()
	heldRoots := append([]string(nil), held.Roots...)
	heldPerRoot := append([]RootProgress(nil), held.PerRoot...)

	clock.advance(time.Second)
	p.beginRoot("novel")
	p.discovered("novel", 3, 9)
	p.bookDone("novel", 1, false, StatusError, "boom")

	if !equalStrings(held.Roots, heldRoots) {
		t.Errorf("the held snapshot's Roots mutated: %v vs %v", held.Roots, heldRoots)
	}
	if len(held.PerRoot) != len(heldPerRoot) {
		t.Errorf("the held snapshot's PerRoot grew from %d to %d", len(heldPerRoot), len(held.PerRoot))
	}
	if held.Errors != 0 {
		t.Errorf("the held snapshot picked up %d later errors", held.Errors)
	}
	if fresh := p.Status(); fresh.Errors != 1 || fresh.LastError != "boom" {
		t.Errorf("the fresh snapshot = errors %d last_error %q, want 1/boom", fresh.Errors, fresh.LastError)
	}
}

// arch §7.10: `eta_ms` is null until a rate can be estimated.
func TestEstimateETA_isNullUntilARateMeansSomething(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		done, total   int64
		elapsed       time.Duration
		wantNil       bool
		wantMsAtLeast int64
	}{
		{name: "nothing done yet", done: 0, total: 100, elapsed: time.Second, wantNil: true},
		{name: "too few samples", done: 3, total: 100, elapsed: time.Second, wantNil: true},
		{name: "too little time", done: 50, total: 100, elapsed: 100 * time.Millisecond, wantNil: true},
		{name: "already finished", done: 100, total: 100, elapsed: time.Second, wantNil: true},
		{name: "half way after a second", done: 50, total: 100, elapsed: time.Second, wantMsAtLeast: 900},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := estimateETA(tc.done, tc.total, tc.elapsed)
			if tc.wantNil {
				if got != nil {
					t.Fatalf("eta = %d ms, want null", *got)
				}
				return
			}
			if got == nil {
				t.Fatal("eta = null, want a number")
			}
			if *got < tc.wantMsAtLeast {
				t.Errorf("eta = %d ms, want at least %d", *got, tc.wantMsAtLeast)
			}
		})
	}
}

type fakeClock struct {
	mu sync.Mutex
	at time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

// arch §4.9's rule, in one table. Getting any row of this wrong deletes a user's
// library, so it is asserted directly rather than only through the scans that
// exercise it.
func TestDecideSweep_refusesEveryCaseWhereARootWasNotFullyEnumerated(t *testing.T) {
	t.Parallel()
	boom := errors.New("root is unreachable")
	cases := []struct {
		name      string
		rootErr   error
		cancelled bool
		targeted  bool
		allowed   bool
	}{
		{name: "a clean full pass sweeps", allowed: true},
		{name: "an unreachable root never sweeps", rootErr: boom},
		{name: "a cancelled run never sweeps", cancelled: true},
		{name: "a targeted run never sweeps", targeted: true},
		{name: "an unreachable root wins over everything", rootErr: boom, cancelled: true, targeted: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := decideSweep(tc.rootErr, tc.cancelled, tc.targeted)
			if d.allowed != tc.allowed {
				t.Fatalf("allowed = %v, want %v", d.allowed, tc.allowed)
			}
			if !d.allowed && d.reason == "" {
				t.Error("a blocked sweep carries no reason; the operator has to be told why")
			}
		})
	}
}

// arch §4.1: cancelling mid-run commits the series that finished and abandons
// the one that did not, without recording it as broken.
func TestScan_cancelMidRun_commitsFinishedSeriesAndAbandonsTheRestCleanly(t *testing.T) {
	t.Parallel()
	h := newHarness(t, map[string]any{
		"시리즈 a": map[string]any{"01권.zip": jpegZIP(t, "001.jpg")},
		"시리즈 b": map[string]any{"01권.zip": jpegZIP(t, "001.jpg")},
		"시리즈 c": map[string]any{"01권.zip": jpegZIP(t, "001.jpg")},
	}, func(sc *config.Scan) { sc.Workers = 1 })
	parked := make(chan struct{})
	h.lister.inner = &parkingLister{
		inner:  h.lister.inner,
		park:   map[string]bool{"시리즈 c/01권.zip": true},
		parked: parked,
	}

	done := make(chan *Result, 1)
	go func() {
		res, err := h.scanner.Run(h.t.Context(), Request{})
		if err != nil {
			t.Errorf("a cancelled scan must return cleanly, got %v", err)
		}
		done <- res
	}()

	// One worker, so books are taken in natural order: by the time 시리즈 c parks,
	// 시리즈 a and 시리즈 b are provably finished.
	select {
	case <-parked:
	case <-time.After(30 * time.Second):
		t.Fatal("the scan never reached the parked book")
	}
	if !h.scanner.Cancel() {
		t.Fatal("Cancel reported that nothing was running")
	}
	res := <-done

	if !res.Cancelled {
		t.Error("Result.Cancelled is false after Cancel")
	}
	if res.Roots[0].Swept != (index.SweepResult{}) {
		t.Errorf("a cancelled run swept %+v", res.Roots[0].Swept)
	}

	got := seriesRels(h.series())
	if len(got) != 2 {
		t.Fatalf("committed %d series (%v); the two that finished must be committed and the parked one must not", len(got), got)
	}
	for _, rel := range got {
		if rel == "시리즈 c" {
			t.Error("the parked series was committed")
		}
		for _, b := range h.books("manga", rel) {
			if b.Status != StatusOK {
				t.Errorf("%s/%s committed as %q (%q); a cancellation is not a broken book",
					rel, b.DisplayName, b.Status, b.Error)
			}
		}
	}
}

// parkingLister holds the named books open-but-unanswered until the context is
// cancelled, so a test can freeze a scan half-finished and cancel it
// deterministically.
type parkingLister struct {
	inner  BookLister
	park   map[string]bool
	once   sync.Once
	parked chan struct{}
}

func (p *parkingLister) Open(ctx context.Context, b source.Book) (source.BookSource, error) {
	if p.park[b.RelPath] {
		p.once.Do(func() { close(p.parked) })
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return p.inner.Open(ctx, b)
}

// FR-IDX-004's per-root breakdown has to survive a root that failed.
func TestScan_progress_perRootBreakdown_recordsAFailedRoot(t *testing.T) {
	t.Parallel()
	live := newHarness(t, map[string]any{
		"시리즈": map[string]any{"01권.zip": jpegZIP(t, "001.jpg")},
	})
	live.cfgRoots[0].Path = live.dataDir + "/gone"
	live.build()
	live.run(Request{})

	st := live.scanner.Status()
	if len(st.PerRoot) != 1 {
		t.Fatalf("per-root breakdown = %+v, want one entry", st.PerRoot)
	}
	if st.PerRoot[0].Error == "" || !strings.Contains(st.PerRoot[0].Error, "unreachable") {
		t.Errorf("per-root error = %q, want it to name the unreachable root", st.PerRoot[0].Error)
	}
	if st.LastError == "" {
		t.Error("ScanStatus.last_error is empty after a root failed")
	}
}

// The `covers` phase of arch §4.12 only claims to know something when the queue
// can actually tell it.
func TestScan_coversPhase_reportsRealNumbersWhenTheQueueCan(t *testing.T) {
	t.Parallel()
	h := newHarness(t, map[string]any{
		"시리즈 a": map[string]any{"01권.zip": jpegZIP(t, "001.jpg")},
		"시리즈 b": map[string]any{"01권.zip": jpegZIP(t, "001.jpg")},
	})
	reporting := &reportingCoverQueue{}
	sc, err := New(Options{
		Index: h.idx, Books: h.lister, Roots: h.rootSet,
		ConfigRoots: h.cfgRoots, Scan: h.scanCfg,
		Covers: reporting, Logger: quietLogger(),
	})
	if err != nil {
		t.Fatalf("constructing the scanner: %v", err)
	}
	t.Cleanup(func() { _ = sc.Close() })

	if _, err := sc.Run(t.Context(), Request{}); err != nil {
		t.Fatalf("scan: %v", err)
	}
	st := sc.Status()
	if st.CoversTotal != 2 || st.CoversDone != 2 {
		t.Errorf("covers done/total = %d/%d, want 2/2", st.CoversDone, st.CoversTotal)
	}
	if got := reporting.count(); got != 2 {
		t.Errorf("the queue received %d covers, want 2", got)
	}
}

type reportingCoverQueue struct {
	mu   sync.Mutex
	seen int64
}

func (q *reportingCoverQueue) EnqueueCover(_ context.Context, _ CoverRequest) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.seen++
}

func (q *reportingCoverQueue) CoverProgress() (done, total int64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.seen, q.seen
}

func (q *reportingCoverQueue) count() int64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.seen
}

// The scan-log buffer is bounded, so a pathological tree turns diagnostics into
// a dropped counter rather than a memory leak.
func TestLogBuffer_isBoundedAndCountsWhatItDropped(t *testing.T) {
	t.Parallel()
	b := &logBuffer{}
	for i := range logBufferMax + 25 {
		b.add(index.LogEntry{Message: fmt.Sprintf("entry %d", i)})
	}
	if got := b.droppedCount(); got != 25 {
		t.Errorf("dropped = %d, want 25", got)
	}
	entries := b.take()
	if len(entries) != logBufferMax {
		t.Errorf("buffered %d entries, want the cap of %d", len(entries), logBufferMax)
	}
	if got := b.take(); got != nil {
		t.Errorf("take() returned %d entries after draining, want none", len(got))
	}
}
