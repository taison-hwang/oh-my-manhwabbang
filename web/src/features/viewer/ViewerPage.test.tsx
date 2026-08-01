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
  seriesDetail,
  settings,
} from '../../api/fixtures'
import type { BookDetail, BookPrefs, PageInfo, Progress } from '../../api/types'
import { resetBasePath } from '../../api/urls'
import { useUiStore } from '../../store/ui'
import { CHROME_AUTOHIDE_MS, cancelChromeAutoHide, useViewerStore } from '../../store/viewer'
import { POINTER_IDLE_MS, ViewerPage } from './ViewerPage'
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

interface Recorded {
  progressPuts: { page: number; completed?: boolean }[]
  prefsPuts: unknown[]
  /** Every URL handed to `new Image()` by the prefetcher. */
  prefetched: string[]
}

function newRecorded(): Recorded {
  return { progressPuts: [], prefsPuts: [], prefetched: [] }
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
    http.put(`${ORIGIN}/api/books/:bid/progress`, async ({ request }) => {
      recorded.progressPuts.push((await request.json()) as { page: number; completed?: boolean })
      return HttpResponse.json(progressOf())
    }),
    http.put(`${ORIGIN}/api/books/:bid/prefs`, async ({ request }) => {
      const body = await request.json()
      recorded.prefsPuts.push(body)
      return HttpResponse.json({ ...detail.prefs, ...(body as object) })
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

async function setup(options: SetupOptions = {}): Promise<Recorded> {
  const recorded = newRecorded()
  const detail = detailOf(options.prefs, options.detail)
  server.use(...handlers(detail, recorded, options.prefetch ?? 4))
  stubViewport(options.width ?? 1_440)
  stubImage(recorded)

  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  render(
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
  await screen.findAllByRole('img', { name: /page_/ })
  return recorded
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
})
afterAll(() => {
  server.close()
})

beforeEach(() => {
  localStorage.clear()
  useUiStore.setState({ theme: 'light', overlays: [] })
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

  it('opens a book stored at 화면 on 높이 instead — E-27 took its control away', async () => {
    // The `contain` *geometry* is still real and still tested, in `fit.test.ts`;
    // what changed is that nothing routes a reader to it any more. Landing on a
    // fit with no button would leave them unable to see which one they are on.
    await setup({ prefs: { fit_mode: 'contain' } })
    expect(stage()).toHaveAttribute('data-fit', 'height')
    expect(screen.queryByRole('radio', { name: '화면' })).toBeNull()
    // …and the three that remain are all there.
    for (const label of ['너비', '높이', '원본']) {
      expect(screen.getByRole('radio', { name: label })).toBeInTheDocument()
    }
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

  it('warns once when the recorded progress no longer matches the file', async () => {
    await setup({ detail: { progress: progressOf({ stale: true }) } })
    expect(screen.getByText('파일이 변경되었습니다')).toBeInTheDocument()
  })

  it('does not warn when the progress is current', async () => {
    await setup()
    expect(screen.queryByText('파일이 변경되었습니다')).not.toBeInTheDocument()
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
