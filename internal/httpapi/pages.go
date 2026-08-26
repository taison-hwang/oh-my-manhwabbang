package httpapi

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"shelf/internal/archive"
	"shelf/internal/index"
	"shelf/internal/pdfium"
	"shelf/internal/source"
)

// handlePage is `GET /api/books/{bid}/pages/{n}` — the hot path (arch §7.6).
//
// Everything about this handler is shaped by FR-SRV-001/-002/-008 and
// NFR-PRF-006: the bytes come straight out of the archive at a stored offset,
// through an io.ReaderAt, into the socket. Nothing is extracted to a temporary
// file, nothing buffers the container, and for a zip or dir page nothing
// re-encodes — the body is byte-identical to the archive member, which is what
// AC-001 and the CRC check of arch §5.1 assert.
func (s *Server) handlePage(w http.ResponseWriter, r *http.Request) error {
	if s.idx == nil || s.sources == nil {
		return unavailable("page serving is not available")
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
	// The status describes how the *container* read. Whether a page can be
	// served is a different question, and the index already answers it: there
	// is a row for it or there is not.
	//
	// This used to refuse anything that was not `ok`, on the reasoning that a
	// page of an unreadable book is genuinely absent. That held while a damaged
	// book had no pages — and it stopped holding twice. The scanner has kept
	// the pages of a partially readable directory since WP-04 (`군계(軍鷄)
	// 07권.zip` keeps 101 of its 102), and zipidx now rebuilds a whole entry
	// list from local headers when the directory is gone (salvage.go). In both
	// cases the bytes are present, seekable and CRC-checkable, and it was the
	// *flag* that was losing them.
	//
	// FR-IDX-010 does not ask for the refusal. It asks that a damaged or
	// encrypted archive be marked with an error status and not abort the scan —
	// which is exactly what still happens: the volume keeps its badge, its
	// `error` string and its scan_log row. See ruling E-54.
	//
	// Encrypted is the one status that refuses on its own account. FR-IDX-010
	// pairs it with damage, and the rule there is flag-and-never-decode, so it
	// must not stream even if entries were somehow listed.
	if book.Status == string(archive.StatusEncrypted) {
		return notFound("book %s is password-protected", bid)
	}
	if book.PageCount <= 0 {
		return notFound("book %s is not readable (status %q)", bid, book.Status)
	}
	if int64(n) > book.PageCount {
		return notFound("book %s has %d pages; there is no page %d", bid, book.PageCount, n)
	}

	mode, err := versionMode(r, book.ContentVersion)
	if err != nil {
		return err
	}

	page, err := s.idx.GetPage(r.Context(), bid, n)
	if err != nil {
		if errors.Is(err, index.ErrNotFound) {
			return notFound("book %s has no page %d", bid, n)
		}
		return internalErr(err)
	}

	if book.Kind == string(source.KindPDF) {
		return s.servePDFPage(w, r, book, page, mode)
	}
	return s.serveArchivePage(w, r, book, page, mode)
}

// serveArchivePage streams a ZIP entry or a loose file (FR-SRV-001, -005, -008).
func (s *Server) serveArchivePage(w http.ResponseWriter, r *http.Request, book index.BookRow, page index.Page, mode cacheMode) error {
	release, err := s.acquireImageSlot(r)
	if err != nil {
		return err
	}
	defer release()

	src, err := s.sources.Open(r.Context(), bookOf(book))
	if err != nil {
		return sourceError(err)
	}
	defer func() { _ = src.Close() }()

	// A container whose (size, mtime) no longer match the index is being read
	// at offsets that may mean nothing. When the client supplied a `?v=` it has
	// metadata to refresh, so 409 sends it back for a new cv rather than
	// handing it bytes from the wrong place (arch §5.2, §7.6).
	//
	// The rescan is not optional and not merely tidy: the `cv` in the 409 is the
	// index's, i.e. the one the client just sent, so without re-indexing the
	// book the client can refetch for ever and never see a different answer.
	// It is enqueued whether or not `?v=` was supplied — arch §5.2 ties it to
	// the disagreement, not to the response — and coalesced per book so that
	// reading a 1,071-page volume cannot start 1,071 scans.
	if checker, ok := src.(source.StaleChecker); ok {
		stale, serr := checker.Stale(r.Context())
		if serr == nil && stale {
			s.log.WarnContext(r.Context(), "serving a book whose container changed on disk",
				"book_id", book.ID, "root", book.RootName, "rel_path", book.RelPath)
			s.enqueueStaleRescan(r.Context(), book)
			if r.URL.Query().Get("v") != "" {
				return staleVersion(book.ContentVersion)
			}
		}
	}

	stream, err := src.Open(r.Context(), pageOf(page), source.OpenOptions{})
	if err != nil {
		return sourceError(err)
	}
	defer func() { _ = stream.Close() }()

	h := w.Header()
	// Content-Type comes from a fixed extension table, never from sniffing
	// (arch §5.3) — combined with nosniff, that is what stops an entry called
	// `x.jpg` holding HTML from being rendered as HTML.
	h.Set("Content-Type", contentTypeFor(page.Ext, stream.ContentType))
	h.Set("ETag", pageETag(book, page))
	h.Set("Cache-Control", mode.header())

	if rs, ok := stream.ReadSeeker(); ok {
		// Stored entries and dir pages are seekable, so ServeContent gives
		// Range, If-Range, If-None-Match and HEAD for free.
		h.Set("Accept-Ranges", "bytes")
		http.ServeContent(w, r, "", stream.ModTime, rs)
		return nil
	}

	// A deflated entry is a forward-only stream. Accept-Ranges is deliberately
	// omitted and a Range request is answered 200 with the whole body: RFC 9110
	// permits ignoring Range, and an <img> never needs it. The length is still
	// known from the central directory, so the client still gets a progress
	// bar and a properly framed response.
	if stream.Size >= 0 {
		h.Set("Content-Length", strconv.FormatInt(stream.Size, 10))
	}
	if !stream.ModTime.IsZero() {
		h.Set("Last-Modified", stream.ModTime.UTC().Format(http.TimeFormat))
	}
	if noneMatch(r, h.Get("ETag")) {
		w.WriteHeader(http.StatusNotModified)
		return nil
	}
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return nil
	}
	if _, err := io.Copy(w, stream); err != nil {
		// The response has already begun; there is no status left to change.
		// Log it and let the truncated body speak for itself.
		s.log.WarnContext(r.Context(), "page stream interrupted",
			"book_id", book.ID, "page", page.PageNo, "err", err)
	}
	return nil
}

