import '@testing-library/jest-dom/vitest'

import { QueryClientProvider, type QueryClient } from '@tanstack/react-query'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { setupServer } from 'msw/node'
import { StrictMode, Suspense, type ReactNode } from 'react'
import { createMemoryRouter, matchPath, Outlet, RouterProvider, useLocation, useNavigate } from 'react-router-dom'
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'

import {
  BOOK_ID,
  ORIGIN,
  SERIES_ID,
  bookDetail,
  bookSummary,
  errorEnvelope,
  seriesDetail,
  settings,
} from '../../api/fixtures'
import type { BookDetail, PageInfo, Progress } from '../../api/types'
import { createQueryClient, PROGRESS_DEBOUNCE_MS, queryKeys } from '../../api/queries'
import { resetBasePath } from '../../api/urls'
import { useGlobalHotkeys } from '../../lib/useHotkeys'
import { cancelChromeAutoHide, useViewerStore } from '../../store/viewer'
import { failUnhandled } from '../../test/msw'
import { resetPrefsWriter } from './prefsWriter'
import { ViewerPage } from './ViewerPage'

/**
 * Re-entering a book with this viewer instance still mounted.
 *
 * `ViewerPage`'s open effect is latched (`openedRef`), and the store is closed
 * elsewhere: by the unmount cleanup, and synchronously by 뒤로 / 라이브러리 /
 * `Esc` ahead of their navigation. A latch that is only "the last book id this
 * instance opened" is never told about either, so any sequence that closes the
 * store and then runs the open effect again **on the same instance** with the
 * same book skipped `open()` — a closed store under a mounted viewer, for good.
 *
 * Cold entries escape it: `detail` is `undefined` when StrictMode replays the
 * effects, so the latch is not yet set. Only a warm cache (`detail` defined on
 * the very first render) hits it.
 *
 * The leave is held **in flight** here rather than raced: the series screen can
 * be made to suspend (`holdTheSeriesScreen`), and a transition that suspends
 * keeps the screen it is leaving — the store already closed by 뒤로, the viewer
 * still mounted — which is exactly the window a real Back lands in while the
 * series screen's render has not committed.
 */

const NEXT_BOOK_ID = 'nextbook33333333'
const PAGE_COUNT = 214
const PAGES: PageInfo[] = Array.from({ length: PAGE_COUNT }, (_, i) => ({
  n: i + 1,
  name: `page_${String(i + 1).padStart(3, '0')}.jpg`,
  ext: '.jpg',
  size: 180_000,
  w: 800,
  h: 1_200,
}))

const PROGRESS: Progress = {
  book_id: BOOK_ID,
  series_id: SERIES_ID,
  last_page: 42,
  page_count: PAGE_COUNT,
  completed: false,
  started_at: 1,
  updated_at: 2,
  stale: false,
}

const detail: BookDetail = {
  ...bookDetail,
  page_count: PAGE_COUNT,
  pages: PAGES,
  dims_state: 'done',
  next_book_id: NEXT_BOOK_ID,
  progress: PROGRESS,
  prefs: { reading_direction: 'ltr', display_mode: 'single', fit_mode: 'height', is_override: true },
}

/** The server's detail for `bid`: 01권 is `detail`, any other id a 190-page 02권 nobody has read. */
function detailOf(bid: string): BookDetail {
  return bid === BOOK_ID
    ? detail
    : {
        ...detail,
        id: bid,
        page_count: 190,
        pages: PAGES.slice(0, 190),
        next_book_id: null,
        progress: null,
      }
}

const progressWrites: { bid: string; body: unknown }[] = []

const BOOK_URL = `/series/${SERIES_ID}/books/${BOOK_ID}?page=12`

