// Package thumbs owns every picture SHELF derives rather than serves: JPEG
// thumbnails of book pages, series covers, the cache those live in, and the
// intrinsic page dimensions the viewer needs to lay out a spread (FR-VWR-004).
//
// It implements FR-THM-001..008 and the decode half of FR-IDX-011. Three
// properties are worth stating up front because everything else follows from
// them.
//
// # Invalidation is structural (FR-THM-006, D-19)
//
// The cache key is arch-backend §5.6's hash input, and books.content_version —
// derived by the scanner from the source file's (size, mtime) — is one of its
// fields. A changed source file therefore produces a different key, a different
// path, and a miss. There is no invalidation code that can be forgotten or get
// it wrong; the superseded file simply becomes unreferenced and is removed by
// an explicit purge. The same is true of the JPEG quality and the format string
// (CON-003 / D-18): switching encoder later is a pure cache-invalidation event
// with no migration.
//
// # Nothing reads the cache without being able to regenerate it (FR-THM-007)
//
// Every path into the cache is a stat that may fail, and every failure enqueues
// generation. `rm -rf <cache_dir>` while the server runs costs latency, not
// correctness — including mid-generation, because the publishing writer
// re-creates the fan-out directories it is about to rename into. That is half
// of AC-005 and it is tested literally.
//
// # A request never blocks on generation (FR-THM-004, FR-THM-005)
//
// [Service.Get] is non-blocking: it returns a ready thumbnail or [ErrQueued],
// which the HTTP layer answers with `202 + Retry-After: 1` (impl-plan §4
// point 3). Generation happens on `thumbnails.workers` background goroutines
// draining two queues — an unbounded cover queue that is always drained first
// (FR-THM-003) and a bounded, drop-oldest page queue (FR-THM-004). Concurrent
// misses for one key coalesce into exactly one decode.
//
// Nothing in this package writes anywhere but <cache_dir> (FR-CFG-005,
// NFR-DAT-002); the media volume is opened read-only through internal/source,
// which is itself covered by the `check-readonly` lint guard.
package thumbs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"shelf/internal/config"
	"shelf/internal/index"
	"shelf/internal/source"
)

// Kind names one subtree of the cache directory. These are exactly the values
// `DELETE /api/cache?kind=` accepts (arch §7.9) — a closed set, which is what
// makes it impossible for a purge to name a directory outside the cache.
type Kind string

const (
	// KindThumbs is <cache_dir>/thumbs: the JPEG thumbnails this package makes.
	KindThumbs Kind = "thumbs"
	// KindPDF is <cache_dir>/pdf: full-size rasterised PDF pages (FR-SRV-006).
	// This package owns the layout and the key; the rendering lives in
	// internal/source.
	KindPDF Kind = "pdf"
	// KindWazero is <cache_dir>/wazero: the wasm compilation cache. It is
	// reported and purgeable but never written here — deleting it costs a
	// ~3.9 s pdfium re-compile, which is why arch §7.9 lists it separately.
	KindWazero Kind = "wazero"
	// KindAll is the purge selector meaning "every kind above".
	KindAll Kind = "all"
)

// kinds is the set Usage walks and `kind=all` purges, in report order.
var kinds = []Kind{KindThumbs, KindPDF, KindWazero}

