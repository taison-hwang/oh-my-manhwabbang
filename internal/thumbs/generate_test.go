package thumbs

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"shelf/internal/index"
	"shelf/internal/testutil"
)

// writeLoose drops an image beside the series and returns the request that
// thumbnails it. It is the shortest route from "these bytes" to "run the whole
// pipeline over them", and it is a real code path — arch §4.10 step 1 covers
// are exactly this.
func (h *harness) writeLoose(name string, data []byte) Request {
	h.t.Helper()
	rel := "series/" + name
	writeFile(h.t, filepath.Join(h.rootPath, filepath.FromSlash(rel)), data)
	return Request{
		ID:             h.seriesID,
		Width:          240,
		ContentVersion: contentVersion(h.t, "file", filepath.Join(h.rootPath, filepath.FromSlash(rel))),
		RootName:       testRoot,
		RelPath:        rel,
	}
}

// ---------------------------------------------------------------------------
// FR-IDX-011 — the seven formats, and the documented degradations
// ---------------------------------------------------------------------------

// FR-IDX-011 is 필수 and lists seven extensions even though the reference
// collection is 98.7 % JPEG and contains zero WebP and zero AVIF (data-survey
// §4). All of them are implemented; this is the proof for the six that need no
// wasm runtime, plus TIFF, which arch §5.5 adds.
func TestGenerate_everyStillFormat_producesAJPEGThumbnail(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)

	for _, tc := range []struct {
		name string
		file string
		data []byte
	}{
		{"jpeg", "a.jpg", testutil.TinyJPEG(t, 40, 60)},
		{"png", "b.png", testutil.TinyPNG(t, 40, 60)},
		{"gif", "c.gif", testutil.TinyGIF(t, 40, 60)},
		{"bmp", "d.bmp", testutil.TinyBMP(t, 40, 60)},
		{"tiff", "e.tiff", testutil.TinyTIFF(t, 40, 60)},
		{"webp", "f.webp", testutil.TinyWebP(t)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := h.writeLoose(tc.file, tc.data)
			res, err := h.svc.Generate(t.Context(), req)
			if err != nil {
				t.Fatalf("Generate(%s): %v", tc.name, err)
			}
			// Every thumbnail is a JPEG whatever went in (CON-003).
			decodeJPEG(t, res.Path)
		})
	}
}

// The sniffer must agree with the decoders it dispatches to. It reads magic
// bytes, never the file name: a PNG named `.jpg` is common in the wild and the
// bytes are what matter.
func TestSniff_recognisesEveryFormatFromItsMagicBytes(t *testing.T) {
	t.Parallel()
	for want, data := range map[string][]byte{
		"jpeg": testutil.TinyJPEG(t, 4, 4),
		"png":  testutil.TinyPNG(t, 4, 4),
		"gif":  testutil.TinyGIF(t, 4, 4),
		"bmp":  testutil.TinyBMP(t, 4, 4),
		"tiff": testutil.TinyTIFF(t, 4, 4),
		"webp": testutil.TinyWebP(t),
		"avif": testutil.TinyAVIF(t),
	} {
		if got := sniff(data); got != want {
			t.Errorf("sniff(%s) = %q", want, got)
		}
	}
	for _, junk := range [][]byte{nil, {}, []byte("not a picture at all"), []byte("PK\x03\x04")} {
		if got := sniff(junk); got != "" {
			t.Errorf("sniff(%q) = %q, want \"\"", junk, got)
		}
	}
	// The `ftyp` box's minor version sits between the major brand and the
	// compatible-brand list and must not be read as a brand.
	fake := append([]byte{0, 0, 0, 20, 'f', 't', 'y', 'p', 'm', 'i', 'f', '1'}, []byte("avif")...)
	fake = append(fake, "mif1"...)
	if got := sniff(fake); got != "" {
		t.Errorf("sniff read the ftyp minor version as a brand: got %q", got)
	}
}

