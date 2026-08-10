package scanner

import (
	"os"
	"path/filepath"
	"testing"

	"shelf/internal/config"
	"shelf/internal/testutil"
)

// FR-IDX-006's name-only rules as they apply to a filesystem child during
// classification. The extension and 0-byte rules deliberately do NOT fire here:
// a 0-byte `.zip` is a book with status='error' (one of the nine real failures),
// not an entry to drop.
func TestIgnoredChild_appliesTheNameRulesOfFrIdx006(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		ignore bool
		reason string
	}{
		{name: "01권.zip", ignore: false},
		{name: "군계(軍鷄) 01권.zip", ignore: false},
		{name: "__MACOSX", ignore: true, reason: reasonResourceFork},
		{name: "._01권.zip", ignore: true, reason: reasonResourceFork},
		{name: ".DS_Store", ignore: true, reason: reasonSystemFile},
		{name: ".ds_store", ignore: true, reason: reasonSystemFile},
		{name: "Thumbs.db", ignore: true, reason: reasonSystemFile},
		{name: "thumbs.DB", ignore: true, reason: reasonSystemFile},
		{name: "desktop.ini", ignore: true, reason: reasonSystemFile},
		{name: "Desktop.ini", ignore: true, reason: reasonSystemFile},
		{name: ".hidden", ignore: true, reason: reasonHidden},
		{name: ".git", ignore: true, reason: reasonHidden},
		// Not a name rule: size and extension are decided later, per role.
		{name: "빈파일.zip", ignore: false},
		{name: "메모.txt", ignore: false},
	}
	for _, tc := range cases {
		got, reason := ignoredChild(tc.name)
		if got != tc.ignore {
			t.Errorf("ignoredChild(%q) = %v, want %v", tc.name, got, tc.ignore)
		}
		if tc.ignore && reason != tc.reason {
			t.Errorf("ignoredChild(%q) reason = %q, want %q", tc.name, reason, tc.reason)
		}
	}
}

// matchGlob is path.Match plus `**`. The `**` cases are the two patterns
// arch §3.2 prints as the documented examples of `exclude_globs`; plain
// path.Match cannot express either.
func TestMatchGlob_extendsPathMatchWithDoubleStar(t *testing.T) {
	t.Parallel()
	cases := []struct {
		pattern, name string
		want          bool
	}{
		{pattern: "*.zip", name: "01권.zip", want: true},
		{pattern: "*.zip", name: "시리즈/01권.zip", want: false}, // `*` never crosses `/`
		{pattern: "시리즈/*.zip", name: "시리즈/01권.zip", want: true},
		{pattern: "**/*.part", name: "시리즈/01권.zip.part", want: true},
		{pattern: "**/*.part", name: "a/b/c/x.part", want: true},
		// `**` matches ZERO or more segments, as globstar does everywhere else:
		// a user who writes `**/*.part` means every `.part` file, including the
		// ones at the top of the root.
		{pattern: "**/*.part", name: "x.part", want: true},
		{pattern: "**", name: "any/depth/at/all", want: true},
		{pattern: "**/@eaDir/**", name: "시리즈/@eaDir", want: true},
		{pattern: "**/@eaDir/**", name: "시리즈/@eaDir/thumb.jpg", want: true},
		{pattern: "**/@eaDir/**", name: "시리즈/01권.zip", want: false},
		{pattern: "a/**/z.jpg", name: "a/z.jpg", want: true},
		{pattern: "a/**/z.jpg", name: "a/b/c/z.jpg", want: true},
		{pattern: "a/**/z.jpg", name: "b/z.jpg", want: false},
		{pattern: "[[]만화]*", name: "[만화] 군계 1~25", want: true},
		{pattern: "[", name: "anything", want: false}, // a broken pattern matches nothing
	}
	for _, tc := range cases {
		if got := matchGlob(tc.pattern, tc.name); got != tc.want {
			t.Errorf("matchGlob(%q, %q) = %v, want %v", tc.pattern, tc.name, got, tc.want)
		}
	}
}

// Amendment A-3 / ruling E-6: an empty include list means "scan everything";
// a non-empty one is an allowlist over base names.
func TestGlobSet_matchBase_emptyMeansEverything(t *testing.T) {
	t.Parallel()
	if !globSet(nil).matchBase("아무거나") {
		t.Error("an empty include list must match everything")
	}
	set := globSet{"*군계*", "[만화] 바퀴.zip"}
	for name, want := range map[string]bool{
		"[만화] 군계 1~25": true,
		"군계":           true,
		"[만화] 바퀴.zip":  false, // `[만화]` is a character class: matches one of 만 화 [ ]
		"다른 시리즈":       false,
	} {
		if got := set.matchBase(name); got != want {
			t.Errorf("matchBase(%q) = %v, want %v", name, got, want)
		}
	}
}