const server = setupServer(
  http.get(`${ORIGIN}/api/books/:bid`, ({ params }) => HttpResponse.json(detailOf(String(params.bid)))),
  http.get(`${ORIGIN}/api/series/:sid`, () =>
    HttpResponse.json({
      ...seriesDetail,
      books: [
        { ...bookSummary, id: BOOK_ID, page_count: PAGE_COUNT },
        { ...bookSummary, id: NEXT_BOOK_ID, name: '군계(軍鷄) 02권.zip', ord: 1, page_count: 190, progress: null },
      ],
    }),
  ),
  http.get(`${ORIGIN}/api/settings`, () => HttpResponse.json(settings)),
  http.put(`${ORIGIN}/api/books/:bid/progress`, async ({ request, params }) => {
    progressWrites.push({ bid: String(params.bid), body: await request.json() })
    return HttpResponse.json(PROGRESS)
  }),
  http.put(`${ORIGIN}/api/books/:bid/prefs`, () => HttpResponse.json(detail.prefs)),
  http.get(`${ORIGIN}/api/books/:bid/thumbs/:n`, () =>
    HttpResponse.json(errorEnvelope('thumb_unavailable', 'not generated'), { status: 422 }),
  ),
)

beforeAll(() => {
  server.listen({ onUnhandledRequest: failUnhandled })
  // Same jsdom/undici shim as ViewerPage.test.tsx: data routers build a Request
  // per navigation with a jsdom AbortSignal that undici rejects.
  const Base = globalThis.Request
  class SignallessRequest extends Base {
    constructor(input: RequestInfo | URL, init?: RequestInit) {
      super(input, init === undefined ? undefined : { ...init, signal: null })
    }
  }
  Object.defineProperty(globalThis, 'Request', { writable: true, configurable: true, value: SignallessRequest })
})
afterEach(() => {
  seriesGate = null
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
  progressWrites.length = 0
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

/** While set, the series screen suspends — so a leave toward it stays in flight. */
let seriesGate: Promise<void> | null = null

/**
 * Hold the series screen's render until the returned function is awaited.
 *
 * The router's update is a transition; a transition that suspends does not
 * commit, so the viewer stays the screen with 뒤로's `close()` already applied.
 */
function holdTheSeriesScreen(): () => Promise<void> {
  let release = (): void => undefined
  const gate = new Promise<void>((resolve) => {
    release = resolve
  })
  seriesGate = gate
  // Each hold lets go of its own gate: a test can hold, be superseded, and hold
  // again, and releasing the first must not await (or drop) the second.
  return async () => {
    if (seriesGate === gate) seriesGate = null
    await act(async () => {
      release()
      await gate
    })
  }
}

/**
 * Keep `bid`'s detail on the wire until the returned function is awaited — a
 * cold entry whose `GET /api/books/{bid}` has not answered yet.
 *
 * The release waits for the answer to reach the cache and then lets React Query
 * publish it, so whatever the open effect does with it has happened by the time
 * the caller looks.
 */
function holdTheDetail(bid: string): (client: QueryClient) => Promise<void> {
  let open = (): void => undefined
  const gate = new Promise<void>((resolve) => {
    open = resolve
  })
  server.use(
    http.get(`${ORIGIN}/api/books/${bid}`, async () => {
      await gate
      return HttpResponse.json(detailOf(bid))
    }),
  )
  return async (client) => {
    await act(async () => {
      open()
      await gate
    })
    await waitFor(() => {
      expect(client.getQueryState(queryKeys.books.detail(bid))?.status, 'the held detail never landed').toBe(
        'success',
      )
    })
    // React Query notifies on its own scheduler tick.
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 20))
    })
  }
}

/** Past the progress writer's debounce, so a save that was scheduled has gone out. */
async function outlastTheDebounce(): Promise<void> {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, PROGRESS_DEBOUNCE_MS + 600))
  })
}

/** The series route, standing in for SeriesDetailPage: just a way back in. */
function SeriesStandIn() {
  const location = useLocation()
  const navigate = useNavigate()
  // React 18's Suspense protocol is a thrown thenable; there is no `use()` yet.
  // eslint-disable-next-line @typescript-eslint/only-throw-error
  if (seriesGate !== null) throw seriesGate
  return (
    <div>
      <p data-testid="location">{location.pathname}</p>
      <button type="button" onClick={() => void navigate(BOOK_URL)}>
        다시 열기
      </button>
    </div>
  )
}

