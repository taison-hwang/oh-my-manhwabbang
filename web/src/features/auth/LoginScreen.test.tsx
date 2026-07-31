import '@testing-library/jest-dom/vitest'

import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { setupServer } from 'msw/node'
import { useCallback, useMemo, type ReactNode } from 'react'
import { RouterProvider, createMemoryRouter } from 'react-router-dom'
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'

import { App } from '../../App'
import { ORIGIN, authStatus, rootsResponse, scanStatusIdle, seriesListResponse } from '../../api/fixtures'
import { errorEnvelope } from '../../api/fixtures'
import { queryKeys, useAuthStatus, useLogin, useRoots, useScanStatus, useSeriesList } from '../../api/queries'
import { resetBasePath } from '../../api/urls'
import { DEFAULT_SHELL_DATA, ShellDataContext, type ShellData } from '../../lib/shellData'
import { useUiStore } from '../../store/ui'

/**
 * Ruling E-17 — **in-app re-authentication**.
 *
 * There are two login surfaces, and they have different jobs. Arch §8.2 makes auth
 * all-or-nothing, static assets included, so an unauthenticated visitor never receives
 * the SPA bundle at all: cold entry is WP-12's *server-rendered* form. This screen is the
 * other job. A reading session outlives its cookie — `session_ttl` is finite and a volume
 * takes an hour — and when the next request comes back `401` the reader must be able to
 * type the password where they are, not be thrown back to a full page load that loses the
 * route, the scroll position and every warm query in the cache.
 *
 * The chain under test is the whole of it, end to end:
 *
 *   any 401  →  client.ts `emitUnauthorized`  →  queries.ts `installUnauthorizedInvalidation`
 *            →  `/api/auth/status` re-read    →  App's gate  →  this screen
 *            →  `useLogin`                    →  invalidate everything  →  back where we were
 *
 * The provider below is the one in `router.tsx` (`ShellDataProvider`), which is private to
 * that module; it is reproduced rather than exported so that this test does not reshape a
 * file it does not own.
 */

const server = setupServer()

beforeAll(() => {
  server.listen({ onUnhandledRequest: 'error' })

  // jsdom/undici interop, not a product concern — the same workaround
  // `appShell.test.tsx` documents: React Router builds a `Request` per navigation
  // and undici rejects jsdom's foreign `AbortSignal`.
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
})
afterAll(() => {
  server.close()
})

beforeEach(() => {
  // A desktop viewport, so the shell renders its sidebar rather than the rail.
  Object.defineProperty(window, 'matchMedia', {
    writable: true,
    configurable: true,
    value: (query: string) => {
      const m = /min-width:\s*(\d+)px/.exec(query)
      return {
        matches: m?.[1] !== undefined && 1440 >= Number(m[1]),
        media: query,
        addEventListener: () => undefined,
        removeEventListener: () => undefined,
      }
    },
  })
  localStorage.clear()
  useUiStore.setState({ theme: 'system', drawerOpen: false, overlays: [], scope: 'all' })
})

/** A verbatim stand-in for `router.tsx`'s private `ShellDataProvider`. */
function ShellDataProvider({ children }: { children: ReactNode }) {
  const auth = useAuthStatus()
  const roots = useRoots()
  const scan = useScanStatus()
  const login = useLogin()
  const all = useSeriesList({ limit: 1 })

  const { mutateAsync } = login
  const submitLogin = useCallback(
    async (password: string): Promise<void> => {
      await mutateAsync(password)
    },
    [mutateAsync],
  )

  const value = useMemo<ShellData>(
    () => ({
      auth: auth.data ?? DEFAULT_SHELL_DATA.auth,
      login: submitLogin,
      loginPending: login.isPending,
      roots: roots.data?.items ?? [],
      rootsLoaded: roots.isSuccess,
      counts: { reading: 0, added: all.data?.total ?? 0, done: 0 },
      scan: scan.data ?? DEFAULT_SHELL_DATA.scan,
    }),
    [all.data, auth.data, login.isPending, roots.data, roots.isSuccess, scan.data, submitLogin],
  )

  return <ShellDataContext.Provider value={value}>{children}</ShellDataContext.Provider>
}

function makeClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: { retry: false, refetchOnWindowFocus: false, gcTime: Infinity },
      mutations: { retry: false },
    },
  })
}

/** Mounts the real `App` at `path`, under the real query hooks. */
function renderApp(client: QueryClient, path: string): void {
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
          { index: true, element: <p>library</p> },
          { path: 'series/:sid', element: <p>series screen</p> },
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

/** The happy path: a live session, everything answering. */
function handlersForALiveSession(): void {
  server.use(
    http.get(`${ORIGIN}/api/auth/status`, () =>
      HttpResponse.json({ auth_required: true, authenticated: true }),
    ),
    http.get(`${ORIGIN}/api/roots`, () => HttpResponse.json(rootsResponse)),
    http.get(`${ORIGIN}/api/scan/status`, () => HttpResponse.json(scanStatusIdle)),
    http.get(`${ORIGIN}/api/series`, () => HttpResponse.json(seriesListResponse)),
  )
}

describe('in-app re-authentication (ruling E-17)', () => {
  it('shows the login screen in place when the session expires mid-session, and restores the route on success', async () => {
    const user = userEvent.setup()
    let sessionLive = true
    server.use(
      http.get(`${ORIGIN}/api/auth/status`, () =>
        HttpResponse.json({ auth_required: true, authenticated: sessionLive }),
      ),
      http.get(`${ORIGIN}/api/roots`, () => HttpResponse.json(rootsResponse)),
      http.get(`${ORIGIN}/api/scan/status`, () => HttpResponse.json(scanStatusIdle)),
      http.get(`${ORIGIN}/api/series`, () =>
        sessionLive
          ? HttpResponse.json(seriesListResponse)
          : HttpResponse.json(errorEnvelope('unauthorized', 'session expired'), { status: 401 }),
      ),
      http.post(`${ORIGIN}/api/auth/login`, () => {
        sessionLive = true
        return new HttpResponse(null, { status: 204 })
      }),
    )

    const client = makeClient()
    // Deep in the app, not on the library index: what a hard reload would cost
    // is exactly this route.
    renderApp(client, '/series/gzj75n6x7rir6but')

    await screen.findByText('series screen')
    expect(screen.queryByRole('heading', { name: '비밀번호를 입력하세요' })).not.toBeInTheDocument()

    // The cookie expires, and the next thing the app asks for comes back 401.
    // Any query does — this one is the library count the sidebar refreshes.
    sessionLive = false
    await act(async () => {
      await client.refetchQueries({ queryKey: queryKeys.series.all })
    })

    // The screen is replaced in place. No navigation, no reload: the router is
    // still on the series route underneath.
    const heading = await screen.findByRole('heading', { name: '비밀번호를 입력하세요' })
    expect(heading).toBeInTheDocument()
    expect(screen.queryByText('series screen')).not.toBeInTheDocument()

    // And the reader types the password where they are.
    await user.type(screen.getByLabelText('비밀번호'), 'hunter2')
    await user.click(screen.getByRole('button', { name: '로그인' }))

    await waitFor(() => {
      expect(screen.getByText('series screen')).toBeInTheDocument()
    })
    expect(screen.queryByRole('heading', { name: '비밀번호를 입력하세요' })).not.toBeInTheDocument()
  })

  it('reports a rejected password in place, without losing the session screen', async () => {
    const user = userEvent.setup()
    handlersForALiveSession()
    server.use(
      http.get(`${ORIGIN}/api/auth/status`, () => HttpResponse.json(authStatus)),
      http.post(`${ORIGIN}/api/auth/login`, () =>
        HttpResponse.json(errorEnvelope('unauthorized', 'invalid password'), { status: 401 }),
      ),
    )

    renderApp(makeClient(), '/')

    await user.type(await screen.findByLabelText('비밀번호'), 'wrong')
    await user.click(screen.getByRole('button', { name: '로그인' }))

    expect(await screen.findByRole('alert')).toHaveTextContent('invalid password')
    // Still the login screen, and still usable for a second attempt.
    expect(screen.getByRole('button', { name: '로그인' })).toBeInTheDocument()
  })
})
