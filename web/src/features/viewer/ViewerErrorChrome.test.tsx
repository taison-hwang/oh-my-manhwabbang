import '@testing-library/jest-dom/vitest'

import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { setupServer } from 'msw/node'
import type { ComponentProps } from 'react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'

import {
  BOOK_ID,
  ORIGIN,
  SERIES_ID,
  bookDetail,
  bookSummary,
  errorEnvelope,
  progress,
  seriesDetail,
  settings,
} from '../../api/fixtures'
import type { BookDetail, PageInfo } from '../../api/types'
import { resetBasePath } from '../../api/urls'
import { CHROME_AUTOHIDE_MS, cancelChromeAutoHide, useViewerStore } from '../../store/viewer'
import { failUnhandled } from '../../test/msw'
import { resetPrefsWriter } from './prefsWriter'
import { ViewerBottomBar } from './ViewerBottomBar'
import { ViewerPage } from './ViewerPage'

/**
 * **The bottom bar on the viewer's error screens claims no page count.**
 *
 * With no book open the store holds `page: 1, pageCount: 0`, and the bar
 * printed that as `1 / 0` — under 책을 열 수 없습니다 (the detail `GET` failed),
 * under 열 수 없는 파일 (the detail came back with `pages: []`), and on the
 * empty reading ground of a cold load. The slider said the same thing to a
 * screen reader through `aria-valuetext`.
 *
 * ui-spec §6.7 says nothing about the bar in an error state, so the rule is the
 * one the quiet counter already follows (ui-spec §6.1): the page display is
 * suppressed wherever it would lie. The bar itself stays — 뒤로's twin at the
 * bottom, 썸네일 · T — and only what it says about pages goes.
 *
 * These render the whole `ViewerPage` against MSW rather than the bar on its
 * own, because the lie lived in the *seam*: the bar was correct for every
 * `pageCount` a test ever handed it, and nothing handed it the 0 a real error
 * screen does.
 */

const PAGE_COUNT = 12
const PAGES: PageInfo[] = Array.from({ length: PAGE_COUNT }, (_, i) => ({
  n: i + 1,
  name: `page_${String(i + 1).padStart(3, '0')}.jpg`,
  ext: '.jpg',
  size: 180_000,
  w: 800,
  h: 1_200,
}))

const readable: BookDetail = {
  ...bookDetail,
  page_count: PAGE_COUNT,
  pages: PAGES,
  dims_state: 'done',
  next_book_id: null,
  progress: null,
}

/** FR-IDX-010 as the viewer receives it: still a 200, `pages: []`, `error` populated. */
const unopenable: BookDetail = {
  ...bookDetail,
  page_count: 0,
  pages: [],
  dims_state: 'none',
  status: 'error',
  error: 'unexpected EOF',
  next_book_id: null,
  progress: null,
}

/** "N / 0" in any spelling: a page count of zero, printed as a denominator. */
const ZERO_DENOMINATOR = /\d\s*\/\s*0(?![\d,])/

/**
 * Everything but the book itself. Kept on the server across `resetHandlers`, so
 * a progress write flushed on unmount lands somewhere instead of failing the
 * file as an unhandled request (`src/test/msw.ts`).
 */
const server = setupServer(
  http.get(`${ORIGIN}/api/series/:sid`, () =>
    HttpResponse.json({
      ...seriesDetail,
      books: [{ ...bookSummary, id: BOOK_ID, page_count: PAGE_COUNT, progress: null }],
    }),
  ),
  http.get(`${ORIGIN}/api/settings`, () => HttpResponse.json(settings)),
  http.put(`${ORIGIN}/api/books/:bid/progress`, () =>
    HttpResponse.json({ ...progress, book_id: BOOK_ID, page_count: PAGE_COUNT }),
  ),
  http.put(`${ORIGIN}/api/books/:bid/prefs`, () => HttpResponse.json(bookDetail.prefs)),
  http.get(`${ORIGIN}/api/books/:bid/thumbs/:n`, () =>
    HttpResponse.json(errorEnvelope('thumb_unavailable', 'not generated'), { status: 422 }),
  ),
)

