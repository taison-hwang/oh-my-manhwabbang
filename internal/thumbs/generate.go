package thumbs

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"math"
	"os"
	"path/filepath"

	"github.com/disintegration/imaging"

	"shelf/internal/index"
	"shelf/internal/pdfium"
	"shelf/internal/source"

	// The decoders of FR-IDX-011 plus the two arch §5.5 adds. Importing them
	// for their registration side effect is what makes image.Decode able to
	// sniff them; AVIF is behind a build tag in avif_on.go / avif_off.go
	// because its wasm runtime costs ~170 MiB per decode (D-25).
	// image/jpeg is imported for its encoder above, which registers the decoder
	// as a side effect; gif and png are decode-only here.
	_ "image/gif"
	_ "image/png"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

// encode renders the finished image in the configured format. CON-003 pins
// that to JPEG in v1, and newCache refuses to build with anything else, so the
// switch has one arm on purpose: the format string is a cache-hash input, and
// adding an arm here without changing that string would serve WebP bytes from a
// path whose hash says "jpeg".
func (s *Service) encode(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	buf.Grow(64 << 10)
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: s.cache.quality}); err != nil {
		return nil, fmt.Errorf("encoding thumbnail: %w", err)
	}
	return buf.Bytes(), nil
}

// produce is the whole pipeline of arch §5.4: read the source, decode it,
// downscale with Lanczos, encode a JPEG, publish it atomically. Dimensions fall
// out for free on the way past (arch §5.8).
func (s *Service) produce(ctx context.Context, j job) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	data, sizes, err := s.readSource(ctx, j)
	if err != nil {
		return Result{}, err
	}
	natural := sizes.natural
	if len(data) == 0 {
		return Result{}, &UndecodableError{Reason: ReasonEmptySource}
	}

	custom := j.req.Cover != nil && j.req.Cover.Region != nil
	img, err := s.decode(ctx, data, custom)
	if err != nil {
		return Result{}, err
	}
	b := img.Bounds()
	srcW, srcH := b.Dx(), b.Dy()
	if custom && sniff(data) == "jpeg" {
		cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return Result{}, err
		}
		srcW, srcH = cfg.Width, cfg.Height
	}
	if srcW <= 0 || srcH <= 0 {
		return Result{}, &UndecodableError{Reason: ReasonDecodeFailed, Err: errors.New("decoded image has no area")}
	}

	// E-69 §2.2: a 권 cover is cut from the decoded page BEFORE the downscale,
	// so the half or the region keeps every source pixel the width allows.
	// srcW and srcH stay the whole page's: they are what FR-VWR-004 records
	// below, and half a spread recorded as the page would read as portrait.
	// Whether the page is landscape is the recorded size's call, not the
	// decode's (landscapeBasis); the cut itself is in the decoded columns.
	if j.req.Cover != nil {
		img = cropTo(img, coverRect(b, *j.req.Cover, landscapeBasis(sizes.recorded, natural, b.Size())))
	}

	// D-10: imaging.Lanczos, not x/image/draw — 18.7 ms against 196.9 ms at our
	// ratios. Upscaling is never done: a page narrower than the requested width
	// is published as it is, which is also what makes a 2×3 test fixture a
	// legal 640 px thumbnail.
	if img.Bounds().Dx() > j.req.Width {
		img = imaging.Resize(img, j.req.Width, 0, imaging.Lanczos)
	}

	encoded, err := s.encode(img)
	if err != nil {
		return Result{}, err
	}
	res, err := s.cache.publish(KindThumbs, j.key, encoded)
	if err != nil {
		return Result{}, err
	}
	res.SourceWidth, res.SourceHeight = srcW, srcH

	// FR-VWR-004 for free: the picture has already been decoded, so its
	// intrinsic size costs nothing to record. A PDF reports the page's own
	// geometry instead of the render size (natural), because the render width
	// is a request parameter and would make the stored value meaningless.
	if !j.req.fromFile() {
		w, h := srcW, srcH
		if natural.X > 0 && natural.Y > 0 {
			w, h = natural.X, natural.Y
		}
		s.recordDims(j.req.ID, j.req.PageNo, w, h)
	}
	return res, nil
}

