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

// An HV3 book must be indistinguishable from a ZIP book everywhere above this
// package. These tests assert that by asserting the same things the ZIP and RAR
// tests assert, on the same shapes: the FR-IDX-006 exclusions, the FR-IDX-007
// natural order, Range support on a page, the arch §4.11 status mapping.
//
// That sameness is the design claim D-07 kept the [archive.Reader] interface
// for, and E-51 is its third proof. An HV3 book differs from a ZIP book by
// which reader the Factory handed its containerSource, and nothing else — no
// exclusion rule, no ordering rule and no naming rule is written twice.
//
// The one thing genuinely new here is that the bytes on disk are not the bytes
// served: ENCR mode 2 masks every payload with its own byte position. So each
// test that reads a page also asserts the container does *not* contain the
// plain bytes, which is what keeps it a test of the reader rather than of a
// fixture that forgot to mask.

func hv3Book(t *testing.T, encr uint32, entries ...testutil.HV3Entry) []byte {
	t.Helper()
	return testutil.BuildHV3(t, testutil.HV3Spec{Entries: entries, Encr: encr})
}

func TestHV3Source_listAndStream(t *testing.T) {
	t.Parallel()

	page1 := testutil.TinyJPEG(t, 8, 8)
	page2 := testutil.TinyJPEG(t, 16, 16)
	raw := hv3Book(t, testutil.HV3EncrMask,
		testutil.HV3Entry{Name: `한글권\002.jpg`, Data: page2},
		testutil.HV3Entry{Name: `한글권\001.jpg`, Data: page1},
		testutil.HV3Entry{Name: `한글권/Thumbs.db`, Data: []byte("junk")},
		testutil.HV3Entry{Name: `한글권/readme.txt`, Data: []byte("메모")},
	)
	f := newFixture(t, map[string]any{"책.hv3": raw})
	src := f.open(t, source.KindHV3, "책.hv3")

	if got := src.Kind(); got != source.KindHV3 {
		t.Errorf("Kind() = %q, want %q", got, source.KindHV3)
	}

	l, err := src.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	// FR-IDX-007: natural order, not the container's order. The container
	// stores 002 before 001 on purpose. The `\` separators are normalised on
	// the way in, or these paths would not match the `/` ones below.
	want := []string{"한글권/001.jpg", "한글권/002.jpg"}
	if got := pageNames(l.Pages); !slices.Equal(got, want) {
		t.Errorf("pages = %v, want %v", got, want)
	}
	// FR-IDX-006: Thumbs.db and the .txt are dropped by the same predicate
	// that drops them out of a ZIP.
	if l.Excluded != 2 {
		t.Errorf("Excluded = %d, want 2", l.Excluded)
	}
	// HV3 names are UTF-16 by construction, so no book is ever a CP949 book.
	if l.NameEncoding != "utf-8" {
		t.Errorf("NameEncoding = %q, want utf-8", l.NameEncoding)
	}
	if l.TotalBytes != int64(len(page1)+len(page2)) {
		t.Errorf("TotalBytes = %d, want %d", l.TotalBytes, len(page1)+len(page2))
	}

	for i, want := range [][]byte{page1, page2} {
		if bytes.Contains(raw, want) {
			t.Fatalf("page %d is stored unmasked — the fixture is not exercising ENCR 2", i+1)
		}
		if got := readPage(t, src, l.Pages[i]); !bytes.Equal(got, want) {
			t.Errorf("page %d: %d bytes, want %d", i+1, len(got), len(want))
		}
	}
}

// arch §5.3: a page must come back seekable so http.ServeContent can answer a
// Range request. For this format that is a real claim rather than a free one —
// the mask has to be applied from the position seeked to, not from zero.
func TestHV3Source_maskedPageIsSeekable(t *testing.T) {
	t.Parallel()

	page := testutil.TinyJPEG(t, 8, 8)
	f := newFixture(t, map[string]any{
		"책.hv3": hv3Book(t, testutil.HV3EncrMask, testutil.HV3Entry{Name: "001.jpg", Data: page}),
	})
	src := f.open(t, source.KindHV3, "책.hv3")

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
		t.Fatalf("masked HV3 page is %T, which is not an io.ReadSeeker", st.ReadCloser)
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
	if st.Size != int64(len(page)) {
		t.Errorf("Size = %d, want %d", st.Size, len(page))
	}
}

func TestHV3Source_failureStatuses(t *testing.T) {
	t.Parallel()

	page := testutil.TinyJPEG(t, 8, 8)
	cases := []struct {
		name   string
		raw    []byte
		status archive.Status
	}{
		{
			name:   "nothing but junk is empty, not an error",
			raw:    hv3Book(t, testutil.HV3EncrMask, testutil.HV3Entry{Name: "Thumbs.db", Data: []byte("junk")}),
			status: archive.StatusEmpty,
		},
		{
			name: "an ENCR mode nobody has decoded is unsupported, not corrupt",
			raw: testutil.BuildHV3(t, testutil.HV3Spec{
				Entries: []testutil.HV3Entry{{Name: "001.jpg", Data: page}}, Encr: 7,
			}),
			status: archive.StatusUnsupported,
		},
		{
			name:   "a file that is not an HV3 at all is corrupt",
			raw:    []byte("PK\x03\x04 this is a zip"),
			status: archive.StatusError,
		},
		{
			name: "a directory that runs past the end of the file is corrupt",
			raw: func() []byte {
				big := uint64(1 << 40)
				return testutil.BuildHV3(t, testutil.HV3Spec{
					Entries:          []testutil.HV3Entry{{Name: "001.jpg", Data: page}},
					ListSizeOverride: &big,
				})
			}(),
			status: archive.StatusError,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t, map[string]any{"책.hv3": tc.raw})
			src := f.open(t, source.KindHV3, "책.hv3")
			_, err := src.List(t.Context())
			if err == nil {
				t.Fatal("List succeeded, want a failure")
			}
			if got := source.StatusOf(err); got != tc.status {
				t.Errorf("StatusOf(%v) = %q, want %q", err, got, tc.status)
			}
			// FR-IDX-010: whatever the verdict, it must not leak the opaque
			// book id into a sentence a reader sees.
			if errors.Is(err, archive.ErrEncrypted) {
				t.Error("an unreadable HV3 must not be reported as password-protected — no HV3 has a password")
			}
		})
	}
}

