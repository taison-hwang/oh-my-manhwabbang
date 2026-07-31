package httpapi

import (
	"encoding/json"

	"shelf/internal/userdata"
)

// The wire types of arch §7.3–§7.12, with the exact field names and
// nullability the contract states. They are duplicated from the storage types
// on purpose: `index.SeriesRow` is free to gain a column without that column
// appearing on the wire, and a JSON tag here is a promise to the frontend that
// only an amendment may break.
//
// Nullability convention, and it is load-bearing: a field the contract types as
// `T | null` is a Go pointer with **no** `omitempty`, so it marshals as an
// explicit `null` rather than disappearing. `web/src/api/types.ts` types those
// fields `T | null`, never `T | undefined` (impl-plan §3 WP-06 acceptance 1);
// an omitted key would deserialise as `undefined` and break that.

// --- §7.2 error envelope ---------------------------------------------------

// ErrorResponse is the body of every non-2xx response, including from the image
// endpoints (arch §7.2).
type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}

// ErrorBody carries the frozen code, a human-readable English message safe to
// display, and optional machine-readable extras (`{cv}` on `stale_version`,
// `{reason}` on `thumb_unavailable`, `{param}` on a rejected query parameter).
type ErrorBody struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Detail  map[string]any `json:"detail,omitempty"`
}

// --- §7.4 roots and health -------------------------------------------------

// Health is `GET /api/health`. It never requires authentication.
type Health struct {
	OK          bool           `json:"ok"`
	Version     string         `json:"version"`
	Commit      string         `json:"commit"`
	StartedAt   int64          `json:"started_at"`
	UptimeMs    int64          `json:"uptime_ms"`
	PDFEnabled  bool           `json:"pdf_enabled"`
	AVIFEnabled bool           `json:"avif_enabled"`
	Verbose     *HealthVerbose `json:"verbose,omitempty"`
}

// HealthVerbose is the `?verbose=1` payload: the pool counters arch §5.2 and
// §9 say stand in for a metrics endpoint in v1. It is `omitempty` because it is
// diagnostics, not contract — the plain health body must not change shape.
type HealthVerbose struct {
	Goroutines   int          `json:"goroutines"`
	HeapAllocKB  uint64       `json:"heap_alloc_kb"`
	SysKB        uint64       `json:"sys_kb"`
	ArchivePool  *PoolCounter `json:"archive_pool"`
	ThumbCounter ThumbCounter `json:"thumbs"`
}

// PoolCounter mirrors openpool.Stats.
type PoolCounter struct {
	Hits      int64 `json:"hits"`
	Misses    int64 `json:"misses"`
	Evictions int64 `json:"evictions"`
	Stale     int64 `json:"stale"`
	Size      int   `json:"size"`
	Open      int   `json:"open"`
}

// ThumbCounter mirrors the subset of thumbs.Stats an operator needs.
//
// `cover_depth`, `page_depth`, `active` and `inflight` are one snapshot, taken
// under a single lock inside thumbs.Stats, and together they are the exact
// conjunction thumbs.Service.idle() tests: all four zero means every thumbnail
// the service was ever handed has been renamed into place under the cache
// directory. The last two exist for that reason alone — the queue depths on
// their own count a decode that is still writing as finished, which is the same
// inaccuracy internal/app/covers.go documents for the scan's `covers` phase.
//
// One honesty note against the "diagnostics, not contract" line on
// HealthVerbose above: that remains true of the block as a whole, but the E2E
// gate in scripts/e2e-assert.py waits on these four before it judges the covers
// the scan pre-generated, so renaming or dropping one of them breaks a gate
// rather than a dashboard.
type ThumbCounter struct {
	Hits       int64 `json:"hits"`
	Queued     int64 `json:"queued"`
	Dropped    int64 `json:"dropped"`
	Generated  int64 `json:"generated"`
	Failed     int64 `json:"failed"`
	CoverDepth int   `json:"cover_depth"`
	PageDepth  int   `json:"page_depth"`
	Active     int   `json:"active"`
	Inflight   int   `json:"inflight"`
}

