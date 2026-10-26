import '@testing-library/jest-dom/vitest'

import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { setupServer } from 'msw/node'
import { RouterProvider, createMemoryRouter } from 'react-router-dom'
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'

import { App } from '../../App'
import { ApiError } from '../../api/errors'
import {
  ORIGIN,
  authStatus,
  rootsResponse,
  scanStatusIdle,
  seriesListResponse,
  settings,
} from '../../api/fixtures'
import { errorEnvelope } from '../../api/fixtures'
import { queryKeys } from '../../api/queries'
import { resetBasePath } from '../../api/urls'
import { ShellDataProvider } from '../../router'
import { useUiStore } from '../../store/ui'
import { failUnhandled } from '../../test/msw'
import { LoginScreen } from './LoginScreen'

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
 * The provider is the shipped one, imported from `router.tsx` (`ShellDataProvider`), not a
 * copy: an earlier copy here drifted from it (it lost the three sidebar counts and the
 * `theme` hydration) while staying green. Every request the provider makes is therefore
 * answered below — `src/test/msw.ts` fails the test naming any endpoint it grows.
 */

const server = setupServer()

beforeAll(() => {
  server.listen({ onUnhandledRequest: failUnhandled })

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
    // The provider hydrates `theme` on every route (`useThemeSettingsSync`).
    http.get(`${ORIGIN}/api/settings`, () => HttpResponse.json(settings)),
    http.get(`${ORIGIN}/api/series`, () => HttpResponse.json(seriesListResponse)),
  )
}

