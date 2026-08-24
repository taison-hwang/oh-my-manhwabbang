package hv3_test

import (
	"bytes"
	"context"
	"errors"
	"hash/crc32"
	"io"
	"strings"
	"testing"
	"time"

	"shelf/internal/archive"
	"shelf/internal/archive/hv3"
	"shelf/internal/kenc"
	"shelf/internal/testutil"
)

func read(t *testing.T, b []byte) (*archive.Index, error) {
	t.Helper()
	return hv3.New().ReadIndex(context.Background(), bytes.NewReader(b), int64(len(b)))
}

// mustRead is the happy path, which most tests want without the error dance.
func mustRead(t *testing.T, b []byte) *archive.Index {
	t.Helper()
	ix, err := read(t, b)
	if err != nil {
		t.Fatalf("ReadIndex: %v", err)
	}
	return ix
}

// stream reads one entry back out the way a page request does: index once,
// then seek to the recorded offset and read that entry alone.
func stream(t *testing.T, b []byte, e archive.Entry) []byte {
	t.Helper()
	rc, err := hv3.New().OpenEntry(context.Background(), bytes.NewReader(b), e.Ref())
	if err != nil {
		t.Fatalf("OpenEntry(%q): %v", e.Name, err)
	}
	defer func() { _ = rc.Close() }()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("reading %q: %v", e.Name, err)
	}
	return got
}

// TestReadIndex_masked_recordsEveryEntry is the shape of the real container:
// ENCR 2, POS4 offsets, one FILE block per entry.
func TestReadIndex_masked_recordsEveryEntry(t *testing.T) {
	t.Parallel()

	pages := testutil.HV3Pages(4)
	c := testutil.BuildHV3(t, testutil.HV3Spec{Entries: pages, Encr: testutil.HV3EncrMask})
	ix := mustRead(t, c)

	if len(ix.Entries) != len(pages) {
		t.Fatalf("indexed %d entries, want %d", len(ix.Entries), len(pages))
	}
	for i, e := range ix.Entries {
		if e.Name != pages[i].Name {
			t.Errorf("entry %d name = %q, want %q", i, e.Name, pages[i].Name)
		}
		if e.Size != int64(len(pages[i].Data)) {
			t.Errorf("entry %d size = %d, want %d", i, e.Size, len(pages[i].Data))
		}
		// Nothing in an HV3 is compressed, so the two sizes are the same
		// number and pages.comp_size is not a separate fact.
		if e.CompSize != e.Size {
			t.Errorf("entry %d comp_size = %d, want %d", i, e.CompSize, e.Size)
		}
		if want := crc32.ChecksumIEEE(pages[i].Data); e.CRC32 != want {
			t.Errorf("entry %d crc = %08X, want %08X", i, e.CRC32, want)
		}
		if e.Method != hv3.MethodMasked {
			t.Errorf("entry %d method = 0x%04X, want 0x%04X (ENCR 2)", i, e.Method, hv3.MethodMasked)
		}
		if e.NameEncoding != kenc.EncUTF8 {
			t.Errorf("entry %d encoding = %q, want %q — HV3 names are UTF-16 by construction",
				i, e.NameEncoding, kenc.EncUTF8)
		}
		if e.Dir || e.Encrypted {
			t.Errorf("entry %d: dir=%v encrypted=%v, want both false", i, e.Dir, e.Encrypted)
		}
	}
}

