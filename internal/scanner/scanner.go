// Package scanner turns the filesystem into the index. It is the component that
// decides what the user sees.
//
// # What it implements
//
// FR-IDX-001 (scan every root), FR-IDX-003 (incremental), FR-IDX-004 (live
// progress), FR-IDX-006 (exclusions), FR-IDX-010 (error isolation), FR-CFG-002
// (per-root enable), FR-CFG-005 (never write to a media volume), FR-THM-003
// (covers enqueued as each series completes) and amendment A-3
// (`scan.include_globs`). The classification table of prd §2.2 is realised
// literally in classify.go and collect.go; FR-IDX-002 (central directory only),
// FR-IDX-007 (natural sort), FR-IDX-008 (CP949) and FR-IDX-011 (the seven image
// extensions) are inherited from internal/source and internal/archive/zipidx and
// are not reimplemented here.
//
// # Pipeline (arch §4.1)
//
//	per root, sequentially:
//
//	  walkRoot ──▶ classify ──▶ [ scan.workers ] ──▶ results ──▶ writer
//	 (1 goroutine)  (in-line)     read central dirs   (buf 512)   (1 goroutine,
//	  os.ReadDir                  / readdir / pdf                  owns the
//	  through os.Root             page count                       write conn)
//	                                    │
//	                                    └──▶ coverQ (FR-THM-003)
//
// Directory enumeration is negligible (39 ms over the whole 11 157-archive
// tree), archive reading is I/O-bound and parallel, and there is exactly one
// writer goroutine, so SQLite write contention is removed by construction rather
// than by retry. The results channel is bounded, so a slow writer applies
// backpressure to the readers. Because the unit that crosses it is a whole
// series, the slot count alone does not bound memory: a second gate bounds the
// page rows those slots may hold, which is what actually keeps the heap flat
// (budget.go).
//
// # Two invariants
//
//   - **Nothing here writes to a media volume.** Every path under a root is
//     reached through that root's *os.Root and only ever opened, stat-ed or
//     read. `make lint`'s check-readonly grep fails the build if a mutation
//     primitive appears anywhere in this package, tests included (FR-CFG-005,
//     NFR-DAT-002, decision D-50).
//   - **No single item can fail the run.** Every per-book unit runs inside a
//     function that recovers panics and converts every failure into a
//     books.status plus one scan_log row (FR-IDX-010). Nine of 11 157 real
//     archives are truncated downloads and one is 0 bytes; the scan finishes and
//     the other 11 148 stay usable.
package scanner

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"shelf/internal/config"
	"shelf/internal/hangul"
	"shelf/internal/ids"
	"shelf/internal/index"
	"shelf/internal/natsort"
	"shelf/internal/source"
)

// resultBuffer is arch §4.1's bounded results channel: readers block when the
// writer falls behind. It bounds how many *series* may be in flight; what bounds
// the memory they hold is maxInFlightPages (budget.go), because the unit sent
// here is a whole series and a series is not a fixed size.
const resultBuffer = 512

// Cover-phase pacing. These are wall-clock waits on another package's queue, so
// they deliberately use the real clock rather than the injected one.
const (
	coverPollInterval = 100 * time.Millisecond
	coverWaitLimit    = 2 * time.Minute
)

// Errors callers match with errors.Is.
var (
	// ErrBusy reports that a scan is already running. The HTTP layer maps it to
	// `409 conflict` (arch §7.10).
	ErrBusy = errors.New("scanner: a scan is already running")
	// ErrClosed reports use of a Scanner after Close.
	ErrClosed = errors.New("scanner: closed")
	// ErrUnknownRoot reports a Request naming a root that is not in the
	// configuration. A *disabled* root is not an error — it is skipped.
	ErrUnknownRoot = errors.New("scanner: unknown root")
	// errRootUnreachable is the root-level abort of arch §4.9: the drive is
	// unmounted or the path is gone. Nothing is swept and last_scan_error is
	// set instead.
	errRootUnreachable = errors.New("root is unreachable")
)

// BookLister is the narrow view of internal/source the scanner needs.
// *source.Factory satisfies it. Declared by the consumer, per impl-plan §5.1.
type BookLister interface {
	Open(ctx context.Context, b source.Book) (source.BookSource, error)
}

// Options configures a Scanner.
type Options struct {
	// Index is the derived catalogue this scan writes. Required.
	Index *index.DB
	// Books opens one book so its pages can be enumerated. Required.
	Books BookLister
	// Roots holds one *os.Root per enabled root — path-traversal layer 3 and
	// the only way this package reaches a media volume. Required.
	Roots *source.RootSet
	// ConfigRoots is `roots:` from the YAML, disabled entries included: a
	// disabled root is kept in the index and merely hidden from listings
	// (FR-CFG-002), so it still has to be upserted.
	ConfigRoots []config.Root
	// Scan is the `scan:` block. Zero values resolve to the documented
	// defaults, so a test may pass config.Scan{}.
	Scan config.Scan
	// Covers receives one request per series as it completes (FR-THM-003).
	// nil disables cover enqueueing entirely, which is what the scanner's own
	// tests use.
	Covers CoverQueue
	// Seen records when each series was first sighted, in user.db, so that
	// 최근 추가 survives --rebuild-index (amendment A-8). *userdata.DB satisfies
	// it; nil disables the recording and nothing else.
	Seen SeriesSeenWriter
	// AfterScan runs once, on the scan's own goroutine, after a run has
	// finished — cancelled and failed runs included. nil disables it.
	//
	// It exists so that reattaching reading progress can be a consequence of
	// scanning rather than a separate thing an operator has to know about: a
	// rename or a container split changes what ids the index holds, and the
	// moment that is true is the moment the repair can see it. The scanner
	// itself knows nothing about progress — this is a callback for the same
	// reason `Covers` and `Seen` are, and for the stronger reason that this
	// package must not write to user.db at all (arch §3.7).
	//
	// It runs after the sweep and the log trim, with a context that is already
	// cancelled if the scan was, so a hook that wants to skip cancelled runs can
	// check `res.Cancelled`. A panic or a slow hook delays `Wait`, so keep it
	// short and let it fail silently on its own terms.
	AfterScan func(ctx context.Context, res *Result)
	// Logger; nil selects slog.Default().
	Logger *slog.Logger
	// Now is the clock. nil selects time.Now.
	Now func() time.Time
	// WriterOptions tunes the index commit cadence. Zero selects 200 books / 2 s.
	WriterOptions index.WriterOptions
}

// Scanner runs scans. One value serves the process; at most one scan runs at a
// time.
type Scanner struct {
	index *index.DB
	books BookLister
	roots *source.RootSet
	// cfgRoots is `roots:` as this process understands it, and since ruling E-40
	// it can grow while the server runs — `POST /api/roots` adopts an addition
	// without a restart. Every read goes through configRoots(); reading the
	// field directly is the race this mutex exists to stop.
	cfgMu         sync.RWMutex
	cfgRoots      []config.Root
	covers        CoverQueue
	seen          SeriesSeenWriter
	afterScan     func(context.Context, *Result)
	log           *slog.Logger
	now           func() time.Time
	writerOptions index.WriterOptions

	workers        int
	maxDepth       int
	coverMaxLoose  int
	followSymlinks bool
	excludeGlobs   globSet
	includeGlobs   globSet

	progress *progress
	// pages bounds the index.Page rows the pipeline holds between a worker and
	// the writer. One scan runs at a time, so one budget serves the Scanner.
	pages *pageBudget

	mu      sync.Mutex
	running bool
	closed  bool
	cancel  context.CancelFunc
	done    chan struct{}
	wg      sync.WaitGroup
}

