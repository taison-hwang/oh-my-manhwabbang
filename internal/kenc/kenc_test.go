package kenc_test

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"

	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/transform"

	"shelf/internal/kenc"
	"shelf/internal/testutil"
)

// The byte-exact golden vectors arch §4.4 and impl-plan §3 WP-02 acceptance 3
// pin. These are what a 2010s Korean archiver wrote into the central directory
// with general-purpose bit 11 clear.
var (
	cp949SuperManhwa = []byte("\xbd\xb4\xc6\xdb\xb8\xb8\xc8\xad\xb5\xa5\xbb\xfd") // 슈퍼만화데생
	cp949Hangul      = []byte("\xc7\xd1\xb1\xdb.jpg")                             // 한글.jpg
)

// TestDecodeEntryName_decisionTable covers all six branches of arch §4.4.
func TestDecodeEntryName_decisionTable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		raw      []byte
		utf8Flag bool
		wantName string
		wantEnc  string
	}{
		{
			name:     "branch 1: flag set and the bytes are valid UTF-8",
			raw:      []byte("한글.jpg"),
			utf8Flag: true,
			wantName: "한글.jpg",
			wantEnc:  kenc.EncUTF8,
		},
		{
			name: "branch 2: flag set but the producer lied",
			// The CP949 bytes with bit 11 set anyway: a real archiver bug. The
			// declared encoding is still the best evidence, so the name is
			// repaired lossily rather than silently re-guessed.
			//
			// c7 stands alone (FFFD), d1 b1 happens to be a well-formed 2-byte
			// sequence (U+0471), db stands alone (FFFD). That is exactly what
			// makes branch 2 worth having: the repair is per invalid run, not
			// wholesale, so whatever was legible stays legible.
			raw:      cp949Hangul,
			utf8Flag: true,
			wantName: "�ѱ�.jpg",
			wantEnc:  kenc.EncUTF8Invalid,
		},
		{
			name:     "branch 3: no flag, non-ASCII, but valid UTF-8 -> UTF-8",
			raw:      []byte("한글.jpg"),
			utf8Flag: false,
			wantName: "한글.jpg",
			wantEnc:  kenc.EncUTF8,
		},
		{
			name:     "branch 4: no flag, pure ASCII",
			raw:      []byte("001.jpg"),
			utf8Flag: false,
			wantName: "001.jpg",
			wantEnc:  kenc.EncUTF8,
		},
		{
			name:     "branch 5: no flag, not UTF-8, clean CP949",
			raw:      cp949Hangul,
			utf8Flag: false,
			wantName: "한글.jpg",
			wantEnc:  kenc.EncCP949,
		},
		{
			name: "branch 6: no flag, not UTF-8, CP949 substitutes",
			// The exact bytes the arch spike used to prove the decoder returns
			// no error while emitting U+FFFD.
			raw:      []byte("\xff\xfe.jpg"),
			utf8Flag: false,
			wantName: "�.jpg",
			wantEnc:  kenc.EncUnknown,
		},
		{
			name:     "empty name, flag clear",
			raw:      []byte{},
			utf8Flag: false,
			wantName: "",
			wantEnc:  kenc.EncUTF8,
		},
		{
			name:     "nil name, flag set",
			raw:      nil,
			utf8Flag: true,
			wantName: "",
			wantEnc:  kenc.EncUTF8,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotName, gotEnc := kenc.DecodeEntryName(tc.raw, tc.utf8Flag)
			if gotName != tc.wantName {
				t.Errorf("name = %q (% x), want %q", gotName, gotName, tc.wantName)
			}
			if gotEnc != tc.wantEnc {
				t.Errorf("enc = %q, want %q", gotEnc, tc.wantEnc)
			}
			if !utf8.ValidString(gotName) {
				t.Errorf("name %q is not valid UTF-8; it is stored in pages.name and marshalled to JSON", gotName)
			}
		})
	}
}

