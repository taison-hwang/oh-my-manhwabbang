package userdata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// SplitMove retires one progress row that named a container and writes the rows
// that name the volumes the container became. internal/repair computes it; this
// file is only the write.
//
// Rows is never empty for a move the planner produced: an unstarted volume gets
// no row, but the volume the reader stopped in always does, and a completed
// container yields one row per volume.
type SplitMove struct {
	OldBookID string
	Rows      []ExportItem
}

// RepairResult counts what RepairSplit did. Kept is not a failure: it means the
// destination volume already had a newer row, which is the correct outcome when
// the reader opened the split volume before the repair ran.
type RepairResult struct {
	// Retired is the number of orphaned rows removed.
	Retired int
	// Written is the number of destination rows inserted or updated.
	Written int
	// Kept is the number of destination rows left alone because the local row
	// was at least as new.
	Kept int
}

// RepairSplit applies the moves in one transaction.
//
// One transaction for the whole repair, for the same reason Import uses one: a
// half-moved reading history is worse than an unmoved one. Two rules inside it
// are load-bearing:
//
//   - a destination that already has a row at least as new is left alone. The
//     orphan is the *older* record by construction — it was written before the
//     split — so overwriting would walk the reader backwards to where they were
//     under the old shape. This is StrategyMerge's rule, deliberately, because
//     it is the same question.
//
//     It applies to the read-through rows too, and that is the interesting
//     case: on the live library `고우영 삼국지` carried an orphan at absolute
//     page 239, which infers volume 1 finished, but the reader had since
//     reopened volume 1 at page 5. The inference loses to the record they
//     made — the series reads 4.4 % rather than 15 %, because 4.4 % is where
//     they actually are in the shape the library now has. An explicit newer
//     position is never overwritten by an older one derived from arithmetic.
//
//   - the orphan is retired either way. It names an id the index does not have,
//     so it can only ever contribute `MIN(last_page, 0)` to the rollup and hide
//     the series it belongs to. Keeping it because its destination was skipped
//     would leave the 0 % in place, which is the whole defect.
//
// `started_at` is preserved by the ON CONFLICT clause (`min` of the two), so a
// volume the reader had already opened keeps the earlier of the two beginnings.
func (db *DB) RepairSplit(ctx context.Context, moves []SplitMove) (RepairResult, error) {
	if len(moves) == 0 {
		return RepairResult{}, nil
	}
	var res RepairResult
	err := db.writeTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res = RepairResult{}

		lookup, err := tx.PrepareContext(ctx, `SELECT updated_at FROM progress WHERE book_id = ?`)
		if err != nil {
			return fmt.Errorf("preparing progress lookup: %w", err)
		}
		defer lookup.Close()

		write, err := tx.PrepareContext(ctx, `
			INSERT INTO progress (book_id, series_id, root_name, book_path, last_page,
			                      page_count, completed, started_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?)
			ON CONFLICT(book_id) DO UPDATE SET
				series_id  = excluded.series_id,
				root_name  = excluded.root_name,
				book_path  = excluded.book_path,
				last_page  = excluded.last_page,
				page_count = excluded.page_count,
				completed  = excluded.completed,
				started_at = min(progress.started_at, excluded.started_at),
				updated_at = excluded.updated_at`)
		if err != nil {
			return fmt.Errorf("preparing repair write: %w", err)
		}
		defer write.Close()

		retire, err := tx.PrepareContext(ctx, `DELETE FROM progress WHERE book_id = ?`)
		if err != nil {
			return fmt.Errorf("preparing repair delete: %w", err)
		}
		defer retire.Close()

		for _, m := range moves {
			if m.OldBookID == "" {
				return fmt.Errorf("userdata: repair move with empty book id: %w", ErrInvalidArgument)
			}
			for _, it := range m.Rows {
				if it.BookID == "" {
					return fmt.Errorf("userdata: repair row with empty book id: %w", ErrInvalidArgument)
				}
				if it.PageCount < 0 {
					return fmt.Errorf("userdata: repair row %q: page count %d is negative: %w",
						it.BookID, it.PageCount, ErrInvalidArgument)
				}
				var localUpdated int64
				switch err := lookup.QueryRowContext(ctx, it.BookID).Scan(&localUpdated); {
				case err == nil:
					if localUpdated >= it.UpdatedAt {
						res.Kept++
						continue
					}
				case errors.Is(err, sql.ErrNoRows):
				default:
					return fmt.Errorf("reading local progress for book %q: %w", it.BookID, err)
				}
				completed := 0
				if it.Completed {
					completed = 1
				}
				startedAt := it.StartedAt
				if startedAt == 0 {
					startedAt = it.UpdatedAt
				}
				if _, err := write.ExecContext(ctx, it.BookID, it.SeriesID, it.RootName,
					it.BookPath, clampPage(it.LastPage, it.PageCount), it.PageCount,
					completed, startedAt, it.UpdatedAt); err != nil {
					return fmt.Errorf("repairing progress for book %q: %w", it.BookID, err)
				}
				res.Written++
			}
			if _, err := retire.ExecContext(ctx, m.OldBookID); err != nil {
				return fmt.Errorf("retiring orphaned progress %q: %w", m.OldBookID, err)
			}
			res.Retired++
		}
		return nil
	})
	if err != nil {
		return RepairResult{}, err
	}
	return res, nil
}

