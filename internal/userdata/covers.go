package userdata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
)

// Cover is one row of `book_covers`: the region of one page the user chose as
// a book's cover (ruling E-69 §2.2(a)).
//
// The region is in normalised page coordinates, 0–1 of the page's original
// pixels (§2.4). PageCount is the book's length when the region was chosen —
// a baseline, like progress.page_count, not a measurement of the book now.
// A row is kept when the book changes under it (§2.5): when Page is no longer
// in the book, or the book is not ok, the default cover is used and the row
// waits for the page to come back.
type Cover struct {
	BookID    string
	SeriesID  string
	RootName  string
	BookPath  string
	Page      int
	PageCount int
	X, Y      float64
	W, H      float64
	UpdatedAt int64
}

// CoverRegion is the body of PUT /api/books/{bid}/cover: a page and a region
// of it.
type CoverRegion struct {
	Page int
	X, Y float64
	W, H float64
}

// CoverPut is a CoverRegion plus the identity and length the caller resolved
// from the index. PageCount is the book's current length; it bounds Page and
// is stored as the row's baseline.
type CoverPut struct {
	BookID    string
	SeriesID  string
	RootName  string
	BookPath  string
	PageCount int
	Region    CoverRegion
}

// minCoverSide is E-69 §2.4's lower bound on w and h.
const minCoverSide = 0.05

// ValidateCoverRegion is E-69 §2.4, verbatim: 0 ≤ x, 0 ≤ y, w ≥ 0.05,
// h ≥ 0.05, x + w ≤ 1, y + h ≤ 1 and page ∈ [1, pageCount]. A refusal is
// ErrInvalidArgument, which httpapi maps to 400 bad_request.
//
// The sums are compared exactly. The ruling names no tolerance and this does
// not invent one; the edge-pinned frames a client sends (x = 1 − w) pass as
// IEEE doubles, which the tests pin.
//
// NaN and ±Inf are refused outright: every comparison with NaN is false, so
// without the explicit check a NaN would slip past a rule written as "refuse
// when x < 0".
//
// pageCount ≤ 0 is refused, by the page rule itself: no page ≥ 1 is ≤ 0.
// That is the intended outcome, not an accident of the arithmetic — 0 means
// "length unknown" (arch §4.11), what the scanner leaves on a book that is not
// ok, and a page cannot be proved to be in a book of unknown length. The same
// rule covers an imported row, whose pageCount is its own recorded baseline: a
// PUT could never have stored 0.
//
// The book's status is not this package's to know; the HTTP layer checks it.
func ValidateCoverRegion(r CoverRegion, pageCount int) error {
	for _, v := range [...]float64{r.X, r.Y, r.W, r.H} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return fmt.Errorf("userdata: cover region %+v is not finite: %w", r, ErrInvalidArgument)
		}
	}
	if r.Page < 1 || r.Page > pageCount {
		return fmt.Errorf("userdata: cover page %d outside [1, %d]: %w",
			r.Page, pageCount, ErrInvalidArgument)
	}
	if r.X < 0 || r.Y < 0 || r.W < minCoverSide || r.H < minCoverSide ||
		r.X+r.W > 1 || r.Y+r.H > 1 {
		return fmt.Errorf("userdata: cover region x=%v y=%v w=%v h=%v: %w",
			r.X, r.Y, r.W, r.H, ErrInvalidArgument)
	}
	return nil
}

const coverColumns = `book_id, series_id, root_name, book_path, page_no, page_count,
	x, y, w, h, updated_at`

func scanCover(sc interface{ Scan(...any) error }) (Cover, error) {
	var c Cover
	err := sc.Scan(&c.BookID, &c.SeriesID, &c.RootName, &c.BookPath, &c.Page, &c.PageCount,
		&c.X, &c.Y, &c.W, &c.H, &c.UpdatedAt)
	return c, err
}

// GetCover returns a book's chosen cover. ok is false when the book has none —
// the default cover is the normal case, not an error.
func (db *DB) GetCover(ctx context.Context, bookID string) (Cover, bool, error) {
	if bookID == "" {
		return Cover{}, false, fmt.Errorf("userdata: empty book id: %w", ErrInvalidArgument)
	}
	c, err := scanCover(db.sqldb.QueryRowContext(ctx,
		`SELECT `+coverColumns+` FROM book_covers WHERE book_id = ?`, bookID))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Cover{}, false, nil
	case err != nil:
		return Cover{}, false, fmt.Errorf("reading cover for book %q: %w", bookID, err)
	}
	return c, true, nil
}

