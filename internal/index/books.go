package index

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Book mirrors one row of the `books` table: a readable unit inside a series —
// a ZIP, a directory of images, or a PDF.
type Book struct {
	ID          string
	SeriesID    string
	RootName    string
	RelPath     string
	DisplayName string
	// SortKey is natsort.Key over the series-relative path; Ord is its
	// materialised 0-based rank so the API never re-sorts.
	SortKey []byte
	Ord     int
	// Kind is "zip", "dir" or "pdf" (conflict resolution C-4).
	Kind       string
	PageCount  int64
	TotalBytes int64
	FileSize   int64
	FileMtime  int64
	// DirFingerprint is the FNV-1a digest of the book directory's direct
	// children; empty for zip and pdf (arch §4.6).
	DirFingerprint string
	// ContentVersion is the cache buster carried in every page URL as ?v=
	// (D-17, arch §5.3).
	ContentVersion string
	// DimsState is "none", "partial" or "done".
	DimsState string
	// Status is "ok", "error", "encrypted", "empty" or "unsupported"
	// (FR-IDX-010).
	Status  string
	Error   string
	ScanGen int64
}

// BookProgress is the ud.progress row joined onto a book, if any.
type BookProgress struct {
	LastPage  int
	PageCount int
	Completed bool
	StartedAt int64
	UpdatedAt int64
}

// BookRow is a book plus its reading progress. Progress is nil when the book
// has never been opened.
type BookRow struct {
	Book
	Progress *BookProgress
}

const bookColumns = `
	b.id, b.series_id, b.root_name, b.rel_path, b.display_name, b.sort_key, b.ord,
	b.kind, b.page_count, b.total_bytes, b.file_size, b.file_mtime,
	COALESCE(b.dir_fingerprint, ''), b.content_version, b.dims_state, b.status,
	COALESCE(b.error, ''), b.scan_gen,
	p.last_page, p.page_count, p.completed, p.started_at, p.updated_at`

const bookJoins = `
	FROM books b
	LEFT JOIN ud.progress p ON p.book_id = b.id`

func scanBookRow(sc interface{ Scan(...any) error }) (BookRow, error) {
	var r BookRow
	var lastPage, progPages, completed, startedAt, updatedAt sql.NullInt64
	err := sc.Scan(&r.ID, &r.SeriesID, &r.RootName, &r.RelPath, &r.DisplayName, &r.SortKey,
		&r.Ord, &r.Kind, &r.PageCount, &r.TotalBytes, &r.FileSize, &r.FileMtime,
		&r.DirFingerprint, &r.ContentVersion, &r.DimsState, &r.Status, &r.Error, &r.ScanGen,
		&lastPage, &progPages, &completed, &startedAt, &updatedAt)
	if err != nil {
		return BookRow{}, err
	}
	if lastPage.Valid {
		r.Progress = &BookProgress{
			LastPage:  int(lastPage.Int64),
			PageCount: int(progPages.Int64),
			Completed: completed.Int64 != 0,
			StartedAt: startedAt.Int64,
			UpdatedAt: updatedAt.Int64,
		}
	}
	return r, nil
}

const selectBookByID = `SELECT` + bookColumns + bookJoins + ` WHERE b.id = ?`

// GetBook returns one book with its progress, or ErrNotFound.
func (db *DB) GetBook(ctx context.Context, id string) (BookRow, error) {
	row := db.stmts.getBook.QueryRowContext(ctx, id)
	r, err := scanBookRow(row)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return BookRow{}, fmt.Errorf("book %q: %w", id, ErrNotFound)
	case err != nil:
		return BookRow{}, fmt.Errorf("reading book %q: %w", id, err)
	}
	return r, nil
}

