//go:build integration

// Package integration is the suite of impl-plan §6.2: the whole product, wired
// exactly as `cmd/shelf` wires it, running against the **real** collection.
//
//	make test-int SHELF_TEST_ROOT="/mnt/big-data/pds/taison-data/02. books/01. mangga"
//
// With SHELF_TEST_ROOT unset every test skips, so `make test-int` is a no-op on
// a machine with no media volume rather than a failure.
//
// # Nothing is copied
//
// The root points at the collection itself and `scan.include_globs` narrows it
// to ten of impl-plan §6.3's curated series (~5.1 GB). Symlinks cannot do
// this — os.Root refuses any symlink that escapes its root — and copying would
// destroy the 2012–2018 mtimes that content_version, the incremental scan and
// FR-THM-006 all key off (D-48). I-9 proves the collection is untouched
// afterwards.
//
// # Everything goes through HTTP
//
// The tests drive the server the way a browser does, so what they assert is
// what a user gets. The one thing they reach around HTTP for is the
// filesystem, in I-9.
package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"shelf/internal/app"
	"shelf/internal/config"
)

// Ten of impl-plan §6.3's curated series, exact names.
//
// A deliberate SUBSET of `scripts/e2e-config.sh`'s CURATED, which has grown to
// fifteen: the rounds this suite drives — a full scan, a whole-volume stream, an
// index-and-cache wipe — cost minutes per series, and the five it leaves out
// (D-70/D-71/D-72's RAR, .7z, .hv3 and nested-container shapes) are covered by
// the e2e round instead. That gap is tracked; adding them here is not free,
// because several of them are shapes this build refuses on purpose and the
// broken-book counts in scan_test.go are written against these ten.
//
// **Every name here must appear verbatim in CURATED**, and `contractcheck`'s
// checkCuratedSeries now enforces exactly that, one way. It was added after
// this list was found still carrying the `[만화] ` prefix the collection had
// dropped six sessions earlier: `include_globs` matched nothing, the suite
// indexed an empty library, and every acceptance test failed — except the
// NFR-PRF-005 memory check, which measured a server holding nothing and passed.
var curated = []string{
	"Clover 클로버 (총4권)",
	"상처를 쫓는자 1-11 (완) 이케가미 료이치",
	"자살도114-122",
	"바퀴.zip",
	"강철의 연금술사 1~27권 완결",
	"군계 1~25",
	"디엔엔젤 1-13권 연재중",
	"미생 1~9 (완결 pdf)",
	"배틀로얄 1~15 [완결].zip",
	"엔젤하트 전32권 완결.zip",
}

const (
	clover       = "Clover 클로버 (총4권)"
	wounds       = "상처를 쫓는자 1-11 (완) 이케가미 료이치"
	suicide      = "자살도114-122"
	wheel        = "바퀴.zip"
	fma          = "강철의 연금술사 1~27권 완결"
	gungye       = "군계 1~25"
	dnangel      = "디엔엔젤 1-13권 연재중"
	misaeng      = "미생 1~9 (완결 pdf)"
	battleRoyale = "배틀로얄 1~15 [완결].zip"
	angelHeart   = "엔젤하트 전32권 완결.zip"
)

// escapeGlob turns a literal name into a path.Match pattern. `[` opens a
// character class, so the literal bracket is written as the one-member class
// `[[]` — impl-plan §6.3's escaping. The raw form compiles and matches nothing,
// which would index an empty library.
func escapeGlob(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '[':
			b.WriteString("[[]")
		case '*':
			b.WriteString("[*]")
		case '?':
			b.WriteString("[?]")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// testRoot is the collection, or "" when the suite must skip.
func testRoot() string { return os.Getenv("SHELF_TEST_ROOT") }

func requireRoot(t *testing.T) string {
	t.Helper()
	root := testRoot()
	if root == "" {
		t.Skip("SHELF_TEST_ROOT is unset; see impl-plan §6.2")
	}
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		t.Skipf("SHELF_TEST_ROOT=%q is not a readable directory: %v", root, err)
	}
	return root
}

// writeConfig renders the curated-subset configuration into stateDir.
func writeConfig(t *testing.T, root, stateDir string) *config.Config {
	t.Helper()
	var globs strings.Builder
	for _, name := range curated {
		fmt.Fprintf(&globs, "    - %q\n", escapeGlob(name))
	}
	body := fmt.Sprintf(`
server: {listen: "127.0.0.1", port: 8792, shutdown_grace: "5s"}
roots:
  - {name: "mangga", label: "만화 (integration subset)", path: %q}
storage: {data_dir: %q, cache_dir: %q}
scan:
  on_start: false
  workers: 8
  include_globs:
%s
thumbnails: {widths: [120, 240, 400, 640], workers: 4}
pdf: {enabled: true, workers: 1}
log: {level: "warn", format: "text", http_requests: false}
`, root, filepath.Join(stateDir, "data"), filepath.Join(stateDir, "cache"), globs.String())

	path := filepath.Join(stateDir, "shelf.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing the configuration: %v", err)
	}
	cfg, err := config.Load(config.Options{ExplicitPath: path})
	if err != nil {
		t.Fatalf("loading the configuration:\n%v", err)
	}
	return cfg
}

