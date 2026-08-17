import '@testing-library/jest-dom/vitest'

import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { setupServer } from 'msw/node'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'

import {
  ORIGIN,
  pendingRoot,
  root,
  rootEntry,
  settings as settingsFixture,
} from '../../api/fixtures'
import type {
  ContinueItem,
  Root,
  SeriesSummary,
  Settings,
  SettingsUpdate,
} from '../../api/types'
import { queryKeys } from '../../api/queries'
import { resetBasePath } from '../../api/urls'
import { matchRange } from '../../lib/chosung'
import { seriesCardDomId, useUiStore } from '../../store/ui'
import { LibraryPage } from './LibraryPage'
import { LIST_CARD_CLASS } from './useLibrary'

/**
 * The Home / Library screen against MSW (impl-plan §6.1, WP-09).
 *
 * Every test here is tied to a requirement id and would fail if the requirement
 * were violated: the sort/scope/search parameters actually sent, the order the
 * rows are rendered in, the grid/list toggle surviving a reload, the 이어보기
 * shelf appearing and vanishing, the `202` cover path, the no-results band and
 * the first-run onboarding.
 *
 * Two environment facts shape the setup:
 *  - jsdom has no `matchMedia`, so the responsive layer would report the mobile
 *    tier and the list would stack; `stubViewport` fixes the width at 1440.
 *  - jsdom reports a zero rect for every element, which would leave the
 *    virtualiser with a zero-height window; `stubRects` gives the scroller a
 *    real height so windowing can be observed rather than assumed.
 */

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

const ID_ALPHABET = 'abcdefghijklmnopqrstuvwxyz234567'

/** A syntactically valid `[a-z2-7]{16}` id derived from an index. */
function makeId(n: number): string {
  let out = ''
  let rest = n
  for (let i = 0; i < 16; i++) {
    out += ID_ALPHABET[rest % ID_ALPHABET.length] ?? 'a'
    rest = Math.floor(rest / ID_ALPHABET.length) + 7
  }
  return out
}

interface SeriesInput {
  id: string
  name: string
  rootName?: string
  kind?: SeriesSummary['kind']
  books?: number
  bytes?: number
  mtime?: number
  addedAt?: number
  percent?: number
  hasCover?: boolean
  lastReadAt?: number | null
}

function makeSeries(input: SeriesInput): SeriesSummary {
  const percent = input.percent ?? 0
  return {
    id: input.id,
    root_name: input.rootName ?? 'mangga',
    name: input.name,
    path: input.name,
    kind: input.kind ?? 'zip',
    book_count: input.books ?? 22,
    page_count: 5_000,
    total_bytes: input.bytes ?? 4_724_464_025,
    mtime: input.mtime ?? 1_478_044_800,
    added_at: input.addedAt ?? 1_753_500_000,
    status: 'ok',
    error: null,
    has_cover: input.hasCover ?? false,
    cover_cv: input.hasCover === true ? 'a1b2c3d4e5f60718' : null,
    progress: {
      books_total: input.books ?? 22,
      books_completed: 0,
      books_started: percent > 0 ? 1 : 0,
      percent,
      last_read_at: input.lastReadAt ?? (percent > 0 ? 1_753_600_500 : null),
      last_book_id: percent > 0 ? makeId(9_000) : null,
      last_page: percent > 0 ? 42 : null,
    },
  }
}

const GUNGYE = makeSeries({ id: makeId(1), name: '[만화] 군계 1~25', books: 25, bytes: 622_000_000 })
const MONSTER = makeSeries({
  id: makeId(2),
  name: '[만화] 몬스터 1~18(완)',
  kind: 'folder',
  books: 18,
  bytes: 3_972_844_748,
  mtime: 1_486_771_200,
  percent: 34,
})
const AKIRA = makeSeries({
  id: makeId(3),
  name: '[스캔] 아키라 1~6권',
  rootName: 'scan',
  kind: 'pdf',
  books: 6,
  bytes: 2_040_109_465,
  mtime: 1_381_190_400,
  percent: 100,
})

const ROOTS: Root[] = [
  root,
  { ...root, name: 'scan', label: '03. scan (PDF)', path: '/pds/scan', series_count: 1 },
]

function makeContinueItem(): ContinueItem {
  return {
    book: {
      id: makeId(77),
      series_id: GUNGYE.id,
      name: '군계(軍鷄) 01권.zip',
      path: '[만화] 군계 1~25/군계(軍鷄) 01권.zip',
      kind: 'zip',
      ord: 0,
      page_count: 187,
      total_bytes: 24_500_000,
      file_size: 24_500_000,
      mtime: 1_400_000_000,
      cv: '3f2a91cc7b40e5d1',
      status: 'ok',
      error: null,
      progress: null,
    },
    series_id: GUNGYE.id,
    series_name: '[만화] 군계 1~25',
    has_cover: false,
    progress: {
      book_id: makeId(77),
      series_id: GUNGYE.id,
      last_page: 42,
      page_count: 187,
      completed: false,
      started_at: 1_753_600_100,
      updated_at: 1_753_600_500,
      stale: false,
    },
  }
}

// ---------------------------------------------------------------------------
// The fake server
// ---------------------------------------------------------------------------

interface Scenario {
  roots: Root[]
  series: SeriesSummary[]
  continueItems: ContinueItem[]
  settings: Settings
  /** Holds `GET /api/series` open so the skeleton state can be observed. */
  hold: boolean
  /**
   * Holds `GET /api/settings` open. `/api/roots` still answers, which is the
   * real ordering the onboarding screen has to survive: `LibraryPage` renders
   * it on `rootsLoaded && rootCount === 0`, and `config_path` arrives later on
   * a different request (amendment A-10).
   */
  holdSettings: boolean
  /** `queued` answers 202 once per series before the image. */
  cover: 'image' | 'queued' | 'missing'
}

let scenario: Scenario
let seriesRequests: URL[]
let settingsUpdates: SettingsUpdate[]
let settingsReads: number
let coverRequests: string[]
/** Bodies of `POST /api/roots` — amendment A-11. */
let rootPosts: unknown[]
let releaseSeries: () => void
let seriesGate: Promise<void>
let releaseSettings: () => void
let settingsGate: Promise<void>

function compareSeries(key: string, a: SeriesSummary, b: SeriesSummary): number {
  switch (key) {
    case 'size':
      return a.total_bytes - b.total_bytes
    case 'books':
      return a.book_count - b.book_count
    case 'mtime':
      return a.mtime - b.mtime
    case 'added':
      return a.added_at - b.added_at
    case 'recent':
      return (a.progress.last_read_at ?? 0) - (b.progress.last_read_at ?? 0)
    default:
      return a.name.localeCompare(b.name, 'ko')
  }
}

const server = setupServer(
  http.get(`${ORIGIN}/api/roots`, () => HttpResponse.json({ items: scenario.roots })),

  /**
   * Amendment A-11. The write lands in the *file*, and roots are opened once at
   * startup — so what the next `GET /api/roots` reports is a `pending` row (R2),
   * not a loaded one. Mutating `scenario.roots` here rather than returning a
   * canned list is what makes "the screen re-read the server" observable: the
   * component sees the new row only if it actually refetched.
   */
  http.post(`${ORIGIN}/api/roots`, async ({ request }) => {
    rootPosts.push(await request.json())
    scenario.roots = [...scenario.roots, pendingRoot]
    return HttpResponse.json(rootEntry, { status: 201 })
  }),

  http.get(`${ORIGIN}/api/settings`, async () => {
    settingsReads += 1
    if (scenario.holdSettings) await settingsGate
    return HttpResponse.json(scenario.settings)
  }),

  http.put(`${ORIGIN}/api/settings`, async ({ request }) => {
    const update = (await request.json()) as SettingsUpdate
    settingsUpdates.push(update)
    scenario.settings = { ...scenario.settings, ...update }
    return HttpResponse.json(scenario.settings)
  }),

  http.get(`${ORIGIN}/api/continue`, () => HttpResponse.json({ items: scenario.continueItems })),

  http.get(`${ORIGIN}/api/series`, async ({ request }) => {
    const url = new URL(request.url)
    seriesRequests.push(url)
    if (scenario.hold) await seriesGate

    let items = scenario.series
    const q = url.searchParams.get('q')
    if (q !== null) items = items.filter((s) => matchRange(s.name, q) !== null)
    const roots = url.searchParams.getAll('root')
    if (roots.length > 0) items = items.filter((s) => roots.includes(s.root_name))
    const progress = url.searchParams.get('progress')
    if (progress === 'reading') {
      items = items.filter((s) => s.progress.percent > 0 && s.progress.percent < 100)
    }
    if (progress === 'done') items = items.filter((s) => s.progress.percent >= 100)

    const sort = url.searchParams.get('sort') ?? 'name'
    items = [...items].sort((a, b) => compareSeries(sort, a, b))
    if (url.searchParams.get('order') === 'desc') items.reverse()

    const offset = Number(url.searchParams.get('offset') ?? '0')
    const limit = Number(url.searchParams.get('limit') ?? '60')
    return HttpResponse.json({
      items: items.slice(offset, offset + limit),
      total: items.length,
      offset,
      limit,
    })
  }),

  http.get(`${ORIGIN}/api/series/:sid/cover`, ({ params }) => {
    const sid = String(params.sid)
    const attempts = coverRequests.filter((id) => id === sid).length
    coverRequests.push(sid)
    if (scenario.cover === 'missing') {
      return HttpResponse.json(
        { error: { code: 'not_found', message: 'no cover' } },
        { status: 404 },
      )
    }
    if (scenario.cover === 'queued' && attempts < 3) {
      // FR-THM-003: a queued thumbnail is a normal 202, not an error.
      return new HttpResponse(null, { status: 202, headers: { 'Retry-After': '0' } })
    }
    return new HttpResponse(new Uint8Array([0xff, 0xd8, 0xff, 0xd9]), {
      headers: { 'Content-Type': 'image/jpeg' },
    })
  }),
)

// ---------------------------------------------------------------------------
// Environment
// ---------------------------------------------------------------------------

interface FakeMql {
  matches: boolean
  media: string
  addEventListener: (type: string, cb: () => void) => void
  removeEventListener: (type: string, cb: () => void) => void
}

let viewportWidth = 0
let viewportListeners: (() => void)[] = []

/**
 * Fixes the viewport width the `useMediaQuery` layer reports.
 *
 * The listeners are kept rather than dropped so `resizeViewport` below can move
 * the tier the way a real window resize does: `useBreakpoint` reads through
 * `useSyncExternalStore`, so a stub whose `addEventListener` is a no-op can be
 * *set* to a new width but can never *change* to one — React is never told.
 */
function stubViewport(width: number): void {
  viewportWidth = width
  viewportListeners = []
  const impl = (query: string): FakeMql => {
    const m = /min-width:\s*(\d+)px/.exec(query)
    const min = m?.[1] === undefined ? null : Number(m[1])
    return {
      get matches(): boolean {
        return min !== null && viewportWidth >= min
      },
      media: query,
      addEventListener: (_: string, cb: () => void) => viewportListeners.push(cb),
      removeEventListener: (_: string, cb: () => void) => {
        viewportListeners = viewportListeners.filter((x) => x !== cb)
      },
    }
  }
  Object.defineProperty(window, 'matchMedia', { writable: true, configurable: true, value: impl })
}

/**
 * Moves the stubbed viewport and tells every `useMediaQuery` about it, then
 * fires the `resize` event `useMeasured` falls back to when — as in jsdom —
 * there is no `ResizeObserver` (`useLibrary.ts`).
 *
 * Both signals in one call, which is the *settled* end state of a resize rather
 * than the moment one arrives. Use `crossTier` below for the moment itself; a
 * grid that only ever sees this helper can hide a defect that lives in the gap
 * between the two.
 */
function resizeViewport(width: number): void {
  viewportWidth = width
  for (const cb of [...viewportListeners]) cb()
  window.dispatchEvent(new Event('resize'))
}

/**
 * A tier crossing as the browser actually delivers one: the media queries fire,
 * and the element's width has **already** moved.
 *
 * This is the honest model and `resizeViewport` above is not, which is why both
 * exist. The layout is CSS-driven — `base.css` gives `.sidebar` a
 * `var(--sidebar-w)` width and `display: none` below 768 — so by the time a
 * `matchMedia` handler runs, the grid's box in the layout tree is already the
 * settled one. What lags is React state, never the DOM. So the caller moves
 * `contentWidth` *before* calling this, and no `resize` event is dispatched:
 * that event stands in for the `ResizeObserver` here, and the whole point is
 * that a correct grid must not need it to have arrived yet.
 *
 * Fire this and nothing else, and a grid that reads its tier and its width
 * through separate hooks renders the intermediate layout — new metrics, old
 * width — for as long as it takes the observer to catch up.
 */
function crossTier(width: number): void {
  viewportWidth = width
  for (const cb of [...viewportListeners]) cb()
}

/**
 * jsdom does no layout, so every element reports `clientWidth === 0` and the one
 * box-model fact the grid's arithmetic turns on — that **`clientWidth` includes
 * padding** — cannot be observed at all. This reproduces it: a `p-4` wrapper
 * reports 32px more than the box its child grid is actually laid out in, so a
 * component that measures the wrapper instead of the grid gets caught here
 * rather than in a screenshot.
 *
 * A thunk may be passed instead of a number when the test needs the measured
 * width to *move*, which is what a resize does.
 */
function stubContentWidth(contentWidth: number | (() => number)): () => void {
  const original = Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'clientWidth')
  Object.defineProperty(HTMLElement.prototype, 'clientWidth', {
    configurable: true,
    get(this: HTMLElement): number {
      const w = typeof contentWidth === 'function' ? contentWidth() : contentWidth
      return w + (this.classList.contains('p-4') ? 32 : 0)
    },
  })
  return () => {
    if (original === undefined) Reflect.deleteProperty(HTMLElement.prototype, 'clientWidth')
    else Object.defineProperty(HTMLElement.prototype, 'clientWidth', original)
  }
}

/** Gives the list scroller a reserved scrollbar gutter of `px`. */
function stubScrollbarGutter(px: number): () => void {
  const original = Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'offsetWidth')
  Object.defineProperty(HTMLElement.prototype, 'offsetWidth', {
    configurable: true,
    get(this: HTMLElement): number {
      return this.dataset.testid === 'library-scroller' ? this.clientWidth + px : this.clientWidth
    },
  })
  return () => {
    if (original === undefined) Reflect.deleteProperty(HTMLElement.prototype, 'offsetWidth')
    else Object.defineProperty(HTMLElement.prototype, 'offsetWidth', original)
  }
}

/** Every offset `virtual-core` asked the scroller to move to, in order. */
let scrollCalls: number[] = []

