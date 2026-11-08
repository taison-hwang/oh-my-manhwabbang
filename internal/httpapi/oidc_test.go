package httpapi

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"shelf/internal/auth/oidc/oidctest"
	"shelf/internal/scanner"
)

// Ruling E-74 / amendment A-20: `auth.mode: oidc` against a fake uni-auth.

func authStatusOf(t *testing.T, e *env, b *browser) AuthStatus {
	t.Helper()
	return decodeBody[AuthStatus](t, b.get(e, "/api/auth/status"), http.StatusOK)
}

func wantReason(t *testing.T, w *httptest.ResponseRecorder, status int, code, reason string) {
	t.Helper()
	body := errorBody(t, w, status, code)
	if got, _ := body.Detail["reason"].(string); got != reason {
		t.Fatalf("detail.reason = %q, want %q; body %s", got, reason, w.Body.String())
	}
}

func TestOIDC_loginFlow(t *testing.T) {
	e, f := newOIDCEnv(t)
	b := newBrowser()

	before := authStatusOf(t, e, b)
	if !before.AuthRequired || before.Authenticated || before.Mode != "oidc" || before.User != nil {
		t.Fatalf("status before login = %+v", before)
	}
	if !before.EndSessionAvailable {
		t.Error("end_session_available = false, but the provider has end_session_endpoint")
	}

	startW := b.get(e, "/api/auth/oidc/start?return="+url.QueryEscape("/series/abc?sort=name"))
	loc := startW.Header().Get("Location")
	var pre *http.Cookie
	for _, c := range startW.Result().Cookies() {
		if c.Name == "__Host-shelf_login" {
			pre = c
		}
	}
	if pre == nil || !pre.HttpOnly || !pre.Secure || pre.SameSite != http.SameSiteLaxMode || pre.Path != "/" || pre.MaxAge != 600 {
		t.Fatalf("pre-auth cookie = %+v, want __Host- HttpOnly Secure Lax Path=/ Max-Age=600", pre)
	}
	got := b.get(e, f.prov.Authorize(t, loc, subReader))
	if got.Code != http.StatusFound || got.Header().Get("Location") != "/series/abc?sort=name" {
		t.Fatalf("callback = %d to %q, want 302 to the return path", got.Code, got.Header().Get("Location"))
	}
	var sid *http.Cookie
	for _, c := range got.Result().Cookies() {
		if c.Name == "__Host-shelf_sid" {
			sid = c
		}
	}
	if sid == nil || !sid.HttpOnly || !sid.Secure || sid.SameSite != http.SameSiteLaxMode || sid.Path != "/" || sid.Domain != "" {
		t.Fatalf("session cookie = %+v, want __Host- HttpOnly Secure Lax Path=/", sid)
	}
	if _, ok := b.cookies["__Host-shelf_login"]; ok {
		t.Error("the pre-auth cookie survived the callback")
	}

	after := authStatusOf(t, e, b)
	if !after.Authenticated || after.User == nil || after.User.Sub != subReader || after.User.IsAdmin {
		t.Fatalf("status after login = %+v", after)
	}
	w := b.get(e, "/api/series")
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "private, no-cache" {
		t.Fatalf("GET /api/series with a session = %d, Cache-Control %q", w.Code, w.Header().Get("Cache-Control"))
	}
	logins, migrated := f.hook.calls()
	if !slices.Equal(logins, []string{subReader}) || len(migrated) != 0 {
		t.Errorf("login hook calls = %v / %v, want a login for %s and no copy (no legacy_owner_sub)", logins, migrated, subReader)
	}
}

func TestOIDC_loginCSRFAndBindingMismatches(t *testing.T) {
	e, f := newOIDCEnv(t)

	t.Run("a callback in another browser changes nothing", func(t *testing.T) {
		victim, attacker := newBrowser(), newBrowser()
		cb := f.prov.Authorize(t, f.start(e, attacker, ""), subReader)
		for name, v := range map[string]*browser{"no pre-auth cookie": victim, "its own login in flight": nil} {
			if v == nil {
				v = newBrowser()
				f.start(e, v, "")
			}
			w := v.get(e, cb)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("%s: foreign callback = %d, want 400 (start again)", name, w.Code)
			}
			if _, ok := v.cookies["__Host-shelf_sid"]; ok {
				t.Fatalf("%s: the victim was signed in to the attacker's account", name)
			}
		}
		if w := attacker.get(e, cb); w.Code != http.StatusFound {
			t.Fatalf("the forged attempt spent the attacker's own login: %d", w.Code)
		}
	})

	t.Run("unknown state", func(t *testing.T) {
		b := newBrowser()
		loc := f.start(e, b, "")
		cb := f.prov.Authorize(t, loc, subReader)
		forged := strings.Replace(cb, "state="+url.QueryEscape(f.prov.AuthorizeQuery(t, loc).Get("state")), "state=forged", 1)
		if w := b.get(e, forged); w.Code != http.StatusBadRequest {
			t.Fatalf("callback with a forged state = %d, want 400", w.Code)
		}
	})

	t.Run("nonce mismatch", func(t *testing.T) {
		f.prov.Update(func(s *oidctest.Settings) {
			s.Tamper = func(kind string, _, c map[string]any) {
				if kind == "id" {
					c["nonce"] = "someone-elses"
				}
			}
		})
		t.Cleanup(func() { f.prov.Update(func(s *oidctest.Settings) { s.Tamper = nil }) })
		b := newBrowser()
		w := b.get(e, f.prov.Authorize(t, f.start(e, b, ""), subReader))
		if w.Code != http.StatusBadGateway || b.cookies["__Host-shelf_sid"] != "" {
			t.Fatalf("nonce mismatch = %d with cookies %v, want 502 and no session", w.Code, b.cookies)
		}
	})

	t.Run("PKCE mismatch", func(t *testing.T) {
		// A code issued to another login's challenge: the verifier this
		// browser's entry holds does not hash to it.
		b, other := newBrowser(), newBrowser()
		loc := f.start(e, b, "")
		otherCB := f.prov.Authorize(t, f.start(e, other, ""), subReader)
		code := mustQuery(t, otherCB).Get("code")
		cb := "/api/auth/oidc/callback?" + url.Values{"code": {code}, "state": {f.prov.AuthorizeQuery(t, loc).Get("state")}}.Encode()
		w := b.get(e, cb)
		if w.Code != http.StatusBadGateway || b.cookies["__Host-shelf_sid"] != "" {
			t.Fatalf("PKCE mismatch = %d, want 502 and no session", w.Code)
		}
	})
}

