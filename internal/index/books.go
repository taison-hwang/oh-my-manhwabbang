package index

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Book mirrors one row of the `books` table: a readable unit inside a series —
// a ZIP, a directory of images, or a PDF.
type Book struct {
	ID       string
	SeriesID string
	RootName string
	RelPath  string
	// InnerPath is the book's path *inside* its container, for a volume that
	// lives in a nested archive (arch §4.12). Empty for every ordinary book,
	// which is what keeps (root_name, rel_path, inner_path) a drop-in
	// replacement for the old (root_name, rel_path) key.
	InnerPath   string
	DisplayName string
	// SortKey is natsort.Key over the series-relative path; Ord is its
	// materialised 0-based rank so the API never re-sorts.
	SortKey []byte
	Ord     int
	// Kind is one of the `source.Kind*` constants — "zip", "dir", "pdf" and the
	// nested spellings D-70/D-71/D-73 added (conflict resolution C-4: a *book*
	// that is a directory is "dir"; a *series* that is one is "folder").
	// internal/source is the authority, and `contractcheck` compares those
	// constants against the client's `BOOK_KINDS`; enumerating them here as well
	// is how this comment went three decisions out of date.
	Kind       string
	PageCount  int64
	TotalBytes int64
	FileSize   int64
	FileMtime  int64
	// DirFingerprint is the FNV-1a digest of the book directory's direct
	// children; empty for zip and pdf (arch §4.6).
	DirFingerprint string
	// ContentVersion is the cache buster carried in every page URL as ?v=
	// (D-17, arch §5.3).
	ContentVersion string
	// DimsState is "none", "partial" or "done".
	DimsState string
	// Status is "ok", "error", "encrypted", "empty" or "unsupported"
	// (FR-IDX-010).
	Status  string
	Error   string
	ScanGen int64
}

// BookProgress is the ud.progress row joined onto a book, if any.
type BookProgress struct {
	LastPage  int
	PageCount int
	Completed bool
	StartedAt int64
	UpdatedAt int64
}

// BookRow is a book plus its reading progress. Progress is nil when the book
// has never been opened.
type BookRow struct {
	Book
	Progress *BookProgress
}

const bookColumns = `
	b.id, b.series_id, b.root_name, b.rel_path, b.inner_path, b.display_name, b.sort_key, b.ord,
	b.kind, b.page_count, b.total_bytes, b.file_size, b.file_mtime,
	COALESCE(b.dir_fingerprint, ''), b.content_version, b.dims_state, b.status,
	COALESCE(b.error, ''), b.scan_gen,
	p.last_page, p.page_count, p.completed, p.started_at, p.updated_at`

const bookJoins = `
	FROM books b
	LEFT JOIN ud.progress p ON p.book_id = b.id`

func scanBookRow(sc interface{ Scan(...any) error }) (BookRow, error) {
	var r BookRow
	var lastPage, progPages, completed, startedAt, updatedAt sql.NullInt64
	err := sc.Scan(&r.ID, &r.SeriesID, &r.RootName, &r.RelPath, &r.InnerPath, &r.DisplayName, &r.SortKey,
		&r.Ord, &r.Kind, &r.PageCount, &r.TotalBytes, &r.FileSize, &r.FileMtime,
		&r.DirFingerprint, &r.ContentVersion, &r.DimsState, &r.Status, &r.Error, &r.ScanGen,
		&lastPage, &progPages, &completed, &startedAt, &updatedAt)
	if err != nil {
		return BookRow{}, err
	}
	if lastPage.Valid {
		r.Progress = &BookProgress{
			LastPage:  int(lastPage.Int64),
			PageCount: int(progPages.Int64),
			Completed: completed.Int64 != 0,
			StartedAt: startedAt.Int64,
			UpdatedAt: updatedAt.Int64,
		}
	}
	return r, nil
}

const selectBookByID = `SELECT` + bookColumns + bookJoins + ` WHERE b.id = ?`

// GetBook returns one book with its progress, or ErrNotFound.
func (db *DB) GetBook(ctx context.Context, id string) (BookRow, error) {
	row := db.stmts.getBook.QueryRowContext(ctx, id)
	r, err := scanBookRow(row)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return BookRow{}, fmt.Errorf("book %q: %w", id, ErrNotFound)
	case err != nil:
		return BookRow{}, fmt.Errorf("reading book %q: %w", id, err)
	}
	return r, nil
}

