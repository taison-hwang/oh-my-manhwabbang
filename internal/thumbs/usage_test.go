package thumbs

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"shelf/internal/testutil"
)

// usageOf pulls one kind's row out of a Usage report.
func usageOf(t *testing.T, u Usage, kind Kind) UsageEntry {
	t.Helper()
	for _, e := range u.Entries {
		if e.Kind == kind {
			return e
		}
	}
	t.Fatalf("usage report has no %q row: %+v", kind, u.Entries)
	return UsageEntry{}
}

// ---------------------------------------------------------------------------
// FR-THM-008 — usage
// ---------------------------------------------------------------------------

func TestUsage_reportsFilesAndBytesPerKind(t *testing.T) {
	t.Parallel()
	clk := &fakeClock{}
	h := newHarness(t, func(o *Options) { o.Now = clk.Now })

	// Two thumbnails and one cached PDF render, so the per-kind split is real.
	for _, w := range []int{120, 240} {
		if _, err := h.svc.Generate(t.Context(), h.pageReq(1, w)); err != nil {
			t.Fatalf("Generate: %v", err)
		}
	}
	render := testutil.TinyJPEG(t, 300, 400)
	if _, err := h.svc.StorePDFPage(h.zipBook, 1, 1200, h.zipCV, render); err != nil {
		t.Fatalf("StorePDFPage: %v", err)
	}

	clk.advance(time.Minute + time.Second) // past the 60 s memo
	u, err := h.svc.Usage(t.Context())
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if u.CacheDir != h.cacheDir {
		t.Fatalf("cache_dir = %q, want %q", u.CacheDir, h.cacheDir)
	}
	if got := usageOf(t, u, KindThumbs); got.Files != 2 {
		t.Fatalf("thumbs files = %d, want 2", got.Files)
	}
	pdf := usageOf(t, u, KindPDF)
	if pdf.Files != 1 || pdf.Bytes != int64(len(render)) {
		t.Fatalf("pdf row = %+v, want 1 file of %d bytes", pdf, len(render))
	}
	if wazero := usageOf(t, u, KindWazero); wazero.Files != 0 || wazero.Bytes != 0 {
		t.Fatalf("wazero row = %+v, want zeros", wazero)
	}

	var sum int64
	for _, e := range u.Entries {
		sum += e.Bytes
	}
	if u.TotalBytes != sum {
		t.Fatalf("total_bytes = %d, want the sum of the rows, %d", u.TotalBytes, sum)
	}
	if u.TotalFiles != 3 {
		t.Fatalf("total files = %d, want 3", u.TotalFiles)
	}
}

// arch §7.9 pins the walk to once per 60 s. At 1.36 M files this is the
// difference between a settings dialog that polls and a settings dialog that
// hammers the filesystem.
func TestUsage_isMemoisedForSixtySeconds(t *testing.T) {
	t.Parallel()
	clk := &fakeClock{}
	h := newHarness(t, func(o *Options) { o.Now = clk.Now })

	if _, err := h.svc.Generate(t.Context(), h.pageReq(1, 120)); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	first, err := h.svc.Usage(t.Context())
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if usageOf(t, first, KindThumbs).Files != 1 {
		t.Fatalf("first walk = %+v", first.Entries)
	}

	if _, err := h.svc.Generate(t.Context(), h.pageReq(2, 120)); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	clk.advance(59 * time.Second)
	cached, err := h.svc.Usage(t.Context())
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if usageOf(t, cached, KindThumbs).Files != 1 {
		t.Fatal("the walk was repeated inside the 60 s window")
	}
	if !cached.ComputedAt.Equal(first.ComputedAt) {
		t.Fatal("computed_at moved without a new walk")
	}

	clk.advance(2 * time.Second)
	fresh, err := h.svc.Usage(t.Context())
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if usageOf(t, fresh, KindThumbs).Files != 2 {
		t.Fatal("the walk was not repeated after the window expired")
	}
}

// ---------------------------------------------------------------------------
// FR-THM-008 — purge
// ---------------------------------------------------------------------------

func TestPurge_byKind_removesOnlyThatSubtree(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)

	if _, err := h.svc.Generate(t.Context(), h.pageReq(1, 240)); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	render := testutil.TinyJPEG(t, 300, 400)
	if _, err := h.svc.StorePDFPage(h.zipBook, 1, 1200, h.zipCV, render); err != nil {
		t.Fatalf("StorePDFPage: %v", err)
	}

	got, err := h.svc.Purge(t.Context(), "thumbs")
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if got.DeletedFiles != 1 || got.FreedBytes <= 0 {
		t.Fatalf("purge reported %+v, want one file and a positive byte count", got)
	}
	if _, err := os.Stat(filepath.Join(h.cacheDir, "thumbs")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the thumbs subtree survived: %v", err)
	}
	if _, ok := h.svc.LookupPDFPage(h.zipBook, 1, 1200, h.zipCV); !ok {
		t.Fatal("purging thumbs also removed the pdf renders")
	}

	// And the cache regenerates, which is FR-THM-007 again from the UI's side.
	res := h.getReady(h.pageReq(1, 240))
	decodeJPEG(t, res.Path)
}

