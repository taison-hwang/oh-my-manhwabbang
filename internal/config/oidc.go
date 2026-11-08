package config

import (
	"net"
	"net/url"
	"slices"
	"strings"
)

// validateOIDC applies ruling E-74 §2.19's startup rules to `auth:` with
// `mode: oidc`. Each failure is an *Error, so the caller exits 2.
func (c *Config) validateOIDC(add func(*Error)) {
	s, a := c.src, c.Auth
	o := a.OIDC
	if a.Password != "" || a.PasswordHash != "" {
		add(s.errf("auth.mode", "auth.password and auth.password_hash are not read in oidc mode; remove them, or use mode: password"))
	}
	for _, f := range []struct{ key, value string }{
		{"auth.oidc.issuer", o.Issuer},
		{"auth.oidc.client_id", o.ClientID},
		{"auth.oidc.audience", o.Audience},
		{"auth.oidc.redirect_url", o.RedirectURL},
	} {
		if strings.TrimSpace(f.value) == "" {
			add(s.errf(f.key, "is required in oidc mode"))
		}
	}
	checkURL := func(key, raw string) *url.URL {
		if raw == "" {
			return nil
		}
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" {
			add(s.errValue(key, raw, "must be an absolute URL with no user, query or fragment"))
			return nil
		}
		if u.Scheme != "https" && (u.Scheme != "http" || !IsLoopbackHost(u.Hostname())) {
			add(s.errValue(key, raw, "must be https (http is accepted only for a loopback host)"))
			return nil
		}
		return u
	}
	if o.Audience != "" && o.Audience == o.ClientID {
		// The ID token's aud is client_id; with the two equal it would also
		// pass as the access token, and the cross-check would prove nothing.
		add(s.errValue("auth.oidc.audience", o.Audience, "must differ from auth.oidc.client_id"))
	}
	checkURL("auth.oidc.issuer", o.Issuer)
	checkURL("auth.oidc.post_logout_redirect_url", o.PostLogoutRedirectURL)
	if u := checkURL("auth.oidc.redirect_url", o.RedirectURL); u != nil {
		base, err := normalizeBasePath(c.Server.BasePath)
		if want := base + "/api/auth/oidc/callback"; err == nil && u.Path != want {
			add(s.errValue("auth.oidc.redirect_url", o.RedirectURL, "the path must be %s", want))
		}
	}
	for i, sub := range o.AllowedSubs {
		if strings.TrimSpace(sub) == "" {
			add(s.errf("auth.oidc.allowed_subs", "entry %d is empty", i))
		}
	}
	for _, sub := range o.AdminSubs {
		if !slices.Contains(o.AllowedSubs, sub) {
			add(s.errValue("auth.oidc.admin_subs", sub, "is not in auth.oidc.allowed_subs; an admin must also be allowed in"))
		}
	}
	if o.LegacyOwnerSub != "" && !slices.Contains(o.AllowedSubs, o.LegacyOwnerSub) {
		add(s.errValue("auth.oidc.legacy_owner_sub", o.LegacyOwnerSub, "is not in auth.oidc.allowed_subs"))
	}
	if a.SessionTTL <= 0 || a.SessionTTL > maxOIDCSessionTTL {
		add(s.errValue("auth.session_ttl", a.SessionTTL, "must be a positive duration of at most %s in oidc mode", maxOIDCSessionTTL))
	}
}

// IsLoopbackHost reports whether host names this machine only: localhost or
// an address in 127.0.0.0/8 or ::1.
func IsLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}
