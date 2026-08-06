/**
 * TanStack Query v5 hooks over `client.ts` — the only way the app reads or writes server
 * state (impl-plan §5.2: "No `useEffect` + `fetch`"). Query keys are exported arrays.
 *
 * Policies encoded here (WP-06 acceptance 4):
 *  * `useScanStatus` polls every 1 000 ms while `state !== "idle"` and stops otherwise (C-11).
 *  * `useScanCompletionRefresh` re-reads the library once per completed run, off that poll.
 *  * A `202` on an image endpoint retries after `Retry-After`, at most 10 attempts, then
 *    reports `unavailable` so the caller falls back to the FR-LIB-008 placeholder.
 *  * A `409 stale_version` invalidates the book query and retries once.
 *  * A `401` invalidates the auth status, which flips the shell to the login screen.
 *  * `useSaveProgress` debounces 1 s and is idempotent.
 */

import {
  keepPreviousData,
  QueryClient,
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
  type UseInfiniteQueryResult,
  type UseMutationResult,
  type UseQueryResult,
} from '@tanstack/react-query'
import { useCallback, useEffect, useRef } from 'react'

import {
  cancelScan,
  createRoot,
  deleteProgress,
  deleteRoot,
  fetchImage,
  getAuthStatus,
  getBook,
  getBookPrefs,
  getBrowse,
  getCacheUsage,
  getContinue,
  getHealth,
  getRoots,
  getScanLog,
  getScanStatus,
  getSeries,
  getSettings,
  listSeries,
  login,
  logout,
  purgeCache,
  putBookPrefs,
  putProgress,
  putSettings,
  rescanSeries,
  startScan,
  subscribeUnauthorized,
  DEFAULT_RETRY_AFTER_MS,
} from './client'
import { type ApiError, ImageQueuedError, isApiError, isImageQueuedError } from './errors'
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
  Progress,
  ProgressUpdate,
  PurgeKind,
  RootCreate,
  RootEntry,
  RootsResponse,
  ScanLogParams,
  ScanLogResponse,
  ScanRunResponse,
  ScanStartRequest,
  ScanState,
  ScanStatus,
  SeriesDetail,
  SeriesListParams,
  SeriesListResponse,
  Settings,
  SettingsUpdate,
} from './types'
import { SERIES_LIST_DEFAULT_LIMIT } from './types'
import {
  assertPageNumber,
  pageThumbUrl,
  seriesCoverUrl,
  type ThumbWidth,
} from './urls'

// ---------------------------------------------------------------------------
// Policies
// ---------------------------------------------------------------------------

/** C-11 / arch §7.10: poll scan status once a second while a run is in flight. */
export const SCAN_POLL_MS = 1_000
/** FR-VWR-009: page turns are cheap, the write is not. */
export const PROGRESS_DEBOUNCE_MS = 1_000
/** Total attempts for a queued (`202`) image before falling back to the placeholder. */
export const MAX_IMAGE_ATTEMPTS = 10
/** A stale `?v=` is retried once, after the book metadata has been invalidated. */
export const STALE_VERSION_RETRIES = 1

/** A 4xx is the server's final answer; only transport and 5xx failures are retried. */
function retryQuery(failureCount: number, error: Error): boolean {
  if (isApiError(error) && error.status >= 400 && error.status < 500) return false
  return failureCount < 2
}

/** Defaults for the app's `QueryClient`; the shell (WP-05) may use this as-is. */
export function createQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: {
        retry: retryQuery,
        refetchOnWindowFocus: false,
        staleTime: 30_000,
      },
      mutations: { retry: false },
    },
  })
}

// ---------------------------------------------------------------------------
// Query keys
// ---------------------------------------------------------------------------

