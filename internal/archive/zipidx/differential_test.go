package zipidx_test

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"shelf/internal/archive/zipidx"
	"shelf/internal/testutil"
)

// The differential oracle (decision E-2, impl-plan WP-04 acceptance 3).
//
// Shipping our own ZIP reader is a deviation from prd 6.1 that was accepted
// because archive/zip cannot expose a local-header offset. The price of that
// deviation is this file: for every archive either implementation can open,
// the two must agree on the entry count, the names, the method, the CRC, both
// sizes and — the field that only exists because we wrote our own — the data
// offset. And for every archive they cannot open, they must agree that it is
// broken.
//
// arch §4.3 records the same check run over all 11 157 real archives: the same
// 9 fail in both implementations, with no case where one succeeds and the
// other does not. This test is what keeps that true.

func TestDifferential_corpus_matchesArchiveZip(t *testing.T) {
	t.Parallel()
	for _, fx := range corpus(t) {
		t.Run(fx.name, func(t *testing.T) {
			t.Parallel()
			compareWithOracle(t, fx.name, bytes.NewReader(fx.data), int64(len(fx.data)))
		})
	}
}

func TestDifferential_frozenTestdata_matchesArchiveZip(t *testing.T) {
	t.Parallel()
	names, err := filepath.Glob(filepath.Join("testdata", "*.zip"))
	if err != nil {
		t.Fatalf("globbing testdata: %v", err)
	}
	if len(names) == 0 {
		t.Fatal("no frozen fixtures under testdata/")
	}
	for _, name := range names {
		t.Run(filepath.Base(name), func(t *testing.T) {
			t.Parallel()
			data, err := os.ReadFile(name)
			if err != nil {
				t.Fatalf("reading %s: %v", name, err)
			}
			compareWithOracle(t, filepath.Base(name), bytes.NewReader(data), int64(len(data)))
		})
	}
}

// compareWithOracle is the whole contract, in one function so that the unit
// corpus and the real-collection integration run share it exactly.
//
// It takes an io.ReaderAt rather than a []byte so the integration run can hand
// it a 1.34 GB archive without loading it (NFR-PRF-006 applies to the test
// suite too).
func compareWithOracle(t testing.TB, name string, r io.ReaderAt, size int64) {
	t.Helper()

	oracle, oracleErr := zip.NewReader(r, size)
	// ErrInsecurePath is advisory — the reader is still usable and the entries
	// are still readable — so it does not count as a refusal.
	if errors.Is(oracleErr, zip.ErrInsecurePath) {
		oracleErr = nil
	}
	ix, ourErr := zipidx.ReadCentralDirectory(t.Context(), r, size)

	// 1. The verdicts must agree.
	if (oracleErr != nil) != (ourErr != nil) {
		t.Fatalf("%s: verdict mismatch — archive/zip err = %v, zipidx err = %v", name, oracleErr, ourErr)
	}
	if oracleErr != nil {
		return
	}

	// 2. The entry lists must agree, in order.
	if len(ix.Entries) != len(oracle.File) {
		t.Fatalf("%s: entry count = %d, archive/zip says %d", name, len(ix.Entries), len(oracle.File))
	}
	for i, want := range oracle.File {
		got := ix.Entries[i]

		// archive/zip hands back the raw name bytes as a Go string without
		// decoding them, so the reader is compared on those bytes and on the
		// flags that say how to read them. The display name is a decision about
		// the whole archive (resolveArchiveNames), owned by internal/kenc's
		// tests and by this package's encoding tests. Recomputing it here one
		// entry at a time would repeat the code under test, and would disagree
		// with it on every Shift_JIS archive. So the display name is pinned
		// only where the archive has no say: a name that is valid UTF-8,
		// flagged or not.
		if string(got.RawName) != want.Name {
			t.Errorf("%s entry %d: raw name = %q, archive/zip says %q", name, i, got.RawName, want.Name)
		}
		if got.Flags != want.Flags {
			t.Errorf("%s entry %q: flags = %#04x, archive/zip says %#04x", name, want.Name, got.Flags, want.Flags)
		}
		if nameNeedsNoDecision(want) && got.Name != want.Name {
			t.Errorf("%s entry %d: name = %q, want the raw name %q unchanged", name, i, got.Name, want.Name)
		}
		if got.Method != want.Method {
			t.Errorf("%s entry %q: method = %d, archive/zip says %d", name, want.Name, got.Method, want.Method)
		}
		if got.CRC32 != want.CRC32 {
			t.Errorf("%s entry %q: crc32 = %#08x, archive/zip says %#08x", name, want.Name, got.CRC32, want.CRC32)
		}
		if uint64(got.Size) != want.UncompressedSize64 {
			t.Errorf("%s entry %q: size = %d, archive/zip says %d", name, want.Name, got.Size, want.UncompressedSize64)
		}
		if uint64(got.CompSize) != want.CompressedSize64 {
			t.Errorf("%s entry %q: comp size = %d, archive/zip says %d", name, want.Name, got.CompSize, want.CompressedSize64)
		}

		// 3. DataOffset parity — the field FR-SRV-002 needs and archive/zip
		//    only exposes after the fact. The verdicts are compared first, as
		//    in step 1: both refusing — as both do where the central directory
		//    points at zeros — is agreement, and one refusing is not
		//    (checkOffset, TestDifferential_zeroedLocalHeader_isRefusedByBoth).
		wantOff, wantErr := want.DataOffset()
		gotOff, gotErr := zipidx.DataOffset(t.Context(), r, got.LocalHdrOff)
		checkOffset(t, name, want.Name, wantOff, wantErr, gotOff, gotErr)
	}

	if oracle.Comment != ix.Comment {
		t.Errorf("%s: comment length = %d, archive/zip says %d", name, len(ix.Comment), len(oracle.Comment))
	}
}

