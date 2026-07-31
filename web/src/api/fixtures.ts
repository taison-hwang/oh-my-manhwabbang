/**
 * Hand-written fixtures matching the arch §7 shapes, used by the MSW-backed tests in
 * this directory.
 *
 * Two jobs:
 *  1. They are the executable form of the contract until WP-12's golden JSON files land
 *     (`internal/httpapi/testdata/golden/*.json`, WP-06 acceptance 5) — at which point
 *     these objects are replaced by the real snapshots and every assertion still applies.
 *  2. Because each one is annotated with its contract type, a field that WP-12 renames or
 *     drops fails `tsc` here, not in a component.
 *
 * The ids are the golden vectors pinned in impl-plan §3 WP-02 acceptance 1, so the
 * fixtures line up with the ids the backend's own tests produce.
 */

import type {
  AuthStatus,
  BookDetail,
  BookPrefs,
  BookSummary,
  CacheUsage,
  ContinueResponse,
  ErrorResponse,
  Health,
  PageInfo,
  Progress,
  ProgressExport,
  Root,
  RootEntry,
  RootsResponse,
  ScanLogResponse,
  ScanStatus,
  SeriesDetail,
  SeriesListResponse,
  SeriesProgress,
  SeriesSummary,
  Settings,
} from './types'

/** jsdom's default document origin; MSW handlers are registered absolutely against it. */
export const ORIGIN = 'http://localhost:3000'

export const SERIES_ID = 'gzj75n6x7rir6but'
export const BOOK_ID = 'ox74tfcrwwnfopch'
export const BOOK_CV = '3f2a91cc7b40e5d1'
export const COVER_CV = 'a1b2c3d4e5f60718'

export const root: Root = {
  name: 'mangga',
  label: '만화',
  path: '/mnt/big-data/pds/taison-data/02. books/01. mangga',
  enabled: true,
  series_count: 10,
  book_count: 96,
  page_count: 24_310,
  total_bytes: 5_472_000_000,
  available: true,
  last_scan_start: 1_753_600_000,
  last_scan_end: 1_753_600_032,
  last_scan_error: null,
  pending: false,
}

/**
 * Amendment A-11 / revision R2 — what `GET /api/roots` reports for a root that
 * `POST /api/roots` has written to `shelf.yaml` and the running server has not
 * opened. §7.3 fixes every other field of such a row: zero counts, null scan
 * timestamps, `available: false`. It is a fixture rather than a spread so those
 * five facts are stated once and a component that reads a count off a pending
 * row reads a zero, not a stale number inherited from `root`.
 */
export const pendingRoot: Root = {
  name: 'lanovel',
  label: '02. lanovel',
  path: '/mnt/big-data/pds/taison-data/02. books/02. lanovel',
  enabled: true,
  series_count: 0,
  book_count: 0,
  page_count: 0,
  total_bytes: 0,
  available: false,
  last_scan_start: null,
  last_scan_end: null,
  last_scan_error: null,
  pending: true,
}

export const rootsResponse: RootsResponse = { items: [root] }

/**
 * The `201` body of `POST /api/roots` (amendment A-11). Deliberately **not** a
 * `Root`: the entry has no index row, so the counts and the scan timestamps
 * would have to be invented. `name` is server-generated — the slug of a purely
 * Korean label is empty, so §7.4's step 2 fallback applies.
 */
export const rootEntry: RootEntry = {
  name: 'lanovel',
  path: '/mnt/big-data/pds/taison-data/02. books/02. lanovel',
  label: '02. lanovel',
  enabled: true,
}

export const seriesProgress: SeriesProgress = {
  books_total: 25,
  books_completed: 6,
  books_started: 1,
  percent: 24,
  last_read_at: 1_753_600_500,
  last_book_id: BOOK_ID,
  last_page: 42,
}