// TestDecodeEntryName_cp949GoldenVectors_decodeByteExactly is AC-002: the
// Korean names inside 2014–2018 archives must come out intact, with zero
// U+FFFD.
func TestDecodeEntryName_cp949GoldenVectors_decodeByteExactly(t *testing.T) {
	t.Parallel()

	// Real name shapes from data-survey §3/§5, encoded exactly as the archiver
	// wrote them. testutil.CP949 is the encode direction of the same table.
	vectors := []struct {
		raw  []byte
		want string
	}{
		{cp949SuperManhwa, "슈퍼만화데생"},
		{cp949Hangul, "한글.jpg"},
		{testutil.CP949(t, "슈퍼만화데생0001.jpg"), "슈퍼만화데생0001.jpg"},
		{testutil.CP949(t, "세월을 잊은"), "세월을 잊은"},
		{testutil.CP949(t, "군계(軍鷄) 01권/001.jpg"), "군계(軍鷄) 01권/001.jpg"},
		{testutil.CP949(t, "바이오하자드 04 - 162.jpg"), "바이오하자드 04 - 162.jpg"},
		{testutil.CP949(t, "시티 헌터 완전판 08권/CS02-026.JPG"), "시티 헌터 완전판 08권/CS02-026.JPG"},
		{testutil.CP949(t, "전설의 용자의 전설 03/003.jpg"), "전설의 용자의 전설 03/003.jpg"},
		{testutil.CP949(t, "밴드 로열 01권/KoiZuMi-000.jpg"), "밴드 로열 01권/KoiZuMi-000.jpg"},
		{testutil.CP949(t, "I'll(아일) 09권.zip"), "I'll(아일) 09권.zip"},
	}

	for _, v := range vectors {
		gotName, gotEnc := kenc.DecodeEntryName(v.raw, false)
		if gotName != v.want {
			t.Errorf("DecodeEntryName(% x, false) name = %q, want %q", v.raw, gotName, v.want)
		}
		if strings.ContainsRune(gotName, utf8.RuneError) {
			t.Errorf("DecodeEntryName(% x, false) produced U+FFFD in %q; AC-002 requires zero", v.raw, gotName)
		}
		wantEnc := kenc.EncCP949
		if utf8.Valid(v.raw) {
			wantEnc = kenc.EncUTF8 // pure-ASCII vectors take the probe branch
		}
		if gotEnc != wantEnc {
			t.Errorf("DecodeEntryName(% x, false) enc = %q, want %q", v.raw, gotEnc, wantEnc)
		}
	}
}

// TestCP949GoldenBytes_matchTheArchSpec asserts the fixture encoder and the
// hard-coded vectors are the same table, so the rest of the golden vectors —
// which are generated — are trustworthy.
func TestCP949GoldenBytes_matchTheArchSpec(t *testing.T) {
	t.Parallel()

	if got := testutil.CP949(t, "슈퍼만화데생"); !bytes.Equal(got, cp949SuperManhwa) {
		t.Errorf("CP949(\"슈퍼만화데생\") = % x, want % x", got, cp949SuperManhwa)
	}
	if got := testutil.CP949(t, "한글.jpg"); !bytes.Equal(got, cp949Hangul) {
		t.Errorf("CP949(\"한글.jpg\") = % x, want % x", got, cp949Hangul)
	}
}

// TestDecodeEntryName_noFlagValidUTF8_returnsUTF8 is the step-2-before-step-3
// requirement stated on its own, because getting it backwards is silent
// corruption rather than a crash: every one of these names decodes into
// plausible mojibake if CP949 is tried first.
func TestDecodeEntryName_noFlagValidUTF8_returnsUTF8(t *testing.T) {
	t.Parallel()

	for _, want := range []string{
		"한글.jpg",
		"슈퍼만화데생/001.jpg",
		"[만화] 군계 1~25/군계(軍鷄) 01권.zip",
		"강철의 연금술사 03권/013.jpg",
		"日本語のファイル名.jpg",
	} {
		gotName, gotEnc := kenc.DecodeEntryName([]byte(want), false)
		if gotName != want || gotEnc != kenc.EncUTF8 {
			t.Errorf("DecodeEntryName(%q, false) = (%q, %q), want (%q, %q)",
				want, gotName, gotEnc, want, kenc.EncUTF8)
		}

		// Show what the missing probe would have produced, so the test
		// documents the failure it prevents rather than just asserting.
		mojibake, _, err := transform.Bytes(korean.EUCKR.NewDecoder(), []byte(want))
		if err == nil && string(mojibake) == want {
			t.Errorf("%q is unchanged by the CP949 decoder, so it cannot show that "+
				"the UTF-8 probe is load-bearing", want)
		}
	}
}

