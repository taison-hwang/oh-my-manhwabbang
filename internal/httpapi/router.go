package httpapi

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"shelf/internal/auth"
	"shelf/internal/config"
)

// DefaultMaxConcurrentPages bounds how many page and thumbnail handlers may be
// in flight at once (arch §6.1).
//
// Without it a browser opening a book fans out to as many concurrent inflates
// as it has connections, times however many tabs; each one holds a 32 KiB
// deflate window plus the entry's buffers, and NFR-PRF-005's 200 MB budget is
// not written with "times unbounded" in it. 32 is far above what one reader can
// generate (six HTTP/1.1 connections) and far below what hurts.
const DefaultMaxConcurrentPages = 32

// Options configures a Server. Everything except Config, Index and UserData is
// optional: a Server built without a Scanner still serves the library, which is
// exactly what NFR-OPS-006 asks for.
type Options struct {
	// Config is the loaded, validated configuration. Required.
	Config *config.Config

	Index    Index
	UserData UserData
	Scanner  Scanner
	Thumbs   Thumbs
	Sources  Sources
	Roots    Roots
	// Pool is the archive handle LRU, for GET /api/health?verbose=1. Optional.
	Pool HandlePool

	// Auth is the NFR-SEC-002 gate. nil builds a disabled one, which is the
	// default deployment (ruling E-8).
	Auth *auth.Authenticator

	// Static is the built SPA (web.Dist()). nil serves a plain-text
	// "run `make web`" placeholder instead of a 404 storm (arch §2.1).
	//
	// It is behind the auth gate like everything else: arch §8.2 and impl-plan
	// §3 WP-12 acceptance 6 both make auth all-or-nothing, "including static
	// assets", and the login form an unauthenticated visitor needs is rendered
	// by this package rather than served out of the bundle (loginpage.go).
	Static fs.FS

	// MaxConcurrentPages bounds in-flight page/thumbnail handlers. 0 selects
	// DefaultMaxConcurrentPages.
	MaxConcurrentPages int

	// Logger; nil selects slog.Default().
	Logger *slog.Logger
	// ConfigDigest is the SHA-256 of the configuration file as it was when this
	// process loaded it, hex-encoded — the baseline
	// `Settings.server.config_changed_on_disk` compares against (§7.8,
	// amendment A-11). The composition root takes it at load, because that is
	// the only moment the bytes this server is actually running are on hand.
	//
	// Empty is legal and means "no baseline was handed over": New falls back to
	// digesting the file itself, and a Config with no file reports `false`
	// forever, which is the honest answer when there is nothing to differ from.
	ConfigDigest string

	// Now is the clock, a test seam. nil selects time.Now.
	Now func() time.Time
	// StartedAt is the process start, reported by /api/health. Zero selects
	// Now().
	StartedAt time.Time
}

// Server is the whole HTTP surface. It implements http.Handler, base path and
// all, so the composition root's job is `http.Server{Handler: srv}`.
type Server struct {
	cfg        *config.Config
	idx        Index
	user       UserData
	scan       Scanner
	thumbs     Thumbs
	sources    Sources
	roots      Roots
	pool       HandlePool
	auth       *auth.Authenticator
	static     fs.FS
	log        *slog.Logger
	now        func() time.Time
	started    time.Time
	base       string
	logHTTP    bool
	trustProxy bool

	// configDigest is the configuration file as this process has adopted it
	// (§7.8). It starts as the bytes startup loaded and, since amendment A-12,
	// moves forward when a hot add makes this process's state match the file
	// again. Guarded by adoptMu: `GET /api/settings` reads it on every poll.
	configDigest string

	// addedRoots is amendment A-12's mirror of removedRoots below: the roots
	// this process has opened since startup, in the order it opened them.
	//
	// It exists for the same reason the removed-set does — so the running server
	// can honour a configuration edit without anything hot-swapping the shared
	// `*config.Config`, which `internal/app` also holds and which has no lock.
	// Every reader of "the roots this process has" goes through
	// `configuredRoots()`; reading `s.cfg.Roots` directly now sees a stale list.
	adoptMu    sync.RWMutex
	addedRoots []config.Root

	// rootEdit serialises the two write handlers of arch §7.4. Each of them
	// re-reads the file inside this lock, so two concurrent adds cannot both
	// splice into the list they each read before the other wrote.
	rootEdit sync.Mutex

	// removedRoots is amendment A-11's revision R1: the roots this process has
	// removed. `GET /api/roots` hides them and the scanner skips them, which is
	// what makes 제거 take effect before the restart without hot-swapping the
	// open root set.
	removedMu    sync.RWMutex
	removedRoots map[string]struct{}

	// rescans coalesces the targeted rescans arch §5.2 asks for when a
	// container is found to have changed on disk.
	rescans *rescanCoalescer

	// pageSem bounds concurrent page and thumbnail handlers.
	pageSem chan struct{}

	handler http.Handler
}