// GetCovers returns the chosen covers of the given books, keyed by book id.
// Books without one are absent from the map. Used by book listings.
func (db *DB) GetCovers(ctx context.Context, bookIDs []string) (map[string]Cover, error) {
	out := make(map[string]Cover, len(bookIDs))
	err := forEachIDChunk(bookIDs, func(args []any, ph string) error {
		rows, err := db.sqldb.QueryContext(ctx,
			`SELECT `+coverColumns+` FROM book_covers WHERE book_id IN (`+ph+`)`, args...)
		if err != nil {
			return fmt.Errorf("reading covers: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanCover(rows)
			if err != nil {
				return fmt.Errorf("scanning cover: %w", err)
			}
			out[c.BookID] = c
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("reading covers: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// PutCover validates a region (ValidateCoverRegion) and stores it as the
// book's cover, replacing any earlier choice. The stored page_count is
// p.PageCount, the same length the page was checked against.
func (db *DB) PutCover(ctx context.Context, p CoverPut) (Cover, error) {
	if p.BookID == "" {
		return Cover{}, fmt.Errorf("userdata: empty book id: %w", ErrInvalidArgument)
	}
	if err := ValidateCoverRegion(p.Region, p.PageCount); err != nil {
		return Cover{}, err
	}
	c := Cover{
		BookID: p.BookID, SeriesID: p.SeriesID, RootName: p.RootName, BookPath: p.BookPath,
		Page: p.Region.Page, PageCount: p.PageCount,
		X: p.Region.X, Y: p.Region.Y, W: p.Region.W, H: p.Region.H,
		UpdatedAt: db.now().Unix(),
	}
	err := db.writeTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		return upsertCover(ctx, tx, c)
	})
	if err != nil {
		return Cover{}, err
	}
	return c, nil
}

const upsertCoverSQL = `
	INSERT INTO book_covers (` + coverColumns + `)
	VALUES (?,?,?,?,?,?,?,?,?,?,?)
	ON CONFLICT(book_id) DO UPDATE SET
		series_id  = excluded.series_id,
		root_name  = excluded.root_name,
		book_path  = excluded.book_path,
		page_no    = excluded.page_no,
		page_count = excluded.page_count,
		x          = excluded.x,
		y          = excluded.y,
		w          = excluded.w,
		h          = excluded.h,
		updated_at = excluded.updated_at`

func upsertCover(ctx context.Context, tx *sql.Tx, c Cover) error {
	_, err := tx.ExecContext(ctx, upsertCoverSQL,
		c.BookID, c.SeriesID, c.RootName, c.BookPath, c.Page, c.PageCount,
		c.X, c.Y, c.W, c.H, c.UpdatedAt)
	if err != nil {
		return fmt.Errorf("writing cover for book %q: %w", c.BookID, err)
	}
	return nil
}

// DeleteCover returns a book to its default cover. A book with no choice is
// not an error: DELETE /api/books/{bid}/cover is 204 either way (E-69 §2.6).
func (db *DB) DeleteCover(ctx context.Context, bookID string) error {
	if bookID == "" {
		return fmt.Errorf("userdata: empty book id: %w", ErrInvalidArgument)
	}
	return db.writeTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM book_covers WHERE book_id = ?`, bookID); err != nil {
			return fmt.Errorf("deleting cover for book %q: %w", bookID, err)
		}
		return nil
	})
}

// ListCovers returns every chosen cover, ordered by book id. Used by the
// export.
func (db *DB) ListCovers(ctx context.Context) ([]Cover, error) {
	rows, err := db.sqldb.QueryContext(ctx,
		`SELECT `+coverColumns+` FROM book_covers ORDER BY book_id`)
	if err != nil {
		return nil, fmt.Errorf("listing covers: %w", err)
	}
	defer rows.Close()

	var out []Cover
	for rows.Next() {
		c, err := scanCover(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning cover: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing covers: %w", err)
	}
	return out, nil
}
