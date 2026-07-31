//go:build integration

package integration

import (
	"fmt"
	"math/rand/v2"
	"net/http"
	"sort"
	"testing"
	"time"
)

// I-7 / NFR-PRF-004 — a second scan immediately after the first finishes in
// under 30 s, because nothing changed and the (size, mtime) and directory
// fingerprint checks skip every book (FR-IDX-003).
func TestI7_NFRPRF004_aNoChangeRescanIsUnder30Seconds(t *testing.T) {
	s := sharedServer(t)
	t.Logf("the initial full scan took %.1fs", sharedScanSeconds)

	took := s.scan(false, 5*time.Minute)
	t.Logf("NFR-PRF-004: the no-change rescan took %s", took)
	if took > 30*time.Second {
		t.Errorf("NFR-PRF-004: a no-change rescan took %s, over the 30 s budget", took)
	}

	// It has to be a *skip*, not a fast re-index: the library must be identical
	// afterwards.
	var list seriesList
	s.get("/api/series?limit=200", &list)
	if list.Total != len(curated) {
		t.Errorf("the rescan changed the library: %d series, want %d", list.Total, len(curated))
	}
}

// I-8 / AC-008 / NFR-PRF-001 — fifty random page jumps in the 1 540-page volume
// have a p95 time-to-first-byte under 100 ms once warm.
func TestI8_AC008_randomJumpsInA1540PageVolumeAreFast(t *testing.T) {
	s := sharedServer(t)

	d := detailOf(s, battleRoyale)
	if len(d.Books) == 0 {
		t.Fatal("배틀로얄 has no book")
	}
	b := d.Books[0]
	var book bookDetail
	s.get("/api/books/"+b.ID, &book)
	if book.PageCount < 500 {
		t.Fatalf("AC-008 needs a 500+ page volume; this one has %d", book.PageCount)
	}
	t.Logf("AC-008: %s has %d pages, and GET /api/books/{bid} returned all %d PageInfo in one request",
		b.Name, book.PageCount, len(book.Pages))
	if len(book.Pages) != book.PageCount {
		t.Errorf("a jump must need no extra round trip: got %d of %d PageInfo",
			len(book.Pages), book.PageCount)
	}

	// Warm the archive handle pool: NFR-PRF-001 is about a warm cached page,
	// and the first request of all also pays for opening a 1.34 GB file.
	for range 3 {
		s.bodyOf(fmt.Sprintf("/api/books/%s/pages/1?v=%s", b.ID, b.CV))
	}

	// A fixed seed: the fifty jumps are the same fifty on every run, so a
	// regression is reproducible rather than "sometimes slow".
	rng := rand.New(rand.NewPCG(0x5EEDBEEF, 42))
	samples := make([]time.Duration, 0, 50)
	for range 50 {
		n := rng.IntN(book.PageCount) + 1 // 1-based; there is no page 0
		start := time.Now()
		status, body, hdr := s.do(http.MethodGet,
			fmt.Sprintf("/api/books/%s/pages/%d?v=%s", b.ID, n, b.CV), "")
		elapsed := time.Since(start)
		if status != http.StatusOK {
			t.Fatalf("page %d returned %d", n, status)
		}
		if ct := hdr.Get("Content-Type"); ct != "image/jpeg" {
			t.Errorf("page %d content type = %q", n, ct)
		}
		if len(body) == 0 {
			t.Errorf("page %d was empty", n)
		}
		samples = append(samples, elapsed)
	}

	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	p50 := samples[len(samples)/2]
	p95 := samples[(len(samples)*95)/100]
	t.Logf("AC-008: 50 random jumps — p50 %s, p95 %s, max %s", p50, p95, samples[len(samples)-1])
	if p95 > 100*time.Millisecond {
		t.Errorf("AC-008 / NFR-PRF-001: p95 was %s, over the 100 ms budget", p95)
	}
}

// NFR-PRF-005 — idle resident memory after a scan of the curated set, with no
// AVIF decoded, stays inside the 200 MB budget.
//
// It is measured on the test process, which hosts the server, so it is an upper
// bound that includes the test harness itself. A PDF *has* been rendered by the
// time this runs if I-4 went first, which is why the check is against the
// budget rather than against a baseline.
func TestNFRPRF005_idleMemoryStaysWithinBudget(t *testing.T) {
	s := sharedServer(t)
	var health struct {
		OK      bool `json:"ok"`
		Verbose *struct {
			Goroutines  int    `json:"goroutines"`
			HeapAllocKB uint64 `json:"heap_alloc_kb"`
			SysKB       uint64 `json:"sys_kb"`
		} `json:"verbose"`
	}
	s.get("/api/health?verbose=1", &health)
	if health.Verbose == nil {
		t.Skip("this build does not report verbose health counters")
	}
	rss := residentKB(t)
	t.Logf("NFR-PRF-005: RSS %d KB, heap %d KB, sys %d KB, %d goroutines",
		rss, health.Verbose.HeapAllocKB, health.Verbose.SysKB, health.Verbose.Goroutines)
	if rss > 0 && rss > 200*1024 {
		t.Errorf("NFR-PRF-005: idle RSS is %d KB, over the 200 MB budget", rss)
	}
}
