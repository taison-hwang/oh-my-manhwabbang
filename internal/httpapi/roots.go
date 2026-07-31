package httpapi

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"shelf/internal/config"
	"shelf/internal/index"
)

// Roots over HTTP — arch §7.4, FR-CFG-001/-002, AMENDMENT A-11 (ruling E-26).
//
// # This file used to say roots were read-only, and that is worth recording
//
// Until 2026-07-30 its comment read: *"Roots are read-only over HTTP: there is
// no POST and no DELETE, by ruling E-3 and prd 5.2 UI-004."* That was correct on
// the evidence it had — prd 5.2 scopes the settings screen to "루트 목록 조회 및
// 수동 재스캔 실행" — and it is still exactly what this server does whenever the
// gate below is shut, which is the default. What changed is that the owner of
// the requirement extended the requirement (ruling E-26), so `POST /api/roots`
// and `DELETE /api/roots/{name}` now exist under seven limits that are part of
// the ruling rather than implementation detail.
//
// # The two rules that shape every handler here
//
//  1. **These endpoints edit `shelf.yaml`; they do not open or close a root in
//     the running server.** Roots are opened exactly once, at startup
//     (`internal/app/app.go` step 6, `source.OpenRoots`), and the open-file
//     pool, the source factory and the scanner are all built over that one set.
//     There is no reload path and A-11 did not buy one. A `POST` therefore
//     produces a *pending* row (revision R2) and a `DELETE` a removed one
//     (revision R1) — neither adopts anything until the restart that
//     `Settings.server.config_changed_on_disk` tells the user to perform.
//
//  2. **Only `internal/config` writes.** This package never opens a file for
//     writing, never renames one and never removes one; it calls
//     `config.AddRoot` / `config.RemoveRoot` and nothing else.
//     `scripts/check-readonly.sh` guard 4 is a grep that keeps that true across
//     six agents.

// maxRootLabelLen bounds the one free-form field of a root, for the same reason
// and with the same number as `library_scope` in §7.8: it must not become a
// place to store arbitrary data, and it ends up in a YAML file this server
// re-parses at startup.
const maxRootLabelLen = 128

// The `detail.reason` vocabulary of §7.4. They are constants because three of
// them are asserted in tests, one is pinned in a golden file, and the frontend
// switches on all of them to choose which instruction to print.
const (
	// The gate (§7.4). Three conditions, one capability, three remedies.
	reasonDisabled         = "disabled"
	reasonNoConfigFile     = "no_config_file"
	reasonConfigInsideRoot = "config_inside_root"

	// `POST` validation (§7.4's table).
	reasonMissing         = "missing"
	reasonNotAbsolute     = "not_absolute"
	reasonDoesNotExist    = "does_not_exist"
	reasonNotADirectory   = "not_a_directory"
	reasonNotReadable     = "not_readable"
	reasonDuplicate       = "duplicate"
	reasonOverlaps        = "overlaps"
	reasonContainsStorage = "contains_storage"
	reasonTooLong         = "too_long"
	reasonControlChars    = "control_characters"

	// The file itself.
	reasonUnparseable = "unparseable"
	// reasonNotABlockSequence is the 409 that is not a broken file: `roots:` is
	// written in flow style (`roots: [{...}]`), which is valid YAML and a legal
	// configuration — `GET /api/roots` reads it — but which the writer cannot
	// splice a line at a time. It is separated from `unparseable` because the
	// remedy is different and actionable: rewrite `roots:` as a block list.
	reasonNotABlockSequence = "not_a_block_sequence"
	reasonFileMissing       = "file_missing"
	reasonLastRoot          = "last_root"
)

