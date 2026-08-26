// Package zipidx is a purpose-built, central-directory-only ZIP reader.
//
// It deliberately replaces archive/zip for indexing (decision E-2, which
// accepts the deviation from prd 6.1). The reason is narrow and decisive:
// zip.File does not expose the local file header offset, and FR-SRV-002 —
// 필수 — requires that offset to be persisted so a page request can seek
// straight to one entry. Rebuilding a zip.Reader per page instead measured
// >10 minutes against 32.3 s for this reader over the reference collection.
//
// archive/zip stays in the test suite permanently as a differential oracle:
// for every well-formed fixture the two must agree entry-for-entry, including
// each entry's data offset, and they must agree on the error verdict for the
// malformed ones (see differential_test.go).
//
// What this package never does: decompress an entry while indexing
// (FR-IDX-002), buffer a whole archive (NFR-PRF-006), decode an entry name
// itself (that is internal/kenc, FR-IDX-008), or panic on hostile input
// (fuzz_test.go).
package zipidx

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"time"

	"shelf/internal/archive"
	"shelf/internal/kenc"
)

// ZIP structure signatures and fixed record sizes (APPNOTE 6.3.x §4.3).
const (
	sigCentralDir uint32 = 0x02014b50
	sigLocalFile  uint32 = 0x04034b50
	sigEOCD       uint32 = 0x06054b50

	centralHeaderLen = 46
	localHeaderLen   = 30
	eocdLen          = 22
)

const (
	// tailSmall is the first tail read. arch §4.3 measured this two-step scan
	// at 2.0 ReadAt calls and 9.4 KB per archive over the real collection: the
	// overwhelming majority of archives carry no comment, so the end record —
	// and for small archives the whole central directory — lands in this 1 KiB.
	tailSmall int64 = 1024
	// tailLarge is the second and last tail read: the largest a comment can be
	// (16-bit length) plus the end record itself. An end record further from
	// EOF than this cannot belong to a well-formed archive.
	tailLarge int64 = eocdLen + 0xffff // 65 557

	// maxCentralDirBytes caps what a claimed directory size may make us
	// allocate. 64 MiB is ~1.4 M entries; the largest book in the reference
	// collection has 1 540 pages. A larger claim is a corrupt or hostile end
	// record, and honouring it would breach NFR-PRF-006.
	maxCentralDirBytes = 64 << 20
)

// Reader implements archive.Reader for ZIP containers. It is stateless and
// safe for concurrent use: every call takes the io.ReaderAt it should work on,
// so one value serves the whole process.
type Reader struct{}

// New returns the ZIP implementation of archive.Reader.
func New() Reader { return Reader{} }

var _ archive.Reader = Reader{}

// Format implements archive.Reader.
func (Reader) Format() string { return "zip" }

// ReadIndex implements archive.Reader.
func (Reader) ReadIndex(ctx context.Context, r io.ReaderAt, size int64) (*archive.Index, error) {
	return ReadCentralDirectory(ctx, r, size)
}

// OpenEntry implements archive.Reader.
func (Reader) OpenEntry(ctx context.Context, r io.ReaderAt, ref archive.EntryRef) (io.ReadCloser, error) {
	return OpenEntry(ctx, r, ref)
}

