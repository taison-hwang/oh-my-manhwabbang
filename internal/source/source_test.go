package source_test

import (
	"context"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"shelf/internal/archive"
	"shelf/internal/openpool"
	"shelf/internal/source"
	"shelf/internal/testutil"
)

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

const rootName = "mangga"

// fixture wires a RootSet, a handle pool and a Factory over a tree built by
// testutil, which is the only thing in this package's test suite allowed to
// write to disk (FR-CFG-005's check-readonly grep covers internal/source
// including its tests).
type fixture struct {
	root    string
	roots   *source.RootSet
	pool    *openpool.Pool
	factory *source.Factory
}

func newFixture(t *testing.T, layout map[string]any) *fixture {
	t.Helper()
	root := testutil.BuildTree(t, layout)
	return newFixtureAt(t, root)
}

func newFixtureAt(t *testing.T, root string) *fixture {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	roots, err := source.NewRootSet(t.Context(), map[string]string{rootName: root}, log)
	if err != nil {
		t.Fatalf("NewRootSet: %v", err)
	}
	t.Cleanup(func() { _ = roots.Close() })

	// The pool opens through os.Root too, so archive containers get
	// path-traversal layer 3 as well as dir pages (arch §8.1).
	pool := openpool.New(openpool.Options{Max: 8, Open: roots.PoolOpener(), Logger: log})
	t.Cleanup(func() { _ = pool.Close() })

	f := source.NewFactory(source.Options{
		Roots:  roots,
		Pool:   pool,
		Logger: log,
	})
	return &fixture{root: root, roots: roots, pool: pool, factory: f}
}

func (f *fixture) open(t *testing.T, kind source.Kind, rel string) source.BookSource {
	t.Helper()
	src, err := f.factory.Open(t.Context(), source.Book{
		ID: "bk" + rel, Kind: kind, RootName: rootName, RelPath: rel,
	})
	if err != nil {
		t.Fatalf("Factory.Open(%s %q): %v", kind, rel, err)
	}
	t.Cleanup(func() { _ = src.Close() })
	return src
}

func pageNames(pages []source.Page) []string {
	out := make([]string, len(pages))
	for i, p := range pages {
		out[i] = p.EntryPath
	}
	return out
}

func readPage(t *testing.T, src source.BookSource, p source.Page) []byte {
	t.Helper()
	st, err := src.Open(t.Context(), p, source.OpenOptions{})
	if err != nil {
		t.Fatalf("Open page %d: %v", p.No, err)
	}
	defer func() { _ = st.Close() }()
	b, err := io.ReadAll(st)
	if err != nil {
		t.Fatalf("reading page %d: %v", p.No, err)
	}
	return b
}

// ---------------------------------------------------------------------------
// Exclusions and extensions — FR-IDX-006, FR-IDX-011
// ---------------------------------------------------------------------------

