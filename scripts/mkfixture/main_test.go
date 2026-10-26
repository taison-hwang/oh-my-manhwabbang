package main

import (
	"io/fs"
	"path/filepath"
	"testing"

	"shelf/internal/kenc"
)

// The tree has to be byte-reproducible for the pixel baseline (docs/ui-shots/
// README.md: "a fixture built with a fixed mtime and no rand"). Files got
// fixedMtime from the start; the directories kept the time they were built at,
// and a directory's mtime is a series' 수정일 — the list view's full layout
// printed the day of the round, and four shots changed every day for it.
func TestBuild_everyEntryCarriesTheFixedMtime(t *testing.T) {
	root := t.TempDir()
	if err := build(root); err != nil {
		t.Fatalf("build: %v", err)
	}
	var dirs, files int
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == root {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			dirs++
		} else {
			files++
		}
		if !info.ModTime().Equal(fixedMtime) {
			rel, _ := filepath.Rel(root, p)
			t.Errorf("%s: mtime %s, want %s", rel, info.ModTime().UTC(), fixedMtime)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the tree: %v", err)
	}
	if dirs == 0 || files == 0 {
		t.Fatalf("walked %d directories and %d files — the check saw nothing", dirs, files)
	}
}

// 울프가이 is in the fixture to carry the Shift_JIS path end to end (item be,
// ruling E-56: the series reports cp932). The readers only ask
// kenc.ArchiveFallback once some name has failed to read as CP949
// (zipidx/centraldir.go resolveArchiveNames, rar4 likewise), and a short
// Japanese name very often reads as CP949 without a single substitution —
// 第01巻 狼の紋章/0001頁.jpg comes out as 묉01뒱 쁔궻뽨뤞/0001빬.jpg. Names
// like that alone never trigger the decision: the archive is labelled cp949,
// its page names are mojibake, and nothing that looks for U+FFFD notices.
func TestShiftJISPages_areConvictedAsCP932(t *testing.T) {
	pages := shiftJISPages(3)
	raws := make([][]byte, 0, len(pages))
	var unread, misread int
	for _, p := range pages {
		raws = append(raws, p.rawName)
		switch _, enc := kenc.DecodeEntryName(p.rawName, false); enc {
		case kenc.EncUnknown:
			unread++
		case kenc.EncCP949:
			misread++
		}
	}
	// Both branches of the replacement: a name CP949 refuses (the trigger) and
	// a name CP949 reads wrongly (what the trigger has to repair).
	if unread == 0 {
		t.Fatalf("no name fails as CP949, so ArchiveFallback is never asked")
	}
	if misread == 0 {
		t.Errorf("no name reads as CP949, so the wrong readings are never replaced")
	}
	if got := kenc.ArchiveFallback(raws); got != kenc.EncCP932 {
		t.Fatalf("ArchiveFallback = %q, want %q", got, kenc.EncCP932)
	}
	want := map[string]bool{
		"第01巻 狼の紋章/0001 犬神明.jpg": true,
		"第01巻 狼の紋章/0002頁.jpg":    true,
		"第01巻 狼の紋章/0003頁.jpg":    true,
	}
	for _, raw := range raws {
		name, enc := kenc.DecodeEntryNameAs(raw, false, kenc.EncCP932)
		if enc != kenc.EncCP932 || !want[name] {
			t.Errorf("% x decoded as %q (%s)", raw, name, enc)
		}
		delete(want, name)
	}
	for name := range want {
		t.Errorf("never produced %q", name)
	}
}
