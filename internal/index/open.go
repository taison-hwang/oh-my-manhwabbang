// Package index owns `<data_dir>/index.db`, the derived catalogue produced by a
// scan: roots, series, books, pages and the scan log.
//
// # Disposability (NFR-DAT-001, FR-IDX-005, AC-005/AC-006)
//
// Everything in this database can be reconstructed from the filesystem and the
// config file. Deleting index.db loses nothing that matters: identifiers are
// pure functions of (root name, root-relative path) (arch-backend §3.4), so a
// rescan reproduces them byte-identically and the reading progress held in the
// physically separate user.db (package userdata) rejoins the rebuilt rows.
// Destroy unlinks index.db and its two WAL sidecars — by name, never by glob —
// and touches nothing else. Reset does the same job on a live handle.
//
// # The two databases (NFR-DAT-004, arch-backend §3.7)
//
// user.db is ATTACHed as `ud` onto every index connection through the driver's
// connection hook, so the series list can join reading progress in one query
// (FR-LIB-003/004, amendment A-4). Two rules keep the separation real and are
// binding on every caller and reviewer:
//
//   - `ud.` is READ-ONLY from an index connection. No statement in this package
//     writes to it; user writes go through package userdata's own handle.
//     `make lint` greps for any SQL literal that names the attached schema and
//     also carries a write verb.
//   - No transaction ever spans both databases. SQLite has no two-phase commit
//     across attached WAL databases, and we do not need one: index writes come
//     from the scanner, user writes from the API.
//
// # Locking model — one writer, many readers
//
// The handle keeps a dedicated *sql.Conn for writes plus a one-permit gate
// (`wsem`). Every mutation in this package — Writer transactions, dimension
// fills, scan-log appends — acquires that permit for the duration of its
// transaction and releases it on commit or rollback, so at most one write
// transaction exists in this process at any moment and SQLITE_BUSY cannot arise
// from self-contention. Acquisition honours the caller's context, so a caller
// that supplies a deadline gets one; a caller that does not (a scan context,
// typically) is warned in the log every 30 s that it is still waiting.
//
// Reads never take the permit. They run on the ordinary connection pool
// (max(4, NumCPU)+1 connections, one of which is the reserved writer) and, in
// WAL mode, proceed concurrently with an open write transaction without
// blocking. `busy_timeout` is still set to 5 s to absorb a *second process*
// (a `shelf --rebuild-index` run against a live server, say).
//
// The Writer batches: it commits every 200 books or 2 s, which bounds how long
// it can hold the permit away from a concurrent dimension fill.
package index

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"modernc.org/sqlite"

	"shelf/internal/buildinfo"
)

// driverName is the database/sql name modernc.org/sqlite registers itself under.
const driverName = "sqlite"

// defaultBusyTimeout is arch-backend §3.5's busy_timeout(5000).
const defaultBusyTimeout = 5 * time.Second

// closeGrace bounds how long Close waits for an in-flight write transaction.
const closeGrace = 5 * time.Second

// Sentinel errors. Compare with errors.Is, never by string.
var (
	// ErrNotFound reports that a well-formed id matched no row.
	ErrNotFound = errors.New("index: not found")
	// ErrSchemaTooNew reports an index.db written by a newer build. The file is
	// never rewritten in place; the operator deletes it (or runs
	// --rebuild-index) and rescans.
	ErrSchemaTooNew = errors.New("index: database schema is newer than this build")
	// ErrInvalidFilter reports a rejected ListSeries parameter. The wrapping
	// message names the offending key and value; httpapi maps it to 400.
	ErrInvalidFilter = errors.New("index: invalid filter")
	// ErrUserDBNotReady reports that the user.db handed to Open has not had its
	// schema applied. Open userdata first — it is the authoritative writer.
	ErrUserDBNotReady = errors.New("index: user database is not initialised")
	// ErrWriterClosed reports use of a Writer after Close.
	ErrWriterClosed = errors.New("index: writer is closed")
)

// attachRegistry maps an index DSN to the user.db path that must be ATTACHed
// onto every connection opened for it.
//
// This is package-level mutable state, which impl-plan §5.1 otherwise forbids.
// It is unavoidable: modernc.org/sqlite's connection hook is registered on the
// process-wide driver and receives only the DSN, so the DSN is the only channel
// through which a hook can learn which user.db belongs to the connection being
// opened. The map is refcounted and guarded; entries live exactly as long as the
// *DB that created them.
var attachRegistry = struct {
	mu sync.RWMutex
	m  map[string]*attachEntry
}{m: make(map[string]*attachEntry)}

type attachEntry struct {
	userPath string
	refs     int
}

