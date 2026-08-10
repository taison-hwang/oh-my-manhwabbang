package scanner

import (
	"io"
	"testing"

	"shelf/internal/source"
	"shelf/internal/testutil"
)

// volume builds the bytes of one inner volume: a ZIP with CP949 page names,
// exactly the shape the collection's containers hold.
func volume(t *testing.T, dir string, pages int) []byte {
	t.Helper()
	entries := make([]testutil.Entry, 0, pages)
	for i := range pages {
		name := dir + "/" + string(rune('0'+(i+1)/10%10)) + string(rune('0'+(i+1)%10)) + ".jpg"
		entries = append(entries, testutil.Entry{
			RawName: testutil.CP949(t, name),
			Data:    jpeg(t),
			Method:  testutil.MethodDeflate,
		})
	}
	return testutil.BuildZIP(t, testutil.ZIPSpec{Entries: entries})
}

// TestScan_containerOfVolumes_becomesOneBookPerVolume is the whole point of the
// nested-archive work: `겟 벡커스 1~39완.zip` used to index as a single book
// with status `empty` and no pages, and 45 books in the collection were like
// that. Each inner ZIP is now its own 권.
func TestScan_containerOfVolumes_becomesOneBookPerVolume(t *testing.T) {
	t.Parallel()

	container := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
		{RawName: testutil.CP949(t, "겟백커스 01.zip"), Data: volume(t, "겟백커스 01권", 3), Method: testutil.MethodDeflate},
		{RawName: testutil.CP949(t, "겟백커스 02.zip"), Data: volume(t, "겟백커스 02권", 2), Method: testutil.MethodStore},
	}})

	h := newHarness(t, map[string]any{
		"겟 벡커스 1~39완.zip": container,
	})
	h.run(Request{})

	books := h.books("manga", "겟 벡커스 1~39완.zip")
	if len(books) != 2 {
		t.Fatalf("indexed %d books %v, want 2 volumes", len(books), bookNames(books))
	}

	want := []struct {
		name      string
		innerPath string
		pages     int
	}{
		{"겟백커스 01.zip", "겟백커스 01.zip", 3},
		{"겟백커스 02.zip", "겟백커스 02.zip", 2},
	}
	for i, w := range want {
		b := books[i]
		if b.DisplayName != w.name {
			t.Errorf("book %d display name = %q, want %q", i, b.DisplayName, w.name)
		}
		if b.InnerPath != w.innerPath {
			t.Errorf("book %d inner_path = %q, want %q", i, b.InnerPath, w.innerPath)
		}
		if b.Kind != string(source.KindNestedZIP) {
			t.Errorf("book %d kind = %q, want %q", i, b.Kind, source.KindNestedZIP)
		}
		if b.Status != StatusOK {
			t.Errorf("book %d status = %q (%s), want ok", i, b.Status, b.Error)
		}
		if int(b.PageCount) != w.pages {
			t.Errorf("book %d page_count = %d, want %d", i, b.PageCount, w.pages)
		}
		// Both volumes live in one file, so both carry the container's identity.
		if b.RelPath != "겟 벡커스 1~39완.zip" {
			t.Errorf("book %d rel_path = %q, want the container", i, b.RelPath)
		}
		if pages := h.pages(b.ID); len(pages) != w.pages {
			t.Errorf("book %d has %d page rows, want %d", i, len(pages), w.pages)
		}
	}

	// The two volumes must be distinguishable: same rel_path, different ids.
	if books[0].ID == books[1].ID {
		t.Fatal("both volumes got the same book id")
	}

	// And the container itself is not recorded beside them as a broken book.
	for _, b := range books {
		if b.InnerPath == "" {
			t.Errorf("the container is still indexed as a book (%q)", b.DisplayName)
		}
	}
}

