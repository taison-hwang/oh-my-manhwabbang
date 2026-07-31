// Package natsort implements the natural ordering of FR-IDX-007: `1.jpg`
// before `2.jpg` before `10.jpg`, whatever the zero-padding, for page names
// inside a book, book names inside a series and `sort=name` in
// GET /api/series.
//
// It ships as two representations that are required to agree (D-31):
//
//   - Compare(a, b) for Go-side ordering.
//   - Key(s), a byte string whose bytes.Compare order is identical. It is
//     stored in series.sort_key / books.sort_key so SQLite's default BINARY
//     collation does the ordering with no user-defined function, which is what
//     keeps `sort=name` cheap at 10^4 series.
//
// The agreement is a property test, not a comment: see natsort_test.go.
//
// # The algorithm (arch §4.7)
//
// Both strings are walked simultaneously in chunks. A chunk is either a
// maximal run of ASCII digits or a single non-digit rune.
//
//  1. Both chunks are digit runs. Strip leading zeros from each and compare by
//     the *length* of the stripped run first — that is the numeric comparison,
//     and it cannot overflow, which matters because the real collection has
//     20+ digit runs. Equal length falls back to a lexicographic compare of the
//     digits. If the numeric values are equal but the leading-zero counts
//     differ, fewer zeros sorts first (1 < 01 < 001): arbitrary, but total and
//     stable, so mixed padding never makes the order nondeterministic.
//  2. One chunk is a digit run and the other is not. Digits sort first, which
//     keeps numbered volumes ahead of prose-named extras (`1권` before `가`).
//  3. Neither is a digit. Compare a folded key (ASCII and fullwidth Latin
//     lowercased, fullwidth digits mapped to ASCII, everything else
//     unicode.ToLower). Hangul, Hanja and Kana fall through to raw code-point
//     order, which for the Hangul syllable block U+AC00–U+D7A3 *is* dictionary
//     order, so `가 < 나 < 다` is free. Folded ties are broken by raw rune, so
//     `A` sorts immediately before `a`.
//  4. One string ran out first: it sorts first.
//
// The result is a total order — Compare(a, b) == 0 if and only if a == b, for
// arbitrary bytes including invalid UTF-8.
package natsort

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// invalidBase is where bytes that are not part of a valid UTF-8 sequence are
// mapped to, above utf8.MaxRune (0x10FFFF). Two effects, both wanted: a
// mojibake byte sorts after every real character, and two different bad bytes
// stay distinguishable, which is what keeps the order total on names that no
// encoding could rescue (kenc's "unknown" branch).
const invalidBase = 0x110000

// Compare returns -1, 0 or +1 as a sorts before, equal to, or after b under
// the natural ordering. It is suitable for slices.SortFunc and never allocates.
func Compare(a, b string) int {
	ia, ib := 0, 0
	for ia < len(a) && ib < len(b) {
		da, db := isASCIIDigit(a[ia]), isASCIIDigit(b[ib])
		switch {
		case da && db:
			// Rule 1: both chunks are digit runs.
			runA, nextA := digitRun(a, ia)
			runB, nextB := digitRun(b, ib)
			numA, zerosA := stripLeadingZeros(runA)
			numB, zerosB := stripLeadingZeros(runB)
			if len(numA) != len(numB) {
				// More significant digits means a larger number. No parsing, so
				// a 22-digit page number cannot overflow.
				return cmpInt(len(numA), len(numB))
			}
			if c := strings.Compare(numA, numB); c != 0 {
				return c
			}
			if zerosA != zerosB {
				// Equal value, different padding: 1 < 01 < 001. Decided here,
				// at the chunk, not deferred to the end of the string — that is
				// what puts `1권.zip` before `01권 (완).zip` in the arch §4.7
				// verified table even though ' ' < '.'.
				return cmpInt(zerosA, zerosB)
			}
			ia, ib = nextA, nextB
		case da:
			return -1 // Rule 2: digits before non-digits.
		case db:
			return 1
		default:
			// Rule 3: one rune each.
			rawA, sizeA := nextRune(a, ia)
			rawB, sizeB := nextRune(b, ib)
			if c := cmpRune(fold(rawA), fold(rawB)); c != 0 {
				return c
			}
			if c := cmpRune(rawA, rawB); c != 0 {
				return c
			}
			ia += sizeA
			ib += sizeB
		}
	}
	// Rule 4: whatever ran out first sorts first.
	switch {
	case ia == len(a) && ib == len(b):
		return 0
	case ia == len(a):
		return -1
	default:
		return 1
	}
}

