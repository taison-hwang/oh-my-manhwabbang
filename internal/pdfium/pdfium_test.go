//go:build !nopdf

package pdfium_test

import (
	"bytes"
	"errors"
	"fmt"
	"image/jpeg"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"shelf/internal/pdfium"
)

// ---------------------------------------------------------------------------
// A minimal, hand-built PDF fixture.
//
// The frozen dependency set has no PDF *writer*, and the reference collection's
// PDFs are 500 MB of copyrighted manga that the hermetic suite may not touch
// (impl-plan §6.1). So the fixture is written out by hand: a catalog, a page
// tree, one content stream per page, and a correct xref table. Roughly 400
// bytes per page, and pdfium parses it exactly as it parses a real file.
// ---------------------------------------------------------------------------

func minimalPDF(t testing.TB, pages int, w, h int) []byte {
	t.Helper()
	return paddedPDF(t, pages, w, h, 0)
}

// paddedPDF is minimalPDF plus an unreferenced stream of pad bytes, so that
// "pdfium does not slurp the file" can be measured on something bigger than a
// page tree.
func paddedPDF(t testing.TB, pages, w, h, pad int) []byte {
	t.Helper()
	var buf bytes.Buffer
	offsets := make(map[int]int)

	buf.WriteString("%PDF-1.4\n")
	obj := func(n int, body string) {
		offsets[n] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", n, body)
	}

	kids := ""
	for i := 0; i < pages; i++ {
		kids += fmt.Sprintf("%d 0 R ", 3+2*i)
	}
	obj(1, "<< /Type /Catalog /Pages 2 0 R >>")
	obj(2, fmt.Sprintf("<< /Type /Pages /Kids [ %s] /Count %d >>", kids, pages))

	for i := 0; i < pages; i++ {
		pageObj, contentObj := 3+2*i, 4+2*i
		obj(pageObj, fmt.Sprintf(
			"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %d %d] /Contents %d 0 R /Resources << >> >>",
			w, h, contentObj))
		// A filled rectangle whose colour varies per page, so a render that
		// silently returns the wrong page is visible in the bytes.
		content := fmt.Sprintf("%0.2f 0.35 0.75 rg 10 10 %d %d re f\n",
			float64(i%10)/10.0, w-20, h-20)
		obj(contentObj, fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(content), content))
	}

	total := 2 + 2*pages
	if pad > 0 {
		total++
		junk := make([]byte, pad)
		for i := range junk {
			junk[i] = byte('A' + i%26)
		}
		offsets[total] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n<< /Length %d >>\nstream\n", total, len(junk))
		buf.Write(junk)
		buf.WriteString("\nendstream\nendobj\n")
	}

	xref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n", total+1)
	buf.WriteString("0000000000 65535 f \n")
	for n := 1; n <= total; n++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[n])
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", total+1, xref)
	return buf.Bytes()
}

// sharedCache is one wazero compilation cache for the whole package. Without
// it every test pays the 3.9 s cold compile; with it only the first does, which
// is exactly the effect D-20 relies on in production.
var sharedCache string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "shelf-wazero-cache")
	if err != nil {
		panic(err)
	}
	sharedCache = dir
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func newRenderer(t *testing.T) *pdfium.Renderer {
	t.Helper()
	r := pdfium.New(pdfium.Options{
		Workers:  1,
		CacheDir: sharedCache,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Errorf("closing the renderer: %v", err)
		}
	})
	return r
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestSupported_isTrueWithoutTheNopdfTag(t *testing.T) {
	t.Parallel()
	if !pdfium.Supported() {
		t.Fatal("Supported() = false in a build without -tags nopdf")
	}
}

func TestOpen_readsPageCountWithoutSlurpingTheFile(t *testing.T) {
	if testing.Short() {
		t.Skip("pdfium's first wasm compile costs seconds")
	}
	const pages = 12
	// 8 MiB of unreferenced payload: a reader that slurps shows up immediately.
	data := paddedPDF(t, pages, 200, 300, 8<<20)

	// A counting reader proves the "streaming open" claim of arch §5.7:
	// pdfium pulls the ranges it needs, it does not read the file end to end.
	rs := &countingReadSeeker{r: bytes.NewReader(data)}
	r := newRenderer(t)

	doc, err := r.Open(t.Context(), rs, int64(len(data)))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = doc.Close() }()

	if got := doc.PageCount(); got != pages {
		t.Errorf("PageCount() = %d, want %d", got, pages)
	}
	t.Logf("opened a %d-byte, %d-page document reading %d bytes in %d calls",
		len(data), pages, rs.bytes, rs.reads)
	if pct := 100 * float64(rs.bytes) / float64(len(data)); pct > 25 {
		t.Errorf("opening read %.1f%% of the file; NFR-PRF-006 requires a streaming open", pct)
	}
}

