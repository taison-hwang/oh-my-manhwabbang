package app

import (
	"context"
	"encoding/json"
	"errors"
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

	"shelf/internal/config"
	"shelf/internal/index"
	"shelf/internal/scanner"
	"shelf/internal/source"
	"shelf/internal/testutil"
	"shelf/internal/userdata"
)

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

// fixtureRoot builds the smallest tree that still exercises three of the four
// prd §2.2 shapes: a folder of ZIPs, a folder of loose images and a top-level
// single ZIP.
func fixtureRoot(t *testing.T) string {
	t.Helper()
	zipA := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
		{Name: "001.jpg", Data: testutil.TinyJPEG(t, 8, 12)},
		{Name: "002.jpg", Data: testutil.TinyJPEG(t, 8, 12)},
	}})
	// A CP949 entry name with no UTF-8 flag — the AC-002 path, in miniature.
	zipB := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
		{RawName: []byte("\xc7\xd1\xb1\xdb.jpg"), Data: testutil.TinyJPEG(t, 8, 12)},
	}})
	return testutil.BuildTree(t, map[string]any{
		"series-zips": map[string]any{
			"01권.zip": zipA,
			"02권.zip": zipB,
		},
		"series-images": map[string]any{
			"1.jpg":  testutil.TinyJPEG(t, 8, 12),
			"10.jpg": testutil.TinyJPEG(t, 8, 12),
			"2.jpg":  testutil.TinyJPEG(t, 8, 12),
		},
		"single.zip": zipA,
	})
}

// testConfig is a validated Config over a fixture root, with its own data and
// cache directories under t.TempDir(). scan.on_start is false so each test
// decides when a scan happens. The port is never used: every test passes an
// explicit :0 listener, and the loader rejects 0 as out of range.
func testConfig(t *testing.T, mediaRoot string) *config.Config {
	t.Helper()
	state := t.TempDir()
	yaml := fmt.Sprintf(`
server: {listen: "127.0.0.1", port: 8790}
roots:
  - {name: "fx", label: "fixtures", path: %q}
storage: {data_dir: %q, cache_dir: %q}
scan: {on_start: false, workers: 2}
thumbnails: {workers: 1}
pdf: {enabled: false}
log: {level: "error", format: "text", http_requests: false}
`, mediaRoot, filepath.Join(state, "data"), filepath.Join(state, "cache"))

	cfg, err := config.Parse([]byte(yaml), filepath.Join(state, "shelf.yaml"))
	if err != nil {
		t.Fatalf("parsing the test configuration: %v", err)
	}
	// config.Parse is hermetic by design; Load is what creates the directories.
	for _, d := range []string{cfg.Storage.DataDir, cfg.Storage.CacheDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatalf("creating %s: %v", d, err)
		}
	}
	return cfg
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// newTestApp brings the whole product up on a random port and tears it down
// when the test ends.
func newTestApp(t *testing.T, cfg *config.Config, rebuild bool) *App {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	a, err := New(t.Context(), Options{
		Config: cfg, Logger: quietLogger(), Listener: ln, RebuildIndex: rebuild,
	})
	if err != nil {
		_ = ln.Close()
		t.Fatalf("app.New: %v", err)
	}
	t.Cleanup(func() {
		if err := a.Close(); err != nil {
			t.Errorf("closing the app: %v", err)
		}
	})
	return a
}

// runApp starts Run in the background and returns a stop function that
// cancels it and waits for a clean shutdown.
func runApp(t *testing.T, a *App) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()

	waitReady(t, a)
	return func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run returned %v", err)
			}
		case <-time.After(30 * time.Second):
			t.Fatal("Run did not return within 30s of cancellation")
		}
	}
}

func waitReady(t *testing.T, a *App) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + a.Addr() + a.cfg.Server.BasePath + "/api/health")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the server never answered GET /api/health")
}

func getJSON(t *testing.T, a *App, path string, into any) int {
	t.Helper()
	resp, err := http.Get("http://" + a.Addr() + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	if into != nil && resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(body, into); err != nil {
			t.Fatalf("decoding %s: %v\n%s", path, err, body)
		}
	}
	return resp.StatusCode
}

