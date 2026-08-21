package scanner

import (
	"io"
	"maps"
	"slices"
	"strings"
	"testing"

	"shelf/internal/index"
	"shelf/internal/source"
	"shelf/internal/testutil"
)

// chapterZIP builds a container whose pages live in per-chapter directories,
// with CP949 names — the shape 484 archives of the collection have.
func chapterZIP(t *testing.T, chapters map[string]int, loose ...string) []byte {
	t.Helper()
	var entries []testutil.Entry
	for _, name := range loose {
		entries = append(entries, testutil.Entry{
			RawName: testutil.CP949(t, name), Data: jpeg(t), Method: testutil.MethodStore,
		})
	}
	// Sorted, so a fixture is byte-reproducible and a failure repeats. The
	// scanner sorts the chapters itself; the container's own entry order is not
	// what decides 권 order.
	for _, dir := range slices.Sorted(maps.Keys(chapters)) {
		for i := 1; i <= chapters[dir]; i++ {
			name := dir + "/" + string(rune('0'+i/10%10)) + string(rune('0'+i%10)) + ".jpg"
			entries = append(entries, testutil.Entry{
				RawName: testutil.CP949(t, name), Data: jpeg(t), Method: testutil.MethodDeflate,
			})
		}
	}
	return testutil.BuildZIP(t, testutil.ZIPSpec{Entries: entries})
}

// TestScan_containerOfChapterDirectories_becomesOneBookPerChapter is D-73's
// subject: `여자친구 만들고파! 01~08권.zip` is 842 pages in eight per-volume
// directories, and it indexed as one 842-page book that no reader could
// navigate. Each directory is now its own 권.
func TestScan_containerOfChapterDirectories_becomesOneBookPerChapter(t *testing.T) {
	t.Parallel()

	container := chapterZIP(t, map[string]int{
		"여친만 01": 3,
		"여친만 02": 2,
		"여친만 10": 4,
	})
	h := newHarness(t, map[string]any{"여자친구 만들고파! 01~08권.zip": container})
	h.run(Request{})

	books := h.books("manga", "여자친구 만들고파! 01~08권.zip")
	if len(books) != 3 {
		t.Fatalf("indexed %d books %v, want one per chapter directory", len(books), bookNames(books))
	}

	want := []struct {
		name      string
		innerPath string
		pages     int
	}{
		// Natural order, so `10` comes last rather than between `01` and `02`.
		{"여친만 01", "여친만 01", 3},
		{"여친만 02", "여친만 02", 2},
		{"여친만 10", "여친만 10", 4},
	}
	for i, w := range want {
		b := books[i]
		if b.DisplayName != w.name {
			t.Errorf("book %d display name = %q, want %q", i, b.DisplayName, w.name)
		}
		if b.InnerPath != w.innerPath {
			t.Errorf("book %d inner_path = %q, want %q", i, b.InnerPath, w.innerPath)
		}
		if b.Kind != string(source.KindNestedDir) {
			t.Errorf("book %d kind = %q, want %q", i, b.Kind, source.KindNestedDir)
		}
		if b.Status != StatusOK {
			t.Errorf("book %d status = %q (%s), want ok", i, b.Status, b.Error)
		}
		if int(b.PageCount) != w.pages {
			t.Errorf("book %d page_count = %d, want %d", i, b.PageCount, w.pages)
		}
		if pages := h.pages(b.ID); len(pages) != w.pages {
			t.Errorf("book %d has %d page rows, want %d", i, len(pages), w.pages)
		}
		// All three chapters live in one file and carry its identity.
		if b.RelPath != "여자친구 만들고파! 01~08권.zip" {
			t.Errorf("book %d rel_path = %q, want the container", i, b.RelPath)
		}
	}

	ids := map[string]bool{}
	for _, b := range books {
		if ids[b.ID] {
			t.Fatalf("two chapters share the book id %s", b.ID)
		}
		ids[b.ID] = true
		// The container itself stops being a book: a 권 list showing "three
		// chapters and the 9-page thing holding them" is not what a reader wants.
		if b.InnerPath == "" {
			t.Errorf("the container is still indexed as a book (%q)", b.DisplayName)
		}
	}

	// The series is the sum of its chapters and nothing is counted twice.
	if s := h.seriesAt("manga", "여자친구 만들고파! 01~08권.zip"); s.PageCount != 9 || s.BookCount != 3 {
		t.Errorf("series = %d books / %d pages, want 3 / 9", s.BookCount, s.PageCount)
	}
}

