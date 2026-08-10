package httpapi

import (
	"math"

	"shelf/internal/index"
	"shelf/internal/userdata"
)

// Storage rows → wire types. Every mapping in this file exists so that the
// shape of a table and the shape of the contract can move independently: a
// column added to `series` does not appear on the wire until somebody writes it
// here, and a contract field cannot silently vanish because a column was
// renamed.

// toSeriesSummary maps one library row.
func toSeriesSummary(row index.SeriesRow) SeriesSummary {
	return SeriesSummary{
		ID:         row.ID,
		RootName:   row.RootName,
		Name:       row.DisplayName,
		Path:       row.RelPath,
		Kind:       row.Kind,
		BookCount:  row.BookCount,
		PageCount:  row.PageCount,
		TotalBytes: row.TotalBytes,
		Mtime:      row.Mtime,
		AddedAt:    row.AddedAt,
		Status:     row.Status,
		Error:      nullableString(row.Error),
		// FR-LIB-008: no cover of any kind means the UI renders its text
		// placeholder rather than requesting an image that will 404.
		HasCover: row.CoverKind != "",
		CoverCV:  nullableString(row.CoverCV),
		Progress: toSeriesProgress(row.Progress),
	}
}

// toSeriesProgress is the FR-STT-002 rollup.
func toSeriesProgress(p index.SeriesProgress) SeriesProgress {
	out := SeriesProgress{
		BooksTotal:     p.BooksTotal,
		BooksCompleted: p.BooksCompleted,
		BooksStarted:   p.BooksStarted,
		Percent:        percent(p.BooksCompleted, p.BooksTotal),
		LastReadAt:     p.LastReadAt,
	}
	// last_book_id / last_page describe one specific book. They are null
	// together with last_read_at, never independently: a "continue reading"
	// target with no time attached would be a card the UI could not order.
	if p.LastBookID != "" {
		id := p.LastBookID
		page := p.LastPage
		out.LastBookID = &id
		out.LastPage = &page
	}
	return out
}

// percent is books_completed/books_total*100 rounded to one decimal place, and
// exactly 0 when the series holds no books.
//
// The zero case is spelled out in the contract (arch §7.3: "*** exactly 0 when
// books_total === 0 ***") because the obvious implementation divides by zero
// and JSON has no way to say NaN — `encoding/json` refuses to marshal it, so
// the whole response would 500 on an empty or broken series, which §4.11 says
// must stay listed.
func percent(completed, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return math.Round(float64(completed)/float64(total)*1000) / 10
}

// toBookSummary maps one 권, with its reading progress where it has any.
func toBookSummary(row index.BookRow) BookSummary {
	b := BookSummary{
		ID:         row.ID,
		SeriesID:   row.SeriesID,
		Name:       row.DisplayName,
		Path:       row.RelPath,
		Kind:       row.Kind,
		Ord:        row.Ord,
		PageCount:  row.PageCount,
		TotalBytes: row.TotalBytes,
		FileSize:   row.FileSize,
		Mtime:      row.FileMtime,
		CV:         row.ContentVersion,
		Status:     row.Status,
		Error:      nullableString(row.Error),
	}
	if row.Progress != nil {
		p := toProgressFromIndex(row.ID, row.SeriesID, *row.Progress, row.PageCount)
		b.Progress = &p
	}
	return b
}

// toProgressFromIndex maps the progress joined onto a book listing.
func toProgressFromIndex(bookID, seriesID string, p index.BookProgress, indexPageCount int64) Progress {
	return Progress{
		BookID:    bookID,
		SeriesID:  seriesID,
		LastPage:  p.LastPage,
		PageCount: p.PageCount,
		Completed: p.Completed,
		StartedAt: p.StartedAt,
		UpdatedAt: p.UpdatedAt,
		Stale:     isStale(p.PageCount, indexPageCount),
	}
}

// toProgressFromUser maps a row read straight out of user.db.
func toProgressFromUser(p userdata.Progress, indexPageCount int64) Progress {
	return Progress{
		BookID:    p.BookID,
		SeriesID:  p.SeriesID,
		LastPage:  p.LastPage,
		PageCount: p.PageCount,
		Completed: p.Completed,
		StartedAt: p.StartedAt,
		UpdatedAt: p.UpdatedAt,
		Stale:     isStale(p.PageCount, indexPageCount),
	}
}

// isStale reports that the book changed under the reader: the length recorded
// when progress was written no longer matches the index (arch §7.3).
//
// It is symmetric — 0 on EITHER side is not stale, because 0 does not mean a
// length, it means "length unknown" (a `status != "ok"` book, arch §4.11), and
// a comparison needs two lengths.
//
// A recorded 0 is the reader's side: the book had no known length when the page
// was turned, and calling that "the file changed" would put a warning on the
// screen for a condition the user cannot act on. A current 0 is the same
// sentence about the other end of the comparison — the book is broken *now*, so
// the screen already says the file cannot be opened and there is no page to
// resume at. Adding "your saved place may have moved" to that is the very thing
// the paragraph above forbids, and it would repeat on every entry forever: the
// viewer cannot acknowledge a hint on a book it never finished loading.
//
// This defers the warning rather than swallowing it. The baseline survives
// (userdata.PutProgress refuses to rebaseline an unknown length), so when the
// file is repaired to a length that is not the recorded one, this answers true
// and the reader is told honestly (ruling E-45 §2).
func isStale(recorded int, current int64) bool {
	if recorded == 0 || current == 0 {
		return false
	}
	return int64(recorded) != current
}

// toPageInfo maps one page row. Width and height stay null until a decode has
// filled them in (arch §5.8) — the viewer treats an unknown page as single-page
// in spread mode rather than blocking on dimensions.
func toPageInfo(p index.Page) PageInfo {
	return PageInfo{
		N:    p.PageNo,
		Name: p.Name,
		Ext:  p.Ext,
		Size: p.Size,
		W:    p.Width,
		H:    p.Height,
	}
}
