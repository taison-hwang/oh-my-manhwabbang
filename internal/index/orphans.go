package index

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// SplitOrphan is a ud.progress row whose book_id is not in `books`.
//
// # Why this query exists
//
// A book id is a pure function of the root name and the root-relative path
// (internal/ids), so it survives an index rebuild — that is the whole point of
// the scheme, and it is what lets user.db be a physically separate file. What it
// does not survive is the book moving out from under that pair. Two ways that
// has happened to this library:
//
//   - D-73 taught the scanner to look inside an archive for chapter directories
//     and split 484 containers into 6,097 volumes. Every one of those containers
//     stopped being a book, so 23 rows named an id that matches nothing.
//   - files were renamed on disk — a leading `[만화] ` tag stripped from 28
//     paths — and one directory moved between roots. Same effect, different
//     cause: the path changed, so the id changed.
//
// Neither is repaired by rescanning. A scan derives ids from what is on disk
// *now*, so it reproduces the current id and never the old one; `이누야샤` is
// `r3lifiza3mmzcqzc` at its current path and the row holds `ghnr2amimty5niyg`,
// which is that book's id under the name it used to have. Rescanning reattaches
// progress only when a path comes *back* unchanged — a remounted drive, a
// deleted index — which is what the "reattach after a rescan" promise means.
//
// The row keeps what a repair needs. `book_path` is the container's or file's
// root-relative path (the writer stores the container's path for a nested volume
// too, so the column means the same thing on both sides of a split), and
// `page_count` is the baseline the reader last agreed that file was — the sum
// the destination must still add up to for the mapping to be the same pages.
// internal/repair owns which relocations are allowed and does the arithmetic;
// this file only reports rows and answers "what is at this path".
//
// # `ud.` stays read-only here
//
// This is a SELECT and nothing else. The repair's write goes through package
// userdata's own handle (userdata.DB.RepairSplit), because no transaction may
// span both databases and `make lint` greps this package for a write verb
// against the attached schema (see the package comment in open.go).
type SplitOrphan struct {
	BookID    string
	SeriesID  string
	RootName  string
	BookPath  string
	LastPage  int
	PageCount int
	Completed bool
	StartedAt int64
	UpdatedAt int64
}

// Location is a root name and a root-relative path: the pair a book id is
// derived from, and therefore the unit a relocation moves between.
type Location struct {
	RootName string
	RelPath  string
}

// SplitVolume is one book at a Location, in reading order. A file that is its
// own book is the one-element case, which is what makes an ordinary rename and a
// container split the same arithmetic downstream.
type SplitVolume struct {
	BookID string
	// SeriesID is the series the book belongs to *now*. Carried because a
	// progress row keys on both: the 이어보기 shelf resolves a book, but the
	// shelf's percentage, the 읽는 중 filter and the 완독 scope all group by
	// series_id. A row moved with a stale series id is reachable and invisible
	// at the same time, which is a worse failure than the one being repaired
	// because nothing about it looks broken.
	SeriesID  string
	Ord       int
	PageCount int
}

const splitOrphanSQL = `
SELECT p.book_id, p.series_id, p.root_name, p.book_path, p.last_page,
       p.page_count, p.completed, p.started_at, p.updated_at
FROM ud.progress p
LEFT JOIN books b ON b.id = p.book_id
WHERE b.id IS NULL
ORDER BY p.book_id`

// SplitOrphans returns every progress row the index cannot resolve to a book.
//
// The result is small by construction — it is bounded by the number of books the
// reader has ever opened that are no longer books at that path — so it loads
// whole rather than paging. A library with no orphans runs one statement and
// returns nil.
func (db *DB) SplitOrphans(ctx context.Context) ([]SplitOrphan, error) {
	rows, err := db.sqldb.QueryContext(ctx, splitOrphanSQL)
	if err != nil {
		return nil, fmt.Errorf("index: listing orphaned progress: %w", err)
	}
	defer rows.Close()

	var out []SplitOrphan
	for rows.Next() {
		var o SplitOrphan
		var completed int
		if err := rows.Scan(&o.BookID, &o.SeriesID, &o.RootName, &o.BookPath,
			&o.LastPage, &o.PageCount, &completed, &o.StartedAt, &o.UpdatedAt); err != nil {
			return nil, fmt.Errorf("index: scanning orphaned progress: %w", err)
		}
		o.Completed = completed != 0
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("index: reading orphaned progress: %w", err)
	}
	return out, nil
}