// New returns a Scanner. It performs no I/O.
func New(opts Options) (*Scanner, error) {
	if opts.Index == nil {
		return nil, errors.New("scanner: Options.Index is nil")
	}
	if opts.Books == nil {
		return nil, errors.New("scanner: Options.Books is nil")
	}
	if opts.Roots == nil {
		return nil, errors.New("scanner: Options.Roots is nil")
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	s := &Scanner{
		index:          opts.Index,
		books:          opts.Books,
		roots:          opts.Roots,
		cfgRoots:       append([]config.Root(nil), opts.ConfigRoots...),
		covers:         opts.Covers,
		seen:           opts.Seen,
		afterScan:      opts.AfterScan,
		log:            log,
		now:            now,
		writerOptions:  opts.WriterOptions,
		workers:        resolveWorkers(opts.Scan.Workers),
		maxDepth:       opts.Scan.MaxDepth,
		coverMaxLoose:  opts.Scan.CoverMaxLooseImages,
		followSymlinks: opts.Scan.FollowSymlinks,
		excludeGlobs:   globSet(opts.Scan.ExcludeGlobs),
		includeGlobs:   globSet(opts.Scan.IncludeGlobs),
		progress:       newProgress(now),
		pages:          newPageBudget(maxInFlightPages),
	}
	if s.maxDepth < 0 {
		s.maxDepth = 0
	}
	if opts.Scan.CoverMaxLooseImages == 0 {
		// arch §3.2's default. Zero would turn every one of the 47 real
		// "N archives + one cover" directories into a spurious one-page book.
		s.coverMaxLoose = 3
	}
	return s, nil
}

// resolveWorkers implements arch §3.2's `workers: 0 => min(8, max(2, NumCPU/2))`.
// The work is I/O-bound: 4 workers measured 147 archives/s cold, 16 measured 346.
func resolveWorkers(n int) int {
	if n > 0 {
		return n
	}
	return min(8, max(2, runtime.NumCPU()/2))
}

// SeriesRef names one series for a targeted rescan (`POST /api/series/{sid}/rescan`).
type SeriesRef struct {
	// Root is the root name. Empty matches every root in the run.
	Root string
	// RelPath is the series' root-relative path.
	RelPath string
}

// Request is one scan.
type Request struct {
	// Roots restricts the run to these root names. Empty means every enabled
	// root. A name that is not in the configuration is ErrUnknownRoot; a name
	// that is configured but disabled is skipped (FR-CFG-002).
	Roots []string
	// Full bypasses every incremental skip (`--rebuild-index`,
	// `POST /api/scan {"full": true}`).
	Full bool
	// Series restricts the run to named series. A targeted run never sweeps:
	// absence of a row is not evidence of absence on disk when only part of the
	// tree was visited.
	Series []SeriesRef
}

func (r Request) targeted() bool { return len(r.Series) > 0 }

// RootResult is what one root's pass produced.
type RootResult struct {
	Name    string
	Series  int64
	Books   int64
	Pages   int64
	Skipped int64
	Errors  int64
	Swept   index.SweepResult
	// Relocations are the books this root's sweep proved had merely moved.
	Relocations []index.Relocation
	SweepNote   string
	Err         error
	StartedAt   time.Time
	FinishedAt  time.Time
}

// Result is one whole run.
type Result struct {
	RunID      string
	Full       bool
	ScanGen    int64
	StartedAt  time.Time
	FinishedAt time.Time
	Roots      []RootResult
	Cancelled  bool
}

// Totals sums the per-root counters.
func (r *Result) Totals() (series, books, pages, skipped, errs int64) {
	for _, rr := range r.Roots {
		series += rr.Series
		books += rr.Books
		pages += rr.Pages
		skipped += rr.Skipped
		errs += rr.Errors
	}
	return
}

// Status returns the latest progress snapshot (FR-IDX-004). It is a lock-free
// atomic load and is safe to call at any time, including before the first scan
// and concurrently with one.
func (s *Scanner) Status() *ScanStatus { return s.progress.Status() }

// Run performs one scan and returns when it is finished.
func (s *Scanner) Run(ctx context.Context, req Request) (*Result, error) {
	runID, err := newRunID()
	if err != nil {
		return nil, err
	}
	runCtx, release, err := s.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	begun, err := s.begin(runID, req)
	if err != nil {
		return nil, err
	}
	return s.run(runCtx, req, runID, begun)
}

// Start launches a scan in the background and returns its run id, which is what
// `POST /api/scan` answers with. ErrBusy means a scan is already running
// (`409 conflict`); ErrUnknownRoot means the request named a root that is not
// configured (`400 bad_param`).
//
// The status snapshot is published **before this returns**, and that ordering is
// the contract rather than an implementation detail. The HTTP layer writes its
// 202 the moment it has the run id, and the client answers a 202 by polling
// `GET /api/scan/status` once and then stopping if the state is idle. A run that
// left idle only after Start returned could therefore be missed entirely by the
// one poll that mattered, and the UI would read `스캔 대기` for the whole run with
// no second chance (arch §7.10, conflict resolution C-11). Publishing first
// makes that unobservable: any snapshot a caller can reach after Start belongs
// to this run.
//
// The run is deliberately detached from ctx's cancellation — an HTTP handler's
// context ends when the response is written, and a scan that died the moment its
// 202 was flushed would be useless. Values (a request-scoped logger, say) are
// preserved. Use Cancel to stop it.
func (s *Scanner) Start(ctx context.Context, req Request) (string, error) {
	runID, err := newRunID()
	if err != nil {
		return "", err
	}
	runCtx, release, err := s.acquire(context.WithoutCancel(ctx))
	if err != nil {
		return "", err
	}
	begun, err := s.begin(runID, req)
	if err != nil {
		// Nothing was published, so there is no run to finish. Refusing here
		// rather than inside the goroutine is also what makes the ErrUnknownRoot
		// branch of httpapi.scanStartError reachable at all.
		release()
		return "", err
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer release()
		if _, err := s.run(runCtx, req, runID, begun); err != nil {
			s.log.Error("scan failed", "run_id", runID, "err", err)
		}
	}()
	return runID, nil
}

// begunRun is what a scan resolves before it can be said to have started: the
// roots it will visit and the instant it started at. It exists so that Start can
// do this work synchronously, on the caller's goroutine, and hand the result to
// the background run.
type begunRun struct {
	roots []config.Root
	names []string
	start time.Time
}

// begin resolves the request and publishes the run's opening status. The caller
// must already hold the one-scan permit, or this would overwrite the progress of
// a scan that is still running.
//
// A request that names an unknown root fails here, before anything is published:
// a status that no goroutine will ever finish is worse than an error the caller
// can act on.
func (s *Scanner) begin(runID string, req Request) (*begunRun, error) {
	roots, err := s.selectRoots(req)
	if err != nil {
		return nil, err
	}
	b := &begunRun{roots: roots, start: s.now(), names: make([]string, 0, len(roots))}
	for _, r := range roots {
		b.names = append(b.names, r.Name)
	}
	s.progress.begin(runID, req.Full, b.names, b.start)
	return b, nil
}

// Cancel asks a running scan to stop. It reports whether there was one. The
// writer commits what it has (arch §4.1) and no generation sweep runs, so a
// cancelled scan can never delete a row.
func (s *Scanner) Cancel() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running || s.cancel == nil {
		return false
	}
	s.progress.setPhase(PhaseCancelling)
	s.cancel()
	return true
}

// Wait blocks until the running scan, if any, has finished.
func (s *Scanner) Wait() {
	s.mu.Lock()
	done, running := s.done, s.running
	s.mu.Unlock()
	if running && done != nil {
		<-done
	}
}

// Close cancels any running scan and waits for its goroutines. It is idempotent.
func (s *Scanner) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.wg.Wait()
	s.Wait()
	return nil
}

// acquire takes the one-scan-at-a-time permit.
func (s *Scanner) acquire(ctx context.Context) (context.Context, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, nil, ErrClosed
	}
	if s.running {
		return nil, nil, ErrBusy
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	s.running, s.cancel, s.done = true, cancel, done

	var once sync.Once
	release := func() {
		once.Do(func() {
			cancel()
			s.mu.Lock()
			s.running, s.cancel = false, nil
			s.mu.Unlock()
			close(done)
		})
	}
	return runCtx, release, nil
}