// Sentinel errors. Callers compare with errors.Is, never by string
// (impl-plan §5.1).
var (
	// ErrQueued reports that the thumbnail does not exist yet and has been
	// handed to a background worker. It is a normal, expected outcome — the
	// HTTP layer answers `202` with `Retry-After: 1` (arch §7.5, §7.6).
	ErrQueued = errors.New("thumbs: thumbnail is queued for generation")

	// ErrUndecodable reports that the source picture cannot be turned into a
	// thumbnail — animated WebP, a corrupt entry, a format this build has no
	// decoder for. The original still streams from /pages/{n}; only the
	// thumbnail is lost, which the HTTP layer reports as
	// `422 thumb_unavailable` (arch §5.5). Match [UndecodableError] with
	// errors.As to recover detail.reason.
	ErrUndecodable = errors.New("thumbs: source image cannot be decoded")

	// ErrNotFound reports that the book, the page or the cover file does not
	// exist. The HTTP layer maps it to 404.
	ErrNotFound = errors.New("thumbs: no such book, page or cover file")

	// ErrBadRequest reports a malformed [Request]: an empty id, a page number
	// below 1, a file request with no root.
	ErrBadRequest = errors.New("thumbs: malformed request")

	// ErrUnknownKind reports a purge naming something that is not a [Kind].
	// This is the whole of the "a purge cannot walk outside the cache
	// directory" guarantee: the kind is never a path, it is a closed
	// enumeration.
	ErrUnknownKind = errors.New("thumbs: unknown cache kind")

	// ErrClosed reports use of a Service after Close.
	ErrClosed = errors.New("thumbs: service is closed")
)

// Reasons carried by an [UndecodableError] and surfaced as `detail.reason` on a
// 422 (arch §5.5).
const (
	// ReasonAnimatedWebP is the documented degradation: x/image/webp rejects an
	// animated file outright (verified, arch §5.5) and no still frame is
	// reachable without a second wasm decoder (D-25). Zero animated WebP files
	// exist in the reference collection (data-survey §4).
	ReasonAnimatedWebP = "animated_webp"
	// ReasonAVIFDisabled means `thumbnails.avif_enabled: false` or a
	// `-tags noavif` build.
	ReasonAVIFDisabled = "avif_disabled"
	// ReasonUnknownFormat means the bytes match no format this build decodes.
	ReasonUnknownFormat = "unknown_format"
	// ReasonDecodeFailed means a decoder was found and refused the bytes.
	ReasonDecodeFailed = "decode_failed"
	// ReasonSourceTooLarge means the entry is bigger than
	// `thumbnails.max_source_bytes`.
	ReasonSourceTooLarge = "source_too_large"
	// ReasonSourceTooLargePixels means the entry is small on disk but declares
	// more pixels than [Options.MaxSourcePixels] allows. `max_source_bytes`
	// measures COMPRESSED bytes and is no defence at all against this: a
	// 127-byte JPEG whose SOF0 says 24000×24000 asks image.Decode for ~549 MiB
	// before it fails, and four of them concurrently is NFR-PRF-005's whole
	// budget an order of magnitude over. The declared size is read with
	// image.DecodeConfig, which allocates nothing.
	ReasonSourceTooLargePixels = "source_too_large_pixels"
	// ReasonEmptySource means the entry decoded to nothing at all.
	ReasonEmptySource = "empty_source"
)

// UndecodableError is the typed form of [ErrUndecodable]. Reason is the string
// the API puts in `detail.reason`.
type UndecodableError struct {
	Reason string
	Err    error
}

func (e *UndecodableError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("thumbs: undecodable source (%s): %v", e.Reason, e.Err)
	}
	return fmt.Sprintf("thumbs: undecodable source (%s)", e.Reason)
}

// Unwrap exposes the decoder's own error, so a caller can still inspect it.
func (e *UndecodableError) Unwrap() error { return e.Err }

// Is makes errors.Is(err, ErrUndecodable) true for every reason, so the HTTP
// layer can map the whole class to 422 with one comparison.
func (e *UndecodableError) Is(target error) bool { return target == ErrUndecodable }

// Priority selects which queue a miss is enqueued on.
type Priority int

const (
	// PriorityPage is the lazy path of FR-THM-004: bounded and drop-oldest, so
	// a reader scrolling a 1 071-page thumbnail strip cannot push a cover out
	// of the way or grow the queue without bound.
	PriorityPage Priority = iota
	// PriorityCover is the eager path of FR-THM-003: unbounded and always
	// drained first, so covers appear while the scan is still running.
	PriorityCover
)

