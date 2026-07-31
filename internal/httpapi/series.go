package httpapi

import (
	"errors"
	"math"
	"net/http"

	"shelf/internal/index"
	"shelf/internal/scanner"
	"shelf/internal/thumbs"
)

// Defaults and bounds of arch §7.5. `limit=0` is deliberately not legal: the
// count idiom is `limit=1` (amendment A-8), and one wasted row is cheaper than
// a special case in the pagination contract.
const (
	seriesLimitDefault = 60
	seriesLimitMin     = 1
	seriesLimitMax     = 200
)

// handleSeriesList is `GET /api/series` — filter, sort, paginate, search
// (FR-LIB-003…007, amendments A-4 and A-8).
func (s *Server) handleSeriesList(w http.ResponseWriter, r *http.Request) error {
	if s.idx == nil {
		return unavailable("the catalogue is not available")
	}
	f, err := s.seriesFilter(r)
	if err != nil {
		return err
	}
	list, err := s.idx.ListSeries(r.Context(), f)
	if err != nil {
		// The storage layer validates the same enums; a rejection here means
		// this package let a value through that it should have caught, so the
		// param is still the client's problem, not a 500.
		if errors.Is(err, index.ErrInvalidFilter) {
			return badRequest("%s", err.Error())
		}
		return internalErr(err)
	}

	items := make([]SeriesSummary, 0, len(list.Items))
	for _, row := range list.Items {
		items = append(items, toSeriesSummary(row))
	}
	writeJSON(w, http.StatusOK, SeriesListResponse{
		Items:  items,
		Total:  list.Total,
		Offset: list.Offset,
		Limit:  list.Limit,
	})
	return nil
}

// seriesFilter parses and validates the query string of arch §7.5.
//
// Every closed-set parameter is checked here rather than being handed to the
// storage layer, because the contract requires the *shape* of the rejection —
// `400` with `detail: {param: "<name>"}` — and only this layer knows about
// HTTP. An unknown parameter is ignored (§7.1); an unknown *value* is not.
func (s *Server) seriesFilter(r *http.Request) (index.SeriesFilter, error) {
	q := r.URL.Query()
	var f index.SeriesFilter

	// `root` is repeatable (FR-LIB-005): ?root=a&root=b is the union.
	f.Roots = q["root"]
	f.Query = q.Get("q")

	var err error
	if f.Status, err = queryEnum(r, "status", "all", "ok", "empty", "error", "all"); err != nil {
		return f, err
	}
	if f.Progress, err = queryEnum(r, "progress", index.ProgressAny,
		index.ProgressAny, index.ProgressReading, index.ProgressDone, index.ProgressUnread); err != nil {
		return f, err
	}
	// A-8: `scope` accepts exactly `all` and `added`. `reading`, `done` and a
	// root name are deliberately *not* accepted — those are `progress=` and
	// `root=`. One meaning, one spelling.
	if f.Scope, err = queryEnum(r, "scope", index.ScopeAll, index.ScopeAll, index.ScopeAdded); err != nil {
		return f, err
	}
	if f.Sort, err = queryEnum(r, "sort", index.SortName,
		index.SortName, index.SortMtime, index.SortRecent,
		index.SortSize, index.SortBooks, index.SortAdded); err != nil {
		return f, err
	}
	// The default direction is per-sort — asc for name, desc for everything
	// else — and the storage layer owns that rule, so "" is passed through
	// rather than resolved here.
	if f.Order, err = queryEnum(r, "order", "", "asc", "desc"); err != nil {
		return f, err
	}
	if f.Offset, err = queryInt(r, "offset", 0, 0, maxOffset); err != nil {
		return f, err
	}
	if f.Limit, err = queryInt(r, "limit", seriesLimitDefault, seriesLimitMin, seriesLimitMax); err != nil {
		return f, err
	}

	if f.Scope == index.ScopeAdded {
		// Evaluated per request and never cached: with the 14-day default a
		// series leaves 최근 추가 on the 15th day with no scan, no restart and
		// no cache purge (arch §7.5).
		f.RecentlyAddedCutoff = s.cfg.Library.RecentlyAddedCutoff(s.now())
	}
	return f, nil
}

// maxOffset bounds `offset`. The contract says only "int ≥ 0"; capping at
// 2³¹−1 keeps the value inside every SQLite integer path and makes an absurd
// offset a 400 rather than a silent overflow.
const maxOffset = math.MaxInt32

