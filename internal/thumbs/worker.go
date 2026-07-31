package thumbs

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// job is one unit of generation: a validated request plus the cache key it
// resolves to.
type job struct {
	req Request
	key string
}

// flight is one in-progress generation. Every goroutine that wants the same key
// waits on the same flight, which is how N concurrent misses become exactly one
// decode (impl-plan WP-07 acceptance 4).
type flight struct {
	done chan struct{}
	res  Result
	err  error
}

// negEntry memoises an undecodable verdict. arch §5.5: "a negative result is
// cached in memory for 10 minutes so we do not retry the decode on every
// scroll". Without it, a viewer scrolling past one animated WebP would pay the
// full read-and-fail cost on every frame of the thumbnail strip.
type negEntry struct {
	err error
	at  time.Time
}

// prepare validates a request and derives its cache key.
func (s *Service) prepare(req Request) (job, error) {
	if req.ID == "" {
		return job{}, fmt.Errorf("%w: empty id", ErrBadRequest)
	}
	if req.fromFile() {
		if req.RootName == "" {
			return job{}, fmt.Errorf("%w: file request for %q has no root", ErrBadRequest, req.ID)
		}
		if _, err := safeRel(req.RelPath); err != nil {
			return job{}, fmt.Errorf("%w: %s", ErrBadRequest, err)
		}
		// A loose file has no page. Pinning it to 0 rather than trusting the
		// caller keeps one file from being cached under two keys.
		req.PageNo = 0
	} else if req.PageNo < 1 {
		return job{}, fmt.Errorf("%w: page number %d is below 1 (pages are 1-based)", ErrBadRequest, req.PageNo)
	}
	req.Width = s.snapWidth(req.Width)
	return job{req: req, key: s.cache.key(req.ID, req.PageNo, req.Width, req.ContentVersion)}, nil
}

// Get returns a ready thumbnail, or enqueues one and reports [ErrQueued]. It
// never blocks on generation, which is what lets the HTTP layer answer
// `202 + Retry-After` instead of holding a connection (impl-plan §4 point 3).
//
// The three outcomes a caller must handle:
//
//	err == nil                          → serve Result.Path
//	errors.Is(err, ErrQueued)           → 202, Retry-After: 1
//	errors.Is(err, ErrUndecodable)      → 422 thumb_unavailable, detail.reason
//
// plus [ErrNotFound] (404) and [ErrBadRequest] (400).
func (s *Service) Get(ctx context.Context, req Request) (Result, error) {
	j, err := s.prepare(req)
	if err != nil {
		return Result{}, err
	}
	if res, ok := s.cache.lookup(KindThumbs, j.key); ok {
		s.counters.hits.Add(1)
		return res, nil
	}
	if err := s.negativeFor(j.key); err != nil {
		s.counters.negative.Add(1)
		return Result{}, err
	}
	if err := s.enqueue(j); err != nil {
		return Result{}, err
	}
	return Result{}, ErrQueued
}

// Generate produces the thumbnail synchronously, coalescing with any in-flight
// generation of the same key.
//
// It is the blocking twin of [Get] and exists for three callers: the eager
// cover pass when it wants to know the outcome, benchmarks, and tests that must
// observe a finished thumbnail without polling. The HTTP layer must NOT use it
// — a slow archive would hold a connection for the whole decode.
func (s *Service) Generate(ctx context.Context, req Request) (Result, error) {
	j, err := s.prepare(req)
	if err != nil {
		return Result{}, err
	}
	if res, ok := s.cache.lookup(KindThumbs, j.key); ok {
		s.counters.hits.Add(1)
		return res, nil
	}
	if err := s.negativeFor(j.key); err != nil {
		s.counters.negative.Add(1)
		return Result{}, err
	}
	return s.singleflight(ctx, j)
}

