package oidc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The reproductions below come from the U-B independent verification
// (UB-1, UB-2, UB-3, UB-5, 2026-11-09).

// E-74 §2.5: starts that all pass admit while discovery is in flight must
// not push the store past its cap.
func TestPending_concurrentStartsKeepTheCap(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	s := newTestService(t, &now)
	defer func() { _ = s.Close() }()
	for i := range pendingMax - 1 {
		s.pending.put(fmt.Sprint(i), &pending{})
	}
	s.prov.fetchMu.Lock()
	var wg sync.WaitGroup
	var accepted, limited atomic.Int32
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := httptest.NewRequest(http.MethodGet, "https://library.example.test/api/auth/oidc/start", nil)
			_, err := s.Start(r, "127.0.0.1", "/", false)
			var rl *RateLimitError
			switch {
			case err == nil:
				accepted.Add(1)
			case errors.As(err, &rl):
				limited.Add(1)
			}
		}()
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		s.pending.mu.Lock()
		b := s.pending.buckets["127.0.0.1"]
		ready := b != nil && b.tokens < 1
		s.pending.mu.Unlock()
		if ready {
			break
		}
		if time.Now().After(deadline) {
			s.prov.fetchMu.Unlock()
			t.Fatal("the starts did not reach discovery")
		}
		time.Sleep(time.Millisecond)
	}
	s.prov.mu.Lock()
	s.prov.meta = &metadata{AuthorizationEndpoint: "https://auth.example.test/authorize"}
	s.prov.metaAt = now
	s.prov.mu.Unlock()
	s.prov.fetchMu.Unlock()
	wg.Wait()
	if got := len(s.pending.entries); got > pendingMax {
		t.Fatalf("accepted %d, pending %d exceeds the cap %d", accepted.Load(), got, pendingMax)
	}
	if accepted.Load() != 1 || limited.Load() != 19 {
		t.Fatalf("accepted %d, rate-limited %d; want 1 and 19", accepted.Load(), limited.Load())
	}
}

// E-74 §2.5: expired entries go without a new start.
func TestPending_sweptWithoutStarts(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	s := newTestService(t, &now)
	defer func() { _ = s.Close() }()
	s.pending.put("old", &pending{})
	now = now.Add(pendingTTL + time.Second)
	s.sweepGrace()
	if n := len(s.pending.entries); n != 0 {
		t.Fatalf("%d expired entries left after the sweep", n)
	}
}

// E-74 §2.6: unknown kids arriving together share one JWKS fetch.
func TestProvider_concurrentUnknownKidsFetchOnce(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	var requests atomic.Int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		<-release
		_, _ = io.WriteString(w, `{"keys":[]}`)
	}))
	defer srv.Close()
	p := newProvider("https://auth.example.test", srv.Client(), func() time.Time { return now })
	p.meta = &metadata{JWKSURI: srv.URL}
	p.keysFetched = true
	p.keysAt = now.Add(-2 * time.Minute)
	p.lastKeyTry = now.Add(-2 * time.Minute)
	// A stale discovery document parks every request at the discovery lock,
	// after the point where the old code had already decided to refetch.
	p.metaAt = now.Add(-2 * time.Hour)
	p.fetchMu.Lock()
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = p.key(context.Background(), "unknown") }()
	}
	time.Sleep(100 * time.Millisecond)
	p.mu.Lock()
	p.metaAt = now
	p.mu.Unlock()
	p.fetchMu.Unlock()
	time.Sleep(200 * time.Millisecond)
	close(release)
	wg.Wait()
	if got := requests.Load(); got != 1 {
		t.Fatalf("JWKS fetched %d times in one minute, want 1", got)
	}
}

// E-74 §2.6: a failing first JWKS fetch is limited too.
func TestProvider_failedJWKSFetchIsLimited(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	p := newProvider("https://auth.example.test", srv.Client(), func() time.Time { return now })
	p.meta = &metadata{JWKSURI: srv.URL}
	p.metaAt = now
	for range 3 {
		if _, err := p.key(context.Background(), "unknown"); !errors.Is(err, errProviderUnavailable) {
			t.Fatalf("key error = %v, want the provider failure", err)
		}
	}
	if n.Load() != 1 {
		t.Fatalf("a failing JWKS was fetched %d times in one minute", n.Load())
	}
	now = now.Add(jwksRefetchGap)
	_, _ = p.key(context.Background(), "unknown")
	if n.Load() != 2 {
		t.Fatalf("no retry after a minute (%d fetches)", n.Load())
	}
}

