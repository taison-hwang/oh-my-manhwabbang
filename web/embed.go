// Package web is simultaneously the Vite project and a Go package: go:embed
// cannot reach outside its own directory, so the frontend lives here and the
// built SPA is compiled into the binary from here (NFR-OPS-001, arch §2.1).
package web

import (
	"embed"
	"io/fs"
)

// The pattern is `all:dist` rather than `dist` for two reasons:
//
//  1. `all:` includes files whose names begin with `.` or `_`, which the plain
//     form silently skips. That is what makes the committed `web/dist/.gitkeep`
//     count as a match, so this package compiles on a clean checkout where
//     `pnpm build` has never run and `dist/` holds nothing else. A bare
//     `//go:embed dist` would fail with "pattern dist: no matching files found".
//  2. Vite emits dot-prefixed files (e.g. `.vite/manifest.json`) that the plain
//     form would drop from the binary.
//
// `make build` runs `pnpm build` before `go build`, so release binaries always
// carry a real SPA; a bare `go build ./...` produces a working server whose
// static handler finds no index.html and says so (arch §2.1).
//
//go:embed all:dist
var distFS embed.FS

// Dist returns the built SPA rooted at dist/. It is valid but nearly empty when
// the frontend has not been built; the HTTP layer detects the missing
// index.html and serves a "run `make web`" placeholder rather than a 404 storm.
func Dist() fs.FS {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		// Impossible: dist/ is guaranteed to exist by the committed .gitkeep,
		// and fs.Sub only fails on an invalid path.
		panic("web: embedded dist/ is unreachable: " + err.Error())
	}
	return sub
}
