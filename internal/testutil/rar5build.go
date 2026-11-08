package testutil

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash/crc32"
	"testing"
)

// A RAR 5.0 writer, hand-rolled for the reason rarbuild.go gives for RAR 4: no
// Go library writes RAR, and the shapes the reader must refuse — a solid flag,
// a volume flag, an encryption record, a split entry — are header bits a real
// packer only produces on request. It packs nothing itself; the one packed
// stream a fixture can use is RAR5PackedSample.

// RAR 5.0 header types and the header flags this writer can emit, named as in
// the RAR 5.0 archive format technical note.
const (
	rar5TypeMain    = 1
	rar5TypeFile    = 2
	rar5TypeService = 3
	rar5TypeCrypt   = 4
	rar5TypeEnd     = 5

	rar5HeadExtra = 0x0001
	rar5HeadData  = 0x0002
)

// Flags a fixture can ask for.
const (
	RAR5ArcVolume uint64 = 0x0001
	RAR5ArcSolid  uint64 = 0x0004

	RAR5HeadSplitBefore uint64 = 0x0008
	RAR5HeadSplitAfter  uint64 = 0x0010

	RAR5CompSolid uint64 = 0x0040
)

// RAR5Sample is a complete RAR 5.0 archive written by a real packer:
// testdata/rar.rar of github.com/gabriel-vasile/mimetype v1.4.3. It holds one
// packed entry, asd.go (method 3, 187 bytes unpacked into 157, CRC-32
// RAR5PackedSampleCRC), and an end-of-archive header. Used verbatim under the
// MIT licence:
//
//	Copyright (c) 2018-2020 Gabriel Vasile
//
//	Permission is hereby granted, free of charge, to any person obtaining a copy
//	of this software and associated documentation files (the "Software"), to deal
//	in the Software without restriction, including without limitation the rights
//	to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
//	copies of the Software, and to permit persons to whom the Software is
//	furnished to do so, subject to the following conditions:
//
//	The above copyright notice and this permission notice shall be included in all
//	copies or substantial portions of the Software.
//
//	THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
//	IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
//	FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
//	AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
//	LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
//	OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
//	SOFTWARE.
//
// testutil is test-only, so the bytes never reach the shipped binary.
const (
	RAR5PackedSampleText = "package main\n\nimport (\n\t\"time\"\n\n\t\"github.com/davecgh/go-spew/spew\"\n)\n\n" +
		"func main() {\n\tloc, _ := time.LoadLocation(\"Indian/Cocos\")\n\n\tspew.Dump(time.Now().In(loc))\n" +
		"\tspew.Dump(time.Now())\n\n}\n"
	RAR5PackedSampleCRC uint32 = 0x230ceab5
	// RAR5PackedSampleCompInfo is the entry's compression-information field:
	// version 0, method 3, dictionary 128 KiB.
	RAR5PackedSampleCompInfo uint64 = 0x180

	rar5SampleHex = "526172211a0701003392b5e50a0105060005010180800046cd35491c02029d0106bb01b483028000f35ab5ea0c2380030106" +
		"6173642e676f" + rar5PackedSampleDataHex + "1d77565103050400"
	rar5PackedSampleDataHex = "c5059a26544342f66044dd9385426a90164de83a974a08f054b664664164bc1c91cd08f7a52e4cdd9c5aecbfc7aaab" +
		"93d9747ab455141f7dc7f106f807b8f10848c684711f533a4d722708b906ae0f84f0a765b462cbc3db9afc18f1db962ecd96b109441de926e8de" +
		"951fe125e45bb0fd4fbb79ecc2f9885e54d02d048e47295f0093381701c91556eb30be342989d0dd073fe59027f54ad4fd54fe98"
)

// RAR5Sample returns a fresh copy of the whole sample archive.
func RAR5Sample() []byte { return mustHex(rar5SampleHex) }