// coverRect is the part of a decoded page of bounds b that a 권 cover shows.
//
// Automatic (E-69 §2.2(b)): a page wider than tall — w > h, FR-VWR-004's one
// definition, so a square page is not landscape — gives the half read first,
// and any other page is used whole. Wider-than-tall is asked of basis, the
// page's size as landscapeBasis picks it, and NOT of b: the decode of a PDF
// render can be square where the page is not. The cut is in b's own columns:
// the width is split exactly, the spare column of an odd width going to the
// half read first — ltr is [0, ceil(W/2)), rtl is [floor(W/2), W) of the
// decoded W. No spine detection (E-69 §3).
//
// A region (E-69 §2.4) is mapped onto the pixels edge by edge: each of the
// four edges is math.Round(fraction × size) — halves away from zero — clamped
// to [0, size]. Rounding the edges, not the origin and the size, means two
// regions that share an edge in normalised space share it in pixels. A cut
// that rounds to nothing keeps one pixel, the last one when it sits at the far
// edge; Region.Valid's 0.05 floor makes that reachable only below 20 px.
func coverRect(b image.Rectangle, c Cover, basis image.Point) image.Rectangle {
	w, h := b.Dx(), b.Dy()
	if c.Region == nil {
		if basis.X <= basis.Y {
			return b
		}
		if c.Dir == dirRTL {
			return image.Rect(b.Min.X+w/2, b.Min.Y, b.Max.X, b.Max.Y)
		}
		return image.Rect(b.Min.X, b.Min.Y, b.Min.X+(w+1)/2, b.Max.Y)
	}
	r := c.Region
	x0, x1 := edges(r.X, r.W, w)
	y0, y1 := edges(r.Y, r.H, h)
	return image.Rect(b.Min.X+x0, b.Min.Y+y0, b.Min.X+x1, b.Min.Y+y1)
}

// edges maps one axis of a region — origin and extent as fractions — onto
// [0, size] pixel edges, as coverRect documents.
func edges(origin, extent float64, size int) (lo, hi int) {
	clamp := func(v float64) int {
		return int(math.Max(0, math.Min(float64(size), math.Round(v*float64(size)))))
	}
	lo, hi = clamp(origin), clamp(origin+extent)
	if hi-lo < 1 {
		if lo >= size {
			lo = size - 1
		}
		hi = lo + 1
	}
	return lo, hi
}

// cropTo returns the part of img inside r. Every decoder this package
// registers returns a type with SubImage, which shares the pixels — no second
// RGBA buffer on top of arch §5.4's ~25 MiB per decode. imaging.Crop, a copy,
// is only the fallback for a type that has none.
func cropTo(img image.Image, r image.Rectangle) image.Image {
	if r == img.Bounds() {
		return img
	}
	if sub, ok := img.(interface {
		SubImage(image.Rectangle) image.Image
	}); ok {
		return sub.SubImage(r)
	}
	return imaging.Crop(img, r)
}

// landscapeBasis is the size that decides whether a page is landscape for an
// automatic 권 cover: the first of sizes with an area, given in this order —
//
//  1. recorded: pages.width/height in the index, once a decode or the
//     dimension pass has measured the page (arch §5.8);
//  2. natural: the size produce is about to record for this page — a PDF's
//     PageSize in points, rounded, which is what recordDims and measurePage
//     both write for a PDF;
//  3. the decoded image, only when neither exists (produce passes it last;
//     readPage, before any decode, passes only the first two and gets the zero
//     Point for "not known yet").
//
// E-69 §2.2(b), last paragraph: the input is the recorded intrinsic size the
// viewer's spread mode is given, and the decoded or rendered pixels only when
// none is recorded, PDFs included. The viewer gets pages.width/height as
// PageInfo.w/h (internal/httpapi/convert.go toPageInfo) and calls it landscape
// on w > h (web/src/features/viewer/fit.ts isLandscape). Judging on the render
// instead gave a different answer for a nearly square landscape PDF page:
// pdfium rounds the render's height UP, so 4000×3999 pt at 800 px is 800×800,
// and the cover used the page whole while the viewer showed it as a spread.
// Once an <img> has loaded, fit.ts makeDimsLookup prefers its natural size;
// for a PDF that is a render, which is the SPA's side of the same rounding.
func landscapeBasis(sizes ...image.Point) image.Point {
	for _, p := range sizes {
		if p.X > 0 && p.Y > 0 {
			return p
		}
	}
	return image.Point{}
}

// recordedSize is a page row's pages.width/height, or the zero Point while the
// page has not been measured.
func recordedSize(p index.Page) image.Point {
	if p.Width == nil || p.Height == nil {
		return image.Point{}
	}
	return image.Pt(*p.Width, *p.Height)
}

