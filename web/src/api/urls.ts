/**
 * Every URL the frontend uses is built here — base path, query encoding, the `?v={cv}`
 * cache version and the `?w=` thumbnail width discipline (impl-plan §5.4, §0.4).
 *
 * Builders return **root-relative** paths (`/reader/api/…`). That is what an `<img src>`
 * wants, and `client.ts` resolves them against the document origin before fetching.
 */

import type {
  ID,
  ScanLogParams,
  SeriesListParams,
  PurgeKind,
  ImportStrategy,
} from './types'

// ---------------------------------------------------------------------------
// Base path (NFR-SEC-003)
// ---------------------------------------------------------------------------

/**
 * Normalises a base path to `''` (mounted at the origin root) or `/segment` with a
 * leading and no trailing slash. `base_path: "/reader"` therefore prefixes every URL
 * below with `/reader` exactly once.
 */
export function normalizeBasePath(raw: string): string {
  let path = raw.trim()
  if (path === '' || path === '/') return ''
  if (!path.startsWith('/')) path = `/${path}`
  while (path.endsWith('/')) path = path.slice(0, -1)
  return path
}

/**
 * Reads the base path from the `<base href>` tag the server injects into `index.html`
 * (arch §7.1, WP-12 acceptance 7). Deliberately does **not** use `document.baseURI`:
 * with no `<base>` tag that returns the current document URL, so a deep link such as
 * `/series/abc` would be mistaken for a base path.
 */
export function readBasePathFromDocument(doc: Document): string {
  const el = doc.querySelector('base')
  const href = el?.getAttribute('href')
  if (href === null || href === undefined || href === '') return ''
  return normalizeBasePath(new URL(href, doc.location.href).pathname)
}

let basePathOverride: string | null = null

/** The base path in force. Resolved from `<base href>` once, then cached. */
export function getBasePath(): string {
  basePathOverride ??= readBasePathFromDocument(document)
  return basePathOverride
}

/** Pins the base path explicitly (used by the app shell and by tests). */
export function setBasePath(raw: string): void {
  basePathOverride = normalizeBasePath(raw)
}

/** Drops the cached value so the next read resolves from the document again. */
export function resetBasePath(): void {
  basePathOverride = null
}

// ---------------------------------------------------------------------------
// Query encoding
// ---------------------------------------------------------------------------

/** A value that may appear in a query string. `undefined` and `null` are dropped. */
export type QueryValue = string | number | boolean | readonly string[] | null | undefined

export type QueryParams = Record<string, QueryValue>

function isStringArray(value: unknown): value is readonly string[] {
  return Array.isArray(value)
}

/** Serialises params in insertion order; array values repeat the key (e.g. `root`). */
export function encodeQuery(params: QueryParams): string {
  const search = new URLSearchParams()
  for (const [key, value] of Object.entries(params)) {
    if (value === undefined || value === null) continue
    if (isStringArray(value)) {
      for (const item of value) search.append(key, item)
      continue
    }
    search.append(key, String(value))
  }
  const encoded = search.toString()
  return encoded === '' ? '' : `?${encoded}`
}

/** `{base_path}/api{path}` plus an encoded query string. */
export function apiUrl(path: string, params: QueryParams = {}): string {
  return `${getBasePath()}/api${path}${encodeQuery(params)}`
}

// ---------------------------------------------------------------------------
// Thumbnail widths (impl-plan §0.4 / amendments A-1, A-6)
// ---------------------------------------------------------------------------

/**
 * The configured `thumbnails.widths` (A-1). The server snaps a requested width **up**
 * to the nearest configured one, so sending an off-set width silently doubles
 * bandwidth: the frontend always sends a member of this tuple.
 */
export const THUMB_WIDTHS = [120, 240, 400, 640] as const
export type ThumbWidth = (typeof THUMB_WIDTHS)[number]

/** Per-consumer widths derived in impl-plan §0.4 from the real rendered sizes at 2× DPR. */
export const THUMB_WIDTH_FOR = {
  /** Viewer thumbnail strip — 48 CSS px. */
  viewerStrip: 120,
  /** List-row thumb — 24 CSS px. */
  listRow: 120,
  /** 이어보기 card thumb — 66 CSS px. */
  continueCard: 240,
  /** Slider drag preview — 68 CSS px. */
  sliderPreview: 240,
  /** Volume tile on series detail — 128 CSS px. */
  volumeTile: 400,
  /** Grid cover at ≥1440 (`--grid-min:152`). */
  gridCoverWide: 400,
  /** Grid cover at ≤768 (`--grid-min:224`). */
  gridCoverNarrow: 640,
  /** Series-detail hero cover — 176 CSS px. */
  seriesHero: 400,
} as const satisfies Record<string, ThumbWidth>

export type ThumbConsumer = keyof typeof THUMB_WIDTH_FOR

/** The largest configured width; anything wider is clamped to it. */
export const LARGEST_THUMB_WIDTH = 640 satisfies ThumbWidth

/** The width a consumer must request. */
export function thumbWidthFor(consumer: ThumbConsumer): ThumbWidth {
  return THUMB_WIDTH_FOR[consumer]
}