/**
 * The shell, standing in for `App.tsx`: the global key ladder, wired the way the
 * app wires it. Its `Esc` closes the store itself and then navigates, so it is a
 * leave that never goes through the viewer's own handlers.
 */
function ShellStandIn() {
  const location = useLocation()
  const navigate = useNavigate()
  const seriesId = matchPath('/series/:sid/*', location.pathname)?.params.sid ?? null
  useGlobalHotkeys({
    onExitViewer: () => {
      void navigate(seriesId === null ? '/' : `/series/${seriesId}`)
    },
  })
  return <Outlet />
}

/** A data router like `router.tsx`'s — its updates go through `startTransition`. */
function mountApp(strict: boolean) {
  const router = createMemoryRouter(
    [
      {
        element: <ShellStandIn />,
        children: [
          // 라이브러리's destination; the stand-in is only a screen that can be held.
          { path: '/', element: <SeriesStandIn /> },
          { path: '/series/:sid', element: <SeriesStandIn /> },
          { path: '/series/:sid/books/:bid', element: <ViewerPage /> },
        ],
      },
    ],
    { initialEntries: [BOOK_URL] },
  )
  // The app's own client, what `main.tsx` boots with.
  const client = createQueryClient()
  const tree: ReactNode = (
    <QueryClientProvider client={client}>
      <Suspense fallback={null}>
        <RouterProvider router={router} />
      </Suspense>
    </QueryClientProvider>
  )
  const view = render(strict ? <StrictMode>{tree}</StrictMode> : tree)
  return { router, client, ...view }
}

async function expectOpenOn(bid: string, pageCount: number): Promise<void> {
  await waitFor(
    () => {
      expect(useViewerStore.getState().bookId, 'the viewer store never opened').toBe(bid)
    },
    { timeout: 1_500 },
  )
  expect(useViewerStore.getState().pageCount).toBe(pageCount)
  expect(await screen.findAllByRole('img', { name: /page_/ })).not.toHaveLength(0)
}

/** 뒤로 — `exit()`: `close()` now, then the navigation (a transition). */
async function leaveThroughBack(): Promise<void> {
  fireEvent.click(screen.getByRole('button', { name: '뒤로' }))
  await waitFor(() => {
    expect(screen.getByTestId('location')).toHaveTextContent(`/series/${SERIES_ID}`)
  })
  expect(useViewerStore.getState().bookId).toBeNull()
}

/** 뒤로, with the series screen held: the store is closed, the viewer still mounted. */
function startLeavingThroughBack(): Promise<() => Promise<void>> {
  return startLeavingThrough('뒤로')
}

/** 뒤로 or 라이브러리 (`goLibrary`), with the screen it goes to held. */
async function startLeavingThrough(button: '뒤로' | '라이브러리'): Promise<() => Promise<void>> {
  const release = holdTheSeriesScreen()
  await act(async () => {
    fireEvent.click(screen.getByRole('button', { name: button }))
    await Promise.resolve()
  })
  expect(useViewerStore.getState().bookId, `${button} closed the store`).toBeNull()
  expect(document.querySelector('[data-role="viewer"]'), 'the leave is still in flight').not.toBeNull()
  expect(screen.queryByTestId('location')).toBeNull()
  return release
}

/** Turn three pages from `?page=12`, so the page being left on is not the URL's. */
function readToPage15(): void {
  for (let i = 0; i < 3; i += 1) fireEvent.keyDown(window, { key: 'ArrowRight' })
  expect(useViewerStore.getState().page).toBe(15)
}

/** Every distinct store `bookId` from now on — a reopen shows up as an entry. */
function watchBookId(): { seen: (string | null)[]; stop: () => void } {
  const seen: (string | null)[] = []
  const stop = useViewerStore.subscribe((s) => {
    if (seen.at(-1) !== s.bookId) seen.push(s.bookId)
  })
  return { seen, stop }
}

