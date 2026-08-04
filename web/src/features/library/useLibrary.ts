/**
 * The library screen's data seam (WP-09).
 *
 * Everything here is either a **pure function of UI state** — which is what makes
 * the sort, scope and search rules testable without a DOM — or a hook that turns
 * that state into one of the WP-06 query hooks. No `fetch`, no `useEffect`
 * fetching, no server data in Zustand (impl-plan §5.2).
 *
 * Two rules travel with this file:
 *
 *  1. **Search is server-side** (C-10). With 963–10 000 series and FR-LIB-007's
 *     virtualised list the client never holds the catalogue, so filtering here
 *     would be wrong by construction. `lib/chosung.ts` survives only to
 *     highlight *which* part of a returned title matched — `highlightParts`.
 *  2. **Grid geometry is duplicated from `tokens.css` on purpose.** The token
 *     layer stays authoritative for paint, but a virtualised grid has to know
 *     its own column count in JavaScript to slice items into rows, and
 *     `repeat(auto-fill, …)` does not report one. `GRID_METRICS` mirrors the
 *     `--grid-min` / `--grid-gap` block and `useLibrary.test.ts` parses
 *     `tokens.css` to assert the two never drift.
 */

import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type RefObject,
} from 'react'

import {
  useContinue,
  useRoots,
  useSaveSettings,
  useSeriesListInfinite,
  useSettings,
} from '../../api/queries'
import type {
  ContinueItem,
  ProgressFilter,
  SeriesListParams,
  SeriesSummary,
  SortOrder,
} from '../../api/types'
import { SORT_KEYS } from '../../api/types'
import { THUMB_WIDTH_FOR, type ThumbWidth } from '../../api/urls'
import { matchRange } from '../../lib/chosung'
import type { Breakpoint } from '../../lib/useMediaQuery'
import { useUiStore, type PersistedUi, type Scope, type SortKey, type ViewMode } from '../../store/ui'

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

/** C-10: the top-bar field debounces into `GET /api/series?q=` at 150 ms. */
export const LIBRARY_SEARCH_DEBOUNCE_MS = 150

/** One page of the infinite list; `SERIES_LIST_DEFAULT_LIMIT` on the wire. */
export const LIBRARY_PAGE_SIZE = 60

/** ui-spec §4.3: the 이어보기 track shows at most five cards. */
export const CONTINUE_MAX_CARDS = 5

/** The four sidebar entries that are not a root name (ui-spec §4.1). */
export const SMART_SCOPES = ['all', 'reading', 'added', 'done'] as const
export type SmartScope = (typeof SMART_SCOPES)[number]

export function isSmartScope(scope: Scope): scope is SmartScope {
  return (SMART_SCOPES as readonly string[]).includes(scope)
}

/**
 * Amendment A-4 — the `progress=` value behind each smart list, or `undefined`
 * where the list is not a progress question at all.
 *
 * 최근 추가 is one of those: amendment **A-8** (ruling E-9) gives it a filter of
 * its own, `scope=added`, so it says nothing about progress. The pre-A-8
 * `added: 'any'` workaround is gone — it made 최근 추가 a pure re-ordering of the
 * whole library, which is exactly the "visibly wrong number" E-9 was raised to
 * fix.
 */
const SCOPE_PROGRESS: Record<SmartScope, ProgressFilter | undefined> = {
  all: undefined,
  reading: 'reading',
  added: undefined,
  done: 'done',
}

const SMART_SCOPE_LABEL: Record<SmartScope, string> = {
  all: '전체 시리즈',
  reading: '읽는 중',
  added: '최근 추가',
  done: '완독',
}

/** The sortable list columns, in ui-spec §4.5 order, with their wire keys. */
export const LIST_SORT_COLUMNS = [
  { key: 'name', label: '시리즈명' },
  { key: 'books', label: '권' },
  { key: 'size', label: '용량' },
  { key: 'mtime', label: '수정일' },
] as const satisfies readonly { key: SortKey; label: string }[]

