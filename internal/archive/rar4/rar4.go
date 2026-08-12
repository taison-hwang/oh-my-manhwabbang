// Package rar4 reads RAR 4.x containers the way zipidx reads ZIPs: the block
// chain only at index time, one seek to a recorded offset at serve time.
//
// It exists because decision D-07 kept [archive.Reader] an interface for
// exactly this, and because the measurement that reopened the question came
// out favourably. Of the 14 RAR archives in the reference collection holding
// 2,914 pages:
//
//	solid archives ....... 0        multi-volume ......... 0
//	solid entries ........ 0        encrypted ............ 0
//	stored entries ... 2,685        packed entries ..... 229 (in 3 books)
//	packing ratio ... 1.0000        RAR 5 ................ 0
//
// Solid is the number that decided it. In a solid archive every file shares
// one compression window, so page N cannot be produced without decompressing
// pages 1..N-1 first, and NFR-PRF-006's promise that a page costs one seek
// would be a lie. None of these archives is solid, and 92% of their entries
// are stored — byte-for-byte the same access pattern as a stored ZIP entry.
// So this package serves stored entries with no dependency at all, and calls
// rardecode only for the 8% that are actually packed.
//
// The two rules from [archive] hold here unchanged:
//
//   - FR-IDX-002 — indexing reads headers only. Nothing below decompresses an
//     entry payload while building an Index.
//   - FR-SRV-002 / NFR-PRF-006 — everything is io.ReaderAt. There is no shared
//     seek cursor, so concurrent reads of one container need no lock, and the
//     handle still comes from the pool (FR-SRV-004) rather than from a path,
//     which is what keeps the os.Root traversal guard of arch §8.1 in force.
package rar4

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/nwaples/rardecode/v2"

	"shelf/internal/archive"
	"shelf/internal/kenc"
)

// maxEntries bounds a walk over untrusted bytes. The largest RAR in the
// reference collection holds 826 entries; the largest ZIP the product indexes
// holds 1,071 pages. Six figures is far past anything real and still refuses
// to spin on a file whose block chain loops.
const maxEntries = 200_000

// Reader is the RAR 4.x implementation of [archive.Reader]. It holds no
// per-file state, so one value serves the whole process.
type Reader struct{}

// New returns a RAR4 reader.
func New() *Reader { return &Reader{} }

// Format implements [archive.Reader].
func (*Reader) Format() string { return "rar" }

// ReadIndex walks the block chain and records one [archive.Entry] per file
// block. No payload is read (FR-IDX-002): the walk moves header to header by
// the sizes the headers themselves declare.
//
// A chain that goes bad partway returns the entries parsed before it together
// with the error, which is what FR-IDX-010 asks for and what lets a truncated
// download still open at the pages it does have.
func (*Reader) ReadIndex(ctx context.Context, r io.ReaderAt, size int64) (*archive.Index, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := readSignature(r, size); err != nil {
		return nil, err
	}

	ix := &archive.Index{}
	var sawMain bool

	pos := int64(len(signature))
	for n := 0; pos < size; n++ {
		if n%512 == 0 {
			if err := ctx.Err(); err != nil {
				return ix, err
			}
		}
		if n >= maxEntries {
			return ix, fmt.Errorf("rar: %w: more than %d blocks", ErrBadBlockHeader, maxEntries)
		}
		b, err := readBlockHeader(r, pos, size)
		if err != nil {
			return ix, err
		}

		switch b.typ {
		case blockMain:
			sawMain = true
			// These three are properties of the whole container and decide
			// whether any page of it can be served, so they are checked before
			// a single entry is recorded.
			if b.flags&mhdPassword != 0 {
				return ix, fmt.Errorf("rar: %w", ErrEncrypted)
			}
			if b.flags&mhdSolid != 0 {
				return ix, unsupported("solid archive: a page cannot be read without the pages before it")
			}
			if b.flags&mhdVolume != 0 {
				return ix, unsupported("multi-volume archive: this build reads one file, not a set")
			}

		case blockFile:
			if !sawMain {
				return ix, fmt.Errorf("rar: %w before the file block at %d", ErrNoMainHeader, b.off)
			}
			f, err := parseFileBlock(r, b, size)
			if err != nil {
				return ix, err
			}
			// parseFileBlock is what learns the block's true 64-bit extent, so
			// the walk advances by its answer rather than by the 32-bit one
			// readBlockHeader could see.
			b = f.block
			if f.flags&lhdPassword != 0 {
				return ix, fmt.Errorf("rar: %w", ErrEncrypted)
			}
			if f.flags&lhdSolid != 0 {
				return ix, unsupported("solid entry: a page cannot be read without the pages before it")
			}
			if f.flags&(lhdSplitBefore|lhdSplitAfter) != 0 {
				return ix, unsupported("entry is split across volumes")
			}
			ix.Entries = append(ix.Entries, entryOf(f))

		case blockEndArc:
			// Everything after the end-of-archive block is recovery data or
			// padding, not entries. Stopping here is what keeps a RAR carrying
			// a recovery record from parsing its parity as garbage blocks.
			return finish(ix), nil
		}

		pos += b.total()
	}
	if !sawMain {
		return ix, fmt.Errorf("rar: %w", ErrNoMainHeader)
	}
	return finish(ix), nil
}