// ListBooks returns a series' books ordered by ord, which the scanner
// materialised from the natural sort (FR-IDX-007).
func (db *DB) ListBooks(ctx context.Context, seriesID string) ([]BookRow, error) {
	rows, err := db.sqldb.QueryContext(ctx,
		`SELECT`+bookColumns+bookJoins+` WHERE b.series_id = ? ORDER BY b.ord ASC, b.id ASC`, seriesID)
	if err != nil {
		return nil, fmt.Errorf("listing books of series %q: %w", seriesID, err)
	}
	defer rows.Close()

	var out []BookRow
	for rows.Next() {
		r, err := scanBookRow(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning book: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing books of series %q: %w", seriesID, err)
	}
	return out, nil
}

// Neighbours returns the ids of the books immediately before and after id
// within its series, by ord. Either is "" at the ends. FR-VWR-010.
func (db *DB) Neighbours(ctx context.Context, id string) (prev, next string, err error) {
	var seriesID string
	var ord int
	err = db.sqldb.QueryRowContext(ctx, `SELECT series_id, ord FROM books WHERE id = ?`, id).
		Scan(&seriesID, &ord)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", "", fmt.Errorf("book %q: %w", id, ErrNotFound)
	case err != nil:
		return "", "", fmt.Errorf("reading book %q: %w", id, err)
	}

	scanOne := func(query string) (string, error) {
		var got string
		qErr := db.sqldb.QueryRowContext(ctx, query, seriesID, ord, ord, id).Scan(&got)
		if errors.Is(qErr, sql.ErrNoRows) {
			return "", nil
		}
		if qErr != nil {
			return "", fmt.Errorf("reading neighbours of book %q: %w", id, qErr)
		}
		return got, nil
	}

	if prev, err = scanOne(`SELECT id FROM books WHERE series_id = ?
		AND (ord < ? OR (ord = ? AND id < ?))
		ORDER BY ord DESC, id DESC LIMIT 1`); err != nil {
		return "", "", err
	}
	if next, err = scanOne(`SELECT id FROM books WHERE series_id = ?
		AND (ord > ? OR (ord = ? AND id > ?))
		ORDER BY ord ASC, id ASC LIMIT 1`); err != nil {
		return "", "", err
	}
	return prev, next, nil
}

// ContinueItem is one entry of the FR-LIB-010 "이어보기" shelf: a book that has
// been started and not finished, with just enough of its series to render a card.
type ContinueItem struct {
	Book        BookRow
	SeriesID    string
	SeriesName  string
	HasCover    bool
	SeriesCover string
}

