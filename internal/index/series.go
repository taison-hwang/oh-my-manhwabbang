package index

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// Series mirrors one row of the `series` table. A series is exactly one direct
// child of a root (prd §1.3, ruling E-4).
type Series struct {
	ID          string
	RootName    string
	RelPath     string
	DisplayName string
	// SortKey is natsort.Key(DisplayName). SQLite orders it under the default
	// BINARY collation, which is what makes sort=name free of a Go-side re-sort
	// and of a user-defined collation (D-31).
	SortKey     []byte
	SearchKey   string
	ChoseongKey string
	// Kind is "folder", "zip" or "pdf".
	Kind       string
	BookCount  int64
	PageCount  int64
	TotalBytes int64
	Mtime      int64
	AddedAt    int64
	// CoverKind is "page", "file" or "" (no cover — FR-LIB-008 placeholder).
	CoverKind    string
	CoverBookID  string
	CoverPageNo  int
	CoverRelPath string
	// Status is "ok", "empty" or "error" — never the book-only "encrypted" or
	// "unsupported". Ruling E-14 fixes the fold: `empty` means the series holds
	// no books at all, `ok` means at least one book is readable, and `error`
	// means it holds books but none of them are readable, whatever the reason.
	// A series the reader cannot open a single page of must not present as
	// healthy (FR-IDX-010), and Error carries the reason the UI shows.
	Status  string
	Error   string
	ScanGen int64
}

// SeriesProgress is the FR-STT-002 rollup of a series' books, computed from
// ud.progress. Percent is deliberately not stored here: it is presentation and
// belongs to the HTTP layer.
//
// **PagesRead / PagesTotal are what E-47 turned `percent` into.** The rollup
// used to be able to answer only "how many 권 are finished", which is a step
// function that stays at 0 for the whole of a reader's first volume — on the
// real library that left 46 of 49 started series reporting 0 %. These two carry
// the finer measure the ruling asked for:
//
//   - PagesRead sums each book's contribution — its whole length when the book
//     is marked completed, otherwise its last read page, clamped to the length
//     the *index* currently reports. The clamp is E-45 §6's rule, not a defensive
//     guard: `ud.progress.page_count` is the stale-detection baseline and can
//     disagree with the file, in both directions;
//   - PagesTotal is the series' own page_count, which the scanner writes as the
//     sum of its books' (`roots.go` recount, verified against the live library:
//     40 books, 7 823 pages, sum identical). It is filled in by scanSeriesRow
//     from the series row, the same way BooksTotal is filled from BookCount —
//     the join cannot supply it, because a series with no progress row at all
//     has no row in the rollup subquery.
type SeriesProgress struct {
	BooksTotal     int64
	BooksCompleted int64
	BooksStarted   int64
	PagesRead      int64
	PagesTotal     int64
	LastReadAt     *int64
	LastBookID     string
	LastPage       int
}

// SeriesRow is a series plus the joined reading progress and the content
// version its cover should be requested with.
type SeriesRow struct {
	Series
	Progress SeriesProgress
	// CoverCV is the cover book's content_version when CoverKind == "page";
	// empty otherwise. The frontend appends it as ?v= (D-17).
	CoverCV string
}

// SeriesDetail is a series with its books, for GET /api/series/{sid}.
type SeriesDetail struct {
	SeriesRow
	Books []BookRow
}

// Sort keys accepted by ListSeries (arch §7.5, conflict resolution C-3).
const (
	SortName   = "name"
	SortMtime  = "mtime"
	SortRecent = "recent"
	SortSize   = "size"
	SortBooks  = "books"
	SortAdded  = "added"
)

// Progress filter values (amendment A-4).
const (
	ProgressAny     = "any"
	ProgressReading = "reading"
	ProgressDone    = "done"
	ProgressUnread  = "unread"
)

// Scope filter values (amendment A-8, ruling E-9). ScopeAdded is the sidebar's
// 최근 추가 smart list: a filter, not a sort and not a view mode.
const (
	ScopeAll   = "all"
	ScopeAdded = "added"
)

// SeriesFilter is the parameter set of GET /api/series.
type SeriesFilter struct {
	// Roots restricts to these root names. Empty means every enabled root.
	Roots []string
	// Query is the FR-LIB-006 search term. A jamo/ASCII query also matches
	// choseong_key; anything else matches search_key only (arch §4.8).
	Query string
	// Status is "", "all", "ok", "empty" or "error".
	Status string
	// Progress is "", "any", "reading", "done" or "unread" (A-4).
	Progress string
	// Scope is "", "all" or "added" (A-8). "added" keeps only the series whose
	// user.db first_seen_at is at or after RecentlyAddedCutoff.
	Scope string
	// RecentlyAddedCutoff is `now - library.recently_added_days * 86400` in unix
	// seconds, computed per request by the caller and never cached: with the
	// 14-day default a series leaves the smart list on the 15th day with no
	// scan, no restart and no cache purge (arch §7.5). It is read only when
	// Scope is "added".
	RecentlyAddedCutoff int64
	// Sort is one of the Sort* constants; "" means SortName.
	Sort string
	// Order is "asc", "desc" or "" (per-sort default: asc for name, desc else).
	Order  string
	Offset int
	Limit  int
	// IncludeDisabledRoots lists series belonging to roots with enabled=0. The
	// API never sets this; the scanner and tests do.
	IncludeDisabledRoots bool
}

