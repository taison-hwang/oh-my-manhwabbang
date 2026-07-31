package index_test

import (
	"errors"
	"path/filepath"
	"testing"

	"shelf/internal/index"
	"shelf/internal/userdata"
)

// Amendment A-8 (ruling E-9): GET /api/series?scope=added, backed by user.db's
// write-once first_seen_at. The listing's job is to filter on that stamp alone,
// to keep reporting COALESCE(first_seen_at, index added_at) as `added_at`, and
// to count the same set it lists.

const (
	scopeNow = int64(1_700_000_000)
	scopeDay = int64(86_400)
)

// markSeen stamps first sightings the way the scanner does.
func markSeen(t *testing.T, ud *userdata.DB, at map[string]int64) {
	t.Helper()
	rows := make([]userdata.SeriesSeen, 0, len(at))
	for id, ts := range at {
		rows = append(rows, userdata.SeriesSeen{
			SeriesID: id, RootName: "manga", SeriesPath: "series-" + id, FirstSeenAt: ts,
		})
	}
	if err := ud.MarkSeriesSeen(t.Context(), rows); err != nil {
		t.Fatalf("MarkSeriesSeen: %v", err)
	}
}

// seenLibrary stamps the five-series fixture so that exactly two are inside a
// 14-day window, one sits on the boundary and one is a decade old. The fifth is
// deliberately left with no row at all.
func seenLibrary(t *testing.T, ud *userdata.DB) {
	t.Helper()
	markSeen(t, ud, map[string]int64{
		"aaaaaaaaaaaaaaaa": scopeNow - 1*scopeDay,   // 군계            — inside
		"bbbbbbbbbbbbbbbb": scopeNow - 13*scopeDay,  // 강철의 연금술사  — inside
		"cccccccccccccccc": scopeNow - 14*scopeDay,  // Attack on Titan — the boundary
		"dddddddddddddddd": scopeNow - 400*scopeDay, // 빈 시리즈       — outside
		// eeeeeeeeeeeeeeee (20세기소년) has no row: never recorded.
	})
}

func TestListSeries_scopeAdded_filtersOnFirstSeenAlone(t *testing.T) {
	t.Parallel()
	idx, ud, _ := newDBs(t)
	ctx := t.Context()
	seed(t, idx, "manga", library())
	seenLibrary(t, ud)

	cases := []struct {
		name   string
		cutoff int64
		want   []string
	}{
		{"14 days, the shipped default", scopeNow - 14*scopeDay,
			[]string{"aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb", "cccccccccccccccc"}},
		{"the boundary is inclusive", scopeNow - 14*scopeDay + 1,
			[]string{"aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb"}},
		{"2 days", scopeNow - 2*scopeDay, []string{"aaaaaaaaaaaaaaaa"}},
		{"a window nothing is inside", scopeNow, nil},
		{"3650 days", scopeNow - 3650*scopeDay,
			[]string{"aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb", "cccccccccccccccc", "dddddddddddddddd"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			list, err := idx.ListSeries(ctx, index.SeriesFilter{
				Scope: index.ScopeAdded, RecentlyAddedCutoff: tc.cutoff,
				Status: "all", Limit: 200,
			})
			if err != nil {
				t.Fatalf("ListSeries: %v", err)
			}
			got := ids(list.Items)
			if !equalStrings(got, tc.want) {
				t.Errorf("scope=added ids = %v, want %v", got, tc.want)
			}
			if list.Total != len(tc.want) {
				t.Errorf("total = %d, want %d", list.Total, len(tc.want))
			}
		})
	}

	// The series with no row is excluded from every window, however wide —
	// NULL >= x is not true, and that is the intended behaviour (arch §3.6
	// rule 7). Without it, `scope=added` would fall back to the index's
	// added_at and a rebuilt index would push the whole library into the smart
	// list, which is the exact failure A-8 exists to prevent.
	all, err := idx.ListSeries(ctx, index.SeriesFilter{
		Scope: index.ScopeAdded, RecentlyAddedCutoff: 0, Status: "all", Limit: 200,
	})
	if err != nil {
		t.Fatalf("ListSeries: %v", err)
	}
	for _, s := range all.Items {
		if s.ID == "eeeeeeeeeeeeeeee" {
			t.Error("a series with no series_seen row appeared in scope=added")
		}
	}
	if all.Total != 4 {
		t.Errorf("total with an epoch cutoff = %d, want the 4 stamped series", all.Total)
	}
}