// renderWidth is the width to ask a PDF source to rasterise a page at, for a
// page thumbnail (c nil) or a 권 cover. Every other source ignores it. A PDF
// has no pixels until it is rendered, at the width the request picks.
//
// The result is ALWAYS rounded UP to pdfium.WidthSnap, because the source
// snaps to the NEAREST step: rendered at "240", a 240 px thumbnail was a 200 px
// render and, nothing being upscaled (D-10), a 200 px thumbnail — 120 was 100,
// 640 was 600. Rounded up, the render is at least the requested width and
// produce's downscale brings it to exactly that width (FR-THM-002's target
// width, impl-plan §0.4). The cache key is not touched: it is derived in
// prepare from the requested width snapped to `thumbnails.widths`, never from
// this one, so a 200 px file cached before the round-up is still a hit under
// the same key and is not re-made until its content version changes or the
// cache is purged.
//
// A cover's render is also widened by the cut's factor (E-69 §2.2, §2.4), or
// the cut would be half — or a twentieth — as wide as asked: ⌈width / W⌉ for a
// region, ×2 for an automatic cover unless basis (landscapeBasis over the
// recorded and natural sizes, the same input coverRect decides the cut on)
// says the page is not landscape. A page of unknown shape is widened as if
// landscape: if it turns out landscape the half is the full width, and if not
// the downscale in produce brings it back to width.
//
// Two ceilings apply. The source clamps to `pdf.max_width` (≤ pdfium.MaxWidth).
// This function keeps the render inside checkPixelBudget when basis is known —
// W² × h/w ≤ maxPixels, rounded down to the snap — because a render over it is
// refused after it was paid for. It never goes below width: a page over the
// ceiling at the plain width is refused as a thumbnail too, and a page
// thumbnail the round-up would push over the ceiling asks for its plain width,
// which the source snaps exactly as it did before the round-up existed. When
// basis is unknown no ceiling can be computed in advance, and checkPixelBudget
// alone guards the decode, after the render. For a PDF the recorded size and
// the natural one are the same rounded PageSize, so basis's aspect is the
// render's own.
func renderWidth(width int, c *Cover, basis image.Point, maxPixels int64) int {
	known := basis.X > 0 && basis.Y > 0
	want := width
	switch {
	case c == nil:
	case c.Region != nil:
		want = int(math.Ceil(float64(width) / c.Region.W))
	case !known || basis.X > basis.Y:
		want = 2 * width
	}
	const snap = pdfium.WidthSnap
	want = (max(want, width) + snap - 1) / snap * snap
	if known && maxPixels > 0 {
		limit := int(math.Sqrt(float64(maxPixels)*float64(basis.X)/float64(basis.Y))) / snap * snap
		if want > limit {
			want = max(limit, width)
		}
	}
	return want
}

// sourceSizes is what reading a page learns about its size besides the bytes.
type sourceSizes struct {
	// natural is the page's intrinsic size when the source can report it
	// without rasterising (PDF only); zero means "use the decoded image". It is
	// what produce records for the page (FR-VWR-004).
	natural image.Point
	// recorded is pages.width/height as the index holds it now, zero until
	// measured. Only a 권 cover's landscape test reads it (landscapeBasis).
	recorded image.Point
}

// readSource fetches the bytes a thumbnail is made from, capped at
// `thumbnails.max_source_bytes`.
//
// A loose-file cover has neither a natural nor a recorded size: it is never
// cut, and nothing is recorded for it.
func (s *Service) readSource(ctx context.Context, j job) (data []byte, sizes sourceSizes, err error) {
	if j.req.fromFile() {
		data, err = s.readCoverFile(ctx, j.req)
		return data, sourceSizes{}, err
	}
	return s.readPage(ctx, j.req)
}

// readCoverFile reads a loose image beside a series — the `cover_kind='file'`
// branch of the cover ladder (arch §4.10).
//
// It goes through the root's *os.Root, which is path-traversal layer 3: os.Root
// refuses at the openat(2) level any path that escapes its directory, including
// through a symlink.
func (s *Service) readCoverFile(ctx context.Context, req Request) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.roots == nil {
		return nil, fmt.Errorf("reading cover of %s: %w (no roots configured)", req.ID, ErrNotFound)
	}
	rel, err := safeRel(req.RelPath)
	if err != nil {
		return nil, err
	}
	root, ok := s.roots.Root(req.RootName)
	if !ok || root == nil {
		return nil, fmt.Errorf("reading cover of %s: %w (root %q is not available)", req.ID, ErrNotFound, req.RootName)
	}
	f, err := root.Open(filepath.FromSlash(rel))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("reading cover of %s: %w", req.ID, ErrNotFound)
		}
		return nil, fmt.Errorf("reading cover of %s: %w", req.ID, err)
	}
	defer func() { _ = f.Close() }()
	return s.readCapped(f)
}

