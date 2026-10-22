package verify

import (
	"bytes"
	"compress/flate"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"strings"
	"testing"

	"github.com/nwaples/rardecode/v2"
)

// The per-page check is the whole of E-58's integrity claim: the bytes a page
// streams must be exactly as many as the container recorded, and must hash to
// the CRC-32 the container recorded. These cases pin each way that can fail.

func TestCheckStream_intactPayloadPasses(t *testing.T) {
	t.Parallel()
	data := []byte("a page that is exactly what the directory says")
	if err := CheckStream(bytes.NewReader(data), int64(len(data)), crc32.ChecksumIEEE(data)); err != nil {
		t.Fatalf("intact payload reported as %v", err)
	}
}

func TestCheckStream_crcMismatchNamesBothSums(t *testing.T) {
	t.Parallel()
	data := []byte("payload")
	want := crc32.ChecksumIEEE(data) ^ 0xdeadbeef
	err := CheckStream(bytes.NewReader(data), int64(len(data)), want)
	if err == nil {
		t.Fatal("a CRC mismatch passed")
	}
	msg := fmt.Sprintf("crc mismatch: want %08x got %08x", want, crc32.ChecksumIEEE(data))
	if err.Error() != msg {
		t.Errorf("cause = %q, want %q", err, msg)
	}
}

func TestCheckStream_fewerBytesThanRecordedIsAShortRead(t *testing.T) {
	t.Parallel()
	data := []byte("abc")
	// The CRC is the CRC of what is there, so only the length can catch it:
	// this is the shape of a stored entry whose recorded size outruns its
	// payload.
	err := CheckStream(bytes.NewReader(data), 5, crc32.ChecksumIEEE(data))
	if err == nil {
		t.Fatal("a short read passed")
	}
	if want := "short read: 3 of 5 bytes"; err.Error() != want {
		t.Errorf("cause = %q, want %q", err, want)
	}
}

func TestCheckStream_moreBytesThanRecordedFails(t *testing.T) {
	t.Parallel()
	data := []byte("abcdef")
	err := CheckStream(bytes.NewReader(data), 3, crc32.ChecksumIEEE(data[:3]))
	if err == nil {
		t.Fatal("a stream longer than its recorded size passed")
	}
	if !strings.Contains(err.Error(), "long read") {
		t.Errorf("cause = %q, want it to say long read", err)
	}
}

// errAfter yields data and then a terminal error instead of io.EOF — the way
// rardecode reports a packed entry whose unpacked CRC disagrees with its
// header, and the way flate reports a damaged stream.
type errAfter struct {
	r   io.Reader
	err error
}

func (e *errAfter) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	if errors.Is(err, io.EOF) {
		return n, e.err
	}
	return n, err
}

// CheckStream's side of the packed path, at the reader boundary: an error that
// arrives with EOF survives the wrap. The end-to-end case, real packed bytes
// through rar4 and rardecode, is TestRun_rarPackedEntryWithWrongCRC_….
func TestCheckStream_rarChecksumErrorAtEOFIsADefect(t *testing.T) {
	t.Parallel()
	data := []byte("unpacked bytes")
	r := &errAfter{r: bytes.NewReader(data), err: rardecode.ErrBadFileChecksum}
	err := CheckStream(r, int64(len(data)), crc32.ChecksumIEEE(data))
	if !errors.Is(err, rardecode.ErrBadFileChecksum) {
		t.Fatalf("err = %v, want rardecode.ErrBadFileChecksum", err)
	}
}

func TestCheckStream_readErrorIsADefect(t *testing.T) {
	t.Parallel()
	data := []byte("half a stream")
	r := &errAfter{r: bytes.NewReader(data), err: flate.CorruptInputError(7)}
	err := CheckStream(r, 100, 0)
	var ce flate.CorruptInputError
	if !errors.As(err, &ce) {
		t.Fatalf("err = %v, want the flate corruption to survive", err)
	}
}
