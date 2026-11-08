package userdata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// SeriesPrefs is one row of `series_prefs`: the reading direction the user
// chose for a whole series (ruling E-69 §2.3). It sits between the book's own
// choice and the global default on the ladder (EffectiveReadingDir), and it is
// what the server needs to pick the first-page half of a two-page cover scan
// in the direction the user reads that series.
//
// It used to be a client-only seed (C-9, E-33 §2); E-69 moved it here so a
// change of the global default leaves a series choice alone. A nil ReadingDir
// means "no choice": the row does not exist.
type SeriesPrefs struct {
	SeriesID   string
	RootName   string
	SeriesPath string
	ReadingDir *string
	UpdatedAt  int64
}

// SeriesPrefsPut is the PUT /api/series/{sid}/prefs payload plus the identity
// the caller resolved from the index. A nil ReadingDir clears the choice.
type SeriesPrefsPut struct {
	SeriesID   string
	RootName   string
	SeriesPath string
	ReadingDir *string
}

const seriesPrefsColumns = `series_id, root_name, series_path, reading_dir, updated_at`

func scanSeriesPrefs(sc interface{ Scan(...any) error }) (SeriesPrefs, error) {
	var p SeriesPrefs
	err := sc.Scan(&p.SeriesID, &p.RootName, &p.SeriesPath, &p.ReadingDir, &p.UpdatedAt)
	return p, err
}

// GetSeriesPrefs returns a series' choice. A series with no row yields a zero
// SeriesPrefs (ReadingDir nil) and no error, as GetPrefs does.
func (db *DB) GetSeriesPrefs(ctx context.Context, seriesID string) (SeriesPrefs, error) {
	if seriesID == "" {
		return SeriesPrefs{}, fmt.Errorf("userdata: empty series id: %w", ErrInvalidArgument)
	}
	p, err := scanSeriesPrefs(db.sqldb.QueryRowContext(ctx,
		`SELECT `+seriesPrefsColumns+` FROM series_prefs WHERE series_id = ?`, seriesID))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return SeriesPrefs{SeriesID: seriesID}, nil
	case err != nil:
		return SeriesPrefs{}, fmt.Errorf("reading prefs for series %q: %w", seriesID, err)
	}
	return p, nil
}

// GetSeriesPrefsMany returns the rows for the given series, keyed by series
// id. Series without a choice are absent from the map.
func (db *DB) GetSeriesPrefsMany(ctx context.Context, seriesIDs []string) (map[string]SeriesPrefs, error) {
	out := make(map[string]SeriesPrefs, len(seriesIDs))
	err := forEachIDChunk(seriesIDs, func(args []any, ph string) error {
		rows, err := db.sqldb.QueryContext(ctx,
			`SELECT `+seriesPrefsColumns+` FROM series_prefs WHERE series_id IN (`+ph+`)`, args...)
		if err != nil {
			return fmt.Errorf("reading series prefs: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			p, err := scanSeriesPrefs(rows)
			if err != nil {
				return fmt.Errorf("scanning series prefs: %w", err)
			}
			out[p.SeriesID] = p
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("reading series prefs: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// PutSeriesPrefs stores a series' reading direction and returns the stored
// result. The value passes the same frozen-enum gate as a book's
// (validateValue). nil deletes the row, so "back to the global default" leaves
// no residue, as PutPrefs does; the returned UpdatedAt is then 0.
func (db *DB) PutSeriesPrefs(ctx context.Context, p SeriesPrefsPut) (SeriesPrefs, error) {
	if p.SeriesID == "" {
		return SeriesPrefs{}, fmt.Errorf("userdata: empty series id: %w", ErrInvalidArgument)
	}
	if err := validateValue("reading_direction", p.ReadingDir, validReadingDir); err != nil {
		return SeriesPrefs{}, err
	}
	out := SeriesPrefs{SeriesID: p.SeriesID, RootName: p.RootName, SeriesPath: p.SeriesPath,
		ReadingDir: p.ReadingDir}
	if p.ReadingDir != nil {
		out.UpdatedAt = db.now().Unix()
	}
	err := db.writeTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if p.ReadingDir == nil {
			if _, err := tx.ExecContext(ctx,
				`DELETE FROM series_prefs WHERE series_id = ?`, p.SeriesID); err != nil {
				return fmt.Errorf("clearing prefs for series %q: %w", p.SeriesID, err)
			}
			return nil
		}
		return upsertSeriesPrefs(ctx, tx, out)
	})
	if err != nil {
		return SeriesPrefs{}, err
	}
	return out, nil
}

func upsertSeriesPrefs(ctx context.Context, tx *sql.Tx, p SeriesPrefs) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO series_prefs (`+seriesPrefsColumns+`) VALUES (?,?,?,?,?)
		ON CONFLICT(series_id) DO UPDATE SET
			root_name   = excluded.root_name,
			series_path = excluded.series_path,
			reading_dir = excluded.reading_dir,
			updated_at  = excluded.updated_at`,
		p.SeriesID, p.RootName, p.SeriesPath, p.ReadingDir, p.UpdatedAt)
	if err != nil {
		return fmt.Errorf("writing prefs for series %q: %w", p.SeriesID, err)
	}
	return nil
}

// ListSeriesPrefs returns every stored series choice, ordered by series id.
// Used by the export.
func (db *DB) ListSeriesPrefs(ctx context.Context) ([]SeriesPrefs, error) {
	rows, err := db.sqldb.QueryContext(ctx,
		`SELECT `+seriesPrefsColumns+` FROM series_prefs ORDER BY series_id`)
	if err != nil {
		return nil, fmt.Errorf("listing series prefs: %w", err)
	}
	defer rows.Close()

	var out []SeriesPrefs
	for rows.Next() {
		p, err := scanSeriesPrefs(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning series prefs: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing series prefs: %w", err)
	}
	return out, nil
}
