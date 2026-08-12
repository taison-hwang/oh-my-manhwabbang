package rar4

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"time"
)

// The RAR 4.x block format, from the public RAR 4 technical note. Every block
// begins with the same 7 bytes:
//
//	HEAD_CRC    2   CRC16 of the header from HEAD_TYPE on
//	HEAD_TYPE   1   blockMain, blockFile, …
//	HEAD_FLAGS  2
//	HEAD_SIZE   2   the whole header, these 7 bytes included
//	[ADD_SIZE]  4   present only when flagLongBlock is set: the payload that
//	                follows the header
//
// A block therefore occupies HEAD_SIZE + ADD_SIZE bytes, which is the only
// thing the walk needs to reach the next one. Nothing here reads a payload.
const (
	blockMark   = 0x72 // the 7-byte signature is itself a block
	blockMain   = 0x73
	blockFile   = 0x74
	blockEndArc = 0x7b

	blockHeaderLen = 7
	// mainHeaderLen is the fixed part of a 0x73 block. Real archives in the
	// collection are exactly this size; a comment makes it longer, which is
	// fine because the whole header is copied verbatim by size.
	mainHeaderLen = 13
	// fileHeaderFixedLen is the 0x74 body up to but not including the name.
	fileHeaderFixedLen = 25
)

// Common header flag.
const flagLongBlock = 0x8000

// Main header (0x73) flags.
const (
	mhdVolume   = 0x0001
	mhdSolid    = 0x0008
	mhdPassword = 0x0080
)

// File header (0x74) flags.
const (
	lhdSplitBefore = 0x0001
	lhdSplitAfter  = 0x0002
	lhdPassword    = 0x0004
	lhdSolid       = 0x0010
	// lhdDirMask is the dictionary-size field; all bits set means the entry is
	// a directory rather than a file.
	lhdDirMask = 0x00E0
	lhdLarge   = 0x0100
	lhdUnicode = 0x0200
	lhdSalt    = 0x0400
	lhdExtTime = 0x1000
)

// MethodStore is RAR's "no compression". It is the only method this package
// serves without help: the payload after the header IS the file, so a page is
// an io.SectionReader over the container and Range works (arch §5.3).
//
// 2,685 of the reference collection's 2,914 RAR entries are stored — JPEGs do
// not compress, and the packers knew it.
const MethodStore uint16 = 0x30

// signature is the RAR 4.x marker block. RAR 5 differs in the 8th byte, which
// is how [readSignature] tells the two apart in order to say so.
var signature = [7]byte{0x52, 0x61, 0x72, 0x21, 0x1A, 0x07, 0x00}

// block is one parsed header. rawHeader is retained only for [blockFile] and
// [blockMain]: OpenEntry splices those two byte ranges back together to make a
// one-entry archive, so they must survive the walk unmodified.
type block struct {
	typ   byte
	flags uint16
	// off is the absolute position of the header's first byte. For a file
	// block this becomes archive.Entry.LocalHdrOff — the column FR-SRV-002
	// lives on.
	off int64
	// hdrSize is HEAD_SIZE: the header alone, payload excluded.
	hdrSize int64
	// addSize is ADD_SIZE: the packed payload following the header. For a file
	// block it equals PACK_SIZE.
	addSize int64
}

// total is what the walk advances by to reach the next block.
func (b block) total() int64 { return b.hdrSize + b.addSize }

// fileBlock is a parsed 0x74 header.
type fileBlock struct {
	block
	packSize int64
	unpSize  int64
	crc32    uint32
	method   uint16
	unpVer   byte
	modified time.Time
	rawName  []byte // as stored: OEM bytes, or OEM + NUL + RAR's encoded Unicode
	isDir    bool
}

// readBlockHeader parses the 7-byte common header (plus ADD_SIZE) at off.
//
// It is the one place that decides a walk can continue. Every bound is checked
// against size rather than trusted, because this parses whatever bytes a media
// volume happens to hold: zipidx has a fuzz corpus for the same reason.
func readBlockHeader(r io.ReaderAt, off, size int64) (block, error) {
	var b block
	if off < 0 || off > size-blockHeaderLen {
		return b, fmt.Errorf("rar: %w: block at %d is past the end of a %d-byte archive",
			ErrTruncated, off, size)
	}
	var hdr [blockHeaderLen + 4]byte
	n := blockHeaderLen
	if _, err := r.ReadAt(hdr[:n], off); err != nil {
		return b, fmt.Errorf("rar: reading block header at %d: %w", off, err)
	}
	b.typ = hdr[2]
	b.flags = binary.LittleEndian.Uint16(hdr[3:5])
	b.hdrSize = int64(binary.LittleEndian.Uint16(hdr[5:7]))
	b.off = off

	if b.hdrSize < blockHeaderLen {
		return b, fmt.Errorf("rar: %w at %d: header size %d is below the %d-byte minimum",
			ErrBadBlockHeader, off, b.hdrSize, blockHeaderLen)
	}
	if b.flags&flagLongBlock != 0 {
		if _, err := r.ReadAt(hdr[n:n+4], off+int64(n)); err != nil {
			return b, fmt.Errorf("rar: reading block payload size at %d: %w", off, err)
		}
		b.addSize = int64(binary.LittleEndian.Uint32(hdr[n : n+4]))
	}
	if b.off+b.total() > size || b.total() <= 0 {
		return b, fmt.Errorf("rar: %w at %d: block claims %d bytes, %d remain",
			ErrTruncated, off, b.total(), size-off)
	}
	return b, nil
}

