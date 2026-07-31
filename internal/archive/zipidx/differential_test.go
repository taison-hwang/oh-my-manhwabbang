package zipidx_test

import (
	"archive/zip"
	"bytes"
	"errors"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"shelf/internal/archive/zipidx"
	"shelf/internal/kenc"
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
func compareWithOracle(t *testing.T, name string, r io.ReaderAt, size int64) {
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
		// decoding them; kenc is what turns those into a display name. Compare
		// on the pre-decode bytes so the comparison is about the *reader*, not
		// about the encoding rule (which internal/kenc tests own).
		wantName, _ := kenc.DecodeEntryName([]byte(want.Name), !want.NonUTF8)
		if got.Name != wantName {
			t.Errorf("%s entry %d: name = %q, archive/zip + kenc says %q", name, i, got.Name, wantName)
		}
		if string(got.RawName) != want.Name {
			t.Errorf("%s entry %d: raw name = %q, archive/zip says %q", name, i, got.RawName, want.Name)
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
		//    only exposes after the fact.
		wantOff, err := want.DataOffset()
		if err != nil {
			t.Errorf("%s entry %q: archive/zip DataOffset: %v", name, want.Name, err)
			continue
		}
		gotOff, err := zipidx.DataOffset(t.Context(), r, got.LocalHdrOff)
		if err != nil {
			t.Errorf("%s entry %q: zipidx DataOffset: %v", name, want.Name, err)
			continue
		}
		if gotOff != wantOff {
			t.Errorf("%s entry %q: data offset = %d, archive/zip says %d", name, want.Name, gotOff, wantOff)
		}
	}

	if oracle.Comment != ix.Comment {
		t.Errorf("%s: comment length = %d, archive/zip says %d", name, len(ix.Comment), len(oracle.Comment))
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
