package httpapi

import (
	"context"
	"errors"
	"sync"
	"time"

	"shelf/internal/index"
	"shelf/internal/scanner"
)

// staleRescanCooldown is how long one book's stale-container rescan is
// suppressed after it has been enqueued.
//
// It has to exist. A reader opening a 1,071-page volume whose archive changed
// on disk makes 1,071 page requests, every one of which detects the same
// staleness; without a cooldown that is 1,071 index lookups and 1,071 calls
// into the scanner for a single fact the first request already established.
// One minute is far longer than a scan of one series takes and far shorter than
// a human notices, so the second attempt — if the first was refused because
// another scan was running — still happens while the reader is on the book.
const staleRescanCooldown = time.Minute

// rescanCoalescer remembers which books have recently been queued for a rescan.
//
// It is deliberately a plain map behind a mutex rather than anything cleverer:
// it only ever holds books that were found stale, which in a healthy library is
// none, and every entry is dropped as soon as it expires.
type rescanCoalescer struct {
	cooldown time.Duration

	mu   sync.Mutex
	last map[string]time.Time
}

func newRescanCoalescer(cooldown time.Duration) *rescanCoalescer {
	return &rescanCoalescer{cooldown: cooldown, last: make(map[string]time.Time)}
}

// claim reports whether this caller is the one that should enqueue a rescan of
// bookID, and records the decision. It expires stale entries as it goes, so the
// map cannot grow without bound over a long uptime.
func (c *rescanCoalescer) claim(bookID string, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if at, ok := c.last[bookID]; ok && now.Sub(at) < c.cooldown {
		return false
	}
	for id, at := range c.last {
		if now.Sub(at) >= c.cooldown {
			delete(c.last, id)
		}
	}
	c.last[bookID] = now
	return true
}

// enqueueStaleRescan is the second half of arch §5.2's pool rule: "if
// `size`/`mtime` disagree with the index, still serve but tag the response so
// the API can answer `409 stale` (§7.6) **and enqueue a rescan of that book**."
//
// Without it the 409 is a dead end. `detail.cv` carries the index's content
// version, which is by definition the one the client just sent — the index is
// what is out of date, not the client — so a client that refetches the book
// gets the same `cv` back, requests the same URL and is refused again.
// `web/src/api/queries.ts` gives up after STALE_VERSION_RETRIES, and the volume
// stays unreadable until somebody scans by hand. Queueing the scan here is what
// closes the loop: the rescan rewrites `content_version` from the file that is
// actually on disk, the next book fetch sees a new `cv`, and the page loads.
//
// The rescan is targeted at the book's series and never sweeps, exactly as
// `POST /api/series/{sid}/rescan` does — absence of a row is not evidence of
// absence on disk when only part of the tree was visited.
//
// Every failure is swallowed. This runs inside a request that is already
// answering 409 or streaming bytes; a scanner that is busy, shutting down or
// missing changes nothing about that answer.
func (s *Server) enqueueStaleRescan(ctx context.Context, book index.BookRow) {
	if s.scan == nil || s.idx == nil {
		return
	}
	if !s.rescans.claim(book.ID, s.now()) {
		return
	}
	// Detached from the request: the work outlives the response, and a client
	// that disconnects mid-page must not cancel the lookup and thereby burn the
	// claim it just took.
	ctx = context.WithoutCancel(ctx)

	series, err := s.idx.GetSeries(ctx, book.SeriesID)
	if err != nil {
		s.log.WarnContext(ctx, "cannot queue a rescan for a changed container",
			"book_id", book.ID, "series_id", book.SeriesID, "err", err)
		return
	}

	runID, err := s.scan.Start(ctx, scanner.Request{
		Roots:  []string{series.RootName},
		Series: []scanner.SeriesRef{{Root: series.RootName, RelPath: series.RelPath}},
	})
	if err != nil {
		if errors.Is(err, scanner.ErrBusy) || errors.Is(err, scanner.ErrClosed) {
			// A scan is already running (it may even be the one that will fix
			// this) or the process is going down. Both are ordinary.
			s.log.DebugContext(ctx, "a rescan for a changed container was not started",
				"book_id", book.ID, "err", err)
			return
		}
		s.log.WarnContext(ctx, "starting a rescan for a changed container",
			"book_id", book.ID, "err", err)
		return
	}
	s.log.InfoContext(ctx, "queued a rescan for a container that changed on disk",
		"book_id", book.ID, "root", series.RootName, "rel_path", series.RelPath, "run_id", runID)
}