// Root is one configured root as of the last scan (arch §7.3).
type Root struct {
	Name          string  `json:"name"`
	Label         string  `json:"label"`
	Path          string  `json:"path"`
	Enabled       bool    `json:"enabled"`
	SeriesCount   int64   `json:"series_count"`
	BookCount     int64   `json:"book_count"`
	PageCount     int64   `json:"page_count"`
	TotalBytes    int64   `json:"total_bytes"`
	Available     bool    `json:"available"`
	LastScanStart *int64  `json:"last_scan_start"`
	LastScanEnd   *int64  `json:"last_scan_end"`
	LastScanError *string `json:"last_scan_error"`
	// Pending is true for a root that is in the configuration FILE ON DISK and
	// has no index row: `POST /api/roots` wrote it, and roots are opened exactly
	// once at startup, so this server cannot serve it until it restarts.
	// AMENDMENT A-11 / revision R2 (ruling E-26).
	//
	// It exists because the alternatives are both worse. Leaving the row out
	// until the restart — which is what §7.4 originally said — makes a
	// successful 201 look like nothing happened, and the design the
	// requirement's owner chose shows the root appearing at once. Leaving the
	// flag out and listing it like any other root would have it claim counts and
	// availability it does not have. A row that says "not loaded yet" is the
	// only honest third answer, and the UI keys its 재시작 label and its missing
	// 재스캔 button off this one boolean.
	//
	// A pending row carries zero counts, null scan timestamps and
	// `available: false`. It is not a hot-add: nothing is opened and nothing is
	// scanned.
	Pending bool `json:"pending"`
}

// RootsResponse is `GET /api/roots`.
type RootsResponse struct {
	Items []Root `json:"items"`
}

// RootCreate is the body of `POST /api/roots` (amendment A-11, arch §7.4).
type RootCreate struct {
	// Path is REQUIRED: absolute, and a readable directory on the server's host.
	Path string `json:"path"`
	// Label is the optional display name; it defaults to the base name of Path.
	Label string `json:"label"`
}

// RootEntry is the `201` of `POST /api/roots`.
//
// It is deliberately NOT Root. The created root has no index row and no open
// handle, so `available`, the four counts and the two scan timestamps would all
// have to be invented. What the endpoint creates is a *configuration entry*, and
// that is what it returns.
type RootEntry struct {
	// Name is SERVER-GENERATED (arch §7.4). It is the stable identity every
	// series_id and book_id hashes, so a client that picked it could silently
	// reattach a new directory to another root's reading progress.
	Name string `json:"name"`
	// Path is absolute and cleaned, exactly as written to the file.
	Path string `json:"path"`
	// Label is as written — the base name of Path when the request omitted it.
	Label string `json:"label"`
	// Enabled is always true: the key is not written, so §3.2's default applies.
	Enabled bool `json:"enabled"`
}

// --- §7.3 shared types -----------------------------------------------------

// Progress is one book's reading position (FR-STT-001).
type Progress struct {
	BookID    string `json:"book_id"`
	SeriesID  string `json:"series_id"`
	LastPage  int    `json:"last_page"`
	PageCount int    `json:"page_count"`
	Completed bool   `json:"completed"`
	StartedAt int64  `json:"started_at"`
	UpdatedAt int64  `json:"updated_at"`
	// Stale is true when the recorded page_count no longer matches the index:
	// the file changed under the reader, and the UI may say so.
	Stale bool `json:"stale"`
}

// SeriesProgress is the FR-STT-002 rollup.
type SeriesProgress struct {
	BooksTotal     int64 `json:"books_total"`
	BooksCompleted int64 `json:"books_completed"`
	BooksStarted   int64 `json:"books_started"`
	// Percent is books_completed/books_total*100 rounded to 1 dp, and is
	// *exactly* 0 when books_total is 0 — never NaN, never null (arch §7.3).
	Percent    float64 `json:"percent"`
	LastReadAt *int64  `json:"last_read_at"`
	LastBookID *string `json:"last_book_id"`
	LastPage   *int    `json:"last_page"`
}

