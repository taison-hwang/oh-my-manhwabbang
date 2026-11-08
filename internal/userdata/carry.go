package userdata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// CoverMove retires the cover row filed under OldBookID and writes Row in its
// place (ruling E-69 §2.11(a)(b)). internal/repair computes Row — the new id,
// the destination's identity columns and, for a split, the mapped page and the
// volume's length; this file only writes it.
type CoverMove struct {
	OldBookID string
	Row       Cover
}

// PrefsMove carries the book_prefs row filed under OldBookID to every id in To
// (ruling E-69 §2.12). A relocation retires the old row; a split copies and
// keeps it (OnlyIfAbsent).
//
// The planner does not read book_prefs: it plans one move for every proven
// pairing, and a move whose old id has no row is a no-op here.
type PrefsMove struct {
	OldBookID string
	To        []string
	// OnlyIfAbsent is the split rule: a destination that already has a row
	// keeps it, whatever its age, because prefs have no baseline of their own
	// and a volume's own choice is the reader's statement about that volume.
	// The source row is KEPT — §2.12 "복사한다. 원본은 남긴다": prefs cannot
	// prove the destination on their own, and the split reattach also runs at
	// start-up, where nothing is deleted (E-50 §2).
	//
	// False is the relocation rule: the newer updated_at wins and equal keeps
	// the destination, as for progress, and the old row is retired with the
	// progress (§2.12: "옛 행은 progress 이관처럼 은퇴한다").
	OnlyIfAbsent bool
}

// SeriesPrefsMove carries the series_prefs row of OldSeriesID to NewSeriesID
// under the destination's root and path (ruling E-69 §2.11(e)). The planner
// emits one only for a series-level 1:1 move.
type SeriesPrefsMove struct {
	OldSeriesID string
	NewSeriesID string
	RootName    string
	SeriesPath  string
}

// Carry is everything one step of the post-scan cleanup moves.
type Carry struct {
	Progress []SplitMove
	Covers   []CoverMove
	Prefs    []PrefsMove
	Series   []SeriesPrefsMove
}

func (c Carry) empty() bool {
	return len(c.Progress) == 0 && len(c.Covers) == 0 && len(c.Prefs) == 0 && len(c.Series) == 0
}

// CarryResult counts what ApplyCarry did, per table. Progress counts exactly
// as RepairSplit always has (one Retired per move); the others count rows that
// existed, because a prefs or cover move is planned without knowing whether
// there is anything to carry.
type CarryResult struct {
	Progress RepairResult
	Covers   RepairResult
	Prefs    RepairResult
	Series   RepairResult
}

// ApplyCarry applies a whole carry in one transaction.
//
// One transaction, for the reason RepairSplit gives and with more force: the
// four tables describe one book (or one series), and a carry that moved the
// progress but failed on the prefs would leave the prefs on an id the next
// purge deletes — the defect E-69 §2.12 exists to fix, reintroduced by a
// partial failure. Either the whole carry lands or none of it does.
//
// Every destination is newer-wins (equal keeps the destination), except a
// PrefsMove with OnlyIfAbsent. The old row is retired either way — it names an
// id the index does not have, and keeping it because its destination was newer
// would leave an orphan for the purge to find — except, again, a PrefsMove
// with OnlyIfAbsent, which is a copy and keeps its source.
func (db *DB) ApplyCarry(ctx context.Context, c Carry) (CarryResult, error) {
	if c.empty() {
		return CarryResult{}, nil
	}
	var res CarryResult
	err := db.writeTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res = CarryResult{}
		var err error
		if res.Progress, err = carryProgress(ctx, tx, c.Progress); err != nil {
			return err
		}
		if res.Covers, err = carryCovers(ctx, tx, c.Covers); err != nil {
			return err
		}
		if res.Prefs, err = carryPrefs(ctx, tx, c.Prefs); err != nil {
			return err
		}
		if res.Series, err = carrySeriesPrefs(ctx, tx, c.Series); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return CarryResult{}, err
	}
	return res, nil
}

// localIsAtLeastAsNew answers the collision rule for one destination: true when
// the destination row exists and is not older than the carried one.
func localIsAtLeastAsNew(ctx context.Context, tx *sql.Tx, query string, args []any, carried int64) (exists, keep bool, err error) {
	var local int64
	switch err := tx.QueryRowContext(ctx, query, args...).Scan(&local); {
	case errors.Is(err, sql.ErrNoRows):
		return false, false, nil
	case err != nil:
		return false, false, err
	}
	return true, local >= carried, nil
}

