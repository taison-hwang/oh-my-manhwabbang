package source

import (
	"context"
	"fmt"
	"time"

	"shelf/internal/archive"
	"shelf/internal/openpool"
)

// containerSource serves a book that is a single archive file.
//
// It is format-blind: which container format it is reading lives entirely in
// the [archive.Reader] it was built with, which is what decision D-07 kept that
// interface for. A ZIP book and a RAR book differ here by one field.
//
// It holds no file handle of its own. Every operation borrows one from the
// pool for exactly as long as it needs it, which is what lets a hundred books
// be open in the UI while 64 descriptors serve them all (FR-SRV-004).
type containerSource struct {
	f    *Factory
	book Book
	abs  string
	// arch reads this container's format. Never f.arch directly: a nested
	// volume's outer and inner containers can be different formats, and a RAR
	// inside a ZIP is nine books in the reference collection.
	arch archive.Reader
	kind Kind
	// chapter narrows the source to one directory inside the container, for a
	// [KindNestedDir] book (D-73). Empty — the ordinary case — is the whole
	// container.
	chapter string
}

func openZIP(_ context.Context, f *Factory, b Book) (BookSource, error) {
	return openContainer(f, b, f.readerFor(KindZIP), KindZIP)
}

func openRAR(_ context.Context, f *Factory, b Book) (BookSource, error) {
	return openContainer(f, b, f.readerFor(KindRAR), KindRAR)
}

func openContainer(f *Factory, b Book, arch archive.Reader, kind Kind) (BookSource, error) {
	if f.pool == nil {
		return nil, fmt.Errorf("opening book %s: %w (no archive handle pool)", b.ID, ErrUnsupported)
	}
	if arch == nil {
		return nil, fmt.Errorf("opening book %s: %w (no reader for kind %q)", b.ID, ErrUnsupported, kind)
	}
	_, abs, err := f.resolve(b)
	if err != nil {
		return nil, err
	}
	return &containerSource{f: f, book: b, abs: abs, arch: arch, kind: kind}, nil
}

func (s *containerSource) Kind() Kind   { return s.kind }
func (s *containerSource) Close() error { return nil }

// List reads the central directory and turns it into pages.
//
// FR-IDX-002 in one sentence: this is the only thing a scan does to an
// archive. No entry payload is touched, and the whole read is the two ReadAt
// calls of arch §4.3.
func (s *containerSource) List(ctx context.Context) (*Listing, error) {
	ref, err := s.acquireMatching(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing book %s: %w", s.book.ID, err)
	}
	defer ref.Release()

	ix, readErr := s.arch.ReadIndex(ctx, ref, ref.Size())
	l := &Listing{Kind: s.kind}
	if ix == nil {
		return l, fmt.Errorf("listing book %s: %w", s.book.ID, readErr)
	}
	l.ZIP64 = ix.ZIP64

	// FR-IDX-010: one encrypted entry makes the whole book encrypted. We do
	// not list a partial page set for it — the pages would 401 one by one,
	// which is a worse experience than a single honest badge on the volume.
	if ix.Encrypted() {
		return l, fmt.Errorf("listing book %s: %w", s.book.ID, encryptedErr(s.arch.Format()))
	}

	pagesFromIndex(l, ix, s.chapter)

	// A partially readable directory is reported *with* the pages that did
	// parse: the truncated `군계 07권.zip` still shows most of its volume, and
	// FR-IDX-010 asks for isolation, not deletion.
	if readErr != nil {
		return l, fmt.Errorf("listing book %s: %w", s.book.ID, readErr)
	}
	if len(l.Pages) == 0 {
		return l, fmt.Errorf("listing book %s: %w", s.book.ID, noPagesErr(l))
	}
	return l, nil
}

// acquireMatching borrows a handle whose (mtime, size) are the ones the caller
// passed in — that is, the metadata this listing is about to be recorded under.
//
// The pool is an LRU of descriptors that are *already open*, and a cache hit
// answers without re-stat-ing anything (openpool.Pool.Acquire). After
// `mv 궁\ 24.zip.new 궁\ 24.zip` the path resolves to a new inode while the pool
// still holds a live descriptor on the old, unlinked one. Reading that is not a
// stale read — it is a read of a file the user deleted — and every number
// derived from it gets written down against the new file's identity. That is
// how a repaired archive keeps its `비어 있음` badge: the verdict is real, it is
// just about a file that no longer exists.
//
// So the mismatch is not tolerated here. The handle is dropped from the pool
// and the path re-opened once, which is one open in the only case that needs
// it. If the fresh descriptor still disagrees, no inode matches what we are
// about to record — the archive is being rewritten right now, or the metadata
// was never measured from this file — and the honest answer is to fail this one
// book (FR-IDX-010) and read it again next scan, which the recorded
// (size, mtime) themselves guarantee.
//
// Only listing does this. openpool's tolerance is written for the serving path
// (arch §5.2, §7.6) and is left exactly as it was: a page stream is committed to
// the offsets the index recorded, and those belong to the descriptor the pool
// is holding, not to the file that replaced it.
func (s *containerSource) acquireMatching(ctx context.Context) (*openpool.Ref, error) {
	ref, err := s.f.pool.Acquire(ctx, s.abs, s.book.FileMtime, s.book.FileSize)
	if err != nil {
		return nil, err
	}
	if !ref.Stale() {
		return ref, nil
	}

	ref.Release()
	s.f.pool.Invalidate(s.abs)
	ref, err = s.f.pool.Acquire(ctx, s.abs, s.book.FileMtime, s.book.FileSize)
	if err != nil {
		return nil, err
	}
	if ref.Stale() {
		size, mtime := ref.Size(), ref.ModTime()
		ref.Release()
		return nil, fmt.Errorf("%w: it is %d bytes at mtime %d, not the %d at %d being recorded",
			ErrContainerChanged, size, mtime, s.book.FileSize, s.book.FileMtime)
	}
	return ref, nil
}