describe('in-app re-authentication (ruling E-17)', () => {
  it('shows the login screen in place when the session expires mid-session, and restores the route on success', async () => {
    const user = userEvent.setup()
    let sessionLive = true
    const submitted: unknown[] = []
    server.use(
      http.get(`${ORIGIN}/api/auth/status`, () =>
        HttpResponse.json({ auth_required: true, authenticated: sessionLive }),
      ),
      http.get(`${ORIGIN}/api/roots`, () => HttpResponse.json(rootsResponse)),
      http.get(`${ORIGIN}/api/scan/status`, () => HttpResponse.json(scanStatusIdle)),
      http.get(`${ORIGIN}/api/settings`, () => HttpResponse.json(settings)),
      // All three sidebar counts (읽는 중 · 최근 추가 · 완독) are `/api/series` calls.
      http.get(`${ORIGIN}/api/series`, () =>
        sessionLive
          ? HttpResponse.json(seriesListResponse)
          : HttpResponse.json(errorEnvelope('unauthorized', 'session expired'), { status: 401 }),
      ),
      // The body is read, not just the route: a screen that submitted anything but
      // the typed password would otherwise pass every test in this file.
      http.post(`${ORIGIN}/api/auth/login`, async ({ request }) => {
        const body: unknown = await request.json()
        submitted.push(body)
        sessionLive = body !== null && typeof body === 'object' && 'password' in body
        return sessionLive
          ? new HttpResponse(null, { status: 204 })
          : HttpResponse.json(errorEnvelope('unauthorized', 'authentication required'), {
              status: 401,
            })
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
    expect(submitted).toEqual([{ password: 'hunter2' }])
  })

  it('renders the gate as the page’s main landmark', async () => {
    // App renders the screen *instead of* the shell, so nothing around it supplies a
    // landmark; the server-rendered form (`internal/httpapi/loginpage.go`) is a <main>.
    handlersForALiveSession()
    server.use(http.get(`${ORIGIN}/api/auth/status`, () => HttpResponse.json(authStatus)))

    renderApp(makeClient(), '/')

    // Wait for the gate itself: until the status answers, the shell's own <main> is up.
    const heading = await screen.findByRole('heading', { name: '비밀번호를 입력하세요' })
    const main = screen.getByRole('main')
    expect(main).toContainElement(heading)
    expect(main).toContainElement(within(main).getByLabelText('비밀번호'))
  })

  it('reports a rejected password in place, without losing the session screen', async () => {
    const user = userEvent.setup()
    const submitted: unknown[] = []
    handlersForALiveSession()
    server.use(
      http.get(`${ORIGIN}/api/auth/status`, () => HttpResponse.json(authStatus)),
      // What the real server answers a wrong password with (internal/httpapi/
      // authhandlers.go): the generic gate envelope, in English.
      http.post(`${ORIGIN}/api/auth/login`, async ({ request }) => {
        submitted.push(await request.json())
        return HttpResponse.json(errorEnvelope('unauthorized', 'authentication required'), {
          status: 401,
        })
      }),
    )

    renderApp(makeClient(), '/')

    await user.type(await screen.findByLabelText('비밀번호'), 'wrong')
    await user.click(screen.getByRole('button', { name: '로그인' }))

    // The server form's sentence (loginErrors.ts), never the envelope's English.
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('비밀번호가 올바르지 않습니다.')
    expect(alert).not.toHaveTextContent('authentication required')
    expect(submitted).toEqual([{ password: 'wrong' }])
    // Still the login screen, and still usable for a second attempt — with the
    // field focused again rather than focus left on <body>.
    expect(screen.getByRole('button', { name: '로그인' })).toBeInTheDocument()
    await waitFor(() => {
      expect(document.activeElement).toBe(screen.getByLabelText('비밀번호'))
    })
  })
})

/** A promise the test settles by hand, standing in for the login mutation. */
function deferred(): {
  promise: Promise<void>
  resolve: () => void
  reject: (reason: unknown) => void
} {
  let resolve: () => void = () => undefined
  let reject: (reason: unknown) => void = () => undefined
  const promise = new Promise<void>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })
  return { promise, resolve, reject }
}

const wrongPassword = (): ApiError =>
  new ApiError({ status: 401, code: 'unauthorized', message: 'authentication required' })
const tooManyAttempts = (): ApiError =>
  new ApiError({ status: 429, code: 'rate_limited', message: 'too many login attempts' })

describe('LoginScreen focus after a rejected password', () => {
  /**
   * While the request is in flight the field and the button are both `disabled`, so
   * whichever one held focus loses it. `pending` belongs to the parent (the mutation's
   * `isPending`) and can still be `true` when `onSubmit` rejects — the order below — so
   * the field cannot be focused from the rejection alone: a disabled input ignores
   * `focus()`. It must happen once the field is enabled again.
   */
  it.each([
    ['pressing Enter in the field', 'enter'],
    ['clicking 로그인', 'click'],
  ] as const)(
    'returns focus to the password field, text selected, after %s',
    async (_label, how) => {
      const user = userEvent.setup()
      const attempt = deferred()
      const onSubmit = (): Promise<void> => attempt.promise
      const { rerender } = render(<LoginScreen onSubmit={onSubmit} />)

      const field = screen.getByLabelText<HTMLInputElement>('비밀번호')
      await user.type(field, 'wrong')
      if (how === 'enter') {
        await user.keyboard('{Enter}')
      } else {
        await user.click(screen.getByRole('button', { name: '로그인' }))
      }
      rerender(<LoginScreen onSubmit={onSubmit} pending />)
      expect(field).toBeDisabled()

      // Rejected while the parent still reports the request as pending.
      await act(async () => {
        attempt.reject(
          new ApiError({ status: 401, code: 'unauthorized', message: 'authentication required' }),
        )
        await attempt.promise.catch(() => undefined)
      })
      expect(screen.getByRole('alert')).toHaveTextContent('비밀번호가 올바르지 않습니다.')

      rerender(<LoginScreen onSubmit={onSubmit} pending={false} />)

      expect(document.activeElement).toBe(field)
      // Selected, so the retyped password replaces the rejected one.
      expect(field.selectionStart).toBe(0)
      expect(field.selectionEnd).toBe('wrong'.length)
    },
  )

  /**
   * The other order: the parent re-enables the field first and the rejection lands
   * after. The field is already enabled, so it is the error arriving that must move
   * focus — the effect has to run on `error` as well as on `pending`.
   */
  it.each([
    ['pressing Enter in the field', 'enter'],
    ['clicking 로그인', 'click'],
  ] as const)(
    'returns focus to the password field, text selected, when pending clears before the rejection, after %s',
    async (_label, how) => {
      const user = userEvent.setup()
      const attempt = deferred()
      const onSubmit = (): Promise<void> => attempt.promise
      const { rerender } = render(<LoginScreen onSubmit={onSubmit} />)

      const field = screen.getByLabelText<HTMLInputElement>('비밀번호')
      await user.type(field, 'wrong')
      if (how === 'enter') {
        await user.keyboard('{Enter}')
      } else {
        await user.click(screen.getByRole('button', { name: '로그인' }))
      }
      rerender(<LoginScreen onSubmit={onSubmit} pending />)
      expect(field).toBeDisabled()

      // The parent reports the request finished before the rejection is delivered.
      rerender(<LoginScreen onSubmit={onSubmit} pending={false} />)
      expect(screen.queryByRole('alert')).not.toBeInTheDocument()

      await act(async () => {
        attempt.reject(wrongPassword())
        await attempt.promise.catch(() => undefined)
      })
      expect(screen.getByRole('alert')).toHaveTextContent('비밀번호가 올바르지 않습니다.')

      expect(document.activeElement).toBe(field)
      expect(field.selectionStart).toBe(0)
      expect(field.selectionEnd).toBe('wrong'.length)
    },
  )
})

describe('LoginScreen with two submissions in flight', () => {
  /**
   * `pending` is the parent's mutation state, not a lock: it reaches this component a
   * render later, so Enter pressed twice before that render sends two requests. `pending`
   * stays `false` throughout these tests — that is exactly the window in which the
   * second submit gets through. Only the latest attempt may speak: an older one settling
   * late must not replace the error shown, nor take focus and select the field — which
   * by then may hold a password the user has started retyping.
   */
  function overlapping(): {
    onSubmit: () => Promise<void>
    attempts: ReturnType<typeof deferred>[]
  } {
    const attempts: ReturnType<typeof deferred>[] = []
    const onSubmit = (): Promise<void> => {
      const attempt = deferred()
      attempts.push(attempt)
      return attempt.promise
    }
    return { onSubmit, attempts }
  }

  async function submitTwice(attempts: ReturnType<typeof deferred>[]): Promise<{
    a: ReturnType<typeof deferred>
    b: ReturnType<typeof deferred>
    field: HTMLInputElement
  }> {
    const user = userEvent.setup()
    const field = screen.getByLabelText<HTMLInputElement>('비밀번호')
    await user.type(field, 'wrong')
    await user.keyboard('{Enter}')
    await user.keyboard('{Enter}')
    expect(attempts).toHaveLength(2)
    const [a, b] = attempts
    if (a === undefined || b === undefined) throw new Error('two attempts expected')
    return { a, b, field }
  }

  it('ignores an older attempt’s rejection that lands after the newer one’s', async () => {
    const user = userEvent.setup()
    const { onSubmit, attempts } = overlapping()
    render(<LoginScreen onSubmit={onSubmit} />)
    const { a, b, field } = await submitTwice(attempts)

    // The newer attempt is rejected first, and the field takes focus with the text selected.
    await act(async () => {
      b.reject(tooManyAttempts())
      await b.promise.catch(() => undefined)
    })
    expect(screen.getByRole('alert')).toHaveTextContent(
      '로그인 시도가 너무 많습니다. 잠시 후 다시 시도하세요.',
    )
    expect(document.activeElement).toBe(field)

    // The user starts retyping — the selection is replaced, the caret sits at the end.
    await user.keyboard('new')
    expect(field.value).toBe('new')

    // Then the older attempt's rejection arrives.
    await act(async () => {
      a.reject(wrongPassword())
      await a.promise.catch(() => undefined)
    })

    expect(screen.getByRole('alert')).toHaveTextContent(
      '로그인 시도가 너무 많습니다. 잠시 후 다시 시도하세요.',
    )
    expect(field.value).toBe('new')
    expect(document.activeElement).toBe(field)
    expect(field.selectionStart).toBe('new'.length)
    expect(field.selectionEnd).toBe('new'.length)
  })

  it('changes nothing when an older attempt succeeds after the newer one was rejected', async () => {
    const user = userEvent.setup()
    const { onSubmit, attempts } = overlapping()
    render(<LoginScreen onSubmit={onSubmit} />)
    const { a, b, field } = await submitTwice(attempts)

    await act(async () => {
      b.reject(tooManyAttempts())
      await b.promise.catch(() => undefined)
    })
    await user.keyboard('new')

    await act(async () => {
      a.resolve()
      await a.promise
    })

    expect(screen.getByRole('alert')).toHaveTextContent(
      '로그인 시도가 너무 많습니다. 잠시 후 다시 시도하세요.',
    )
    expect(field.value).toBe('new')
    expect(document.activeElement).toBe(field)
    expect(field.selectionStart).toBe('new'.length)
    expect(field.selectionEnd).toBe('new'.length)
  })
})