// TestOpenEntry_masked_unmasksToTheRecordedCRC is the measurement that
// overturned D-72, in miniature: the stored bytes are not the file's bytes, and
// the container's own CRC-32 is what proves the transform is right.
func TestOpenEntry_masked_unmasksToTheRecordedCRC(t *testing.T) {
	t.Parallel()

	pages := testutil.HV3Pages(3)
	c := testutil.BuildHV3(t, testutil.HV3Spec{Entries: pages, Encr: testutil.HV3EncrMask})
	ix := mustRead(t, c)

	for i, e := range ix.Entries {
		got := stream(t, c, e)
		if !bytes.Equal(got, pages[i].Data) {
			t.Fatalf("entry %d: streamed %d bytes, want the %d original ones", i, len(got), len(pages[i].Data))
		}
		if crc := crc32.ChecksumIEEE(got); crc != e.CRC32 {
			t.Errorf("entry %d: crc %08X, container recorded %08X", i, crc, e.CRC32)
		}
		// The stored bytes must NOT already be the answer, or this test would
		// pass against a reader that does nothing at all.
		if bytes.Contains(c, pages[i].Data) {
			t.Errorf("entry %d: the plain bytes are in the container — the fixture is not masking", i)
		}
	}
}

// TestOpenEntry_plain_isTheSameBytes covers ENCR 0 and ENCR absent, which the
// reference extractor treats identically and so does this reader.
func TestOpenEntry_plain_isTheSameBytes(t *testing.T) {
	t.Parallel()

	pages := testutil.HV3Pages(2)
	for _, tc := range []struct {
		name string
		spec testutil.HV3Spec
	}{
		{"ENCR 0", testutil.HV3Spec{Entries: pages, Encr: testutil.HV3EncrNone}},
		{"no ENCR chunk", testutil.HV3Spec{Entries: pages, OmitEncr: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := testutil.BuildHV3(t, tc.spec)
			ix := mustRead(t, c)
			if len(ix.Entries) != 2 {
				t.Fatalf("indexed %d entries, want 2", len(ix.Entries))
			}
			for i, e := range ix.Entries {
				if e.Method != hv3.MethodPlain {
					t.Errorf("entry %d method = 0x%04X, want 0x%04X", i, e.Method, hv3.MethodPlain)
				}
				if got := stream(t, c, e); !bytes.Equal(got, pages[i].Data) {
					t.Errorf("entry %d: streamed bytes differ from the original", i)
				}
			}
		})
	}
}

// TestOpenEntry_masked_isSeekable is arch §5.3: a page body that can seek is a
// page the HTTP layer can answer a Range request for. The mask is positional,
// so a range must decode correctly without the bytes before it ever being read
// — which is the property a decode-to-buffer implementation would lose.
func TestOpenEntry_masked_isSeekable(t *testing.T) {
	t.Parallel()

	pages := testutil.HV3Pages(1)
	c := testutil.BuildHV3(t, testutil.HV3Spec{Entries: pages, Encr: testutil.HV3EncrMask})
	ix := mustRead(t, c)

	rc, err := hv3.New().OpenEntry(context.Background(), bytes.NewReader(c), ix.Entries[0].Ref())
	if err != nil {
		t.Fatalf("OpenEntry: %v", err)
	}
	defer func() { _ = rc.Close() }()

	rs, ok := rc.(io.ReadSeeker)
	if !ok {
		t.Fatal("the body does not implement io.ReadSeeker — every page loses Range support")
	}
	want := pages[0].Data
	const from = 17
	if _, err := rs.Seek(from, io.SeekStart); err != nil {
		t.Fatalf("Seek: %v", err)
	}
	got, err := io.ReadAll(rs)
	if err != nil {
		t.Fatalf("ReadAll after seek: %v", err)
	}
	if !bytes.Equal(got, want[from:]) {
		t.Errorf("bytes from offset %d are wrong — the mask is being applied from 0, not from the position", from)
	}

	// http.ServeContent's first move is to seek to the end to size the body.
	end, err := rs.Seek(0, io.SeekEnd)
	if err != nil {
		t.Fatalf("Seek(end): %v", err)
	}
	if end != int64(len(want)) {
		t.Errorf("Seek(0, end) = %d, want %d", end, len(want))
	}
	if _, err := rs.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("Seek(start): %v", err)
	}
	if got, err := io.ReadAll(rs); err != nil || !bytes.Equal(got, want) {
		t.Errorf("re-reading from the start after a seek to the end did not return the file (err %v)", err)
	}
}

