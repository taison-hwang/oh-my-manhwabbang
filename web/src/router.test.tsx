/**
 * `ShellDataProvider` — the seam between `GET /api/roots` and the app shell.
 *
 * Amendment **A-8** (ruling E-9) exists because 최근 추가 used to report the
 * whole-library total: `sort=added` re-orders the library, it does not filter
 * it, so the row's number was the number of *every* series. The regression this
 * file guards is exactly that — the provider must ask the server for
 * `scope=added`, and the number it shows must be that query's `total` and not
 * the unfiltered one.
 *
 * The second block is amendment **A-11** / revision **R2**: a `pending` root
 * reaches the sidebar through this same provider, and must not arrive there as
 * an ordinary row. Both blocks drive the assertion from MSW through the query
 * layer into the rendered shell, never from a prop — the last two defects in
 * this area were a prop that worked and a caller that never supplied it.
 */

import '@testing-library/jest-dom/vitest'

import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { setupServer } from 'msw/node'
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { RouterProvider, createMemoryRouter } from 'react-router-dom'

import { App } from './App'
import {
  errorEnvelope,
  ORIGIN,
  pendingRoot,
  root,
  rootsResponse,
  scanStatusIdle,
  scanStatusRunning,
  settings,
} from './api/fixtures'
import { SETTINGS_SAVE_DEBOUNCE_MS } from './api/settingsWriter'
import type { AuthStatus, SeriesListResponse, Settings, SettingsUpdate } from './api/types'
import { resetBasePath } from './api/urls'
import { LibraryPage } from './features/library/LibraryPage'
import { useShellData } from './lib/shellData'
import { ShellDataProvider } from './router'
import { UI_STORAGE_KEY, useUiStore } from './store/ui'
import { failUnhandled } from './test/msw'

const server = setupServer()

/** Every `GET /api/series` the provider made, as a query string. */
let seriesQueries: string[] = []

/** `total` per query shape, so a wrong query cannot accidentally read right. */
const TOTALS = { all: 10, added: 3, reading: 4, done: 1 }

function page(total: number): SeriesListResponse {
  return { items: [], total, offset: 0, limit: 1 }
}

beforeAll(() => {
  server.listen({ onUnhandledRequest: failUnhandled })
})
beforeEach(() => {
  seriesQueries = []
  server.use(
    http.get(`${ORIGIN}/api/auth/status`, () =>
      HttpResponse.json({ auth_required: false, authenticated: true } satisfies AuthStatus),
    ),
    http.get(`${ORIGIN}/api/roots`, () => HttpResponse.json(rootsResponse)),
    http.get(`${ORIGIN}/api/scan/status`, () => HttpResponse.json(scanStatusIdle)),
    // The provider hydrates `theme` on every route (`useThemeSettingsSync`), so
    // every test that mounts it reads the settings once.
    http.get(`${ORIGIN}/api/settings`, () => HttpResponse.json(settings)),
    http.get(`${ORIGIN}/api/series`, ({ request }) => {
      const params = new URL(request.url).searchParams
      seriesQueries.push(params.toString())
      if (params.get('scope') === 'added') return HttpResponse.json(page(TOTALS.added))
      if (params.get('progress') === 'reading') return HttpResponse.json(page(TOTALS.reading))
      if (params.get('progress') === 'done') return HttpResponse.json(page(TOTALS.done))
      return HttpResponse.json(page(TOTALS.all))
    }),
  )
})
afterEach(() => {
  server.resetHandlers()
  resetBasePath()
})
afterAll(() => {
  server.close()
})

function Counts() {
  const { counts } = useShellData()
  return (
    <ul>
      <li data-testid="reading">{counts.reading}</li>
      <li data-testid="added">{counts.added}</li>
      <li data-testid="done">{counts.done}</li>
    </ul>
  )
}

function renderProvider() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={client}>
      <ShellDataProvider>
        <Counts />
      </ShellDataProvider>
    </QueryClientProvider>,
  )
}

