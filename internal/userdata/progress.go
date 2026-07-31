package userdata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Progress is one row of the `progress` table: how far through a book the user
// got. FR-STT-001.
//
// PageCount is the book's length as recorded when progress was last written.
// When it no longer matches the index the file changed under the reader, and
// the UI shows a stale-progress hint — that is the whole reason it is stored
// here rather than looked up (arch §3.4).
type Progress struct {
	BookID    string
	SeriesID  string
	RootName  string
	BookPath  string
	LastPage  int
	PageCount int
	Completed bool
	StartedAt int64
	UpdatedAt int64
}

// ProgressUpdate is the PUT /api/books/{bid}/progress payload plus the identity
// the caller resolved from the index. Completed is optional: nil means "auto",
// i.e. true exactly when Page reaches PageCount (FR-VWR-012).
type ProgressUpdate struct {
	BookID    string
	SeriesID  string
	RootName  string
	BookPath  string
	Page      int
	PageCount int
	Completed *bool
}

const progressColumns = `book_id, series_id, root_name, book_path, last_page,
	page_count, completed, started_at, updated_at`

func scanProgress(sc interface{ Scan(...any) error }) (Progress, error) {
	var p Progress
	err := sc.Scan(&p.BookID, &p.SeriesID, &p.RootName, &p.BookPath, &p.LastPage,
		&p.PageCount, &p.Completed, &p.StartedAt, &p.UpdatedAt)
	return p, err
}

// GetProgress returns a book's progress, or ErrNotFound when it has never been
// opened.
func (db *DB) GetProgress(ctx context.Context, bookID string) (Progress, error) {
	row := db.sqldb.QueryRowContext(ctx,
		`SELECT `+progressColumns+` FROM progress WHERE book_id = ?`, bookID)
	p, err := scanProgress(row)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Progress{}, fmt.Errorf("progress for book %q: %w", bookID, ErrNotFound)
	case err != nil:
		return Progress{}, fmt.Errorf("reading progress for book %q: %w", bookID, err)
	}
	return p, nil
}

