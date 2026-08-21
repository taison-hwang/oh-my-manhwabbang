package source

import (
	"context"
	"fmt"
	"io"
	"time"

	"shelf/internal/archive"
	"shelf/internal/archive/nested"
)

// nestedSource serves a book that is an archive *inside* another archive.
//
// 45 books in the reference collection are containers holding nothing but
// volumes — `겟 벡커스 1~39완.zip` is 1.4 GB and 39 of them — and prd §7.2 put
// them out of scope, so each indexed as one empty book with no pages at all.
// This source is what makes each inner volume its own 권.
//
// It is a thin wrapper on purpose. Everything below it is unchanged: the inner
// archive is presented as an io.ReaderAt by internal/archive/nested, and an
// [archive.Reader] then indexes it and streams its entries. The exclusion
// rules, the natural page order and the CP949/Shift_JIS name decoding are all
// the ones an ordinary book gets, because they are literally the same code.
//
// The two readers are separate because the two containers need not be the same
// format. `사모님은 학생회장.zip` holds 7 ZIPs and 8 RARs; each of the 15 is a
// volume, read by whichever reader its own extension names, through one outer
// ZIP that neither of them knows about.
type nestedSource struct {
	containerSource
	// innerPath is the outer container's entry name for this volume, exactly as
	// books.inner_path recorded it.
	innerPath string
	// innerArch reads the volume. containerSource.arch reads the container it
	// sits in.
	innerArch archive.Reader
}

func openNestedZIP(ctx context.Context, f *Factory, b Book) (BookSource, error) {
	return openNested(ctx, f, b, KindNestedZIP)
}

func openNestedRAR(ctx context.Context, f *Factory, b Book) (BookSource, error) {
	return openNested(ctx, f, b, KindNestedRAR)
}

// openNested builds a nested source. The outer container is opened by the
// reader its own path names — the outer is a ZIP in all 632 nested books here,
// and nothing assumes it — and the inner by the reader `kind` names.
func openNested(_ context.Context, f *Factory, b Book, kind Kind) (BookSource, error) {
	if b.InnerPath == "" {
		return nil, fmt.Errorf("opening book %s: %w (a nested book with no inner path)", b.ID, ErrUnsupported)
	}
	outerKind := ContainerKind(b.RelPath)
	inner, err := openContainer(f, b, f.readerFor(outerKind), kind)
	if err != nil {
		return nil, err
	}
	innerArch := f.readerFor(innerKind(kind))
	if innerArch == nil {
		return nil, fmt.Errorf("opening book %s: %w (no reader for nested kind %q)",
			b.ID, ErrUnsupported, kind)
	}
	return &nestedSource{
		containerSource: *inner.(*containerSource),
		innerPath:       b.InnerPath,
		innerArch:       innerArch,
	}, nil
}

func (s *nestedSource) Kind() Kind { return s.kind }

// VolumeLister is implemented by a source whose container may hold whole books
// rather than pages. The scanner type-asserts for it, exactly as the HTTP layer
// does for [StaleChecker].
type VolumeLister interface {
	Volumes(ctx context.Context) ([]string, error)
}

// Volumes lists the archive entries inside a container that are themselves
// archives this build can open, in the container's own order.
//
// It is what turns one `비어 있음` book into a series of volumes, and the
// scanner calls it only for a container that produced no pages of its own — so
// an ordinary book never pays for it, and neither does an unchanged one, which
// is not opened at all.
//
// An entry whose extension names no reader is not returned, because listing it
// would produce a book that cannot open. Under D-07 that silently dropped every
// `.rar`: `사모님은 학생회장.zip` holds 7 ZIPs and 8 RARs and yielded 7 volumes.
// D-71 gives RAR a reader, so it now yields all 15. `.7z` and friends are still
// dropped, and for the same reason as before.
//
// A source narrowed to one chapter directory answers about that directory only
// (D-73). The scanner never asks one — a chapter is made of pages, so it is
// never the `empty` book that triggers the question — but a source that
// answered about its container's *other* chapters would be a trap for the next
// caller.
func (s *containerSource) Volumes(ctx context.Context) ([]string, error) {
	ref, err := s.acquireMatching(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing volumes of book %s: %w", s.book.ID, err)
	}
	defer ref.Release()

	ix, err := s.arch.ReadIndex(ctx, ref, ref.Size())
	if ix == nil {
		return nil, fmt.Errorf("listing volumes of book %s: %w", s.book.ID, err)
	}
	var out []string
	for i := range ix.Entries {
		e := &ix.Entries[i]
		if s.chapter != "" && !inChapter(e.Name, s.chapter) {
			continue
		}
		if e.Dir || e.Encrypted || e.Size == 0 {
			continue
		}
		if !NestedVolumeExt(Ext(e.Name)) {
			continue
		}
		out = append(out, e.Name)
	}
	return out, nil
}

