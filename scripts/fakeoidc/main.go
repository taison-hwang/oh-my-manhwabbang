// Command fakeoidc is a stand-in uni-auth for the oidc e2e round
// (`make e2e-synthetic-oidc`, ruling E-74 §2.21: the real uni-auth is never
// used by tests). It is a separate main package, so nothing here is linked into
// dist/shelf.
//
// It speaks the provider side of the contract the server checks — discovery,
// an Ed25519 JWKS, authorization code + PKCE S256, an ID token and an access
// token carrying iss, sub, aud, exp, nbf, iat, tid and nonce — and refuses a
// request that leaves the contract instead of being lenient with it. The
// server is run unmodified against it over loopback http (E-74 §2.19's
// loopback exception).
//
//	go run ./scripts/fakeoidc -client-id shelf-e2e -audience http://127.0.0.1:8792/api \
//	    -redirect-uri http://127.0.0.1:8792/api/auth/oidc/callback \
//	    -users alice=sub-a,bob=sub-b -addr-file /tmp/x/fakeoidc.addr
//
// The sign-in page lists the users as buttons; a test picks one by clicking
// it. Like a real provider it keeps its own browser session (a cookie on its
// own origin): an authorization request without `prompt` signs that user in
// again without the page, `prompt=none` answers `login_required` when there is
// no session, and `prompt=login` always shows the page.
//
// GET /_fake/events returns every authorization and end-session request in
// order, so a test can assert which `prompt` the server sent.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	tenant       = "default"
	keyID        = "fakeoidc-1"
	codeLifetime = time.Minute
	tokenTTL     = time.Hour
	sessCookie   = "fakeoidc_session"
)

type user struct{ Label, Sub string }

type grant struct {
	sub, nonce, challenge, redirect string
	expires                         time.Time
}

// Event is one authorization or end-session request, as /_fake/events reports it.
type Event struct {
	Endpoint string `json:"endpoint"`
	Prompt   string `json:"prompt,omitempty"`
	// Outcome is "page", "code", "login_required", "access_denied" or "logged_out".
	Outcome string `json:"outcome"`
	Sub     string `json:"sub,omitempty"`
	Hint    bool   `json:"id_token_hint,omitempty"`
}

type provider struct {
	issuer, clientID, audience, redirectURI, postLogoutURI string
	users                                                  []user
	key                                                    ed25519.PrivateKey

	mu       sync.Mutex
	codes    map[string]grant
	sessions map[string]string // provider session cookie → sub
	events   []Event
}

func main() {
	log.SetFlags(0)
	log.SetPrefix("fakeoidc: ")
	addr := flag.String("addr", "127.0.0.1:0", "listen address (loopback only)")
	addrFile := flag.String("addr-file", "", "write the issuer URL here once listening")
	clientID := flag.String("client-id", "", "the one registered client_id")
	audience := flag.String("audience", "", "the access token audience")
	redirectURI := flag.String("redirect-uri", "", "the one registered redirect_uri")
	postLogout := flag.String("post-logout-redirect-uri", "", "the registered post_logout_redirect_uri, if any")
	users := flag.String("users", "", "label=sub pairs, comma-separated")
	flag.Parse()

	p, err := newProvider(*clientID, *audience, *redirectURI, *postLogout, *users)
	if err != nil {
		log.Fatal(err)
	}
	host, _, err := net.SplitHostPort(*addr)
	if err != nil || (host != "127.0.0.1" && host != "::1" && host != "localhost") {
		log.Fatalf("-addr %q must be a loopback host:port", *addr)
	}
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	p.issuer = "http://" + ln.Addr().String()
	if *addrFile != "" {
		if err := os.WriteFile(*addrFile, []byte(p.issuer+"\n"), 0o600); err != nil {
			log.Fatal(err)
		}
	}
	log.Printf("issuer %s", p.issuer)
	srv := &http.Server{Handler: p.routes(), ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.Serve(ln))
}

func newProvider(clientID, audience, redirectURI, postLogout, users string) (*provider, error) {
	if clientID == "" || audience == "" || redirectURI == "" || users == "" {
		return nil, errors.New("-client-id, -audience, -redirect-uri and -users are required")
	}
	p := &provider{clientID: clientID, audience: audience, redirectURI: redirectURI, postLogoutURI: postLogout,
		codes: map[string]grant{}, sessions: map[string]string{}}
	for _, pair := range strings.Split(users, ",") {
		label, sub, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if !ok || label == "" || sub == "" {
			return nil, fmt.Errorf("-users entry %q is not label=sub", pair)
		}
		p.users = append(p.users, user{Label: label, Sub: sub})
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	p.key = key
	return p, nil
}

func (p *provider) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", p.discovery)
	mux.HandleFunc("GET /jwks", p.jwks)
	mux.HandleFunc("GET /authorize", p.authorize)
	mux.HandleFunc("POST /authorize", p.approve)
	mux.HandleFunc("POST /token", p.token)
	mux.HandleFunc("GET /logout", p.endSession)
	mux.HandleFunc("GET /_fake/events", p.listEvents)
	return mux
}

