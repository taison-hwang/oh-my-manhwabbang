//go:build nopdf

package source_test

import (
	"errors"
	"testing"

	"shelf/internal/archive"
	"shelf/internal/source"
	"shelf/internal/testutil"
)

func buildOnePageZIP(t *testing.T, jpg []byte) []byte {
	t.Helper()
	return testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
		{Name: "1.jpg", Data: jpg, Method: testutil.MethodDeflate},
	}})
}

// Under -tags nopdf a PDF book must be `unsupported`, which the API turns into
// 501 and the UI into a badge on the volume. It must not be "unknown kind" and
// it must not be a crash: the rest of the library keeps working.
func TestNopdf_pdfBookIsUnsupported(t *testing.T) {
	t.Parallel()
	f := newFixture(t, map[string]any{
		"series": map[string]any{"vol1.pdf": []byte("%PDF-1.4\n%%EOF\n")},
	})

	_, err := f.factory.Open(t.Context(), source.Book{
		ID: "bk", Kind: source.KindPDF, RootName: rootName, RelPath: "series/vol1.pdf",
	})
	if !errors.Is(err, source.ErrUnsupported) {
		t.Fatalf("err = %v, want source.ErrUnsupported", err)
	}
	if got := source.StatusOf(err); got != archive.StatusUnsupported {
		t.Errorf("status = %q, want %q", got, archive.StatusUnsupported)
	}

	// The kind stays registered, so the message is the specific one.
	kinds := f.factory.Kinds()
	var found bool
	for _, k := range kinds {
		if k == source.KindPDF {
			found = true
		}
	}
	if !found {
		t.Errorf("Kinds() = %v, want pdf to stay registered so the error is specific", kinds)
	}
}

// The ZIP and folder halves of the library are untouched by the tag.
func TestNopdf_zipAndDirStillWork(t *testing.T) {
	t.Parallel()
	jpg := []byte("\xff\xd8\xff\xe0not-really-a-jpeg-but-bytes-are-bytes")
	f := newFixture(t, map[string]any{
		"series": map[string]any{
			"vol1":     map[string]any{"1.jpg": jpg},
			"vol1.zip": buildOnePageZIP(t, jpg),
		},
	})
	for _, tc := range []struct {
		kind source.Kind
		rel  string
	}{{source.KindDir, "series/vol1"}, {source.KindZIP, "series/vol1.zip"}} {
		src := f.open(t, tc.kind, tc.rel)
		list, err := src.List(t.Context())
		if err != nil {
			t.Fatalf("%s List: %v", tc.kind, err)
		}
		if len(list.Pages) != 1 {
			t.Errorf("%s pages = %d, want 1", tc.kind, len(list.Pages))
		}
		if got := readPage(t, src, list.Pages[0]); string(got) != string(jpg) {
			t.Errorf("%s page bytes differ", tc.kind)
		}
	}
}
