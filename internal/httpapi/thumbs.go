package httpapi

import (
	"errors"
	"net/http"
	"os"
	"strconv"

	"shelf/internal/index"
	"shelf/internal/thumbs"
)

// cacheMode selects the Cache-Control of an image response (arch §5.3).
type cacheMode int

const (
	// cacheImmutable is the promise that the bytes at this URL will never
	// change. It is only honest when the caller supplied a `?v=` equal to the
	// current content version — which is precisely why the version is in the
	// URL rather than only in a header (D-17).
	cacheImmutable cacheMode = iota
	// cacheShort is what a versionless URL gets: cacheable, but only for a
	// minute, and revalidated after that.
	cacheShort
)

func (m cacheMode) header() string {
	if m == cacheImmutable {
		return "public, max-age=31536000, immutable"
	}
	return "public, max-age=60, must-revalidate"
}

// versionMode applies the `?v=` matrix of arch §5.3, which §7.5 and §7.6 both
// declare normative for covers, thumbnails and pages alike:
//
//	v absent            -> max-age=60, must-revalidate
//	v == current cv     -> immutable
//	v present but stale -> 409 stale_version with detail.cv = the current one
//
// The stale branch is the point of the whole mechanism. Serving the bytes
// anyway would let a browser cache a superseded page for a year under a URL
// that now means something else; answering 409 makes the client refetch the
// book and discover the new cv.
func versionMode(r *http.Request, current string) (cacheMode, error) {
	v := r.URL.Query().Get("v")
	switch {
	case v == "":
		return cacheShort, nil
	case v == current:
		return cacheImmutable, nil
	default:
		return cacheShort, staleVersion(current)
	}
}

// thumbWidth reads `?w=`, defaulting to `thumbnails.widths[0]` — 120 under
// amendments A-1/A-6.
//
// The value is not snapped here: internal/thumbs owns the ladder and snaps
// *up* to the nearest configured width, so a client that asks for 96 gets 120
// and a client that asks for 5000 gets the largest configured entry. This
// layer only rejects values that cannot mean anything.
func (s *Server) thumbWidth(r *http.Request) (int, error) {
	raw := r.URL.Query().Get("w")
	if raw == "" {
		if s.thumbs != nil {
			if widths := s.thumbs.Widths(); len(widths) > 0 {
				return widths[0], nil
			}
		}
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, badParam("w", "w must be an integer").withDetail("value", raw)
	}
	if n <= 0 {
		return 0, badParam("w", "w must be positive").withDetail("value", n)
	}
	return n, nil
}

// handlePageThumb is `GET /api/books/{bid}/thumbs/{n}` (FR-VWR-008).
//
// The thumbnail strip of a 1 071-page volume requests these lazily as it
// scrolls, and the `202` path is what keeps it from blocking: a miss is queued
// and answered immediately rather than held open for a decode.
func (s *Server) handlePageThumb(w http.ResponseWriter, r *http.Request) error {
	if s.idx == nil || s.thumbs == nil {
		return unavailable("the thumbnail cache is not available")
	}
	bid, err := pathID(r, "bid")
	if err != nil {
		return err
	}
	n, err := pageNumber(r, "n")
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
	if int64(n) > book.PageCount {
		return notFound("book %s has %d pages; there is no page %d", bid, book.PageCount, n)
	}
	width, err := s.thumbWidth(r)
	if err != nil {
		return err
	}
	mode, err := versionMode(r, book.ContentVersion)
	if err != nil {
		return err
	}

	return s.serveThumb(w, r, thumbs.Request{
		ID:             bid,
		PageNo:         n,
		Width:          width,
		Priority:       thumbs.PriorityPage,
		ContentVersion: book.ContentVersion,
	}, mode)
}

// serveThumb is the one place a thumbnail turns into a response, so covers and
// page thumbnails cannot answer the same condition two different ways.
//
// The outcome set is the contract's, and three of the four are not errors:
//
//	ready       -> 200 image/jpeg with a strong ETag
//	queued      -> 202 + Retry-After: 1     (a normal answer, impl-plan §4 #3)
//	undecodable -> 422 thumb_unavailable with detail.reason (arch §5.5)
//	missing     -> 404
func (s *Server) serveThumb(w http.ResponseWriter, r *http.Request, req thumbs.Request, mode cacheMode) error {
	release, err := s.acquireImageSlot(r)
	if err != nil {
		return err
	}
	defer release()

	res, err := s.thumbs.Get(r.Context(), req)
	if err != nil {
		if errors.Is(err, thumbs.ErrQueued) {
			writeQueued(w)
			return nil
		}
		return thumbError(err)
	}
	return serveThumbFile(w, r, res, mode)
}

// writeQueued is the `202` of arch §7.5/§7.6: the image does not exist yet and
// a worker is making it. `Retry-After: 1` is the whole protocol — the frontend
// shows a skeleton, waits, and asks again.
func writeQueued(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "1")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", "0")
	w.WriteHeader(http.StatusAccepted)
}

// thumbError maps internal/thumbs' sentinels onto the contract.
func thumbError(err error) error {
	var undec *thumbs.UndecodableError
	if errors.As(err, &undec) {
		return errf(CodeThumbUnavailable, "the source image cannot be decoded").
			withDetail("reason", undec.Reason)
	}
	switch {
	case errors.Is(err, thumbs.ErrUndecodable):
		return errf(CodeThumbUnavailable, "the source image cannot be decoded")
	case errors.Is(err, thumbs.ErrNotFound):
		return notFound("no such page or cover")
	case errors.Is(err, thumbs.ErrBadRequest):
		return badRequest("malformed thumbnail request")
	case errors.Is(err, thumbs.ErrClosed):
		return unavailable("the server is shutting down")
	default:
		return internalErr(err)
	}
}

// serveThumbFile streams a finished thumbnail.
//
// The ETag is `"t1-<key>"` (arch §5.3): the cache key already hashes book,
// page, width, format, quality and content version, so nothing else needs to
// go into the tag and two different pictures can never share one.
func serveThumbFile(w http.ResponseWriter, r *http.Request, res thumbs.Result, mode cacheMode) error {
	f, err := os.Open(res.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Someone deleted the cache between the stat and the open.
			// FR-THM-007 says that must cost latency, not correctness: answer
			// "queued" and the next request regenerates it.
			writeQueued(w)
			return nil
		}
		return internalErr(err)
	}
	defer func() { _ = f.Close() }()

	h := w.Header()
	h.Set("Content-Type", "image/jpeg")
	h.Set("ETag", `"t1-`+res.Key+`"`)
	h.Set("Cache-Control", mode.header())
	h.Set("Accept-Ranges", "bytes")
	// ServeContent handles If-None-Match against the ETag above, If-Modified-
	// Since, Range and HEAD.
	http.ServeContent(w, r, "thumb.jpg", res.ModTime, f)
	return nil
}

// acquireImageSlot bounds concurrent page and thumbnail handlers (arch §6.1).
//
// It returns a release function rather than taking a callback so the caller can
// hold the slot across the whole response — including the body write, which is
// where the memory actually is.
func (s *Server) acquireImageSlot(r *http.Request) (func(), error) {
	select {
	case s.pageSem <- struct{}{}:
		return func() { <-s.pageSem }, nil
	case <-r.Context().Done():
		return nil, unavailable("the request was cancelled while waiting for a serving slot")
	}
}