func mustQuery(t *testing.T, target string) url.Values {
	t.Helper()
	u, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query()
}

func TestOIDC_tokenValidation(t *testing.T) {
	_, otherKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	both := func(fn func(c map[string]any)) func(string, map[string]any, map[string]any) {
		return func(_ string, _, c map[string]any) { fn(c) }
	}
	cases := []struct {
		name   string
		set    func(*oidctest.Settings)
		status int
	}{
		{"signature by another key", func(s *oidctest.Settings) { s.SignWith = otherKey }, http.StatusBadGateway},
		{"unknown kid", func(s *oidctest.Settings) {
			s.Tamper = func(_ string, h, _ map[string]any) { h["kid"] = "nobody" }
		}, http.StatusBadGateway},
		{"alg none", func(s *oidctest.Settings) {
			s.Tamper = func(_ string, h, _ map[string]any) { h["alg"] = "none" }
		}, http.StatusBadGateway},
		{"issuer", func(s *oidctest.Settings) { s.Tamper = both(func(c map[string]any) { c["iss"] = "https://evil.test" }) }, http.StatusBadGateway},
		{"ID token audience", func(s *oidctest.Settings) {
			s.Tamper = func(k string, _, c map[string]any) {
				if k == "id" {
					c["aud"] = "another-client"
				}
			}
		}, http.StatusBadGateway},
		{"access token audience", func(s *oidctest.Settings) {
			s.Tamper = func(k string, _, c map[string]any) {
				if k == "access" {
					c["aud"] = []string{"https://other.test/api"}
				}
			}
		}, http.StatusBadGateway},
		{"expired", func(s *oidctest.Settings) {
			s.Tamper = both(func(c map[string]any) { c["exp"] = fixedNow.Add(-2 * time.Minute).Unix() })
		}, http.StatusBadGateway},
		{"no exp", func(s *oidctest.Settings) { s.Tamper = both(func(c map[string]any) { delete(c, "exp") }) }, http.StatusBadGateway},
		{"no iat", func(s *oidctest.Settings) { s.Tamper = both(func(c map[string]any) { delete(c, "iat") }) }, http.StatusBadGateway},
		{"not yet valid", func(s *oidctest.Settings) {
			s.Tamper = both(func(c map[string]any) { c["nbf"] = fixedNow.Add(2 * time.Minute).Unix() })
		}, http.StatusBadGateway},
		{"issued in the future", func(s *oidctest.Settings) {
			s.Tamper = both(func(c map[string]any) { c["iat"] = fixedNow.Add(2 * time.Minute).Unix() })
		}, http.StatusBadGateway},
		{"empty sub", func(s *oidctest.Settings) { s.Tamper = both(func(c map[string]any) { c["sub"] = "" }) }, http.StatusBadGateway},
		{"tenant", func(s *oidctest.Settings) { s.Tamper = both(func(c map[string]any) { c["tid"] = "other" }) }, http.StatusBadGateway},
		{"subs differ between the tokens", func(s *oidctest.Settings) {
			s.Tamper = func(k string, _, c map[string]any) {
				if k == "access" {
					c["sub"] = subAdmin
				}
			}
		}, http.StatusBadGateway},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, f := newOIDCEnv(t)
			b := newBrowser()
			loc := f.start(e, b, "")
			f.prov.Update(tc.set)
			w := b.get(e, f.prov.Authorize(t, loc, subReader))
			if w.Code != tc.status {
				t.Fatalf("callback = %d, want %d", w.Code, tc.status)
			}
			if b.cookies["__Host-shelf_sid"] != "" {
				t.Fatal("a session was created from a token that should have been refused")
			}
		})
	}

	t.Run("within the clock skew is accepted", func(t *testing.T) {
		e, f := newOIDCEnv(t)
		b := newBrowser()
		f.prov.Update(func(s *oidctest.Settings) {
			s.Tamper = both(func(c map[string]any) { c["iat"] = fixedNow.Add(50 * time.Second).Unix() })
		})
		f.login(e, b, subReader)
	})

	t.Run("discovery naming another issuer", func(t *testing.T) {
		e, f := newOIDCEnv(t)
		f.prov.Update(func(s *oidctest.Settings) { s.DiscoveryIssuer = "https://evil.test" })
		b := newBrowser()
		if w := b.get(e, "/api/auth/oidc/start"); w.Code != http.StatusBadGateway {
			t.Fatalf("start against a mismatched discovery document = %d, want 502", w.Code)
		}
	})

	t.Run("provider down", func(t *testing.T) {
		e, f := newOIDCEnv(t)
		f.prov.Update(func(s *oidctest.Settings) { s.Down = true })
		b := newBrowser()
		w := b.get(e, "/api/auth/oidc/start")
		if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "인증 서버에 연결할 수 없습니다") {
			t.Fatalf("start with uni-auth down = %d, want 503 and the unavailable page", w.Code)
		}
	})
}

