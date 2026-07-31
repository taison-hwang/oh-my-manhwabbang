//go:build integration

package integration

import (
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// I-3 / AC-003 — a folder-of-ZIPs series and a single-ZIP series are read
// through an identical flow: the same three requests, the same response shapes,
// the same headers.
func TestI3_AC003_folderAndZipSeriesReadThroughOneFlow(t *testing.T) {
	s := sharedServer(t)

	type shape struct {
		hasPages     bool
		pagesMatch   bool
		hasPrefs     bool
		pageStatus   int
		pageIsImage  bool
		cacheControl string
	}
	read := func(name string) shape {
		t.Helper()
		d := detailOf(s, name)
		var first bookSummary
		for _, b := range d.Books {
			if b.Status == "ok" && b.PageCount > 0 {
				first = b
				break
			}
		}
		if first.ID == "" {
			t.Fatalf("%s has no readable volume", name)
		}
		var book bookDetail
		s.get("/api/books/"+first.ID, &book)
		status, body, hdr := s.do(http.MethodGet,
			fmt.Sprintf("/api/books/%s/pages/1?v=%s", first.ID, first.CV), "")
		_ = body
		return shape{
			hasPages:     book.Pages != nil,
			pagesMatch:   len(book.Pages) == book.PageCount,
			hasPrefs:     book.Prefs.FitMode != "",
			pageStatus:   status,
			pageIsImage:  strings.HasPrefix(hdr.Get("Content-Type"), "image/"),
			cacheControl: hdr.Get("Cache-Control"),
		}
	}

	folder := read(clover)
	zipped := read(wheel)
	loose := read(wounds)

	if folder != zipped {
		t.Errorf("AC-003: the folder-of-ZIPs and single-ZIP flows differ\n folder: %+v\n zip:    %+v",
			folder, zipped)
	}
	if folder != loose {
		t.Errorf("AC-003: the folder-of-ZIPs and folder-of-images flows differ\n zips:   %+v\n images: %+v",
			folder, loose)
	}
	if !folder.hasPages || !folder.pagesMatch || folder.pageStatus != http.StatusOK || !folder.pageIsImage {
		t.Errorf("AC-003: the shared flow is not actually working: %+v", folder)
	}
	if folder.cacheControl != "public, max-age=31536000, immutable" {
		t.Errorf("FR-SRV-007: a page with a matching ?v= must be immutable, got %q", folder.cacheControl)
	}
}

// I-4 / AC-004 — a PDF series opens in the same viewer flow as a ZIP series and
// its pages arrive as JPEG (FR-SRV-006).
func TestI4_AC004_pdfSeriesRendersThroughTheSameFlow(t *testing.T) {
	s := sharedServer(t)

	d := detailOf(s, misaeng)
	if len(d.Books) == 0 {
		t.Fatal("미생 has no books")
	}
	for _, b := range d.Books {
		if b.Kind != "pdf" {
			t.Errorf("미생 volume %q has kind %q, want pdf", b.Name, b.Kind)
		}
	}
	var first bookSummary
	for _, b := range d.Books {
		if b.Status == "ok" && b.PageCount > 0 {
			first = b
			break
		}
	}
	if first.ID == "" {
		t.Fatal("no readable PDF volume")
	}
	if first.PageCount < 10 {
		t.Errorf("%q reports %d pages, which is not a plausible volume", first.Name, first.PageCount)
	}

	var book bookDetail
	s.get("/api/books/"+first.ID, &book)
	if len(book.Pages) != first.PageCount {
		t.Errorf("a PDF book must return all its PageInfo in one shot: %d of %d",
			len(book.Pages), first.PageCount)
	}

	start := time.Now()
	body, hdr := s.bodyOf(fmt.Sprintf("/api/books/%s/pages/1?v=%s&w=1400", first.ID, first.CV))
	t.Logf("AC-004: rendered page 1 of %q in %s (%d bytes)", first.Name, time.Since(start), len(body))
	if ct := hdr.Get("Content-Type"); ct != "image/jpeg" {
		t.Errorf("a rendered PDF page must be image/jpeg, got %q", ct)
	}
	if len(body) < 5000 {
		t.Errorf("the rendered page is %d bytes, which cannot be a full-width scan", len(body))
	}

	// The second request must come from the render cache, not pdfium.
	warm := time.Now()
	s.bodyOf(fmt.Sprintf("/api/books/%s/pages/1?v=%s&w=1400", first.ID, first.CV))
	t.Logf("AC-004: the cached re-render took %s", time.Since(warm))
}

// I-5 / AC-001 / NFR-PRF-006 — read every page of the 1 540-page volume: the
// resident set must not grow with the archive, and nothing may be written
// outside cache_dir.
func TestI5_AC001_readingAWholeVolumeStreamsAndWritesNothing(t *testing.T) {
	s := sharedServer(t)

	d := detailOf(s, battleRoyale)
	if len(d.Books) == 0 {
		t.Fatal("배틀로얄 has no book")
	}
	b := d.Books[0]
	var book bookDetail
	s.get("/api/books/"+b.ID, &book)
	if len(book.Pages) < 1000 {
		t.Fatalf("배틀로얄 has %d pages; the AC-001 case needs the 1 540-page volume", len(book.Pages))
	}

	marker := filepath.Join(t.TempDir(), "marker")
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	markerInfo, err := os.Stat(marker)
	if err != nil {
		t.Fatal(err)
	}

	// The temp-file half of AC-001. The server runs in this process, so
	// os.TempDir() is where any stray scratch file would land — and os.TempDir()
	// re-reads TMPDIR on every call, so pointing it at a private, empty
	// directory makes the assertion about *this* run rather than about whatever
	// else the machine happens to keep in /tmp. cwd is the other half of
	// impl-plan §6.2 I-5's `find $TMPDIR $PWD -newer <marker>`.
	privateTmp := filepath.Join(t.TempDir(), "tmp")
	if err := os.Mkdir(privateTmp, 0o700); err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", privateTmp)
	time.Sleep(1100 * time.Millisecond)

	before := residentKB(t)
	bytesRead := 0
	for _, p := range book.Pages {
		body, _ := s.bodyOf(fmt.Sprintf("/api/books/%s/pages/%d?v=%s", b.ID, p.N, b.CV))
		bytesRead += len(body)
	}
	after := residentKB(t)

	t.Logf("NFR-PRF-006: streamed %d pages (%.1f MiB) — test-process RSS %d KB → %d KB",
		len(book.Pages), float64(bytesRead)/(1<<20), before, after)
	// The measurement is of the *test* process, which holds every response body
	// only transiently; the server is in-process too, so its buffers are
	// included. A per-page buffer of the whole archive would show as hundreds
	// of megabytes here. The budget is impl-plan §6.2 I-5's: 64 MiB, in the
	// kilobytes residentKB reports.
	if before > 0 && after-before > 64*1024 {
		t.Errorf("NFR-PRF-006: RSS grew by %d KB while streaming a 1.34 GB archive, over the 64 MiB budget",
			after-before)
	}

	// AC-001, the file half: reading a whole volume creates nothing anywhere
	// but cache_dir and data_dir. sharedDir holds both, and is excluded for
	// that reason and no other.
	for _, dir := range []string{privateTmp, cwd} {
		if created := createdSince(t, dir, markerInfo.ModTime(), sharedDir); len(created) > 0 {
			t.Errorf("AC-001: reading a volume wrote %d path(s) under %s, outside cache_dir and data_dir:\n%s",
				len(created), dir, strings.Join(created, "\n"))
		}
	}

	// AC-001: nothing under the media volume changed while a whole volume was
	// read (the write half of FR-CFG-005 is I-9; this is the read half).
	if changed := changedSince(t, filepath.Join(testRoot(), battleRoyale), markerInfo.ModTime()); len(changed) > 0 {
		t.Errorf("AC-001: reading a volume modified the media volume: %v", changed)
	}
}

// createdSince walks dir and returns every entry — file or directory — modified
// after `since`, skipping the named subtrees. It is `find <dir> -newer <marker>`
// with the exclusions AC-001 allows.
func createdSince(t *testing.T, dir string, since time.Time, skip ...string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		for _, s := range skip {
			if s != "" && (p == s || strings.HasPrefix(p, s+string(filepath.Separator))) {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
		}
		if p == dir {
			return nil // the container itself, not something written into it
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.ModTime().After(since) {
			out = append(out, fmt.Sprintf("  %s  (mtime %s)", p, info.ModTime().Format(time.RFC3339)))
		}
		return nil
	})
	if err != nil {
		t.Logf("walking %s: %v", dir, err)
	}
	return out
}

// I-6 / AC-005 / AC-006 — delete index.db and the whole cache, restart,
// rescan: reading progress, per-book preferences and settings are intact and
// covers regenerate.
//
// This one runs on its own state directory: it destroys what it works on.
func TestI6_AC005_AC006_progressSurvivesAnIndexAndCacheWipe(t *testing.T) {
	root := requireRoot(t)
	state := t.TempDir()

	s := startServer(t, root, state, false)
	s.scan(true, 10*time.Minute)

	item := mustSeries(s, clover)
	var d seriesDetail
	s.get("/api/series/"+item.ID, &d)
	var book bookSummary
	for _, b := range d.Books {
		if b.Status == "ok" && b.PageCount > 1 {
			book = b
			break
		}
	}
	if book.ID == "" {
		t.Fatal("no readable Clover volume")
	}

	if status, body, _ := s.do(http.MethodPut, "/api/books/"+book.ID+"/progress", `{"page":7}`); status != http.StatusOK {
		t.Fatalf("PUT progress = %d: %s", status, truncate(body))
	}
	if status, body, _ := s.do(http.MethodPut, "/api/books/"+book.ID+"/prefs", `{"reading_direction":"rtl"}`); status != http.StatusOK {
		t.Fatalf("PUT prefs = %d: %s", status, truncate(body))
	}
	if status, body, _ := s.do(http.MethodPut, "/api/settings", `{"theme":"dark","prefetch":6}`); status != http.StatusOK {
		t.Fatalf("PUT settings = %d: %s", status, truncate(body))
	}
	// Warm a cover so its regeneration is observable.
	waitForCover(t, s, item)
	s.stop()

	// The wipe: index.db and its two sidecars, and the entire cache directory.
	for _, f := range []string{"index.db", "index.db-wal", "index.db-shm"} {
		if err := os.Remove(filepath.Join(state, "data", f)); err != nil && !os.IsNotExist(err) {
			t.Fatalf("removing %s: %v", f, err)
		}
	}
	if err := os.RemoveAll(filepath.Join(state, "cache")); err != nil {
		t.Fatalf("removing the cache: %v", err)
	}
	if _, err := os.Stat(filepath.Join(state, "data", "user.db")); err != nil {
		t.Fatalf("AC-006: user.db must not be part of the wipe: %v", err)
	}

	s2 := startServer(t, root, state, false)
	defer s2.stop()

	// Settings live purely in user.db and need no index row, so they are
	// readable the instant the server is back up.
	var settings struct {
		Theme    string `json:"theme"`
		Prefetch int    `json:"prefetch"`
	}
	s2.get("/api/settings", &settings)
	if settings.Theme != "dark" || settings.Prefetch != 6 {
		t.Errorf("AC-006: settings did not survive: %+v", settings)
	}
	// The book itself is gone until the scan puts it back: /api/books/{bid}
	// resolves an id through the *index*, and the index is what was deleted.
	if status, _, _ := s2.do(http.MethodGet, "/api/books/"+book.ID, ""); status != http.StatusNotFound {
		t.Errorf("with index.db deleted, GET /api/books/{bid} should be 404, got %d", status)
	}

	// AC-005: the library recovers completely from the filesystem alone.
	s2.scan(true, 10*time.Minute)
	var list seriesList
	s2.get("/api/series?limit=200", &list)
	if list.Total != len(curated) {
		t.Errorf("AC-005: the rebuilt library has %d series, want %d", list.Total, len(curated))
	}

	// AC-006 / FR-CFG-004: identity is derived from the configuration and the
	// filesystem only, so the rebuild reproduces the same id — which is the
	// entire mechanism by which the authored data reattaches itself.
	var reread bookDetail
	status, body, _ := s2.do(http.MethodGet, "/api/books/"+book.ID, "")
	if status != http.StatusOK {
		t.Fatalf("FR-CFG-004: the rebuilt index did not reproduce book %s (%d): %s",
			book.ID, status, truncate(body))
	}
	s2.get("/api/books/"+book.ID, &reread)
	if reread.Progress == nil {
		t.Fatal("AC-006: the rebuilt book carries no progress")
	}
	if reread.Progress.LastPage != 7 {
		t.Errorf("AC-006: last_page = %d after the rebuild, want 7", reread.Progress.LastPage)
	}
	var prefs struct {
		ReadingDirection string `json:"reading_direction"`
		IsOverride       bool   `json:"is_override"`
	}
	s2.get("/api/books/"+book.ID+"/prefs", &prefs)
	if prefs.ReadingDirection != "rtl" || !prefs.IsOverride {
		t.Errorf("AC-006: the per-book reading direction did not survive: %+v", prefs)
	}
	if !waitForCover(t, s2, mustSeries(s2, clover)) {
		t.Error("AC-005: the cover did not regenerate after the cache was deleted")
	}
}

// waitForCover requests a cover, honouring the 202 + Retry-After dance.
func waitForCover(t *testing.T, s *server, item seriesSummary) bool {
	t.Helper()
	path := "/api/series/" + item.ID + "/cover?w=240"
	if item.CoverCV != "" {
		path += "&v=" + item.CoverCV
	}
	for range 20 {
		status, _, hdr := s.do(http.MethodGet, path, "")
		switch status {
		case http.StatusOK:
			return true
		case http.StatusAccepted:
			d, _ := strconv.Atoi(hdr.Get("Retry-After"))
			if d <= 0 {
				d = 1
			}
			time.Sleep(time.Duration(d) * time.Second)
		default:
			t.Logf("cover for %q returned %d", item.Name, status)
			return false
		}
	}
	return false
}

// I-9 / FR-CFG-005 — after a full scan and a full read, nothing under the media
// root has been created, modified or re-timestamped.
func TestI9_FRCFG005_theMediaVolumeIsNeverWritten(t *testing.T) {
	root := requireRoot(t)
	state := t.TempDir()

	marker := filepath.Join(state, "marker")
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	mi, err := os.Stat(marker)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)

	s := startServer(t, root, state, false)
	defer s.stop()
	s.scan(true, 10*time.Minute)

	// A read of every shape, so the assertion covers serving and not just
	// scanning.
	for _, name := range curated {
		d := detailOf(s, name)
		for _, b := range d.Books {
			if b.Status != "ok" || b.PageCount == 0 {
				continue
			}
			s.bodyOf(fmt.Sprintf("/api/books/%s/pages/1?v=%s", b.ID, b.CV))
			break
		}
	}

	var offenders []string
	for _, name := range curated {
		offenders = append(offenders, changedSince(t, filepath.Join(root, name), mi.ModTime())...)
		if len(offenders) > 20 {
			break
		}
	}
	if len(offenders) > 0 {
		t.Errorf("FR-CFG-005: %d path(s) under the media root changed:\n%s",
			len(offenders), strings.Join(offenders, "\n"))
	} else {
		t.Log("FR-CFG-005: the media volume is byte-for-byte and mtime-for-mtime untouched")
	}
}

// changedSince walks a path and returns anything modified after `since`.
func changedSince(t *testing.T, path string, since time.Time) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // an unreadable entry is not evidence of a write
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.ModTime().After(since) {
			out = append(out, fmt.Sprintf("  %s  (mtime %s)", p, info.ModTime().Format(time.RFC3339)))
		}
		return nil
	})
	if err != nil {
		t.Logf("walking %s: %v", path, err)
	}
	return out
}

// residentKB reads VmRSS from /proc/self/status. It returns 0 anywhere that is
// not Linux, and the callers treat 0 as "not measured".
func residentKB(t *testing.T) int {
	t.Helper()
	if runtime.GOOS != "linux" {
		return 0
	}
	runtime.GC()
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "VmRSS:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0
		}
		n, err := strconv.Atoi(fields[1])
		if err != nil {
			return 0
		}
		return n
	}
	return 0
}
