package source_test

import (
	"bytes"
	"io"
	"slices"
	"testing"

	"shelf/internal/archive"
	"shelf/internal/source"
	"shelf/internal/testutil"
)

// E-71: a RAR 5 book is kind "rar" and opens through the Factory's default
// reader, which must pick the version per container.
func TestRAR5Source_listStreamAndRange(t *testing.T) {
	t.Parallel()

	page1 := testutil.TinyJPEG(t, 8, 8)
	page2 := testutil.TinyJPEG(t, 16, 16)
	raw := testutil.BuildRAR5(t, testutil.RAR5Spec{Entries: []testutil.RAR5Entry{
		{Name: []byte("권/002.jpg"), Data: page2},
		{Name: []byte("권/001.jpg"), Data: page1},
		{Name: []byte("권"), Dir: true},
		{Name: []byte("권/Thumbs.db"), Data: []byte("junk")},
	}})
	f := newFixture(t, map[string]any{"책.rar": raw})
	src := f.open(t, source.KindRAR, "책.rar")

	l, err := src.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got, want := pageNames(l.Pages), []string{"권/001.jpg", "권/002.jpg"}; !slices.Equal(got, want) {
		t.Fatalf("pages = %v, want %v", got, want)
	}
	if l.NameEncoding != "utf-8" || l.Excluded != 2 {
		t.Errorf("NameEncoding %q Excluded %d, want utf-8 and 2", l.NameEncoding, l.Excluded)
	}
	for i, want := range [][]byte{page1, page2} {
		if got := readPage(t, src, l.Pages[i]); !bytes.Equal(got, want) {
			t.Errorf("page %d: %d bytes, want %d", i+1, len(got), len(want))
		}
	}

	st, err := src.Open(t.Context(), l.Pages[1], source.OpenOptions{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = st.Close() }()
	rs, ok := st.ReadSeeker()
	if !ok {
		t.Fatalf("stored RAR 5 page is %T, not an io.ReadSeeker", st.ReadCloser)
	}
	if _, err := rs.Seek(int64(len(page2)-4), io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if got, _ := io.ReadAll(rs); !bytes.Equal(got, page2[len(page2)-4:]) {
		t.Errorf("after seek got % x", got)
	}
}

func TestRAR5Source_refusalsKeepTheirStatus(t *testing.T) {
	t.Parallel()

	page := testutil.RAR5Entry{Name: []byte("001.jpg"), Data: testutil.TinyJPEG(t, 8, 8)}
	enc := page
	enc.Encrypted = true
	cases := []struct {
		name   string
		spec   testutil.RAR5Spec
		status archive.Status
	}{
		{"solid", testutil.RAR5Spec{ArcFlags: testutil.RAR5ArcSolid, Entries: []testutil.RAR5Entry{page}}, archive.StatusUnsupported},
		{"encrypted", testutil.RAR5Spec{Entries: []testutil.RAR5Entry{enc}}, archive.StatusEncrypted},
		{"truncated", testutil.RAR5Spec{Entries: []testutil.RAR5Entry{page}, TruncateTail: 40}, archive.StatusError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t, map[string]any{"책.rar": testutil.BuildRAR5(t, tc.spec)})
			_, err := f.open(t, source.KindRAR, "책.rar").List(t.Context())
			if got := source.StatusOf(err); got != tc.status {
				t.Errorf("StatusOf(%v) = %q, want %q", err, got, tc.status)
			}
		})
	}
}
