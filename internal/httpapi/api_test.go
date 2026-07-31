package httpapi

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"shelf/internal/config"
	"shelf/internal/ids"
	"shelf/internal/index"
	"shelf/internal/natsort"
	"shelf/internal/scanner"
	"shelf/internal/thumbs"
)

// impl-plan §3 WP-12 acceptance 1 — every route of arch §7.13 answers, on
// net/http.ServeMux, with no router dependency.
func TestRoutes_everyEndpointOfSection713Answers(t *testing.T) {
	e := newEnv(t)

	cases := []struct {
		name   string
		method string
		target string
		body   string
		want   int
	}{
		{"health", http.MethodGet, "/api/health", "", 200},
		{"health verbose", http.MethodGet, "/api/health?verbose=1", "", 200},
		{"roots", http.MethodGet, "/api/roots", "", 200},
		{"series list", http.MethodGet, "/api/series", "", 200},
		{"series detail", http.MethodGet, "/api/series/{sid}", "", 200},
		{"series cover", http.MethodGet, "/api/series/{sid}/cover", "", 202},
		{"series rescan", http.MethodPost, "/api/series/{sid}/rescan", "", 202},
		{"book detail", http.MethodGet, "/api/books/{bid}", "", 200},
		{"book page", http.MethodGet, "/api/books/{bid}/pages/1", "", 200},
		{"book thumb", http.MethodGet, "/api/books/{bid}/thumbs/1", "", 202},
		{"put progress", http.MethodPut, "/api/books/{bid}/progress", `{"page":1}`, 200},
		{"delete progress", http.MethodDelete, "/api/books/{bid}/progress", "", 204},
		{"get prefs", http.MethodGet, "/api/books/{bid}/prefs", "", 200},
		{"put prefs", http.MethodPut, "/api/books/{bid}/prefs", `{"fit_mode":"width"}`, 200},
		{"continue", http.MethodGet, "/api/continue", "", 200},
		{"get settings", http.MethodGet, "/api/settings", "", 200},
		{"put settings", http.MethodPut, "/api/settings", `{"theme":"dark"}`, 200},
		{"cache usage", http.MethodGet, "/api/cache/usage", "", 200},
		{"cache purge", http.MethodDelete, "/api/cache?kind=thumbs", "", 200},
		{"start scan", http.MethodPost, "/api/scan", `{}`, 202},
		{"scan status", http.MethodGet, "/api/scan/status", "", 200},
		{"cancel scan", http.MethodPost, "/api/scan/cancel", "", 204},
		{"scan log", http.MethodGet, "/api/scan/log", "", 200},
		{"progress export", http.MethodGet, "/api/progress/export", "", 200},
		{"auth status", http.MethodGet, "/api/auth/status", "", 200},
		{"auth logout", http.MethodPost, "/api/auth/logout", "", 204},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := strings.NewReplacer(
				"{sid}", e.seriesFolderID,
				"{bid}", e.bookZipID,
			).Replace(tc.target)

			var w = e.do(tc.method, target, nil)
			if tc.body != "" {
				w = e.jsonBody(tc.method, target, tc.body)
			}
			if w.Code != tc.want {
				t.Fatalf("%s %s = %d, want %d; body: %s", tc.method, target, w.Code, tc.want, w.Body.String())
			}
		})
	}
}

// impl-plan §3 WP-12 acceptance 1 — the wrong verb is 405, in the §7.2
// envelope, with an Allow header.
func TestMethod_wrongVerb_is405WithAllowHeader(t *testing.T) {
	e := newEnv(t)

	cases := []struct {
		method, target, wantAllow string
	}{
		{http.MethodPost, "/api/series", "GET, HEAD"},
		// GET, HEAD, POST since amendment A-11: `POST /api/roots` exists on every
		// build and answers 403 when the gate is shut, so a client can tell "the
		// feature is switched off here" from "this server is too old".
		{http.MethodDelete, "/api/roots", "GET, HEAD, POST"},
		{http.MethodGet, "/api/roots/manga", "DELETE"},
		{http.MethodPatch, "/api/settings", "GET, HEAD, PUT"},
		{http.MethodGet, "/api/scan", "POST"},
		{http.MethodPut, "/api/cache", "DELETE"},
		{http.MethodGet, "/api/auth/login", "POST"},
		{http.MethodPost, "/api/books/" + e.bookZipID + "/progress", "DELETE, PUT"},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.target, func(t *testing.T) {
			w := e.do(tc.method, tc.target, nil)
			body := errorBody(t, w, http.StatusMethodNotAllowed, CodeBadRequest)
			if got := w.Header().Get("Allow"); got != tc.wantAllow {
				t.Errorf("Allow = %q, want %q", got, tc.wantAllow)
			}
			if body.Detail["allow"] != tc.wantAllow {
				t.Errorf("detail.allow = %v, want %q", body.Detail["allow"], tc.wantAllow)
			}
		})
	}
}

// HEAD is accepted wherever GET is, and net/http suppresses the body itself.
func TestHead_isAcceptedWhereverGetIs(t *testing.T) {
	e := newEnv(t)
	w := e.do(http.MethodHead, "/api/settings", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("HEAD /api/settings = %d, want 200", w.Code)
	}
}

// impl-plan §3 WP-12 acceptance 1 — an unknown /api/* path is a JSON 404 in the
// envelope, **never** the SPA fallback. Answering it with index.html would make
// the frontend report a JSON parse error instead of a 404.
func TestUnknownAPIPath_isJSONNotTheSPAFallback(t *testing.T) {
	e := newEnv(t, withStatic())

	w := e.get("/api/does-not-exist")
	errorBody(t, w, http.StatusNotFound, CodeNotFound)
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if strings.Contains(w.Body.String(), "<!doctype html>") {
		t.Error("an unknown /api/ path served the SPA shell")
	}

	// A client-side route, by contrast, *is* the SPA.
	spa := e.get("/series/" + e.seriesFolderID)
	if spa.Code != http.StatusOK || !strings.Contains(spa.Body.String(), "<div id=\"root\">") {
		t.Errorf("a client-side route did not serve index.html: %d %s", spa.Code, spa.Body.String())
	}
}