// ReadCentralDirectory parses the central directory of the archive of the
// given size and nothing else (FR-IDX-002).
//
// On a structural failure it returns the entries parsed so far *together with*
// the error, per arch §4.3 step 6: a directory that goes bad at record 812
// still yields 811 readable pages, and FR-IDX-010 asks us to keep the book
// usable rather than drop it.
//
// Entry.Modified is decoded from the MS-DOS timestamp only. The extended
// timestamp extra fields (0x5455, 0x000a, 0x5855) are deliberately not
// consulted: nothing in this product reads a per-entry mtime — content
// versioning keys off the container's own (size, mtime) per arch §5.3 — and
// parsing them would be code with no caller.
func ReadCentralDirectory(ctx context.Context, r io.ReaderAt, size int64) (*archive.Index, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if size < eocdLen {
		return nil, fmt.Errorf("zip: %w (archive is %d bytes)", ErrNoEOCD, size)
	}

	end, tl, err := readEnd(r, size)
	if err != nil {
		// The map is gone; the territory may not be. FR-IDX-010 prefers a book
		// the reader can open with a damaged badge over an empty one, so the
		// local headers get a walk before we give up (see salvage.go). Only
		// structural damage qualifies — an I/O failure means the bytes were
		// never read, and re-reading them front-to-back would not change that.
		if errCorrupt(err) {
			return salvage(ctx, r, size, err)
		}
		return nil, err
	}

	base, err := baseOffset(r, tl, end, size)
	if err != nil {
		return nil, err
	}

	if end.dirSize > maxCentralDirBytes {
		return nil, fmt.Errorf("zip: %w (%d bytes claimed)", ErrCDTooLarge, end.dirSize)
	}

	cdStart := base + int64(end.dirOffset)
	// The directory, plus everything between it and EOF: whatever separates it
	// from the end record, the end records themselves, and the comment. Reading
	// past the claimed directory size is deliberate — archive/zip streams to EOF
	// and stops at the first record that will not parse, so a writer that
	// understates dirSize by a few bytes must not make us disagree with it.
	//
	// The upper bound matters. When baseOffset's fallback below decides the
	// recorded offset was right after all, cdStart is that raw offset, which on
	// a large container can sit far from EOF; "everything to EOF" would then
	// allocate the whole archive and breach NFR-PRF-006. In the ordinary case
	// cdStart == endOffset-dirSize and this expression is exactly size-cdStart,
	// so the bound changes nothing that used to work.
	span := int64(end.dirSize) + (size - end.endOffset)
	if span > size-cdStart {
		span = size - cdStart
	}
	cd, err := readSpan(r, tl, cdStart, span)
	if err != nil {
		return nil, fmt.Errorf("zip: reading central directory: %w", err)
	}

	ix := &archive.Index{
		Comment:    end.comment,
		ZIP64:      end.zip64,
		BaseOffset: base,
	}
	// Preallocate only when the claimed record count is plausible for the
	// bytes available: a malformed end record may claim 2^64-1 entries.
	if uint64(len(cd))/centralHeaderLen >= end.records {
		ix.Entries = make([]archive.Entry, 0, end.records)
	}

	var parseErr error
	var parsed, unusable int
	for off := 0; ; {
		e, n, err := parseCentralHeader(cd[off:], base)
		if err != nil {
			parseErr = err
			break
		}
		if err := ctx.Err(); err != nil {
			return ix, err
		}
		off += n
		parsed++
		if !usableEntry(e.Entry, size) {
			// The record parsed, but its geometry points outside the archive:
			// a negative offset once the base correction is applied, or a size
			// that overflows int64. Found by fuzzing, and real only for
			// corrupt containers.
			//
			// It is still *counted*, so the modulo-65536 tally below — and with
			// it the error verdict archive/zip would reach — is unaffected. It
			// is not *listed*, because an entry at a negative offset can only
			// ever produce a 500 once it is in the index.
			unusable++
			continue
		}
		ix.Entries = append(ix.Entries, e.Entry)
		if e.zip64Extra {
			ix.ZIP64 = true
		}
	}

	// Every name above was decoded on its own, which assumes CP949 for the
	// flagless ones. That assumption is settled here, once, against the whole
	// directory — including on the error paths below, because a book with a
	// partially readable directory still shows the pages that did parse.
	resolveArchiveNames(ix)

	// The end record's entry count is 16-bit in a non-ZIP64 archive, so a real
	// archive with more than 65 535 entries wraps it. archive/zip glosses over
	// this by reading until a record fails to parse and only complaining if
	// the count disagrees modulo 65 536; we do the same, because the
	// differential oracle must agree on the error verdict for every archive
	// (impl-plan WP-04 acceptance 3).
	if uint16(parsed) != uint16(end.records) {
		return ix, fmt.Errorf("zip: %w at entry %d of %d", parseErr, parsed+1, end.records)
	}
	if unusable > 0 {
		// This is a deliberate divergence from archive/zip, which accepts such
		// a record and only fails later when something tries to read it. A book
		// whose directory contains unreachable entries is broken, and saying so
		// at scan time is what FR-IDX-010 asks for.
		return ix, fmt.Errorf("zip: %w (%d of %d entries point outside the archive)",
			ErrBadCentralHeader, unusable, parsed)
	}
	return ix, nil
}

