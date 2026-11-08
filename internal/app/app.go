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
	"shelf/internal/principal"
	"shelf/internal/repair"
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

	// PerUser is true for auth.mode "oidc": progress, preferences and settings
	// are kept per user, and the cover rule ignores both (ruling E-74 §2.13).
	// cmd/shelf does not set it yet; the account wiring (U-B) will.
	PerUser bool

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

	pendingRelocations []index.Relocation
	rebuilt            bool
	perUser            bool
	started            time.Time
	closed             bool
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

	a = &App{cfg: opts.Config, log: log, started: started, rebuilt: opts.RebuildIndex, perUser: opts.PerUser}
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

	// ---- step 5a: reattach progress the index has re-shaped --------------
	// Between step 4 and the first scan the index is whatever the last run left,
	// which is exactly the state a progress row orphaned by a container split
	// needs to be read against (internal/repair). Every start, not once behind a
	// flag: a rung that has already run finds nothing and costs two statements,
	// while a flag would have to be right about a scan that had not happened
	// yet. A split performed by *this* run's scan is repaired on the next start.
	//
	// Logged, not fatal, for the reason the package comment gives for an
	// unopenable root: a library that serves with 20 series reading 0 % is worth
	// more than one that refuses to start. The repair is idempotent, so the next
	// start tries again.
	a.forEachScope(ctx, log, func(c context.Context) bool { a.repairSplitProgress(c, log); return true })
	a.forEachScope(ctx, log, func(c context.Context) bool { a.refileMisfiledProgress(c, log); return true })

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
		covers = newCoverBridge(a.thumb, a.idx, a.user, a.cfg.Reader.ReadingDirection, a.perUser)
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
		// A scan is the moment the index learns that a book was renamed, moved
		// or split, so it is the moment reading progress can be put back on it.
		// Running the repair here is what makes "just rescan" true: without it
		// the operator has to restart, and the only visible symptom in the
		// meantime is a series reading 0 % for no reason they can see.
		AfterScan: func(ctx context.Context, res *scanner.Result) {
			if bridge, ok := covers.(*coverBridge); ok {
				defer bridge.Refresh(ctx)
			}
			// Evidence first, inference second. carryProgress acts on moves the
			// scan proved by content hash; repairSplitProgress then sweeps up
			// whatever is still orphaned, including rows whose proof was
			// destroyed by a sweep that ran before any of this existed.
			//
			// The purge runs only when every step before it succeeded. It
			// deletes whatever those steps did not explain, so a step that
			// failed — a carry whose write rolled back, a lookup that errored —
			// would hand it rows that were explained and only failed to land,
			// and a transient error would destroy a moved book's progress,
			// cover and prefs (U1 codex cycle 1, finding 2). Each step still
			// runs; they write, and none of them deletes.
			explained := a.carryProgressAcrossRelocations(ctx, res, log)
			explained = a.forEachScope(ctx, log, func(ctx context.Context) bool {
				return a.repairSplitProgress(ctx, log)
			}) && explained
			explained = a.forEachScope(ctx, log, func(ctx context.Context) bool {
				return a.refileMisfiledProgress(ctx, log)
			}) && explained
			if !explained {
				log.Warn("not deleting anything for missing books this run: an earlier cleanup step failed, " +
					"and a row it would have moved looks the same as a vanished one")
				return
			}
			a.purgeVanishedProgress(ctx, res, log)
		},
		Logger: log,
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
		PerUser:      a.perUser,
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

// forEachScope runs one cleanup step against the single-user tables and then
// against each user's rows (ruling E-74 §2.13). The index and userdata read the
// scope from the context's principal, so a user's pass is the same code under
// that user's identity. The first pass runs with the principal cleared: a scan
// started by a request carries that request's context, and without clearing it
// the single-user tables would be skipped and the caller's rows cleaned twice. It reports whether every pass succeeded.
//
// Listing the users can fail; that counts as a failed step, which stops the
// purge for this run, so a user's rows are never judged by a scan that could
// not look at them.
func (a *App) forEachScope(ctx context.Context, log *slog.Logger, step func(context.Context) bool) bool {
	scopes, err := a.scopes(ctx)
	ok := true
	for _, sc := range scopes {
		ok = step(sc) && ok
	}
	if err != nil {
		log.Warn("could not list users to clean up their reading progress; the next scan will try again",
			"error", err)
		return false
	}
	return ok
}

