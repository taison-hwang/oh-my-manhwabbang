package httpapi

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// indexFile is the SPA entry document inside the embedded filesystem.
const indexFile = "index.html"

// assetPrefix is Vite's hashed-output directory. Everything under it carries a
// content hash in its file name, which is the only thing that makes
// `immutable` honest for a static asset — exactly the argument arch §5.3 makes
// for `?v=` on pages (D-17).
const assetPrefix = "assets/"

// assetCacheControl is the policy for every file under assetPrefix, whichever
// form it is stored in.
const assetCacheControl = "public, max-age=31536000, immutable"

// baseHrefPattern matches the `<base href="…">` placeholder web/index.html
// ships. It is rewritten per request rather than at build time so one binary
// serves correctly whether it is mounted at `/` or at `/reader/`
// (NFR-SEC-003, arch §8.3).
var baseHrefPattern = regexp.MustCompile(`<base\s+href="[^"]*"\s*/?>`)

// placeholderBody is what a binary built without `pnpm build` serves at the
// root. `go build ./...` on a clean checkout produces exactly that (web/dist
// holds only .gitkeep), and a developer meeting a blank 404 would reasonably
// conclude the server is broken rather than that the frontend is unbuilt.
const placeholderBody = "SHELF: the frontend has not been built.\n" +
	"Run `make web` (or `cd web && pnpm build`) and rebuild.\n"

// spaHandler serves the embedded SPA and the client-side route fallback.
//
// routes() calls it once, so the spaAssets made here lives exactly as long as
// the route table, which is to say the Server: nothing outside the handler
// chain can reach it. It starts empty and is filled on first request, so a test
// that swaps s.static after New still sees only its own files.
func (s *Server) spaHandler() http.Handler {
	a := new(spaAssets)
	return s.get(func(w http.ResponseWriter, r *http.Request) error {
		return s.serveStatic(w, r, a)
	})
}

// serveStatic resolves one static request.
//
// The resolution order is: an existing file wins; anything else is a
// client-side route and gets index.html so a deep link like /series/{sid}
// survives a reload. `/api/*` never reaches here — router.go registers an
// explicit catch-all for it — because answering an unknown API call with HTML
// makes the frontend report a JSON parse error instead of a 404.
func (s *Server) serveStatic(w http.ResponseWriter, r *http.Request, a *spaAssets) error {
	if s.static == nil {
		return s.servePlaceholder(w, r)
	}
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "" || name == indexFile {
		return s.serveIndex(w, r)
	}

	f, err := s.static.Open(name)
	if err != nil {
		if strings.HasPrefix(name, assetPrefix) && errors.Is(err, fs.ErrNotExist) {
			// A release dist holds assets/x.js only as assets/x.js.gz (E-63).
			// servePrecompressed answers the 404 itself when there is no .gz
			// either.
			return s.servePrecompressed(w, r, a, name)
		}
		if strings.HasPrefix(name, assetPrefix) {
			// A missing hashed asset is a genuine 404, not a client-side route.
			// Falling back to index.html here would answer a `<script src>`
			// with HTML, and the browser would report a MIME type error
			// instead of the missing file.
			return notFound("no such asset: %s", name)
		}
		// Not a file: a client-side route. The SPA decides whether it exists.
		return s.serveIndex(w, r)
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil || info.IsDir() {
		return s.serveIndex(w, r)
	}
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		return internalErr(nil)
	}

	if ct := mime.TypeByExtension(path.Ext(name)); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	if strings.HasPrefix(name, assetPrefix) {
		// The file name carries a content hash, so the bytes at this URL can
		// never change.
		w.Header().Set("Cache-Control", assetCacheControl)
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	// An embedded file has no meaningful mtime, so ServeContent is given a zero
	// time and therefore emits no Last-Modified: a wrong validator is worse
	// than none.
	http.ServeContent(w, r, name, time.Time{}, rs)
	return nil
}

// serveIndex writes index.html with `<base href>` rewritten to the configured
// base path.
//
// It is explicitly not cacheable: index.html is the one file whose bytes change
// on every deploy while its URL does not, and a cached copy pointing at
// last week's hashed bundle is a white screen.
func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request) error {
	raw, err := fs.ReadFile(s.static, indexFile)
	if err != nil {
		return s.servePlaceholder(w, r)
	}
	body := s.injectBaseHref(raw)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
	return nil
}

