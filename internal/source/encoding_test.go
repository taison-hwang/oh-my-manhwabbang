package source_test

import (
	"testing"

	"shelf/internal/kenc"
	"shelf/internal/source"
	"shelf/internal/testutil"
)

// Ruling E-56 records each book's kenc label in books.name_encoding, and
// scanner.unchanged refuses to skip an ok book whose label is empty — that is
// how the first scan after the v3 migration fills the column for a library
// whose files have not changed. The same rule makes an empty label expensive:
// a kind whose successful listing comes back unlabelled would have every one of
// its books re-read on every scan, for ever, with nothing failing.
//
// So this is the guard, and it is driven by the registry rather than by a list
// of kinds: a kind added to the Factory without a case here fails the test
// instead of slipping past it.

// labelCase opens one book of a kind whose listing must succeed.
type labelCase func(t *testing.T) source.BookSource

// labelCases holds a case per registered kind. The PDF case lives in a file of
// its own (encoding_pdf_test.go) because only a build with pdfium can list a
// PDF successfully; labelSkips says why a build without it has none.
var labelCases = map[source.Kind]labelCase{
	source.KindZIP: func(t *testing.T) source.BookSource {
		f := newFixture(t, map[string]any{"01권.zip": testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
			{RawName: testutil.CP949(t, "군계 001.jpg"), Data: testutil.TinyJPEG(t, 8, 8)},
		}})})
		return f.open(t, source.KindZIP, "01권.zip")
	},
	source.KindRAR: func(t *testing.T) source.BookSource {
		f := newFixture(t, map[string]any{"01권.rar": rarBook(t,
			testutil.RAR4Entry{Name: testutil.CP949(t, `한글권\001.jpg`), Data: testutil.TinyJPEG(t, 8, 8)},
		)})
		return f.open(t, source.KindRAR, "01권.rar")
	},
	source.KindHV3: func(t *testing.T) source.BookSource {
		f := newFixture(t, map[string]any{"책.hv3": hv3Book(t, testutil.HV3EncrMask,
			testutil.HV3Entry{Name: "001.jpg", Data: testutil.TinyJPEG(t, 8, 8)},
		)})
		return f.open(t, source.KindHV3, "책.hv3")
	},
	source.KindDir: func(t *testing.T) source.BookSource {
		f := newFixture(t, map[string]any{"01권": map[string]any{"001.jpg": testutil.TinyJPEG(t, 8, 8)}})
		return f.open(t, source.KindDir, "01권")
	},
	source.KindNestedZIP: func(t *testing.T) source.BookSource {
		inner := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
			{Name: "001.jpg", Data: testutil.TinyJPEG(t, 8, 8), Flags: testutil.FlagUTF8},
		}})
		return openNested(t, source.KindNestedZIP, "02권.zip", inner)
	},
	source.KindNestedRAR: func(t *testing.T) source.BookSource {
		inner := rarBook(t, testutil.RAR4Entry{Name: testutil.CP949(t, `2권\001.jpg`), Data: testutil.TinyJPEG(t, 8, 8)})
		return openNested(t, source.KindNestedRAR, "02권.rar", inner)
	},
	source.KindNestedHV3: func(t *testing.T) source.BookSource {
		inner := hv3Book(t, testutil.HV3EncrMask, testutil.HV3Entry{Name: "0001.jpg", Data: testutil.TinyJPEG(t, 8, 8)})
		return openNested(t, source.KindNestedHV3, "04.hv3", inner)
	},
	source.KindNestedDir: func(t *testing.T) source.BookSource {
		container := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
			{RawName: testutil.CP949(t, "여친만 01/001.jpg"), Data: testutil.TinyJPEG(t, 8, 8)},
			{RawName: testutil.CP949(t, "여친만 02/001.jpg"), Data: testutil.TinyJPEG(t, 8, 8)},
		}})
		f := newFixture(t, map[string]any{"컨테이너.zip": container})
		return openBook(t, f, source.Book{
			ID: "bk", Kind: source.KindNestedDir, RootName: rootName,
			RelPath: "컨테이너.zip", InnerPath: "여친만 01",
		})
	},
}

// labelSkips names the registered kinds this build cannot list successfully,
// and why that is safe: a book that is never `ok` is re-examined every scan by
// E-39 regardless, so its label is not what decides.
var labelSkips = map[source.Kind]string{}

func openNested(t *testing.T, kind source.Kind, innerName string, inner []byte) source.BookSource {
	t.Helper()
	outer := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
		{Name: innerName, Data: inner, Method: testutil.MethodDeflate, Flags: testutil.FlagUTF8},
	}})
	f := newFixture(t, map[string]any{"컨테이너.zip": outer})
	return openBook(t, f, source.Book{
		ID: "bk", Kind: kind, RootName: rootName, RelPath: "컨테이너.zip", InnerPath: innerName,
	})
}

func openBook(t *testing.T, f *fixture, b source.Book) source.BookSource {
	t.Helper()
	src, err := f.factory.Open(t.Context(), b)
	if err != nil {
		t.Fatalf("Factory.Open(%s %q %q): %v", b.Kind, b.RelPath, b.InnerPath, err)
	}
	t.Cleanup(func() { _ = src.Close() })
	return src
}

func TestEverySourceKind_labelsASuccessfulListing(t *testing.T) {
	t.Parallel()
	f := newFixture(t, map[string]any{"book": map[string]any{"001.jpg": testutil.TinyJPEG(t, 8, 8)}})

	for _, kind := range f.factory.Kinds() {
		build, ok := labelCases[kind]
		if !ok {
			if why, skip := labelSkips[kind]; skip {
				t.Logf("kind %s: no successful listing in this build (%s)", kind, why)
				continue
			}
			t.Errorf("kind %s is registered but has no case here. Add one: an ok book of this kind "+
				"listed without a name encoding is re-read on every scan (scanner.unchanged, E-56).", kind)
			continue
		}
		t.Run(string(kind), func(t *testing.T) {
			t.Parallel()
			src := build(t)
			l, err := src.List(t.Context())
			if err != nil {
				t.Fatalf("List: %v — the case must be a listing that succeeds", err)
			}
			if len(l.Pages) == 0 {
				t.Fatal("List succeeded with no pages; the case must be an ok book")
			}
			if l.NameEncoding == "" {
				t.Fatalf("a successful %s listing has no NameEncoding: every such book would be "+
					"re-listed on every scan", kind)
			}
			if kenc.MostSuspect(l.NameEncoding) != l.NameEncoding {
				t.Errorf("NameEncoding = %q, which is not one of kenc's labels", l.NameEncoding)
			}
		})
	}
}