// scanNow runs one synchronous scan through the wired scanner, which is what a
// test wants instead of racing the background one.
func scanNow(t *testing.T, a *App, full bool) *scanner.Result {
	t.Helper()
	res, err := a.scan.Run(t.Context(), scanner.Request{Full: full})
	if err != nil {
		t.Fatalf("scanning: %v", err)
	}
	return res
}

// ---------------------------------------------------------------------------
// Start-up sequence (arch §6.3)
// ---------------------------------------------------------------------------

func TestApp_startup_createsCacheKindsAndReconcilesRoots(t *testing.T) {
	cfg := testConfig(t, fixtureRoot(t))
	a := newTestApp(t, cfg, false)

	for _, kind := range cacheKinds {
		dir := filepath.Join(cfg.Storage.CacheDir, kind)
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			t.Errorf("cache kind %s was not created: %v", kind, err)
		}
	}
	// arch §6.3 step 4: the roots table mirrors the configuration before any
	// scan has run, so GET /api/roots is answerable on a first boot.
	roots, err := a.idx.ListRoots(t.Context())
	if err != nil {
		t.Fatalf("listing roots: %v", err)
	}
	if len(roots) != 1 || roots[0].Name != "fx" || roots[0].Label != "fixtures" {
		t.Fatalf("roots were not reconciled: %+v", roots)
	}
	if roots[0].Path != cfg.Roots[0].Path {
		t.Errorf("root path = %q, want %q", roots[0].Path, cfg.Roots[0].Path)
	}
}

func TestApp_reconcileRoots_keepsARootThatLeftTheConfiguration(t *testing.T) {
	cfg := testConfig(t, fixtureRoot(t))
	a := newTestApp(t, cfg, false)
	if err := a.idx.UpsertRoot(t.Context(), index.Root{
		Name: "gone", Path: "/nowhere", Enabled: true,
	}); err != nil {
		t.Fatalf("seeding a stale root: %v", err)
	}
	if err := a.reconcileRoots(t.Context()); err != nil {
		t.Fatalf("reconciling: %v", err)
	}
	roots, err := a.idx.ListRoots(t.Context())
	if err != nil {
		t.Fatalf("listing roots: %v", err)
	}
	if len(roots) != 2 {
		// Deleting it would delete its series and books. arch §4.9: absence
		// from one run is never evidence of absence on disk.
		t.Fatalf("a root missing from the config must be kept, got %d roots: %+v", len(roots), roots)
	}
}

// NFR-OPS-006 — the library is served from the existing index before any scan
// starts, which is what makes a restart after `kill -9` invisible to a reader.
func TestApp_restart_servesTheExistingIndexBeforeScanning(t *testing.T) {
	media := fixtureRoot(t)
	cfg := testConfig(t, media)

	// First run: scan, then simulate an unclean exit by closing without any
	// checkpoint of our own — the WAL is left for the next start to recover.
	first := newTestApp(t, cfg, false)
	res := scanNow(t, first, true)
	series, _, _, _, _ := res.Totals()
	if series != 3 {
		t.Fatalf("first scan indexed %d series, want 3", series)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("closing the first app: %v", err)
	}

	// Second run: no scan at all. Everything below is answered from index.db.
	second := newTestApp(t, cfg, false)
	stop := runApp(t, second)
	defer stop()

	var list struct {
		Total int `json:"total"`
		Items []struct {
			Name string `json:"name"`
		} `json:"items"`
	}
	if code := getJSON(t, second, "/api/series", &list); code != http.StatusOK {
		t.Fatalf("GET /api/series = %d", code)
	}
	if list.Total != 3 {
		t.Fatalf("after a restart with no scan, /api/series total = %d, want 3", list.Total)
	}
	var status struct {
		State string `json:"state"`
	}
	getJSON(t, second, "/api/scan/status", &status)
	if status.State != "idle" {
		t.Errorf("scan state = %q, want idle: NFR-OPS-006 requires serving without scanning", status.State)
	}
}

