package source

import (
	"path"
	"strings"
)

// FR-IDX-006 and FR-IDX-011, implemented once for every book kind.
//
// These rules are not theoretical. `Thumbs.db` alone accounts for 125 excluded
// entries in a 508-archive sample of the reference collection (arch §4.5), and
// `__MACOSX/` forks are pervasive in archives repacked on a Mac. An unfiltered
// page list puts a 0-byte `desktop.ini` in the middle of a comic.

// SupportedExts is FR-IDX-011's list, lowercase and with the dot. Membership
// is what makes a file a page.
//
// `.tif`/`.tiff` are deliberately absent. arch §5.5 has the thumbnailer decode
// them if it ever meets one, but FR-IDX-011 does not advertise them, so they
// are not pages.
var SupportedExts = []string{".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp", ".avif"}

var supportedExtSet = func() map[string]struct{} {
	m := make(map[string]struct{}, len(SupportedExts))
	for _, e := range SupportedExts {
		m[e] = struct{}{}
	}
	return m
}()

// SupportedExt reports whether ext (lowercase, with dot) is a page extension.
func SupportedExt(ext string) bool {
	_, ok := supportedExtSet[ext]
	return ok
}

// Ext returns the lowercase extension of a name, dot included, or "".
func Ext(name string) string { return strings.ToLower(path.Ext(name)) }

// Exclusion reasons, used verbatim in scan-log messages so an operator can see
// which rule dropped an entry.
const (
	ReasonDirectory    = "directory entry"
	ReasonResourceFork = "macos resource fork"
	ReasonHidden       = "hidden file"
	ReasonSystemFile   = "system file"
	ReasonZeroByte     = "zero-byte entry"
	ReasonExtension    = "unsupported extension"
)

// systemJunk is matched case-insensitively against the base name.
var systemJunk = []string{".ds_store", "thumbs.db", "desktop.ini"}

// Excluded reports whether a page candidate must be dropped and, if so, why.
//
// name is the decoded, slash-separated path — the full entry path inside an
// archive, or the name within a book directory. The rules are applied in the
// order arch §4.5 lists them so that the reason is the most specific one.
//
// Config-driven exclude_globs and include_globs (FR-CFG-003, amendment A-3)
// are deliberately *not* here: they apply to root-relative paths at scan time
// and belong to the scanner, which owns the config.
func Excluded(name string, size int64, isDir bool) (bool, string) {
	if isDir || name == "" || strings.HasSuffix(name, "/") {
		return true, ReasonDirectory
	}
	slashed := path.Clean(strings.ReplaceAll(name, `\`, "/"))
	base := path.Base(slashed)

	if slashed == "__MACOSX" ||
		strings.HasPrefix(slashed, "__MACOSX/") ||
		strings.Contains(slashed, "/__MACOSX/") ||
		strings.HasPrefix(base, "._") {
		return true, ReasonResourceFork
	}
	for _, junk := range systemJunk {
		if strings.EqualFold(base, junk) {
			return true, ReasonSystemFile
		}
	}
	if strings.HasPrefix(base, ".") {
		return true, ReasonHidden
	}
	// A hidden *directory* anywhere on the path hides everything under it,
	// which is what "숨김 파일" means for a nested `.git/` or `.thumbnails/`.
	for _, el := range strings.Split(slashed, "/") {
		if len(el) > 1 && el[0] == '.' {
			return true, ReasonHidden
		}
	}
	if size == 0 {
		return true, ReasonZeroByte
	}
	if !SupportedExt(Ext(base)) {
		return true, ReasonExtension
	}
	return false, ""
}

// contentTypes maps a page extension to the Content-Type served for it.
//
// arch §5.3: the type comes from this table and never from sniffing. Sniffing
// a user's file to decide what to tell the browser it is has exactly the
// failure mode you would expect.
var contentTypes = map[string]string{
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".png":  "image/png",
	".gif":  "image/gif",
	".webp": "image/webp",
	".bmp":  "image/bmp",
	".avif": "image/avif",
	".tif":  "image/tiff",
	".tiff": "image/tiff",
}

// ContentType returns the media type for a page extension, or
// application/octet-stream for anything unrecognised.
func ContentType(ext string) string {
	if ct, ok := contentTypes[strings.ToLower(ext)]; ok {
		return ct
	}
	return "application/octet-stream"
}

// nameEncoding folds the per-entry encodings kenc reported into the one value
// recorded for the book (arch §4.4). The worst outcome wins: one entry that
// needed CP949 makes the book a CP949 book, and one entry that fits neither
// encoding is the thing an operator most needs to see.
func nameEncoding(seen map[string]int) string {
	for _, enc := range []string{"unknown", "utf-8-invalid", "cp949"} {
		if seen[enc] > 0 {
			return enc
		}
	}
	return "utf-8"
}
