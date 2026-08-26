package zipidx

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"shelf/internal/archive"
	"shelf/internal/kenc"
)

// Rebuilding an index from local file headers, for a container whose end
// record is gone (FR-IDX-010).
//
// # Why this exists
//
// A ZIP is read back-to-front: the end record points at the central directory,
// and the directory points at each entry. Both live at the *end* of the file,
// so the two ways a download dies — stopping early, and being overwritten with
// zeroes from some offset on — destroy the map while leaving the territory
// intact. Every entry is also preceded by its own 30-byte local header holding
// the same geometry the directory would have given us, and those are spread
// through the file rather than pooled at the end.
//
// Measured on this collection's nine damaged archives: 733 of 740 images are
// byte-for-byte intact and CRC-verifiable behind an unreadable directory. Eight
// of the nine open completely from local headers alone. Returning nil for all
// of them is throwing away readable books to honour a missing index.
//
// # What it is not
//
// This is not a repair, and it does not make the book healthy. The recovered
// index comes back *with* an error, so the volume keeps its damaged badge and
// its scan_log row — arch §4.11's verdict is unchanged, only the page list
// stops being empty. FR-IDX-010 asks for isolation, not deletion, and a book
// the reader can open is the whole point of the distinction.
//
// It also never decompresses anything (FR-IDX-002): a local header carries the
// compressed size, so the walk seeks over each payload without reading it. The
// entry data this recovers is therefore *claimed* to be intact, not proven —
// only serving the page checks the CRC, exactly as for an undamaged book.
const (
	// salvageChunk is the read granularity of both the chain walk and the
	// resynchronising scan. It has to exceed the largest single header —
	// 30 bytes plus a 16-bit name length plus a 16-bit extra length, so
	// 131 100 — or a header straddling two windows could never be read whole.
	salvageChunk = 256 << 10

	// maxSalvageNameLen bounds the name length a local header may claim before
	// we stop believing it is a header at all. The longest entry name in the
	// reference collection is 187 bytes; four random bytes matching the local
	// signature inside compressed data routinely claim tens of thousands.
	maxSalvageNameLen = 512

	// maxSalvageEntries bounds the walk the way maxCentralDirBytes bounds a
	// directory read. The largest book in the collection has 1 540 pages.
	maxSalvageEntries = 1 << 20
)

// salvage is the fallback [ReadCentralDirectory] takes when the end record
// cannot be read. cause is the structural error that sent us here; it is
// preserved in the returned error so the operator still learns what was
// actually wrong with the container.
//
// Returning (nil, cause) when nothing is recovered keeps a non-ZIP file, and
// the 0-byte one in the collection, behaving exactly as they did before.
func salvage(ctx context.Context, r io.ReaderAt, size int64, cause error) (*archive.Index, error) {
	ix := salvageLocalHeaders(ctx, r, size)
	if ix == nil || len(ix.Entries) == 0 {
		return nil, cause
	}
	return ix, fmt.Errorf("zip: %w (%v; %d entries recovered)",
		ErrSalvagedFromLocalHeaders, cause, len(ix.Entries))
}

// salvageLocalHeaders walks the archive front-to-back and returns what it could
// read, or nil if that is nothing.
//
// The walk is a chain first and a search second, and it needs to be both. A
// pure signature scan mistakes the four bytes `PK\x03\x04` occurring inside
// compressed data for an entry — they do occur, and a phantom entry serves
// garbage. A pure chain stops at the first damaged payload and abandons every
// intact entry after it, which on `유레카26.zip` is 3 of 91 and on
// `최종병기그녀 06권.zip` is 16 of 147. So: follow the chain while it holds,
// and on a break scan forward for the next header that parses.
func salvageLocalHeaders(ctx context.Context, r io.ReaderAt, size int64) *archive.Index {
	if size < localHeaderLen {
		return nil
	}
	sr := &salvageReader{r: r, size: size}
	ix := &archive.Index{}

	// lastData is where the most recently trusted header's payload begins, and
	// searched is how far the resynchronising scan has already looked.
	//
	// The pair is what makes a break recoverable. When the chain lands
	// somewhere that will not parse, the field that put it there — the last
	// header's compressed size — is the thing that is wrong, so the landing
	// point is not evidence of anything and searching forward *from it* skips
	// however much it overshot. On a 4-entry fixture with one corrupted size
	// field that is one whole entry lost. So the search restarts inside the
	// suspect payload instead, and `searched` keeps it honest: it only ever
	// moves forward, so no byte is scanned twice and the walk stays linear.
	var lastData, searched int64

	for off := int64(0); off < size && len(ix.Entries) < maxSalvageEntries; {
		if ctx.Err() != nil {
			break
		}
		buf, err := sr.window(off)
		if err != nil {
			break
		}
		e, dataStart, next, verdict := parseLocalHeader(buf, off, size)
		switch verdict {
		case localOK:
			ix.Entries = append(ix.Entries, e)
			lastData, off = dataStart, next
			continue
		case localStop:
			// A payload that runs past EOF, or the start of the central
			// directory: either way there is no further entry to find, and
			// scanning the rest would only turn compressed noise into phantoms.
			off = size
			continue
		case localSkip:
			// Parsed, but unservable — a data descriptor holds its sizes after
			// the payload, so this entry's length is unknown without inflating
			// it, and FR-IDX-002 forbids that here.
			lastData = dataStart
		}
		// localBad and localSkip both search, and both search from inside the
		// last payload rather than from wherever the arithmetic landed.
		from := max(lastData+1, searched)
		n, found := sr.findLocalHeader(ctx, from)
		if !found {
			break
		}
		searched, off = n+1, n
	}

	if len(ix.Entries) == 0 {
		return nil
	}
	// The names were decoded one at a time, each assuming CP949 where no UTF-8
	// flag was set. Settle that against the whole set, exactly as a directory
	// read does — a salvaged book must not get worse names than a healthy one.
	resolveArchiveNames(ix)
	return ix
}