// FR-IDX-005 / AC-006 — --rebuild-index deletes index.db and its two sidecars
// and nothing else. Reading progress lives in user.db and must survive.
func TestApp_rebuildIndex_deletesOnlyTheIndexAndKeepsProgress(t *testing.T) {
	media := fixtureRoot(t)
	cfg := testConfig(t, media)

	first := newTestApp(t, cfg, false)
	scanNow(t, first, true)

	list, err := first.idx.ListSeries(t.Context(), index.SeriesFilter{})
	if err != nil || len(list.Items) == 0 {
		t.Fatalf("listing series: %v (%d items)", err, len(list.Items))
	}
	books, err := first.idx.ListBooks(t.Context(), list.Items[0].ID)
	if err != nil || len(books) == 0 {
		t.Fatalf("listing books: %v (%d books)", err, len(books))
	}
	bookID, seriesID := books[0].ID, list.Items[0].ID

	if _, err := first.user.PutProgress(t.Context(), userdata.ProgressUpdate{
		BookID: bookID, SeriesID: seriesID, Page: 2, PageCount: 2,
	}); err != nil {
		t.Fatalf("writing progress: %v", err)
	}
	if _, err := first.user.PutPrefs(t.Context(), bookID, userdata.PrefsPatch{
		ReadingDir: userdata.SetPatch("rtl"),
	}); err != nil {
		t.Fatalf("writing prefs: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("closing: %v", err)
	}

	// Record every file in data_dir so the deletion can be checked exactly.
	before := dirListing(t, cfg.Storage.DataDir)

	second := newTestApp(t, cfg, true /* --rebuild-index */)
	after := dirListing(t, cfg.Storage.DataDir)
	for name := range before {
		if strings.HasPrefix(name, "index.db") {
			continue // may legitimately have been deleted and recreated
		}
		if _, ok := after[name]; !ok {
			t.Errorf("--rebuild-index deleted %s, which is not an index file", name)
		}
	}

	got, err := second.user.GetProgress(t.Context(), bookID)
	if err != nil {
		t.Fatalf("reading progress after a rebuild: %v", err)
	}
	if got.LastPage != 2 {
		t.Errorf("last_page = %d after a rebuild, want 2 (AC-006)", got.LastPage)
	}
	prefs, err := second.user.GetPrefs(t.Context(), bookID)
	if err != nil {
		t.Fatalf("reading prefs after a rebuild: %v", err)
	}
	if prefs.ReadingDir == nil || *prefs.ReadingDir != "rtl" {
		t.Errorf("reading direction did not survive the rebuild: %+v", prefs)
	}

	// And the index really was emptied and is rebuilt to the same ids
	// (FR-CFG-004: identity depends only on the config and the filesystem).
	scanNow(t, second, true)
	rebuilt, err := second.idx.ListBooks(t.Context(), seriesID)
	if err != nil {
		t.Fatalf("listing books after the rebuild: %v", err)
	}
	if len(rebuilt) == 0 || rebuilt[0].ID != bookID {
		t.Errorf("book id changed across a rebuild: %+v", rebuilt)
	}
}

// FR-IDX-005 — `--rebuild-index` is a *rebuild*, not a delete. [New] removes
// index.db; [App.Run] is what puts it back, and it must do so whatever
// `scan.on_start` says.
//
// `scan.on_start: false` is a documented, supported and widely used setting —
// it is what test/shelf.e2e.yaml.tmpl, the integration harness and this file's
// own testConfig all use. Tying the rebuild to it would leave that operator with
// a deleted index, an empty library and a log line promising a scan that never
// arrives.
func TestApp_rebuildIndex_scansEvenWhenScanOnStartIsFalse(t *testing.T) {
	cfg := testConfig(t, fixtureRoot(t))
	if cfg.Scan.OnStart {
		t.Fatal("this test only means anything with scan.on_start false")
	}

	// A populated index to destroy.
	first := newTestApp(t, cfg, false)
	series, _, _, _, _ := scanNow(t, first, true).Totals()
	if series != 3 {
		t.Fatalf("the first scan indexed %d series, want 3", series)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("closing the first app: %v", err)
	}

	second := newTestApp(t, cfg, true /* --rebuild-index */)

	// New has deleted index.db and nothing has rebuilt it yet: whatever the
	// assertion below sees is the work of Run alone.
	if list, err := second.idx.ListSeries(t.Context(), index.SeriesFilter{}); err != nil {
		t.Fatalf("listing series straight after the rebuild: %v", err)
	} else if list.Total != 0 {
		t.Fatalf("--rebuild-index left %d series behind; the index was not deleted", list.Total)
	}

	stop := runApp(t, second)
	defer stop()

	var list struct {
		Total int `json:"total"`
	}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if code := getJSON(t, second, "/api/series", &list); code != http.StatusOK {
			t.Fatalf("GET /api/series = %d", code)
		}
		if list.Total == 3 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	var status struct {
		State string `json:"state"`
		RunID string `json:"run_id"`
	}
	getJSON(t, second, "/api/scan/status", &status)
	t.Fatalf("FR-IDX-005: --rebuild-index deleted the index and never rebuilt it — "+
		"/api/series total = %d after 60 s, want 3 (scan state=%q run_id=%q)",
		list.Total, status.State, status.RunID)
}

// blockingLister is the seam Options.wrapBooks exists for. It holds the
// scanner's very first book open until it is released, which is the only way to
// observe an *ordering* rather than a coincidence: while one of these is
// blocked, the start-up scan provably cannot have finished.
type blockingLister struct {
	inner    scanner.BookLister
	opened   chan struct{} // closed when the scan reaches its first book
	release  chan struct{} // closed to let every Open through
	openOnce sync.Once
	relOnce  sync.Once
}

func newBlockingLister() *blockingLister {
	return &blockingLister{opened: make(chan struct{}), release: make(chan struct{})}
}

func (b *blockingLister) Open(ctx context.Context, bk source.Book) (source.BookSource, error) {
	b.openOnce.Do(func() { close(b.opened) })
	select {
	case <-b.release:
	case <-ctx.Done():
		// A cancelled scan must not leave a worker parked here for ever;
		// shutdown would then block on it.
		return nil, ctx.Err()
	}
	return b.inner.Open(ctx, bk)
}

func (b *blockingLister) let() { b.relOnce.Do(func() { close(b.release) }) }

// NFR-OPS-006 — Serve comes *before* the start-up scan, not merely alongside a
// disabled one. With scan.on_start true and the scan wedged on its first book,
// every read endpoint must still answer.
//
// The listener is bound by New, so a Run that scanned first would leave this
// connection sitting in the accept backlog rather than refusing it: the failure
// mode is a hang, which is why the client below carries a timeout.
func TestApp_run_servesWhileTheStartUpScanIsStillRunning(t *testing.T) {
	media := fixtureRoot(t)
	cfg := testConfig(t, media)
	cfg.Scan.OnStart = true

	// Seed the index so /api/series has something to answer with — NFR-OPS-006
	// is precisely "the library answers from the index that is already on disk".
	seed := newTestApp(t, cfg, false)
	scanNow(t, seed, true)
	if err := seed.Close(); err != nil {
		t.Fatalf("closing the seeding app: %v", err)
	}

	// A series the seeding scan never saw. The start-up scan is incremental, so
	// without new work on disk it would skip every book by mtime and never open
	// one — the gate below would never close.
	fresh := filepath.Join(media, "series-fresh")
	if err := os.MkdirAll(fresh, 0o755); err != nil {
		t.Fatalf("creating a new series: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fresh, "01권.zip"),
		testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
			{Name: "001.jpg", Data: testutil.TinyJPEG(t, 8, 12)},
		}}), 0o644); err != nil {
		t.Fatalf("writing a new volume: %v", err)
	}

	gate := newBlockingLister()
	defer gate.let()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	a, err := New(t.Context(), Options{
		Config: cfg, Logger: quietLogger(), Listener: ln,
		wrapBooks: func(inner scanner.BookLister) scanner.BookLister {
			gate.inner = inner
			return gate
		},
	})
	if err != nil {
		_ = ln.Close()
		t.Fatalf("app.New: %v", err)
	}
	t.Cleanup(func() {
		gate.let()
		if err := a.Close(); err != nil {
			t.Errorf("closing the app: %v", err)
		}
	})

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	defer func() {
		gate.let()
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run returned %v", err)
			}
		case <-time.After(30 * time.Second):
			t.Error("Run did not return within 30s of cancellation")
		}
	}()

	select {
	case <-gate.opened:
	case <-time.After(30 * time.Second):
		t.Fatal("the start-up scan never reached its first book; the seam is not wired")
	}

	client := &http.Client{Timeout: 20 * time.Second}
	fetch := func(path string, into any) {
		t.Helper()
		resp, err := client.Get("http://" + a.Addr() + path)
		if err != nil {
			t.Fatalf("NFR-OPS-006: GET %s while the start-up scan is running: %v\n"+
				"the server must be serving before the scan starts", path, err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s = %d during the start-up scan: %s", path, resp.StatusCode, body)
		}
		if into != nil {
			if err := json.Unmarshal(body, into); err != nil {
				t.Fatalf("decoding %s: %v\n%s", path, err, body)
			}
		}
	}

	fetch("/api/health", nil)
	var list struct {
		Total int `json:"total"`
	}
	fetch("/api/series", &list)
	if list.Total != 3 {
		t.Errorf("/api/series answered %d series during the start-up scan, want the 3 already on disk", list.Total)
	}

	// And the scan really was still in flight while all of that was answered,
	// so the assertion above is about order and not about a scan that had
	// quietly finished first.
	var status struct {
		State string `json:"state"`
	}
	fetch("/api/scan/status", &status)
	if status.State == "idle" {
		t.Errorf("the start-up scan reported %q while a book was held open; "+
			"this test can no longer distinguish serve-then-scan from scan-then-serve", status.State)
	}
}

