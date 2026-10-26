package httpapi

import (
	"bytes"
	"compress/gzip"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"
	"weak"

	"shelf/internal/config"
)

// Decision E-63: the release build replaces assets/*.js and *.css with .gz
// files (scripts/precompress) and serveStatic serves the compressed form to a
// client that accepts it and the decompressed form to one that does not. These
// tests pin the HTTP semantics of that fallback; the build side is pinned in
// scripts/precompress/main_test.go.

// The bodies are large enough that `bytes=0-9` is a strict prefix of both the
// compressed and the identity representation, so a Range served from the wrong
// one cannot pass by accident.
var (
	precompJS  = []byte(strings.Repeat("export const shelf = () => console.log('shelf');\n", 200))
	precompCSS = []byte(strings.Repeat(".shelf{color:var(--ink);margin:0 auto}\n", 150))
	plainJS    = []byte("console.log('plain, as pnpm build alone leaves it')\n")
)

const (
	precompJSPath  = "/assets/app-abc123.js"
	precompCSSPath = "/assets/app-abc123.css"
	plainJSPath    = "/assets/plain-def456.js"
	brokenJSPath   = "/assets/broken-000000.js"
	immutable      = "public, max-age=31536000, immutable"
)

func gzipBytes(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := zw.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// precompEnv is a server whose dist looks like `make web` leaves it, plus one
// plain asset (a `pnpm build`-only dist), one truncated .gz, and two decoys
// outside assets/ that the fallback must never consult.
type precompEnv struct {
	*env
	jsGz, cssGz []byte
	fs          *countingFS
}

func newPrecompEnv(t *testing.T) *precompEnv {
	t.Helper()
	e := newEnv(t, withStatic())
	jsGz, cssGz := gzipBytes(t, precompJS), gzipBytes(t, precompCSS)
	index := "<!doctype html>\n<html lang=\"ko\">\n  <head>\n    <base href=\"/\" />\n    <title>SHELF</title>\n  </head>\n  <body><div id=\"root\"></div></body>\n</html>\n"
	mfs := fstest.MapFS{
		"index.html":                  {Data: []byte(index)},
		"index.html.gz":               {Data: gzipBytes(t, []byte("decoy: index.html is never served from a .gz"))},
		"series/x.gz":                 {Data: gzipBytes(t, []byte("decoy: a client-side route is never a .gz"))},
		"assets/app-abc123.js.gz":     {Data: jsGz},
		"assets/app-abc123.css.gz":    {Data: cssGz},
		"assets/plain-def456.js":      {Data: plainJS},
		"assets/broken-000000.js.gz":  {Data: jsGz[:len(jsGz)/2]},
		"assets/dir-only.js.gz/inner": {Data: []byte("a directory named like a .gz is not one")},
	}
	cfs := newCountingFS(mfs)
	e.srv.static = cfs
	return &precompEnv{env: e, jsGz: jsGz, cssGz: cssGz, fs: cfs}
}

func acceptEncoding(v string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("Accept-Encoding", v) }
}

func hasVaryAcceptEncoding(w *httptest.ResponseRecorder) bool {
	for _, v := range w.Header().Values("Vary") {
		for _, part := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(part), "Accept-Encoding") {
				return true
			}
		}
	}
	return false
}

// wantRepresentation asserts one complete 200 of a precompressed asset.
func wantRepresentation(t *testing.T, w *httptest.ResponseRecorder, ext string, gzipped bool, body []byte) {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %q", w.Code, w.Body.String())
	}
	h := w.Header()
	if gzipped {
		if got := h.Get("Content-Encoding"); got != "gzip" {
			t.Errorf("Content-Encoding = %q, want gzip", got)
		}
	} else if got := h.Values("Content-Encoding"); len(got) != 0 {
		t.Errorf("Content-Encoding = %q on the identity representation", got)
	}
	if !bytes.Equal(w.Body.Bytes(), body) {
		t.Errorf("body is %d bytes, want the %d-byte %s representation", w.Body.Len(), len(body), map[bool]string{true: "gzip", false: "identity"}[gzipped])
	}
	if got, want := h.Get("Content-Length"), strconv.Itoa(len(body)); got != want {
		t.Errorf("Content-Length = %q, want %q", got, want)
	}
	if got, want := h.Get("Content-Type"), mime.TypeByExtension(ext); got != want {
		t.Errorf("Content-Type = %q, want %q — the type of the original, not of the gzip bytes", got, want)
	}
	if got := h.Get("Cache-Control"); got != immutable {
		t.Errorf("Cache-Control = %q, want %q", got, immutable)
	}
	if !hasVaryAcceptEncoding(w) {
		t.Errorf("Vary = %q, want Accept-Encoding — a shared cache would otherwise hand gzip to a client that refused it", h.Values("Vary"))
	}
}

