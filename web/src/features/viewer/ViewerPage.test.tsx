import '@testing-library/jest-dom/vitest'

import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { setupServer } from 'msw/node'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom'
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'

import {
  BOOK_CV,
  BOOK_ID,
  ORIGIN,
  SERIES_ID,
  bookDetail,
  bookSummary,
  errorEnvelope,
  root,
  scanStatusIdle,
  seriesDetail,
  seriesSummary,
  settings,
} from '../../api/fixtures'
import type {
  BookDetail,
  BookPrefs,
  DisplayMode,
  FitMode,
  PageInfo,
  Progress,
  ReadingDir,
  SeriesSummary,
} from '../../api/types'
import { resetBasePath } from '../../api/urls'
import { LibraryPage } from '../library/LibraryPage'
import { SeriesDetailPage } from '../series/SeriesDetailPage'
import { useSeriesDirStore } from '../../store/seriesDir'
import { seriesCardDomId, seriesRowDomId, useUiStore } from '../../store/ui'
import {
  CHROME_AUTOHIDE_MS,
  STALE_NOTICE_MS,
  cancelChromeAutoHide,
  useViewerStore,
} from '../../store/viewer'
import { PROGRESS_DEBOUNCE_MS, queryKeys } from '../../api/queries'
import { EDGE_STRIP_PX, POINTER_IDLE_MS, ViewerPage } from './ViewerPage'
import { OVERRIDE_CHIP_LABEL } from './ViewerTopBar'
import { THUMB_SLOT_PX, THUMB_SLOT_TOUCH_PX } from './ThumbnailStrip'
import { LOADING_INDICATOR_DELAY_MS } from './useDelayedFlag'

/**
 * Screen 3 end to end against MSW — the composition WP-11 is actually judged on
 * (impl-plan §3 WP-11 acceptance 1, 2, 4–9, 13–14; §6.1 rows 09–11).
 *
 * The parts of this package are tested pure in `fit.test.ts` and
 * `interaction.test.ts`; what is asserted here is that they are *wired*: that
 * the RTL rule reaches the DOM, that `settings.prefetch` reaches `new Image()`,
 * that a page turn does not blank the stage, that the arrow keys invert, and
 * that leaving the book writes the page.
 *
 * jsdom never loads an `<img>`, which is convenient rather than limiting: it
 * means every load and every failure in here is fired deliberately, so the
 * "previous page stays on screen" contract is observed at the exact moment it
 * matters instead of being raced against a decode.
 */

const NEXT_BOOK_ID = 'nextbook33333333'
const PAGE_COUNT = 214

/** Page 7 is a double-page scan (FR-VWR-004); everything else is portrait. */
const PAGES: PageInfo[] = Array.from({ length: PAGE_COUNT }, (_, i) => {
  const n = i + 1
  const landscape = n === 7
  return {
    n,
    name: `page_${String(n).padStart(3, '0')}.jpg`,
    ext: '.jpg',
    size: 180_000,
    w: landscape ? 1_600 : 800,
    h: landscape ? 1_000 : 1_200,
  }
})

function progressOf(overrides: Partial<Progress> = {}): Progress {
  return {
    book_id: BOOK_ID,
    series_id: SERIES_ID,
    last_page: 42,
    page_count: PAGE_COUNT,
    completed: false,
    started_at: 1,
    updated_at: 2,
    stale: false,
    ...overrides,
  }
}

function detailOf(prefs: Partial<BookPrefs> = {}, overrides: Partial<BookDetail> = {}): BookDetail {
  return {
    ...bookDetail,
    page_count: PAGE_COUNT,
    pages: PAGES,
    dims_state: 'done',
    next_book_id: NEXT_BOOK_ID,
    progress: progressOf(),
    prefs: {
      reading_direction: 'ltr',
      display_mode: 'single',
      fit_mode: 'height',
      is_override: true,
      ...prefs,
    },
    ...overrides,
  }
}

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

interface ProgressPutBody {
  page: number
  completed?: boolean
  stale_seen?: boolean
}

interface Recorded {
  /** `stale_seen` is E-45 §2's acknowledgement — recorded so its *absence* is assertable. */
  progressPuts: ProgressPutBody[]
  /**
   * The same writes, each with the **book it landed on** (E-45 §1 REVISION).
   *
   * The body alone cannot see the two defects that live one route param away: an
   * acknowledgement signed for the volume the reader moved *to*, and volume 1's
   * page written onto volume 2 while volume 2 is still loading. Both are correct
   * bodies at the wrong address.
   */
  progressWrites: { bid: string; body: ProgressPutBody }[]
  prefsPuts: unknown[]
  /** Every URL handed to `new Image()` by the prefetcher. */
  prefetched: string[]
}

function newRecorded(): Recorded {
  return { progressPuts: [], progressWrites: [], prefsPuts: [], prefetched: [] }
}

type PrefsPatch = Partial<Record<'reading_direction' | 'display_mode' | 'fit_mode', string | null>>

const PREF_FIELDS = ['reading_direction', 'display_mode', 'fit_mode'] as const

/**
 * What `PUT /api/books/{bid}/prefs` actually answers — the **server's merge**,
 * not an echo of the request body.
 *
 * The distinction is the whole of E-33 §3. `internal/httpapi/books.go` decodes
 * each field as a `json.RawMessage` precisely so that `{"fit_mode": null}` and
 * `{}` are different requests: the first *clears* the override and the book
 * falls back to the global default, the second leaves it alone.
 * `mergePrefsWithDefaults` then fills every cleared field from
 * `GET /api/settings` and reports `is_override` for the object as a whole.
 *
 * The handler used to reply `{ ...detail.prefs, ...body }`, which for the reset
 * this file now exercises would have answered `reading_direction: null` — a
 * shape the contract cannot produce. A test written against that is pinning a
 * fiction, and the store setters it is meant to police would have been fed
 * `null` instead of a direction.
 */
function applyPrefsPatch(effective: BookPrefs, patch: PrefsPatch): BookPrefs {
  // The fixture reports effective values plus one flag, so "what is stored" has
  // to be reconstructed the only way the flag allows: overridden ⇒ all three are
  // the book's own, not overridden ⇒ none of them are.
  const stored: Record<string, string | null> = effective.is_override
    ? {
        reading_direction: effective.reading_direction,
        display_mode: effective.display_mode,
        fit_mode: effective.fit_mode,
      }
    : { reading_direction: null, display_mode: null, fit_mode: null }
  for (const field of PREF_FIELDS) {
    if (field in patch) stored[field] = patch[field] ?? null
  }
  return {
    reading_direction: (stored.reading_direction ?? settings.reading_direction) as ReadingDir,
    display_mode: (stored.display_mode ?? settings.display_mode) as DisplayMode,
    fit_mode: (stored.fit_mode ?? settings.fit_mode) as FitMode,
    is_override: PREF_FIELDS.some((field) => stored[field] != null),
  }
}

function handlers(detail: BookDetail, recorded: Recorded, prefetch = 4) {
  return [
    http.get(`${ORIGIN}/api/books/:bid`, ({ params }) =>
      params.bid === BOOK_ID
        ? HttpResponse.json(detail)
        : HttpResponse.json({ ...detail, id: String(params.bid), next_book_id: null }),
    ),
    http.get(`${ORIGIN}/api/series/:sid`, () =>
      HttpResponse.json({
        ...seriesDetail,
        books: [
          { ...bookSummary, id: BOOK_ID, page_count: PAGE_COUNT },
          {
            ...bookSummary,
            id: NEXT_BOOK_ID,
            name: '군계(軍鷄) 02권.zip',
            ord: 1,
            page_count: 190,
            progress: null,
          },
        ],
      }),
    ),
    http.get(`${ORIGIN}/api/settings`, () => HttpResponse.json({ ...settings, prefetch })),
    // Answers `progressOf()` — i.e. `stale: false` — exactly as the server does:
    // `PUT` replies with the progress it just stored, and E-45's acknowledgement
    // is the *only* thing that re-baselines it. This reply is what
    // `useSaveProgress.onSuccess` writes over `books.detail` with, so it is also
    // the murder weapon in the defect E-45 names.
    http.put(`${ORIGIN}/api/books/:bid/progress`, async ({ request, params }) => {
      const body = (await request.json()) as ProgressPutBody
      recorded.progressPuts.push(body)
      recorded.progressWrites.push({ bid: String(params.bid), body })
      return HttpResponse.json(progressOf())
    }),
    http.put(`${ORIGIN}/api/books/:bid/prefs`, async ({ request }) => {
      const body = (await request.json()) as PrefsPatch
      recorded.prefsPuts.push(body)
      return HttpResponse.json(applyPrefsPatch(detail.prefs, body))
    }),
    // Thumbnails are lazy (FR-THM-004); nothing on this screen may break on one.
    http.get(`${ORIGIN}/api/books/:bid/thumbs/:n`, () =>
      HttpResponse.json(errorEnvelope('thumb_unavailable', 'not generated'), { status: 422 }),
    ),
  ]
}

/**
 * A pending progress write is flushed on unmount, which happens *after* the
 * test that caused it has finished and `resetHandlers` has run. These defaults
 * survive the reset so the late write lands somewhere instead of tripping
 * `onUnhandledRequest: 'error'`; the per-test handlers above override them.
 */
const server = setupServer(
  http.put(`${ORIGIN}/api/books/:bid/progress`, () => HttpResponse.json(progressOf())),
  http.put(`${ORIGIN}/api/books/:bid/prefs`, () => HttpResponse.json(bookDetail.prefs)),
)

/**
 * jsdom has no `matchMedia`; without one the viewer chrome reports `mobile`.
 *
 * The listeners are real, not no-ops, so a test can *move* the viewport:
 * `useMediaQuery` is a `useSyncExternalStore`, and with a stub that never
 * notifies, every breakpoint is frozen at whatever it was on mount — which is
 * exactly the case the strip's re-measure exists for.
 */
let viewportWidth = 1_440
let viewportListeners: (() => void)[] = []

function stubViewport(width: number): void {
  viewportWidth = width
  viewportListeners = []
  const impl = (query: string) => {
    const m = /min-width:\s*(\d+)px/.exec(query)
    const min = m?.[1] === undefined ? null : Number(m[1])
    return {
      matches: min !== null && viewportWidth >= min,
      media: query,
      addEventListener: (_: string, cb: () => void) => viewportListeners.push(cb),
      removeEventListener: (_: string, cb: () => void) => {
        viewportListeners = viewportListeners.filter((x) => x !== cb)
      },
    }
  }
  Object.defineProperty(window, 'matchMedia', { writable: true, configurable: true, value: impl })
}

/** Move the stubbed viewport and tell every `useMediaQuery` about it. */
function resizeViewport(width: number): void {
  viewportWidth = width
  for (const cb of [...viewportListeners]) cb()
}

/**
 * A stand-in for `new Image()` that records the URL it was pointed at.
 *
 * jsdom does not fetch `img.src`, so watching MSW would pass whatever the
 * prefetcher did — including doing nothing at all.
 */
function stubImage(recorded: Recorded): void {
  class RecordingImage {
    #src = ''
    addEventListener(): void {
      /* the prefetcher only uses this to release its reference */
    }
    get src(): string {
      return this.#src
    }
    set src(value: string) {
      this.#src = value
      recorded.prefetched.push(value)
    }
  }
  vi.stubGlobal('Image', RecordingImage)
}

function LocationProbe() {
  const location = useLocation()
  return <p data-testid="location">{`${location.pathname}${location.search}`}</p>
}

interface SetupOptions {
  prefs?: Partial<BookPrefs>
  detail?: Partial<BookDetail>
  /** Appended to the viewer route; the series screen sets `?page=`. */
  search?: string
  prefetch?: number
  width?: number
}

interface Mounted {
  recorded: Recorded
  /** The screen's own cache — the surface `useSaveProgress.onSuccess` writes to. */
  client: QueryClient
  unmount: () => void
}

/** Everything `setup` does except waiting, so a fake clock can own the wait. */
function mount(options: SetupOptions = {}): Mounted {
  const recorded = newRecorded()
  const detail = detailOf(options.prefs, options.detail)
  server.use(...handlers(detail, recorded, options.prefetch ?? 4))
  stubViewport(options.width ?? 1_440)
  stubImage(recorded)

  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  const { unmount } = render(
    <QueryClientProvider client={client}>
      <MemoryRouter
        initialEntries={[`/series/${SERIES_ID}/books/${BOOK_ID}${options.search ?? '?page=12'}`]}
      >
        <Routes>
          <Route path="/series/:sid" element={<LocationProbe />} />
          <Route path="/series/:sid/books/:bid" element={<ViewerPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  )
  return { recorded, client, unmount }
}

async function setup(options: SetupOptions = {}): Promise<Recorded> {
  const { recorded } = mount(options)
  await screen.findAllByRole('img', { name: /page_/ })
  return recorded
}

/**
 * Let the fake clock run until `ready()`, yielding to the real event loop
 * between ticks so MSW, `fetch` and React Query can make progress.
 *
 * `waitFor` and `findBy*` cannot be used under a fake clock here:
 * `@testing-library/dom`'s fake-timer support is **jest-only** — `helpers.js`
 * gates it on `typeof jest !== 'undefined'`, and this suite runs on vitest — so
 * their polling `setInterval` is itself faked and nothing would ever advance it.
 * The budget is spent in *fake* milliseconds, and deliberately far short of
 * anything this file times, so a settle cannot eat a lifetime under test.
 */
async function tickUntil(ready: () => boolean, budgetMs = 400): Promise<void> {
  for (let spent = 0; spent <= budgetMs; spent += 5) {
    if (ready()) return
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5)
    })
  }
  if (!ready()) throw new Error('the screen never settled inside its fake-clock budget')
}

/**
 * `setup` with the fake clock installed **before the first render**.
 *
 * It has to be before: the notice's timer is armed by the open effect, which
 * runs inside this function. A clock installed afterwards does not own that
 * timer — `advanceTimersByTime` would not fire it, and the real 3 400 ms would
 * land long after the test had ended, which is a test that can only ever agree
 * with whatever the screen does.
 */
async function setupOnFakeClock(options: SetupOptions = {}): Promise<Mounted> {
  vi.useFakeTimers()
  const mounted = mount(options)
  await tickUntil(() => document.querySelector('img[alt*="page_"]') !== null)
  return mounted
}

/**
 * `setupOnFakeClock`, stopped at the **instant the notice went up**, with the
 * clock reading of that instant (**E-45 §1**).
 *
 * Without it the screen tier cannot state a duration at all: `setupOnFakeClock`
 * spends an unknown number of fake milliseconds waiting for the artwork, so
 * every assertion afterwards is "some time later", and a test written that way
 * passes for any lifetime between the two moments it happens to sample. It ticks
 * one millisecond at a time and reads the clock *before* advancing, so `armedAt`
 * is the arming instant to within a single tick — which is why the boundary case
 * below leaves itself two.
 */
async function mountUntilStaleArmed(
  options: SetupOptions = {},
): Promise<Mounted & { armedAt: number }> {
  vi.useFakeTimers()
  const mounted = mount(options)
  for (let spent = 0; spent <= 400; spent += 1) {
    if (useViewerStore.getState().staleVisible) break
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1)
    })
  }
  if (!useViewerStore.getState().staleVisible) {
    throw new Error('the changed-file notice never went up')
  }
  return { ...mounted, armedAt: Date.now() }
}

