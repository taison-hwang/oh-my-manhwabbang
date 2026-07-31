package scanner

import (
	"context"

	"shelf/internal/userdata"
)

// SeriesSeenWriter is the narrow view of user.db the scanner needs in order to
// record when each series was first seen (amendment A-8, ruling E-9).
// *userdata.DB satisfies it. Declared by the consumer, per impl-plan §5.1.
//
// This is the only user.db write the scanner is ever allowed to make. It is
// write-once and idempotent, and it runs in its own transaction on the userdata
// handle — never inside the index writer's, because no transaction may span
// both databases (arch §3.7). A crash between the two commits is harmless in
// either order: an index row without a first-sighting row is merely not
// "recently added" until the next run, and a first-sighting row without an
// index row is invisible to every query.
type SeriesSeenWriter interface {
	SeriesFirstSeen(ctx context.Context, seriesIDs []string) (map[string]int64, error)
	MarkSeriesSeen(ctx context.Context, rows []userdata.SeriesSeen) error
	FirstSeenBootstrapNeeded(ctx context.Context) (bool, error)
	CompleteFirstSeenBootstrap(ctx context.Context, at int64) error
}

// seenRun is the run-level first-sighting context, decided once at the start of
// a scan and shared, read-only, by every root's pass.
type seenRun struct {
	// w is nil when there is nothing to record against, which disables the
	// whole mechanism — the scanner's own tests, and any composition without a
	// user.db, run that way.
	w SeriesSeenWriter
	// at is the run's start time in unix seconds. Every series a run discovers
	// gets this same value rather than a per-series clock read, so a 32 s scan
	// cannot straddle the recently-added boundary and split its own batch
	// (arch §3.6 rule 3).
	at int64
	// bootstrap marks the first run against a user.db that has never recorded a
	// sighting (arch §3.6 rule 6).
	bootstrap bool
	// tally is the run-level record of what the roots actually managed to do,
	// shared by every root's batch. It is what finishSeen consults before it
	// closes bootstrapping. nil exactly when w is.
	//
	// It needs no lock: every write comes from seenBatch.flush, and both the
	// batch and the run are driven by the single goroutine that walks the roots
	// in order (Scanner.run → scanRoot → writeResults).
	tally *seenTally
}

// seenTally answers the only question finishSeen has: did this bootstrap run
// finish the job whose completion the marker asserts?
type seenTally struct {
	// recorded counts the sightings this run got into user.db.
	recorded int
	// failed marks a root whose batch never reached user.db, which leaves that
	// root's series unrecorded and therefore still to be bootstrapped.
	failed bool
}

// newBatch starts one root's collection of first sightings.
func (r seenRun) newBatch() *seenBatch {
	if r.w == nil {
		return nil
	}
	return &seenBatch{run: r}
}

// seenBatch accumulates one root's sightings and writes them once, at the end of
// that root's pass. It is touched only by the single writer goroutine, so it
// needs no lock; every method tolerates a nil receiver, which is how a scanner
// with no user.db carries no per-call branches.
type seenBatch struct {
	run  seenRun
	rows []userdata.SeriesSeen
}

// add notes that this run listed a series. Every series the run lists gets a
// row, `empty` and `error` ones included: they appear in the library (arch §4.2),
// so they can be recently added.
//
// seriesMtime is the newest mtime among the series' books and is used only by a
// bootstrap run, where it is the best evidence the filesystem offers about when
// the material actually arrived.
func (b *seenBatch) add(seriesID, rootName, relPath string, seriesMtime int64) {
	if b == nil {
		return
	}
	at := b.run.at
	if b.run.bootstrap && seriesMtime > 0 && seriesMtime < at {
		at = seriesMtime
	}
	b.rows = append(b.rows, userdata.SeriesSeen{
		SeriesID: seriesID, RootName: rootName, SeriesPath: relPath, FirstSeenAt: at,
	})
}

// flush records everything this root sighted, in one transaction.
//
// It reads first and writes only what is missing. The write itself is already
// set-once, so the read is not needed for correctness — it is needed for
// NFR-PRF-004: on the no-change rescan that FR-IDX-003 exists to make cheap,
// every series is already recorded, so this collapses to a handful of indexed
// SELECTs and no write transaction at all. That matters beyond the microseconds
// saved: an unconditional write would take user.db's WAL writer lock once per
// root per scan, in a process that is also serving `PUT .../progress`.
func (b *seenBatch) flush(ctx context.Context) error {
	if b == nil || len(b.rows) == 0 {
		return nil
	}
	rows := b.rows
	b.rows = nil

	ids := make([]string, 0, len(rows))
	for i := range rows {
		ids = append(ids, rows[i].SeriesID)
	}
	known, err := b.run.w.SeriesFirstSeen(ctx, ids)
	if err != nil {
		b.run.tally.failed = true
		return err
	}
	pending := make([]userdata.SeriesSeen, 0, len(rows)-len(known))
	for _, r := range rows {
		if _, ok := known[r.SeriesID]; ok {
			continue
		}
		pending = append(pending, r)
	}
	if len(pending) == 0 {
		// Every one of this root's series is already on record, which is a
		// completed root, not an idle one: it counts towards the run having
		// recorded something.
		b.run.tally.recorded += len(rows)
		return nil
	}
	if err := b.run.w.MarkSeriesSeen(ctx, pending); err != nil {
		b.run.tally.failed = true
		return err
	}
	b.run.tally.recorded += len(rows)
	return nil
}