// resolveArchiveNames re-reads the legacy-encoded names of an archive that
// CP949 could not read completely (FR-IDX-008, extended for Shift_JIS).
//
// The trigger is one EncUnknown name, which is what makes this free for the
// 11,192 archives of 11,196 that do not need it: they leave the loop below on
// the first iteration and nothing is decoded twice. Only when a name failed do
// we ask kenc whether another encoding reads the whole set, and only then are
// the CP949 readings — which may be *wrong* rather than merely absent, see
// kenc.ArchiveFallback — replaced.
//
// Entry.Dir is deliberately not recomputed. It was derived from the external
// attributes and a trailing slash, and a re-decode cannot move that slash:
// neither CP949 nor Shift_JIS ever emits 0x2F as a trailing byte of a
// double-byte character, so the 0x2F positions in RawName are the same
// characters before and after.
func resolveArchiveNames(ix *archive.Index) {
	var needs bool
	for i := range ix.Entries {
		if ix.Entries[i].NameEncoding == kenc.EncUnknown {
			needs = true
			break
		}
	}
	if !needs {
		return
	}

	// Only the names that were read in a legacy encoding, or could not be read
	// at all, are evidence. A name the producer flagged as UTF-8, or that the
	// probe proved is UTF-8, says nothing about how the rest were written.
	raws := make([][]byte, 0, len(ix.Entries))
	for i := range ix.Entries {
		switch ix.Entries[i].NameEncoding {
		case kenc.EncCP949, kenc.EncUnknown:
			raws = append(raws, ix.Entries[i].RawName)
		}
	}
	legacy := kenc.ArchiveFallback(raws)
	if legacy == "" {
		return
	}
	for i := range ix.Entries {
		e := &ix.Entries[i]
		switch e.NameEncoding {
		case kenc.EncCP949, kenc.EncUnknown:
			e.Name, e.NameEncoding = kenc.DecodeEntryNameAs(e.RawName, false, legacy)
		}
	}
}

// usableEntry rejects geometry that can never be served: an offset before the
// start of the archive or past its end, a negative size (a uint64 that
// overflowed int64), or a compressed size larger than the whole container.
//
// The uncompressed size is deliberately *not* bounded by the archive size —
// a legitimate stored-then-compressed entry can expand well past it, and the
// unresolved-ZIP64-sentinel tolerance archive/zip keeps for 42.zip leaves it
// at 0xffffffff.
func usableEntry(e archive.Entry, size int64) bool {
	switch {
	case e.LocalHdrOff < 0 || e.LocalHdrOff >= size:
		return false
	case e.CompSize < 0 || e.CompSize > size:
		return false
	case e.Size < 0:
		return false
	default:
		return true
	}
}

// entry adds the parse-local flag "this record carried a 0x0001 extra" to an
// archive.Entry, which the caller folds into Index.ZIP64.
type entry struct {
	archive.Entry
	zip64Extra bool
}

