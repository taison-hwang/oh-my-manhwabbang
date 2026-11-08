//go:build !nopdf

package thumbs

import (
	"bytes"
	"crypto/sha256"
	"encoding/base32"
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"shelf/internal/index"
	"shelf/internal/pdfium"
	"shelf/internal/source"
)

// The PDF half of the 권 cover (E-69 §2.2). A PDF page has no pixels until it
// is rasterised, at a width the request picks, so this is the one source where
// the cut can starve the thumbnail: rendered at the thumbnail's width, half a
// spread is half as wide as asked. Everything here needs a live pdfium, which
// `-tags nopdf` compiles out — that build answers every PDF book with
// ErrUnsupported, and internal/source's stub tests pin that.

// spreadPDF is a one-page PDF of w×h points painted like spread(): the left
// half red, the right half blue, a green spine 5 % of the width either side of
// the middle. The frozen dependency set has no PDF writer, so it is written by
// hand, as internal/source's and internal/pdfium's fixtures are.
func spreadPDF(t testing.TB, w, h int) []byte {
	t.Helper()
	rg := func(s swatch) string {
		return fmt.Sprintf("%.4f %.4f %.4f rg", float64(s.c.R)/255, float64(s.c.G)/255, float64(s.c.B)/255)
	}
	spine := w / 20
	content := fmt.Sprintf("%s 0 0 %d %d re f\n%s %d 0 %d %d re f\n%s %d 0 %d %d re f\n",
		rg(red), w/2, h,
		rg(blue), w/2, w-w/2, h,
		rg(green), w/2-spine/2, spine, h)

	var buf bytes.Buffer
	offsets := map[int]int{}
	obj := func(n int, body string) {
		offsets[n] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", n, body)
	}
	buf.WriteString("%PDF-1.4\n")
	obj(1, "<< /Type /Catalog /Pages 2 0 R >>")
	obj(2, "<< /Type /Pages /Kids [ 3 0 R ] /Count 1 >>")
	obj(3, fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %d %d] /Contents 4 0 R /Resources << >> >>", w, h))
	obj(4, fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(content), content))
	xref := buf.Len()
	buf.WriteString("xref\n0 5\n0000000000 65535 f \n")
	for n := 1; n <= 4; n++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[n])
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size 5 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", xref)
	return buf.Bytes()
}

// newPDFRenderer is a live pdfium with one worker, closed with the test.
func newPDFRenderer(t testing.TB) *pdfium.Renderer {
	t.Helper()
	r := pdfium.New(pdfium.Options{Workers: 1, CacheDir: t.TempDir(), Logger: quietLogger()})
	t.Cleanup(func() { _ = r.Close() })
	return r
}

// pdfService is a source factory that renders PDFs through r, clamped to
// `pdf.max_width` = maxWidth, and a thumbnail Service over it and the harness's
// index, with tune applied to its Options.
func (h *harness) pdfService(r *pdfium.Renderer, maxWidth int, tune func(*Options)) (*source.Factory, *Service) {
	h.t.Helper()
	pdfs := source.NewFactory(source.Options{
		Roots: h.roots, Pool: h.pool, PDF: r,
		PDFWidth: 1200, PDFMaxWidth: maxWidth, PDFQuality: 90,
		Logger: quietLogger(),
	})
	opts := Options{
		CacheDir: h.t.TempDir(), Index: h.idx, Sources: pdfs, Roots: h.roots,
		Workers: 1, Logger: quietLogger(),
	}
	if tune != nil {
		tune(&opts)
	}
	svc, err := New(h.t.Context(), opts)
	if err != nil {
		h.t.Fatalf("thumbs.New: %v", err)
	}
	h.t.Cleanup(func() { _ = svc.Close() })
	return pdfs, svc
}

