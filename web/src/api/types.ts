/**
 * The frozen HTTP contract, as TypeScript.
 *
 * Normative source: `docs/arch-backend.md` §7.1–§7.13, amended by `docs/impl-plan.md`
 * §0.3 (A-1 … A-7) and the enum resolutions C-1 … C-4. Nothing here may be invented:
 * every field name, every optional marker and every enum member is copied from those
 * documents. WP-12 implements the same document server-side and WP-13 diffs its golden
 * JSON against this file.
 *
 * Rules that bite (impl-plan §4), encoded below:
 *  1. Every page number is **1-based**; `n ∈ [1, page_count]`. There is no page 0.
 *  2. Page/thumb/cover URLs carry `?v={cv}` — see `urls.ts`.
 *  3. `202` is a normal response for covers and thumbs — see `client.ts`.
 *  4. A book with `status !== "ok"` is `200` with `pages: []` and a populated `error`.
 *  5. Unknown JSON body fields are rejected with `400`; unknown query params are ignored.
 *
 * Wire fields keep their `snake_case` names (impl-plan §5.3). A field the server sends
 * as `null` is typed `T | null` and never `T | undefined` (WP-06 acceptance 1); `?` is
 * used only where §7 itself writes `?`, i.e. in request bodies and the error `detail`.
 */

// ---------------------------------------------------------------------------
// §7.3 Shared scalars and enums
// ---------------------------------------------------------------------------

/** `/^[a-z2-7]{16}$/` — an opaque, path-derived id (arch §3.4). */
export type ID = string
/** Integer Unix seconds, UTC (arch §7.1). */
export type Unix = number

/** Matches every id the API accepts; a syntactically invalid id is `400`, unknown is `404`. */
export const ID_PATTERN = /^[a-z2-7]{16}$/

/**
 * The `nested*` spellings are volumes inside a container archive — one of the
 * 39 books in `겟 벡커스 1~39완.zip` rather than an archive of their own. They
 * read exactly like their un-nested twin and wear the same badge; the
 * distinction is where the bytes live, which is the server's problem.
 *
 * `rar`/`nestedrar` arrived with D-71 and were missing here until they were
 * measured on the wire: 14 `rar` and 8 `nestedrar` books in the collection,
 * against a client that had never heard of either. `contractcheck`'s enum rule
 * could not catch it, because it can only judge a string some golden file
 * happens to contain and every golden book is a `zip` or a `dir`. It now
 * compares this list against the `Kind*` constants themselves.
 */
export const BOOK_KINDS = ['zip', 'nestedzip', 'rar', 'nestedrar', 'dir', 'pdf'] as const
export type BookKind = (typeof BOOK_KINDS)[number]

/** C-4: books say `dir`, series say `folder`. The badge text is FOLDER for both. */
export const SERIES_KINDS = ['folder', 'zip', 'pdf'] as const
export type SeriesKind = (typeof SERIES_KINDS)[number]

export const ITEM_STATUSES = ['ok', 'empty', 'error', 'encrypted', 'unsupported'] as const
export type ItemStatus = (typeof ITEM_STATUSES)[number]

/**
 * A **series**' status — ruling E-14. Narrower than `ItemStatus`: `encrypted` and
 * `unsupported` are verdicts on one container and are book-only.
 *
 * The fold: `empty` when the series holds no books at all, `ok` when at least one book
 * is readable, `error` when it holds books but none of them are. A series whose every
 * volume is encrypted must not present as healthy (FR-IDX-010, design.md 화면 2).
 */
export const SERIES_STATUSES = ['ok', 'empty', 'error'] as const
export type SeriesStatus = (typeof SERIES_STATUSES)[number]

export const READING_DIRS = ['ltr', 'rtl'] as const
export type ReadingDir = (typeof READING_DIRS)[number]

/** C-1: the wire value is `spread`, never `double`. The Korean label 양면 is unaffected. */
export const DISPLAY_MODES = ['single', 'spread', 'vertical'] as const
export type DisplayMode = (typeof DISPLAY_MODES)[number]

/** C-2: the wire value is `contain`, never `screen`. The Korean label 화면 is unaffected. */
export const FIT_MODES = ['width', 'height', 'original', 'contain'] as const
export type FitMode = (typeof FIT_MODES)[number]

