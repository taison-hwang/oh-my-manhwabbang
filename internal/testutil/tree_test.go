package testutil_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"shelf/internal/testutil"
)

func TestBuildTree_realCollectionShapes_materialiseOnDisk(t *testing.T) {
	t.Parallel()

	page := testutil.TinyJPEG(t, 16, 24)
	vol := testutil.BuildZIP(t, testutil.ZIPSpec{
		Entries: []testutil.Entry{{Name: "001.jpg", Data: page, Method: testutil.MethodDeflate}},
	})
	mtime := time.Date(2014, time.July, 2, 11, 30, 0, 0, time.UTC)

	// prd §2.2 rows 1, 2, 3 and 6 in one tree, using the real names of the
	// curated E2E subset (impl-plan §6.3).
	root := testutil.BuildTree(t, map[string]any{
		// row 1: folder of ZIPs
		"[만화] Clover 클로버 (총4권)": map[string]any{
			"Clover 클로버 1.zip": vol,
			"Clover 클로버 2.zip": vol,
		},
		// row 2: folder of image sub-folders
		"[만화] 상처를 쫓는자 1-11 (완)": map[string]any{
			"01권": map[string]any{"001.jpg": page, "002.jpg": page},
		},
		// row 3: loose images directly in the series folder
		"[만화] 자살도114-122": map[string]any{
			"1.jpg": page, "10.jpg": page, "100.jpg": page,
		},
		// row 4: a single top-level ZIP is its own series and its own book
		"[만화] 바퀴.zip": vol,
		// row 6 as it actually occurs (D-5): N archives + exactly one cover
		"[만화] 강철의 연금술사 1~27권 완결": map[string]any{
			"01권.zip":               testutil.File{Data: vol, ModTime: mtime},
			"강철의 연금술사 00 Cover.jpg": page,
		},
		// D-7: a series holding no media at all
		"[소설] 텍스트만": map[string]any{"info.txt": "not media"},
		// an explicitly empty directory
		"empty": nil,
	})

	mustBeFile := []string{
		"[만화] Clover 클로버 (총4권)/Clover 클로버 1.zip",
		"[만화] 상처를 쫓는자 1-11 (완)/01권/001.jpg",
		"[만화] 자살도114-122/100.jpg",
		"[만화] 바퀴.zip",
		"[만화] 강철의 연금술사 1~27권 완결/강철의 연금술사 00 Cover.jpg",
		"[소설] 텍스트만/info.txt",
	}
	for _, rel := range mustBeFile {
		fi, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Errorf("stat %q: %v", rel, err)
			continue
		}
		if fi.IsDir() {
			t.Errorf("%q is a directory, want a file", rel)
		}
		if fi.Size() == 0 {
			t.Errorf("%q is 0 bytes", rel)
		}
	}

	for _, rel := range []string{"empty", "[만화] 상처를 쫓는자 1-11 (완)/01권"} {
		fi, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Errorf("stat %q: %v", rel, err)
			continue
		}
		if !fi.IsDir() {
			t.Errorf("%q is a file, want a directory", rel)
		}
	}
}

func TestBuildTree_explicitModTime_isPinned(t *testing.T) {
	t.Parallel()

	// content_version, the incremental scan and FR-THM-006 all key off
	// (size, mtime), so a fixture that cannot pin an mtime cannot test them.
	want := time.Date(2012, time.November, 5, 8, 15, 30, 0, time.UTC)
	root := testutil.BuildTree(t, map[string]any{
		"series": map[string]any{
			"001.jpg": testutil.File{Data: testutil.TinyJPEG(t, 8, 8), ModTime: want},
		},
	})

	path := filepath.Join(root, "series", "001.jpg")
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := fi.ModTime().UTC(); !got.Equal(want) {
		t.Errorf("mtime = %s, want %s", got, want)
	}

	testutil.Touch(t, path, 48*time.Hour)
	fi2, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat after Touch: %v", err)
	}
	if got, want2 := fi2.ModTime().UTC(), want.Add(48*time.Hour); !got.Equal(want2) {
		t.Errorf("mtime after Touch = %s, want %s", got, want2)
	}
}

func TestBuildTree_slashSeparatedKey_createsIntermediateDirectories(t *testing.T) {
	t.Parallel()

	root := testutil.BuildTree(t, map[string]any{
		"a/b/c/001.jpg": testutil.TinyJPEG(t, 8, 8),
	})
	if _, err := os.Stat(filepath.Join(root, "a", "b", "c", "001.jpg")); err != nil {
		t.Fatalf("stat: %v", err)
	}
}
