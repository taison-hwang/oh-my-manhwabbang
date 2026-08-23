package scanner

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"

	"shelf/internal/index"
)

// Generation stamping and deletion — arch §4.9.
//
// Every run allocates one monotonic `scan_gen` and stamps every row it touches
// with it, whether the row was rewritten or merely confirmed unchanged by
// FR-IDX-003. At the end of a root, rows still carrying an older generation are
// the ones the filesystem no longer has, and they are deleted in one
// transaction.
//
// The whole subtlety is in *when not to sweep*. A generation sweep is a
// `DELETE FROM series WHERE root_name = ? AND scan_gen < ?`, so a run that did
// not actually enumerate a root would erase that root's entire library. Three
// cases must therefore never sweep, and each is a distinct way of not having
// enumerated the root:
//
//   - the root is unreachable (an unmounted drive, a renamed path) or its top
//     level could not be read — `roots.last_scan_error` is set instead;
//   - the root is disabled (FR-CFG-002), so it was not visited at all and its
//     rows must survive to keep the user's progress joinable;
//   - the run was cancelled or targeted at named series, so absence of a row is
//     not evidence of absence on disk.

// sweepDecision explains, in one value, whether a root's stale rows may be
// deleted — and why not, when they may not.
type sweepDecision struct {
	allowed bool
	reason  string
}

func sweepAllowed() sweepDecision              { return sweepDecision{allowed: true} }
func sweepBlocked(reason string) sweepDecision { return sweepDecision{reason: reason} }

// decideSweep is the single place the rule of arch §4.9 is expressed.
func decideSweep(rootErr error, cancelled, targeted bool) sweepDecision {
	switch {
	case rootErr != nil:
		return sweepBlocked("the root could not be enumerated")
	case cancelled:
		return sweepBlocked("the scan was cancelled")
	case targeted:
		return sweepBlocked("the run only covered named series")
	}
	return sweepAllowed()
}

// sweepRoot deletes everything left behind at an older generation for one root.
//
// It runs on a Writer of its own, after the run's Writer has been closed: it is
// the destructive step, `index.Writer.SweepRoot` flushes and then uses its own
// transaction, and mixing it into the batch that produced the rows would make a
// half-applied sweep representable.
func (s *Scanner) sweepRoot(ctx context.Context, rootName string, gen int64, d sweepDecision) (index.SweepResult, []index.Relocation, error) {
	if !d.allowed {
		s.log.Info("skipping the generation sweep", "root", rootName, "reason", d.reason)
		return index.SweepResult{}, nil, nil
	}
	w := s.index.Writer(index.WriterOptions{})
	defer func() { _ = w.Close() }()

	res, moved, err := w.SweepRoot(ctx, rootName, gen)
	if err != nil {
		return index.SweepResult{}, nil, fmt.Errorf("sweeping root %q: %w", rootName, err)
	}
	if res.Series > 0 || res.Books > 0 || res.Pages > 0 {
		s.log.Info("swept rows the filesystem no longer has",
			"root", rootName, "scan_gen", gen,
			"series", res.Series, "books", res.Books, "pages", res.Pages)
	}
	if len(moved) > 0 {
		s.log.Info("books moved rather than vanished; the sweep paired them by content",
			"root", rootName, "scan_gen", gen, "relocations", len(moved))
	}
	return res, moved, nil
}

// genStamps accumulates the ids of rows that were confirmed unchanged, so they
// can be moved forward to the current generation in bulk.
//
// This is the other half of FR-IDX-003: an unchanged archive is never re-read,
// but it must not look stale to the sweep that follows. Batching matters for
// more than tidiness — index.Writer issues each statement through the
// transaction rather than a cached prepared statement, so an `UPDATE … WHERE id
// IN (…)` per series costs one SQL parse per series, while one per 400 ids costs
// one parse per 400 rows. On a no-change rescan that is the difference between
// a marginal saving and an order of magnitude.
//
// Losing accumulated stamps to a mid-root failure is safe by construction: the
// failure sets RootResult.Err, and decideSweep refuses to sweep a root that
// could not be enumerated cleanly.
type genStamps struct {
	series []string
	books  []string
}

// stampChunk is when the accumulator is flushed. It matches index.Writer's own
// id-chunk size, so a flush is one statement.
const stampChunk = 400

func (g *genStamps) pending() int { return len(g.series) + len(g.books) }

func (g *genStamps) flushIfFull(ctx context.Context, w *index.Writer, gen int64) error {
	if g.pending() < stampChunk {
		return nil
	}
	return g.flush(ctx, w, gen)
}

// flush moves everything accumulated so far to gen. The call rides inside the
// writer's open batch transaction, so the stamping is atomic with respect to a
// concurrent reader in exactly the way a single statement would be.
func (g *genStamps) flush(ctx context.Context, w *index.Writer, gen int64) error {
	if g.pending() == 0 {
		return nil
	}
	series, books := g.series, g.books
	g.series, g.books = g.series[:0], g.books[:0]
	if err := w.StampGen(ctx, gen, series, books); err != nil {
		return fmt.Errorf("stamping %d unchanged series and %d unchanged books: %w",
			len(series), len(books), err)
	}
	return nil
}

// sameSeriesRow reports whether a scan would rewrite a series row with exactly
// what it already holds.
//
// scan_gen and added_at are excluded deliberately: the generation is what the
// stamp is *for*, and added_at is never moved forward anyway (index.Writer keeps
// the minimum, because "최근 추가" means the first sighting).
func sameSeriesRow(prior, next index.Series) bool {
	return prior.ID == next.ID &&
		prior.ID != "" &&
		prior.RootName == next.RootName &&
		prior.RelPath == next.RelPath &&
		prior.DisplayName == next.DisplayName &&
		bytes.Equal(prior.SortKey, next.SortKey) &&
		prior.SearchKey == next.SearchKey &&
		prior.ChoseongKey == next.ChoseongKey &&
		prior.Kind == next.Kind &&
		prior.BookCount == next.BookCount &&
		prior.PageCount == next.PageCount &&
		prior.TotalBytes == next.TotalBytes &&
		prior.Mtime == next.Mtime &&
		prior.CoverKind == next.CoverKind &&
		prior.CoverBookID == next.CoverBookID &&
		prior.CoverPageNo == next.CoverPageNo &&
		prior.CoverRelPath == next.CoverRelPath &&
		prior.Status == next.Status &&
		prior.Error == next.Error
}

// logAttrs is the standard attribute set of impl-plan §5.1 for a scan event.
func logAttrs(runID, root, relPath string) []any {
	attrs := []any{slog.String("run_id", runID)}
	if root != "" {
		attrs = append(attrs, slog.String("root", root))
	}
	if relPath != "" {
		attrs = append(attrs, slog.String("rel_path", relPath))
	}
	return attrs
}
