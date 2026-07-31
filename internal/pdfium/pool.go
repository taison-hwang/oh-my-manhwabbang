//go:build !nopdf

// Package pdfium rasterises PDF pages (FR-SRV-006, AC-004).
//
// It wraps klippa-app/go-pdfium in webassembly (wazero) mode, which keeps the
// build cgo-free — CON-001 — at the cost of a one-off wasm compilation. That
// compilation is the reason for every design choice here:
//
//   - Lazy. The pool is created on the first PDF request, not at startup. A
//     library of ZIP and folder series (961 of the 963 real series) never pays
//     for it at all, which is how NFR-PRF-005's 200 MB idle budget survives.
//   - Cached. wazero's on-disk compilation cache turns a 3.885 s / 299 MiB
//     cold init into 135 ms / 43 MiB warm (decision D-20).
//   - Torn down. After pdf.idle_timeout with no work the pool closes and the
//     43–300 MiB goes back to the OS. The next request pays the warm init.
//
// Build with -tags nopdf to compile the whole thing out; see pool_stub.go.
package pdfium

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	gopdfium "github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/webassembly"
	"github.com/tetratelabs/wazero"
)

// Supported reports whether this build can rasterise PDFs. It is false in a
// -tags nopdf build, and is what the scanner and the HTTP layer branch on
// rather than probing for an error.
func Supported() bool { return true }

// Errors callers match with errors.Is.
var (
	// ErrUnsupported means PDF rendering is not available: a nopdf build, or
	// pdf.enabled=false. It maps to books.status='unsupported' and to the
	// HTTP 501 of arch §7.6.
	ErrUnsupported = errors.New("pdf support is not enabled in this build")
	// ErrClosed is returned once the renderer has been shut down.
	ErrClosed = errors.New("pdf renderer is closed")
	// ErrNoSuchPage is a page number outside [1, PageCount].
	ErrNoSuchPage = errors.New("no such pdf page")
)

// Defaults mirroring the pdf: block of arch §3.2.
const (
	DefaultWorkers     = 1
	DefaultIdleTimeout = 5 * time.Minute
	// acquireTimeout bounds the wait for a free pdfium worker. Rendering one
	// page took 296 ms on a real 284-page Korean file, so a request that has
	// waited 60 s is queued behind something pathological and should fail
	// rather than hold an HTTP handler forever.
	acquireTimeout = 60 * time.Second
)

// Options configures a Renderer.
type Options struct {
	// Workers is the maximum number of concurrent pdfium instances
	// (pdf.workers). Zero means DefaultWorkers.
	Workers int
	// CacheDir is the directory for wazero's compilation cache, normally
	// <cache_dir>/wazero. Empty disables the on-disk cache, which costs the
	// full 3.9 s init on every cold start.
	CacheDir string
	// IdleTimeout is how long the pool may sit unused before it is torn down.
	// Zero means DefaultIdleTimeout; negative disables teardown.
	IdleTimeout time.Duration
	// Logger; zero means slog.Default().
	Logger *slog.Logger
}

// Renderer owns the lazily-created pdfium pool and the goroutine that reaps it.
// It is safe for concurrent use. Callers must Close it.
type Renderer struct {
	workers     int
	cacheDir    string
	idleTimeout time.Duration
	log         *slog.Logger

	mu       sync.Mutex
	pool     gopdfium.Pool
	cache    wazero.CompilationCache
	openDocs int
	lastUse  time.Time
	closed   bool

	// The reaper is started with the pool and stopped by Close. No goroutine
	// in this package is unowned (impl-plan §5.1).
	reaperStop chan struct{}
	reaperDone chan struct{}
}

// New returns a renderer. Nothing is initialised until the first Open.
func New(opts Options) *Renderer {
	r := &Renderer{
		workers:     opts.Workers,
		cacheDir:    opts.CacheDir,
		idleTimeout: opts.IdleTimeout,
		log:         opts.Logger,
	}
	if r.workers <= 0 {
		r.workers = DefaultWorkers
	}
	if r.idleTimeout == 0 {
		r.idleTimeout = DefaultIdleTimeout
	}
	if r.log == nil {
		r.log = slog.Default()
	}
	return r
}

// instance borrows a pdfium worker, initialising the pool if this is the first
// call since startup or since the last idle teardown.
func (r *Renderer) instance(ctx context.Context) (gopdfium.Pdfium, error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, ErrClosed
	}
	if r.pool == nil {
		if err := r.initLocked(ctx); err != nil {
			r.mu.Unlock()
			return nil, err
		}
	}
	pool := r.pool
	r.openDocs++
	r.lastUse = time.Now()
	r.mu.Unlock()

	inst, err := pool.GetInstance(acquireTimeout)
	if err != nil {
		r.mu.Lock()
		r.openDocs--
		r.mu.Unlock()
		return nil, fmt.Errorf("acquiring a pdfium worker: %w", err)
	}
	return inst, nil
}

// releaseInstance returns a worker and re-arms the idle clock.
func (r *Renderer) releaseInstance(inst gopdfium.Pdfium) {
	if inst != nil {
		if err := inst.Close(); err != nil {
			r.log.Warn("closing a pdfium instance", "err", err)
		}
	}
	r.mu.Lock()
	r.openDocs--
	r.lastUse = time.Now()
	r.mu.Unlock()
}

