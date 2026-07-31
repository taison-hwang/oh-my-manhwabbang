// Package auth implements SHELF's optional single-password authentication
// (NFR-SEC-002, arch-backend §8.2).
//
// Three properties define the whole design and every one of them is a
// requirement rather than a preference:
//
//   - **It is optional, and off by default.** A configuration with no `auth:`
//     block produces a disabled [Authenticator] whose [Authenticator.Enabled]
//     is false; the HTTP layer then gates nothing at all (ruling E-8). There is
//     no first-run prompt and no default password.
//   - **The stored secret is a bcrypt hash, never a plaintext.** `auth.password`
//     is hashed at startup with cost 12 and the hash is what lives in memory;
//     `shelf hash-password` ([HashPassword]) lets an operator keep the
//     plaintext out of the YAML entirely.
//   - **A wrong password is expensive and rate-limited.** Every failure is
//     padded to at least [MinFailureDelay] and each client address gets a
//     [LoginBurst]-token bucket refilling at [LoginRate]; beyond that the answer
//     is `429` with `Retry-After` and no bcrypt comparison is performed at all.
//
// The session itself is stateless: an HMAC-SHA256-signed token (session.go)
// verified with [hmac.Equal]. There is no server-side session table to grow, to
// leak or to lose across a restart, and revoking every session is one `rm` of
// the key file.
//
// CSRF needs no token in v1: the cookie is `SameSite=Lax` and no state-changing
// endpoint is a `GET`, so a cross-site request can never carry the session
// (decision D-23, arch §8.2).
package auth

import (
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"shelf/internal/config"
)

// Tunables fixed by arch §8.2. They are constants rather than configuration on
// purpose: an operator who can lower the bcrypt cost or widen the rate limit
// has been handed a way to make the product less safe than the requirement it
// claims to satisfy.
const (
	// CookieName is the session cookie. It is prefixed with the product name
	// so two SHELF instances on the same host under different base paths do not
	// collide in a browser jar.
	CookieName = "shelf_session"

	// HashCost is the bcrypt cost applied to `auth.password` and by
	// `shelf hash-password`. 12 is ~250 ms on this class of machine, which is
	// also why MinFailureDelay is 250 ms: a correct password and an incorrect
	// one take indistinguishable time.
	HashCost = 12

	// MinFailureDelay is the floor on how long a rejected login takes
	// (arch §8.2). It is applied to *every* failure, including one whose bcrypt
	// comparison returned early, so response time carries no signal.
	MinFailureDelay = 250 * time.Millisecond

	// LoginBurst and LoginRate are the per-IP token bucket of arch §8.2:
	// 5 attempts per minute, burst 5 — i.e. five tokens to spend at once and
	// one new token every twelve seconds.
	LoginBurst = 5
	LoginRate  = time.Minute / LoginBurst

	// KeyLength is the size of the HMAC session key in bytes.
	KeyLength = 32

	// keyFileMode is the permission the session key file is created with and
	// the permission it must not exceed.
	keyFileMode os.FileMode = 0o600
)

// Sentinel errors. Callers compare with errors.Is, never by string
// (impl-plan §5.1).
var (
	// ErrDisabled reports a login attempt against a build with no password
	// configured. The HTTP layer answers 404-free: it simply never routes there
	// when Enabled is false, so this is a programming error, not a user one.
	ErrDisabled = errors.New("auth: authentication is not enabled")

	// ErrBadCredentials reports a wrong password. It maps to `401 unauthorized`.
	ErrBadCredentials = errors.New("auth: incorrect password")

	// ErrRateLimited reports that this client has spent its token bucket. It
	// maps to `429` with `Retry-After`; match [RateLimitError] with errors.As
	// to recover how long to wait.
	ErrRateLimited = errors.New("auth: too many attempts")

	// ErrBothPasswordForms reports `auth.password` and `auth.password_hash`
	// both set — the configuration does not say which one is authoritative,
	// so refusing is the only honest answer.
	ErrBothPasswordForms = errors.New("auth: password and password_hash are mutually exclusive")

	// ErrNoPassword reports an `auth:` block that enables authentication
	// without supplying either form of the secret.
	ErrNoPassword = errors.New("auth: neither password nor password_hash is set")
)

// RateLimitError is the typed form of ErrRateLimited, carrying the wait the
// `Retry-After` header should advertise.
type RateLimitError struct {
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("auth: too many attempts, retry in %s", e.RetryAfter.Round(time.Second))
}

// Is makes errors.Is(err, ErrRateLimited) true for every wait.
func (e *RateLimitError) Is(target error) bool { return target == ErrRateLimited }

