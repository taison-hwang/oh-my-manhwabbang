// Package app is the composition root: the one place that knows every other
// package exists.
//
// Nothing here implements a requirement on its own. What it owns is the *order*
// in which seventeen packages are brought up and taken down, and that order is
// itself a requirement — arch-backend §6.3, NFR-OPS-006:
//
//  1. load and validate the configuration                      (cmd/shelf)
//  2. create data_dir, cache_dir and the three cache kinds
//  3. --rebuild-index: delete index.db and its two sidecars, and nothing else
//  4. open user.db, migrate; open index.db, migrate, ATTACH user.db;
//     refuse to start on an unknown or newer schema_version / id_version
//  5. reconcile the `roots` table with the configuration
//  6. build the archive pool, the sources, the thumbnail service, the scanner
//     and the HTTP handler
//  7. LISTEN AND SERVE — the library is answered from the index that is
//     already on disk, before any scan starts. This is the whole of
//     NFR-OPS-006: after `kill -9`, the next start serves immediately.
//  8. only then, if scan.on_start *or* --rebuild-index, kick off the
//     background scan
//  9. SIGINT/SIGTERM: cancel the scan, Server.Shutdown within shutdown_grace,
//     close the pools, close both databases, checkpoint both WALs
//
// # Why serving comes before scanning
//
// A cold scan of the reference collection is 32 s and a warm one is under 30 s
// (NFR-PRF-004), but a library on a spun-down NAS can be minutes. The index is
// the durable artefact; the scan only reconciles it with the disk. Serving
// first means a restart is invisible to a reader, and a scan that fails
// entirely — unmounted drive, permissions — still leaves a working library
// instead of an empty one.
//
// # What is deliberately tolerant
//
// A root that cannot be opened is logged, not fatal (arch §4.9): one unmounted
// drive must not take the rest of the library down. A missing SPA build serves
// a placeholder rather than 404s. A scanner that fails to start leaves the
// server running and reports the failure through GET /api/scan/status.
package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"shelf/internal/auth"
	"shelf/internal/config"
	"shelf/internal/httpapi"
	"shelf/internal/index"
	"shelf/internal/openpool"
	"shelf/internal/pdfium"
	"shelf/internal/scanner"
	"shelf/internal/source"
	"shelf/internal/thumbs"
	"shelf/internal/userdata"

	_ "modernc.org/sqlite" // the driver both storage packages open; also used by checkpoint.go
)

// cacheKinds are the subdirectories arch §6.3 step 2 creates up front. Creating
// them at startup rather than lazily means an unwritable cache_dir is a
// start-up failure with a path in the message, not a warning buried in the
// first thumbnail request.
var cacheKinds = []string{"thumbs", "pdf", "wazero"}

// Options configures [New]. Only Config is required.
type Options struct {
	// Config is the loaded, validated configuration. Required.
	Config *config.Config

	// Logger is the process logger. nil selects slog.Default().
	Logger *slog.Logger

	// Static is the embedded SPA, normally web.Dist(). nil makes the server
	// answer a "run `make web`" placeholder instead of a 404 storm (arch §2.1).
	Static fs.FS

	// RebuildIndex deletes index.db and its WAL sidecars before opening
	// anything, then forces the first scan to be full (FR-IDX-005). user.db is
	// never touched, which is the whole of AC-006.
	RebuildIndex bool

	// StartedAt is the process start, reported by GET /api/health. Zero selects
	// time.Now().
	StartedAt time.Time

	// Listener overrides the socket. Zero listens on server.listen:server.port.
	// Tests pass a :0 listener so they can run in parallel.
	Listener net.Listener

	// wrapBooks wraps the BookLister the scanner is handed. It is unexported on
	// purpose: it is a seam for this package's own tests and cannot be set from
	// outside. NFR-OPS-006 is a claim about *order*, and an order is only
	// observable while the later step is still running — so the test that proves
	// Serve precedes the scan holds the scan open by blocking the one call the
	// scanner makes per book. Production leaves it nil and the scanner gets the
	// factory itself.
	wrapBooks func(scanner.BookLister) scanner.BookLister
}

