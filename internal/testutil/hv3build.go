package testutil

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"testing"
	"time"
	"unicode/utf16"
)

// The HV3 writer, hand-rolled for the same reason the ZIP and RAR writers are:
// nothing writes HoneyView containers except HoneyView, and half the shapes
// internal/archive/hv3 must survive — a LIST that lies about its length, a
// record pointing past the end of the file, an ENCR mode nobody has decoded —
// are things a real packer would never produce on request.
//
// It writes the layout measured on `펌프킨 시저스 04.hv3` exactly: the six
// container tags with 16-byte headers, the leaf chunks with 8-byte ones, the
// directory at the front and the payloads behind a `BODY` chunk. A file it
// produces with HV3EncrMask set round-trips through the real reference
// extractor.

// HV3 ENCR modes. HV3EncrMask is the byte-position XOR the real container
// uses; HV3EncrNone stores the payload plainly.
const (
	HV3EncrNone uint32 = 0
	HV3EncrMask uint32 = 2
)

// HV3Entry is one file in a container.
type HV3Entry struct {
	// Name is written as UTF-16LE with the trailing NUL a real container pads
	// with. It is the display path, so `dir/page.jpg` nests.
	Name string
	Data []byte
	// Modified is written to MTIM as a Windows FILETIME. Zero writes zero,
	// which the reader reports as no timestamp at all.
	Modified time.Time
	// OmitCRC, OmitSize, OmitPos drop a field the reader requires, for the
	// tests that assert a record missing one is refused rather than guessed at.
	OmitCRC  bool
	OmitSize bool
	OmitPos  bool
	// Pos8 writes the offset as the 64-bit POS8 field instead of POS4. The
	// real container uses POS4 throughout; a file over 4 GiB could not.
	Pos8 bool
	// SizeOverride, when non-nil, is written to SIZE instead of len(Data), so
	// a test can build a record whose declared length disagrees with the FILE
	// block behind it.
	SizeOverride *uint32
}

// HV3Spec is a whole container.
type HV3Spec struct {
	Entries []HV3Entry
	// Encr is the ENCR chunk's value. Modes other than the two constants above
	// are written verbatim, which is how the "this build declines mode N" path
	// is reached.
	Encr uint32
	// OmitEncr writes no ENCR chunk at all. The reader must treat that as
	// HV3EncrNone, which is what the reference extractor does.
	OmitEncr bool
	// Title and Maker fill the HEAD block's TITL and MAKR chunks. Empty writes
	// the chunk with an empty payload, exactly as a container with no title
	// does.
	Title string
	Maker string
	// ListSizeOverride, when non-nil, is written as the LIST chunk's 64-bit
	// length instead of the real one — the truncated-directory shape.
	ListSizeOverride *uint64
	// NoSignature corrupts the four magic bytes.
	NoSignature bool
}

// BuildHV3 assembles a container and returns its bytes.
func BuildHV3(t testing.TB, spec HV3Spec) []byte {
	t.Helper()
	return HV3Bytes(spec)
}

// HV3Bytes is BuildHV3 without a testing.TB, for scripts/mkfixture — so the
// e2e fixture tree and the unit fixtures are produced by one writer rather than
// by two that could drift.
//
// It cannot fail: unlike the RAR writer there is no field here whose width a
// spec can overflow, because every length in the format is written from what
// the caller actually supplied.
func HV3Bytes(spec HV3Spec) []byte {
	// The directory records the absolute offset of each FILE block, so the
	// layout is computed first and the bytes are written second. Everything
	// before BODY is fixed-width once the names are known.
	names := make([][]byte, len(spec.Entries))
	for i, e := range spec.Entries {
		names[i] = hv3UTF16(e.Name)
	}

	var head bytes.Buffer
	hv3Leaf(&head, "GUID", make([]byte, 16))
	hv3Leaf(&head, "UUID", make([]byte, 16))
	hv3Leaf(&head, "FTIM", hv3FileTime(time.Time{}))
	hv3Leaf(&head, "TITL", hv3UTF16(spec.Title))
	hv3Leaf(&head, "MAKR", hv3UTF16(spec.Maker))
	if !spec.OmitEncr {
		hv3Leaf(&head, "ENCR", hv3U32(spec.Encr))
	}

	// The record bodies, with a placeholder offset that is patched once the
	// body's position is known.
	type patch struct{ at, width int }
	var list bytes.Buffer
	patches := make([]patch, len(spec.Entries))
	for i, e := range spec.Entries {
		var rec bytes.Buffer
		hv3Leaf(&rec, "NAME", names[i])
		if !e.OmitPos {
			patches[i].width = 4
			if e.Pos8 {
				patches[i].width = 8
			}
			// Recorded relative to the record buffer, fixed up below once the
			// FINF header and the chunks before it are accounted for. The
			// payload starts after the leaf header, not after the tag.
			patches[i].at = rec.Len() + hv3LeafHeaderLen
			if e.Pos8 {
				hv3Leaf(&rec, "POS8", make([]byte, 8))
			} else {
				hv3Leaf(&rec, "POS4", make([]byte, 4))
			}
		}
		if !e.OmitSize {
			size := uint32(len(e.Data))
			if e.SizeOverride != nil {
				size = *e.SizeOverride
			}
			hv3Leaf(&rec, "SIZE", hv3U32(size))
		}
		if !e.OmitCRC {
			hv3Leaf(&rec, "CRC3", hv3U32(crc32.ChecksumIEEE(e.Data)))
		}
		hv3Leaf(&rec, "MTIM", hv3FileTime(e.Modified))

		if patches[i].width > 0 {
			patches[i].at += list.Len() + hv3ContainerHeaderLen
		}
		hv3Container(&list, "FINF", uint32(rec.Len()), 0)
		list.Write(rec.Bytes())
	}

	// Offsets are absolute, so everything in front of BODY has to be sized
	// before a single one can be written.
	const magicLen = hv3ContainerHeaderLen + 2*hv3LeafHeaderLen + 8 // HV30 + VERS + FSIZ
	headLen := hv3ContainerHeaderLen + head.Len()
	listLen := hv3ContainerHeaderLen + list.Len()
	bodyOff := int64(magicLen + headLen + listLen)
	filesOff := bodyOff + hv3ContainerHeaderLen

	listBytes := list.Bytes()
	at := filesOff
	for i, e := range spec.Entries {
		if p := patches[i]; p.width == 4 {
			binary.LittleEndian.PutUint32(listBytes[p.at:], uint32(at))
		} else if p.width == 8 {
			binary.LittleEndian.PutUint64(listBytes[p.at:], uint64(at))
		}
		at += hv3ContainerHeaderLen + int64(len(e.Data))
	}

	var body bytes.Buffer
	for _, e := range spec.Entries {
		payload := append([]byte(nil), e.Data...)
		if spec.Encr == HV3EncrMask && !spec.OmitEncr {
			hv3Mask(payload)
		}
		// The FILE block: the tag, eight bytes that are zero in every one of
		// the real container's 104, and the payload length.
		body.WriteString("FILE")
		body.Write(make([]byte, 8))
		body.Write(hv3U32(uint32(len(e.Data))))
		body.Write(payload)
	}

	total := bodyOff + hv3ContainerHeaderLen + int64(body.Len())

	var out bytes.Buffer
	if spec.NoSignature {
		out.WriteString("NOPE")
	} else {
		out.WriteString("HV30")
	}
	// HV30's 32-bit field is the length of its two children; its 64-bit field
	// is everything from the end of FSIZ to the end of the file.
	out.Write(hv3U32(2*hv3LeafHeaderLen + 8))
	binary.Write(&out, binary.LittleEndian, uint64(total-magicLen)) //nolint:errcheck // bytes.Buffer never fails
	hv3Leaf(&out, "VERS", []byte{0x07, 0x04, 0x08, 0x20})
	hv3Leaf(&out, "FSIZ", hv3U32(uint32(total)))

	hv3Container(&out, "HEAD", uint32(head.Len()), uint64(listLen))
	out.Write(head.Bytes())

	listSize := uint64(len(listBytes))
	if spec.ListSizeOverride != nil {
		listSize = *spec.ListSizeOverride
	}
	hv3Container(&out, "LIST", 0, listSize)
	out.Write(listBytes)

	hv3Container(&out, "BODY", 0, uint64(body.Len()))
	out.Write(body.Bytes())
	return out.Bytes()
}

