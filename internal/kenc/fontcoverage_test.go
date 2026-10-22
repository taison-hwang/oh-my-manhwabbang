package kenc

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// The vendored 한자/가나 faces (web/src/assets/fonts/noto-*) are cut from a
// character set that scripts/mkfonts/subset.py derives by round-tripping
// Python's codecs, and records in cjk-coverage.txt. This package decodes with
// x/text's. Those are two implementations of "CP949 or Shift_JIS", and they
// drifted once: subset.py used Python's strict `shift_jis` while x/text's
// ShiftJIS is Windows-31J, so 265 ideographs this package can produce had no
// glyph in the product's face.
//
// So the codecs this package actually holds are the specification, and the
// manifest must say exactly what they can produce — no fewer (tofu in a
// title) and no more (bytes in the binary nothing can ask for). The other half
// of the chain, that every woff2 file carries what the manifest lists, is
// web/src/styles/fontCoverage.test.ts reading the files' cmaps.
func TestDecodableIdeographs_AreTheVendoredFontSubset(t *testing.T) {
	t.Parallel()

	want := map[rune]bool{}
	for _, enc := range []struct {
		name string
		dec  func([]byte) (string, error)
	}{
		{"cp932", func(b []byte) (string, error) { return cp932.NewDecoder().String(string(b)) }},
		{"cp949", func(b []byte) (string, error) { return cp949.NewDecoder().String(string(b)) }},
	} {
		n := 0
		for hi := 0x80; hi <= 0xFF; hi++ {
			for lo := 0x00; lo <= 0xFF; lo++ {
				s, err := enc.dec([]byte{byte(hi), byte(lo)})
				if err != nil || utf8.RuneCountInString(s) != 1 {
					continue
				}
				if r, _ := utf8.DecodeRuneInString(s); isIdeograph(r) {
					want[r] = true
					n++
				}
			}
		}
		// A decoder that suddenly produces nothing would make the comparison
		// below vacuous for its half; x/text's tables are thousands deep.
		if n < 4000 {
			t.Fatalf("%s produced only %d ideographs from every two-byte sequence", enc.name, n)
		}
	}

	path := filepath.Join("..", "..", "scripts", "mkfonts", "cjk-coverage.txt")
	got := map[rune]bool{}
	for r := range readCoverage(t, path) {
		if isIdeograph(r) {
			got[r] = true
		}
	}

	if missing := difference(want, got); len(missing) > 0 {
		t.Errorf("%d ideographs internal/kenc can decode are not in %s, so a title holding one "+
			"falls through the font's unicode-range to the reader's system face: %s\n"+
			"Re-run scripts/mkfonts/subset.py with the codecs this package uses.",
			len(missing), path, sample(missing))
	}
	if extra := difference(got, want); len(extra) > 0 {
		t.Errorf("%d ideographs in %s cannot come out of internal/kenc's codecs, so the font "+
			"carries glyphs no legacy name can ask for: %s", len(extra), path, sample(extra))
	}
}

// isIdeograph is the three Han blocks the vendored faces' unicode-range
// claims (subset.py UNICODE_RANGE_BLOCKS, fonts.css).
func isIdeograph(r rune) bool {
	return (r >= 0x3400 && r <= 0x4DBF) || (r >= 0x4E00 && r <= 0x9FFF) || (r >= 0xF900 && r <= 0xFAFF)
}

// readCoverage parses subset.py's manifest: one hexadecimal code point per
// line, '#' starting a comment.
func readCoverage(t *testing.T, path string) map[rune]bool {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("the font subset's manifest: %v (scripts/mkfonts/subset.py writes it)", err)
	}
	defer f.Close()
	out := map[rune]bool{}
	sc := bufio.NewScanner(f)
	for line := 1; sc.Scan(); line++ {
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		cp, err := strconv.ParseUint(text, 16, 32)
		if err != nil {
			t.Fatalf("%s:%d: %q is not a hexadecimal code point", path, line, text)
		}
		out[rune(cp)] = true
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func difference(a, b map[rune]bool) []rune {
	var out []rune
	for r := range a {
		if !b[r] {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func sample(rs []rune) string {
	var b strings.Builder
	for i, r := range rs {
		if i == 12 {
			fmt.Fprintf(&b, " … and %d more", len(rs)-i)
			break
		}
		fmt.Fprintf(&b, "%c(U+%04X) ", r, r)
	}
	return b.String()
}