export const queryKeys = {
  health: ['health'] as const,
  roots: ['roots'] as const,
  /**
   * The directory picker (A-12). Keyed by the path so that walking back up a
   * breadcrumb is a cache hit rather than a refetch — `undefined` is the
   * synthetic top level and is a distinct key from any real directory.
   */
  browse: (path?: string) => ['browse', path ?? null] as const,
  series: {
    all: ['series'] as const,
    list: (params: SeriesListParams) => ['series', 'list', params] as const,
    infinite: (params: SeriesListParams) => ['series', 'infinite', params] as const,
    detail: (sid: ID) => ['series', 'detail', sid] as const,
  },
  books: {
    all: ['books'] as const,
    detail: (bid: ID) => ['books', 'detail', bid] as const,
    prefs: (bid: ID) => ['books', 'prefs', bid] as const,
  },
  continueReading: {
    all: ['continue'] as const,
    list: (limit?: number) => ['continue', { limit }] as const,
  },
  settings: ['settings'] as const,
  cache: { usage: ['cache', 'usage'] as const },
  scan: {
    all: ['scan'] as const,
    status: ['scan', 'status'] as const,
    /**
     * The **prefix** every `useScanLog` key starts with; `log(params)` appends
     * the params object, so `['scan','log',{limit:200}]` and
     * `['scan','log',{level:'error'}]` are different queries and neither is
     * matched by `queryKeys.scan.status`. Invalidating the log therefore needs
     * this, not `status` — the omission is why the 스캔 로그 panel never moved
     * while the user watched it.
     */
    logs: ['scan', 'log'] as const,
    log: (params: ScanLogParams) => ['scan', 'log', params] as const,
  },
  auth: { status: ['auth', 'status'] as const },
  image: {
    all: ['image'] as const,
    cover: (sid: ID, w: number, v: string | null) => ['image', 'cover', sid, w, v] as const,
    thumb: (bid: ID, n: number, w: number, v: string | null) =>
      ['image', 'thumb', bid, n, w, v] as const,
  },
} as const

export interface EnabledOption {
  enabled?: boolean
}

// ---------------------------------------------------------------------------
// §7.4 Health and roots
// ---------------------------------------------------------------------------

export function useHealth(options: EnabledOption = {}): UseQueryResult<Health> {
  return useQuery({
    queryKey: queryKeys.health,
    queryFn: ({ signal }) => getHealth({ signal }),
    enabled: options.enabled ?? true,
    retry: retryQuery,
  })
}

export function useRoots(options: EnabledOption = {}): UseQueryResult<RootsResponse> {
  return useQuery({
    queryKey: queryKeys.roots,
    queryFn: ({ signal }) => getRoots({ signal }),
    enabled: options.enabled ?? true,
    retry: retryQuery,
  })
}

/**
 * Both root-editing mutations invalidate the **same two** queries, and neither
 * writes the cache itself. Amendment A-11 (ruling E-26):
 *
 *  * `roots` — the server is the only thing that knows what the list now looks
 *    like. R2 turns a `POST` into a `pending` row and R1 makes a `DELETE` drop
 *    the row at once, and neither shape is derivable from the request: `POST`'s
 *    `201` is a `RootEntry`, which has no counts and no `available`, and
 *    splicing a `Root` together from it would put invented numbers on screen.
 *  * `settings` — `server.config_changed_on_disk` is always true after a
 *    successful write (arch §7.8) and is what drives the restart notice, so a
 *    mutation that refreshed only the list would leave the screen claiming the
 *    file still matched what the server loaded.
 *
 * The promise is **returned**, so the mutation stays `isPending` until both
 * refetches have settled. That is what keeps the confirm button disabled across
 * the gap in which the list is stale, rather than flashing the old rows back.
 */
function invalidateRootState(queryClient: QueryClient): Promise<void> {
  return Promise.all([
    queryClient.invalidateQueries({ queryKey: queryKeys.roots }),
    queryClient.invalidateQueries({ queryKey: queryKeys.settings }),
  ]).then(() => undefined)
}

/**
 * `GET /api/browse` — the directory picker of amendment **A-12** (ruling E-40).
 *
 * `enabled` matters here more than on the other read hooks: the endpoint is
 * `403` unless the operator turned on both `server.allow_root_editing` and
 * `server.browse_bases`, so mounting the picker unconditionally would fire a
 * request that fails on every server that has not opted in. The caller passes
 * the gate it already reads from `Settings`.
 *
 * `retry: false` rather than `retryQuery`: every failure this endpoint has is a
 * refusal about configuration, and retrying a `403` three times delays the
 * message that tells the user which key to set.
 */