// App is a fully wired server. Build it with [New], run it with [Run], and call
// [App.Close] exactly once when Run returns.
type App struct {
	cfg *config.Config
	log *slog.Logger

	user  *userdata.DB
	idx   *index.DB
	roots *source.RootSet
	pool  *openpool.Pool
	pdf   *pdfium.Renderer
	src   *source.Factory
	thumb *thumbs.Service
	scan  *scanner.Scanner
	api   *httpapi.Server
	http  *http.Server
	ln    net.Listener

	rebuilt bool
	started time.Time
	closed  bool
}

// New performs steps 2 to 6 of arch §6.3 and binds the listener. It does not
// serve; [App.Run] does. Splitting the two is what lets a test know the port
// before a single request is possible, and it is why a startup failure exits
// with a message rather than a half-open socket.
//
// Every error returned here is fatal. On failure nothing is left open: the
// partially built App closes itself before returning.
func New(ctx context.Context, opts Options) (a *App, err error) {
	if opts.Config == nil {
		return nil, errors.New("app: Options.Config is required")
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	started := opts.StartedAt
	if started.IsZero() {
		started = time.Now()
	}

	a = &App{cfg: opts.Config, log: log, started: started, rebuilt: opts.RebuildIndex}
	// Any failure below leaves half a process behind unless it is unwound here.
	defer func() {
		if err != nil {
			_ = a.Close()
			a = nil
		}
	}()

	// ---- step 2: our own directories ------------------------------------
	if err = a.makeDirs(); err != nil {
		return nil, err
	}

	// The configuration file as this process is about to run it. It is taken
	// here, before anything can edit it, because that is the only moment the
	// bytes this server actually loaded are still on disk unambiguously —
	// `POST /api/roots` may change them minutes later (amendment A-11).
	// `Settings.server.config_changed_on_disk` compares against this digest.
	//
	// A failure to read is not fatal and never has been: the file was already
	// parsed by the time we get here, so an unreadable one now means it moved or
	// its permissions changed between then and now, and the answer to that is to
	// report "changed" from the settings endpoint — not to refuse to start.
	// Nothing else in this function moves: `source.OpenRoots` and
	// `reconcileRoots` are untouched, which is the whole point of
	// "restart-based".
	var configDigest string
	if path := a.cfg.AbsFilePath(); path != "" {
		state, derr := config.ReadFileState(path)
		if derr != nil {
			log.Warn("the configuration file could not be digested at load; "+
				"the settings screen will report it as changed", "path", path, "err", derr)
		} else {
			configDigest = state.Digest
		}
	}

	// ---- step 3: --rebuild-index (FR-IDX-005) ---------------------------
	indexPath := filepath.Join(a.cfg.Storage.DataDir, "index.db")
	userPath := filepath.Join(a.cfg.Storage.DataDir, "user.db")
	if opts.RebuildIndex {
		// index.Destroy walks a hard-coded three-entry allowlist
		// (index.DBFiles), never a glob. user.db is not in it.
		if err = index.Destroy(indexPath); err != nil {
			return nil, fmt.Errorf("rebuilding the index: %w", err)
		}
		log.Info("index deleted; a full scan will rebuild it",
			"path", indexPath, "user_db_untouched", userPath)
	}

	// ---- step 4: the two databases --------------------------------------
	// user.db first and always: index.Open ATTACHes it and verifies its schema,
	// and it is the file that must never be lost (NFR-DAT-004).
	if a.user, err = userdata.Open(ctx, userdata.Options{Path: userPath, Logger: log}); err != nil {
		return nil, fmt.Errorf("opening user data: %w", err)
	}
	if a.idx, err = index.Open(ctx, index.Options{
		Path: indexPath, UserPath: userPath, Logger: log,
	}); err != nil {
		return nil, fmt.Errorf("opening the index: %w", err)
	}

	// ---- step 5: reconcile roots ----------------------------------------
	if err = a.reconcileRoots(ctx); err != nil {
		return nil, err
	}

	// ---- step 6: pools, sources, thumbnails, scanner, HTTP ---------------
	if a.roots, err = source.OpenRoots(ctx, a.cfg.Roots, log); err != nil {
		return nil, fmt.Errorf("opening roots: %w", err)
	}
	a.pool = openpool.New(openpool.Options{Open: a.roots.PoolOpener(), Logger: log})
	a.pdf = pdfium.New(pdfium.Options{
		Workers:  a.cfg.PDF.Workers,
		CacheDir: filepath.Join(a.cfg.Storage.CacheDir, "wazero"),
		Logger:   log,
	})
	a.src = source.NewFactory(source.Options{
		Roots:       a.roots,
		Pool:        a.pool,
		PDF:         a.pdf,
		PDFWidth:    a.cfg.PDF.DefaultWidth,
		PDFMaxWidth: a.cfg.PDF.MaxWidth,
		PDFQuality:  a.cfg.Thumbnails.Quality,
		Logger:      log,
	})

	topts := thumbs.FromConfig(a.cfg)
	topts.Index, topts.Sources, topts.Roots, topts.Logger = a.idx, a.src, a.roots, log
	if a.thumb, err = thumbs.New(ctx, topts); err != nil {
		return nil, fmt.Errorf("starting the thumbnail service: %w", err)
	}

	covers := scanner.CoverQueue(nil)
	if a.cfg.Thumbnails.CoverFirst {
		covers = newCoverBridge(a.thumb)
	}
	books := scanner.BookLister(a.src)
	if opts.wrapBooks != nil {
		books = opts.wrapBooks(books)
	}
	if a.scan, err = scanner.New(scanner.Options{
		Index:       a.idx,
		Books:       books,
		Roots:       a.roots,
		ConfigRoots: a.cfg.Roots,
		Scan:        a.cfg.Scan,
		Covers:      covers,
		Seen:        a.user, // amendment A-8: first_seen_at lives in user.db
		Logger:      log,
	}); err != nil {
		return nil, fmt.Errorf("building the scanner: %w", err)
	}

	var authn *auth.Authenticator
	if authn, err = buildAuth(a.cfg, log); err != nil {
		return nil, err
	}
	if a.api, err = httpapi.New(httpapi.Options{
		Config:       a.cfg,
		Index:        a.idx,
		UserData:     a.user,
		Scanner:      a.scan,
		Thumbs:       a.thumb,
		Sources:      a.src,
		Roots:        a.roots,
		Pool:         a.pool,
		Auth:         authn,
		Static:       opts.Static,
		Logger:       log,
		StartedAt:    started,
		ConfigDigest: configDigest,
	}); err != nil {
		return nil, fmt.Errorf("building the HTTP server: %w", err)
	}

	a.http = &http.Server{
		Handler:           a.api,
		ReadHeaderTimeout: a.cfg.Server.ReadHeaderTimeout,
		// There is deliberately no WriteTimeout: a 1.34 GB archive's page
		// stream, or a cold pdfium render, must not be cut off mid-image
		// (arch §3.2).
		ErrorLog: slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

	// ---- the socket ------------------------------------------------------
	if a.ln = opts.Listener; a.ln == nil {
		addr := net.JoinHostPort(a.cfg.Server.Listen, fmt.Sprint(a.cfg.Server.Port))
		var lc net.ListenConfig
		if a.ln, err = lc.Listen(ctx, "tcp", addr); err != nil {
			return nil, fmt.Errorf("listening on %s: %w", addr, err)
		}
	}
	return a, nil
}

// makeDirs is step 2. config.Load has already created and write-checked
// data_dir and cache_dir; this adds the three cache kinds so that a purge, a
// usage walk and the wazero compilation cache all have somewhere to be.
func (a *App) makeDirs() error {
	dirs := []string{a.cfg.Storage.DataDir, a.cfg.Storage.CacheDir}
	for _, k := range cacheKinds {
		dirs = append(dirs, filepath.Join(a.cfg.Storage.CacheDir, k))
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return fmt.Errorf("creating %s: %w", d, err)
		}
	}
	return nil
}

// reconcileRoots is step 4 of arch §6.3: the configuration is the source of
// truth for which roots exist, and the index remembers what it has seen.
//
// New roots are inserted, changed paths/labels/enabled flags are updated, and a
// root that has left the configuration is **logged and left alone**. Deleting it
// would delete its series and books, and with them nothing the user authored —
// but it would also make a typo in the YAML silently destructive, and arch §4.9
// is explicit that absence from one run is never evidence of absence on disk.
// `shelf migrate-root` (arch §3.4, phase 3) is the tool that would clean it up.
func (a *App) reconcileRoots(ctx context.Context) error {
	known, err := a.idx.ListRoots(ctx)
	if err != nil {
		return fmt.Errorf("reading known roots: %w", err)
	}
	configured := make(map[string]struct{}, len(a.cfg.Roots))
	for _, r := range a.cfg.Roots {
		configured[r.Name] = struct{}{}
		if err := a.idx.UpsertRoot(ctx, index.Root{
			Name: r.Name, Path: r.Path, Label: r.Label, Enabled: r.Enabled,
		}); err != nil {
			return fmt.Errorf("reconciling root %q: %w", r.Name, err)
		}
	}
	for _, k := range known {
		if _, ok := configured[k.Name]; ok {
			continue
		}
		a.log.Warn("a root in the index is no longer in the configuration; "+
			"its series stay indexed and its reading progress is intact, but it is not scanned or listed",
			"root", k.Name, "series", k.SeriesCount, "books", k.BookCount)
	}
	return nil
}

// buildAuth turns the optional `auth:` block into an Authenticator (NFR-SEC-002).
// No block means no password, which is the default deployment (ruling E-8) and
// not an error.
func buildAuth(cfg *config.Config, log *slog.Logger) (*auth.Authenticator, error) {
	opts, err := auth.FromConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("configuring authentication: %w", err)
	}
	opts.Logger = log
	a, err := auth.New(opts)
	if err != nil {
		return nil, fmt.Errorf("configuring authentication: %w", err)
	}
	if cfg.AuthEnabled() {
		log.Info("authentication is enabled", "session_key", cfg.Auth.SessionKeyFile)
	}
	return a, nil
}