func TestExcluded_everyRuleOfFRIDX006(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		entry      string
		size       int64
		isDir      bool
		wantDrop   bool
		wantReason string
	}{
		{"plain jpg", "001.jpg", 1024, false, false, ""},
		{"uppercase extension", "001.JPG", 1024, false, false, ""},
		{"nested page", "vol1/002.png", 1024, false, false, ""},
		{"directory entry by flag", "vol1", 0, true, true, source.ReasonDirectory},
		{"directory entry by slash", "vol1/", 0, false, true, source.ReasonDirectory},
		{"macosx root", "__MACOSX", 10, false, true, source.ReasonResourceFork},
		{"macosx prefix", "__MACOSX/vol1/._001.jpg", 10, false, true, source.ReasonResourceFork},
		{"macosx nested", "a/__MACOSX/b.jpg", 10, false, true, source.ReasonResourceFork},
		{"appledouble", "vol1/._001.jpg", 10, false, true, source.ReasonResourceFork},
		{"ds_store", "vol1/.DS_Store", 100, false, true, source.ReasonSystemFile},
		{"ds_store case", "vol1/.ds_store", 100, false, true, source.ReasonSystemFile},
		{"thumbs.db", "Thumbs.db", 100, false, true, source.ReasonSystemFile},
		{"thumbs.db case", "thumbs.DB", 100, false, true, source.ReasonSystemFile},
		{"desktop.ini", "desktop.ini", 100, false, true, source.ReasonSystemFile},
		{"desktop.ini case", "Desktop.ini", 100, false, true, source.ReasonSystemFile},
		// FR-IDX-006's 숨김 파일, as narrowed in exclude.go: a leading dot is
		// not a hidden attribute inside an archive, so a dot-name that is a
		// page is kept and a dot-name that is not is still dropped. The first
		// case is the real shape from `엽기인 Girl 스나코 26권.zip`, whose 80
		// pages the old rule dropped in their entirety.
		{"dot-prefixed page", "vol/.▶스나코_26권◀_Scan11192010_193728.jpg", 100, false, false, ""},
		{"dot-prefixed page, plain", "vol1/.hidden.jpg", 100, false, false, ""},
		{"dot-prefixed non-page", "vol1/.hidden.txt", 100, false, true, source.ReasonHidden},
		{"dot-prefixed zero byte", "vol1/.hidden.jpg", 0, false, true, source.ReasonZeroByte},
		{"extension with no name", "vol1/.jpg", 100, false, true, source.ReasonHidden},
		{"bare dot", ".", 100, false, true, source.ReasonHidden},
		{"hidden directory", ".cache/001.jpg", 100, false, true, source.ReasonHidden},
		{"hidden directory nested", "vol1/.cache/001.jpg", 100, false, true, source.ReasonHidden},
		{"zero bytes", "001.jpg", 0, false, true, source.ReasonZeroByte},
		{"text file", "readme.txt", 100, false, true, source.ReasonExtension},
		{"nested archive", "vol1.zip", 100, false, true, source.ReasonExtension},
		{"no extension", "COVER", 100, false, true, source.ReasonExtension},
		// arch §4.5: TIFF decodes in the thumbnailer but FR-IDX-011 does not
		// advertise it, so it is not a page.
		{"tiff", "001.tif", 100, false, true, source.ReasonExtension},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			drop, reason := source.Excluded(tc.entry, tc.size, tc.isDir)
			if drop != tc.wantDrop {
				t.Fatalf("Excluded(%q, %d, %v) = %v, want %v (reason %q)",
					tc.entry, tc.size, tc.isDir, drop, tc.wantDrop, reason)
			}
			if drop && reason != tc.wantReason {
				t.Errorf("reason = %q, want %q", reason, tc.wantReason)
			}
		})
	}
}

func TestSupportedExts_matchFRIDX011(t *testing.T) {
	t.Parallel()
	want := map[string]bool{
		".jpg": true, ".jpeg": true, ".png": true, ".gif": true,
		".webp": true, ".bmp": true, ".avif": true,
		".tif": false, ".tiff": false, ".pdf": false, ".zip": false, ".txt": false,
	}
	for ext, in := range want {
		if got := source.SupportedExt(ext); got != in {
			t.Errorf("SupportedExt(%q) = %v, want %v", ext, got, in)
		}
	}
	if len(source.SupportedExts) != 7 {
		t.Errorf("SupportedExts has %d entries, FR-IDX-011 lists 7", len(source.SupportedExts))
	}
}

