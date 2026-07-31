package scanner

import (
	"errors"
	"path"
	"slices"
	"strings"

	"shelf/internal/natsort"
	"shelf/internal/source"
)

// hardMaxDepth bounds the recursion when `scan.max_depth` is 0, which arch §3.2
// documents as "unlimited (unwise)". Unlimited without a ceiling is a stack
// overflow waiting for a pathological tree; the deepest real nesting in the
// reference collection is 3 (data-survey §7).
const hardMaxDepth = 32

// errTooDeep is recorded once per directory that hardMaxDepth stopped at, so a
// truncated tree is visible in the scan log instead of silently short.
var errTooDeep = errors.New("directory nesting exceeds the scanner's hard depth limit")

// bookNameSeparator joins the sub-path elements of a nested book's display
// name, so `[만화] 기동전사 건담 시리즈` — eight sub-directories flattened into one
// series by ruling E-4 — still shows its grouping as
// `크로스본 건담 / 크로스본 건담 01권.zip` (arch §4.2).
const bookNameSeparator = " / "

// collectBooks is arch §4.2's single function, and it realises every row of the
// prd §2.2 table including "혼재" (mixed).
//
//  1. entries := readdir(D) filtered by the exclusion rules of §4.5
//  2. partition into archives A, pdfs P, loose images I, subdirectories S
//  3. sub := recurse into S while depth < scan.max_depth
//  4. books := A ++ P ++ sub
//  5. loose images:
//     none                        -> nothing
//     no other books              -> D itself is one book       (prd row 3)
//     <= cover_max_loose_images   -> cover candidates, NOT a book  (D-5)
//     more                        -> one "(loose pages)" book   (prd row 6)
//
// dirRel is root-relative and dirMtime is its own modification time. The
// returned loose-image and cover-candidate paths are root-relative and are only
// populated for the top call (depth 0), because the cover ladder of arch §4.10
// looks in the *series* directory.
//
// Ruling E-5 is why there is no dedup anywhere in here: a directory holding both
// `01권/` and `01권.zip` yields two books, and `07권.zip`, `07권.repair.zip`,
// `07권 (2).repair.zip` yield three. Two of those three are truncated downloads,
// so any preference rule risks hiding the only readable copy.
func (s *Scanner) collectBooks(rt *rootRun, dirRel string, dirMtime int64, depth int) (
	books []bookUnit, looseImages, coverCandidates []string, err error,
) {
	children, err := readDir(rt.root, dirRel, rt.followSymlinks)
	if err != nil {
		return nil, nil, nil, err
	}

	var archives, pdfs, images, dirs []childInfo
	for _, c := range children {
		if c.skip != "" {
			rt.note(c.rel, c.skip)
			continue
		}
		if !rt.excludeGlobs.empty() && rt.excludeGlobs.matchPath(c.rel) {
			rt.note(c.rel, reasonExcludeGlob)
			continue
		}
		switch {
		case c.isDir:
			dirs = append(dirs, c)
		case isArchiveName(c.name):
			archives = append(archives, c)
		case isPDFName(c.name):
			pdfs = append(pdfs, c)
		case isPageCandidate(c.name, c.size):
			images = append(images, c)
		default:
			rt.note(c.rel, reasonNotAContainer)
		}
	}

	var sub []bookUnit
	if len(dirs) > 0 && (s.maxDepth == 0 || depth < s.maxDepth) {
		if depth >= hardMaxDepth {
			rt.warn(dirRel, errTooDeep)
		} else {
			for _, d := range dirs {
				nested, _, _, nerr := s.collectBooks(rt, d.rel, d.mtime, depth+1)
				if nerr != nil {
					// An unreadable sub-directory costs its own books, never
					// the series and never the scan (FR-IDX-010).
					rt.warn(d.rel, nerr)
					continue
				}
				sub = append(sub, nested...)
			}
		}
	}

	for _, a := range archives {
		books = append(books, bookUnit{
			relPath: a.rel, kind: source.KindZIP, size: a.size, mtime: a.mtime,
		})
	}
	for _, p := range pdfs {
		books = append(books, bookUnit{
			relPath: p.rel, kind: source.KindPDF, size: p.size, mtime: p.mtime,
		})
	}
	books = append(books, sub...)

	// Step 5 — the loose images, which is where prd §2.2's "mixed" row and the
	// real collection's 47 "N archives + exactly one cover image" directories
	// part company (adjustment D-5, config key scan.cover_max_loose_images).
	switch {
	case len(images) == 0:
		// nothing
	case len(books) == 0:
		// prd §2.2 row 3: images directly inside the folder, so the folder
		// itself is the single book. At depth > 0 this is row 2 — one book per
		// sub-folder, which is 26+ real series.
		books = append(books, s.looseBook(dirRel, dirMtime, children, ""))
	case len(images) <= s.coverMaxLoose:
		for _, i := range images {
			coverCandidates = append(coverCandidates, i.rel)
		}
	default:
		books = append(books, s.looseBook(dirRel, dirMtime, children,
			path.Base(dirRel)+looseBookSuffix))
	}

	if depth == 0 {
		for _, i := range images {
			looseImages = append(looseImages, i.rel)
		}
	}

	// Series-relative paths, then one natural sort over the whole set
	// (FR-IDX-007). Sorting here rather than trusting the A ++ P ++ sub order is
	// what puts `01권/` immediately before `01권.zip` for ruling E-5's
	// duplicates instead of at opposite ends of the volume list.
	for i := range books {
		books[i].rel = seriesRelative(dirRel, books[i].relPath, depth)
	}
	if depth == 0 {
		for i := range books {
			if books[i].name == "" {
				books[i].name = displayName(books[i].rel)
			}
		}
	}
	slices.SortStableFunc(books, func(a, b bookUnit) int {
		if c := natsort.Compare(a.rel, b.rel); c != 0 {
			return c
		}
		return strings.Compare(a.rel, b.rel)
	})
	return books, looseImages, coverCandidates, nil
}