// The sidebar badge is `total` from ?scope=added&limit=1 (arch §7.5). It has to
// be the count of the list the same filter returns, or the badge and the screen
// it opens disagree.
func TestListSeries_scopeAdded_countIdiomMatchesTheListLength(t *testing.T) {
	t.Parallel()
	idx, ud, _ := newDBs(t)
	ctx := t.Context()
	seed(t, idx, "manga", library())
	seenLibrary(t, ud)

	for _, days := range []int64{1, 2, 14, 15, 401, 3650} {
		f := index.SeriesFilter{
			Scope: index.ScopeAdded, RecentlyAddedCutoff: scopeNow - days*scopeDay,
			Status: "all",
		}
		badge := f
		badge.Limit = 1
		one, err := idx.ListSeries(ctx, badge)
		if err != nil {
			t.Fatalf("badge query at %d days: %v", days, err)
		}
		full := f
		full.Limit = 200
		page, err := idx.ListSeries(ctx, full)
		if err != nil {
			t.Fatalf("list at %d days: %v", days, err)
		}
		if one.Total != len(page.Items) {
			t.Errorf("%d-day window: badge total = %d but the list holds %d series",
				days, one.Total, len(page.Items))
		}
		if one.Total != page.Total {
			t.Errorf("%d-day window: total = %d with limit=1 and %d with limit=200",
				days, one.Total, page.Total)
		}
		if len(one.Items) > 1 {
			t.Errorf("%d-day window: limit=1 returned %d items", days, len(one.Items))
		}
	}
}

// scope is AND-ed with every other filter and changes none of their meanings
// (arch §7.5 "How it composes").
func TestListSeries_scopeAdded_composesWithTheOtherFilters(t *testing.T) {
	t.Parallel()
	idx, ud, _ := newDBs(t)
	ctx := t.Context()
	seed(t, idx, "manga", library())
	seed(t, idx, "books", []seedSeries{
		{id: "ffffffffffffffff", name: "다른 루트", sortKey: "6-other", searchKey: "다른 루트",
			mtime: 600, addedAt: 60,
			books: []seedBook{{id: "b6ffffffffffffff", name: "1.zip", ord: 0, kind: "zip", pages: 4}}},
	})
	seenLibrary(t, ud)
	markSeen(t, ud, map[string]int64{"ffffffffffffffff": scopeNow - 2*scopeDay})
	mustPut(t, ud, "b1aaaaaaaaaaaaaa", "aaaaaaaaaaaaaaaa", 2, 5, false)

	base := index.SeriesFilter{
		Scope: index.ScopeAdded, RecentlyAddedCutoff: scopeNow - 14*scopeDay,
		Status: "all", Limit: 200,
	}
	cases := []struct {
		name string
		fn   func(index.SeriesFilter) index.SeriesFilter
		want []string
	}{
		{"root", func(f index.SeriesFilter) index.SeriesFilter {
			f.Roots = []string{"books"}
			return f
		}, []string{"ffffffffffffffff"}},
		{"q", func(f index.SeriesFilter) index.SeriesFilter {
			f.Query = "attack"
			return f
		}, []string{"cccccccccccccccc"}},
		{"status", func(f index.SeriesFilter) index.SeriesFilter {
			f.Status = "empty"
			return f
		}, nil}, // the only empty series is 400 days old
		{"progress=reading", func(f index.SeriesFilter) index.SeriesFilter {
			f.Progress = index.ProgressReading
			return f
		}, []string{"aaaaaaaaaaaaaaaa"}},
		{"progress=unread", func(f index.SeriesFilter) index.SeriesFilter {
			f.Progress = index.ProgressUnread
			return f
		}, []string{"bbbbbbbbbbbbbbbb", "cccccccccccccccc", "ffffffffffffffff"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			list, err := idx.ListSeries(ctx, tc.fn(base))
			if err != nil {
				t.Fatalf("ListSeries: %v", err)
			}
			if got := ids(list.Items); !equalStrings(got, tc.want) {
				t.Errorf("ids = %v, want %v", got, tc.want)
			}
			if list.Total != len(tc.want) {
				t.Errorf("total = %d, want %d", list.Total, len(tc.want))
			}
		})
	}
}

