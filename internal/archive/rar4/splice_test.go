package rar4

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/nwaples/rardecode/v2"

	"shelf/internal/archive"
)

// The splice is the load-bearing trick of this package: it is what lets
// OpenEntry reach a packed entry from LocalHdrOff alone, with no name column
// and no ordinal, and it is only correct if the bytes it assembles really are
// a valid RAR archive.
//
// This checks that with a stored entry, which an unpacker reads by the same
// path a packed one takes — same signature, same main header, same block
// header, same payload framing, only the packing method differs. What it does
// not cover is the LZSS itself, which is rardecode's code and not ours;
// TestIntegration_realCollection_matchesUnpacker decodes all 229 packed
// entries in the collection against a whole-archive oracle for that.
func TestSplice_producesAnArchiveAnUnpackerAccepts(t *testing.T) {
	pages := []struct {
		name string
		data []byte
	}{
		{"book/001.jpg", bytes.Repeat([]byte("first "), 300)},
		{"book/002.jpg", bytes.Repeat([]byte("second "), 700)},
		{"book/003.jpg", []byte("third")},
	}
	b := newBuilder(t).mainHeader(0)
	for _, p := range pages {
		b = b.file(entryOpt{rawName: []byte(p.name), data: p.data})
	}
	raw := b.endArc().bytes()

	ix, err := readIndex(t, raw)
	if err != nil {
		t.Fatalf("ReadIndex: %v", err)
	}
	if len(ix.Entries) != len(pages) {
		t.Fatalf("got %d entries, want %d", len(ix.Entries), len(pages))
	}

	// Every entry, including the last, must come out of a splice of its own —
	// the point being that reaching entry 3 costs exactly what reaching entry
	// 1 costs, with nothing before it decoded.
	for i, want := range pages {
		ref := ix.Entries[i].Ref()
		var hdr [blockHeaderLen]byte
		if _, err := bytes.NewReader(raw).ReadAt(hdr[:], ref.LocalHdrOff); err != nil {
			t.Fatalf("entry %d: reading block header: %v", i, err)
		}
		hdrSize := int64(hdr[5]) | int64(hdr[6])<<8

		sp, err := splice(bytes.NewReader(raw), ref.LocalHdrOff, hdrSize,
			ref.LocalHdrOff+hdrSize, ref.CompSize)
		if err != nil {
			t.Fatalf("entry %d: splice: %v", i, err)
		}
		rr, err := rardecode.NewReader(sp)
		if err != nil {
			t.Fatalf("entry %d: the spliced bytes are not a RAR archive: %v", i, err)
		}
		h, err := rr.Next()
		if err != nil {
			t.Fatalf("entry %d: Next: %v", i, err)
		}
		if h.Name != want.name {
			t.Errorf("entry %d: spliced name %q, want %q", i, h.Name, want.name)
		}
		got, err := io.ReadAll(rr)
		if err != nil {
			t.Fatalf("entry %d: reading: %v", i, err)
		}
		if !bytes.Equal(got, want.data) {
			t.Errorf("entry %d: %d bytes out, want %d", i, len(got), len(want.data))
		}
		// The spliced archive holds this entry and nothing else.
		if _, err := rr.Next(); err != io.EOF {
			t.Errorf("entry %d: spliced archive has a second entry (err = %v)", i, err)
		}
	}
}

// readMainHeader copies the container's own main header rather than inventing
// one, so a container whose header is missing or misplaced fails loudly here
// instead of producing an archive that asserts flags the file never had.
func TestReadMainHeader_failures(t *testing.T) {
	t.Run("main header present", func(t *testing.T) {
		raw := newBuilder(t).mainHeader(0).
			file(entryOpt{rawName: []byte("a.jpg"), data: []byte("x")}).bytes()
		got, err := readMainHeader(bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("readMainHeader: %v", err)
		}
		if len(got) != mainHeaderLen {
			t.Errorf("copied %d bytes, want %d", len(got), mainHeaderLen)
		}
		if got[2] != blockMain {
			t.Errorf("copied block type 0x%02x, want 0x%02x", got[2], blockMain)
		}
	})

	t.Run("first block is not a main header", func(t *testing.T) {
		raw := newBuilder(t).
			file(entryOpt{rawName: []byte("a.jpg"), data: []byte("x")}).bytes()
		if _, err := readMainHeader(bytes.NewReader(raw)); err == nil {
			t.Fatal("expected an error")
		}
	})

	t.Run("truncated before the main header", func(t *testing.T) {
		if _, err := readMainHeader(bytes.NewReader(signature[:])); err == nil {
			t.Fatal("expected an error")
		}
	})
}

// The container decides how its own bytes are packed, not the index row.
//
// The case that matters is a stale `stored` in the index over a file that is
// now packed: the stored fast path would hand the client compressed bytes as
// though they were the image, and nothing would report an error. Here the
// index is deliberately lied to in both directions and the served bytes must
// follow the block header either way.
func TestOpenEntry_methodComesFromTheContainerNotTheIndex(t *testing.T) {
	data := bytes.Repeat([]byte("page bytes "), 40)
	raw := newBuilder(t).mainHeader(0).
		file(entryOpt{rawName: []byte("a.jpg"), data: data}).
		bytes()
	ix, err := readIndex(t, raw)
	if err != nil {
		t.Fatalf("ReadIndex: %v", err)
	}

	// The block is stored. An index row claiming it is packed must still
	// produce the stored bytes, not an unpacker run over them.
	for _, stale := range []uint16{0x31, 0x33, 0x35} {
		ref := ix.Entries[0].Ref()
		ref.Method = stale
		rc, err := New().OpenEntry(t.Context(), bytes.NewReader(raw), ref)
		if err != nil {
			t.Fatalf("index method %#x: OpenEntry: %v", stale, err)
		}
		got, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatalf("index method %#x: reading: %v", stale, err)
		}
		if !bytes.Equal(got, data) {
			t.Errorf("index method %#x: served %d bytes, want the stored %d",
				stale, len(got), len(data))
		}
	}

	// And a method the container itself declares and this build cannot serve
	// is refused rather than guessed at.
	packed := newBuilder(t).mainHeader(0).
		file(entryOpt{rawName: []byte("a.jpg"), data: data, method: 0x40}).
		bytes()
	pix, err := readIndex(t, packed)
	if err != nil {
		t.Fatalf("ReadIndex: %v", err)
	}
	ref := pix.Entries[0].Ref()
	ref.Method = MethodStore // the index says stored; the container says 0x40
	_, err = New().OpenEntry(t.Context(), bytes.NewReader(packed), ref)
	if err == nil {
		t.Fatal("expected the container's own method to be refused")
	}
	if !errors.Is(err, archive.ErrUnsupportedMethod) {
		t.Errorf("errors.Is(%v, ErrUnsupportedMethod) = false", err)
	}
}