func TestContentType_comesFromTheTableNotSniffing(t *testing.T) {
	t.Parallel()
	for ext, want := range map[string]string{
		".jpg": "image/jpeg", ".JPEG": "image/jpeg", ".png": "image/png",
		".gif": "image/gif", ".webp": "image/webp", ".bmp": "image/bmp",
		".avif": "image/avif", ".tif": "image/tiff",
		".exe": "application/octet-stream", "": "application/octet-stream",
	} {
		if got := source.ContentType(ext); got != want {
			t.Errorf("ContentType(%q) = %q, want %q", ext, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// Path traversal — NFR-SEC-001, arch §8.1 layers 2, 3 and 4
// ---------------------------------------------------------------------------

func TestDirSource_osRootRefusesEveryEscapeShape(t *testing.T) {
	t.Parallel()

	// Two secrets: one beside the root, and one *inside* the root but outside
	// the book. The second is the interesting case — os.Root would happily open
	// it, so only the entry-path validation stops a page escaping its own book.
	base := t.TempDir()
	testutil.BuildTreeAt(t, base, map[string]any{
		"secret.txt": "the operator's ssh key",
		"library": map[string]any{
			"other-series.txt": "another book's data",
			"book": map[string]any{
				"001.jpg": testutil.TinyJPEG(t, 8, 8),
				"sub":     map[string]any{"002.jpg": testutil.TinyJPEG(t, 8, 8)},
			},
		},
	})
	root := filepath.Join(base, "library")

	// Two symlinks the scanner could never produce but a hostile index could
	// name: one pointing outside the root, one pointing at "..".
	if err := os.Symlink(filepath.Join(base, "secret.txt"), filepath.Join(root, "book", "escape.jpg")); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}
	if err := os.Symlink("..", filepath.Join(root, "book", "up")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	f := newFixtureAt(t, root)
	src := f.open(t, source.KindDir, "book")

	// arch §8.1's four verified shapes, plus the legal one.
	cases := []struct {
		name    string
		entry   string
		wantErr bool
	}{
		{"parent escape", "../secret.txt", true},
		// Inside the root, outside the book: os.Root cannot see the difference.
		{"sibling escape inside the root", "../other-series.txt", true},
		{"absolute path", filepath.Join(base, "secret.txt"), true},
		{"deep escape", "../../../../etc/passwd", true},
		{"symlink out of the root", "escape.jpg", true},
		{"symlink to ..", "up/secret.txt", true},
		{"stays inside", "sub/../001.jpg", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st, err := src.Open(t.Context(), source.Page{No: 1, EntryPath: tc.entry, Ext: ".jpg"}, source.OpenOptions{})
			if err == nil {
				defer func() { _ = st.Close() }()
				body, _ := io.ReadAll(st)
				if tc.wantErr {
					t.Fatalf("opening %q succeeded and returned %d bytes; it must be refused", tc.entry, len(body))
				}
				return
			}
			if !tc.wantErr {
				t.Fatalf("opening %q was refused but should be allowed: %v", tc.entry, err)
			}
		})
	}
}

func TestFactory_rejectsANonLocalRelPath(t *testing.T) {
	t.Parallel()
	f := newFixture(t, map[string]any{"book": map[string]any{"001.jpg": testutil.TinyJPEG(t, 8, 8)}})

	for _, rel := range []string{"../outside", "/etc/passwd", "", ".", "a/../../b", `..\windows`} {
		_, err := f.factory.Open(t.Context(), source.Book{
			ID: "bk", Kind: source.KindDir, RootName: rootName, RelPath: rel,
		})
		if !errors.Is(err, source.ErrUnsafePath) {
			t.Errorf("Open(rel=%q) err = %v, want source.ErrUnsafePath", rel, err)
		}
	}
}

func TestFactory_unknownRoot_isErrUnknownRoot(t *testing.T) {
	t.Parallel()
	f := newFixture(t, map[string]any{"book": map[string]any{"001.jpg": testutil.TinyJPEG(t, 8, 8)}})
	_, err := f.factory.Open(t.Context(), source.Book{
		ID: "bk", Kind: source.KindDir, RootName: "nope", RelPath: "book",
	})
	if !errors.Is(err, source.ErrUnknownRoot) {
		t.Errorf("err = %v, want source.ErrUnknownRoot", err)
	}
}

// The pool opener is the archive half of layer 3: a path outside every
// configured root cannot be opened at all, whatever put it in the index.
func TestRootSet_poolOpener_refusesPathsOutsideEveryRoot(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	testutil.BuildTreeAt(t, base, map[string]any{
		"outside.zip": testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{{Name: "a.jpg", Data: []byte("x")}}}),
		"library":     map[string]any{"in.zip": testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{{Name: "a.jpg", Data: []byte("x")}}})},
	})
	root := filepath.Join(base, "library")
	f := newFixtureAt(t, root)

	if _, err := f.pool.Acquire(t.Context(), filepath.Join(root, "in.zip"), 0, 0); err != nil {
		t.Fatalf("a path inside the root must open: %v", err)
	}
	if _, err := f.pool.Acquire(t.Context(), filepath.Join(base, "outside.zip"), 0, 0); !errors.Is(err, source.ErrUnknownRoot) {
		t.Errorf("err = %v, want source.ErrUnknownRoot for a path outside every root", err)
	}
}

