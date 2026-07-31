package userdata

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
)

// SeriesSeen is one row of `series_seen`: the first moment SHELF ever saw a
// series (amendment A-8, ruling E-9).
//
// RootName and SeriesPath are part of the write-once row for the same reason
// progress carries them: they let `shelf migrate-root` re-derive the id after a
// root is renamed, without the index. They are never rewritten — if the series
// moves, its path changes, so its id changes, and this row is simply not the one
// that matches any more (arch §3.6 rule 5).
type SeriesSeen struct {
	SeriesID   string
	RootName   string
	SeriesPath string
	// FirstSeenAt is unix seconds, and is the scan run's start time rather than
	// a per-row clock read: one run produces one identical stamp for every
	// series it discovers, so a 32 s scan cannot straddle the recently-added
	// window boundary and split a batch (arch §3.6 rule 3).
	FirstSeenAt int64
}

// insertSeriesSeen is the only statement in SHELF that may write first_seen_at
// (arch §3.6 rule 1). `DO NOTHING` is the whole amendment in one clause: the
// first sighting wins for ever, so a rescan, a restored backup, a remounted
// drive and a full --rebuild-index are all invisible here, and a series that
// vanishes and returns does not light 최근 추가 up again.
//
// There is no companion statement that rewrites or removes a row, in this file
// or anywhere else, and `make lint` greps for one.
const insertSeriesSeen = `
	INSERT INTO series_seen (series_id, root_name, series_path, first_seen_at)
	VALUES (?, ?, ?, ?) ON CONFLICT(series_id) DO NOTHING`

// MarkSeriesSeen records the first sighting of each series. It is idempotent
// and set-once: a row that already exists is left exactly as it was, whatever
// timestamp the caller offers.
//
// The whole batch is one transaction on the dedicated writer connection, which
// is what makes it safe for the scanner to call while the index writer holds
// index.db's write lock: no transaction spans both databases (arch §3.7).
//
// Concurrency needs no coordination beyond the primary key. Two callers racing
// on the same series_id produce one row — one insert applies, the other's
// ON CONFLICT fires — and both return nil, because "somebody else recorded the
// first sighting first" is the expected outcome, not a conflict to report.
func (db *DB) MarkSeriesSeen(ctx context.Context, rows []SeriesSeen) error {
	if len(rows) == 0 {
		return nil
	}
	for _, r := range rows {
		if r.SeriesID == "" {
			return fmt.Errorf("userdata: empty series id: %w", ErrInvalidArgument)
		}
		if r.FirstSeenAt <= 0 {
			return fmt.Errorf("userdata: first_seen_at %d for series %q is not a unix timestamp: %w",
				r.FirstSeenAt, r.SeriesID, ErrInvalidArgument)
		}
	}
	return db.writeTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		// One prepared statement for the batch: a scan hands this a whole
		// root's series, and re-parsing the same insert a thousand times is
		// most of what the incremental path (NFR-PRF-004) cannot afford.
		st, err := tx.PrepareContext(ctx, insertSeriesSeen)
		if err != nil {
			return fmt.Errorf("preparing the first-sighting insert: %w", err)
		}
		defer func() { _ = st.Close() }()

		for _, r := range rows {
			if _, err := st.ExecContext(ctx, r.SeriesID, r.RootName, r.SeriesPath, r.FirstSeenAt); err != nil {
				return fmt.Errorf("recording the first sighting of series %q: %w", r.SeriesID, err)
			}
		}
		return nil
	})
}

