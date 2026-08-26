package httpapi

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"shelf/internal/index"
	"shelf/internal/scanner"
)

// FR-SRV-008 / AC-001 — the response body is byte-identical to the archive
// member. No resize, no re-encode, no EXIF stripping, and no temporary file:
// the bytes come out of the ZIP at a stored offset and go into the socket.
func TestPage_bodyIsByteIdenticalToTheArchiveMember(t *testing.T) {
	e := newEnv(t)

	cases := []struct {
		name string
		page int
		want []byte
		ct   string
	}{
		{"stored entry", 1, e.zipPayloads[0], "image/jpeg"},
		{"deflated entry", 2, e.zipPayloads[1], "image/jpeg"},
		{"deflated png", 3, e.zipPayloads[2], "image/png"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := e.get(fmt.Sprintf("/api/books/%s/pages/%d", e.bookZipID, tc.page))
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", w.Code, w.Body.String())
			}
			if !bytes.Equal(w.Body.Bytes(), tc.want) {
				t.Fatalf("body differs from the archive member (%d vs %d bytes)", w.Body.Len(), len(tc.want))
			}
			if got := w.Header().Get("Content-Type"); got != tc.ct {
				t.Errorf("Content-Type = %q, want %q (from the extension table, never sniffed)", got, tc.ct)
			}
			if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
			}
			if got := w.Header().Get("Content-Length"); got != strconv.Itoa(len(tc.want)) {
				t.Errorf("Content-Length = %q, want %d", got, len(tc.want))
			}
		})
	}
}

// FR-SRV-005 — a page of a directory book is served from the filesystem, and is
// equally byte-identical.
func TestPage_directoryBookIsServedFromTheFilesystem(t *testing.T) {
	e := newEnv(t)
	w := e.get("/api/books/" + e.bookDirID + "/pages/1")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if !bytes.Equal(w.Body.Bytes(), e.dirPayloads[0]) {
		t.Fatal("a dir page's body differs from the file on disk")
	}
}

// FR-SRV-007 / arch §5.3 — the `?v=` matrix. `immutable` is a promise that the
// bytes never change, and only a URL carrying the current content version can
// honour it.
func TestPage_cacheControlMatrix(t *testing.T) {
	e := newEnv(t)
	base := "/api/books/" + e.bookZipID + "/pages/1"

	t.Run("v absent is cacheable for a minute", func(t *testing.T) {
		w := e.get(base)
		if got := w.Header().Get("Cache-Control"); got != "public, max-age=60, must-revalidate" {
			t.Errorf("Cache-Control = %q", got)
		}
	})

	t.Run("v matching the cv is immutable", func(t *testing.T) {
		w := e.get(base + "?v=" + cvZip)
		if got := w.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
			t.Errorf("Cache-Control = %q", got)
		}
	})

	t.Run("a stale v is 409 with the current cv", func(t *testing.T) {
		w := e.get(base + "?v=notthecurrentone")
		body := errorBody(t, w, http.StatusConflict, CodeStaleVersion)
		if body.Detail["cv"] != cvZip {
			t.Errorf("detail.cv = %v, want %q", body.Detail["cv"], cvZip)
		}
	})
}

// arch §5.3 — the strong ETag forms, and the 304 they enable. The ZIP form
// carries the entry's CRC-32, which is free from the central directory and is
// derived from the bytes rather than from their metadata.
func TestPage_etagFormsAnd304(t *testing.T) {
	e := newEnv(t)

	cases := []struct {
		name   string
		target string
		prefix string
	}{
		{"stored zip page", "/api/books/" + e.bookZipID + "/pages/1", `"p1-` + e.bookZipID + `-1-`},
		{"deflated zip page", "/api/books/" + e.bookZipID + "/pages/2", `"p1-` + e.bookZipID + `-2-`},
		{"dir page", "/api/books/" + e.bookDirID + "/pages/1", `"f1-` + e.bookDirID + `-1-`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := e.get(tc.target)
			etag := w.Header().Get("ETag")
			if !strings.HasPrefix(etag, tc.prefix) {
				t.Fatalf("ETag = %q, want the %s form", etag, tc.prefix)
			}
			if !strings.HasSuffix(etag, `"`) {
				t.Errorf("ETag = %q, want a quoted strong validator", etag)
			}

			nm := e.get(tc.target, func(r *http.Request) { r.Header.Set("If-None-Match", etag) })
			if nm.Code != http.StatusNotModified {
				t.Fatalf("If-None-Match = %d, want 304", nm.Code)
			}
			if nm.Body.Len() != 0 {
				t.Errorf("a 304 carried a %d-byte body", nm.Body.Len())
			}

			// A different validator must not match.
			other := e.get(tc.target, func(r *http.Request) { r.Header.Set("If-None-Match", `"p1-nope"`) })
			if other.Code != http.StatusOK {
				t.Errorf("a non-matching If-None-Match = %d, want 200", other.Code)
			}
		})
	}
}

