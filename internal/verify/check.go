package verify

import (
	"fmt"
	"hash/crc32"
	"io"
)

// CheckStream reads r to its end and holds what came out against what the
// container recorded for the entry: exactly size bytes, hashing to crc
// (CRC-32, IEEE — the polynomial ZIP, RAR 4 and HV3 all record).
//
// It is the only CRC check in the product. Page serving never hashes (an
// archive.EntryRef carries the CRC and nothing reads it), and E-58 keeps it
// that way: verifying is an operator's offline question, not a cost every page
// request should pay.
//
// A read error is returned as is, wrapped, so errors.Is still sees
// rardecode.ErrBadFileChecksum — which is how a packed RAR entry reports its
// own checksum failure, at EOF — and flate.CorruptInputError.
//
// The read is bounded at size+1 bytes: one byte past the recorded size is
// enough to prove the stream is too long, and a damaged length field must not
// turn a verify into an unbounded read. Reading up to size+1 also means a
// stream of the right length is read through to its EOF, which is the moment
// rardecode checks its checksum.
func CheckStream(r io.Reader, size int64, crc uint32) error {
	h := crc32.NewIEEE()
	n, err := io.Copy(h, io.LimitReader(r, size+1))
	if err != nil {
		return fmt.Errorf("read error after %d bytes: %w", n, err)
	}
	switch {
	case n < size:
		return fmt.Errorf("short read: %d of %d bytes", n, size)
	case n > size:
		return fmt.Errorf("long read: more than the recorded %d bytes", size)
	}
	if got := h.Sum32(); got != crc {
		return fmt.Errorf("crc mismatch: want %08x got %08x", crc, got)
	}
	return nil
}