// BooksAt answers, for each Location asked about, the books the index holds
// there in reading order. A Location with no books is absent from the map rather
// than present and empty, so a caller can tell "nothing there" from "I did not
// ask" without a second lookup.
//
// **Ordinary books are included, not filtered to `inner_path <> ”`.** The
// filter would look like a safety rail — an ordinary book must not be a volume
// of itself — but it is unnecessary and it breaks the rename case. Unnecessary:
// a book with an empty inner path has exactly the id BookID(root, rel_path)
// gives, so if one sat at an orphan's own Location the row would have resolved
// and never been an orphan at all. Breaking: after a rename the destination
// Location holds one ordinary book, and that book is the answer.
func (db *DB) BooksAt(ctx context.Context, locs []Location) (map[Location][]SplitVolume, error) {
	if len(locs) == 0 {
		return nil, nil
	}
	stmt, err := db.sqldb.PrepareContext(ctx, `
		SELECT id, series_id, ord, page_count
		FROM books
		WHERE root_name = ? AND rel_path = ?
		ORDER BY ord`)
	if err != nil {
		return nil, fmt.Errorf("index: preparing book lookup: %w", err)
	}
	defer stmt.Close()

	out := make(map[Location][]SplitVolume, len(locs))
	for _, loc := range locs {
		if _, done := out[loc]; done {
			continue
		}
		vols, err := booksAtOne(ctx, stmt, loc)
		if err != nil {
			return nil, err
		}
		if len(vols) > 0 {
			out[loc] = vols
		}
	}
	return out, nil
}

func booksAtOne(ctx context.Context, stmt *sql.Stmt, loc Location) ([]SplitVolume, error) {
	rows, err := stmt.QueryContext(ctx, loc.RootName, loc.RelPath)
	if err != nil {
		return nil, fmt.Errorf("index: listing books at %q: %w", loc.RelPath, err)
	}
	defer rows.Close()

	var out []SplitVolume
	for rows.Next() {
		var v SplitVolume
		var pc sql.NullInt64
		if err := rows.Scan(&v.BookID, &v.SeriesID, &v.Ord, &pc); err != nil {
			return nil, fmt.Errorf("index: scanning book at %q: %w", loc.RelPath, err)
		}
		v.PageCount = int(pc.Int64)
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("index: reading books at %q: %w", loc.RelPath, err)
	}
	return out, nil
}