// finish applies the archive-wide encoding decision, exactly as zipidx does
// after reading a central directory. It is a no-op for every archive whose
// names all decoded, which is all 14 of them.
func finish(ix *archive.Index) *archive.Index {
	resolveArchiveNames(ix)
	return ix
}

func entryOf(f fileBlock) archive.Entry {
	unicode := f.flags&lhdUnicode != 0
	name, enc := decodeName(f.rawName, unicode)
	return archive.Entry{
		Name:         name,
		RawName:      legacyBytes(f.rawName, unicode),
		NameEncoding: enc,
		// Flags are RAR's, not ZIP's, and nothing downstream reads them as
		// ZIP general-purpose bits: the two fields the rest of the product
		// cares about — encryption and the name encoding — are already
		// resolved into Encrypted and NameEncoding above.
		Flags:       f.flags,
		Method:      f.method,
		CRC32:       f.crc32,
		CompSize:    f.packSize,
		Size:        f.unpSize,
		LocalHdrOff: f.off,
		Modified:    f.modified,
		Dir:         f.isDir,
		Encrypted:   f.flags&lhdPassword != 0,
	}
}

// resolveArchiveNames is zipidx's per-archive Shift_JIS fallback, applied to
// the RAR names that were read in a legacy code page.
//
// The rule it inherits matters more than the code: an encoding is decided for
// the archive as a whole and never per entry, because Shift_JIS reads Korean
// bytes happily and one name is not enough evidence to convict a container
// (see kenc.ArchiveFallback). Entries whose Unicode companion decoded are not
// evidence and are not rewritten — they are already right.
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
			name, enc := kenc.DecodeEntryNameAs(e.RawName, false, legacy)
			e.Name, e.NameEncoding = normalizeSeparators(name), enc
		}
	}
}

// OpenEntry streams one entry straight out of the container.
//
// Stored entries take the same path a stored ZIP entry takes: one small read
// of the block header to learn its length, then an io.SectionReader over the
// payload. The result implements io.ReadSeeker, so the HTTP layer hands it to
// http.ServeContent and Range works (arch §5.3). 92% of the collection's RAR
// pages are served this way, with rardecode not on the call path at all.
//
// Packed entries are the interesting case. [archive.EntryRef] carries no name
// and no ordinal — deliberately, it is the pages row and nothing more — so
// there is no way to ask an unpacker for "the entry called X". Instead the
// entry is lifted out of the container as a complete one-file archive:
//
//	signature + the container's own main header + this entry's block
//
// which is valid RAR and decodes to exactly this file. It works because the
// archive is not solid — ReadIndex refuses it otherwise — so the entry's
// compression window depends on nothing before it. LocalHdrOff keeps meaning
// what FR-SRV-002 says it means, no schema column is added, and reaching page
// 826 of a 385 MB archive costs the same as reaching page 1.
//
// The splice is assembled with io.MultiReader, so the payload is streamed from
// the container rather than buffered: only the two headers, a few dozen bytes,
// are ever in memory.
func (*Reader) OpenEntry(ctx context.Context, r io.ReaderAt, ref archive.EntryRef) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if ref.LocalHdrOff < 0 || ref.CompSize < 0 {
		return nil, fmt.Errorf("rar: %w (offset %d, packed size %d)",
			ErrBadBlockHeader, ref.LocalHdrOff, ref.CompSize)
	}

	// The block header is read far enough to reach METHOD, and that byte — not
	// ref.Method — decides the branch below.
	//
	// The container is the authority on how its own bytes are packed. Trusting
	// the index instead has one failure mode that matters: if the index says
	// stored and the file on disk is now packed, the stored fast path would
	// hand a client the compressed bytes as though they were the image, with
	// no error anywhere. Reading the byte we are already seeking past costs
	// nothing and removes the case. (The packed branch was never exposed to
	// it: rardecode reads the spliced header, so it always followed the
	// container.)
	var hdr [blockHeaderLen + fileHeaderFixedLen]byte
	if _, err := r.ReadAt(hdr[:], ref.LocalHdrOff); err != nil {
		return nil, fmt.Errorf("rar: reading block header at %d: %w", ref.LocalHdrOff, err)
	}
	if hdr[2] != blockFile {
		return nil, fmt.Errorf("rar: %w at %d: block type 0x%02x is not a file block",
			ErrBadBlockHeader, ref.LocalHdrOff, hdr[2])
	}
	hdrSize := int64(binary.LittleEndian.Uint16(hdr[5:7]))
	if hdrSize < blockHeaderLen+fileHeaderFixedLen {
		return nil, fmt.Errorf("rar: %w at %d: header size %d", ErrBadBlockHeader, ref.LocalHdrOff, hdrSize)
	}
	method := uint16(hdr[blockHeaderLen+18])
	dataOff := ref.LocalHdrOff + hdrSize

	if method == MethodStore {
		// FR-SRV-003: no decompression step at all, and the bytes stay
		// seekable so Range works.
		return &SectionReadCloser{SectionReader: io.NewSectionReader(r, dataOff, ref.CompSize)}, nil
	}
	if method < 0x31 || method > 0x35 {
		return nil, fmt.Errorf("rar: %w %s", ErrUnsupportedMethod, methodName(method))
	}

	spliced, err := splice(r, ref.LocalHdrOff, hdrSize, dataOff, ref.CompSize)
	if err != nil {
		return nil, err
	}
	rr, err := rardecode.NewReader(spliced)
	if err != nil {
		return nil, fmt.Errorf("rar: opening entry at %d: %w", ref.LocalHdrOff, err)
	}
	if _, err := rr.Next(); err != nil {
		return nil, fmt.Errorf("rar: opening entry at %d: %w", ref.LocalHdrOff, err)
	}
	return unpackReadCloser{rr}, nil
}

