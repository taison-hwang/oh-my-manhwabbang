package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"shelf/internal/archive"
	"shelf/internal/scanner"
	"shelf/internal/source"
	"shelf/internal/thumbs"
)

// The field name is what makes an unknown-field 400 actionable, and
// encoding/json exposes it only inside an error string. This pins the parse so
// a standard-library wording change is caught here rather than by a frontend
// bug report.
func TestUnknownField_extractsTheFieldName(t *testing.T) {
	t.Parallel()

	var dst struct {
		Known string `json:"known"`
	}
	dec := json.NewDecoder(strings.NewReader(`{"unknown_thing":1}`))
	dec.DisallowUnknownFields()
	err := dec.Decode(&dst)
	if err == nil {
		t.Fatal("DisallowUnknownFields accepted an unknown field")
	}
	name, ok := unknownField(err)
	if !ok || name != "unknown_thing" {
		t.Fatalf("unknownField(%v) = %q, %v; want \"unknown_thing\", true", err, name, ok)
	}
	if _, ok := unknownField(errors.New("something else entirely")); ok {
		t.Error("unknownField matched an unrelated error")
	}
}

// arch §5.3's `?v=` matrix, in isolation.
func TestVersionMode(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		query   string
		current string
		want    cacheMode
		wantErr bool
	}{
		{"absent", "", "abc", cacheShort, false},
		{"matching", "?v=abc", "abc", cacheImmutable, false},
		{"stale", "?v=old", "abc", cacheShort, true},
		{
			// A cover whose source is a loose file has no content version at
			// all, so any `?v=` a client invents is by definition not the
			// current one. The client is told the current value — the empty
			// string — and stops sending it.
			"no current version", "?v=anything", "", cacheShort, true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/api/books/x/pages/1"+tc.query, nil)
			got, err := versionMode(r, tc.current)
			if tc.wantErr {
				var ae *apiError
				if !errors.As(err, &ae) || ae.code != CodeStaleVersion {
					t.Fatalf("error = %v, want stale_version", err)
				}
				if ae.detail["cv"] != tc.current {
					t.Errorf("detail.cv = %v, want %q", ae.detail["cv"], tc.current)
				}
				return
			}
			if err != nil {
				t.Fatalf("versionMode: %v", err)
			}
			if got != tc.want {
				t.Errorf("mode = %v, want %v", got, tc.want)
			}
		})
	}

	if got := cacheImmutable.header(); got != "public, max-age=31536000, immutable" {
		t.Errorf("immutable header = %q", got)
	}
	if got := cacheShort.header(); got != "public, max-age=60, must-revalidate" {
		t.Errorf("short header = %q", got)
	}
}

// arch §5.3 — Content-Type comes from a fixed extension table, never from
// sniffing. Combined with nosniff, that is what stops an entry called `x.jpg`
// holding HTML from being rendered as HTML.
func TestContentTypeFor(t *testing.T) {
	t.Parallel()

	cases := []struct{ ext, fromSource, want string }{
		{".jpg", "", "image/jpeg"},
		{".jpeg", "", "image/jpeg"},
		{".png", "", "image/png"},
		{".gif", "", "image/gif"},
		{".webp", "", "image/webp"},
		{".avif", "", "image/avif"},
		{"", "image/jpeg", "image/jpeg"},          // a rendered PDF page
		{".exe", "", "application/octet-stream"},  // never text/html
		{".html", "", "application/octet-stream"}, // especially not this one
	}
	for _, tc := range cases {
		t.Run(tc.ext, func(t *testing.T) {
			if got := contentTypeFor(tc.ext, tc.fromSource); got != tc.want {
				t.Errorf("contentTypeFor(%q, %q) = %q, want %q", tc.ext, tc.fromSource, got, tc.want)
			}
		})
	}
}

// The conditional-request parse used on the one path that does not go through
// http.ServeContent: a forward-only deflate stream.
func TestNoneMatch(t *testing.T) {
	t.Parallel()

	const etag = `"p1-abcdefghijklmnop-1-deadbeef"`
	cases := []struct {
		header string
		want   bool
	}{
		{"", false},
		{etag, true},
		{`"other", ` + etag, true},
		{`W/` + etag, true},
		{"*", true},
		{`"other"`, false},
	}
	for _, tc := range cases {
		t.Run(tc.header, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.header != "" {
				r.Header.Set("If-None-Match", tc.header)
			}
			if got := noneMatch(r, etag); got != tc.want {
				t.Errorf("noneMatch(%q) = %v, want %v", tc.header, got, tc.want)
			}
		})
	}
}

// NFR-SEC-003 — the injected `<base href>` always ends in a slash. Without it
// the browser resolves `assets/x.js` against the parent directory and every
// asset 404s.
func TestInjectBaseHref(t *testing.T) {
	t.Parallel()

	const doc = "<!doctype html>\n<html><head>\n<base href=\"/\" />\n<title>SHELF</title>\n</head><body></body></html>"

	for _, base := range []string{"", "/reader", "/a/b"} {
		t.Run("base="+base, func(t *testing.T) {
			s := &Server{base: base}
			got := string(s.injectBaseHref([]byte(doc)))
			want := `<base href="` + base + `/" />`
			if !strings.Contains(got, want) {
				t.Errorf("injected document has no %s:\n%s", want, got)
			}
			if strings.Count(got, "<base") != 1 {
				t.Errorf("document has %d <base> elements, want 1", strings.Count(got, "<base"))
			}
		})
	}

	t.Run("a document with no placeholder still gets one", func(t *testing.T) {
		s := &Server{base: "/reader"}
		got := string(s.injectBaseHref([]byte("<html><head><title>x</title></head></html>")))
		if !strings.Contains(got, `<base href="/reader/" />`) {
			t.Errorf("no base href was inserted:\n%s", got)
		}
	})
}

