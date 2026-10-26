//go:build integration

package zipidx_test

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"shelf/internal/testutil"
)

// pinned are the archives every run compares, sampled or not: shapes that a
// draw of a few hundred out of eleven thousand would almost never reach
// (ruling E-66). Paths are relative to the collection root.
var pinned = []string{
	// Refused by both readers. Of the nine archives arch §4.3 found without a
	// central directory, these are the two the collection still held on
	// 2026-10-26; the other seven had been repaired on disk.
	"디엔엔젤 1-13권 연재중/D.N.Angel 08권.zip",   // 0 bytes
	"쓰르라미 울적에/[화보집] 쓰르라미 울적에 비주얼 팬북.zip", // truncated
	// The four Shift_JIS archives of arch §4.4.1, whose names are decided for
	// the whole archive rather than one at a time. 天上天下 20 is the one whose
	// names CP949 mostly reads, wrongly.
	"바다의 무녀 (완결, 미번) (文月晃, 海の御先)/[文月晃] 海の御先 09.zip",
	"바다의 무녀 (완결, 미번) (文月晃, 海の御先)/[文月晃] 海の御先 10.zip",
	"바다의 무녀 (완결, 미번) (文月晃, 海の御先)/[文月晃] 海の御先 11.zip",
	"천상천하(완)/天上天下 20.zip",
}

// The differential oracle, run over the real collection (impl-plan §6.2).
//
// arch §4.3 records the result of doing this once by hand: over all 11 157
// archives, the same 9 fail in both implementations and no archive succeeds in
// one and fails in the other. This test is that check, on demand:
//
//	SHELF_TEST_ROOT="/mnt/big-data/…/01. mangga" \
//	  go test -tags integration ./internal/archive/zipidx -run Integration -timeout 120m
//
// All of it takes about 54 minutes, because every entry's local header is a
// seek of its own. `make test-int` therefore compares a sample —
// SHELF_TEST_SAMPLE archives drawn by testutil.Sample, plus the pinned ones —
// and `make test-int-full` (or the command above, which leaves the variable
// unset) compares every archive (E-66).
//
// It is read-only by construction — FR-CFG-005 — and gated on the env var, so
// `make test` on a machine without the media volume simply skips it.
func TestIntegration_realCollection_matchesArchiveZip(t *testing.T) {
	root := testutil.MediaRoot(t)

	var rels []string
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
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rels = append(rels, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	if len(rels) == 0 {
		t.Fatalf("no .zip files under %s", root)
	}

	var agreed, failed, skipped int
	picked := testutil.PickSample(t, "archives", rels, pinned)
	for _, rel := range picked {
		p := filepath.Join(root, filepath.FromSlash(rel))
		t.Run(path.Base(rel), func(t *testing.T) {
			// Counted on the way out: a skip and compareWithOracle's Fatalf
			// both end the subtest before any line after them would run.
			defer func() {
				switch {
				case t.Failed():
					failed++
				case t.Skipped():
					skipped++
				default:
					agreed++
				}
			}()
			// The archive is never buffered: both implementations get the same
			// *os.File as an io.ReaderAt, which is how the product reads it.
			f, err := os.Open(p)
			if err != nil {
				unreadable(t, rel, err)
			}
			defer func() { _ = f.Close() }()
			fi, err := f.Stat()
			if err != nil {
				unreadable(t, rel, err)
			}
			compareWithOracle(t, p, f, fi.Size())
		})
	}
	t.Logf("%d archives: %d agree with archive/zip, %d failed, %d skipped as unreadable",
		len(picked), agreed, failed, skipped)
}

// unreadable ends the subtest of an archive that cannot be opened or stat'ed.
// For most archives that is a fact about the collection, and a skip. For a
// pinned one it is a failure naming the pin: the pin is there because a draw
// would miss its shape, and a skip reads exactly like a pass (E-66).
func unreadable(t *testing.T, rel string, err error) {
	t.Helper()
	if slices.Contains(pinned, rel) {
		t.Fatalf("pinned %q cannot be read, so the shape it stands for is not compared: %v", rel, err)
	}
	t.Skipf("%v", err)
}