// servePDFPage rasterises a PDF page (FR-SRV-006, AC-004).
//
// The viewer cannot tell the difference: it asks for page n and gets a JPEG,
// exactly as it does for a ZIP. `w` is the resolution parameter FR-SRV-006
// requires, clamped to `pdf.max_width` and snapped to 100 px so that dragging a
// zoom slider cannot spawn a hundred distinct renders.
func (s *Server) servePDFPage(w http.ResponseWriter, r *http.Request, book index.BookRow, page index.Page, mode cacheMode) error {
	if !s.pdfEnabled() {
		return unsupported("this build cannot render PDF pages")
	}
	width, err := s.pdfWidth(r)
	if err != nil {
		return err
	}

	h := w.Header()
	h.Set("Content-Type", "image/jpeg")
	h.Set("ETag", pdfETag(book, page, width))
	h.Set("Cache-Control", mode.header())
	h.Set("Accept-Ranges", "bytes")

	// A render is expensive (~300 ms) and perfectly reproducible, so it is
	// cached under <cache_dir>/pdf keyed by (book, page, width, cv) — the same
	// structural invalidation as a thumbnail (arch §5.6). A cache entry that
	// disappeared between the lookup and the open is not an error: FR-THM-007
	// says deleting the cache costs latency, so the render below runs instead.
	if s.thumbs != nil && s.cfg.PDF.CacheRenders {
		if res, ok := s.thumbs.LookupPDFPage(book.ID, page.PageNo, width, book.ContentVersion); ok {
			if f, err := os.Open(res.Path); err == nil {
				defer func() { _ = f.Close() }()
				http.ServeContent(w, r, "", res.ModTime, f)
				return nil
			}
		}
	}

	release, err := s.acquireImageSlot(r)
	if err != nil {
		return err
	}
	defer release()

	src, err := s.sources.Open(r.Context(), bookOf(book))
	if err != nil {
		return sourceError(err)
	}
	defer func() { _ = src.Close() }()

	stream, err := src.Open(r.Context(), pageOf(page), source.OpenOptions{Width: width})
	if err != nil {
		return sourceError(err)
	}
	defer func() { _ = stream.Close() }()

	// The rendered page is already fully in memory inside internal/source
	// (it has to be — JPEG encoding is not streamable), so reading it here
	// costs nothing extra and lets the result be cached before it is sent.
	body, err := io.ReadAll(stream)
	if err != nil {
		return internalErr(err)
	}
	if s.thumbs != nil && s.cfg.PDF.CacheRenders {
		if _, err := s.thumbs.StorePDFPage(book.ID, page.PageNo, width, book.ContentVersion, body); err != nil {
			// A cache write failure is not a request failure.
			s.log.WarnContext(r.Context(), "caching a rendered pdf page",
				"book_id", book.ID, "page", page.PageNo, "err", err)
		}
	}

	http.ServeContent(w, r, "", stream.ModTime, bytes.NewReader(body))
	return nil
}

// pageETag is the strong validator of arch §5.3.
//
//	zip page  "p1-<book_id>-<page_no>-<crc32, 8 lowercase hex>"
//	dir page  "f1-<book_id>-<page_no>-<size hex>-<mtime hex>"
//
// The CRC is free from the central directory, so a ZIP page's tag is derived
// from the bytes themselves rather than from their metadata: two archives whose
// entries have the same name and size but different content cannot collide.
func pageETag(book index.BookRow, page index.Page) string {
	if book.Kind == string(source.KindDir) {
		return fmt.Sprintf(`"f1-%s-%d-%x-%x"`, book.ID, page.PageNo, page.Size, page.Mtime)
	}
	return fmt.Sprintf(`"p1-%s-%d-%08x"`, book.ID, page.PageNo, page.CRC32)
}

