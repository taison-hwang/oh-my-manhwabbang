package httpapi

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"shelf/internal/config"
	"shelf/internal/natsort"
)

// The directory picker — `GET /api/browse`, AMENDMENT A-12 (ruling E-40).
//
// # This is the one endpoint in the package that takes a filesystem path
//
// Every other route takes an opaque 16-character id and resolves it through the
// index (see the package comment, NFR-SEC-001 layer 1). This one cannot: its
// entire purpose is to help a user name a directory the server has never seen,
// which is by definition not in the index. So it replaces layer 1 with two
// other limits, and both are load-bearing rather than defence in depth:
//
//  1. **`server.browse_bases` is an allowlist, and an empty one refuses
//     everything.** The endpoint lists a base or something under a base, and
//     nothing else. It is not "the filesystem minus some denied paths" — there
//     is no path that reaches it without first matching a configured base.
//  2. **The read itself goes through `os.Root`**, opened on the matched base.
//     That is exactly the layer-3 handle every media root uses, and it refuses
//     an escape at the openat(2) level, symlinks included. A `..` in the
//     request, or a symlink inside a base pointing at `/etc`, is refused by the
//     kernel and not by a string comparison in this file.
//
// It is additionally behind `gateRootEditing()`, so the capability cannot be
// reached at all unless the operator has already granted the strictly larger
// one — mounting any readable directory as a root. E-40 §3 is explicit that
// browsing must never be the *first* privilege an installation grants.
//
// # It reports directories only, and it reports why one cannot be chosen
//
// The `selectable` flag on every entry is computed from the same rules
// `validateRootCreate` applies, from the same helpers. A picker that offered a
// directory `POST /api/roots` would then reject with `overlaps` would be a
// picker that lies, and the frontend must not re-implement §7.4's table to
// avoid that.

const (
	// browseMaxEntries bounds one listing. A media volume's top level is tens of
	// entries; a directory with a hundred thousand children is not a place a
	// user picks a root from, and streaming it would cost the client more than
	// it costs us. Truncation is reported rather than silent (§6.5).
	browseMaxEntries = 2000

	// The `detail.reason` vocabulary this endpoint adds to §7.4's.
	reasonNoBrowseBases = "no_browse_bases"
	reasonOutsideBases  = "outside_browse_bases"
)

// handleBrowse is `GET /api/browse[?path=…]` → `200 BrowseResponse`.
//
// With no `path` it answers the synthetic top level: the configured bases
// themselves. That is why `path` is optional rather than defaulted to `/` — the
// bases are the roots of this tree, and there is no node above them.
func (s *Server) handleBrowse(w http.ResponseWriter, r *http.Request) error {
	if err := s.gateRootEditing(); err != nil {
		return err
	}
	bases := s.browseBases()
	if len(bases) == 0 {
		return forbidden(reasonNoBrowseBases,
			"no directories are open to the picker; set server.browse_bases in the configuration file")
	}

	raw := strings.TrimSpace(r.URL.Query().Get("path"))
	if raw == "" {
		return s.browseBaseList(w, bases)
	}
	if !filepath.IsAbs(raw) {
		return badRoot("path", reasonNotAbsolute, "path must be absolute")
	}
	if hasControlChars(raw) {
		return badRoot("path", reasonControlChars, "path must not contain control characters")
	}
	clean := filepath.Clean(raw)

	base, rel, ok := matchBrowseBase(clean, bases)
	if !ok {
		// Deliberately the same answer for "there is no such directory" and "that
		// directory is not one this server will list": a caller must not be able
		// to probe for the existence of paths outside the allowlist by reading
		// our error codes.
		return forbidden(reasonOutsideBases,
			"that directory is not inside any of this server's browsable directories")
	}

	entries, truncated, err := readSubdirs(base, rel)
	if err != nil {
		return browseReadError(err)
	}

	current := s.browseEntry(clean)
	parent := browseParent(clean, base)
	writeJSON(w, http.StatusOK, BrowseResponse{
		Path:      clean,
		Parent:    parent,
		Self:      &current,
		Entries:   s.browseEntries(clean, entries),
		Truncated: truncated,
	})
	return nil
}

// browseBaseList is the synthetic top level.
//
// A base that cannot be opened is listed and marked unselectable rather than
// hidden. Hiding it would make an unmounted drive indistinguishable from a
// typo in the configuration, which is the same reasoning that makes an
// unreachable root `available: false` instead of absent (§7.3).
func (s *Server) browseBaseList(w http.ResponseWriter, bases []string) error {
	entries := make([]BrowseEntry, 0, len(bases))
	for _, base := range bases {
		entry := s.browseEntry(base)
		if _, err := os.Stat(base); err != nil {
			entry.Selectable = false
			entry.Reason = reasonPtr(reasonDoesNotExist)
		}
		entries = append(entries, entry)
	}
	writeJSON(w, http.StatusOK, BrowseResponse{
		Path:    "",
		Parent:  nil,
		Self:    nil,
		Entries: entries,
	})
	return nil
}

// browseBases is the allowlist, cleaned at load (`config.validate`).
func (s *Server) browseBases() []string { return s.cfg.Server.BrowseBases }