func (p Priority) String() string {
	if p == PriorityCover {
		return "cover"
	}
	return "page"
}

// Request is one thumbnail to produce. It carries both halves of the job: the
// identity the cache key is derived from, and where the pixels come from.
//
// There are exactly two shapes, matching the cover ladder of arch §4.10:
//
//   - a page thumbnail — ID is the book id, PageNo is 1-based, and RelPath is
//     empty. This also covers `cover_kind='page'`, where the series cover is
//     page 1 of the first readable book.
//   - a loose-file cover — ID is the series id, RootName and RelPath name an
//     image sitting in the series directory (`cover_kind='file'`), and PageNo
//     is ignored. RelPath is relative to the ROOT, slash-separated, and is
//     validated before it is joined.
type Request struct {
	// ID is the cache identity: the book id for a page thumbnail, the series id
	// for a loose-file cover. It is opaque here; the HTTP layer has already
	// validated its syntax.
	ID string
	// PageNo is 1-based. There is no page 0 anywhere in this product; a
	// loose-file cover is keyed with 0 because it has no page at all.
	PageNo int
	// Width is the requested pixel width. It is snapped UP to the nearest
	// configured `thumbnails.widths` entry and clamped to the largest
	// (impl-plan §0.4); 0 selects the smallest.
	Width int
	// ContentVersion is books.content_version for a page, or the equivalent
	// (size, mtime) digest of the cover file. It is a hash input, which is what
	// makes FR-THM-006 structural rather than a code path.
	ContentVersion string
	// Priority selects the queue on a miss.
	Priority Priority

	// RootName and RelPath, both set, make this a loose-file request.
	RootName string
	RelPath  string
}

// fromFile reports the loose-file shape.
func (r Request) fromFile() bool { return r.RelPath != "" }

// Result describes a thumbnail that exists on disk.
type Result struct {
	// Key is the 16-character cache key. The HTTP strong ETag is `"t1-<Key>"`
	// (arch §5.3) — the key already covers book, page, width, format, quality
	// and content version, so nothing else needs to go in the tag.
	Key string
	// Path is the absolute path of the JPEG. The HTTP layer opens it directly.
	Path string
	// Size and ModTime are the file's, for Content-Length and Last-Modified.
	Size    int64
	ModTime time.Time
	// SourceWidth and SourceHeight are the intrinsic dimensions of the page the
	// thumbnail was made from (FR-VWR-004). They are zero on a cache hit, where
	// nothing was decoded; the persisted values live in pages.width/height.
	SourceWidth  int
	SourceHeight int
}

// Index is the slice of internal/index this package needs. It is declared here,
// by the consumer, so that *index.DB stays a concrete type (impl-plan §5.1).
type Index interface {
	GetBook(ctx context.Context, id string) (index.BookRow, error)
	GetPage(ctx context.Context, bookID string, pageNo int) (index.Page, error)
	ListPages(ctx context.Context, bookID string) ([]index.Page, error)
	UpdateDims(ctx context.Context, bookID string, dims []index.PageDims) error
}

// Sources opens a book. *source.Factory satisfies it.
type Sources interface {
	Open(ctx context.Context, b source.Book) (source.BookSource, error)
}

// Roots resolves a configured root name to its *os.Root — path-traversal
// layer 3 (arch §8.1). *source.RootSet satisfies it.
type Roots interface {
	Root(name string) (*os.Root, bool)
}

// pageSizer is implemented by a source whose pages have an intrinsic size that
// can be read without rasterising — today only the PDF source. Using it keeps a
// PDF's stored dimensions the page's real geometry rather than whatever width
// the thumbnail happened to be rendered at.
type pageSizer interface {
	PageSize(ctx context.Context, pageNo int) (width, height float64, err error)
}

// The three dependencies above are satisfied by the wave-1 concrete types.
// These assertions are what turn a signature drift in those packages into a
// compile error here rather than a runtime nil.
var (
	_ Index   = (*index.DB)(nil)
	_ Sources = (*source.Factory)(nil)
	_ Roots   = (*source.RootSet)(nil)
)