// arch §7.1 / impl-plan §3 WP-12 acceptance 2 — a syntactically invalid id is
// 400; a well-formed but unknown one is 404. The distinction is what lets the
// frontend tell "you built a bad URL" from "that series is gone".
func TestIDs_invalidIs400_wellFormedUnknownIs404(t *testing.T) {
	e := newEnv(t)

	invalid := []string{
		"NOT-AN-ID",
		"tooshort",
		"aaaaaaaaaaaaaaaaa", // 17 chars
		"aaaaaaaaaaaaaaa1",  // '1' is not in the base32 alphabet
		"AAAAAAAAAAAAAAAA",  // uppercase
	}
	for _, id := range invalid {
		t.Run("invalid "+id, func(t *testing.T) {
			w := e.get("/api/series/" + id)
			body := errorBody(t, w, http.StatusBadRequest, CodeBadRequest)
			if body.Detail["param"] != "sid" {
				t.Errorf("detail.param = %v, want \"sid\"", body.Detail["param"])
			}
		})
	}

	for _, target := range []string{
		"/api/series/aaaaaaaaaaaaaaaa",
		"/api/series/aaaaaaaaaaaaaaaa/cover",
		"/api/books/aaaaaaaaaaaaaaaa",
		"/api/books/aaaaaaaaaaaaaaaa/pages/1",
		"/api/books/aaaaaaaaaaaaaaaa/thumbs/1",
		"/api/books/aaaaaaaaaaaaaaaa/prefs",
	} {
		t.Run("unknown "+target, func(t *testing.T) {
			errorBody(t, e.get(target), http.StatusNotFound, CodeNotFound)
		})
	}
}

// impl-plan §4 rule 5 — unknown JSON *body* fields are 400.
func TestStrictDecoding_unknownBodyField_is400(t *testing.T) {
	e := newEnv(t)

	cases := []struct{ name, method, target, body, field string }{
		{"settings", http.MethodPut, "/api/settings", `{"them":"dark"}`, "them"},
		{"settings server block", http.MethodPut, "/api/settings", `{"server":{"port":1}}`, "server"},
		{"progress", http.MethodPut, "/api/books/{bid}/progress", `{"page":1,"pages":2}`, "pages"},
		{"prefs", http.MethodPut, "/api/books/{bid}/prefs", `{"direction":"rtl"}`, "direction"},
		{"scan", http.MethodPost, "/api/scan", `{"deep":true}`, "deep"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := strings.ReplaceAll(tc.target, "{bid}", e.bookZipID)
			w := e.jsonBody(tc.method, target, tc.body)
			body := errorBody(t, w, http.StatusBadRequest, CodeBadRequest)
			if body.Detail["field"] != tc.field {
				t.Errorf("detail.field = %v, want %q", body.Detail["field"], tc.field)
			}
		})
	}

	// The login body is only reachable when a password is configured.
	withAuth := newEnv(t, withPassword("hunter2"))
	body := errorBody(t, withAuth.jsonBody(http.MethodPost, "/api/auth/login", `{"pass":"x"}`),
		http.StatusBadRequest, CodeBadRequest)
	if body.Detail["field"] != "pass" {
		t.Errorf("detail.field = %v, want \"pass\"", body.Detail["field"])
	}
}

// NFR-SEC-001 — path traversal is impossible by construction. Every route takes
// an opaque id, so there is no request that can name a file: the shapes below
// are refused by id validation, by the router's own path cleaning, or by the
// 404 catch-all, and none of them ever reaches a filesystem call.
func TestPathTraversal_isImpossible(t *testing.T) {
	e := newEnv(t, withStatic())

	targets := []string{
		"/api/series/..%2f..%2fetc%2fpasswd",
		"/api/series/" + e.seriesFolderID + "/../../../etc/passwd",
		"/api/books/" + e.bookZipID + "/pages/../../../etc/passwd",
		"/api/books/..%2F..%2Fetc%2Fpasswd/pages/1",
		"/../../etc/passwd",
		"/assets/../../../etc/passwd",
	}
	for _, target := range targets {
		t.Run(target, func(t *testing.T) {
			w := e.get(target)
			if w.Code == http.StatusOK && strings.Contains(w.Body.String(), "root:") {
				t.Fatalf("a traversal attempt returned file content: %s", w.Body.String())
			}
			if w.Code >= 500 {
				t.Fatalf("a traversal attempt produced %d: %s", w.Code, w.Body.String())
			}
		})
	}

	// The one thing that must be impossible whatever the request looks like:
	// no page handler ever receives a name. Page rows are addressed by an
	// integer index, and the entry name is display metadata (arch §8.1).
	if got := e.get("/api/books/" + e.bookZipID + "/pages/001.jpg"); got.Code != http.StatusBadRequest {
		t.Errorf("a page addressed by name = %d, want 400", got.Code)
	}
}

// arch §7.8 — `server` is a read-only mirror of the YAML, so sending any key
// under it is 400. Strict decoding makes that automatic, and this test is what
// stops somebody "helpfully" adding the field later.
func TestPutSettings_serverBlockIsRejected(t *testing.T) {
	e := newEnv(t)
	// Every key under `server` is rejected by the same rule — the block has no
	// field on settingsUpdateBody at all — so a new key needs no new branch, and
	// `config_path` (A-10) is here to prove it did not get one.
	for _, body := range []string{
		`{"server":{"auth_enabled":true}}`,
		`{"server":{"config_path":"/etc/shelf/shelf.yaml"}}`,
		`{"theme":"dark","server":{"config_path":"x"}}`,
	} {
		w := e.jsonBody(http.MethodPut, "/api/settings", body)
		errorBody(t, w, http.StatusBadRequest, CodeBadRequest)
	}
}

