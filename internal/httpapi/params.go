package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"shelf/internal/ids"
)

// Body size caps (arch §8.4). JSON bodies are tiny — the largest is a settings
// patch — so 1 MiB is already three orders of magnitude of headroom; the
// progress import is a whole reading history and gets 32 MiB.
const (
	maxJSONBody   = 1 << 20
	maxImportBody = 32 << 20
)

// pathID reads a `{sid}` or `{bid}` wildcard and validates it against
// `^[a-z2-7]{16}$`.
//
// arch §7.1 draws the line precisely: a *syntactically invalid* id is 400,
// because the client built a URL that cannot refer to anything; a well-formed
// id that matches no row is 404, because it might have existed yesterday. The
// distinction matters to the frontend's error handling and is asserted by test.
func pathID(r *http.Request, name string) (string, error) {
	v := r.PathValue(name)
	if !ids.Valid(v) {
		return "", badParam(name, "%s must be 16 characters of [a-z2-7]", name).
			withDetail("value", v)
	}
	return v, nil
}

// pageNumber reads the `{n}` wildcard. Every page number in this API is
// 1-based: `n ∈ [1, page_count]` and there is no page 0 (arch §7.1).
//
// The two failures are deliberately different codes, and the split comes from
// the contract rather than from taste. §7.6 enumerates the 404 conditions as
// "unknown book, or `n` outside `[1, page_count]`" — 0 and −1 are outside the
// range, so they are 404 exactly as page 9000 of a 3-page book is. A segment
// that is not a number at all is not a page reference in the first place, so it
// is `400 bad_request`.
func pageNumber(r *http.Request, name string) (int, error) {
	raw := r.PathValue(name)
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, badParam(name, "page number must be an integer").withDetail("value", raw)
	}
	if n < 1 {
		return 0, notFound("page numbers are 1-based; there is no page %d", n)
	}
	return n, nil
}

// queryEnum reads a closed-set parameter. An absent or empty value selects
// `def`; anything outside `allowed` is `400` with `detail: {param}` exactly as
// arch §7.5 requires.
//
// Unknown *parameters* are ignored (§7.1) — only unknown *values* of a known
// parameter are rejected. That asymmetry is deliberate: a stray `&utm_source=`
// must not break a page load, while a misspelt `sort=recentt` must not silently
// give the user a differently-ordered library than they asked for.
func queryEnum(r *http.Request, param, def string, allowed ...string) (string, error) {
	v := r.URL.Query().Get(param)
	if v == "" {
		return def, nil
	}
	for _, a := range allowed {
		if v == a {
			return v, nil
		}
	}
	return "", badParam(param, "%s must be one of %s", param, strings.Join(allowed, ", ")).
		withDetail("value", v)
}

// queryInt reads a bounded integer parameter, defaulting when absent.
func queryInt(r *http.Request, param string, def, minimum, maximum int) (int, error) {
	raw := r.URL.Query().Get(param)
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, badParam(param, "%s must be an integer", param).withDetail("value", raw)
	}
	if n < minimum || n > maximum {
		return 0, badParam(param, "%s must be between %d and %d", param, minimum, maximum).
			withDetail("value", n)
	}
	return n, nil
}

// decodeJSON applies the strict decoding of arch §7.1: the body is capped, then
// decoded with DisallowUnknownFields so that a typo surfaces immediately as
// `400 bad_request` naming the field rather than being silently dropped.
//
// A trailing second JSON value is refused too. `{"page":3}{"page":9000}` is not
// one document, and accepting the first while ignoring the second would be a
// quiet way to write a value the user never sent.
func decodeJSON(w http.ResponseWriter, r *http.Request, limit int64, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		return decodeError(err, limit)
	}
	if err := dec.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		return badRequest("body must contain exactly one JSON object")
	}
	return nil
}

// decodeJSONOptional is decodeJSON for endpoints whose body is optional
// (`POST /api/scan` takes `{}` or nothing at all).
func decodeJSONOptional(w http.ResponseWriter, r *http.Request, limit int64, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return decodeError(err, limit)
	}
	if err := dec.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		return badRequest("body must contain exactly one JSON object")
	}
	return nil
}

// decodeError turns a json decoding failure into the envelope, preserving the
// one piece of machine-readable information the client can act on: which field
// it got wrong.
func decodeError(err error, limit int64) error {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		return badRequest("request body exceeds %d bytes", limit)
	}
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		return badRequest("field %q must be of type %s", typeErr.Field, typeErr.Type.String()).
			withDetail("field", typeErr.Field)
	}
	if field, ok := unknownField(err); ok {
		return badRequest("unknown field %q", field).withDetail("field", field)
	}
	if errors.Is(err, io.EOF) {
		return badRequest("request body is empty")
	}
	return badRequest("request body is not valid JSON")
}

// unknownField extracts the field name from encoding/json's
// `json: unknown field "x"`. The standard library gives no typed error for it,
// and the field name is exactly what makes the 400 actionable, so the string is
// parsed here — in one place, guarded by a test — rather than at ten call sites.
func unknownField(err error) (string, bool) {
	const prefix = "json: unknown field "
	msg := err.Error()
	i := strings.Index(msg, prefix)
	if i < 0 {
		return "", false
	}
	quoted := msg[i+len(prefix):]
	name, uerr := strconv.Unquote(quoted)
	if uerr != nil {
		return strings.Trim(quoted, `"`), true
	}
	return name, true
}
