// Package testutil builds synthetic fixtures so the unit suite is hermetic and
// fast (arch §10.3). Nothing here is compiled into the product binary: every
// entry point takes a testing.TB.
//
// The ZIP writers are deliberately hand-rolled rather than layered on
// archive/zip. Half the shapes the scanner must survive — a GP bit-0
// "encrypted" flag, raw CP949 name bytes that are not valid UTF-8, a 40 KiB
// archive comment, a truncated tail, a forced ZIP64 header on a 12-byte file —
// are exactly the things archive/zip.Writer refuses to produce.
package testutil

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"hash/crc32"
	"testing"
	"time"

	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/korean"
)

// ZIP structure signatures and fixed record sizes (APPNOTE 6.3.x §4.3).
const (
	sigLocalFile     uint32 = 0x04034b50
	sigCentralDir    uint32 = 0x02014b50
	sigEOCD          uint32 = 0x06054b50
	sigZIP64EOCD     uint32 = 0x06064b50
	sigZIP64Locator  uint32 = 0x07064b50
	localHeaderLen          = 30
	centralHeaderLen        = 46
	eocdLen                 = 22
	zip64EOCDLen            = 56
	zip64LocatorLen         = 20

	// zip64Marker is the 32-bit sentinel that says "the real value lives in the
	// 0x0001 extra field".
	zip64Marker uint32 = 0xffffffff
	// zip16Marker is its 16-bit counterpart, used for record counts in the EOCD.
	zip16Marker uint16 = 0xffff

	// encryptionHeaderLen is the size of the ZipCrypto encryption header that
	// precedes the payload of an entry with GP bit 0 set (APPNOTE §7.1).
	encryptionHeaderLen = 12
)

// Compression methods. Only these two occur in the target collection.
const (
	MethodStore   uint16 = 0
	MethodDeflate uint16 = 8
)

// General-purpose bit-flag bits that the scanner has to reason about
// (FR-IDX-008, FR-IDX-010).
const (
	// FlagEncrypted is GP bit 0. Set it to make a well-formed archive whose
	// entries cannot be read — zipidx must report this as an encrypted book
	// rather than a corrupt one.
	//
	// Setting it also prepends encryptionHeaderLen bytes to the payload, the
	// way a real ZipCrypto entry is laid out. That matters: Go's archive/zip
	// does not look at bit 0 at all, so a flag-only fixture would still
	// decompress cleanly and the "encrypted" case would silently test nothing.
	FlagEncrypted uint16 = 1 << 0
	// FlagUTF8 is GP bit 11 (the "language encoding" / EFS flag). When clear,
	// a non-ASCII name has to be probed for UTF-8 and then decoded as CP949
	// (arch §4.4).
	FlagUTF8 uint16 = 1 << 11
)

// Entry describes one member of a synthetic archive.
//
// Name and RawName are alternatives: RawName wins when non-nil and is written
// to both headers byte-for-byte, which is what makes CP949 golden vectors
// exact. Name is UTF-8 and is a convenience for ASCII fixtures — it does NOT
// imply FlagUTF8; set that explicitly so the flagless-but-UTF-8 branch of
// arch §4.4 stays testable.
type Entry struct {
	Name    string // UTF-8 name, used when RawName is nil
	RawName []byte // exact name bytes; wins over Name

	Data   []byte // uncompressed content; ignored when Dir is true
	Method uint16 // MethodStore (default) or MethodDeflate
	Flags  uint16 // extra GP flags, OR-ed in (FlagEncrypted, FlagUTF8, ...)

	// Dir marks a directory entry: a trailing "/" is appended to the name if
	// missing, the content is forced empty, and the MS-DOS directory attribute
	// plus a 0755 unix mode are set. FR-IDX-006 requires these to be skipped.
	Dir bool

	Modified time.Time // defaults to a fixed 2016 timestamp
	Comment  string    // per-entry comment, central directory only

	// ExternalAttrs overrides the computed external file attributes when non-nil.
	ExternalAttrs *uint32

	// CRC32Override replaces the computed CRC-32. Used to fabricate the
	// "central directory disagrees with the payload" corruption shape.
	CRC32Override *uint32

	// Extra is appended verbatim to the central-directory extra field. The
	// ZIP64 extra is generated separately by BuildZIP64.
	Extra []byte
}

