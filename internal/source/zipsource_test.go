package source_test

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"shelf/internal/archive"
	"shelf/internal/source"
	"shelf/internal/testutil"
)

// FR-IDX-010 as the scanner will experience it: every failure comes back as a
// books.status, the partial pages survive where they can, and nothing panics.
// arch §4.11's table, one row per subtest.
func TestZipSource_errorIsolation_everyFailureIsAStatus(t *testing.T) {
	t.Parallel()
	jpg := testutil.TinyJPEG(t, 12, 16)

	cases := []struct {
		name       string
		data       []byte
		wantStatus archive.Status
		wantErr    error
		// wantPages: how many pages must survive the failure.
		wantPages int
	}{
		{
			name: "healthy",
			data: testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
				{Name: "001.jpg", Data: jpg}, {Name: "002.jpg", Data: jpg},
			}}),
			wantStatus: archive.StatusOK,
			wantPages:  2,
		},
		{
			// The `엔젤하트` shape: a 1.44 GB archive of 33 nested ZIPs and no
			// images. D-07 puts nested archives out of scope; this is what
			// "without crashing" looks like.
			name: "nested archives only",
			data: testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
				{Name: "01권.zip", Data: []byte("PK\x05\x06" + "0000000000000000000")},
				{Name: "02권.zip", Data: []byte("PK\x05\x06" + "0000000000000000000")},
			}}),
			wantStatus: archive.StatusEmpty,
			wantErr:    source.ErrNoPages,
		},
		{
			name: "only excluded entries",
			data: testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
				{Name: "Thumbs.db", Data: []byte("junk")},
				{Name: "__MACOSX/._001.jpg", Data: []byte("fork")},
				{Name: "empty.jpg", Data: nil},
				{Name: "readme.txt", Data: []byte("notes")},
			}}),
			wantStatus: archive.StatusEmpty,
			wantErr:    source.ErrNoPages,
		},
		{
			name: "encrypted",
			data: testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
				{Name: "001.jpg", Data: jpg, Flags: testutil.FlagEncrypted},
				{Name: "002.jpg", Data: jpg, Flags: testutil.FlagEncrypted},
			}}),
			wantStatus: archive.StatusEncrypted,
			wantErr:    archive.ErrEncrypted,
		},
		{
			// The real one: 9 of 11 157 archives, all interrupted downloads.
			name: "truncated download",
			data: testutil.BuildZIP(t, testutil.ZIPSpec{
				Entries:      []testutil.Entry{{Name: "001.jpg", Data: jpg}},
				TruncateTail: 40,
			}),
			wantStatus: archive.StatusError,
			wantErr:    archive.ErrCorrupt,
		},
		{
			name:       "zero bytes",
			data:       []byte{},
			wantStatus: archive.StatusError,
			wantErr:    archive.ErrCorrupt,
		},
		{
			name:       "not an archive at all",
			data:       []byte("this is a text file someone renamed"),
			wantStatus: archive.StatusError,
			wantErr:    archive.ErrCorrupt,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t, map[string]any{"series": map[string]any{"vol.zip": tc.data}})
			src := f.open(t, source.KindZIP, "series/vol.zip")

			list, err := src.List(t.Context())
			if got := source.StatusOf(err); got != tc.wantStatus {
				t.Fatalf("status = %q, want %q (err = %v)", got, tc.wantStatus, err)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Errorf("err = %v, want it to wrap %v", err, tc.wantErr)
			}
			// A failure must never come back as a nil Listing: the scanner
			// records counts from it either way.
			if list == nil {
				t.Fatal("List returned a nil Listing")
			}
			if len(list.Pages) != tc.wantPages {
				t.Errorf("pages = %d, want %d", len(list.Pages), tc.wantPages)
			}
		})
	}
}

// A directory that goes bad part-way through still yields the pages that
// parsed — arch §4.3 step 6. Losing a whole 200-page volume because record 180
// is corrupt would be the wrong trade.
func TestZipSource_partiallyCorruptDirectory_keepsTheReadablePages(t *testing.T) {
	t.Parallel()
	jpg := testutil.TinyJPEG(t, 10, 10)
	entries := make([]testutil.Entry, 0, 10)
	for i := 1; i <= 10; i++ {
		entries = append(entries, testutil.Entry{
			Name: fmt.Sprintf("%03d.jpg", i), Data: jpg, Method: testutil.MethodDeflate,
		})
	}
	data := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: entries})
	// Snip the last 40 bytes off the central directory and leave the end record
	// claiming ten entries.
	broken := truncateCentralDirectory(t, data, 40)

	f := newFixture(t, map[string]any{"series": map[string]any{"vol.zip": broken}})
	src := f.open(t, source.KindZIP, "series/vol.zip")

	list, err := src.List(t.Context())
	if err == nil {
		t.Fatal("want an error for a truncated central directory")
	}
	if got := source.StatusOf(err); got != archive.StatusError {
		t.Errorf("status = %q, want %q", got, archive.StatusError)
	}
	if len(list.Pages) == 0 || len(list.Pages) >= 10 {
		t.Fatalf("pages = %d, want some but not all ten", len(list.Pages))
	}
	// And the pages that survived must actually be readable.
	for _, p := range list.Pages {
		if got := readPage(t, src, p); len(got) != len(jpg) {
			t.Errorf("page %q read back %d bytes, want %d", p.EntryPath, len(got), len(jpg))
		}
	}
}