// Defaults for the knobs that have no config key of their own.
const (
	// defaultPageQueue bounds the lazy queue. At 4 workers and ~15 ms per
	// thumbnail this is ~1 s of backlog: long enough that a fast scroll does
	// not drop work that is about to be wanted, short enough that a reader who
	// scrolled past 2 000 thumbnails is not still generating them a minute
	// later. Overflow drops the OLDEST, which is the one furthest from what the
	// reader is now looking at.
	defaultPageQueue = 256
	// defaultNegativeTTL is arch §5.5's "a negative result is cached in memory
	// for 10 minutes so we do not retry the decode on every scroll".
	defaultNegativeTTL = 10 * time.Minute
	// defaultUsageTTL is arch §7.9's "the walk is cached for 60 s".
	defaultUsageTTL = 60 * time.Second
	// defaultMaxSourcePixels is the ceiling on a source picture's DECLARED area,
	// the companion to `thumbnails.max_source_bytes`'s ceiling on its compressed
	// size. The two measure different things and only the pair is a bound: a
	// decoder allocates from the header, not from the entry length.
	//
	// 40 Mpx (41 943 040) is ~10× the 1600×2400 page arch §5.4 sizes the
	// ~25 MiB-per-decode budget from, and about 1.2× a 600 dpi A4 scan
	// (4960×7016 = 34.8 Mpx) — the largest shape a real book page plausibly
	// takes. Above it we are certainly looking at a malformed or hostile header,
	// and refusing costs one placeholder thumbnail (FR-LIB-008) while
	// /pages/{n} still streams the original untouched (FR-SRV-008).
	defaultMaxSourcePixels = 40 << 20
)

// Options configures a [Service]. Zero values select the documented defaults,
// so a test needs only CacheDir plus whichever dependencies it exercises.
type Options struct {
	// CacheDir is `storage.cache_dir`. Required. Nothing is written outside it.
	CacheDir string
	// Widths is `thumbnails.widths`, ascending. Zero selects amendment A-1's
	// [120, 240, 400, 640].
	Widths []int
	// Quality is `thumbnails.quality` (1..100). Zero selects 82.
	Quality int
	// Format is `thumbnails.format`. Only "jpeg" exists in v1 (CON-003); it is
	// a hash input, so a future change invalidates the cache by construction.
	Format string
	// Workers is `thumbnails.workers` — FR-THM-005's configurable concurrency
	// bound. Zero selects min(4, NumCPU).
	//
	// It bounds EVERY generation path, not just the pool: [Service.Generate] and
	// [Service.Get] take the same permits the workers do, so a caller that
	// bypasses the queues (the eager cover pass, a benchmark) cannot put more
	// decodes in flight than the configuration allows.
	Workers int
	// MaxSourceBytes is `thumbnails.max_source_bytes`. Zero selects 64 MiB.
	// It bounds the COMPRESSED size of one entry; MaxSourcePixels bounds what
	// that entry is allowed to decode INTO.
	MaxSourceBytes int64
	// MaxSourcePixels caps the declared area (width × height) of a source
	// picture. Zero selects 40 Mpx. Anything above it is refused as
	// [ReasonSourceTooLargePixels] before a pixel buffer is allocated.
	//
	// There is no `thumbnails:` key for this yet; it is an Option so the
	// composition root can lower it on a memory-tight install.
	MaxSourcePixels int64
	// AVIFEnabled is `thumbnails.avif_enabled`. AVIF decoding is serialised
	// behind a one-permit semaphore whatever Workers says (D-25): it costs
	// ~1.1 s and ~170 MiB per picture and zero AVIF files exist in the
	// reference collection.
	AVIFEnabled bool
	// PageQueue bounds the lazy queue. Zero selects defaultPageQueue.
	PageQueue int
	// NegativeTTL is how long an undecodable verdict is remembered. Zero
	// selects 10 min.
	NegativeTTL time.Duration
	// UsageTTL is how long a cache-usage walk is reused. Zero selects 60 s.
	UsageTTL time.Duration

	// Index, Sources and Roots are the dependencies. Index and Sources are
	// required for page thumbnails; Roots is required for loose-file covers.
	// A Service built without one simply fails those requests, which is what
	// lets a test exercise the cache alone.
	Index   Index
	Sources Sources
	Roots   Roots

	// Logger receives one warn line per isolated per-page failure. Zero selects
	// slog.Default().
	Logger *slog.Logger
	// Now is the clock, a test seam. Zero selects time.Now.
	Now func() time.Time

	// hookDecode runs at the top of every decode. It is unexported because it
	// exists solely so the package's own tests can observe decode concurrency
	// (FR-THM-005) without a sleep; setting it after New would be a data race,
	// which is why it lives here rather than on Service.
	hookDecode func()

	// noWorkers suppresses the worker pool so the package's own tests can
	// inspect the queues deterministically instead of racing them. Unexported
	// for the obvious reason: a Service with no workers never generates
	// anything.
	noWorkers bool
}

