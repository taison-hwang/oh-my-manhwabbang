package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"shelf/internal/index"
	"shelf/internal/userdata"
)

// Ruling E-69 (amendment A-19) — the 권 표지 the user can choose, the default
// that takes the first half of a two-page scan in the effective reading
// direction, and the series reading direction that moved to the server so the
// server can know that direction.
//
// Every expectation here is taken from E-69 §2 and arch §7, never from what an
// implementation returns. The new fields are read through the local wire types
// below rather than through the package DTOs, so the file compiles before the
// DTOs grow them; each reader fails on an ABSENT key, because `cover: null` is
// the contract's "default cover" and a missing key is not.

// wireCover is E-69 §2.6's `{page, x, y, w, h}`.
type wireCover struct {
	Page int     `json:"page"`
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	W    float64 `json:"w"`
	H    float64 `json:"h"`
}

// wireBook is the part of a BookSummary these tests read.
type wireBook struct {
	ID      string
	CV      string
	CoverCV string
	Cover   *wireCover
}

// wireSeries is the part of a SeriesDetail these tests read.
type wireSeries struct {
	CoverCV          *string
	ReadingDirection *string
	Books            []wireBook
}

func rawObject(t *testing.T, raw []byte, what string) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("%s is not a JSON object: %v; body: %s", what, err, raw)
	}
	return m
}

func requireKeys(t *testing.T, m map[string]json.RawMessage, what string, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if _, ok := m[k]; !ok {
			t.Fatalf("%s has no %q key (E-69 §2.6)", what, k)
		}
	}
}

func unmarshalInto(t *testing.T, raw json.RawMessage, dst any, what string) {
	t.Helper()
	if err := json.Unmarshal(raw, dst); err != nil {
		t.Fatalf("%s = %s: %v", what, raw, err)
	}
}

// decodeCover reads a `cover` value: null, or exactly the five fields.
func decodeCover(t *testing.T, raw json.RawMessage, what string) *wireCover {
	t.Helper()
	if string(raw) == "null" {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var c wireCover
	if err := dec.Decode(&c); err != nil {
		t.Fatalf("%s = %s is not {page, x, y, w, h}: %v", what, raw, err)
	}
	m := rawObject(t, raw, what)
	requireKeys(t, m, what, "page", "x", "y", "w", "h")
	return &c
}

func decodeWireBook(t *testing.T, raw json.RawMessage) wireBook {
	t.Helper()
	m := rawObject(t, raw, "BookSummary")
	requireKeys(t, m, "BookSummary", "id", "cv", "cover_cv", "cover")
	var b wireBook
	unmarshalInto(t, m["id"], &b.ID, "id")
	unmarshalInto(t, m["cv"], &b.CV, "cv")
	unmarshalInto(t, m["cover_cv"], &b.CoverCV, "BookSummary.cover_cv (string, never null)")
	if b.CoverCV == "" {
		t.Fatalf("BookSummary %s has an empty cover_cv", b.ID)
	}
	b.Cover = decodeCover(t, m["cover"], "BookSummary.cover")
	return b
}

// wireBookOf is `GET /api/books/{bid}` (BookDetail extends BookSummary).
func (e *env) wireBookOf(bid string) wireBook {
	e.t.Helper()
	w := e.get("/api/books/" + bid)
	if w.Code != http.StatusOK {
		e.t.Fatalf("GET /api/books/%s = %d: %s", bid, w.Code, w.Body.String())
	}
	return decodeWireBook(e.t, w.Body.Bytes())
}

// wireSeriesOf is `GET /api/series/{sid}`.
func (e *env) wireSeriesOf(sid string) wireSeries {
	e.t.Helper()
	w := e.get("/api/series/" + sid)
	if w.Code != http.StatusOK {
		e.t.Fatalf("GET /api/series/%s = %d: %s", sid, w.Code, w.Body.String())
	}
	m := rawObject(e.t, w.Body.Bytes(), "SeriesDetail")
	requireKeys(e.t, m, "SeriesDetail", "cover_cv", "reading_direction", "books")
	var s wireSeries
	unmarshalInto(e.t, m["cover_cv"], &s.CoverCV, "SeriesDetail.cover_cv")
	unmarshalInto(e.t, m["reading_direction"], &s.ReadingDirection, "SeriesDetail.reading_direction")
	var books []json.RawMessage
	unmarshalInto(e.t, m["books"], &books, "SeriesDetail.books")
	for _, b := range books {
		s.Books = append(s.Books, decodeWireBook(e.t, b))
	}
	return s
}

// seriesListCoverCV is a series' `cover_cv` as `GET /api/series` reports it.
func (e *env) seriesListCoverCV(sid string) *string {
	e.t.Helper()
	var list struct {
		Items []struct {
			ID      string  `json:"id"`
			CoverCV *string `json:"cover_cv"`
		} `json:"items"`
	}
	w := e.get("/api/series?limit=200")
	if w.Code != http.StatusOK {
		e.t.Fatalf("GET /api/series = %d: %s", w.Code, w.Body.String())
	}
	unmarshalInto(e.t, w.Body.Bytes(), &list, "SeriesListResponse")
	for _, it := range list.Items {
		if it.ID == sid {
			return it.CoverCV
		}
	}
	e.t.Fatalf("series %s is not in GET /api/series", sid)
	return nil
}

func (e *env) putCover(bid, body string) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.jsonBody(http.MethodPut, "/api/books/"+bid+"/cover", body)
}

// putCoverOK is a PUT that must succeed; it returns the response's
// `{cover_cv, cover}`, which must have exactly those two keys.
func (e *env) putCoverOK(bid, body string) (string, *wireCover) {
	e.t.Helper()
	w := e.putCover(bid, body)
	if w.Code != http.StatusOK {
		e.t.Fatalf("PUT /api/books/%s/cover %s = %d, want 200: %s", bid, body, w.Code, w.Body.String())
	}
	m := rawObject(e.t, w.Body.Bytes(), "PUT cover response")
	requireKeys(e.t, m, "PUT cover response", "cover_cv", "cover")
	if len(m) != 2 {
		e.t.Errorf("PUT cover response has keys %v, want exactly cover_cv and cover", keysOf(m))
	}
	var cv string
	unmarshalInto(e.t, m["cover_cv"], &cv, "PUT cover response cover_cv")
	if cv == "" {
		e.t.Fatal("PUT cover response cover_cv is empty")
	}
	return cv, decodeCover(e.t, m["cover"], "PUT cover response cover")
}

func (e *env) putSeriesPrefs(sid, body string) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.jsonBody(http.MethodPut, "/api/series/"+sid+"/prefs", body)
}

// putSeriesDirOK sets a series' reading direction ("" for null) and checks the
// response is exactly `{reading_direction}` echoing it.
func (e *env) putSeriesDirOK(sid, dir string) {
	e.t.Helper()
	val := "null"
	if dir != "" {
		val = `"` + dir + `"`
	}
	w := e.putSeriesPrefs(sid, `{"reading_direction":`+val+`}`)
	if w.Code != http.StatusOK {
		e.t.Fatalf("PUT /api/series/%s/prefs %s = %d, want 200: %s", sid, val, w.Code, w.Body.String())
	}
	m := rawObject(e.t, w.Body.Bytes(), "PUT series prefs response")
	requireKeys(e.t, m, "PUT series prefs response", "reading_direction")
	if len(m) != 1 {
		e.t.Errorf("PUT series prefs response has keys %v, want exactly reading_direction", keysOf(m))
	}
	if got := string(m["reading_direction"]); got != val {
		e.t.Errorf("PUT series prefs response reading_direction = %s, want %s", got, val)
	}
}

