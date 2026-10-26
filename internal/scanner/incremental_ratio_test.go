//go:build integration && !race

package scanner

import (
	"testing"
	"time"
)

// The scanner's one timing assertion, and the build tag is what keeps it
// honest.
//
// The FR-IDX-003 / NFR-PRF-004 measurement is a *ratio* between two timings.
// Under the race detector both sides are dominated by shadow-memory bookkeeping
// rather than by the work being compared, so a speed-up asserted there measures
// the detector, not the scanner. `make test` always runs with -race and
// `make bench` runs `-run '^$'`, so while the guard was a runtime skip this test
// ran nowhere (test audit, 10-26).
//
// `integration` is the tag `make test-int` builds with, without -race, so that
// is where it runs now. That widens what the tag means — from "needs the real
// collection" to "slow suites kept out of make test" — and this test needs no
// SHELF_TEST_ROOT: it builds its own 500-archive library (bench_test.go). The
// deterministic half of the claim, that an unchanged library opens no archive,
// does not wait for this tag: incremental_test.go asserts it in every run.

// The measurement WP-08 is asked to make: cold versus incremental on the same
// tree, in one run, with a hard ratio assertion so a regression that quietly
// re-reads every archive fails the build instead of merely being slow.
func TestScan_incrementalRescan_isOrdersOfMagnitudeFasterThanCold(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a 500-archive, 60 000-page fixture")
	}
	// Shaped like the reference collection: 11 157 archives holding 1.36 M
	// entries is ~122 pages per archive, and it is the page rows — not the
	// central-directory reads — that dominate a cold scan. A fixture with
	// twenty-page archives would understate the incremental win by a factor of
	// several, which is the opposite of what a performance test is for.
	const (
		seriesCount    = 100
		booksPerSeries = 5
		pagesPerBook   = 120
	)
	root := syntheticLibrary(t, seriesCount, booksPerSeries, pagesPerBook)
	h := newHarnessAt(t, map[string]string{"manga": root})

	coldStart := time.Now()
	cold := h.run(Request{})
	coldDur := time.Since(coldStart)

	series, books, pages, skipped, errs := cold.Totals()
	if series != seriesCount || books != seriesCount*booksPerSeries || errs != 0 {
		t.Fatalf("cold scan: series %d books %d pages %d errors %d", series, books, pages, errs)
	}
	if skipped != 0 {
		t.Fatalf("cold scan skipped %d books", skipped)
	}

	h.lister.reset()
	warmStart := time.Now()
	warm := h.run(Request{})
	warmDur := time.Since(warmStart)

	_, warmBooks, _, warmSkipped, _ := warm.Totals()
	if warmSkipped != warmBooks || warmBooks != books {
		t.Fatalf("incremental scan skipped %d of %d books, want all %d", warmSkipped, warmBooks, books)
	}
	if got := h.lister.listedPaths(); len(got) != 0 {
		t.Fatalf("the incremental scan opened %d archives, want 0", len(got))
	}

	perThousandSeries := warmDur * time.Duration(1000) / time.Duration(seriesCount)
	t.Logf("cold: %v for %d series / %d archives / %d pages", coldDur, series, books, pages)
	t.Logf("incremental (no change): %v — extrapolated to 1 000 series: %v (NFR-PRF-004 budget 30s)",
		warmDur, perThousandSeries)
	t.Logf("speed-up: %.1fx", float64(coldDur)/float64(warmDur))

	if warmDur >= coldDur/4 {
		t.Errorf("incremental %v vs cold %v: FR-IDX-003 must make the no-change path dramatically cheaper, not marginally",
			warmDur, coldDur)
	}
	if perThousandSeries > 30*time.Second {
		t.Errorf("extrapolated incremental scan of 1 000 series is %v, over NFR-PRF-004's 30 s budget",
			perThousandSeries)
	}
}
