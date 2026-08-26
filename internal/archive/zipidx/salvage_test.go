package zipidx_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"strings"
	"testing"

	"shelf/internal/archive"
	"shelf/internal/archive/zipidx"
	"shelf/internal/testutil"
)

// The local-header salvage (salvage.go, FR-IDX-010).
//
// Every fixture here is one of the four damage shapes the real collection
// actually holds, measured over its nine damaged archives:
//
//	tail overwritten with zeroes  3 archives
//	tail cut off                  3
//	mid-file damage, tail lost    2
//	zero bytes                    1
//
// The third shape is the one that decides the design. A chain walk alone stops
// at the damaged payload and loses everything after it — 3 of 91 entries in
// `유레카26.zip`, 16 of 147 in `최종병기그녀 06권.zip`. A signature scan alone
// invents entries out of compressed data. The walk has to be both, and these
// tests are what hold it to that.

// zipWithoutTail returns a well-formed archive with its central directory and
// end record removed: the "download stopped early" shape.
func zipWithoutTail(t *testing.T, entries []testutil.Entry) []byte {
	t.Helper()
	full := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: entries})
	cd := bytes.Index(full, []byte("PK\x01\x02"))
	if cd <= 0 {
		t.Fatalf("fixture has no central directory to remove")
	}
	return full[:cd]
}

func salvageIndex(t *testing.T, data []byte) *archive.Index {
	t.Helper()
	ix, err := zipidx.ReadCentralDirectory(t.Context(), bytes.NewReader(data), int64(len(data)))
	if !errors.Is(err, zipidx.ErrSalvagedFromLocalHeaders) {
		t.Fatalf("err = %v, want zipidx.ErrSalvagedFromLocalHeaders", err)
	}
	if ix == nil {
		t.Fatal("got no index, want the entries recovered from local headers")
	}
	return ix
}

func names(ix *archive.Index) []string {
	out := make([]string, 0, len(ix.Entries))
	for _, e := range ix.Entries {
		out = append(out, e.Name)
	}
	return out
}

// A book whose directory is gone is still a book. This is the whole feature in
// one test: the pages come back, and the verdict stays "damaged".
func TestSalvage_missingDirectory_recoversEntriesAndStaysDamaged(t *testing.T) {
	t.Parallel()
	jpg := testutil.TinyJPEG(t, 16, 16)
	data := zipWithoutTail(t, []testutil.Entry{
		{Name: "001.jpg", Data: jpg, Method: testutil.MethodStore},
		{Name: "002.jpg", Data: jpg, Method: testutil.MethodDeflate},
		{Name: "003.jpg", Data: jpg, Method: testutil.MethodDeflate},
	})

	ix := salvageIndex(t, data)
	if got := names(ix); len(got) != 3 {
		t.Fatalf("recovered %v, want all three entries", got)
	}
	_, err := zipidx.ReadCentralDirectory(t.Context(), bytes.NewReader(data), int64(len(data)))
	if got := archive.StatusOf(err); got != archive.StatusError {
		t.Errorf("status = %q, want %q — salvage never makes a book healthy", got, archive.StatusError)
	}
	if !strings.Contains(err.Error(), "3 entries recovered") {
		t.Errorf("err = %v, want it to report how many entries were recovered", err)
	}
}

// Recovered geometry has to be right, not just present: a local-header offset
// that is off by one byte produces an entry that lists fine and serves garbage.
// So the recovered entries are read back through the ordinary serving path and
// checked against the bytes that went in.
func TestSalvage_recoveredEntries_serveTheOriginalBytes(t *testing.T) {
	t.Parallel()
	want := map[string][]byte{
		"001.jpg": testutil.TinyJPEG(t, 8, 8),
		"002.jpg": testutil.TinyJPEG(t, 24, 12),
		"003.jpg": testutil.TinyJPEG(t, 16, 16),
	}
	data := zipWithoutTail(t, []testutil.Entry{
		{Name: "001.jpg", Data: want["001.jpg"], Method: testutil.MethodStore},
		{Name: "002.jpg", Data: want["002.jpg"], Method: testutil.MethodDeflate},
		{Name: "003.jpg", Data: want["003.jpg"], Method: testutil.MethodDeflate},
	})

	r := bytes.NewReader(data)
	ix := salvageIndex(t, data)
	for _, e := range ix.Entries {
		rc, err := zipidx.OpenEntry(t.Context(), r, e.Ref())
		if err != nil {
			t.Fatalf("opening salvaged %q: %v", e.Name, err)
		}
		got, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatalf("reading salvaged %q: %v", e.Name, err)
		}
		if !bytes.Equal(got, want[e.Name]) {
			t.Errorf("%s: %d bytes served, want the original %d", e.Name, len(got), len(want[e.Name]))
		}
		if sum := crc32.ChecksumIEEE(got); sum != e.CRC32 {
			t.Errorf("%s: crc32 %#08x, local header says %#08x", e.Name, sum, e.CRC32)
		}
	}
}

