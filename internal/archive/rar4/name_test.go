package rar4

import (
	"bytes"
	"strings"
	"testing"

	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/transform"

	"shelf/internal/kenc"
)

// Real name fields, lifted byte for byte out of the collection's archives, with
// the reading rardecode produces from the same bytes as the expected value.
//
// They are here rather than round-tripped through an encoder written for the
// test on purpose. An encoder of mine could be wrong in exactly the way the
// decoder is wrong and the test would still pass; WinRAR's output checked
// against a second implementation cannot agree with a mistake of mine by
// accident.
//
// The Wolf Guy vector is the one that justifies the decoder existing at all.
// Its OEM prefix is `[????x?????] ????? - ???? ?01?` — the packer's code page
// could not spell the name, so it wrote question marks — while the companion
// field carries the real 平井和正×泉谷あゆみ. Decoding the prefix, however
// carefully, recovers nothing.
var realNameVectors = []struct {
	name string
	oem  []byte
	enc  []byte
	want string
}{
	{
		name: "CP949 with a long ASCII run",
		oem: []byte{
			0xb4, 0xed, 0xbd, 0xcc, 0xc6, 0xfa, 0xb8, 0xae, 0xbd, 0xba, 0xb8, 0xc7,
			0x28, 0x44, 0x61, 0x6e, 0x63, 0x69, 0x6e, 0x67, 0x5f, 0x50, 0x6f, 0x6c,
			0x69, 0x63, 0x65, 0x6d, 0x61, 0x6e, 0x29, 0x5c, 0x30, 0x30, 0x30, 0x2e,
			0x6a, 0x70, 0x67,
		},
		enc: []byte{
			0xb3, 0x6a, 0x04, 0xf1, 0xc2, 0xf4, 0xd3, 0xac, 0xb9, 0xa0, 0xa4, 0xc2,
			0xe8, 0xb9, 0x28, 0x44, 0x00, 0x61, 0x6e, 0x63, 0x69, 0x00, 0x6e, 0x67,
			0x5f, 0x50, 0x00, 0x6f, 0x6c, 0x69, 0x63, 0x00, 0x65, 0x6d, 0x61, 0x6e,
			0x00, 0x29, 0x5c, 0x30, 0x30, 0x00, 0x30, 0x2e, 0x6a, 0x70, 0x00, 0x67,
		},
		want: "댄싱폴리스맨(Dancing_Policeman)/000.jpg",
	},
	{
		name: "CP949 with brackets and a space",
		oem: []byte{
			0xb6, 0xf3, 0xc1, 0xa6, 0xc6, 0xf9, 0x20, 0x31, 0xb1, 0xc7, 0x5b, 0xb9,
			0xf8, 0xbf, 0xaa, 0x5d, 0x5c, 0x30, 0x30, 0x31, 0x2e, 0x6a, 0x70, 0x67,
		},
		enc: []byte{
			0xb7, 0x68, 0x7c, 0x1c, 0xc8, 0xf0, 0xd3, 0x20, 0x22, 0x31, 0x8c, 0xad,
			0x5b, 0x88, 0xbc, 0x80, 0xed, 0xc5, 0x5d, 0x5c, 0x30, 0x00, 0x30, 0x31,
			0x2e, 0x6a, 0x00, 0x70, 0x67,
		},
		want: "라제폰 1권[번역]/001.jpg",
	},
	{
		name: "Japanese the OEM prefix could not spell",
		oem: []byte{
			0x57, 0x6f, 0x6c, 0x66, 0x5f, 0x47, 0x75, 0x79, 0x5f, 0x2d, 0x5f, 0x57,
			0x6f, 0x6c, 0x66, 0x65, 0x6e, 0x5f, 0x43, 0x72, 0x65, 0x73, 0x74, 0x5f,
			0x76, 0x30, 0x31, 0x5f, 0x4a, 0x50, 0x5c, 0x5b, 0x3f, 0x3f, 0x3f, 0x3f,
			0x78, 0x3f, 0x3f, 0x3f, 0x3f, 0x3f, 0x5d, 0x20, 0x3f, 0x3f, 0x3f, 0x3f,
			0x3f, 0x20, 0x2d, 0x20, 0x3f, 0x3f, 0x3f, 0x3f, 0x20, 0x3f, 0x30, 0x31,
			0x3f, 0x5c, 0x30, 0x30, 0x30, 0x2e, 0x6a, 0x70, 0x67,
		},
		enc: []byte{
			0x5e, 0xda, 0x1e, 0x73, 0x95, 0x4e, 0x8c, 0x54, 0x8a, 0x63, 0x6b, 0xd7,
			0xc9, 0x6c, 0x37, 0x8c, 0xab, 0x42, 0x30, 0x86, 0x30, 0x7f, 0x30, 0x00,
			0xaa, 0xa6, 0x30, 0xeb, 0x30, 0xd5, 0x30, 0xac, 0x30, 0xba, 0xa4, 0x30,
			0x01, 0xfc, 0x72, 0x6e, 0x30, 0xa2, 0x0b, 0x7d, 0xe0, 0x7a, 0x20, 0x2c,
			0x7b, 0xec, 0x00, 0xfb, 0x5d, 0x06,
		},
		want: "Wolf_Guy_-_Wolfen_Crest_v01_JP/[平井和正×泉谷あゆみ] ウルフガイ - 狼の紋章 第01巻/000.jpg",
	},
}

