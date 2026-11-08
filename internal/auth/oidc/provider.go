package oidc

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Cache lifetimes of ruling E-74 §2.6: discovery 1h, JWKS 30 min, and an
// unknown kid refetches the JWKS at most once a minute.
const (
	metadataTTL     = time.Hour
	metadataRetry   = 30 * time.Second
	jwksTTL         = 30 * time.Minute
	jwksRefetchGap  = time.Minute
	maxProviderBody = 1 << 20
	// maxProviderHeader is net/http's server default (DefaultMaxHeaderBytes).
	maxProviderHeader = 1 << 20
)

var errProviderUnavailable = errors.New("oidc: the identity provider could not be reached")

type metadata struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	JWKSURI               string `json:"jwks_uri"`
	EndSessionEndpoint    string `json:"end_session_endpoint"`
}

type provider struct {
	issuer string
	client Doer
	now    func() time.Time

	// fetchMu keeps one discovery fetch in flight, and failAt answers the
	// retries for metadataRetry, so anonymous requests while uni-auth is
	// down do not each become a request to it.
	fetchMu sync.Mutex
	failAt  time.Time
	failErr error

	keysMu     sync.Mutex
	lastKeyTry time.Time
	keysErr    error

	mu          sync.Mutex
	meta        *metadata
	metaAt      time.Time
	keys        map[string]ed25519.PublicKey
	keysAt      time.Time
	keysFetched bool
}

func newProvider(issuer string, client Doer, now func() time.Time) *provider {
	return &provider{issuer: issuer, client: client, now: now}
}

func (p *provider) getJSON(ctx context.Context, url string, into any) error {
	return call(ctx, p.client, http.MethodGet, url, "", into)
}

// call sends one request to uni-auth and decodes its JSON answer. The request
// is built by hand: http.NewRequestWithContext costs binary the NFR-OPS-001
// budget does not have.
func call(ctx context.Context, c Doer, method, rawURL, form string, into any) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	req := (&http.Request{Method: method, URL: u, Host: u.Host, Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header: http.Header{"Accept": {"application/json"}}}).WithContext(ctx)
	if form != "" {
		req.Header["Content-Type"] = []string{"application/x-www-form-urlencoded"}
		req.Body, req.ContentLength = io.NopCloser(strings.NewReader(form)), int64(len(form))
	}
	resp, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", errProviderUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxProviderBody))
	if err != nil {
		return fmt.Errorf("%w: %v", errProviderUnavailable, err)
	}
	if resp.StatusCode != http.StatusOK {
		if form != "" {
			var perr struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal(body, &perr)
			return fmt.Errorf("oidc: token exchange answered %d %s", resp.StatusCode, errorCode(perr.Error))
		}
		return fmt.Errorf("%w: GET %s answered %d", errProviderUnavailable, rawURL, resp.StatusCode)
	}
	if err := json.Unmarshal(body, into); err != nil {
		return fmt.Errorf("oidc: decoding %s: %w", rawURL, err)
	}
	return nil
}

// metadata returns the discovery document, refusing one whose issuer is not
// the configured string exactly (ruling E-74 §2.6).
func (p *provider) metadata(ctx context.Context) (*metadata, error) {
	if m := p.freshMetadata(); m != nil {
		return m, nil
	}
	p.fetchMu.Lock()
	defer p.fetchMu.Unlock()
	if m := p.freshMetadata(); m != nil {
		return m, nil
	}
	if !p.failAt.IsZero() && p.now().Sub(p.failAt) < metadataRetry {
		return nil, p.failErr
	}
	m, err := p.fetchMetadata(ctx)
	if err != nil {
		// A request that gave up is not news about uni-auth.
		if ctx.Err() == nil {
			p.failAt, p.failErr = p.now(), err
		}
		return nil, err
	}
	p.failAt, p.failErr = time.Time{}, nil
	p.mu.Lock()
	p.meta, p.metaAt = m, p.now()
	p.mu.Unlock()
	return m, nil
}

func (p *provider) freshMetadata() *metadata {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.meta != nil && p.now().Sub(p.metaAt) < metadataTTL {
		return p.meta
	}
	return nil
}

func (p *provider) fetchMetadata(ctx context.Context) (*metadata, error) {
	var m metadata
	if err := p.getJSON(ctx, strings.TrimSuffix(p.issuer, "/")+"/.well-known/openid-configuration", &m); err != nil {
		return nil, err
	}
	if m.Issuer != p.issuer {
		return nil, fmt.Errorf("oidc: the discovery document names issuer %q, not the configured %q", m.Issuer, p.issuer)
	}
	if m.AuthorizationEndpoint == "" || m.TokenEndpoint == "" || m.JWKSURI == "" {
		return nil, errors.New("oidc: the discovery document lacks an authorization, token or jwks endpoint")
	}
	return &m, nil
}

// cachedMetadata never touches the network.
func (p *provider) cachedMetadata() *metadata {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.meta
}

type jwk struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Kid string `json:"kid"`
	Use string `json:"use"`
}

func (p *provider) key(ctx context.Context, kid string) (ed25519.PublicKey, error) {
	if k, ok := p.cachedKey(kid); ok {
		return k, nil
	}
	// keysMu keeps one JWKS fetch in flight; lastKeyTry is set before the
	// fetch and counts failures too, so neither concurrent unknown kids nor a
	// failing uni-auth get more than one fetch a minute (E-74 §2.6).
	p.keysMu.Lock()
	defer p.keysMu.Unlock()
	if k, ok := p.cachedKey(kid); ok {
		return k, nil
	}
	now := p.now()
	if !p.lastKeyTry.IsZero() && now.Sub(p.lastKeyTry) < jwksRefetchGap {
		if p.keysErr != nil {
			return nil, p.keysErr
		}
		return nil, fmt.Errorf("oidc: unknown key id %q", kid)
	}
	p.lastKeyTry = now
	keys, err := p.fetchKeys(ctx)
	p.keysErr = err
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.keys, p.keysAt, p.keysFetched = keys, p.now(), true
	p.mu.Unlock()
	if k, ok := keys[kid]; ok {
		return k, nil
	}
	return nil, fmt.Errorf("oidc: unknown key id %q", kid)
}

func (p *provider) cachedKey(kid string) (ed25519.PublicKey, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	k, ok := p.keys[kid]
	return k, ok && p.keysFetched && p.now().Sub(p.keysAt) < jwksTTL
}

func (p *provider) fetchKeys(ctx context.Context) (map[string]ed25519.PublicKey, error) {
	m, err := p.metadata(ctx)
	if err != nil {
		return nil, err
	}
	var set struct {
		Keys []jwk `json:"keys"`
	}
	if err := p.getJSON(ctx, m.JWKSURI, &set); err != nil {
		return nil, err
	}
	keys := map[string]ed25519.PublicKey{}
	for _, j := range set.Keys {
		if j.Kty != "OKP" || j.Crv != "Ed25519" || j.Kid == "" || (j.Use != "" && j.Use != "sig") {
			continue
		}
		raw, err := base64.RawURLEncoding.DecodeString(j.X)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			continue
		}
		keys[j.Kid] = ed25519.PublicKey(raw)
	}
	return keys, nil
}
