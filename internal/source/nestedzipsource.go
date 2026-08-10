package source

import (
	"context"
	"fmt"
	"io"
	"time"

	"shelf/internal/archive"
	"shelf/internal/archive/nested"
)

// nestedZipSource serves a book that is a ZIP *inside* another ZIP.
//
// 45 books in the reference collection are containers holding nothing but
// volumes — `겟 벡커스 1~39완.zip` is 1.4 GB and 39 of them — and prd §7.2 put
// them out of scope, so each indexed as one empty book with no pages at all.
// This source is what makes each inner volume its own 권.
//
// It is a thin wrapper on purpose. Everything below it is unchanged: the inner
// archive is presented as an io.ReaderAt by internal/archive/nested, and the
// same archive.Reader then indexes it and streams its entries. The exclusion
// rules, the natural page order and the CP949/Shift_JIS name decoding are all
// the ones an ordinary ZIP book gets, because they are literally the same code.
type nestedZipSource struct {
	zipSource
	// innerPath is the outer container's entry name for this volume, exactly as
	// books.inner_path recorded it.
	innerPath string
}

func openNestedZIP(ctx context.Context, f *Factory, b Book) (BookSource, error) {
	if b.InnerPath == "" {
		return nil, fmt.Errorf("opening book %s: %w (a nested book with no inner path)", b.ID, ErrUnsupported)
	}
	inner, err := openZIP(ctx, f, b)
	if err != nil {
		return nil, err
	}
	return &nestedZipSource{zipSource: *inner.(*zipSource), innerPath: b.InnerPath}, nil
}

func (s *nestedZipSource) Kind() Kind { return KindNestedZIP }

// VolumeLister is implemented by a source whose container may hold whole books
// rather than pages. The scanner type-asserts for it, exactly as the HTTP layer
// does for [StaleChecker].
type VolumeLister interface {
	Volumes(ctx context.Context) ([]string, error)
}

// Volumes lists the archive entries inside a ZIP container that are themselves
// archives this build can open, in the container's own order.
//
// It is what turns one `비어 있음` book into a series of volumes, and the
// scanner calls it only for a container that produced no pages of its own — so
// an ordinary book never pays for it, and neither does an unchanged one, which
// is not opened at all.
//
// `.rar`, `.7z` and friends are deliberately not returned. prd §7.2 keeps those
// formats out and nothing here can read them; listing them would produce books
// that cannot open. `사모님은 학생회장.zip` is the mixed case in the collection —
// 7 ZIPs and 8 RARs — and it yields its 7 readable volumes rather than nothing.
func (s *zipSource) Volumes(ctx context.Context) ([]string, error) {
	ref, err := s.acquireMatching(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing volumes of book %s: %w", s.book.ID, err)
	}
	defer ref.Release()

	ix, err := s.f.arch.ReadIndex(ctx, ref, ref.Size())
	if ix == nil {
		return nil, fmt.Errorf("listing volumes of book %s: %w", s.book.ID, err)
	}
	var out []string
	for i := range ix.Entries {
		e := &ix.Entries[i]
		if e.Dir || e.Encrypted || e.Size == 0 {
			continue
		}
		if !nestedVolumeExt(Ext(e.Name)) {
			continue
		}
		out = append(out, e.Name)
	}
	return out, nil
}

// nestedVolumeExt is the set of inner-archive extensions that become volumes.
// It is ZIP and its comic-book alias only; see NestedVolumes.
func nestedVolumeExt(ext string) bool {
	return ext == ".zip" || ext == ".cbz"
}

// innerRef finds this volume's entry in the outer container's directory.
//
// The outer directory is re-read on every call rather than cached in the index.
// It is the cheap half of the work — two ReadAt calls for a container that
// holds tens of entries, against inflating the volume itself — and it keeps
// books.inner_path the single source of truth for which volume this is, with no
// stored offsets that a repacked container could silently invalidate.
func (s *nestedZipSource) innerRef(ctx context.Context, r io.ReaderAt, size int64) (archive.EntryRef, error) {
	ix, err := s.f.arch.ReadIndex(ctx, r, size)
	if ix == nil {
		return archive.EntryRef{}, fmt.Errorf("reading container of book %s: %w", s.book.ID, err)
	}
	for i := range ix.Entries {
		if ix.Entries[i].Name == s.innerPath {
			return ix.Entries[i].Ref(), nil
		}
	}
	// The container no longer holds this volume: it was repacked, or the index
	// is stale. Either way this book is gone, not broken — FR-IDX-010 says say
	// so and carry on.
	return archive.EntryRef{}, fmt.Errorf("book %s: %w (%q is no longer in the container)",
		s.book.ID, archive.ErrCorrupt, s.innerPath)
}

