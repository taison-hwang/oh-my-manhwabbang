package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"shelf/internal/auth"
	"shelf/internal/auth/oidc"
	"shelf/internal/principal"
)

// handleAuthStatus is `GET /api/auth/status` (arch §7.12).
//
// It never returns 401 — that is the point. The SPA calls it first and renders
// the login screen when `auth_required && !authenticated`; an endpoint that
// required a session to report whether a session is required would be a
// bootstrap problem with no solution.
func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) error {
	if s.oidc != nil {
		st := AuthStatus{AuthRequired: true, Mode: "oidc", EndSessionAvailable: s.oidc.EndSessionAvailable(r.Context())}
		if p, ok := principal.From(r.Context()); ok {
			st.Authenticated = true
			st.User = &AuthUser{Sub: p.Sub, IsAdmin: p.Admin}
		}
		writeJSON(w, http.StatusOK, st)
		return nil
	}
	mode := "none"
	if s.auth.Enabled() {
		mode = "password"
	}
	writeJSON(w, http.StatusOK, AuthStatus{
		AuthRequired:  s.auth.Enabled(),
		Authenticated: s.auth.Enabled() && s.auth.Authenticated(r),
		Mode:          mode,
	})
	return nil
}

// handleLogin is `POST /api/auth/login` → `204` + `Set-Cookie`, or `401`.
//
// The password is compared with bcrypt (constant-time by construction), every
// failure is padded to ≥250 ms, and each client address gets 5 attempts per
// minute before the answer becomes `429` with `Retry-After` — all of it in
// internal/auth, so this handler is the mapping and nothing else (arch §8.2).
//
// # Two request encodings, one contract
//
// arch §7.12 freezes the JSON form: `body {password: string} -> 204 +
// Set-Cookie | 401 unauthorized`. That path is untouched, byte for byte.
//
// The handler additionally accepts `application/x-www-form-urlencoded`, which
// is what the server-rendered login page of loginpage.go posts. It is an
// additional accepted media type, not a change to the frozen one: a client that
// sends JSON cannot tell the difference. It exists because §8.2's all-or-nothing
// gate means the SPA bundle is not served to an unauthenticated visitor, and
// §8.4's CSP (`default-src 'self'`, no `script-src`) forbids the inline script a
// JSON-posting form would need — so a plain HTML form is the only login
// mechanism the product's own rules leave available. A form submission answers
// in the browser's language: `303` to `{base}/` on success (a GET, so a refresh
// cannot repost the password) and the form again, with the failure's status, on
// anything else.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) error {
	if s.oidc != nil {
		return badRequest("this server signs in through uni-auth").withDetail("reason", "oidc_mode")
	}
	if !s.auth.Enabled() {
		// Nothing to log in to. 404 rather than 204: pretending to
		// authenticate would leave the client believing it holds a session.
		return notFound("authentication is not enabled")
	}
	form := isFormLogin(r)
	password, err := s.loginPassword(w, r, form)
	if err != nil {
		return err
	}

	token, expires, err := s.auth.Login(r, []byte(password))
	if err != nil {
		ae := loginFailure(err)
		if !form {
			return ae
		}
		for k, vs := range ae.header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		s.writeLoginPage(w, r, ae.status, loginMessage(ae.status))
		return nil
	}

	http.SetCookie(w, s.auth.Cookie(r, token, expires))
	if form {
		http.Redirect(w, r, s.base+"/", http.StatusSeeOther)
		return nil
	}
	noContent(w)
	return nil
}

// isFormLogin reports whether the body is the login page's form submission
// rather than the frozen JSON body of arch §7.12.
func isFormLogin(r *http.Request) bool {
	media, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";")
	return strings.EqualFold(strings.TrimSpace(media), "application/x-www-form-urlencoded")
}

// loginPassword reads the secret out of whichever encoding arrived, under the
// same 1 MiB body cap either way (arch §8.4).
func (s *Server) loginPassword(w http.ResponseWriter, r *http.Request, form bool) (string, error) {
	if !form {
		var body loginBody
		if err := decodeJSON(w, r, maxJSONBody, &body); err != nil {
			return "", err
		}
		return body.Password, nil
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	if err := r.ParseForm(); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return "", badRequest("request body exceeds %d bytes", int64(maxJSONBody))
		}
		return "", badRequest("the login form could not be parsed")
	}
	// PostFormValue, not FormValue: a password in the query string would be in
	// every access log and every browser history entry, so it is not accepted
	// there even as a fallback.
	return r.PostFormValue("password"), nil
}

// loginFailure maps internal/auth's refusals onto the envelope. It is a
// function rather than inline so that the JSON and form paths cannot drift into
// answering the same failure with two different statuses.
func loginFailure(err error) *apiError {
	var rl *auth.RateLimitError
	switch {
	case errors.As(err, &rl):
		seconds := int(rl.RetryAfter.Seconds())
		if seconds < 1 {
			seconds = 1
		}
		return errf(CodeRateLimited, "too many login attempts").
			setHeader("Retry-After", strconv.Itoa(seconds)).
			withDetail("retry_after", seconds)
	case errors.Is(err, auth.ErrBadCredentials):
		// Deliberately identical whatever was wrong with the password. The
		// message is the only thing the client learns, and it must not
		// distinguish "no password sent" from "wrong password".
		return unauthorized()
	default:
		return internalErr(err)
	}
}

