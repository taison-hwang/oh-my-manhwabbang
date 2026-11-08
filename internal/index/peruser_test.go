package index_test

import (
	"context"
	"testing"

	"shelf/internal/index"
	"shelf/internal/principal"
	"shelf/internal/userdata"
)

func userCtx(sub string) context.Context {
	return principal.With(context.Background(), principal.Principal{Sub: sub})
}

func putAs(t *testing.T, u *userdata.DB, ctx context.Context, bookID, seriesID string, page, count int) {
	t.Helper()
	if _, err := u.PutProgress(ctx, userdata.ProgressUpdate{
		BookID: bookID, SeriesID: seriesID, RootName: "manga", BookPath: bookID + ".zip",
		Page: page, PageCount: count,
	}); err != nil {
		t.Fatalf("PutProgress %s: %v", bookID, err)
	}
}

// Every query that reads reading progress sees the requesting user's rows and
// nobody else's (ruling E-74 §2.13): the book join, the series rollup and its
// filters, the continue shelf and the post-scan orphan lookups.
func TestPerUser_progressReadsAreScopedToThePrincipal(t *testing.T) {
	idx, user, _ := newDBs(t)
	splitFixture(t, idx)
	alice, bob, legacy := userCtx("alice"), userCtx("bob"), context.Background()

	putAs(t, user, alice, "book-plain", "ser-plain", 2, 7)
	putAs(t, user, legacy, "vol-a", "ser-split", 3, 10)

	// GetBook / ListBooks
	if r, err := idx.GetBook(alice, "book-plain"); err != nil || r.Progress == nil || r.Progress.LastPage != 2 {
		t.Fatalf("alice GetBook = %+v, %v", r.Progress, err)
	}
	if r, err := idx.GetBook(bob, "book-plain"); err != nil || r.Progress != nil {
		t.Fatalf("bob GetBook leaks %+v (%v)", r.Progress, err)
	}
	if r, _ := idx.GetBook(legacy, "vol-a"); r.Progress == nil {
		t.Fatal("the single-user reader lost their progress")
	}
	if r, _ := idx.GetBook(alice, "vol-a"); r.Progress != nil {
		t.Fatalf("alice sees the single-user progress: %+v", r.Progress)
	}
	books, err := idx.ListBooks(bob, "ser-plain")
	if err != nil || len(books) != 1 || books[0].Progress != nil {
		t.Fatalf("bob ListBooks = %+v, %v", books, err)
	}

	// ListSeries rollup, filters and sort
	la, err := idx.ListSeries(alice, index.SeriesFilter{Progress: index.ProgressReading})
	if err != nil || la.Total != 1 || la.Items[0].ID != "ser-plain" {
		t.Fatalf("alice reading = %+v, %v", la, err)
	}
	lb, err := idx.ListSeries(bob, index.SeriesFilter{Progress: index.ProgressReading})
	if err != nil || lb.Total != 0 {
		t.Fatalf("bob reading = %+v, %v", lb, err)
	}
	all, _ := idx.ListSeries(bob, index.SeriesFilter{Sort: index.SortRecent})
	for _, s := range all.Items {
		if s.Progress.LastReadAt != nil || s.Progress.BooksStarted != 0 {
			t.Fatalf("bob's rollup for %s leaks: %+v", s.ID, s.Progress)
		}
	}
	if sd, _ := idx.GetSeries(alice, "ser-plain"); sd.Progress.BooksStarted != 1 {
		t.Fatalf("alice GetSeries rollup = %+v", sd.Progress)
	}
	if sd, _ := idx.GetSeries(bob, "ser-plain"); sd.Progress.BooksStarted != 0 {
		t.Fatalf("bob GetSeries rollup = %+v", sd.Progress)
	}

	// ListContinue
	if c, err := idx.ListContinue(alice, 10); err != nil || len(c) != 1 || c[0].Book.ID != "book-plain" {
		t.Fatalf("alice continue = %+v, %v", c, err)
	}
	if c, err := idx.ListContinue(bob, 10); err != nil || len(c) != 0 {
		t.Fatalf("bob continue = %+v, %v", c, err)
	}
	if c, err := idx.ListContinue(legacy, 10); err != nil || len(c) != 1 || c[0].Book.ID != "vol-a" {
		t.Fatalf("single-user continue = %+v, %v", c, err)
	}
}

func TestPerUser_orphanLookupsAreScopedToThePrincipal(t *testing.T) {
	idx, user, _ := newDBs(t)
	splitFixture(t, idx)
	alice, bob, legacy := userCtx("alice"), userCtx("bob"), context.Background()

	// A row nobody can resolve: alice's and the single-user reader's.
	putAs(t, user, alice, "gone-alice", "ser-x", 1, 5)
	putAs(t, user, legacy, "gone-legacy", "ser-x", 1, 5)
	// A row filed under the wrong series: bob's only.
	putAs(t, user, bob, "book-plain", "ser-wrong", 1, 7)

	ids := func(os []index.SplitOrphan) []string {
		var out []string
		for _, o := range os {
			out = append(out, o.BookID)
		}
		return out
	}
	if o, err := idx.SplitOrphans(alice); err != nil || len(o) != 1 || o[0].BookID != "gone-alice" {
		t.Fatalf("alice SplitOrphans = %v, %v", ids(o), err)
	}
	if o, err := idx.SplitOrphans(bob); err != nil || len(o) != 0 {
		t.Fatalf("bob SplitOrphans = %v, %v", ids(o), err)
	}
	if o, err := idx.SplitOrphans(legacy); err != nil || len(o) != 1 || o[0].BookID != "gone-legacy" {
		t.Fatalf("single-user SplitOrphans = %v, %v", ids(o), err)
	}

	if v, err := idx.VanishedProgress(alice, []string{"manga"}); err != nil || len(v) != 1 || v[0].BookID != "gone-alice" {
		t.Fatalf("alice VanishedProgress = %+v, %v", v, err)
	}
	if v, err := idx.VanishedProgress(bob, []string{"manga"}); err != nil || len(v) != 0 {
		t.Fatalf("bob VanishedProgress = %+v, %v", v, err)
	}

	if m, err := idx.MisfiledProgress(bob); err != nil || len(m) != 1 || m[0].BookID != "book-plain" {
		t.Fatalf("bob MisfiledProgress = %+v, %v", m, err)
	}
	if m, err := idx.MisfiledProgress(alice); err != nil || len(m) != 0 {
		t.Fatalf("alice MisfiledProgress = %+v, %v", m, err)
	}
	if m, err := idx.MisfiledProgress(legacy); err != nil || len(m) != 0 {
		t.Fatalf("single-user MisfiledProgress = %+v, %v", m, err)
	}
}
