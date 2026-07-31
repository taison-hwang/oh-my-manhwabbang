package httpapi

import (
	"errors"
	"net/http"

	"shelf/internal/thumbs"
)

// handleCacheUsage is `GET /api/cache/usage` (FR-THM-008).
//
// The walk behind it is cached for 60 s by internal/thumbs, so the settings
// dialog can poll it without turning a 1.36 M-file cache directory into an I/O
// storm.
func (s *Server) handleCacheUsage(w http.ResponseWriter, r *http.Request) error {
	if s.thumbs == nil {
		return unavailable("the thumbnail cache is not available")
	}
	usage, err := s.thumbs.Usage(r.Context())
	if err != nil {
		return internalErr(err)
	}

	entries := make([]CacheUsageItem, 0, len(usage.Entries))
	for _, e := range usage.Entries {
		entries = append(entries, CacheUsageItem{
			Kind:  string(e.Kind),
			Files: e.Files,
			Bytes: e.Bytes,
		})
	}
	writeJSON(w, http.StatusOK, CacheUsage{
		ComputedAt: usage.ComputedAt.Unix(),
		Entries:    entries,
		TotalBytes: usage.TotalBytes,
		CacheDir:   usage.CacheDir,
	})
	return nil
}

// handleCachePurge is `DELETE /api/cache?kind=thumbs|pdf|wazero|all` (FR-THM-008).
//
// `kind` is a closed enumeration checked before anything is touched, and it is
// never a path. That is the whole of the "a purge cannot walk outside the cache
// directory" guarantee — there is no string here that could be made to name
// `/`, because the only strings accepted are four literals.
func (s *Server) handleCachePurge(w http.ResponseWriter, r *http.Request) error {
	if s.thumbs == nil {
		return unavailable("the thumbnail cache is not available")
	}
	kind, err := queryEnum(r, "kind", string(thumbs.KindAll),
		string(thumbs.KindThumbs), string(thumbs.KindPDF),
		string(thumbs.KindWazero), string(thumbs.KindAll))
	if err != nil {
		return err
	}

	res, err := s.thumbs.Purge(r.Context(), kind)
	if err != nil {
		if errors.Is(err, thumbs.ErrUnknownKind) {
			// Unreachable while queryEnum guards the same set, kept so that a
			// future kind added in one place and not the other fails as a 400
			// rather than a 500.
			return badParam("kind", "unknown cache kind").withDetail("value", kind)
		}
		return internalErr(err)
	}
	writeJSON(w, http.StatusOK, PurgeResult{
		DeletedFiles: res.DeletedFiles,
		FreedBytes:   res.FreedBytes,
	})
	return nil
}
