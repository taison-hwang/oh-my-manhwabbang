package source_test

import (
	"io"
	"slices"
	"strings"
	"testing"

	"shelf/internal/source"
	"shelf/internal/testutil"
)

// pageList turns entry paths into the page list [source.Chapters] partitions.
func pageList(names ...string) []source.Page {
	pages := make([]source.Page, 0, len(names))
	for i, n := range names {
		pages = append(pages, source.Page{No: i + 1, EntryPath: n, Ext: ".jpg", Size: 100})
	}
	return pages
}

func chapterPaths(chs []source.Chapter) []string {
	out := make([]string, 0, len(chs))
	for _, c := range chs {
		out = append(out, c.Path)
	}
	return out
}

// TestChapters_partitionsAContainerOfChapterDirectories is D-73's subject:
// `여자친구 만들고파! 01~08권.zip`, 842 pages in eight per-volume directories,
// indexed until now as one 842-page book.
func TestChapters_partitionsAContainerOfChapterDirectories(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		pages []source.Page
		want  []string
		// counts is the page tally per returned chapter, in the same order.
		counts []int
	}{{
		name:   "one directory per volume",
		pages:  pageList("여친만 01/001.jpg", "여친만 01/002.jpg", "여친만 02/001.jpg"),
		want:   []string{"여친만 01", "여친만 02"},
		counts: []int{2, 1},
	}, {
		// The whole tree packed under one folder. The shared prefix comes off
		// first, or every one of these archives would be a single chapter.
		name:   "a shared wrapper directory is stripped",
		pages:  pageList("시리즈/01/a.jpg", "시리즈/01/b.jpg", "시리즈/02/a.jpg"),
		want:   []string{"시리즈/01", "시리즈/02"},
		counts: []int{2, 1},
	}, {
		// 27 archives of the collection nest deeper inside a chapter. The page
		// belongs to the chapter, not to a chapter of its own.
		name:   "deeper nesting inside a chapter stays in that chapter",
		pages:  pageList("01/a.jpg", "01/컬러/b.jpg", "02/a.jpg"),
		want:   []string{"01", "02"},
		counts: []int{2, 1},
	}, {
		// Natural order, or `10화` sorts between `1화` and `2화` (FR-IDX-007).
		name:   "chapters come back in natural order",
		pages:  pageList("10화/a.jpg", "2화/a.jpg", "1화/a.jpg"),
		want:   []string{"1화", "2화", "10화"},
		counts: []int{1, 1, 1},
	}, {
		// The cover image sitting beside 29 volume directories in
		// `야와라! - YAWARA! (1-29).zip`. It is a 권 of its own rather than a
		// dropped page, and it comes first so the cover ladder's rule 3 finds it.
		name:   "loose top-level pages become the first chapter",
		pages:  pageList("cover.jpg", "01/a.jpg", "02/a.jpg"),
		want:   []string{source.ChapterRoot, "01", "02"},
		counts: []int{1, 1, 1},
	}}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := source.Chapters(tc.pages)
			if !slices.Equal(chapterPaths(got), tc.want) {
				t.Fatalf("Chapters() = %v, want %v", chapterPaths(got), tc.want)
			}
			total := 0
			for i, c := range got {
				if c.Pages != tc.counts[i] {
					t.Errorf("chapter %q holds %d pages, want %d", c.Path, c.Pages, tc.counts[i])
				}
				total += c.Pages
			}
			// The partition is total: a rule that split a book by dropping part
			// of it would trade one wrong page list for a quieter one.
			if total != len(tc.pages) {
				t.Errorf("the chapters hold %d of %d pages", total, len(tc.pages))
			}
		})
	}
}