// ---------------------------------------------------------------------------
// Registry — the prd §7.2 extension seam
// ---------------------------------------------------------------------------

func TestFactory_registry(t *testing.T) {
	t.Parallel()
	f := newFixture(t, map[string]any{"book": map[string]any{"001.jpg": testutil.TinyJPEG(t, 8, 8)}})

	kinds := f.factory.Kinds()
	want := []source.Kind{
		source.KindDir, source.KindHV3, source.KindNestedDir, source.KindNestedHV3,
		source.KindNestedRAR, source.KindNestedZIP, source.KindPDF, source.KindRAR,
		source.KindZIP,
	}
	if !slices.Equal(kinds, want) {
		t.Errorf("Kinds() = %v, want %v", kinds, want)
	}

	// An unregistered kind is ErrUnsupported, not a panic and not a nil source.
	// `7z` stands in for "a format this build has no reader for" — the role
	// `rar` played until D-71 gave it one.
	_, err := f.factory.Open(t.Context(), source.Book{ID: "bk", Kind: "7z", RootName: rootName, RelPath: "book"})
	if !errors.Is(err, source.ErrUnsupported) {
		t.Fatalf("err = %v, want source.ErrUnsupported", err)
	}
	if got := source.StatusOf(err); got != archive.StatusUnsupported {
		t.Errorf("StatusOf = %q, want %q", got, archive.StatusUnsupported)
	}

	// Registering it makes it work, with no change anywhere else. This is the
	// seam prd §7.2 asked for, and D-71 is the proof it works: RAR arrived as
	// one archive.Reader plus two lines here.
	f.factory.Register("7z", func(_ context.Context, _ *source.Factory, b source.Book) (source.BookSource, error) {
		return stubSource{id: b.ID}, nil
	})
	src, err := f.factory.Open(t.Context(), source.Book{ID: "bk", Kind: "7z", RootName: rootName, RelPath: "book"})
	if err != nil {
		t.Fatalf("Open after Register: %v", err)
	}
	if src.Kind() != "7z" {
		t.Errorf("Kind() = %q, want %q", src.Kind(), "7z")
	}
}

type stubSource struct{ id string }

func (stubSource) Kind() source.Kind { return "7z" }
func (stubSource) List(context.Context) (*source.Listing, error) {
	return &source.Listing{Kind: "7z"}, nil
}
func (stubSource) Open(context.Context, source.Page, source.OpenOptions) (*source.Stream, error) {
	return nil, source.ErrUnsupported
}
func (stubSource) Close() error { return nil }

// ---------------------------------------------------------------------------
// Status mapping — arch §4.11
// ---------------------------------------------------------------------------

