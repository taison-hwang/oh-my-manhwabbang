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

	mu          sync.Mutex
	meta        *metadata
	metaAt      time.Time
	keys        map[string]ed25519.PublicKey
	keysAt      time.Time
	lastKeyTry  time.Time
	keysFetched bool
}

func newProvider(issuer string, client Doer, now func() time.Time) *provider {
	return &provider{issuer: issuer, client: client, now: now}
}

func (p *provider) getJSON(ctx context.Context, url string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", errProviderUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: GET %s answered %d", errProviderUnavailable, url, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxProviderBody))
	if err != nil {
		return fmt.Errorf("%w: %v", errProviderUnavailable, err)
	}
	if err := json.Unmarshal(body, into); err != nil {
		return fmt.Errorf("oidc: decoding %s: %w", url, err)
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
	p.mu.Lock()
	fresh := p.keysFetched && p.now().Sub(p.keysAt) < jwksTTL
	k, ok := p.keys[kid]
	canRetry := p.now().Sub(p.lastKeyTry) >= jwksRefetchGap
	p.mu.Unlock()
	if ok && fresh {
		return k, nil
	}
	if fresh && !canRetry {
		return nil, fmt.Errorf("oidc: unknown key id %q", kid)
	}

	m, err := p.metadata(ctx)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.lastKeyTry = p.now()
	p.mu.Unlock()
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
	p.mu.Lock()
	p.keys, p.keysAt, p.keysFetched = keys, p.now(), true
	p.mu.Unlock()
	if k, ok := keys[kid]; ok {
		return k, nil
	}
	return nil, fmt.Errorf("oidc: unknown key id %q", kid)
}
