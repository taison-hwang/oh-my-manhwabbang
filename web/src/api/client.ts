/**
 * **The only module in the frontend that calls `fetch`** (impl-plan §5.4, D-44).
 * `eslint.config.js` enforces that with `no-restricted-globals: fetch` outside `src/api/`.
 *
 * Every function here is a thin, typed wrapper over one endpoint of the frozen contract
 * (arch §7 + impl-plan §0.3). React components never call these directly — they use the
 * TanStack Query hooks in `queries.ts`.
 */

import { ApiError, codeForStatus, parseErrorEnvelope } from './errors'
import type {
  AuthStatus,
  BookDetail,
  BookPrefs,
  BookPrefsUpdate,
  BrowseResponse,
  CachePurgeResult,
  CacheUsage,
  ContinueResponse,
  Health,
  ID,
  ImportStrategy,
  Progress,
  ProgressExport,
  ProgressImportResult,
  ProgressUpdate,
  PurgeKind,
  RootCreate,
  RootEntry,
  RootsResponse,
  ScanLogParams,
  ScanLogResponse,
  ScanRunResponse,
  ScanStartRequest,
  ScanStatus,
  SeriesDetail,
  SeriesListParams,
  SeriesListResponse,
  Settings,
  SettingsUpdate,
} from './types'
import {
  authLoginUrl,
  authLogoutUrl,
  authStatusUrl,
  bookPrefsUrl,
  bookProgressUrl,
  bookUrl,
  browseUrl,
  cachePurgeUrl,
  cacheUsageUrl,
  continueUrl,
  healthUrl,
  progressExportUrl,
  progressImportUrl,
  rootsUrl,
  rootUrl,
  scanCancelUrl,
  scanLogUrl,
  scanStatusUrl,
  scanUrl,
  seriesListUrl,
  seriesRescanUrl,
  seriesUrl,
  settingsUrl,
} from './urls'

// ---------------------------------------------------------------------------
// Core
// ---------------------------------------------------------------------------

const JSON_CONTENT_TYPE = 'application/json; charset=utf-8'

/** Default wait before re-requesting a queued image when `Retry-After` is absent. */
export const DEFAULT_RETRY_AFTER_MS = 1_000
/** Upper bound applied to any server-supplied `Retry-After`. */
export const MAX_RETRY_AFTER_MS = 60_000

export interface RequestOptions {
  signal?: AbortSignal
}

interface RequestSpec extends RequestOptions {
  method?: 'GET' | 'POST' | 'PUT' | 'DELETE'
  /** Serialised as JSON. Only the fields of the typed body are sent — §7.1 rejects extras. */
  body?: unknown
  accept?: string
}

/**
 * Resolves a root-relative API path against the document origin. Native `fetch` requires
 * an absolute URL, and `<base href>` must not be applied twice — the path built by
 * `urls.ts` already carries the base path.
 */
function toAbsoluteUrl(path: string): string {
  return new URL(path, document.location.href).toString()
}

type UnauthorizedListener = () => void

const unauthorizedListeners = new Set<UnauthorizedListener>()

/**
 * Called whenever any request comes back `401`. `queries.ts` uses it to invalidate the
 * auth status so the shell falls back to the login screen (WP-06 acceptance 4).
 */
export function subscribeUnauthorized(listener: UnauthorizedListener): () => void {
  unauthorizedListeners.add(listener)
  return () => {
    unauthorizedListeners.delete(listener)
  }
}

function emitUnauthorized(): void {
  for (const listener of unauthorizedListeners) listener()
}

/** `Retry-After` in milliseconds. Accepts delta-seconds and HTTP-dates (RFC 9110). */
export function parseRetryAfter(header: string | null): number | null {
  if (header === null) return null
  const trimmed = header.trim()
  if (trimmed === '') return null
  const seconds = Number(trimmed)
  if (Number.isFinite(seconds)) {
    return clampRetryAfter(seconds * 1_000)
  }
  const at = Date.parse(trimmed)
  if (Number.isNaN(at)) return null
  return clampRetryAfter(at - Date.now())
}

function clampRetryAfter(ms: number): number {
  if (!Number.isFinite(ms) || ms < 0) return 0
  return Math.min(ms, MAX_RETRY_AFTER_MS)
}

