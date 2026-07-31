package ids_test

import (
	"crypto/sha256"
	"encoding/base32"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"shelf/internal/ids"
	"shelf/internal/testutil"
)

// The golden pair for the two names arch §3.4 uses in its worked example,
// computed from the hash input that same section spells out as "a compatibility
// surface". If a refactor changes the hash input, the version tag, the domain
// tag, the truncation length or the base32 alphabet, every reading-progress row
// in every existing install is orphaned, and this is the test that says so.
//
// These are NOT the strings printed under §3.4's "*Worked example* (VERIFIED)"
// heading. Those (gzj75n6x7rir6but / ox74tfcrwwnfopch) are what the same inputs
// hash to with the version and domain tags *dropped*, so they contradict the
// construction they are printed beneath. See the package comment for the
// precedence argument that resolves it; TestIDs_hashInput_isTheArchSpecString
// below recomputes these values from the literal spec string rather than
// trusting the constants.
const (
	goldenRootName   = "mangga"
	goldenSeriesName = "[만화] 군계 1~25"
	goldenBookName   = "[만화] 군계 1~25/군계(軍鷄) 01권.zip"
	goldenSeriesID   = "ruzwlotzngls2ua5"
	goldenBookID     = "yvtfrny77ehkt2we"
)

func TestSeriesID_archGoldenVector_matchesVerbatim(t *testing.T) {
	t.Parallel()

	if got := ids.SeriesID(goldenRootName, goldenSeriesName); got != goldenSeriesID {
		t.Errorf("SeriesID(%q, %q) = %q, want %q", goldenRootName, goldenSeriesName, got, goldenSeriesID)
	}
}

func TestBookID_archGoldenVector_matchesVerbatim(t *testing.T) {
	t.Parallel()

	if got := ids.BookID(goldenRootName, goldenBookName); got != goldenBookID {
		t.Errorf("BookID(%q, %q) = %q, want %q", goldenRootName, goldenBookName, got, goldenBookID)
	}
}

// TestIDs_hashInput_isTheArchSpecString pins the compatibility surface itself
// rather than two opaque strings: it rebuilds arch §3.4's byte diagram
//
//	"shelf-id/1" ‖ 0x00 ‖ "series"|"book" ‖ 0x00 ‖ <root name> ‖ 0x00 ‖ <rel>
//
// from literals here in the test — no constant is shared with the package
// except IDVersion, which is itself asserted — takes SHA-256, keeps 10 bytes and
// encodes them with the lowercase RFC 4648 alphabet. A field dropped, reordered,
// renamed or joined with a different separator fails here even if someone
// "fixes" the golden constants to match the new output.
func TestIDs_hashInput_isTheArchSpecString(t *testing.T) {
	t.Parallel()

	if ids.IDVersion != "shelf-id/1" {
		t.Fatalf("IDVersion = %q, want %q — it is also meta.id_version in both databases "+
			"(arch §3.5, §3.6), so it cannot drift", ids.IDVersion, "shelf-id/1")
	}

	spec := func(domain, root, rel string) string {
		sum := sha256.Sum256([]byte("shelf-id/1" + "\x00" + domain + "\x00" + root + "\x00" + rel))
		return base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").
			WithPadding(base32.NoPadding).EncodeToString(sum[:10])
	}

	cases := []struct {
		root, rel string
	}{
		{goldenRootName, goldenSeriesName},
		{goldenRootName, goldenBookName},
		{"manga", ""},
		{"manga", "[만화] 엔젤하트 전32권 완결.zip"},
		{"manga", "a/b/c.zip"},
	}
	for _, c := range cases {
		if got, want := ids.SeriesID(c.root, c.rel), spec("series", c.root, c.rel); got != want {
			t.Errorf("SeriesID(%q, %q) = %q, want %q (arch §3.4 hash input)", c.root, c.rel, got, want)
		}
		if got, want := ids.BookID(c.root, c.rel), spec("book", c.root, c.rel); got != want {
			t.Errorf("BookID(%q, %q) = %q, want %q (arch §3.4 hash input)", c.root, c.rel, got, want)
		}
	}

	// And the two golden constants above really are that construction applied to
	// §3.4's own example inputs, so a reader can verify them by hand.
	if got := spec("series", goldenRootName, goldenSeriesName); got != goldenSeriesID {
		t.Errorf("golden series id is stale: spec says %q, constant says %q", got, goldenSeriesID)
	}
	if got := spec("book", goldenRootName, goldenBookName); got != goldenBookID {
		t.Errorf("golden book id is stale: spec says %q, constant says %q", got, goldenBookID)
	}
}

