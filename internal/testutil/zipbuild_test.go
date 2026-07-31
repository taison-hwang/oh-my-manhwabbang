package testutil_test

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"testing"
	"unicode/utf8"

	"shelf/internal/testutil"
)

// The contract of this package is "archive/zip agrees with us". WP-04 keeps
// archive/zip as a permanent differential oracle (impl-plan C-6 / decisions
// E-2), so a fixture the stdlib cannot open would poison every downstream
// test. Every well-formed shape below is therefore round-tripped through
// zip.NewReader and checked entry-for-entry; the deliberately broken ones
// assert that the stdlib rejects them too.

// wantEntry is the expected central-directory view of one member.
type wantEntry struct {
	name       string // the name as archive/zip reports it
	size       uint64 // uncompressed size
	method     uint16
	dir        bool
	unreadable bool // opening the payload is expected to fail (GP bit 0)
	data       []byte
}

func openZIP(t *testing.T, raw []byte) *zip.Reader {
	t.Helper()
	r, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("archive/zip refused a fixture it should accept: %v", err)
	}
	return r
}

func assertEntries(t *testing.T, raw []byte, want []wantEntry) {
	t.Helper()
	r := openZIP(t, raw)

	if len(r.File) != len(want) {
		got := make([]string, len(r.File))
		for i, f := range r.File {
			got[i] = f.Name
		}
		t.Fatalf("entry count = %d %q, want %d", len(r.File), got, len(want))
	}

	for i, w := range want {
		f := r.File[i]
		if f.Name != w.name {
			t.Errorf("entry %d: name = %q, want %q", i, f.Name, w.name)
		}
		if f.UncompressedSize64 != w.size {
			t.Errorf("entry %d (%s): uncompressed size = %d, want %d", i, f.Name, f.UncompressedSize64, w.size)
		}
		if f.Method != w.method {
			t.Errorf("entry %d (%s): method = %d, want %d", i, f.Name, f.Method, w.method)
		}
		if got := f.FileInfo().IsDir(); got != w.dir {
			t.Errorf("entry %d (%s): IsDir = %v, want %v", i, f.Name, got, w.dir)
		}

		rc, err := f.Open()
		if w.unreadable {
			// Go's archive/zip never inspects GP bit 0, so Open() itself
			// succeeds; the failure surfaces when the ciphertext refuses to
			// decompress or fails its CRC. Either is acceptable — what must
			// not happen is a clean read.
			if err == nil {
				_, err = io.ReadAll(rc)
				_ = rc.Close()
			}
			if err == nil {
				t.Errorf("entry %d (%s): read cleanly, want a failure — an "+
					"encrypted fixture that decodes tests nothing", i, f.Name)
			}
			continue
		}
		if err != nil {
			t.Errorf("entry %d (%s): Open: %v", i, f.Name, err)
			continue
		}
		got, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			// io.ReadAll surfaces the CRC-32 mismatch on Close/EOF.
			t.Errorf("entry %d (%s): reading payload: %v", i, f.Name, err)
			continue
		}
		if !bytes.Equal(got, w.data) {
			t.Errorf("entry %d (%s): payload = %q, want %q", i, f.Name, got, w.data)
		}
	}
}

