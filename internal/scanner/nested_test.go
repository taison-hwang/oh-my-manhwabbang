package scanner

import (
	"io"
	"strings"
	"testing"

	"shelf/internal/index"
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

// TestScan_containerOfUnreadableVolumes_staysOneBook: expansion must not invent
// books out of formats this build cannot open. D-71 gave RAR a reader, so the
// standing example moved to `.7z` — the rule is unchanged, only its subject.
//
// The status moved too, and that is D-72 rather than a side effect. This
// container is not empty: it holds two 7-Zip archives. `비어 있음` is the
// sentence for `비둘기.zip`, which holds nothing (ruling E-14), and saying it
// here would send an owner looking for damage in two files that are fine.
func TestScan_containerOfUnreadableVolumes_staysOneBook(t *testing.T) {
	t.Parallel()

	sevens := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
		{RawName: testutil.CP949(t, "사모님은 학생회장 01.7z"), Data: []byte("7z\xbc\xaf\x27\x1cnot really"), Method: testutil.MethodStore},
		{RawName: testutil.CP949(t, "사모님은 학생회장 02.7z"), Data: []byte("7z\xbc\xaf\x27\x1cnot really"), Method: testutil.MethodStore},
	}})
	h := newHarness(t, map[string]any{"랄만.zip": sevens})
	h.run(Request{})

	books := h.books("manga", "랄만.zip")
	if len(books) != 1 {
		t.Fatalf("indexed %d books, want the single unexpanded book", len(books))
	}
	if books[0].Status != StatusUnsupported {
		t.Errorf("status = %q, want %q", books[0].Status, StatusUnsupported)
	}
	if !strings.Contains(books[0].Error, "7-Zip") {
		t.Errorf("error = %q, want it to name the format that could not be opened", books[0].Error)
	}
	if books[0].InnerPath != "" {
		t.Errorf("inner_path = %q, want empty — nothing in there is readable", books[0].InnerPath)
	}
}

// TestScan_containerOfOneHV3_becomesAVolume is `펌프킨 시저스 04.zip`, and it
// is the same file three rulings have now been written about.
//
// Under D-07 it was `비어 있음 · no supported image entries` — false, since it
// holds 39.5 MB. Under D-72 it was `unsupported`, naming HV3 — true as far as
// it went, and the best answer available while the format was believed to be
// encrypted. Under E-51 it is what it always was: 104 pages. The container is
// one nested volume, indexed by the same path `사모님은 학생회장.zip`'s fifteen
// take.
//
// Deflating the inner container is deliberate and is the case that costs
// something: internal/archive/nested has to inflate forward to reach each
// page's offset, and HV3 keeps its directory at the *front*, which is the one
// place the adapter's tail-window optimisation cannot help.
func TestScan_containerOfOneHV3_becomesAVolume(t *testing.T) {
	t.Parallel()

	pages := testutil.HV3Pages(3)
	inner := testutil.BuildHV3(t, testutil.HV3Spec{Entries: pages, Encr: testutil.HV3EncrMask})
	book := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
		{RawName: testutil.CP949(t, "펌프킨 시저스 04.hv3"), Data: inner, Method: testutil.MethodDeflate},
	}})
	h := newHarness(t, map[string]any{"펌프킨 시저스 04.zip": book})
	h.run(Request{})

	books := h.books("manga", "펌프킨 시저스 04.zip")
	if len(books) != 1 {
		t.Fatalf("indexed %d books %v, want 1", len(books), bookNames(books))
	}
	b := books[0]
	if b.Status != StatusOK {
		t.Fatalf("status = %q, want %q (error %q)", b.Status, StatusOK, b.Error)
	}
	if b.Kind != string(source.KindNestedHV3) {
		t.Errorf("kind = %q, want %q", b.Kind, source.KindNestedHV3)
	}
	if b.InnerPath != "펌프킨 시저스 04.hv3" {
		t.Errorf("inner_path = %q, want the entry that is the volume", b.InnerPath)
	}
	if b.PageCount != int64(len(pages)) {
		t.Errorf("page_count = %d, want %d", b.PageCount, len(pages))
	}
}