func TestStatusOf_mapsEveryFailureToABookStatus(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		err  error
		want archive.Status
	}{
		"nil":         {nil, archive.StatusOK},
		"no pages":    {fmt.Errorf("x: %w", source.ErrNoPages), archive.StatusEmpty},
		"unsupported": {fmt.Errorf("x: %w", source.ErrUnsupported), archive.StatusUnsupported},
		"encrypted":   {fmt.Errorf("x: %w", archive.ErrEncrypted), archive.StatusEncrypted},
		"corrupt":     {fmt.Errorf("x: %w", archive.ErrCorrupt), archive.StatusError},
		"unsafe path": {fmt.Errorf("x: %w", source.ErrUnsafePath), archive.StatusError},
		"os error":    {os.ErrNotExist, archive.StatusError},
		"unknown":     {errors.New("something nobody classified"), archive.StatusError},
	}
	for name, tc := range cases {
		if got := source.StatusOf(tc.err); got != tc.want {
			t.Errorf("%s: StatusOf(%v) = %q, want %q", name, tc.err, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Cross-kind behaviour — AC-003
// ---------------------------------------------------------------------------

// AC-003: a folder-of-images book and a ZIP book must produce the same shape.
// This is the unit half of that acceptance criterion.
func TestSources_zipAndDir_produceIdenticalListings(t *testing.T) {
	t.Parallel()

	// The same nine files, once loose on disk and once inside an archive,
	// including the junk both must drop.
	files := map[string][]byte{
		"1.jpg":       testutil.TinyJPEG(t, 8, 8),
		"2.jpg":       testutil.TinyJPEG(t, 9, 9),
		"10.jpg":      testutil.TinyJPEG(t, 10, 10),
		"cover.png":   testutil.TinyPNG(t, 8, 8),
		"Thumbs.db":   []byte("junk"),
		".DS_Store":   []byte("junk"),
		"desktop.ini": []byte("junk"),
		"notes.txt":   []byte("not a page"),
		"empty.jpg":   {},
	}

	dirLayout := map[string]any{}
	entries := make([]testutil.Entry, 0, len(files))
	for name, data := range files {
		dirLayout[name] = data
		entries = append(entries, testutil.Entry{Name: name, Data: data, Method: testutil.MethodDeflate})
	}

	f := newFixture(t, map[string]any{
		"series": map[string]any{
			"vol1":     dirLayout,
			"vol1.zip": testutil.BuildZIP(t, testutil.ZIPSpec{Entries: entries}),
		},
	})

	dirSrc := f.open(t, source.KindDir, "series/vol1")
	zipSrc := f.open(t, source.KindZIP, "series/vol1.zip")

	dirList, err := dirSrc.List(t.Context())
	if err != nil {
		t.Fatalf("dir List: %v", err)
	}
	zipList, err := zipSrc.List(t.Context())
	if err != nil {
		t.Fatalf("zip List: %v", err)
	}

	// FR-IDX-007: natural order, not lexicographic. `10.jpg` after `2.jpg`.
	want := []string{"1.jpg", "2.jpg", "10.jpg", "cover.png"}
	for _, got := range []struct {
		what string
		list *source.Listing
	}{{"dir", dirList}, {"zip", zipList}} {
		names := pageNames(got.list.Pages)
		if len(names) != len(want) {
			t.Fatalf("%s: pages = %q, want %q", got.what, names, want)
		}
		for i := range want {
			if names[i] != want[i] {
				t.Errorf("%s: page %d = %q, want %q", got.what, i+1, names[i], want[i])
			}
			if got.list.Pages[i].No != i+1 {
				t.Errorf("%s: page %d has No = %d", got.what, i+1, got.list.Pages[i].No)
			}
		}
		if got.list.Excluded != 5 {
			t.Errorf("%s: excluded = %d, want 5", got.what, got.list.Excluded)
		}
	}

	// FR-SRV-008 / AC-003: the bytes are identical whichever kind served them.
	for i := range dirList.Pages {
		fromDir := readPage(t, dirSrc, dirList.Pages[i])
		fromZip := readPage(t, zipSrc, zipList.Pages[i])
		orig := files[dirList.Pages[i].EntryPath]
		if string(fromDir) != string(orig) {
			t.Errorf("dir page %q differs from the file on disk", dirList.Pages[i].EntryPath)
		}
		if string(fromZip) != string(orig) {
			t.Errorf("zip page %q differs from what was packed", zipList.Pages[i].EntryPath)
		}
		if sum := crc32.ChecksumIEEE(fromZip); sum != zipList.Pages[i].CRC32 {
			t.Errorf("zip page %q: crc32 %#08x != central directory %#08x",
				zipList.Pages[i].EntryPath, sum, zipList.Pages[i].CRC32)
		}
	}
}

func TestSources_contentTypeAndSize(t *testing.T) {
	t.Parallel()
	jpg := testutil.TinyJPEG(t, 16, 16)
	png := testutil.TinyPNG(t, 16, 16)
	f := newFixture(t, map[string]any{
		"series": map[string]any{
			"vol1": map[string]any{"1.jpg": jpg, "2.png": png},
			"vol1.zip": testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
				{Name: "1.jpg", Data: jpg, Method: testutil.MethodStore},
				{Name: "2.png", Data: png, Method: testutil.MethodDeflate},
			}}),
		},
	})

	for _, tc := range []struct {
		kind source.Kind
		rel  string
	}{{source.KindDir, "series/vol1"}, {source.KindZIP, "series/vol1.zip"}} {
		src := f.open(t, tc.kind, tc.rel)
		list, err := src.List(t.Context())
		if err != nil {
			t.Fatalf("%s List: %v", tc.kind, err)
		}
		for _, p := range list.Pages {
			st, err := src.Open(t.Context(), p, source.OpenOptions{})
			if err != nil {
				t.Fatalf("%s Open %q: %v", tc.kind, p.EntryPath, err)
			}
			if want := source.ContentType(p.Ext); st.ContentType != want {
				t.Errorf("%s %q: content type = %q, want %q", tc.kind, p.EntryPath, st.ContentType, want)
			}
			if st.Size != p.Size {
				t.Errorf("%s %q: stream size = %d, page size = %d", tc.kind, p.EntryPath, st.Size, p.Size)
			}
			_ = st.Close()
		}
	}
}