// checkOffset compares the two readers' answers for one local header, the way
// step 1 compares their answers for a whole archive: both refusing is agreement
// and leaves a log line, so a real round still shows how many entries it met
// damaged; one refusing is a failure, and two offsets must be the same offset.
func checkOffset(t testing.TB, name, entry string, wantOff int64, wantErr error, gotOff int64, gotErr error) {
	t.Helper()
	switch {
	case wantErr != nil && gotErr != nil:
		t.Logf("%s entry %q: both refuse the local header — archive/zip: %v; zipidx: %v",
			name, entry, wantErr, gotErr)
	case wantErr != nil || gotErr != nil:
		t.Errorf("%s entry %q: local header verdict mismatch — archive/zip err = %v, zipidx err = %v",
			name, entry, wantErr, gotErr)
	case gotOff != wantOff:
		t.Errorf("%s entry %q: data offset = %d, archive/zip says %d", name, entry, gotOff, wantOff)
	}
}

// spy is a testing.TB that keeps what a comparison reports instead of acting on
// it, so a test can assert which of Errorf and Logf it reached. Everything else
// — Fatalf, Context, Helper — is the real T's.
type spy struct {
	*testing.T
	errs, logs []string
}

func (s *spy) Errorf(format string, args ...any) {
	s.errs = append(s.errs, fmt.Sprintf(format, args...))
}
func (s *spy) Logf(format string, args ...any) { s.logs = append(s.logs, fmt.Sprintf(format, args...)) }

// nameNeedsNoDecision reports whether f's name is settled before any decision
// about the whole archive is made. A name that is valid UTF-8 is read as UTF-8
// whether or not bit 11 says so (arch §4.4 step 2), and the archive-level pass
// neither counts it as evidence nor decodes it again (§4.4.1) — so it comes
// back as its raw bytes whatever the other names are in. ASCII is the commonest
// case of it.
func nameNeedsNoDecision(f *zip.File) bool {
	return utf8.ValidString(f.Name)
}

// The four Shift_JIS archives in the collection reach compareWithOracle in the
// integration run like every other archive, and theirs are the names decided
// for the whole archive rather than one at a time. The oracle has to hold on
// them too, without reaching into the decision it does not own.
//
// The decision has an edge the oracle does pin: the names it must not reach.
// "あとがき.jpg" is UTF-8 without the flag, and its bytes are ones Shift_JIS
// also reads cleanly, as "縺ゅ→縺後″.jpg" — so an archive decided as Shift_JIS
// that decoded it again would serve that instead.
func TestDifferential_shiftJISArchive_matchesArchiveZip(t *testing.T) {
	t.Parallel()
	page := testutil.TinyJPEG(t, 8, 8)
	data := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
		{RawName: sjisAmbiguous, Data: page, Method: testutil.MethodDeflate},
		{RawName: sjisKana, Data: page, Method: testutil.MethodDeflate},
		{Name: "표지.jpg", Data: page, Method: testutil.MethodStore, Flags: testutil.FlagUTF8},
		{Name: "あとがき.jpg", Data: page, Method: testutil.MethodStore},
		{Name: "001.jpg", Data: page, Method: testutil.MethodStore},
	}})
	compareWithOracle(t, "shift_jis", bytes.NewReader(data), int64(len(data)))
}

