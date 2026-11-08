package oidc

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// clockSkew is ruling E-74 §2.6's ±60 seconds.
const clockSkew = 60 * time.Second

// tenant is uni-auth's reserved tid, which its integration guide asks every
// relying party to check rather than ignore.
const tenant = "default"

type audience []string

func (a *audience) UnmarshalJSON(b []byte) error {
	if bytes.HasPrefix(bytes.TrimSpace(b), []byte("[")) {
		var many []string
		if err := json.Unmarshal(b, &many); err != nil {
			return err
		}
		*a = many
		return nil
	}
	var one string
	if err := json.Unmarshal(b, &one); err != nil {
		return err
	}
	*a = audience{one}
	return nil
}

type claims struct {
	Iss   string   `json:"iss"`
	Sub   string   `json:"sub"`
	Aud   audience `json:"aud"`
	Exp   *float64 `json:"exp"`
	Nbf   *float64 `json:"nbf"`
	Iat   *float64 `json:"iat"`
	Nonce string   `json:"nonce"`
	Tid   string   `json:"tid"`
	Sid   string   `json:"sid"`
}

type jwtHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
}

// tokenCheck is what one token must satisfy beyond its signature.
type tokenCheck struct {
	audience string
	nonce    *string
}

// verify checks an EdDSA-signed JWT: alg, kid, signature, iss, aud, exp, nbf,
// iat, tid and, for an ID token, nonce (ruling E-74 §2.6).
func (s *Service) verify(ctx context.Context, token string, want tokenCheck) (*claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("oidc: a token is not a three-part JWT")
	}
	var h jwtHeader
	if err := decodeSegment(parts[0], &h); err != nil {
		return nil, fmt.Errorf("oidc: token header: %w", err)
	}
	if h.Alg != "EdDSA" {
		return nil, fmt.Errorf("oidc: token algorithm %q is not EdDSA", h.Alg)
	}
	if h.Kid == "" {
		return nil, errors.New("oidc: token has no kid")
	}
	key, err := s.prov.key(ctx, h.Kid)
	if err != nil {
		return nil, err
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, errors.New("oidc: token signature is not base64url")
	}
	if !ed25519.Verify(key, []byte(parts[0]+"."+parts[1]), sig) {
		return nil, errors.New("oidc: token signature does not verify")
	}
	var c claims
	if err := decodeSegment(parts[1], &c); err != nil {
		return nil, fmt.Errorf("oidc: token claims: %w", err)
	}
	now := s.now()
	switch {
	case c.Iss != s.cfg.Issuer:
		return nil, fmt.Errorf("oidc: token issuer %q is not %q", c.Iss, s.cfg.Issuer)
	case !slices.Contains(c.Aud, want.audience):
		return nil, fmt.Errorf("oidc: token audience does not include %q", want.audience)
	case c.Exp == nil || now.After(unix(*c.Exp).Add(clockSkew)):
		return nil, errors.New("oidc: token is expired or has no exp")
	case c.Nbf != nil && unix(*c.Nbf).After(now.Add(clockSkew)):
		return nil, errors.New("oidc: token is not yet valid")
	case c.Iat == nil || unix(*c.Iat).After(now.Add(clockSkew)):
		return nil, errors.New("oidc: token iat is missing or in the future")
	case c.Tid != tenant:
		return nil, fmt.Errorf("oidc: token tid %q is not %q", c.Tid, tenant)
	case c.Sub == "":
		return nil, errors.New("oidc: token has no sub")
	case want.nonce != nil && c.Nonce != *want.nonce:
		return nil, errors.New("oidc: token nonce does not match")
	}
	return &c, nil
}

func decodeSegment(seg string, into any) error {
	raw, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, into)
}

func unix(f float64) time.Time { return time.Unix(int64(f), 0) }