/**
 * The browser's half of a virtualised scroll, which jsdom simply does not have.
 *
 * The same two stubs the viewer's reveal tests already needed
 * (`features/viewer/ViewerPage.test.tsx`), for the same two reasons:
 *
 *  1. `getOffsetForAlignment` clamps every scroll to `scrollHeight - size`, and
 *     jsdom reports `scrollHeight === 0` for everything — so the clamp is
 *     negative, **every** scroll resolves to 0, and a test cannot fail. The
 *     virtualiser sizes exactly one child to the whole content height, so that
 *     inline height is the honest answer.
 *  2. jsdom has no `Element.prototype.scrollTo` at all and `virtual-core`'s
 *     `elementScroll` calls it optionally, so without it the scroll silently
 *     does nothing and the only observable left would be "`scrollToIndex` was
 *     called" — one step short of the claim.
 */
function stubScrolling(): () => void {
  const scrollHeight = Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'scrollHeight')
  Object.defineProperty(HTMLElement.prototype, 'scrollHeight', {
    configurable: true,
    get(this: HTMLElement): number {
      const sizer = this.querySelector<HTMLElement>('div[style*="height:"]')
      const px = sizer === null ? 0 : Number.parseFloat(sizer.style.height)
      return Number.isFinite(px) ? px : 0
    },
  })
  const scrollTo = Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'scrollTo')
  Object.defineProperty(HTMLElement.prototype, 'scrollTo', {
    configurable: true,
    writable: true,
    value(this: HTMLElement, options: ScrollToOptions) {
      const top = Math.round(options.top ?? 0)
      scrollCalls.push(top)
      this.scrollTop = top
      this.dispatchEvent(new Event('scroll'))
    },
  })
  return () => {
    if (scrollHeight === undefined) Reflect.deleteProperty(HTMLElement.prototype, 'scrollHeight')
    else Object.defineProperty(HTMLElement.prototype, 'scrollHeight', scrollHeight)
    if (scrollTo === undefined) Reflect.deleteProperty(HTMLElement.prototype, 'scrollTo')
    else Object.defineProperty(HTMLElement.prototype, 'scrollTo', scrollTo)
  }
}

/** Puts the reader `top` px down the scroller, the way a wheel would. */
function scrollTo(scroller: HTMLElement, top: number): void {
  scroller.scrollTop = top
  scroller.dispatchEvent(new Event('scroll'))
}

/** The `translateY` a virtualised row is positioned at, in px. */
function rowOffset(row: HTMLElement | null | undefined): number {
  const m = /translateY\((-?[\d.]+)px\)/.exec(row?.style.transform ?? '')
  return m?.[1] === undefined ? Number.NaN : Number.parseFloat(m[1])
}

/** jsdom measures everything as 0×0; the virtualiser needs a real window. */
function stubRects(height: number): void {
  const rect: DOMRect = {
    x: 0,
    y: 0,
    top: 0,
    left: 0,
    right: 1_154,
    bottom: height,
    width: 1_154,
    height,
    toJSON: () => ({}),
  }
  vi.spyOn(Element.prototype, 'getBoundingClientRect').mockReturnValue(rect)
}

/**
 * Renders the library screen.
 *
 * Returns the `QueryClient` so a test can invalidate a key the way the product
 * does — `invalidateRootState` in `api/queries.ts` invalidates `roots` *and*
 * `settings` on every root add/remove. Callers that do not need it may ignore
 * the return value.
 */
function renderLibrary(): QueryClient {
  const client = new QueryClient({
    // `retry` here is advisory only — `queries.ts` pins its own `retryQuery` on
    // every hook, and a 5xx is retried twice on purpose. `retryDelay: 0` is
    // *not* overridden, so the failure path resolves without three seconds of
    // exponential backoff in the middle of a test.
    defaultOptions: {
      queries: { retry: false, retryDelay: 0 },
      mutations: { retry: false },
    },
  })
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/']}>
        <Routes>
          <Route path="/" element={<LibraryPage />} />
          <Route path="/series/:sid" element={<p>series detail</p>} />
          <Route path="/series/:sid/books/:bid" element={<p>viewer</p>} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  )
  return client
}

/** Waits for the first list *and* the settings hydration to have landed. */
async function waitForLibrary(): Promise<void> {
  await waitFor(() => {
    expect(screen.queryByTestId('library-skeleton')).not.toBeInTheDocument()
  })
  await waitFor(() => {
    expect(settingsReads).toBeGreaterThan(0)
  })
}

/** The last `GET /api/series` the screen issued. */
function lastSeriesRequest(): URLSearchParams {
  const url = seriesRequests.at(-1)
  if (url === undefined) throw new Error('no /api/series request was made')
  return url.searchParams
}

beforeAll(() => {
  server.listen({ onUnhandledRequest: 'error' })
})
afterEach(() => {
  server.resetHandlers()
  resetBasePath()
})
afterAll(() => {
  server.close()
})

beforeEach(() => {
  scenario = {
    roots: ROOTS,
    series: [GUNGYE, MONSTER, AKIRA],
    continueItems: [],
    settings: { ...settingsFixture },
    hold: false,
    holdSettings: false,
    cover: 'image',
  }
  seriesRequests = []
  settingsUpdates = []
  settingsReads = 0
  scrollCalls = []
  coverRequests = []
  rootPosts = []
  seriesGate = new Promise<void>((resolve) => {
    releaseSeries = resolve
  })
  settingsGate = new Promise<void>((resolve) => {
    releaseSettings = resolve
  })
  localStorage.clear()
  useUiStore.setState({
    view: 'grid',
    scope: 'all',
    sort: 'name',
    order: 'asc',
    query: '',
    paletteQuery: '',
    drawerOpen: false,
    overlays: [],
    // The E-34 §2 instruction is consumed by whichever surface is mounted, so a
    // test that arms it and does not reach it would arm it for the next one.
    revealSeries: null,
  })
  stubViewport(1_440)
  stubRects(900)
})

// ---------------------------------------------------------------------------
// FR-LIB-001 / FR-LIB-008 — the grid
// ---------------------------------------------------------------------------