// NestedVolumeExt reports whether an inner-archive extension becomes a volume.
//
// It is exported because the scanner asks the same question about a container
// before it opens one, and the two answers must not be able to drift: a
// container the scanner expands into volumes the source then refuses to list
// would produce a series of books that all fail to open.
func NestedVolumeExt(ext string) bool {
	return ContainerKind(ext) != ""
}

// ContainerKind maps a path or a bare extension to the book kind that reads
// it. Empty means no reader, which is the honest answer for `.7z`.
//
// It is the one table that decides which container formats this build opens.
// The scanner uses it to classify a file on disk, Volumes uses it to decide
// which entries of a container are volumes, and openNested uses it to pick the
// outer reader — so those three can never disagree about what a `.rar` is.
func ContainerKind(name string) Kind {
	switch Ext(name) {
	case ".zip", ".cbz":
		return KindZIP
	case ".rar", ".cbr":
		return KindRAR
	}
	return ""
}

// innerKind maps a nested kind to the plain kind that reads the same format,
// so one extension table serves both.
func innerKind(k Kind) Kind {
	switch k {
	case KindNestedZIP:
		return KindZIP
	case KindNestedRAR:
		return KindRAR
	}
	return ""
}

// NestedKind is the book kind for a volume of format `ext` found inside a
// container. The scanner records it in books.kind.
func NestedKind(ext string) Kind {
	switch ContainerKind(ext) {
	case KindZIP:
		return KindNestedZIP
	case KindRAR:
		return KindNestedRAR
	}
	return ""
}

// innerRef finds this volume's entry in the outer container's directory.
//
// The outer directory is re-read on every call rather than cached in the index.
// It is the cheap half of the work — two ReadAt calls for a container that
// holds tens of entries, against inflating the volume itself — and it keeps
// books.inner_path the single source of truth for which volume this is, with no
// stored offsets that a repacked container could silently invalidate.
func (s *nestedSource) innerRef(ctx context.Context, r io.ReaderAt, size int64) (archive.EntryRef, error) {
	ix, err := s.arch.ReadIndex(ctx, r, size)
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
func (s *nestedSource) List(ctx context.Context) (*Listing, error) {
	ref, err := s.acquireMatching(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing book %s: %w", s.book.ID, err)
	}
	defer ref.Release()

	l := &Listing{Kind: s.kind}
	entry, err := s.innerRef(ctx, ref, ref.Size())
	if err != nil {
		return l, fmt.Errorf("listing book %s: %w", s.book.ID, err)
	}

	inner, err := nested.Open(ctx, ref, entry)
	if err != nil {
		return l, fmt.Errorf("listing book %s: %w", s.book.ID, err)
	}
	defer inner.Close()

	ix, readErr := s.innerArch.ReadIndex(ctx, inner, inner.Size())
	if ix == nil {
		return l, fmt.Errorf("listing book %s: %w", s.book.ID, readErr)
	}
	l.ZIP64 = ix.ZIP64
	if ix.Encrypted() {
		return l, fmt.Errorf("listing book %s: %w", s.book.ID, encryptedErr(s.innerArch.Format()))
	}

	// s.chapter is empty for every nested volume: a chapter *inside* a volume
	// inside a container needs two inner paths and books.inner_path is one
	// column, so the scanner does not produce that book (D-73).
	pagesFromIndex(l, ix, s.chapter)

	if readErr != nil {
		return l, fmt.Errorf("listing book %s: %w", s.book.ID, readErr)
	}
	if len(l.Pages) == 0 {
		return l, fmt.Errorf("listing book %s: %w", s.book.ID, noPagesErr(l))
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
func (s *nestedSource) Open(ctx context.Context, p Page, _ OpenOptions) (*Stream, error) {
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
	rc, err := s.innerArch.OpenEntry(ctx, inner, p.Ref())
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
	_ BookSource   = (*nestedSource)(nil)
	_ StaleChecker = (*nestedSource)(nil)
	_ VolumeLister = (*containerSource)(nil)
)
