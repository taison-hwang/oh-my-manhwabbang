package rar5

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"shelf/internal/archive"
	"shelf/internal/archive/rar4"
	"shelf/internal/testutil"
)

func readIndex(t *testing.T, r archive.Reader, raw []byte) (*archive.Index, error) {
	t.Helper()
	return r.ReadIndex(t.Context(), bytes.NewReader(raw), int64(len(raw)))
}

func readEntry(t *testing.T, r archive.Reader, ra io.ReaderAt, e archive.Entry) []byte {
	t.Helper()
	rc, err := r.OpenEntry(t.Context(), ra, e.Ref())
	if err != nil {
		t.Fatalf("OpenEntry(%q): %v", e.Name, err)
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("reading %q: %v", e.Name, err)
	}
	return b
}

// The one archive here that a real packer wrote.
func TestRealSample_indexesAndDecodes(t *testing.T) {
	raw := testutil.RAR5Sample()
	for _, r := range []archive.Reader{New(), NewAny(rar4.New())} {
		ix, err := readIndex(t, r, raw)
		if err != nil {
			t.Fatalf("%T ReadIndex: %v", r, err)
		}
		if len(ix.Entries) != 1 {
			t.Fatalf("entries = %d, want 1", len(ix.Entries))
		}
		e := ix.Entries[0]
		if e.Name != "asd.go" || e.Size != int64(len(testutil.RAR5PackedSampleText)) ||
			e.CRC32 != testutil.RAR5PackedSampleCRC || e.Method != 0x33 || e.NameEncoding != "utf-8" {
			t.Fatalf("entry = %+v", e)
		}
		if got := readEntry(t, r, bytes.NewReader(raw), e); string(got) != testutil.RAR5PackedSampleText {
			t.Fatalf("decoded %q", got)
		}
	}
}

var pages = []testutil.RAR5Entry{
	{Name: []byte("vol/001.jpg"), Data: bytes.Repeat([]byte{0xFF, 0xD8, 1}, 300)},
	{Name: []byte("vol"), Dir: true},
	{Name: []byte("vol/002.jpg"), Data: bytes.Repeat([]byte{0xFF, 0xD8, 2}, 500)},
	{Name: []byte("vol/한글 003.jpg"), Data: bytes.Repeat([]byte{0xFF, 0xD8, 3}, 200)},
}

func mixedArchive(t *testing.T) ([]byte, map[string][]byte) {
	t.Helper()
	entries := append([]testutil.RAR5Entry(nil), pages...)
	entries = append(entries[:2], append([]testutil.RAR5Entry{testutil.RAR5PackedSampleEntry("vol/packed.txt")}, entries[2:]...)...)
	want := map[string][]byte{"vol/packed.txt": []byte(testutil.RAR5PackedSampleText)}
	for _, e := range pages {
		if !e.Dir {
			want[string(e.Name)] = e.Data
		}
	}
	return testutil.BuildRAR5(t, testutil.RAR5Spec{Entries: entries, Comment: "a comment service header"}), want
}

// Byte oracle: every entry reads back as the bytes the fixture packed, stored
// entries stay seekable, and the comment service header is not an entry.
func TestReadIndexAndOpen_matchTheOracle(t *testing.T) {
	raw, want := mixedArchive(t)
	r := NewAny(rar4.New())
	ix, err := readIndex(t, r, raw)
	if err != nil {
		t.Fatalf("ReadIndex: %v", err)
	}
	var files, dirs int
	for _, e := range ix.Entries {
		if e.Dir {
			dirs++
			continue
		}
		files++
		w, ok := want[e.Name]
		if !ok {
			t.Fatalf("unexpected entry %q", e.Name)
		}
		if got := readEntry(t, r, bytes.NewReader(raw), e); !bytes.Equal(got, w) {
			t.Errorf("%q: %d bytes differ from the oracle's %d", e.Name, len(got), len(w))
		}
		rc, err := r.OpenEntry(t.Context(), bytes.NewReader(raw), e.Ref())
		if err != nil {
			t.Fatal(err)
		}
		_, seekable := rc.(io.ReadSeeker)
		if stored := e.Method == 0x30; seekable != stored {
			t.Errorf("%q: method 0x%02x, seekable %v", e.Name, e.Method, seekable)
		}
		rc.Close()
	}
	if files != len(want) || dirs != 1 {
		t.Fatalf("files %d dirs %d, want %d and 1", files, dirs, len(want))
	}
}