// readPage streams one page out of its book. It is the ordinary path: 98.7 % of
// the reference collection reaches this function with a deflated JPEG inside a
// ZIP.
func (s *Service) readPage(ctx context.Context, req Request) ([]byte, sourceSizes, error) {
	if s.idx == nil || s.src == nil {
		return nil, sourceSizes{}, fmt.Errorf("thumbnailing book %s: %w (no index or source factory)", req.ID, ErrNotFound)
	}
	book, row, err := s.lookupPage(ctx, req.ID, req.PageNo)
	if err != nil {
		return nil, sourceSizes{}, err
	}
	page := pageOf(row)
	bs, err := s.src.Open(ctx, book)
	if err != nil {
		return nil, sourceSizes{}, mapSourceError(req.ID, err)
	}
	defer func() { _ = bs.Close() }()

	// A PDF page has an intrinsic size in points that costs one document open
	// to read and is not the size of the render we are about to make.
	var natural image.Point
	if sizer, ok := bs.(pageSizer); ok {
		if w, h, err := sizer.PageSize(ctx, page.No); err == nil && w > 0 && h > 0 {
			natural = image.Pt(int(math.Round(w)), int(math.Round(h)))
		}
	}

	// A PDF render is rounded up past the source's nearest-100 px snap so a page
	// thumbnail is the requested width, and for a cover also widened to leave
	// the cut that width (E-69 §2.2) — renderWidth. Every other source ignores
	// Width. The key was fixed in prepare from the requested width alone.
	sizes := sourceSizes{natural: natural, recorded: recordedSize(row)}
	width := renderWidth(req.Width, req.Cover, landscapeBasis(sizes.recorded, sizes.natural), s.maxPixels)
	st, err := bs.Open(ctx, page, source.OpenOptions{Width: width})
	if err != nil {
		return nil, sourceSizes{}, mapSourceError(req.ID, err)
	}
	defer func() { _ = st.Close() }()

	data, err := s.readCapped(st)
	if err != nil {
		return nil, sourceSizes{}, err
	}
	return data, sizes, nil
}

// lookupPage turns an opaque book id into the book the source factory needs
// and the page's index row — pageOf of it for the source, and its recorded
// size for a cover's landscape test. Nothing but the index ever converts an id
// into a path — that is path-traversal layer 1.
func (s *Service) lookupPage(ctx context.Context, bookID string, pageNo int) (source.Book, index.Page, error) {
	row, err := s.idx.GetBook(ctx, bookID)
	if err != nil {
		if errors.Is(err, index.ErrNotFound) {
			return source.Book{}, index.Page{}, fmt.Errorf("thumbnailing book %s: %w", bookID, ErrNotFound)
		}
		return source.Book{}, index.Page{}, fmt.Errorf("thumbnailing book %s: %w", bookID, err)
	}
	pg, err := s.idx.GetPage(ctx, bookID, pageNo)
	if err != nil {
		if errors.Is(err, index.ErrNotFound) {
			return source.Book{}, index.Page{}, fmt.Errorf("thumbnailing book %s page %d: %w", bookID, pageNo, ErrNotFound)
		}
		return source.Book{}, index.Page{}, fmt.Errorf("thumbnailing book %s page %d: %w", bookID, pageNo, err)
	}
	return bookOf(row), pg, nil
}

// bookOf maps an index row onto the plain value internal/source consumes.
func bookOf(row index.BookRow) source.Book {
	return source.Book{
		ID:        row.ID,
		Kind:      source.Kind(row.Kind),
		RootName:  row.RootName,
		RelPath:   row.RelPath,
		InnerPath: row.InnerPath,
		FileSize:  row.FileSize,
		FileMtime: row.FileMtime,
	}
}

// pageOf maps an index row onto internal/source's page value.
func pageOf(p index.Page) source.Page {
	return source.Page{
		No:          p.PageNo,
		Name:        p.Name,
		EntryPath:   p.EntryPath,
		Ext:         p.Ext,
		Size:        p.Size,
		CompSize:    p.CompSize,
		Method:      uint16(p.Method),
		LocalHdrOff: p.LocalHdrOff,
		CRC32:       p.CRC32,
		Mtime:       p.Mtime,
	}
}

