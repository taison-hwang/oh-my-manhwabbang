//go:build integration

package integration

import (
	"fmt"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
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

// I-8 / AC-008 / NFR-PRF-001 — fifty random page jumps into the 1 540-page,
// 1.34 GB archive have a p95 time-to-first-byte under 100 ms once warm.
//
// The jumps are spread over the archive's fifteen volumes rather than one book's
// pages: D-73 made `배틀로얄 1~15 [완결].zip` the fifteen 권 its directories say
// it is. What NFR-PRF-001 measures is untouched by that — a jump is still one
// seek to a recorded local-header offset somewhere in 1.34 GB, and a jump into
// the last volume is a seek to the far end of it.
//
// The fifty jumps are timed twice. The first pass is AC-008's observation, logged
// and not asserted; the second, over a warm page cache, is NFR-PRF-001's budget.
func TestI8_AC008_randomJumpsInA1540PageVolumeAreFast(t *testing.T) {
	s := sharedServer(t)

	d := detailOf(s, battleRoyale)
	if len(d.Books) == 0 {
		t.Fatal("배틀로얄 has no book")
	}
	type target struct {
		id, cv string
		page   int
	}
	var targets []target
	for _, b := range d.Books {
		var book bookDetail
		s.get("/api/books/"+b.ID, &book)
		if len(book.Pages) != book.PageCount {
			t.Errorf("%s: a jump must need no extra round trip: got %d of %d PageInfo",
				b.Name, len(book.Pages), book.PageCount)
		}
		for _, p := range book.Pages {
			targets = append(targets, target{id: b.ID, cv: b.CV, page: p.N})
		}
	}
	if len(targets) < 500 {
		t.Fatalf("AC-008 needs a 500+ page archive; this one has %d", len(targets))
	}
	t.Logf("AC-008: 배틀로얄 has %d pages across %d volumes, and GET /api/books/{bid} returned "+
		"every PageInfo in one request per volume", len(targets), len(d.Books))

	// A fixed seed: the fifty jumps are the same fifty on every run, so a
	// regression is reproducible rather than "sometimes slow".
	rng := rand.New(rand.NewPCG(0x5EEDBEEF, 42))
	jumps := make([]target, 50)
	for i := range jumps {
		jumps[i] = targets[rng.IntN(len(targets))]
	}
	pass := func() (p50, p95, worst time.Duration) {
		samples := make([]time.Duration, 0, len(jumps))
		for _, tgt := range jumps {
			n := tgt.page
			start := time.Now()
			status, body, hdr := s.do(http.MethodGet,
				fmt.Sprintf("/api/books/%s/pages/%d?v=%s", tgt.id, n, tgt.cv), "")
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
		return samples[len(samples)/2], samples[(len(samples)*95)/100], samples[len(samples)-1]
	}

	// One file, one handle: the pool keys by path, so the fifteen volumes share
	// it. The first pass also fills the OS page cache, which NFR-PRF-001 assumes
	// ("캐시된 페이지 이미지"). On a cold cache this pass is a rotating disk's
	// random seeks, which no code here can move, so it is logged, not asserted.
	p50, p95, worst := pass()
	t.Logf("AC-008 cold (log only): 50 random jumps — p50 %s, p95 %s, max %s", p50, p95, worst)

	p50, p95, worst = pass()
	t.Logf("AC-008: 50 random jumps, warm — p50 %s, p95 %s, max %s", p50, p95, worst)
	if p95 > 100*time.Millisecond {
		t.Errorf("AC-008 / NFR-PRF-001: p95 was %s, over the 100 ms budget", p95)
	}
}

// NFR-PRF-005 — idle resident memory after a scan stays inside the 200 MB
// budget, measured twice against the same budget:
//
//   - "impl-plan §7": the curated set less its one PDF series, the condition
//     impl-plan §7 writes ("with no PDF or AVIF touched" — a scan opens a PDF
//     to count its pages);
//   - "prd": the curated set as it is, PDF included, because prd NFR-PRF-005
//     names no exception and the real collection holds PDFs.
//
// AVIF is out of both by construction: the binary carries the shipped tags.
//
// Each is a server process of its own: the binary built here, its own state
// directory, one full scan and nothing else — no page streamed. The test used
// to read its own process, which hosts the shared server *and* the harness, so
// whatever the earlier tests left in that process decided the result: 172 MB,
// 169 MB and 298 MB in three runs of the same suite (2026-10-26), the failing
// one with the smallest live heap of the three (55 MB against 70 and 87).
//
// Idle is what the runtime reaches when nobody asks the server for anything.
// RSS is sampled every 5 s after the scan returns; it passes once settled
// samples in a row are inside the budget, and fails if that has not happened
// within idleWindow — a window longer than the runtime's forced-GC period (two
// minutes), so a process that only gets there after a collection and the
// scavenger's return of freed spans is given the time to. No GC is forced from
// outside: the requirement is about the product left alone.
func TestNFRPRF005_idleMemoryStaysWithinBudget(t *testing.T) {
	root := requireRoot(t)
	if runtime.GOOS != "linux" {
		t.Skip("NFR-PRF-005 reads /proc/<pid>/status")
	}
	bin := buildShelf(t)
	var noPDF []string
	for _, n := range curated {
		if n != misaeng {
			noPDF = append(noPDF, n)
		}
	}
	t.Run("impl-plan §7 no PDF touched", func(t *testing.T) { idleRSSWithinBudget(t, bin, root, noPDF) })
	t.Run("prd the curated set as it is", func(t *testing.T) { idleRSSWithinBudget(t, bin, root, curated) })
}

func idleRSSWithinBudget(t *testing.T, bin, root string, names []string) {
	const (
		budgetKB   = 200 * 1024
		idleWindow = 150 * time.Second
		settled    = 3
	)
	srv, pid := startShelfProcess(t, bin, root, t.TempDir(), names)
	t.Logf("NFR-PRF-005: the separate server's full scan of %d series took %s",
		len(names), srv.scan(true, 10*time.Minute))

	status := fmt.Sprintf("/proc/%d/status", pid)
	start := time.Now()
	var trace []string
	inside := 0
	for {
		rss := vmRSSKB(status)
		elapsed := time.Since(start).Round(time.Second)
		trace = append(trace, fmt.Sprintf("%s %d KB", elapsed, rss))
		if rss == 0 {
			t.Fatalf("NFR-PRF-005: could not read VmRSS from %s", status)
		}
		if rss <= budgetKB {
			inside++
		} else {
			inside = 0
		}
		if inside == settled {
			t.Logf("NFR-PRF-005: idle RSS %d KB, inside %d KB for %d samples in a row (%s)",
				rss, budgetKB, settled, strings.Join(trace, ", "))
			return
		}
		if elapsed >= idleWindow {
			t.Errorf("NFR-PRF-005: idle RSS was not inside the %d KB budget for %d samples in a row within %s of the scan: %s",
				budgetKB, settled, idleWindow, strings.Join(trace, ", "))
			return
		}
		time.Sleep(5 * time.Second)
	}
}

// buildShelf builds cmd/shelf with the shipped tags (ruling E-21: noavif) into
// a temporary directory and returns the binary's path.
func buildShelf(t *testing.T) string {
	t.Helper()
	repo, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("locating the repository: %v", err)
	}
	bin := filepath.Join(t.TempDir(), "shelf")
	build := exec.Command("go", "build", "-trimpath", "-tags", "noavif", "-o", bin, "./cmd/shelf")
	build.Dir = repo
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	start := time.Now()
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building cmd/shelf: %v\n%s", err, out)
	}
	t.Logf("built cmd/shelf in %s", time.Since(start).Round(100*time.Millisecond))
	return bin
}