/** C-3: API sort keys win over the ui-spec's. */
export const SORT_KEYS = ['name', 'mtime', 'recent', 'size', 'books', 'added'] as const
export type SortKey = (typeof SORT_KEYS)[number]

export const SORT_ORDERS = ['asc', 'desc'] as const
export type SortOrder = (typeof SORT_ORDERS)[number]

/** `GET /api/series?status=` (§7.5) — note `all`, which `ItemStatus` does not have. */
export const SERIES_STATUS_FILTERS = ['ok', 'empty', 'error', 'all'] as const
export type SeriesStatusFilter = (typeof SERIES_STATUS_FILTERS)[number]

/** Amendment A-4: `GET /api/series?progress=`. */
export const PROGRESS_FILTERS = ['any', 'reading', 'done', 'unread'] as const
export type ProgressFilter = (typeof PROGRESS_FILTERS)[number]

/**
 * Amendment A-8 (ruling E-9): `GET /api/series?scope=`. A *filter*, AND-ed with
 * `root`/`q`/`status`/`progress`; it does not change the `sort`/`order` defaults.
 * `added` means `first_seen_at >= now − recently_added_days × 86400`. Exactly these
 * two values are legal — `reading`, `done` and a root name are `400 bad_request`
 * with `detail.param = "scope"`, because the sidebar's 5 entries fan out into 3
 * different wire parameters and must never be conflated (arch §7.5).
 */
export const SERIES_SCOPES = ['all', 'added'] as const
export type SeriesScope = (typeof SERIES_SCOPES)[number]

export const DIMS_STATES = ['none', 'partial', 'done'] as const
export type DimsState = (typeof DIMS_STATES)[number]

export const SCAN_STATES = ['idle', 'walking', 'indexing', 'covers', 'cancelling'] as const
export type ScanState = (typeof SCAN_STATES)[number]

export const LOG_LEVELS = ['info', 'warn', 'error'] as const
export type LogLevel = (typeof LOG_LEVELS)[number]

export const CACHE_KINDS = ['thumbs', 'pdf', 'wazero'] as const
export type CacheKind = (typeof CACHE_KINDS)[number]

/** `DELETE /api/cache?kind=` also accepts `all` (§7.9). */
export const PURGE_KINDS = ['thumbs', 'pdf', 'wazero', 'all'] as const
export type PurgeKind = (typeof PURGE_KINDS)[number]

export const THEMES = ['light', 'dark', 'system'] as const
export type Theme = (typeof THEMES)[number]

export const LIBRARY_VIEWS = ['grid', 'list'] as const
export type LibraryView = (typeof LIBRARY_VIEWS)[number]

// ---------------------------------------------------------------------------
// §7.2 Error envelope
// ---------------------------------------------------------------------------

export const ERROR_CODES = [
  'bad_request', // 400  malformed input
  'unauthorized', // 401  auth enabled and the session is missing/expired
  'forbidden', // 403  understood, well-formed, and refused by configuration — AMENDMENT A-11
  'not_found', // 404  unknown id, or page out of range
  'conflict', // 409  e.g. a scan is already running
  'stale_version', // 409  ?v= does not match the book's current cv
  'unprocessable', // 422  understood but cannot be produced
  'thumb_unavailable', // 422  the source cannot be decoded server-side (arch §5.5)
  'rate_limited', // 429  too many login attempts (arch §8.2) — AMENDMENT A-9
  'unsupported', // 501  feature absent from this build (e.g. nopdf)
  'unavailable', // 503  media volume unreachable / shutting down
  'internal', // 500
] as const
export type ErrorCode = (typeof ERROR_CODES)[number]

export interface ErrorBody {
  code: ErrorCode
  /** English, human-readable, safe to display. */
  message: string
  /** Machine-readable extras, e.g. `{cv}` on `stale_version`, `{reason}` on `thumb_unavailable`. */
  detail?: Record<string, unknown>
}

/** Every non-2xx response, including from image endpoints, has this body. */
export interface ErrorResponse {
  error: ErrorBody
}

// ---------------------------------------------------------------------------
// §7.3 Shared types
// ---------------------------------------------------------------------------

