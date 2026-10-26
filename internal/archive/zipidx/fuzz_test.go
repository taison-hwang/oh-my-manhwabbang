package zipidx_test

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"shelf/internal/archive"
	"shelf/internal/archive/zipidx"
	"shelf/internal/testutil"
)

// The reader runs against 11 157 archives written by a decade of Korean
// archivers, 9 of which are interrupted downloads. "Corrupt input returns an
// error, never a panic" is therefore a property of the product, not a nicety —
// a panic in the scanner takes the whole scan down and FR-IDX-010 forbids that.
//
// Run longer with:
//
//	go test ./internal/archive/zipidx -run '^$' -fuzz FuzzReadCentralDirectory -fuzztime 60s

func FuzzReadCentralDirectory(f *testing.F) {
	for _, fx := range corpus(f) {
		f.Add(fx.data)
	}
	// Hand-picked shapes that exercise the arithmetic rather than the parser:
	// a lone end record, and end records with hostile size/offset fields.
	f.Add(eocd(0, 0, 0, nil))
	f.Add(eocd(0xffff, 0xffffffff, 0xffffffff, nil))
	f.Add(eocd(1, 0xffffffff, 0, nil))
	f.Add(eocd(1, 0, 0xffffffff, nil))
	f.Add(eocd(1, 22, 0, nil))
	f.Add(eocd(65535, 0, 0, bytes.Repeat([]byte("c"), 300)))

	f.Fuzz(func(t *testing.T, data []byte) {
		ix, err := zipidx.ReadCentralDirectory(t.Context(), bytes.NewReader(data), int64(len(data)))
		if err != nil {
			// Every failure must classify. An unclassifiable error would land
			// in books.error as a bare Go string with no status.
			if s := archive.StatusOf(err); s == archive.StatusOK {
				t.Fatalf("error %v classified as %q", err, s)
			}
		}
		if ix == nil {
			return
		}
		for _, e := range ix.Entries {
			// Whatever the input, an entry must be self-consistent enough that
			// the serving path cannot be tricked into a huge allocation or a
			// negative offset.
			if e.CompSize < 0 || e.Size < 0 || e.LocalHdrOff < 0 {
				t.Fatalf("entry %q has negative geometry: off=%d comp=%d size=%d",
					e.Name, e.LocalHdrOff, e.CompSize, e.Size)
			}
			// And the name must be valid UTF-8: it goes straight into SQLite
			// and then into JSON.
			if !utf8Valid(e.Name) {
				t.Fatalf("entry name %q is not valid UTF-8", e.Name)
			}
		}
	})
}

func FuzzOpenEntry(f *testing.F) {
	jpg := testutil.TinyJPEG(f, 12, 12)
	good := testutil.BuildZIP(f, testutil.ZIPSpec{Entries: []testutil.Entry{
		{Name: "001.jpg", Data: jpg, Method: testutil.MethodStore},
		{Name: "002.jpg", Data: jpg, Method: testutil.MethodDeflate},
	}})
	locked := testutil.BuildZIP(f, testutil.ZIPSpec{Entries: []testutil.Entry{
		{Name: "001.jpg", Data: jpg, Method: testutil.MethodStore, Flags: testutil.FlagEncrypted},
	}})
	// Every real entry, at the geometry its own directory gives it, so the
	// seeds reach both serving paths and the encryption refusal.
	for _, zf := range [][]byte{good, locked} {
		ix, err := zipidx.ReadCentralDirectory(f.Context(), bytes.NewReader(zf), int64(len(zf)))
		if err != nil {
			f.Fatalf("seed archive: %v", err)
		}
		for _, e := range ix.Entries {
			f.Add(zf, e.LocalHdrOff, e.CompSize, uint(e.Method))
		}
	}
	f.Add(good, int64(0), int64(1<<40), uint(8))
	f.Add(good, int64(len(good)-1), int64(64), uint(8))
	f.Add([]byte("PK\x03\x04"), int64(0), int64(16), uint(8))

	f.Fuzz(func(t *testing.T, data []byte, off, compSize int64, method uint) {
		c := newCounter(data)
		rc, err := zipidx.OpenEntry(t.Context(), c, archive.EntryRef{
			LocalHdrOff: off,
			CompSize:    compSize,
			Method:      uint16(method),
		})
		if err != nil {
			return
		}
		defer func() { _ = rc.Close() }()

		// A stream came back, so what sits at off must be something the
		// serving path may stream: a local header, not encrypted (FR-IDX-010
		// reports those and never decodes them), in a method this build reads.
		if off < 0 || off > int64(len(data))-30 || string(data[off:off+4]) != "PK\x03\x04" {
			t.Fatalf("opened an entry at %d, where there is no local header", off)
		}
		hdr := data[off : off+30]
		if binary.LittleEndian.Uint16(hdr[6:])&archive.FlagEncrypted != 0 {
			t.Fatalf("opened the encrypted entry at %d", off)
		}
		if m := uint16(method); m != archive.MethodStore && m != archive.MethodDeflate {
			t.Fatalf("opened an entry in method %d", m)
		}
		dataOff := off + 30 + int64(binary.LittleEndian.Uint16(hdr[26:])) + int64(binary.LittleEndian.Uint16(hdr[28:]))

		// The cap is the test's own memory, not a property of the reader.
		// Garbage fed to inflate fails, and that is fine; a panic is not, and
		// the fuzzer catches that for us.
		got, readErr := io.ReadAll(io.LimitReader(rc, 1<<20))

		// AC-008: what leaves the disk is the 30-byte header and the entry's
		// own compSize bytes after it, whatever those bytes turn out to hold.
		for _, s := range c.spans {
			inHeader := s.off >= off && s.end <= off+30
			inPayload := s.off >= dataOff && s.end-dataOff <= compSize
			if !inHeader && !inPayload {
				t.Fatalf("read [%d, %d) — outside the header at %d and the %d-byte payload at %d",
					s.off, s.end, off, compSize, dataOff)
			}
		}

		// FR-SRV-003 and arch §5.3: a stored entry is its payload bytes
		// verbatim, and stays seekable so Range works.
		if uint16(method) == archive.MethodStore {
			if _, ok := rc.(io.ReadSeeker); !ok {
				t.Fatalf("stored entry came back as %T, not an io.ReadSeeker", rc)
			}
			if readErr != nil {
				t.Fatalf("reading a stored entry: %v", readErr)
			}
			n := max(0, min(int64(len(data))-dataOff, compSize, 1<<20))
			if want := data[min(dataOff, int64(len(data))):][:n]; !bytes.Equal(got, want) {
				t.Fatalf("stored entry served %d bytes, want the %d payload bytes at %d verbatim",
					len(got), len(want), dataOff)
			}
		}
	})
}