// TestOpenEntry_masked_readAtIsPositional is the same claim through
// io.ReaderAt, which is what a caller that shares one body between goroutines
// would use.
func TestOpenEntry_masked_readAtIsPositional(t *testing.T) {
	t.Parallel()

	pages := testutil.HV3Pages(1)
	c := testutil.BuildHV3(t, testutil.HV3Spec{Entries: pages, Encr: testutil.HV3EncrMask})
	ix := mustRead(t, c)
	rc, err := hv3.New().OpenEntry(context.Background(), bytes.NewReader(c), ix.Entries[0].Ref())
	if err != nil {
		t.Fatalf("OpenEntry: %v", err)
	}
	defer func() { _ = rc.Close() }()

	ra, ok := rc.(io.ReaderAt)
	if !ok {
		t.Fatal("the body does not implement io.ReaderAt")
	}
	want := pages[0].Data
	for _, off := range []int64{0, 1, 5, int64(len(want)) - 3} {
		buf := make([]byte, 3)
		n, err := ra.ReadAt(buf, off)
		if err != nil && !errors.Is(err, io.EOF) {
			t.Fatalf("ReadAt(%d): %v", off, err)
		}
		if !bytes.Equal(buf[:n], want[off:off+int64(n)]) {
			t.Errorf("ReadAt(%d) = % X, want % X", off, buf[:n], want[off:off+int64(n)])
		}
	}
}

// TestReadIndex_pos8 is the 64-bit offset field. The real container uses POS4
// throughout because it is 39.5 MB; anything past 4 GiB could not.
func TestReadIndex_pos8(t *testing.T) {
	t.Parallel()

	pages := testutil.HV3Pages(2)
	for i := range pages {
		pages[i].Pos8 = true
	}
	c := testutil.BuildHV3(t, testutil.HV3Spec{Entries: pages, Encr: testutil.HV3EncrMask})
	ix := mustRead(t, c)
	if len(ix.Entries) != 2 {
		t.Fatalf("indexed %d entries, want 2", len(ix.Entries))
	}
	for i, e := range ix.Entries {
		if got := stream(t, c, e); !bytes.Equal(got, pages[i].Data) {
			t.Errorf("entry %d streamed the wrong bytes through POS8", i)
		}
	}
}

// TestReadIndex_names covers the two things a NAME field can carry that the
// rest of the product then has to live with: non-ASCII, and a directory
// separator in the DOS spelling.
func TestReadIndex_names(t *testing.T) {
	t.Parallel()

	entries := []testutil.HV3Entry{
		{Name: "펌프킨 시저스(Pumpkin Scissors)_04_0001(Scan By Q.H).jpg", Data: []byte("a")},
		{Name: `1화\001.jpg`, Data: []byte("b")},
		{Name: "01화/002.jpg", Data: []byte("c")},
	}
	c := testutil.BuildHV3(t, testutil.HV3Spec{Entries: entries, Encr: testutil.HV3EncrMask})
	ix := mustRead(t, c)

	want := []string{
		"펌프킨 시저스(Pumpkin Scissors)_04_0001(Scan By Q.H).jpg",
		"1화/001.jpg",
		"01화/002.jpg",
	}
	for i, e := range ix.Entries {
		if e.Name != want[i] {
			t.Errorf("entry %d name = %q, want %q", i, e.Name, want[i])
		}
	}
}

// TestReadIndex_mtime decodes the Windows FILETIME in MTIM. It is the only
// per-entry timestamp an HV3 has, and the thumbnailer and the API both surface
// whatever lands in it.
func TestReadIndex_mtime(t *testing.T) {
	t.Parallel()

	when := time.Date(2009, 1, 6, 12, 30, 51, 0, time.UTC)
	c := testutil.BuildHV3(t, testutil.HV3Spec{
		Entries: []testutil.HV3Entry{
			{Name: "a.jpg", Data: []byte("a"), Modified: when},
			{Name: "b.jpg", Data: []byte("b")}, // no timestamp at all
		},
		Encr: testutil.HV3EncrMask,
	})
	ix := mustRead(t, c)
	if got := ix.Entries[0].Modified; !got.Equal(when) {
		t.Errorf("modified = %v, want %v", got, when)
	}
	if got := ix.Entries[1].Modified; !got.IsZero() {
		t.Errorf("modified = %v for a zero MTIM, want the zero time", got)
	}
}

