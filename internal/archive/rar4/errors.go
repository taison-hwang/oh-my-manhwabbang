package rar4

import (
	"fmt"

	"shelf/internal/archive"
)

// The sentinels below are the vocabulary arch §4.11 maps onto books.status, in
// the same shape zipidx uses: every error this package returns wraps exactly
// one of them, so a caller classifies with errors.Is and never with string
// matching (impl-plan §5.1).
//
// Messages are fragments, not sentences. Call sites build the operator-facing
// text with fmt.Errorf("rar: %w at block %d", …) so the sentinel never appears
// twice in one message.
var (
	// ErrNoSignature — the file does not begin with the 7-byte RAR4 marker.
	// Either it is not a RAR, or its head is gone.
	ErrNoSignature = corrupt("RAR signature not found")
	// ErrBadBlockHeader — a block header declares a size below the 7-byte
	// minimum, or its fields run past the end of the file. Walking further
	// would be reading noise as structure.
	ErrBadBlockHeader = corrupt("malformed block header")
	// ErrTruncated — the block chain stops before the file does, or a block
	// claims more bytes than remain. The interrupted-download shape.
	ErrTruncated = corrupt("truncated archive")
	// ErrNoMainHeader — no 0x73 main header was seen before the first file
	// block. OpenEntry needs it verbatim to splice a single-entry archive, so
	// its absence is structural, not cosmetic.
	ErrNoMainHeader = corrupt("main header missing")

	// ErrEncrypted — LHD_PASSWORD on an entry, or MHD_PASSWORD on the archive.
	// FR-IDX-010: flag it, never try to decode it. This build ships no
	// decryption and never will.
	ErrEncrypted = &kindError{msg: "archive is password-protected", kind: archive.ErrEncrypted}
	// ErrUnsupportedMethod — a container this reader is not willing to serve
	// pages from. It covers three distinct shapes, all of which would break
	// FR-SRV-002's promise that one page costs one seek; see [unsupported].
	ErrUnsupportedMethod = &kindError{msg: "unsupported compression method", kind: archive.ErrUnsupportedMethod}
)

// kindError is a sentinel that also carries the format-blind classification
// archive.StatusOf needs, so errors.Is(err, archive.ErrCorrupt) works on
// anything this package returns.
type kindError struct {
	msg  string
	kind error
}

func (e *kindError) Error() string { return e.msg }
func (e *kindError) Unwrap() error { return e.kind }

func corrupt(msg string) *kindError { return &kindError{msg: msg, kind: archive.ErrCorrupt} }

// Status maps err to the books.status value of arch §4.11.
func Status(err error) archive.Status { return archive.StatusOf(err) }

// unsupported names the reason rather than the number, because "unsupported"
// alone tells an operator nothing about whether their file is broken or merely
// packed in a shape this build declines.
//
// Solid and multi-volume are refusals, not failures. Both are readable in
// principle and neither occurs in the reference collection (0 of 14 archives,
// 0 of 2,914 entries); serving them would mean decompressing every preceding
// entry to reach page N, which is precisely the cost NFR-PRF-006 exists to
// forbid. Saying so is better than a book that opens and then stalls.
func unsupported(reason string) error {
	return fmt.Errorf("rar: %w (%s)", ErrUnsupportedMethod, reason)
}

// methodName annotates an unsupported packing method with its RAR name.
func methodName(m uint16) string {
	switch m {
	case MethodStore:
		return "store"
	case 0x31:
		return "fastest"
	case 0x32:
		return "fast"
	case 0x33:
		return "normal"
	case 0x34:
		return "good"
	case 0x35:
		return "best"
	default:
		return fmt.Sprintf("0x%02x", m)
	}
}
