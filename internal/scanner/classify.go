package scanner

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"shelf/internal/archive"
	"shelf/internal/natsort"
	"shelf/internal/source"
)

// series.kind values (arch §3.5). Conflict resolution C-4: a *series* that is a
// directory is "folder"; a *book* that is a directory is "dir".
const (
	SeriesFolder = "folder"
	SeriesZIP    = "zip"
	SeriesPDF    = "pdf"
)

// books.status / series.status values (arch §3.5, §4.11), spelled once so the
// scanner and internal/archive can never drift apart.
const (
	StatusOK          = string(archive.StatusOK)
	StatusError       = string(archive.StatusError)
	StatusEncrypted   = string(archive.StatusEncrypted)
	StatusEmpty       = string(archive.StatusEmpty)
	StatusUnsupported = string(archive.StatusUnsupported)
)

// isFailure reports whether a books.status is one an operator should be counted
// and told about.
//
// 'empty' is deliberately not one. A directory holding only `.txt` novels
// (adjustment D-7) and the 1.44 GB archive of 33 nested archives (decision D-10)
// are both legitimate, fully understood outcomes; counting them as scan errors
// would put a permanent red number on a library that is working correctly.
func isFailure(status string) bool {
	switch status {
	case StatusError, StatusEncrypted, StatusUnsupported:
		return true
	}
	return false
}

// looseBookSuffix is appended to the display name of the book made from loose
// images that sit *alongside* real books, so the volume list says what it is
// (arch §4.2 step 5).
const looseBookSuffix = " (loose pages)"

// childInfo is one direct child of a directory, as one os.ReadDir saw it.
//
// Every field the FNV-1a fingerprint of FR-IDX-003 hashes is here, and the
// fingerprint deliberately covers *every* child including the skipped ones: a
// false re-scan costs one central-directory read, a false skip costs a wrong
// page list until the next time the user touches the file.
type childInfo struct {
	name    string // base name
	rel     string // root-relative slash path
	isDir   bool
	symlink bool
	size    int64
	mtime   int64
	// skip is the FR-IDX-006 reason this child is not a candidate, or "".
	skip string
}

// readDir lists a directory's direct children through the root's *os.Root —
// path-traversal layer 3 of arch §8.1, which refuses at the openat(2) level to
// leave the root, including through a symlink.
//
// It reads. It never creates, renames, removes or touches anything, which is
// FR-CFG-005 / NFR-DAT-002 and is enforced by `make lint`'s check-readonly grep
// over this whole package.
func readDir(root *os.Root, dirRel string, follow bool) ([]childInfo, error) {
	name := "."
	if dirRel != "" {
		name = filepath.FromSlash(dirRel)
	}
	d, err := root.Open(name)
	if err != nil {
		return nil, fmt.Errorf("reading directory %q: %w", dirRel, err)
	}
	defer func() { _ = d.Close() }()

	entries, err := d.ReadDir(-1)
	if err != nil {
		return nil, fmt.Errorf("reading directory %q: %w", dirRel, err)
	}

	out := make([]childInfo, 0, len(entries))
	for _, e := range entries {
		c := childInfo{name: e.Name(), rel: path.Join(dirRel, e.Name())}
		c.symlink = e.Type()&fs.ModeSymlink != 0

		var fi fs.FileInfo
		switch {
		case c.symlink && !follow:
			// Recorded, not resolved: it still has to appear in the
			// fingerprint, because a symlink appearing or vanishing is a
			// change to the directory.
			if lfi, lerr := e.Info(); lerr == nil {
				c.size, c.mtime = lfi.Size(), lfi.ModTime().Unix()
			}
			c.skip = reasonSymlink
			out = append(out, c)
			continue
		case c.symlink:
			// os.Root.Stat follows the link and still refuses an escape, so a
			// symlink out of the root simply disappears here.
			fi, err = root.Stat(filepath.FromSlash(c.rel))
		default:
			fi, err = e.Info()
		}
		if err != nil {
			// Vanished between ReadDir and Info, or a dangling/escaping link.
			// One child fewer is not a scan failure (FR-IDX-010).
			continue
		}

		c.isDir = fi.IsDir()
		c.size = fi.Size()
		c.mtime = fi.ModTime().Unix()
		if !c.isDir && !fi.Mode().IsRegular() {
			c.skip = reasonIrregular
		} else if drop, reason := ignoredChild(c.name); drop {
			c.skip = reason
		}
		out = append(out, c)
	}

	// Natural order (FR-IDX-007) so a scan visits `1권` before `10권`, the
	// fingerprint is order-stable, and the progress display reads the way the
	// user's file manager does.
	slices.SortFunc(out, func(a, b childInfo) int {
		if c := natsort.Compare(a.name, b.name); c != 0 {
			return c
		}
		return strings.Compare(a.name, b.name)
	})
	return out, nil
}

// seriesUnit is one series as classification found it: prd §1.3's "exactly one
// direct child of a root" (ruling E-4 — a two-level directory flattens into one
// series holding every book beneath it).
type seriesUnit struct {
	rootName string
	relPath  string // root-relative slash path
	name     string // display name = base name of relPath
	kind     string // SeriesFolder | SeriesZIP | SeriesPDF
	mtime    int64  // the child's own mtime; overridden by the newest book mtime

	books []bookUnit

	// looseImages are every direct-child image of the series directory,
	// root-relative. Cover ladder rule 1 searches this list (arch §4.10).
	looseImages []string
	// coverCandidates is the ≤ cover_max_loose_images set that arch §4.2 step 5
	// consumed as covers rather than turning into a book (adjustment D-5).
	coverCandidates []string

	// err is a classification failure — an unreadable series directory. The
	// series is still recorded, with status='error' (FR-IDX-010).
	err error
}