// server is a running SHELF plus the HTTP helpers the tests use.
type server struct {
	t    *testing.T
	app  *app.App
	base string
	stop func()
}

// startServer brings the product up on a random port against stateDir.
//
// The lifetime is context.Background() rather than t.Context(): the shared
// server outlives the test that created it, and a request issued from the
// second test through a context the first test cancelled would fail for a
// reason that has nothing to do with the product.
func startServer(t *testing.T, root, stateDir string, rebuildIndex bool) *server {
	t.Helper()
	cfg := writeConfig(t, root, stateDir)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	a, err := app.New(context.Background(), app.Options{
		Config:       cfg,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError})),
		Listener:     ln,
		RebuildIndex: rebuildIndex,
	})
	if err != nil {
		_ = ln.Close()
		t.Fatalf("app.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()

	s := &server{t: t, app: a, base: "http://" + a.Addr()}
	s.stop = func() {
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Error("the server did not shut down within 30 s")
		}
		if err := a.Close(); err != nil {
			t.Errorf("closing the app: %v", err)
		}
	}
	s.waitReady()
	return s
}

func (s *server) waitReady() {
	s.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := http.Get(s.base + "/api/health"); err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	s.t.Fatal("the server never answered GET /api/health")
}

// do performs a request and returns the status, body and headers.
func (s *server) do(method, path, body string) (int, []byte, http.Header) {
	s.t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, s.base+path, rdr)
	if err != nil {
		s.t.Fatalf("%s %s: %v", method, path, err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		s.t.Fatalf("reading %s: %v", path, err)
	}
	return resp.StatusCode, data, resp.Header
}

// get decodes a 200 response into out.
func (s *server) get(path string, out any) {
	s.t.Helper()
	status, body, _ := s.do(http.MethodGet, path, "")
	if status != http.StatusOK {
		s.t.Fatalf("GET %s = %d: %s", path, status, truncate(body))
	}
	if out == nil {
		return
	}
	if err := json.Unmarshal(body, out); err != nil {
		s.t.Fatalf("decoding %s: %v\n%s", path, err, truncate(body))
	}
}

func truncate(b []byte) string {
	if len(b) > 400 {
		return string(b[:400]) + "…"
	}
	return string(b)
}

// scan starts a scan and waits for it to finish, returning how long it took.
func (s *server) scan(full bool, limit time.Duration) time.Duration {
	s.t.Helper()
	body := `{}`
	if full {
		body = `{"full":true}`
	}
	status, resp, _ := s.do(http.MethodPost, "/api/scan", body)
	if status != http.StatusAccepted {
		s.t.Fatalf("POST /api/scan = %d: %s", status, truncate(resp))
	}
	var accepted struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(resp, &accepted); err != nil || accepted.RunID == "" {
		s.t.Fatalf("POST /api/scan returned no run id: %s", truncate(resp))
	}

	// The run id is checked, not just the state. `idle` is both "not started
	// yet" and "finished" — arch §7.10 has no "done" — so reading the first
	// `idle` as "finished" is only honest because Scanner.Start publishes this
	// run's snapshot before the 202 is written: every status this loop can
	// reach therefore belongs to this run. Comparing the id keeps that honest
	// from *this* side too, so a regression of that ordering surfaces as a
	// timeout here rather than as a suite that silently asserts against the
	// previous scan's index.
	start := time.Now()
	for time.Since(start) < limit {
		var st scanStatus
		s.get("/api/scan/status", &st)
		if st.State == "idle" && st.RunID == accepted.RunID {
			return time.Since(start)
		}
		time.Sleep(500 * time.Millisecond)
	}
	s.t.Fatalf("the scan did not finish within %s", limit)
	return 0
}

// ---------------------------------------------------------------------------
// The contract, as far as these tests need it (arch §7.3)
// ---------------------------------------------------------------------------

type seriesSummary struct {
	ID         string `json:"id"`
	RootName   string `json:"root_name"`
	Name       string `json:"name"`
	Path       string `json:"path"`
	Kind       string `json:"kind"`
	BookCount  int    `json:"book_count"`
	PageCount  int    `json:"page_count"`
	TotalBytes int64  `json:"total_bytes"`
	Status     string `json:"status"`
	Error      string `json:"error"`
	HasCover   bool   `json:"has_cover"`
	CoverCV    string `json:"cover_cv"`
}

type bookSummary struct {
	ID         string `json:"id"`
	SeriesID   string `json:"series_id"`
	Name       string `json:"name"`
	Path       string `json:"path"`
	Kind       string `json:"kind"`
	Ord        int    `json:"ord"`
	PageCount  int    `json:"page_count"`
	TotalBytes int64  `json:"total_bytes"`
	FileSize   int64  `json:"file_size"`
	CV         string `json:"cv"`
	Status     string `json:"status"`
	Error      string `json:"error"`
}

type pageInfo struct {
	N    int    `json:"n"`
	Name string `json:"name"`
	Ext  string `json:"ext"`
	Size int64  `json:"size"`
}

