// Package kenc decodes ZIP entry names into Go strings (FR-IDX-008, AC-002).
//
// This is a first-class MVP path, not a fallback: every one of the 14,630
// flagless non-ASCII entry names measured across the target collection is
// CP949, and without this package none of them displays as Korean (D-24,
// data-survey §3).
//
// # Why the decoder's error return is useless
//
// golang.org/x/text/encoding/korean.EUCKR.NewDecoder() NEVER returns an error.
// It silently substitutes U+FFFD for bytes it cannot map — VERIFIED in the
// arch spike, where transform.String returned err=<nil> while producing
// "��.jpg" for "\xff\xfe.jpg". A "try EUC-KR and check err" decoder
// therefore accepts everything and reports success on pure garbage. The only
// usable signal is the *content* of the output, so this package inspects the
// decoded runes and TestEUCKRDecoder_onGarbage_stillReturnsNilError guards the
// assumption so the content check cannot be deleted later as dead code.
//
// # Why the valid-UTF-8 probe comes before CP949
//
// A UTF-8 Korean name pushed through the CP949 decoder yields plausible-looking
// mojibake rather than an error ("한글.jpg" decodes to "?쒓?.jpg"), so guessing
// CP949 whenever bit 11 is clear corrupts every archive written by a modern
// tool that omits the flag. The probe costs nothing on this collection — 0 of
// the 14,630 flagless non-ASCII names are valid UTF-8 — and it is what makes
// the rule safe for archives the collection does not contain (arch §4.4).
//
// # The probe's residual risk, which is real and is accepted
//
// The probe is a heuristic, not a proof, and it is not free of false positives:
// UTF-8 validity is a property of the bytes, so a *CP949* name whose bytes
// happen to parse as UTF-8 takes branch 2 and comes out as Latin-looking
// nonsense labelled "utf-8" rather than as Korean labelled "cp949". 345 of the
// 11,172 Hangul syllables encode to a CP949 pair that is also a well-formed
// two-byte UTF-8 sequence — CP949("징.jpg") is C2 A1 2E 6A 70 67, i.e. "¡.jpg" —
// and a name built only out of those 345 syllables and compatible ASCII is
// therefore misread. TestDecodeEntryName_utf8ProbeFirst_hasFalsePositives pins
// both the count and the worked example.
//
// This is accepted, not overlooked:
//
//   - The order is mandated. arch §4.4, D-24 and impl-plan §3 WP-02 acceptance
//     3 all require step 2 *before* step 3, in those words.
//   - Reversing it trades a rare failure for a systematic one. Every flagless
//     UTF-8 archive would be corrupted; today's failure needs a name drawn
//     entirely from a 3 % subset of the syllabary, which no name in the
//     measured 14,630 is (0 of them are valid UTF-8 at all).
//   - There is no better signal available at this layer. Both readings are
//     well-formed; only a human or a corpus-wide statistic could break the tie,
//     and neither is present when one central-directory record is decoded.
package kenc

import (
	"bytes"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/transform"
)

// The encoding labels DecodeEntryName reports. They are stored per book so the
// UI can surface how a name was read, and "unknown" is the only value that
// means "these characters may be wrong".
const (
	// EncUTF8 — the bytes are valid UTF-8, either because the producer set
	// general-purpose bit 11 or because the probe proved it.
	EncUTF8 = "utf-8"
	// EncUTF8Invalid — bit 11 was set but the bytes are not valid UTF-8. The
	// producer lied; the name is repaired lossily rather than second-guessed,
	// because a declared encoding is still the best evidence available.
	EncUTF8Invalid = "utf-8-invalid"
	// EncCP949 — decoded as CP949 (Unified Hangul Code, the superset of EUC-KR
	// that x/text's korean.EUCKR implements) with no unmappable bytes.
	EncCP949 = "cp949"
	// EncUnknown — neither UTF-8 nor CP949 fits. The bytes are kept lossily so
	// the pages are still readable and the book still opens.
	EncUnknown = "unknown"
)

// replacement is U+FFFD, both the substitution the EUC-KR decoder emits for
// unmappable bytes and the one used when repairing invalid UTF-8.
const replacement = "�"

// DecodeEntryName turns the raw name bytes of a ZIP central-directory record
// into a UTF-8 string, and reports which encoding was used.
//
// utf8Flag is general-purpose bit 11 (0x0800) of that record. The returned name
// is always valid UTF-8, so it can be stored in pages.name, marshalled to JSON
// and compared by natsort without further sanitising.
//
// The four steps are arch §4.4, in this order, and the order is load-bearing.
func DecodeEntryName(raw []byte, utf8Flag bool) (name string, enc string) {
	// 1. The producer declared UTF-8. Trust it; repair only if it lied.
	if utf8Flag {
		if utf8.Valid(raw) {
			return string(raw), EncUTF8
		}
		return strings.ToValidUTF8(string(raw), replacement), EncUTF8Invalid
	}

	// 2. No flag, but the bytes are already valid UTF-8, so it IS UTF-8. This
	//    test MUST come before CP949. Pure-ASCII names ("001.jpg", 18,826 of
	//    the flagless entries measured) take this branch too, which is correct
	//    and free.
	if utf8.Valid(raw) {
		return string(raw), EncUTF8
	}

	// 3. Not valid UTF-8, so decode as CP949. The error is discarded on
	//    purpose: it is always nil (see the package comment). What decides the
	//    outcome is whether the decoder had to substitute anything.
	dec, _, _ := transform.Bytes(korean.EUCKR.NewDecoder(), raw)
	if !bytes.ContainsRune(dec, utf8.RuneError) {
		return string(dec), EncCP949
	}

	// 4. Neither encoding fits. Keep the bytes lossily so the page is still
	//    reachable, and label the book so the UI can say so (FR-IDX-010).
	return strings.ToValidUTF8(string(raw), replacement), EncUnknown
}
