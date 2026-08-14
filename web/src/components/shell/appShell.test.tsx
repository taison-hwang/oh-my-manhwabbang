import '@testing-library/jest-dom/vitest'

import { act, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactElement } from 'react'
import { RouterProvider, createMemoryRouter } from 'react-router-dom'
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'

import { App } from '../../App'
import { DEFAULT_SHELL_DATA, ShellDataContext, type ShellData } from '../../lib/shellData'
import { router } from '../../router'
import { useUiStore } from '../../store/ui'

/**
 * The shell as a whole: the auth gate, the onboarding gate, the responsive
 * tiers and the routes. WP-05 acceptance 3, 6 and 7.
 */

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

function renderShell(data: Partial<ShellData> = {}, initialPath = '/'): void {
  const value: ShellData = { ...DEFAULT_SHELL_DATA, ...data }
  const memory = createMemoryRouter(
    [
      {
        path: '/',
        element: (
          <ShellDataContext.Provider value={value}>
            <App />
          </ShellDataContext.Provider>
        ),
        children: [
          { index: true, element: <p>library</p> },
          { path: 'series/:sid', element: <p>series</p> },
          { path: 'series/:sid/books/:bid', element: <p>viewer</p> },
        ],
      },
    ],
    { initialEntries: [initialPath] },
  )
  const tree: ReactElement = <RouterProvider router={memory} />
  render(tree)
}

/**
 * jsdom/undici interop, not a product concern.
 *
 * React Router v7 builds a `Request` for every client-side navigation and hands
 * it an `AbortSignal`. Under the jsdom environment `AbortSignal` is jsdom's
 * while `Request` is Node's undici, and undici's `instanceof` check rejects the
 * foreign signal — every `navigate()` in a test throws. Both globals are native
 * and consistent in a real browser, so the narrowest fix is to drop the signal
 * here rather than to reshape the app around a test-environment artefact.
 */