func (e *env) putGlobalDir(dir string) {
	e.t.Helper()
	if w := e.jsonBody(http.MethodPut, "/api/settings", `{"reading_direction":"`+dir+`"}`); w.Code != http.StatusOK {
		e.t.Fatalf("PUT settings reading_direction=%s = %d: %s", dir, w.Code, w.Body.String())
	}
}

func keysOf(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// fetchImage GETs an image endpoint, follows one 202 (arch §7.5: `Retry-After:
// 1`, no body) by draining the generator, and decodes the 200 JPEG.
func (e *env) fetchImage(target string) image.Image {
	e.t.Helper()
	w := e.get(target)
	if w.Code == http.StatusAccepted {
		if got := w.Header().Get("Retry-After"); got != "1" {
			e.t.Errorf("GET %s: 202 with Retry-After %q, want 1", target, got)
		}
		if err := e.thumbs.Drain(e.t.Context()); err != nil {
			e.t.Fatalf("draining the generator: %v", err)
		}
		w = e.get(target)
	}
	if w.Code != http.StatusOK {
		e.t.Fatalf("GET %s = %d, want 200: %s", target, w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "image/jpeg" {
		e.t.Errorf("GET %s Content-Type = %q, want image/jpeg", target, ct)
	}
	img, err := jpeg.Decode(bytes.NewReader(w.Body.Bytes()))
	if err != nil {
		e.t.Fatalf("GET %s is not a JPEG: %v", target, err)
	}
	return img
}

func requireSize(t *testing.T, img image.Image, w, h int, what string) {
	t.Helper()
	if b := img.Bounds(); b.Dx() != w || b.Dy() != h {
		t.Errorf("%s is %dx%d, want %dx%d", what, b.Dx(), b.Dy(), w, h)
	}
}

// --- the landscape fixture (E-69 §2.2(b)) -----------------------------------

// The fixture pages are all portrait, so the half rule needs a page of its
// own: 25×10 (w > h, FR-VWR-004), the left twelve columns red, the middle one
// green and the right twelve blue. 25 is odd on purpose — §2.2 gives the extra
// pixel to the first-page side, so the ltr half is columns 0..12 and the rtl
// half columns 12..24, both 13 wide, and their centres are red and blue.
const (
	landscapeW = 25
	landscapeH = 10
	halfW      = 13
)

func landscapeJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, landscapeW, landscapeH))
	for y := range landscapeH {
		for x := range landscapeW {
			c := color.RGBA{0, 0, 255, 255}
			switch {
			case x < 12:
				c = color.RGBA{255, 0, 0, 255}
			case x == 12:
				c = color.RGBA{0, 255, 0, 255}
			}
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 100}); err != nil {
		t.Fatalf("encoding the landscape page: %v", err)
	}
	return buf.Bytes()
}

// makeDirBookLandscape replaces page 1 of the directory book with the
// landscape page and re-indexes it the way a scan would.
func makeDirBookLandscape(t *testing.T, e *env) {
	t.Helper()
	data := landscapeJPEG(t)
	rewriteFile(t, filepath.Join(e.media, filepath.FromSlash(bookDirPath), "01.jpg"), data, fixedMtime)
	reindexBookPages(t, e, e.bookDirID, "", "0f1e2d3c4b5a6900", []index.Page{{
		PageNo: 1, Name: "01.jpg", EntryPath: "01.jpg", Ext: ".jpg",
		Size: int64(len(data)), Mtime: fixedMtime.Unix(),
	}, {
		PageNo: 2, Name: "02.jpg", EntryPath: "02.jpg", Ext: ".jpg",
		Size: int64(len(e.dirPayloads[1])), Mtime: fixedMtime.Unix(),
	}})
}

// hue names the dominant colour at the centre of an image.
func hue(img image.Image) string {
	b := img.Bounds()
	r, g, bl, _ := img.At(b.Min.X+b.Dx()/2, b.Min.Y+b.Dy()/2).RGBA()
	r, g, bl = r>>8, g>>8, bl>>8
	switch {
	case r > 160 && g < 100 && bl < 100:
		return "red"
	case bl > 160 && r < 100 && g < 100:
		return "blue"
	}
	return "other"
}

// --- GET /api/books/{bid}/cover ---------------------------------------------

// E-69 §2.6: the book cover's responses mean what §7.5's series cover's do,
// and `v` is the book's `cover_cv` — not its `cv`.
func TestBookCover_getFollowsTheSeriesCoverSemantics(t *testing.T) {
	e := newEnv(t)
	book := e.wireBookOf(e.bookZipID)
	base := "/api/books/" + e.bookZipID + "/cover"

	first := e.get(base + "?w=120&v=" + book.CoverCV)
	if first.Code != http.StatusAccepted {
		t.Fatalf("first request = %d, want 202 (queued): %s", first.Code, first.Body.String())
	}
	if got := first.Header().Get("Retry-After"); got != "1" {
		t.Errorf("Retry-After = %q, want 1", got)
	}
	if first.Body.Len() != 0 {
		t.Errorf("the 202 carried a %d-byte body", first.Body.Len())
	}
	if err := e.thumbs.Drain(t.Context()); err != nil {
		t.Fatalf("draining: %v", err)
	}

	ready := e.get(base + "?w=120&v=" + book.CoverCV)
	if ready.Code != http.StatusOK {
		t.Fatalf("after generation = %d, want 200: %s", ready.Code, ready.Body.String())
	}
	if got := ready.Header().Get("Content-Type"); got != "image/jpeg" {
		t.Errorf("Content-Type = %q, want image/jpeg", got)
	}
	if got := ready.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Errorf("Cache-Control with the current v = %q, want immutable (§5.3)", got)
	}
	if got := e.get(base + "?w=120").Header().Get("Cache-Control"); got != "public, max-age=60, must-revalidate" {
		t.Errorf("Cache-Control without v = %q, want max-age=60, must-revalidate (§5.3)", got)
	}

	for _, stale := range []string{"stalestalestale", book.CV} {
		t.Run("409 v="+stale, func(t *testing.T) {
			body := errorBody(t, e.get(base+"?v="+stale), http.StatusConflict, CodeStaleVersion)
			if body.Detail["cv"] != book.CoverCV {
				t.Errorf("detail.cv = %v, want the cover_cv %q", body.Detail["cv"], book.CoverCV)
			}
		})
	}

	for _, bad := range []string{"?w=0", "?w=-5", "?w=wide"} {
		t.Run("w"+bad, func(t *testing.T) {
			body := errorBody(t, e.get(base+bad), http.StatusBadRequest, CodeBadRequest)
			if body.Detail["param"] != "w" {
				t.Errorf("detail.param = %v, want \"w\"", body.Detail["param"])
			}
		})
	}

	t.Run("unknown bid is 404", func(t *testing.T) {
		errorBody(t, e.get("/api/books/aaaaaaaaaaaaaaaa/cover"), http.StatusNotFound, CodeNotFound)
	})

	// E-69 §2.6 (answer Q4): a book with no pages at all has no cover — 404,
	// the FR-LIB-008 placeholder — while a book that is not `ok` but still has
	// pages (E-54) gets §2.2(b)'s automatic page-1 cover.
	t.Run("no pages is 404", func(t *testing.T) {
		errorBody(t, e.get("/api/books/"+e.bookBrokenID+"/cover"), http.StatusNotFound, CodeNotFound)
	})
	t.Run("status!=ok with pages gets the automatic cover", func(t *testing.T) {
		cv := e.wireBookOf(e.bookSalvagedID).CoverCV
		requireSize(t, e.fetchImage("/api/books/"+e.bookSalvagedID+"/cover?w=120&v="+cv), 16, 24,
			"the cover of a damaged book that still has its pages")
	})
}

