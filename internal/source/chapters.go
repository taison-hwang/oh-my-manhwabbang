package source

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"shelf/internal/natsort"
)

// A container whose pages all live in per-chapter sub-directories is not one
// 권 — it is several, and decision D-73 makes each of them a book.
//
// `여자친구 만들고파! 01~08권.zip` is the shape: 842 pages in eight directories,
// one per volume, indexed as a single 842-page book that no reader can navigate.
// Measured over the whole collection, 484 of 11,153 archives (4.3%) are this,
// holding 279,541 pages between them; the biggest is `암살교실 1~180화.zip`, whose
// directories are literally named 화.
//
// The rule is not new — it is the one prd §2.2 row 2 already applies to a
// *folder* on disk, where each image sub-folder is its own 권. An archive of the
// same tree behaved differently only because nothing had ever looked inside one
// for directories. This makes the two agree.
//
// Nothing here reads a payload. The partition is computed from the page list the
// scanner already holds, so the decision costs one pass over a slice it just
// built (arch §4.3's two ReadAt calls are still the whole of what a scan does to
// an archive).

// ChapterRoot is the [Chapter.Path] of the pages that sit at the top level of a
// container rather than inside one of its chapter directories — the stray cover
// image beside 29 volume directories in `야와라! - YAWARA! (1-29).zip`.
//
// It is `.` rather than the empty string on purpose: an empty inner path is what
// every book that is *not* nested has, and [ids.NestedBookID] gives such a book
// the plain [ids.BookID] — so the container and the chapter made of its loose
// pages would collide on one id. `.` is also exactly what path.Dir answers for a
// top-level entry, so the sentinel is the value the question already has.
const ChapterRoot = "."

// Chapter is one 권 that a container's directory structure describes.
type Chapter struct {
	// Path is the directory's full slash path inside the container, exactly as
	// books.inner_path records it, or [ChapterRoot] for the top-level pages.
	Path string
	// Pages is how many of the container's pages fell into this chapter. It is
	// what the scanner logs; the chapter's own listing is what it indexes.
	Pages int
}

// Chapters partitions a container's pages into the 권 its directories describe,
// or returns nil for a container that is one book.
//
// The partition is total: every page belongs to exactly one returned chapter,
// and no page is dropped. That is the property worth keeping — a rule that
// splits a book by discarding part of it would trade one wrong page list for a
// quieter one.
//
// It answers nil — one book, unchanged — for all of these:
//
//   - fewer than two directories, which is 10,589 of the collection's archives
//     (94.9%) and includes the very common single wrapper directory, where
//     `01권.zip/01권/001.jpg` is one volume that happens to be packed with its
//     folder;
//   - pages loose at the top level *and* a shared wrapper directory, the one
//     shape where "which directory is this page's chapter" has two defensible
//     answers. No archive in the collection is this, and inventing a rule for a
//     file that does not exist is how the wrong rule gets shipped.
//
// Only `/` separates a path. A DOS-era archive that uses `\` produces one
// chapter-less book, which is today's behaviour and the safe direction: the
// alternative is a partition whose prefixes do not match the entry names the
// pages are actually served by.
func Chapters(pages []Page) []Chapter {
	if len(pages) < 2 {
		return nil
	}
	common := commonDir(pages)
	counts := make(map[string]int, 8)
	for i := range pages {
		counts[chapterOf(pages[i].EntryPath, common)]++
	}
	loose := counts[ChapterRoot]
	delete(counts, ChapterRoot)
	if len(counts) < 2 {
		return nil
	}
	if loose > 0 && common != "" {
		return nil
	}

	out := make([]Chapter, 0, len(counts)+1)
	if loose > 0 {
		out = append(out, Chapter{Path: ChapterRoot, Pages: loose})
	}
	for dir, n := range counts {
		out = append(out, Chapter{Path: dir, Pages: n})
	}
	// FR-IDX-007 order, with the loose pages first so that the cover ladder's
	// rule 3 — page 1 of the first readable book — reaches the cover image that
	// was sitting at the top of the archive for exactly that purpose.
	slices.SortFunc(out, func(a, b Chapter) int {
		switch {
		case a.Path == b.Path:
			return 0
		case a.Path == ChapterRoot:
			return -1
		case b.Path == ChapterRoot:
			return 1
		}
		if c := natsort.Compare(a.Path, b.Path); c != 0 {
			return c
		}
		return strings.Compare(a.Path, b.Path)
	})
	return out
}