describe('grid mode (FR-LIB-001, FR-LIB-008)', () => {
  it('renders a card per series with its title, volume count and size', async () => {
    renderLibrary()
    await waitForLibrary()

    expect(await screen.findByRole('button', { name: GUNGYE.name })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: MONSTER.name })).toBeInTheDocument()
    expect(screen.getByText('25권')).toBeInTheDocument()
    expect(screen.getByText('622 MB')).toBeInTheDocument()
    expect(screen.getByText('4.0 GB')).toBeInTheDocument()
    expect(screen.getByText('3개 시리즈')).toBeInTheDocument()
    expect(screen.getByText('전체 시리즈')).toBeInTheDocument()
  })

  it('carries the series name in the fallback cover when there is no thumbnail', async () => {
    renderLibrary()
    await waitForLibrary()

    // FR-LIB-008: the placeholder is text, and it names the series.
    const fallbacks = await screen.findAllByText('ZIP · NO THUMBNAIL')
    expect(fallbacks.length).toBeGreaterThan(0)
    expect(screen.getByText('PDF · NO THUMBNAIL')).toBeInTheDocument()
    // The name appears twice: inside the fallback and under the cover.
    expect(screen.getAllByText(GUNGYE.name).length).toBe(2)
    expect(document.querySelector('img')).toBeNull()
  })

  it('badges format and 완독, and shows a progress bar only while in progress', async () => {
    renderLibrary()
    await waitForLibrary()

    expect(await screen.findByText('완독')).toBeInTheDocument()
    expect(screen.getAllByText('ZIP').length).toBeGreaterThan(0)
    expect(screen.getByText('FOLDER')).toBeInTheDocument()
    expect(screen.getByText('PDF')).toBeInTheDocument()

    const bars = screen.getAllByRole('progressbar')
    // Only 몬스터 (34%) is partially read: 군계 is untouched and 아키라 is 완독.
    expect(bars).toHaveLength(1)
    expect(bars[0]).toHaveAttribute('aria-valuenow', '34')
    expect(bars[0]).toHaveAttribute('aria-label', MONSTER.name)
  })

  /**
   * E-32's structural swap on the card, asserted on the class list.
   *
   * The 1px hairline round every cover became elevation plus a hover lift. What
   * is worth pinning is not the look but the pair of things that were *removed*:
   * a `border` on a cover that now has a radius clips the artwork at the corners
   * and reads as a mis-render, and `hover:border-accent` — which is what the
   * continue card and the volume tile used to do — is a deep teal at ~1.2:1
   * against the surface in the dark theme, i.e. a hover state that does nothing.
   */
  it('gives the cover elevation and a lift instead of a hairline (E-32)', async () => {
    renderLibrary()
    await waitForLibrary()

    const cover = await screen.findByRole('button', { name: MONSTER.name })
    const box = cover.parentElement
    expect(box).toHaveClass('rounded-md', 'shadow-md', 'hover:shadow-lg')
    expect(box?.classList.contains('border')).toBe(false)
    expect(box?.classList.contains('border-rule')).toBe(false)
  })

  it('lays out the auto-fill column count of the inner grid box (acceptance 1, ui-spec §7)', async () => {
    // 1440 viewport − 240 sidebar − 2px rule − 12px scrollbar = 1188 for the
    // padded wrapper, so 1156 for the grid inside its `p-4`. What CSS would do
    // with `repeat(auto-fill, minmax(152px, 1fr))` and a 16px gap on 1156:
    //   floor((1156 + 16) / (152 + 16)) = 6 columns of 179.3px
    // — which is what ui-spec §7 ("6 cols @1440") and library-grid-1440.png
    // both show. Measuring the padded wrapper instead yields floor(1204/168)
    // = 7 columns of 151.4px, i.e. *narrower than `--grid-min`*, and turns the
    // skeleton→grid transition into a 6→7 column reflow.
    const restore = stubContentWidth(1_156)
    try {
      scenario.series = Array.from({ length: 12 }, (_, i) =>
        makeSeries({ id: makeId(3_000 + i), name: `[만화] 시리즈 ${(i + 1).toString()}` }),
      )
      renderLibrary()
      await waitForLibrary()
      await screen.findByRole('button', { name: '[만화] 시리즈 1' })

      const scroller = screen.getByTestId('library-scroller')
      const rows = scroller.querySelectorAll<HTMLElement>('[data-index]')
      expect(rows.length).toBe(2)
      expect(rows[0]?.style.gridTemplateColumns).toBe('repeat(6, minmax(0, 1fr))')
      expect(rows[0]?.children).toHaveLength(6)
      expect(rows[1]?.children).toHaveLength(6)

      // …and the row *pitch* is the real card height plus one `--grid-gap`,
      // not an estimate that happens to be close. A 179.33px column is a
      // 269px 2:3 cover over a 60px text block = 329px, so row 1 starts at
      // 329 + 16 and the stack is 2×329 + 16 tall. Get the measured width or
      // `CARD_TEXT_HEIGHT` wrong and the rendered gap stops being 16px.
      const cardH = ((1_156 - 16 * 5) / 6) * 1.5 + 60
      expect(cardH).toBe(329)
      expect(rows[1]?.style.transform).toBe('translateY(345px)')
      expect(rows[0]?.parentElement?.style.height).toBe('674px')
    } finally {
      restore()
    }
  })

  it('re-lays out the rows when a resize moves the card height (open item m)', async () => {
    // `virtual-core` memoises `getMeasurements` on
    // `[count, paddingStart, scrollMargin, getItemKey, enabled]` + the item-size
    // cache — **not** on `estimateSize`, and not on `gap` either. Handing it a
    // taller row therefore changes nothing on its own; only `measure()`, which
    // swaps the size cache for a fresh Map, does. Same disease as the thumbnail
    // strip's (E-28), and `SeriesGrid` had no `measure()` at all.
    //
    // A grid box of 1100 → 1156 is the *hard* case on purpose. Both widths are
    // six columns and both stay in the `desktop` tier, so `count` — the one
    // memo-key entry this grid ever moves — stays at 2 and nothing invalidates
    // the cache by accident. Only the pitch changes: a 170px column is a 315px
    // card, a 179.33px column is a 329px one. It grows, which is the direction
    // that overlaps rather than the one that gaps them.
    //
    // Measured in Chrome on the shipped build (60 synthetic series) before the
    // fix, at the two widths this stands in for: 1280 → 1440 left the pitch at
    // 305.5px while the cards grew to 329.5px, so every row overlapped the one
    // above it by 24px, and the track stayed 3 039px against 3 439px. jsdom
    // does no layout, but the pitch and the track are pure `estimateSize`
    // arithmetic, so neither assertion below is weakened by that.
    let contentWidth = 1_100
    const restore = stubContentWidth(() => contentWidth)
    try {
      scenario.series = Array.from({ length: 12 }, (_, i) =>
        makeSeries({ id: makeId(3_000 + i), name: `[만화] 시리즈 ${(i + 1).toString()}` }),
      )
      renderLibrary()
      await waitForLibrary()
      await screen.findByRole('button', { name: '[만화] 시리즈 1' })

      const rowsNow = (): NodeListOf<HTMLElement> =>
        screen.getByTestId('library-scroller').querySelectorAll<HTMLElement>('[data-index]')

      expect(rowsNow()[1]?.style.transform).toBe('translateY(331px)')
      expect(rowsNow()[0]?.parentElement?.style.height).toBe('646px')

      contentWidth = 1_156
      act(() => {
        resizeViewport(1_600)
      })

      // Still six columns, still two rows and still `desktop` — the column
      // count and the tier are *not* what moved, which is why keying the
      // re-measure on the breakpoint or on `columns` would not have caught it.
      expect(rowsNow()).toHaveLength(2)
      expect(rowsNow()[0]?.style.gridTemplateColumns).toBe('repeat(6, minmax(0, 1fr))')
      const cardH = ((1_156 - 16 * 5) / 6) * 1.5 + 60
      expect(cardH).toBe(329)
      expect(rowsNow()[1]?.style.transform).toBe('translateY(345px)')
      expect(rowsNow()[0]?.parentElement?.style.height).toBe('674px')
    } finally {
      restore()
    }
  })

  it('re-lays out the rows when only `--grid-gap` moves (open item m)', async () => {
    // `gap` is the *other* `useVirtualizer` option `virtual-core` reads inside
    // the memo body and leaves out of the memo key, so it goes stale in exactly
    // the same way `estimateSize` does, and `SeriesGrid` depends on it for that
    // reason.
    //
    // **This transition is manufactured, and the honest reading of this test is
    // "the `metrics.gap` dependency is wired up", not "the product would break
    // without it".** `--grid-gap` only changes at 768, and across that boundary
    // the real layout moves the column count 2 → 4 and the card 550.5 → 318.4px
    // (measured), so `rowHeight` moves 232px at the same instant and the
    // `rowHeight` dependency alone would already have re-measured. To isolate
    // the gap at all, the grid box has to be driven to widths the layout never
    // produces: a 464px box at `tablet` and a 460px one at `mobile` both come
    // out at two columns of exactly 224px — the same column count, so `count`
    // (6 rows of 12 series) cannot invalidate the cache by accident, and the
    // same 396px card, so `rowHeight` does not move. Only the pitch does:
    // 412px → 408px.
    let contentWidth = 464
    const restore = stubContentWidth(() => contentWidth)
    try {
      stubViewport(800)
      scenario.series = Array.from({ length: 12 }, (_, i) =>
        makeSeries({ id: makeId(3_500 + i), name: `[만화] 시리즈 ${(i + 1).toString()}` }),
      )
      renderLibrary()
      await waitForLibrary()
      await screen.findByRole('button', { name: '[만화] 시리즈 1' })

      const rowsNow = (): NodeListOf<HTMLElement> =>
        screen.getByTestId('library-scroller').querySelectorAll<HTMLElement>('[data-index]')

      // Only a window is mounted, so the row *count* is read off the track:
      // 6 × 396 + 5 gaps.
      expect(rowsNow()[0]?.style.gridTemplateColumns).toBe('repeat(2, minmax(0, 1fr))')
      expect(rowsNow()[0]?.style.gap).toBe('16px')
      expect(rowsNow()[1]?.style.transform).toBe('translateY(412px)')
      expect(rowsNow()[0]?.parentElement?.style.height).toBe('2456px')

      contentWidth = 460
      act(() => {
        resizeViewport(700)
      })

      expect(rowsNow()[0]?.style.gridTemplateColumns).toBe('repeat(2, minmax(0, 1fr))')
      expect(rowsNow()[0]?.style.gap).toBe('12px')
      expect(rowsNow()[1]?.style.transform).toBe('translateY(408px)')
      expect(rowsNow()[0]?.parentElement?.style.height).toBe('2436px')
    } finally {
      restore()
    }
  })

  it('keeps the reader on the row they were on across that re-measure', async () => {
    // The re-measure moves every row's `start` and leaves `scrollTop` where it
    // was, so on its own it displaces the reader by `topRowIndex × Δpitch` —
    // **linear in scroll depth**, which is what makes it worse than a wobble.
    // Measured in Chrome on the shipped build: 1440 → 1280 at `scrollTop` 1500
    // moved the anchor card 160px, 1280 → 1440 on row 6 moved it 200px, and a
    // 10 000-series library 500 rows down with a 40px Δpitch would move 20 000.
    // The strip has paired `measure()` with a `scrollToIndex` since E-28; this
    // pins that the grid does too.
    //
    // **The grid box is driven to two columns to buy scroll depth, and the
    // depth is the point.** 60 series in two columns is 30 rows; a six-column
    // grid of the same 60 is ten rows, and ten rows is not deep enough to tell
    // a correct anchor from a plausible-looking wrong one — at row 4 the row
    // picked from the stale offsets and the row picked from the fresh ones are
    // the same row, so the test would pass on code that reads the anchor at the
    // wrong moment. At row 20 they are 7 rows apart.
    //
    // 480px box → 2 columns of 232px → 408px cards → 424px pitch.
    // 320px box → 2 columns of 152px → 288px cards → 304px pitch.
    // Same column count, so `count` stays 30 and cannot invalidate the memo by
    // accident. Parked on row 20 at scrollTop 8480; row 20's new start is 6080.
    //
    // It shrinks rather than grows because `getOffsetForAlignment` clamps to
    // `scrollHeight − size` read off the **DOM**, and at the instant this effect
    // runs the DOM still carries the pre-measure track height. Shrinking makes
    // that clamp the looser of the two, so the assertion is about the anchor
    // and not about the clamp. (Growing is bounded by the same clamp in the
    // product; see the note in `SeriesGrid`.)
    let contentWidth = 480
    const restoreWidth = stubContentWidth(() => contentWidth)
    const restoreScroll = stubScrolling()
    try {
      scenario.series = Array.from({ length: 60 }, (_, i) =>
        makeSeries({ id: makeId(3_800 + i), name: `[만화] 시리즈 ${(i + 1).toString()}` }),
      )
      renderLibrary()
      await waitForLibrary()
      await screen.findByRole('button', { name: '[만화] 시리즈 1' })

      const scroller = screen.getByTestId('library-scroller')
      const rowAt = (index: number): HTMLElement | null =>
        scroller.querySelector<HTMLElement>(`[data-index="${index.toString()}"]`)

      act(() => {
        scrollTo(scroller, 20 * 424)
      })
      expect(rowOffset(rowAt(20))).toBe(8_480)
      expect(scroller.scrollTop).toBe(8_480)

      contentWidth = 320
      act(() => {
        resizeViewport(1_600)
      })

      // The re-anchor waits a frame, because a `scrollToIndex` in this commit
      // would read the offsets `measure()` has just invalidated. Until it
      // lands, the window is drawn around the *old* scrollTop against the new
      // pitch, so rows 20–21 are not even mounted yet.
      await waitFor(() => {
        expect(scroller.scrollTop).toBe(6_080)
      })

      // Row 20 — the row the reader was on — is the row flush with the top of
      // the scroller. Reading the anchor after the re-measure would name row 27
      // here, and not re-anchoring at all would leave 8480.
      expect(rowOffset(rowAt(20))).toBe(6_080)
      // …and the pitch really did move, so this cannot pass on a grid that
      // never re-measured at all.
      expect(rowOffset(rowAt(21)) - rowOffset(rowAt(20))).toBe(304)
    } finally {
      restoreScroll()
      restoreWidth()
    }
  })

  it('keeps the reader on the same series when the column count changes', async () => {
    // **Every other guard in this file holds the column count fixed**, which was
    // deliberate — a constant `count` is what stops the `getMeasurements` memo
    // invalidating by accident — and it is exactly why they all passed over a
    // re-anchor that threw the reader to the top of the library.
    //
    // The first version of this effect read the anchor from
    // `getVirtualItems().find((row) => row.end > scrollTop)` before calling
    // `measure()`, on the theory that the offsets were still the old ones there.
    // `count` *is* in the memo key, so a render that changes the row count has
    // already recomputed them: `end > scrollTop` then measures the old scroll
    // position against the new layout and names a row far too early. Measured in
    // Chrome on the shipped build, 12 series at 871 → 800: row 0's new span is
    // [0, 574.5], it contains the old scrollTop of 447, the anchor came out as
    // row 0 and `scrollTop` went to 0 — worse than the defect, which left it at
    // 447.
    //
    // 773px box at `tablet`: 3 columns of 247px → 430.5px cards → 446.5 pitch.
    // 702px box at `tablet`: 2 columns of 343px → 574.5px cards → 590.5 pitch.
    //
    // Parked on **row 2**, not row 1, and the difference matters: row 2 of a
    // three-column grid starts at series 6, and series 6 is row *3* of a
    // two-column one. Anchoring on the row index, or dividing by the old column
    // count instead of the new one, both land on row 2 — so a shallower park
    // cannot tell a correct re-anchor from either of those.
    let contentWidth = 773
    const restoreWidth = stubContentWidth(() => contentWidth)
    const restoreScroll = stubScrolling()
    try {
      stubViewport(800)
      scenario.series = Array.from({ length: 12 }, (_, i) =>
        makeSeries({ id: makeId(3_700 + i), name: `[만화] 시리즈 ${(i + 1).toString()}` }),
      )
      renderLibrary()
      await waitForLibrary()
      await screen.findByRole('button', { name: '[만화] 시리즈 1' })

      const scroller = screen.getByTestId('library-scroller')
      const rowAt = (index: number): HTMLElement | null =>
        scroller.querySelector<HTMLElement>(`[data-index="${index.toString()}"]`)

      expect(rowAt(0)?.children).toHaveLength(3)
      act(() => {
        scrollTo(scroller, 893)
      })
      expect(rowOffset(rowAt(2))).toBe(893)
      // Whatever the sort put first in the row the reader is on. Read from the
      // DOM rather than named: `/api/series` sorts by name as a string, so the
      // twelfth series is not the twelfth row.
      const anchorLabel = rowAt(2)?.querySelector('[aria-label]')?.getAttribute('aria-label')
      expect(anchorLabel).toBeTruthy()

      contentWidth = 702
      act(() => {
        resizeViewport(800)
      })

      // The premise: the column count really did move, so the row the reader was
      // on is not the row they end up on by index alone.
      expect(
        [...scroller.querySelectorAll('[data-index]')][0]?.children,
      ).toHaveLength(2)

      // 1772, not 1771.5: `stubScrolling` rounds the way the viewer's equivalent
      // does, because that is what a `scrollTo` reports back in jsdom. The
      // virtualiser's own offset below is the unrounded number.
      await waitFor(() => {
        expect(scroller.scrollTop).toBe(1_772)
      })
      expect(rowOffset(rowAt(3))).toBe(1_771.5)

      // …and the series the reader was looking at is in that row. `toContain`
      // rather than "is first", and that is the honest claim: this anchor is
      // row-granular, so a series that led the old row can land mid-row when the
      // columns change. What it may never do is leave the screen, which is what
      // the version that asked the virtualiser did.
      const labels = [...(rowAt(3)?.querySelectorAll('[aria-label]') ?? [])].map((el) =>
        el.getAttribute('aria-label'),
      )
      expect(labels).toContain(anchorLabel)
    } finally {
      restoreScroll()
      restoreWidth()
    }
  })

  it('re-derives the anchor when the reader moved, not us, between two resizes', async () => {
    // **`lastWrittenRef` is a question, not a flag: "is the scroller where *we*
    // put it?"** Every other anchor test here is `scroll once → resize`, which
    // never asks that question — the scroller is always where the reader left
    // it. So this one resizes, lets the *reader* scroll, and resizes again.
    //
    // Two mutants live in the gap this closes, and both are silent bugs rather
    // than crashes: `ours = lastWrittenRef.current !== null` (the ref's
    // existence, not the position) and a loose tolerance. Both make the second
    // resize drag the reader back to the series they were on before they
    // scrolled away.
    //
    // 60 series in a 480px box: 2 columns of 232px → 408px cards → 424 pitch,
    // 30 rows. Parked on row 20 → anchored to row 20 of the 304 pitch = 6080.
    // The reader then scrolls to row 22 (6688) — 608px, which is far enough to
    // change the answer but well inside a sloppy tolerance.
    let contentWidth = 480
    const restoreWidth = stubContentWidth(() => contentWidth)
    const restoreScroll = stubScrolling()
    try {
      scenario.series = Array.from({ length: 60 }, (_, i) =>
        makeSeries({ id: makeId(4_400 + i), name: `[만화] 시리즈 ${(i + 1).toString()}` }),
      )
      renderLibrary()
      await waitForLibrary()
      await screen.findByRole('button', { name: '[만화] 시리즈 1' })

      const scroller = screen.getByTestId('library-scroller')
      const rowAt = (index: number): HTMLElement | null =>
        scroller.querySelector<HTMLElement>(`[data-index="${index.toString()}"]`)

      act(() => {
        scrollTo(scroller, 20 * 424)
      })
      contentWidth = 320
      act(() => {
        resizeViewport(1_600)
      })
      await waitFor(() => {
        expect(scroller.scrollTop).toBe(6_080)
      })

      // The reader moves themselves, two rows on.
      act(() => {
        scrollTo(scroller, 22 * 304)
      })
      expect(scroller.scrollTop).toBe(6_688)

      contentWidth = 480
      act(() => {
        resizeViewport(1_600)
      })

      // Row 22, where the reader went — not row 20, where we last put them.
      await waitFor(() => {
        expect(scroller.scrollTop).toBe(22 * 424)
      })
      expect(rowOffset(rowAt(22))).toBe(9_328)
    } finally {
      restoreScroll()
      restoreWidth()
    }
  })

  it('forgets what it wrote once there is no anchor to keep', async () => {
    // A five-step path, and every step is ordinary on its own:
    //
    //  1. a resize deep in the library writes `lastWrittenRef = 6080`;
    //  2. the reader goes back to the top;
    //  3. a resize there finds no anchor (`scrollTop === 0`) and writes nothing,
    //     so without clearing it, `lastWrittenRef` is still 6080 — a number from
    //     a layout two resizes ago;
    //  4. the reader scrolls to exactly 6080, which is now an ordinary position
    //     they chose;
    //  5. the next resize asks "is the scroller where we put it?", gets `true`
    //     off that stale number, reaches for a remembered series that was
    //     cleared in step 3, and **skips the re-anchor entirely**.
    //
    // The 1px window in step 4 makes this rare, not impossible, and it is one
    // line to close.
    let contentWidth = 480
    const restoreWidth = stubContentWidth(() => contentWidth)
    const restoreScroll = stubScrolling()
    try {
      scenario.series = Array.from({ length: 60 }, (_, i) =>
        makeSeries({ id: makeId(4_600 + i), name: `[만화] 시리즈 ${(i + 1).toString()}` }),
      )
      renderLibrary()
      await waitForLibrary()
      await screen.findByRole('button', { name: '[만화] 시리즈 1' })

      const scroller = screen.getByTestId('library-scroller')

      act(() => {
        scrollTo(scroller, 20 * 424)
      })
      contentWidth = 320
      act(() => {
        resizeViewport(1_600)
      })
      await waitFor(() => {
        expect(scroller.scrollTop).toBe(6_080)
      })

      act(() => {
        scrollTo(scroller, 0)
      })
      contentWidth = 480
      act(() => {
        resizeViewport(1_600)
      })
      expect(scroller.scrollTop).toBe(0)

      // The reader lands back on the old written offset of their own accord.
      act(() => {
        scrollTo(scroller, 6_080)
      })
      contentWidth = 320
      act(() => {
        resizeViewport(1_600)
      })

      // 6080 in a 424 pitch is row 14, which is series 28, which is row 14 of a
      // 304 pitch. Leaving `lastWrittenRef` set would skip this entirely and
      // strand the reader at 6080.
      await waitFor(() => {
        expect(scroller.scrollTop).toBe(14 * 304)
      })
    } finally {
      restoreScroll()
      restoreWidth()
    }
  })

  it('anchors from a park in the middle of a row, not only from row boundaries', async () => {
    // Every other anchor test parks on an exact row boundary, so `floor()` is
    // never asked an ambiguous question and the residual the browser shows —
    // `align: 'start'` dropping however far into a row the reader was — has no
    // unit expression at all. This adds both.
    //
    // 480px box, 424 pitch. Row 20 starts at 8480; park 200px into it. The
    // anchor is still row 20 (`floor(8680 / 424) = 20`), and after the resize
    // the reader is flush with row 20 at 6080 — 200px of their position inside
    // the card is gone, which is the cost written up in `SeriesGrid`.
    let contentWidth = 480
    const restoreWidth = stubContentWidth(() => contentWidth)
    const restoreScroll = stubScrolling()
    try {
      scenario.series = Array.from({ length: 60 }, (_, i) =>
        makeSeries({ id: makeId(4_500 + i), name: `[만화] 시리즈 ${(i + 1).toString()}` }),
      )
      renderLibrary()
      await waitForLibrary()
      await screen.findByRole('button', { name: '[만화] 시리즈 1' })

      const scroller = screen.getByTestId('library-scroller')
      const rowAt = (index: number): HTMLElement | null =>
        scroller.querySelector<HTMLElement>(`[data-index="${index.toString()}"]`)

      act(() => {
        scrollTo(scroller, 20 * 424 + 200)
      })
      expect(scroller.scrollTop).toBe(8_680)
      // 200px into row 20, and row 21 has not been reached.
      expect(rowOffset(rowAt(20))).toBe(8_480)

      contentWidth = 320
      act(() => {
        resizeViewport(1_600)
      })

      await waitFor(() => {
        expect(scroller.scrollTop).toBe(6_080)
      })
      expect(rowOffset(rowAt(20))).toBe(6_080)
      // The 200px is the residual, and it is bounded by the card height rather
      // than by the pitch: it is `scrollTop − anchorRow.start`, nothing more.
      expect(8_680 - 8_480).toBe(200)
    } finally {
      restoreScroll()
      restoreWidth()
    }
  })

  it('lands the tier and the measured width in the same commit', async () => {
    // **One resize is not one commit — unless the grid makes it one.** The tier
    // arrives on a `matchMedia` change and the width on a `ResizeObserver`
    // callback, 31–49ms apart in Chrome, and the tier is always first. A grid
    // that reads them through separate hooks therefore renders an intermediate
    // layout from the *new* metrics and the *old* width. `useGridBox` closes
    // that by having both listeners run one reader, so this test fires only the
    // first of the two — the case that used to break — and asserts that the
    // commit it produces is already the settled one.
    //
    // 773px box at `tablet`  → 3 columns, 430.5px cards, 446.5 pitch.
    // 312px box at `mobile`  → 2 columns, 285px cards, 297 pitch — settled.
    // 773px box at `mobile`  → 4 columns (`--grid-min` drops to 150) — the
    //                          intermediate, which must never be rendered.
    //
    // The reader is on row 1, i.e. series 3, which the settled layout puts on
    // row 1 at 297. The intermediate puts it at `floor(3 / 4) = 0`, the top of
    // the library, and its 1 033px track also clamps the reader's 447 away
    // before any effect can read it — the two halves of items `s` and `t`.
    //
    // This replaces a test that asserted the intermediate *as expected
    // behaviour* — 4 children, then `scrollTop === 0`, then a recovery on the
    // settled commit. That was an honest description of the defect and is the
    // wrong assertion now.
    let contentWidth = 773
    const restoreWidth = stubContentWidth(() => contentWidth)
    const restoreScroll = stubScrolling()
    try {
      stubViewport(800)
      scenario.series = Array.from({ length: 12 }, (_, i) =>
        makeSeries({ id: makeId(3_650 + i), name: `[만화] 시리즈 ${(i + 1).toString()}` }),
      )
      renderLibrary()
      await waitForLibrary()
      await screen.findByRole('button', { name: '[만화] 시리즈 1' })

      const scroller = screen.getByTestId('library-scroller')
      const rowAt = (index: number): HTMLElement | null =>
        scroller.querySelector<HTMLElement>(`[data-index="${index.toString()}"]`)

      act(() => {
        scrollTo(scroller, 447)
      })
      expect(rowOffset(rowAt(1))).toBe(446.5)

      // The CSS has already moved the box — that is what makes the synchronous
      // read in `useGridBox` correct — and only the media queries have fired.
      contentWidth = 312
      act(() => {
        crossTier(354)
      })

      // Two columns on the *first* commit after the crossing. Four is the
      // defect, and this is the assertion that catches it: everything below
      // follows from the layout being right, but this is the thing that was
      // wrong.
      expect([...scroller.querySelectorAll('[data-index]')][0]?.children).toHaveLength(2)

      // …and with no intermediate to anchor against, the reader's own 447 is
      // what the anchor is derived from.
      await waitFor(() => {
        expect(scroller.scrollTop).toBe(297)
      })
      expect(rowOffset(rowAt(1))).toBe(297)
    } finally {
      restoreScroll()
      restoreWidth()
    }
  })

  it('re-anchors even when the re-measure leaves the total size unchanged', async () => {
    // The second layout effect is gated on `getTotalSize()`, which fires it only
    // when the total *changed*. A re-measure can move the pitch and leave the
    // total exactly where it was, and then the pending anchor would sit there
    // undischarged until some later, unrelated size change — a pagination
    // append — fired it against a layout two generations old. `anchorGeneration`
    // is what closes that, and this is the transition that reaches it.
    //
    // 12 series, 773px box at `tablet`: 3 columns of 247px → 430.5px cards →
    //   4 rows → 4 × (430.5 + 16) − 16 = 1770.
    // 12 series, 312px box at `mobile`: 2 columns of 150px → 285px cards →
    //   6 rows → 6 × (285 + 12) − 12 = 1770.
    // The wider `--grid-gap` and coarser `--grid-min` above 768 trade against
    // the extra row exactly. Sweeping all four tiers at quarter-pixel resolution
    // turns up thousands of these pairs at every library size, so this is a
    // reachable state and not a contrived one — it is only *hard to stumble on*,
    // which is what makes it worth pinning.
    let contentWidth = 773
    const restoreWidth = stubContentWidth(() => contentWidth)
    const restoreScroll = stubScrolling()
    try {
      stubViewport(800)
      scenario.series = Array.from({ length: 12 }, (_, i) =>
        makeSeries({ id: makeId(3_600 + i), name: `[만화] 시리즈 ${(i + 1).toString()}` }),
      )
      renderLibrary()
      await waitForLibrary()
      await screen.findByRole('button', { name: '[만화] 시리즈 1' })

      const scroller = screen.getByTestId('library-scroller')
      const rowAt = (index: number): HTMLElement | null =>
        scroller.querySelector<HTMLElement>(`[data-index="${index.toString()}"]`)
      const track = (): HTMLElement | null | undefined => rowAt(1)?.parentElement

      act(() => {
        scrollTo(scroller, 446.5)
      })
      expect(rowOffset(rowAt(1))).toBe(446.5)
      expect(track()?.style.height).toBe('1770px')

      contentWidth = 312
      act(() => {
        resizeViewport(700)
      })

      // The premise of the test, asserted rather than assumed: the pitch moved
      // and the total did not, so `getTotalSize()` on its own would never fire
      // the re-anchor.
      expect(track()?.style.height).toBe('1770px')
      expect(rowOffset(rowAt(2)) - rowOffset(rowAt(1))).toBe(297)

      await waitFor(() => {
        expect(scroller.scrollTop).toBe(297)
      })
      expect(rowOffset(rowAt(1))).toBe(297)
    } finally {
      restoreScroll()
      restoreWidth()
    }
  })

  it('re-anchors nothing when the reader is already at the top', async () => {
    // Resizing at the top of the library is the common case, and there is
    // nothing to preserve there — row 0 is flush whatever the pitch is. So the
    // anchor must not fire at all: a `scrollTo` from the top is a jump from the
    // top, which is the one thing worse than a jump.
    let contentWidth = 1_156
    const restoreWidth = stubContentWidth(() => contentWidth)
    const restoreScroll = stubScrolling()
    try {
      scenario.series = Array.from({ length: 60 }, (_, i) =>
        makeSeries({ id: makeId(3_900 + i), name: `[만화] 시리즈 ${(i + 1).toString()}` }),
      )
      renderLibrary()
      await waitForLibrary()
      await screen.findByRole('button', { name: '[만화] 시리즈 1' })

      const scroller = screen.getByTestId('library-scroller')
      expect(scroller.scrollTop).toBe(0)
      scrollCalls = []

      contentWidth = 1_100
      act(() => {
        resizeViewport(1_600)
      })

      await act(async () => {
        await new Promise((resolve) => requestAnimationFrame(() => {
          resolve(undefined)
        }))
      })
      expect(scrollCalls).toEqual([])
      expect(scroller.scrollTop).toBe(0)
      // The re-measure still happened; only the re-anchor was skipped.
      expect(
        rowOffset(scroller.querySelector<HTMLElement>('[data-index="1"]')),
      ).toBe(331)

      // …and row 0 starts at 0. That is not a restatement of the line above: it
      // pins `paddingStart === 0` and `scrollMargin === 0`, the two
      // `useVirtualizer` options the components' `start_i = i × pitch`
      // arithmetic silently assumes. Adding either — a sticky header inside the
      // scroller is the obvious future reason — shifts every row without
      // changing the pitch, and every other assertion in this file would still
      // pass while the re-anchor landed one padding out.
      expect(
        rowOffset(scroller.querySelector<HTMLElement>('[data-index="0"]')),
      ).toBe(0)
    } finally {
      restoreScroll()
      restoreWidth()
    }
  })

  it('opens the series detail from the cover and from 상세, and resumes from the overlay', async () => {
    const user = userEvent.setup()
    renderLibrary()
    await waitForLibrary()

    // The cover is the 상세 target. jsdom does no hit-testing, so this half
    // only proves the handler is wired — that the hover overlay above it does
    // not swallow a real pointer is e2e/01-library.spec.ts 6.1 (overlay)'s job.
    await user.click(await screen.findByRole('button', { name: MONSTER.name }))
    expect(await screen.findByText('series detail')).toBeInTheDocument()

    // …and the overlay's primary action is a *different* destination: 몬스터 is
    // 34% read, so 이어 읽기 resumes `last_book_id` at `?page=42` (the viewer
    // route) rather than repeating the 상세 navigation. Navigating unmounted
    // the library, so the second half needs its own mount; narrowing the
    // scenario to 몬스터 keeps 이어 읽기 unambiguous (아키라 has one too).
    cleanup()
    scenario.series = [MONSTER]
    renderLibrary()
    await waitForLibrary()

    await user.click(await screen.findByRole('button', { name: '이어 읽기' }))
    expect(await screen.findByText('viewer')).toBeInTheDocument()

    // …and the overlay's *second* action is the cover's destination, not the
    // primary's: 상세 is `onOpen`, so it lands on the series screen. Asserting
    // the destination rather than the label is the whole point — the label was
    // already covered, and with only that, rewiring 상세 to `onResume` (which
    // for 몬스터 jumps straight into the viewer at page 42, skipping the screen
    // the button is named after) left every unit test green, and the e2e guard
    // green too back when it only used this button as a `has:` filter and never
    // pressed it. That guard now presses it for real and checks where it lands
    // (`e2e/01-library.spec.ts` 6.1 (overlay buttons)) — but only catches the
    // rewire once something has given 군계 progress, since `resumeSeries` falls
    // back to `onOpen` for a series with none. This assertion is the
    // unconditional one: 몬스터 is 34 % read in the fixture, so `onResume` here
    // would go to the viewer and be caught on every run.
    cleanup()
    renderLibrary()
    await waitForLibrary()

    await user.click(await screen.findByRole('button', { name: '상세' }))
    expect(await screen.findByText('series detail')).toBeInTheDocument()
    expect(screen.queryByText('viewer')).not.toBeInTheDocument()
  })

  /**
   * The overlay's two gates have to be **one** gate.
   *
   * `opacity` lives on the scrim and `pointer-events` on the buttons, so the
   * two can drift apart in either direction — and both directions have already
   * shipped in this codebase:
   *
   *  - *live while invisible*: `VolumeTile`'s read toggle kept winning the hit
   *    test at the top-right of every tile while transparent, and a tap there
   *    flipped persisted read state without opening the volume (`VolumeTile.tsx`
   *    "An invisible control must not be a hit target", guarded by
   *    `e2e/03-series-detail.spec.ts` 6.5 (guard));
   *  - *visible while dead*: this scrim taking pointer events on hover killed
   *    every cover click, because a mouse must hover before it can click
   *    (HANDOFF §5.3 item 1).
   *
   * So what is asserted is the equality, not a class list: the set of
   * conditions that reveals the scrim must be exactly the set that arms each
   * button, and the scrim itself must be armed by nothing at all. A future
   * `(hover: none)` fallback stays green iff it is added to all three.
   *
   * It is a class-attribute assertion because this tier cannot make it any
   * other way, which the `getComputedStyle` line below measures rather than
   * assumes: `vitest.config.ts:22` sets `css: false`, so Tailwind never loads
   * and jsdom answers `auto` for the very element that ships
   * `pointer-events-none`. The consequence, measured: with
   * `group-hover:pointer-events-auto` deleted from 상세, and again with it
   * *added to the scrim* (i.e. §5.3 item 1 re-committed), every other test in
   * this file stayed green — this one is the only thing in the tier that goes
   * red. What the *rendered* gate does is `e2e/01-library.spec.ts`
   * 6.1 (overlay) and 6.1 (overlay buttons), in a real browser; the day `css`
   * is turned on here, that
   * one assertion fails and these become behaviour assertions.
   */
  it('reveals and arms the overlay under the identical gate (ui-spec §4.5/§8.3)', async () => {
    renderLibrary()
    await waitForLibrary()

    // A missing ancestor must stop the test, not silently narrow it to a
    // document-wide query that still passes.
    const parentOf = (el: Element, what: string): HTMLElement => {
      const parent = el.parentElement
      if (parent === null) throw new Error(`${what} has no parent element`)
      return parent
    }

    // 몬스터 is 34 % read, so its primary reads 이어 읽기.
    const cover = await screen.findByRole('button', { name: MONSTER.name })
    const card = parentOf(cover, 'the cover button')

    const detail = within(card).getByRole('button', { name: '상세' })
    const primary = within(card).getByRole('button', { name: '이어 읽기' })
    const scrim = parentOf(detail, 'the 상세 button')
    // Named, so a DOM refactor cannot leave the assertions below pointed at
    // some other div and still passing.
    // `.cover-scrim` (base.css), not `bg-scrim-cover`: E-32 turns the flat 72 %
    // wash into a vertical gradient, which no Tailwind colour utility can name.
    expect(scrim.className, 'both buttons sit inside the inset-0 scrim').toContain('cover-scrim')
    expect(scrim.className).toContain('absolute inset-0')
    expect(scrim).toContainElement(primary)

    // jsdom cannot see any of this: with no stylesheet the element that ships
    // `pointer-events-none` computes to `auto` (measured). That is why what
    // follows reads the class attribute, and it is the assertion that will
    // demand the upgrade if `css` is ever turned on.
    expect(getComputedStyle(detail).pointerEvents).not.toBe('none')

    /** Variants that apply `utility` to `el`, e.g. `group-hover` for `group-hover:opacity-100`. */
    const gates = (el: Element, utility: string): string[] =>
      el.className
        .split(/\s+/)
        .filter((name) => name.endsWith(`:${utility}`))
        .map((name) => name.slice(0, -(utility.length + 1)))
        .sort()

    /** Utilities applied unconditionally, i.e. with no variant prefix. */
    const unconditional = (el: Element): string[] =>
      el.className
        .split(/\s+/)
        .filter((name) => name.includes('pointer-events') && !name.includes(':'))
        .sort()

    const reveal = gates(scrim, 'opacity-100')
    // ui-spec §4.5 (hover reveals it) and §8.3 ("Mirror it on `:focus-within`
    // so keyboard users get the same actions"). A superset is allowed — that
    // is where a touch fallback would go — but neither of these may leave.
    expect(reveal).toContain('group-hover')
    expect(reveal).toContain('group-focus-within')

    // The scrim spans `inset-0` above the cover button, so anything that armed
    // it would eat the cover click for exactly the users who can reach the
    // overlay at all. Nothing arms it, under any condition.
    expect(gates(scrim, 'pointer-events-auto')).toEqual([])
    expect(unconditional(scrim)).toEqual(['pointer-events-none'])

    // …and each button is dead by default and armed by precisely the
    // conditions that made it visible — never one without the other.
    for (const button of [primary, detail]) {
      expect(unconditional(button)).toEqual(['pointer-events-none'])
      expect(gates(button, 'pointer-events-auto')).toEqual(reveal)
    }
  })

  it('labels the overlay action 이어 읽기 only for a started series', async () => {
    renderLibrary()
    await waitForLibrary()

    // 몬스터 (34%) and 아키라 (완독) have both been opened; 군계 has not.
    expect(await screen.findAllByText('이어 읽기')).toHaveLength(2)
    expect(screen.getAllByText('읽기 시작')).toHaveLength(1)
    expect(screen.getAllByText('상세')).toHaveLength(3)
  })
})