// E-69 §2.2(b): with no choice the cover is page 1 — whole when it is not
// wider than tall, otherwise the first-page half in the effective reading
// direction, the odd pixel going to that half. §2.7: the page thumbnail does
// not change, so /thumbs/1 is still the whole page.
func TestBookCover_defaultIsPageOneOrItsFirstHalf(t *testing.T) {
	e := newEnv(t)

	portrait := e.fetchImage("/api/books/" + e.bookZipID + "/cover?w=120")
	requireSize(t, portrait, 16, 24, "the cover of a book whose page 1 is portrait")

	makeDirBookLandscape(t, e)
	// Fixture check through an endpoint that already exists: page 1 really is
	// the 25×10 picture, and the strip thumbnail keeps all of it.
	thumb := e.fetchImage("/api/books/" + e.bookDirID + "/thumbs/1?w=120")
	requireSize(t, thumb, landscapeW, landscapeH, "thumbs/1 of the landscape page (§2.7: unchanged)")

	ltrCV := e.wireBookOf(e.bookDirID).CoverCV
	ltr := e.fetchImage("/api/books/" + e.bookDirID + "/cover?w=120&v=" + ltrCV)
	requireSize(t, ltr, halfW, landscapeH, "the ltr cover of a landscape page 1")
	if got := hue(ltr); got != "red" {
		t.Errorf("ltr cover centre is %s, want red (the left half)", got)
	}

	e.putGlobalDir("rtl")
	rtlCV := e.wireBookOf(e.bookDirID).CoverCV
	if rtlCV == ltrCV {
		t.Fatalf("cover_cv did not change with the reading direction (§2.7 auto-ltr/auto-rtl): %q", rtlCV)
	}
	rtl := e.fetchImage("/api/books/" + e.bookDirID + "/cover?w=120&v=" + rtlCV)
	requireSize(t, rtl, halfW, landscapeH, "the rtl cover of a landscape page 1")
	if got := hue(rtl); got != "blue" {
		t.Errorf("rtl cover centre is %s, want blue (the right half)", got)
	}

	// §2.3: the series direction outranks the global one for the cover too.
	e.putSeriesDirOK(e.seriesFolderID, "ltr")
	pinned := e.wireBookOf(e.bookDirID).CoverCV
	if pinned != ltrCV {
		t.Errorf("cover_cv under series ltr + global rtl = %q, want the ltr value %q", pinned, ltrCV)
	}
	if got := hue(e.fetchImage("/api/books/" + e.bookDirID + "/cover?v=" + pinned)); got != "red" {
		t.Errorf("cover under series ltr + global rtl is %s, want red", got)
	}

	// And a book's own direction outranks the series'.
	if w := e.jsonBody(http.MethodPut, "/api/books/"+e.bookDirID+"/prefs", `{"reading_direction":"rtl"}`); w.Code != http.StatusOK {
		t.Fatalf("PUT book prefs = %d: %s", w.Code, w.Body.String())
	}
	if got := e.wireBookOf(e.bookDirID).CoverCV; got != rtlCV {
		t.Errorf("cover_cv under book rtl + series ltr = %q, want the rtl value %q", got, rtlCV)
	}
}

// --- PUT /api/books/{bid}/cover ---------------------------------------------

// E-69 §2.2(a)/§2.6: a stored choice is echoed, shows on every BookSummary
// with the same cover_cv, and the cover is that region of that page.
func TestBookCover_putStoresTheRegionAndTheCoverUsesIt(t *testing.T) {
	e := newEnv(t)
	before := e.wireBookOf(e.bookZipID)
	if before.Cover != nil {
		t.Fatalf("a book nobody chose a cover for has cover %+v, want null", *before.Cover)
	}

	want := wireCover{Page: 2, X: 0.5, Y: 0.5, W: 0.5, H: 0.5}
	cv, echoed := e.putCoverOK(e.bookZipID, `{"page":2,"x":0.5,"y":0.5,"w":0.5,"h":0.5}`)
	if echoed == nil || *echoed != want {
		t.Errorf("PUT response cover = %+v, want %+v", echoed, want)
	}
	if cv == before.CoverCV {
		t.Errorf("cover_cv did not change when a region was chosen: %q", cv)
	}

	detail := e.wireBookOf(e.bookZipID)
	if detail.CoverCV != cv || detail.Cover == nil || *detail.Cover != want {
		t.Errorf("BookDetail = cover_cv %q cover %+v, want %q %+v", detail.CoverCV, detail.Cover, cv, want)
	}
	series := e.wireSeriesOf(e.seriesFolderID)
	if got := series.Books[0]; got.ID != e.bookZipID || got.CoverCV != cv || got.Cover == nil || *got.Cover != want {
		t.Errorf("SeriesDetail.books[0] = %+v, want cover_cv %q cover %+v", got, cv, want)
	}

	// Page 2 is 20×30; half of each side is 10×15.
	img := e.fetchImage("/api/books/" + e.bookZipID + "/cover?w=120&v=" + cv)
	requireSize(t, img, 10, 15, "the cover cut from page 2 at {0.5, 0.5, 0.5, 0.5}")
}

// E-69 §2.4 — the server's whole validation list, each rule on both sides of
// its boundary, and §7.1's strict decoding. A refused body stores nothing.
func TestBookCover_validation(t *testing.T) {
	e := newEnv(t)
	body := func(page, x, y, w, h string) string {
		return `{"page":` + page + `,"x":` + x + `,"y":` + y + `,"w":` + w + `,"h":` + h + `}`
	}

	// Exact binary fractions, so no boundary depends on float rounding.
	accepted := []struct{ name, body string }{
		{"smallest region", body("1", "0", "0", "0.05", "0.05")},
		{"x+w exactly 1", body("1", "0.5", "0", "0.5", "0.5")},
		{"y+h exactly 1", body("1", "0", "0.25", "0.5", "0.75")},
		{"the whole page", body("1", "0", "0", "1", "1")},
		{"the last page", body("3", "0", "0", "1", "1")},
		// Answer Q8: 0.95+0.05 is exactly 1.0 in float64, so it is inside.
		{"0.95+0.05", body("1", "0.95", "0", "0.05", "0.5")},
	}
	for _, tc := range accepted {
		t.Run("200 "+tc.name, func(t *testing.T) {
			if w := e.putCover(e.bookZipID, tc.body); w.Code != http.StatusOK {
				t.Errorf("%s = %d, want 200: %s", tc.body, w.Code, w.Body.String())
			}
		})
	}
	if w := e.do(http.MethodDelete, "/api/books/"+e.bookZipID+"/cover", nil); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE = %d, want 204: %s", w.Code, w.Body.String())
	}

	refused := []struct{ name, body, field string }{
		{"x below 0", body("1", "-0.0625", "0", "0.5", "0.5"), ""},
		{"y below 0", body("1", "0", "-0.0625", "0.5", "0.5"), ""},
		{"w below 0.05", body("1", "0", "0", "0.0499", "0.5"), ""},
		{"h below 0.05", body("1", "0", "0", "0.5", "0.0499"), ""},
		{"x+w past 1", body("1", "0.5", "0", "0.5625", "0.5"), ""},
		{"y+h past 1", body("1", "0", "0.5", "0.5", "0.5625"), ""},
		// Answer Q8: no tolerance. 0.6 + 0.4000000000000002 is
		// 1.0000000000000002 in float64 (checked), one ulp past 1. (The
		// decimal complements such as 0.95+0.05 sum to exactly 1.0 in float64,
		// so they are accepted above, not refused here.)
		{"x+w one ulp past 1", body("1", "0.6", "0", "0.4000000000000002", "0.5"), ""},
		{"y+h one ulp past 1", body("1", "0", "0.6", "0.5", "0.4000000000000002"), ""},
		{"page 0", body("0", "0", "0", "0.5", "0.5"), ""},
		{"page past page_count", body("4", "0", "0", "0.5", "0.5"), ""},
		{"unknown field", `{"page":1,"x":0,"y":0,"w":0.5,"h":0.5,"z":1}`, "z"},
		{"page as a string", body(`"1"`, "0", "0", "0.5", "0.5"), "page"},
		{"x as a string", body("1", `"0"`, "0", "0.5", "0.5"), "x"},
		{"missing page", `{"x":0,"y":0,"w":0.5,"h":0.5}`, "page"},
		{"missing x", `{"page":1,"y":0,"w":0.5,"h":0.5}`, "x"},
		{"missing y", `{"page":1,"x":0,"w":0.5,"h":0.5}`, "y"},
		{"missing w", `{"page":1,"x":0,"y":0,"h":0.5}`, "w"},
		{"missing h", `{"page":1,"x":0,"y":0,"w":0.5}`, "h"},
		{"two documents", body("1", "0", "0", "0.5", "0.5") + body("2", "0", "0", "0.5", "0.5"), ""},
		{"empty body", "", ""},
		{"not JSON", "not json", ""},
		{"an array", "[]", ""},
	}
	for _, tc := range refused {
		t.Run("400 "+tc.name, func(t *testing.T) {
			got := errorBody(t, e.putCover(e.bookZipID, tc.body), http.StatusBadRequest, CodeBadRequest)
			if tc.field != "" && got.Detail["field"] != tc.field {
				t.Errorf("detail.field = %v, want %q", got.Detail["field"], tc.field)
			}
		})
	}
	if got := e.wireBookOf(e.bookZipID).Cover; got != nil {
		t.Errorf("after only refused PUTs the book has cover %+v, want null", *got)
	}

	// `status = ok` is part of the list: a damaged book that still serves its
	// pages (E-54) and an unreadable one are both refused.
	for _, bid := range []string{e.bookSalvagedID, e.bookBrokenID} {
		t.Run("400 status!=ok "+bid, func(t *testing.T) {
			errorBody(t, e.putCover(bid, body("1", "0", "0", "0.5", "0.5")), http.StatusBadRequest, CodeBadRequest)
		})
	}

	t.Run("unknown bid is 404", func(t *testing.T) {
		errorBody(t, e.putCover("aaaaaaaaaaaaaaaa", body("1", "0", "0", "0.5", "0.5")), http.StatusNotFound, CodeNotFound)
	})
}