// Addr is the address the server is listening on, with the port resolved. Tests
// that asked for :0 read it here.
func (a *App) Addr() string {
	if a.ln == nil {
		return ""
	}
	return a.ln.Addr().String()
}

// Handler is the whole HTTP surface, base path and all. Exposed for tests that
// want httptest rather than a socket.
func (a *App) Handler() http.Handler { return a.api }

// BaseURL is where a browser should be pointed, including base_path.
func (a *App) BaseURL() string {
	return "http://" + a.Addr() + a.cfg.Server.BasePath + "/"
}

// Run is steps 7 to 9: serve until ctx is cancelled, then shut down gracefully.
//
// It returns nil on a clean shutdown. The caller must still call [App.Close].
func (a *App) Run(ctx context.Context) error {
	a.log.Info("serving",
		"addr", a.Addr(),
		"base_path", a.cfg.Server.BasePath,
		"roots", len(a.cfg.Roots),
		"data_dir", a.cfg.Storage.DataDir,
		"cache_dir", a.cfg.Storage.CacheDir,
		"auth", a.cfg.AuthEnabled(),
		"pdf", a.cfg.PDF.Enabled && pdfium.Supported())

	serveErr := make(chan error, 1)
	go func() {
		err := a.http.Serve(a.ln)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serveErr <- err
	}()

	// ---- step 8: the background scan ------------------------------------
	//
	// Started only after Serve is running, and never waited on: NFR-OPS-006 is
	// that the library answers from the index that is already on disk. The scan
	// context is the process context, so a shutdown cancels it and the scanner
	// commits what it has (arch §4.1).
	//
	// `a.rebuilt` forces the scan even when scan.on_start is false. FR-IDX-005
	// is that --rebuild-index *rebuilds*: step 3 has already deleted index.db,
	// so deferring the rebuild to a config flag would leave the operator with an
	// empty library and a log line promising a scan that never came. The two
	// halves of the flag belong together.
	if a.cfg.Scan.OnStart || a.rebuilt {
		if _, err := a.scan.Start(ctx, scanner.Request{Full: a.rebuilt}); err != nil {
			// Not fatal. A library that cannot be rescanned is still readable,
			// and POST /api/scan lets an operator retry once the cause is fixed.
			a.log.Error("the start-up scan could not be started", "err", err)
		}
	}

	select {
	case err := <-serveErr:
		if err != nil {
			return fmt.Errorf("serving: %w", err)
		}
		return nil
	case <-ctx.Done():
	}

	// ---- step 9: graceful shutdown --------------------------------------
	a.log.Info("shutting down", "grace", a.cfg.Server.ShutdownGrace)
	if a.scan.Cancel() {
		a.log.Info("cancelling the running scan")
	}

	grace := a.cfg.Server.ShutdownGrace
	if grace <= 0 {
		grace = 10 * time.Second
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), grace)
	defer cancel()

	err := a.http.Shutdown(sctx)
	if err != nil {
		// The grace period expired with requests still in flight. Close is
		// brutal but bounded, which is what a shutdown must be.
		a.log.Warn("the shutdown grace period expired; closing connections", "err", err)
		_ = a.http.Close()
	}
	<-serveErr
	return nil
}