// ZIPSpec describes a whole synthetic archive.
type ZIPSpec struct {
	Entries []Entry

	// Comment is the archive comment stored after the EOCD. A comment longer
	// than 1 KiB forces the second, 65 557-byte tail scan in zipidx
	// (WP-04 acceptance 1); CommentSize is the lazy way to ask for one.
	Comment []byte
	// CommentSize, when > 0 and Comment is nil, generates a comment of exactly
	// that many bytes. Use CommentSize40KiB for the documented fixture.
	CommentSize int

	// TruncateTail removes this many bytes from the end of the finished
	// archive. Enough truncation destroys the EOCD (zipidx.ErrNoEOCD); a
	// smaller amount leaves the EOCD pointing past the end of the file.
	TruncateTail int

	// CorruptEOCDSignature flips one byte of the EOCD signature, producing a
	// file that has the right length but no locatable end record.
	CorruptEOCDSignature bool

	// OffsetBias is added to every offset the archive records: each entry's
	// local-header offset in the central directory, and the central-directory
	// offset in the EOCD.
	//
	// Set it to -len(Prefix) to reproduce a self-extracting archive whose
	// writer numbered offsets from the start of the ZIP payload rather than
	// from the start of the file. That is a *recoverable* shape, not a broken
	// one: archive/zip derives baseOffset = eocdOffset - dirSize - dirOffset
	// and adds it back to every header offset, so zipidx must do the same or
	// the differential oracle (impl-plan C-6) disagrees on a real shape.
	OffsetBias int64

	// Prefix is written before the first local header. Real self-extracting
	// archives carry an executable stub here, which is why every offset in the
	// central directory is absolute rather than relative.
	Prefix []byte
}

// CommentSize40KiB is the archive-comment length called for by impl-plan §3
// WP-00 acceptance 5 and arch §10.1.
const CommentSize40KiB = 40 * 1024

// defaultModified is a fixed timestamp inside the 2014–2018 window that
// AC-002 cares about, so fixtures are byte-reproducible across runs.
var defaultModified = time.Date(2016, time.March, 14, 9, 26, 54, 0, time.UTC)

// BuildZIP renders spec into a complete ZIP archive.
//
// It supports every shape WP-04 and WP-08 have to survive: stored and deflated
// entries, GP bit 0 and bit 11 in any combination, arbitrary raw name bytes,
// 0-byte entries, directory entries, a 40 KiB archive comment, a truncated
// tail and a corrupted end record.
func BuildZIP(t testing.TB, spec ZIPSpec) []byte {
	t.Helper()
	return buildZIP(t, spec, zip64Options{})
}

// zip64Options controls the ZIP64 escalation applied by BuildZIP64.
type zip64Options struct {
	force        bool // write the ZIP64 EOCD record + locator and 0x0001 extras
	includeDisk  bool // also emit the 4-byte disk-start slot in the extra field
	localHeaders bool // put 0x0001 extras in the local headers too
}