// handleRoots is `GET /api/roots`.
//
// It answers from the index — so a root that has left the configuration is
// still listed, with `available: false` — plus two amendments of A-11:
//
//   - **R1**: a root removed by `DELETE` in this process's lifetime is excluded
//     **from the index-derived rows**. On the ordinary path that filter changes
//     nothing and cannot: the `DELETE` already purged the index rows and took
//     the entry out of the file, so the loop is looking at a set the root is not
//     in. What it is for is the window the purge cannot close — a scan that was
//     already walking when the `DELETE` landed will write its `roots` row back
//     through `index.UpsertRoot`, and nothing in `index.DeleteRoot` prevents
//     that. That is the case
//     `TestDeleteRoot_keepsARemovedRootOutWhenSomethingReinsertsItsRows`
//     produces, and it is the only thing that fails if this line is deleted.
//     It does **not** extend to the R2 loop below; see the comment there.
//   - **R2**: a root that is in the configuration file on disk with no index row
//     is appended as `pending: true`, with no counts and no scan timestamps.
func (s *Server) handleRoots(w http.ResponseWriter, r *http.Request) error {
	if s.idx == nil {
		return unavailable("the catalogue is not available")
	}
	rows, err := s.idx.ListRoots(r.Context())
	if err != nil {
		return internalErr(err)
	}

	indexed := make(map[string]bool, len(rows))
	items := make([]Root, 0, len(rows))
	for _, row := range rows {
		if s.rootIsRemoved(row.Name) {
			continue
		}
		indexed[row.Name] = true
		items = append(items, s.toRoot(row))
	}

	// R2. The file is read through the same call that answers
	// `config_changed_on_disk`, so there is one reader of the configuration on
	// disk and not two that can disagree. A file that has gone missing or
	// unparseable since startup simply contributes no pending rows: this is a
	// listing, and refusing to list what the index does know because the file is
	// mid-edit would be a worse answer than showing it.
	//
	// The removed-set is deliberately NOT consulted in this loop, and the
	// asymmetry with the one above is the point. R1 filters rows that come from
	// the *index*, which is where a stale row can survive a removal. These rows
	// come from the configuration file **as it is right now**, so reaching this
	// line at all means the file lists that name at this moment — which after a
	// `DELETE` can only be because something put an entry back: a hand-edit, a
	// restored `.bak`, or `POST /api/roots` generating the retired name again,
	// which §7.4's generator does on purpose (`uniqueRootName` uniquifies against
	// the configuration, not the index, so a re-added directory reattaches the
	// progress `user.db` kept). Skipping it here made that `POST` answer `201`
	// with a `Location` for a resource `GET /api/roots` then refused to list, and
	// a second attempt `400 duplicate` naming a root that was nowhere on screen.
	// Listing it as pending is both true and useful: it *is* in the file, it is
	// not open, and a restart is what opens it — which is exactly what a pending
	// row says. The scan side keeps refusing (§7.10): this process's handle for
	// that name is still the old directory, and no button reaches it anyway
	// because a pending row has no 재스캔.
	if state, ok := s.configFileStateOrZero(r); ok {
		pending := make([]Root, 0, 2)
		for _, cfgRoot := range state.Roots {
			if indexed[cfgRoot.Name] {
				continue
			}
			pending = append(pending, pendingRoot(cfgRoot))
		}
		slices.SortFunc(pending, func(a, b Root) int { return strings.Compare(a.Name, b.Name) })
		items = append(items, pending...)
	}

	writeJSON(w, http.StatusOK, RootsResponse{Items: items})
	return nil
}