// Amendment A-10 / ruling E-25 — `Settings.server.config_path` is the file the
// settings screen tells the user to edit.
//
// The value is **absolute**: `./shelf.yaml` is entry 2 of the lookup order
// (`cmd/shelf/flags.go`), and a relative path is precisely the answer that
// names no file. The second half of this test is the one that bites: the
// harness's own config path is already absolute, so `filepath.IsAbs` alone
// would pass over a handler that simply echoed `Config.FilePath`.
func TestSettings_configPathIsTheAbsoluteFileTheUserMustEdit(t *testing.T) {
	e := newEnv(t)

	got := decodeBody[Settings](t, e.get("/api/settings"), http.StatusOK).Server.ConfigPath
	if got == "" {
		t.Fatal(`server.config_path is empty — "shelf.yaml을 편집한 뒤 재시작하세요" then names no file (C-5, rulings D-33/E-3/OQ-3)`)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("server.config_path = %q, want an absolute path", got)
	}
	if filepath.Base(got) != config.FileName {
		t.Errorf("server.config_path = %q, want a path ending in %q", got, config.FileName)
	}
	if got != e.cfg.FilePath {
		t.Errorf("server.config_path = %q, want the loaded configuration file %q", got, e.cfg.FilePath)
	}

	// The case the field exists for: the binary was started from the directory
	// that holds the file, so the loader recorded a relative path.
	absolute := e.cfg.FilePath
	e.cfg.FilePath = filepath.Join(".", config.FileName)
	relative := decodeBody[Settings](t, e.get("/api/settings"), http.StatusOK).Server.ConfigPath
	if !filepath.IsAbs(relative) {
		t.Errorf("with FilePath = %q the response carried %q; want it resolved against the working directory",
			e.cfg.FilePath, relative)
	}
	if filepath.Base(relative) != config.FileName {
		t.Errorf("server.config_path = %q, want a path ending in %q", relative, config.FileName)
	}
	// Resolving is a read: the field every error message is prefixed with must
	// come back untouched.
	if e.cfg.FilePath != filepath.Join(".", config.FileName) {
		t.Errorf("Config.FilePath = %q after the request; the response must not mutate it", e.cfg.FilePath)
	}
	e.cfg.FilePath = absolute
}

// impl-plan §4 rule 5 — unknown *query* parameters are ignored. A stray
// tracking parameter must not break a page load.
func TestUnknownQueryParams_areIgnored(t *testing.T) {
	e := newEnv(t)
	w := e.get("/api/series?utm_source=newsletter&nonsense=1&limit=2")
	list := decodeBody[SeriesListResponse](t, w, http.StatusOK)
	if list.Limit != 2 {
		t.Errorf("limit = %d, want 2 — the known parameter must still apply", list.Limit)
	}
}

// arch §7.5 — an unknown *value* of a known parameter is 400 with
// `detail: {param}`.
func TestSeriesList_rejectedParameters(t *testing.T) {
	e := newEnv(t)

	cases := []struct{ query, param string }{
		{"sort=recentt", "sort"},
		{"order=descending", "order"},
		{"status=encrypted", "status"}, // an ItemStatus, but not a filter value
		{"progress=finished", "progress"},
		{"scope=reading", "scope"}, // A-8: deliberately not accepted here
		{"scope=done", "scope"},
		{"scope=manga", "scope"}, // nor is a root name
		{"limit=0", "limit"},     // the count idiom is limit=1
		{"limit=201", "limit"},
		{"limit=-1", "limit"},
		{"limit=many", "limit"},
		{"offset=-1", "offset"},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			w := e.get("/api/series?" + tc.query)
			body := errorBody(t, w, http.StatusBadRequest, CodeBadRequest)
			if body.Detail["param"] != tc.param {
				t.Errorf("detail.param = %v, want %q", body.Detail["param"], tc.param)
			}
		})
	}
}

// FR-LIB-003/004/005/007 and amendments A-4/A-8 — the filters compose.
func TestSeriesList_filters(t *testing.T) {
	e := newEnv(t)

	cases := []struct {
		name  string
		query string
		want  []string
	}{
		{"default is every enabled root", "", []string{seriesCloverPath, seriesFolderPath, seriesPDFPath, seriesBrokenPath}},
		{"root filter", "root=manga", []string{seriesCloverPath, seriesFolderPath, seriesPDFPath, seriesBrokenPath}},
		{"unknown root matches nothing", "root=nope", nil},
		{"status ok", "status=ok", []string{seriesCloverPath, seriesFolderPath, seriesPDFPath}},
		{"status error", "status=error", []string{seriesBrokenPath}},
		{"progress reading", "progress=reading", []string{seriesFolderPath}},
		{"progress unread", "progress=unread", []string{seriesCloverPath, seriesPDFPath, seriesBrokenPath}},
		{"scope added", "scope=added", []string{seriesFolderPath}},
		{"scope all is the default", "scope=all", []string{seriesCloverPath, seriesFolderPath, seriesPDFPath, seriesBrokenPath}},
		{"substring search", "q=군계", []string{seriesFolderPath}},
		{"choseong search", "q=ㅁㅅ", []string{seriesPDFPath}},
		{"filters compose", "scope=added&status=ok&progress=reading", []string{seriesFolderPath}},
		{"paging", "limit=1&offset=1", []string{seriesFolderPath}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := e.get("/api/series?" + tc.query)
			list := decodeBody[SeriesListResponse](t, w, http.StatusOK)
			got := make([]string, 0, len(list.Items))
			for _, item := range list.Items {
				got = append(got, item.Path)
			}
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Errorf("items = %v, want %v", got, tc.want)
			}
		})
	}
}

// Amendment A-8 — the 최근 추가 badge is `total` from `?scope=added&limit=1`.
// There is no separate count endpoint, and `total` is the pre-pagination match
// count for every filter combination.
func TestSeriesList_countIdiom(t *testing.T) {
	e := newEnv(t)

	cases := []struct {
		query string
		total int
	}{
		{"limit=1", 4},                  // 전체 시리즈
		{"progress=reading&limit=1", 1}, // 읽는 중
		{"scope=added&limit=1", 1},      // 최근 추가
		{"progress=done&limit=1", 0},    // 완독
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			w := e.get("/api/series?" + tc.query)
			list := decodeBody[SeriesListResponse](t, w, http.StatusOK)
			if list.Total != tc.total {
				t.Errorf("total = %d, want %d", list.Total, tc.total)
			}
			if len(list.Items) > 1 {
				t.Errorf("items = %d, want at most 1 for the count idiom", len(list.Items))
			}
		})
	}

	// A count endpoint would be a second implementation of the same WHERE
	// clause. Assert it does not exist.
	errorBody(t, e.get("/api/library/counts"), http.StatusNotFound, CodeNotFound)
}