describe('re-entering the same book with a warm cache', () => {
  it('control: opens again without StrictMode', async () => {
    mountApp(false)
    await expectOpenOn(BOOK_ID, PAGE_COUNT)
    await leaveThroughBack()
    fireEvent.click(screen.getByRole('button', { name: '다시 열기' }))
    await expectOpenOn(BOOK_ID, PAGE_COUNT)
  })

  it('control: a cold first entry opens under StrictMode', async () => {
    mountApp(true)
    await expectOpenOn(BOOK_ID, PAGE_COUNT)
  })

  it('opens again under StrictMode (dev: mount → unmount → mount)', async () => {
    const { client } = mountApp(true)
    await expectOpenOn(BOOK_ID, PAGE_COUNT)
    await leaveThroughBack()
    // The precondition that makes it bite: the detail is cached and fresh.
    expect(client.getQueryState(queryKeys.books.detail(BOOK_ID))?.status).toBe('success')
    fireEvent.click(screen.getByRole('button', { name: '다시 열기' }))
    await expectOpenOn(BOOK_ID, PAGE_COUNT)
  })

  it('opens on a different book whose cache is warm, under StrictMode', async () => {
    // Visit 02권 first (warms its detail), go back to 01권, then return to 02권.
    const { router } = mountApp(true)
    await expectOpenOn(BOOK_ID, PAGE_COUNT)
    await act(async () => {
      await router.navigate(`/series/${SERIES_ID}/books/${NEXT_BOOK_ID}?page=1`)
    })
    await expectOpenOn(NEXT_BOOK_ID, 190)
    await leaveThroughBack()
    await act(async () => {
      await router.navigate(`/series/${SERIES_ID}/books/${NEXT_BOOK_ID}?page=1`)
    })
    await expectOpenOn(NEXT_BOOK_ID, 190)
  })
})

