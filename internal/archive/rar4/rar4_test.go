package rar4

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"shelf/internal/archive"
	"shelf/internal/kenc"
)

func readIndex(t *testing.T, b []byte) (*archive.Index, error) {
	t.Helper()
	return New().ReadIndex(t.Context(), bytes.NewReader(b), int64(len(b)))
}

func TestReadIndex_storedEntries(t *testing.T) {
	page1 := []byte("first page bytes")
	page2 := []byte("second page bytes, longer")
	raw := newBuilder(t).
		mainHeader(0).
		file(entryOpt{rawName: []byte("book/001.jpg"), data: page1}).
		file(entryOpt{rawName: []byte("book/002.jpg"), data: page2}).
		endArc().
		bytes()

	ix, err := readIndex(t, raw)
	if err != nil {
		t.Fatalf("ReadIndex: %v", err)
	}
	if len(ix.Entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(ix.Entries))
	}
	if got, want := ix.Entries[0].Name, "book/001.jpg"; got != want {
		t.Errorf("Name = %q, want %q", got, want)
	}
	if got, want := ix.Entries[1].Size, int64(len(page2)); got != want {
		t.Errorf("Size = %d, want %d", got, want)
	}
	if got, want := ix.Entries[0].Method, MethodStore; got != want {
		t.Errorf("Method = %#x, want %#x", got, want)
	}
	if ix.Entries[0].Dir || ix.Entries[1].Dir {
		t.Error("stored files reported as directories")
	}

	// The offsets are the whole point: streaming must land on the payload.
	for i, want := range [][]byte{page1, page2} {
		rc, err := New().OpenEntry(t.Context(), bytes.NewReader(raw), ix.Entries[i].Ref())
		if err != nil {
			t.Fatalf("OpenEntry(%d): %v", i, err)
		}
		got, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("reading entry %d: %v", i, err)
		}
		_ = rc.Close()
		if !bytes.Equal(got, want) {
			t.Errorf("entry %d = %q, want %q", i, got, want)
		}
	}
}

// FR-SRV-003 / arch §5.3: a stored page must come back seekable, or every
// Range request for an uncompressed page silently becomes a full download.
func TestOpenEntry_storedIsSeekable(t *testing.T) {
	data := []byte("0123456789abcdef")
	raw := newBuilder(t).
		mainHeader(0).
		file(entryOpt{rawName: []byte("p.jpg"), data: data}).
		bytes()
	ix, err := readIndex(t, raw)
	if err != nil {
		t.Fatalf("ReadIndex: %v", err)
	}
	rc, err := New().OpenEntry(t.Context(), bytes.NewReader(raw), ix.Entries[0].Ref())
	if err != nil {
		t.Fatalf("OpenEntry: %v", err)
	}
	defer func() { _ = rc.Close() }()

	rs, ok := rc.(io.ReadSeeker)
	if !ok {
		t.Fatalf("stored entry is %T, which is not an io.ReadSeeker", rc)
	}
	if _, err := rs.Seek(10, io.SeekStart); err != nil {
		t.Fatalf("Seek: %v", err)
	}
	got, err := io.ReadAll(rs)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if want := "abcdef"; string(got) != want {
		t.Errorf("after seek got %q, want %q", got, want)
	}
}

func TestReadIndex_directoryEntry(t *testing.T) {
	raw := newBuilder(t).
		mainHeader(0).
		file(entryOpt{rawName: []byte("book"), dir: true}).
		file(entryOpt{rawName: []byte("book/001.jpg"), data: []byte("x")}).
		bytes()
	ix, err := readIndex(t, raw)
	if err != nil {
		t.Fatalf("ReadIndex: %v", err)
	}
	if len(ix.Entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(ix.Entries))
	}
	if !ix.Entries[0].Dir {
		t.Error("directory entry not flagged as Dir")
	}
	if ix.Entries[1].Dir {
		t.Error("file entry flagged as Dir")
	}
}

// RAR writes `\` between path components. Everything above this package —
// source.Excluded, baseName, the natsort order, the API's entry_path — treats
// an entry path as slash-separated, so the conversion has to happen here.
func TestReadIndex_backslashSeparatorsBecomeSlashes(t *testing.T) {
	raw := newBuilder(t).
		mainHeader(0).
		file(entryOpt{rawName: []byte(`vol 01\ch 1\001.jpg`), data: []byte("x")}).
		bytes()
	ix, err := readIndex(t, raw)
	if err != nil {
		t.Fatalf("ReadIndex: %v", err)
	}
	if got, want := ix.Entries[0].Name, "vol 01/ch 1/001.jpg"; got != want {
		t.Errorf("Name = %q, want %q", got, want)
	}
}