/** Run the fake clock forward to an absolute reading of it. */
async function advanceTo(when: number): Promise<void> {
  const remaining = when - Date.now()
  if (remaining < 0) throw new Error('the clock is already past that instant')
  await act(async () => {
    await vi.advanceTimersByTimeAsync(remaining)
  })
}

function stage(): HTMLElement {
  const el = document.querySelector('[data-role="stage"]')
  if (el === null) throw new Error('the stage is not mounted')
  return el as HTMLElement
}

function framePages(): string[] {
  return [...document.querySelectorAll('[data-role="page-frame"]')].map(
    (f) => (f as HTMLElement).dataset.page ?? '?',
  )
}

function counter(): string {
  return document.querySelector('[data-role="page-counter"]')?.textContent ?? ''
}

function need(role: string): HTMLElement {
  const el = document.querySelector(`[data-role="${role}"]`)
  if (el === null) throw new Error(`there is no ${role}`)
  return el as HTMLElement
}

const viewerRoot = (): HTMLElement => need('viewer')
const topBar = (): HTMLElement => need('viewer-top-bar')
const zones = (): HTMLElement => need('stage-zones')

/**
 * The boundary event a **browser** dispatches, which jsdom has no constructor
 * for (`window.PointerEvent` is undefined in jsdom 26).
 *
 * E-27's hover-hold is answered from `pointerover`/`pointerout` rather than from
 * React's `onMouseEnter`/`onMouseLeave`, because those two are *synthesised* out
 * of `mouseover`/`mouseout` and the synthesis drops the pair whenever the
 * `relatedTarget` is a node React manages — which is every crossing that happens
 * because the layout moved rather than because the pointer did. A `MouseEvent`
 * carrying the pointer event's name and `pointerType` is exactly what React's
 * delegated listener at the root receives from Chrome.
 */
function crossPointer(
  type: 'pointerover' | 'pointerout',
  target: Element,
  relatedTarget: Element | null,
  pointerType = 'mouse',
): void {
  const event = new MouseEvent(type, { bubbles: true, cancelable: true, relatedTarget })
  Object.defineProperty(event, 'pointerType', { value: pointerType })
  fireEvent(target, event)
}

interface ChromeWatch {
  /** Every value `data-chrome` has *changed to* since the watch began. */
  flips: () => string[]
  stop: () => void
}

/**
 * The unit tier's `MutationObserver` on `data-chrome` — the only shape in which
 * "**nothing happened**" can be asserted here (**E-31**, 뒤집을 경우의 전제).
 *
 * `toHaveAttribute` retries. `expect(viewer).toHaveAttribute('data-chrome',
 * 'hidden')` is therefore satisfied by a chrome that woke on the action under
 * test and went back down on its own 2 600 ms later, which is precisely what
 * this ruling's defect looks like — session 8 wrote that assertion and got a
 * green first mutation out of it, and `e2e/09-viewer-chrome.spec.ts` records the
 * same trap twice over from the browser side. A transition list has nothing to
 * retry into: one entry means one thing happened, and `[]` means none did.
 *
 * Read through `takeRecords()` rather than the callback, because the callback is
 * a microtask and everything asserted here is synchronous under a fake clock. A
 * `MutationRecord` carries only `oldValue`, so the value each change landed *on*
 * is the next record's `oldValue` — and, for the last change, the attribute as
 * it now stands.
 */
function watchChrome(): ChromeWatch {
  const root = viewerRoot()
  const seen: MutationRecord[] = []
  const observer = new MutationObserver((records) => {
    seen.push(...records)
  })
  observer.observe(root, {
    attributes: true,
    attributeFilter: ['data-chrome'],
    attributeOldValue: true,
  })
  return {
    flips: () => {
      seen.push(...observer.takeRecords())
      if (seen.length === 0) return []
      return [...seen.slice(1).map((r) => r.oldValue ?? ''), root.getAttribute('data-chrome') ?? '']
    },
    stop: () => {
      observer.disconnect()
    },
  }
}

/**
 * Summons the chrome from the top screen-edge strip, leaves the pointer in the
 * bar that replaced it, and **proves the hold is really in force** by letting
 * the auto-hide deadline go by.
 *
 * The proof is the point. A release test whose setup never held anything passes
 * against a chrome that was going to hide on its own — green, and about nothing
 * (HANDOFF §6.5). Call under fake timers.
 */
function holdFromTheEdge(): void {
  const edge = document.querySelector('[data-role="viewer-edge-top"]')
  if (edge === null) throw new Error('no top edge strip')
  fireEvent.mouseEnter(edge)
  crossPointer('pointerover', topBar(), viewerRoot())
  act(() => {
    vi.advanceTimersByTime(CHROME_AUTOHIDE_MS * 2)
  })
  expect(
    useViewerStore.getState().chromeVisible,
    'the release cannot be observed unless something was holding it',
  ).toBe(true)
}

/**
 * The chromeless page number (E-27) — `null` once a bar is up to carry one.
 *
 * Distinct from `counter()`: that one lives in the bottom bar, which is mounted
 * the whole time and only fades, so it is readable whether the chrome is up or
 * not and cannot tell the two states apart.
 */
function quietCounter(): string | null {
  return document.querySelector('[data-role="quiet-page-counter"]')?.textContent ?? null
}

/**
 * A tap at `clientX` on the stage zones, over a stubbed 1 000 × 800 stage.
 *
 * Module-scoped because two blocks need it: the zone rules themselves, and
 * E-27's "turning a page does not summon the chrome".
 */
function tapAt(clientX: number): void {
  const zones = document.querySelector('[data-role="stage-zones"]')
  if (zones === null) throw new Error('no tap zones')
  vi.spyOn(zones, 'getBoundingClientRect').mockReturnValue({
    x: 0,
    y: 0,
    left: 0,
    top: 0,
    right: 1_000,
    bottom: 800,
    width: 1_000,
    height: 800,
    toJSON: () => ({}),
  } as DOMRect)
  fireEvent.mouseDown(zones, { clientX, clientY: 400 })
  fireEvent.mouseUp(zones, { clientX, clientY: 400 })
}

/** Marks the pages currently on the stage as decoded, as a browser would. */
function decodeShownPages(): void {
  for (const img of document.querySelectorAll('img[data-role="page"]')) {
    Object.defineProperty(img, 'naturalWidth', { configurable: true, value: 800 })
    Object.defineProperty(img, 'naturalHeight', { configurable: true, value: 1_200 })
    fireEvent.load(img)
  }
}

beforeAll(() => {
  server.listen({ onUnhandledRequest: 'error' })
  // jsdom/undici interop, not a product concern: React Router builds a
  // `Request` per navigation and hands it a jsdom `AbortSignal`, which undici's
  // `instanceof` check rejects. Both are native and consistent in a browser.
  const Base = globalThis.Request
  class SignallessRequest extends Base {
    constructor(input: RequestInfo | URL, init?: RequestInit) {
      super(input, init === undefined ? undefined : { ...init, signal: null })
    }
  }
  Object.defineProperty(globalThis, 'Request', {
    writable: true,
    configurable: true,
    value: SignallessRequest,
  })
})
afterEach(() => {
  server.resetHandlers()
  resetBasePath()
  cancelChromeAutoHide()
  vi.unstubAllGlobals()
  // A fake clock installed by one case must not be inherited by the next: the
  // cases that time things install it *before* their first render, so a leaked
  // one would freeze a later test's debounce with nothing to advance it.
  vi.useRealTimers()
})
afterAll(() => {
  server.close()
})

beforeEach(() => {
  localStorage.clear()
  useUiStore.setState({
    theme: 'light',
    overlays: [],
    scope: 'all',
    query: '',
    view: 'grid',
    revealSeries: null,
  })
  useSeriesDirStore.setState({ bySeries: {} })
  useViewerStore.getState().close()
})

// ---------------------------------------------------------------------------
// Acceptance 1 & 2 — the reading ground
// ---------------------------------------------------------------------------

