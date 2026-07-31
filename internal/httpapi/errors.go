package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
)

// The frozen ErrorCode enum of arch §7.2. Nothing outside this list may appear
// in an `error.code`.
const (
	CodeBadRequest   = "bad_request"  // 400 malformed input
	CodeUnauthorized = "unauthorized" // 401 auth enabled, session missing or expired
	// CodeForbidden is the refusal of a request that is understood, well-formed
	// and authenticated, and that this server's *configuration* declines to
	// perform. AMENDMENT A-11, ruling E-26: arch §7.4's root-editing endpoints
	// are gated by `server.allow_root_editing`, and §7.2 had no name for the 403
	// that ruling mandates — the same defect A-9 fixed for the login limiter's
	// 429. `unauthorized` was the near miss and would have been actively
	// harmful: `web/src/api/errors.ts`'s isAuthError keys ruling E-17's re-auth
	// path off it, so a correctly authenticated user — or a user of the
	// password-less default server (ruling E-8) — would be shown a login screen
	// for a refusal no login can lift.
	CodeForbidden        = "forbidden"         // 403 understood, and refused by configuration
	CodeNotFound         = "not_found"         // 404 unknown id, or page out of range
	CodeConflict         = "conflict"          // 409 e.g. a scan is already running
	CodeStaleVersion     = "stale_version"     // 409 ?v= no longer matches the book's cv
	CodeUnprocessable    = "unprocessable"     // 422 understood but cannot be produced
	CodeThumbUnavailable = "thumb_unavailable" // 422 the source cannot be decoded (§5.5)
	// CodeRateLimited is the login limiter's 429 (arch §8.2). AMENDMENT A-9,
	// ruling E-13: §7.2 mandated the behaviour but had no name for the answer,
	// so `web/src/api/errors.ts` carried a status-only fallback. It is now part
	// of the enum, and the fallback is the contract.
	CodeRateLimited = "rate_limited" // 429 too many login attempts (§8.2)
	CodeUnsupported = "unsupported"  // 501 absent from this build (e.g. nopdf)
	CodeUnavailable = "unavailable"  // 503 media volume unreachable / shutting down
	CodeInternal    = "internal"     // 500
)

// statusForCode is the code→status mapping of arch §7.2. It is the default,
// not a law: a handler may pair a code with a different status where the
// contract says so (405 carries `bad_request`), and `apiError.status` always
// wins.
func statusForCode(code string) int {
	switch code {
	case CodeBadRequest:
		return http.StatusBadRequest
	case CodeUnauthorized:
		return http.StatusUnauthorized
	case CodeForbidden:
		return http.StatusForbidden
	case CodeNotFound:
		return http.StatusNotFound
	case CodeConflict, CodeStaleVersion:
		return http.StatusConflict
	case CodeUnprocessable, CodeThumbUnavailable:
		return http.StatusUnprocessableEntity
	case CodeUnsupported:
		return http.StatusNotImplemented
	case CodeUnavailable:
		return http.StatusServiceUnavailable
	case CodeRateLimited:
		return http.StatusTooManyRequests
	default:
		return http.StatusInternalServerError
	}
}

// apiError is a failure on its way to the §7.2 envelope. Handlers return it;
// one place — writeError — turns it into a response, so the envelope cannot be
// written two different ways.
type apiError struct {
	status int
	code   string
	msg    string
	detail map[string]any
	// header carries extras the status requires: `Allow` on a 405,
	// `Retry-After` on a 429.
	header http.Header
	// cause is the underlying error, logged but never sent: arch §8.4 forbids
	// leaking absolute filesystem paths to the client.
	cause error
}

func (e *apiError) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.code, e.msg, e.cause)
	}
	return e.code + ": " + e.msg
}

func (e *apiError) Unwrap() error { return e.cause }

// errf builds an apiError with the status the code maps to.
func errf(code, format string, args ...any) *apiError {
	return &apiError{status: statusForCode(code), code: code, msg: fmt.Sprintf(format, args...)}
}

// withDetail attaches the machine-readable extras of arch §7.2.
func (e *apiError) withDetail(kv ...any) *apiError {
	if len(kv)%2 != 0 {
		panic("httpapi: withDetail needs key/value pairs")
	}
	if e.detail == nil {
		e.detail = make(map[string]any, len(kv)/2)
	}
	for i := 0; i < len(kv); i += 2 {
		key, ok := kv[i].(string)
		if !ok {
			panic("httpapi: withDetail keys must be strings")
		}
		e.detail[key] = kv[i+1]
	}
	return e
}