// bookUnit is one 권 as classification found it, before its pages are read.
type bookUnit struct {
	relPath string      // root-relative slash path
	rel     string      // path relative to the SERIES directory; sort_key hashes this
	name    string      // display name shown in the UI
	kind    source.Kind // zip | dir | pdf | nestedzip
	// innerPath is the entry inside relPath that IS this book, for a volume
	// found inside a container of volumes (source.KindNestedZIP). Empty for
	// every book that is its own file or directory.
	innerPath string
	size      int64 // container size; 0 for kind=dir (arch §3.5)
	mtime     int64
	// fingerprint is the FR-IDX-003 digest of a directory book's children. It
	// is computed from the ReadDir collectBooks already did, so a directory is
	// never read twice.
	fingerprint string
}

// classifyChild implements prd §2.2 for one direct child of a root.
//
//	regular file, .zip/.cbz -> Series{kind:"zip"}, exactly one Book{kind:"zip"}
//	regular file, .pdf      -> Series{kind:"pdf"}, exactly one Book{kind:"pdf"}
//	regular file, other     -> not a series; one info-level scan_log row
//	directory               -> Series{kind:"folder"}, books = collectBooks(...)
//
// The four file rows are prd §2.2 rows 4 and 5; every other row is a property
// of collectBooks, which is where the "mixed" row and ruling E-5's duplicates
// are realised.
func (s *Scanner) classifyChild(rt *rootRun, c childInfo) (*seriesUnit, bool) {
	if !c.isDir {
		kind, bookKind, ok := seriesKindOfFile(c.name)
		if !ok {
			return nil, false
		}
		u := &seriesUnit{
			rootName: rt.cfg.Name,
			relPath:  c.rel,
			name:     c.name,
			kind:     kind,
			mtime:    c.mtime,
			books: []bookUnit{{
				relPath: c.rel,
				// A single-file series is its own only book, so the
				// series-relative path is empty; the base name is what
				// sort_key and the UI want instead.
				rel:   c.name,
				name:  c.name,
				kind:  bookKind,
				size:  c.size,
				mtime: c.mtime,
			}},
		}
		return u, true
	}

	u := &seriesUnit{
		rootName: rt.cfg.Name,
		relPath:  c.rel,
		name:     c.name,
		kind:     SeriesFolder,
		mtime:    c.mtime,
	}
	books, loose, candidates, err := s.collectBooks(rt, c.rel, c.mtime, 0)
	u.books, u.looseImages, u.coverCandidates, u.err = books, loose, candidates, err
	return u, true
}

// seriesKindOfFile maps a regular file at the top of a root to a series kind.
func seriesKindOfFile(name string) (kind string, bookKind source.Kind, ok bool) {
	switch {
	case isArchiveName(name):
		return SeriesZIP, source.ContainerKind(name), true
	case isPDFName(name):
		return SeriesPDF, source.KindPDF, true
	}
	return "", "", false
}

// seriesStatus folds the books' statuses into the series' own (arch §3.5:
// 'ok' | 'empty' | 'error'), as ruled by E-14. Exactly three rows:
//
//   - no books at all                  → 'empty'
//   - ≥1 book and at least one is 'ok' → 'ok'
//   - ≥1 book and none of them is 'ok' → 'error', carrying a reason
//
// Zero books is 'empty' — that is arch §4.2's five text-novel directories and
// adjustment D-7, listed greyed out rather than silently swallowed.
//
// A series whose every book is itself `empty` is 'error', not 'empty'. That is
// decision D-10's `[만화] 엔젤하트 전32권 완결.zip`, a container of 33 nested ZIPs
// with zero image entries: the *book* is legitimately `empty` (nested archives
// are out of scope, prd §7.2) but the reader cannot open a single page of the
// series, and ruling E-14 (which narrows D-10 at the series level) says such a
// series must not present as healthy — FR-IDX-010 wants the failure visible and
// design.md 화면 2 wants a reason on screen. `empty` is now reserved for "a
// series with nothing *in* it", never "a series with nothing *readable* in it".
//
// A series with at least one readable book stays 'ok' however many of its other
// volumes are broken — the badge belongs on the volume.
func seriesStatus(results []bookResult) (status, message string) {
	if len(results) == 0 {
		return StatusEmpty, "no readable books"
	}
	// A hard failure ('error'/'encrypted'/'unsupported') names a cause the
	// operator can act on, so it outranks an `empty` book's generic reason
	// however the two are ordered within the series.
	var failMsg, emptyMsg string
	for _, r := range results {
		switch r.status {
		case StatusOK:
			return StatusOK, ""
		case StatusEmpty:
			if emptyMsg == "" {
				emptyMsg = r.errMsg
			}
		default:
			if failMsg == "" {
				failMsg = r.errMsg
			}
		}
	}
	switch {
	case failMsg != "":
		message = failMsg
	case emptyMsg != "":
		message = emptyMsg
	default:
		message = "no supported image entries"
	}
	return StatusError, message
}
