package index_test

import (
	"fmt"
	"testing"
	"time"

	"shelf/internal/index"
)

// The age half of the commit cadence (impl-plan WP-03 acceptance 5: "every 200
// books or 2 s"). A batch far below BatchBooks stays private to the writer
// while it is younger than BatchAge, and a write landing once it has aged
// commits it — readers on the pool then see every row, with no Flush.
//
// The Writer's clock is replaced with a hand-moved one, so the boundary is
// exact: age-1ns is still young, age is old, and a commit restarts the clock.
func TestWriter_batching_commitsOnceTheBatchAges(t *testing.T) {
	t.Parallel()
	idx, _, _ := newDBs(t)
	ctx := t.Context()
	if err := idx.UpsertRoot(ctx, index.Root{Name: "manga", Path: "/m", Enabled: true}); err != nil {
		t.Fatalf("UpsertRoot: %v", err)
	}
	const age = 2 * time.Second
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
	clock := time.Unix(1_000_000, 0)
	w := idx.Writer(index.WriterOptions{BatchBooks: 1000, BatchAge: age})
	w.SetClock(func() time.Time { return clock })
	t.Cleanup(func() { _ = w.Close() })
	upsert := func(i int) {
		t.Helper()
		if err := w.UpsertBook(ctx, book(i)); err != nil {
			t.Fatalf("UpsertBook %d: %v", i, err)
		}
	}
	if err := w.UpsertSeries(ctx, index.Series{
		ID: sid, RootName: "manga", RelPath: "s", DisplayName: "s",
		SortKey: sortKeyOf("s"), Kind: "folder", Mtime: 1, AddedAt: 1, Status: "ok", ScanGen: 1,
	}); err != nil {
		t.Fatalf("UpsertSeries: %v", err)
	}
	upsert(0) // the batch opened at the series write; clock has not moved

	clock = clock.Add(age - time.Nanosecond)
	upsert(1)
	if n := visible(); n != 0 {
		t.Fatalf("books visible at age-1ns = %d, want 0 (the batch is still younger than %v)", n, age)
	}

	clock = clock.Add(time.Nanosecond)
	upsert(2)
	if n := visible(); n != 3 {
		t.Fatalf("books visible at exactly %v = %d, want 3 (the batch must have committed)", age, n)
	}

	// The commit restarts the clock: the next batch is young again.
	upsert(3)
	clock = clock.Add(age - time.Nanosecond)
	upsert(4)
	if n := visible(); n != 3 {
		t.Fatalf("books visible one ns short of a restarted batch age = %d, want 3", n)
	}

	if err := w.Close(); err != nil {
		t.Fatalf("writer.Close: %v", err)
	}
	if n := visible(); n != 5 {
		t.Errorf("books visible after Close = %d, want 5", n)
	}
}
