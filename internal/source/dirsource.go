package source

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
)

// dirSource serves a book that is a directory of loose images (FR-SRV-005).
//
// Every open goes through the root's *os.Root — path-traversal layer 3 of arch
// §8.1 — which refuses at the openat(2) level to leave the root, including
// through a symlink. Nothing in this file ever calls os.Open on a joined path.
type dirSource struct {
	f    *Factory
	book Book
	root *os.Root
	rel  string // the book directory, relative to the root, slash-separated
}

func openDir(_ context.Context, f *Factory, b Book) (BookSource, error) {
	root, _, err := f.resolve(b)
	if err != nil {
		return nil, err
	}
	rel, err := safeRel(b.RelPath)
	if err != nil {
		return nil, fmt.Errorf("opening book %s: %w", b.ID, err)
	}
	return &dirSource{f: f, book: b, root: root, rel: rel}, nil
}

func (s *dirSource) Kind() Kind   { return KindDir }
func (s *dirSource) Close() error { return nil }

// List enumerates the book directory's *direct* children.
//
// It is deliberately not recursive. prd §2.2 makes an image sub-folder a book
// in its own right, so a directory nested inside a book directory is either a
// separate book — the scanner's classification decides that, not this package —
// or noise. Recursing here would silently merge two volumes into one.
func (s *dirSource) List(ctx context.Context) (*Listing, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	l := &Listing{Kind: KindDir, NameEncoding: "utf-8"}

	d, err := s.root.Open(filepath.FromSlash(s.rel))
	if err != nil {
		return l, fmt.Errorf("listing book %s: %w", s.book.ID, err)
	}
	defer func() { _ = d.Close() }()

	ents, err := d.ReadDir(-1)
	if err != nil {
		return l, fmt.Errorf("listing book %s: %w", s.book.ID, err)
	}

	pages := make([]Page, 0, len(ents))
	for _, ent := range ents {
		name := ent.Name()
		if ent.IsDir() {
			l.Excluded++
			continue
		}
		fi, err := ent.Info()
		if err != nil {
			// A file that vanished between ReadDir and Info is not a book
			// failure; it is one page fewer.
			l.Excluded++
			s.f.log.Debug("skipping an unreadable directory entry",
				"book_id", s.book.ID, "root", s.book.RootName, "rel_path", path.Join(s.rel, name), "err", err)
			continue
		}
		// DirEntry.Info reports the link itself, not its target, so a symlink
		// fails this test. That is what we want: os.Root refuses an escaping
		// symlink at Open time anyway, and indexing a page that can never be
		// served would put a permanent error into the viewer's page list.
		if !fi.Mode().IsRegular() {
			l.Excluded++
			continue
		}
		if drop, _ := Excluded(name, fi.Size(), false); drop {
			l.Excluded++
			continue
		}
		pages = append(pages, Page{
			Name:      name,
			EntryPath: name,
			Ext:       Ext(name),
			Size:      fi.Size(),
			Mtime:     fi.ModTime().Unix(),
		})
	}

	finish(l, pages)
	if len(l.Pages) == 0 {
		return l, fmt.Errorf("listing book %s: %w", s.book.ID, ErrNoPages)
	}
	return l, nil
}

// Open serves one file straight off the filesystem (FR-SRV-005, FR-SRV-008).
//
// The body is an *os.File, so it is seekable and the HTTP layer gets Range,
// Last-Modified and If-Modified-Since from http.ServeContent for free.
func (s *dirSource) Open(ctx context.Context, p Page, _ OpenOptions) (*Stream, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// The entry is validated *before* it is joined, not after. path.Join cleans
	// as it concatenates, so "book" + "../secret.jpg" collapses to
	// "secret.jpg" — still inside the root, so os.Root would happily open it,
	// and a page would have escaped its own book. Checking the entry on its own
	// is what makes that unrepresentable.
	entry, err := safeRel(p.EntryPath)
	if err != nil {
		return nil, fmt.Errorf("opening page %d of book %s: %w", p.No, s.book.ID, err)
	}
	rel, err := safeRel(path.Join(s.rel, entry))
	if err != nil {
		return nil, fmt.Errorf("opening page %d of book %s: %w", p.No, s.book.ID, err)
	}

	f, err := s.root.Open(filepath.FromSlash(rel))
	if err != nil {
		return nil, fmt.Errorf("opening page %d of book %s: %w", p.No, s.book.ID, err)
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("opening page %d of book %s: %w", p.No, s.book.ID, err)
	}
	if !fi.Mode().IsRegular() {
		_ = f.Close()
		return nil, fmt.Errorf("opening page %d of book %s: %w", p.No, s.book.ID, ErrUnsafePath)
	}

	return &Stream{
		ReadCloser:  wrapBody(f, nil),
		ContentType: ContentType(p.Ext),
		Size:        fi.Size(),
		ModTime:     fi.ModTime().UTC(),
	}, nil
}

var _ BookSource = (*dirSource)(nil)