// RAR5PackedSampleEntry is the sample's entry as a RAR5Entry, for a fixture
// that wants a packed entry among stored ones. A checksum failure is built by
// changing e.CRC.
func RAR5PackedSampleEntry(name string) RAR5Entry {
	return RAR5Entry{
		Name:     []byte(name),
		Data:     mustHex(rar5PackedSampleDataHex),
		CompInfo: RAR5PackedSampleCompInfo,
		UnpSize:  int64(len(RAR5PackedSampleText)),
		CRC:      RAR5PackedSampleCRC,
	}
}

func mustHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

// RAR5Entry is one file header and its data area.
type RAR5Entry struct {
	Name []byte // written verbatim; RAR 5 names are UTF-8
	Data []byte

	Dir bool

	// CompInfo is the compression-information field; zero means stored with
	// the smallest dictionary. Method lives in bits 7-9, the solid bit is
	// RAR5CompSolid, the dictionary size in bits 10-14.
	CompInfo uint64

	// HeadFlags are OR-ed into the header flags: RAR5HeadSplitBefore/After.
	HeadFlags uint64

	// UnpSize overrides the recorded unpacked size; zero means len(Data).
	UnpSize int64
	// DataSize overrides the recorded data-area size, which is how a header
	// that claims more bytes than the file holds is built.
	DataSize int64
	// CRC overrides the recorded CRC-32; zero means crc32(Data).
	CRC uint32
	// NoCRC leaves the CRC-32 field out, the shape `rar -htb` writes when it
	// records a BLAKE2 hash instead.
	NoCRC bool
	// Encrypted adds a file-encryption extra record (type 1).
	Encrypted bool
	// TimeAndHash adds the file-time (type 3) and BLAKE2sp hash (type 2)
	// extra records WinRAR commonly writes, which a reader must step over.
	TimeAndHash bool
}

// RAR5Spec describes a whole synthetic RAR 5.0 archive.
type RAR5Spec struct {
	Entries []RAR5Entry

	// ArcFlags are the main header's archive flags: RAR5ArcVolume,
	// RAR5ArcSolid.
	ArcFlags uint64

	// HeaderEncryption writes an archive-encryption header in place of the
	// main header, the shape `rar -hp` produces.
	HeaderEncryption bool

	// Comment adds a CMT service header with this text after the entries.
	Comment string

	OmitEnd      bool
	TruncateTail int
}

// BuildRAR5 returns a synthetic RAR 5.0 archive, failing the test on a spec it
// cannot honour.
func BuildRAR5(t testing.TB, spec RAR5Spec) []byte {
	t.Helper()
	out, err := RAR5Bytes(spec)
	if err != nil {
		t.Fatalf("testutil: %v", err)
	}
	return out
}

