package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"shelf/internal/bookcover"
	"shelf/internal/index"
	"shelf/internal/thumbs"
	"shelf/internal/userdata"
)

type BookCover struct {
	Page int     `json:"page"`
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	W    float64 `json:"w"`
	H    float64 `json:"h"`
}
type BookCoverResult struct {
	CoverCV string     `json:"cover_cv"`
	Cover   *BookCover `json:"cover"`
}

func (s *Server) coverInputs(r *http.Request, b index.BookRow) (thumbs.Request, BookCoverResult, error) {
	global := s.cfg.Reader.ReadingDirection
	if !s.perUser {
		global = s.defaultPrefs(r).ReadingDirection
	}
	req, stored, err := bookcover.Resolve(r.Context(), s.user, b, global, s.perUser)
	if err != nil {
		return req, BookCoverResult{}, internalErr(err)
	}
	var cover *BookCover
	if stored != nil {
		cover = &BookCover{stored.Page, stored.X, stored.Y, stored.W, stored.H}
	}
	var cv string
	if s.thumbs != nil {
		cv = s.thumbs.CoverVersion(b.ID, req.PageNo, *req.Cover, b.ContentVersion)
	} else {
		cv = thumbs.CoverVersion(b.ID, req.PageNo, *req.Cover, "jpeg", s.cfg.Thumbnails.Quality, b.ContentVersion)
	}
	return req, BookCoverResult{cv, cover}, nil
}

func (s *Server) bookSummary(r *http.Request, b index.BookRow) (BookSummary, error) {
	_, c, err := s.coverInputs(r, b)
	out := toBookSummary(b)
	out.CoverCV, out.Cover = c.CoverCV, c.Cover
	return out, err
}
func (s *Server) seriesSummary(r *http.Request, row index.SeriesRow) (SeriesSummary, error) {
	out := toSeriesSummary(row)
	if row.CoverKind == "page" {
		b, err := s.idx.GetBook(r.Context(), row.CoverBookID)
		if err != nil {
			return out, internalErr(err)
		}
		_, c, err := s.coverInputs(r, b)
		if err != nil {
			return out, err
		}
		out.CoverCV = &c.CoverCV
	}
	return out, nil
}
func (s *Server) coverBook(r *http.Request) (index.BookRow, error) {
	bid, err := pathID(r, "bid")
	if err != nil {
		return index.BookRow{}, err
	}
	if s.idx == nil {
		return index.BookRow{}, unavailable("the catalogue is not available")
	}
	b, err := s.idx.GetBook(r.Context(), bid)
	if errors.Is(err, index.ErrNotFound) {
		return b, notFound("no book with id %s", bid)
	}
	if err != nil {
		return b, internalErr(err)
	}
	return b, nil
}
func (s *Server) handleBookCover(w http.ResponseWriter, r *http.Request) error {
	if s.thumbs == nil {
		return unavailable("the thumbnail cache is not available")
	}
	b, err := s.coverBook(r)
	if err != nil {
		return err
	}
	if _, err = s.idx.GetPage(r.Context(), b.ID, 1); errors.Is(err, index.ErrNotFound) {
		return notFound("book has no pages")
	} else if err != nil {
		return internalErr(err)
	}
	req, c, err := s.coverInputs(r, b)
	if err != nil {
		return err
	}
	width, err := s.thumbWidth(r)
	if err != nil {
		return err
	}
	mode, err := versionMode(r, c.CoverCV)
	if err != nil {
		return err
	}
	req.Width = width
	return s.serveThumb(w, r, req, mode)
}
func (s *Server) handlePutBookCover(w http.ResponseWriter, r *http.Request) error {
	if s.user == nil {
		return unavailable("user data is not available")
	}
	b, err := s.coverBook(r)
	if err != nil {
		return err
	}
	var body struct {
		Page *int     `json:"page"`
		X    *float64 `json:"x"`
		Y    *float64 `json:"y"`
		W    *float64 `json:"w"`
		H    *float64 `json:"h"`
	}
	if err := decodeJSON(w, r, maxJSONBody, &body); err != nil {
		return err
	}
	for _, f := range []struct {
		name    string
		missing bool
	}{{"page", body.Page == nil}, {"x", body.X == nil}, {"y", body.Y == nil}, {"w", body.W == nil}, {"h", body.H == nil}} {
		if f.missing {
			return badRequest("field %q is required", f.name).withDetail("field", f.name)
		}
	}
	if b.Status != "ok" {
		return badRequest("book must have status ok")
	}
	_, err = s.user.PutCover(r.Context(), userdata.CoverPut{BookID: b.ID, SeriesID: b.SeriesID, RootName: b.RootName, BookPath: b.RelPath, PageCount: int(b.PageCount), Region: userdata.CoverRegion{Page: *body.Page, X: *body.X, Y: *body.Y, W: *body.W, H: *body.H}})
	if errors.Is(err, userdata.ErrInvalidArgument) {
		return badRequest("%s", err)
	}
	if err != nil {
		return internalErr(err)
	}
	_, c, err := s.coverInputs(r, b)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, c)
	return nil
}
func (s *Server) handleDeleteBookCover(w http.ResponseWriter, r *http.Request) error {
	if s.user == nil {
		return unavailable("user data is not available")
	}
	b, err := s.coverBook(r)
	if err != nil {
		return err
	}
	if err := s.user.DeleteCover(r.Context(), b.ID); err != nil {
		return internalErr(err)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type SeriesPrefs struct {
	ReadingDirection *string `json:"reading_direction"`
}

func (s *Server) handlePutSeriesPrefs(w http.ResponseWriter, r *http.Request) error {
	if s.idx == nil || s.user == nil {
		return unavailable("user data is not available")
	}
	sid, err := pathID(r, "sid")
	if err != nil {
		return err
	}
	row, err := s.idx.GetSeries(r.Context(), sid)
	if errors.Is(err, index.ErrNotFound) {
		return notFound("no series with id %s", sid)
	}
	if err != nil {
		return internalErr(err)
	}
	var body struct {
		ReadingDirection json.RawMessage `json:"reading_direction"`
	}
	if err := decodeJSON(w, r, maxJSONBody, &body); err != nil {
		return err
	}
	if len(body.ReadingDirection) == 0 {
		return badRequest("reading_direction is required").withDetail("field", "reading_direction")
	}
	patch, err := enumPatch("reading_direction", body.ReadingDirection, readingDirections)
	if err != nil {
		return err
	}
	_, err = s.user.PutSeriesPrefs(r.Context(), userdata.SeriesPrefsPut{SeriesID: sid, RootName: row.RootName, SeriesPath: row.RelPath, ReadingDir: patch.Value})
	if err != nil {
		return internalErr(err)
	}
	writeJSON(w, http.StatusOK, SeriesPrefs{patch.Value})
	return nil
}