// SeriesSummary is one row of the library list.
type SeriesSummary struct {
	ID         string `json:"id"`
	RootName   string `json:"root_name"`
	Name       string `json:"name"`
	Path       string `json:"path"`
	Kind       string `json:"kind"`
	BookCount  int64  `json:"book_count"`
	PageCount  int64  `json:"page_count"`
	TotalBytes int64  `json:"total_bytes"`
	Mtime      int64  `json:"mtime"`
	// AddedAt is COALESCE(user.db first_seen_at, index added_at) — amendment
	// A-8. Same field name and type as before; the source is what changed, so
	// that it survives --rebuild-index.
	AddedAt int64   `json:"added_at"`
	Status  string  `json:"status"`
	Error   *string `json:"error"`
	// HasCover false means render the FR-LIB-008 text placeholder.
	HasCover bool `json:"has_cover"`
	// CoverCV is appended to the cover URL as ?v=. It is null for a series
	// whose cover is a loose file, which has no book content_version to carry.
	CoverCV  *string        `json:"cover_cv"`
	Progress SeriesProgress `json:"progress"`
}

// SeriesDetail is `GET /api/series/{sid}`: a summary plus its books.
type SeriesDetail struct {
	SeriesSummary
	Books []BookSummary `json:"books"`
	// Encoding is diagnostics: "utf-8" | "cp949" | "mixed" | null.
	Encoding *string `json:"encoding"`
}

// BookSummary is one 권.
type BookSummary struct {
	ID       string `json:"id"`
	SeriesID string `json:"series_id"`
	Name     string `json:"name"`
	Path     string `json:"path"`
	Kind     string `json:"kind"`
	// Ord is the 0-based position within the series, materialised by the
	// scanner from the natural sort so nothing above re-sorts.
	Ord        int   `json:"ord"`
	PageCount  int64 `json:"page_count"`
	TotalBytes int64 `json:"total_bytes"`
	// FileSize is 0 for kind:"dir".
	FileSize int64 `json:"file_size"`
	Mtime    int64 `json:"mtime"`
	// CV is the content version. Append it as ?v= to page and thumb URLs.
	CV       string    `json:"cv"`
	Status   string    `json:"status"`
	Error    *string   `json:"error"`
	Progress *Progress `json:"progress"`
}

// BookDetail is `GET /api/books/{bid}`, which ships every PageInfo in one
// response (D-15) — 1 071 pages is ~110 KB and it is what makes an arbitrary
// page jump need no round trip (AC-008).
type BookDetail struct {
	BookSummary
	SeriesName string     `json:"series_name"`
	RootName   string     `json:"root_name"`
	Pages      []PageInfo `json:"pages"`
	DimsState  string     `json:"dims_state"`
	PrevBookID *string    `json:"prev_book_id"`
	NextBookID *string    `json:"next_book_id"`
	Prefs      BookPrefs  `json:"prefs"`
}

// PageInfo is one page's metadata. w and h stay null until a decode has filled
// them in (arch §5.8); the viewer treats an unknown page as single-page in
// spread mode until its natural size is known.
type PageInfo struct {
	N    int    `json:"n"`
	Name string `json:"name"`
	Ext  string `json:"ext"`
	Size int64  `json:"size"`
	W    *int   `json:"w"`
	H    *int   `json:"h"`
}

// BookPrefs is the effective viewer configuration for a book: the per-book
// override where one exists, the global default otherwise.
type BookPrefs struct {
	ReadingDirection string `json:"reading_direction"`
	DisplayMode      string `json:"display_mode"`
	FitMode          string `json:"fit_mode"`
	// IsOverride false means these are the global defaults verbatim.
	IsOverride bool `json:"is_override"`
}

// --- §7.5 series list ------------------------------------------------------

// SeriesListResponse is `GET /api/series`. Total is the number of rows matching
// the whole filter *before* offset/limit, which is what makes
// `?…&limit=1` the sidebar's count idiom (amendment A-8).
type SeriesListResponse struct {
	Items  []SeriesSummary `json:"items"`
	Total  int             `json:"total"`
	Offset int             `json:"offset"`
	Limit  int             `json:"limit"`
}

// --- §7.7 continue reading -------------------------------------------------

// ContinueItem is one card of the 이어보기 shelf (FR-LIB-010).
type ContinueItem struct {
	Book       BookSummary `json:"book"`
	SeriesID   string      `json:"series_id"`
	SeriesName string      `json:"series_name"`
	HasCover   bool        `json:"has_cover"`
	Progress   Progress    `json:"progress"`
}

// ContinueResponse is `GET /api/continue`. An empty Items is the signal to hide
// the whole shelf.
type ContinueResponse struct {
	Items []ContinueItem `json:"items"`
}