// arch §5.3's range policy: stored entries and dir pages seek, deflated entries
// do not. Advertising `Accept-Ranges` on a forward-only stream would be a lie,
// and answering 200 to a Range request is explicitly legal.
func TestPage_rangePolicy(t *testing.T) {
	e := newEnv(t)

	t.Run("stored entry supports Range", func(t *testing.T) {
		target := "/api/books/" + e.bookZipID + "/pages/1"
		if got := e.get(target).Header().Get("Accept-Ranges"); got != "bytes" {
			t.Errorf("Accept-Ranges = %q, want bytes", got)
		}
		w := e.get(target, func(r *http.Request) { r.Header.Set("Range", "bytes=0-9") })
		if w.Code != http.StatusPartialContent {
			t.Fatalf("Range = %d, want 206: %s", w.Code, w.Body.String())
		}
		if w.Body.Len() != 10 {
			t.Errorf("206 body = %d bytes, want 10", w.Body.Len())
		}
		want := fmt.Sprintf("bytes 0-9/%d", len(e.zipPayloads[0]))
		if got := w.Header().Get("Content-Range"); got != want {
			t.Errorf("Content-Range = %q, want %q", got, want)
		}
	})

	t.Run("dir page supports Range", func(t *testing.T) {
		w := e.get("/api/books/"+e.bookDirID+"/pages/1", func(r *http.Request) {
			r.Header.Set("Range", "bytes=0-4")
		})
		if w.Code != http.StatusPartialContent {
			t.Fatalf("Range = %d, want 206", w.Code)
		}
	})

	t.Run("deflated entry ignores Range", func(t *testing.T) {
		target := "/api/books/" + e.bookZipID + "/pages/2"
		if got := e.get(target).Header().Get("Accept-Ranges"); got != "" {
			t.Errorf("Accept-Ranges = %q, want it absent for a forward-only stream", got)
		}
		w := e.get(target, func(r *http.Request) { r.Header.Set("Range", "bytes=0-9") })
		if w.Code != http.StatusOK {
			t.Fatalf("Range on a deflated entry = %d, want 200", w.Code)
		}
		if !bytes.Equal(w.Body.Bytes(), e.zipPayloads[1]) {
			t.Error("ignoring Range must still deliver the whole entry")
		}
	})
}

// impl-plan §4 rule 1 — pages are 1-based, and arch §7.6 makes anything outside
// [1, page_count] a 404.
func TestPage_boundsAre1Based(t *testing.T) {
	e := newEnv(t)

	for _, tc := range []struct {
		n    string
		want int
	}{
		{"0", http.StatusNotFound},
		{"-1", http.StatusNotFound},
		{"4", http.StatusNotFound}, // the fixture book has 3 pages
		{"9000", http.StatusNotFound},
		{"one", http.StatusBadRequest},
		{"1.5", http.StatusBadRequest},
		{"1", http.StatusOK},
		{"3", http.StatusOK},
	} {
		t.Run("n="+tc.n, func(t *testing.T) {
			w := e.get("/api/books/" + e.bookZipID + "/pages/" + tc.n)
			if w.Code != tc.want {
				t.Errorf("status = %d, want %d: %s", w.Code, tc.want, w.Body.String())
			}
		})
	}
}