func TestOIDC_allowList(t *testing.T) {
	e, f := newOIDCEnv(t)

	t.Run("a sub outside allowed_subs gets no session", func(t *testing.T) {
		b := newBrowser()
		w := b.get(e, f.prov.Authorize(t, f.start(e, b, ""), subStranger))
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), subStranger) {
			t.Fatalf("callback for a stranger = %d, want 403 showing the sub; body %s", w.Code, w.Body.String())
		}
		if b.cookies["__Host-shelf_sid"] != "" {
			t.Fatal("a stranger got a session cookie")
		}
		if logins, _ := f.hook.calls(); slices.Contains(logins, subStranger) {
			t.Fatal("a refused login was recorded in user.db")
		}
	})

	t.Run("a sub removed from allowed_subs loses its session", func(t *testing.T) {
		b := newBrowser()
		f.login(e, b, subReader)
		// The service reads the configured list on every request; this
		// stands in for an operator editing it.
		e.cfg.Auth.OIDC.AllowedSubs[0] = "sub-somebody-else"
		t.Cleanup(func() { e.cfg.Auth.OIDC.AllowedSubs[0] = subReader })
		wantReason(t, b.get(e, "/api/series"), http.StatusForbidden, CodeForbidden, "not_allowed")
		errorBody(t, b.get(e, "/api/series"), http.StatusUnauthorized, CodeUnauthorized)
	})
}

func TestOIDC_adminAndReauth(t *testing.T) {
	e, f := newOIDCEnv(t)
	reader, admin := newBrowser(), newBrowser()
	f.login(e, reader, subReader)
	f.login(e, admin, subAdmin)

	adminWrites := []struct{ method, target string }{
		{http.MethodPost, "/api/scan"},
		{http.MethodPost, "/api/scan/cancel"},
		{http.MethodPost, "/api/series/" + e.seriesFolderID + "/rescan"},
		{http.MethodDelete, "/api/cache"},
		{http.MethodPost, "/api/roots"},
		{http.MethodDelete, "/api/roots/" + rootName},
		{http.MethodGet, "/api/browse"},
	}
	send := func(b *browser, method, target string) *httptest.ResponseRecorder {
		w := e.do(method, target, nil, b.send, sameSite)
		b.keep(w)
		return w
	}
	for _, ep := range adminWrites {
		t.Run("reader "+ep.method+" "+ep.target, func(t *testing.T) {
			wantReason(t, send(reader, ep.method, ep.target), http.StatusForbidden, CodeForbidden, "admin_only")
		})
		t.Run("admin without reauth "+ep.method+" "+ep.target, func(t *testing.T) {
			wantReason(t, send(admin, ep.method, ep.target), http.StatusForbidden, CodeForbidden, "reauth_required")
		})
	}
	wantReason(t, reader.get(e, "/api/scan/log"), http.StatusForbidden, CodeForbidden, "admin_only")
	if w := admin.get(e, "/api/scan/log"); w.Code != http.StatusOK {
		t.Fatalf("admin GET /api/scan/log = %d, want 200 (admin only, no reauth)", w.Code)
	}

	t.Run("reauth as another sub changes nothing", func(t *testing.T) {
		w := f.reauth(e, admin, subReader)
		if w.Code != http.StatusForbidden {
			t.Fatalf("reauth as another sub = %d, want 403", w.Code)
		}
		wantReason(t, send(admin, http.MethodPost, "/api/scan"), http.StatusForbidden, CodeForbidden, "reauth_required")
		if st := authStatusOf(t, e, admin); st.User == nil || st.User.Sub != subAdmin {
			t.Fatalf("the session changed hands: %+v", st)
		}
	})

	t.Run("reauth refused by the provider", func(t *testing.T) {
		loc := f.start(e, admin, "?reauth=1")
		if w := admin.get(e, f.prov.Deny(t, loc, "login_required")); w.Code != http.StatusForbidden {
			t.Fatalf("a refused reauth = %d, want 403", w.Code)
		}
		wantReason(t, send(admin, http.MethodPost, "/api/scan"), http.StatusForbidden, CodeForbidden, "reauth_required")
	})

	t.Run("reauth opens the window for fifteen minutes", func(t *testing.T) {
		w := f.reauth(e, admin, subAdmin)
		if w.Code != http.StatusFound || w.Header().Get("Location") != "/settings" {
			t.Fatalf("reauth callback = %d to %q, want 302 to /settings", w.Code, w.Header().Get("Location"))
		}
		if got := send(admin, http.MethodPost, "/api/scan/cancel"); got.Code == http.StatusForbidden {
			t.Fatalf("admin write after reauth = 403: %s", got.Body.String())
		}
		f.advance(16 * time.Minute)
		wantReason(t, send(admin, http.MethodPost, "/api/scan/cancel"), http.StatusForbidden, CodeForbidden, "reauth_required")
	})

	t.Run("reauth replaces the session id", func(t *testing.T) {
		old := admin.cookies["__Host-shelf_sid"]
		if w := f.reauth(e, admin, subAdmin); w.Code != http.StatusFound {
			t.Fatalf("reauth = %d", w.Code)
		}
		if admin.cookies["__Host-shelf_sid"] == old {
			t.Fatal("the session id survived the re-authentication")
		}
		stale := &browser{cookies: map[string]string{"__Host-shelf_sid": old}, ip: "10.0.0.1"}
		errorBody(t, stale.get(e, "/api/series"), http.StatusUnauthorized, CodeUnauthorized)
		if got := send(admin, http.MethodPost, "/api/scan/cancel"); got.Code == http.StatusForbidden {
			t.Fatalf("admin write with the new id = 403: %s", got.Body.String())
		}
	})

	t.Run("reauth finished under another session of the same sub", func(t *testing.T) {
		// The entry is bound to the session that started it; another
		// session of the same user must not collect the window.
		other := newBrowser()
		f.login(e, other, subAdmin)
		starter := newBrowser()
		f.login(e, starter, subAdmin)
		loc := f.start(e, starter, "?reauth=1")
		mixed := &browser{ip: "10.0.0.1", cookies: map[string]string{
			"__Host-shelf_login": starter.cookies["__Host-shelf_login"],
			"__Host-shelf_sid":   other.cookies["__Host-shelf_sid"],
		}}
		if w := mixed.get(e, f.prov.Authorize(t, loc, subAdmin)); w.Code != http.StatusForbidden {
			t.Fatalf("reauth under another session = %d, want 403", w.Code)
		}
		wantReason(t, send(other, http.MethodPost, "/api/scan/cancel"), http.StatusForbidden, CodeForbidden, "reauth_required")
	})

	t.Run("reauth needs a live session", func(t *testing.T) {
		b := newBrowser()
		if w := b.get(e, "/api/auth/oidc/start?reauth=1"); w.Code != http.StatusUnauthorized {
			t.Fatalf("reauth without a session = %d, want 401", w.Code)
		}
	})
}