// A central directory can point at a local header that has been zeroed. Two
// volumes of the real collection have that: in 우에키의 법칙 13 (entries #1–#3)
// and ＋ 02 (#88–#89) the 64 bytes at each recorded offset are all zero, and
// the first sampled integration run (E-66) drew both. Both readers refuse such
// an entry, and both refusing is agreement, as a refused archive is in step 1 —
// the comparison must not fail on the oracle's refusal without asking zipidx.
func TestDifferential_zeroedLocalHeader_isRefusedByBoth(t *testing.T) {
	t.Parallel()
	page := testutil.TinyJPEG(t, 8, 8)
	data := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
		{Name: "001.jpg", Data: page, Method: testutil.MethodStore},
		{Name: "002.jpg", Data: page, Method: testutil.MethodDeflate},
		{Name: "003.jpg", Data: page, Method: testutil.MethodStore},
	}})
	ix, err := zipidx.ReadCentralDirectory(t.Context(), bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	off := ix.Entries[1].LocalHdrOff
	if !bytes.HasPrefix(data[off:], []byte("PK\x03\x04")) {
		t.Fatalf("no local header at %d to zero; the fixture is not the shape it claims", off)
	}
	clear(data[off : off+30]) // the fixed part of a local header
	oracle, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := oracle.File[1].DataOffset(); err == nil {
		t.Fatal("archive/zip reads a zeroed local header; the fixture refuses nothing")
	}
	s := &spy{T: t}
	compareWithOracle(s, "zeroed local header", bytes.NewReader(data), int64(len(data)))
	if len(s.errs) != 0 || len(s.logs) != 1 || !strings.Contains(s.logs[0], `entry "002.jpg": both refuse`) {
		t.Errorf("errors %q, logs %q; want no error and one both-refuse line for 002.jpg", s.errs, s.logs)
	}
}

// The rule compareWithOracle applies to every local header, on its own, down
// to which of Errorf and Logf it reaches: the zeroed-header fixture above can
// only show the both-refuse row, because the product refuses what archive/zip
// refuses. A rule — or a report — that let one side's refusal pass would hide
// exactly the divergence the oracle is for.
func TestCheckOffset(t *testing.T) {
	t.Parallel()
	bad := errors.New("zip: not a valid zip file")
	for _, tc := range []struct {
		name            string
		wantOff, gotOff int64
		wantErr, gotErr error
		errs, logs      int
	}{
		{"both read the same offset", 30, 30, nil, nil, 0, 0},
		{"both read, different offsets", 30, 31, nil, nil, 1, 0},
		{"both refuse", 0, 0, bad, bad, 0, 1},
		// The refusing side's offset is set equal to the other's, so the
		// failure has to come from the refusal and not from the offsets.
		{"only archive/zip refuses", 30, 30, bad, nil, 1, 0},
		{"only zipidx refuses", 30, 30, nil, bad, 1, 0},
	} {
		s := &spy{T: t}
		checkOffset(s, "x.zip", "001.jpg", tc.wantOff, tc.wantErr, tc.gotOff, tc.gotErr)
		if len(s.errs) != tc.errs || len(s.logs) != tc.logs {
			t.Errorf("%s: errors %q, logs %q; want %d error(s), %d log line(s)", tc.name, s.errs, s.logs, tc.errs, tc.logs)
		}
	}
}

// The bytes we stream must be the bytes archive/zip streams, for every entry
// it can decompress. This is FR-SRV-008 — original bytes, no re-encoding —
// checked against an implementation nobody can accuse us of having written.
func TestDifferential_entryBytes_matchArchiveZip(t *testing.T) {
	t.Parallel()
	for _, fx := range corpus(t) {
		if fx.wantErr || strings.HasPrefix(fx.name, "encrypted") {
			continue
		}
		t.Run(fx.name, func(t *testing.T) {
			t.Parallel()
			r := bytes.NewReader(fx.data)
			oracle, err := zip.NewReader(r, int64(len(fx.data)))
			if errors.Is(err, zip.ErrInsecurePath) {
				err = nil
			}
			if err != nil {
				t.Skipf("archive/zip refuses this fixture: %v", err)
			}
			ix, err := zipidx.ReadCentralDirectory(t.Context(), r, int64(len(fx.data)))
			if err != nil {
				t.Fatalf("ReadCentralDirectory: %v", err)
			}
			for i, want := range oracle.File {
				if want.FileInfo().IsDir() {
					continue
				}
				wantRC, err := want.Open()
				if err != nil {
					t.Fatalf("archive/zip open %q: %v", want.Name, err)
				}
				wantBytes, err := io.ReadAll(wantRC)
				_ = wantRC.Close()
				if err != nil {
					t.Fatalf("archive/zip read %q: %v", want.Name, err)
				}

				gotRC, err := zipidx.OpenEntry(t.Context(), r, ix.Entries[i].Ref())
				if err != nil {
					t.Fatalf("zipidx open %q: %v", want.Name, err)
				}
				gotBytes, err := io.ReadAll(gotRC)
				_ = gotRC.Close()
				if err != nil {
					t.Fatalf("zipidx read %q: %v", want.Name, err)
				}
				if !bytes.Equal(gotBytes, wantBytes) {
					t.Fatalf("entry %q: %d bytes differ from archive/zip's %d",
						want.Name, len(gotBytes), len(wantBytes))
				}
				if sum := crc32.ChecksumIEEE(gotBytes); sum != ix.Entries[i].CRC32 {
					t.Errorf("entry %q: crc32 of the stream = %#08x, central directory says %#08x",
						want.Name, sum, ix.Entries[i].CRC32)
				}
			}
		})
	}
}