// hookOnce guards registration of the connection hook. modernc.org/sqlite
// appends hooks to an unsynchronised slice and offers no way to remove one, so
// it must be registered exactly once and before the first Open — hence init.
var hookOnce sync.Once

func init() { registerAttachHook() }

func registerAttachHook() {
	hookOnce.Do(func() {
		sqlite.RegisterConnectionHook(func(conn sqlite.ExecQuerierContext, dsn string) error {
			attachRegistry.mu.RLock()
			entry, ok := attachRegistry.m[dsn]
			var userPath string
			if ok {
				userPath = entry.userPath
			}
			attachRegistry.mu.RUnlock()
			if !ok {
				// Not one of ours (a userdata handle, or a third party's).
				return nil
			}
			_, err := conn.ExecContext(context.Background(),
				"ATTACH DATABASE ? AS ud",
				[]driver.NamedValue{{Ordinal: 1, Value: userPath}})
			if err != nil {
				return fmt.Errorf("attaching user database: %w", err)
			}
			return nil
		})
	})
}

func registerAttach(dsn, userPath string) {
	attachRegistry.mu.Lock()
	defer attachRegistry.mu.Unlock()
	if e, ok := attachRegistry.m[dsn]; ok {
		e.refs++
		return
	}
	attachRegistry.m[dsn] = &attachEntry{userPath: userPath, refs: 1}
}

func unregisterAttach(dsn string) {
	attachRegistry.mu.Lock()
	defer attachRegistry.mu.Unlock()
	e, ok := attachRegistry.m[dsn]
	if !ok {
		return
	}
	e.refs--
	if e.refs <= 0 {
		delete(attachRegistry.m, dsn)
	}
}

// Options configures Open.
type Options struct {
	// Path is <data_dir>/index.db. Required.
	Path string
	// UserPath is <data_dir>/user.db, ATTACHed as `ud`. Required, and the file
	// must already carry the userdata schema.
	UserPath string
	// MaxOpenConns bounds the reader pool. 0 selects max(4, NumCPU); one extra
	// connection is added for the reserved writer.
	MaxOpenConns int
	// BusyTimeout is the SQLite busy handler timeout. 0 selects 5s.
	BusyTimeout time.Duration
	// Logger receives schema and rebuild events. nil selects slog.Default().
	Logger *slog.Logger
}

// DB is an open index.db with user.db attached. It is safe for concurrent use.
type DB struct {
	sqldb    *sql.DB
	wconn    *sql.Conn
	wsem     chan struct{}
	path     string
	userPath string
	dsn      string
	log      *slog.Logger

	stmts     stmtCache
	closeOnce sync.Once
	closeErr  error
}

type stmtCache struct {
	getBook   *sql.Stmt
	getPage   *sql.Stmt
	listPages *sql.Stmt
	pageRange *sql.Stmt
}