// E-74 §2.5: return is judged on the path the router will see.
func TestSafeReturn_decodedAndCleaned(t *testing.T) {
	s := &Service{}
	for _, ret := range []string{"/%61pi/settings", "/api%2fsettings", "/x/../api/settings", "/x/%2e%2e/api/settings",
		"/%0d%0aLocation:evil", "/%5cevil.test", "/%2fevil.test", "/api", "/./api/settings",
		"/%2561pi/settings", "/x/%252e%252e/api/settings"} {
		if got := s.SafeReturn(ret); got != "/" {
			t.Errorf("SafeReturn(%q) = %q, want /", ret, got)
		}
	}
	for _, ret := range []string{"/", "/series/abc", "/books/1?page=2", "/%ED%95%9C/", "/settings"} {
		if got := s.SafeReturn(ret); got != ret {
			t.Errorf("SafeReturn(%q) = %q, want it kept", ret, got)
		}
	}
	s.cfg.BasePath = "/shelf"
	for _, ret := range []string{"/shelf/../api/settings", "/shelf/%2e%2e/x", "/shelf/api/series", "/shelf/%61pi/x", "/other/"} {
		if got := s.SafeReturn(ret); got != "/shelf/" {
			t.Errorf("base /shelf: SafeReturn(%q) = %q, want /shelf/", ret, got)
		}
	}
	if got := s.SafeReturn("/shelf/books/1"); got != "/shelf/books/1" {
		t.Errorf("base /shelf: a book path became %q", got)
	}
}

// E-74 §2.12: a logged-out row and its id_token go after ten minutes, by
// the next request with that cookie or by the minute sweep.
func TestLogout_rowGoesAfterGrace(t *testing.T) {
	for _, via := range []string{"request", "sweep"} {
		t.Run(via, func(t *testing.T) {
			now := time.Unix(1_800_000_000, 0)
			s := newTestService(t, &now)
			defer func() { _ = s.Close() }()
			id := "opaque-cookie"
			row := sessionRow{idHash: hashID(id), sub: "sub-a", idToken: "retained-id-token", createdAt: now, lastSeenAt: now, expiresAt: now.Add(12 * time.Hour)}
			if err := s.store.insert(context.Background(), row); err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, "https://library.example.test/api/auth/logout", nil)
			req.AddCookie(&http.Cookie{Name: s.sidName, Value: id})
			if _, err := s.Logout(req); err != nil {
				t.Fatal(err)
			}
			now = now.Add(9 * time.Minute)
			s.sweepGrace()
			if _, ok, _ := s.store.get(context.Background(), hashID(id)); !ok {
				t.Fatal("the row went before its ten minutes")
			}
			now = now.Add(2 * time.Minute)
			if via == "request" {
				if _, st, err := s.Authenticate(req); err != nil || st != StatusNone {
					t.Fatalf("logged-out row: status %v, err %v", st, err)
				}
			} else {
				s.sweepGrace()
			}
			if got, ok, err := s.store.get(context.Background(), hashID(id)); err != nil || ok {
				t.Fatalf("eleven minutes after logout the row remains (id_token %q, err %v)", got.idToken, err)
			}
		})
	}
}

// A login arriving while the first JWKS fetch is in flight waits for it
// instead of failing on the minute limit that fetch just took.
func TestProvider_secondLoginWaitsForTheFetch(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	arrived, release := make(chan struct{}, 4), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		arrived <- struct{}{}
		<-release
		_, _ = io.WriteString(w, `{"keys":[{"kid":"k1","kty":"OKP","crv":"Ed25519","x":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}]}`)
	}))
	defer srv.Close()
	p := newProvider("https://auth.example.test", srv.Client(), func() time.Time { return now })
	p.meta = &metadata{JWKSURI: srv.URL}
	p.metaAt = now
	errs := make(chan error, 2)
	get := func() { _, err := p.key(context.Background(), "k1"); errs <- err }
	go get()
	<-arrived
	go get()
	time.Sleep(100 * time.Millisecond)
	close(release)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("a login during the first JWKS fetch failed: %v", err)
		}
	}
}