// parseCentralHeader decodes one central-directory record from the front of
// buf and returns its total length. base is added to the recorded local-header
// offset (self-extracting archives, see baseOffset).
func parseCentralHeader(buf []byte, base int64) (entry, int, error) {
	var e entry
	if len(buf) < centralHeaderLen {
		return e, 0, ErrTruncatedCD
	}
	if binary.LittleEndian.Uint32(buf) != sigCentralDir {
		// Normal termination: this is where the end record starts. It is also
		// what a corrupted record looks like — the count check upstream is
		// what tells the two apart.
		return e, 0, ErrBadCentralHeader
	}

	flags := binary.LittleEndian.Uint16(buf[8:])
	method := binary.LittleEndian.Uint16(buf[10:])
	modTime := binary.LittleEndian.Uint16(buf[12:])
	modDate := binary.LittleEndian.Uint16(buf[14:])
	crc := binary.LittleEndian.Uint32(buf[16:])
	compSize := binary.LittleEndian.Uint32(buf[20:])
	uncompSize := binary.LittleEndian.Uint32(buf[24:])
	nameLen := int(binary.LittleEndian.Uint16(buf[28:]))
	extraLen := int(binary.LittleEndian.Uint16(buf[30:]))
	commentLen := int(binary.LittleEndian.Uint16(buf[32:]))
	externalAttrs := binary.LittleEndian.Uint32(buf[38:])
	localOff := binary.LittleEndian.Uint32(buf[42:])

	total := centralHeaderLen + nameLen + extraLen + commentLen
	if len(buf) < total {
		return e, 0, ErrTruncatedCD
	}
	rawName := buf[centralHeaderLen : centralHeaderLen+nameLen]
	extra := buf[centralHeaderLen+nameLen : centralHeaderLen+nameLen+extraLen]

	// FR-IDX-008 / AC-002: the raw bytes and the flag go to kenc, which owns
	// the UTF-8-probe-then-CP949 rule. Nothing here guesses at an encoding.
	name, enc := kenc.DecodeEntryName(rawName, flags&archive.FlagUTF8 != 0)

	// FR-IDX-009. The 0x0001 members exist only for the 32-bit slots that hold
	// the sentinel, in a fixed order; zip64.go does that and nothing else.
	sizes := zip64Sizes{
		uncompressed: uint64(uncompSize),
		compressed:   uint64(compSize),
		localOffset:  uint64(localOff),
		needUncomp:   uncompSize == zip64Marker,
		needComp:     compSize == zip64Marker,
		needOffset:   localOff == zip64Marker,
	}
	saw64, err := parseZIP64Extra(extra, &sizes)
	if err != nil {
		return e, 0, err
	}
	// A sentinel with no extra field to resolve it is unrecoverable for the
	// two fields we must have exactly right. archive/zip tolerates it for the
	// uncompressed size alone (the 42.zip case) and so do we, so that the
	// error verdicts stay identical.
	if sizes.needComp || sizes.needOffset {
		return e, 0, ErrBadCentralHeader
	}

	e.Entry = archive.Entry{
		Name:         name,
		RawName:      append([]byte(nil), rawName...),
		NameEncoding: enc,
		Flags:        flags,
		Method:       method,
		CRC32:        crc,
		CompSize:     int64(sizes.compressed),
		Size:         int64(sizes.uncompressed),
		LocalHdrOff:  int64(sizes.localOffset) + base,
		Modified:     msdosTime(modDate, modTime),
		Dir:          isDirEntry(name, externalAttrs),
		Encrypted:    flags&archive.FlagEncrypted != 0,
	}
	e.zip64Extra = saw64
	return e, total, nil
}

// centralRecordLen returns the total on-disk length of the central-directory
// record at the front of buf, or 0 when buf does not begin with one. It reads
// only the fixed part, so a caller can size a record before fetching it.
func centralRecordLen(buf []byte) int {
	if len(buf) < centralHeaderLen || binary.LittleEndian.Uint32(buf) != sigCentralDir {
		return 0
	}
	return centralHeaderLen +
		int(binary.LittleEndian.Uint16(buf[28:])) + // file name length
		int(binary.LittleEndian.Uint16(buf[30:])) + // extra field length
		int(binary.LittleEndian.Uint16(buf[32:])) // file comment length
}

// isDirEntry implements the directory half of FR-IDX-006: a trailing slash, the
// MS-DOS directory attribute, or the unix S_IFDIR bit. Any one is enough —
// writers disagree about which they set.
func isDirEntry(name string, externalAttrs uint32) bool {
	const (
		msdosDir  = 0x10
		unixIFDir = 0o040000
	)
	if name != "" && name[len(name)-1] == '/' {
		return true
	}
	if externalAttrs&msdosDir != 0 {
		return true
	}
	return (externalAttrs>>16)&0o170000 == unixIFDir
}

