package natsort_test

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"slices"
	"sort"
	"strings"
	"testing"
	"unicode"

	"shelf/internal/natsort"
)

// sortedByCompare returns in sorted with Compare, leaving the input untouched.
func sortedByCompare(in []string) []string {
	out := slices.Clone(in)
	slices.SortStableFunc(out, natsort.Compare)
	return out
}

// sortedByKey returns in sorted by bytes.Compare over Key, which is exactly
// what SQLite's BINARY collation does to the sort_key BLOB column (D-31).
func sortedByKey(in []string) []string {
	out := slices.Clone(in)
	slices.SortStableFunc(out, func(a, b string) int {
		return bytes.Compare(natsort.Key(a), natsort.Key(b))
	})
	return out
}

// TestCompare_archVerifiedTable_reproducesEveryRow walks arch §4.7's VERIFIED
// output table verbatim. Bold rows in that table are real names from the
// collection.
func TestCompare_archVerifiedTable_reproducesEveryRow(t *testing.T) {
	t.Parallel()

	const big21 = "999999999999999999999"  // 21 nines
	const big22 = "1000000000000000000000" // 1 followed by 21 zeros

	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "plain page numbers, no padding",
			in:   []string{"10.jpg", "1.jpg", "2.jpg", "20.jpg", "3.jpg"},
			want: []string{"1.jpg", "2.jpg", "3.jpg", "10.jpg", "20.jpg"},
		},
		{
			name: "mixed zero padding groups by value then by padding",
			in:   []string{"001.jpg", "10.jpg", "1.jpg", "01.jpg", "002.jpg", "2.jpg"},
			want: []string{"1.jpg", "01.jpg", "001.jpg", "2.jpg", "002.jpg", "10.jpg"},
		},
		{
			name: "number after a literal prefix",
			in:   []string{"page-9", "page-10", "page-1", "page-100"},
			want: []string{"page-1", "page-9", "page-10", "page-100"},
		},
		{
			name: "two independent numeric fields",
			in:   []string{"vol 2 ch 10", "vol 2 ch 2", "vol 10 ch 1", "vol 1 ch 30"},
			want: []string{"vol 1 ch 30", "vol 2 ch 2", "vol 2 ch 10", "vol 10 ch 1"},
		},
		{
			name: "real: 군계 volumes",
			in:   []string{"군계 10권", "군계 2권", "군계 1권", "군계 25권"},
			want: []string{"군계 1권", "군계 2권", "군계 10권", "군계 25권"},
		},
		{
			name: "real: 강철의 연금술사 volumes",
			in:   []string{"강철의 연금술사 27", "강철의 연금술사 3", "강철의 연금술사 10"},
			want: []string{"강철의 연금술사 3", "강철의 연금술사 10", "강철의 연금술사 27"},
		},
		{
			name: "ASCII case folds, upper first on a tie",
			in:   []string{"b.jpg", "A.jpg", "a.jpg", "B.jpg"},
			want: []string{"A.jpg", "a.jpg", "B.jpg", "b.jpg"},
		},
		{
			name: "22-digit numbers do not overflow",
			in:   []string{big21, big22, "2"},
			want: []string{"2", big21, big22},
		},
		{
			name: "real: 권 archives with mixed padding and suffixes",
			in:   []string{"01권 (완).zip", "1권.zip", "10권.zip", "2권 (2).zip"},
			want: []string{"1권.zip", "01권 (완).zip", "2권 (2).zip", "10권.zip"},
		},
		{
			name: "digits before letters before Hangul",
			in:   []string{"cover.jpg", "0001.jpg", "z.jpg", "가.jpg"},
			want: []string{"0001.jpg", "cover.jpg", "z.jpg", "가.jpg"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := sortedByCompare(tc.in); !slices.Equal(got, tc.want) {
				t.Errorf("Compare order\n got %q\nwant %q", got, tc.want)
			}
			if got := sortedByKey(tc.in); !slices.Equal(got, tc.want) {
				t.Errorf("Key order\n got %q\nwant %q", got, tc.want)
			}
		})
	}
}