// scopes is the single-user scope (principal cleared) followed by one scope per
// user. When the users cannot be listed it returns the single-user scope alone
// with the error; a caller that must see every user stops on the error.
func (a *App) scopes(ctx context.Context) ([]context.Context, error) {
	ctx = principal.With(ctx, principal.Principal{})
	out := []context.Context{ctx}
	subs, err := a.user.UserSubs(ctx)
	if err != nil {
		return out, err
	}
	for _, sub := range subs {
		out = append(out, principal.With(ctx, principal.Principal{Sub: sub}))
	}
	return out, nil
}

// carryProgressAcrossRelocations moves reading progress onto the new id of every
// book this scan proved had merely moved — and, since ruling E-69 §2.11(a)(e)
// and §2.12, the book's chosen cover, its viewer preferences and (for a
// series-level 1:1 move) the series' reading direction, in one transaction,
// whether or not the book was ever read.
//
// This is the mechanism a rename *should* go through, and the reason it lives
// here rather than in the scanner is arch §3.7: internal/scanner cannot write to
// user.db, and no transaction may span both databases. The scan observes and
// reports (index.Relocation); this decides what the observation means for a
// reader; userdata performs the write.
//
// It runs before the orphan repair because it is the better evidence. A pairing
// on content hash needs no opinion about filenames; the path rules downstream
// exist only for rows whose old index entry was already swept away, which is a
// backlog and not a mechanism.
//
// It reports false when a lookup or the write failed, so the purge of the same
// run can stand down (see the AfterScan hook). A pairing it declined is not a
// failure.
func (a *App) carryProgressAcrossRelocations(ctx context.Context, res *scanner.Result, log *slog.Logger) (success bool) {
	if res == nil {
		return true
	}
	var fresh []index.Relocation
	for _, rr := range res.Roots {
		fresh = append(fresh, rr.Relocations...)
	}
	pending, err := a.user.PendingRelocations(ctx)
	var relocs []index.Relocation
	for _, p := range pending {
		relocs = append(relocs, index.Relocation(p))
	}
	relocs = append(relocs, a.pendingRelocations...)
	relocs = append(relocs, fresh...)
	seen := map[string]bool{}
	unique := relocs[:0]
	for _, p := range relocs {
		if !seen[p.OldBookID] {
			unique = append(unique, p)
			seen[p.OldBookID] = true
		}
	}
	relocs = unique
	defer func() {
		if success {
			a.pendingRelocations = nil
			return
		}
		a.pendingRelocations = append([]index.Relocation(nil), relocs...)
		pairs := make([]userdata.PendingRelocation, 0, len(relocs))
		for _, p := range relocs {
			pairs = append(pairs, userdata.PendingRelocation(p))
		}
		if err := a.user.SavePendingRelocations(ctx, pairs); err != nil {
			log.Warn("keeping relocation evidence in memory: persistence failed", "error", err)
		}
	}()
	if err != nil {
		log.Warn("could not read pending relocation evidence; skipping deletion", "error", err)
		return false
	}
	if len(relocs) == 0 {
		return true
	}
	oldIDsToClear := repair.RelocationIDs(relocs)
	available := relocs[:0]
	for _, p := range relocs {
		b, err := a.idx.GetBook(ctx, p.NewBookID)
		if errors.Is(err, index.ErrNotFound) {
			continue
		}
		if err != nil {
			return false
		}
		p.NewRootName, p.NewRelPath, p.NewSeriesID, p.NewPageCount = b.RootName, b.RelPath, b.SeriesID, int(b.PageCount)
		available = append(available, p)
	}
	relocs = available
	defer func() {
		if success {
			if err := a.user.ClearPendingRelocations(ctx, oldIDsToClear); err != nil {
				log.Warn("could not retire relocation evidence", "error", err)
				success = false
			}
		}
	}()

	// The same proof is applied to the single-user tables and then to every
	// user's rows (ruling E-74 §2.13): a rename moves each reader's place. The
	// covers and series directions are shared, so the first pass carries them
	// and the later ones find nothing left to do.
	carryOnce := func(ctx context.Context) bool {
		// Every table filed under the old ids, not only progress (ruling E-69
		// §2.12): a book nobody read can still carry a direction or a cover.
		oldIDs := repair.RelocationIDs(relocs)
		existing, err := a.user.GetProgressMany(ctx, oldIDs)
		if err != nil {
			log.Warn("could not check which relocated books had been read; "+
				"reading progress is unchanged", "error", err)
			return false
		}
		covers, err := a.user.GetCovers(ctx, oldIDs)
		if err != nil {
			log.Warn("could not check which relocated books had a chosen cover; "+
				"user.db is unchanged", "error", err)
			return false
		}
		seriesNow, err := a.idx.SeriesAt(ctx, repair.RelocationSeriesIDs(relocs))
		if err != nil {
			log.Warn("could not check which series the relocated books left; "+
				"user.db is unchanged", "error", err)
			return false
		}
		carry, skipped := repair.PlanCarry(relocs, existing, covers, seriesNow)
		for _, s := range skipped {
			log.Warn("reading progress not carried across a move: the pairing does not add up "+
				"(the cover and preferences still follow it)",
				"old_book_id", s.OldBookID, "new_book_id", s.NewBookID,
				"reason", string(s.Reason), "baseline_pages", s.PageCount, "new_pages", s.NewPages)
		}
		r, err := a.user.ApplyCarry(ctx, carry)
		if err != nil {
			log.Warn("could not carry reading progress, covers and preferences across a move; "+
				"user.db is unchanged",
				"error", err, "planned_progress", len(carry.Progress), "declined", len(skipped))
			return false
		}
		if r == (userdata.CarryResult{}) {
			log.Info("books moved, none of them had progress, a cover or preferences to carry",
				"relocations", len(relocs), "declined", len(skipped))
			return true
		}
		log.Info("carried reading progress, covers and preferences across books the scan saw move",
			"relocations", len(relocs), "retired", r.Progress.Retired, "written", r.Progress.Written,
			"kept_newer_local", r.Progress.Kept,
			"covers_written", r.Covers.Written, "covers_kept", r.Covers.Kept,
			"prefs_written", r.Prefs.Written, "prefs_kept", r.Prefs.Kept,
			"series_prefs_written", r.Series.Written, "series_prefs_kept", r.Series.Kept,
			"declined", len(skipped))
		return true
	}
	return a.forEachScope(ctx, log, carryOnce)
}