func carryCovers(ctx context.Context, tx *sql.Tx, moves []CoverMove) (RepairResult, error) {
	var res RepairResult
	for _, m := range moves {
		if m.OldBookID == "" || m.Row.BookID == "" {
			return RepairResult{}, fmt.Errorf("userdata: cover move with an empty book id: %w", ErrInvalidArgument)
		}
		if m.OldBookID == m.Row.BookID {
			return RepairResult{}, fmt.Errorf("userdata: cover move onto itself (%q): %w", m.OldBookID, ErrInvalidArgument)
		}
		_, keep, err := localIsAtLeastAsNew(ctx, tx,
			`SELECT updated_at FROM book_covers WHERE book_id = ?`, []any{m.Row.BookID}, m.Row.UpdatedAt)
		if err != nil {
			return RepairResult{}, fmt.Errorf("reading local cover for book %q: %w", m.Row.BookID, err)
		}
		if keep {
			res.Kept++
		} else {
			if err := upsertCover(ctx, tx, m.Row); err != nil {
				return RepairResult{}, err
			}
			res.Written++
		}
		r, err := tx.ExecContext(ctx, `DELETE FROM book_covers WHERE book_id = ?`, m.OldBookID)
		if err != nil {
			return RepairResult{}, fmt.Errorf("retiring the cover of %q: %w", m.OldBookID, err)
		}
		if n, _ := r.RowsAffected(); n > 0 {
			res.Retired += int(n)
		}
	}
	return res, nil
}

func carryPrefs(ctx context.Context, tx *sql.Tx, moves []PrefsMove) (RepairResult, error) {
	var res RepairResult
	sc := scopeOf(ctx)
	for _, m := range moves {
		if m.OldBookID == "" {
			return RepairResult{}, fmt.Errorf("userdata: prefs move with an empty book id: %w", ErrInvalidArgument)
		}
		for _, to := range m.To {
			if to == "" || to == m.OldBookID {
				return RepairResult{}, fmt.Errorf("userdata: prefs move %q -> %q: %w", m.OldBookID, to, ErrInvalidArgument)
			}
		}
		var p Prefs
		err := tx.QueryRowContext(ctx,
			`SELECT reading_dir, display_mode, fit_mode, updated_at FROM `+sc.prefs()+sc.where(`book_id = ?`),
			sc.args(m.OldBookID)...).Scan(&p.ReadingDir, &p.DisplayMode, &p.FitMode, &p.UpdatedAt)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			continue // nothing to carry
		case err != nil:
			return RepairResult{}, fmt.Errorf("reading prefs for book %q: %w", m.OldBookID, err)
		}
		for _, to := range m.To {
			exists, keep, err := localIsAtLeastAsNew(ctx, tx,
				`SELECT updated_at FROM `+sc.prefs()+sc.where(`book_id = ?`), sc.args(to), p.UpdatedAt)
			if err != nil {
				return RepairResult{}, fmt.Errorf("reading local prefs for book %q: %w", to, err)
			}
			if (m.OnlyIfAbsent && exists) || (!m.OnlyIfAbsent && keep) {
				res.Kept++
				continue
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO `+sc.prefs()+` (`+sc.insCols()+`book_id, reading_dir, display_mode, fit_mode, updated_at)
				VALUES (`+sc.insVals()+`?,?,?,?,?)
				ON CONFLICT(`+sc.conflict()+`book_id) DO UPDATE SET
					reading_dir  = excluded.reading_dir,
					display_mode = excluded.display_mode,
					fit_mode     = excluded.fit_mode,
					updated_at   = excluded.updated_at`,
				sc.args(to, p.ReadingDir, p.DisplayMode, p.FitMode, p.UpdatedAt)...); err != nil {
				return RepairResult{}, fmt.Errorf("carrying prefs to book %q: %w", to, err)
			}
			res.Written++
		}
		if m.OnlyIfAbsent {
			continue // a split copies; the source row stays (see PrefsMove)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+sc.prefs()+sc.where(`book_id = ?`), sc.args(m.OldBookID)...); err != nil {
			return RepairResult{}, fmt.Errorf("retiring the prefs of %q: %w", m.OldBookID, err)
		}
		res.Retired++
	}
	return res, nil
}

func carrySeriesPrefs(ctx context.Context, tx *sql.Tx, moves []SeriesPrefsMove) (RepairResult, error) {
	var res RepairResult
	for _, m := range moves {
		if m.OldSeriesID == "" || m.NewSeriesID == "" || m.OldSeriesID == m.NewSeriesID {
			return RepairResult{}, fmt.Errorf("userdata: series prefs move %q -> %q: %w",
				m.OldSeriesID, m.NewSeriesID, ErrInvalidArgument)
		}
		old, err := scanSeriesPrefs(tx.QueryRowContext(ctx,
			`SELECT `+seriesPrefsColumns+` FROM series_prefs WHERE series_id = ?`, m.OldSeriesID))
		switch {
		case errors.Is(err, sql.ErrNoRows):
			continue
		case err != nil:
			return RepairResult{}, fmt.Errorf("reading prefs for series %q: %w", m.OldSeriesID, err)
		}
		_, keep, err := localIsAtLeastAsNew(ctx, tx,
			`SELECT updated_at FROM series_prefs WHERE series_id = ?`, []any{m.NewSeriesID}, old.UpdatedAt)
		if err != nil {
			return RepairResult{}, fmt.Errorf("reading local prefs for series %q: %w", m.NewSeriesID, err)
		}
		if keep {
			res.Kept++
		} else {
			if err := upsertSeriesPrefs(ctx, tx, SeriesPrefs{
				SeriesID: m.NewSeriesID, RootName: m.RootName, SeriesPath: m.SeriesPath,
				ReadingDir: old.ReadingDir, UpdatedAt: old.UpdatedAt,
			}); err != nil {
				return RepairResult{}, err
			}
			res.Written++
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM series_prefs WHERE series_id = ?`, m.OldSeriesID); err != nil {
			return RepairResult{}, fmt.Errorf("retiring the prefs of series %q: %w", m.OldSeriesID, err)
		}
		res.Retired++
	}
	return res, nil
}