func TestOIDC_sameOriginBeforeAuthentication(t *testing.T) {
	e, f := newOIDCEnv(t)
	b := newBrowser()
	f.login(e, b, subAdmin)

	cases := map[string]func(*http.Request){
		"no Origin and no Sec-Fetch-Site": func(*http.Request) {},
		"a foreign Origin":                func(r *http.Request) { r.Header.Set("Origin", "https://evil.betterax.net") },
		"Origin null":                     func(r *http.Request) { r.Header.Set("Origin", "null") },
		"Sec-Fetch-Site same-site":        func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-site") },
		"Sec-Fetch-Site cross-site":       func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			wantReason(t, e.do(http.MethodPost, "/api/auth/logout", nil, b.send, mutate), http.StatusForbidden, CodeForbidden, "cross_origin")
			// Without a session too: cross_origin, not 401.
			wantReason(t, e.do(http.MethodPost, "/api/scan", nil, mutate), http.StatusForbidden, CodeForbidden, "cross_origin")
		})
	}
	if st := authStatusOf(t, e, b); !st.Authenticated {
		t.Fatal("a refused cross-origin logout signed the user out")
	}
	w := e.do(http.MethodPost, "/api/auth/logout", nil, b.send, func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-origin") })
	if w.Code != http.StatusNoContent {
		t.Fatalf("same-origin logout by Sec-Fetch-Site = %d, want 204", w.Code)
	}
}

func TestOIDC_returnIsNeverAnOpenRedirect(t *testing.T) {
	e, f := newOIDCEnv(t)
	cases := map[string]string{
		"//evil.test/x":         "/",
		"/\\evil.test":          "/",
		"https://evil.test/":    "/",
		"javascript:alert(1)":   "/",
		"/series/a\r\nSet-X: y": "/",
		"/series/\x00":          "/",
		"/api/auth/logout":      "/",
		"/api/series":           "/",
		"series/relative":       "/",
		"":                      "/",
		"/series/abc?x=1#top":   "/series/abc?x=1#top",
	}
	for ret, want := range cases {
		t.Run(ret, func(t *testing.T) {
			b := newBrowser()
			b.ip = "10.9.0." + string(rune('1'+len(ret)%8))
			cb := f.prov.Authorize(t, f.start(e, b, "?return="+url.QueryEscape(ret)), subReader)
			w := b.get(e, cb)
			if w.Code != http.StatusFound || w.Header().Get("Location") != want {
				t.Fatalf("return %q led to %d %q, want %q", ret, w.Code, w.Header().Get("Location"), want)
			}
		})
	}
}

func TestOIDC_preAuthLimit(t *testing.T) {
	e, _ := newOIDCEnv(t)
	b := newBrowser()
	for i := range 20 {
		if w := b.get(e, "/api/auth/oidc/start"); w.Code != http.StatusFound {
			t.Fatalf("start %d = %d, want 302", i+1, w.Code)
		}
	}
	w := b.get(e, "/api/auth/oidc/start")
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" {
		t.Fatalf("21st start in a minute = %d (Retry-After %q), want 429 with Retry-After", w.Code, w.Header().Get("Retry-After"))
	}
	other := newBrowser()
	other.ip = "10.0.0.2"
	if w := other.get(e, "/api/auth/oidc/start"); w.Code != http.StatusFound {
		t.Fatalf("another address = %d, want 302 — the limit is per IP", w.Code)
	}
}

func TestOIDC_callbackRetry(t *testing.T) {
	e, f := newOIDCEnv(t)
	b := newBrowser()
	cb := f.prov.Authorize(t, f.start(e, b, "?return=/settings"), subReader)
	if w := b.get(e, cb); w.Code != http.StatusFound {
		t.Fatalf("callback = %d", w.Code)
	}
	sid := b.cookies["__Host-shelf_sid"]
	w := b.get(e, cb)
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/" || b.cookies["__Host-shelf_sid"] != sid {
		t.Fatalf("replayed callback with a live session = %d to %q, want 302 to / and the same session", w.Code, w.Header().Get("Location"))
	}
	if w := newBrowser().get(e, cb); w.Code != http.StatusBadRequest {
		t.Fatalf("replayed callback without a session = %d, want 400", w.Code)
	}
}

func TestOIDC_expiredSessionAndPromptNone(t *testing.T) {
	e, f := newOIDCEnv(t)
	b := newBrowser()
	f.login(e, b, subReader)
	f.advance(13 * time.Hour)
	wantReason(t, b.get(e, "/api/series"), http.StatusUnauthorized, CodeUnauthorized, "session_expired")

	loc := f.start(e, b, "")
	if got := f.prov.AuthorizeQuery(t, loc).Get("prompt"); got != "none" {
		t.Fatalf("start after expiry prompt = %q, want none", got)
	}
	// login_required falls back once to an interactive login.
	w := b.get(e, f.prov.Deny(t, loc, "login_required"))
	if w.Code != http.StatusFound {
		t.Fatalf("prompt=none fallback = %d, want 302", w.Code)
	}
	next := w.Header().Get("Location")
	if got := f.prov.AuthorizeQuery(t, next).Get("prompt"); got != "" {
		t.Fatalf("fallback prompt = %q, want none set", got)
	}
	if w := b.get(e, f.prov.Deny(t, next, "login_required")); w.Code != http.StatusBadGateway {
		t.Fatalf("a second login_required = %d, want 502 — the fallback runs once", w.Code)
	}
}

