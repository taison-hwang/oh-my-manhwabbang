import '@testing-library/jest-dom/vitest'

import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { setupServer } from 'msw/node'
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'

import { errorEnvelope, ORIGIN, settings as settingsFixture } from '../../api/fixtures'
import { queryKeys } from '../../api/queries'
import { resetSettingsWriter, SETTINGS_SAVE_DEBOUNCE_MS } from '../../api/settingsWriter'
import type { Settings, SettingsUpdate } from '../../api/types'
import { resetBasePath } from '../../api/urls'
import { useUiStore } from '../../store/ui'
import { ReadDefaultsPanel } from './ReadDefaultsPanel'
import { failUnhandled } from '../../test/msw'

/**
 * The read-defaults controls move on the input itself (ui-spec §8.6 §2: the
 * writer patches the cache "at once").
 *
 * Measured in a real browser: 읽기 방향 / 표시 모드 painted the *previous* option
 * for one frame after a click — 3/3 clicks with a save on the wire, 6/20 rapid
 * toggles with none. The writer's `setQueryData` is synchronous, but
 * react-query delivers it to the component on a scheduled tick (query-core
 * `notifyManager`, `setTimeout(cb, 0)`), and React restores a controlled input
 * to the props it last rendered as soon as the event handler returns. So the
 * click commits the old value, and the screen catches up a task later.
 *
 * The contract asserted here is therefore taken **synchronously after the
 * event** — before any timer, and so before react-query's notification — and
 * the re-sync tests pin the other half: whatever puts a pick on screen early must
 * not hold it against the cache when the cache changes for any other reason (a
 * refused write, a refetch carrying another device's change) — the failure a
 * local copy of the picks would bring.
 */

let serverSettings: Settings
let puts: SettingsUpdate[]
let gets: number
/** When set, each `PUT` answers 400 — the writer's quarantine path. */
let refusePuts: boolean
/** When set, each `PUT` is applied on arrival but answered only on `release`. */
let holdPuts: boolean
let held: (() => void)[]

const server = setupServer(
  http.get(`${ORIGIN}/api/settings`, () => {
    gets += 1
    return HttpResponse.json(serverSettings)
  }),
  http.put(`${ORIGIN}/api/settings`, async ({ request }) => {
    const update = (await request.json()) as SettingsUpdate
    puts.push(update)
    if (refusePuts) {
      return HttpResponse.json(errorEnvelope('bad_request', 'refused'), { status: 400 })
    }
    serverSettings = { ...serverSettings, ...update }
    const snapshot = serverSettings
    if (holdPuts) {
      await new Promise<void>((resolve) => {
        held.push(resolve)
      })
    }
    return HttpResponse.json(snapshot)
  }),
)

beforeAll(() => {
  server.listen({ onUnhandledRequest: failUnhandled })
})
afterEach(() => {
  for (const release of held) release()
  cleanup()
  resetSettingsWriter()
  resetBasePath()
  document.documentElement.removeAttribute('data-theme')
})
afterAll(() => {
  server.close()
})

beforeEach(() => {
  serverSettings = { ...settingsFixture } // ltr / single / prefetch 4
  puts = []
  gets = 0
  refusePuts = false
  holdPuts = false
  held = []
  localStorage.clear()
  useUiStore.setState({ theme: 'system' })
})

async function renderPanel(): Promise<QueryClient> {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={client}>
      <ReadDefaultsPanel />
    </QueryClientProvider>,
  )
  // Loaded: the slider is enabled only once the settings are in.
  await waitFor(() => {
    expect(screen.getByLabelText('프리페치 페이지')).toBeEnabled()
  })
  return client
}

function radio(group: string, label: string): HTMLElement {
  return within(screen.getByRole('radiogroup', { name: group })).getByRole('radio', { name: label })
}

/** The checked option of a group, as the screen shows it: the input and the styled label agree. */
function shown(group: string): string[] {
  const root = screen.getByRole('radiogroup', { name: group })
  const inputs = within(root)
    .getAllByRole('radio')
    .filter((r) => (r as HTMLInputElement).checked)
    .map((r) => (r as HTMLInputElement).value)
  const labels = [...root.querySelectorAll('label[data-checked="true"]')].map((l) =>
    l.getAttribute('data-value'),
  )
  return [...inputs, ...labels.map((v) => `label:${v ?? ''}`)]
}

/** The writer's debounce, then a margin for the round trip. */
async function settle(): Promise<void> {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, SETTINGS_SAVE_DEBOUNCE_MS + 80))
  })
}