export function useBrowse(
  path: string | undefined,
  options: EnabledOption = {},
): UseQueryResult<BrowseResponse> {
  return useQuery({
    queryKey: queryKeys.browse(path),
    queryFn: ({ signal }) => getBrowse(path, { signal }),
    enabled: options.enabled ?? true,
    retry: false,
  })
}

/**
 * `POST /api/roots` (amendment A-11, adoption by A-12).
 *
 * Since ruling E-40 a successful add is usually *live* — the server opens the
 * root and starts scanning it — so this invalidates the scan status too. Without
 * that the settings dialog's progress block (E-38) would not appear until its
 * own 1 s poll came round, and the first thing the user sees after pressing 추가
 * is the second in which nothing has happened yet.
 */
export function useCreateRoot(): UseMutationResult<RootEntry, Error, RootCreate> {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (body: RootCreate) => createRoot(body),
    onSuccess: () =>
      Promise.all([
        invalidateRootState(queryClient),
        // Only on the add. A `DELETE` starts nothing, and invalidating the scan
        // status there would be a request made to observe a change that cannot
        // have happened.
        queryClient.invalidateQueries({ queryKey: queryKeys.scan.status }),
      ]).then(() => undefined),
  })
}

/**
 * `DELETE /api/roots/{name}` (amendment A-11 / R1). The series go with it; the
 * reading progress in `user.db` does not.
 */
export function useDeleteRoot(): UseMutationResult<void, Error, string> {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (name: string) => deleteRoot(name),
    onSuccess: () => invalidateRootState(queryClient),
    // A `404` means the root is already gone from the file on disk — a hand-edit
    // beat this click, or an earlier DELETE did. The list on screen is then the
    // stale half, so it is re-read on failure too, exactly as on success.
    onError: (error) => {
      if (isApiError(error) && error.status === 404) void invalidateRootState(queryClient)
    },
  })
}

// ---------------------------------------------------------------------------
// §7.5 Series
// ---------------------------------------------------------------------------

export function useSeriesList(
  params: SeriesListParams = {},
  options: EnabledOption = {},
): UseQueryResult<SeriesListResponse> {
  return useQuery({
    queryKey: queryKeys.series.list(params),
    queryFn: ({ signal }) => listSeries(params, { signal }),
    enabled: options.enabled ?? true,
    // Keeps the grid populated while a 150 ms-debounced search changes the key (C-10).
    placeholderData: keepPreviousData,
    retry: retryQuery,
  })
}

/** FR-LIB-007: offset/limit pagination behind the virtualised grid and list. */
export function useSeriesListInfinite(
  params: SeriesListParams = {},
  options: EnabledOption = {},
): UseInfiniteQueryResult<{ pages: SeriesListResponse[]; pageParams: number[] }> {
  const limit = params.limit ?? SERIES_LIST_DEFAULT_LIMIT
  return useInfiniteQuery({
    queryKey: queryKeys.series.infinite({ ...params, limit }),
    initialPageParam: params.offset ?? 0,
    queryFn: ({ pageParam, signal }) => listSeries({ ...params, limit, offset: pageParam }, { signal }),
    getNextPageParam: (last: SeriesListResponse) => {
      const next = last.offset + last.items.length
      return next < last.total ? next : undefined
    },
    enabled: options.enabled ?? true,
    retry: retryQuery,
  })
}

export function useSeries(
  sid: ID,
  options: EnabledOption = {},
): UseQueryResult<SeriesDetail> {
  return useQuery({
    queryKey: queryKeys.series.detail(sid),
    queryFn: ({ signal }) => getSeries(sid, { signal }),
    enabled: options.enabled ?? true,
    retry: retryQuery,
  })
}

/** UI-002 "이 시리즈 재스캔"; `409 conflict` surfaces as an `ApiError` for the notice. */
export function useRescanSeries(): UseMutationResult<ScanRunResponse, Error, ID> {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (sid: ID) => rescanSeries(sid),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.scan.status })
    },
  })
}

// ---------------------------------------------------------------------------
// §7.6 Books, progress, prefs
// ---------------------------------------------------------------------------

