//go:build nopdf

package source

import (
	"context"
	"fmt"
)

// openPDF in a -tags nopdf build.
//
// The kind stays registered so that the failure is the specific, honest
// "this build has no PDF support" (books.status='unsupported', HTTP 501)
// rather than the generic "unknown kind" a caller would get from an
// unregistered kind. prd CON-002 keeps this tag as the escape hatch for the
// wasm path; decisions.md ships PDF enabled by default.
func openPDF(_ context.Context, _ *Factory, b Book) (BookSource, error) {
	return nil, fmt.Errorf("opening book %s: %w (built with -tags nopdf)", b.ID, ErrUnsupported)
}