// purgeVanishedProgress deletes reading history for books a completed walk of
// their root did not find, together with their chosen covers and viewer
// preferences, and the reading direction of series such a walk did not find
// (ruling E-69 §2.11(d)(e)).
//
// **This is the only thing in the server that destroys authored data**, so its
// guard is the whole design. It runs only from a scan — never at startup, where
// nothing has been walked and every absence is unexplained — and only for roots
// the scanner was willing to sweep. That willingness is not a proxy:
// `scanner.decideSweep` withholds it when the root failed to enumerate, when the
// scan was cancelled, and when the run only covered named series, which are
// exactly the three ways an absence can mean "not looked at" instead of "not
// there". A root whose scan was skipped keeps every row it has.
//
// It also runs last. Anything that could be carried across a move, reattached to
// a split, or refiled has already been by now, so a row reaching this point has
// been offered every explanation the system has and matched none of them.
//
// Every deletion is logged with its path and the page the reader was on. A purge
// that prints a count is indistinguishable from one that took the wrong rows,
// and this one cannot be undone from inside the product.
func (a *App) purgeVanishedProgress(ctx context.Context, res *scanner.Result, log *slog.Logger) {
	if res == nil || res.Cancelled {
		return
	}
	var swept []string
	for _, rr := range res.Roots {
		if rr.Err == nil && rr.SweepNote == "" {
			swept = append(swept, rr.Name)
		} else if rr.SweepNote != "" {
			log.Info("keeping reading progress for a root this run did not fully walk",
				"root", rr.Name, "reason", rr.SweepNote)
		}
	}
	if len(swept) == 0 {
		return
	}
	failed := func(err error) {
		log.Warn("could not check for reading progress, covers or preferences whose book is gone; "+
			"nothing was deleted and the next scan will try again", "error", err)
	}

	// The ids are collected from the tables that record where a book was —
	// progress and book_covers, each with root_name and book_path (ruling E-69
	// §2.11(d)): a book can have a cover and no progress at all. Each id found
	// is removed from all three tables, its book_prefs included.
	//
	// book_prefs has no root column, so a prefs row with neither a progress nor
	// a cover row beside it cannot say which root it was in, and it is never
	// deleted here (§2.11(d), revised after U1 codex cycle 1). "Every root the
	// index knows was walked" is not a substitute: a root taken out of the
	// configuration and an index rebuilt leave the index not knowing that root
	// at all, and A-11 promises adding it back reattaches everything. Such a
	// row is harmless, and a book that returns at the same path picks it up.
	// On a per-user server one book id has a progress row in the single-user
	// table and in each user's table, and one shared cover. The decision to
	// delete an id has to see all of them: a row in any scope that names a root
	// this run did not walk holds the id for everyone, and the shared cover
	// stays until every scope has been cleaned, because it is what proves the
	// deletion to a scope whose own rows say nothing (ruling E-74 §2.13).
	scopes, err := a.scopes(ctx)
	if err != nil {
		log.Warn("could not list users to check their reading progress; nothing was deleted and the next scan will try again",
			"error", err)
		return
	}
	goneCovers, err := a.idx.VanishedCovers(ctx, swept)
	if err != nil {
		failed(err)
		return
	}
	goneSeries, err := a.idx.VanishedSeriesPrefs(ctx, swept)
	if err != nil {
		failed(err)
		return
	}
	goneBy := make([][]index.Vanished, len(scopes))
	var candidates []string
	candidate := map[string]bool{}
	addCandidate := func(id string) {
		if !candidate[id] {
			candidate[id] = true
			candidates = append(candidates, id)
		}
	}
	for i, sc := range scopes {
		if goneBy[i], err = a.idx.VanishedProgress(sc, swept); err != nil {
			failed(err)
			return
		}
		for _, g := range goneBy[i] {
			addCandidate(g.BookID)
		}
	}
	for _, g := range goneCovers {
		addCandidate(g.BookID)
	}

	// One id can have rows that disagree. A split reattachment across roots
	// files the cover under the destination it chose and leaves the progress
	// row naming the orphan's root (repair.row), so one table can say "gone from
	// a root this run walked" while another names a root nobody walked — one
	// taken out of the configuration, or one whose sweep was refused. The purge
	// believes an id only when every row it has, in every scope, names a walked
	// root, and otherwise keeps all of them (U1 codex cycle 2). Keeping is the
	// safe error: a held row is harmless, a deleted one is gone.
	walked := make(map[string]bool, len(swept))
	for _, r := range swept {
		walked[r] = true
	}
	coverOf, err := a.user.GetCovers(ctx, candidates)
	if err != nil {
		failed(err)
		return
	}
	held := map[string]bool{}
	hold := func(id, scope string, p userdata.Progress, c userdata.Cover) {
		if held[id] {
			return
		}
		held[id] = true
		log.Info("keeping a missing book's progress, cover and preferences: one of its rows names a root this run did not walk end to end",
			"book_id", id, "scope", scope,
			"progress_root", p.RootName, "progress_book_path", p.BookPath,
			"cover_root", c.RootName, "cover_book_path", c.BookPath)
	}
	for _, sc := range scopes {
		progressOf, err := a.user.GetProgressMany(sc, candidates)
		if err != nil {
			failed(err)
			return
		}
		for _, id := range candidates {
			p, hasProgress := progressOf[id]
			c, hasCover := coverOf[id]
			if hasProgress && !walked[p.RootName] || hasCover && !walked[c.RootName] {
				hold(id, scopeName(sc), p, c)
			}
		}
	}

	var books []string
	seen := map[string]struct{}{}
	add := func(id string) bool {
		if _, dup := seen[id]; dup || held[id] {
			return false
		}
		seen[id] = struct{}{}
		books = append(books, id)
		return true
	}
	for i, rows := range goneBy {
		for _, g := range rows {
			if add(g.BookID) {
				log.Info("deleting reading progress: the file is not in a root this run walked end to end",
					"scope", scopeName(scopes[i]), "root", g.RootName, "book_path", g.BookPath, "book_id", g.BookID,
					"last_page", g.LastPage, "page_count", g.PageCount)
			}
		}
	}
	for _, g := range goneCovers {
		if add(g.BookID) {
			log.Info("deleting a chosen cover: the file is not in a root this run walked end to end",
				"root", g.RootName, "book_path", g.BookPath, "book_id", g.BookID,
				"page", g.Page, "page_count", g.PageCount)
		}
	}
	var series []string
	for _, g := range goneSeries {
		series = append(series, g.SeriesID)
		log.Info("deleting a series reading direction: the series is not in a root this run walked end to end",
			"root", g.RootName, "series_path", g.SeriesPath, "series_id", g.SeriesID)
	}
	if len(books) == 0 && len(series) == 0 {
		return
	}

	// Each user's rows first, the single-user table and the shared rows in the
	// last call. A failure part-way leaves the shared cover in place, so the
	// next scan reaches the same decision and finishes.
	var total userdata.PurgeResult
	for i := len(scopes) - 1; i >= 0; i-- {
		last := i == 0
		r, err := a.user.Purge(scopes[i], userdata.PurgeSet{Books: books, Series: series, KeepShared: !last})
		if err != nil {
			log.Warn("could not delete reading progress, covers or preferences for missing books; "+
				"the next scan will try again",
				"error", err, "books", len(books), "series", len(series))
			return
		}
		total.Progress += r.Progress
		total.Prefs += r.Prefs
		total.Covers += r.Covers
		total.Series += r.Series
	}
	log.Info("deleted reading progress, covers and preferences for books the filesystem no longer has",
		"progress_rows", total.Progress, "pref_rows", total.Prefs, "cover_rows", total.Covers,
		"series_pref_rows", total.Series, "roots_walked", len(swept))
}