// The refusals. Each is a container this build could parse but must not serve
// pages from, and each has to be classified as unsupported rather than as
// corruption — an operator reading "archive is corrupt" about a perfectly good
// solid RAR would go looking for a damaged file that does not exist.
func TestReadIndex_refusals(t *testing.T) {
	body := []byte("payload")
	tests := []struct {
		name  string
		raw   []byte
		kind  error
		match string
	}{
		{
			name: "solid archive",
			raw: newBuilder(t).mainHeader(tSolidMain).
				file(entryOpt{rawName: []byte("a.jpg"), data: body}).bytes(),
			kind:  archive.ErrUnsupportedMethod,
			match: "solid archive",
		},
		{
			name: "multi-volume archive",
			raw: newBuilder(t).mainHeader(tVolumeMain).
				file(entryOpt{rawName: []byte("a.jpg"), data: body}).bytes(),
			kind:  archive.ErrUnsupportedMethod,
			match: "multi-volume",
		},
		{
			name: "solid entry",
			raw: newBuilder(t).mainHeader(0).
				file(entryOpt{rawName: []byte("a.jpg"), data: body, flags: tFileSolid}).bytes(),
			kind:  archive.ErrUnsupportedMethod,
			match: "solid entry",
		},
		{
			name: "entry split across volumes",
			raw: newBuilder(t).mainHeader(0).
				file(entryOpt{rawName: []byte("a.jpg"), data: body, flags: tSplitAfter}).bytes(),
			kind:  archive.ErrUnsupportedMethod,
			match: "split across volumes",
		},
		{
			name: "archive password",
			raw: newBuilder(t).mainHeader(tPasswordMain).
				file(entryOpt{rawName: []byte("a.jpg"), data: body}).bytes(),
			kind:  archive.ErrEncrypted,
			match: "password-protected",
		},
		{
			name: "entry password",
			raw: newBuilder(t).mainHeader(0).
				file(entryOpt{rawName: []byte("a.jpg"), data: body, flags: tFilePassword}).bytes(),
			kind:  archive.ErrEncrypted,
			match: "password-protected",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := readIndex(t, tc.raw)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !errors.Is(err, tc.kind) {
				t.Errorf("errors.Is(%v, %v) = false", err, tc.kind)
			}
			if !bytes.Contains([]byte(err.Error()), []byte(tc.match)) {
				t.Errorf("message %q does not mention %q", err, tc.match)
			}
		})
	}
}

// RAR 5 is a different format wearing a similar signature. Saying so beats
// "corrupt", which would send an operator hunting for damage.
func TestReadIndex_rar5IsNamed(t *testing.T) {
	raw := []byte{0x52, 0x61, 0x72, 0x21, 0x1A, 0x07, 0x01, 0x00, 0x00, 0x00}
	_, err := readIndex(t, raw)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errors.Is(err, archive.ErrUnsupportedMethod) {
		t.Errorf("errors.Is(err, ErrUnsupportedMethod) = false for %v", err)
	}
	if !bytes.Contains([]byte(err.Error()), []byte("RAR 5")) {
		t.Errorf("message %q does not name RAR 5", err)
	}
}