describe('the pick is on screen in the click’s own commit', () => {
  it('읽기 방향: the clicked option is the checked one, before any timer runs', async () => {
    await renderPanel()
    expect(shown('읽기 방향')).toEqual(['ltr', 'label:ltr'])

    fireEvent.click(radio('읽기 방향', 'R→L'))
    // No await between the event and these lines: no timer can have run.
    expect(radio('읽기 방향', 'R→L')).toBeChecked()
    expect(radio('읽기 방향', 'L→R')).not.toBeChecked()
    expect(shown('읽기 방향')).toEqual(['rtl', 'label:rtl'])

    await settle()
    expect(puts).toEqual([{ reading_direction: 'rtl' }])
    expect(shown('읽기 방향')).toEqual(['rtl', 'label:rtl'])
  })

  it('표시 모드: two quick picks each show at once, and only the last is sent', async () => {
    await renderPanel()

    fireEvent.click(radio('표시 모드', '양면'))
    expect(shown('표시 모드')).toEqual(['spread', 'label:spread'])
    fireEvent.click(radio('표시 모드', '세로'))
    expect(shown('표시 모드')).toEqual(['vertical', 'label:vertical'])
    expect(radio('표시 모드', '단면')).not.toBeChecked()

    await settle()
    expect(puts).toEqual([{ display_mode: 'vertical' }])
    expect(shown('표시 모드')).toEqual(['vertical', 'label:vertical'])
  })

  it('shows a pick at once while an earlier save is still on the wire, and never disables', async () => {
    holdPuts = true
    await renderPanel()

    fireEvent.click(radio('읽기 방향', 'R→L'))
    await settle()
    expect(puts).toEqual([{ reading_direction: 'rtl' }])
    expect(held).toHaveLength(1) // on the wire, unanswered

    fireEvent.click(radio('표시 모드', '양면'))
    expect(shown('표시 모드')).toEqual(['spread', 'label:spread'])
    expect(shown('읽기 방향')).toEqual(['rtl', 'label:rtl'])
    for (const r of screen.getAllByRole('radio')) expect(r).toBeEnabled()
    expect(screen.getByLabelText('프리페치 페이지')).toBeEnabled()

    await act(async () => {
      for (const release of held.splice(0)) release()
      await Promise.resolve()
    })
    await settle()
    expect(puts).toEqual([{ reading_direction: 'rtl' }, { display_mode: 'spread' }])
  })

  it('프리페치 페이지: the thumb and the readout follow each step of a drag; steps inside one debounce window are one PUT', async () => {
    await renderPanel()
    const slider = screen.getByLabelText('프리페치 페이지')
    const readout = () => slider.parentElement?.nextElementSibling?.textContent

    for (const step of ['5', '6', '7', '8', '9']) {
      fireEvent.change(slider, { target: { value: step } })
      expect(slider).toHaveValue(step)
      expect(readout()).toBe(step)
      expect(slider).toBeEnabled()
    }

    await settle()
    expect(puts).toEqual([{ prefetch: 9 }])
    expect(slider).toHaveValue('9')
  })
})

describe('the cache still decides once it changes', () => {
  it('ends on the server’s value when the write is refused (quarantine → re-read)', async () => {
    refusePuts = true
    await renderPanel()
    const getsBefore = gets

    fireEvent.click(radio('읽기 방향', 'R→L'))
    expect(shown('읽기 방향')).toEqual(['rtl', 'label:rtl'])

    await waitFor(
      () => {
        expect(gets).toBeGreaterThan(getsBefore)
        expect(shown('읽기 방향')).toEqual(['ltr', 'label:ltr'])
      },
      { timeout: 3000 },
    )
    expect(puts).toEqual([{ reading_direction: 'rtl' }])
  })

  it('ends on the server’s prefetch when a slider write is refused', async () => {
    refusePuts = true
    await renderPanel()
    const slider = screen.getByLabelText('프리페치 페이지')

    fireEvent.change(slider, { target: { value: '9' } })
    expect(slider).toHaveValue('9')

    await waitFor(
      () => {
        expect(slider).toHaveValue('4')
      },
      { timeout: 3000 },
    )
  })

  it('follows a refetch that carries another device’s change after a pick was confirmed', async () => {
    const client = await renderPanel()

    fireEvent.click(radio('읽기 방향', 'R→L'))
    fireEvent.click(radio('표시 모드', '양면'))
    fireEvent.change(screen.getByLabelText('프리페치 페이지'), { target: { value: '9' } })
    await settle()
    expect(puts).toEqual([{ reading_direction: 'rtl', display_mode: 'spread', prefetch: 9 }])

    // Another device, later: every field moves away from this client's picks.
    serverSettings = { ...serverSettings, reading_direction: 'ltr', display_mode: 'vertical', prefetch: 2 }
    await act(async () => {
      await client.invalidateQueries({ queryKey: queryKeys.settings })
    })

    await waitFor(() => {
      expect(shown('읽기 방향')).toEqual(['ltr', 'label:ltr'])
    })
    expect(shown('표시 모드')).toEqual(['vertical', 'label:vertical'])
    expect(screen.getByLabelText('프리페치 페이지')).toHaveValue('2')
  })
})