// rangeReader records every byte range read from it.
type rangeReader struct {
	r  io.ReaderAt
	mu sync.Mutex
	rs [][2]int64
}

func (rr *rangeReader) ReadAt(p []byte, off int64) (int, error) {
	n, err := rr.r.ReadAt(p, off)
	rr.mu.Lock()
	rr.rs = append(rr.rs, [2]int64{off, off + int64(n)})
	rr.mu.Unlock()
	return n, err
}

// FR-SRV-002: a page reads its own header and data, the signature, and for a
// packed entry the main header — never a byte of another entry.
func TestOpenEntry_readsOnlyThatEntry(t *testing.T) {
	raw, _ := mixedArchive(t)
	r := NewAny(rar4.New())
	ix, err := readIndex(t, r, raw)
	if err != nil {
		t.Fatal(err)
	}
	mainEnd := int64(len(signature))
	mh, err := readHeader(bytes.NewReader(raw), mainEnd, int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	mainEnd += mh.total()

	for i, e := range ix.Entries {
		if e.Dir {
			continue
		}
		end := int64(len(raw))
		for _, o := range ix.Entries {
			if o.LocalHdrOff > e.LocalHdrOff && o.LocalHdrOff < end {
				end = o.LocalHdrOff
			}
		}
		h, err := readHeader(bytes.NewReader(raw), e.LocalHdrOff, int64(len(raw)))
		if err != nil {
			t.Fatal(err)
		}
		own := [2]int64{e.LocalHdrOff, e.LocalHdrOff + h.total()}
		if own[1] > end {
			t.Fatalf("entry %d extends past the next header", i)
		}

		rr := &rangeReader{r: bytes.NewReader(raw)}
		readEntry(t, r, rr, e)
		var payload int64
		for _, g := range rr.rs {
			inSig := g[1] <= int64(len(signature))
			inMain := e.Method != 0x30 && g[0] >= int64(len(signature)) && g[1] <= mainEnd
			inOwn := g[0] >= own[0] && g[1] <= own[1]
			if !inSig && !inMain && !inOwn {
				t.Errorf("%q (method 0x%02x): read [%d,%d) outside its own [%d,%d)", e.Name, e.Method, g[0], g[1], own[0], own[1])
			}
			if inOwn {
				payload += g[1] - g[0]
			}
		}
		if limit := 2*h.hdrLen + e.CompSize + 7; payload > limit {
			t.Errorf("%q: read %d bytes of its own range, want at most %d", e.Name, payload, limit)
		}
	}
}

func TestRefusals_nameTheReason(t *testing.T) {
	page := testutil.RAR5Entry{Name: []byte("001.jpg"), Data: []byte("jpeg bytes")}
	with := func(f func(*testutil.RAR5Entry)) testutil.RAR5Spec {
		e := page
		f(&e)
		return testutil.RAR5Spec{Entries: []testutil.RAR5Entry{e}}
	}
	big := testutil.RAR5PackedSampleEntry("big.jpg")
	big.CompInfo |= 10 << 10 // 128 MiB dictionary
	big.UnpSize = 200 << 20

	cases := []struct {
		name   string
		spec   testutil.RAR5Spec
		status archive.Status
		reason string
	}{
		{"solid archive", testutil.RAR5Spec{ArcFlags: testutil.RAR5ArcSolid, Entries: []testutil.RAR5Entry{page}}, archive.StatusUnsupported, "solid archive"},
		{"volume", testutil.RAR5Spec{ArcFlags: testutil.RAR5ArcVolume, Entries: []testutil.RAR5Entry{page}}, archive.StatusUnsupported, "multi-volume"},
		{"header encryption", testutil.RAR5Spec{HeaderEncryption: true}, archive.StatusEncrypted, "encrypted"},
		{"entry encryption", with(func(e *testutil.RAR5Entry) { e.Encrypted = true }), archive.StatusEncrypted, "password"},
		{"split after", with(func(e *testutil.RAR5Entry) { e.HeadFlags = testutil.RAR5HeadSplitAfter }), archive.StatusUnsupported, "split"},
		{"split before", with(func(e *testutil.RAR5Entry) { e.HeadFlags = testutil.RAR5HeadSplitBefore }), archive.StatusUnsupported, "split"},
		{"solid entry", with(func(e *testutil.RAR5Entry) { e.CompInfo = testutil.RAR5CompSolid }), archive.StatusUnsupported, "solid entry"},
		{"no CRC-32", with(func(e *testutil.RAR5Entry) { e.NoCRC = true }), archive.StatusUnsupported, "no CRC-32"},
		{"version 2", with(func(e *testutil.RAR5Entry) { e.CompInfo = 2 | 3<<7 }), archive.StatusUnsupported, "compression version 2"},
		{"dictionary", testutil.RAR5Spec{Entries: []testutil.RAR5Entry{big}}, archive.StatusUnsupported, "dictionary"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			raw := testutil.BuildRAR5(t, c.spec)
			_, err := readIndex(t, NewAny(rar4.New()), raw)
			if got := archive.StatusOf(err); got != c.status {
				t.Fatalf("status %q (%v), want %q", got, err, c.status)
			}
			if !strings.Contains(err.Error(), c.reason) {
				t.Fatalf("error %q does not name %q", err, c.reason)
			}
		})
	}
}