// Open streams one entry (FR-SRV-001, FR-SRV-002, FR-SRV-003, NFR-PRF-006).
//
// The handle stays borrowed until the caller closes the stream, so a page
// being written to a slow client keeps its descriptor and the pool will not
// close it underneath (openpool's eviction rule).
func (s *containerSource) Open(ctx context.Context, p Page, _ OpenOptions) (*Stream, error) {
	ref, err := s.f.pool.Acquire(ctx, s.abs, s.book.FileMtime, s.book.FileSize)
	if err != nil {
		return nil, fmt.Errorf("opening page %d of book %s: %w", p.No, s.book.ID, err)
	}

	rc, err := s.arch.OpenEntry(ctx, ref, p.Ref())
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
func (s *containerSource) Stale(ctx context.Context) (bool, error) {
	ref, err := s.f.pool.Acquire(ctx, s.abs, s.book.FileMtime, s.book.FileSize)
	if err != nil {
		return false, fmt.Errorf("checking book %s: %w", s.book.ID, err)
	}
	defer ref.Release()
	return ref.Stale(), nil
}

// pagesFromIndex turns a container's directory into the book's page list,
// applying the FR-IDX-006 exclusions and FR-IDX-007 order.
//
// It is shared by every source and every format, which is the point: a volume
// inside a container must be enumerated by exactly the same rules as one that
// is its own file, and a RAR by the same rules as a ZIP, down to which entries
// are dropped and why. `Thumbs.db` is excluded from a RAR because it is the
// identical predicate, not because anyone remembered to add it twice.
//
// chapter, when set, is the one directory of the container this book is
// (D-73). Entries outside it are not this book's business at all — not its
// pages, not its Excluded count, and not evidence about what format it holds —
// so they are dropped before any of those are decided.
func pagesFromIndex(l *Listing, ix *archive.Index, chapter string) {
	encodings := make(map[string]int, 4)
	// Bytes per foreign container format, so that a book which is nothing but
	// one of them can say which one. Sized by bytes rather than by count
	// because the biggest entry is what the book actually is.
	var foreign map[string]int64
	// Whether anything in here is a volume this build could open. A container
	// holding one is not described by what it could not read: it is about to
	// become a series of books (D-70, D-71), and the scanner only tries that
	// for a book reported `empty`.
	var hasVolume bool
	pages := make([]Page, 0, len(ix.Entries))
	for i := range ix.Entries {
		e := &ix.Entries[i]
		if chapter != "" && !inChapter(e.Name, chapter) {
			continue
		}
		if drop, _ := Excluded(e.Name, e.Size, e.Dir); drop {
			l.Excluded++
			if !e.Dir && e.Size > 0 && !e.Encrypted {
				if NestedVolumeExt(Ext(e.Name)) {
					hasVolume = true
				} else if f := ForeignFormat(e.Name); f != "" {
					if foreign == nil {
						foreign = make(map[string]int64, 2)
					}
					foreign[f] += e.Size
				}
			}
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
	// Only a book with nothing else to offer is described by what it could not
	// read. One stray `.7z` beside 103 pages is a stray `.7z`; one beside seven
	// ZIPs and eight RARs is `사모님은 학생회장.zip`, whose fifteen volumes are
	// the answer and whose `.7z` is a footnote. Reporting the format there
	// would close the container before the scanner ever looked inside it.
	if len(pages) == 0 && !hasVolume {
		l.Foreign = dominantFormat(foreign)
	}
	l.NameEncoding = nameEncoding(encodings)
	finish(l, pages)
}

// encryptedErr keeps the arch §4.11 wording in one place. The format prefix
// comes from the reader that found the encrypted entry, so a RAR book does not
// report itself as a ZIP in books.error.
func encryptedErr(format string) error {
	return fmt.Errorf("%s: %w", format, archive.ErrEncrypted)
}

// dominantFormat picks the foreign format that accounts for the most bytes,
// ties broken by name so the answer is stable across scans.
func dominantFormat(byBytes map[string]int64) string {
	var best string
	var bestBytes int64 = -1
	for name, n := range byBytes {
		if n > bestBytes || (n == bestBytes && name < best) {
			best, bestBytes = name, n
		}
	}
	return best
}

// noPagesErr distinguishes the two ways a container can produce no pages.
//
// "비어 있음 · no supported image entries" is the honest sentence for
// `비둘기.zip`, which holds one directory entry and nothing at all (ruling
// E-14). It is a false sentence for `펌프킨 시저스 04.zip`, which holds 39.5 MB
// of HV3: there is something in there, this build just cannot open it. Saying
// "empty" sends its owner looking for a file that is fine.
func noPagesErr(l *Listing) error {
	if l.Foreign != "" {
		return fmt.Errorf("%w (the archive holds %s, which this build cannot open)",
			ErrUnsupported, l.Foreign)
	}
	return ErrNoPages
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
	_ BookSource   = (*containerSource)(nil)
	_ StaleChecker = (*containerSource)(nil)
)
