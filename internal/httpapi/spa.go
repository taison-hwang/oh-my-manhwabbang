package httpapi

import (
	"bytes"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// indexFile is the SPA entry document inside the embedded filesystem.
const indexFile = "index.html"

// assetPrefix is Vite's hashed-output directory. Everything under it carries a
// content hash in its file name, which is the only thing that makes
// `immutable` honest for a static asset — exactly the argument arch §5.3 makes
// for `?v=` on pages (D-17).
const assetPrefix = "assets/"

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
func (s *Server) spaHandler() http.Handler {
	return s.get(s.serveStatic)
}

// serveStatic resolves one static request.
//
// The resolution order is: an existing file wins; anything else is a
// client-side route and gets index.html so a deep link like /series/{sid}
// survives a reload. `/api/*` never reaches here — router.go registers an
// explicit catch-all for it — because answering an unknown API call with HTML
// makes the frontend report a JSON parse error instead of a 404.
func (s *Server) serveStatic(w http.ResponseWriter, r *http.Request) error {
	if s.static == nil {
		return s.servePlaceholder(w, r)
	}
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "" || name == indexFile {
		return s.serveIndex(w, r)
	}

	f, err := s.static.Open(name)
	if err != nil {
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
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
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
