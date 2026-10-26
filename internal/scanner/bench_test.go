package scanner

import (
	"fmt"
	"testing"
	"time"

	"shelf/internal/testutil"
)

// NFR-PRF-004 — "a no-change incremental scan of 1 000 series completes inside
// 30 s" — is really a statement about the *ratio*: the wave-1 spike indexed
// 11 157 archives and 1.36 M entries in 32.3 s cold, so the incremental path has
// to be a different order of magnitude, not a slightly better one.
//
// These build a synthetic library on a real filesystem and measure both paths.
// The ratio itself is asserted in incremental_ratio_test.go, behind the
// integration tag.

// syntheticLibrary writes seriesCount folder-of-zips series of booksPerSeries
// archives each, every archive holding pagesPerBook entries. One archive's bytes
// are reused for every file: the scanner reads the central directory, not the
// content, so distinct payloads would only slow the fixture down.
func syntheticLibrary(tb testing.TB, seriesCount, booksPerSeries, pagesPerBook int) string {
	tb.Helper()
	names := make([]string, 0, pagesPerBook)
	for i := 1; i <= pagesPerBook; i++ {
		names = append(names, fmt.Sprintf("%03d.jpg", i))
	}
	archive := jpegZIP(tb, names...)

	layout := make(map[string]any, seriesCount)
	for s := range seriesCount {
		books := make(map[string]any, booksPerSeries)
		for b := 1; b <= booksPerSeries; b++ {
			books[fmt.Sprintf("%02d권.zip", b)] = archive
		}
		layout[fmt.Sprintf("[만화] 합성 시리즈 %04d", s)] = books
	}
	return testutil.BuildTree(tb, layout)
}

// BenchmarkScan_cold measures a full index build. b.N runs each rebuild the
// index from empty so every iteration really is cold.
func BenchmarkScan_cold(b *testing.B) {
	root := syntheticLibrary(b, 40, 5, 20)
	h := newHarnessAt(b, map[string]string{"manga": root})
	ctx := b.Context()

	b.ReportAllocs()
	for b.Loop() {
		b.StopTimer()
		if err := h.idx.Reset(ctx); err != nil {
			b.Fatalf("resetting the index: %v", err)
		}
		b.StartTimer()
		if _, err := h.scanner.Run(ctx, Request{Full: true}); err != nil {
			b.Fatalf("scan: %v", err)
		}
	}
}

// BenchmarkScan_incremental measures the FR-IDX-003 path: nothing changed, so
// nothing may be opened.
func BenchmarkScan_incremental(b *testing.B) {
	root := syntheticLibrary(b, 40, 5, 20)
	h := newHarnessAt(b, map[string]string{"manga": root})
	ctx := b.Context()

	if _, err := h.scanner.Run(ctx, Request{}); err != nil {
		b.Fatalf("priming scan: %v", err)
	}
	h.lister.reset()

	b.ReportAllocs()
	for b.Loop() {
		if _, err := h.scanner.Run(ctx, Request{}); err != nil {
			b.Fatalf("scan: %v", err)
		}
	}
	b.StopTimer()
	if got := h.lister.listedPaths(); len(got) != 0 {
		b.Fatalf("the incremental benchmark opened %d archives; it is measuring the wrong path", len(got))
	}
}

// BenchmarkClassify isolates the walk-and-classify half, which is the part that
// runs even when every book is skipped.
func BenchmarkClassify(b *testing.B) {
	root := syntheticLibrary(b, 40, 5, 4)
	h := newHarnessAt(b, map[string]string{"manga": root})
	rt := &rootRun{
		cfg: h.cfgRoots[0], runID: "bench", logs: &logBuffer{},
		log: quietLogger(), now: time.Now,
	}
	var ok bool
	rt.root, ok = h.rootSet.Root("manga")
	if !ok {
		b.Fatal("root is unreachable")
	}

	b.ReportAllocs()
	for b.Loop() {
		children, err := readDir(rt.root, "", false)
		if err != nil {
			b.Fatalf("reading the root: %v", err)
		}
		for _, c := range children {
			if _, ok := h.scanner.classifyChild(rt, c); !ok {
				b.Fatalf("failed to classify %q", c.rel)
			}
		}
	}
}