func TestReadIndex_structuralFailures(t *testing.T) {
	full := newBuilder(t).
		mainHeader(0).
		file(entryOpt{rawName: []byte("001.jpg"), data: bytes.Repeat([]byte("a"), 64)}).
		file(entryOpt{rawName: []byte("002.jpg"), data: bytes.Repeat([]byte("b"), 64)}).
		endArc()

	tests := []struct {
		name    string
		raw     []byte
		kind    error
		entries int // entries expected alongside the error (FR-IDX-010)
	}{
		{
			name: "not a rar at all",
			raw:  []byte("PK\x03\x04 this is a zip"),
			kind: archive.ErrCorrupt,
		},
		{
			name: "empty file",
			raw:  nil,
			kind: archive.ErrCorrupt,
		},
		{
			name: "signature only",
			raw:  signature[:],
			kind: archive.ErrCorrupt,
		},
		{
			name:    "truncated mid-payload keeps the entries before it",
			raw:     full.truncate(len(full.bytes()) - 80),
			kind:    archive.ErrCorrupt,
			entries: 1,
		},
		{
			name: "file block before any main header",
			raw: newBuilder(t).
				file(entryOpt{rawName: []byte("001.jpg"), data: []byte("x")}).bytes(),
			kind: archive.ErrCorrupt,
		},
		{
			name: "block header claiming an impossible size",
			raw: newBuilder(t).mainHeader(0).
				raw(0, 0, blockFile, 0, 0, 3, 0).bytes(),
			kind: archive.ErrCorrupt,
		},
		{
			name: "packed size larger than the file",
			raw: newBuilder(t).mainHeader(0).
				file(entryOpt{rawName: []byte("a.jpg"), data: []byte("1234"), packSize: 9999}).bytes(),
			kind: archive.ErrCorrupt,
		},
		{
			// The 64-bit size fields are the reason parseFileBlock, and not
			// readBlockHeader, settles a block's extent: ADD_SIZE holds only
			// the low half. A high half of 1 means "4 GB of payload", which
			// this handful of bytes plainly does not have, and the walk must
			// say so rather than step 4 GB short and read a payload as a
			// header.
			name: "64-bit packed size larger than the file",
			raw: newBuilder(t).mainHeader(0).
				file(entryOpt{rawName: []byte("a.jpg"), data: []byte("1234"), highPack: 1}).bytes(),
			kind: archive.ErrCorrupt,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ix, err := readIndex(t, tc.raw)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !errors.Is(err, tc.kind) {
				t.Errorf("errors.Is(%v, %v) = false", err, tc.kind)
			}
			if tc.entries > 0 {
				if ix == nil {
					t.Fatal("index is nil; FR-IDX-010 wants the entries that did parse")
				}
				if len(ix.Entries) != tc.entries {
					t.Errorf("kept %d entries, want %d", len(ix.Entries), tc.entries)
				}
			}
		})
	}
}

// A truncated archive must still yield readable pages, and those pages must
// actually stream — the whole point of keeping them.
func TestReadIndex_truncatedArchiveStillServesItsPages(t *testing.T) {
	good := bytes.Repeat([]byte("a"), 64)
	b := newBuilder(t).
		mainHeader(0).
		file(entryOpt{rawName: []byte("001.jpg"), data: good}).
		file(entryOpt{rawName: []byte("002.jpg"), data: bytes.Repeat([]byte("b"), 64)})
	raw := b.truncate(len(b.bytes()) - 80)

	ix, err := readIndex(t, raw)
	if err == nil {
		t.Fatal("expected a truncation error")
	}
	if len(ix.Entries) != 1 {
		t.Fatalf("kept %d entries, want 1", len(ix.Entries))
	}
	rc, err := New().OpenEntry(t.Context(), bytes.NewReader(raw), ix.Entries[0].Ref())
	if err != nil {
		t.Fatalf("OpenEntry on the surviving entry: %v", err)
	}
	defer func() { _ = rc.Close() }()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("reading the surviving entry: %v", err)
	}
	if !bytes.Equal(got, good) {
		t.Errorf("surviving page = %q, want %q", got, good)
	}
}

// A legal 64-bit entry: hugely compressible, so the unpacked size needs the
// high half while the payload stays small. It exercises the part of the header
// that moves when lhdLarge is set — the name starts eight bytes later — which
// a wrong offset would turn into a garbage name rather than an error.
func TestReadIndex_sixtyFourBitUnpackedSize(t *testing.T) {
	data := []byte("packed")
	raw := newBuilder(t).mainHeader(0).
		file(entryOpt{rawName: []byte("huge.bin"), data: data, unpSize: 7, highUnp: 1}).
		endArc().
		bytes()

	ix, err := readIndex(t, raw)
	if err != nil {
		t.Fatalf("ReadIndex: %v", err)
	}
	if len(ix.Entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(ix.Entries))
	}
	e := ix.Entries[0]
	if got, want := e.Name, "huge.bin"; got != want {
		t.Errorf("Name = %q, want %q — the 64-bit fields shifted the name", got, want)
	}
	if got, want := e.Size, int64(1)<<32|7; got != want {
		t.Errorf("Size = %d, want %d", got, want)
	}
	if got, want := e.CompSize, int64(len(data)); got != want {
		t.Errorf("CompSize = %d, want %d", got, want)
	}
}