// injectBaseHref rewrites the placeholder to `{base_path}/`.
//
// The trailing slash is not optional: a `<base href="/reader">` makes the
// browser resolve `assets/x.js` against `/` rather than `/reader/`, and every
// asset 404s. web/index.html carries the same warning next to the placeholder.
func (s *Server) injectBaseHref(raw []byte) []byte {
	href := s.base + "/"
	replacement := []byte(`<base href="` + href + `" />`)
	if baseHrefPattern.Match(raw) {
		return baseHrefPattern.ReplaceAll(raw, replacement)
	}
	// No placeholder: insert one directly after <head> so a hand-written or
	// differently-built index.html still resolves its assets correctly.
	if i := bytes.Index(raw, []byte("<head>")); i >= 0 {
		out := make([]byte, 0, len(raw)+len(replacement))
		out = append(out, raw[:i+len("<head>")]...)
		out = append(out, replacement...)
		out = append(out, raw[i+len("<head>"):]...)
		return out
	}
	return raw
}

// servePlaceholder answers when there is no built frontend at all.
func (s *Server) servePlaceholder(w http.ResponseWriter, _ *http.Request) error {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(placeholderBody)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(placeholderBody))
	return nil
}

// --- precompressed assets (decision E-63) -----------------------------------
//
// `make web` runs scripts/precompress after `pnpm build`, which replaces every
// assets/**/*.js and *.css with a gzip file of the same name plus ".gz" and
// removes the original. go:embed stores bytes verbatim, so that is the only way
// the release binary stops carrying the SPA's text uncompressed.
//
// The fallback below is therefore reached only when the original is absent: a
// dist built by `pnpm build` alone (`make web-gates`, `vite build` in a dev
// loop) still has the plain file and is served exactly as before, with no
// Content-Encoding and no Vary. It is confined to assets/ because those names
// carry a content hash; index.html is rewritten per request by serveIndex and
// never comes from a .gz, and a client-side route is never a file at all.

// gzSuffix is what scripts/precompress appends; it carries the same constant.
const gzSuffix = ".gz"

// servePrecompressed serves name from name+".gz", or answers the asset 404.
//
// Both representations go through ServeContent, so Range, HEAD and the
// conditional headers keep the semantics they have for a plain asset. A Range
// on the gzip response addresses the gzip bytes, which is what RFC 9110
// §14.1.2 says a range means once a content coding is applied — the
// representation is the encoded one. A multi-range request is the exception on
// the gzip branch: see singleRangeOnly.
//
// A client that refuses both gzip and identity (`gzip;q=0, identity;q=0`,
// `*;q=0`) still gets identity, not 406, deliberately: RFC 9110 §12.5.3 lets a
// server disregard the field, and identity is the one answer that cannot be
// wrong.
func (s *Server) servePrecompressed(w http.ResponseWriter, r *http.Request, a *spaAssets, name string) error {
	f, err := s.static.Open(name + gzSuffix)
	if err != nil {
		return notFound("no such asset: %s", name)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return notFound("no such asset: %s", name)
	}
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		return internalErr(nil)
	}

	gzipOK := acceptsGzip(r.Header)
	var identity []byte
	if !gzipOK {
		// Inflate before a single header is set: a failure here becomes the
		// 500 envelope, and writeError adds to whatever headers are already
		// on w — an `immutable` Cache-Control on an error would pin it in
		// every cache for a year.
		identity, err = a.inflated(name, rs)
		if err != nil {
			return internalErr(err)
		}
	}

	h := w.Header()
	// Vary goes on both branches. A shared cache that stored the gzip answer
	// without it would hand gzip to the next client that refused it, and one
	// that stored the identity answer would cost every later client the
	// saving. Add, not Set, so a Vary some middleware already wrote survives.
	h.Add("Vary", "Accept-Encoding")
	// The type of the original, never of the stored bytes. An explicit
	// fallback keeps ServeContent from sniffing: sniffing gzip yields
	// application/x-gzip, and a browser refuses to run that as a script.
	ct := mime.TypeByExtension(path.Ext(name))
	if ct == "" {
		ct = "application/octet-stream"
	}
	h.Set("Content-Type", ct)
	h.Set("Cache-Control", assetCacheControl)

	if gzipOK {
		http.ServeContent(&gzipOnSuccess{ResponseWriter: w}, singleRangeOnly(r), name, time.Time{}, rs)
		return nil
	}
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(identity))
	return nil
}

