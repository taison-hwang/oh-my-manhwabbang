package thumbs

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"testing"

	"shelf/internal/ids"
	"shelf/internal/index"
	"shelf/internal/source"
	"shelf/internal/testutil"
)

// ---------------------------------------------------------------------------
// E-69 — the 권 cover: a region of one page, or the reading-direction half of a
// landscape page 1. The expected values in this file come from decisions.md
// E-69 §2.2 (the automatic half), §2.4 (the region and its validation) and §2.7
// (the cache key), and from FR-VWR-004's definition of a landscape page, w > h.
// ---------------------------------------------------------------------------

// E-69 §2.4: `0 ≤ x`, `0 ≤ y`, `w ≥ 0.05`, `h ≥ 0.05`, `x + w ≤ 1`,
// `y + h ≤ 1`, and anything else is `400 bad_request`. NaN and the infinities
// are not numbers a frame on a page can have; every comparison with NaN is
// false, so a check written as "reject when x < 0" lets one straight through.
func TestRegionValid_isE69Section24Exactly(t *testing.T) {
	t.Parallel()

	negZero := math.Copysign(0, -1)
	ok := map[string]Region{
		"the whole page":              {0, 0, 1, 1},
		"the smallest side, 0.05":     {0, 0, 0.05, 0.05},
		"x + w = 1 exactly":           {0.5, 0, 0.5, 1},
		"y + h = 1 exactly":           {0, 0.75, 1, 0.25},
		"the far corner, 0.95+0.05":   {0.95, 0.95, 0.05, 0.05},
		"a negative zero is a zero":   {negZero, negZero, 0.5, 0.5},
		"a 2:3 frame inside a spread": {0.5, 0, 1.0 / 3, 1},
	}
	for name, r := range ok {
		if err := r.Valid(); err != nil {
			t.Errorf("%s %+v: Valid() = %v, want nil", name, r, err)
		}
	}

	bad := map[string]Region{
		"w below 0.05":   {0, 0, 0.0499, 0.5},
		"h below 0.05":   {0, 0, 0.5, 0.0499},
		"w zero":         {0, 0, 0, 0.5},
		"x + w over 1":   {0.5000001, 0, 0.5, 1},
		"y + h over 1":   {0, 0.6, 1, 0.5},
		"w over 1":       {0, 0, 1.5, 1},
		"negative x":     {-0.01, 0, 0.5, 0.5},
		"negative y":     {0, -0.01, 0.5, 0.5},
		"negative w":     {0.5, 0, -0.1, 0.5},
		"NaN x":          {math.NaN(), 0, 0.5, 0.5},
		"NaN y":          {0, math.NaN(), 0.5, 0.5},
		"NaN w":          {0, 0, math.NaN(), 0.5},
		"NaN h":          {0, 0, 0.5, math.NaN()},
		"+Inf w":         {0, 0, math.Inf(1), 0.5},
		"-Inf x":         {math.Inf(-1), 0, 0.5, 0.5},
		"+Inf h":         {0, 0, 0.5, math.Inf(1)},
		"-Inf y, +Inf h": {0, math.Inf(-1), 0.5, math.Inf(1)},
	}
	for name, r := range bad {
		err := r.Valid()
		if err == nil {
			t.Errorf("%s %+v: Valid() = nil, want ErrBadRequest", name, r)
			continue
		}
		if !errors.Is(err, ErrBadRequest) {
			t.Errorf("%s %+v: Valid() = %v, which is not ErrBadRequest (the HTTP layer maps that to 400)", name, r, err)
		}
	}
}

// E-69 §2.7: the area field of the key is `auto-ltr`/`auto-rtl` when nothing
// was chosen — the direction always goes in, landscape or not — and the region
// otherwise, each coordinate in Go's shortest round-trip spelling, comma-joined.
// A region makes the direction irrelevant, so it must not reach the key.
func TestCoverArea_spellsTheKeyField(t *testing.T) {
	t.Parallel()

	negZero := math.Copysign(0, -1)
	for _, c := range []struct {
		what  string
		cover Cover
		want  string
	}{
		{"automatic, right-to-left", Cover{Dir: "rtl"}, "auto-rtl"},
		{"automatic, left-to-right", Cover{Dir: "ltr"}, "auto-ltr"},
		{"a region", Cover{Region: &Region{0.25, 0, 0.5, 0.75}}, "0.25,0,0.5,0.75"},
		{"a region ignores rtl", Cover{Region: &Region{0.25, 0, 0.5, 0.75}, Dir: "rtl"}, "0.25,0,0.5,0.75"},
		{"a region ignores ltr", Cover{Region: &Region{0.25, 0, 0.5, 0.75}, Dir: "ltr"}, "0.25,0,0.5,0.75"},
		{"shortest round-trip digits", Cover{Region: &Region{1.0 / 3, 0.1, 2.0 / 3, 0.9}}, "0.3333333333333333,0.1,0.6666666666666666,0.9"},
		{"the whole page", Cover{Region: &Region{0, 0, 1, 1}}, "0,0,1,1"},
		// -0 and 0 are one frame; spelling them apart would cache it twice.
		{"negative zero is spelled 0", Cover{Region: &Region{negZero, negZero, 1, 1}}, "0,0,1,1"},
	} {
		if got := c.cover.area(); got != c.want {
			t.Errorf("%s: area() = %q, want %q", c.what, got, c.want)
		}
	}
}

