package natsort

import (
	"strconv"
	"unicode/utf8"
)

// Key encoding markers. Every chunk is introduced by one of these, and the
// terminator is the smallest byte in the encoding, which is what makes "the
// string that ran out first sorts first" (rule 4) fall out of memcmp. markDigits
// below markRune is what makes "digits before non-digits" (rule 2) fall out too.
const (
	terminator = 0x00
	markDigits = 0x01
	markRune   = 0x02

	// markInvalid introduces a byte that was not part of a valid UTF-8
	// sequence. 0xF5 is never a legal UTF-8 lead byte (the highest is 0xF4), so
	// mojibake sorts after every real rune, matching invalidBase in Compare.
	markInvalid = 0xF5
)

// Key returns a byte string whose bytes.Compare order is identical to Compare.
// It is stored in series.sort_key / books.sort_key as a BLOB and ordered by
// SQLite's default BINARY collation. Any byte value may appear, including 0x00,
// which is legal inside a BLOB.
//
// Encoding, one record per chunk, left to right, then a 0x00 terminator:
//
//	digit run    0x01 | count(len(stripped)) | stripped digits | count(leading zeros)
//	non-digit    0x02 | rune(fold(r)) | rune(r)
//
// where count(n) is a self-delimiting, order-preserving decimal-of-hex-length
// encoding and rune(r) is UTF-8 for a real rune or 0xF5 followed by the raw
// byte for an unmappable one. Every record is self-delimiting under a common
// prefix, so memcmp can never fall out of alignment.
//
// This deviates from the sketch in arch §4.7 in two places, both forced:
//
//   - The leading-zero count is part of the digit record. The sketch defers all
//     tie-breaking to a trailing copy of the original string, which orders
//     `01권 (완).zip` before `1권.zip` and so contradicts §4.7's own VERIFIED
//     table. Compare decides padding at the chunk (rule 1), and the key has to
//     agree.
//   - Lengths are not fixed at "two ASCII hex chars", which would cap a digit
//     run at 255 characters and mis-order anything longer. The encoding used
//     here has no length ceiling.
//
// The trailing copy of the original string that the sketch appends is not
// needed: the encoding above is injective (stripped digits plus zero count
// reconstruct the run; the raw rune reconstructs the character), so distinct
// strings always produce distinct keys.
func Key(s string) []byte {
	// 3 bytes per ASCII character covers the common all-digits and all-Latin
	// cases without a regrow.
	out := make([]byte, 0, 3*len(s)+8)

	for i := 0; i < len(s); {
		if isASCIIDigit(s[i]) {
			run, next := digitRun(s, i)
			digits, zeros := stripLeadingZeros(run)
			out = append(out, markDigits)
			out = appendCount(out, len(digits))
			out = append(out, digits...)
			out = appendCount(out, zeros)
			i = next
			continue
		}
		r, size := nextRune(s, i)
		out = append(out, markRune)
		out = appendRune(out, fold(r))
		out = appendRune(out, r)
		i += size
	}
	return append(out, terminator)
}

// appendCount writes n so that memcmp orders counts numerically: one byte
// giving the number of hex digits, then the hex digits themselves. Longer
// means larger, and equal-length counts compare digit by digit — and because
// the length byte fixes the field width, the next field stays aligned.
func appendCount(out []byte, n int) []byte {
	h := strconv.FormatInt(int64(n), 16)
	out = append(out, byte('0'+len(h)))
	return append(out, h...)
}

// appendRune writes r in an order-preserving, self-delimiting form. UTF-8 has
// the property that lexicographic byte order equals code-point order and no
// encoding is a prefix of another, so real runes need no framing; unmappable
// bytes get a fixed two-byte record above every UTF-8 lead byte.
func appendRune(out []byte, r rune) []byte {
	if r >= invalidBase {
		return append(out, markInvalid, byte(r-invalidBase))
	}
	return utf8.AppendRune(out, r)
}