func TestOIDC_logout(t *testing.T) {
	t.Run("local logout", func(t *testing.T) {
		e, f := newOIDCEnv(t)
		b := newBrowser()
		f.login(e, b, subReader)
		old := b.cookies["__Host-shelf_sid"]
		if w := b.post(e, "/api/auth/logout"); w.Code != http.StatusNoContent {
			t.Fatalf("logout = %d, want 204", w.Code)
		}
		replay := &browser{cookies: map[string]string{"__Host-shelf_sid": old}, ip: "10.0.0.1"}
		errorBody(t, replay.get(e, "/api/series"), http.StatusUnauthorized, CodeUnauthorized)
		if st := authStatusOf(t, e, b); st.Authenticated {
			t.Fatal("still authenticated after logout")
		}
	})

	t.Run("form logout renders the completion page", func(t *testing.T) {
		e, f := newOIDCEnv(t)
		b := newBrowser()
		f.login(e, b, subReader)
		w := b.post(e, "/api/auth/logout", func(r *http.Request) {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		})
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "로그아웃했습니다") ||
			!strings.Contains(w.Body.String(), "/api/auth/oidc/end-session") {
			t.Fatalf("form logout = %d; body %s", w.Code, w.Body.String())
		}
	})

	t.Run("logout from every service", func(t *testing.T) {
		e, f := newOIDCEnv(t)
		b := newBrowser()
		f.login(e, b, subReader)
		w := b.post(e, "/api/auth/oidc/end-session")
		loc := w.Header().Get("Location")
		if w.Code != http.StatusFound || !strings.HasPrefix(loc, f.prov.URL+"/logout?") {
			t.Fatalf("end-session = %d to %q, want 302 to the provider", w.Code, loc)
		}
		if _, ok := oidctest.IDTokenHint(loc); !ok {
			t.Errorf("end-session URL has no id_token_hint: %s", loc)
		}
		if mustQuery(t, loc).Has("post_logout_redirect_uri") {
			t.Errorf("post_logout_redirect_uri sent while unconfigured: %s", loc)
		}
		if st := authStatusOf(t, e, b); st.Authenticated {
			t.Fatal("still authenticated after end-session")
		}
	})

	t.Run("end-session after the logout grace sends no token", func(t *testing.T) {
		e, f := newOIDCEnv(t)
		b := newBrowser()
		f.login(e, b, subReader)
		old := b.cookies["__Host-shelf_sid"]
		b.post(e, "/api/auth/logout")
		f.advance(11 * time.Minute)
		stale := &browser{cookies: map[string]string{"__Host-shelf_sid": old}, ip: "10.0.0.1"}
		w := stale.post(e, "/api/auth/oidc/end-session")
		if w.Code != http.StatusOK || w.Header().Get("Location") != "" {
			t.Fatalf("end-session 11 minutes after logout = %d to %q, want 200 and no redirect", w.Code, w.Header().Get("Location"))
		}
	})

	t.Run("end-session within the logout grace", func(t *testing.T) {
		e, f := newOIDCEnv(t)
		b := newBrowser()
		f.login(e, b, subReader)
		b.post(e, "/api/auth/logout")
		f.advance(5 * time.Minute)
		if _, ok := oidctest.IDTokenHint(b.post(e, "/api/auth/oidc/end-session").Header().Get("Location")); !ok {
			t.Fatal("end-session within ten minutes of logout lost the id_token_hint")
		}
	})

	t.Run("post_logout_redirect_url when configured", func(t *testing.T) {
		f := newOIDCFixture(t)
		f.postLogout = oidcOrigin + "/"
		e := newEnv(t, withStatic(), withOIDC(f))
		b := newBrowser()
		f.login(e, b, subReader)
		loc := b.post(e, "/api/auth/oidc/end-session").Header().Get("Location")
		if got := mustQuery(t, loc).Get("post_logout_redirect_uri"); got != oidcOrigin+"/" {
			t.Fatalf("post_logout_redirect_uri = %q", got)
		}
	})

	t.Run("no end_session_endpoint", func(t *testing.T) {
		e, f := newOIDCEnv(t)
		f.prov.Update(func(s *oidctest.Settings) { s.EndSession = false })
		b := newBrowser()
		f.login(e, b, subReader)
		if st := authStatusOf(t, e, b); st.EndSessionAvailable {
			t.Error("end_session_available = true without an end_session_endpoint")
		}
	})
}