// The shape a chain walk cannot survive: damage in the middle, intact entries
// after it. 2 of the collection's 9 archives look like this, and between them
// they hold 19 entries that live past the break.
func TestSalvage_damageMidFile_resyncsToTheEntriesAfterIt(t *testing.T) {
	t.Parallel()
	jpg := testutil.TinyJPEG(t, 16, 16)
	data := zipWithoutTail(t, []testutil.Entry{
		{Name: "001.jpg", Data: jpg, Method: testutil.MethodStore},
		{Name: "002.jpg", Data: jpg, Method: testutil.MethodStore},
		{Name: "003.jpg", Data: jpg, Method: testutil.MethodStore},
		{Name: "004.jpg", Data: jpg, Method: testutil.MethodStore},
	})
	// Corrupt the length field of 002's local header so the chain walks off
	// into its payload and cannot find 003 by following it.
	at := nthLocalHeader(t, data, 1)
	data[at+18] ^= 0xff // compressed size, low byte

	ix := salvageIndex(t, data)
	got := names(ix)
	if len(got) < 3 {
		t.Fatalf("recovered %v, want the entries after the break too — a chain "+
			"walk alone would stop at 002", got)
	}
	for _, want := range []string{"003.jpg", "004.jpg"} {
		if !contains(got, want) {
			t.Errorf("recovered %v, want it to include %s from past the break", got, want)
		}
	}
}

// A tail overwritten with zeroes is the commonest shape here (3 of 9). It
// differs from a cut tail in that the file keeps its original length, so the
// walk has to stop on content rather than on EOF.
func TestSalvage_zeroFilledTail_recoversWhatPrecedesIt(t *testing.T) {
	t.Parallel()
	jpg := testutil.TinyJPEG(t, 16, 16)
	body := zipWithoutTail(t, []testutil.Entry{
		{Name: "001.jpg", Data: jpg, Method: testutil.MethodDeflate},
		{Name: "002.jpg", Data: jpg, Method: testutil.MethodDeflate},
	})
	data := append(body, make([]byte, 300_000)...)

	if got := names(salvageIndex(t, data)); len(got) != 2 {
		t.Errorf("recovered %v, want both entries that precede the zero fill", got)
	}
}

// Four bytes matching the local signature occur inside compressed data, and a
// scan that trusts them lists an entry whose geometry is noise. Serving that
// entry hands the reader garbage, so it must never reach the index.
func TestSalvage_signatureInsideData_isNotAnEntry(t *testing.T) {
	t.Parallel()
	jpg := testutil.TinyJPEG(t, 16, 16)
	// A stored entry whose payload literally contains the signature, twice.
	payload := bytes.Join([][]byte{
		jpg, []byte("PK\x03\x04"), bytes.Repeat([]byte{0xde, 0xad}, 64),
		[]byte("PK\x03\x04"), jpg,
	}, nil)
	data := zipWithoutTail(t, []testutil.Entry{
		{Name: "001.jpg", Data: payload, Method: testutil.MethodStore},
		{Name: "002.jpg", Data: jpg, Method: testutil.MethodStore},
	})

	got := names(salvageIndex(t, data))
	if len(got) != 2 {
		t.Errorf("recovered %v, want exactly the two real entries — the "+
			"signatures inside 001's payload are not headers", got)
	}
}