// msdosTime converts the MS-DOS date/time pair to UTC. The format has 2-second
// resolution and a 1980 epoch.
func msdosTime(dosDate, dosTime uint16) time.Time {
	return time.Date(
		int(dosDate>>9)+1980,
		time.Month(dosDate>>5&0xf),
		int(dosDate&0x1f),
		int(dosTime>>11),
		int(dosTime>>5&0x3f),
		int(dosTime&0x1f*2),
		0, time.UTC,
	)
}

// endRecord is the end-of-central-directory record, already merged with the
// ZIP64 end record when one is present.
type endRecord struct {
	records   uint64
	dirSize   uint64
	dirOffset uint64
	comment   string
	zip64     bool
	// endOffset is the absolute position of the record the directory is
	// measured back from: the ZIP64 end record when there is one, otherwise
	// the 22-byte legacy one.
	endOffset int64
}

// readEnd locates and parses the end record with the two-step tail scan of
// arch §4.3, returning the tail buffer so that the caller can serve the
// central directory out of it without a second read.
func readEnd(r io.ReaderAt, size int64) (endRecord, *tail, error) {
	var end endRecord

	tl := &tail{}
	for i, want := range [...]int64{tailSmall, tailLarge} {
		n := min(want, size)
		if err := tl.fill(r, size-n, n); err != nil {
			return end, nil, fmt.Errorf("zip: reading archive tail: %w", err)
		}
		if p := findEOCD(tl.buf); p >= 0 {
			end.endOffset = tl.off + int64(p)
			if err := parseEOCD(tl.buf[p:], &end); err != nil {
				return end, nil, err
			}
			break
		}
		if i == 1 || n == size {
			return end, nil, fmt.Errorf("zip: %w", ErrNoEOCD)
		}
	}

	// FR-IDX-009. arch §4.3 step 2: any of the three sentinels means the real
	// values live in a ZIP64 end record reachable through the 20-byte locator
	// that immediately precedes the legacy record.
	if end.records == 0xffff || end.dirSize == uint64(zip64Marker) || end.dirOffset == uint64(zip64Marker) {
		off, err := findZIP64End(r, tl, end.endOffset)
		if err != nil {
			return end, nil, err
		}
		if off >= 0 {
			if err := readZIP64End(r, tl, off, &end); err != nil {
				return end, nil, err
			}
			end.endOffset = off
			end.zip64 = true
		}
	}
	return end, tl, nil
}

// findEOCD scans a tail buffer backwards for the end record. The comment-length
// consistency check is what stops a stray signature inside compressed data from
// being mistaken for the real record.
func findEOCD(b []byte) int {
	for i := len(b) - eocdLen; i >= 0; i-- {
		if b[i] != 'P' || b[i+1] != 'K' || b[i+2] != 0x05 || b[i+3] != 0x06 {
			continue
		}
		commentLen := int(binary.LittleEndian.Uint16(b[i+eocdLen-2:]))
		if i+eocdLen+commentLen > len(b) {
			// The declared comment runs past EOF. Info-ZIP ignores such a
			// record rather than treating it as a hard error, and archive/zip
			// follows suit by abandoning the scan; we match that so the two
			// implementations agree on the verdict.
			return -1
		}
		return i
	}
	return -1
}

func parseEOCD(b []byte, end *endRecord) error {
	if len(b) < eocdLen {
		return fmt.Errorf("zip: %w", ErrNoEOCD)
	}
	end.records = uint64(binary.LittleEndian.Uint16(b[10:]))
	end.dirSize = uint64(binary.LittleEndian.Uint32(b[12:]))
	end.dirOffset = uint64(binary.LittleEndian.Uint32(b[16:]))
	commentLen := int(binary.LittleEndian.Uint16(b[20:]))
	if eocdLen+commentLen > len(b) {
		return fmt.Errorf("zip: %w (comment length %d)", ErrNoEOCD, commentLen)
	}
	end.comment = string(b[eocdLen : eocdLen+commentLen])
	return nil
}

