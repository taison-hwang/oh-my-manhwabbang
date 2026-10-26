import '@testing-library/jest-dom/vitest'

import { act, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useEffect, type ReactElement } from 'react'
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

function renderShell(
  data: Partial<ShellData> = {},
  initialPath = '/',
  viewer: ReactElement = <p>viewer</p>,
): ReturnType<typeof createMemoryRouter> {
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
          { path: 'series/:sid/books/:bid', element: viewer },
        ],
      },
    ],
    { initialEntries: [initialPath] },
  )
  const tree: ReactElement = <RouterProvider router={memory} />
  render(tree)
  return memory
}

/** How many times {@link ViewerMountProbe} has mounted; reset by the test that reads it. */
let viewerMounts = 0

/** The viewer route element, counting its mounts. */
function ViewerMountProbe() {
  useEffect(() => {
    viewerMounts += 1
  }, [])
  return <p>viewer</p>
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
    expect(screen.queryByRole('navigation')).toBeNull()
    expect(screen.queryByPlaceholderText('시리즈 검색 (초성 가능)')).toBeNull()
  })

  it('shows the shell once authenticated', () => {
    renderShell({ ...LOADED_ROOTS, auth: { auth_required: true, authenticated: true } })
    expect(screen.getByRole('navigation', { name: '라이브러리 탐색' })).toBeInTheDocument()
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
    expect(screen.queryByRole('navigation')).toBeNull()
    expect(screen.queryByPlaceholderText('시리즈 검색 (초성 가능)')).toBeNull()
    // The route still renders — onboarding is a screen, not a shell state.
    expect(screen.getByText('library')).toBeInTheDocument()
  })

  it('does not flash onboarding before /api/roots has answered', () => {
    renderShell({ rootsLoaded: false, roots: [] })
    expect(screen.getByRole('navigation', { name: '라이브러리 탐색' })).toBeInTheDocument()
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

  /**
   * **E-61.** The viewer covers the shell edge to edge, so the shell under it
   * stays mounted — not replaced — but is not displayed. Displayed, it served
   * only things nobody could see: Tab walked into the sidebar and the top bar
   * behind the viewer (26 of 40 presses at 1440), and the top bar, laid out
   * with its 라이브러리 button, stood a row taller than the library's at 1024
   * and 400 — so coming back out of a book moved it and the body under it by
   * 48–56px (CLS 0.110 / 0.062). The Layout Instability API does not care that
   * an opaque layer hid the move; a box that was not displayed only appears.
   * Rendered beside the shell, the viewer is outside the shell's <main>, so it
   * needs a main landmark of its own — `ViewerPage` declares none.
   */
  it('renders the viewer route over the shell rather than instead of it, with the shell not displayed (E-61)', () => {
    renderShell(LOADED_ROOTS, '/series/abc/books/def')
    const viewer = screen.getByText('viewer')
    expect(viewer).toBeVisible()
    // Beside the shell, not inside it …
    expect(viewer.closest('.shell')).toBeNull()
    // … and still in the main landmark, which the undisplayed shell's is not.
    expect(screen.getByRole('main')).toContainElement(viewer)
    // The shell: mounted …
    const nav = screen.getByRole('navigation', { name: '라이브러리 탐색', hidden: true })
    expect(nav).toBeInTheDocument()
    // … not displayed — `aria-hidden` alone would leave it laid out and focusable …
    expect(nav).not.toBeVisible()
    // … and out of reach: neither the sidebar nor the top bar is in the tree.
    expect(screen.queryByRole('navigation', { name: '라이브러리 탐색' })).toBeNull()
    expect(screen.queryByRole('searchbox')).toBeNull()
    expect(screen.queryByRole('button', { name: '라이브러리' })).toBeNull()
  })

  /**
   * The whole trip, library → book → next volume → page → library. Each node
   * survives it: the shell is not rebuilt on the way in or out, and the viewer
   * is mounted once, not again per volume or page.
   */
  it('keeps the same shell and the same viewer from the library through the viewer and back (E-61)', async () => {
    viewerMounts = 0
    const memory = renderShell(LOADED_ROOTS, '/', <ViewerMountProbe />)
    const nav = screen.getByRole('navigation', { name: '라이브러리 탐색' })
    const search = screen.getByRole('searchbox')

    await act(async () => {
      await memory.navigate('/series/abc/books/def')
    })
    expect(screen.getByText('viewer')).toBeVisible()
    expect(screen.getByRole('navigation', { name: '라이브러리 탐색', hidden: true })).toBe(nav)
    expect(screen.getByRole('searchbox', { hidden: true })).toBe(search)
    expect(nav).not.toBeVisible()
    expect(search).not.toBeVisible()

    await act(async () => {
      await memory.navigate('/series/abc/books/ghi')
    })
    await act(async () => {
      await memory.navigate('/series/abc/books/ghi?page=3')
    })
    expect(screen.getByText('viewer')).toBeVisible()
    expect(viewerMounts).toBe(1)

    await act(async () => {
      await memory.navigate('/')
    })
    expect(await screen.findByText('library')).toBeVisible()
    expect(screen.getByRole('navigation', { name: '라이브러리 탐색' })).toBe(nav)
    expect(screen.getByRole('searchbox')).toBe(search)
    expect(nav).toBeVisible()
    expect(search).toBeVisible()
  })
})