// Options configures an [Authenticator].
//
// The zero value is a *disabled* authenticator, which is exactly what a
// configuration with no `auth:` block must produce.
type Options struct {
	// PasswordHash is the bcrypt hash to compare against. Empty disables
	// authentication entirely.
	PasswordHash []byte
	// SessionKey is the HMAC key, [KeyLength] bytes. Required when
	// PasswordHash is set; [LoadOrCreateKey] produces one.
	SessionKey []byte
	// SessionTTL is how long an issued token stays valid. Zero selects the
	// config default of 720h.
	SessionTTL time.Duration
	// BasePath is `server.base_path`, already normalised to "" or "/prefix".
	// It becomes the cookie's Path, so a SHELF at /reader cannot see the
	// cookie of a SHELF at /.
	BasePath string
	// TrustedProxyHeaders honours X-Forwarded-For and X-Forwarded-Proto.
	// Leaving it false is what stops a client forging its own address and
	// walking around the rate limiter one fake IP at a time.
	TrustedProxyHeaders bool
	// Now is the clock; nil selects time.Now. Tests set it.
	Now func() time.Time
	// Sleep is how a failure is padded to MinFailureDelay; nil selects
	// time.Sleep. Tests replace it so the suite does not spend a second per
	// wrong password.
	Sleep func(time.Duration)
	// Logger; nil selects slog.Default(). It never receives a password or a
	// key (impl-plan §5.1).
	Logger *slog.Logger
}

// Authenticator verifies passwords and mints session cookies. One value serves
// the process and it is safe for concurrent use.
type Authenticator struct {
	hash    []byte
	signer  *signer
	ttl     time.Duration
	base    string
	trusted bool
	now     func() time.Time
	sleep   func(time.Duration)
	log     *slog.Logger
	limiter *limiter
}

// New builds an Authenticator. An empty Options.PasswordHash yields a disabled
// one — that is not an error, it is the default deployment (ruling E-8).
func New(opts Options) (*Authenticator, error) {
	a := &Authenticator{
		hash:    opts.PasswordHash,
		ttl:     opts.SessionTTL,
		base:    opts.BasePath,
		trusted: opts.TrustedProxyHeaders,
		now:     opts.Now,
		sleep:   opts.Sleep,
		log:     opts.Logger,
	}
	if a.now == nil {
		a.now = time.Now
	}
	if a.sleep == nil {
		a.sleep = time.Sleep
	}
	if a.log == nil {
		a.log = slog.Default()
	}
	if a.ttl <= 0 {
		a.ttl = 720 * time.Hour
	}
	a.limiter = newLimiter(LoginBurst, LoginRate, a.now)

	if len(a.hash) == 0 {
		return a, nil
	}
	if _, err := bcrypt.Cost(a.hash); err != nil {
		return nil, fmt.Errorf("reading password hash: %w", err)
	}
	if len(opts.SessionKey) != KeyLength {
		return nil, fmt.Errorf("session key: want %d bytes, got %d", KeyLength, len(opts.SessionKey))
	}
	a.signer = newSigner(opts.SessionKey)
	return a, nil
}

// FromConfig builds the Options for a loaded configuration, hashing a plaintext
// `auth.password` with cost 12 and loading (or creating) the session key.
//
// The plaintext is not zeroed, and cannot be: config.Auth.Password is a Go
// string and strings are immutable. arch §8.2's "the plaintext is zeroed" is
// achievable only if the config type carries []byte, which is WP-01's decision
// and not this package's to make. The mitigation that is available — and the
// one the documentation tells operators to use — is `shelf hash-password`, so
// the plaintext never enters the process at all.
func FromConfig(cfg *config.Config) (Options, error) {
	opts := Options{
		BasePath:            cfg.Server.BasePath,
		TrustedProxyHeaders: cfg.Server.TrustedProxyHeaders,
	}
	if cfg.Auth == nil {
		return opts, nil
	}
	opts.SessionTTL = cfg.Auth.SessionTTL

	hasPlain := cfg.Auth.Password != ""
	hasHash := cfg.Auth.PasswordHash != ""
	switch {
	case hasPlain && hasHash:
		return Options{}, ErrBothPasswordForms
	case hasHash:
		opts.PasswordHash = []byte(cfg.Auth.PasswordHash)
	case hasPlain:
		h, err := bcrypt.GenerateFromPassword([]byte(cfg.Auth.Password), HashCost)
		if err != nil {
			return Options{}, fmt.Errorf("hashing auth.password: %w", err)
		}
		opts.PasswordHash = h
	default:
		return Options{}, ErrNoPassword
	}

	key, err := LoadOrCreateKey(cfg.Auth.SessionKeyFile)
	if err != nil {
		return Options{}, err
	}
	opts.SessionKey = key
	return opts, nil
}

// HashPassword produces the bcrypt hash `shelf hash-password` prints and
// `auth.password_hash` accepts.
func HashPassword(plain []byte) (string, error) {
	h, err := bcrypt.GenerateFromPassword(plain, HashCost)
	if err != nil {
		return "", fmt.Errorf("hashing password: %w", err)
	}
	return string(h), nil
}