// --- DELETE /api/books/{bid}/cover ------------------------------------------

// E-69 §2.6/§2.7: DELETE is 204 with or without a choice, and puts the cover
// back to the default — `cover: null` and the cover_cv the default had, since
// cover_cv is a hash of the same inputs.
func TestBookCover_deleteRestoresTheDefault(t *testing.T) {
	e := newEnv(t)
	target := "/api/books/" + e.bookZipID + "/cover"

	if w := e.do(http.MethodDelete, target, nil); w.Code != http.StatusNoContent || w.Body.Len() != 0 {
		t.Fatalf("DELETE with no choice = %d (%d bytes), want an empty 204", w.Code, w.Body.Len())
	}
	auto := e.wireBookOf(e.bookZipID).CoverCV

	chosen, _ := e.putCoverOK(e.bookZipID, `{"page":2,"x":0,"y":0,"w":0.5,"h":0.5}`)
	if chosen == auto {
		t.Fatalf("cover_cv %q did not change when a region was chosen", chosen)
	}

	if w := e.do(http.MethodDelete, target, nil); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE = %d, want 204: %s", w.Code, w.Body.String())
	}
	after := e.wireBookOf(e.bookZipID)
	if after.Cover != nil {
		t.Errorf("cover after DELETE = %+v, want null", *after.Cover)
	}
	if after.CoverCV != auto {
		t.Errorf("cover_cv after DELETE = %q, want the default's %q", after.CoverCV, auto)
	}
	requireSize(t, e.fetchImage(target+"?v="+after.CoverCV), 16, 24, "the cover after DELETE (page 1, whole)")

	if w := e.do(http.MethodDelete, target, nil); w.Code != http.StatusNoContent {
		t.Errorf("a second DELETE = %d, want 204", w.Code)
	}
	errorBody(t, e.do(http.MethodDelete, "/api/books/aaaaaaaaaaaaaaaa/cover", nil), http.StatusNotFound, CodeNotFound)
}

// E-69 §2.7 — cover_cv is a hash of book_id ‖ page_no ‖ region (auto-ltr /
// auto-rtl when there is no choice) ‖ format ‖ quality ‖ cv. So: the same
// choice twice is the same value; another region is another value; the
// direction moves it only for a book with no choice; the content version
// moves it for every book.
func TestBookCover_coverCVFollowsItsInputs(t *testing.T) {
	e := newEnv(t)
	region := `{"page":2,"x":0,"y":0,"w":0.5,"h":0.5}`

	a, _ := e.putCoverOK(e.bookZipID, region)
	b, _ := e.putCoverOK(e.bookZipID, region)
	if a != b {
		t.Errorf("the same choice twice gave cover_cv %q then %q", a, b)
	}
	other, _ := e.putCoverOK(e.bookZipID, `{"page":2,"x":0,"y":0,"w":0.5,"h":0.25}`)
	if other == a {
		t.Errorf("a different region kept cover_cv %q", a)
	}
	samePageElsewhere, _ := e.putCoverOK(e.bookZipID, `{"page":3,"x":0,"y":0,"w":0.5,"h":0.25}`)
	if samePageElsewhere == other {
		t.Errorf("the same region on another page kept cover_cv %q", other)
	}
	chosen, _ := e.putCoverOK(e.bookZipID, region)

	plainBefore := e.wireBookOf(e.bookDirID).CoverCV
	e.putGlobalDir("rtl")
	if got := e.wireBookOf(e.bookZipID).CoverCV; got != chosen {
		t.Errorf("the reading direction moved a chosen cover's cover_cv: %q -> %q", chosen, got)
	}
	plainAfter := e.wireBookOf(e.bookDirID).CoverCV
	if plainAfter == plainBefore {
		t.Errorf("the reading direction did not move cover_cv of a book with no choice (%q)", plainBefore)
	}

	reindexFixtureArchive(t, e, "a1b2c3d4e5f60799")
	if got := e.wireBookOf(e.bookZipID).CoverCV; got == chosen {
		t.Errorf("a new content version kept the chosen cover's cover_cv %q (FR-THM-006)", got)
	}
}

