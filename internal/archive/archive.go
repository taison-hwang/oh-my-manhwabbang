// Package archive is the format-blind seam between the indexer/server and the
// container formats a book can live in.
//
// prd §7.2 puts RAR/CBR and 7z out of scope but requires the archive reader to
// stay an interface so a later format can be added without touching the
// scanner or the HTTP layer (decision D-07). That interface is [Reader]; the
// only implementation in this build is internal/archive/zipidx.
//
// Two rules shape every type here and are not negotiable:
//
//   - FR-IDX-002 — indexing reads the container's directory only. No entry
//     payload is ever decompressed while building an [Index].
//   - FR-SRV-002 / NFR-PRF-006 — serving one page seeks straight to a stored
//     offset. Nothing in this package accepts a []byte archive or an
//     io.Reader; everything is io.ReaderAt, which is pread(2) on POSIX and an
//     explicit-offset ReadFile on Windows. There is no shared seek cursor, so
//     concurrent reads of one container need no lock and no handle duplication
//     (CON-004).
package archive

import (
	"context"
	"errors"
	"io"
	"time"
)

// Compression methods. Only these two occur in the target collection; anything
// else is reported as [ErrUnsupportedMethod] rather than guessed at.
const (
	MethodStore   uint16 = 0
	MethodDeflate uint16 = 8
)

// General-purpose bit flags a ZIP entry can carry that this product reasons
// about (APPNOTE §4.4.4).
const (
	// FlagEncrypted is bit 0. FR-IDX-010: a book holding one of these is
	// flagged, never decoded.
	FlagEncrypted uint16 = 1 << 0
	// FlagDataDescriptor is bit 3: sizes and CRC live after the payload rather
	// than in the local header. The central directory always has the real
	// values, which is the only place we read them from.
	FlagDataDescriptor uint16 = 1 << 3
	// FlagUTF8 is bit 11, the "language encoding" flag of FR-IDX-008. It is
	// handed to kenc.DecodeEntryName together with the raw name bytes; nothing
	// in this package decodes a name itself.
	FlagUTF8 uint16 = 1 << 11
)

// Status is the value the caller stores in books.status (arch §4.11). Failures
// are per-book states, never scan aborts (FR-IDX-010).
type Status string

const (
	StatusOK          Status = "ok"
	StatusError       Status = "error"
	StatusEncrypted   Status = "encrypted"
	StatusEmpty       Status = "empty"
	StatusUnsupported Status = "unsupported"
)

// Sentinels every format implementation wraps, so that a caller can classify a
// failure without knowing which format produced it. Compare with errors.Is.
var (
	// ErrCorrupt covers every structural failure: a missing end record, a
	// truncated directory, a malformed header.
	ErrCorrupt = errors.New("archive is corrupt")
	// ErrEncrypted means at least one entry is password-protected.
	ErrEncrypted = errors.New("archive is password-protected")
	// ErrUnsupportedMethod means an entry uses a compression method this build
	// cannot stream.
	ErrUnsupportedMethod = errors.New("unsupported compression method")
)

// StatusOf maps a failure to the books.status value of arch §4.11. A nil error
// is StatusOK; anything unrecognised is StatusError, because "we do not know
// what went wrong" must never read as "fine".
func StatusOf(err error) Status {
	switch {
	case err == nil:
		return StatusOK
	case errors.Is(err, ErrEncrypted):
		return StatusEncrypted
	case errors.Is(err, ErrUnsupportedMethod):
		return StatusUnsupported
	default:
		return StatusError
	}
}

// Entry is one member of a container as recorded by a directory-only read.
//
// Name is already decoded; RawName and Flags are kept because they are the
// evidence for that decoding and the scanner records NameEncoding per book so
// the UI can surface it (arch §4.4).
type Entry struct {
	Name         string // decoded display name, always valid UTF-8
	RawName      []byte // the bytes as stored, undecoded
	NameEncoding string // "utf-8" | "utf-8-invalid" | "cp949" | "unknown"

	Flags  uint16
	Method uint16
	CRC32  uint32

	CompSize int64 // bytes on disk, after compression
	Size     int64 // bytes after decompression

	// LocalHdrOff is the absolute offset of the entry's local file header in
	// the container, base-offset corrected. This is the field FR-SRV-002 is
	// about and the field archive/zip does not expose, which is the whole
	// reason zipidx exists (decision E-2).
	LocalHdrOff int64

	Modified time.Time

	Dir       bool // a directory entry: excluded from pages by FR-IDX-006
	Encrypted bool // general-purpose bit 0
}

// Ref reduces an entry to the columns the index persists and page serving
// needs. Everything else is display metadata.
func (e Entry) Ref() EntryRef {
	return EntryRef{
		LocalHdrOff: e.LocalHdrOff,
		CompSize:    e.CompSize,
		Size:        e.Size,
		Method:      e.Method,
		CRC32:       e.CRC32,
	}
}

// EntryRef is the minimum needed to stream one entry back out: exactly the
// pages(local_hdr_off, comp_size, size, method, crc32) columns of arch §3.5.
//
// It exists so that serving a page never needs the directory again. A page
// request costs one 30-byte header read plus the entry's own bytes, whatever
// the size of the container (AC-008).
type EntryRef struct {
	LocalHdrOff int64
	CompSize    int64
	Size        int64
	Method      uint16
	CRC32       uint32
}

// Index is the result of reading a container's directory.
//
// Entries may be non-empty alongside a non-nil error from [Reader.ReadIndex]:
// a directory that goes bad at record 812 still yields 811 usable pages, and
// FR-IDX-010 asks us to keep them rather than lose the book.
type Index struct {
	Entries []Entry
	Comment string
	// ZIP64 reports that the container used 64-bit end records or extra
	// fields (FR-IDX-009).
	ZIP64 bool
	// BaseOffset is the byte position at which the container proper starts.
	// It is non-zero for self-extracting archives whose recorded offsets are
	// relative to the payload rather than to the file. Entry.LocalHdrOff
	// already includes it.
	BaseOffset int64
}

// Encrypted reports whether any entry carries general-purpose bit 0, which
// makes the whole book books.status='encrypted' (FR-IDX-010).
func (ix *Index) Encrypted() bool {
	for i := range ix.Entries {
		if ix.Entries[i].Encrypted {
			return true
		}
	}
	return false
}

// Reader indexes and streams one container format.
//
// Implementations must be safe for concurrent use and must hold no per-file
// state: the io.ReaderAt is supplied per call by the caller's handle pool, so
// one Reader value serves the whole process.
type Reader interface {
	// Format is the short lowercase name of the container format, e.g. "zip".
	Format() string

	// ReadIndex reads the container's directory and nothing else. It must
	// never decompress an entry payload (FR-IDX-002) and must never buffer
	// the whole container (NFR-PRF-006).
	//
	// A structural failure returns whatever entries were parsed before it,
	// plus an error whose Status classifies the book.
	ReadIndex(ctx context.Context, r io.ReaderAt, size int64) (*Index, error)

	// OpenEntry streams one entry's decompressed bytes. The returned
	// io.ReadCloser also implements io.ReadSeeker when the entry is stored
	// uncompressed, which is what lets the HTTP layer answer Range requests
	// (arch §5.3); callers detect that with a type assertion.
	OpenEntry(ctx context.Context, r io.ReaderAt, ref EntryRef) (io.ReadCloser, error)
}
