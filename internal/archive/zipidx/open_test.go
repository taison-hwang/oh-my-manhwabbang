package zipidx_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"sync"
	"testing"

	"shelf/internal/archive"
	"shelf/internal/archive/zipidx"
	"shelf/internal/testutil"
)

// bigArchive builds an archive of n pages of the given payload size, so that
// "reading one page costs the same at any archive size" can be measured rather
// than asserted.
func bigArchive(t testing.TB, pages, pageBytes int, method uint16) ([]byte, [][]byte) {
	t.Helper()
	entries := make([]testutil.Entry, 0, pages)
	payloads := make([][]byte, 0, pages)
	for i := 0; i < pages; i++ {
		// Deterministic, poorly-compressible content: a deflated entry must
		// stay roughly pageBytes so the byte accounting is meaningful.
		p := make([]byte, pageBytes)
		seed := uint32(i*2654435761 + 1)
		for j := range p {
			seed = seed*1664525 + 1013904223
			p[j] = byte(seed >> 24)
		}
		payloads = append(payloads, p)
		entries = append(entries, testutil.Entry{
			Name:   pageName(i + 1),
			Data:   p,
			Method: method,
		})
	}
	return testutil.BuildZIP(t, testutil.ZIPSpec{Entries: entries}), payloads
}

func pageName(n int) string {
	digits := []byte("0000")
	for i := 3; i >= 0 && n > 0; i-- {
		digits[i] = byte('0' + n%10)
		n /= 10
	}
	return string(digits) + ".jpg"
}

// impl-plan WP-04 acceptance 4: OpenEntry reads exactly comp_size + 30 bytes.
// That is the literal content of FR-SRV-002 and NFR-PRF-006 — the archive is
// never buffered, and no part of it outside the entry is touched.
func TestOpenEntry_readsExactlyCompSizePlus30(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		method uint16
	}{
		{"stored", testutil.MethodStore},
		{"deflate", testutil.MethodDeflate},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data, payloads := bigArchive(t, 40, 4096, tc.method)
			ix, err := zipidx.ReadCentralDirectory(t.Context(), bytes.NewReader(data), int64(len(data)))
			if err != nil {
				t.Fatalf("ReadCentralDirectory: %v", err)
			}

			// First, middle and last page, as arch §5.1 verified on the real
			// 104-entry archive.
			for _, idx := range []int{0, len(ix.Entries) / 2, len(ix.Entries) - 1} {
				e := ix.Entries[idx]
				c := newCounter(data)
				rc, err := zipidx.OpenEntry(t.Context(), c, e.Ref())
				if err != nil {
					t.Fatalf("OpenEntry %q: %v", e.Name, err)
				}
				got, err := io.ReadAll(rc)
				if err != nil {
					t.Fatalf("reading %q: %v", e.Name, err)
				}
				if err := rc.Close(); err != nil {
					t.Fatalf("closing %q: %v", e.Name, err)
				}

				if want := e.CompSize + 30; c.bytes.Load() != want {
					t.Errorf("%q: read %d bytes, want exactly %d (comp_size + 30)",
						e.Name, c.bytes.Load(), want)
				}
				if !bytes.Equal(got, payloads[idx]) {
					t.Errorf("%q: content differs from what was packed", e.Name)
				}
				if sum := crc32.ChecksumIEEE(got); sum != e.CRC32 {
					t.Errorf("%q: crc32 = %#08x, central directory says %#08x", e.Name, sum, e.CRC32)
				}
			}
		})
	}
}

// FR-SRV-003: a stored entry is handed through with no decompression step, and
// stays seekable so the HTTP layer can answer Range.
func TestOpenEntry_storedEntry_isSeekableAndUnwrapped(t *testing.T) {
	t.Parallel()
	payload := bytes.Repeat([]byte("stored page bytes "), 512)
	data := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
		{Name: "001.jpg", Data: payload, Method: testutil.MethodStore},
	}})
	ix, err := zipidx.ReadCentralDirectory(t.Context(), bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("ReadCentralDirectory: %v", err)
	}
	e := ix.Entries[0]
	if e.CompSize != e.Size {
		t.Fatalf("stored entry: comp size %d != size %d", e.CompSize, e.Size)
	}

	rc, err := zipidx.OpenEntry(t.Context(), bytes.NewReader(data), e.Ref())
	if err != nil {
		t.Fatalf("OpenEntry: %v", err)
	}
	defer func() { _ = rc.Close() }()

	rs, ok := rc.(io.ReadSeeker)
	if !ok {
		t.Fatalf("stored entry body is %T, want an io.ReadSeeker (Range support depends on it)", rc)
	}
	if _, err := rs.Seek(-16, io.SeekEnd); err != nil {
		t.Fatalf("Seek: %v", err)
	}
	tail, err := io.ReadAll(rs)
	if err != nil {
		t.Fatalf("reading the tail: %v", err)
	}
	if !bytes.Equal(tail, payload[len(payload)-16:]) {
		t.Errorf("tail = %q, want %q", tail, payload[len(payload)-16:])
	}
}