export const seriesSummary: SeriesSummary = {
  id: SERIES_ID,
  root_name: 'mangga',
  name: '[만화] 군계 1~25',
  path: '[만화] 군계 1~25',
  kind: 'folder',
  book_count: 27,
  page_count: 5_120,
  total_bytes: 622_000_000,
  mtime: 1_400_000_000,
  added_at: 1_753_500_000,
  status: 'ok',
  error: null,
  has_cover: true,
  cover_cv: COVER_CV,
  progress: seriesProgress,
}

export const progress: Progress = {
  book_id: BOOK_ID,
  series_id: SERIES_ID,
  last_page: 42,
  page_count: 187,
  completed: false,
  started_at: 1_753_600_100,
  updated_at: 1_753_600_500,
  stale: false,
}

export const bookSummary: BookSummary = {
  id: BOOK_ID,
  series_id: SERIES_ID,
  name: '군계(軍鷄) 01권.zip',
  path: '[만화] 군계 1~25/군계(軍鷄) 01권.zip',
  kind: 'zip',
  ord: 0,
  page_count: 187,
  total_bytes: 24_500_000,
  file_size: 24_500_000,
  mtime: 1_400_000_000,
  cv: BOOK_CV,
  status: 'ok',
  error: null,
  progress,
}

/** FR-IDX-010 as the UI sees it: still `200`, `pages: []`, `error` populated. */
export const brokenBookSummary: BookSummary = {
  id: 'bmknbook22222222',
  series_id: SERIES_ID,
  name: '군계(軍鷄) 07권.repair.zip',
  path: '[만화] 군계 1~25/군계(軍鷄) 07권.repair.zip',
  kind: 'zip',
  ord: 8,
  page_count: 0,
  total_bytes: 0,
  file_size: 18_300_000,
  mtime: 1_400_000_000,
  cv: 'ccdd00112233aabb',
  status: 'error',
  error: 'reading central directory: unexpected EOF',
  progress: null,
}

export const seriesDetail: SeriesDetail = {
  ...seriesSummary,
  books: [bookSummary, brokenBookSummary],
  encoding: 'cp949',
}

export const seriesListResponse: SeriesListResponse = {
  items: [seriesSummary],
  total: 10,
  offset: 0,
  limit: 60,
}

export const bookPrefs: BookPrefs = {
  reading_direction: 'rtl',
  display_mode: 'spread',
  fit_mode: 'height',
  is_override: true,
}

export const pages: PageInfo[] = [
  { n: 1, name: '001.jpg', ext: '.jpg', size: 184_320, w: 1_600, h: 2_400 },
  { n: 2, name: '002.jpg', ext: '.jpg', size: 176_100, w: null, h: null },
  { n: 3, name: '003.jpg', ext: '.jpg', size: 190_400, w: null, h: null },
]

export const bookDetail: BookDetail = {
  ...bookSummary,
  series_name: '[만화] 군계 1~25',
  root_name: 'mangga',
  pages,
  dims_state: 'partial',
  prev_book_id: null,
  next_book_id: 'nextbook33333333',
  prefs: bookPrefs,
}

/** impl-plan §4 rule 4: `status !== "ok"` is a 200 with no pages and a populated error. */
export const brokenBookDetail: BookDetail = {
  ...brokenBookSummary,
  series_name: '[만화] 군계 1~25',
  root_name: 'mangga',
  pages: [],
  dims_state: 'none',
  prev_book_id: BOOK_ID,
  next_book_id: null,
  prefs: { ...bookPrefs, is_override: false },
}

export const continueResponse: ContinueResponse = {
  items: [
    {
      book: bookSummary,
      series_id: SERIES_ID,
      series_name: '[만화] 군계 1~25',
      has_cover: true,
      progress,
    },
  ],
}

