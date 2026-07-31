package scanner

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// The page budget of budget.go: arch §4.1 asks the pipeline to bound memory, and
// with a whole series per results slot the slot count cannot do that on its own.
// These tests pin the three properties the gate has to have — it blocks, it lets
// an oversized series through anyway, and it stops blocking when the run is
// cancelled — and then prove it is actually wired into a scan.

// The gate blocks a second acquire that would exceed the limit, and releases it
// the moment the writer gives the budget back.
func TestPageBudget_blocksUntilTheBudgetComesBack(t *testing.T) {
	t.Parallel()
	b := newPageBudget(10)
	ctx := t.Context()

	if !b.acquire(ctx, 6) {
		t.Fatal("the first acquire must succeed")
	}

	second := make(chan bool, 1)
	go func() { second <- b.acquire(ctx, 6) }()

	select {
	case <-second:
		t.Fatal("6 + 6 > 10: the second acquire must wait for the writer")
	case <-time.After(50 * time.Millisecond):
	}

	b.release(6)
	select {
	case ok := <-second:
		if !ok {
			t.Fatal("the second acquire failed after the budget was released")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the second acquire never woke after release")
	}

	held, peak, taken, waits := b.stats()
	if held != 6 || peak != 6 || taken != 12 {
		t.Errorf("held/peak/taken = %d/%d/%d, want 6/6/12", held, peak, taken)
	}
	if waits == 0 {
		t.Error("the blocked acquire was not counted as a wait")
	}
	b.release(6)
	if held, _, _, _ := b.stats(); held != 0 {
		t.Errorf("held = %d after every release, want 0", held)
	}
}

// The rule that makes the gate deadlock-free: a series larger than the whole
// budget is admitted whenever nothing else is in flight. A 1 540-page volume
// must never wedge the pipeline.
func TestPageBudget_admitsASeriesLargerThanTheWholeBudget(t *testing.T) {
	t.Parallel()
	b := newPageBudget(8)
	if !b.acquire(t.Context(), 5000) {
		t.Fatal("an oversized series must be admitted when nothing is in flight")
	}
	if held, peak, _, waits := b.stats(); held != 5000 || peak != 5000 || waits != 0 {
		t.Errorf("held/peak/waits = %d/%d/%d, want 5000/5000/0", held, peak, waits)
	}
	// ...and it is the only thing in flight until it is written.
	done := make(chan bool, 1)
	go func() { done <- b.acquire(t.Context(), 1) }()
	select {
	case <-done:
		t.Fatal("nothing else may be admitted while an oversized series is in flight")
	case <-time.After(50 * time.Millisecond):
	}
	b.release(5000)
	if ok := <-done; !ok {
		t.Fatal("the waiter failed after the oversized series was written")
	}
}

// A cancelled run must not leave a worker parked on the budget for ever, and a
// failed acquire must take nothing.
func TestPageBudget_acquireStopsWhenTheRunIsCancelled(t *testing.T) {
	t.Parallel()
	b := newPageBudget(4)
	if !b.acquire(t.Context(), 4) {
		t.Fatal("the first acquire must succeed")
	}

	ctx, cancel := context.WithCancel(t.Context())
	got := make(chan bool, 1)
	go func() { got <- b.acquire(ctx, 4) }()
	select {
	case <-got:
		t.Fatal("the second acquire must wait")
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	select {
	case ok := <-got:
		if ok {
			t.Fatal("acquire reported success on a cancelled context")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("acquire ignored the cancellation")
	}
	if held, _, taken, _ := b.stats(); held != 4 || taken != 4 {
		t.Errorf("held/taken = %d/%d after a cancelled acquire, want 4/4 — it must take nothing", held, taken)
	}
}

// The regression assertion the review asked for: the gate is wired into a real
// scan, every page row that reaches the writer is accounted for, every one is
// given back, and the high-water mark stays at the bound the gate promises —
// max(limit, the largest single series) — rather than growing with the library.
//
// With the budget set to 4 and every series holding 6 page rows, admission is
// only ever possible on the "nothing is in flight" rule, so the assertion below
// is an invariant of the gate and not a race the scheduler might hide.
func TestScan_pageBudget_boundsWhatThePipelineHolds(t *testing.T) {
	t.Parallel()
	const (
		seriesCount    = 12
		booksPerSeries = 2
		pagesPerBook   = 3
		seriesPages    = booksPerSeries * pagesPerBook
		limit          = 4
	)
	layout := map[string]any{}
	for s := range seriesCount {
		books := map[string]any{}
		for b := 1; b <= booksPerSeries; b++ {
			books[fmt.Sprintf("%02d권.zip", b)] = jpegZIP(t, "001.jpg", "002.jpg", "003.jpg")
		}
		layout[fmt.Sprintf("시리즈 %02d", s)] = books
	}
	h := newHarness(t, layout)
	h.scanner.pages = newPageBudget(limit)

	res := h.run(Request{})
	_, books, pages, _, errs := res.Totals()
	if books != seriesCount*booksPerSeries || pages != seriesCount*seriesPages || errs != 0 {
		t.Fatalf("scan under a tight page budget indexed books %d pages %d errors %d, want %d/%d/0",
			books, pages, errs, seriesCount*booksPerSeries, seriesCount*seriesPages)
	}

	held, peak, taken, _ := h.scanner.pages.stats()
	if taken != pages {
		t.Errorf("the budget accounted for %d page rows but the scan wrote %d — the gate is not on the path every result takes",
			taken, pages)
	}
	if held != 0 {
		t.Errorf("%d page slots were never returned: an acquire is missing its release", held)
	}
	if want := int64(max(limit, seriesPages)); peak > want {
		t.Errorf("peak in-flight page rows = %d, want at most %d — the bound has widened", peak, want)
	}
}

// maxInFlightPages is a memory budget, so it is stated in page rows and has to
// stay one. At roughly 200 bytes per materialised index.Page, 64 Ki rows is
// ~13 MiB of in-flight index; a bound loose enough to hold a whole cold scan of
// the reference collection (1.36 M entries) would be no bound at all, on a
// product whose primary deployment target is a NAS (NFR-OPS-003).
func TestMaxInFlightPages_staysAMemoryBound(t *testing.T) {
	t.Parallel()
	const ceiling = 128 << 10 // ~26 MiB of index.Page
	if maxInFlightPages > ceiling {
		t.Errorf("maxInFlightPages = %d, over the %d-row ceiling: at ~200 B per page row that is %d MiB held between the workers and the writer",
			maxInFlightPages, ceiling, maxInFlightPages*200/(1<<20))
	}
	if maxInFlightPages < 1024 {
		t.Errorf("maxInFlightPages = %d is so tight the pipeline serialises on it", maxInFlightPages)
	}
}
