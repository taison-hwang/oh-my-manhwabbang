package httpapi

import (
	"bytes"
	"encoding/json"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"shelf/internal/auth"
	"shelf/internal/scanner"
	"shelf/internal/thumbs"
)

// The golden files are the reconciliation artefact of impl-plan §4: WP-13 diffs
// `testdata/golden/*.json` against `web/src/api/types.ts`, and WP-06 switches
// its MSW fixtures onto them. They are therefore complete responses from a real
// server over real SQLite databases and a real ZIP on disk — not hand-written
// samples — with every timestamp coming from a fixed clock so the bytes are
// reproducible.
//
// Regenerate with:
//
//	go test ./internal/httpapi -run TestGolden -update
//
// and read the diff. A change here is a change to the frozen contract and needs
// an `A-` amendment in impl-plan §0.3 first.
var update = flag.Bool("update", false, "rewrite the golden files in testdata/golden")

const goldenDir = "testdata/golden"

// assertGolden compares a JSON response against its golden file.
func assertGolden(t *testing.T, name string, body []byte) {
	t.Helper()
	assertGoldenRedacted(t, name, body, nil)
}

// assertGoldenRedacted is assertGolden with the per-run temporary directory
// replaced, for the two payloads that carry an absolute path.
func assertGoldenRedacted(t *testing.T, name string, body []byte, e *env) {
	t.Helper()
	if e != nil {
		body = e.redact(body)
	}

	var pretty bytes.Buffer
	if err := json.Indent(&pretty, body, "", "  "); err != nil {
		t.Fatalf("%s: the response is not valid JSON: %v\n%s", name, err, body)
	}
	pretty.WriteByte('\n')

	path := filepath.Join(goldenDir, name+".json")
	if *update {
		if err := os.MkdirAll(goldenDir, 0o755); err != nil {
			t.Fatalf("creating %s: %v", goldenDir, err)
		}
		if err := os.WriteFile(path, pretty.Bytes(), 0o644); err != nil {
			t.Fatalf("writing %s: %v", path, err)
		}
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v (run `go test ./internal/httpapi -run TestGolden -update`)", path, err)
	}
	if !bytes.Equal(want, pretty.Bytes()) {
		t.Errorf("%s does not match the golden file.\n--- want ---\n%s\n--- got ---\n%s", path, want, pretty.Bytes())
	}
}