beforeAll(() => {
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

const LOADED_ROOTS: Pick<ShellData, 'roots' | 'rootsLoaded'> = {
  rootsLoaded: true,
  roots: [
    { name: '01. mangga', label: '01. mangga', series_count: 21, available: true, pending: false },
  ],
}

beforeEach(() => {
  stubViewport(1440)
  localStorage.clear()
  useUiStore.setState({
    theme: 'system',
    drawerOpen: false,
    overlays: [],
    scope: 'all',
    query: '',
    // The E-34 §2 instruction is one-shot and nothing in this file mounts the
    // library that would consume it, so a test that arms it would leave it
    // armed for whatever ran next.
    revealSeries: null,
  })
})

afterEach(() => {
  document.documentElement.removeAttribute('data-theme')
})

describe('the auth gate (NFR-SEC-002)', () => {
  it('replaces the whole shell with the login screen when auth is required', () => {
    renderShell({
      ...LOADED_ROOTS,
      auth: { auth_required: true, authenticated: false },
    })
    expect(screen.getByRole('heading', { name: '비밀번호를 입력하세요' })).toBeInTheDocument()
    // Auth is all-or-nothing: no chrome leaks past it.
    expect(screen.queryByRole('complementary')).toBeNull()
    expect(screen.queryByPlaceholderText('시리즈 검색 (초성 가능)')).toBeNull()
  })

  it('shows the shell once authenticated', () => {
    renderShell({ ...LOADED_ROOTS, auth: { auth_required: true, authenticated: true } })
    expect(screen.getByRole('complementary', { name: '라이브러리 탐색' })).toBeInTheDocument()
  })

  it('surfaces a rejected login as an alert without unmounting the form', async () => {
    renderShell({
      ...LOADED_ROOTS,
      auth: { auth_required: true, authenticated: false },
      login: () => Promise.reject(new Error('비밀번호가 올바르지 않습니다.')),
    })
    await userEvent.type(screen.getByLabelText('비밀번호'), 'nope')
    await userEvent.click(screen.getByRole('button', { name: '로그인' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('비밀번호가 올바르지 않습니다.')
  })
})

describe('the onboarding gate (ui-spec §4.6)', () => {
  it('drops the chrome entirely when there are no roots', () => {
    renderShell({ rootsLoaded: true, roots: [] })
    expect(screen.queryByRole('complementary')).toBeNull()
    expect(screen.queryByPlaceholderText('시리즈 검색 (초성 가능)')).toBeNull()
    // The route still renders — onboarding is a screen, not a shell state.
    expect(screen.getByText('library')).toBeInTheDocument()
  })

  it('does not flash onboarding before /api/roots has answered', () => {
    renderShell({ rootsLoaded: false, roots: [] })
    expect(screen.getByRole('complementary', { name: '라이브러리 탐색' })).toBeInTheDocument()
  })
})

describe('routing (ui-spec §3)', () => {
  it('declares exactly the three specified paths', () => {
    const root = router.routes[0]
    expect(root?.path).toBe('/')
    const children = root?.children ?? []
    expect(children.map((c) => ('path' in c ? (c.path ?? '') : ''))).toEqual([
      '',
      'series/:sid',
      'series/:sid/books/:bid',
    ])
  })

  it('keeps palette / settings / shortcuts out of the route table — they are state', () => {
    const paths = (router.routes[0]?.children ?? []).map((c) => c.path ?? '')
    expect(paths).not.toContain('settings')
    expect(paths).not.toContain('shortcuts')
    expect(paths).not.toContain('palette')
  })

  it('shows the back button only on a series route and returns to the library', async () => {
    renderShell(LOADED_ROOTS, '/series/abc')
    expect(screen.getByText('series')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '라이브러리' }))
    expect(await screen.findByText('library')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '라이브러리' })).toBeNull()
  })

  /**
   * E-34 §2 from the series detail screen.
   *
   * The ruling's reveal was wired into the viewer's own 라이브러리 button and
   * nowhere else, so this one — the commoner path back — dropped the reader at
   * the top of the collection every time. The scroll and the focus themselves
   * belong to the two library surfaces and are asserted against a real
   * virtualiser in `features/viewer/ViewerPage.test.tsx`; what is this screen's
   * to get right is arming the instruction with the series being left, and
   * arming *only* that.
   */
  it('arms the E-34 reveal with the series it is leaving', async () => {
    renderShell(LOADED_ROOTS, '/series/abc')
    expect(useUiStore.getState().revealSeries).toBeNull()

    await userEvent.click(screen.getByRole('button', { name: '라이브러리' }))

    expect(await screen.findByText('library')).toBeInTheDocument()
    expect(useUiStore.getState().revealSeries).toBe('abc')
  })

  it('leaves the sidebar filter and the search box alone doing it (E-34 §1)', async () => {
    // Both are A-5 write-backs — `useLibrarySettingsSync` PUTs `library_scope`
    // to the server — so a button that "cleared the filters" to widen the shelf
    // would unset the reader's sidebar choice on every machine, permanently.
    useUiStore.setState({ scope: '01. mangga', query: '군계' })
    renderShell(LOADED_ROOTS, '/series/abc')

    await userEvent.click(screen.getByRole('button', { name: '라이브러리' }))

    await screen.findByText('library')
    expect(useUiStore.getState().scope).toBe('01. mangga')
    expect(useUiStore.getState().query).toBe('군계')
  })

  it('renders the viewer route over the shell rather than instead of it', () => {
    renderShell(LOADED_ROOTS, '/series/abc/books/def')
    expect(screen.getByText('viewer')).toBeInTheDocument()
    expect(screen.getByRole('complementary', { name: '라이브러리 탐색' })).toBeInTheDocument()
  })
})

describe('the responsive layer (ui-spec §7, NFR-CMP-002)', () => {
  it('keeps the 240px sidebar with labels at 1440', () => {
    stubViewport(1440)
    renderShell(LOADED_ROOTS)
    const nav = screen.getByRole('complementary', { name: '라이브러리 탐색' })
    expect(nav).not.toHaveClass('sidebar-rail')
    expect(screen.getByText('01. mangga')).toBeInTheDocument()
  })

  it('collapses to the icon rail between 768 and 1023', () => {
    stubViewport(768)
    renderShell(LOADED_ROOTS)
    expect(screen.getByRole('complementary', { name: '라이브러리 탐색' })).toHaveClass(
      'sidebar-rail',
    )
  })

  it('keeps the full sidebar at 1024', () => {
    stubViewport(1024)
    renderShell(LOADED_ROOTS)
    expect(screen.getByRole('complementary', { name: '라이브러리 탐색' })).not.toHaveClass(
      'sidebar-rail',
    )
  })

  it('opens the full sidebar as an overlay drawer over the rail (768–1023)', async () => {
    stubViewport(900)
    renderShell(LOADED_ROOTS)
    const rail = screen.getByRole('complementary', { name: '라이브러리 탐색' })
    expect(rail).toHaveClass('sidebar-rail')
    // The rail carries no root name and no count — every icon is the same
    // FolderOpen, so without the drawer the three real roots are one blur.
    expect(within(rail).queryByText('21')).toBeNull()

    await userEvent.click(screen.getByRole('button', { name: '라이브러리 탐색 열기' }))
    const drawer = await screen.findByRole('dialog', { name: '라이브러리 탐색' })
    expect(within(drawer).getByText('01. mangga')).toBeVisible()
    expect(within(drawer).getByText('21')).toBeVisible()
  })

  it('keeps the drawer trigger reachable at every tier below 1024', () => {
    stubViewport(900)
    renderShell(LOADED_ROOTS)
    const trigger = screen.getByRole('button', { name: '라이브러리 탐색 열기' })
    // jsdom applies no media query, so the visibility rule can only be pinned on
    // the class itself: `md:hidden` measures 0x0 at 768–1023, which is exactly
    // the tier whose sidebar is a nameless 56px rail (ui-spec §7).
    expect(trigger).toHaveClass('lg:hidden')
    expect(trigger).not.toHaveClass('md:hidden')
  })

  it('mounts no drawer at 1024 and up — the full sidebar is already on screen', () => {
    stubViewport(1440)
    renderShell(LOADED_ROOTS)
    act(() => {
      useUiStore.setState({ drawerOpen: true })
    })
    expect(screen.queryByRole('dialog', { name: '라이브러리 탐색' })).toBeNull()
  })

  it('moves the sidebar into an off-canvas drawer below 768', async () => {
    stubViewport(400)
    renderShell(LOADED_ROOTS)
    // Closed by default.
    expect(screen.queryByRole('dialog', { name: '라이브러리 탐색' })).toBeNull()
    await userEvent.click(screen.getByRole('button', { name: '라이브러리 탐색 열기' }))
    const drawer = await screen.findByRole('dialog', { name: '라이브러리 탐색' })
    expect(drawer).toHaveClass('drawer-panel')
    expect(screen.getByRole('button', { name: /01\. mangga/ })).toBeInTheDocument()
  })

  it('closes the drawer when a scope is chosen — it covers the result otherwise', async () => {
    stubViewport(400)
    renderShell(LOADED_ROOTS)
    await userEvent.click(screen.getByRole('button', { name: '라이브러리 탐색 열기' }))
    await userEvent.click(await screen.findByRole('button', { name: /읽는 중/ }))
    expect(screen.queryByRole('dialog', { name: '라이브러리 탐색' })).toBeNull()
    expect(useUiStore.getState().scope).toBe('reading')
  })
})

describe('theme (NFR-CMP-003)', () => {
  it('writes the user setting onto <html> on mount', () => {
    useUiStore.setState({ theme: 'dark' })
    renderShell(LOADED_ROOTS)
    expect(document.documentElement.getAttribute('data-theme')).toBe('dark')
  })

  it('resolves "system" against the OS rather than writing it literally', () => {
    Object.defineProperty(window, 'matchMedia', {
      writable: true,
      configurable: true,
      value: (query: string): FakeMql => ({
        matches: query.includes('prefers-color-scheme: dark'),
        media: query,
        addEventListener: () => undefined,
        removeEventListener: () => undefined,
      }),
    })
    useUiStore.setState({ theme: 'system' })
    renderShell(LOADED_ROOTS)
    expect(document.documentElement.getAttribute('data-theme')).toBe('dark')
  })
})