// FromConfig fills the knobs that have a `thumbnails:` key from a loaded
// configuration. The dependencies are left for the composition root to add.
func FromConfig(cfg *config.Config) Options {
	return Options{
		CacheDir:       cfg.Storage.CacheDir,
		Widths:         append([]int(nil), cfg.Thumbnails.Widths...),
		Quality:        cfg.Thumbnails.Quality,
		Format:         cfg.Thumbnails.Format,
		Workers:        cfg.Thumbnails.Workers,
		MaxSourceBytes: cfg.Thumbnails.MaxSourceBytes,
		AVIFEnabled:    cfg.Thumbnails.AVIFEnabled,
	}
}

// Stats is the counter set for diagnostics and for the tests that assert the
// single-flight and queue-bound properties.
//
// The last four fields are one snapshot of the work in progress and together
// they are exactly the conjunction [Service.idle] tests, which is why they are
// worth exporting rather than leaving to the counters. A job is accounted for
// continuously from the moment it is queued until its bytes are on disk:
// CoverDepth/PageDepth hold it from enqueue until a worker takes it (both
// happen under s.mu, so it is never in the gap), Active holds it for the length
// of that worker's turn, and Inflight holds the key from the moment
// singleflight claims it until after cache.publish has done its
// write-temp-then-os.Rename. All four zero therefore means every thumbnail ever
// queued is at its final path.
//
// Nothing weaker is that guarantee. The scan's own `covers_done == covers_total`
// is derived from queue depth alone (internal/app/covers.go says so in its own
// comment), so the up-to-`Workers` decodes still running count as done —
// measured on the E2E subset at `thumbnails.workers: 4`: 32-33 of 36 files
// existed when the phase reported 36/36.
type Stats struct {
	Hits       int64 // Get answered from the cache
	Queued     int64 // jobs handed to a worker
	Dropped    int64 // page jobs evicted by the bounded queue
	Generated  int64 // thumbnails written
	Failed     int64 // generations that ended in an error
	Decodes    int64 // image decodes performed — one per generation, never more
	Negative   int64 // Get answered from the undecodable memo
	DimsPages  int64 // pages probed by the dimension pass
	DimsBytes  int64 // bytes read from the media volume by that pass
	CoverDepth int   // cover jobs waiting
	PageDepth  int   // page jobs waiting
	Active     int   // workers between taking a job and finishing it
	Inflight   int   // distinct keys inside singleflight right now
}

