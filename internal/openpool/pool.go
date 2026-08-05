// Package openpool is the LRU pool of open container handles (FR-SRV-004).
//
// A viewer session pulls 3–5 prefetched pages plus the current one out of the
// same archive within a second or two. Re-opening the file for each is a
// syscall and a path walk per page for no reason, so handles are kept.
//
// Two invariants make it safe:
//
//   - CON-004. A borrowed handle is exposed as a [Ref], which offers ReadAt and
//     nothing else. There is no Seek, on the Ref or on the internal [File]
//     interface, so concurrent readers of one archive cannot share a cursor —
//     each one gets an independent pread. That is what prd CON-004 asks for and
//     what makes the pool lock-free on the read path.
//   - A file descriptor is never closed underneath a live stream. Eviction only
//     ever closes handles with no borrowers; [Pool.Invalidate] and [Pool.Close]
//     unpublish a handle immediately but defer the close to the last release.
//
// The pool opens files. It never creates, renames, truncates or removes one —
// FR-CFG-005 / NFR-DAT-002, enforced by `make lint`'s check-readonly grep.
package openpool

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"sync"
)

// ErrClosed is returned by Acquire after the pool has been closed.
var ErrClosed = errors.New("openpool: pool is closed")

// DefaultMax matches server.max_open_archives (arch §3.2). It is far below a
// typical RLIMIT_NOFILE of 1024, deliberately.
const DefaultMax = 64

// File is the subset of *os.File the pool needs.
//
// The absence of io.Seeker is the point, not an oversight: it makes a shared
// cursor unrepresentable, which is how CON-004 is satisfied structurally
// rather than by convention.
type File interface {
	io.ReaderAt
	io.Closer
	Stat() (fs.FileInfo, error)
}

// OpenFunc opens a container by absolute path. The default is os.Open; the
// composition root replaces it with a root-scoped opener so that container
// access also passes through os.Root (path-traversal layer 3, arch §8.1).
type OpenFunc func(path string) (File, error)

func osOpen(path string) (File, error) { return os.Open(path) }

// Options configures a Pool.
type Options struct {
	// Max is the number of handles to keep open. Zero means DefaultMax.
	Max int
	// Open overrides how a path becomes a File. Zero means os.Open.
	Open OpenFunc
	// Logger receives eviction and capacity diagnostics. Zero means
	// slog.Default().
	Logger *slog.Logger
}

// Stats is the counter set exposed at GET /api/health?verbose=1 (arch §5.2).
type Stats struct {
	Hits      int64 // Acquire found a published handle
	Misses    int64 // Acquire had to open the file
	Evictions int64 // handles dropped to stay within Max
	Stale     int64 // handles whose (size, mtime) disagreed with the index
	Size      int   // handles currently published
	Open      int   // file descriptors currently held, including unpublished ones
}

// Pool is a bounded LRU of open container handles. The zero value is not
// usable; call New.
type Pool struct {
	mu     sync.Mutex
	max    int
	open   OpenFunc
	log    *slog.Logger
	lru    *list.List               // *handle, most recently used at the front
	items  map[string]*list.Element // published handles, keyed by absolute path
	live   int                      // descriptors held, published or not
	closed bool
	stats  Stats
}

// New returns a pool honouring opts.
func New(opts Options) *Pool {
	p := &Pool{
		max:   opts.Max,
		open:  opts.Open,
		log:   opts.Logger,
		lru:   list.New(),
		items: make(map[string]*list.Element),
	}
	if p.max <= 0 {
		p.max = DefaultMax
	}
	if p.open == nil {
		p.open = osOpen
	}
	if p.log == nil {
		p.log = slog.Default()
	}
	return p
}

// handle is one open file plus its borrow count. Every field is guarded by
// Pool.mu except f, which is immutable once set.
type handle struct {
	path  string
	f     File
	size  int64
	mtime int64

	refs      int
	published bool // still reachable through Pool.items
}