// arch §5.5: animated WebP is the one documented "serve the original, refuse
// the thumbnail" case. The reason travels to the user as `detail.reason`, so it
// has to be the true one — hence the structural ANIM/ANMF check rather than
// "the decoder said no".
func TestGenerate_animatedWebP_isUndecodableWithTheAnimatedReason(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	req := h.writeLoose("anim.webp", testutil.TinyAnimatedWebP(t))

	_, err := h.svc.Generate(t.Context(), req)
	if !errors.Is(err, ErrUndecodable) {
		t.Fatalf("err = %v, want ErrUndecodable", err)
	}
	var ue *UndecodableError
	if !errors.As(err, &ue) {
		t.Fatalf("err = %v, want an *UndecodableError carrying a reason", err)
	}
	if ue.Reason != ReasonAnimatedWebP {
		t.Fatalf("reason = %q, want %q", ue.Reason, ReasonAnimatedWebP)
	}
	// A still WebP must NOT be caught by the same check.
	still := h.writeLoose("still.webp", testutil.TinyWebP(t))
	if _, err := h.svc.Generate(t.Context(), still); err != nil {
		t.Fatalf("a still WebP was rejected: %v", err)
	}
}

func TestIsAnimatedWebP_distinguishesTheTwoRIFFShapes(t *testing.T) {
	t.Parallel()
	if !isAnimatedWebP(testutil.TinyAnimatedWebP(t)) {
		t.Error("the animated fixture was not detected")
	}
	if isAnimatedWebP(testutil.TinyWebP(t)) {
		t.Error("a still WebP was reported as animated")
	}
	for _, junk := range [][]byte{nil, []byte("RIFF"), testutil.TinyJPEG(t, 4, 4)} {
		if isAnimatedWebP(junk) {
			t.Errorf("isAnimatedWebP(%q) = true", junk[:min(len(junk), 8)])
		}
	}
}

// A page that no decoder recognises is a per-page failure, logged and turned
// into a placeholder — never a crash and never a failed scan.
func TestGenerate_garbageBytes_areAPerPageFailure(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)

	req := h.writeLoose("junk.jpg", []byte("this is not an image, it is a text file with a lie for a name"))
	_, err := h.svc.Generate(t.Context(), req)
	var ue *UndecodableError
	if !errors.As(err, &ue) {
		t.Fatalf("err = %v, want *UndecodableError", err)
	}
	if ue.Reason != ReasonUnknownFormat {
		t.Fatalf("reason = %q, want %q", ue.Reason, ReasonUnknownFormat)
	}

	// Truncated but correctly-magicked bytes reach a decoder and fail there.
	good := testutil.TinyJPEG(t, 40, 60)
	req = h.writeLoose("truncated.jpg", good[:len(good)/3])
	_, err = h.svc.Generate(t.Context(), req)
	if !errors.As(err, &ue) {
		t.Fatalf("err = %v, want *UndecodableError", err)
	}
	if ue.Reason != ReasonDecodeFailed {
		t.Fatalf("reason = %q, want %q", ue.Reason, ReasonDecodeFailed)
	}

	// The service is still perfectly usable afterwards.
	if _, err := h.svc.Generate(t.Context(), h.pageReq(1, 120)); err != nil {
		t.Fatalf("a normal page failed after two undecodable ones: %v", err)
	}
}

// `thumbnails.max_source_bytes` bounds the memory one decode can cost. A page
// over the cap is a named failure, not an OOM.
func TestGenerate_sourceOverTheCap_isRefusedByName(t *testing.T) {
	t.Parallel()
	h := newHarness(t, func(o *Options) { o.MaxSourceBytes = 128 })

	req := h.writeLoose("big.jpg", testutil.TinyJPEG(t, 200, 300))
	_, err := h.svc.Generate(t.Context(), req)
	var ue *UndecodableError
	if !errors.As(err, &ue) || ue.Reason != ReasonSourceTooLarge {
		t.Fatalf("err = %v, want reason %q", err, ReasonSourceTooLarge)
	}
}