func dirListing(t *testing.T, dir string) map[string]struct{} {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	out := make(map[string]struct{}, len(entries))
	for _, e := range entries {
		out[e.Name()] = struct{}{}
	}
	return out
}

// arch §6.3 step 7 — a clean shutdown checkpoints both write-ahead logs, so the
// next start has nothing to replay.
func TestApp_shutdown_checkpointsBothWALs(t *testing.T) {
	cfg := testConfig(t, fixtureRoot(t))
	a := newTestApp(t, cfg, false)
	scanNow(t, a, true)
	if err := a.user.Settings().Put(t.Context(), "theme", "dark"); err != nil {
		t.Fatalf("writing a setting: %v", err)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("closing: %v", err)
	}
	for _, name := range []string{"index.db-wal", "user.db-wal"} {
		p := filepath.Join(cfg.Storage.DataDir, name)
		fi, err := os.Stat(p)
		if errors.Is(err, os.ErrNotExist) {
			continue // checkpointed and removed, which is the ideal outcome
		}
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		if fi.Size() != 0 {
			t.Errorf("%s is %d bytes after shutdown; wal_checkpoint(TRUNCATE) did not run", name, fi.Size())
		}
	}
}

// The whole product, end to end, over HTTP: scan a tree, then walk
// series → book → page and read the original bytes back (AC-003 in miniature).
func TestApp_httpFlow_seriesToBookToPage(t *testing.T) {
	cfg := testConfig(t, fixtureRoot(t))
	a := newTestApp(t, cfg, false)
	scanNow(t, a, true)
	stop := runApp(t, a)
	defer stop()

	var list struct {
		Total int `json:"total"`
		Items []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			Kind string `json:"kind"`
		} `json:"items"`
	}
	if code := getJSON(t, a, "/api/series?sort=name", &list); code != http.StatusOK {
		t.Fatalf("GET /api/series = %d", code)
	}
	if list.Total != 3 {
		t.Fatalf("series total = %d, want 3", list.Total)
	}
	var kinds []string
	for _, s := range list.Items {
		kinds = append(kinds, s.Kind)
	}
	if !contains(kinds, "folder") || !contains(kinds, "zip") {
		t.Errorf("kinds = %v, want at least one folder and one zip", kinds)
	}

	for _, s := range list.Items {
		var detail struct {
			Books []struct {
				ID        string `json:"id"`
				CV        string `json:"cv"`
				Status    string `json:"status"`
				PageCount int    `json:"page_count"`
			} `json:"books"`
		}
		if code := getJSON(t, a, "/api/series/"+s.ID, &detail); code != http.StatusOK {
			t.Fatalf("GET /api/series/%s = %d", s.ID, code)
		}
		if len(detail.Books) == 0 {
			t.Fatalf("series %q has no books", s.Name)
		}
		b := detail.Books[0]
		if b.Status != "ok" || b.PageCount == 0 {
			t.Fatalf("series %q first book: status=%s pages=%d", s.Name, b.Status, b.PageCount)
		}
		url := fmt.Sprintf("http://%s/api/books/%s/pages/1?v=%s", a.Addr(), b.ID, b.CV)
		resp, err := http.Get(url)
		if err != nil {
			t.Fatalf("GET page: %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s = %d: %s", url, resp.StatusCode, body)
		}
		if ct := resp.Header.Get("Content-Type"); ct != "image/jpeg" {
			t.Errorf("content type = %q, want image/jpeg", ct)
		}
		if cc := resp.Header.Get("Cache-Control"); !strings.Contains(cc, "immutable") {
			t.Errorf("with a matching ?v= the response must be immutable, got %q", cc)
		}
		if len(body) < 100 {
			t.Errorf("page body is %d bytes, which cannot be a JPEG", len(body))
		}
	}
}

