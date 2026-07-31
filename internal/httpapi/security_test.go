package httpapi

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"shelf/internal/auth"
)

// NFR-SEC-002 / ruling E-8 — with no `auth:` block there is no password, and
// nothing is gated.
func TestAuth_disabledByDefault(t *testing.T) {
	e := newEnv(t)

	status := decodeBody[AuthStatus](t, e.get("/api/auth/status"), http.StatusOK)
	if status.AuthRequired {
		t.Error("auth_required = true with no auth: block (ruling E-8)")
	}
	if status.Authenticated {
		t.Error("authenticated = true with authentication disabled; there is nothing to be authenticated as")
	}
	if w := e.get("/api/roots"); w.Code != http.StatusOK {
		t.Errorf("GET /api/roots = %d with auth disabled, want 200", w.Code)
	}
	// Logging in when there is nothing to log into is a 404, not a fake 204:
	// pretending would leave the client believing it holds a session.
	errorBody(t, e.jsonBody(http.MethodPost, "/api/auth/login", `{"password":"x"}`),
		http.StatusNotFound, CodeNotFound)

	settings := decodeBody[Settings](t, e.get("/api/settings"), http.StatusOK)
	if settings.Server.AuthEnabled {
		t.Error("settings.server.auth_enabled = true with auth disabled")
	}
}

// NFR-SEC-002 — with a password configured, every /api/* route except health
// and auth answers 401 without the session cookie, and the whole login flow
// works end to end.
func TestAuth_gateAndLoginFlow(t *testing.T) {
	e := newEnv(t, withPassword("hunter2"), withStatic())

	gated := []string{
		"/api/roots",
		"/api/series",
		"/api/series/" + e.seriesFolderID,
		"/api/series/" + e.seriesFolderID + "/cover",
		"/api/books/" + e.bookZipID,
		"/api/books/" + e.bookZipID + "/pages/1",
		"/api/books/" + e.bookZipID + "/thumbs/1",
		"/api/continue",
		"/api/settings",
		"/api/cache/usage",
		"/api/scan/status",
		"/api/scan/log",
		"/api/progress/export",
		"/api/nope",
	}
	for _, target := range gated {
		t.Run("401 "+target, func(t *testing.T) {
			errorBody(t, e.get(target), http.StatusUnauthorized, CodeUnauthorized)
		})
	}

	// The two exemptions, and only these two.
	if w := e.get("/api/health"); w.Code != http.StatusOK {
		t.Errorf("GET /api/health = %d, want 200 — health never requires auth", w.Code)
	}
	before := decodeBody[AuthStatus](t, e.get("/api/auth/status"), http.StatusOK)
	if !before.AuthRequired || before.Authenticated {
		t.Errorf("auth status = %+v, want required and not authenticated", before)
	}

	// A wrong password is 401 with the same message as any other failure.
	errorBody(t, e.jsonBody(http.MethodPost, "/api/auth/login", `{"password":"wrong"}`),
		http.StatusUnauthorized, CodeUnauthorized)

	// The right one mints the cookie.
	w := e.jsonBody(http.MethodPost, "/api/auth/login", `{"password":"hunter2"}`)
	if w.Code != http.StatusNoContent {
		t.Fatalf("login = %d, want 204: %s", w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != auth.CookieName {
		t.Fatalf("login set %v, want one %s cookie", cookies, auth.CookieName)
	}
	session := cookies[0]
	if !session.HttpOnly {
		t.Error("the session cookie is not HttpOnly")
	}
	if session.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, want Lax", session.SameSite)
	}
	if session.Path != "/" {
		t.Errorf("cookie Path = %q, want / with no base path", session.Path)
	}

	withCookie := func(r *http.Request) { r.AddCookie(session) }
	if got := e.get("/api/roots", withCookie); got.Code != http.StatusOK {
		t.Fatalf("GET /api/roots with the session = %d, want 200: %s", got.Code, got.Body.String())
	}
	after := decodeBody[AuthStatus](t, e.get("/api/auth/status", withCookie), http.StatusOK)
	if !after.AuthRequired || !after.Authenticated {
		t.Errorf("auth status with the session = %+v, want required and authenticated", after)
	}

	// A forged cookie is not a session.
	forged := e.get("/api/roots", func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: session.Value + "x"})
	})
	errorBody(t, forged, http.StatusUnauthorized, CodeUnauthorized)

	// Logout clears it.
	out := e.do(http.MethodPost, "/api/auth/logout", nil, withCookie)
	if out.Code != http.StatusNoContent {
		t.Fatalf("logout = %d, want 204", out.Code)
	}
	cleared := out.Result().Cookies()
	if len(cleared) != 1 || cleared[0].MaxAge >= 0 || cleared[0].Value != "" {
		t.Errorf("logout set %v, want an expired empty cookie", cleared)
	}

	// The settings mirror reports the gate.
	settings := decodeBody[Settings](t, e.get("/api/settings", withCookie), http.StatusOK)
	if !settings.Server.AuthEnabled {
		t.Error("settings.server.auth_enabled = false with a password configured")
	}
}