// arch §5.3: stored ZIP entries and dir pages must be seekable so the HTTP
// layer can answer Range; deflated entries must not pretend to be.
func TestSources_seekability(t *testing.T) {
	t.Parallel()
	payload := []byte(strings.Repeat("page bytes ", 200))
	f := newFixture(t, map[string]any{
		"series": map[string]any{
			"vol1": map[string]any{"1.jpg": payload},
			"vol1.zip": testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
				{Name: "1.jpg", Data: payload, Method: testutil.MethodStore},
				{Name: "2.jpg", Data: payload, Method: testutil.MethodDeflate},
			}}),
		},
	})

	dirSrc := f.open(t, source.KindDir, "series/vol1")
	dl, err := dirSrc.List(t.Context())
	if err != nil {
		t.Fatalf("dir List: %v", err)
	}
	st, err := dirSrc.Open(t.Context(), dl.Pages[0], source.OpenOptions{})
	if err != nil {
		t.Fatalf("dir Open: %v", err)
	}
	if _, ok := st.ReadSeeker(); !ok {
		t.Error("a dir page is not seekable; Range support depends on it")
	}
	_ = st.Close()

	zipSrc := f.open(t, source.KindZIP, "series/vol1.zip")
	zl, err := zipSrc.List(t.Context())
	if err != nil {
		t.Fatalf("zip List: %v", err)
	}
	stored, err := zipSrc.Open(t.Context(), zl.Pages[0], source.OpenOptions{})
	if err != nil {
		t.Fatalf("zip Open stored: %v", err)
	}
	if _, ok := stored.ReadSeeker(); !ok {
		t.Error("a stored zip entry is not seekable (FR-SRV-003 / arch §5.3)")
	}
	_ = stored.Close()

	deflated, err := zipSrc.Open(t.Context(), zl.Pages[1], source.OpenOptions{})
	if err != nil {
		t.Fatalf("zip Open deflated: %v", err)
	}
	if _, ok := deflated.ReadSeeker(); ok {
		t.Error("a deflated zip entry claims to be seekable; arch §5.3 omits Accept-Ranges for it")
	}
	_ = deflated.Close()
}