/** Snaps an arbitrary device-pixel width **up** to a configured width, clamped to the largest. */
export function snapThumbWidth(devicePx: number): ThumbWidth {
  return THUMB_WIDTHS.find((width) => devicePx <= width) ?? LARGEST_THUMB_WIDTH
}

// ---------------------------------------------------------------------------
// 1-based page numbers (impl-plan §4, rule 1)
// ---------------------------------------------------------------------------

/** Throws on any non-1-based page number. There is no page 0. */
export function assertPageNumber(n: number): void {
  if (!Number.isInteger(n) || n < 1) {
    throw new RangeError(`page numbers are 1-based integers; got ${String(n)}`)
  }
}

// ---------------------------------------------------------------------------
// Endpoint builders
// ---------------------------------------------------------------------------

export function healthUrl(): string {
  return apiUrl('/health')
}

/** `GET` the list, and — amendment A-11 — `POST` a new configuration entry. */
export function rootsUrl(): string {
  return apiUrl('/roots')
}

/**
 * `DELETE /api/roots/{name}` — amendment A-11 (ruling E-26).
 *
 * `{name}` is a **configuration** identity (`[a-zA-Z0-9._-]{1,64}`, arch §3.2),
 * not one of §7.1's opaque `[a-z2-7]{16}` ids, so it is the one wildcard in this
 * file whose legal alphabet includes `.` — and therefore the one where a caller
 * could try `..`. It is encoded like every other segment; the server answers
 * `400` for a syntactically invalid name and `404` for a well-formed absent one.
 */
export function rootUrl(name: string): string {
  return apiUrl(`/roots/${encodeURIComponent(name)}`)
}

export function seriesListUrl(params: SeriesListParams = {}): string {
  return apiUrl('/series', {
    root: params.root,
    q: params.q,
    status: params.status,
    progress: params.progress,
    scope: params.scope,
    sort: params.sort,
    order: params.order,
    offset: params.offset,
    limit: params.limit,
  })
}

export function seriesUrl(sid: ID): string {
  return apiUrl(`/series/${encodeURIComponent(sid)}`)
}

export function seriesRescanUrl(sid: ID): string {
  return apiUrl(`/series/${encodeURIComponent(sid)}/rescan`)
}

/**
 * Cover thumbnail. `v` is the series' `cover_cv`; without it the response is only
 * cacheable for 60 s (arch §5.3), so it is always sent when the series has one.
 */
export function seriesCoverUrl(sid: ID, options: ImageUrlOptions): string {
  return apiUrl(`/series/${encodeURIComponent(sid)}/cover`, {
    w: options.w,
    v: options.v,
  })
}

export interface ImageUrlOptions {
  /** Always explicit, always from `THUMB_WIDTHS` (§0.4). */
  w: ThumbWidth
  /** The book's `cv` / the series' `cover_cv`; `null` only when the server has none. */
  v: string | null
}

export function bookUrl(bid: ID): string {
  return apiUrl(`/books/${encodeURIComponent(bid)}`)
}

export interface PageUrlOptions {
  /** The book's `cv`. Enables `Cache-Control: immutable` (arch §5.3). */
  v: string | null
  /** PDF only: render width in px. Ignored by the server for `zip`/`dir` books. */
  w?: number
}

/** The hot path. `n` is 1-based. */
export function pageUrl(bid: ID, n: number, options: PageUrlOptions): string {
  assertPageNumber(n)
  return apiUrl(`/books/${encodeURIComponent(bid)}/pages/${String(n)}`, {
    v: options.v,
    w: options.w,
  })
}

/** Page thumbnail. `n` is 1-based. */
export function pageThumbUrl(bid: ID, n: number, options: ImageUrlOptions): string {
  assertPageNumber(n)
  return apiUrl(`/books/${encodeURIComponent(bid)}/thumbs/${String(n)}`, {
    w: options.w,
    v: options.v,
  })
}

export function bookProgressUrl(bid: ID): string {
  return apiUrl(`/books/${encodeURIComponent(bid)}/progress`)
}

export function bookPrefsUrl(bid: ID): string {
  return apiUrl(`/books/${encodeURIComponent(bid)}/prefs`)
}

export function continueUrl(limit?: number): string {
  return apiUrl('/continue', { limit })
}

export function settingsUrl(): string {
  return apiUrl('/settings')
}

export function cacheUsageUrl(): string {
  return apiUrl('/cache/usage')
}

export function cachePurgeUrl(kind: PurgeKind): string {
  return apiUrl('/cache', { kind })
}

export function scanUrl(): string {
  return apiUrl('/scan')
}

export function scanStatusUrl(): string {
  return apiUrl('/scan/status')
}

export function scanCancelUrl(): string {
  return apiUrl('/scan/cancel')
}

export function scanLogUrl(params: ScanLogParams = {}): string {
  return apiUrl('/scan/log', {
    limit: params.limit,
    level: params.level,
    run_id: params.run_id,
    since_id: params.since_id,
  })
}

export function progressExportUrl(): string {
  return apiUrl('/progress/export')
}

export function progressImportUrl(strategy?: ImportStrategy): string {
  return apiUrl('/progress/import', { strategy })
}

export function authStatusUrl(): string {
  return apiUrl('/auth/status')
}

export function authLoginUrl(): string {
  return apiUrl('/auth/login')
}

export function authLogoutUrl(): string {
  return apiUrl('/auth/logout')
}
