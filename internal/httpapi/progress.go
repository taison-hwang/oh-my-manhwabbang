package httpapi

import (
	"errors"
	"net/http"

	"shelf/internal/index"
	"shelf/internal/userdata"
)

// handlePutProgress is `PUT /api/books/{bid}/progress` (FR-VWR-009, FR-STT-001).
//
// It is idempotent and safe to send on every page turn — the frontend debounces
// to ~1 s. Two clamping rules matter and both come straight from arch §7.6:
//
//   - `page` is clamped server-side to [1, page_count]. A client that sends
//     page 9000 of a 200-page book gets 200, not a 400: the file may genuinely
//     have shrunk since the client loaded it, and losing the reader's place
//     over a race is worse than saving an approximate one.
//   - `page_count === 0` means "length unknown" — a book whose status is not
//     "ok" (§4.11). Only the lower bound applies, so the clamp is [1, ∞). It is
//     NOT a 400 and NOT an empty range.
func (s *Server) handlePutProgress(w http.ResponseWriter, r *http.Request) error {
	if s.user == nil {
		return unavailable("user data is not available")
	}
	bid, err := pathID(r, "bid")
	if err != nil {
		return err
	}
	book, err := s.book(r, bid)
	if err != nil {
		return err
	}

	var body progressUpdateBody
	if err := decodeJSON(w, r, maxJSONBody, &body); err != nil {
		return err
	}
	if body.Page == nil {
		return badRequest("field %q is required", "page").withDetail("field", "page")
	}
	if *body.Page < 1 {
		// The clamp has a lower bound of 1, but a client asking for page 0 has
		// an off-by-one somewhere and should hear about it rather than be
		// silently corrected (impl-plan §4 #1).
		return badRequest("page numbers are 1-based; there is no page %d", *body.Page).
			withDetail("field", "page", "value", *body.Page)
	}

	stored, err := s.user.PutProgress(r.Context(), userdata.ProgressUpdate{
		BookID:    book.ID,
		SeriesID:  book.SeriesID,
		RootName:  book.RootName,
		BookPath:  book.RelPath,
		Page:      *body.Page,
		PageCount: int(book.PageCount),
		Completed: body.Completed,
	})
	if err != nil {
		if errors.Is(err, userdata.ErrInvalidArgument) {
			return badRequest("%s", err.Error())
		}
		return internalErr(err)
	}
	writeJSON(w, http.StatusOK, toProgressFromUser(stored, book.PageCount))
	return nil
}

// handleDeleteProgress is `DELETE /api/books/{bid}/progress` → 204: the
// "안읽음" / mark-as-unread half of FR-VWR-012.
func (s *Server) handleDeleteProgress(w http.ResponseWriter, r *http.Request) error {
	if s.user == nil {
		return unavailable("user data is not available")
	}
	bid, err := pathID(r, "bid")
	if err != nil {
		return err
	}
	if err := s.requireBook(r, bid); err != nil {
		return err
	}
	if err := s.user.DeleteProgress(r.Context(), bid); err != nil {
		return internalErr(err)
	}
	noContent(w)
	return nil
}

// handleProgressExport is `GET /api/progress/export` (FR-STT-004).
//
// This is the only authored data in the product, and the export is what makes
// it portable — decision D-04 keeps the endpoints in scope even though the UI
// for them is stage 3. `Content-Disposition: attachment` so a browser hitting
// the URL saves a file instead of rendering JSON.
func (s *Server) handleProgressExport(w http.ResponseWriter, r *http.Request) error {
	if s.user == nil {
		return unavailable("user data is not available")
	}
	doc, err := s.user.Export(r.Context())
	if err != nil {
		return internalErr(err)
	}
	w.Header().Set("Content-Disposition", `attachment; filename="shelf-progress.json"`)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, progressExport(doc))
	return nil
}

// handleProgressImport is `POST /api/progress/import` (FR-STT-004).
//
// The document is untrusted: it is a file the user chose. The storage layer
// validates every enum against the frozen §7.3 sets, clamps page numbers
// exactly as a page turn would, refuses an id_version mismatch outright, and
// applies the whole thing in one transaction — a partial import of somebody's
// reading history is worse than a failed one.
func (s *Server) handleProgressImport(w http.ResponseWriter, r *http.Request) error {
	if s.user == nil {
		return unavailable("user data is not available")
	}
	strategyParam, err := queryEnum(r, "strategy", string(userdata.StrategyMerge),
		string(userdata.StrategyMerge), string(userdata.StrategyReplace))
	if err != nil {
		return err
	}

	var doc progressExport
	if err := decodeJSON(w, r, maxImportBody, &doc); err != nil {
		return err
	}

	res, err := s.user.Import(r.Context(), doc, userdata.ImportStrategy(strategyParam))
	if err != nil {
		switch {
		case errors.Is(err, userdata.ErrIDVersionMismatch):
			// The ids in the document were derived under a different scheme,
			// so they point at different books. Merging them would attach one
			// book's history to another.
			return badRequest("the document uses a different identifier scheme").
				withDetail("id_version", doc.IDVersion)
		case errors.Is(err, userdata.ErrInvalidArgument):
			return badRequest("%s", err.Error())
		default:
			return internalErr(err)
		}
	}
	writeJSON(w, http.StatusOK, res)
	return nil
}

// book resolves a book id to its row, with the contract's 404.
func (s *Server) book(r *http.Request, bid string) (index.BookRow, error) {
	if s.idx == nil {
		return index.BookRow{}, unavailable("the catalogue is not available")
	}
	row, err := s.idx.GetBook(r.Context(), bid)
	if err != nil {
		if errors.Is(err, index.ErrNotFound) {
			return index.BookRow{}, notFound("no book with id %s", bid)
		}
		return index.BookRow{}, internalErr(err)
	}
	return row, nil
}