// ---------------------------------------------------------------------------
// FR-LIB-002 / FR-LIB-003 — the list, and the sticky toggle
// ---------------------------------------------------------------------------

describe('list mode (FR-LIB-003)', () => {
  beforeEach(() => {
    scenario.settings = { ...scenario.settings, library_view: 'list' }
  })

  it('renders every ui-spec §4.5 column', async () => {
    renderLibrary()
    await waitForLibrary()

    // The four sortable headers are buttons; 형식 and 진행률 are static. The
    // active column (name, the default) carries its direction arrow.
    expect(await screen.findByRole('button', { name: '시리즈명 ↑' })).toBeInTheDocument()
    for (const label of ['권', '용량', '수정일']) {
      expect(screen.getByRole('button', { name: label })).toBeInTheDocument()
    }
    expect(screen.getByText('형식')).toBeInTheDocument()
    expect(screen.getByText('진행률')).toBeInTheDocument()

    const row = await screen.findByRole('button', { name: MONSTER.name })
    expect(within(row).getByText('FOLDER')).toBeInTheDocument()
    expect(within(row).getByText('18권')).toBeInTheDocument()
    expect(within(row).getByText('4.0 GB')).toBeInTheDocument()
    expect(within(row).getByText('2017-02-11')).toBeInTheDocument()
    expect(within(row).getByText('34%')).toBeInTheDocument()
  })

  /**
   * E-32: the rows lose their 1px dividers and gain `.row-chip`, and the whole
   * table is drawn inside one raised card.
   *
   * `.row-chip` carries the hover *and* the radius, and it exists as a class
   * rather than as `hover:bg-neutral-100` at the call site because the light
   * hover is a ramp step and the ramps do not flip with the theme — see the
   * rule and its dark override in base.css, pinned in `ds.test.tsx`. A row that
   * keeps `border-b` here is a row that draws a hairline across a card that has
   * no other hard edge on it.
   */
  it('draws rows as hover chips inside one card, with no dividers (E-32)', async () => {
    renderLibrary()
    await waitForLibrary()

    const row = await screen.findByRole('button', { name: MONSTER.name })
    expect(row).toHaveClass('row-chip')
    expect(row.classList.contains('border-b')).toBe(false)
    expect(row.classList.contains('hover:bg-row-hover')).toBe(false)

    // The card is the list's own root — the header band and the scroller are
    // both inside it, so the two grids stay on one surface.
    const card = screen.getByTestId('library-list-header-wrapper').parentElement
    expect(card).toHaveClass(...LIST_CARD_CLASS.split(/\s+/))
  })

  it('shows 완독 for a finished series and — for an untouched one', async () => {
    renderLibrary()
    await waitForLibrary()

    const done = await screen.findByRole('button', { name: AKIRA.name })
    expect(within(done).getByText('완독')).toBeInTheDocument()

    const unread = screen.getByRole('button', { name: GUNGYE.name })
    expect(within(unread).getByText('—')).toBeInTheDocument()
  })

  it('gives the header and every row the identical column template (acceptance 1)', async () => {
    renderLibrary()
    await waitForLibrary()

    const header = await screen.findByTestId('library-list-header')
    const row = screen.getByRole('button', { name: MONSTER.name })
    // ui-spec §4.5, verbatim. Two grids that merely *look* similar drift the
    // moment one of them is edited.
    expect(header.style.gridTemplateColumns).toBe(
      '32px minmax(0,1fr) 66px 64px 78px 100px 148px',
    )
    expect(row.style.gridTemplateColumns).toBe(header.style.gridTemplateColumns)
  })

  it('reserves the scroller gutter on the header so both grids share a width', async () => {
    // The header is outside the scroller (acceptance 3), so without this the
    // row grid is a scrollbar narrower than the header grid, `minmax(0,1fr)`
    // absorbs the whole difference, and every column from 형식 rightwards sits
    // 12px off. Sharing `LIST_TEMPLATE` does not fix that on its own.
    const restore = stubScrollbarGutter(12)
    try {
      renderLibrary()
      await waitForLibrary()

      const wrapper = await screen.findByTestId('library-list-header-wrapper')
      expect(wrapper.style.paddingRight).toBe('calc(var(--space-4) + 12px)')
      // …and the gutter is reserved unconditionally, so that number does not
      // change when the list stops overflowing.
      expect(screen.getByTestId('library-scroller').style.scrollbarGutter).toBe('stable')
    } finally {
      restore()
    }
  })

  it('gives every numeric cell tabular-nums', async () => {
    renderLibrary()
    await waitForLibrary()

    const row = await screen.findByRole('button', { name: MONSTER.name })
    for (const text of ['18권', '4.0 GB', '2017-02-11', '34%']) {
      expect(within(row).getByText(text)).toHaveClass('tabular-nums')
    }
  })

  it('re-lays out the rows when the row height crosses 768 (open item m)', async () => {
    // The same `virtual-core` memo as the grid's — `estimateSize` is not in the
    // key, so only `measure()` makes a new row height take effect. The list
    // *looks* immune because `rowHeight` is one of two constants rather than a
    // function of the measured width, but 768 moves it: 45px above, 60px below
    // (`LIST_ROW_HEIGHT` / `LIST_ROW_HEIGHT_STACKED`, ui-spec §7).
    //
    // Measured in Chrome on the shipped build (60 synthetic series) before the
    // fix: 1440 → 700 stacked the rows to their two-line shape while the pitch
    // stayed at 45px, so consecutive rows overlapped by 8.7px, and the track
    // stayed 2 700px where a reload at 700 gave 3 600px.
    scenario.series = Array.from({ length: 12 }, (_, i) =>
      makeSeries({ id: makeId(4_000 + i), name: `[만화] 시리즈 ${(i + 1).toString()}` }),
    )
    renderLibrary()
    await waitForLibrary()
    await screen.findByRole('button', { name: '[만화] 시리즈 1' })

    const rowsNow = (): NodeListOf<HTMLElement> =>
      screen.getByTestId('library-scroller').querySelectorAll<HTMLElement>('[data-index]')

    expect(rowsNow()[1]?.style.transform).toBe('translateY(45px)')
    expect(rowsNow()[0]?.parentElement?.style.height).toBe('540px')

    act(() => {
      resizeViewport(700)
    })

    // The row count cannot have invalidated the memo: it is one row per series
    // here, so it is 12 at both widths.
    expect(rowsNow()).toHaveLength(12)
    expect(rowsNow()[1]?.style.transform).toBe('translateY(60px)')
    expect(rowsNow()[0]?.parentElement?.style.height).toBe('720px')
  })

  it('keeps the reader on the row they were on across that re-measure', async () => {
    // The grid's twin, and for the same reason written out there: `measure()`
    // moves every offset and leaves `scrollTop` alone, so without the paired
    // `scrollToIndex` the reader is displaced by `topRowIndex × Δpitch`.
    // 60 rows of 45px, parked on row 10 (scrollTop 450); below 768 the rows are
    // 60px, so row 10 moves to 600.
    const restoreScroll = stubScrolling()
    try {
      scenario.series = Array.from({ length: 60 }, (_, i) =>
        makeSeries({ id: makeId(4_100 + i), name: `[만화] 시리즈 ${(i + 1).toString()}` }),
      )
      renderLibrary()
      await waitForLibrary()
      await screen.findByRole('button', { name: '[만화] 시리즈 1' })

      const scroller = screen.getByTestId('library-scroller')
      const rowAt = (index: number): HTMLElement | null =>
        scroller.querySelector<HTMLElement>(`[data-index="${index.toString()}"]`)

      act(() => {
        scrollTo(scroller, 10 * 45)
      })
      expect(rowOffset(rowAt(10))).toBe(450)
      expect(scroller.scrollTop).toBe(450)

      act(() => {
        resizeViewport(700)
      })

      await waitFor(() => {
        expect(scroller.scrollTop).toBe(600)
      })
      expect(rowOffset(rowAt(10))).toBe(600)
      expect(rowOffset(rowAt(11)) - rowOffset(rowAt(10))).toBe(60)
    } finally {
      restoreScroll()
    }
  })

  it('re-anchors nothing when the reader is already at the top', async () => {
    const restoreScroll = stubScrolling()
    try {
      scenario.series = Array.from({ length: 60 }, (_, i) =>
        makeSeries({ id: makeId(4_200 + i), name: `[만화] 시리즈 ${(i + 1).toString()}` }),
      )
      renderLibrary()
      await waitForLibrary()
      await screen.findByRole('button', { name: '[만화] 시리즈 1' })

      const scroller = screen.getByTestId('library-scroller')
      expect(scroller.scrollTop).toBe(0)
      scrollCalls = []

      act(() => {
        resizeViewport(700)
      })

      await act(async () => {
        await new Promise((resolve) => requestAnimationFrame(() => {
          resolve(undefined)
        }))
      })
      expect(scrollCalls).toEqual([])
      expect(scroller.scrollTop).toBe(0)
      expect(
        rowOffset(scroller.querySelector<HTMLElement>('[data-index="1"]')),
      ).toBe(60)
      // `paddingStart`/`scrollMargin` are 0 here too — see the grid's twin.
      expect(
        rowOffset(scroller.querySelector<HTMLElement>('[data-index="0"]')),
      ).toBe(0)
    } finally {
      restoreScroll()
    }
  })
})