// toRoot maps an index row onto the wire type.
//
// `label` falls back to `name` per arch §7.3 ("display name; equals name when
// no label is set"), and the *configured* label wins over the indexed one: a
// label edited in the YAML must show up on restart without waiting for a scan.
func (s *Server) toRoot(row index.Root) Root {
	label := row.Label
	path := row.Path
	enabled := row.Enabled
	if cfgRoot, ok := s.cfg.RootByName(row.Name); ok {
		label = cfgRoot.DisplayName()
		path = cfgRoot.Path
		enabled = cfgRoot.Enabled
	}
	if label == "" {
		label = row.Name
	}
	return Root{
		Name:          row.Name,
		Label:         label,
		Path:          path,
		Enabled:       enabled,
		SeriesCount:   row.SeriesCount,
		BookCount:     row.BookCount,
		PageCount:     row.PageCount,
		TotalBytes:    row.TotalBytes,
		Available:     s.rootAvailable(row.Name),
		LastScanStart: row.LastScanStart,
		LastScanEnd:   row.LastScanEnd,
		LastScanError: nullableString(row.LastScanError),
		Pending:       false,
	}
}

// pendingRoot is R2's row: a configuration entry this server has not opened.
//
// Every number is zero and every timestamp null on purpose. The root has never
// been scanned, so there is no count to report and nothing to invent one from;
// `available` is false because availability is answered through the `*os.Root`
// handle set that startup opened, and this root is not in it.
func pendingRoot(r config.Root) Root {
	return Root{
		Name:      r.Name,
		Label:     r.DisplayName(),
		Path:      r.Path,
		Enabled:   r.Enabled,
		Available: false,
		Pending:   true,
	}
}

// rootAvailable answers `Root.available` — "false when the path is currently
// unreachable".
//
// It is answered through the *os.Root handle rather than by stat-ing a string,
// so this package never assembles a filesystem path at all (NFR-SEC-001
// layer 1). A root missing from the set was unopenable at startup — an
// unmounted drive, a typo in the YAML — and a root whose handle no longer
// stats is one that went away since. Both are `available: false`, which is what
// stops the settings screen claiming a disconnected drive holds 400 series.
func (s *Server) rootAvailable(name string) bool {
	if s.roots == nil {
		return false
	}
	root, ok := s.roots.Root(name)
	if !ok || root == nil {
		return false
	}
	_, err := root.Stat(".")
	return err == nil
}

