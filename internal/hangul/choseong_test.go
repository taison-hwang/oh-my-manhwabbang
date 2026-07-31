package hangul_test

import (
	"strings"
	"testing"

	"golang.org/x/text/unicode/norm"

	"shelf/internal/hangul"
)

// TestChoseong_archVerifiedVectors walks the four vectors arch §4.8 and
// impl-plan §3 WP-02 acceptance 4 mark VERIFIED, then the cases the collection
// adds around them.
func TestChoseong_archVerifiedVectors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		// The four verified vectors, verbatim.
		{"강철의 연금술사", "강철의 연금술사", "ㄱㅊㅇ ㅇㄱㅅㅅ"},
		{"군계", "군계", "ㄱㄱ"},
		{"20세기소년", "20세기소년", "20ㅅㄱㅅㄴ"},
		{"Attack on Titan", "Attack on Titan", "attack on titan"},

		// Real series names from the collection.
		{"기동전사 건담", "기동전사 건담", "ㄱㄷㅈㅅ ㄱㄷ"},
		{"쩐의 전쟁", "쩐의 전쟁", "ㅉㅇ ㅈㅈ"},
		{"우에키의 법칙", "우에키의 법칙", "ㅇㅇㅋㅇ ㅂㅊ"},
		{"상처를 쫓는자", "상처를 쫓는자", "ㅅㅊㄹ ㅉㄴㅈ"},
		{"세월을 잊은 버려진 처녀", "세월을 잊은 버려진 처녀", "ㅅㅇㅇ ㅇㅇ ㅂㄹㅈ ㅊㄴ"},
		{"XXX 홀릭", "XXX 홀릭", "xxx ㅎㄹ"},
		{"D.N.Angel", "D.N.Angel", "d.n.angel"},
		{"I'll(아일)", "I'll(아일)", "i'll(ㅇㅇ)"},
		{"[만화] 군계 1~25", "[만화] 군계 1~25", "[ㅁㅎ] ㄱㄱ 1~25"},

		// Every consonant class, including the five doubled initials.
		{"all 19 initials", "가까나다따라마바빠사싸아자짜차카타파하", "ㄱㄲㄴㄷㄸㄹㅁㅂㅃㅅㅆㅇㅈㅉㅊㅋㅌㅍㅎ"},
		// A final consonant must never leak into the key.
		{"finals are ignored", "값밟닭", "ㄱㅂㄷ"},

		// Jamo passthrough: what the user actually types.
		{"bare jamo", "ㄱㄷ", "ㄱㄷ"},
		{"compound jamo typed directly", "ㄳㄵㄶㄺㄻㄼㄽㄾㄿㅀㅄ", "ㄳㄵㄶㄺㄻㄼㄽㄾㄿㅀㅄ"},
		{"vowel jamo", "ㅏㅑㅗㅢ", "ㅏㅑㅗㅢ"},
		{"jamo mixed with syllables", "ㄱ군계", "ㄱㄱㄱ"},

		// Non-Hangul is lower-cased and otherwise left alone.
		{"empty", "", ""},
		{"digits and punctuation", "01권 (완).zip", "01ㄱ (ㅇ).zip"},
		{"hanja", "軍鷄", "軍鷄"},
		{"kana", "カタカナ", "カタカナ"},
		{"latin uppercase", "BLACK LAGOON", "black lagoon"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := hangul.Choseong(tc.in); got != tc.want {
				t.Errorf("Choseong(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestChoseong_everySyllableAndEveryBlockBoundary walks all 11,172 code points
// of the Hangul Syllables block plus the code point on each side of it, and
// every Hangul Compatibility Jamo consonant plus the code point on each side of
// that range.
//
// The table-driven vectors above stop at the syllables that happen to appear in
// real series names, which leaves both *upper* bounds of arch §4.8's two ranges
// untested: U+D7A3 (힣) and U+314E (ㅎ) are each the last member of their range,
// so an off-by-one that excludes them is invisible to every other test in this
// file — ToLower is the identity on both, so the fallback branch returns the
// same rune for ㅎ and silently returns the *syllable* 힣 instead of ㅎ. The
// consequences are a name ending in 힣 dropping out of 초성 search and, via
// IsChoseongQuery, every ㅎ query being routed away from choseong_key
// altogether.
func TestChoseong_everySyllableAndEveryBlockBoundary(t *testing.T) {
	t.Parallel()

	// arch §4.8's table, written out here rather than imported, so this test is
	// an independent statement of the mapping.
	const initials = "ㄱㄲㄴㄷㄸㄹㅁㅂㅃㅅㅆㅇㅈㅉㅊㅋㅌㅍㅎ"
	table := []rune(initials)

	const (
		firstSyllable = 0xAC00 // 가
		lastSyllable  = 0xD7A3 // 힣
		perInitial    = 588    // 21 medials x 28 finals
	)

	count := 0
	for r := rune(firstSyllable); r <= lastSyllable; r++ {
		want := string(table[(r-firstSyllable)/perInitial])
		if got := hangul.Choseong(string(r)); got != want {
			t.Fatalf("Choseong(%q, U+%04X) = %q, want %q", string(r), r, got, want)
		}
		count++
	}
	if count != 11172 {
		t.Fatalf("walked %d syllables, want 11172", count)
	}

	// The two ends of the block, called out by name so a failure reads clearly,
	// and the code point immediately outside each end, which must NOT be treated
	// as a syllable.
	below, above := string(rune(firstSyllable-1)), string(rune(lastSyllable+1))
	for _, tc := range []struct{ in, want string }{
		{"가", "ㄱ"},     // U+AC00, the first syllable
		{"힣", "ㅎ"},     // U+D7A3, the last syllable
		{below, below}, // U+ABFF, one below the block: unassigned, passes through
		{above, above}, // U+D7A4, one above the block: unassigned, passes through
		{"힣힣", "ㅎㅎ"},
		{"강철의 연금술사힣", "ㄱㅊㅇ ㅇㄱㅅㅅㅎ"},
	} {
		if got := hangul.Choseong(tc.in); got != tc.want {
			t.Errorf("Choseong(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	// Every compatibility-jamo consonant passes through unchanged, and so does
	// every conjoining lead jamo after being mapped onto its compatibility form.
	// U+3131 and U+314E are the range ends of arch §4.8's second rule; U+1100
	// and U+1112 are the ends of the conjoining range.
	for i, r := range []rune("ㄱㄲㄴㄷㄸㄹㅁㅂㅃㅅㅆㅇㅈㅉㅊㅋㅌㅍㅎ") {
		if got := hangul.Choseong(string(r)); got != string(r) {
			t.Errorf("Choseong(%q, U+%04X) = %q, want it unchanged", string(r), r, got)
		}
		lead := rune(0x1100 + i)
		if got := hangul.Choseong(string(lead)); got != string(r) {
			t.Errorf("Choseong(conjoining U+%04X) = %q, want %q", lead, got, string(r))
		}
	}
}

// TestIsChoseongQuery_coversTheWholeJamoRange pins both ends of arch §4.8's
// `3131 <= r <= 314E` range for the *query* side. The routing test below uses
// only ㄱ, ㄷ, ㅅ, ㅈ and ㅊ, so a range that stopped one short of ㅎ would send
// every ㅎ query to search_key alone and quietly break 초성 search for a fifth
// of the alphabet.
func TestIsChoseongQuery_coversTheWholeJamoRange(t *testing.T) {
	t.Parallel()

	for r := rune(0x3131); r <= 0x314E; r++ { // ㄱ .. ㅎ
		if !hangul.IsChoseongQuery(string(r)) {
			t.Errorf("IsChoseongQuery(%q, U+%04X) = false, want true", string(r), r)
		}
	}
	for r := rune(0x1100); r <= 0x1112; r++ { // conjoining ᄀ .. ᄒ
		if !hangul.IsChoseongQuery(string(r)) {
			t.Errorf("IsChoseongQuery(conjoining U+%04X) = false, want true", r)
		}
	}

	// Outside the consonant range on both sides. U+3130 is unassigned; U+314F
	// onwards are the vowels, which no choseong_key can ever contain — a
	// vowel-only query would be a guaranteed false negative there, so it is
	// routed to search_key alone, exactly like "군계".
	for _, r := range []rune{0x3130, 0x314F, 0x3163, 0x10FF, 0x1113} {
		if hangul.IsChoseongQuery(string(r)) {
			t.Errorf("IsChoseongQuery(U+%04X) = true, want false", r)
		}
	}
}

// TestChoseong_queriesMatchTheKey is FR-LIB-006 stated the way the user
// experiences it: a typed 초성 string has to be a substring of the key, which
// is exactly what the SQL `choseong_key LIKE '%'||q||'%'` does.
func TestChoseong_queriesMatchTheKey(t *testing.T) {
	t.Parallel()

	series := []string{
		"기동전사 건담",
		"강철의 연금술사",
		"군계",
		"20세기소년",
		"Attack on Titan",
		"쩐의 전쟁",
	}

	tests := []struct {
		query string
		want  []string
	}{
		{"ㄱㄷ", []string{"기동전사 건담"}},
		{"ㄱㅊ", []string{"강철의 연금술사"}},
		{"ㄱㄱ", []string{"군계"}},
		{"ㅅㄱㅅㄴ", []string{"20세기소년"}},
		{"ㅈㅈ", []string{"쩐의 전쟁"}},
		// A single consonant is a broad prefix query and must match anywhere in
		// the key, including "20세기소년" -> "20ㅅㄱㅅㄴ".
		{"ㄱ", []string{"기동전사 건담", "강철의 연금술사", "군계", "20세기소년"}},
		{"attack", []string{"Attack on Titan"}},
		{"ㅋㅋㅋ", nil},
	}

	for _, tc := range tests {
		t.Run(tc.query, func(t *testing.T) {
			t.Parallel()

			if !hangul.IsChoseongQuery(tc.query) {
				t.Fatalf("IsChoseongQuery(%q) = false, so the query would never reach choseong_key", tc.query)
			}
			var got []string
			for _, s := range series {
				if strings.Contains(hangul.Choseong(s), hangul.Choseong(tc.query)) {
					got = append(got, s)
				}
			}
			if len(got) != len(tc.want) {
				t.Fatalf("query %q matched %q, want %q", tc.query, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("query %q matched %q, want %q", tc.query, got, tc.want)
				}
			}
		})
	}
}

// TestChoseong_decomposedHangul_yieldsTheSameKey covers NFD input. macOS
// writes decomposed Korean into filenames and into ZIP entry names, and an
// NFD series name whose key came out as raw jamo would be unfindable.
func TestChoseong_decomposedHangul_yieldsTheSameKey(t *testing.T) {
	t.Parallel()

	for _, s := range []string{"강철의 연금술사", "군계", "기동전사 건담", "20세기소년"} {
		nfd := norm.NFD.String(s)
		if nfd == s {
			t.Fatalf("%q is unchanged by NFD; the case is not being exercised", s)
		}
		if got, want := hangul.Choseong(nfd), hangul.Choseong(s); got != want {
			t.Errorf("Choseong(NFD(%q)) = %q, want %q", s, got, want)
		}
		if got, want := hangul.SearchKey(nfd), hangul.SearchKey(s); got != want {
			t.Errorf("SearchKey(NFD(%q)) = %q, want %q", s, got, want)
		}
	}

	// A lone conjoining choseong jamo cannot compose into a syllable, so it is
	// mapped explicitly onto its compatibility form.
	if got, want := hangul.Choseong("ᄀᄃ"), "ㄱㄷ"; got != want {
		t.Errorf("Choseong(conjoining ᄀᄃ) = %q, want %q", got, want)
	}
}

// TestChoseong_caseFoldingThatCreatesAComposition_stillCanonicalises is a
// regression found by FuzzChoseong (corpus entry 138ba96edaa17e14). "Y" +
// U+030A is already NFC, but its lower case "y" + U+030A composes to U+1E99,
// so normalising only the input left the key in a non-canonical form and a
// LIKE against the same title written the other way missed.
func TestChoseong_caseFoldingThatCreatesAComposition_stillCanonicalises(t *testing.T) {
	t.Parallel()

	const decomposed = "Y̊" // Y + combining ring above
	const composed = "ẙ"    // ẙ

	if norm.NFC.String(decomposed) != decomposed {
		t.Fatal("the input is no longer the case this test was written for")
	}
	for _, fn := range []struct {
		name string
		f    func(string) string
	}{{"Choseong", hangul.Choseong}, {"SearchKey", hangul.SearchKey}} {
		got := fn.f(decomposed)
		if got != composed {
			t.Errorf("%s(%q) = %q, want %q", fn.name, decomposed, got, composed)
		}
		if again := fn.f(got); again != got {
			t.Errorf("%s is not idempotent: %q -> %q -> %q", fn.name, decomposed, got, again)
		}
	}
}

func TestChoseong_isDeterministicAndKeepsLength(t *testing.T) {
	t.Parallel()

	// choseong_key is compared with LIKE '%q%', so the key must stay aligned
	// with the display name rune for rune — one syllable in, one jamo out.
	for _, s := range []string{"강철의 연금술사", "[만화] 군계 1~25", "20세기소년"} {
		key := hangul.Choseong(s)
		if a, b := len([]rune(key)), len([]rune(norm.NFC.String(s))); a != b {
			t.Errorf("Choseong(%q) = %q: %d runes, want %d", s, key, a, b)
		}
		if again := hangul.Choseong(s); again != key {
			t.Errorf("Choseong(%q) is not deterministic: %q then %q", s, key, again)
		}
		// The key is idempotent: jamo pass through unchanged, so re-keying a
		// key (which is what happens when a query is normalised) is a no-op.
		if twice := hangul.Choseong(key); twice != key {
			t.Errorf("Choseong is not idempotent on %q: %q", key, twice)
		}
	}
}

func TestSearchKey_foldsCaseAndNormalises(t *testing.T) {
	t.Parallel()

	tests := []struct{ in, want string }{
		{"Attack on Titan", "attack on titan"},
		{"BlackLagoon05_034.JPG", "blacklagoon05_034.jpg"},
		{"[만화] 군계 1~25", "[만화] 군계 1~25"},
		{"XXX 홀릭 13", "xxx 홀릭 13"},
		{"", ""},
	}
	for _, tc := range tests {
		if got := hangul.SearchKey(tc.in); got != tc.want {
			t.Errorf("SearchKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestIsChoseongQuery_separatesJamoQueriesFromTextQueries(t *testing.T) {
	t.Parallel()

	// arch §4.8: jamo/ASCII/space queries hit choseong_key OR search_key;
	// everything else hits search_key only.
	jamo := []string{"ㄱㄷ", "ㄱ", "ㄱ ㄷ", "attack", "attack on titan", "20", "ㄳ", "i'll", "ᄀ"}
	text := []string{"", "군계", "강철의", "ㄱ군계", "軍鷄", "カタカナ", "ㄱ 군계"}

	for _, q := range jamo {
		if !hangul.IsChoseongQuery(q) {
			t.Errorf("IsChoseongQuery(%q) = false, want true", q)
		}
	}
	for _, q := range text {
		if hangul.IsChoseongQuery(q) {
			t.Errorf("IsChoseongQuery(%q) = true, want false", q)
		}
	}
}

// FuzzChoseong asserts the key builder survives arbitrary input — series names
// come from a filesystem nobody validated — and always produces valid,
// searchable output.
func FuzzChoseong(f *testing.F) {
	f.Add("강철의 연금술사")
	f.Add("ㄱㄷ")
	f.Add("20세기소년")
	f.Add("Attack on Titan")
	f.Add("\xff\xfe.jpg")
	f.Add("")
	f.Add("가")

	f.Fuzz(func(t *testing.T, s string) {
		key := hangul.Choseong(s)
		if again := hangul.Choseong(s); again != key {
			t.Fatalf("Choseong(%q) is not deterministic: %q then %q", s, key, again)
		}
		if strings.ContainsFunc(key, func(r rune) bool {
			return r >= 0xAC00 && r <= 0xD7A3
		}) {
			t.Fatalf("Choseong(%q) = %q still contains a Hangul syllable", s, key)
		}
		if twice := hangul.Choseong(key); twice != key {
			t.Fatalf("Choseong is not idempotent: %q -> %q -> %q", s, key, twice)
		}
		_ = hangul.SearchKey(s)
		_ = hangul.IsChoseongQuery(s)
	})
}