describe('view toggle (FR-LIB-002)', () => {
  it('follows the server-persisted library_view on load', async () => {
    scenario.settings = { ...scenario.settings, library_view: 'list' }
    renderLibrary()
    await waitForLibrary()

    // The list header only exists in list mode.
    expect(await screen.findByText('수정일')).toBeInTheDocument()
    expect(useUiStore.getState().view).toBe('list')
  })

  it('persists a change to both localStorage and PUT /api/settings', async () => {
    renderLibrary()
    await waitForLibrary()
    expect(screen.queryByText('수정일')).not.toBeInTheDocument()

    act(() => {
      useUiStore.getState().setView('list')
    })

    expect(await screen.findByText('수정일')).toBeInTheDocument()
    await waitFor(() => {
      expect(settingsUpdates).toContainEqual(
        expect.objectContaining({ library_view: 'list' }) as unknown as SettingsUpdate,
      )
    })
    expect(localStorage.getItem('shelf.ui')).toContain('"view":"list"')
  })
})

// ---------------------------------------------------------------------------
// Amendment A-5 — the settings write-back
// ---------------------------------------------------------------------------

/**
 * `useLibrarySettingsSync` against **the requests it sends**, not the store.
 *
 * `store/ui.ts` says the server is authoritative once it answers, and
 * `hydrateFromSettings` is the one-way door. The defect this block exists for
 * inverted that: the hydrate effect and the write-back effect both list `data`,
 * so the commit where the payload lands runs both, and a `useRef` flag flipped
 * by the first one let the second run with the **pre-hydration** closure and
 * `PUT` the client's defaults over the payload the server had just sent. It
 * converges, so the screen looks right; the server does not, and two tabs make
 * it a lost update.
 *
 * Two traps shape how this is written (§6.5):
 *
 *  1. **Asserting on the store cannot see it.** Hydration puts the server's
 *     values there whether or not a `PUT` went out. The only observable
 *     difference is the request, so `settingsUpdates` — the same recorder
 *     pattern as `seriesRequests` — is the subject.
 *  2. **`onUnhandledRequest: 'error'` is not an assertion.** `msw/node` fails
 *     the *request*, not the test, and the handler here answers `PUT` anyway.
 *
 * And an empty recorder proves nothing on its own, so the negative case carries
 * its own positive control, through the same recorder and the same flush.
 */