// TestReadIndex_refusals is the classification table: every malformed shape
// must come back as a sentinel a caller can match, never as a panic and never
// as a silently short page list.
func TestReadIndex_refusals(t *testing.T) {
	t.Parallel()

	good := testutil.HV3Pages(2)
	bigList := uint64(1 << 40)

	tests := []struct {
		name string
		mut  func(t *testing.T) []byte
		want error
	}{
		{
			name: "not an HV3 at all",
			mut: func(t *testing.T) []byte {
				return testutil.BuildHV3(t, testutil.HV3Spec{Entries: good, NoSignature: true})
			},
			want: hv3.ErrNoSignature,
		},
		{
			name: "empty file",
			mut:  func(*testing.T) []byte { return nil },
			want: hv3.ErrNoSignature,
		},
		{
			name: "LIST declares more than the file holds",
			mut: func(t *testing.T) []byte {
				return testutil.BuildHV3(t, testutil.HV3Spec{Entries: good, ListSizeOverride: &bigList})
			},
			want: hv3.ErrNoList,
		},
		{
			name: "record with no NAME",
			mut: func(t *testing.T) []byte {
				e := testutil.HV3Pages(1)
				e[0].Name = ""
				return testutil.BuildHV3(t, testutil.HV3Spec{Entries: e, Encr: testutil.HV3EncrMask})
			},
			// An empty NAME still writes the field, so this is a directory
			// entry, not a refusal — asserted separately below.
			want: nil,
		},
		{
			name: "record with no SIZE",
			mut: func(t *testing.T) []byte {
				e := testutil.HV3Pages(1)
				e[0].OmitSize = true
				return testutil.BuildHV3(t, testutil.HV3Spec{Entries: e})
			},
			want: hv3.ErrBadRecord,
		},
		{
			name: "record with no offset",
			mut: func(t *testing.T) []byte {
				e := testutil.HV3Pages(1)
				e[0].OmitPos = true
				return testutil.BuildHV3(t, testutil.HV3Spec{Entries: e})
			},
			want: hv3.ErrBadRecord,
		},
		{
			name: "record with no CRC3",
			mut: func(t *testing.T) []byte {
				e := testutil.HV3Pages(1)
				e[0].OmitCRC = true
				return testutil.BuildHV3(t, testutil.HV3Spec{Entries: e})
			},
			want: hv3.ErrBadRecord,
		},
		{
			name: "record points past the end of the file",
			mut: func(t *testing.T) []byte {
				e := testutil.HV3Pages(1)
				huge := uint32(1 << 30)
				e[0].SizeOverride = &huge
				return testutil.BuildHV3(t, testutil.HV3Spec{Entries: e})
			},
			want: hv3.ErrTruncated,
		},
		{
			name: "an ENCR mode nobody has decoded",
			mut: func(t *testing.T) []byte {
				return testutil.BuildHV3(t, testutil.HV3Spec{Entries: good, Encr: 7})
			},
			want: archive.ErrUnsupportedMethod,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := read(t, tc.mut(t))
			if tc.want == nil {
				if err != nil {
					t.Fatalf("ReadIndex: %v, want no error", err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("ReadIndex error = %v, want one matching %v", err, tc.want)
			}
		})
	}
}

// TestReadIndex_emptyName_isADirectory keeps the one case above that is not a
// refusal honest: an entry with no name is not a page, and FR-IDX-006 has to
// see it as a directory to drop it.
func TestReadIndex_emptyName_isADirectory(t *testing.T) {
	t.Parallel()

	c := testutil.BuildHV3(t, testutil.HV3Spec{
		Entries: []testutil.HV3Entry{{Name: "", Data: []byte("x")}},
		Encr:    testutil.HV3EncrMask,
	})
	ix := mustRead(t, c)
	if len(ix.Entries) != 1 || !ix.Entries[0].Dir {
		t.Fatalf("entries = %+v, want one marked Dir", ix.Entries)
	}
}

// TestReadIndex_status maps this package's refusals onto books.status, which is
// the value an operator actually reads.
func TestReadIndex_status(t *testing.T) {
	t.Parallel()

	if got := hv3.Status(nil); got != archive.StatusOK {
		t.Errorf("Status(nil) = %q, want %q", got, archive.StatusOK)
	}
	_, err := read(t, testutil.BuildHV3(t, testutil.HV3Spec{Entries: testutil.HV3Pages(1), Encr: 7}))
	if got := hv3.Status(err); got != archive.StatusUnsupported {
		t.Errorf("Status(unknown ENCR) = %q, want %q", got, archive.StatusUnsupported)
	}
	_, err = read(t, []byte("Rar!\x1a\x07\x00nope"))
	if got := hv3.Status(err); got != archive.StatusError {
		t.Errorf("Status(not an HV3) = %q, want %q", got, archive.StatusError)
	}
}

// TestReadIndex_namedAsHV3ButIsNot names the format the bytes actually are.
//
// 54 of the 55 `.hv3` files on this machine are RAR archives wearing the
// extension. None is in the library — they are all in the trash — so nothing
// dispatches on the signature. But `HV3 signature not found` on a perfectly
// good RAR sends its owner looking for damage, and that is the exact wrong
// story D-72 was raised about in the other direction.
func TestReadIndex_namedAsHV3ButIsNot(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		head []byte
		want string
	}{
		{[]byte("Rar!\x1a\x07\x00\x00\x00\x00\x00"), "RAR 4.x"},
		{[]byte("Rar!\x1a\x07\x01\x00\x00\x00\x00"), "RAR 5"},
		{[]byte("PK\x03\x04\x00\x00\x00\x00\x00\x00"), "ZIP"},
		{[]byte("7z\xbc\xaf\x27\x1c\x00\x00\x00\x00"), "7-Zip"},
		{[]byte("%PDF-1.7\x00\x00"), "PDF"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()
			_, err := read(t, tc.head)
			if !errors.Is(err, hv3.ErrNoSignature) {
				t.Fatalf("error = %v, want it to match ErrNoSignature", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to name %s", err, tc.want)
			}
		})
	}

	// A file that is not any format this build has heard of says only what it
	// knows, rather than guessing.
	_, err := read(t, []byte("\x01\x02\x03\x04\x05\x06\x07\x08"))
	if !errors.Is(err, hv3.ErrNoSignature) {
		t.Fatalf("error = %v, want ErrNoSignature", err)
	}
	if strings.Contains(err.Error(), "but is") {
		t.Errorf("error = %q, want no claim about what the file is", err)
	}
}