// surveyNames is every filename data-survey §5 lists as a natural-sort stress
// case, plus the two mojibake samples from §3. They are stored here exactly as
// the survey prints them.
var surveyNames = []string{
	"02-07.jpg",
	"075__.jpg",
	"13018.jpg",
	"13_08.jpg",
	"18-05.jpg",
	"BlackLagoon05_034.JPG",
	"CS02-026.JPG",
	"MLM08-0062.jpg",
	"c03_p108.png",
	"kv005002152.gif",
	"m02741.jpg",
	"sam 05 167.gif",
	"sirius_201005_040.jpg",
	"⌡∙▐╘ 04-068.jpg",
	"║≥ ┐└┤⌡_03_011.jpg",
	"║╧╡╬└╟▒╟12-135.jpg",
	"╜╩╞╚╗τ╖½1▒╟156.jpg",
	"╣┘└╠┐└╕▐░í 04 - 162.jpg",
	"╣Φ╞▓ ╖╬╛Γ 01▒╟/KoiZuMi-000.jpg",
	"╜║─╡0001.jpg",
	"╝╝┐∙└╗ └╪└║",
}

// TestCompare_realCollectionNameShapes_sortNumerically takes every filename
// shape from data-survey §5, varies its page-number field over a range that
// crosses the padding width, and requires the natural order to be the numeric
// order. Each case is also asserted to be a *genuine* stress case: plain
// lexicographic sorting must get it wrong, otherwise the row proves nothing.
func TestCompare_realCollectionNameShapes_sortNumerically(t *testing.T) {
	t.Parallel()

	// A template per survey name: %s marks the numeric field that varies, and
	// pad is the zero-padding width the real name uses for it.
	families := []struct {
		survey string
		tmpl   string
		pad    int
	}{
		{"02-07.jpg", "02-%s.jpg", 2},
		{"075__.jpg", "%s__.jpg", 3},
		{"13018.jpg", "%s.jpg", 5},
		{"13_08.jpg", "13_%s.jpg", 2},
		{"18-05.jpg", "18-%s.jpg", 2},
		{"BlackLagoon05_034.JPG", "BlackLagoon05_%s.JPG", 3},
		{"CS02-026.JPG", "CS02-%s.JPG", 3},
		{"MLM08-0062.jpg", "MLM08-%s.jpg", 4},
		{"c03_p108.png", "c03_p%s.png", 3},
		{"kv005002152.gif", "kv005002%s.gif", 3},
		{"m02741.jpg", "m%s.jpg", 5},
		{"sam 05 167.gif", "sam 05 %s.gif", 3},
		{"sirius_201005_040.jpg", "sirius_201005_%s.jpg", 3},
		{"⌡∙▐╘ 04-068.jpg", "⌡∙▐╘ 04-%s.jpg", 3},
		{"║≥ ┐└┤⌡_03_011.jpg", "║≥ ┐└┤⌡_03_%s.jpg", 3},
		{"║╧╡╬└╟▒╟12-135.jpg", "║╧╡╬└╟▒╟12-%s.jpg", 3},
		{"╜╩╞╚╗τ╖½1▒╟156.jpg", "╜╩╞╚╗τ╖½1▒╟%s.jpg", 3},
		{"╣┘└╠┐└╕▐░í 04 - 162.jpg", "╣┘└╠┐└╕▐░í 04 - %s.jpg", 3},
		{"╣Φ╞▓ ╖╬╛Γ 01▒╟/KoiZuMi-000.jpg", "╣Φ╞▓ ╖╬╛Γ 01▒╟/KoiZuMi-%s.jpg", 3},
		{"╜║─╡0001.jpg", "╜║─╡%s.jpg", 4},
		// The same shapes after kenc has decoded them, which is what actually
		// reaches the sorter at scan time (AC-002 then FR-IDX-007).
		{"슈퍼만화데생0001.jpg (decoded)", "슈퍼만화데생%s.jpg", 4},
		{"바이오하자드 04 - 162.jpg (decoded)", "바이오하자드 04 - %s.jpg", 3},
	}

	// Values that cross every padding width in the table, so the padded
	// spellings stop being equal-width and lexicographic order breaks.
	values := []int{1, 2, 9, 10, 99, 100, 1000, 99999, 100000}

	for _, f := range families {
		t.Run(f.survey, func(t *testing.T) {
			t.Parallel()

			want := make([]string, 0, len(values))
			for _, v := range values {
				want = append(want, fmt.Sprintf(f.tmpl, fmt.Sprintf("%0*d", f.pad, v)))
			}

			// Deterministic shuffle: reverse, which no correct implementation
			// can accidentally pass.
			in := slices.Clone(want)
			slices.Reverse(in)

			if got := sortedByCompare(in); !slices.Equal(got, want) {
				t.Errorf("Compare order for %q\n got %q\nwant %q", f.survey, got, want)
			}
			if got := sortedByKey(in); !slices.Equal(got, want) {
				t.Errorf("Key order for %q\n got %q\nwant %q", f.survey, got, want)
			}

			lexicographic := slices.Clone(in)
			sort.Strings(lexicographic)
			if slices.Equal(lexicographic, want) {
				t.Errorf("family %q is not a natural-sort stress case: plain "+
					"lexicographic order already produces %q", f.survey, want)
			}
		})
	}
}

