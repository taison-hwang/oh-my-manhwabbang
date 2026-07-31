//go:build !nopdf

package pdfium

import (
	"bytes"
	"context"
	"fmt"
	"image/jpeg"
	"sync"

	gopdfium "github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/references"
	"github.com/klippa-app/go-pdfium/requests"
)

// Render width policy. FR-SRV-006 makes the resolution a request parameter;
// these bounds keep a slider drag from spawning an unbounded set of distinct
// renders and cache entries (arch §5.7).
const (
	DefaultWidth = 1200
	MinWidth     = 100
	MaxWidth     = 4000
	// WidthSnap is the granularity a requested width is rounded to.
	WidthSnap = 100
)

// SnapWidth clamps w into [MinWidth, maxWidth] and rounds it to the nearest
// WidthSnap. maxWidth <= 0 means MaxWidth; w <= 0 means DefaultWidth.
//
// It is exported because the HTTP layer must snap *before* it builds the cache
// key, or two requests one pixel apart become two cache entries for the same
// picture.
func SnapWidth(w, maxWidth int) int {
	if maxWidth <= 0 || maxWidth > MaxWidth {
		maxWidth = MaxWidth
	}
	if w <= 0 {
		w = DefaultWidth
	}
	w = ((w + WidthSnap/2) / WidthSnap) * WidthSnap
	if w < MinWidth {
		w = MinWidth
	}
	if w > maxWidth {
		w = maxWidth
	}
	return w
}

// Doc is an open PDF document. It holds one pdfium worker, so it is the unit
// of serialisation against pdf.workers. It is safe for concurrent use, but
// renders through it are serialised.
type Doc struct {
	renderer *Renderer
	pages    int

	mu     sync.Mutex
	inst   gopdfium.Pdfium
	ref    references.FPDF_DOCUMENT
	closed bool
}

// PageCount is the number of pages, read once at Open.
func (d *Doc) PageCount() int { return d.pages }

// RenderJPEG rasterises a 1-based page at the given width and encodes it as
// JPEG at the given quality.
//
// Encoding happens here rather than in the caller for one reason: in wasm mode
// the rendered image is backed by the module's linear memory and
// res.Cleanup() is mandatory to release it. Handing the *image.RGBA out would
// make every call site responsible for a deferred cleanup, and one missed
// defer is a slow memory leak in the wasm heap. Bytes are safe to hand out;
// the bitmap is not.
func (d *Doc) RenderJPEG(ctx context.Context, pageNo, width, quality int) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if pageNo < 1 || pageNo > d.pages {
		return nil, fmt.Errorf("rendering pdf page %d: %w (document has %d)", pageNo, ErrNoSuchPage, d.pages)
	}
	if quality <= 0 || quality > 100 {
		quality = 82
	}
	width = SnapWidth(width, 0)

	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil, ErrClosed
	}

	res, err := d.inst.RenderPageInPixels(&requests.RenderPageInPixels{
		Page: requests.Page{
			ByIndex: &requests.PageByIndex{Document: d.ref, Index: pageNo - 1},
		},
		Width: width, // height follows from the page's aspect ratio
	})
	if err != nil {
		return nil, fmt.Errorf("rendering pdf page %d: %w", pageNo, err)
	}
	defer res.Cleanup()

	if res.Result.Image == nil {
		return nil, fmt.Errorf("rendering pdf page %d: pdfium returned no image", pageNo)
	}

	var buf bytes.Buffer
	buf.Grow(64 << 10)
	if err := jpeg.Encode(&buf, res.Result.Image, &jpeg.Options{Quality: quality}); err != nil {
		return nil, fmt.Errorf("encoding pdf page %d: %w", pageNo, err)
	}
	return buf.Bytes(), nil
}

// PageSize returns the point dimensions of a 1-based page, which is what the
// viewer needs to know whether a page is a double-page spread (FR-VWR-004)
// without rasterising it.
func (d *Doc) PageSize(ctx context.Context, pageNo int) (width, height float64, err error) {
	if err := ctx.Err(); err != nil {
		return 0, 0, err
	}
	if pageNo < 1 || pageNo > d.pages {
		return 0, 0, fmt.Errorf("sizing pdf page %d: %w (document has %d)", pageNo, ErrNoSuchPage, d.pages)
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return 0, 0, ErrClosed
	}

	size, err := d.inst.GetPageSize(&requests.GetPageSize{
		Page: requests.Page{
			ByIndex: &requests.PageByIndex{Document: d.ref, Index: pageNo - 1},
		},
	})
	if err != nil {
		return 0, 0, fmt.Errorf("sizing pdf page %d: %w", pageNo, err)
	}
	return size.Width, size.Height, nil
}

// Close releases the document and returns the pdfium worker to the pool. It is
// idempotent.
func (d *Doc) Close() error {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil
	}
	d.closed = true
	inst, ref := d.inst, d.ref
	d.inst = nil
	d.mu.Unlock()

	var err error
	if _, cerr := inst.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: ref}); cerr != nil {
		err = fmt.Errorf("closing pdf document: %w", cerr)
	}
	d.renderer.releaseInstance(inst)
	return err
}