func TestPrecompressed_GzipClientGetsTheGzipBytes(t *testing.T) {
	e := newPrecompEnv(t)
	wantRepresentation(t, e.get(precompJSPath, acceptEncoding("gzip, deflate, br")), ".js", true, e.jsGz)
	wantRepresentation(t, e.get(precompCSSPath, acceptEncoding("gzip, deflate, br")), ".css", true, e.cssGz)
}

func TestPrecompressed_IdentityClientGetsTheOriginalBytes(t *testing.T) {
	e := newPrecompEnv(t)
	wantRepresentation(t, e.get(precompJSPath), ".js", false, precompJS)
	wantRepresentation(t, e.get(precompCSSPath), ".css", false, precompCSS)
}

// RFC 9110 §12.5.3. A malformed qvalue is treated as "not acceptable": the
// identity representation is always correct, gzip is only correct when asked
// for, so the conservative reading of an unreadable request is identity.
func TestPrecompressed_AcceptEncodingNegotiation(t *testing.T) {
	for _, tc := range []struct {
		header string
		gzip   bool
	}{
		{"gzip", true},
		{"gzip;q=0", false},
		{"gzip; q=0.000", false},
		{"*;q=0.5", true},
		{"*", true},
		{"*;q=0", false},
		{"gzip;q=0, *", false},
		{"*, gzip;q=0", false},
		{"GZIP; Q=0.3", true},
		{"  deflate ,\tgzip ; q=1.0 ", true},
		{"identity", false},
		{"x-gzip", true},
		{"br", false},
		{"br;q=1, identity;q=0.5", false},
		// Nothing is acceptable. RFC 9110 §12.5.3 lets a server disregard the
		// field rather than answer 406, and identity is the representation
		// that cannot be wrong, so that is what goes out — deliberately.
		{"gzip;q=0, identity;q=0", false},
		{"gzip;q=abc", false},
		{"gzip;q=1.5", false},
		{"gzip;q=-1", false},
		{"gzip;q=NaN", false},
		{"", false},
		{"gzipped", false},
	} {
		t.Run(tc.header, func(t *testing.T) {
			e := newPrecompEnv(t)
			w := e.get(precompJSPath, acceptEncoding(tc.header))
			if tc.gzip {
				wantRepresentation(t, w, ".js", true, e.jsGz)
			} else {
				wantRepresentation(t, w, ".js", false, precompJS)
			}
		})
	}
}

// Two Accept-Encoding field lines are one list (RFC 9110 §5.3).
func TestPrecompressed_AcceptEncodingAcrossFieldLines(t *testing.T) {
	e := newPrecompEnv(t)
	w := e.get(precompJSPath, func(r *http.Request) {
		r.Header.Add("Accept-Encoding", "br")
		r.Header.Add("Accept-Encoding", "gzip")
	})
	wantRepresentation(t, w, ".js", true, e.jsGz)
}