// TestDecodeEntryName_utf8ProbeFirst_hasFalsePositives measures the cost side
// of the mandated step-2-before-step-3 order, which the tests above only ever
// exercise from its winning side.
//
// UTF-8 validity is a property of bytes, not of intent, so a CP949 name whose
// bytes happen to parse as UTF-8 is read as UTF-8 — silently, and labelled
// "utf-8" so nothing downstream can tell. This test pins how large that hole
// is (345 of 11,172 syllables) and what falling into it looks like, so the
// package comment's claim is a measurement rather than a hope, and so a future
// x/text table change that widens the hole is reported rather than absorbed.
//
// It is deliberately NOT a change request: arch §4.4, D-24 and impl-plan §3
// WP-02 acceptance 3 all require the probe to come first, and reversing it
// would corrupt every flagless-but-UTF-8 archive instead — a systematic failure
// traded for a rare one.
func TestDecodeEntryName_utf8ProbeFirst_hasFalsePositives(t *testing.T) {
	t.Parallel()

	// The exact worked example from the package comment: CP949("징.jpg") is a
	// well-formed two-byte UTF-8 sequence followed by ASCII.
	raw := testutil.CP949(t, "징.jpg")
	if want := []byte{0xC2, 0xA1, '.', 'j', 'p', 'g'}; !bytes.Equal(raw, want) {
		t.Fatalf("CP949(\"징.jpg\") = % x, want % x; the example this test is built on has moved", raw, want)
	}
	gotName, gotEnc := kenc.DecodeEntryName(raw, false)
	if gotName != "¡.jpg" || gotEnc != kenc.EncUTF8 {
		t.Errorf("DecodeEntryName(% x, false) = (%q, %q), want (%q, %q) — the documented "+
			"false positive of the UTF-8 probe", raw, gotName, gotEnc, "¡.jpg", kenc.EncUTF8)
	}

	// How many single syllables land in the hole. Any name built only out of
	// these (plus ASCII that keeps the byte string well-formed) is misread.
	encoder := korean.EUCKR.NewEncoder()
	var affected int
	for r := rune(0xAC00); r <= 0xD7A3; r++ {
		b, _, err := transform.Bytes(encoder, []byte(string(r)))
		if err != nil {
			t.Fatalf("CP949-encoding U+%04X: %v", r, err)
		}
		if utf8.Valid(b) {
			affected++
		}
	}
	if affected != 345 {
		t.Errorf("%d of 11172 Hangul syllables CP949-encode to valid UTF-8, want 345; "+
			"the residual risk documented in the package comment has changed size", affected)
	}

	// And the reason it stays a footnote rather than a defect: real names are
	// long and mixed, so their CP949 bytes are not valid UTF-8 and they take the
	// CP949 branch. These are the survey names used by the golden vectors above.
	for _, name := range []string{
		"슈퍼만화데생",
		"한글.jpg",
		"세월을 잊은",
		"군계(軍鷄) 01권/001.jpg",
		"강철의 연금술사 03권/013.jpg",
		"징검다리.jpg", // starts with the offending syllable, still safe in context
	} {
		cp := testutil.CP949(t, name)
		if utf8.Valid(cp) {
			t.Errorf("CP949(%q) is valid UTF-8, so it would take the probe branch", name)
			continue
		}
		if got, enc := kenc.DecodeEntryName(cp, false); got != name || enc != kenc.EncCP949 {
			t.Errorf("DecodeEntryName(CP949(%q), false) = (%q, %q), want (%q, %q)",
				name, got, enc, name, kenc.EncCP949)
		}
	}
}

// TestEUCKRDecoder_onGarbage_stillReturnsNilError is the regression guard
// impl-plan §3 WP-02 acceptance 3 asks for. If a future x/text starts returning
// an error here, kenc's content check could legitimately be replaced by an
// error check — and until then, deleting the content check as "dead code"
// silently accepts mojibake.
func TestEUCKRDecoder_onGarbage_stillReturnsNilError(t *testing.T) {
	t.Parallel()

	const garbage = "\xff\xfe.jpg"
	got, _, err := transform.String(korean.EUCKR.NewDecoder(), garbage)
	if err != nil {
		t.Fatalf("korean.EUCKR.NewDecoder() now returns an error (%v) for %q; "+
			"kenc's U+FFFD content check may be replaceable, re-read arch §4.4", err, garbage)
	}
	if !strings.ContainsRune(got, utf8.RuneError) {
		t.Fatalf("decoding %q produced %q with no U+FFFD; the whole detection "+
			"strategy of arch §4.4 rests on that substitution", garbage, got)
	}
}