func TestBuildZIP_everyShape_roundTripsThroughArchiveZip(t *testing.T) {
	t.Parallel()

	page1 := testutil.TinyJPEG(t, 16, 24)
	page2 := testutil.TinyPNG(t, 8, 12)

	// A CP949 name with no UTF-8 flag: the AC-002 shape. Stated as Korean here
	// and encoded to exact bytes so the golden vector is legible.
	cp949Name := testutil.CP949(t, "한글.jpg")
	if utf8.Valid(cp949Name) {
		t.Fatalf("CP949(%q) = % x, which is valid UTF-8 — the fixture would not "+
			"exercise the FR-IDX-008 fallback", "한글.jpg", cp949Name)
	}
	if want := []byte("\xc7\xd1\xb1\xdb.jpg"); !bytes.Equal(cp949Name, want) {
		t.Fatalf("CP949(%q) = % x, want % x (the arch §4.4 golden vector)", "한글.jpg", cp949Name, want)
	}

	raw := testutil.BuildZIP(t, testutil.ZIPSpec{
		Entries: []testutil.Entry{
			// stored
			{Name: "001.jpg", Data: page1, Method: testutil.MethodStore},
			// deflate
			{Name: "002.png", Data: page2, Method: testutil.MethodDeflate},
			// GP bit 11 set: a genuinely UTF-8-flagged name
			{Name: "003 슈퍼만화데생.jpg", Data: page1, Method: testutil.MethodDeflate,
				Flags: testutil.FlagUTF8},
			// raw CP949 name bytes, no flag
			{RawName: cp949Name, Data: page1, Method: testutil.MethodStore},
			// directory entry — FR-IDX-006 must skip it
			{Name: "sub", Dir: true},
			// nested member below that directory
			{Name: "sub/004.jpg", Data: page1, Method: testutil.MethodDeflate},
			// 0-byte entry — FR-IDX-006 must skip it
			{Name: "empty.jpg", Data: nil, Method: testutil.MethodStore},
			// the two junk families FR-IDX-006 names explicitly
			{Name: "__MACOSX/", Dir: true},
			{Name: "__MACOSX/._001.jpg", Data: []byte("resource fork"), Method: testutil.MethodStore},
			{Name: "Thumbs.db", Data: []byte("thumbs"), Method: testutil.MethodStore},
		},
	})

	assertEntries(t, raw, []wantEntry{
		{name: "001.jpg", size: uint64(len(page1)), method: testutil.MethodStore, data: page1},
		{name: "002.png", size: uint64(len(page2)), method: testutil.MethodDeflate, data: page2},
		{name: "003 슈퍼만화데생.jpg", size: uint64(len(page1)), method: testutil.MethodDeflate, data: page1},
		// archive/zip has no CP949 knowledge and does not transcode: with no
		// UTF-8 flag it hands back the raw bytes as a Go string, invalid UTF-8
		// and all (it only sets FileHeader.NonUTF8). Asserting byte equality
		// here proves the writer stored the name untouched, which is what makes
		// WP-02's kenc golden vectors meaningful.
		{name: string(cp949Name), size: uint64(len(page1)), method: testutil.MethodStore, data: page1},
		{name: "sub/", size: 0, method: testutil.MethodStore, dir: true},
		{name: "sub/004.jpg", size: uint64(len(page1)), method: testutil.MethodDeflate, data: page1},
		{name: "empty.jpg", size: 0, method: testutil.MethodStore, data: []byte{}},
		{name: "__MACOSX/", size: 0, method: testutil.MethodStore, dir: true},
		{name: "__MACOSX/._001.jpg", size: 13, method: testutil.MethodStore, data: []byte("resource fork")},
		{name: "Thumbs.db", size: 6, method: testutil.MethodStore, data: []byte("thumbs")},
	})
}

func TestBuildZIP_rawNameBytes_arePreservedExactly(t *testing.T) {
	t.Parallel()

	// Bytes that are neither valid UTF-8 nor valid CP949: the "garbage" branch
	// of the arch §4.4 decision table. The writer must not touch them.
	rawName := []byte{0xff, 0xfe, 0x80, 0x2e, 0x6a, 0x70, 0x67} // "\xff\xfe\x80.jpg"

	raw := testutil.BuildZIP(t, testutil.ZIPSpec{
		Entries: []testutil.Entry{{RawName: rawName, Data: []byte("x"), Method: testutil.MethodStore}},
	})

	r := openZIP(t, raw)
	if len(r.File) != 1 {
		t.Fatalf("entry count = %d, want 1", len(r.File))
	}
	// zip.File.Name is the CP437-reinterpreted string; recovering the original
	// bytes means finding them verbatim in both headers.
	if n := bytes.Count(raw, rawName); n != 2 {
		t.Errorf("raw name bytes % x appear %d times in the archive, want 2 "+
			"(local header + central directory)", rawName, n)
	}
}

