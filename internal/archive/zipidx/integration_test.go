//go:build integration

package zipidx_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The differential oracle, run over the real collection (impl-plan §6.2).
//
// arch §4.3 records the result of doing this once by hand: over all 11 157
// archives, the same 9 fail in both implementations and no archive succeeds in
// one and fails in the other. This test is that check, on demand:
//
//	SHELF_TEST_ROOT="/mnt/big-data/…/01. mangga" \
//	  go test -tags integration ./internal/archive/zipidx -run Integration -timeout 30m
//
// It is read-only by construction — FR-CFG-005 — and gated on the env var, so
// `make test` on a machine without the media volume simply skips it.
func TestIntegration_realCollection_matchesArchiveZip(t *testing.T) {
	root := os.Getenv("SHELF_TEST_ROOT")
	if root == "" {
		t.Skip("SHELF_TEST_ROOT is not set")
	}

	var archives, mismatches int
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// An unreadable directory is a fact about the collection, not a
			// test failure: the scanner has to survive it too.
			t.Logf("walk: %v", err)
			return nil
		}
		if d.IsDir() || !strings.EqualFold(filepath.Ext(p), ".zip") {
			return nil
		}
		archives++
		t.Run(filepath.Base(p), func(t *testing.T) {
			// The archive is never buffered: both implementations get the same
			// *os.File as an io.ReaderAt, which is how the product reads it.
			f, err := os.Open(p)
			if err != nil {
				t.Skipf("opening %s: %v", p, err)
			}
			defer func() { _ = f.Close() }()
			fi, err := f.Stat()
			if err != nil {
				t.Skipf("stat %s: %v", p, err)
			}
			compareWithOracle(t, p, f, fi.Size())
			if t.Failed() {
				mismatches++
			}
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	t.Logf("compared %d archives, %d mismatches", archives, mismatches)
	if archives == 0 {
		t.Fatalf("no .zip files under %s", root)
	}
}
