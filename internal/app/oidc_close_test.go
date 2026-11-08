package app

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"shelf/internal/config"
)

// openFDsTo counts this process's descriptors open on path (Linux /proc).
func openFDsTo(t *testing.T, path string) int {
	t.Helper()
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skipf("no /proc/self/fd: %v", err)
	}
	n := 0
	for _, e := range ents {
		if target, err := os.Readlink(filepath.Join("/proc/self/fd", e.Name())); err == nil && target == path {
			n++
		}
	}
	return n
}

// App.Close closes auth.db in oidc mode (ruling E-74; the session store is
// opened by httpapi.New and released through Server.Close).
func TestClose_releasesAuthDB(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reads /proc/self/fd")
	}
	cfg := testConfig(t, t.TempDir())
	cfg.Auth = &config.Auth{Mode: config.AuthModeOIDC, SessionTTL: 12 * time.Hour, OIDC: config.OIDC{
		Issuer: "http://127.0.0.1:1", ClientID: "zip-viewer", Audience: "https://library.test/api",
		RedirectURL: "http://127.0.0.1:8790/api/auth/oidc/callback", AllowedSubs: []string{"sub-a"},
	}}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(t.Context(), Options{Config: cfg, Logger: quietLogger(), Listener: ln, PerUser: true})
	if err != nil {
		_ = ln.Close()
		t.Fatalf("app.New in oidc mode: %v", err)
	}
	authDB, err := filepath.Abs(filepath.Join(cfg.Storage.DataDir, "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	if openFDsTo(t, authDB) == 0 {
		_ = a.Close()
		t.Fatalf("auth.db %s is not open after New", authDB)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if n := openFDsTo(t, authDB); n != 0 {
		t.Fatalf("auth.db still has %d open descriptors after Close", n)
	}
}
