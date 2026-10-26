//go:build integration

package hv3_test

import (
	"archive/zip"
	"bytes"
	"context"
	"hash/crc32"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"shelf/internal/archive"
	"shelf/internal/archive/hv3"
	"shelf/internal/testutil"
)

// realHV3 is one HV3 container reachable under the test root, named by where
// it was found rather than by a path, because the container is not always a
// file.
type realHV3 struct {
	label string // what to print when something is wrong with it
	path  string // the file on disk
	inner string // the entry within it, empty when the file IS the container
}

// maxInline bounds what a nested entry may be inflated to. The real one is
// 39.5 MB; the cap is here so a hostile or unexpected root cannot turn this
// tier into an OOM.
const maxInline = 1 << 30

// open returns the bytes to read the container from, and the function that
// releases them.
func (c realHV3) open() (io.ReaderAt, int64, func(), error) {
	if c.inner == "" {
		f, err := os.Open(c.path)
		if err != nil {
			return nil, 0, func() {}, err
		}
		fi, err := f.Stat()
		if err != nil {
			_ = f.Close()
			return nil, 0, func() {}, err
		}
		return f, fi.Size(), func() { _ = f.Close() }, nil
	}

	zr, err := zip.OpenReader(c.path)
	if err != nil {
		return nil, 0, func() {}, err
	}
	defer func() { _ = zr.Close() }()
	for _, f := range zr.File {
		if f.Name != c.inner {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, 0, func() {}, err
		}
		b, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			return nil, 0, func() {}, err
		}
		return bytes.NewReader(b), int64(len(b)), func() {}, nil
	}
	return nil, 0, func() {}, fs.ErrNotExist
}

// pinned are the ZIPs every search opens, sampled or not (ruling E-66), as
// paths relative to the collection root. A draw of a few hundred out of eleven
// thousand would almost never reach the one that matters.
var pinned = []string{
	// The collection's only real HV3, as a deflated entry (findRealHV3s).
	"펌프킨 시저스 1~13권/펌프킨 시저스 04.zip",
}

// search is findRealHV3s's result for the root it last searched. Both tests
// below need the same list, and opening eleven thousand central directories
// twice was most of what this tier cost (E-66).
//
// What the search reported is shared along with what it found. A search that
// failed — a pin gone or unopenable — reported it to the test that ran it;
// failedIn names that test so a later one reusing the list fails too, rather
// than passing on a list the search itself said was short.
var search struct {
	sync.Mutex
	root     string
	done     bool
	found    []realHV3
	failedIn string // the test that ran a search that reported errors, or ""
}

// findRealHV3s collects every HV3 container under root: the loose `.hv3` files
// on disk, AND the `.hv3` entries inside `.zip`/`.cbz` containers.
//
// # Why the nested half is not an extra
//
// It is where the collection's only real HV3 actually lives. `펌프킨 시저스
// 04.zip` holds one deflated `.hv3` and nothing else, and on 2026-08-24 the
// loose copy that had been sitting beside it was moved to the trash by its
// owner — at which point this entire tier began reporting `ok` in 0.06 s while
// verifying nothing at all. Both tests below skipped, and a skip reads exactly
// like a pass in `go test ./...` output.
//
// That is the failure this file exists to catch, turned on the file itself: a
// check that goes quiet when its subject moves is not a check. So the search
// follows the subject — a container this build can read — instead of a path
// shape it happened to have on the day it was written.
//
// A nested entry is inflated into memory because it is deflated (39.5 MB of
// JPEG, which deflate cannot help) and the oracle needs an io.ReaderAt.
// Nothing is written to disk: FR-CFG-005 binds the test as it binds the
// product.
//
// # What a sampled run searches
//
// Every loose `.hv3`, always. Of the ZIPs, SHELF_TEST_SAMPLE drawn by
// testutil.Sample plus the pinned one above, because opening every central
// directory is what made this tier take thirteen minutes (E-66). The pin does
// not undo the lesson above: if the container moves, PickSample fails on the
// pin by name, and if it stays but no longer opens, the search fails on it by
// name — never a quiet pass on whatever else the search found — and a full run
// (`make test-int-full`) still follows the subject wherever it went.
func findRealHV3s(t *testing.T, root string) []realHV3 {
	t.Helper()
	search.Lock()
	defer search.Unlock()
	if search.done && search.root == root {
		if search.failedIn != "" {
			t.Errorf("the shared search of %s failed in %s, so the %d HV3 containers reused here are not all it should have found",
				root, search.failedIn, len(search.found))
		}
		t.Logf("reusing this run's search of %s: %d HV3 containers", root, len(search.found))
		return search.found
	}

	var out []realHV3
	var zips []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			t.Logf("walk: %v", err)
			return nil
		}
		if d.IsDir() {
			return nil
		}
		switch strings.ToLower(filepath.Ext(p)) {
		case ".hv3":
			out = append(out, realHV3{label: p, path: p})
		case ".zip", ".cbz":
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			zips = append(zips, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}

	var read int
	for _, rel := range testutil.PickSample(t, "ZIPs searched for a nested HV3", zips, pinned) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		// The central directory only — no entry payload is read here, and a
		// container that will not open is not this tier's business, unless it
		// is pinned: the pin is there to be examined, and passing over it would
		// leave both tests passing on any loose `.hv3` beside it.
		zr, err := zip.OpenReader(p)
		if err != nil {
			if slices.Contains(pinned, rel) {
				t.Errorf("pinned %q cannot be opened, so the HV3 it stands for is not examined: %v", rel, err)
			}
			continue
		}
		read++
		for _, f := range zr.File {
			if !strings.EqualFold(path.Ext(f.Name), ".hv3") {
				continue
			}
			if f.UncompressedSize64 > maxInline {
				t.Logf("%s :: %s is %d bytes, past the %d-byte inline cap — not checked",
					p, f.Name, f.UncompressedSize64, maxInline)
				continue
			}
			out = append(out, realHV3{label: p + " :: " + f.Name, path: p, inner: f.Name})
		}
		_ = zr.Close()
	}
	t.Logf("found %d HV3 containers under %s (%d ZIPs read)", len(out), root, read)
	search.root, search.done, search.found, search.failedIn = root, true, out, ""
	// Nothing before the search can fail this test without ending it
	// (MediaRoot only skips or fails fatally), so Failed here is the search.
	if t.Failed() {
		search.failedIn = t.Name()
	}
	return out
}

