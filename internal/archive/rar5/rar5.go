// Package rar5 reads RAR 5.0 containers under the conditions ruling E-71
// admits them: not solid, one volume, not encrypted. The reasons are rar4's
// (D-71): a solid entry cannot be produced without the entries before it, so
// FR-SRV-002's one seek per page would not hold.
package rar5

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"

	"github.com/nwaples/rardecode/v2"

	"shelf/internal/archive"
	"shelf/internal/kenc"
)

const (
	// maxEntries bounds a walk over untrusted bytes, as in rar4.
	maxEntries = 200_000
	// maxWindow is E-71 §3.4: the header's dictionary size is a claim made by
	// the file, and rardecode allocates it, up to its own default ceiling of
	// 4 GiB per open entry. RAR 5's default dictionary, 32 MiB, fits.
	maxWindow = 64 << 20
	// methodBase maps RAR 5's method numbers 0-5 onto RAR 4's 0x30-0x35, so a
	// pages row of kind "rar" means the same method whichever version wrote it.
	methodBase = 0x30
)

// Reader is the RAR 5.0 implementation of [archive.Reader].
type Reader struct{}

// New returns a RAR 5 reader.
func New() *Reader { return &Reader{} }

// Format implements [archive.Reader].
func (*Reader) Format() string { return "rar" }

// ReadIndex implements [archive.Reader]. Entries parsed before a failure are
// returned with it (FR-IDX-010).
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
			return ix, fmt.Errorf("rar: %w: more than %d headers", ErrBadHeader, maxEntries)
		}
		h, err := readHeader(r, pos, size)
		if err != nil {
			return ix, err
		}
		switch h.typ {
		case typeCrypt:
			return ix, fmt.Errorf("rar: %w (headers are encrypted)", ErrEncrypted)
		case typeMain:
			sawMain = true
			c := cursor{b: h.body}
			flags := c.vint()
			if c.bad {
				return ix, fmt.Errorf("rar: %w at %d: main header", ErrBadHeader, h.off)
			}
			if flags&arcSolid != 0 {
				return ix, unsupported("solid archive: a page cannot be read without the pages before it")
			}
			if flags&arcVolume != 0 {
				return ix, unsupported("multi-volume archive: this build reads one file, not a set")
			}
		case typeFile:
			if !sawMain {
				return ix, fmt.Errorf("rar: %w before the file header at %d", ErrNoMainHeader, h.off)
			}
			f, err := parseFile(h)
			if err != nil {
				return ix, err
			}
			if err := admit(f); err != nil {
				return ix, err
			}
			ix.Entries = append(ix.Entries, entryOf(f))
		case typeEnd:
			return ix, nil
		}
		pos += h.total()
	}
	if !sawMain {
		return ix, fmt.Errorf("rar: %w", ErrNoMainHeader)
	}
	return ix, nil
}

// admit is every per-entry refusal of E-71 §3.3-3.4. OpenEntry repeats it
// against the container, which is the authority on its own bytes.
func admit(f fileHeader) error {
	switch {
	case f.encrypted:
		return fmt.Errorf("rar: %w", ErrEncrypted)
	case f.flags&(headSplitBefore|headSplitAfter) != 0:
		return unsupported("entry is split across volumes")
	case f.fileFlags&fileDir != 0:
		return nil
	case f.compInfo&compSolid != 0:
		return unsupported("solid entry: a page cannot be read without the pages before it")
	case f.fileFlags&fileUnknownSize != 0:
		return unsupported("entry records no unpacked size")
	case f.fileFlags&fileCRC == 0:
		// shelf verify (arch §6.4) holds every archive entry to a CRC-32.
		return unsupported("entry records a BLAKE2 hash and no CRC-32")
	case f.version() > 1 || f.method() > 5:
		return unsupported(fmt.Sprintf("compression version %d method %d", f.version(), f.method()))
	case f.method() != 0 && f.window() > maxWindow:
		return unsupported(fmt.Sprintf("dictionary of %d bytes exceeds %d", f.window(), maxWindow))
	case f.method() == 0 && f.dataSize != f.unpSize:
		return fmt.Errorf("rar: %w at %d: stored entry of %d bytes records %d unpacked",
			ErrBadHeader, f.off, f.dataSize, f.unpSize)
	}
	return nil
}

func entryOf(f fileHeader) archive.Entry {
	name, enc := kenc.DecodeEntryName(f.name, true)
	return archive.Entry{
		Name:         name,
		RawName:      append([]byte(nil), f.name...),
		NameEncoding: enc,
		Flags:        uint16(f.fileFlags),
		Method:       uint16(methodBase + f.method()),
		CRC32:        f.crc32,
		CompSize:     f.dataSize,
		Size:         f.unpSize,
		LocalHdrOff:  f.off,
		Dir:          f.fileFlags&fileDir != 0,
	}
}

