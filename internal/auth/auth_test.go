package auth_test

import (
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"shelf/internal/auth"
	"shelf/internal/config"
)

// hashFor produces a cheap bcrypt hash. Cost 12 is the shipping cost
// (TestHashPassword_usesCost12 pins it) but ~250 ms per comparison would make
// this file take a minute, so everything except that one test uses MinCost.
func hashFor(tb testing.TB, plain string) []byte {
	tb.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.MinCost)
	if err != nil {
		tb.Fatalf("bcrypt: %v", err)
	}
	return h
}

func key32(tb testing.TB) []byte {
	tb.Helper()
	k := make([]byte, auth.KeyLength)
	for i := range k {
		k[i] = byte(i + 1)
	}
	return k
}

// clock is a manual clock so the TTL and rate-limiter tests assert arithmetic
// rather than wait for wall time.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: time.Unix(1_700_000_000, 0).UTC()} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// enabled builds an authenticator with a password, a fake clock and a sleep
// that records rather than blocks.
func enabled(tb testing.TB, password string) (*auth.Authenticator, *clock, *[]time.Duration) {
	tb.Helper()
	c := newClock()
	var slept []time.Duration
	var mu sync.Mutex
	a, err := auth.New(auth.Options{
		PasswordHash: hashFor(tb, password),
		SessionKey:   key32(tb),
		SessionTTL:   2 * time.Hour,
		Now:          c.Now,
		Logger:       slog.New(slog.DiscardHandler),
		Sleep: func(d time.Duration) {
			mu.Lock()
			slept = append(slept, d)
			mu.Unlock()
		},
	})
	if err != nil {
		tb.Fatalf("auth.New: %v", err)
	}
	return a, c, &slept
}

func req(remote string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	r.RemoteAddr = remote
	return r
}

// NFR-SEC-002 — absent config means no password (ruling E-8).
func TestNew_noPasswordHash_isDisabled(t *testing.T) {
	t.Parallel()
	a, err := auth.New(auth.Options{})
	if err != nil {
		t.Fatalf("auth.New: %v", err)
	}
	if a.Enabled() {
		t.Fatal("Enabled() = true for an authenticator with no password")
	}
	if !a.Authenticated(req("10.0.0.1:1234")) {
		t.Fatal("Authenticated() = false with auth disabled; nothing is being proved")
	}
	if _, _, err := a.Login(req("10.0.0.1:1234"), []byte("anything")); !errors.Is(err, auth.ErrDisabled) {
		t.Fatalf("Login error = %v, want ErrDisabled", err)
	}
}

func TestNew_passwordWithoutSessionKey_isRefused(t *testing.T) {
	t.Parallel()
	_, err := auth.New(auth.Options{PasswordHash: hashFor(t, "hunter2")})
	if err == nil {
		t.Fatal("auth.New accepted a password with no session key")
	}
}

func TestNew_malformedHash_isRefused(t *testing.T) {
	t.Parallel()
	_, err := auth.New(auth.Options{PasswordHash: []byte("not-a-bcrypt-hash"), SessionKey: key32(t)})
	if err == nil {
		t.Fatal("auth.New accepted a hash bcrypt cannot read")
	}
}

// NFR-SEC-002 — the correct password mints a session the same authenticator
// then accepts.
func TestLogin_correctPassword_issuesAVerifiableSession(t *testing.T) {
	t.Parallel()
	a, c, _ := enabled(t, "hunter2")

	token, expires, err := a.Login(req("10.0.0.1:1"), []byte("hunter2"))
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if token == "" {
		t.Fatal("Login returned an empty token")
	}
	if want := c.Now().Add(2 * time.Hour); !expires.Equal(want) {
		t.Errorf("expires = %v, want %v", expires, want)
	}

	r := req("10.0.0.1:1")
	r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
	if !a.Authenticated(r) {
		t.Fatal("Authenticated() = false for a freshly issued token")
	}
}

func TestAuthenticated_noCookie_isFalse(t *testing.T) {
	t.Parallel()
	a, _, _ := enabled(t, "hunter2")
	if a.Authenticated(req("10.0.0.1:1")) {
		t.Fatal("Authenticated() = true with no cookie")
	}
}

func TestAuthenticated_expiredToken_isFalse(t *testing.T) {
	t.Parallel()
	a, c, _ := enabled(t, "hunter2")
	token, _, err := a.Login(req("10.0.0.1:1"), []byte("hunter2"))
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	c.advance(2*time.Hour + time.Second)

	r := req("10.0.0.1:1")
	r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
	if a.Authenticated(r) {
		t.Fatal("Authenticated() = true for an expired token")
	}
}

