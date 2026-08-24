package hv3_test

import (
	"bytes"
	"io"
	"testing"
	"unicode/utf8"

	"shelf/internal/archive/hv3"
	"shelf/internal/testutil"
)

// This package parses whatever bytes a media volume happens to hold, and this
// format arrives with a worse prior than the other two: 54 of the 55 `.hv3`
// files on this machine are not HV3 containers at all. zipidx and rar4 carry a
// fuzz target for the same reason.
//
// The contract under fuzz is narrow and absolute: ReadIndex and OpenEntry may
// return anything at all, but they must not panic, must not hang, and must not
// hand back an entry whose recorded extent lies outside the container — a page
// row like that would send the serving path reading at an offset that means
// nothing.
func FuzzReadIndex(f *testing.F) {
	big := uint64(1 << 50)
	huge := uint32(1 << 30)

	f.Add(testutil.BuildHV3(f, testutil.HV3Spec{Entries: testutil.HV3Pages(2), Encr: testutil.HV3EncrMask}))
	f.Add(testutil.BuildHV3(f, testutil.HV3Spec{Entries: testutil.HV3Pages(2)}))
	f.Add(testutil.BuildHV3(f, testutil.HV3Spec{Entries: testutil.HV3Pages(1), OmitEncr: true}))
	f.Add(testutil.BuildHV3(f, testutil.HV3Spec{Entries: testutil.HV3Pages(1), Encr: 7}))
	f.Add(testutil.BuildHV3(f, testutil.HV3Spec{Entries: nil, Encr: testutil.HV3EncrMask}))
	f.Add(testutil.BuildHV3(f, testutil.HV3Spec{
		Entries: testutil.HV3Pages(1), ListSizeOverride: &big,
	}))
	f.Add(testutil.BuildHV3(f, testutil.HV3Spec{
		Entries: []testutil.HV3Entry{{Name: "a.jpg", Data: []byte("x"), SizeOverride: &huge}},
	}))
	f.Add(testutil.BuildHV3(f, testutil.HV3Spec{
		Entries: []testutil.HV3Entry{{Name: "a.jpg", Data: []byte("x"), Pos8: true}},
		Encr:    testutil.HV3EncrMask,
	}))
	f.Add(testutil.BuildHV3(f, testutil.HV3Spec{
		Entries: testutil.HV3Pages(1), Title: "LIST", Encr: testutil.HV3EncrMask,
	}))
	// The shapes the collection actually holds under this extension.
	f.Add([]byte("HV30"))
	f.Add([]byte("Rar!\x1a\x07\x00\x00\x00\x00\x00"))
	f.Add([]byte("PK\x03\x04nope"))
	f.Add([]byte("HV30LIST\x00\x00\x00\x00\xff\xff\xff\xff\xff\xff\xff\xff"))
	f.Add([]byte(nil))

	r := hv3.New()
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
				t.Fatalf("entry %d: negative size (stored %d, real %d)", i, e.CompSize, e.Size)
			}
			if e.LocalHdrOff+e.Size > size {
				t.Fatalf("entry %d: extent %d+%d runs past a %d-byte container",
					i, e.LocalHdrOff, e.Size, size)
			}
			// The name goes straight into pages.name and into JSON, so it has
			// to be valid UTF-8 however the UTF-16 decoded. A lossy repair may
			// leave U+FFFD in it, which is fine; invalid bytes are not.
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