func TestIDs_shape_isSixteenLowercaseBase32Chars(t *testing.T) {
	t.Parallel()

	// Real names from the collection (data-survey §1/§2) plus the shapes the
	// scanner produces for single-file series and deeply nested books.
	rels := []string{
		"",
		"[만화] Clover 클로버 (총4권)",
		"[만화] 상처를 쫓는자 1-11 (완)/01권",
		"[만화] 엔젤하트 전32권 완결.zip",
		"[만화] 단편 만화/아다치/쇼트 프로그램 (전,후 完)",
		"I'll(아일) 09권.zip",
		"미생 1~9 완결 pdf/미생 9권 (완).pdf",
	}
	for _, rel := range rels {
		for _, id := range []string{ids.SeriesID("manga", rel), ids.BookID("manga", rel)} {
			if len(id) != ids.Length {
				t.Errorf("id %q for rel %q has length %d, want %d", id, rel, len(id), ids.Length)
			}
			if strings.Trim(id, ids.Alphabet) != "" {
				t.Errorf("id %q for rel %q contains characters outside %q", id, rel, ids.Alphabet)
			}
			if !ids.Valid(id) {
				t.Errorf("Valid(%q) = false for a freshly derived id", id)
			}
		}
	}
}

func TestIDs_sameInputs_areDeterministicAcrossCalls(t *testing.T) {
	t.Parallel()

	// AC-006's precondition: a rescan must recompute the same id from the same
	// two inputs, otherwise the LEFT JOIN onto user.db finds nothing.
	for range 100 {
		if got := ids.SeriesID(goldenRootName, goldenSeriesName); got != goldenSeriesID {
			t.Fatalf("SeriesID is not deterministic: got %q, want %q", got, goldenSeriesID)
		}
	}
}

// TestIDs_seriesAndBookOfTheSameRelPath_neverCollide is impl-plan §3 WP-02
// acceptance 1's third clause, arch §10.1's "series vs book domain separation"
// and decisions.md D-14's domain field, stated as the case that actually
// occurs: arch §4.2 counts 291 top-level ZIPs where the series *is* its own
// single book, so SeriesID and BookID are handed the identical (root, rel) pair
// and must still return different ids.
func TestIDs_seriesAndBookOfTheSameRelPath_neverCollide(t *testing.T) {
	t.Parallel()

	rels := []string{
		"",
		"[만화] 엔젤하트 전32권 완결.zip",
		"미생 1~9 완결 pdf/미생 1권.pdf",
		"[만화] 군계 1~25",
		"a/b/c.zip",
	}
	seen := map[string]string{}
	for _, rel := range rels {
		s, b := ids.SeriesID("manga", rel), ids.BookID("manga", rel)
		if s == b {
			t.Errorf("SeriesID(%q) == BookID(%q) == %q; the domain tag is missing from the hash input", rel, rel, s)
		}
		for what, id := range map[string]string{"series " + rel: s, "book " + rel: b} {
			if prev, dup := seen[id]; dup {
				t.Errorf("%s collides with %s: both %q", what, prev, id)
			}
			seen[id] = what
		}
	}
}

