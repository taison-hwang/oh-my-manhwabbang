// Package ids derives every opaque identifier the product is keyed by
// (FR-CFG-004, FR-STT-003, AC-006).
//
// An id is a pure function of two values that live *outside* the index: the
// root's logical name, which comes from the config file, and the item's
// root-relative path, which comes from the filesystem. No database row id and
// no absolute path is ever an input. Three consequences follow, and they are
// the whole reason the scheme exists:
//
//   - Rebuilding index.db reproduces every id byte-identically, so the progress
//     rows in user.db (a physically separate file, NFR-DAT-004) still join.
//   - Moving a root to a different physical path changes nothing.
//   - Renaming a root in the YAML *is* an identity change and orphans that
//     root's progress. That is intended, documented behaviour — see the WARNING
//     block above `roots:` in shelf.example.yaml.
//
// # The hash input is a compatibility surface
//
// Item ids are the exact hash input of arch-backend §3.4, spelled out there
// "because it is a compatibility surface":
//
//	"shelf-id/1" | 0x00 | "series" | 0x00 | <root name> | 0x00 | <root-relative slash path>
//	"shelf-id/1" | 0x00 | "book"   | 0x00 | <root name> | 0x00 | <root-relative slash path>
//
// SHA-256, first 10 bytes, base32(abcdefghijklmnopqrstuvwxyz234567) with no
// padding -> 16 chars. IDVersion is the leading field and is also what WP-03
// writes into meta.id_version in both databases (arch §3.5, §3.6): a future
// change to this construction must bump it, and startup then refuses an
// unknown value rather than silently orphaning progress (arch §11 step 3).
//
// # A documented conflict in the source material, and how it is resolved
//
// arch §3.4 prints the construction above twice — once as Go, once as a byte
// diagram — and then quotes a "worked example (VERIFIED)":
//
//	SeriesID("mangga", "[만화] 군계 1~25")                   = gzj75n6x7rir6but
//	BookID  ("mangga", "[만화] 군계 1~25/군계(軍鷄) 01권.zip") = ox74tfcrwwnfopch
//
// Those two strings are NOT what §3.4's own code produces. They are
// SHA-256(root | 0x00 | rel) with both tags dropped; §3.4's code yields
// ruzwlotzngls2ua5 and yvtfrny77ehkt2we, which is what this package returns and
// what its golden test recomputes from the literal spec string. The example
// therefore contradicts the section it illustrates, and one of the two has to
// lose.
//
// The tagged construction wins, on document precedence:
//
//   - decisions.md D-14 (precedence 2, above impl-plan and arch) states the
//     scheme as SHA-256(domain | root name | root-relative path). Nothing at
//     precedence 2 or above mentions the example strings.
//   - impl-plan §3 WP-02 acceptance 1 opens with "the exact hash input string
//     of arch §3.4" and closes with "series and book domains never collide for
//     the same rel path". Two of its three clauses require the domain tag; only
//     the quoted values do not.
//   - arch §10.1's ids test row and impl-plan §6.1 both list domain separation
//     as a thing the tests must assert.
//
// Dropping the tags to match the example would make SeriesID == BookID for
// every input, which for the 291 top-level ZIPs and 12 nested PDFs of arch §4.2
// ("series == its own single book") means one id for two entities, and would
// leave meta.id_version describing nothing. Changing the derivation later is a
// user.db migration; changing it now, in wave 1 with no user.db on disk
// anywhere, costs two constants.
//
// Two documents still record the losing side and are outside this package's
// ownership: arch §3.4's "*Worked example* (VERIFIED)" block and the same
// strings quoted in arch §10.1 and impl-plan §3 WP-02 acceptance 1. They need
// the orchestrator's correction to the values this package produces, which
// TestIDs_hashInput_isTheArchSpecString derives from the spec string in situ.
//
// Cache keys (ThumbKey, PDFPageKey) carry their own domain, exactly as arch
// §5.6 specifies; nothing contradicts that section.
package ids

import (
	"crypto/sha256"
	"encoding/base32"
	"path"
	"strconv"
	"strings"
)

// IDVersion is the first field of every item-id hash input (arch §3.4) and the
// value WP-03 stores in meta.id_version in index.db and user.db (arch §3.5,
// §3.6). Startup compares the stored value against this constant and refuses to
// run on a mismatch rather than join progress rows onto ids computed by a
// different scheme (arch §11 step 3), which is only possible because the tag is
// inside the hash: bumping it necessarily changes every id.
const IDVersion = "shelf-id/1"

// Item-id domain tags, the second field of an item-id hash input (arch §3.4).
// They are what keeps a single-file series — arch §4.2's "a single ZIP file |
// 291 | series == its own single book" — from colliding with its own only book,
// since both are the same (root name, rel path) pair.
const (
	domainSeries = "series"
	domainBook   = "book"
)

// Cache-key domain tags, the first field of a cache-key hash input (arch §5.6).
// Changing either string invalidates that whole cache — which is exactly how
// D-18/D-19 want a format change to behave, but it is never free.
const (
	thumbVersion   = "shelf-thumb/1"
	pdfPageVersion = "shelf-pdfpage/1"
)

// Length is the character count of every id and cache key: 80 bits of SHA-256
// rendered in base32 is exactly 16 characters with no padding. At 10^6 items
// the birthday collision probability is ~4e-13, which the UNIQUE constraints in
// arch §3.5 turn into a loud scan error rather than silent corruption.
const Length = 16

