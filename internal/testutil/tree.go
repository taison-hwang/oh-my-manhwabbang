package testutil

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// File is a leaf in a BuildTree layout when the defaults are not enough:
// an explicit mode, or an explicit modification time.
//
// ModTime matters more than it looks. content_version, the incremental scan
// and FR-THM-006 all key off (size, mtime), so any test that exercises "did
// this change?" has to be able to pin and then move an mtime deliberately
// rather than depending on wall-clock ordering.
type File struct {
	Data    []byte
	Mode    os.FileMode // defaults to 0644 for files, 0755 for directories
	ModTime time.Time   // zero means "leave whatever the filesystem set"
}

// Dir is an explicit empty directory in a layout. A nil map value means the
// same thing; Dir just reads better when the intent is "this stays empty".
var Dir = map[string]any{}

// BuildTree materialises a directory tree under t.TempDir() and returns its
// absolute path.
//
// Keys are names, or slash-separated relative paths when it is shorter to say
// so. Values may be:
//
//	nil                 an empty directory
//	map[string]any      a subdirectory, recursively
//	[]byte / string     a file with that content
//	File                a file with an explicit mode and/or mtime
//
// Example — prd §2.2 row 6, "mixed", as it actually occurs (data-survey D-5):
//
//	root := testutil.BuildTree(t, map[string]any{
//	    "[만화] 강철의 연금술사 1~27권 완결": map[string]any{
//	        "01권.zip":                     testutil.BuildZIP(t, spec),
//	        "02권.zip":                     testutil.BuildZIP(t, spec),
//	        "강철의 연금술사 00 Cover.jpg":  testutil.TinyJPEG(t, 16, 24),
//	    },
//	})
//
// Directories are created before their contents and mtimes are applied
// depth-first afterwards, so setting an mtime on a directory is not undone by
// writing a file into it.
func BuildTree(t testing.TB, layout map[string]any) string {
	t.Helper()
	root := t.TempDir()
	var pending []timedPath
	writeInto(t, root, layout, &pending)
	applyDeferredTimes(t, pending)
	return root
}

// BuildTreeAt is BuildTree against a caller-chosen directory, for tests that
// need two roots or a root nested inside another fixture. dir must exist.
func BuildTreeAt(t testing.TB, dir string, layout map[string]any) string {
	t.Helper()
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("testutil: BuildTreeAt target %q is not an existing directory (err=%v)", dir, err)
	}
	var pending []timedPath
	writeInto(t, dir, layout, &pending)
	applyDeferredTimes(t, pending)
	return dir
}

// timedPath is an mtime to apply once the whole tree exists. The list is
// threaded through the recursion rather than kept in a package variable so
// that two tests calling BuildTree under t.Parallel() cannot collide.
type timedPath struct {
	path string
	when time.Time
}

func writeInto(t testing.TB, dir string, layout map[string]any, pending *[]timedPath) {
	t.Helper()

	// Sort the keys so the tree is built in a deterministic order; several
	// fixtures depend on creation order being stable across runs.
	names := make([]string, 0, len(layout))
	for name := range layout {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		if name == "" || name == "." || strings.HasPrefix(name, "/") {
			t.Fatalf("testutil: layout key %q must be a non-empty relative path", name)
		}
		if strings.Contains(name, "..") {
			t.Fatalf("testutil: layout key %q may not contain %q", name, "..")
		}
		target := filepath.Join(dir, filepath.FromSlash(name))

		switch v := layout[name].(type) {
		case nil:
			mkdirAll(t, target, 0o755)
		case map[string]any:
			mkdirAll(t, target, 0o755)
			writeInto(t, target, v, pending)
		case []byte:
			writeFile(t, target, v, 0o644)
		case string:
			writeFile(t, target, []byte(v), 0o644)
		case File:
			mode := v.Mode
			if mode == 0 {
				mode = 0o644
			}
			writeFile(t, target, v.Data, mode)
			if !v.ModTime.IsZero() {
				*pending = append(*pending, timedPath{target, v.ModTime})
			}
		default:
			t.Fatalf("testutil: layout key %q has unsupported value type %T "+
				"(want nil, map[string]any, []byte, string or testutil.File)", name, v)
		}
	}
}

func applyDeferredTimes(t testing.TB, pending []timedPath) {
	t.Helper()
	// Deepest paths first, so touching a parent directory afterwards is not
	// undone by writing into a child.
	sort.Slice(pending, func(i, j int) bool {
		return len(pending[i].path) > len(pending[j].path)
	})
	for _, p := range pending {
		if err := os.Chtimes(p.path, p.when, p.when); err != nil {
			t.Fatalf("testutil: setting mtime on %q: %v", p.path, err)
		}
	}
}

func mkdirAll(t testing.TB, path string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(path, mode); err != nil {
		t.Fatalf("testutil: creating directory %q: %v", path, err)
	}
}

func writeFile(t testing.TB, path string, data []byte, mode os.FileMode) {
	t.Helper()
	mkdirAll(t, filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatalf("testutil: writing %q: %v", path, err)
	}
}

// ReplaceFile swaps a file's contents for new bytes the way a person repairing
// a broken download does: the bytes are written beside the path and renamed
// over it, so the name ends up on a **different inode** and any descriptor
// still open on the old one keeps reading the old bytes.
//
// That distinction is the whole reason this exists. Rewriting the same path
// with os.WriteFile truncates in place, so an already-open descriptor follows
// the new content and a test written that way cannot see a handle pool serving
// a replaced file. `mv 궁\ 24.zip.new 궁\ 24.zip` — the operation the user
// actually performed — leaves the old inode alive and readable, which is the
// failure mode the pool has to survive.
//
// when pins the new file's mtime; a zero time leaves whatever the filesystem
// set. Pinning it matters because (size, mtime) is the index's whole notion of
// identity and the clock's one-second granularity would otherwise decide the
// test's outcome.
func ReplaceFile(t testing.TB, path string, data []byte, when time.Time) {
	t.Helper()
	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("testutil: ReplaceFile needs an existing file at %q: %v", path, err)
	}
	tmp := path + ".testutil-replacement"
	writeFile(t, tmp, data, before.Mode().Perm())
	if !when.IsZero() {
		if err := os.Chtimes(tmp, when, when); err != nil {
			t.Fatalf("testutil: setting mtime on %q: %v", tmp, err)
		}
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatalf("testutil: renaming %q over %q: %v", tmp, path, err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("testutil: stat %q after replacing it: %v", path, err)
	}
	// Proof, not assumption: a helper that quietly rewrote the same inode would
	// make every test built on it pass for the wrong reason.
	if os.SameFile(before, after) {
		t.Fatalf("testutil: ReplaceFile left %q on the same inode", path)
	}
}

// Touch moves a path's modification time forward by d, which is how a test
// says "this file changed" to the incremental scanner (FR-IDX-003) without
// sleeping.
func Touch(t testing.TB, path string, d time.Duration) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("testutil: stat %q: %v", path, err)
	}
	when := fi.ModTime().Add(d)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatalf("testutil: setting mtime on %q: %v", path, err)
	}
}