type seriesList struct {
	Items  []seriesSummary `json:"items"`
	Total  int             `json:"total"`
	Offset int             `json:"offset"`
	Limit  int             `json:"limit"`
}

type seriesDetail struct {
	seriesSummary
	Books    []bookSummary `json:"books"`
	Encoding *string       `json:"encoding"`
}

type bookDetail struct {
	bookSummary
	Progress   *progressBody `json:"progress"`
	SeriesName string        `json:"series_name"`
	Pages      []pageInfo    `json:"pages"`
	DimsState  string        `json:"dims_state"`
	PrevBookID *string       `json:"prev_book_id"`
	NextBookID *string       `json:"next_book_id"`
	Prefs      struct {
		ReadingDirection string `json:"reading_direction"`
		DisplayMode      string `json:"display_mode"`
		FitMode          string `json:"fit_mode"`
		IsOverride       bool   `json:"is_override"`
	} `json:"prefs"`
}

type scanStatus struct {
	State    string `json:"state"`
	RunID    string `json:"run_id"`
	Total    int    `json:"total"`
	Done     int    `json:"done"`
	Errors   int    `json:"errors"`
	LastErr  string `json:"last_error"`
	Elapsed  int64  `json:"elapsed_ms"`
	CoversNo int    `json:"covers_total"`
}

type scanLog struct {
	Items []struct {
		ID      int64  `json:"id"`
		Level   string `json:"level"`
		RelPath string `json:"rel_path"`
		Message string `json:"message"`
	} `json:"items"`
}

type progressBody struct {
	BookID   string `json:"book_id"`
	LastPage int    `json:"last_page"`
	Complete bool   `json:"completed"`
}

// ---------------------------------------------------------------------------
// One shared scan for the whole package
// ---------------------------------------------------------------------------

var (
	sharedOnce sync.Once
	sharedDir  string
	sharedErr  error
)

// sharedState returns a directory holding a data/ and cache/ pair that persists
// for the whole package run, so the ~5 GB curated scan happens once rather than
// once per test. It is removed by TestMain.
func sharedState() (string, error) {
	sharedOnce.Do(func() {
		sharedDir, sharedErr = os.MkdirTemp("", "shelf-integration-")
	})
	return sharedDir, sharedErr
}

func TestMain(m *testing.M) {
	code := m.Run()
	if scannedSrv != nil {
		scannedSrv.stop()
	}
	if sharedDir != "" {
		_ = os.RemoveAll(sharedDir)
	}
	os.Exit(code)
}

// scanned is the shared server, scanned once. Tests that need a pristine state
// call startServer themselves with their own t.TempDir().
var (
	scannedOnce sync.Once
	scannedSrv  *server
)

// stopShared is called from TestMain.

// sharedServer starts (once) a server over the shared state and runs a full
// scan the first time it is asked for.
//
// It deliberately outlives the test that created it: the alternative is a
// 5 GB scan per test. The cost is that its *testing.T is the first test's, so
// a failure inside the shared server is attributed there; every assertion the
// tests make is on values they read afterwards, so that is a reporting detail
// rather than a correctness one.
func sharedServer(t *testing.T) *server {
	t.Helper()
	root := requireRoot(t)
	scannedOnce.Do(func() {
		dir, err := sharedState()
		if err != nil {
			t.Fatalf("creating the shared state directory: %v", err)
		}
		scannedSrv = startServer(t, root, dir, false)
		sharedScanSeconds = scannedSrv.scan(true, 10*time.Minute).Seconds()
	})
	if scannedSrv == nil {
		t.Fatal("the shared server was not started")
	}
	// Failures and log lines belong to the test that is running now.
	scannedSrv.t = t
	return scannedSrv
}

// sharedScanSeconds is how long the one full curated scan took, reported by
// the perf tests rather than measured again.
var sharedScanSeconds float64

// seriesByName indexes the whole library by display name.
func seriesByName(s *server) map[string]seriesSummary {
	s.t.Helper()
	var list seriesList
	s.get("/api/series?limit=200", &list)
	out := make(map[string]seriesSummary, len(list.Items))
	for _, it := range list.Items {
		out[it.Name] = it
	}
	return out
}

func mustSeries(s *server, name string) seriesSummary {
	s.t.Helper()
	all := seriesByName(s)
	item, ok := all[name]
	if !ok {
		names := make([]string, 0, len(all))
		for n := range all {
			names = append(names, n)
		}
		s.t.Fatalf("series %q is not in the library; got %v", name, names)
	}
	return item
}

func detailOf(s *server, name string) seriesDetail {
	s.t.Helper()
	var d seriesDetail
	s.get("/api/series/"+mustSeries(s, name).ID, &d)
	return d
}

// bodyOf performs a GET and returns the raw bytes, for the page endpoints.
func (s *server) bodyOf(path string) ([]byte, http.Header) {
	s.t.Helper()
	status, body, hdr := s.do(http.MethodGet, path, "")
	if status != http.StatusOK {
		s.t.Fatalf("GET %s = %d: %s", path, status, truncate(body))
	}
	return body, hdr
}
