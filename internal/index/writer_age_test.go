package index_test

import (
	"fmt"
	"testing"
	"time"

	"shelf/internal/index"
)

// The age half of the commit cadence (impl-plan WP-03 acceptance 5: "every 200
// books or 2 s"). A batch far below BatchBooks stays private to the writer
// while it is younger than BatchAge, and a write landing after it has aged
// commits it — readers on the pool then see every row, with no Flush.
//
// Writer reads the wall clock, so the test cannot inject one. It uses the
// spec's 2 s and brackets every claim with measured time instead:
//
//   - "still young" is asserted only when the whole write-then-read took well
//     under BatchAge, measured from before the batch opened. The commit
//     decision is made inside the write, so a batch visible by then committed
//     while provably young. A check that ran too late to prove that fails —
//     loudly, with the reason — rather than skipping, so a premature commit
//     can never hide behind a slow runner.
//   - "has aged" waits BatchAge plus a margin past the latest instant the
//     batch could have opened, then writes.
func TestWriter_batching_commitsOnceTheBatchAges(t *testing.T) {
	t.Parallel()
	idx, _, _ := newDBs(t)
	ctx := t.Context()
	if err := idx.UpsertRoot(ctx, index.Root{Name: "manga", Path: "/m", Enabled: true}); err != nil {
		t.Fatalf("UpsertRoot: %v", err)
	}
	const (
		age      = 2 * time.Second
		youngMax = age / 2                // a "still young" check must finish inside this
		margin   = 300 * time.Millisecond // past BatchAge before the "has aged" write
	)
	sid := "s0000000000000000"[:16]
	book := func(i int) index.Book {
		return index.Book{
			ID: fmt.Sprintf("bk%014d", i), SeriesID: sid, RootName: "manga",
			RelPath: fmt.Sprintf("s/%d.zip", i), DisplayName: fmt.Sprintf("%d.zip", i),
			SortKey: sortKeyOf("x"), Ord: i, Kind: "zip", ContentVersion: "cv", Status: "ok", ScanGen: 1,
		}
	}
	visible := func() int {
		t.Helper()
		books, err := idx.ListBooks(ctx, sid)
		if err != nil {
			t.Fatalf("ListBooks: %v", err)
		}
		return len(books)
	}
	// assertYoung checks a batch opened no earlier than since is still pending.
	assertYoung := func(since time.Time, want int, what string) {
		t.Helper()
		n := visible()
		if took := time.Since(since); took >= youngMax {
			t.Fatalf("%s: write and read took %v, not under %v, so the batch cannot be proved young "+
				"against BatchAge %v; this run proves nothing either way", what, took, youngMax, age)
		}
		if n != want {
			t.Fatalf("%s: books visible = %d, want %d (the batch is younger than %v and far below BatchBooks)",
				what, n, want, age)
		}
	}

	w := idx.Writer(index.WriterOptions{BatchBooks: 1000, BatchAge: age})
	t.Cleanup(func() { _ = w.Close() })

	start := time.Now() // no later than the batch's open
	if err := w.UpsertSeries(ctx, index.Series{
		ID: sid, RootName: "manga", RelPath: "s", DisplayName: "s",
		SortKey: sortKeyOf("s"), Kind: "folder", Mtime: 1, AddedAt: 1, Status: "ok", ScanGen: 1,
	}); err != nil {
		t.Fatalf("UpsertSeries: %v", err)
	}
	opened := time.Now() // no earlier than the batch's open
	if err := w.UpsertBook(ctx, book(0)); err != nil {
		t.Fatalf("UpsertBook 0: %v", err)
	}
	assertYoung(start, 0, "first batch")

	time.Sleep(time.Until(opened.Add(age + margin)))
	if err := w.UpsertBook(ctx, book(1)); err != nil {
		t.Fatalf("UpsertBook 1: %v", err)
	}
	if n := visible(); n != 2 {
		t.Errorf("books visible after a write to a batch older than %v = %d, want 2 (it must have committed)", age, n)
	}

	// The commit restarts the clock: the next batch is young again.
	start = time.Now()
	if err := w.UpsertBook(ctx, book(2)); err != nil {
		t.Fatalf("UpsertBook 2: %v", err)
	}
	assertYoung(start, 2, "batch opened after the age commit")

	if err := w.Close(); err != nil {
		t.Fatalf("writer.Close: %v", err)
	}
	if n := visible(); n != 3 {
		t.Errorf("books visible after Close = %d, want 3", n)
	}
}
