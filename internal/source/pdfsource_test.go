//go:build !nopdf

package source_test

import (
	"bytes"
	"errors"
	"fmt"
	"image/jpeg"
	"io"
	"log/slog"
	"testing"

	"shelf/internal/archive"
	"shelf/internal/openpool"
	"shelf/internal/pdfium"
	"shelf/internal/source"
	"shelf/internal/testutil"
)

// AC-004: a PDF series is read in the same viewer as a ZIP series, with no
// distinction. At this layer that means: same BookSource interface, same
// Listing shape, and an image out of Open.

// pdfFixture is the same hand-built PDF the pdfium package's tests use — the
// frozen dependency set has no PDF writer and the real 500 MB PDF series may
// not enter the hermetic suite (impl-plan §6.1).
func pdfFixture(t testing.TB, pages, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	offsets := make(map[int]int)
	buf.WriteString("%PDF-1.4\n")
	obj := func(n int, body string) {
		offsets[n] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", n, body)
	}
	kids := ""
	for i := 0; i < pages; i++ {
		kids += fmt.Sprintf("%d 0 R ", 3+2*i)
	}
	obj(1, "<< /Type /Catalog /Pages 2 0 R >>")
	obj(2, fmt.Sprintf("<< /Type /Pages /Kids [ %s] /Count %d >>", kids, pages))
	for i := 0; i < pages; i++ {
		obj(3+2*i, fmt.Sprintf(
			"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %d %d] /Contents %d 0 R /Resources << >> >>",
			w, h, 4+2*i))
		content := fmt.Sprintf("%0.2f 0.35 0.75 rg 10 10 %d %d re f\n", float64(i%10)/10.0, w-20, h-20)
		obj(4+2*i, fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(content), content))
	}
	total := 2 + 2*pages
	xref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n", total+1)
	buf.WriteString("0000000000 65535 f \n")
	for n := 1; n <= total; n++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[n])
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", total+1, xref)
	return buf.Bytes()
}

// newPDFFixture is newFixture with a live rasteriser wired in.
func newPDFFixture(t *testing.T, layout map[string]any) (*fixture, *pdfium.Renderer) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	root := testutil.BuildTree(t, layout)

	roots, err := source.NewRootSet(t.Context(), map[string]string{rootName: root}, log)
	if err != nil {
		t.Fatalf("NewRootSet: %v", err)
	}
	t.Cleanup(func() { _ = roots.Close() })

	pool := openpool.New(openpool.Options{Max: 8, Open: roots.PoolOpener(), Logger: log})
	t.Cleanup(func() { _ = pool.Close() })

	r := pdfium.New(pdfium.Options{Workers: 1, CacheDir: t.TempDir(), Logger: log})
	t.Cleanup(func() { _ = r.Close() })

	f := source.NewFactory(source.Options{
		Roots:       roots,
		Pool:        pool,
		PDF:         r,
		PDFWidth:    800,
		PDFMaxWidth: 2000,
		PDFQuality:  82,
		Logger:      log,
	})
	return &fixture{root: root, roots: roots, pool: pool, factory: f}, r
}

func TestPDFSource_listsOnePagePerPDFPage(t *testing.T) {
	if testing.Short() {
		t.Skip("pdfium's first wasm compile costs seconds")
	}
	const pages = 9
	f, _ := newPDFFixture(t, map[string]any{
		"series": map[string]any{"vol1.pdf": pdfFixture(t, pages, 200, 300)},
	})
	src := f.open(t, source.KindPDF, "series/vol1.pdf")

	if src.Kind() != source.KindPDF {
		t.Errorf("Kind() = %q, want %q", src.Kind(), source.KindPDF)
	}
	list, err := src.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list.Pages) != pages {
		t.Fatalf("pages = %d, want %d", len(list.Pages), pages)
	}
	for i, p := range list.Pages {
		if p.No != i+1 {
			t.Errorf("page %d has No = %d", i+1, p.No)
		}
		// arch §3.5: a PDF page has no entry path.
		if p.EntryPath != "" {
			t.Errorf("page %d has entry_path %q, want empty", p.No, p.EntryPath)
		}
		// What /pages/{n} actually returns.
		if p.Ext != ".jpg" {
			t.Errorf("page %d ext = %q, want .jpg", p.No, p.Ext)
		}
	}
}