func readSignature(r io.ReaderAt, size int64) error {
	if size < int64(len(signature)) {
		return fmt.Errorf("rar: %w (archive is %d bytes)", ErrNoSignature, size)
	}
	var sig [8]byte
	if _, err := r.ReadAt(sig[:], 0); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("rar: reading signature: %w", err)
	}
	if sig == signature {
		return nil
	}
	if [7]byte(sig[:7]) == [7]byte{0x52, 0x61, 0x72, 0x21, 0x1A, 0x07, 0x00} {
		return errNotRAR5Version
	}
	return fmt.Errorf("rar: %w", ErrNoSignature)
}

// OpenEntry implements [archive.Reader]. A stored entry is a seekable window
// onto the container, so Range works (FR-SRV-003). A packed entry is handed to
// rardecode as a one-entry archive — signature, the container's main header,
// this entry's header and data — which decodes alone because the archive is
// not solid; the bytes of no other entry are read.
func (*Reader) OpenEntry(ctx context.Context, r io.ReaderAt, ref archive.EntryRef) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if ref.LocalHdrOff < int64(len(signature)) || ref.CompSize < 0 {
		return nil, fmt.Errorf("rar: %w (offset %d, packed size %d)", ErrBadHeader, ref.LocalHdrOff, ref.CompSize)
	}
	h, err := readHeader(r, ref.LocalHdrOff, math.MaxInt64)
	if err != nil {
		return nil, err
	}
	if h.typ != typeFile {
		return nil, fmt.Errorf("rar: %w at %d: header type %d is not a file header", ErrBadHeader, h.off, h.typ)
	}
	f, err := parseFile(h)
	if err != nil {
		return nil, err
	}
	if err := admit(f); err != nil {
		return nil, err
	}
	if f.fileFlags&fileDir != 0 || f.dataSize != ref.CompSize || f.unpSize != ref.Size {
		return nil, fmt.Errorf("rar: %w at %d", ErrEntryChanged, h.off)
	}
	data := io.NewSectionReader(r, h.off+h.hdrLen, h.dataSize)
	if f.method() == 0 {
		return &sectionReadCloser{data}, nil
	}

	mainHdr, err := readMainHeader(r)
	if err != nil {
		return nil, err
	}
	fileHdr := make([]byte, h.hdrLen)
	if _, err := r.ReadAt(fileHdr, h.off); err != nil {
		return nil, fmt.Errorf("rar: reading file header at %d: %w", h.off, err)
	}
	var rr *rardecode.Reader
	err = guard(func() error {
		var err error
		rr, err = rardecode.NewReader(io.MultiReader(
			bytes.NewReader(signature[:]),
			bytes.NewReader(mainHdr),
			bytes.NewReader(fileHdr),
			data,
		))
		if err == nil {
			_, err = rr.Next()
		}
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("rar: opening entry at %d: %w", h.off, err)
	}
	return &unpackReadCloser{r: rr}, nil
}

// guard turns a panic inside rardecode into ErrDecode. rardecode v2.3.0's RAR 5
// decoder indexes out of range on some malformed streams (decode_reader.go:125,
// found by FuzzReadIndex), and a page is also read by background thumbnail
// workers, where an unrecovered panic ends the process.
func guard(f func() error) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("%w: %v", ErrDecode, p)
		}
	}()
	return f()
}

// readMainHeader returns the container's main header verbatim: its flags are
// the archive's, and ReadIndex has already refused the ones this package
// cannot honour.
func readMainHeader(r io.ReaderAt) ([]byte, error) {
	off := int64(len(signature))
	h, err := readHeader(r, off, math.MaxInt64)
	if err != nil {
		return nil, err
	}
	if h.typ != typeMain {
		return nil, fmt.Errorf("rar: %w (header type %d at %d)", ErrNoMainHeader, h.typ, off)
	}
	buf := make([]byte, h.hdrLen)
	if _, err := r.ReadAt(buf, off); err != nil {
		return nil, fmt.Errorf("rar: reading main header: %w", err)
	}
	return buf, nil
}

// sectionReadCloser keeps the Seeker that io.NopCloser would drop; the
// container handle belongs to the pool (internal/openpool), so Close does
// nothing.
type sectionReadCloser struct{ *io.SectionReader }

func (*sectionReadCloser) Close() error { return nil }

// unpackReadCloser stays failed once the decoder has panicked: its state is
// no longer trustworthy.
type unpackReadCloser struct {
	r   io.Reader
	err error
}

func (u *unpackReadCloser) Read(p []byte) (n int, err error) {
	if u.err != nil {
		return 0, u.err
	}
	err = guard(func() error {
		var rerr error
		n, rerr = u.r.Read(p)
		return rerr
	})
	if errors.Is(err, ErrDecode) {
		u.err, n = err, 0
	}
	return n, err
}

func (*unpackReadCloser) Close() error { return nil }

var (
	_ archive.Reader = (*Reader)(nil)
	_ io.ReadSeeker  = (*sectionReadCloser)(nil)
	_ io.ReadCloser  = (*unpackReadCloser)(nil)
)