/**
 * The whole book in one request, including every `PageInfo` (D-15/D-9) — this is what
 * makes arbitrary page jumps instant (AC-008). A book with `status !== "ok"` resolves
 * normally with `pages: []`; it is not an error.
 */
export function useBook(bid: ID, options: EnabledOption = {}): UseQueryResult<BookDetail> {
  return useQuery({
    queryKey: queryKeys.books.detail(bid),
    queryFn: ({ signal }) => getBook(bid, { signal }),
    enabled: options.enabled ?? true,
    retry: retryQuery,
  })
}

export function useBookPrefs(
  bid: ID,
  options: EnabledOption = {},
): UseQueryResult<BookPrefs> {
  return useQuery({
    queryKey: queryKeys.books.prefs(bid),
    queryFn: ({ signal }) => getBookPrefs(bid, { signal }),
    enabled: options.enabled ?? true,
    retry: retryQuery,
  })
}

/** FR-VWR-002. A `null` field clears the override and falls back to the global default. */
export function useSetPrefs(bid: ID): UseMutationResult<BookPrefs, Error, BookPrefsUpdate> {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (update: BookPrefsUpdate) => putBookPrefs(bid, update),
    onSuccess: (prefs) => {
      queryClient.setQueryData<BookPrefs>(queryKeys.books.prefs(bid), prefs)
      queryClient.setQueryData<BookDetail>(queryKeys.books.detail(bid), (old) =>
        old === undefined ? undefined : { ...old, prefs },
      )
    },
  })
}

export interface SaveProgressOptions {
  /** Overridable for tests; production is `PROGRESS_DEBOUNCE_MS`. */
  debounceMs?: number
}

/**
 * The book id travels *with* the write. FR-VWR-010 ("다음 권") changes only the `:bid`
 * route param, so React Router keeps the viewer mounted and `useSaveProgress` is simply
 * re-rendered with a new `bid`. If the pending write closed over the render-time `bid`,
 * a debounced page from book A would land on book B — losing A's progress and
 * overwriting B's in `user.db` (D-13 / NFR-DAT-004).
 */
export interface SaveProgressVariables {
  bid: ID
  update: ProgressUpdate
}

export interface SaveProgressApi {
  /** Records a 1-based page; the write is debounced and idempotent. */
  save: (page: number, completed?: boolean) => void
  /** Sends the pending write immediately — use on unmount and `visibilitychange`. */
  flush: () => void
  mutation: UseMutationResult<Progress, Error, SaveProgressVariables>
}

/** FR-STT-001 / FR-VWR-009: `PUT /api/books/{bid}/progress`, debounced 1 s. */
export function useSaveProgress(bid: ID, options: SaveProgressOptions = {}): SaveProgressApi {
  const debounceMs = options.debounceMs ?? PROGRESS_DEBOUNCE_MS
  const queryClient = useQueryClient()
  const mutation = useMutation({
    mutationFn: (variables: SaveProgressVariables) => putProgress(variables.bid, variables.update),
    onSuccess: (progress, variables) => {
      queryClient.setQueryData<BookDetail>(queryKeys.books.detail(variables.bid), (old) =>
        old === undefined ? undefined : { ...old, progress },
      )
      void queryClient.invalidateQueries({
        queryKey: queryKeys.continueReading.all,
        refetchType: 'none',
      })
    },
  })

  const { mutate } = mutation
  const pending = useRef<SaveProgressVariables | null>(null)
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null)

  const flush = useCallback(() => {
    if (timer.current !== null) {
      clearTimeout(timer.current)
      timer.current = null
    }
    const variables = pending.current
    if (variables === null) return
    pending.current = null
    mutate(variables)
  }, [mutate])

  const save = useCallback(
    (page: number, completed?: boolean) => {
      assertPageNumber(page)
      pending.current = {
        bid,
        update: completed === undefined ? { page } : { page, completed },
      }
      if (timer.current !== null) clearTimeout(timer.current)
      timer.current = setTimeout(flush, debounceMs)
    },
    [bid, debounceMs, flush],
  )

  // Flushes on unmount *and* whenever `bid` changes, so the previous book's pending
  // write goes out before the next book's `save` can replace it in the buffer.
  useEffect(() => flush, [flush, bid])

  return { save, flush, mutation }
}