// `thumbnails.max_source_bytes` measures the wrong quantity on its own: it
// bounds COMPRESSED bytes, and compression is exactly what a bomb exploits.
// Options.MaxSourcePixels bounds the other one — the area the header DECLARES,
// which is what a decoder allocates from.
//
// The boundary is asserted with genuine pictures so that "allowed" really does
// mean a thumbnail came out the other end.
func TestGenerate_sourcePixelCeiling_isExactAtTheBoundary(t *testing.T) {
	t.Parallel()
	// 400×600 as the ceiling makes both sides of the boundary expressible
	// without materialising a 40 Mpx fixture.
	const ceiling = 400 * 600
	h := newHarness(t, func(o *Options) { o.MaxSourcePixels = ceiling })

	for _, tc := range []struct {
		name   string
		file   string
		data   []byte
		refuse bool
	}{
		{"jpeg_exactly_at_the_ceiling", "at.jpg", testutil.TinyJPEG(t, 400, 600), false},
		{"jpeg_one_pixel_over", "over.jpg", testutil.TinyJPEG(t, 401, 600), true},
		{"png_one_row_over", "over.png", testutil.TinyPNG(t, 400, 601), true},
		{"png_well_under", "under.png", testutil.TinyPNG(t, 40, 60), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := h.svc.Stats().Decodes
			_, err := h.svc.Generate(t.Context(), h.writeLoose(tc.file, tc.data))
			decoded := h.svc.Stats().Decodes - before

			if !tc.refuse {
				if err != nil {
					t.Fatalf("Generate: %v", err)
				}
				if decoded != 1 {
					t.Fatalf("decodes = %d, want 1: the picture is inside the ceiling", decoded)
				}
				return
			}
			var ue *UndecodableError
			if !errors.As(err, &ue) || ue.Reason != ReasonSourceTooLargePixels {
				t.Fatalf("err = %v, want reason %q", err, ReasonSourceTooLargePixels)
			}
			// The whole point: refused from the header, so image.Decode — the
			// call that would allocate the pixel buffer — is never reached.
			if decoded != 0 {
				t.Fatalf("decodes = %d, want 0: the refusal must happen before image.Decode", decoded)
			}
		})
	}
}

// The bomb itself: a few hundred bytes on disk, half a gigabyte in the decoder.
// Both fixtures sit far inside the default 64 MiB `max_source_bytes`, which is
// precisely why that knob cannot be the bound.
//
// Not parallel: it measures process-wide TotalAlloc, and Go pauses the parallel
// tests of this package while a sequential one runs.
func TestGenerate_decompressionBomb_costsNoAllocation(t *testing.T) {
	h := newHarness(t, nil) // default MaxSourcePixels

	for _, tc := range []struct {
		name string
		file string
		data []byte
		want int64 // pixels the header claims
	}{
		{"jpeg_sof0_lies", "bomb.jpg", jpegDeclaring(t, 24000, 24000), 24000 * 24000},
		{"png_ihdr_lies", "bomb.png", pngDeclaring(t, 30000, 30000), 30000 * 30000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.data) > 4096 {
				t.Fatalf("fixture is %d bytes; a bomb has to be small to be a bomb", len(tc.data))
			}
			req := h.writeLoose(tc.file, tc.data)
			before := h.svc.Stats().Decodes

			var m0, m1 runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&m0)
			_, err := h.svc.Generate(t.Context(), req)
			runtime.ReadMemStats(&m1)
			grew := int64(m1.TotalAlloc - m0.TotalAlloc)

			// Asserted first so that a regression reports the number that
			// matters. The unguarded path allocates a buffer per declared pixel —
			// measured at 549 MiB for the JPEG. Anything in that neighbourhood
			// means the header was believed rather than checked.
			const budget = 128 << 20
			if grew > budget {
				t.Errorf("TotalAlloc grew by %d bytes refusing a %d-byte source declaring %d pixels",
					grew, len(tc.data), tc.want)
			}
			var ue *UndecodableError
			if !errors.As(err, &ue) || ue.Reason != ReasonSourceTooLargePixels {
				t.Fatalf("err = %v, want reason %q", err, ReasonSourceTooLargePixels)
			}
			if got := h.svc.Stats().Decodes - before; got != 0 {
				t.Fatalf("decodes = %d, want 0: %d declared pixels reached image.Decode", got, tc.want)
			}
		})
	}
}