// cancelled reports whether err is (or wraps) a context cancellation, which is
// the one failure mode a scan treats as a clean, expected ending rather than an
// error to report (arch §4.1: `POST /api/scan/cancel` commits what it has).
func cancelled(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// abortedBy reports whether err is the consequence of ctx being cancelled.
//
// The context check is not redundant with errors.Is. SQLite's interrupt handler
// surfaces a cancelled query as a driver-level `interrupted (9)`, which wraps
// nothing and cannot be matched — so the only reliable question is whether the
// context that failed had already been cancelled. Getting this wrong turns
// `POST /api/scan/cancel` into a scan that reports an error.
func abortedBy(ctx context.Context, err error) bool {
	return err != nil && (ctx.Err() != nil || cancelled(err))
}

func newRunID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generating a scan run id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// run is the body of one scan. The run has already been published by begin, so
// every exit from here has to end at progress.finish — which is why the deferred
// finish is the first statement.
func (s *Scanner) run(ctx context.Context, req Request, runID string, begun *begunRun) (*Result, error) {
	roots, names, start := begun.roots, begun.names, begun.start

	res := &Result{RunID: runID, Full: req.Full, StartedAt: start}
	defer func() {
		res.FinishedAt = s.now()
		res.Cancelled = ctx.Err() != nil
		s.progress.finish(res.FinishedAt, res.Cancelled)
	}()

	// The roots table mirrors the configuration on every run, disabled entries
	// included: FR-CFG-002 says disabling hides a root from listings, never
	// destroys what it points at.
	for _, r := range s.configRoots() {
		p, _ := s.roots.Path(r.Name)
		if p == "" {
			p = r.Path
		}
		if err := s.index.UpsertRoot(ctx, index.Root{
			Name: r.Name, Path: p, Label: r.Label, Enabled: r.Enabled,
		}); err != nil {
			if abortedBy(ctx, err) {
				return res, nil
			}
			return res, fmt.Errorf("recording root %q: %w", r.Name, err)
		}
	}

	gen, err := s.index.NextScanGen(ctx)
	if err != nil {
		// A run cancelled before it allocated a generation is not a failure —
		// it is a cancellation, and the caller gets a clean, empty result.
		if abortedBy(ctx, err) {
			return res, nil
		}
		return res, err
	}
	res.ScanGen = gen
	s.log.Info("scan started", "run_id", runID, "scan_gen", gen,
		"full", req.Full, "roots", names, "workers", s.workers)

	seen := s.beginSeen(ctx, start.Unix())

	s.progress.setPhase(PhaseWalking)
	for _, r := range roots {
		if ctx.Err() != nil {
			break
		}
		res.Roots = append(res.Roots, s.scanRoot(ctx, runID, gen, r, req, seen))
	}
	s.finishSeen(ctx, seen, req, len(roots), res)

	s.waitForCovers(ctx)

	if _, err := s.index.TrimLog(context.WithoutCancel(ctx), index.LogRetention); err != nil {
		s.log.Warn("trimming the scan log", "run_id", runID, "err", err)
	}

	series, books, pages, skipped, errCount := res.Totals()
	s.log.Info("scan finished", "run_id", runID,
		"series", series, "books", books, "pages", pages,
		"skipped", skipped, "errors", errCount,
		"dur_ms", s.now().Sub(start).Milliseconds(), "cancelled", ctx.Err() != nil)

	// After the log line, so the hook's own lines read as consequences of a scan
	// that has already reported itself, and after the sweep, so it sees the
	// index the filesystem actually justifies.
	if s.afterScan != nil {
		s.afterScan(ctx, res)
	}
	return res, nil
}

// configRoots is the snapshot every reader of `roots:` takes.
//
// A copy rather than the slice: callers range over it while a scan runs, and
// since E-40 an `AddConfigRoot` can append during exactly that window. Handing
// out the backing array would make the append visible mid-range on some paths
// and not others.
func (s *Scanner) configRoots() []config.Root {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return append([]config.Root(nil), s.cfgRoots...)
}

// AddConfigRoot adopts a root added to the configuration file while this server
// runs — amendment A-12, ruling E-40.
//
// It is the scanner's half of the hot add. `source.RootSet.Add` opens the
// handle; this makes the root *selectable*, which is what lets the very next
// `POST /api/scan {roots:[name]}` find it instead of answering
// `ErrUnknownRoot`. Both are needed and neither implies the other.
//
// A name already present is a no-op rather than an error: the caller's job is
// to make the configuration true here, and it already is.
func (s *Scanner) AddConfigRoot(r config.Root) {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	for _, existing := range s.cfgRoots {
		if existing.Name == r.Name {
			return
		}
	}
	s.cfgRoots = append(s.cfgRoots, r)
}

// selectRoots resolves Request.Roots against the configuration.
func (s *Scanner) selectRoots(req Request) ([]config.Root, error) {
	cfgRoots := s.configRoots()
	byName := make(map[string]config.Root, len(cfgRoots))
	for _, r := range cfgRoots {
		byName[r.Name] = r
	}
	if len(req.Roots) == 0 {
		out := make([]config.Root, 0, len(cfgRoots))
		for _, r := range cfgRoots {
			if r.Enabled {
				out = append(out, r)
			}
		}
		return out, nil
	}
	out := make([]config.Root, 0, len(req.Roots))
	for _, name := range req.Roots {
		r, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("%w %q", ErrUnknownRoot, name)
		}
		if !r.Enabled {
			s.log.Info("skipping a disabled root", "root", name)
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// rootRun is the per-root context shared by the walker, the workers and the
// writer. Everything in it is immutable for the duration of the root's pass
// except logs, which has its own lock.
type rootRun struct {
	cfg            config.Root
	root           *os.Root
	runID          string
	gen            int64
	full           bool
	followSymlinks bool
	excludeGlobs   globSet
	includeGlobs   globSet
	// priorSeries is every series row the index already holds for this root,
	// prefetched once so the walk never queries per series (FR-IDX-003). It is
	// empty on a full scan, where nothing may be skipped anyway.
	priorSeries map[string]index.Series
	// seen collects this root's first sightings for one write at the end of the
	// pass (amendment A-8). nil when the run records none.
	seen *seenBatch
	logs *logBuffer
	log  *slog.Logger
	now  func() time.Time
}

// note records an entry the exclusion rules dropped. It is debug-level and does
// not reach scan_log: `Thumbs.db` alone accounts for 125 exclusions in a
// 508-archive sample, and a scan_log row per excluded page would bury the nine
// rows an operator actually needs to see.
func (rt *rootRun) note(relPath, reason string) {
	rt.log.Debug("excluded from the scan",
		append(logAttrs(rt.runID, rt.cfg.Name, relPath), "reason", reason)...)
}

// info records one scan_log info row. arch §4.2 asks for exactly one per root
// child that is not a series — the stray `.rar` and `.DS_Store` at the top of
// the real collection.
func (rt *rootRun) info(relPath, message string) {
	rt.logs.add(rt.entry(index.LevelInfo, relPath, message))
	rt.log.Debug("ignored a root child", append(logAttrs(rt.runID, rt.cfg.Name, relPath),
		"message", message)...)
}

// warn records one scan_log warn row. FR-IDX-010 wants exactly one per isolated
// failure.
func (rt *rootRun) warn(relPath string, err error) {
	rt.logs.add(rt.entry(index.LevelWarn, relPath, err.Error()))
	rt.log.Warn("isolated a scan failure",
		append(logAttrs(rt.runID, rt.cfg.Name, relPath), "err", err)...)
}

func (rt *rootRun) entry(level, relPath, message string) index.LogEntry {
	return index.LogEntry{
		TS: rt.now().Unix(), RunID: rt.runID, Level: level,
		Root: rt.cfg.Name, RelPath: relPath, Message: message,
	}
}

// logBuffer collects scan_log rows produced outside the writer goroutine.
//
// It exists because index.Writer holds the process write permit for the whole of
// an open batch: a walker goroutine calling the DB-level AppendLog mid-batch
// would wait for a commit only the writer can make. Buffering and letting the
// writer drain it keeps the single-writer rule intact with no extra channel.
type logBuffer struct {
	mu      sync.Mutex
	entries []index.LogEntry
	dropped int64
}

// logBufferMax bounds the buffer at the scan log's own retention, so a
// pathological tree cannot turn diagnostics into a memory leak.
const logBufferMax = index.LogRetention

func (b *logBuffer) add(entries ...index.LogEntry) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, e := range entries {
		if len(b.entries) >= logBufferMax {
			b.dropped++
			continue
		}
		b.entries = append(b.entries, e)
	}
}

func (b *logBuffer) take() []index.LogEntry {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.entries) == 0 {
		return nil
	}
	out := b.entries
	b.entries = nil
	return out
}

func (b *logBuffer) droppedCount() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.dropped
}