// TestGolden captures one file per response shape the contract defines.
//
// `withoutAVIFConfig()` is what makes these files TAG-INDEPENDENT, and it is
// load-bearing. `avif_enabled` is `thumbnails.avif_enabled && AVIFSupported()`
// (ruling E-21), and since `noavif` became the default build tag the second half
// is false in the configuration that ships — so with the config key at its `true`
// default, `health.json` and `settings.json` could only ever match untagged, and
// `go test -tags noavif ./...` failed on precisely these two. Turning the key off
// makes the answer `false` in every build, exactly as `pdf.enabled` is already
// left off here. The `true` case is not lost: TestCapabilityFlags_neverExceed
// TheBuild asserts BOTH directions (reported ⟹ capable AND capable ⟹ reported)
// in whichever build it runs, and `make test` now runs the suite both ways.
func TestGolden(t *testing.T) {
	e := newEnv(t, withoutAVIFConfig())

	// A cache file with known content, so `GET /api/cache/usage` reports real
	// numbers that do not depend on the JPEG encoder's output size.
	seedCacheFile(t, e, "thumbs/2k/q5/2kq5mshjlgisgk4l.jpg", 4096)
	seedCacheFile(t, e, "pdf/6h/nn/6hnnlicenkpkywx4.jpg", 65536)

	cases := []struct {
		name   string
		method string
		target string
		body   string
		status int
	}{
		{name: "health", method: http.MethodGet, target: "/api/health", status: 200},
		{name: "roots", method: http.MethodGet, target: "/api/roots", status: 200},
		{name: "series_list", method: http.MethodGet, target: "/api/series", status: 200},
		{
			// The count idiom of amendment A-8: `total` is the pre-pagination
			// match count, so one row is all the sidebar badge needs.
			name: "series_list_count", method: http.MethodGet,
			target: "/api/series?limit=1", status: 200,
		},
		{
			// A-8 proper: `scope=added` filters on user.db's first_seen_at
			// alone, so the two series with no series_seen row are excluded.
			name: "series_list_scope_added", method: http.MethodGet,
			target: "/api/series?scope=added&sort=added&order=desc", status: 200,
		},
		{
			name: "series_list_search", method: http.MethodGet,
			target: "/api/series?q=" + "%E3%84%B1%E3%84%B1", status: 200, // ㄱㄱ, FR-LIB-006
		},
		{name: "continue", method: http.MethodGet, target: "/api/continue", status: 200},
		{name: "settings", method: http.MethodGet, target: "/api/settings", status: 200},
		{name: "scan_status", method: http.MethodGet, target: "/api/scan/status", status: 200},
		{name: "scan_log", method: http.MethodGet, target: "/api/scan/log", status: 200},
		{name: "cache_usage", method: http.MethodGet, target: "/api/cache/usage", status: 200},
		{name: "auth_status", method: http.MethodGet, target: "/api/auth/status", status: 200},
		{name: "scan_accepted", method: http.MethodPost, target: "/api/scan", body: `{"full":true}`, status: 202},
		{name: "cache_purge", method: http.MethodDelete, target: "/api/cache?kind=pdf", status: 200},
		{name: "progress_export", method: http.MethodGet, target: "/api/progress/export", status: 200},
		{
			name: "error_not_found", method: http.MethodGet,
			target: "/api/series/aaaaaaaaaaaaaaaa", status: 404,
		},
		{
			name: "error_bad_id", method: http.MethodGet,
			target: "/api/series/NOT-AN-ID", status: 400,
		},
		{
			name: "error_bad_param", method: http.MethodGet,
			target: "/api/series?sort=recentt", status: 400,
		},
		{
			name: "error_unknown_field", method: http.MethodPut,
			target: "/api/settings", body: `{"theme":"dark","them":"light"}`, status: 400,
		},
		{
			name: "error_method_not_allowed", method: http.MethodDelete,
			target: "/api/series", status: 405,
		},
		{
			name: "error_unknown_endpoint", method: http.MethodGet,
			target: "/api/nope", status: 404,
		},
		{
			// `403 forbidden` — amendment A-11 (ruling E-26). This env has
			// `server.allow_root_editing` at its default, which is off, so the
			// envelope pinned here is the one the shipped configuration
			// produces. `detail.reason` is what lets the UI print the remedy
			// that applies, and it is pinned with it.
			name: "error_forbidden", method: http.MethodPost,
			target: "/api/roots", body: `{"path":"/tmp"}`, status: 403,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var w *httptest.ResponseRecorder
			if tc.body == "" {
				w = e.do(tc.method, tc.target, nil)
			} else {
				w = e.jsonBody(tc.method, tc.target, tc.body)
			}
			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d; body: %s", w.Code, tc.status, w.Body.String())
			}
			assertGoldenRedacted(t, tc.name, w.Body.Bytes(), e)
		})
	}

	// The id-bearing payloads need the fixture ids, so they are built here
	// rather than in the table.
	t.Run("series_detail", func(t *testing.T) {
		w := e.get("/api/series/" + e.seriesFolderID)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", w.Code, w.Body.String())
		}
		assertGoldenRedacted(t, "series_detail", w.Body.Bytes(), e)
	})

	// The other cover shape: cover_kind='page', so `cover_cv` is the cover
	// book's content version rather than null.
	t.Run("series_detail_page_cover", func(t *testing.T) {
		w := e.get("/api/series/" + e.seriesCloverID)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", w.Code, w.Body.String())
		}
		assertGoldenRedacted(t, "series_detail_page_cover", w.Body.Bytes(), e)
	})

	t.Run("book_detail", func(t *testing.T) {
		w := e.get("/api/books/" + e.bookZipID)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", w.Code, w.Body.String())
		}
		assertGoldenRedacted(t, "book_detail", w.Body.Bytes(), e)
	})

	// impl-plan §4 rule 4: a book whose status is not "ok" answers 200 with
	// `pages: []` and a populated `error`.
	t.Run("book_detail_broken", func(t *testing.T) {
		w := e.get("/api/books/" + e.bookBrokenID)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 for a broken book: %s", w.Code, w.Body.String())
		}
		assertGoldenRedacted(t, "book_detail_broken", w.Body.Bytes(), e)
	})

	t.Run("book_prefs", func(t *testing.T) {
		w := e.get("/api/books/" + e.bookZipID + "/prefs")
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", w.Code, w.Body.String())
		}
		assertGoldenRedacted(t, "book_prefs", w.Body.Bytes(), e)
	})

	t.Run("progress", func(t *testing.T) {
		w := e.jsonBody(http.MethodPut, "/api/books/"+e.bookZipID+"/progress", `{"page":2}`)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", w.Code, w.Body.String())
		}
		assertGoldenRedacted(t, "progress", w.Body.Bytes(), e)
	})

	// The `409 stale_version` of arch §5.3, with the current cv in detail so
	// the client can refetch its metadata instead of caching a superseded page.
	t.Run("error_stale_version", func(t *testing.T) {
		w := e.get("/api/books/" + e.bookZipID + "/pages/1?v=stalestalestale")
		if w.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409: %s", w.Code, w.Body.String())
		}
		assertGolden(t, "error_stale_version", w.Body.Bytes())
	})

	// `501 unsupported` — a PDF page with `pdf.enabled: false` (the fixture's
	// configuration) is exactly the shape a `-tags nopdf` build produces.
	t.Run("error_unsupported", func(t *testing.T) {
		w := e.get("/api/books/" + e.bookPDFID + "/pages/1")
		if w.Code != http.StatusNotImplemented {
			t.Fatalf("status = %d, want 501: %s", w.Code, w.Body.String())
		}
		assertGolden(t, "error_unsupported", w.Body.Bytes())
	})

	t.Run("error_conflict", func(t *testing.T) {
		e.scan.startErr = scanner.ErrBusy
		defer func() { e.scan.startErr = nil }()
		w := e.jsonBody(http.MethodPost, "/api/scan", `{}`)
		if w.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409: %s", w.Code, w.Body.String())
		}
		assertGolden(t, "error_conflict", w.Body.Bytes())
	})

	t.Run("progress_import", func(t *testing.T) {
		doc := `{"format":"shelf-progress/1","exported_at":1785229200,"id_version":"shelf-id/1",` +
			`"items":[{"book_id":"` + e.bookDirID + `","series_id":"` + e.seriesFolderID + `",` +
			`"root_name":"manga","book_path":"` + jsonEscape(bookDirPath) + `","last_page":2,` +
			`"page_count":2,"completed":true,"started_at":1785229100,"updated_at":1785229150}],"prefs":[]}`
		w := e.jsonBody(http.MethodPost, "/api/progress/import", doc)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", w.Code, w.Body.String())
		}
		assertGolden(t, "import_result", w.Body.Bytes())
	})
}