// FR-IDX-010 — a page of a book that failed to index is genuinely absent. The
// *book* still answers 200 with its error (that is rule 4); its pages do not.
func TestPage_brokenBookIs404(t *testing.T) {
	e := newEnv(t)
	errorBody(t, e.get("/api/books/"+e.bookBrokenID+"/pages/1"), http.StatusNotFound, CodeNotFound)
}

// arch §7.6 — `501 unsupported` for a PDF page in a build (or configuration)
// without PDF support. The fixture runs with `pdf.enabled: false`, which is the
// same code path a `-tags nopdf` binary takes.
func TestPage_pdfWithoutSupportIs501(t *testing.T) {
	e := newEnv(t)
	errorBody(t, e.get("/api/books/"+e.bookPDFID+"/pages/1"), http.StatusNotImplemented, CodeUnsupported)
}

// A damaged container that still has a readable entry list must serve its
// pages (ruling E-54).
//
// This is the seam nothing was looking at. `zipidx` returns the entries, the
// scanner writes the page rows, `OpenEntry` streams the bytes — every tier
// green — and the HTTP layer then refused all of them because the book carried
// `status='error'`. The refusal predates the salvage: the scanner has kept the
// pages of a partially readable directory since WP-04, and they have been
// 404ing ever since. It survived because the only broken fixture was a 12-byte
// stub with genuinely no pages, so "damaged" and "damaged and unreadable"
// looked identical to every test.
//
// FR-IDX-010 asks for an error status that does not abort the scan. It does not
// ask for this, and the volume keeps its badge, its error and its scan_log row
// either way.
func TestPage_damagedBookWithPages_servesThem(t *testing.T) {
	e := newEnv(t)

	book := decodeBody[BookDetail](t, e.get("/api/books/"+e.bookSalvagedID), http.StatusOK)
	if book.Status != "error" {
		t.Fatalf("status = %q, want the volume still flagged as error", book.Status)
	}
	if book.Error == nil || *book.Error == "" {
		t.Error("error is null; the badge still needs its reason")
	}
	if len(book.Pages) == 0 {
		t.Fatal("pages = [], want the entries the index holds for this book")
	}

	for n := 1; n <= len(book.Pages); n++ {
		w := e.get(fmt.Sprintf("/api/books/%s/pages/%d?v=%s", e.bookSalvagedID, n, book.CV))
		if w.Code != http.StatusOK {
			t.Fatalf("page %d = %d, want 200: %s", n, w.Code, w.Body.String())
		}
		if got := w.Body.Len(); got == 0 {
			t.Errorf("page %d served 0 bytes", n)
		}
		if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "image/") {
			t.Errorf("page %d content-type = %q, want an image", n, ct)
		}
	}

	// And the page past the end is still a 404 — serving damaged pages does not
	// mean serving pages that are not there.
	past := e.get(fmt.Sprintf("/api/books/%s/pages/%d", e.bookSalvagedID, len(book.Pages)+1))
	if past.Code != http.StatusNotFound {
		t.Errorf("page past the end = %d, want 404", past.Code)
	}
}

// The other half of the same rule: a book with no pages serves none, whatever
// its status says. This is what keeps E-54 from becoming "serve anything".
func TestPage_damagedBookWithNoPages_stillRefuses(t *testing.T) {
	e := newEnv(t)
	errorBody(t, e.get("/api/books/"+e.bookBrokenID+"/pages/1"), http.StatusNotFound, CodeNotFound)
}

// impl-plan §4 rule 4 — a book whose status is not "ok" answers 200 with a
// populated error. Its pages are whatever the index holds, which for this
// fixture — 12 bytes, too short for even one local header — is none.
func TestBookDetail_brokenBookIs200WithAnError(t *testing.T) {
	e := newEnv(t)
	book := decodeBody[BookDetail](t, e.get("/api/books/"+e.bookBrokenID), http.StatusOK)

	if book.Status != "error" {
		t.Errorf("status = %q, want error", book.Status)
	}
	if len(book.Pages) != 0 {
		t.Errorf("pages = %d, want an empty array", len(book.Pages))
	}
	if book.Error == nil || *book.Error == "" {
		t.Error("error is null; the UI needs the reason for its badge")
	}
	if !strings.Contains(e.get("/api/books/"+e.bookBrokenID).Body.String(), `"pages":[]`) {
		t.Error("pages marshalled as null rather than []")
	}
}