// E-69 §2.5 and answer Q7 — a choice whose page is gone, or whose book is not
// `ok`, falls back to §2.2(b) and is kept: BookSummary.cover still shows the
// stored row, cover_cv is computed from the cover actually served (page 1,
// automatic, effective direction) and so equals the value the book has with
// no choice at all, and when the page or the status comes back cover_cv
// returns to the chosen value.
//
// The book's content version is held at cvZip throughout, so the three
// cover_cv values are compared directly rather than by relation: the inputs
// that differ between them are only the page and the region.
func TestBookCover_aChoiceThatCannotApplyFallsBackAndIsKept(t *testing.T) {
	e := newEnv(t)
	zipPath := filepath.Join(e.media, filepath.FromSlash(bookZipPath))
	coverURL := func(cv string) string { return "/api/books/" + e.bookZipID + "/cover?v=" + cv }

	auto := e.wireBookOf(e.bookZipID).CoverCV                                            // no choice: page 1, auto-ltr
	chosenCV, row := e.putCoverOK(e.bookZipID, `{"page":3,"x":0,"y":0,"w":0.5,"h":0.5}`) // page 3 is 8×12 -> 4×6
	if row == nil {
		t.Fatal("PUT response cover is null")
	}
	if chosenCV == auto {
		t.Fatalf("choosing a region kept the automatic cover_cv %q", auto)
	}

	unusable := func(t *testing.T, why string) {
		t.Helper()
		b := e.wireBookOf(e.bookZipID)
		if b.Cover == nil || *b.Cover != *row {
			t.Errorf("%s: BookSummary.cover = %+v, want the stored row %+v (Q7)", why, b.Cover, *row)
		}
		if b.CoverCV != auto {
			t.Errorf("%s: cover_cv = %q, want the automatic cover's %q (Q7)", why, b.CoverCV, auto)
		}
		requireSize(t, e.fetchImage(coverURL(b.CoverCV)), 16, 24, why+": the cover (§2.2(b): page 1)")
		if n := len(exportCovers(t, e)); n != 1 {
			t.Errorf("%s: export lists %d cover rows, want the row kept", why, n)
		}
	}
	usable := func(t *testing.T, why string) {
		t.Helper()
		b := e.wireBookOf(e.bookZipID)
		if b.CoverCV != chosenCV {
			t.Errorf("%s: cover_cv = %q, want the chosen value %q back (Q7)", why, b.CoverCV, chosenCV)
		}
		requireSize(t, e.fetchImage(coverURL(b.CoverCV)), 4, 6, why+": the cover (the choice applies again)")
	}

	t.Run("the page is gone", func(t *testing.T) {
		reindexBookPages(t, e, e.bookZipID, zipPath, cvZip, e.zipPages[:2])
		unusable(t, "page 3 gone")
		reindexBookPages(t, e, e.bookZipID, zipPath, cvZip, e.zipPages)
		usable(t, "page 3 back")
	})

	t.Run("the book is not ok", func(t *testing.T) {
		setBookStatus(t, e, e.bookZipID, "error", "zip: central directory unreadable, rebuilt from local file headers")
		unusable(t, "status=error")
		setBookStatus(t, e, e.bookZipID, "ok", "")
		usable(t, "status=ok again")
	})
}

// --- series ----------------------------------------------------------------

// E-69 §2.6 — a `cover_kind='page'` series' cover IS its cover book's book
// cover, cover_cv included; a `cover_kind='file'` series ignores choices.
func TestSeriesCover_followsTheCoverBooksBookCover(t *testing.T) {
	e := newEnv(t)

	t.Run("cover_kind=page", func(t *testing.T) {
		s := e.wireSeriesOf(e.seriesCloverID)
		if s.CoverCV == nil || *s.CoverCV != s.Books[0].CoverCV {
			t.Fatalf("series cover_cv = %v, want the cover book's cover_cv %q", s.CoverCV, s.Books[0].CoverCV)
		}
		if got := e.seriesListCoverCV(e.seriesCloverID); got == nil || *got != s.Books[0].CoverCV {
			t.Errorf("GET /api/series cover_cv = %v, want %q", got, s.Books[0].CoverCV)
		}
		old := *s.CoverCV

		chosen, _ := e.putCoverOK(e.bookCloverID, `{"page":2,"x":0.5,"y":0.5,"w":0.5,"h":0.5}`)
		s = e.wireSeriesOf(e.seriesCloverID)
		if s.CoverCV == nil || *s.CoverCV != chosen || *s.CoverCV == old {
			t.Fatalf("series cover_cv after choosing the first book's cover = %v, want %q (was %q)", s.CoverCV, chosen, old)
		}
		if got := e.seriesListCoverCV(e.seriesCloverID); got == nil || *got != chosen {
			t.Errorf("GET /api/series cover_cv = %v, want %q", got, chosen)
		}
		img := e.fetchImage("/api/series/" + e.seriesCloverID + "/cover?w=120&v=" + chosen)
		requireSize(t, img, 10, 15, "the series cover after choosing its first book's cover")

		body := errorBody(t, e.get("/api/series/"+e.seriesCloverID+"/cover?v="+cvClover), http.StatusConflict, CodeStaleVersion)
		if body.Detail["cv"] != chosen {
			t.Errorf("detail.cv = %v, want %q", body.Detail["cv"], chosen)
		}
	})

	t.Run("cover_kind=file", func(t *testing.T) {
		before := e.wireSeriesOf(e.seriesFolderID).CoverCV
		e.putCoverOK(e.bookZipID, `{"page":2,"x":0.5,"y":0.5,"w":0.5,"h":0.5}`)
		e.putSeriesDirOK(e.seriesFolderID, "rtl")
		after := e.wireSeriesOf(e.seriesFolderID).CoverCV
		if !equalStringPtr(before, after) {
			t.Errorf("cover_kind=file series cover_cv moved %v -> %v", deref(before), deref(after))
		}
		// [cover].jpg is 24×36: the file as it is, no region, no half.
		requireSize(t, e.fetchImage("/api/series/"+e.seriesFolderID+"/cover?w=120"), 24, 36, "the loose cover file")
	})
}

func equalStringPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func deref(s *string) string {
	if s == nil {
		return "<null>"
	}
	return *s
}

// E-69 §2.6 — `PUT /api/series/{sid}/prefs` and `SeriesDetail.reading_direction`.
func TestSeriesPrefs_putAndDetail(t *testing.T) {
	e := newEnv(t)

	if got := e.wireSeriesOf(e.seriesFolderID).ReadingDirection; got != nil {
		t.Fatalf("reading_direction before any write = %q, want null", *got)
	}
	for _, dir := range []string{"rtl", "ltr"} {
		e.putSeriesDirOK(e.seriesFolderID, dir)
		if got := e.wireSeriesOf(e.seriesFolderID).ReadingDirection; got == nil || *got != dir {
			t.Errorf("reading_direction after PUT %s = %v", dir, deref(got))
		}
	}
	if got := e.wireSeriesOf(e.seriesCloverID).ReadingDirection; got != nil {
		t.Errorf("another series' reading_direction = %q, want null", *got)
	}
	e.putSeriesDirOK(e.seriesFolderID, "")
	if got := e.wireSeriesOf(e.seriesFolderID).ReadingDirection; got != nil {
		t.Errorf("reading_direction after PUT null = %q, want null", *got)
	}

	refused := []struct{ name, body, field string }{
		{"not a ReadingDir", `{"reading_direction":"up"}`, ""},
		{"unknown field", `{"direction":"rtl"}`, "direction"},
		{"wrong type", `{"reading_direction":1}`, "reading_direction"},
		// E-69 §2.6 (answer Q2): the field is required; only an explicit
		// null means "no series direction".
		{"missing reading_direction", `{}`, "reading_direction"},
		{"two documents", `{"reading_direction":"rtl"}{"reading_direction":"ltr"}`, ""},
		{"not JSON", "not json", ""},
		{"empty body", "", ""},
	}
	for _, tc := range refused {
		t.Run("400 "+tc.name, func(t *testing.T) {
			got := errorBody(t, e.putSeriesPrefs(e.seriesFolderID, tc.body), http.StatusBadRequest, CodeBadRequest)
			if tc.field != "" && got.Detail["field"] != tc.field {
				t.Errorf("detail.field = %v, want %q", got.Detail["field"], tc.field)
			}
		})
	}
	if got := e.wireSeriesOf(e.seriesFolderID).ReadingDirection; got != nil {
		t.Errorf("reading_direction after refused PUTs = %q, want null", *got)
	}

	t.Run("unknown sid is 404", func(t *testing.T) {
		errorBody(t, e.putSeriesPrefs("aaaaaaaaaaaaaaaa", `{"reading_direction":"rtl"}`), http.StatusNotFound, CodeNotFound)
	})
	for _, id := range []string{"NOT-AN-ID", "aaaaaaaaaaaaaaa1"} {
		t.Run("malformed sid "+id, func(t *testing.T) {
			got := errorBody(t, e.putSeriesPrefs(id, `{"reading_direction":"rtl"}`), http.StatusBadRequest, CodeBadRequest)
			if got.Detail["param"] != "sid" {
				t.Errorf("detail.param = %v, want \"sid\"", got.Detail["param"])
			}
		})
	}
}