// mapSourceError converts a book-level failure into this package's vocabulary.
// A book the build cannot serve at all is a 501 elsewhere, but as a thumbnail
// it is simply unavailable — the placeholder of FR-LIB-008 covers it.
func mapSourceError(bookID string, err error) error {
	switch {
	case errors.Is(err, source.ErrUnsupported):
		return &UndecodableError{Reason: ReasonUnknownFormat, Err: err}
	case errors.Is(err, source.ErrNoPages), errors.Is(err, source.ErrUnknownRoot), errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("thumbnailing book %s: %w", bookID, ErrNotFound)
	default:
		return fmt.Errorf("thumbnailing book %s: %w", bookID, err)
	}
}

// readCapped reads at most MaxSourceBytes, and reports anything larger as a
// per-page failure rather than letting one pathological entry take the process
// out. The +1 is what distinguishes "exactly at the cap" from "over it".
func (s *Service) readCapped(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, s.maxSource+1))
	if err != nil {
		// A cancelled read is not a verdict about the picture. Passing it
		// through unchanged is what keeps a shutdown from being memoised as
		// "this page cannot be decoded".
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, &UndecodableError{Reason: ReasonDecodeFailed, Err: err}
	}
	if int64(len(data)) > s.maxSource {
		return nil, &UndecodableError{
			Reason: ReasonSourceTooLarge,
			Err:    fmt.Errorf("source exceeds thumbnails.max_source_bytes (%d)", s.maxSource),
		}
	}
	return data, nil
}

// decode turns bytes into an image, applying the format policy of arch §5.5.
//
// The format is sniffed from the magic bytes BEFORE anything is decoded, for
// two reasons that image.Decode's own sniffing cannot serve: an animated WebP
// has to be refused with a named reason rather than a generic decoder error,
// and an AVIF has to take the one-permit semaphore before its wasm runtime
// allocates ~170 MiB.
func (s *Service) decode(ctx context.Context, data []byte, orient bool) (image.Image, error) {
	if s.hookDecode != nil {
		s.hookDecode()
	}
	switch format := sniff(data); format {
	case "":
		return nil, &UndecodableError{Reason: ReasonUnknownFormat}

	case "webp":
		// arch §5.5: x/image/webp rejects an animated file outright (verified:
		// `webp: invalid format`), and there is no still frame to fall back to
		// without a second wasm decoder. Naming the reason is what turns the
		// failure into a 422 with `detail.reason: "animated_webp"` instead of
		// an anonymous decode error. Note that image.DecodeConfig *succeeds*
		// on these — the VP8X chunk carries the canvas size — so the dimension
		// pass must never be used as a decodability probe.
		if isAnimatedWebP(data) {
			return nil, &UndecodableError{Reason: ReasonAnimatedWebP}
		}

	case "avif":
		if !s.avif || !avifEnabled {
			return nil, &UndecodableError{Reason: ReasonAVIFDisabled}
		}
		release, err := s.acquireAVIF(ctx)
		if err != nil {
			return nil, err
		}
		defer release()
	}

	if err := s.checkPixelBudget(data); err != nil {
		return nil, err
	}

	s.counters.decodes.Add(1)
	var img image.Image
	var err error
	if orient {
		img, err = imaging.Decode(bytes.NewReader(data), imaging.AutoOrientation(true))
	} else {
		img, _, err = image.Decode(bytes.NewReader(data))
	}
	if err != nil {
		return nil, &UndecodableError{Reason: ReasonDecodeFailed, Err: err}
	}
	return img, nil
}