/** FR-VWR-012 "안읽음": `DELETE /api/books/{bid}/progress`. */
export function useDeleteProgress(): UseMutationResult<void, Error, ID> {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (bid: ID) => deleteProgress(bid),
    onSuccess: (_result, bid) => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.books.detail(bid) })
      void queryClient.invalidateQueries({ queryKey: queryKeys.series.all })
      void queryClient.invalidateQueries({ queryKey: queryKeys.continueReading.all })
    },
  })
}

// ---------------------------------------------------------------------------
// §7.7 Continue reading
// ---------------------------------------------------------------------------

export function useContinue(
  limit?: number,
  options: EnabledOption = {},
): UseQueryResult<ContinueResponse> {
  return useQuery({
    queryKey: queryKeys.continueReading.list(limit),
    queryFn: ({ signal }) => getContinue(limit, { signal }),
    enabled: options.enabled ?? true,
    retry: retryQuery,
  })
}

// ---------------------------------------------------------------------------
// §7.8 Settings
// ---------------------------------------------------------------------------

export function useSettings(options: EnabledOption = {}): UseQueryResult<Settings> {
  return useQuery({
    queryKey: queryKeys.settings,
    queryFn: ({ signal }) => getSettings({ signal }),
    enabled: options.enabled ?? true,
    retry: retryQuery,
  })
}

export function useSaveSettings(): UseMutationResult<Settings, Error, SettingsUpdate> {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (update: SettingsUpdate) => putSettings(update),
    onSuccess: (settings) => {
      queryClient.setQueryData<Settings>(queryKeys.settings, settings)
    },
  })
}

// ---------------------------------------------------------------------------
// §7.9 Cache (FR-THM-008)
// ---------------------------------------------------------------------------

export function useCacheUsage(options: EnabledOption = {}): UseQueryResult<CacheUsage> {
  return useQuery({
    queryKey: queryKeys.cache.usage,
    queryFn: ({ signal }) => getCacheUsage({ signal }),
    enabled: options.enabled ?? true,
    retry: retryQuery,
  })
}

export function usePurgeCache(): UseMutationResult<CachePurgeResult, Error, PurgeKind> {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (kind: PurgeKind) => purgeCache(kind),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.cache.usage })
      // Every cover and thumbnail just became a 202 again.
      void queryClient.invalidateQueries({ queryKey: queryKeys.image.all })
    },
  })
}

// ---------------------------------------------------------------------------
// §7.10 Scan (FR-IDX-004)
// ---------------------------------------------------------------------------

export function useScanStatus(options: EnabledOption = {}): UseQueryResult<ScanStatus> {
  return useQuery({
    queryKey: queryKeys.scan.status,
    queryFn: ({ signal }) => getScanStatus({ signal }),
    enabled: options.enabled ?? true,
    retry: retryQuery,
    staleTime: 0,
    refetchInterval: (query) =>
      query.state.data !== undefined && query.state.data.state !== 'idle' ? SCAN_POLL_MS : false,
  })
}

export function useScanLog(
  params: ScanLogParams = {},
  options: EnabledOption = {},
): UseQueryResult<ScanLogResponse> {
  return useQuery({
    queryKey: queryKeys.scan.log(params),
    queryFn: ({ signal }) => getScanLog(params, { signal }),
    enabled: options.enabled ?? true,
    retry: retryQuery,
  })
}

export function useStartScan(): UseMutationResult<ScanRunResponse, Error, ScanStartRequest> {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (request: ScanStartRequest) => startScan(request),
    onSuccess: () => {
      // Re-arms the 1 s poll: the refetch sees a non-idle state.
      void queryClient.invalidateQueries({ queryKey: queryKeys.scan.status })
    },
  })
}

export function useCancelScan(): UseMutationResult<void, Error, void> {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: () => cancelScan(),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.scan.status })
    },
  })
}

