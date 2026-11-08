package userdata

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// Export is the FR-STT-004 document of arch §7.11. The json tags are part of
// the frozen contract; the HTTP layer marshals this type directly.
//
// Covers and SeriesPrefs are format /2 (ruling E-69 §2.9). A /1 document has
// neither; Import refuses one that carries them — as keys, whatever they hold.
type Export struct {
	Format      string            `json:"format"`
	ExportedAt  int64             `json:"exported_at"`
	IDVersion   string            `json:"id_version"`
	Items       []ExportItem      `json:"items"`
	Prefs       []ExportPref      `json:"prefs"`
	Covers      ExportCovers      `json:"covers,omitzero"`
	SeriesPrefs ExportSeriesPrefs `json:"series_prefs,omitzero"`
}

// ExportCovers and ExportSeriesPrefs are the /2 arrays as decoded: non-nil
// exactly when the document had the key. E-69 §2.9 refuses a /1 document that
// has `covers` or `series_prefs` "빈 배열(`[]`)이어도", and strict decoding
// "키가 있는지만 본다" — so `null` counts as present too. encoding/json already
// gives `[]` a non-nil empty slice; only `null` needs UnmarshalJSON, which
// records it as an empty non-nil slice.
//
// The same reading runs the other way through `omitzero`: a nil slice — a
// document built in Go without the array, which is what a /1 document is —
// marshals with no key, while Export's non-nil `[]` still marshals as `[]`
// (omitzero omits only the zero value, nil; omitempty would drop `[]` too and
// change the export). So marshal-then-decode keeps "no key" and "empty key"
// apart, and Export's bytes are what they were.
type (
	ExportCovers      []ExportCover
	ExportSeriesPrefs []ExportSeriesPref
)

// UnmarshalJSON keeps the key's presence; see [ExportCovers]. It also refuses
// a row whose page, page_count or region field is absent or null; see
// [requireCoverFields].
func (c *ExportCovers) UnmarshalJSON(b []byte) error {
	if err := decodePresent(b, (*[]ExportCover)(c)); err != nil {
		return err
	}
	return requireCoverFields(b)
}

// coverRequired are the ExportCover keys a row must carry with a value.
// E-69 §2.9 checks an imported cover "the same as the PUT" (§2.6), whose
// fields are all required — "빠진 `x`가 0으로 통과하지 않게". encoding/json
// leaves an absent or null number at zero, and x = 0, y = 0 are valid, so only
// the key can tell "the left edge" from "not said". page_count is the row's
// own bound for page; 0 is refused as "length unknown" and absent is the same
// thing, so it is required by the same rule.
var coverRequired = []string{"page", "page_count", "x", "y", "w", "h"}

// requireCoverFields runs after decodePresent has decoded the rows strictly,
// so an unknown field is still reported as such first. It returns
// ErrInvalidArgument naming the row and the field: the document is refused
// whole before Import sees any of it, which is the all-or-nothing §2.9 asks.
func requireCoverFields(b []byte) error {
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(b, &rows); err != nil {
		return err
	}
	for i, row := range rows {
		for _, key := range coverRequired {
			v, ok := row[key]
			if !ok || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
				return fmt.Errorf("userdata: covers[%d]: field %q is missing or null: %w",
					i, key, ErrInvalidArgument)
			}
		}
	}
	return nil
}

// UnmarshalJSON keeps the key's presence; see [ExportCovers].
func (p *ExportSeriesPrefs) UnmarshalJSON(b []byte) error {
	return decodePresent(b, (*[]ExportSeriesPref)(p))
}

// decodePresent decodes one array value into a non-nil slice. An Unmarshaler
// is handed raw bytes and the caller's decoder settings do not reach it, so
// the rows are decoded here with DisallowUnknownFields: arch §7.1's strict
// decoding is the contract for the whole document, rows included, and a
// presence check must not be the hole an unknown field walks through.
func decodePresent[T any](b []byte, dst *[]T) error {
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		*dst = []T{}
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var rows []T
	if err := dec.Decode(&rows); err != nil {
		return err
	}
	if rows == nil {
		rows = []T{}
	}
	*dst = rows
	return nil
}