// TestScan_chapterPages_streamBackOutOfTheContainer closes the loop: the page
// rows a chapter produced must serve, at the container's own offsets, with no
// page of a neighbouring chapter among them.
func TestScan_chapterPages_streamBackOutOfTheContainer(t *testing.T) {
	t.Parallel()

	container := chapterZIP(t, map[string]int{"01화": 2, "02화": 3})
	h := newHarness(t, map[string]any{"컨테이너.zip": container})
	h.run(Request{})

	books := h.books("manga", "컨테이너.zip")
	if len(books) != 2 {
		t.Fatalf("indexed %d books %v, want 2 chapters", len(books), bookNames(books))
	}
	b := books[1]
	pages := h.pages(b.ID)
	if len(pages) != 3 {
		t.Fatalf("page rows = %d, want 3", len(pages))
	}
	// Against the book's own inner path rather than a literal: whichever legacy
	// encoding kenc settles on for the container (arch §4.4), the chapter's
	// pages and the chapter's name have to be spelled the same way, or the
	// prefix that selects them selects nothing.
	for _, p := range pages {
		if !strings.HasPrefix(p.EntryPath, b.InnerPath+"/") {
			t.Errorf("page %d is %q, which is not inside chapter %q", p.PageNo, p.EntryPath, b.InnerPath)
		}
	}
	if got := pages[0].Name; got != "01.jpg" {
		t.Errorf("first page name = %q, want 01.jpg — CP949 decoding applies inside a chapter too", got)
	}

	src, err := h.lister.inner.Open(t.Context(), source.Book{
		ID: b.ID, Kind: source.Kind(b.Kind), RootName: b.RootName,
		RelPath: b.RelPath, InnerPath: b.InnerPath,
		FileSize: b.FileSize, FileMtime: b.FileMtime,
	})
	if err != nil {
		t.Fatalf("opening the chapter: %v", err)
	}
	defer func() { _ = src.Close() }()

	for _, p := range pages {
		st, err := src.Open(t.Context(), source.Page{
			No: p.PageNo, Ext: p.Ext, Size: p.Size, CompSize: p.CompSize,
			Method: uint16(p.Method), LocalHdrOff: p.LocalHdrOff, CRC32: p.CRC32,
		}, source.OpenOptions{})
		if err != nil {
			t.Fatalf("streaming page %d: %v", p.PageNo, err)
		}
		got, err := io.ReadAll(st)
		_ = st.Close()
		if err != nil {
			t.Fatalf("reading page %d: %v", p.PageNo, err)
		}
		if string(got) != string(jpeg(t)) {
			t.Errorf("page %d bytes differ from the source image", p.PageNo)
		}
	}
}

// TestScan_chapterContainerWithALooseCover_keepsIt is the `야와라!` shape: 29
// volume directories and one cover image at the top of the archive. The cover
// is a 권 of its own rather than a dropped page, it sorts first, and being first
// is what makes the cover ladder pick it up (arch §4.10 rule 3).
func TestScan_chapterContainerWithALooseCover_keepsIt(t *testing.T) {
	t.Parallel()

	container := chapterZIP(t, map[string]int{"01": 2, "02": 2}, "cover.jpg")
	h := newHarness(t, map[string]any{"야와라.zip": container})
	h.run(Request{})

	books := h.books("manga", "야와라.zip")
	if len(books) != 3 {
		t.Fatalf("indexed %d books %v, want two chapters and the loose cover", len(books), bookNames(books))
	}
	first := books[0]
	if first.InnerPath != source.ChapterRoot {
		t.Errorf("first book inner_path = %q, want %q", first.InnerPath, source.ChapterRoot)
	}
	if !strings.HasSuffix(first.DisplayName, looseBookSuffix) {
		t.Errorf("first book display name = %q, want the %q suffix", first.DisplayName, looseBookSuffix)
	}
	if first.PageCount != 1 {
		t.Errorf("first book page_count = %d, want the one loose image", first.PageCount)
	}
	// No page is lost and none is counted twice: 1 + 2 + 2.
	if s := h.seriesAt("manga", "야와라.zip"); s.PageCount != 5 {
		t.Errorf("series page_count = %d, want 5", s.PageCount)
	}
	if s := h.seriesAt("manga", "야와라.zip"); s.CoverKind != CoverPage || s.CoverBookID != first.ID {
		t.Errorf("cover = %q/%s, want page 1 of the loose-pages book %s",
			s.CoverKind, s.CoverBookID, first.ID)
	}
}