// ---------------------------------------------------------------------------
// Pure query-parameter rules
// ---------------------------------------------------------------------------

export interface LibraryParamsInput {
  scope: Scope
  sort: SortKey
  order: SortOrder
  /** Raw field text; trimmed here and dropped when empty. */
  query: string
}

/**
 * Turns the four pieces of UI state into `GET /api/series` parameters.
 *
 * The `added` smart list is the one scope that overrides the sort, and since
 * amendment **A-8** (ruling E-9) it is also the one that sends `scope=`: 최근
 * 추가 is a *filter* (`first_seen_at` inside `library.recently_added_days`) with
 * `sort=added&order=desc` as the ordering *within* it. Sending only the sort —
 * what this function did before A-8 — listed the entire library under a heading
 * that promises a window.
 */
export function libraryParams(input: LibraryParamsInput): SeriesListParams {
  const { scope, order, query } = input
  const added = scope === 'added'
  const params: SeriesListParams = {
    sort: added ? 'added' : input.sort,
    order: added ? 'desc' : order,
    limit: LIBRARY_PAGE_SIZE,
  }
  if (added) params.scope = 'added'

  if (isSmartScope(scope)) {
    const progress = SCOPE_PROGRESS[scope]
    if (progress !== undefined) params.progress = progress
  } else {
    // FR-LIB-005. Repeatable on the wire; the sidebar selects exactly one.
    params.root = [scope]
  }

  const q = query.trim()
  if (q !== '') params.q = q

  return params
}

/** The section header's scope name (ui-spec §4.4). A root shows its label. */
export function scopeLabel(scope: Scope, roots: readonly { name: string; label: string }[]): string {
  if (isSmartScope(scope)) return SMART_SCOPE_LABEL[scope]
  return roots.find((root) => root.name === scope)?.label ?? scope
}

// ---------------------------------------------------------------------------
// Search-match highlighting (FR-LIB-006, highlighting half of C-10)
// ---------------------------------------------------------------------------

export interface TitleParts {
  before: string
  /** Empty when the query did not match this title. */
  match: string
  after: string
}

/**
 * Splits a title around the span the query matched, in code points, so a 초성
 * query (`ㄱㄱ`) highlights the syllables it stands for (`군계`).
 */
export function highlightParts(title: string, query: string): TitleParts {
  const needle = query.trim()
  const range = needle === '' ? null : matchRange(title, needle)
  if (range === null) return { before: title, match: '', after: '' }
  const chars = Array.from(title)
  return {
    before: chars.slice(0, range[0]).join(''),
    match: chars.slice(range[0], range[1]).join(''),
    after: chars.slice(range[1]).join(''),
  }
}

// ---------------------------------------------------------------------------
// Grid geometry (FR-LIB-007)
// ---------------------------------------------------------------------------

export interface GridMetrics {
  /** `--grid-min` at this tier, in CSS px. */
  min: number
  /** `--grid-gap` at this tier, in CSS px. */
  gap: number
}

/** Mirrors the `--grid-min` / `--grid-gap` block of `styles/tokens.css`. */
export const GRID_METRICS: Record<Breakpoint, GridMetrics> = {
  mobile: { min: 150, gap: 12 },
  tablet: { min: 224, gap: 16 },
  laptop: { min: 150, gap: 16 },
  desktop: { min: 152, gap: 16 },
}

/** What `repeat(auto-fill, minmax(min, 1fr))` would produce at `width`. */
export function columnCount(width: number, metrics: GridMetrics): number {
  if (!Number.isFinite(width) || width <= 0) return 1
  return Math.max(1, Math.floor((width + metrics.gap) / (metrics.min + metrics.gap)))
}