// seriesTask carries one series from the walker, through the workers, to the
// writer.
type seriesTask struct {
	unit  *seriesUnit
	id    string
	books []bookUnit
	// priorSeries is the series row as the index currently holds it. An empty
	// ID means the series is new, which also means none of its books can have a
	// prior row under it — so the workers skip their own lookup entirely and a
	// cold scan costs no queries at all.
	priorSeries index.Series
	results     []bookResult
	// remaining is decremented by each worker; the one that reaches zero owns
	// handing the task to the writer. The atomic read-modify-write is what
	// publishes every worker's slot write to the writer goroutine.
	remaining atomic.Int64
	// heldPages is the page budget this task took on its way into the results
	// channel, and that the writer must give back. It is written by the
	// delivering worker before the send and read by the writer after the
	// receive, so the channel itself orders the two.
	heldPages int64
}

// pageRows is how much of the index this task is carrying.
func (t *seriesTask) pageRows() int64 {
	var n int64
	for i := range t.results {
		n += int64(len(t.results[i].pages))
	}
	return n
}

type bookJob struct {
	task  *seriesTask
	index int
}

// bookResult is one finished book.
type bookResult struct {
	id   string
	unit bookUnit

	status         string
	errMsg         string
	pageCount      int64
	totalBytes     int64
	contentVersion string
	nameEncoding   string
	dimsState      string
	pages          []index.Page

	// skipped marks a book FR-IDX-003 left alone, together with the position it
	// held last time: an unchanged book that *moved* still needs its `ord`
	// rewritten, or inserting one volume would misorder every later one.
	skipped       bool
	priorOrd      int
	priorSeriesID string

	// aborted marks a book abandoned because the run was cancelled. Its series
	// is not written at all, so a cancelled scan never records a spurious
	// error status.
	aborted bool

	// expanded is set when this book turned out to be a *container* of volumes
	// rather than a book: a ZIP whose entries are all more ZIPs. The results in
	// it replace this one entirely (see expandContainers), which is how
	// `겟 벡커스 1~39완.zip` stops being one empty book and becomes 39 real ones.
	expanded []bookResult

	logs []index.LogEntry
}

// scanRoot runs the whole pipeline for one root.
func (s *Scanner) scanRoot(ctx context.Context, runID string, gen int64, cfg config.Root,
	req Request, seen seenRun,
) RootResult {
	out := RootResult{Name: cfg.Name, StartedAt: s.now()}
	s.progress.beginRoot(cfg.Name)

	if err := s.index.MarkRootScanStart(ctx, cfg.Name, out.StartedAt.Unix()); err != nil {
		if !abortedBy(ctx, err) {
			out.Err = fmt.Errorf("stamping the scan start of root %q: %w", cfg.Name, err)
		}
		s.finishRoot(ctx, &out)
		return out
	}

	root, ok := s.roots.Root(cfg.Name)
	if !ok {
		out.Err = unreachableError(cfg)
		s.log.Error("root is unreachable; nothing will be swept",
			"run_id", runID, "root", cfg.Name, "path", cfg.Path, "err", out.Err)
		s.finishRoot(ctx, &out)
		return out
	}

	rt := &rootRun{
		cfg: cfg, root: root, runID: runID, gen: gen, full: req.Full,
		followSymlinks: s.followSymlinks,
		excludeGlobs:   s.excludeGlobs,
		includeGlobs:   s.includeGlobs,
		seen:           seen.newBatch(),
		logs:           &logBuffer{},
		log:            s.log,
		now:            s.now,
	}
	if !req.Full {
		rt.priorSeries = s.priorSeriesRows(ctx, cfg.Name)
	}

	pipeCtx, abort := context.WithCancel(ctx)
	defer abort()

	bookCh := make(chan bookJob, s.workers*2)
	resCh := make(chan *seriesTask, resultBuffer)

	var walkErr error
	var walkWG, workerWG sync.WaitGroup

	walkWG.Add(1)
	go func() {
		defer walkWG.Done()
		defer close(bookCh)
		walkErr = s.walkRoot(pipeCtx, rt, req, func(t *seriesTask) error {
			s.progress.discovered(rt.cfg.Name, 1, len(t.books))
			if len(t.books) == 0 {
				// A series with no books carries no page rows, so it needs no
				// budget — but it still goes through the same door.
				return s.deliver(pipeCtx, resCh, t)
			}
			t.results = make([]bookResult, len(t.books))
			t.remaining.Store(int64(len(t.books)))
			for i := range t.books {
				select {
				case bookCh <- bookJob{task: t, index: i}:
				case <-pipeCtx.Done():
					return pipeCtx.Err()
				}
			}
			return nil
		})
	}()

	s.progress.setPhase(PhaseIndexing)
	for range s.workers {
		workerWG.Add(1)
		go func() {
			defer workerWG.Done()
			for job := range bookCh {
				t := job.task
				t.results[job.index] = s.indexBook(pipeCtx, rt, t, job.index)
				if t.remaining.Add(-1) != 0 {
					continue
				}
				// Every book of this series is finished, so this goroutine owns
				// the task outright and can rewrite its book list.
				flattenExpanded(t)
				if err := s.deliver(pipeCtx, resCh, t); err != nil {
					return
				}
			}
		}()
	}

	go func() {
		walkWG.Wait()
		workerWG.Wait()
		close(resCh)
	}()

	writeErr := s.writeResults(pipeCtx, rt, resCh, &out, abort)

	switch {
	// The write path runs on an uncancellable context, so a write failure is
	// always genuine — and it must not be masked by the abort() it just
	// triggered, which is exactly what testing pipeCtx here would do.
	case writeErr != nil && !cancelled(writeErr):
		out.Err = writeErr
	case walkErr != nil && !abortedBy(pipeCtx, walkErr):
		out.Err = walkErr
	}

	// After the index writer has closed, so the two databases are never written
	// inside one another's transaction (arch §3.7).
	s.flushSeen(ctx, rt)

	decision := decideSweep(out.Err, ctx.Err() != nil, req.targeted())
	out.SweepNote = decision.reason
	if swept, moved, err := s.sweepRoot(context.WithoutCancel(ctx), cfg.Name, gen, decision); err != nil {
		s.log.Error("sweep failed", "run_id", runID, "root", cfg.Name, "err", err)
		if out.Err == nil {
			out.Err = err
		}
	} else {
		out.Swept = swept
		out.Relocations = moved
	}

	if dropped := rt.logs.droppedCount(); dropped > 0 {
		s.log.Warn("scan log entries dropped", "run_id", runID, "root", cfg.Name, "dropped", dropped)
	}
	s.finishRoot(ctx, &out)
	return out
}

// finishRoot recounts the root and stamps the end of its pass. Both are
// DB-level writes, so they run only after the root's index.Writer is closed.
func (s *Scanner) finishRoot(ctx context.Context, out *RootResult) {
	// The bookkeeping must survive a cancelled run, or a cancelled scan would
	// leave last_scan_start set for ever with no end.
	ctx = context.WithoutCancel(ctx)
	out.FinishedAt = s.now()
	errMsg := ""
	if out.Err != nil {
		errMsg = out.Err.Error()
	}
	if err := s.index.RecountRoot(ctx, out.Name); err != nil {
		s.log.Warn("recounting a root", "root", out.Name, "err", err)
	}
	if err := s.index.MarkRootScanEnd(ctx, out.Name, out.FinishedAt.Unix(), errMsg); err != nil {
		s.log.Warn("stamping the scan end of a root", "root", out.Name, "err", err)
	}
	s.progress.endRoot(out.Name, errMsg)
}