func TestOIDC_noBypass(t *testing.T) {
	e, f := newOIDCEnv(t)

	t.Run("password login is refused", func(t *testing.T) {
		w := e.do(http.MethodPost, "/api/auth/login", strings.NewReader(`{"password":"x"}`), sameSite,
			func(r *http.Request) { r.Header.Set("Content-Type", "application/json") })
		wantReason(t, w, http.StatusBadRequest, CodeBadRequest, "oidc_mode")
	})

	t.Run("a password-mode cookie is not a session", func(t *testing.T) {
		pw := newEnv(t, withPassword("hunter2"))
		c := loginCookie(t, pw, "hunter2")
		errorBody(t, e.get("/api/series", withCookie(c)), http.StatusUnauthorized, CodeUnauthorized)
	})

	t.Run("an oidc cookie is not a password-mode session", func(t *testing.T) {
		b := newBrowser()
		f.login(e, b, subAdmin)
		pw := newEnv(t, withPassword("hunter2"))
		errorBody(t, pw.get("/api/series", b.send), http.StatusUnauthorized, CodeUnauthorized)
	})

	gated := []string{
		"/", "/series/abc", "/assets/index-abc123.js", "/api/series", "/api/settings", "/api/roots",
		"/api/books/" + e.bookZipID + "/pages/1", "/api/books/" + e.bookZipID + "/thumbs/1",
		"/api/series/" + e.seriesFolderID + "/cover", "/api/scan/status", "/api/progress/export",
		"/api/auth/oidc/startx", "/api/auth/oidc/callback/", "/api/health/", "/api/auth/statusx",
		// ServeMux routes on the escaped path, so these reach the SPA.
		"/api%2Fhealth", "/api%2fhealth", "/api%2Fauth%2Fstatus", "/api%2Fauth%2Foidc%2Fstart", "/api/auth%2Fstatus",
	}
	for _, target := range gated {
		t.Run("gated "+target, func(t *testing.T) {
			w := e.get(target)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("GET %s without a session = %d, want 401", target, w.Code)
			}
			if strings.Contains(w.Body.String(), "console.log") || strings.Contains(w.Body.String(), `id="root"`) {
				t.Fatalf("GET %s leaked the SPA without a session", target)
			}
		})
	}
	t.Run("a browser gets the login page", func(t *testing.T) {
		w := e.get("/series/abc", asHTML)
		if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "uni-auth로 로그인") ||
			!strings.Contains(w.Body.String(), "/api/auth/oidc/start?return=%2Fseries%2Fabc") {
			t.Fatalf("login page = %d; body %s", w.Code, w.Body.String())
		}
	})

	exempt := []string{"/api/health", "/api/auth/status"}
	for _, target := range exempt {
		if w := e.get(target); w.Code != http.StatusOK {
			t.Errorf("GET %s without a session = %d, want 200", target, w.Code)
		}
	}
	for _, p := range []string{
		"/api/health", "/api/auth/status", "/api/auth/login", "/api/auth/logout",
		"/api/auth/oidc/start", "/api/auth/oidc/callback", "/api/auth/oidc/end-session",
	} {
		if !oidcExempt(p) {
			t.Errorf("%s is not exempt", p)
		}
	}
	for _, p := range []string{"/", "/api/auth/oidc", "/api/auth/oidc/", "/api/auth/oidc/start/", "/api/healthz", "/assets/x.js"} {
		if oidcExempt(p) {
			t.Errorf("%s is exempt", p)
		}
	}

	t.Run("verbose health needs a session", func(t *testing.T) {
		anon := decodeBody[map[string]any](t, e.get("/api/health?verbose=1"), http.StatusOK)
		b := newBrowser()
		f.login(e, b, subReader)
		signed := decodeBody[map[string]any](t, b.get(e, "/api/health?verbose=1"), http.StatusOK)
		if len(anon) >= len(signed) {
			t.Fatalf("verbose health without a session has %d fields, with one %d", len(anon), len(signed))
		}
	})
}

func TestOIDC_requestLogCarriesNoSecrets(t *testing.T) {
	e, f := newOIDCEnv(t)
	var buf bytes.Buffer
	e.srv.log = slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	b := newBrowser()
	loc := f.start(e, b, "")
	q := f.prov.AuthorizeQuery(t, loc)
	cb := f.prov.Authorize(t, loc, subReader)
	b.get(e, cb)
	b.post(e, "/api/auth/oidc/end-session")
	out := buf.String()
	secrets := map[string]string{
		"code": mustQuery(t, cb).Get("code"), "state": q.Get("state"), "nonce": q.Get("nonce"),
		"session cookie": b.cookies["__Host-shelf_sid"],
	}
	for name, v := range secrets {
		if v != "" && strings.Contains(out, v) {
			t.Errorf("the log carries the %s", name)
		}
	}
	if strings.Contains(out, "eyJ") {
		t.Error("the log carries a JWT")
	}
	if !strings.Contains(out, "/api/auth/oidc/callback") {
		t.Fatalf("the callback was not logged at all:\n%s", out)
	}
}

func TestOIDC_hidesServerPathsFromReaders(t *testing.T) {
	e, f := newOIDCEnv(t)
	reader, admin := newBrowser(), newBrowser()
	f.login(e, reader, subReader)
	f.login(e, admin, subAdmin)

	rs := decodeBody[RootsResponse](t, reader.get(e, "/api/roots"), http.StatusOK)
	for _, r := range rs.Items {
		if r.Path != "" || r.LastScanError != nil {
			t.Errorf("a reader sees root %q path %q / error %v", r.Name, r.Path, r.LastScanError)
		}
	}
	if as := decodeBody[RootsResponse](t, admin.get(e, "/api/roots"), http.StatusOK); as.Items[0].Path == "" {
		t.Error("an admin does not see the root path")
	}
	if cu := decodeBody[CacheUsage](t, reader.get(e, "/api/cache/usage"), http.StatusOK); cu.CacheDir != "" {
		t.Errorf("a reader sees cache_dir %q", cu.CacheDir)
	}
	st := decodeBody[Settings](t, reader.get(e, "/api/settings"), http.StatusOK)
	if st.Server.ConfigPath != "" || st.Server.AuthMode != "oidc" || !st.Server.AuthEnabled {
		t.Errorf("reader settings server = config_path %q auth_mode %q auth_enabled %t",
			st.Server.ConfigPath, st.Server.AuthMode, st.Server.AuthEnabled)
	}
	if as := decodeBody[Settings](t, admin.get(e, "/api/settings"), http.StatusOK); as.Server.ConfigPath == "" {
		t.Error("an admin does not see config_path")
	}
}