// checkPixelBudget refuses a picture whose HEADER declares more area than
// Options.MaxSourcePixels, before image.Decode allocates a pixel buffer from
// that same header.
//
// `thumbnails.max_source_bytes` cannot do this job: it measures compressed
// bytes, and compression is exactly what a decompression bomb exploits. A
// 127-byte JPEG whose SOF0 says 24000×24000 is comfortably inside the 64 MiB
// cap and asks image.Decode for ~549 MiB — at four workers, ~2.2 GB against
// NFR-PRF-005's 200 MB. image.DecodeConfig reads the same header for a few
// microseconds and allocates nothing, so the guard is the cheapest part of the
// pipeline.
//
// A header DecodeConfig cannot parse is not refused here. Every decoder
// registered in this package parses the header before it allocates (jpeg
// allocates at SOS, png after IHDR), so image.Decode will fail in the same place
// without allocating — and letting it fail there keeps the reason the decoder's
// own `decode_failed` rather than a misleading size verdict.
func (s *Service) checkPixelBudget(data []byte) error {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return nil
	}
	px := int64(cfg.Width) * int64(cfg.Height)
	if px <= s.maxPixels {
		return nil
	}
	return &UndecodableError{
		Reason: ReasonSourceTooLargePixels,
		Err: fmt.Errorf("source declares %d×%d = %d pixels, over the %d-pixel decode ceiling",
			cfg.Width, cfg.Height, px, s.maxPixels),
	}
}

// acquireAVIF takes the one-permit AVIF gate of D-25 and returns its release.
//
// One permit regardless of `thumbnails.workers` is not a tuning choice: a
// single AVIF decode costs ~1.1 s and ~170 MiB of wasm heap, so four
// concurrent ones would put idle RSS an order of magnitude past NFR-PRF-005's
// 200 MB budget for a format that occurs zero times in the reference
// collection.
func (s *Service) acquireAVIF(ctx context.Context) (release func(), err error) {
	select {
	case s.avifSem <- struct{}{}:
		return func() { <-s.avifSem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// sniff names the container format from its magic bytes. It knows exactly the
// seven extensions of FR-IDX-011 plus TIFF, which arch §5.5 requires the
// thumbnailer to decode even though it is not a listed extension.
//
// It deliberately does not consult the file name: a `.jpg` holding a PNG is
// common in the wild, and the decoder that matters is the one the bytes ask
// for.
func sniff(b []byte) string {
	switch {
	case len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
		return "jpeg"
	case len(b) >= 8 && string(b[:8]) == "\x89PNG\r\n\x1a\n":
		return "png"
	case len(b) >= 6 && (string(b[:6]) == "GIF87a" || string(b[:6]) == "GIF89a"):
		return "gif"
	case len(b) >= 2 && b[0] == 'B' && b[1] == 'M':
		return "bmp"
	case len(b) >= 4 && (string(b[:4]) == "II*\x00" || string(b[:4]) == "MM\x00*"):
		return "tiff"
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return "webp"
	case len(b) >= 12 && string(b[4:8]) == "ftyp" && isAVIFBrand(b):
		return "avif"
	}
	return ""
}

// isAVIFBrand inspects an ISO-BMFF `ftyp` box: the major brand at bytes 8..12
// and, because a still AVIF is sometimes written with a generic major brand,
// the compatible-brand list that starts at byte 16.
//
// Bytes 12..16 are the minor VERSION, not a brand. Skipping them is what keeps
// a minor version that happens to spell 0x61766966 from being read as "avif".
func isAVIFBrand(b []byte) bool {
	if len(b) < 12 {
		return false
	}
	if isAVIFTag(b[8:12]) {
		return true
	}
	boxLen := int(binary.BigEndian.Uint32(b[0:4]))
	if boxLen < 16 || boxLen > len(b) {
		boxLen = len(b)
	}
	for off := 16; off+4 <= boxLen; off += 4 {
		if isAVIFTag(b[off : off+4]) {
			return true
		}
	}
	return false
}

func isAVIFTag(tag []byte) bool {
	return string(tag) == "avif" || string(tag) == "avis"
}

// isAnimatedWebP reports the presence of an ANIM/ANMF chunk in a RIFF WebP.
//
// Detecting it structurally rather than by "the decoder said no" is what keeps
// a corrupt still WebP from being mislabelled `animated_webp` in the 422 the
// user sees — the reason string ends up in front of the user, so it has to be
// true.
func isAnimatedWebP(b []byte) bool {
	if len(b) < 12 || string(b[:4]) != "RIFF" || string(b[8:12]) != "WEBP" {
		return false
	}
	end := uint64(len(b))
	if riff := uint64(binary.LittleEndian.Uint32(b[4:8])) + 8; riff < end {
		end = riff
	}
	for off := uint64(12); off+8 <= end; {
		id := string(b[off : off+4])
		if id == "ANIM" || id == "ANMF" {
			return true
		}
		size := uint64(binary.LittleEndian.Uint32(b[off+4 : off+8]))
		// Every RIFF chunk is padded to an even length.
		off += 8 + size + size%2
	}
	return false
}