async function toApiError(response: Response): Promise<ApiError> {
  const requestId = response.headers.get('X-Request-Id')
  const retryAfterMs = parseRetryAfter(response.headers.get('Retry-After'))
  let envelope = null
  try {
    const text = await response.text()
    if (text !== '') envelope = parseErrorEnvelope(JSON.parse(text) as unknown)
  } catch {
    envelope = null
  }
  const error = new ApiError({
    status: response.status,
    code: envelope?.code ?? codeForStatus(response.status),
    message: envelope?.message ?? '',
    detail: envelope?.detail ?? null,
    requestId,
    retryAfterMs,
    rawCode: envelope?.rawCode ?? null,
  })
  if (error.status === 401) emitUnauthorized()
  return error
}

/**
 * Whether this runtime's `fetch` accepts this `AbortSignal` implementation.
 *
 * It always does in a browser. It does **not** under vitest's jsdom environment, where
 * jsdom's `AbortController` shadows Node's global and undici rejects the foreign object
 * with `TypeError: Expected signal to be an instance of AbortSignal` — which would make
 * every component test that goes through TanStack Query (it supplies the signal) fail for
 * a reason that has nothing to do with the code under test. Probed once and cached.
 */
let fetchAcceptsSignal: boolean | null = null

function isFetchUsableSignal(signal: AbortSignal): boolean {
  if (fetchAcceptsSignal === null) {
    try {
      new Request('http://localhost/', { signal })
      fetchAcceptsSignal = true
    } catch {
      fetchAcceptsSignal = false
    }
  }
  return fetchAcceptsSignal
}

function abortError(signal: AbortSignal): Error {
  const reason: unknown = signal.reason
  if (reason instanceof Error) return reason
  return new DOMException('The operation was aborted.', 'AbortError')
}

/** Keeps "aborting rejects the request" true even where the signal cannot reach `fetch`. */
function rejectOnAbort<T>(pending: Promise<T>, signal: AbortSignal): Promise<T> {
  return Promise.race([
    pending,
    new Promise<never>((_resolve, reject) => {
      signal.addEventListener(
        'abort',
        () => {
          reject(abortError(signal))
        },
        { once: true },
      )
    }),
  ])
}

async function send(path: string, spec: RequestSpec): Promise<Response> {
  const headers: Record<string, string> = { Accept: spec.accept ?? 'application/json' }
  const init: RequestInit = {
    method: spec.method ?? 'GET',
    credentials: 'same-origin',
    headers,
  }
  if (spec.body !== undefined) {
    headers['Content-Type'] = JSON_CONTENT_TYPE
    init.body = JSON.stringify(spec.body)
  }
  const signal = spec.signal
  const url = toAbsoluteUrl(path)
  if (signal === undefined) return fetch(url, init)
  if (signal.aborted) throw abortError(signal)
  if (isFetchUsableSignal(signal)) {
    init.signal = signal
    return fetch(url, init)
  }
  return rejectOnAbort(fetch(url, init), signal)
}

/** Performs a request and decodes a JSON response body. Rejects with `ApiError` on non-2xx. */
async function requestJson<T>(path: string, spec: RequestSpec = {}): Promise<T> {
  const response = await send(path, spec)
  if (!response.ok) throw await toApiError(response)
  const text = await response.text()
  if (text === '') {
    throw new ApiError({
      status: response.status,
      code: 'internal',
      message: `empty body from ${path}`,
    })
  }
  return JSON.parse(text) as T
}

/** Performs a request whose success response has no body worth reading (`204`, or `202`). */
async function requestVoid(path: string, spec: RequestSpec = {}): Promise<void> {
  const response = await send(path, spec)
  if (!response.ok) throw await toApiError(response)
  await response.text()
}

// ---------------------------------------------------------------------------
// Images — where `202` is a normal answer (impl-plan §4, rule 3)
// ---------------------------------------------------------------------------

export type ImageResult =
  | { state: 'ready'; url: string }
  | { state: 'queued'; url: string; retryAfterMs: number }

/**
 * Requests a cover or page thumbnail and reports whether it is ready.
 *
 * `202` is **not** an error: the thumbnail is queued and the caller should retry after
 * `retryAfterMs` (arch §7.5/§7.6). The body of a ready response is drained and dropped —
 * the caller renders `<img src={url}>`, which the browser serves out of its HTTP cache
 * because the URL carries `?v={cv}` and is therefore `immutable` (arch §5.3).
 */