describe('settings write-back (A-5)', () => {
  /**
   * A server payload whose four library values all differ from the store
   * defaults of `store/ui.ts` (`grid` / `name` / `asc` / `all`).
   *
   * That is the whole reason the defect survived: `api/fixtures.ts` ships
   * settings identical to those defaults, so the stale write was byte-identical
   * to the payload and every existing test stayed green.
   */
  const REMOTE = {
    library_view: 'list',
    library_sort: 'mtime',
    library_order: 'desc',
    library_scope: 'reading',
  } as const

  /**
   * Yields long enough for a `PUT` started by an effect to reach MSW and be
   * recorded.
   *
   * How long that is cannot be asserted, only bounded from below, which is why
   * the test below runs the *same* helper over a change that must write: a
   * helper that returned without yielding at all would fail that positive
   * control rather than let the negative `toEqual([])` pass vacuously.
   *
   * That is the whole of what the control shows, and it is worth being exact
   * about it, because "3" looks like a measured number and is not. Swept by
   * hand, the control fails at 0 turns and passes at 1, 2, 3 and 4: it
   * discriminates "yielded" from "did not", not 3 from 1. **Three is headroom,
   * not a demonstrated floor** — do not cite it as one.
   */
  async function flushSettingsWrites(): Promise<void> {
    for (let turn = 0; turn < 3; turn++) {
      await act(async () => {
        await new Promise((resolve) => {
          setTimeout(resolve, 0)
        })
      })
    }
  }

  it('sends no PUT at all when the server payload is the one that wins', async () => {
    scenario.settings = { ...settingsFixture, ...REMOTE }
    renderLibrary()
    await waitForLibrary()

    // Preconditions, asserted rather than assumed: hydration happened, and it
    // moved all four values. Without this the `toEqual([])` below would also be
    // satisfied by a payload that never arrived.
    await waitFor(() => {
      expect(useUiStore.getState().sort).toBe('mtime')
    })
    const hydratedStore = useUiStore.getState()
    expect([
      hydratedStore.view,
      hydratedStore.sort,
      hydratedStore.order,
      hydratedStore.scope,
    ]).toEqual(['list', 'mtime', 'desc', 'reading'])

    await flushSettingsWrites()
    expect(settingsUpdates).toEqual([])

    // Positive control, same recorder and same flush. A genuine local change
    // after hydration must still write back exactly once — otherwise the
    // assertion above would pass just as happily against a client that never
    // writes, or a recorder that never records.
    act(() => {
      useUiStore.getState().setView('grid')
    })
    await flushSettingsWrites()
    expect(settingsUpdates).toEqual([
      {
        library_view: 'grid',
        library_sort: 'mtime',
        library_order: 'desc',
        library_scope: 'reading',
      },
    ])
  })

  it('repairs a library_sort the client cannot read, and sends only that repair', async () => {
    // The one genuine PUT on the hydration path: `isSortKey` rejects the wire
    // value, so `hydrateFromSettings` leaves `sort` alone and the store's own
    // valid key has to travel back. This is what stops the fix from being
    // "never write after hydration" — and the *body* is the discriminator, since
    // the stale write would have carried `grid` / `asc` / `all` with it.
    scenario.settings = {
      ...settingsFixture,
      ...REMOTE,
      library_sort: 'nonsense' as unknown as Settings['library_sort'],
    }
    renderLibrary()
    await waitForLibrary()

    await waitFor(() => {
      expect(useUiStore.getState().view).toBe('list')
    })
    expect(useUiStore.getState().sort).toBe('name')

    await flushSettingsWrites()
    expect(settingsUpdates).toEqual([
      {
        library_view: 'list',
        library_sort: 'name',
        library_order: 'desc',
        library_scope: 'reading',
      },
    ])
  })

  /**
   * The **refetch** path — the same lost update, one door further along.
   *
   * The first fix made `hydrated` a `useState` boolean, which closed the
   * hydrating-commit race but left the flag latched: once true, a settings
   * payload carrying *new* server values never re-hydrated, and the write-back
   * that followed PUT the client's older values straight back over them. It is
   * reachable through `invalidateRootState` (`api/queries.ts`), which invalidates
   * `queryKeys.settings` on every root add and remove — so adding a root in one
   * tab could silently revert the other tab's library preferences on the server.
   *
   * The subject is the request list, for the same two reasons the block header
   * gives: hydration moves the store either way, and `onUnhandledRequest` fails
   * requests rather than tests.
   */
  it('re-hydrates from a refetch carrying new values instead of writing the old ones back', async () => {
    scenario.settings = { ...settingsFixture, ...REMOTE }
    const client = renderLibrary()
    await waitForLibrary()

    await waitFor(() => {
      expect(useUiStore.getState().sort).toBe('mtime')
    })
    await flushSettingsWrites()
    expect(settingsUpdates).toEqual([])

    // The server's values change underneath us — another tab, or this one adding
    // a root — and the key is invalidated exactly as the product invalidates it.
    scenario.settings = {
      ...settingsFixture,
      library_view: 'grid',
      library_sort: 'name',
      library_order: 'asc',
      library_scope: 'all',
    }
    await act(async () => {
      await client.invalidateQueries({ queryKey: queryKeys.settings })
    })

    // The store must follow the server, on all four values.
    await waitFor(() => {
      expect(useUiStore.getState().sort).toBe('name')
    })
    const rehydrated = useUiStore.getState()
    expect([rehydrated.view, rehydrated.sort, rehydrated.order, rehydrated.scope]).toEqual([
      'grid',
      'name',
      'asc',
      'all',
    ])

    // And nothing may have been written back. Before the fix this recorded one
    // PUT carrying list/mtime/desc/reading — the values the server had just
    // replaced.
    await flushSettingsWrites()
    expect(settingsUpdates).toEqual([])

    // Positive control, same recorder and same flush: a genuine local change
    // after the refetch still writes back exactly once, and carries the *new*
    // server values alongside it rather than the pre-refetch ones.
    act(() => {
      useUiStore.getState().setView('list')
    })
    await flushSettingsWrites()
    expect(settingsUpdates).toEqual([
      {
        library_view: 'list',
        library_sort: 'name',
        library_order: 'asc',
        library_scope: 'all',
      },
    ])
  })

  /**
   * `lastSent` remembers one snapshot; a refetch has to forget it.
   *
   * Both cases below were found by review, against a version of the hook that
   * had the reset deleted on the grounds that a mutation removing it stayed
   * green. It did — because no test walked these paths. The lesson is worth more
   * than the tests: **a surviving mutation says the line is unguarded, not that
   * it is unneeded**, and the two are only the same thing if you already know
   * the test set is complete.
   */
  it('writes back a value it has already sent once, after a refetch moved the store off it', async () => {
    scenario.settings = { ...settingsFixture, ...REMOTE }
    const client = renderLibrary()
    await waitForLibrary()
    await waitFor(() => {
      expect(useUiStore.getState().view).toBe('list')
    })

    // The reader picks 그리드. It is sent, and remembered.
    act(() => {
      useUiStore.getState().setView('grid')
    })
    await flushSettingsWrites()
    expect(settingsUpdates).toHaveLength(1)

    // The server's own value comes back — another tab, or `invalidateRootState`
    // on a root add — and the store follows it back to `list`.
    scenario.settings = { ...settingsFixture, ...REMOTE }
    await act(async () => {
      await client.invalidateQueries({ queryKey: queryKeys.settings })
    })
    await waitFor(() => {
      expect(useUiStore.getState().view).toBe('list')
    })

    // The reader picks 그리드 again. Without the reset this is byte-identical to
    // the remembered snapshot and is dropped on the floor: the screen shows
    // 그리드 and the server keeps 리스트.
    act(() => {
      useUiStore.getState().setView('grid')
    })
    await flushSettingsWrites()
    expect(settingsUpdates.slice(1)).toEqual([
      {
        library_view: 'grid',
        library_sort: 'mtime',
        library_order: 'desc',
        library_scope: 'reading',
      },
    ])
  })

  it('still repairs an unreadable library_sort that arrives on a refetch, not just on the first load', async () => {
    scenario.settings = { ...settingsFixture, ...REMOTE }
    const client = renderLibrary()
    await waitForLibrary()
    await waitFor(() => {
      expect(useUiStore.getState().sort).toBe('mtime')
    })

    // Any earlier write-back is enough to arm the trap — it is what puts a
    // snapshot in `lastSent`.
    act(() => {
      useUiStore.getState().setView('grid')
    })
    await flushSettingsWrites()
    expect(settingsUpdates).toHaveLength(1)

    // Now the server hands over a sort the client cannot read. `hydrateFromSettings`
    // leaves `sort` alone, so the store's own valid key has to travel back — the
    // one genuine PUT on a hydration path.
    scenario.settings = {
      ...settingsFixture,
      ...REMOTE,
      library_view: 'grid',
      library_sort: 'nonsense' as unknown as Settings['library_sort'],
    }
    await act(async () => {
      await client.invalidateQueries({ queryKey: queryKeys.settings })
    })

    await flushSettingsWrites()
    expect(settingsUpdates.slice(1)).toEqual([
      {
        library_view: 'grid',
        library_sort: 'mtime',
        library_order: 'desc',
        library_scope: 'reading',
      },
    ])
  })
})

// ---------------------------------------------------------------------------
// FR-LIB-004 — sorting
// ---------------------------------------------------------------------------

describe('sorting (FR-LIB-004)', () => {
  beforeEach(() => {
    scenario.settings = { ...scenario.settings, library_view: 'list' }
  })

  const KNOWN = new Set([GUNGYE.name, MONSTER.name, AKIRA.name])

  /** The series rows, in DOM order — the 이어보기 card is not one of them. */
  const rowNames = (): string[] =>
    screen
      .getAllByRole('button')
      .map((el) => el.getAttribute('aria-label'))
      .filter((label): label is string => label !== null)
      .filter((label) => KNOWN.has(label))

  it('sorts 용량 descending on the first click and flips on the second', async () => {
    const user = userEvent.setup()
    renderLibrary()
    await waitForLibrary()
    expect(rowNames()).toEqual([GUNGYE.name, MONSTER.name, AKIRA.name])

    await user.click(screen.getByRole('button', { name: '용량' }))
    await waitFor(() => {
      expect(lastSeriesRequest().get('sort')).toBe('size')
    })
    expect(lastSeriesRequest().get('order')).toBe('desc')
    await waitFor(() => {
      expect(rowNames()).toEqual([MONSTER.name, AKIRA.name, GUNGYE.name])
    })
    expect(screen.getByRole('button', { name: '용량 ↓' })).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: '용량 ↓' }))
    await waitFor(() => {
      expect(lastSeriesRequest().get('order')).toBe('asc')
    })
    await waitFor(() => {
      expect(rowNames()).toEqual([GUNGYE.name, AKIRA.name, MONSTER.name])
    })
    expect(screen.getByRole('button', { name: '용량 ↑' })).toBeInTheDocument()
  })

  it('sorts 시리즈명 ascending on the first click (ui-spec §4.5)', async () => {
    const user = userEvent.setup()
    renderLibrary()
    await waitForLibrary()

    await user.click(screen.getByRole('button', { name: '권' }))
    await waitFor(() => {
      expect(lastSeriesRequest().get('sort')).toBe('books')
    })
    expect(lastSeriesRequest().get('order')).toBe('desc')

    await user.click(screen.getByRole('button', { name: '시리즈명' }))
    await waitFor(() => {
      expect(lastSeriesRequest().get('sort')).toBe('name')
    })
    expect(lastSeriesRequest().get('order')).toBe('asc')
    expect(screen.getByRole('button', { name: '시리즈명 ↑' })).toBeInTheDocument()
  })

  it('never flashes the skeleton while re-sorting', async () => {
    const user = userEvent.setup()
    scenario.continueItems = [makeContinueItem()]
    renderLibrary()
    await waitForLibrary()
    await screen.findByText('이어보기')

    // Hold the *second* request open so the loading window is observable.
    scenario.hold = true
    await user.click(screen.getByRole('button', { name: '용량' }))
    await waitFor(() => {
      expect(lastSeriesRequest().get('sort')).toBe('size')
    })

    // The new sort is a new query key, so `data` is undefined until it lands;
    // the previous rows must stay on screen (C-10's keepPreviousData policy).
    expect(screen.queryByTestId('library-skeleton')).not.toBeInTheDocument()
    expect(screen.getByText('이어보기')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: GUNGYE.name })).toBeInTheDocument()

    act(() => {
      releaseSeries()
    })
    await waitFor(() => {
      expect(rowNames()).toEqual([MONSTER.name, AKIRA.name, GUNGYE.name])
    })
  })

  it('sends 최근 읽은 순 and 수정일 from the top bar select', async () => {
    renderLibrary()
    await waitForLibrary()

    act(() => {
      useUiStore.getState().setSort('recent')
    })
    await waitFor(() => {
      expect(lastSeriesRequest().get('sort')).toBe('recent')
    })
    expect(lastSeriesRequest().get('order')).toBe('desc')

    act(() => {
      useUiStore.getState().setSort('mtime')
    })
    await waitFor(() => {
      expect(lastSeriesRequest().get('sort')).toBe('mtime')
    })
  })
})

// ---------------------------------------------------------------------------
// FR-LIB-005 — root and smart-list scopes
// ---------------------------------------------------------------------------

describe('scopes (FR-LIB-005, amendment A-4)', () => {
  it('filters by root and names the root in the section header', async () => {
    renderLibrary()
    await waitForLibrary()

    act(() => {
      useUiStore.getState().setScope('scan')
    })

    await waitFor(() => {
      expect(lastSeriesRequest().getAll('root')).toEqual(['scan'])
    })
    expect(await screen.findByText('03. scan (PDF)')).toBeInTheDocument()
    await waitFor(() => {
      expect(screen.queryByRole('button', { name: GUNGYE.name })).not.toBeInTheDocument()
    })
    expect(screen.getByRole('button', { name: AKIRA.name })).toBeInTheDocument()
    expect(screen.getByText('1개 시리즈')).toBeInTheDocument()
  })

  it('drives 읽는 중 / 완독 through progress= and 최근 추가 through scope=added', async () => {
    renderLibrary()
    await waitForLibrary()

    act(() => {
      useUiStore.getState().setScope('reading')
    })
    await waitFor(() => {
      expect(lastSeriesRequest().get('progress')).toBe('reading')
    })
    expect(await screen.findByText('읽는 중')).toBeInTheDocument()

    act(() => {
      useUiStore.getState().setScope('done')
    })
    await waitFor(() => {
      expect(lastSeriesRequest().get('progress')).toBe('done')
    })

    act(() => {
      useUiStore.getState().setScope('added')
    })
    // A-8 / ruling E-9: `scope=added` is the filter; `sort=added&order=desc` is
    // only the ordering *within* it. Sending the sort alone listed the whole
    // library under the 최근 추가 heading.
    await waitFor(() => {
      expect(lastSeriesRequest().get('scope')).toBe('added')
    })
    expect(lastSeriesRequest().get('sort')).toBe('added')
    expect(lastSeriesRequest().get('order')).toBe('desc')
    expect(lastSeriesRequest().get('progress')).toBeNull()
  })
})

// ---------------------------------------------------------------------------
// FR-LIB-006 — search, including 초성
// ---------------------------------------------------------------------------

