package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"shelf/internal/archive"
	"shelf/internal/index"
	"shelf/internal/userdata"
)

// The frozen enums of arch §7.3, spelled once. Conflict resolutions C-1 and
// C-2 are the reason two of them read the way they do: the wire value is
// `spread` (not `double`) and `contain` (not `screen`). The Korean labels
// 양면 and 화면 live in the UI catalogue and are not types.
var (
	readingDirections = []string{"ltr", "rtl"}
	displayModes      = []string{"single", "spread", "vertical"}
	fitModes          = []string{"width", "height", "original", "contain"}
)

// handleBookDetail is `GET /api/books/{bid}` (arch §7.6).
//
// Every PageInfo ships in this one response (D-15): 1 071 pages is ~110 KB of
// JSON, and paying it once is what makes an arbitrary page jump need no round
// trip at all (AC-008).
//
// A book whose status is not "ok" still answers **200**, with `pages: []` and a
// populated `error`. It is not an HTTP failure — the series screen has to
// render a 손상 badge and the reason, and it cannot do that from a 500
// (impl-plan §4 #4, FR-IDX-010).
func (s *Server) handleBookDetail(w http.ResponseWriter, r *http.Request) error {
	if s.idx == nil {
		return unavailable("the catalogue is not available")
	}
	bid, err := pathID(r, "bid")
	if err != nil {
		return err
	}
	book, err := s.idx.GetBook(r.Context(), bid)
	if err != nil {
		if errors.Is(err, index.ErrNotFound) {
			return notFound("no book with id %s", bid)
		}
		return internalErr(err)
	}

	// Listed whenever the index holds them, which is no longer the same as
	// `status == "ok"` (E-54): a damaged container can still have a readable
	// entry list, and the viewer needs the rows to render what survived. An
	// encrypted book is the exception the page handler also makes — its pages
	// are never listed and never streamed.
	pages := []PageInfo{}
	if book.Status != string(archive.StatusEncrypted) {
		rows, err := s.idx.ListPages(r.Context(), bid)
		if err != nil {
			return internalErr(err)
		}
		pages = make([]PageInfo, 0, len(rows))
		for _, p := range rows {
			pages = append(pages, toPageInfo(p))
		}
	}

	prev, next, err := s.idx.Neighbours(r.Context(), bid)
	if err != nil {
		return internalErr(err)
	}

	prefs, err := s.effectivePrefs(r, bid)
	if err != nil {
		return err
	}

	series, err := s.idx.GetSeries(r.Context(), book.SeriesID)
	if err != nil && !errors.Is(err, index.ErrNotFound) {
		return internalErr(err)
	}

	// Opening a book whose page dimensions are not fully known schedules the
	// background pass of arch §5.8. The call returns immediately: spread mode
	// treats an unknown page as single-page until the numbers arrive, so nothing
	// blocks and nothing shifts (FR-VWR-004).
	//
	// The guard is `!= "done"`, not `== "none"`. Cover generation (FR-THM-003)
	// measures page 1 for free, and index.refreshDimsState then derives
	// `partial` from "some widths are NULL" — so the cover book of every series
	// lands in `partial` with exactly one measured page and a `== "none"` guard
	// would never enqueue it again. That left 양면 mode permanently unpaired on
	// the most-opened book of each series. `Service.measureBook` probes only the
	// pages whose width is still NULL, so re-enqueueing a `partial` book costs
	// nothing it has already done, and a `done` book is still never queued.
	if s.thumbs != nil && book.DimsState != "done" && book.Status == "ok" {
		s.thumbs.EnsureDims(bid)
	}

	writeJSON(w, http.StatusOK, BookDetail{
		BookSummary: toBookSummary(book),
		SeriesName:  series.DisplayName,
		RootName:    book.RootName,
		Pages:       pages,
		DimsState:   book.DimsState,
		PrevBookID:  nullableString(prev),
		NextBookID:  nullableString(next),
		Prefs:       prefs,
	})
	return nil
}

// handleGetPrefs is `GET /api/books/{bid}/prefs`.
func (s *Server) handleGetPrefs(w http.ResponseWriter, r *http.Request) error {
	bid, err := pathID(r, "bid")
	if err != nil {
		return err
	}
	if err := s.requireBook(r, bid); err != nil {
		return err
	}
	prefs, err := s.effectivePrefs(r, bid)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, prefs)
	return nil
}

// handlePutPrefs is `PUT /api/books/{bid}/prefs` (FR-VWR-002).
//
// Reading direction and friends are remembered **per book** — prd FR-VWR-002
// says 권 단위 (conflict resolution C-9) — falling back to the global defaults
// in `/api/settings`. Every field is optional and three-state: absent leaves
// the override alone, `null` clears it, a value sets it.
func (s *Server) handlePutPrefs(w http.ResponseWriter, r *http.Request) error {
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

	var body bookPrefsUpdateBody
	if err := decodeJSON(w, r, maxJSONBody, &body); err != nil {
		return err
	}

	patch := userdata.PrefsPatch{}
	if patch.ReadingDir, err = enumPatch("reading_direction", body.ReadingDirection, readingDirections); err != nil {
		return err
	}
	if patch.DisplayMode, err = enumPatch("display_mode", body.DisplayMode, displayModes); err != nil {
		return err
	}
	if patch.FitMode, err = enumPatch("fit_mode", body.FitMode, fitModes); err != nil {
		return err
	}

	stored, err := s.user.PutPrefs(r.Context(), bid, patch)
	if err != nil {
		if errors.Is(err, userdata.ErrInvalidArgument) {
			return badRequest("%s", err.Error())
		}
		return internalErr(err)
	}
	writeJSON(w, http.StatusOK, s.mergePrefsWithDefaults(stored, s.defaultPrefs(r)))
	return nil
}

