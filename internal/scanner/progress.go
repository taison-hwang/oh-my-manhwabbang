package scanner

import (
	"sync"
	"sync/atomic"
	"time"
)

// FR-IDX-004 — live scan progress, shaped for `GET /api/scan/status` (arch
// §7.10) and for the 1 s polling of conflict resolution C-11.
//
// One `atomic.Pointer[ScanStatus]` holds a fully-formed, immutable snapshot.
// Reading it is a single atomic load with no lock and no allocation, so the
// status endpoint can be polled as hard as anyone likes without ever appearing
// in the scan's profile. Writers take a mutex over the mutable working copy and
// republish at most every [publishInterval]; a phase change, the first book and
// the end of the run publish immediately, because those are the transitions a
// 1 Hz poller would otherwise show a second late.

// publishInterval is arch §4.12's "updated at most every 200 ms".
const publishInterval = 200 * time.Millisecond

// Phase is ScanStatus.state (arch §7.10). There is no "done" phase: a finished
// run is idle with FinishedAt set.
type Phase string

const (
	PhaseIdle       Phase = "idle"
	PhaseWalking    Phase = "walking"
	PhaseIndexing   Phase = "indexing"
	PhaseCovers     Phase = "covers"
	PhaseCancelling Phase = "cancelling"
)

// RootProgress is the per-root breakdown. It is not part of the frozen wire
// contract of arch §7.10 — that carries `roots: string[]` — so the HTTP layer is
// free to surface it or ignore it. The settings screen's per-root counts and the
// operator question "which root is the slow one?" both live here.
type RootProgress struct {
	Name string
	// Series and Books are what this run has written or stamped for the root.
	Series int64
	Books  int64
	Pages  int64
	// Skipped counts books FR-IDX-003 left alone. On a no-change rescan it
	// equals Books, which is the cheapest possible proof that the incremental
	// path is doing its job.
	Skipped int64
	Errors  int64
	// Done marks a root whose pass finished, Error the message that aborted it
	// (an unreachable drive, a permission failure at the top level).
	Done  bool
	Error string
}

// ScanStatus is one immutable snapshot. Every field is a value or a freshly
// allocated slice, so a snapshot handed to a reader can never change underneath
// it.
type ScanStatus struct {
	State Phase
	RunID string
	Full  bool
	// StartedAt and FinishedAt are Unix seconds; nil is the contract's null.
	StartedAt  *int64
	FinishedAt *int64
	// Roots are the root names included in this run.
	Roots       []string
	CurrentRoot string
	// CurrentItem is the root-relative path of the item being read.
	CurrentItem string
	// Total is books discovered so far — it grows during PhaseWalking.
	Total       int64
	Done        int64
	Errors      int64
	Skipped     int64
	CoversTotal int64
	CoversDone  int64
	ElapsedMs   int64
	// ETAMs is nil until a rate can be estimated (arch §7.10).
	ETAMs     *int64
	LastError string
	PerRoot   []RootProgress
}

// clone deep-copies the parts a reader could otherwise observe mutating.
func (s *ScanStatus) clone() *ScanStatus {
	out := *s
	if s.StartedAt != nil {
		v := *s.StartedAt
		out.StartedAt = &v
	}
	if s.FinishedAt != nil {
		v := *s.FinishedAt
		out.FinishedAt = &v
	}
	if s.ETAMs != nil {
		v := *s.ETAMs
		out.ETAMs = &v
	}
	out.Roots = append([]string(nil), s.Roots...)
	out.PerRoot = append([]RootProgress(nil), s.PerRoot...)
	return &out
}

// etaFloor is how much work must be behind us before an ETA is anything but
// noise. Below it the contract's `eta_ms: null` is the honest answer.
const (
	etaMinDone    = 8
	etaMinElapsed = 500 * time.Millisecond
)

// progress owns the working copy and the published snapshot.
type progress struct {
	now func() time.Time

	mu          sync.Mutex
	st          ScanStatus
	perRoot     map[string]*RootProgress
	order       []string
	started     time.Time
	lastPublish time.Time

	snap atomic.Pointer[ScanStatus]
}

func newProgress(now func() time.Time) *progress {
	p := &progress{now: now, perRoot: map[string]*RootProgress{}}
	p.st.State = PhaseIdle
	p.snap.Store(p.st.clone())
	return p
}

// Status returns the latest snapshot. Lock-free, allocation-free, and safe to
// call from any goroutine at any time, including while no scan is running.
func (p *progress) Status() *ScanStatus { return p.snap.Load() }