func buildZIP(t testing.TB, spec ZIPSpec, z64 zip64Options) []byte {
	t.Helper()

	var buf bytes.Buffer
	buf.Write(spec.Prefix)

	type record struct {
		name        []byte
		flags       uint16
		method      uint16
		crc         uint32
		compSize    uint64
		uncompSize  uint64
		localOffset uint64
		modTime     uint16
		modDate     uint16
		extra       []byte
		comment     string
		externalAtt uint32
	}
	records := make([]record, 0, len(spec.Entries))

	for i := range spec.Entries {
		e := spec.Entries[i]
		name := entryName(e)

		data := e.Data
		if e.Dir {
			data = nil
		}

		method := e.Method
		payload := data
		if method == MethodDeflate {
			payload = deflate(t, data)
		} else if method != MethodStore {
			t.Fatalf("testutil: entry %q uses unsupported method %d (want %d or %d)",
				name, method, MethodStore, MethodDeflate)
		}

		if e.Flags&FlagEncrypted != 0 {
			// A ZipCrypto entry stores a 12-byte encryption header followed by
			// the ciphertext, and the compressed size counts both. We do not
			// implement the cipher — nothing in this product will ever decrypt
			// an entry — but the header must be there and the bytes after it
			// must not decompress, or the fixture is indistinguishable from a
			// plaintext one.
			payload = append(encryptionHeader(name), payload...)
		}

		crc := crc32.ChecksumIEEE(data)
		if e.CRC32Override != nil {
			crc = *e.CRC32Override
		}

		mod := e.Modified
		if mod.IsZero() {
			mod = defaultModified
		}
		modTime, modDate := dosTime(mod)

		rec := record{
			name:        name,
			flags:       e.Flags,
			method:      method,
			crc:         crc,
			compSize:    uint64(len(payload)),
			uncompSize:  uint64(len(data)),
			localOffset: uint64(buf.Len()),
			modTime:     modTime,
			modDate:     modDate,
			extra:       e.Extra,
			comment:     e.Comment,
			externalAtt: externalAttrs(e),
		}

		versionNeeded := uint16(20)
		if z64.force {
			versionNeeded = 45
		}

		// --- local file header (30 bytes + name + extra) ---
		var localExtra []byte
		localCompSize, localUncompSize := u32(rec.compSize), u32(rec.uncompSize)
		if z64.force && z64.localHeaders {
			localExtra = zip64Extra(rec.uncompSize, rec.compSize, nil, false)
			localCompSize, localUncompSize = zip64Marker, zip64Marker
		}
		put32(&buf, sigLocalFile)
		put16(&buf, versionNeeded)
		put16(&buf, rec.flags)
		put16(&buf, rec.method)
		put16(&buf, rec.modTime)
		put16(&buf, rec.modDate)
		put32(&buf, rec.crc)
		put32(&buf, localCompSize)
		put32(&buf, localUncompSize)
		put16(&buf, uint16(len(rec.name)))
		put16(&buf, uint16(len(localExtra)))
		buf.Write(rec.name)
		buf.Write(localExtra)
		buf.Write(payload)

		records = append(records, rec)
	}

	// --- central directory ---
	cdOffset := uint64(buf.Len())
	for i := range records {
		rec := records[i]

		versionNeeded := uint16(20)
		recorded := uint64(int64(rec.localOffset) + spec.OffsetBias)
		cdCompSize, cdUncompSize, cdLocalOffset := u32(rec.compSize), u32(rec.uncompSize), u32(recorded)
		extra := rec.extra
		if z64.force {
			versionNeeded = 45
			off := recorded
			extra = append(zip64Extra(rec.uncompSize, rec.compSize, &off, z64.includeDisk), extra...)
			cdCompSize, cdUncompSize, cdLocalOffset = zip64Marker, zip64Marker, zip64Marker
		}

		put32(&buf, sigCentralDir)
		put16(&buf, 0x031e) // version made by: unix, spec 3.0
		put16(&buf, versionNeeded)
		put16(&buf, rec.flags)
		put16(&buf, rec.method)
		put16(&buf, rec.modTime)
		put16(&buf, rec.modDate)
		put32(&buf, rec.crc)
		put32(&buf, cdCompSize)
		put32(&buf, cdUncompSize)
		put16(&buf, uint16(len(rec.name)))
		put16(&buf, uint16(len(extra)))
		put16(&buf, uint16(len(rec.comment)))
		put16(&buf, 0) // disk number start
		put16(&buf, 0) // internal file attributes
		put32(&buf, rec.externalAtt)
		put32(&buf, cdLocalOffset)
		buf.Write(rec.name)
		buf.Write(extra)
		buf.WriteString(rec.comment)
	}
	cdSize := uint64(buf.Len()) - cdOffset
	total := uint64(len(records))

	// --- ZIP64 end records, when forced ---
	recordedCDOffset := uint64(int64(cdOffset) + spec.OffsetBias)
	eocdRecords, eocdCDSize, eocdCDOffset := uint16(total), u32(cdSize), u32(recordedCDOffset)
	if z64.force {
		z64Offset := uint64(buf.Len())

		put32(&buf, sigZIP64EOCD)
		put64(&buf, uint64(zip64EOCDLen-12)) // size of the record after this field
		put16(&buf, 45)                      // version made by
		put16(&buf, 45)                      // version needed
		put32(&buf, 0)                       // this disk number
		put32(&buf, 0)                       // disk with the central directory
		put64(&buf, total)                   // entries on this disk
		put64(&buf, total)                   // entries in total
		put64(&buf, cdSize)
		put64(&buf, recordedCDOffset)

		put32(&buf, sigZIP64Locator)
		put32(&buf, 0) // disk holding the ZIP64 EOCD
		put64(&buf, z64Offset)
		put32(&buf, 1) // total number of disks

		// Every sentinel is set so a reader that honours any one of them takes
		// the ZIP64 path.
		eocdRecords, eocdCDSize, eocdCDOffset = zip16Marker, zip64Marker, zip64Marker
	}

	// --- end of central directory ---
	comment := spec.Comment
	if comment == nil && spec.CommentSize > 0 {
		comment = filler(spec.CommentSize)
	}
	if len(comment) > 0xffff {
		t.Fatalf("testutil: archive comment is %d bytes, the field is 16-bit", len(comment))
	}

	sig := sigEOCD
	if spec.CorruptEOCDSignature {
		sig ^= 0xff
	}
	put32(&buf, sig)
	put16(&buf, 0) // this disk
	put16(&buf, 0) // disk with the start of the central directory
	put16(&buf, eocdRecords)
	put16(&buf, eocdRecords)
	put32(&buf, eocdCDSize)
	put32(&buf, eocdCDOffset)
	put16(&buf, uint16(len(comment)))
	buf.Write(comment)

	out := buf.Bytes()
	if spec.TruncateTail > 0 {
		if spec.TruncateTail >= len(out) {
			t.Fatalf("testutil: TruncateTail %d >= archive size %d", spec.TruncateTail, len(out))
		}
		out = out[:len(out)-spec.TruncateTail]
	}
	return out
}