// enumPatch turns one raw JSON field into the storage layer's three-state
// patch, validating the value against the frozen enum.
//
// The three states are the whole reason the body is decoded into
// json.RawMessage: with a *string, `{"fit_mode": null}` and `{}` are the same
// value, and there would be no way to say "go back to the global default".
func enumPatch(field string, raw json.RawMessage, allowed []string) (userdata.Patch[string], error) {
	if len(raw) == 0 {
		return userdata.Patch[string]{}, nil
	}
	if string(raw) == "null" {
		return userdata.ClearPatch[string](), nil
	}
	var v string
	if err := json.Unmarshal(raw, &v); err != nil {
		return userdata.Patch[string]{}, badRequest("field %q must be a string or null", field).
			withDetail("field", field)
	}
	if !contains(allowed, v) {
		return userdata.Patch[string]{}, badRequest("%s must be one of %v", field, allowed).
			withDetail("field", field, "value", v)
	}
	return userdata.SetPatch(v), nil
}

func contains(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}

// requireBook turns an unknown book id into the 404 the contract promises,
// before any user.db write happens. Progress for a book that does not exist is
// not obviously wrong — an unplugged drive must not erase history — but
// *creating* it from an HTTP request would be, so the write paths check first.
func (s *Server) requireBook(r *http.Request, bid string) error {
	if s.idx == nil {
		return unavailable("the catalogue is not available")
	}
	if _, err := s.idx.GetBook(r.Context(), bid); err != nil {
		if errors.Is(err, index.ErrNotFound) {
			return notFound("no book with id %s", bid)
		}
		return internalErr(err)
	}
	return nil
}

// effectivePrefs resolves a book's stored overrides against the global
// defaults.
func (s *Server) effectivePrefs(r *http.Request, bid string) (BookPrefs, error) {
	if s.user == nil {
		return s.defaultPrefs(r), nil
	}
	stored, err := s.user.GetPrefs(r.Context(), bid)
	if err != nil {
		if errors.Is(err, userdata.ErrNotFound) {
			return s.defaultPrefs(r), nil
		}
		return BookPrefs{}, internalErr(err)
	}
	return s.mergePrefsWithDefaults(stored, s.defaultPrefs(r)), nil
}

// mergePrefsWithDefaults fills each nil override from the default.
//
// `is_override` is false only when *nothing* is overridden — the contract's
// "false => these are the global defaults" is a statement about the whole
// object, so a book that overrides only its reading direction still reports
// true and the UI can show that it differs.
func (s *Server) mergePrefsWithDefaults(stored userdata.Prefs, def BookPrefs) BookPrefs {
	out := def
	overridden := false
	if stored.ReadingDir != nil {
		out.ReadingDirection = *stored.ReadingDir
		overridden = true
	}
	if stored.DisplayMode != nil {
		out.DisplayMode = *stored.DisplayMode
		overridden = true
	}
	if stored.FitMode != nil {
		out.FitMode = *stored.FitMode
		overridden = true
	}
	out.IsOverride = overridden
	return out
}

// defaultPrefs is the global reading defaults: the user's `/api/settings`
// values where they exist, the `reader:` block of the YAML otherwise.
func (s *Server) defaultPrefs(r *http.Request) BookPrefs {
	if s.user == nil {
		return s.defaultPrefsFromSettings(nil)
	}
	stored, err := s.user.Settings().All(r.Context())
	if err != nil {
		// Reading the settings store must never fail opening a book: the YAML
		// defaults are a complete, valid answer.
		s.log.WarnContext(r.Context(), "reading settings for book prefs", "err", err)
		return s.defaultPrefsFromSettings(nil)
	}
	return s.defaultPrefsFromSettings(stored)
}

// defaultPrefsFromSettings applies the user's stored settings over the YAML.
func (s *Server) defaultPrefsFromSettings(stored map[string]string) BookPrefs {
	p := BookPrefs{
		ReadingDirection: s.cfg.Reader.ReadingDirection,
		DisplayMode:      s.cfg.Reader.DisplayMode,
		FitMode:          s.cfg.Reader.FitMode,
		IsOverride:       false,
	}
	if v, ok := stored[settingReadingDirection]; ok && contains(readingDirections, v) {
		p.ReadingDirection = v
	}
	if v, ok := stored[settingDisplayMode]; ok && contains(displayModes, v) {
		p.DisplayMode = v
	}
	if v, ok := stored[settingFitMode]; ok && contains(fitModes, v) {
		p.FitMode = v
	}
	return p
}
