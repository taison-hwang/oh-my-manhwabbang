/**
 * TanStack Query v5 hooks over `client.ts` — the only way the app reads or writes server
 * state (impl-plan §5.2: "No `useEffect` + `fetch`"). Query keys are exported arrays.
 *
 * Policies encoded here (WP-06 acceptance 4):
 *  * `useScanStatus` polls every 1 000 ms while `state !== "idle"` and stops otherwise (C-11).
 *  * `useScanCompletionRefresh` re-reads the library once per completed run, off that poll.
 *  * A `202` on an image endpoint retries after `Retry-After`, at most 10 attempts, then
 *    reports `unavailable` so the caller falls back to the FR-LIB-008 placeholder.
 *  * A `409 stale_version` re-reads the book query — with a read that starts after the
 *    409 (`rereadAfterStale`) — and retries once (a book cover also re-reads its series,
 *    which is where a volume card's `cover_cv` comes from — E-69).
 *  * A `401` invalidates the auth status, which flips the shell to the login screen.
 *  * `useSaveProgress` debounces 1 s and is idempotent.
 *  * Settings writes are ordered by `settingsWriter.ts`; a `GET /api/settings` keeps the
 *    choices that writer has not seen confirmed yet (`useSettings`).
 */

import {
  keepPreviousData,
  MutationObserver,
  QueryClient,
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
  type Query,
  type QueryKey,
  type UseInfiniteQueryResult,
  type UseMutationResult,
  type UseQueryResult,
} from '@tanstack/react-query'
import { useCallback, useEffect, useRef } from 'react'