// TestChapters_leavesOrdinaryBooksAlone is the other half, and the important
// half: 94.9% of the collection's archives must come back unchanged.
func TestChapters_leavesOrdinaryBooksAlone(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		pages []source.Page
	}{{
		name:  "a flat archive",
		pages: pageList("001.jpg", "002.jpg", "003.jpg"),
	}, {
		// The single wrapper directory, which is very common and is one volume
		// packed with its folder — not a container of chapters.
		name:  "one directory holding everything",
		pages: pageList("01권/001.jpg", "01권/002.jpg"),
	}, {
		name:  "one directory, nested",
		pages: pageList("a/b/001.jpg", "a/b/002.jpg"),
	}, {
		name:  "a single page",
		pages: pageList("01/001.jpg"),
	}, {
		// The one shape where "which directory is this page's chapter" has two
		// defensible answers. No archive in the collection is this, and a rule
		// invented for a file that does not exist is how the wrong rule ships.
		name:  "a wrapper directory with pages loose inside it",
		pages: pageList("시리즈/cover.jpg", "시리즈/01/a.jpg", "시리즈/02/a.jpg"),
	}, {
		// One volume's folder with its cover sitting beside it — 13 archives of
		// the collection. "Two or more directories" is what keeps this one book:
		// a 권 list of `… (loose pages)` and `01` is a worse answer than the
		// volume it replaced, and there is no second chapter to justify it.
		name:  "one directory with a cover loose beside it",
		pages: pageList("cover.jpg", "01/a.jpg", "01/b.jpg"),
	}, {
		// A DOS-era archive. Splitting on a separator the entry names do not use
		// would produce prefixes that match nothing.
		name:  "backslash separators",
		pages: pageList(`01\001.jpg`, `02\001.jpg`),
	}}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := source.Chapters(tc.pages); got != nil {
				t.Errorf("Chapters() = %v, want nil — this is one book", chapterPaths(got))
			}
		})
	}
}

// TestNestedDir_servesOneChapterOfTheContainer is the source half of D-73: a
// `nesteddir` book lists its own directory's pages and nobody else's, and those
// pages stream out of the container at the container's own offsets.
func TestNestedDir_servesOneChapterOfTheContainer(t *testing.T) {
	t.Parallel()

	first, second := testutil.TinyJPEG(t, 8, 8), testutil.TinyJPEG(t, 16, 16)
	container := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
		{RawName: testutil.CP949(t, "여친만 01/001.jpg"), Data: first, Method: testutil.MethodStore},
		{RawName: testutil.CP949(t, "여친만 01/002.jpg"), Data: second, Method: testutil.MethodDeflate},
		{RawName: testutil.CP949(t, "여친만 01/Thumbs.db"), Data: []byte("junk"), Method: testutil.MethodStore},
		{RawName: testutil.CP949(t, "여친만 02/001.jpg"), Data: first, Method: testutil.MethodStore},
		// A sibling whose name *starts with* this chapter's. The separator in
		// the prefix test is the whole of what keeps it out: without it, `여친만
		// 01` swallows every page of `여친만 01 특별편` and the two 권 become one.
		{RawName: testutil.CP949(t, "여친만 01 특별편/001.jpg"), Data: first, Method: testutil.MethodStore},
	}})
	f := newFixture(t, map[string]any{"컨테이너.zip": container})

	src, err := f.factory.Open(t.Context(), source.Book{
		ID: "bk", Kind: source.KindNestedDir, RootName: rootName,
		RelPath: "컨테이너.zip", InnerPath: "여친만 01",
	})
	if err != nil {
		t.Fatalf("opening the chapter: %v", err)
	}
	defer func() { _ = src.Close() }()

	if got := src.Kind(); got != source.KindNestedDir {
		t.Errorf("Kind() = %q, want %q", got, source.KindNestedDir)
	}

	l, err := src.List(t.Context())
	if err != nil {
		t.Fatalf("listing the chapter: %v", err)
	}
	want := []string{"여친만 01/001.jpg", "여친만 01/002.jpg"}
	got := make([]string, 0, len(l.Pages))
	for _, p := range l.Pages {
		got = append(got, p.EntryPath)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("pages = %v, want %v — a chapter lists its own directory only", got, want)
	}
	// FR-IDX-006 applies inside a chapter exactly as it does anywhere else, and
	// the count belongs to this chapter rather than to the whole container.
	if l.Excluded != 1 {
		t.Errorf("excluded = %d, want 1 (the Thumbs.db in this chapter)", l.Excluded)
	}

	for i, p := range l.Pages {
		st, err := src.Open(t.Context(), p, source.OpenOptions{})
		if err != nil {
			t.Fatalf("streaming page %d: %v", p.No, err)
		}
		body, err := io.ReadAll(st)
		_ = st.Close()
		if err != nil {
			t.Fatalf("reading page %d: %v", p.No, err)
		}
		wantBytes := [][]byte{first, second}[i]
		if string(body) != string(wantBytes) {
			t.Errorf("page %d is %d bytes, want the %d of the source image", p.No, len(body), len(wantBytes))
		}
	}
}