describe('the chromeless ground (acceptance 1, 2; ui-spec §6.1, ruling E-27)', () => {
  it('opens dark, chromeless, and saying so', async () => {
    await setup()
    const viewer = document.querySelector('[data-role="viewer"]')
    expect(viewer).toHaveAttribute('data-theme', 'dark')
    expect(viewer).toHaveAttribute('data-chrome', 'hidden')
    // Never `display:none` — the bar is mounted the whole time (ui-spec §6.6).
    expect(document.querySelector('[data-role="viewer-top-bar"]')).toHaveAttribute(
      'data-visible',
      'false',
    )
    // …and the reader is told where it went, or the screen is a black box.
    expect(screen.getByRole('status')).toHaveAttribute('data-role', 'viewer-chrome-hint')
  })

  it('hides the cursor when the *pointer* idles, and never wakes the chrome doing it', async () => {
    await setup()
    const viewer = document.querySelector('[data-role="viewer"]')
    if (viewer === null) throw new Error('no viewer')

    // The chrome is already away and the cursor is still here: E-27 separated
    // them. Under the old rule this assertion read `none`.
    expect(useViewerStore.getState().chromeVisible).toBe(false)
    expect(viewer).toHaveStyle({ cursor: 'default' })

    // Installed *before* the move, so the idle timeout it schedules lands on
    // the fake clock rather than making this test wait 1.6 real seconds.
    vi.useFakeTimers()
    try {
      fireEvent.mouseMove(viewer, { clientX: 500, clientY: 400 })
      // The move that hides the cursor is the same move that used to raise
      // three rows of controls. It no longer does.
      expect(useViewerStore.getState().chromeVisible).toBe(false)

      // design.md principle 2: while reading there is no UI, and the pointer is UI.
      act(() => {
        vi.advanceTimersByTime(POINTER_IDLE_MS)
      })
      expect(viewer).toHaveStyle({ cursor: 'none' })

      fireEvent.mouseMove(viewer, { clientX: 520, clientY: 400 })
      expect(viewer).toHaveStyle({ cursor: 'default' })
    } finally {
      vi.useRealTimers()
    }
  })

  it('summons the chrome from the screen edge', async () => {
    await setup()
    const edge = document.querySelector('[data-role="viewer-edge-top"]')
    if (edge === null) throw new Error('no top edge strip')

    fireEvent.mouseEnter(edge)
    expect(useViewerStore.getState().chromeVisible).toBe(true)

    // …and the strips get out of the way once it is up, or the first click on
    // 뒤로 lands on an invisible box instead.
    expect(document.querySelector('[data-role="viewer-edge-top"]')).toBeNull()
  })

  it('holds the chrome open while the pointer rests on a bar', async () => {
    await setup()
    const bar = topBar()

    vi.useFakeTimers()
    try {
      act(() => {
        useViewerStore.getState().wake()
      })
      // The reader's own path to a control: pointer on the page, then into the
      // bar. `stage-zones` is what it leaves.
      crossPointer('pointerover', bar, zones())
      act(() => {
        vi.advanceTimersByTime(CHROME_AUTOHIDE_MS * 3)
      })
      // The reader is looking at the control they are reaching for.
      expect(useViewerStore.getState().chromeVisible).toBe(true)

      crossPointer('pointerout', bar, zones())
      act(() => {
        vi.advanceTimersByTime(CHROME_AUTOHIDE_MS)
      })
      expect(useViewerStore.getState().chromeVisible).toBe(false)
    } finally {
      vi.useRealTimers()
    }
  })

  /**
   * The path E-27's hold did **not** cover, and the one the ruling itself added.
   *
   * The strips are rendered only while the chrome is away, so waking from one
   * unmounts the strip and lights the bar in the same commit — *under a pointer
   * that has not moved*. The old wiring hung the hold on the bar's
   * `onMouseEnter`, and React synthesises that from `mouseover`: measured in
   * Chrome at all four widths, the browser does re-hit-test and does dispatch
   * `pointerover`/`mouseover` on the bar ~10 ms later, but React drops the pair
   * because the event's `relatedTarget` is a node it manages. No hold was taken,
   * and 2 600 ms later the chrome dissolved under a pointer sitting inside it —
   * or, with the pointer at rest in the 44 px the strip re-occupies, the strip
   * re-mounted beneath it and the bars blinked every 2.6 s indefinitely.
   *
   * **What this test is entitled to assert.** The browser sending that
   * `pointerover` is a browser fact and belongs to `09-viewer-chrome.spec.ts`,
   * which parks a real pointer in a real strip and watches the real deadline
   * pass. What is asserted here is the half that is this screen's: given that
   * the pointer is over a bar, the chrome is held — no matter that no crossing
   * ever happened, because the rule reads what is under the pointer rather than
   * remembering a boundary it was told about.
   */
  it.each([
    { edge: 'viewer-edge-top', bar: 'viewer-top-bar', gesture: 'hover' },
    { edge: 'viewer-edge-bottom', bar: 'viewer-bottom-bar', gesture: 'click' },
  ])(
    'holds the chrome a screen edge just summoned ($edge, $gesture), under a pointer that never moved',
    async ({ edge, bar, gesture }) => {
      await setup()
      const strip = document.querySelector(`[data-role="${edge}"]`)
      if (strip === null) throw new Error(`no ${edge}`)

      vi.useFakeTimers()
      try {
        // Both halves of a strip: hovering one wakes the chrome, and so does
        // pressing one — the second is what a reader who was already pointing at
        // the edge does, and the only one a finger can perform.
        if (gesture === 'hover') fireEvent.mouseEnter(strip)
        else fireEvent.click(strip)
        expect(useViewerStore.getState().chromeVisible).toBe(true)
        expect(
          document.querySelector(`[data-role="${edge}"]`),
          'the strip is gone and the bar is where the pointer is: that is the whole defect',
        ).toBeNull()

        // Chrome's post-layout hit test, verbatim: `pointerover` on the bar with
        // the viewer root as `relatedTarget`.
        crossPointer('pointerover', need(bar), viewerRoot())
        act(() => {
          vi.advanceTimersByTime(CHROME_AUTOHIDE_MS * 3)
        })
        expect(
          useViewerStore.getState().chromeVisible,
          'E-27: the reader is looking at the control they are about to press',
        ).toBe(true)
      } finally {
        vi.useRealTimers()
      }
    },
  )

  /**
   * …and it lets go again, by three routes that do not share a failure.
   *
   * A hold that is never released disarms the auto-hide for the rest of the
   * session — `chromeHeld` is module-scoped and nothing on screen renders from
   * it, so there is no state a reader could see or correct. The hold above was
   * taken without a crossing, so the release must not depend on the matching
   * crossing arriving either.
   */
  it('lets the held chrome go when the pointer moves off the bar', async () => {
    await setup()
    vi.useFakeTimers()
    try {
      holdFromTheEdge()
      crossPointer('pointerout', topBar(), zones())
      act(() => {
        vi.advanceTimersByTime(CHROME_AUTOHIDE_MS)
      })
      expect(useViewerStore.getState().chromeVisible).toBe(false)
    } finally {
      vi.useRealTimers()
    }
  })

  it('lets it go when the pointer leaves the window altogether', async () => {
    await setup()
    vi.useFakeTimers()
    try {
      holdFromTheEdge()
      // The pointer left the browser window: `relatedTarget` is null, which is
      // the browser saying "nothing" rather than naming a destination.
      crossPointer('pointerout', topBar(), null)
      act(() => {
        vi.advanceTimersByTime(CHROME_AUTOHIDE_MS)
      })
      expect(useViewerStore.getState().chromeVisible).toBe(false)
    } finally {
      vi.useRealTimers()
    }
  })

  it('lets it go on a plain move over the stage — a boundary event need not arrive', async () => {
    await setup()
    vi.useFakeTimers()
    try {
      holdFromTheEdge()
      // A different event family from the two above, and the one that keeps
      // arriving rather than firing once at a boundary that may be missed.
      fireEvent.mouseMove(zones(), { clientX: 500, clientY: 400 })
      act(() => {
        vi.advanceTimersByTime(CHROME_AUTOHIDE_MS)
      })
      expect(useViewerStore.getState().chromeVisible).toBe(false)
    } finally {
      vi.useRealTimers()
    }
  })

  /**
   * **A finger is not a resting pointer.**
   *
   * Chrome sends compatibility mouse events after a tap and they do not say they
   * came from a touch, so a tap inside a bar took the hold and no `mouseleave`
   * ever came to give it back — measured on the shipped build at mobile-400,
   * where one tap on the page counter pinned the chrome open for good. E-27's
   * justification is a pointer *resting* on a control, and there is no such
   * thing on a touch screen: `pointerType` is what tells them apart.
   */
  it('never holds the chrome for a touch, which has nothing to rest', async () => {
    await setup()
    vi.useFakeTimers()
    try {
      act(() => {
        useViewerStore.getState().wake()
      })
      crossPointer('pointerover', topBar(), viewerRoot(), 'touch')
      act(() => {
        vi.advanceTimersByTime(CHROME_AUTOHIDE_MS)
      })
      expect(useViewerStore.getState().chromeVisible).toBe(false)
    } finally {
      vi.useRealTimers()
    }
  })

  it('shows the page number quietly while there is no bar to hold it', async () => {
    await setup()
    expect(quietCounter()).toBe('12 / 214')

    act(() => {
      useViewerStore.getState().wake()
    })
    // The bar has its own counter; two on screen at once is the bug.
    expect(quietCounter()).toBeNull()
  })

  /**
   * The one row of E-27's table the shipped build did not honour.
   *
   * `useViewerStore.step()` implemented "a page turn does not wake the chrome"
   * and `viewer.test.ts` pinned it — but the screen never called it. Its stride
   * has to be however many pages are on the stage (FR-VWR-004), which the store
   * cannot know, so `goNext`/`goPrev` went through `goTo`, and `goTo` wakes
   * unconditionally. Every arrow key, `Space` and side-zone tap raised three rows
   * of controls, and because each turn re-armed the 2 600 ms timer, the quiet
   * counter below — which exists so a turn *can* give feedback without the bars —
   * was unreachable after the reader's first page turn.
   *
   * So this is asserted **here**, at the level the reader actually operates, and
   * on the counter's *text advancing*: a viewer that never turned a page would
   * satisfy "the chrome stayed hidden" perfectly.
   */
  it('turns the page without summoning the chrome, by key and by tap (E-27)', async () => {
    await setup()
    decodeShownPages()

    // Preconditions, asserted rather than assumed.
    expect(useViewerStore.getState().chromeVisible).toBe(false)
    expect(quietCounter()).toBe('12 / 214')

    fireEvent.keyDown(window, { key: 'ArrowRight' })
    decodeShownPages()
    expect(useViewerStore.getState().chromeVisible).toBe(false)
    expect(document.querySelector('[data-role="viewer"]')).toHaveAttribute('data-chrome', 'hidden')
    expect(quietCounter()).toBe('13 / 214')

    // The other way into the same turn: the right-hand tap zone under L→R.
    tapAt(900)
    decodeShownPages()
    expect(useViewerStore.getState().chromeVisible).toBe(false)
    expect(document.querySelector('[data-role="viewer"]')).toHaveAttribute('data-chrome', 'hidden')
    expect(quietCounter()).toBe('14 / 214')

    // Control: a chrome that could no longer be summoned at all would pass
    // everything above. The screen edge still works, on the page just turned to.
    const edge = document.querySelector('[data-role="viewer-edge-top"]')
    if (edge === null) throw new Error('no top edge strip')
    fireEvent.mouseEnter(edge)
    expect(useViewerStore.getState().chromeVisible).toBe(true)
    expect(counter()).toBe('14 / 214')
  })

  /**
   * The counterpart, and the reason the fix adds a store action rather than
   * taking the wake out of `goTo`: the slider and the thumbnail strip live *in*
   * the bar, so the bar must not fade out from under the press.
   *
   * **What this does and does not pin.** It pins the rule — a slider commit
   * leaves the chrome up — and it would go red if the fix had reached one step
   * too far and made the controls silent too. It is **not** evidence about
   * `goTo` specifically: the commit path is `setDragging(false)` then `goTo`,
   * and `setDragging` wakes as well, so either one alone would satisfy this.
   * `goTo`'s own wake is pinned in `store/viewer.test.ts`. The strip is the one
   * control that reaches `goTo` unaccompanied (`onJump`), and it cannot be
   * clicked here: `@tanstack/react-virtual` measures the strip through
   * `offsetWidth`, which jsdom reports as 0, so it renders no cells at all.
   */
  it('still wakes the chrome for the controls, which the page turn must not break', async () => {
    await setup()
    expect(useViewerStore.getState().chromeVisible).toBe(false)

    // A keyboard change on the slider has no pointer-down, so it commits at once.
    fireEvent.change(screen.getByRole('slider', { name: '페이지' }), { target: { value: '50' } })
    expect(counter()).toBe('50 / 214')
    expect(useViewerStore.getState().chromeVisible).toBe(true)
    // …and with a bar up to carry the number, the quiet one steps aside.
    expect(quietCounter()).toBeNull()
  })

  it('stacks both bars above the end-of-volume scrim', async () => {
    // The scrim is last in the DOM and positioned, so painting order alone puts
    // it over both bars — which made the end of a volume a dead end: 뒤로, the
    // slider, 표시 모드 and the strip all went under an opaque sheet whose only
    // exits were its own two buttons. `z-chrome` (3) is the prototype's own
    // layering. jsdom cannot hit-test, but the class list is real.
    await setup()
    act(() => {
      useViewerStore.getState().wake()
    })
    for (const role of ['viewer-top-bar', 'viewer-bottom-bar']) {
      const bar = document.querySelector(`[data-role="${role}"]`)
      if (bar === null) throw new Error(`no ${role}`)
      expect([...bar.classList], `${role} must paint above the scrim`).toContain('z-chrome')
    }
  })

  it('keeps every viewer control inline at every width — no ⋯ overflow sheet', async () => {
    // The bar wraps instead (`flex-wrap` here, `flex-none whitespace-nowrap` on
    // each group), which is what lets it carry a z-index at all: the sheet it
    // replaced had to escape the bar on `z-overlay`, so the bar was forbidden
    // one, so the scrim above could not be stopped.
    await setup()
    act(() => {
      useViewerStore.getState().wake()
    })
    const bar = document.querySelector('[data-role="viewer-top-bar"]')
    if (bar === null) throw new Error('no top bar')
    expect([...bar.classList]).toContain('flex-wrap')
    expect(document.querySelector('[data-role="viewer-control-sheet"]')).toBeNull()
    expect(screen.queryByRole('button', { name: '뷰어 컨트롤' })).toBeNull()
    for (const group of ['표시 모드', '읽기 방향', '맞춤']) {
      expect(bar.querySelector(`[aria-label="${group}"]`), group).not.toBeNull()
    }
  })

  it('keeps the 파일이 변경되었습니다 warning on screen without the chrome (FR-VWR-009)', async () => {
    await setup({ detail: { progress: progressOf({ stale: true }) } })
    expect(useViewerStore.getState().chromeVisible).toBe(false)
    // It used to ride along with the chrome. After E-27 that would have deleted
    // it: the chrome no longer comes up on its own.
    expect(screen.getByText('파일이 변경되었습니다')).toBeInTheDocument()
    // Chromeless it is an overlay, so the reading ground keeps its full height.
    const notice = document.querySelector('[data-role="stale-progress"]')
    expect([...(notice?.classList ?? [])]).toContain('absolute')
  })

  it('puts the warning under a top bar that has wrapped, not behind it', async () => {
    // A fixed `top-14` cleared the 53px single-row bar by three pixels and
    // nothing else. Once E-28 let the bar wrap — measured 103px at 900 and
    // 122px at 760 — the notice sat inside the bar's box, and the bar's
    // `z-chrome` painted over it. In the column it is under the bar at any
    // height. jsdom has no layout, so the invariant asserted is the mechanism:
    // in flow, sharing the bar's `order-first`, and later in the DOM than it.
    await setup({ detail: { progress: progressOf({ stale: true }) }, width: 760 })
    act(() => {
      useViewerStore.getState().wake()
    })
    const notice = document.querySelector('[data-role="stale-progress"]')
    const bar = document.querySelector('[data-role="viewer-top-bar"]')
    if (notice === null || bar === null) throw new Error('no notice or no top bar')

    const classes = [...notice.classList]
    expect(classes).toContain('order-first')
    expect(classes).not.toContain('absolute')
    expect(
      bar.compareDocumentPosition(notice) & Node.DOCUMENT_POSITION_FOLLOWING,
      'the notice must break the order-first tie *after* the bar',
    ).toBeTruthy()
  })
})

// ---------------------------------------------------------------------------
// Ruling E-31 — the edge strip wakes on *entry*
// ---------------------------------------------------------------------------

/**
 * **Waking is an event, not a state (E-31).**
 *
 * The strips exist only while the chrome is away, so *every* path that dismisses
 * the chrome mounts them — wherever the pointer happens to be at that instant.
 * With the pointer at rest inside the 44 px, the browser's own post-layout hit
 * test then lands a `mouseover` on a strip nothing walked into (~13 ms, measured
 * at all four widths), and the chrome the reader just sent away came straight
 * back. Before E-30 that was a 2.6 s flicker loop; after it, the derived hold
 * catches the returning chrome on the first cycle and it settles as "visible and
 * held" — so at that one pointer position `H` simply looked broken (E-30 §7,
 * measured and deliberately left unruled).
 *
 * E-31 rules it: if the pointer was already inside the strip's area when the
 * strip mounted, that is not an entry, and it does not wake. And it is explicit
 * that this must **not** share a signal with E-30's hold — the hold is a
 * function of where the pointer *is*, the wake of where it *moved* — because one
 * signal cannot answer both questions and the defect returns.
 *
 * The two are asserted here as a pair: the last test in this block is E-30's
 * hold, re-proved on top of the new gate.
 */