// A large dictionary on a small entry is admitted: rardecode shrinks a
// non-solid entry's window to its size, and the limit is on that allocation.
func TestLargeDictionaryOnASmallEntry_isAdmitted(t *testing.T) {
	e := testutil.RAR5PackedSampleEntry("small.txt")
	e.CompInfo |= 10 << 10 // 128 MiB declared
	raw := testutil.BuildRAR5(t, testutil.RAR5Spec{Entries: []testutil.RAR5Entry{e}})
	ix, err := readIndex(t, New(), raw)
	if err != nil {
		t.Fatalf("ReadIndex: %v", err)
	}
	if got := readEntry(t, New(), bytes.NewReader(raw), ix.Entries[0]); string(got) != testutil.RAR5PackedSampleText {
		t.Fatalf("decoded %q", got)
	}
}

// The index and the container disagree: the container wins and says so.
func TestOpenEntry_refusesAnEntryThatChanged(t *testing.T) {
	raw, _ := mixedArchive(t)
	ix, err := readIndex(t, New(), raw)
	if err != nil {
		t.Fatal(err)
	}
	ref := ix.Entries[0].Ref()
	ref.CompSize++
	if _, err := New().OpenEntry(t.Context(), bytes.NewReader(raw), ref); !errors.Is(err, ErrEntryChanged) {
		t.Fatalf("err = %v, want ErrEntryChanged", err)
	}
	ref = ix.Entries[0].Ref()
	ref.LocalHdrOff++
	if _, err := New().OpenEntry(t.Context(), bytes.NewReader(raw), ref); archive.StatusOf(err) != archive.StatusError {
		t.Fatalf("err = %v, want a corrupt error", err)
	}
}

func TestPackedEntry_badChecksumFailsTheRead(t *testing.T) {
	e := testutil.RAR5PackedSampleEntry("x.txt")
	e.CRC ^= 1
	raw := testutil.BuildRAR5(t, testutil.RAR5Spec{Entries: []testutil.RAR5Entry{e}})
	ix, err := readIndex(t, New(), raw)
	if err != nil {
		t.Fatal(err)
	}
	rc, err := New().OpenEntry(t.Context(), bytes.NewReader(raw), ix.Entries[0].Ref())
	if err != nil {
		return
	}
	if _, err := io.ReadAll(rc); err == nil {
		t.Fatal("a wrong CRC-32 read back without error")
	}
}