func TestDecodeName_realArchiveVectors(t *testing.T) {
	for _, tc := range realNameVectors {
		t.Run(tc.name, func(t *testing.T) {
			raw := append(append(append([]byte(nil), tc.oem...), 0), tc.enc...)
			got, enc := decodeName(raw, true)
			if got != tc.want {
				t.Errorf("decodeName() = %q\n            want %q", got, tc.want)
			}
			if enc != kenc.EncUTF8 {
				t.Errorf("NameEncoding = %q, want %q", enc, kenc.EncUTF8)
			}
		})
	}
}

// The companion is authoritative, so the OEM prefix must not be evidence for
// the per-archive Shift_JIS vote and must never be rewritten by it. That is
// what keeps a Japanese RAR whose prefix is question marks from dragging a
// whole archive to a wrong encoding.
func TestLegacyBytes_companionNamesAreNotEvidence(t *testing.T) {
	tc := realNameVectors[2]
	raw := append(append(append([]byte(nil), tc.oem...), 0), tc.enc...)

	got := legacyBytes(raw, true)
	if !bytes.Equal(got, tc.oem) {
		t.Errorf("legacyBytes returned % x, want the OEM prefix % x", got, tc.oem)
	}
	// And the entry it produces is UTF-8, which is the encoding
	// resolveArchiveNames skips.
	if _, enc := decodeName(raw, true); enc != kenc.EncUTF8 {
		t.Errorf("encoding = %q, want %q so the archive vote ignores it", enc, kenc.EncUTF8)
	}
}

func TestDecodeName_flaggedUnicodeWithoutCompanion(t *testing.T) {
	// The flag is set but there is no NUL: the field is plain UTF-8.
	raw := []byte("책/한글.jpg")
	got, enc := decodeName(raw, true)
	if want := "책/한글.jpg"; got != want {
		t.Errorf("decodeName() = %q, want %q", got, want)
	}
	if enc != kenc.EncUTF8 {
		t.Errorf("NameEncoding = %q, want %q", enc, kenc.EncUTF8)
	}
}

func TestDecodeName_unflaggedIsLegacy(t *testing.T) {
	cp949 := []byte{0xC7, 0xD1, 0xB1, 0xDB, '.', 'j', 'p', 'g'} // 한글.jpg
	got, enc := decodeName(cp949, false)
	if want := "한글.jpg"; got != want {
		t.Errorf("decodeName() = %q, want %q", got, want)
	}
	if enc != kenc.EncCP949 {
		t.Errorf("NameEncoding = %q, want %q", enc, kenc.EncCP949)
	}
}