export interface Root {
  /** Stable identity from the config; renaming it orphans progress (arch §3.4). */
  name: string
  /** Display name; equals `name` when no label is set. */
  label: string
  /** Absolute path — shown only on the settings/detail screen (UI-5.3). */
  path: string
  enabled: boolean
  series_count: number
  book_count: number
  page_count: number
  total_bytes: number
  /** `false` when the path is currently unreachable. */
  available: boolean
  last_scan_start: Unix | null
  last_scan_end: Unix | null
  last_scan_error: string | null
  /**
   * AMENDMENT A-11 / revision R2 (ruling E-26). `true` for a root that is in the
   * configuration file on disk and has no index row: `POST /api/roots` wrote it,
   * and roots are opened once at startup, so the server cannot serve it until it
   * restarts. Such a row carries zero counts, null scan timestamps and
   * `available: false`, and must offer no 재스캔.
   */
  pending: boolean
}

export interface Progress {
  book_id: ID
  series_id: ID
  /** 1-based. */
  last_page: number
  /** As recorded when progress was written. */
  page_count: number
  completed: boolean
  started_at: Unix
  updated_at: Unix
  /** `true` when `page_count` no longer matches the index — the UI warns that the file changed. */
  stale: boolean
}

/** FR-STT-002, aggregated over the series' books. */
export interface SeriesProgress {
  books_total: number
  books_completed: number
  /** Started but not completed. */
  books_started: number
  /** 0..100, rounded to 1 dp: `books_completed / books_total * 100`. */
  percent: number
  last_read_at: Unix | null
  /** The book "이어 읽기" should open. */
  last_book_id: ID | null
  last_page: number | null
}

export interface SeriesSummary {
  id: ID
  root_name: string
  /** Display name. */
  name: string
  /** Root-relative slash path (supporting text only). */
  path: string
  kind: SeriesKind
  book_count: number
  page_count: number
  total_bytes: number
  mtime: Unix
  added_at: Unix
  /** Ruling E-14 — the three-value fold, never a book-only verdict. */
  status: SeriesStatus
  error: string | null
  /** `false` → render the FR-LIB-008 text placeholder. */
  has_cover: boolean
  /** Append as `?v=` to the cover URL. */
  cover_cv: string | null
  progress: SeriesProgress
}

export interface BookSummary {
  id: ID
  series_id: ID
  name: string
  /** Root-relative slash path. */
  path: string
  /** Drives the ZIP/폴더/PDF badge (FR-LIB-009). */
  kind: BookKind
  /** 0-based position in the series. */
  ord: number
  page_count: number
  total_bytes: number
  /** 0 for `kind: "dir"`. */
  file_size: number
  mtime: Unix
  /** Content version — append as `?v=` to page/thumb URLs (arch §5.3). */
  cv: string
  status: ItemStatus
  error: string | null
  progress: Progress | null
}

export interface PageInfo {
  /** 1-based. */
  n: number
  /** Decoded entry/file name, for the thumbnail panel. */
  name: string
  /** e.g. `".jpg"`. */
  ext: string
  /** Uncompressed bytes. */
  size: number
  /** `null` until known (arch §5.8). */
  w: number | null
  h: number | null
}

// ---------------------------------------------------------------------------
// §7.4 Roots and health
// ---------------------------------------------------------------------------

export interface Health {
  ok: boolean
  version: string
  commit: string
  started_at: Unix
  uptime_ms: number
  pdf_enabled: boolean
  avif_enabled: boolean
}

export interface RootsResponse {
  items: Root[]
}

/**
 * Body of `POST /api/roots` — AMENDMENT A-11 (ruling E-26). Gated by
 * `Settings['server'].root_editing_enabled`; without it the request is `403 forbidden`.
 *
 * `name` is deliberately absent and is not settable: the server generates it, because it is
 * hashed into every `series_id` and `book_id` (arch §3.4), so a client that picked it could
 * silently reattach a new directory to another root's reading progress.
 */
export interface RootCreate {
  /** REQUIRED. Absolute, and a readable directory on the **server's** host. */
  path: string
  /** Optional display name; defaults to the base name of `path`, and is written either way. */
  label?: string
}

/**
 * `201 Created` from `POST /api/roots`, with `Location: {base_path}/api/roots/{name}`.
 *
 * It is **not** a `Root`: the created root has no index row and no open handle, so
 * `available`, the four counts and the two scan timestamps would all have to be invented.
 * What the endpoint creates is a *configuration entry*, and that is what it returns; the
 * root shows up in `GET /api/roots` as a `pending` row until the server restarts.
 */