// Amendment A-8 — `scope` does not change the sort defaults. A parameter whose
// default silently depends on another parameter is exactly the kind of thing
// two teams implement differently.
func TestSeriesList_scopeDoesNotChangeTheSortDefault(t *testing.T) {
	e := newEnv(t)

	plain := decodeBody[SeriesListResponse](t, e.get("/api/series?limit=200"), http.StatusOK)
	scoped := decodeBody[SeriesListResponse](t, e.get("/api/series?scope=all&limit=200"), http.StatusOK)
	if len(plain.Items) != len(scoped.Items) {
		t.Fatalf("scope=all changed the result set: %d vs %d", len(plain.Items), len(scoped.Items))
	}
	for i := range plain.Items {
		if plain.Items[i].ID != scoped.Items[i].ID {
			t.Fatalf("scope=all changed the order at %d", i)
		}
	}
}

// arch §7.3 — percent is exactly 0 when books_total is 0, never NaN and never
// null. `encoding/json` refuses to marshal NaN, so the obvious implementation
// would 500 on an empty series, which §4.11 says must stay listed.
// Ruling E-14 — `series.status` is a three-value fold over the series' books,
// and the wire never carries a book-only verdict for a series:
//
//	no books at all              -> "empty"
//	≥1 book, at least one "ok"   -> "ok"
//	≥1 book, none of them "ok"   -> "error"
//
// `empty` is reserved for the first case. A five-volume series where every
// volume is encrypted is not "empty" — it is a series the reader cannot open a
// single page of, and FR-IDX-010 plus design.md 화면 2 require it to say so with
// a reason. Before E-14 that combination was simply undefined: §3.5 allowed
// `ok|empty|error` while §7.3 typed the field `ItemStatus`, whose other two
// values (`encrypted`, `unsupported`) are book-only.
//
// The fold is computed once, by the scanner, and stored; what this test pins is
// the promise §7.3 makes to a client — three values, and a series with nothing
// readable in it reads `error` and carries the reason.
func TestSeriesStatus_isTheThreeValueFold(t *testing.T) {
	e := newEnv(t)

	// The third case needs a series with no books at all, which the fixture
	// tree has none of: arch §4.2's text-novel directories, listed greyed out
	// rather than silently swallowed.
	const emptyPath = "[소설] 책 없는 시리즈"
	emptyID := ids.SeriesID(rootName, emptyPath)
	w := e.idx.Writer(index.WriterOptions{})
	if err := w.UpsertSeries(t.Context(), index.Series{
		ID: emptyID, RootName: rootName, RelPath: emptyPath,
		DisplayName: emptyPath, SortKey: natsort.Key(emptyPath),
		SearchKey: strings.ToLower(emptyPath), ChoseongKey: "ㅊ ㅇㄴ ㅅㄹㅈ",
		Kind: "folder", BookCount: 0, PageCount: 0, TotalBytes: 0,
		Mtime: fixedMtime.Unix(), AddedAt: fixedNow.Unix(),
		Status: "empty", Error: "no readable books", ScanGen: 1,
	}); err != nil {
		t.Fatalf("upserting the bookless series: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("closing the writer: %v", err)
	}

	cases := []struct {
		name, id, want string
		wantReason     bool
	}{
		{"a series with readable volumes", e.seriesFolderID, "ok", false},
		{"a series whose only volume is unreadable", e.seriesBrokenID, "error", true},
		{"a series with no books at all", emptyID, "empty", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := decodeBody[SeriesDetail](t, e.get("/api/series/"+tc.id), http.StatusOK)
			if got.Status != tc.want {
				t.Errorf("status = %q, want %q", got.Status, tc.want)
			}
			if tc.wantReason && (got.Error == nil || *got.Error == "") {
				t.Errorf("status %q carries no reason; design.md 화면 2 requires one", got.Status)
			}
		})
	}

	// And the closed set is closed on the way in too: the two book-only
	// statuses are not series statuses, so `?status=` refuses them rather than
	// quietly matching nothing (TestSeriesList_rejectedParameters pins
	// `encrypted`; `unsupported` is the other half).
	for _, bad := range []string{"encrypted", "unsupported"} {
		body := errorBody(t, e.get("/api/series?status="+bad), http.StatusBadRequest, CodeBadRequest)
		if body.Detail["param"] != "status" {
			t.Errorf("status=%s: detail = %v, want param=status", bad, body.Detail)
		}
	}
}

func TestSeriesProgress_percentIsZeroForAnEmptySeries(t *testing.T) {
	t.Parallel()
	if got := percent(0, 0); got != 0 {
		t.Errorf("percent(0,0) = %v, want 0", got)
	}
	if got := percent(1, 3); got != 33.3 {
		t.Errorf("percent(1,3) = %v, want 33.3 (one decimal place)", got)
	}
	if got := percent(3, 3); got != 100 {
		t.Errorf("percent(3,3) = %v, want 100", got)
	}
}

// arch §8.4 — the security headers are on every response, whatever it is.
func TestSecurityHeaders_onEveryResponse(t *testing.T) {
	e := newEnv(t, withStatic())

	targets := []string{
		"/api/health",
		"/api/series",
		"/api/nope",                              // 404 envelope
		"/api/books/" + e.bookZipID + "/pages/1", // an image body
		"/",                                      // the SPA shell
		"/assets/index-abc123.js",                // a static asset
	}
	for _, target := range targets {
		t.Run(target, func(t *testing.T) {
			w := e.get(target)
			h := w.Header()
			if got := h.Get("X-Content-Type-Options"); got != "nosniff" {
				t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
			}
			if got := h.Get("Referrer-Policy"); got != referrerPolicy {
				t.Errorf("Referrer-Policy = %q, want %q", got, referrerPolicy)
			}
			if got := h.Get("Content-Security-Policy"); got != contentSecurityPolicy {
				t.Errorf("Content-Security-Policy = %q, want the arch §8.4 policy", got)
			}
			if h.Get(requestIDHeader) == "" {
				t.Error("X-Request-Id is missing; arch §7.1 requires it on every response")
			}
		})
	}
}