// Service is the thumbnail cache and its worker pool. One value serves the
// whole process. It is safe for concurrent use; call Close exactly once.
type Service struct {
	cache     *cache
	widths    []int
	maxSource int64
	maxPixels int64
	avif      bool
	negTTL    time.Duration
	log       *slog.Logger
	now       func() time.Time

	idx   Index
	src   Sources
	roots Roots

	// decodeSem is FR-THM-005's bound. It holds `thumbnails.workers` permits and
	// is taken around the whole of produce — read, decode, resize, encode — by
	// EVERY generation path, so the bound belongs to the service rather than to
	// the pool. Sizing the pool alone would have left Service.Generate (the
	// eager cover pass, benchmarks) unbounded: 24 concurrent Generate calls
	// measured 24 concurrent decodes at Workers=2, i.e. ~600 MiB of RGBA against
	// arch §5.4's ~25 MiB × workers.
	//
	// Waiters coalescing on an in-flight key do NOT hold a permit — only the one
	// goroutine actually producing does — so the permit count is exactly the
	// number of decodes in flight.
	decodeSem chan struct{}

	// avifSem is the one-permit AVIF gate of D-25, held across a decode
	// regardless of how many workers exist. It nests INSIDE decodeSem, always in
	// that order.
	avifSem chan struct{}

	// ctx is cancelled by Close; every background job runs under it.
	ctx    context.Context
	cancel context.CancelFunc

	mu           sync.Mutex
	cond         *sync.Cond
	coverQ       []job
	pageQ        []job
	pageQueueMax int
	queued       map[string]struct{}
	inflight     map[string]*flight
	negative     map[string]negEntry
	active       int
	closed       bool

	dims dimsState

	usage usageCache

	wg      sync.WaitGroup
	stopped chan struct{}

	counters counters

	// hookDecode, when set by a test, runs at the top of every decode. It is
	// how the concurrency bound of FR-THM-005 is measured without a sleep.
	hookDecode func()
}

type counters struct {
	hits      atomic.Int64
	queued    atomic.Int64
	dropped   atomic.Int64
	generated atomic.Int64
	failed    atomic.Int64
	decodes   atomic.Int64
	negative  atomic.Int64
	dimsPages atomic.Int64
	dimsBytes atomic.Int64
}

// New builds a Service and starts its workers. ctx bounds the lifetime of every
// background job; Close stops them regardless.
func New(ctx context.Context, opts Options) (*Service, error) {
	if opts.CacheDir == "" {
		return nil, errors.New("thumbs: Options.CacheDir is empty")
	}
	c, err := newCache(opts.CacheDir, opts.Format, opts.Quality)
	if err != nil {
		return nil, err
	}

	widths := normaliseWidths(opts.Widths)
	workers := opts.Workers
	if workers <= 0 {
		workers = min(4, max(1, runtime.NumCPU()))
	}
	// permits is the FR-THM-005 bound and is deliberately NOT the number of
	// worker goroutines: opts.noWorkers zeroes the pool so a test can inspect the
	// queues, and a service with zero permits would deadlock every direct
	// Service.Generate instead.
	permits := workers
	maxSource := opts.MaxSourceBytes
	if maxSource <= 0 {
		maxSource = 64 << 20
	}
	maxPixels := opts.MaxSourcePixels
	if maxPixels <= 0 {
		maxPixels = defaultMaxSourcePixels
	}
	pageQueue := opts.PageQueue
	if pageQueue <= 0 {
		pageQueue = defaultPageQueue
	}
	negTTL := opts.NegativeTTL
	if negTTL <= 0 {
		negTTL = defaultNegativeTTL
	}
	usageTTL := opts.UsageTTL
	if usageTTL <= 0 {
		usageTTL = defaultUsageTTL
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}

	sctx, cancel := context.WithCancel(ctx)
	s := &Service{
		cache:        c,
		widths:       widths,
		maxSource:    maxSource,
		maxPixels:    maxPixels,
		avif:         opts.AVIFEnabled,
		negTTL:       negTTL,
		log:          log,
		now:          now,
		idx:          opts.Index,
		src:          opts.Sources,
		roots:        opts.Roots,
		decodeSem:    make(chan struct{}, permits),
		avifSem:      make(chan struct{}, 1),
		ctx:          sctx,
		cancel:       cancel,
		pageQueueMax: pageQueue,
		queued:       make(map[string]struct{}),
		inflight:     make(map[string]*flight),
		negative:     make(map[string]negEntry),
		stopped:      make(chan struct{}),
		hookDecode:   opts.hookDecode,
	}
	s.cond = sync.NewCond(&s.mu)
	s.usage.ttl = usageTTL
	s.dims.pending = make(map[string][]index.PageDims)
	s.dims.queued = make(map[string]struct{})
	s.dims.signal = make(chan struct{}, 1)

	if opts.noWorkers {
		workers = 0
	}
	s.wg.Add(workers + 1)
	for range workers {
		go s.worker()
	}
	go s.dimsWorker()
	return s, nil
}