describe('search (FR-LIB-006, C-10)', () => {
  it('sends a 초성 query to the server and highlights the matched span', async () => {
    renderLibrary()
    await waitForLibrary()

    act(() => {
      useUiStore.getState().setQuery('ㄱㄱ')
    })

    await waitFor(() => {
      expect(lastSeriesRequest().get('q')).toBe('ㄱㄱ')
    })
    await waitFor(() => {
      expect(screen.queryByRole('button', { name: MONSTER.name })).not.toBeInTheDocument()
    })

    const highlighted = screen.getByText('군계')
    expect(highlighted).toHaveClass('text-accent-text')
  })

  it('debounces the field into one request (150 ms)', async () => {
    renderLibrary()
    await waitForLibrary()
    const before = seriesRequests.length

    // Three keystrokes 100 ms apart — under the 150 ms delay, so each one
    // restarts it and only the last survives. Batching them into a single
    // `act()` would not test this at all: React would coalesce them into one
    // render, `useDebouncedValue` would only ever see the final value, and the
    // assertion below would hold with the debounce deleted outright.
    vi.useFakeTimers({ shouldAdvanceTime: true })
    try {
      for (const keystroke of ['군', '군계', '군계 ']) {
        act(() => {
          useUiStore.getState().setQuery(keystroke)
        })
        await act(async () => {
          await vi.advanceTimersByTimeAsync(100)
        })
        expect(seriesRequests.length - before).toBe(0)
      }
      await act(async () => {
        await vi.advanceTimersByTimeAsync(200)
      })
    } finally {
      vi.useRealTimers()
    }

    await waitFor(() => {
      expect(lastSeriesRequest().get('q')).toBe('군계')
    })
    expect(seriesRequests.length - before).toBe(1)
  })

  it('shows the no-results band, and 검색 지우기 restores the library', async () => {
    const user = userEvent.setup()
    renderLibrary()
    await waitForLibrary()

    act(() => {
      useUiStore.getState().setQuery('ㅋㅋㅋ존재하지않음')
    })

    expect(await screen.findByText('검색 결과 없음')).toBeInTheDocument()
    expect(
      screen.getByText('초성 검색도 지원합니다. 다른 표기를 시도해 보세요.'),
    ).toBeInTheDocument()
    expect(screen.getByText('0개 시리즈')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: '검색 지우기' }))
    expect(useUiStore.getState().query).toBe('')
    expect(await screen.findByRole('button', { name: GUNGYE.name })).toBeInTheDocument()
  })

  it('does not claim a search that never happened when a scope is simply empty', async () => {
    // data-survey: a fresh library has 963 series and none of them read, so
    // 완독 legitimately returns `total: 0` with no `q` on the wire. Labelling
    // that 검색 결과 없음 tells the user their search failed.
    scenario.series = []
    renderLibrary()
    await waitForLibrary()

    expect(await screen.findByText('시리즈가 없습니다')).toBeInTheDocument()
    expect(screen.queryByText('검색 결과 없음')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '검색 지우기' })).not.toBeInTheDocument()
    expect(lastSeriesRequest().get('q')).toBeNull()
    expect(screen.getByText('0개 시리즈')).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Failure — a 500 is not an empty library
// ---------------------------------------------------------------------------

describe('request failure', () => {
  it('renders an error band rather than an empty library when /api/series 500s', async () => {
    const user = userEvent.setup()
    server.use(
      http.get(`${ORIGIN}/api/series`, ({ request }) => {
        seriesRequests.push(new URL(request.url))
        return HttpResponse.json(
          { error: { code: 'internal', message: 'boom' } },
          { status: 500 },
        )
      }),
    )
    renderLibrary()

    expect(await screen.findByText('목록을 불러오지 못했습니다')).toBeInTheDocument()
    // The failure must not be dressed as an empty result set, and above all
    // must not be labelled with copy asserting a search.
    expect(screen.queryByText('검색 결과 없음')).not.toBeInTheDocument()
    expect(screen.queryByText('시리즈가 없습니다')).not.toBeInTheDocument()

    const before = seriesRequests.length
    await user.click(screen.getByRole('button', { name: '다시 시도' }))
    await waitFor(() => {
      expect(seriesRequests.length).toBeGreaterThan(before)
    })
  })
})

// ---------------------------------------------------------------------------
// FR-LIB-007 — virtualisation
// ---------------------------------------------------------------------------

describe('virtualisation (FR-LIB-007, NFR-PRF-003)', () => {
  function thousandSeries(): SeriesSummary[] {
    return Array.from({ length: 1_000 }, (_, i) =>
      makeSeries({ id: makeId(1_000 + i), name: `[만화] 시리즈 ${(i + 1).toString()}` }),
    )
  }

  it('renders a window, not a thousand cards', async () => {
    scenario.series = thousandSeries()
    renderLibrary()
    await waitForLibrary()

    await screen.findByText('1,000개 시리즈')
    const cards = screen.getAllByRole('button').filter((el) => {
      return el.getAttribute('aria-label')?.startsWith('[만화] 시리즈') === true
    })
    // One page is 60 items; the 900px window shows a fraction of it.
    expect(cards.length).toBeGreaterThan(2)
    expect(cards.length).toBeLessThan(40)
  })

  it('renders a window in list mode too — the list is not a fallback', async () => {
    scenario.series = thousandSeries()
    scenario.settings = { ...scenario.settings, library_view: 'list' }
    renderLibrary()
    await waitForLibrary()

    await screen.findByText('1,000개 시리즈')
    const rows = screen.getAllByRole('button').filter((el) => {
      return el.getAttribute('aria-label')?.startsWith('[만화] 시리즈') === true
    })
    expect(rows.length).toBeGreaterThan(2)
    expect(rows.length).toBeLessThan(60)
  })

  it('fetches the next page when the window reaches the end', async () => {
    scenario.series = Array.from({ length: 65 }, (_, i) =>
      makeSeries({ id: makeId(2_000 + i), name: `[만화] 시리즈 ${(i + 1).toString()}` }),
    )
    // A window tall enough to render the whole first page at once.
    stubRects(100_000)
    renderLibrary()
    await waitForLibrary()

    await waitFor(() => {
      expect(seriesRequests.map((u) => u.searchParams.get('offset'))).toContain('60')
    })
    expect(seriesRequests[0]?.searchParams.get('limit')).toBe('60')
  })
})

// ---------------------------------------------------------------------------
// FR-LIB-008 — covers answer 202 while queued
// ---------------------------------------------------------------------------

describe('covers (FR-LIB-008, FR-THM-003)', () => {
  it('shows the fallback while a cover is queued, then fades the image in over it', async () => {
    scenario.cover = 'queued'
    scenario.series = [{ ...GUNGYE, has_cover: true, cover_cv: 'a1b2c3d4e5f60718' }]
    renderLibrary()
    await waitForLibrary()

    // 202 is not an error: the fallback renders and the retry is silent. The
    // handler answers 202 three times before the bytes, so a card that gave up
    // on the first queued response would never show an image.
    expect(await screen.findByText('ZIP · NO THUMBNAIL')).toBeInTheDocument()

    await waitFor(() => {
      expect(document.querySelector('img')).not.toBeNull()
    })
    expect(coverRequests.length).toBe(4)
    // The fallback is never removed — that is what keeps CLS at zero.
    expect(screen.getByText('ZIP · NO THUMBNAIL')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: GUNGYE.name })).toBeInTheDocument()
  })

  it('keeps the fallback and never renders an error when the cover 404s', async () => {
    scenario.cover = 'missing'
    scenario.series = [{ ...GUNGYE, has_cover: true, cover_cv: 'a1b2c3d4e5f60718' }]
    renderLibrary()
    await waitForLibrary()

    expect(await screen.findByText('ZIP · NO THUMBNAIL')).toBeInTheDocument()
    await waitFor(() => {
      expect(coverRequests.length).toBeGreaterThan(0)
    })
    expect(document.querySelector('img')).toBeNull()
    expect(screen.getByRole('button', { name: GUNGYE.name })).toBeInTheDocument()
  })

  it('does not request a cover for a series that has none', async () => {
    renderLibrary()
    await waitForLibrary()
    await screen.findByRole('button', { name: GUNGYE.name })
    expect(coverRequests).toEqual([])
  })
})

// ---------------------------------------------------------------------------
// FR-LIB-010 — the 이어보기 shelf
// ---------------------------------------------------------------------------

describe('이어보기 (FR-LIB-010)', () => {
  it('is absent entirely when nothing is in progress', async () => {
    renderLibrary()
    await waitForLibrary()
    await screen.findByRole('button', { name: GUNGYE.name })

    expect(screen.queryByText('이어보기')).not.toBeInTheDocument()
    expect(screen.queryByRole('region', { name: '이어보기' })).not.toBeInTheDocument()
  })

  it('appears with a count, a page counter and a resume target', async () => {
    const user = userEvent.setup()
    scenario.continueItems = [makeContinueItem()]
    renderLibrary()
    await waitForLibrary()

    expect(await screen.findByText('이어보기')).toBeInTheDocument()
    expect(screen.getByText('1개')).toBeInTheDocument()
    expect(screen.getByText('42 / 187p')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: /군계\(軍鷄\) 01권\.zip/ }))
    expect(await screen.findByText('viewer')).toBeInTheDocument()
  })

  /**
   * **E-45 §6 — the counter's denominator is `book.page_count`.**
   *
   * `progress.page_count` is the stale-detection baseline (arch §3.4), and E-45
   * §2 made the server preserve it across writes the reader never acknowledged.
   * The two fields then disagree, and only the index's current length is a
   * denominator: a 10-page file that grew to 190 leaves a reader who saw 10
   * pages at 5 %, not at `10 / 10p`.
   *
   * `makeContinueItem()` carries 187 in both fields, so the counter assertion in
   * the test above passes with either one. This one does not.
   */
  it('counts against the index length, not the stale baseline (E-45 §6)', async () => {
    const grew = makeContinueItem()
    grew.book.page_count = 190
    grew.progress.page_count = 10
    grew.progress.last_page = 10
    scenario.continueItems = [grew]
    renderLibrary()
    await waitForLibrary()

    expect(await screen.findByText('10 / 190p')).toBeInTheDocument()
    expect(screen.queryByText('10 / 10p')).not.toBeInTheDocument()

    // The bar has to agree with the counter — they are two readings of the same
    // fraction, and the old denominator drew a full bar next to `10 / 190p`.
    const card = screen.getByRole('button', { name: /군계\(軍鷄\) 01권\.zip/ })
    expect(within(card).getByRole('progressbar')).toHaveAttribute('aria-valuenow', '5')
  })

  it('counts against the index length when the file shrank too (E-45 §6)', async () => {
    // 190 → 10 with the reader clamped to the last page that exists: 완독, and a
    // full bar. The preserved baseline would have drawn 5 %.
    const shrank = makeContinueItem()
    shrank.book.page_count = 10
    shrank.progress.page_count = 190
    shrank.progress.last_page = 10
    scenario.continueItems = [shrank]
    renderLibrary()
    await waitForLibrary()

    expect(await screen.findByText('10 / 10p')).toBeInTheDocument()
    const card = screen.getByRole('button', { name: /군계\(軍鷄\) 01권\.zip/ })
    expect(within(card).getByRole('progressbar')).toHaveAttribute('aria-valuenow', '100')
  })

  it('draws an empty bar rather than a full one when the volume lost its pages', async () => {
    // `status !== "ok"` books report `page_count: 0` (arch §4.11), and the card
    // divides by that: the ratio it hands down really is `Infinity`.
    //
    // **This assertion is what defends `ProgressBar`'s non-finite fallback**,
    // and it is the only thing that does at this seam. The comment here used to
    // credit a `total > 0` guard in `ContinueCard` instead — measured, and
    // false: that guard could be deleted with all 69 cases still green, because
    // `Math.min(1, Infinity)` is 1 and only `Number.isFinite` in `ProgressBar`
    // turns it back into an empty trough. The guard is gone; this line stayed.
    const gone = makeContinueItem()
    gone.book.page_count = 0
    gone.book.status = 'error'
    scenario.continueItems = [gone]
    renderLibrary()
    await waitForLibrary()

    expect(await screen.findByText('42 / 0p')).toBeInTheDocument()
    const card = screen.getByRole('button', { name: /군계\(軍鷄\) 01권\.zip/ })
    expect(within(card).getByRole('progressbar')).toHaveAttribute('aria-valuenow', '0')
  })

  it('stays hidden during the skeleton state', async () => {
    scenario.hold = true
    scenario.continueItems = [makeContinueItem()]
    renderLibrary()

    expect(await screen.findByTestId('library-skeleton')).toBeInTheDocument()
    await waitFor(() => {
      expect(screen.getByText('전체 시리즈')).toBeInTheDocument()
    })
    expect(screen.queryByText('이어보기')).not.toBeInTheDocument()

    act(() => {
      releaseSeries()
    })
    expect(await screen.findByText('이어보기')).toBeInTheDocument()
  })

  /**
   * The card width, pinned on the class list (E-37 §4).
   *
   * The point of this test is **not** that 218 and 269 are good numbers — it is
   * that nothing anywhere else fails when they change. ui-spec §4.3 carried
   * `flex:0 0 300px` from the first commit while this component shipped 272/336
   * and then 218/269, and no check in five gates ever noticed, because no check
   * looked: the width is not a behaviour, so no unit, e2e or contrast test has
   * an opinion about it. That is how a spec number survives ten sessions of
   * being wrong.
   *
   * So this assertion exists to make the spec and this class list **change in
   * the same edit**. If you are here because it went red, the fix is to update
   * `ui-spec.md` §4.3 and the 이어보기 column of the §7 matrix and this line —
   * not to relax the assertion.
   *
   * jsdom does no layout, so this reads the class list rather than a measured
   * width; the geometry it stands for was measured in Chrome (218 − 96 cover −
   * 12 gap − 24 padding = an 86px text column).
   */
  it('pins the card width so the spec cannot drift from it again (E-37)', async () => {
    scenario.continueItems = [makeContinueItem()]
    renderLibrary()
    await waitForLibrary()

    const card = await screen.findByRole('button', { name: /군계\(軍鷄\) 01권\.zip/ })
    expect(card.className).toContain('flex-[0_0_218px]')
    expect(card.className).toContain('md:flex-[0_0_269px]')
  })
})

// ---------------------------------------------------------------------------
// Loading and first run
// ---------------------------------------------------------------------------

describe('skeleton (prd §5.3, WP-09 acceptance 9)', () => {
  it('renders 18 cells in the geometry the grid is about to occupy', async () => {
    scenario.hold = true
    renderLibrary()

    const skeleton = await screen.findByTestId('library-skeleton')
    // Same 2:3 covers, same padding, same token-driven template: nothing moves
    // when the real cards replace them.
    expect(skeleton.querySelectorAll('.aspect-\\[2\\/3\\]')).toHaveLength(18)
    expect(skeleton).toHaveClass('p-4')
    expect(skeleton.style.gridTemplateColumns).toBe(
      'repeat(auto-fill, minmax(var(--grid-min), 1fr))',
    )

    act(() => {
      releaseSeries()
    })
    await waitFor(() => {
      expect(screen.queryByTestId('library-skeleton')).not.toBeInTheDocument()
    })
  })

  it('uses a row-shaped skeleton in list mode', async () => {
    scenario.hold = true
    scenario.settings = { ...scenario.settings, library_view: 'list' }
    renderLibrary()

    await waitFor(() => {
      expect(useUiStore.getState().view).toBe('list')
    })
    const skeleton = await screen.findByTestId('library-skeleton')
    expect(skeleton.querySelectorAll('.aspect-\\[2\\/3\\]')).toHaveLength(0)

    act(() => {
      releaseSeries()
    })
    await waitFor(() => {
      expect(screen.queryByTestId('library-skeleton')).not.toBeInTheDocument()
    })
  })

  it('reserves the list sort-header band, so the table does not drop when rows land', async () => {
    // The loaded list prepends a header the skeleton must also occupy; without
    // it every row jumps down by the band's height. The Layout Instability API
    // scores that transition 0 — the skeleton nodes are *removed* and different
    // nodes inserted rather than moved — so acceptance 9's Playwright assertion
    // cannot see it. This can: the two bands must be the same box.
    scenario.hold = true
    scenario.settings = { ...scenario.settings, library_view: 'list' }
    renderLibrary()

    await waitFor(() => {
      expect(useUiStore.getState().view).toBe('list')
    })
    const band = await screen.findByTestId('library-skeleton-header')
    const bandWrapper = band.parentElement

    act(() => {
      releaseSeries()
    })
    const header = await screen.findByTestId('library-list-header')
    expect(header.className).toBe(band.className)
    expect(header.parentElement?.className).toBe(bandWrapper?.className)
    // E-32 put the whole table inside a card with its own margin and padding.
    // The skeleton has to be inside the identical one, or the band comparison
    // above is satisfied while every row still moves by 16px when data lands —
    // which the Layout Instability API cannot see either.
    expect(bandWrapper?.parentElement).toHaveClass(...LIST_CARD_CLASS.split(/\s+/))
  })
})