// E-69 §2.3 — BookPrefs.reading_direction is book → series → global, field by
// field; a series choice survives a global change; is_override still means
// "the book has a row".
func TestBookPrefs_readingDirectionLadder(t *testing.T) {
	e := newEnv(t)
	prefsOf := func(bid string) BookPrefs {
		t.Helper()
		return decodeBody[BookPrefs](t, e.get("/api/books/"+bid+"/prefs"), http.StatusOK)
	}
	detailDir := func(bid string) string {
		t.Helper()
		return decodeBody[BookDetail](t, e.get("/api/books/"+bid), http.StatusOK).Prefs.ReadingDirection
	}
	putBook := func(bid, body string) {
		t.Helper()
		if w := e.jsonBody(http.MethodPut, "/api/books/"+bid+"/prefs", body); w.Code != http.StatusOK {
			t.Fatalf("PUT book prefs %s = %d: %s", body, w.Code, w.Body.String())
		}
	}

	e.putGlobalDir("ltr")
	e.putSeriesDirOK(e.seriesFolderID, "rtl")
	if p := prefsOf(e.bookZipID); p.ReadingDirection != "rtl" || p.IsOverride {
		t.Errorf("book with no row under series rtl = %+v, want rtl and is_override false", p)
	}
	if got := detailDir(e.bookZipID); got != "rtl" {
		t.Errorf("BookDetail.prefs.reading_direction = %q, want rtl", got)
	}
	if p := prefsOf(e.bookCloverID); p.ReadingDirection != "ltr" {
		t.Errorf("a book of another series = %q, want the global ltr", p.ReadingDirection)
	}

	// A series choice survives the global default moving either way.
	e.putGlobalDir("rtl")
	e.putSeriesDirOK(e.seriesFolderID, "ltr")
	e.putGlobalDir("ltr")
	e.putGlobalDir("rtl")
	if p := prefsOf(e.bookZipID); p.ReadingDirection != "ltr" {
		t.Errorf("series ltr under global rtl = %q, want ltr", p.ReadingDirection)
	}

	// Field by field: a row that sets only the display mode keeps the series
	// direction (the seed is no longer lost to is_override).
	putBook(e.bookZipID, `{"display_mode":"spread"}`)
	if p := prefsOf(e.bookZipID); p.ReadingDirection != "ltr" || !p.IsOverride || p.DisplayMode != "spread" {
		t.Errorf("display-mode-only row under series ltr = %+v, want ltr, spread, is_override", p)
	}
	putBook(e.bookZipID, `{"reading_direction":"rtl"}`)
	if p := prefsOf(e.bookZipID); p.ReadingDirection != "rtl" {
		t.Errorf("book rtl over series ltr = %q, want rtl", p.ReadingDirection)
	}
	putBook(e.bookZipID, `{"reading_direction":null}`)
	if p := prefsOf(e.bookZipID); p.ReadingDirection != "ltr" {
		t.Errorf("book direction cleared under series ltr = %q, want ltr", p.ReadingDirection)
	}

	e.putSeriesDirOK(e.seriesFolderID, "")
	if p := prefsOf(e.bookZipID); p.ReadingDirection != "rtl" {
		t.Errorf("series cleared under global rtl = %q, want rtl", p.ReadingDirection)
	}
}

// --- export / import (E-69 §2.9) --------------------------------------------

type v2Cover struct {
	BookID    string  `json:"book_id"`
	SeriesID  string  `json:"series_id"`
	RootName  string  `json:"root_name"`
	BookPath  string  `json:"book_path"`
	Page      int     `json:"page"`
	PageCount int     `json:"page_count"`
	X         float64 `json:"x"`
	Y         float64 `json:"y"`
	W         float64 `json:"w"`
	H         float64 `json:"h"`
	UpdatedAt int64   `json:"updated_at"`
}

type v2SeriesPref struct {
	SeriesID         string  `json:"series_id"`
	RootName         string  `json:"root_name"`
	SeriesPath       string  `json:"series_path"`
	ReadingDirection *string `json:"reading_direction"`
}

type v2Doc struct {
	Format      string                `json:"format"`
	ExportedAt  int64                 `json:"exported_at"`
	IDVersion   string                `json:"id_version"`
	Items       []userdata.ExportItem `json:"items"`
	Prefs       []userdata.ExportPref `json:"prefs"`
	Covers      []v2Cover             `json:"covers"`
	SeriesPrefs []v2SeriesPref        `json:"series_prefs"`
}

var (
	coverRowKeys      = []string{"book_id", "book_path", "h", "page", "page_count", "root_name", "series_id", "updated_at", "w", "x", "y"}
	seriesPrefRowKeys = []string{"reading_direction", "root_name", "series_id", "series_path"}
)

func newV2Doc() v2Doc {
	return v2Doc{
		Format: "shelf-progress/2", ExportedAt: fixedNow.Unix(), IDVersion: "shelf-id/1",
		Items: []userdata.ExportItem{}, Prefs: []userdata.ExportPref{},
		Covers: []v2Cover{}, SeriesPrefs: []v2SeriesPref{},
	}
}

func (e *env) zipCoverRow(page int, x, y, w, h float64, updatedAt int64) v2Cover {
	return v2Cover{
		BookID: e.bookZipID, SeriesID: e.seriesFolderID, RootName: rootName, BookPath: bookZipPath,
		Page: page, PageCount: len(e.zipPages), X: x, Y: y, W: w, H: h, UpdatedAt: updatedAt,
	}
}

func (e *env) folderPrefRow(dir *string) v2SeriesPref {
	return v2SeriesPref{SeriesID: e.seriesFolderID, RootName: rootName, SeriesPath: seriesFolderPath, ReadingDirection: dir}
}

func strp(s string) *string { return &s }

func (e *env) importDoc(target string, doc any) *httptest.ResponseRecorder {
	e.t.Helper()
	raw, err := json.Marshal(doc)
	if err != nil {
		e.t.Fatalf("encoding the import document: %v", err)
	}
	return e.jsonBody(http.MethodPost, target, string(raw))
}

// exportCovers returns the export's `covers` rows as raw objects.
func exportCovers(t *testing.T, e *env) []map[string]json.RawMessage {
	t.Helper()
	return exportArray(t, e, "covers")
}

func exportArray(t *testing.T, e *env, key string) []map[string]json.RawMessage {
	t.Helper()
	w := e.get("/api/progress/export")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/progress/export = %d: %s", w.Code, w.Body.String())
	}
	doc := rawObject(t, w.Body.Bytes(), "ProgressExport")
	requireKeys(t, doc, "ProgressExport", key)
	var rows []map[string]json.RawMessage
	unmarshalInto(t, doc[key], &rows, "ProgressExport."+key)
	return rows
}

