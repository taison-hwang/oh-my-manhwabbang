package scanner

import (
	"encoding/hex"
	"hash/fnv"
	"strconv"

	"shelf/internal/index"
	"shelf/internal/source"
)

// FR-IDX-003, and with it NFR-PRF-004 (a no-change rescan of 1 000 series in
// under 30 s against a full cold scan measured at 32.3 s for 11 157 archives).
//
// The whole of the speed-up is that an unchanged container is never opened: the
// skip decision costs the `stat` that the directory listing already performed,
// so a no-change rescan degenerates into one `os.ReadDir` per directory plus a
// generation stamp. Nothing seeks into an archive, nothing decompresses
// (FR-IDX-002 would forbid the second even if we wanted it).

// fingerprintChildren is the FNV-1a-64 digest of a directory's direct children,
// rendered as 16 hex characters — the books.dir_fingerprint column of arch §3.5
// and the directory half of arch §4.6.
//
// It exists because `stat` on a directory does not move when a nested file is
// rewritten in place, so (size, mtime) cannot answer "did this book change?" for
// kind='dir'. The tuples are (name, size, mtime, isDir) over the *natural-sorted*
// children, which readDir has already ordered.
//
// Every child is hashed, including the ones FR-IDX-006 excludes. That is
// deliberate and it is the safe direction of the trade: a new `Thumbs.db` costs
// one needless re-enumeration of a directory we have already listed, while
// leaving an excluded child out would let `mv page.jpg .page.jpg` produce a page
// list that no longer matches the disk.
func fingerprintChildren(children []childInfo) string {
	h := fnv.New64a()
	buf := make([]byte, 0, 64)
	for _, c := range children {
		buf = buf[:0]
		buf = append(buf, c.name...)
		buf = append(buf, 0)
		buf = strconv.AppendInt(buf, c.size, 10)
		buf = append(buf, 0)
		buf = strconv.AppendInt(buf, c.mtime, 10)
		buf = append(buf, 0)
		if c.isDir {
			buf = append(buf, 'd')
		} else {
			buf = append(buf, 'f')
		}
		buf = append(buf, 0)
		_, _ = h.Write(buf)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// contentVersion is books.content_version: 16 hex characters derived from the
// container's (size, mtime), exactly as arch §5.6 requires.
//
// This is what makes FR-THM-006 structural rather than procedural. The value is
// an input to every thumbnail cache key (ids.ThumbKey) and rides in every page
// URL as `?v=` (D-17), so a source file whose size or mtime moved yields a
// different key, a different cache path and a `409 stale_version` on a page
// request that still carries the old one. There is no invalidation code that can
// be forgotten, because there is no invalidation code.
//
// A directory book has no meaningful container size or mtime, so it uses its
// fingerprint — which is the same 64-bit digest over the thing that actually
// determines its content.
func contentVersion(kind source.Kind, size, mtime int64, fingerprint string) string {
	if kind == source.KindDir {
		if fingerprint != "" {
			return fingerprint
		}
	}
	h := fnv.New64a()
	buf := make([]byte, 0, 48)
	buf = strconv.AppendInt(buf, size, 10)
	buf = append(buf, ':')
	buf = strconv.AppendInt(buf, mtime, 10)
	_, _ = h.Write(buf)
	return hex.EncodeToString(h.Sum(nil))
}

// unchanged decides whether a book can be skipped (arch §4.6).
//
//	status != 'ok'                              -> never skip, re-examine
//	archive / PDF   (size, mtime) both equal    -> skip, never open the file
//	directory       fingerprint equal           -> skip, never re-enumerate
//	full: true                                  -> never skip
//
// The first line is ruling E-39 (draft) and it widens arch §4.6, which skipped
// on (size, mtime) alone whatever the recorded status was.
//
// The rule it replaces already carried one exception — 'unsupported' — for the
// right reason: that status means "this *build* cannot read it" (a PDF under
// `-tags nopdf`, arch §4.11), which is a property of the binary and not of the
// file, so a differently-built binary must be free to reach a different answer.
// The exception was simply drawn too narrowly. 'empty' and 'error' are not
// reliably properties of the file either: a listing taken from a handle the pool
// had open on a since-replaced inode, a transient read failure, a book abandoned
// by a scan that raced a copy — every one of them writes a verdict that the
// bytes on disk do not support, and every one of them is then skipped for ever,
// because the (size, mtime) it was recorded with are the file's real ones. That
// is how `궁 24.zip` stayed `비어 있음` after it was repaired.
//
// So the skip is now what it always meant: an optimisation for books we have
// already read *successfully*. A book whose recorded answer is a failure is
// re-derived, never remembered.
//
// The cost is one open plus one central-directory read per non-ok book per scan
// — exactly the cost the 'unsupported' exception already accepted, and bounded
// by a quantity that is small in any healthy library (57 of 11,261 books in the
// real collection, 0.5%, none of which reads an entry payload: FR-IDX-002).
func unchanged(u bookUnit, prior index.Book, full bool) bool {
	if full {
		return false
	}
	if prior.Status != StatusOK {
		return false
	}
	if u.kind == source.KindDir {
		return prior.DirFingerprint != "" && prior.DirFingerprint == u.fingerprint
	}
	return prior.FileSize == u.size && prior.FileMtime == u.mtime
}