/**
 * jsdom has no clipboard at all, so every test that cares what `설정 파일 위치
 * 보기` hands the user has to install one. Returns the list of strings written;
 * the caller deletes the property again in a `finally`.
 *
 * **Call this after `userEvent.setup()`, never before**: `setup()` installs its
 * own `navigator.clipboard` stub, so the reverse order leaves this recorder
 * detached and every `expect(written).toEqual([…])` reading `[]` — which looks
 * exactly like "the button copied nothing", the very state these tests
 * distinguish.
 */
function stubClipboard(): string[] {
  const written: string[] = []
  Object.defineProperty(navigator, 'clipboard', {
    configurable: true,
    value: {
      writeText: (text: string) => {
        written.push(text)
        return Promise.resolve()
      },
    },
  })
  return written
}

describe('onboarding — no roots (ui-spec §4.6, C-5)', () => {
  beforeEach(() => {
    scenario.roots = []
  })

  it('replaces the screen and offers the config path, not a 루트 추가 button', async () => {
    renderLibrary()

    expect(await screen.findByText('읽을 폴더를 등록하세요')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '설정 파일 위치 보기' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '설정' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '루트 추가' })).not.toBeInTheDocument()
    // Amendment A-10 / ruling E-25: the *resolved* file from `GET /api/settings`,
    // not the bare name — the lookup order has four candidates and the reader
    // has no way to tell which one this server loaded.
    expect(await screen.findByText(settingsFixture.server.config_path)).toBeInTheDocument()
    expect(screen.getByText('shelf.yaml을 편집한 뒤 재시작하세요')).toBeInTheDocument()
    expect(
      screen.getByText(
        'ZIP · 폴더 · PDF가 담긴 루트를 지정하면 압축을 풀지 않고 그대로 훑어 시리즈로 정리합니다.',
      ),
    ).toBeInTheDocument()
  })

  it('copies the resolved path, not the bare file name (A-10)', async () => {
    // `설정 파일 위치 보기` exists to hand the user something they can paste into
    // an editor; the file's name is not that. jsdom has no clipboard at all, so
    // the stub is also the assertion that the guard in `Onboarding` is a guard
    // and not a swallow.
    const user = userEvent.setup()
    const written = stubClipboard()
    try {
      renderLibrary()
      await screen.findByText(settingsFixture.server.config_path)
      await user.click(screen.getByRole('button', { name: '설정 파일 위치 보기' }))
      expect(written).toEqual([settingsFixture.server.config_path])
    } finally {
      Reflect.deleteProperty(navigator, 'clipboard')
    }
  })

  /**
   * The window nothing covered: `/api/roots` has answered, `/api/settings` has
   * not.
   *
   * `LibraryPage` renders this screen on `rootsLoaded && rootCount === 0`, and
   * those are two different requests — so there is a real interval in which the
   * screen is up and `config_path` is unknown. `Onboarding` used to fill it with
   * a `configPath = 'shelf.yaml'` parameter default, which put the bare name on
   * screen *and on the clipboard*: precisely the defect E-25 exists to remove,
   * surviving inside the fix for it.
   */
  it('names no file, and copies none, while GET /api/settings is in flight (A-10)', async () => {
    scenario.holdSettings = true
    const user = userEvent.setup()
    const written = stubClipboard()
    try {
      renderLibrary()
      await screen.findByText('읽을 폴더를 등록하세요')

      expect(screen.queryByTestId('onboarding-config-path')).not.toBeInTheDocument()
      expect(screen.queryByText('shelf.yaml')).not.toBeInTheDocument()
      const copy = screen.getByRole('button', { name: '설정 파일 위치 보기' })
      expect(copy).toBeDisabled()
      await user.click(copy)
      expect(written).toEqual([])

      // A disabled control is a promise; this is the promise being kept. One
      // request later the path is on screen and the button works — which is
      // also why disabling beats removing here.
      act(() => {
        releaseSettings()
      })
      expect(await screen.findByText(settingsFixture.server.config_path)).toBeInTheDocument()
      await waitFor(() => {
        expect(screen.getByRole('button', { name: '설정 파일 위치 보기' })).toBeEnabled()
      })
      await user.click(screen.getByRole('button', { name: '설정 파일 위치 보기' }))
      expect(written).toEqual([settingsFixture.server.config_path])
    } finally {
      Reflect.deleteProperty(navigator, 'clipboard')
    }
  })

  /**
   * `config_path: ""` — arch §7.8, "only for a server built from a
   * configuration with no file". Unreachable through `config.Load`, reachable
   * through `config.Parse`, and promised by the type either way.
   *
   * `""` is not a shorter path, it is the absence of one, so it has to land on
   * the same branch as "not answered yet" — and it must not reach the DOM as an
   * empty element, which would look like a path that failed to render and would
   * satisfy a test that asked only whether the element was there.
   */
  it('treats an empty config_path as no path at all (arch §7.8)', async () => {
    scenario.settings = {
      ...settingsFixture,
      // The settle signal, and it has to be one: with `config_path: ''` the
      // screen looks the same before and after the payload lands, so waiting on
      // the screen — or on `settingsReads`, which counts requests sent, not
      // responses consumed — would let this pass against a `/api/settings` that
      // never answered, i.e. assert the loading state and call it this one.
      // `useLibrarySettingsSync` hydrates the store from the payload, and the
      // `beforeEach` above puts `view` at `grid`.
      library_view: 'list',
      server: { ...settingsFixture.server, config_path: '' },
    }
    const user = userEvent.setup()
    const written = stubClipboard()
    try {
      renderLibrary()
      await screen.findByText('읽을 폴더를 등록하세요')
      await waitFor(() => {
        expect(useUiStore.getState().view).toBe('list')
      })

      expect(screen.queryByTestId('onboarding-config-path')).not.toBeInTheDocument()
      const copy = screen.getByRole('button', { name: '설정 파일 위치 보기' })
      expect(copy).toBeDisabled()
      await user.click(copy)
      expect(written).toEqual([])
      // The instruction survives; only the claim about *which* file is dropped.
      expect(screen.getByText('shelf.yaml을 편집한 뒤 재시작하세요')).toBeInTheDocument()
    } finally {
      Reflect.deleteProperty(navigator, 'clipboard')
    }
  })

  it('never asks the server for a series list when there are no roots', async () => {
    renderLibrary()
    await screen.findByText('읽을 폴더를 등록하세요')
    expect(seriesRequests).toEqual([])
  })

  it('opens the settings overlay from 설정', async () => {
    const user = userEvent.setup()
    renderLibrary()

    await user.click(await screen.findByRole('button', { name: '설정' }))
    expect(useUiStore.getState().overlays).toContain('settings')
  })

  /**
   * Amendment A-11 (ruling E-26). The design's onboarding primary button is
   * `+ 루트 추가`, and after E-26 there is an endpoint behind it — but only
   * when `Settings.server.root_editing_enabled` is true. With it false the
   * answer stays C-5's `설정 파일 위치 보기`, which is the test directly above.
   *
   * The capability comes down `GET /api/settings` exactly as `config_path`
   * does. `LibraryPage` is the one that reads it, so this drives the whole path
   * — MSW → `useSettings` → `LibraryPage` → `Onboarding` — and no test here
   * hands the flag to the component.
   */
  it('offers 루트 추가 on first run once the capability is on (A-11)', async () => {
    scenario.settings = {
      ...settingsFixture,
      server: { ...settingsFixture.server, root_editing_enabled: true },
    }
    const user = userEvent.setup()
    renderLibrary()

    await user.click(await screen.findByRole('button', { name: '루트 추가' }))
    await user.type(screen.getByLabelText('루트 경로'), '/srv/media/manga')
    await user.click(screen.getByRole('button', { name: '추가' }))

    await waitFor(() => {
      expect(rootPosts).toEqual([{ path: '/srv/media/manga' }])
    })
    // The screen leaves onboarding because `GET /api/roots` was re-read and now
    // reports a root — not because the client decided the POST had worked.
    await waitFor(() => {
      expect(screen.queryByText('읽을 폴더를 등록하세요')).not.toBeInTheDocument()
    })
  })

  it('keeps 설정 파일 위치 보기 and offers no 루트 추가 while the capability is off (C-5)', async () => {
    // The `false` half of the same boolean, against the same screen — a flag
    // asserted only in its `true` state is a flag that has not been tested.
    renderLibrary()
    await screen.findByText(settingsFixture.server.config_path)

    expect(screen.getByRole('button', { name: '설정 파일 위치 보기' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '루트 추가' })).not.toBeInTheDocument()
    expect(screen.queryByLabelText('루트 경로')).not.toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Ruling E-34 §2 — the reveal has to be able to *reach* the series
// ---------------------------------------------------------------------------

/**
 * The half of the reveal that arming alone does not buy.
 *
 * `App.tsx` and `ViewerPage.tsx` leave a series id in `store/ui.ts`, and both
 * library surfaces locate it by its index in `items`. `items` is whatever
 * `useSeriesListInfinite` is holding — and that cache is **transient**:
 * `main.tsx` sets no `gcTime`, so react-query's five-minute default applies, and
 * the library's query has no observer at all while the reader is inside a book.
 *
 * Measured on the real collection before this fix: 14 pages paged in, parked
 * 40 931px down, five and a half minutes in the viewer, then 라이브러리 —
 * `scrollTop: 0`, one `GET /api/series?offset=0&limit=60`, the series not in the
 * document, nothing focused. The reveal behaved exactly as specified (`index ===
 * -1`, stay armed, steal no focus) and the reader still lost their place.
 *
 * These two tests are that scenario without the five minutes: an armed
 * instruction, a cold list, and the series on page 3.
 */
describe('the E-34 §2 reveal reaches a series the loaded pages do not hold', () => {
  /** 150 series; the target sits at index 130, i.e. on the third page of 60. */
  const PAGED_COUNT = 150
  const TARGET_INDEX = 130

  function pagedSeries(): SeriesSummary[] {
    return Array.from({ length: PAGED_COUNT }, (_, i) =>
      makeSeries({
        id: makeId(4_200 + i),
        // Zero-padded so the server's name sort and this array agree, which is
        // what makes TARGET_INDEX mean "the 131st row" rather than "somewhere".
        name: `[만화] 시리즈 ${String(i + 1).padStart(3, '0')}`,
      }),
    )
  }

  /** The offsets `GET /api/series` was asked for, in order, list calls only. */
  function listOffsets(): number[] {
    return seriesRequests
      .filter((url) => url.searchParams.get('limit') !== '1')
      .map((url) => Number(url.searchParams.get('offset') ?? '0'))
  }

  it('pages forward until it finds it, then scrolls to it and focuses it', async () => {
    const restoreWidth = stubContentWidth(1_156)
    const restoreScroll = stubScrolling()
    try {
      const series = pagedSeries()
      const target = series[TARGET_INDEX]
      // A throw rather than an `expect`: it narrows the type, so every use below
      // reads `target.id` instead of an optional chain that would silently look
      // for a card named `undefined`.
      if (target === undefined) throw new Error('the fixture must carry the target')
      scenario.series = series
      // Exactly the state the shell leaves behind on the way out of a book,
      // with nothing in the query cache — which is what five minutes of reading
      // produces.
      useUiStore.setState({ revealSeries: target.id })

      renderLibrary()
      await waitForLibrary()

      const card = await screen.findByRole('button', { name: target.name }, { timeout: 5_000 })
      expect(card).toBeInTheDocument()
      const cardRoot = document.getElementById(seriesCardDomId(target.id))
      await waitFor(() => {
        expect(cardRoot).toHaveAttribute('data-revealed', 'true')
      })
      await waitFor(() => {
        expect(document.activeElement).toBe(cardRoot)
      })

      // Three pages, in order, and no more: it stops the moment the series is in
      // `items` rather than reading the rest of the shelf.
      expect(listOffsets()).toEqual([0, 60, 120])

      // …and the scroller actually moved. `scrollCalls` is what `virtual-core`
      // asked the DOM for, so a reveal that "ran" but resolved to 0 fails here.
      expect(scrollCalls.at(-1)).toBeGreaterThan(0)

      // The instruction is spent, so a later mount does not re-steal focus.
      expect(useUiStore.getState().revealSeries).toBeNull()
    } finally {
      restoreScroll()
      restoreWidth()
    }
  })

  it('stops at the end of the filtered list, and steals no focus, when it is not there', async () => {
    // The termination condition, which is the whole answer to E-34's "unbounded"
    // objection: `hasNextPage` going false ends it after exactly one pass. The
    // instruction stays armed — E-34 §1 keeps a series outside the reader's
    // filter armed rather than widening the filter to find it, and that ruling is
    // about `scope`, not about how many pages have been fetched.
    const restoreWidth = stubContentWidth(1_156)
    const restoreScroll = stubScrolling()
    try {
      scenario.series = pagedSeries()
      useUiStore.setState({ revealSeries: makeId(9_999) })

      renderLibrary()
      await waitForLibrary()
      await screen.findByRole('button', { name: '[만화] 시리즈 001' })

      await waitFor(() => {
        expect(listOffsets()).toEqual([0, 60, 120])
      })
      // Held for a beat: a run that has not terminated issues a fourth call here.
      await act(async () => {
        await new Promise((resolve) => setTimeout(resolve, 50))
      })
      expect(listOffsets()).toEqual([0, 60, 120])

      expect(document.activeElement).toBe(document.body)
      expect(useUiStore.getState().revealSeries).toBe(makeId(9_999))
    } finally {
      restoreScroll()
      restoreWidth()
    }
  })
})