// Enqueue schedules a thumbnail without asking whether it is ready — the
// scanner's cover-first path (FR-THM-003). A cover that is already cached is
// not re-generated.
func (s *Service) Enqueue(req Request) error {
	j, err := s.prepare(req)
	if err != nil {
		return err
	}
	if _, ok := s.cache.lookup(KindThumbs, j.key); ok {
		return nil
	}
	if err := s.negativeFor(j.key); err != nil {
		return nil // already known bad; nothing to schedule
	}
	return s.enqueue(j)
}

// enqueue puts a job on the right queue, deduplicating by key.
//
// FR-THM-003 and FR-THM-004 are the two queues: coverQ is unbounded and always
// drained first so covers appear during a scan; pageQ is bounded and drops the
// OLDEST job on overflow, because the oldest lazy request is the one the reader
// has already scrolled past.
func (s *Service) enqueue(j job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	if _, dup := s.queued[j.key]; dup {
		return nil
	}
	if _, running := s.inflight[j.key]; running {
		return nil
	}
	if j.req.Priority == PriorityCover {
		s.coverQ = append(s.coverQ, j)
	} else {
		if len(s.pageQ) >= s.pageQueueMax {
			oldest := s.pageQ[0]
			s.pageQ = s.pageQ[1:]
			delete(s.queued, oldest.key)
			s.counters.dropped.Add(1)
		}
		s.pageQ = append(s.pageQ, j)
	}
	s.queued[j.key] = struct{}{}
	s.counters.queued.Add(1)
	s.cond.Signal()
	return nil
}

// takeLocked pops the next job. The cover queue wins whenever it is non-empty,
// which is FR-THM-003's "covers are generated first" in one branch: a reader
// scrolling a thumbnail strip can fill the page queue without ever delaying the
// covers a running scan is producing.
//
// The caller holds s.mu.
func (s *Service) takeLocked() (job, bool) {
	if len(s.coverQ) > 0 {
		j := s.coverQ[0]
		s.coverQ = s.coverQ[1:]
		delete(s.queued, j.key)
		return j, true
	}
	if len(s.pageQ) > 0 {
		j := s.pageQ[0]
		s.pageQ = s.pageQ[1:]
		delete(s.queued, j.key)
		return j, true
	}
	return job{}, false
}

// take is takeLocked with the lock, for the tests that assert queue order.
func (s *Service) take() (job, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.takeLocked()
}

// worker drains the two queues until Close.
func (s *Service) worker() {
	defer s.wg.Done()
	for {
		s.mu.Lock()
		for !s.closed && len(s.coverQ) == 0 && len(s.pageQ) == 0 {
			s.cond.Wait()
		}
		if s.closed {
			s.mu.Unlock()
			return
		}
		j, ok := s.takeLocked()
		if !ok {
			s.mu.Unlock()
			continue
		}
		s.active++
		s.mu.Unlock()

		_, err := s.singleflight(s.ctx, j)
		if err != nil && !errors.Is(err, context.Canceled) {
			s.log.Warn("thumbnail generation failed",
				"book_id", j.req.ID, "page", j.req.PageNo, "width", j.req.Width,
				"priority", j.req.Priority.String(), "err", err)
		}

		s.mu.Lock()
		s.active--
		s.cond.Broadcast()
		s.mu.Unlock()
	}
}