export interface RootEntry {
  /** Server-generated: a slug of `label`, else of the base name of `path`, uniquified. */
  name: string
  /** Absolute and cleaned, exactly as written to the file. */
  path: string
  /** As written — the base name of `path` when the request omitted it. */
  label: string
  /** Always `true`: the key is not written, so the YAML default applies. */
  enabled: boolean
}

/**
 * One directory offered by the picker — `GET /api/browse`, AMENDMENT **A-12**
 * (ruling **E-40**).
 */
export interface BrowseEntry {
  name: string
  /**
   * Absolute and cleaned — exactly what `POST /api/roots` wants. The client
   * never assembles a filesystem path itself; it sends back what it was given.
   */
  path: string
  /**
   * `false` when `POST /api/roots` would reject this directory. The server
   * computes it from §7.4's own rules, so the picker cannot grey out a folder
   * the endpoint would have accepted, or offer one it would refuse.
   */
  selectable: boolean
  /**
   * §7.4's vocabulary — `duplicate`, `overlaps`, `contains_storage`,
   * `does_not_exist` — and `null` exactly when `selectable` is `true`.
   */
  reason: string | null
}

/**
 * One level of the picker's tree — `GET /api/browse[?path=…]` (A-12).
 *
 * The tree's roots are `server.browse_bases`; there is nothing above them, which
 * is why `path` is `''` and `parent` is `null` at the top rather than naming
 * `/`.
 */
export interface BrowseResponse {
  /** The directory listed. `''` is the synthetic top level of configured bases. */
  path: string
  /** The next level up, or `null` at a base and at the top level. */
  parent: string | null
  /**
   * `path` itself as an add candidate — the picker's "choose this folder".
   * `null` at the top level, where there is no single directory to choose.
   */
  self: BrowseEntry | null
  /** Immediate sub-directories, natural-sorted. Files and symlinks are absent. */
  entries: BrowseEntry[]
  /**
   * The listing hit the server's per-directory cap. The UI must say so rather
   * than present a partial list as complete.
   */
  truncated: boolean
}

// ---------------------------------------------------------------------------
// §7.5 Series
// ---------------------------------------------------------------------------

/**
 * Query parameters of `GET /api/series`. Unknown query params are ignored by the
 * server (§7.1), but `urls.ts` only ever emits the keys below.
 */
export interface SeriesListParams {
  /** Repeatable — FR-LIB-005. Defaults to all enabled roots. */
  root?: string[]
  /** FR-LIB-006; name substring, plus choseong when the query is jamo/ASCII. */
  q?: string
  /** Default `all`. */
  status?: SeriesStatusFilter
  /** Amendment A-4. Default `any`. */
  progress?: ProgressFilter
  /**
   * Amendment A-8. Default `all`, which is identical to omitting it. `added`
   * backs the 최근 추가 sidebar row and its count (`scope=added&limit=1` → `total`).
   */
  scope?: SeriesScope
  /** Default `name`. */
  sort?: SortKey
  /** Default `asc` for `name`, `desc` otherwise. */
  order?: SortOrder
  /** ≥ 0, default 0. */
  offset?: number
  /** 1..200, default 60. */
  limit?: number
}

export const SERIES_LIST_DEFAULT_LIMIT = 60
export const SERIES_LIST_MAX_LIMIT = 200

export interface SeriesListResponse {
  items: SeriesSummary[]
  /** Matching the filter, before `offset`/`limit`. */
  total: number
  offset: number
  limit: number
}

export interface SeriesDetail extends SeriesSummary {
  /** Natural-sorted by `ord`; the UI never re-sorts. */
  books: BookSummary[]
  /** `"utf-8" | "cp949" | "mixed" | null` — diagnostics only. */
  encoding: string | null
}

/** `POST /api/series/{sid}/rescan` and `POST /api/scan` both answer `202 {run_id}`. */
export interface ScanRunResponse {
  run_id: string
}

// ---------------------------------------------------------------------------
// §7.6 Books and pages
// ---------------------------------------------------------------------------

export interface BookPrefs {
  reading_direction: ReadingDir
  display_mode: DisplayMode
  fit_mode: FitMode
  /** `false` ⇒ these are the global defaults from `GET /api/settings`. */
  is_override: boolean
}