// FR-IDX-006, end to end and applied to the *decoded* entry name, for both a
// ZIP book and a directory book — the whole point of routing both through
// internal/source is that the rules cannot diverge.
func TestScan_frIdx006_dropsEveryExcludedEntryFromBothBookKinds(t *testing.T) {
	t.Parallel()
	archive := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
		{Name: "vol/", Dir: true, Flags: testutil.FlagUTF8},
		{Name: "vol/001.jpg", Data: jpeg(t), Flags: testutil.FlagUTF8},
		{Name: "__MACOSX/vol/._001.jpg", Data: []byte("fork"), Flags: testutil.FlagUTF8},
		{Name: "vol/._002.jpg", Data: []byte("fork"), Flags: testutil.FlagUTF8},
		// A dot-name that is a page is a page (see source.Excluded): the
		// collection's `엽기인 Girl 스나코 26권.zip` is 80 of these and nothing
		// else. `.hidden.txt` on the next line is the half of the rule that
		// still drops.
		{Name: "vol/.hidden.jpg", Data: jpeg(t), Flags: testutil.FlagUTF8},
		{Name: "vol/.hidden.txt", Data: []byte("메모"), Flags: testutil.FlagUTF8},
		{Name: "vol/.cache/003.jpg", Data: jpeg(t), Flags: testutil.FlagUTF8},
		{Name: "vol/Thumbs.db", Data: []byte("junk"), Flags: testutil.FlagUTF8},
		{Name: "vol/.DS_Store", Data: []byte("junk"), Flags: testutil.FlagUTF8},
		{Name: "vol/desktop.ini", Data: []byte("junk"), Flags: testutil.FlagUTF8},
		{Name: "vol/zero.jpg", Data: nil, Flags: testutil.FlagUTF8},
		{Name: "vol/readme.txt", Data: []byte("메모"), Flags: testutil.FlagUTF8},
		{Name: "vol/002.jpg", Data: jpeg(t), Flags: testutil.FlagUTF8},
	}})

	h := newHarness(t, map[string]any{
		"시리즈": map[string]any{
			"01권.zip": archive,
			"02권": map[string]any{
				"001.jpg":     jpeg(t),
				"._002.jpg":   []byte("fork"),
				".hidden.jpg": jpeg(t),
				".hidden.txt": "메모",
				".cache":      map[string]any{"003.jpg": jpeg(t)},
				"Thumbs.db":   []byte("junk"),
				".DS_Store":   []byte("junk"),
				"desktop.ini": []byte("junk"),
				"zero.jpg":    []byte{},
				"readme.txt":  "메모",
				"002.jpg":     jpeg(t),
			},
		},
	})
	h.run(Request{})

	books := h.books("manga", "시리즈")
	if len(books) != 2 {
		t.Fatalf("indexed %d books %v, want 2", len(books), bookNames(books))
	}
	// `.hidden.jpg` is listed and `.hidden.txt` / `.cache/003.jpg` are not:
	// a leading dot no longer decides on its own, but a hidden *directory*
	// still hides what is under it.
	// Natural sort (FR-IDX-007) puts the digits first and the dot-name last;
	// the collection's real book is 80 dot-names and nothing else, so they
	// order among themselves.
	want := []string{"001.jpg", "002.jpg", ".hidden.jpg"}
	if got := pageNames(h.pages(books[0].ID)); !equalStrings(got, want) {
		t.Errorf("zip pages = %v, want %v", got, want)
	}
	if got := pageNames(h.pages(books[1].ID)); !equalStrings(got, want) {
		t.Errorf("dir pages = %v, want %v", got, want)
	}
}

// A `__MACOSX` directory beside real books is not a volume, and a hidden
// directory hides everything under it.
func TestScan_junkDirectories_neverBecomeBooks(t *testing.T) {
	t.Parallel()
	h := newHarness(t, map[string]any{
		"시리즈": map[string]any{
			"01권.zip": jpegZIP(t, "001.jpg"),
			"__MACOSX": map[string]any{
				"._01권.zip": []byte("fork"),
				"001.jpg":   jpeg(t),
			},
			".thumbnails": imageDir(t, "001.jpg", "002.jpg"),
		},
	})
	h.run(Request{})

	books := h.books("manga", "시리즈")
	if got := bookNames(books); !equalStrings(got, []string{"01권.zip"}) {
		t.Errorf("books = %v, want only [01권.zip]", got)
	}
}