// jpegDeclaring returns a real, tiny baseline JPEG whose SOF0 header LIES about
// the picture's size. That is the shape of a decompression bomb: the decoder
// sizes its buffer from the header, long before it discovers there is no scan
// data to fill it.
func jpegDeclaring(t testing.TB, w, h int) []byte {
	t.Helper()
	data := append([]byte(nil), testutil.TinyJPEG(t, 8, 8)...)
	// Walk the marker segments from just after SOI. Every marker before SOS
	// carries a 2-byte length, so this needs no JPEG knowledge beyond SOF0's
	// layout: length(2) precision(1) height(2) width(2).
	for i := 2; i+9 < len(data); {
		if data[i] != 0xFF {
			t.Fatalf("jpeg fixture is not marker-aligned at offset %d", i)
		}
		if data[i+1] == 0xC0 {
			binary.BigEndian.PutUint16(data[i+5:i+7], uint16(h))
			binary.BigEndian.PutUint16(data[i+7:i+9], uint16(w))
			return data
		}
		i += 2 + int(binary.BigEndian.Uint16(data[i+2:i+4]))
	}
	t.Fatal("no SOF0 marker in the jpeg fixture")
	return nil
}

// pngDeclaring is the same trick in PNG's IHDR. The chunk CRC has to be
// recomputed or image/png rejects the file before it ever reads the size, which
// would prove nothing.
func pngDeclaring(t testing.TB, w, h int) []byte {
	t.Helper()
	data := append([]byte(nil), testutil.TinyPNG(t, 8, 8)...)
	const sig = 8 // the 8-byte PNG signature, then length(4) type(4) data...
	if len(data) < sig+12 || string(data[sig+4:sig+8]) != "IHDR" {
		t.Fatal("png fixture does not begin with an IHDR chunk")
	}
	n := int(binary.BigEndian.Uint32(data[sig : sig+4]))
	binary.BigEndian.PutUint32(data[sig+8:sig+12], uint32(w))
	binary.BigEndian.PutUint32(data[sig+12:sig+16], uint32(h))
	binary.BigEndian.PutUint32(data[sig+8+n:sig+12+n], crc32.ChecksumIEEE(data[sig+4:sig+8+n]))
	return data
}

// arch §5.5: "a negative result is cached in memory for 10 minutes so we do not
// retry the decode on every scroll". Without it a thumbnail strip containing
// one animated WebP would re-read and re-fail it on every frame.
func TestGenerate_undecodableVerdict_isMemoisedForTheTTL(t *testing.T) {
	t.Parallel()
	clk := &fakeClock{}
	// Counting attempts rather than Stats().Decodes is deliberate: an animated
	// WebP is refused BEFORE any decoder runs, so the decode counter would stay
	// at zero and prove nothing. The hook fires on every attempt that got as
	// far as looking at the bytes.
	var attempts atomic.Int64
	h := newHarness(t, func(o *Options) {
		o.Now = clk.Now
		o.NegativeTTL = 10 * time.Minute
		o.hookDecode = func() { attempts.Add(1) }
	})
	req := h.writeLoose("anim.webp", testutil.TinyAnimatedWebP(t))

	if _, err := h.svc.Generate(t.Context(), req); !errors.Is(err, ErrUndecodable) {
		t.Fatalf("first attempt: %v", err)
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("attempts after the first request = %d, want 1", got)
	}

	for range 5 {
		if _, err := h.svc.Generate(t.Context(), req); !errors.Is(err, ErrUndecodable) {
			t.Fatalf("memoised attempt: %v", err)
		}
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("attempts = %d after six requests; the negative result was not memoised", got)
	}
	if got := h.svc.Stats().Negative; got != 5 {
		t.Fatalf("negative-cache hits = %d, want 5", got)
	}
	// Get must answer from the memo too, not enqueue a doomed job.
	if _, err := h.svc.Get(t.Context(), req); !errors.Is(err, ErrUndecodable) {
		t.Fatalf("Get on a memoised failure = %v, want ErrUndecodable", err)
	}
	if got := h.svc.Stats().Queued; got != 0 {
		t.Fatalf("%d jobs were queued for a source known to be undecodable", got)
	}

	// Past the TTL the verdict is re-derived, so a file that has since been
	// replaced with a decodable one recovers on its own.
	clk.advance(11 * time.Minute)
	if _, err := h.svc.Generate(t.Context(), req); !errors.Is(err, ErrUndecodable) {
		t.Fatalf("after the TTL: %v", err)
	}
	if got := attempts.Load(); got != 2 {
		t.Fatalf("attempts = %d after the TTL expired, want 2", got)
	}
}

