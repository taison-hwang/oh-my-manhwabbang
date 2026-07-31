//go:build !nopdf

package httpapi

import (
	"bytes"
	"fmt"
	"image/jpeg"
	"net/http"
	"strings"
	"testing"

	"shelf/internal/pdfium"
)

// The rendered-PDF half of `GET /api/books/{bid}/pages/{n}` (arch §7.6,
// FR-SRV-006, AC-004).
//
// Everything in this file needs a real document and a live rasteriser, so the
// environment is built with `withPDF()`. Without it the handler stops at
// `501 unsupported` and the render path — the `r1-` ETag of §5.3, the `w`
// clamp, the `?v=` matrix and the render cache — never executes at all.
//
// `-tags nopdf` compiles the file out: in that build `pdf.enabled` is
// overridden by `pdfium.Supported() == false` and 501 is the only correct
// answer, which pages_test.go's TestPage_pdfWithoutSupportIs501 already pins.

// decodeJPEG fails the test unless the body really is a JPEG, and reports its
// width — the one thing that proves `w` was honoured rather than echoed.
func decodeJPEG(t *testing.T, body []byte) (width, height int) {
	t.Helper()
	img, err := jpeg.Decode(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("the response body is not a JPEG: %v", err)
	}
	b := img.Bounds()
	return b.Dx(), b.Dy()
}

// A PDF page comes back as a JPEG with the §5.3 raster validator, and the
// viewer cannot tell it from a ZIP page.
func TestPDFPage_rendersJPEGWithTheRasterETag(t *testing.T) {
	e := newEnv(t, withPDF())
	target := "/api/books/" + e.bookPDFID + "/pages/1"

	w := e.get(target)
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", target, w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "image/jpeg" {
		t.Errorf("Content-Type = %q, want image/jpeg", got)
	}
	if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}

	// arch §5.3: pdf raster "r1-<book_id>-<page_no>-<width>-<cv>". The default
	// width is `pdf.default_width` snapped to 100 px.
	want := fmt.Sprintf(`"r1-%s-1-%d-%s"`,
		e.bookPDFID, pdfium.SnapWidth(e.cfg.PDF.DefaultWidth, e.cfg.PDF.MaxWidth), cvPDF)
	if got := w.Header().Get("ETag"); got != want {
		t.Errorf("ETag = %s, want %s", got, want)
	}

	gotW, gotH := decodeJPEG(t, w.Body.Bytes())
	if gotW != pdfium.SnapWidth(e.cfg.PDF.DefaultWidth, e.cfg.PDF.MaxWidth) {
		t.Errorf("rendered width = %d, want the default %d", gotW,
			pdfium.SnapWidth(e.cfg.PDF.DefaultWidth, e.cfg.PDF.MaxWidth))
	}
	// A4 at 595x842: the height must follow the page's aspect ratio, which is
	// what proves the render used the document rather than a blank canvas.
	wantH := gotW * pdfPageHeight / pdfPageWidth
	if gotH < wantH-2 || gotH > wantH+2 {
		t.Errorf("rendered height = %d, want ~%d for a %dx%d page", gotH, wantH, pdfPageWidth, pdfPageHeight)
	}

	// Every page of the fixture is filled with a different colour, so a handler
	// that ignored `n` and always rendered page 1 would return the same bytes.
	other := e.get("/api/books/" + e.bookPDFID + "/pages/2")
	if other.Code != http.StatusOK {
		t.Fatalf("GET page 2 = %d: %s", other.Code, other.Body.String())
	}
	if bytes.Equal(other.Body.Bytes(), w.Body.Bytes()) {
		t.Error("pages 1 and 2 rendered identical bytes; the page number is being ignored")
	}
	if got := other.Header().Get("ETag"); !strings.Contains(got, "-2-") {
		t.Errorf("page 2 ETag = %s, want the page number in it", got)
	}
}