// wrap records the underlying cause for the log without putting it on the wire.
func (e *apiError) wrap(err error) *apiError {
	e.cause = err
	return e
}

// setHeader adds a response header the status requires.
func (e *apiError) setHeader(k, v string) *apiError {
	if e.header == nil {
		e.header = make(http.Header, 1)
	}
	e.header.Set(k, v)
	return e
}

// Common constructors, so the message wording of a given failure is written
// once and every endpoint reports it identically.

func badRequest(format string, args ...any) *apiError {
	return errf(CodeBadRequest, format, args...)
}

// badParam is the shape arch §7.5 mandates for a rejected query parameter:
// `400` with `detail: {param: "<name>"}`.
func badParam(param, format string, args ...any) *apiError {
	return errf(CodeBadRequest, format, args...).withDetail("param", param)
}

func notFound(format string, args ...any) *apiError {
	return errf(CodeNotFound, format, args...)
}

func conflict(format string, args ...any) *apiError {
	return errf(CodeConflict, format, args...)
}

// staleVersion is the `409` of arch §5.3: the `?v=` the client holds is no
// longer the book's content version, and `detail.cv` tells it the current one
// so it can refetch metadata instead of caching a superseded page for a year.
func staleVersion(current string) *apiError {
	return errf(CodeStaleVersion, "the requested version is no longer current").
		withDetail("cv", current)
}

func unauthorized() *apiError {
	return errf(CodeUnauthorized, "authentication required")
}

// forbidden is the `403` of arch §7.4's root-editing gate (amendment A-11).
//
// `detail.reason` is not decoration: the gate folds three conditions into one
// capability — the key is off, this server has no configuration file, or that
// file lives inside a media root — and each one has a different remedy, so the
// UI cannot give the right instruction from the status alone.
func forbidden(reason, format string, args ...any) *apiError {
	return errf(CodeForbidden, format, args...).withDetail("reason", reason)
}

func unsupported(format string, args ...any) *apiError {
	return errf(CodeUnsupported, format, args...)
}

func unavailable(format string, args ...any) *apiError {
	return errf(CodeUnavailable, format, args...)
}

func internalErr(err error) *apiError {
	return errf(CodeInternal, "internal server error").wrap(err)
}

// methodNotAllowed is the 405 of impl-plan §3 WP-12 acceptance 1. §7.2's enum
// has no 405 code, so it carries `bad_request` — the request is malformed, in
// the sense that this verb does not exist here — with the permitted verbs in
// both the `Allow` header (required by RFC 9110) and `detail.allow`.
func methodNotAllowed(allow []string) *apiError {
	list := strings.Join(allow, ", ")
	return (&apiError{
		status: http.StatusMethodNotAllowed,
		code:   CodeBadRequest,
		msg:    "method not allowed",
	}).withDetail("allow", list).setHeader("Allow", list)
}

// writeError renders an error as the §7.2 envelope. Anything that is not an
// *apiError is a bug in this package and becomes a 500 without detail — the
// client learns nothing about the internals, the log learns everything.
func writeError(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) {
	var ae *apiError
	if !errors.As(err, &ae) {
		ae = internalErr(err)
	}
	for k, vs := range ae.header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	if ae.status >= http.StatusInternalServerError && log != nil {
		log.ErrorContext(r.Context(), "request failed",
			"code", ae.code, "status", ae.status, "err", ae.Error())
	}
	writeJSON(w, ae.status, ErrorResponse{Error: ErrorBody{
		Code:    ae.code,
		Message: ae.msg,
		Detail:  ae.detail,
	}})
}

// writeJSON writes a JSON body with the contract's content type.
//
// The body is marshalled to memory first so that a marshalling failure cannot
// produce a 200 with a truncated body — by the time the first byte is written
// the whole answer exists.
func writeJSON(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		// Every DTO in this package is marshallable; reaching here is a
		// programming error, and a 500 with a hand-written body is the only
		// honest answer left.
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"code":"internal","message":"response could not be encoded"}}`))
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// noContent is the `204` of DELETE /api/books/{bid}/progress and the auth
// endpoints.
func noContent(w http.ResponseWriter) { w.WriteHeader(http.StatusNoContent) }