// GetProgressMany returns the rows for the given books, keyed by book id.
// Missing books are simply absent from the map.
func (db *DB) GetProgressMany(ctx context.Context, bookIDs []string) (map[string]Progress, error) {
	out := make(map[string]Progress, len(bookIDs))
	err := forEachIDChunk(bookIDs, func(args []any, ph string) error {
		rows, err := db.sqldb.QueryContext(ctx,
			`SELECT `+progressColumns+` FROM progress WHERE book_id IN (`+ph+`)`, args...)
		if err != nil {
			return fmt.Errorf("reading progress: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			p, err := scanProgress(rows)
			if err != nil {
				return fmt.Errorf("scanning progress: %w", err)
			}
			out[p.BookID] = p
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("reading progress: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// PutProgress records a page turn. It is idempotent and safe to call on every
// turn (the frontend debounces to ~1 s).
//
// Page is clamped to [1, PageCount]. started_at is preserved across updates —
// it is when the user first opened the book, not when they last touched it.
func (db *DB) PutProgress(ctx context.Context, u ProgressUpdate) (Progress, error) {
	if u.BookID == "" {
		return Progress{}, fmt.Errorf("userdata: empty book id: %w", ErrInvalidArgument)
	}
	if u.PageCount < 0 {
		return Progress{}, fmt.Errorf("userdata: page count %d is negative: %w",
			u.PageCount, ErrInvalidArgument)
	}
	page := clampPage(u.Page, u.PageCount)
	completed := 0
	if u.Completed != nil {
		if *u.Completed {
			completed = 1
		}
	} else if u.PageCount > 0 && page >= u.PageCount {
		completed = 1
	}

	now := db.now().Unix()
	err := db.writeTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
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
				updated_at = excluded.updated_at`,
			u.BookID, u.SeriesID, u.RootName, u.BookPath, page, u.PageCount, completed, now, now)
		if err != nil {
			return fmt.Errorf("writing progress for book %q: %w", u.BookID, err)
		}
		return nil
	})
	if err != nil {
		return Progress{}, err
	}
	return db.GetProgress(ctx, u.BookID)
}

// clampPage forces a page number into the book. Every page number in the API is
// 1-based (arch §7.1), so 0 and negatives are meaningless rather than special;
// pageCount == 0 means "length unknown", so only the lower bound applies.
//
// The import path calls this too: a hand-edited export must not be able to store
// a page number the PUT path would have clamped away.
func clampPage(page, pageCount int) int {
	if page < 1 {
		page = 1
	}
	if pageCount > 0 && page > pageCount {
		page = pageCount
	}
	return page
}

// DeleteProgress removes a book's progress ("mark as unread"). Deleting a row
// that does not exist is not an error.
func (db *DB) DeleteProgress(ctx context.Context, bookID string) error {
	return db.writeTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM progress WHERE book_id = ?`, bookID); err != nil {
			return fmt.Errorf("deleting progress for book %q: %w", bookID, err)
		}
		return nil
	})
}

// SeriesAggregate is the FR-STT-002 rollup for one series, computed from the
// rows this database owns. The index computes the same numbers in SQL when it
// needs them inside a listing query; this is for callers holding only user.db.
type SeriesAggregate struct {
	SeriesID       string
	BooksCompleted int64
	BooksStarted   int64
	LastReadAt     int64
	LastBookID     string
	LastPage       int
}

const seriesAggregateSQL = `
	SELECT p.series_id,
	       SUM(p.completed),
	       SUM(1 - p.completed),
	       MAX(p.updated_at),
	       (SELECT q.book_id   FROM progress q WHERE q.series_id = p.series_id
	         ORDER BY q.updated_at DESC, q.book_id LIMIT 1),
	       (SELECT q.last_page FROM progress q WHERE q.series_id = p.series_id
	         ORDER BY q.updated_at DESC, q.book_id LIMIT 1)
	FROM progress p`

// SeriesAggregates rolls progress up per series. An empty seriesIDs slice
// aggregates every series that has any progress at all.
//
// The filter is chunked (see forEachIDChunk): each series_id groups
// independently, so the union of the per-chunk maps is the same answer one
// oversized statement would have given — except that it does not fail.
func (db *DB) SeriesAggregates(ctx context.Context, seriesIDs []string) (map[string]SeriesAggregate, error) {
	out := make(map[string]SeriesAggregate)
	if len(seriesIDs) == 0 {
		if err := db.aggregateInto(ctx, out, "", nil); err != nil {
			return nil, err
		}
		return out, nil
	}
	err := forEachIDChunk(seriesIDs, func(args []any, ph string) error {
		return db.aggregateInto(ctx, out, ` WHERE p.series_id IN (`+ph+`)`, args)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (db *DB) aggregateInto(ctx context.Context, out map[string]SeriesAggregate,
	where string, args []any) error {
	rows, err := db.sqldb.QueryContext(ctx,
		seriesAggregateSQL+where+` GROUP BY p.series_id`, args...)
	if err != nil {
		return fmt.Errorf("aggregating series progress: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var a SeriesAggregate
		if err := rows.Scan(&a.SeriesID, &a.BooksCompleted, &a.BooksStarted,
			&a.LastReadAt, &a.LastBookID, &a.LastPage); err != nil {
			return fmt.Errorf("scanning series aggregate: %w", err)
		}
		out[a.SeriesID] = a
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("aggregating series progress: %w", err)
	}
	return nil
}

// Continue lists started-but-unfinished books, most recently read first
// (FR-LIB-010). This is the user.db-only view; index.ListContinue joins the
// same rows to book and series metadata for the API.
func (db *DB) Continue(ctx context.Context, limit int) ([]Progress, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := db.sqldb.QueryContext(ctx,
		`SELECT `+progressColumns+` FROM progress WHERE completed = 0
		 ORDER BY updated_at DESC, book_id ASC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("listing continue-reading progress: %w", err)
	}
	defer rows.Close()

	var out []Progress
	for rows.Next() {
		p, err := scanProgress(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning progress: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing continue-reading progress: %w", err)
	}
	return out, nil
}

// CountProgress reports how many books have a progress row.
func (db *DB) CountProgress(ctx context.Context) (int64, error) {
	var n int64
	if err := db.sqldb.QueryRowContext(ctx, `SELECT count(*) FROM progress`).Scan(&n); err != nil {
		return 0, fmt.Errorf("counting progress rows: %w", err)
	}
	return n, nil
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// idChunk bounds how many ids go into one `IN (?,…)` list.
//
// SQLite's bound-variable ceiling is a compile-time constant — 32 766 in the
// build modernc.org/sqlite ships, 999 in older ones — and crossing it is a hard
// "too many SQL variables" error, not a slow query. Callers hand these lists a
// page of book ids today, but nothing in the API caps them and prd §5 sizes the
// collection at thousands of series, so the length is not ours to assume.
const idChunk = 400

// forEachIDChunk calls fn once per batch of ids with the bound arguments and the
// matching placeholder list. It is a no-op for an empty slice, so a caller that
// means "no filter" must not route through it.
func forEachIDChunk(ids []string, fn func(args []any, placeholderList string) error) error {
	for start := 0; start < len(ids); start += idChunk {
		end := min(start+idChunk, len(ids))
		args := make([]any, 0, end-start)
		for _, id := range ids[start:end] {
			args = append(args, id)
		}
		if err := fn(args, placeholders(end-start)); err != nil {
			return err
		}
	}
	return nil
}
