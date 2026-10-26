import '@testing-library/jest-dom/vitest'

import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { http, HttpResponse } from 'msw'
import { setupServer } from 'msw/node'
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'

import { ORIGIN, settings as settingsFixture } from '../../api/fixtures'
import { queryKeys } from '../../api/queries'
import type { Settings, SettingsUpdate } from '../../api/types'
import { SETTINGS_SAVE_DEBOUNCE_MS } from '../../api/settingsWriter'
import { resetBasePath } from '../../api/urls'
import { useUiStore } from '../../store/ui'
import { ReadDefaultsPanel } from '../overlays/ReadDefaultsPanel'
import { useLibrarySettingsSync, useThemeSettingsSync } from './useLibrary'
import { failUnhandled } from '../../test/msw'

/**
 * Item bq — `theme` hydrates from `GET /api/settings` so it follows the reader
 * across devices (arch §7.8: the server persists `theme`; `store/ui.ts`: the
 * server is authoritative once it answers).
 *
 * Asserted against the **requests sent** as well as the store, for the reason
 * `library.test.tsx`'s A-5 block gives: hydrating and PUTting back put the same
 * value on screen, and only the request list tells them apart.
 *
 * Every write goes through `api/settingsWriter.ts`: debounced, one on the wire at
 * a time, and no echo older than the latest pick reaches the cache the store
 * hydrates from.
 *
 * Two hooks share the payload: `useThemeSettingsSync` hydrates `theme`, and
 * `useLibrarySettingsSync` the four library fields. `ShellDataProvider` mounts
 * both, on every route, in that order in one component — so the theme's effect
 * runs first on every commit. `Harness` mounts them the same way, above
 * everything else as the provider is; that the provider really mounts them is
 * `router.test.tsx`'s to prove.
 */

let serverSettings: Settings
let puts: SettingsUpdate[]
/** When set, each `PUT` is applied on arrival but its response is held here. */
let holdPuts: boolean
let held: { body: SettingsUpdate; release: () => void }[]

const server = setupServer(
  http.get(`${ORIGIN}/api/settings`, () => HttpResponse.json(serverSettings)),
  http.put(`${ORIGIN}/api/settings`, async ({ request }) => {
    const update = (await request.json()) as SettingsUpdate
    puts.push(update)
    // Applied in arrival order, answered whenever the test says so: the echo
    // carries the server state *as of this request*, which is what goes stale.
    serverSettings = { ...serverSettings, ...update }
    const snapshot = serverSettings
    if (holdPuts) {
      await new Promise<void>((resolve) => {
        held.push({ body: update, release: resolve })
      })
    }
    return HttpResponse.json(snapshot)
  }),
)

beforeAll(() => {
  server.listen({ onUnhandledRequest: failUnhandled })
})
afterEach(() => {
  cleanup()
  server.resetHandlers()
  resetBasePath()
  document.documentElement.removeAttribute('data-theme')
})
afterAll(() => {
  server.close()
})

beforeEach(() => {
  serverSettings = { ...settingsFixture }
  puts = []
  holdPuts = false
  held = []
  localStorage.clear()
  useUiStore.setState({
    theme: 'system',
    view: 'grid',
    scope: 'all',
    sort: 'name',
    order: 'asc',
  })
})

interface Mounts {
  /** `ShellDataProvider`'s theme sync. */
  theme?: boolean
  /** `ShellDataProvider`'s library sync. */
  library?: boolean
}

/**
 * The provider's two syncs, theme first, in one component — as `ShellDataProvider`
 * has them: effects run in declaration order, so the theme's runs first on every
 * commit, and both run after everything below them.
 */
function ShellSync({ children }: { children: ReactNode }) {
  useThemeSettingsSync()
  useLibrarySettingsSync()
  return children
}

/** The theme sync alone, for a test that turns the library's off. */
function ThemeSync({ children }: { children: ReactNode }) {
  useThemeSettingsSync()
  return children
}

/** The library sync alone, for a test that turns the theme's off. */
function LibrarySync({ children }: { children: ReactNode }) {
  useLibrarySettingsSync()
  return children
}