export async function fetchImage(url: string, options: RequestOptions = {}): Promise<ImageResult> {
  const spec: RequestSpec = { accept: 'image/*' }
  if (options.signal !== undefined) spec.signal = options.signal
  const response = await send(url, spec)
  if (response.status === 202) {
    await response.text()
    return {
      state: 'queued',
      url,
      retryAfterMs: parseRetryAfter(response.headers.get('Retry-After')) ?? DEFAULT_RETRY_AFTER_MS,
    }
  }
  if (!response.ok) throw await toApiError(response)
  await response.blob()
  return { state: 'ready', url }
}

// ---------------------------------------------------------------------------
// §7.4 Health and roots
// ---------------------------------------------------------------------------

export function getHealth(options?: RequestOptions): Promise<Health> {
  return requestJson<Health>(healthUrl(), options)
}

export function getRoots(options?: RequestOptions): Promise<RootsResponse> {
  return requestJson<RootsResponse>(rootsUrl(), options)
}

/**
 * `POST /api/roots` → `201 RootEntry` — amendment A-11 (ruling E-26), as
 * amended for adoption by **A-12** (ruling **E-40**).
 *
 * It writes the `roots:` list of `shelf.yaml` **and, when it can, opens the root
 * into the running server and starts a scan of it.** The restart A-11 required
 * is gone for addition — but only for addition, and only when the adoption
 * succeeds. If the directory cannot be opened after the write the entry is
 * still created and falls back to A-11's behaviour: a `pending` row and a
 * restart notice. Either way the `201` body is the same `RootEntry`, so the
 * client learns which happened by re-reading `GET /api/roots` — which is what
 * `useCreateRoot` invalidates.
 *
 * `name` is server-generated and is not in the request. `403 forbidden` when
 * `server.allow_root_editing` is off, `400` naming the validation rule that
 * failed.
 */
export function createRoot(body: RootCreate, options?: RequestOptions): Promise<RootEntry> {
  return requestJson<RootEntry>(rootsUrl(), { ...options, method: 'POST', body })
}

/**
 * `GET /api/browse[?path=…]` → `200 BrowseResponse` — amendment **A-12**
 * (ruling **E-40**).
 *
 * Omit `path` for the synthetic top level: the `server.browse_bases` allowlist.
 * `403 forbidden` when root editing is off (`disabled`) or when no bases are
 * configured (`no_browse_bases`), and `403 outside_browse_bases` for a path the
 * allowlist does not cover — deliberately the same answer as for a path that
 * does not exist, so the error codes cannot be used to probe the host's
 * filesystem.
 */
export function getBrowse(path?: string, options?: RequestOptions): Promise<BrowseResponse> {
  return requestJson<BrowseResponse>(browseUrl(path), options)
}

/**
 * `DELETE /api/roots/{name}` → `204` — amendment A-11, as revised by R1.
 *
 * Removes the entry from the file **and** purges that root's rows from
 * `index.db`, so the row and its series go at once. `user.db` is untouched:
 * reading progress survives and reattaches if the same directory is added again.
 */
export function deleteRoot(name: string, options?: RequestOptions): Promise<void> {
  return requestVoid(rootUrl(name), { ...options, method: 'DELETE' })
}

// ---------------------------------------------------------------------------
// §7.5 Series
// ---------------------------------------------------------------------------

export function listSeries(
  params: SeriesListParams = {},
  options?: RequestOptions,
): Promise<SeriesListResponse> {
  return requestJson<SeriesListResponse>(seriesListUrl(params), options)
}

export function getSeries(sid: ID, options?: RequestOptions): Promise<SeriesDetail> {
  return requestJson<SeriesDetail>(seriesUrl(sid), options)
}

/** `202 {run_id}`; `409 conflict` when a scan is already running. */
export function rescanSeries(sid: ID, options?: RequestOptions): Promise<ScanRunResponse> {
  return requestJson<ScanRunResponse>(seriesRescanUrl(sid), { ...options, method: 'POST' })
}

// ---------------------------------------------------------------------------
// §7.6 Books, pages, progress and prefs
// ---------------------------------------------------------------------------

/**
 * A book with `status !== "ok"` still answers `200`, with `pages: []` and a populated
 * `error` (impl-plan §4, rule 4) — it is never an HTTP error.
 */
export function getBook(bid: ID, options?: RequestOptions): Promise<BookDetail> {
  return requestJson<BookDetail>(bookUrl(bid), options)
}

