package userdata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
)

// schemaVersion is the DDL revision this build writes and understands.
//
// 2 adds `series_seen` (amendment A-8, ruling E-9). The rung is additive, so a
// file at version 2 is still readable by everything that mattered at version 1 —
// but a build that only knows version 1 still refuses to open it
// (ErrSchemaTooNew), which is correct: this file is never rebuilt.
const schemaVersion = 2

// IDVersion pins the identifier scheme of arch-backend §3.4. It is exported
// because the progress export carries it and the importer refuses a mismatch:
// ids derived under a different scheme point at different books, and silently
// merging them would attach one book's progress to another.
//
// It is a literal rather than an import of internal/ids so that the file that
// must never be lost cannot be invalidated by a change in the package that
// derives ids — a mismatch here is an error, never a rebuild.
const IDVersion = "shelf-id/1"

// ExportFormat is the envelope tag of arch §7.11.
const ExportFormat = "shelf-progress/1"

const (
	metaSchemaVersion = "schema_version"
	metaIDVersion     = "id_version"
	metaCreatedAt     = "created_at"
	// metaFirstSeenBootstrap is stamped once, by the scan run that seeded
	// series_seen for a pre-existing collection (arch §3.6 rule 6).
	metaFirstSeenBootstrap = "first_seen_bootstrap"
)

type migration struct {
	to  int
	sql string
}

// migrations is the whole migration path from an empty file. Rungs may only be
// appended: this database is authored data, and a destructive migration would
// be a data-loss bug, not an inconvenience.
var migrations = []migration{
	{to: 1, sql: schemaV1},
	{to: 2, sql: schemaV2},
}

// schemaV1 is the DDL of arch-backend §3.6, verbatim.
//
// There are deliberately NO foreign keys into index.db. A row is allowed to
// reference a book that does not currently exist: an unplugged drive, a not-yet
// rescanned library or a deleted index must not destroy reading history
// (NFR-DAT-004).
const schemaV1 = `
CREATE TABLE IF NOT EXISTS meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS progress (
    book_id     TEXT PRIMARY KEY,
    series_id   TEXT NOT NULL,
    root_name   TEXT NOT NULL,
    book_path   TEXT NOT NULL,
    last_page   INTEGER NOT NULL,
    page_count  INTEGER NOT NULL,
    completed   INTEGER NOT NULL DEFAULT 0,
    started_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_progress_updated   ON progress(updated_at DESC);
CREATE INDEX IF NOT EXISTS ix_progress_series    ON progress(series_id);
CREATE INDEX IF NOT EXISTS ix_progress_continue  ON progress(updated_at DESC)
    WHERE completed = 0;

CREATE TABLE IF NOT EXISTS book_prefs (
    book_id      TEXT PRIMARY KEY,
    reading_dir  TEXT,
    display_mode TEXT,
    fit_mode     TEXT,
    updated_at   INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS view_state (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);
`

// schemaV2 is amendment A-8 (ruling E-9): the write-once first-sighting stamp
// that backs GET /api/series?scope=added and the sidebar's 최근 추가 count.
//
// It lives here rather than in index.db because --rebuild-index deletes
// index.db and nothing else. In the index, every series would look newly added
// the moment an operator rebuilt — the exact opposite of what the label means
// (NFR-DAT-004, AC-006).
//
// The rung is append-only and creates only. arch §3.6 rule 8: user.db is
// authored data, so a migration may add tables and indexes and nothing else.
const schemaV2 = `
CREATE TABLE IF NOT EXISTS series_seen (
    series_id     TEXT PRIMARY KEY,
    root_name     TEXT NOT NULL,
    series_path   TEXT NOT NULL,
    first_seen_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_series_seen_first ON series_seen(first_seen_at DESC);
`

// migrate brings the file up to schemaVersion, creating it from empty when the
// meta table does not exist yet.
//
// Unlike index.db this file is never dropped and rebuilt. A schema version from
// the future is ErrSchemaTooNew and an id_version mismatch is
// ErrIDVersionMismatch: both stop the process rather than touch authored data.
func (db *DB) migrate(ctx context.Context) error {
	stored, err := db.readSchemaVersion(ctx)
	if err != nil {
		return err
	}
	if stored > schemaVersion {
		return fmt.Errorf("userdata: %s is at schema version %d, this build understands %d: %w",
			db.path, stored, schemaVersion, ErrSchemaTooNew)
	}
	if stored > 0 {
		got, ok, err := db.Meta(ctx, metaIDVersion)
		if err != nil {
			return err
		}
		if ok && got != IDVersion {
			return fmt.Errorf("userdata: %s was written with id scheme %q, this build uses %q: %w",
				db.path, got, IDVersion, ErrIDVersionMismatch)
		}
	}

	now := db.now().Unix()
	for _, m := range migrations {
		if m.to <= stored {
			continue
		}
		err := db.writeTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, m.sql); err != nil {
				return fmt.Errorf("applying user schema v%d: %w", m.to, err)
			}
			if err := setMeta(ctx, tx, metaSchemaVersion, strconv.Itoa(m.to)); err != nil {
				return err
			}
			if err := setMeta(ctx, tx, metaIDVersion, IDVersion); err != nil {
				return err
			}
			return setMetaIfAbsent(ctx, tx, metaCreatedAt, strconv.FormatInt(now, 10))
		})
		if err != nil {
			return err
		}
		db.log.Info("user schema applied", "path", db.path, "version", m.to)
	}
	return nil
}

func (db *DB) readSchemaVersion(ctx context.Context) (int, error) {
	var n int
	err := db.sqldb.QueryRowContext(ctx,
		`SELECT count(*) FROM main.sqlite_master WHERE type = 'table' AND name = 'meta'`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("reading user schema version: %w", err)
	}
	if n == 0 {
		return 0, nil
	}
	raw, ok, err := db.Meta(ctx, metaSchemaVersion)
	if err != nil || !ok {
		return 0, err
	}
	v, convErr := strconv.Atoi(raw)
	if convErr != nil {
		return 0, fmt.Errorf("userdata: schema_version %q is not an integer", raw)
	}
	return v, nil
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