// arch §7.1 — every response carries a distinct X-Request-Id.
func TestRequestID_isPerRequest(t *testing.T) {
	e := newEnv(t)
	a := e.get("/api/health").Header().Get(requestIDHeader)
	b := e.get("/api/health").Header().Get(requestIDHeader)
	if a == "" || b == "" {
		t.Fatal("X-Request-Id is empty")
	}
	if a == b {
		t.Errorf("two requests shared the id %q", a)
	}
}

// arch §8.4 — JSON bodies are capped at 1 MiB.
func TestBodyCap_oversizedJSONIs400(t *testing.T) {
	e := newEnv(t)
	big := `{"theme":"` + strings.Repeat("x", maxJSONBody+16) + `"}`
	errorBody(t, e.jsonBody(http.MethodPut, "/api/settings", big), http.StatusBadRequest, CodeBadRequest)
}

// A body that is not JSON at all, an empty body where one is required, and two
// concatenated documents are all 400 — the last because accepting the first and
// ignoring the second would quietly write a value the user never sent.
func TestDecoding_malformedBodies(t *testing.T) {
	e := newEnv(t)
	target := "/api/books/" + e.bookZipID + "/progress"

	for _, body := range []string{"", "not json", `{"page":1}{"page":9}`, `{"page":"two"}`, `[]`} {
		t.Run(body, func(t *testing.T) {
			errorBody(t, e.jsonBody(http.MethodPut, target, body), http.StatusBadRequest, CodeBadRequest)
		})
	}
}

// arch §7.6 — POST /api/scan's body is optional.
func TestStartScan_bodyIsOptional(t *testing.T) {
	e := newEnv(t)
	for _, body := range []string{"", "{}", `{"full":true}`, `{"roots":["manga"],"full":false}`} {
		t.Run("body="+body, func(t *testing.T) {
			w := e.jsonBody(http.MethodPost, "/api/scan", body)
			decodeBody[RunAccepted](t, w, http.StatusAccepted)
		})
	}
}

// arch §7.10 — a scan already running is 409 conflict.
func TestStartScan_busyIs409(t *testing.T) {
	e := newEnv(t)
	e.scan.startErr = scanner.ErrBusy
	errorBody(t, e.jsonBody(http.MethodPost, "/api/scan", `{}`), http.StatusConflict, CodeConflict)
}

// arch §7.10 — a `roots[]` name that is not in the configuration is `400`, not
// `404`.
//
// The split is the whole of what makes A-11 / R1's `404` readable: a name that
// was never configured is a client that built a request out of nothing and
// cannot be made to work by retrying, while a name this process *removed* names
// a resource that has gone (see TestDeleteRoot_takesEffectBeforeTheRestart).
// Both statuses were undocumented until 2026-07-30 and R1 widened the surface
// without adding either.
func TestStartScan_unknownRootIs400(t *testing.T) {
	e := newEnv(t)
	e.scan.startErr = scanner.ErrUnknownRoot
	w := e.jsonBody(http.MethodPost, "/api/scan", `{"roots":["nosuchroot"]}`)
	body := errorBody(t, w, http.StatusBadRequest, CodeBadRequest)
	if body.Detail["param"] != "roots" {
		t.Errorf("detail.param = %v, want %q", body.Detail["param"], "roots")
	}
}

// arch §7.5 — the per-series rescan targets exactly that series, so a partial
// visit can never sweep rows the run did not look at.
func TestSeriesRescan_targetsTheSeries(t *testing.T) {
	e := newEnv(t)
	w := e.do(http.MethodPost, "/api/series/"+e.seriesFolderID+"/rescan", nil)
	decodeBody[RunAccepted](t, w, http.StatusAccepted)

	if len(e.scan.lastReq.Series) != 1 {
		t.Fatalf("scanner request Series = %v, want exactly one entry", e.scan.lastReq.Series)
	}
	if got := e.scan.lastReq.Series[0].RelPath; got != seriesFolderPath {
		t.Errorf("rescan targeted %q, want %q", got, seriesFolderPath)
	}
	if got := e.scan.lastReq.Roots; len(got) != 1 || got[0] != rootName {
		t.Errorf("rescan roots = %v, want [%q]", got, rootName)
	}
}

// arch §7.10 — cancel is idempotent and answers 204 whether or not a scan was
// running: "there is no scan now" is the state the caller asked for either way.
func TestCancelScan_isIdempotent(t *testing.T) {
	e := newEnv(t)
	for range 2 {
		if w := e.do(http.MethodPost, "/api/scan/cancel", nil); w.Code != http.StatusNoContent {
			t.Fatalf("cancel = %d, want 204", w.Code)
		}
	}
	if e.scan.cancels != 2 {
		t.Errorf("scanner.Cancel called %d times, want 2", e.scan.cancels)
	}
}

// arch §7.10 — the log query's bounds.
func TestScanLog_parameters(t *testing.T) {
	e := newEnv(t)

	if w := e.get("/api/scan/log?level=warn"); w.Code != http.StatusOK {
		t.Fatalf("level=warn = %d, want 200: %s", w.Code, w.Body.String())
	}
	body := decodeBody[ScanLogResponse](t, e.get("/api/scan/log?level=warn"), http.StatusOK)
	if len(body.Items) != 1 {
		t.Fatalf("items = %d, want the one seeded warn row", len(body.Items))
	}
	if body.Items[0].RootName == nil || *body.Items[0].RootName != rootName {
		t.Errorf("root_name = %v, want %q", body.Items[0].RootName, rootName)
	}

	if w := e.get("/api/scan/log?level=trace"); w.Code != http.StatusBadRequest {
		t.Errorf("level=trace = %d, want 400", w.Code)
	}
	if w := e.get("/api/scan/log?limit=0"); w.Code != http.StatusBadRequest {
		t.Errorf("limit=0 = %d, want 400", w.Code)
	}
}

