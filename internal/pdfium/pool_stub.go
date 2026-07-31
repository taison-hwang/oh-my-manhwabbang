//go:build nopdf

// Package pdfium is compiled out by the nopdf build tag. Every entry point
// keeps its signature and returns ErrUnsupported, so callers need one branch
// (pdfium.Supported()) rather than a build tag of their own.
//
// The tag is worth 8.34 MB of binary (arch §11): the embedded pdfium.wasm is
// half the size of the whole product. prd CON-002 keeps it as the escape
// hatch, and decisions.md's "Also settled" keeps PDF itself in v1.
package pdfium

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"time"
)

// Supported reports whether this build can rasterise PDFs. Always false here.
func Supported() bool { return false }

// Errors callers match with errors.Is. The set and the semantics are identical
// to the non-nopdf build.
var (
	ErrUnsupported = errors.New("pdf support is not enabled in this build")
	ErrClosed      = errors.New("pdf renderer is closed")
	ErrNoSuchPage  = errors.New("no such pdf page")
)

// Defaults, kept so config validation and tests compile identically under
// either tag.
const (
	DefaultWorkers     = 1
	DefaultIdleTimeout = 5 * time.Minute
)

// Render width policy, mirrored from render.go so that URL handling and cache
// keys are tag-independent.
const (
	DefaultWidth = 1200
	MinWidth     = 100
	MaxWidth     = 4000
	WidthSnap    = 100
)

// SnapWidth is identical to the non-nopdf implementation: a URL built by the
// frontend must snap to the same value whichever binary answers it.
func SnapWidth(w, maxWidth int) int {
	if maxWidth <= 0 || maxWidth > MaxWidth {
		maxWidth = MaxWidth
	}
	if w <= 0 {
		w = DefaultWidth
	}
	w = ((w + WidthSnap/2) / WidthSnap) * WidthSnap
	if w < MinWidth {
		w = MinWidth
	}
	if w > maxWidth {
		w = maxWidth
	}
	return w
}

// Options is the nopdf twin of the real Options. The fields are accepted and
// ignored so a composition root does not need a build tag.
type Options struct {
	Workers     int
	CacheDir    string
	IdleTimeout time.Duration
	Logger      *slog.Logger
}

// Renderer exists only to satisfy the same call sites.
type Renderer struct{}

// New returns a renderer whose every operation is ErrUnsupported.
func New(Options) *Renderer { return &Renderer{} }

// Open always fails with ErrUnsupported.
func (*Renderer) Open(context.Context, io.ReadSeeker, int64) (*Doc, error) {
	return nil, ErrUnsupported
}

// Active always reports false: there is no runtime to be up.
func (*Renderer) Active() bool { return false }

// Close is a no-op.
func (*Renderer) Close() error { return nil }

// Doc exists only so that the type is spellable under either tag. No value of
// it is ever produced in a nopdf build.
type Doc struct{}

// PageCount always reports zero.
func (*Doc) PageCount() int { return 0 }

// RenderJPEG always fails with ErrUnsupported.
func (*Doc) RenderJPEG(context.Context, int, int, int) ([]byte, error) { return nil, ErrUnsupported }

// PageSize always fails with ErrUnsupported.
func (*Doc) PageSize(context.Context, int) (float64, float64, error) { return 0, 0, ErrUnsupported }

// Close is a no-op.
func (*Doc) Close() error { return nil }