// --- §7.8 settings ---------------------------------------------------------

// Settings is `GET /api/settings`: the user-mutable block persisted in user.db,
// plus a read-only mirror of the YAML so the settings screen can show it.
type Settings struct {
	ReadingDirection string `json:"reading_direction"`
	DisplayMode      string `json:"display_mode"`
	FitMode          string `json:"fit_mode"`
	Prefetch         int    `json:"prefetch"`
	Theme            string `json:"theme"`
	LibraryView      string `json:"library_view"`
	LibrarySort      string `json:"library_sort"`
	LibraryOrder     string `json:"library_order"`
	// LibraryScope is the sticky sidebar scope (amendment A-5): "all",
	// "reading", "added", "done", or a root name.
	LibraryScope string         `json:"library_scope"`
	Server       ServerSettings `json:"server"`
}

// ServerSettings is read-only. `PUT /api/settings` with any key under it is
// `400 bad_request` — it is a mirror of the YAML, and the YAML is the source of
// truth (C-5, ruling E-3).
type ServerSettings struct {
	ThumbnailWidths []int  `json:"thumbnail_widths"`
	ScanWorkers     int    `json:"scan_workers"`
	ThumbWorkers    int    `json:"thumb_workers"`
	PDFEnabled      bool   `json:"pdf_enabled"`
	AVIFEnabled     bool   `json:"avif_enabled"`
	AuthEnabled     bool   `json:"auth_enabled"`
	BasePath        string `json:"base_path"`
	Version         string `json:"version"`
	// RecentlyAddedDays is `library.recently_added_days` (amendment A-8). It
	// exists so the 최근 추가 empty state can say "최근 14일" without the client
	// hard-coding 14.
	RecentlyAddedDays int `json:"recently_added_days"`
	// ConfigPath is the absolute path of the loaded `shelf.yaml` (amendment
	// A-10, ruling E-25). The settings screen and the onboarding screen both
	// tell the user to edit the file (C-5, ruling E-3, arch OQ-3); this is the
	// field that lets them say *which* file, out of the four-entry lookup order
	// in `cmd/shelf/flags.go`. Read-only like the rest of this block.
	ConfigPath string `json:"config_path"`
	// RootEditingEnabled is the CAPABILITY behind §7.4's two endpoints, not the
	// key: true iff `server.allow_root_editing` is on AND this server has a
	// configuration file AND that file is not inside a configured root.
	// AMENDMENT A-11 (ruling E-26).
	//
	// One boolean for three conditions, exactly as PDFEnabled folds the `nopdf`
	// build tag together with `pdf.enabled`: a client that had to AND three
	// fields would get it wrong in one of the three, and it is the client that
	// decides whether the 추가/제거 controls are rendered at all.
	RootEditingEnabled bool `json:"root_editing_enabled"`
	// ConfigChangedOnDisk reports that the file at ConfigPath is no longer
	// byte-identical to the one this process loaded. AMENDMENT A-11.
	//
	// It is NOT "a write happened". It is equally true when the user hand-edited
	// the file, which is the workflow C-5 has been telling them to use all along
	// — and because it is the server's state rather than the tab's, it survives
	// a browser reload. It flips on a comment edit too, so the UI must say "the
	// configuration file changed — restart to apply it", never "you must
	// restart".
	//
	// A deleted file reads true (it differs); an absent ConfigPath reads false
	// (there is nothing to differ from).
	ConfigChangedOnDisk bool `json:"config_changed_on_disk"`
}

// --- §7.9 cache ------------------------------------------------------------

// CacheUsage is `GET /api/cache/usage`. The walk behind it is cached for 60 s.
type CacheUsage struct {
	ComputedAt int64            `json:"computed_at"`
	Entries    []CacheUsageItem `json:"entries"`
	TotalBytes int64            `json:"total_bytes"`
	CacheDir   string           `json:"cache_dir"`
}

// CacheUsageItem is one kind's footprint.
type CacheUsageItem struct {
	Kind  string `json:"kind"`
	Files int64  `json:"files"`
	Bytes int64  `json:"bytes"`
}

// PurgeResult is `DELETE /api/cache`.
type PurgeResult struct {
	DeletedFiles int64 `json:"deleted_files"`
	FreedBytes   int64 `json:"freed_bytes"`
}