// FR-THM-008 — purge takes a closed enumeration, never a path.
func TestCachePurge_kindValidation(t *testing.T) {
	e := newEnv(t)
	for _, kind := range []string{"thumbs", "pdf", "wazero", "all"} {
		t.Run(kind, func(t *testing.T) {
			decodeBody[PurgeResult](t, e.do(http.MethodDelete, "/api/cache?kind="+kind, nil), http.StatusOK)
		})
	}
	for _, kind := range []string{"..", "/etc", "everything", "thumbs/../.."} {
		t.Run("rejects "+kind, func(t *testing.T) {
			w := e.do(http.MethodDelete, "/api/cache?kind="+kind, nil)
			body := errorBody(t, w, http.StatusBadRequest, CodeBadRequest)
			if body.Detail["param"] != "kind" {
				t.Errorf("detail.param = %v, want \"kind\"", body.Detail["param"])
			}
		})
	}
}

// FR-LIB-010 — the continue shelf's bounds.
func TestContinue_limitBounds(t *testing.T) {
	e := newEnv(t)
	decodeBody[ContinueResponse](t, e.get("/api/continue"), http.StatusOK)
	decodeBody[ContinueResponse](t, e.get("/api/continue?limit=50"), http.StatusOK)
	for _, q := range []string{"limit=0", "limit=51", "limit=x"} {
		if w := e.get("/api/continue?" + q); w.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", q, w.Code)
		}
	}
}

// FR-VWR-009 / FR-STT-001 — progress round-trips, clamps and auto-completes.
func TestProgress_writeReadDelete(t *testing.T) {
	e := newEnv(t)
	target := "/api/books/" + e.bookDirID + "/progress"

	got := decodeBody[Progress](t, e.jsonBody(http.MethodPut, target, `{"page":1}`), http.StatusOK)
	if got.LastPage != 1 || got.Completed {
		t.Fatalf("progress = %+v, want page 1 and not completed", got)
	}

	// FR-VWR-012 — completed is automatic when page reaches page_count.
	got = decodeBody[Progress](t, e.jsonBody(http.MethodPut, target, `{"page":2}`), http.StatusOK)
	if !got.Completed {
		t.Errorf("reaching the last page did not set completed: %+v", got)
	}

	// ...and can be overridden explicitly.
	got = decodeBody[Progress](t, e.jsonBody(http.MethodPut, target, `{"page":2,"completed":false}`), http.StatusOK)
	if got.Completed {
		t.Errorf("an explicit completed:false was ignored: %+v", got)
	}

	// The page is clamped server-side rather than rejected: the file may have
	// shrunk since the client loaded it, and losing the reader's place over a
	// race is worse than saving an approximate one.
	got = decodeBody[Progress](t, e.jsonBody(http.MethodPut, target, `{"page":9000}`), http.StatusOK)
	if got.LastPage != 2 {
		t.Errorf("last_page = %d, want the clamp to 2", got.LastPage)
	}

	// There is no page 0 (impl-plan §4 rule 1).
	errorBody(t, e.jsonBody(http.MethodPut, target, `{"page":0}`), http.StatusBadRequest, CodeBadRequest)
	errorBody(t, e.jsonBody(http.MethodPut, target, `{"page":-3}`), http.StatusBadRequest, CodeBadRequest)
	errorBody(t, e.jsonBody(http.MethodPut, target, `{"completed":true}`), http.StatusBadRequest, CodeBadRequest)

	if w := e.do(http.MethodDelete, target, nil); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE progress = %d, want 204", w.Code)
	}
	// Deleting twice is still 204: mark-as-unread is idempotent.
	if w := e.do(http.MethodDelete, target, nil); w.Code != http.StatusNoContent {
		t.Fatalf("second DELETE progress = %d, want 204", w.Code)
	}
	book := decodeBody[BookDetail](t, e.get("/api/books/"+e.bookDirID), http.StatusOK)
	if book.Progress != nil {
		t.Errorf("progress survived the delete: %+v", book.Progress)
	}
}

// arch §7.6 — `page_count === 0` means "length unknown": the clamp is [1, ∞),
// it is NOT a 400, and the auto-complete rule cannot fire.
func TestProgress_unknownLengthClampsOnlyBelow(t *testing.T) {
	e := newEnv(t)
	target := "/api/books/" + e.bookBrokenID + "/progress"

	got := decodeBody[Progress](t, e.jsonBody(http.MethodPut, target, `{"page":42}`), http.StatusOK)
	if got.LastPage != 42 {
		t.Errorf("last_page = %d, want 42 — an unknown length has no upper bound", got.LastPage)
	}
	if got.Completed {
		t.Error("completed fired with page_count 0; the auto rule cannot apply")
	}
	if got.Stale {
		t.Error("stale = true for a book that never had a known length")
	}
}

// arch §7.3 — `stale` reports that the file changed under the reader.
func TestProgress_staleWhenTheIndexLengthMoved(t *testing.T) {
	t.Parallel()
	if isStale(3, 3) {
		t.Error("stale = true when the lengths agree")
	}
	if !isStale(3, 5) {
		t.Error("stale = false when the recorded length no longer matches the index")
	}
	if isStale(0, 5) {
		t.Error("stale = true for a book whose length was never known")
	}
}

// FR-VWR-002 — per-book preferences are three-state: absent leaves the override
// alone, null clears it, a value sets it.
func TestPrefs_threeStatePatch(t *testing.T) {
	e := newEnv(t)
	target := "/api/books/" + e.bookZipID + "/prefs"

	base := decodeBody[BookPrefs](t, e.get(target), http.StatusOK)
	if base.IsOverride {
		t.Fatalf("a book with no stored prefs reported is_override: %+v", base)
	}

	set := decodeBody[BookPrefs](t, e.jsonBody(http.MethodPut, target, `{"reading_direction":"rtl"}`), http.StatusOK)
	if set.ReadingDirection != "rtl" || !set.IsOverride {
		t.Fatalf("prefs = %+v, want rtl with is_override", set)
	}
	if set.FitMode != base.FitMode {
		t.Errorf("an absent field changed fit_mode: %q -> %q", base.FitMode, set.FitMode)
	}

	// An omitted field leaves the stored override alone.
	again := decodeBody[BookPrefs](t, e.jsonBody(http.MethodPut, target, `{"display_mode":"spread"}`), http.StatusOK)
	if again.ReadingDirection != "rtl" {
		t.Errorf("an omitted field cleared the stored override: %+v", again)
	}

	// An explicit null clears it and falls back to the global default.
	cleared := decodeBody[BookPrefs](t, e.jsonBody(http.MethodPut, target, `{"reading_direction":null}`), http.StatusOK)
	if cleared.ReadingDirection != base.ReadingDirection {
		t.Errorf("null did not restore the default: %+v", cleared)
	}
	if !cleared.IsOverride {
		t.Error("display_mode is still overridden, so is_override must stay true")
	}
}