// startShelfProcess starts bin as a process of its own over the series in
// names, on a free port, with its state and TMPDIR under dir. It is stopped by
// its pid when the test ends. The returned server has no in-process app: only
// its HTTP helpers are used.
func startShelfProcess(t *testing.T, bin, root, dir string, names []string) (*server, int) {
	t.Helper()
	state, tmp := filepath.Join(dir, "state"), filepath.Join(dir, "tmp")
	for _, d := range []string{state, tmp} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatalf("creating %s: %v", d, err)
		}
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("finding a free port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	_, cfgPath := writeConfigOn(t, root, state, names, port)

	logFile, err := os.Create(filepath.Join(dir, "shelf.log"))
	if err != nil {
		t.Fatalf("creating the server log: %v", err)
	}
	cmd := exec.Command(bin, "-config", cfgPath)
	cmd.Env = append(os.Environ(), "TMPDIR="+tmp)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		t.Fatalf("starting %s: %v", bin, err)
	}
	exited := make(chan struct{})
	var waitErr error
	go func() { waitErr = cmd.Wait(); close(exited) }()
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-exited:
		case <-time.After(30 * time.Second):
			t.Error("the separate server did not stop within 30 s of SIGTERM; killing it")
			_ = cmd.Process.Kill()
			<-exited
		}
		_ = logFile.Close()
		if t.Failed() {
			if b, err := os.ReadFile(logFile.Name()); err == nil && len(b) > 0 {
				t.Logf("server log:\n%s", truncate(b))
			}
		}
	})

	// Ready is GET /api/health answering while this process is still alive; a
	// process that exits first (a bad config, the port taken between the
	// probe's Close and the bind) fails here with its log, rather than leaving
	// the health check to whatever else answers on the port.
	s := &server{t: t, base: fmt.Sprintf("http://127.0.0.1:%d", port)}
	deadline := time.Now().Add(30 * time.Second)
	for {
		select {
		case <-exited:
			t.Fatalf("the separate server exited before it was ready: %v", waitErr)
		default:
		}
		if resp, err := http.Get(s.base + "/api/health"); err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("the separate server never answered GET /api/health")
		}
		time.Sleep(50 * time.Millisecond)
	}
	select {
	case <-exited:
		t.Fatalf("the separate server exited as it became ready: %v", waitErr)
	default:
	}
	return s, cmd.Process.Pid
}
