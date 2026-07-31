package zipidx

import (
	"compress/flate"
	"context"
	"encoding/binary"
	"fmt"
	"io"

	"shelf/internal/archive"
)

// OpenEntry streams one entry straight out of the container.
//
// This is FR-SRV-001, FR-SRV-002 and NFR-PRF-006 in eleven lines of I/O: one
// 30-byte read at the offset the index already holds to learn the local
// header's two variable-length fields, then a section reader over the entry's
// own bytes. The central directory is not consulted, no temporary file is
// created (AC-001), and the number of bytes that leave the disk is exactly
// CompSize + 30 whether the archive is 4 MB or 1.4 GB (AC-008).
//
// The returned reader is a *SectionReadCloser for stored entries — it
// implements io.ReadSeeker, so the HTTP layer can hand it to
// http.ServeContent and get Range support for free (arch §5.3). Deflated
// entries are forward-only.
func OpenEntry(ctx context.Context, r io.ReaderAt, ref archive.EntryRef) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if ref.LocalHdrOff < 0 || ref.CompSize < 0 {
		return nil, fmt.Errorf("zip: %w (offset %d, compressed size %d)",
			ErrBadLocalHeader, ref.LocalHdrOff, ref.CompSize)
	}

	var lfh [localHeaderLen]byte
	if _, err := r.ReadAt(lfh[:], ref.LocalHdrOff); err != nil {
		return nil, fmt.Errorf("zip: reading local header at %d: %w", ref.LocalHdrOff, err)
	}
	if binary.LittleEndian.Uint32(lfh[0:]) != sigLocalFile {
		return nil, fmt.Errorf("zip: %w at %d", ErrBadLocalHeader, ref.LocalHdrOff)
	}

	// The local header repeats the general-purpose flags, so the encryption
	// check costs nothing and does not depend on the index carrying a flags
	// column. FR-IDX-010: an encrypted entry is reported, never decoded.
	if binary.LittleEndian.Uint16(lfh[6:])&archive.FlagEncrypted != 0 {
		return nil, fmt.Errorf("zip: %w", ErrEncrypted)
	}

	nameLen := int64(binary.LittleEndian.Uint16(lfh[26:]))
	extraLen := int64(binary.LittleEndian.Uint16(lfh[28:]))
	dataOff := ref.LocalHdrOff + localHeaderLen + nameLen + extraLen

	// io.SectionReader is pread-only: it holds its own offset and never seeks
	// the underlying file, which is what makes concurrent reads of one handle
	// safe without a lock (CON-004).
	sec := io.NewSectionReader(r, dataOff, ref.CompSize)

	switch ref.Method {
	case archive.MethodStore:
		// FR-SRV-003: no decompression step at all, and the bytes stay
		// seekable so Range works.
		return &SectionReadCloser{SectionReader: sec}, nil
	case archive.MethodDeflate:
		return flate.NewReader(sec), nil
	default:
		return nil, unsupportedMethod(ref.Method)
	}
}

// DataOffset returns the absolute offset of an entry's payload, which is the
// local-header offset plus the header's own variable length.
//
// It exists for the differential oracle — archive/zip's File.DataOffset() must
// agree with it byte for byte — and for tests that assert the exact read
// accounting. Serving a page does not call it: OpenEntry has the header in
// hand already.
func DataOffset(ctx context.Context, r io.ReaderAt, localHdrOff int64) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	var lfh [localHeaderLen]byte
	if _, err := r.ReadAt(lfh[:], localHdrOff); err != nil {
		return 0, fmt.Errorf("zip: reading local header at %d: %w", localHdrOff, err)
	}
	if binary.LittleEndian.Uint32(lfh[0:]) != sigLocalFile {
		return 0, fmt.Errorf("zip: %w at %d", ErrBadLocalHeader, localHdrOff)
	}
	nameLen := int64(binary.LittleEndian.Uint16(lfh[26:]))
	extraLen := int64(binary.LittleEndian.Uint16(lfh[28:]))
	return localHdrOff + localHeaderLen + nameLen + extraLen, nil
}

// SectionReadCloser is a stored entry's payload: a read-only, seekable window
// onto the container with a Close that does nothing, because the container's
// handle belongs to the pool and not to this reader.
//
// io.NopCloser would have been shorter and wrong — it drops the Seeker, and
// with it Range support for every uncompressed page.
type SectionReadCloser struct {
	*io.SectionReader
}

// Close implements io.Closer. Releasing the underlying file is the handle
// pool's job (internal/openpool); a page stream must never close it.
func (*SectionReadCloser) Close() error { return nil }

var (
	_ io.ReadSeeker = (*SectionReadCloser)(nil)
	_ io.ReaderAt   = (*SectionReadCloser)(nil)
	_ io.ReadCloser = (*SectionReadCloser)(nil)
)