// Widths reports the configured width ladder, ascending. The HTTP layer needs
// it to document the default (`widths[0]`, amendment A-6).
func (s *Service) Widths() []int { return append([]int(nil), s.widths...) }

// CacheDir reports the root of the cache, for `GET /api/cache/usage`.
func (s *Service) CacheDir() string { return s.cache.dir }

// Stats snapshots the counters.
//
// The four work-in-progress numbers are read under ONE hold of s.mu, and that
// is load-bearing rather than tidy. Split across two acquisitions they can all
// read zero while a decode is running: a page thumbnail enqueued after the
// `active` read and taken by a worker before the queue-depth read is counted by
// neither, and a caller that treats "all four zero" as "every file is on disk"
// would then be told the cache is settled while a temp file is still open.
func (s *Service) Stats() Stats {
	s.mu.Lock()
	coverDepth, pageDepth := len(s.coverQ), len(s.pageQ)
	active, inflight := s.active, len(s.inflight)
	s.mu.Unlock()
	return Stats{
		Hits:       s.counters.hits.Load(),
		Queued:     s.counters.queued.Load(),
		Dropped:    s.counters.dropped.Load(),
		Generated:  s.counters.generated.Load(),
		Failed:     s.counters.failed.Load(),
		Decodes:    s.counters.decodes.Load(),
		Negative:   s.counters.negative.Load(),
		DimsPages:  s.counters.dimsPages.Load(),
		DimsBytes:  s.counters.dimsBytes.Load(),
		CoverDepth: coverDepth,
		PageDepth:  pageDepth,
		Active:     active,
		Inflight:   inflight,
	}
}

// Close stops the workers and waits for them. Queued-but-unstarted work is
// dropped: it is a cache, and the next request re-enqueues it.
func (s *Service) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.coverQ, s.pageQ = nil, nil
	clear(s.queued)
	s.cond.Broadcast()
	s.mu.Unlock()

	close(s.stopped)
	s.cancel()
	s.wg.Wait()
	return nil
}

// normaliseWidths sorts, deduplicates and drops non-positive entries, falling
// back to amendment A-1's ladder when nothing usable is left.
func normaliseWidths(in []int) []int {
	out := make([]int, 0, len(in))
	seen := make(map[int]struct{}, len(in))
	for _, w := range in {
		if w <= 0 {
			continue
		}
		if _, dup := seen[w]; dup {
			continue
		}
		seen[w] = struct{}{}
		out = append(out, w)
	}
	if len(out) == 0 {
		return []int{120, 240, 400, 640}
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// snapWidth rounds a requested width UP to the nearest configured width and
// clamps it to the largest (impl-plan §0.4). Snapping up rather than to the
// nearest is deliberate: a thumbnail smaller than the box it is drawn in is
// visibly soft, and the frontend is expected to send an exact member of the
// ladder anyway.
func (s *Service) snapWidth(w int) int {
	if w <= 0 {
		return s.widths[0]
	}
	for _, cand := range s.widths {
		if cand >= w {
			return cand
		}
	}
	return s.widths[len(s.widths)-1]
}
