package userdata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Export is the FR-STT-004 document of arch §7.11. The json tags are part of
// the frozen contract; the HTTP layer marshals this type directly.
type Export struct {
	Format     string       `json:"format"`
	ExportedAt int64        `json:"exported_at"`
	IDVersion  string       `json:"id_version"`
	Items      []ExportItem `json:"items"`
	Prefs      []ExportPref `json:"prefs"`
}

// ExportItem is one progress row.
type ExportItem struct {
	BookID    string `json:"book_id"`
	SeriesID  string `json:"series_id"`
	RootName  string `json:"root_name"`
	BookPath  string `json:"book_path"`
	LastPage  int    `json:"last_page"`
	PageCount int    `json:"page_count"`
	Completed bool   `json:"completed"`
	StartedAt int64  `json:"started_at"`
	UpdatedAt int64  `json:"updated_at"`
}

// ExportPref is one per-book override. A null field means "no override".
type ExportPref struct {
	BookID      string  `json:"book_id"`
	ReadingDir  *string `json:"reading_direction"`
	DisplayMode *string `json:"display_mode"`
	FitMode     *string `json:"fit_mode"`
}

// ImportStrategy selects how an incoming row meets an existing one.
type ImportStrategy string

const (
	// StrategyMerge keeps whichever progress row has the newer updated_at, and
	// keeps the local prefs row when one exists (the export carries no
	// timestamp for prefs, so there is nothing to compare).
	StrategyMerge ImportStrategy = "merge"
	// StrategyReplace overwrites unconditionally (?strategy=replace).
	StrategyReplace ImportStrategy = "replace"
)

// ImportResult is the POST /api/progress/import response body.
type ImportResult struct {
	Imported  int `json:"imported"`
	Skipped   int `json:"skipped"`
	Conflicts int `json:"conflicts"`
}

// Export snapshots every progress row and every stored preference override.
func (db *DB) Export(ctx context.Context) (Export, error) {
	out := Export{
		Format:     ExportFormat,
		ExportedAt: db.now().Unix(),
		IDVersion:  IDVersion,
		Items:      []ExportItem{},
		Prefs:      []ExportPref{},
	}

	rows, err := db.sqldb.QueryContext(ctx,
		`SELECT `+progressColumns+` FROM progress ORDER BY book_id`)
	if err != nil {
		return Export{}, fmt.Errorf("exporting progress: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		p, err := scanProgress(rows)
		if err != nil {
			return Export{}, fmt.Errorf("scanning progress: %w", err)
		}
		// ExportItem is Progress with json tags; the conversion is deliberate,
		// so that adding a field to one and not the other fails to compile.
		out.Items = append(out.Items, ExportItem(p))
	}
	if err := rows.Err(); err != nil {
		return Export{}, fmt.Errorf("exporting progress: %w", err)
	}

	prefs, err := db.ListPrefs(ctx)
	if err != nil {
		return Export{}, err
	}
	for _, p := range prefs {
		out.Prefs = append(out.Prefs, ExportPref{
			BookID: p.BookID, ReadingDir: p.ReadingDir,
			DisplayMode: p.DisplayMode, FitMode: p.FitMode,
		})
	}
	return out, nil
}

// Import merges a document produced by Export.
//
// The whole import is one transaction: a partial import of somebody's reading
// history is worse than a failed one. An id_version mismatch is refused outright
// — the ids would point at different books.
//
// The document is untrusted. It arrives on POST /api/progress/import as a file
// the user chose, so every value in it passes the same gate the corresponding
// write path applies: enum fields are validated against the frozen §7.3 sets and
// a refusal aborts the whole document; page numbers are clamped exactly as
// PutProgress clamps them.
//
// Counting, pinned by test: Conflicts counts incoming rows that already existed
// locally (whatever the outcome); Imported counts rows actually written;
// Skipped counts rows dropped because the local copy was newer.
func (db *DB) Import(ctx context.Context, doc Export, strategy ImportStrategy) (ImportResult, error) {
	if doc.Format != "" && doc.Format != ExportFormat {
		return ImportResult{}, fmt.Errorf("userdata: export format %q, want %q: %w",
			doc.Format, ExportFormat, ErrInvalidArgument)
	}
	if doc.IDVersion != "" && doc.IDVersion != IDVersion {
		return ImportResult{}, fmt.Errorf("userdata: export id scheme %q, this build uses %q: %w",
			doc.IDVersion, IDVersion, ErrIDVersionMismatch)
	}
	switch strategy {
	case "", StrategyMerge:
		strategy = StrategyMerge
	case StrategyReplace:
	default:
		return ImportResult{}, fmt.Errorf("userdata: strategy %q: %w", strategy, ErrInvalidArgument)
	}

	var res ImportResult
	err := db.writeTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res = ImportResult{}
		if err := importProgress(ctx, tx, doc.Items, strategy, &res); err != nil {
			return err
		}
		return importPrefs(ctx, tx, doc.Prefs, strategy, db.now().Unix())
	})
	if err != nil {
		return ImportResult{}, err
	}
	return res, nil
}

