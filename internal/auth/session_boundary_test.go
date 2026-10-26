package auth_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"shelf/internal/auth"
)

// withToken returns a request carrying token as the session cookie.
func withToken(token string) *http.Request {
	r := req("10.0.0.1:1")
	r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
	return r
}

// session.go: "a token is dead the second it expires". One second before exp
// it verifies; at exp exactly it does not.
func TestAuthenticated_expiryBoundary(t *testing.T) {
	t.Parallel()
	a, c, _ := enabled(t, "hunter2")
	token, _, err := a.Login(req("10.0.0.1:1"), []byte("hunter2"))
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	c.advance(2*time.Hour - time.Second)
	if !a.Authenticated(withToken(token)) {
		t.Fatal("Authenticated() = false one second before expiry")
	}
	c.advance(time.Second)
	if a.Authenticated(withToken(token)) {
		t.Fatal("Authenticated() = true at the expiry second; the token must be dead the second it expires")
	}
}

// session.go: a token "claiming to have been issued later than that [60 s of
// skew] is a clock problem or a forgery attempt and is refused". The token is minted through
// the public API by an authenticator whose clock is an hour ahead and sharing
// the key, so it is otherwise genuine and unexpired.
//
// The second control is what makes the refusal mean "future iat": a token from
// a minter with the same key on present's clock must be accepted by present.
// Without it, a key mismatch between the two would refuse the future token on
// its signature and the test would stay green with the iat check deleted.
func TestAuthenticated_issuedInTheFuture_isRefused(t *testing.T) {
	t.Parallel()
	present, c, _ := enabled(t, "hunter2")
	minterKey := key32(t)
	mint := func(at time.Time) (*auth.Authenticator, string) {
		t.Helper()
		m, err := auth.New(auth.Options{
			PasswordHash: hashFor(t, "hunter2"),
			SessionKey:   minterKey,
			SessionTTL:   2 * time.Hour,
			Now:          func() time.Time { return at },
			Logger:       slog.New(slog.DiscardHandler),
			Sleep:        func(time.Duration) {},
		})
		if err != nil {
			t.Fatalf("auth.New: %v", err)
		}
		token, _, err := m.Login(req("10.0.0.1:1"), []byte("hunter2"))
		if err != nil {
			t.Fatalf("Login: %v", err)
		}
		return m, token
	}

	if _, now := mint(c.Now()); !present.Authenticated(withToken(now)) {
		t.Fatal("control: present rejects a token minted now with the minter's key, " +
			"so the keys differ and the refusal below would prove nothing about iat")
	}
	ahead, token := mint(c.Now().Add(time.Hour))
	if !ahead.Authenticated(withToken(token)) {
		t.Fatal("control: the minting authenticator rejects its own token")
	}
	if present.Authenticated(withToken(token)) {
		t.Fatal("Authenticated() = true for a token issued an hour in the future")
	}
}

// session.go: the issue time may run "up to 60 seconds ahead of this clock".
// Tokens are built to arch §8.2's wire format with the test's key, so iat is
// the only thing that differs: +60 s verifies, +61 s does not.
func TestAuthenticated_issuedAtSkewBoundary(t *testing.T) {
	t.Parallel()
	a, c, _ := enabled(t, "hunter2")
	now := c.Now().Unix()
	mint := func(iat int64) string {
		body := base64.RawURLEncoding.EncodeToString(
			fmt.Appendf(nil, `{"iat":%d,"exp":%d,"v":1}`, iat, now+3600))
		m := hmac.New(sha256.New, key32(t))
		m.Write([]byte(body))
		return body + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
	}
	if !a.Authenticated(withToken(mint(now + 60))) {
		t.Error("Authenticated() = false for iat 60 s ahead; that much skew is allowed")
	}
	if a.Authenticated(withToken(mint(now + 61))) {
		t.Error("Authenticated() = true for iat 61 s ahead; past the skew allowance it must be refused")
	}
}

// session.go: "a token carrying an unknown version simply fails to verify".
// The token is built to arch §8.2's wire format — base64url(payload) "."
// base64url(HMAC-SHA256(key, payload)) — with the test's key, and the v=1
// control proves the forgery is otherwise correct.
func TestAuthenticated_unknownVersion_isRefused(t *testing.T) {
	t.Parallel()
	a, c, _ := enabled(t, "hunter2")
	now := c.Now().Unix()
	mint := func(v int) string {
		body := base64.RawURLEncoding.EncodeToString(
			fmt.Appendf(nil, `{"iat":%d,"exp":%d,"v":%d}`, now, now+3600, v))
		m := hmac.New(sha256.New, key32(t))
		m.Write([]byte(body))
		return body + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
	}
	if !a.Authenticated(withToken(mint(1))) {
		t.Fatal("control: a hand-built v=1 token does not verify, so the forgery is wrong")
	}
	for _, v := range []int{0, 2} {
		if a.Authenticated(withToken(mint(v))) {
			t.Errorf("Authenticated() = true for a correctly signed token with v=%d", v)
		}
	}
}

// arch §8.2 — Secure "when the request arrived over TLS (directly, ...)". The
// direct case needs no trusted proxy headers.
func TestSecure_directTLS(t *testing.T) {
	t.Parallel()
	a, err := auth.New(auth.Options{PasswordHash: hashFor(t, "x"), SessionKey: key32(t)})
	if err != nil {
		t.Fatalf("auth.New: %v", err)
	}
	r := req("10.0.0.1:1")
	r.TLS = &tls.ConnectionState{}
	if !a.Secure(r) {
		t.Error("Secure() = false for a request that arrived over TLS")
	}
	if c := a.Cookie(r, "token", time.Now().Add(time.Hour)); !c.Secure {
		t.Error("session cookie is not Secure on a TLS request")
	}
}
