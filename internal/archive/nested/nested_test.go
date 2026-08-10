package nested_test

import (
	"bytes"
	"errors"
	"io"
	"math/rand"
	"testing"

	"shelf/internal/archive"
	"shelf/internal/archive/nested"
	"shelf/internal/archive/zipidx"
	"shelf/internal/testutil"
)

// innerVolume builds the bytes of a ZIP that will be nested inside another one.
func innerVolume(t testing.TB, pages int) []byte {
	t.Helper()
	entries := make([]testutil.Entry, 0, pages)
	for i := range pages {
		entries = append(entries, testutil.Entry{
			RawName: testutil.CP949(t, pageName(i)),
			Data:    testutil.TinyJPEG(t, 16, 16),
			Method:  testutil.MethodDeflate,
		})
	}
	return testutil.BuildZIP(t, testutil.ZIPSpec{Entries: entries})
}

func pageName(i int) string {
	return "겟백커스 01권/" + string(rune('0'+i/100%10)) + string(rune('0'+i/10%10)) + string(rune('0'+i%10)) + ".jpg"
}

// outerOf wraps one inner archive in an outer container and returns the outer
// bytes together with the ref that names the inner one.
func outerOf(t testing.TB, inner []byte, method uint16) ([]byte, archive.EntryRef) {
	t.Helper()
	outer := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
		{RawName: testutil.CP949(t, "겟백커스 01.zip"), Data: inner, Method: method},
	}})
	ix, err := zipidx.ReadCentralDirectory(t.Context(), bytes.NewReader(outer), int64(len(outer)))
	if err != nil {
		t.Fatalf("reading the outer container: %v", err)
	}
	if len(ix.Entries) != 1 {
		t.Fatalf("outer entries = %d, want 1", len(ix.Entries))
	}
	return outer, ix.Entries[0].Ref()
}

// TestOpen_readAtMatchesTheInnerBytesEverywhere is the correctness statement
// the whole package rests on: whatever offset and length are asked for, the
// adapter must return exactly what a plain bytes.Reader over the inner archive
// would.
func TestOpen_readAtMatchesTheInnerBytesEverywhere(t *testing.T) {
	t.Parallel()

	inner := innerVolume(t, 40)
	for _, tc := range []struct {
		name   string
		method uint16
	}{
		{"stored inner archive", testutil.MethodStore},
		{"deflated inner archive", testutil.MethodDeflate},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			outer, ref := outerOf(t, inner, tc.method)

			r, err := nested.Open(t.Context(), bytes.NewReader(outer), ref)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			defer r.Close()

			if r.Size() != int64(len(inner)) {
				t.Fatalf("Size = %d, want %d", r.Size(), len(inner))
			}

			// A deterministic mix of forward, backward and end-of-file reads:
			// backward is the case that forces a stream restart, and the end is
			// where the central directory lives.
			rng := rand.New(rand.NewSource(1))
			for i := range 300 {
				var off int64
				var n int
				switch i % 4 {
				case 0: // the very end, where zipidx looks first
					n = 1 + rng.Intn(2048)
					off = int64(len(inner) - n)
				case 1: // the very start
					off, n = int64(rng.Intn(64)), 1+rng.Intn(512)
				default:
					off = int64(rng.Intn(len(inner)))
					n = 1 + rng.Intn(4096)
				}
				if off < 0 {
					off = 0
				}
				got := make([]byte, n)
				gotN, gotErr := r.ReadAt(got, off)

				want := make([]byte, n)
				wantN, wantErr := bytes.NewReader(inner).ReadAt(want, off)

				if gotN != wantN || !bytes.Equal(got[:gotN], want[:wantN]) {
					t.Fatalf("ReadAt(%d bytes @ %d): got %d bytes %x..., want %d bytes %x...",
						n, off, gotN, first(got[:gotN]), wantN, first(want[:wantN]))
				}
				if (gotErr != nil) != (wantErr != nil) {
					t.Fatalf("ReadAt(%d bytes @ %d): err = %v, want %v", n, off, gotErr, wantErr)
				}
			}
		})
	}
}

func first(b []byte) []byte {
	if len(b) > 8 {
		return b[:8]
	}
	return b
}

