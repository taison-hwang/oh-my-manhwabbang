package config

import "testing"

// TestIsInside_table pins the contract on IsInside's doc comment: path "is dir
// itself or lives under it", both already absolute and cleaned. A-11 applies
// it to overlap and config_inside_root (arch §7.4) and FR-CFG-005, so a sibling
// that merely shares a textual prefix — `/media/manga2` next to `/media/manga`
// — must not count as inside: that would refuse a legitimate root and, the
// other way round, is exactly the plain strings.HasPrefix mistake.
//
// Trailing slashes and `..` are deliberately absent: the doc makes cleaned
// input the caller's job, so they are outside the contract.
func TestIsInside_table(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		path, dir string
		want      bool
	}{
		{"equal paths", "/media/manga", "/media/manga", true},
		{"direct child", "/media/manga/series", "/media/manga", true},
		{"deep descendant", "/media/manga/a/b/c.zip", "/media/manga", true},
		{"sibling sharing a prefix", "/media/manga2", "/media/manga", false},
		{"sibling sharing a prefix and a separator-like suffix", "/media/manga-old/x", "/media/manga", false},
		{"descendant of a prefix sibling", "/media/manga2/series", "/media/manga", false},
		{"parent is not inside its child", "/media", "/media/manga", false},
		{"unrelated tree", "/srv/data", "/media/manga", false},
		{"everything is inside the filesystem root", "/media/manga", "/", true},
		{"filesystem root is not inside a directory", "/", "/media", false},
		{"child whose name starts with two dots", "/media/manga/..hidden", "/media/manga", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := IsInside(tc.path, tc.dir); got != tc.want {
				t.Errorf("IsInside(%q, %q) = %v, want %v", tc.path, tc.dir, got, tc.want)
			}
		})
	}
}
