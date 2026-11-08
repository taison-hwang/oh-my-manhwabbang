package rar5

import (
	"fmt"

	"shelf/internal/archive"
)

// Every error this package returns wraps exactly one of these, so callers
// classify with errors.Is (impl-plan §5.1), as with zipidx and rar4.
var (
	ErrNoSignature    = corrupt("RAR 5 signature not found")
	ErrBadHeader      = corrupt("malformed header")
	ErrTruncated      = corrupt("truncated archive")
	ErrNoMainHeader   = corrupt("main header missing")
	ErrEntryChanged   = corrupt("entry no longer matches the index")
	ErrDecode         = corrupt("packed data does not decode")
	ErrEncrypted      = &kindError{msg: "archive is password-protected", kind: archive.ErrEncrypted}
	ErrUnsupported    = &kindError{msg: "unsupported compression method", kind: archive.ErrUnsupportedMethod}
	errNotRAR5Version = fmt.Errorf("rar: %w (RAR 4 signature; this reader reads RAR 5)", ErrNoSignature)
)

type kindError struct {
	msg  string
	kind error
}

func (e *kindError) Error() string { return e.msg }
func (e *kindError) Unwrap() error { return e.kind }

func corrupt(msg string) *kindError { return &kindError{msg: msg, kind: archive.ErrCorrupt} }

// unsupported names the reason, because "unsupported" alone does not tell an
// owner whether the file is broken or merely packed in a shape this build
// declines (D-72, E-71).
func unsupported(reason string) error {
	return fmt.Errorf("rar: %w (%s)", ErrUnsupported, reason)
}
