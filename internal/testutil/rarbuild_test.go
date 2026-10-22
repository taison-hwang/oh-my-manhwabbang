package testutil

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"testing"

	"github.com/nwaples/rardecode/v2"
)

// fileCRC reads FILE_CRC from the first file block, which follows the 7-byte
// marker and the 13-byte main header: block header (7) + PACK_SIZE (4) +
// UNP_SIZE (4) + HOST_OS (1).
func fileCRC(t *testing.T, arc []byte) uint32 {
	t.Helper()
	const at = 7 + rarMainHeaderLen + rarBlockHeaderLen + 9
	if len(arc) < at+4 || arc[7+rarMainHeaderLen+2] != rarBlockFile {
		t.Fatalf("no file block at %d in %d bytes", 7+rarMainHeaderLen, len(arc))
	}
	return binary.LittleEndian.Uint32(arc[at : at+4])
}

func TestRAR4Entry_zeroCRCRecordsTheCRCOfData(t *testing.T) {
	t.Parallel()
	data := []byte("stored bytes")
	arc := BuildRAR4(t, RAR4Spec{Entries: []RAR4Entry{{Name: []byte("01.jpg"), Data: data}}})
	if got, want := fileCRC(t, arc), crc32.ChecksumIEEE(data); got != want {
		t.Fatalf("FILE_CRC = %08x, want crc32(Data) %08x", got, want)
	}
}

func TestRAR4Entry_CRCIsRecordedVerbatim(t *testing.T) {
	t.Parallel()
	arc := BuildRAR4(t, RAR4Spec{Entries: []RAR4Entry{
		{Name: []byte("01.jpg"), Data: []byte("stored bytes"), CRC: 0x01020304},
	}})
	if got := fileCRC(t, arc); got != 0x01020304 {
		t.Fatalf("FILE_CRC = %08x, want 01020304", got)
	}
}

// The sample is what makes the packed path testable at all: rardecode must
// unpack it to the text libarchive's fixture says it holds, and must report
// its own checksum failure when the recorded CRC is wrong.
func TestRARPackedSampleEntry_unpacksThroughRardecode(t *testing.T) {
	t.Parallel()
	e := RARPackedSampleEntry([]byte("01.jpg"))
	if e.Method != RARPackedSampleMethod || e.UnpSize != int64(len(RARPackedSampleText)) {
		t.Fatalf("entry = method %#x unp %d, want %#x %d",
			e.Method, e.UnpSize, RARPackedSampleMethod, len(RARPackedSampleText))
	}
	if crc32.ChecksumIEEE([]byte(RARPackedSampleText)) != RARPackedSampleCRC {
		t.Fatalf("RARPackedSampleCRC does not match RARPackedSampleText")
	}

	out, err := unpackFirst(t, BuildRAR4(t, RAR4Spec{Entries: []RAR4Entry{e}}))
	if err != nil {
		t.Fatalf("unpack: %v", err)
	}
	if string(out) != RARPackedSampleText {
		t.Fatalf("unpacked %q, want %q", out, RARPackedSampleText)
	}
}

func TestRARPackedSampleEntry_wrongCRCIsRardecodesChecksumError(t *testing.T) {
	t.Parallel()
	e := RARPackedSampleEntry([]byte("01.jpg"))
	e.CRC ^= 1
	_, err := unpackFirst(t, BuildRAR4(t, RAR4Spec{Entries: []RAR4Entry{e}}))
	if !errors.Is(err, rardecode.ErrBadFileChecksum) {
		t.Fatalf("err = %v, want rardecode.ErrBadFileChecksum", err)
	}
}

// Each call hands out its own bytes, so a test that damages them cannot reach
// a parallel test's fixture.
func TestRARPackedSample_isAFreshCopy(t *testing.T) {
	t.Parallel()
	a := RARPackedSample()
	if len(a) != 30 {
		t.Fatalf("len = %d, want 30", len(a))
	}
	a[0] ^= 0xff
	if b := RARPackedSample(); bytes.Equal(a, b) {
		t.Fatal("RARPackedSample returned shared storage")
	}
}

func unpackFirst(t *testing.T, arc []byte) ([]byte, error) {
	t.Helper()
	r, err := rardecode.NewReader(bytes.NewReader(arc))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	if _, err := r.Next(); err != nil {
		t.Fatalf("Next: %v", err)
	}
	return io.ReadAll(r)
}