// TestScan_singleWrapperDirectory_staysOneBook is the guard that matters most:
// an archive packed with its own folder — `01권.zip` holding `01권/001.jpg` — is
// one volume, and 94.9% of the collection must come through this change
// completely unchanged.
func TestScan_singleWrapperDirectory_staysOneBook(t *testing.T) {
	t.Parallel()

	h := newHarness(t, map[string]any{
		"01권.zip": chapterZIP(t, map[string]int{"01권": 3}),
		"평평.zip":   jpegZIP(t, "001.jpg", "002.jpg"),
	})
	h.run(Request{})

	for _, rel := range []string{"01권.zip", "평평.zip"} {
		books := h.books("manga", rel)
		if len(books) != 1 {
			t.Fatalf("%s indexed %d books %v, want 1", rel, len(books), bookNames(books))
		}
		if books[0].Kind != string(source.KindZIP) || books[0].InnerPath != "" {
			t.Errorf("%s: kind = %q inner_path = %q, want a plain zip book",
				rel, books[0].Kind, books[0].InnerPath)
		}
	}
}

// A container of chapter directories nested inside a *series folder* is the
// same book, and its chapters are named for their directories rather than for
// the path that got them there.
func TestScan_chapterContainerInsideASeriesFolder(t *testing.T) {
	t.Parallel()

	h := newHarness(t, map[string]any{
		"어떤 시리즈": map[string]any{
			"01~08권.zip": chapterZIP(t, map[string]int{"01": 2, "02": 2}),
			"09권.zip":    jpegZIP(t, "001.jpg"),
		},
	})
	h.run(Request{})

	books := h.books("manga", "어떤 시리즈")
	if len(books) != 3 {
		t.Fatalf("indexed %d books %v, want two chapters and the plain volume", len(books), bookNames(books))
	}
	got := bookNames(books)
	want := []string{"01", "02", "09권.zip"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("books = %v, want %v", got, want)
		}
	}
	// The chapters sort with the volume they came from, not at the end: the
	// container's own series-relative path is the prefix of theirs.
	for _, b := range books[:2] {
		if b.RelPath != "어떤 시리즈/01~08권.zip" {
			t.Errorf("chapter %q rel_path = %q, want the container", b.DisplayName, b.RelPath)
		}
	}
}

// A RAR full of chapter directories splits the same way, and it is worth a test
// rather than a claim: `nesteddir` does not name a format, so the reader has to
// come from the *container's* extension. Get that wrong and a chapter of a `.rar`
// is handed to the ZIP reader and reported as corrupt — a wrong story about a
// perfectly good file, which is the defect D-71 called out by name.
func TestScan_rarContainerOfChapterDirectories_splitsToo(t *testing.T) {
	t.Parallel()

	container := testutil.BuildRAR4(t, testutil.RAR4Spec{Entries: []testutil.RAR4Entry{
		{Name: []byte("01/001.jpg"), Data: jpeg(t)},
		{Name: []byte("01/002.jpg"), Data: jpeg(t)},
		{Name: []byte("02/001.jpg"), Data: jpeg(t)},
	}})
	h := newHarness(t, map[string]any{"시리즈.rar": container})
	h.run(Request{})

	books := h.books("manga", "시리즈.rar")
	if len(books) != 2 {
		t.Fatalf("indexed %d books %v, want one per chapter directory", len(books), bookNames(books))
	}
	for i, want := range []struct {
		name  string
		pages int64
	}{{"01", 2}, {"02", 1}} {
		b := books[i]
		if b.DisplayName != want.name || b.PageCount != want.pages {
			t.Errorf("book %d = %q / %d pages, want %q / %d",
				i, b.DisplayName, b.PageCount, want.name, want.pages)
		}
		if b.Kind != string(source.KindNestedDir) {
			t.Errorf("book %d kind = %q, want %q", i, b.Kind, source.KindNestedDir)
		}
		if b.Status != StatusOK {
			t.Errorf("book %d status = %q (%s), want ok", i, b.Status, b.Error)
		}
	}
}

