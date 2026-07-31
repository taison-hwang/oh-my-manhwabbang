package scanner

import (
	"context"
	"path"
	"strings"
)

// FR-THM-003 / FR-LIB-008 — the cover ladder of arch §4.10, and the enqueue that
// makes covers appear *during* a scan rather than after it.

// cover_kind values (arch §3.5).
const (
	CoverFile = "file"
	CoverPage = "page"
)

// coverStems are the exact base names (case-insensitive, extension removed,
// optional surrounding brackets) that mark a file as the series cover.
var coverStems = [...]string{"cover", "folder", "poster", "thumb", "thumbnail"}

// isCoverFileName implements arch §4.10 rule 1's predicate:
// `^(\[)?(cover|folder|poster|thumb|thumbnail)(\])?$` **or** a name containing
// "cover". It catches both real shapes in the collection — `[cover].jpg` and
// `강철의 연금술사 00 Cover.jpg`.
func isCoverFileName(base string) bool {
	stem := strings.ToLower(strings.TrimSuffix(base, path.Ext(base)))
	bare := stem
	if strings.HasPrefix(bare, "[") && strings.HasSuffix(bare, "]") && len(bare) > 1 {
		bare = bare[1 : len(bare)-1]
	}
	for _, want := range coverStems {
		if bare == want {
			return true
		}
	}
	return strings.Contains(stem, "cover")
}

// coverChoice is the outcome of the ladder, in series-row terms.
type coverChoice struct {
	Kind    string // CoverFile, CoverPage or "" for "no cover"
	RelPath string // root-relative, when Kind == CoverFile
	BookID  string // when Kind == CoverPage
	PageNo  int    // 1-based, when Kind == CoverPage
	// ContentVersion of the cover book, so the enqueue can build a cache key
	// that FR-THM-006 invalidates structurally.
	ContentVersion string
}

// chooseCover walks arch §4.10 in order:
//
//  1. a loose image in the series directory whose name says "cover";
//  2. otherwise the single cover candidate collectBooks set aside (D-5's
//     "N archives + exactly one image" shape, 47 real directories);
//  3. otherwise page 1 of the first book by `ord` whose status is 'ok';
//  4. otherwise nothing — `GET /api/series/{sid}/cover` answers 404 and the
//     frontend draws FR-LIB-008's name-text placeholder. The API never
//     fabricates an image.
//
// The book list must already be in `ord` order; the caller sorts once.
func chooseCover(u *seriesUnit, results []bookResult) coverChoice {
	for _, rel := range u.looseImages {
		if isCoverFileName(path.Base(rel)) {
			return coverChoice{Kind: CoverFile, RelPath: rel}
		}
	}
	if len(u.coverCandidates) == 1 {
		return coverChoice{Kind: CoverFile, RelPath: u.coverCandidates[0]}
	}
	for _, r := range results {
		if r.status == StatusOK && r.pageCount > 0 {
			return coverChoice{
				Kind:           CoverPage,
				BookID:         r.id,
				PageNo:         1,
				ContentVersion: r.contentVersion,
			}
		}
	}
	return coverChoice{}
}

// CoverRequest is what the scanner hands the thumbnail queue as each series
// completes (FR-THM-003: "the series cover — the first page of the first volume
// — is generated immediately after the scan").
type CoverRequest struct {
	SeriesID string
	RootName string
	// SeriesRelPath is the series directory or file, root-relative.
	SeriesRelPath string
	// Kind is CoverFile or CoverPage.
	Kind string
	// RelPath is the image file, root-relative, when Kind == CoverFile.
	RelPath string
	// BookID and PageNo identify the page when Kind == CoverPage.
	BookID string
	PageNo int
	// ContentVersion is the cover book's books.content_version, an input to the
	// thumbnail cache key (arch §5.6). It is empty for CoverFile, whose bytes
	// come off the filesystem rather than out of a book.
	ContentVersion string
}

// CoverQueue is the narrow view of internal/thumbs that the scanner needs.
//
// It is declared here, by the consumer, exactly as impl-plan §5.1 requires:
// internal/thumbs returns a concrete type and this package never imports it, so
// the two wave-2 packages compile and test independently and WP-13 does the one
// line of wiring.
//
// EnqueueCover must not block: it is called from the writer goroutine, which
// owns the index write connection, and a queue that blocks there stalls the
// whole scan. arch §5.4's coverQ is unbounded precisely so this can be true.
//
// It is called from index.Writer.AfterCommit, i.e. once the series' book and
// page rows are readable on another connection. A `page` cover is resolved by
// looking those rows up, so an enqueue made any earlier resolves to "no such
// book" and the cover is silently lost (FR-THM-003).
type CoverQueue interface {
	EnqueueCover(ctx context.Context, req CoverRequest)
}

// CoverProgressReporter is an optional extension a CoverQueue may implement so
// the `covers` phase of arch §4.12 can report real numbers instead of guessing.
// A queue that does not implement it simply makes the phase a no-op — the scan
// still enqueues every cover, it just stops claiming to know when they landed.
type CoverProgressReporter interface {
	// CoverProgress reports covers finished and covers accepted, cumulative for
	// the process.
	CoverProgress() (done, total int64)
}

// enqueueCover posts one series cover, if it has one.
func (s *Scanner) enqueueCover(ctx context.Context, u *seriesUnit, seriesID string, c coverChoice) {
	if s.covers == nil || c.Kind == "" {
		return
	}
	s.covers.EnqueueCover(ctx, CoverRequest{
		SeriesID:       seriesID,
		RootName:       u.rootName,
		SeriesRelPath:  u.relPath,
		Kind:           c.Kind,
		RelPath:        c.RelPath,
		BookID:         c.BookID,
		PageNo:         c.PageNo,
		ContentVersion: c.ContentVersion,
	})
	s.progress.coversQueued(1)
}