describe('the screen edge wakes on entry, not on arriving under a pointer (ruling E-31)', () => {
  /**
   * `useTouchZones` swallows a mouse press within 600 ms of a `touchend`, and a
   * fake clock starts at zero — which reads as "a touch has just ended". A
   * moment on that clock, well inside the 2 600 ms auto-hide, puts the centre
   * tap back within reach.
   */
  const TAP_ARMED_MS = 700

  type Edge = 'top' | 'bottom'

  /** Half a strip deep — unambiguously inside the band, at either end. */
  const insideEdge = (edge: Edge): number =>
    edge === 'top'
      ? Math.round(EDGE_STRIP_PX / 2)
      : window.innerHeight - Math.round(EDGE_STRIP_PX / 2)

  /** The middle of the screen: as far from both bands as it is possible to be. */
  const overThePage = (): number => Math.round(window.innerHeight / 2)

  const barOf = (edge: Edge): string => (edge === 'top' ? 'viewer-top-bar' : 'viewer-bottom-bar')
  const stripOf = (edge: Edge): string => `viewer-edge-${edge}`

  /**
   * A pointer **movement** to `clientY`, reported by whatever it is over.
   *
   * The one event family that carries movement, and therefore the only one the
   * wake is allowed to read (E-31). It bubbles to the viewer root from a bar and
   * from a strip exactly as it does from the stage, which is what lets the rule
   * be stated once.
   */
  function movePointerOver(target: Element, clientY: number): void {
    fireEvent.mouseMove(target, { clientX: 500, clientY })
  }

  /**
   * The reader's whole journey to *chrome up, pointer at rest inside the 44 px*
   * — which is the state every hide path in this block then acts on.
   *
   * Every step is a real one: on the page, up into the strip (a crossing, so it
   * wakes), and then the bar arrives under a pointer that does not move again.
   */
  function reachForTheEdge(edge: Edge): void {
    movePointerOver(zones(), overThePage())
    fireEvent.mouseEnter(need(stripOf(edge)))
    expect(
      useViewerStore.getState().chromeVisible,
      'a pointer that walked into the strip must still summon the chrome, or nothing below is about E-31',
    ).toBe(true)
    // The bar is now what the pointer is over, and it goes on saying so — this
    // is the reading that used to summon the chrome back the instant it left.
    movePointerOver(need(barOf(edge)), insideEdge(edge))
  }

  /**
   * The defect E-31 closes, from both ends of the screen.
   *
   * Two strips, two `onMouseEnter`s: a fix applied to one of them leaves the
   * other exactly as it was, so both are walked.
   */
  it.each(['top', 'bottom'] as const)(
    '`H` sends the chrome away for good, with the pointer at rest in the %s strip',
    async (edge) => {
      await setup()
      vi.useFakeTimers()
      try {
        reachForTheEdge(edge)
        // E-30 is in force here and is *not* what is under test: the pointer
        // really is inside the chrome, so the chrome really is held. `H` is
        // still entitled to take it down.
        crossPointer('pointerover', need(barOf(edge)), viewerRoot())

        const watch = watchChrome()
        try {
          fireEvent.keyDown(window, { key: 'h' })
          expect(useViewerStore.getState().chromeVisible).toBe(false)
          // The strip is back under a pointer that has not moved. Chrome's
          // post-layout hit test reaches it about 13 ms later (E-30 §1,
          // measured); here it is dispatched by hand.
          fireEvent.mouseEnter(need(stripOf(edge)))
          act(() => {
            vi.advanceTimersByTime(CHROME_AUTOHIDE_MS * 3)
          })
          expect(
            watch.flips(),
            `E-31: the ${edge} strip mounted under a pointer that was already there, which is not an entry — one transition, away, and nothing after it`,
          ).toEqual(['hidden'])
        } finally {
          watch.stop()
        }
      } finally {
        vi.useRealTimers()
      }
    },
  )

  /**
   * …and the strips are not disarmed, only the phantom is removed.
   *
   * Two ways back in, because they are two different pieces of the rule: the
   * pointer that walks out over the page and reaches for the edge again reads
   * the *movement* memory, and the pointer that leaves the window entirely reads
   * the root's `pointerleave` — without which a reader who left through the top
   * edge would come back through it to a strip that will not answer.
   */
  it.each([
    {
      how: 'walks back over the page and reaches for the edge again',
      comeBack: () => {
        movePointerOver(zones(), overThePage())
      },
    },
    {
      how: 'leaves the window altogether and comes back in through the edge',
      comeBack: () => {
        // `relatedTarget: null` is the browser saying "nowhere", which is how a
        // pointer leaving the window is announced.
        crossPointer('pointerout', need(stripOf('top')), null)
      },
    },
  ])('wakes again once the pointer $how (E-31 removes a phantom, not a feature)', async ({
    comeBack,
  }) => {
    await setup()
    vi.useFakeTimers()
    try {
      reachForTheEdge('top')
      const watch = watchChrome()
      try {
        fireEvent.keyDown(window, { key: 'h' })
        fireEvent.mouseEnter(need(stripOf('top')))
        expect(
          watch.flips(),
          'the strip arriving under the resting pointer is the wake E-31 refuses',
        ).toEqual(['hidden'])

        comeBack()
        fireEvent.mouseEnter(need(stripOf('top')))
        expect(
          watch.flips(),
          'E-31: only genuine movement into the strip wakes — and it still does',
        ).toEqual(['hidden', 'visible'])
      } finally {
        watch.stop()
      }
    } finally {
      vi.useRealTimers()
    }
  })

  /**
   * **There are two bands, and the memory has to say *which*.**
   *
   * The gate above answers "was the pointer already inside a strip's area?" —
   * and a single boolean answers that question for *both* strips at once. A
   * pointer parked in the top band therefore silences the **bottom** strip, and
   * the reverse, even though crossing the screen into the other band is the
   * plainest entry there is: the pointer moved, and it moved into a strip it was
   * not in.
   *
   * The sequence below is the browser's, not an invention. Boundary events are
   * dispatched **before** the `mousemove` that lands at the new position, so the
   * band the gate reads on a crossing is always the one the *previous* sample
   * reported. A flick fast enough that no sample lands between the two bands —
   * one frame's worth of pointer travel across the viewport — hands the bottom
   * strip's `mouseenter` a memory that still says `top`.
   *
   * Both directions, because they are two `onMouseEnter`s and a fix applied to
   * one leaves the other as it was.
   */
  it.each([
    { from: 'top', into: 'bottom' },
    { from: 'bottom', into: 'top' },
  ] as const)(
    'wakes when the pointer crosses from the $from band into the $into strip (E-31: two bands, not one)',
    async ({ from, into }) => {
      await setup()
      vi.useFakeTimers()
      try {
        reachForTheEdge(from)
        const watch = watchChrome()
        try {
          fireEvent.keyDown(window, { key: 'h' })
          // Its own strip, under a pointer that has not moved: refused, and that
          // half is the rule working. Asserted so the test cannot pass by simply
          // waking everything.
          fireEvent.mouseEnter(need(stripOf(from)))
          expect(
            watch.flips(),
            `E-31: the ${from} strip mounted under a pointer already in the ${from} band`,
          ).toEqual(['hidden'])

          // Now the pointer crosses the screen. Nothing about the *other* band
          // was ever true of it.
          fireEvent.mouseEnter(need(stripOf(into)))
          expect(
            watch.flips(),
            `E-31: entering the ${into} strip from the ${from} band is an entry — the ${from} band is not the ${into} band, and one boolean cannot tell them apart`,
          ).toEqual(['hidden', 'visible'])
        } finally {
          watch.stop()
        }
      } finally {
        vi.useRealTimers()
      }
    },
  )

  /**
   * **E-31's 적용 범위**: the rule stands on every path that hides the chrome.
   *
   * The ruling says so in as many words, and says why — the auto-hide cannot
   * reach this state *today* only because E-30's hold gets there first, so a
   * rule written into the `H` handler would sit there looking correct until the
   * hold rule changes and then fail silently. Neither path below goes anywhere
   * near a key handler.
   */
  it.each([
    {
      path: 'a centre tap',
      hide: () => {
        tapAt(500)
      },
    },
    {
      path: 'the 2 600 ms auto-hide',
      hide: () => {
        act(() => {
          vi.advanceTimersByTime(CHROME_AUTOHIDE_MS)
        })
      },
    },
  ])('stands when the chrome goes to $path, not only to `H` (E-31 적용 범위)', async ({ hide }) => {
    await setup()
    vi.useFakeTimers()
    try {
      // No `pointerover` on the bar, so nothing holds the chrome: this is the
      // state the ruling describes as out of reach *for now*, and the reason it
      // refuses to let the rule be scoped to the key that reaches it.
      reachForTheEdge('top')
      act(() => {
        vi.advanceTimersByTime(TAP_ARMED_MS)
      })

      const watch = watchChrome()
      try {
        hide()
        expect(
          useViewerStore.getState().chromeVisible,
          'the hide path under test has to have hidden something',
        ).toBe(false)
        fireEvent.mouseEnter(need(stripOf('top')))
        act(() => {
          vi.advanceTimersByTime(CHROME_AUTOHIDE_MS * 3)
        })
        expect(
          watch.flips(),
          'E-31 적용 범위: the strip does not know which path dismissed the chrome, and must not need to',
        ).toEqual(['hidden'])
      } finally {
        watch.stop()
      }
    } finally {
      vi.useRealTimers()
    }
  })

  /**
   * **E-30 is untouched.** The hold is still a function of where the pointer is.
   *
   * The gate above reads movement; the hold reads the browser's answer to "what
   * is under the pointer now". Implement both from one signal and E-31 says the
   * defect returns — so this walks the path E-30 exists for, on top of the new
   * gate, and asserts the transition list rather than the attribute for the
   * reason E-30 §1 gives: an oscillating chrome answers `visible` to almost
   * every sample of its state.
   */
  it('still holds a chrome the edge just summoned, under a pointer that never moved (E-30 §1)', async () => {
    await setup()
    vi.useFakeTimers()
    try {
      movePointerOver(zones(), overThePage())
      const watch = watchChrome()
      try {
        fireEvent.mouseEnter(need(stripOf('top')))
        expect(
          document.querySelector('[data-role="viewer-edge-top"]'),
          'the strip is gone and the bar is where the pointer is: that is the whole of E-30',
        ).toBeNull()
        crossPointer('pointerover', topBar(), viewerRoot())
        act(() => {
          vi.advanceTimersByTime(CHROME_AUTOHIDE_MS * 3)
        })
        expect(
          watch.flips(),
          'E-30: one transition, and the reader is left looking at the control they are about to press — no auto-hide, and no strip summoning it back',
        ).toEqual(['visible'])
      } finally {
        watch.stop()
      }
    } finally {
      vi.useRealTimers()
    }
  })
})

// ---------------------------------------------------------------------------
// Acceptance 4 & 5 — the spread
// ---------------------------------------------------------------------------

describe('양면 and RTL (acceptance 4, 5; ui-spec §6.2)', () => {
  it('keeps the DOM order ascending and flips the flow to row-reverse', async () => {
    await setup({ prefs: { display_mode: 'spread', reading_direction: 'rtl' } })
    // The whole rule: page 12 is *first in the DOM* and *on the right*.
    expect(framePages()).toEqual(['12', '13'])
    expect(stage()).toHaveAttribute('data-flow', 'row-reverse')
    expect(stage().style.flexDirection).toBe('row-reverse')
  })

  it('flows left-to-right under L→R with the same DOM order', async () => {
    await setup({ prefs: { display_mode: 'spread', reading_direction: 'ltr' } })
    expect(framePages()).toEqual(['12', '13'])
    expect(stage().style.flexDirection).toBe('row')
  })

  it('never pairs the page before a landscape scan (FR-VWR-004)', async () => {
    await setup({ prefs: { display_mode: 'spread' }, search: '?page=6' })
    expect(framePages()).toEqual(['6'])
  })

  it('never pairs the landscape scan itself', async () => {
    await setup({ prefs: { display_mode: 'spread' }, search: '?page=7' })
    expect(framePages()).toEqual(['7'])
  })

  it('pairs normally on either side of it', async () => {
    await setup({ prefs: { display_mode: 'spread' }, search: '?page=8' })
    expect(framePages()).toEqual(['8', '9'])
  })
})

// ---------------------------------------------------------------------------
// Acceptance 3 — the fit rules
// ---------------------------------------------------------------------------

describe('fit (acceptance 3; FR-VWR-005, ui-spec §6.2)', () => {
  function frame(): HTMLElement {
    const el = document.querySelector('[data-role="page-frame"]')
    if (el === null) throw new Error('no page frame')
    return el as HTMLElement
  }

  it('defaults to 높이 and gives the page a stage-sized box to resolve against', async () => {
    // C-13 / D-38. `height:100%` on the image is inert unless the frame it
    // resolves against has a definite height — without this the page renders at
    // its intrinsic size and the stage clips the top and bottom of every page.
    await setup()
    expect(stage()).toHaveAttribute('data-fit', 'height')
    expect(frame().style.height).toBe('100%')
    const img = document.querySelector('img[data-role="page"]')
    expect(img).toHaveStyle({ height: '100%', width: 'auto' })
  })

  it('opens a book stored at 화면 on 화면, with that radio checked (E-44)', async () => {
    // Both halves of E-27 §1, inverted together. That ruling deleted the 화면
    // segment and, *because* it had deleted it, opened this book on 높이 —
    // nobody may be parked on a fit with no button. E-44 restores the button, so
    // the coercion becomes the thing that contradicts the reader: their stored
    // fit refused, by a control sitting on screen showing a different one.
    // The `contain` geometry never moved — it is asserted pure in `fit.test.ts`;
    // what is pinned here is that a reader can arrive at it.
    await setup({ prefs: { fit_mode: 'contain' } })
    expect(stage()).toHaveAttribute('data-fit', 'contain')
    expect(screen.getByRole('radio', { name: '화면' })).toBeChecked()
  })

  it('offers four fits in E-44 §1 order — 너비 · 높이 · 화면 · 원본', async () => {
    // Order is a keyboard mapping, not decoration: `Seg` is a single tab stop
    // whose ←/→ walk the radios in DOM order — nothing in `Seg` implements that
    // (it has no keydown handler at all), it falls out of every option's input
    // sharing one `useId()` as its `name` (`web/src/components/ds/Seg.tsx:8-9`,
    // `:50`, `:65`), which makes the four one native radio group. So a reorder
    // silently remaps ←/→ for every reader — which is why it is pinned rather
    // than left to a diff.
    //
    // **화면 was fourth and is now third** — the live design project moved it
    // when it restored it (fetched 2026-08-09: `isFitW · isFitH · isFitS ·
    // isFitO`). Everything in the repo predates that: `docs/ui-html/…zip`
    // (2026-07-28) and `viewer-overlay-visible-1440.png` both read
    // 너비 · 높이 · 원본 · 화면. They are the old position, not a contradiction;
    // see the long note on `FIT_OPTIONS` in `ViewerTopBar.tsx`.
    //
    // Asserted as one list carrying *both* halves of every option rather than
    // four `getByRole` existence checks, because a deletion, a reorder, a
    // relabelled 화면 and a segment wired to the wrong wire value are four
    // different regressions and a presence check catches only the first.
    await setup()
    const fits = within(screen.getByRole('radiogroup', { name: '맞춤' })).getAllByRole('radio')
    expect(fits.map((el) => [el.getAttribute('value'), el.closest('label')?.textContent])).toEqual([
      ['width', '너비'],
      ['height', '높이'],
      // C-2: `contain` on the wire, 화면 on the button. There is no `screen`.
      ['contain', '화면'],
      ['original', '원본'],
    ])
  })

  it('draws each fit with its E-44 §1 icon — 화면 is `Maximize`', async () => {
    // The one value E-44 introduced that nothing else held: swapping 화면's
    // `Maximize` for `MoveHorizontal` in `FIT_OPTIONS` left typecheck, lint and
    // every other test green. The icon is BINDING (E-44 §1, ui-spec §6.6), and
    // in a segmented control it is what the row is *read* by — the labels are
    // two and three characters at 13 px — so a wrong glyph is a wrong button to
    // anyone scanning rather than reading.
    //
    // Pinned on the class, not on the path data. lucide's `createLucideIcon`
    // puts `lucide-${kebab(iconName)}` on every icon's `<svg>` next to a bare
    // `lucide` (`web/node_modules/lucide-react/dist/esm/createLucideIcon.js`
    // and `Icon.js`, v0.474.0), so the class *is* the icon's identity: it names
    // which icon is wrong when this fails, and it survives lucide redrawing a
    // glyph, which a `d` attribute would not. Asserting the whole class string
    // rather than a `toContain` also catches the icon being dropped for a bare
    // `<svg>`. The glyphs are `aria-hidden` by rule (`Seg.tsx:73-77` — the label
    // stays the accessible name), so there is no accessible name to check
    // instead; the DOM is the only place the choice is visible to a test.
    await setup()
    const icons = within(screen.getByRole('radiogroup', { name: '맞춤' }))
      .getAllByRole('radio')
      .map((el) => el.closest('label')?.querySelector('svg')?.getAttribute('class'))
    expect(icons).toEqual([
      'lucide lucide-move-horizontal',
      'lucide lucide-move-vertical',
      'lucide lucide-maximize',
      'lucide lucide-image',
    ])
  })

  it('gives 너비 a definite width and lets the stage scroll', async () => {
    await setup({ prefs: { fit_mode: 'width' } })
    expect(frame().style.flex).toBe('1 1 0px')
    expect(stage().style.overflowY).toBe('auto')
    expect(document.querySelector('img[data-role="page"]')).toHaveStyle({ width: '100%' })
  })

  it('leaves 원본 at the intrinsic size with no stage padding', async () => {
    await setup({ prefs: { fit_mode: 'original' } })
    expect(frame().style.height).toBe('')
    expect(frame().style.flex).toBe('0 0 auto')
    expect(stage().style.padding).toBe('0px')
    expect(stage().style.overflowX).toBe('auto')
  })
})

// ---------------------------------------------------------------------------
// Acceptance 7 — keyboard
// ---------------------------------------------------------------------------