// TestHV3Source_isNotForeignAnyMore is the one-line consequence of E-51 that
// the rest of the product actually reads: a `.hv3` used to be a format
// [source.ForeignFormat] named and D-72 closed a book over. It has a reader
// now, so it must not be in that table — a format there is one nothing can
// open, and a stale entry would make a readable volume report `unsupported`.
func TestHV3Source_isNotForeignAnyMore(t *testing.T) {
	t.Parallel()

	if got := source.ForeignFormat("펌프킨 시저스 04.hv3"); got != "" {
		t.Errorf("ForeignFormat(.hv3) = %q, want empty — it has a reader now", got)
	}
	if got := source.ContainerKind("펌프킨 시저스 04.hv3"); got != source.KindHV3 {
		t.Errorf("ContainerKind(.hv3) = %q, want %q", got, source.KindHV3)
	}
	if !source.NestedVolumeExt(".hv3") {
		t.Error("NestedVolumeExt(.hv3) = false — the one HV3 in the collection lives inside a ZIP")
	}
	if got := source.NestedKind(".hv3"); got != source.KindNestedHV3 {
		t.Errorf("NestedKind(.hv3) = %q, want %q", got, source.KindNestedHV3)
	}
	// `.7z` is what the table is still for, and the assertion that the two
	// halves of E-51 did not blur into one edit.
	if got := source.ForeignFormat("특전.7z"); got != "7-Zip" {
		t.Errorf("ForeignFormat(.7z) = %q, want 7-Zip", got)
	}
	if got := source.ContainerKind("특전.7z"); got != "" {
		t.Errorf("ContainerKind(.7z) = %q, want empty", got)
	}
}

// TestHV3Source_nestedVolume is the collection's actual shape: the HV3 is the
// sole entry of `펌프킨 시저스 04.zip`, so it is read through
// internal/archive/nested rather than off the disk.
//
// The inner container is deflated, which is the case that costs something: the
// adapter inflates forward, and HV3 keeps its directory at the front where the
// adapter's tail window cannot help. Reading a page therefore reads the header,
// then the directory, then the payload — in that order, which is the ordering
// [hv3.Reader.OpenEntry] is written to preserve.
func TestHV3Source_nestedVolume(t *testing.T) {
	t.Parallel()

	pages := []testutil.HV3Entry{
		{Name: "0001.jpg", Data: testutil.TinyJPEG(t, 8, 8)},
		{Name: "0002.jpg", Data: testutil.TinyJPEG(t, 16, 16)},
	}
	inner := hv3Book(t, testutil.HV3EncrMask, pages...)
	outer := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
		{RawName: testutil.CP949(t, "펌프킨 시저스 04.hv3"), Data: inner, Method: testutil.MethodDeflate},
	}})

	f := newFixture(t, map[string]any{"펌프킨 시저스 04.zip": outer})
	src, err := f.factory.Open(t.Context(), source.Book{
		ID: "bk", Kind: source.KindNestedHV3, RootName: rootName,
		RelPath: "펌프킨 시저스 04.zip", InnerPath: "펌프킨 시저스 04.hv3",
	})
	if err != nil {
		t.Fatalf("Factory.Open: %v", err)
	}
	defer func() { _ = src.Close() }()

	l, err := src.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := pageNames(l.Pages); !slices.Equal(got, []string{"0001.jpg", "0002.jpg"}) {
		t.Fatalf("pages = %v, want the two pages of the inner volume", got)
	}
	for i, want := range pages {
		if got := readPage(t, src, l.Pages[i]); !bytes.Equal(got, want.Data) {
			t.Errorf("page %d: %d bytes, want %d", i+1, len(got), len(want.Data))
		}
	}
}

// TestHV3Source_volumesOfAContainer is the question the scanner asks a
// container that produced no pages of its own: which of your entries are whole
// books? `펌프킨 시저스 04.zip` has exactly one answer, and under D-72 it had
// none.
func TestHV3Source_volumesOfAContainer(t *testing.T) {
	t.Parallel()

	inner := hv3Book(t, testutil.HV3EncrMask, testutil.HV3Entry{
		Name: "0001.jpg", Data: testutil.TinyJPEG(t, 8, 8),
	})
	outer := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
		{RawName: testutil.CP949(t, "펌프킨 시저스 04.hv3"), Data: inner, Method: testutil.MethodDeflate},
	}})
	f := newFixture(t, map[string]any{"펌프킨 시저스 04.zip": outer})
	src := f.open(t, source.KindZIP, "펌프킨 시저스 04.zip")

	lister, ok := src.(source.VolumeLister)
	if !ok {
		t.Fatal("a ZIP container does not implement VolumeLister")
	}
	vols, err := lister.Volumes(t.Context())
	if err != nil {
		t.Fatalf("Volumes: %v", err)
	}
	if !slices.Equal(vols, []string{"펌프킨 시저스 04.hv3"}) {
		t.Errorf("Volumes() = %v, want the one HV3 entry", vols)
	}
}