func scopeName(ctx context.Context) string {
	if p, ok := principal.From(ctx); ok {
		return p.Sub
	}
	return "single-user"
}

// refileMisfiledProgress puts progress rows — and chosen covers (ruling E-69
// §2.11(c)) — back under the series their book is actually in.
//
// It runs last of the three, after any row this scan moved has been written, so
// a carry that got the series wrong is corrected in the same pass rather than a
// restart later. It is also the cheapest of the three on a healthy library: one
// equality test on a join, and nothing to do.
//
// Unlike the other two this one cannot decline. `books.series_id` is the index's
// own answer to which series a book is in, so there is no second candidate to
// weigh and nothing to be uncertain about — a disagreement is simply wrong and
// the correction is a lookup.
//
// It reports false when either refile failed (see the AfterScan hook).
func (a *App) refileMisfiledProgress(ctx context.Context, log *slog.Logger) bool {
	ok := a.refile(ctx, log, "reading progress", a.idx.MisfiledProgress, a.user.RefileProgress)
	// The same correction for chosen covers (ruling E-69 §2.11(c)).
	return a.refile(ctx, log, "chosen covers", a.idx.MisfiledCovers, a.user.RefileCovers) && ok
}

func (a *App) refile(ctx context.Context, log *slog.Logger, what string,
	find func(context.Context) ([]index.Misfiled, error),
	write func(context.Context, []userdata.Refile) (int, error)) bool {
	misfiled, err := find(ctx)
	if err != nil {
		log.Warn("could not check whether "+what+" is filed under the right series; "+
			"the library is unaffected and the next scan will try again", "error", err)
		return false
	}
	if len(misfiled) == 0 {
		return true
	}
	rows := make([]userdata.Refile, 0, len(misfiled))
	for _, m := range misfiled {
		rows = append(rows, userdata.Refile{BookID: m.BookID, SeriesID: m.ActualSeria})
	}
	n, err := write(ctx, rows)
	if err != nil {
		log.Warn("could not refile "+what+" onto the right series; "+
			"user.db is unchanged and the next scan will try again",
			"error", err, "rows", len(rows))
		return false
	}
	log.Info("refiled "+what+" onto the series its book belongs to",
		"rows", n, "seen", len(misfiled))
	return true
}

