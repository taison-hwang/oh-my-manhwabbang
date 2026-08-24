// Package hv3 reads HoneyView HV3 containers the way zipidx reads ZIPs and
// rar4 reads RARs: the directory only at index time, one seek to a recorded
// offset at serve time.
//
// # Why this exists, when decision D-72 said it could not
//
// D-72 recorded that `펌프킨 시저스 04` — the collection's one HV3 — was
// *encrypted*, and that "nothing recovers that without the key". Two of the
// three measurements behind that sentence were misread:
//
//   - "the LIST chunk is empty" came from reading the four bytes after the
//     `LIST` tag as its length. They are zero. The length is the **64-bit**
//     field after them, and it is 18,512 bytes holding 104 complete file
//     records with names, sizes, offsets and CRC-32s.
//   - "7.9972 bits of entropy per byte" is true and means nothing here. The
//     payload is 104 JPEGs, which are already at 8 bits/byte before anything
//     is done to them; the `ENCR` chunk's value 2 selects a **byte-position
//     XOR mask**, `plain[i] = stored[i] ^ (i & 0xFF)`, restarted at every
//     file. That is obfuscation with no key, not encryption.
//
// The correction is measured, not argued: all 104 entries of the real file
// decode to bytes whose CRC-32 matches the CRC-32 the container itself
// recorded, and all 104 begin `FF D8 FF E0`. D-72's *rule* is untouched and
// still right — a book that is one unopenable format names the format instead
// of claiming to be empty. What changed is that HV3 is no longer one of those
// formats. See ruling E-51.
//
// # The layout, as measured
//
// Chunks are `TAG` + a 32-bit length. Six tags — `HV30`, `HEAD`, `LIST`,
// `FINF`, `BODY`, `FILE` — carry eight further bytes before their payload, so
// their headers are 16 bytes wide. The directory sits at the FRONT of the
// file, which is the one structural difference from ZIP that matters here:
//
//	HV30 ┐ VERS FSIZ
//	HEAD ┐ GUID UUID FTIM TITL MAKR ENCR      ← ENCR selects the mask
//	LIST ┐ FINF ┐ NAME POS4|POS8 SIZE CRC3 MTIM   ← one record per file
//	     │ FINF ┐ …
//	BODY ┐ FILE ┐ <payload> FILE ┐ <payload> …
//
// A `FILE` block is 16 bytes — the tag, eight bytes that are zero in every one
// of the 104 measured, and the payload length as a 32-bit value — followed by
// the payload itself. `POS4`/`POS8` is that block's absolute offset, which is
// exactly what [archive.Entry.LocalHdrOff] means for the other two formats, so
// FR-SRV-002 needs no new column and no new idea.
//
// Nothing is compressed. Every entry is stored, which makes an HV3 page the
// same one-seek read a stored ZIP entry is — and, when the mask is on, one XOR
// pass over the bytes as they stream.
//
// # The two rules from [archive] hold here unchanged
//
//   - FR-IDX-002 — indexing reads the header window and the LIST chunk. No
//     entry payload is touched while building an Index.
//   - FR-SRV-002 / NFR-PRF-006 — everything is io.ReaderAt. There is no shared
//     seek cursor, so concurrent reads of one container need no lock, and the
//     handle still comes from the pool (FR-SRV-004) rather than from a path,
//     which is what keeps the os.Root traversal guard of arch §8.1 in force.
package hv3

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf16"

	"shelf/internal/archive"
	"shelf/internal/kenc"
)

// signature is the four bytes an HV3 container starts with.
const signature = "HV30"

// Header widths, all measured against the real container.
const (
	// chunkHeaderLen is a leaf chunk: `TAG` plus a 32-bit payload length.
	chunkHeaderLen = 8
	// recordHeaderLen is the wide form the container tags use: the leaf header
	// plus eight bytes that are zero in every `FINF` measured and carry the
	// 64-bit length in `LIST` and `BODY`.
	recordHeaderLen = 16
	// fileHeaderLen is the block that precedes one entry's payload. Its last
	// four bytes are the payload length, which is the only field in it this
	// package reads.
	fileHeaderLen = 16
	// fileSizeOff is where that length sits within the block.
	fileSizeOff = 12
)