// TestScan_looseHV3_isABook is the same format as its own file. The collection
// holds one — `펌프킨 시저스 04` was unpacked out of its ZIP — and 26 of them
// when arch §4.2 was first surveyed, which is where adjustment D-7's
// ".txt/.hv3 directories" came from.
func TestScan_looseHV3_isABook(t *testing.T) {
	t.Parallel()

	pages := testutil.HV3Pages(4)
	h := newHarness(t, map[string]any{
		"펌프킨 시저스 1~13권": map[string]any{
			"펌프킨 시저스 04.hv3": testutil.BuildHV3(t, testutil.HV3Spec{
				Entries: pages, Encr: testutil.HV3EncrMask,
			}),
		},
	})
	h.run(Request{})

	books := h.books("manga", "펌프킨 시저스 1~13권")
	if len(books) != 1 {
		t.Fatalf("indexed %d books %v, want 1", len(books), bookNames(books))
	}
	b := books[0]
	if b.Status != StatusOK {
		t.Fatalf("status = %q, want %q (error %q)", b.Status, StatusOK, b.Error)
	}
	if b.Kind != string(source.KindHV3) {
		t.Errorf("kind = %q, want %q", b.Kind, source.KindHV3)
	}
	if b.PageCount != int64(len(pages)) {
		t.Errorf("page_count = %d, want %d", b.PageCount, len(pages))
	}
}

// TestScan_hv3ThatIsNotAnHV3_saysWhatItIs is the shape 54 of the 55 `.hv3`
// files on this machine actually have: a RAR archive wearing the extension.
//
// They are all in the trash and none is in the library, so nothing dispatches
// on the signature — [source.ContainerKind] still reads the name, as it does
// for every other format. What must not happen is the *wrong story*: `HV3
// signature not found` on a good RAR sends its owner looking for damage, which
// is the failure D-72 exists to prevent, pointed the other way.
func TestScan_hv3ThatIsNotAnHV3_saysWhatItIs(t *testing.T) {
	t.Parallel()

	rar := testutil.BuildRAR4(t, testutil.RAR4Spec{Entries: []testutil.RAR4Entry{
		{Name: []byte("001.jpg"), Data: jpeg(t)},
	}})
	h := newHarness(t, map[string]any{"궁 09.hv3": rar})
	h.run(Request{})

	books := h.books("manga", "궁 09.hv3")
	if len(books) != 1 {
		t.Fatalf("indexed %d books, want 1", len(books))
	}
	b := books[0]
	if b.Status != StatusError {
		t.Errorf("status = %q, want %q", b.Status, StatusError)
	}
	if !strings.Contains(b.Error, "RAR") {
		t.Errorf("error = %q, want it to name what the file actually is", b.Error)
	}
}

// TestScan_containerOfVolumesAndOneForeignFormat_stillExpands is the ordering
// D-72 has to respect, and did not when it was first written.
//
// `사모님은 학생회장.zip` holds ZIP volumes, RAR volumes and one `.7z`. Naming
// the `.7z` makes the container `unsupported`, and the scanner only looks for
// volumes inside a book it was told is `empty` — so the whole series collapsed
// to one closed box, and the e2e round caught it two tiers away from the change.
//
// The rule is therefore "described by what it could not read" only when there
// was nothing else to do: no pages *and* no volume. A foreign format beside
// readable volumes is a footnote, exactly as it is beside readable pages.
func TestScan_containerOfVolumesAndOneForeignFormat_stillExpands(t *testing.T) {
	t.Parallel()

	rarVol := testutil.BuildRAR4(t, testutil.RAR4Spec{Entries: []testutil.RAR4Entry{
		{Name: []byte("001.jpg"), Data: jpeg(t)},
	}})
	outer := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
		{RawName: testutil.CP949(t, "사모님은 학생회장 01.zip"), Data: volume(t, "01권", 2), Method: testutil.MethodStore},
		{RawName: testutil.CP949(t, "사모님은 학생회장 02.rar"), Data: rarVol, Method: testutil.MethodStore},
		{RawName: testutil.CP949(t, "사모님은 학생회장 특전.7z"), Data: []byte("7z\xbc\xaf\x27\x1cnope"), Method: testutil.MethodStore},
	}})
	h := newHarness(t, map[string]any{"사모님은 학생회장.zip": outer})
	h.run(Request{})

	books := h.books("manga", "사모님은 학생회장.zip")
	if len(books) != 2 {
		t.Fatalf("indexed %d books %v, want the two readable volumes", len(books), bookNames(books))
	}
	kinds := map[string]bool{}
	for _, b := range books {
		kinds[b.Kind] = true
		if b.Status != StatusOK {
			t.Errorf("%s: status = %q (%s), want ok", b.Kind, b.Status, b.Error)
		}
	}
	for _, want := range []source.Kind{source.KindNestedZIP, source.KindNestedRAR} {
		if !kinds[string(want)] {
			t.Errorf("no %s volume among %v", want, bookNames(books))
		}
	}
	for _, b := range books {
		if strings.HasSuffix(b.DisplayName, ".7z") {
			t.Errorf("the .7z became a book (%q)", b.DisplayName)
		}
	}
}