// FR-VWR-010 — prev/next drive the next-volume card, and are null at the ends.
func TestBookDetail_neighbours(t *testing.T) {
	e := newEnv(t)

	first := decodeBody[BookDetail](t, e.get("/api/books/"+e.bookZipID), http.StatusOK)
	if first.PrevBookID != nil {
		t.Errorf("prev_book_id = %v on the first volume, want null", *first.PrevBookID)
	}
	if first.NextBookID == nil || *first.NextBookID != e.bookDirID {
		t.Errorf("next_book_id = %v, want %q", first.NextBookID, e.bookDirID)
	}

	last := decodeBody[BookDetail](t, e.get("/api/books/"+e.bookDirID), http.StatusOK)
	if last.NextBookID != nil {
		t.Errorf("next_book_id = %v on the last volume, want null", *last.NextBookID)
	}
	if last.PrevBookID == nil || *last.PrevBookID != e.bookZipID {
		t.Errorf("prev_book_id = %v, want %q", last.PrevBookID, e.bookZipID)
	}
}

// D-15 / AC-008 — every PageInfo ships in one response, 1-based, with the
// dimensions filled in where they are known and null where they are not.
func TestBookDetail_shipsEveryPage(t *testing.T) {
	e := newEnv(t)

	book := decodeBody[BookDetail](t, e.get("/api/books/"+e.bookZipID), http.StatusOK)
	if len(book.Pages) != 3 {
		t.Fatalf("pages = %d, want 3", len(book.Pages))
	}
	for i, p := range book.Pages {
		if p.N != i+1 {
			t.Errorf("pages[%d].n = %d, want %d — page numbers are 1-based", i, p.N, i+1)
		}
		if p.W != nil || p.H != nil {
			t.Errorf("pages[%d] has dimensions the fixture never filled in", i)
		}
	}

	dir := decodeBody[BookDetail](t, e.get("/api/books/"+e.bookDirID), http.StatusOK)
	if dir.Pages[0].W == nil || *dir.Pages[0].W != 120 {
		t.Errorf("a page with stored dimensions reported w = %v", dir.Pages[0].W)
	}
	if dir.Pages[1].W != nil {
		t.Error("a page with no stored dimensions must report null, not 0")
	}
}

// impl-plan §4 rule 3 — 202 is a normal answer for a thumbnail. It means
// "queued", and `Retry-After` is the whole protocol.
func TestThumb_queuedThenReady(t *testing.T) {
	e := newEnv(t)
	target := "/api/books/" + e.bookZipID + "/thumbs/1?w=120&v=" + cvZip

	first := e.get(target)
	if first.Code != http.StatusAccepted {
		t.Fatalf("first request = %d, want 202: %s", first.Code, first.Body.String())
	}
	if got := first.Header().Get("Retry-After"); got != "1" {
		t.Errorf("Retry-After = %q, want 1", got)
	}
	if first.Body.Len() != 0 {
		t.Errorf("the 202 carried a %d-byte body", first.Body.Len())
	}

	if err := e.thumbs.Drain(t.Context()); err != nil {
		t.Fatalf("draining the thumbnail queue: %v", err)
	}

	ready := e.get(target)
	if ready.Code != http.StatusOK {
		t.Fatalf("after generation = %d, want 200: %s", ready.Code, ready.Body.String())
	}
	if got := ready.Header().Get("Content-Type"); got != "image/jpeg" {
		t.Errorf("Content-Type = %q, want image/jpeg (CON-003: thumbnails are JPEG only)", got)
	}
	etag := ready.Header().Get("ETag")
	if !strings.HasPrefix(etag, `"t1-`) {
		t.Errorf("ETag = %q, want the t1- form", etag)
	}
	if got := ready.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Errorf("Cache-Control = %q, want immutable for a matching ?v=", got)
	}

	nm := e.get(target, func(r *http.Request) { r.Header.Set("If-None-Match", etag) })
	if nm.Code != http.StatusNotModified {
		t.Errorf("If-None-Match on a thumbnail = %d, want 304", nm.Code)
	}
}