// The verdicts parseLocalHeader can reach.
type localVerdict int

const (
	localBad  localVerdict = iota // not a header; search forward
	localOK                       // a usable entry, chain to next
	localSkip                     // a real header we cannot serve; search from next
	localStop                     // end of the recoverable region
)

// parseLocalHeader decodes the local file header at the front of buf, which
// begins at absolute offset off in an archive of the given size. It returns the
// entry, where its payload starts, where the next header should be, and how far
// the caller may trust any of that.
//
// It is deliberately stricter than [parseCentralHeader]. That one is reading a
// record the archive itself pointed at, so a wrong guess is impossible; this
// one is reading a position *we* guessed, and every field it checks is a way
// to reject compressed data that happens to start with the signature.
func parseLocalHeader(buf []byte, off, size int64) (archive.Entry, int64, int64, localVerdict) {
	var e archive.Entry
	if len(buf) < localHeaderLen {
		return e, 0, 0, localStop
	}
	switch binary.LittleEndian.Uint32(buf) {
	case sigLocalFile:
	case sigCentralDir:
		// The directory begins here. Whatever is wrong with it is upstream's
		// problem; there are no more local headers past this point.
		return e, 0, 0, localStop
	default:
		return e, 0, 0, localBad
	}

	flags := binary.LittleEndian.Uint16(buf[6:])
	method := binary.LittleEndian.Uint16(buf[8:])
	modTime := binary.LittleEndian.Uint16(buf[10:])
	modDate := binary.LittleEndian.Uint16(buf[12:])
	crc := binary.LittleEndian.Uint32(buf[14:])
	compSize := int64(binary.LittleEndian.Uint32(buf[18:]))
	uncompSize := int64(binary.LittleEndian.Uint32(buf[22:]))
	nameLen := int(binary.LittleEndian.Uint16(buf[26:]))
	extraLen := int(binary.LittleEndian.Uint16(buf[28:]))

	if nameLen == 0 || nameLen > maxSalvageNameLen {
		return e, 0, 0, localBad
	}
	if localHeaderLen+nameLen+extraLen > len(buf) {
		return e, 0, 0, localBad
	}
	rawName := buf[localHeaderLen : localHeaderLen+nameLen]
	if !plausibleEntryName(rawName) {
		return e, 0, 0, localBad
	}

	dataStart := off + int64(localHeaderLen+nameLen+extraLen)

	// Bit 3 puts the sizes *after* the payload, and a writer that sets it
	// commonly zeroes the header copies. The central directory is where the
	// real values live, and it is precisely what we do not have.
	if flags&archive.FlagDataDescriptor != 0 && compSize == 0 {
		return e, dataStart, dataStart, localSkip
	}
	if compSize < 0 || uncompSize < 0 {
		return e, 0, 0, localBad
	}
	if dataStart > size || dataStart+compSize > size {
		// The last entry of a truncated archive. Its bytes are incomplete, so
		// serving it would hand the reader a half image; `zip -FF` drops it for
		// the same reason.
		return e, 0, 0, localStop
	}

	name, enc := kenc.DecodeEntryName(rawName, flags&archive.FlagUTF8 != 0)
	e = archive.Entry{
		Name:         name,
		RawName:      append([]byte(nil), rawName...),
		NameEncoding: enc,
		Flags:        flags,
		Method:       method,
		CRC32:        crc,
		CompSize:     compSize,
		Size:         uncompSize,
		LocalHdrOff:  off,
		Modified:     msdosTime(modDate, modTime),
		// The external attributes live only in the central directory, so a
		// trailing slash is the only directory evidence a local header carries.
		Dir:       nameLen > 0 && rawName[nameLen-1] == '/',
		Encrypted: flags&archive.FlagEncrypted != 0,
	}
	if !usableEntry(e, size) {
		return archive.Entry{}, 0, 0, localBad
	}
	return e, dataStart, dataStart + compSize, localOK
}

