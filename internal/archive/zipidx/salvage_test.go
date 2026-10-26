package zipidx_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"slices"
	"strings"
	"testing"

	"shelf/internal/archive"
	"shelf/internal/archive/zipidx"
	"shelf/internal/kenc"
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
//
// A candidate only meets the scan once the chain has broken — an intact chain
// steps over every payload whole and never looks inside one. So each fixture
// punches a hole where 002's header was, which sends the resynchronising scan
// back through 001's payload, and plants there a header that is well formed in
// every field but one. Each row is therefore rejected by exactly one of
// parseLocalHeader's checks, and 003, past the candidate, proves the scan went
// all the way through.
func TestSalvage_signatureInsideData_isNotAnEntry(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		rawName []byte
	}{
		// The tell in random bytes nearly every time: a name length no
		// archiver writes. Every byte of it is printable, so the length is
		// the only thing wrong.
		{"name longer than any archiver writes", []byte(strings.Repeat("n", 596) + ".jpg")},
		// A control byte is part of no UTF-8, CP949 or Shift_JIS name.
		{"control bytes in the name", []byte("p\x00\x07ge.jpg")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			jpg := testutil.TinyJPEG(t, 16, 16)
			payload := bytes.Join([][]byte{
				jpg, forgedLocalHeader(tc.rawName, 64), bytes.Repeat([]byte{0x5a}, 64), jpg,
			}, nil)
			data := zipWithoutTail(t, []testutil.Entry{
				{Name: "001.jpg", Data: payload, Method: testutil.MethodStore},
				{Name: "002.jpg", Data: jpg, Method: testutil.MethodStore},
				{Name: "003.jpg", Data: jpg, Method: testutil.MethodStore},
			})
			at := localHeaderNamed(t, data, "002.jpg")
			clear(data[at : at+30+len("002.jpg")])

			got := names(salvageIndex(t, data))
			if want := []string{"001.jpg", "003.jpg"}; !slices.Equal(got, want) {
				t.Errorf("recovered %q, want %q — the candidate inside 001's payload "+
					"is not a header, and 003 lies past it", got, want)
			}
		})
	}
}

// findLocalHeader validates each candidate itself rather than handing it back
// to the walk, because in compressed data the round trip would dominate. That
// is a read-accounting claim, so it is checked as one: a damaged payload full
// of signature-like bytes must cost exactly the reads the same payload costs
// without them. A candidate handed back to the walk costs a fresh window to
// parse it and another to resume the scan.
func TestSalvage_signaturesInsideData_costNoExtraReads(t *testing.T) {
	t.Parallel()
	jpg := testutil.TinyJPEG(t, 16, 16)
	build := func(sig string) []byte {
		// 64 candidates in the shape random bytes take — the signature, then a
		// name length no archiver writes — all more than one 256 KiB window
		// before the end of the archive, where every window is a full one.
		var payload []byte
		for range 64 {
			payload = append(payload, sig...)
			payload = append(payload, bytes.Repeat([]byte{0xff}, 26)...)
			payload = append(payload, bytes.Repeat([]byte{0x5a}, 4<<10)...)
		}
		payload = append(payload, bytes.Repeat([]byte{0x5a}, 320<<10)...)
		data := zipWithoutTail(t, []testutil.Entry{
			{Name: "001.bin", Data: payload, Method: testutil.MethodStore},
			{Name: "002.jpg", Data: jpg, Method: testutil.MethodStore},
			{Name: "003.jpg", Data: jpg, Method: testutil.MethodStore},
		})
		at := localHeaderNamed(t, data, "002.jpg")
		clear(data[at : at+30+len("002.jpg")])
		return data
	}
	reads := func(data []byte) int64 {
		t.Helper()
		c := newCounter(data)
		ix, err := zipidx.ReadCentralDirectory(t.Context(), c, int64(len(data)))
		if !errors.Is(err, zipidx.ErrSalvagedFromLocalHeaders) || ix == nil {
			t.Fatalf("err = %v, want zipidx.ErrSalvagedFromLocalHeaders", err)
		}
		if got, want := names(ix), []string{"001.bin", "003.jpg"}; !slices.Equal(got, want) {
			t.Fatalf("recovered %q, want %q — the scan must cross 001's payload to reach 003", got, want)
		}
		return c.calls.Load()
	}

	clean := reads(build("QK\x03\x04"))
	planted := reads(build("PK\x03\x04"))
	if planted != clean {
		t.Errorf("%d reads with 64 signatures in the damaged payload, %d without — "+
			"a candidate cost a round trip through the walk", planted, clean)
	}
}

// A salvaged book must not get worse names than a healthy one (salvage.go):
// the names a local-header walk decodes one at a time are settled against the
// whole set, exactly as a directory read settles them. Shift_JIS is the shape
// where that decides the answer — `天上天下 20.zip` reads as CP949 one name at
// a time, and wrongly.
func TestSalvage_shiftJISNames_areDecidedForTheWholeArchive(t *testing.T) {
	t.Parallel()
	page := testutil.TinyJPEG(t, 8, 8)
	data := zipWithoutTail(t, []testutil.Entry{
		{RawName: sjisAmbiguous, Data: page, Method: testutil.MethodDeflate},
		{RawName: sjisKana, Data: page, Method: testutil.MethodDeflate},
		// A flagged UTF-8 name is outside the archive-level decision.
		{Name: "표지.jpg", Data: page, Method: testutil.MethodStore, Flags: testutil.FlagUTF8},
	})

	ix := salvageIndex(t, data)
	want := []struct{ name, enc string }{
		{"tenjou tenge v20/天天-20-001.jpg", kenc.EncCP932},
		{"tenjou tenge v20/天天-20-008 のコピー2.jpg", kenc.EncCP932},
		{"표지.jpg", kenc.EncUTF8},
	}
	if len(ix.Entries) != len(want) {
		t.Fatalf("recovered %q, want %d entries", names(ix), len(want))
	}
	for i, e := range ix.Entries {
		if e.Name != want[i].name || e.NameEncoding != want[i].enc {
			t.Errorf("entry %d = (%q, %q), want (%q, %q)", i, e.Name, e.NameEncoding, want[i].name, want[i].enc)
		}
	}
}

// forgedLocalHeader is a local file header no archiver wrote — four signature
// bytes inside some payload, at their most convincing. Every field but the name
// is plausible: stored, no flags, a payload of compSize bytes that fits.
func forgedLocalHeader(rawName []byte, compSize uint32) []byte {
	var b bytes.Buffer
	put32(&b, 0x04034b50)
	put16(&b, 20)     // version needed to extract
	put16(&b, 0)      // flags
	put16(&b, 0)      // method: stored
	put16(&b, 0)      // modification time
	put16(&b, 0x4821) // modification date: 2016-01-01
	put32(&b, 0xdeadbeef)
	put32(&b, compSize)
	put32(&b, compSize)
	put16(&b, uint16(len(rawName)))
	put16(&b, 0) // extra length
	b.Write(rawName)
	return b.Bytes()
}

// localHeaderNamed returns the offset of the local header carrying name.
// Counting signatures would not do: these fixtures plant extra ones.
func localHeaderNamed(t *testing.T, data []byte, name string) int {
	t.Helper()
	i := bytes.Index(data, []byte(name))
	at := i - 30
	if i < 0 || at < 0 || !bytes.HasPrefix(data[at:], []byte("PK\x03\x04")) {
		t.Fatalf("fixture: no local header named %q", name)
	}
	return at
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