// TestScan_containerPages_streamBackOutOfTheVolume closes the loop: the page
// rows a nested volume produced must actually serve.
func TestScan_containerPages_streamBackOutOfTheVolume(t *testing.T) {
	t.Parallel()

	container := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
		{RawName: testutil.CP949(t, "겟백커스 01.zip"), Data: volume(t, "겟백커스 01권", 4), Method: testutil.MethodDeflate},
	}})
	h := newHarness(t, map[string]any{"컨테이너.zip": container})
	h.run(Request{})

	books := h.books("manga", "컨테이너.zip")
	if len(books) != 1 {
		t.Fatalf("indexed %d books, want 1 volume", len(books))
	}
	pages := h.pages(books[0].ID)
	if len(pages) != 4 {
		t.Fatalf("page rows = %d, want 4", len(pages))
	}
	if got := pages[0].Name; got != "01.jpg" {
		t.Errorf("first page name = %q, want 01.jpg — CP949 decoding must apply inside the volume too", got)
	}

	src, err := h.lister.inner.Open(t.Context(), source.Book{
		ID: books[0].ID, Kind: source.Kind(books[0].Kind), RootName: books[0].RootName,
		RelPath: books[0].RelPath, InnerPath: books[0].InnerPath,
		FileSize: books[0].FileSize, FileMtime: books[0].FileMtime,
	})
	if err != nil {
		t.Fatalf("opening the volume: %v", err)
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
		got := make([]byte, p.Size)
		n, _ := io.ReadFull(st, got)
		_ = st.Close()
		if int64(n) != p.Size {
			t.Errorf("page %d streamed %d bytes, want %d", p.PageNo, n, p.Size)
		}
		if want := jpeg(t); string(got[:n]) != string(want) {
			t.Errorf("page %d bytes differ from the source image", p.PageNo)
		}
	}
}

// TestScan_archiveWithPages_isNeverExpanded guards the ordinary path: a book
// that has pages of its own must not be reopened looking for volumes, and an
// archive holding both images and a stray ZIP stays one book.
func TestScan_archiveWithPages_isNeverExpanded(t *testing.T) {
	t.Parallel()

	mixed := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
		{Name: "001.jpg", Data: jpeg(t), Flags: testutil.FlagUTF8},
		{Name: "002.jpg", Data: jpeg(t), Flags: testutil.FlagUTF8},
		{Name: "bonus.zip", Data: volume(t, "bonus", 2), Flags: testutil.FlagUTF8},
	}})
	h := newHarness(t, map[string]any{"보통책.zip": mixed})
	h.run(Request{})

	books := h.books("manga", "보통책.zip")
	if len(books) != 1 {
		t.Fatalf("indexed %d books %v, want 1", len(books), bookNames(books))
	}
	if books[0].Kind != string(source.KindZIP) || books[0].InnerPath != "" {
		t.Errorf("kind = %q inner_path = %q, want a plain zip book", books[0].Kind, books[0].InnerPath)
	}
	if books[0].PageCount != 2 {
		t.Errorf("page_count = %d, want 2 (the .zip entry is not a page)", books[0].PageCount)
	}
}

// TestScan_containerOfUnreadableVolumes_staysEmpty: expansion must not invent
// books out of formats this build cannot open. prd §7.2 keeps RAR out, so a
// container holding only RARs has nothing to offer and stays what it was.
func TestScan_containerOfUnreadableVolumes_staysEmpty(t *testing.T) {
	t.Parallel()

	rars := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
		{RawName: testutil.CP949(t, "사모님은 학생회장 01.rar"), Data: []byte("Rar!\x1a\x07\x00not really"), Method: testutil.MethodStore},
		{RawName: testutil.CP949(t, "사모님은 학생회장 02.rar"), Data: []byte("Rar!\x1a\x07\x00not really"), Method: testutil.MethodStore},
	}})
	h := newHarness(t, map[string]any{"랄만.zip": rars})
	h.run(Request{})

	books := h.books("manga", "랄만.zip")
	if len(books) != 1 {
		t.Fatalf("indexed %d books, want the single empty book", len(books))
	}
	if books[0].Status != StatusEmpty {
		t.Errorf("status = %q, want %q", books[0].Status, StatusEmpty)
	}
	if books[0].InnerPath != "" {
		t.Errorf("inner_path = %q, want empty — nothing in there is readable", books[0].InnerPath)
	}
}