// eocd builds a bare end-of-central-directory record, for seeds that are about
// the arithmetic rather than about records.
func eocd(records uint16, dirSize, dirOffset uint32, comment []byte) []byte {
	var b bytes.Buffer
	put32(&b, 0x06054b50)
	put16(&b, 0)
	put16(&b, 0)
	put16(&b, records)
	put16(&b, records)
	put32(&b, dirSize)
	put32(&b, dirOffset)
	put16(&b, uint16(len(comment)))
	b.Write(comment)
	return b.Bytes()
}

func put16(b *bytes.Buffer, v uint16) {
	b.Write([]byte{byte(v), byte(v >> 8)})
}

func put32(b *bytes.Buffer, v uint32) {
	b.Write([]byte{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)})
}

// utf8Valid guards the invariant that a decoded name is storable and
// serialisable. U+FFFD is a legitimate output of the "neither encoding fits"
// branch of arch §4.4; raw invalid bytes are not.
func utf8Valid(s string) bool { return utf8.ValidString(s) }

// The oracle must agree with us on fuzz-discovered inputs too. Keeping this in
// the corpus rather than in the fuzz body means `go test` exercises it on
// every run, including the seeds recorded under testdata/fuzz/.
func TestFuzzSeeds_agreeWithArchiveZip(t *testing.T) {
	t.Parallel()
	seeds := [][]byte{
		eocd(0, 0, 0, nil),
		eocd(0xffff, 0xffffffff, 0xffffffff, nil),
		eocd(1, 0xffffffff, 0, nil),
		eocd(1, 0, 0xffffffff, nil),
		eocd(1, 22, 0, nil),
		eocd(65535, 0, 0, bytes.Repeat([]byte("c"), 300)),
	}
	recorded := recordedFuzzInputs(t, "FuzzReadCentralDirectory")
	if len(recorded) == 0 {
		t.Fatal("no recorded inputs under testdata/fuzz/FuzzReadCentralDirectory")
	}
	seeds = append(seeds, recorded...)
	for i, data := range seeds {
		_, oracleErr := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if errors.Is(oracleErr, zip.ErrInsecurePath) {
			oracleErr = nil
		}
		_, ourErr := zipidx.ReadCentralDirectory(t.Context(), bytes.NewReader(data), int64(len(data)))
		if (oracleErr != nil) != (ourErr != nil) {
			t.Errorf("seed %d: verdict mismatch — archive/zip = %v, zipidx = %v", i, oracleErr, ourErr)
		}
	}
}

// recordedFuzzInputs reads the inputs `go test -fuzz` saved for target, each
// one a failure it found and minimised. The fuzz body replays them on every
// run, but only against its own invariants; this is how they reach the oracle.
func recordedFuzzInputs(t *testing.T, target string) [][]byte {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", "fuzz", target, "*"))
	if err != nil {
		t.Fatalf("globbing recorded inputs: %v", err)
	}
	var out [][]byte
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("reading %s: %v", p, err)
		}
		// The v1 format: a header line, then one Go literal per argument.
		lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
		if len(lines) != 2 || lines[0] != "go test fuzz v1" {
			t.Fatalf("%s: not a one-argument go test fuzz v1 file", p)
		}
		lit, okPrefix := strings.CutPrefix(lines[1], "[]byte(")
		lit, okSuffix := strings.CutSuffix(lit, ")")
		v, err := strconv.Unquote(lit)
		if !okPrefix || !okSuffix || err != nil {
			t.Fatalf("%s: %q is not a []byte literal", p, lines[1])
		}
		out = append(out, []byte(v))
	}
	return out
}
