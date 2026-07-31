package scanner

import (
	"path"
	"strings"

	"shelf/internal/source"
)

// FR-IDX-006 as it applies to *filesystem* children during classification, plus
// the two config-driven glob lists (FR-CFG-003, amendment A-3).
//
// The page-level half of FR-IDX-006 already lives in internal/source.Excluded
// and is applied identically to a ZIP entry and to a file on disk. What is left
// here is the part source deliberately does not own:
//
//   - the same junk rules applied to a *candidate book or series* rather than
//     to a page, where the extension test must not fire (a `.zip` is not an
//     image, yet it is exactly what we are looking for);
//   - `scan.exclude_globs`, matched against the root-relative slash path;
//   - `scan.include_globs` (A-3, ruling E-6), matched against the base name of
//     a root's direct child, before classification.

// Reasons, used verbatim in scan-log messages so an operator can see which rule
// dropped an entry.
const (
	reasonHidden        = "hidden file"
	reasonResourceFork  = "macos resource fork"
	reasonSystemFile    = "system file"
	reasonSymlink       = "symlink and scan.follow_symlinks is false"
	reasonIrregular     = "not a regular file or directory"
	reasonExcludeGlob   = "matched scan.exclude_globs"
	reasonNotIncluded   = "did not match scan.include_globs"
	reasonNotAContainer = "not an archive, a PDF or a supported image"
)

// systemJunk is matched case-insensitively against the base name. `Thumbs.db`
// alone accounts for 125 excluded entries in a 508-archive sample of the
// reference collection (arch §4.5); these rules are load-bearing.
var systemJunk = [...]string{".ds_store", "thumbs.db", "desktop.ini"}

// ignoredChild applies the name-only rules of FR-IDX-006 to one directory
// child. It is deliberately silent about size and extension: a 0-byte `.zip` is
// a *book* that will be recorded with status='error' (arch §4.11 lists
// `D.N.Angel 08권.zip` (0 bytes) as one of the nine real failures), not an entry
// to drop, and the extension test only makes sense for pages.
func ignoredChild(name string) (bool, string) {
	if name == "__MACOSX" || strings.HasPrefix(name, "._") {
		return true, reasonResourceFork
	}
	for _, junk := range systemJunk {
		if strings.EqualFold(name, junk) {
			return true, reasonSystemFile
		}
	}
	if strings.HasPrefix(name, ".") {
		return true, reasonHidden
	}
	return false, ""
}

// Container extensions. arch §4.2 names `.zip` and `.cbz`; RAR/CBR and 7z are
// out of scope (prd §7.2, decision D-07) and fall through to "ignored" with one
// info-level scan-log row rather than to an error.
func isArchiveName(name string) bool {
	switch source.Ext(name) {
	case ".zip", ".cbz":
		return true
	}
	return false
}

func isPDFName(name string) bool { return source.Ext(name) == ".pdf" }

// isPageCandidate reports whether a loose file could be a page: one of
// FR-IDX-011's seven extensions, non-empty, and not junk. It is the same
// predicate internal/source applies to an archive entry, so a `Thumbs.db` next
// to a JPEG is dropped by the identical rule whichever side of a container it
// is on.
func isPageCandidate(name string, size int64) bool {
	drop, _ := source.Excluded(name, size, false)
	return !drop
}

// globSet is one of the two configured pattern lists.
type globSet []string

func (g globSet) empty() bool { return len(g) == 0 }

// matchPath reports whether any pattern matches a root-relative slash path.
// An empty set matches nothing — callers must special-case "empty means
// everything" where that is the documented behaviour (include_globs).
func (g globSet) matchPath(rel string) bool {
	for _, pattern := range g {
		if matchGlob(pattern, rel) {
			return true
		}
	}
	return false
}

// matchBase is A-3's rule: only a root's direct children whose *base name*
// matches at least one pattern become series. An empty list means "scan
// everything", which is the default and the behaviour when the key is absent.
func (g globSet) matchBase(base string) bool {
	if g.empty() {
		return true
	}
	for _, pattern := range g {
		if ok, err := path.Match(pattern, base); err == nil && ok {
			return true
		}
	}
	return false
}

// matchGlob is path.Match extended with `**`, which matches zero or more whole
// path segments.
//
// The extension is not decoration: arch §3.2 documents `exclude_globs` with the
// examples `["**/*.part", "**/@eaDir/**"]`, and plain path.Match cannot express
// either of them because `*` never crosses a `/`. Patterns without `**` are
// handed straight to path.Match, so config validation (which probes every
// pattern with path.Match) stays exactly as strict as it is today.
func matchGlob(pattern, name string) bool {
	if !strings.Contains(pattern, "**") {
		ok, err := path.Match(pattern, name)
		return err == nil && ok
	}
	return matchSegments(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

func matchSegments(pattern, segments []string) bool {
	for len(pattern) > 0 {
		if pattern[0] == "**" {
			if len(pattern) == 1 {
				return true
			}
			for i := 0; i <= len(segments); i++ {
				if matchSegments(pattern[1:], segments[i:]) {
					return true
				}
			}
			return false
		}
		if len(segments) == 0 {
			return false
		}
		ok, err := path.Match(pattern[0], segments[0])
		if err != nil || !ok {
			return false
		}
		pattern, segments = pattern[1:], segments[1:]
	}
	return len(segments) == 0
}
