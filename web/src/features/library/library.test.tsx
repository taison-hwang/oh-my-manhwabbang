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
import { useUiStore } from '../../store/ui'
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
  addEventListener: () => void
  removeEventListener: () => void
}

function stubViewport(width: number): void {
  const impl = (query: string): FakeMql => {
    const m = /min-width:\s*(\d+)px/.exec(query)
    return {
      matches: m?.[1] === undefined ? false : width >= Number(m[1]),
      media: query,
      addEventListener: () => undefined,
      removeEventListener: () => undefined,
    }
  }
  Object.defineProperty(window, 'matchMedia', { writable: true, configurable: true, value: impl })
}

/**
 * jsdom does no layout, so every element reports `clientWidth === 0` and the one
 * box-model fact the grid's arithmetic turns on — that **`clientWidth` includes
 * padding** — cannot be observed at all. This reproduces it: a `p-4` wrapper
 * reports 32px more than the box its child grid is actually laid out in, so a
 * component that measures the wrapper instead of the grid gets caught here
 * rather than in a screenshot.
 */
function stubContentWidth(contentWidth: number): () => void {
  const original = Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'clientWidth')
  Object.defineProperty(HTMLElement.prototype, 'clientWidth', {
    configurable: true,
    get(this: HTMLElement): number {
      return contentWidth + (this.classList.contains('p-4') ? 32 : 0)
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