func (p *progress) begin(runID string, full bool, roots []string, at time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	unix := at.Unix()
	p.st = ScanStatus{
		State:     PhaseWalking,
		RunID:     runID,
		Full:      full,
		StartedAt: &unix,
		Roots:     append([]string(nil), roots...),
	}
	p.perRoot = make(map[string]*RootProgress, len(roots))
	p.order = nil
	p.started = at
	p.publishLocked(at)
}

func (p *progress) setPhase(ph Phase) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.st.State == ph {
		return
	}
	p.st.State = ph
	p.publishLocked(p.now()) // a phase change is always worth a poll cycle
}

func (p *progress) beginRoot(name string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.st.CurrentRoot = name
	p.st.CurrentItem = ""
	p.rootLocked(name)
	p.publishLocked(p.now())
}

func (p *progress) endRoot(name string, errMsg string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	r := p.rootLocked(name)
	r.Done = true
	r.Error = errMsg
	if errMsg != "" {
		p.st.LastError = errMsg
	}
	p.publishLocked(p.now())
}

// discovered is called by the walker as each series contributes its books.
func (p *progress) discovered(root string, series int, books int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	r := p.rootLocked(root)
	r.Series += int64(series)
	p.st.Total += int64(books)
	p.maybePublishLocked()
}

// current records the item a worker is reading (arch §7.10's current_item).
func (p *progress) current(root, relPath string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.st.CurrentRoot = root
	p.st.CurrentItem = relPath
	p.maybePublishLocked()
}

// bookDone folds one finished book into the counters.
func (p *progress) bookDone(root string, pages int64, skipped bool, status, errMsg string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	r := p.rootLocked(root)
	r.Books++
	r.Pages += pages
	p.st.Done++
	if skipped {
		r.Skipped++
		p.st.Skipped++
	}
	if isFailure(status) {
		r.Errors++
		p.st.Errors++
		p.st.LastError = errMsg
	}
	// Two transitions never wait for the window: the first result, which is what
	// tells a 1 Hz poller the run is alive rather than stuck, and the last one,
	// so the indexing phase does not finish reading "999 / 1000" while the
	// covers are generated.
	if p.st.Done == 1 || (p.st.Total > 0 && p.st.Done >= p.st.Total) {
		p.publishLocked(p.now())
		return
	}
	p.maybePublishLocked()
}

func (p *progress) coversQueued(n int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.st.CoversTotal += n
	p.maybePublishLocked()
}

func (p *progress) coversProgress(done, total int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.st.CoversDone, p.st.CoversTotal = done, total
	p.maybePublishLocked()
}

// finish returns the snapshot to idle with FinishedAt set.
func (p *progress) finish(at time.Time, cancelled bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	unix := at.Unix()
	p.st.State = PhaseIdle
	p.st.FinishedAt = &unix
	p.st.CurrentItem = ""
	p.st.CurrentRoot = ""
	if cancelled && p.st.LastError == "" {
		p.st.LastError = "scan cancelled"
	}
	p.publishLocked(at)
}

func (p *progress) rootLocked(name string) *RootProgress {
	r, ok := p.perRoot[name]
	if !ok {
		r = &RootProgress{Name: name}
		p.perRoot[name] = r
		p.order = append(p.order, name)
	}
	return r
}

func (p *progress) maybePublishLocked() {
	now := p.now()
	if now.Sub(p.lastPublish) < publishInterval {
		return
	}
	p.publishLocked(now)
}

func (p *progress) publishLocked(now time.Time) {
	p.lastPublish = now
	if p.st.StartedAt != nil {
		p.st.ElapsedMs = now.Sub(p.started).Milliseconds()
	}
	p.st.ETAMs = estimateETA(p.st.Done, p.st.Total, now.Sub(p.started))
	p.st.PerRoot = p.st.PerRoot[:0]
	for _, name := range p.order {
		p.st.PerRoot = append(p.st.PerRoot, *p.perRoot[name])
	}
	p.snap.Store(p.st.clone())
}

// estimateETA is arch §7.10's `eta_ms: number | null`. It returns nil until
// enough work is behind us for the extrapolation to mean anything, which is the
// difference between a useful number and a progress bar that jumps from 4 s to
// 9 min and back.
func estimateETA(done, total int64, elapsed time.Duration) *int64 {
	if done < etaMinDone || done >= total || elapsed < etaMinElapsed {
		return nil
	}
	perItem := float64(elapsed) / float64(done)
	eta := time.Duration(perItem * float64(total-done)).Milliseconds()
	return &eta
}