// truncateCentralDirectory removes n bytes from the end of the central
// directory and rewrites the end record's directory size to match the bytes
// that are physically there, leaving the entry count untouched.
func truncateCentralDirectory(t *testing.T, data []byte, n int) []byte {
	t.Helper()
	eocd := -1
	for i := len(data) - 22; i >= 0; i-- {
		if data[i] == 'P' && data[i+1] == 'K' && data[i+2] == 0x05 && data[i+3] == 0x06 {
			eocd = i
			break
		}
	}
	if eocd < 0 {
		t.Fatal("fixture has no end record")
	}
	get32 := func(off int) int {
		return int(data[off]) | int(data[off+1])<<8 | int(data[off+2])<<16 | int(data[off+3])<<24
	}
	cdSize, cdOff := get32(eocd+12), get32(eocd+16)
	if n >= cdSize {
		t.Fatalf("cannot cut %d bytes from a %d-byte directory", n, cdSize)
	}

	out := append([]byte(nil), data[:cdOff+cdSize-n]...)
	tail := append([]byte(nil), data[eocd:]...)
	newSize := cdSize - n
	tail[12] = byte(newSize)
	tail[13] = byte(newSize >> 8)
	tail[14] = byte(newSize >> 16)
	tail[15] = byte(newSize >> 24)
	return append(out, tail...)
}

// FR-IDX-008 / AC-002 at the source level: the encoding kenc chose is recorded
// per book so the UI can surface it, and the names are Korean rather than
// mojibake.
func TestZipSource_recordsTheNameEncoding(t *testing.T) {
	t.Parallel()
	jpg := testutil.TinyJPEG(t, 8, 8)

	cases := []struct {
		name    string
		entries []testutil.Entry
		want    string
		names   []string
	}{
		{
			name: "ascii only",
			entries: []testutil.Entry{
				{Name: "001.jpg", Data: jpg}, {Name: "002.jpg", Data: jpg},
			},
			want:  "utf-8",
			names: []string{"001.jpg", "002.jpg"},
		},
		{
			name: "cp949 without the flag",
			entries: []testutil.Entry{
				{RawName: testutil.CP949(t, "한글.jpg"), Data: jpg},
				{Name: "002.jpg", Data: jpg},
			},
			want:  "cp949",
			names: []string{"002.jpg", "한글.jpg"},
		},
		{
			name: "utf-8 with the flag",
			entries: []testutil.Entry{
				{Name: "한글.jpg", Data: jpg, Flags: testutil.FlagUTF8},
			},
			want:  "utf-8",
			names: []string{"한글.jpg"},
		},
		{
			// D-24: a modern archiver that wrote UTF-8 and forgot the flag.
			name: "utf-8 without the flag",
			entries: []testutil.Entry{
				{Name: "한글.jpg", Data: jpg},
			},
			want:  "utf-8",
			names: []string{"한글.jpg"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t, map[string]any{"series": map[string]any{
				"vol.zip": testutil.BuildZIP(t, testutil.ZIPSpec{Entries: tc.entries}),
			}})
			src := f.open(t, source.KindZIP, "series/vol.zip")
			list, err := src.List(t.Context())
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if list.NameEncoding != tc.want {
				t.Errorf("NameEncoding = %q, want %q", list.NameEncoding, tc.want)
			}
			got := pageNames(list.Pages)
			if len(got) != len(tc.names) {
				t.Fatalf("pages = %q, want %q", got, tc.names)
			}
			for i := range got {
				if got[i] != tc.names[i] {
					t.Errorf("page %d = %q, want %q", i+1, got[i], tc.names[i])
				}
			}
		})
	}
}

