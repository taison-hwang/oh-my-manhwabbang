package main

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The bodies are deliberately larger than a gzip header and repetitive enough
// to compress, so "the .gz decompresses to the original" is a statement about
// real deflate output rather than about a stored block.
var (
	jsBody     = strings.Repeat("export const shelf = () => console.log('shelf');\n", 400)
	cssBody    = strings.Repeat(".shelf{color:var(--ink);margin:0 auto}\n", 300)
	nestedBody = strings.Repeat("export default 42;\n", 100)
	indexBody  = "<!doctype html><html><head><base href=\"/\" /></head><body></body></html>\n"
	fontBody   = "wOF2\x00\x01\x00\x00not-really-a-font"
	mapBody    = `{"version":3,"sources":[]}`
)

// writeDist lays out a miniature `web/dist` with every shape the tool has to
// tell apart: the two compressible extensions, a nested directory, the entry
// document (which serveIndex rewrites per request and so must stay plain),
// a font (already compressed — gzip would only grow it), and an unrelated file.
func writeDist(t *testing.T, mtime time.Time) string {
	t.Helper()
	dist := t.TempDir()
	files := map[string]string{
		"index.html":                   indexBody,
		"favicon.svg":                  "<svg/>",
		"assets/app-abc123.js":         jsBody,
		"assets/app-abc123.css":        cssBody,
		"assets/inter-def456.woff2":    fontBody,
		"assets/app-abc123.js.map":     mapBody,
		"assets/chunks/lazy-789aaa.js": nestedBody,
	}
	for rel, body := range files {
		p := filepath.Join(dist, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	return dist
}

func readFile(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func gunzip(t *testing.T, gz []byte) (*gzip.Header, []byte) {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		t.Fatalf("not a gzip stream: %v", err)
	}
	body, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("gzip stream does not decode: %v", err)
	}
	return &zr.Header, body
}

func mustRun(t *testing.T, dist string) string {
	t.Helper()
	var out bytes.Buffer
	if err := run(dist, &out); err != nil {
		t.Fatalf("run: %v", err)
	}
	return out.String()
}

func TestRun_ReplacesJSAndCSSWithGzip(t *testing.T) {
	dist := writeDist(t, time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	summary := mustRun(t, dist)

	for rel, want := range map[string]string{
		"assets/app-abc123.js":         jsBody,
		"assets/app-abc123.css":        cssBody,
		"assets/chunks/lazy-789aaa.js": nestedBody,
	} {
		p := filepath.Join(dist, filepath.FromSlash(rel))
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Errorf("%s: the original is still there (err=%v); the binary would carry both forms", rel, err)
		}
		_, body := gunzip(t, readFile(t, p+".gz"))
		if string(body) != want {
			t.Errorf("%s.gz decodes to %d bytes that differ from the original %d", rel, len(body), len(want))
		}
	}

	if !strings.HasPrefix(summary, "precompress: 3 files, ") || strings.Count(summary, "\n") != 1 {
		t.Errorf("summary = %q, want one line starting \"precompress: 3 files, \"", summary)
	}
	wantIn := len(jsBody) + len(cssBody) + len(nestedBody)
	if !strings.Contains(summary, " "+itoa(wantIn)+" -> ") {
		t.Errorf("summary = %q, want the input total %d", summary, wantIn)
	}
}

func TestRun_LeavesEverythingElseAlone(t *testing.T) {
	dist := writeDist(t, time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	mustRun(t, dist)

	for rel, want := range map[string]string{
		"index.html":                indexBody,
		"favicon.svg":               "<svg/>",
		"assets/inter-def456.woff2": fontBody,
		"assets/app-abc123.js.map":  mapBody,
	} {
		p := filepath.Join(dist, filepath.FromSlash(rel))
		if got := readFile(t, p); string(got) != want {
			t.Errorf("%s was modified", rel)
		}
		if _, err := os.Lstat(p + ".gz"); !os.IsNotExist(err) {
			t.Errorf("%s.gz exists; only assets/*.js and *.css are compressed", rel)
		}
	}
}

// The release build must be byte-reproducible (the size budget compares it
// against a recorded figure). gzip's header can carry the source's mtime and
// name, and two otherwise-identical builds made a second apart would then
// differ — so the two copies here differ in mtime on purpose.
func TestRun_IsDeterministic(t *testing.T) {
	a := writeDist(t, time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	b := writeDist(t, time.Date(2025, 3, 4, 5, 6, 7, 0, time.UTC))
	mustRun(t, a)
	mustRun(t, b)

	for _, rel := range []string{"assets/app-abc123.js.gz", "assets/app-abc123.css.gz", "assets/chunks/lazy-789aaa.js.gz"} {
		ga := readFile(t, filepath.Join(a, filepath.FromSlash(rel)))
		gb := readFile(t, filepath.Join(b, filepath.FromSlash(rel)))
		if !bytes.Equal(ga, gb) {
			t.Errorf("%s differs between two builds of the same input", rel)
		}
		hdr, _ := gunzip(t, ga)
		if !hdr.ModTime.IsZero() || hdr.Name != "" || hdr.Comment != "" {
			t.Errorf("%s header = {ModTime:%v Name:%q Comment:%q}, want all empty", rel, hdr.ModTime, hdr.Name, hdr.Comment)
		}
	}
}

func TestRun_UsesBestCompression(t *testing.T) {
	dist := writeDist(t, time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	mustRun(t, dist)
	var want bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&want, gzip.BestCompression)
	_, _ = zw.Write([]byte(jsBody))
	_ = zw.Close()
	if got := readFile(t, filepath.Join(dist, "assets", "app-abc123.js.gz")); !bytes.Equal(got, want.Bytes()) {
		t.Errorf("app-abc123.js.gz is %d bytes, want the BestCompression stream of %d", len(got), want.Len())
	}
}

func TestRun_SecondRunIsANoOp(t *testing.T) {
	dist := writeDist(t, time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	mustRun(t, dist)
	before := snapshot(t, dist)
	summary := mustRun(t, dist)
	if after := snapshot(t, dist); !equalTrees(before, after) {
		t.Errorf("a second run changed the tree:\nbefore %v\nafter  %v", keys(before), keys(after))
	}
	if !strings.HasPrefix(summary, "precompress: 0 files, ") {
		t.Errorf("second-run summary = %q, want 0 files", summary)
	}
}

// A stale .gz next to a fresh original (an interrupted earlier run, or a
// rebuild over an old dist) must lose: the original is the source of truth.
func TestRun_OverwritesAStaleGzip(t *testing.T) {
	dist := writeDist(t, time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	stale := filepath.Join(dist, "assets", "app-abc123.js.gz")
	if err := os.WriteFile(stale, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, dist)
	if _, body := gunzip(t, readFile(t, stale)); string(body) != jsBody {
		t.Error("the stale .gz survived")
	}
}

// A run that fails part-way must leave a tree a re-run can finish: the file it
// failed on still plain and intact (its bytes are the only copy), no temporary
// file the next `go build` would embed, and nothing it already converted
// rewritten when it runs again. A directory squatting on the destination name
// makes the rename fail after the .gz was fully written, the latest point a
// failure can strike before the original is removed.
func TestRun_FailedRenameKeepsTheOriginalAndConverges(t *testing.T) {
	dist := writeDist(t, time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	assetsDir := filepath.Join(dist, "assets")
	orig := filepath.Join(assetsDir, "app-abc123.js")
	blocker := orig + ".gz"
	if err := os.Mkdir(blocker, 0o755); err != nil {
		t.Fatal(err)
	}

	err := run(dist, io.Discard)
	if err == nil {
		t.Fatal("run succeeded although the .gz could not be put in place")
	}
	// The rename, not some earlier step: only then was a complete .gz written
	// and waiting, which is the state this test exists to examine.
	if le := (*os.LinkError)(nil); !errors.As(err, &le) || le.Op != "rename" {
		t.Fatalf("run failed with %v, want the rename onto %s to be what failed", err, blocker)
	}
	if got := readFile(t, orig); string(got) != jsBody {
		t.Fatalf("%s is no longer the original after a failed run", orig)
	}
	noTemps := func() {
		t.Helper()
		err := filepath.WalkDir(dist, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if strings.HasPrefix(d.Name(), ".precompress-") {
				t.Errorf("temporary file left behind: %s", p)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	noTemps()

	// Lexical walk order: the .css came before the .js, so it was converted.
	cssGz := filepath.Join(assetsDir, "app-abc123.css.gz")
	cssBefore, err := os.Stat(cssGz)
	if err != nil {
		t.Fatalf("the file converted before the failure is gone: %v", err)
	}
	cssBytes := readFile(t, cssGz)

	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	mustRun(t, dist)
	noTemps()
	if _, err := os.Lstat(orig); !os.IsNotExist(err) {
		t.Errorf("%s survived the re-run (err=%v)", orig, err)
	}
	if _, body := gunzip(t, readFile(t, blocker)); string(body) != jsBody {
		t.Error("the re-run's .gz does not decode to the original")
	}
	if _, body := gunzip(t, readFile(t, filepath.Join(assetsDir, "chunks", "lazy-789aaa.js.gz"))); string(body) != nestedBody {
		t.Error("the file after the failure point was not converted by the re-run")
	}
	cssAfter, err := os.Stat(cssGz)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(cssBefore, cssAfter) || !bytes.Equal(readFile(t, cssGz), cssBytes) {
		t.Error("the re-run rewrote a .gz the first run had already produced")
	}
}

func TestRun_SkipsSymlinks(t *testing.T) {
	dist := writeDist(t, time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	target := filepath.Join(t.TempDir(), "outside.js")
	if err := os.WriteFile(target, []byte(jsBody), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dist, "assets", "linked-000000.js")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	mustRun(t, dist)
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the symlink was replaced or removed (err=%v)", err)
	}
	if _, err := os.Lstat(link + ".gz"); !os.IsNotExist(err) {
		t.Error("a .gz was written for a symlink")
	}
	if got := readFile(t, target); string(got) != jsBody {
		t.Error("the symlink's target was modified")
	}
}

func TestRun_MissingAssetsIsAnError(t *testing.T) {
	dist := t.TempDir()
	if err := os.WriteFile(filepath.Join(dist, "index.html"), []byte(indexBody), 0o644); err != nil {
		t.Fatal(err)
	}
	err := run(dist, io.Discard)
	if err == nil {
		t.Fatal("run succeeded on a dist with no assets/; a release would ship without the SPA")
	}
	if !strings.Contains(err.Error(), "assets") {
		t.Errorf("error %q does not name the missing directory", err)
	}
}

func TestMainExitCodes(t *testing.T) {
	dist := writeDist(t, time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	for _, tc := range []struct {
		name string
		args []string
		want int
	}{
		{"no argument", nil, 2},
		{"two arguments", []string{dist, dist}, 2},
		{"missing dist", []string{filepath.Join(dist, "nope")}, 1},
		{"ok", []string{dist}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			if got := realMain(tc.args, &out, &errOut); got != tc.want {
				t.Errorf("exit = %d, want %d; stderr %q", got, tc.want, errOut.String())
			}
			if tc.want != 0 && errOut.Len() == 0 {
				t.Error("a failing run printed nothing on stderr")
			}
		})
	}
}

// --- helpers ---------------------------------------------------------------

func itoa(n int) string {
	var b [20]byte
	i := len(b)
	for {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
		if n == 0 {
			return string(b[i:])
		}
	}
}

type entry struct {
	mode os.FileMode
	body string
}

func snapshot(t *testing.T, root string) map[string]entry {
	t.Helper()
	m := map[string]entry{}
	err := filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		e := entry{mode: fi.Mode()}
		if fi.Mode().IsRegular() {
			e.body = string(readFile(t, p))
		}
		m[rel] = e
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func equalTrees(a, b map[string]entry) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func keys(m map[string]entry) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