// looseBook builds the kind='dir' book that both loose-image rows of prd §2.2
// produce. Its FR-IDX-003 fingerprint comes from the ReadDir collectBooks has
// already done, so a directory is never enumerated twice in one scan.
func (s *Scanner) looseBook(dirRel string, dirMtime int64, children []childInfo, name string) bookUnit {
	return bookUnit{
		relPath: dirRel,
		name:    name,
		kind:    source.KindDir,
		// arch §3.5: file_size is the container size and is 0 for kind='dir'.
		size:        0,
		mtime:       newestMtime(dirMtime, children),
		fingerprint: fingerprintChildren(children),
	}
}

// newestMtime is what a directory book records as its modification time.
// books.file_mtime feeds series.mtime ("newest mtime among its books"), which is
// FR-LIB-003's 수정일 column and the `sort=mtime` key — so it has to move when the
// contents move, and a directory's own mtime does not change when a nested file
// is rewritten in place.
func newestMtime(dirMtime int64, children []childInfo) int64 {
	newest := dirMtime
	for _, c := range children {
		if c.mtime > newest {
			newest = c.mtime
		}
	}
	return newest
}

// seriesRelative renders a book's path relative to the series directory. Nested
// calls leave the root-relative path in place and the single depth-0 pass
// strips the prefix once, so the arithmetic happens exactly one time per book.
func seriesRelative(dirRel, bookRel string, depth int) string {
	if depth > 0 {
		return bookRel
	}
	if bookRel == dirRel {
		return path.Base(bookRel)
	}
	return strings.TrimPrefix(bookRel, dirRel+"/")
}

// displayName turns a series-relative path into the volume name the UI shows,
// carrying the sub-path so a flattened two-level series still reads as grouped
// (arch §4.2, ruling E-4).
func displayName(rel string) string {
	return strings.Join(strings.Split(rel, "/"), bookNameSeparator)
}