// arch §7.6 — a thumbnail outside the book's page range is 404, and the width
// parameter is validated.
func TestThumb_boundsAndWidth(t *testing.T) {
	e := newEnv(t)
	base := "/api/books/" + e.bookZipID + "/thumbs/"

	errorBody(t, e.get(base+"9000"), http.StatusNotFound, CodeNotFound)
	errorBody(t, e.get(base+"0"), http.StatusNotFound, CodeNotFound)

	for _, bad := range []string{"?w=0", "?w=-5", "?w=wide"} {
		t.Run("w"+bad, func(t *testing.T) {
			body := errorBody(t, e.get(base+"1"+bad), http.StatusBadRequest, CodeBadRequest)
			if body.Detail["param"] != "w" {
				t.Errorf("detail.param = %v, want \"w\"", body.Detail["param"])
			}
		})
	}
}

// FR-THM-003 / FR-LIB-008 — the cover endpoint's three normal outcomes.
func TestCover(t *testing.T) {
	e := newEnv(t)

	t.Run("a loose cover file generates", func(t *testing.T) {
		target := "/api/series/" + e.seriesFolderID + "/cover?w=120"
		if w := e.get(target); w.Code != http.StatusAccepted {
			t.Fatalf("first request = %d, want 202", w.Code)
		}
		if err := e.thumbs.Drain(t.Context()); err != nil {
			t.Fatalf("draining: %v", err)
		}
		w := e.get(target)
		if w.Code != http.StatusOK {
			t.Fatalf("after generation = %d, want 200: %s", w.Code, w.Body.String())
		}
		if got := w.Header().Get("Content-Type"); got != "image/jpeg" {
			t.Errorf("Content-Type = %q", got)
		}
	})

	t.Run("a page cover carries the book's cv", func(t *testing.T) {
		detail := decodeBody[SeriesDetail](t, e.get("/api/series/"+e.seriesCloverID), http.StatusOK)
		if !detail.HasCover {
			t.Fatal("has_cover = false for a series with cover_kind=page")
		}
		if detail.CoverCV == nil || *detail.CoverCV != cvClover {
			t.Fatalf("cover_cv = %v, want %q", detail.CoverCV, cvClover)
		}
		// A stale ?v= is 409 here exactly as it is on a page.
		w := e.get("/api/series/" + e.seriesCloverID + "/cover?v=stale")
		body := errorBody(t, w, http.StatusConflict, CodeStaleVersion)
		if body.Detail["cv"] != cvClover {
			t.Errorf("detail.cv = %v, want %q", body.Detail["cv"], cvClover)
		}
	})

	t.Run("no cover at all is 404", func(t *testing.T) {
		// FR-LIB-008: the client renders its text placeholder, so this must be
		// distinguishable from "queued".
		errorBody(t, e.get("/api/series/"+e.seriesBrokenID+"/cover"), http.StatusNotFound, CodeNotFound)
	})
}

// arch §5.3 — the default width is `thumbnails.widths[0]`, which amendments
// A-1/A-6 make 120.
func TestThumbWidth_defaultsToTheFirstConfiguredWidth(t *testing.T) {
	e := newEnv(t)
	got, err := e.srv.thumbWidth(requestFor("/api/books/x/thumbs/1"))
	if err != nil {
		t.Fatalf("thumbWidth: %v", err)
	}
	if got != 120 {
		t.Errorf("default w = %d, want 120 (widths[0] under A-1/A-6)", got)
	}
}