// unreachableError explains why a root could not be opened, without leaking a
// path from outside roots[].path into the message (impl-plan §5.1).
func unreachableError(cfg config.Root) error {
	if _, err := os.Stat(cfg.Path); err != nil {
		return fmt.Errorf("%w: %w", errRootUnreachable, err)
	}
	return fmt.Errorf("%w: %s", errRootUnreachable, cfg.Path)
}

// walkRoot enumerates a root's direct children and classifies each one
// (prd §1.3: a series is exactly one direct child of a root).
func (s *Scanner) walkRoot(ctx context.Context, rt *rootRun, req Request, emit func(*seriesTask) error) error {
	children, err := readDir(rt.root, "", rt.followSymlinks)
	if err != nil {
		return fmt.Errorf("walking root %q: %w", rt.cfg.Name, err)
	}

	var wanted map[string]bool
	if req.targeted() {
		wanted = make(map[string]bool, len(req.Series))
		for _, ref := range req.Series {
			if ref.Root == "" || ref.Root == rt.cfg.Name {
				wanted[ids.NormalizeRel(ref.RelPath)] = true
			}
		}
	}

	for _, c := range children {
		if err := ctx.Err(); err != nil {
			return err
		}
		if wanted != nil && !wanted[c.rel] {
			continue
		}
		if c.skip != "" {
			rt.note(c.rel, c.skip)
			continue
		}
		if !rt.excludeGlobs.empty() && rt.excludeGlobs.matchPath(c.rel) {
			rt.note(c.rel, reasonExcludeGlob)
			continue
		}
		// Amendment A-3 / ruling E-6: an allowlist over base names, applied
		// before classification. It is how the E2E suite indexes ten named
		// series inside a 414 GB collection without copying a byte.
		if !rt.includeGlobs.matchBase(c.name) {
			rt.note(c.rel, reasonNotIncluded)
			continue
		}

		unit, ok := s.classifyChild(rt, c)
		if !ok {
			rt.info(c.rel, "ignored: "+reasonNotAContainer)
			continue
		}
		if unit.err != nil {
			rt.warn(c.rel, unit.err)
		}
		s.progress.current(rt.cfg.Name, c.rel)

		task := &seriesTask{unit: unit, id: ids.SeriesID(rt.cfg.Name, unit.relPath), books: unit.books}
		task.priorSeries = rt.priorSeries[task.id]
		if err := emit(task); err != nil {
			return err
		}
	}
	return nil
}

// priorSeriesRows loads every series row of one root, in pages, before the walk
// starts. The writer needs them to tell an unchanged series row from a changed
// one, and the walker needs to know whether a series is new at all.
//
// It is a whole-root prefetch rather than a lookup per series on purpose.
// index.ListSeries is issued as an ordinary statement, so SQLite parses it on
// every call — five pages for a thousand series is five parses, where a lookup
// per series would be a thousand, and on a no-change rescan that parsing was
// measurably the single largest cost in the run.
func (s *Scanner) priorSeriesRows(ctx context.Context, rootName string) map[string]index.Series {
	const page = 200 // index.maxSeriesLimit
	out := map[string]index.Series{}
	for offset := 0; ; offset += page {
		list, err := s.index.ListSeries(ctx, index.SeriesFilter{
			Roots: []string{rootName}, Status: "all", IncludeDisabledRoots: true,
			Offset: offset, Limit: page,
		})
		if err != nil {
			s.log.Debug("reading prior series rows", "root", rootName, "err", err)
			return out
		}
		for _, r := range list.Items {
			out[r.ID] = r.Series
		}
		if len(list.Items) < page || offset+len(list.Items) >= list.Total {
			return out
		}
	}
}

// priorBook is the FR-IDX-003 lookup for one book. It runs on the workers, in
// parallel, against index.DB's *prepared* book statement — which is why it is a
// per-book query and the series rows above are a bulk prefetch.
func (s *Scanner) priorBook(ctx context.Context, bookID string) (index.Book, bool) {
	row, err := s.index.GetBook(ctx, bookID)
	if err != nil {
		if !errors.Is(err, index.ErrNotFound) && !cancelled(err) {
			s.log.Debug("reading prior book state", "book_id", bookID, "err", err)
		}
		return index.Book{}, false
	}
	return row.Book, true
}

// indexBook enumerates one book's pages, or explains why it could not.
//
// This is FR-IDX-010's isolation boundary. Every failure — a missing end record,
// a truncated central directory, an encrypted archive, an unsupported method, a
// vanished file, a PDF in a nopdf build, and a panic in any of the above —
// becomes a books.status and a scan_log row. Nothing here returns an error to
// its caller, because there is no failure the caller could usefully act on: the
// scan must complete.
func (s *Scanner) indexBook(ctx context.Context, rt *rootRun, t *seriesTask, i int) bookResult {
	return s.indexUnit(ctx, rt, t, t.books[i])
}

// indexUnit indexes one book unit. It is separate from indexBook because a
// container of volumes indexes several units that classification never saw —
// see expandContainer.
func (s *Scanner) indexUnit(ctx context.Context, rt *rootRun, t *seriesTask, u bookUnit) (res bookResult) {
	res = bookResult{
		id: ids.NestedBookID(rt.cfg.Name, u.relPath, u.innerPath), unit: u,
		status: StatusOK, dimsState: "none",
	}

	defer func() {
		if r := recover(); r != nil {
			res.status = StatusError
			res.errMsg = fmt.Sprintf("panic while indexing: %v", r)
			res.pages, res.pageCount, res.totalBytes = nil, 0, 0
			res.skipped = false
			res.contentVersion = contentVersion(u.kind, u.size, u.mtime, u.fingerprint)
			res.logs = append(res.logs, rt.entry(index.LevelError, u.relPath, res.errMsg))
			s.log.Error("recovered a panic while indexing a book",
				append(logAttrs(rt.runID, rt.cfg.Name, u.relPath),
					"book_id", res.id, "panic", fmt.Sprint(r))...)
		}
		// A container that turned out to hold books is not one of them, and it
		// has already told progress how many it became (expandContainer,
		// expandChapters). Counting it too would put `done` past `total` — one
		// extra per container under D-70, and 6,097 extra under D-73, which is a
		// progress bar that runs past its own end.
		if !res.aborted && len(res.expanded) == 0 {
			s.progress.bookDone(rt.cfg.Name, res.pageCount, res.skipped, res.status, res.errMsg)
		}
	}()

	if ctx.Err() != nil {
		res.aborted = true
		return res
	}

	// A series the index has never seen cannot have a prior row for this book,
	// so a cold scan never issues the lookup at all.
	if !rt.full && t.priorSeries.ID != "" {
		if prior, ok := s.priorBook(ctx, res.id); ok && unchanged(u, prior, rt.full) {
			res.skipped = true
			res.status = prior.Status
			res.errMsg = prior.Error
			res.pageCount = prior.PageCount
			res.totalBytes = prior.TotalBytes
			res.contentVersion = prior.ContentVersion
			res.dimsState = prior.DimsState
			res.priorOrd, res.priorSeriesID = prior.Ord, prior.SeriesID
			return res
		}
	}

	s.progress.current(rt.cfg.Name, u.relPath)
	res.contentVersion = contentVersion(u.kind, u.size, u.mtime, u.fingerprint)

	src, err := s.books.Open(ctx, source.Book{
		ID: res.id, Kind: u.kind, RootName: rt.cfg.Name, RelPath: u.relPath,
		InnerPath: u.innerPath, FileSize: u.size, FileMtime: u.mtime,
	})
	if err != nil {
		// A book abandoned because the run was cancelled is not a broken book.
		// Without this, cancelling a scan would stamp `status='error'` onto
		// every volume that happened to be in flight.
		if ctx.Err() != nil {
			res.aborted = true
			return res
		}
		return s.bookFailure(rt, res, err)
	}
	defer func() { _ = src.Close() }()

	listing, listErr := src.List(ctx)
	if listing != nil {
		res.nameEncoding = listing.NameEncoding
		res.pages = s.convertPages(rt, u, listing)
		res.pageCount = int64(len(res.pages))
		for i := range res.pages {
			res.totalBytes += res.pages[i].Size
		}
	}
	if listErr != nil {
		if ctx.Err() != nil {
			res.aborted = true
			return res
		}
		// "Nothing in here is a page" is how a container of volumes announces
		// itself, so that verdict is re-examined before it is recorded.
		if errors.Is(listErr, source.ErrNoPages) {
			if expanded, ok := s.expandContainer(ctx, rt, t, res); ok {
				return expanded
			}
		}
		// A partially readable central directory keeps the pages that parsed:
		// the truncated `군계(軍鷄) 07권.zip` still shows most of its volume, and
		// FR-IDX-010 asks for isolation, not deletion.
		return s.bookFailure(rt, res, listErr)
	}
	if res.pageCount == 0 {
		// Before calling it empty: a container of volumes has no pages of its
		// own precisely because its entries are whole books. This is the only
		// place the check runs, so an ordinary book never pays for it.
		if expanded, ok := s.expandContainer(ctx, rt, t, res); ok {
			return expanded
		}
		res.status = StatusEmpty
		res.errMsg = "no supported image entries"
		res.logs = append(res.logs, rt.entry(index.LevelWarn, u.relPath, res.errMsg))
		return res
	}
	// A container *with* pages can still be several books: 484 archives in the
	// collection hold nothing but per-chapter directories (D-73). The question
	// is asked of the listing already in hand, so it costs one pass over a slice
	// and never a second read of the file.
	if expanded, ok := s.expandChapters(ctx, rt, t, res, listing); ok {
		return expanded
	}
	return res
}