/**
 * One banner, one navigation, one main, each holding what its name says.
 *
 * The 10-23 accessibility audit found only the last: the top bar was a `<div>`
 * — inside `<main>`, where even a `<header>` is not a banner — and the sidebar
 * an `<aside>`, a complementary region, though all it holds is the way around
 * the library. A screen reader's landmark list read "main" and "라이브러리 탐색
 * (complementary)", with search, sort and 보기 방식 nowhere but inside main.
 * jsdom's role table does not apply the scoping rule, so "main does not hold
 * the banner" is asserted rather than assumed.
 */
describe('landmarks (a11y audit #6)', () => {
  it('gives the top bar the banner, the sidebar the navigation and the route the main', () => {
    renderShell(LOADED_ROOTS)
    const banner = screen.getByRole('banner')
    expect(within(banner).getByRole('searchbox')).toBeInTheDocument()
    expect(screen.getByRole('navigation', { name: '라이브러리 탐색' })).toBeInTheDocument()
    const main = screen.getByRole('main')
    expect(within(main).getByText('library')).toBeInTheDocument()
    expect(main).not.toContainElement(banner)
    expect(screen.queryByRole('complementary')).toBeNull()
  })

  it('keeps the icon rail a navigation landmark between 768 and 1023', () => {
    stubViewport(768)
    renderShell(LOADED_ROOTS)
    expect(screen.getByRole('navigation', { name: '라이브러리 탐색' })).toHaveClass('sidebar-rail')
  })

  it('carries the navigation into the drawer below 768', async () => {
    stubViewport(400)
    renderShell(LOADED_ROOTS)
    expect(screen.queryByRole('navigation')).toBeNull()

    await userEvent.click(screen.getByRole('button', { name: '라이브러리 탐색 열기' }))

    const drawer = screen.getByRole('dialog', { name: '라이브러리 탐색' })
    expect(within(drawer).getByRole('navigation', { name: '라이브러리 탐색' })).toBeInTheDocument()
  })
})

/**
 * The drawer is a modal (10-23 accessibility audit #2).
 *
 * It said `aria-modal="true"` and was nothing of the kind: Tab ran off its last
 * row into the page under the scrim — 22 of 40 presses at 400 landed on the
 * hamburger, the search box, the sort select and the cards — and from there Esc
 * did nothing, because only the panel listened for it. While it is open,
 * everything but the drawer is inert (`lib/modalLayer.ts`). jsdom has no
 * `inert`: it neither blocks focus nor filters roles, so the attribute is
 * asserted on the nodes, and the browser half is measured with Tab.
 */