/** The rendered width of one column once `1fr` has distributed the remainder. */
export function columnWidth(width: number, columns: number, gap: number): number {
  if (!Number.isFinite(width) || width <= 0 || columns <= 0) return 0
  return Math.max(0, (width - gap * (columns - 1)) / columns)
}

/**
 * Everything under the 2:3 cover in a card: the 7px gap, a two-line 12px title
 * and the 11px meta row — `7 + 2×(12×1.3) + 7 + 11×1.35 = 60.05`.
 *
 * It is a **constant, not an estimate**: `SeriesCard` and `GridSkeleton` both
 * pin their text block to exactly this height (and the title to exactly two
 * lines), so a one-line title produces the same card as a two-line one. Without
 * that pin the number is only true for half the cards, the virtualiser's
 * `estimateSize` drifts by a line per row, and the "gap" between rows becomes
 * `--grid-gap` plus whatever the estimate was wrong by.
 */
export const CARD_TEXT_HEIGHT = 60

/**
 * The exact rendered height of one card.
 *
 * Not rounded: the cover is an `aspect-ratio:2/3` border box, which the browser
 * lays out in subpixels, so rounding here would reintroduce up to half a pixel
 * of drift per row for no benefit.
 */
export function cardHeight(coverWidth: number): number {
  if (!Number.isFinite(coverWidth) || coverWidth <= 0) return CARD_TEXT_HEIGHT
  return coverWidth * 1.5 + CARD_TEXT_HEIGHT
}

/** impl-plan §0.4: 224 CSS px covers at the rail tier need `w=640`, the rest 400. */
export function gridCoverWidth(breakpoint: Breakpoint): ThumbWidth {
  return breakpoint === 'tablet' ? THUMB_WIDTH_FOR.gridCoverNarrow : THUMB_WIDTH_FOR.gridCoverWide
}

/** ui-spec §4.5: 45px rows (36px thumb + 4px padding + 1px).
 *  E-32 removed the rule that last pixel used to be; it is kept as the gap
 *  between two hover chips, which now need one. */
export const LIST_ROW_HEIGHT = 45
/** Below 768 the row becomes two lines (ui-spec §7). */
export const LIST_ROW_HEIGHT_STACKED = 60

export type ListLayout = 'full' | 'compact' | 'stacked'

/** ui-spec §7: 수정일 + 용량 drop at 768–1023; below 768 the row stacks. */
export function listLayoutFor(breakpoint: Breakpoint): ListLayout {
  if (breakpoint === 'mobile') return 'stacked'
  if (breakpoint === 'tablet') return 'compact'
  return 'full'
}

export const LIST_TEMPLATE: Record<ListLayout, string> = {
  full: '32px minmax(0,1fr) 66px 64px 78px 100px 148px',
  compact: '32px minmax(0,1fr) 66px 64px 120px',
  stacked: '32px minmax(0,1fr)',
}

/**
 * The list's column-header band, as two class strings shared by `SeriesList`
 * and `GridSkeleton`.
 *
 * The loaded list prepends a header the skeleton must also reserve, or the
 * whole table jumps down by the band's height the instant data lands — which is
 * the layout shift WP-09 acceptance 9 forbids and which the Layout Instability
 * API does **not** report, because the skeleton nodes are removed and different
 * nodes inserted rather than moved. The band is therefore defined once, here,
 * and `library.test.tsx` asserts the two elements carry the identical string.
 */
export const LIST_HEADER_WRAPPER_CLASS = 'px-2 pt-2'
export const LIST_HEADER_BAND_CLASS =
  'grid items-center gap-3 border-b-2 border-rule-strong p-2 text-xs uppercase tracking-[.08em] text-ink-dim'