// nullableString renders "" as JSON null, which is what the contract types for
// every optional string field (`error`, `last_scan_error`, `cover_cv`).
func nullableString(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

// --- the gate --------------------------------------------------------------

// rootEditingReason returns "" when this server may write its `roots:` list,
// and otherwise the `detail.reason` of the `403` (arch §7.4).
//
// The three conditions are one capability — `Settings.server.root_editing_enabled`
// — but the *remedies* differ, which is why the refusal names which one failed:
// "set a key in the file whose path this API publishes", "start this server
// from a configuration file", "move your configuration out from under a media
// volume". A client that only saw 403 could not print any of them.
func (s *Server) rootEditingReason() string {
	if !s.cfg.Server.AllowRootEditing {
		return reasonDisabled
	}
	path := s.cfg.AbsFilePath()
	if path == "" {
		// A-10 documents `config_path: ""` as possible: a server built from a
		// configuration with no file has nothing to edit.
		return reasonNoConfigFile
	}
	for _, root := range s.cfg.Roots {
		if config.IsInside(path, root.Path) {
			// Writing under a media volume is precisely what FR-CFG-005 /
			// NFR-DAT-002 forbid, and `internal/config/validate.go` already
			// refuses `storage.data_dir` and `storage.cache_dir` there for the
			// same reason. It is deliberately NOT a startup error (§3.2): that
			// would stop an existing installation booting over a feature it
			// never asked for.
			return reasonConfigInsideRoot
		}
	}
	return ""
}

// rootEditingEnabled is the capability as `Settings.server` reports it (§7.8).
func (s *Server) rootEditingEnabled() bool { return s.rootEditingReason() == "" }

// gateRootEditing is the first line of both write handlers.
func (s *Server) gateRootEditing() error {
	switch reason := s.rootEditingReason(); reason {
	case "":
		return nil
	case reasonDisabled:
		return forbidden(reason, "editing the roots list is disabled; set server.allow_root_editing in the configuration file")
	case reasonNoConfigFile:
		return forbidden(reason, "this server was not started from a configuration file, so there is nothing to edit")
	default:
		return forbidden(reason, "the configuration file is inside a configured root; SHELF never writes to a media volume")
	}
}

// --- POST /api/roots -------------------------------------------------------

// handleCreateRoot is `POST /api/roots` → `201 RootEntry` (amendment A-11).
//
// The governing rule of the validation below is that **this endpoint must never
// write a file the server would refuse to start from**. It tells the user to
// restart, so handing them `exit 2` would be a defect of the endpoint rather
// than of their configuration; every root-related rejection of
// `internal/config/validate.go` is therefore applied here first, plus A-11's own
// overlap rule.
func (s *Server) handleCreateRoot(w http.ResponseWriter, r *http.Request) error {
	if err := s.gateRootEditing(); err != nil {
		return err
	}
	var body RootCreate
	if err := decodeJSON(w, r, maxJSONBody, &body); err != nil {
		return err
	}

	// One writer at a time, and every write re-reads the file it is about to
	// edit, so a root added by hand between two requests is never silently
	// discarded (§7.4).
	s.rootEdit.Lock()
	defer s.rootEdit.Unlock()

	path := s.cfg.AbsFilePath()
	state, err := config.ReadFileState(path)
	if err != nil {
		return configFileError(err)
	}

	entry, err := s.validateRootCreate(body, state.Roots)
	if err != nil {
		return err
	}
	if err := config.AddRoot(path, config.Root{
		Name: entry.Name, Path: entry.Path, Label: entry.Label, Enabled: true,
	}); err != nil {
		return configFileError(err)
	}
	s.log.InfoContext(r.Context(), "a root was added to the configuration file",
		"root", entry.Name, "restart_required", true)

	// `Location` is the only way the client learns the generated name without
	// re-reading the file, and 201 rather than 200 because a new addressable
	// resource now exists at a URL the client did not choose.
	w.Header().Set("Location", s.base+"/api/roots/"+entry.Name)
	writeJSON(w, http.StatusCreated, entry)
	return nil
}

// validateRootCreate applies §7.4's table and generates the name.
//
// `path` is cleaned and its symlinks resolved **for comparison only**; the value
// written to the file is the cleaned absolute path the caller supplied, so the
// file keeps saying what the operator meant. The rejected value is never echoed
// back: the caller sent it, and keeping absolute host paths out of the envelope
// is what lets these failures be pinned in a golden file at all.
func (s *Server) validateRootCreate(body RootCreate, current []config.Root) (RootEntry, error) {
	raw := strings.TrimSpace(body.Path)
	if raw == "" {
		return RootEntry{}, badRoot("path", reasonMissing, "path is required")
	}
	if !filepath.IsAbs(raw) {
		return RootEntry{}, badRoot("path", reasonNotAbsolute, "path must be absolute")
	}
	// Before anything touches the filesystem. A directory named `media\nb` is
	// legal on Linux, and this endpoint's governing rule is that it must never
	// write a file the server would refuse to start from: written into a
	// double-quoted YAML scalar a line break *folds to a space*, so the file
	// would name a directory that does not exist and the next start would be
	// `exit 2` — from a request that answered `201` echoing the correct path.
	// `\a` is worse still: `yaml.Unmarshal` refuses the file outright ("control
	// characters are not allowed"), so the server cannot even parse what this
	// endpoint wrote. The check is here rather than after the stat because a
	// path carrying a NUL byte fails `os.Stat` with EINVAL, which would be
	// reported as `not_readable` — a true statement about the wrong problem.
	if hasControlChars(raw) {
		return RootEntry{}, badRoot("path", reasonControlChars, "path must not contain control characters")
	}
	clean := filepath.Clean(raw)

	info, err := os.Stat(clean)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return RootEntry{}, badRoot("path", reasonDoesNotExist, "there is no such directory on the server")
	case err != nil:
		return RootEntry{}, badRoot("path", reasonNotReadable, "the server cannot read that path")
	case !info.IsDir():
		return RootEntry{}, badRoot("path", reasonNotADirectory, "that path is a file, not a directory")
	}
	// "Readable" means openable: a directory the server cannot open would index
	// as empty and report `available: false`, which is a root that exists only
	// to look broken.
	dir, err := os.Open(clean)
	if err != nil {
		return RootEntry{}, badRoot("path", reasonNotReadable, "the server cannot read that directory")
	}
	_ = dir.Close()

	label := strings.TrimSpace(body.Label)
	if len(label) > maxRootLabelLen {
		return RootEntry{}, badRoot("label", reasonTooLong, "label must be at most %d bytes", maxRootLabelLen)
	}
	if hasControlChars(label) {
		return RootEntry{}, badRoot("label", reasonControlChars, "label must not contain control characters")
	}

	resolved := resolvePath(clean)
	for _, existing := range current {
		// Disabled roots are checked too: a disabled root is still in the file
		// and still collides the moment it is re-enabled.
		other := resolvePath(filepath.Clean(existing.Path))
		switch {
		case other == resolved:
			return RootEntry{}, badRoot("path", reasonDuplicate,
				"that directory is already a configured root").withDetail("conflicts_with", existing.Name)
		case config.IsInside(resolved, other), config.IsInside(other, resolved):
			// The same file would sit under two roots and get two series
			// identities, one of which the scanner would resolve first
			// arbitrarily.
			return RootEntry{}, badRoot("path", reasonOverlaps,
				"that directory is an ancestor or a descendant of a configured root").
				withDetail("conflicts_with", existing.Name)
		}
	}
	for _, ours := range []string{s.cfg.Storage.DataDir, s.cfg.Storage.CacheDir} {
		if ours == "" {
			continue
		}
		if config.IsInside(resolvePath(ours), resolved) {
			// The mirror of the startup rule that keeps SHELF from writing to a
			// media volume (FR-CFG-005).
			return RootEntry{}, badRoot("path", reasonContainsStorage,
				"SHELF's own data or cache directory is inside that directory")
		}
	}

	if label == "" {
		label = filepath.Base(clean)
	}
	taken := make(map[string]bool, len(current))
	for _, existing := range current {
		taken[existing.Name] = true
	}
	return RootEntry{
		Name:    uniqueRootName(slugifyRootName(labelOrBase(label, clean)), taken),
		Path:    clean,
		Label:   label,
		Enabled: true,
	}, nil
}