// Close releases everything New acquired, in reverse order, and checkpoints both
// write-ahead logs (arch §6.3 step 7). It is idempotent.
//
// The order matters. The scanner is stopped first because it holds the index
// write connection; the thumbnail service next because its workers read through
// the sources; then the sources, the pool and the roots; then the databases,
// which nothing may still be using; and only then the WAL checkpoint, which
// needs to be the last connection to each file.
func (a *App) Close() error {
	if a == nil || a.closed {
		return nil
	}
	a.closed = true
	var errs []error
	add := func(what string, err error) {
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", what, err))
		}
	}

	if a.scan != nil {
		add("closing the scanner", a.scan.Close())
	}
	if a.thumb != nil {
		add("closing the thumbnail service", a.thumb.Close())
	}
	if a.pdf != nil {
		add("closing the PDF renderer", a.pdf.Close())
	}
	if a.pool != nil {
		add("closing the archive pool", a.pool.Close())
	}
	if a.roots != nil {
		add("closing the roots", a.roots.Close())
	}
	if a.ln != nil && a.http == nil {
		// Only when New failed before the server was built; otherwise
		// http.Server.Serve owns the listener and has already closed it.
		add("closing the listener", a.ln.Close())
	}

	indexPath, userPath := "", ""
	if a.idx != nil {
		indexPath = a.idx.Path()
		add("closing the index", a.idx.Close())
	}
	if a.user != nil {
		userPath = a.user.Path()
		add("closing user data", a.user.Close())
	}
	for _, p := range []string{indexPath, userPath} {
		if p == "" {
			continue
		}
		if err := checkpointWAL(p); err != nil {
			// A failed checkpoint costs recovery time on the next start and
			// nothing else — the WAL is still a complete, replayable record.
			a.log.Warn("could not checkpoint the write-ahead log", "path", p, "err", err)
		}
	}
	return errors.Join(errs...)
}

// checkpointWAL is arch §6.3 step 7's `PRAGMA wal_checkpoint(TRUNCATE)`.
//
// It runs on a fresh connection *after* the pool has been closed, which is the
// only moment this process is guaranteed to be the sole writer — a checkpoint
// issued while readers hold the file can only ever be partial. Closing the last
// connection already checkpoints and removes the WAL in ordinary SQLite; doing
// it explicitly makes the guarantee independent of that behaviour and leaves a
// diagnosable error when the file cannot be written at all.
func checkpointWAL(path string) error {
	if _, err := os.Stat(path); err != nil {
		return nil // never opened, or already gone
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return fmt.Errorf("reopening %s: %w", filepath.Base(path), err)
	}
	defer func() { _ = db.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var busy, logFrames, checkpointed int
	if err := db.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).
		Scan(&busy, &logFrames, &checkpointed); err != nil {
		return fmt.Errorf("checkpointing %s: %w", filepath.Base(path), err)
	}
	if busy != 0 {
		return fmt.Errorf("checkpointing %s: the database was busy", filepath.Base(path))
	}
	return nil
}