/**
 * The list view's **card** (E-32): `--radius-lg`, `--color-surface`,
 * `--shadow-md`, 8px of padding with 14 at the bottom.
 *
 * Shared by `SeriesList` and `GridSkeleton` for the same reason the band above
 * is: the two have to occupy one geometry, and a card on the loaded list alone
 * would move every row by its own margin the moment data arrived. The column
 * header keeps its 2px underline — E-32 removes the *row* dividers, not the one
 * that separates the head of a table from its body.
 *
 * `pb-[14px]` rather than `pb-4` is the prototype's own number, and it is the
 * bottom inset the last row's hover chip needs to sit inside the card.
 */
export const LIST_CARD_CLASS = 'mx-4 mb-4 mt-4 rounded-lg bg-surface pb-[14px] shadow-md'

// ---------------------------------------------------------------------------
// Small hooks
// ---------------------------------------------------------------------------

/** Trailing debounce. Used for the 150 ms search delay of C-10. */
export function useDebouncedValue<T>(value: T, delayMs: number): T {
  const [debounced, setDebounced] = useState(value)
  useEffect(() => {
    if (Object.is(value, debounced)) return undefined
    const timer = setTimeout(() => {
      setDebounced(value)
    }, delayMs)
    return () => {
      clearTimeout(timer)
    }
  }, [value, debounced, delayMs])
  return debounced
}

/** Shared plumbing: re-run `measure` whenever `element` is resized. */
function useMeasured(
  ref: RefObject<HTMLElement | null>,
  read: (element: HTMLElement) => number,
): number {
  const [value, setValue] = useState(0)
  const readRef = useRef(read)
  readRef.current = read

  // `useLayoutEffect`, not `useEffect`: the first measurement has to happen
  // before the browser paints. `useEffect` runs after, so the very first frame
  // of the grid is laid out against `width === 0` — `columnCount(0)` is 1 — and
  // the reader sees one full-width column for a frame before the measurement
  // lands and it snaps to six. Measured in Chrome 150 at 1440×900 against the
  // real collection, that frame is worth up to **0.0120 CLS** on its own, over
  // impl-plan §6.3 step 6.1's 0.01 budget, and it is a *race*: on a run where
  // `/api/series` resolved before the first paint the same page scored 0.0007.
  // A layout effect removes the frame rather than the flake.
  useLayoutEffect(() => {
    const element = ref.current
    if (element === null) return undefined
    const measure = (): void => {
      setValue(readRef.current(element))
    }
    measure()
    // jsdom has no `ResizeObserver`, and a virtualised grid that throws in the
    // test environment is a grid nobody can test.
    if (typeof ResizeObserver === 'undefined') {
      window.addEventListener('resize', measure)
      return () => {
        window.removeEventListener('resize', measure)
      }
    }
    const observer = new ResizeObserver(measure)
    observer.observe(element)
    return () => {
      observer.disconnect()
    }
  }, [ref])

  return value
}

/**
 * The width of an element's **padding box** — `clientWidth`, padding included.
 *
 * That distinction is the whole reason this doc comment exists. `columnCount`
 * reproduces `repeat(auto-fill, minmax(--grid-min, 1fr))`, and the box CSS
 * resolves that against is the *content* box of the grid container. Point this
 * hook at a padded wrapper and you feed the arithmetic 32px it does not have:
 * at 1440 that is 1188 instead of 1156, which is a seventh column of 151.42px
 * where ui-spec §7 and `library-grid-1440.png` both say six of ~180px — and
 * 151.42px is below the 152px `--grid-min` the columns are supposed to honour.
 *
 * So: **measure the element the grid template is applied to**, never its
 * padded parent.
 */
export function useElementWidth(ref: RefObject<HTMLElement | null>): number {
  return useMeasured(ref, (element) => element.clientWidth)
}

/**
 * The width a scroll container reserves for its vertical scrollbar.
 *
 * The list header sits outside the scroller (WP-09 acceptance 3), so the two
 * grids resolve `minmax(0,1fr)` against different widths — the scrollbar's —
 * and every column from 형식 rightwards lands 12px off on a stable-gutter
 * platform. `SeriesList` reserves the gutter on the header to close it; pair
 * this with `scrollbar-gutter: stable` on the scroller so the number does not
 * change when the list stops overflowing.
 */
