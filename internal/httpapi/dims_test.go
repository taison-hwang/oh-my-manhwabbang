package httpapi

import (
	"net/http"
	"slices"
	"sync"
	"testing"

	"shelf/internal/index"
	"shelf/internal/thumbs"
)

// thumbsSpy is the Thumbs the harness wires into the server: a pass-through
// decorator that records which books the HTTP layer asked to have measured.
//
// It embeds the interface rather than the concrete *thumbs.Service so every
// other method keeps its real behaviour and every golden file stays byte-identical.
type thumbsSpy struct {
	Thumbs

	mu       sync.Mutex
	ensured  []string
	measured map[string]int
	stats    *thumbs.Stats // non-nil once pinStats has been called
}

func (s *thumbsSpy) EnsureDims(bookID string) {
	s.mu.Lock()
	s.ensured = append(s.ensured, bookID)
	if s.measured == nil {
		s.measured = map[string]int{}
	}
	s.measured[bookID]++
	s.mu.Unlock()
	s.Thumbs.EnsureDims(bookID)
}

func (s *thumbsSpy) ensuredBooks() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.ensured)
}

// pinStats makes the spy report a fixed counter set instead of the live
// service's. It lives here with the rest of the decorator, but its one caller is
// the verbose-health test in api_test.go: the counters of a running service are
// all zero at rest and cannot distinguish a mirror that copies every field from
// one that transposes two of them or drops the last two. Pinned distinct values
// can.
func (s *thumbsSpy) pinStats(st thumbs.Stats) {
	s.mu.Lock()
	s.stats = &st
	s.mu.Unlock()
}

func (s *thumbsSpy) Stats() thumbs.Stats {
	s.mu.Lock()
	pinned := s.stats
	s.mu.Unlock()
	if pinned != nil {
		return *pinned
	}
	return s.Thumbs.Stats()
}

// FR-VWR-004 / arch §5.8. Opening a book schedules the background dimension
// pass unless every page is already measured.
//
// The regression: the guard used to read `dims_state == "none"`. Cover
// generation (FR-THM-003) measures page 1 for free, and index.refreshDimsState
// derives `partial` from "some widths are NULL" — so the cover book of every
// series sat in `partial` with 1 of N pages measured and could never be
// enqueued again. 양면 mode pairs pages only where both are measured, so on the
// most-opened book of each series it silently degraded to single-page for ever.
func TestGetBook_schedulesTheDimensionPassUntilEveryPageIsMeasured(t *testing.T) {
	t.Parallel()
	e := newEnv(t)

	// `partial` is precisely the state cover generation leaves behind. It must
	// be enqueued: the pass is what finishes the other N-1 pages.
	if rec := e.get("/api/books/" + e.bookZipID); rec.Code != http.StatusOK {
		t.Fatalf("GET the partial book = %d, want 200", rec.Code)
	}
	if got := e.dims.ensuredBooks(); !slices.Contains(got, e.bookZipID) {
		t.Errorf("opening a dims_state='partial' book did not schedule the pass; ensured %v", got)
	}

	// `none` has always been enqueued; it stays enqueued.
	if rec := e.get("/api/books/" + e.bookCloverID); rec.Code != http.StatusOK {
		t.Fatalf("GET the unmeasured book = %d, want 200", rec.Code)
	}
	if got := e.dims.ensuredBooks(); !slices.Contains(got, e.bookCloverID) {
		t.Errorf("opening a dims_state='none' book did not schedule the pass; ensured %v", got)
	}

	// A book with nothing to measure must NOT be enqueued: the queue is a
	// single low-priority goroutine and re-walking finished books would starve
	// the ones that still need work.
	ctx := t.Context()
	if err := e.idx.UpdateDims(ctx, e.bookDirID, []index.PageDims{
		{PageNo: 1, Width: 120, Height: 180},
		{PageNo: 2, Width: 120, Height: 180},
	}); err != nil {
		t.Fatalf("measuring the dir book: %v", err)
	}
	row, err := e.idx.GetBook(ctx, e.bookDirID)
	if err != nil {
		t.Fatalf("reading the dir book back: %v", err)
	}
	if row.DimsState != "done" {
		t.Fatalf("dims_state after measuring every page = %q, want %q", row.DimsState, "done")
	}
	before := len(e.dims.ensuredBooks())
	if rec := e.get("/api/books/" + e.bookDirID); rec.Code != http.StatusOK {
		t.Fatalf("GET the measured book = %d, want 200", rec.Code)
	}
	if got := e.dims.ensuredBooks(); len(got) != before {
		t.Errorf("opening a dims_state='done' book scheduled the pass again; ensured %v", got)
	}

	// A book that cannot be read has no page to measure either.
	before = len(e.dims.ensuredBooks())
	if rec := e.get("/api/books/" + e.bookBrokenID); rec.Code != http.StatusOK {
		t.Fatalf("GET the broken book = %d, want 200", rec.Code)
	}
	if got := e.dims.ensuredBooks(); len(got) != before {
		t.Errorf("opening a status='error' book scheduled the pass; ensured %v", got)
	}
}