// A deflated entry is a forward-only stream. Asserting that keeps arch §5.3's
// "omit Accept-Ranges for deflate" honest: if this ever became seekable, the
// HTTP layer's Range policy would silently be wrong.
func TestOpenEntry_deflatedEntry_isForwardOnly(t *testing.T) {
	t.Parallel()
	data := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
		{Name: "001.jpg", Data: bytes.Repeat([]byte("deflate me "), 512), Method: testutil.MethodDeflate},
	}})
	ix, err := zipidx.ReadCentralDirectory(t.Context(), bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("ReadCentralDirectory: %v", err)
	}
	rc, err := zipidx.OpenEntry(t.Context(), bytes.NewReader(data), ix.Entries[0].Ref())
	if err != nil {
		t.Fatalf("OpenEntry: %v", err)
	}
	defer func() { _ = rc.Close() }()
	if _, ok := rc.(io.Seeker); ok {
		t.Errorf("deflated entry body is seekable (%T); arch §5.3 assumes it is not", rc)
	}
}

func TestOpenEntry_badRef_returnsTypedErrors(t *testing.T) {
	t.Parallel()
	data := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
		{Name: "001.jpg", Data: testutil.TinyJPEG(t, 8, 8), Method: testutil.MethodDeflate},
	}})
	ix, err := zipidx.ReadCentralDirectory(t.Context(), bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("ReadCentralDirectory: %v", err)
	}
	good := ix.Entries[0].Ref()

	cases := []struct {
		name string
		ref  archive.EntryRef
		want error
	}{
		{"offset points at nothing", archive.EntryRef{LocalHdrOff: 7, CompSize: 4}, zipidx.ErrBadLocalHeader},
		{"negative offset", archive.EntryRef{LocalHdrOff: -1, CompSize: 4}, zipidx.ErrBadLocalHeader},
		{"negative comp size", archive.EntryRef{LocalHdrOff: 0, CompSize: -1}, zipidx.ErrBadLocalHeader},
		{
			"unsupported method",
			archive.EntryRef{LocalHdrOff: good.LocalHdrOff, CompSize: good.CompSize, Method: 14},
			zipidx.ErrUnsupportedMethod,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := zipidx.OpenEntry(t.Context(), bytes.NewReader(data), tc.ref)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want it to wrap %v", err, tc.want)
			}
		})
	}

	// The unsupported-method message must name the method, per arch §4.11.
	_, err = zipidx.OpenEntry(t.Context(), bytes.NewReader(data),
		archive.EntryRef{LocalHdrOff: good.LocalHdrOff, CompSize: good.CompSize, Method: 14})
	if got := err.Error(); got != "zip: unsupported compression method 14 (LZMA)" {
		t.Errorf("message = %q, want %q", got, "zip: unsupported compression method 14 (LZMA)")
	}
	if got := archive.StatusOf(err); got != archive.StatusUnsupported {
		t.Errorf("status = %q, want %q", got, archive.StatusUnsupported)
	}
}

func TestOpenEntry_cancelledContext_returnsCtxErr(t *testing.T) {
	t.Parallel()
	data := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
		{Name: "001.jpg", Data: testutil.TinyJPEG(t, 8, 8)},
	}})
	ix, err := zipidx.ReadCentralDirectory(t.Context(), bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("ReadCentralDirectory: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := zipidx.OpenEntry(ctx, bytes.NewReader(data), ix.Entries[0].Ref()); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

// CON-004, at the level this package is responsible for: one io.ReaderAt,
// many concurrent readers, no shared cursor, no lock, no corruption.
func TestOpenEntry_concurrentReadsOfOneReaderAt(t *testing.T) {
	t.Parallel()
	const (
		goroutines = 8
		perReader  = 40
	)
	data, payloads := bigArchive(t, 32, 2048, testutil.MethodDeflate)
	src := bytes.NewReader(data)
	ix, err := zipidx.ReadCentralDirectory(t.Context(), src, int64(len(data)))
	if err != nil {
		t.Fatalf("ReadCentralDirectory: %v", err)
	}

	var wg sync.WaitGroup
	errCh := make(chan error, goroutines*perReader)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perReader; i++ {
				idx := (g*perReader + i) % len(ix.Entries)
				rc, err := zipidx.OpenEntry(t.Context(), src, ix.Entries[idx].Ref())
				if err != nil {
					errCh <- err
					return
				}
				got, err := io.ReadAll(rc)
				_ = rc.Close()
				if err != nil {
					errCh <- err
					return
				}
				if !bytes.Equal(got, payloads[idx]) {
					errCh <- errors.New("page " + ix.Entries[idx].Name + " came back corrupted")
					return
				}
			}
		}(g)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
}

// The 30-byte local-header read must be the only thing OpenEntry looks at
// outside the entry: a ZIP64 local extra field changes the header length, and
// getting that wrong shifts every byte of the payload.
func TestOpenEntry_zip64LocalExtra_dataOffsetIsCorrect(t *testing.T) {
	t.Parallel()
	payload := bytes.Repeat([]byte("zip64 payload "), 300)
	data := testutil.BuildZIP64(t, testutil.ZIPSpec{Entries: []testutil.Entry{
		{Name: "001.jpg", Data: payload, Method: testutil.MethodStore},
	}}, testutil.ZIP64Spec{LocalHeaders: true, IncludeDiskField: true})

	ix, err := zipidx.ReadCentralDirectory(t.Context(), bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("ReadCentralDirectory: %v", err)
	}
	e := ix.Entries[0]
	if extraLen := binary.LittleEndian.Uint16(data[e.LocalHdrOff+28:]); extraLen == 0 {
		t.Fatal("fixture has no local extra field; the test would prove nothing")
	}
	rc, err := zipidx.OpenEntry(t.Context(), bytes.NewReader(data), e.Ref())
	if err != nil {
		t.Fatalf("OpenEntry: %v", err)
	}
	got, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("payload differs; the local header's extra length was mishandled")
	}
}
