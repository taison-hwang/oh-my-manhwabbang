package rar5

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
)

// The RAR 5.0 header layout, from the RAR 5.0 archive format technical note:
//
//	CRC32        4      of everything from the size field to the end of the extra area
//	size         vint   of the header from the type field to the end of the extra area
//	type         vint
//	flags        vint
//	[extra size] vint   when headExtra
//	[data size]  vint   when headData
//	...type-specific fields, extra area, then the data area
const (
	typeMain    = 1
	typeFile    = 2
	typeService = 3
	typeCrypt   = 4
	typeEnd     = 5

	headExtra       = 0x0001
	headData        = 0x0002
	headSplitBefore = 0x0008
	headSplitAfter  = 0x0010

	arcVolume = 0x0001
	arcSolid  = 0x0004

	fileDir         = 0x0001
	fileTime        = 0x0002
	fileCRC         = 0x0004
	fileUnknownSize = 0x0008

	compSolid = 0x0040

	extraCrypt = 0x01

	// maxHeaderSize is the technical note's own limit: "Header size ...
	// must not exceed 2 MB".
	maxHeaderSize = 2 << 20
	// maxVintLen covers a 64-bit value; anything longer is not a vint.
	maxVintLen = 10
)

var signature = [8]byte{0x52, 0x61, 0x72, 0x21, 0x1A, 0x07, 0x01, 0x00}

type header struct {
	off      int64
	hdrLen   int64 // from off to the first byte of the data area
	typ      uint64
	flags    uint64
	dataSize int64
	body     []byte
	extra    []byte
}

func (h header) total() int64 { return h.hdrLen + h.dataSize }

// readHeader parses the header at off. size bounds every length the header
// declares; OpenEntry, which has no container size, passes math.MaxInt64 and
// relies on ReadAt failing instead.
func readHeader(r io.ReaderAt, off, size int64) (header, error) {
	h := header{off: off}
	if off < 0 || size-off < 4+1 {
		return h, fmt.Errorf("rar: %w: header at %d is past the end of a %d-byte archive", ErrTruncated, off, size)
	}
	var lead [4 + 3]byte
	n := int64(len(lead))
	if size-off < n {
		n = size - off
	}
	if _, err := r.ReadAt(lead[:n], off); err != nil {
		return h, fmt.Errorf("rar: reading header at %d: %w", off, err)
	}
	c := cursor{b: lead[4:n]}
	hsize := c.vint()
	if c.bad || hsize < 2 || hsize > maxHeaderSize {
		return h, fmt.Errorf("rar: %w at %d: header size", ErrBadHeader, off)
	}
	sizeLen := int64(c.pos)
	h.hdrLen = 4 + sizeLen + int64(hsize)
	if h.hdrLen > size-off {
		return h, fmt.Errorf("rar: %w at %d: header claims %d bytes, %d remain", ErrTruncated, off, h.hdrLen, size-off)
	}
	raw := make([]byte, h.hdrLen)
	if _, err := r.ReadAt(raw, off); err != nil {
		return h, fmt.Errorf("rar: reading header at %d: %w", off, err)
	}
	if crc32.ChecksumIEEE(raw[4:]) != binary.LittleEndian.Uint32(raw[:4]) {
		return h, fmt.Errorf("rar: %w at %d: header CRC mismatch", ErrBadHeader, off)
	}

	c = cursor{b: raw[4+sizeLen:]}
	h.typ = c.vint()
	h.flags = c.vint()
	var extraSize, dataSize uint64
	if h.flags&headExtra != 0 {
		extraSize = c.vint()
	}
	if h.flags&headData != 0 {
		dataSize = c.vint()
	}
	if c.bad || extraSize > uint64(len(c.b)-c.pos) {
		return h, fmt.Errorf("rar: %w at %d: fields run past the header", ErrBadHeader, off)
	}
	rest := c.b[c.pos:]
	h.body = rest[:len(rest)-int(extraSize)]
	h.extra = rest[len(rest)-int(extraSize):]
	if dataSize > uint64(size-off-h.hdrLen) {
		return h, fmt.Errorf("rar: %w at %d: data area of %d bytes, %d remain", ErrTruncated, off, dataSize, size-off-h.hdrLen)
	}
	h.dataSize = int64(dataSize)
	return h, nil
}

type fileHeader struct {
	header
	fileFlags uint64
	unpSize   int64
	crc32     uint32
	compInfo  uint64
	name      []byte
	encrypted bool
}

func (f fileHeader) method() uint64  { return (f.compInfo >> 7) & 7 }
func (f fileHeader) version() uint64 { return f.compInfo & 0x3f }

// window is the decoder's dictionary for this entry, with the reduction
// rardecode applies to a non-solid entry of known size (reader.go, v2.3.0),
// so that the limit enforced here is the allocation rardecode would make.
func (f fileHeader) window() int64 {
	bits := (f.compInfo >> 10) & 0x1f
	w := int64(0x20000) << bits
	if f.version() == 1 {
		w += w / 32 * int64((f.compInfo>>15)&0x1f)
	}
	if f.fileFlags&fileUnknownSize == 0 && w > f.unpSize {
		w = f.unpSize
	}
	return w
}

func parseFile(h header) (fileHeader, error) {
	f := fileHeader{header: h}
	c := cursor{b: h.body}
	f.fileFlags = c.vint()
	unp := c.vint()
	c.vint() // attributes
	if f.fileFlags&fileTime != 0 {
		c.u32()
	}
	if f.fileFlags&fileCRC != 0 {
		f.crc32 = c.u32()
	}
	f.compInfo = c.vint()
	c.vint() // host OS
	nameLen := c.vint()
	f.name = c.take(nameLen)
	if c.bad {
		return f, fmt.Errorf("rar: %w at %d: file header fields run past the header", ErrBadHeader, h.off)
	}
	if unp > 1<<62 {
		return f, fmt.Errorf("rar: %w at %d: unpacked size %d", ErrBadHeader, h.off, unp)
	}
	f.unpSize = int64(unp)

	x := cursor{b: h.extra}
	for x.pos < len(x.b) {
		recSize := x.vint()
		rec := cursor{b: x.take(recSize)}
		if x.bad {
			return f, fmt.Errorf("rar: %w at %d: extra record runs past the header", ErrBadHeader, h.off)
		}
		if rec.vint() == extraCrypt {
			f.encrypted = true
		}
	}
	return f, nil
}

type cursor struct {
	b   []byte
	pos int
	bad bool
}

func (c *cursor) vint() uint64 {
	var v uint64
	for i := 0; i < maxVintLen; i++ {
		if c.pos >= len(c.b) {
			c.bad = true
			return 0
		}
		b := c.b[c.pos]
		c.pos++
		v |= uint64(b&0x7f) << (7 * i)
		if b&0x80 == 0 {
			return v
		}
	}
	c.bad = true
	return 0
}

func (c *cursor) u32() uint32 {
	b := c.take(4)
	if len(b) < 4 {
		return 0
	}
	return binary.LittleEndian.Uint32(b)
}

func (c *cursor) take(n uint64) []byte {
	if c.bad || n > uint64(len(c.b)-c.pos) {
		c.bad = true
		return nil
	}
	b := c.b[c.pos : c.pos+int(n)]
	c.pos += int(n)
	return b
}