// expandContainer turns a book that is really a container of volumes into one
// result per volume (prd §7.2 as widened; decision D-07 is superseded for ZIP).
//
// It reports false for anything that is not one, which is every book with pages
// and every archive whose entries are not archives — so the ordinary path is a
// single extra central-directory read for a book that was about to be recorded
// as `비어 있음` anyway.
//
// The container itself stops being a book. It is not recorded as an empty
// volume beside its own contents, because a 권 list showing "39 volumes and one
// broken one, which is the thing holding the 39" is a worse answer than the one
// the reader wants.
func (s *Scanner) expandContainer(ctx context.Context, rt *rootRun, t *seriesTask, res bookResult) (bookResult, bool) {
	u := res.unit
	// Only a top-level archive expands. A volume already inside a container is
	// not opened looking for more containers: nesting deeper than one level does
	// not occur in the collection, and refusing it here is what bounds the work.
	//
	// Only a ZIP container expands, too. Every one of the 46 containers of
	// volumes in the collection is a ZIP, and internal/archive/nested presents
	// an inner archive by reading a ZIP local header and inflating it — so a RAR
	// container would need that adapter generalised, not just a kind added here.
	// Declining is the honest answer until such a file exists.
	if u.kind != source.KindZIP || u.innerPath != "" || ctx.Err() != nil {
		return res, false
	}
	src, err := s.books.Open(ctx, source.Book{
		ID: res.id, Kind: source.KindZIP, RootName: rt.cfg.Name, RelPath: u.relPath,
		FileSize: u.size, FileMtime: u.mtime,
	})
	if err != nil {
		return res, false
	}
	lister, ok := src.(source.VolumeLister)
	if !ok {
		_ = src.Close()
		return res, false
	}
	inner, err := lister.Volumes(ctx)
	_ = src.Close()
	if err != nil || len(inner) == 0 {
		return res, false
	}

	// The walker counted this container as one book. It is about to become
	// len(inner) of them, so the estimate is corrected before the first one
	// finishes — FR-IDX-004's `total` is a running figure, not a promise made at
	// the start.
	s.progress.discovered(rt.cfg.Name, 0, len(inner)-1)

	out := res
	out.expanded = make([]bookResult, 0, len(inner))
	for _, name := range inner {
		vu := u
		vu.innerPath = name
		// The volume's own extension chooses its kind, and therefore the reader
		// that will index it. A `.rar` recorded as nestedzip would be handed to
		// the ZIP reader and fail as corrupt, which is the wrong story about a
		// perfectly good file. Volumes() only returns extensions that name a
		// reader, so this is never empty.
		vu.kind = source.NestedKind(name)
		// The volume's identity within the series is the container plus the
		// entry, so sort_key orders volumes inside a container the way natural
		// sort orders any other pair of names.
		vu.rel = path.Join(u.rel, name)
		vu.name = baseName(name)
		out.expanded = append(out.expanded, s.indexUnit(ctx, rt, t, vu))
	}
	out.logs = append(out.logs, rt.entry(index.LevelInfo, u.relPath,
		fmt.Sprintf("container of %d volumes: indexed each one as a book", len(inner))))
	return out, true
}

// expandChapters turns a container whose pages live in per-chapter
// sub-directories into one result per directory (decision D-73).
//
// It is the same move expandContainer makes and for the same reason, one level
// down: what the reader is looking at is not a 842-page 권, it is eight of them.
// `여자친구 만들고파! 01~08권.zip` is the file that prompted it, and 484 archives
// of the collection — 279,541 pages — are the shape.
//
// The partition itself is [source.Chapters], computed from the listing this
// book has already produced. What is *not* free is the indexing: each chapter is
// sent back through indexUnit, which reads the container's directory again to
// list that chapter's pages. That is deliberate. indexUnit is where FR-IDX-003
// lives, so on a rescan an unchanged chapter is recognised by its own
// (size, mtime) and its page rows are never touched — and a chapter re-derived
// here instead would rewrite every page row of a 6,097-book library on every
// scan, which is the cost that actually matters. A second central-directory read
// is two ReadAt calls against a handle the pool already has open.
//
// Like expandContainer, the container stops being a book: it is not recorded as
// a 842-page volume beside the eight it turned out to hold.
func (s *Scanner) expandChapters(ctx context.Context, rt *rootRun, t *seriesTask,
	res bookResult, l *source.Listing,
) (bookResult, bool) {
	u := res.unit
	// Only a container that is its own file splits. A chapter inside a volume
	// inside a container would need two inner paths and books.inner_path is one
	// column (arch §3.5) — and no such file exists in the collection. A `dir`
	// book is already split by collectBooks, one book per image sub-folder
	// (prd §2.2 row 2), and a PDF has no directories at all.
	if u.innerPath != "" || ctx.Err() != nil {
		return res, false
	}
	switch u.kind {
	case source.KindZIP, source.KindRAR, source.KindHV3:
	default:
		return res, false
	}
	chapters := source.Chapters(l.Pages)
	if len(chapters) == 0 {
		return res, false
	}

	// One book in the walker's count is about to become len(chapters) of them.
	s.progress.discovered(rt.cfg.Name, 0, len(chapters)-1)

	out := res
	// The container's own page list is dropped rather than carried: every page
	// in it is about to be listed again under the chapter that owns it, and
	// these are the biggest books in the library to be holding twice.
	out.pages, out.pageCount, out.totalBytes = nil, 0, 0
	out.expanded = make([]bookResult, 0, len(chapters))
	for _, ch := range chapters {
		cu := u
		cu.kind = source.KindNestedDir
		cu.innerPath = ch.Path
		if ch.Path == source.ChapterRoot {
			// The pages that were loose at the top of the archive. They keep the
			// container's own sort key so they sort before every chapter, and
			// they say what they are — the same sentence collectBooks puts on the
			// same shape one level up (arch §4.2 step 5).
			cu.rel = u.rel
			cu.name = baseName(u.rel) + looseBookSuffix
		} else {
			cu.rel = path.Join(u.rel, ch.Path)
			cu.name = baseName(ch.Path)
		}
		out.expanded = append(out.expanded, s.indexUnit(ctx, rt, t, cu))
	}
	out.logs = append(out.logs, rt.entry(index.LevelInfo, u.relPath,
		fmt.Sprintf("container of %d chapter directories: indexed each one as a book",
			len(chapters))))
	return out, true
}

