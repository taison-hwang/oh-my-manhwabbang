//go:build nopdf

package pdfium_test

import (
	"bytes"
	"errors"
	"testing"

	"shelf/internal/pdfium"
)

// Under -tags nopdf the package must still compile against every call site and
// answer honestly. Losing 8.34 MB of embedded wasm (arch §11) is only useful if
// nothing above has to know it happened.

func TestNopdf_supportedIsFalse(t *testing.T) {
	t.Parallel()
	if pdfium.Supported() {
		t.Fatal("Supported() = true in a -tags nopdf build")
	}
}

func TestNopdf_everyEntrypointIsErrUnsupported(t *testing.T) {
	t.Parallel()
	r := pdfium.New(pdfium.Options{Workers: 1, CacheDir: t.TempDir()})
	t.Cleanup(func() { _ = r.Close() })

	if r.Active() {
		t.Error("Active() = true in a -tags nopdf build")
	}

	doc, err := r.Open(t.Context(), bytes.NewReader([]byte("%PDF-1.4\n")), 9)
	if !errors.Is(err, pdfium.ErrUnsupported) {
		t.Fatalf("Open err = %v, want pdfium.ErrUnsupported", err)
	}
	if doc != nil {
		t.Fatal("Open returned a non-nil Doc in a -tags nopdf build")
	}

	var d pdfium.Doc
	if got := d.PageCount(); got != 0 {
		t.Errorf("PageCount() = %d, want 0", got)
	}
	if _, err := d.RenderJPEG(t.Context(), 1, 800, 80); !errors.Is(err, pdfium.ErrUnsupported) {
		t.Errorf("RenderJPEG err = %v, want pdfium.ErrUnsupported", err)
	}
	if _, _, err := d.PageSize(t.Context(), 1); !errors.Is(err, pdfium.ErrUnsupported) {
		t.Errorf("PageSize err = %v, want pdfium.ErrUnsupported", err)
	}
	if err := d.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// The URL and cache-key arithmetic must be identical under either tag, or a
// frontend built against one binary produces URLs the other snaps differently.
func TestNopdf_snapWidthMatchesTheRealImplementation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, max, want int
	}{
		{0, 0, pdfium.DefaultWidth},
		{1, 0, pdfium.MinWidth},
		{149, 0, 100},
		{151, 0, 200},
		{1234, 0, 1200},
		{99999, 0, pdfium.MaxWidth},
		{5000, 1600, 1600},
	}
	for _, tc := range cases {
		if got := pdfium.SnapWidth(tc.in, tc.max); got != tc.want {
			t.Errorf("SnapWidth(%d, %d) = %d, want %d", tc.in, tc.max, got, tc.want)
		}
	}
}
