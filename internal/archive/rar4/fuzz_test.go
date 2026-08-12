package rar4

import (
	"bytes"
	"io"
	"testing"
	"unicode/utf8"
)

// This package parses whatever bytes a media volume happens to hold, and the
// collection already contains files whose tails are gone. zipidx carries a
// fuzz target for the same reason.
//
// The contract under fuzz is narrow and absolute: ReadIndex and OpenEntry may
// return anything at all, but they must not panic, must not hang, and must not
// hand back an entry whose recorded extent lies outside the container — a page
// row like that would send the serving path reading at an offset that means
// nothing.
func FuzzReadIndex(f *testing.F) {
	f.Add(newBuilder(f).mainHeader(0).
		file(entryOpt{rawName: []byte("001.jpg"), data: []byte("page one")}).
		file(entryOpt{rawName: []byte("002.jpg"), data: []byte("page two")}).
		endArc().bytes())
	f.Add(newBuilder(f).mainHeader(tSolidMain).
		file(entryOpt{rawName: []byte("a.jpg"), data: []byte("x")}).bytes())
	f.Add(newBuilder(f).mainHeader(0).
		file(entryOpt{rawName: []byte("dir"), dir: true}).bytes())
	f.Add(newBuilder(f).mainHeader(0).
		file(entryOpt{rawName: []byte("big"), data: []byte("x"), highUnp: 1}).bytes())
	f.Add(signature[:])
	f.Add([]byte("Rar!\x1a\x07\x01\x00"))
	f.Add([]byte("not a rar at all"))
	f.Add([]byte(nil))

	// A real name field: OEM prefix, NUL, RAR's encoded Unicode companion.
	vec := realNameVectors[0]
	name := append(append(append([]byte(nil), vec.oem...), 0), vec.enc...)
	f.Add(newBuilder(f).mainHeader(0).
		file(entryOpt{rawName: name, flags: tFileUnicode, data: []byte("x")}).bytes())

	r := New()
	f.Fuzz(func(t *testing.T, data []byte) {
		size := int64(len(data))
		ra := bytes.NewReader(data)

		ix, err := r.ReadIndex(t.Context(), ra, size)
		if ix == nil {
			if err == nil {
				t.Fatal("ReadIndex returned a nil index and a nil error")
			}
			return
		}

		for i := range ix.Entries {
			e := ix.Entries[i]
			// FR-IDX-010 keeps partially parsed entries, but a kept entry has
			// to be one the serving path can actually use.
			if e.LocalHdrOff < 0 || e.LocalHdrOff >= size {
				t.Fatalf("entry %d: LocalHdrOff %d outside a %d-byte container", i, e.LocalHdrOff, size)
			}
			if e.CompSize < 0 || e.Size < 0 {
				t.Fatalf("entry %d: negative size (packed %d, unpacked %d)", i, e.CompSize, e.Size)
			}
			if e.LocalHdrOff+e.CompSize > size {
				t.Fatalf("entry %d: extent %d+%d runs past a %d-byte container",
					i, e.LocalHdrOff, e.CompSize, size)
			}
			// kenc promises a name that is always valid UTF-8 so it can go
			// straight into pages.name and into JSON. A lossy repair may leave
			// U+FFFD in it, which is fine; invalid bytes are not.
			if !utf8.ValidString(e.Name) {
				t.Fatalf("entry %d: name is not valid UTF-8: %q", i, e.Name)
			}

			// Streaming it must not panic either. Errors are fine; the bytes
			// are arbitrary.
			rc, err := r.OpenEntry(t.Context(), ra, e.Ref())
			if err != nil {
				continue
			}
			_, _ = io.Copy(io.Discard, io.LimitReader(rc, 1<<20))
			_ = rc.Close()
		}
	})
}
