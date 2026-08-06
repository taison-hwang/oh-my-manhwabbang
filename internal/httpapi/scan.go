package httpapi

import (
	"errors"
	"net/http"

	"shelf/internal/index"
	"shelf/internal/scanner"
)

// Bounds of arch §7.10's log query.
const (
	scanLogLimitDefault = 200
	scanLogLimitMin     = 1
	scanLogLimitMax     = 1000
)

// handleStartScan is `POST /api/scan` (FR-IDX-001).
//
// The body is optional: `{}`, `{"full": true}` or nothing at all. `202` with a
// run id, or `409 conflict` when a scan is already running — one scan at a time
// is a property of the scanner (one writer goroutine, arch §4.1), not a policy
// this layer enforces.
func (s *Server) handleStartScan(w http.ResponseWriter, r *http.Request) error {
	if s.scan == nil {
		return unavailable("the scanner is not available")
	}
	var body scanRequestBody
	if err := decodeJSONOptional(w, r, maxJSONBody, &body); err != nil {
		return err
	}
	roots, err := s.scanRoots(body.Roots)
	if err != nil {
		return err
	}

	runID, err := s.scan.Start(r.Context(), scanner.Request{
		Roots: roots,
		Full:  body.Full,
	})
	if err != nil {
		return scanStartError(err)
	}
	writeJSON(w, http.StatusAccepted, RunAccepted{RunID: runID})
	return nil
}

// scanRoots applies amendment A-11's revision R1 to a scan request: a root this
// process has removed is not scanned again before the restart.
//
// Naming one explicitly is `404 not_found`, not the `400` an unknown root gets.
// The distinction is the caller's: a name that was never in the configuration is
// a client that built a request out of nothing, while this name *was* right and
// the resource has since gone — the same split arch §7.1 draws between a
// malformed id and an unknown one.
//
// A full scan (no `roots` in the body) becomes an explicit list of the enabled
// configured roots minus the removed ones, which is exactly what the scanner
// derives itself from an empty list. When nothing has been removed the request
// is passed through untouched, so the ordinary path cannot regress.
func (s *Server) scanRoots(requested []string) ([]string, error) {
	removed := s.removedRootNames()
	if len(removed) == 0 {
		return requested, nil
	}
	if len(requested) > 0 {
		for _, name := range requested {
			if _, gone := removed[name]; gone {
				return nil, notFound("root %q was removed from the configuration; restart to apply it", name)
			}
		}
		return requested, nil
	}
	configured := s.configuredRoots()
	out := make([]string, 0, len(configured))
	for _, root := range configured {
		if _, gone := removed[root.Name]; gone || !root.Enabled {
			continue
		}
		out = append(out, root.Name)
	}
	if len(out) == 0 {
		// An empty list means "every enabled root" to the scanner, which is the
		// opposite of what this caller asked for.
		return nil, conflict("every configured root has been removed; restart to apply the changes")
	}
	return out, nil
}

// scanStartError maps the scanner's refusals onto the contract.
func scanStartError(err error) error {
	switch {
	case errors.Is(err, scanner.ErrBusy):
		return conflict("a scan is already running")
	case errors.Is(err, scanner.ErrUnknownRoot):
		// The client named a root that is not in the configuration. That is a
		// bad parameter, not a missing resource: the request cannot be made to
		// work by retrying it.
		return badParam("roots", "%s", err.Error())
	case errors.Is(err, scanner.ErrClosed):
		return unavailable("the server is shutting down")
	default:
		return internalErr(err)
	}
}

// handleScanStatus is `GET /api/scan/status` (FR-IDX-004).
//
// This is the normative progress mechanism: the frontend polls it at 1 s while
// `state !== "idle"` and stops when idle (C-11). SSE is explicitly out of scope
// — a full cold scan is 32 s, so a whole run costs ~32 requests against a
// lock-free atomic snapshot, while a stream would permanently hold one of the
// browser's six connections that the viewer needs for prefetch.
func (s *Server) handleScanStatus(w http.ResponseWriter, r *http.Request) error {
	if s.scan == nil {
		return unavailable("the scanner is not available")
	}
	writeJSON(w, http.StatusOK, toScanStatus(s.scan.Status()))
	return nil
}