import {
  cancelScan,
  createRoot,
  deleteBookCover,
  deleteProgress,
  deleteRoot,
  exportProgress,
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
  importProgress,
  listSeries,
  login,
  logout,
  purgeCache,
  putBookCover,
  putBookPrefs,
  putProgress,
  putSeriesPrefs,
  rescanSeries,
  startScan,
  subscribeUnauthorized,
  DEFAULT_RETRY_AFTER_MS,
} from './client'
import { type ApiError, ImageQueuedError, isApiError, isImageQueuedError } from './errors'
import type {
  AuthStatus,
  BookCover,
  BookCoverResult,
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
  ReadingDir,
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
  SeriesPrefs,
  Settings,
} from './types'
import { SERIES_LIST_DEFAULT_LIMIT } from './types'
import { SETTINGS_QUERY_KEY, settingsReadMark, withUnsavedSettings } from './settingsWriter'
import {
  assertPageNumber,
  bookCoverUrl,
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

/**
 * The app's one `QueryClient` configuration: `main.tsx` boots with it, and a
 * test that means "under the app's defaults" builds its client here rather than
 * copying them.
 */
export function createQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: {
        retry: retryQuery,
        // A single-user LAN tool; window focus is not a signal that anything
        // changed, and the viewer refetching on every alt-tab is user-hostile.
        refetchOnWindowFocus: false,
        // The index is a local SQLite file behind a localhost server: refetching
        // is cheap, but a 30 s window still collapses the storm of duplicate
        // requests a grid of cards would otherwise make.
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
  settings: SETTINGS_QUERY_KEY,
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
    /** E-69 §2.6 — keyed by `cover_cv`, so a new choice or direction is a new entry. */
    bookCover: (bid: ID, w: number, v: string | null) => ['image', 'bookCover', bid, w, v] as const,
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

/**
 * Everything a book cover's `cover_cv` rides on (E-69 §2.6/§2.8): every `books` key
 * (detail and prefs), every `series` key (the volume list, and the series cover that
 * `cover_kind = 'page'` takes from its cover volume) and 이어보기. The image keys carry
 * `cover_cv`, so they turn over by themselves once these land — `image.all` is left alone.
 * Fire-and-forget, as `useImportProgress` does (E-68 §2.6). The `.seg`, the cover writes
 * and each seed the boot-time upload has the server take (`store/seriesDir.ts`) use it.
 */
export function invalidateCoverFamilies(queryClient: QueryClient): void {
  void queryClient.invalidateQueries({ queryKey: queryKeys.books.all })
  void queryClient.invalidateQueries({ queryKey: queryKeys.series.all })
  void queryClient.invalidateQueries({ queryKey: queryKeys.continueReading.all })
}

/**
 * The one queue every write of a series' direction goes through: the `.seg`
 * (`useSetSeriesDirection`) and the boot-time upload of the old localStorage seed
 * (`store/seriesDir.ts`, E-69 §2.3). In one TanStack mutation scope, a step runs
 * after every write asked for before it and before every one asked for after it,
 * so the upload's GET → "still null?" → PUT cannot have a reader's choice land in
 * the middle, nor overtake one.
 */
export const SERIES_PREFS_SCOPE = { id: 'series-prefs' } as const

/** The series whose direction the reader has asked to write, per client — noted at `mutate`. */
const seriesDirectionAsks = new WeakMap<QueryClient, Set<ID>>()

/**
 * Whether the reader has asked, on this client, for a direction of `sid` — noted
 * when the write is asked for, not when the queue reaches it. A seed may not go
 * over that: E-69 §2.3 uploads one only where nobody chose, and a `null` the
 * server holds may be the reader's own 기본.
 */
export function seriesDirectionAsked(client: QueryClient, sid: ID): boolean {
  return seriesDirectionAsks.get(client)?.has(sid) ?? false
}

/** Runs `step` in `SERIES_PREFS_SCOPE`, in its turn among the `.seg`'s writes. */
export function inSeriesPrefsQueue<T>(client: QueryClient, step: () => Promise<T>): Promise<T> {
  const observer = new MutationObserver<T, Error, void>(client, {
    scope: SERIES_PREFS_SCOPE,
    mutationFn: step,
  })
  // `reset` lets go of the mutation, so the cache can collect it once it settles.
  return observer.mutate().finally(() => {
    observer.reset()
  })
}

export interface SetSeriesDirectionVariables {
  sid: ID
  /** `null` clears the series' own direction; its books then fall to the global default. */
  reading_direction: ReadingDir | null
}

/**
 * `PUT /api/series/{sid}/prefs` — the series-detail `.seg` (ruling E-69 §2.3).
 *
 * The answer is written into that series' detail at once, so the segment shows the
 * server's value without waiting for a refetch. Then everything the direction moves is
 * invalidated: each book of the series now opens on another *effective* direction
 * (`BookPrefs.reading_direction` is the book → series → global ladder), and every cover
 * without a chosen region turned over its `cover_cv` (§2.7) — hence the three families,
 * which include `books.prefs` for every book of the series.
 *
 * `scope` serialises the writes: two clicks in a row reach the server in the order they
 * were made, so the later choice is the one that stays. The seed upload takes the same
 * queue (`SERIES_PREFS_SCOPE`) and stands down for a series the reader asked to write.
 */
export function useSetSeriesDirection(): UseMutationResult<
  SeriesPrefs,
  Error,
  SetSeriesDirectionVariables
> {
  const queryClient = useQueryClient()
  return useMutation({
    scope: SERIES_PREFS_SCOPE,
    // `onMutate` runs when the write is asked for, before the scope lets it go.
    onMutate: ({ sid }: SetSeriesDirectionVariables) => {
      let asked = seriesDirectionAsks.get(queryClient)
      if (asked === undefined) {
        asked = new Set()
        seriesDirectionAsks.set(queryClient, asked)
      }
      asked.add(sid)
    },
    mutationFn: ({ sid, reading_direction }: SetSeriesDirectionVariables) =>
      putSeriesPrefs(sid, { reading_direction }),
    onSuccess: (prefs, { sid }) => {
      queryClient.setQueryData<SeriesDetail>(queryKeys.series.detail(sid), (old) =>
        old === undefined ? undefined : { ...old, reading_direction: prefs.reading_direction },
      )
      invalidateCoverFamilies(queryClient)
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
  /**
   * **E-45 §2** — carries `stale_seen: true` on the next write and sends it now.
   *
   * Separate from `save` because it says a different thing. `save` reports where
   * the reader is; this reports that `파일이 변경되었습니다` was on screen for its
   * whole `STALE_NOTICE_MS`, which is the one fact that lets the server re-take
   * its `page_count` baseline. Deriving it from a page turn is the §6.5 shape
   * this repository keeps paying for — the check would end up watching the thing
   * *next to* the notice rather than the notice.
   *
   * It **takes over** the debounce buffer rather than racing it, so a page
   * turned inside the last second and the acknowledgement leave as **one**
   * request. That much is a path and is asserted (`ViewerPage.test.tsx`, "carries
   * a page turned inside the debounce out with the acknowledgement"): the
   * obvious alternative — acknowledge with its own `mutate` and leave the buffer
   * alone — sends two writes with the bare page landing *after* the flag.
   *
   * The field-level merge inside it is a **guard, not a path**; see the note at
   * the implementation for why nothing distinguishable can be in that buffer.
   */
  acknowledgeStale: (page: number) => void
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

  const acknowledgeStale = useCallback(
    (page: number) => {
      assertPageNumber(page)
      // A buffered write for *another* book is that book's page, and replacing
      // it in the buffer would lose it (D-13 / NFR-DAT-004). It should never be
      // here — the `bid` effect below flushes on every volume change — so this
      // is a guard rather than a path, and it sends rather than discards.
      if (pending.current !== null && pending.current.bid !== bid) flush()
      // **Both of the lines below are guards, and only one of them is a path.**
      //
      // Replacing the buffer *is* the path: whatever is in it must not simply be
      // dropped, and a second `mutate` beside it would put two writes on the
      // wire in an undefined order. That is asserted.
      //
      // Spreading the buffered fields is the guard, and it is measured to be an
      // identity today — remove it and the whole suite stays green. The only
      // thing that can be buffered when this runs is a `{page}` from the effect
      // in `useProgressSync`, and that page is *this* page: both read the same
      // render's `page`, and `setCompleted`, the one caller that would add a
      // second field, flushes on the spot. The spread is kept because it costs
      // nothing and states the intent — the acknowledgement adds a flag to the
      // reader's write, it does not replace it — but it is not defended, and a
      // comment claiming otherwise is the §6.5 shape this repository keeps
      // paying for. If a future caller can buffer `completed`, this becomes a
      // path and needs a case of its own.
      const buffered = pending.current
      pending.current = {
        bid,
        update: buffered === null ? { page, stale_seen: true } : { ...buffered.update, stale_seen: true },
      }
      flush()
    },
    [bid, flush],
  )

  // Flushes on unmount *and* whenever `bid` changes, so the previous book's pending
  // write goes out before the next book's `save` can replace it in the buffer.
  useEffect(() => flush, [flush, bid])

  return { save, acknowledgeStale, flush, mutation }
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

/**
 * The mutation scope of one book's cover writes. 표지로 지정 and 기본 표지로 되돌리기
 * share it, so a reset asked for while a choice is on its way is sent after the
 * choice has been answered — never beside it, where the server could apply the two
 * in either order and keep the region the reader just took back. Another book's
 * writes are another scope and do not wait.
 */
export function bookCoverScope(bid: ID): { id: string } {
  return { id: `book-cover:${bid}` }
}

/**
 * `PUT /api/books/{bid}/cover` — the 표지로 dialog's 표지로 지정 (ruling E-69 §2.8).
 * A success invalidates `books`, `series` and 이어보기; a refusal (`400` for a region
 * outside §2.4's rules or a book that is not `ok`) invalidates nothing.
 *
 * The book is the hook's, not the write's: a TanStack scope is fixed per hook, and the
 * order this has to keep is per book (`bookCoverScope`). A write already asked for
 * keeps the book it was asked for, even if the viewer moves on to the next volume.
 */
export function useSetBookCover(bid: ID): UseMutationResult<BookCoverResult, Error, BookCover> {
  const queryClient = useQueryClient()
  return useMutation({
    scope: bookCoverScope(bid),
    mutationFn: (cover: BookCover) => putBookCover(bid, cover),
    onSuccess: () => {
      invalidateCoverFamilies(queryClient)
    },
  })
}

/**
 * `DELETE /api/books/{bid}/cover` — 기본 표지로 되돌리기 (E-69 §2.8); `204` even with no
 * choice. In `useSetBookCover`'s scope for the same book, so the two keep their order.
 */
export function useResetBookCover(bid: ID): UseMutationResult<void, Error, void> {
  const queryClient = useQueryClient()
  return useMutation({
    scope: bookCoverScope(bid),
    mutationFn: () => deleteBookCover(bid),
    onSuccess: () => {
      invalidateCoverFamilies(queryClient)
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

/**
 * The mark is taken when the GET is sent: a field a `PUT` answered for after that
 * keeps its written value, because the GET may have been read before the `PUT`
 * was applied (`settingsWriter.ts`).
 */
export function useSettings(options: EnabledOption = {}): UseQueryResult<Settings> {
  const queryClient = useQueryClient()
  return useQuery({
    queryKey: queryKeys.settings,
    queryFn: async ({ signal }) => {
      const mark = settingsReadMark()
      return withUnsavedSettings(queryClient, await getSettings({ signal }), mark)
    },
    enabled: options.enabled ?? true,
    retry: retryQuery,
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

/**
 * The scan log, newest first, read **incrementally** after the first page.
 *
 * `follow` re-reads it every `SCAN_POLL_MS` — the caller passes "a run is in
 * progress" off the status poll, so outside a run this sets no timer (D-16's
 * budget during a run becomes the status poll plus this one, and one more in a
 * second that wrote more than a page — see `readScanLog`; HANDOFF `ab`).
 * The end of a run needs no special case here: `useScanCompletionRefresh`
 * invalidates `scan.logs`, and that refetch is incremental like any other, so
 * it is what brings in the tail written after the last poll.
 */
export function useScanLog(
  params: ScanLogParams = {},
  options: EnabledOption & { follow?: boolean } = {},
): UseQueryResult<ScanLogResponse> {
  const queryClient = useQueryClient()
  const queryKey = queryKeys.scan.log(params)
  return useQuery({
    queryKey,
    queryFn: ({ signal }) =>
      readScanLog(params, queryClient.getQueryData<ScanLogResponse>(queryKey), signal),
    enabled: options.enabled ?? true,
    retry: retryQuery,
    refetchInterval: options.follow === true ? SCAN_POLL_MS : false,
  })
}

/** arch §7.10: `limit` defaults to 200 on the wire. */
const SCAN_LOG_WIRE_LIMIT = 200

/**
 * One read of the log, given what the cache already holds (newest first).
 *
 * `index.ListLog` answers newest-first without `since_id` and **oldest-first**
 * after it, `limit` rows either way. So the rows after the newest one held are
 * fetched, reversed and put on top, and the page is cut back to `limit`. A
 * reply that fills the whole page may not be all of them — the run wrote more
 * than a page since the last read — and those rows are the *oldest* of the
 * new ones, i.e. already behind; the newest page is read instead, which is the
 * one this view shows.
 *
 * A caller that passes its own `since_id` gets the server's answer as it is —
 * oldest first — and none of this.
 */
async function readScanLog(
  params: ScanLogParams,
  held: ScanLogResponse | undefined,
  signal: AbortSignal,
): Promise<ScanLogResponse> {
  const newest = held?.items[0]?.id
  if (held === undefined || newest === undefined || params.since_id !== undefined) {
    return getScanLog(params, { signal })
  }
  const limit = params.limit ?? SCAN_LOG_WIRE_LIMIT
  const fresh = await getScanLog({ ...params, since_id: newest }, { signal })
  if (fresh.items.length >= limit) return getScanLog(params, { signal })
  const added = fresh.items.filter((item) => item.id > newest).reverse()
  if (added.length === 0) return held
  return { items: [...added, ...held.items].slice(0, limit) }
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
  // Cancelled first: an invalidation does not cancel an in-flight fetch of a
  // query that has no data yet — it returns that same promise — so a log read
  // the server answered before the run's last rows would stand as the final
  // one, with nothing polling after it (HANDOFF `ab`).
  void queryClient
    .cancelQueries({ queryKey: queryKeys.scan.logs })
    .then(() => queryClient.invalidateQueries({ queryKey: queryKeys.scan.logs }))
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
// §7.11 Progress export / import (FR-STT-004, ruling E-68)
// ---------------------------------------------------------------------------

/**
 * `GET /api/progress/export`, as a mutation: it is a file the user asks for
 * with a click (the settings dialog's 읽기 기록), not state a screen reads, so
 * nothing caches it and nothing refetches it.
 */
export function useExportProgress(): UseMutationResult<ProgressExport, Error, void> {
  return useMutation({ mutationFn: () => exportProgress() })
}

export interface ImportProgressVariables {
  payload: ProgressExport
  /** Omitted for merge, the server's default; `'replace'` overwrites. */
  strategy?: ImportStrategy
}

/**
 * `POST /api/progress/import`. A success can have rewritten any book's
 * progress and prefs, so it invalidates every `books` key (detail and prefs),
 * every `series` key (their read counts) and 이어보기. A refusal invalidates
 * nothing: the import is one transaction (`internal/userdata/export.go`).
 */
export function useImportProgress(): UseMutationResult<
  ProgressImportResult,
  Error,
  ImportProgressVariables
> {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({ payload, strategy }: ImportProgressVariables) =>
      importProgress(payload, strategy),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.books.all })
      void queryClient.invalidateQueries({ queryKey: queryKeys.series.all })
      void queryClient.invalidateQueries({ queryKey: queryKeys.continueReading.all })
    },
  })
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

/**
 * The auth status as the shell last read it, **without fetching it**. Panels that only
 * hide an admin control use this: the shell owns the request, and a panel that fetched
 * on its own would add a request to every screen that merely mentions a button.
 * `undefined` until the shell has the answer — callers treat that as "allowed".
 */
export function useCachedAuthStatus(): AuthStatus | undefined {
  return useQuery({
    queryKey: queryKeys.auth.status,
    queryFn: ({ signal }) => getAuthStatus({ signal }),
    enabled: false,
  }).data
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

/**
 * The queries owed a read that starts after a `409 stale_version` — see `rereadAfterStale`.
 * Keyed by the `Query` itself, so per client, and let go with it.
 */
const staleRereadsOwed = new WeakSet<Query>()

/**
 * Re-reads `queryKey` because a `409 stale_version` said the version it handed an image
 * is gone (E-69 §2.7) — with a read that **starts after the 409**.
 *
 *  * Nothing on the wire: invalidate, which starts that read now (or, for a query
 *    nobody is watching, on its next mount).
 *  * A read on the wire began before this 409 and may answer the very version the server
 *    just refused — it was asked before the change. Joining it (`cancelRefetch: false`)
 *    would land that version again; the image's one retry is refused on it too, nothing
 *    is left to re-read, and the card stays striped. Cancelling it instead restarts the
 *    read once per 409, so a grid that went stale all at once would ask once a card. So
 *    it is let finish, and then the query is invalidated once more.
 *  * That second read is owed once per query: every 409 that comes back before it starts
 *    is answered by it. A grid of N stale covers costs one or two reads, not N.
 */
function rereadAfterStale(client: QueryClient, queryKey: QueryKey): void {
  const cache = client.getQueryCache()
  const query = cache.find({ queryKey, exact: true })
  // Not in the cache: whoever mounts it next reads it from the server anyway.
  if (query === undefined || staleRereadsOwed.has(query)) return
  if (query.state.fetchStatus === 'idle') {
    void client.invalidateQueries({ queryKey, exact: true }, { cancelRefetch: false })
    return
  }
  staleRereadsOwed.add(query)
  const unsubscribe = cache.subscribe((event) => {
    if (event.query !== query) return
    if (event.type === 'removed') {
      unsubscribe()
      staleRereadsOwed.delete(query)
      return
    }
    if (query.state.fetchStatus !== 'idle') return
    unsubscribe()
    // After the cache has finished telling everyone that read settled, not inside it.
    queueMicrotask(() => {
      staleRereadsOwed.delete(query)
      void client.invalidateQueries({ queryKey, exact: true }, { cancelRefetch: false })
    })
  })
}

interface ImageResourceOptions extends EnabledOption {
  /** When set, a `409 stale_version` invalidates this book before the single retry. */
  bookId?: ID
  /** And this series' detail — where a volume card read its `cover_cv` (E-69). */
  seriesId?: ID
}

function useImageResource(
  queryKey: readonly unknown[],
  url: string,
  options: ImageResourceOptions,
): ImageState {
  const queryClient = useQueryClient()
  const bookId = options.bookId
  const seriesId = options.seriesId
  const query = useQuery({
    queryKey,
    queryFn: async ({ signal }) => {
      try {
        const result = await fetchImage(url, { signal })
        if (result.state === 'queued') throw new ImageQueuedError(result.retryAfterMs)
        return result.url
      } catch (error) {
        if (isApiError(error) && error.code === 'stale_version') {
          // A read that starts after this 409, shared by every 409 waiting for one: a
          // grid of covers that all went stale at once (a direction change from another
          // tab) asks for the series once or twice, not once each.
          if (bookId !== undefined) rereadAfterStale(queryClient, queryKeys.books.detail(bookId))
          if (seriesId !== undefined) {
            rereadAfterStale(queryClient, queryKeys.series.detail(seriesId))
          }
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

export interface BookCoverImageOptions extends EnabledOption {
  /** Always an explicit width from `THUMB_WIDTHS` (§0.4). */
  w: ThumbWidth
  /** The book's `cover_cv` — not its content `cv` (E-69 §2.7). */
  v: string | null
  /**
   * The series the caller read `cover_cv` from. A `409 stale_version` re-reads it, as well
   * as the book: the cover's version moves on things this tab did not do (a global
   * direction change, another browser's choice), and the same url retried cannot recover.
   */
  seriesId?: ID
}

/**
 * `GET /api/books/{bid}/cover` — a volume card's picture (ruling E-69 §2.6, §2.8). The
 * series cover's behaviour exactly: `202` retries after `Retry-After`, `404` (a book with
 * no pages) and `422` are `unavailable` at once, `409 stale_version` re-reads and retries
 * once. `unavailable` means "keep the striped placeholder".
 */
export function useBookCoverImage(bid: ID, options: BookCoverImageOptions): ImageState {
  const url = bookCoverUrl(bid, { w: options.w, v: options.v })
  return useImageResource(queryKeys.image.bookCover(bid, options.w, options.v), url, {
    enabled: options.enabled ?? true,
    bookId: bid,
    ...(options.seriesId === undefined ? {} : { seriesId: options.seriesId }),
  })
}