describe('ShellDataProvider — sidebar counts (A-8 / ruling E-9)', () => {
  it('counts 최근 추가 with scope=added, not with the whole-library total', async () => {
    renderProvider()

    await waitFor(() => {
      expect(screen.getByTestId('added')).toHaveTextContent(String(TOTALS.added))
    })
    // The pre-A-8 bug in one assertion: the row showed the library's size.
    expect(screen.getByTestId('added')).not.toHaveTextContent(String(TOTALS.all))

    const added = seriesQueries.filter((q) => new URLSearchParams(q).get('scope') === 'added')
    expect(added).toHaveLength(1)
    // `limit=1` — only `total` is read (1 is the smallest the contract allows).
    expect(new URLSearchParams(added[0] ?? '').get('limit')).toBe('1')
    // A-8: `scope` is a filter of its own, never a progress value.
    expect(new URLSearchParams(added[0] ?? '').get('progress')).toBeNull()
  })

  it('still counts 읽는 중 and 완독 with progress= (amendment A-4)', async () => {
    renderProvider()

    await waitFor(() => {
      expect(screen.getByTestId('reading')).toHaveTextContent(String(TOTALS.reading))
    })
    await waitFor(() => {
      expect(screen.getByTestId('done')).toHaveTextContent(String(TOTALS.done))
    })
    expect(seriesQueries).toHaveLength(3)
    // Nothing asks for the unfiltered library any more: no count is derived
    // from it, and a fourth request would be one nobody reads.
    expect(seriesQueries.some((q) => new URLSearchParams(q).toString() === 'limit=1')).toBe(false)
  })
})

// ---------------------------------------------------------------------------
// The scan-completion refresh, mounted — FR-IDX-004
// ---------------------------------------------------------------------------

/**
 * `useScanCompletionRefresh` is unit-tested in `api/queries.test.tsx`. This
 * asserts the half that file cannot: that something **mounts** it.
 *
 * That is the exact defect shape this repo has now shipped twice — a prop or a
 * hook that worked, and a caller that never supplied it (rulings E-25, E-26).
 * The provider is the single component that outlives every screen and every
 * overlay, so it is the only correct mount point *and* the only place a missing
 * mount is invisible. Nothing here is passed in: the run has to travel
 * `GET /api/scan/status` → `useScanStatus` → the provider → `invalidateQueries`
 * → a second `GET /api/series` on the wire.
 */
describe('ShellDataProvider — refreshing when a scan finishes (FR-IDX-004)', () => {
  it('re-reads the library and the roots on the non-idle → idle edge', async () => {
    let rootsCalls = 0
    let statusCalls = 0
    server.use(
      http.get(`${ORIGIN}/api/roots`, () => {
        rootsCalls += 1
        return HttpResponse.json(rootsResponse)
      }),
      http.get(`${ORIGIN}/api/scan/status`, () => {
        statusCalls += 1
        // running, running, then finished — the C-11 poll carries it across.
        return HttpResponse.json(statusCalls < 3 ? scanStatusRunning : scanStatusIdle)
      }),
    )
    renderProvider()

    // The three `limit=1` counts and the one root list, before the run ends.
    await waitFor(() => {
      expect(seriesQueries).toHaveLength(3)
    })
    expect(rootsCalls).toBe(1)

    await waitFor(
      () => {
        expect(seriesQueries).toHaveLength(6)
      },
      { timeout: 6_000 },
    )
    expect(rootsCalls).toBe(2)
    // The same three queries, not three new ones: the counts are re-read, and
    // a scan does not change which questions the sidebar asks.
    expect([...new Set(seriesQueries)]).toHaveLength(3)
  }, 15_000)

  it('re-reads nothing when the server was idle the whole time', async () => {
    // The negative control the positive one needs: `beforeEach` already serves
    // an idle status, and a hook that fired on mount would refetch the entire
    // library on every cold start.
    let rootsCalls = 0
    server.use(
      http.get(`${ORIGIN}/api/roots`, () => {
        rootsCalls += 1
        return HttpResponse.json(rootsResponse)
      }),
    )
    renderProvider()

    await waitFor(() => {
      expect(seriesQueries).toHaveLength(3)
    })
    await new Promise((resolve) => setTimeout(resolve, 1_200))
    expect(seriesQueries).toHaveLength(3)
    expect(rootsCalls).toBe(1)
  }, 10_000)
})

