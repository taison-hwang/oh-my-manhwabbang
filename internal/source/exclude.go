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

// foreignFormats are container formats this build recognises by name and
// cannot open. They are still excluded as entries — they are not pages — but a
// book whose entire contents are one of these is not empty, it is unreadable,
// and those are different sentences to put in front of a reader.
//
// Ruling E-14 settled that `비둘기.zip`, which holds one directory entry and
// nothing else, is honestly `비어 있음`. A book that is nothing but a `.7z` is
// not that: telling its owner there are "no supported image entries" describes
// a file that does not exist.
//
// The value is what the reader is told. It names the format, not a remedy,
// because for everything left in this table there is no remedy in this build.
//
// `.hv3` used to head the list and is the reason D-72 exists. It is gone from
// it: ruling E-51 found that the header reading behind "encrypted, nothing
// recovers that" was wrong twice over, and internal/archive/hv3 now serves the
// container's 104 pages. A format leaves this table by acquiring a reader —
// which is the only way anything should ever leave it.
var foreignFormats = map[string]string{
	".7z":  "7-Zip",
	".alz": "ALZ",
	".egg": "EGG",
	".lzh": "LZH",
	".tar": "TAR",
	".gz":  "gzip",
	".iso": "ISO",
}

// ForeignFormat names the container format of an entry this build recognises
// but cannot open, or "" for anything else — including `.zip` and `.rar`,
// which have readers, and `.txt`, which is not a container at all.
func ForeignFormat(name string) string { return foreignFormats[Ext(name)] }

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
	// FR-IDX-006 요구사항 "숨김 파일", narrowed to what it can mean inside an
	// archive. A ZIP entry has no hidden attribute; a leading dot is a leading
	// dot, and whether it means "hide me" depends on a filesystem convention
	// the entry was never subject to. Every artefact the convention is really
	// aimed at is already caught above by a rule that names it — `._*` forks,
	// `.DS_Store` — so what is left here is a name that merely starts with a
	// dot.
	//
	// In this collection that is `엽기인 Girl 스나코 26권.zip`, whose 80 pages
	// are all called `.▶스나코_26권◀_Scan11192010_193728.jpg`. The rule dropped
	// every one of them and the book indexed as `비어 있음` with no pages at all
	// — FR-IDX-006 costing a whole volume to hide nothing.
	//
	// So a dot-name that *is* a page is kept, and a dot-name that is not is
	// still dropped. Measured over all 11,196 indexed ZIPs, this changes what
	// is listed for exactly that one book: it is the only one with a
	// dot-prefixed image entry, and there is no archive anywhere in the
	// collection with an image under a dot-prefixed directory.
	if strings.HasPrefix(base, ".") {
		if stem := strings.TrimSuffix(base, Ext(base)); stem == "" || stem == "." || !SupportedExt(Ext(base)) {
			return true, ReasonHidden
		}
	}
	// A hidden *directory* is a different claim, and it is left alone: `.git/`
	// or `.thumbnails/` is a tool's working directory, not a volume someone
	// named oddly, and hiding everything beneath it is what the convention is
	// for. Nothing in the collection is affected either way.
	for _, el := range strings.Split(path.Dir(slashed), "/") {
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
// `cp932` and `cp949` cannot both appear — the archive-level decision in
// zipidx.resolveArchiveNames rewrites every legacy name to the one encoding it
// settled on — but the fold is total anyway rather than relying on that.
func nameEncoding(seen map[string]int) string {
	for _, enc := range []string{"unknown", "utf-8-invalid", "cp932", "cp949"} {
		if seen[enc] > 0 {
			return enc
		}
	}
	return "utf-8"
}
