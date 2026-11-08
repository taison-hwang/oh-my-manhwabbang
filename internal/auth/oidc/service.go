// Package oidc is the server side of `auth.mode: oidc`: login through uni-auth
// with the authorization code flow and PKCE, the server acting as the BFF and
// holding sessions in <data_dir>/auth.db (ruling E-74, amendment A-20).
package oidc

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"sync"
	"time"
)

// Defaults of ruling E-74 §2.4, §2.7 and §2.9.
const (
	CookieMaxAge  = 30 * 24 * 60 * 60
	ReauthWindow  = 15 * time.Minute
	touchInterval = time.Minute
	sweepInterval = 24 * time.Hour
	// graceSweep keeps a logged-out row's id_token from outliving its ten
	// minutes by more than this (E-74 §2.12), and clears expired pre-auth
	// entries (§2.5).
	graceSweep  = time.Minute
	httpTimeout = 10 * time.Second
)

// LoginHook is told about every successful login. userdata implements it
// (ruling E-74 §2.13, §2.14).
type LoginHook interface {
	RecordLogin(ctx context.Context, sub string) error
	MigrateLegacyOwner(ctx context.Context, sub string) (bool, error)
}

// Config is what the service needs from `auth:` and `server:`.
type Config struct {
	Issuer                string
	ClientID              string
	Audience              string
	RedirectURL           string
	PostLogoutRedirectURL string
	AllowedSubs           []string
	AdminSubs             []string
	LegacyOwnerSub        string
	SessionTTL            time.Duration
	BasePath              string
	DBPath                string

	Hook       LoginHook
	HTTPClient Doer
	Now        func() time.Time
	Logger     *slog.Logger
}

// Service is safe for concurrent use.
type Service struct {
	cfg     Config
	prov    *provider
	store   *store
	pending *pendingStore
	client  Doer
	now     func() time.Time
	log     *slog.Logger
	origin  string

	sidName, loginName string
	sidPath, loginPath string

	stop     chan struct{}
	stopOnce sync.Once
	done     chan struct{}
}

// New opens auth.db and starts the daily sweep. It does not contact the
// provider: a server must start while uni-auth is down (AC-007, E-74 §2.16).
func New(ctx context.Context, cfg Config) (*Service, error) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = directClient{timeout: httpTimeout}
	}
	u, err := url.Parse(cfg.RedirectURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("oidc: redirect_url %q is not an absolute URL", cfg.RedirectURL)
	}
	st, err := openStore(ctx, cfg.DBPath)
	if err != nil {
		return nil, err
	}
	s := &Service{
		cfg:     cfg,
		prov:    newProvider(cfg.Issuer, cfg.HTTPClient, cfg.Now),
		store:   st,
		pending: newPendingStore(cfg.Now),
		client:  cfg.HTTPClient,
		now:     cfg.Now,
		log:     cfg.Logger,
		origin:  origin(u),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	// __Host- needs Path=/ and no Domain, which only an empty base path can
	// give; it stops a sibling subdomain from planting these cookies (E-74 §2.4).
	if cfg.BasePath == "" {
		s.sidName, s.loginName = "__Host-shelf_sid", "__Host-shelf_login"
		s.sidPath, s.loginPath = "/", "/"
	} else {
		s.sidName, s.loginName = "shelf_sid", "shelf_login"
		s.sidPath, s.loginPath = cfg.BasePath+"/", cfg.BasePath+"/api/auth/oidc/"
	}
	if err := st.sweep(ctx, s.now()); err != nil {
		s.log.Warn("sweeping auth.db", "err", err)
	}
	go s.sweepLoop()
	return s, nil
}

// Close stops the sweep and closes auth.db.
func (s *Service) Close() error {
	s.stopOnce.Do(func() { close(s.stop) })
	<-s.done
	return s.store.close()
}

