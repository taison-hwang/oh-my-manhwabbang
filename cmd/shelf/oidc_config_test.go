package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"shelf/internal/config"
)

const oidcGood = `auth:
  mode: oidc
  oidc:
    issuer: "https://auth.example.test"
    client_id: "zip-viewer"
    audience: "https://library.example.test/api"
    redirect_url: "https://library.example.test/api/auth/oidc/callback"
    allowed_subs: ["sub-a", "sub-b"]
    admin_subs: ["sub-a"]
`

func oidcConfigFile(t *testing.T, server, auth string) string {
	t.Helper()
	dir := t.TempDir()
	media := filepath.Join(dir, "media")
	if err := os.MkdirAll(media, 0o755); err != nil {
		t.Fatal(err)
	}
	body := server + "roots:\n  - {name: \"m\", path: " + media + "}\nstorage: {data_dir: " +
		filepath.Join(dir, "data") + ", cache_dir: " + filepath.Join(dir, "cache") + "}\n" + auth
	path := filepath.Join(dir, "shelf.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// Ruling E-74 §2.19: every startup failure condition of `auth.mode: oidc`
// exits 2 before anything is opened.
func TestRun_oidcConfigFailuresExitTwo(t *testing.T) {
	t.Parallel()
	edit := func(old, new string) string {
		if !strings.Contains(oidcGood, old) {
			t.Fatalf("fixture has no %q", old)
		}
		return strings.Replace(oidcGood, old, new, 1)
	}
	cases := map[string]struct{ server, auth string }{
		"issuer empty":         {"", edit(`issuer: "https://auth.example.test"`, `issuer: ""`)},
		"client_id missing":    {"", edit(`    client_id: "zip-viewer"`+"\n", "")},
		"audience missing":     {"", edit(`    audience: "https://library.example.test/api"`+"\n", "")},
		"redirect_url missing": {"", edit(`    redirect_url: "https://library.example.test/api/auth/oidc/callback"`+"\n", "")},
		"issuer over http":     {"", edit(`https://auth.example.test`, `http://auth.example.test`)},
		"redirect over http":   {"", edit(`https://library.example.test/api/auth`, `http://library.example.test/api/auth`)},
		"post_logout over http": {"", edit("    allowed_subs:",
			`    post_logout_redirect_url: "http://library.example.test/"`+"\n    allowed_subs:")},
		"redirect path":                    {"", edit(`/api/auth/oidc/callback`, `/callback`)},
		"redirect path without base":       {"server:\n  base_path: \"/shelf\"\n", oidcGood},
		"password together":                {"", oidcGood + "  password: \"hunter2\"\n"},
		"password_hash together":           {"", oidcGood + "  password_hash: \"$2a$12$abcdefghijklmnopqrstuv\"\n"},
		"admin outside allowed":            {"", edit(`admin_subs: ["sub-a"]`, `admin_subs: ["sub-z"]`)},
		"legacy owner outside allowed":     {"", oidcGood + "    legacy_owner_sub: \"sub-z\"\n"},
		"session_ttl over 24h":             {"", oidcGood + "  session_ttl: \"25h\"\n"},
		"audience equals client_id":        {"", edit(`audience: "https://library.example.test/api"`, `audience: "zip-viewer"`)},
		"unknown mode":                     {"", edit("mode: oidc", "mode: saml")},
		"redirect with a query":            {"", edit(`/api/auth/oidc/callback"`, `/api/auth/oidc/callback?x=1"`)},
		"http on a non-loopback lookalike": {"", edit(`https://auth.example.test`, `http://127.0.0.1.example.test`)},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var out, errOut bytes.Buffer
			code := run([]string{"--config", oidcConfigFile(t, tc.server, tc.auth)}, strings.NewReader(""), &out, &errOut)
			if code != config.ExitCode {
				t.Fatalf("exit = %d, want %d\n%s", code, config.ExitCode, errOut.String())
			}
			if !strings.Contains(errOut.String(), "auth.") {
				t.Fatalf("the failure does not name an auth key:\n%s", errOut.String())
			}
		})
	}
}

func TestOIDCConfig_goodParsesWithTheOIDCDefaults(t *testing.T) {
	t.Parallel()
	for name, auth := range map[string]string{
		"https":         oidcGood,
		"loopback http": strings.ReplaceAll(strings.ReplaceAll(oidcGood, "https://auth.example.test", "http://127.0.0.1:9000"), "https://library.example.test/api/auth", "http://localhost:8790/api/auth"),
	} {
		t.Run(name, func(t *testing.T) {
			path := oidcConfigFile(t, "", auth)
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			cfg, err := config.Parse(b, path)
			if err != nil {
				t.Fatalf("a good oidc config is refused: %v", err)
			}
			if !cfg.Auth.IsOIDC() || cfg.Auth.SessionTTL.Hours() != 12 {
				t.Fatalf("mode %q ttl %s, want oidc and 12h", cfg.Auth.Mode, cfg.Auth.SessionTTL)
			}
		})
	}
}