// Amendment A-3 / ruling E-6 — the mechanism the E2E plan depends on: with a
// non-empty include list, only matching direct children of a root become series.
// Everything else is not scanned at all, which is what makes ten named series
// inside a 414 GB collection testable without copying a byte.
func TestScan_includeGlobs_restrictSeriesToTheAllowlist(t *testing.T) {
	t.Parallel()
	layout := map[string]any{
		"[만화] 군계 1~25":           map[string]any{"01권.zip": jpegZIP(t, "001.jpg")},
		"[만화] 강철의 연금술사 1~27권 완결": map[string]any{"01권.zip": jpegZIP(t, "001.jpg")},
		"[만화] 바퀴.zip":            jpegZIP(t, "001.jpg"),
		"관계없는 시리즈":               map[string]any{"01권.zip": jpegZIP(t, "001.jpg")},
	}

	// Empty list: everything, which is the default and must stay the default.
	all := newHarness(t, layout)
	all.run(Request{})
	if got := len(all.series()); got != 4 {
		t.Fatalf("with no include_globs the scan indexed %d series, want 4", got)
	}

	h := newHarness(t, layout, func(s *config.Scan) {
		s.IncludeGlobs = []string{"*군계*", "*바퀴*"}
	})
	h.run(Request{})

	want := []string{"[만화] 군계 1~25", "[만화] 바퀴.zip"}
	got := seriesRels(h.series())
	if len(got) != len(want) {
		t.Fatalf("include_globs indexed %v, want %v", got, want)
	}
	for _, w := range want {
		found := false
		for _, g := range got {
			if g == w {
				found = true
			}
		}
		if !found {
			t.Errorf("%q was excluded by include_globs but should match", w)
		}
	}
	// Nothing outside the allowlist was even opened.
	for _, rel := range h.lister.listedPaths() {
		if rel != "[만화] 군계 1~25/01권.zip" && rel != "[만화] 바퀴.zip" {
			t.Errorf("include_globs did not stop the scan reading %q", rel)
		}
	}
}

// `scan.exclude_globs` is matched against the root-relative slash path and
// applies at every level: a series, a book, and a page.
func TestScan_excludeGlobs_dropSeriesBooksAndPages(t *testing.T) {
	t.Parallel()
	mixedFormats := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
		{Name: "001.jpg", Data: jpeg(t), Flags: testutil.FlagUTF8},
		{Name: "002.gif", Data: testutil.TinyGIF(t, 8, 12), Flags: testutil.FlagUTF8},
		{Name: "003.jpg", Data: jpeg(t), Flags: testutil.FlagUTF8},
	}})
	h := newHarness(t, map[string]any{
		"보이는 시리즈": map[string]any{
			"01권.zip":      mixedFormats,
			"01권.zip.part": jpegZIP(t, "001.jpg"),
			"@eaDir":       imageDir(t, "thumb.jpg"),
		},
		"숨김 시리즈": map[string]any{"01권.zip": jpegZIP(t, "001.jpg")},
	}, func(s *config.Scan) {
		s.ExcludeGlobs = []string{"숨김 시리즈", "**/*.part", "**/@eaDir/**", "**/*.gif"}
	})
	h.run(Request{})

	if got := seriesRels(h.series()); !equalStrings(got, []string{"보이는 시리즈"}) {
		t.Fatalf("series = %v, want only [보이는 시리즈]", got)
	}
	books := h.books("manga", "보이는 시리즈")
	if got := bookNames(books); !equalStrings(got, []string{"01권.zip"}) {
		t.Fatalf("books = %v, want only [01권.zip] — .part and @eaDir are excluded", got)
	}
	if got := pageNames(h.pages(books[0].ID)); !equalStrings(got, []string{"001.jpg", "003.jpg"}) {
		t.Errorf("pages = %v, want [001.jpg 003.jpg] — the .gif is excluded by glob", got)
	}
}