func (s *Service) sweepLoop() {
	defer close(s.done)
	day, minute := time.NewTicker(sweepInterval), time.NewTicker(graceSweep)
	defer day.Stop()
	defer minute.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-day.C:
			if err := s.store.sweep(context.Background(), s.now()); err != nil {
				s.log.Warn("sweeping auth.db", "err", err)
			}
		case <-minute.C:
			s.sweepGrace()
		}
	}
}

func (s *Service) sweepGrace() {
	s.pending.sweep()
	if err := s.store.dropLoggedOut(context.Background(), s.now()); err != nil {
		s.log.Warn("sweeping logged-out sessions", "err", err)
	}
}

// Origin is the scheme and host of redirect_url: the only origin a
// state-changing request may come from (ruling E-74 §2.11).
func (s *Service) Origin() string { return s.origin }

// Allowed and Admin read the configuration on every call (ruling E-74 §2.8).
func (s *Service) Allowed(sub string) bool {
	return sub != "" && slices.Contains(s.cfg.AllowedSubs, sub)
}
func (s *Service) Admin(sub string) bool {
	return s.Allowed(sub) && slices.Contains(s.cfg.AdminSubs, sub)
}

// Status is the outcome of looking up a request's session.
type Status int

const (
	StatusNone       Status = iota // no cookie, no row, or a logged-out row
	StatusOK                       // a live session for an allowed sub
	StatusExpired                  // the row exists and has expired
	StatusNotAllowed               // the sub has left allowed_subs; the row is gone
)

// Session is an authenticated request's session.
type Session struct {
	Sub      string
	IDHash   string
	ReauthAt time.Time
}

// ReauthFresh reports whether an admin write may proceed (ruling E-74 §2.9).
func (s *Service) ReauthFresh(sess Session) bool {
	return !sess.ReauthAt.IsZero() && s.now().Sub(sess.ReauthAt) < ReauthWindow
}

func hashID(id string) string {
	h := sha256.Sum256([]byte(id))
	return hex.EncodeToString(h[:])
}

func (s *Service) cookieValue(r *http.Request, name string) string {
	c, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return c.Value
}

// lookup returns the row behind the request's session cookie, whatever its
// state.
func (s *Service) lookup(ctx context.Context, r *http.Request) (sessionRow, bool, error) {
	id := s.cookieValue(r, s.sidName)
	if id == "" {
		return sessionRow{}, false, nil
	}
	return s.store.get(ctx, hashID(id))
}

// Authenticate checks the request's session on every request (ruling E-74 §2.7).
func (s *Service) Authenticate(r *http.Request) (Session, Status, error) {
	ctx := r.Context()
	row, ok, err := s.lookup(ctx, r)
	if err != nil || !ok {
		return Session{}, StatusNone, err
	}
	now := s.now()
	if !row.loggedOutAt.IsZero() {
		if now.Sub(row.loggedOutAt) >= loggedOutGrace {
			err = s.store.delete(ctx, row.idHash)
		}
		return Session{}, StatusNone, err
	}
	if !now.Before(row.expiresAt) {
		return Session{}, StatusExpired, nil
	}
	if !s.Allowed(row.sub) {
		if err := s.store.delete(ctx, row.idHash); err != nil {
			return Session{}, StatusNone, err
		}
		return Session{}, StatusNotAllowed, nil
	}
	if now.Sub(row.lastSeenAt) >= touchInterval {
		if err := s.store.touch(ctx, row.idHash, now); err != nil {
			s.log.Warn("recording session activity", "err", err)
		}
	}
	return Session{Sub: row.sub, IDHash: row.idHash, ReauthAt: row.reauthAt}, StatusOK, nil
}

// ErrNoSession refuses a reauth start without a live session (E-74 §2.5).
var ErrNoSession = errors.New("oidc: re-authentication needs a live session")

// RateLimitError is a start refused by the pre-auth limits (E-74 §2.5).
type RateLimitError struct{ RetryAfter time.Duration }