// headerWindow is how much of the front of a container is read to find the
// LIST chunk and the ENCR mode.
//
// The real file's header is 224 bytes: `LIST` begins at 0xE0 and everything
// before it — magic, version, declared size, and the whole HEAD block with its
// GUID, UUID, timestamp, title, maker and ENCR — fits in that. 64 KiB is 290
// times the measurement, and a container whose header does not fit says so by
// name ([ErrNoList]) rather than being searched for further. Inventing a
// bigger window for a file that has never been seen is how the wrong rule gets
// shipped.
//
// It is also what one page costs: [Reader.OpenEntry] re-reads this window to
// learn the mask mode from the container rather than trusting the index, which
// is one pread against the several hundred kilobytes of the page itself.
const headerWindow = 64 << 10

// maxEntries bounds a walk over untrusted bytes, matching rar4's guard. The
// real HV3 holds 104; the largest book this product indexes holds 7,480 pages.
const maxEntries = 200_000

// maxListSize caps the directory allocation. A LIST chunk must also fit inside
// the file, which is the real bound; this is the guard for the case where the
// file is itself enormous.
const maxListSize = 64 << 20

// ENCR modes, as the container spells them.
const (
	// modePlain — the payload is stored as-is. `ENCR` absent means this.
	modePlain uint32 = 0
	// modeMasked — the payload is masked with its own byte position, restarted
	// at each entry. See [unmask].
	modeMasked uint32 = 2
)

// methodBase is where this format's [archive.Entry.Method] numbering starts.
//
// Method is per-format by contract — ZIP uses 0 and 8, RAR uses 0x30–0x35 —
// and nothing downstream compares two formats' values. The base is still
// chosen clear of both, because a value that collides is a value that reads as
// meaningful in a log line about the wrong format.
const methodBase uint16 = 0x4800 // 'H' << 8

// Method values recorded in pages.method. They are diagnostic: the container's
// own ENCR chunk, not this column, decides how a page is decoded (see
// [Reader.OpenEntry]).
const (
	MethodPlain  = methodBase | uint16(modePlain)
	MethodMasked = methodBase | uint16(modeMasked)
)

// Reader is the HV3 implementation of [archive.Reader]. It holds no per-file
// state, so one value serves the whole process.
type Reader struct{}

// New returns an HV3 reader.
func New() *Reader { return &Reader{} }

// Format implements [archive.Reader].
func (*Reader) Format() string { return "hv3" }

// header is what the front of a container declares.
type header struct {
	listOff  int64 // offset of the LIST chunk's own tag
	listSize int64 // its payload length, from the 64-bit field
	mode     uint32
}

// ReadIndex reads the header window and the LIST chunk, and records one
// [archive.Entry] per FINF record. No payload is read (FR-IDX-002).
//
// A directory that goes bad partway returns the entries parsed before it
// together with the error, which is what FR-IDX-010 asks for and what lets a
// truncated download still open at the pages it does have.
func (*Reader) ReadIndex(ctx context.Context, r io.ReaderAt, size int64) (*archive.Index, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	win, err := readWindow(r, size)
	if err != nil {
		return nil, err
	}
	h, err := parseHeader(win, size)
	if err != nil {
		return nil, err
	}

	ix := &archive.Index{}
	// The mode is a property of the whole container and decides whether any
	// page of it can be served, so it is checked before a single entry is
	// recorded — the shape rar4 uses for a solid or password-protected
	// archive.
	if h.mode != modePlain && h.mode != modeMasked {
		return ix, unsupportedMode(h.mode)
	}

	meta := make([]byte, h.listSize)
	if _, err := r.ReadAt(meta, h.listOff+recordHeaderLen); err != nil {
		return ix, fmt.Errorf("hv3: reading LIST at 0x%X: %w", h.listOff, err)
	}
	entries, err := parseRecords(ctx, meta, h, size)
	ix.Entries = entries
	return ix, err
}