describe('the drawer is modal (a11y audit #2)', () => {
  it('inerts the page under it, not itself or its scrim, and lets go on close', async () => {
    stubViewport(400)
    renderShell(LOADED_ROOTS)
    const hamburger = screen.getByRole('button', { name: '라이브러리 탐색 열기' })
    const search = screen.getByRole('searchbox')

    await userEvent.click(hamburger)
    const drawer = await screen.findByRole('dialog', { name: '라이브러리 탐색' })

    expect(search.closest('[inert]')).not.toBeNull()
    expect(hamburger.closest('[inert]')).not.toBeNull()
    expect(drawer.closest('[inert]')).toBeNull()
    // The scrim closes the drawer on a press; inert, it could not.
    expect(document.querySelector('.drawer-backdrop')?.closest('[inert]')).toBeNull()

    await userEvent.keyboard('{Escape}')

    expect(screen.queryByRole('dialog', { name: '라이브러리 탐색' })).toBeNull()
    expect(search.closest('[inert]')).toBeNull()
    expect(hamburger).toHaveFocus()
  })

  it('leaves one navigation reachable when it opens over the rail (768–1023)', async () => {
    stubViewport(900)
    renderShell(LOADED_ROOTS)
    const rail = screen.getByRole('navigation', { name: '라이브러리 탐색' })

    await userEvent.click(screen.getByRole('button', { name: '라이브러리 탐색 열기' }))
    const drawer = await screen.findByRole('dialog', { name: '라이브러리 탐색' })

    expect(rail.closest('[inert]')).not.toBeNull()
    const inDrawer = within(drawer).getByRole('navigation', { name: '라이브러리 탐색' })
    expect(inDrawer.closest('[inert]')).toBeNull()
  })
})

describe('the responsive layer (ui-spec §7, NFR-CMP-002)', () => {
  it('keeps the 240px sidebar with labels at 1440', () => {
    stubViewport(1440)
    renderShell(LOADED_ROOTS)
    const nav = screen.getByRole('navigation', { name: '라이브러리 탐색' })
    expect(nav).not.toHaveClass('sidebar-rail')
    expect(screen.getByText('01. mangga')).toBeInTheDocument()
  })

  it('collapses to the icon rail between 768 and 1023', () => {
    stubViewport(768)
    renderShell(LOADED_ROOTS)
    expect(screen.getByRole('navigation', { name: '라이브러리 탐색' })).toHaveClass(
      'sidebar-rail',
    )
  })

  it('keeps the full sidebar at 1024', () => {
    stubViewport(1024)
    renderShell(LOADED_ROOTS)
    expect(screen.getByRole('navigation', { name: '라이브러리 탐색' })).not.toHaveClass(
      'sidebar-rail',
    )
  })

  it('opens the full sidebar as an overlay drawer over the rail (768–1023)', async () => {
    stubViewport(900)
    renderShell(LOADED_ROOTS)
    const rail = screen.getByRole('navigation', { name: '라이브러리 탐색' })
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
  /** An OS that prefers dark; every other query answers no. */
  function stubOsPrefersDark(): void {
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
  }

  it('writes the user setting onto <html> on mount', () => {
    // The setting is light against a dark OS, the opposite of the next case,
    // so each answer is reachable only by its own path: an App that painted
    // dark whatever the setting — or passed `system` and let the OS decide —
    // reads dark here.
    stubOsPrefersDark()
    useUiStore.setState({ theme: 'light' })
    renderShell(LOADED_ROOTS)
    expect(document.documentElement.getAttribute('data-theme')).toBe('light')
  })

  it('resolves "system" against the OS rather than writing it literally', () => {
    stubOsPrefersDark()
    useUiStore.setState({ theme: 'system' })
    renderShell(LOADED_ROOTS)
    expect(document.documentElement.getAttribute('data-theme')).toBe('dark')
  })
})