// `scan.follow_symlinks` defaults false, and os.Root refuses an escaping link
// whatever the setting (decision D-48 — which is why the E2E subset cannot be a
// symlink farm).
//
// The fixture makes real links, because the only other way to test a symlink
// branch is to assert a hand-built constant against itself. os.Symlink touches
// no media volume and is not one of the eleven mutation primitives the
// FR-CFG-005 guard forbids this package — the check-readonly grep of arch §11
// covers the create/remove/rename/mkdir/chtimes/chmod/truncate/write family and
// stays clean with these three calls present (`make check-readonly` verified).
// The links land in the test's own t.TempDir fixture, the same directory
// testutil.BuildTree wrote two lines earlier.
func TestScan_symlinks_areSkippedWhenFollowSymlinksIsFalse(t *testing.T) {
	t.Parallel()
	root := testutil.BuildTree(t, map[string]any{
		"실제 시리즈": map[string]any{"01권.zip": jpegZIP(t, "001.jpg")},
	})
	outside := testutil.BuildTree(t, map[string]any{
		"바깥 시리즈": map[string]any{"01권.zip": jpegZIP(t, "001.jpg")},
	})
	// Relative targets on purpose: os.Root resolves the link itself and reads
	// *any* absolute target as an escape, even one pointing back inside the
	// root. `탈출 링크` is the escape that must never resolve either way.
	for at, target := range map[string]string{
		filepath.Join(root, "링크 시리즈"):            "실제 시리즈",
		filepath.Join(root, "실제 시리즈", "02권.zip"): "01권.zip",
		filepath.Join(root, "탈출 링크"):             outside,
	} {
		if err := os.Symlink(target, at); err != nil {
			t.Skipf("this platform cannot create a symlink: %v", err)
		}
	}

	h := newHarnessAt(t, map[string]string{"manga": root})
	osRoot, ok := h.rootSet.Root("manga")
	if !ok {
		t.Fatal("root is unreachable")
	}

	// follow_symlinks: false. A link is still *recorded* — it has to appear in
	// the FR-IDX-003 fingerprint, because a link appearing or vanishing is a
	// change to the directory — but it is never a candidate.
	unfollowed := childrenByName(t, osRoot, "", false)
	for _, name := range []string{"링크 시리즈", "탈출 링크"} {
		c, seen := unfollowed[name]
		switch {
		case !seen:
			t.Errorf("%q was dropped from the listing entirely; a link must still be fingerprinted", name)
		case !c.symlink:
			t.Errorf("%q was not recognised as a symlink", name)
		case c.skip != reasonSymlink:
			t.Errorf("%q skip = %q, want %q", name, c.skip, reasonSymlink)
		}
	}
	if c := unfollowed["실제 시리즈"]; c.skip != "" || !c.isDir {
		t.Errorf("the real directory was skipped (%q) or misread as a file", c.skip)
	}

	h.run(Request{})
	if got := seriesRels(h.series()); !equalStrings(got, []string{"실제 시리즈"}) {
		t.Fatalf("series = %v, want only [실제 시리즈] — neither link may become a series", got)
	}
	if got := bookRels(h.books("manga", "실제 시리즈")); !equalStrings(got, []string{"실제 시리즈/01권.zip"}) {
		t.Errorf("books = %v, want only [실제 시리즈/01권.zip] — the linked volume is a symlink too", got)
	}

	// follow_symlinks: true. The in-root links resolve and are scanned; the
	// escaping one is refused by os.Root at the openat(2) level and simply
	// disappears (D-48), which is the guarantee that survives any config.
	followed := childrenByName(t, osRoot, "", true)
	if c, seen := followed["링크 시리즈"]; !seen || c.skip != "" || !c.isDir {
		t.Errorf("링크 시리즈 with follow_symlinks: true = %+v, want a followed directory", c)
	}
	if c, seen := followed["탈출 링크"]; seen {
		t.Errorf("an escaping link resolved to %+v; os.Root must refuse it whatever follow_symlinks says", c)
	}

	f := newHarnessAt(t, map[string]string{"manga": root}, func(s *config.Scan) {
		s.FollowSymlinks = true
	})
	f.run(Request{})
	if got := seriesRels(f.series()); !equalStrings(got, []string{"링크 시리즈", "실제 시리즈"}) {
		t.Fatalf("series = %v, want [링크 시리즈 실제 시리즈] — the in-root link is followed, the escape is not", got)
	}
	if got := bookRels(f.books("manga", "실제 시리즈")); !equalStrings(got,
		[]string{"실제 시리즈/01권.zip", "실제 시리즈/02권.zip"}) {
		t.Errorf("books = %v, want both volumes — the linked one is followed now", got)
	}
}

// childrenByName is readDir keyed by base name, so a test can ask about one
// child without depending on the natural order of the rest.
func childrenByName(t *testing.T, root *os.Root, dirRel string, follow bool) map[string]childInfo {
	t.Helper()
	children, err := readDir(root, dirRel, follow)
	if err != nil {
		t.Fatalf("reading %q (follow=%v): %v", dirRel, follow, err)
	}
	out := make(map[string]childInfo, len(children))
	for _, c := range children {
		out[c.name] = c
	}
	return out
}