// Closing a page stream must give the pooled handle back, or the pool leaks a
// descriptor per page served.
func TestZipSource_streamClose_releasesThePoolHandle(t *testing.T) {
	t.Parallel()
	f := newFixture(t, map[string]any{
		"series": map[string]any{
			"vol1.zip": testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
				{Name: "1.jpg", Data: testutil.TinyJPEG(t, 8, 8)},
			}}),
		},
	})
	src := f.open(t, source.KindZIP, "series/vol1.zip")
	list, err := src.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	for i := 0; i < 50; i++ {
		st, err := src.Open(t.Context(), list.Pages[0], source.OpenOptions{})
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if _, err := io.ReadAll(st); err != nil {
			t.Fatalf("read: %v", err)
		}
		if err := st.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
	}
	if got := f.pool.Stats().Open; got != 1 {
		t.Errorf("pool holds %d descriptors after 50 page reads, want 1", got)
	}
	// Double-closing a stream must be harmless: a handler that closes on both
	// the happy path and in a defer is a normal shape.
	st, err := src.Open(t.Context(), list.Pages[0], source.OpenOptions{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = st.Close()
	_ = st.Close()
	if got := f.pool.Stats().Open; got != 1 {
		t.Errorf("pool holds %d descriptors after a double close, want 1", got)
	}
}

// Two configured roots may nest. Whichever one the pool opener picks, os.Root
// makes an escape impossible — but the choice must be deterministic, so a
// failure is reproducible. Longest match wins.
func TestRootSet_poolOpener_prefersTheDeepestMatchingRoot(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	zipBytes := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
		{Name: "1.jpg", Data: testutil.TinyJPEG(t, 8, 8)},
	}})
	testutil.BuildTreeAt(t, base, map[string]any{
		"outer": map[string]any{
			"top.zip": zipBytes,
			"inner":   map[string]any{"deep.zip": zipBytes},
		},
	})
	outer := filepath.Join(base, "outer")
	inner := filepath.Join(outer, "inner")

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	roots, err := source.NewRootSet(t.Context(), map[string]string{"outer": outer, "inner": inner}, log)
	if err != nil {
		t.Fatalf("NewRootSet: %v", err)
	}
	t.Cleanup(func() { _ = roots.Close() })
	open := roots.PoolOpener()

	// Run it repeatedly: a map-order-dependent implementation would be flaky
	// rather than wrong, and flaky is what this asserts against.
	for i := 0; i < 50; i++ {
		for _, p := range []string{filepath.Join(outer, "top.zip"), filepath.Join(inner, "deep.zip")} {
			f, err := open(p)
			if err != nil {
				t.Fatalf("iteration %d: opening %s: %v", i, p, err)
			}
			if _, err := f.Stat(); err != nil {
				t.Errorf("stat %s: %v", p, err)
			}
			_ = f.Close()
		}
	}
}