func TestCorrupt_keepsEarlierEntriesAndNeverPanics(t *testing.T) {
	raw, _ := mixedArchive(t)
	ix, err := readIndex(t, New(), raw[:len(raw)-40])
	if archive.StatusOf(err) != archive.StatusError || ix == nil || len(ix.Entries) == 0 {
		t.Fatalf("truncated: %d entries, err %v", len(ix.Entries), err)
	}

	flipped := append([]byte(nil), raw...)
	at := bytes.Index(flipped, []byte("vol/001.jpg"))
	flipped[at+4] = '9'
	if _, err := readIndex(t, New(), flipped); !errors.Is(err, ErrBadHeader) {
		t.Fatalf("name byte changed under its header CRC: err %v, want ErrBadHeader", err)
	}

	r4 := testutil.BuildRAR4(t, testutil.RAR4Spec{Entries: []testutil.RAR4Entry{{Name: []byte("a.jpg"), Data: []byte("x")}}})
	if _, err := readIndex(t, New(), r4); !errors.Is(err, ErrNoSignature) {
		t.Fatalf("RAR 4 given to the RAR 5 reader: %v", err)
	}
	for n := 0; n < len(raw); n++ {
		_, _ = readIndex(t, NewAny(rar4.New()), raw[:n])
	}
}

// Kind "rar" keeps serving RAR 4 through the dispatcher.
func TestAny_sendsRAR4ToRAR4(t *testing.T) {
	raw := testutil.BuildRAR4(t, testutil.RAR4Spec{Entries: []testutil.RAR4Entry{
		{Name: []byte("001.jpg"), Data: []byte("first")},
		{Name: []byte("002.jpg"), Data: []byte("second")},
	}})
	r := NewAny(rar4.New())
	ix, err := readIndex(t, r, raw)
	if err != nil || len(ix.Entries) != 2 {
		t.Fatalf("entries %v err %v", ix, err)
	}
	if got := readEntry(t, r, bytes.NewReader(raw), ix.Entries[1]); string(got) != "second" {
		t.Fatalf("got %q", got)
	}
}

func FuzzReadIndex(f *testing.F) {
	f.Add(testutil.RAR5Sample())
	for _, spec := range []testutil.RAR5Spec{
		{Entries: pages, Comment: "c"},
		{Entries: []testutil.RAR5Entry{testutil.RAR5PackedSampleEntry("p.txt")}},
		{ArcFlags: testutil.RAR5ArcSolid, Entries: pages[:1]},
		{HeaderEncryption: true},
		{Entries: pages, OmitEnd: true, TruncateTail: 5},
	} {
		raw, err := testutil.RAR5Bytes(spec)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(raw)
	}
	f.Add(signature[:])
	f.Add([]byte(nil))

	r := NewAny(rar4.New())
	f.Fuzz(func(t *testing.T, data []byte) {
		ra := bytes.NewReader(data)
		ix, err := r.ReadIndex(t.Context(), ra, int64(len(data)))
		if ix == nil {
			if err == nil {
				t.Fatal("nil index and nil error")
			}
			return
		}
		for i, e := range ix.Entries {
			if e.LocalHdrOff < 0 || e.CompSize < 0 || e.Size < 0 || e.LocalHdrOff+e.CompSize > int64(len(data)) {
				t.Fatalf("entry %d out of bounds: %+v", i, e)
			}
			rc, err := r.OpenEntry(t.Context(), ra, e.Ref())
			if err != nil {
				continue
			}
			_, _ = io.Copy(io.Discard, io.LimitReader(rc, 1<<20))
			rc.Close()
		}
	})
}
