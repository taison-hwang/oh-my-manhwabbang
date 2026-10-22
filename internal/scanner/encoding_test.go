package scanner

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	"shelf/internal/archive"
	"shelf/internal/testutil"
)

// Ruling E-56 stores the kenc label of every book in books.name_encoding. These
// are the two ways the label could be lost on a library that already exists,
// which is where the field spent its whole life as a hard-coded null.

// cp949ZIP is one flagless CP949 entry: a book kenc labels "cp949", which is a
// value no default or zero value could produce by accident.
func cp949ZIP(t *testing.T) []byte {
	t.Helper()
	return testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
		{RawName: testutil.CP949(t, "군계 001.jpg"), Data: jpeg(t)},
	}})
}

// rawIndex opens a second handle on the harness's index.db, for the one thing
// no API does: writing the NULL a schema-v2 row migrates to.
func rawIndex(t *testing.T, h *harness) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(h.dataDir, "index.db"))
	if err != nil {
		t.Fatalf("opening index.db raw: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// The upgrade: after the v3 migration every row's label is NULL and every file
// is unchanged. The first scan must list each book once — otherwise
// scanner.unchanged would skip them all and the label would stay NULL for
// ever — and must take only the label: the page rows, and the dimensions the
// thumbnail worker measured into them, are what the skip exists to keep. The
// scan after that must read nothing again.
func TestScan_nullNameEncoding_isRelabelledOnceThenSkipped(t *testing.T) {
	t.Parallel()
	h := newHarness(t, map[string]any{"[만화] 군계 1~25": map[string]any{
		"01권.zip": cp949ZIP(t),
		"02권":     imageDir(t, "001.jpg"),
	}})
	h.run(Request{})
	for _, b := range h.books("manga", "[만화] 군계 1~25") {
		if b.NameEncoding == "" {
			t.Fatalf("cold scan left %q without a label", b.DisplayName)
		}
	}

	raw := rawIndex(t, h)
	for _, q := range []string{
		`UPDATE books SET name_encoding = NULL`,
		// Stand-ins for what the thumbnail worker measures. A re-index would
		// rewrite the page rows and lose them.
		`UPDATE pages SET width = 111, height = 222`,
		`UPDATE books SET dims_state = 'done'`,
	} {
		if _, err := raw.Exec(q); err != nil {
			t.Fatalf("forging the post-migration state (%s): %v", q, err)
		}
	}

	h.lister.reset()
	h.run(Request{})
	if got := h.lister.listedPaths(); !equalStrings(got, []string{"[만화] 군계 1~25/01권.zip", "[만화] 군계 1~25/02권"}) {
		t.Errorf("first scan after the migration read %v, want both books once", got)
	}
	want := map[string]string{"01권.zip": "cp949", "02권": "utf-8"}
	for _, b := range h.books("manga", "[만화] 군계 1~25") {
		if b.NameEncoding != want[b.DisplayName] {
			t.Errorf("%q NameEncoding = %q after the relabel, want %q", b.DisplayName, b.NameEncoding, want[b.DisplayName])
		}
		if b.DimsState != "done" {
			t.Errorf("%q dims_state = %q after the relabel, want done kept", b.DisplayName, b.DimsState)
		}
		for _, p := range h.pages(b.ID) {
			if p.Width == nil || p.Height == nil || *p.Width != 111 || *p.Height != 222 {
				t.Errorf("%q page %d lost its measured 111x222 in the relabel (width %v, height %v)",
					b.DisplayName, p.PageNo, p.Width, p.Height)
			}
		}
	}

	h.lister.reset()
	h.run(Request{})
	if got := h.lister.listedPaths(); len(got) != 0 {
		t.Errorf("the scan after the relabel read %v; it must happen once, not every scan", got)
	}
}

// A skipped book that moved is rewritten in full (writeSeries), because its
// ord changed. The label has to travel with it like every other carried field,
// or the rewrite writes NULL over the one the last scan recorded.
func TestScan_skippedBookThatMoved_keepsItsNameEncoding(t *testing.T) {
	t.Parallel()
	h := newHarness(t, map[string]any{"[만화] 군계 1~25": map[string]any{
		"02권.zip": cp949ZIP(t),
	}})
	h.run(Request{})
	if b := h.books("manga", "[만화] 군계 1~25"); len(b) != 1 || b[0].NameEncoding != "cp949" {
		t.Fatalf("cold scan: %+v, want one cp949 book", b)
	}

	// A volume that sorts first pushes 02권 from ord 0 to ord 1 without
	// touching its file, so 02권 is skipped and still rewritten.
	// Through testutil: this package may not hold a write primitive, tests
	// included (scripts/check-readonly.sh, FR-CFG-005).
	testutil.BuildTreeAt(t, filepath.Join(h.rootDirs["manga"], "[만화] 군계 1~25"),
		map[string]any{"01권.zip": jpegZIP(t, "001.jpg")})

	h.lister.reset()
	h.run(Request{})
	if got := h.lister.listedPaths(); !equalStrings(got, []string{"[만화] 군계 1~25/01권.zip"}) {
		t.Fatalf("the scan read %v, want only the new volume (02권 must be skipped for this test to mean anything)", got)
	}
	books := h.books("manga", "[만화] 군계 1~25")
	if len(books) != 2 || books[1].DisplayName != "02권.zip" || books[1].Ord != 1 {
		t.Fatalf("books = %v, want 02권.zip moved to ord 1", bookNames(books))
	}
	if got := books[1].NameEncoding; got != "cp949" {
		t.Errorf("moved, skipped 02권.zip NameEncoding = %q, want cp949 carried from the last scan", got)
	}
}

// A book whose file has not changed but no longer lists — a transient read
// failure, or an ok PDF met by a nopdf binary — cannot be relabelled. It must
// fall through to the ordinary path and be recorded as what it now is. Kept as
// "ok with no label" instead, it would be listed again on every scan for ever,
// which is the cost the relabel exists to pay only once.
func TestScan_relabelThatCannotList_fallsThroughAndRecordsTheFailure(t *testing.T) {
	t.Parallel()
	h := newHarness(t, map[string]any{"[만화] 군계 1~25": map[string]any{
		"01권.zip": cp949ZIP(t),
	}})
	h.run(Request{})

	if _, err := rawIndex(t, h).Exec(`UPDATE books SET name_encoding = NULL`); err != nil {
		t.Fatalf("forging the post-migration NULL: %v", err)
	}
	h.lister.failWith["[만화] 군계 1~25/01권.zip"] = fmt.Errorf("opening: %w", archive.ErrCorrupt)

	h.run(Request{})
	books := h.books("manga", "[만화] 군계 1~25")
	if len(books) != 1 {
		t.Fatalf("books = %v, want the one volume", bookNames(books))
	}
	if books[0].Status == StatusOK {
		t.Errorf("a book that no longer lists is still recorded ok (label %q); it must fall through "+
			"to the ordinary path, or it is relisted on every scan", books[0].NameEncoding)
	}
	if books[0].Error == "" {
		t.Error("the fall-through recorded no error message")
	}
}