// splice presents one entry of a container as a complete one-file RAR archive:
// the signature, the container's own main header, this entry's block header,
// and this entry's packed bytes read straight from the container.
//
// io.MultiReader rather than a buffer, so the payload never lands in memory —
// only the two headers do, a few dozen bytes. The result is a plain io.Reader
// with no Seeker, which is all a one-entry archive needs: there is nothing to
// skip past.
func splice(r io.ReaderAt, hdrOff, hdrSize, dataOff, compSize int64) (io.Reader, error) {
	main, err := readMainHeader(r)
	if err != nil {
		return nil, err
	}
	blockHdr := make([]byte, hdrSize)
	if _, err := r.ReadAt(blockHdr, hdrOff); err != nil {
		return nil, fmt.Errorf("rar: reading file header at %d: %w", hdrOff, err)
	}
	return io.MultiReader(
		bytes.NewReader(signature[:]),
		bytes.NewReader(main),
		bytes.NewReader(blockHdr),
		io.NewSectionReader(r, dataOff, compSize),
	), nil
}

// readMainHeader returns the container's own 0x73 block verbatim.
//
// Copying it rather than synthesising one is deliberate: the main header
// carries the archive's flags, and a synthesised header would assert flags
// this file may not have. Since ReadIndex has already refused solid and
// multi-volume containers, the copied flags are known to be ones this package
// can honour.
func readMainHeader(r io.ReaderAt) ([]byte, error) {
	off := int64(len(signature))
	var hdr [blockHeaderLen + 4]byte
	if _, err := r.ReadAt(hdr[:blockHeaderLen], off); err != nil {
		return nil, fmt.Errorf("rar: reading main header: %w", err)
	}
	if hdr[2] != blockMain {
		return nil, fmt.Errorf("rar: %w (block type 0x%02x at %d)", ErrNoMainHeader, hdr[2], off)
	}
	flags := binary.LittleEndian.Uint16(hdr[3:5])
	size := int64(binary.LittleEndian.Uint16(hdr[5:7]))
	if size < blockHeaderLen {
		return nil, fmt.Errorf("rar: %w: main header size %d", ErrBadBlockHeader, size)
	}
	if flags&flagLongBlock != 0 {
		if _, err := r.ReadAt(hdr[blockHeaderLen:], off+blockHeaderLen); err != nil {
			return nil, fmt.Errorf("rar: reading main header payload size: %w", err)
		}
		size += int64(binary.LittleEndian.Uint32(hdr[blockHeaderLen:]))
	}
	buf := make([]byte, size)
	if _, err := r.ReadAt(buf, off); err != nil {
		return nil, fmt.Errorf("rar: reading main header: %w", err)
	}
	return buf, nil
}

// SectionReadCloser is a stored entry's payload: a read-only, seekable window
// onto the container with a Close that does nothing, because the container's
// handle belongs to the pool and not to this reader.
//
// io.NopCloser would have been shorter and wrong — it drops the Seeker, and
// with it Range support for every stored page.
type SectionReadCloser struct {
	*io.SectionReader
}

// Close implements io.Closer. Releasing the underlying file is the handle
// pool's job (internal/openpool); a page stream must never close it.
func (*SectionReadCloser) Close() error { return nil }

// unpackReadCloser adapts rardecode's forward-only reader, which has no Close
// of its own because it never opened anything.
type unpackReadCloser struct{ r io.Reader }

func (u unpackReadCloser) Read(p []byte) (int, error) { return u.r.Read(p) }
func (unpackReadCloser) Close() error                 { return nil }

var (
	_ archive.Reader = (*Reader)(nil)
	_ io.ReadSeeker  = (*SectionReadCloser)(nil)
	_ io.ReaderAt    = (*SectionReadCloser)(nil)
	_ io.ReadCloser  = (*SectionReadCloser)(nil)
	_ io.ReadCloser  = unpackReadCloser{}
)
