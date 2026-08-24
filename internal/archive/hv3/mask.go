package hv3

import (
	"errors"
	"io"
)

// unmask applies the ENCR mode 2 transform in place: every byte is XORed with
// its own position within the entry, counted from 0 and taken modulo 256.
//
//	plain[i] = stored[i] ^ byte(i)
//
// off is the position of b[0] within the entry, which is what makes the
// transform seekable: byte 100,000 can be produced without touching byte
// 99,999, so a Range request costs exactly the range. That is the property
// FR-SRV-002 needs and it is the reason this is a stream wrapper rather than a
// decode-to-buffer step.
//
// It is an involution — applying it twice returns the original bytes — so the
// same function would also produce an HV3, which is what the round-trip test
// uses it for.
func unmask(b []byte, off int64) {
	for i := range b {
		b[i] ^= byte(off + int64(i))
	}
}

// sectionReadCloser is an unmasked entry's payload: a read-only, seekable
// window onto the container with a Close that does nothing, because the
// container's handle belongs to the pool and not to this reader.
//
// io.NopCloser would have been shorter and wrong — it drops the Seeker, and
// with it Range support for every page.
type sectionReadCloser struct {
	*io.SectionReader
}

// Close implements io.Closer. Releasing the underlying file is the handle
// pool's job (internal/openpool); a page stream must never close it.
func (*sectionReadCloser) Close() error { return nil }

// maskedReader is the same window with [unmask] applied as the bytes go past.
//
// It keeps the Seeker, which is the whole point: http.ServeContent seeks to
// the end to size the body and then back to the start, and a Range request
// seeks to the range. The position each read starts at is taken from the
// section itself rather than tracked here, so a seek the caller performs is
// automatically the position the mask uses — the failure mode of a separately
// counted offset is a body that decodes correctly only when it is read from
// byte 0.
type maskedReader struct {
	sec *io.SectionReader
}

func (m *maskedReader) Read(p []byte) (int, error) {
	off, err := m.sec.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, err
	}
	n, err := m.sec.Read(p)
	unmask(p[:n], off)
	return n, err
}

func (m *maskedReader) ReadAt(p []byte, off int64) (int, error) {
	n, err := m.sec.ReadAt(p, off)
	unmask(p[:n], off)
	return n, err
}

func (m *maskedReader) Seek(offset int64, whence int) (int64, error) {
	return m.sec.Seek(offset, whence)
}

// Size reports the entry's length, so a caller that has the stream but not the
// index row can still size the body.
func (m *maskedReader) Size() int64 { return m.sec.Size() }

// Close implements io.Closer, on the same terms as [sectionReadCloser].
func (*maskedReader) Close() error { return nil }

// isNoList reports whether err is the "directory not found" refusal.
//
// It exists because [Reader.OpenEntry] tolerates exactly that one failure — it
// needs the ENCR mode from the header, not the directory, and the directory
// was read successfully at index time or this page would not exist — while
// still refusing a file that is not an HV3 at all.
func isNoList(err error) bool { return errors.Is(err, ErrNoList) }

var (
	_ io.ReadSeeker = (*sectionReadCloser)(nil)
	_ io.ReaderAt   = (*sectionReadCloser)(nil)
	_ io.ReadCloser = (*sectionReadCloser)(nil)
	_ io.ReadSeeker = (*maskedReader)(nil)
	_ io.ReaderAt   = (*maskedReader)(nil)
	_ io.ReadCloser = (*maskedReader)(nil)
)