// A chapter that no longer exists — the container was repacked — is `empty`,
// which is the honest verdict for a directory with nothing in it (ruling E-14).
// It is not an error, and it must not be the *container's* page list either.
func TestNestedDir_missingChapter_isEmptyNotTheWholeContainer(t *testing.T) {
	t.Parallel()

	container := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
		{Name: "01/001.jpg", Data: testutil.TinyJPEG(t, 8, 8), Flags: testutil.FlagUTF8},
		{Name: "02/001.jpg", Data: testutil.TinyJPEG(t, 8, 8), Flags: testutil.FlagUTF8},
	}})
	f := newFixture(t, map[string]any{"컨테이너.zip": container})

	src, err := f.factory.Open(t.Context(), source.Book{
		ID: "bk", Kind: source.KindNestedDir, RootName: rootName,
		RelPath: "컨테이너.zip", InnerPath: "03",
	})
	if err != nil {
		t.Fatalf("opening the chapter: %v", err)
	}
	defer func() { _ = src.Close() }()

	l, err := src.List(t.Context())
	if err == nil || source.StatusOf(err) != "empty" {
		t.Fatalf("err = %v (status %q), want an empty book", err, source.StatusOf(err))
	}
	if len(l.Pages) != 0 {
		t.Errorf("pages = %d, want 0 — a missing chapter must not fall back to the container", len(l.Pages))
	}
}

// A chapter book with no inner path names no chapter at all. Serving the whole
// container under a chapter's id would be a wrong page list that looks measured.
func TestNestedDir_withoutAnInnerPath_isUnsupported(t *testing.T) {
	t.Parallel()

	container := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
		{Name: "01/001.jpg", Data: testutil.TinyJPEG(t, 8, 8), Flags: testutil.FlagUTF8},
	}})
	f := newFixture(t, map[string]any{"컨테이너.zip": container})

	_, err := f.factory.Open(t.Context(), source.Book{
		ID: "bk", Kind: source.KindNestedDir, RootName: rootName, RelPath: "컨테이너.zip",
	})
	if err == nil || source.StatusOf(err) != "unsupported" {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
	if !strings.Contains(err.Error(), "inner path") {
		t.Errorf("err = %v, want it to say what is missing", err)
	}
}

// A source narrowed to one chapter answers about that chapter only — including
// the volume question, which is asked of a container by the scanner.
func TestNestedDir_volumes_areTheChaptersOwn(t *testing.T) {
	t.Parallel()

	inner := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
		{Name: "001.jpg", Data: testutil.TinyJPEG(t, 8, 8), Flags: testutil.FlagUTF8},
	}})
	container := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
		{Name: "01/001.jpg", Data: testutil.TinyJPEG(t, 8, 8), Flags: testutil.FlagUTF8},
		{Name: "01/bonus.zip", Data: inner, Flags: testutil.FlagUTF8},
		{Name: "02/other.zip", Data: inner, Flags: testutil.FlagUTF8},
	}})
	f := newFixture(t, map[string]any{"컨테이너.zip": container})

	src, err := f.factory.Open(t.Context(), source.Book{
		ID: "bk", Kind: source.KindNestedDir, RootName: rootName,
		RelPath: "컨테이너.zip", InnerPath: "01",
	})
	if err != nil {
		t.Fatalf("opening the chapter: %v", err)
	}
	defer func() { _ = src.Close() }()

	lister, ok := src.(source.VolumeLister)
	if !ok {
		t.Fatal("a chapter source is not a VolumeLister")
	}
	vols, err := lister.Volumes(t.Context())
	if err != nil {
		t.Fatalf("Volumes: %v", err)
	}
	if !slices.Equal(vols, []string{"01/bonus.zip"}) {
		t.Errorf("Volumes() = %v, want only this chapter's %q", vols, "01/bonus.zip")
	}
}