// TestOpenEntry_refusesABlockThatMoved is the check that separates this reader
// from one that trusts pages.local_hdr_off.
//
// A container repacked under the same (size, mtime) the handle pool checks
// would leave the index pointing at a FILE block that now holds a different
// entry. Streaming it would serve the neighbour's bytes under this page's name
// and its Content-Length, with no error anywhere.
func TestOpenEntry_refusesABlockThatMoved(t *testing.T) {
	t.Parallel()

	c := testutil.BuildHV3(t, testutil.HV3Spec{Entries: testutil.HV3Pages(3), Encr: testutil.HV3EncrMask})
	ix := mustRead(t, c)

	ref := ix.Entries[0].Ref()
	ref.Size++ // the index and the block now disagree by one byte
	if _, err := hv3.New().OpenEntry(context.Background(), bytes.NewReader(c), ref); !errors.Is(err, hv3.ErrBadFileBlock) {
		t.Errorf("OpenEntry with a size the block disagrees with: %v, want ErrBadFileBlock", err)
	}

	ref = ix.Entries[0].Ref()
	ref.LocalHdrOff += 4 // no longer at a FILE tag
	if _, err := hv3.New().OpenEntry(context.Background(), bytes.NewReader(c), ref); !errors.Is(err, hv3.ErrBadFileBlock) {
		t.Errorf("OpenEntry at an offset with no FILE block: %v, want ErrBadFileBlock", err)
	}

	ref = ix.Entries[0].Ref()
	ref.LocalHdrOff = -1
	if _, err := hv3.New().OpenEntry(context.Background(), bytes.NewReader(c), ref); !errors.Is(err, hv3.ErrBadFileBlock) {
		t.Errorf("OpenEntry at a negative offset: %v, want ErrBadFileBlock", err)
	}
}