// ---------------------------------------------------------------------------
// The sidebar's pending root — amendment A-11 / revision R2 (ruling E-26)
// ---------------------------------------------------------------------------

interface FakeMql {
  matches: boolean
  media: string
  addEventListener: () => void
  removeEventListener: () => void
}

/** Answers `(min-width: Npx)` against a fixed viewport width. */
function stubViewport(width: number): void {
  const impl = (query: string): FakeMql => {
    const m = /min-width:\s*(\d+)px/.exec(query)
    const matches = m?.[1] === undefined ? false : width >= Number(m[1])
    return {
      matches,
      media: query,
      addEventListener: () => undefined,
      removeEventListener: () => undefined,
    }
  }
  Object.defineProperty(window, 'matchMedia', { writable: true, configurable: true, value: impl })
}

/**
 * The real shell over the real provider over MSW.
 *
 * No `ShellDataContext.Provider` and no `roots` prop anywhere: the row under
 * test has to arrive as `GET /api/roots` → `useRoots` → `ShellDataProvider` →
 * `App` → `Sidebar`, because the field it turns on (`pending`) was already on
 * the wire and already in `api/types.ts` — what was missing was every step in
 * between, and a prop-fed test cannot see a missing step.
 */
function renderShellFromServer(): void {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const memory = createMemoryRouter(
    [
      {
        path: '/',
        element: (
          <ShellDataProvider>
            <App />
          </ShellDataProvider>
        ),
        children: [{ index: true, element: <p>library</p> }],
      },
    ],
    { initialEntries: ['/'] },
  )
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={memory} />
    </QueryClientProvider>,
  )
}

/**
 * The same shell over the same provider, with the **real library screen** under
 * it instead of a placeholder.
 *
 * It exists for exactly one assertion — the `active` guard — and the screen is
 * what makes that assertion honest. `scope` is not something a test may simply
 * set: what puts a root name in it on a cold start is `useLibrarySettingsSync`
 * hydrating `Settings.library_scope` into the store, and that hook lives in
 * `LibraryPage`. Mounting it is what turns "a stale scope" from a state a test
 * arranged into the state the server hands the client.
 */
function renderLibraryShellFromServer(): void {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const memory = createMemoryRouter(
    [
      {
        path: '/',
        element: (
          <ShellDataProvider>
            <App />
          </ShellDataProvider>
        ),
        children: [{ index: true, element: <LibraryPage /> }],
      },
    ],
    { initialEntries: ['/'] },
  )
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={memory} />
    </QueryClientProvider>,
  )
}