export function putProgress(
  bid: ID,
  update: ProgressUpdate,
  options?: RequestOptions,
): Promise<Progress> {
  return requestJson<Progress>(bookProgressUrl(bid), {
    ...options,
    method: 'PUT',
    body: update,
  })
}

/** "Mark as unread" — `204`, no body. */
export function deleteProgress(bid: ID, options?: RequestOptions): Promise<void> {
  return requestVoid(bookProgressUrl(bid), { ...options, method: 'DELETE' })
}

export function getBookPrefs(bid: ID, options?: RequestOptions): Promise<BookPrefs> {
  return requestJson<BookPrefs>(bookPrefsUrl(bid), options)
}

/** A `null` field clears the per-book override and falls back to the global default. */
export function putBookPrefs(
  bid: ID,
  update: BookPrefsUpdate,
  options?: RequestOptions,
): Promise<BookPrefs> {
  return requestJson<BookPrefs>(bookPrefsUrl(bid), { ...options, method: 'PUT', body: update })
}

// ---------------------------------------------------------------------------
// §7.7 Continue reading
// ---------------------------------------------------------------------------

export function getContinue(limit?: number, options?: RequestOptions): Promise<ContinueResponse> {
  return requestJson<ContinueResponse>(continueUrl(limit), options)
}

// ---------------------------------------------------------------------------
// §7.8 Settings
// ---------------------------------------------------------------------------

export function getSettings(options?: RequestOptions): Promise<Settings> {
  return requestJson<Settings>(settingsUrl(), options)
}

/** Partial; sending a `server.*` key is `400 bad_request`, so only user keys are typed. */
export function putSettings(update: SettingsUpdate, options?: RequestOptions): Promise<Settings> {
  return requestJson<Settings>(settingsUrl(), { ...options, method: 'PUT', body: update })
}

// ---------------------------------------------------------------------------
// §7.9 Cache
// ---------------------------------------------------------------------------

export function getCacheUsage(options?: RequestOptions): Promise<CacheUsage> {
  return requestJson<CacheUsage>(cacheUsageUrl(), options)
}

export function purgeCache(
  kind: PurgeKind,
  options?: RequestOptions,
): Promise<CachePurgeResult> {
  return requestJson<CachePurgeResult>(cachePurgeUrl(kind), { ...options, method: 'DELETE' })
}

// ---------------------------------------------------------------------------
// §7.10 Scan
// ---------------------------------------------------------------------------

export function startScan(
  request: ScanStartRequest = {},
  options?: RequestOptions,
): Promise<ScanRunResponse> {
  return requestJson<ScanRunResponse>(scanUrl(), { ...options, method: 'POST', body: request })
}

export function getScanStatus(options?: RequestOptions): Promise<ScanStatus> {
  return requestJson<ScanStatus>(scanStatusUrl(), options)
}

export function cancelScan(options?: RequestOptions): Promise<void> {
  return requestVoid(scanCancelUrl(), { ...options, method: 'POST' })
}

export function getScanLog(
  params: ScanLogParams = {},
  options?: RequestOptions,
): Promise<ScanLogResponse> {
  return requestJson<ScanLogResponse>(scanLogUrl(params), options)
}

// ---------------------------------------------------------------------------
// §7.11 Progress export / import (FR-STT-004 — API only, no UI in this build)
// ---------------------------------------------------------------------------

export function exportProgress(options?: RequestOptions): Promise<ProgressExport> {
  return requestJson<ProgressExport>(progressExportUrl(), options)
}

export function importProgress(
  payload: ProgressExport,
  strategy?: ImportStrategy,
  options?: RequestOptions,
): Promise<ProgressImportResult> {
  return requestJson<ProgressImportResult>(progressImportUrl(strategy), {
    ...options,
    method: 'POST',
    body: payload,
  })
}

// ---------------------------------------------------------------------------
// §7.12 Auth
// ---------------------------------------------------------------------------

/** Never returns `401`; it is how the shell decides to render the login screen. */
export function getAuthStatus(options?: RequestOptions): Promise<AuthStatus> {
  return requestJson<AuthStatus>(authStatusUrl(), options)
}

/** `204 + Set-Cookie`, or `401 unauthorized` (`429` while rate-limited). */
export function login(password: string, options?: RequestOptions): Promise<void> {
  return requestVoid(authLoginUrl(), { ...options, method: 'POST', body: { password } })
}

export function logout(options?: RequestOptions): Promise<void> {
  return requestVoid(authLogoutUrl(), { ...options, method: 'POST' })
}
