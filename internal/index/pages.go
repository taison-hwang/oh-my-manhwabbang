package index

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Page mirrors one row of the `pages` table. Page identity is positional: the
// pair (BookID, PageNo) with PageNo 1-based, assigned by the natural sort of
// arch §4.7. The table is WITHOUT ROWID keyed on that pair, so "pages 40..48 of
// book X" is one contiguous B-tree range scan — the storage half of AC-008.
type Page struct {
	BookID string
	PageNo int
	// Name is the decoded display name; EntryPath is the full decoded ZIP entry
	// path, the book-relative path for a directory book, or "" for a PDF.
	Name      string
	EntryPath string
	// Ext is lowercase with the dot, e.g. ".jpg".
	Ext      string
	Size     int64
	CompSize int64
	// Method is the ZIP compression method: 0 stored, 8 deflate.
	Method int
	// LocalHdrOff is the local file header offset that FR-SRV-002 seeks to.
	LocalHdrOff int64
	CRC32       uint32
	Mtime       int64
	// Width and Height are nil until a decode has filled them in (arch §5.8).
	Width  *int
	Height *int
}

// PageDims is one (page, size) pair for UpdateDims.
type PageDims struct {
	PageNo int
	Width  int
	Height int
}

const pageColumns = `
	book_id, page_no, name, entry_path, ext, size, comp_size, method,
	local_hdr_off, crc32, mtime, width, height`

const (
	selectPagesByBook = `SELECT` + pageColumns + ` FROM pages WHERE book_id = ? ORDER BY page_no`
	selectPageRange   = `SELECT` + pageColumns + ` FROM pages
	                     WHERE book_id = ? AND page_no BETWEEN ? AND ? ORDER BY page_no`
	selectPageByNo = `SELECT` + pageColumns + ` FROM pages WHERE book_id = ? AND page_no = ?`
)

func scanPage(sc interface{ Scan(...any) error }) (Page, error) {
	var p Page
	var crc int64
	err := sc.Scan(&p.BookID, &p.PageNo, &p.Name, &p.EntryPath, &p.Ext, &p.Size, &p.CompSize,
		&p.Method, &p.LocalHdrOff, &crc, &p.Mtime, &p.Width, &p.Height)
	if err != nil {
		return Page{}, err
	}
	// SQLite has no unsigned integers; the CRC round-trips through int64.
	p.CRC32 = uint32(crc)
	return p, nil
}

// ListPages returns every page of a book in reading order. GET /api/books/{bid}
// ships all of them in one response (D-15) — 1 071 pages is ~110 KB of JSON and
// it is what makes an arbitrary jump need no round trip.
func (db *DB) ListPages(ctx context.Context, bookID string) ([]Page, error) {
	return collectPages(ctx, db.stmts.listPages, "listing pages", bookID)
}

// PageRange returns pages from..to inclusive, 1-based, clamped to the stored
// rows. from > to yields no rows rather than an error.
func (db *DB) PageRange(ctx context.Context, bookID string, from, to int) ([]Page, error) {
	if from < 1 {
		from = 1
	}
	return collectPages(ctx, db.stmts.pageRange, "reading page range", bookID, from, to)
}

func collectPages(ctx context.Context, stmt *sql.Stmt, what string, args ...any) ([]Page, error) {
	rows, err := stmt.QueryContext(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	defer rows.Close()

	var out []Page
	for rows.Next() {
		p, err := scanPage(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning page: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	return out, nil
}

// GetPage returns one page, or ErrNotFound when n is outside the book.
func (db *DB) GetPage(ctx context.Context, bookID string, pageNo int) (Page, error) {
	row := db.stmts.getPage.QueryRowContext(ctx, bookID, pageNo)
	p, err := scanPage(row)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Page{}, fmt.Errorf("book %q page %d: %w", bookID, pageNo, ErrNotFound)
	case err != nil:
		return Page{}, fmt.Errorf("reading book %q page %d: %w", bookID, pageNo, err)
	}
	return p, nil
}

// UpdateDims fills in the width/height of decoded pages and re-derives the
// book's dims_state (FR-VWR-004). Called by the thumbnail worker, concurrently
// with a running scan, which is why it takes the same write permit as the
// scanner rather than a second connection.
func (db *DB) UpdateDims(ctx context.Context, bookID string, dims []PageDims) error {
	if len(dims) == 0 {
		return nil
	}
	return db.writeTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx,
			`UPDATE pages SET width = ?, height = ? WHERE book_id = ? AND page_no = ?`)
		if err != nil {
			return fmt.Errorf("preparing dimension update: %w", err)
		}
		defer stmt.Close()

		for _, d := range dims {
			if _, err := stmt.ExecContext(ctx, d.Width, d.Height, bookID, d.PageNo); err != nil {
				return fmt.Errorf("updating dimensions of book %q page %d: %w", bookID, d.PageNo, err)
			}
		}
		return refreshDimsState(ctx, tx, bookID)
	})
}

// refreshDimsState recomputes books.dims_state from the page rows so the value
// can never drift from the data it describes.
func refreshDimsState(ctx context.Context, tx *sql.Tx, bookID string) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE books SET dims_state = (
			SELECT CASE
				WHEN count(*) = 0 THEN 'none'
				WHEN sum(CASE WHEN width IS NULL THEN 1 ELSE 0 END) = 0 THEN 'done'
				WHEN sum(CASE WHEN width IS NULL THEN 1 ELSE 0 END) = count(*) THEN 'none'
				ELSE 'partial' END
			FROM pages WHERE book_id = ?)
		WHERE id = ?`, bookID, bookID)
	if err != nil {
		return fmt.Errorf("refreshing dimension state of book %q: %w", bookID, err)
	}
	return nil
}

// CountPages reports how many page rows a book has. Used by the tests and by
// the orphan assertions; the serving path uses books.page_count.
func (db *DB) CountPages(ctx context.Context, bookID string) (int64, error) {
	var n int64
	if err := db.sqldb.QueryRowContext(ctx,
		`SELECT count(*) FROM pages WHERE book_id = ?`, bookID).Scan(&n); err != nil {
		return 0, fmt.Errorf("counting pages of book %q: %w", bookID, err)
	}
	return n, nil
}

// CountOrphanPages reports page rows whose book no longer exists. `pages` has no
// foreign key (see schema.go), so this is the invariant the deletion paths must
// keep at zero.
func (db *DB) CountOrphanPages(ctx context.Context) (int64, error) {
	var n int64
	if err := db.sqldb.QueryRowContext(ctx,
		`SELECT count(*) FROM pages WHERE book_id NOT IN (SELECT id FROM books)`).Scan(&n); err != nil {
		return 0, fmt.Errorf("counting orphan pages: %w", err)
	}
	return n, nil
}