func TestIDs_backslashAndSlashSpellings_produceTheSameID(t *testing.T) {
	t.Parallel()

	// filepath.Rel returns `\`-separated paths on Windows. NFR-OPS-003 ships a
	// Windows binary, and a library on a shared volume must index to the same
	// ids from either OS.
	cases := []struct{ a, b string }{
		{`군계/01권.zip`, `군계\01권.zip`},
		{`a/b/c.zip`, `a\b\c.zip`},
		{`a/b/c.zip`, `a/b//c.zip`},
		{`a/b/c.zip`, `./a/b/c.zip`},
		{`a/b/c.zip`, `/a/b/c.zip`},
		{`a`, `a/`},
		{``, `.`},
	}
	for _, c := range cases {
		if got, want := ids.BookID("manga", c.b), ids.BookID("manga", c.a); got != want {
			t.Errorf("BookID(manga, %q) = %q, want %q (= BookID for %q)", c.b, got, want, c.a)
		}
		if got, want := ids.SeriesID("manga", c.b), ids.SeriesID("manga", c.a); got != want {
			t.Errorf("SeriesID(manga, %q) = %q, want %q (= SeriesID for %q)", c.b, got, want, c.a)
		}
	}
}

func TestIDs_distinctRelPaths_produceDistinctIDs(t *testing.T) {
	t.Parallel()

	// The NUL separator has to make the field boundaries unambiguous: without
	// it ("manga" + "x/y") and ("mangax" + "/y") would hash identically.
	seen := map[string]string{}
	for _, c := range []struct{ root, rel string }{
		{"manga", "x/y"},
		{"mangax", "y"},
		{"manga", "xy"},
		{"mang", "ax/y"},
		{"manga", "x"},
	} {
		id := ids.BookID(c.root, c.rel)
		if prev, dup := seen[id]; dup {
			t.Errorf("BookID(%q, %q) collides with %s: both %q", c.root, c.rel, prev, id)
		}
		seen[id] = c.root + "|" + c.rel
	}
}

// TestProgressIdentity_survivesRootMoveAndIndexRebuild is the FR-CFG-004 /
// FR-STT-003 / AC-006 proof, done against two real on-disk trees rather than
// against string literals: the same library materialised at two different
// absolute paths must yield the same book ids, because a moved root must not
// orphan progress.
func TestProgressIdentity_survivesRootMoveAndIndexRebuild(t *testing.T) {
	t.Parallel()

	layout := map[string]any{
		"[만화] 군계 1~25/군계(軍鷄) 01권.zip": testutil.BuildZIP(t, testutil.ZIPSpec{
			Entries: []testutil.Entry{{Name: "001.jpg", Data: testutil.TinyJPEG(t, 8, 8)}},
		}),
		"[만화] 군계 1~25/군계(軍鷄) 02권.zip": testutil.BuildZIP(t, testutil.ZIPSpec{
			Entries: []testutil.Entry{{Name: "001.jpg", Data: testutil.TinyJPEG(t, 8, 8)}},
		}),
	}

	// Two different physical locations, same logical content. This is a `mv` of
	// the media volume, or a NAS remounted somewhere else.
	before := testutil.BuildTreeAt(t, mkdir(t, filepath.Join(t.TempDir(), "mnt", "old-disk", "manga")), layout)
	after := testutil.BuildTreeAt(t, mkdir(t, filepath.Join(t.TempDir(), "srv", "media2", "manga")), layout)
	if before == after {
		t.Fatal("test setup produced identical paths; the move is not being exercised")
	}

	idsUnder := func(root, rootName string) map[string]string {
		out := map[string]string{}
		if err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			if rel == "." {
				return nil
			}
			if d.IsDir() {
				out[filepath.ToSlash(rel)] = ids.SeriesID(rootName, rel)
				return nil
			}
			out[filepath.ToSlash(rel)] = ids.BookID(rootName, rel)
			return nil
		}); err != nil {
			t.Fatalf("walking %s: %v", root, err)
		}
		if len(out) == 0 {
			t.Fatalf("walking %s produced no entries", root)
		}
		return out
	}

	// (1) The physical path of the root is not an input: same ids after a move.
	// (2) The ids are also what a rebuilt index recomputes, since the function
	//     is pure — nothing here has ever read index.db.
	moved := idsUnder(after, "manga")
	for rel, want := range idsUnder(before, "manga") {
		if got := moved[rel]; got != want {
			t.Errorf("id for %q changed when the root moved on disk: %q -> %q", rel, want, got)
		}
	}

	// (3) The documented, intended orphaning: the root's *logical* name is an
	//     identity. Renaming it in the YAML must change every id under it, or
	//     the shelf.example.yaml warning would be a lie.
	renamed := idsUnder(before, "manga-old")
	for rel, orig := range idsUnder(before, "manga") {
		if renamed[rel] == orig {
			t.Errorf("id for %q survived a root rename (%q); renaming a root must orphan its progress", rel, orig)
		}
	}
}