// The last entry of a cut-short archive has only some of its bytes. `zip -FF`
// drops it and so do we: half a JPEG rendered as a page is worse than a page
// that is honestly absent.
func TestSalvage_truncatedFinalEntry_isDropped(t *testing.T) {
	t.Parallel()
	jpg := testutil.TinyJPEG(t, 32, 32)
	body := zipWithoutTail(t, []testutil.Entry{
		{Name: "001.jpg", Data: jpg, Method: testutil.MethodStore},
		{Name: "002.jpg", Data: jpg, Method: testutil.MethodStore},
	})
	// Cut into the middle of 002's payload.
	data := body[:len(body)-len(jpg)/2]

	got := names(salvageIndex(t, data))
	if len(got) != 1 || got[0] != "001.jpg" {
		t.Errorf("recovered %v, want only the entry whose bytes are all present", got)
	}
}

// Bit 3 puts the sizes after the payload. Without the central directory there
// is nowhere to read them from, and FR-IDX-002 forbids inflating to find out —
// so such an entry is skipped rather than listed with a zero length.
func TestSalvage_dataDescriptorEntry_isSkippedNotListedEmpty(t *testing.T) {
	t.Parallel()
	jpg := testutil.TinyJPEG(t, 16, 16)
	data := zipWithoutTail(t, []testutil.Entry{
		{Name: "001.jpg", Data: jpg, Method: testutil.MethodStore},
		{Name: "002.jpg", Data: jpg, Method: testutil.MethodStore},
	})
	// Turn 001 into a streamed entry: set bit 3 and zero the header's sizes.
	at := nthLocalHeader(t, data, 0)
	data[at+6] |= 0x08
	for i := 14; i < 26; i++ {
		data[at+i] = 0
	}

	ix := salvageIndex(t, data)
	for _, e := range ix.Entries {
		if e.Name == "001.jpg" {
			t.Errorf("listed %q with comp size %d; a streamed entry has no "+
				"knowable length without the directory", e.Name, e.CompSize)
		}
		if e.CompSize == 0 && !e.Dir {
			t.Errorf("listed %q as a zero-length file", e.Name)
		}
	}
	if !contains(names(ix), "002.jpg") {
		t.Errorf("recovered %v, want the walk to resync past the streamed entry", names(ix))
	}
}

// Nothing recoverable must behave exactly as it did before the fallback
// existed, or the 0-byte archive in the collection changes verdict for no
// reason and a non-ZIP file starts reporting a ZIP problem.
func TestSalvage_nothingToRecover_keepsTheOriginalVerdict(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"empty", nil},
		{"short", []byte("PK")},
		{"zeroes", bytes.Repeat([]byte{0x00}, 4096)},
		{"not a zip", bytes.Repeat([]byte("plain text, no headers here. "), 200)},
		{"signature only", []byte("PK\x03\x04")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ix, err := zipidx.ReadCentralDirectory(t.Context(), bytes.NewReader(tc.data), int64(len(tc.data)))
			if ix != nil {
				t.Errorf("got %d entries, want none", len(ix.Entries))
			}
			if !errors.Is(err, zipidx.ErrNoEOCD) {
				t.Errorf("err = %v, want the unchanged zipidx.ErrNoEOCD", err)
			}
			if errors.Is(err, zipidx.ErrSalvagedFromLocalHeaders) {
				t.Errorf("err = %v, want no salvage claimed when nothing was recovered", err)
			}
		})
	}
}

// A healthy archive must not touch the fallback at all: the salvage path is
// slower, and reaching it would mean the ordinary read had silently failed.
func TestSalvage_healthyArchive_neverSalvages(t *testing.T) {
	t.Parallel()
	jpg := testutil.TinyJPEG(t, 16, 16)
	data := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
		{Name: "001.jpg", Data: jpg, Method: testutil.MethodDeflate},
		{Name: "002.jpg", Data: jpg, Method: testutil.MethodStore},
	}})
	ix, err := zipidx.ReadCentralDirectory(t.Context(), bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("a healthy archive must read cleanly: %v", err)
	}
	if len(ix.Entries) != 2 {
		t.Errorf("entries = %d, want 2", len(ix.Entries))
	}
}