// ---------------------------------------------------------------------------
// AVIF — implemented for FR-IDX-011, never on a critical path (D-25)
// ---------------------------------------------------------------------------

func TestGenerate_avifWithDecodingDisabled_saysSo(t *testing.T) {
	t.Parallel()
	h := newHarness(t, func(o *Options) { o.AVIFEnabled = false })

	req := h.writeLoose("cover.avif", testutil.TinyAVIF(t))
	_, err := h.svc.Generate(t.Context(), req)
	var ue *UndecodableError
	if !errors.As(err, &ue) || ue.Reason != ReasonAVIFDisabled {
		t.Fatalf("err = %v, want reason %q", err, ReasonAVIFDisabled)
	}
}

// D-25: AVIF holds a ONE-permit semaphore whatever `thumbnails.workers` says,
// because one decode is ~1.1 s and ~170 MiB of wasm heap. Testing the gate
// directly rather than through two real decodes keeps the assertion exact and
// the test instant.
func TestAcquireAVIF_serialisesRegardlessOfWorkerCount(t *testing.T) {
	t.Parallel()
	h := newHarness(t, func(o *Options) { o.Workers = 8 })

	release, err := h.svc.acquireAVIF(t.Context())
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	// A second acquirer must wait, and must honour its context while waiting.
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if _, err := h.svc.acquireAVIF(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second acquire while the permit is held = %v, want a deadline", err)
	}

	release()
	release2, err := h.svc.acquireAVIF(t.Context())
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	release2()
}

// The slow path of D-25: a real AVIF decode through wazero. It costs ~1 s of
// lazy runtime initialisation, which is exactly why it is serialised behind a
// one-permit semaphore and why zero AVIF files exist in the collection.
func TestGenerate_avif_decodesThroughTheSerialisedSlowPath(t *testing.T) {
	if testing.Short() {
		t.Skip("AVIF decoding costs a ~1 s wazero initialisation")
	}
	if !avifEnabled {
		t.Skip("built with -tags noavif")
	}
	t.Parallel()
	h := newHarness(t, nil)

	req := h.writeLoose("cover.avif", testutil.TinyAVIF(t))
	res, err := h.svc.Generate(t.Context(), req)
	if err != nil {
		t.Fatalf("Generate(avif): %v", err)
	}
	decodeJPEG(t, res.Path)
}

// ---------------------------------------------------------------------------
// FR-VWR-004 — page dimensions
// ---------------------------------------------------------------------------

// arch §5.8: dimensions are filled "for free" whenever a thumbnail is made,
// because the picture has already been decoded. The landscape page is the one
// that matters — FR-VWR-004 renders a page with w > h single even in spread
// mode, and this row is where that decision comes from.
func TestGenerate_recordsPageDimensionsForFree(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)

	if _, err := h.svc.Generate(t.Context(), h.pageReq(2, 240)); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	h.drain()

	page, err := h.idx.GetPage(t.Context(), h.zipBook, 2)
	if err != nil {
		t.Fatalf("GetPage: %v", err)
	}
	if page.Width == nil || page.Height == nil {
		t.Fatal("page 2 has no dimensions after its thumbnail was generated")
	}
	if *page.Width != 800 || *page.Height != 400 {
		t.Fatalf("page 2 = %dx%d, want 800x400 (the SOURCE size, not the thumbnail's)",
			*page.Width, *page.Height)
	}
	if *page.Width <= *page.Height {
		t.Fatal("the landscape fixture is not landscape; FR-VWR-004 would be untested")
	}

	book, err := h.idx.GetBook(t.Context(), h.zipBook)
	if err != nil {
		t.Fatalf("GetBook: %v", err)
	}
	if book.DimsState != "partial" {
		t.Fatalf("dims_state = %q after one page of three, want %q", book.DimsState, "partial")
	}
}