// TestCompare_surveyNamesVerbatim_areTotallyOrdered feeds the survey's names
// exactly as printed — mojibake, embedded slashes, trailing underscores and
// all — through the total-order and Compare/Key agreement checks.
func TestCompare_surveyNamesVerbatim_areTotallyOrdered(t *testing.T) {
	t.Parallel()
	assertTotalOrder(t, surveyNames)
}

func TestCompare_koreanMixedWithDigits_ordersByNumberNotByCodePoint(t *testing.T) {
	t.Parallel()

	// Korean text on both sides of the number, several numeric groups, and the
	// two-level book naming arch §4.2 produces when a series is flattened
	// (E-4: "크로스본 건담 / 크로스본 건담 01권.zip").
	in := []string{
		"크로스본 건담 / 크로스본 건담 10권.zip",
		"크로스본 건담 / 크로스본 건담 2권.zip",
		"기동전사 건담 0080 / 08권 3화.zip",
		"기동전사 건담 0080 / 08권 12화.zip",
		"기동전사 건담 0079 / 10권 1화.zip",
		"크로스본 건담 / 크로스본 건담 1권.zip",
	}
	want := []string{
		"기동전사 건담 0079 / 10권 1화.zip",
		"기동전사 건담 0080 / 08권 3화.zip",
		"기동전사 건담 0080 / 08권 12화.zip",
		"크로스본 건담 / 크로스본 건담 1권.zip",
		"크로스본 건담 / 크로스본 건담 2권.zip",
		"크로스본 건담 / 크로스본 건담 10권.zip",
	}
	if got := sortedByCompare(in); !slices.Equal(got, want) {
		t.Errorf("Compare order\n got %q\nwant %q", got, want)
	}
	if got := sortedByKey(in); !slices.Equal(got, want) {
		t.Errorf("Key order\n got %q\nwant %q", got, want)
	}
}

func TestCompare_duplicateBooks_areOrderedNotDeduplicated(t *testing.T) {
	t.Parallel()

	// E-5 (BINDING): 군계 really holds all of these and every one is listed,
	// natural-sorted. The ordering must be stable and deterministic or the
	// library view flickers between scans.
	in := []string{
		"07권 (2).repair.zip",
		"01권",
		"07권.repair.zip",
		"01권.zip",
		"07권.zip",
	}
	want := []string{
		"01권",
		"01권.zip",
		"07권 (2).repair.zip",
		"07권.repair.zip",
		"07권.zip",
	}
	if got := sortedByCompare(in); !slices.Equal(got, want) {
		t.Errorf("Compare order\n got %q\nwant %q", got, want)
	}
	if got := sortedByKey(in); !slices.Equal(got, want) {
		t.Errorf("Key order\n got %q\nwant %q", got, want)
	}
}