export function useScrollbarGutter(ref: RefObject<HTMLElement | null>): number {
  return useMeasured(ref, (element) => Math.max(0, element.offsetWidth - element.clientWidth))
}

/** The header's right padding: `px-4` plus the scroller's reserved gutter. */
export function listHeaderPadRight(gutter: number): string {
  return `calc(var(--space-4) + ${gutter.toString()}px)`
}

// ---------------------------------------------------------------------------
// Sticky view/sort/scope (FR-LIB-002, amendment A-5)
// ---------------------------------------------------------------------------

function isSortKey(value: string): value is SortKey {
  return (SORT_KEYS as readonly string[]).includes(value)
}

/** The four library preferences of a settings payload, as one comparable key. */
function settingsKey(s: {
  library_view: string
  library_sort: string
  library_order: string
  library_scope: string
}): string {
  return JSON.stringify([s.library_view, s.library_sort, s.library_order, s.library_scope])
}

/**
 * Keeps the four library preferences in step with `GET/PUT /api/settings`.
 *
 * The server is authoritative once it answers (`store/ui.ts`): **every** settings
 * payload hydrates the store, and every local change after that is written back
 * exactly once. `lastSent` bounds the write to one request per distinct local
 * state, so a `PUT` cannot be sent twice while its own response is in flight.
 *
 * **`reconciled` is `useState`, not `useRef`, and it holds a payload key rather
 * than a boolean. Both halves of that matter.**
 *
 * *Why state.* Both effects below list `data`, so the commit where a payload
 * arrives runs *both* of them. A ref is mutated during that commit, so the
 * write-back would see the guard already open while `view/sort/order/scope` still
 * hold the **pre-hydration** closure values — the store defaults, or whatever the
 * user's own `localStorage` copy said — and `PUT` them straight back over the
 * payload the server just sent. It converges (the next render matches, so no
 * third request), which is exactly why it stayed invisible: every fixture's
 * settings equal the store defaults, and the screen shows the server's value
 * while the server has been handed the client's. With two tabs open it is a lost
 * update. State cannot change mid-commit, so the write-back sees the *old* key on
 * the hydrating commit and returns. What follows is **two** commits, not one —
 * `hydrateFromSettings` settles the zustand selectors in the first, and
 * `setReconciled` lands in the one after. By the time the guard opens,
 * `view/sort/order/scope` are the server's values and never the stale closure.
 *
 * *Why a key and not a boolean.* A boolean `hydrated` latches: once true, a
 * **refetch carrying new server values never re-hydrates**, and the write-back
 * that follows PUTs the client's older values back over them. That is the same
 * lost update on the refetch path, and it is reachable — `invalidateRootState`
 * (`api/queries.ts`) invalidates `queryKeys.settings` whenever a root is added or
 * removed. Keying on the payload closes it: a payload the store has not been
 * reconciled against is, by definition, news from the server, and the server
 * wins. A successful `PUT` goes through the same door, because `useSaveSettings`
 * writes its response into the cache with `setQueryData` — the echo is just
 * another payload, and adopting it is what makes a server that stores something
 * other than what it was sent terminate instead of loop.
 *
 * So: **do not "simplify" this back into a ref, and do not collapse the key back
 * into a boolean.** Two behaviours depend on it and only tests defend them —
 * `react-hooks/exhaustive-deps` is `warn`, not `error` (`eslint.config.js` takes
 * the plugin's recommended set as-is), so `pnpm lint` exits 0 with a missing dep.
 * The invalid-`library_sort` repair below (`isSortKey` fails → the store keeps its
 * own valid sort → the write-back fixes the server) is the one genuine `PUT` on
 * the hydration path, and `library.test.tsx` asserts on the recorded request list
 * — not on the store — to tell the two apart.
 */