// latestPerSeries names, for every series that has any started-but-unfinished
// book, the ONE book the 이어보기 shelf is allowed to show. The ranking, in
// order: a book that can actually be READ beats one that cannot; then the
// furthest along the series (greatest `ord`); then the greatest `id` as a
// deterministic tie-break. It is spliced into ListContinue's WHERE clause.
//
// READABILITY PREFERS, IT NEVER EXCLUDES. `status = 'ok'` is the whole of
// arch §4.11's readable case: the scanner writes exactly 'ok', 'error',
// 'encrypted', 'empty' and 'unsupported' (internal/archive's Status constants,
// reached through scanner/classify.go), and it reaches for 'empty' the moment a
// successful listing yields no pages. No second `page_count > 0` clause is
// needed here — but that is a SCANNER CONVENTION, not a guarantee, and the
// difference is worth stating because the sentence this replaces claimed the
// stronger thing. `books` carries no CHECK tying `status` to `page_count`
// (schema.go), `scanner.go`'s `indexBook` initialises every result as 'ok' with
// zero pages and only downgrades it to 'empty' at the end of a successful
// listing, and `index_test.go` writes 'ok'/0 rows directly through the writer.
// So an 'ok' book with no pages is representable, and only a completed scan's
// classification step keeps it out of the database. Ranking one first would
// cost the reader an open that fails rather than a wrong shelf, which is what a
// stale index does anyway; adding the clause would cost a query that is already
// the expensive one. It stays out, on those terms rather than on an invariant.
//
// It is a ranking key rather than a filter for a reason. A broken volume left
// the reader nothing to read, so it must not out-rank the volume they went back
// to — httpapi/progress.go accepts `page_count == 0` ("length unknown") for
// exactly these books, and userdata.PutProgress can never auto-complete a row
// whose PageCount is 0, so such a row stays unfinished and would otherwise win
// its partition for ever.
// But writing `AND b2.status = 'ok'` in here instead would make a series whose
// only started books are ALL broken match nothing and vanish from 이어보기
// altogether — trading one silent behaviour change for another. Ranking keeps
// both: the readable volume wins when there is one, and a series with none
// still shows its latest.
//
// WHY A WINDOW FUNCTION. `ROW_NUMBER() OVER (PARTITION BY …)` is the first use
// of a window function in this repository, so it was verified rather than
// assumed: modernc.org/sqlite v1.54.0 reports `sqlite_version() = 3.53.3`, well
// past the 3.25 that introduced them, and the transpiled amalgamation compiles
// the window-function code — a probe of exactly this query shape returned the
// expected rows. See the plan note on ListContinue for the alternatives that
// were measured against it.
//
// `r.enabled = 1` is deliberately NOT repeated in here. Every book of a series
// hangs off that series' single root, so enabled-ness is uniform within a
// partition: a winner picked from a disabled root's series simply never
// survives the outer join, and one picked from an enabled root is the same
// book either way. That argument leans on the OUTER clause doing its job, which
// went unpinned for as long as this paragraph existed —
// TestListContinue_disabledRoot_isOffTheShelf pins it now. `p2.completed = 0` on
// the other hand is load-bearing — drop it and a series whose LAST volume is
// finished would elect that finished volume, match nothing in the outer query,
// and vanish from the shelf entirely even though an earlier volume is still in
// progress (TestListContinue_seriesSurvivesWhenItsLastVolumeIsFinished).
//
// `b2.ord DESC` is the user's rule itself and is the one key here that no
// fixture could see for a while: every book id in `index_test.go` used to sort
// the same way as its `ord`, so `ORDER BY (b2.status='ok') DESC, b2.id DESC`
// passed the whole suite while shipping an arbitrary volume per series. Real
// ids are base32 SHA-256 digests over the root name and the root-relative path
// (`internal/ids`) and have no relation to `ord` at all.
// TestListContinue_electionRanksByOrd_whenTheIDOrderDisagrees is the fixture
// where the two orders disagree; it is what makes this line falsifiable.
const latestPerSeries = `
	SELECT id FROM (
		SELECT b2.id AS id,
		       ROW_NUMBER() OVER (
		           PARTITION BY b2.series_id
		           ORDER BY (b2.status = 'ok') DESC, b2.ord DESC, b2.id DESC
		       ) AS rn
		FROM ud.progress p2
		JOIN books b2 ON b2.id = p2.book_id
		WHERE p2.completed = 0
	) WHERE rn = 1`

// seriesActivity is when the shelf last saw ANY unfinished book of the series
// the outer row belongs to. It is the shelf's ordering key, and it is correlated
// on `b.series_id`, so it only makes sense spliced into ListContinue.
//
// It exists because the elected card's own `updated_at` stopped being a
// statement about the series the moment one card per series was elected by
// `ord`. A reader who peeked at 07권 a month ago and read 01권 five minutes ago
// is reading that series right now; 07권 is still the card 뒷화 우선 asks for,
// but ranking the shelf by 07권's month-old timestamp buries the series at the
// bottom — and `docs/ui-spec.md` §4.3 caps the track at five cards, which
// `web/src/features/library/useLibrary.ts` asks this endpoint for as
// `CONTINUE_MAX_CARDS = 5`, so with six series in progress it falls off the
// shelf entirely. The card is chosen by the series' shape; the shelf is ordered
// by the series' activity.
//
// `p3.completed = 0` here is as load-bearing as its twin in the election, and
// for a different reason: without it a series whose most recent event was
// FINISHING a volume would be lifted up the shelf by that completion. 이어보기 is
// the shelf of what is left unfinished, so a reader who closed off 03권 last
// night must not out-rank a series they are in the middle of. Pinned by
// TestListContinue_seriesActivity_ignoresFinishedVolumes.
const seriesActivity = `
	SELECT MAX(p3.updated_at)
	FROM ud.progress p3
	JOIN books b3 ON b3.id = p3.book_id
	WHERE b3.series_id = b.series_id AND p3.completed = 0`