// TestDecodeEntryName_throughARealArchive_decodesCentralDirectoryBytes runs the
// decoder over names taken out of an actual ZIP central directory rather than
// out of a Go literal, which is the path the scanner takes (WP-04 hands zipidx's
// raw name bytes and GP flags straight to this function).
func TestDecodeEntryName_throughARealArchive_decodesCentralDirectoryBytes(t *testing.T) {
	t.Parallel()

	page := testutil.TinyJPEG(t, 8, 8)
	raw := testutil.BuildZIP(t, testutil.ZIPSpec{
		Entries: []testutil.Entry{
			// The dominant real shape: CP949 bytes, bit 11 clear (data-survey §3
			// found 11 of 11 readable archives like this).
			{RawName: testutil.CP949(t, "시티 헌터 완전판 08권/CS02-026.JPG"), Data: page, Method: testutil.MethodDeflate},
			{RawName: testutil.CP949(t, "슈퍼만화데생"), Data: page, Method: testutil.MethodStore},
			// A modern archiver that sets the flag.
			{Name: "한글.jpg", Data: page, Method: testutil.MethodDeflate, Flags: testutil.FlagUTF8},
			// A modern archiver that forgets to: this must NOT be re-read as CP949.
			{Name: "강철의 연금술사 03권/013.jpg", Data: page, Method: testutil.MethodDeflate},
			// Pure ASCII, no flag.
			{Name: "001.jpg", Data: page, Method: testutil.MethodStore},
			// Bytes no encoding can rescue.
			{RawName: []byte("\xff\xfe\x80.jpg"), Data: page, Method: testutil.MethodStore},
		},
	})

	r, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("opening the fixture: %v", err)
	}

	want := []struct {
		name string
		enc  string
	}{
		{"시티 헌터 완전판 08권/CS02-026.JPG", kenc.EncCP949},
		{"슈퍼만화데생", kenc.EncCP949},
		{"한글.jpg", kenc.EncUTF8},
		{"강철의 연금술사 03권/013.jpg", kenc.EncUTF8},
		{"001.jpg", kenc.EncUTF8},
		{"�.jpg", kenc.EncUnknown},
	}
	if len(r.File) != len(want) {
		t.Fatalf("entry count = %d, want %d", len(r.File), len(want))
	}
	for i, f := range r.File {
		// archive/zip hands back the undecoded central-directory bytes as a Go
		// string; that is exactly what zipidx will hand kenc.
		gotName, gotEnc := kenc.DecodeEntryName([]byte(f.Name), f.Flags&testutil.FlagUTF8 != 0)
		if gotName != want[i].name || gotEnc != want[i].enc {
			t.Errorf("entry %d: DecodeEntryName(% x, %v) = (%q, %q), want (%q, %q)",
				i, f.Name, f.Flags&testutil.FlagUTF8 != 0, gotName, gotEnc, want[i].name, want[i].enc)
		}
	}
}

func TestDecodeEntryName_neverMutatesItsInput(t *testing.T) {
	t.Parallel()

	// zipidx parses records in place inside one shared central-directory
	// buffer, so a decoder that scribbled on raw would corrupt later entries.
	raw := append([]byte(nil), cp949Hangul...)
	before := append([]byte(nil), raw...)
	for _, flag := range []bool{false, true} {
		kenc.DecodeEntryName(raw, flag)
	}
	if !bytes.Equal(raw, before) {
		t.Errorf("input was modified: % x -> % x", before, raw)
	}
}

// FuzzDecodeEntryName drives the decoder from arbitrary bytes. The scanner runs
// it over ~1.2 M entries from archives it did not write, so "no panic" and
// "always valid UTF-8" are load-bearing, not decorative.
func FuzzDecodeEntryName(f *testing.F) {
	f.Add([]byte("001.jpg"), false)
	f.Add(cp949Hangul, false)
	f.Add(cp949SuperManhwa, false)
	f.Add([]byte("한글.jpg"), false)
	f.Add([]byte("한글.jpg"), true)
	f.Add([]byte("\xff\xfe.jpg"), false)
	f.Add([]byte("\xff\xfe.jpg"), true)
	f.Add([]byte{}, false)
	f.Add([]byte{0x00}, false)
	f.Add([]byte{0x80, 0xa1, 0xa1, 0xff}, false)

	f.Fuzz(func(t *testing.T, raw []byte, utf8Flag bool) {
		before := append([]byte(nil), raw...)

		name, enc := kenc.DecodeEntryName(raw, utf8Flag)

		if !utf8.ValidString(name) {
			t.Fatalf("DecodeEntryName(% x, %v) returned invalid UTF-8: %q", raw, utf8Flag, name)
		}
		switch enc {
		case kenc.EncUTF8, kenc.EncUTF8Invalid, kenc.EncCP949, kenc.EncUnknown:
		default:
			t.Fatalf("DecodeEntryName(% x, %v) returned unknown label %q", raw, utf8Flag, enc)
		}
		if !bytes.Equal(raw, before) {
			t.Fatalf("input was modified: % x -> % x", before, raw)
		}
		// A name reported as utf-8 must be the input verbatim: that label tells
		// the rest of the system no transformation happened.
		if enc == kenc.EncUTF8 && name != string(raw) {
			t.Fatalf("enc=utf-8 but the name changed: % x -> %q", raw, name)
		}
		// The clean branches must never contain a substitution character unless
		// the input genuinely did.
		if enc == kenc.EncCP949 && strings.ContainsRune(name, utf8.RuneError) {
			t.Fatalf("enc=cp949 but the name %q contains U+FFFD", name)
		}
	})
}