// The chapter split must not fire on a container of volumes: those are D-70's,
// they have no pages of their own, and expanding one as chapters would produce
// books whose "directory" is an archive.
func TestScan_containerOfVolumes_isNotSplitAsChapters(t *testing.T) {
	t.Parallel()

	container := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
		{RawName: testutil.CP949(t, "겟백커스 01.zip"), Data: volume(t, "겟백커스 01권", 2), Method: testutil.MethodStore},
		{RawName: testutil.CP949(t, "겟백커스 02.zip"), Data: volume(t, "겟백커스 02권", 2), Method: testutil.MethodStore},
	}})
	h := newHarness(t, map[string]any{"겟 벡커스.zip": container})
	h.run(Request{})

	for _, b := range h.books("manga", "겟 벡커스.zip") {
		if b.Kind != string(source.KindNestedZIP) {
			t.Errorf("%q kind = %q, want %q", b.DisplayName, b.Kind, source.KindNestedZIP)
		}
	}
}

// A chapter is not opened looking for chapters of its own. One inner path is
// what books.inner_path holds (arch §3.5), so a second level could not be
// addressed even if the scanner found one.
func TestScan_chaptersInsideAVolume_areNotSplitAgain(t *testing.T) {
	t.Parallel()

	inner := chapterZIP(t, map[string]int{"a": 2, "b": 2})
	container := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
		{RawName: testutil.CP949(t, "01권.zip"), Data: inner, Method: testutil.MethodStore},
		{RawName: testutil.CP949(t, "02권.zip"), Data: inner, Method: testutil.MethodStore},
	}})
	h := newHarness(t, map[string]any{"시리즈.zip": container})
	h.run(Request{})

	books := h.books("manga", "시리즈.zip")
	if len(books) != 2 {
		t.Fatalf("indexed %d books %v, want the two volumes", len(books), bookNames(books))
	}
	for _, b := range books {
		if b.Kind != string(source.KindNestedZIP) {
			t.Errorf("%q kind = %q, want %q", b.DisplayName, b.Kind, source.KindNestedZIP)
		}
		if b.PageCount != 4 {
			t.Errorf("%q page_count = %d, want all 4 pages of the volume", b.DisplayName, b.PageCount)
		}
	}
}

// FR-IDX-003 has to reach the chapters, or a library of 6,097 of them rewrites
// every page row on every scan. An unchanged container's chapters are each
// recognised by the container's own (size, mtime) and skipped.
func TestScan_unchangedChapters_areSkippedOnRescan(t *testing.T) {
	t.Parallel()

	h := newHarness(t, map[string]any{
		"시리즈.zip": chapterZIP(t, map[string]int{"01": 2, "02": 2, "03": 2}),
	})
	h.run(Request{})
	res := h.run(Request{})

	_, indexed, _, skipped, _ := res.Totals()
	if indexed != 3 || skipped != 3 {
		t.Errorf("rescan indexed %d books of which %d skipped, want 3 / 3", indexed, skipped)
	}
	books := h.books("manga", "시리즈.zip")
	if len(books) != 3 {
		t.Fatalf("after the rescan there are %d books %v, want 3", len(books), bookNames(books))
	}
	for i, b := range books {
		if b.Ord != i {
			t.Errorf("book %q ord = %d, want %d", b.DisplayName, b.Ord, i)
		}
	}
}