// beginSeen decides, once per run, what every root's sightings will be stamped
// with. The bootstrap question has to be asked here rather than per root:
// the first root's own rows make the answer false for the second.
func (s *Scanner) beginSeen(ctx context.Context, at int64) seenRun {
	if s.seen == nil {
		return seenRun{}
	}
	bootstrap, err := s.seen.FirstSeenBootstrapNeeded(ctx)
	if err != nil {
		// Recording nothing is the safe failure: a series with no row is
		// excluded from 최근 추가 (arch §3.6 rule 7) and the next run records
		// it. Guessing "not a bootstrap" would instead stamp a whole
		// pre-existing collection as added today, which is the wrong number
		// ruling E-9 was raised about.
		s.log.Warn("skipping first-sighting records for this run", "err", err)
		return seenRun{}
	}
	return seenRun{w: s.seen, at: at, bootstrap: bootstrap, tally: &seenTally{}}
}

// finishSeen stamps the bootstrap marker, so every later run uses its own start
// time unconditionally. It runs after every root, because until then the run is
// still bootstrapping.
//
// Only a bootstrap run that actually did the job the marker asserts may stamp
// it. The marker is a claim — "this collection has been dated from its
// filesystem evidence, everything from here on is genuinely new" — and a run
// that was cancelled, that skipped a root, or that never wrote a row has not
// made that claim true. Stamping anyway is strictly worse than leaving it
// unset: rule 6's own precondition (`series_seen` empty *and* the marker unset)
// would still hold, so an unset marker lets the next run bootstrap, whereas a
// premature one condemns the next run to stamp a decade-old collection with its
// own wall clock and flood 최근 추가 — the exact wrong number ruling E-9 was
// raised about (arch §3.6 rule 6).
func (s *Scanner) finishSeen(ctx context.Context, sr seenRun, req Request, wanted int, res *Result) {
	if sr.w == nil || !sr.bootstrap {
		return
	}
	if why := bootstrapIncomplete(ctx, sr, req, wanted, res); why != "" {
		// Info, not Warn: an operator who cancels a scan has not made a mistake.
		// The consequence is only that the next run finishes the bootstrap.
		s.log.Info("leaving the first-sighting bootstrap open for the next run",
			"run_id", res.RunID, "reason", why)
		return
	}
	if err := sr.w.CompleteFirstSeenBootstrap(context.WithoutCancel(ctx), sr.at); err != nil {
		s.log.Warn("recording the first-sighting bootstrap marker", "err", err)
	}
}

// bootstrapIncomplete names the reason this bootstrap run may not close
// bootstrapping, or "" when it may. wanted is the number of roots the run set
// out to scan.
func bootstrapIncomplete(ctx context.Context, sr seenRun, req Request, wanted int, res *Result) string {
	switch {
	case ctx.Err() != nil:
		// A cancelled run committed what it had and stopped; the rest of the
		// library has never been dated.
		return "the run was cancelled"
	case len(req.Roots) > 0:
		return "the run covered only the roots it was given"
	case req.targeted():
		return "the run covered only the series it was given"
	case len(res.Roots) != wanted:
		return "the run did not reach every root"
	case sr.tally.failed:
		return "a root's first sightings were not written"
	case sr.tally.recorded == 0:
		return "the run recorded no first sightings"
	}
	for _, rr := range res.Roots {
		if rr.Err != nil {
			// An unreachable NAS is the common one: its series are still
			// undated, and next time they must be dated from their mtimes and
			// not from that run's clock.
			return "root " + rr.Name + " failed"
		}
	}
	return ""
}

// flushSeen writes one root's sightings.
//
// A failure here is logged, not returned: the index is complete and correct
// either way, and the only consequence is that the series are not in 최근 추가
// until the next scan (arch §3.6 rule 7). Failing the root over bookkeeping
// would turn a locked user.db into a scan that reports an error.
//
// flush records it on the run's tally all the same, so that a bootstrap run
// whose rows were lost to a locked user.db does not go on to declare
// bootstrapping finished.
func (s *Scanner) flushSeen(ctx context.Context, rt *rootRun) {
	if err := rt.seen.flush(context.WithoutCancel(ctx)); err != nil {
		s.log.Warn("recording first sightings", "run_id", rt.runID, "root", rt.cfg.Name, "err", err)
	}
}