// CP949 encodes a UTF-8 string to the raw CP949 (EUC-KR / Windows-949) bytes a
// 2010s Korean archiver would have written, for use as Entry.RawName.
//
// It exists so golden vectors can be stated as readable Korean in the test and
// still hit the decoder as exact bytes: CP949("슈퍼만화데생") is
// "\xbd\xb4\xc6\xdb\xb8\xb8\xc8\xad\xb5\xa5\xbb\xfd", the vector arch §4.4
// pins. This is the *encode* direction only; decoding is WP-02's kenc.
func CP949(t testing.TB, s string) []byte {
	t.Helper()
	out, err := korean.EUCKR.NewEncoder().Bytes([]byte(s))
	if err != nil {
		t.Fatalf("testutil: %q is not representable in CP949: %v", s, err)
	}
	return out
}

// ShiftJIS is CP949's counterpart for the Japanese archives of the collection,
// the encoding kenc.ArchiveFallback selects for a whole container.
//
// Note what it can and cannot state. ShiftJIS("天天-20-001.jpg") is
// "\x93\x56\x93\x56-20-001.jpg", which CP949 *also* reads — as
// "밮밮-20-001.jpg" — so a golden vector built only from names like that one
// proves nothing about detection. A test that means to exercise the fallback
// needs at least one name CP949 cannot read, e.g. one containing kana.
func ShiftJIS(t testing.TB, s string) []byte {
	t.Helper()
	out, err := japanese.ShiftJIS.NewEncoder().Bytes([]byte(s))
	if err != nil {
		t.Fatalf("testutil: %q is not representable in Shift_JIS: %v", s, err)
	}
	return out
}

// entryName resolves an Entry's on-disk name bytes.
func entryName(e Entry) []byte {
	var name []byte
	if e.RawName != nil {
		name = append([]byte(nil), e.RawName...)
	} else {
		name = []byte(e.Name)
	}
	if e.Dir && (len(name) == 0 || name[len(name)-1] != '/') {
		name = append(name, '/')
	}
	return name
}