// initLocked creates the wazero runtime, the compilation cache and the worker
// pool. Caller holds r.mu.
func (r *Renderer) initLocked(ctx context.Context) error {
	start := time.Now()

	runtimeCfg := wazero.NewRuntimeConfig()
	if r.cacheDir != "" {
		// wazero creates the directory itself, versioned by wazero build and
		// GOARCH/GOOS, so a toolchain upgrade cannot serve a stale module.
		cache, err := wazero.NewCompilationCacheWithDir(r.cacheDir)
		if err != nil {
			// A broken cache directory must not make PDFs unreadable — it only
			// makes them slow. Fall through to an uncached runtime.
			r.log.Warn("pdf compilation cache unavailable, falling back to a cold compile",
				"dir", r.cacheDir, "err", err)
		} else {
			r.cache = cache
			runtimeCfg = runtimeCfg.WithCompilationCache(cache)
		}
	}

	// The pool outlives the request that created it, so it gets a background
	// context rather than the caller's. Cancellation is Close's job.
	pool, err := webassembly.Init(webassembly.Config{
		Context:       context.WithoutCancel(ctx),
		MinIdle:       0,
		MaxIdle:       r.workers,
		MaxTotal:      r.workers,
		RuntimeConfig: runtimeCfg,
		ReuseWorkers:  true,
	})
	if err != nil {
		r.closeCacheLocked()
		return fmt.Errorf("initialising the pdfium runtime: %w", err)
	}
	r.pool = pool
	r.lastUse = time.Now()
	r.log.Info("pdfium runtime ready", "workers", r.workers,
		"cache_dir", r.cacheDir, "dur_ms", time.Since(start).Milliseconds())

	if r.idleTimeout > 0 {
		r.reaperStop = make(chan struct{})
		r.reaperDone = make(chan struct{})
		go r.reap(r.reaperStop, r.reaperDone)
	}
	return nil
}

// reap tears the pool down after idleTimeout with no work, releasing the wasm
// module's memory back to the OS (decision D-20).
func (r *Renderer) reap(stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	interval := r.idleTimeout / 2
	if interval < time.Second {
		interval = time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			r.mu.Lock()
			idle := r.pool != nil && r.openDocs == 0 && time.Since(r.lastUse) >= r.idleTimeout
			if !idle {
				r.mu.Unlock()
				continue
			}
			r.log.Info("tearing down the idle pdfium runtime",
				"idle_ms", time.Since(r.lastUse).Milliseconds())
			r.shutdownPoolLocked()
			// The reaper belongs to the pool it just closed; a new pool starts
			// a new reaper. Clearing the handles here stops Close from waiting
			// on a goroutine that is about to return.
			r.reaperStop, r.reaperDone = nil, nil
			r.mu.Unlock()
			return
		}
	}
}

// shutdownPoolLocked closes the pool and the compilation cache. Caller holds
// r.mu and has verified that no document is open.
func (r *Renderer) shutdownPoolLocked() {
	if r.pool != nil {
		if err := r.pool.Close(); err != nil {
			r.log.Warn("closing the pdfium pool", "err", err)
		}
		r.pool = nil
	}
	r.closeCacheLocked()
}

func (r *Renderer) closeCacheLocked() {
	if r.cache != nil {
		if err := r.cache.Close(context.Background()); err != nil {
			r.log.Warn("closing the pdfium compilation cache", "err", err)
		}
		r.cache = nil
	}
}

// Active reports whether the wasm runtime is currently up.
//
// It is what GET /api/health?verbose=1 shows and what makes D-20's lazy
// initialisation and idle teardown observable rather than a claim.
func (r *Renderer) Active() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pool != nil
}

// Close shuts the renderer down, unconditionally: it tears the wasm pool down
// even if documents are still open, because it is the process-shutdown path and
// blocking it on an in-flight render would hang the server. A Doc that outlives
// its Renderer fails its next call — Close it anyway, it is idempotent and
// releasing the pool handle is all it can still do.
//
// This is deliberate rather than incidental. Draining first would need Close to
// wait on openDocs, and a wedged render would then hold shutdown open for the
// whole acquireTimeout.
func (r *Renderer) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	stop, done := r.reaperStop, r.reaperDone
	r.reaperStop, r.reaperDone = nil, nil
	r.shutdownPoolLocked()
	r.mu.Unlock()

	if stop != nil {
		close(stop)
		<-done
	}
	return nil
}

// Open reads a document's structure from rs, which must cover exactly size
// bytes.
//
// pdfium pulls the byte ranges it needs through the reader — a 36.2 MB file
// opens in 2–36 ms — so the file is never slurped into memory and
// NFR-PRF-006 holds for PDFs as it does for ZIPs.
//
// The returned Doc holds a pdfium worker for its lifetime. Close it.
func (r *Renderer) Open(ctx context.Context, rs io.ReadSeeker, size int64) (*Doc, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if size <= 0 {
		return nil, fmt.Errorf("opening pdf: %w (size %d)", ErrNoSuchPage, size)
	}

	inst, err := r.instance(ctx)
	if err != nil {
		return nil, err
	}

	doc, err := inst.OpenDocument(&requests.OpenDocument{
		FileReader:     rs,
		FileReaderSize: size,
	})
	if err != nil {
		r.releaseInstance(inst)
		return nil, fmt.Errorf("opening pdf: %w", err)
	}

	count, err := inst.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: doc.Document})
	if err != nil {
		_, _ = inst.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: doc.Document})
		r.releaseInstance(inst)
		return nil, fmt.Errorf("reading pdf page count: %w", err)
	}

	return &Doc{
		renderer: r,
		inst:     inst,
		ref:      doc.Document,
		pages:    count.PageCount,
	}, nil
}