// TestScan_encryptedChapterContainer_staysOneEncryptedBook records what the
// split cannot rescue, so nobody looks for the bug later.
//
// A chapter is a directory, not a file, so every way a container can fail to be
// read is a *container*-level failure: FR-IDX-010 already rules that one
// encrypted entry makes the whole book encrypted, and a truncated central
// directory is truncated for every directory in it. The split runs on a page
// list, and a container that cannot produce one is never split at all — it keeps
// the single honest badge it has today rather than growing eight copies of it.
func TestScan_encryptedChapterContainer_staysOneEncryptedBook(t *testing.T) {
	t.Parallel()

	container := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
		{Name: "01/001.jpg", Data: jpeg(t), Flags: testutil.FlagUTF8},
		{Name: "01/002.jpg", Data: jpeg(t), Flags: testutil.FlagUTF8},
		{Name: "02/001.jpg", Data: jpeg(t), Flags: testutil.FlagUTF8 | testutil.FlagEncrypted},
	}})
	h := newHarness(t, map[string]any{"시리즈.zip": container})
	h.run(Request{})

	books := h.books("manga", "시리즈.zip")
	if len(books) != 1 {
		t.Fatalf("indexed %d books %v, want the one unsplit book", len(books), bookNames(books))
	}
	if books[0].Status != StatusEncrypted {
		t.Errorf("status = %q (%s), want %q", books[0].Status, books[0].Error, StatusEncrypted)
	}
	if books[0].InnerPath != "" {
		t.Errorf("inner_path = %q, want empty — nothing in there could be listed", books[0].InnerPath)
	}
}

// UI-002's 총 용량 is the bytes on disk, and a container is one file however many
// 권 it turns out to hold.
//
// Every volume inside a container records that container's size (arch §3.5), so
// a plain sum multiplies it: measured on the real collection before this was
// fixed, `엔젤하트 전32권 완결.zip` — 1.55 GB, 33 nested volumes — reported 51 GB,
// and `암살교실 1~180화.zip` — 588 MB in 182 chapter directories — reported 107 GB.
func TestScan_chapterContainer_seriesBytesCountTheFileOnce(t *testing.T) {
	t.Parallel()

	container := chapterZIP(t, map[string]int{"01": 2, "02": 2, "03": 2})
	h := newHarness(t, map[string]any{"시리즈.zip": container})
	h.run(Request{})

	s := h.seriesAt("manga", "시리즈.zip")
	if s.TotalBytes != int64(len(container)) {
		t.Errorf("series total_bytes = %d, want the container's own %d (it is one file)",
			s.TotalBytes, len(container))
	}
	// The per-volume column is unchanged and deliberately so: a 권 inside a
	// container has no file of its own, so it reports the container's size.
	for _, b := range h.books("manga", "시리즈.zip") {
		if b.FileSize != int64(len(container)) {
			t.Errorf("volume %q file_size = %d, want the container's %d",
				b.DisplayName, b.FileSize, len(container))
		}
	}
}

// FR-IDX-004's progress has to survive the split. The walker counts one book
// per container; a container that becomes fifteen has to say so, and must not
// then be counted as a sixteenth.
//
// Before this was arithmetic anybody checked, the counters ran the other way: a
// verification scan of five real containers reported `done 252 / total 5`.
func TestScan_chapterContainer_progressTotalsAgree(t *testing.T) {
	t.Parallel()

	h := newHarness(t, map[string]any{
		"시리즈.zip": chapterZIP(t, map[string]int{"01": 2, "02": 2, "03": 2}),
		"평평.zip":   jpegZIP(t, "001.jpg"),
	})
	h.run(Request{})

	st := h.scanner.Status()
	// Three chapters plus the flat archive; the container itself is not a book.
	if st.Done != 4 || st.Total != 4 {
		t.Errorf("progress = %d done / %d total, want 4 / 4", st.Done, st.Total)
	}
}

// The scan log has to say what happened, or a 권 list that grew from 1 to 182
// has no explanation anywhere.
func TestScan_chapterContainer_logsTheSplit(t *testing.T) {
	t.Parallel()

	h := newHarness(t, map[string]any{"시리즈.zip": chapterZIP(t, map[string]int{"01": 1, "02": 1})})
	h.run(Request{})

	var found bool
	for _, l := range h.logs() {
		if l.Level == index.LevelInfo && strings.Contains(l.Message, "chapter directories") {
			found = true
		}
	}
	if !found {
		t.Errorf("no scan-log row explains the split; log = %v", h.logs())
	}
}