// Ref is a borrowed handle. Callers must call Release exactly once, and must
// not use the Ref afterwards.
//
// Ref implements io.ReaderAt and nothing more: pass it straight to
// zipidx.OpenEntry, which wraps it in an io.SectionReader.
type Ref struct {
	pool  *Pool
	h     *handle
	stale bool

	releaseOnce sync.Once
}

// ReadAt implements io.ReaderAt. It is a pread: no shared offset, safe from
// any number of goroutines at once (CON-004).
func (r *Ref) ReadAt(p []byte, off int64) (int, error) { return r.h.f.ReadAt(p, off) }

// Size is the container's size in bytes as of the moment it was opened.
func (r *Ref) Size() int64 { return r.h.size }

// ModTime is the container's modification time in Unix seconds.
func (r *Ref) ModTime() int64 { return r.h.mtime }

// Stale reports that the file on disk no longer matches the (mtime, size) the
// caller asked for. The read still proceeds — a stale offset simply produces
// garbage or an error — but the HTTP layer turns this into `409 stale_version`
// and the scanner into a rescan of that book (arch §5.2, §7.6).
func (r *Ref) Stale() bool { return r.stale }

// Release returns the handle to the pool. It is idempotent, so
// `defer ref.Release()` next to an early `ref.Release()` is harmless.
func (r *Ref) Release() {
	r.releaseOnce.Do(func() { r.pool.release(r.h) })
}

var _ io.ReaderAt = (*Ref)(nil)

// Acquire borrows the handle for path, opening it if the pool does not hold it.
//
// wantMtime and wantSize are what the index recorded for the container; pass 0
// for either to skip the check. A mismatch is reported through Ref.Stale, not
// as an error: refusing to serve a page because the file grew would be worse
// than serving it and telling the client to refresh its metadata.
//
// Note what a *hit* does and does not do. It answers from the descriptor the
// pool already holds and re-stats nothing, so the (mtime, size) a Ref reports
// are the ones its file had when it was opened — after `mv new.zip old.zip` the
// path is a new inode and this handle is the old, unlinked one, still perfectly
// readable. That is the right answer for a reader committed to offsets the index
// recorded before the change, and the wrong one for anybody deriving a fresh
// verdict: source.zipSource.List treats a stale ref as a reason to Invalidate
// and re-open rather than as a fact to report onwards (arch §4.6, §5.2).
func (p *Pool) Acquire(ctx context.Context, path string, wantMtime, wantSize int64) (*Ref, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, ErrClosed
	}
	if el, ok := p.items[path]; ok {
		h := el.Value.(*handle)
		p.lru.MoveToFront(el)
		h.refs++
		p.stats.Hits++
		p.mu.Unlock()
		return p.ref(h, wantMtime, wantSize), nil
	}
	p.stats.Misses++
	p.mu.Unlock()

	// Opening happens outside the lock: a cold NAS open can take milliseconds
	// and must not block every other reader. The cost is that two goroutines
	// racing on the same cold path may both open it; the loser's handle is
	// closed below. That is cheaper and far simpler than a per-path
	// single-flight, and it happens at most once per archive per burst.
	f, err := p.open(path)
	if err != nil {
		return nil, fmt.Errorf("opening archive: %w", err)
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("stat of open archive: %w", err)
	}

	h := &handle{path: path, f: f, size: fi.Size(), mtime: fi.ModTime().Unix()}

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		_ = f.Close()
		return nil, ErrClosed
	}
	if el, ok := p.items[path]; ok {
		// Lost the race. Use the published handle and drop ours.
		winner := el.Value.(*handle)
		p.lru.MoveToFront(el)
		winner.refs++
		p.mu.Unlock()
		_ = f.Close()
		return p.ref(winner, wantMtime, wantSize), nil
	}
	h.refs = 1
	h.published = true
	p.live++
	p.items[path] = p.lru.PushFront(h)
	p.evictLocked()
	p.mu.Unlock()

	return p.ref(h, wantMtime, wantSize), nil
}