// hasControlChars is §7.4's `control_characters` rule, applied to **both**
// free-form strings that reach the YAML writer.
//
// It has to cover `path` and not only `label`, and the reason is not symmetry:
// an omitted `label` is derived as `filepath.Base(path)` *after* validation, so
// checking the label alone lets the very same bytes through by the back door.
// `internal/config`'s `quoteYAML` escapes `\` and `"` and nothing else, by
// design — it is a text splicer, not a YAML emitter — so this is the only place
// a control character can be stopped.
func hasControlChars(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// labelOrBase is the slug's input: the label when there is one, else the
// directory's base name (§7.4).
func labelOrBase(label, path string) string {
	if label != "" {
		return label
	}
	return filepath.Base(path)
}

// resolvePath resolves symlinks for comparison, falling back to the cleaned
// path when it cannot — an unmounted root still has to be compared against.
func resolvePath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
}

// badRoot is §7.4's `400`: `detail: {field, reason}`, and never the value.
func badRoot(field, reason, format string, args ...any) *apiError {
	return badRequest(format, args...).withDetail("field", field, "reason", reason)
}

// maxRootNameLen is §3.2's `[a-zA-Z0-9._-]{1,64}`.
const maxRootNameLen = 64

// slugifyRootName is step 1 of §7.4's name generation.
//
// Lowercase ASCII letters and digits pass through, `.`, `_` and `-` are kept,
// every other byte — including all Hangul, which is the common case here — is
// dropped, and runs of dropped bytes collapse to a single `-`. Uppercase ASCII
// is lowercased rather than dropped: "slugify" means the output alphabet is
// lowercase, and dropping it would turn `Manga` into `anga`. The result
// satisfies §3.2's alphabet by construction.
func slugifyRootName(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	dropped := false
	for _, r := range s {
		var keep rune
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			keep = r
		case r >= 'A' && r <= 'Z':
			keep = r + ('a' - 'A')
		default:
			dropped = true
			continue
		}
		if dropped && b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
			b.WriteByte('-')
		}
		dropped = false
		b.WriteRune(keep)
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > maxRootNameLen {
		out = strings.Trim(out[:maxRootNameLen], "-")
	}
	return out
}