// New builds a Server and its route table.
func New(opts Options) (*Server, error) {
	if opts.Config == nil {
		return nil, errors.New("httpapi: Config is required")
	}
	s := &Server{
		cfg:          opts.Config,
		configDigest: opts.ConfigDigest,
		idx:          opts.Index,
		user:         opts.UserData,
		scan:         opts.Scanner,
		thumbs:       opts.Thumbs,
		sources:      opts.Sources,
		roots:        opts.Roots,
		pool:         opts.Pool,
		auth:         opts.Auth,
		static:       opts.Static,
		log:          opts.Logger,
		now:          opts.Now,
		started:      opts.StartedAt,
		base:         opts.Config.Server.BasePath,
		logHTTP:      opts.Config.Log.HTTPRequests,
		trustProxy:   opts.Config.Server.TrustedProxyHeaders,
		rescans:      newRescanCoalescer(staleRescanCooldown),
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.started.IsZero() {
		s.started = s.now()
	}
	if s.configDigest == "" {
		// Nobody handed one over. Digesting the file here is later than load and
		// therefore weaker — a hand-edit in between goes unnoticed — but it is
		// strictly better than reporting `config_changed_on_disk: false` forever,
		// and it keeps a Server built directly (a test, an embedder) honest.
		if path := s.cfg.AbsFilePath(); path != "" {
			if state, err := config.ReadFileState(path); err == nil {
				s.configDigest = state.Digest
			}
		}
	}
	if s.auth == nil {
		a, err := auth.New(auth.Options{})
		if err != nil {
			return nil, fmt.Errorf("building the disabled authenticator: %w", err)
		}
		s.auth = a
	}
	n := opts.MaxConcurrentPages
	if n <= 0 {
		n = DefaultMaxConcurrentPages
	}
	s.pageSem = make(chan struct{}, n)

	s.handler = s.mount(s.withObservability(s.withAuth(s.routes())))
	return s, nil
}

// ServeHTTP makes Server the whole application handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}

// BasePath is the normalised `server.base_path`, "" or "/prefix".
func (s *Server) BasePath() string { return s.base }

// mount implements NFR-SEC-003: the whole application lives under
// `server.base_path`, stripped before routing so every handler and every
// PathValue is written as if it were at the root.
//
// A request to `{base}` with no trailing slash is 308-redirected to `{base}/`.
// 308 rather than 301 because it preserves the method and body — a POST that
// forgot the slash must not silently become a GET. Anything outside the base is
// a JSON 404: an operator who mounts SHELF at /reader has not asked it to
// answer for /.
func (s *Server) mount(inner http.Handler) http.Handler {
	if s.base == "" {
		return inner
	}
	stripped := http.StripPrefix(s.base, inner)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == s.base:
			target := s.base + "/"
			if q := r.URL.RawQuery; q != "" {
				target += "?" + q
			}
			http.Redirect(w, r, target, http.StatusPermanentRedirect)
		case strings.HasPrefix(r.URL.Path, s.base+"/"):
			stripped.ServeHTTP(w, r)
		default:
			writeError(w, r, s.log, notFound("no route for %s", r.URL.Path))
		}
	})
}

// handlerFunc is a handler that may fail. Returning an error rather than
// writing one is what lets every failure in this package go through exactly one
// renderer (writeError) and therefore produce exactly one envelope shape.
type handlerFunc func(w http.ResponseWriter, r *http.Request) error

// h adapts a fallible handler to net/http.
func (s *Server) h(fn handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := fn(w, r); err != nil {
			writeError(w, r, s.log, err)
		}
	}
}

// methods dispatches by verb and answers 405 with an `Allow` header for
// anything else.
//
// Go 1.22's ServeMux can match on the method itself, but its own 405 is a
// plain-text body — and arch §7.2 says *every* non-2xx response carries the
// envelope, image endpoints included. So the pattern is registered without a
// method and the verb is dispatched here, which also keeps `Allow` correct for
// free.
//
// HEAD is accepted wherever GET is: net/http discards the body of a HEAD
// response itself, so a GET handler is already a correct HEAD handler.
func (s *Server) methods(m map[string]handlerFunc) http.Handler {
	allow := make([]string, 0, len(m)+1)
	for verb := range m {
		allow = append(allow, verb)
	}
	if _, ok := m[http.MethodGet]; ok {
		allow = append(allow, http.MethodHead)
	}
	slices.Sort(allow)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.Method
		if method == http.MethodHead {
			method = http.MethodGet
		}
		fn, ok := m[method]
		if !ok {
			writeError(w, r, s.log, methodNotAllowed(allow))
			return
		}
		if err := fn(w, r); err != nil {
			writeError(w, r, s.log, err)
		}
	})
}

