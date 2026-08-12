package source_test

import (
	"bytes"
	"errors"
	"io"
	"slices"
	"testing"

	"shelf/internal/archive"
	"shelf/internal/source"
	"shelf/internal/testutil"
)

// A RAR book must be indistinguishable from a ZIP book everywhere above this
// package. These tests assert that by asserting the same things the ZIP tests
// assert, on the same shapes: the FR-IDX-006 exclusions, the FR-IDX-007 natural
// order, the CP949 name decoding, Range support on a stored page.
//
// That sameness is the design claim of D-71. It holds because none of it is
// reimplemented — a RAR book differs from a ZIP book by which archive.Reader
// the Factory handed its containerSource, and nothing else.

func rarBook(t *testing.T, entries ...testutil.RAR4Entry) []byte {
	t.Helper()
	return testutil.BuildRAR4(t, testutil.RAR4Spec{Entries: entries})
}

func TestRARSource_listAndStream(t *testing.T) {
	t.Parallel()

	page1 := testutil.TinyJPEG(t, 8, 8)
	page2 := testutil.TinyJPEG(t, 16, 16)
	raw := rarBook(t,
		testutil.RAR4Entry{Name: testutil.CP949(t, `한글권\002.jpg`), Data: page2},
		testutil.RAR4Entry{Name: testutil.CP949(t, `한글권\001.jpg`), Data: page1},
		testutil.RAR4Entry{Name: []byte(`한글권`), Dir: true},
		testutil.RAR4Entry{Name: []byte(`한글권\Thumbs.db`), Data: []byte("junk")},
		testutil.RAR4Entry{Name: []byte(`한글권\readme.txt`), Data: []byte("메모")},
	)
	f := newFixture(t, map[string]any{"책.rar": raw})
	src := f.open(t, source.KindRAR, "책.rar")

	if got := src.Kind(); got != source.KindRAR {
		t.Errorf("Kind() = %q, want %q", got, source.KindRAR)
	}

	l, err := src.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	// FR-IDX-007: natural order, not the container's order. The archive stores
	// 002 before 001 on purpose.
	want := []string{"한글권/001.jpg", "한글권/002.jpg"}
	if got := pageNames(l.Pages); !slices.Equal(got, want) {
		t.Errorf("pages = %v, want %v", got, want)
	}
	// FR-IDX-006: the directory entry, Thumbs.db and the .txt are all dropped,
	// by the same predicate that drops them out of a ZIP.
	if l.Excluded != 3 {
		t.Errorf("Excluded = %d, want 3", l.Excluded)
	}
	if l.NameEncoding != "cp949" {
		t.Errorf("NameEncoding = %q, want cp949", l.NameEncoding)
	}
	if l.TotalBytes != int64(len(page1)+len(page2)) {
		t.Errorf("TotalBytes = %d, want %d", l.TotalBytes, len(page1)+len(page2))
	}

	for i, want := range [][]byte{page1, page2} {
		if got := readPage(t, src, l.Pages[i]); !bytes.Equal(got, want) {
			t.Errorf("page %d: %d bytes, want %d", i+1, len(got), len(want))
		}
	}
}

// arch §5.3: a stored page must come back seekable so http.ServeContent can
// answer a Range request. Every page of 12 of the collection's 14 RAR archives
// is stored, so losing this would lose Range for almost all of them.
func TestRARSource_storedPageIsSeekable(t *testing.T) {
	t.Parallel()

	page := testutil.TinyJPEG(t, 8, 8)
	f := newFixture(t, map[string]any{
		"책.rar": rarBook(t, testutil.RAR4Entry{Name: []byte("001.jpg"), Data: page}),
	})
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

	rs, ok := st.ReadSeeker()
	if !ok {
		t.Fatalf("stored RAR page is %T, which is not an io.ReadSeeker", st.ReadCloser)
	}
	if _, err := rs.Seek(int64(len(page)-4), io.SeekStart); err != nil {
		t.Fatalf("Seek: %v", err)
	}
	got, err := io.ReadAll(rs)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, page[len(page)-4:]) {
		t.Errorf("after seek got % x, want % x", got, page[len(page)-4:])
	}
	if st.ContentType != "image/jpeg" {
		t.Errorf("ContentType = %q, want image/jpeg", st.ContentType)
	}
}

func TestRARSource_failureStatuses(t *testing.T) {
	t.Parallel()

	page := testutil.TinyJPEG(t, 8, 8)
	cases := []struct {
		name   string
		raw    []byte
		status archive.Status
	}{
		{
			name: "an encrypted entry is flagged, never decoded",
			raw: rarBook(t, testutil.RAR4Entry{
				Name: []byte("001.jpg"), Data: page, Flags: testutil.RARFilePassword,
			}),
			status: archive.StatusEncrypted,
		},
		{
			name: "a solid archive is refused as unsupported, not as corrupt",
			raw: testutil.BuildRAR4(t, testutil.RAR4Spec{
				MainFlags: testutil.RARMainSolid,
				Entries:   []testutil.RAR4Entry{{Name: []byte("001.jpg"), Data: page}},
			}),
			status: archive.StatusUnsupported,
		},
		{
			name:   "nothing but junk is empty, not an error",
			raw:    rarBook(t, testutil.RAR4Entry{Name: []byte("Thumbs.db"), Data: []byte("junk")}),
			status: archive.StatusEmpty,
		},
		{
			name: "a truncated archive is corrupt",
			raw: testutil.BuildRAR4(t, testutil.RAR4Spec{
				Entries:      []testutil.RAR4Entry{{Name: []byte("001.jpg"), Data: page}},
				TruncateTail: len(page) / 2,
			}),
			status: archive.StatusError,
		},
		{
			name:   "a file that is not a RAR at all is corrupt",
			raw:    []byte("PK\x03\x04 this is a zip"),
			status: archive.StatusError,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t, map[string]any{"책.rar": tc.raw})
			src := f.open(t, source.KindRAR, "책.rar")
			_, err := src.List(t.Context())
			if err == nil {
				t.Fatal("List succeeded; expected a failure")
			}
			if got := source.StatusOf(err); got != tc.status {
				t.Errorf("StatusOf(%v) = %q, want %q", err, got, tc.status)
			}
		})
	}
}