// Alphabet is RFC 4648 base32 lowercased. Lowercase keeps ids safe on
// case-insensitive filesystems (the thumbnail cache fans out on the first four
// characters) and free of URL escaping.
const Alphabet = "abcdefghijklmnopqrstuvwxyz234567"

// separator joins the hash-input fields. NUL cannot occur in a path component
// on any supported OS, nor in a root name (config validates [a-zA-Z0-9._-]),
// so it is an unambiguous separator: no two distinct field tuples can produce
// the same input string, and ("manga", "x/y") cannot collide with
// ("mangax", "/y").
const separator = "\x00"

var enc = base32.NewEncoding(Alphabet).WithPadding(base32.NoPadding)

// SeriesID returns the identifier of the series at relPath inside the root
// named rootName (arch §3.4).
//
// relPath is the path of the series directory or file relative to the root
// directory, not relative to anything else. Separators may be `/` or `\`; the
// path is normalised before hashing, so both spellings of one path agree.
func SeriesID(rootName, relPath string) string {
	return derive(domainSeries, rootName, relPath)
}

// BookID returns the identifier of the book at relPath inside the root named
// rootName (arch §3.4).
//
// relPath is relative to the ROOT, not to the series, so two books with the
// same file name in different series never collide. A single-file series passes
// the same relPath to both SeriesID and BookID; the domain tag is what keeps
// the two ids apart.
func BookID(rootName, relPath string) string {
	return derive(domainBook, rootName, relPath)
}

// NestedBookID returns the identifier of one volume inside a container of
// volumes: relPath names the container, innerPath the entry within it.
//
// An empty innerPath is the ordinary case and gives exactly [BookID], so a book
// that is its own file keeps the id it has always had — reading progress for
// the 11,340 books already in the index survives this scheme gaining a field
// (the id is what progress is keyed by, arch §3.4).
func NestedBookID(rootName, relPath, innerPath string) string {
	if innerPath == "" {
		return BookID(rootName, relPath)
	}
	return hash(IDVersion, domainBook, rootName, NormalizeRel(relPath), innerPath)
}

// derive implements the item-id hash input of arch §3.4: the version tag, the
// domain tag, the root name and the normalised root-relative path,
// NUL-separated.
func derive(domain, rootName, relPath string) string {
	return hash(IDVersion, domain, rootName, NormalizeRel(relPath))
}

// ThumbKey returns the cache key of one rendered thumbnail (arch §5.6). The
// caller stores it at <cache_dir>/thumbs/<k[0:2]>/<k[2:4]>/<k>.jpg.
//
// contentVersion is books.content_version, derived from the source file's size
// and mtime. Including it is what makes FR-THM-006 structural: a changed source
// file yields a different key and therefore a different path, so there is no
// invalidation code that can be forgotten (D-19). format and quality are inputs
// for the same reason (D-18): switching encoder later is a pure
// cache-invalidation event with no migration.
func ThumbKey(bookID string, pageNo, width int, format string, quality int, contentVersion string) string {
	return cacheKey(thumbVersion, bookID, pageNo, width, format, quality, contentVersion)
}

// PDFPageKey returns the cache key of one rasterised PDF page (arch §5.6). It
// is the identical scheme under a different domain, so a thumbnail and a
// full-size render of the same page at the same width never share a file.
func PDFPageKey(bookID string, pageNo, width int, format string, quality int, contentVersion string) string {
	return cacheKey(pdfPageVersion, bookID, pageNo, width, format, quality, contentVersion)
}

// cacheKey implements the exact hash input of arch §5.6:
//
//	<domain> | 0x00 | <book_id> | 0x00 | <page_no> | 0x00 | <width>
//	         | 0x00 | <format>  | 0x00 | <quality> | 0x00 | <content_version>
func cacheKey(domain, bookID string, pageNo, width int, format string, quality int, contentVersion string) string {
	return hash(domain, bookID,
		strconv.Itoa(pageNo), strconv.Itoa(width),
		format, strconv.Itoa(quality), contentVersion)
}

// hash joins fields with separator, takes SHA-256, keeps the first 10 bytes and
// encodes them.
func hash(fields ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(fields, separator)))
	return enc.EncodeToString(sum[:10])
}

// NormalizeRel puts a root-relative path into the one canonical spelling that
// is hashed: forward slashes, cleaned, no leading slash, and "" for the root
// itself.
//
// Windows hands out `\`-separated paths from filepath.Rel, so without this a
// library indexed on Windows and one indexed on Linux would disagree about
// every id for the same files on the same shared volume.
func NormalizeRel(relPath string) string {
	s := strings.ReplaceAll(relPath, `\`, "/")
	s = path.Clean(s)
	s = strings.TrimPrefix(s, "/")
	if s == "." {
		return ""
	}
	return s
}

// Valid reports whether s is syntactically an id: exactly Length characters
// drawn from Alphabet.
//
// The HTTP layer uses it to separate a malformed id (400 bad_request) from a
// well-formed but unknown one (404 not_found) per arch §7.1, and it is the
// first of the four path-traversal layers of D-21: an id that cannot contain
// `/`, `.` or a drive letter cannot express a path at all.
func Valid(s string) bool {
	if len(s) != Length {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < '2' || c > '7') {
			return false
		}
	}
	return true
}
