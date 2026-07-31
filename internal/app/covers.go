package app

import (
	"context"
	"sync/atomic"

	"shelf/internal/scanner"
	"shelf/internal/thumbs"
)

// coverBridge is the one piece of wiring between the two wave-2 packages.
//
// internal/scanner declares [scanner.CoverQueue] as a consumer-side interface
// and internal/thumbs returns a concrete *thumbs.Service (impl-plan §5.1, D-46),
// so neither imports the other and this type is where FR-THM-003 —
// "the cover is generated immediately, during the scan" — actually happens.
//
// Every configured width is enqueued, not just widths[0]. The library grid asks
// for w=400 or w=640 (impl-plan §0.4) and the volume tiles for w=400; enqueueing
// only the default 120 would leave every visible cover answering 202 until a
// second, lazy pass caught up, which is the failure FR-THM-003 exists to
// prevent. The cover queue is unbounded and drained ahead of the page queue, so
// the extra jobs cost background decode time and nothing on the scan's critical
// path.
type coverBridge struct {
	svc    *thumbs.Service
	widths []int

	// accepted counts the jobs handed to the service. It is the denominator of
	// the `covers` phase of arch §4.12.
	accepted atomic.Int64
}

func newCoverBridge(svc *thumbs.Service) *coverBridge {
	return &coverBridge{svc: svc, widths: svc.Widths()}
}

// EnqueueCover implements scanner.CoverQueue. It must not block: the scanner
// calls it from the goroutine that owns the index write connection.
// thumbs.Service.Enqueue only ever appends to an unbounded slice under a mutex.
func (c *coverBridge) EnqueueCover(ctx context.Context, req scanner.CoverRequest) {
	if err := ctx.Err(); err != nil {
		return
	}
	base := thumbs.Request{
		ID:             req.SeriesID,
		Priority:       thumbs.PriorityCover,
		ContentVersion: req.ContentVersion,
	}
	switch req.Kind {
	case scanner.CoverFile:
		// A loose image in the series directory: keyed by the series, read
		// through that root's os.Root. ContentVersion is empty here, exactly as
		// GET /api/series/{sid}/cover sends it (index.SeriesRow.CoverCV is only
		// set for a page cover), so both paths land on the same cache key.
		base.RootName = req.RootName
		base.RelPath = req.RelPath
	case scanner.CoverPage:
		// cover_kind='page': the key belongs to the book, not the series —
		// which is what httpapi.handleSeriesCover does too.
		base.ID = req.BookID
		base.PageNo = req.PageNo
	default:
		return
	}

	for _, w := range c.widths {
		r := base
		r.Width = w
		if err := c.svc.Enqueue(r); err != nil {
			return // the service is closed; the scan is on its way down too
		}
		c.accepted.Add(1)
	}
}

// CoverProgress implements scanner.CoverProgressReporter so the `covers` phase
// of arch §4.12 reports real numbers.
//
// done is derived from the queue depth rather than from a completion counter:
// the service deduplicates by cache key and skips covers that are already on
// disk, so counting generations would make `done` permanently short of `total`
// on an incremental scan and the phase would spin until its two-minute limit.
// The one inaccuracy is that jobs currently being decoded — at most
// `thumbnails.workers` of them — are counted as done, so the phase can end a
// few hundred milliseconds early. That is the right way round: it never hangs.
func (c *coverBridge) CoverProgress() (done, total int64) {
	total = c.accepted.Load()
	depth := int64(c.svc.Stats().CoverDepth)
	done = total - depth
	if done < 0 {
		done = 0
	}
	return done, total
}

// The scanner's two interfaces, asserted here so a signature drift in either
// package is a compile error in the composition root rather than a nil at
// runtime.
var (
	_ scanner.CoverQueue            = (*coverBridge)(nil)
	_ scanner.CoverProgressReporter = (*coverBridge)(nil)
)
