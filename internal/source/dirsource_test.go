package source_test

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"shelf/internal/archive"
	"shelf/internal/source"
	"shelf/internal/testutil"
)

// FR-IDX-007 is the requirement most likely to be silently wrong, because
// lexicographic order looks almost right. arch §4.7's verified table, as pages
// on disk.
func TestDirSource_naturalPageOrder(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		files []string
		want  []string
	}{
		{
			name:  "plain numbers",
			files: []string{"10.jpg", "1.jpg", "2.jpg", "20.jpg", "3.jpg"},
			want:  []string{"1.jpg", "2.jpg", "3.jpg", "10.jpg", "20.jpg"},
		},
		{
			name:  "mixed zero padding",
			files: []string{"001.jpg", "10.jpg", "1.jpg", "01.jpg", "002.jpg", "2.jpg"},
			want:  []string{"1.jpg", "01.jpg", "001.jpg", "2.jpg", "002.jpg", "10.jpg"},
		},
		{
			// The `자살도114-122` shape: 181 loose images numbered without
			// padding, the only instance of prd §2.2 row 3 in the collection.
			name:  "unpadded hundreds",
			files: []string{"100.jpg", "1.jpg", "9.jpg", "10.jpg", "99.jpg", "181.jpg"},
			want:  []string{"1.jpg", "9.jpg", "10.jpg", "99.jpg", "100.jpg", "181.jpg"},
		},
		{
			name:  "prefixed",
			files: []string{"page-9.png", "page-10.png", "page-1.png", "page-100.png"},
			want:  []string{"page-1.png", "page-9.png", "page-10.png", "page-100.png"},
		},
		{
			name:  "hangul and latin",
			files: []string{"가.jpg", "cover.jpg", "0001.jpg", "z.jpg"},
			want:  []string{"0001.jpg", "cover.jpg", "z.jpg", "가.jpg"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			layout := map[string]any{}
			for i, name := range tc.files {
				layout[name] = testutil.TinyJPEG(t, 8+i, 8)
			}
			f := newFixture(t, map[string]any{"series": map[string]any{"vol1": layout}})
			src := f.open(t, source.KindDir, "series/vol1")

			list, err := src.List(t.Context())
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			got := pageNames(list.Pages)
			if len(got) != len(tc.want) {
				t.Fatalf("pages = %q, want %q", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("page %d = %q, want %q\n got %q\nwant %q", i+1, got[i], tc.want[i], got, tc.want)
					break
				}
			}
		})
	}
}

// FR-SRV-005 and FR-SRV-008: the file is served from the filesystem, byte for
// byte, with its own mtime.
func TestDirSource_servesOriginalBytesAndMetadata(t *testing.T) {
	t.Parallel()
	jpg := testutil.TinyJPEG(t, 40, 60)
	when := time.Date(2014, time.June, 3, 12, 0, 0, 0, time.UTC)

	f := newFixture(t, map[string]any{
		"series": map[string]any{
			"vol1": map[string]any{
				"001.jpg": testutil.File{Data: jpg, ModTime: when},
			},
		},
	})
	src := f.open(t, source.KindDir, "series/vol1")
	list, err := src.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list.Pages) != 1 {
		t.Fatalf("pages = %d, want 1", len(list.Pages))
	}
	p := list.Pages[0]
	if p.Size != int64(len(jpg)) {
		t.Errorf("size = %d, want %d", p.Size, len(jpg))
	}
	if p.Mtime != when.Unix() {
		t.Errorf("mtime = %d, want %d", p.Mtime, when.Unix())
	}

	st, err := src.Open(t.Context(), p, source.OpenOptions{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = st.Close() }()
	body, err := io.ReadAll(st)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(body) != string(jpg) {
		t.Error("the served bytes differ from the file on disk (FR-SRV-008)")
	}
	if !st.ModTime.Equal(when) {
		t.Errorf("stream mtime = %v, want %v", st.ModTime, when)
	}
	if st.ContentType != "image/jpeg" {
		t.Errorf("content type = %q, want image/jpeg", st.ContentType)
	}
}

