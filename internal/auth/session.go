package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"
)

// sessionVersion is the `v` field of the payload. It exists so a future change
// to the claim set can be recognised and rejected rather than mis-read; a token
// carrying an unknown version simply fails to verify, which logs everybody out
// exactly once.
const sessionVersion = 1

// payload is the token's claim set (arch §8.2): issued-at, expiry, version.
//
// There is deliberately no user id, no role and no nonce. SHELF is single-user
// (prd §1.4) and the session proves exactly one thing — that whoever holds this
// cookie knew the password at time `iat`.
type payload struct {
	IAT int64 `json:"iat"`
	Exp int64 `json:"exp"`
	V   int   `json:"v"`
}

// signer mints and verifies `base64url(payload).base64url(HMAC-SHA256(payload))`
// tokens. It holds only the key, so one value serves every request.
type signer struct {
	key []byte
}

func newSigner(key []byte) *signer {
	k := make([]byte, len(key))
	copy(k, key)
	return &signer{key: k}
}

var enc = base64.RawURLEncoding

// issue returns a signed token valid until `expires`.
func (s *signer) issue(now, expires time.Time) string {
	body, err := json.Marshal(payload{IAT: now.Unix(), Exp: expires.Unix(), V: sessionVersion})
	if err != nil {
		// payload has three scalar fields; json.Marshal cannot fail on it.
		panic("auth: marshalling session payload: " + err.Error())
	}
	b := enc.EncodeToString(body)
	return b + "." + enc.EncodeToString(s.mac(b))
}

// verify reports whether the token is well-formed, correctly signed and not yet
// expired at `now`.
//
// The signature is checked with hmac.Equal — a constant-time comparison — and
// it is checked *before* the payload is parsed, so a forged token never reaches
// the JSON decoder.
func (s *signer) verify(token string, now time.Time) bool {
	body, sig, ok := strings.Cut(token, ".")
	if !ok || body == "" || sig == "" {
		return false
	}
	want, err := enc.DecodeString(sig)
	if err != nil {
		return false
	}
	if !hmac.Equal(want, s.mac(body)) {
		return false
	}
	raw, err := enc.DecodeString(body)
	if err != nil {
		return false
	}
	var p payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return false
	}
	if p.V != sessionVersion {
		return false
	}
	// The window is half-open at both ends: a token is dead the second it
	// expires, and one claiming to have been issued in the future is a clock
	// problem or a forgery attempt and is refused either way.
	if p.Exp <= now.Unix() || p.IAT > now.Unix()+60 {
		return false
	}
	return true
}

func (s *signer) mac(body string) []byte {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte(body))
	return m.Sum(nil)
}