// The counterpart, so the rule cannot creep: a foreign container sitting beside
// real pages is a stray file, not the book's identity.
func TestScan_foreignContainerBesidePages_isJustAStrayEntry(t *testing.T) {
	t.Parallel()

	book := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
		{Name: "001.jpg", Data: jpeg(t), Flags: testutil.FlagUTF8},
		{Name: "002.jpg", Data: jpeg(t), Flags: testutil.FlagUTF8},
		{Name: "특전.7z", Data: []byte("7z\xbc\xaf\x27\x1cnope"), Flags: testutil.FlagUTF8},
	}})
	h := newHarness(t, map[string]any{"보통책.zip": book})
	h.run(Request{})

	books := h.books("manga", "보통책.zip")
	if len(books) != 1 {
		t.Fatalf("indexed %d books, want 1", len(books))
	}
	if books[0].Status != StatusOK {
		t.Errorf("status = %q (%s), want ok", books[0].Status, books[0].Error)
	}
	if books[0].PageCount != 2 {
		t.Errorf("page_count = %d, want 2", books[0].PageCount)
	}
}

// TestScan_containerOfMixedFormats_expandsBoth is the `사모님은 학생회장.zip`
// case, which is what D-71 is for: one container holding 7 ZIPs and 8 RARs.
// Under D-07 it yielded 7 volumes and silently dropped the other 8.
//
// The RAR volumes here are real RAR 4 archives, built by the same helper the
// rar4 tests use, so this exercises the whole path — the outer ZIP read by
// zipidx, each inner volume read by the reader its own extension names.
func TestScan_containerOfMixedFormats_expandsBoth(t *testing.T) {
	t.Parallel()

	zipVol := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
		{RawName: []byte("001.jpg"), Data: testutil.TinyJPEG(t, 8, 8), Method: testutil.MethodStore},
		{RawName: []byte("002.jpg"), Data: testutil.TinyJPEG(t, 8, 8), Method: testutil.MethodStore},
	}})
	rarVol := testutil.BuildRAR4(t, testutil.RAR4Spec{Entries: []testutil.RAR4Entry{
		{Name: []byte("001.jpg"), Data: testutil.TinyJPEG(t, 8, 8)},
		{Name: []byte("002.jpg"), Data: testutil.TinyJPEG(t, 8, 8)},
		{Name: []byte("003.jpg"), Data: testutil.TinyJPEG(t, 8, 8)},
	}})
	outer := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
		{RawName: testutil.CP949(t, "사모님은 학생회장 01.zip"), Data: zipVol, Method: testutil.MethodStore},
		{RawName: testutil.CP949(t, "사모님은 학생회장 02.rar"), Data: rarVol, Method: testutil.MethodStore},
	}})
	h := newHarness(t, map[string]any{"사모님은 학생회장.zip": outer})
	h.run(Request{})

	books := h.books("manga", "사모님은 학생회장.zip")
	if len(books) != 2 {
		t.Fatalf("indexed %d books %v, want both volumes", len(books), bookNames(books))
	}

	byKind := map[string]index.BookRow{}
	for _, b := range books {
		byKind[b.Kind] = b
	}
	zb, ok := byKind[string(source.KindNestedZIP)]
	if !ok {
		t.Fatalf("no %s book among %v", source.KindNestedZIP, bookNames(books))
	}
	rb, ok := byKind[string(source.KindNestedRAR)]
	if !ok {
		t.Fatalf("no %s book among %v — the RAR volume was dropped", source.KindNestedRAR, bookNames(books))
	}
	for _, b := range []index.BookRow{zb, rb} {
		if b.Status != StatusOK {
			t.Errorf("%s: status = %q (%s), want %q", b.Kind, b.Status, b.Error, StatusOK)
		}
		if b.InnerPath == "" {
			t.Errorf("%s: inner_path is empty", b.Kind)
		}
	}
	if zb.PageCount != 2 {
		t.Errorf("zip volume page_count = %d, want 2", zb.PageCount)
	}
	if rb.PageCount != 3 {
		t.Errorf("rar volume page_count = %d, want 3", rb.PageCount)
	}
}