// matchBrowseBase finds the base that contains clean, longest match first.
//
// Longest match matters for the same reason it does in `RootSet.PoolOpener`:
// two bases may nest, and opening the shorter one would still be safe — os.Root
// refuses the escape either way — but which one served a request would vary
// between runs.
func matchBrowseBase(clean string, bases []string) (base, rel string, ok bool) {
	var best string
	for _, b := range bases {
		if clean != b && !config.IsInside(clean, b) {
			continue
		}
		if len(b) > len(best) {
			best = b
		}
	}
	if best == "" {
		return "", "", false
	}
	if clean == best {
		return best, ".", true
	}
	rel, err := filepath.Rel(best, clean)
	if err != nil {
		return "", "", false
	}
	return best, rel, true
}

// readSubdirs lists the immediate sub-directories of rel inside base, through
// an os.Root opened on base.
//
// Non-directories are dropped: this picker chooses a root, and a root is a
// directory. A symlink is reported by ReadDir as a symlink rather than as its
// target, and is dropped with them — following one here would be this file
// deciding to leave the base, which is precisely what os.Root exists to
// prevent. A symlinked media directory must be named by its real path.
func readSubdirs(base, rel string) (names []string, truncated bool, err error) {
	root, err := os.OpenRoot(base)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = root.Close() }()

	f, err := root.Open(rel)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = f.Close() }()

	// Asked of the open handle rather than of the path, and asked before
	// ReadDir: `f.ReadDir` on a regular file fails with a platform-specific
	// errno that is ENOTDIR on Linux and something else on Windows, and mapping
	// that would be a portability bug in an error message. One Stat answers it
	// the same way everywhere.
	info, err := f.Stat()
	if err != nil {
		return nil, false, err
	}
	if !info.IsDir() {
		return nil, false, errNotADirectory
	}

	// ReadDir(-1) rather than a streamed loop: the cap below bounds the result
	// and these are directory entries, not file contents.
	des, err := f.ReadDir(-1)
	if err != nil {
		return nil, false, err
	}
	names = make([]string, 0, min(len(des), browseMaxEntries))
	for _, de := range des {
		if !de.IsDir() {
			continue
		}
		if len(names) == browseMaxEntries {
			truncated = true
			break
		}
		names = append(names, de.Name())
	}
	slices.SortFunc(names, natsort.Compare)
	return names, truncated, nil
}

// browseEntries turns names under dir into wire entries.
func (s *Server) browseEntries(dir string, names []string) []BrowseEntry {
	out := make([]BrowseEntry, 0, len(names))
	for _, name := range names {
		out = append(out, s.browseEntry(filepath.Join(dir, name)))
	}
	return out
}

// browseEntry describes one directory as an add candidate.
//
// The rules are `validateRootCreate`'s, in its order, minus the ones that
// cannot apply to a path this endpoint produced (it exists, it is a directory,
// it is absolute and clean). Keeping the order identical is what makes the
// picker's greyed-out reason and the `400` a user would get by typing the same
// path the same sentence.
func (s *Server) browseEntry(path string) BrowseEntry {
	e := BrowseEntry{Name: filepath.Base(path), Path: path, Selectable: true}
	resolved := resolvePath(path)
	for _, existing := range s.configuredRoots() {
		other := resolvePath(filepath.Clean(existing.Path))
		switch {
		case other == resolved:
			e.Selectable, e.Reason = false, reasonPtr(reasonDuplicate)
			return e
		case config.IsInside(resolved, other), config.IsInside(other, resolved):
			e.Selectable, e.Reason = false, reasonPtr(reasonOverlaps)
			return e
		}
	}
	for _, ours := range []string{s.cfg.Storage.DataDir, s.cfg.Storage.CacheDir} {
		if ours == "" {
			continue
		}
		if config.IsInside(resolvePath(ours), resolved) {
			e.Selectable, e.Reason = false, reasonPtr(reasonContainsStorage)
			return e
		}
	}
	return e
}

// browseParent is the directory above path, or null when path is a base.
//
// It stops at the base rather than walking to `/`, because a `..` out of the
// allowlist is not somewhere this endpoint will go and offering it would render
// a breadcrumb whose next click is a 403.
func browseParent(clean, base string) *string {
	if clean == base {
		return nil
	}
	parent := filepath.Dir(clean)
	if parent == clean {
		return nil
	}
	return &parent
}

func reasonPtr(s string) *string { return &s }

// browseReadError maps a failed listing onto the contract.
//
// `os.Root` returns the same `ErrNotExist` for a path that is not there and for
// one that tried to escape, and that conflation is convenient here rather than
// awkward: both are "no such directory under a base", which is what the client
// is told.
func browseReadError(err error) error {
	switch {
	case errors.Is(err, errNotADirectory):
		return badRoot("path", reasonNotADirectory, "that path is a file, not a directory")
	case errors.Is(err, os.ErrNotExist):
		return badRoot("path", reasonDoesNotExist, "there is no such directory on the server")
	default:
		// Permission denied lands here with every other read failure, and on
		// purpose: they are one remedy from the caller's side.
		return badRoot("path", reasonNotReadable, "the server cannot read that directory")
	}
}

// errNotADirectory is readSubdirs' one sentinel, so the mapping above does not
// have to recognise a platform's errno spelling.
var errNotADirectory = errors.New("not a directory")