// uniqueRootName is steps 2 and 3: an empty slug falls back to `root`, and a
// collision **with a name in the current configuration** gets `-2`, `-3`, …
//
// Checking the configuration and not the index is deliberate, and revision R1
// is what makes it load-bearing. An ex-configured root's name is exactly the
// name a re-added directory should get back: the same label over the same
// directory produces the same slug, and that is what reattaches the reading
// progress `DELETE` went out of its way to keep in `user.db`.
func uniqueRootName(base string, taken map[string]bool) string {
	if base == "" {
		base = "root"
	}
	if !taken[base] {
		return base
	}
	for n := 2; ; n++ {
		suffix := "-" + strconv.Itoa(n)
		stem := base
		if len(stem)+len(suffix) > maxRootNameLen {
			stem = strings.TrimRight(stem[:maxRootNameLen-len(suffix)], "-")
		}
		if candidate := stem + suffix; !taken[candidate] {
			return candidate
		}
	}
}

// --- DELETE /api/roots/{name} ----------------------------------------------

// handleDeleteRoot is `DELETE /api/roots/{name}` → `204` (amendment A-11,
// revision R1).
//
// It removes the entry from the file **and purges that root's rows from
// `index.db`**, and it does the purge FIRST. The order is the whole of the
// failure design: if the file write then fails the request is `500`, the root is
// still configured, and the next scan re-indexes it completely — `index.db` is
// the derived, disposable half (arch §3.5). The other order fails the other way,
// leaving a root gone from the file with its rows orphaned in an index that
// `App.reconcileRoots` deliberately keeps forever, which is the exact defect R1
// exists to remove.
//
// `user.db` is not touched at any point. Reading progress, per-book preferences
// and `series_seen` survive, and they reattach if the same directory is added
// again under the same generated name.
func (s *Server) handleDeleteRoot(w http.ResponseWriter, r *http.Request) error {
	if err := s.gateRootEditing(); err != nil {
		return err
	}
	// A configuration identity, not one of §7.1's opaque ids — but the same
	// split: syntactically invalid is 400, well-formed but absent is 404.
	name := r.PathValue("name")
	if !config.ValidRootName(name) {
		return badParam("name", "a root name is 1..64 characters of [a-zA-Z0-9._-]").
			withDetail("value", name)
	}

	s.rootEdit.Lock()
	defer s.rootEdit.Unlock()

	path := s.cfg.AbsFilePath()
	state, err := config.ReadFileState(path)
	if err != nil {
		return configFileError(err)
	}
	if !slices.ContainsFunc(state.Roots, func(r config.Root) bool { return r.Name == name }) {
		return notFound("no root named %q is in the configuration file", name)
	}
	if len(state.Roots) == 1 {
		return conflict("the last root cannot be removed; the server needs at least one to start").
			withDetail("reason", reasonLastRoot)
	}

	if s.idx != nil {
		if err := s.idx.DeleteRoot(r.Context(), name); err != nil {
			return internalErr(err)
		}
	}
	if _, err := config.RemoveRoot(path, name); err != nil {
		return configFileError(err)
	}
	s.markRootRemoved(name)
	s.log.InfoContext(r.Context(), "a root was removed from the configuration file and purged from the index",
		"root", name, "user_data_kept", true)

	noContent(w)
	return nil
}