describe('keyboard (acceptance 7; ui-spec §8.2)', () => {
  it('inverts the arrow keys under R→L', async () => {
    await setup({ prefs: { reading_direction: 'rtl' } })
    expect(counter()).toBe('12 / 214')

    fireEvent.keyDown(window, { key: 'ArrowLeft' })
    expect(counter()).toBe('13 / 214')
    fireEvent.keyDown(window, { key: 'ArrowRight' })
    expect(counter()).toBe('12 / 214')
  })

  it('reads the arrow keys literally under L→R', async () => {
    await setup({ prefs: { reading_direction: 'ltr' } })
    fireEvent.keyDown(window, { key: 'ArrowRight' })
    expect(counter()).toBe('13 / 214')
    fireEvent.keyDown(window, { key: 'ArrowLeft' })
    expect(counter()).toBe('12 / 214')
  })

  it('steps +2 in 양면 and clamps at both ends', async () => {
    await setup({ prefs: { display_mode: 'spread' } })
    fireEvent.keyDown(window, { key: 'ArrowRight' })
    expect(counter()).toBe('14 / 214')

    act(() => {
      useViewerStore.getState().goTo(213)
    })
    fireEvent.keyDown(window, { key: 'ArrowRight' })
    expect(counter()).toBe('214 / 214')
    fireEvent.keyDown(window, { key: 'ArrowRight' })
    expect(counter()).toBe('214 / 214')

    act(() => {
      useViewerStore.getState().goTo(1)
    })
    fireEvent.keyDown(window, { key: 'ArrowLeft' })
    expect(counter()).toBe('1 / 214')
  })

  it('always advances on Space, whatever the reading direction', async () => {
    await setup({ prefs: { reading_direction: 'rtl' } })
    fireEvent.keyDown(window, { key: ' ' })
    expect(counter()).toBe('13 / 214')
  })

  it('binds 1 / 2 / 3 to the display modes and T to the strip', async () => {
    await setup()
    fireEvent.keyDown(window, { key: '2' })
    expect(useViewerStore.getState().mode).toBe('spread')
    fireEvent.keyDown(window, { key: '3' })
    expect(useViewerStore.getState().mode).toBe('vertical')
    fireEvent.keyDown(window, { key: '1' })
    expect(useViewerStore.getState().mode).toBe('single')

    fireEvent.keyDown(window, { key: 't' })
    expect(await screen.findByRole('button', { name: /썸네일/ })).toHaveAttribute(
      'aria-pressed',
      'true',
    )
  })

  it('asks the browser for real fullscreen on F', async () => {
    const request = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(Element.prototype, 'requestFullscreen', {
      writable: true,
      configurable: true,
      value: request,
    })
    // jsdom implements neither; a browser reports `null` when nothing is fullscreen.
    Object.defineProperty(document, 'fullscreenElement', {
      writable: true,
      configurable: true,
      value: null,
    })
    await setup()
    fireEvent.keyDown(window, { key: 'f' })
    expect(request).toHaveBeenCalledTimes(1)
  })

  it('leaves the book on Escape', async () => {
    await setup()
    fireEvent.keyDown(window, { key: 'Escape' })
    expect(await screen.findByTestId('location')).toHaveTextContent(`/series/${SERIES_ID}`)
    expect(useViewerStore.getState().bookId).toBeNull()
  })
})

// ---------------------------------------------------------------------------
// Acceptance 8 — touch
// ---------------------------------------------------------------------------

describe('tap zones (acceptance 8; FR-VWR-011)', () => {
  it('resolves the side zones in reading order, not screen order', async () => {
    await setup({ prefs: { reading_direction: 'rtl' } })
    // R→L: the left third is *forward*.
    tapAt(100)
    expect(counter()).toBe('13 / 214')
    tapAt(900)
    expect(counter()).toBe('12 / 214')
  })

  it('toggles the chrome from the centre 36 %', async () => {
    await setup()
    // E-27: the viewer opens chromeless, so the centre tap is what *summons*
    // the chrome now, and a second one sends it away.
    expect(useViewerStore.getState().chromeVisible).toBe(false)
    tapAt(500)
    expect(useViewerStore.getState().chromeVisible).toBe(true)
    tapAt(500)
    expect(useViewerStore.getState().chromeVisible).toBe(false)
    // …and neither tap turned a page.
    expect(counter()).toBe('12 / 214')
  })
})

// ---------------------------------------------------------------------------
// Acceptance 6 — prefetch
// ---------------------------------------------------------------------------

describe('prefetch (acceptance 6; FR-VWR-006, FR-SRV-007)', () => {
  it('warms `settings.prefetch` pages ahead plus one behind, at the versioned URL', async () => {
    const recorded = await setup({ prefetch: 3 })
    await waitFor(() => {
      expect(recorded.prefetched.length).toBeGreaterThanOrEqual(4)
    })
    expect(recorded.prefetched).toEqual([
      `/api/books/${BOOK_ID}/pages/13?v=${BOOK_CV}`,
      `/api/books/${BOOK_ID}/pages/14?v=${BOOK_CV}`,
      `/api/books/${BOOK_ID}/pages/15?v=${BOOK_CV}`,
      `/api/books/${BOOK_ID}/pages/11?v=${BOOK_CV}`,
    ])
    // The `<img>` the page is actually shown through must use the same URL, or
    // the warm cache entry is never read.
    const img = document.querySelector('img[data-role="page"]')
    expect(img).toHaveAttribute('src', `/api/books/${BOOK_ID}/pages/12?v=${BOOK_CV}`)
  })

  it('honours a prefetch setting of 0', async () => {
    const recorded = await setup({ prefetch: 0 })
    await waitFor(() => {
      expect(recorded.prefetched).toEqual([`/api/books/${BOOK_ID}/pages/11?v=${BOOK_CV}`])
    })
  })
})

// ---------------------------------------------------------------------------
// Acceptance 9 & 10 — loading and failure
// ---------------------------------------------------------------------------

describe('page transitions (acceptance 9, 10; ui-spec §6.3, §6.4)', () => {
  it('keeps the previous page on screen while the next one loads', async () => {
    await setup()
    decodeShownPages()
    expect(document.querySelector('[data-role="previous-page"]')).toBeNull()

    fireEvent.keyDown(window, { key: 'ArrowRight' })

    // The frame must not have been remounted: page 12 is still painted, in flow,
    // under the undecoded page 13.
    const previous = document.querySelector('[data-role="previous-page"]')
    expect(previous).toHaveAttribute('src', `/api/books/${BOOK_ID}/pages/12?v=${BOOK_CV}`)
    expect(document.querySelectorAll('[data-role="page-frame"]')).toHaveLength(1)
    expect(document.querySelector('img[data-role="page"]')).toHaveAttribute(
      'src',
      `/api/books/${BOOK_ID}/pages/13?v=${BOOK_CV}`,
    )
  })

  it('shows nothing for a fast page and the 페이지 로딩 indicator for a slow one', async () => {
    await setup()
    decodeShownPages()
    expect(screen.queryByText('페이지 로딩')).not.toBeInTheDocument()

    fireEvent.keyDown(window, { key: 'ArrowRight' })
    // Below the threshold a spinner reads as jank, not as progress.
    expect(screen.queryByText('페이지 로딩')).not.toBeInTheDocument()

    await screen.findByText('페이지 로딩', undefined, {
      timeout: LOADING_INDICATOR_DELAY_MS * 4,
    })

    decodeShownPages()
    await waitFor(() => {
      expect(screen.queryByText('페이지 로딩')).not.toBeInTheDocument()
    })
  })

  it('scopes a failed page to its own frame, flush-left, with a retry', async () => {
    await setup({ detail: { error: 'CRC mismatch' } })
    const img = document.querySelector('img[data-role="page"]')
    if (img === null) throw new Error('no page image')
    fireEvent.error(img)

    const panel = await screen.findByRole('alert')
    expect(within(panel).getByText('이미지 로드 실패')).toBeInTheDocument()
    expect(within(panel).getByText('page_012.jpg — CRC mismatch')).toBeInTheDocument()
    expect(panel).toHaveClass('items-start')

    await userEvent.click(within(panel).getByRole('button', { name: '다시 시도' }))
    // The retry busts the cache with `_r`, and only for the page it was pressed on.
    expect(document.querySelector('img[data-role="page"]')).toHaveAttribute(
      'src',
      `/api/books/${BOOK_ID}/pages/12?v=${BOOK_CV}&_r=1`,
    )
    fireEvent.keyDown(window, { key: 'ArrowRight' })
    expect(document.querySelector('img[data-role="page"]')).toHaveAttribute(
      'src',
      `/api/books/${BOOK_ID}/pages/13?v=${BOOK_CV}`,
    )
  })
})

// ---------------------------------------------------------------------------
// Acceptance 13 — progress and preferences
// ---------------------------------------------------------------------------