// RootNames lists the configured root names, which is the set a relocation may
// move a path between. Sorted, so a plan built from it is deterministic.
func (db *DB) RootNames(ctx context.Context) ([]string, error) {
	rows, err := db.sqldb.QueryContext(ctx, `SELECT name FROM roots ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("index: listing root names: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, fmt.Errorf("index: scanning root name: %w", err)
		}
		out = append(out, strings.TrimSpace(n))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("index: reading root names: %w", err)
	}
	return out, nil
}

// Misfiled is a progress row that resolves to a book but records a different
// series than the book belongs to.
//
// # Why the row can be wrong in this particular way
//
// `progress` carries both ids, and they answer different questions: `book_id`
// is what 이어보기 opens, `series_id` is what the shelf's percentage, the
// 읽는 중 filter and the 완독 scope group by. A row whose book resolves but
// whose series does not is therefore reachable and invisible at the same
// moment, which looks like nothing is wrong at all.
//
// It happens when a file is renamed: both ids are hashes of a path, so both
// change, and any carry that moves the book id without the series id leaves the
// row half-moved. This library had 27 such rows after a first cut of the repair
// shipped without carrying the series.
//
// Nothing here is inference. `books.series_id` is the index's own answer to
// "what series is this book in", so the correction is a lookup, not a guess.
type Misfiled struct {
	BookID      string
	BookPath    string
	StoredSeria string
	ActualSeria string
}

// MisfiledProgress lists progress rows whose series disagrees with the index.
//
// Cheap on a clean library: it is an equality test on a join that already has
// both columns, and it returns nothing.
func (db *DB) MisfiledProgress(ctx context.Context) ([]Misfiled, error) {
	rows, err := db.sqldb.QueryContext(ctx, `
		SELECT p.book_id, p.book_path, p.series_id, b.series_id
		FROM ud.progress p
		JOIN books b ON b.id = p.book_id
		WHERE p.series_id <> b.series_id
		ORDER BY p.book_id`)
	if err != nil {
		return nil, fmt.Errorf("index: listing misfiled progress: %w", err)
	}
	defer rows.Close()

	var out []Misfiled
	for rows.Next() {
		var m Misfiled
		if err := rows.Scan(&m.BookID, &m.BookPath, &m.StoredSeria, &m.ActualSeria); err != nil {
			return nil, fmt.Errorf("index: scanning misfiled progress: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("index: reading misfiled progress: %w", err)
	}
	return out, nil
}

// Vanished is a progress row whose book is not in the index and whose root has
// just been walked from end to end.
//
// # Why the root matters more than the row
//
// "The book is missing" is not evidence that the file is gone. An unmounted
// drive, a root that failed to enumerate, a cancelled scan and a per-series
// rescan all produce exactly the same absence, which is why NFR-DAT-004 keeps
// reading history for a book the index cannot see — and why that rule saved 54
// of 60 rows on this library, where "vanished" turned out to mean "renamed".
//
// A completed sweep is different in kind. The scanner only sweeps a root it
// walked without error, without cancellation and without narrowing to named
// series (scanner.decideSweep), so a row still unresolved *after* that walk is
// one the filesystem was asked about and did not have. The caller passes only
// those roots; this query trusts the caller for that and nothing else.
//
// It is the last thing to run. A row that could be carried across a move or
// reattached to a split has already been by the time this asks, so anything
// still here has had every chance.
type Vanished struct {
	BookID    string
	RootName  string
	BookPath  string
	LastPage  int
	PageCount int
	UpdatedAt int64
}

// VanishedProgress lists progress rows the given roots no longer account for.
//
// An empty root list returns nothing rather than everything: "no root finished
// cleanly" must not read as "delete all reading history".
func (db *DB) VanishedProgress(ctx context.Context, roots []string) ([]Vanished, error) {
	if len(roots) == 0 {
		return nil, nil
	}
	set := make(map[string]struct{}, len(roots))
	for _, r := range roots {
		set[r] = struct{}{}
	}
	rows, err := db.sqldb.QueryContext(ctx, `
		SELECT p.book_id, p.root_name, p.book_path, p.last_page, p.page_count, p.updated_at
		FROM ud.progress p
		LEFT JOIN books b ON b.id = p.book_id
		WHERE b.id IS NULL
		ORDER BY p.book_id`)
	if err != nil {
		return nil, fmt.Errorf("index: listing vanished progress: %w", err)
	}
	defer rows.Close()

	var out []Vanished
	for rows.Next() {
		var v Vanished
		if err := rows.Scan(&v.BookID, &v.RootName, &v.BookPath,
			&v.LastPage, &v.PageCount, &v.UpdatedAt); err != nil {
			return nil, fmt.Errorf("index: scanning vanished progress: %w", err)
		}
		// Filtered here rather than in SQL: the root list is short and building a
		// variadic IN clause for it would be the only dynamic SQL in this file.
		if _, ok := set[v.RootName]; ok {
			out = append(out, v)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("index: reading vanished progress: %w", err)
	}
	return out, nil
}