// arch §7.10 — before the first scan there is no snapshot, and "idle with
// nothing in it" is the answer the frontend's poll policy expects.
func TestToScanStatus_nilSnapshotIsIdle(t *testing.T) {
	t.Parallel()

	got := toScanStatus(nil)
	if got.State != string(scanner.PhaseIdle) {
		t.Errorf("state = %q, want idle", got.State)
	}
	if got.Roots == nil {
		t.Error("roots is nil; the contract types it string[] and the client iterates it")
	}
	if got.RunID != nil || got.StartedAt != nil || got.ETAMs != nil {
		t.Errorf("an idle snapshot carries values: %+v", got)
	}

	body, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	if !strings.Contains(string(body), `"roots":[]`) {
		t.Errorf("roots marshalled as %s, want []", body)
	}
}

// arch §7.2 — the code→status mapping, spelled out so a renamed constant
// cannot silently change a status.
func TestStatusForCode(t *testing.T) {
	t.Parallel()

	cases := map[string]int{
		CodeBadRequest:       400,
		CodeUnauthorized:     401,
		CodeNotFound:         404,
		CodeConflict:         409,
		CodeStaleVersion:     409,
		CodeUnprocessable:    422,
		CodeThumbUnavailable: 422,
		CodeUnsupported:      501,
		CodeUnavailable:      503,
		CodeInternal:         500,
		CodeRateLimited:      429,
		"something else":     500,
	}
	for code, want := range cases {
		if got := statusForCode(code); got != want {
			t.Errorf("statusForCode(%q) = %d, want %d", code, got, want)
		}
	}
}

// The 405 body carries the same Allow list as the header, sorted, with HEAD
// wherever GET is permitted.
func TestMethodNotAllowed_envelope(t *testing.T) {
	t.Parallel()

	err := methodNotAllowed([]string{"GET", "HEAD", "PUT"})
	if err.status != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", err.status)
	}
	if err.header.Get("Allow") != "GET, HEAD, PUT" {
		t.Errorf("Allow = %q", err.header.Get("Allow"))
	}
	if err.detail["allow"] != "GET, HEAD, PUT" {
		t.Errorf("detail.allow = %v", err.detail["allow"])
	}
}

// arch §7.6 — how internal/source's failures reach the client. Each of these is
// a different thing for the UI to do, which is why they are different codes:
// 501 means "this build cannot", 503 means "try again when the drive is back",
// 422 means "this file is broken", 500 means "we have a bug".
func TestSourceError_mapping(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"unsupported format", source.ErrUnsupported, http.StatusNotImplemented, CodeUnsupported},
		{"no pages", source.ErrNoPages, http.StatusNotFound, CodeNotFound},
		{"unreachable root", source.ErrUnknownRoot, http.StatusServiceUnavailable, CodeUnavailable},
		{"encrypted archive", archive.ErrEncrypted, http.StatusUnprocessableEntity, CodeUnprocessable},
		{"corrupt archive", archive.ErrCorrupt, http.StatusUnprocessableEntity, CodeUnprocessable},
		{"unsupported method", archive.ErrUnsupportedMethod, http.StatusUnprocessableEntity, CodeUnprocessable},
		// Path-traversal layer 2 refused a stored relative path. That can only
		// happen if index.db was tampered with, so it is our bug and not the
		// client's: 500, and an error-level log (arch §8.1 layer 4).
		{"unsafe stored path", source.ErrUnsafePath, http.StatusInternalServerError, CodeInternal},
		{"anything else", errors.New("disk on fire"), http.StatusServiceUnavailable, CodeUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var ae *apiError
			if !errors.As(sourceError(fmt.Errorf("wrapped: %w", tc.err)), &ae) {
				t.Fatalf("sourceError(%v) is not an apiError", tc.err)
			}
			if ae.status != tc.status || ae.code != tc.code {
				t.Errorf("sourceError(%v) = %d %s, want %d %s", tc.err, ae.status, ae.code, tc.status, tc.code)
			}
		})
	}
}

// arch §5.5 — the thumbnail failures, and the `detail.reason` the frontend keys
// its fallback on.
func TestThumbError_mapping(t *testing.T) {
	t.Parallel()

	var ae *apiError
	if !errors.As(thumbError(&thumbs.UndecodableError{Reason: thumbs.ReasonAnimatedWebP}), &ae) {
		t.Fatal("an UndecodableError is not an apiError")
	}
	if ae.status != http.StatusUnprocessableEntity || ae.code != CodeThumbUnavailable {
		t.Errorf("undecodable = %d %s, want 422 thumb_unavailable", ae.status, ae.code)
	}
	if ae.detail["reason"] != thumbs.ReasonAnimatedWebP {
		t.Errorf("detail.reason = %v, want %q", ae.detail["reason"], thumbs.ReasonAnimatedWebP)
	}

	for _, tc := range []struct {
		err    error
		status int
	}{
		{thumbs.ErrNotFound, http.StatusNotFound},
		{thumbs.ErrBadRequest, http.StatusBadRequest},
		{thumbs.ErrClosed, http.StatusServiceUnavailable},
		{errors.New("boom"), http.StatusInternalServerError},
	} {
		var got *apiError
		if !errors.As(thumbError(fmt.Errorf("wrapped: %w", tc.err)), &got) {
			t.Fatalf("thumbError(%v) is not an apiError", tc.err)
		}
		if got.status != tc.status {
			t.Errorf("thumbError(%v) = %d, want %d", tc.err, got.status, tc.status)
		}
	}
}
