// Package buildinfo carries the version stamps injected at link time.
//
// These are the only mutable package-level variables permitted anywhere in the
// product (impl-plan §5.1); they are written exactly once, by the linker, and
// treated as read-only afterwards. `make build` sets them with:
//
//	-ldflags "-X shelf/internal/buildinfo.Version=... \
//	          -X shelf/internal/buildinfo.Commit=... \
//	          -X shelf/internal/buildinfo.Date=..."
package buildinfo

import (
	"runtime"
	"strings"
)

// Defaults describe an un-stamped build (`go run ./cmd/shelf`, `go test`).
var (
	// Version is the release identity, e.g. "v1.2.3" or "v1.2.3-4-gabc1234-dirty".
	Version = "dev"
	// Commit is the short git revision the binary was built from.
	Commit = "none"
	// Date is the UTC build timestamp in RFC 3339 form.
	Date = "unknown"
)

// String renders the one-line banner used by `shelf --version` and by the
// startup log record.
func String() string {
	var b strings.Builder
	b.WriteString("shelf ")
	b.WriteString(Version)
	b.WriteString(" (")
	b.WriteString(Commit)
	b.WriteString(", built ")
	b.WriteString(Date)
	b.WriteString(", ")
	b.WriteString(runtime.Version())
	b.WriteString(" ")
	b.WriteString(runtime.GOOS)
	b.WriteString("/")
	b.WriteString(runtime.GOARCH)
	b.WriteString(")")
	return b.String()
}