// The walk is the one place that reads a whole archive front-to-back, so it is
// the one place a cancelled scan could keep working after the user pressed
// stop.
func TestSalvage_cancelledContext_stops(t *testing.T) {
	t.Parallel()
	jpg := testutil.TinyJPEG(t, 16, 16)
	data := zipWithoutTail(t, []testutil.Entry{
		{Name: "001.jpg", Data: jpg, Method: testutil.MethodStore},
		{Name: "002.jpg", Data: jpg, Method: testutil.MethodStore},
	})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	ix, err := zipidx.ReadCentralDirectory(ctx, bytes.NewReader(data), int64(len(data)))
	if ix != nil && len(ix.Entries) > 0 {
		t.Errorf("recovered %d entries from a cancelled context", len(ix.Entries))
	}
	if err == nil {
		t.Error("err = nil, want the cancellation reported")
	}
}

// nthLocalHeader returns the offset of the n-th local file header (0-based).
func nthLocalHeader(t *testing.T, data []byte, n int) int {
	t.Helper()
	at := -1
	for i := 0; i <= n; i++ {
		next := bytes.Index(data[at+1:], []byte("PK\x03\x04"))
		if next < 0 {
			t.Fatalf("fixture has fewer than %d local headers", n+1)
		}
		at += 1 + next
	}
	return at
}

func contains(hay []string, needle string) bool {
	for _, s := range hay {
		if s == needle {
			return true
		}
	}
	return false
}

// The walk reads through a 256 KiB window, and both of its loops have an edge
// there. A header landing near a window's end must not be lost to the boundary,
// and the scan must not read past the buffer looking for one — the fuzzer found
// the second as a panic on a 30-byte input ending in `P`, and the first is the
// silent version of the same mistake: entries that simply stop being listed.
//
// So this fixture spans several windows and puts entries at every offset a
// boundary can fall on, then insists every one of them comes back.
func TestSalvage_entriesAcrossWindowBoundaries_areAllFound(t *testing.T) {
	t.Parallel()
	// ~40 KiB per entry over 24 entries is a little under 1 MiB: four windows,
	// with the odd name lengths making the boundaries land mid-header.
	entries := make([]testutil.Entry, 0, 24)
	for i := range 24 {
		entries = append(entries, testutil.Entry{
			Name:   fmt.Sprintf("chapter-%02d/page_%03d.jpg", i, i),
			Data:   bytes.Repeat([]byte{byte(i), 0x5a, 0x0f}, 13_000+i*37),
			Method: testutil.MethodStore,
		})
	}
	data := zipWithoutTail(t, entries)
	if len(data) < 3*256<<10 {
		t.Fatalf("fixture is %d bytes, want it to span several 256 KiB windows", len(data))
	}

	got := names(salvageIndex(t, data))
	if len(got) != len(entries) {
		t.Errorf("recovered %d entries, want all %d — a header near a window "+
			"edge was dropped", len(got), len(entries))
	}
	for _, want := range entries {
		if !contains(got, want.Name) {
			t.Errorf("recovered %d entries but not %s", len(got), want.Name)
		}
	}
}

// A tiny archive that ends just after a lone signature byte is the shape the
// fuzzer minimised to. It must be a verdict, not a panic.
func TestSalvage_truncatedInsideASignature_doesNotPanic(t *testing.T) {
	t.Parallel()
	for _, data := range [][]byte{
		append(bytes.Repeat([]byte("0"), 29), 'P'),
		append(bytes.Repeat([]byte("0"), 28), 'P', 'K'),
		append(bytes.Repeat([]byte("0"), 27), 'P', 'K', 0x03),
		append(bytes.Repeat([]byte("0"), 26), 'P', 'K', 0x03, 0x04),
	} {
		ix, err := zipidx.ReadCentralDirectory(t.Context(), bytes.NewReader(data), int64(len(data)))
		if err == nil {
			t.Errorf("%q: err = nil, want a verdict", data[len(data)-4:])
		}
		if ix != nil {
			t.Errorf("%q: got %d entries out of noise", data[len(data)-4:], len(ix.Entries))
		}
	}
}