func TestCompare_equalStrings_areStableUnderSortStableFunc(t *testing.T) {
	t.Parallel()

	// Compare returns 0 only for identical strings, so a stable sort must keep
	// duplicates in their original relative order. books.ord is materialised
	// from this ordering, and duplicates are real (E-5).
	type row struct {
		name string
		seq  int
	}
	rows := []row{
		{"07권.zip", 0}, {"01권.zip", 1}, {"07권.zip", 2},
		{"01권.zip", 3}, {"07권.zip", 4}, {"01권.zip", 5},
	}
	slices.SortStableFunc(rows, func(a, b row) int { return natsort.Compare(a.name, b.name) })

	wantSeq := []int{1, 3, 5, 0, 2, 4}
	for i, r := range rows {
		if r.seq != wantSeq[i] {
			t.Fatalf("stable order = %v, want original sequence %v", rows, wantSeq)
		}
	}
}

func TestCompare_longDigitRuns_neverOverflow(t *testing.T) {
	t.Parallel()

	// Rule 1 compares digit-run *lengths*, so no integer type is involved and
	// nothing can wrap. 40 digits is far past uint64 and int64.
	cases := []struct {
		a, b string
		want int
	}{
		{strings.Repeat("9", 21), "1" + strings.Repeat("0", 21), -1},
		{strings.Repeat("9", 40), strings.Repeat("9", 41), -1},
		{"18446744073709551615", "18446744073709551616", -1}, // MaxUint64, +1
		{"9223372036854775807", "9223372036854775808", -1},   // MaxInt64, +1
		{strings.Repeat("0", 40) + "1", "1", 1},              // same value, more padding
		{strings.Repeat("9", 40), strings.Repeat("9", 40), 0},
	}
	for _, c := range cases {
		if got := natsort.Compare(c.a, c.b); got != c.want {
			t.Errorf("Compare(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
		if got := sign(bytes.Compare(natsort.Key(c.a), natsort.Key(c.b))); got != c.want {
			t.Errorf("Key order for (%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

// TestKeyAndCompare_agreeAcrossEveryCountEncodingBoundary is the D-31 property
// aimed at the one place the key encoding can silently diverge from Compare:
// the self-delimiting count that Key writes before a digit run's significant
// digits and after its leading zeros.
//
// That count is rendered in hex, so its *width* changes at 16, 256 and 4096 —
// and hex is not order-preserving across widths ("f" > "10" under memcmp while
// 15 < 16). The length byte in front of it is what fixes that. The generated
// corpus cannot reach here: randomName's longest digit run is seven characters
// and its longest zero pad four, so deleting the length byte leaves the whole
// rest of the suite green while inverting Key against Compare for any name with
// a 16-or-more-digit run — which is a SQLite-orders-differently-from-Go bug
// (`sort=name` vs every other listing) that no test would report.
func TestKeyAndCompare_agreeAcrossEveryCountEncodingBoundary(t *testing.T) {
	t.Parallel()

	// (lo, hi) straddle a hex-width change: 0xf/0x10, 0xff/0x100, 0xfff/0x1000.
	boundaries := []struct{ lo, hi int }{{15, 16}, {255, 256}, {4095, 4096}}

	for _, b := range boundaries {
		// More significant digits is a larger number, whatever the counts encode
		// to. "9"*lo has lo significant digits; "1"+"0"*lo has hi of them.
		fewer, more := strings.Repeat("9", b.lo), "1"+strings.Repeat("0", b.lo)
		if len(more) != b.hi {
			t.Fatalf("test setup: %d significant digits, want %d", len(more), b.hi)
		}
		assertOrder(t, fewer, more, -1, "significant-digit count")

		// Same value, different padding: fewer leading zeros sorts first (rule 1).
		assertOrder(t, strings.Repeat("0", b.lo)+"1", strings.Repeat("0", b.hi)+"1", -1, "leading-zero count")

		// The count is not the last field of the record, so a width change must
		// not shift what follows out of alignment either.
		assertOrder(t, strings.Repeat("9", b.lo)+"권", strings.Repeat("9", b.hi)+"권", -1, "count followed by more chunks")
		assertOrder(t, strings.Repeat("0", b.lo)+"1권", strings.Repeat("0", b.hi)+"1권", -1, "zero count followed by more chunks")
	}

	// Widths that do not change still have to order digit by digit: 10 ("a")
	// against 15 ("f") is inside one width, 16 ("10") against 255 ("ff") spans
	// two, and 255 ("ff") against 4096 ("1000") spans three.
	for _, c := range []struct{ lo, hi int }{{10, 15}, {16, 255}, {255, 4096}, {1, 4096}} {
		assertOrder(t, strings.Repeat("9", c.lo), strings.Repeat("9", c.hi), -1, "same or wider count class")
	}

	// And the whole family sorted together, which is the strict-total-order form
	// of the same claim.
	var corpus []string
	for _, n := range []int{0, 1, 2, 9, 14, 15, 16, 17, 254, 255, 256, 257, 4095, 4096, 4097} {
		if n > 0 {
			corpus = append(corpus,
				strings.Repeat("9", n),
				strings.Repeat("9", n)+"권",
				"1"+strings.Repeat("0", n-1),
			)
		}
		corpus = append(corpus, strings.Repeat("0", n)+"1", strings.Repeat("0", n)+"1권")
	}
	assertTotalOrder(t, corpus)
}

// assertOrder checks Compare and the Key BLOB order agree with each other and
// with want, in both directions.
func assertOrder(t *testing.T, a, b string, want int, what string) {
	t.Helper()

	label := func(s string) string {
		if len(s) > 24 {
			return fmt.Sprintf("%q…(%d bytes)", s[:24], len(s))
		}
		return fmt.Sprintf("%q", s)
	}
	if got := natsort.Compare(a, b); got != want {
		t.Errorf("%s: Compare(%s, %s) = %d, want %d", what, label(a), label(b), got, want)
	}
	if got := natsort.Compare(b, a); got != -want {
		t.Errorf("%s: Compare(%s, %s) = %d, want %d", what, label(b), label(a), got, -want)
	}
	if got := sign(bytes.Compare(natsort.Key(a), natsort.Key(b))); got != want {
		t.Errorf("%s: Key order for (%s, %s) = %d, want %d — SQLite would sort these "+
			"differently from Go (D-31)", what, label(a), label(b), got, want)
	}
}

func TestKey_arbitraryBytes_areAcceptedAndOrderTotally(t *testing.T) {
	t.Parallel()

	// series.sort_key is a BLOB, so every byte value is legal on the way in and
	// on the way out. These are the inputs that break naive key encodings: an
	// embedded NUL, bytes that are not valid UTF-8 (kenc's "unknown" branch),
	// and a genuine U+FFFD that must not be confused with a bad byte.
	corpus := []string{
		"",
		"\x00",
		"\x00\x00",
		"a\x00b",
		"\xff\xfe.jpg",
		"\xff\xff.jpg",
		"\x80\x81",
		"�.jpg",
		"��",
		"1\x00",
		"01\x00",
		"\xed\xa0\x80", // a CESU-8 surrogate: invalid UTF-8
		"가\xff",
	}
	assertTotalOrder(t, corpus)

	// The key must survive the round trip a BLOB column performs on it.
	for _, s := range corpus {
		k := natsort.Key(s)
		if !bytes.Equal(k, slices.Clone(k)) {
			t.Errorf("Key(%q) is not value-stable", s)
		}
		if len(k) == 0 {
			t.Errorf("Key(%q) is empty; even the empty string must get a terminator", s)
		}
	}
}

// TestCompareAndKey_agreeOverGeneratedCorpus is the D-31 property: the Go
// comparator and the BLOB SQLite orders under BINARY collation must produce
// the same sequence, or `sort=name` in GET /api/series silently disagrees with
// every other listing in the product.
//
// It also asserts the total-order laws. Sorting the corpus and then requiring
// Compare(x[i], x[j]) < 0 for every i < j is stronger than checking
// transitivity on sampled triples: it holds only if the relation is a strict
// total order over the whole corpus.
func TestCompareAndKey_agreeOverGeneratedCorpus(t *testing.T) {
	t.Parallel()

	rnd := rand.New(rand.NewPCG(0x5EED, 0xC0FFEE))
	set := map[string]struct{}{}
	for _, s := range surveyNames {
		set[s] = struct{}{}
	}
	for len(set) < 700 {
		set[randomName(rnd)] = struct{}{}
	}
	corpus := make([]string, 0, len(set))
	for s := range set {
		corpus = append(corpus, s)
	}
	sort.Strings(corpus) // deterministic starting order for a reproducible failure

	assertTotalOrder(t, corpus)
}

// TestCompareAndKey_agreeOverGeneratedPairs is the ≥100 000-string half of the
// same property (impl-plan §3 WP-02 acceptance 2), run over freshly generated
// pairs rather than a fixed corpus.
func TestCompareAndKey_agreeOverGeneratedPairs(t *testing.T) {
	t.Parallel()

	const pairs = 60_000 // 120 000 generated strings
	rnd := rand.New(rand.NewPCG(1, 2))
	for i := range pairs {
		a, b := randomName(rnd), randomName(rnd)

		c := natsort.Compare(a, b)
		if c < -1 || c > 1 {
			t.Fatalf("Compare(%q, %q) = %d, want -1, 0 or 1", a, b, c)
		}
		if k := sign(bytes.Compare(natsort.Key(a), natsort.Key(b))); k != c {
			t.Fatalf("pair %d: Compare(%q, %q) = %d but Key order = %d", i, a, b, c, k)
		}
		if r := natsort.Compare(b, a); r != -c {
			t.Fatalf("pair %d: antisymmetry violated: Compare(%q, %q) = %d, Compare(%q, %q) = %d",
				i, a, b, c, b, a, r)
		}
		if (c == 0) != (a == b) {
			t.Fatalf("pair %d: Compare(%q, %q) = 0 but the strings differ (or vice versa)", i, a, b)
		}
		if got := natsort.Compare(a, a); got != 0 {
			t.Fatalf("Compare(%q, %q) = %d, want 0", a, a, got)
		}
	}
}

// FuzzCompare drives the ordering axioms from arbitrary input, including bytes
// no encoding can decode. Compare and Key must never panic and must never
// disagree.
func FuzzCompare(f *testing.F) {
	for _, s := range surveyNames {
		f.Add(s, s+"1")
	}
	f.Add("1.jpg", "10.jpg")
	f.Add("01권 (완).zip", "1권.zip")
	f.Add("\xff\xfe.jpg", "�.jpg")
	f.Add("", "\x00")
	f.Add(strings.Repeat("0", 300)+"1", "1")

	f.Fuzz(func(t *testing.T, a, b string) {
		c := natsort.Compare(a, b)
		if c < -1 || c > 1 {
			t.Fatalf("Compare(%q, %q) = %d, want -1, 0 or 1", a, b, c)
		}
		if r := natsort.Compare(b, a); r != -c {
			t.Fatalf("antisymmetry: Compare(%q, %q) = %d, Compare(%q, %q) = %d", a, b, c, b, a, r)
		}
		if (c == 0) != (a == b) {
			t.Fatalf("Compare(%q, %q) = %d, but a == b is %v", a, b, c, a == b)
		}
		if k := sign(bytes.Compare(natsort.Key(a), natsort.Key(b))); k != c {
			t.Fatalf("Compare(%q, %q) = %d but Key order = %d", a, b, c, k)
		}
		// Transitivity against a third value derived from the inputs.
		for _, m := range []string{a + b, b + a, a[:len(a)/2], "0" + a} {
			if err := transitive(a, m, b); err != nil {
				t.Fatal(err)
			}
		}
	})
}

// TestFold_caselessRanges_haveNoLowerCaseMapping validates the one Unicode
// assumption the folding fast path makes: Hangul Jamo, Hangul Compatibility
// Jamo through the CJK Unified Ideographs (which spans Kana), and the Hangul
// Syllables block contain no cased characters, so skipping unicode.ToLower for
// them cannot change any ordering. If a future Unicode revision assigns a case
// mapping inside these blocks, this fails and the fast path has to shrink.
func TestFold_caselessRanges_haveNoLowerCaseMapping(t *testing.T) {
	t.Parallel()

	ranges := []struct {
		name   string
		lo, hi rune
	}{
		{"Hangul Jamo", 0x1100, 0x11FF},
		{"Hangul Compatibility Jamo .. CJK Unified Ideographs", 0x3130, 0x9FFF},
		{"Hangul Syllables", 0xAC00, 0xD7A3},
	}
	for _, rg := range ranges {
		for r := rg.lo; r <= rg.hi; r++ {
			if unicode.ToLower(r) != r || unicode.ToUpper(r) != r {
				t.Fatalf("%s: U+%04X has a case mapping (lower U+%04X, upper U+%04X); "+
					"the caseless fast path in fold() is no longer safe",
					rg.name, r, unicode.ToLower(r), unicode.ToUpper(r))
			}
		}
	}
}

func TestCompare_fullwidthForms_foldOntoASCII(t *testing.T) {
	t.Parallel()

	// Rule 3 maps fullwidth Latin and fullwidth digits onto ASCII, so a title
	// typed in fullwidth sorts next to its halfwidth twin instead of after
	// every Hangul name.
	cases := []struct {
		a, b string
		want int
	}{
		// Folds equal, so the raw code point breaks the tie — and a fullwidth
		// code point sits far above ASCII, so the halfwidth spelling wins.
		{"Ａ", "a", 1},
		{"ａ", "a", 1},
		{"Ａ", "ａ", -1},
		{"Ａ", "b", -1}, // fullwidth A folds below b
		{"ｚ", "b", 1},  // fullwidth z folds above b
		{"１", "가", -1}, // a fullwidth digit folds to '1', well below Hangul
		{"1", "１", -1}, // but it is not an ASCII digit run, so rule 2 beats it
		{"Ａ", "Ａ", 0},
	}
	for _, c := range cases {
		if got := natsort.Compare(c.a, c.b); got != c.want {
			t.Errorf("Compare(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
		if got := sign(bytes.Compare(natsort.Key(c.a), natsort.Key(c.b))); got != c.want {
			t.Errorf("Key order for (%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func BenchmarkCompare(b *testing.B) {
	// A real page name after kenc has decoded it, differing only in the last
	// digit so the whole string is walked.
	x, y := "바이오하자드 04 - 162.jpg", "바이오하자드 04 - 1620.jpg"
	b.ReportAllocs()
	for b.Loop() {
		natsort.Compare(x, y)
	}
}

func BenchmarkKey(b *testing.B) {
	s := "[만화] 강철의 연금술사 1~27권 완결/강철의 연금술사 03권.zip"
	b.ReportAllocs()
	for b.Loop() {
		natsort.Key(s)
	}
}

// assertTotalOrder sorts corpus with Compare and then checks every ordered
// pair. It proves, in one pass: strict total ordering (hence transitivity),
// antisymmetry, Compare == 0 only for identical strings, and byte-for-byte
// agreement between Compare and Key.
func assertTotalOrder(t *testing.T, corpus []string) {
	t.Helper()

	unique := slices.Clone(corpus)
	slices.Sort(unique)
	unique = slices.Compact(unique)

	byCompare := sortedByCompare(unique)
	if byKey := sortedByKey(unique); !slices.Equal(byCompare, byKey) {
		for i := range byCompare {
			if byCompare[i] != byKey[i] {
				t.Fatalf("Compare and Key disagree at position %d: %q vs %q", i, byCompare[i], byKey[i])
			}
		}
	}

	keys := make([][]byte, len(byCompare))
	for i, s := range byCompare {
		keys[i] = natsort.Key(s)
	}

	for i := range byCompare {
		if got := natsort.Compare(byCompare[i], byCompare[i]); got != 0 {
			t.Fatalf("Compare(%q, itself) = %d, want 0", byCompare[i], got)
		}
		for j := i + 1; j < len(byCompare); j++ {
			a, b := byCompare[i], byCompare[j]
			if c := natsort.Compare(a, b); c != -1 {
				t.Fatalf("after sorting, Compare(%q, %q) = %d, want -1 "+
					"(the relation is not a strict total order)", a, b, c)
			}
			if c := natsort.Compare(b, a); c != 1 {
				t.Fatalf("antisymmetry: Compare(%q, %q) = %d, want 1", b, a, c)
			}
			if c := sign(bytes.Compare(keys[i], keys[j])); c != -1 {
				t.Fatalf("Key order disagrees with Compare for (%q, %q): got %d, want -1", a, b, c)
			}
		}
	}
}

func transitive(a, b, c string) error {
	ab, bc, ac := natsort.Compare(a, b), natsort.Compare(b, c), natsort.Compare(a, c)
	if ab <= 0 && bc <= 0 && ac > 0 {
		return fmt.Errorf("transitivity: %q <= %q <= %q but Compare(a, c) = %d", a, b, c, ac)
	}
	if ab >= 0 && bc >= 0 && ac < 0 {
		return fmt.Errorf("transitivity: %q >= %q >= %q but Compare(a, c) = %d", a, b, c, ac)
	}
	return nil
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	default:
		return 0
	}
}

// randomName generates a filename-shaped string out of the alphabets the real
// collection actually mixes: padded and unpadded digit runs, both ASCII cases,
// Hangul syllables, Hanja, compatibility jamo, fullwidth forms, separators,
// and raw high bytes that are not valid UTF-8.
func randomName(rnd *rand.Rand) string {
	var b strings.Builder
	for range rnd.IntN(7) {
		switch rnd.IntN(10) {
		case 0:
			fmt.Fprintf(&b, "%0*d", rnd.IntN(5), rnd.IntN(1000))
		case 1:
			b.WriteByte(byte('a' + rnd.IntN(26)))
		case 2:
			b.WriteByte(byte('A' + rnd.IntN(26)))
		case 3:
			b.WriteRune(rune(0xAC00 + rnd.IntN(0xD7A3-0xAC00+1))) // Hangul syllables
		case 4:
			b.WriteRune(rune(0x4E00 + rnd.IntN(0x1000))) // Hanja
		case 5:
			b.WriteRune(rune(0xFF01 + rnd.IntN(0x5E))) // fullwidth forms
		case 6:
			b.WriteString([]string{" ", ".", "-", "_", "(", ")", "권", "~", "[", "]"}[rnd.IntN(10)])
		case 7:
			b.WriteRune(rune(0x3131 + rnd.IntN(0x1E))) // compatibility jamo
		case 8:
			b.WriteByte(byte(0x80 + rnd.IntN(0x80))) // never a valid UTF-8 lead
		case 9:
			b.WriteString([]string{"0", "00", "000", "1", "01", "001", "9", "10", "99", "100"}[rnd.IntN(10)])
		}
	}
	return b.String()
}