// readWindow reads the front of the container, short-read tolerant so a
// container smaller than the window is not an error by itself.
func readWindow(r io.ReaderAt, size int64) ([]byte, error) {
	if size < int64(len(signature)) {
		return nil, fmt.Errorf("hv3: %w (file is %d bytes)", ErrNoSignature, size)
	}
	n := int64(headerWindow)
	if size < n {
		n = size
	}
	win := make([]byte, n)
	got, err := r.ReadAt(win, 0)
	if got < len(signature) {
		return nil, fmt.Errorf("hv3: reading header: %w", err)
	}
	return win[:got], nil
}

// parseHeader locates the LIST chunk and the ENCR mode.
//
// # Why LIST is searched for rather than walked to
//
// The container tags carry two candidate lengths — a 32-bit one and a 64-bit
// one — and the real file uses a different one for each tag: `HV30` declares
// 24 (its VERS and FSIZ children) in the 32-bit field while its 64-bit field
// holds the whole rest of the file, and `LIST` declares 0 in the 32-bit field
// with its real 18,512 in the 64-bit one. A walk has to guess which field is
// authoritative per tag, and one sample is not enough evidence to convict a
// format of a convention.
//
// So LIST is found the way zipidx finds the end-of-central-directory record:
// by its signature within a bounded window, and then *validated* — the length
// must fit inside the file, and the bytes it points at must begin a `FINF`
// record. A candidate that fails is skipped and the search continues, which is
// what makes a stray `LIST\0\0\0\0` inside a title or a comment harmless.
//
// ENCR is then searched for only in the bytes *before* LIST. That bound is
// what keeps the search off the record data, where four arbitrary bytes could
// spell anything.
//
// size may be negative, meaning "not known here" — [Reader.OpenEntry] has no
// container size to check against and only needs the mode. The LIST-fits-in-
// the-file check is skipped in that case and nothing else changes.
func parseHeader(win []byte, size int64) (header, error) {
	var h header
	if len(win) < len(signature) || string(win[:len(signature)]) != signature {
		return h, notHV3(win)
	}

	listMarker := []byte("LIST\x00\x00\x00\x00")
	found := false
	// A candidate declaring zero bytes cannot be checked against a record, so
	// it is remembered rather than taken: a container with no files at all is
	// legitimate — it reports `empty`, which is what it is — but so is a stray
	// marker followed by eight zero bytes, and letting that one win would turn
	// a readable book into an empty one. A FINF-backed candidate anywhere in
	// the window beats it.
	var empty int64 = -1
	for at := len(signature); ; {
		rel := indexAt(win, listMarker, at)
		if rel < 0 {
			break
		}
		off := int64(rel)
		at = rel + 1
		if rel+recordHeaderLen > len(win) {
			continue
		}
		listSize := int64(binary.LittleEndian.Uint64(win[rel+chunkHeaderLen:]))
		if listSize < 0 || listSize > maxListSize {
			continue
		}
		if size >= 0 && off+recordHeaderLen+listSize > size {
			continue
		}
		if listSize == 0 {
			if empty < 0 {
				empty = off
			}
			continue
		}
		// A non-empty directory must begin with a record. When the window is
		// too short to see that far the check is deferred to parseRecords,
		// which makes the same assertion against the bytes it actually read.
		if rel+recordHeaderLen+4 <= len(win) &&
			string(win[rel+recordHeaderLen:rel+recordHeaderLen+4]) != "FINF" {
			continue
		}
		h.listOff, h.listSize, found = off, listSize, true
		break
	}
	if !found && empty >= 0 {
		h.listOff, h.listSize, found = empty, 0, true
	}

	// The mode is read whether or not LIST was found, because OpenEntry needs
	// it and a container whose directory this build cannot locate can still
	// have pages the index recorded from a healthier read.
	limit := len(win)
	if found && h.listOff < int64(limit) {
		limit = int(h.listOff)
	}
	if rel := indexAt(win[:limit], []byte("ENCR\x04\x00\x00\x00"), 0); rel >= 0 &&
		rel+chunkHeaderLen+4 <= limit {
		h.mode = binary.LittleEndian.Uint32(win[rel+chunkHeaderLen:])
	}

	if !found {
		return h, fmt.Errorf("hv3: %w in the first %d bytes", ErrNoList, len(win))
	}
	return h, nil
}