// FR-SRV-006 / arch §7.6: `w` is "clamped to `pdf.max_width`, snapped to
// 100 px". Both halves are observable in the ETag and in the pixels.
func TestPDFPage_widthIsSnappedAndClamped(t *testing.T) {
	e := newEnv(t, withPDF())
	maxW := e.cfg.PDF.MaxWidth

	cases := []struct {
		name string
		w    string
		want int
	}{
		{"snapped down to the nearest 100", "449", pdfium.SnapWidth(449, maxW)},
		{"snapped up to the nearest 100", "451", pdfium.SnapWidth(451, maxW)},
		{"an exact multiple is unchanged", "800", 800},
		{"clamped to pdf.max_width", "99999", maxW},
		{"never below the floor", "1", pdfium.MinWidth},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := e.get("/api/books/" + e.bookPDFID + "/pages/1?w=" + tc.w)
			if w.Code != http.StatusOK {
				t.Fatalf("w=%s = %d: %s", tc.w, w.Code, w.Body.String())
			}
			wantTag := fmt.Sprintf(`"r1-%s-1-%d-%s"`, e.bookPDFID, tc.want, cvPDF)
			if got := w.Header().Get("ETag"); got != wantTag {
				t.Errorf("ETag = %s, want %s", got, wantTag)
			}
			if gotW, _ := decodeJPEG(t, w.Body.Bytes()); gotW != tc.want {
				t.Errorf("rendered width = %d, want %d", gotW, tc.want)
			}
		})
	}

	// A `w` that is not a number at all is a client mistake, not a width.
	body := errorBody(t, e.get("/api/books/"+e.bookPDFID+"/pages/1?w=wide"),
		http.StatusBadRequest, CodeBadRequest)
	if body.Detail["param"] != "w" {
		t.Errorf("detail.param = %v, want \"w\"", body.Detail["param"])
	}

	// Snapping is what keeps a dragged zoom slider from spawning a hundred
	// distinct renders: two nearby widths must produce one cache entry.
	if _, ok := e.thumbs.LookupPDFPage(e.bookPDFID, 1, pdfium.SnapWidth(449, maxW), cvPDF); !ok {
		t.Error("no cached render for the snapped width")
	}
	if _, ok := e.thumbs.LookupPDFPage(e.bookPDFID, 1, 449, cvPDF); ok {
		t.Error("a render was cached under the unsnapped width 449")
	}
}

// arch §5.3's `?v=` matrix applies to a rendered page exactly as it does to a
// stored one, and `304` still works.
func TestPDFPage_versionMatrixAndConditionalGet(t *testing.T) {
	e := newEnv(t, withPDF())
	target := "/api/books/" + e.bookPDFID + "/pages/1"

	t.Run("v matches the cv", func(t *testing.T) {
		w := e.get(target + "?v=" + cvPDF)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", w.Code, w.Body.String())
		}
		if got := w.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
			t.Errorf("Cache-Control = %q, want immutable", got)
		}
	})

	t.Run("v absent", func(t *testing.T) {
		w := e.get(target)
		if got := w.Header().Get("Cache-Control"); got != "public, max-age=60, must-revalidate" {
			t.Errorf("Cache-Control = %q, want the short window", got)
		}
	})

	t.Run("v is stale", func(t *testing.T) {
		w := e.get(target + "?v=00000000deadbeef")
		body := errorBody(t, w, http.StatusConflict, CodeStaleVersion)
		if body.Detail["cv"] != cvPDF {
			t.Errorf("detail.cv = %v, want %q", body.Detail["cv"], cvPDF)
		}
	})

	t.Run("If-None-Match is 304 with an empty body", func(t *testing.T) {
		first := e.get(target)
		etag := first.Header().Get("ETag")
		second := e.get(target, func(r *http.Request) { r.Header.Set("If-None-Match", etag) })
		if second.Code != http.StatusNotModified {
			t.Fatalf("If-None-Match = %d, want 304", second.Code)
		}
		if second.Body.Len() != 0 {
			t.Errorf("a 304 carries %d bytes of body", second.Body.Len())
		}
	})
}