// A landscape PDF page's rtl cover is its right half at the REQUESTED width:
// at 400 px the page must be rendered at 800 for the half to be 400. Rendered
// at the thumbnail's own 400 the half would be 200 — published as is, since
// nothing is upscaled. The pages row still gets the page's own geometry in
// points, not the render's and not the cut's (FR-VWR-004).
func TestGenerate_pdfCover_rendersWideEnoughForTheHalfToFillTheWidth(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	r := newPDFRenderer(t)
	pdfs, svc := h.pdfService(r, pdfium.MaxWidth, nil)
	book, cv := h.indexFile("series/spread.pdf", source.KindPDF, spreadPDF(t, 400, 200), pdfs)

	res, err := svc.Generate(t.Context(), Request{ID: book, PageNo: 1, Width: 400, ContentVersion: cv, Cover: &Cover{Dir: "rtl"}})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	img := decodeThumb(t, res.Path)
	// 400×200 pt rendered 800 px wide is 800×400; its right half is 400×400.
	wantSize(t, "rtl cover of a 400×200 pt PDF spread at 400 px", img, 400, 400)
	wantColours(t, "rtl cover of a PDF spread", img, map[float64]swatch{0.25: blue, 0.5: blue, 0.75: blue})

	if err := svc.Drain(t.Context()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	page, err := h.idx.GetPage(t.Context(), book, 1)
	if err != nil {
		t.Fatalf("GetPage: %v", err)
	}
	if page.Width == nil || page.Height == nil || *page.Width != 400 || *page.Height != 200 {
		t.Fatalf("pages row = %v×%v, want the page's 400×200 pt", deref(page.Width), deref(page.Height))
	}
}

// E-69 §2.2 asks for the cover at the requested width, and a page that is not
// cut in width still goes through the source's snap: pdfium.SnapWidth rounds
// to the NEAREST 100 px, so a render asked for at 240 is 200 px wide, and since
// nothing is upscaled the cover would be 200. Asked for at the next step UP,
// 300, the render is downscaled to 240. Two covers are not cut in width: a
// portrait page's automatic cover, which is the whole page, and a region the
// whole width of the page.
func TestGenerate_pdfCover_notCutInWidth_isStillTheRequestedWidth(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	pdfs, svc := h.pdfService(newPDFRenderer(t), pdfium.MaxWidth, nil)
	portrait, pcv := h.indexFile("series/portrait.pdf", source.KindPDF, spreadPDF(t, 200, 300), pdfs)
	landscape, lcv := h.indexFile("series/landscape.pdf", source.KindPDF, spreadPDF(t, 400, 200), pdfs)

	for _, c := range []struct {
		what     string
		book, cv string
		width    int
		cover    Cover
		w, h     int
		want     map[float64]swatch
	}{
		// 200×300 pt at 240 px wide is 240×360, the whole page: red | blue.
		{"portrait, automatic, 240 px", portrait, pcv, 240, Cover{Dir: "rtl"}, 240, 360, map[float64]swatch{0.25: red, 0.75: blue}},
		// At 120 px the nearest step is 100, the next one up 200: 120×180.
		{"portrait, automatic, 120 px", portrait, pcv, 120, Cover{Dir: "ltr"}, 120, 180, map[float64]swatch{0.25: red, 0.75: blue}},
		// 400×200 pt, all of it, at 240 px is 240×120: red | green | blue.
		{"landscape, region the whole page, 240 px", landscape, lcv, 240, Cover{Region: &Region{0, 0, 1, 1}}, 240, 120,
			map[float64]swatch{0.25: red, 0.5: green, 0.75: blue}},
	} {
		res, err := svc.Generate(t.Context(), Request{ID: c.book, PageNo: 1, Width: c.width, ContentVersion: c.cv, Cover: &c.cover})
		if err != nil {
			t.Fatalf("%s: Generate: %v", c.what, err)
		}
		img := decodeThumb(t, res.Path)
		wantSize(t, c.what, img, c.w, c.h)
		wantColours(t, c.what, img, c.want)
	}
}

// The two ceilings on a PDF cover's render, and what each publishes. Neither
// is a failure: the cover is the cut of the render the ceiling allows, and as
// nothing is upscaled (D-10) it is narrower than asked.
//
//   - `pdf.max_width` (arch §3.2) clamps every render in the source. At 300 the
//     800 px that a 400 px rtl half needs is a 300×150 render; its half is
//     150×150.
//   - The decode ceiling (Options.MaxSourcePixels, checkPixelBudget) refuses a
//     render over it AFTER it was paid for: 800×400 is 320 000 px, over 200 000.
//     renderWidth asks for ⌊√(200 000 × 400/200)⌋ = 632 → 600 instead,
//     600×300 = 180 000 px, and the half is 300×300.
func TestGenerate_pdfCover_underARenderCeiling_isTheCutOfWhatTheCeilingAllows(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	r := newPDFRenderer(t)
	clamped, narrow := h.pdfService(r, 300, nil)
	_, budgeted := h.pdfService(r, pdfium.MaxWidth, func(o *Options) { o.MaxSourcePixels = 200_000 })
	book, cv := h.indexFile("series/spread.pdf", source.KindPDF, spreadPDF(t, 400, 200), clamped)

	for _, c := range []struct {
		what string
		svc  *Service
		side int
	}{
		{"pdf.max_width 300", narrow, 150},
		{"a 200 000 px decode ceiling", budgeted, 300},
	} {
		res, err := c.svc.Generate(t.Context(), Request{ID: book, PageNo: 1, Width: 400, ContentVersion: cv, Cover: &Cover{Dir: "rtl"}})
		if err != nil {
			t.Fatalf("%s: Generate: %v, want the cut of the render the ceiling allows", c.what, err)
		}
		img := decodeThumb(t, res.Path)
		wantSize(t, "rtl cover at 400 px under "+c.what, img, c.side, c.side)
		wantColours(t, "rtl cover under "+c.what, img, map[float64]swatch{0.25: blue, 0.5: blue, 0.75: blue})
	}
}

func deref(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

// E-69 §2.2(b), last paragraph: a page is landscape by its RECORDED intrinsic
// size, the pages.width/height the viewer's spread mode reads, PDFs included;
// the rendered pixels decide only when nothing is recorded. For a PDF the
// recorded size is PageSize in points, rounded — produce's recordDims and the
// dimension pass write the same value.
//
// A 4000×3999 pt page is landscape. Its rtl cover at 400 px is rendered 800 px
// wide, and pdfium rounds the height UP (go-pdfium v1.19.6,
// internal/implementation_webassembly/render.go: math.Ceil): ⌈800 × 3999/4000⌉
// = ⌈799.8⌉ = 800, a SQUARE render. Judged on the render, the cover was the
// whole page squeezed to 400×400, red, green and blue, while the viewer laid
// the page out as a spread. Judged on the recorded size it is the right half
// of the render: columns [400, 800) of 800, so 400×800, all blue.
//
// Both ways the recorded size can exist are checked: not yet recorded (the
// page's PageSize is what produce is about to record) and already in the index.
func TestGenerate_pdfCover_nearlySquareLandscapePage_isJudgedOnItsRecordedSize(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	pdfs, svc := h.pdfService(newPDFRenderer(t), pdfium.MaxWidth, nil)
	page := spreadPDF(t, 4000, 3999)
	cold, ccv := h.indexFile("series/cold.pdf", source.KindPDF, page, pdfs)
	recorded, rcv := h.indexFile("series/recorded.pdf", source.KindPDF, page, pdfs)
	if err := h.idx.UpdateDims(t.Context(), recorded, []index.PageDims{{PageNo: 1, Width: 4000, Height: 3999}}); err != nil {
		t.Fatalf("UpdateDims: %v", err)
	}

	for _, c := range []struct {
		what     string
		book, cv string
	}{
		{"nothing recorded yet", cold, ccv},
		{"4000×3999 already recorded", recorded, rcv},
	} {
		res, err := svc.Generate(t.Context(), Request{ID: c.book, PageNo: 1, Width: 400, ContentVersion: c.cv, Cover: &Cover{Dir: "rtl"}})
		if err != nil {
			t.Fatalf("%s: Generate: %v", c.what, err)
		}
		img := decodeThumb(t, res.Path)
		wantSize(t, "rtl cover of a 4000×3999 pt page, "+c.what, img, 400, 800)
		wantColours(t, "rtl cover of a 4000×3999 pt page, "+c.what, img, map[float64]swatch{0.25: blue, 0.5: blue, 0.75: blue})
	}

	// And the recorded size is the one that said "landscape": what the viewer
	// is given for this page agrees with the cover that was cut.
	if err := svc.Drain(t.Context()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	for _, book := range []string{cold, recorded} {
		row, err := h.idx.GetPage(t.Context(), book, 1)
		if err != nil {
			t.Fatalf("GetPage: %v", err)
		}
		if row.Width == nil || row.Height == nil || *row.Width != 4000 || *row.Height != 3999 {
			t.Fatalf("pages row = %v×%v, want the page's 4000×3999 pt — landscape, w > h", deref(row.Width), deref(row.Height))
		}
	}
}

// The order of E-69 §2.2(b)'s inputs for a PDF: the size in the index outranks
// PageSize, for the render width AND for the cut. In life the two are the
// same rounded PageSize; here the index is made to disagree, which is the only
// way to see which one was read.
//
//   - 400×200 pt recorded 200×400: not landscape, so not widened (400 is on
//     the snap) and not cut — the 400×200 render whole, red, green and blue.
//     Judged on PageSize it would be the right half of an 800 px render,
//     400×400 blue.
//   - 200×400 pt recorded 400×200: landscape, so widened to 800 — an 800×1600
//     render — and its right half is 400×1600, blue. Widened on PageSize (not
//     at all) the half would be 200×800.
func TestGenerate_pdfCover_recordedSizeOutranksPageSize(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	pdfs, svc := h.pdfService(newPDFRenderer(t), pdfium.MaxWidth, nil)
	wide, wcv := h.indexFile("series/wide.pdf", source.KindPDF, spreadPDF(t, 400, 200), pdfs)
	tall, tcv := h.indexFile("series/tall.pdf", source.KindPDF, spreadPDF(t, 200, 400), pdfs)
	for _, rec := range []struct {
		book string
		w, h int
	}{
		{wide, 200, 400},
		{tall, 400, 200},
	} {
		if err := h.idx.UpdateDims(t.Context(), rec.book, []index.PageDims{{PageNo: 1, Width: rec.w, Height: rec.h}}); err != nil {
			t.Fatalf("UpdateDims: %v", err)
		}
	}

	for _, c := range []struct {
		what     string
		book, cv string
		w, h     int
		want     map[float64]swatch
	}{
		{"400×200 pt recorded 200×400", wide, wcv, 400, 200, map[float64]swatch{0.25: red, 0.5: green, 0.75: blue}},
		{"200×400 pt recorded 400×200", tall, tcv, 400, 1600, map[float64]swatch{0.25: blue, 0.5: blue, 0.75: blue}},
	} {
		res, err := svc.Generate(t.Context(), Request{ID: c.book, PageNo: 1, Width: 400, ContentVersion: c.cv, Cover: &Cover{Dir: "rtl"}})
		if err != nil {
			t.Fatalf("%s: Generate: %v", c.what, err)
		}
		img := decodeThumb(t, res.Path)
		wantSize(t, "rtl cover at 400 px, "+c.what, img, c.w, c.h)
		wantColours(t, "rtl cover at 400 px, "+c.what, img, c.want)
	}
}

// ---------------------------------------------------------------------------
// PDF page thumbnails: the requested width, not the source's nearest step
// ---------------------------------------------------------------------------

// A page thumbnail is "요청 폭으로 snap한 섬네일" (FR-THM-002's target width,
// impl-plan §0.4's ladder [120, 240, 400, 640]), and nothing is ever upscaled
// (D-10). A PDF page has no pixel width of its own — it is vector, and any
// width up to the render ceiling can be had — so the only correct size is the
// requested one: the width, and the height the page's own aspect gives it.
//
// The shipped defect: the source snaps a render to the NEAREST 100 px, so a
// thumbnail asked for at 240 was rendered at 200 and published at 200, 120 at
// 100, and 640 at 600 — every strip and grid thumbnail of a PDF book visibly
// soft in a box drawn for the requested width. 400 was never short (it is on
// the 100 px step), and is here so a fix cannot overshoot it.
func TestGenerate_pdfPageThumbnail_isTheRequestedWidth(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	pdfs, svc := h.pdfService(newPDFRenderer(t), pdfium.MaxWidth, nil)
	portrait, pcv := h.indexFile("series/portrait.pdf", source.KindPDF, spreadPDF(t, 200, 300), pdfs)
	landscape, lcv := h.indexFile("series/landscape.pdf", source.KindPDF, spreadPDF(t, 400, 200), pdfs)

	for _, c := range []struct {
		what     string
		book, cv string
		width    int
		w, h     int
	}{
		// 200×300 pt is 2:3, so the height is 1.5 × the width.
		{"portrait at 120", portrait, pcv, 120, 120, 180},
		{"portrait at 240", portrait, pcv, 240, 240, 360},
		{"portrait at 400", portrait, pcv, 400, 400, 600},
		{"portrait at 640", portrait, pcv, 640, 640, 960},
		// 400×200 pt is 2:1: the whole spread, half as tall as wide.
		{"landscape at 240", landscape, lcv, 240, 240, 120},
	} {
		res, err := svc.Generate(t.Context(), Request{ID: c.book, PageNo: 1, Width: c.width, ContentVersion: c.cv})
		if err != nil {
			t.Fatalf("%s: Generate: %v", c.what, err)
		}
		img := decodeThumb(t, res.Path)
		wantSize(t, "PDF page thumbnail, "+c.what, img, c.w, c.h)
		// The whole page, not a cut: the left half red, the right half blue.
		wantColours(t, "PDF page thumbnail, "+c.what, img, map[float64]swatch{0.25: red, 0.75: blue})
	}
}

// The two render ceilings still bound a PDF page thumbnail, exactly as before
// the round-up: neither is a failure, and what is published is the ceiling's
// render, never an upscale of it (D-10).
//
//   - `pdf.max_width` = 200 (arch §3.2): 240 cannot be had, and the thumbnail is
//     the 200 px render. 120 can — the round-up asks for 200, which the cap
//     allows — and is downscaled to 120.
//   - The decode ceiling (checkPixelBudget) on a 1:10 page: 4 000 000 px allow a
//     render of √(4 000 000 / 10) = 632 px, so 600 on the 100 px step. A 640 px
//     thumbnail rounded up to 700 would be 700×7000 = 4.9 Mpx and refused after
//     the render was paid for; the plain request's 600 px render fits, and that
//     is the thumbnail — 600×6000, as before the fix.
func TestGenerate_pdfPageThumbnail_underARenderCeiling_isWhatTheCeilingAllows(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	r := newPDFRenderer(t)
	capped, narrow := h.pdfService(r, 200, nil)
	_, budgeted := h.pdfService(r, pdfium.MaxWidth, func(o *Options) { o.MaxSourcePixels = 4_000_000 })
	portrait, pcv := h.indexFile("series/portrait.pdf", source.KindPDF, spreadPDF(t, 200, 300), capped)
	tall, tcv := h.indexFile("series/tall.pdf", source.KindPDF, spreadPDF(t, 100, 1000), capped)

	for _, c := range []struct {
		what     string
		svc      *Service
		book, cv string
		width    int
		w, h     int
	}{
		{"pdf.max_width 200, asked 240", narrow, portrait, pcv, 240, 200, 300},
		{"pdf.max_width 200, asked 120", narrow, portrait, pcv, 120, 120, 180},
		{"a 4 Mpx decode ceiling on a 1:10 page, asked 640", budgeted, tall, tcv, 640, 600, 6000},
	} {
		res, err := c.svc.Generate(t.Context(), Request{ID: c.book, PageNo: 1, Width: c.width, ContentVersion: c.cv})
		if err != nil {
			t.Fatalf("%s: Generate: %v, want the render the ceiling allows", c.what, err)
		}
		img := decodeThumb(t, res.Path)
		wantSize(t, "PDF page thumbnail, "+c.what, img, c.w, c.h)
	}
}

// archThumbKey is arch §5.6's page-thumbnail key rebuilt from literals —
// "shelf-thumb/1" ‖ 0 ‖ book ‖ 0 ‖ page ‖ 0 ‖ width ‖ 0 ‖ jpeg ‖ 0 ‖ 82 ‖ 0 ‖ cv,
// SHA-256, the first 10 bytes in lowercase unpadded base32 — as
// TestCachePath_isTheArchSpecHash_withTwoLevelFanout does, so the assertion
// does not lean on ids.ThumbKey (whose own vector is pinned in internal/ids).
func archThumbKey(book string, page, width int, cv string) string {
	input := strings.Join([]string{"shelf-thumb/1", book, strconv.Itoa(page), strconv.Itoa(width), "jpeg", "82", cv}, "\x00")
	sum := sha256.Sum256([]byte(input))
	return base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding).EncodeToString(sum[:10])
}

// Rounding the render up must not move a single cache file. The key is the
// REQUESTED width snapped to `thumbnails.widths` (FR-THM-002, arch §5.6) and
// E-69 §2.7 leaves `shelf-thumb/1` exactly as it was; the render width is an
// implementation detail of one source and goes nowhere near it. So a 240 px
// PDF thumbnail — rendered at 300 now, at 200 before — is the file it always
// was, and a request for 200 (which snaps up to 240) is that same file.
//
// The consequence is deliberate: a 200 px file already in a cache from before
// the fix is a hit under the same key and is not re-made. It goes when its
// book's content version changes or the cache is purged (FR-THM-006/008).
func TestGenerate_pdfPageThumbnail_keyAndPathAreTheRequestedWidths(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	pdfs, svc := h.pdfService(newPDFRenderer(t), pdfium.MaxWidth, nil)
	book, cv := h.indexFile("series/portrait.pdf", source.KindPDF, spreadPDF(t, 200, 300), pdfs)

	for _, c := range []struct {
		asked, snapped int
	}{
		{240, 240},
		{200, 240},
		{120, 120},
		{640, 640},
	} {
		res, err := svc.Generate(t.Context(), Request{ID: book, PageNo: 1, Width: c.asked, ContentVersion: cv})
		if err != nil {
			t.Fatalf("asked %d: Generate: %v", c.asked, err)
		}
		want := archThumbKey(book, 1, c.snapped, cv)
		if res.Key != want {
			t.Errorf("asked %d: key = %q, want arch §5.6's key for width %d, %q", c.asked, res.Key, c.snapped, want)
		}
		wantPath := filepath.Join(svc.CacheDir(), "thumbs", want[0:2], want[2:4], want+".jpg")
		if res.Path != wantPath {
			t.Errorf("asked %d: path = %q, want %q (FR-THM-002)", c.asked, res.Path, wantPath)
		}
		// The same request again is a hit on that same file.
		again, err := svc.Get(t.Context(), Request{ID: book, PageNo: 1, Width: c.asked, ContentVersion: cv})
		if err != nil || again.Key != want || again.Path != wantPath {
			t.Errorf("asked %d: Get after Generate = %+v, %v; want a hit on %q", c.asked, again, err, wantPath)
		}
	}
}

// ---------------------------------------------------------------------------
// PDF covers past the absolute render ceiling (pdfium.MaxWidth)
// ---------------------------------------------------------------------------

// A region 0.05 of the page wide (E-69 §2.4's narrowest) at 640 px needs a
// render of ⌈640 / 0.05⌉ = 12 800 px for the cut to fill the width. No render
// is ever that wide: the source clamps to `pdf.max_width`, and to
// pdfium.MaxWidth = 4000 when that is 0 or larger (arch §3.2, SnapWidth). The
// cover is then the cut of the 4000 px render, published as it is (D-10):
//
//	render  4000 × 2000   (400×200 pt is 2:1)
//	cut     round(0.05 × 4000) = 200 wide, the full 2000 tall (h = 1)
//
// Which ceiling wins at the default decode ceiling (40 Mi px): renderWidth
// first trims 12 800 to its pixel ceiling, ⌊√(41 943 040 × 400/200)⌋ = 9158 →
// 9100, and the source then clamps 9100 to 4000. 4000 × 2000 = 8 Mpx is well
// inside the budget, so `pdf.max_width`/pdfium.MaxWidth is the ceiling that
// decides the size; the pixel ceiling only shortened the request.
func TestGenerate_pdfCover_regionPastTheAbsoluteRenderCeiling_isTheCutOfA4000pxRender(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	r := newPDFRenderer(t)
	atMax, svcMax := h.pdfService(r, pdfium.MaxWidth, nil)
	_, svcAbove := h.pdfService(r, 2*pdfium.MaxWidth, nil)
	_, svcZero := h.pdfService(r, 0, nil)
	book, cv := h.indexFile("series/spread.pdf", source.KindPDF, spreadPDF(t, 400, 200), atMax)

	const width = 640
	region := Region{X: 0, Y: 0, W: 0.05, H: 1}
	if err := region.Valid(); err != nil {
		t.Fatalf("the fixture region must be legal under E-69 §2.4: %v", err)
	}
	// The absolute ceiling as a number, not as pdfium.MaxWidth: a test that
	// read the constant would agree with any value it was changed to. It is
	// enforced twice — the source's SnapWidth(w, pdf.max_width) and
	// Doc.RenderJPEG's own SnapWidth(w, 0) — so this holds while either does.
	const render = 4000
	wantW := int(math.Round(region.W * render)) // 200
	wantH := render * 200 / 400                 // 2000: the render's height, all of it

	for _, c := range []struct {
		what string
		svc  *Service
	}{
		{"pdf.max_width = pdfium.MaxWidth", svcMax},
		{"pdf.max_width above pdfium.MaxWidth", svcAbove},
		{"pdf.max_width 0 (the absolute ceiling)", svcZero},
	} {
		res, err := c.svc.Generate(t.Context(), Request{ID: book, PageNo: 1, Width: width, ContentVersion: cv, Cover: &Cover{Region: &region}})
		if err != nil {
			t.Fatalf("%s: Generate: %v, want the cut of the 4000 px render", c.what, err)
		}
		img := decodeThumb(t, res.Path)
		wantSize(t, "0.05-wide region at 640 px under "+c.what, img, wantW, wantH)
		// x ∈ [0, 20) pt of a page whose left half is red and whose spine
		// starts at 190 pt.
		wantColours(t, "0.05-wide region under "+c.what, img, map[float64]swatch{0.25: red, 0.5: red, 0.75: red})
	}
}