// A cover request is checked before anything touches the disk: E-69 §2.4's
// region rules, a direction that is one of the two the wire has, a 1-based
// page, and never mixed with the loose-file shape — that one is keyed by the
// SERIES id and has no page, so a "cover of a cover file" has no meaning.
func TestGet_malformedCoverRequests_areBadRequest(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)

	coverOf := func(c Cover) Request {
		return Request{ID: h.zipBook, PageNo: 1, Width: 240, ContentVersion: h.zipCV, Cover: &c}
	}
	bad := map[string]Request{
		"no direction":          coverOf(Cover{}),
		"direction in capitals": coverOf(Cover{Dir: "RTL"}),
		"a third direction":     coverOf(Cover{Dir: "ttb"}),
		"region too narrow":     coverOf(Cover{Region: &Region{0, 0, 0.0499, 1}}),
		"region off the page":   coverOf(Cover{Region: &Region{0.6, 0, 0.5, 1}}),
		"region NaN":            coverOf(Cover{Region: &Region{math.NaN(), 0, 0.5, 1}}),
		"page zero": func() Request {
			r := coverOf(Cover{Dir: "rtl"})
			r.PageNo = 0
			return r
		}(),
		// PageNo 1, so that only the shape — not "page 0" — can refuse it.
		"with the loose-file shape": {
			ID: h.seriesID, PageNo: 1, Width: 240, ContentVersion: h.coverCV,
			RootName: testRoot, RelPath: coverRelPath, Cover: &Cover{Dir: "rtl"},
		},
	}
	for name, req := range bad {
		if _, err := h.svc.Get(t.Context(), req); !errors.Is(err, ErrBadRequest) {
			t.Errorf("%s: Get err = %v, want ErrBadRequest", name, err)
		}
		if _, err := h.svc.Generate(t.Context(), req); !errors.Is(err, ErrBadRequest) {
			t.Errorf("%s: Generate err = %v, want ErrBadRequest", name, err)
		}
		if err := h.svc.Enqueue(req); !errors.Is(err, ErrBadRequest) {
			t.Errorf("%s: Enqueue err = %v, want ErrBadRequest", name, err)
		}
	}
	if st := h.svc.Stats(); st.Queued != 0 || st.Decodes != 0 {
		t.Fatalf("malformed cover requests reached the queue: %+v", st)
	}

	// A region makes the direction irrelevant, so a missing one is not an error.
	if _, err := h.svc.Get(t.Context(), coverOf(Cover{Region: &Region{0, 0, 0.5, 1}})); !errors.Is(err, ErrQueued) {
		t.Fatalf("region with no direction: Get err = %v, want ErrQueued", err)
	}
}

// ---------------------------------------------------------------------------
// Pictures with known colours in known places
// ---------------------------------------------------------------------------

// swatch is a named flat colour. The four are far apart in RGB (the closest
// pair, red and yellow, is ~170 apart), so a JPEG round trip cannot move one
// into another's neighbourhood.
type swatch struct {
	name string
	c    color.NRGBA
}

var (
	red    = swatch{"red", color.NRGBA{220, 40, 40, 255}}
	blue   = swatch{"blue", color.NRGBA{40, 60, 220, 255}}
	green  = swatch{"green", color.NRGBA{40, 200, 60, 255}}
	yellow = swatch{"yellow", color.NRGBA{230, 210, 40, 255}}

	palette = []swatch{red, blue, green, yellow}
)

// colourTolerance is how far a sample may sit from its swatch after JPEG at
// quality 82. Flat areas come back within ~10; the samples are taken well
// away from every edge, where 4:2:0 chroma bleed lives. 48 is under a third of
// the closest pair's distance, so it separates every swatch from every other.
const colourTolerance = 48

// fill is one rectangle of one colour, painted in order.
type fill struct {
	r image.Rectangle
	s swatch
}

// paint fills a w×h page, each fill over the last.
func paint(w, h int, fills ...fill) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for _, f := range fills {
		for y := f.r.Min.Y; y < f.r.Max.Y; y++ {
			for x := f.r.Min.X; x < f.r.Max.X; x++ {
				img.SetNRGBA(x, y, f.s.c)
			}
		}
	}
	return img
}

// paintPNG paints a w×h page, each fill over the last, as a lossless PNG — the
// colours only ever pass through one lossy step, the thumbnail's own encode.
func paintPNG(t testing.TB, w, h int, fills ...fill) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, paint(w, h, fills...)); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	return buf.Bytes()
}

// paintJPEG is paintPNG as a quality-95 JPEG. Go's encoder writes 4:2:0, so it
// decodes to an *image.YCbCr whose chroma planes are half the width — the
// shape 98.7 % of the reference collection decodes to.
func paintJPEG(t testing.TB, w, h int, fills ...fill) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, paint(w, h, fills...), &jpeg.Options{Quality: 95}); err != nil {
		t.Fatalf("jpeg.Encode: %v", err)
	}
	return buf.Bytes()
}

// cols paints full-height vertical bands: cols(h, 0, red, 200, blue, 400) is
// red over x ∈ [0,200) and blue over [200,400).
func cols(h int, spec ...any) []fill {
	var out []fill
	for i := 0; i+2 < len(spec); i += 2 {
		out = append(out, fill{image.Rect(spec[i].(int), 0, spec[i+2].(int), h), spec[i+1].(swatch)})
	}
	return out
}

// addPages writes a ZIP of the given page images into the fixture root and
// indexes it as one more book.
func (h *harness) addPages(rel string, pages ...[]byte) (bookID, cv string) {
	h.t.Helper()
	entries := make([]testutil.Entry, 0, len(pages))
	for i, data := range pages {
		entries = append(entries, testutil.Entry{Name: fmt.Sprintf("%03d.png", i+1), Data: data, Method: testutil.MethodDeflate})
	}
	return h.indexFile(rel, source.KindZIP, testutil.BuildZIP(h.t, testutil.ZIPSpec{Entries: entries}), h.factory)
}

// indexFile writes data at rel under the fixture root and indexes it as a book
// of kind, with the page rows taken from f's own listing — f being a factory
// that can open that kind — exactly as seedIndex does for the fixture's books.
func (h *harness) indexFile(rel string, kind source.Kind, data []byte, f *source.Factory) (bookID, cv string) {
	h.t.Helper()
	ctx := h.t.Context()

	abs := filepath.Join(h.rootPath, filepath.FromSlash(rel))
	writeFile(h.t, abs, data)
	fi, err := os.Stat(abs)
	if err != nil {
		h.t.Fatalf("stat %s: %v", rel, err)
	}
	bookID = ids.BookID(testRoot, rel)
	cv = contentVersion(h.t, string(kind), abs)

	book := source.Book{
		ID: bookID, Kind: kind, RootName: testRoot, RelPath: rel,
		FileSize: fi.Size(), FileMtime: fi.ModTime().Unix(),
	}
	bs, err := f.Open(ctx, book)
	if err != nil {
		h.t.Fatalf("opening %s: %v", rel, err)
	}
	listing, err := bs.List(ctx)
	if err != nil {
		h.t.Fatalf("listing %s: %v", rel, err)
	}
	_ = bs.Close()

	w := h.idx.Writer(index.WriterOptions{})
	defer func() { _ = w.Close() }()
	if err := w.UpsertBook(ctx, index.Book{
		ID: bookID, SeriesID: h.seriesID, RootName: testRoot, RelPath: rel,
		DisplayName: filepath.Base(rel), SortKey: []byte(rel), Ord: 9,
		Kind: string(kind), PageCount: int64(len(listing.Pages)),
		TotalBytes: listing.TotalBytes, FileSize: book.FileSize, FileMtime: book.FileMtime,
		ContentVersion: cv, DimsState: "none", Status: "ok", ScanGen: 1,
	}); err != nil {
		h.t.Fatalf("UpsertBook: %v", err)
	}
	rows := make([]index.Page, 0, len(listing.Pages))
	for _, p := range listing.Pages {
		rows = append(rows, index.Page{
			BookID: bookID, PageNo: p.No, Name: p.Name, EntryPath: p.EntryPath, Ext: p.Ext,
			Size: p.Size, CompSize: p.CompSize, Method: int(p.Method),
			LocalHdrOff: p.LocalHdrOff, CRC32: p.CRC32, Mtime: p.Mtime,
		})
	}
	if err := w.ReplacePages(ctx, bookID, rows); err != nil {
		h.t.Fatalf("ReplacePages: %v", err)
	}
	if err := w.Flush(ctx); err != nil {
		h.t.Fatalf("Flush: %v", err)
	}
	return bookID, cv
}

