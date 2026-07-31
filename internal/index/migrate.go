package index

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// migrate brings the file up to schemaVersion, creating it from empty when the
// meta table does not exist yet.
//
// Three outcomes are possible and all three are tested:
//
//	stored == 0            fresh file, or one with no meta table: run the ladder
//	stored <  schemaVersion   run the remaining rungs
//	stored == schemaVersion   nothing to do
//	stored >  schemaVersion   ErrSchemaTooNew — refuse, do not guess
//
// An id_version mismatch is a fourth case, and it is special to index.db: the
// ids are derived data, so the whole file is dropped and rebuilt rather than
// reported as an error. userdata does the opposite, because its ids are the
// only thing tying authored rows to books.
func (db *DB) migrate(ctx context.Context) error {
	stored, err := db.readSchemaVersion(ctx)
	if err != nil {
		return err
	}
	if stored > schemaVersion {
		return fmt.Errorf("index: %s is at schema version %d, this build understands %d: %w",
			db.path, stored, schemaVersion, ErrSchemaTooNew)
	}

	if stored == schemaVersion {
		ok, err := db.idVersionMatches(ctx)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		db.log.Warn("index id scheme changed, discarding derived index",
			"path", db.path, "want", idVersion)
		if err := db.writeTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
			_, execErr := tx.ExecContext(ctx, dropAllSQL)
			return execErr
		}); err != nil {
			return fmt.Errorf("dropping stale index: %w", err)
		}
		stored = 0
	}

	now := time.Now().Unix()
	for _, m := range migrations {
		if m.to <= stored {
			continue
		}
		err := db.writeTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, m.sql); err != nil {
				return fmt.Errorf("applying index schema v%d: %w", m.to, err)
			}
			if err := setMeta(ctx, tx, metaSchemaVersion, strconv.Itoa(m.to)); err != nil {
				return err
			}
			if err := setMeta(ctx, tx, metaIDVersion, idVersion); err != nil {
				return err
			}
			if err := setMeta(ctx, tx, metaAppVersion, appVersion()); err != nil {
				return err
			}
			return setMetaIfAbsent(ctx, tx, metaCreatedAt, strconv.FormatInt(now, 10))
		})
		if err != nil {
			return err
		}
		db.log.Info("index schema applied", "path", db.path, "version", m.to)
	}
	return nil
}

// readSchemaVersion returns 0 for a database that has never been initialised.
func (db *DB) readSchemaVersion(ctx context.Context) (int, error) {
	var n int
	err := db.sqldb.QueryRowContext(ctx,
		`SELECT count(*) FROM main.sqlite_master WHERE type = 'table' AND name = 'meta'`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("reading index schema version: %w", err)
	}
	if n == 0 {
		return 0, nil
	}
	var raw string
	err = db.sqldb.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, metaSchemaVersion).Scan(&raw)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return 0, nil
	case err != nil:
		return 0, fmt.Errorf("reading index schema version: %w", err)
	}
	v, convErr := strconv.Atoi(raw)
	if convErr != nil {
		return 0, fmt.Errorf("index: schema_version %q is not an integer", raw)
	}
	return v, nil
}

func (db *DB) idVersionMatches(ctx context.Context) (bool, error) {
	var raw string
	err := db.sqldb.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, metaIDVersion).Scan(&raw)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("reading index id version: %w", err)
	}
	return raw == idVersion, nil
}

func setMeta(ctx context.Context, tx *sql.Tx, key, value string) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO meta (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	if err != nil {
		return fmt.Errorf("writing meta %s: %w", key, err)
	}
	return nil
}

func setMetaIfAbsent(ctx context.Context, tx *sql.Tx, key, value string) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO NOTHING`, key, value)
	if err != nil {
		return fmt.Errorf("writing meta %s: %w", key, err)
	}
	return nil
}

// Meta reads one meta row. ok is false when the key is absent.
func (db *DB) Meta(ctx context.Context, key string) (value string, ok bool, err error) {
	err = db.sqldb.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&value)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", false, nil
	case err != nil:
		return "", false, fmt.Errorf("reading meta %s: %w", key, err)
	}
	return value, true, nil
}

// SchemaVersion reports the version stamped in the open file.
func (db *DB) SchemaVersion(ctx context.Context) (int, error) { return db.readSchemaVersion(ctx) }

// NextScanGen allocates the monotonic generation stamp for a scan run
// (arch-backend §4.9). Rows the run touches carry it; rows left behind at a
// lower generation are swept by SweepRoot.
//
// It is one upsert with RETURNING rather than a SELECT followed by an UPDATE.
// That is not a micro-optimisation: it keeps this transaction write-first, which
// is the invariant that lets the connection run with a deferred txlock (see
// buildDSN) without ever needing a read-to-write lock upgrade.
func (db *DB) NextScanGen(ctx context.Context) (int64, error) {
	var gen int64
	err := db.writeTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var raw string
		err := tx.QueryRowContext(ctx, `
			INSERT INTO meta (key, value) VALUES (?, '1')
			ON CONFLICT(key) DO UPDATE SET
				value = CAST(CAST(meta.value AS INTEGER) + 1 AS TEXT)
			RETURNING value`, metaScanGen).Scan(&raw)
		if err != nil {
			return fmt.Errorf("allocating scan generation: %w", err)
		}
		gen, err = strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return fmt.Errorf("index: scan_gen %q is not an integer", raw)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return gen, nil
}