// readSignature checks the 7-byte marker and distinguishes RAR 5, whose files
// look identical to a casual `file(1)` and are a completely different format.
func readSignature(r io.ReaderAt, size int64) error {
	if size < int64(len(signature))+1 {
		return fmt.Errorf("rar: %w (archive is %d bytes)", ErrNoSignature, size)
	}
	var sig [8]byte
	if _, err := r.ReadAt(sig[:], 0); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("rar: reading signature: %w", err)
	}
	if sig[0] == 0x52 && sig[1] == 0x61 && sig[2] == 0x72 && sig[3] == 0x21 &&
		sig[4] == 0x1A && sig[5] == 0x07 && sig[6] == 0x01 && sig[7] == 0x00 {
		// A real format, deliberately declined: RAR 5 replaces the whole block
		// layout and the unpack algorithm. Nothing in the reference collection
		// uses it (0 of 14), and guessing would be worse than saying so.
		return unsupported("RAR 5 archive; this build reads RAR 4")
	}
	if [7]byte(sig[:7]) != signature {
		return fmt.Errorf("rar: %w", ErrNoSignature)
	}
	return nil
}

// parseFileBlock reads the variable-length body of a 0x74 block.
//
// The whole header is read once and then sliced; the name is the only
// variable-length field before the fields we keep, and the optional SALT and
// EXT_TIME tails are skipped rather than parsed because nothing downstream
// needs them.
func parseFileBlock(r io.ReaderAt, b block, size int64) (fileBlock, error) {
	f := fileBlock{block: b}
	if b.hdrSize < blockHeaderLen+fileHeaderFixedLen {
		return f, fmt.Errorf("rar: %w at %d: file header is %d bytes, needs %d",
			ErrBadBlockHeader, b.off, b.hdrSize, blockHeaderLen+fileHeaderFixedLen)
	}
	raw := make([]byte, b.hdrSize)
	if _, err := r.ReadAt(raw, b.off); err != nil {
		return f, fmt.Errorf("rar: reading file header at %d: %w", b.off, err)
	}
	body := raw[blockHeaderLen:]

	f.packSize = int64(binary.LittleEndian.Uint32(body[0:4]))
	f.unpSize = int64(binary.LittleEndian.Uint32(body[4:8]))
	f.crc32 = binary.LittleEndian.Uint32(body[9:13])
	f.modified = dosTime(binary.LittleEndian.Uint32(body[13:17]))
	f.unpVer = body[17]
	f.method = uint16(body[18])
	nameSize := int(binary.LittleEndian.Uint16(body[19:21]))
	f.isDir = b.flags&lhdDirMask == lhdDirMask

	pos := fileHeaderFixedLen
	if b.flags&lhdLarge != 0 {
		// 64-bit sizes: the low halves above are the bottom 32 bits.
		if pos+8 > len(body) {
			return f, fmt.Errorf("rar: %w at %d: 64-bit size fields run past the header",
				ErrBadBlockHeader, b.off)
		}
		f.packSize |= int64(binary.LittleEndian.Uint32(body[pos:pos+4])) << 32
		f.unpSize |= int64(binary.LittleEndian.Uint32(body[pos+4:pos+8])) << 32
		pos += 8
	}
	if nameSize < 0 || pos+nameSize > len(body) {
		return f, fmt.Errorf("rar: %w at %d: name of %d bytes runs past the %d-byte header",
			ErrBadBlockHeader, b.off, nameSize, b.hdrSize)
	}
	f.rawName = body[pos : pos+nameSize]

	if f.packSize < 0 || f.unpSize < 0 {
		return f, fmt.Errorf("rar: %w at %d: negative size (packed %d, unpacked %d)",
			ErrBadBlockHeader, b.off, f.packSize, f.unpSize)
	}

	// ADD_SIZE and PACK_SIZE are the same four bytes of a file block, so a
	// 32-bit payload size was already known before this function ran. The
	// 64-bit one was not: with lhdLarge the true size is those four bytes plus
	// the high half above, and b.addSize is only the low half.
	//
	// So the block's real extent is settled here, not in readBlockHeader, and
	// the caller advances by this. Leaving the truncated value in place would
	// step the walk 4 GB short on an entry that large and read the middle of a
	// payload as the next header.
	f.addSize = f.packSize
	if f.off+f.total() > size || f.total() <= 0 {
		return f, fmt.Errorf("rar: %w at %d: block claims %d bytes, %d remain",
			ErrTruncated, b.off, f.total(), size-b.off)
	}
	return f, nil
}

// dosTime converts an MS-DOS packed date/time to a Time in the local zone,
// which is the only thing the format records — there is no zone in a RAR4
// FTIME, exactly as in a ZIP.
func dosTime(v uint32) time.Time {
	var (
		sec  = int(v&0x1F) * 2
		min  = int(v>>5) & 0x3F
		hour = int(v>>11) & 0x1F
		day  = int(v>>16) & 0x1F
		mon  = int(v>>21) & 0x0F
		year = int(v>>25) + 1980
	)
	if mon < 1 || mon > 12 || day < 1 || day > 31 {
		return time.Time{}
	}
	return time.Date(year, time.Month(mon), day, hour, min, sec, 0, time.Local)
}
