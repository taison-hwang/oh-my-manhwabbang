package source

import (
	"context"
	"fmt"
	"time"

	"shelf/internal/archive"
)

// zipSource serves a book that is a single ZIP container.
//
// It holds no file handle of its own. Every operation borrows one from the
// pool for exactly as long as it needs it, which is what lets a hundred books
// be open in the UI while 64 descriptors serve them all (FR-SRV-004).
type zipSource struct {
	f    *Factory
	book Book
	abs  string
}

func openZIP(_ context.Context, f *Factory, b Book) (BookSource, error) {
	if f.pool == nil {
		return nil, fmt.Errorf("opening book %s: %w (no archive handle pool)", b.ID, ErrUnsupported)
	}
	_, abs, err := f.resolve(b)
	if err != nil {
		return nil, err
	}
	return &zipSource{f: f, book: b, abs: abs}, nil
}

func (s *zipSource) Kind() Kind   { return KindZIP }
func (s *zipSource) Close() error { return nil }

// List reads the central directory and turns it into pages.
//
// FR-IDX-002 in one sentence: this is the only thing a scan does to an
// archive. No entry payload is touched, and the whole read is the two ReadAt
// calls of arch §4.3.
func (s *zipSource) List(ctx context.Context) (*Listing, error) {
	ref, err := s.f.pool.Acquire(ctx, s.abs, s.book.FileMtime, s.book.FileSize)
	if err != nil {
		return nil, fmt.Errorf("listing book %s: %w", s.book.ID, err)
	}
	defer ref.Release()

	ix, readErr := s.f.arch.ReadIndex(ctx, ref, ref.Size())
	l := &Listing{Kind: KindZIP}
	if ix == nil {
		return l, fmt.Errorf("listing book %s: %w", s.book.ID, readErr)
	}
	l.ZIP64 = ix.ZIP64

	// FR-IDX-010: one encrypted entry makes the whole book encrypted. We do
	// not list a partial page set for it — the pages would 401 one by one,
	// which is a worse experience than a single honest badge on the volume.
	if ix.Encrypted() {
		return l, fmt.Errorf("listing book %s: %w", s.book.ID, encryptedErr())
	}

	encodings := make(map[string]int, 4)
	pages := make([]Page, 0, len(ix.Entries))
	for i := range ix.Entries {
		e := &ix.Entries[i]
		if drop, _ := Excluded(e.Name, e.Size, e.Dir); drop {
			l.Excluded++
			continue
		}
		encodings[e.NameEncoding]++
		pages = append(pages, Page{
			Name:        baseName(e.Name),
			EntryPath:   e.Name,
			Ext:         Ext(e.Name),
			Size:        e.Size,
			CompSize:    e.CompSize,
			Method:      e.Method,
			LocalHdrOff: e.LocalHdrOff,
			CRC32:       e.CRC32,
		})
	}
	l.NameEncoding = nameEncoding(encodings)
	finish(l, pages)

	// A partially readable directory is reported *with* the pages that did
	// parse: the truncated `군계 07권.zip` still shows most of its volume, and
	// FR-IDX-010 asks for isolation, not deletion.
	if readErr != nil {
		return l, fmt.Errorf("listing book %s: %w", s.book.ID, readErr)
	}
	if len(l.Pages) == 0 {
		return l, fmt.Errorf("listing book %s: %w", s.book.ID, ErrNoPages)
	}
	return l, nil
}

// Open streams one entry (FR-SRV-001, FR-SRV-002, FR-SRV-003, NFR-PRF-006).
//
// The handle stays borrowed until the caller closes the stream, so a page
// being written to a slow client keeps its descriptor and the pool will not
// close it underneath (openpool's eviction rule).
func (s *zipSource) Open(ctx context.Context, p Page, _ OpenOptions) (*Stream, error) {
	ref, err := s.f.pool.Acquire(ctx, s.abs, s.book.FileMtime, s.book.FileSize)
	if err != nil {
		return nil, fmt.Errorf("opening page %d of book %s: %w", p.No, s.book.ID, err)
	}

	rc, err := s.f.arch.OpenEntry(ctx, ref, p.Ref())
	if err != nil {
		ref.Release()
		return nil, fmt.Errorf("opening page %d of book %s: %w", p.No, s.book.ID, err)
	}

	return &Stream{
		ReadCloser:  wrapBody(rc, ref.Release),
		ContentType: ContentType(p.Ext),
		// pages.size is the uncompressed length, which is the response's
		// Content-Length for both stored and deflated entries.
		Size:    p.Size,
		ModTime: time.Unix(s.book.FileMtime, 0).UTC(),
	}, nil
}

// Stale reports whether the container on disk still matches what the index
// recorded, without reading a page. The HTTP layer uses it to decide between
// serving and answering 409 stale_version (arch §7.6).
func (s *zipSource) Stale(ctx context.Context) (bool, error) {
	ref, err := s.f.pool.Acquire(ctx, s.abs, s.book.FileMtime, s.book.FileSize)
	if err != nil {
		return false, fmt.Errorf("checking book %s: %w", s.book.ID, err)
	}
	defer ref.Release()
	return ref.Stale(), nil
}

// encryptedErr keeps the arch §4.11 wording in one place.
func encryptedErr() error {
	return fmt.Errorf("zip: %w", archive.ErrEncrypted)
}

// baseName is path.Base for a slash path, but returns "" for "" rather than
// ".".
func baseName(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[i+1:]
		}
	}
	return p
}

var (
	_ BookSource   = (*zipSource)(nil)
	_ StaleChecker = (*zipSource)(nil)
)