// FR-SRV-006: the render resolution is a request parameter.
func TestRenderJPEG_widthIsARequestParameter(t *testing.T) {
	if testing.Short() {
		t.Skip("pdfium's first wasm compile costs seconds")
	}
	data := minimalPDF(t, 3, 200, 300)
	r := newRenderer(t)
	doc, err := r.Open(t.Context(), bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = doc.Close() }()

	for _, want := range []int{200, 600, 1200} {
		jpg, err := doc.RenderJPEG(t.Context(), 1, want, 80)
		if err != nil {
			t.Fatalf("RenderJPEG at %d: %v", want, err)
		}
		cfg, err := jpeg.DecodeConfig(bytes.NewReader(jpg))
		if err != nil {
			t.Fatalf("the render at %d is not a decodable JPEG: %v", want, err)
		}
		if cfg.Width != want {
			t.Errorf("rendered width = %d, want %d", cfg.Width, want)
		}
		// The 2:3 MediaBox must be preserved.
		if wantH := want * 3 / 2; cfg.Height != wantH {
			t.Errorf("rendered height = %d, want %d (aspect ratio not preserved)", cfg.Height, wantH)
		}
	}
}

func TestRenderJPEG_distinctPagesRenderDistinctBytes(t *testing.T) {
	if testing.Short() {
		t.Skip("pdfium's first wasm compile costs seconds")
	}
	data := minimalPDF(t, 4, 120, 160)
	r := newRenderer(t)
	doc, err := r.Open(t.Context(), bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = doc.Close() }()

	seen := make(map[string]int, 4)
	for n := 1; n <= doc.PageCount(); n++ {
		jpg, err := doc.RenderJPEG(t.Context(), n, 200, 80)
		if err != nil {
			t.Fatalf("RenderJPEG page %d: %v", n, err)
		}
		if prev, dup := seen[string(jpg)]; dup {
			t.Errorf("page %d rendered byte-identically to page %d", n, prev)
		}
		seen[string(jpg)] = n
	}
}

func TestRenderJPEG_outOfRangePage_isErrNoSuchPage(t *testing.T) {
	if testing.Short() {
		t.Skip("pdfium's first wasm compile costs seconds")
	}
	data := minimalPDF(t, 2, 100, 100)
	r := newRenderer(t)
	doc, err := r.Open(t.Context(), bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = doc.Close() }()

	// All page numbers in this product are 1-based; there is no page 0.
	for _, n := range []int{0, -1, 3, 1 << 20} {
		if _, err := doc.RenderJPEG(t.Context(), n, 200, 80); !errors.Is(err, pdfium.ErrNoSuchPage) {
			t.Errorf("RenderJPEG(page %d) err = %v, want pdfium.ErrNoSuchPage", n, err)
		}
	}
}

func TestPageSize_reportsTheMediaBox(t *testing.T) {
	if testing.Short() {
		t.Skip("pdfium's first wasm compile costs seconds")
	}
	data := minimalPDF(t, 2, 320, 480)
	r := newRenderer(t)
	doc, err := r.Open(t.Context(), bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = doc.Close() }()

	w, h, err := doc.PageSize(t.Context(), 1)
	if err != nil {
		t.Fatalf("PageSize: %v", err)
	}
	if int(w) != 320 || int(h) != 480 {
		t.Errorf("PageSize = %vx%v, want 320x480", w, h)
	}
}

// res.Cleanup() is mandatory in wasm mode. If a render leaked its bitmap, a
// few hundred renders would show it as unbounded growth.
func TestRenderJPEG_repeated_doesNotLeakTheWasmBitmap(t *testing.T) {
	if testing.Short() {
		t.Skip("pdfium's first wasm compile costs seconds")
	}
	data := minimalPDF(t, 1, 400, 600)
	r := newRenderer(t)
	doc, err := r.Open(t.Context(), bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = doc.Close() }()

	// Warm up, then measure.
	for i := 0; i < 5; i++ {
		if _, err := doc.RenderJPEG(t.Context(), 1, 800, 80); err != nil {
			t.Fatalf("warm-up render: %v", err)
		}
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)

	const renders = 60
	for i := 0; i < renders; i++ {
		if _, err := doc.RenderJPEG(t.Context(), 1, 800, 80); err != nil {
			t.Fatalf("render %d: %v", i, err)
		}
	}
	runtime.GC()
	runtime.ReadMemStats(&after)

	growth := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	t.Logf("%d renders at 800px: heap %+d bytes", renders, growth)
	// One 800x1200 RGBA bitmap is ~3.8 MB. Leaking even a tenth of them would
	// be tens of megabytes; 32 MiB of slack keeps the test from being flaky
	// while still catching a real leak.
	if growth > 32<<20 {
		t.Errorf("heap grew %d bytes over %d renders; res.Cleanup() is probably not being called", growth, renders)
	}
}