// List indexes the inner volume.
//
// This is the one place where a nested book costs more than a plain one: a
// deflated inner archive has to be inflated once to reach its central
// directory, which is at its end. Measured on the collection that is ~500 ms
// for a 107-page volume of `겟 벡커스`, paid once per volume per scan.
func (s *nestedZipSource) List(ctx context.Context) (*Listing, error) {
	ref, err := s.acquireMatching(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing book %s: %w", s.book.ID, err)
	}
	defer ref.Release()

	l := &Listing{Kind: KindNestedZIP}
	entry, err := s.innerRef(ctx, ref, ref.Size())
	if err != nil {
		return l, fmt.Errorf("listing book %s: %w", s.book.ID, err)
	}

	inner, err := nested.Open(ctx, ref, entry)
	if err != nil {
		return l, fmt.Errorf("listing book %s: %w", s.book.ID, err)
	}
	defer inner.Close()

	ix, readErr := s.f.arch.ReadIndex(ctx, inner, inner.Size())
	if ix == nil {
		return l, fmt.Errorf("listing book %s: %w", s.book.ID, readErr)
	}
	l.ZIP64 = ix.ZIP64
	if ix.Encrypted() {
		return l, fmt.Errorf("listing book %s: %w", s.book.ID, encryptedErr())
	}

	pagesFromIndex(l, ix)

	if readErr != nil {
		return l, fmt.Errorf("listing book %s: %w", s.book.ID, readErr)
	}
	if len(l.Pages) == 0 {
		return l, fmt.Errorf("listing book %s: %w", s.book.ID, ErrNoPages)
	}
	return l, nil
}

// Open streams one page out of the inner volume.
//
// A fresh nested reader is opened per page. That is deliberate: the adapter
// holds one inflate stream whose offset only moves forward cheaply, so sharing
// it between concurrent page requests would make them fight over it. One reader
// per stream costs an inflate up to the page's offset — 13 ms for a mid-volume
// page of `겟 벡커스`, because the inner entries are stored JPEGs and inflating
// them is very nearly a copy.
func (s *nestedZipSource) Open(ctx context.Context, p Page, _ OpenOptions) (*Stream, error) {
	ref, err := s.f.pool.Acquire(ctx, s.abs, s.book.FileMtime, s.book.FileSize)
	if err != nil {
		return nil, fmt.Errorf("opening page %d of book %s: %w", p.No, s.book.ID, err)
	}

	entry, err := s.innerRef(ctx, ref, ref.Size())
	if err != nil {
		ref.Release()
		return nil, fmt.Errorf("opening page %d of book %s: %w", p.No, s.book.ID, err)
	}
	inner, err := nested.Open(ctx, ref, entry)
	if err != nil {
		ref.Release()
		return nil, fmt.Errorf("opening page %d of book %s: %w", p.No, s.book.ID, err)
	}
	rc, err := s.f.arch.OpenEntry(ctx, inner, p.Ref())
	if err != nil {
		inner.Close()
		ref.Release()
		return nil, fmt.Errorf("opening page %d of book %s: %w", p.No, s.book.ID, err)
	}

	// Both the adapter and the pooled handle are released when the response is
	// finished with, in that order: the adapter reads through the handle.
	release := func() {
		inner.Close()
		ref.Release()
	}
	return &Stream{
		ReadCloser:  wrapBody(rc, release),
		ContentType: ContentType(p.Ext),
		Size:        p.Size,
		ModTime:     time.Unix(s.book.FileMtime, 0).UTC(),
	}, nil
}

var (
	_ BookSource   = (*nestedZipSource)(nil)
	_ StaleChecker = (*nestedZipSource)(nil)
	_ VolumeLister = (*zipSource)(nil)
)