describe('progress (acceptance 13; FR-VWR-009, FR-STT-001)', () => {
  it('resumes at the recorded last page when the route carries none', async () => {
    await setup({ search: '' })
    expect(counter()).toBe('42 / 214')
  })

  it('opens at the page the series screen asked for', async () => {
    await setup({ search: '?page=100' })
    expect(counter()).toBe('100 / 214')
  })

  it('debounces the write and sends the page the reader landed on', async () => {
    const recorded = await setup()
    fireEvent.keyDown(window, { key: 'ArrowRight' })
    fireEvent.keyDown(window, { key: 'ArrowRight' })
    fireEvent.keyDown(window, { key: 'ArrowRight' })

    await waitFor(
      () => {
        expect(recorded.progressPuts).toHaveLength(1)
      },
      { timeout: 3_000 },
    )
    // Three turns and an open, one request — and `completed` is left to the
    // server's `page === page_count` rule (FR-VWR-012).
    expect(recorded.progressPuts[0]).toEqual({ page: 15 })
  })

  it('flushes the pending write when the tab is hidden', async () => {
    const recorded = await setup()
    fireEvent.keyDown(window, { key: 'ArrowRight' })
    Object.defineProperty(document, 'visibilityState', {
      configurable: true,
      value: 'hidden',
    })
    fireEvent(document, new Event('visibilitychange'))

    // Well inside the 1 s debounce: closing the tab must not lose the page.
    await waitFor(() => {
      expect(recorded.progressPuts).toEqual([{ page: 13 }])
    })
    Object.defineProperty(document, 'visibilityState', {
      configurable: true,
      value: 'visible',
    })
  })

  /**
   * **The writer is off until *this* book has loaded (`progressReady`).**
   *
   * The guard reads `detail !== undefined && pageCount > 0`, and until now
   * nothing asserted it: deleting it left every test on this screen green,
   * because on a first mount `pageCount` is 0 anyway and `useProgressSync`
   * refuses on its own. The case it actually stands in front of is a **volume
   * change**. `page` and `pageCount` are store state that outlives the route
   * param, so between 다음 권 읽기 and the moment volume 2's detail arrives the
   * screen is holding volume 1's page against volume 2's id — and
   * `useProgressSync`'s effect re-runs precisely then, because `save`'s identity
   * changes with the new `bid`. Without the guard that re-run records **page 214
   * of a 190-page volume 2** (D-13 / NFR-DAT-004), which the server clamps and
   * then marks completed.
   *
   * Volume 2's detail is held open on purpose: the window is real but short, and
   * a test that raced it would pass on timing rather than on the guard.
   */
  it('writes nothing to the next volume until the next volume has loaded', async () => {
    let release = (): void => {
      throw new Error('the held handler was never installed')
    }
    const held = new Promise<void>((resolve) => {
      release = resolve
    })
    const { recorded } = await setupOnFakeClock({ search: '?page=214' })
    server.use(
      http.get(`${ORIGIN}/api/books/:bid`, async ({ params }) => {
        if (params.bid === BOOK_ID) return HttpResponse.json(detailOf())
        await held
        return HttpResponse.json({
          ...detailOf(),
          id: String(params.bid),
          page_count: 190,
          pages: PAGES.slice(0, 190),
          next_book_id: null,
        })
      }),
    )
    await tickUntil(() => screen.queryByText('권의 마지막 페이지') !== null)

    fireEvent.click(screen.getByRole('button', { name: '다음 권 읽기' }))
    // Long past the debounce, with volume 2 still in flight.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(PROGRESS_DEBOUNCE_MS * 3)
    })
    expect(writesTo(recorded, NEXT_BOOK_ID), 'volume 1’s page was written to volume 2').toEqual([])
    // Volume 1's own page did go out — the guard suppresses the wrong write, not
    // the right one.
    expect(writesTo(recorded, BOOK_ID)).toContainEqual({ page: 214 })

    release()
    await tickUntil(() => useViewerStore.getState().bookId === NEXT_BOOK_ID)
    await act(async () => {
      await vi.advanceTimersByTimeAsync(PROGRESS_DEBOUNCE_MS)
    })
    await tickUntil(() => writesTo(recorded, NEXT_BOOK_ID).length > 0)
    expect(writesTo(recorded, NEXT_BOOK_ID)).toEqual([{ page: 1 }])
  })

  it('does not warn when the progress is current', async () => {
    await setup()
    expect(screen.queryByText(STALE_NOTICE)).not.toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Ruling E-45 — the changed-file notice has a lifetime
// ---------------------------------------------------------------------------

const STALE_NOTICE = '파일이 변경되었습니다'

const staleNotice = (): HTMLElement | null =>
  document.querySelector('[data-role="stale-progress"]')

/** Only E-45's acknowledgement carries the flag; every other write must not. */
const acknowledgements = (recorded: Recorded): Recorded['progressPuts'] =>
  recorded.progressPuts.filter((put) => put.stale_seen === true)

/** The same, with the book each one was signed against (E-45 §1 REVISION). */
const signedFor = (recorded: Recorded): Recorded['progressWrites'] =>
  recorded.progressWrites.filter((write) => write.body.stale_seen === true)

/** Every progress write that landed on `bid`, in order. */
const writesTo = (recorded: Recorded, bid: string): ProgressPutBody[] =>
  recorded.progressWrites.filter((write) => write.bid === bid).map((write) => write.body)

/**
 * `파일이 변경되었습니다` — the half of E-45 that lives in the browser.
 *
 * **What the test this replaces was worth.** It was called *"warns **once** when
 * the recorded progress no longer matches the file"* and asserted a single
 * `getByText`. It measured no timer, no disappearance and no second entry; it
 * passed because it finished before the 1 s progress debounce did. The defect it
 * was named for — the notice living about a second and then never again — was
 * sitting underneath it the whole time.
 *
 * The mechanism, so these tests are read as a set: the screen used to derive the
 * notice per render from `detail.progress.stale` in the React Query cache, and
 * `useSaveProgress.onSuccess` replaces that cache entry with the `PUT`'s own
 * `progress`, whose `stale` is `false`. The `PUT` goes out because the book
 * loaded — the reader need not turn a single page — so the notice unmounted on
 * its own save path. Hence the two shapes asserted below: it **survives** the
 * write, and it dies of **nothing but its own clock**.
 */
describe('the changed-file notice has a lifetime (ruling E-45)', () => {
  const staleBook: SetupOptions = { detail: { progress: progressOf({ stale: true }) } }

  afterEach(() => {
    vi.useRealTimers()
  })

  /**
   * Let the automatic write — the one nobody asked for — reach the server.
   *
   * It is buffered behind `PROGRESS_DEBOUNCE_MS`, well past what `tickUntil`
   * budgets, so the debounce is spent deliberately rather than waited out.
   */
  async function letTheAutomaticWriteLand(recorded: Recorded): Promise<void> {
    await act(async () => {
      await vi.advanceTimersByTimeAsync(PROGRESS_DEBOUNCE_MS)
    })
    await tickUntil(() => recorded.progressPuts.length > 0)
  }

  it('outlives the progress write that overwrites the cache it used to read', async () => {
    const { recorded, client } = await setupOnFakeClock(staleBook)
    expect(staleNotice()).toBeInTheDocument()
    // A notice that removes itself and has no live region has a lifetime of
    // zero on a screen reader (E-45 §1); the chrome hint already had one.
    expect(staleNotice()).toHaveAttribute('role', 'status')

    // Nobody touched anything: this write exists because the book loaded.
    await letTheAutomaticWriteLand(recorded)
    expect(recorded.progressPuts[0]).toEqual({ page: 12 })
    expect(acknowledgements(recorded), 'a page write is not consent').toEqual([])

    // The cache really has been overwritten with `stale: false` — this is the
    // exact state in which the old screen unmounted the notice…
    await tickUntil(
      () => client.getQueryData<BookDetail>(queryKeys.books.detail(BOOK_ID))?.progress?.stale === false,
    )
    // …and the latched one keeps it up, because the reader has not seen it yet.
    expect(staleNotice()).toBeInTheDocument()
    expect(screen.getByText(STALE_NOTICE)).toBeInTheDocument()
  })

  it('goes down when its lifetime runs out, and acknowledges itself only then', async () => {
    const { recorded } = await setupOnFakeClock(staleBook)
    await letTheAutomaticWriteLand(recorded)
    expect(staleNotice()).toBeInTheDocument()

    await act(async () => {
      await vi.advanceTimersByTimeAsync(STALE_NOTICE_MS)
    })
    expect(staleNotice()).not.toBeInTheDocument()

    // E-45 §2: the reader saw the whole notice, so the server may re-baseline.
    await tickUntil(() => acknowledgements(recorded).length > 0)
    expect(acknowledgements(recorded)).toEqual([{ page: 12, stale_seen: true }])
  })

  it('acknowledges nothing when the reader leaves before the notice is done', async () => {
    const { recorded, unmount } = await setupOnFakeClock(staleBook)
    await act(async () => {
      await vi.advanceTimersByTimeAsync(Math.floor(STALE_NOTICE_MS / 2))
    })
    // The proof that this test is inside the window rather than past it.
    expect(staleNotice()).toBeInTheDocument()

    unmount()
    await act(async () => {
      await vi.advanceTimersByTimeAsync(STALE_NOTICE_MS * 2)
    })

    // The right ending (E-45 §2): the baseline survives, so the next entry
    // warns again. A stale timer that outlived the screen would have signed for
    // a notice the reader closed the tab on.
    expect(acknowledgements(recorded)).toEqual([])
    expect(recorded.progressPuts.every((put) => put.stale_seen === undefined)).toBe(true)
  })

  it('does not come back when the book is fetched again inside the same entry', async () => {
    const { recorded, client } = await setupOnFakeClock(staleBook)
    await act(async () => {
      await vi.advanceTimersByTimeAsync(STALE_NOTICE_MS)
    })
    expect(staleNotice()).not.toBeInTheDocument()
    // Every write has to be *finished*, not merely sent: a mutation still in
    // flight lands its `onSuccess` after the refetch below and puts
    // `stale: false` back, at which point the cache agrees with the screen for
    // the wrong reason and this test passes against the very defect it is for.
    // `isMutating() === 0` is the only form of that guarantee available here.
    await tickUntil(() => acknowledgements(recorded).length > 0 && client.isMutating() === 0)

    // The server has not been told anything yet, so `books.detail` refills with
    // `stale: true`. Armed **once per entry** means the notice stays down: a
    // render that derives from the cache would put it straight back up, which
    // is the same wiring fault seen from the other side.
    // The refetched payload is marked, so the wait below can be answered by the
    // *screen* rather than by the cache. Waiting on the cache alone is a race
    // this test lost once: the store write lands a tick before React re-renders
    // from it, so a cache-only gate can assert the DOM before the render that
    // would have put the notice back.
    const REFETCHED = '군계(軍鷄) 01권 — 다시 읽어온 이름.zip'
    server.use(
      http.get(`${ORIGIN}/api/books/:bid`, () =>
        HttpResponse.json({
          ...detailOf(undefined, { progress: progressOf({ stale: true }) }),
          name: REFETCHED,
        }),
      ),
    )
    act(() => {
      void client.refetchQueries({ queryKey: queryKeys.books.detail(BOOK_ID) })
    })
    await tickUntil(() => screen.queryByText(REFETCHED) !== null)

    expect(
      client.getQueryData<BookDetail>(queryKeys.books.detail(BOOK_ID))?.progress?.stale,
      'the refetch has to reinstate the flag, or this test proves nothing',
    ).toBe(true)
    expect(staleNotice()).not.toBeInTheDocument()
  })

  /**
   * **The lifetime, measured on the screen (E-45 §4-2).**
   *
   * The store tier already pins `STALE_NOTICE_MS - 1` against `STALE_NOTICE_MS`,
   * but the ruling asks for the duration to be measurable *here*, where the
   * timer, the render and the save path are wired together — and the two cases
   * around this one sample the clock so far apart that `STALE_NOTICE_MS` could
   * be retuned to anything between them without a screen test noticing.
   *
   * The two-millisecond slack is `mountUntilStaleArmed`'s tick, not tolerance
   * for the duration: it is far tighter than any retuning this is meant to
   * catch, and it does not widen with the value under test.
   */
  it('measures its own lifetime, on the screen and not only in the store', async () => {
    const { armedAt } = await mountUntilStaleArmed(staleBook)

    await advanceTo(armedAt + STALE_NOTICE_MS - 2)
    expect(staleNotice(), 'gone early — the notice is shorter than its constant').toBeInTheDocument()

    await advanceTo(armedAt + STALE_NOTICE_MS)
    expect(staleNotice(), 'still up — the notice is longer than its constant').not.toBeInTheDocument()
  })

  /**
   * **A page turn inside the debounce leaves with the acknowledgement, as one
   * request (E-45 §2).**
   *
   * This is the only reachable state in which `acknowledgeStale` finds anything
   * in `useSaveProgress`'s buffer at all, and it is worth pinning because the
   * obvious implementation — acknowledge with its own `mutate` and leave the
   * buffer alone — sends **two** writes, with the plain page landing *after* the
   * flag. See the note on `acknowledgeStale` in `queries.ts` for why the merge
   * itself cannot change the body.
   */
  it('carries a page turned inside the debounce out with the acknowledgement', async () => {
    const { recorded, armedAt } = await mountUntilStaleArmed(staleBook)
    // The automatic write is long gone by here; page 13 is the reader's own.
    await advanceTo(armedAt + STALE_NOTICE_MS - 400)
    expect(recorded.progressPuts).toEqual([{ page: 12 }])

    fireEvent.keyDown(window, { key: 'ArrowRight' })
    await advanceTo(armedAt + STALE_NOTICE_MS)
    await tickUntil(() => acknowledgements(recorded).length > 0)

    // One request, not two, and the flag rides the page rather than chasing it.
    expect(recorded.progressPuts).toEqual([{ page: 12 }, { page: 13, stale_seen: true }])
    // Still only one after the debounce the turn armed would have expired.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(PROGRESS_DEBOUNCE_MS * 2)
    })
    expect(recorded.progressPuts).toEqual([{ page: 12 }, { page: 13, stale_seen: true }])
  })

  /**
   * **The next volume is a different file, so it is asked again (E-45 §1
   * REVISION).**
   *
   * This case replaces one that asserted the opposite. The first cut reused
   * `open()`'s `continuing` judgement for the warning as well as for the opening
   * hint, on the reasoning that one word should not carry two criteria — and
   * that saving cost two defects at once. This is the second: a volume 2 that
   * really *had* changed underneath the reader was never announced.
   */
  it('warns again on the next volume, and signs that volume’s own notice', async () => {
    const { recorded } = await setupOnFakeClock({ ...staleBook, search: '?page=214' })
    await tickUntil(() => screen.queryByText('권의 마지막 페이지') !== null)
    await act(async () => {
      await vi.advanceTimersByTimeAsync(STALE_NOTICE_MS)
    })
    expect(staleNotice()).not.toBeInTheDocument()
    await tickUntil(() => acknowledgements(recorded).length > 0)

    // The next volume answers `stale: true` too (the MSW book handler serves the
    // same progress under any id) — and it is a different file, so the reader is
    // told about it.
    fireEvent.click(screen.getByRole('button', { name: '다음 권 읽기' }))
    await tickUntil(() => useViewerStore.getState().bookId === NEXT_BOOK_ID)
    expect(staleNotice()).toBeInTheDocument()

    await act(async () => {
      await vi.advanceTimersByTimeAsync(STALE_NOTICE_MS)
    })
    await tickUntil(() => signedFor(recorded).length > 1)
    // Two notices, two acknowledgements, each against its own book — never two
    // against the same one, and never volume 1's notice signed as volume 2.
    expect(signedFor(recorded)).toEqual([
      { bid: BOOK_ID, body: { page: 214, stale_seen: true } },
      { bid: NEXT_BOOK_ID, body: { page: 1, stale_seen: true } },
    ])
  })

  /**
   * **The measured defect: 다음 권 읽기 *inside* the notice's window.**
   *
   * A plain path — resume volume 1, read the warning, press the button before
   * the 3 400 ms are up. Volume 1's timer used to survive the volume change,
   * latch a moment later, and be signed here as volume 2, because the writer is
   * bound to the route's `:bid`. The observed request was
   * `{"bid":"nextbook…","body":{"page":1,"stale_seen":true}}`: **volume 2's
   * baseline burnt over a warning its reader was never shown.**
   *
   * The right ending is neither book signed. Volume 1's notice did not run its
   * course, so it is not consent (E-45 §2, the same rule as leaving the screen);
   * volume 2's has only just started.
   */
  it('signs neither book when the reader moves on inside the window', async () => {
    const { recorded } = await setupOnFakeClock({ ...staleBook, search: '?page=214' })
    await tickUntil(() => screen.queryByText('권의 마지막 페이지') !== null)
    // The proof that this test is inside volume 1's window rather than past it.
    expect(staleNotice()).toBeInTheDocument()
    expect(acknowledgements(recorded)).toEqual([])

    fireEvent.click(screen.getByRole('button', { name: '다음 권 읽기' }))
    await tickUntil(() => useViewerStore.getState().bookId === NEXT_BOOK_ID)
    // Volume 1's timer is gone, and the latch — if anything ever set it — could
    // no longer name this book.
    expect(useViewerStore.getState().staleBookId).toBe(NEXT_BOOK_ID)

    // Straight through the instant volume 1's timer would have fired.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(STALE_NOTICE_MS - 1)
    })
    expect(signedFor(recorded), 'volume 1 was left inside its window').toEqual([])

    // …and volume 2's own notice, which started at the volume change, is still
    // running: it has a full life of its own, not the remainder of volume 1's.
    expect(staleNotice()).toBeInTheDocument()
  })
})

describe('per-book preferences (FR-VWR-002 / D-35)', () => {
  it('persists the display mode, direction and fit to the book', async () => {
    const recorded = await setup()
    await userEvent.click(screen.getByRole('radio', { name: '양면' }))
    await userEvent.click(screen.getByRole('radio', { name: 'R→L' }))
    await userEvent.click(screen.getByRole('radio', { name: '너비' }))

    await waitFor(() => {
      expect(recorded.prefsPuts).toHaveLength(3)
    })
    expect(recorded.prefsPuts).toEqual([
      { display_mode: 'spread' },
      { reading_direction: 'rtl' },
      { fit_mode: 'width' },
    ])
    // C-1: the wire value, never `double`.
    expect(useViewerStore.getState().mode).toBe('spread')
    expect(useViewerStore.getState().fit).toBe('width')
  })

  it('persists 화면 as `contain`, and the stage takes the fit (E-44)', async () => {
    // The other end of the round trip the test above walks for 너비. It is worth
    // its own case because 화면 is the one fit whose *press* had no code path at
    // all under E-27 §1 — the segment did not exist, so nothing could send this
    // body, and `openingFit` would have turned the stored value back into
    // `height` on the next open. Both ends are asserted: the wire word (C-2 —
    // `contain`, never `screen`) and the `data-fit` the stage actually resolves
    // its geometry from (`fit.ts`).
    const recorded = await setup()
    await userEvent.click(screen.getByRole('radio', { name: '화면' }))

    await waitFor(() => {
      expect(recorded.prefsPuts).toEqual([{ fit_mode: 'contain' }])
    })
    expect(stage()).toHaveAttribute('data-fit', 'contain')
    expect(screen.getByRole('radio', { name: '화면' })).toBeChecked()
  })

  it('opens with the book’s own preferences, not the global defaults', async () => {
    await setup({
      prefs: { display_mode: 'spread', reading_direction: 'rtl', fit_mode: 'original' },
    })
    expect(stage()).toHaveAttribute('data-fit', 'original')
    expect(stage()).toHaveAttribute('data-dir', 'rtl')
    expect(stage()).toHaveAttribute('data-mode', 'spread')
  })

  it('keeps the reader in place when a preference is saved', async () => {
    // `useSetPrefs` writes the new prefs into the book cache; a naive effect
    // would treat that as a fresh book and throw the reader back to page 42.
    await setup()
    fireEvent.keyDown(window, { key: 'ArrowRight' })
    expect(counter()).toBe('13 / 214')
    await userEvent.click(screen.getByRole('radio', { name: '양면' }))
    await waitFor(() => {
      expect(useViewerStore.getState().mode).toBe('spread')
    })
    expect(counter()).toBe('13 / 214')
  })
})

// ---------------------------------------------------------------------------
// Acceptance 11 & 14 — end of volume, slider
// ---------------------------------------------------------------------------

function scrim(): HTMLElement | null {
  return document.querySelector<HTMLElement>('[data-role="next-volume-scrim"]')
}

/**
 * Click the scrim itself, which is what "outside the card" means.
 *
 * The throw is the assertion: without the card up there is nothing to dismiss,
 * so a case that reached here by accident would otherwise click nothing and go
 * on to pass for the wrong reason.
 */
async function clickOutsideTheCard(): Promise<void> {
  const element = scrim()
  if (element === null) throw new Error('the end-of-volume scrim must be up to be dismissed')
  await userEvent.click(element)
}