// prd §2.2 makes an image sub-folder a book in its own right, so listing must
// not recurse: recursing would merge two volumes into one.
func TestDirSource_doesNotRecurseIntoSubdirectories(t *testing.T) {
	t.Parallel()
	f := newFixture(t, map[string]any{
		"series": map[string]any{
			"vol1": map[string]any{
				"001.jpg": testutil.TinyJPEG(t, 8, 8),
				"002.jpg": testutil.TinyJPEG(t, 8, 8),
				"extra": map[string]any{
					"003.jpg": testutil.TinyJPEG(t, 8, 8),
				},
			},
		},
	})
	src := f.open(t, source.KindDir, "series/vol1")
	list, err := src.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := pageNames(list.Pages); len(got) != 2 {
		t.Errorf("pages = %q, want only the two direct children", got)
	}
	if list.Excluded != 1 {
		t.Errorf("excluded = %d, want 1 (the subdirectory)", list.Excluded)
	}
}

func TestDirSource_emptyOrJunkOnly_isStatusEmpty(t *testing.T) {
	t.Parallel()
	cases := map[string]map[string]any{
		"no files at all": {},
		"only junk": {
			"Thumbs.db":   []byte("junk"),
			".DS_Store":   []byte("junk"),
			"desktop.ini": []byte("junk"),
			"notes.txt":   []byte("text"),
			"empty.jpg":   []byte{},
		},
	}
	for name, layout := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t, map[string]any{"series": map[string]any{"vol1": layout}})
			src := f.open(t, source.KindDir, "series/vol1")
			list, err := src.List(t.Context())
			if !errors.Is(err, source.ErrNoPages) {
				t.Fatalf("err = %v, want source.ErrNoPages", err)
			}
			if got := source.StatusOf(err); got != archive.StatusEmpty {
				t.Errorf("status = %q, want %q", got, archive.StatusEmpty)
			}
			if len(list.Pages) != 0 {
				t.Errorf("pages = %d, want 0", len(list.Pages))
			}
		})
	}
}

func TestDirSource_missingDirectory_isStatusError(t *testing.T) {
	t.Parallel()
	f := newFixture(t, map[string]any{"series": map[string]any{"vol1": map[string]any{
		"001.jpg": testutil.TinyJPEG(t, 8, 8),
	}}})
	src := f.open(t, source.KindDir, "series/gone")
	_, err := src.List(t.Context())
	if err == nil {
		t.Fatal("want an error for a directory that is not there")
	}
	if got := source.StatusOf(err); got != archive.StatusError {
		t.Errorf("status = %q, want %q", got, archive.StatusError)
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("err = %v, want it to wrap os.ErrNotExist", err)
	}
}

// A symlinked page is dropped from the listing rather than indexed and then
// failing at serve time. os.Root refuses it either way; this keeps the index
// honest.
func TestDirSource_symlinkedPagesAreNotListed(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	testutil.BuildTreeAt(t, base, map[string]any{
		"secret.jpg": testutil.TinyJPEG(t, 8, 8),
		"library": map[string]any{
			"vol1": map[string]any{"001.jpg": testutil.TinyJPEG(t, 8, 8)},
		},
	})
	root := filepath.Join(base, "library")
	if err := os.Symlink(filepath.Join(base, "secret.jpg"), filepath.Join(root, "vol1", "002.jpg")); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}

	f := newFixtureAt(t, root)
	src := f.open(t, source.KindDir, "vol1")
	list, err := src.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := pageNames(list.Pages); len(got) != 1 || got[0] != "001.jpg" {
		t.Errorf("pages = %q, want only the real file", got)
	}
}

// Serving many pages from a directory book must not leak descriptors either.
func TestDirSource_streamClose_releasesTheFile(t *testing.T) {
	t.Parallel()
	layout := map[string]any{}
	for i := 1; i <= 20; i++ {
		layout[fmt.Sprintf("%03d.jpg", i)] = testutil.TinyJPEG(t, 8, 8)
	}
	f := newFixture(t, map[string]any{"series": map[string]any{"vol1": layout}})
	src := f.open(t, source.KindDir, "series/vol1")
	list, err := src.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for round := 0; round < 20; round++ {
		for _, p := range list.Pages {
			st, err := src.Open(t.Context(), p, source.OpenOptions{})
			if err != nil {
				t.Fatalf("Open %q: %v", p.EntryPath, err)
			}
			if _, err := io.ReadAll(st); err != nil {
				t.Fatalf("read %q: %v", p.EntryPath, err)
			}
			if err := st.Close(); err != nil {
				t.Fatalf("close %q: %v", p.EntryPath, err)
			}
		}
	}
	// 400 opens with no close would exhaust a low ulimit; that this test
	// completes at all is most of the assertion.
	if len(list.Pages) != 20 {
		t.Fatalf("pages = %d, want 20", len(list.Pages))
	}
}
