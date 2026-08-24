//go:build integration

package hv3_test

import (
	"context"
	"hash/crc32"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"shelf/internal/archive"
	"shelf/internal/archive/hv3"
)

// The oracle for HV3, run over the real collection:
//
//	SHELF_TEST_ROOT="/mnt/big-data/…/01. mangga" \
//	  go test -tags integration ./internal/archive/hv3 -run Integration -timeout 30m
//
// There is no second implementation to differ against the way zipidx has
// archive/zip and rar4 has rardecode — nothing in Go reads HoneyView
// containers. What there is instead is better: **the container states the
// answer itself.** Every FINF record carries the CRC-32 of the file's real
// bytes, written by the packer before the mask was applied. So the check is
// end to end and self-verifying — index the file, seek to each recorded
// offset, stream that entry alone, and the CRC-32 of what comes out must equal
// the CRC-32 the container wrote down.
//
// That is exactly the measurement that overturned D-72. If the mask were
// misunderstood — applied from the wrong origin, restarted at the wrong place,
// or not the transform it appears to be — every CRC would be wrong, and no
// amount of "the JPEG header looks right" would hide it.
//
// It is read-only by construction (FR-CFG-005) and gated on the env var, so
// `make test` on a machine without the media volume simply skips it.
func TestIntegration_realCollection_matchesRecordedCRC(t *testing.T) {
	root := os.Getenv("SHELF_TEST_ROOT")
	if root == "" {
		t.Skip("SHELF_TEST_ROOT is not set")
	}

	var containers, entries, mislabelled int
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			t.Logf("walk: %v", err)
			return nil
		}
		if d.IsDir() || !strings.EqualFold(filepath.Ext(p), ".hv3") {
			return nil
		}

		f, err := os.Open(p)
		if err != nil {
			t.Errorf("%s: %v", p, err)
			return nil
		}
		defer func() { _ = f.Close() }()
		fi, err := f.Stat()
		if err != nil {
			t.Errorf("%s: %v", p, err)
			return nil
		}

		r := hv3.New()
		ix, err := r.ReadIndex(context.Background(), f, fi.Size())
		if err != nil {
			// 54 of the 55 `.hv3` files on this machine are RAR archives
			// wearing the extension, all of them in the trash. A refusal that
			// names the format is the correct outcome for one of those, and it
			// is counted rather than failed.
			if strings.Contains(err.Error(), "but is") {
				mislabelled++
				t.Logf("%s: %v", filepath.Base(p), err)
				return nil
			}
			t.Errorf("%s: ReadIndex: %v", p, err)
			return nil
		}
		containers++

		for i := range ix.Entries {
			e := ix.Entries[i]
			if e.Dir {
				continue
			}
			if !utf8.ValidString(e.Name) {
				t.Errorf("%s: entry %d name is not valid UTF-8", p, i)
			}
			rc, err := r.OpenEntry(context.Background(), f, e.Ref())
			if err != nil {
				t.Errorf("%s: OpenEntry(%q): %v", p, e.Name, err)
				continue
			}
			h := crc32.NewIEEE()
			n, err := io.Copy(h, rc)
			_ = rc.Close()
			if err != nil {
				t.Errorf("%s: streaming %q: %v", p, e.Name, err)
				continue
			}
			if n != e.Size {
				t.Errorf("%s: %q streamed %d bytes, the record says %d", p, e.Name, n, e.Size)
			}
			if got := h.Sum32(); got != e.CRC32 {
				t.Errorf("%s: %q crc %08X, the container recorded %08X", p, e.Name, got, e.CRC32)
			}
			entries++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	if containers == 0 && mislabelled == 0 {
		t.Skip("no .hv3 files under SHELF_TEST_ROOT")
	}
	t.Logf("verified %d entries across %d HV3 containers (%d files named .hv3 that are not)",
		entries, containers, mislabelled)
}

// TestIntegration_realCollection_isStoredAndSeekable is the NFR-PRF-006 claim
// for this format, measured rather than asserted: every entry of every real
// container must be servable as a seekable window, because that is what makes
// a page one seek and a Range request cost the range.
func TestIntegration_realCollection_isStoredAndSeekable(t *testing.T) {
	root := os.Getenv("SHELF_TEST_ROOT")
	if root == "" {
		t.Skip("SHELF_TEST_ROOT is not set")
	}

	var checked int
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.EqualFold(filepath.Ext(p), ".hv3") {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return nil
		}
		defer func() { _ = f.Close() }()
		fi, _ := f.Stat()

		r := hv3.New()
		ix, err := r.ReadIndex(context.Background(), f, fi.Size())
		if err != nil {
			return nil
		}
		for i := range ix.Entries {
			e := ix.Entries[i]
			if e.Dir {
				continue
			}
			if e.CompSize != e.Size {
				t.Errorf("%s: %q is %d packed into %d — nothing in an HV3 is compressed",
					p, e.Name, e.Size, e.CompSize)
			}
			rc, err := r.OpenEntry(context.Background(), f, e.Ref())
			if err != nil {
				t.Errorf("%s: OpenEntry(%q): %v", p, e.Name, err)
				continue
			}
			if _, ok := rc.(io.ReadSeeker); !ok {
				t.Errorf("%s: %q is not seekable — Range support is lost for it", p, e.Name)
			}
			_ = rc.Close()
			checked++
		}
		return nil
	})
	if checked == 0 {
		t.Skip("no readable .hv3 files under SHELF_TEST_ROOT")
	}
	t.Logf("%d entries are stored and seekable", checked)
}

var _ archive.Reader = hv3.New()