describe('the sidebar and a pending root (A-11 / R2, ruling E-26)', () => {
  beforeEach(() => {
    localStorage.clear()
    useUiStore.setState({ theme: 'system', drawerOpen: false, overlays: [], scope: 'all' })
    stubViewport(1440)
    server.use(
      http.get(`${ORIGIN}/api/roots`, () =>
        HttpResponse.json({ items: [root, pendingRoot] }),
      ),
    )
  })

  afterEach(() => {
    document.documentElement.removeAttribute('data-theme')
  })

  /**
   * The failure this pins: 루트 추가 succeeds, the settings panel correctly says
   * 재시작 후 적용 — and the sidebar simultaneously grows a clickable row with a
   * `0` beside it that scopes the library to nothing, with no explanation.
   * §7.3 fixes a pending root's counts at zero, so the number is not stale, it
   * is meaningless; and roots open only at startup, so the scope is not slow to
   * fill, it is empty until the restart.
   */
  it('shows a pending root as not yet loaded rather than as an empty scope', async () => {
    renderShellFromServer()
    const nav = await screen.findByRole('complementary', { name: '라이브러리 탐색' })
    const row = await within(nav).findByRole('button', { name: new RegExp(pendingRoot.label) })

    // The whole row, exactly: the note is present *and* the phantom `0` is not.
    // Asserted as the full text because `02. lanovel` contains a `0` of its own,
    // so a substring check could not tell the two apart.
    expect(row.textContent).toBe(`${pendingRoot.label}재시작 후 적용`)

    // A disabled control is a promise. This one is kept — a restart opens the
    // root — and the row states it, which is why disabling it is honest here
    // and would not be for the capability gate in `RootsPanel`.
    expect(row).toBeDisabled()
    expect(row).toHaveAttribute('data-active', 'false')
  })

  it('refuses to scope the library to a root the server has not opened', async () => {
    renderShellFromServer()
    const nav = await screen.findByRole('complementary', { name: '라이브러리 탐색' })
    const row = await within(nav).findByRole('button', { name: new RegExp(pendingRoot.label) })

    await userEvent.click(row)

    expect(useUiStore.getState().scope).toBe('all')
    expect(row).toHaveAttribute('data-active', 'false')
  })

  it('leaves a loaded root selectable, with its count', async () => {
    renderShellFromServer()
    const nav = await screen.findByRole('complementary', { name: '라이브러리 탐색' })
    const loaded = await within(nav).findByRole('button', { name: new RegExp(root.label) })

    expect(loaded).toBeEnabled()
    expect(loaded.textContent).toBe(`${root.label}${String(root.series_count)}`)

    await userEvent.click(loaded)
    expect(useUiStore.getState().scope).toBe(root.name)
  })

  /**
   * The stale `library_scope`, which is not a hypothetical and not a state a
   * user can only reach by misusing the app.
   *
   * `Settings.library_scope` is hydrated straight into the store with no
   * validation against the roots that actually loaded
   * (`features/library/useLibrary.ts`; `library_sort` is validated, `scope`
   * cannot be — a root name is user-supplied configuration, not an enum). Delete
   * `index.db` and restart, which `shelf.example.yaml` and arch §3.5 both
   * explicitly invite, and *every* configured root is configured-but-unindexed
   * — `pending: true` — while `library_scope` still names one of them. So this
   * is the ordinary first render after a rebuild, not an edge case.
   *
   * Without the guard the row paints the accent bar and `aria-current="page"`
   * while `disabled`: the one mark on the screen that says "you are here", on
   * the one row the reader cannot leave by clicking a neighbour it looks like.
   *
   * Every other pending-root test above seeds `scope: 'all'`, so `scope ===
   * root.name` is false whatever the guard does and none of them can see it.
   */
  it('refuses to mark a pending root current even when library_scope names it', async () => {
    let served: Settings = { ...settings, library_scope: pendingRoot.name }
    server.use(
      http.get(`${ORIGIN}/api/settings`, () => HttpResponse.json(served)),
      // A-5's write-back is live here because `LibraryPage` is: the real screen
      // syncs the four library preferences, and MSW runs with
      // `onUnhandledRequest: 'error'`. The handler behaves like the server —
      // merge and echo — so the sync settles instead of erroring; what this test
      // asserts is the store, which `hydrateFromSettings` writes exactly once.
      http.put(`${ORIGIN}/api/settings`, async ({ request }) => {
        served = { ...served, ...((await request.json()) as Partial<Settings>) }
        return HttpResponse.json(served)
      }),
      http.get(`${ORIGIN}/api/continue`, () => HttpResponse.json({ items: [] })),
    )
    renderLibraryShellFromServer()

    const nav = await screen.findByRole('complementary', { name: '라이브러리 탐색' })
    const row = await within(nav).findByRole('button', { name: new RegExp(pendingRoot.label) })

    // The premise, measured rather than assumed: the settings payload really did
    // land in the store, so the guard is under load. Without this the test would
    // pass just as happily against a `/api/settings` that never answered.
    await waitFor(() => {
      expect(useUiStore.getState().scope).toBe(pendingRoot.name)
    })

    expect(row).toBeDisabled()
    expect(row).toHaveAttribute('data-active', 'false')
    expect(row).not.toHaveAttribute('aria-current')
    // And nothing else has quietly become "here" either: a stale scope that
    // matches no selectable row leaves the sidebar with no current row at all,
    // which is the honest answer.
    expect(within(nav).queryByRole('button', { current: 'page' })).toBeNull()
  })

  /**
   * The 56px rail (ui-spec §7, 768–1023) drops every label, so a pending row
   * there is one more identical folder icon. The note has to survive into the
   * accessible name or the rail is where this defect comes back.
   */
  it('carries the note into the accessible name in the icon rail', async () => {
    stubViewport(900)
    renderShellFromServer()
    const nav = await screen.findByRole('complementary', { name: '라이브러리 탐색' })
    expect(nav).toHaveClass('sidebar-rail')

    const row = await within(nav).findByRole('button', {
      name: `${pendingRoot.label} — 재시작 후 적용`,
    })
    expect(row).toBeDisabled()
    expect(within(nav).getByRole('button', { name: root.label })).toBeEnabled()
  })
})

