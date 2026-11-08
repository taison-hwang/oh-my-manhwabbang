package oidc

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestService(t *testing.T, now *time.Time) *Service {
	t.Helper()
	s, err := New(context.Background(), Config{
		Issuer: "https://auth.example.test", ClientID: "zip-viewer", Audience: "https://library.example.test/api",
		RedirectURL: "https://library.example.test/api/auth/oidc/callback",
		AllowedSubs: []string{"sub-a"}, SessionTTL: 12 * time.Hour,
		DBPath: filepath.Join(t.TempDir(), "data", "auth.db"),
		Now:    func() time.Time { return *now },
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestStore_authDBIsPrivate(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	s := newTestService(t, &now)
	defer func() { _ = s.Close() }()
	info, err := os.Stat(s.cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("auth.db mode = %o, want 600", info.Mode().Perm())
	}
}

func TestStore_sweepKeepsWhatPromptNoneNeeds(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	s := newTestService(t, &now)
	defer func() { _ = s.Close() }()
	ctx := context.Background()
	rows := map[string]sessionRow{
		"expired-yesterday": {expiresAt: now.Add(-24 * time.Hour)},
		"expired-31-days":   {expiresAt: now.Add(-31 * 24 * time.Hour)},
		"live":              {expiresAt: now.Add(time.Hour)},
	}
	for id, r := range rows {
		r.idHash, r.sub, r.createdAt, r.lastSeenAt = id, "sub-a", now, now
		if err := s.store.insert(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.store.markLoggedOut(ctx, "live", now.Add(-11*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.store.sweep(ctx, now); err != nil {
		t.Fatal(err)
	}
	for id, keep := range map[string]bool{"expired-yesterday": true, "expired-31-days": false, "live": false} {
		if _, ok, err := s.store.get(ctx, id); err != nil || ok != keep {
			t.Errorf("%s kept = %t (err %v), want %t", id, ok, err, keep)
		}
	}
}

func TestPending_globalCap(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	p := newPendingStore(func() time.Time { return now })
	for range pendingMax {
		p.put(randomToken(), &pending{})
	}
	if _, ok := p.admit("10.0.0.9"); ok {
		t.Fatal("a start was admitted with the pre-auth store full")
	}
	now = now.Add(pendingTTL + time.Second)
	if _, ok := p.admit("10.0.0.9"); !ok {
		t.Fatal("expired entries were not swept")
	}
}

func TestClose_closesAuthDB(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	s := newTestService(t, &now)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.store.db.Ping(); err == nil {
		t.Fatal("auth.db is still open after Close")
	}
}

func TestOrigin_isWhatABrowserSends(t *testing.T) {
	for raw, want := range map[string]string{
		"https://Library.Example.Test/api/auth/oidc/callback":     "https://library.example.test",
		"https://library.example.test:443/api/auth/oidc/callback": "https://library.example.test",
		"http://127.0.0.1:80/api/auth/oidc/callback":              "http://127.0.0.1",
		"http://127.0.0.1:8790/api/auth/oidc/callback":            "http://127.0.0.1:8790",
		"http://[::1]:8790/api/auth/oidc/callback":                "http://[::1]:8790",
	} {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if got := origin(u); got != want {
			t.Errorf("origin(%s) = %q, want %q", raw, got, want)
		}
	}
}