// Refile corrects one progress row's series without touching anything else
// about it.
type Refile struct {
	BookID   string
	SeriesID string
}

// RefileProgress moves rows onto the series the index says their book is in.
//
// One transaction, and deliberately narrow: it writes `series_id` and nothing
// else. The reading position, the baseline, the completion flag and both
// timestamps are correct already — the row was filed under the wrong series, not
// mis-read — and `updated_at` in particular must not move, because 이어보기
// sorts by it and a correction is not something the reader did.
func (db *DB) RefileProgress(ctx context.Context, rows []Refile) (int, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	n := 0
	err := db.writeTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		n = 0
		stmt, err := tx.PrepareContext(ctx,
			`UPDATE progress SET series_id = ? WHERE book_id = ? AND series_id <> ?`)
		if err != nil {
			return fmt.Errorf("preparing the refile: %w", err)
		}
		defer stmt.Close()
		for _, r := range rows {
			if r.BookID == "" || r.SeriesID == "" {
				return fmt.Errorf("userdata: refile with an empty id: %w", ErrInvalidArgument)
			}
			res, err := stmt.ExecContext(ctx, r.SeriesID, r.BookID, r.SeriesID)
			if err != nil {
				return fmt.Errorf("refiling progress for book %q: %w", r.BookID, err)
			}
			if c, _ := res.RowsAffected(); c > 0 {
				n += int(c)
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}

// PurgeProgress deletes reading history for books that are gone, along with the
// per-book preferences filed under the same ids.
//
// This is the one operation in this package that destroys authored data, so two
// things about it are deliberate. It takes explicit ids rather than a predicate:
// deciding *which* rows are gone is the caller's job and needs evidence this
// package does not have (see index.VanishedProgress). And it removes prefs in
// the same transaction, because "related information" means all of it — a
// reading direction filed under a book nobody can open is the same orphan by
// another name.
//
// It returns what it deleted so the caller can say so out loud. A purge that
// reports only a count is indistinguishable from a purge that took the wrong
// rows.
func (db *DB) PurgeProgress(ctx context.Context, bookIDs []string) (progress, prefs int, err error) {
	if len(bookIDs) == 0 {
		return 0, 0, nil
	}
	err = db.writeTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		progress, prefs = 0, 0
		delProgress, err := tx.PrepareContext(ctx, `DELETE FROM progress WHERE book_id = ?`)
		if err != nil {
			return fmt.Errorf("preparing the progress purge: %w", err)
		}
		defer delProgress.Close()
		delPrefs, err := tx.PrepareContext(ctx, `DELETE FROM book_prefs WHERE book_id = ?`)
		if err != nil {
			return fmt.Errorf("preparing the prefs purge: %w", err)
		}
		defer delPrefs.Close()

		for _, id := range bookIDs {
			if id == "" {
				return fmt.Errorf("userdata: purge with an empty book id: %w", ErrInvalidArgument)
			}
			r, err := delProgress.ExecContext(ctx, id)
			if err != nil {
				return fmt.Errorf("purging progress for book %q: %w", id, err)
			}
			if n, _ := r.RowsAffected(); n > 0 {
				progress += int(n)
			}
			r, err = delPrefs.ExecContext(ctx, id)
			if err != nil {
				return fmt.Errorf("purging preferences for book %q: %w", id, err)
			}
			if n, _ := r.RowsAffected(); n > 0 {
				prefs += int(n)
			}
		}
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	return progress, prefs, nil
}