// RAR5Bytes is BuildRAR5 without a testing.TB, for scripts/mkfixture.
func RAR5Bytes(spec RAR5Spec) ([]byte, error) {
	out := []byte{0x52, 0x61, 0x72, 0x21, 0x1A, 0x07, 0x01, 0x00}
	if spec.HeaderEncryption {
		// encryption version 0, flags 0, KDF count 15, 16-byte salt
		body := rar5Vint(nil, 0)
		body = rar5Vint(body, 0)
		body = append(body, 15)
		body = append(body, make([]byte, 16)...)
		out = append(out, rar5Header(rar5TypeCrypt, 0, body, nil, 0)...)
		return rar5Finish(out, spec), nil
	}
	out = append(out, rar5Header(rar5TypeMain, 0, rar5Vint(nil, spec.ArcFlags), nil, 0)...)

	for i, e := range spec.Entries {
		if e.Dir && len(e.Data) > 0 {
			return nil, fmt.Errorf("RAR5 entry %d: a directory carries no data", i)
		}
		var fileFlags uint64
		if e.Dir {
			fileFlags |= 0x0001
		}
		if !e.NoCRC {
			fileFlags |= 0x0004
		}
		unp := e.UnpSize
		if unp == 0 {
			unp = int64(len(e.Data))
		}
		body := rar5Vint(nil, fileFlags)
		body = rar5Vint(body, uint64(unp))
		body = rar5Vint(body, 0x20) // attributes
		if !e.NoCRC {
			c := e.CRC
			if c == 0 {
				c = crc32.ChecksumIEEE(e.Data)
			}
			body = binary.LittleEndian.AppendUint32(body, c)
		}
		body = rar5Vint(body, e.CompInfo)
		body = rar5Vint(body, 1) // host OS: Unix
		body = rar5Vint(body, uint64(len(e.Name)))
		body = append(body, e.Name...)

		var extra []byte
		if e.TimeAndHash {
			rec := rar5Vint(nil, 3)
			rec = rar5Vint(rec, 0x0001|0x0002) // Unix time format, mtime present
			rec = binary.LittleEndian.AppendUint32(rec, 1_700_000_000)
			extra = append(extra, rar5Vint(nil, uint64(len(rec)))...)
			extra = append(extra, rec...)
			rec = rar5Vint(nil, 2)
			rec = rar5Vint(rec, 0) // BLAKE2sp
			rec = append(rec, make([]byte, 32)...)
			extra = append(extra, rar5Vint(nil, uint64(len(rec)))...)
			extra = append(extra, rec...)
		}
		if e.Encrypted {
			// version 0, flags 0, KDF count 15, salt 16, IV 16
			rec := rar5Vint(nil, 1)
			rec = rar5Vint(rec, 0)
			rec = rar5Vint(rec, 0)
			rec = append(rec, 15)
			rec = append(rec, make([]byte, 32)...)
			extra = append(extra, rar5Vint(nil, uint64(len(rec)))...)
			extra = append(extra, rec...)
		}
		dataSize := int64(len(e.Data))
		if e.DataSize != 0 {
			dataSize = e.DataSize
		}
		out = append(out, rar5Header(rar5TypeFile, e.HeadFlags, body, extra, dataSize)...)
		out = append(out, e.Data...)
	}

	if spec.Comment != "" {
		body := rar5Vint(nil, 0)
		body = rar5Vint(body, uint64(len(spec.Comment)))
		body = rar5Vint(body, 0)
		body = rar5Vint(body, 0) // stored
		body = rar5Vint(body, 1)
		body = rar5Vint(body, 3)
		body = append(body, "CMT"...)
		out = append(out, rar5Header(rar5TypeService, 0, body, nil, int64(len(spec.Comment)))...)
		out = append(out, spec.Comment...)
	}
	return rar5Finish(out, spec), nil
}

func rar5Finish(out []byte, spec RAR5Spec) []byte {
	if !spec.OmitEnd {
		out = append(out, rar5Header(rar5TypeEnd, 0, rar5Vint(nil, 0), nil, 0)...)
	}
	if spec.TruncateTail > 0 && spec.TruncateTail <= len(out) {
		out = out[:len(out)-spec.TruncateTail]
	}
	return out
}

// rar5Header frames one header: CRC32, size, type, flags, the optional extra
// and data sizes, the type-specific body and the extra area. The data area is
// the caller's to append.
func rar5Header(typ, flags uint64, body, extra []byte, dataSize int64) []byte {
	if len(extra) > 0 {
		flags |= rar5HeadExtra
	}
	if dataSize > 0 {
		flags |= rar5HeadData
	}
	h := rar5Vint(nil, typ)
	h = rar5Vint(h, flags)
	if len(extra) > 0 {
		h = rar5Vint(h, uint64(len(extra)))
	}
	if dataSize > 0 {
		h = rar5Vint(h, uint64(dataSize))
	}
	h = append(h, body...)
	h = append(h, extra...)
	sized := rar5Vint(nil, uint64(len(h)))
	sized = append(sized, h...)
	out := binary.LittleEndian.AppendUint32(nil, crc32.ChecksumIEEE(sized))
	return append(out, sized...)
}

func rar5Vint(b []byte, v uint64) []byte {
	for v >= 0x80 {
		b = append(b, byte(v)|0x80)
		v >>= 7
	}
	return append(b, byte(v))
}
