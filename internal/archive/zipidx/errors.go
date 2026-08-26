package zipidx

import (
	"fmt"

	"shelf/internal/archive"
)

// The sentinels below are the vocabulary arch §4.11 maps onto books.status.
// Every error this package returns wraps exactly one of them, so a caller can
// classify with errors.Is and never with string matching (impl-plan §5.1).
//
// Their messages are deliberately fragments rather than sentences: call sites
// build the operator-facing message with fmt.Errorf("zip: %w at entry %d", …),
// which produces the exact strings arch §4.11 lists — "zip: truncated central
// directory at entry 812" — without the sentinel text appearing twice.
var (
	// ErrNoEOCD — the 22-byte end-of-central-directory record was not found in
	// the last 65 557 bytes. Either the file is not a ZIP, or its tail is gone.
	ErrNoEOCD = corrupt("end of central directory not found")
	// ErrTruncatedCD — the central directory ends before the record count in
	// the end record is reached. The common shape in the real collection: an
	// interrupted download (9 of 11 157 archives, arch §4.11).
	ErrTruncatedCD = corrupt("truncated central directory")
	// ErrBadCentralHeader — a central-directory record has a wrong signature
	// or a field length that runs past the directory.
	ErrBadCentralHeader = corrupt("malformed central directory record")
	// ErrBadLocalHeader — the local file header at a stored offset does not
	// start with 0x04034b50. The index and the container disagree.
	ErrBadLocalHeader = corrupt("bad local file header")
	// ErrCDTooLarge — the end record claims a central directory larger than
	// [maxCentralDirBytes]. Reading it would breach NFR-PRF-006, so we refuse
	// rather than allocate.
	ErrCDTooLarge = corrupt("central directory is implausibly large")
	// ErrBadZIP64 — the ZIP64 locator or end record is present but malformed
	// (FR-IDX-009).
	ErrBadZIP64 = corrupt("malformed zip64 end record")
	// ErrSalvagedFromLocalHeaders — the central directory could not be read at
	// all, and the entry list was rebuilt by walking local file headers
	// instead (salvage.go). It is still corrupt: the book keeps its damaged
	// status and its scan_log row, and what changes is only that its pages are
	// listed rather than lost. 8 of this collection's 9 damaged archives open
	// this way, 733 of their 740 images intact.
	ErrSalvagedFromLocalHeaders = corrupt("central directory unreadable, rebuilt from local file headers")

	// ErrEncrypted — general-purpose bit 0. FR-IDX-010: flag it, never try to
	// decode it. This build ships no decryption and never will.
	ErrEncrypted = &kindError{msg: "archive is password-protected", kind: archive.ErrEncrypted}
	// ErrUnsupportedMethod — a compression method other than store or deflate.
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

// methodName annotates the unsupported-method error with the APPNOTE name of
// the method, because "method 14" alone tells an operator nothing.
func methodName(m uint16) string {
	switch m {
	case 1:
		return " (Shrink)"
	case 6:
		return " (Implode)"
	case 9:
		return " (Deflate64)"
	case 12:
		return " (BZIP2)"
	case 14:
		return " (LZMA)"
	case 93:
		return " (Zstandard)"
	case 95:
		return " (XZ)"
	case 96:
		return " (JPEG)"
	case 97:
		return " (WavPack)"
	case 98:
		return " (PPMd)"
	case 99:
		return " (AES)"
	default:
		return ""
	}
}

func unsupportedMethod(m uint16) error {
	return fmt.Errorf("zip: %w %d%s", ErrUnsupportedMethod, m, methodName(m))
}