export function useLibrarySettingsSync(): void {
  const settings = useSettings()
  const { mutate } = useSaveSettings()
  const hydrateFromSettings = useUiStore((s) => s.hydrateFromSettings)
  const view = useUiStore((s) => s.view)
  const sort = useUiStore((s) => s.sort)
  const order = useUiStore((s) => s.order)
  const scope = useUiStore((s) => s.scope)

  const data = settings.data
  // `null` until the first payload lands, which is what keeps the write-back shut
  // on a cold start — no payload has been reconciled, so there is nothing to
  // write back *to* yet.
  const [reconciled, setReconciled] = useState<string | null>(null)
  const lastSent = useRef<string | null>(null)
  const serverKey = data === undefined ? null : settingsKey(data)

  useEffect(() => {
    if (data === undefined || serverKey === null) return
    if (reconciled === serverKey) return
    const next: Partial<PersistedUi> = {
      view: data.library_view,
      order: data.library_order,
      scope: data.library_scope,
    }
    if (isSortKey(data.library_sort)) next.sort = data.library_sort
    hydrateFromSettings(next)
    setReconciled(serverKey)
    // A new server truth reopens the write-back. `lastSent` remembers one
    // snapshot forever otherwise, and the store can come back to it: the reader
    // picks 그리드 (sent, remembered), a refetch re-hydrates the store to the
    // server's 리스트, the reader picks 그리드 again — `snapshot ===
    // lastSent.current`, the write is suppressed, the screen says 그리드 and the
    // server keeps 리스트 until the next reload throws the choice away. The
    // invalid-`library_sort` repair below dies the same way: after any earlier
    // write-back, a payload carrying an unreadable sort re-hydrates the other
    // three fields and the repair `PUT` is swallowed by the same guard.
    //
    // This line was deleted once and a review put it back. The reasoning that
    // deleted it was that a mutation removing it left every test in this file
    // green — but that is a statement about the tests, not about the code, and
    // the two scenarios above are exactly the paths the tests did not walk.
    // **"The mutation survived" is evidence a line is unguarded, never evidence
    // it is unnecessary.** Both scenarios now have tests (`library.test.tsx`).
    lastSent.current = null
  }, [reconciled, serverKey, data, hydrateFromSettings])

  // `reconciled` and `serverKey` are dependencies, not just guards —
  // unconditionally, whatever the payload happens to contain. `hydrateFromSettings`
  // and `setReconciled` sit in the same effect body but land in **two separate
  // commits**: the zustand selectors settle first, and `reconciled` catches up in
  // the commit after. So on the commit where this guard finally opens, *nothing
  // else in this dependency array has changed* — there is no re-run left to
  // piggyback on. Drop `reconciled` and the effect simply never runs again after
  // the guard opens, and the invalid-`library_sort` repair never fires.
  //
  // Do not talk yourself out of it with "but the payload disagrees with the store
  // here, so a selector will re-trigger it anyway". That is the false step: those
  // selectors change one commit too early, while the guard is still shut. The
  // repair test's payload disagrees on three of the four values and dropping the
  // dep still breaks it.
  useEffect(() => {
    if (data === undefined || serverKey === null) return
    // Shut while a payload is still being absorbed. On the commit a *new* payload
    // arrives, `reconciled` still names the previous one and the store still holds
    // the previous values — writing back here is precisely the lost update.
    if (reconciled !== serverKey) return
    const snapshot = settingsKey({
      library_view: view,
      library_sort: sort,
      library_order: order,
      library_scope: scope,
    })
    if (snapshot === serverKey) return
    if (lastSent.current === snapshot) return
    lastSent.current = snapshot
    mutate({
      library_view: view,
      library_sort: sort,
      library_order: order,
      library_scope: scope,
    })
  }, [reconciled, serverKey, data, view, sort, order, scope, mutate])
}

// ---------------------------------------------------------------------------
// The screen's state
// ---------------------------------------------------------------------------