func TestPrecompressed_HEAD(t *testing.T) {
	e := newPrecompEnv(t)

	w := e.do(http.MethodHead, precompJSPath, nil, acceptEncoding("gzip"))
	if w.Code != http.StatusOK || w.Body.Len() != 0 {
		t.Fatalf("HEAD gzip = %d with %d body bytes, want 200 and none", w.Code, w.Body.Len())
	}
	if got := w.Header().Get("Content-Encoding"); got != "gzip" {
		t.Errorf("HEAD gzip Content-Encoding = %q", got)
	}
	if got, want := w.Header().Get("Content-Length"), strconv.Itoa(len(e.jsGz)); got != want {
		t.Errorf("HEAD gzip Content-Length = %q, want %q (the GET's)", got, want)
	}
	if !hasVaryAcceptEncoding(w) {
		t.Error("HEAD gzip has no Vary: Accept-Encoding")
	}

	w = e.do(http.MethodHead, precompJSPath, nil)
	if w.Code != http.StatusOK || w.Body.Len() != 0 {
		t.Fatalf("HEAD identity = %d with %d body bytes", w.Code, w.Body.Len())
	}
	if got := w.Header().Values("Content-Encoding"); len(got) != 0 {
		t.Errorf("HEAD identity Content-Encoding = %q", got)
	}
	if got, want := w.Header().Get("Content-Length"), strconv.Itoa(len(precompJS)); got != want {
		t.Errorf("HEAD identity Content-Length = %q, want %q", got, want)
	}
}

// A Range applies to the selected representation (RFC 9110 §14.1.2): to the
// gzip bytes when Content-Encoding is gzip, to the original otherwise.
func TestPrecompressed_Range(t *testing.T) {
	e := newPrecompEnv(t)
	byteRange := func(r *http.Request) { r.Header.Set("Range", "bytes=0-9") }

	w := e.get(precompJSPath, acceptEncoding("gzip"), byteRange)
	if w.Code != http.StatusPartialContent {
		t.Fatalf("gzip Range = %d, want 206", w.Code)
	}
	if got := w.Header().Get("Content-Encoding"); got != "gzip" {
		t.Errorf("gzip 206 Content-Encoding = %q", got)
	}
	if got, want := w.Header().Get("Content-Range"), "bytes 0-9/"+strconv.Itoa(len(e.jsGz)); got != want {
		t.Errorf("gzip Content-Range = %q, want %q", got, want)
	}
	if !bytes.Equal(w.Body.Bytes(), e.jsGz[:10]) {
		t.Errorf("gzip 206 body = %q, want the first ten gzip bytes", w.Body.Bytes())
	}
	if got := w.Header().Get("Content-Length"); got != "10" {
		t.Errorf("gzip 206 Content-Length = %q, want 10", got)
	}
	if !hasVaryAcceptEncoding(w) {
		t.Error("gzip 206 has no Vary: Accept-Encoding")
	}

	w = e.get(precompJSPath, byteRange)
	if w.Code != http.StatusPartialContent {
		t.Fatalf("identity Range = %d, want 206", w.Code)
	}
	if got := w.Header().Values("Content-Encoding"); len(got) != 0 {
		t.Errorf("identity 206 Content-Encoding = %q", got)
	}
	if got, want := w.Header().Get("Content-Range"), "bytes 0-9/"+strconv.Itoa(len(precompJS)); got != want {
		t.Errorf("identity Content-Range = %q, want %q", got, want)
	}
	if !bytes.Equal(w.Body.Bytes(), precompJS[:10]) {
		t.Errorf("identity 206 body = %q, want %q", w.Body.Bytes(), precompJS[:10])
	}
}