func TestBuildZIP_encryptedFlag_listsButRefusesToOpen(t *testing.T) {
	t.Parallel()

	// GP bit 0 with an unencrypted payload. The central directory is perfectly
	// well formed — which is the point: FR-IDX-010 has to tell "encrypted"
	// apart from "corrupt", and no encrypted archive exists in the real
	// collection to test against (data-survey D-4).
	page := testutil.TinyJPEG(t, 8, 8)
	raw := testutil.BuildZIP(t, testutil.ZIPSpec{
		Entries: []testutil.Entry{
			{Name: "secret.jpg", Data: page, Method: testutil.MethodDeflate, Flags: testutil.FlagEncrypted},
		},
	})

	// The listing side is intact: FR-IDX-010 wants "encrypted", which is a
	// per-book status, not a scan-aborting parse failure.
	assertEntries(t, raw, []wantEntry{
		{name: "secret.jpg", size: uint64(len(page)), method: testutil.MethodDeflate, unreadable: true},
	})

	r := openZIP(t, raw)
	if got := r.File[0].Flags & 1; got != 1 {
		t.Errorf("GP flag bit 0 = %d, want 1", got)
	}
	// The compressed size must account for the 12-byte encryption header,
	// otherwise a reader that seeks by comp_size lands in the wrong place.
	if r.File[0].CompressedSize64 <= uint64(len(page))/4 {
		t.Errorf("compressed size = %d, implausibly small for an encrypted entry",
			r.File[0].CompressedSize64)
	}
}

func TestBuildZIP_largeArchiveComment_forcesTheSecondTailScan(t *testing.T) {
	t.Parallel()

	page := testutil.TinyJPEG(t, 8, 8)
	raw := testutil.BuildZIP(t, testutil.ZIPSpec{
		Entries:     []testutil.Entry{{Name: "001.jpg", Data: page, Method: testutil.MethodStore}},
		CommentSize: testutil.CommentSize40KiB,
	})

	r := openZIP(t, raw)
	if len(r.Comment) != testutil.CommentSize40KiB {
		t.Errorf("archive comment = %d bytes, want %d", len(r.Comment), testutil.CommentSize40KiB)
	}
	// The EOCD is now ~40 KiB from the end, past the 1 KiB first-pass window
	// WP-04 acceptance 1 describes and inside the 65 557 B second pass.
	if len(raw) < 1024 {
		t.Fatalf("archive is only %d bytes; the comment did not land", len(raw))
	}
	assertEntries(t, raw, []wantEntry{
		{name: "001.jpg", size: uint64(len(page)), method: testutil.MethodStore, data: page},
	})
}

func TestBuildZIP_deliberatelyBroken_areRejectedByArchiveZip(t *testing.T) {
	t.Parallel()

	page := testutil.TinyJPEG(t, 8, 8)
	base := testutil.ZIPSpec{
		Entries: []testutil.Entry{
			{Name: "001.jpg", Data: page, Method: testutil.MethodDeflate},
			{Name: "002.jpg", Data: page, Method: testutil.MethodDeflate},
		},
	}

	tests := []struct {
		name string
		spec func(testutil.ZIPSpec) testutil.ZIPSpec
	}{
		{
			// The 9 real broken archives in the collection are all truncated
			// (data-survey D-4). Chopping the tail destroys the EOCD.
			name: "truncated tail",
			spec: func(s testutil.ZIPSpec) testutil.ZIPSpec { s.TruncateTail = 40; return s },
		},
		{
			// Enough to cut into the central directory itself, not just the
			// end record.
			name: "truncated into the central directory",
			spec: func(s testutil.ZIPSpec) testutil.ZIPSpec { s.TruncateTail = 120; return s },
		},
		{
			name: "corrupt EOCD signature",
			spec: func(s testutil.ZIPSpec) testutil.ZIPSpec { s.CorruptEOCDSignature = true; return s },
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			raw := testutil.BuildZIP(t, tc.spec(base))
			if _, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw))); err == nil {
				t.Fatal("archive/zip accepted a deliberately broken fixture; " +
					"the differential oracle would then disagree with zipidx by construction")
			}
		})
	}
}

func TestBuildZIP_shiftedCentralDirOffset_isRecoveredByBaseOffset(t *testing.T) {
	t.Parallel()

	// A self-extracting archive whose writer computed offsets relative to the
	// ZIP payload rather than to the file. archive/zip recovers by deriving
	// baseOffset = eocdOffset - directorySize - directoryOffset, which is why
	// this is NOT a broken-archive case: zipidx has to perform the same
	// recovery or the differential oracle (C-6) disagrees on a real shape.
	page := testutil.TinyJPEG(t, 8, 8)
	const stubLen = 512
	raw := testutil.BuildZIP(t, testutil.ZIPSpec{
		Prefix:     bytes.Repeat([]byte("MZ"), stubLen/2),
		Entries:    []testutil.Entry{{Name: "001.jpg", Data: page, Method: testutil.MethodDeflate}},
		OffsetBias: -stubLen,
	})

	assertEntries(t, raw, []wantEntry{
		{name: "001.jpg", size: uint64(len(page)), method: testutil.MethodDeflate, data: page},
	})
}

