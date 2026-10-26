// Command precompress replaces the SPA's hashed JS and CSS with gzip files
// before `go build` embeds them (decision E-63).
//
//	go run ./scripts/precompress web/dist
//
// Every regular `<dist>/assets/**/*.js` and `*.css` becomes `<file>.gz` and the
// original is removed. The binary then carries only the compressed form, which
// is the whole point: go:embed stores bytes as they are, so text left plain in
// web/dist is text left plain in the release. internal/httpapi/spa.go serves
// the .gz as-is to a client that accepts gzip and decompresses it once for one
// that does not, so nothing a browser can observe depends on which form the
// dist was built in.
//
// What is deliberately left alone:
//   - index.html — serveIndex rewrites its `<base href>` per request, so it has
//     to be plain, and it is a few hundred bytes anyway.
//   - fonts (.woff2) and images — already compressed; gzip would only add a
//     header and a trailer.
//   - anything outside assets/ — those URLs are not content-hashed, and the
//     server's .gz fallback is restricted to assets/ for the same reason.
//   - symlinks and other non-regular files — Vite never writes them, and
//     following one would compress something outside the dist.
//
// The output must be byte-reproducible: the release size budget is compared
// against a recorded figure, and a gzip header carrying each file's mtime would
// make two builds of the same commit differ. So the header's ModTime, Name and
// Comment are pinned empty and the bytes depend only on the input and the Go
// version's deflate.
//
// Running it twice is a no-op: after the first run there is no .js or .css
// left to find. A dist with no assets/ directory is an error, not a no-op —
// `make web` always runs `pnpm build` first, so a missing directory means the
// SPA was not built, and a release must not ship without it.
package main

import (
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// gzSuffix is what the server looks for when the original name is missing;
// internal/httpapi/spa.go carries the same constant.
const gzSuffix = ".gz"

// compressible is the set of extensions replaced. Lower-case only, because
// Vite only ever writes lower-case extensions and the server derives the
// Content-Type from this same extension.
var compressible = map[string]bool{".js": true, ".css": true}

func main() {
	os.Exit(realMain(os.Args[1:], os.Stdout, os.Stderr))
}

// realMain is main without the process: tests call it to pin the exit codes.
func realMain(args []string, out, errOut io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(errOut, "usage: precompress <dist dir>")
		return 2
	}
	if err := run(args[0], out); err != nil {
		fmt.Fprintf(errOut, "precompress: %v\n", err)
		return 1
	}
	return 0
}

// run compresses every eligible file under <dist>/assets and prints one
// summary line.
func run(dist string, out io.Writer) error {
	assets := filepath.Join(dist, "assets")
	info, err := os.Stat(assets)
	if err != nil {
		return fmt.Errorf("%s: %w (run `pnpm build` first; a release must carry the SPA)", assets, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", assets)
	}

	// Collect first, write second: creating files in a directory WalkDir is
	// still reading is legal but makes "what did this run see" depend on
	// readdir order.
	var targets []string
	err = filepath.WalkDir(assets, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// d.Type() comes from Lstat, so a symlink reports ModeSymlink here
		// and is skipped rather than followed.
		if d.Type().IsRegular() && compressible[filepath.Ext(p)] {
			targets = append(targets, p)
		}
		return nil
	})
	if err != nil {
		return err
	}

	var before, after int64
	for _, p := range targets {
		in, outN, err := compressFile(p)
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		before += in
		after += outN
	}
	fmt.Fprintf(out, "precompress: %d files, %d -> %d bytes\n", len(targets), before, after)
	return nil
}

// compressFile writes p+".gz" and removes p, returning both sizes.
//
// The .gz is written to a temporary file in the same directory and renamed
// over the destination, so an interrupted run leaves either the old state or
// the new one, never a truncated .gz that the server would serve as an asset.
// The original goes only after the rename: a crash in between leaves both
// forms, which a re-run repairs (the original wins and the .gz is rewritten).
func compressFile(p string) (int64, int64, error) {
	src, err := os.ReadFile(p)
	if err != nil {
		return 0, 0, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".precompress-*")
	if err != nil {
		return 0, 0, err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // a no-op once renamed

	zw, err := gzip.NewWriterLevel(tmp, gzip.BestCompression)
	if err != nil {
		_ = tmp.Close()
		return 0, 0, err
	}
	// These are the writer's zero values already; they are set explicitly so
	// the reproducibility requirement is visible where it is met, and so a
	// later "helpful" edit that records the source's name or mtime has to
	// delete a line that says why not.
	zw.ModTime = time.Time{}
	zw.Name = ""
	zw.Comment = ""
	if _, err := zw.Write(src); err != nil {
		_ = tmp.Close()
		return 0, 0, err
	}
	if err := zw.Close(); err != nil {
		_ = tmp.Close()
		return 0, 0, err
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return 0, 0, err
	}
	fi, err := tmp.Stat()
	if err != nil {
		_ = tmp.Close()
		return 0, 0, err
	}
	if err := tmp.Close(); err != nil {
		return 0, 0, err
	}
	if err := os.Rename(tmpName, p+gzSuffix); err != nil {
		return 0, 0, err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return 0, 0, err
	}
	return int64(len(src)), fi.Size(), nil
}