// handleSeriesDetail is `GET /api/series/{sid}` (arch §7.5).
func (s *Server) handleSeriesDetail(w http.ResponseWriter, r *http.Request) error {
	if s.idx == nil {
		return unavailable("the catalogue is not available")
	}
	sid, err := pathID(r, "sid")
	if err != nil {
		return err
	}
	detail, err := s.idx.GetSeries(r.Context(), sid)
	if err != nil {
		if errors.Is(err, index.ErrNotFound) {
			return notFound("no series with id %s", sid)
		}
		return internalErr(err)
	}

	books := make([]BookSummary, 0, len(detail.Books))
	for _, b := range detail.Books {
		books = append(books, toBookSummary(b))
	}
	writeJSON(w, http.StatusOK, SeriesDetail{
		SeriesSummary: toSeriesSummary(detail.SeriesRow),
		Books:         books,
		// `encoding` is diagnostics and is typed `string | null`. index.db has
		// no column for it: arch §4.4 says the chosen encoding "is recorded per
		// book", but the landed §3.5 schema (WP-03, frozen) carries no such
		// column and source.Listing.NameEncoding is discarded at scan time.
		// Reporting null — the contract's own "unknown" — is the honest
		// answer; inventing a guess from decoded names would be worse than
		// saying nothing. Recorded as proposed amendment A-9.
		Encoding: nil,
	})
	return nil
}

// handleSeriesCover is `GET /api/series/{sid}/cover` (arch §7.5, FR-THM-003).
//
// The three outcomes that matter are all normal: `200` with the JPEG, `202`
// with `Retry-After: 1` while it is queued — the frontend shows a skeleton and
// retries — and `404` when the series has no cover at all, which is the
// FR-LIB-008 text-placeholder case.
func (s *Server) handleSeriesCover(w http.ResponseWriter, r *http.Request) error {
	if s.idx == nil || s.thumbs == nil {
		return unavailable("the thumbnail cache is not available")
	}
	sid, err := pathID(r, "sid")
	if err != nil {
		return err
	}
	detail, err := s.idx.GetSeries(r.Context(), sid)
	if err != nil {
		if errors.Is(err, index.ErrNotFound) {
			return notFound("no series with id %s", sid)
		}
		return internalErr(err)
	}
	if detail.CoverKind == "" {
		return notFound("series %s has no cover", sid)
	}

	width, err := s.thumbWidth(r)
	if err != nil {
		return err
	}
	// §5.3's ?v= rules apply here unchanged (arch §7.5), against the series'
	// cover_cv rather than a book's cv.
	cacheMode, err := versionMode(r, detail.CoverCV)
	if err != nil {
		return err
	}

	req := thumbs.Request{
		ID:             sid,
		Width:          width,
		Priority:       thumbs.PriorityCover,
		ContentVersion: detail.CoverCV,
	}
	switch detail.CoverKind {
	case "file":
		// A loose image sitting in the series directory. RelPath is
		// root-relative and is validated by internal/thumbs before it is
		// joined through the root's os.Root (traversal layers 2 and 3).
		req.RootName = detail.RootName
		req.RelPath = detail.CoverRelPath
	default:
		// cover_kind='page': the cover is page N of a book, so the cache key is
		// that book's, not the series'.
		req.ID = detail.CoverBookID
		req.PageNo = detail.CoverPageNo
	}

	return s.serveThumb(w, r, req, cacheMode)
}

// handleSeriesRescan is `POST /api/series/{sid}/rescan` — UI-002's
// "이 시리즈 재스캔".
//
// A targeted run never sweeps: absence of a row is not evidence of absence on
// disk when only part of the tree was visited (scanner.Request.Series).
func (s *Server) handleSeriesRescan(w http.ResponseWriter, r *http.Request) error {
	if s.idx == nil || s.scan == nil {
		return unavailable("the scanner is not available")
	}
	sid, err := pathID(r, "sid")
	if err != nil {
		return err
	}
	detail, err := s.idx.GetSeries(r.Context(), sid)
	if err != nil {
		if errors.Is(err, index.ErrNotFound) {
			return notFound("no series with id %s", sid)
		}
		return internalErr(err)
	}

	runID, err := s.scan.Start(r.Context(), scanner.Request{
		Roots:  []string{detail.RootName},
		Series: []scanner.SeriesRef{{Root: detail.RootName, RelPath: detail.RelPath}},
	})
	if err != nil {
		return scanStartError(err)
	}
	writeJSON(w, http.StatusAccepted, RunAccepted{RunID: runID})
	return nil
}