// ListContinue returns started-but-unfinished books, the most recently read
// SERIES first, AT MOST ONE PER SERIES. It is the one query that genuinely needs
// both databases at once, which is why user.db is attached rather than opened
// separately.
//
// The one-per-series rule is the user's: a series read across several volumes
// used to occupy as many cards as it had unfinished volumes. The survivor is
// the LATER volume (뒷화 우선) — greatest `ord`, the same in-series ordering
// Neighbours navigates by — and NOT the most recently read or the furthest
// read. Those come apart in practice and the reported case is exactly that
// shape: 「사랑」 07권 at page 1 of 113 wins over 01권 at page 24 of 116. A
// volume that cannot be opened at all is ranked below one that can, first of
// all the keys; see latestPerSeries.
//
// The de-duplication is inside the SQL, before LIMIT, on purpose. Filtering
// duplicates out in Go after the fact would silently shrink the shelf — a
// `limit=20` request that happened to fetch six volumes of one series would
// render fifteen cards.
//
// PLAN. Two things cost the driving loop, and only one of them is negotiable.
// Without de-duplication SQLite walks `ix_progress_continue` (the partial index
// on `progress(updated_at DESC) WHERE completed = 0`) and needs a temp b-tree
// only "FOR LAST TERM OF ORDER BY", the `b.id` tie-break; on the fixture in the
// table below that query answers a `limit=5` in 0.88 ms and a `limit=500` in
// 13.2 ms — the gap IS the short-circuit. Once the shelf is ordered by the SERIES' activity
// rather than by a column of the driving row, that index's ordering is worth
// nothing to the ORDER BY, whichever way the query is written, and the whole
// result is sorted in a temp b-tree before LIMIT can help. An earlier version of
// this comment said the regression was unavoidable because "no index can remove
// it". The index half is true; the conclusion was drawn too early, so four forms
// were written out and measured against each other.
//
// WHAT THE MEASUREMENT SUPPORTS. Every form measured returns BYTE-IDENTICAL
// rows at limit 5, 20 and 500 on every fixture tried. That is the part worth
// recording, and it is the only reason one could ever be swapped for another.
//
// WHAT IT DOES NOT SUPPORT — A RANKING. This comment used to carry a table
// ordering five forms by speed. A re-measurement inverts it, so it is gone
// rather than corrected: the order depends on a property of the LIBRARY that the
// old fixture line ("2 000 series / 10 000 books / 600 unfinished rows") did not
// name at all — how many DISTINCT SERIES those unfinished rows cover, which is
// precisely the number of rows the sort must handle before LIMIT. Holding that
// fixture fixed and moving only the covered-series count, at limit=5,
// best-of-25, three interleaved passes, no ANALYZE (nothing in the app runs one):
//
//	covered series                 60         200         600
//	this one                       2.99-3.04  6.16        10.78-10.91
//	window election+MAX in one
//	  derived table (id, act)      3.06-3.08  5.78-5.83   7.83-7.85
//	NOT EXISTS + correlated MAX    3.50-3.54  6.90-6.92   10.87-12.51
//	NOT EXISTS + grouped CTE       3.62-3.63  9.59-9.62   27.8-28.7
//
// The derived-table form is 2 % slower at 60, 6 % faster at 200 and 28 % faster
// at 600: the sign of the comparison changes inside one fixture description. The
// old table ranked it THIRD and this form first. At 200 covered series the old
// table's absolute numbers also reproduce almost exactly (it read 6.33-6.44 for
// this form where 6.16 is measured, and 9.38-9.84 for the grouped CTE where
// 9.59-9.62 is), which is how the missing variable was identified — the old
// figures were never wrong, they were unlabelled. A ranking that inverts under
// re-measurement is not a fact about the query, so none is asserted. If the
// shelf ever has to serve hundreds of series in progress at once, the
// derived-table form is the first thing to re-measure; at the size the product
// ships there is nothing between them. The NOT EXISTS forms have an independent
// mark against them whatever the clock says: they state the ranking rule twice,
// once inverted by hand as an "is-anything-better-than-me" predicate, which is
// exactly how a selection key and an ordering key drift apart.
//
// THE HONEST COST, AT THE SIZE THE PRODUCT SHIPS. On 964 series / 11 261 books,
// limit=5, best-of-25: the un-de-duplicated query answers in 0.40 ms and this
// one in 0.95 ms for a plausible 12-unfinished library. The multiplier is real
// and the absolute number is not — both are far under anything a five-card shelf
// can notice. Cost tracks the number of SERIES with unfinished reading, not the
// size of the library: on that same library this query costs 1.5 ms at 50 such
// series, 5.0 ms at 200 and 13.8 ms at 600, while 600 unfinished rows spread
// over only 60 series cost 5.0 ms — the same rows, a third of the time. Growing
// the library itself from 964/11 261 to 2 000/10 000 at a fixed 600 covered
// series moves it by ~20 %, far less than that spread. It no longer
// short-circuits under LIMIT either: `limit=500` costs at most ~70 % more than
// `limit=5` here, where the pre-de-duplication query spans 15x. None of it is
// worth optimising further: the set being sorted is the user's unfinished books,
// and 600 series in progress at once is already a library far larger than the
// shelf will face.
//
// The outer `p.completed = 0` is now STRUCTURALLY REDUNDANT and is kept anyway.
// `progress` is keyed by `book_id`, so the row `p` names is the same row the
// election already tested — a completed book cannot reach here. It is left in
// place because it states the endpoint's contract at the point a reader looks
// for it, and because deleting it would make the query silently depend on the
// election's filter for its correctness. It is not, however, load-bearing, and a
// future reader must not read it as the thing that keeps finished books off the
// shelf — `p2.completed = 0` in the election is. Deleting this one leaves both
// packages green; that is expected here, not a coverage gap. `r.enabled = 1` on
// the same line IS load-bearing (see latestPerSeries).
func (db *DB) ListContinue(ctx context.Context, limit int) ([]ContinueItem, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := db.sqldb.QueryContext(ctx, `
		SELECT`+bookColumns+`,
		       s.display_name, COALESCE(s.cover_kind, '')
		FROM ud.progress p
		JOIN books b  ON b.id = p.book_id
		JOIN series s ON s.id = b.series_id
		JOIN roots r  ON r.name = s.root_name
		WHERE p.completed = 0 AND r.enabled = 1
		  AND b.id IN (`+latestPerSeries+`)
		ORDER BY (`+seriesActivity+`) DESC, b.id ASC
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("listing continue-reading books: %w", err)
	}
	defer rows.Close()

	var out []ContinueItem
	for rows.Next() {
		var item ContinueItem
		var seriesName, coverKind string
		var lastPage, progPages, completed, startedAt, updatedAt sql.NullInt64
		b := &item.Book
		err := rows.Scan(&b.ID, &b.SeriesID, &b.RootName, &b.RelPath, &b.InnerPath, &b.DisplayName, &b.SortKey,
			&b.Ord, &b.Kind, &b.PageCount, &b.TotalBytes, &b.FileSize, &b.FileMtime,
			&b.DirFingerprint, &b.ContentVersion, &b.DimsState, &b.Status, &b.Error, &b.ScanGen,
			&lastPage, &progPages, &completed, &startedAt, &updatedAt,
			&seriesName, &coverKind)
		if err != nil {
			return nil, fmt.Errorf("scanning continue-reading book: %w", err)
		}
		if lastPage.Valid {
			b.Progress = &BookProgress{
				LastPage:  int(lastPage.Int64),
				PageCount: int(progPages.Int64),
				Completed: completed.Int64 != 0,
				StartedAt: startedAt.Int64,
				UpdatedAt: updatedAt.Int64,
			}
		}
		item.SeriesID = b.SeriesID
		item.SeriesName = seriesName
		item.HasCover = coverKind != ""
		item.SeriesCover = coverKind
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing continue-reading books: %w", err)
	}
	return out, nil
}