// get is the common case: one verb, one handler.
func (s *Server) get(fn handlerFunc) http.Handler {
	return s.methods(map[string]handlerFunc{http.MethodGet: fn})
}

// routes is arch §7.13's endpoint table, verbatim.
//
// Patterns carry no method (see methods) and every `/api/…` path is registered
// explicitly, so the `/api/` catch-all below can distinguish "no such endpoint"
// — a JSON 404 — from a client-side route, which falls through to the SPA.
// An unknown `/api/*` must never return index.html: the frontend would try to
// parse HTML as JSON and report a nonsense error (impl-plan §3 WP-12 #1).
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	mux.Handle("/api/health", s.get(s.handleHealth))
	// `POST` and the `{name}` pattern are amendment A-11 (ruling E-26). Both are
	// registered unconditionally and refuse with `403 forbidden` when the gate is
	// shut: a route that disappeared with a configuration key would answer `405`
	// and `404` instead, and a client cannot tell "this server has the feature
	// switched off" from "this server is too old" out of either.
	mux.Handle("/api/roots", s.methods(map[string]handlerFunc{
		http.MethodGet:  s.handleRoots,
		http.MethodPost: s.handleCreateRoot,
	}))
	mux.Handle("/api/roots/{name}", s.methods(map[string]handlerFunc{
		http.MethodDelete: s.handleDeleteRoot,
	}))
	// The directory picker is amendment A-12 (ruling E-40). It is `/api/browse`
	// and NOT `/api/roots/browse`, which would have read better: `browse` is a
	// legal root name (§3.2's alphabet admits it), and the more specific literal
	// pattern beats `/api/roots/{name}`, so a user with a root called `browse`
	// would find it undeletable — a silent, data-dependent 405.
	mux.Handle("/api/browse", s.get(s.handleBrowse))

	mux.Handle("/api/series", s.get(s.handleSeriesList))
	mux.Handle("/api/series/{sid}", s.get(s.handleSeriesDetail))
	mux.Handle("/api/series/{sid}/cover", s.get(s.handleSeriesCover))
	mux.Handle("/api/series/{sid}/rescan", s.methods(map[string]handlerFunc{
		http.MethodPost: s.handleSeriesRescan,
	}))

	mux.Handle("/api/books/{bid}", s.get(s.handleBookDetail))
	mux.Handle("/api/books/{bid}/pages/{n}", s.get(s.handlePage))
	mux.Handle("/api/books/{bid}/thumbs/{n}", s.get(s.handlePageThumb))
	mux.Handle("/api/books/{bid}/progress", s.methods(map[string]handlerFunc{
		http.MethodPut:    s.handlePutProgress,
		http.MethodDelete: s.handleDeleteProgress,
	}))
	mux.Handle("/api/books/{bid}/prefs", s.methods(map[string]handlerFunc{
		http.MethodGet: s.handleGetPrefs,
		http.MethodPut: s.handlePutPrefs,
	}))

	mux.Handle("/api/continue", s.get(s.handleContinue))

	mux.Handle("/api/settings", s.methods(map[string]handlerFunc{
		http.MethodGet: s.handleGetSettings,
		http.MethodPut: s.handlePutSettings,
	}))

	mux.Handle("/api/cache/usage", s.get(s.handleCacheUsage))
	mux.Handle("/api/cache", s.methods(map[string]handlerFunc{
		http.MethodDelete: s.handleCachePurge,
	}))

	mux.Handle("/api/scan", s.methods(map[string]handlerFunc{
		http.MethodPost: s.handleStartScan,
	}))
	mux.Handle("/api/scan/status", s.get(s.handleScanStatus))
	mux.Handle("/api/scan/cancel", s.methods(map[string]handlerFunc{
		http.MethodPost: s.handleCancelScan,
	}))
	mux.Handle("/api/scan/log", s.get(s.handleScanLog))

	mux.Handle("/api/progress/export", s.get(s.handleProgressExport))
	mux.Handle("/api/progress/import", s.methods(map[string]handlerFunc{
		http.MethodPost: s.handleProgressImport,
	}))

	mux.Handle("/api/auth/status", s.get(s.handleAuthStatus))
	mux.Handle("/api/auth/login", s.methods(map[string]handlerFunc{
		http.MethodPost: s.handleLogin,
	}))
	mux.Handle("/api/auth/logout", s.methods(map[string]handlerFunc{
		http.MethodPost: s.handleLogout,
	}))

	// Anything under /api/ that matched none of the above is an unknown
	// endpoint, and says so in the envelope.
	mux.Handle("/api/", s.h(func(_ http.ResponseWriter, r *http.Request) error {
		return notFound("no such endpoint: %s", r.URL.Path)
	}))

	// Everything else is the SPA: its assets, or a client-side route that
	// resolves to index.html.
	mux.Handle("/", s.spaHandler())

	return mux
}