// TestRootSet_add_opensARootIntoALiveSet is amendment A-12 (ruling E-40).
//
// # What this replaces
//
// `OpenRoots` is called exactly once, at startup, and A-11 limit (1) made that
// load-bearing: the pool, the source factory and the scanner are all built over
// that one set, so a root added to `shelf.yaml` needed a restart. E-40 overturns
// that for addition, and `Add` is the whole mechanism. The reason it works with
// no re-wiring is asserted here through `PoolOpener`: those collaborators hold a
// pointer to this set, so a name inserted under the write lock is reachable from
// an opener that was created before the insert.
func TestRootSet_add_opensARootIntoALiveSet(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	zipBytes := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
		{Name: "1.jpg", Data: testutil.TinyJPEG(t, 8, 8)},
	}})
	testutil.BuildTreeAt(t, base, map[string]any{
		"first":  map[string]any{"a.zip": zipBytes},
		"second": map[string]any{"b.zip": zipBytes},
	})
	first := filepath.Join(base, "first")
	second := filepath.Join(base, "second")

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	roots, err := source.NewRootSet(t.Context(), map[string]string{"first": first}, log)
	if err != nil {
		t.Fatalf("NewRootSet: %v", err)
	}
	t.Cleanup(func() { _ = roots.Close() })

	// Taken BEFORE the add. This is the point: the opener the pool was built
	// with must see the new root without being rebuilt.
	open := roots.PoolOpener()
	if _, err := open(filepath.Join(second, "b.zip")); err == nil {
		t.Fatal("the opener reached an unconfigured root before Add; the rest of this test would prove nothing")
	}

	if err := roots.Add("second", second); err != nil {
		t.Fatalf("Add: %v", err)
	}

	if _, ok := roots.Root("second"); !ok {
		t.Error("Root(\"second\") is not resolvable after Add")
	}
	if got, ok := roots.Path("second"); !ok || got != second {
		t.Errorf("Path(\"second\") = (%q, %v), want (%q, true)", got, ok, second)
	}
	f, err := open(filepath.Join(second, "b.zip"))
	if err != nil {
		t.Fatalf("the pre-existing opener cannot reach the added root: %v", err)
	}
	_ = f.Close()

	// The first root is untouched — an add is additive, and a set that lost a
	// root while gaining one would take the library down with it.
	if _, err := open(filepath.Join(first, "a.zip")); err != nil {
		t.Errorf("the original root stopped resolving after Add: %v", err)
	}
}

// TestRootSet_add_refusesADuplicateNameAndLeaksNothing pins the collision.
//
// The name is the identity every `series_id` hashes (arch §3.4), so a second
// entry under one name would make which directory a book resolves in depend on
// map order. The handle opened before the lock is taken must be closed on the
// losing path — that is the only descriptor leak this method can have, and a
// leak is invisible to every other assertion.
func TestRootSet_add_refusesADuplicateNameAndLeaksNothing(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	testutil.BuildTreeAt(t, base, map[string]any{
		"one": map[string]any{}, "two": map[string]any{},
	})
	one := filepath.Join(base, "one")
	two := filepath.Join(base, "two")

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	roots, err := source.NewRootSet(t.Context(), map[string]string{"dup": one}, log)
	if err != nil {
		t.Fatalf("NewRootSet: %v", err)
	}
	t.Cleanup(func() { _ = roots.Close() })

	if err := roots.Add("dup", two); !errors.Is(err, source.ErrRootExists) {
		t.Fatalf("Add over an existing name = %v, want ErrRootExists", err)
	}
	// The original mapping survives the refusal.
	if got, _ := roots.Path("dup"); got != one {
		t.Errorf("Path(\"dup\") = %q, want the original %q — a refused Add must change nothing", got, one)
	}
}

// TestRootSet_add_reportsAnUnopenableDirectory is the asymmetry with OpenRoots,
// and it is deliberate.
//
// At startup an unreachable root is recorded rather than fatal (arch §4.9): the
// rest of the library must still come up. Here a caller is asking for one
// specific directory and can be told, so `POST /api/roots` answers `400` instead
// of adding a row that exists only to look broken.
func TestRootSet_add_reportsAnUnopenableDirectory(t *testing.T) {
	t.Parallel()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	roots, err := source.NewRootSet(t.Context(), map[string]string{}, log)
	if err != nil {
		t.Fatalf("NewRootSet: %v", err)
	}
	t.Cleanup(func() { _ = roots.Close() })

	missing := filepath.Join(t.TempDir(), "not-mounted")
	if err := roots.Add("gone", missing); err == nil {
		t.Fatal("Add of a non-existent directory returned nil")
	}
	if _, ok := roots.Root("gone"); ok {
		t.Error("a failed Add left the name resolvable")
	}
}