// A companion that runs off the end of its buffer is not a name. Falling back
// to the OEM prefix keeps the entry; returning half a decoded string would put
// a corrupted page name in the index and in the API.
func TestDecodeName_brokenCompanionFallsBackToPrefix(t *testing.T) {
	oem := []byte{0xC7, 0xD1, 0xB1, 0xDB, '.', 'j', 'p', 'g'} // 한글.jpg
	broken := []byte{0xB3, 0xFF}                              // high byte, then an opcode stream that ends mid-run
	raw := append(append(append([]byte(nil), oem...), 0), broken...)

	got, enc := decodeName(raw, true)
	if got == "" {
		t.Fatal("decodeName returned an empty name; the entry would be lost")
	}
	if strings.Contains(got, "\x00") {
		t.Errorf("decodeName() = %q, which contains a NUL", got)
	}
	t.Logf("fell back to %q (encoding %q)", got, enc)
}

func TestDecodeEncodedName_refusesRunsPastTheOEMName(t *testing.T) {
	// Opcode 3 with length 0 copies 2 bytes from an OEM name that has none.
	// Returning ok=false is what makes the caller fall back rather than read
	// out of bounds.
	if _, ok := decodeEncodedName(nil, []byte{0x00, 0xC0, 0x00}); ok {
		t.Error("decodeEncodedName accepted a run with no OEM name to copy from")
	}
}

func TestDecodeEncodedName_emptyStream(t *testing.T) {
	if _, ok := decodeEncodedName([]byte("abc"), nil); ok {
		t.Error("decodeEncodedName accepted an empty companion")
	}
}

func TestNormalizeSeparators(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`a\b\c.jpg`, "a/b/c.jpg"},
		{"a/b/c.jpg", "a/b/c.jpg"},
		{"plain.jpg", "plain.jpg"},
		{"", ""},
	} {
		if got := normalizeSeparators(tc.in); got != tc.want {
			t.Errorf("normalizeSeparators(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Shift_JIS trailing bytes may be 0x5C, which is `\`. 表 is 0x95 0x5C, the
// classic case, and it is the reason normalizeSeparators runs on the decoded
// string and never on the raw bytes.
//
// The assertion is at archive level, not on decodeName, because that is where
// the guarantee actually lives. The first pass reads these names as CP949, gets
// them wrong, and may well turn a 0x5C into a separator — and then
// resolveArchiveNames re-decodes from RawName, not from the damaged string, so
// the wrong reading is discarded whole rather than patched. Asserting on
// decodeName alone would be asserting on an intermediate value the product
// never shows anyone.
func TestReadIndex_shiftJISNamesKeepTheirTrailingBackslashBytes(t *testing.T) {
	enc := japanese.ShiftJIS.NewEncoder()
	sjis := func(s string) []byte {
		b, _, err := transform.Bytes(enc, []byte(s))
		if err != nil {
			t.Fatalf("encoding %q to Shift_JIS: %v", s, err)
		}
		return b
	}

	// 表 (0x95 0x5C) and 十 (0x8F 0x5C) both end in 0x5C. The rest give
	// kenc.ArchiveFallback the fullwidth evidence it requires before it will
	// convict an archive of being Japanese.
	want := []string{
		"表紙.jpg",
		"第十巻 狼の紋章/001.jpg",
		"第十巻 狼の紋章/002.jpg",
	}
	b := newBuilder(t).mainHeader(0)
	for _, n := range want {
		b = b.file(entryOpt{rawName: sjis(n), data: []byte("x")})
	}
	raw := b.endArc().bytes()

	ix, err := readIndex(t, raw)
	if err != nil {
		t.Fatalf("ReadIndex: %v", err)
	}
	if len(ix.Entries) != len(want) {
		t.Fatalf("got %d entries, want %d", len(ix.Entries), len(want))
	}
	for i, w := range want {
		e := ix.Entries[i]
		if e.Name != w {
			t.Errorf("entry %d = %q, want %q", i, e.Name, w)
		}
		if e.NameEncoding != kenc.EncCP932 {
			t.Errorf("entry %d encoding = %q, want %q", i, e.NameEncoding, kenc.EncCP932)
		}
	}
	// The one that matters: 表紙.jpg has no separator in it at all.
	if strings.Contains(ix.Entries[0].Name, "/") {
		t.Errorf("entry 0 = %q — a Shift_JIS trailing 0x5C became a separator",
			ix.Entries[0].Name)
	}
}