// gzipOnSuccess adds `Content-Encoding: gzip` at the moment the status is
// written, and only for 200 and 206.
//
// Setting the header before ServeContent instead would cost two things,
// both read off net/http/fs.go (go1.25 and go1.26 alike):
//   - serveContent omits Content-Length on a full response when
//     Content-Encoding is already set — a guard for handlers that compress on
//     the fly — so every gzip 200 would go out chunked with no length;
//   - its error paths (416, and 304's writeNotModified) delete
//     Content-Encoding themselves, which is correct only as long as that
//     stdlib behaviour (GODEBUG httpservecontentkeepheaders) holds.
//
// Deciding at WriteHeader keeps both independent of those details: the length
// ServeContent computes is the length of the bytes it copies, which are the
// gzip bytes, and a plain-text error body is never labelled gzip.
//
// It forwards nothing but Write and WriteHeader. ServeContent copies a
// known-length body with io.CopyN and never flushes, hijacks or sets
// deadlines, so the wrapper hides no capability this path uses; Unwrap is
// there so http.ResponseController still reaches the real writer should a
// middleware above ever ask.
type gzipOnSuccess struct {
	http.ResponseWriter
	wroteHeader bool
}

func (g *gzipOnSuccess) WriteHeader(code int) {
	if !g.wroteHeader {
		g.wroteHeader = true
		if code == http.StatusOK || code == http.StatusPartialContent {
			g.Header().Set("Content-Encoding", "gzip")
		}
	}
	g.ResponseWriter.WriteHeader(code)
}

func (g *gzipOnSuccess) Write(b []byte) (int, error) {
	if !g.wroteHeader {
		g.WriteHeader(http.StatusOK)
	}
	return g.ResponseWriter.Write(b)
}

func (g *gzipOnSuccess) Unwrap() http.ResponseWriter { return g.ResponseWriter }

// singleRangeOnly returns r, or a clone of it without Range and If-Range when
// Range asks for more than one range.
//
// ServeContent answers a multi-range request with a multipart/byteranges
// envelope: MIME boundaries and part headers in plain text around slices of
// the stored bytes. gzipOnSuccess would label that envelope gzip, and a
// decoder would meet a boundary where it expects a gzip header. There is no
// correct way to send it either — Content-Encoding applies to the whole
// message content, and a part has no header that says its slice is gzip — so
// the range goes instead. RFC 9110 §14.2 lets a server ignore Range, and the
// whole encoded representation as a 200 is always right.
//
// It does not rely on ServeContent ignoring such requests itself: go1.26 does,
// but only through the GODEBUG default for go.mod's language version
// (httpservecontentmaxranges=1 below go 1.26); the day go.mod moves, the
// envelope comes back. A clone rather than a header edit, because r belongs to
// the caller — middleware above may still read it. Commas are counted the
// way ServeContent splits the list, skipping empty members, so `bytes=0-9,` is
// still the single range it parses.
func singleRangeOnly(r *http.Request) *http.Request {
	spec, ok := strings.CutPrefix(r.Header.Get("Range"), "bytes=")
	if !ok {
		return r // absent, or a unit ServeContent rejects with 416 anyway
	}
	n := 0
	for _, member := range strings.Split(spec, ",") {
		if strings.TrimSpace(member) != "" {
			n++
		}
	}
	if n <= 1 {
		return r
	}
	c := r.Clone(r.Context())
	c.Header.Del("Range")
	c.Header.Del("If-Range")
	return c
}