describe('end of volume (acceptance 11; ui-spec §6.5)', () => {
  it('raises the next-volume card on the last page and follows it', async () => {
    await setup({ search: '?page=214' })
    const card = await screen.findByText('권의 마지막 페이지')
    expect(card).toBeInTheDocument()
    expect(await screen.findByText('군계(軍鷄) 02권.zip')).toBeInTheDocument()

    await userEvent.click(screen.getByRole('button', { name: '다음 권 읽기' }))
    await waitFor(() => {
      expect(useViewerStore.getState().pageCount).toBe(PAGE_COUNT)
    })
    expect(counter()).toBe('1 / 214')
  })

  it('changes the volume without replaying the opening ceremony', async () => {
    await setup({ search: '?page=214' })
    await screen.findByText('권의 마지막 페이지')

    // The reader has the chrome up and has long since read the hint.
    act(() => {
      useViewerStore.getState().toggleChrome()
    })
    expect(useViewerStore.getState().chromeVisible).toBe(true)
    expect(useViewerStore.getState().hintVisible).toBe(false)

    await userEvent.click(screen.getByRole('button', { name: '다음 권 읽기' }))
    await waitFor(() => {
      expect(useViewerStore.getState().page).toBe(1)
    })

    // 다음 권 읽기 is a second `open()` on a still-mounted screen. Treating it
    // as an entry took the chrome back down and put the "where did the controls
    // go" line back up on every single volume.
    expect(useViewerStore.getState().chromeVisible).toBe(true)
    expect(useViewerStore.getState().hintVisible).toBe(false)
    expect(document.querySelector('[data-role="viewer-chrome-hint"]')).toBeNull()
  })

  it('never raises it in 세로, where scrolling past the end is the end', async () => {
    await setup({ prefs: { display_mode: 'vertical' }, search: '?page=214' })
    expect(screen.queryByText('권의 마지막 페이지')).not.toBeInTheDocument()
  })

  /**
   * The scrim covers the stage, so while the card is up the last page cannot be
   * re-read: the tap zones and the click-to-turn are underneath it and the only
   * ways on are the card's own three buttons. Clicking outside the card gives
   * the page back, and a forward turn — the same gesture that raised it —
   * brings it back.
   *
   * This is asserted on the *screen* rather than on `NextVolumeCard`, which has
   * its own test for the outside-click. A card that dismissed itself perfectly
   * while `ViewerPage` re-rendered it anyway would pass that one and fail here.
   */
  it('puts the card away on an outside click, and a forward turn brings it back', async () => {
    await setup({ search: '?page=214' })
    await screen.findByText('권의 마지막 페이지')

    expect(scrim()).not.toBeNull()

    await clickOutsideTheCard()
    expect(screen.queryByText('권의 마지막 페이지')).not.toBeInTheDocument()
    expect(scrim(), 'the scrim goes with it — that is what frees the stage').toBeNull()
    // Dismissing is not a page turn: the reader is still on the last page, now
    // able to read it and to go back.
    expect(useViewerStore.getState().page).toBe(214)

    // …and going back is possible, which is the whole point of the dismissal.
    act(() => {
      useViewerStore.getState().turnTo(213)
    })
    expect(counter()).toBe('213 / 214')
    expect(screen.queryByText('권의 마지막 페이지')).not.toBeInTheDocument()
  })

  it('raises the card again when the reader turns forward at the end', async () => {
    await setup({ search: '?page=214' })
    await screen.findByText('권의 마지막 페이지')
    await clickOutsideTheCard()
    expect(screen.queryByText('권의 마지막 페이지')).not.toBeInTheDocument()

    // `Space` is always forward whatever the reading direction (useViewerKeys),
    // and at the last page it has nowhere to go — which is exactly the request
    // the card answers.
    fireEvent.keyDown(window, { key: ' ' })
    expect(await screen.findByText('권의 마지막 페이지')).toBeInTheDocument()
    expect(useViewerStore.getState().page, 'and it did not move the reader').toBe(214)
  })

  it('forgets the dismissal once the reader leaves the last page', async () => {
    await setup({ search: '?page=214' })
    await screen.findByText('권의 마지막 페이지')
    await clickOutsideTheCard()
    expect(screen.queryByText('권의 마지막 페이지')).not.toBeInTheDocument()

    act(() => {
      useViewerStore.getState().turnTo(200)
    })
    expect(screen.queryByText('권의 마지막 페이지')).not.toBeInTheDocument()

    // Coming back to the end is a fresh arrival, not a continuation of the
    // dismissal — otherwise the card would be gone for the rest of the volume.
    act(() => {
      useViewerStore.getState().turnTo(214)
    })
    expect(await screen.findByText('권의 마지막 페이지')).toBeInTheDocument()
  })
})

describe('the bottom bar (acceptance 12, 14; ui-spec §6.7)', () => {
  it('leaves the slider box to the stylesheet, on the viewer’s lighter track', async () => {
    await setup({ width: 500 })
    const slider = screen.getByRole('slider', { name: '페이지' })
    // 24px normally, 44px below 768 — one rule in `base.css` (asserted in
    // `tokens.test.ts`) rather than a 44px inline height at every width, which
    // is what made the bottom bar 12px taller than the design on a desktop.
    expect(slider.style.height).toBe('')
    expect([...slider.classList]).toContain('on-dark')
  })

  it('re-lays out the thumbnail strip when the slot size crosses 768', async () => {
    // `virtual-core` memoises its measurements on
    // `[count, paddingStart, scrollMargin, getItemKey, enabled]` + the size
    // cache — **not** on `estimateSize`. Handing it a bigger slot therefore
    // changes nothing on its own. Measured in Chrome at 900 → 700 with the
    // strip open: the cells grew to 56px while the pitch stayed 52px (four
    // pixels of overlap per thumb) and the track stayed 5 044px where the
    // pages then needed 5 820, so the tail was unreachable.
    //
    // The track width is pure `estimateSize` arithmetic, so jsdom's lack of
    // layout does not weaken this.
    await setup({ width: 900 })
    act(() => {
      useViewerStore.getState().setStripOpen(true)
    })
    const track = (): HTMLElement => {
      const el = document.querySelector('[data-role="thumbnail-strip"] > div')
      if (el === null) throw new Error('the strip is not mounted')
      return el as HTMLElement
    }
    expect(track().style.width).toBe(`${String(PAGE_COUNT * THUMB_SLOT_PX)}px`)

    act(() => {
      resizeViewport(700)
    })
    expect(track().style.width).toBe(`${String(PAGE_COUNT * THUMB_SLOT_TOUCH_PX)}px`)
  })

  it('commits a slider drag on release only', async () => {
    await setup()
    const slider = screen.getByRole('slider', { name: '페이지' })
    fireEvent.mouseDown(slider)
    fireEvent.change(slider, { target: { value: '180' } })
    // Still on 12: a 1 540-page book would otherwise load a page per pixel.
    expect(counter()).toBe('12 / 214')
    expect(useViewerStore.getState().dragPage).toBe(180)

    fireEvent.mouseUp(slider)
    expect(counter()).toBe('180 / 214')
  })

  it('commits immediately for a keyboard change, which has no pointer-down', async () => {
    await setup()
    const slider = screen.getByRole('slider', { name: '페이지' })
    fireEvent.change(slider, { target: { value: '50' } })
    expect(counter()).toBe('50 / 214')
  })
})

// ---------------------------------------------------------------------------
// Unopenable books (impl-plan §4 rule 4 / FR-IDX-010)
// ---------------------------------------------------------------------------

describe('a book that cannot be opened', () => {
  it('says so instead of rendering an empty stage', async () => {
    const recorded = newRecorded()
    server.use(
      ...handlers(
        detailOf({}, { pages: [], page_count: 0, status: 'error', error: 'unexpected EOF' }),
        recorded,
      ),
    )
    stubViewport(1_440)
    stubImage(recorded)
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
    })
    render(
      <QueryClientProvider client={client}>
        <MemoryRouter initialEntries={[`/series/${SERIES_ID}/books/${BOOK_ID}`]}>
          <Routes>
            <Route path="/series/:sid" element={<LocationProbe />} />
            <Route path="/series/:sid/books/:bid" element={<ViewerPage />} />
          </Routes>
        </MemoryRouter>
      </QueryClientProvider>,
    )
    expect(await screen.findByText('열 수 없는 파일')).toBeInTheDocument()
    expect(screen.getByText('unexpected EOF')).toBeInTheDocument()
    expect(document.querySelector('[data-role="stage"]')).toBeNull()
  })
})

// ---------------------------------------------------------------------------
// Ruling E-33 §2 — the series-detail seed reaches the viewer
// ---------------------------------------------------------------------------

/**
 * The extra handlers the *series* screen needs, on top of `handlers()`.
 *
 * `SeriesHeader` reads `useRoots()` for the 원본 경로 and `useCoverImage`, and
 * the page polls `useScanStatus`. Without them MSW fails the **request** rather
 * than the test (`onUnhandledRequest: 'error'` is not a test-level assertion),
 * and the screen renders in a degraded shape that would still let a wrong
 * assertion pass.
 */
function seriesScreenHandlers() {
  return [
    http.get(`${ORIGIN}/api/roots`, () => HttpResponse.json({ items: [root] })),
    http.get(`${ORIGIN}/api/scan/status`, () => HttpResponse.json(scanStatusIdle)),
    http.get(`${ORIGIN}/api/series/:sid/cover`, () =>
      HttpResponse.json(errorEnvelope('not_found', 'no cover'), { status: 404 }),
    ),
  ]
}

/**
 * Renders the **series screen and the viewer in one router**, starting on the
 * series screen.
 *
 * E-33 §2 spells out why the test has to be shaped like this: "스토어를 단언하는
 * 테스트로는 보이지 않는다 — 스토어는 결함이 있을 때도 올바른 값을 담고 있었다."
 * `store/seriesDir.ts` held `rtl` perfectly well throughout the defect; what was
 * missing was a consumer, and the only way to see a missing consumer is to walk
 * the reader's actual path — set the direction on one screen, open a volume, and
 * read the direction off the *other* screen's DOM.
 */
async function setupSeriesThenViewer(prefs: Partial<BookPrefs> = {}): Promise<Recorded> {
  const recorded = newRecorded()
  const detail = detailOf(prefs)
  server.use(...handlers(detail, recorded), ...seriesScreenHandlers())
  stubViewport(1_440)
  stubImage(recorded)

  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[`/series/${SERIES_ID}`]}>
        <Routes>
          <Route path="/series/:sid" element={<SeriesDetailPage />} />
          <Route path="/series/:sid/books/:bid" element={<ViewerPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  )
  await screen.findByRole('heading', { name: seriesDetail.name })
  return recorded
}

/** Opens the first volume from the series screen's grid. */
async function openFirstVolume(): Promise<void> {
  const grid = await screen.findByTestId('volume-grid')
  const tiles = within(grid).getAllByRole('button')
  const tile = tiles[0]
  if (tile === undefined) throw new Error('the series screen listed no openable volume')
  await userEvent.click(tile)
  await screen.findAllByRole('img', { name: /page_/ })
}

describe('the series-detail 읽기 방향 seed (ruling E-33 §2, C-9)', () => {
  it('opens a volume in the direction the series screen was just set to', async () => {
    // `is_override: false` ⇒ every field of `prefs` is the global default, which
    // is what the seed is entitled to replace.
    await setupSeriesThenViewer({ reading_direction: 'ltr', is_override: false })

    const group = screen.getByRole('radiogroup', { name: '읽기 방향' })
    await userEvent.click(within(group).getByRole('radio', { name: 'R→L' }))

    await openFirstVolume()

    // The DOM of the *viewer*, not the seed store: the store was already right
    // while the product was wrong (E-33 §2).
    expect(stage()).toHaveAttribute('data-dir', 'rtl')
    expect(screen.getByRole('radio', { name: 'R→L' })).toBeChecked()
  })

  it('leaves a volume that has its own settings alone — the seed is not an override', async () => {
    // A book the reader has already set to L→R for itself. The seed replaces the
    // *global default*; it does not beat a choice made for this volume.
    await setupSeriesThenViewer({ reading_direction: 'ltr', is_override: true })

    const group = screen.getByRole('radiogroup', { name: '읽기 방향' })
    await userEvent.click(within(group).getByRole('radio', { name: 'R→L' }))

    await openFirstVolume()

    expect(stage()).toHaveAttribute('data-dir', 'ltr')
  })

  it('falls back to the global default when the series has no seed', async () => {
    await setupSeriesThenViewer({ reading_direction: 'rtl', is_override: false })
    // Nothing touched on the series screen: `bySeries` is empty, so the book's
    // effective prefs stand exactly as the server sent them.
    await openFirstVolume()
    expect(stage()).toHaveAttribute('data-dir', 'rtl')
  })
})

// ---------------------------------------------------------------------------
// Ruling E-33 §3 — 이 권 전용 설정: the badge and the reset
// ---------------------------------------------------------------------------

function chip(): HTMLElement | null {
  return document.querySelector('[data-role="viewer-override-chip"]')
}

describe('the per-book override chip (ruling E-33 §3)', () => {
  it('is raised only for a book that actually carries an override', async () => {
    await setup({ prefs: { is_override: true } })
    expect(chip()).toHaveTextContent(OVERRIDE_CHIP_LABEL)
    // 권, not the prototype's 시리즈: the product persists per book (C-9).
    expect(screen.queryByText('이 시리즈 전용 설정')).not.toBeInTheDocument()
  })

  it('is absent when the book is simply on the global defaults', async () => {
    await setup({ prefs: { is_override: false } })
    expect(chip()).toBeNull()
  })

  it('clears the override with three nulls and puts the viewer back on the defaults', async () => {
    // A book overriding all three, every one of them different from the global
    // defaults in `settings` (ltr / single / height) — so nothing below can pass
    // by coincidence.
    const recorded = await setup({
      prefs: {
        reading_direction: 'rtl',
        display_mode: 'spread',
        fit_mode: 'original',
        is_override: true,
      },
    })
    expect(stage()).toHaveAttribute('data-dir', 'rtl')
    expect(stage()).toHaveAttribute('data-mode', 'spread')
    expect(stage()).toHaveAttribute('data-fit', 'original')

    const button = chip()
    if (button === null) throw new Error('the override chip was not raised')
    await userEvent.click(button)

    // The request list, not the store: `null` and "field absent" are different
    // requests to this endpoint and only the recorded body can tell them apart.
    await waitFor(() => {
      expect(recorded.prefsPuts).toHaveLength(1)
    })
    expect(recorded.prefsPuts[0]).toEqual({
      reading_direction: null,
      display_mode: null,
      fit_mode: null,
    })

    // …and the trap E-33 §3 names. `useSetPrefs.onSuccess` writes the query
    // cache; `open()` is guarded by `openedRef` and will not run again. A reset
    // that only invalidated leaves all three of these on the deleted override.
    await waitFor(() => {
      expect(stage()).toHaveAttribute('data-dir', 'ltr')
    })
    expect(stage()).toHaveAttribute('data-mode', 'single')
    expect(stage()).toHaveAttribute('data-fit', 'height')
    expect(useViewerStore.getState().mode).toBe('single')
    expect(useViewerStore.getState().dir).toBe('ltr')
    expect(useViewerStore.getState().fit).toBe('height')

    // The controls the reader looks at agree, and the chip has gone.
    expect(screen.getByRole('radio', { name: 'L→R' })).toBeChecked()
    expect(screen.getByRole('radio', { name: '단면' })).toBeChecked()
    expect(screen.getByRole('radio', { name: '높이' })).toBeChecked()
    expect(chip()).toBeNull()
  })

  it('keeps the reader on their page while it resets', async () => {
    await setup({ prefs: { display_mode: 'spread', is_override: true } })
    fireEvent.keyDown(window, { key: 'ArrowRight' })
    const before = counter()
    const button = chip()
    if (button === null) throw new Error('the override chip was not raised')
    await userEvent.click(button)
    await waitFor(() => {
      expect(useViewerStore.getState().mode).toBe('single')
    })
    expect(counter()).toBe(before)
  })

  it('lands on the series seed rather than the global default, when there is one', async () => {
    // One rule, two call sites: with the override gone, "what this volume shows
    // when nobody chose anything for it" is the seed (E-33 §2) — otherwise 이 권
    // 전용 설정 would clear the override and then disagree with the very next
    // open of the same book.
    useSeriesDirStore.setState({ bySeries: { [SERIES_ID]: 'rtl' } })
    await setup({
      prefs: { reading_direction: 'ltr', display_mode: 'spread', is_override: true },
    })
    const button = chip()
    if (button === null) throw new Error('the override chip was not raised')
    await userEvent.click(button)

    await waitFor(() => {
      expect(useViewerStore.getState().mode).toBe('single')
    })
    expect(stage()).toHaveAttribute('data-dir', 'rtl')
  })
})

// ---------------------------------------------------------------------------
// Ruling E-34 — the 라이브러리 button, and the focus reveal through the virtualiser
// ---------------------------------------------------------------------------

/** 120 series, all under the `scan` root; the one at `TARGET_INDEX` is this book's. */
const LIBRARY_COUNT = 120
/**
 * Inside the first page of 60, so it is in `items` — and outside the 30 cards
 * (five rows of six) the 1 440 window renders, so it is **not in the document**.
 * That gap is the entire point of E-34 §2.
 */
const TARGET_INDEX = 36
const LIBRARY_SCOPE = 'scan'
const LIBRARY_QUERY = '시리즈'