// TestOpenEntry_followsTheContainerNotTheIndex is the other half of that
// argument, and the reason OpenEntry pays for a second read of the header.
//
// The index row says plain; the file on disk says masked. A reader that
// believed the row would hand a client 400 KB of XORed noise with
// Content-Type: image/jpeg and nothing would report a problem.
func TestOpenEntry_followsTheContainerNotTheIndex(t *testing.T) {
	t.Parallel()

	pages := testutil.HV3Pages(1)
	c := testutil.BuildHV3(t, testutil.HV3Spec{Entries: pages, Encr: testutil.HV3EncrMask})
	ix := mustRead(t, c)

	ref := ix.Entries[0].Ref()
	ref.Method = hv3.MethodPlain // a stale row from before the file was remade
	rc, err := hv3.New().OpenEntry(context.Background(), bytes.NewReader(c), ref)
	if err != nil {
		t.Fatalf("OpenEntry: %v", err)
	}
	defer func() { _ = rc.Close() }()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, pages[0].Data) {
		t.Error("the stale method won — the container's own ENCR chunk must decide")
	}
}

// TestReadIndex_partialDirectory keeps the entries that parsed (FR-IDX-010). A
// directory that goes bad at record 3 still gives a reader the first two pages,
// which is what lets a truncated download open at all.
func TestReadIndex_partialDirectory(t *testing.T) {
	t.Parallel()

	c := testutil.BuildHV3(t, testutil.HV3Spec{Entries: testutil.HV3Pages(4), Encr: testutil.HV3EncrMask})
	// Corrupt the third record's tag in place. Finding it by searching for the
	// FINF tags keeps this independent of the fixture's exact widths.
	at := -1
	for i, n := 0, 0; ; n++ {
		j := bytes.Index(c[i:], []byte("FINF"))
		if j < 0 {
			t.Fatal("fewer than three FINF records in the fixture")
		}
		i += j + 4
		if n == 2 {
			at = i - 4
			break
		}
	}
	copy(c[at:], "XXXX")

	ix, err := read(t, c)
	if err == nil {
		t.Fatal("ReadIndex succeeded on a directory with a broken record")
	}
	if !errors.Is(err, archive.ErrCorrupt) {
		t.Errorf("error = %v, want it to classify as corrupt", err)
	}
	if ix == nil || len(ix.Entries) != 2 {
		t.Fatalf("kept %v entries, want the 2 that parsed before the break", ix)
	}
}

// TestReadIndex_contextCancelled stops rather than finishing the walk. A scan
// the operator cancelled must not keep reading a 7,000-entry directory.
func TestReadIndex_contextCancelled(t *testing.T) {
	t.Parallel()

	c := testutil.BuildHV3(t, testutil.HV3Spec{Entries: testutil.HV3Pages(2), Encr: testutil.HV3EncrMask})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := hv3.New().ReadIndex(ctx, bytes.NewReader(c), int64(len(c))); !errors.Is(err, context.Canceled) {
		t.Errorf("ReadIndex on a cancelled context = %v, want context.Canceled", err)
	}
	if _, err := hv3.New().OpenEntry(ctx, bytes.NewReader(c), archive.EntryRef{}); !errors.Is(err, context.Canceled) {
		t.Errorf("OpenEntry on a cancelled context = %v, want context.Canceled", err)
	}
}