// PurgeSet is what the post-scan purge deletes (ruling E-69 §2.11(d), E-50).
// Books are removed from progress, book_prefs and book_covers alike; the
// caller collects them from each of the three tables, because a book can have
// a row in any one of them without the others. Series are removed from
// series_prefs.
type PurgeSet struct {
	Books  []string
	Series []string

	// KeepShared leaves the shared tables (book_covers, series_prefs) alone and
	// deletes only the calling scope's own progress and preferences. A per-user
	// server purges every user's rows first with this set and the shared rows
	// last, so a shared cover keeps proving the deletion to every scope until
	// the last one is done (ruling E-74 §2.13).
	KeepShared bool
}

// PurgeResult counts the rows Purge deleted, per table.
type PurgeResult struct {
	Progress int
	Prefs    int
	Covers   int
	Series   int
}

// Purge deletes everything filed under the given ids in one transaction. It
// is PurgeProgress widened to the E-69 tables, and the same two things about
// it are deliberate: it takes explicit ids, never a predicate, and it counts
// what it deleted so the caller can say so.
func (db *DB) Purge(ctx context.Context, set PurgeSet) (PurgeResult, error) {
	if len(set.Books) == 0 && len(set.Series) == 0 {
		return PurgeResult{}, nil
	}
	for _, id := range set.Books {
		if id == "" {
			return PurgeResult{}, fmt.Errorf("userdata: purge with an empty book id: %w", ErrInvalidArgument)
		}
	}
	for _, id := range set.Series {
		if id == "" {
			return PurgeResult{}, fmt.Errorf("userdata: purge with an empty series id: %w", ErrInvalidArgument)
		}
	}
	var res PurgeResult
	sc := scopeOf(ctx)
	err := db.writeTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res = PurgeResult{}
		del := func(query, what, id string, args []any, into *int) error {
			r, err := tx.ExecContext(ctx, query, args...)
			if err != nil {
				return fmt.Errorf("purging %s for %q: %w", what, id, err)
			}
			if n, _ := r.RowsAffected(); n > 0 {
				*into += int(n)
			}
			return nil
		}
		for _, id := range set.Books {
			if err := del(`DELETE FROM `+sc.progress()+sc.where(`book_id = ?`), "progress", id, sc.args(id), &res.Progress); err != nil {
				return err
			}
			if err := del(`DELETE FROM `+sc.prefs()+sc.where(`book_id = ?`), "preferences", id, sc.args(id), &res.Prefs); err != nil {
				return err
			}
			if set.KeepShared {
				continue
			}
			if err := del(`DELETE FROM book_covers WHERE book_id = ?`, "the cover", id, []any{id}, &res.Covers); err != nil {
				return err
			}
		}
		for _, id := range set.Series {
			if set.KeepShared {
				break
			}
			if err := del(`DELETE FROM series_prefs WHERE series_id = ?`, "series preferences", id, []any{id}, &res.Series); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return PurgeResult{}, err
	}
	return res, nil
}

// RefileCovers is RefileProgress for book_covers (ruling E-69 §2.11(c)): it
// writes series_id and nothing else, and updated_at does not move.
func (db *DB) RefileCovers(ctx context.Context, rows []Refile) (int, error) {
	return db.refile(ctx, `UPDATE book_covers SET series_id = ? WHERE book_id = ? AND series_id <> ?`,
		"cover", rows)
}