// singleflight runs produce for a key at most once at a time. Every other
// caller of the same key waits for that one result — the counter assertion of
// impl-plan WP-07 acceptance 4 is exactly this function.
//
// It is also where FR-THM-005's bound is applied, because it is the ONE place
// every generation path passes through: the worker pool, Service.Get's queue and
// the direct Service.Generate all end here. Bounding the pool alone would leave
// the eager cover pass free to run a decode per goroutine.
func (s *Service) singleflight(ctx context.Context, j job) (Result, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return Result{}, ErrClosed
	}
	if f, ok := s.inflight[j.key]; ok {
		s.mu.Unlock()
		select {
		case <-f.done:
			return f.res, f.err
		case <-ctx.Done():
			return Result{}, ctx.Err()
		}
	}
	f := &flight{done: make(chan struct{})}
	s.inflight[j.key] = f
	s.mu.Unlock()

	// From here every exit must close f.done, or the coalesced callers above
	// wait forever — which is why the permit failure flows into the same tail
	// rather than returning early.
	var res Result
	release, err := s.acquireDecode(ctx)
	if err == nil {
		res, err = s.produce(ctx, j)
		release()
	}

	s.mu.Lock()
	delete(s.inflight, j.key)
	// Memoise the verdict, not the error's identity: the reason travels with it
	// so a later 422 still carries detail.reason.
	//
	// The ctx.Err() guard matters more than it looks. A shutdown that cancels a
	// read mid-decode must not leave "this page is undecodable" in the memo for
	// the next ten minutes — that would turn one interrupted request into a
	// visibly broken thumbnail long after the cause was gone.
	if err != nil && errors.Is(err, ErrUndecodable) && ctx.Err() == nil {
		s.negative[j.key] = negEntry{err: err, at: s.now()}
	}
	s.mu.Unlock()

	f.res, f.err = res, err
	close(f.done)

	switch {
	case err == nil:
		s.counters.generated.Add(1)
	case errors.Is(err, context.Canceled):
		// Shutdown, not a failure.
	default:
		s.counters.failed.Add(1)
	}
	return res, err
}

// acquireDecode takes one of the `thumbnails.workers` generation permits and
// returns its release.
//
// The permit covers the whole of produce, not just the image.Decode call,
// because arch §5.4 sizes the budget on what is resident at once: "each
// in-flight decode holds the compressed source plus an RGBA buffer — a
// 1600×2400 page is ~15 MiB of RGBA alone, so peak RSS ≈ 25 MiB × workers". The
// compressed source is alive from readSource until the JPEG is published.
//
// It is never taken while s.mu is held, and a caller waiting on another
// goroutine's flight never holds one, so the only lock ordering in the package
// is decodeSem → avifSem.
func (s *Service) acquireDecode(ctx context.Context) (release func(), err error) {
	select {
	case s.decodeSem <- struct{}{}:
		return func() { <-s.decodeSem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.stopped:
		// A caller whose own context outlives the service — the eager cover pass
		// on a background context — must not sit on a permit queue after Close.
		return nil, ErrClosed
	}
}

// negativeFor returns the memoised undecodable verdict for a key, if it is
// still fresh.
func (s *Service) negativeFor(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.negative[key]
	if !ok {
		return nil
	}
	if s.now().Sub(e.at) >= s.negTTL {
		delete(s.negative, key)
		return nil
	}
	return e.err
}

// forgetNegatives drops every memoised failure. A purge calls it so that
// "delete the cache and try again" really does try again, rather than replaying
// a ten-minute-old verdict.
func (s *Service) forgetNegatives() {
	s.mu.Lock()
	clear(s.negative)
	s.mu.Unlock()
}

// idle reports that no background work is outstanding.
func (s *Service) idle() bool {
	s.mu.Lock()
	quiet := len(s.coverQ) == 0 && len(s.pageQ) == 0 && s.active == 0 && len(s.inflight) == 0
	s.mu.Unlock()
	if !quiet {
		return false
	}
	return s.dimsIdle()
}

// Drain blocks until every queued thumbnail and every pending dimension write
// has been dealt with.
//
// It is a test and shutdown affordance, not a request path: the HTTP layer must
// never wait for generation (that is what [ErrQueued] is for). The scanner uses
// it to know when its `covers` phase is really finished.
func (s *Service) Drain(ctx context.Context) error {
	const poll = 500 * time.Microsecond
	t := time.NewTicker(poll)
	defer t.Stop()
	for {
		if s.idle() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.stopped:
			return ErrClosed
		case <-t.C:
		}
	}
}