const LIBRARY_ROOTS = [
  root,
  { ...root, name: LIBRARY_SCOPE, label: '03. scan (PDF)', path: '/pds/scan', series_count: 120 },
]

function librarySeries(): SeriesSummary[] {
  return Array.from({ length: LIBRARY_COUNT }, (_, i) => ({
    ...seriesSummary,
    // The viewer's own series has to be the one at TARGET_INDEX, or the reveal
    // has nothing to find.
    id: i === TARGET_INDEX ? SERIES_ID : `libseries${String(i).padStart(6, '0')}`,
    name: `시리즈 ${String(i + 1).padStart(3, '0')}`,
    root_name: LIBRARY_SCOPE,
    // No cover request per card: this file's MSW has no cover route and an
    // unhandled request would fail silently as a degraded render.
    has_cover: false,
    cover_cv: null,
  }))
}

interface LibraryRecorded {
  /** Every `GET /api/series` the library issued, as a query string. */
  seriesQueries: string[]
  /** Every `PUT /api/settings` — the A-5 write-back E-34 §1 must not trigger. */
  settingsPuts: unknown[]
}

function libraryHandlers(recorded: LibraryRecorded, view: 'grid' | 'list' = 'grid') {
  const items = librarySeries()
  return [
    http.get(`${ORIGIN}/api/roots`, () => HttpResponse.json({ items: LIBRARY_ROOTS })),
    http.get(`${ORIGIN}/api/continue`, () => HttpResponse.json({ items: [] })),
    // The server already stores what the sidebar shows (A-5). If it did not,
    // `useLibrarySettingsSync` would hydrate the store back to `all` on arrival
    // and this file would be testing the hydration, not the button.
    http.get(`${ORIGIN}/api/settings`, () =>
      HttpResponse.json({ ...settings, library_scope: LIBRARY_SCOPE, library_view: view }),
    ),
    http.put(`${ORIGIN}/api/settings`, async ({ request }) => {
      recorded.settingsPuts.push(await request.json())
      return HttpResponse.json(settings)
    }),
    http.get(`${ORIGIN}/api/series`, ({ request }) => {
      const url = new URL(request.url)
      recorded.seriesQueries.push(url.search)
      const offset = Number(url.searchParams.get('offset') ?? '0')
      const limit = Number(url.searchParams.get('limit') ?? '60')
      return HttpResponse.json({
        items: items.slice(offset, offset + limit),
        total: items.length,
        offset,
        limit,
      })
    }),
  ]
}

/**
 * jsdom does no layout, so every element measures 0×0 and the virtualiser gets a
 * zero-height window. `SeriesGrid` also derives its column count from
 * `clientWidth`, and `clientWidth` **includes padding** — the `p-4` wrapper
 * reports 32px the grid box does not have.
 */
function stubLibraryLayout(): () => void {
  const rect: DOMRect = {
    x: 0,
    y: 0,
    top: 0,
    left: 0,
    right: 1_154,
    bottom: 900,
    width: 1_154,
    height: 900,
    toJSON: () => ({}),
  }
  vi.spyOn(Element.prototype, 'getBoundingClientRect').mockReturnValue(rect)

  const width = Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'clientWidth')
  Object.defineProperty(HTMLElement.prototype, 'clientWidth', {
    configurable: true,
    get(this: HTMLElement): number {
      return 1_154 + (this.classList.contains('p-4') ? 32 : 0)
    },
  })

  // `virtual-core`'s `getOffsetForAlignment` clamps every scroll to
  // `scrollElement.scrollHeight - size`, and jsdom reports `scrollHeight === 0`
  // for everything — so the clamp is `-900` and **every** scroll resolves to 0.
  // A test without this cannot fail: the reveal and a no-op both leave the
  // scroller at the top. The virtualiser sizes exactly one child to the whole
  // content height, so that inline height is the honest answer.
  const scrollHeight = Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'scrollHeight')
  Object.defineProperty(HTMLElement.prototype, 'scrollHeight', {
    configurable: true,
    get(this: HTMLElement): number {
      const sizer = this.querySelector<HTMLElement>('div[style*="height:"]')
      const px = sizer === null ? 0 : Number.parseFloat(sizer.style.height)
      return Number.isFinite(px) ? px : 0
    },
  })

  // jsdom has **no** `Element.prototype.scrollTo` at all, and `virtual-core`'s
  // `elementScroll` is an optional call — so without this the scroll silently
  // does nothing and the only observable left would be "`scrollToIndex` was
  // called", one step short of the claim. This is the browser's half: move the
  // scroller, then fire the `scroll` event `observeElementOffset` subscribes to.
  const scrollTo = Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'scrollTo')
  Object.defineProperty(HTMLElement.prototype, 'scrollTo', {
    configurable: true,
    writable: true,
    value(this: HTMLElement, options: ScrollToOptions) {
      this.scrollTop = Math.round(options.top ?? 0)
      this.dispatchEvent(new Event('scroll'))
    },
  })

  return () => {
    if (width === undefined) Reflect.deleteProperty(HTMLElement.prototype, 'clientWidth')
    else Object.defineProperty(HTMLElement.prototype, 'clientWidth', width)
    if (scrollHeight === undefined) Reflect.deleteProperty(HTMLElement.prototype, 'scrollHeight')
    else Object.defineProperty(HTMLElement.prototype, 'scrollHeight', scrollHeight)
    if (scrollTo === undefined) Reflect.deleteProperty(HTMLElement.prototype, 'scrollTo')
    else Object.defineProperty(HTMLElement.prototype, 'scrollTo', scrollTo)
  }
}

/** The `translateY` a virtualised row is positioned at, in px. */
function rowOffset(row: HTMLElement | null): number {
  const m = /translateY\((-?[\d.]+)px\)/.exec(row?.style.transform ?? '')
  return m?.[1] === undefined ? Number.NaN : Number.parseFloat(m[1])
}

interface LibrarySetup {
  recorded: Recorded
  library: LibraryRecorded
}

/** The viewer and the real library screen in one router, starting in the viewer. */
async function setupViewerThenLibrary(
  options: { scope?: string; query?: string; view?: 'grid' | 'list' } = {},
): Promise<LibrarySetup> {
  const recorded = newRecorded()
  const library: LibraryRecorded = { seriesQueries: [], settingsPuts: [] }
  // The library's handlers go first: MSW resolves in registration order, and
  // `handlers()` carries a `GET /api/settings` whose `library_scope` is `all`.
  // Left in front it would hydrate the store back to `all` on arrival (A-5, the
  // server wins), and this file would be testing the hydration rather than the
  // button.
  server.use(...libraryHandlers(library, options.view ?? 'grid'), ...handlers(detailOf(), recorded))
  stubViewport(1_440)
  stubImage(recorded)
  useUiStore.setState({
    scope: options.scope ?? LIBRARY_SCOPE,
    query: options.query ?? LIBRARY_QUERY,
    view: options.view ?? 'grid',
  })

  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[`/series/${SERIES_ID}/books/${BOOK_ID}?page=12`]}>
        <Routes>
          <Route path="/" element={<LibraryPage />} />
          <Route path="/series/:sid" element={<LocationProbe />} />
          <Route path="/series/:sid/books/:bid" element={<ViewerPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  )
  await screen.findAllByRole('img', { name: /page_/ })
  return { recorded, library }
}

async function pressLibrary(): Promise<void> {
  act(() => {
    useViewerStore.getState().wake()
  })
  await userEvent.click(screen.getByRole('button', { name: '라이브러리' }))
  await screen.findByTestId('library-scroller')
}

interface FilterWatch {
  /** Every value `scope` and `query` *changed to* since the watch began. */
  moves: () => string[]
  stop: () => void
}

/**
 * The store's **transition list** for the sidebar filter and the search box —
 * the only shape in which "the button did not touch them" can be asserted here.
 *
 * E-34 §1 rules on `scope` *and* `q`, and the three obvious observables can only
 * see the `q` half:
 *
 *  * **The wire.** `useLibrarySettingsSync` runs its hydration effect on the
 *    library's mount, and `GET /api/settings` is already in the cache because
 *    the viewer asked for it — so `library_scope` is put back **before** the
 *    first `GET /api/series` is ever issued. `root=` on the wire is the server's
 *    value under either behaviour. `q` is not a settings field, so nothing
 *    repairs it and that half stays honest.
 *  * **`PUT /api/settings`.** The repair leaves the store equal to the payload,
 *    so the write-back's `snapshot === serverKey` guard closes and no request is
 *    sent. A cleared scope produces no `PUT` at all.
 *  * **`useUiStore.getState().scope` afterwards.** Same repair, one step later.
 *
 * All three read the value **after** it settles, and the value settles back to
 * the truth. What no repair can undo is that it *moved*: `store/ui.ts` is behind
 * zustand's `persist`, which writes `shelf.ui` on every `set`, so a scope that
 * went to `all` and back was written to `localStorage` as `all` in between — a
 * reload, a second tab, or a settings request that never answers in that window
 * keeps it. And it is the plain reading of the ruling: 건드리지 않는다. A value
 * changed and changed back was touched.
 *
 * Same reasoning as `watchChrome` above, one layer down: a list has nothing to
 * settle into, and `[]` means nothing happened.
 */
function watchFilters(): FilterWatch {
  const seen: string[] = []
  const stop = useUiStore.subscribe((state, prev) => {
    if (state.scope !== prev.scope) seen.push(`scope=${state.scope}`)
    if (state.query !== prev.query) seen.push(`q=${state.query}`)
  })
  return { moves: () => [...seen], stop }
}

describe('the 라이브러리 button (ruling E-34)', () => {
  let restoreLayout: () => void = () => undefined

  beforeEach(() => {
    restoreLayout = stubLibraryLayout()
  })
  afterEach(() => {
    restoreLayout()
  })

  it('reaches the library without clearing the scope or the search (E-34 §1)', async () => {
    const { library } = await setupViewerThenLibrary()
    const watch = watchFilters()
    try {
      await pressLibrary()

      // The whole of the ruling, and the only assertion here that can see the
      // `scope` half: neither value moved at any point between the press and the
      // library settling. Everything below is the `q` half and the corroboration
      // — see `watchFilters` for why they cannot stand in for this line.
      expect(
        watch.moves(),
        'E-34 §1: 라이브러리 touches neither the sidebar filter nor the search box — a value that went to `all` and was hydrated back was still touched',
      ).toEqual([])

      // The *request list*. `q` is not a settings field, so nothing on arrival
      // repairs it: a cleared search is visible on the wire, and this is where
      // it shows.
      await waitFor(() => {
        expect(library.seriesQueries.length).toBeGreaterThan(0)
      })
      const last = library.seriesQueries.at(-1) ?? ''
      expect(new URLSearchParams(last).getAll('root')).toEqual([LIBRARY_SCOPE])
      expect(new URLSearchParams(last).get('q')).toBe(LIBRARY_QUERY)
      expect(library.settingsPuts).toEqual([])
      expect(useUiStore.getState().scope).toBe(LIBRARY_SCOPE)
      expect(useUiStore.getState().query).toBe(LIBRARY_QUERY)
      expect(
        watch.moves(),
        'nothing moved them on the way to the first request either',
      ).toEqual([])
    } finally {
      watch.stop()
    }
  })

  it('scrolls the virtualiser to a card outside the window and focuses it (E-34 §2)', async () => {
    await setupViewerThenLibrary()
    await pressLibrary()

    const target = librarySeries()[TARGET_INDEX]
    if (target === undefined) throw new Error('no target series')
    expect(target.id).toBe(SERIES_ID)

    // The premise, asserted rather than assumed: at this width the grid renders
    // about five rows of six, so the prototype's `document.getElementById` has
    // nothing to find here and would fail silently.
    const scroller = screen.getByTestId('library-scroller')
    expect(scroller.querySelectorAll('[data-index]').length).toBeGreaterThan(0)

    await waitFor(() => {
      expect(document.getElementById(seriesCardDomId(SERIES_ID))).not.toBeNull()
    })
    const card = document.getElementById(seriesCardDomId(SERIES_ID))
    await waitFor(() => {
      expect(document.activeElement).toBe(card)
    })
    // …and it is marked, which is the other half of the ruling.
    expect(card).toHaveAttribute('data-revealed', 'true')

    // **Where** it landed, not merely that something moved. `align: 'start'`
    // means the card's own row is flush with the top of the scroller — which is
    // also what rules out the prototype's 96px offset, and what catches a
    // reveal that ran against an unmeasured grid and scrolled to the wrong row
    // (the card can still end up on screen there, several hundred pixels off).
    const rowBox = card?.closest('[data-index]') as HTMLElement | null
    expect(rowBox?.dataset.index).toBe(String(Math.floor(TARGET_INDEX / 6)))
    expect(rowOffset(rowBox)).toBeCloseTo(scroller.scrollTop, 0)
    expect(scroller.scrollTop).toBeGreaterThan(0)

    // One-shot: the instruction is consumed, or every later mount of the library
    // would steal the focus again.
    expect(useUiStore.getState().revealSeries).toBeNull()
  })

  it('marks exactly one card', async () => {
    await setupViewerThenLibrary()
    await pressLibrary()
    await waitFor(() => {
      expect(document.querySelectorAll('[data-revealed="true"]')).toHaveLength(1)
    })
  })

  it('reveals the row through the list virtualiser too', async () => {
    // `library_view` is server-held too (A-5), so the payload has to agree or
    // the hydration puts the screen straight back into the grid.
    await setupViewerThenLibrary({ view: 'list' })
    await pressLibrary()

    await waitFor(() => {
      expect(document.getElementById(seriesRowDomId(SERIES_ID))).not.toBeNull()
    })
    const row = document.getElementById(seriesRowDomId(SERIES_ID))
    await waitFor(() => {
      expect(document.activeElement).toBe(row)
    })
    expect(row).toHaveAttribute('data-revealed', 'true')
    const scroller = screen.getByTestId('library-scroller')
    expect(rowOffset(row?.closest('[data-index]') as HTMLElement | null)).toBeCloseTo(
      scroller.scrollTop,
      0,
    )
    expect(scroller.scrollTop).toBeGreaterThan(0)
  })

  it('stays armed, and steals no focus, while the series is not in the loaded pages', async () => {
    // `scope` narrowed to a root the target is not under: the series is simply
    // not in `items`, and no amount of paging would bring it in — which is
    // exactly why chasing pages is the wrong answer (E-34 §1 forbids clearing
    // the scope to widen the search).
    const { library } = await setupViewerThenLibrary({ scope: 'mangga' })
    server.use(
      http.get(`${ORIGIN}/api/series`, ({ request }) => {
        library.seriesQueries.push(new URL(request.url).search)
        return HttpResponse.json({ items: [], total: 0, offset: 0, limit: 60 })
      }),
    )
    await act(async () => {
      useViewerStore.getState().wake()
      await Promise.resolve()
    })
    await userEvent.click(screen.getByRole('button', { name: '라이브러리' }))

    await screen.findByText('검색 결과 없음')
    expect(document.activeElement).toBe(document.body)
    // Still armed: the reveal has not happened, so the instruction must survive
    // for the page that finally carries the series.
    expect(useUiStore.getState().revealSeries).toBe(SERIES_ID)
  })
})