// handleLogout is `POST /api/auth/logout` → `204`.
//
// It is a POST, not a GET, and that is load-bearing: `SameSite=Lax` sends
// cookies on a cross-site *GET* navigation, so a `GET /logout` would be a
// working CSRF. With every mutation on POST/PUT/DELETE, v1 needs no CSRF token
// at all (decision D-23).
//
// Clearing an absent session is not an error: 204 is idempotent and "you are
// logged out" is true either way.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) error {
	if s.oidc != nil {
		cookies, err := s.oidc.Logout(r)
		if err != nil {
			return internalErr(err)
		}
		for _, c := range cookies {
			http.SetCookie(w, c)
		}
		if isFormLogin(r) {
			s.writeOIDCPage(w, r, http.StatusOK, oidcPage{Kind: pageLoggedOut, EndSession: s.oidc.EndSessionAvailable(r.Context())})
			return nil
		}
		noContent(w)
		return nil
	}
	http.SetCookie(w, s.auth.ClearCookie(r))
	noContent(w)
	return nil
}

// handleOIDCStart is `GET /api/auth/oidc/start?return=&reauth=1` (ruling E-74 §2.5).
func (s *Server) handleOIDCStart(w http.ResponseWriter, r *http.Request) error {
	if s.oidc == nil {
		return notFound("uni-auth login is not enabled")
	}
	q := r.URL.Query()
	ret := q.Get("return")
	red, err := s.oidc.Start(r, auth.ClientIP(r, s.trustProxy), ret, q.Get("reauth") == "1")
	var rl *oidc.RateLimitError
	switch {
	case errors.As(err, &rl):
		seconds := max(1, int(rl.RetryAfter.Seconds()))
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
		s.writeOIDCPage(w, r, http.StatusTooManyRequests, oidcPage{Kind: pageRateLimited, Return: ret})
		return nil
	case errors.Is(err, oidc.ErrNoSession):
		s.writeOIDCPage(w, r, http.StatusUnauthorized, oidcPage{Kind: pageLogin, Return: ret})
		return nil
	case oidc.IsProviderUnavailable(err):
		s.log.Warn("uni-auth unreachable", "err", err)
		s.writeOIDCPage(w, r, http.StatusServiceUnavailable, oidcPage{Kind: pageUnavailable, Return: ret})
		return nil
	case err != nil:
		s.log.Error("starting a uni-auth login", "err", err)
		s.writeOIDCPage(w, r, http.StatusBadGateway, oidcPage{Kind: pageLoginFailed, Return: ret})
		return nil
	}
	s.redirect(w, r, red)
	return nil
}

// handleOIDCCallback is `GET /api/auth/oidc/callback` (ruling E-74 §2.6).
func (s *Server) handleOIDCCallback(w http.ResponseWriter, r *http.Request) error {
	if s.oidc == nil {
		return notFound("uni-auth login is not enabled")
	}
	res := s.oidc.Callback(r)
	for _, c := range res.Redirect.Cookies {
		http.SetCookie(w, c)
	}
	if res.Err != nil {
		s.log.Warn("uni-auth login did not complete", "err", res.Err)
	}
	switch res.Outcome {
	case oidc.OutcomeRedirect:
		http.Redirect(w, r, res.Redirect.Location, http.StatusFound)
	case oidc.OutcomeNotAllowed:
		s.writeOIDCPage(w, r, http.StatusForbidden, oidcPage{Kind: pageNotAllowed, Sub: res.Sub})
	case oidc.OutcomeReauthFailed:
		s.writeOIDCPage(w, r, http.StatusForbidden, oidcPage{Kind: pageReauthFailed})
	case oidc.OutcomeUnavailable:
		s.writeOIDCPage(w, r, http.StatusServiceUnavailable, oidcPage{Kind: pageUnavailable})
	case oidc.OutcomeRetryLater:
		s.writeOIDCPage(w, r, http.StatusServiceUnavailable, oidcPage{Kind: pageRetryLater})
	case oidc.OutcomeLoginFailed:
		s.writeOIDCPage(w, r, http.StatusBadGateway, oidcPage{Kind: pageLoginFailed})
	default:
		s.writeOIDCPage(w, r, http.StatusBadRequest, oidcPage{Kind: pageRestart})
	}
	return nil
}

// handleOIDCEndSession is `POST /api/auth/oidc/end-session` (ruling E-74 §2.12).
func (s *Server) handleOIDCEndSession(w http.ResponseWriter, r *http.Request) error {
	if s.oidc == nil {
		return notFound("uni-auth login is not enabled")
	}
	red, err := s.oidc.EndSession(r)
	for _, c := range red.Cookies {
		http.SetCookie(w, c)
	}
	if err != nil {
		s.log.Warn("ending the uni-auth session", "err", err)
	}
	if red.Location != "" {
		http.Redirect(w, r, red.Location, http.StatusFound)
		return nil
	}
	s.writeOIDCPage(w, r, http.StatusOK, oidcPage{Kind: pageLoggedOut})
	return nil
}

func (s *Server) redirect(w http.ResponseWriter, r *http.Request, red oidc.Redirect) {
	for _, c := range red.Cookies {
		http.SetCookie(w, c)
	}
	http.Redirect(w, r, red.Location, http.StatusFound)
}