// decodeThumb decodes a published thumbnail in full.
func decodeThumb(t testing.TB, path string) image.Image {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading thumbnail: %v", err)
	}
	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decoding thumbnail %s: %v", path, err)
	}
	if format != "jpeg" {
		t.Fatalf("thumbnail format = %q, want jpeg (CON-003)", format)
	}
	return img
}

// nearest names the swatch closest to the pixel at (fx, fy) — fractions of the
// image's width and height — and how far from it the pixel is.
func nearest(img image.Image, fx, fy float64) (swatch, float64) {
	b := img.Bounds()
	x := b.Min.X + int(fx*float64(b.Dx()))
	y := b.Min.Y + int(fy*float64(b.Dy()))
	r, g, bl, _ := img.At(x, y).RGBA()
	best, bestD := palette[0], math.Inf(1)
	for _, s := range palette {
		dr := float64(r>>8) - float64(s.c.R)
		dg := float64(g>>8) - float64(s.c.G)
		db := float64(bl>>8) - float64(s.c.B)
		if d := math.Sqrt(dr*dr + dg*dg + db*db); d < bestD {
			best, bestD = s, d
		}
	}
	return best, bestD
}

// wantColours asserts the colour at each horizontal fraction across the middle
// row of the thumbnail.
func wantColours(t testing.TB, what string, img image.Image, want map[float64]swatch) {
	t.Helper()
	for fx, w := range want {
		got, d := nearest(img, fx, 0.5)
		if got != w || d > colourTolerance {
			t.Errorf("%s: pixel at %.0f%% across is %s (%.0f away), want %s", what, fx*100, got.name, d, w.name)
		}
	}
}

// wantSize asserts a thumbnail's pixel size.
func wantSize(t testing.TB, what string, img image.Image, w, h int) {
	t.Helper()
	if b := img.Bounds(); b.Dx() != w || b.Dy() != h {
		t.Errorf("%s: thumbnail is %d×%d, want %d×%d", what, b.Dx(), b.Dy(), w, h)
	}
}

// coverReqFor is a cover request at 640 px. Every test page is narrower than
// that, and nothing is ever upscaled, so the thumbnail's pixels are the cut's
// pixels one for one and its width IS the cut's width.
func coverReqFor(bookID, cv string, page int, c Cover) Request {
	return Request{ID: bookID, PageNo: page, Width: 640, ContentVersion: cv, Cover: &c}
}

// spread is the double-page scan of E-69 §1: the left page red, the right page
// blue, and a green spine down the middle — the thing a centred 2:3 crop would
// put on the cover. The spine straddles the cut at x = 200, so the correct
// half carries 10 px of it at its inner edge and nothing more.
func spread(t testing.TB) []byte {
	return paintPNG(t, 400, 200, cols(200, 0, red, 190, green, 210, blue, 400)...)
}

// ---------------------------------------------------------------------------
// E-69 §2.2 — the automatic cover
// ---------------------------------------------------------------------------

// E-69 §2.2(b): page 1 wider than tall gives the half read first — the RIGHT
// half for rtl, the LEFT for ltr — and nothing of the other half. Sampling a
// quarter, a half and three quarters across tells every wrong answer apart:
// no cut reads red/green/blue, a centred cut red/green/blue too, the wrong
// half the other colour throughout.
func TestGenerate_autoCover_landscapePage_isTheHalfReadFirst(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	book, cv := h.addPages("series/spread.zip", spread(t))

	for _, c := range []struct {
		dir  string
		want swatch
	}{
		{"rtl", blue},
		{"ltr", red},
	} {
		res, err := h.svc.Generate(t.Context(), coverReqFor(book, cv, 1, Cover{Dir: c.dir}))
		if err != nil {
			t.Fatalf("%s: Generate: %v", c.dir, err)
		}
		img := decodeThumb(t, res.Path)
		wantSize(t, c.dir+" cover", img, 200, 200)
		wantColours(t, c.dir+" cover", img, map[float64]swatch{0.25: c.want, 0.5: c.want, 0.75: c.want})
	}
}

// The tests above feed lossless PNG pages and ask for 640 px so that nothing
// is resized. The common real case is neither: 98.7 % of the reference
// collection is JPEG, which decodes to a chroma-subsampled *image.YCbCr whose
// SubImage must line its chroma planes up at an odd offset, and a 240 px cover
// of a 1 600 px page goes through Lanczos. Both must cut the same half.
func TestGenerate_cover_ofAJPEGPageAndThroughTheDownscale_isTheSameCut(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)

	jpg, jcv := h.addPages("series/spread-jpeg.zip", paintJPEG(t, 400, 200, cols(200, 0, red, 190, green, 210, blue, 400)...))
	pngBook, pcv := h.addPages("series/spread-png.zip", spread(t))

	for _, c := range []struct {
		what     string
		book, cv string
		width    int
		cover    Cover
		w, h     int
		want     swatch
	}{
		{"JPEG page, rtl", jpg, jcv, 640, Cover{Dir: "rtl"}, 200, 200, blue},
		{"JPEG page, ltr", jpg, jcv, 640, Cover{Dir: "ltr"}, 200, 200, red},
		// x = 0.2525 × 400 = 101: an odd left edge, inside the red half.
		{"JPEG page, region at an odd x", jpg, jcv, 640, Cover{Region: &Region{0.2525, 0, 0.2, 1}}, 80, 200, red},
		// 200 px halves at 120 px: Lanczos over the SubImage, not the page.
		{"downscaled, rtl", pngBook, pcv, 120, Cover{Dir: "rtl"}, 120, 120, blue},
		{"downscaled, ltr", pngBook, pcv, 120, Cover{Dir: "ltr"}, 120, 120, red},
	} {
		res, err := h.svc.Generate(t.Context(), Request{ID: c.book, PageNo: 1, Width: c.width, ContentVersion: c.cv, Cover: &c.cover})
		if err != nil {
			t.Fatalf("%s: Generate: %v", c.what, err)
		}
		got := decodeThumb(t, res.Path)
		wantSize(t, c.what, got, c.w, c.h)
		wantColours(t, c.what, got, map[float64]swatch{0.25: c.want, 0.5: c.want, 0.75: c.want})
	}
}

