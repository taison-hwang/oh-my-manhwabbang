package testutil

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"testing"
)

// The RAR 4.x writer, hand-rolled for the same reason the ZIP writers are: no
// Go library writes RAR at all, and half the shapes the scanner must survive —
// a solid flag, a multi-volume flag, raw CP949 name bytes, a block chain that
// stops early — are things a real packer would never produce on request.
//
// It writes stored entries only. Every refusal internal/archive/rar4 makes is
// decided from header bits and lengths, none of which depend on the packing
// method, and no Go library compresses RAR either. Fixtures that need real
// packed bytes use the collection, through the integration-tagged tests.

// RAR 4.x block types and the fixed parts of the two headers this writer emits.
const (
	rarBlockMain = 0x73
	rarBlockFile = 0x74
	rarBlockEnd  = 0x7b

	rarBlockHeaderLen = 7
	rarMainHeaderLen  = 13
	rarFileHeaderLen  = 25
)

// Header flags a fixture can ask for. The names are the ones the RAR technical
// note uses.
const (
	RARFlagLongBlock uint16 = 0x8000 // set on every file block; ADD_SIZE follows

	RARMainSolid    uint16 = 0x0008
	RARMainVolume   uint16 = 0x0001
	RARMainPassword uint16 = 0x0080

	RARFileSplitBefore uint16 = 0x0001
	RARFileSplitAfter  uint16 = 0x0002
	RARFilePassword    uint16 = 0x0004
	RARFileSolid       uint16 = 0x0010
	RARFileDirectory   uint16 = 0x00E0
	RARFileLarge       uint16 = 0x0100
	RARFileUnicode     uint16 = 0x0200
)

// RARMethodStore is RAR's "no compression", the only method this writer emits.
const RARMethodStore uint16 = 0x30

// RAR4Entry is one file block.
//
// Name is written verbatim, so a fixture can supply raw CP949 bytes, or the
// `<OEM> NUL <encoded UTF-16>` shape a real packer writes when RARFileUnicode
// is set.
type RAR4Entry struct {
	Name []byte
	Data []byte

	Method uint16 // RARMethodStore (default)
	Flags  uint16 // extra flags, OR-ed in

	// Dir marks a directory entry: the dictionary-size bits are all set and the
	// content is forced empty. FR-IDX-006 requires these to be skipped.
	Dir bool

	// UnpSize overrides the stored uncompressed size; zero means len(Data).
	// PackSize overrides the stored packed size, which is how a header that
	// claims more bytes than the file holds is built.
	UnpSize  int64
	PackSize int64

	// HighPack and HighUnp are the upper 32 bits of the 64-bit sizes. Setting
	// either sets RARFileLarge and adds the eight-byte field, which is the only
	// way to describe an entry above 4 GB.
	HighPack uint32
	HighUnp  uint32
}

// RAR4Spec describes a whole synthetic archive.
type RAR4Spec struct {
	Entries []RAR4Entry

	// MainFlags are the archive-wide flags: RARMainSolid, RARMainVolume,
	// RARMainPassword.
	MainFlags uint16

	// OmitEndBlock leaves off the 0x7b end-of-archive block. Real archives
	// usually carry one; a reader must not require it.
	OmitEndBlock bool

	// TruncateTail removes this many bytes from the finished archive, the
	// interrupted-download shape.
	TruncateTail int

	// Trailing is appended after the end-of-archive block, standing in for the
	// recovery record a RAR may carry. A walk that does not stop at the end
	// block parses it as garbage entries.
	Trailing []byte
}

// BuildRAR4 returns a synthetic RAR 4.x archive, failing the test on a spec it
// cannot honour.
func BuildRAR4(t testing.TB, spec RAR4Spec) []byte {
	t.Helper()
	out, err := RAR4Bytes(spec)
	if err != nil {
		t.Fatalf("testutil: %v", err)
	}
	return out
}