// E-69 §2.9 — the export is always /2 and carries both new arrays, with
// exactly the listed fields; `page_count` is the book's length when the choice
// was made (§2.5).
func TestProgressExport_v2CarriesCoversAndSeriesPrefs(t *testing.T) {
	e := newEnv(t)
	e.putCoverOK(e.bookZipID, `{"page":2,"x":0.25,"y":0.125,"w":0.5,"h":0.75}`)
	e.putSeriesDirOK(e.seriesFolderID, "rtl")

	w := e.get("/api/progress/export")
	doc := rawObject(t, w.Body.Bytes(), "ProgressExport")
	if got := string(doc["format"]); got != `"shelf-progress/2"` {
		t.Errorf("format = %s, want \"shelf-progress/2\"", got)
	}

	covers := exportCovers(t, e)
	if len(covers) != 1 {
		t.Fatalf("export covers = %d rows, want 1", len(covers))
	}
	if got := keysOf(covers[0]); !slices.Equal(got, coverRowKeys) {
		t.Errorf("covers row keys = %v, want %v", got, coverRowKeys)
	}
	var row v2Cover
	raw, _ := json.Marshal(covers[0])
	unmarshalInto(t, raw, &row, "covers[0]")
	if want := e.zipCoverRow(2, 0.25, 0.125, 0.5, 0.75, fixedNow.Unix()); row != want {
		t.Errorf("covers[0] = %+v, want %+v", row, want)
	}

	prefs := exportArray(t, e, "series_prefs")
	if len(prefs) != 1 {
		t.Fatalf("export series_prefs = %d rows, want 1", len(prefs))
	}
	if got := keysOf(prefs[0]); !slices.Equal(got, seriesPrefRowKeys) {
		t.Errorf("series_prefs row keys = %v, want %v", got, seriesPrefRowKeys)
	}
	var pref v2SeriesPref
	raw, _ = json.Marshal(prefs[0])
	unmarshalInto(t, raw, &pref, "series_prefs[0]")
	if pref.SeriesID != e.seriesFolderID || pref.RootName != rootName || pref.SeriesPath != seriesFolderPath ||
		pref.ReadingDirection == nil || *pref.ReadingDirection != "rtl" {
		t.Errorf("series_prefs[0] = %+v, want the folder series at rtl", pref)
	}

	// And it round-trips into a server that has neither.
	fresh := newEnv(t)
	if w := fresh.jsonBody(http.MethodPost, "/api/progress/import", w.Body.String()); w.Code != http.StatusOK {
		t.Fatalf("importing the export = %d: %s", w.Code, w.Body.String())
	}
	want := wireCover{Page: 2, X: 0.25, Y: 0.125, W: 0.5, H: 0.75}
	if got := fresh.wireBookOf(fresh.bookZipID).Cover; got == nil || *got != want {
		t.Errorf("cover after import = %+v, want %+v", got, want)
	}
	if got := fresh.wireSeriesOf(fresh.seriesFolderID).ReadingDirection; got == nil || *got != "rtl" {
		t.Errorf("series reading_direction after import = %v, want rtl", deref(got))
	}
}

// E-69 §2.9 — covers and series_prefs are not counted in imported/skipped/
// conflicts, which describe items only.
func TestProgressImport_v2ArraysAreNotCounted(t *testing.T) {
	item := func(e *env) userdata.ExportItem {
		return userdata.ExportItem{
			BookID: e.bookDirID, SeriesID: e.seriesFolderID, RootName: rootName, BookPath: bookDirPath,
			LastPage: 2, PageCount: 2, Completed: true, StartedAt: 1785229100, UpdatedAt: 1785229150,
		}
	}
	run := func(withArrays bool) userdata.ImportResult {
		e := newEnv(t)
		doc := newV2Doc()
		doc.Items = []userdata.ExportItem{item(e)}
		if withArrays {
			doc.Covers = []v2Cover{e.zipCoverRow(1, 0, 0, 0.5, 0.5, fixedNow.Unix())}
			doc.SeriesPrefs = []v2SeriesPref{e.folderPrefRow(strp("rtl"))}
		}
		return decodeBody[userdata.ImportResult](t, e.importDoc("/api/progress/import", doc), http.StatusOK)
	}
	if with, without := run(true), run(false); with != without {
		t.Errorf("import result with covers/series_prefs = %+v, without = %+v; they must not be counted", with, without)
	}

	e := newEnv(t)
	doc := newV2Doc()
	doc.Covers = []v2Cover{e.zipCoverRow(1, 0, 0, 0.5, 0.5, fixedNow.Unix())}
	doc.SeriesPrefs = []v2SeriesPref{e.folderPrefRow(strp("rtl"))}
	if got := decodeBody[userdata.ImportResult](t, e.importDoc("/api/progress/import", doc), http.StatusOK); got != (userdata.ImportResult{}) {
		t.Errorf("a document with only covers and series_prefs counted %+v, want all zero", got)
	}
}

// E-69 §2.9 — one bad region refuses the whole document: no item, no series
// pref and no other cover lands. Strict decoding (§7.1) reaches into the new
// rows too.
func TestProgressImport_v2RefusesTheWholeDocument(t *testing.T) {
	bad := []struct {
		name  string
		cover func(e *env) v2Cover
	}{
		{"x below 0", func(e *env) v2Cover { return e.zipCoverRow(1, -0.0625, 0, 0.5, 0.5, 1) }},
		{"y below 0", func(e *env) v2Cover { return e.zipCoverRow(1, 0, -0.0625, 0.5, 0.5, 1) }},
		{"w below 0.05", func(e *env) v2Cover { return e.zipCoverRow(1, 0, 0, 0.0499, 0.5, 1) }},
		{"h below 0.05", func(e *env) v2Cover { return e.zipCoverRow(1, 0, 0, 0.5, 0.0499, 1) }},
		{"x+w past 1", func(e *env) v2Cover { return e.zipCoverRow(1, 0.5, 0, 0.5625, 0.5, 1) }},
		{"y+h past 1", func(e *env) v2Cover { return e.zipCoverRow(1, 0, 0.5, 0.5, 0.5625, 1) }},
		// Answer Q3: the page is checked against the ROW's page_count only.
		{"page 0", func(e *env) v2Cover { return e.zipCoverRow(0, 0, 0, 0.5, 0.5, 1) }},
		{"page past the row's page_count", func(e *env) v2Cover {
			c := e.zipCoverRow(4, 0, 0, 0.5, 0.5, 1)
			c.PageCount = 3
			return c
		}},
		{"page_count 0", func(e *env) v2Cover {
			c := e.zipCoverRow(1, 0, 0, 0.5, 0.5, 1)
			c.PageCount = 0
			return c
		}},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			doc := newV2Doc()
			doc.Items = []userdata.ExportItem{{
				BookID: e.bookDirID, SeriesID: e.seriesFolderID, RootName: rootName, BookPath: bookDirPath,
				LastPage: 2, PageCount: 2, Completed: true, StartedAt: 1785229100, UpdatedAt: 1785229150,
			}}
			doc.SeriesPrefs = []v2SeriesPref{e.folderPrefRow(strp("rtl"))}
			good := v2Cover{
				BookID: e.bookCloverID, SeriesID: e.seriesCloverID, RootName: rootName, BookPath: bookCloverPath,
				Page: 1, PageCount: len(e.zipPages), X: 0, Y: 0, W: 0.5, H: 0.5, UpdatedAt: 1,
			}
			doc.Covers = []v2Cover{good, tc.cover(e)}

			errorBody(t, e.importDoc("/api/progress/import", doc), http.StatusBadRequest, CodeBadRequest)

			if _, err := e.user.GetProgress(t.Context(), e.bookDirID); !errors.Is(err, userdata.ErrNotFound) {
				t.Errorf("the refused document's item was written (err %v)", err)
			}
			if got := e.wireSeriesOf(e.seriesFolderID).ReadingDirection; got != nil {
				t.Errorf("the refused document's series pref was written: %q", *got)
			}
			if got := e.wireBookOf(e.bookCloverID).Cover; got != nil {
				t.Errorf("the refused document's valid cover was written: %+v", *got)
			}
		})
	}

	strict := []struct{ name, doc string }{
		{"unknown field in a covers row", `{"format":"shelf-progress/2","exported_at":1,"id_version":"shelf-id/1","items":[],"prefs":[],` +
			`"covers":[{"book_id":"{bid}","series_id":"{sid}","root_name":"manga","book_path":"p","page":1,"page_count":3,` +
			`"x":0,"y":0,"w":0.5,"h":0.5,"updated_at":1,"zoom":2}],"series_prefs":[]}`},
		{"unknown field in a series_prefs row", `{"format":"shelf-progress/2","exported_at":1,"id_version":"shelf-id/1","items":[],"prefs":[],` +
			`"covers":[],"series_prefs":[{"series_id":"{sid}","root_name":"manga","series_path":"p","reading_direction":"rtl","dir":"rtl"}]}`},
		{"a series_prefs direction outside ReadingDir", `{"format":"shelf-progress/2","exported_at":1,"id_version":"shelf-id/1","items":[],"prefs":[],` +
			`"covers":[],"series_prefs":[{"series_id":"{sid}","root_name":"manga","series_path":"p","reading_direction":"up"}]}`},
		{"an unknown format", `{"format":"shelf-progress/3","exported_at":1,"id_version":"shelf-id/1","items":[],"prefs":[]}`},
		// Answer Q3: a covers row with no page_count reads as 0, which no
		// page satisfies.
		{"a covers row without page_count", `{"format":"shelf-progress/2","exported_at":1,"id_version":"shelf-id/1","items":[],"prefs":[],` +
			`"covers":[{"book_id":"{bid}","series_id":"{sid}","root_name":"manga","book_path":"p","page":1,` +
			`"x":0,"y":0,"w":0.5,"h":0.5,"updated_at":1}],"series_prefs":[]}`},
		// Answer Q3: /1 has neither array, so carrying one is refused.
		{"a /1 document with covers", `{"format":"shelf-progress/1","exported_at":1,"id_version":"shelf-id/1","items":[],"prefs":[],` +
			`"covers":[{"book_id":"{bid}","series_id":"{sid}","root_name":"manga","book_path":"p","page":1,"page_count":3,` +
			`"x":0,"y":0,"w":0.5,"h":0.5,"updated_at":1}]}`},
		{"a /1 document with series_prefs", `{"format":"shelf-progress/1","exported_at":1,"id_version":"shelf-id/1","items":[],"prefs":[],` +
			`"series_prefs":[{"series_id":"{sid}","root_name":"manga","series_path":"p","reading_direction":"rtl"}]}`},
		{"a /1 document with empty new arrays", `{"format":"shelf-progress/1","exported_at":1,"id_version":"shelf-id/1","items":[],"prefs":[],` +
			`"covers":[],"series_prefs":[]}`},
	}
	for _, tc := range strict {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			// The control: a well-formed /2 document IS accepted here, so the
			// 400 below is the refusal of this one row and not of `covers`
			// as such — which a server that predates E-69 also refuses.
			if w := e.importDoc("/api/progress/import", newV2Doc()); w.Code != http.StatusOK {
				t.Fatalf("control: an empty /2 document = %d, want 200: %s", w.Code, w.Body.String())
			}
			doc := strings.NewReplacer("{bid}", e.bookZipID, "{sid}", e.seriesFolderID).Replace(tc.doc)
			errorBody(t, e.jsonBody(http.MethodPost, "/api/progress/import", doc), http.StatusBadRequest, CodeBadRequest)
		})
	}
}