// The token is HMAC-signed: flipping any byte of the payload or of the
// signature must fail verification (arch §8.2).
func TestAuthenticated_tamperedToken_isFalse(t *testing.T) {
	t.Parallel()
	a, _, _ := enabled(t, "hunter2")
	token, _, err := a.Login(req("10.0.0.1:1"), []byte("hunter2"))
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	body, sig, _ := strings.Cut(token, ".")

	for _, tc := range []struct {
		name  string
		token string
	}{
		{"flipped payload", flip(body) + "." + sig},
		{"flipped signature", body + "." + flip(sig)},
		{"no signature", body},
		{"empty", ""},
		{"signature only", "." + sig},
		{"unsigned payload", body + "."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := req("10.0.0.1:1")
			r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: tc.token})
			if a.Authenticated(r) {
				t.Fatalf("Authenticated() = true for %q", tc.name)
			}
		})
	}
}

// A token signed with a different key must not verify: deleting session.key is
// the documented way to log everybody out.
func TestAuthenticated_differentSessionKey_isFalse(t *testing.T) {
	t.Parallel()
	a, _, _ := enabled(t, "hunter2")
	token, _, err := a.Login(req("10.0.0.1:1"), []byte("hunter2"))
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	other := make([]byte, auth.KeyLength)
	for i := range other {
		other[i] = 0xAA
	}
	b, err := auth.New(auth.Options{PasswordHash: hashFor(t, "hunter2"), SessionKey: other})
	if err != nil {
		t.Fatalf("auth.New: %v", err)
	}
	r := req("10.0.0.1:1")
	r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
	if b.Authenticated(r) {
		t.Fatal("a token signed with another key verified")
	}
}

func flip(s string) string {
	if s == "" {
		return "x"
	}
	b := []byte(s)
	if b[0] == 'A' {
		b[0] = 'B'
	} else {
		b[0] = 'A'
	}
	return string(b)
}

// arch §8.2 — every failure is padded to at least 250 ms.
func TestLogin_wrongPassword_isPaddedToTheFailureFloor(t *testing.T) {
	t.Parallel()
	a, _, slept := enabled(t, "hunter2")

	_, _, err := a.Login(req("10.0.0.1:1"), []byte("wrong"))
	if !errors.Is(err, auth.ErrBadCredentials) {
		t.Fatalf("Login error = %v, want ErrBadCredentials", err)
	}
	if len(*slept) != 1 {
		t.Fatalf("padding sleeps = %d, want 1", len(*slept))
	}
	if got := (*slept)[0]; got != auth.MinFailureDelay {
		t.Errorf("padded by %v, want %v (the fake clock does not advance)", got, auth.MinFailureDelay)
	}
}

// arch §8.2 / impl-plan §7.3 — 5 attempts per minute, burst 5, then 429 with a
// Retry-After. The sixth attempt must not even reach bcrypt.
func TestLogin_sixthAttemptInAMinute_isRateLimited(t *testing.T) {
	t.Parallel()
	a, c, slept := enabled(t, "hunter2")

	for i := range auth.LoginBurst {
		if _, _, err := a.Login(req("10.0.0.1:1"), []byte("wrong")); !errors.Is(err, auth.ErrBadCredentials) {
			t.Fatalf("attempt %d: error = %v, want ErrBadCredentials", i+1, err)
		}
	}
	before := len(*slept)

	_, _, err := a.Login(req("10.0.0.1:1"), []byte("wrong"))
	if !errors.Is(err, auth.ErrRateLimited) {
		t.Fatalf("attempt 6: error = %v, want ErrRateLimited", err)
	}
	var rl *auth.RateLimitError
	if !errors.As(err, &rl) {
		t.Fatalf("attempt 6: error %T does not carry a RateLimitError", err)
	}
	if rl.RetryAfter <= 0 {
		t.Errorf("RetryAfter = %v, want > 0", rl.RetryAfter)
	}
	if len(*slept) != before {
		t.Error("a rate-limited attempt paid the bcrypt failure floor; it must be refused before the compare")
	}

	// Even the correct password is refused while the bucket is empty.
	if _, _, err := a.Login(req("10.0.0.1:1"), []byte("hunter2")); !errors.Is(err, auth.ErrRateLimited) {
		t.Fatalf("correct password while limited: error = %v, want ErrRateLimited", err)
	}

	// One token refills after a fifth of a minute.
	c.advance(12*time.Second + time.Millisecond)
	if _, _, err := a.Login(req("10.0.0.1:1"), []byte("hunter2")); err != nil {
		t.Fatalf("after refill: error = %v, want success", err)
	}
}

