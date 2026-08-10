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
//
// PageCount is always the index's current length: it is what the clamp and the
// auto-complete rule are computed from. StaleSeen decides something else — a
// caller sets it when the reader has acknowledged the "the file changed" hint,
// and only then does PageCount also replace the stored baseline of an existing
// row, and only when that length is known at all (ruling E-45 §2: an
// acknowledgement of an unknown length would rebaseline to 0, which is never
// stale and therefore permanent).
type ProgressUpdate struct {
	BookID    string
	SeriesID  string
	RootName  string
	BookPath  string
	Page      int
	PageCount int
	Completed *bool
	StaleSeen bool
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
// page_count is preserved the same way, and for the same kind of reason: it is
// a baseline, not a measurement of this write. It records how long the book was
// when the reader last agreed it was that long, so that `stale` stays derivable
// (arch §3.4, §7.3). Only a write carrying StaleSeen and a KNOWN length
// (PageCount > 0) moves it — an unacknowledged write that rebaselined would
// silently destroy the evidence the hint is computed from, and an acknowledged
// write of an unknown length would destroy it just as completely (ruling E-45).
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

	// The INSERT always stores the length it was given: a first write has no
	// baseline to protect, and the reader has seen the book at exactly this
	// length. Only the UPDATE branch has something to preserve, and the CASE is
	// where it does. Two things about that CASE are worth knowing:
	// `progress.page_count` is qualified for clarity, not out of necessity — a
	// bare `page_count` on the right-hand side of DO UPDATE SET already means
	// the pre-update row's value in SQLite and behaves identically, but the
	// qualifier says so at a glance next to `excluded.`; and its `?` is the
	// TENTH parameter, since SQLite numbers anonymous parameters by their order
	// in the statement text and the conflict clause is written after the nine
	// VALUES.
	//
	// The acknowledgement only counts when the length is known. `PageCount == 0`
	// means "length unknown" (arch §4.11) — what the scanner leaves behind when a
	// file goes bad (scanner.bookFailure) — and an unknown length is not
	// something a reader can acknowledge: what they saw was "the file changed",
	// not "this book is 0 pages long". Letting an acknowledgement store 0 would
	// be permanent, because a recorded 0 is never stale (isStale, convert.go):
	// the baseline would answer "unchanged" for every length the book is ever
	// repaired to, and the warning could never fire again. Refusing it is also
	// what keeps `page_count = 0` on the wire meaning exactly one thing —
	// "length unknown" — instead of doubling as "the reader acknowledged a
	// length of zero", which no reader ever means (ruling E-45 §2).
	//
	// `isStale` is symmetric, so no hint is shown while the length is unknown and
	// no viewer sends this acknowledgement in the first place. This gate is what
	// makes that true of a hand-made API call as well: the request never passes
	// through the screen, so the contract has to hold the line itself.
	ackStale := 0
	if u.StaleSeen && u.PageCount > 0 {
		ackStale = 1
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
				page_count = CASE WHEN ? = 1
				                  THEN excluded.page_count
				                  ELSE progress.page_count END,
				completed  = excluded.completed,
				updated_at = excluded.updated_at`,
			u.BookID, u.SeriesID, u.RootName, u.BookPath, page, u.PageCount, completed, now, now,
			ackStale)
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
