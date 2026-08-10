// Package nested exposes an archive stored *inside* another archive as an
// io.ReaderAt, so that internal/archive/zipidx can index it and stream pages
// out of it without knowing it is nested at all.
//
// # Why this exists
//
// prd §7.2 and decision D-07 put nested archives out of scope, and 45 books in
// the reference collection are exactly that: a ZIP holding nothing but more
// ZIPs, 623 of them in total, which indexed as `비어 있음` with zero pages.
// `겟 벡커스 1~39완.zip` is 1.4 GB and 39 volumes, and none of it was readable.
// This package is what makes those volumes books.
//
// # Why an io.ReaderAt and not an extraction
//
// Everything above [archive.Reader] already speaks io.ReaderAt: ReadIndex and
// OpenEntry both take one. An inner archive presented as an io.ReaderAt is
// therefore just an archive, and the entire indexing and page-serving path
// works on it unchanged — no second format, no temporary files, no cache
// directory to size or evict (AC-001 keeps its meaning).
//
// # The two cases, and why one of them is free
//
//   - A **stored** inner archive is a contiguous byte range of the outer file.
//     [io.SectionReader] over it *is* the ReaderAt, with true random access and
//     no buffering. 13 of the 623 are like this.
//   - A **deflated** inner archive has no random access: offset N is only
//     reachable by inflating the N bytes before it. The other 610 are like
//     this, and [inflateAt] below is the adapter for them.
//
// The deflated case is much cheaper here than it sounds, because the entries
// are already-compressed JPEGs: measured over all 623 inner archives, 16.9 GB
// uncompressed is stored in 16.9 GB — a ratio of 1.0000, with 618 of them above
// 0.99. Inflating is very nearly a copy.
package nested

import (
	"compress/flate"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"

	"shelf/internal/archive"
)

// ErrUnsupportedMethod is returned for an inner archive stored with a method
// this package cannot stream. It wraps archive.ErrUnsupportedMethod so the
// book is classified, not crashed.
var ErrUnsupportedMethod = archive.ErrUnsupportedMethod

// tailWindow is how many bytes at the end of an inner archive are kept once
// they have been inflated.
//
// It is sized by what the reader above actually asks for. zipidx locates the
// end record within the last 65,557 bytes and then reads the central directory,
// which is ~70 bytes per entry — 70 KiB for a 1,000-page volume, and the
// largest book in the collection has 1,540 pages. 2 MiB covers every archive
// here with room to spare, and the cost of being wrong is a re-inflate, not a
// wrong answer.
const tailWindow = 2 << 20

// ReaderAt is an inner archive presented as a random-access byte range.
//
// Size is the inner archive's own length, which is what zipidx must be handed
// as the container size. Close releases the adapter's inflate stream; it never
// touches the outer container's handle, which belongs to the caller's pool.
type ReaderAt interface {
	io.ReaderAt
	io.Closer
	Size() int64
}

// Open presents the inner entry described by ref as a ReaderAt over its
// uncompressed bytes.
//
// outer must stay valid for the lifetime of the returned reader; this package
// never closes it. ref is the *outer* archive's entry for the inner container,
// exactly as the index recorded it.
func Open(ctx context.Context, outer io.ReaderAt, ref archive.EntryRef) (ReaderAt, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if ref.LocalHdrOff < 0 || ref.CompSize < 0 || ref.Size < 0 {
		return nil, fmt.Errorf("nested: %w (offset %d, sizes %d/%d)",
			archive.ErrCorrupt, ref.LocalHdrOff, ref.CompSize, ref.Size)
	}
	dataOff, err := payloadOffset(outer, ref)
	if err != nil {
		return nil, err
	}

	switch ref.Method {
	case archive.MethodStore:
		// The inner archive is literally a window onto the outer file. This is
		// the whole implementation for that case.
		return &storedAt{SectionReader: io.NewSectionReader(outer, dataOff, ref.Size)}, nil
	case archive.MethodDeflate:
		return &inflateAt{outer: outer, dataOff: dataOff, compSize: ref.CompSize, size: ref.Size}, nil
	default:
		return nil, fmt.Errorf("nested: %w (method %d)", ErrUnsupportedMethod, ref.Method)
	}
}

// payloadOffset reads the inner entry's local header to find where its bytes
// begin. It is the same 30-byte read zipidx.OpenEntry does, repeated here
// because this package must also refuse an encrypted inner archive rather than
// hand back a reader over ciphertext.
func payloadOffset(outer io.ReaderAt, ref archive.EntryRef) (int64, error) {
	const localHeaderLen = 30
	const sigLocalFile = 0x04034b50

	var lfh [localHeaderLen]byte
	if _, err := outer.ReadAt(lfh[:], ref.LocalHdrOff); err != nil {
		return 0, fmt.Errorf("nested: reading local header at %d: %w", ref.LocalHdrOff, err)
	}
	if binary.LittleEndian.Uint32(lfh[0:]) != sigLocalFile {
		return 0, fmt.Errorf("nested: %w: no local header at %d", archive.ErrCorrupt, ref.LocalHdrOff)
	}
	if binary.LittleEndian.Uint16(lfh[6:])&archive.FlagEncrypted != 0 {
		return 0, fmt.Errorf("nested: %w", archive.ErrEncrypted)
	}
	nameLen := int64(binary.LittleEndian.Uint16(lfh[26:]))
	extraLen := int64(binary.LittleEndian.Uint16(lfh[28:]))
	return ref.LocalHdrOff + localHeaderLen + nameLen + extraLen, nil
}

// storedAt is the uncompressed case: a plain section of the outer file.
type storedAt struct {
	*io.SectionReader
}

func (s *storedAt) Close() error { return nil }

func (s *storedAt) Size() int64 { return s.SectionReader.Size() }