func (p *provider) record(e Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, e)
}

func (p *provider) discovery(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                p.issuer,
		"authorization_endpoint":                p.issuer + "/authorize",
		"token_endpoint":                        p.issuer + "/token",
		"jwks_uri":                              p.issuer + "/jwks",
		"end_session_endpoint":                  p.issuer + "/logout",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"EdDSA"},
		"code_challenge_methods_supported":      []string{"S256"},
		"scopes_supported":                      []string{"openid", "offline_access"},
	})
}

func (p *provider) jwks(w http.ResponseWriter, _ *http.Request) {
	pub := p.key.Public().(ed25519.PublicKey)
	writeJSON(w, http.StatusOK, map[string]any{"keys": []map[string]string{{
		"kty": "OKP", "crv": "Ed25519", "use": "sig", "alg": "EdDSA", "kid": keyID,
		"x": base64.RawURLEncoding.EncodeToString(pub),
	}}})
}

// checkAuthRequest holds the server to the authorization request of E-74 §2.5.
func (p *provider) checkAuthRequest(q url.Values) error {
	switch {
	case q.Get("response_type") != "code":
		return errors.New("response_type must be code")
	case q.Get("client_id") != p.clientID:
		return fmt.Errorf("unknown client_id %q", q.Get("client_id"))
	case q.Get("redirect_uri") != p.redirectURI:
		return fmt.Errorf("redirect_uri %q is not the registered one", q.Get("redirect_uri"))
	case q.Get("scope") != "openid":
		return fmt.Errorf("scope %q: the client requests openid only", q.Get("scope"))
	case q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "":
		return errors.New("PKCE S256 is required")
	case q.Get("state") == "" || q.Get("nonce") == "":
		return errors.New("state and nonce are required")
	}
	switch q.Get("prompt") {
	case "", "none", "login":
	default:
		return fmt.Errorf("prompt %q is not one the client sends", q.Get("prompt"))
	}
	return nil
}

func (p *provider) sessionSub(r *http.Request) string {
	c, err := r.Cookie(sessCookie)
	if err != nil {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sessions[c.Value]
}

func (p *provider) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if err := p.checkAuthRequest(q); err != nil {
		http.Error(w, "invalid_request: "+err.Error(), http.StatusBadRequest)
		return
	}
	prompt := q.Get("prompt")
	sub := p.sessionSub(r)
	switch {
	case prompt == "login" || (prompt == "" && sub == ""):
		p.record(Event{Endpoint: "authorize", Prompt: prompt, Outcome: "page"})
		p.signInPage(w, q)
	case sub == "":
		p.record(Event{Endpoint: "authorize", Prompt: prompt, Outcome: "login_required"})
		p.redirectBack(w, r, q, url.Values{"error": {"login_required"}})
	default:
		p.record(Event{Endpoint: "authorize", Prompt: prompt, Outcome: "code", Sub: sub})
		p.redirectBack(w, r, q, url.Values{"code": {p.issueCode(sub, q)}})
	}
}

// approve is the sign-in page's form: the button pressed names the user.
func (p *provider) approve(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid_request", http.StatusBadRequest)
		return
	}
	q := r.PostForm
	if err := p.checkAuthRequest(q); err != nil {
		http.Error(w, "invalid_request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if q.Get("deny") != "" {
		p.record(Event{Endpoint: "authorize", Prompt: q.Get("prompt"), Outcome: "access_denied"})
		p.redirectBack(w, r, q, url.Values{"error": {"access_denied"}})
		return
	}
	sub := q.Get("sub")
	if !slices.ContainsFunc(p.users, func(u user) bool { return u.Sub == sub }) {
		http.Error(w, "unknown user", http.StatusBadRequest)
		return
	}
	sid := randomToken()
	p.mu.Lock()
	p.sessions[sid] = sub
	p.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: sessCookie, Value: sid, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode})
	p.record(Event{Endpoint: "authorize", Prompt: q.Get("prompt"), Outcome: "code", Sub: sub})
	p.redirectBack(w, r, q, url.Values{"code": {p.issueCode(sub, q)}})
}

func (p *provider) issueCode(sub string, q url.Values) string {
	code := randomToken()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.codes[code] = grant{sub: sub, nonce: q.Get("nonce"), challenge: q.Get("code_challenge"),
		redirect: q.Get("redirect_uri"), expires: time.Now().Add(codeLifetime)}
	return code
}

