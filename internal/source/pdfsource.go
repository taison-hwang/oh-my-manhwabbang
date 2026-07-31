//go:build !nopdf

package source

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"shelf/internal/pdfium"
)

// pdfSource serves a book that is a PDF, by rasterising its pages
// (FR-SRV-006, AC-004).
//
// The viewer cannot tell the difference: it asks for page n and gets an image,
// exactly as it does for a ZIP. That is the whole of AC-004 — "a PDF series is
// read in the same viewer, with no distinction".
type pdfSource struct {
	f    *Factory
	book Book
	root *os.Root
	rel  string
}

func openPDF(_ context.Context, f *Factory, b Book) (BookSource, error) {
	if f.pdf == nil || !pdfium.Supported() {
		return nil, fmt.Errorf("opening book %s: %w (pdf rendering is disabled)", b.ID, ErrUnsupported)
	}
	root, _, err := f.resolve(b)
	if err != nil {
		return nil, err
	}
	rel, err := safeRel(b.RelPath)
	if err != nil {
		return nil, fmt.Errorf("opening book %s: %w", b.ID, err)
	}
	return &pdfSource{f: f, book: b, root: root, rel: rel}, nil
}

func (s *pdfSource) Kind() Kind   { return KindPDF }
func (s *pdfSource) Close() error { return nil }

// openDoc opens the file through os.Root (traversal layer 3) and hands the
// *os.File to pdfium as an io.ReadSeeker.
//
// The file is never read into memory: pdfium pulls the byte ranges it needs
// through the reader, which is how NFR-PRF-006 holds for a 500 MB PDF series
// as it does for a ZIP.
func (s *pdfSource) openDoc(ctx context.Context) (*pdfium.Doc, io.Closer, error) {
	f, err := s.root.Open(filepath.FromSlash(s.rel))
	if err != nil {
		return nil, nil, fmt.Errorf("opening book %s: %w", s.book.ID, err)
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, fmt.Errorf("opening book %s: %w", s.book.ID, err)
	}
	doc, err := s.f.pdf.Open(ctx, f, fi.Size())
	if err != nil {
		_ = f.Close()
		return nil, nil, fmt.Errorf("opening book %s: %w", s.book.ID, err)
	}
	return doc, f, nil
}

// List reports one page per PDF page.
//
// Sizes stay zero: a PDF page has no byte length until it is rendered, and the
// render width is a request parameter (FR-SRV-006). EntryPath stays empty, per
// the pages schema of arch §3.5.
func (s *pdfSource) List(ctx context.Context) (*Listing, error) {
	l := &Listing{Kind: KindPDF, NameEncoding: "utf-8"}

	doc, file, err := s.openDoc(ctx)
	if err != nil {
		return l, err
	}
	defer func() {
		_ = doc.Close()
		_ = file.Close()
	}()

	n := doc.PageCount()
	pages := make([]Page, 0, n)
	for i := 1; i <= n; i++ {
		pages = append(pages, Page{
			No: i,
			// The display name is the page number: a PDF page carries no name
			// of its own, and inventing "0001.jpg" would be a lie the UI would
			// then show to the user.
			Name: strconv.Itoa(i),
			Ext:  ".jpg", // what /pages/{n} actually returns
		})
	}
	// The order is already the document's own — every page's EntryPath is empty,
	// so the natural sort is a stable no-op — but finish still runs, so
	// numbering and the empty check are identical across the three kinds.
	finish(l, pages)
	if len(l.Pages) == 0 {
		return l, fmt.Errorf("listing book %s: %w", s.book.ID, ErrNoPages)
	}
	return l, nil
}

// Open rasterises one page at the requested width and returns it as JPEG.
//
// The width is clamped to pdf.max_width and snapped to 100 px so that dragging
// the viewer's zoom cannot spawn a hundred distinct renders and cache entries
// (arch §5.7).
func (s *pdfSource) Open(ctx context.Context, p Page, opt OpenOptions) (*Stream, error) {
	width := opt.Width
	if width <= 0 {
		width = s.f.pdfW
	}
	width = pdfium.SnapWidth(width, s.f.pdfMaxW)

	doc, file, err := s.openDoc(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = doc.Close()
		_ = file.Close()
	}()

	jpg, err := doc.RenderJPEG(ctx, p.No, width, s.f.pdfQ)
	if err != nil {
		return nil, fmt.Errorf("rendering page %d of book %s: %w", p.No, s.book.ID, err)
	}

	// The bytes are already in hand, so the body is a bytes.Reader: seekable,
	// which gives the HTTP layer Range for free.
	return &Stream{
		ReadCloser:  wrapBody(readSeekNopCloser{bytes.NewReader(jpg)}, nil),
		ContentType: "image/jpeg",
		Size:        int64(len(jpg)),
		ModTime:     time.Unix(s.book.FileMtime, 0).UTC(),
	}, nil
}

// PageSize returns a page's intrinsic size in points, which is what spread
// mode needs to spot a double-page scan (FR-VWR-004) without rasterising.
func (s *pdfSource) PageSize(ctx context.Context, pageNo int) (w, h float64, err error) {
	doc, file, err := s.openDoc(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer func() {
		_ = doc.Close()
		_ = file.Close()
	}()
	return doc.PageSize(ctx, pageNo)
}

// readSeekNopCloser adds a no-op Close to a *bytes.Reader while keeping Seek,
// which io.NopCloser would drop.
type readSeekNopCloser struct{ *bytes.Reader }

func (readSeekNopCloser) Close() error { return nil }

var _ BookSource = (*pdfSource)(nil)