// RAR4Bytes is BuildRAR4 without a testing.TB, for scripts/mkfixture — so the
// e2e fixture tree and the unit fixtures are produced by one writer rather than
// by two that could drift.
func RAR4Bytes(spec RAR4Spec) ([]byte, error) {
	buf := []byte{0x52, 0x61, 0x72, 0x21, 0x1A, 0x07, 0x00} // marker block

	main := make([]byte, rarMainHeaderLen)
	main[2] = rarBlockMain
	binary.LittleEndian.PutUint16(main[3:5], spec.MainFlags)
	binary.LittleEndian.PutUint16(main[5:7], rarMainHeaderLen)
	buf = append(buf, rarSeal(main)...)

	for _, e := range spec.Entries {
		blk, err := rarFileBlock(e)
		if err != nil {
			return nil, err
		}
		buf = append(buf, blk...)
	}

	if !spec.OmitEndBlock {
		end := make([]byte, rarBlockHeaderLen)
		end[2] = rarBlockEnd
		binary.LittleEndian.PutUint16(end[5:7], rarBlockHeaderLen)
		buf = append(buf, rarSeal(end)...)
	}
	buf = append(buf, spec.Trailing...)

	if spec.TruncateTail > 0 {
		if spec.TruncateTail >= len(buf) {
			return nil, fmt.Errorf("TruncateTail %d removes the whole %d-byte archive",
				spec.TruncateTail, len(buf))
		}
		buf = buf[:len(buf)-spec.TruncateTail]
	}
	return buf, nil
}

func rarFileBlock(e RAR4Entry) ([]byte, error) {
	data := e.Data
	flags := e.Flags | RARFlagLongBlock
	if e.Dir {
		flags |= RARFileDirectory
		data = nil
	}
	method := e.Method
	if method == 0 {
		method = RARMethodStore
	}
	unp := e.UnpSize
	if unp == 0 {
		unp = int64(len(data))
	}
	pack := e.PackSize
	if pack == 0 {
		pack = int64(len(data))
	}
	large := e.HighPack != 0 || e.HighUnp != 0

	size := rarBlockHeaderLen + rarFileHeaderLen + len(e.Name)
	if large {
		flags |= RARFileLarge
		size += 8
	}
	if size > 0xFFFF {
		return nil, fmt.Errorf("file header of %d bytes exceeds the 16-bit HEAD_SIZE field", size)
	}

	h := make([]byte, size)
	h[2] = rarBlockFile
	binary.LittleEndian.PutUint16(h[3:5], flags)
	binary.LittleEndian.PutUint16(h[5:7], uint16(size))

	body := h[rarBlockHeaderLen:]
	// PACK_SIZE doubles as the block's ADD_SIZE: they are the same four bytes.
	binary.LittleEndian.PutUint32(body[0:4], uint32(pack))
	binary.LittleEndian.PutUint32(body[4:8], uint32(unp))
	body[8] = 3 // HOST_OS: Unix
	binary.LittleEndian.PutUint32(body[9:13], crc32.ChecksumIEEE(data))
	// FTIME: 2016-01-02 03:04:04, matching the ZIP writer's fixed timestamp.
	binary.LittleEndian.PutUint32(body[13:17], (36<<25)|(1<<21)|(2<<16)|(3<<11)|(4<<5)|2)
	body[17] = 29 // UNP_VER 2.9, what RAR 3.x/4.x writes
	body[18] = byte(method)
	binary.LittleEndian.PutUint16(body[19:21], uint16(len(e.Name)))
	binary.LittleEndian.PutUint32(body[21:25], 0x81a4)

	pos := rarFileHeaderLen
	if large {
		binary.LittleEndian.PutUint32(body[pos:pos+4], e.HighPack)
		binary.LittleEndian.PutUint32(body[pos+4:pos+8], e.HighUnp)
		pos += 8
	}
	copy(body[pos:], e.Name)

	return append(rarSeal(h), data...), nil
}

// rarSeal writes HEAD_CRC, the low 16 bits of the CRC-32 of everything after
// it. Verified against the collection: 나사(Nasa).rar stores 0x90cf for its
// main header and 0xd401 for its first file header, and both are exactly
// crc32(header[2:])&0xFFFF.
func rarSeal(h []byte) []byte {
	binary.LittleEndian.PutUint16(h[0:2], uint16(crc32.ChecksumIEEE(h[2:])))
	return h
}