describe('production paths (no StrictMode)', () => {
  /**
   * The browser's Back, pressed before 뒤로's transition has committed. Back is
   * a POP to the entry the leave started from, and a POP hands back that
   * entry's **own** key: nothing the viewer can see from the location says a
   * navigation happened. A fix keyed on `location.key` passes the PUSH case
   * below and fails this one.
   */
  it('뒤로 superseded by the browser’s Back — a POP that brings back the same location key', async () => {
    const { router } = mountApp(false)
    await expectOpenOn(BOOK_ID, PAGE_COUNT)
    const entryKey = router.state.location.key
    const release = await startLeavingThroughBack()

    await act(async () => {
      await router.navigate(-1)
    })
    expect(router.state.historyAction).toBe('POP')
    expect(router.state.location.key, 'the entry the leave started from, key and all').toBe(entryKey)
    await release()
    await expectOpenOn(BOOK_ID, PAGE_COUNT)
  })

  it('뒤로 superseded by a fresh navigation to the same URL — a PUSH, a new key', async () => {
    const { router } = mountApp(false)
    await expectOpenOn(BOOK_ID, PAGE_COUNT)
    const release = await startLeavingThroughBack()

    await act(async () => {
      await router.navigate(BOOK_URL)
    })
    expect(router.state.historyAction).toBe('PUSH')
    await release()
    await expectOpenOn(BOOK_ID, PAGE_COUNT)
  })

  /**
   * The guard on any fix: 뒤로's own `close()` must not be undone while the
   * viewer is still mounted. A reopen there resets `page` to the URL's
   * `?page=12`, and the unmount flush then writes 12 over the page the reader
   * actually left on (D-13 / NFR-DAT-004). Subscribing the open effect to the
   * store's `bookId` would do exactly that.
   */
  it('뒤로 does not reopen the viewer on its way out, and saves the page the reader left on', async () => {
    mountApp(false)
    await expectOpenOn(BOOK_ID, PAGE_COUNT)
    readToPage15()
    const watch = watchBookId()
    await leaveThroughBack()
    watch.stop()
    expect(watch.seen, 'the store reopened between close() and the unmount').toEqual([null])
    await waitFor(() => {
      expect(progressWrites.length).toBeGreaterThan(0)
    })
    expect(progressWrites.at(-1)).toEqual({ bid: BOOK_ID, body: { page: 15 } })
  })

  /**
   * The same guard, with the window held open and something re-running the
   * open effect inside it: a progress `PUT` that was on the wire answers, and
   * `useSaveProgress.onSuccess` replaces the book's detail. "The store is
   * closed" alone is no reason to reopen — 뒤로 closed it, and is not done.
   */
  it('a detail write landing while 뒤로 is in flight reopens nothing', async () => {
    const { client } = mountApp(false)
    await expectOpenOn(BOOK_ID, PAGE_COUNT)
    readToPage15()
    const watch = watchBookId()
    const release = await startLeavingThroughBack()

    await act(async () => {
      client.setQueryData<BookDetail>(queryKeys.books.detail(BOOK_ID), (old) =>
        old === undefined ? old : { ...old, progress: { ...PROGRESS, last_page: 15, updated_at: 3 } },
      )
      // React Query notifies on its own scheduler tick.
      await new Promise((resolve) => setTimeout(resolve, 20))
    })
    expect(useViewerStore.getState().bookId, 'reopened under a leave in flight').toBeNull()

    await release()
    await waitFor(() => {
      expect(screen.getByTestId('location')).toHaveTextContent(`/series/${SERIES_ID}`)
    })
    watch.stop()
    expect(watch.seen).toEqual([null])
    await waitFor(() => {
      expect(progressWrites.length).toBeGreaterThan(0)
    })
    expect(progressWrites.at(-1)).toEqual({ bid: BOOK_ID, body: { page: 15 } })
  })

  /**
   * One instance, measured by its root node: a remount per book (`key={bid}`
   * on the route element or the viewer root) would also reopen each book, so
   * reopening alone cannot tell the two apart.
   */
  it('X → 다음 권 → back to X keeps one instance and reopens X', async () => {
    const viewerRoot = (): Element | null => document.querySelector('[data-role="viewer"]')
    const { router } = mountApp(false)
    await expectOpenOn(BOOK_ID, PAGE_COUNT)
    expect(useViewerStore.getState().page, 'X opens on its ?page=12').toBe(12)
    const root = viewerRoot()
    expect(root).not.toBeNull()

    await act(async () => {
      await router.navigate(`/series/${SERIES_ID}/books/${NEXT_BOOK_ID}?page=1`)
    })
    await expectOpenOn(NEXT_BOOK_ID, 190)
    expect(useViewerStore.getState().page, '다음 권 opens on its ?page=1').toBe(1)
    expect(viewerRoot(), '다음 권 remounted the viewer').toBe(root)

    await act(async () => {
      await router.navigate(-1)
    })
    await expectOpenOn(BOOK_ID, PAGE_COUNT)
    expect(useViewerStore.getState().page, 'back to X opens on its ?page=12 again').toBe(12)
    expect(viewerRoot(), 'back to X remounted the viewer').toBe(root)
  })

  it('X → elsewhere → X as two committed navigations remounts and reopens', async () => {
    const { router } = mountApp(false)
    await expectOpenOn(BOOK_ID, PAGE_COUNT)
    await act(async () => {
      await router.navigate(`/series/${SERIES_ID}`)
    })
    expect(useViewerStore.getState().bookId).toBeNull()
    await act(async () => {
      await router.navigate(BOOK_URL)
    })
    await expectOpenOn(BOOK_ID, PAGE_COUNT)
  })
})

/**
 * Leaving a cold entry before its detail has answered.
 *
 * On a cold entry the leave has nothing to close: this instance has opened
 * nothing yet, so `openedRef` is empty. A detail landing while the leave is in
 * flight therefore looked like a first open and opened the book — and the
 * unmount flush then wrote `?page=` as progress for a book the reader never saw,
 * which also moves it up 이어보기 (D-13).
 */