// scope does not change the sort defaults: the frontend sends sort=added
// explicitly for 최근 추가 (arch §7.5).
func TestListSeries_scopeAdded_keepsTheDefaultSort(t *testing.T) {
	t.Parallel()
	idx, ud, _ := newDBs(t)
	ctx := t.Context()
	seed(t, idx, "manga", library())
	seenLibrary(t, ud)

	f := index.SeriesFilter{
		Scope: index.ScopeAdded, RecentlyAddedCutoff: scopeNow - 3650*scopeDay,
		Status: "all", Limit: 200,
	}
	byDefault, err := idx.ListSeries(ctx, f)
	if err != nil {
		t.Fatalf("ListSeries: %v", err)
	}
	// sort_key order, ascending — the same default any other request gets.
	want := []string{"aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb", "cccccccccccccccc", "dddddddddddddddd"}
	if got := ids(byDefault.Items); !equalStrings(got, want) {
		t.Errorf("scope=added with no sort = %v, want natural name order %v", got, want)
	}

	f.Sort, f.Order = index.SortAdded, "desc"
	newest, err := idx.ListSeries(ctx, f)
	if err != nil {
		t.Fatalf("ListSeries sort=added: %v", err)
	}
	want = []string{"aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb", "cccccccccccccccc", "dddddddddddddddd"}
	if got := ids(newest.Items); !equalStrings(got, want) {
		t.Errorf("sort=added&order=desc = %v, want newest first %v", got, want)
	}
}

// added_at is COALESCE(first_seen_at, index added_at) everywhere the word
// "added" appears, and sort=added follows the same expression.
func TestListSeries_addedAt_prefersTheUserStampOverTheIndexColumn(t *testing.T) {
	t.Parallel()
	idx, ud, _ := newDBs(t)
	ctx := t.Context()
	seed(t, idx, "manga", library())
	// The index says these were added at 10..50 (a rebuild would say "now").
	// user.db says otherwise for four of the five.
	seenLibrary(t, ud)

	list, err := idx.ListSeries(ctx, index.SeriesFilter{
		Status: "all", Sort: index.SortAdded, Order: "asc", Limit: 200,
	})
	if err != nil {
		t.Fatalf("ListSeries: %v", err)
	}
	want := map[string]int64{
		"aaaaaaaaaaaaaaaa": scopeNow - 1*scopeDay,
		"bbbbbbbbbbbbbbbb": scopeNow - 13*scopeDay,
		"cccccccccccccccc": scopeNow - 14*scopeDay,
		"dddddddddddddddd": scopeNow - 400*scopeDay,
		"eeeeeeeeeeeeeeee": 40, // no stamp: the index column is the fallback
	}
	for _, s := range list.Items {
		if got := s.AddedAt; got != want[s.ID] {
			t.Errorf("%s added_at = %d, want %d", s.ID, got, want[s.ID])
		}
	}
	// And the ordering follows the same expression: the fallback row sorts
	// first because 40 is older than every stamp.
	wantOrder := []string{"eeeeeeeeeeeeeeee", "dddddddddddddddd", "cccccccccccccccc",
		"bbbbbbbbbbbbbbbb", "aaaaaaaaaaaaaaaa"}
	if got := ids(list.Items); !equalStrings(got, wantOrder) {
		t.Errorf("sort=added asc = %v, want %v", got, wantOrder)
	}

	// GetSeries reports the same value as the listing (they share seriesColumns).
	d, err := idx.GetSeries(ctx, "aaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatalf("GetSeries: %v", err)
	}
	if d.AddedAt != want["aaaaaaaaaaaaaaaa"] {
		t.Errorf("GetSeries added_at = %d, want %d", d.AddedAt, want["aaaaaaaaaaaaaaaa"])
	}
}