// inflatedEntry is one asset's identity representation, computed once.
type inflatedEntry struct {
	once sync.Once
	body []byte
	err  error
}

// spaAssets is the state one spaHandler keeps across requests: name →
// *inflatedEntry for the identity representations computed so far. Its zero
// value is ready to use.
type spaAssets struct {
	identity sync.Map
}

// inflated returns name's decompressed bytes, inflating gz at most once per
// handler — once per Server, since routes() builds one.
//
// Only names whose .gz exists and is a regular file reach here, so the cache
// is bounded by the embedded file count; a request for a made-up name 404s
// before it can add an entry. A failure is cached along with success: the
// data is embedded and cannot change under a running process, so retrying
// would only repeat the work and the 500 (which is logged each time).
func (a *spaAssets) inflated(name string, gz io.Reader) ([]byte, error) {
	v, _ := a.identity.LoadOrStore(name, new(inflatedEntry))
	e := v.(*inflatedEntry)
	e.once.Do(func() {
		zr, err := gzip.NewReader(gz)
		if err != nil {
			e.err = err
			return
		}
		// Reading to EOF is what checks the CRC-32 and length trailer;
		// zr.Close does not.
		e.body, e.err = io.ReadAll(zr)
		if e.err != nil {
			e.body = nil
		}
	})
	return e.body, e.err
}

// acceptsGzip reports whether Accept-Encoding (RFC 9110 §12.5.3) admits the
// gzip coding.
//
// gzip is acceptable iff an explicit `gzip` or `x-gzip` entry has q > 0, or
// there is no explicit entry and a `*` entry has q > 0; an explicit `gzip;q=0`
// beats `*`. Codings and parameter names are case-insensitive and whitespace
// around list members and parameters is ignored. A qvalue that does not match
// the RFC's grammar (`0[.ddd]` or `1[.000]`) counts as 0: the identity
// representation is correct for every client and gzip only for one that asked,
// so an unreadable request gets the one that cannot be wrong — which is also
// why refusing identity too is answered with identity rather than 406 (see
// servePrecompressed). No header at all
// means identity — RFC 9110 permits any coding then, but curl and other plain
// clients send none and expect bytes they can read.
func acceptsGzip(h http.Header) bool {
	explicit, star := -1.0, -1.0 // highest q seen; -1 = no such entry
	for _, field := range h.Values("Accept-Encoding") {
		for _, member := range strings.Split(field, ",") {
			coding, params, _ := strings.Cut(member, ";")
			coding = strings.ToLower(strings.TrimSpace(coding))
			q := qvalue(params)
			switch coding {
			case "gzip", "x-gzip":
				explicit = max(explicit, q)
			case "*":
				star = max(star, q)
			}
		}
	}
	if explicit >= 0 {
		return explicit > 0
	}
	return star > 0
}

// qvaluePattern is RFC 9110 §12.4.2's qvalue.
var qvaluePattern = regexp.MustCompile(`^(?:0(?:\.[0-9]{0,3})?|1(?:\.0{0,3})?)$`)

// qvalue extracts q from a member's parameters: 1 when absent, 0 when
// malformed.
func qvalue(params string) float64 {
	q := 1.0
	for _, p := range strings.Split(params, ";") {
		k, v, _ := strings.Cut(p, "=")
		if !strings.EqualFold(strings.TrimSpace(k), "q") {
			continue
		}
		v = strings.TrimSpace(v)
		if !qvaluePattern.MatchString(v) {
			return 0
		}
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return 0
		}
		q = f
	}
	return q
}