// baseName is path.Base for a slash path, minus the "." it returns for "".
func baseName(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

// flattenExpanded replaces every container result with the volumes it turned
// out to hold, so that everything downstream — ordering, series status, the
// page rows, the counters — sees an ordinary list of books.
//
// It runs after the last of a series' books is indexed and before the series is
// delivered, on the goroutine that finished last, which is the only moment at
// which the task is owned by nobody else.
func flattenExpanded(t *seriesTask) {
	expanded := false
	for i := range t.results {
		if len(t.results[i].expanded) > 0 {
			expanded = true
			break
		}
	}
	if !expanded {
		return
	}

	books := make([]bookUnit, 0, len(t.books))
	results := make([]bookResult, 0, len(t.results))
	for i := range t.results {
		vols := t.results[i].expanded
		if len(vols) == 0 {
			books = append(books, t.results[i].unit)
			results = append(results, t.results[i])
			continue
		}
		// The container's own scan_log rows are kept by moving them onto its
		// first volume; the container itself is not recorded as a book.
		vols[0].logs = append(t.results[i].logs, vols[0].logs...)
		for j := range vols {
			books = append(books, vols[j].unit)
			results = append(results, vols[j])
		}
	}
	t.books, t.results = books, results
}

// bookFailure records one isolated failure against a book.
func (s *Scanner) bookFailure(rt *rootRun, res bookResult, err error) bookResult {
	res.status = string(source.StatusOf(err))
	res.errMsg = bookErrorMessage(err, res.id)
	res.logs = append(res.logs, rt.entry(index.LevelWarn, res.unit.relPath, res.errMsg))
	s.log.Warn("book could not be indexed",
		append(logAttrs(rt.runID, rt.cfg.Name, res.unit.relPath),
			"book_id", res.id, "status", res.status, "err", err)...)
	return res
}

// bookErrorMessage renders an error for books.error, which the UI shows on the
// volume tile. internal/source prefixes its errors with the opaque book id;
// stripping it is the difference between `zip: end of central directory not
// found` and `listing book yvtfrny77ehkt2we: zip: end of central directory not
// found` on a badge the user has to read.
func bookErrorMessage(err error, bookID string) string {
	msg := err.Error()
	for _, verb := range [...]string{"listing", "opening", "rendering", "checking"} {
		msg = strings.TrimPrefix(msg, verb+" book "+bookID+": ")
	}
	return msg
}

// convertPages maps a source listing onto index rows and applies the one
// exclusion internal/source cannot: `scan.exclude_globs`, which is defined
// against the root-relative slash path and so needs the book's own path.
//
// The join is `<book rel>/<entry path>`, which for an archive is a synthetic
// path — `series/vol1.zip/vol1/001.jpg`. That is the only spelling that lets one
// pattern language cover a loose file and an archive member alike, and the whole
// loop is skipped when the list is empty, which is the default.
func (s *Scanner) convertPages(rt *rootRun, u bookUnit, l *source.Listing) []index.Page {
	pages := make([]index.Page, 0, len(l.Pages))
	for _, p := range l.Pages {
		if !rt.excludeGlobs.empty() && rt.excludeGlobs.matchPath(pageRelPath(u, p)) {
			rt.note(pageRelPath(u, p), reasonExcludeGlob)
			continue
		}
		pages = append(pages, index.Page{
			PageNo:      len(pages) + 1,
			Name:        p.Name,
			EntryPath:   p.EntryPath,
			Ext:         p.Ext,
			Size:        p.Size,
			CompSize:    p.CompSize,
			Method:      int(p.Method),
			LocalHdrOff: p.LocalHdrOff,
			CRC32:       p.CRC32,
			Mtime:       p.Mtime,
		})
	}
	return pages
}

func pageRelPath(u bookUnit, p source.Page) string {
	if p.EntryPath == "" {
		return u.relPath
	}
	return path.Join(u.relPath, p.EntryPath)
}

// deliver hands one finished series to the writer.
//
// It is the only place a task enters the results channel, and it takes the page
// budget first: the channel bounds how many series may be in flight, the budget
// bounds how much index they may be carrying (budget.go). Both waits end
// immediately when the run is cancelled, and a task that never reaches the
// writer gives its budget straight back — the writer only refunds what it
// actually received.
func (s *Scanner) deliver(ctx context.Context, resCh chan<- *seriesTask, t *seriesTask) error {
	t.heldPages = t.pageRows()
	if !s.pages.acquire(ctx, t.heldPages) {
		return ctx.Err()
	}
	select {
	case resCh <- t:
		return nil
	case <-ctx.Done():
		s.pages.release(t.heldPages)
		return ctx.Err()
	}
}

// writeResults is the single writer goroutine of arch §4.1. It owns the index
// write connection for the whole of a root's pass, so no two goroutines in this
// process ever contend for the SQLite write lock.
//
// Every index call it makes uses a context stripped of cancellation, and that is
// load-bearing rather than defensive. `POST /api/scan/cancel` must "commit what
// it has" (arch §4.1) — but database/sql tears a transaction down the moment the
// context that began it is cancelled, so a batch begun on the scan's own context
// would be *rolled back* by the cancellation, silently discarding up to 200
// already-indexed books. The producers still stop instantly; only the commit of
// work already done is protected.
func (s *Scanner) writeResults(ctx context.Context, rt *rootRun, resCh <-chan *seriesTask,
	out *RootResult, abort context.CancelFunc,
) error {
	ctx = context.WithoutCancel(ctx)
	w := s.index.Writer(s.writerOptions)
	stamps := &genStamps{}
	var firstErr error

	for t := range resCh {
		if firstErr != nil {
			// Keep draining so the producers never block — and give the page
			// budget back, or a failed root would starve the rest of the run.
			s.pages.release(t.heldPages)
			continue
		}
		err := s.writeSeries(ctx, w, rt, t, out, stamps)
		// The rows are in the transaction (or abandoned) and the task is
		// garbage from here, so the budget is returned before the error is
		// even looked at.
		s.pages.release(t.heldPages)
		if err != nil {
			firstErr = err
			abort()
			continue
		}
		if err := stamps.flushIfFull(ctx, w, rt.gen); err != nil {
			firstErr = err
			abort()
		}
	}
	if firstErr == nil {
		if err := stamps.flush(ctx, w, rt.gen); err != nil {
			firstErr = err
		}
	}
	if firstErr == nil {
		if err := s.drainLogs(ctx, w, rt); err != nil {
			firstErr = err
		}
	}
	// Close flushes: a cancelled scan commits what it has (arch §4.1).
	if err := w.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

func (s *Scanner) drainLogs(ctx context.Context, w *index.Writer, rt *rootRun) error {
	entries := rt.logs.take()
	if len(entries) == 0 {
		return nil
	}
	if err := w.AppendLog(ctx, entries...); err != nil {
		return fmt.Errorf("writing scan log rows: %w", err)
	}
	return nil
}

// diskBytes is one book's on-disk footprint — the number that rolls up into
// `series.total_bytes` and therefore into every 용량 the product shows
// (prd FR-LIB-003, FR-LIB-009, UI-002, and the `sort=size` ordering of
// FR-LIB-004).
//
// It is deliberately NOT `bookResult.totalBytes`. That field is `books.total_bytes`,
// which arch §4.4 defines as the sum of *uncompressed page* bytes — a quantity
// that is legitimately **zero** for a PDF (its pages are rendered on demand, not
// stored) and for any book with no readable pages. Rolling that up made the nine
// 미생 PDF volumes, the whole 미생 series (520 MB on disk) and the 1.44 GB
// 엔젤하트 container all read `0 KB` in the grid card, the list column, the series
// header and the settings-dialog total. Ruling E-11 settled that a silently
// wrong 용량 is the worst of the available answers.
//
// `bookUnit.size` is the container size, and arch §4.4 fixes it at 0 for
// `kind='dir'` — a directory has no container, so there its pages' bytes ARE its
// bytes on disk. The two cases therefore compose into one rule with no
// per-kind switch.
//
// impl-plan §6.3 step 6.2 pins the result: sorting the library by 용량 must put
// the 1.44 GB 엔젤하트 series first, and that series holds no page rows at all.
func diskBytes(r *bookResult) int64 {
	if r.unit.size > 0 {
		return r.unit.size
	}
	return r.totalBytes
}

// seriesDiskBytes is the same quantity for a whole series: the bytes it occupies
// on disk, counting each file once.
//
// Once is the whole point. Every 권 that lives inside a container records that
// *container's* size (arch §3.5 — the volume has no file of its own), so a plain
// sum multiplies it by the number of volumes: the 1.55 GB `엔젤하트 전32권 완결.zip`
// reported **51 GB** in the grid card, the list column, the series header and the
// settings total, and D-73 would have taken `암살교실 1~180화.zip` — 588 MB — to
// **107 GB**. Ruling E-11's finding is what applies: a silently wrong 용량 is the
// worst of the available answers, and it is worst of all in `sort=size`, where
// the ordering it produces is a ranking by volume count wearing a byte unit.
//
// The de-duplication key is the book's path, and only for a book that names a
// container. A `kind='dir'` book has `size == 0` and contributes its pages'
// bytes, which are its own; ruling E-5's duplicates (`01권/` beside `01권.zip`)
// are different paths and are still counted twice, because they are two files.
func seriesDiskBytes(results []bookResult) int64 {
	var total int64
	var counted map[string]struct{}
	for i := range results {
		r := &results[i]
		if r.unit.size > 0 {
			if counted == nil {
				counted = make(map[string]struct{}, len(results))
			}
			if _, seen := counted[r.unit.relPath]; seen {
				continue
			}
			counted[r.unit.relPath] = struct{}{}
		}
		total += diskBytes(r)
	}
	return total
}

// writeSeries persists one finished series: the series row, then its books and
// their pages, then the generation stamp for everything the incremental path
// skipped, then the scan-log rows, and finally an enqueue of the cover that
// fires when the batch holding those rows commits.
//
// The series row goes first because books.series_id is a foreign key and
// `PRAGMA foreign_keys` is ON.
func (s *Scanner) writeSeries(ctx context.Context, w *index.Writer, rt *rootRun,
	t *seriesTask, out *RootResult, stamps *genStamps,
) error {
	for i := range t.results {
		if t.results[i].aborted {
			// The run was cancelled mid-series. Leaving the old rows at the old
			// generation is correct: no sweep runs after a cancellation, so
			// they survive untouched rather than being replaced by a half-read
			// picture of the series.
			return nil
		}
	}

	u := t.unit
	status, message := seriesStatus(t.results)
	if u.err != nil {
		status, message = StatusError, u.err.Error()
	}

	var pageCount int64
	mtime := u.mtime
	for i := range t.results {
		pageCount += t.results[i].pageCount
		if m := t.results[i].unit.mtime; m > mtime {
			mtime = m
		}
	}
	totalBytes := seriesDiskBytes(t.results)

	cover := chooseCover(u, t.results)
	now := s.now().Unix()

	row := index.Series{
		ID: t.id, RootName: u.rootName, RelPath: u.relPath, DisplayName: u.name,
		SortKey:     natsort.Key(u.name),
		SearchKey:   hangul.SearchKey(u.name),
		ChoseongKey: hangul.Choseong(u.name),
		Kind:        u.kind,
		BookCount:   int64(len(t.results)),
		PageCount:   pageCount,
		TotalBytes:  totalBytes,
		Mtime:       mtime,
		// added_at is never moved forward — index.Writer keeps the minimum, so
		// "최근 추가" means the first time we ever saw the series.
		AddedAt:      now,
		CoverKind:    cover.Kind,
		CoverBookID:  cover.BookID,
		CoverPageNo:  cover.PageNo,
		CoverRelPath: cover.RelPath,
		Status:       status,
		Error:        message,
		ScanGen:      rt.gen,
	}
	// A series whose row would come back byte-identical is stamped forward
	// instead of rewritten. On a no-change rescan that turns the widest
	// statement in the scanner — a twenty-column upsert per series — into a
	// share of one 400-id UPDATE, which is most of what makes FR-IDX-003 a
	// different order of magnitude rather than a modest saving.
	if sameSeriesRow(t.priorSeries, row) {
		stamps.series = append(stamps.series, t.id)
	} else if err := w.UpsertSeries(ctx, row); err != nil {
		return err
	}
	out.Series++
	// Amendment A-8: every series this run listed is offered to user.db, whether
	// its row was rewritten or merely stamped forward. The first offer wins for
	// ever; the rest are no-ops (arch §3.6 rule 1).
	rt.seen.add(t.id, u.rootName, u.relPath, row.Mtime)

	var logs []index.LogEntry
	for ord := range t.results {
		r := &t.results[ord]
		logs = append(logs, r.logs...)
		out.Books++
		out.Pages += r.pageCount
		if r.skipped {
			out.Skipped++
		}
		if isFailure(r.status) {
			out.Errors++
		}

		// The pure incremental path: the row is already right, so it is stamped
		// forward instead of rewritten and its page rows are not touched at all.
		if r.skipped && r.priorSeriesID == t.id && r.priorOrd == ord {
			stamps.books = append(stamps.books, r.id)
			continue
		}

		if err := w.UpsertBook(ctx, index.Book{
			ID: r.id, SeriesID: t.id, RootName: u.rootName, RelPath: r.unit.relPath,
			InnerPath:      r.unit.innerPath,
			DisplayName:    r.unit.name,
			SortKey:        natsort.Key(r.unit.rel),
			Ord:            ord,
			Kind:           string(r.unit.kind),
			PageCount:      r.pageCount,
			TotalBytes:     r.totalBytes,
			FileSize:       r.unit.size,
			FileMtime:      r.unit.mtime,
			DirFingerprint: r.unit.fingerprint,
			ContentVersion: r.contentVersion,
			DimsState:      r.dimsState,
			Status:         r.status,
			Error:          r.errMsg,
			ScanGen:        rt.gen,
		}); err != nil {
			return err
		}
		// An unchanged book that merely moved keeps its pages: re-reading them
		// would undo the whole point of FR-IDX-003.
		if !r.skipped {
			if err := w.ReplacePages(ctx, r.id, r.pages); err != nil {
				return err
			}
		}
	}

	logs = append(logs, rt.logs.take()...)
	if len(logs) > 0 {
		if err := w.AppendLog(ctx, logs...); err != nil {
			return fmt.Errorf("writing scan log rows: %w", err)
		}
	}

	// FR-THM-003. The enqueue is deferred to the commit that makes the rows
	// above visible, and that is load-bearing rather than tidy: a `page` cover
	// is generated by reading `books` and `pages` back through the *read* pool,
	// and inside an open batch those rows are not there. Enqueueing here — as
	// this line used to — failed every page cover with thumbs.ErrNotFound, once
	// per configured width, with no retry: on the curated E2E subset 28 of 36
	// covers lost, on the real collection every series whose cover is not a
	// loose file. Writer.AfterCommit runs the closure the moment the batch is
	// durable, so covers still stream out *during* the scan rather than after
	// it, and a rolled-back batch drops its enqueues along with its rows.
	w.AfterCommit(func() { s.enqueueCover(ctx, u, t.id, cover) })
	return nil
}

// waitForCovers is arch §4.12's `covers` phase. It only runs when the queue can
// actually report progress; a queue that cannot simply gets its covers enqueued
// and the scan ends, rather than the status pretending to know something.
func (s *Scanner) waitForCovers(ctx context.Context) {
	if s.covers == nil {
		return
	}
	reporter, ok := s.covers.(CoverProgressReporter)
	if !ok {
		return
	}
	s.progress.setPhase(PhaseCovers)
	deadline := time.Now().Add(coverWaitLimit)
	ticker := time.NewTicker(coverPollInterval)
	defer ticker.Stop()
	for {
		done, total := reporter.CoverProgress()
		s.progress.coversProgress(done, total)
		if done >= total || !time.Now().Before(deadline) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
