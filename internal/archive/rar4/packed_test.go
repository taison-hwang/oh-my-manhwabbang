package rar4

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/nwaples/rardecode/v2"

	"shelf/internal/testutil"
)

// The packed branch of OpenEntry (splice → rardecode), against a real RAR
// compressor's output: libarchive's 30-byte sample, which unpacks to
// testutil.RARPackedSampleText with CRC-32 testutil.RARPackedSampleCRC.
// Before it existed the branch was only reached through the real-collection
// integration test, which does not run where the collection is absent.
func packedArchive(t *testing.T, crc uint32) []byte {
	t.Helper()
	e := testutil.RARPackedSampleEntry([]byte("test.txt"))
	e.CRC = crc
	return newBuilder(t).mainHeader(0).entry(e).bytes()
}

func TestOpenEntry_packedDecodesToTheOriginalAndIsForwardOnly(t *testing.T) {
	raw := packedArchive(t, testutil.RARPackedSampleCRC)
	ix, err := readIndex(t, raw)
	if err != nil {
		t.Fatalf("ReadIndex: %v", err)
	}
	if len(ix.Entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(ix.Entries))
	}
	e := ix.Entries[0]
	if e.Method == MethodStore {
		t.Fatalf("method = 0x%02x: the fixture is not packed", e.Method)
	}
	if want := int64(len(testutil.RARPackedSampleText)); e.Size != want {
		t.Errorf("Size = %d, want %d", e.Size, want)
	}
	if want := int64(len(testutil.RARPackedSample())); e.CompSize != want {
		t.Errorf("CompSize = %d, want %d", e.CompSize, want)
	}
	if e.CRC32 != testutil.RARPackedSampleCRC {
		t.Errorf("CRC32 = %08x, want %08x", e.CRC32, testutil.RARPackedSampleCRC)
	}

	rc, err := New().OpenEntry(t.Context(), bytes.NewReader(raw), e.Ref())
	if err != nil {
		t.Fatalf("OpenEntry: %v", err)
	}
	defer func() { _ = rc.Close() }()

	// arch §5.3: a packed entry is a forward-only stream. Were it to claim
	// io.Seeker, the HTTP layer would advertise Range on it.
	if _, ok := rc.(io.Seeker); ok {
		t.Errorf("packed entry is %T, which claims io.Seeker", rc)
	}
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != testutil.RARPackedSampleText {
		t.Errorf("decoded %q, want %q", got, testutil.RARPackedSampleText)
	}
}

// A packed entry whose recorded CRC does not match what it unpacks to is a
// read error at EOF — rardecode's ErrBadFileChecksum — never a clean read.
// This is the error internal/verify reports as a defect.
func TestOpenEntry_packedWithAWrongCRC_failsWithBadFileChecksum(t *testing.T) {
	raw := packedArchive(t, testutil.RARPackedSampleCRC^1)
	ix, err := readIndex(t, raw)
	if err != nil {
		t.Fatalf("ReadIndex: %v", err)
	}
	rc, err := New().OpenEntry(t.Context(), bytes.NewReader(raw), ix.Entries[0].Ref())
	if err != nil {
		t.Fatalf("OpenEntry: %v", err)
	}
	defer func() { _ = rc.Close() }()
	// The whole body arrives before the error: the checksum can only be judged
	// at EOF, so an error that came early would be a different defect.
	got, err := io.ReadAll(rc)
	if !errors.Is(err, rardecode.ErrBadFileChecksum) {
		t.Fatalf("ReadAll error = %v, want rardecode.ErrBadFileChecksum", err)
	}
	if string(got) != testutil.RARPackedSampleText {
		t.Errorf("read %q before the error, want the whole %q", got, testutil.RARPackedSampleText)
	}
}