// Ruling E-74 §2.18 and the Orchestrator's condition on the response wrapper:
// with a session, no response may leave with `public` in Cache-Control,
// including late-header and 304 paths.
func TestOIDC_noPublicCacheControl(t *testing.T) {
	e, f := newOIDCEnv(t)
	b := newBrowser()
	f.login(e, b, subReader)

	// check wants 200 or 304 with a Cache-Control that says private: the
	// handlers below all write `public` in the other modes, so a missing
	// `private` means the rewrite never ran.
	check := func(t *testing.T, target string, want int, mutate ...func(*http.Request)) *httptest.ResponseRecorder {
		t.Helper()
		w := b.get(e, target, mutate...)
		cc := w.Header().Get("Cache-Control")
		if w.Code != want || !strings.Contains(cc, "private") || strings.Contains(strings.ToLower(cc), "public") {
			t.Fatalf("GET %s = %d with Cache-Control %q, want %d and private", target, w.Code, cc, want)
		}
		return w
	}
	images := []string{
		"/api/books/" + e.bookZipID + "/pages/1",
		"/api/books/" + e.bookZipID + "/pages/1?v=" + cvZip,
		"/api/books/" + e.bookZipID + "/thumbs/1",
		"/api/books/" + e.bookZipID + "/cover",
		"/api/series/" + e.seriesFolderID + "/cover",
	}
	// Thumbnails and covers answer 202 until generated; the 200 that follows
	// is the late-header path.
	for _, target := range images {
		b.get(e, target)
	}
	if err := e.thumbs.Drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, target := range images {
		t.Run(target, func(t *testing.T) {
			check(t, target, http.StatusNotModified, revalidate(t, check(t, target, http.StatusOK)))
		})
	}
	// Hashed SPA assets carry no validator, so they have no 304 path.
	check(t, "/assets/index-abc123.js", http.StatusOK)
	if cc := b.get(e, "/").Header().Get("Cache-Control"); strings.Contains(cc, "public") {
		t.Errorf("index.html Cache-Control = %q", cc)
	}

	t.Run("precompressed asset", func(t *testing.T) {
		e.srv.static = fstest.MapFS{
			"index.html":              {Data: []byte("<!doctype html><title>SHELF</title>")},
			"assets/app-abc123.js.gz": {Data: gzipBytes(t, precompJS)},
		}
		gz := check(t, precompJSPath, http.StatusOK, acceptEncoding("gzip"))
		if gz.Header().Get("Content-Encoding") != "gzip" {
			t.Fatalf("the gzip client did not get the .gz: %v", gz.Header())
		}
		check(t, precompJSPath, http.StatusOK, acceptEncoding("identity"))
	})
}

// revalidate is the conditional request a cache would send for w.
func revalidate(t *testing.T, w *httptest.ResponseRecorder) func(*http.Request) {
	t.Helper()
	if etag := w.Header().Get("ETag"); etag != "" {
		return func(r *http.Request) { r.Header.Set("If-None-Match", etag) }
	}
	t.Fatalf("the response has no ETag: %v", w.Header())
	return nil
}

func TestOIDC_basePathCookies(t *testing.T) {
	e, f := newOIDCEnv(t, withBasePath("/shelf"))
	b := newBrowser()
	loc := f.start(e, b, "?return=/shelf/series/x")
	if q := f.prov.AuthorizeQuery(t, loc); q.Get("redirect_uri") != oidcOrigin+"/shelf/api/auth/oidc/callback" {
		t.Fatalf("redirect_uri = %q", q.Get("redirect_uri"))
	}
	w := b.get(e, f.prov.Authorize(t, loc, subReader))
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/shelf/series/x" {
		t.Fatalf("callback = %d to %q", w.Code, w.Header().Get("Location"))
	}
	for _, c := range w.Result().Cookies() {
		if strings.HasPrefix(c.Name, "__Host-") {
			t.Errorf("cookie %s under a base path: __Host- needs Path=/", c.Name)
		}
		if c.Name == "shelf_sid" && (c.Path != "/shelf/" || !c.Secure || !c.HttpOnly) {
			t.Errorf("shelf_sid = %+v, want Path=/shelf/ Secure HttpOnly", c)
		}
	}
	if st := decodeBody[AuthStatus](t, b.get(e, "/shelf/api/auth/status"), http.StatusOK); !st.Authenticated {
		t.Fatal("not authenticated under the base path")
	}
	for _, target := range []string{"/shelf/api%2Fhealth", "/shelf/api%2Fauth%2Fstatus"} {
		if w := e.get(target); w.Code != http.StatusUnauthorized {
			t.Errorf("GET %s without a session = %d, want 401", target, w.Code)
		}
	}
}

// Server.Close closes auth.db: a session lookup after it fails rather than
// reading a database the process has let go of.
func TestOIDC_closeReleasesAuthDB(t *testing.T) {
	e, f := newOIDCEnv(t)
	b := newBrowser()
	f.login(e, b, subReader)
	if err := e.srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if w := b.get(e, "/api/series"); w.Code != http.StatusInternalServerError {
		t.Fatalf("a request after Close = %d, want 500 from the closed auth.db", w.Code)
	}
}

// Without user.db's login entry points the server refuses to start in oidc
// mode rather than skip the owner copy (E-74 §2.14).
func TestOIDC_needsTheLoginHook(t *testing.T) {
	e, _ := newOIDCEnv(t)
	bare := struct{ UserData }{e.user} // only the UserData methods
	if _, err := buildOIDC(e.cfg, bare, discard(), time.Now); err == nil {
		t.Fatal("buildOIDC accepted a user database without RecordLogin")
	}
}

func TestOIDC_statusDoesNotHammerADownProvider(t *testing.T) {
	e, f := newOIDCEnv(t)
	f.prov.Update(func(s *oidctest.Settings) { s.Down = true })
	for range 25 {
		if st := decodeBody[AuthStatus](t, e.get("/api/auth/status"), http.StatusOK); st.EndSessionAvailable {
			t.Fatal("end_session_available with uni-auth down")
		}
	}
	if n := len(f.prov.Requests()); n > 1 {
		t.Fatalf("25 anonymous status calls made %d requests to uni-auth, want at most 1", n)
	}
	f.advance(31 * time.Second)
	f.prov.Update(func(s *oidctest.Settings) { s.Down = false })
	if st := decodeBody[AuthStatus](t, e.get("/api/auth/status"), http.StatusOK); !st.EndSessionAvailable {
		t.Fatal("discovery was not retried after the back-off")
	}
}