// pdfETag is `"r1-<book_id>-<page_no>-<width>-<cv>"`: a rasterisation is a
// function of exactly those four things.
func pdfETag(book index.BookRow, page index.Page, width int) string {
	return fmt.Sprintf(`"r1-%s-%d-%d-%s"`, book.ID, page.PageNo, width, book.ContentVersion)
}

// pdfWidth reads the FR-SRV-006 resolution parameter.
func (s *Server) pdfWidth(r *http.Request) (int, error) {
	raw := r.URL.Query().Get("w")
	if raw == "" {
		return pdfium.SnapWidth(s.cfg.PDF.DefaultWidth, s.cfg.PDF.MaxWidth), nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, badParam("w", "w must be an integer").withDetail("value", raw)
	}
	if n <= 0 {
		return 0, badParam("w", "w must be positive").withDetail("value", n)
	}
	return pdfium.SnapWidth(n, s.cfg.PDF.MaxWidth), nil
}

// contentTypeFor resolves a page's media type from its extension table entry,
// falling back to whatever the source declared (a rendered PDF page says
// image/jpeg for itself).
func contentTypeFor(ext, fromSource string) string {
	if ct := source.ContentType(ext); ct != "" && ct != "application/octet-stream" {
		return ct
	}
	if fromSource != "" {
		return fromSource
	}
	return "application/octet-stream"
}

// noneMatch implements the If-None-Match check for the one path that does not
// go through http.ServeContent: a forward-only deflate stream.
func noneMatch(r *http.Request, etag string) bool {
	if etag == "" {
		return false
	}
	header := r.Header.Get("If-None-Match")
	if header == "" {
		return false
	}
	if header == "*" {
		return true
	}
	for _, candidate := range splitETags(header) {
		if candidate == etag {
			return true
		}
	}
	return false
}

// splitETags splits a comma-separated If-None-Match, tolerating the weak
// prefix. A weak match is enough for a conditional GET (RFC 9110 §13.1.2).
func splitETags(header string) []string {
	out := make([]string, 0, 4)
	for _, part := range bytes.Split([]byte(header), []byte(",")) {
		tag := string(bytes.TrimSpace(part))
		tag, _ = strings.CutPrefix(tag, "W/")
		if tag != "" {
			out = append(out, tag)
		}
	}
	return out
}

// bookOf maps an index row onto the value internal/source consumes. The two
// types are deliberately separate: internal/source never imports internal/index
// (its Book is "a plain value so this package never imports internal/index"),
// so this three-line copy is the seam.
func bookOf(row index.BookRow) source.Book {
	return source.Book{
		ID:        row.ID,
		Kind:      source.Kind(row.Kind),
		RootName:  row.RootName,
		RelPath:   row.RelPath,
		InnerPath: row.InnerPath,
		FileSize:  row.FileSize,
		FileMtime: row.FileMtime,
	}
}

// pageOf maps a page row onto the source's page value.
//
// Note what crosses: an offset, a length, a method and a CRC. No name, no path
// — FR-SRV-002 seeks to `local_hdr_off` and reads `comp_size + 30` bytes, so
// the entry name is display metadata that never reaches the filesystem
// (NFR-SEC-001, arch §8.1 layer 1).
func pageOf(p index.Page) source.Page {
	return source.Page{
		No:          p.PageNo,
		Name:        p.Name,
		EntryPath:   p.EntryPath,
		Ext:         p.Ext,
		Size:        p.Size,
		CompSize:    p.CompSize,
		Method:      uint16(p.Method), //nolint:gosec // 0 or 8; the schema stores it as an int
		LocalHdrOff: p.LocalHdrOff,
		CRC32:       p.CRC32,
		Mtime:       p.Mtime,
	}
}

// sourceError maps internal/source's failures onto the contract (arch §7.6).
func sourceError(err error) error {
	switch {
	case errors.Is(err, source.ErrUnsupported):
		return unsupported("this build cannot read that book format")
	case errors.Is(err, source.ErrNoPages):
		return notFound("the book holds no readable pages")
	case errors.Is(err, source.ErrUnknownRoot):
		return unavailable("the media volume for this book is not reachable")
	case errors.Is(err, source.ErrUnsafePath):
		// Layer 2 refused a stored relative path. That can only happen if the
		// index was tampered with, so it is a 500 and an error-level log, not a
		// client mistake (arch §8.1 layer 4).
		return internalErr(err)
	case errors.Is(err, archive.ErrEncrypted):
		return errf(CodeUnprocessable, "the archive is password-protected")
	case errors.Is(err, archive.ErrCorrupt), errors.Is(err, archive.ErrUnsupportedMethod):
		return errf(CodeUnprocessable, "the archive entry cannot be read")
	default:
		return unavailable("the page could not be read from the media volume")
	}
}