// A multi-range request on the gzip branch would make ServeContent build a
// multipart/byteranges envelope — plain text with MIME boundaries — that the
// wrapper would then label gzip, and a decoder would meet a boundary instead of
// a gzip header. The gzip branch ignores such a Range (RFC 9110 §14.2 lets a
// server ignore Range) and sends the whole encoded representation. The
// identity branch keeps multipart: its parts are ranges of plain bytes, which is
// exactly what multipart/byteranges carries.
//
// The GODEBUG is not decoration. go.mod says go 1.25, and for that language
// version go1.26's ServeContent defaults httpservecontentmaxranges to 1, i.e.
// it ignores every multi-range request itself — so without the override both
// branches answer 200 and the defect is invisible. 200 is go1.26's own default
// and what the binary gets the day go.mod moves; the identity half below fails
// loudly if the override ever stops reaching ServeContent.
func TestPrecompressed_MultiRange(t *testing.T) {
	t.Setenv("GODEBUG", "httpservecontentmaxranges=200")
	e := newPrecompEnv(t)
	const twoRanges = "bytes=0-9,20-29"

	r := httptest.NewRequest(http.MethodGet, precompJSPath, nil)
	r.RemoteAddr = "10.0.0.1:54321"
	r.Header.Set("Accept-Encoding", "gzip")
	r.Header.Set("Range", twoRanges)
	w := httptest.NewRecorder()
	e.srv.ServeHTTP(w, r)
	wantRepresentation(t, w, ".js", true, e.jsGz)
	if got := w.Header().Values("Content-Range"); len(got) != 0 {
		t.Errorf("gzip multi-range 200 carries Content-Range %q", got)
	}
	// The drop happens on a copy: a handler's caller (middleware, a retry,
	// the access log) still sees the request it sent.
	if got := r.Header.Get("Range"); got != twoRanges {
		t.Errorf("caller's Range header = %q after the request, want %q untouched", got, twoRanges)
	}

	w = e.get(precompJSPath, func(r *http.Request) { r.Header.Set("Range", twoRanges) })
	if w.Code != http.StatusPartialContent {
		t.Fatalf("identity multi-range = %d, want 206 — the multipart path is not live, so this test proves nothing", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "multipart/byteranges") {
		t.Errorf("identity multi-range Content-Type = %q, want multipart/byteranges", ct)
	}
	if got := w.Header().Values("Content-Encoding"); len(got) != 0 {
		t.Errorf("identity multi-range Content-Encoding = %q", got)
	}
	body := w.Body.String()
	if !strings.Contains(body, string(precompJS[0:10])) || !strings.Contains(body, string(precompJS[20:30])) {
		t.Errorf("identity multipart body does not carry both identity ranges: %q", body)
	}

	// One range is still a range on the gzip branch.
	w = e.get(precompJSPath, acceptEncoding("gzip"), func(r *http.Request) { r.Header.Set("Range", "bytes=20-29") })
	if w.Code != http.StatusPartialContent || !bytes.Equal(w.Body.Bytes(), e.jsGz[20:30]) {
		t.Errorf("gzip single range = %d %q, want 206 and gzip bytes 20-29", w.Code, w.Body.Bytes())
	}
	if got := w.Header().Get("Content-Encoding"); got != "gzip" {
		t.Errorf("gzip single-range 206 Content-Encoding = %q", got)
	}
}

// An error response has no representation to be encoded, so it must not say
// it is gzip: a client would try to inflate a plain-text error message.
func TestPrecompressed_UnsatisfiableRangeIsNotLabelledGzip(t *testing.T) {
	e := newPrecompEnv(t)
	w := e.get(precompJSPath, acceptEncoding("gzip"), func(r *http.Request) { r.Header.Set("Range", "bytes=999999-") })
	if w.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("status = %d, want 416", w.Code)
	}
	if got := w.Header().Values("Content-Encoding"); len(got) != 0 {
		t.Errorf("416 Content-Encoding = %q, want none", got)
	}
}

// index.html is rewritten per request by serveIndex and never comes from a
// .gz, even when one sits next to it; neither does a client-side route.
func TestPrecompressed_IndexAndRoutesNeverNegotiate(t *testing.T) {
	e := newPrecompEnv(t)
	for _, target := range []string{"/", "/index.html", "/series/x"} {
		w := e.get(target, acceptEncoding("gzip"))
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", target, w.Code)
		}
		if !strings.Contains(w.Body.String(), `<div id="root">`) {
			t.Errorf("GET %s did not serve the SPA shell: %q", target, w.Body.String())
		}
		if got := w.Header().Values("Content-Encoding"); len(got) != 0 {
			t.Errorf("GET %s Content-Encoding = %q", target, got)
		}
		if got := w.Header().Values("Vary"); len(got) != 0 {
			t.Errorf("GET %s Vary = %q; the response does not vary", target, got)
		}
	}
}

// A dist built by `pnpm build` alone (no precompress) is served exactly as
// before E-63.
func TestPrecompressed_PlainAssetIsUnchanged(t *testing.T) {
	e := newPrecompEnv(t)
	w := e.get(plainJSPath, acceptEncoding("gzip"))
	if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), plainJS) {
		t.Fatalf("GET plain = %d %q", w.Code, w.Body.String())
	}
	if got := w.Header().Values("Content-Encoding"); len(got) != 0 {
		t.Errorf("plain asset Content-Encoding = %q", got)
	}
	if got := w.Header().Values("Vary"); len(got) != 0 {
		t.Errorf("plain asset Vary = %q", got)
	}
	if got := w.Header().Get("Cache-Control"); got != immutable {
		t.Errorf("plain asset Cache-Control = %q", got)
	}
}