// Conflict resolutions C-1 and C-2 — the wire values are `spread` and
// `contain`. `double` and `screen` do not exist anywhere in the product.
func TestFrozenEnums_spreadAndContain(t *testing.T) {
	e := newEnv(t)
	target := "/api/books/" + e.bookZipID + "/prefs"

	for _, ok := range []string{`{"display_mode":"spread"}`, `{"fit_mode":"contain"}`} {
		if w := e.jsonBody(http.MethodPut, target, ok); w.Code != http.StatusOK {
			t.Errorf("%s = %d, want 200: %s", ok, w.Code, w.Body.String())
		}
	}
	for _, bad := range []string{`{"display_mode":"double"}`, `{"fit_mode":"screen"}`} {
		if w := e.jsonBody(http.MethodPut, target, bad); w.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400 — the frozen enum has no such value", bad, w.Code)
		}
	}
	for _, bad := range []string{`{"library_sort":"vols"}`, `{"display_mode":"double"}`, `{"fit_mode":"screen"}`} {
		if w := e.jsonBody(http.MethodPut, "/api/settings", bad); w.Code != http.StatusBadRequest {
			t.Errorf("settings %s = %d, want 400", bad, w.Code)
		}
	}
}

// Ruling E-15 — `library_sort` is the closed FR-LIB-004 sort set, not the free
// string §7.8 used to type it. Every member round-trips, and anything outside
// the set is a clean `400 bad_request` naming the field: a client that invents
// `vols` finds out on the write, instead of storing a value the library screen
// cannot honour and silently falling back to `name` on every later read.
func TestSettings_librarySortIsAClosedUnion(t *testing.T) {
	e := newEnv(t)

	// The wire values of §7.5's `sort` parameter, which is the same set.
	for _, sort := range []string{
		index.SortName, index.SortMtime, index.SortRecent,
		index.SortSize, index.SortBooks, index.SortAdded,
	} {
		body := `{"library_sort":` + strconv.Quote(sort) + `}`
		got := decodeBody[Settings](t, e.jsonBody(http.MethodPut, "/api/settings", body), http.StatusOK)
		if got.LibrarySort != sort {
			t.Errorf("PUT %s then library_sort = %q, want %q", body, got.LibrarySort, sort)
		}
	}

	// Plausible near-misses: the ui-spec's own names (C-3), a case variant, a
	// stray space, and empty.
	for _, bad := range []string{"vols", "read", "Name", "added ", "", "recentt"} {
		body := `{"library_sort":` + strconv.Quote(bad) + `}`
		got := errorBody(t, e.jsonBody(http.MethodPut, "/api/settings", body), http.StatusBadRequest, CodeBadRequest)
		if got.Detail["field"] != "library_sort" {
			t.Errorf("PUT %s: detail = %v, want field=library_sort", body, got.Detail)
		}
	}

	// A rejected write changes nothing — the last accepted value is still there.
	if after := decodeBody[Settings](t, e.get("/api/settings"), http.StatusOK); after.LibrarySort != index.SortAdded {
		t.Errorf("library_sort = %q after six rejected writes, want the last accepted value %q",
			after.LibrarySort, index.SortAdded)
	}
}

// arch §7.8 — PUT is partial and only the sent keys change.
func TestSettings_partialUpdate(t *testing.T) {
	e := newEnv(t)

	before := decodeBody[Settings](t, e.get("/api/settings"), http.StatusOK)
	after := decodeBody[Settings](t, e.jsonBody(http.MethodPut, "/api/settings", `{"theme":"dark","prefetch":7}`), http.StatusOK)

	if after.Theme != "dark" || after.Prefetch != 7 {
		t.Fatalf("settings = %+v, want theme dark and prefetch 7", after)
	}
	if after.LibraryView != before.LibraryView || after.FitMode != before.FitMode {
		t.Errorf("an unsent key changed: %+v -> %+v", before, after)
	}

	// The server mirror is unchanged and still reports the YAML.
	if after.Server.RecentlyAddedDays != 14 {
		t.Errorf("server.recently_added_days = %d, want 14 (amendment A-8)", after.Server.RecentlyAddedDays)
	}
	if after.Server.BasePath != "" {
		t.Errorf("server.base_path = %q, want empty", after.Server.BasePath)
	}

	// It persists.
	reread := decodeBody[Settings](t, e.get("/api/settings"), http.StatusOK)
	if reread.Theme != "dark" || reread.Prefetch != 7 {
		t.Errorf("settings did not persist: %+v", reread)
	}

	// Out-of-range values are refused.
	for _, bad := range []string{`{"prefetch":21}`, `{"prefetch":-1}`, `{"theme":"neon"}`, `{"library_scope":""}`} {
		if w := e.jsonBody(http.MethodPut, "/api/settings", bad); w.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", bad, w.Code)
		}
	}

	// A-5: library_scope may be a root name, so it is validated loosely rather
	// than against a closed enum.
	if w := e.jsonBody(http.MethodPut, "/api/settings", `{"library_scope":"manga"}`); w.Code != http.StatusOK {
		t.Errorf("library_scope=manga = %d, want 200", w.Code)
	}
}

// FR-VWR-002 — the global default flows into a book's effective prefs.
func TestSettings_defaultsFlowIntoBookPrefs(t *testing.T) {
	e := newEnv(t)
	if w := e.jsonBody(http.MethodPut, "/api/settings", `{"reading_direction":"rtl","fit_mode":"width"}`); w.Code != http.StatusOK {
		t.Fatalf("PUT settings = %d: %s", w.Code, w.Body.String())
	}
	prefs := decodeBody[BookPrefs](t, e.get("/api/books/"+e.bookDirID+"/prefs"), http.StatusOK)
	if prefs.ReadingDirection != "rtl" || prefs.FitMode != "width" {
		t.Errorf("prefs = %+v, want the new global defaults", prefs)
	}
	if prefs.IsOverride {
		t.Error("is_override = true for a book with no stored override")
	}
}