// E-69 §2.9 (answer Q3) — a covers row is checked against itself, not against
// the index: page 5 of a row whose page_count is 5 lands although the local
// book has 3 pages (§2.5 keeps such a row and falls back to §2.2(b)).
func TestProgressImport_coversRowIsCheckedAgainstItself(t *testing.T) {
	e := newEnv(t)
	row := e.zipCoverRow(5, 0, 0, 0.5, 0.5, fixedNow.Unix())
	row.PageCount = 5
	doc := newV2Doc()
	doc.Covers = []v2Cover{row}
	if w := e.importDoc("/api/progress/import", doc); w.Code != http.StatusOK {
		t.Fatalf("import = %d, want 200: %s", w.Code, w.Body.String())
	}
	if n := len(exportCovers(t, e)); n != 1 {
		t.Errorf("export lists %d cover rows after the import, want 1", n)
	}
	cv := e.wireBookOf(e.bookZipID).CoverCV
	requireSize(t, e.fetchImage("/api/books/"+e.bookZipID+"/cover?v="+cv), 16, 24,
		"the cover while the imported page is not in the book (§2.2(b))")
}

// E-69 §2.9 — covers merge like items: the newer updated_at wins and a tie
// keeps the local row; `?strategy=replace` overwrites regardless (answer Q9). The local choice is written at fixedNow (the harness
// clock).
func TestProgressImport_coversMergeByUpdatedAt(t *testing.T) {
	local := wireCover{Page: 2, X: 0, Y: 0, W: 0.5, H: 0.5}
	incoming := wireCover{Page: 1, X: 0.5, Y: 0.5, W: 0.5, H: 0.5}
	const merge, replace = "/api/progress/import", "/api/progress/import?strategy=replace"
	cases := []struct {
		name    string
		target  string
		updated int64
		want    wireCover
	}{
		{"older incoming keeps local", merge, fixedNow.Add(-time.Hour).Unix(), local},
		{"a tie keeps local", merge, fixedNow.Unix(), local},
		{"newer incoming wins", merge, fixedNow.Add(time.Hour).Unix(), incoming},
		// Answer Q9: replace overwrites whatever the timestamps say.
		{"replace overwrites an older incoming", replace, fixedNow.Add(-time.Hour).Unix(), incoming},
		{"replace overwrites a tie", replace, fixedNow.Unix(), incoming},
		{"replace overwrites a newer incoming", replace, fixedNow.Add(time.Hour).Unix(), incoming},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.putCoverOK(e.bookZipID, `{"page":2,"x":0,"y":0,"w":0.5,"h":0.5}`)
			doc := newV2Doc()
			doc.Covers = []v2Cover{e.zipCoverRow(incoming.Page, incoming.X, incoming.Y, incoming.W, incoming.H, tc.updated)}
			if w := e.importDoc(tc.target, doc); w.Code != http.StatusOK {
				t.Fatalf("import = %d: %s", w.Code, w.Body.String())
			}
			if got := e.wireBookOf(e.bookZipID).Cover; got == nil || *got != tc.want {
				t.Errorf("cover after import = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// E-69 §2.9 — series_prefs follow A-17's prefs rule: a local choice is kept
// under merge, replace overwrites it, null is ignored under both, and a series
// with no local choice takes the incoming one.
func TestProgressImport_seriesPrefsFollowA17(t *testing.T) {
	cases := []struct {
		name     string
		local    string // "" = no local choice
		incoming *string
		target   string
		want     string // "" = null
	}{
		{"merge keeps local", "rtl", strp("ltr"), "/api/progress/import", "rtl"},
		{"replace overwrites", "rtl", strp("ltr"), "/api/progress/import?strategy=replace", "ltr"},
		{"merge ignores null", "rtl", nil, "/api/progress/import", "rtl"},
		{"replace ignores null", "rtl", nil, "/api/progress/import?strategy=replace", "rtl"},
		{"merge fills an empty series", "", strp("rtl"), "/api/progress/import", "rtl"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			if tc.local != "" {
				e.putSeriesDirOK(e.seriesFolderID, tc.local)
			}
			doc := newV2Doc()
			doc.SeriesPrefs = []v2SeriesPref{e.folderPrefRow(tc.incoming)}
			if w := e.importDoc(tc.target, doc); w.Code != http.StatusOK {
				t.Fatalf("import = %d: %s", w.Code, w.Body.String())
			}
			got := e.wireSeriesOf(e.seriesFolderID).ReadingDirection
			if (tc.want == "" && got != nil) || (tc.want != "" && (got == nil || *got != tc.want)) {
				t.Errorf("series reading_direction after import = %s, want %q", deref(got), tc.want)
			}
		})
	}
}
