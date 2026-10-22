package source_test

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/nwaples/rardecode/v2"

	"shelf/internal/source"
	"shelf/internal/testutil"
)

// A packed RAR page through the Factory: the same bytes the rar4 reader
// decodes, and — arch §5.3 — no ReadSeeker, so the HTTP layer serves it
// forward-only without Accept-Ranges. The page is named .jpg so FR-IDX-006
// keeps it; the content type comes from the extension, never from sniffing.
func packedRAREntry(crc uint32) testutil.RAR4Entry {
	e := testutil.RARPackedSampleEntry([]byte("001.jpg"))
	e.CRC = crc
	return e
}

func TestRARSource_packedPageStreamsForwardOnly(t *testing.T) {
	t.Parallel()

	f := newFixture(t, map[string]any{"책.rar": rarBook(t, packedRAREntry(testutil.RARPackedSampleCRC))})
	src := f.open(t, source.KindRAR, "책.rar")
	l, err := src.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(l.Pages) != 1 {
		t.Fatalf("pages = %d, want 1", len(l.Pages))
	}
	st, err := src.Open(t.Context(), l.Pages[0], source.OpenOptions{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = st.Close() }()
	if _, ok := st.ReadSeeker(); ok {
		t.Error("a packed RAR page claims to be seekable; Range would be advertised on a forward-only stream")
	}
	if want := int64(len(testutil.RARPackedSampleText)); st.Size != want {
		t.Errorf("Size = %d, want %d (Content-Length comes from it)", st.Size, want)
	}
	got, err := io.ReadAll(st)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != testutil.RARPackedSampleText {
		t.Errorf("page = %q, want %q", got, testutil.RARPackedSampleText)
	}
}

func TestRARSource_packedPageWithAWrongCRC_isAReadError(t *testing.T) {
	t.Parallel()

	f := newFixture(t, map[string]any{"책.rar": rarBook(t, packedRAREntry(testutil.RARPackedSampleCRC^1))})
	src := f.open(t, source.KindRAR, "책.rar")
	l, err := src.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	st, err := src.Open(t.Context(), l.Pages[0], source.OpenOptions{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = st.Close() }()
	got, err := io.ReadAll(st)
	if !errors.Is(err, rardecode.ErrBadFileChecksum) {
		t.Fatalf("ReadAll error = %v, want rardecode.ErrBadFileChecksum", err)
	}
	// Judged at EOF, so the whole body comes first.
	if string(got) != testutil.RARPackedSampleText {
		t.Errorf("read %q before the error, want the whole %q", got, testutil.RARPackedSampleText)
	}
}

// KindNestedRAR: the packed RAR volume sits inside a ZIP, stored or deflated.
func TestNestedRARSource_packedInnerVolume(t *testing.T) {
	t.Parallel()

	rarVol := rarBook(t, packedRAREntry(testutil.RARPackedSampleCRC))
	for _, method := range []struct {
		name string
		m    uint16
	}{
		{"stored outer entry", testutil.MethodStore},
		{"deflated outer entry", testutil.MethodDeflate},
	} {
		t.Run(method.name, func(t *testing.T) {
			t.Parallel()
			outer := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
				{Name: "02권.rar", Data: rarVol, Method: method.m, Flags: testutil.FlagUTF8},
			}})
			f := newFixture(t, map[string]any{"컨테이너.zip": outer})
			src, err := f.factory.Open(t.Context(), source.Book{
				ID: "bk", Kind: source.KindNestedRAR, RootName: rootName,
				RelPath: "컨테이너.zip", InnerPath: "02권.rar",
			})
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			defer func() { _ = src.Close() }()
			l, err := src.List(t.Context())
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(l.Pages) != 1 {
				t.Fatalf("pages = %d, want 1", len(l.Pages))
			}
			st, err := src.Open(t.Context(), l.Pages[0], source.OpenOptions{})
			if err != nil {
				t.Fatalf("Open page: %v", err)
			}
			defer func() { _ = st.Close() }()
			if _, ok := st.ReadSeeker(); ok {
				t.Error("a packed page inside a nested RAR volume claims to be seekable")
			}
			got, err := io.ReadAll(st)
			if err != nil {
				t.Fatalf("ReadAll: %v", err)
			}
			if !bytes.Equal(got, []byte(testutil.RARPackedSampleText)) {
				t.Errorf("page = %q, want %q", got, testutil.RARPackedSampleText)
			}
		})
	}
}