function Harness({ panel, theme, library }: { panel: boolean } & Required<Mounts>) {
  const below = panel ? <ReadDefaultsPanel /> : null
  if (theme && library) return <ShellSync>{below}</ShellSync>
  if (theme) return <ThemeSync>{below}</ThemeSync>
  if (library) return <LibrarySync>{below}</LibrarySync>
  return below
}

function renderSync(panel = false, { theme = true, library = true }: Mounts = {}): QueryClient {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={client}>
      <Harness panel={panel} theme={theme} library={library} />
    </QueryClientProvider>,
  )
  return client
}

/**
 * Long enough for a `PUT` started by a change to reach MSW and be recorded: the
 * writer's debounce, then a margin for the round trip.
 */
async function settle(): Promise<void> {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, SETTINGS_SAVE_DEBOUNCE_MS + 50))
  })
}

/** light → dark → system → light → dark → system, as labels and wire values. */
const SWEEP = [
  ['다크', 'dark'],
  ['시스템', 'system'],
  ['라이트', 'light'],
  ['다크', 'dark'],
  ['시스템', 'system'],
] as const

describe('theme hydration (item bq)', () => {
  it('lets the server theme win over a stale local copy, without writing it back', async () => {
    // The local copy is what `persist` restored from `shelf.ui` — this device's
    // last choice. Another device has since set `dark` on the server.
    useUiStore.setState({ theme: 'light' })
    serverSettings = { ...settingsFixture, theme: 'dark' }

    renderSync()

    await waitFor(() => {
      expect(useUiStore.getState().theme).toBe('dark')
    })
    expect(document.documentElement).toHaveAttribute('data-theme', 'dark')
    await settle()
    // Hydration is read-only: no echo of the value it just adopted.
    expect(puts).toEqual([])
  })

  it('hydrates the theme with the theme hook alone, without writing it back', async () => {
    // In isolation: the provider mounts both hooks on every route, so this proves
    // the theme hook needs nothing from the library sync, not what a route mounts.
    useUiStore.setState({ theme: 'light' })
    serverSettings = { ...settingsFixture, theme: 'dark' }

    renderSync(false, { library: false })

    await waitFor(() => {
      expect(useUiStore.getState().theme).toBe('dark')
    })
    expect(document.documentElement).toHaveAttribute('data-theme', 'dark')
    await settle()
    expect(puts).toEqual([])
  })

  it('leaves the theme to the theme hook: the library sync neither hydrates nor writes it', async () => {
    // One hydrator, so two can never disagree about which payload is news.
    useUiStore.setState({ theme: 'light' })
    serverSettings = { ...settingsFixture, theme: 'dark', library_view: 'list' }
    const client = renderSync(false, { theme: false })

    await waitFor(() => {
      expect(useUiStore.getState().view).toBe('list')
    })
    expect(useUiStore.getState().theme).toBe('light')

    // Nor on a refetch whose only change is the theme. Whether that payload is
    // news to this hook at all (its reconcile key) cannot show here: hydrating
    // again only re-asserts library values the store already holds. It shows
    // when a library pick shares the commit — "keeps a library pick that lands in
    // the same commit as a theme-only payload" below is the test for that.
    serverSettings = { ...serverSettings, theme: 'system' }
    await act(async () => {
      await client.invalidateQueries({ queryKey: queryKeys.settings })
    })
    await settle()
    expect(useUiStore.getState().theme).toBe('light')
    expect(puts).toEqual([])
  })

  it('re-hydrates when a refetch carries a theme set elsewhere, even if nothing else moved', async () => {
    serverSettings = { ...settingsFixture, theme: 'dark' }
    const client = renderSync()
    await waitFor(() => {
      expect(useUiStore.getState().theme).toBe('dark')
    })

    // Only `theme` differs from the payload already reconciled. A reconcile key
    // that leaves `theme` out sees "nothing new" here and keeps `dark`.
    serverSettings = { ...serverSettings, theme: 'light' }
    await act(async () => {
      await client.invalidateQueries({ queryKey: queryKeys.settings })
    })

    await waitFor(() => {
      expect(useUiStore.getState().theme).toBe('light')
    })
    await settle()
    expect(puts).toEqual([])
  })

  it('restores the server theme on the next payload with new content, after an out-of-band local change', async () => {
    // The theme hook keys on the payload's whole content, not on its theme: a
    // store that drifted from the cache without a write (a late `persist`
    // rehydrate, a `setTheme` that never reached the writer) is put back by the
    // next payload that carries any news — here only a library field.
    serverSettings = { ...settingsFixture, theme: 'dark', library_view: 'grid' }
    const client = renderSync()
    await waitFor(() => {
      expect(useUiStore.getState().theme).toBe('dark')
    })
    await settle()

    act(() => {
      useUiStore.getState().setTheme('light')
    })
    serverSettings = { ...serverSettings, library_view: 'list' }
    await act(async () => {
      await client.invalidateQueries({ queryKey: queryKeys.settings })
    })

    await waitFor(() => {
      expect(useUiStore.getState().view).toBe('list')
    })
    expect(useUiStore.getState().theme).toBe('dark')
    await settle()
    expect(puts).toEqual([])
  })

  it('keeps a library pick that lands in the same commit as a theme-only payload', async () => {
    // A payload whose only news is a theme is the theme hook's. Were it news to
    // the library sync too, its hydration would run first in that commit and put
    // the cache's old sort back over the pick, while the write-back stays shut on
    // the not-yet-reconciled key; on the next commit store and cache agree, so
    // the pick would never be sent.
    const client = renderSync()
    await waitFor(() => {
      expect(client.getQueryData<Settings>(queryKeys.settings)?.library_sort).toBe('name')
    })
    await settle()

    serverSettings = { ...serverSettings, theme: 'dark' }
    await act(async () => {
      client.setQueryData<Settings>(queryKeys.settings, (old) =>
        old === undefined ? old : { ...old, theme: 'dark' },
      )
      useUiStore.getState().setSort('size')
      // Past the query cache's notify tick (a `setTimeout(0)`), still inside one
      // `act`: both changes render in one commit.
      await new Promise((resolve) => setTimeout(resolve, 20))
    })
    await settle()

    expect(useUiStore.getState().sort).toBe('size')
    expect(puts).toEqual([
      expect.objectContaining({ library_sort: 'size' }) as unknown as SettingsUpdate,
    ])
    expect(puts[0]).not.toHaveProperty('theme')
    expect(useUiStore.getState().theme).toBe('dark')
  })

  it('keeps the local theme when the server sends one it cannot read', async () => {
    useUiStore.setState({ theme: 'dark' })
    serverSettings = { ...settingsFixture, theme: 'sepia' as Settings['theme'], library_view: 'list' }

    renderSync()

    // The payload did land — the library half of it hydrated…
    await waitFor(() => {
      expect(useUiStore.getState().view).toBe('list')
    })
    // …and the unreadable theme was not adopted, nor "repaired" over the wire:
    // the theme has exactly one writer, and it is the user's switch.
    expect(useUiStore.getState().theme).toBe('dark')
    await settle()
    expect(puts).toEqual([])
  })

  it('never carries theme on the library write-back', async () => {
    // The one genuine PUT on the hydration path is the invalid-`library_sort`
    // repair. It must stay a library-only write.
    serverSettings = {
      ...settingsFixture,
      theme: 'dark',
      library_sort: 'bogus' as Settings['library_sort'],
    }

    renderSync()

    await waitFor(() => {
      expect(puts).toHaveLength(1)
    })
    expect(puts[0]).not.toHaveProperty('theme')
    expect(useUiStore.getState().theme).toBe('dark')
  })

  it('writes a local change exactly once, through the switch, and does not revert it', async () => {
    serverSettings = { ...settingsFixture, theme: 'light' }
    renderSync(true)
    await waitFor(() => {
      expect(useUiStore.getState().theme).toBe('light')
    })

    await userEvent.click(screen.getByRole('radio', { name: '다크' }))

    // Local first — the paint does not wait for the round trip.
    expect(document.documentElement).toHaveAttribute('data-theme', 'dark')
    await waitFor(() => {
      expect(puts).toEqual([{ theme: 'dark' }])
    })
    await settle()
    // One request: the switch's. A second writer (the library write-back) would
    // show up here as a second body carrying `theme`.
    expect(puts).toEqual([{ theme: 'dark' }])
    // The echo is adopted, and it agrees — no flip back to `light`.
    expect(useUiStore.getState().theme).toBe('dark')
    expect(document.documentElement).toHaveAttribute('data-theme', 'dark')
  })

  describe('a stale echo does not undo a newer local choice', () => {
    const cachedTheme = (client: QueryClient): string | undefined =>
      client.getQueryData<Settings>(queryKeys.settings)?.theme

    /** Every change of the store's theme — what `data-theme` follows — away from the latest pick. */
    function watchFlips(latest: () => string): { flips: string[]; stop: () => void } {
      const flips: string[] = []
      const stop = useUiStore.subscribe((s, prev) => {
        if (s.theme !== prev.theme && s.theme !== latest()) {
          flips.push(`${s.theme} while ${latest()} was picked`)
        }
      })
      return { flips, stop }
    }

    async function pick(label: string): Promise<void> {
      await userEvent.click(screen.getByRole('radio', { name: label }))
    }

    /** Releases one held response and lets whatever follows it settle. */
    async function release(index: number): Promise<void> {
      act(() => {
        held[index]?.release()
      })
      await settle()
    }

    /** Another device changes the theme; this one refetches. */
    async function remoteTheme(client: QueryClient, theme: Settings['theme']) {
      serverSettings = { ...serverSettings, theme }
      await act(async () => {
        await client.invalidateQueries({ queryKey: queryKeys.settings })
      })
    }

    it('never flips the screen back on a sweep that revisits a value, and ends the server on the last pick', async () => {
      // The browser-measured case, made deterministic: light→dark→system→light→
      // dark→system with every answer held, then answered in order. An echo of
      // the earlier `system` used to release the pick, and the `light` and `dark`
      // echoes behind it then flipped the screen.
      serverSettings = { ...settingsFixture, theme: 'light' }
      const client = renderSync(true)
      await waitFor(() => {
        expect(cachedTheme(client)).toBe('light')
      })
      await settle()

      holdPuts = true
      let latest = 'light'
      const watch = watchFlips(() => latest)
      for (const [label, value] of SWEEP) {
        latest = value
        await pick(label)
      }
      await settle()
      for (let i = 0; i < held.length; i += 1) await release(i)
      watch.stop()

      expect(watch.flips, 'an older echo changed the screen after a newer pick').toEqual([])
      expect(serverSettings.theme, 'the server ends on the last pick').toBe('system')
      expect(useUiStore.getState().theme).toBe('system')
      expect(cachedTheme(client)).toBe('system')
      expect(puts.at(-1)).toEqual({ theme: 'system' })
    })

    it('keeps light when dark is on the wire and its echo lands after light was picked', async () => {
      serverSettings = { ...settingsFixture, theme: 'system' }
      const client = renderSync(true)
      await waitFor(() => {
        expect(cachedTheme(client)).toBe('system')
      })
      await settle()

      holdPuts = true
      let latest = 'dark'
      const watch = watchFlips(() => latest)
      await pick('다크')
      await waitFor(() => {
        expect(held.map((h) => h.body)).toEqual([{ theme: 'dark' }])
      })
      latest = 'light'
      await pick('라이트')
      await settle()
      expect(held, 'light waits behind dark: one request on the wire').toHaveLength(1)

      await release(0)
      expect(useUiStore.getState().theme).toBe('light')
      expect(document.documentElement).toHaveAttribute('data-theme', 'light')
      expect(held.map((h) => h.body)).toEqual([{ theme: 'dark' }, { theme: 'light' }])

      await release(1)
      watch.stop()
      expect(watch.flips).toEqual([])
      expect(serverSettings.theme).toBe('light')
      expect(cachedTheme(client)).toBe('light')
    })

    it("ignores the old theme on a library PUT's response that lands after a local change", async () => {
      serverSettings = { ...settingsFixture, theme: 'system' }
      const client = renderSync(true)
      await waitFor(() => {
        expect(cachedTheme(client)).toBe('system')
      })
      await settle()

      holdPuts = true
      act(() => {
        useUiStore.getState().setView('list')
      })
      await waitFor(() => {
        expect(held).toHaveLength(1)
      })
      await pick('다크')
      await settle()
      expect(held, 'the theme waits behind the library write').toHaveLength(1)

      // The library write-back's echo: `list`, and the theme as it was before
      // the click. The view is news; the theme is not.
      await release(0)
      expect(useUiStore.getState()).toMatchObject({ view: 'list', theme: 'dark' })
      expect(document.documentElement).toHaveAttribute('data-theme', 'dark')

      expect(held).toHaveLength(2)
      await release(1)
      expect(useUiStore.getState()).toMatchObject({ view: 'list', theme: 'dark' })
      expect(puts).toEqual([
        expect.objectContaining({ library_view: 'list' }) as unknown as SettingsUpdate,
        { theme: 'dark' },
      ])
    })

    it('does not let a library echo flip the view back after a newer view change', async () => {
      const client = renderSync()
      await waitFor(() => {
        expect(client.getQueryData<Settings>(queryKeys.settings)?.library_view).toBe('grid')
      })
      await settle()

      holdPuts = true
      act(() => {
        useUiStore.getState().setView('list')
      })
      await waitFor(() => {
        expect(held).toHaveLength(1)
      })
      const views: string[] = []
      const stop = useUiStore.subscribe((s, prev) => {
        if (s.view !== prev.view) views.push(s.view)
      })
      act(() => {
        useUiStore.getState().setView('grid')
      })
      await settle()

      await release(0)
      expect(useUiStore.getState().view, 'the list echo is older than the grid pick').toBe('grid')
      await waitFor(() => {
        expect(held).toHaveLength(2)
      })
      await release(1)
      stop()
      expect(views).toEqual(['grid'])
      expect(serverSettings.library_view).toBe('grid')
      expect(puts.map((p) => p.library_view)).toEqual(['list', 'grid'])
    })

    it('hydrates the next remote change once a local pick has settled', async () => {
      serverSettings = { ...settingsFixture, theme: 'light' }
      const client = renderSync(true)
      await waitFor(() => {
        expect(cachedTheme(client)).toBe('light')
      })
      await settle()

      await pick('다크')
      await pick('라이트')
      await waitFor(() => {
        expect(puts).toEqual([{ theme: 'light' }])
      })
      await settle()
      expect(useUiStore.getState().theme).toBe('light')

      await remoteTheme(client, 'dark')
      await waitFor(() => {
        expect(useUiStore.getState().theme).toBe('dark')
      })
    })

    it("does not hold a refused pick over the server's theme, nor over the next remote change", async () => {
      server.use(
        http.put(`${ORIGIN}/api/settings`, async ({ request }) => {
          puts.push((await request.json()) as SettingsUpdate)
          return HttpResponse.json(
            { error: { code: 'bad_request', message: 'refused' } },
            { status: 400 },
          )
        }),
      )
      serverSettings = { ...settingsFixture, theme: 'light' }
      const client = renderSync(true)
      await waitFor(() => {
        expect(cachedTheme(client)).toBe('light')
      })
      await settle()

      await pick('다크')
      expect(useUiStore.getState().theme, 'local first').toBe('dark')
      await waitFor(() => {
        expect(puts).toEqual([{ theme: 'dark' }])
      })
      // The refusal re-reads the server, whose theme is still light.
      await waitFor(() => {
        expect(useUiStore.getState().theme).toBe('light')
      })

      await remoteTheme(client, 'system')
      await waitFor(() => {
        expect(useUiStore.getState().theme).toBe('system')
      })
    })

    it("hydrates a server 'system' over a local theme", async () => {
      useUiStore.setState({ theme: 'dark' })
      serverSettings = { ...settingsFixture, theme: 'system' }
      renderSync()
      await waitFor(() => {
        expect(useUiStore.getState().theme).toBe('system')
      })
      await settle()
      expect(puts).toEqual([])
    })

    it("releases a local 'system' choice on its echo, so the next remote change hydrates", async () => {
      serverSettings = { ...settingsFixture, theme: 'dark' }
      const client = renderSync(true)
      await waitFor(() => {
        expect(useUiStore.getState().theme).toBe('dark')
      })

      await pick('시스템')
      expect(cachedTheme(client), 'the cache shows the pick at once').toBe('system')
      await waitFor(() => {
        expect(puts).toEqual([{ theme: 'system' }])
      })
      await settle()
      expect(useUiStore.getState().theme).toBe('system')

      await remoteTheme(client, 'light')
      await waitFor(() => {
        expect(useUiStore.getState().theme).toBe('light')
      })
    })
  })
})