// The background pass of arch §5.8: `none → partial → done`, reading only
// headers.
func TestEnsureDims_measuresEveryPage_readingOnlyHeaders(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)

	h.svc.EnsureDims(h.zipBook)
	h.drain()

	for i, want := range pageGeometry {
		page, err := h.idx.GetPage(t.Context(), h.zipBook, i+1)
		if err != nil {
			t.Fatalf("GetPage %d: %v", i+1, err)
		}
		if page.Width == nil || page.Height == nil {
			t.Fatalf("page %d still has no dimensions", i+1)
		}
		if *page.Width != want[0] || *page.Height != want[1] {
			t.Fatalf("page %d = %dx%d, want %dx%d", i+1, *page.Width, *page.Height, want[0], want[1])
		}
	}

	book, err := h.idx.GetBook(t.Context(), h.zipBook)
	if err != nil {
		t.Fatalf("GetBook: %v", err)
	}
	if book.DimsState != "done" {
		t.Fatalf("dims_state = %q after the full pass, want %q", book.DimsState, "done")
	}

	// impl-plan WP-07 acceptance 7: the pass must NOT read whole entries.
	st := h.svc.Stats()
	if st.DimsPages != int64(len(pageGeometry)) {
		t.Fatalf("probed %d pages, want %d", st.DimsPages, len(pageGeometry))
	}
	if perPage := st.DimsBytes / st.DimsPages; perPage >= dimsProbeCap {
		t.Fatalf("the dimension pass read %d bytes per page, want < %d", perPage, dimsProbeCap)
	}
}

// A book whose pages are already measured costs nothing.
func TestEnsureDims_alreadyMeasured_readsNothing(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)

	dims := make([]index.PageDims, 0, len(pageGeometry))
	for i, g := range pageGeometry {
		dims = append(dims, index.PageDims{PageNo: i + 1, Width: g[0], Height: g[1]})
	}
	if err := h.idx.UpdateDims(t.Context(), h.zipBook, dims); err != nil {
		t.Fatalf("UpdateDims: %v", err)
	}

	h.svc.EnsureDims(h.zipBook)
	h.drain()
	if got := h.svc.Stats().DimsPages; got != 0 {
		t.Fatalf("the pass probed %d pages of an already-measured book", got)
	}
}

// A book with an undecodable page must still get dimensions for the rest of it.
func TestEnsureDims_oneBadPage_doesNotStopTheOthers(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)

	// Break page 2 of the directory book by replacing it with junk.
	writeFile(t, filepath.Join(h.rootPath, dirRelPath, "002.jpg"), []byte("not an image"))

	h.svc.EnsureDims(h.dirBook)
	h.drain()

	for _, no := range []int{1, 3} {
		page, err := h.idx.GetPage(t.Context(), h.dirBook, no)
		if err != nil {
			t.Fatalf("GetPage %d: %v", no, err)
		}
		if page.Width == nil {
			t.Fatalf("page %d was skipped because a different page was broken", no)
		}
	}
	broken, err := h.idx.GetPage(t.Context(), h.dirBook, 2)
	if err != nil {
		t.Fatalf("GetPage 2: %v", err)
	}
	if broken.Width != nil {
		t.Fatal("the undecodable page reported a size")
	}
	book, err := h.idx.GetBook(t.Context(), h.dirBook)
	if err != nil {
		t.Fatalf("GetBook: %v", err)
	}
	if book.DimsState != "partial" {
		t.Fatalf("dims_state = %q, want %q", book.DimsState, "partial")
	}
}

