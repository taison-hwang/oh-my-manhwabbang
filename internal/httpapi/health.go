package httpapi

import (
	"net/http"
	"runtime"

	"shelf/internal/buildinfo"
	"shelf/internal/pdfium"
	"shelf/internal/thumbs"
)

// handleHealth is `GET /api/health` (arch §7.4).
//
// It never requires authentication: a monitor, a container health check or a
// reverse proxy must be able to ask "are you up?" without a password, and the
// answer discloses nothing about the library. `?verbose=1` adds the pool
// counters that stand in for a metrics endpoint in v1 (arch §9).
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) error {
	now := s.now()
	h := Health{
		OK:          true,
		Version:     buildinfo.Version,
		Commit:      buildinfo.Commit,
		StartedAt:   s.started.Unix(),
		UptimeMs:    now.Sub(s.started).Milliseconds(),
		PDFEnabled:  s.pdfEnabled(),
		AVIFEnabled: s.avifEnabled(),
	}
	if r.URL.Query().Get("verbose") == "1" {
		h.Verbose = s.healthVerbose()
	}
	writeJSON(w, http.StatusOK, h)
	return nil
}

// pdfEnabled is both halves of the FR-SRV-006 gate: the configuration key and
// whether this build carries pdfium at all (`-tags nopdf`). A `pdf:` block
// enabling something the binary cannot do would be a lie the viewer acts on.
func (s *Server) pdfEnabled() bool {
	return s.cfg.PDF.Enabled && pdfium.Supported()
}

// avifEnabled is the same two halves for FR-IDX-011: `thumbnails.avif_enabled`
// and whether this build carries the decoder (`-tags noavif`).
//
// Ruling E-21 made `noavif` the DEFAULT — gen2brain/avif drags in
// ebitengine/purego, which links the binary against libc whatever CGO_ENABLED
// says — while `thumbnails.avif_enabled` still defaults to true. Reporting the
// config key alone would therefore advertise a decoder the shipped binary does
// not contain, and internal/thumbs answers every .avif with
// `422 thumb_unavailable, reason: avif_disabled`. Exactly the lie pdfEnabled
// above exists to prevent.
func (s *Server) avifEnabled() bool {
	return s.cfg.Thumbnails.AVIFEnabled && thumbs.AVIFSupported()
}

// healthVerbose snapshots the counters of arch §5.2 and §9.
func (s *Server) healthVerbose() *HealthVerbose {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	v := &HealthVerbose{
		Goroutines:  runtime.NumGoroutine(),
		HeapAllocKB: ms.HeapAlloc / 1024,
		SysKB:       ms.Sys / 1024,
	}
	if s.pool != nil {
		p := s.pool.Stats()
		v.ArchivePool = &PoolCounter{
			Hits:      p.Hits,
			Misses:    p.Misses,
			Evictions: p.Evictions,
			Stale:     p.Stale,
			Size:      p.Size,
			Open:      p.Open,
		}
	}
	if s.thumbs != nil {
		t := s.thumbs.Stats()
		v.ThumbCounter = ThumbCounter{
			Hits:       t.Hits,
			Queued:     t.Queued,
			Dropped:    t.Dropped,
			Generated:  t.Generated,
			Failed:     t.Failed,
			CoverDepth: t.CoverDepth,
			PageDepth:  t.PageDepth,
			Active:     t.Active,
			Inflight:   t.Inflight,
		}
	}
	return v
}
