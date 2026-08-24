package hv3

import (
	"bytes"
	"fmt"

	"shelf/internal/archive"
)

// The sentinels below are the vocabulary arch §4.11 maps onto books.status, in
// the same shape zipidx and rar4 use: every error this package returns wraps
// exactly one of them, so a caller classifies with errors.Is and never with
// string matching (impl-plan §5.1).
//
// Messages are fragments, not sentences. Call sites build the operator-facing
// text with fmt.Errorf("hv3: %w at 0x%X", …) so the sentinel never appears
// twice in one message.
var (
	// ErrNoSignature — the file does not begin with `HV30`. Either it is not
	// an HV3 container, or its head is gone.
	ErrNoSignature = corrupt("HV3 signature not found")
	// ErrNoList — the LIST chunk was not found in the header window, or every
	// candidate failed validation. HV3 keeps its directory at the *front* of
	// the file, so this is the analogue of a ZIP with no end-of-central-
	// directory record: there is nothing to enumerate.
	ErrNoList = corrupt("LIST chunk not found")
	// ErrBadRecord — a FINF record or one of its fields declares a length that
	// runs past the end of the directory, or omits a field page serving cannot
	// do without. Walking further would be reading noise as structure.
	ErrBadRecord = corrupt("malformed FINF record")
	// ErrBadFileBlock — the FILE block a record points at is missing, or it
	// declares a length the record disagrees with. Serving it would hand a
	// client somebody else's bytes.
	ErrBadFileBlock = corrupt("malformed FILE block")
	// ErrTruncated — a record points past the end of the file. The
	// interrupted-download shape.
	ErrTruncated = corrupt("truncated container")

	// ErrUnsupportedMethod — an ENCR mode this reader will not serve pages
	// from; see [unsupportedMode].
	ErrUnsupportedMethod = &kindError{msg: "unsupported ENCR mode", kind: archive.ErrUnsupportedMethod}
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

// magics names what a file that is not an HV3 actually is, from its first
// bytes.
//
// # Why a reader carries a table of other formats' signatures
//
// `.hv3` is not a reliable claim in this collection. Measured 2026-08-24 over
// every `.hv3` on the machine: **54 of 55 are RAR archives** wearing the
// extension — the whole `궁` series, `Rar!\x1a\x07\x00` in every one — and the
// 55th, `펌프킨 시저스 04`, is the only genuine HV3 there is. The 54 are in the
// trash and none of them is in the library, so nothing here dispatches on the
// signature or second-guesses [source.ContainerKind]; that would be a rule
// invented for files that do not exist.
//
// What it does do is refuse to tell the wrong story. `HV3 signature not found`
// on a perfectly good RAR sends its owner looking for damage, which is the
// exact failure D-72 was raised about in the other direction. Naming the
// format costs one comparison against bytes that have already been read.
var magics = []struct {
	prefix []byte
	format string
}{
	{[]byte("Rar!\x1a\x07\x00"), "a RAR 4.x archive"},
	{[]byte("Rar!\x1a\x07\x01"), "a RAR 5 archive"},
	{[]byte("PK\x03\x04"), "a ZIP archive"},
	{[]byte("PK\x05\x06"), "an empty ZIP archive"},
	{[]byte("7z\xbc\xaf\x27\x1c"), "a 7-Zip archive"},
	{[]byte("%PDF-"), "a PDF"},
	{[]byte("\xff\xd8\xff"), "a JPEG image"},
	{[]byte("\x89PNG\r\n\x1a\n"), "a PNG image"},
	{[]byte("EGGA"), "an EGG archive"},
	{[]byte("ALZ\x01"), "an ALZ archive"},
}

// notHV3 is [ErrNoSignature] with the format the bytes actually name, when
// they name one this build has heard of.
func notHV3(head []byte) error {
	for _, m := range magics {
		if bytes.HasPrefix(head, m.prefix) {
			return fmt.Errorf("hv3: %w — the file is named .hv3 but is %s", ErrNoSignature, m.format)
		}
	}
	return fmt.Errorf("hv3: %w", ErrNoSignature)
}

// unsupportedMode names the ENCR value rather than swallowing it, because an
// operator holding a file this build declines needs to know whether they are
// looking at damage or at a shape nobody has decoded yet.
//
// Only two modes are known from measurement: 0 (the bytes are stored plainly)
// and 2 (the byte-position mask of [unmask]). A third value is not assumed to
// be either — it is refused, and the number goes in books.error so the next
// person to meet one has somewhere to start.
//
// It is deliberately NOT [archive.ErrEncrypted]. That sentinel means "there is
// a password and this build ships no decryption", which is a statement about
// the file; an unknown ENCR mode is a statement about this reader.
func unsupportedMode(mode uint32) error {
	return fmt.Errorf("hv3: %w %d", ErrUnsupportedMethod, mode)
}