// LoadOrCreateKey reads the HMAC session key, generating [KeyLength] random
// bytes at mode 0600 on first boot (arch §8.2).
//
// Deleting the file invalidates every issued session, which is the documented
// way to log everybody out.
func LoadOrCreateKey(path string) ([]byte, error) {
	if path == "" {
		return nil, errors.New("session key file: path is empty")
	}
	data, err := os.ReadFile(path) //nolint:gosec // operator-supplied path from the config
	switch {
	case err == nil:
		if len(data) != KeyLength {
			return nil, fmt.Errorf("session key %s: want %d bytes, got %d", filepath.Base(path), KeyLength, len(data))
		}
		return data, nil
	case !errors.Is(err, os.ErrNotExist):
		return nil, fmt.Errorf("reading session key: %w", err)
	}

	key := make([]byte, KeyLength)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generating session key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("creating session key directory: %w", err)
	}
	// O_EXCL so two processes racing on first boot cannot each believe they
	// wrote the key that the other is now signing with.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, keyFileMode)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return LoadOrCreateKey(path)
		}
		return nil, fmt.Errorf("creating session key: %w", err)
	}
	if _, err := f.Write(key); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("writing session key: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("writing session key: %w", err)
	}
	return key, nil
}

// Enabled reports whether a password is required (NFR-SEC-002).
func (a *Authenticator) Enabled() bool { return len(a.hash) > 0 }

// SessionTTL is how long an issued cookie lives.
func (a *Authenticator) SessionTTL() time.Duration { return a.ttl }

// Authenticated reports whether the request carries a valid, unexpired session.
// A disabled Authenticator reports true for everything: there is nothing to
// prove.
func (a *Authenticator) Authenticated(r *http.Request) bool {
	if !a.Enabled() {
		return true
	}
	c, err := r.Cookie(CookieName)
	if err != nil || c.Value == "" {
		return false
	}
	return a.signer.verify(c.Value, a.now())
}

// Login checks a password and mints a session token.
//
// The order is deliberate: the rate limiter is consulted *before* bcrypt, so a
// flood costs the server a map lookup rather than 250 ms of key stretching per
// attempt. A successful login refunds nothing — the bucket is about attempts,
// not failures, so a script cannot alternate a known-good password in to keep
// its budget topped up.
func (a *Authenticator) Login(r *http.Request, password []byte) (token string, expires time.Time, err error) {
	if !a.Enabled() {
		return "", time.Time{}, ErrDisabled
	}
	client := ClientIP(r, a.trusted)
	if wait, ok := a.limiter.allow(client); !ok {
		a.log.Warn("login rate limited", "remote", client, "retry_after_ms", wait.Milliseconds())
		return "", time.Time{}, &RateLimitError{RetryAfter: wait}
	}

	start := a.now()
	cmpErr := bcrypt.CompareHashAndPassword(a.hash, password)
	if cmpErr != nil {
		if elapsed := a.now().Sub(start); elapsed < MinFailureDelay {
			a.sleep(MinFailureDelay - elapsed)
		}
		a.log.Warn("login failed", "remote", client)
		return "", time.Time{}, ErrBadCredentials
	}

	now := a.now()
	expires = now.Add(a.ttl)
	token = a.signer.issue(now, expires)
	a.log.Info("login succeeded", "remote", client)
	return token, expires, nil
}

// Cookie is the `Set-Cookie` for a freshly issued token.
func (a *Authenticator) Cookie(r *http.Request, token string, expires time.Time) *http.Cookie {
	return &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     a.cookiePath(),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   a.Secure(r),
		MaxAge:   int(time.Until(expires).Seconds()),
		Expires:  expires,
	}
}

// ClearCookie is the `Set-Cookie` that ends a session.
func (a *Authenticator) ClearCookie(r *http.Request) *http.Cookie {
	return &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     a.cookiePath(),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   a.Secure(r),
		MaxAge:   -1,
	}
}

// cookiePath is `{base_path}/`, so a reverse-proxied instance scopes its cookie
// to its own mount point (arch §8.2).
func (a *Authenticator) cookiePath() string {
	if a.base == "" {
		return "/"
	}
	return a.base + "/"
}

// Secure reports whether the request arrived over TLS, directly or — when
// `server.trusted_proxy_headers` is on — according to X-Forwarded-Proto.
//
// It gates the cookie's Secure attribute rather than the request: marking a
// cookie Secure on a plain-HTTP LAN deployment would make it undeliverable and
// lock the user out.
func (a *Authenticator) Secure(r *http.Request) bool {
	if r == nil {
		return false
	}
	if r.TLS != nil {
		return true
	}
	if a.trusted && strings.EqualFold(firstHeaderValue(r.Header.Get("X-Forwarded-Proto")), "https") {
		return true
	}
	return false
}

// ClientIP is the address the rate limiter counts against.
//
// X-Forwarded-For is honoured only when the operator has said a proxy is in
// front (`server.trusted_proxy_headers`). Trusting it unconditionally would let
// any client mint a fresh identity per request and empty the limiter of meaning.
func ClientIP(r *http.Request, trustProxy bool) string {
	if r == nil {
		return ""
	}
	if trustProxy {
		if v := firstHeaderValue(r.Header.Get("X-Forwarded-For")); v != "" {
			return v
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// firstHeaderValue takes the left-most element of a comma-separated header,
// which for X-Forwarded-* is the original client.
func firstHeaderValue(v string) string {
	if v == "" {
		return ""
	}
	if i := strings.IndexByte(v, ','); i >= 0 {
		v = v[:i]
	}
	return strings.TrimSpace(v)
}