// plausibleEntryName rejects raw name bytes that no archiver wrote.
//
// A control byte is the tell. Entry names in this collection are UTF-8, CP949
// or Shift_JIS, and none of those encodes a byte below 0x20 as part of a
// multi-byte character — so a NUL or a stray 0x07 means we are looking at
// compressed data, not a name.
func plausibleEntryName(raw []byte) bool {
	for _, b := range raw {
		if b < 0x20 || b == 0x7f {
			return false
		}
	}
	return true
}

// salvageReader reads an archive front-to-back through one reusable window.
//
// NFR-PRF-006 applies here as much as to a healthy read: a 120 MB container
// must not become a 120 MB allocation just because its directory is gone.
type salvageReader struct {
	r    io.ReaderAt
	size int64
	buf  []byte
	off  int64 // absolute offset of buf[0]
	n    int   // valid bytes in buf
}

// window returns the bytes at abs: [salvageChunk] of them, or every byte left
// in the archive when fewer remain. The result aliases the reader's buffer and
// stays valid only until the next call.
//
// The length guarantee is load-bearing, which is why a cache hit needs the
// cached buffer to cover the *whole* span and not merely to contain abs.
// Handing back a short tail of the previous window would make a caller near its
// edge see an archive that ends there: the chain walk would reject a header
// whose name ran past the slice, and the scan below would give up 29 bytes
// short of a 256 KiB window with the file still going. Both are silent losses,
// so the buffer is re-read instead — 541 bytes of overlap per window, against
// entries that would otherwise vanish.
func (s *salvageReader) window(abs int64) ([]byte, error) {
	if abs < 0 || abs >= s.size {
		return nil, io.EOF
	}
	span := min(int64(salvageChunk), s.size-abs)
	if s.n > 0 && abs >= s.off && abs+span <= s.off+int64(s.n) {
		return s.buf[abs-s.off : abs-s.off+span], nil
	}
	if int64(cap(s.buf)) < span {
		s.buf = make([]byte, span)
	}
	b := s.buf[:span]
	n, err := io.ReadFull(io.NewSectionReader(s.r, abs, span), b)
	if n <= 0 {
		if err == nil {
			err = io.EOF
		}
		return nil, err
	}
	s.off, s.n = abs, n
	return s.buf[:n], nil
}

// findLocalHeader scans forward from abs for the next position that parses as a
// local file header, and reports whether it found one.
//
// It validates candidates itself rather than handing every signature match back
// to the walk. The four bytes occur inside compressed data often enough that
// the round trip would dominate, and the check is the same one the walk would
// have made.
func (s *salvageReader) findLocalHeader(ctx context.Context, abs int64) (int64, bool) {
	for abs < s.size {
		if ctx.Err() != nil {
			return 0, false
		}
		buf, err := s.window(abs)
		if err != nil {
			return 0, false
		}
		// The last index worth testing. Away from EOF the scan stops a whole
		// header short of the window's edge, so one straddling the boundary is
		// retried from the next window instead of being rejected for being cut;
		// the next window starts at limit+1, so nothing is skipped. At EOF
		// there is no next window, and a position with fewer than 30 bytes
		// behind it cannot hold a header at all.
		limit := len(buf) - localHeaderLen
		if int64(len(buf)) == salvageChunk {
			limit = len(buf) - (localHeaderLen + maxSalvageNameLen)
		}
		if limit < 0 {
			return 0, false
		}
		for i := 0; i <= limit; i++ {
			if buf[i] != 'P' || buf[i+1] != 'K' || buf[i+2] != 0x03 || buf[i+3] != 0x04 {
				continue
			}
			if _, _, _, v := parseLocalHeader(buf[i:], abs+int64(i), s.size); v != localBad {
				return abs + int64(i), true
			}
		}
		abs += int64(limit) + 1
	}
	return 0, false
}

// errCorrupt reports whether err is the kind of structural damage worth trying
// to salvage. An I/O failure is not: the bytes may be fine and unreadable, and
// walking them would only produce a second, less honest error.
func errCorrupt(err error) bool { return errors.Is(err, archive.ErrCorrupt) }