// configFileError maps `internal/config`'s refusals onto §7.4's status table.
func configFileError(err error) error {
	switch {
	case errors.Is(err, config.ErrNotABlockSequence):
		// Checked before ErrUnparseable, which it deliberately satisfies too.
		// This is the one 409 here that is *not* a broken file: the same file is
		// being listed by `GET /api/roots` on the same screen, so "cannot be
		// read" is a sentence the user can only disbelieve. The remedy is
		// specific and the UI can print it.
		return conflict("the `roots:` list is written in flow style, which this server cannot edit in place").
			withDetail("reason", reasonNotABlockSequence).wrap(err)
	case errors.Is(err, config.ErrUnparseable):
		// Nothing was written and no `.bak` was taken: a writer that cannot
		// understand the file cannot promise to preserve it, and overwriting it
		// would destroy an edit the user is halfway through.
		return conflict("the configuration file on disk can no longer be parsed").
			withDetail("reason", reasonUnparseable).wrap(err)
	case errors.Is(err, os.ErrNotExist):
		return conflict("the configuration file is no longer where this server loaded it from").
			withDetail("reason", reasonFileMissing).wrap(err)
	case errors.Is(err, config.ErrLastRoot):
		return conflict("the last root cannot be removed; the server needs at least one to start").
			withDetail("reason", reasonLastRoot).wrap(err)
	case errors.Is(err, config.ErrRootNotFound):
		return notFound("that root is not in the configuration file")
	case errors.Is(err, config.ErrDuplicateRootName):
		// The name was free when this request read the file. Something else
		// took it in between, which is why the writer re-checks.
		return conflict("a root of that name was added while this request was in flight").
			withDetail("reason", reasonDuplicate).wrap(err)
	default:
		// The message names no path (§8.4); the client already holds
		// `Settings.server.config_path`.
		return internalErr(err)
	}
}

// --- the removed-set (revision R1) -----------------------------------------

// markRootRemoved records that this process has removed a root.
func (s *Server) markRootRemoved(name string) {
	s.removedMu.Lock()
	defer s.removedMu.Unlock()
	if s.removedRoots == nil {
		s.removedRoots = make(map[string]struct{}, 1)
	}
	s.removedRoots[name] = struct{}{}
}

// rootIsRemoved reports whether this process removed that root.
//
// The set exists because "restart-based" was never meant to mean "the thing you
// deleted keeps working until you reboot", and it is what lets the running
// process honour a removal without hot-swapping the open root set, the handle
// pool or the source factory — none of which A-11 bought.
//
// Its two callers do different amounts of work, and saying so is the point:
//
//   - `scanRoots` (scan.go) genuinely depends on it. The configuration this
//     process loaded still lists the root, and a scan of it would re-index
//     everything the `DELETE` purged.
//   - `handleRoots` does not, on the ordinary path — the purge has already
//     removed the rows and the file no longer has the entry. It is a guard for
//     one window: a scan that was already walking can re-insert the `roots` row
//     after the purge. See `handleRoots`'s own comment and the test named there.
func (s *Server) rootIsRemoved(name string) bool {
	s.removedMu.RLock()
	defer s.removedMu.RUnlock()
	_, ok := s.removedRoots[name]
	return ok
}

// removedRootNames is a snapshot, for the callers that need the whole set.
func (s *Server) removedRootNames() map[string]struct{} {
	s.removedMu.RLock()
	defer s.removedMu.RUnlock()
	if len(s.removedRoots) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(s.removedRoots))
	for name := range s.removedRoots {
		out[name] = struct{}{}
	}
	return out
}