func TestOIDC_scanErrorIsHiddenFromReaders(t *testing.T) {
	e, f := newOIDCEnv(t)
	e.scan.status = &scanner.ScanStatus{State: scanner.PhaseIdle, LastError: "open /srv/secret/manga: permission denied"}
	reader, admin := newBrowser(), newBrowser()
	f.login(e, reader, subReader)
	f.login(e, admin, subAdmin)
	if st := decodeBody[ScanStatus](t, reader.get(e, "/api/scan/status"), http.StatusOK); st.LastError != nil {
		t.Errorf("a reader sees last_error %q", *st.LastError)
	}
	if st := decodeBody[ScanStatus](t, admin.get(e, "/api/scan/status"), http.StatusOK); st.LastError == nil {
		t.Error("an admin does not see last_error")
	}
}

func TestOIDC_passwordModeEscapedExemptPath(t *testing.T) {
	e := newEnv(t, withPassword("hunter2"), withStatic())
	for _, target := range []string{"/api%2Fhealth", "/api%2Fauth%2Fstatus"} {
		if w := e.get(target); w.Code != http.StatusUnauthorized {
			t.Errorf("GET %s without a session = %d, want 401", target, w.Code)
		}
	}
}

// Ruling E-74 §2.13: two users signed in through the real flow keep their own
// progress. The users table records both logins.
func TestOIDC_twoUsersKeepTheirOwnProgress(t *testing.T) {
	e, f := newOIDCEnv(t)
	reader, admin := newBrowser(), newBrowser()
	f.login(e, reader, subReader)
	f.login(e, admin, subAdmin)
	bid := e.bookZipID
	put := func(b *browser, page string) {
		t.Helper()
		w := e.do(http.MethodPut, "/api/books/"+bid+"/progress", strings.NewReader(`{"page":`+page+`}`), b.send, sameSite,
			func(r *http.Request) { r.Header.Set("Content-Type", "application/json") })
		if w.Code != http.StatusOK {
			t.Fatalf("PUT progress = %d: %s", w.Code, w.Body.String())
		}
	}
	page := func(b *browser) int {
		t.Helper()
		d := decodeBody[BookDetail](t, b.get(e, "/api/books/"+bid), http.StatusOK)
		if d.Progress == nil {
			return 0
		}
		return d.Progress.LastPage
	}
	if page(reader) != 0 || page(admin) != 0 {
		t.Fatalf("a fresh user sees the single-user progress: reader %d admin %d", page(reader), page(admin))
	}
	put(reader, "3")
	if page(reader) != 3 || page(admin) != 0 {
		t.Fatalf("after the reader's write: reader %d admin %d, want 3 and 0", page(reader), page(admin))
	}
	put(admin, "1")
	if page(reader) != 3 || page(admin) != 1 {
		t.Fatalf("after the admin's write: reader %d admin %d, want 3 and 1", page(reader), page(admin))
	}
	subs, err := e.user.UserSubs(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(subs, subReader) || !slices.Contains(subs, subAdmin) {
		t.Fatalf("users = %v, want both logins recorded", subs)
	}
}

// Ruling E-74 §2.14: only legacy_owner_sub receives the single-user data, at
// its login; a failed copy does not refuse the login and runs again next time.
func TestOIDC_legacyOwnerCopy(t *testing.T) {
	f := newOIDCFixture(t)
	f.legacyOwner = subAdmin
	e := newEnv(t, withStatic(), withOIDC(f))
	bid := e.bookZipID
	seeded := func(b *browser) bool {
		d := decodeBody[BookDetail](t, b.get(e, "/api/books/"+bid), http.StatusOK)
		return d.Progress != nil && d.Progress.LastPage == 2
	}

	reader := newBrowser()
	f.login(e, reader, subReader)
	if _, migrated := f.hook.calls(); len(migrated) != 0 || seeded(reader) {
		t.Fatalf("a user other than the legacy owner got the copy (calls %v)", migrated)
	}

	f.hook.mu.Lock()
	f.hook.failMigrate = errors.New("user.db is busy")
	f.hook.mu.Unlock()
	admin := newBrowser()
	if loc := f.login(e, admin, subAdmin); loc != "/" {
		t.Fatalf("login with a failing copy went to %q", loc)
	}
	if st := authStatusOf(t, e, admin); !st.Authenticated || seeded(admin) {
		t.Fatalf("after a failed copy: authenticated %t, seeded %t; want signed in without the data", st.Authenticated, seeded(admin))
	}

	f.hook.mu.Lock()
	f.hook.failMigrate = nil
	f.hook.mu.Unlock()
	again := newBrowser()
	f.login(e, again, subAdmin)
	if !seeded(again) {
		t.Fatal("the copy was not retried at the owner's next login")
	}
	if seeded(reader) {
		t.Fatal("the reader sees the owner's copied progress")
	}
	if _, migrated := f.hook.calls(); !slices.Equal(migrated, []string{subAdmin, subAdmin}) {
		t.Fatalf("copy calls = %v, want two for the owner", migrated)
	}
}

func TestOIDC_needsPerUserData(t *testing.T) {
	e, _ := newOIDCEnv(t)
	opts := Options{Config: e.cfg, Index: e.idx, UserData: hookedUser{UserData: e.user, real: e.user, spy: &loginHookSpy{}}, Logger: discard()}
	if _, err := New(opts); err == nil {
		t.Fatal("httpapi.New accepted oidc mode without per-user data")
	}
}