// indexAt is bytes.Index over b[from:], returning an index into b.
func indexAt(b, sep []byte, from int) int {
	if from >= len(b) {
		return -1
	}
	i := bytes.Index(b[from:], sep)
	if i < 0 {
		return -1
	}
	return from + i
}

// parseRecords turns the LIST payload into entries.
//
// Every field a page needs is required rather than defaulted: a record with no
// NAME cannot be displayed, one with no SIZE cannot be served a Content-Length,
// one with no POS4/POS8 cannot be found at all, and a record missing any of
// them is evidence the walk has left the structure rather than something to
// paper over.
func parseRecords(ctx context.Context, meta []byte, h header, size int64) ([]archive.Entry, error) {
	entries := make([]archive.Entry, 0, 64)
	for pos, n := 0, 0; pos < len(meta); n++ {
		if n%512 == 0 {
			if err := ctx.Err(); err != nil {
				return entries, err
			}
		}
		if n >= maxEntries {
			return entries, fmt.Errorf("hv3: %w: more than %d records", ErrBadRecord, maxEntries)
		}
		at := h.listOff + recordHeaderLen + int64(pos)
		if pos+recordHeaderLen > len(meta) || string(meta[pos:pos+4]) != "FINF" {
			return entries, fmt.Errorf("hv3: %w at 0x%X", ErrBadRecord, at)
		}
		recLen := int(binary.LittleEndian.Uint32(meta[pos+4:]))
		end := pos + recordHeaderLen + recLen
		if recLen < 0 || end > len(meta) {
			return entries, fmt.Errorf("hv3: %w at 0x%X: it declares %d bytes", ErrBadRecord, at, recLen)
		}

		f, err := parseFields(meta[pos+recordHeaderLen:end], at)
		if err != nil {
			return entries, err
		}
		e, err := f.entry(at, size, h.mode)
		if err != nil {
			return entries, err
		}
		entries = append(entries, e)
		pos = end
	}
	return entries, nil
}

// fields is one FINF record's contents, undecoded.
type fields struct {
	name  []byte
	size  int64
	crc   uint32
	off   int64
	mtime time.Time

	hasName, hasSize, hasCRC, hasOff bool
}

func parseFields(rec []byte, at int64) (fields, error) {
	var f fields
	for p := 0; p < len(rec); {
		if p+chunkHeaderLen > len(rec) {
			return f, fmt.Errorf("hv3: %w at 0x%X: a field header runs past the record", ErrBadRecord, at)
		}
		tag := string(rec[p : p+4])
		ln := int(binary.LittleEndian.Uint32(rec[p+4:]))
		body := p + chunkHeaderLen
		next := body + ln
		if ln < 0 || next > len(rec) {
			return f, fmt.Errorf("hv3: %w at 0x%X: field %q declares %d bytes", ErrBadRecord, at, tag, ln)
		}
		val := rec[body:next]

		switch tag {
		case "NAME":
			f.name, f.hasName = val, true
		case "SIZE":
			if ln < 4 {
				return f, fmt.Errorf("hv3: %w at 0x%X: SIZE is %d bytes", ErrBadRecord, at, ln)
			}
			f.size, f.hasSize = int64(binary.LittleEndian.Uint32(val)), true
		case "CRC3":
			if ln < 4 {
				return f, fmt.Errorf("hv3: %w at 0x%X: CRC3 is %d bytes", ErrBadRecord, at, ln)
			}
			f.crc, f.hasCRC = binary.LittleEndian.Uint32(val), true
		case "POS4":
			if ln < 4 {
				return f, fmt.Errorf("hv3: %w at 0x%X: POS4 is %d bytes", ErrBadRecord, at, ln)
			}
			f.off, f.hasOff = int64(binary.LittleEndian.Uint32(val)), true
		case "POS8":
			if ln < 8 {
				return f, fmt.Errorf("hv3: %w at 0x%X: POS8 is %d bytes", ErrBadRecord, at, ln)
			}
			// A container past 8 EiB is not a thing; the sign check below is
			// the guard against a hostile value, not against a real file.
			f.off, f.hasOff = int64(binary.LittleEndian.Uint64(val)), true
		case "MTIM":
			if ln >= 8 {
				f.mtime = fileTime(binary.LittleEndian.Uint64(val))
			}
		}
		p = next
	}
	return f, nil
}