// A render costs ~300 ms and is perfectly reproducible, so it is cached under
// <cache_dir>/pdf keyed by (book, page, width, cv) — the same structural
// invalidation as a thumbnail (arch §5.6, D-19).
func TestPDFPage_rendersAreCached(t *testing.T) {
	e := newEnv(t, withPDF())
	target := "/api/books/" + e.bookPDFID + "/pages/3?w=400"

	if _, ok := e.thumbs.LookupPDFPage(e.bookPDFID, 3, 400, cvPDF); ok {
		t.Fatal("the render cache is already populated before the first request")
	}

	first := e.get(target)
	if first.Code != http.StatusOK {
		t.Fatalf("first render = %d: %s", first.Code, first.Body.String())
	}
	res, ok := e.thumbs.LookupPDFPage(e.bookPDFID, 3, 400, cvPDF)
	if !ok {
		t.Fatal("the render was not cached")
	}
	if !strings.Contains(res.Path, "/pdf/") {
		t.Errorf("the render was cached at %q, want it under <cache_dir>/pdf", res.Path)
	}

	// The second request is served from that file and must be byte-identical.
	second := e.get(target)
	if second.Code != http.StatusOK {
		t.Fatalf("cached render = %d: %s", second.Code, second.Body.String())
	}
	if !bytes.Equal(first.Body.Bytes(), second.Body.Bytes()) {
		t.Error("the cached render differs from the freshly rendered bytes")
	}
	if got := second.Header().Get("ETag"); got != first.Header().Get("ETag") {
		t.Errorf("the cache-hit ETag %s differs from the render ETag %s", got, first.Header().Get("ETag"))
	}
	if got := second.Header().Get("Content-Type"); got != "image/jpeg" {
		t.Errorf("the cache-hit Content-Type = %q, want image/jpeg", got)
	}

	// The cache key carries the content version, so a book whose bytes changed
	// can never be answered from the old render (D-19).
	if _, ok := e.thumbs.LookupPDFPage(e.bookPDFID, 3, 400, "ffffffffffffffff"); ok {
		t.Error("a render was found under a different content version")
	}
}

// The page bounds of arch §7.6 hold for a PDF exactly as for an archive.
func TestPDFPage_boundsAre1Based(t *testing.T) {
	e := newEnv(t, withPDF())
	base := "/api/books/" + e.bookPDFID + "/pages/"

	if w := e.get(base + fmt.Sprint(pdfPageCount)); w.Code != http.StatusOK {
		t.Errorf("the last page = %d, want 200: %s", w.Code, w.Body.String())
	}
	errorBody(t, e.get(base+fmt.Sprint(pdfPageCount+1)), http.StatusNotFound, CodeNotFound)
	errorBody(t, e.get(base+"0"), http.StatusNotFound, CodeNotFound)
	errorBody(t, e.get(base+"abc"), http.StatusBadRequest, CodeBadRequest)
}

// FR-SRV-006's gate is the configuration key *and* the build tag. With a real
// renderer wired in, `pdf.enabled: false` still refuses.
func TestPDFPage_configKeyStillGates(t *testing.T) {
	e := newEnv(t) // pdf.enabled: false
	errorBody(t, e.get("/api/books/"+e.bookPDFID+"/pages/1"),
		http.StatusNotImplemented, CodeUnsupported)

	settings := decodeBody[Settings](t, e.get("/api/settings"), http.StatusOK)
	if settings.Server.PDFEnabled {
		t.Error("settings.server.pdf_enabled = true with pdf.enabled: false")
	}

	on := newEnv(t, withPDF())
	enabled := decodeBody[Settings](t, on.get("/api/settings"), http.StatusOK)
	if !enabled.Server.PDFEnabled {
		t.Error("settings.server.pdf_enabled = false with pdf.enabled: true in a pdf-capable build")
	}
}