// SeriesFirstSeen returns first_seen_at for each of the given series, keyed by
// series id. A series with no row is simply absent from the map — that is not
// an error, it is "not recently added" (arch §3.6 rule 7).
//
// An empty seriesIDs slice means "every row", which is what the count and the
// listing paths use. A non-empty one is chunked, so a caller may pass ten
// thousand ids without meeting SQLite's bound-variable ceiling.
func (db *DB) SeriesFirstSeen(ctx context.Context, seriesIDs []string) (map[string]int64, error) {
	out := make(map[string]int64, len(seriesIDs))
	scan := func(rows *sql.Rows, err error) error {
		if err != nil {
			return fmt.Errorf("reading first-sighting rows: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var id string
			var at int64
			if err := rows.Scan(&id, &at); err != nil {
				return fmt.Errorf("scanning a first-sighting row: %w", err)
			}
			out[id] = at
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("reading first-sighting rows: %w", err)
		}
		return nil
	}

	if len(seriesIDs) == 0 {
		if err := scan(db.sqldb.QueryContext(ctx,
			`SELECT series_id, first_seen_at FROM series_seen`)); err != nil {
			return nil, err
		}
		return out, nil
	}
	err := forEachIDChunk(seriesIDs, func(args []any, ph string) error {
		return scan(db.sqldb.QueryContext(ctx,
			`SELECT series_id, first_seen_at FROM series_seen WHERE series_id IN (`+ph+`)`, args...))
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// CountSeriesSeenSince reports how many series were first seen at or after
// `since` — the half-open window `[since, ∞)` of arch §7.5.
//
// This is the user.db-only view, the counterpart of CountProgress: it answers
// "how many first sightings does this file hold", for diagnostics and for tests
// that pin the window arithmetic. It is deliberately NOT the sidebar's 최근 추가
// badge. That number is `total` from GET /api/series?scope=added&limit=1 — the
// same WHERE clause as the list itself, AND-ed with root, q, status and progress
// and restricted to enabled roots, none of which this file knows about. A second
// implementation of the badge would be a second thing to get wrong (arch §7.5).
func (db *DB) CountSeriesSeenSince(ctx context.Context, since int64) (int64, error) {
	var n int64
	err := db.sqldb.QueryRowContext(ctx,
		`SELECT count(*) FROM series_seen WHERE first_seen_at >= ?`, since).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("counting first-sighting rows since %d: %w", since, err)
	}
	return n, nil
}

// FirstSeenBootstrapNeeded reports whether the next scan is the bootstrap run of
// arch §3.6 rule 6: a run against a user.db that has never been stamped
// `meta.first_seen_bootstrap`.
//
// Without it, the first scan of a pre-existing collection would stamp 963 series
// as "added today" and put 963 in a badge that means "new" — the same class of
// visibly wrong number ruling E-9 was raised about. A bootstrap run instead uses
// min(run start, series mtime), so a 2012 series starts outside the window and
// one copied in yesterday starts inside it.
//
// **The marker alone decides it** (ruling E-16). It used to also require
// `series_seen` to be empty, and that second condition was not merely redundant
// but actively harmful: the scanner already withholds the marker from a run that
// was cancelled, restricted to some roots, or lost a root's batch, precisely so
// the next run finishes the job — but a run that had committed even one root's
// rows before it stopped left the table non-empty, so the recovering run was not
// treated as a bootstrap and stamped the *rest* of a decade-old collection with
// its own wall clock. On a first scan of 414 GB, being interrupted once is the
// normal path, not the edge case.
//
// The marker is therefore the whole signal, and it is only ever written by a
// bootstrap run that completed (internal/scanner's finishSeen). A user.db
// restored from a backup that predates A-8 has no marker and correctly
// bootstraps; one whose bootstrap finished has the marker and correctly does
// not, however many rows have since come and gone.
func (db *DB) FirstSeenBootstrapNeeded(ctx context.Context) (bool, error) {
	_, ok, err := db.Meta(ctx, metaFirstSeenBootstrap)
	if err != nil {
		return false, fmt.Errorf("reading the first-sighting bootstrap marker: %w", err)
	}
	return !ok, nil
}

// CompleteFirstSeenBootstrap stamps meta.first_seen_bootstrap with the run's
// start time, so every later run uses that run's start unconditionally. Like the
// rows themselves it is written once: a second call leaves the first value
// alone, which keeps the marker a record of when bootstrapping happened rather
// than of the last scan.
func (db *DB) CompleteFirstSeenBootstrap(ctx context.Context, at int64) error {
	if at <= 0 {
		return fmt.Errorf("userdata: bootstrap timestamp %d is not a unix timestamp: %w",
			at, ErrInvalidArgument)
	}
	return db.writeTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		return setMetaIfAbsent(ctx, tx, metaFirstSeenBootstrap, strconv.FormatInt(at, 10))
	})
}