// HV3Pages is the convenience the archive tests use: n JPEG-ish pages named
// `0001.jpg`… with distinguishable contents.
func HV3Pages(n int) []HV3Entry {
	out := make([]HV3Entry, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, HV3Entry{
			Name:     hv3PageName(i),
			Data:     hv3PageBytes(i),
			Modified: time.Date(2009, 1, 6, 12, 0, 0, 0, time.UTC),
		})
	}
	return out
}

// HV3Mask applies the ENCR mode 2 transform, exported so a test can build the
// masked bytes it expects to read back.
func HV3Mask(b []byte) { hv3Mask(b) }

func hv3Mask(b []byte) {
	for i := range b {
		b[i] ^= byte(i)
	}
}

func hv3PageName(i int) string {
	return string([]byte{byte('0' + i/1000%10), byte('0' + i/100%10), byte('0' + i/10%10), byte('0' + i%10)}) + ".jpg"
}

// hv3PageBytes is a JPEG header followed by filler that differs per page, so a
// test that streams the wrong entry sees a different CRC rather than a
// coincidence.
func hv3PageBytes(i int) []byte {
	b := []byte{0xFF, 0xD8, 0xFF, 0xE0}
	for n := 0; n < 64+i; n++ {
		b = append(b, byte(i*31+n))
	}
	return append(b, 0xFF, 0xD9)
}

// Header widths, mirroring internal/archive/hv3's own constants. They are
// spelled again here on purpose: a fixture that imported the reader's
// constants would agree with it by construction, and a test that cannot
// disagree is not a test.
const (
	hv3LeafHeaderLen      = 8
	hv3ContainerHeaderLen = 16
)

func hv3Leaf(w *bytes.Buffer, tag string, payload []byte) {
	w.WriteString(tag)
	w.Write(hv3U32(uint32(len(payload))))
	w.Write(payload)
}

func hv3Container(w *bytes.Buffer, tag string, len32 uint32, len64 uint64) {
	w.WriteString(tag)
	w.Write(hv3U32(len32))
	binary.Write(w, binary.LittleEndian, len64) //nolint:errcheck // bytes.Buffer never fails
}

func hv3U32(v uint32) []byte {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], v)
	return b[:]
}

// hv3UTF16 encodes a name the way a container does: UTF-16LE with one trailing
// NUL. An empty string still gets the NUL, which is what the real file writes
// for an empty TITL.
func hv3UTF16(s string) []byte {
	u := utf16.Encode([]rune(s + "\x00"))
	b := make([]byte, 2*len(u))
	for i, r := range u {
		binary.LittleEndian.PutUint16(b[2*i:], r)
	}
	return b
}

// hv3FileTime writes a Windows FILETIME: 100-nanosecond ticks since 1601.
func hv3FileTime(t time.Time) []byte {
	var b [8]byte
	if !t.IsZero() {
		ticks := uint64(t.Unix()+11644473600)*10_000_000 + uint64(t.Nanosecond())/100
		binary.LittleEndian.PutUint64(b[:], ticks)
	}
	return b[:]
}