// The bucket is per client address: one attacker must not lock out the user.
func TestLogin_rateLimitIsPerClientAddress(t *testing.T) {
	t.Parallel()
	a, _, _ := enabled(t, "hunter2")

	for range auth.LoginBurst + 1 {
		_, _, _ = a.Login(req("10.0.0.1:1"), []byte("wrong"))
	}
	if _, _, err := a.Login(req("10.0.0.9:1"), []byte("hunter2")); err != nil {
		t.Fatalf("second address: error = %v, want success", err)
	}
}

// X-Forwarded-For is honoured only behind `server.trusted_proxy_headers`;
// otherwise a client could mint a new identity per request and empty the
// limiter of meaning.
func TestClientIP_forwardedForIsHonouredOnlyWhenTrusted(t *testing.T) {
	t.Parallel()
	r := req("10.0.0.1:5555")
	r.Header.Set("X-Forwarded-For", "203.0.113.7, 10.0.0.2")

	if got := auth.ClientIP(r, false); got != "10.0.0.1" {
		t.Errorf("untrusted ClientIP = %q, want the socket address 10.0.0.1", got)
	}
	if got := auth.ClientIP(r, true); got != "203.0.113.7" {
		t.Errorf("trusted ClientIP = %q, want 203.0.113.7", got)
	}
}

func TestLogin_untrustedForwardedFor_cannotEvadeTheLimiter(t *testing.T) {
	t.Parallel()
	a, _, _ := enabled(t, "hunter2")

	for i := range auth.LoginBurst + 1 {
		r := req("10.0.0.1:1")
		r.Header.Set("X-Forwarded-For", "203.0.113."+string(rune('0'+i)))
		_, _, err := a.Login(r, []byte("wrong"))
		if i < auth.LoginBurst && !errors.Is(err, auth.ErrBadCredentials) {
			t.Fatalf("attempt %d: error = %v, want ErrBadCredentials", i+1, err)
		}
		if i == auth.LoginBurst && !errors.Is(err, auth.ErrRateLimited) {
			t.Fatalf("attempt %d: error = %v, want ErrRateLimited despite a rotating X-Forwarded-For", i+1, err)
		}
	}
}

// arch §8.2 — HttpOnly, SameSite=Lax, Path scoped to the base path, and Secure
// only when the request actually arrived over TLS.
func TestCookie_attributes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		base     string
		wantPath string
	}{
		{"no base path", "", "/"},
		{"under /reader", "/reader", "/reader/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, err := auth.New(auth.Options{
				PasswordHash: hashFor(t, "hunter2"),
				SessionKey:   key32(t),
				BasePath:     tc.base,
			})
			if err != nil {
				t.Fatalf("auth.New: %v", err)
			}
			c := a.Cookie(req("10.0.0.1:1"), "token", time.Now().Add(time.Hour))
			if c.Name != auth.CookieName {
				t.Errorf("cookie name = %q, want %q", c.Name, auth.CookieName)
			}
			if c.Path != tc.wantPath {
				t.Errorf("cookie path = %q, want %q", c.Path, tc.wantPath)
			}
			if !c.HttpOnly {
				t.Error("cookie is not HttpOnly")
			}
			if c.SameSite != http.SameSiteLaxMode {
				t.Errorf("cookie SameSite = %v, want Lax", c.SameSite)
			}
			if c.Secure {
				t.Error("cookie is Secure on a plain-HTTP request; it would be undeliverable on a LAN")
			}
			if cleared := a.ClearCookie(req("10.0.0.1:1")); cleared.MaxAge >= 0 || cleared.Value != "" {
				t.Errorf("ClearCookie = %+v, want an empty value with a negative Max-Age", cleared)
			}
		})
	}
}

func TestSecure_trustedForwardedProto(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		trusted bool
		proto   string
		want    bool
	}{
		{"untrusted https header", false, "https", false},
		{"trusted https header", true, "https", true},
		{"trusted http header", true, "http", false},
		{"trusted list", true, "https, http", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, err := auth.New(auth.Options{
				PasswordHash:        hashFor(t, "x"),
				SessionKey:          key32(t),
				TrustedProxyHeaders: tc.trusted,
			})
			if err != nil {
				t.Fatalf("auth.New: %v", err)
			}
			r := req("10.0.0.1:1")
			r.Header.Set("X-Forwarded-Proto", tc.proto)
			if got := a.Secure(r); got != tc.want {
				t.Errorf("Secure() = %v, want %v", got, tc.want)
			}
		})
	}
}