// The oracle for HV3, run over the real collection:
//
//	SHELF_TEST_ROOT="/mnt/big-data/…/01. mangga" \
//	  go test -tags integration ./internal/archive/hv3 -run Integration -timeout 30m
//
// That searches every ZIP; `make test-int` searches a sample (findRealHV3s).
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
	root := testutil.MediaRoot(t)

	found := findRealHV3s(t, root)
	if len(found) == 0 {
		// A failure, not a skip, as zipidx and rar4 fail on a root without
		// their format: a skip reads exactly like a pass (findRealHV3s).
		t.Fatalf("no HV3 containers under %s", root)
	}

	var containers, entries, mislabelled int
	for _, c := range found {
		ra, size, closer, err := c.open()
		if err != nil {
			t.Errorf("%s: %v", c.label, err)
			continue
		}

		r := hv3.New()
		ix, err := r.ReadIndex(context.Background(), ra, size)
		if err != nil {
			// 54 of the 55 `.hv3` files this machine held in 2026-08 were RAR
			// archives wearing the extension, all of them in the trash (they
			// have since been deleted outright). A refusal that names the
			// format is the correct outcome for one of those, and it is
			// counted rather than failed.
			if strings.Contains(err.Error(), "but is") {
				mislabelled++
				t.Logf("%s: %v", c.label, err)
				closer()
				continue
			}
			t.Errorf("%s: ReadIndex: %v", c.label, err)
			closer()
			continue
		}
		containers++

		for i := range ix.Entries {
			e := ix.Entries[i]
			if e.Dir {
				continue
			}
			if !utf8.ValidString(e.Name) {
				t.Errorf("%s: entry %d name is not valid UTF-8", c.label, i)
			}
			rc, err := r.OpenEntry(context.Background(), ra, e.Ref())
			if err != nil {
				t.Errorf("%s: OpenEntry(%q): %v", c.label, e.Name, err)
				continue
			}
			h := crc32.NewIEEE()
			n, err := io.Copy(h, rc)
			_ = rc.Close()
			if err != nil {
				t.Errorf("%s: streaming %q: %v", c.label, e.Name, err)
				continue
			}
			if n != e.Size {
				t.Errorf("%s: %q streamed %d bytes, the record says %d", c.label, e.Name, n, e.Size)
			}
			if got := h.Sum32(); got != e.CRC32 {
				t.Errorf("%s: %q crc %08X, the container recorded %08X", c.label, e.Name, got, e.CRC32)
			}
			entries++
		}
		closer()
	}

	// A root that holds nothing but mislabelled files is a real answer; a root
	// where every container turned out to be readable but empty is not, and
	// used to be indistinguishable from success.
	if containers > 0 && entries == 0 {
		t.Errorf("%d HV3 containers opened and not one entry was verified", containers)
	}
	t.Logf("verified %d entries across %d HV3 containers (%d files named .hv3 that are not)",
		entries, containers, mislabelled)
}

// TestIntegration_realCollection_isStoredAndSeekable is the NFR-PRF-006 claim
// for this format, measured rather than asserted: every entry of every real
// container must be servable as a seekable window, because that is what makes
// a page one seek and a Range request cost the range.
func TestIntegration_realCollection_isStoredAndSeekable(t *testing.T) {
	root := testutil.MediaRoot(t)

	found := findRealHV3s(t, root)
	if len(found) == 0 {
		// A failure, not a skip, as zipidx and rar4 fail on a root without
		// their format: a skip reads exactly like a pass (findRealHV3s).
		t.Fatalf("no HV3 containers under %s", root)
	}

	var checked int
	for _, c := range found {
		ra, size, closer, err := c.open()
		if err != nil {
			continue
		}

		r := hv3.New()
		ix, err := r.ReadIndex(context.Background(), ra, size)
		if err != nil {
			closer()
			continue
		}
		for i := range ix.Entries {
			e := ix.Entries[i]
			if e.Dir {
				continue
			}
			if e.CompSize != e.Size {
				t.Errorf("%s: %q is %d packed into %d — nothing in an HV3 is compressed",
					c.label, e.Name, e.Size, e.CompSize)
			}
			rc, err := r.OpenEntry(context.Background(), ra, e.Ref())
			if err != nil {
				t.Errorf("%s: OpenEntry(%q): %v", c.label, e.Name, err)
				continue
			}
			if _, ok := rc.(io.ReadSeeker); !ok {
				t.Errorf("%s: %q is not seekable — Range support is lost for it", c.label, e.Name)
			}
			_ = rc.Close()
			checked++
		}
		closer()
	}
	if checked == 0 {
		t.Errorf("%d HV3 containers were found and not one entry was checked", len(found))
	}
	t.Logf("%d entries are stored and seekable", checked)
}

var _ archive.Reader = hv3.New()