// arch §8.2 — the login limiter is 5 per minute, burst 5, then 429 with a
// Retry-After carrying `rate_limited`, which amendment A-9 (ruling E-13) put
// into §7.2's enum so the answer has a name the contract can express.
func TestAuth_loginRateLimit(t *testing.T) {
	e := newEnv(t, withPassword("hunter2"))

	for i := range auth.LoginBurst {
		w := e.jsonBody(http.MethodPost, "/api/auth/login", `{"password":"wrong"}`)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d = %d, want 401", i+1, w.Code)
		}
	}
	w := e.jsonBody(http.MethodPost, "/api/auth/login", `{"password":"wrong"}`)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("attempt %d = %d, want 429", auth.LoginBurst+1, w.Code)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Error("the 429 carries no Retry-After")
	}
	body := errorBody(t, w, http.StatusTooManyRequests, CodeRateLimited)
	if body.Detail["retry_after"] == nil {
		t.Error("detail.retry_after is missing")
	}

	// Even the correct password is refused while the bucket is empty, so a
	// script cannot alternate a known-good value in to keep its budget topped up.
	if again := e.jsonBody(http.MethodPost, "/api/auth/login", `{"password":"hunter2"}`); again.Code != http.StatusTooManyRequests {
		t.Errorf("the correct password while limited = %d, want 429", again.Code)
	}
}

// asHTML makes a request look like a browser navigation, which is what selects
// the server-rendered login page over the JSON envelope.
func asHTML(r *http.Request) {
	r.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
}

// arch §8.2 / impl-plan §3 WP-12 acceptance 6 / decision D-23 — auth is
// all-or-nothing. With a password configured, static assets are protected too,
// "so an unauthenticated visitor cannot even enumerate the SPA's routes"; only
// `/api/health` and `/api/auth/*` are exempt.
func TestAuth_gatesStaticAssetsToo(t *testing.T) {
	e := newEnv(t, withPassword("hunter2"), withStatic())

	// Not one byte of the shell: not index.html, not a client-side route, not a
	// hashed asset. A non-navigation request gets the §7.2 envelope.
	for _, target := range []string{"/", "/index.html", "/series/abc", "/assets/index-abc123.js"} {
		t.Run("401 "+target, func(t *testing.T) {
			w := e.get(target)
			errorBody(t, w, http.StatusUnauthorized, CodeUnauthorized)
			if strings.Contains(w.Body.String(), `id="root"`) {
				t.Error("the SPA shell was served to an unauthenticated visitor")
			}
		})
	}

	// The two exemptions still answer, so a monitor and the SPA's own bootstrap
	// call keep working.
	if w := e.get("/api/health"); w.Code != http.StatusOK {
		t.Errorf("GET /api/health = %d, want 200 — health never requires auth", w.Code)
	}
	if w := e.get("/api/auth/status"); w.Code != http.StatusOK {
		t.Errorf("GET /api/auth/status = %d, want 200", w.Code)
	}

	// With a session everything is served again.
	session := loginCookie(t, e, "hunter2")
	for _, target := range []string{"/", "/series/abc", "/assets/index-abc123.js"} {
		if w := e.get(target, withCookie(session)); w.Code != http.StatusOK {
			t.Errorf("GET %s with a session = %d, want 200", target, w.Code)
		}
	}
}