// TestGolden_rootCreated pins the `201 RootEntry` of amendment A-11.
//
// It needs its own server because it is the one shape that requires the gate
// OPEN, and `settings.json` and `error_forbidden.json` above are pinned with it
// shut — which is the pairing impl-plan §0.3 asks for in as many words: "a
// golden file pins `false` as happily as `true`; the gate's tests must assert
// the 403 **and** the 201 against the same server with the key flipped, not one
// of them."
//
// The request omits `label` on purpose. §7.4 requires the derived label to be
// *written* rather than left absent, so a golden that sent one would not show
// the rule at work.
func TestGolden_rootCreated(t *testing.T) {
	e := newEnv(t, withoutAVIFConfig(), withRootEditing())

	// A directory under the fixture's own temporary root, so the absolute path
	// in the payload redacts to a stable placeholder the way `Root.path` does.
	dir := filepath.Join(e.dir, "books")
	mustMkdir(t, dir)

	w := e.jsonBody(http.MethodPost, "/api/roots", `{"path":"`+jsonEscape(dir)+`"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", w.Code, w.Body.String())
	}
	if got, want := w.Header().Get("Location"), "/api/roots/books"; got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
	assertGoldenRedacted(t, "root_created", w.Body.Bytes(), e)
}

// TestGolden_unauthorized needs its own server, because the envelope only
// exists when a password is configured.
func TestGolden_unauthorized(t *testing.T) {
	e := newEnv(t, withPassword("hunter2"))
	w := e.get("/api/roots")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401: %s", w.Code, w.Body.String())
	}
	assertGolden(t, "error_unauthorized", w.Body.Bytes())
}

// TestGolden_thumbUnavailable pins the `422 thumb_unavailable` envelope,
// including `detail.reason` — the frontend keys its fallback on it (arch §5.5).
//
// It maps the sentinel directly rather than staging an undecodable fixture,
// because the mapping is the part this package owns: internal/thumbs already
// has its own test that an animated WebP produces this error.
func TestGolden_thumbUnavailable(t *testing.T) {
	t.Parallel()
	err := thumbError(&thumbs.UndecodableError{Reason: thumbs.ReasonAnimatedWebP})
	rec := httptest.NewRecorder()
	writeError(rec, httptest.NewRequest(http.MethodGet, "/api/books/x/thumbs/1", nil), discard(), err)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
	assertGolden(t, "error_thumb_unavailable", rec.Body.Bytes())
}

// TestGolden_rateLimited pins the `429 rate_limited` envelope of amendment A-9
// (ruling E-13) — arch §8.2's login limiter, which §7.2 had no code for until
// A-9 added one. `detail.retry_after` is the same integer as the `Retry-After`
// header, so a client that ignores headers can still back off.
//
// It maps a fixed wait rather than emptying a real token bucket, because a real
// one leaves a sub-second remainder on the clock and the truncated seconds are
// then 11 or 12 depending on how fast the machine is — a flaky golden. The
// bucket itself is exercised by TestAuth_loginRateLimit; the mapping is the part
// this file is pinning.
func TestGolden_rateLimited(t *testing.T) {
	t.Parallel()
	err := loginFailure(&auth.RateLimitError{RetryAfter: 12 * time.Second})
	rec := httptest.NewRecorder()
	writeError(rec, httptest.NewRequest(http.MethodPost, "/api/auth/login", nil), discard(), err)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "12" {
		t.Errorf("Retry-After = %q, want %q", got, "12")
	}
	assertGolden(t, "error_rate_limited", rec.Body.Bytes())
}

// jsonEscape quotes a string for embedding in a JSON literal in a test.
func jsonEscape(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b[1 : len(b)-1])
}

// seedCacheFile writes a file of a known size into the thumbnail cache so the
// usage golden carries deterministic byte counts.
func seedCacheFile(t *testing.T, e *env, rel string, size int) {
	t.Helper()
	path := filepath.Join(e.cfg.Storage.CacheDir, filepath.FromSlash(rel))
	mustMkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, bytes.Repeat([]byte{0x5a}, size), 0o644); err != nil {
		t.Fatalf("seeding %s: %v", path, err)
	}
}