// inflateAt makes a deflate stream answer ReadAt.
//
// It holds one inflate stream and the logical offset that stream has reached.
// A read ahead of that offset is served by discarding the bytes in between; a
// read behind it restarts the stream. That is O(offset) per backward seek, so
// the access pattern matters — and the two patterns this product has are both
// well behaved:
//
//   - **Indexing** reads only the end of the archive (end record, then central
//     directory). The first such read inflates the whole thing once and keeps
//     the last [tailWindow] bytes, after which every further directory read is
//     answered from memory with no I/O at all.
//   - **Serving a page** reads one entry's local header and then its payload,
//     forward, through an io.SectionReader. Offsets only increase, so the
//     stream is never restarted: the cost is inflating up to the page's offset
//     once, and reading a manga volume front to back pays that once per page
//     rather than once per byte.
//
// It is safe for concurrent use, but concurrent readers at distant offsets will
// thrash the single stream. Callers that serve pages concurrently should open
// one inflateAt per stream, which is what internal/source does.
type inflateAt struct {
	outer    io.ReaderAt
	dataOff  int64 // where the deflate stream starts in the outer file
	compSize int64 // its length there
	size     int64 // the inner archive's uncompressed length

	mu     sync.Mutex
	stream io.ReadCloser
	pos    int64 // logical offset of the next byte stream will produce

	// tail is the last min(size, tailWindow) bytes, valid once tailDone is set.
	tail     []byte
	tailOff  int64
	tailDone bool
	closed   bool
}

func (z *inflateAt) Size() int64 { return z.size }

func (z *inflateAt) Close() error {
	z.mu.Lock()
	defer z.mu.Unlock()
	z.closed = true
	z.tail = nil
	return z.closeStream()
}

func (z *inflateAt) closeStream() error {
	if z.stream == nil {
		return nil
	}
	err := z.stream.Close()
	z.stream, z.pos = nil, 0
	return err
}

// restart throws away the current stream and opens a new one at offset 0.
func (z *inflateAt) restart() error {
	if err := z.closeStream(); err != nil {
		return err
	}
	sec := io.NewSectionReader(z.outer, z.dataOff, z.compSize)
	z.stream = flate.NewReader(sec)
	z.pos = 0
	return nil
}

// discard advances the stream by n bytes, feeding whatever it passes through
// into the tail window when the tail is being built.
func (z *inflateAt) discard(n int64, collect bool) error {
	buf := make([]byte, 32<<10)
	for n > 0 {
		want := int64(len(buf))
		if n < want {
			want = n
		}
		got, err := io.ReadFull(z.stream, buf[:want])
		if got > 0 {
			z.pos += int64(got)
			if collect {
				z.appendTail(buf[:got])
			}
			n -= int64(got)
		}
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return io.EOF
			}
			return err
		}
	}
	return nil
}

// appendTail keeps the most recent tailWindow bytes seen.
func (z *inflateAt) appendTail(b []byte) {
	if len(b) >= tailWindow {
		z.tail = append(z.tail[:0], b[len(b)-tailWindow:]...)
		z.tailOff = z.pos - int64(len(z.tail))
		return
	}
	z.tail = append(z.tail, b...)
	if len(z.tail) > tailWindow {
		z.tail = append(z.tail[:0], z.tail[len(z.tail)-tailWindow:]...)
	}
	z.tailOff = z.pos - int64(len(z.tail))
}

// fillTail inflates to the end of the archive so that the tail window holds its
// last bytes. It runs at most once per reader.
func (z *inflateAt) fillTail() error {
	if z.tailDone {
		return nil
	}
	if z.stream == nil || z.pos > 0 {
		if err := z.restart(); err != nil {
			return err
		}
	}
	z.tail, z.tailOff = z.tail[:0], 0
	if err := z.discard(z.size-z.pos, true); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	z.tailDone = true
	return nil
}

// ReadAt implements io.ReaderAt. A short read at the end of the archive comes
// back with io.EOF, as the interface requires.
func (z *inflateAt) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, fmt.Errorf("nested: negative offset %d", off)
	}
	if len(p) == 0 {
		return 0, nil
	}

	z.mu.Lock()
	defer z.mu.Unlock()
	if z.closed {
		return 0, errors.New("nested: reader is closed")
	}
	if off >= z.size {
		return 0, io.EOF
	}

	// Clamp to the archive's length so a caller asking past the end gets a
	// short read plus io.EOF rather than an inflate error.
	want := int64(len(p))
	short := false
	if off+want > z.size {
		want = z.size - off
		short = true
	}

	// The directory lives at the end, so anything in the tail window is worth
	// one full inflate and is then free forever.
	if off+want > z.size-int64(tailWindow) {
		if err := z.fillTail(); err != nil {
			return 0, err
		}
		if off >= z.tailOff {
			lo := off - z.tailOff
			n := copy(p[:want], z.tail[lo:])
			if int64(n) < want || short {
				return n, io.EOF
			}
			return n, nil
		}
		// The window was not big enough to reach back this far; fall through
		// to the sequential path, which is always correct.
	}

	if z.stream == nil || off < z.pos {
		if err := z.restart(); err != nil {
			return 0, err
		}
	}
	if off > z.pos {
		if err := z.discard(off-z.pos, false); err != nil {
			if errors.Is(err, io.EOF) {
				return 0, io.EOF
			}
			return 0, err
		}
	}
	n, err := io.ReadFull(z.stream, p[:want])
	z.pos += int64(n)
	switch {
	case err == nil && short:
		return n, io.EOF
	case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
		return n, io.EOF
	default:
		return n, err
	}
}

var (
	_ ReaderAt = (*storedAt)(nil)
	_ ReaderAt = (*inflateAt)(nil)
)
