package rar5

import (
	"context"
	"io"

	"shelf/internal/archive"
)

// Any is the reader for kind "rar": E-71 keeps one kind for both versions, so
// the version is chosen per container from its 8-byte signature. Anything
// that is not RAR 5 goes to the RAR 4 reader, which reports its own errors.
type Any struct {
	v4 archive.Reader
	v5 *Reader
}

// NewAny returns a reader that sends RAR 5 containers to this package and
// everything else to v4.
func NewAny(v4 archive.Reader) *Any { return &Any{v4: v4, v5: New()} }

// Format implements [archive.Reader].
func (*Any) Format() string { return "rar" }

func (a *Any) pick(r io.ReaderAt) archive.Reader {
	var sig [8]byte
	if n, _ := r.ReadAt(sig[:], 0); n == len(sig) && sig == signature {
		return a.v5
	}
	return a.v4
}

// ReadIndex implements [archive.Reader].
func (a *Any) ReadIndex(ctx context.Context, r io.ReaderAt, size int64) (*archive.Index, error) {
	return a.pick(r).ReadIndex(ctx, r, size)
}

// OpenEntry implements [archive.Reader].
func (a *Any) OpenEntry(ctx context.Context, r io.ReaderAt, ref archive.EntryRef) (io.ReadCloser, error) {
	return a.pick(r).OpenEntry(ctx, r, ref)
}

var _ archive.Reader = (*Any)(nil)