// TestOpen_zipidxIndexesTheInnerArchiveUnchanged is the point of presenting an
// io.ReaderAt: the existing reader works on a nested volume with no changes at
// all, including the CP949 names inside it.
func TestOpen_zipidxIndexesTheInnerArchiveUnchanged(t *testing.T) {
	t.Parallel()

	inner := innerVolume(t, 12)
	for _, method := range []uint16{testutil.MethodStore, testutil.MethodDeflate} {
		outer, ref := outerOf(t, inner, method)
		r, err := nested.Open(t.Context(), bytes.NewReader(outer), ref)
		if err != nil {
			t.Fatalf("method %d: Open: %v", method, err)
		}

		ix, err := zipidx.ReadCentralDirectory(t.Context(), r, r.Size())
		if err != nil {
			r.Close()
			t.Fatalf("method %d: indexing the inner archive: %v", method, err)
		}
		if len(ix.Entries) != 12 {
			r.Close()
			t.Fatalf("method %d: inner entries = %d, want 12", method, len(ix.Entries))
		}
		if got := ix.Entries[0].Name; got != "겟백커스 01권/000.jpg" {
			r.Close()
			t.Errorf("method %d: first inner name = %q", method, got)
		}
		if got := ix.Entries[0].NameEncoding; got != "cp949" {
			r.Close()
			t.Errorf("method %d: first inner encoding = %q, want cp949", method, got)
		}

		// And a page streams back out of it byte for byte.
		body, err := zipidx.OpenEntry(t.Context(), r, ix.Entries[3].Ref())
		if err != nil {
			r.Close()
			t.Fatalf("method %d: OpenEntry: %v", method, err)
		}
		got, err := io.ReadAll(body)
		body.Close()
		r.Close()
		if err != nil {
			t.Fatalf("method %d: reading the page: %v", method, err)
		}
		if want := testutil.TinyJPEG(t, 16, 16); !bytes.Equal(got, want) {
			t.Errorf("method %d: page bytes differ (%d vs %d)", method, len(got), len(want))
		}
	}
}

// TestOpen_refusesWhatItCannotServe: an inner archive this build cannot stream
// must be classified, not guessed at.
func TestOpen_refusesWhatItCannotServe(t *testing.T) {
	t.Parallel()

	inner := innerVolume(t, 2)
	outer, ref := outerOf(t, inner, testutil.MethodStore)

	t.Run("unsupported method", func(t *testing.T) {
		t.Parallel()
		bad := ref
		bad.Method = 12 // bzip2: legal ZIP, not something this build inflates
		_, err := nested.Open(t.Context(), bytes.NewReader(outer), bad)
		if !errors.Is(err, archive.ErrUnsupportedMethod) {
			t.Errorf("err = %v, want archive.ErrUnsupportedMethod", err)
		}
	})

	t.Run("no local header at the recorded offset", func(t *testing.T) {
		t.Parallel()
		bad := ref
		bad.LocalHdrOff = 7
		_, err := nested.Open(t.Context(), bytes.NewReader(outer), bad)
		if !errors.Is(err, archive.ErrCorrupt) {
			t.Errorf("err = %v, want archive.ErrCorrupt", err)
		}
	})

	t.Run("negative geometry", func(t *testing.T) {
		t.Parallel()
		bad := ref
		bad.Size = -1
		_, err := nested.Open(t.Context(), bytes.NewReader(outer), bad)
		if !errors.Is(err, archive.ErrCorrupt) {
			t.Errorf("err = %v, want archive.ErrCorrupt", err)
		}
	})
}

// TestOpen_readPastTheEnd pins the io.ReaderAt contract at the boundary, which
// is where zipidx's tail scan spends all its time.
func TestOpen_readPastTheEnd(t *testing.T) {
	t.Parallel()

	inner := innerVolume(t, 3)
	for _, method := range []uint16{testutil.MethodStore, testutil.MethodDeflate} {
		outer, ref := outerOf(t, inner, method)
		r, err := nested.Open(t.Context(), bytes.NewReader(outer), ref)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}

		// Wholly past the end.
		if n, err := r.ReadAt(make([]byte, 16), r.Size()); n != 0 || !errors.Is(err, io.EOF) {
			t.Errorf("method %d: ReadAt at EOF = (%d, %v), want (0, io.EOF)", method, n, err)
		}
		// Straddling the end: a short read plus io.EOF.
		buf := make([]byte, 64)
		n, err := r.ReadAt(buf, r.Size()-16)
		if n != 16 || !errors.Is(err, io.EOF) {
			t.Errorf("method %d: straddling ReadAt = (%d, %v), want (16, io.EOF)", method, n, err)
		}
		if !bytes.Equal(buf[:16], inner[len(inner)-16:]) {
			t.Errorf("method %d: straddling ReadAt returned the wrong bytes", method)
		}
		r.Close()
	}
}