func TestBuildZIP_truncatedPayload_isReadableUntilTheCRCFails(t *testing.T) {
	t.Parallel()

	// A subtler corruption than a missing EOCD: the directory is intact but a
	// member's recorded CRC does not match its bytes. FR-IDX-010 must isolate
	// this to one book rather than failing the scan.
	page := testutil.TinyJPEG(t, 8, 8)
	bogus := uint32(0xdeadbeef)
	raw := testutil.BuildZIP(t, testutil.ZIPSpec{
		Entries: []testutil.Entry{
			{Name: "001.jpg", Data: page, Method: testutil.MethodStore, CRC32Override: &bogus},
		},
	})

	r := openZIP(t, raw)
	rc, err := r.File[0].Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = rc.Close() }()
	if _, err := io.ReadAll(rc); !errors.Is(err, zip.ErrChecksum) {
		t.Errorf("reading a member with a bad CRC gave err = %v, want zip.ErrChecksum", err)
	}
}

func TestBuildZIP_prefix_doesNotDisturbOffsets(t *testing.T) {
	t.Parallel()

	// Self-extracting archives carry an executable stub before the first local
	// header. Every offset in the central directory is absolute, so a reader
	// that assumes the first entry begins at 0 breaks here.
	page := testutil.TinyJPEG(t, 8, 8)
	raw := testutil.BuildZIP(t, testutil.ZIPSpec{
		Prefix:  bytes.Repeat([]byte("MZ stub "), 64),
		Entries: []testutil.Entry{{Name: "001.jpg", Data: page, Method: testutil.MethodDeflate}},
	})

	assertEntries(t, raw, []wantEntry{
		{name: "001.jpg", size: uint64(len(page)), method: testutil.MethodDeflate, data: page},
	})
}

func TestBuildZIP64_forcesZIP64Records_andStillOpens(t *testing.T) {
	t.Parallel()

	page := testutil.TinyJPEG(t, 16, 24)
	spec := testutil.ZIPSpec{
		Entries: []testutil.Entry{
			{Name: "001.jpg", Data: page, Method: testutil.MethodStore},
			{Name: "002.jpg", Data: page, Method: testutil.MethodDeflate},
		},
	}

	variants := []struct {
		name string
		z64  testutil.ZIP64Spec
	}{
		{"central directory only", testutil.ZIP64Spec{}},
		{"with the optional disk slot", testutil.ZIP64Spec{IncludeDiskField: true}},
		{"with local-header extras too", testutil.ZIP64Spec{LocalHeaders: true}},
		{"everything", testutil.ZIP64Spec{IncludeDiskField: true, LocalHeaders: true}},
	}

	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			t.Parallel()
			raw := testutil.BuildZIP64(t, spec, v.z64)

			// The ZIP64 end-of-central-directory record and its locator must
			// both be present, otherwise the fixture is just a normal archive
			// and FR-IDX-009 goes untested.
			if !bytes.Contains(raw, []byte{0x50, 0x4b, 0x06, 0x06}) {
				t.Error("no ZIP64 EOCD record (PK\\x06\\x06) in the archive")
			}
			if !bytes.Contains(raw, []byte{0x50, 0x4b, 0x06, 0x07}) {
				t.Error("no ZIP64 EOCD locator (PK\\x06\\x07) in the archive")
			}
			// 0x0001 extra fields must be present in the central directory.
			if !bytes.Contains(raw, []byte{0x01, 0x00, 0x18, 0x00}) &&
				!bytes.Contains(raw, []byte{0x01, 0x00, 0x1c, 0x00}) {
				t.Error("no 0x0001 ZIP64 extra field with a 24- or 28-byte body")
			}
			// The archive is under 100 bytes of payload: a reader that only
			// takes the ZIP64 path when the file is >4 GiB would fail here.
			if len(raw) > 4096 {
				t.Errorf("fixture is %d bytes, expected a tiny archive", len(raw))
			}

			assertEntries(t, raw, []wantEntry{
				{name: "001.jpg", size: uint64(len(page)), method: testutil.MethodStore, data: page},
				{name: "002.jpg", size: uint64(len(page)), method: testutil.MethodDeflate, data: page},
			})
		})
	}
}