func importProgress(ctx context.Context, tx *sql.Tx, items []ExportItem,
	strategy ImportStrategy, res *ImportResult) error {
	if len(items) == 0 {
		return nil
	}
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
		return fmt.Errorf("preparing progress write: %w", err)
	}
	defer write.Close()

	for _, it := range items {
		if it.BookID == "" {
			return fmt.Errorf("userdata: import item with empty book id: %w", ErrInvalidArgument)
		}
		if it.PageCount < 0 {
			return fmt.Errorf("userdata: import item %q: page count %d is negative: %w",
				it.BookID, it.PageCount, ErrInvalidArgument)
		}
		var localUpdated int64
		err := lookup.QueryRowContext(ctx, it.BookID).Scan(&localUpdated)
		exists := true
		switch {
		case errors.Is(err, sql.ErrNoRows):
			exists = false
		case err != nil:
			return fmt.Errorf("reading local progress for book %q: %w", it.BookID, err)
		}
		if exists {
			res.Conflicts++
			if strategy == StrategyMerge && localUpdated >= it.UpdatedAt {
				res.Skipped++
				continue
			}
		}
		completed := 0
		if it.Completed {
			completed = 1
		}
		startedAt := it.StartedAt
		if startedAt == 0 {
			startedAt = it.UpdatedAt
		}
		if _, err := write.ExecContext(ctx, it.BookID, it.SeriesID, it.RootName, it.BookPath,
			clampPage(it.LastPage, it.PageCount), it.PageCount, completed, startedAt,
			it.UpdatedAt); err != nil {
			return fmt.Errorf("importing progress for book %q: %w", it.BookID, err)
		}
		res.Imported++
	}
	return nil
}

func importPrefs(ctx context.Context, tx *sql.Tx, prefs []ExportPref,
	strategy ImportStrategy, now int64) error {
	if len(prefs) == 0 {
		return nil
	}
	verb := `ON CONFLICT(book_id) DO UPDATE SET
			reading_dir  = excluded.reading_dir,
			display_mode = excluded.display_mode,
			fit_mode     = excluded.fit_mode,
			updated_at   = excluded.updated_at`
	if strategy == StrategyMerge {
		verb = `ON CONFLICT(book_id) DO NOTHING`
	}
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO book_prefs (book_id, reading_dir, display_mode, fit_mode, updated_at)
		VALUES (?,?,?,?,?) `+verb)
	if err != nil {
		return fmt.Errorf("preparing prefs write: %w", err)
	}
	defer stmt.Close()

	for _, p := range prefs {
		if p.BookID == "" {
			return fmt.Errorf("userdata: import prefs with empty book id: %w", ErrInvalidArgument)
		}
		if err := validatePrefValues(p); err != nil {
			return fmt.Errorf("importing prefs for book %q: %w", p.BookID, err)
		}
		if p.ReadingDir == nil && p.DisplayMode == nil && p.FitMode == nil {
			continue
		}
		if _, err := stmt.ExecContext(ctx, p.BookID, p.ReadingDir, p.DisplayMode,
			p.FitMode, now); err != nil {
			return fmt.Errorf("importing prefs for book %q: %w", p.BookID, err)
		}
	}
	return nil
}

// validatePrefValues runs an imported override through the same enum gate
// PutPrefs applies (prefs.go). Without it the import is a hole straight past
// C-1/C-2: "double" and "screen" would reach storage and leave again as a
// DisplayMode/FitMode the frozen contract says cannot exist.
func validatePrefValues(p ExportPref) error {
	if err := validateValue("reading_direction", p.ReadingDir, validReadingDir); err != nil {
		return err
	}
	if err := validateValue("display_mode", p.DisplayMode, validDisplayMode); err != nil {
		return err
	}
	return validateValue("fit_mode", p.FitMode, validFitMode)
}