// The three hard parts at once, on the common source: a JPEG page (an
// *image.YCbCr with 4:2:0 chroma), an odd width, so the rtl half starts at an
// odd column the chroma planes cannot start at, and a half wider than the
// request, so Lanczos runs over the SubImage. On 1 203 px the halves are 602 px
// wide (E-69 §2.2: the spare column goes to the half read first): ltr [0, 602)
// and rtl [601, 1 203). At 240 px each is 240 × 600·240/602 = 239.2 → 239.
//
// Each half is two colours, and no colour is on both sides, so the four
// samples tell the right half from the wrong one, from the whole page (green,
// red, blue, yellow) and from a cut shifted by a band (red, red, blue, blue):
// green | red ‖ blue | yellow, the bands 301, 300, 301 and 301 px wide.
func TestGenerate_cover_ofAnOddWidthJPEGPageThroughTheDownscale_isTheHalfReadFirst(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	book, cv := h.addPages("series/odd-jpeg.zip",
		paintJPEG(t, 1203, 600, cols(600, 0, green, 301, red, 601, blue, 902, yellow, 1203)...))

	for _, c := range []struct {
		dir         string
		left, right swatch
	}{
		// rtl [601, 1203): blue over its first 301 px, yellow over the rest.
		{"rtl", blue, yellow},
		// ltr [0, 602): green over its first 301 px, red, and column 601 blue.
		{"ltr", green, red},
	} {
		res, err := h.svc.Generate(t.Context(), Request{ID: book, PageNo: 1, Width: 240, ContentVersion: cv, Cover: &Cover{Dir: c.dir}})
		if err != nil {
			t.Fatalf("%s: Generate: %v", c.dir, err)
		}
		img := decodeThumb(t, res.Path)
		wantSize(t, c.dir+" cover of a 1203×600 JPEG at 240 px", img, 240, 239)
		wantColours(t, c.dir+" cover of a 1203×600 JPEG at 240 px", img,
			map[float64]swatch{0.15: c.left, 0.3: c.left, 0.7: c.right, 0.85: c.right})
	}
}

// E-69 §2.2: "폭이 홀수면 첫 쪽 쪽이 1px 더 갖는다". On a 401 px spread the
// half read first is 201 px whichever side it is on: ltr [0, 201), rtl
// [200, 401). Giving the spare column to the other side makes both 200.
func TestGenerate_autoCover_oddWidth_givesTheSpareColumnToTheFirstHalf(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	book, cv := h.addPages("series/odd.zip",
		paintPNG(t, 401, 200, cols(200, 0, red, 200, green, 201, blue, 401)...))

	for _, c := range []struct {
		dir  string
		want swatch
	}{
		{"rtl", blue},
		{"ltr", red},
	} {
		res, err := h.svc.Generate(t.Context(), coverReqFor(book, cv, 1, Cover{Dir: c.dir}))
		if err != nil {
			t.Fatalf("%s: Generate: %v", c.dir, err)
		}
		img := decodeThumb(t, res.Path)
		wantSize(t, c.dir+" cover of a 401 px spread", img, 201, 200)
		wantColours(t, c.dir+" cover of a 401 px spread", img, map[float64]swatch{0.25: c.want, 0.75: c.want})
	}
}

// The exact pixel ranges of the automatic cut, including the spare column and
// an image whose bounds do not start at the origin.
func TestCoverRect_automatic_splitsTheWidthExactly(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		what   string
		bounds image.Rectangle
		dir    string
		want   image.Rectangle
	}{
		{"even spread, rtl", image.Rect(0, 0, 400, 200), "rtl", image.Rect(200, 0, 400, 200)},
		{"even spread, ltr", image.Rect(0, 0, 400, 200), "ltr", image.Rect(0, 0, 200, 200)},
		{"odd spread, rtl: the spare column is the right half's", image.Rect(0, 0, 401, 200), "rtl", image.Rect(200, 0, 401, 200)},
		{"odd spread, ltr: the spare column is the left half's", image.Rect(0, 0, 401, 200), "ltr", image.Rect(0, 0, 201, 200)},
		{"offset bounds, rtl", image.Rect(10, 5, 411, 205), "rtl", image.Rect(210, 5, 411, 205)},
		{"offset bounds, ltr", image.Rect(10, 5, 411, 205), "ltr", image.Rect(10, 5, 211, 205)},
		{"one pixel wider than tall", image.Rect(0, 0, 301, 300), "rtl", image.Rect(150, 0, 301, 300)},
		{"square is not landscape (w > h, not ≥)", image.Rect(0, 0, 300, 300), "rtl", image.Rect(0, 0, 300, 300)},
		{"portrait", image.Rect(0, 0, 200, 400), "ltr", image.Rect(0, 0, 200, 400)},
	} {
		// Nothing recorded: the decoded size is the basis (landscapeBasis's last).
		if got := coverRect(c.bounds, Cover{Dir: c.dir}, c.bounds.Size()); got != c.want {
			t.Errorf("%s: coverRect(%v, %s) = %v, want %v", c.what, c.bounds, c.dir, got, c.want)
		}
	}
}

// E-69 §2.2(b), last paragraph: the basis — the recorded size when there is
// one — says WHETHER to cut; the decoded bounds say WHERE. A square decode of
// a landscape page is halved in its own columns, the spare column of an odd
// width still going to the half read first, and a landscape decode of a page
// recorded portrait or square is used whole.
func TestCoverRect_automatic_basisDecidesWhether_boundsDecideWhere(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		what   string
		bounds image.Rectangle
		basis  image.Point
		dir    string
		want   image.Rectangle
	}{
		{"square render of a 4000×3999 page, rtl", image.Rect(0, 0, 800, 800), image.Pt(4000, 3999), "rtl", image.Rect(400, 0, 800, 800)},
		{"square render of a 4000×3999 page, ltr", image.Rect(0, 0, 800, 800), image.Pt(4000, 3999), "ltr", image.Rect(0, 0, 400, 800)},
		{"odd decode of a landscape page, rtl", image.Rect(0, 0, 401, 401), image.Pt(402, 401), "rtl", image.Rect(200, 0, 401, 401)},
		{"odd decode of a landscape page, ltr", image.Rect(0, 0, 401, 401), image.Pt(402, 401), "ltr", image.Rect(0, 0, 201, 401)},
		{"offset bounds, rtl", image.Rect(10, 5, 410, 405), image.Pt(800, 400), "rtl", image.Rect(210, 5, 410, 405)},
		{"landscape decode, recorded portrait", image.Rect(0, 0, 400, 200), image.Pt(200, 400), "rtl", image.Rect(0, 0, 400, 200)},
		{"landscape decode, recorded square", image.Rect(0, 0, 400, 200), image.Pt(300, 300), "ltr", image.Rect(0, 0, 400, 200)},
	} {
		if got := coverRect(c.bounds, Cover{Dir: c.dir}, c.basis); got != c.want {
			t.Errorf("%s: coverRect(%v, %s, basis %v) = %v, want %v", c.what, c.bounds, c.dir, c.basis, got, c.want)
		}
	}
}

