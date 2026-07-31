// Package userdata owns `<data_dir>/user.db`: reading progress, per-book viewer
// preferences, user settings and sticky view state.
//
// # Why this is a separate file (NFR-DAT-004, AC-006)
//
// This is the only authored data in SHELF. Everything else — the catalogue, the
// thumbnails, the rasterised PDF pages — is derived from the filesystem and can
// be thrown away and rebuilt. So it lives in its own SQLite file that
// `--rebuild-index` never names: FR-IDX-005 deletes index.db, index.db-wal and
// index.db-shm from a hard-coded list, and user.db is not on it.
//
// The join between the two is the identifier, not a foreign key. book_id is
// SHA-256 over (root name, root-relative path) truncated to 80 bits
// (arch-backend §3.4), a pure function of the config file and the filesystem, so
// a rebuilt index recomputes exactly the ids already stored here. There are
// deliberately no foreign keys into index.db either: a row may reference a book
// that does not currently exist, because an unplugged drive must not erase
// history.
//
// # Locking model
//
// One writer, many readers, exactly as package index: a dedicated *sql.Conn plus
// a one-permit gate that every mutation holds for the length of its
// transaction. Writes here are tiny and rare (a page turn every few seconds),
// so the pool is 4 connections plus the reserved writer. busy_timeout is 5 s to
// absorb a second process. Reads never take the permit and, under WAL, never
// block on a writer.
//
// An index connection ATTACHes this file as `ud` for read-only joins; this
// package is the only writer. No transaction spans both databases.
package userdata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver, CON-001
)

const driverName = "sqlite"

const defaultBusyTimeout = 5 * time.Second

// Sentinel errors. Compare with errors.Is, never by string.
var (
	// ErrNotFound reports that a book has no row in the requested table.
	ErrNotFound = errors.New("userdata: not found")
	// ErrSchemaTooNew reports a user.db written by a newer build. Unlike the
	// index, this file is never rebuilt: the operator downgrades or upgrades.
	ErrSchemaTooNew = errors.New("userdata: database schema is newer than this build")
	// ErrIDVersionMismatch reports authored data written under a different
	// identifier scheme.
	ErrIDVersionMismatch = errors.New("userdata: identifier scheme mismatch")
	// ErrInvalidArgument reports a rejected value; httpapi maps it to 400.
	ErrInvalidArgument = errors.New("userdata: invalid argument")
)

// Options configures Open.
type Options struct {
	// Path is <data_dir>/user.db. Required.
	Path string
	// MaxOpenConns bounds the reader pool. 0 selects 4 (arch §3.7).
	MaxOpenConns int
	// BusyTimeout is the SQLite busy handler timeout. 0 selects 5s.
	BusyTimeout time.Duration
	// Logger receives schema events. nil selects slog.Default().
	Logger *slog.Logger
	// Now overrides the clock. Tests set it; production leaves it nil.
	Now func() time.Time
}

// DB is an open user.db. It is safe for concurrent use.
type DB struct {
	sqldb *sql.DB
	wconn *sql.Conn
	wsem  chan struct{}
	path  string
	log   *slog.Logger
	now   func() time.Time

	closeOnce sync.Once
	closeErr  error
}

// Open opens (creating if absent) user.db and applies the migration ladder.
func Open(ctx context.Context, opts Options) (*DB, error) {
	if opts.Path == "" {
		return nil, errors.New("userdata: Options.Path is empty")
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	busy := opts.BusyTimeout
	if busy <= 0 {
		busy = defaultBusyTimeout
	}
	maxConns := opts.MaxOpenConns
	if maxConns <= 0 {
		maxConns = 4
	}

	abs, err := filepath.Abs(opts.Path)
	if err != nil {
		return nil, fmt.Errorf("resolving user database path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return nil, fmt.Errorf("creating data directory: %w", err)
	}

	sqldb, err := sql.Open(driverName, buildDSN(abs, busy))
	if err != nil {
		return nil, fmt.Errorf("opening user database: %w", err)
	}
	sqldb.SetMaxOpenConns(maxConns + 1)
	sqldb.SetMaxIdleConns(2)

	db := &DB{sqldb: sqldb, wsem: make(chan struct{}, 1), path: abs, log: log, now: now}
	if err := db.init(ctx); err != nil {
		_ = sqldb.Close()
		return nil, err
	}
	return db, nil
}

func (db *DB) init(ctx context.Context) error {
	if err := db.sqldb.PingContext(ctx); err != nil {
		return fmt.Errorf("opening user database %s: %w", db.path, err)
	}
	if err := verifyJournalMode(ctx, db.sqldb); err != nil {
		return err
	}
	wconn, err := db.sqldb.Conn(ctx)
	if err != nil {
		return fmt.Errorf("reserving writer connection: %w", err)
	}
	db.wconn = wconn
	if err := db.migrate(ctx); err != nil {
		_ = wconn.Close()
		return err
	}
	return nil
}

// Path is the absolute path of user.db.
func (db *DB) Path() string { return db.path }

// Close releases the writer connection and the pool.
func (db *DB) Close() error {
	db.closeOnce.Do(func() {
		var errs []error
		if db.wconn != nil {
			if err := db.wconn.Close(); err != nil {
				errs = append(errs, err)
			}
		}
		if err := db.sqldb.Close(); err != nil {
			errs = append(errs, err)
		}
		db.closeErr = errors.Join(errs...)
	})
	return db.closeErr
}

func (db *DB) acquireWrite(ctx context.Context) (func(), error) {
	select {
	case db.wsem <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-db.wsem }) }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// writeTx runs fn inside one immediate transaction on the reserved writer
// connection, holding the single write permit for its duration.
func (db *DB) writeTx(ctx context.Context, fn func(context.Context, *sql.Tx) error) error {
	release, err := db.acquireWrite(ctx)
	if err != nil {
		return err
	}
	defer release()

	tx, err := db.wconn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning write transaction: %w", err)
	}
	if err := fn(ctx, tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing write transaction: %w", err)
	}
	return nil
}

// buildDSN renders the connection string of arch-backend §3.6 (WAL is
// NFR-DAT-003). foreign_keys is on for consistency even though this schema has
// none, so a future rung inherits the safe default.
func buildDSN(absPath string, busy time.Duration) string {
	q := url.Values{}
	q.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", busy.Milliseconds()))
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Set("_txlock", "immediate")
	return "file:" + uriPath(absPath) + "?" + q.Encode()
}

func uriPath(absPath string) string {
	p := filepath.ToSlash(absPath)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	u := url.URL{Path: p}
	return "//" + u.EscapedPath()
}

func verifyJournalMode(ctx context.Context, sqldb *sql.DB) error {
	var mode string
	if err := sqldb.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		return fmt.Errorf("reading journal mode: %w", err)
	}
	if !strings.EqualFold(mode, "wal") {
		return fmt.Errorf("userdata: journal_mode is %q, want wal (NFR-DAT-003)", mode)
	}
	return nil
}

// DBFiles lists every file that belongs to a user.db. It exists so an operator
// tool can back the set up as a unit — nothing in SHELF ever deletes them.
func DBFiles(path string) []string {
	return []string{path, path + "-wal", path + "-shm"}
}
