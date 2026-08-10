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
//
// # Where the per-entry rule stops, and what takes over
//
// "One record at a time" is also what limits DecodeEntryName. CP949 and
// Shift_JIS overlap, so a Japanese archive can hand it a name that reads
// perfectly as Korean and is nonetheless wrong — 160 of the 189 names in the
// collection's `天上天下 20.zip` do exactly that. No amount of looking at that
// one record reveals it; what does is the rest of the archive, where 28 more
// names are Shift_JIS-only. ArchiveFallback is that second look, and
// zipidx.resolveArchiveNames is the caller that has the whole directory to
// give it. It runs only for archives where CP949 already failed on some name,
// so the 11,192 archives of 11,196 that never needed it pay nothing.
package kenc

import (
	"bytes"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/japanese"
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
	// EncCP932 — decoded as Shift_JIS. Only ArchiveFallback ever selects this,
	// and only for a whole archive at once; see its comment for why an entry on
	// its own is not enough evidence.
	EncCP932 = "cp932"
	// EncUnknown — none of the encodings fits. The bytes are kept lossily so
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
	return DecodeEntryNameAs(raw, utf8Flag, EncCP949)
}

// DecodeEntryNameAs is DecodeEntryName with the legacy encoding named by the
// caller rather than assumed to be CP949.
//
// legacy is what step 3 tries, and only EncCP932 diverts it; anything else —
// including "" — means CP949, so a caller that has not run ArchiveFallback
// gets the historical behaviour. Steps 1, 2 and 4 do not depend on it: a name
// that is valid UTF-8 is UTF-8 whatever the rest of the archive is in.
func DecodeEntryNameAs(raw []byte, utf8Flag bool, legacy string) (name string, enc string) {
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

	// 3. Not valid UTF-8, so decode in the archive's legacy encoding. The error
	//    is discarded on purpose: it is always nil (see the package comment).
	//    What decides the outcome is whether the decoder had to substitute
	//    anything.
	if legacy == EncCP932 {
		if dec, ok := decodeLegacy(japanese.ShiftJIS.NewDecoder(), raw); ok {
			return dec, EncCP932
		}
	} else if dec, ok := decodeLegacy(korean.EUCKR.NewDecoder(), raw); ok {
		return dec, EncCP949
	}

	// 4. No encoding fits. Keep the bytes lossily so the page is still
	//    reachable, and label the book so the UI can say so (FR-IDX-010).
	return strings.ToValidUTF8(string(raw), replacement), EncUnknown
}

// ArchiveFallback reports the legacy encoding that reads *every* name in raws,
// for an archive where CP949 has already failed on at least one of them. It
// returns "" when no candidate reads them all, which leaves those names at
// EncUnknown.
//
// # Why this is decided per archive and not per entry
//
// One name is not enough evidence, because CP949 and Shift_JIS overlap: 160 of
// the 189 flagless names in the collection's `天上天下 20.zip` decode as CP949
// without a single U+FFFD, and every one of those readings is wrong —
// "밮밮-20-001.jpg" is the CP949 misreading of "天天-20-001.jpg". Per-entry
// decoding cannot see that, because each of those 160 names is individually a
// perfectly good CP949 string. What gives the archive away is the *other* 28
// names, which CP949 cannot read at all and Shift_JIS can. Names inside one
// container come from one producer in one encoding, so the 28 settle the 160.
//
// # Why Shift_JIS is the only candidate
//
// It is the only one the collection needs and every extra candidate is another
// chance to misread a Korean archive as something else. Measured over all
// 11,196 indexed ZIPs (1.35 M entries):
//
//   - 6,757 archives have nothing but UTF-8 or ASCII names — unaffected.
//   - 1,871 are read completely by CP949 and by nothing else.
//   - 4 are read completely by Shift_JIS and not by CP949: the three
//     `[文月晃] 海の御先` volumes and `天上天下 20.zip`. These are the 728
//     entry names this function exists for.
//   - 2,554 are read completely by *both*, and in none of them does the
//     Shift_JIS reading contain kana while the CP949 reading lacks Hangul —
//     i.e. not one of them is really Japanese. CP949 winning those ties (which
//     it does by never reaching this function) is therefore not a guess.
//   - 1 is read by neither: `BM 넥타 09.zip`, whose names are not mis-encoded
//     but truncated — the leading bytes of UTF-8 NFD jamo sequences are simply
//     gone. No decoder recovers that, and EncUnknown is the honest answer.
//
// # Why "Shift_JIS read it" is not on its own enough
//
// Shift_JIS reads plenty of Korean bytes without complaint, so decoding
// cleanly cannot be the whole test: CP949("한글.jpg") comes back as
// "ﾇﾑｱﾛ.jpg". What separates the two is *which* characters come out. Korean
// bytes land in the single-byte halfwidth-katakana block, because the CP949
// lead byte of an ordinary Hangul syllable (0xB0–0xC8) is exactly Shift_JIS's
// halfwidth range (0xA1–0xDF); real Japanese names land in the double-byte
// kana and kanji blocks. Measured on the same collection:
//
//   - The 4 Japanese archives contain 0 halfwidth katakana across all 794
//     names, and 11,175 fullwidth kana/kanji.
//   - Every Korean name tried the same way produces halfwidth katakana (4 to
//     15 per name) and essentially no fullwidth kana/kanji.
//
// So one halfwidth katakana vetoes the archive, and at least one fullwidth
// kana or kanji is required to accept it. The second condition costs nothing
// here — a name with no CJK in it at all had no reason to reach this function,
// since it would have to be non-UTF-8 to get here — and it is what stops a
// short, odd byte sequence from carrying a whole archive on its own.
func ArchiveFallback(raws [][]byte) string {
	if len(raws) == 0 {
		return ""
	}
	dec := japanese.ShiftJIS.NewDecoder()
	var cjk int
	for _, raw := range raws {
		s, ok := decodeLegacy(dec, raw)
		dec.Reset()
		if !ok {
			return ""
		}
		for _, r := range s {
			switch {
			case r >= 0xFF61 && r <= 0xFF9F:
				return ""
			case r >= 0x3040 && r <= 0x30FF, r >= 0x4E00 && r <= 0x9FFF:
				cjk++
			}
		}
	}
	if cjk == 0 {
		return ""
	}
	return EncCP932
}

// decodeLegacy runs one legacy decoder and reports whether it read the bytes
// without substituting. Both x/text decoders used here return a nil error even
// for input they cannot map (see the package comment), so the substitution
// character in the output is the only signal there is.
func decodeLegacy(dec transform.Transformer, raw []byte) (string, bool) {
	out, _, err := transform.Bytes(dec, raw)
	if err != nil || bytes.ContainsRune(out, utf8.RuneError) {
		return "", false
	}
	return string(out), true
}