func TestPurge_all_removesEveryKind(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)

	if _, err := h.svc.Generate(t.Context(), h.pageReq(1, 240)); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if _, err := h.svc.StorePDFPage(h.zipBook, 1, 1200, h.zipCV, testutil.TinyJPEG(t, 100, 100)); err != nil {
		t.Fatalf("StorePDFPage: %v", err)
	}
	writeFile(t, filepath.Join(h.cacheDir, "wazero", "module.bin"), []byte("compiled wasm"))

	got, err := h.svc.Purge(t.Context(), "all")
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if got.DeletedFiles != 3 {
		t.Fatalf("deleted_files = %d, want 3", got.DeletedFiles)
	}
	for _, k := range kinds {
		if _, err := os.Stat(filepath.Join(h.cacheDir, string(k))); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s survived a purge of everything: %v", k, err)
		}
	}
	u, err := h.svc.Usage(t.Context())
	if err != nil {
		t.Fatalf("Usage after purge: %v", err)
	}
	if u.TotalBytes != 0 {
		t.Fatalf("usage after a full purge = %d bytes, want 0", u.TotalBytes)
	}
}

// The purge selector is an enumeration, never a path. This is the whole of "a
// purge cannot walk outside the cache directory": there is no string a caller
// can send that names a directory of their choosing.
func TestPurge_anythingButAKnownKind_deletesNothing(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)

	if _, err := h.svc.Generate(t.Context(), h.pageReq(1, 240)); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	// A file next to the cache directory, and the media volume itself, stand in
	// for "everything the server can reach but must not delete".
	sentinel := filepath.Join(filepath.Dir(h.cacheDir), "precious.txt")
	writeFile(t, sentinel, []byte("do not delete"))
	mediaBefore := treeSnapshot(t, h.rootPath)

	for _, bad := range []string{
		"", "..", "../..", "/", "/etc", "thumbs/../..", `..\..`,
		"THUMBS", "thumbs ", "./thumbs", h.cacheDir, "*", "all/../..",
	} {
		got, err := h.svc.Purge(t.Context(), bad)
		if !errors.Is(err, ErrUnknownKind) {
			t.Errorf("Purge(%q) = (%+v, %v), want ErrUnknownKind", bad, got, err)
		}
		if got.DeletedFiles != 0 || got.FreedBytes != 0 {
			t.Errorf("Purge(%q) reported deleting %+v", bad, got)
		}
	}

	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("a file beside the cache directory was deleted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(h.cacheDir, "thumbs")); err != nil {
		t.Fatalf("the thumbs subtree was deleted by a rejected purge: %v", err)
	}
	assertMediaUnchanged(t, h.rootPath, mediaBefore)
}

func TestPurge_missingCacheDirectory_reportsZeroAndSucceeds(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)

	if err := os.RemoveAll(h.cacheDir); err != nil {
		t.Fatalf("removing the cache directory: %v", err)
	}
	got, err := h.svc.Purge(t.Context(), "all")
	if err != nil {
		t.Fatalf("Purge on a missing cache directory: %v", err)
	}
	if got.DeletedFiles != 0 || got.FreedBytes != 0 {
		t.Fatalf("purge of nothing reported %+v", got)
	}
}

// "전체 삭제" is a user saying "try again". Replaying a ten-minute-old
// undecodable verdict afterwards would make the button look broken.
func TestPurge_clearsTheUndecodableMemo(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)

	req := h.writeLoose("anim.webp", testutil.TinyAnimatedWebP(t))
	if _, err := h.svc.Generate(t.Context(), req); !errors.Is(err, ErrUndecodable) {
		t.Fatalf("Generate: %v", err)
	}
	j, err := h.svc.prepare(req)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if h.svc.negativeFor(j.key) == nil {
		t.Fatal("the failure was not memoised in the first place")
	}

	if _, err := h.svc.Purge(t.Context(), "all"); err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if got := h.svc.negativeFor(j.key); got != nil {
		t.Fatalf("the memo survived a purge: %v", got)
	}
}

func TestWithinDir_isNotFooledByASharedPrefix(t *testing.T) {
	t.Parallel()
	base := filepath.Join("/var", "cache", "shelf")
	for path, want := range map[string]bool{
		base:                              true,
		filepath.Join(base, "thumbs"):     true,
		filepath.Join(base, "a", "b.jpg"): true,
		"/var/cache/shelf-evil":           false,
		"/var/cache":                      false,
		"/":                               false,
	} {
		if got := withinDir(path, base); got != want {
			t.Errorf("withinDir(%q, %q) = %v, want %v", path, base, got, want)
		}
	}
}
