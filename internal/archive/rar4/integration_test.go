//go:build integration

package rar4_test

import (
	"hash/crc32"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/nwaples/rardecode/v2"

	"shelf/internal/archive"
	"shelf/internal/archive/rar4"
)

// The differential oracle for RAR, run over the real collection, in the shape
// zipidx uses against archive/zip:
//
//	SHELF_TEST_ROOT="/mnt/big-data/…/01. mangga" \
//	  go test -tags integration ./internal/archive/rar4 -run Integration -timeout 30m
//
// The oracle is a whole-archive sequential decode by rardecode — the ordinary
// way to read a RAR, and the way this package deliberately does not read one.
// Every entry is then re-read the product's way (index once, then seek to
// LocalHdrOff and stream that entry alone) and the two must agree on the name,
// the length and the CRC-32 of the bytes.
//
// That is the check that matters, because the two paths differ exactly where
// the risk is. The oracle decodes entries in order with all the state that
// implies; the product decodes entry 826 having read nothing before it. If the
// splice in OpenEntry were subtly wrong — a main header that asserts the wrong
// flags, an off-by-one on the block header, a solid archive slipping past the
// refusal — the CRCs are where it would show.
//
// It is read-only by construction (FR-CFG-005) and gated on the env var, so
// `make test` on a machine without the media volume simply skips it.
func TestIntegration_realCollection_matchesUnpacker(t *testing.T) {
	root := os.Getenv("SHELF_TEST_ROOT")
	if root == "" {
		t.Skip("SHELF_TEST_ROOT is not set")
	}

	var archives, entries, packed, stored int
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			t.Logf("walk: %v", err)
			return nil
		}
		if d.IsDir() {
			return nil
		}
		switch strings.ToLower(filepath.Ext(p)) {
		case ".rar", ".cbr":
		default:
			return nil
		}
		archives++
		t.Run(filepath.Base(p), func(t *testing.T) {
			n, np, ns := compareOneArchive(t, p)
			entries += n
			packed += np
			stored += ns
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	t.Logf("compared %d archives, %d entries (%d stored, %d packed)",
		archives, entries, stored, packed)
	if archives == 0 {
		t.Fatalf("no .rar files under %s", root)
	}
	if packed == 0 {
		t.Error("no packed entries were exercised; the splice path went untested")
	}
}

func compareOneArchive(t *testing.T, path string) (entries, packed, stored int) {
	t.Helper()

	f, err := os.Open(path)
	if err != nil {
		t.Skipf("opening %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		t.Skipf("stat %s: %v", path, err)
	}

	// --- the oracle: read it the ordinary way, start to finish ---
	type want struct {
		name string
		size int64
		crc  uint32
	}
	var oracle []want
	rr, err := rardecode.NewReader(io.NewSectionReader(f, 0, fi.Size()))
	if err != nil {
		t.Fatalf("oracle: %v", err)
	}
	for {
		h, err := rr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("oracle at entry %d: %v", len(oracle), err)
		}
		if h.IsDir {
			continue
		}
		hsh := crc32.NewIEEE()
		n, err := io.Copy(hsh, rr)
		if err != nil {
			t.Fatalf("oracle reading %q: %v", h.Name, err)
		}
		oracle = append(oracle, want{name: h.Name, size: n, crc: hsh.Sum32()})
	}

	// --- the product: index once, then one seek per entry ---
	r := rar4.New()
	ix, err := r.ReadIndex(t.Context(), f, fi.Size())
	if err != nil {
		t.Fatalf("ReadIndex: %v", err)
	}
	var got []archive.Entry
	for i := range ix.Entries {
		if !ix.Entries[i].Dir {
			got = append(got, ix.Entries[i])
		}
	}
	if len(got) != len(oracle) {
		t.Fatalf("indexed %d file entries, oracle found %d", len(got), len(oracle))
	}

	for i, w := range oracle {
		e := got[i]
		if e.Name != w.name {
			t.Errorf("entry %d: name %q, oracle %q", i, e.Name, w.name)
		}
		if !utf8.ValidString(e.Name) {
			t.Errorf("entry %d: name is not valid UTF-8: %q", i, e.Name)
		}
		if e.Size != w.size {
			t.Errorf("entry %d (%s): indexed size %d, oracle read %d", i, e.Name, e.Size, w.size)
		}
		// The header's own CRC must agree with the bytes, or the index is
		// describing something other than this file.
		if e.CRC32 != w.crc {
			t.Errorf("entry %d (%s): header CRC %08x, oracle %08x", i, e.Name, e.CRC32, w.crc)
		}

		if e.Method == 0x30 {
			stored++
		} else {
			packed++
		}

		rc, err := r.OpenEntry(t.Context(), f, e.Ref())
		if err != nil {
			t.Errorf("entry %d (%s): OpenEntry: %v", i, e.Name, err)
			continue
		}
		hsh := crc32.NewIEEE()
		n, err := io.Copy(hsh, rc)
		_ = rc.Close()
		if err != nil {
			t.Errorf("entry %d (%s): streaming: %v", i, e.Name, err)
			continue
		}
		if n != w.size {
			t.Errorf("entry %d (%s): streamed %d bytes, oracle %d", i, e.Name, n, w.size)
		}
		if hsh.Sum32() != w.crc {
			t.Errorf("entry %d (%s): streamed CRC %08x, oracle %08x", i, e.Name, hsh.Sum32(), w.crc)
		}

		// A stored entry must arrive seekable, or Range support silently
		// disappears for 92% of the collection's RAR pages (arch §5.3).
		if e.Method == 0x30 {
			rc, err := r.OpenEntry(t.Context(), f, e.Ref())
			if err != nil {
				t.Errorf("entry %d: reopening: %v", i, err)
				continue
			}
			if _, ok := rc.(io.ReadSeeker); !ok {
				t.Errorf("entry %d (%s): stored entry came back as %T, not an io.ReadSeeker",
					i, e.Name, rc)
			}
			_ = rc.Close()
		}
	}
	return len(oracle), packed, stored
}