// SeriesList is one page of results plus the unpaged total.
type SeriesList struct {
	Items  []SeriesRow
	Total  int
	Offset int
	Limit  int
}

const maxSeriesLimit = 200
const defaultSeriesLimit = 60

// seriesColumns is shared by ListSeries and GetSeries so a column added to one
// cannot be forgotten in the other.
//
// AddedAt is COALESCE(ud.series_seen.first_seen_at, s.added_at) under amendment
// A-8: one meaning of "added" for the whole API. The user.db stamp is the
// authority because a rebuild cannot move it; the index column is the fallback
// for a library whose user.db has no row yet (arch §7.5).
const seriesColumns = `
	s.id, s.root_name, s.rel_path, s.display_name, s.sort_key, s.search_key,
	s.choseong_key, s.kind, s.book_count, s.page_count, s.total_bytes, s.mtime,
	` + addedAtExpr + `, COALESCE(s.cover_kind, ''), COALESCE(s.cover_book_id, ''),
	COALESCE(s.cover_page_no, 0), COALESCE(s.cover_rel_path, ''), s.status,
	COALESCE(s.error, ''), s.scan_gen,
	COALESCE(cb.content_version, ''),
	COALESCE(pr.books_completed, 0), COALESCE(pr.books_started, 0),
	COALESCE(pr.pages_read, 0),
	pr.last_read_at,
	COALESCE((SELECT q.book_id FROM ud.progress q WHERE q.series_id = s.id
	           ORDER BY q.updated_at DESC, q.book_id LIMIT 1), ''),
	COALESCE((SELECT q.last_page FROM ud.progress q WHERE q.series_id = s.id
	           ORDER BY q.updated_at DESC, q.book_id LIMIT 1), 0)`

// addedAtExpr is A-8's single definition of "added", used by the projection and
// by SortAdded so the column a client sees and the column it sorts by can never
// drift apart. scope=added deliberately does NOT use it — see seriesWhere.
const addedAtExpr = `COALESCE(sn.first_seen_at, s.added_at)`

// seriesJoins attaches the cover book (for its content version), the per-series
// progress rollup and (A-8) the first-sighting stamp. Both ud reads are
// read-only, as the package doc requires.
//
// first_seen_at lives in a different database, so there is a real choice here:
// join it through the ATTACHed `ud` schema, or run a second query against
// package userdata and merge in Go. This joins, for two reasons that are
// properties of the endpoint rather than preferences. `total` is defined as the
// number of rows matching the whole filter *before* offset/limit (arch §7.5), so
// a Go-side merge would have to pull every matching id out of both databases on
// every request just to count them — at 10⁴ series that is FR-LIB-007 inverted.
// And `sort=added` has to order by the merged value before paging, which cannot
// be done after a LIMIT. The ATTACH is already there for ud.progress, already
// verified under a hammered pool (arch §3.7), and costs one more LEFT JOIN on a
// primary key.
//
// **The rollup joins `books` too, and that join is the point of E-47.** A page
// count belongs to the index, the page a reader stopped on belongs to user.db,
// and `percent` needs both in the same SUM. It is a join on the books primary
// key inside a subquery that already scans ud.progress once per request, so it
// adds a lookup per progress row and no extra pass. `LEFT JOIN`, not `JOIN`:
// progress survives a book disappearing from the index (that is what makes
// reading position reattach after a rescan), and an inner join would silently
// drop those rows out of the rollup — including out of `books_completed`, which
// would move a number this ruling never touched.
const seriesJoins = `
	FROM series s
	JOIN roots r ON r.name = s.root_name
	LEFT JOIN books cb ON cb.id = s.cover_book_id
	LEFT JOIN ud.series_seen sn ON sn.series_id = s.id
	LEFT JOIN (
		SELECT p.series_id,
		       SUM(p.completed)     AS books_completed,
		       SUM(1 - p.completed) AS books_started,
		       SUM(CASE WHEN p.completed = 1 THEN COALESCE(b.page_count, 0)
		                ELSE MIN(p.last_page, COALESCE(b.page_count, 0)) END) AS pages_read,
		       MAX(p.updated_at)    AS last_read_at
		FROM ud.progress p
		LEFT JOIN books b ON b.id = p.book_id
		GROUP BY p.series_id
	) pr ON pr.series_id = s.id`

