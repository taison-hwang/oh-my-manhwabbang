package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"shelf/internal/auth/oidc/oidctest"
	"shelf/internal/userdata"
)

const (
	subReader   = "sub-reader-0001"
	subAdmin    = "sub-admin-0001"
	subStranger = "sub-stranger-0001"
	oidcOrigin  = "http://127.0.0.1:8790"
	oidcClient  = "zip-viewer"
	oidcAud     = "https://library.test/api"
)

// oidcFixture is a fake uni-auth plus the knobs of `auth.oidc` (ruling E-74).
type oidcFixture struct {
	t     *testing.T
	prov  *oidctest.Provider
	hook  *loginHookSpy
	allow []string
	admin []string
	// postLogout is `post_logout_redirect_url`; empty by default as shipped.
	postLogout string
	// legacyOwner is `legacy_owner_sub`; empty by default as shipped.
	legacyOwner string

	mu    sync.Mutex
	clock time.Time
}

func newOIDCFixture(t *testing.T) *oidcFixture {
	t.Helper()
	f := &oidcFixture{t: t, hook: &loginHookSpy{}, clock: fixedNow,
		allow: []string{subReader, subAdmin}, admin: []string{subAdmin}}
	f.prov = oidctest.New(t, oidcClient, oidcAud, f.now)
	return f
}

func withOIDC(f *oidcFixture) envOption {
	return func(c *envConfig) { c.oidc, c.perUser = f, true }
}

// newOIDCEnv is the usual oidc harness: a built SPA and the fake provider.
func newOIDCEnv(t *testing.T, opts ...envOption) (*env, *oidcFixture) {
	t.Helper()
	f := newOIDCFixture(t)
	return newEnv(t, append([]envOption{withStatic(), withOIDC(f)}, opts...)...), f
}

func (f *oidcFixture) now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.clock
}

func (f *oidcFixture) advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clock = f.clock.Add(d)
}

func (f *oidcFixture) yaml(base string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "auth:\n  mode: oidc\n  oidc:\n")
	fmt.Fprintf(&b, "    issuer: %q\n    client_id: %q\n    audience: %q\n", f.prov.URL, oidcClient, oidcAud)
	fmt.Fprintf(&b, "    redirect_url: %q\n", oidcOrigin+base+"/api/auth/oidc/callback")
	if f.postLogout != "" {
		fmt.Fprintf(&b, "    post_logout_redirect_url: %q\n", f.postLogout)
	}
	list := func(key string, subs []string) {
		fmt.Fprintf(&b, "    %s:\n", key)
		for _, s := range subs {
			fmt.Fprintf(&b, "      - %q\n", s)
		}
	}
	list("allowed_subs", f.allow)
	if len(f.admin) > 0 {
		list("admin_subs", f.admin)
	}
	if f.legacyOwner != "" {
		fmt.Fprintf(&b, "    legacy_owner_sub: %q\n", f.legacyOwner)
	}
	return b.String()
}

func (f *oidcFixture) attach(e *env) (UserData, func() time.Time) {
	return hookedUser{UserData: e.user, real: e.user, spy: f.hook}, f.now
}

// loginHookSpy records the calls to user.db's login entry points and passes
// them on, failing the copy while failMigrate is set.
type loginHookSpy struct {
	mu          sync.Mutex
	logins      []string
	migrated    []string
	failMigrate error
}

func (h *loginHookSpy) calls() (logins, migrated []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.logins...), append([]string(nil), h.migrated...)
}

type hookedUser struct {
	UserData
	real *userdata.DB
	spy  *loginHookSpy
}

func (u hookedUser) RecordLogin(ctx context.Context, sub string) error {
	u.spy.mu.Lock()
	u.spy.logins = append(u.spy.logins, sub)
	u.spy.mu.Unlock()
	return u.real.RecordLogin(ctx, sub)
}

func (u hookedUser) MigrateLegacyOwner(ctx context.Context, sub string) (bool, error) {
	u.spy.mu.Lock()
	u.spy.migrated = append(u.spy.migrated, sub)
	fail := u.spy.failMigrate
	u.spy.mu.Unlock()
	if fail != nil {
		return false, fail
	}
	return u.real.MigrateLegacyOwner(ctx, sub)
}

// browser is a cookie jar for one user agent: it sends what it holds and
// keeps what the server sets, dropping expired cookies.
type browser struct {
	cookies map[string]string
	ip      string
}

func newBrowser() *browser { return &browser{cookies: map[string]string{}, ip: "10.0.0.1"} }

func (b *browser) send(r *http.Request) {
	for name, v := range b.cookies {
		r.AddCookie(&http.Cookie{Name: name, Value: v})
	}
	r.RemoteAddr = b.ip + ":54321"
}

func (b *browser) keep(w *httptest.ResponseRecorder) {
	for _, c := range w.Result().Cookies() {
		if c.MaxAge < 0 || c.Value == "" {
			delete(b.cookies, c.Name)
			continue
		}
		b.cookies[c.Name] = c.Value
	}
}

// sameSite makes a request look like the SPA's own fetch.
func sameSite(r *http.Request) { r.Header.Set("Origin", oidcOrigin) }

func (b *browser) get(e *env, target string, mutate ...func(*http.Request)) *httptest.ResponseRecorder {
	e.t.Helper()
	w := e.get(target, append([]func(*http.Request){b.send}, mutate...)...)
	b.keep(w)
	return w
}

func (b *browser) post(e *env, target string, mutate ...func(*http.Request)) *httptest.ResponseRecorder {
	e.t.Helper()
	w := e.do(http.MethodPost, target, nil, append([]func(*http.Request){b.send, sameSite}, mutate...)...)
	b.keep(w)
	return w
}

// start begins a login and returns the 302 to the provider.
func (f *oidcFixture) start(e *env, b *browser, query string) string {
	e.t.Helper()
	w := b.get(e, e.cfg.Server.BasePath+"/api/auth/oidc/start"+query)
	if w.Code != http.StatusFound {
		e.t.Fatalf("start = %d, want 302; body %s", w.Code, w.Body.String())
	}
	return w.Header().Get("Location")
}

// login signs b in as sub and returns where the callback sent it.
func (f *oidcFixture) login(e *env, b *browser, sub string) string {
	e.t.Helper()
	cb := f.prov.Authorize(e.t, f.start(e, b, ""), sub)
	w := b.get(e, cb)
	if w.Code != http.StatusFound {
		e.t.Fatalf("callback = %d, want 302; body %s", w.Code, w.Body.String())
	}
	return w.Header().Get("Location")
}

// reauth runs `reauth=1` as sub and returns the callback's response.
func (f *oidcFixture) reauth(e *env, b *browser, sub string) *httptest.ResponseRecorder {
	e.t.Helper()
	loc := f.start(e, b, "?reauth=1&return=/settings")
	if got := f.prov.AuthorizeQuery(e.t, loc).Get("prompt"); got != "login" {
		e.t.Fatalf("reauth prompt = %q, want login", got)
	}
	return b.get(e, f.prov.Authorize(e.t, loc, sub))
}