func (p *Pool) ref(h *handle, wantMtime, wantSize int64) *Ref {
	stale := (wantSize != 0 && wantSize != h.size) || (wantMtime != 0 && wantMtime != h.mtime)
	if stale {
		p.mu.Lock()
		p.stats.Stale++
		p.mu.Unlock()
		p.log.Warn("archive on disk differs from the index",
			"path", h.path, "size", h.size, "want_size", wantSize,
			"mtime", h.mtime, "want_mtime", wantMtime)
	}
	return &Ref{pool: p, h: h, stale: stale}
}

// release drops one borrow and closes the descriptor if the handle has been
// unpublished and this was the last one.
func (p *Pool) release(h *handle) {
	p.mu.Lock()
	h.refs--
	closeNow := h.refs <= 0 && !h.published
	if closeNow {
		p.live--
	}
	p.mu.Unlock()
	if closeNow {
		_ = h.f.Close()
	}
}

// evictLocked trims the pool back to Max. Caller holds p.mu.
//
// Only handles with no borrowers are evicted. If every handle in the pool is
// in use the pool is allowed to sit over capacity for a moment rather than
// close a descriptor out from under a page stream — a temporarily larger fd
// set is recoverable, a truncated image is not. Max is 64 against a typical
// limit of 1024, so the headroom is real.
func (p *Pool) evictLocked() {
	for p.lru.Len() > p.max {
		victim := p.oldestIdleLocked()
		if victim == nil {
			p.log.Debug("archive pool is over capacity with every handle in use",
				"size", p.lru.Len(), "max", p.max)
			return
		}
		p.unpublishLocked(victim)
		p.stats.Evictions++
		h := victim.Value.(*handle)
		p.live--
		// Closing under the lock is fine: the descriptor has no borrowers, and
		// close(2) on a regular file does not block.
		_ = h.f.Close()
	}
}

func (p *Pool) oldestIdleLocked() *list.Element {
	for el := p.lru.Back(); el != nil; el = el.Prev() {
		if el.Value.(*handle).refs == 0 {
			return el
		}
	}
	return nil
}

// unpublishLocked removes an element from the LRU and the map without touching
// the descriptor. Caller holds p.mu.
func (p *Pool) unpublishLocked(el *list.Element) {
	h := el.Value.(*handle)
	h.published = false
	p.lru.Remove(el)
	delete(p.items, h.path)
}

// Invalidate drops the handle for path so the next Acquire re-opens the file.
//
// The scanner calls it before re-reading a container whose (mtime, size) do not
// match the handle on offer: a rebuilt index carries new offsets, and reading
// those out of a handle opened before the archive changed would produce a
// verdict about a file that is no longer there. In-flight readers keep the old
// descriptor until they finish — they are already committed to the old offsets,
// which are the ones that match what they are reading.
func (p *Pool) Invalidate(path string) {
	p.mu.Lock()
	el, ok := p.items[path]
	if !ok {
		p.mu.Unlock()
		return
	}
	h := el.Value.(*handle)
	p.unpublishLocked(el)
	closeNow := h.refs <= 0
	if closeNow {
		p.live--
	}
	p.mu.Unlock()
	if closeNow {
		_ = h.f.Close()
	}
}

// Stats returns a snapshot of the pool counters.
func (p *Pool) Stats() Stats {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.stats
	s.Size = p.lru.Len()
	s.Open = p.live
	return s
}

// Close unpublishes every handle and closes the idle ones. Handles that are
// still borrowed are closed by their last Release, so a shutdown never
// truncates a response that is already being written.
func (p *Pool) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	var idle []*handle
	for el := p.lru.Front(); el != nil; {
		next := el.Next()
		h := el.Value.(*handle)
		p.unpublishLocked(el)
		if h.refs <= 0 {
			p.live--
			idle = append(idle, h)
		}
		el = next
	}
	p.mu.Unlock()

	var err error
	for _, h := range idle {
		if cerr := h.f.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("closing %s: %w", h.path, cerr)
		}
	}
	return err
}