// handleCancelScan is `POST /api/scan/cancel`.
//
// It is idempotent and answers 204 whether or not a scan was running: "there is
// no scan now" is the state the caller asked for either way. The writer commits
// what it has and no generation sweep runs, so a cancelled scan can never
// delete a row (arch §4.1).
func (s *Server) handleCancelScan(w http.ResponseWriter, r *http.Request) error {
	if s.scan == nil {
		return unavailable("the scanner is not available")
	}
	s.scan.Cancel()
	noContent(w)
	return nil
}

// handleScanLog is `GET /api/scan/log` — the UI-004 "스캔 로그 열람" panel.
//
// It exists so an operator never needs shell access to find out why a series is
// broken (FR-IDX-010 surfacing, arch §9).
func (s *Server) handleScanLog(w http.ResponseWriter, r *http.Request) error {
	if s.idx == nil {
		return unavailable("the catalogue is not available")
	}
	limit, err := queryInt(r, "limit", scanLogLimitDefault, scanLogLimitMin, scanLogLimitMax)
	if err != nil {
		return err
	}
	level, err := queryEnum(r, "level", "", index.LevelInfo, index.LevelWarn, index.LevelError)
	if err != nil {
		return err
	}
	sinceID, err := queryInt(r, "since_id", 0, 0, maxOffset)
	if err != nil {
		return err
	}

	rows, err := s.idx.ListLog(r.Context(), index.LogFilter{
		Limit:   limit,
		Level:   level,
		RunID:   r.URL.Query().Get("run_id"),
		SinceID: int64(sinceID),
	})
	if err != nil {
		return internalErr(err)
	}

	items := make([]ScanLogEntry, 0, len(rows))
	for _, row := range rows {
		items = append(items, ScanLogEntry{
			ID:       row.ID,
			TS:       row.TS,
			RunID:    row.RunID,
			Level:    row.Level,
			RootName: nullableString(row.Root),
			RelPath:  nullableString(row.RelPath),
			Message:  row.Message,
		})
	}
	writeJSON(w, http.StatusOK, ScanLogResponse{Items: items})
	return nil
}

// toScanStatus maps the scanner's snapshot onto the wire type.
//
// Two shape differences are deliberate. `PerRoot` is dropped: arch §7.10's
// ScanStatus carries `roots: string[]` and nothing more, and the per-root
// breakdown is not part of the frozen contract. `Skipped` is dropped for the
// same reason — a rescan that skips everything is visible as `done == total`.
func toScanStatus(st *scanner.ScanStatus) ScanStatus {
	if st == nil {
		// Before the first scan there is no snapshot at all. "idle with nothing
		// in it" is the honest answer and is exactly what the frontend's poll
		// policy expects: it stops polling on idle.
		return ScanStatus{State: string(scanner.PhaseIdle), Roots: []string{}}
	}
	roots := st.Roots
	if roots == nil {
		roots = []string{}
	}
	return ScanStatus{
		State:       string(st.State),
		RunID:       nullableString(st.RunID),
		Full:        st.Full,
		StartedAt:   st.StartedAt,
		FinishedAt:  st.FinishedAt,
		Roots:       roots,
		CurrentRoot: nullableString(st.CurrentRoot),
		CurrentItem: nullableString(st.CurrentItem),
		Total:       st.Total,
		Done:        st.Done,
		Errors:      st.Errors,
		CoversTotal: st.CoversTotal,
		CoversDone:  st.CoversDone,
		ElapsedMs:   st.ElapsedMs,
		ETAMs:       st.ETAMs,
		LastError:   nullableString(st.LastError),
	}
}