export interface BookDetail extends BookSummary {
  series_name: string
  root_name: string
  /** Natural-sorted, `n = 1..page_count`. Empty when `status !== "ok"` (impl-plan §4.4). */
  pages: PageInfo[]
  dims_state: DimsState
  /** FR-VWR-010 — previous book in the series. */
  prev_book_id: ID | null
  /** FR-VWR-010 — next book; `null` on the last. */
  next_book_id: ID | null
  /** Effective values (book override ?? global default). */
  prefs: BookPrefs
}

/** `PUT /api/books/{bid}/progress` (FR-VWR-009, FR-STT-001). */
export interface ProgressUpdate {
  /** 1-based, clamped server-side to `[1, page_count]`. */
  page: number
  /** Omitted ⇒ auto: `true` when `page === page_count` (FR-VWR-012). */
  completed?: boolean
  /**
   * **E-45 §2 — the reader saw `파일이 변경되었습니다` for its full lifetime.**
   *
   * The only signal that lets the server re-baseline the recorded `page_count`;
   * every other write preserves it. Sent by exactly one caller,
   * `useSaveProgress.acknowledgeStale`, and never inferred from a page turn —
   * `useProgressSync`'s automatic write goes out because the book loaded, so
   * treating it as consent would acknowledge a notice nobody read.
   *
   * `contractcheck` cannot see this: it deliberately does not inspect request
   * bodies (`scripts/contractcheck/main.go:48-53`). Known and accepted (E-45 §4).
   */
  stale_seen?: boolean
}

/** `PUT /api/books/{bid}/prefs` — every field optional; `null` clears the override. */
export interface BookPrefsUpdate {
  reading_direction?: ReadingDir | null
  display_mode?: DisplayMode | null
  fit_mode?: FitMode | null
}

// ---------------------------------------------------------------------------
// §7.7 Continue reading (FR-LIB-010)
// ---------------------------------------------------------------------------

export interface ContinueItem {
  book: BookSummary
  series_id: ID
  series_name: string
  has_cover: boolean
  /** `completed === false`, ordered by `updated_at` DESC. */
  progress: Progress
}

export interface ContinueResponse {
  /** An empty array is the signal to hide the whole 이어보기 shelf. */
  items: ContinueItem[]
}

export const CONTINUE_DEFAULT_LIMIT = 20
export const CONTINUE_MAX_LIMIT = 50

// ---------------------------------------------------------------------------
// §7.8 Settings (UI-004)
// ---------------------------------------------------------------------------

/** Read-only mirror of the YAML, so the settings screen can show it. */
export interface ServerSettings {
  thumbnail_widths: number[]
  scan_workers: number
  thumb_workers: number
  pdf_enabled: boolean
  avif_enabled: boolean
  auth_enabled: boolean
  base_path: string
  version: string
  /**
   * Amendment A-8 — `library.recently_added_days`. Read-only like the rest of this block.
   * It exists so the 최근 추가 empty state can say "최근 14일" without hard-coding 14 in the
   * client, and so the settings screen can show the window.
   */
  recently_added_days: number
  /**
   * Amendment A-10 — the **absolute** path of the loaded `shelf.yaml`. Read-only like the
   * rest of this block. The config lookup order has four entries (`$SHELF_CONFIG`,
   * `./shelf.yaml`, `$XDG_CONFIG_HOME/shelf/shelf.yaml`, `/etc/shelf/shelf.yaml`), so
   * "shelf.yaml을 편집한 뒤 재시작하세요" is only actionable next to this value (C-5,
   * ruling E-3, arch OQ-3).
   */
  config_path: string
  /**
   * Amendment A-11 — the **capability** behind `POST /api/roots` and
   * `DELETE /api/roots/{name}`, not the key: `true` iff `server.allow_root_editing` is on
   * AND this server has a configuration file AND that file is not inside a configured
   * root. One boolean for three conditions, exactly as `pdf_enabled` folds the `nopdf`
   * build tag together with `pdf.enabled`. Render the 추가/제거 controls only when it is
   * true; when it is false the settings screen is what C-5 and ruling E-3 describe.
   */
  root_editing_enabled: boolean
  /**
   * Amendment A-11 — the file at `config_path` is no longer byte-identical to the one this
   * process loaded. It is **not** "a write happened": it is equally true when the user
   * hand-edited the file (the C-5 workflow), and it survives a browser reload because it is
   * the server's state and not the tab's. It flips on a comment edit too, so the notice must
   * read "the configuration file changed — restart to apply it", never "you must restart".
   * A deleted file reads `true`; an absent `config_path` reads `false`.
   */
  config_changed_on_disk: boolean
}

