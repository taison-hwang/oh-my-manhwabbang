package thumbs

import (
	"bufio"
	"context"
	"errors"
	"image"
	"io"
	"math"
	"sync"
	"time"

	"shelf/internal/index"
	"shelf/internal/source"
)

// Page dimensions — FR-VWR-004, arch §5.8.
//
// Spread mode has to know BEFORE layout whether a page is a double-page scan,
// or every wide page produces a visible re-flow. Reading dimensions for all
// 1.36 M pages at scan time would mean one random seek per page, which is the
// access pattern that blew past ten minutes in the architecture spike. So the
// value is filled in two ways, both cheap:
//
//   - for free whenever a thumbnail is generated, because the picture has
//     already been decoded (see produce);
//   - by a low-priority background pass that opens each entry and reads only
//     until image.DecodeConfig succeeds — measured at 23 µs for JPEG, 10 µs for
//     PNG. The cost is the seek, not the parse.
//
// Until a page has been measured, PageInfo.w/h are null and the viewer uses the
// image's natural size once loaded, treating an unknown page as single. No
// blocking, no layout shift.

const (
	// dimsProbeCap is the hard ceiling on how much of one entry the dimension
	// pass may read. A JPEG's SOF marker lives within a few kilobytes of the
	// start; anything that has not declared its size within 64 KiB is not worth
	// a random read on a spinning disk.
	//
	// impl-plan WP-07 acceptance 7 asserts against this number directly.
	dimsProbeCap = 64 << 10
	// dimsProbeChunk is the buffered-reader size, i.e. the granularity at which
	// bytes actually leave the archive. Small enough that a JPEG header costs
	// one or two reads.
	dimsProbeChunk = 4 << 10
	// dimsFlushInterval bounds how long a dimension learned during thumbnail
	// generation waits before it reaches the database. Batching matters: each
	// flush takes index's single write permit, and a scan is often holding it.
	dimsFlushInterval = 100 * time.Millisecond
	// dimsFlushRows forces an early flush once this many rows are pending.
	dimsFlushRows = 128
	// dimsPassBatch is how many measured pages the background pass accumulates
	// before writing, so `dims_state` moves none → partial → done rather than
	// jumping.
	dimsPassBatch = 64
)

// dimsState is the Service's dimension bookkeeping: rows waiting to be written,
// and books waiting to be swept.
type dimsState struct {
	mu      sync.Mutex
	pending map[string][]index.PageDims
	rows    int
	queue   []string
	queued  map[string]struct{}
	active  bool
	signal  chan struct{}
}

// recordDims remembers one measured page. It never blocks and never writes:
// the flusher owns every database call, so a thumbnail worker cannot end up
// waiting on the scanner's write permit mid-decode.
func (s *Service) recordDims(bookID string, pageNo, w, h int) {
	if bookID == "" || pageNo < 1 || w <= 0 || h <= 0 {
		return
	}
	if s.idx == nil {
		return
	}
	s.dims.mu.Lock()
	s.dims.pending[bookID] = append(s.dims.pending[bookID], index.PageDims{PageNo: pageNo, Width: w, Height: h})
	s.dims.rows++
	full := s.dims.rows >= dimsFlushRows
	s.dims.mu.Unlock()
	if full {
		s.signalDims()
	}
}

// EnsureDims schedules the background dimension pass for a book (FR-VWR-004).
//
// The HTTP layer calls it when a book is opened with `dims_state='none'`; the
// call returns immediately and the pass runs at low priority on a single
// goroutine, so it can never starve the thumbnail workers.
func (s *Service) EnsureDims(bookID string) {
	if bookID == "" || s.idx == nil || s.src == nil {
		return
	}
	s.dims.mu.Lock()
	if s.dims.queued == nil {
		s.dims.queued = make(map[string]struct{})
	}
	if _, dup := s.dims.queued[bookID]; dup {
		s.dims.mu.Unlock()
		return
	}
	s.dims.queued[bookID] = struct{}{}
	s.dims.queue = append(s.dims.queue, bookID)
	s.dims.mu.Unlock()
	s.signalDims()
}

func (s *Service) signalDims() {
	select {
	case s.dims.signal <- struct{}{}:
	default: // a wake-up is already pending; one is enough
	}
}

// dimsIdle reports that nothing is pending and no pass is running.
func (s *Service) dimsIdle() bool {
	s.dims.mu.Lock()
	defer s.dims.mu.Unlock()
	return s.dims.rows == 0 && len(s.dims.queue) == 0 && !s.dims.active
}

// dimsWorker is the single low-priority goroutine of arch §5.8. One goroutine
// is the whole priority scheme: the pass can never take a worker away from a
// thumbnail a reader is waiting for.
func (s *Service) dimsWorker() {
	defer s.wg.Done()
	tick := time.NewTicker(dimsFlushInterval)
	defer tick.Stop()
	for {
		select {
		case <-s.stopped:
			// Write out what has already been measured. The service context is
			// cancelled by now, so this uses a short independent deadline
			// rather than dropping the rows.
			ctx, cancel := context.WithTimeout(context.WithoutCancel(s.ctx), 2*time.Second)
			s.flushDims(ctx)
			cancel()
			return
		case <-s.dims.signal:
		case <-tick.C:
		}
		s.flushDims(s.ctx)
		s.runDimsQueue(s.ctx)
	}
}