// baseOffset recovers the offset the archive proper starts at.
//
// Self-extracting archives prepend an executable stub and number their offsets
// from the start of the ZIP payload, so every recorded offset is short by the
// stub's length. The end record gives it away: the directory must physically
// end where the end record begins, so base = endOffset - dirSize - dirOffset.
// archive/zip does exactly this, including the fallback below, and the
// differential oracle requires us to match it.
func baseOffset(r io.ReaderAt, tl *tail, end endRecord, size int64) (int64, error) {
	const maxInt64 = uint64(1<<63 - 1)
	if end.dirSize > maxInt64 || end.dirOffset > maxInt64 {
		return 0, fmt.Errorf("zip: %w (directory size %d, offset %d)", ErrBadCentralHeader, end.dirSize, end.dirOffset)
	}
	base := end.endOffset - int64(end.dirSize) - int64(end.dirOffset)
	if o := base + int64(end.dirOffset); o < 0 || o >= size {
		return 0, fmt.Errorf("zip: %w (directory at %d, archive is %d bytes)", ErrBadCentralHeader, o, size)
	}
	// Some writers record a directory offset that is simply wrong, which makes
	// the arithmetic above produce a non-zero base for an ordinary archive. If
	// a valid record is sitting at the uncorrected offset, believe the offset.
	//
	// The probe must cover a *whole* record. parseCentralHeader reports a record
	// whose name, extra field or comment runs past the end of the buffer as
	// truncated, so probing with only the 46-byte fixed part would fail for
	// every record with a non-empty name and this recovery would never fire —
	// while archive/zip, which reads the record through a section reader that
	// runs to EOF, recovers and opens the archive. That is exactly the verdict
	// divergence decision E-2 and impl-plan §0.1 C-6 forbid.
	//
	// It is read in two steps so the common case stays cheap: a genuine
	// self-extracting archive has payload bytes at the uncorrected offset, the
	// signature check fails on the first 46 bytes, and no second read happens.
	if base > 0 {
		off := int64(end.dirOffset)
		if head, err := readSpan(r, tl, off, min(centralHeaderLen, size-off)); err == nil {
			if n := centralRecordLen(head); n > 0 {
				if probe, err := readSpan(r, tl, off, min(int64(n), size-off)); err == nil {
					if _, _, err := parseCentralHeader(probe, 0); err == nil {
						base = 0
					}
				}
			}
		}
	}
	return base, nil
}

// tail caches the bytes at the end of an archive.
//
// It is what keeps the whole index of a small archive at a single ReadAt: the
// end record has to be read anyway, and for most archives the central
// directory is inside the same 1 KiB. Larger archives cost the documented two
// calls (impl-plan WP-04 acceptance 1).
type tail struct {
	buf []byte
	off int64 // absolute offset of buf[0]
}

func (t *tail) fill(r io.ReaderAt, off, n int64) error {
	t.buf = make([]byte, n)
	t.off = off
	if _, err := r.ReadAt(t.buf, off); err != nil && err != io.EOF {
		t.buf, t.off = nil, 0
		return err
	}
	return nil
}

// slice returns the cached bytes for [off, off+n) when they are all present.
func (t *tail) slice(off, n int64) ([]byte, bool) {
	if t == nil || t.buf == nil || off < t.off || n < 0 {
		return nil, false
	}
	lo := off - t.off
	if lo+n > int64(len(t.buf)) {
		return nil, false
	}
	return t.buf[lo : lo+n], true
}

// readSpan returns [off, off+n), preferring the cached tail.
func readSpan(r io.ReaderAt, tl *tail, off, n int64) ([]byte, error) {
	if n < 0 {
		return nil, io.ErrUnexpectedEOF
	}
	if b, ok := tl.slice(off, n); ok {
		return b, nil
	}
	buf := make([]byte, n)
	read, err := r.ReadAt(buf, off)
	if err != nil && err != io.EOF {
		return nil, err
	}
	// A short read at EOF leaves the tail of buf zeroed; hand back only what
	// actually came off the disk so the parser sees "truncated", not "zeros".
	return buf[:read], nil
}