// entry turns one record into the [archive.Entry] the rest of the product
// speaks, and refuses one whose offset and size do not describe bytes that are
// actually in the file.
func (f fields) entry(at, size int64, mode uint32) (archive.Entry, error) {
	switch {
	case !f.hasName:
		return archive.Entry{}, fmt.Errorf("hv3: %w at 0x%X: no NAME", ErrBadRecord, at)
	case !f.hasSize:
		return archive.Entry{}, fmt.Errorf("hv3: %w at 0x%X: no SIZE", ErrBadRecord, at)
	case !f.hasOff:
		return archive.Entry{}, fmt.Errorf("hv3: %w at 0x%X: no POS4 or POS8", ErrBadRecord, at)
	case !f.hasCRC:
		return archive.Entry{}, fmt.Errorf("hv3: %w at 0x%X: no CRC3", ErrBadRecord, at)
	case f.off < 0 || f.size < 0:
		return archive.Entry{}, fmt.Errorf("hv3: %w at 0x%X: offset %d, size %d",
			ErrBadRecord, at, f.off, f.size)
	case size >= 0 && f.off+fileHeaderLen+f.size > size:
		return archive.Entry{}, fmt.Errorf("hv3: %w: the record at 0x%X points at 0x%X+%d, past the %d-byte file",
			ErrTruncated, at, f.off, f.size, size)
	}

	name := decodeName(f.name)
	return archive.Entry{
		Name: name,
		// The bytes as stored, which for this format are UTF-16LE. Nothing
		// re-decodes them — HV3 names are Unicode by construction, so the
		// per-archive legacy code-page fallback zipidx and rar4 need has
		// nothing to decide here — but [archive.Entry] documents RawName as
		// the evidence for NameEncoding and a re-encoded copy would not be.
		RawName:      f.name,
		NameEncoding: kenc.EncUTF8,
		Method:       methodBase | uint16(mode),
		CRC32:        f.crc,
		CompSize:     f.size, // nothing in an HV3 is compressed
		Size:         f.size,
		LocalHdrOff:  f.off,
		Modified:     f.mtime,
		Dir:          name == "" || strings.HasSuffix(name, "/"),
	}, nil
}

// decodeName turns a UTF-16LE NAME field into a display name.
//
// Trailing NULs are stripped — the real container pads every name with one —
// and `\` becomes `/` for the same reason rar4 does it: an entry path is
// matched against chapter prefixes and exclusion rules that are written in
// slashes, and a DOS-era separator that survived would quietly opt one book
// out of both.
func decodeName(raw []byte) string {
	u := make([]uint16, 0, len(raw)/2)
	for i := 0; i+1 < len(raw); i += 2 {
		u = append(u, binary.LittleEndian.Uint16(raw[i:]))
	}
	s := strings.TrimRight(string(utf16.Decode(u)), "\x00")
	if strings.ContainsRune(s, '\\') {
		s = strings.ReplaceAll(s, "\\", "/")
	}
	return s
}

