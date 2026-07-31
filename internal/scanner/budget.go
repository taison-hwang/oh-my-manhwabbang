package scanner

import (
	"context"
	"sync"
)

// The memory bound of arch §4.1.
//
// arch §4.1 says "the results channel is bounded (512); readers block when the
// writer falls behind, which bounds memory", and its pipeline diagram carries
// *per-book* results into the writer. This implementation aggregates a whole
// series before handing it over — a series is the unit the writer must have in
// one piece, because series.book_count, series.page_count and the cover ladder
// are all folds over every book in it, and because books.series_id is a foreign
// key onto a row that has to exist first.
//
// That aggregation is what makes the slot count a poor proxy for memory: 512
// slots hold 512 *series*, and the reference collection averages ~1,414 page
// rows per series rather than the ~122 of one book. Left at that, peak heap
// tracks page count rather than series count and a cold scan of the reference
// collection would hold several hundred MiB of index.Page on a product whose
// primary deployment target is a NAS (NFR-OPS-003).
//
// So the channel stays bounded at 512, exactly as arch §4.1 and impl-plan §3
// (WP-08, item 1) require, and a second gate bounds what those 512 slots are
// allowed to hold: the page rows themselves. The channel bounds the pipeline's
// *depth*, the budget bounds its *weight*.

// maxInFlightPages is how many index.Page rows may sit between a worker that
// produced them and the writer that has not yet written them.
//
// 512 books at the reference collection's 1.36 M entries / 11 157 archives =
// ~122 entries per archive — i.e. arch §4.1's 512 slots, measured in the unit
// its diagram actually draws. At roughly 200 bytes per materialised page row
// that is ~13 MiB of in-flight index, and it does not grow with the size of the
// library.
const maxInFlightPages = 64 << 10 // 65 536

// pageBudget is the gate. It is a counting semaphore with one extra rule, and
// that rule is what makes it deadlock-free.
//
// # Why it cannot deadlock
//
// A worker takes the budget immediately before offering its series to the
// writer, and the writer returns it the moment that series has been written.
// Everything the budget counts therefore belongs to a series that is already in
// the results channel, is being written, or is in the hands of a worker that is
// not itself blocked — a worker waiting for budget holds none of it. The writer
// never blocks on anything but the results channel (index.Writer owns the write
// connection and CoverQueue.EnqueueCover is documented as non-blocking), so it
// always drains, the budget always comes back, and `held` always reaches zero.
//
// The extra rule handles the one series that is bigger than the whole budget:
// an acquire always succeeds when nothing is in flight. A 1,540-page volume, or
// a series of them, is admitted rather than wedging the pipeline for ever — the
// bound is on *concurrency*, never on what a single item is allowed to be.
type pageBudget struct {
	limit int64

	mu   sync.Mutex
	held int64
	// wake is closed and replaced on every release. It is a broadcast that a
	// waiter can select on together with ctx.Done(), which sync.Cond cannot do.
	wake chan struct{}

	// Diagnostics, and the only honest way for a test to prove the gate is
	// wired in at all rather than merely present.
	peak     int64
	acquired int64
	waits    int64
}

func newPageBudget(limit int64) *pageBudget {
	if limit < 1 {
		limit = 1
	}
	return &pageBudget{limit: limit, wake: make(chan struct{})}
}

// acquire takes n page slots, blocking until they are free. It reports false
// only when ctx was cancelled while waiting, in which case nothing was taken.
//
// n <= 0 is free: a series with no page rows costs no memory, and making it
// wait would let a single oversized series that is already in flight block a
// task that is not there.
func (b *pageBudget) acquire(ctx context.Context, n int64) bool {
	if n <= 0 {
		return true
	}
	for {
		b.mu.Lock()
		if b.held == 0 || b.held+n <= b.limit {
			b.held += n
			b.acquired += n
			if b.held > b.peak {
				b.peak = b.held
			}
			b.mu.Unlock()
			return true
		}
		b.waits++
		wake := b.wake
		b.mu.Unlock()

		select {
		case <-wake:
		case <-ctx.Done():
			return false
		}
	}
}

// release returns n page slots and wakes every waiter.
func (b *pageBudget) release(n int64) {
	if n <= 0 {
		return
	}
	b.mu.Lock()
	b.held -= n
	close(b.wake)
	b.wake = make(chan struct{})
	b.mu.Unlock()
}

// stats reports the budget's counters. held must be zero once a run has
// finished; peak is the high-water mark, which the gate holds at
// max(limit, largest single series) by construction; taken is cumulative for
// the life of the Scanner.
func (b *pageBudget) stats() (held, peak, taken, waits int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.held, b.peak, b.acquired, b.waits
}