func scanSeriesRow(sc interface{ Scan(...any) error }) (SeriesRow, error) {
	var r SeriesRow
	err := sc.Scan(&r.ID, &r.RootName, &r.RelPath, &r.DisplayName, &r.SortKey, &r.SearchKey,
		&r.ChoseongKey, &r.Kind, &r.BookCount, &r.PageCount, &r.TotalBytes, &r.Mtime,
		&r.AddedAt, &r.CoverKind, &r.CoverBookID, &r.CoverPageNo, &r.CoverRelPath,
		&r.Status, &r.Error, &r.ScanGen, &r.CoverCV,
		&r.Progress.BooksCompleted, &r.Progress.BooksStarted, &r.Progress.PagesRead,
		&r.Progress.LastReadAt, &r.Progress.LastBookID, &r.Progress.LastPage)
	r.Progress.BooksTotal = r.BookCount
	r.Progress.PagesTotal = r.PageCount
	return r, err
}

// ListSeries answers GET /api/series: filters, natural-sorted paging and the
// cross-database progress join, all in two statements (count + page).
func (db *DB) ListSeries(ctx context.Context, f SeriesFilter) (SeriesList, error) {
	where, args, err := seriesWhere(f)
	if err != nil {
		return SeriesList{}, err
	}
	orderBy, err := seriesOrder(f.Sort, f.Order)
	if err != nil {
		return SeriesList{}, err
	}
	limit := f.Limit
	switch {
	case limit == 0:
		limit = defaultSeriesLimit
	case limit < 0 || limit > maxSeriesLimit:
		return SeriesList{}, fmt.Errorf("index: limit %d out of range 1..%d: %w",
			f.Limit, maxSeriesLimit, ErrInvalidFilter)
	}
	offset := f.Offset
	if offset < 0 {
		return SeriesList{}, fmt.Errorf("index: offset %d is negative: %w", f.Offset, ErrInvalidFilter)
	}

	out := SeriesList{Offset: offset, Limit: limit}

	countQuery := `SELECT count(*) FROM series s JOIN roots r ON r.name = s.root_name` + where
	if err := db.sqldb.QueryRowContext(ctx, countQuery, args...).Scan(&out.Total); err != nil {
		return SeriesList{}, fmt.Errorf("counting series: %w", err)
	}
	if out.Total == 0 || offset >= out.Total {
		return out, nil
	}

	pageQuery := `SELECT` + seriesColumns + seriesJoins + where + ` ORDER BY ` + orderBy + ` LIMIT ? OFFSET ?`
	pageArgs := append(append([]any{}, args...), limit, offset)
	rows, err := db.sqldb.QueryContext(ctx, pageQuery, pageArgs...)
	if err != nil {
		return SeriesList{}, fmt.Errorf("listing series: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		r, err := scanSeriesRow(rows)
		if err != nil {
			return SeriesList{}, fmt.Errorf("scanning series: %w", err)
		}
		out.Items = append(out.Items, r)
	}
	if err := rows.Err(); err != nil {
		return SeriesList{}, fmt.Errorf("listing series: %w", err)
	}
	return out, nil
}

// GetSeries returns a series with its books, natural-sorted by ord.
func (db *DB) GetSeries(ctx context.Context, id string) (SeriesDetail, error) {
	row := db.sqldb.QueryRowContext(ctx,
		`SELECT`+seriesColumns+seriesJoins+` WHERE s.id = ?`, id)
	sr, err := scanSeriesRow(row)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return SeriesDetail{}, fmt.Errorf("series %q: %w", id, ErrNotFound)
	case err != nil:
		return SeriesDetail{}, fmt.Errorf("reading series %q: %w", id, err)
	}
	books, err := db.ListBooks(ctx, id)
	if err != nil {
		return SeriesDetail{}, err
	}
	return SeriesDetail{SeriesRow: sr, Books: books}, nil
}