// fileTimeEpoch is the number of seconds between 1601-01-01 and the Unix
// epoch. MTIM is a Windows FILETIME: 100-nanosecond ticks since 1601.
const fileTimeEpoch = 11644473600

func fileTime(ticks uint64) time.Time {
	if ticks == 0 {
		return time.Time{}
	}
	sec := int64(ticks/10_000_000) - fileTimeEpoch
	nsec := int64(ticks%10_000_000) * 100
	return time.Unix(sec, nsec).UTC()
}

// OpenEntry streams one entry straight out of the container.
//
// The read is the same one a stored ZIP entry gets — an [io.SectionReader]
// over the payload, which implements io.ReadSeeker so the HTTP layer hands it
// to http.ServeContent and Range works (arch §5.3) — with one XOR pass layered
// on when the container says its payload is masked.
//
// # Why the container is re-read for the mode
//
// The mask is a property of the whole file, recorded once in its HEAD block,
// and the FILE block in front of an entry carries no trace of it. So unlike
// rar4 — which reads the packing method out of the block header it is already
// seeking past — this reader has nothing local to consult, and the choice is
// between trusting pages.method and re-reading the container's header.
//
// It re-reads. If the index says plain and the file on disk is masked, the
// trusted-index version hands a client 400 KB of XORed noise with its
// Content-Type set to image/jpeg and no error anywhere in the process. One
// pread of the front of the file removes that case, and it is small against
// the page it precedes.
//
// The header is read BEFORE the entry, and that ordering is load-bearing for a
// nested book: internal/archive/nested serves a deflated inner container by
// inflating forward from its start, so a backward seek costs a restart. Header
// first, then payload, means offsets only ever increase.
func (*Reader) OpenEntry(ctx context.Context, r io.ReaderAt, ref archive.EntryRef) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if ref.LocalHdrOff < 0 || ref.Size < 0 {
		return nil, fmt.Errorf("hv3: %w (offset %d, size %d)", ErrBadFileBlock, ref.LocalHdrOff, ref.Size)
	}

	win := make([]byte, headerWindow)
	got, err := r.ReadAt(win, 0)
	if got < len(signature) {
		return nil, fmt.Errorf("hv3: reading header: %w", err)
	}
	// ErrNoList is not fatal here: the directory this entry came from was read
	// once already, and what OpenEntry needs from the header is the mode.
	h, err := parseHeader(win[:got], -1)
	if err != nil && !isNoList(err) {
		return nil, err
	}

	var hdr [fileHeaderLen]byte
	if _, err := r.ReadAt(hdr[:], ref.LocalHdrOff); err != nil {
		return nil, fmt.Errorf("hv3: reading FILE block at 0x%X: %w", ref.LocalHdrOff, err)
	}
	if string(hdr[:4]) != "FILE" {
		return nil, fmt.Errorf("hv3: %w at 0x%X: no FILE block there", ErrBadFileBlock, ref.LocalHdrOff)
	}
	// The block's own length against the one the index recorded. A container
	// that was repacked under the same (size, mtime) the pool checks is the
	// case this catches, and serving it would mean streaming the neighbouring
	// entry's bytes under this page's name.
	if stored := int64(binary.LittleEndian.Uint32(hdr[fileSizeOff:])); stored != ref.Size {
		return nil, fmt.Errorf("hv3: %w at 0x%X: the block holds %d bytes, the index recorded %d",
			ErrBadFileBlock, ref.LocalHdrOff, stored, ref.Size)
	}

	sec := io.NewSectionReader(r, ref.LocalHdrOff+fileHeaderLen, ref.Size)
	switch h.mode {
	case modePlain:
		// FR-SRV-003: no transformation at all, and the bytes stay seekable.
		return &sectionReadCloser{SectionReader: sec}, nil
	case modeMasked:
		return &maskedReader{sec: sec}, nil
	default:
		return nil, unsupportedMode(h.mode)
	}
}

var _ archive.Reader = (*Reader)(nil)