// arch §5.2 / §7.6 — a container whose (size, mtime) no longer match the index
// is being read at offsets that may mean nothing. A request carrying `?v=` has
// metadata it can refresh, so it is sent back for a new cv rather than handed
// bytes from the wrong place.
func TestPage_changedContainerIsStaleWhenAVersionWasSupplied(t *testing.T) {
	e := newEnv(t)
	target := "/api/books/" + e.bookZipID + "/pages/1"

	// Grow the archive on disk before anything opens it — the handle pool
	// caches by path, and staleness is decided when a handle is first opened.
	// This is what "somebody re-downloaded a better scan" looks like from the
	// index's point of view. (TestPage_cacheControlMatrix already proves the
	// untouched fixture answers 200 for the same URL, so this cannot pass for
	// the wrong reason.)
	growFixtureArchive(t, e)

	w := e.get(target + "?v=" + cvZip)
	body := errorBody(t, w, http.StatusConflict, CodeStaleVersion)
	if body.Detail["cv"] != cvZip {
		t.Errorf("detail.cv = %v, want the current %q", body.Detail["cv"], cvZip)
	}

	// arch §5.2: the pool rule is "still serve but tag the response so the API
	// can answer 409 stale (§7.6) **and enqueue a rescan of that book**". The
	// enqueue is not decoration — `detail.cv` above is the index's cv, i.e. the
	// one the client just sent, so without re-indexing the book a client can
	// refetch its metadata for ever and get the same answer every time.
	if e.scan.starts != 1 {
		t.Fatalf("the stale container queued %d scans, want exactly 1", e.scan.starts)
	}
	if got := e.scan.lastReq.Roots; len(got) != 1 || got[0] != rootName {
		t.Errorf("rescan roots = %v, want [%s]", got, rootName)
	}
	if got := e.scan.lastReq.Series; len(got) != 1 ||
		got[0].Root != rootName || got[0].RelPath != seriesFolderPath {
		t.Errorf("rescan series = %+v, want the book's own series %q", got, seriesFolderPath)
	}
	if e.scan.lastReq.Full {
		t.Error("the rescan is full; a targeted run must not sweep the whole root")
	}
}

// A 1,071-page read of a changed volume detects the same staleness 1,071 times.
// One rescan is the answer; 1,071 is a denial of service against the scanner.
func TestPage_staleRescanIsCoalescedPerBook(t *testing.T) {
	e := newEnv(t)
	growFixtureArchive(t, e)

	for n := 1; n <= 3; n++ {
		w := e.get(fmt.Sprintf("/api/books/%s/pages/%d?v=%s", e.bookZipID, n, cvZip))
		if w.Code != http.StatusConflict {
			t.Fatalf("page %d = %d, want 409", n, w.Code)
		}
	}
	// The versionless request still serves bytes (arch §5.2 says "still serve")
	// and must not queue a second scan either.
	if w := e.get("/api/books/" + e.bookZipID + "/pages/1"); w.Code != http.StatusOK {
		t.Fatalf("a versionless request on a changed container = %d, want 200", w.Code)
	}
	if e.scan.starts != 1 {
		t.Errorf("four requests queued %d scans, want 1", e.scan.starts)
	}

	// Past the cooldown the book may be queued again, so a scan that was
	// refused the first time is retried rather than lost.
	e.srv.rescans = newRescanCoalescer(0)
	if w := e.get("/api/books/" + e.bookZipID + "/pages/1?v=" + cvZip); w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", w.Code)
	}
	if e.scan.starts != 2 {
		t.Errorf("after the cooldown the scan count is %d, want 2", e.scan.starts)
	}
}

// The whole point of the rescan: once it has landed, the volume is readable
// again. This is the end of the loop the 409 opens.
func TestPage_recoversOnceTheRescanHasReindexedTheBook(t *testing.T) {
	e := newEnv(t)
	growFixtureArchive(t, e)

	target := "/api/books/" + e.bookZipID + "/pages/1"
	if w := e.get(target + "?v=" + cvZip); w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 before the rescan", w.Code)
	}

	// Stand in for the queued scan: re-read the container's (size, mtime) and
	// write a new content version, which is exactly what the scanner does.
	const cvAfter = "9999888877776666"
	reindexFixtureArchive(t, e, cvAfter)

	// The client refetches the book detail, sees the new cv and asks again.
	detail := decodeBody[BookDetail](t, e.get("/api/books/"+e.bookZipID), http.StatusOK)
	if detail.CV != cvAfter {
		t.Fatalf("book detail cv = %q, want the reindexed %q", detail.CV, cvAfter)
	}
	w := e.get(target + "?v=" + detail.CV)
	if w.Code != http.StatusOK {
		t.Fatalf("after the rescan the page = %d, want 200: %s", w.Code, w.Body.String())
	}
	if !bytes.Equal(w.Body.Bytes(), e.zipPayloads[0]) {
		t.Error("the recovered page is not the archive member's bytes")
	}
	if got := w.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Errorf("Cache-Control = %q, want immutable once the version matches again", got)
	}
}