// flushDims writes every pending measurement. index.UpdateDims re-derives
// books.dims_state from the page rows inside the same transaction, so the
// summary column can never drift from the data it describes.
func (s *Service) flushDims(ctx context.Context) {
	s.dims.mu.Lock()
	if s.dims.rows == 0 {
		s.dims.mu.Unlock()
		return
	}
	batch := s.dims.pending
	s.dims.pending = make(map[string][]index.PageDims)
	s.dims.rows = 0
	s.dims.active = true
	s.dims.mu.Unlock()

	for bookID, dims := range batch {
		if err := s.idx.UpdateDims(ctx, bookID, dims); err != nil {
			if !errors.Is(err, context.Canceled) {
				s.log.Warn("recording page dimensions failed", "book_id", bookID, "pages", len(dims), "err", err)
			}
		}
	}

	s.dims.mu.Lock()
	s.dims.active = false
	s.dims.mu.Unlock()
}

// runDimsQueue drains the EnsureDims backlog, one book at a time.
func (s *Service) runDimsQueue(ctx context.Context) {
	for {
		s.dims.mu.Lock()
		if len(s.dims.queue) == 0 {
			s.dims.mu.Unlock()
			return
		}
		bookID := s.dims.queue[0]
		s.dims.queue = s.dims.queue[1:]
		s.dims.active = true
		s.dims.mu.Unlock()

		err := s.measureBook(ctx, bookID)

		s.dims.mu.Lock()
		delete(s.dims.queued, bookID)
		s.dims.active = false
		s.dims.mu.Unlock()

		if err != nil && !errors.Is(err, context.Canceled) {
			s.log.Warn("dimension pass failed", "book_id", bookID, "err", err)
		}
		if ctx.Err() != nil {
			return
		}
	}
}

// measureBook probes every page of a book whose width is still NULL.
//
// A page that cannot be measured is skipped and logged at debug: an
// undecodable page is a normal event (FR-IDX-011 formats this build cannot
// read, a truncated entry) and it must not stop the other 1 070 pages of the
// book from being measured.
func (s *Service) measureBook(ctx context.Context, bookID string) error {
	pages, err := s.idx.ListPages(ctx, bookID)
	if err != nil {
		return err
	}
	todo := pages[:0:0]
	for _, p := range pages {
		if p.Width == nil || p.Height == nil {
			todo = append(todo, p)
		}
	}
	if len(todo) == 0 {
		return nil
	}

	row, err := s.idx.GetBook(ctx, bookID)
	if err != nil {
		return err
	}
	bs, err := s.src.Open(ctx, bookOf(row))
	if err != nil {
		return mapSourceError(bookID, err)
	}
	defer func() { _ = bs.Close() }()
	sizer, hasSizer := bs.(pageSizer)

	batch := make([]index.PageDims, 0, dimsPassBatch)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := s.idx.UpdateDims(ctx, bookID, batch); err != nil {
			return err
		}
		batch = batch[:0]
		return nil
	}

	for _, p := range todo {
		if err := ctx.Err(); err != nil {
			_ = flush()
			return err
		}
		w, h, ok := s.measurePage(ctx, bs, sizer, hasSizer, pageOf(p))
		if !ok {
			continue
		}
		batch = append(batch, index.PageDims{PageNo: p.PageNo, Width: w, Height: h})
		if len(batch) >= dimsPassBatch {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	return flush()
}

// measurePage reads one page's intrinsic size.
func (s *Service) measurePage(ctx context.Context, bs source.BookSource, sizer pageSizer, hasSizer bool, p source.Page) (int, int, bool) {
	// A PDF page's geometry is a property of the document, not of any render,
	// and reading it never rasterises anything.
	if hasSizer {
		if w, h, err := sizer.PageSize(ctx, p.No); err == nil && w > 0 && h > 0 {
			s.counters.dimsPages.Add(1)
			return int(math.Round(w)), int(math.Round(h)), true
		}
		return 0, 0, false
	}

	st, err := bs.Open(ctx, p, source.OpenOptions{})
	if err != nil {
		s.log.Debug("dimension probe could not open page", "page", p.No, "err", err)
		return 0, 0, false
	}
	defer func() { _ = st.Close() }()

	cfg, read, err := probeDims(st)
	s.counters.dimsPages.Add(1)
	s.counters.dimsBytes.Add(read)
	if err != nil {
		s.log.Debug("dimension probe failed", "page", p.No, "bytes", read, "err", err)
		return 0, 0, false
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return 0, 0, false
	}
	return cfg.Width, cfg.Height, true
}

// countingReader records how many bytes actually left the source. It is what
// turns "the probe must not read the whole entry" from a claim into an
// assertion (impl-plan WP-07 acceptance 7).
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// probeDims reads a header and stops.
//
// Two limiters, doing different jobs: io.LimitReader is the hard ceiling that
// makes a 300 MB entry cost 64 KiB in the worst case, and the small bufio
// buffer is what keeps the COMMON case down to one or two reads — DecodeConfig
// asks for a few bytes at a time, and without buffering that would be dozens of
// syscalls, while with a large buffer it would be a 64 KiB read every time.
func probeDims(r io.Reader) (image.Config, int64, error) {
	cr := &countingReader{r: io.LimitReader(r, dimsProbeCap)}
	cfg, _, err := image.DecodeConfig(bufio.NewReaderSize(cr, dimsProbeChunk))
	return cfg, cr.n, err
}