/**
 * Everything a finished scan can have changed, and nothing else.
 *
 *  * `series` — the run is what adds, removes and re-counts them, and the
 *    covers it generated arrive as a new `cover_cv` *inside* the series
 *    payload, so the image keys (which carry `cover_cv`) turn over on their own
 *    once this lands. Invalidating `image.all` here would instead re-request
 *    every cover on screen for a byte-identical answer.
 *  * `roots` — `series_count`, `total_bytes`, `last_scan_at` and
 *    `last_scan_error` are all written by the run (arch §7.4).
 *  * `continue` — a rescan can drop the book a card points at, or move a
 *    series' latest volume (E-37).
 *  * `scan.logs` — the run's warnings and errors only exist here. This is the
 *    key `['scan','status']` never matched.
 *
 * `settings` is deliberately absent: a scan writes none of it.
 */
function invalidateAfterScan(queryClient: QueryClient): void {
  void queryClient.invalidateQueries({ queryKey: queryKeys.series.all })
  void queryClient.invalidateQueries({ queryKey: queryKeys.roots })
  void queryClient.invalidateQueries({ queryKey: queryKeys.continueReading.all })
  void queryClient.invalidateQueries({ queryKey: queryKeys.scan.logs })
}

/**
 * Re-reads the library when a scan run **finishes** (FR-IDX-004's other half).
 *
 * Nothing else in the app watches for this. `useStartScan` invalidates
 * `['scan','status']` — which only re-arms the 1 s poll — and the poll itself
 * writes the status into the cache and stops. So the grid, the sidebar counts,
 * the root rows and the scan log all stayed on whatever they held *before* the
 * run, and a user who rescanned a root watched a progress bar reach 100 % and
 * then watched nothing change.
 *
 * ## Why it fires exactly once, and never on mount
 *
 * The transition is read from the **previous state this hook observed**, held
 * in a ref, not from the payload:
 *
 *  * `previous.current` starts as `null` — *nothing observed yet*, which is a
 *    third value and not a synonym for `idle`. The first payload only ever
 *    records itself, so a mount against an already-idle server cannot fire.
 *    That is the case a `state === 'idle'` test alone would get wrong on every
 *    page load, and there is one on every page load.
 *  * The edge is `non-idle → idle`. After it fires, `previous.current` is
 *    `idle`, so every later poll of the same idle status takes the early
 *    return. One completed run, one invalidation.
 *  * The effect is keyed on the `status` **object**, and TanStack Query's
 *    structural sharing keeps that reference stable while the payload is
 *    unchanged — so an idle server that is polled again does not even re-run
 *    the effect. Under StrictMode's double-invoke it does re-run, with the same
 *    object and `previous.current` already `idle`, and returns early. Refs
 *    survive that remount, which is what makes the guard hold there too.
 *
 * `run_id` is not consulted, and cannot be: arch §7.10 keeps it set after the
 * run ends, so a finished run and the run before it are indistinguishable by
 * id. `state` is the only edge on the wire.
 *
 * Mount it **once** — `ShellDataProvider` — for the same reason: one mount, one
 * invalidation. A second copy would double every refetch.
 */
export function useScanCompletionRefresh(status: ScanStatus | undefined): void {
  const queryClient = useQueryClient()
  const previous = useRef<ScanState | null>(null)

  useEffect(() => {
    if (status === undefined) return
    const before = previous.current
    previous.current = status.state
    // `null` is "first payload", not "was idle": firing here would refetch the
    // whole library on every cold start.
    if (before === null || before === 'idle') return
    if (status.state !== 'idle') return
    invalidateAfterScan(queryClient)
  }, [queryClient, status])
}

// ---------------------------------------------------------------------------
// §7.12 Auth (NFR-SEC-002)
// ---------------------------------------------------------------------------

/**
 * Subscribes a `QueryClient` to 401s: any request that comes back unauthorized marks the
 * auth status stale, so the shell re-reads it and renders the login screen. Returns the
 * unsubscribe function.
 */
export function installUnauthorizedInvalidation(queryClient: QueryClient): () => void {
  return subscribeUnauthorized(() => {
    void queryClient.invalidateQueries({ queryKey: queryKeys.auth.status })
  })
}

export function useAuthStatus(options: EnabledOption = {}): UseQueryResult<AuthStatus> {
  const queryClient = useQueryClient()
  useEffect(() => installUnauthorizedInvalidation(queryClient), [queryClient])
  return useQuery({
    queryKey: queryKeys.auth.status,
    queryFn: ({ signal }) => getAuthStatus({ signal }),
    enabled: options.enabled ?? true,
    retry: retryQuery,
  })
}