func TestPrecompressed_MissingAssetIs404(t *testing.T) {
	e := newPrecompEnv(t)
	errorBody(t, e.get("/assets/gone-deadbeef.js", acceptEncoding("gzip")), http.StatusNotFound, CodeNotFound)
	errorBody(t, e.get("/assets/gone-deadbeef.js"), http.StatusNotFound, CodeNotFound)
	// A directory that happens to be named like the .gz is not the asset.
	errorBody(t, e.get("/assets/dir-only.js", acceptEncoding("gzip")), http.StatusNotFound, CodeNotFound)
	errorBody(t, e.get("/assets/dir-only.js"), http.StatusNotFound, CodeNotFound)
}

// The identity representation costs one inflate per Server, not one per
// request. Counting reads of the .gz handle is the observable: the cached path
// may stat the file but never reads it again.
func TestPrecompressed_IdentityIsDecompressedOnce(t *testing.T) {
	e := newPrecompEnv(t)
	const gz = "assets/app-abc123.js.gz"

	wantRepresentation(t, e.get(precompJSPath), ".js", false, precompJS)
	first := e.fs.readsOf(gz)
	if first == 0 {
		t.Fatal("the first identity request read nothing from the .gz; the counter is not wired")
	}
	wantRepresentation(t, e.get(precompJSPath), ".js", false, precompJS)
	wantRepresentation(t, e.get(precompJSPath, acceptEncoding("br")), ".js", false, precompJS)
	if got := e.fs.readsOf(gz); got != first {
		t.Errorf("reads of the .gz went %d -> %d over two more identity requests; want no re-inflate", first, got)
	}
}

func TestPrecompressed_CorruptGzip(t *testing.T) {
	e := newPrecompEnv(t)

	// An identity client needs the inflated bytes, and there are none.
	for range 2 { // the second answer comes from the cached failure
		w := e.get(brokenJSPath)
		errorBody(t, w, http.StatusInternalServerError, CodeInternal)
		if got := w.Header().Get("Cache-Control"); got == immutable {
			t.Error("a 500 carries the asset's immutable Cache-Control")
		}
		if got := w.Header().Values("Content-Encoding"); len(got) != 0 {
			t.Errorf("a 500 claims Content-Encoding %q", got)
		}
	}

	// A gzip client is handed the stored bytes untouched: the server does not
	// inflate on this path and so cannot know they are damaged.
	w := e.get(brokenJSPath, acceptEncoding("gzip"))
	if w.Code != http.StatusOK {
		t.Fatalf("gzip client on a corrupt .gz = %d, want 200", w.Code)
	}
	if got := w.Header().Get("Content-Encoding"); got != "gzip" {
		t.Errorf("Content-Encoding = %q", got)
	}
}

// The first identity requests for an asset arrive together (a page load asks
// for every chunk at once). They must share one inflate. What is counted is the
// number of distinct .gz handles anything Read from: every request opens its own
// handle, and only an inflate reads one, so that number is the number of
// inflations whatever buffering gzip.Reader does. The first Read sleeps to hold
// the window open, and a start barrier releases every request at once.
// Run under -race by the orchestrator as well.
func TestPrecompressed_ConcurrentFirstIdentityInflatesOnce(t *testing.T) {
	e := newPrecompEnv(t)
	const gz = "assets/app-abc123.js.gz"
	e.fs.slowFirstRead(gz, 50*time.Millisecond)

	const n = 16
	bodies := make([][]byte, n)
	codes := make([]int, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := httptest.NewRequest(http.MethodGet, precompJSPath, nil)
			r.RemoteAddr = "10.0.0.1:54321"
			w := httptest.NewRecorder()
			<-start
			e.srv.ServeHTTP(w, r)
			codes[i], bodies[i] = w.Code, w.Body.Bytes()
		}()
	}
	close(start)
	wg.Wait()
	for i := range n {
		if codes[i] != http.StatusOK || !bytes.Equal(bodies[i], precompJS) {
			t.Errorf("request %d = %d with %d bytes, want 200 and the %d-byte original", i, codes[i], len(bodies[i]), len(precompJS))
		}
	}
	if got := e.fs.handlesReadOf(gz); got != 1 {
		t.Errorf("%d .gz handles were read by %d simultaneous first requests, want exactly 1 inflate", got, n)
	}
}

