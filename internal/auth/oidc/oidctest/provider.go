// Package oidctest is a fake uni-auth for tests: discovery, JWKS, a token
// endpoint that enforces PKCE S256, and an authorization step the test drives
// by hand. It signs with a throwaway Ed25519 key. No production code imports it.
package oidctest

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// Settings is what a test may change between requests.
type Settings struct {
	// DiscoveryIssuer is the issuer the discovery document names; empty is the
	// real one.
	DiscoveryIssuer string
	EndSession      bool
	// Down answers every request 503.
	Down bool
	// Tamper edits a token's header and claims before signing; kind is "id"
	// or "access".
	Tamper func(kind string, header, claims map[string]any)
	// SignWith signs tokens with another key while the JWKS still publishes
	// the provider's own.
	SignWith ed25519.PrivateKey
}

type grant struct {
	sub, nonce, challenge, redirect string
}

// Provider is the fake. URL is its issuer.
type Provider struct {
	URL      string
	ClientID string
	Audience string
	KID      string

	now func() time.Time
	key ed25519.PrivateKey
	srv *httptest.Server

	mu       sync.Mutex
	set      Settings
	codes    map[string]grant
	requests []string
}

// New starts the fake on a loopback httptest server.
func New(t testing.TB, clientID, audience string, now func() time.Time) *Provider {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	p := &Provider{ClientID: clientID, Audience: audience, KID: "test-key-1", now: now, key: key,
		codes: map[string]grant{}, set: Settings{EndSession: true}}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", p.discovery)
	mux.HandleFunc("/jwks", p.jwks)
	mux.HandleFunc("/token", p.token)
	p.srv = httptest.NewServer(p.record(mux))
	p.URL = p.srv.URL
	t.Cleanup(p.srv.Close)
	return p
}

// Update changes the settings under the provider's lock.
func (p *Provider) Update(fn func(*Settings)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	fn(&p.set)
}

// Requests is every path the fake has served, in order.
func (p *Provider) Requests() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.requests...)
}

func (p *Provider) settings() Settings {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.set
}

func (p *Provider) record(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.requests = append(p.requests, r.URL.Path)
		down := p.set.Down
		p.mu.Unlock()
		if down {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (p *Provider) discovery(w http.ResponseWriter, _ *http.Request) {
	s := p.settings()
	iss := p.URL
	if s.DiscoveryIssuer != "" {
		iss = s.DiscoveryIssuer
	}
	doc := map[string]string{
		"issuer":                 iss,
		"authorization_endpoint": p.URL + "/authorize",
		"token_endpoint":         p.URL + "/token",
		"jwks_uri":               p.URL + "/jwks",
	}
	if s.EndSession {
		doc["end_session_endpoint"] = p.URL + "/logout"
	}
	writeJSON(w, http.StatusOK, doc)
}

func (p *Provider) jwks(w http.ResponseWriter, _ *http.Request) {
	pub := p.key.Public().(ed25519.PublicKey)
	writeJSON(w, http.StatusOK, map[string]any{"keys": []map[string]string{{
		"kty": "OKP", "crv": "Ed25519", "use": "sig", "kid": p.KID,
		"x": base64.RawURLEncoding.EncodeToString(pub),
	}}})
}

// Authorize plays the user signing in as sub at location (the 302 the server
// sent) and returns the callback path with code and state.
func (p *Provider) Authorize(t testing.TB, location, sub string) string {
	t.Helper()
	q := p.AuthorizeQuery(t, location)
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		t.Fatalf("authorize request without PKCE S256: %s", location)
	}
	if q.Get("scope") != "openid" || q.Get("response_type") != "code" || q.Get("client_id") != p.ClientID {
		t.Fatalf("authorize request has the wrong scope, response_type or client_id: %s", location)
	}
	code := token()
	p.mu.Lock()
	p.codes[code] = grant{sub: sub, nonce: q.Get("nonce"), challenge: q.Get("code_challenge"), redirect: q.Get("redirect_uri")}
	p.mu.Unlock()
	return callbackPath(t, q.Get("redirect_uri"), url.Values{"code": {code}, "state": {q.Get("state")}})
}

// Deny answers the authorization request with an OAuth error.
func (p *Provider) Deny(t testing.TB, location, oauthErr string) string {
	t.Helper()
	q := p.AuthorizeQuery(t, location)
	return callbackPath(t, q.Get("redirect_uri"), url.Values{"error": {oauthErr}, "state": {q.Get("state")}})
}

// AuthorizeQuery returns the query of a 302 to the authorization endpoint.
func (p *Provider) AuthorizeQuery(t testing.TB, location string) url.Values {
	t.Helper()
	u, err := url.Parse(location)
	if err != nil || u.Scheme+"://"+u.Host+u.Path != p.URL+"/authorize" {
		t.Fatalf("not a redirect to the fake's authorization endpoint: %q", location)
	}
	return u.Query()
}

func callbackPath(t testing.TB, redirect string, q url.Values) string {
	t.Helper()
	u, err := url.Parse(redirect)
	if err != nil {
		t.Fatalf("redirect_uri %q: %v", redirect, err)
	}
	return u.Path + "?" + q.Encode()
}

func (p *Provider) token(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.ParseForm() != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	code := r.PostForm.Get("code")
	p.mu.Lock()
	g, ok := p.codes[code]
	delete(p.codes, code)
	p.mu.Unlock()
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	switch {
	case !ok, r.PostForm.Get("grant_type") != "authorization_code",
		r.PostForm.Get("client_id") != p.ClientID, r.PostForm.Get("redirect_uri") != g.redirect,
		base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
		return
	}
	now := p.now().Unix()
	id := map[string]any{"iss": p.URL, "sub": g.sub, "aud": p.ClientID, "exp": now + 300, "iat": now,
		"nonce": g.nonce, "tid": "default", "sid": "uni-sid-" + g.sub}
	at := map[string]any{"iss": p.URL, "sub": g.sub, "aud": []string{p.Audience}, "exp": now + 300, "iat": now,
		"tid": "default", "client_id": p.ClientID}
	writeJSON(w, http.StatusOK, map[string]any{
		"id_token": p.sign("id", id), "access_token": p.sign("access", at), "token_type": "bearer",
	})
}

func (p *Provider) sign(kind string, claims map[string]any) string {
	s := p.settings()
	header := map[string]any{"alg": "EdDSA", "kid": p.KID, "typ": "JWT"}
	if s.Tamper != nil {
		s.Tamper(kind, header, claims)
	}
	key := p.key
	if s.SignWith != nil {
		key = s.SignWith
	}
	h, _ := json.Marshal(header)
	c, _ := json.Marshal(claims)
	input := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(c)
	return input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, []byte(input)))
}

// IDTokenHint is the id_token_hint of an end-session URL.
func IDTokenHint(location string) (string, bool) {
	u, err := url.Parse(location)
	if err != nil {
		return "", false
	}
	v := u.Query().Get("id_token_hint")
	return v, v != "" && strings.Count(v, ".") == 2
}

func token() string {
	var b [24]byte
	_, _ = rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