// A gated bundle takes LoginScreen.tsx with it, so the one document an
// unauthenticated browser may have is rendered by the server (loginpage.go).
func TestAuth_serverRenderedLoginPage(t *testing.T) {
	e := newEnv(t, withPassword("hunter2"), withStatic())

	w := e.get("/series/abc", asHTML)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("a navigation while logged out = %d, want 401", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store — a cached login form outlives the session it denies", got)
	}
	body := w.Body.String()
	for _, want := range []string{`action="/api/auth/login"`, `method="post"`, `name="password"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the login page does not contain %s:\n%s", want, body)
		}
	}
	// It must reveal nothing about the SPA: no bundle, no route, no script.
	// The absent `<script>` is also a correctness requirement rather than
	// taste — arch §8.4's CSP has no `script-src`, so `default-src 'self'`
	// would refuse an inline one and the form would never submit.
	for _, forbidden := range []string{"index-abc123.js", `id="root"`, "<script"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("the login page leaks %q — arch §8.2 says an unauthenticated "+
				"visitor cannot enumerate the SPA's routes", forbidden)
		}
	}
	// The hardening of arch §8.4 applies to it like every other response.
	if got := w.Header().Get("Content-Security-Policy"); got != contentSecurityPolicy {
		t.Errorf("the login page CSP = %q, want the arch §8.4 policy", got)
	}
	if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}

	// An XHR or an <img> is not a navigation and still gets the envelope, which
	// is what web/src/api/client.ts parses.
	errorBody(t, e.get("/assets/index-abc123.js", func(r *http.Request) {
		r.Header.Set("Accept", "*/*")
	}), http.StatusUnauthorized, CodeUnauthorized)

	// Nor is an /api/ route, whatever it claims to accept.
	errorBody(t, e.get("/api/series", asHTML), http.StatusUnauthorized, CodeUnauthorized)

	// With auth off there is no login page anywhere.
	open := newEnv(t, withStatic())
	if w := open.get("/", asHTML); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `id="root"`) {
		t.Errorf("GET / with auth disabled = %d, want the SPA", w.Code)
	}
}

// The whole point of the page is that it can log in without an asset and
// without a script — CSP `default-src 'self'` forbids inline JS (arch §8.4), so
// the form is a plain POST and `POST /api/auth/login` accepts it.
func TestAuth_loginPageFormFlow(t *testing.T) {
	e := newEnv(t, withPassword("hunter2"), withStatic())

	postForm := func(values url.Values) *httptest.ResponseRecorder {
		return e.do(http.MethodPost, "/api/auth/login", strings.NewReader(values.Encode()),
			func(r *http.Request) {
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			})
	}

	// A wrong password re-renders the form with the failure's status and a
	// message, rather than a JSON body a browser would display raw.
	bad := postForm(url.Values{"password": {"wrong"}})
	if bad.Code != http.StatusUnauthorized {
		t.Fatalf("a wrong password through the form = %d, want 401", bad.Code)
	}
	if !strings.Contains(bad.Body.String(), `name="password"`) {
		t.Error("a failed form login does not return the form")
	}
	if len(bad.Result().Cookies()) != 0 {
		t.Error("a failed form login set a cookie")
	}

	// The right one mints the same cookie the JSON path does and redirects with
	// 303, so refreshing the landing page cannot repost the password.
	ok := postForm(url.Values{"password": {"hunter2"}})
	if ok.Code != http.StatusSeeOther {
		t.Fatalf("form login = %d, want 303: %s", ok.Code, ok.Body.String())
	}
	if got := ok.Header().Get("Location"); got != "/" {
		t.Errorf("Location = %q, want /", got)
	}
	cookies := ok.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != auth.CookieName || !cookies[0].HttpOnly {
		t.Fatalf("form login set %v, want one HttpOnly %s cookie", cookies, auth.CookieName)
	}
	if w := e.get("/", withCookie(cookies[0]), asHTML); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), `id="root"`) {
		t.Errorf("GET / after a form login = %d, want the SPA", w.Code)
	}

	// The JSON contract of arch §7.12 is untouched by the addition.
	json := e.jsonBody(http.MethodPost, "/api/auth/login", `{"password":"hunter2"}`)
	if json.Code != http.StatusNoContent {
		t.Errorf("the JSON login = %d, want the frozen 204", json.Code)
	}

	// A password is never taken from the query string: it would be in every
	// access log and browser history.
	query := e.do(http.MethodPost, "/api/auth/login?password=hunter2", strings.NewReader(""),
		func(r *http.Request) { r.Header.Set("Content-Type", "application/x-www-form-urlencoded") })
	if query.Code != http.StatusUnauthorized {
		t.Errorf("a password in the query string = %d, want 401", query.Code)
	}
}

// Under a base path the form must post to, and land on, the mounted prefix —
// otherwise logging in through the gate 404s (NFR-SEC-003).
func TestAuth_loginPageUnderABasePath(t *testing.T) {
	e := newEnv(t, withBasePath("/reader"), withPassword("hunter2"), withStatic())

	page := e.get("/reader/series/abc", asHTML)
	if page.Code != http.StatusUnauthorized {
		t.Fatalf("a navigation under the base while logged out = %d, want 401", page.Code)
	}
	if !strings.Contains(page.Body.String(), `action="/reader/api/auth/login"`) {
		t.Errorf("the form action is not mounted under the base path:\n%s", page.Body.String())
	}

	w := e.do(http.MethodPost, "/reader/api/auth/login",
		strings.NewReader(url.Values{"password": {"hunter2"}}.Encode()),
		func(r *http.Request) { r.Header.Set("Content-Type", "application/x-www-form-urlencoded") })
	if w.Code != http.StatusSeeOther {
		t.Fatalf("form login under the base = %d, want 303", w.Code)
	}
	if got := w.Header().Get("Location"); got != "/reader/" {
		t.Errorf("Location = %q, want /reader/", got)
	}
}

// The rate limiter reaches the form too: the browser flow must not be a way
// around arch §8.2's five-per-minute bucket.
func TestAuth_loginPageIsRateLimited(t *testing.T) {
	e := newEnv(t, withPassword("hunter2"))
	form := func() *httptest.ResponseRecorder {
		return e.do(http.MethodPost, "/api/auth/login",
			strings.NewReader(url.Values{"password": {"wrong"}}.Encode()),
			func(r *http.Request) { r.Header.Set("Content-Type", "application/x-www-form-urlencoded") })
	}
	for i := range auth.LoginBurst {
		if w := form(); w.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d = %d, want 401", i+1, w.Code)
		}
	}
	w := form()
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("attempt %d = %d, want 429", auth.LoginBurst+1, w.Code)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Error("the throttled form response carries no Retry-After")
	}
	if !strings.Contains(w.Body.String(), `name="password"`) {
		t.Error("the throttled form response is not the form")
	}
}

// The form body is capped like every other body (arch §8.4), and a HEAD on a
// gated navigation is a 401 with headers and no body.
func TestAuth_loginPageEdges(t *testing.T) {
	e := newEnv(t, withPassword("hunter2"), withStatic())

	t.Run("an oversized form body is 400", func(t *testing.T) {
		huge := "password=" + strings.Repeat("x", (1<<20)+1)
		w := e.do(http.MethodPost, "/api/auth/login", strings.NewReader(huge), func(r *http.Request) {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		})
		errorBody(t, w, http.StatusBadRequest, CodeBadRequest)
	})

	t.Run("HEAD of a gated navigation", func(t *testing.T) {
		w := e.do(http.MethodHead, "/series/abc", nil, asHTML)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("HEAD = %d, want 401", w.Code)
		}
		if w.Body.Len() != 0 {
			t.Errorf("a HEAD response carries %d bytes of body", w.Body.Len())
		}
		if w.Header().Get("Content-Length") == "" {
			t.Error("a HEAD response carries no Content-Length")
		}
	})
}

// withCookie attaches a session to a request.
func withCookie(c *http.Cookie) func(*http.Request) {
	return func(r *http.Request) { r.AddCookie(c) }
}

// loginCookie logs in through the JSON endpoint and returns the session.
func loginCookie(t *testing.T, e *env, password string) *http.Cookie {
	t.Helper()
	w := e.jsonBody(http.MethodPost, "/api/auth/login", `{"password":"`+jsonEscape(password)+`"}`)
	if w.Code != http.StatusNoContent {
		t.Fatalf("login = %d: %s", w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("login set %d cookies, want 1", len(cookies))
	}
	return cookies[0]
}

// NFR-SEC-003 / arch §8.3 — the whole application mounts under `base_path`.
func TestBasePath_mountsTheWholeApplication(t *testing.T) {
	e := newEnv(t, withBasePath("/reader"), withStatic())

	t.Run("api routes live under the base", func(t *testing.T) {
		if w := e.get("/reader/api/health"); w.Code != http.StatusOK {
			t.Errorf("GET /reader/api/health = %d, want 200", w.Code)
		}
		if w := e.get("/api/health"); w.Code != http.StatusNotFound {
			t.Errorf("GET /api/health = %d, want 404 — nothing lives outside the base", w.Code)
		} else {
			errorBody(t, w, http.StatusNotFound, CodeNotFound)
		}
	})

	t.Run("path values survive the strip", func(t *testing.T) {
		w := e.get("/reader/api/books/" + e.bookZipID + "/pages/1")
		if w.Code != http.StatusOK {
			t.Fatalf("GET a page under the base = %d: %s", w.Code, w.Body.String())
		}
		if !bytes.Equal(w.Body.Bytes(), e.zipPayloads[0]) {
			t.Error("the page body differs under a base path")
		}
		put := e.jsonBody(http.MethodPut, "/reader/api/books/"+e.bookZipID+"/progress", `{"page":1}`)
		if put.Code != http.StatusOK {
			t.Errorf("PUT progress under the base = %d: %s", put.Code, put.Body.String())
		}
	})

	t.Run("the base without a trailing slash redirects", func(t *testing.T) {
		w := e.get("/reader")
		if w.Code != http.StatusPermanentRedirect {
			t.Fatalf("GET /reader = %d, want 308", w.Code)
		}
		if got := w.Header().Get("Location"); got != "/reader/" {
			t.Errorf("Location = %q, want /reader/", got)
		}
	})

	t.Run("index.html carries the injected base href", func(t *testing.T) {
		w := e.get("/reader/")
		if w.Code != http.StatusOK {
			t.Fatalf("GET /reader/ = %d", w.Code)
		}
		if !strings.Contains(w.Body.String(), `<base href="/reader/" />`) {
			t.Errorf("index.html does not carry the injected base href:\n%s", w.Body.String())
		}
	})

	t.Run("the SPA fallback works under the base", func(t *testing.T) {
		w := e.get("/reader/series/" + e.seriesFolderID)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "id=\"root\"") {
			t.Errorf("a client-side route under the base = %d", w.Code)
		}
	})

	t.Run("the wrong method is still 405", func(t *testing.T) {
		w := e.do(http.MethodPost, "/reader/api/series", nil)
		errorBody(t, w, http.StatusMethodNotAllowed, CodeBadRequest)
	})

	t.Run("the base path is mirrored in settings", func(t *testing.T) {
		s := decodeBody[Settings](t, e.get("/reader/api/settings"), http.StatusOK)
		if s.Server.BasePath != "/reader" {
			t.Errorf("settings.server.base_path = %q, want /reader", s.Server.BasePath)
		}
	})
}

// The session cookie is scoped to the base path, so two SHELF instances behind
// one proxy cannot see each other's sessions.
func TestBasePath_scopesTheSessionCookie(t *testing.T) {
	e := newEnv(t, withBasePath("/reader"), withPassword("hunter2"))
	w := e.jsonBody(http.MethodPost, "/reader/api/auth/login", `{"password":"hunter2"}`)
	if w.Code != http.StatusNoContent {
		t.Fatalf("login = %d: %s", w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Path != "/reader/" {
		t.Fatalf("cookie = %v, want Path=/reader/", cookies)
	}
}

// arch §2.1 — a binary built without `pnpm build` says so instead of 404-ing.
func TestSPA_placeholderWhenTheFrontendIsUnbuilt(t *testing.T) {
	e := newEnv(t) // no withStatic()
	w := e.get("/")
	if w.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "the frontend has not been built") {
		t.Errorf("body = %q, want the build placeholder", w.Body.String())
	}
}

// Hashed assets are immutable; index.html never is.
func TestSPA_cachePolicy(t *testing.T) {
	e := newEnv(t, withStatic())

	asset := e.get("/assets/index-abc123.js")
	if asset.Code != http.StatusOK {
		t.Fatalf("GET the asset = %d", asset.Code)
	}
	if got := asset.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Errorf("asset Cache-Control = %q, want immutable — the name carries a content hash", got)
	}

	index := e.get("/")
	if got := index.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("index.html Cache-Control = %q, want no-cache — its URL outlives its bytes", got)
	}
	if got := index.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("index.html Content-Type = %q", got)
	}

	// A missing hashed asset is a real 404, not the SPA shell: answering a
	// <script src> with HTML produces a MIME error rather than a clear failure.
	errorBody(t, e.get("/assets/gone-deadbeef.js"), http.StatusNotFound, CodeNotFound)
}

// NFR-OPS-005 / arch §9 — one structured line per request with the standard
// attribute set, and image endpoints demoted to debug so a 1 071-page read does
// not drown the log.
func TestLogging_oneLinePerRequestWithImagesDemoted(t *testing.T) {
	e := newEnv(t)

	var buf bytes.Buffer
	e.srv.log = slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	e.get("/api/settings")
	e.get("/api/books/" + e.bookZipID + "/pages/1")
	e.get("/api/series/aaaaaaaaaaaaaaaa")

	lines := parseLogLines(t, buf.String())
	if len(lines) != 3 {
		t.Fatalf("log lines = %d, want one per request:\n%s", len(lines), buf.String())
	}

	for _, want := range []string{"req_id", "method", "path", "status", "bytes", "dur_ms", "remote"} {
		if _, ok := lines[0][want]; !ok {
			t.Errorf("the access log line has no %q attribute: %v", want, lines[0])
		}
	}
	if lines[0]["level"] != "INFO" {
		t.Errorf("a settings request logged at %v, want INFO", lines[0]["level"])
	}
	if lines[1]["level"] != "DEBUG" {
		t.Errorf("an image request logged at %v, want DEBUG (arch §9)", lines[1]["level"])
	}
	if lines[2]["level"] != "WARN" {
		t.Errorf("a 404 logged at %v, want WARN", lines[2]["level"])
	}
	if lines[2]["status"] != float64(404) {
		t.Errorf("status = %v, want 404", lines[2]["status"])
	}
}

// A password is never logged, whatever happens to it.
func TestLogging_neverCarriesThePassword(t *testing.T) {
	e := newEnv(t, withPassword("correct horse battery staple"))

	var buf bytes.Buffer
	e.srv.log = slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	e.jsonBody(http.MethodPost, "/api/auth/login", `{"password":"correct horse battery staple"}`)
	if strings.Contains(buf.String(), "correct horse") {
		t.Fatalf("the password reached the log:\n%s", buf.String())
	}
}

func parseLogLines(t *testing.T, out string) []map[string]any {
	t.Helper()
	var lines []map[string]any
	for _, raw := range strings.Split(strings.TrimSpace(out), "\n") {
		if raw == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			t.Fatalf("log line is not JSON: %v (%q)", err, raw)
		}
		lines = append(lines, m)
	}
	return lines
}

// arch §8.4 — error messages never leak an absolute filesystem path outside
// the roots[].path that GET /api/roots already exposes.
func TestErrors_doNotLeakFilesystemPaths(t *testing.T) {
	e := newEnv(t)

	targets := []string{
		"/api/series/aaaaaaaaaaaaaaaa",
		"/api/books/aaaaaaaaaaaaaaaa/pages/1",
		"/api/books/" + e.bookBrokenID + "/pages/1",
		"/api/series/NOT-AN-ID",
	}
	for _, target := range targets {
		t.Run(target, func(t *testing.T) {
			body := e.get(target).Body.String()
			if strings.Contains(body, e.media) || strings.Contains(body, e.dir) {
				t.Errorf("an error body leaked a filesystem path: %s", body)
			}
		})
	}
}

// A 500 tells the client nothing and the log everything (arch §8.4).
func TestInternalError_isOpaqueToTheClient(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))

	writeError(rec, requestFor("/api/series"), log,
		internalErr(errSecret))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "s3cr3t") {
		t.Errorf("the cause reached the client: %s", rec.Body.String())
	}
	if !strings.Contains(buf.String(), "s3cr3t") {
		t.Errorf("the cause did not reach the log: %s", buf.String())
	}
}