// Open opens (creating if absent) index.db, applies the migration ladder and
// attaches user.db.
func Open(ctx context.Context, opts Options) (*DB, error) {
	if opts.Path == "" {
		return nil, errors.New("index: Options.Path is empty")
	}
	if opts.UserPath == "" {
		return nil, errors.New("index: Options.UserPath is empty")
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	busy := opts.BusyTimeout
	if busy <= 0 {
		busy = defaultBusyTimeout
	}
	maxConns := opts.MaxOpenConns
	if maxConns <= 0 {
		maxConns = max(4, runtime.NumCPU())
	}

	absIndex, err := filepath.Abs(opts.Path)
	if err != nil {
		return nil, fmt.Errorf("resolving index path: %w", err)
	}
	absUser, err := filepath.Abs(opts.UserPath)
	if err != nil {
		return nil, fmt.Errorf("resolving user database path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(absIndex), 0o700); err != nil {
		return nil, fmt.Errorf("creating data directory: %w", err)
	}

	dsn := buildDSN(absIndex, busy)
	registerAttach(dsn, absUser)

	sqldb, err := sql.Open(driverName, dsn)
	if err != nil {
		unregisterAttach(dsn)
		return nil, fmt.Errorf("opening index database: %w", err)
	}
	// +1 so the reserved writer connection never starves the readers.
	sqldb.SetMaxOpenConns(maxConns + 1)
	sqldb.SetMaxIdleConns(4)
	sqldb.SetConnMaxIdleTime(0)

	db := &DB{
		sqldb:    sqldb,
		wsem:     make(chan struct{}, 1),
		path:     absIndex,
		userPath: absUser,
		dsn:      dsn,
		log:      log,
	}

	if err := db.init(ctx); err != nil {
		_ = sqldb.Close()
		unregisterAttach(dsn)
		return nil, err
	}
	return db, nil
}

func (db *DB) init(ctx context.Context) error {
	if err := db.sqldb.PingContext(ctx); err != nil {
		return fmt.Errorf("opening index database %s: %w", db.path, err)
	}
	if err := verifyJournalMode(ctx, db.sqldb); err != nil {
		return err
	}
	if err := db.verifyUserDB(ctx); err != nil {
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
	if err := db.prepare(ctx); err != nil {
		_ = wconn.Close()
		return err
	}
	return nil
}

// verifyUserDB proves the ATTACH landed on an initialised user.db. Without this
// check a missing user.db would be silently created empty by SQLite and every
// progress join would fail later with a confusing "no such table" at request
// time instead of here, at startup.
//
// Both tables the listing joins are checked. series_seen arrived with user.db
// schema version 2 (amendment A-8), so a file left at version 1 — opened by an
// older build, or restored from a backup taken before it — would answer every
// GET /api/series with "no such table". Package userdata is the authoritative
// writer and migrates on Open; this is the assertion that it ran first.
func (db *DB) verifyUserDB(ctx context.Context) error {
	for _, table := range [...]string{"progress", "series_seen"} {
		var n int
		err := db.sqldb.QueryRowContext(ctx,
			`SELECT count(*) FROM ud.sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&n)
		if err != nil {
			return fmt.Errorf("inspecting attached user database %s: %w", db.userPath, err)
		}
		if n == 0 {
			return fmt.Errorf("index: user database %s has no %s table: %w",
				db.userPath, table, ErrUserDBNotReady)
		}
	}
	return nil
}

func (db *DB) prepare(ctx context.Context) error {
	var err error
	if db.stmts.getBook, err = db.sqldb.PrepareContext(ctx, selectBookByID); err != nil {
		return fmt.Errorf("preparing book lookup: %w", err)
	}
	if db.stmts.getPage, err = db.sqldb.PrepareContext(ctx, selectPageByNo); err != nil {
		return fmt.Errorf("preparing page lookup: %w", err)
	}
	if db.stmts.listPages, err = db.sqldb.PrepareContext(ctx, selectPagesByBook); err != nil {
		return fmt.Errorf("preparing page listing: %w", err)
	}
	if db.stmts.pageRange, err = db.sqldb.PrepareContext(ctx, selectPageRange); err != nil {
		return fmt.Errorf("preparing page range: %w", err)
	}
	return nil
}

// Path is the absolute path of index.db.
func (db *DB) Path() string { return db.path }

// UserPath is the absolute path of the attached user.db.
func (db *DB) UserPath() string { return db.userPath }

// Close releases the writer connection, the prepared statements and the pool.
func (db *DB) Close() error {
	db.closeOnce.Do(func() {
		var errs []error
		for _, st := range []*sql.Stmt{db.stmts.getBook, db.stmts.getPage, db.stmts.listPages, db.stmts.pageRange} {
			if st != nil {
				if err := st.Close(); err != nil {
					errs = append(errs, err)
				}
			}
		}
		// sql.Conn.Close blocks until any transaction on it finishes, so take
		// the write permit first — with a deadline, because a caller that
		// forgot to Close its Writer must get a diagnosable warning rather
		// than a hung shutdown. sql.DB.Close does not block, and the reserved
		// connection is returned to the (closed) pool when its transaction
		// eventually ends.
		if db.wconn != nil {
			ctx, cancel := context.WithTimeout(context.Background(), closeGrace)
			release, err := db.acquireWrite(ctx)
			cancel()
			if err != nil {
				db.log.Warn("closing the index with a write transaction still open",
					"path", db.path)
			} else {
				if err := db.wconn.Close(); err != nil {
					errs = append(errs, err)
				}
				release()
			}
		}
		if err := db.sqldb.Close(); err != nil {
			errs = append(errs, err)
		}
		unregisterAttach(db.dsn)
		db.closeErr = errors.Join(errs...)
	})
	return db.closeErr
}

// writeWaitWarning bounds how long acquireWrite blocks *silently*. Every holder
// of the permit releases it within one batch (200 books or 2 s) or one small
// transaction, so a wait this long means the permit is not coming back on its
// own — in practice a DB-level write issued from the goroutine that is holding
// an open Writer batch, which can never make progress (see the Writer docs).
// Nothing is failed here: a caller with a deadline still gets its deadline, and
// a caller without one at least leaves a trail instead of hanging in silence.
const writeWaitWarning = 30 * time.Second

// acquireWrite takes the single write permit, honouring ctx. The returned
// function releases it and must be called exactly once.
func (db *DB) acquireWrite(ctx context.Context) (func(), error) {
	// Fast path: uncontended, and allocation-free.
	select {
	case db.wsem <- struct{}{}:
		return db.releaseWrite(), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	t := time.NewTicker(writeWaitWarning)
	defer t.Stop()
	waited := time.Duration(0)
	for {
		select {
		case db.wsem <- struct{}{}:
			return db.releaseWrite(), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-t.C:
			waited += writeWaitWarning
			db.log.Warn("still waiting for the index write permit; "+
				"a database-level write from inside an open Writer batch cannot make progress",
				"path", db.path, "waited", waited)
		}
	}
}

func (db *DB) releaseWrite() func() {
	var once sync.Once
	return func() { once.Do(func() { <-db.wsem }) }
}

// writeTx runs fn inside one immediate transaction on the reserved writer
// connection, holding the write permit for its duration.
func (db *DB) writeTx(ctx context.Context, fn func(context.Context, *sql.Tx) error) error {
	release, err := db.acquireWrite(ctx)
	if err != nil {
		return err
	}
	defer release()
	return db.txOnWriter(ctx, fn)
}

func (db *DB) txOnWriter(ctx context.Context, fn func(context.Context, *sql.Tx) error) error {
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

// Reset drops every table and re-applies the schema on the live handle. It is
// the in-process half of FR-IDX-005 and touches nothing outside index.db — the
// attached user.db is not part of the transaction and not part of the DDL.
func (db *DB) Reset(ctx context.Context) error {
	err := db.writeTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, dropAllSQL); err != nil {
			return fmt.Errorf("dropping index tables: %w", err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	db.log.Info("index rebuilt from empty", "path", db.path)
	return db.migrate(ctx)
}

// DBFiles lists every file that belongs to an index.db, as a hard-coded suffix
// allowlist. arch-backend §3.7: the rebuild deletes these and nothing else, and
// it is never expressed as a glob.
func DBFiles(indexPath string) []string {
	return []string{indexPath, indexPath + "-wal", indexPath + "-shm"}
}

// Destroy removes index.db and its WAL sidecars. It is the offline half of
// FR-IDX-005, called by `shelf --rebuild-index` before the server starts. It
// never looks at user.db, so reading progress is untouched (AC-006).
func Destroy(indexPath string) error {
	abs, err := filepath.Abs(indexPath)
	if err != nil {
		return fmt.Errorf("resolving index path: %w", err)
	}
	for _, f := range DBFiles(abs) {
		if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("removing %s: %w", filepath.Base(f), err)
		}
	}
	return nil
}

// buildDSN renders the connection string of arch-backend §3.5.
//
// One deliberate deviation from the spelling in §3.5: `_txlock` is **deferred**,
// not `immediate`. `BEGIN IMMEDIATE` starts a write transaction on *every*
// attached database, so an index connection with user.db attached would hold
// user.db's WAL writer lock for the whole of a scan batch and every
// `PUT /api/books/{bid}/progress` in that window would fail with SQLITE_BUSY.
// That is not hypothetical — it is what the storage tests reproduce.
//
// Deferred loses nothing here. Every write transaction in this package issues a
// write as its first statement (NextScanGen is written as an upsert with
// RETURNING precisely so that this holds), so the write lock on `main` is taken
// at BEGIN+1 and the busy handler can still retry it; there is no read-then-
// write upgrade anywhere, which is the one case a busy timeout cannot rescue.
func buildDSN(absPath string, busy time.Duration) string {
	q := url.Values{}
	// busy_timeout is applied first by the driver regardless of order here.
	q.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", busy.Milliseconds()))
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Set("_txlock", "deferred")
	return "file:" + uriPath(absPath) + "?" + q.Encode()
}

// uriPath renders an absolute filesystem path as the path component of a
// SQLite file: URI. Windows paths ("C:\x") become "/C:/x"; percent-escaping is
// left to url.URL so spaces and '#' survive.
func uriPath(absPath string) string {
	p := filepath.ToSlash(absPath)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	u := url.URL{Path: p}
	return "//" + u.EscapedPath()
}

// verifyJournalMode asserts NFR-DAT-003. WAL is a persistent property of the
// file, so a database created by an older, misconfigured build would keep its
// old mode; failing loudly beats silently losing crash safety.
func verifyJournalMode(ctx context.Context, sqldb *sql.DB) error {
	var mode string
	if err := sqldb.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		return fmt.Errorf("reading journal mode: %w", err)
	}
	if !strings.EqualFold(mode, "wal") {
		return fmt.Errorf("index: journal_mode is %q, want wal (NFR-DAT-003)", mode)
	}
	return nil
}

// appVersion is recorded in meta so a support dump says which build wrote the
// index.
func appVersion() string { return buildinfo.Version }