// repairSplitProgress reattaches reading progress that names a book the index
// has since split into volumes (internal/repair), and with it the chosen covers
// that pass the same gates on their own baseline and the container's viewer
// preferences (ruling E-69 §2.11(b), §2.12).
//
// It reports false when a lookup or the write failed (see the AfterScan hook);
// a declined row is not a failure. Start-up (step 5a) ignores the report:
// nothing is purged there.
//
// It counts every decline rather than only the successes: a repair that moved
// 20 of 23 rows and said nothing about the other three would be the same shape
// of check this codebase has been bitten by before, one that watches the part
// that works. The counts ride on the summary line, so a start with nothing to
// do stays one line long.
//
// Only two of the four declines get a line of their own, and the split is about
// whether a human can act on it. `container-gone` and `not-a-container` mean the
// index has never heard of the file — an older rename, a deleted archive, an
// unplugged drive — so they are permanent until the file comes back and would
// otherwise print the same 37 lines on this library at every single start.
// `length-mismatch` and `page-out-of-range` mean the container WAS found and the
// arithmetic still refused it, which is the case worth reading: it is either a
// file that changed length under a reader or a bug in the partition.
//
// Nothing here is fatal. Every failure path leaves user.db exactly as it was
// (the write is one transaction) and the next start tries again.
func (a *App) repairSplitProgress(ctx context.Context, log *slog.Logger) bool {
	orphans, err := a.idx.SplitOrphans(ctx)
	if err != nil {
		log.Warn("could not look for reading progress the index cannot resolve; "+
			"the library is unaffected and the next scan will try again", "error", err)
		return false
	}
	// Chosen covers go through the same gates against their own baseline
	// (ruling E-69 §2.11(b)), so they are looked for here too.
	coverOrphans, err := a.idx.CoverOrphans(ctx)
	if err != nil {
		log.Warn("could not look for chosen covers the index cannot resolve; "+
			"the library is unaffected and the next scan will try again", "error", err)
		return false
	}
	if len(orphans) == 0 && len(coverOrphans) == 0 {
		return true
	}
	roots, err := a.idx.RootNames(ctx)
	if err != nil {
		log.Warn("could not list roots to look for relocated books; "+
			"the library is unaffected and the next scan will try again", "error", err)
		return false
	}
	found, err := a.idx.BooksAt(ctx, append(repair.Candidates(orphans, roots),
		repair.CoverCandidates(coverOrphans, roots)...))
	if err != nil {
		log.Warn("could not look up where orphaned reading progress may have moved to; "+
			"the library is unaffected and the next scan will try again", "error", err)
		return false
	}
	moves, skipped := repair.Plan(orphans, roots, found)
	coverMoves, coverSkipped := repair.PlanCovers(coverOrphans, roots, found)
	carry := userdata.Carry{
		Progress: moves,
		Covers:   coverMoves,
		Prefs:    repair.PlanSplitPrefs(orphans, roots, found),
	}

	byReason := make(map[repair.SkipReason]int, 5)
	for _, s := range append(skipped, coverSkipped...) {
		byReason[s.Reason]++
		switch s.Reason {
		case repair.SkipLengthMismatch, repair.SkipPageOutOfRange, repair.SkipAmbiguous:
			log.Warn("reading progress or a chosen cover left alone: a destination was found but cannot be trusted",
				"book_path", s.BookPath, "book_id", s.BookID, "reason", string(s.Reason),
				"page", s.LastPage, "baseline_pages", s.PageCount, "destination_pages", s.DestPages)
		case repair.SkipNotAContainer, repair.SkipUnresolved:
			// Counted only: nothing on this machine can act on them.
		}
	}
	counts := []any{
		"orphans_seen", len(orphans),
		"cover_orphans_seen", len(coverOrphans),
		"unreachable_file_gone", byReason[repair.SkipUnresolved] + byReason[repair.SkipNotAContainer],
		"declined_ambiguous", byReason[repair.SkipAmbiguous],
		"declined_length_mismatch", byReason[repair.SkipLengthMismatch],
		"declined_page_out_of_range", byReason[repair.SkipPageOutOfRange],
	}

	if len(carry.Progress) == 0 && len(carry.Covers) == 0 {
		log.Info("orphaned reading progress found, none of it reattachable", counts...)
		return true
	}
	res, err := a.user.ApplyCarry(ctx, carry)
	if err != nil {
		log.Warn("could not reattach orphaned reading progress; "+
			"user.db is unchanged and the next scan will try again",
			append([]any{"error", err, "planned", len(moves), "planned_covers", len(coverMoves)}, counts...)...)
		return false
	}
	log.Info("reattached reading progress to the books it belongs to",
		append([]any{"retired", res.Progress.Retired, "written", res.Progress.Written,
			"kept_newer_local", res.Progress.Kept,
			"covers_written", res.Covers.Written, "covers_kept", res.Covers.Kept,
			"prefs_written", res.Prefs.Written, "prefs_kept", res.Prefs.Kept}, counts...)...)
	return true
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