function serveBook(respond: () => Response | Promise<Response>): void {
  server.use(http.get(`${ORIGIN}/api/books/:bid`, respond))
}

function mountViewer(search = ''): void {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[`/series/${SERIES_ID}/books/${BOOK_ID}${search}`]}>
        <Routes>
          <Route path="/series/:sid/books/:bid" element={<ViewerPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

/** The bar, asserted *up*: a hidden bar is `inert` and not what anyone reads. */
function visibleBottomBar(): HTMLElement {
  const bar = document.querySelector<HTMLElement>('[data-role="viewer-bottom-bar"]')
  if (bar === null) throw new Error('the viewer rendered no bottom bar')
  expect(bar, 'the chrome is up, so this is the bar a reader sees').toHaveAttribute(
    'data-visible',
    'true',
  )
  return bar
}

/** The way a reader brings the bar back over a chromeless ground (E-27). */
function summonChromeFromTheEdge(): void {
  const edge = document.querySelector('[data-role="viewer-edge-bottom"]')
  if (edge === null) throw new Error('no screen edge to summon the chrome from')
  fireEvent.click(edge)
}

interface ErrorScreen {
  name: string
  /** Mounts the viewer and returns once that screen is showing with the chrome up. */
  enter: () => Promise<void>
  /** Lets go of anything `enter` left in flight. */
  leave?: () => Promise<void>
}

let releaseBook: (() => void) | null = null

/** The cold load, named: one row below starts here and lets the detail land. */
const COLD_LOAD: ErrorScreen = {
  name: 'the detail is still on the wire (cold load)',
  enter: async () => {
    let onWire = (): void => undefined
    const arrived = new Promise<void>((resolve) => {
      onWire = resolve
    })
    const gate = new Promise<void>((resolve) => {
      releaseBook = resolve
    })
    serveBook(async () => {
      onWire()
      await gate
      return HttpResponse.json(readable)
    })
    mountViewer()
    await arrived
  },
  leave: async () => {
    releaseBook?.()
    // The held request is answered under this test, not after it.
    await waitFor(() => {
      expect(useViewerStore.getState().pageCount).toBe(PAGE_COUNT)
    })
  },
}

const ERROR_SCREENS: ErrorScreen[] = [
  {
    name: 'the book cannot be fetched (404 → 책을 열 수 없습니다)',
    enter: async () => {
      serveBook(() =>
        HttpResponse.json(errorEnvelope('not_found', 'book not found'), { status: 404 }),
      )
      mountViewer()
      // `open()` never runs, so the store is the closed one: chrome up, `pageCount: 0`.
      await screen.findByRole('heading', { level: 2, name: '책을 열 수 없습니다' })
    },
  },
  {
    name: 'the book has no pages (pages: [] → 열 수 없는 파일)',
    enter: async () => {
      serveBook(() => HttpResponse.json(unopenable))
      mountViewer()
      await screen.findByRole('heading', { level: 2, name: '열 수 없는 파일' })
      // `open()` did run, with `pageCount: 0`, and opened chromeless like any book.
      summonChromeFromTheEdge()
    },
  },
  COLD_LOAD,
]

beforeAll(() => {
  server.listen({ onUnhandledRequest: failUnhandled })
})
afterEach(() => {
  releaseBook?.()
  releaseBook = null
  server.resetHandlers()
  resetBasePath()
  cancelChromeAutoHide()
  resetPrefsWriter()
  vi.unstubAllGlobals()
})
afterAll(() => {
  server.close()
})
beforeEach(() => {
  localStorage.clear()
  useViewerStore.getState().close()
  Object.defineProperty(window, 'matchMedia', {
    writable: true,
    configurable: true,
    value: (query: string) => ({
      matches: query.includes('min-width'),
      media: query,
      addEventListener: () => undefined,
      removeEventListener: () => undefined,
    }),
  })
  // jsdom does not fetch `img.src`; the prefetcher only needs something to point.
  vi.stubGlobal(
    'Image',
    class {
      src = ''
      addEventListener(): void {
        /* prefetcher only */
      }
    },
  )
})

describe('the bottom bar on an error screen claims no page count (ui-spec §6.1 rule, §6.7)', () => {
  it.each(ERROR_SCREENS)('$name: no "N / 0" counter, and the bar is kept', async (s) => {
    await s.enter()
    const bar = visibleBottomBar()

    expect(document.body).not.toHaveTextContent(ZERO_DENOMINATOR)
    // The counter's box is kept, empty, so the slider and 썸네일 · T do not
    // shift sideways between an error screen and a book.
    expect(bar.querySelector('[data-role="page-counter"]')).toBeEmptyDOMElement()
    // Only the page display goes: the bar's own control is still there.
    expect(within(bar).getByRole('button', { name: /썸네일/ })).toBeInTheDocument()

    await s.leave?.()
  })

  it.each(ERROR_SCREENS)(
    '$name: the slider announces no "1 / 0" and offers nothing to seek',
    async (s) => {
      await s.enter()
      const slider = within(visibleBottomBar()).getByRole('slider', { name: '페이지' })

      expect(slider.getAttribute('aria-valuetext') ?? '').not.toMatch(ZERO_DENOMINATOR)
      // Disabled: out of the Tab order and reported unavailable. (Not, on its
      // own, out of reach of a touch — see the press row below.)
      expect(slider).toBeDisabled()
      // …and not dressed as operable: `cursor-pointer` would outrank base.css's
      // `:disabled { cursor: not-allowed }`.
      expect(slider).not.toHaveClass('cursor-pointer')

      await s.leave?.()
    },
  )

  /**
   * The control: a book with pages still counts and still announces. Without it
   * every assertion above is satisfied by a bar that never prints a count at all.
   */
  it('a book with pages still shows "1 / 12" and the slider still announces it', async () => {
    serveBook(() => HttpResponse.json(readable))
    mountViewer('?page=1')
    await waitFor(() => {
      expect(useViewerStore.getState().pageCount).toBe(PAGE_COUNT)
    })
    summonChromeFromTheEdge()
    const bar = visibleBottomBar()

    expect(bar.querySelector('[data-role="page-counter"]')).toHaveTextContent(/^1 \/ 12$/)
    const slider = within(bar).getByRole('slider', { name: '페이지' })
    expect(slider).toHaveAttribute('aria-valuetext', '1 / 12')
    expect(slider).toBeEnabled()
    expect(slider).toHaveClass('cursor-pointer')
  })

  /**
   * `disabled` never sticks: the same input that sat disabled on the cold
   * load's empty ground is the one the book arrives into. The control above
   * only ever saw a book that had its pages from the first render, so a slider
   * that latched "no pages" would pass it.
   */
  it('the cold-load screen, once its detail lands, counts "1 / 12" and the slider is live again', async () => {
    await COLD_LOAD.enter()
    expect(
      within(visibleBottomBar()).getByRole('slider', { name: '페이지' }),
      'the row starts page-less, or it proves nothing',
    ).toBeDisabled()

    await COLD_LOAD.leave?.()
    // `open()` brings a book up chromeless (E-27).
    summonChromeFromTheEdge()
    const bar = visibleBottomBar()

    expect(bar.querySelector('[data-role="page-counter"]')).toHaveTextContent(/^1 \/ 12$/)
    const slider = within(bar).getByRole('slider', { name: '페이지' })
    expect(slider).toBeEnabled()
    expect(slider).toHaveAttribute('aria-valuetext', '1 / 12')
    expect(slider).toHaveClass('cursor-pointer')
  })

  /**
   * React 18 withholds only *mouse* handlers from a disabled element
   * (react-dom's `shouldPreventMouseEvent`); a touch still reaches
   * `onTouchStart`. `toBeDisabled()` above cannot see that, so this presses.
   * The touch is held — no `touchEnd` — because a held finger is exactly when
   * the preview shows.
   */
  it.each(ERROR_SCREENS)(
    '$name: a press on the slider starts no drag and raises no preview',
    async (s) => {
      await s.enter()
      const slider = within(visibleBottomBar()).getByRole('slider', { name: '페이지' })

      fireEvent.touchStart(slider)
      expect(useViewerStore.getState(), 'a touch on the slider').toMatchObject({
        dragging: false,
        dragPage: null,
      })
      expect(document.querySelector('[data-role="slider-preview"]'), 'after a touch').toBeNull()

      fireEvent.mouseDown(slider)
      expect(useViewerStore.getState(), 'a mouse press on the slider').toMatchObject({
        dragging: false,
        dragPage: null,
      })
      expect(
        document.querySelector('[data-role="slider-preview"]'),
        'after a mouse press',
      ).toBeNull()

      await s.leave?.()
    },
  )
})

/**
 * The bar on its own, for the two things the viewer cannot hand it: a drag that
 * outlives its pages (`open()` and `close()` both clear the drag, so only a
 * direct prop reaches this), and the moment the pages go while the slider holds
 * the keyboard focus.
 */
describe('the bottom bar when its pages go', () => {
  type BarProps = ComponentProps<typeof ViewerBottomBar>
  const barProps = (over: Partial<BarProps> = {}): BarProps => ({
    visible: true,
    bookId: BOOK_ID,
    cv: null,
    page: 1,
    pageCount: PAGE_COUNT,
    dir: 'ltr',
    stripOpen: false,
    dragging: false,
    dragPage: null,
    onToggleStrip: vi.fn(),
    onDragStart: vi.fn(),
    onDrag: vi.fn(),
    onCommit: vi.fn(),
    onJump: vi.fn(),
    ...over,
  })

  function renderBar(props: BarProps) {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    const ui = (p: BarProps) => (
      <QueryClientProvider client={client}>
        <button type="button">elsewhere</button>
        <ViewerBottomBar {...p} />
      </QueryClientProvider>
    )
    const view = render(ui(props))
    return {
      rerender: (p: BarProps) => {
        view.rerender(ui(p))
      },
    }
  }

  const slider = (): HTMLElement => screen.getByRole('slider', { name: '페이지' })
  const thumbsButton = (): HTMLElement => screen.getByRole('button', { name: /썸네일/ })

  it('a drag the bar is still handed draws no preview, and commits nothing, over a book with no pages', () => {
    const onCommit = vi.fn()
    const { rerender } = renderBar(barProps({ dragging: true, dragPage: 1, onCommit }))
    expect(
      document.querySelector('[data-role="slider-preview"]'),
      'control: with pages, the same drag does draw its preview',
    ).not.toBeNull()

    rerender(barProps({ pageCount: 0, dragging: true, dragPage: 1, onCommit }))
    expect(document.querySelector('[data-role="slider-preview"]')).toBeNull()
    // …and letting go of it seeks nowhere: there is no page 1 to go to.
    fireEvent.touchEnd(slider())
    expect(onCommit).not.toHaveBeenCalled()
  })

  it('the pages going under a focused slider hand the focus to 썸네일 · T', () => {
    const { rerender } = renderBar(barProps())
    slider().focus()
    expect(document.activeElement).toBe(slider())

    rerender(barProps({ pageCount: 0 }))
    expect(slider()).toBeDisabled()
    expect(document.activeElement).toBe(thumbsButton())
  })

  it('the pages going while the focus is elsewhere move nothing', () => {
    const { rerender } = renderBar(barProps())
    const elsewhere = screen.getByRole('button', { name: 'elsewhere' })
    elsewhere.focus()

    rerender(barProps({ pageCount: 0 }))
    expect(document.activeElement).toBe(elsewhere)
  })

  it('a bar going to sleep in the same commit keeps out of it: the root landing owns that focus', () => {
    const { rerender } = renderBar(barProps())
    slider().focus()

    rerender(barProps({ pageCount: 0, visible: false }))
    // The bar is inert now; `ViewerPage`'s landing (E-62 §2.5) puts this focus
    // on the viewer root, and 썸네일 · T is no place for it.
    expect(document.activeElement).not.toBe(thumbsButton())
  })
})

/**
 * The handoff above, through the one path that reaches it on screen (codex
 * cycle 2): 다음 권 to a volume whose detail is `pages: []`.
 *
 * The bar-alone rows hand the bar `pageCount: 0` by prop. In the viewer the 0
 * arrives with the focus still in the bar only one way: `open()` taking the
 * next volume as a continuation — no `close()` between the volumes, so the bar
 * is never re-mounted, the chrome stays up, and the slider the keyboard is on
 * goes from live (the store still holds volume 1's twelve pages while volume 2
 * is on the wire) to disabled in one commit. Two other rules meet there:
 * E-62's keyboard hold, asked at each auto-hide deadline ("was the last input a
 * key that was not a page turn, and is the focus in a bar?" — ui-spec §8.2),
 * and `ViewerPage`'s landing, which puts a bar's focus on the viewer root when
 * the chrome hides from under it (E-62 §2.5).
 *
 * On a fake clock, because the hold is only visible at a deadline. Testing
 * Library's `waitFor` cannot poll under vitest's fake timers (its fake-timer
 * support is jest-only — see `tickUntil` in ViewerPage.test.tsx), so the clock
 * is stepped by hand.
 */
describe('다음 권 with no pages, landing under a slider the keyboard holds (E-62 × §6.7)', () => {
  const NEXT_BOOK_ID = 'nextbook33333333'
  const volumeOne: BookDetail = { ...readable, next_book_id: NEXT_BOOK_ID }
  const volumeTwo: BookDetail = {
    ...unopenable,
    id: NEXT_BOOK_ID,
    name: '군계(軍鷄) 02권.zip',
    prev_book_id: BOOK_ID,
    next_book_id: null,
  }

  /** A key pressed where the focus is now, which is where a browser dispatches it. */
  const press = (key: string): void => {
    fireEvent.keyDown(document.activeElement ?? document.body, { key })
  }

  /** Step the fake clock until `ready()`, yielding so MSW and React Query can run. */
  async function tickUntil(ready: () => boolean, what: string): Promise<void> {
    for (let spent = 0; spent <= 400; spent += 5) {
      if (ready()) return
      await act(async () => {
        await vi.advanceTimersByTimeAsync(5)
      })
    }
    if (!ready()) throw new Error(`never settled inside the fake-clock budget: ${what}`)
  }

  it('hands the focus to 썸네일 · T, keeps the chrome held by it, and H lands it on the viewer root', async () => {
    let volumeTwoOnWire = false
    const volumeTwoGate = new Promise<void>((resolve) => {
      releaseBook = resolve
    })
    server.use(
      http.get(`${ORIGIN}/api/series/:sid`, () =>
        HttpResponse.json({
          ...seriesDetail,
          books: [
            { ...bookSummary, id: BOOK_ID, page_count: PAGE_COUNT, progress: null },
            {
              ...bookSummary,
              id: NEXT_BOOK_ID,
              name: volumeTwo.name,
              ord: 1,
              page_count: 0,
              status: 'error',
              error: volumeTwo.error,
              progress: null,
            },
          ],
        }),
      ),
      http.get(`${ORIGIN}/api/books/:bid`, async ({ params }) => {
        if (params.bid === BOOK_ID) return HttpResponse.json(volumeOne)
        volumeTwoOnWire = true
        await volumeTwoGate
        return HttpResponse.json(volumeTwo)
      }),
    )

    vi.useFakeTimers()
    // Every change of book or page count, in order: a `close()` between the
    // volumes would show as `closed/0`.
    const storeLog: string[] = []
    // Every change of the chrome, in order — a transition log, so "it hid and
    // came back" cannot pass for "it stayed up" (E-31).
    const chromeLog: string[] = []
    let unsubscribe = (): void => undefined
    try {
      // ── 1. A readable volume 1 at its last page; the chrome woken by `H`. ──
      mountViewer('?page=12')
      await tickUntil(
        () => screen.queryByRole('button', { name: '다음 권 읽기' }) !== null,
        'volume 1 open at its end, with the next-volume card up',
      )
      press('h')
      const bar = visibleBottomBar()
      const slider = within(bar).getByRole('slider', { name: '페이지' })
      const thumbs = within(bar).getByRole('button', { name: '썸네일 · T' })
      unsubscribe = useViewerStore.subscribe((s, p) => {
        if (s.bookId !== p.bookId || s.pageCount !== p.pageCount) {
          storeLog.push(`${s.bookId ?? 'closed'}/${String(s.pageCount)}`)
        }
        if (s.chromeVisible !== p.chromeVisible) chromeLog.push(s.chromeVisible ? 'up' : 'down')
      })

      // ── 2. 다음 권 읽기, pressed as a pointer presses it; then, while volume 2
      //       is on the wire, the keyboard Tabs onto the slider. That window is
      //       the only time the slider can hold the focus when the detail lands:
      //       pressing the card moves the focus to the card. ──
      const next = screen.getByRole('button', { name: '다음 권 읽기' })
      const down = new MouseEvent('pointerdown', { bubbles: true, cancelable: true })
      Object.defineProperty(down, 'pointerType', { value: 'mouse' })
      fireEvent(next, down)
      act(() => {
        next.focus()
      })
      fireEvent.click(next)
      await tickUntil(() => volumeTwoOnWire, 'volume 2 requested')
      press('Tab')
      act(() => {
        slider.focus()
      })

      // The premise: no `close()` has run, so the store still holds volume 1 and
      // the slider the keyboard is on is live.
      expect(useViewerStore.getState(), 'volume 2 is still on the wire').toMatchObject({
        bookId: BOOK_ID,
        pageCount: PAGE_COUNT,
      })
      expect(slider, 'the slider is live while volume 1 is still in the store').toBeEnabled()
      expect(document.activeElement, 'the keyboard is on the slider').toBe(slider)

      // ── 3. Volume 2's detail lands: `pages: []`. ──
      releaseBook?.()
      await tickUntil(
        () => useViewerStore.getState().bookId === NEXT_BOOK_ID,
        'volume 2 opened',
      )

      expect(
        storeLog,
        'volume 1 → volume 2 with no close() between: 12 pages until the detail landed, then 0',
      ).toEqual([`${NEXT_BOOK_ID}/0`])
      expect(screen.getByRole('heading', { level: 2, name: '열 수 없는 파일' })).toBeInTheDocument()
      expect(visibleBottomBar(), 'the same bar, never re-mounted').toBe(bar)
      expect(slider).toBeDisabled()
      // Exactly the button, and this is the assertion that tells the handoff
      // apart here. jsdom 26 does not run the focus fixup a browser runs on a
      // control that becomes disabled: without the handoff `activeElement`
      // stays on the disabled slider (still inside the bar, so the hold below
      // would still be satisfied), where a browser drops it to `body`. jsdom's
      // `blur()` is a no-op on a control that is no longer focusable, so the
      // browser's loss cannot be reproduced here either.
      expect(
        document.activeElement,
        'the focus the disabled slider would drop is on 썸네일 · T — not body, not the slider',
      ).toBe(thumbs)
      expect(bar).not.toHaveTextContent(ZERO_DENOMINATOR)
      expect(bar.querySelector('[data-role="page-counter"]')).toBeEmptyDOMElement()
      expect(slider.getAttribute('aria-valuetext') ?? '').not.toMatch(ZERO_DENOMINATOR)

      // The continuation re-armed the auto-hide (the chrome carried over); the
      // keyboard focus in the bar holds it at every deadline.
      await act(async () => {
        await vi.advanceTimersByTimeAsync(CHROME_AUTOHIDE_MS * 2)
      })
      expect(chromeLog, 'the keyboard focus is in the bar, so the chrome stays up').toEqual([])
      expect(document.activeElement).toBe(thumbs)

      // ── 4. `H` from 썸네일 · T: the chrome hides and the focus lands on the root. ──
      press('h')
      expect(chromeLog, 'H hides the chrome').toEqual(['down'])
      const root = document.querySelector<HTMLElement>('[data-role="viewer"]')
      expect(root).toHaveAttribute('data-chrome', 'hidden')
      expect(bar).toHaveAttribute('inert')
      expect(document.activeElement, 'the landing (E-62 §2.5): the viewer root').toBe(root)
    } finally {
      unsubscribe()
      releaseBook?.()
      // The store's timers were armed on the fake clock; drop them on it.
      cancelChromeAutoHide()
      vi.useRealTimers()
    }
  })
})