// books.error must name the format that failed. Before D-71 this string was
// hard-coded to "zip:", which would have had a RAR book reporting itself as a
// ZIP in the scan log and in the UI.
func TestRARSource_encryptedErrorNamesRARNotZIP(t *testing.T) {
	t.Parallel()

	f := newFixture(t, map[string]any{
		"책.rar": rarBook(t, testutil.RAR4Entry{
			Name: []byte("001.jpg"), Data: testutil.TinyJPEG(t, 8, 8),
			Flags: testutil.RARFilePassword,
		}),
	})
	src := f.open(t, source.KindRAR, "책.rar")

	_, err := src.List(t.Context())
	if err == nil {
		t.Fatal("List succeeded; expected an encrypted failure")
	}
	if !errors.Is(err, archive.ErrEncrypted) {
		t.Fatalf("errors.Is(%v, archive.ErrEncrypted) = false", err)
	}
	if !bytes.Contains([]byte(err.Error()), []byte("rar:")) {
		t.Errorf("error %q does not name rar", err)
	}
	if bytes.Contains([]byte(err.Error()), []byte("zip:")) {
		t.Errorf("error %q calls a RAR book a zip", err)
	}
}

// A container holding volumes of both formats yields all of them. Under D-07
// Volumes() returned only the ZIPs, which is why `사모님은 학생회장.zip` was 7
// books rather than 15.
func TestVolumes_returnsBothFormats(t *testing.T) {
	t.Parallel()

	zipVol := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
		{Name: "001.jpg", Data: testutil.TinyJPEG(t, 8, 8), Flags: testutil.FlagUTF8},
	}})
	rarVol := rarBook(t, testutil.RAR4Entry{Name: []byte("001.jpg"), Data: testutil.TinyJPEG(t, 8, 8)})
	outer := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
		{Name: "01권.zip", Data: zipVol, Flags: testutil.FlagUTF8},
		{Name: "02권.rar", Data: rarVol, Flags: testutil.FlagUTF8},
		{Name: "03권.7z", Data: []byte("7z\xbc\xaf\x27\x1cnope"), Flags: testutil.FlagUTF8},
		{Name: "표지.jpg", Data: testutil.TinyJPEG(t, 8, 8), Flags: testutil.FlagUTF8},
	}})

	f := newFixture(t, map[string]any{"컨테이너.zip": outer})
	src := f.open(t, source.KindZIP, "컨테이너.zip")

	lister, ok := src.(source.VolumeLister)
	if !ok {
		t.Fatalf("%T does not implement source.VolumeLister", src)
	}
	got, err := lister.Volumes(t.Context())
	if err != nil {
		t.Fatalf("Volumes: %v", err)
	}
	want := []string{"01권.zip", "02권.rar"}
	if !slices.Equal(got, want) {
		t.Errorf("Volumes() = %v, want %v — the .7z has no reader and the .jpg is a page", got, want)
	}
}

// The nested RAR volume, read end to end: an outer ZIP, an inner RAR, and the
// pages coming back out of it.
func TestNestedRARSource_listAndStream(t *testing.T) {
	t.Parallel()

	page1 := testutil.TinyJPEG(t, 8, 8)
	page2 := testutil.TinyJPEG(t, 16, 16)
	rarVol := rarBook(t,
		testutil.RAR4Entry{Name: testutil.CP949(t, `2권\001.jpg`), Data: page1},
		testutil.RAR4Entry{Name: testutil.CP949(t, `2권\002.jpg`), Data: page2},
	)
	for _, method := range []struct {
		name string
		m    uint16
	}{
		{"stored inner volume", testutil.MethodStore},
		{"deflated inner volume", testutil.MethodDeflate},
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

			if got := src.Kind(); got != source.KindNestedRAR {
				t.Errorf("Kind() = %q, want %q", got, source.KindNestedRAR)
			}
			l, err := src.List(t.Context())
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			want := []string{"2권/001.jpg", "2권/002.jpg"}
			if got := pageNames(l.Pages); !slices.Equal(got, want) {
				t.Fatalf("pages = %v, want %v", got, want)
			}
			for i, want := range [][]byte{page1, page2} {
				if got := readPage(t, src, l.Pages[i]); !bytes.Equal(got, want) {
					t.Errorf("page %d: %d bytes, want %d", i+1, len(got), len(want))
				}
			}
		})
	}
}