// ListBooks returns a series' books ordered by ord, which the scanner
// materialised from the natural sort (FR-IDX-007).
func (db *DB) ListBooks(ctx context.Context, seriesID string) ([]BookRow, error) {
	rows, err := db.sqldb.QueryContext(ctx,
		`SELECT`+bookColumns+bookJoins+` WHERE b.series_id = ? ORDER BY b.ord ASC, b.id ASC`, seriesID)
	if err != nil {
		return nil, fmt.Errorf("listing books of series %q: %w", seriesID, err)
	}
	defer rows.Close()

	var out []BookRow
	for rows.Next() {
		r, err := scanBookRow(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning book: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing books of series %q: %w", seriesID, err)
	}
	return out, nil
}

// Neighbours returns the ids of the books immediately before and after id
// within its series, by ord. Either is "" at the ends. FR-VWR-010.
func (db *DB) Neighbours(ctx context.Context, id string) (prev, next string, err error) {
	var seriesID string
	var ord int
	err = db.sqldb.QueryRowContext(ctx, `SELECT series_id, ord FROM books WHERE id = ?`, id).
		Scan(&seriesID, &ord)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", "", fmt.Errorf("book %q: %w", id, ErrNotFound)
	case err != nil:
		return "", "", fmt.Errorf("reading book %q: %w", id, err)
	}

	scanOne := func(query string) (string, error) {
		var got string
		qErr := db.sqldb.QueryRowContext(ctx, query, seriesID, ord, ord, id).Scan(&got)
		if errors.Is(qErr, sql.ErrNoRows) {
			return "", nil
		}
		if qErr != nil {
			return "", fmt.Errorf("reading neighbours of book %q: %w", id, qErr)
		}
		return got, nil
	}

	if prev, err = scanOne(`SELECT id FROM books WHERE series_id = ?
		AND (ord < ? OR (ord = ? AND id < ?))
		ORDER BY ord DESC, id DESC LIMIT 1`); err != nil {
		return "", "", err
	}
	if next, err = scanOne(`SELECT id FROM books WHERE series_id = ?
		AND (ord > ? OR (ord = ? AND id > ?))
		ORDER BY ord ASC, id ASC LIMIT 1`); err != nil {
		return "", "", err
	}
	return prev, next, nil
}

// ContinueItem is one entry of the FR-LIB-010 "이어보기" shelf: a book that has
// been started and not finished, with just enough of its series to render a card.
type ContinueItem struct {
	Book        BookRow
	SeriesID    string
	SeriesName  string
	HasCover    bool
	SeriesCover string
}

// ListContinue returns started-but-unfinished books, most recently read first.
// It is the one query that genuinely needs both databases at once, which is why
// user.db is attached rather than opened separately.
func (db *DB) ListContinue(ctx context.Context, limit int) ([]ContinueItem, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := db.sqldb.QueryContext(ctx, `
		SELECT`+bookColumns+`,
		       s.display_name, COALESCE(s.cover_kind, '')
		FROM ud.progress p
		JOIN books b  ON b.id = p.book_id
		JOIN series s ON s.id = b.series_id
		JOIN roots r  ON r.name = s.root_name
		WHERE p.completed = 0 AND r.enabled = 1
		ORDER BY p.updated_at DESC, b.id ASC
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("listing continue-reading books: %w", err)
	}
	defer rows.Close()

	var out []ContinueItem
	for rows.Next() {
		var item ContinueItem
		var seriesName, coverKind string
		var lastPage, progPages, completed, startedAt, updatedAt sql.NullInt64
		b := &item.Book
		err := rows.Scan(&b.ID, &b.SeriesID, &b.RootName, &b.RelPath, &b.DisplayName, &b.SortKey,
			&b.Ord, &b.Kind, &b.PageCount, &b.TotalBytes, &b.FileSize, &b.FileMtime,
			&b.DirFingerprint, &b.ContentVersion, &b.DimsState, &b.Status, &b.Error, &b.ScanGen,
			&lastPage, &progPages, &completed, &startedAt, &updatedAt,
			&seriesName, &coverKind)
		if err != nil {
			return nil, fmt.Errorf("scanning continue-reading book: %w", err)
		}
		if lastPage.Valid {
			b.Progress = &BookProgress{
				LastPage:  int(lastPage.Int64),
				PageCount: int(progPages.Int64),
				Completed: completed.Int64 != 0,
				StartedAt: startedAt.Int64,
				UpdatedAt: updatedAt.Int64,
			}
		}
		item.SeriesID = b.SeriesID
		item.SeriesName = seriesName
		item.HasCover = coverKind != ""
		item.SeriesCover = coverKind
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing continue-reading books: %w", err)
	}
	return out, nil
}