// present reports whether a decoded document had the key at all.
func (c ExportCovers) present() bool      { return c != nil }
func (p ExportSeriesPrefs) present() bool { return p != nil }

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

// ExportCover is one book_covers row (E-69 §2.9). PageCount is the row's
// baseline, and the bound an imported Page is checked against.
type ExportCover struct {
	BookID    string  `json:"book_id"`
	SeriesID  string  `json:"series_id"`
	RootName  string  `json:"root_name"`
	BookPath  string  `json:"book_path"`
	Page      int     `json:"page"`
	PageCount int     `json:"page_count"`
	X         float64 `json:"x"`
	Y         float64 `json:"y"`
	W         float64 `json:"w"`
	H         float64 `json:"h"`
	UpdatedAt int64   `json:"updated_at"`
}

// ExportSeriesPref is one series_prefs row (E-69 §2.9). Like ExportPref it
// carries no timestamp, so it merges by A-17's prefs rule.
type ExportSeriesPref struct {
	SeriesID   string  `json:"series_id"`
	RootName   string  `json:"root_name"`
	SeriesPath string  `json:"series_path"`
	ReadingDir *string `json:"reading_direction"`
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

// Export snapshots every progress row, every stored preference override, every
// chosen cover and every series reading direction. It always writes the
// current format, /2.
func (db *DB) Export(ctx context.Context) (Export, error) {
	out := Export{
		Format:      ExportFormat,
		ExportedAt:  db.now().Unix(),
		IDVersion:   IDVersion,
		Items:       []ExportItem{},
		Prefs:       []ExportPref{},
		Covers:      []ExportCover{},
		SeriesPrefs: []ExportSeriesPref{},
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

	covers, err := db.ListCovers(ctx)
	if err != nil {
		return Export{}, err
	}
	for _, c := range covers {
		// ExportCover is Cover with json tags, converted for the same reason
		// ExportItem is: a field added to one and not the other fails to compile.
		out.Covers = append(out.Covers, ExportCover(c))
	}

	series, err := db.ListSeriesPrefs(ctx)
	if err != nil {
		return Export{}, err
	}
	for _, p := range series {
		out.SeriesPrefs = append(out.SeriesPrefs, ExportSeriesPref{
			SeriesID: p.SeriesID, RootName: p.RootName, SeriesPath: p.SeriesPath,
			ReadingDir: p.ReadingDir,
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
// Skipped counts rows dropped because the local copy was newer. All three
// describe items only — prefs, covers and series_prefs are never counted
// (A-17, E-69 §2.9).
//
// Formats /1 and /2 are accepted (E-69 §2.9), and an absent format is read as
// the current one, as before. A /1 document carrying covers or series_prefs is
// refused: /1 has no such arrays, no SHELF wrote one, and a pre-E-69 server
// would have refused the unknown keys under strict decoding (arch §7.1). The
// test is the key, not the length — `"covers":[]` and `"covers":null` are
// refused too (E-69 §2.9); a document built in Go with a nil slice has no key.
func (db *DB) Import(ctx context.Context, doc Export, strategy ImportStrategy) (ImportResult, error) {
	switch doc.Format {
	case "", ExportFormat:
	case ExportFormatV1:
		if doc.Covers.present() || doc.SeriesPrefs.present() {
			return ImportResult{}, fmt.Errorf("userdata: export format %q carries covers or series_prefs, "+
				"which only %q has: %w", doc.Format, ExportFormat, ErrInvalidArgument)
		}
	default:
		return ImportResult{}, fmt.Errorf("userdata: export format %q, want %q or %q: %w",
			doc.Format, ExportFormatV1, ExportFormat, ErrInvalidArgument)
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
		now := db.now().Unix()
		if err := importProgress(ctx, tx, doc.Items, strategy, &res); err != nil {
			return err
		}
		if err := importPrefs(ctx, tx, doc.Prefs, strategy, now); err != nil {
			return err
		}
		if err := importCovers(ctx, tx, doc.Covers, strategy); err != nil {
			return err
		}
		return importSeriesPrefs(ctx, tx, doc.SeriesPrefs, strategy, now)
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

// importCovers merges book_covers rows the way importProgress merges items
// (E-69 §2.9): under merge the newer updated_at wins and a tie keeps the local
// row; replace overwrites. The stored updated_at is the incoming one, so the
// next merge compares the choice's own age.
//
// Every row passes ValidateCoverRegion against its own page_count, the same
// gate as the PUT. A refusal aborts the whole document.
func importCovers(ctx context.Context, tx *sql.Tx, covers []ExportCover,
	strategy ImportStrategy) error {
	if len(covers) == 0 {
		return nil
	}
	lookup, err := tx.PrepareContext(ctx, `SELECT updated_at FROM book_covers WHERE book_id = ?`)
	if err != nil {
		return fmt.Errorf("preparing cover lookup: %w", err)
	}
	defer lookup.Close()

	for _, c := range covers {
		if c.BookID == "" {
			return fmt.Errorf("userdata: import cover with empty book id: %w", ErrInvalidArgument)
		}
		region := CoverRegion{Page: c.Page, X: c.X, Y: c.Y, W: c.W, H: c.H}
		if err := ValidateCoverRegion(region, c.PageCount); err != nil {
			return fmt.Errorf("importing cover for book %q: %w", c.BookID, err)
		}
		if strategy == StrategyMerge {
			var localUpdated int64
			err := lookup.QueryRowContext(ctx, c.BookID).Scan(&localUpdated)
			switch {
			case errors.Is(err, sql.ErrNoRows):
			case err != nil:
				return fmt.Errorf("reading local cover for book %q: %w", c.BookID, err)
			case localUpdated >= c.UpdatedAt:
				continue
			}
		}
		if err := upsertCover(ctx, tx, Cover(c)); err != nil {
			return err
		}
	}
	return nil
}

// importSeriesPrefs applies A-17's prefs rule to series_prefs (E-69 §2.9): a
// row carries no timestamp, so under merge a series with a local choice keeps
// it and one without takes the incoming row; replace overwrites. A row whose
// reading_direction is null is ignored under either strategy — it does not
// clear a local choice. The stored updated_at is the import time.
func importSeriesPrefs(ctx context.Context, tx *sql.Tx, prefs []ExportSeriesPref,
	strategy ImportStrategy, now int64) error {
	if len(prefs) == 0 {
		return nil
	}
	verb := `ON CONFLICT(series_id) DO UPDATE SET
			root_name   = excluded.root_name,
			series_path = excluded.series_path,
			reading_dir = excluded.reading_dir,
			updated_at  = excluded.updated_at`
	if strategy == StrategyMerge {
		verb = `ON CONFLICT(series_id) DO NOTHING`
	}
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO series_prefs (`+seriesPrefsColumns+`) VALUES (?,?,?,?,?) `+verb)
	if err != nil {
		return fmt.Errorf("preparing series prefs write: %w", err)
	}
	defer stmt.Close()

	for _, p := range prefs {
		if p.SeriesID == "" {
			return fmt.Errorf("userdata: import series prefs with empty series id: %w", ErrInvalidArgument)
		}
		if err := validateValue("reading_direction", p.ReadingDir, validReadingDir); err != nil {
			return fmt.Errorf("importing prefs for series %q: %w", p.SeriesID, err)
		}
		if p.ReadingDir == nil {
			continue
		}
		if _, err := stmt.ExecContext(ctx, p.SeriesID, p.RootName, p.SeriesPath,
			p.ReadingDir, now); err != nil {
			return fmt.Errorf("importing prefs for series %q: %w", p.SeriesID, err)
		}
	}
	return nil
}