// AC-002 in miniature: a CP949 entry name with no UTF-8 flag comes back as
// Hangul, with no U+FFFD anywhere.
func TestApp_pageNames_cp949DecodesWithoutReplacementCharacters(t *testing.T) {
	cfg := testConfig(t, fixtureRoot(t))
	a := newTestApp(t, cfg, false)
	scanNow(t, a, true)
	stop := runApp(t, a)
	defer stop()

	var list struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	getJSON(t, a, "/api/series", &list)

	found := false
	for _, s := range list.Items {
		var detail struct {
			Books []struct {
				ID string `json:"id"`
			} `json:"books"`
		}
		getJSON(t, a, "/api/series/"+s.ID, &detail)
		for _, b := range detail.Books {
			var book struct {
				Pages []struct {
					Name string `json:"name"`
				} `json:"pages"`
			}
			getJSON(t, a, "/api/books/"+b.ID, &book)
			for _, p := range book.Pages {
				if strings.ContainsRune(p.Name, '�') {
					t.Errorf("page name %q contains U+FFFD (AC-002)", p.Name)
				}
				if p.Name == "한글.jpg" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Error(`the CP949 entry did not come back as "한글.jpg"`)
	}
}

// NFR-SEC-003 — the whole app, including the SPA fallback, mounts under
// base_path, and a request outside it is a JSON 404 rather than the SPA.
func TestApp_basePath_mountsTheWholeApplication(t *testing.T) {
	cfg := testConfig(t, fixtureRoot(t))
	cfg.Server.BasePath = "/reader"
	a := newTestApp(t, cfg, false)
	stop := runApp(t, a)
	defer stop()

	if code := getJSON(t, a, "/reader/api/health", nil); code != http.StatusOK {
		t.Errorf("GET /reader/api/health = %d, want 200", code)
	}
	if code := getJSON(t, a, "/api/health", nil); code != http.StatusNotFound {
		t.Errorf("GET /api/health outside the base path = %d, want 404", code)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Get("http://" + a.Addr() + "/reader")
	if err != nil {
		t.Fatalf("GET /reader: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusPermanentRedirect {
		t.Errorf("GET /reader = %d, want 308 to /reader/", resp.StatusCode)
	}
}

func contains(hay []string, needle string) bool {
	for _, s := range hay {
		if s == needle {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Logging (NFR-OPS-005)
// ---------------------------------------------------------------------------

func TestParseLevel(t *testing.T) {
	t.Parallel()
	cases := map[string]slog.Level{
		"debug":     slog.LevelDebug,
		"INFO":      slog.LevelInfo,
		"":          slog.LevelInfo,
		"warn":      slog.LevelWarn,
		" warning ": slog.LevelWarn,
		"error":     slog.LevelError,
	}
	for in, want := range cases {
		got, err := ParseLevel(in)
		if err != nil {
			t.Errorf("ParseLevel(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseLevel(%q) = %v, want %v", in, got, want)
		}
	}
	if _, err := ParseLevel("verbose"); err == nil {
		t.Error("ParseLevel(\"verbose\") must fail rather than silently defaulting")
	}
}

func TestNewLogger_jsonFormatEmitsJSON(t *testing.T) {
	t.Parallel()
	var buf strings.Builder
	NewLogger(&buf, "json", slog.LevelInfo).Info("hello", "req_id", "abc")
	var rec map[string]any
	if err := json.Unmarshal([]byte(buf.String()), &rec); err != nil {
		t.Fatalf("log line is not JSON: %v\n%s", err, buf.String())
	}
	if rec["req_id"] != "abc" {
		t.Errorf("attribute lost: %v", rec)
	}
}