// The probe reads a header and stops. 23 µs for a JPEG is the measured number
// in arch §5.8, and the reason it is that cheap is that it never touches the
// pixel data.
func TestProbeDims_readsFarLessThanTheWholeEntry(t *testing.T) {
	t.Parallel()
	big := testutil.TinyJPEG(t, 1400, 2000)
	if len(big) < 64<<10 {
		t.Fatalf("the fixture is only %d bytes; the assertion would be vacuous", len(big))
	}

	cfg, read, err := probeDims(bytes.NewReader(big))
	if err != nil {
		t.Fatalf("probeDims: %v", err)
	}
	if cfg.Width != 1400 || cfg.Height != 2000 {
		t.Fatalf("config = %dx%d, want 1400x2000", cfg.Width, cfg.Height)
	}
	if read >= dimsProbeCap {
		t.Fatalf("read %d bytes, want < %d", read, dimsProbeCap)
	}
	if read >= int64(len(big)) {
		t.Fatalf("read %d of %d bytes: the probe consumed the whole entry", read, len(big))
	}
}

// The cap is hard: a file that never declares a size cannot make the probe read
// a gigabyte looking for one.
func TestProbeDims_neverReadsPastTheCap(t *testing.T) {
	t.Parallel()
	junk := bytes.Repeat([]byte{0x42}, 4<<20)
	_, read, err := probeDims(bytes.NewReader(junk))
	if err == nil {
		t.Fatal("probeDims found dimensions in 4 MiB of 0x42")
	}
	if read > dimsProbeCap {
		t.Fatalf("read %d bytes, want at most the %d-byte cap", read, dimsProbeCap)
	}
}

// image.DecodeConfig succeeds on an animated WebP — the VP8X chunk carries the
// canvas size — so a dimension pass must never be mistaken for a decodability
// probe. This test exists to keep that trap documented in code.
func TestProbeDims_animatedWebP_reportsASizeItCannotDecode(t *testing.T) {
	t.Parallel()
	data := testutil.TinyAnimatedWebP(t)
	cfg, _, err := probeDims(bytes.NewReader(data))
	if err != nil {
		t.Skipf("x/image/webp no longer reports a config for animated files: %v", err)
	}
	if cfg.Width == 0 {
		t.Fatal("expected a canvas size")
	}
	if !isAnimatedWebP(data) {
		t.Fatal("the same bytes must still be refused for thumbnailing")
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func TestSafeRel_refusesEveryEscapeShape(t *testing.T) {
	t.Parallel()
	for _, bad := range []string{
		"", ".", "..", "/etc/passwd", "../secret.jpg", "a/../../b",
		`..\windows\win.ini`, "a//../../b", "./../x",
	} {
		if got, err := safeRel(bad); err == nil {
			t.Errorf("safeRel(%q) = %q, want an error", bad, got)
		}
	}
	for _, good := range []string{"a.jpg", "series/[cover].jpg", `series\cover.jpg`, "a/b/c.png"} {
		if _, err := safeRel(good); err != nil {
			t.Errorf("safeRel(%q): %v", good, err)
		}
	}
}

func TestValidKey_rejectsAnythingIdsCouldNotHaveProduced(t *testing.T) {
	t.Parallel()
	for _, bad := range []string{"", "short", strings.Repeat("a", 17), "../../etc/pass", "AAAAAAAAAAAAAAAA", "aaaaaaaa.aaaaaaa"} {
		if err := validKey(bad); err == nil {
			t.Errorf("validKey(%q) = nil", bad)
		}
	}
	if err := validKey("yvtfrny77ehkt2we"); err != nil {
		t.Errorf("validKey on a real id: %v", err)
	}
}

func TestUndecodableError_isMatchableBothWays(t *testing.T) {
	t.Parallel()
	err := fmt.Errorf("wrapped: %w", &UndecodableError{Reason: ReasonAnimatedWebP, Err: os.ErrInvalid})
	if !errors.Is(err, ErrUndecodable) {
		t.Fatal("errors.Is(err, ErrUndecodable) = false")
	}
	if !errors.Is(err, os.ErrInvalid) {
		t.Fatal("the decoder's own error was not reachable through Unwrap")
	}
	var ue *UndecodableError
	if !errors.As(err, &ue) || ue.Reason != ReasonAnimatedWebP {
		t.Fatalf("errors.As lost the reason: %v", err)
	}
}