func (e *RateLimitError) Error() string { return "oidc: too many logins in flight" }

// IsProviderUnavailable reports whether err means uni-auth could not be reached.
func IsProviderUnavailable(err error) bool { return errors.Is(err, errProviderUnavailable) }

// Redirect is a 302 to send, with the cookies to set first.
type Redirect struct {
	Location string
	Cookies  []*http.Cookie
}

// Start begins a login (ruling E-74 §2.5). returnPath is sanitized here.
func (s *Service) Start(r *http.Request, clientIP, returnPath string, reauth bool) (Redirect, error) {
	ctx := r.Context()
	e := &pending{returnTo: s.SafeReturn(returnPath), reauth: reauth}
	row, hasRow, err := s.lookup(ctx, r)
	if err != nil {
		return Redirect{}, err
	}
	if reauth {
		sess, st, err := s.Authenticate(r)
		if err != nil {
			return Redirect{}, err
		}
		if st != StatusOK {
			return Redirect{}, ErrNoSession
		}
		e.sessionHash = sess.IDHash
	} else if hasRow && row.loggedOutAt.IsZero() && !s.now().Before(row.expiresAt) {
		e.promptNone = true
	}
	if wait, ok := s.pending.admit(clientIP); !ok {
		return Redirect{}, &RateLimitError{RetryAfter: wait}
	}
	m, err := s.prov.metadata(ctx)
	if err != nil {
		return Redirect{}, err
	}
	return s.authorize(m, e)
}

func (s *Service) authorize(m *metadata, e *pending) (Redirect, error) {
	state, loginID, nonce, verifier := randomToken(), randomToken(), randomToken(), randomToken()
	e.loginHash = sha256.Sum256([]byte(loginID))
	e.nonce, e.verifier = nonce, verifier
	challenge := sha256.Sum256([]byte(verifier))
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", s.cfg.ClientID)
	q.Set("redirect_uri", s.cfg.RedirectURL)
	q.Set("scope", "openid")
	q.Set("state", state)
	q.Set("nonce", nonce)
	q.Set("code_challenge", base64.RawURLEncoding.EncodeToString(challenge[:]))
	q.Set("code_challenge_method", "S256")
	switch {
	case e.reauth:
		q.Set("prompt", "login")
	case e.promptNone:
		q.Set("prompt", "none")
	}
	if !s.pending.put(state, e) {
		return Redirect{}, &RateLimitError{RetryAfter: pendingTTL / 10}
	}
	loc := m.AuthorizationEndpoint
	if strings.Contains(loc, "?") {
		loc += "&" + q.Encode()
	} else {
		loc += "?" + q.Encode()
	}
	return Redirect{Location: loc, Cookies: []*http.Cookie{s.loginCookie(loginID, 600)}}, nil
}