describe('leaving a cold entry before its detail arrives (no StrictMode)', () => {
  it('뒤로 before the detail opens nothing on the way out and writes no progress', async () => {
    const releaseDetail = holdTheDetail(BOOK_ID)
    const { client } = mountApp(false)
    expect(client.getQueryData(queryKeys.books.detail(BOOK_ID)), 'a cold entry').toBeUndefined()
    const watch = watchBookId()
    const releaseLeave = await startLeavingThroughBack()

    await releaseDetail(client)
    expect(useViewerStore.getState().bookId, 'the detail opened the book under a leave in flight').toBeNull()

    await releaseLeave()
    await waitFor(() => {
      expect(screen.getByTestId('location')).toHaveTextContent(`/series/${SERIES_ID}`)
    })
    await outlastTheDebounce()
    watch.stop()
    expect(watch.seen, 'the store opened a book the reader never saw').not.toContain(BOOK_ID)
    expect(progressWrites, 'progress written for a book the reader never saw').toEqual([])
  })

  it('라이브러리 before the detail opens nothing on the way out and writes no progress', async () => {
    const releaseDetail = holdTheDetail(BOOK_ID)
    const { client } = mountApp(false)
    expect(client.getQueryData(queryKeys.books.detail(BOOK_ID)), 'a cold entry').toBeUndefined()
    const watch = watchBookId()
    const releaseLeave = await startLeavingThrough('라이브러리')

    await releaseDetail(client)
    expect(useViewerStore.getState().bookId, 'the detail opened the book under a leave in flight').toBeNull()

    await releaseLeave()
    await waitFor(() => {
      expect(screen.getByTestId('location').textContent).toBe('/')
    })
    await outlastTheDebounce()
    watch.stop()
    expect(watch.seen, 'the store opened a book the reader never saw').not.toContain(BOOK_ID)
    expect(progressWrites, 'progress written for a book the reader never saw').toEqual([])
  })

  /**
   * The guard on the fix: what stops the leave's own detail must not stop a
   * return. Back before 뒤로 has committed lands this route again, and the
   * detail that arrives after it is an entry like any other.
   */
  it('뒤로 superseded by the browser’s Back before the detail: the detail then opens the book', async () => {
    const releaseDetail = holdTheDetail(BOOK_ID)
    const { router, client } = mountApp(false)
    const releaseLeave = await startLeavingThroughBack()

    await act(async () => {
      await router.navigate(-1)
    })
    expect(router.state.historyAction).toBe('POP')
    await releaseDetail(client)
    await expectOpenOn(BOOK_ID, PAGE_COUNT)
    // The superseded leave's screen, let go: it lands nothing.
    await releaseLeave()
    expect(useViewerStore.getState().bookId).toBe(BOOK_ID)
  })

  /**
   * Two leaves on one cold entry, with a return between them. The return renders
   * this route again, but the detail is still on the wire, so the open effect
   * stops at its first line and the latch keeps the *first* leave's route. The
   * second leave is the same book, and a leave that latched only "if this
   * instance has not latched this book yet" left that stale route in place — so
   * the detail, landing under the second leave, looked navigated-to and opened
   * the book.
   */
  it('뒤로, Back to the book, 뒤로 again — all before the detail: nothing opens, nothing is written', async () => {
    const releaseDetail = holdTheDetail(BOOK_ID)
    const { router, client } = mountApp(false)
    expect(client.getQueryData(queryKeys.books.detail(BOOK_ID)), 'a cold entry').toBeUndefined()
    const watch = watchBookId()
    const releaseFirstLeave = await startLeavingThroughBack()

    await act(async () => {
      await router.navigate(-1)
    })
    expect(router.state.historyAction).toBe('POP')
    expect(router.state.location.pathname).toBe(`/series/${SERIES_ID}/books/${BOOK_ID}`)
    const releaseSecondLeave = await startLeavingThroughBack()

    await releaseDetail(client)
    expect(useViewerStore.getState().bookId, 'the detail opened the book under the second leave').toBeNull()

    await releaseSecondLeave()
    await releaseFirstLeave()
    await waitFor(() => {
      expect(screen.getByTestId('location')).toHaveTextContent(`/series/${SERIES_ID}`)
    })
    await outlastTheDebounce()
    watch.stop()
    expect(watch.seen, 'the store opened a book the reader never saw').not.toContain(BOOK_ID)
    expect(progressWrites, 'progress written for a book the reader never saw').toEqual([])
  })

  /**
   * 다음 권 is a cold entry on a warm instance: `openedRef` holds the volume
   * before it, so "has this instance latched *this* book" is the question, not
   * "has it latched anything".
   */
  it('다음 권, then 뒤로 before its detail: the next volume is neither opened nor written', async () => {
    const releaseNext = holdTheDetail(NEXT_BOOK_ID)
    const { router, client } = mountApp(false)
    await expectOpenOn(BOOK_ID, PAGE_COUNT)
    await act(async () => {
      await router.navigate(`/series/${SERIES_ID}/books/${NEXT_BOOK_ID}?page=1`)
    })
    expect(client.getQueryData(queryKeys.books.detail(NEXT_BOOK_ID)), '다음 권 is a cold entry').toBeUndefined()
    const watch = watchBookId()
    const releaseLeave = await startLeavingThroughBack()

    await releaseNext(client)
    expect(useViewerStore.getState().bookId, 'the detail opened 다음 권 under a leave in flight').toBeNull()

    await releaseLeave()
    await waitFor(() => {
      expect(screen.getByTestId('location')).toHaveTextContent(`/series/${SERIES_ID}`)
    })
    await outlastTheDebounce()
    watch.stop()
    expect(watch.seen, 'the store opened a volume the reader never saw').not.toContain(NEXT_BOOK_ID)
    expect(
      progressWrites.filter((w) => w.bid === NEXT_BOOK_ID),
      'progress written for a volume the reader never saw',
    ).toEqual([])
  })

  /**
   * The global `Esc` ladder (`useHotkeys.ts`, wired by `App.tsx`) is a leave the
   * viewer's own handlers never see. While 다음 권's detail is on the wire the
   * viewer's keys are off (`enabled: detail !== undefined`) and the store still
   * holds the volume before, so the ladder closes the store itself and
   * navigates. Nothing latched the new volume, and its detail, landing under
   * that leave, opened it and wrote progress for it.
   */
  it('다음 권, then the global Esc before its detail: the next volume is neither opened nor written', async () => {
    const releaseNext = holdTheDetail(NEXT_BOOK_ID)
    const { router, client } = mountApp(false)
    await expectOpenOn(BOOK_ID, PAGE_COUNT)
    await act(async () => {
      await router.navigate(`/series/${SERIES_ID}/books/${NEXT_BOOK_ID}?page=1`)
    })
    expect(client.getQueryData(queryKeys.books.detail(NEXT_BOOK_ID)), '다음 권 is a cold entry').toBeUndefined()
    expect(useViewerStore.getState().bookId, 'the store still holds the volume before').toBe(BOOK_ID)
    const watch = watchBookId()

    const releaseLeave = holdTheSeriesScreen()
    await act(async () => {
      fireEvent.keyDown(window, { key: 'Escape' })
      await Promise.resolve()
    })
    expect(useViewerStore.getState().bookId, 'the global Esc closed the store').toBeNull()
    expect(document.querySelector('[data-role="viewer"]'), 'the leave is still in flight').not.toBeNull()
    expect(screen.queryByTestId('location')).toBeNull()

    await releaseNext(client)
    expect(useViewerStore.getState().bookId, 'the detail opened 다음 권 under a leave in flight').toBeNull()

    await releaseLeave()
    await waitFor(() => {
      expect(screen.getByTestId('location')).toHaveTextContent(`/series/${SERIES_ID}`)
    })
    await outlastTheDebounce()
    watch.stop()
    expect(watch.seen, 'the store opened a volume the reader never saw').not.toContain(NEXT_BOOK_ID)
    expect(
      progressWrites.filter((w) => w.bid === NEXT_BOOK_ID),
      'progress written for a volume the reader never saw',
    ).toEqual([])
  })
})