// seriesWhere builds the shared predicate for the count and page queries.
func seriesWhere(f SeriesFilter) (string, []any, error) {
	var conds []string
	var args []any

	if !f.IncludeDisabledRoots {
		conds = append(conds, `r.enabled = 1`)
	}
	if len(f.Roots) > 0 {
		conds = append(conds, `s.root_name IN (`+placeholders(len(f.Roots))+`)`)
		for _, r := range f.Roots {
			args = append(args, r)
		}
	}
	switch f.Status {
	case "", "all":
	case "ok", "empty", "error":
		conds = append(conds, `s.status = ?`)
		args = append(args, f.Status)
	default:
		return "", nil, fmt.Errorf("index: status %q: %w", f.Status, ErrInvalidFilter)
	}

	if q := strings.TrimSpace(f.Query); q != "" {
		pattern := "%" + escapeLike(strings.ToLower(q)) + "%"
		if isChoseongQuery(q) {
			conds = append(conds,
				`(s.search_key LIKE ? ESCAPE '\' OR s.choseong_key LIKE ? ESCAPE '\')`)
			args = append(args, pattern, pattern)
		} else {
			conds = append(conds, `s.search_key LIKE ? ESCAPE '\'`)
			args = append(args, pattern)
		}
	}

	switch f.Progress {
	case "", ProgressAny:
	case ProgressReading:
		conds = append(conds,
			`EXISTS (SELECT 1 FROM ud.progress p WHERE p.series_id = s.id AND p.completed = 0)`)
	case ProgressDone:
		conds = append(conds,
			`s.book_count > 0 AND (SELECT count(*) FROM ud.progress p
			  WHERE p.series_id = s.id AND p.completed = 1) >= s.book_count`)
	case ProgressUnread:
		conds = append(conds, `NOT EXISTS (SELECT 1 FROM ud.progress p WHERE p.series_id = s.id)`)
	default:
		return "", nil, fmt.Errorf("index: progress %q: %w", f.Progress, ErrInvalidFilter)
	}

	// Amendment A-8. The predicate is `first_seen_at >= cutoff` and nothing
	// else: never the COALESCE of addedAtExpr, or a rebuilt index would push the
	// whole library into 최근 추가 through the fallback, which is the failure the
	// amendment exists to prevent. A series with no row is excluded, because
	// NULL >= x is not true — under-reporting is the safe direction and the next
	// scan writes the row (arch §3.6 rule 7).
	//
	// It is an EXISTS rather than a reference to seriesJoins' `sn`, because the
	// count query joins only `roots`; sharing one predicate between the count
	// and the page is worth more than one saved subquery, and SQLite plans both
	// through series_seen's primary key.
	switch f.Scope {
	case "", ScopeAll:
	case ScopeAdded:
		conds = append(conds, `EXISTS (SELECT 1 FROM ud.series_seen sn2
			WHERE sn2.series_id = s.id AND sn2.first_seen_at >= ?)`)
		args = append(args, f.RecentlyAddedCutoff)
	default:
		return "", nil, fmt.Errorf("index: scope %q: %w", f.Scope, ErrInvalidFilter)
	}

	if len(conds) == 0 {
		return "", args, nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args, nil
}

// seriesOrder maps (sort, order) to an ORDER BY clause. Every clause ends with
// the sort key and the id so paging is stable when the primary key ties.
func seriesOrder(sort, order string) (string, error) {
	if sort == "" {
		sort = SortName
	}
	var expr, defaultDir string
	switch sort {
	case SortName:
		expr, defaultDir = "s.sort_key", "ASC"
	case SortMtime:
		expr, defaultDir = "s.mtime", "DESC"
	case SortSize:
		expr, defaultDir = "s.total_bytes", "DESC"
	case SortBooks:
		expr, defaultDir = "s.book_count", "DESC"
	case SortAdded:
		expr, defaultDir = addedAtExpr, "DESC"
	case SortRecent:
		expr, defaultDir = "pr.last_read_at", "DESC"
	default:
		return "", fmt.Errorf("index: sort %q: %w", sort, ErrInvalidFilter)
	}

	dir := defaultDir
	switch strings.ToLower(order) {
	case "":
	case "asc":
		dir = "ASC"
	case "desc":
		dir = "DESC"
	default:
		return "", fmt.Errorf("index: order %q: %w", order, ErrInvalidFilter)
	}

	if sort == SortRecent {
		// "series never read sort last" (arch §7.5) — in either direction.
		return "(pr.last_read_at IS NULL) ASC, " + expr + " " + dir + ", s.sort_key ASC, s.id ASC", nil
	}
	if sort == SortName {
		return expr + " " + dir + ", s.id ASC", nil
	}
	return expr + " " + dir + ", s.sort_key ASC, s.id ASC", nil
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// escapeLike neutralises the LIKE metacharacters in a user-supplied query. The
// statements pair it with ESCAPE '\'.
func escapeLike(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '%', '_', '\\':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// isChoseongQuery reports whether the query is made only of ASCII, spaces and
// compatibility jamo (U+3131..U+314E), which is the arch §4.8 condition for
// also matching choseong_key. A query containing full Hangul syllables or any
// other script matches the name only.
func isChoseongQuery(q string) bool {
	for _, r := range q {
		switch {
		case r <= unicode.MaxASCII:
		case r >= 0x3131 && r <= 0x314E:
		default:
			return false
		}
	}
	return true
}