func (p *provider) redirectBack(w http.ResponseWriter, r *http.Request, q, add url.Values) {
	add.Set("state", q.Get("state"))
	http.Redirect(w, r, q.Get("redirect_uri")+"?"+add.Encode(), http.StatusFound)
}

var signInTemplate = template.Must(template.New("signin").Parse(`<!doctype html>
<html lang="ko"><head><meta charset="utf-8"><title>fake uni-auth</title></head>
<body><h1>fake uni-auth</h1>
<form method="post" action="/authorize">
{{range $k, $v := .Hidden}}<input type="hidden" name="{{$k}}" value="{{$v}}">
{{end}}{{range .Users}}<button type="submit" name="sub" value="{{.Sub}}">{{.Label}}로 로그인</button>
{{end}}<button type="submit" name="deny" value="1">거부</button>
</form></body></html>
`))

func (p *provider) signInPage(w http.ResponseWriter, q url.Values) {
	hidden := map[string]string{}
	for k := range q {
		hidden[k] = q.Get(k)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := signInTemplate.Execute(w, map[string]any{"Hidden": hidden, "Users": p.users}); err != nil {
		log.Printf("sign-in page: %v", err)
	}
}

func (p *provider) token(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") || r.ParseForm() != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	f := r.PostForm
	code := f.Get("code")
	p.mu.Lock()
	g, ok := p.codes[code]
	delete(p.codes, code)
	p.mu.Unlock()
	sum := sha256.Sum256([]byte(f.Get("code_verifier")))
	switch {
	case !ok, time.Now().After(g.expires), f.Get("grant_type") != "authorization_code",
		f.Get("client_id") != p.clientID, f.Get("redirect_uri") != g.redirect,
		base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
		return
	}
	now := time.Now().Unix()
	exp := now + int64(tokenTTL/time.Second)
	id := map[string]any{"iss": p.issuer, "sub": g.sub, "aud": []string{p.clientID}, "exp": exp, "nbf": now,
		"iat": now, "nonce": g.nonce, "tid": tenant, "sid": "fake-sid-" + randomToken()}
	at := map[string]any{"iss": p.issuer, "sub": g.sub, "aud": []string{p.audience}, "exp": exp, "nbf": now,
		"iat": now, "tid": tenant, "client_id": p.clientID, "scp": []string{"openid"}}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"id_token": p.sign(id), "access_token": p.sign(at), "token_type": "bearer",
		"expires_in": int64(tokenTTL / time.Second), "scope": "openid",
	})
}

func (p *provider) sign(claims map[string]any) string {
	h, _ := json.Marshal(map[string]any{"alg": "EdDSA", "kid": keyID, "typ": "JWT"})
	c, _ := json.Marshal(claims)
	input := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(c)
	return input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(p.key, []byte(input)))
}

// hintSub returns the sub of an ID token this provider issued to this client,
// expired or not (OIDC RP-Initiated Logout accepts an expired hint).
func (p *provider) hintSub(tok string) (string, error) {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return "", errors.New("not a JWT")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !ed25519.Verify(p.key.Public().(ed25519.PublicKey), []byte(parts[0]+"."+parts[1]), sig) {
		return "", errors.New("signature does not verify")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", err
	}
	var c struct {
		Iss string   `json:"iss"`
		Sub string   `json:"sub"`
		Aud []string `json:"aud"`
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return "", err
	}
	if c.Iss != p.issuer || !slices.Contains(c.Aud, p.clientID) || c.Sub == "" {
		return "", errors.New("not an ID token for this client")
	}
	return c.Sub, nil
}

func (p *provider) endSession(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	hint := q.Get("id_token_hint")
	sub := ""
	if hint != "" {
		var err error
		if sub, err = p.hintSub(hint); err != nil {
			http.Error(w, "invalid id_token_hint: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	back := q.Get("post_logout_redirect_uri")
	if back != "" && back != p.postLogoutURI {
		http.Error(w, "post_logout_redirect_uri is not registered", http.StatusBadRequest)
		return
	}
	if c, err := r.Cookie(sessCookie); err == nil {
		p.mu.Lock()
		delete(p.sessions, c.Value)
		p.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: sessCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	p.record(Event{Endpoint: "end_session", Outcome: "logged_out", Sub: sub, Hint: hint != ""})
	if back != "" {
		http.Redirect(w, r, back, http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprint(w, `<!doctype html><html lang="ko"><head><meta charset="utf-8"><title>fake uni-auth</title></head>`+
		`<body><h1>fake uni-auth</h1><p>uni-auth에서 로그아웃했습니다.</p></body></html>`)
}

func (p *provider) listEvents(w http.ResponseWriter, _ *http.Request) {
	p.mu.Lock()
	events := append([]Event{}, p.events...)
	p.mu.Unlock()
	writeJSON(w, http.StatusOK, events)
}

func randomToken() string {
	var b [24]byte
	_, _ = rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