// FR-SRV-006: the render resolution is a request parameter, clamped to
// pdf.max_width and snapped so a slider drag cannot multiply cache entries.
func TestPDFSource_openRendersAtTheRequestedWidth(t *testing.T) {
	if testing.Short() {
		t.Skip("pdfium's first wasm compile costs seconds")
	}
	f, _ := newPDFFixture(t, map[string]any{
		"series": map[string]any{"vol1.pdf": pdfFixture(t, 3, 200, 300)},
	})
	src := f.open(t, source.KindPDF, "series/vol1.pdf")
	list, err := src.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	cases := []struct{ ask, want int }{
		{0, 800},     // unset -> the configured default
		{400, 400},   // honoured
		{1234, 1200}, // snapped
		{9000, 2000}, // clamped to PDFMaxWidth
	}
	for _, tc := range cases {
		st, err := src.Open(t.Context(), list.Pages[0], source.OpenOptions{Width: tc.ask})
		if err != nil {
			t.Fatalf("Open at width %d: %v", tc.ask, err)
		}
		if st.ContentType != "image/jpeg" {
			t.Errorf("content type = %q, want image/jpeg (AC-004: the viewer must not be able to tell)", st.ContentType)
		}
		body, err := io.ReadAll(st)
		_ = st.Close()
		if err != nil {
			t.Fatalf("read at width %d: %v", tc.ask, err)
		}
		if int64(len(body)) != st.Size {
			t.Errorf("stream size = %d, body = %d bytes", st.Size, len(body))
		}
		cfg, err := jpeg.DecodeConfig(bytes.NewReader(body))
		if err != nil {
			t.Fatalf("width %d did not produce a decodable JPEG: %v", tc.ask, err)
		}
		if cfg.Width != tc.want {
			t.Errorf("asked for %d, got %d, want %d", tc.ask, cfg.Width, tc.want)
		}
	}
}

// A rendered page is already in memory, so it must be seekable — that is what
// gives the HTTP layer Range for PDF pages too.
func TestPDFSource_renderedPageIsSeekable(t *testing.T) {
	if testing.Short() {
		t.Skip("pdfium's first wasm compile costs seconds")
	}
	f, _ := newPDFFixture(t, map[string]any{
		"series": map[string]any{"vol1.pdf": pdfFixture(t, 1, 100, 150)},
	})
	src := f.open(t, source.KindPDF, "series/vol1.pdf")
	list, err := src.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	st, err := src.Open(t.Context(), list.Pages[0], source.OpenOptions{Width: 200})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = st.Close() }()
	rs, ok := st.ReadSeeker()
	if !ok {
		t.Fatal("a rendered PDF page is not seekable")
	}
	if _, err := rs.Seek(0, io.SeekStart); err != nil {
		t.Errorf("Seek: %v", err)
	}
}

func TestPDFSource_corruptDocument_isStatusError(t *testing.T) {
	if testing.Short() {
		t.Skip("pdfium's first wasm compile costs seconds")
	}
	f, _ := newPDFFixture(t, map[string]any{
		"series": map[string]any{"vol1.pdf": []byte("%PDF-1.4\nthis is not a pdf\n%%EOF\n")},
	})
	src := f.open(t, source.KindPDF, "series/vol1.pdf")
	list, err := src.List(t.Context())
	if err == nil {
		t.Fatal("want an error for a corrupt PDF")
	}
	if got := source.StatusOf(err); got != archive.StatusError {
		t.Errorf("status = %q, want %q", got, archive.StatusError)
	}
	if list == nil {
		t.Error("List returned a nil Listing alongside the error")
	}
}

// pdf.enabled=false, expressed as "no renderer wired in". The book must be
// unsupported, not a crash and not a generic error.
func TestPDFSource_withoutARenderer_isErrUnsupported(t *testing.T) {
	t.Parallel()
	f := newFixture(t, map[string]any{
		"series": map[string]any{"vol1.pdf": []byte("%PDF-1.4\n")},
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
}

// AC-004 stated as a property: the three kinds are interchangeable through the
// interface. Nothing here mentions a format.
func TestSources_allThreeKinds_areInterchangeable(t *testing.T) {
	if testing.Short() {
		t.Skip("pdfium's first wasm compile costs seconds")
	}
	jpg := testutil.TinyJPEG(t, 40, 60)
	f, _ := newPDFFixture(t, map[string]any{
		"series": map[string]any{
			"a.zip": testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
				{Name: "1.jpg", Data: jpg}, {Name: "2.jpg", Data: jpg}, {Name: "3.jpg", Data: jpg},
			}}),
			"b":     map[string]any{"1.jpg": jpg, "2.jpg": jpg, "3.jpg": jpg},
			"c.pdf": pdfFixture(t, 3, 200, 300),
		},
	})

	for _, tc := range []struct {
		kind source.Kind
		rel  string
	}{
		{source.KindZIP, "series/a.zip"},
		{source.KindDir, "series/b"},
		{source.KindPDF, "series/c.pdf"},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			var src source.BookSource = f.open(t, tc.kind, tc.rel)

			list, err := src.List(t.Context())
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(list.Pages) != 3 {
				t.Fatalf("pages = %d, want 3", len(list.Pages))
			}
			for i, p := range list.Pages {
				if p.No != i+1 {
					t.Fatalf("page %d has No = %d", i+1, p.No)
				}
				st, err := src.Open(t.Context(), p, source.OpenOptions{Width: 400})
				if err != nil {
					t.Fatalf("Open page %d: %v", p.No, err)
				}
				body, err := io.ReadAll(st)
				_ = st.Close()
				if err != nil {
					t.Fatalf("read page %d: %v", p.No, err)
				}
				if len(body) == 0 {
					t.Fatalf("page %d came back empty", p.No)
				}
				// Every kind returns an image the browser can render.
				if want := "image/"; len(st.ContentType) < len(want) || st.ContentType[:len(want)] != want {
					t.Errorf("page %d content type = %q, want an image/* type", p.No, st.ContentType)
				}
			}
		})
	}
}