// isASCIIDigit reports whether c is one of '0'..'9'. Fullwidth digits
// (U+FF10–U+FF19) are deliberately *not* digit runs: they are single runes and
// take rule 3, where folding maps them onto ASCII digits.
func isASCIIDigit(c byte) bool { return c >= '0' && c <= '9' }

// digitRun returns the maximal run of ASCII digits starting at i and the index
// just past it.
func digitRun(s string, i int) (run string, next int) {
	j := i
	for j < len(s) && isASCIIDigit(s[j]) {
		j++
	}
	return s[i:j], j
}

// stripLeadingZeros splits a digit run into its significant digits and the
// number of leading zeros it carried. A run of all zeros yields ("", n), which
// compares as the number 0 with n zeros of padding.
func stripLeadingZeros(run string) (digits string, zeros int) {
	i := 0
	for i < len(run) && run[i] == '0' {
		i++
	}
	return run[i:], i
}

// nextRune decodes one rune at i. A byte that is not part of a valid UTF-8
// sequence is returned as invalidBase+byte rather than as U+FFFD, so that two
// different bad bytes do not compare equal.
func nextRune(s string, i int) (rune, int) {
	r, size := utf8.DecodeRuneInString(s[i:])
	if r == utf8.RuneError && size == 1 {
		return invalidBase + rune(s[i]), 1
	}
	return r, size
}

// Caseless blocks that this collection is overwhelmingly made of. Every rune in
// them is its own lower case, so the fold can skip unicode.ToLower's binary
// search over the case ranges — worth roughly a third of Compare's cost on a
// Korean title. TestFold_caselessRanges_haveNoLowerCaseMapping verifies the
// claim exhaustively rather than trusting it.
const (
	hangulJamoFirst = 0x1100 // Hangul Jamo
	hangulJamoLast  = 0x11FF
	cjkFirst        = 0x3130 // Hangul Compatibility Jamo through CJK Unified
	cjkLast         = 0x9FFF //   Ideographs, including Kana and Extension A
	hangulSylFirst  = 0xAC00 // Hangul Syllables
	hangulSylLast   = 0xD7A3
	fullwidthDigit0 = 0xFF10
	fullwidthDigit9 = 0xFF19
	fullwidthUpperA = 0xFF21
	fullwidthUpperZ = 0xFF3A
	fullwidthLowerA = 0xFF41
	fullwidthLowerZ = 0xFF5A
)

// fold is the case/width folding of arch §4.7 rule 3.
func fold(r rune) rune {
	switch {
	case r >= invalidBase:
		// An unmappable byte folds to the replacement character, so mojibake
		// clusters together; the raw tie-break then keeps the order total.
		return utf8.RuneError
	case r >= 'A' && r <= 'Z':
		return r + ('a' - 'A')
	case r < utf8.RuneSelf:
		return r // the rest of ASCII is its own lower case
	case r >= hangulJamoFirst && r <= hangulJamoLast,
		r >= cjkFirst && r <= cjkLast,
		r >= hangulSylFirst && r <= hangulSylLast:
		return r
	case r >= fullwidthDigit0 && r <= fullwidthDigit9:
		return r - fullwidthDigit0 + '0'
	case r >= fullwidthUpperA && r <= fullwidthUpperZ:
		return r - fullwidthUpperA + 'a'
	case r >= fullwidthLowerA && r <= fullwidthLowerZ:
		return r - fullwidthLowerA + 'a'
	default:
		return unicode.ToLower(r)
	}
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

func cmpRune(a, b rune) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}