// A scanner that is already busy — or missing, or shutting down — must not turn
// a page request into a 500. The 409 is the answer either way; the rescan is
// best-effort by construction (arch §5.2).
func TestPage_staleRescanFailureDoesNotAffectTheResponse(t *testing.T) {
	e := newEnv(t)
	growFixtureArchive(t, e)
	e.scan.startErr = scanner.ErrBusy

	body := errorBody(t, e.get("/api/books/"+e.bookZipID+"/pages/1?v="+cvZip),
		http.StatusConflict, CodeStaleVersion)
	if body.Detail["cv"] != cvZip {
		t.Errorf("detail.cv = %v, want %q", body.Detail["cv"], cvZip)
	}
	if e.scan.starts != 1 {
		t.Errorf("the scanner was asked %d times, want 1", e.scan.starts)
	}

	// A server built without a scanner at all still serves.
	e.srv.scan = nil
	e.srv.rescans = newRescanCoalescer(staleRescanCooldown)
	if w := e.get("/api/books/" + e.bookZipID + "/pages/1"); w.Code != http.StatusOK {
		t.Errorf("with no scanner the page = %d, want 200", w.Code)
	}
}

// The coalescer's own contract, away from HTTP.
func TestRescanCoalescer_claimsOncePerCooldown(t *testing.T) {
	c := newRescanCoalescer(time.Minute)
	t0 := time.Date(2026, 7, 28, 9, 0, 0, 0, time.UTC)

	if !c.claim("book-a", t0) {
		t.Fatal("the first claim was refused")
	}
	if c.claim("book-a", t0.Add(59*time.Second)) {
		t.Error("a second claim inside the cooldown was granted")
	}
	// A different book is independent — one changed volume must not silence
	// another.
	if !c.claim("book-b", t0.Add(time.Second)) {
		t.Error("a different book was refused while the first was cooling down")
	}
	if !c.claim("book-a", t0.Add(time.Minute)) {
		t.Error("a claim after the cooldown was refused")
	}
	// Expired entries are dropped, so a long uptime cannot accumulate one entry
	// per book ever seen stale.
	c.claim("book-c", t0.Add(10*time.Minute))
	c.mu.Lock()
	size := len(c.last)
	c.mu.Unlock()
	if size != 1 {
		t.Errorf("the coalescer holds %d entries, want the 1 that is still live", size)
	}
}

// growFixtureArchive appends to the fixture archive so its (size, mtime) no
// longer match the index — what "somebody re-downloaded a better scan" looks
// like from the index's point of view. The appended bytes sit after the end-of-
// central-directory record, so every stored offset still resolves.
func growFixtureArchive(t *testing.T, e *env) {
	t.Helper()
	path := filepath.Join(e.media, filepath.FromSlash(bookZipPath))
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("opening the fixture archive: %v", err)
	}
	if _, err := f.Write(bytes.Repeat([]byte{0}, 64)); err != nil {
		t.Fatalf("growing the fixture archive: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("closing the fixture archive: %v", err)
	}
}

// reindexFixtureArchive replays what a scanner run would write: the container's
// current (size, mtime) and a fresh content version.
func reindexFixtureArchive(t *testing.T, e *env, cv string) {
	t.Helper()
	ctx := t.Context()
	book, err := e.idx.GetBook(ctx, e.bookZipID)
	if err != nil {
		t.Fatalf("reading the book row: %v", err)
	}
	fi, err := os.Stat(filepath.Join(e.media, filepath.FromSlash(bookZipPath)))
	if err != nil {
		t.Fatalf("stat of the fixture archive: %v", err)
	}
	row := book.Book
	row.FileSize = fi.Size()
	row.FileMtime = fi.ModTime().Unix()
	row.ContentVersion = cv

	w := e.idx.Writer(index.WriterOptions{})
	defer func() { _ = w.Close() }()
	if err := w.UpsertBook(ctx, row); err != nil {
		t.Fatalf("reindexing the book: %v", err)
	}
	if err := w.Flush(ctx); err != nil {
		t.Fatalf("flushing the reindex: %v", err)
	}
}