// externalAttrs mirrors what Info-ZIP writes: the unix mode in the high 16
// bits and the MS-DOS attribute byte in the low 8.
func externalAttrs(e Entry) uint32 {
	if e.ExternalAttrs != nil {
		return *e.ExternalAttrs
	}
	if e.Dir {
		return 0o40755<<16 | 0x10 // S_IFDIR | drwxr-xr-x, MS-DOS directory bit
	}
	return 0o100644 << 16 // S_IFREG | rw-r--r--
}

// deflate compresses data with the raw DEFLATE stream a ZIP entry stores
// (no zlib header, no trailer).
func deflate(t testing.TB, data []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	w, err := flate.NewWriter(&out, flate.DefaultCompression)
	if err != nil {
		t.Fatalf("testutil: creating flate writer: %v", err)
	}
	if _, err := w.Write(data); err != nil {
		t.Fatalf("testutil: deflating %d bytes: %v", len(data), err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("testutil: closing flate writer: %v", err)
	}
	return out.Bytes()
}

// zip64Extra builds the 0x0001 extra field. The order is fixed by APPNOTE
// §4.5.3 — uncompressed size, compressed size, local-header offset, disk
// number — and only the slots whose 32-bit counterpart holds 0xffffffff are
// present. WP-04 parses it in exactly this order.
func zip64Extra(uncompressed, compressed uint64, localOffset *uint64, includeDisk bool) []byte {
	body := make([]byte, 0, 28)
	body = binary.LittleEndian.AppendUint64(body, uncompressed)
	body = binary.LittleEndian.AppendUint64(body, compressed)
	if localOffset != nil {
		body = binary.LittleEndian.AppendUint64(body, *localOffset)
		if includeDisk {
			body = binary.LittleEndian.AppendUint32(body, 0)
		}
	}
	out := make([]byte, 0, 4+len(body))
	out = binary.LittleEndian.AppendUint16(out, 0x0001)
	out = binary.LittleEndian.AppendUint16(out, uint16(len(body)))
	return append(out, body...)
}

// encryptionHeader returns a deterministic 12-byte stand-in for the ZipCrypto
// header. It is derived from the entry name so fixtures stay byte-reproducible
// across runs, and is deliberately not a valid DEFLATE prefix.
func encryptionHeader(name []byte) []byte {
	sum := crc32.ChecksumIEEE(name)
	out := make([]byte, encryptionHeaderLen)
	for i := range out {
		out[i] = byte(sum>>uint(8*(i%4))) ^ 0xa5
	}
	return out
}

// dosTime converts a Go time to the MS-DOS time/date pair ZIP records use.
// The format has 2-second resolution and an epoch of 1980.
func dosTime(t time.Time) (dosT, dosD uint16) {
	if t.Year() < 1980 {
		return 0, 1 << 5 // 1980-01-01 00:00:00, the smallest representable value
	}
	dosT = uint16(t.Second()/2) | uint16(t.Minute())<<5 | uint16(t.Hour())<<11
	dosD = uint16(t.Day()) | uint16(t.Month())<<5 | uint16(t.Year()-1980)<<9
	return dosT, dosD
}

// filler returns n bytes of readable, non-repeating-looking padding so a
// stray EOCD signature cannot appear inside a generated comment by accident.
func filler(n int) []byte {
	const alphabet = "shelf archive comment padding 0123456789 "
	out := make([]byte, n)
	for i := range out {
		out[i] = alphabet[i%len(alphabet)]
	}
	return out
}

func u32(v uint64) uint32 {
	if v > uint64(zip64Marker) {
		return zip64Marker
	}
	return uint32(v)
}

func put16(b *bytes.Buffer, v uint16) { _, _ = b.Write(binary.LittleEndian.AppendUint16(nil, v)) }
func put32(b *bytes.Buffer, v uint32) { _, _ = b.Write(binary.LittleEndian.AppendUint32(nil, v)) }
func put64(b *bytes.Buffer, v uint64) { _, _ = b.Write(binary.LittleEndian.AppendUint64(nil, v)) }
