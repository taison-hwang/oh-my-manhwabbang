import '@testing-library/jest-dom/vitest'

import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { setupServer } from 'msw/node'
import { StrictMode, Suspense, type ReactNode } from 'react'
import { createMemoryRouter, RouterProvider, useLocation, useNavigate } from 'react-router-dom'
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
import { queryKeys } from '../../api/queries'
import { resetBasePath } from '../../api/urls'
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

const progressWrites: { bid: string; body: unknown }[] = []

const BOOK_URL = `/series/${SERIES_ID}/books/${BOOK_ID}?page=12`

const server = setupServer(
  http.get(`${ORIGIN}/api/books/:bid`, ({ params }) =>
    params.bid === BOOK_ID
      ? HttpResponse.json(detail)
      : HttpResponse.json({
          ...detail,
          id: String(params.bid),
          page_count: 190,
          pages: PAGES.slice(0, 190),
          next_book_id: null,
          progress: null,
        }),
  ),
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
  seriesGate = new Promise<void>((resolve) => {
    release = resolve
  })
  return async () => {
    const gate = seriesGate
    seriesGate = null
    await act(async () => {
      release()
      await gate
    })
  }
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

/** The app's QueryClient (`main.tsx`), minus nothing. */
function appQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: { queries: { staleTime: 30_000, refetchOnWindowFocus: false, retry: 1 } },
  })
}

/** A data router like `router.tsx`'s — its updates go through `startTransition`. */
function mountApp(strict: boolean) {
  const router = createMemoryRouter(
    [
      { path: '/series/:sid', element: <SeriesStandIn /> },
      { path: '/series/:sid/books/:bid', element: <ViewerPage /> },
    ],
    { initialEntries: [BOOK_URL] },
  )
  const client = appQueryClient()
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
async function startLeavingThroughBack(): Promise<() => Promise<void>> {
  const release = holdTheSeriesScreen()
  await act(async () => {
    fireEvent.click(screen.getByRole('button', { name: '뒤로' }))
    await Promise.resolve()
  })
  expect(useViewerStore.getState().bookId, '뒤로 closed the store').toBeNull()
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