export interface LibraryState {
  /** Every page fetched so far, flattened in server order. */
  items: SeriesSummary[]
  /** Matches before `offset`/`limit` — what the section header counts. */
  total: number
  /** First load, including the roots probe: render the skeleton. */
  isLoading: boolean
  /**
   * `/api/roots` or `/api/series` failed.
   *
   * The screen **must** branch on this before it branches on `items.length`:
   * an unread `isError` renders a failed request as an empty library, and the
   * empty band's copy then claims a search the user never ran.
   */
  isError: boolean
  /** Refetches whatever failed. Backs the error band's 다시 시도. */
  retry: () => void
  hasNextPage: boolean
  /** Idempotent; ignores the call while a page is already in flight. */
  loadMore: () => void
  /** The debounced query, for `highlightParts`. */
  query: string
  /** True once `/api/roots` has answered; distinguishes "empty" from "unknown". */
  rootsLoaded: boolean
  rootCount: number
  scopeName: string
  view: ViewMode
  params: SeriesListParams
}

export function useLibrary(): LibraryState {
  const view = useUiStore((s) => s.view)
  const scope = useUiStore((s) => s.scope)
  const sort = useUiStore((s) => s.sort)
  const order = useUiStore((s) => s.order)
  const rawQuery = useUiStore((s) => s.query)
  const query = useDebouncedValue(rawQuery, LIBRARY_SEARCH_DEBOUNCE_MS)

  const roots = useRoots()
  const rootItems = useMemo(() => roots.data?.items ?? [], [roots.data])
  const hasRoots = rootItems.length > 0

  const params = useMemo(
    () => libraryParams({ scope, sort, order, query }),
    [scope, sort, order, query],
  )

  const list = useSeriesListInfinite(params, { enabled: roots.isSuccess && hasRoots })

  const items = useMemo(
    () => (list.data?.pages ?? []).flatMap((page) => page.items),
    [list.data],
  )
  const total = list.data?.pages[0]?.total ?? 0

  /**
   * The last result set that arrived.
   *
   * Every sort, scope and (debounced) keystroke is a **new query key**, so
   * `data` is `undefined` again while the next page loads. Without this the
   * screen would drop to the skeleton — and take the 이어보기 shelf with it —
   * on every keystroke. `useSeriesList` states the same policy as
   * `placeholderData: keepPreviousData`; the infinite variant has no such
   * option, so the previous snapshot is held here instead.
   */
  const previous = useRef<{ items: SeriesSummary[]; total: number } | null>(null)
  if (list.data !== undefined) previous.current = { items, total }
  const shown = list.data !== undefined ? { items, total } : (previous.current ?? { items, total })

  const { fetchNextPage, hasNextPage, isFetchingNextPage } = list
  const loadMore = useCallback(() => {
    if (!hasNextPage || isFetchingNextPage) return
    void fetchNextPage()
  }, [fetchNextPage, hasNextPage, isFetchingNextPage])

  const refetchRoots = roots.refetch
  const refetchList = list.refetch
  const retry = useCallback(() => {
    void refetchRoots()
    void refetchList()
  }, [refetchRoots, refetchList])

  return {
    items: shown.items,
    total: shown.total,
    isLoading: roots.isPending || (hasRoots && list.isLoading && previous.current === null),
    isError: roots.isError || list.isError,
    retry,
    hasNextPage,
    loadMore,
    query,
    rootsLoaded: roots.isSuccess,
    rootCount: rootItems.length,
    scopeName: scopeLabel(scope, rootItems),
    view,
    params,
  }
}

/** FR-LIB-010. An empty `items` array is the signal to hide the whole shelf. */
export function useContinueItems(): { items: ContinueItem[]; isLoading: boolean } {
  const query = useContinue(CONTINUE_MAX_CARDS)
  return { items: query.data?.items ?? [], isLoading: query.isPending }
}
