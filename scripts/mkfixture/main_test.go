package main

import (
	"archive/zip"
	"image"
	"image/jpeg"
	"io/fs"
	"math"
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

// 표지 테스트 is ruling E-69's sample (U5). §2.2 picks the half of a landscape
// page 1 that the effective reading direction opens on, and the browser tier
// tells the halves apart by comparing the card against each half of the page —
// so the halves have to differ, and the difference has to be the one a
// centre crop (the bug E-69 §1 is about) would also get wrong. 03권 is the
// designation book: a portrait page 1, which §2.2 shows whole, and a page 2
// whose quadrants differ, so a designated region is told from the whole page.
func TestCoverSamples_haveTheShapesE69Asserts(t *testing.T) {
	root := t.TempDir()
	if err := build(root); err != nil {
		t.Fatalf("build: %v", err)
	}
	for _, vol := range []string{"표지 테스트 01권.zip", "표지 테스트 02권.zip"} {
		pages := zipPages(t, filepath.Join(root, "표지 테스트", vol))
		p1 := pages[0]
		b := p1.Bounds()
		if b.Dx() <= b.Dy() {
			t.Fatalf("%s page 1 is %dx%d; E-69 §2.2 only halves a page with w > h", vol, b.Dx(), b.Dy())
		}
		left := meanRGB(p1, image.Rect(0, 0, b.Dx()/2, b.Dy()))
		right := meanRGB(p1, image.Rect(b.Dx()/2, 0, b.Dx(), b.Dy()))
		centre := meanRGB(p1, image.Rect(b.Dx()/4, 0, b.Dx()*3/4, b.Dy()))
		if !(left[0] > 150 && left[2] < 90) {
			t.Errorf("%s page 1 left half is %v, want red", vol, left)
		}
		if !(right[2] > 150 && right[0] < 90) {
			t.Errorf("%s page 1 right half is %v, want blue", vol, right)
		}
		// A centre crop straddles both halves, so it must look like neither.
		redLike := centre[0] > 150 && centre[2] < 90
		blueLike := centre[2] > 150 && centre[0] < 90
		if redLike || blueLike {
			t.Errorf("%s page 1 centre is %v: a centre crop would pass for a half", vol, centre)
		}
		for i, p := range pages[1:] {
			if pb := p.Bounds(); pb.Dx() > pb.Dy() {
				t.Errorf("%s page %d is landscape; only page 1 is the sample", vol, i+2)
			}
		}
	}

	pages := zipPages(t, filepath.Join(root, "표지 테스트", "표지 테스트 03권.zip"))
	if len(pages) < 2 {
		t.Fatalf("03권 has %d pages; the designation needs a page 2", len(pages))
	}
	if b := pages[0].Bounds(); b.Dx() > b.Dy() {
		t.Errorf("03권 page 1 is %dx%d; it is the portrait case, shown whole", b.Dx(), b.Dy())
	} else {
		// Shown whole, but its halves differ: the browser tier tells "whole"
		// from "a half" on this page, which a flat page cannot do.
		left := meanRGB(pages[0], image.Rect(0, 0, b.Dx()/2, b.Dy()))
		right := meanRGB(pages[0], image.Rect(b.Dx()/2, 0, b.Dx(), b.Dy()))
		if d := rgbDistance(left, right); d < 120 {
			t.Errorf("03권 page 1 halves are %v and %v, %0.f apart: a halving bug would pass for the whole page", left, right, d)
		}
	}
	p2 := pages[1]
	b := p2.Bounds()
	if b.Dx() > b.Dy() {
		t.Errorf("03권 page 2 is %dx%d; the designation page is portrait", b.Dx(), b.Dy())
	}
	w, h := b.Dx(), b.Dy()
	quads := [][3]float64{
		meanRGB(p2, image.Rect(0, 0, w/2, h/2)),
		meanRGB(p2, image.Rect(w/2, 0, w, h/2)),
		meanRGB(p2, image.Rect(0, h/2, w/2, h)),
		meanRGB(p2, image.Rect(w/2, h/2, w, h)),
	}
	for i := range quads {
		for j := i + 1; j < len(quads); j++ {
			if rgbDistance(quads[i], quads[j]) < 120 {
				t.Errorf("03권 page 2 quadrants %d and %d are %v and %v: too alike to tell a region from the page", i, j, quads[i], quads[j])
			}
		}
	}
	if d := rgbDistance(meanRGB(pages[0], pages[0].Bounds()), meanRGB(p2, b)); d < 60 {
		t.Errorf("03권 pages 1 and 2 average %0.f apart: the designated cover would look like the default", d)
	}
}

func zipPages(t *testing.T, path string) []image.Image {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("opening %s: %v", path, err)
	}
	defer zr.Close()
	var out []image.Image
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("%s: %v", f.Name, err)
		}
		img, err := jpeg.Decode(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatalf("%s: decoding: %v", f.Name, err)
		}
		out = append(out, img)
	}
	if len(out) == 0 {
		t.Fatalf("%s holds no page", path)
	}
	return out
}

func meanRGB(img image.Image, r image.Rectangle) [3]float64 {
	var sum [3]float64
	var n float64
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			cr, cg, cb, _ := img.At(x, y).RGBA()
			sum[0] += float64(cr >> 8)
			sum[1] += float64(cg >> 8)
			sum[2] += float64(cb >> 8)
			n++
		}
	}
	return [3]float64{sum[0] / n, sum[1] / n, sum[2] / n}
}

func rgbDistance(a, b [3]float64) float64 {
	return math.Abs(a[0]-b[0]) + math.Abs(a[1]-b[1]) + math.Abs(a[2]-b[2])
}
