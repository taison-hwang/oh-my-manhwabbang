// Package hangul implements the Korean initial-consonant (초성) search key of
// FR-LIB-006: typing "ㄱㄷ" finds "기동전사 건담".
//
// Choseong is computed once per series at scan time into series.choseong_key;
// GET /api/series?q= then matches `choseong_key LIKE '%'||q||'%'` OR
// `search_key LIKE '%'||q||'%'` when the query is jamo/ASCII/space, and
// search_key alone otherwise (arch §4.8). IsChoseongQuery decides which.
// Search is a server-side query — the client never holds all series (D-34,
// FR-LIB-007) — so both sides of the comparison have to be built with the same
// function, which is why the key and the query normaliser live together here.
package hangul

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// The Hangul Syllables block, U+AC00–U+D7A3. Every one of its 11,172 syllables
// is (choseong × 21 + jungseong) × 28 + jongseong, so the initial consonant is
// a division by 588 (= 21 × 28).
const (
	syllableBase  = 0xAC00
	syllableLast  = 0xD7A3
	syllableStep  = 588
	jamoFirst     = 0x3131 // ㄱ, first Hangul Compatibility Jamo
	jamoLast      = 0x314E // ㅎ, last compatibility consonant
	leadJamoFirst = 0x1100 // ᄀ, first conjoining choseong jamo
	leadJamoLast  = 0x1112 // ᄒ, last conjoining choseong jamo
)

// choseongTable is the 19 initial consonants in code-point order. Index =
// (syllable - 0xAC00) / 588.
const choseongTable = "ㄱㄲㄴㄷㄸㄹㅁㅂㅃㅅㅆㅇㅈㅉㅊㅋㅌㅍㅎ"

// choseong holds choseongTable as runes so indexing is O(1).
var choseong = []rune(choseongTable)

// leadJamo maps the conjoining choseong jamo U+1100–U+1112 onto the same 19
// compatibility jamo. A decomposed (NFD) Hangul string — what macOS filesystems
// hand out — normalises back to syllables before it reaches this table, but a
// *lone* leading jamo does not compose, and mapping it here is what keeps such
// a name searchable.
var leadJamo = []rune{
	'ㄱ', 'ㄲ', 'ㄴ', 'ㄷ', 'ㄸ', 'ㄹ', 'ㅁ', 'ㅂ', 'ㅃ', 'ㅅ',
	'ㅆ', 'ㅇ', 'ㅈ', 'ㅉ', 'ㅊ', 'ㅋ', 'ㅌ', 'ㅍ', 'ㅎ',
}

// Choseong returns the initial-consonant search key of s (arch §4.8):
//
//	Hangul syllable  -> its initial consonant, as a compatibility jamo
//	compatibility jamo (U+3131–U+314E) -> itself, so a typed "ㄱ" matches
//	anything else    -> unicode.ToLower(r), so Latin titles stay searchable
//
// The input is normalised to NFC first, so a decomposed Korean name — the form
// macOS writes into ZIP entry names and onto disk — yields the same key as its
// composed twin. Nothing else is stripped: spaces and punctuation are kept so
// that "ㄱㅊㅇ ㅇㄱㅅㅅ" matches "강철의 연금술사" positionally.
//
// The result is normalised again, because case folding can create a sequence
// that was not composable before it: "Y" + U+030A is already NFC, but its
// lower case "y" + U+030A composes to U+1E99. Without the second pass the key
// and a query for the same title would end up in different forms and LIKE
// would miss. Compatibility jamo are untouched by NFC, so the Korean half of
// the key is unaffected.
func Choseong(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range norm.NFC.String(s) {
		switch {
		case r >= syllableBase && r <= syllableLast:
			b.WriteRune(choseong[(r-syllableBase)/syllableStep])
		case r >= jamoFirst && r <= jamoLast:
			b.WriteRune(r)
		case r >= leadJamoFirst && r <= leadJamoLast:
			b.WriteRune(leadJamo[r-leadJamoFirst])
		default:
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return norm.NFC.String(b.String())
}

// SearchKey returns the substring-match key stored in series.search_key: the
// NFC form of s, lower-cased, re-normalised. It is the "otherwise" half of
// arch §4.8's search rule and the only key consulted for queries containing
// Hangul syllables. See Choseong for why the second normalisation is there.
func SearchKey(s string) string {
	return norm.NFC.String(strings.ToLower(norm.NFC.String(s)))
}

// IsChoseongQuery reports whether q may be matched against choseong_key, i.e.
// whether it consists only of Hangul compatibility jamo, ASCII and spaces
// (arch §4.8).
//
// A query containing whole Hangul syllables ("군계") is not a 초성 query — it
// is ordinary text, and matching it against a key made of bare consonants would
// only ever produce false negatives. An empty query is not a query at all.
func IsChoseongQuery(q string) bool {
	if q == "" {
		return false
	}
	for _, r := range q {
		switch {
		case r < 0x80: // ASCII, which includes the space
		case r >= jamoFirst && r <= jamoLast:
		case r >= leadJamoFirst && r <= leadJamoLast:
		case unicode.IsSpace(r):
		default:
			return false
		}
	}
	return true
}
