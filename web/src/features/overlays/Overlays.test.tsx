import '@testing-library/jest-dom/vitest'

import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { setupServer } from 'msw/node'
import { MemoryRouter } from 'react-router-dom'
import {
  afterAll,
  afterEach,
  beforeAll,
  beforeEach,
  describe,
  expect,
  it,
  onTestFinished,
} from 'vitest'

import {
  cacheUsage,
  continueResponse,
  ORIGIN,
  rootsResponse,
  scanLogResponse,
  scanStatusIdle,
  settings,
} from '../../api/fixtures'
import type { Settings } from '../../api/types'
import { resetBasePath } from '../../api/urls'
import { useGlobalHotkeys } from '../../lib/useHotkeys'
import { useUiStore } from '../../store/ui'
import { Overlays } from './Overlays'
import { failUnhandled } from '../../test/msw'

/**
 * Overlays are **state, not routes** (impl-plan §5.2): `store/ui.ts` owns the
 * open set and this component is the whole of the shell's mounting cost. The
 * `Esc` ladder in `lib/useHotkeys.ts` pops that same stack, so a dialog that
 * were a route would put a modal in the back-button history.
 */

const server = setupServer()

/**
 * The five requests `SettingsDialog`'s panels make, in the order this file
 * registers recorders for them: `RootsPanel` (`useRoots`, the A-10 `useSettings`
 * and — since the panel grew the FR-IDX-004 progress block — `useScanStatus`),
 * `CachePanel`, `ReadDefaultsPanel` (a second `useSettings`, deduplicated by the
 * query key) and `ScanLogPanel`.
 *
 * `/api/scan/status` is listed here rather than exempted: it is the one that
 * *polls*, so "not mounted while closed" is worth more for it than for any of
 * the others.
 */
const SETTINGS_PATHS = [
  '/api/roots',
  '/api/settings',
  '/api/cache/usage',
  '/api/scan/log',
  '/api/scan/status',
]

/**
 * Every settings request MSW saw, appended by the handlers themselves — the
 * `seriesRequests` pattern of `features/library/library.test.tsx`.
 *
 * A recorder is the only thing that can assert about a request that *did not*
 * happen. The unhandled-request check (`src/test/msw.ts`) fails a test for a
 * request no handler answers, but a settings request this file's handlers do
 * answer is invisible to it — and before that check, `onUnhandledRequest:
 * 'error'` failed only the request: adding `useSettings()` to `Overlays.tsx`
 * left this file at 5/5 while MSW printed the warning to stderr.
 */
let settingsRequests: string[] = []

/** Handlers that answer the four settings requests *and* log them. */
function recordSettingsRequests(): void {
  server.use(
    http.get(`${ORIGIN}/api/roots`, () => {
      settingsRequests.push('/api/roots')
      return HttpResponse.json(rootsResponse)
    }),
    http.get(`${ORIGIN}/api/settings`, () => {
      settingsRequests.push('/api/settings')
      return HttpResponse.json(settings)
    }),
    http.get(`${ORIGIN}/api/cache/usage`, () => {
      settingsRequests.push('/api/cache/usage')
      return HttpResponse.json(cacheUsage)
    }),
    http.get(`${ORIGIN}/api/scan/log`, () => {
      settingsRequests.push('/api/scan/log')
      return HttpResponse.json(scanLogResponse)
    }),
    http.get(`${ORIGIN}/api/scan/status`, () => {
      settingsRequests.push('/api/scan/status')
      return HttpResponse.json(scanStatusIdle)
    }),
  )
}

/**
 * Yields long enough for a request started by a mount effect to reach MSW.
 *
 * How long that is cannot be asserted, only demonstrated, so the test below
 * runs the *same* flush over a mount that does fetch: if one macrotask turn
 * were too short, the positive control would fail rather than the negative one
 * silently passing.
 */
async function flushPendingRequests(): Promise<void> {
  await act(async () => {
    await new Promise((resolve) => {
      setTimeout(resolve, 0)
    })
  })
}

beforeAll(() => {
  server.listen({ onUnhandledRequest: failUnhandled })
})
afterEach(() => {
  server.resetHandlers()
  resetBasePath()
})
afterAll(() => {
  server.close()
})

beforeEach(() => {
  localStorage.clear()
  settingsRequests = []
  useUiStore.setState({ overlays: [], paletteQuery: '' })
  server.use(http.get(`${ORIGIN}/api/continue`, () => HttpResponse.json(continueResponse)))
})