// ---------------------------------------------------------------------------
// The server theme on every route, not only the library's
// ---------------------------------------------------------------------------

/**
 * The real shell over the real provider, entered **directly** at `path` — a
 * bookmark, a reload, a link — with placeholder screens, so nothing below the
 * provider reads the settings.
 */
function renderShellAt(
  path: string,
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } }),
): void {
  const memory = createMemoryRouter(
    [
      {
        path: '/',
        element: (
          <ShellDataProvider>
            <App />
          </ShellDataProvider>
        ),
        children: [
          { index: true, element: <p>library screen</p> },
          { path: 'series/:sid', element: <p>series screen</p> },
          { path: 'series/:sid/books/:bid', element: <p>viewer screen</p> },
        ],
      },
    ],
    { initialEntries: [path] },
  )
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={memory} />
    </QueryClientProvider>,
  )
}

/**
 * `main.tsx`'s query defaults, copied: what a query that sets no `retry` of its
 * own inherits in the app (`retry: 1`), where the test default above would hide it.
 */
function productionClient(): QueryClient {
  return new QueryClient({
    defaultOptions: { queries: { staleTime: 30_000, refetchOnWindowFocus: false, retry: 1 } },
  })
}

/** Past the settings writer's debounce, so a write-back would have been sent. */
async function settle(): Promise<void> {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, SETTINGS_SAVE_DEBOUNCE_MS + 100))
  })
}

/**
 * `theme` used to reach the store only through `LibraryPage`'s settings sync, so
 * a reader who never opened the library kept whatever `shelf.ui` restored. The
 * provider is the one component every route renders under; the theme travels
 * `GET /api/settings` → `useThemeSettingsSync` → the store → `data-theme`, with
 * no library screen anywhere in the tree.
 */
