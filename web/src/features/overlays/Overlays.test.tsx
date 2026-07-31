import '@testing-library/jest-dom/vitest'

import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { setupServer } from 'msw/node'
import { MemoryRouter } from 'react-router-dom'
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'

import {
  cacheUsage,
  continueResponse,
  ORIGIN,
  rootsResponse,
  scanLogResponse,
  settings,
} from '../../api/fixtures'
import type { Settings } from '../../api/types'
import { resetBasePath } from '../../api/urls'
import { useUiStore } from '../../store/ui'
import { Overlays } from './Overlays'

/**
 * Overlays are **state, not routes** (impl-plan §5.2): `store/ui.ts` owns the
 * open set and this component is the whole of the shell's mounting cost. The
 * `Esc` ladder in `lib/useHotkeys.ts` pops that same stack, so a dialog that
 * were a route would put a modal in the back-button history.
 */

const server = setupServer()

/**
 * The four requests `SettingsDialog`'s panels make, in the order this file
 * registers recorders for them: `RootsPanel` (`useRoots` + the A-10
 * `useSettings`), `CachePanel`, `ReadDefaultsPanel` (a second `useSettings`,
 * deduplicated by the query key) and `ScanLogPanel`.
 */
const SETTINGS_PATHS = ['/api/roots', '/api/settings', '/api/cache/usage', '/api/scan/log']

/**
 * Every settings request MSW saw, appended by the handlers themselves — the
 * `seriesRequests` pattern of `features/library/library.test.tsx`.
 *
 * A recorder is the only thing that can assert about a request that *did not*
 * happen. `onUnhandledRequest: 'error'` cannot: `msw/node` fails the **request**
 * — the fetch rejects and React Query swallows it into an error state nothing
 * here reads — so it never fails the test. Measured, not assumed: adding
 * `useSettings()` to `Overlays.tsx` left this file at 5/5 and exit 0 while MSW
 * printed `intercepted a request without a matching request handler` to stderr.
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
  server.listen({ onUnhandledRequest: 'error' })
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
    // produce all four. Without it `toEqual([])` would pass just as happily
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
    )
    renderOverlays()
    act(() => {
      useUiStore.getState().openOverlay('settings')
    })
    expect(await screen.findByText(configPath)).toBeInTheDocument()
  })
})
