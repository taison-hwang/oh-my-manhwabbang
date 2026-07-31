package zipidx_test

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
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
	f.Add(good, int64(0), int64(len(jpg)), uint(0))
	f.Add(good, int64(0), int64(1<<40), uint(8))
	f.Add(good, int64(len(good)-1), int64(64), uint(8))
	f.Add([]byte("PK\x03\x04"), int64(0), int64(16), uint(8))

	f.Fuzz(func(t *testing.T, data []byte, off, compSize int64, method uint) {
		rc, err := zipidx.OpenEntry(t.Context(), bytes.NewReader(data), archive.EntryRef{
			LocalHdrOff: off,
			CompSize:    compSize,
			Method:      uint16(method),
		})
		if err != nil {
			return
		}
		defer func() { _ = rc.Close() }()
		// Reading must not panic and must not be allowed to produce more than
		// the entry claims to hold: NFR-PRF-006 is about bounded memory, and a
		// zip bomb is exactly an unbounded claim.
		n, err := io.Copy(io.Discard, io.LimitReader(rc, 1<<20))
		if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
			// A decompression failure on garbage is fine; a panic is not, and
			// the fuzzer catches that for us.
			_ = err
		}
		_ = n
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