// --- §7.10 scan ------------------------------------------------------------

// RunAccepted is the `202` of `POST /api/scan` and `POST /api/series/{sid}/rescan`.
type RunAccepted struct {
	RunID string `json:"run_id"`
}

// ScanStatus is the 1 s polling payload (C-11: polling, not SSE).
type ScanStatus struct {
	State       string   `json:"state"`
	RunID       *string  `json:"run_id"`
	Full        bool     `json:"full"`
	StartedAt   *int64   `json:"started_at"`
	FinishedAt  *int64   `json:"finished_at"`
	Roots       []string `json:"roots"`
	CurrentRoot *string  `json:"current_root"`
	CurrentItem *string  `json:"current_item"`
	Total       int64    `json:"total"`
	Done        int64    `json:"done"`
	Errors      int64    `json:"errors"`
	CoversTotal int64    `json:"covers_total"`
	CoversDone  int64    `json:"covers_done"`
	ElapsedMs   int64    `json:"elapsed_ms"`
	ETAMs       *int64   `json:"eta_ms"`
	LastError   *string  `json:"last_error"`
}

// ScanLogEntry is one persisted scan diagnostic (UI-004's 스캔 로그 panel).
type ScanLogEntry struct {
	ID       int64   `json:"id"`
	TS       int64   `json:"ts"`
	RunID    string  `json:"run_id"`
	Level    string  `json:"level"`
	RootName *string `json:"root_name"`
	RelPath  *string `json:"rel_path"`
	Message  string  `json:"message"`
}

// ScanLogResponse is `GET /api/scan/log`.
type ScanLogResponse struct {
	Items []ScanLogEntry `json:"items"`
}

// --- §7.12 auth ------------------------------------------------------------

// AuthStatus is `GET /api/auth/status`. It never returns 401 — it is how the
// SPA learns whether to render the login screen at all.
type AuthStatus struct {
	AuthRequired  bool `json:"auth_required"`
	Authenticated bool `json:"authenticated"`
}

// --- request bodies --------------------------------------------------------
//
// Every one of these is decoded with DisallowUnknownFields, so a typo is a
// `400 bad_request` naming the field rather than a silently ignored setting
// (arch §7.1).

// progressUpdateBody is `PUT /api/books/{bid}/progress`.
//
// Page is a pointer so that an absent `page` is distinguishable from `0`: the
// contract has no page 0, and "you forgot the field" and "you sent an illegal
// value" are different messages.
type progressUpdateBody struct {
	Page      *int  `json:"page"`
	Completed *bool `json:"completed"`
}

// bookPrefsUpdateBody is `PUT /api/books/{bid}/prefs`.
//
// Each field is a json.RawMessage rather than a *string because the contract
// distinguishes three states and a *string only carries two: absent leaves the
// stored override alone, explicit `null` clears it, and a value sets it.
// Collapsing null into absent would make it impossible to go back to the
// global default once an override existed.
type bookPrefsUpdateBody struct {
	ReadingDirection json.RawMessage `json:"reading_direction"`
	DisplayMode      json.RawMessage `json:"display_mode"`
	FitMode          json.RawMessage `json:"fit_mode"`
}

// settingsUpdateBody is `PUT /api/settings`: partial, and deliberately without
// a `server` field so that sending one is an unknown-field 400 (arch §7.8).
type settingsUpdateBody struct {
	ReadingDirection *string `json:"reading_direction"`
	DisplayMode      *string `json:"display_mode"`
	FitMode          *string `json:"fit_mode"`
	Prefetch         *int    `json:"prefetch"`
	Theme            *string `json:"theme"`
	LibraryView      *string `json:"library_view"`
	LibrarySort      *string `json:"library_sort"`
	LibraryOrder     *string `json:"library_order"`
	LibraryScope     *string `json:"library_scope"`
}

// scanRequestBody is `POST /api/scan`.
type scanRequestBody struct {
	Roots []string `json:"roots"`
	Full  bool     `json:"full"`
}

// loginBody is `POST /api/auth/login`.
type loginBody struct {
	Password string `json:"password"`
}

// progressExport is the FR-STT-004 document. The storage type already carries
// the frozen json tags of arch §7.11, so it is marshalled directly rather than
// copied field by field into a twin that could drift.
type progressExport = userdata.Export
