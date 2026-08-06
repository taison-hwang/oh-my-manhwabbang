package httpapi

import (
	"net/http"
	"strconv"
	"strings"
	"unicode"

	"shelf/internal/buildinfo"
	"shelf/internal/config"
	"shelf/internal/index"
)

// The keys of the user.db `settings` store. They are the wire field names, so a
// value written by one version is read by the next without a migration, and a
// reader of the database can tell what a row means.
const (
	settingReadingDirection = "reading_direction"
	settingDisplayMode      = "display_mode"
	settingFitMode          = "fit_mode"
	settingPrefetch         = "prefetch"
	settingTheme            = "theme"
	settingLibraryView      = "library_view"
	settingLibrarySort      = "library_sort"
	settingLibraryOrder     = "library_order"
	settingLibraryScope     = "library_scope"
)

// Enumerations for the user-mutable settings that are not already covered by
// the book-prefs enums.
var (
	themes        = []string{"light", "dark", "system"}
	libraryViews  = []string{"grid", "list"}
	librarySorts  = []string{index.SortName, index.SortMtime, index.SortRecent, index.SortSize, index.SortBooks, index.SortAdded}
	libraryOrders = []string{"asc", "desc"}
)

// Bounds and defaults for the rest.
const (
	prefetchMin = 0
	prefetchMax = 20

	defaultLibraryView  = "grid"
	defaultLibrarySort  = index.SortName
	defaultLibraryOrder = "asc"
	// defaultLibraryScope is the sidebar's 전체 시리즈 (amendment A-5).
	defaultLibraryScope = "all"
	// maxLibraryScopeLen bounds the one free-form setting: `library_scope` may
	// be a root name, so it cannot be a closed enum, but it can be stopped from
	// becoming a place to store arbitrary data.
	maxLibraryScopeLen = 128
)

// handleGetSettings is `GET /api/settings` (UI-004).
func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) error {
	settings, err := s.settings(r)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, settings)
	return nil
}

// handlePutSettings is `PUT /api/settings`: partial, so only the keys sent
// change.
//
// Sending anything under `server` is `400 bad_request` — that block is a
// read-only mirror of the YAML (arch §7.8), and it falls out of strict decoding
// for free because settingsUpdateBody has no `server` field. Roots are not
// editable here either (C-5, ruling E-3).
func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) error {
	if s.user == nil {
		return unavailable("user data is not available")
	}
	var body settingsUpdateBody
	if err := decodeJSON(w, r, maxJSONBody, &body); err != nil {
		return err
	}

	values := make(map[string]string, 9)
	if err := enumSetting(values, settingReadingDirection, body.ReadingDirection, readingDirections); err != nil {
		return err
	}
	if err := enumSetting(values, settingDisplayMode, body.DisplayMode, displayModes); err != nil {
		return err
	}
	if err := enumSetting(values, settingFitMode, body.FitMode, fitModes); err != nil {
		return err
	}
	if err := enumSetting(values, settingTheme, body.Theme, themes); err != nil {
		return err
	}
	if err := enumSetting(values, settingLibraryView, body.LibraryView, libraryViews); err != nil {
		return err
	}
	if err := enumSetting(values, settingLibrarySort, body.LibrarySort, librarySorts); err != nil {
		return err
	}
	if err := enumSetting(values, settingLibraryOrder, body.LibraryOrder, libraryOrders); err != nil {
		return err
	}
	if body.Prefetch != nil {
		if *body.Prefetch < prefetchMin || *body.Prefetch > prefetchMax {
			return badRequest("prefetch must be between %d and %d", prefetchMin, prefetchMax).
				withDetail("field", settingPrefetch, "value", *body.Prefetch)
		}
		values[settingPrefetch] = strconv.Itoa(*body.Prefetch)
	}
	if body.LibraryScope != nil {
		v := *body.LibraryScope
		if err := validateScope(v); err != nil {
			return err
		}
		values[settingLibraryScope] = v
	}

	if len(values) > 0 {
		// One transaction: PUT is partial, and a half-applied settings change
		// would be visible to a concurrent GET.
		if err := s.user.Settings().PutAll(r.Context(), values); err != nil {
			return internalErr(err)
		}
	}

	settings, err := s.settings(r)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, settings)
	return nil
}

// enumSetting validates one closed-set field and stages it for the write.
func enumSetting(dst map[string]string, key string, value *string, allowed []string) error {
	if value == nil {
		return nil
	}
	if !contains(allowed, *value) {
		return badRequest("%s must be one of %s", key, strings.Join(allowed, ", ")).
			withDetail("field", key, "value", *value)
	}
	dst[key] = *value
	return nil
}

// validateScope checks `library_scope` (amendment A-5).
//
// It cannot be a closed enum — the value may be a root name — so the rule is
// the weakest one that still keeps the field a scope: non-empty, bounded, and
// free of control characters, which would otherwise end up in a log line or an
// HTML attribute.
func validateScope(v string) error {
	if v == "" {
		return badRequest("library_scope must not be empty").withDetail("field", settingLibraryScope)
	}
	if len(v) > maxLibraryScopeLen {
		return badRequest("library_scope must be at most %d bytes", maxLibraryScopeLen).
			withDetail("field", settingLibraryScope)
	}
	for _, ru := range v {
		if unicode.IsControl(ru) {
			return badRequest("library_scope must not contain control characters").
				withDetail("field", settingLibraryScope)
		}
	}
	return nil
}