export const settings: Settings = {
  reading_direction: 'ltr',
  display_mode: 'single',
  fit_mode: 'height',
  prefetch: 4,
  theme: 'system',
  library_view: 'grid',
  library_sort: 'name',
  library_order: 'asc',
  library_scope: 'all',
  server: {
    thumbnail_widths: [120, 240, 400, 640],
    scan_workers: 8,
    thumb_workers: 4,
    pdf_enabled: true,
    avif_enabled: true,
    auth_enabled: false,
    base_path: '',
    version: '0.1.0',
    recently_added_days: 14,
    config_path: '/home/user/.config/shelf/shelf.yaml',
    // Amendment A-11. `false` is not an arbitrary fixture choice: the config key
    // behind it (`server.allow_root_editing`) ships **false** by default (ruling
    // E-26, decision 2), so this is the shape of a default deployment, and a
    // test that wants the controls has to say so explicitly.
    root_editing_enabled: false,
    config_changed_on_disk: false,
  },
}

export const cacheUsage: CacheUsage = {
  computed_at: 1_753_600_600,
  entries: [
    { kind: 'thumbs', files: 4_812, bytes: 226_000_000 },
    { kind: 'pdf', files: 120, bytes: 41_000_000 },
    { kind: 'wazero', files: 3, bytes: 18_000_000 },
  ],
  total_bytes: 285_000_000,
  cache_dir: '/home/user/.cache/shelf',
}

export const scanStatusIdle: ScanStatus = {
  state: 'idle',
  run_id: null,
  full: false,
  started_at: 1_753_600_000,
  finished_at: 1_753_600_032,
  roots: ['mangga'],
  current_root: null,
  current_item: null,
  total: 96,
  done: 96,
  errors: 3,
  covers_total: 10,
  covers_done: 10,
  elapsed_ms: 32_000,
  eta_ms: null,
  last_error: null,
}

export const scanStatusRunning: ScanStatus = {
  ...scanStatusIdle,
  state: 'indexing',
  run_id: 'run-2026-07-28-01',
  finished_at: null,
  current_root: 'mangga',
  current_item: '[만화] 군계 1~25/군계(軍鷄) 01권.zip',
  done: 41,
  covers_done: 4,
  elapsed_ms: 12_400,
  eta_ms: 19_600,
}

export const scanLogResponse: ScanLogResponse = {
  items: [
    {
      id: 41,
      ts: 1_753_600_020,
      run_id: 'run-2026-07-28-01',
      level: 'warn',
      root_name: 'mangga',
      rel_path: '[만화] 군계 1~25/군계(軍鷄) 07권.repair.zip',
      message: 'reading central directory: unexpected EOF',
    },
  ],
}

export const authStatus: AuthStatus = { auth_required: true, authenticated: false }

export const health: Health = {
  ok: true,
  version: '0.1.0',
  commit: 'deadbeef',
  started_at: 1_753_599_000,
  uptime_ms: 1_600_000,
  pdf_enabled: true,
  avif_enabled: true,
}

export const progressExport: ProgressExport = {
  format: 'shelf-progress/1',
  exported_at: 1_753_600_700,
  id_version: 'shelf-id/1',
  items: [
    {
      book_id: BOOK_ID,
      series_id: SERIES_ID,
      root_name: 'mangga',
      book_path: '[만화] 군계 1~25/군계(軍鷄) 01권.zip',
      last_page: 42,
      page_count: 187,
      completed: false,
      started_at: 1_753_600_100,
      updated_at: 1_753_600_500,
    },
  ],
  prefs: [
    {
      book_id: BOOK_ID,
      reading_direction: 'rtl',
      display_mode: 'spread',
      fit_mode: null,
    },
  ],
}

/** The §7.2 envelope, for handlers that must fail in a contract-shaped way. */
export function errorEnvelope(
  code: ErrorResponse['error']['code'],
  message: string,
  detail?: Record<string, unknown>,
): ErrorResponse {
  return detail === undefined
    ? { error: { code, message } }
    : { error: { code, message, detail } }
}
