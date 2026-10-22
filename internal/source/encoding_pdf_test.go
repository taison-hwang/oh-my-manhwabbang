//go:build !nopdf

package source_test

import (
	"testing"

	"shelf/internal/source"
)

// The PDF case of TestEverySourceKind_labelsASuccessfulListing. Only a build
// with pdfium lists a PDF successfully, so the case lives behind its tag.
func init() {
	labelCases[source.KindPDF] = func(t *testing.T) source.BookSource {
		f, _ := newPDFFixture(t, map[string]any{"vol1.pdf": pdfFixture(t, 1, 200, 300)})
		return f.open(t, source.KindPDF, "vol1.pdf")
	}
}