// mkdir creates dir and returns it, so BuildTreeAt (which requires an existing
// directory) can be pointed at a nested, deliberately different mount point.
func mkdir(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("creating %s: %v", dir, err)
	}
	return dir
}

func TestThumbKey_everyInput_changesTheKey(t *testing.T) {
	t.Parallel()

	const (
		bookID = goldenBookID
		cv     = "9f3a1c7d5e2b8046"
	)
	base := ids.ThumbKey(bookID, 1, 240, "jpeg", 82, cv)

	if len(base) != ids.Length || !ids.Valid(base) {
		t.Fatalf("ThumbKey returned %q, want a valid %d-char id", base, ids.Length)
	}
	if got := ids.ThumbKey(bookID, 1, 240, "jpeg", 82, cv); got != base {
		t.Errorf("ThumbKey is not deterministic: %q then %q", base, got)
	}

	variants := map[string]string{
		"different book":            ids.ThumbKey(goldenSeriesID, 1, 240, "jpeg", 82, cv),
		"different page":            ids.ThumbKey(bookID, 2, 240, "jpeg", 82, cv),
		"different width":           ids.ThumbKey(bookID, 1, 400, "jpeg", 82, cv),
		"different format":          ids.ThumbKey(bookID, 1, 240, "webp", 82, cv),
		"different quality":         ids.ThumbKey(bookID, 1, 240, "jpeg", 90, cv),
		"different content_version": ids.ThumbKey(bookID, 1, 240, "jpeg", 82, "0000000000000000"),
		// D-19 / FR-THM-006: invalidation is structural. A source file whose
		// size or mtime moved yields a new cv, hence a new path, so a stale
		// thumbnail can never be served — there is no invalidation code to get
		// wrong.
		"pdf page domain": ids.PDFPageKey(bookID, 1, 240, "jpeg", 82, cv),
	}
	for what, got := range variants {
		if got == base {
			t.Errorf("%s produced the same cache key %q", what, base)
		}
		if !ids.Valid(got) {
			t.Errorf("%s produced an invalid key %q", what, got)
		}
	}

	// The two-level fan-out of arch §5.6 slices the key, so it must be long
	// enough and lowercase for a case-insensitive filesystem.
	if len(base) < 4 {
		t.Fatalf("key %q is too short for the <k[0:2]>/<k[2:4]> fan-out", base)
	}
}

func TestValid_rejectsEverythingThatIsNotAnID(t *testing.T) {
	t.Parallel()

	// arch §7.1: a syntactically invalid id is 400, an unknown one is 404. This
	// is also traversal layer 1 (D-21) — none of these may ever be accepted.
	bad := []string{
		"",
		"ruzwlotzngls2ua",   // 15 chars
		"ruzwlotzngls2ua5x", // 17 chars
		"RUZWLOTZNGLS2UA5",  // uppercase
		"ruzwlotzngls2ua0",  // '0' is not in the alphabet
		"ruzwlotzngls2ua1",  // '1' is not in the alphabet
		"ruzwlotzngls2ua8",  // '8' is not in the alphabet
		"ruzwlotzngls2ua9",  // '9' is not in the alphabet
		"../../../etc/pass",
		"ruzwlotz/ngls2ua",
		"ruzwlotz.ngls2ua",
		"ruzwlotz\x00ngls2ua",
		"군계군계군계군계군계군계군계군계",
	}
	for _, s := range bad {
		if ids.Valid(s) {
			t.Errorf("Valid(%q) = true, want false", s)
		}
	}
	for _, s := range []string{goldenSeriesID, goldenBookID, "aaaaaaaaaaaaaaaa", "7777777777777777"} {
		if !ids.Valid(s) {
			t.Errorf("Valid(%q) = false, want true", s)
		}
	}
}
