package zipidx

import (
	"encoding/binary"
	"fmt"
	"io"
)

// ZIP64 support (FR-IDX-009, 필수).
//
// No ZIP64 archive exists in the reference collection — the largest single
// file is 1.48 GB (data-survey, decision D-26) — so every line here is
// exercised only by hand-built fixtures and by the differential oracle running
// over them. Two generators cover it between them:
//
//   - testutil.BuildZIP64 escalates all three 32-bit slots at once, with and
//     without the optional disk slot and with and without local-header extras;
//   - the partial-escalation fixtures in centraldir_test.go escalate one slot
//     group at a time (offset only, sizes only), which is the shape a real
//     >4 GB archive produces and the only thing that pins the "consume a member
//     only for a slot that held the sentinel" rule below.
const (
	sigZIP64EOCD    uint32 = 0x06064b50
	sigZIP64Locator uint32 = 0x07064b50

	zip64EOCDLen    = 56
	zip64LocatorLen = 20

	// zip64Marker is the 32-bit sentinel meaning "the real value is in the
	// 0x0001 extra field" (or, in the end record, "in the ZIP64 end record").
	zip64Marker uint32 = 0xffffffff

	// extraZIP64 is the header id of the ZIP64 extended information extra
	// field (APPNOTE §4.5.3).
	extraZIP64 uint16 = 0x0001
)

// findZIP64End reads the 20-byte locator that sits immediately before the
// legacy end record and returns the offset of the ZIP64 end record, or -1 when
// there is no usable locator.
//
// A missing or multi-disk locator is not an error: the legacy record's
// sentinels may simply be a coincidence (an archive with exactly 65 535
// entries reads as 0xffff). Returning -1 lets the caller carry on with the
// 32-bit values, which is what archive/zip does.
func findZIP64End(r io.ReaderAt, tl *tail, endOffset int64) (int64, error) {
	locOffset := endOffset - zip64LocatorLen
	if locOffset < 0 {
		return -1, nil
	}
	buf, err := readSpan(r, tl, locOffset, zip64LocatorLen)
	if err != nil {
		return -1, fmt.Errorf("zip: reading zip64 locator: %w", err)
	}
	if len(buf) < zip64LocatorLen || binary.LittleEndian.Uint32(buf) != sigZIP64Locator {
		return -1, nil
	}
	if binary.LittleEndian.Uint32(buf[4:]) != 0 {
		return -1, nil // the end record lives on another disk: not a shape we serve
	}
	off := binary.LittleEndian.Uint64(buf[8:])
	if binary.LittleEndian.Uint32(buf[16:]) != 1 {
		return -1, nil // multi-disk archive
	}
	if off > uint64(1<<63-1) {
		return -1, fmt.Errorf("zip: %w (end record at offset %d)", ErrBadZIP64, off)
	}
	return int64(off), nil
}

// readZIP64End overwrites the 32-bit counts and offsets with the 64-bit ones.
func readZIP64End(r io.ReaderAt, tl *tail, off int64, end *endRecord) error {
	buf, err := readSpan(r, tl, off, zip64EOCDLen)
	if err != nil {
		return fmt.Errorf("zip: reading zip64 end record: %w", err)
	}
	if len(buf) < zip64EOCDLen || binary.LittleEndian.Uint32(buf) != sigZIP64EOCD {
		return fmt.Errorf("zip: %w at offset %d", ErrBadZIP64, off)
	}
	// Layout after the 12 bytes of signature + record size: version made by,
	// version needed, this disk, disk with the directory, entries on this
	// disk, entries total, directory size, directory offset.
	end.records = binary.LittleEndian.Uint64(buf[32:])
	end.dirSize = binary.LittleEndian.Uint64(buf[40:])
	end.dirOffset = binary.LittleEndian.Uint64(buf[48:])
	return nil
}

// zip64Sizes carries the three values that a central-directory record may have
// escalated into the 0x0001 extra field, along with which of them actually
// need resolving.
type zip64Sizes struct {
	uncompressed uint64
	compressed   uint64
	localOffset  uint64

	needUncomp bool
	needComp   bool
	needOffset bool
}

// parseZIP64Extra walks the extra-field block and fills in whichever members
// the 32-bit slots asked for. It reports whether a 0x0001 field was present.
//
// APPNOTE §4.5.3 fixes the order — uncompressed size, compressed size,
// local-header offset, disk number — and makes each member present *only* when
// its 32-bit counterpart held 0xffffffff. A parser that reads the slots
// positionally rather than by need gets the common "offset escalated, sizes
// not" archive wrong: it would read the offset out of the uncompressed-size
// slot. testutil.BuildZIP64 always escalates all three at once and so cannot
// catch that; TestReadCentralDirectory_zip64_partialEscalation builds the
// partial shapes by hand and is what holds this loop to the rule.
func parseZIP64Extra(extra []byte, s *zip64Sizes) (bool, error) {
	var found bool
	for len(extra) >= 4 {
		tag := binary.LittleEndian.Uint16(extra)
		size := int(binary.LittleEndian.Uint16(extra[2:]))
		extra = extra[4:]
		if len(extra) < size {
			// A truncated extra block is tolerated rather than fatal: other
			// writers do not even follow the basic format, and archive/zip
			// stops reading extras here too.
			break
		}
		field := extra[:size]
		extra = extra[size:]
		if tag != extraZIP64 {
			continue
		}
		found = true
		if s.needUncomp {
			s.needUncomp = false
			if len(field) < 8 {
				return found, ErrBadCentralHeader
			}
			s.uncompressed = binary.LittleEndian.Uint64(field)
			field = field[8:]
		}
		if s.needComp {
			s.needComp = false
			if len(field) < 8 {
				return found, ErrBadCentralHeader
			}
			s.compressed = binary.LittleEndian.Uint64(field)
			field = field[8:]
		}
		if s.needOffset {
			s.needOffset = false
			if len(field) < 8 {
				return found, ErrBadCentralHeader
			}
			s.localOffset = binary.LittleEndian.Uint64(field)
		}
	}
	return found, nil
}
