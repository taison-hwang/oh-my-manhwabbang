package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// contextKey is this package's private context key type, so a value stored here
// cannot be read or overwritten by another package by accident.
type contextKey int

const requestIDKey contextKey = iota

// The security headers of arch §8.4, applied to every response — JSON, image,
// static asset, redirect and error alike.
const (
	// contentSecurityPolicy allows nothing off-origin. `data:` and `blob:` are
	// needed for `img-src` because the viewer prefetches through `new Image()`
	// and the DS renders inline SVG data URIs. `'unsafe-inline'` on style-src
	// is a stated temporary allowance (arch §8.4) until the Vite build is
	// verified to emit no inline styles.
	contentSecurityPolicy = "default-src 'self'; img-src 'self' data: blob:; " +
		"style-src 'self' 'unsafe-inline'; object-src 'none'; frame-ancestors 'none'"
	referrerPolicy = "same-origin"
)

// requestIDHeader is on every response (arch §7.1), so a user reporting a
// failure can quote one string that finds the log line.
const requestIDHeader = "X-Request-Id"

// statusRecorder captures what the handler answered, for the access log. It
// deliberately does not buffer the body: a 1.34 GB archive is streamed straight
// to the socket (FR-SRV-001, NFR-PRF-006) and buffering it would defeat the
// entire serving design.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *statusRecorder) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusRecorder) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

// Unwrap lets http.ResponseController and http.MaxBytesReader reach the real
// writer through the recorder.
func (w *statusRecorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// newRequestID returns 16 hex characters of randomness.
//
// It is generated rather than taken from an inbound header: a client-supplied
// id would let one request's log lines be attributed to another, and correlating
// with an upstream proxy is not a v1 requirement.
func newRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand cannot fail on any supported platform; a request id is
		// diagnostics, so degrade rather than fail the request.
		return "0000000000000000"
	}
	return hex.EncodeToString(b[:])
}

// requestIDFrom returns the id assigned to this request, or "".
func requestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// withObservability is the outermost middleware: it assigns a request id,
// applies the security headers, recovers panics and writes the access log.
//
// Order inside matters. The headers are set before `next` runs because they
// must be on the response whatever it turns out to be; the recover is inside
// them so a panic still answers with a hardened 500.
func (s *Server) withObservability(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := s.now()
		id := newRequestID()

		h := w.Header()
		h.Set(requestIDHeader, id)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", referrerPolicy)
		h.Set("Content-Security-Policy", contentSecurityPolicy)

		rec := &statusRecorder{ResponseWriter: w}
		ctx := context.WithValue(r.Context(), requestIDKey, id)
		r = r.WithContext(ctx)

		defer func() {
			if rv := recover(); rv != nil {
				// http.ErrAbortHandler is the documented way for a handler to
				// abandon a response; re-panicking preserves that contract
				// instead of turning a deliberate abort into a logged bug.
				if rv == http.ErrAbortHandler {
					panic(rv)
				}
				s.log.ErrorContext(ctx, "panic recovered in handler",
					"req_id", id, "method", r.Method, "path", r.URL.Path, "panic", rv)
				if rec.status == 0 {
					writeError(rec, r, nil, internalErr(nil))
				}
			}
			s.logRequest(r, rec, start)
		}()

		next.ServeHTTP(rec, r)
	})
}

// logRequest writes the one line per request of NFR-OPS-005, with the standard
// attribute set of arch §9.
//
// Image endpoints are demoted to debug: reading a 1 071-page volume is 1 071
// page requests plus its thumbnails, and at info level that drowns every line
// an operator actually wants. A failing image request is still a warning,
// because the point of demoting the noise is to make the signal visible.
func (s *Server) logRequest(r *http.Request, rec *statusRecorder, start time.Time) {
	if !s.logHTTP {
		return
	}
	status := rec.status
	if status == 0 {
		status = http.StatusOK
	}
	level := slog.LevelInfo
	if isImagePath(r.URL.Path) {
		level = slog.LevelDebug
	}
	if status >= 400 {
		level = slog.LevelWarn
	}
	if status >= 500 {
		level = slog.LevelError
	}
	s.log.Log(r.Context(), level, "http request",
		"req_id", requestIDFrom(r.Context()),
		"method", r.Method,
		"path", r.URL.Path,
		"status", status,
		"bytes", rec.bytes,
		"dur_ms", s.now().Sub(start).Milliseconds(),
		"remote", clientAddr(r, s.trustProxy),
	)
}

// isImagePath reports whether a path is one of the three high-volume image
// endpoints (arch §9).
func isImagePath(p string) bool {
	return strings.Contains(p, "/pages/") ||
		strings.Contains(p, "/thumbs/") ||
		strings.HasSuffix(p, "/cover")
}

// clientAddr is the `remote` log attribute. It honours X-Forwarded-For only
// when the operator has declared a proxy, for the same reason the rate limiter
// does (arch §8.2).
func clientAddr(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if v := r.Header.Get("X-Forwarded-For"); v != "" {
			if i := strings.IndexByte(v, ','); i >= 0 {
				v = v[:i]
			}
			return strings.TrimSpace(v)
		}
	}
	return r.RemoteAddr
}

// withAuth is the NFR-SEC-002 gate. It is a no-op when no password is
// configured, which is the default deployment (ruling E-8).
//
// With a password configured the gate is **all-or-nothing**: arch §8.2 —
// "with it enabled, static assets are protected too, so an unauthenticated
// visitor cannot even enumerate the SPA's routes" — impl-plan §3 WP-12
// acceptance 6 ("including static assets") and decision D-23 all say the same
// thing, so index.html, every hashed asset and every client-side route are
// behind it exactly like `/api/*`.
//
// Exemptions, and they are the only ones: `/api/health` — a monitor must not
// need a password, and it leaks nothing — and `/api/auth/*`, without which
// logging in would require being logged in.
//
// Gating the bundle takes `web/src/features/auth/LoginScreen.tsx` away with it,
// so a browser navigation is answered with the server-rendered form in
// loginpage.go: `401` with a document that ships no asset and reveals no route.
// Everything else — an XHR, an `<img>`, a curl — gets the §7.2 JSON envelope,
// which is what `web/src/api/client.ts` already keys on.
func (s *Server) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.auth.Enabled() || authExempt(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if s.auth.Authenticated(r) {
			next.ServeHTTP(w, r)
			return
		}
		if wantsLoginPage(r) {
			s.writeLoginPage(w, r, http.StatusUnauthorized, "")
			return
		}
		writeError(w, r, s.log, unauthorized())
	})
}

// authExempt reports the paths reachable without a session. The list is closed
// and exact: a prefix match here would make `/api/auth/../series` a bypass.
func authExempt(path string) bool {
	return path == "/api/health" || path == "/api/auth/status" ||
		path == "/api/auth/login" || path == "/api/auth/logout"
}

// wantsLoginPage reports whether an unauthenticated request is a browser
// navigation, and therefore something a human is about to look at.
//
// The test is an explicit `text/html` in `Accept`, not `*/*`: a `fetch()` for
// JSON and an `<img>` prefetch both send `*/*`, and answering those with HTML
// would replace a parseable 401 envelope with a document the client cannot
// read. `/api/*` never qualifies whatever it asks for — that surface is JSON by
// contract (arch §7.2).
func wantsLoginPage(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		return false
	}
	for _, part := range strings.Split(r.Header.Get("Accept"), ",") {
		media, _, _ := strings.Cut(part, ";")
		switch strings.ToLower(strings.TrimSpace(media)) {
		case "text/html", "application/xhtml+xml":
			return true
		}
	}
	return false
}
