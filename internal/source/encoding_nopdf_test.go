//go:build nopdf

package source_test

import "shelf/internal/source"

func init() {
	labelSkips[source.KindPDF] = "a nopdf build records every PDF 'unsupported', never ok"
}
