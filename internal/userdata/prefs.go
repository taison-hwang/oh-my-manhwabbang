package userdata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Prefs is the per-book viewer memory of FR-VWR-002. A nil field means "inherit
// the global default"; it is not the same as an empty string, which is why the
// fields are pointers and why the update type below is three-state.
type Prefs struct {
	BookID      string
	ReadingDir  *string
	DisplayMode *string
	FitMode     *string
	UpdatedAt   int64
}

// Patch is a three-state field for a PATCH-like update:
//
//	absent          leave the stored value alone
//	present, nil    clear the override, fall back to the global default
//	present, value  set the override
//
// This exists because arch §7.6's BookPrefsUpdate distinguishes an omitted key
// from an explicit null, and collapsing the two would make it impossible to
// return to the default once an override was set.
type Patch[T any] struct {
	Present bool
	Value   *T
}

// SetPatch builds a "set to v" patch.
func SetPatch[T any](v T) Patch[T] { return Patch[T]{Present: true, Value: &v} }

// ClearPatch builds a "clear the override" patch.
func ClearPatch[T any]() Patch[T] { return Patch[T]{Present: true} }

// PrefsPatch is the update payload of PUT /api/books/{bid}/prefs.
type PrefsPatch struct {
	ReadingDir  Patch[string]
	DisplayMode Patch[string]
	FitMode     Patch[string]
}

// Legal wire values, frozen by arch §7.3 and conflict resolutions C-1/C-2.
var (
	validReadingDir  = []string{"ltr", "rtl"}
	validDisplayMode = []string{"single", "spread", "vertical"}
	validFitMode     = []string{"width", "height", "original", "contain"}
)

func validate(field string, p Patch[string], allowed []string) error {
	if !p.Present {
		return nil
	}
	return validateValue(field, p.Value, allowed)
}

// validateValue is the single gate on the frozen enums. nil means "no override"
// and is always legal.
//
// Both write paths go through here: PUT /api/books/{bid}/prefs *and*
// POST /api/progress/import. An export document is a file the user hands us, so
// it is exactly as untrusted as a request body (arch §7.11) — and the values it
// carries leave storage again through GET /api/books/{bid}/prefs, whose type is
// frozen at DisplayMode = "single"|"spread"|"vertical" (arch §7.3, C-1/C-2).
func validateValue(field string, v *string, allowed []string) error {
	if v == nil {
		return nil
	}
	for _, a := range allowed {
		if *v == a {
			return nil
		}
	}
	return fmt.Errorf("userdata: %s %q: %w", field, *v, ErrInvalidArgument)
}

// GetPrefs returns a book's overrides. A book with no row yields a zero Prefs
// (every field nil, i.e. inherit everything) and no error — "not configured" is
// the normal case, not an exception.
func (db *DB) GetPrefs(ctx context.Context, bookID string) (Prefs, error) {
	p := Prefs{BookID: bookID}
	err := db.sqldb.QueryRowContext(ctx,
		`SELECT reading_dir, display_mode, fit_mode, updated_at FROM book_prefs WHERE book_id = ?`,
		bookID).Scan(&p.ReadingDir, &p.DisplayMode, &p.FitMode, &p.UpdatedAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return p, nil
	case err != nil:
		return Prefs{}, fmt.Errorf("reading prefs for book %q: %w", bookID, err)
	}
	return p, nil
}

// PutPrefs applies a three-state patch and returns the stored result. When the
// patch leaves every override nil the row is deleted, so "reset to defaults"
// leaves no residue.
func (db *DB) PutPrefs(ctx context.Context, bookID string, patch PrefsPatch) (Prefs, error) {
	if bookID == "" {
		return Prefs{}, fmt.Errorf("userdata: empty book id: %w", ErrInvalidArgument)
	}
	if err := validate("reading_direction", patch.ReadingDir, validReadingDir); err != nil {
		return Prefs{}, err
	}
	if err := validate("display_mode", patch.DisplayMode, validDisplayMode); err != nil {
		return Prefs{}, err
	}
	if err := validate("fit_mode", patch.FitMode, validFitMode); err != nil {
		return Prefs{}, err
	}

	current, err := db.GetPrefs(ctx, bookID)
	if err != nil {
		return Prefs{}, err
	}
	next := Prefs{
		BookID:      bookID,
		ReadingDir:  apply(current.ReadingDir, patch.ReadingDir),
		DisplayMode: apply(current.DisplayMode, patch.DisplayMode),
		FitMode:     apply(current.FitMode, patch.FitMode),
		UpdatedAt:   db.now().Unix(),
	}

	err = db.writeTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if next.ReadingDir == nil && next.DisplayMode == nil && next.FitMode == nil {
			if _, err := tx.ExecContext(ctx, `DELETE FROM book_prefs WHERE book_id = ?`, bookID); err != nil {
				return fmt.Errorf("clearing prefs for book %q: %w", bookID, err)
			}
			return nil
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO book_prefs (book_id, reading_dir, display_mode, fit_mode, updated_at)
			VALUES (?,?,?,?,?)
			ON CONFLICT(book_id) DO UPDATE SET
				reading_dir  = excluded.reading_dir,
				display_mode = excluded.display_mode,
				fit_mode     = excluded.fit_mode,
				updated_at   = excluded.updated_at`,
			bookID, next.ReadingDir, next.DisplayMode, next.FitMode, next.UpdatedAt)
		if err != nil {
			return fmt.Errorf("writing prefs for book %q: %w", bookID, err)
		}
		return nil
	})
	if err != nil {
		return Prefs{}, err
	}
	if next.ReadingDir == nil && next.DisplayMode == nil && next.FitMode == nil {
		next.UpdatedAt = 0
	}
	return next, nil
}

func apply(current *string, p Patch[string]) *string {
	if !p.Present {
		return current
	}
	return p.Value
}

// ListPrefs returns every stored override, ordered by book id. Used by the
// export.
func (db *DB) ListPrefs(ctx context.Context) ([]Prefs, error) {
	rows, err := db.sqldb.QueryContext(ctx,
		`SELECT book_id, reading_dir, display_mode, fit_mode, updated_at
		 FROM book_prefs ORDER BY book_id`)
	if err != nil {
		return nil, fmt.Errorf("listing prefs: %w", err)
	}
	defer rows.Close()

	var out []Prefs
	for rows.Next() {
		var p Prefs
		if err := rows.Scan(&p.BookID, &p.ReadingDir, &p.DisplayMode, &p.FitMode, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning prefs: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing prefs: %w", err)
	}
	return out, nil
}