// The order of landscapeBasis is E-69 §2.2(b)'s: the size recorded in the
// index, then the one about to be recorded (a PDF's PageSize), then the
// decode. A size without area — not measured, or a probe that read 0 — is
// skipped, never taken for "not landscape".
func TestLandscapeBasis_recordedThenNaturalThenDecoded(t *testing.T) {
	t.Parallel()

	none := image.Point{}
	recorded, natural, decoded := image.Pt(4000, 3999), image.Pt(3999, 4000), image.Pt(800, 800)
	for _, c := range []struct {
		what  string
		sizes []image.Point
		want  image.Point
	}{
		{"all three: the recorded one", []image.Point{recorded, natural, decoded}, recorded},
		{"nothing recorded: the natural one", []image.Point{none, natural, decoded}, natural},
		{"neither: the decode", []image.Point{none, none, decoded}, decoded},
		{"a zero side is no size", []image.Point{image.Pt(4000, 0), image.Pt(0, 3999), decoded}, decoded},
		{"before the decode, nothing known", []image.Point{none, none}, none},
	} {
		if got := landscapeBasis(c.sizes...); got != c.want {
			t.Errorf("%s: landscapeBasis(%v) = %v, want %v", c.what, c.sizes, got, c.want)
		}
	}
}

// E-69 §2.2(b): "가로로 길지 않으면 쪽 전체다". A portrait page and a SQUARE
// page — w > h is strict — are used whole in either direction.
func TestGenerate_autoCover_portraitAndSquarePages_areUsedWhole(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	portrait, pcv := h.addPages("series/portrait.zip", paintPNG(t, 200, 400, cols(400, 0, red, 100, blue, 200)...))
	square, scv := h.addPages("series/square.zip", paintPNG(t, 300, 300, cols(300, 0, red, 150, blue, 300)...))

	for _, c := range []struct {
		what     string
		book, cv string
		w, h     int
	}{
		{"portrait", portrait, pcv, 200, 400},
		{"square", square, scv, 300, 300},
	} {
		for _, dir := range []string{"rtl", "ltr"} {
			res, err := h.svc.Generate(t.Context(), coverReqFor(c.book, c.cv, 1, Cover{Dir: dir}))
			if err != nil {
				t.Fatalf("%s %s: Generate: %v", c.what, dir, err)
			}
			img := decodeThumb(t, res.Path)
			wantSize(t, c.what+" "+dir, img, c.w, c.h)
			wantColours(t, c.what+" "+dir, img, map[float64]swatch{0.25: red, 0.75: blue})
		}
	}
}

// E-69 §2.2(b), last paragraph: whether the page is landscape is decided on
// its RECORDED intrinsic size — pages.width/height, the value the viewer's
// spread mode is given — and on the decoded pixels only when nothing is
// recorded. WHERE the cut falls is still the decoded image's own columns.
//
// The two fixtures make the recorded size and the decode disagree, which is
// the only way to see which one decided: a square picture recorded landscape
// is cut in half (the right half for rtl, 200 of its 400 columns), and a
// landscape picture recorded portrait is used whole (800×400 at 640 px is
// 640×320). Judged on the decode, each would be the other answer.
func TestGenerate_autoCover_isJudgedOnTheRecordedSize_notTheDecodedOne(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	square, scv := h.addPages("series/square-recorded-wide.zip", paintPNG(t, 400, 400, cols(400, 0, red, 200, blue, 400)...))
	wide, wcv := h.addPages("series/wide-recorded-tall.zip", paintPNG(t, 800, 400, cols(400, 0, red, 400, blue, 800)...))
	for _, rec := range []struct {
		book string
		w, h int
	}{
		{square, 800, 400},
		{wide, 400, 800},
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
		{"400×400 decoded, 800×400 recorded: the right half", square, scv, 200, 400, map[float64]swatch{0.25: blue, 0.5: blue, 0.75: blue}},
		{"800×400 decoded, 400×800 recorded: the whole page", wide, wcv, 640, 320, map[float64]swatch{0.25: red, 0.75: blue}},
	} {
		res, err := h.svc.Generate(t.Context(), coverReqFor(c.book, c.cv, 1, Cover{Dir: "rtl"}))
		if err != nil {
			t.Fatalf("%s: Generate: %v", c.what, err)
		}
		img := decodeThumb(t, res.Path)
		wantSize(t, c.what, img, c.w, c.h)
		wantColours(t, c.what, img, c.want)
	}
}

// ---------------------------------------------------------------------------
// E-69 §2.4 — a chosen region
// ---------------------------------------------------------------------------

// A region is cut from the page as it is, landscape or not, and the direction
// plays no part.
func TestGenerate_regionCover_isThatRegionOfThePage(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	// Four 100 px bands on a portrait page.
	bands, bcv := h.addPages("series/bands.zip",
		paintPNG(t, 400, 600, cols(600, 0, red, 100, green, 200, blue, 300, yellow, 400)...))
	landscape, lcv := h.addPages("series/spread.zip", spread(t))

	for _, c := range []struct {
		what     string
		book, cv string
		cover    Cover
		w, h     int
		want     swatch
	}{
		{"the second band", bands, bcv, Cover{Region: &Region{0.25, 0, 0.25, 1}}, 100, 600, green},
		{"the fourth band, middle half of its height", bands, bcv, Cover{Region: &Region{0.75, 0.25, 0.25, 0.5}}, 100, 300, yellow},
		// The left 40 % of a spread, asked for under rtl: a region overrides
		// the automatic half, so this is red, not the blue right half.
		{"a region on a spread ignores the direction", landscape, lcv, Cover{Region: &Region{0, 0, 0.4, 1}, Dir: "rtl"}, 160, 200, red},
	} {
		res, err := h.svc.Generate(t.Context(), coverReqFor(c.book, c.cv, 1, c.cover))
		if err != nil {
			t.Fatalf("%s: Generate: %v", c.what, err)
		}
		img := decodeThumb(t, res.Path)
		wantSize(t, c.what, img, c.w, c.h)
		wantColours(t, c.what, img, map[float64]swatch{0.25: c.want, 0.5: c.want, 0.75: c.want})
	}
}