func TestListSeries_scope_rejectsUnknownValues(t *testing.T) {
	t.Parallel()
	idx, _, _ := newDBs(t)
	ctx := t.Context()
	seed(t, idx, "manga", library())

	// "reading" and "done" are progress values and a root name is a root value;
	// none of them may be spelled as a scope (arch §7.5).
	for _, scope := range []string{"recent", "reading", "done", "manga", "ADDED", "true"} {
		_, err := idx.ListSeries(ctx, index.SeriesFilter{Scope: scope, RecentlyAddedCutoff: scopeNow})
		if !errors.Is(err, index.ErrInvalidFilter) {
			t.Errorf("scope=%q = %v, want ErrInvalidFilter", scope, err)
		}
	}
	for _, scope := range []string{"", index.ScopeAll} {
		list, err := idx.ListSeries(ctx, index.SeriesFilter{Scope: scope, Status: "all", Limit: 200})
		if err != nil {
			t.Fatalf("scope=%q: %v", scope, err)
		}
		if list.Total != 5 {
			t.Errorf("scope=%q total = %d, want the whole library (5)", scope, list.Total)
		}
	}
}

// AC-006 end to end at the storage layer: destroy the index, rebuild it, and the
// smart list is unchanged. Doing this in index.db instead would make every
// series newly added after a rebuild.
func TestListSeries_scopeAdded_survivesAnIndexRebuild(t *testing.T) {
	t.Parallel()
	idx, ud, _ := newDBs(t)
	ctx := t.Context()
	seed(t, idx, "manga", library())
	seenLibrary(t, ud)

	f := index.SeriesFilter{
		Scope: index.ScopeAdded, RecentlyAddedCutoff: scopeNow - 14*scopeDay,
		Status: "all", Limit: 200,
	}
	before, err := idx.ListSeries(ctx, f)
	if err != nil {
		t.Fatalf("ListSeries: %v", err)
	}

	if err := idx.Reset(ctx); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if empty, err := idx.ListSeries(ctx, f); err != nil {
		t.Fatalf("ListSeries on the empty index: %v", err)
	} else if empty.Total != 0 {
		t.Fatalf("the index was not emptied: total = %d", empty.Total)
	}

	// The rescan reproduces the same ids (arch §3.4) but an index.added_at of
	// "now" — which is exactly the value scope=added must ignore.
	rebuilt := library()
	for i := range rebuilt {
		rebuilt[i].addedAt = scopeNow
	}
	seed(t, idx, "manga", rebuilt)

	after, err := idx.ListSeries(ctx, f)
	if err != nil {
		t.Fatalf("ListSeries after the rebuild: %v", err)
	}
	if !equalStrings(ids(after.Items), ids(before.Items)) {
		t.Errorf("최근 추가 after a rebuild = %v, want %v unchanged",
			ids(after.Items), ids(before.Items))
	}
	if after.Total != before.Total {
		t.Errorf("total after a rebuild = %d, want %d", after.Total, before.Total)
	}
	// The series that never had a stamp is still out, even though the index now
	// says it was added this second.
	for _, s := range after.Items {
		if s.ID == "eeeeeeeeeeeeeeee" {
			t.Error("a rebuilt index.added_at leaked into scope=added through the COALESCE")
		}
	}
}

// index.Open must refuse a user.db that predates amendment A-8 rather than fail
// every listing later with "no such table: ud.series_seen".
func TestOpen_userDBWithoutSeriesSeen_isRefusedAtStartup(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, ud, _ := openDBsIn(t, dir, nil)
	if err := ud.Close(); err != nil {
		t.Fatalf("closing user.db: %v", err)
	}

	raw := openRaw(t, filepath.Join(dir, "user.db"))
	if _, err := raw.Exec(`DROP TABLE series_seen`); err != nil {
		t.Fatalf("simulating a version-1 user.db: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("closing the raw handle: %v", err)
	}

	_, err := index.Open(t.Context(), index.Options{
		Path:     filepath.Join(dir, "index2.db"),
		UserPath: filepath.Join(dir, "user.db"),
		Logger:   quietLogger(),
	})
	if !errors.Is(err, index.ErrUserDBNotReady) {
		t.Fatalf("Open with a pre-A-8 user.db = %v, want ErrUserDBNotReady", err)
	}
}