// arch §8.2 — `shelf hash-password` prints a cost-12 hash.
func TestHashPassword_usesCost12(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("bcrypt cost 12 is deliberately slow")
	}
	h, err := auth.HashPassword([]byte("hunter2"))
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	cost, err := bcrypt.Cost([]byte(h))
	if err != nil {
		t.Fatalf("bcrypt.Cost: %v", err)
	}
	if cost != auth.HashCost {
		t.Errorf("cost = %d, want %d", cost, auth.HashCost)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(h), []byte("hunter2")); err != nil {
		t.Errorf("the printed hash does not verify its own password: %v", err)
	}
}

// arch §8.2 — the key is 32 random bytes at mode 0600, created on first boot
// and stable afterwards.
func TestLoadOrCreateKey_createsOnceAtMode0600(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "sub", "session.key")

	first, err := auth.LoadOrCreateKey(path)
	if err != nil {
		t.Fatalf("LoadOrCreateKey: %v", err)
	}
	if len(first) != auth.KeyLength {
		t.Fatalf("key length = %d, want %d", len(first), auth.KeyLength)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if runtime.GOOS != "windows" {
		if perm := fi.Mode().Perm(); perm != 0o600 {
			t.Errorf("session.key mode = %04o, want 0600", perm)
		}
	}

	second, err := auth.LoadOrCreateKey(path)
	if err != nil {
		t.Fatalf("LoadOrCreateKey (reload): %v", err)
	}
	if string(second) != string(first) {
		t.Error("the key changed on reload; every session would be invalidated on every restart")
	}
}

func TestLoadOrCreateKey_wrongLength_isRefused(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "session.key")
	if err := os.WriteFile(path, []byte("short"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := auth.LoadOrCreateKey(path); err == nil {
		t.Fatal("LoadOrCreateKey accepted a truncated key")
	}
}

// The config seam: no `auth:` block means disabled, a hash is taken verbatim,
// and both forms at once is refused.
func TestFromConfig(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	t.Run("no auth block is disabled", func(t *testing.T) {
		t.Parallel()
		opts, err := auth.FromConfig(&config.Config{})
		if err != nil {
			t.Fatalf("FromConfig: %v", err)
		}
		if len(opts.PasswordHash) != 0 {
			t.Error("a config with no auth: block produced a password hash")
		}
		a, err := auth.New(opts)
		if err != nil {
			t.Fatalf("auth.New: %v", err)
		}
		if a.Enabled() {
			t.Error("Enabled() = true for a config with no auth: block (ruling E-8)")
		}
	})

	t.Run("password_hash is used as-is", func(t *testing.T) {
		t.Parallel()
		hash := string(hashFor(t, "hunter2"))
		cfg := &config.Config{Auth: &config.Auth{
			PasswordHash:   hash,
			SessionTTL:     time.Hour,
			SessionKeyFile: filepath.Join(dir, "as-is.key"),
		}}
		opts, err := auth.FromConfig(cfg)
		if err != nil {
			t.Fatalf("FromConfig: %v", err)
		}
		if string(opts.PasswordHash) != hash {
			t.Error("password_hash was not used verbatim")
		}
		if len(opts.SessionKey) != auth.KeyLength {
			t.Errorf("session key length = %d, want %d", len(opts.SessionKey), auth.KeyLength)
		}
	})

	t.Run("both forms is an error", func(t *testing.T) {
		t.Parallel()
		cfg := &config.Config{Auth: &config.Auth{
			Password:       "hunter2",
			PasswordHash:   string(hashFor(t, "hunter2")),
			SessionKeyFile: filepath.Join(dir, "both.key"),
		}}
		if _, err := auth.FromConfig(cfg); !errors.Is(err, auth.ErrBothPasswordForms) {
			t.Fatalf("FromConfig error = %v, want ErrBothPasswordForms", err)
		}
	})

	t.Run("empty auth block is an error", func(t *testing.T) {
		t.Parallel()
		cfg := &config.Config{Auth: &config.Auth{SessionKeyFile: filepath.Join(dir, "empty.key")}}
		if _, err := auth.FromConfig(cfg); !errors.Is(err, auth.ErrNoPassword) {
			t.Fatalf("FromConfig error = %v, want ErrNoPassword", err)
		}
	})
}