func TestFormat(t *testing.T) {
	t.Parallel()
	if got := hv3.New().Format(); got != "hv3" {
		t.Errorf("Format() = %q, want %q", got, "hv3")
	}
}

// listMarker is the eight bytes parseHeader searches for. Spelled here so the
// two traps below can assert they actually contain it: an ASCII "LIST" typed
// into a title does NOT, because TITL is UTF-16LE and encodes it as
// `L\0I\0S\0T\0` — which is why the first version of this test passed against
// a reader with the validation removed.
var listMarker = []byte("LIST\x00\x00\x00\x00")

// strayTitle returns a title whose UTF-16LE bytes are a literal LIST chunk
// header declaring listSize.
//
// U+494C and U+5453 are ordinary CJK ideographs whose little-endian bytes are
// `4C 49` and `53 54` — "LIST". The NULs after them are the 64-bit length.
func strayTitle(listSize uint16) string {
	return string([]rune{0x494C, 0x5453, 0, 0, rune(listSize), 0, 0, 0})
}

// TestReadIndex_strayListMarkerInATitle is why the LIST chunk is validated
// rather than merely found. A container whose title happens to contain the
// eight bytes `LIST\0\0\0\0` must still index, because a candidate that is not
// followed by a FINF record is skipped and the search goes on.
func TestReadIndex_strayListMarkerInATitle(t *testing.T) {
	t.Parallel()

	pages := testutil.HV3Pages(2)
	// A plausible length, so the candidate survives every check except the one
	// under test. A huge or negative length would be thrown out by the bounds
	// check instead and this would assert nothing.
	c := testutil.BuildHV3(t, testutil.HV3Spec{
		Entries: pages, Encr: testutil.HV3EncrMask, Title: strayTitle(16),
	})
	if at := bytes.Index(c, listMarker); at < 0 || at >= bytes.LastIndex(c, listMarker) {
		t.Fatalf("the fixture carries %d LIST markers, want the stray one before the real one",
			bytes.Count(c, listMarker))
	}
	ix, err := read(t, c)
	if err != nil {
		t.Fatalf("ReadIndex: %v — a stray LIST marker derailed the search", err)
	}
	if len(ix.Entries) != 2 {
		t.Fatalf("indexed %d entries, want 2", len(ix.Entries))
	}
}

// TestReadIndex_strayListMarkerDeclaringNothing is the other half: a stray
// marker followed by eight zero bytes cannot be checked against a record, so it
// must not be *taken* either. Believing it would report a 104-page book as
// empty, which is a worse answer than any error.
func TestReadIndex_strayListMarkerDeclaringNothing(t *testing.T) {
	t.Parallel()

	pages := testutil.HV3Pages(2)
	c := testutil.BuildHV3(t, testutil.HV3Spec{
		Entries: pages, Encr: testutil.HV3EncrMask, Title: strayTitle(0),
	})
	if bytes.Count(c, listMarker) != 2 {
		t.Fatalf("the fixture carries %d LIST markers, want 2", bytes.Count(c, listMarker))
	}
	ix := mustRead(t, c)
	if len(ix.Entries) != 2 {
		t.Fatalf("indexed %d entries, want 2 — the zero-length candidate won", len(ix.Entries))
	}
}

// TestReadIndex_containerWithNoFiles is why that candidate is remembered
// rather than discarded. A container that genuinely holds nothing has a LIST
// chunk of zero bytes and no record to validate against; it is `empty`, which
// is what it is, and not `corrupt`.
func TestReadIndex_containerWithNoFiles(t *testing.T) {
	t.Parallel()

	ix, err := read(t, testutil.BuildHV3(t, testutil.HV3Spec{Encr: testutil.HV3EncrMask}))
	if err != nil {
		t.Fatalf("ReadIndex: %v, want an empty index and no error", err)
	}
	if len(ix.Entries) != 0 {
		t.Fatalf("indexed %d entries, want none", len(ix.Entries))
	}
}