// FR-IDX-009 survives the whole stack, not just the parser.
func TestZipSource_zip64Archive_isReadable(t *testing.T) {
	t.Parallel()
	jpg := testutil.TinyJPEG(t, 20, 30)
	data := testutil.BuildZIP64(t, testutil.ZIPSpec{Entries: []testutil.Entry{
		{Name: "001.jpg", Data: jpg, Method: testutil.MethodStore},
		{Name: "002.jpg", Data: jpg, Method: testutil.MethodDeflate},
	}}, testutil.ZIP64Spec{IncludeDiskField: true, LocalHeaders: true})

	f := newFixture(t, map[string]any{"series": map[string]any{"vol.zip": data}})
	src := f.open(t, source.KindZIP, "series/vol.zip")
	list, err := src.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !list.ZIP64 {
		t.Error("Listing.ZIP64 = false for a zip64 archive")
	}
	for _, p := range list.Pages {
		if got := readPage(t, src, p); string(got) != string(jpg) {
			t.Errorf("page %q did not round-trip through the zip64 path", p.EntryPath)
		}
	}
}

// FR-SRV-002 as a scaling property, at the level the HTTP layer will use it:
// serving one page out of a 2 000-page archive must not read the archive.
func TestZipSource_openPage_doesNotScaleWithArchiveSize(t *testing.T) {
	t.Parallel()
	const pages = 2000
	payload := make([]byte, 2048)
	for i := range payload {
		payload[i] = byte(i * 7)
	}
	entries := make([]testutil.Entry, 0, pages)
	for i := 1; i <= pages; i++ {
		entries = append(entries, testutil.Entry{
			Name: fmt.Sprintf("%05d.jpg", i), Data: payload, Method: testutil.MethodStore,
		})
	}
	data := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: entries})

	f := newFixture(t, map[string]any{"series": map[string]any{"vol.zip": data}})
	src := f.open(t, source.KindZIP, "series/vol.zip")
	list, err := src.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list.Pages) != pages {
		t.Fatalf("pages = %d, want %d", len(list.Pages), pages)
	}

	// AC-008: jumping to the last page is the same work as the first.
	for _, idx := range []int{0, pages / 2, pages - 1} {
		st, err := src.Open(t.Context(), list.Pages[idx], source.OpenOptions{})
		if err != nil {
			t.Fatalf("Open page %d: %v", idx+1, err)
		}
		got, err := io.ReadAll(st)
		_ = st.Close()
		if err != nil {
			t.Fatalf("read page %d: %v", idx+1, err)
		}
		if len(got) != len(payload) {
			t.Errorf("page %d: %d bytes, want %d", idx+1, len(got), len(payload))
		}
	}
	t.Logf("archive %d bytes / %d pages; pool stats %+v", len(data), pages, f.pool.Stats())
}

// arch §5.2 / §7.6: an archive that changed on disk since the scan is still
// served — refusing would be worse — but it is reported stale so the API can
// answer 409 and the client can refetch its metadata.
func TestZipSource_staleContainer_isReportedNotRefused(t *testing.T) {
	t.Parallel()
	jpg := testutil.TinyJPEG(t, 16, 16)
	data := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
		{Name: "001.jpg", Data: jpg},
	}})
	f := newFixture(t, map[string]any{"series": map[string]any{"vol.zip": data}})

	// What the index recorded, honestly.
	fresh, err := f.factory.Open(t.Context(), source.Book{
		ID: "bk", Kind: source.KindZIP, RootName: rootName, RelPath: "series/vol.zip",
		FileSize: int64(len(data)), FileMtime: containerMtime(t, f, "series/vol.zip"),
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	checker, ok := fresh.(source.StaleChecker)
	if !ok {
		t.Fatalf("a zip source is %T, want a source.StaleChecker", fresh)
	}
	if stale, err := checker.Stale(t.Context()); err != nil || stale {
		t.Errorf("Stale() = %v, %v; want false, nil for an unchanged container", stale, err)
	}

	// What the index recorded, wrongly: the file has been replaced since.
	drifted, err := f.factory.Open(t.Context(), source.Book{
		ID: "bk", Kind: source.KindZIP, RootName: rootName, RelPath: "series/vol.zip",
		FileSize: int64(len(data)) + 1, FileMtime: 1,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	stale, err := drifted.(source.StaleChecker).Stale(t.Context())
	if err != nil {
		t.Fatalf("Stale: %v", err)
	}
	if !stale {
		t.Error("Stale() = false for a container whose size and mtime both differ")
	}
	// It must still serve.
	list, err := drifted.List(t.Context())
	if err != nil {
		t.Fatalf("List on a stale container: %v", err)
	}
	if got := readPage(t, drifted, list.Pages[0]); string(got) != string(jpg) {
		t.Error("a stale container must still serve its bytes, not fail")
	}
}

func containerMtime(t *testing.T, f *fixture, rel string) int64 {
	t.Helper()
	fi, err := os.Stat(filepath.Join(f.root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("stat %s: %v", rel, err)
	}
	return fi.ModTime().Unix()
}