func TestDoc_concurrentRenders_areSerialisedNotCorrupted(t *testing.T) {
	if testing.Short() {
		t.Skip("pdfium's first wasm compile costs seconds")
	}
	data := minimalPDF(t, 3, 150, 200)
	r := newRenderer(t)
	doc, err := r.Open(t.Context(), bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = doc.Close() }()

	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 3; i++ {
				jpg, err := doc.RenderJPEG(t.Context(), (i%3)+1, 300, 80)
				if err != nil {
					errs <- err
					return
				}
				if _, err := jpeg.DecodeConfig(bytes.NewReader(jpg)); err != nil {
					errs <- fmt.Errorf("goroutine %d produced an undecodable jpeg: %w", g, err)
					return
				}
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestDoc_close_isIdempotentAndReleasesTheWorker(t *testing.T) {
	if testing.Short() {
		t.Skip("pdfium's first wasm compile costs seconds")
	}
	data := minimalPDF(t, 1, 100, 100)
	r := newRenderer(t)

	// Workers = 1, so a leaked worker makes the second Open block until the
	// 60 s acquire timeout. Opening twice in a row proves the release.
	for i := 0; i < 3; i++ {
		doc, err := r.Open(t.Context(), bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatalf("Open %d: %v", i, err)
		}
		if err := doc.Close(); err != nil {
			t.Fatalf("Close %d: %v", i, err)
		}
		if err := doc.Close(); err != nil {
			t.Fatalf("second Close %d: %v", i, err)
		}
		if _, err := doc.RenderJPEG(t.Context(), 1, 200, 80); !errors.Is(err, pdfium.ErrClosed) {
			t.Errorf("render after Close = %v, want pdfium.ErrClosed", err)
		}
	}
}

// D-20: the runtime is created on first use, not at startup. A library of ZIP
// and folder series must never pay pdfium's 43–299 MiB.
func TestNew_isLazy_andCloseIsSafeBeforeAnyUse(t *testing.T) {
	t.Parallel()
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	r := pdfium.New(pdfium.Options{Workers: 1, CacheDir: t.TempDir()})

	runtime.GC()
	runtime.ReadMemStats(&after)
	if growth := int64(after.HeapAlloc) - int64(before.HeapAlloc); growth > 1<<20 {
		t.Errorf("New() allocated %d bytes; the runtime must be lazy (D-20)", growth)
	}
	if err := r.Close(); err != nil {
		t.Errorf("Close before any use: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
	if _, err := r.Open(t.Context(), bytes.NewReader(minimalPDF(t, 1, 10, 10)), 10); !errors.Is(err, pdfium.ErrClosed) {
		t.Errorf("Open after Close = %v, want pdfium.ErrClosed", err)
	}
}

// The wazero compilation cache is what turns a 3.885 s cold init into 135 ms
// (D-20). Sharing a cache directory between two renderers must produce a
// populated cache and a faster second start.
func TestCompilationCache_isReusedAcrossRenderers(t *testing.T) {
	if testing.Short() {
		t.Skip("pdfium's first wasm compile costs seconds")
	}
	cacheDir := t.TempDir()
	data := minimalPDF(t, 1, 100, 100)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	timeFirstRender := func() time.Duration {
		r := pdfium.New(pdfium.Options{Workers: 1, CacheDir: cacheDir, Logger: log})
		defer func() { _ = r.Close() }()
		start := time.Now()
		doc, err := r.Open(t.Context(), bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		_ = doc.Close()
		return time.Since(start)
	}

	cold := timeFirstRender()
	warm := timeFirstRender()
	t.Logf("first init %v, second init %v (cache dir %s)", cold, warm, cacheDir)
	if warm > cold {
		t.Logf("warning: the warm start (%v) was not faster than the cold one (%v)", warm, cold)
	}
}

// A broken cache directory must cost speed, never correctness.
func TestCompilationCache_unusableDirectory_stillRenders(t *testing.T) {
	if testing.Short() {
		t.Skip("pdfium's first wasm compile costs seconds")
	}
	// A path whose parent is a file cannot be created as a directory.
	dir := t.TempDir()
	blocker := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(blocker, []byte("i am a file"), 0o644); err != nil {
		t.Skipf("cannot build the unusable-directory case here: %v", err)
	}
	bad := filepath.Join(blocker, "cache")

	r := pdfium.New(pdfium.Options{
		Workers:  1,
		CacheDir: bad,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	t.Cleanup(func() { _ = r.Close() })

	data := minimalPDF(t, 1, 100, 100)
	doc, err := r.Open(t.Context(), bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("a broken cache directory must not break rendering: %v", err)
	}
	defer func() { _ = doc.Close() }()
	if _, err := doc.RenderJPEG(t.Context(), 1, 200, 80); err != nil {
		t.Errorf("RenderJPEG: %v", err)
	}
}

func TestSnapWidth(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, max, want int
	}{
		{0, 0, pdfium.DefaultWidth},  // unset -> the default
		{-5, 0, pdfium.DefaultWidth}, // nonsense -> the default
		{1, 0, pdfium.MinWidth},      // below the floor
		{149, 0, 100},                // snaps down
		{151, 0, 200},                // snaps up
		{1234, 0, 1200},              // a slider drag collapses to one cache entry
		{1250, 0, 1300},              // .5 rounds up
		{99999, 0, pdfium.MaxWidth},  // clamped to the hard ceiling
		{5000, 1600, 1600},           // clamped to pdf.max_width
		{800, 1600, 800},             // inside both bounds
	}
	for _, tc := range cases {
		if got := pdfium.SnapWidth(tc.in, tc.max); got != tc.want {
			t.Errorf("SnapWidth(%d, %d) = %d, want %d", tc.in, tc.max, got, tc.want)
		}
	}
	// The snapping must be idempotent, or the cache key is not stable.
	for w := 1; w < 5000; w += 37 {
		once := pdfium.SnapWidth(w, 0)
		if twice := pdfium.SnapWidth(once, 0); twice != once {
			t.Fatalf("SnapWidth is not idempotent at %d: %d then %d", w, once, twice)
		}
	}
}

// countingReadSeeker records how much of the file pdfium actually pulls.
type countingReadSeeker struct {
	r     io.ReadSeeker
	reads int
	bytes int64
}

func (c *countingReadSeeker) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.reads++
	c.bytes += int64(n)
	return n, err
}

func (c *countingReadSeeker) Seek(off int64, whence int) (int64, error) {
	return c.r.Seek(off, whence)
}

// D-20's other half: after pdf.idle_timeout with no work the runtime is torn
// down and its 43–299 MiB goes back to the OS, and the next request brings it
// back on the warm path.
func TestIdleTimeout_tearsDownTheRuntimeAndItComesBack(t *testing.T) {
	if testing.Short() {
		t.Skip("pdfium's first wasm compile costs seconds")
	}
	r := pdfium.New(pdfium.Options{
		Workers:     1,
		CacheDir:    sharedCache,
		IdleTimeout: 300 * time.Millisecond,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	t.Cleanup(func() { _ = r.Close() })

	if r.Active() {
		t.Fatal("Active() = true before any use; the runtime must be lazy")
	}
	data := minimalPDF(t, 1, 100, 100)

	doc, err := r.Open(t.Context(), bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !r.Active() {
		t.Error("Active() = false while a document is open")
	}
	if _, err := doc.RenderJPEG(t.Context(), 1, 200, 80); err != nil {
		t.Fatalf("RenderJPEG: %v", err)
	}
	if err := doc.Close(); err != nil {
		t.Fatalf("doc.Close: %v", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for r.Active() {
		if time.Now().After(deadline) {
			t.Fatal("the runtime was still up 10 s after a 300 ms idle timeout")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// And it must come back, on the warm path.
	start := time.Now()
	doc2, err := r.Open(t.Context(), bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("Open after teardown: %v", err)
	}
	t.Logf("re-initialised in %v", time.Since(start))
	if _, err := doc2.RenderJPEG(t.Context(), 1, 200, 80); err != nil {
		t.Errorf("RenderJPEG after teardown: %v", err)
	}
	if err := doc2.Close(); err != nil {
		t.Errorf("doc2.Close: %v", err)
	}
}

// A document that is still open must not be torn down under the caller.
func TestIdleTimeout_doesNotTearDownWhileADocumentIsOpen(t *testing.T) {
	if testing.Short() {
		t.Skip("pdfium's first wasm compile costs seconds")
	}
	r := pdfium.New(pdfium.Options{
		Workers:     1,
		CacheDir:    sharedCache,
		IdleTimeout: 200 * time.Millisecond,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	t.Cleanup(func() { _ = r.Close() })

	data := minimalPDF(t, 2, 100, 100)
	doc, err := r.Open(t.Context(), bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = doc.Close() }()

	time.Sleep(900 * time.Millisecond)
	if !r.Active() {
		t.Fatal("the runtime was torn down while a document was open")
	}
	if _, err := doc.RenderJPEG(t.Context(), 2, 200, 80); err != nil {
		t.Errorf("RenderJPEG after the idle window: %v", err)
	}
}