// settings assembles the response: the stored user values over the YAML
// defaults, plus the read-only server mirror.
func (s *Server) settings(r *http.Request) (Settings, error) {
	stored := map[string]string{}
	if s.user != nil {
		all, err := s.user.Settings().All(r.Context())
		if err != nil {
			return Settings{}, internalErr(err)
		}
		stored = all
	}

	prefs := s.defaultPrefsFromSettings(stored)
	out := Settings{
		ReadingDirection: prefs.ReadingDirection,
		DisplayMode:      prefs.DisplayMode,
		FitMode:          prefs.FitMode,
		Prefetch:         s.cfg.Reader.Prefetch,
		Theme:            s.cfg.Reader.Theme,
		LibraryView:      defaultLibraryView,
		LibrarySort:      defaultLibrarySort,
		LibraryOrder:     defaultLibraryOrder,
		LibraryScope:     defaultLibraryScope,
		Server:           s.serverSettings(r),
	}
	// A stored value that no longer passes validation — written by an older
	// build, or edited in the database by hand — is ignored rather than
	// returned. The client's types say `theme: "light" | "dark" | "system"`, and
	// handing it something else would break a switch statement somewhere in the
	// UI for a value the user never sees.
	if v, ok := stored[settingTheme]; ok && contains(themes, v) {
		out.Theme = v
	}
	if v, ok := stored[settingLibraryView]; ok && contains(libraryViews, v) {
		out.LibraryView = v
	}
	if v, ok := stored[settingLibrarySort]; ok && contains(librarySorts, v) {
		out.LibrarySort = v
	}
	if v, ok := stored[settingLibraryOrder]; ok && contains(libraryOrders, v) {
		out.LibraryOrder = v
	}
	if v, ok := stored[settingLibraryScope]; ok && validateScope(v) == nil {
		out.LibraryScope = v
	}
	if v, ok := stored[settingPrefetch]; ok {
		if n, err := strconv.Atoi(v); err == nil && n >= prefetchMin && n <= prefetchMax {
			out.Prefetch = n
		}
	}
	return out, nil
}

// serverSettings is the read-only mirror of the YAML the settings screen shows.
func (s *Server) serverSettings(r *http.Request) ServerSettings {
	widths := s.cfg.Thumbnails.Widths
	if s.thumbs != nil {
		widths = s.thumbs.Widths()
	}
	// A nil slice would marshal as `null`; the contract types it `number[]`.
	if widths == nil {
		widths = []int{}
	}
	return ServerSettings{
		ThumbnailWidths:   widths,
		ScanWorkers:       s.cfg.Scan.Workers,
		ThumbWorkers:      s.cfg.Thumbnails.Workers,
		PDFEnabled:        s.pdfEnabled(),
		AVIFEnabled:       s.avifEnabled(),
		AuthEnabled:       s.auth.Enabled(),
		BasePath:          s.base,
		Version:           buildinfo.Version,
		RecentlyAddedDays: s.cfg.Library.RecentlyAddedDays,
		// AbsFilePath, not FilePath: the lookup order's second entry is
		// `./shelf.yaml`, and "edit shelf.yaml" is not an answer to "which of the
		// four candidates am I running?" (amendment A-10, ruling E-25).
		ConfigPath:          s.cfg.AbsFilePath(),
		RootEditingEnabled:  s.rootEditingEnabled(),
		ConfigChangedOnDisk: s.configChangedOnDisk(r),
	}
}

// configChangedOnDisk answers `Settings.server.config_changed_on_disk` (§7.8,
// amendment A-11) by re-hashing the file on every read of this endpoint.
//
// The three answers are all deliberate. No configuration file, or no baseline
// digest, is **false**: there is nothing to differ from, and a permanent "the
// file changed" banner is a banner users learn to ignore. A file that has gone
// missing or unreadable since load is **true**: it differs, and telling the
// operator so is more useful than pretending nothing happened. Otherwise it is
// a byte comparison, which is why it flips on a comment edit as readily as on a
// new root — the UI must therefore say "the configuration file changed —
// restart to apply it", never "you must restart".
// Since amendment A-12 the baseline it compares against is not fixed: a hot add
// moves it forward, because after one this process and the file agree again and
// a notice telling the user to restart would be false.
func (s *Server) configChangedOnDisk(r *http.Request) bool {
	path := s.cfg.AbsFilePath()
	s.adoptMu.RLock()
	baseline := s.configDigest
	s.adoptMu.RUnlock()
	if path == "" || baseline == "" {
		return false
	}
	state, err := config.ReadFileState(path)
	if err != nil {
		s.log.DebugContext(r.Context(), "the configuration file could not be re-read", "err", err)
		return true
	}
	return state.Digest != baseline
}

// configFileStateOrZero reads the configuration file for the callers that want
// what it says and can live without it: `GET /api/roots` renders R2's pending
// rows from it, and a file that is missing or mid-edit simply contributes none.
//
// It is the same call `configChangedOnDisk` makes, on purpose. The digest and
// the roots list are two answers about one file, and reading it through two code
// paths is how they come to disagree about which version they saw.
func (s *Server) configFileStateOrZero(r *http.Request) (config.FileState, bool) {
	path := s.cfg.AbsFilePath()
	if path == "" {
		return config.FileState{}, false
	}
	state, err := config.ReadFileState(path)
	if err != nil {
		s.log.DebugContext(r.Context(), "the configuration file could not be read for pending roots", "err", err)
		return config.FileState{}, false
	}
	return state, true
}
