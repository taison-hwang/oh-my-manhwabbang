package index

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Root mirrors one row of the `roots` table: the config's roots[] entry as of
// the last scan, plus its rolled-up counts. `name` is the identity anchor —
// changing it in the YAML orphans that root's reading progress (arch §3.4).
type Root struct {
	Name        string
	Path        string
	Label       string
	Enabled     bool
	SeriesCount int64
	BookCount   int64
	PageCount   int64
	TotalBytes  int64
	// LastScanStart, LastScanEnd are Unix seconds; nil means "never".
	LastScanStart *int64
	LastScanEnd   *int64
	// LastScanError is the message that aborted the last run for this root, or
	// "" when the run succeeded.
	LastScanError string
}

const selectRootColumns = `
	SELECT name, path, COALESCE(label, ''), enabled, series_count, book_count,
	       page_count, total_bytes, last_scan_start, last_scan_end,
	       COALESCE(last_scan_error, '')
	FROM roots`

func scanRoot(rows interface{ Scan(...any) error }) (Root, error) {
	var r Root
	err := rows.Scan(&r.Name, &r.Path, &r.Label, &r.Enabled, &r.SeriesCount, &r.BookCount,
		&r.PageCount, &r.TotalBytes, &r.LastScanStart, &r.LastScanEnd, &r.LastScanError)
	return r, err
}

// ListRoots returns every known root, enabled or not, ordered by name. A
// disabled root is kept in the index and merely excluded from series listings
// (arch §3.2): disabling must never destroy the user's progress.
func (db *DB) ListRoots(ctx context.Context) ([]Root, error) {
	rows, err := db.sqldb.QueryContext(ctx, selectRootColumns+` ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("listing roots: %w", err)
	}
	defer rows.Close()

	var out []Root
	for rows.Next() {
		r, err := scanRoot(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning root: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing roots: %w", err)
	}
	return out, nil
}

// GetRoot returns one root by name, or ErrNotFound.
func (db *DB) GetRoot(ctx context.Context, name string) (Root, error) {
	row := db.sqldb.QueryRowContext(ctx, selectRootColumns+` WHERE name = ?`, name)
	r, err := scanRoot(row)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Root{}, fmt.Errorf("root %q: %w", name, ErrNotFound)
	case err != nil:
		return Root{}, fmt.Errorf("reading root %q: %w", name, err)
	}
	return r, nil
}

// UpsertRoot writes the identity and configuration of a root. Counts and scan
// timestamps are left alone — RecountRoot and MarkRootScan own those.
func (db *DB) UpsertRoot(ctx context.Context, r Root) error {
	return db.writeTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO roots (name, path, label, enabled)
			VALUES (?, ?, ?, ?)
			ON CONFLICT(name) DO UPDATE SET
				path = excluded.path,
				label = excluded.label,
				enabled = excluded.enabled`,
			r.Name, r.Path, nullString(r.Label), r.Enabled)
		if err != nil {
			return fmt.Errorf("upserting root %q: %w", r.Name, err)
		}
		return nil
	})
}

// DeleteRoot removes a root and everything derived from it. Page rows carry no
// foreign key, so they are removed first, by hand.
func (db *DB) DeleteRoot(ctx context.Context, name string) error {
	return db.writeTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM pages WHERE book_id IN (SELECT id FROM books WHERE root_name = ?)`, name); err != nil {
			return fmt.Errorf("deleting pages of root %q: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM roots WHERE name = ?`, name); err != nil {
			return fmt.Errorf("deleting root %q: %w", name, err)
		}
		return nil
	})
}

// MarkRootScanStart stamps the beginning of a run and clears the previous error.
func (db *DB) MarkRootScanStart(ctx context.Context, name string, at int64) error {
	return db.writeTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE roots SET last_scan_start = ?, last_scan_error = NULL WHERE name = ?`, at, name)
		if err != nil {
			return fmt.Errorf("stamping scan start for root %q: %w", name, err)
		}
		return nil
	})
}

// MarkRootScanEnd stamps the end of a run. errMsg is "" on success.
func (db *DB) MarkRootScanEnd(ctx context.Context, name string, at int64, errMsg string) error {
	return db.writeTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE roots SET last_scan_end = ?, last_scan_error = ? WHERE name = ?`,
			at, nullString(errMsg), name)
		if err != nil {
			return fmt.Errorf("stamping scan end for root %q: %w", name, err)
		}
		return nil
	})
}

// RecountRoot recomputes series_count / book_count / page_count / total_bytes
// from the rows that currently exist. Called at the end of a root's scan so the
// settings screen never shows a stale total.
func (db *DB) RecountRoot(ctx context.Context, name string) error {
	return db.writeTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE roots SET
				series_count = (SELECT count(*)             FROM series WHERE root_name = roots.name),
				book_count   = (SELECT count(*)             FROM books  WHERE root_name = roots.name),
				page_count   = (SELECT COALESCE(sum(page_count), 0)  FROM books WHERE root_name = roots.name),
				total_bytes  = (SELECT COALESCE(sum(total_bytes), 0) FROM series WHERE root_name = roots.name)
			WHERE name = ?`, name)
		if err != nil {
			return fmt.Errorf("recounting root %q: %w", name, err)
		}
		return nil
	})
}

// nullString maps "" to SQL NULL so a nullable TEXT column never stores an
// empty string that reads back as "set but blank".
func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// nullInt maps 0 to SQL NULL for columns where 0 is not a legal value
// (cover_page_no is 1-based).
func nullInt(n int) any {
	if n == 0 {
		return nil
	}
	return n
}