/** The user-mutable half, persisted in `user.db`. `PUT` accepts only these keys. */
export interface UserSettings {
  reading_direction: ReadingDir
  display_mode: DisplayMode
  fit_mode: FitMode
  /** 0..20 — FR-VWR-006. */
  prefetch: number
  theme: Theme
  /** FR-LIB-002 — sticky across sessions. */
  library_view: LibraryView
  /**
   * Ruling E-15 — the closed FR-LIB-004 sort set, the same one `?sort=` takes.
   * It was typed `string`, which invited a silent `400`: the server has always
   * rejected anything else (`vols`, `read`, `Name`) and it is not a value the
   * library screen could honour anyway.
   */
  library_sort: SortKey
  library_order: SortOrder
  /** Amendment A-5: `"all" | "reading" | "added" | "done"` or a root name. */
  library_scope: string
}

export interface Settings extends UserSettings {
  server: ServerSettings
}

/** `PUT /api/settings` is partial; sending a `server.*` key is `400 bad_request`. */
export type SettingsUpdate = Partial<UserSettings>

// ---------------------------------------------------------------------------
// §7.9 Cache (FR-THM-008)
// ---------------------------------------------------------------------------

export interface CacheUsageEntry {
  kind: CacheKind
  files: number
  bytes: number
}

export interface CacheUsage {
  /** The walk is cached for 60 s. */
  computed_at: Unix
  entries: CacheUsageEntry[]
  total_bytes: number
  cache_dir: string
}

export interface CachePurgeResult {
  deleted_files: number
  freed_bytes: number
}

// ---------------------------------------------------------------------------
// §7.10 Scan (FR-IDX-001, FR-IDX-004)
// ---------------------------------------------------------------------------

export interface ScanStartRequest {
  roots?: string[]
  full?: boolean
}

export interface ScanStatus {
  state: ScanState
  run_id: string | null
  full: boolean
  started_at: Unix | null
  finished_at: Unix | null
  /** Roots included in this run. */
  roots: string[]
  current_root: string | null
  /** Root-relative path of the item being read. */
  current_item: string | null
  /** Books discovered so far; grows during `walking`. */
  total: number
  done: number
  errors: number
  covers_total: number
  covers_done: number
  elapsed_ms: number
  /** `null` until a rate can be estimated. */
  eta_ms: number | null
  last_error: string | null
}

export interface ScanLogEntry {
  /** Monotonic; use as `since_id` for incremental fetch. */
  id: number
  ts: Unix
  run_id: string
  level: LogLevel
  root_name: string | null
  rel_path: string | null
  message: string
}

export interface ScanLogParams {
  /** Default 200 on the wire. */
  limit?: number
  level?: LogLevel
  run_id?: string
  since_id?: number
}

export interface ScanLogResponse {
  items: ScanLogEntry[]
}

// ---------------------------------------------------------------------------
// §7.11 Progress export/import (FR-STT-004 — backend only, no UI in this build)
// ---------------------------------------------------------------------------

export interface ProgressExportItem {
  book_id: ID
  series_id: ID
  root_name: string
  /** Lets an importer re-derive ids after a rename. */
  book_path: string
  last_page: number
  page_count: number
  completed: boolean
  started_at: Unix
  updated_at: Unix
}

export interface ProgressExportPref {
  book_id: ID
  reading_direction: ReadingDir | null
  display_mode: DisplayMode | null
  fit_mode: FitMode | null
}

export interface ProgressExport {
  format: 'shelf-progress/1'
  exported_at: Unix
  /** The importer refuses a mismatch. */
  id_version: 'shelf-id/1'
  items: ProgressExportItem[]
  prefs: ProgressExportPref[]
}

export const IMPORT_STRATEGIES = ['merge', 'replace'] as const
export type ImportStrategy = (typeof IMPORT_STRATEGIES)[number]

export interface ProgressImportResult {
  imported: number
  skipped: number
  conflicts: number
}

// ---------------------------------------------------------------------------
// §7.12 Auth (NFR-SEC-002)
// ---------------------------------------------------------------------------

/** `GET /api/auth/status` never returns 401. */
export interface AuthStatus {
  auth_required: boolean
  authenticated: boolean
}

export interface LoginRequest {
  password: string
}