// chapterOf names the chapter an entry belongs to, given the directory prefix
// every page of the container shares.
//
// The shared prefix is stripped before the first segment is taken, so the eight
// volumes of `시리즈/01`…`시리즈/08` are eight chapters rather than one; and a page
// nested deeper inside a chapter (`01/컬러/001.jpg`, 27 archives) belongs to the
// chapter, not to a chapter of its own.
func chapterOf(entry, common string) string {
	rest := entry
	if common != "" {
		rest = rest[len(common)+1:]
	}
	i := strings.IndexByte(rest, '/')
	if i < 0 {
		return ChapterRoot
	}
	if common == "" {
		return rest[:i]
	}
	return common + "/" + rest[:i]
}

// commonDir is the longest directory prefix every page shares, or "".
func commonDir(pages []Page) string {
	common := dirSegments(pages[0].EntryPath)
	for i := 1; i < len(pages) && len(common) > 0; i++ {
		segs := dirSegments(pages[i].EntryPath)
		n := min(len(common), len(segs))
		k := 0
		for k < n && common[k] == segs[k] {
			k++
		}
		common = common[:k]
	}
	return strings.Join(common, "/")
}

// dirSegments splits an entry's directory into its elements; a top-level entry
// has none.
func dirSegments(entry string) []string {
	i := strings.LastIndexByte(entry, '/')
	if i < 0 {
		return nil
	}
	return strings.Split(entry[:i], "/")
}

// inChapter reports whether a container entry belongs to one chapter.
//
// The two rules are exact rather than approximate, and [Chapters] is what makes
// them so: the returned directories are siblings under one shared prefix, so no
// entry can be under two of them, and [ChapterRoot] only ever appears when that
// shared prefix is empty — which is what lets "no slash at all" be the whole
// test for it.
func inChapter(entry, chapter string) bool {
	if chapter == ChapterRoot {
		return !strings.Contains(entry, "/")
	}
	return strings.HasPrefix(entry, chapter+"/")
}

// openNestedDir builds the source for one chapter directory of a container
// ([KindNestedDir]).
//
// It is the cheapest of the nested shapes: the pages are entries of the
// container itself, at the container's own offsets, so serving one is the
// ordinary [containerSource] path with a prefix filter over the directory read.
// Nothing is inflated to reach them and no adapter sits in the way — unlike an
// archive nested inside an archive, which has to be inflated to be indexed at
// all (see nestedsource.go).
//
// The reader comes from the *container's* extension, not from the kind, because
// the kind names a directory and a directory has no format. That is what lets
// the same book kind serve a chapter of a ZIP and a chapter of a RAR.
func openNestedDir(_ context.Context, f *Factory, b Book) (BookSource, error) {
	if b.InnerPath == "" {
		return nil, fmt.Errorf("opening book %s: %w (a chapter book with no inner path)",
			b.ID, ErrUnsupported)
	}
	src, err := openContainer(f, b, f.readerFor(ContainerKind(b.RelPath)), KindNestedDir)
	if err != nil {
		return nil, err
	}
	cs, ok := src.(*containerSource)
	if !ok {
		return nil, fmt.Errorf("opening book %s: %w (chapter of a %T)", b.ID, ErrUnsupported, src)
	}
	cs.chapter = b.InnerPath
	return cs, nil
}