// SafeReturn keeps only a same-site path under the base path, and never an
// API path (ruling E-74 §2.5). The checks run on the decoded, cleaned path
// the router will see; a path that cleaning changes is refused rather than
// rewritten, since browsers and http.Redirect resolve dot segments too.
func (s *Service) SafeReturn(p string) string {
	home := s.cfg.BasePath + "/"
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.Contains(p, `\`) {
		return home
	}
	u, err := url.Parse(p)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || u.Opaque != "" {
		return home
	}
	d := u.Path
	for _, c := range d {
		if c < 0x20 || c == 0x7f || c == '\\' {
			return home
		}
	}
	clean := path.Clean(d)
	if strings.HasSuffix(d, "/") && clean != "/" {
		clean += "/"
	}
	if clean != d || strings.HasPrefix(d, "//") || !strings.HasPrefix(d, home) ||
		strings.HasPrefix(d, s.cfg.BasePath+"/api/") || d == s.cfg.BasePath+"/api" {
		return home
	}
	for _, c := range p {
		if c < 0x20 || c == 0x7f {
			return home
		}
	}
	return p
}

// Outcome is what the callback asks the HTTP layer to do.
type Outcome int

const (
	OutcomeRedirect     Outcome = iota // Result.Redirect
	OutcomeRestart                     // show "start again"; nothing was changed
	OutcomeNotAllowed                  // the sub is not in allowed_subs; Result.Sub
	OutcomeReauthFailed                // re-authentication did not complete for this session
	OutcomeLoginFailed                 // the provider refused or answered nonsense
	OutcomeUnavailable                 // the provider could not be reached
	OutcomeRetryLater                  // the legacy owner's data copy failed; no session was made
)

// Result is the callback's outcome.
type Result struct {
	Outcome  Outcome
	Redirect Redirect
	Sub      string
	Err      error
}

var fallbackErrors = []string{"login_required", "consent_required", "interaction_required"}

// oauthErrors are the error codes of RFC 6749 §4.1.2.1, §5.2 and OIDC Core
// §3.1.2.6. Anything else the provider (or a forged callback) sends is logged
// as "unrecognized", so a request cannot write its own text into the log.
var oauthErrors = append([]string{"access_denied", "invalid_request", "unauthorized_client", "unsupported_response_type",
	"invalid_scope", "server_error", "temporarily_unavailable", "invalid_client", "invalid_grant",
	"unsupported_grant_type", "account_selection_required", "invalid_request_uri", "invalid_request_object",
	"request_not_supported", "request_uri_not_supported", "registration_not_supported"}, fallbackErrors...)

func errorCode(s string) string {
	if slices.Contains(oauthErrors, s) {
		return s
	}
	return "unrecognized"
}

// Callback finishes a login (ruling E-74 §2.6). Nothing changes unless state
// and the pre-auth cookie name the same entry.
func (s *Service) Callback(r *http.Request) Result {
	ctx := r.Context()
	q := r.URL.Query()
	e, ok := s.pending.take(q.Get("state"), s.cookieValue(r, s.loginName))
	if !ok {
		if _, st, err := s.Authenticate(r); err == nil && st == StatusOK {
			return Result{Outcome: OutcomeRedirect, Redirect: Redirect{Location: s.cfg.BasePath + "/"}}
		}
		return Result{Outcome: OutcomeRestart}
	}
	clear := s.loginCookie("", -1)
	failed := Redirect{Cookies: []*http.Cookie{clear}}
	fail := func(o Outcome, sub string, err error) Result {
		return Result{Outcome: o, Sub: sub, Err: err, Redirect: failed}
	}

	if perr := q.Get("error"); perr != "" {
		if e.promptNone && !e.fellBack && slices.Contains(fallbackErrors, perr) {
			m, err := s.prov.metadata(ctx)
			if err != nil {
				return fail(OutcomeUnavailable, "", err)
			}
			next := &pending{returnTo: e.returnTo, fellBack: true}
			red, err := s.authorize(m, next)
			if err != nil {
				return fail(OutcomeLoginFailed, "", err)
			}
			return Result{Outcome: OutcomeRedirect, Redirect: red}
		}
		if e.reauth {
			return fail(OutcomeReauthFailed, "", errors.New("oidc: provider answered "+errorCode(perr)))
		}
		return fail(OutcomeLoginFailed, "", errors.New("oidc: provider answered "+errorCode(perr)))
	}
	code := q.Get("code")
	if code == "" {
		return fail(OutcomeLoginFailed, "", errors.New("oidc: callback without code"))
	}

	m, err := s.prov.metadata(ctx)
	if err != nil {
		return fail(outcomeFor(err), "", err)
	}
	idToken, accessToken, err := s.exchange(ctx, m, code, e.verifier)
	if err != nil {
		return fail(outcomeFor(err), "", err)
	}
	idc, err := s.verify(ctx, idToken, tokenCheck{audience: s.cfg.ClientID, nonce: &e.nonce})
	if err != nil {
		return fail(outcomeFor(err), "", err)
	}
	atc, err := s.verify(ctx, accessToken, tokenCheck{audience: s.cfg.Audience})
	if err != nil {
		return fail(outcomeFor(err), "", err)
	}
	if atc.Sub != idc.Sub {
		return fail(OutcomeLoginFailed, "", errors.New("oidc: the ID token and the access token name different users"))
	}
	sub := idc.Sub
	if !s.Allowed(sub) {
		s.log.Warn("login refused: not in auth.oidc.allowed_subs", "sub", sub)
		return fail(OutcomeNotAllowed, sub, nil)
	}
	now := s.now()

	if e.reauth {
		row, ok, err := s.lookup(ctx, r)
		if err != nil {
			return fail(OutcomeLoginFailed, "", err)
		}
		if !ok || row.idHash != e.sessionHash || row.sub != sub || !row.loggedOutAt.IsZero() || !now.Before(row.expiresAt) {
			return fail(OutcomeReauthFailed, sub, nil)
		}
		// The session id is replaced as privilege rises, so an id taken
		// before the re-authentication does not carry the admin window.
		id := randomToken()
		if err := s.store.rotate(ctx, row.idHash, sessionRow{
			idHash: hashID(id), sub: row.sub, uniSID: row.uniSID, idToken: row.idToken,
			createdAt: row.createdAt, lastSeenAt: now, reauthAt: now, expiresAt: now.Add(s.cfg.SessionTTL),
		}); err != nil {
			return fail(OutcomeLoginFailed, "", err)
		}
		return Result{Outcome: OutcomeRedirect, Redirect: Redirect{
			Location: e.returnTo,
			Cookies:  []*http.Cookie{s.sidCookie(id, CookieMaxAge), clear},
		}}
	}

	if s.cfg.Hook != nil {
		if err := s.cfg.Hook.RecordLogin(ctx, sub); err != nil {
			s.log.Error("recording login", "err", err)
		}
		// E-74 §2.14: a failed copy refuses the owner's login. A session would
		// let the owner change progress that the retried copy then brings back.
		if s.cfg.LegacyOwnerSub != "" && sub == s.cfg.LegacyOwnerSub {
			migrated, err := s.cfg.Hook.MigrateLegacyOwner(ctx, sub)
			if err != nil {
				return fail(OutcomeRetryLater, "", fmt.Errorf("copying the single-user data to the legacy owner: %w", err))
			}
			if migrated {
				s.log.Info("copied the single-user data to the legacy owner", "sub", sub)
			}
		}
	}
	if old, ok, err := s.lookup(ctx, r); err == nil && ok {
		if err := s.store.delete(ctx, old.idHash); err != nil {
			return fail(OutcomeLoginFailed, "", err)
		}
	}
	id := randomToken()
	if err := s.store.insert(ctx, sessionRow{
		idHash: hashID(id), sub: sub, uniSID: idc.Sid, idToken: idToken,
		createdAt: now, lastSeenAt: now, expiresAt: now.Add(s.cfg.SessionTTL),
	}); err != nil {
		return fail(OutcomeLoginFailed, "", err)
	}
	s.log.Info("login succeeded", "sub", sub)
	return Result{Outcome: OutcomeRedirect, Redirect: Redirect{
		Location: e.returnTo,
		Cookies:  []*http.Cookie{s.sidCookie(id, CookieMaxAge), clear},
	}}
}

func outcomeFor(err error) Outcome {
	if IsProviderUnavailable(err) {
		return OutcomeUnavailable
	}
	return OutcomeLoginFailed
}

func (s *Service) exchange(ctx context.Context, m *metadata, code, verifier string) (idToken, accessToken string, err error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", s.cfg.RedirectURL)
	form.Set("client_id", s.cfg.ClientID)
	form.Set("code_verifier", verifier)
	var tok struct {
		IDToken     string `json:"id_token"`
		AccessToken string `json:"access_token"`
	}
	if err := call(ctx, s.client, http.MethodPost, m.TokenEndpoint, form.Encode(), &tok); err != nil {
		return "", "", err
	}
	if tok.IDToken == "" || tok.AccessToken == "" {
		return "", "", errors.New("oidc: the token response lacks an id_token or access_token")
	}
	return tok.IDToken, tok.AccessToken, nil
}

// Logout is the local logout (ruling E-74 §2.12): the row stops
// authenticating at once and is kept ten minutes for end-session.
func (s *Service) Logout(r *http.Request) ([]*http.Cookie, error) {
	ctx := r.Context()
	row, ok, err := s.lookup(ctx, r)
	if err != nil {
		return nil, err
	}
	if !ok {
		return []*http.Cookie{s.sidCookie("", -1)}, nil
	}
	if err := s.store.markLoggedOut(ctx, row.idHash, s.now()); err != nil {
		return nil, err
	}
	return []*http.Cookie{s.sidCookie(s.cookieValue(r, s.sidName), int(loggedOutGrace.Seconds()))}, nil
}

// EndSession is the logout from every service (ruling E-74 §2.12). Location
// is empty when there is no row to take an id_token_hint from, or the
// provider has no end_session_endpoint.
func (s *Service) EndSession(r *http.Request) (Redirect, error) {
	ctx := r.Context()
	out := Redirect{Cookies: []*http.Cookie{s.sidCookie("", -1)}}
	row, ok, err := s.lookup(ctx, r)
	if err != nil {
		return out, err
	}
	if !ok {
		return out, nil
	}
	if err := s.store.delete(ctx, row.idHash); err != nil {
		return out, err
	}
	if !row.loggedOutAt.IsZero() && s.now().Sub(row.loggedOutAt) >= loggedOutGrace {
		return out, nil
	}
	m, err := s.prov.metadata(ctx)
	if err != nil || m.EndSessionEndpoint == "" {
		return out, err
	}
	q := url.Values{}
	q.Set("id_token_hint", row.idToken)
	if s.cfg.PostLogoutRedirectURL != "" {
		q.Set("post_logout_redirect_uri", s.cfg.PostLogoutRedirectURL)
	}
	sep := "?"
	if strings.Contains(m.EndSessionEndpoint, "?") {
		sep = "&"
	}
	out.Location = m.EndSessionEndpoint + sep + q.Encode()
	return out, nil
}

// EndSessionAvailable reads only cached discovery, fetching it once with a
// short deadline when nothing is cached, so /api/auth/status never waits on
// a slow provider for long.
func (s *Service) EndSessionAvailable(ctx context.Context) bool {
	m := s.prov.cachedMetadata()
	if m == nil {
		cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		var err error
		if m, err = s.prov.metadata(cctx); err != nil {
			return false
		}
	}
	return m.EndSessionEndpoint != ""
}

func (s *Service) sidCookie(value string, maxAge int) *http.Cookie {
	return &http.Cookie{Name: s.sidName, Value: value, Path: s.sidPath, MaxAge: maxAge,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode}
}

func (s *Service) loginCookie(value string, maxAge int) *http.Cookie {
	return &http.Cookie{Name: s.loginName, Value: value, Path: s.loginPath, MaxAge: maxAge,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode}
}

// IsSessionCookie reports whether name is a cookie of this mode, so the HTTP
// layer can tell it apart from password mode's shelf_session.
func (s *Service) IsSessionCookie(name string) bool { return name == s.sidName }

// origin is the serialization a browser sends in Origin: lower-case host and
// no default port.
func origin(u *url.URL) string {
	host := strings.ToLower(u.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if p := u.Port(); p != "" && !(u.Scheme == "https" && p == "443") && !(u.Scheme == "http" && p == "80") {
		host += ":" + p
	}
	return strings.ToLower(u.Scheme) + "://" + host
}

func randomToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("oidc: crypto/rand failed: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}