export function useLogin(): UseMutationResult<void, Error, string> {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (password: string) => login(password),
    onSuccess: () => {
      void queryClient.invalidateQueries()
    },
  })
}

export function useLogout(): UseMutationResult<void, Error, void> {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: () => logout(),
    onSuccess: () => {
      queryClient.clear()
    },
  })
}

// ---------------------------------------------------------------------------
// Images: covers and page thumbnails, where 202 is normal
// ---------------------------------------------------------------------------

/** `queued` means the server answered `202` and a retry is scheduled. */
export type ImageStatus = 'loading' | 'queued' | 'ready' | 'unavailable'

export interface ImageState {
  status: ImageStatus
  /** The versioned URL to hand to `<img src>`; valid only when `status === "ready"`. */
  url: string
  /** Set when the endpoint answered `404`/`422`/`409`; `null` while queued or ready. */
  error: ApiError | null
}

interface ImageResourceOptions extends EnabledOption {
  /** When set, a `409 stale_version` invalidates this book before the single retry. */
  bookId?: ID
}

function useImageResource(
  queryKey: readonly unknown[],
  url: string,
  options: ImageResourceOptions,
): ImageState {
  const queryClient = useQueryClient()
  const bookId = options.bookId
  const query = useQuery({
    queryKey,
    queryFn: async ({ signal }) => {
      try {
        const result = await fetchImage(url, { signal })
        if (result.state === 'queued') throw new ImageQueuedError(result.retryAfterMs)
        return result.url
      } catch (error) {
        if (bookId !== undefined && isApiError(error) && error.code === 'stale_version') {
          void queryClient.invalidateQueries({ queryKey: queryKeys.books.detail(bookId) })
        }
        throw error
      }
    },
    enabled: options.enabled ?? true,
    staleTime: Infinity,
    // `failureCount` is 0 on the first retry decision, so `< N - 1` caps the total
    // number of requests at N.
    retry: (failureCount: number, error: Error) => {
      if (isImageQueuedError(error)) return failureCount < MAX_IMAGE_ATTEMPTS - 1
      if (isApiError(error) && error.code === 'stale_version') {
        return failureCount < STALE_VERSION_RETRIES
      }
      return false
    },
    retryDelay: (_attempt: number, error: Error) =>
      isImageQueuedError(error) ? error.retryAfterMs : DEFAULT_RETRY_AFTER_MS,
  })

  if (query.data !== undefined) return { status: 'ready', url: query.data, error: null }
  if (query.isError) {
    return {
      status: 'unavailable',
      url,
      error: isApiError(query.error) ? query.error : null,
    }
  }
  const queued = isImageQueuedError(query.failureReason)
  return { status: queued ? 'queued' : 'loading', url, error: null }
}

export interface CoverImageOptions extends EnabledOption {
  /** Always an explicit width from `THUMB_WIDTHS` (§0.4). */
  w: ThumbWidth
  /** The series' `cover_cv`. */
  v: string | null
}

/**
 * FR-THM-003 / FR-LIB-008. `status === "queued"` while the cover is being generated and
 * `"unavailable"` once the retries are exhausted or the series has no cover — both mean
 * "render the striped fallback", which sits beneath the image and never shifts layout.
 */
export function useCoverImage(sid: ID, options: CoverImageOptions): ImageState {
  const url = seriesCoverUrl(sid, { w: options.w, v: options.v })
  return useImageResource(queryKeys.image.cover(sid, options.w, options.v), url, {
    enabled: options.enabled ?? true,
  })
}

export interface ThumbImageOptions extends EnabledOption {
  w: ThumbWidth
  /** The book's `cv`. */
  v: string | null
}

/** FR-VWR-008: the virtualised thumbnail strip, one hook per visible page. */
export function usePageThumbImage(bid: ID, n: number, options: ThumbImageOptions): ImageState {
  const url = pageThumbUrl(bid, n, { w: options.w, v: options.v })
  return useImageResource(queryKeys.image.thumb(bid, n, options.w, options.v), url, {
    enabled: options.enabled ?? true,
    bookId: bid,
  })
}