func TestOpenEntry_rejectsBadRefs(t *testing.T) {
	raw := newBuilder(t).
		mainHeader(0).
		file(entryOpt{rawName: []byte("a.jpg"), data: []byte("payload")}).
		bytes()
	ix, err := readIndex(t, raw)
	if err != nil {
		t.Fatalf("ReadIndex: %v", err)
	}
	good := ix.Entries[0].Ref()

	t.Run("negative offset", func(t *testing.T) {
		ref := good
		ref.LocalHdrOff = -1
		if _, err := New().OpenEntry(t.Context(), bytes.NewReader(raw), ref); err == nil {
			t.Fatal("expected an error")
		}
	})
	t.Run("offset that is not a file block", func(t *testing.T) {
		ref := good
		ref.LocalHdrOff = 0 // the signature, not a block
		_, err := New().OpenEntry(t.Context(), bytes.NewReader(raw), ref)
		if err == nil {
			t.Fatal("expected an error")
		}
		if !errors.Is(err, archive.ErrCorrupt) {
			t.Errorf("errors.Is(%v, ErrCorrupt) = false", err)
		}
	})
	t.Run("offset past the end of the container", func(t *testing.T) {
		ref := good
		ref.LocalHdrOff = int64(len(raw)) + 1
		if _, err := New().OpenEntry(t.Context(), bytes.NewReader(raw), ref); err == nil {
			t.Fatal("expected an error")
		}
	})
	// An unsupported packing method is not tested here any more: it is a
	// property of the container, not of the ref, and
	// TestOpenEntry_methodComesFromTheContainerNotTheIndex is where that lives.
}

func TestReadIndex_contextCancellation(t *testing.T) {
	raw := newBuilder(t).
		mainHeader(0).
		file(entryOpt{rawName: []byte("a.jpg"), data: []byte("x")}).
		bytes()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := New().ReadIndex(ctx, bytes.NewReader(raw), int64(len(raw))); !errors.Is(err, context.Canceled) {
		t.Errorf("ReadIndex on a cancelled context = %v, want context.Canceled", err)
	}
	if _, err := New().OpenEntry(ctx, bytes.NewReader(raw), archive.EntryRef{}); !errors.Is(err, context.Canceled) {
		t.Errorf("OpenEntry on a cancelled context = %v, want context.Canceled", err)
	}
}

func TestFormat(t *testing.T) {
	if got, want := New().Format(), "rar"; got != want {
		t.Errorf("Format() = %q, want %q", got, want)
	}
}

// arch §4.4 applies to RAR names exactly as it does to ZIP names: a name with
// no Unicode companion is legacy-encoded and goes through kenc, so a CP949
// archive reads correctly and reports the encoding it was read in.
func TestReadIndex_legacyNamesGoThroughKenc(t *testing.T) {
	// "한글.jpg" in CP949.
	cp949 := []byte{0xC7, 0xD1, 0xB1, 0xDB, '.', 'j', 'p', 'g'}
	raw := newBuilder(t).
		mainHeader(0).
		file(entryOpt{rawName: cp949, data: []byte("x")}).
		bytes()
	ix, err := readIndex(t, raw)
	if err != nil {
		t.Fatalf("ReadIndex: %v", err)
	}
	e := ix.Entries[0]
	if got, want := e.Name, "한글.jpg"; got != want {
		t.Errorf("Name = %q, want %q", got, want)
	}
	if got, want := e.NameEncoding, kenc.EncCP949; got != want {
		t.Errorf("NameEncoding = %q, want %q", got, want)
	}
	if !bytes.Equal(e.RawName, cp949) {
		t.Errorf("RawName = % x, want % x", e.RawName, cp949)
	}
}

func TestReadIndex_asciiNameIsUTF8(t *testing.T) {
	raw := newBuilder(t).
		mainHeader(0).
		file(entryOpt{rawName: []byte("001.jpg"), data: []byte("x")}).
		bytes()
	ix, err := readIndex(t, raw)
	if err != nil {
		t.Fatalf("ReadIndex: %v", err)
	}
	if got, want := ix.Entries[0].NameEncoding, kenc.EncUTF8; got != want {
		t.Errorf("NameEncoding = %q, want %q", got, want)
	}
}

// The end-of-archive block ends the walk. A RAR carrying a recovery record
// stores parity after it, and parsing that as blocks invents entries.
func TestReadIndex_stopsAtEndOfArchiveBlock(t *testing.T) {
	raw := newBuilder(t).
		mainHeader(0).
		file(entryOpt{rawName: []byte("001.jpg"), data: []byte("x")}).
		endArc().
		raw(bytes.Repeat([]byte{0xDE, 0xAD, 0xBE, 0xEF}, 64)...).
		bytes()
	ix, err := readIndex(t, raw)
	if err != nil {
		t.Fatalf("ReadIndex: %v", err)
	}
	if len(ix.Entries) != 1 {
		t.Fatalf("got %d entries, want 1 — the trailing bytes were parsed as blocks", len(ix.Entries))
	}
}