// The region-to-pixel rule: each EDGE is rounded (math.Round, halves away from
// zero) and clamped to the image, and a cut that rounds to nothing keeps one
// pixel. Rounding edges rather than origin and size is what keeps two regions
// that share an edge in normalised space sharing it in pixels.
func TestCoverRect_region_roundsEachEdge(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		what   string
		bounds image.Rectangle
		r      Region
		want   image.Rectangle
	}{
		{"40.1 → 40 and 240.6 → 241", image.Rect(0, 0, 401, 200), Region{0.1, 0.1, 0.5, 0.5}, image.Rect(40, 20, 241, 120)},
		{"a half rounds away from zero: 2.5 → 3", image.Rect(0, 0, 5, 5), Region{0.5, 0.5, 0.5, 0.5}, image.Rect(3, 3, 5, 5)},
		{"the whole page", image.Rect(0, 0, 401, 200), Region{0, 0, 1, 1}, image.Rect(0, 0, 401, 200)},
		{"offset bounds", image.Rect(10, 5, 410, 205), Region{0.25, 0, 0.5, 1}, image.Rect(110, 5, 310, 205)},
		{"rounds to nothing mid-page: keeps one pixel", image.Rect(0, 0, 4, 4), Region{0.5, 0.5, 0.05, 0.05}, image.Rect(2, 2, 3, 3)},
		{"rounds to nothing at the far edge: keeps the last pixel", image.Rect(0, 0, 4, 4), Region{0.95, 0.95, 0.05, 0.05}, image.Rect(3, 3, 4, 4)},
	} {
		// A region is cut whatever the page's shape; the basis plays no part.
		if got := coverRect(c.bounds, Cover{Region: &c.r}, image.Point{}); got != c.want {
			t.Errorf("%s: coverRect(%v, %+v) = %v, want %v", c.what, c.bounds, c.r, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// FR-VWR-004 survives the cut; the strip thumbnail is untouched (E-69 §2.7)
// ---------------------------------------------------------------------------

// The cover of a 400×200 spread is 200×200, but the PAGE is 400×200, and that
// is what generation must record (FR-VWR-004, arch §5.8). Recording the cut
// would store a square, and the viewer would lay the spread out as a single
// page: w > h decides spread mode, and 200 > 200 is false.
func TestGenerate_cover_recordsTheWholePageSize(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	book, cv := h.addPages("series/spread.zip", spread(t))

	res, err := h.svc.Generate(t.Context(), coverReqFor(book, cv, 1, Cover{Dir: "rtl"}))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if res.SourceWidth != 400 || res.SourceHeight != 200 {
		t.Errorf("Result.Source = %d×%d, want the page's 400×200, not the cut's", res.SourceWidth, res.SourceHeight)
	}
	h.drain()

	page, err := h.idx.GetPage(t.Context(), book, 1)
	if err != nil {
		t.Fatalf("GetPage: %v", err)
	}
	if page.Width == nil || page.Height == nil {
		t.Fatal("generating the cover recorded no page size at all")
	}
	if *page.Width != 400 || *page.Height != 200 {
		t.Fatalf("pages row = %d×%d, want 400×200 — FR-VWR-004 would see the cut, not the page", *page.Width, *page.Height)
	}
}

// E-69 §2.7: page 1's strip thumbnail keeps its key and stays the whole page;
// the cover cut from the same page at the same width is another file.
func TestGenerate_coverAndStripThumbnailOfPageOne_areTwoFiles(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	book, cv := h.addPages("series/spread.zip", spread(t))

	cover, err := h.svc.Generate(t.Context(), coverReqFor(book, cv, 1, Cover{Dir: "rtl"}))
	if err != nil {
		t.Fatalf("Generate cover: %v", err)
	}
	strip, err := h.svc.Generate(t.Context(), Request{ID: book, PageNo: 1, Width: 640, ContentVersion: cv})
	if err != nil {
		t.Fatalf("Generate strip thumbnail: %v", err)
	}

	if want := ids.ThumbKey(book, 1, 640, "jpeg", 82, cv); strip.Key != want {
		t.Errorf("strip thumbnail key = %q, want ThumbKey's %q (E-69 §2.7: unchanged)", strip.Key, want)
	}
	if want := ids.CoverKey(book, 1, "auto-rtl", 640, "jpeg", 82, cv); cover.Key != want {
		t.Errorf("cover key = %q, want CoverKey's %q", cover.Key, want)
	}
	if cover.Key == strip.Key || cover.Path == strip.Path {
		t.Fatalf("cover and strip thumbnail share key %q / path %q", cover.Key, cover.Path)
	}
	if rel, err := filepath.Rel(filepath.Join(h.cacheDir, string(KindThumbs)), cover.Path); err != nil || rel != filepath.Join(cover.Key[0:2], cover.Key[2:4], cover.Key+".jpg") {
		t.Errorf("cover path = %s, want it under thumbs/<k0:2>/<k2:4>/ (one purge kind, arch §7.9)", cover.Path)
	}

	stripImg := decodeThumb(t, strip.Path)
	wantSize(t, "strip thumbnail", stripImg, 400, 200)
	wantColours(t, "strip thumbnail", stripImg, map[float64]swatch{0.25: red, 0.5: green, 0.75: blue})
	coverImg := decodeThumb(t, cover.Path)
	wantSize(t, "cover", coverImg, 200, 200)
	wantColours(t, "cover", coverImg, map[float64]swatch{0.25: blue, 0.75: blue})
}

// `cover_cv` (E-69 §2.6, §2.7) is ids.CoverVersion over the service's own
// format and quality, and is one value for every width: the thumbnail made at
// each width is keyed by CoverKey at that width, under the one version.
func TestCoverVersion_isTheServicesEncoderAndIgnoresTheWidth(t *testing.T) {
	t.Parallel()
	h := newHarness(t, func(o *Options) { o.Quality = 90 })
	book, cv := h.addPages("series/spread.zip", spread(t))

	auto := Cover{Dir: "ltr"}
	region := Cover{Region: &Region{0.25, 0, 0.5, 0.75}, Dir: "rtl"}
	if got, want := h.svc.CoverVersion(book, 1, auto, cv), ids.CoverVersion(book, 1, "auto-ltr", "jpeg", 90, cv); got != want {
		t.Errorf("CoverVersion(auto ltr) = %q, want %q (quality 90, not the default 82)", got, want)
	}
	if got, want := h.svc.CoverVersion(book, 3, region, cv), ids.CoverVersion(book, 3, "0.25,0,0.5,0.75", "jpeg", 90, cv); got != want {
		t.Errorf("CoverVersion(region) = %q, want %q", got, want)
	}

	for _, w := range []int{120, 240, 400, 640} {
		res, err := h.svc.Generate(t.Context(), Request{ID: book, PageNo: 1, Width: w, ContentVersion: cv, Cover: &auto})
		if err != nil {
			t.Fatalf("Generate at %d px: %v", w, err)
		}
		if want := ids.CoverKey(book, 1, "auto-ltr", w, "jpeg", 90, cv); res.Key != want {
			t.Errorf("cover at %d px has key %q, want CoverKey's %q", w, res.Key, want)
		}
	}
}

// Generate — the synchronous path — answers a cover of a page the book does not
// have with ErrNotFound, exactly as it does a page thumbnail past the end
// (TestGet_unknownBookOrPage_isNotFound). Get does not: it queues the request
// like any cold miss (TestGet_coverOfAMissingPage_isQueuedLikeAPageThumbnail).
// So neither the HTTP layer's 404 nor the caller's fallback to page 1 (E-69
// §2.2(a)) can rest on this error; both check the page against the index first.
func TestGenerate_coverOfAMissingPageOrBook_isNotFound(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	book, cv := h.addPages("series/spread.zip", spread(t))

	for name, req := range map[string]Request{
		"page past the end, automatic": coverReqFor(book, cv, 2, Cover{Dir: "rtl"}),
		"page past the end, region":    coverReqFor(book, cv, 99, Cover{Region: &Region{0, 0, 1, 1}}),
		"unknown book":                 coverReqFor("aaaaaaaaaaaaaaaa", "cv", 1, Cover{Dir: "ltr"}),
	} {
		if _, err := h.svc.Generate(t.Context(), req); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: Generate err = %v, want ErrNotFound", name, err)
		}
	}
}

// Get never answers "no such page" itself. A cold miss is queued before
// anything is looked up — that is what keeps Get from blocking — and the
// worker's lookup then fails with ErrNotFound, which singleflight does not
// memoise: the negative memo holds undecodable verdicts only. So a Get of a
// page past the end is ErrQueued, and after the worker has run, ErrQueued
// again. The HTTP layer answers 404 before calling Get, from book.PageCount
// (internal/httpapi/thumbs.go).
//
// A cover is a thumbnail of one page, so the expected sequence is the page
// thumbnail's: it is observed side by side, and a cover must not differ from
// it. The concrete ErrQueued, ErrQueued is asserted too, so that a change to
// both paths is a decision and not a drift.
func TestGet_coverOfAMissingPage_isQueuedLikeAPageThumbnail(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	book, cv := h.addPages("series/spread.zip", spread(t))

	// twice is Get, the worker run to completion, and Get again.
	twice := func(req Request) [2]error {
		var out [2]error
		for i := range out {
			_, out[i] = h.svc.Get(t.Context(), req)
			h.drain()
		}
		return out
	}
	same := func(a, b error) bool {
		for _, s := range []error{ErrQueued, ErrNotFound, ErrUndecodable, ErrBadRequest} {
			if errors.Is(a, s) != errors.Is(b, s) {
				return false
			}
		}
		return (a == nil) == (b == nil)
	}

	for _, c := range []struct {
		what  string
		strip Request
		cover Request
	}{
		{
			"page past the end, automatic",
			Request{ID: book, PageNo: 2, Width: 240, ContentVersion: cv},
			Request{ID: book, PageNo: 2, Width: 240, ContentVersion: cv, Cover: &Cover{Dir: "rtl"}},
		},
		{
			"page past the end, region",
			Request{ID: book, PageNo: 99, Width: 400, ContentVersion: cv},
			Request{ID: book, PageNo: 99, Width: 400, ContentVersion: cv, Cover: &Cover{Region: &Region{0, 0, 1, 1}}},
		},
		{
			"unknown book",
			Request{ID: "aaaaaaaaaaaaaaaa", PageNo: 1, Width: 240, ContentVersion: "cv"},
			Request{ID: "aaaaaaaaaaaaaaaa", PageNo: 1, Width: 240, ContentVersion: "cv", Cover: &Cover{Dir: "ltr"}},
		},
	} {
		strip, cover := twice(c.strip), twice(c.cover)
		for i := range strip {
			if !same(cover[i], strip[i]) {
				t.Errorf("%s: Get #%d of the cover = %v, of the page thumbnail = %v; want the same", c.what, i+1, cover[i], strip[i])
			}
			if !errors.Is(strip[i], ErrQueued) || !errors.Is(cover[i], ErrQueued) {
				t.Errorf("%s: Get #%d = %v (page thumbnail), %v (cover); want ErrQueued for both", c.what, i+1, strip[i], cover[i])
			}
		}
	}
}

// ---------------------------------------------------------------------------
// PDF: render wide enough that the cut still fills the requested width
// ---------------------------------------------------------------------------

// A PDF page is rasterised at a width the request chooses, so a cover cut from
// it would come out half (or a fifth) as wide as asked, and a page thumbnail
// rendered at the source's nearest step short of it. E-69 §2.2 wants the cover
// at the requested width, and FR-THM-002 / impl-plan §0.4 the thumbnail at the
// width it was snapped to, which fixes every expected value below:
//
//   - A cover's render is widened by the cut's factor — ×2 for an automatic
//     cover unless the basis (landscapeBasis: recorded, else PageSize) says
//     the page is NOT landscape, ⌈width / region.W⌉ for a region. A page of
//     unknown shape may decode landscape, and its half would then be W/2; if
//     it decodes portrait, the downscale brings it back to W. A page
//     thumbnail (no Cover) is not widened.
//   - It is ALWAYS rounded up to the source's 100 px snap, widened or not: the
//     source rounds to the NEAREST step, so a 240 px picture rendered at "240"
//     is a 200 px render, and nothing is upscaled — a 200 px picture.
//   - It stays inside the decode ceiling (checkPixelBudget) when the basis is
//     known, but never below the width a plain thumbnail would use. When the
//     shape is unknown no ceiling can be computed in advance.
func TestRenderWidth_roundsUpWidensByTheCutAndRespectsThePixelCeiling(t *testing.T) {
	t.Parallel()

	const ceiling = 40 << 20 // defaultMaxSourcePixels
	landscape, portrait, square := image.Pt(800, 400), image.Pt(400, 800), image.Pt(500, 500)
	for _, c := range []struct {
		what    string
		width   int
		cover   *Cover
		basis   image.Point
		ceiling int64
		want    int
	}{
		{"landscape, automatic: twice, 400 → 800", 400, &Cover{Dir: "rtl"}, landscape, ceiling, 800},
		{"landscape, automatic: 240 → 480, up to 500", 240, &Cover{Dir: "ltr"}, landscape, ceiling, 500},
		{"landscape, automatic: 120 → 240, up to 300 (nearest would be 200)", 120, &Cover{Dir: "rtl"}, landscape, ceiling, 300},
		{"landscape, automatic: 640 → 1280, up to 1300", 640, &Cover{Dir: "rtl"}, landscape, ceiling, 1300},
		{"portrait, automatic: not widened, 400 is on the snap already", 400, &Cover{Dir: "rtl"}, portrait, ceiling, 400},
		{"portrait, automatic: not widened but rounded UP, 240 → 300 (nearest would be 200)", 240, &Cover{Dir: "rtl"}, portrait, ceiling, 300},
		{"portrait, automatic: 120 → 200 (nearest would be 100)", 120, &Cover{Dir: "ltr"}, portrait, ceiling, 200},
		{"square, automatic: not widened (w > h is strict)", 400, &Cover{Dir: "rtl"}, square, ceiling, 400},
		{"square, automatic: rounded UP, 640 → 700", 640, &Cover{Dir: "rtl"}, square, ceiling, 700},
		{"shape unknown, automatic: widened as if landscape, 400 → 800", 400, &Cover{Dir: "rtl"}, image.Point{}, ceiling, 800},
		{"shape unknown, automatic: 240 → 480, up to 500", 240, &Cover{Dir: "ltr"}, image.Point{}, ceiling, 500},
		// No shape, no √(maxPixels × w/h): checkPixelBudget refuses after the render.
		{"shape unknown: no ceiling before the render", 640, &Cover{Dir: "rtl"}, image.Point{}, 1 << 20, 1300},
		{"region a quarter wide: 240 / 0.25 = 960, up to 1000", 240, &Cover{Region: &Region{0.5, 0, 0.25, 1}}, portrait, ceiling, 1000},
		{"region a third wide: ⌈400 / (1/3)⌉ = 1200", 400, &Cover{Region: &Region{0, 0, 1.0 / 3, 1}}, landscape, ceiling, 1200},
		{"region the whole width: not widened but rounded UP, 240 → 300", 240, &Cover{Region: &Region{0, 0, 1, 1}}, landscape, ceiling, 300},
		{"region the whole width at 400: on the snap already", 400, &Cover{Region: &Region{0, 0, 1, 1}}, portrait, ceiling, 400},
		{"region, shape unknown: widened all the same", 240, &Cover{Region: &Region{0, 0, 0.5, 1}}, image.Point{}, ceiling, 500},
		// 1 Mpx ceiling on a square page: √(1 048 576) = 1024 → 1000, not 2400.
		{"capped by the pixel ceiling, down to the snap", 240, &Cover{Region: &Region{0, 0, 0.1, 1}}, square, 1 << 20, 1000},
		// 40 Mpx on a 1:4 page: √(41 943 040 / 4) = 3238 → 3200 (3200 × 12800 fits).
		{"a tall page under the real ceiling", 640, &Cover{Region: &Region{0, 0, 0.05, 1}}, image.Pt(1000, 4000), ceiling, 3200},
		// A ceiling below the plain width: the page would be refused at that
		// width anyway, so the cover asks for no less than a thumbnail would.
		{"never below the requested width", 400, &Cover{Region: &Region{0, 0, 0.1, 1}}, image.Pt(100, 10000), 1 << 20, 400},

		// A page thumbnail: never widened, always rounded UP to the snap.
		{"page thumbnail, 120 → 200 (nearest would be 100)", 120, nil, portrait, ceiling, 200},
		{"page thumbnail, 240 → 300 (nearest would be 200)", 240, nil, portrait, ceiling, 300},
		{"page thumbnail, 400: on the snap already", 400, nil, portrait, ceiling, 400},
		{"page thumbnail, 640 → 700 (nearest would be 600)", 640, nil, portrait, ceiling, 700},
		{"page thumbnail of a landscape page: not widened, 240 → 300", 240, nil, landscape, ceiling, 300},
		{"page thumbnail, shape unknown: not widened, 240 → 300", 240, nil, image.Point{}, ceiling, 300},
		// 4 Mpx on a 1:10 page: √(4 000 000 / 10) = 632 → 600 < 700, so the
		// plain 640 — which the source snaps to 600, as before the round-up.
		{"page thumbnail over the pixel ceiling when rounded up: the plain width", 640, nil, image.Pt(100, 1000), 4_000_000, 640},
	} {
		if got := renderWidth(c.width, c.cover, c.basis, c.ceiling); got != c.want {
			t.Errorf("%s: renderWidth(%d, %+v, %v, %d) = %d, want %d", c.what, c.width, c.cover, c.basis, c.ceiling, got, c.want)
		}
	}
}

func TestGenerate_regionCoverUsesTheExifOrientedPageWithoutChangingRecordedDimensions(t *testing.T) {
	h := newHarness(t, nil)
	raw := paintJPEG(t, 400, 600, fill{image.Rect(0, 0, 400, 300), red}, fill{image.Rect(0, 300, 400, 600), blue})
	exif := []byte{0xff, 0xe1, 0, 34, 'E', 'x', 'i', 'f', 0, 0, 'I', 'I', 42, 0, 8, 0, 0, 0, 1, 0, 0x12, 1, 3, 0, 1, 0, 0, 0, 6, 0, 0, 0, 0, 0, 0, 0}
	data := append(append(append([]byte{}, raw[:2]...), exif...), raw[2:]...)
	book, cv := h.addPages("series/rotated.zip", data)
	result, err := h.svc.Generate(t.Context(), coverReqFor(book, cv, 1, Cover{Region: &Region{0, 0, 4.0 / 9, 1}}))
	if err != nil {
		t.Fatal(err)
	}
	img := decodeThumb(t, result.Path)
	wantSize(t, "oriented region", img, 267, 400)
	wantColours(t, "oriented region", img, map[float64]swatch{0.25: blue, 0.5: blue, 0.75: blue})
	h.drain()
	page, err := h.idx.GetPage(t.Context(), book, 1)
	if err != nil || page.Width == nil || page.Height == nil || *page.Width != 400 || *page.Height != 600 {
		t.Fatalf("intrinsic page dimensions changed: %+v (%v)", page, err)
	}
}
