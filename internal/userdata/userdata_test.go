package userdata_test

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"shelf/internal/userdata"
)

// ---------------------------------------------------------------- fixtures --

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// testClock drives the Unix-second timestamps this package writes. Sleeping for
// a second per assertion would make the suite unusable, and `updated_at` is what
// the merge rules turn on, so the clock is injected instead.
type testClock struct {
	mu sync.Mutex
	at time.Time
}

func newClock() *testClock { return &testClock{at: time.Unix(1_700_000_000, 0).UTC()} }

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

func newDB(t *testing.T) (*userdata.DB, *testClock, string) {
	t.Helper()
	dir := t.TempDir()
	clk := newClock()
	return openIn(t, dir, clk), clk, dir
}

func openIn(t *testing.T, dir string, clk *testClock) *userdata.DB {
	t.Helper()
	opts := userdata.Options{Path: filepath.Join(dir, "user.db"), Logger: quietLogger()}
	if clk != nil {
		opts.Now = clk.Now
	}
	db, err := userdata.Open(t.Context(), opts)
	if err != nil {
		t.Fatalf("userdata.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func update(bookID, seriesID string, page, count int) userdata.ProgressUpdate {
	return userdata.ProgressUpdate{
		BookID: bookID, SeriesID: seriesID, RootName: "manga",
		BookPath: "series/" + bookID + ".zip", Page: page, PageCount: count,
	}
}

// ------------------------------------------------------------------ schema --

func TestOpen_freshDirectory_appliesSchemaInWALMode(t *testing.T) {
	t.Parallel()
	db, _, dir := newDB(t)

	if mode := journalMode(t, filepath.Join(dir, "user.db")); !strings.EqualFold(mode, "wal") {
		t.Errorf("journal_mode = %q, want wal (NFR-DAT-003)", mode)
	}
	if _, err := db.PutProgress(t.Context(), update("b1", "s1", 1, 10)); err != nil {
		t.Fatalf("PutProgress: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "user.db-wal")); err != nil {
		t.Errorf("user.db-wal not present after a write: %v", err)
	}
	if v, err := db.SchemaVersion(t.Context()); err != nil || v != 2 {
		t.Errorf("schema version = %d (%v), want 2 (amendment A-8 added series_seen)", v, err)
	}
	if v, ok, _ := db.Meta(t.Context(), "id_version"); !ok || v != userdata.IDVersion {
		t.Errorf("meta id_version = %q (present %v), want %q", v, ok, userdata.IDVersion)
	}
}

// The columns of arch-backend §3.6, pinned. Dropping one silently changes what
// survives a rebuild, which is the single property this file exists to protect.
func TestOpen_schema_matchesArchDDL(t *testing.T) {
	t.Parallel()
	_, _, dir := newDB(t)
	raw := openRaw(t, filepath.Join(dir, "user.db"))

	want := map[string][]string{
		"meta":       {"key", "value"},
		"progress":   {"book_id", "series_id", "root_name", "book_path", "last_page", "page_count", "completed", "started_at", "updated_at"},
		"book_prefs": {"book_id", "reading_dir", "display_mode", "fit_mode", "updated_at"},
		"settings":   {"key", "value", "updated_at"},
		"view_state": {"key", "value", "updated_at"},
		// Amendment A-8, schema version 2.
		"series_seen": {"series_id", "root_name", "series_path", "first_seen_at"},
	}
	for table, cols := range want {
		if got := tableColumns(t, raw, table); !equalStrings(got, cols) {
			t.Errorf("table %s columns = %v, want %v", table, got, cols)
		}
	}
	for _, ix := range []string{"ix_progress_updated", "ix_progress_series", "ix_progress_continue",
		"ix_series_seen_first"} {
		var n int
		if err := raw.QueryRow(
			`SELECT count(*) FROM sqlite_master WHERE type='index' AND name=?`, ix).Scan(&n); err != nil {
			t.Fatalf("looking up %s: %v", ix, err)
		}
		if n != 1 {
			t.Errorf("index %s is missing", ix)
		}
	}

	// No foreign keys into index.db (NFR-DAT-004): a row may reference a book
	// that does not exist right now.
	for _, table := range []string{"progress", "book_prefs", "series_seen"} {
		var n int
		if err := raw.QueryRow(
			`SELECT count(*) FROM pragma_foreign_key_list(?)`, table).Scan(&n); err != nil {
			t.Fatalf("reading foreign keys of %s: %v", table, err)
		}
		if n != 0 {
			t.Errorf("table %s declares %d foreign keys, want 0", table, n)
		}
	}
}

func TestOpen_futureSchemaVersion_isRefused(t *testing.T) {
	t.Parallel()
	db, _, dir := newDB(t)
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	raw := openRaw(t, filepath.Join(dir, "user.db"))
	if _, err := raw.Exec(`UPDATE meta SET value = '42' WHERE key = 'schema_version'`); err != nil {
		t.Fatalf("forging schema version: %v", err)
	}
	_ = raw.Close()

	_, err := userdata.Open(t.Context(), userdata.Options{
		Path: filepath.Join(dir, "user.db"), Logger: quietLogger()})
	if !errors.Is(err, userdata.ErrSchemaTooNew) {
		t.Fatalf("Open on a v42 user.db = %v, want ErrSchemaTooNew", err)
	}
	if !strings.Contains(err.Error(), "42") {
		t.Errorf("error %q does not name the offending version", err)
	}
}

// index.db rebuilds itself when the id scheme changes. user.db must NOT: the ids
// are the only link between authored rows and books, so a mismatch stops the
// process instead of destroying data.
func TestOpen_foreignIDVersion_isRefusedNotRebuilt(t *testing.T) {
	t.Parallel()
	db, _, dir := newDB(t)
	if _, err := db.PutProgress(t.Context(), update("b1", "s1", 4, 10)); err != nil {
		t.Fatalf("PutProgress: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	raw := openRaw(t, filepath.Join(dir, "user.db"))
	if _, err := raw.Exec(`UPDATE meta SET value = 'shelf-id/9' WHERE key = 'id_version'`); err != nil {
		t.Fatalf("forging id version: %v", err)
	}
	_ = raw.Close()

	_, err := userdata.Open(t.Context(), userdata.Options{
		Path: filepath.Join(dir, "user.db"), Logger: quietLogger()})
	if !errors.Is(err, userdata.ErrIDVersionMismatch) {
		t.Fatalf("Open with a foreign id scheme = %v, want ErrIDVersionMismatch", err)
	}

	// And the row is still there — nothing was dropped on the way out.
	raw2 := openRaw(t, filepath.Join(dir, "user.db"))
	var n int
	if err := raw2.QueryRow(`SELECT count(*) FROM progress`).Scan(&n); err != nil {
		t.Fatalf("counting progress: %v", err)
	}
	if n != 1 {
		t.Errorf("progress rows after a refused open = %d, want 1", n)
	}
}

func TestOpen_corruptFile_isCleanError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	garbage := make([]byte, 4096)
	for i := range garbage {
		garbage[i] = byte(i) ^ 0x33
	}
	if err := os.WriteFile(filepath.Join(dir, "user.db"), garbage, 0o600); err != nil {
		t.Fatalf("writing garbage: %v", err)
	}
	_, err := userdata.Open(t.Context(), userdata.Options{
		Path: filepath.Join(dir, "user.db"), Logger: quietLogger()})
	if err == nil {
		t.Fatal("Open on a corrupt file returned no error")
	}
}

func TestOpen_reopen_keepsEverything(t *testing.T) {
	t.Parallel()
	db, _, dir := newDB(t)
	ctx := t.Context()
	if _, err := db.PutProgress(ctx, update("b1", "s1", 7, 20)); err != nil {
		t.Fatalf("PutProgress: %v", err)
	}
	if _, err := db.PutPrefs(ctx, "b1", userdata.PrefsPatch{
		FitMode: userdata.SetPatch("height")}); err != nil {
		t.Fatalf("PutPrefs: %v", err)
	}
	if err := db.Settings().Put(ctx, "prefetch", "4"); err != nil {
		t.Fatalf("Settings.Put: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	db2 := openIn(t, dir, newClock())
	p, err := db2.GetProgress(ctx, "b1")
	if err != nil || p.LastPage != 7 {
		t.Errorf("progress after reopen = %+v (%v)", p, err)
	}
	pr, err := db2.GetPrefs(ctx, "b1")
	if err != nil || pr.FitMode == nil || *pr.FitMode != "height" {
		t.Errorf("prefs after reopen = %+v (%v)", pr, err)
	}
	if v, ok, _ := db2.Settings().Get(ctx, "prefetch"); !ok || v != "4" {
		t.Errorf("setting after reopen = %q (present %v)", v, ok)
	}
}

// ---------------------------------------------------------------- progress --

func TestPutProgress_clampsPageAndDerivesCompletion(t *testing.T) {
	t.Parallel()
	db, _, _ := newDB(t)
	ctx := t.Context()

	yes, no := true, false
	tests := []struct {
		name          string
		page, count   int
		completed     *bool
		wantPage      int
		wantCompleted bool
	}{
		{"page below 1 clamps up", 0, 10, nil, 1, false},
		{"negative page clamps up", -5, 10, nil, 1, false},
		{"page past the end clamps down", 99, 10, nil, 10, true},
		{"last page auto-completes (FR-VWR-012)", 10, 10, nil, 10, true},
		{"middle page does not complete", 5, 10, nil, 5, false},
		{"explicit true wins over the middle", 5, 10, &yes, 5, true},
		{"explicit false wins over the last page", 10, 10, &no, 10, false},
		{"unknown page count does not auto-complete", 3, 0, nil, 3, false},
	}
	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			bookID := fmt.Sprintf("book%02d", i)
			u := update(bookID, "s1", tc.page, tc.count)
			u.Completed = tc.completed
			got, err := db.PutProgress(ctx, u)
			if err != nil {
				t.Fatalf("PutProgress: %v", err)
			}
			if got.LastPage != tc.wantPage || got.Completed != tc.wantCompleted {
				t.Errorf("progress = page %d completed %v, want page %d completed %v",
					got.LastPage, got.Completed, tc.wantPage, tc.wantCompleted)
			}
		})
	}
}

func TestPutProgress_isIdempotentAndKeepsStartedAt(t *testing.T) {
	t.Parallel()
	db, clk, _ := newDB(t)
	ctx := t.Context()

	first, err := db.PutProgress(ctx, update("b1", "s1", 2, 10))
	if err != nil {
		t.Fatalf("PutProgress: %v", err)
	}
	clk.Advance(90 * time.Second)
	second, err := db.PutProgress(ctx, update("b1", "s1", 3, 10))
	if err != nil {
		t.Fatalf("PutProgress: %v", err)
	}

	if second.StartedAt != first.StartedAt {
		t.Errorf("started_at moved from %d to %d; it must record the first opening",
			first.StartedAt, second.StartedAt)
	}
	if second.UpdatedAt != first.UpdatedAt+90 {
		t.Errorf("updated_at = %d, want %d", second.UpdatedAt, first.UpdatedAt+90)
	}

	again, err := db.PutProgress(ctx, update("b1", "s1", 3, 10))
	if err != nil {
		t.Fatalf("PutProgress (repeat): %v", err)
	}
	if again != second {
		t.Errorf("repeating the same write changed the row: %+v vs %+v", again, second)
	}
	if n, _ := db.CountProgress(ctx); n != 1 {
		t.Errorf("progress rows = %d, want 1", n)
	}
}

// Ruling E-45 — `page_count` is a baseline, not a measurement of this write.
// The "the file changed" hint is *derived* from it (arch §3.4, §7.3), so an
// ordinary page turn must not be able to erase it: a write that rebaselined
// without the reader's acknowledgement would destroy the only evidence the hint
// is computed from, and the hint could never fire again.
func TestPutProgress_keepsThePageCountBaselineUntilAcknowledged(t *testing.T) {
	t.Parallel()
	db, _, _ := newDB(t)
	ctx := t.Context()

	// A first write has no baseline to protect, so the INSERT records the length
	// it was given.
	first, err := db.PutProgress(ctx, update("b1", "s1", 2, 10))
	if err != nil {
		t.Fatalf("PutProgress: %v", err)
	}
	if first.PageCount != 10 {
		t.Fatalf("the first write recorded page_count %d, want 10", first.PageCount)
	}

	// The file then grew to 190 pages under the reader. Every unacknowledged
	// write after that keeps the 10 the reader actually agreed to.
	for i, page := range []int{3, 4, 5} {
		got, err := db.PutProgress(ctx, update("b1", "s1", page, 190))
		if err != nil {
			t.Fatalf("PutProgress %d: %v", i, err)
		}
		if got.PageCount != 10 {
			t.Fatalf("unacknowledged write %d moved the baseline to %d; want the recorded 10",
				i, got.PageCount)
		}
	}

	// What is preserved is the recorded column, not the number the write
	// computes with: the clamp and the auto-complete rule still use the length
	// the caller passed, so the reader can reach page 190 of a book that now has
	// 190 pages (E-45 §3).
	far, err := db.PutProgress(ctx, update("b1", "s1", 190, 190))
	if err != nil {
		t.Fatalf("PutProgress (far page): %v", err)
	}
	if far.LastPage != 190 || !far.Completed {
		t.Errorf("progress = page %d completed %v, want page 190 completed true — the clamp "+
			"and the auto-complete rule use the current length, not the baseline",
			far.LastPage, far.Completed)
	}
	if far.PageCount != 10 {
		t.Errorf("baseline = %d after reaching the new last page, want 10", far.PageCount)
	}

	// The acknowledgement — and only the acknowledgement — rebaselines.
	ack := update("b1", "s1", 6, 190)
	ack.StaleSeen = true
	seen, err := db.PutProgress(ctx, ack)
	if err != nil {
		t.Fatalf("PutProgress (acknowledged): %v", err)
	}
	if seen.PageCount != 190 {
		t.Fatalf("an acknowledged write left the baseline at %d, want 190", seen.PageCount)
	}

	// ...and the new baseline is then held just as firmly as the old one.
	after, err := db.PutProgress(ctx, update("b1", "s1", 7, 190))
	if err != nil {
		t.Fatalf("PutProgress (after the acknowledgement): %v", err)
	}
	if after.PageCount != 190 {
		t.Errorf("baseline = %d after the acknowledgement, want the acknowledged 190",
			after.PageCount)
	}
	if after.StartedAt != first.StartedAt {
		t.Errorf("started_at moved from %d to %d", first.StartedAt, after.StartedAt)
	}
	if n, _ := db.CountProgress(ctx); n != 1 {
		t.Errorf("progress rows = %d, want 1", n)
	}
}

// Ruling E-45 §2 — an acknowledgement of an UNKNOWN length is not an
// acknowledgement. A broken file leaves the index at page_count 0
// (scanner.bookFailure), and storing that 0 as the baseline would be permanent:
// a recorded 0 is never stale, so no repaired length would ever be compared
// against it again. The reader saw "the file changed", not "this book is 0 pages
// long", and only the second would justify writing it.
func TestPutProgress_acknowledgementWithAnUnknownLengthKeepsTheBaseline(t *testing.T) {
	t.Parallel()
	db, _, _ := newDB(t)
	ctx := t.Context()

	if _, err := db.PutProgress(ctx, update("b1", "s1", 99, 99)); err != nil {
		t.Fatalf("PutProgress: %v", err)
	}

	broke := update("b1", "s1", 1, 0)
	broke.StaleSeen = true
	got, err := db.PutProgress(ctx, broke)
	if err != nil {
		t.Fatalf("PutProgress (acknowledged while broken): %v", err)
	}
	if got.PageCount != 99 {
		t.Fatalf("the baseline moved to %d; an unknown length cannot be acknowledged",
			got.PageCount)
	}

	// Repaired to a length that is not the old one: the surviving baseline is
	// what lets the two be compared at all, which is what `stale` is derived
	// from one layer up.
	repaired, err := db.PutProgress(ctx, update("b1", "s1", 3, 7))
	if err != nil {
		t.Fatalf("PutProgress (repaired): %v", err)
	}
	if repaired.PageCount != 99 {
		t.Errorf("baseline = %d after the repair, want the recorded 99", repaired.PageCount)
	}

	// ...and with a length to agree to, the acknowledgement lands.
	ack := update("b1", "s1", 3, 7)
	ack.StaleSeen = true
	seen, err := db.PutProgress(ctx, ack)
	if err != nil {
		t.Fatalf("PutProgress (acknowledged after the repair): %v", err)
	}
	if seen.PageCount != 7 {
		t.Errorf("baseline = %d after acknowledging a known length, want 7", seen.PageCount)
	}
}

func TestProgress_missingAndDeleted(t *testing.T) {
	t.Parallel()
	db, _, _ := newDB(t)
	ctx := t.Context()

	if _, err := db.GetProgress(ctx, "never-opened"); !errors.Is(err, userdata.ErrNotFound) {
		t.Errorf("GetProgress(unknown) = %v, want ErrNotFound", err)
	}
	if err := db.DeleteProgress(ctx, "never-opened"); err != nil {
		t.Errorf("DeleteProgress(unknown) = %v, want nil", err)
	}
	if _, err := db.PutProgress(ctx, update("b1", "s1", 1, 5)); err != nil {
		t.Fatalf("PutProgress: %v", err)
	}
	if err := db.DeleteProgress(ctx, "b1"); err != nil {
		t.Fatalf("DeleteProgress: %v", err)
	}
	if _, err := db.GetProgress(ctx, "b1"); !errors.Is(err, userdata.ErrNotFound) {
		t.Errorf("GetProgress after delete = %v, want ErrNotFound", err)
	}
}

func TestPutProgress_rejectsEmptyBookID(t *testing.T) {
	t.Parallel()
	db, _, _ := newDB(t)
	if _, err := db.PutProgress(t.Context(), update("", "s1", 1, 5)); !errors.Is(err, userdata.ErrInvalidArgument) {
		t.Errorf("PutProgress with no book id = %v, want ErrInvalidArgument", err)
	}
}

func TestSeriesAggregates_rollUpPerSeries(t *testing.T) {
	t.Parallel()
	db, clk, _ := newDB(t)
	ctx := t.Context()

	done := true
	for _, u := range []userdata.ProgressUpdate{
		update("b1", "s1", 5, 5),
		update("b2", "s1", 2, 9),
		update("b3", "s2", 4, 4),
	} {
		if u.BookID == "b1" || u.BookID == "b3" {
			u.Completed = &done
		}
		if _, err := db.PutProgress(ctx, u); err != nil {
			t.Fatalf("PutProgress %s: %v", u.BookID, err)
		}
		clk.Advance(time.Minute)
	}

	agg, err := db.SeriesAggregates(ctx, nil)
	if err != nil {
		t.Fatalf("SeriesAggregates: %v", err)
	}
	s1 := agg["s1"]
	if s1.BooksCompleted != 1 || s1.BooksStarted != 1 {
		t.Errorf("s1 = %d completed, %d started, want 1 and 1", s1.BooksCompleted, s1.BooksStarted)
	}
	if s1.LastBookID != "b2" {
		t.Errorf("s1 last book = %q, want b2 (the most recently updated)", s1.LastBookID)
	}
	if s1.LastPage != 2 {
		t.Errorf("s1 last page = %d, want 2", s1.LastPage)
	}
	if agg["s2"].BooksCompleted != 1 {
		t.Errorf("s2 completed = %d, want 1", agg["s2"].BooksCompleted)
	}

	only, err := db.SeriesAggregates(ctx, []string{"s2"})
	if err != nil {
		t.Fatalf("SeriesAggregates(s2): %v", err)
	}
	if len(only) != 1 {
		t.Errorf("filtered aggregate returned %d series, want 1", len(only))
	}
}

func TestContinue_excludesCompletedAndOrdersByRecency(t *testing.T) {
	t.Parallel()
	db, clk, _ := newDB(t)
	ctx := t.Context()

	if _, err := db.PutProgress(ctx, update("old", "s1", 2, 10)); err != nil {
		t.Fatalf("PutProgress: %v", err)
	}
	clk.Advance(time.Hour)
	if _, err := db.PutProgress(ctx, update("new", "s1", 3, 10)); err != nil {
		t.Fatalf("PutProgress: %v", err)
	}
	clk.Advance(time.Hour)
	if _, err := db.PutProgress(ctx, update("finished", "s2", 10, 10)); err != nil {
		t.Fatalf("PutProgress: %v", err)
	}

	got, err := db.Continue(ctx, 10)
	if err != nil {
		t.Fatalf("Continue: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("continue items = %d, want 2 (the finished book is excluded)", len(got))
	}
	if got[0].BookID != "new" || got[1].BookID != "old" {
		t.Errorf("order = %s, %s; want the most recently read first", got[0].BookID, got[1].BookID)
	}

	limited, err := db.Continue(ctx, 1)
	if err != nil {
		t.Fatalf("Continue(1): %v", err)
	}
	if len(limited) != 1 || limited[0].BookID != "new" {
		t.Errorf("limited continue = %+v", limited)
	}
}

// ------------------------------------------------------------------- prefs --

func TestPrefs_threeStatePatch(t *testing.T) {
	t.Parallel()
	db, _, _ := newDB(t)
	ctx := t.Context()

	// Nothing stored: every field inherits.
	got, err := db.GetPrefs(ctx, "b1")
	if err != nil {
		t.Fatalf("GetPrefs: %v", err)
	}
	if got.ReadingDir != nil || got.DisplayMode != nil || got.FitMode != nil {
		t.Fatalf("unset prefs = %+v, want every field nil", got)
	}

	// Set two overrides.
	got, err = db.PutPrefs(ctx, "b1", userdata.PrefsPatch{
		ReadingDir:  userdata.SetPatch("rtl"),
		DisplayMode: userdata.SetPatch("spread"),
	})
	if err != nil {
		t.Fatalf("PutPrefs: %v", err)
	}
	if got.ReadingDir == nil || *got.ReadingDir != "rtl" ||
		got.DisplayMode == nil || *got.DisplayMode != "spread" || got.FitMode != nil {
		t.Fatalf("after set = %+v", got)
	}

	// An absent field must not disturb the stored value.
	got, err = db.PutPrefs(ctx, "b1", userdata.PrefsPatch{FitMode: userdata.SetPatch("contain")})
	if err != nil {
		t.Fatalf("PutPrefs: %v", err)
	}
	if got.ReadingDir == nil || *got.ReadingDir != "rtl" {
		t.Errorf("an absent field cleared reading_dir: %+v", got)
	}
	if got.FitMode == nil || *got.FitMode != "contain" {
		t.Errorf("fit mode = %v, want contain", got.FitMode)
	}

	// An explicit null clears exactly one override.
	got, err = db.PutPrefs(ctx, "b1", userdata.PrefsPatch{ReadingDir: userdata.ClearPatch[string]()})
	if err != nil {
		t.Fatalf("PutPrefs: %v", err)
	}
	if got.ReadingDir != nil {
		t.Errorf("explicit null did not clear reading_dir: %v", *got.ReadingDir)
	}
	if got.DisplayMode == nil || *got.DisplayMode != "spread" {
		t.Errorf("clearing one field disturbed another: %+v", got)
	}

	// Clearing the last override removes the row entirely.
	got, err = db.PutPrefs(ctx, "b1", userdata.PrefsPatch{
		DisplayMode: userdata.ClearPatch[string](),
		FitMode:     userdata.ClearPatch[string](),
	})
	if err != nil {
		t.Fatalf("PutPrefs: %v", err)
	}
	if got.ReadingDir != nil || got.DisplayMode != nil || got.FitMode != nil {
		t.Errorf("after clearing everything = %+v", got)
	}
	rows, err := db.ListPrefs(ctx)
	if err != nil {
		t.Fatalf("ListPrefs: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("prefs rows after a full clear = %d, want 0", len(rows))
	}
}

func TestPrefs_rejectsValuesOutsideTheFrozenEnums(t *testing.T) {
	t.Parallel()
	db, _, _ := newDB(t)
	ctx := t.Context()

	// C-1 and C-2: the wire values are "spread" and "contain"; the UI labels
	// 양면 / 화면 and the rejected "double" / "screen" must not reach storage.
	for _, patch := range []userdata.PrefsPatch{
		{ReadingDir: userdata.SetPatch("sideways")},
		{DisplayMode: userdata.SetPatch("double")},
		{FitMode: userdata.SetPatch("screen")},
	} {
		if _, err := db.PutPrefs(ctx, "b1", patch); !errors.Is(err, userdata.ErrInvalidArgument) {
			t.Errorf("PutPrefs(%+v) = %v, want ErrInvalidArgument", patch, err)
		}
	}
	if _, err := db.PutPrefs(ctx, "", userdata.PrefsPatch{}); !errors.Is(err, userdata.ErrInvalidArgument) {
		t.Errorf("PutPrefs with no book id = %v, want ErrInvalidArgument", err)
	}
	// A rejected patch writes nothing.
	if rows, _ := db.ListPrefs(ctx); len(rows) != 0 {
		t.Errorf("rejected patches left %d rows behind", len(rows))
	}
}

// ---------------------------------------------------------------- settings --

func TestSettingsAndViewState_areIndependentStores(t *testing.T) {
	t.Parallel()
	db, clk, _ := newDB(t)
	ctx := t.Context()

	if err := db.Settings().PutAll(ctx, map[string]string{
		"theme":         `"dark"`,
		"prefetch":      "4",
		"library_scope": `"reading"`, // A-5
	}); err != nil {
		t.Fatalf("Settings.PutAll: %v", err)
	}
	if err := db.ViewState().Put(ctx, "library_view", `"grid"`); err != nil {
		t.Fatalf("ViewState.Put: %v", err)
	}

	all, err := db.Settings().All(ctx)
	if err != nil {
		t.Fatalf("Settings.All: %v", err)
	}
	if len(all) != 3 || all["theme"] != `"dark"` {
		t.Errorf("settings = %v", all)
	}
	if _, ok, _ := db.Settings().Get(ctx, "library_view"); ok {
		t.Error("view_state key leaked into settings")
	}
	if v, ok, _ := db.ViewState().Get(ctx, "library_view"); !ok || v != `"grid"` {
		t.Errorf("view state = %q (present %v)", v, ok)
	}

	// A partial write touches only the keys it names.
	clk.Advance(time.Minute)
	if err := db.Settings().Put(ctx, "theme", `"light"`); err != nil {
		t.Fatalf("Settings.Put: %v", err)
	}
	all, _ = db.Settings().All(ctx)
	if all["theme"] != `"light"` || all["prefetch"] != "4" {
		t.Errorf("partial write clobbered other keys: %v", all)
	}

	if err := db.Settings().Delete(ctx, "theme"); err != nil {
		t.Fatalf("Settings.Delete: %v", err)
	}
	if _, ok, _ := db.Settings().Get(ctx, "theme"); ok {
		t.Error("deleted setting is still present")
	}
	if err := db.Settings().Delete(ctx, "never-set"); err != nil {
		t.Errorf("deleting an absent key = %v, want nil", err)
	}
	if err := db.Settings().PutAll(ctx, map[string]string{"": "x"}); !errors.Is(err, userdata.ErrInvalidArgument) {
		t.Errorf("empty key = %v, want ErrInvalidArgument", err)
	}
}

// ----------------------------------------------------------- export/import --

func TestExport_roundTripsThroughImport(t *testing.T) {
	t.Parallel()
	src, clk, _ := newDB(t)
	ctx := t.Context()

	done := true
	u := update("b1", "s1", 5, 5)
	u.Completed = &done
	if _, err := src.PutProgress(ctx, u); err != nil {
		t.Fatalf("PutProgress: %v", err)
	}
	clk.Advance(time.Minute)
	if _, err := src.PutProgress(ctx, update("b2", "s1", 3, 12)); err != nil {
		t.Fatalf("PutProgress: %v", err)
	}
	if _, err := src.PutPrefs(ctx, "b2", userdata.PrefsPatch{
		ReadingDir: userdata.SetPatch("rtl"), FitMode: userdata.SetPatch("width")}); err != nil {
		t.Fatalf("PutPrefs: %v", err)
	}

	doc, err := src.Export(ctx)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if doc.Format != "shelf-progress/1" || doc.IDVersion != "shelf-id/1" {
		t.Errorf("envelope = format %q, id_version %q", doc.Format, doc.IDVersion)
	}
	if len(doc.Items) != 2 || len(doc.Prefs) != 1 {
		t.Fatalf("export = %d items, %d prefs", len(doc.Items), len(doc.Prefs))
	}

	dst := openIn(t, t.TempDir(), newClock())
	res, err := dst.Import(ctx, doc, userdata.StrategyMerge)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.Imported != 2 || res.Skipped != 0 || res.Conflicts != 0 {
		t.Errorf("import result = %+v, want 2 imported", res)
	}

	back, err := dst.Export(ctx)
	if err != nil {
		t.Fatalf("Export from the destination: %v", err)
	}
	if len(back.Items) != len(doc.Items) {
		t.Fatalf("round trip lost rows: %d vs %d", len(back.Items), len(doc.Items))
	}
	for i := range doc.Items {
		if back.Items[i] != doc.Items[i] {
			t.Errorf("item %d changed:\n got %+v\nwant %+v", i, back.Items[i], doc.Items[i])
		}
	}
	p, err := dst.GetPrefs(ctx, "b2")
	if err != nil {
		t.Fatalf("GetPrefs: %v", err)
	}
	if p.ReadingDir == nil || *p.ReadingDir != "rtl" || p.FitMode == nil || *p.FitMode != "width" {
		t.Errorf("prefs did not survive the round trip: %+v", p)
	}
}

func TestImport_mergeKeepsTheNewerRow_replaceOverwrites(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	makeDoc := func(bookID string, page int, updatedAt int64) userdata.Export {
		return userdata.Export{
			Format: "shelf-progress/1", IDVersion: "shelf-id/1", ExportedAt: updatedAt,
			Items: []userdata.ExportItem{{
				BookID: bookID, SeriesID: "s1", RootName: "manga", BookPath: "s/x.zip",
				LastPage: page, PageCount: 20, StartedAt: 1000, UpdatedAt: updatedAt,
			}},
		}
	}

	t.Run("incoming newer wins", func(t *testing.T) {
		db, clk, _ := newDB(t)
		local, err := db.PutProgress(ctx, update("b1", "s1", 5, 20))
		if err != nil {
			t.Fatalf("PutProgress: %v", err)
		}
		res, err := db.Import(ctx, makeDoc("b1", 11, local.UpdatedAt+60), userdata.StrategyMerge)
		if err != nil {
			t.Fatalf("Import: %v", err)
		}
		if res.Imported != 1 || res.Skipped != 0 || res.Conflicts != 1 {
			t.Errorf("result = %+v, want imported 1, skipped 0, conflicts 1", res)
		}
		got, _ := db.GetProgress(ctx, "b1")
		if got.LastPage != 11 {
			t.Errorf("last page = %d, want 11", got.LastPage)
		}
		clk.Advance(0)
	})

	t.Run("local newer wins", func(t *testing.T) {
		db, _, _ := newDB(t)
		local, err := db.PutProgress(ctx, update("b1", "s1", 5, 20))
		if err != nil {
			t.Fatalf("PutProgress: %v", err)
		}
		res, err := db.Import(ctx, makeDoc("b1", 11, local.UpdatedAt-60), userdata.StrategyMerge)
		if err != nil {
			t.Fatalf("Import: %v", err)
		}
		if res.Imported != 0 || res.Skipped != 1 || res.Conflicts != 1 {
			t.Errorf("result = %+v, want imported 0, skipped 1, conflicts 1", res)
		}
		got, _ := db.GetProgress(ctx, "b1")
		if got.LastPage != 5 {
			t.Errorf("last page = %d, want the local 5", got.LastPage)
		}
	})

	t.Run("equal timestamps keep the local row", func(t *testing.T) {
		db, _, _ := newDB(t)
		local, err := db.PutProgress(ctx, update("b1", "s1", 5, 20))
		if err != nil {
			t.Fatalf("PutProgress: %v", err)
		}
		res, err := db.Import(ctx, makeDoc("b1", 11, local.UpdatedAt), userdata.StrategyMerge)
		if err != nil {
			t.Fatalf("Import: %v", err)
		}
		if res.Skipped != 1 {
			t.Errorf("result = %+v, want the tie to go to the local row", res)
		}
	})

	t.Run("replace overwrites an older document", func(t *testing.T) {
		db, _, _ := newDB(t)
		local, err := db.PutProgress(ctx, update("b1", "s1", 5, 20))
		if err != nil {
			t.Fatalf("PutProgress: %v", err)
		}
		res, err := db.Import(ctx, makeDoc("b1", 11, local.UpdatedAt-3600), userdata.StrategyReplace)
		if err != nil {
			t.Fatalf("Import: %v", err)
		}
		if res.Imported != 1 || res.Skipped != 0 || res.Conflicts != 1 {
			t.Errorf("result = %+v, want the incoming row to win unconditionally", res)
		}
		got, _ := db.GetProgress(ctx, "b1")
		if got.LastPage != 11 {
			t.Errorf("last page = %d, want 11", got.LastPage)
		}
	})
}

func TestImport_refusesForeignEnvelopes(t *testing.T) {
	t.Parallel()
	db, _, _ := newDB(t)
	ctx := t.Context()

	if _, err := db.Import(ctx, userdata.Export{
		Format: "someone-else/2", IDVersion: "shelf-id/1"}, userdata.StrategyMerge); !errors.Is(err, userdata.ErrInvalidArgument) {
		t.Errorf("foreign format = %v, want ErrInvalidArgument", err)
	}
	if _, err := db.Import(ctx, userdata.Export{
		Format: "shelf-progress/1", IDVersion: "shelf-id/2"}, userdata.StrategyMerge); !errors.Is(err, userdata.ErrIDVersionMismatch) {
		t.Errorf("foreign id scheme = %v, want ErrIDVersionMismatch", err)
	}
	if _, err := db.Import(ctx, userdata.Export{}, "sideways"); !errors.Is(err, userdata.ErrInvalidArgument) {
		t.Errorf("unknown strategy = %v, want ErrInvalidArgument", err)
	}
}

// A malformed item must not leave half an import behind: somebody's reading
// history is not a place for partial writes.
func TestImport_isAtomic(t *testing.T) {
	t.Parallel()
	db, _, _ := newDB(t)
	ctx := t.Context()

	doc := userdata.Export{
		Format: "shelf-progress/1", IDVersion: "shelf-id/1",
		Items: []userdata.ExportItem{
			{BookID: "good", SeriesID: "s1", RootName: "m", BookPath: "p", LastPage: 1, PageCount: 5, UpdatedAt: 10},
			{BookID: "", SeriesID: "s1", RootName: "m", BookPath: "p", LastPage: 1, PageCount: 5, UpdatedAt: 10},
		},
	}
	if _, err := db.Import(ctx, doc, userdata.StrategyMerge); !errors.Is(err, userdata.ErrInvalidArgument) {
		t.Fatalf("Import = %v, want ErrInvalidArgument", err)
	}
	if n, _ := db.CountProgress(ctx); n != 0 {
		t.Errorf("rows after a failed import = %d, want 0", n)
	}
}

// An import document is a file the user hands us (arch §7.11), so it is exactly
// as untrusted as a PUT body. Every value it carries must clear the same gate:
// "double" and "screen" are the values C-1/C-2 rule cannot exist anywhere in
// code, and they would leave storage again through GET /api/books/{bid}/prefs as
// a DisplayMode/FitMode the frozen contract does not define.
func TestImport_rejectsPrefValuesOutsideTheFrozenEnums(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	str := func(s string) *string { return &s }
	cases := map[string]userdata.ExportPref{
		"display_mode double (C-1)": {BookID: "b1", DisplayMode: str("double")},
		"fit_mode screen (C-2)":     {BookID: "b1", FitMode: str("screen")},
		"fit_mode injection":        {BookID: "b1", FitMode: str(`'; DROP TABLE progress; --`)},
		"reading_direction upwards": {BookID: "b1", ReadingDir: str("up")},
	}
	for name, pref := range cases {
		t.Run(name, func(t *testing.T) {
			db, _, _ := newDB(t)
			doc := userdata.Export{
				Format: "shelf-progress/1", IDVersion: "shelf-id/1",
				Items: []userdata.ExportItem{{
					BookID: "b1", SeriesID: "s1", RootName: "m", BookPath: "p",
					LastPage: 2, PageCount: 5, UpdatedAt: 10,
				}},
				Prefs: []userdata.ExportPref{pref},
			}
			if _, err := db.Import(ctx, doc, userdata.StrategyMerge); !errors.Is(err, userdata.ErrInvalidArgument) {
				t.Fatalf("Import = %v, want ErrInvalidArgument", err)
			}
			// One transaction: the progress row that preceded the bad pref must
			// be gone too.
			if p, err := db.GetPrefs(ctx, "b1"); err != nil {
				t.Fatalf("GetPrefs: %v", err)
			} else if p.DisplayMode != nil || p.FitMode != nil || p.ReadingDir != nil {
				t.Errorf("prefs after a refused import = %+v, want nothing stored", p)
			}
			if n, _ := db.CountProgress(ctx); n != 0 {
				t.Errorf("progress rows after a refused import = %d, want 0", n)
			}
		})
	}
}

// Page numbers are 1-based everywhere in the API (arch §7.1). The import path
// clamps them exactly as PutProgress does, so a hand-edited export cannot store
// a page the PUT path would have clamped away; a negative page_count is a
// malformed document and is refused, again as on the write path.
func TestImport_clampsPageNumbersLikeTheWritePath(t *testing.T) {
	t.Parallel()
	db, _, _ := newDB(t)
	ctx := t.Context()

	item := func(id string, last, count int) userdata.ExportItem {
		return userdata.ExportItem{
			BookID: id, SeriesID: "s1", RootName: "m", BookPath: "p",
			LastPage: last, PageCount: count, UpdatedAt: 10,
		}
	}
	doc := userdata.Export{
		Format: "shelf-progress/1", IDVersion: "shelf-id/1",
		Items: []userdata.ExportItem{
			item("under", -42, 5),
			item("zero", 0, 5),
			item("over", 999_999, 5),
			item("unknown-length", 7, 0),
		},
	}
	if _, err := db.Import(ctx, doc, userdata.StrategyMerge); err != nil {
		t.Fatalf("Import: %v", err)
	}
	for id, want := range map[string]int{"under": 1, "zero": 1, "over": 5, "unknown-length": 7} {
		got, err := db.GetProgress(ctx, id)
		if err != nil {
			t.Fatalf("GetProgress(%s): %v", id, err)
		}
		if got.LastPage != want {
			t.Errorf("%s: last_page = %d, want %d", id, got.LastPage, want)
		}
	}

	bad := userdata.Export{
		Format: "shelf-progress/1", IDVersion: "shelf-id/1",
		Items: []userdata.ExportItem{item("negative-count", 1, -1)},
	}
	if _, err := db.Import(ctx, bad, userdata.StrategyMerge); !errors.Is(err, userdata.ErrInvalidArgument) {
		t.Errorf("import with page_count -1 = %v, want ErrInvalidArgument", err)
	}
}

// ------------------------------------------------------------- id batching --

// Nothing caps the id lists these two take, and SQLite's bound-variable ceiling
// is a hard error rather than a slow query. 45 000 is past the 32 766 of the
// build modernc.org/sqlite ships and far past the 999 of older ones.
func TestQueries_acceptUnboundedIDLists(t *testing.T) {
	t.Parallel()
	db, _, _ := newDB(t)
	ctx := t.Context()

	const real = 40
	bookIDs := make([]string, 0, 45_000)
	seriesIDs := make([]string, 0, 45_000)
	for i := range real {
		bookID := fmt.Sprintf("real%012d", i)
		seriesID := fmt.Sprintf("ser%013d", i)
		if _, err := db.PutProgress(ctx, update(bookID, seriesID, 2, 10)); err != nil {
			t.Fatalf("PutProgress: %v", err)
		}
		bookIDs = append(bookIDs, bookID)
		seriesIDs = append(seriesIDs, seriesID)
	}
	for i := range 45_000 - real {
		bookIDs = append(bookIDs, fmt.Sprintf("ghost%011d", i))
		seriesIDs = append(seriesIDs, fmt.Sprintf("ghostser%08d", i))
	}

	got, err := db.GetProgressMany(ctx, bookIDs)
	if err != nil {
		t.Fatalf("GetProgressMany with %d ids: %v", len(bookIDs), err)
	}
	if len(got) != real {
		t.Errorf("GetProgressMany returned %d rows, want %d", len(got), real)
	}
	for i := range real {
		if _, ok := got[fmt.Sprintf("real%012d", i)]; !ok {
			t.Fatalf("book real%012d is missing from the result", i)
		}
	}

	agg, err := db.SeriesAggregates(ctx, seriesIDs)
	if err != nil {
		t.Fatalf("SeriesAggregates with %d ids: %v", len(seriesIDs), err)
	}
	if len(agg) != real {
		t.Errorf("SeriesAggregates returned %d series, want %d", len(agg), real)
	}
	for i := range real {
		a, ok := agg[fmt.Sprintf("ser%013d", i)]
		if !ok {
			t.Fatalf("series ser%013d is missing from the aggregate", i)
		}
		if a.BooksStarted != 1 || a.BooksCompleted != 0 {
			t.Errorf("series %s rollup = %+v, want 1 started, 0 completed", a.SeriesID, a)
		}
	}
}

// ---------------------------------------------------------- AC-006 / rebuild --

// The userdata half of AC-006: index.db and its sidecars are deleted out from
// under this database — exactly what `--rebuild-index` does — and every authored
// row must still be there afterwards, byte for byte.
func TestUserData_survivesDeletionOfTheIndexFiles(t *testing.T) {
	t.Parallel()
	db, clk, dir := newDB(t)
	ctx := t.Context()

	done := true
	u := update("b1", "s1", 9, 9)
	u.Completed = &done
	if _, err := db.PutProgress(ctx, u); err != nil {
		t.Fatalf("PutProgress: %v", err)
	}
	clk.Advance(time.Minute)
	if _, err := db.PutProgress(ctx, update("b2", "s1", 4, 30)); err != nil {
		t.Fatalf("PutProgress: %v", err)
	}
	if _, err := db.PutPrefs(ctx, "b2", userdata.PrefsPatch{
		DisplayMode: userdata.SetPatch("vertical")}); err != nil {
		t.Fatalf("PutPrefs: %v", err)
	}
	if err := db.Settings().Put(ctx, "reading_direction", `"rtl"`); err != nil {
		t.Fatalf("Settings.Put: %v", err)
	}
	if err := db.ViewState().Put(ctx, "library_view", `"list"`); err != nil {
		t.Fatalf("ViewState.Put: %v", err)
	}
	before, err := db.Export(ctx)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Stand in for a scan's output, then delete it the way FR-IDX-005 does.
	for _, name := range []string{"index.db", "index.db-wal", "index.db-shm"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("derived"), 0o600); err != nil {
			t.Fatalf("creating %s: %v", name, err)
		}
	}
	for _, name := range []string{"index.db", "index.db-wal", "index.db-shm"} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			t.Fatalf("removing %s: %v", name, err)
		}
	}

	db2 := openIn(t, dir, newClock())
	after, err := db2.Export(ctx)
	if err != nil {
		t.Fatalf("Export after the rebuild: %v", err)
	}
	if len(after.Items) != len(before.Items) {
		t.Fatalf("progress rows: %d before, %d after", len(before.Items), len(after.Items))
	}
	for i := range before.Items {
		if after.Items[i] != before.Items[i] {
			t.Errorf("progress row %d changed:\n got %+v\nwant %+v", i, after.Items[i], before.Items[i])
		}
	}
	if len(after.Prefs) != 1 || after.Prefs[0].DisplayMode == nil || *after.Prefs[0].DisplayMode != "vertical" {
		t.Errorf("prefs after the rebuild = %+v", after.Prefs)
	}
	if v, ok, _ := db2.Settings().Get(ctx, "reading_direction"); !ok || v != `"rtl"` {
		t.Errorf("setting after the rebuild = %q (present %v)", v, ok)
	}
	if v, ok, _ := db2.ViewState().Get(ctx, "library_view"); !ok || v != `"list"` {
		t.Errorf("view state after the rebuild = %q (present %v)", v, ok)
	}
}

// ------------------------------------------------------------ concurrency --

func TestConcurrentReadersAndWriters(t *testing.T) {
	t.Parallel()
	db, _, _ := newDB(t)
	ctx := t.Context()

	var wg sync.WaitGroup
	errCh := make(chan error, 64)

	for w := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 40 {
				if _, err := db.PutProgress(ctx, update(fmt.Sprintf("b%d", w), "s1", i+1, 100)); err != nil {
					errCh <- fmt.Errorf("writer %d: %w", w, err)
					return
				}
			}
		}()
	}
	for r := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 40 {
				if _, err := db.Continue(ctx, 10); err != nil {
					errCh <- fmt.Errorf("reader %d: Continue: %w", r, err)
					return
				}
				if _, err := db.SeriesAggregates(ctx, []string{"s1"}); err != nil {
					errCh <- fmt.Errorf("reader %d: SeriesAggregates: %w", r, err)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}

	if n, _ := db.CountProgress(ctx); n != 8 {
		t.Errorf("progress rows = %d, want 8", n)
	}
}

// ----------------------------------------------------------------- helpers --

func openRaw(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("opening %s raw: %v", path, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func journalMode(t *testing.T, path string) string {
	t.Helper()
	var mode string
	if err := openRaw(t, path).QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("reading journal mode: %v", err)
	}
	return mode
}

func tableColumns(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?) ORDER BY cid`, table)
	if err != nil {
		t.Fatalf("reading columns of %s: %v", table, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scanning column: %v", err)
		}
		out = append(out, name)
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