// FR-STT-004 — export/import round-trips and refuses a foreign id scheme.
func TestProgressExportImport(t *testing.T) {
	e := newEnv(t)

	w := e.get("/api/progress/export")
	if cd := w.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment") {
		t.Errorf("Content-Disposition = %q, want an attachment", cd)
	}
	var doc map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("decoding the export: %v", err)
	}
	if doc["format"] != "shelf-progress/1" || doc["id_version"] != "shelf-id/1" {
		t.Errorf("export envelope = %v", doc)
	}

	// Re-importing the document we just exported is a no-op merge.
	res := decodeBody[map[string]int](t, e.do(http.MethodPost, "/api/progress/import", strings.NewReader(w.Body.String())), http.StatusOK)
	if _, ok := res["imported"]; !ok {
		t.Errorf("import result = %v, want imported/skipped/conflicts", res)
	}

	bad := `{"format":"shelf-progress/1","exported_at":1,"id_version":"shelf-id/9","items":[],"prefs":[]}`
	errorBody(t, e.jsonBody(http.MethodPost, "/api/progress/import", bad), http.StatusBadRequest, CodeBadRequest)

	if w := e.jsonBody(http.MethodPost, "/api/progress/import?strategy=nonsense", `{}`); w.Code != http.StatusBadRequest {
		t.Errorf("strategy=nonsense = %d, want 400", w.Code)
	}
}

// arch §7.4 — ?verbose=1 adds the pool counters and the plain body does not
// change shape.
func TestHealth_verbose(t *testing.T) {
	e := newEnv(t)

	plain := decodeBody[Health](t, e.get("/api/health"), http.StatusOK)
	if plain.Verbose != nil {
		t.Error("the plain health body carries the verbose block")
	}
	if !plain.OK || plain.UptimeMs != 90000 {
		t.Errorf("health = %+v, want ok with a 90 s uptime", plain)
	}

	verbose := decodeBody[Health](t, e.get("/api/health?verbose=1"), http.StatusOK)
	if verbose.Verbose == nil || verbose.Verbose.ArchivePool == nil {
		t.Fatalf("verbose health = %+v, want the pool counters", verbose)
	}
}

// The `thumbs` block of `?verbose=1` carries thumbs.Stats field for field, and
// in particular it carries `active` and `inflight`.
//
// Those two are not decoration. With `cover_depth` and `page_depth` they are the
// conjunction thumbs.Service.idle() tests, and scripts/e2e-assert.py waits on
// all four before it judges the covers the scan pre-generated — the sleep it
// replaced let 3-4 of 36 cover files be missing while the scan reported 36/36
// (internal/app/covers.go derives `done` from queue depth). A health.go that
// dropped `active` would make that wait return immediately and always pass, so
// the gate would be watching nothing.
//
// The values are pinned rather than observed because a service at rest reports
// zero for everything, and zero cannot tell a correct mirror from one that
// transposed two fields or forgot the last two. Every number below is distinct
// for that reason.
func TestHealthVerbose_thumbCountersMirrorTheServiceFieldForField(t *testing.T) {
	t.Parallel()
	e := newEnv(t)

	e.dims.pinStats(thumbs.Stats{
		Hits:       11,
		Queued:     22,
		Dropped:    33,
		Generated:  44,
		Failed:     55,
		Decodes:    66, // not mirrored; a copy that leaked it into a JSON field
		Negative:   77, // would show up as a mismatch below
		DimsPages:  88,
		DimsBytes:  99,
		CoverDepth: 3,
		PageDepth:  5,
		Active:     2,
		Inflight:   4,
	})

	got := decodeBody[Health](t, e.get("/api/health?verbose=1"), http.StatusOK)
	if got.Verbose == nil {
		t.Fatalf("verbose health = %+v, want the verbose block", got)
	}
	want := ThumbCounter{
		Hits: 11, Queued: 22, Dropped: 33, Generated: 44, Failed: 55,
		CoverDepth: 3, PageDepth: 5, Active: 2, Inflight: 4,
	}
	if got.Verbose.ThumbCounter != want {
		t.Errorf("thumbs counters = %+v, want %+v", got.Verbose.ThumbCounter, want)
	}

	// The JSON names are what the E2E polls, so assert the wire, not just the
	// struct: a renamed tag would keep the comparison above green.
	var wire struct {
		Verbose struct {
			Thumbs map[string]json.RawMessage `json:"thumbs"`
		} `json:"verbose"`
	}
	rec := e.get("/api/health?verbose=1")
	if err := json.Unmarshal(rec.Body.Bytes(), &wire); err != nil {
		t.Fatalf("decoding the verbose body: %v", err)
	}
	for key, value := range map[string]string{
		"cover_depth": "3", "page_depth": "5", "active": "2", "inflight": "4",
	} {
		raw, ok := wire.Verbose.Thumbs[key]
		if !ok {
			t.Errorf("the thumbs block has no %q; scripts/e2e-assert.py polls it by that name", key)
			continue
		}
		if string(raw) != value {
			t.Errorf("thumbs.%s = %s, want %s", key, raw, value)
		}
	}
}

// NFR-OPS-005 — a panic in a handler is recovered, logged and answered with the
// envelope rather than a dropped connection.
func TestPanicInHandler_isRecoveredAsA500Envelope(t *testing.T) {
	e := newEnv(t)
	panicking := e.srv.h(func(http.ResponseWriter, *http.Request) error {
		panic("boom")
	})
	wrapped := e.srv.withObservability(panicking)

	w := e.do(http.MethodGet, "/api/health", nil) // warm the harness
	_ = w

	rec := newRecorder()
	wrapped.ServeHTTP(rec, requestFor("/api/anything"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	var env ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("the panic response is not the envelope: %v; body %s", err, rec.Body.String())
	}
	if env.Error.Code != CodeInternal {
		t.Errorf("code = %q, want %q", env.Error.Code, CodeInternal)
	}
}