// Two servers whose dists hold the same asset name with different bytes each
// answer with their own: a cache keyed by file name alone would hand the second
// server the first one's body.
func TestPrecompressed_IdentityCacheIsPerServer(t *testing.T) {
	const name = "assets/same-111111.js"
	other := []byte(strings.Repeat("export const other = 2;\n", 120))
	mk := func(body []byte) *env {
		e := newEnv(t, withStatic())
		e.srv.static = fstest.MapFS{
			"index.html":    {Data: []byte("<html><head></head><body></body></html>")},
			name + gzSuffix: {Data: gzipBytes(t, body)},
		}
		return e
	}
	a, b := mk(precompJS), mk(other)
	wantRepresentation(t, a.get("/"+name), ".js", false, precompJS)
	wantRepresentation(t, b.get("/"+name), ".js", false, other)
	wantRepresentation(t, a.get("/"+name), ".js", false, precompJS)
}

// The identity cache belongs to the handler, so it goes when the Server goes.
// A cache that outlives its Server keeps the Server (and every inflated asset)
// reachable for the life of the process.
func TestPrecompressed_IdentityCacheDiesWithTheServer(t *testing.T) {
	wp := func() weak.Pointer[Server] {
		s, err := New(Options{
			Config: &config.Config{},
			Static: fstest.MapFS{"assets/gc-222222.js.gz": {Data: gzipBytes(t, precompJS)}},
		})
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/assets/gc-222222.js", nil))
		if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), precompJS) {
			t.Fatalf("identity request = %d with %d bytes; the cache was never populated", w.Code, w.Body.Len())
		}
		return weak.Make(s)
	}()
	for range 10 {
		runtime.GC()
		if wp.Value() == nil {
			return
		}
	}
	t.Fatal("the Server is still reachable after its last reference was dropped; something package-level holds it")
}

// --- counting filesystem ---------------------------------------------------

// countingFS counts, per opened name, Read calls and the number of distinct
// handles that were Read at all. It keeps the io.Seeker the server needs from
// an embedded file.
type countingFS struct {
	fs.FS
	mu          sync.Mutex
	reads       map[string]int
	handlesRead map[string]int
	slowName    string
	slowFor     time.Duration
}

func newCountingFS(inner fs.FS) *countingFS {
	return &countingFS{FS: inner, reads: map[string]int{}, handlesRead: map[string]int{}}
}

// slowFirstRead makes the first Read of name, across all handles, sleep d.
func (c *countingFS) slowFirstRead(name string, d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.slowName, c.slowFor = name, d
}

func (c *countingFS) Open(name string) (fs.File, error) {
	f, err := c.FS.Open(name)
	if err != nil {
		return nil, err
	}
	return &countingFile{File: f, owner: c, name: name}, nil
}

func (c *countingFS) readsOf(name string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reads[name]
}

func (c *countingFS) handlesReadOf(name string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.handlesRead[name]
}

// countingFile is one handle; a handle is used by one request's goroutine, so
// readAny needs no lock of its own.
type countingFile struct {
	fs.File
	owner   *countingFS
	name    string
	readAny bool
}

func (f *countingFile) Read(p []byte) (int, error) {
	c := f.owner
	c.mu.Lock()
	first := c.reads[f.name] == 0
	c.reads[f.name]++
	if !f.readAny {
		f.readAny = true
		c.handlesRead[f.name]++
	}
	var sleep time.Duration
	if first && f.name == c.slowName {
		sleep = c.slowFor
	}
	c.mu.Unlock()
	if sleep > 0 {
		time.Sleep(sleep)
	}
	return f.File.Read(p)
}

func (f *countingFile) Seek(offset int64, whence int) (int64, error) {
	s, ok := f.File.(io.Seeker)
	if !ok {
		return 0, fs.ErrInvalid
	}
	return s.Seek(offset, whence)
}
