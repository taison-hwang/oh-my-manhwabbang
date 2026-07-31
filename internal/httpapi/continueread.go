package httpapi

import (
	"net/http"
)

// Bounds of arch §7.7.
const (
	continueLimitDefault = 20
	continueLimitMin     = 1
	continueLimitMax     = 50
)

// handleContinue is `GET /api/continue` — the 이어보기 shelf (FR-LIB-010).
//
// An empty `items` array is the signal to hide the whole row ("진행 중인 항목이
// 없으면 영역 자체를 숨김"), which is why it is an empty array and never null:
// `items.length === 0` has to be a safe thing for the client to write.
func (s *Server) handleContinue(w http.ResponseWriter, r *http.Request) error {
	if s.idx == nil {
		return unavailable("the catalogue is not available")
	}
	limit, err := queryInt(r, "limit", continueLimitDefault, continueLimitMin, continueLimitMax)
	if err != nil {
		return err
	}

	rows, err := s.idx.ListContinue(r.Context(), limit)
	if err != nil {
		return internalErr(err)
	}

	items := make([]ContinueItem, 0, len(rows))
	for _, row := range rows {
		book := toBookSummary(row.Book)
		// Every row of this query has progress by construction — it selects
		// started-but-unfinished books — but the wire type carries a non-nullable
		// Progress, so a row that somehow lacks it is dropped rather than
		// marshalled as a zero value the UI would render as "page 0 of 0".
		if book.Progress == nil {
			continue
		}
		items = append(items, ContinueItem{
			Book:       book,
			SeriesID:   row.SeriesID,
			SeriesName: row.SeriesName,
			HasCover:   row.HasCover,
			Progress:   *book.Progress,
		})
	}
	writeJSON(w, http.StatusOK, ContinueResponse{Items: items})
	return nil
}