/** The shell's real key dispatcher, so an `Esc` reaches window the way it does in the app. */
function HotkeyHost() {
  useGlobalHotkeys()
  return null
}

/**
 * The overlays as the shell mounts them: beside the global dispatcher, over a
 * page with something to have opened them. Returns that page control.
 */
function renderShell(): HTMLElement {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/']}>
        <HotkeyHost />
        <button type="button">배경</button>
        <Overlays />
      </MemoryRouter>
    </QueryClientProvider>,
  )
  return screen.getByRole('button', { name: '배경' })
}

function renderOverlays(): void {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/']}>
        <Overlays />
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

describe('Overlays', () => {
  it('renders nothing while the store holds no open overlay', () => {
    renderOverlays()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('opens the shortcuts dialog from the store', () => {
    renderOverlays()
    act(() => {
      useUiStore.getState().openOverlay('shortcuts')
    })
    expect(screen.getByRole('dialog')).toHaveAccessibleName(
      expect.stringContaining('키보드 단축키') as unknown as string,
    )
  })

  it('opens the palette from the store and closes it again', async () => {
    renderOverlays()
    act(() => {
      useUiStore.getState().openOverlay('palette')
    })
    expect(await screen.findByPlaceholderText('시리즈로 이동…')).toBeInTheDocument()

    act(() => {
      useUiStore.getState().closeOverlay('palette')
    })
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('does not mount the settings panels — and their queries — while closed', async () => {
    // The subject is the *requests*. A heading is a proxy for them at best: a
    // query can fire from a component that renders nothing at all, which is
    // precisely the regression this test is named for, and `queryByText` is
    // blind to it. So the handlers are recorders and the log is the assertion.
    recordSettingsRequests()
    renderOverlays()
    await flushPendingRequests()

    expect(settingsRequests).toEqual([])
    expect(screen.queryByText('루트 관리')).not.toBeInTheDocument()

    // Positive control, same recorders and same flush: opening the dialog must
    // produce all five. Without it `toEqual([])` would pass just as happily
    // against a recorder that records nothing, or a flush that returns too soon.
    act(() => {
      useUiStore.getState().openOverlay('settings')
    })
    await flushPendingRequests()
    expect([...settingsRequests].sort()).toEqual([...SETTINGS_PATHS].sort())
    await waitFor(() => {
      expect(screen.getByText('루트 관리')).toBeInTheDocument()
    })
  })

  /**
   * Amendment A-10 / ruling E-25, through the mount path that actually ships.
   *
   * This is where the defect lived: `SettingsDialog` and `RootsPanel` have
   * carried a `configPath` prop since WP-10 and `Overlays` — the only thing that
   * mounts the dialog in the app — has never had a value to pass it, so every
   * real user saw the note with no file name. Nothing below passes a prop; the
   * value has to travel `GET /api/settings` → `useSettings` → the panel.
   */
  it('shows the resolved config path when the settings overlay opens', async () => {
    const configPath = '/etc/shelf/shelf.yaml'
    server.use(
      http.get(`${ORIGIN}/api/roots`, () => HttpResponse.json(rootsResponse)),
      http.get(`${ORIGIN}/api/settings`, () =>
        HttpResponse.json({
          ...settings,
          server: { ...settings.server, config_path: configPath },
        } satisfies Settings),
      ),
      http.get(`${ORIGIN}/api/cache/usage`, () => HttpResponse.json(cacheUsage)),
      http.get(`${ORIGIN}/api/scan/log`, () => HttpResponse.json(scanLogResponse)),
      http.get(`${ORIGIN}/api/scan/status`, () => HttpResponse.json(scanStatusIdle)),
    )
    renderOverlays()
    act(() => {
      useUiStore.getState().openOverlay('settings')
    })
    expect(await screen.findByText(configPath)).toBeInTheDocument()
  })

  /** Audit MED, through the store: picking a row must take the palette off the stack. */
  it('takes the palette off the overlay stack when a row is picked', async () => {
    renderOverlays()
    act(() => {
      useUiStore.getState().openOverlay('palette')
    })
    await userEvent.click(await screen.findByRole('option', { name: /군계/ }))
    expect(useUiStore.getState().overlays).not.toContain('palette')
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  /**
   * Audit #8 with two overlays at once: the palette opened over settings.
   *
   * Only the top layer is live — the page *and* the settings dialog under the
   * palette are inert — and `Esc` unwinds one layer at a time, each time handing
   * focus back to the element that opened that layer, after that element has
   * left the inert set (read inside its own `focus` listener: jsdom lets focus
   * land inside an inert subtree, so `toHaveFocus()` alone cannot see the order).
   *
   * The real global dispatcher is mounted: "one layer at a time" is a claim
   * about the dialog's own `Esc` *and* the window ladder together — without it
   * a keystroke both of them handle would still pop exactly one here.
   */
  it('unwinds the palette over settings one Esc at a time, with inert following the top', async () => {
    recordSettingsRequests()
    const background = renderShell()
    background.focus()

    act(() => {
      useUiStore.getState().openOverlay('settings')
    })
    await screen.findByText('루트 관리')
    const close = screen.getByRole('button', { name: '닫기' })
    expect(close).toHaveFocus()
    expect(background.closest('[inert]')).not.toBeNull()

    act(() => {
      useUiStore.getState().openOverlay('palette')
    })
    const input = await screen.findByPlaceholderText('시리즈로 이동…')
    expect(input).toHaveFocus()
    expect(input.closest('[inert]')).toBeNull()
    expect(close.closest('[inert]')).not.toBeNull()
    expect(background.closest('[inert]')).not.toBeNull()

    const inertWhenFocused = new Map<Element, Element | null>()
    for (const el of [close, background]) {
      el.addEventListener('focus', () => {
        inertWhenFocused.set(el, el.closest('[inert]'))
      })
    }

    await userEvent.keyboard('{Escape}')
    expect(screen.queryByPlaceholderText('시리즈로 이동…')).not.toBeInTheDocument()
    expect(useUiStore.getState().overlays).toEqual(['settings'])
    expect(close).toHaveFocus()
    expect(inertWhenFocused.get(close)).toBeNull()
    expect(close.closest('[inert]')).toBeNull()
    expect(background.closest('[inert]')).not.toBeNull()

    await userEvent.keyboard('{Escape}')
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(useUiStore.getState().overlays).toEqual([])
    expect(background).toHaveFocus()
    expect(inertWhenFocused.get(background)).toBeNull()
    expect(document.querySelector('[inert]')).toBeNull()
  })

  /**
   * Review #2, keystroke for keystroke: settings → `Ctrl+K` → a result focused →
   * `?` opens the shortcuts dialog from the palette → `Ctrl+K` closes the palette
   * *underneath* it → `Esc`. The shortcuts dialog's opener left with the
   * palette, and settings is still modal, so focus goes into settings — not to
   * `<body>`, and not, while the shortcuts dialog is up, to the inert settings.
   */
  it('hands focus to settings when the layer that opened the shortcuts dialog closed under it', async () => {
    recordSettingsRequests()
    const background = renderShell()
    background.focus()

    act(() => {
      useUiStore.getState().openOverlay('settings')
    })
    await screen.findByText('루트 관리')
    const close = screen.getByRole('button', { name: '닫기' })
    expect(close).toHaveFocus()

    // Every focus from here on, and whether it landed in an inert subtree —
    // jsdom lets it, and React's post-commit focus restore would then hide it.
    const intoInert: string[] = []
    const onFocus = (e: FocusEvent): void => {
      if (e.target instanceof Element && e.target.closest('[inert]') !== null) {
        intoInert.push(e.target.outerHTML.slice(0, 80))
      }
    }
    document.addEventListener('focus', onFocus, true)
    onTestFinished(() => {
      document.removeEventListener('focus', onFocus, true)
    })

    await userEvent.keyboard('{Control>}k{/Control}')
    const result = await screen.findByRole('option', { name: /군계/ })
    result.focus()
    await userEvent.keyboard('?')
    expect(useUiStore.getState().overlays).toEqual(['settings', 'palette', 'shortcuts'])
    const shortcuts = screen.getByRole('dialog', { name: '키보드 단축키' })
    expect(shortcuts).toHaveFocus()

    await userEvent.keyboard('{Control>}k{/Control}')
    expect(useUiStore.getState().overlays).toEqual(['settings', 'shortcuts'])
    expect(shortcuts).toHaveFocus()

    let inertWhenFocused: Element | null | undefined
    close.addEventListener('focus', () => {
      inertWhenFocused = close.closest('[inert]')
    })
    await userEvent.keyboard('{Escape}')
    expect(useUiStore.getState().overlays).toEqual(['settings'])
    expect(close).toHaveFocus()
    expect(inertWhenFocused).toBeNull()
    expect(background.closest('[inert]')).not.toBeNull()
    expect(intoInert).toEqual([])
  })
})