describe('ShellDataProvider — the server theme on every route', () => {
  let puts: SettingsUpdate[]

  beforeEach(() => {
    puts = []
    useUiStore.setState({ theme: 'system', drawerOpen: false, overlays: [], scope: 'all' })
    localStorage.clear()
    stubViewport(1440)
    server.use(
      // Recorded rather than left unhandled: hydration must send nothing, and an
      // unhandled request would fail as a network error instead of as a count.
      http.put(`${ORIGIN}/api/settings`, async ({ request }) => {
        const update = (await request.json()) as SettingsUpdate
        puts.push(update)
        return HttpResponse.json({ ...settings, ...update })
      }),
    )
  })

  afterEach(() => {
    document.documentElement.removeAttribute('data-theme')
  })

  it.each([
    ['a series', '/series/s1', 'series screen'],
    ['a viewer', '/series/s1/books/b1', 'viewer screen'],
  ])(
    'lets the server theme beat the local copy on %s URL entered directly, without writing it back',
    async (_, path, screenText) => {
      // This device's last choice, exactly as `persist` stored it.
      localStorage.setItem(UI_STORAGE_KEY, JSON.stringify({ state: { theme: 'light' }, version: 0 }))
      await useUiStore.persist.rehydrate()
      expect(useUiStore.getState().theme, 'premise: the local copy restored').toBe('light')
      // Another device has since saved `dark`.
      server.use(
        http.get(`${ORIGIN}/api/settings`, () =>
          HttpResponse.json({ ...settings, theme: 'dark' } satisfies Settings),
        ),
      )

      renderShellAt(path)
      await screen.findByText(screenText)

      await waitFor(() => {
        expect(useUiStore.getState().theme).toBe('dark')
      })
      expect(document.documentElement).toHaveAttribute('data-theme', 'dark')
      await settle()
      // Read-only: the theme's one writer is the 테마 switch.
      expect(puts).toEqual([])
    },
  )

  /**
   * The provider's queries run before the auth gate decides anything, so behind
   * a password the settings read is one more `401`. It must cost one request
   * (a 4xx is not retried, and the `401` only re-reads the auth status), leave
   * the local theme alone, and be answered by the login's refetch-everything.
   * Under the app's own query defaults, so the "not retried" is `useSettings`'s
   * `retryQuery` and not this file's `retry: false`.
   */
  it('reads the settings once behind the login screen, then hydrates once logged in', async () => {
    const user = userEvent.setup()
    let loggedIn = false
    let settingsReads = 0
    const refused = () =>
      HttpResponse.json(errorEnvelope('unauthorized', 'login required'), { status: 401 })
    useUiStore.setState({ theme: 'light' })
    server.use(
      http.get(`${ORIGIN}/api/auth/status`, () =>
        HttpResponse.json({ auth_required: true, authenticated: loggedIn } satisfies AuthStatus),
      ),
      http.get(`${ORIGIN}/api/roots`, () => (loggedIn ? HttpResponse.json(rootsResponse) : refused())),
      http.get(`${ORIGIN}/api/scan/status`, () =>
        loggedIn ? HttpResponse.json(scanStatusIdle) : refused(),
      ),
      http.get(`${ORIGIN}/api/series`, () => (loggedIn ? HttpResponse.json(page(0)) : refused())),
      http.get(`${ORIGIN}/api/settings`, () => {
        settingsReads += 1
        return loggedIn
          ? HttpResponse.json({ ...settings, theme: 'dark' } satisfies Settings)
          : refused()
      }),
      http.post(`${ORIGIN}/api/auth/login`, () => {
        loggedIn = true
        return new HttpResponse(null, { status: 204 })
      }),
    )

    renderShellAt('/series/s1', productionClient())
    await screen.findByRole('heading', { name: '비밀번호를 입력하세요' })
    // Past the first retry `retry: 1` would schedule (1 s after the failure).
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 1_600))
    })
    expect(settingsReads, 'a refused read is not retried or looped').toBe(1)
    expect(useUiStore.getState().theme).toBe('light')

    await user.type(screen.getByLabelText('비밀번호'), 'hunter2')
    await user.click(screen.getByRole('button', { name: '로그인' }))

    await screen.findByText('series screen')
    await waitFor(() => {
      expect(useUiStore.getState().theme).toBe('dark')
    })
    expect(settingsReads).toBe(2)
    await settle()
    expect(puts).toEqual([])
  }, 10_000)
})

