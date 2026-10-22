import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, renderHook } from '@testing-library/react'
import { createElement, type ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { getSettings, putSettings, type WriteOptions } from './client'
import { ApiError } from './errors'
import { settings as FIXTURE } from './fixtures'
import { queryKeys, useSettings } from './queries'
import {
  disposeSettingsWriter,
  resetSettingsWriter,
  SETTINGS_RETRY_BASE_MS,
  SETTINGS_SAVE_DEBOUNCE_MS,
  saveSettings,
  settingsReadMark,
  settingsWriterState,
  useSettingsWriter,
  withUnsavedSettings,
  type SaveSettings,
} from './settingsWriter'
import type { Settings, SettingsUpdate, Theme } from './types'

/**
 * The settings writer without a screen. The screen reads the settings cache —
 * the library store hydrates from it, the read-defaults controls render it — so
 * "the screen flips back" is asserted as "the cache flips back".
 *
 * `putSettings` is a fake server. A test either answers each request by hand
 * (`arrive` applies the body and answers with the server's state, in whatever
 * order the test calls it), or sets `answerAfter`: applied on arrival, answered
 * that many ms later with the state as of that request — the timing under which
 * both failures were seen in a real browser. `getSettings` is answered by hand.
 */

vi.mock('./client', async (importOriginal) => ({
  ...(await importOriginal<object>()),
  getSettings: vi.fn(),
  putSettings: vi.fn(),
}))

interface Put {
  body: SettingsUpdate
  options: WriteOptions | undefined
  answered: () => boolean
  arrive: () => void
  fail: (error: Error) => void
}

let server: Settings
let puts: Put[]
let gets: ((settings: Settings) => void)[]
let open: number
let maxOpen: number
let answerAfter: number | null
let latest: Theme | null
let client: QueryClient

/** light → dark → system → light → dark → system: revisits both values. */
const SWEEP: readonly Theme[] = ['dark', 'system', 'light', 'dark', 'system']

/**
 * The contract, written out rather than imported: a test that reads the
 * constant passes whatever it is set to.
 */
const RETRIES = 3
/** Longer than every backoff together: whatever is still to come has come. */
const LONG_AFTER = SETTINGS_RETRY_BASE_MS * 2 ** (RETRIES + 2)

beforeEach(() => {
  vi.useFakeTimers()
  server = { ...FIXTURE, theme: 'light' }
  puts = []
  gets = []
  open = 0
  maxOpen = 0
  answerAfter = null
  latest = null
  vi.mocked(putSettings).mockImplementation(
    (body, options) =>
      new Promise<Settings>((resolve, reject) => {
        let answered = false
        const settle = (): boolean => {
          if (answered) return false
          answered = true
          open -= 1
          return true
        }
        open += 1
        maxOpen = Math.max(maxOpen, open)
        puts.push({
          body,
          options,
          answered: () => answered,
          arrive: () => {
            if (!settle()) return
            server = { ...server, ...body }
            resolve({ ...server })
          },
          fail: (error) => {
            if (settle()) reject(error)
          },
        })
        if (answerAfter !== null) {
          server = { ...server, ...body }
          const asOfThisRequest = { ...server }
          setTimeout(() => {
            if (settle()) resolve(asOfThisRequest)
          }, answerAfter)
        }
      }),
  )
  vi.mocked(getSettings).mockImplementation(
    () =>
      new Promise<Settings>((resolve) => {
        gets.push(resolve)
      }),
  )
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  client.setQueryData<Settings>(queryKeys.settings, { ...server })
})

afterEach(() => {
  disposeSettingsWriter(client)
  resetSettingsWriter() // the extra clients a few tests make
  Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' })
  vi.useRealTimers()
})

function wrapper({ children }: { children: ReactNode }) {
  return createElement(QueryClientProvider, { client }, children)
}

function mountWriter(): SaveSettings {
  return renderHook(() => useSettingsWriter(), { wrapper }).result.current
}

async function advance(ms: number): Promise<void> {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms)
  })
}

function put(n: number): Put {
  const found = puts[n]
  if (found === undefined) throw new Error(`there is no PUT #${String(n + 1)}`)
  return found
}

const cached = (): Settings | undefined => client.getQueryData<Settings>(queryKeys.settings)
const cachedTheme = (): Theme | undefined => cached()?.theme

function choose(save: SaveSettings, theme: Theme): void {
  latest = theme
  act(() => {
    save({ theme })
  })
}

function save(writer: SaveSettings, update: SettingsUpdate): void {
  act(() => {
    writer(update)
  })
}

/** Every change of the cached theme to something other than the latest choice. */
function watchTheme(): { flips: string[]; stop: () => void } {
  const flips: string[] = []
  let last = cachedTheme()
  const stop = client.getQueryCache().subscribe(() => {
    const now = cachedTheme()
    if (now === last) return
    last = now
    if (latest !== null && now !== latest) {
      flips.push(`${String(now)} while ${latest} was the latest choice`)
    }
  })
  return { flips, stop }
}

const unavailable = (): ApiError => new ApiError({ status: 503, code: 'unavailable', message: '' })
const refused = (): ApiError => new ApiError({ status: 400, code: 'bad_request', message: '' })
const unauthorized = (): ApiError => new ApiError({ status: 401, code: 'unauthorized', message: '' })

// ---------------------------------------------------------------------------

describe('ordering — the two failures seen in the browser', () => {
  it.each([
    ['at key-repeat speed, answers held 300 ms', 60, 300],
    [
      'one step per debounce, answers slower than the steps',
      SETTINGS_SAVE_DEBOUNCE_MS + 50,
      2 * (SETTINGS_SAVE_DEBOUNCE_MS + 50),
    ],
  ])('a sweep %s that revisits a value never shows an older theme after a newer choice', async (_, gap, delay) => {
    answerAfter = delay
    const writer = mountWriter()
    const watch = watchTheme()

    for (const theme of SWEEP) {
      choose(writer, theme)
      await advance(gap)
    }
    await advance(20 * (SETTINGS_SAVE_DEBOUNCE_MS + delay))
    watch.stop()

    expect(watch.flips, 'an older echo changed the screen after a newer choice').toEqual([])
    expect(server.theme, 'the server ends on the last choice').toBe('system')
    expect(cachedTheme()).toBe('system')
    expect(maxOpen, 'one request on the wire at a time').toBe(1)
  })

  it('ends the server on the last choice even when queued requests arrive newest first', async () => {
    const writer = mountWriter()
    for (const theme of SWEEP) {
      choose(writer, theme)
      await advance(100)
    }
    // Whatever is on the wire arrives newest first — what the browser once did
    // with five queued PUTs (server `dark`, screen `system`).
    for (let round = 0; round < 10; round += 1) {
      for (const p of puts.filter((q) => !q.answered()).reverse()) p.arrive()
      await advance(SETTINGS_SAVE_DEBOUNCE_MS)
    }

    expect(server.theme, 'the server ends on the last choice').toBe('system')
    expect(cachedTheme(), 'and the screen agrees with it').toBe('system')
    expect(maxOpen, 'nothing to reorder: one request on the wire at a time').toBe(1)
  })
})

describe('debounce', () => {
  it('sends a sweep as one PUT of the last value, after the last step, and shows each step at once', async () => {
    const writer = mountWriter()
    for (const [i, theme] of SWEEP.entries()) {
      if (i > 0) await advance(100)
      choose(writer, theme)
      expect(cachedTheme(), 'the cache, and so the screen, moves on the step').toBe(theme)
    }

    await advance(SETTINGS_SAVE_DEBOUNCE_MS - 1)
    expect(puts, 'nothing until the burst has been quiet for the debounce').toHaveLength(0)
    await advance(1)
    expect(puts.map((p) => p.body)).toEqual([{ theme: 'system' }])
  })

  it('merges a burst across fields into one body, the newest value of each', async () => {
    const writer = mountWriter()
    save(writer, { library_view: 'list' })
    save(writer, { theme: 'dark' })
    save(writer, { library_view: 'grid', prefetch: 9 })
    expect(cached()).toMatchObject({ library_view: 'grid', theme: 'dark', prefetch: 9 })

    await advance(SETTINGS_SAVE_DEBOUNCE_MS)
    expect(puts.map((p) => p.body)).toEqual([{ library_view: 'grid', theme: 'dark', prefetch: 9 }])
  })
})

describe('one request at a time, and the newest choice wins', () => {
  it('holds a due choice until the request on the wire has answered', async () => {
    const writer = mountWriter()
    choose(writer, 'dark')
    await advance(SETTINGS_SAVE_DEBOUNCE_MS)
    expect(puts).toHaveLength(1)

    choose(writer, 'system')
    await advance(SETTINGS_SAVE_DEBOUNCE_MS * 3)
    expect(puts, 'system is due, but dark is still on the wire').toHaveLength(1)

    put(0).arrive()
    await advance(0)
    expect(puts.map((p) => p.body)).toEqual([{ theme: 'dark' }, { theme: 'system' }])
  })

  it('does not let an answer landing inside a newer debounce overwrite the newer choice', async () => {
    const writer = mountWriter()
    choose(writer, 'dark')
    await advance(SETTINGS_SAVE_DEBOUNCE_MS)
    choose(writer, 'system')

    put(0).arrive()
    await advance(0)
    expect(cachedTheme(), 'the dark answer is older than the system choice').toBe('system')

    await advance(SETTINGS_SAVE_DEBOUNCE_MS)
    put(1).arrive()
    await advance(0)
    expect(cached()).toEqual(server)
  })

  it('takes the whole answer when nothing was chosen since it was sent', async () => {
    const writer = mountWriter()
    // Another device moved prefetch; this one only knows once an answer says so.
    server = { ...server, prefetch: 11 }
    choose(writer, 'dark')
    await advance(SETTINGS_SAVE_DEBOUNCE_MS)
    put(0).arrive()
    await advance(0)
    expect(cached()).toEqual({ ...FIXTURE, theme: 'dark', prefetch: 11 })
  })
})

describe('failures', () => {
  it('keeps a field the server did not confirm, and sends it with the next write', async () => {
    const writer = mountWriter()
    save(writer, { library_view: 'list' })
    await advance(SETTINGS_SAVE_DEBOUNCE_MS)
    save(writer, { theme: 'dark' })
    put(0).fail(new TypeError('Failed to fetch'))
    await advance(SETTINGS_RETRY_BASE_MS)

    expect(put(1).body).toEqual({ library_view: 'list', theme: 'dark' })
    put(1).arrive()
    await advance(SETTINGS_SAVE_DEBOUNCE_MS * 2)
    expect(puts, 'both fields confirmed: nothing left to send').toHaveLength(2)
  })

  it('retries a transient failure 3 times with backoff, then re-reads the server and keeps the field', async () => {
    const invalidate = vi.spyOn(client, 'invalidateQueries')
    const writer = mountWriter()
    choose(writer, 'dark')
    await advance(SETTINGS_SAVE_DEBOUNCE_MS)

    for (let attempt = 0; attempt < RETRIES; attempt += 1) {
      put(attempt).fail(unavailable())
      const delay = SETTINGS_RETRY_BASE_MS * 2 ** attempt
      await advance(delay - 1)
      expect(puts, `retry ${String(attempt + 1)} waits its ${String(delay)} ms`).toHaveLength(
        attempt + 1,
      )
      await advance(1)
      expect(puts).toHaveLength(attempt + 2)
      expect(put(attempt + 1).body).toEqual({ theme: 'dark' })
    }
    expect(invalidate).not.toHaveBeenCalled()

    put(3).fail(unavailable())
    await advance(LONG_AFTER)
    expect(puts, 'the first attempt and 3 retries, no more').toHaveLength(4)
    expect(invalidate).toHaveBeenCalledWith({ queryKey: queryKeys.settings })

    // Not lost: it rides along with the next change.
    save(writer, { prefetch: 9 })
    await advance(SETTINGS_SAVE_DEBOUNCE_MS)
    expect(put(4).body).toEqual({ theme: 'dark', prefetch: 9 })
  })

  it('quarantines the fields a 4xx refused, so a later edit of another field goes through', async () => {
    const invalidate = vi.spyOn(client, 'invalidateQueries')
    const writer = mountWriter()
    save(writer, { library_scope: 'gone' })
    await advance(SETTINGS_SAVE_DEBOUNCE_MS)
    put(0).fail(refused())
    await advance(LONG_AFTER)
    expect(puts, 'the same body would get the same answer: no retry').toHaveLength(1)
    expect(invalidate).toHaveBeenCalledWith({ queryKey: queryKeys.settings })
    expect(settingsWriterState(client).pending, 'nothing left to send').toBe(0)

    choose(writer, 'dark')
    await advance(SETTINGS_SAVE_DEBOUNCE_MS)
    expect(put(1).body, 'the refused field does not poison the next write').toEqual({
      theme: 'dark',
    })
  })

  it('keeps a newer edit of a field whose older value a 4xx refused', async () => {
    const writer = mountWriter()
    save(writer, { library_scope: 'gone' })
    await advance(SETTINGS_SAVE_DEBOUNCE_MS)
    save(writer, { library_scope: 'reading' })
    put(0).fail(refused())
    await advance(SETTINGS_SAVE_DEBOUNCE_MS)
    expect(put(1).body).toEqual({ library_scope: 'reading' })
  })

  it.each([408, 429])('retries a %i as recoverable', async (status) => {
    const writer = mountWriter()
    choose(writer, 'dark')
    await advance(SETTINGS_SAVE_DEBOUNCE_MS)
    put(0).fail(new ApiError({ status, code: 'rate_limited', message: '' }))
    await advance(SETTINGS_RETRY_BASE_MS)
    expect(put(1).body).toEqual({ theme: 'dark' })
  })

  it('does not retry a 401, but keeps the field for after the login', async () => {
    const writer = mountWriter()
    choose(writer, 'dark')
    await advance(SETTINGS_SAVE_DEBOUNCE_MS)
    put(0).fail(new ApiError({ status: 401, code: 'unauthorized', message: '' }))
    await advance(LONG_AFTER)
    expect(puts).toHaveLength(1)

    save(writer, { prefetch: 9 })
    await advance(SETTINGS_SAVE_DEBOUNCE_MS)
    expect(put(1).body).toEqual({ theme: 'dark', prefetch: 9 })
  })

  it('lets nothing a reset left behind send, retry or touch the cache', async () => {
    const writer = mountWriter()
    choose(writer, 'dark')
    await advance(SETTINGS_SAVE_DEBOUNCE_MS)
    choose(writer, 'system')

    resetSettingsWriter()
    client.setQueryData<Settings>(queryKeys.settings, { ...FIXTURE, theme: 'light' })
    put(0).fail(unavailable())
    await advance(LONG_AFTER)
    expect(puts, 'no retry and no queued write after the reset').toHaveLength(1)
    expect(cachedTheme()).toBe('light')
  })
})

describe('leaving the page', () => {
  function hide(): void {
    act(() => {
      Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'hidden' })
      document.dispatchEvent(new Event('visibilitychange'))
    })
  }

  function show(): void {
    act(() => {
      Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' })
      document.dispatchEvent(new Event('visibilitychange'))
    })
  }

  function pageHide(): void {
    act(() => {
      window.dispatchEvent(new Event('pagehide'))
    })
  }

  /** Answers whatever is on the wire, newest first, until nothing is left to send. */
  async function drainNewestFirst(): Promise<void> {
    for (let round = 0; round < 10; round += 1) {
      for (const p of puts.filter((q) => !q.answered()).reverse()) p.arrive()
      await advance(SETTINGS_SAVE_DEBOUNCE_MS)
    }
  }

  // --- visibilitychange to hidden: the page is still alive, so the queue holds ---

  it('sends the buffer at once with keepalive on a hide, as the queued request', () => {
    const writer = mountWriter()
    choose(writer, 'dark')

    hide()

    expect(puts).toHaveLength(1)
    expect(put(0)).toMatchObject({ body: { theme: 'dark' }, options: { keepalive: true } })
    expect(settingsWriterState(client).inFlight, 'the queue waits for it').toBe(true)
  })

  it('does not let a write sent on a hide land after a newer choice made on return', async () => {
    // The codex repro: hidden before the debounce, so the hide sends `dark`; back,
    // the reader picks `system`. If the hide's write were fire-and-forget the
    // queue would send `system` beside it, and the server could apply `dark` last.
    const writer = mountWriter()
    choose(writer, 'dark')
    hide()
    show()
    choose(writer, 'system')
    await advance(SETTINGS_SAVE_DEBOUNCE_MS * 3)

    await drainNewestFirst()

    expect(server.theme, 'the server ends on the last choice').toBe('system')
    expect(cachedTheme()).toBe('system')
    expect(maxOpen, 'the hide write is the queued one: nothing beside it').toBe(1)
    expect(puts.map((p) => p.body)).toEqual([{ theme: 'dark' }, { theme: 'system' }])
  })

  it('sends nothing beside a request on the wire at a hide; the rest follows its answer, with keepalive', async () => {
    const writer = mountWriter()
    choose(writer, 'dark')
    await advance(SETTINGS_SAVE_DEBOUNCE_MS)
    choose(writer, 'system')

    hide()
    expect(puts, 'dark is on the wire: system waits behind it').toHaveLength(1)
    expect(put(0).options, 'a write sent while visible is an ordinary one').toBeUndefined()

    put(0).arrive()
    await advance(0)
    expect(put(1), 'no debounce: the hide asked for it now').toMatchObject({
      body: { theme: 'system' },
      options: { keepalive: true },
    })
    put(1).arrive()
    await advance(0)
    expect(server.theme).toBe('system')
    expect(cachedTheme()).toBe('system')
  })

  // --- pagehide: the document is going away and will not wait for the queue ---

  it('does not send on pagehide what the hide before it already has on the wire', () => {
    const writer = mountWriter()
    choose(writer, 'dark')
    hide()
    pageHide()
    expect(puts.map((p) => p.body), 'one write: the hide sent this exact buffer').toEqual([
      { theme: 'dark' },
    ])
  })

  it('sends on pagehide what the hide could not, beside the request on the wire', async () => {
    const writer = mountWriter()
    choose(writer, 'dark')
    await advance(SETTINGS_SAVE_DEBOUNCE_MS)
    choose(writer, 'system')
    hide()
    expect(puts).toHaveLength(1)

    pageHide()

    expect(puts).toHaveLength(2)
    expect(put(1)).toMatchObject({ body: { theme: 'system' }, options: { keepalive: true } })
  })

  it('sends the buffer at once with keepalive on a pagehide with nothing before it', async () => {
    const writer = mountWriter()
    choose(writer, 'dark')
    await advance(SETTINGS_SAVE_DEBOUNCE_MS)
    expect(puts, 'the first write is on the wire and unanswered').toHaveLength(1)
    choose(writer, 'system')

    pageHide()

    expect(puts).toHaveLength(2)
    expect(put(1)).toMatchObject({ body: { theme: 'system' }, options: { keepalive: true } })
    expect(put(0).options, 'a queued write is an ordinary one').toBeUndefined()
  })

  it('re-sends what a pagehide write raced when the page comes back: the pagehide write answers first', async () => {
    const writer = mountWriter()
    choose(writer, 'dark')
    await advance(SETTINGS_SAVE_DEBOUNCE_MS)
    choose(writer, 'system')
    pageHide()
    expect(puts).toHaveLength(2)

    // The two were on the wire together, and the server applied the older last.
    put(1).arrive()
    put(0).arrive()
    await advance(0)
    expect(server.theme).toBe('dark')

    expect(put(2).body, 'the page was kept (bfcache), and it is still here').toEqual({
      theme: 'system',
    })
    put(2).arrive()
    await advance(0)
    expect(server.theme).toBe('system')
    expect(cachedTheme()).toBe('system')
  })

  it('re-sends what a pagehide write raced only once both have answered: the queued one answers first', async () => {
    const writer = mountWriter()
    choose(writer, 'dark')
    await advance(SETTINGS_SAVE_DEBOUNCE_MS)
    choose(writer, 'system')
    pageHide()

    put(0).arrive()
    await advance(SETTINGS_SAVE_DEBOUNCE_MS)
    expect(puts, 'nothing goes while the pagehide write is still on the wire').toHaveLength(2)

    put(1).arrive()
    await advance(0)
    expect(put(2).body, 'the order they were applied in is unknown here').toEqual({
      theme: 'system',
    })
    put(2).arrive()
    await advance(0)
    expect(server.theme).toBe('system')
    expect(cachedTheme()).toBe('system')
  })

  it('does not let a pagehide write overtake a choice made after the page came back', async () => {
    const writer = mountWriter()
    choose(writer, 'dark')
    await advance(SETTINGS_SAVE_DEBOUNCE_MS)
    choose(writer, 'system')
    pageHide()
    expect(puts).toHaveLength(2)

    // Back from the bfcache, the reader picks `light`, and the queued `dark`
    // answers while the pagehide's `system` is still on the wire.
    choose(writer, 'light')
    await advance(SETTINGS_SAVE_DEBOUNCE_MS)
    put(0).arrive()
    await advance(0)

    await drainNewestFirst()

    expect(server.theme, 'the server ends on the last choice').toBe('light')
    expect(cachedTheme()).toBe('light')
  })

  it.each([
    ['the queued request answers first, the pagehide write is refused last', [0, 1]],
    ['the pagehide write is refused first, the queued request answers last', [1, 0]],
  ] as const)(
    'quarantines a scope a raced pagehide write was refused for, and loses no edit made since: %s',
    async (_, order) => {
      // The codex repro: the queued `reading` and the pagehide's newer `gone`
      // race; back from the bfcache, the reader picks a theme. The server refuses
      // `gone` — the whole body, as a 400 does. Whichever of the two answers last,
      // the refusal must take `gone` out of the buffer before the raced fields go
      // again, or `gone` rides along with the theme and its refusal drops both.
      const writer = mountWriter()
      save(writer, { library_scope: 'reading' })
      await advance(SETTINGS_SAVE_DEBOUNCE_MS)
      save(writer, { library_scope: 'gone' })
      pageHide()
      expect(puts.map((p) => p.body)).toEqual([{ library_scope: 'reading' }, { library_scope: 'gone' }])

      choose(writer, 'dark')
      await advance(SETTINGS_SAVE_DEBOUNCE_MS)
      expect(puts, 'nothing goes while either is on the wire').toHaveLength(2)

      for (const n of order) {
        if (n === 0) put(0).arrive()
        else put(1).fail(refused())
        await advance(0)
      }
      // Whatever follows meets the same server: `gone` refused, anything else applied.
      for (let round = 0; round < 10; round += 1) {
        for (const p of puts.filter((q) => !q.answered())) {
          if (p.body.library_scope === 'gone') p.fail(refused())
          else p.arrive()
        }
        await advance(SETTINGS_SAVE_DEBOUNCE_MS)
      }

      expect(server.theme, 'the theme chosen after the page came back reaches the server').toBe('dark')
      expect(
        puts.slice(2).map((p) => p.body),
        'once refused, gone is not sent again: the theme goes alone',
      ).toEqual([{ theme: 'dark' }])
      expect(server.library_scope, 'the last scope the server accepted').toBe('reading')
      expect(cached(), 'and the screen agrees with the server').toEqual(server)
      expect(settingsWriterState(client)).toEqual({ pending: 0, inFlight: false, listening: false })
    },
  )

  it.each([
    ['the queued request is refused first, the pagehide write last', [0, 1]],
    ['the pagehide write is refused first, the queued request last', [1, 0]],
  ] as const)(
    'holds what a raced pagehide write carried when both come back 401, until the next write: %s',
    async (_, order) => {
      // The codex repro: the queued `dark` and the pagehide's newer `system` race,
      // and the session has expired, so the server refuses both with a 401. A 401
      // waits for the next write (a login fixes it): settling the race must not
      // send `system` again by itself, with nothing changed and nobody logged in.
      const writer = mountWriter()
      choose(writer, 'dark')
      await advance(SETTINGS_SAVE_DEBOUNCE_MS)
      choose(writer, 'system')
      pageHide()
      expect(puts.map((p) => p.body)).toEqual([{ theme: 'dark' }, { theme: 'system' }])

      for (const n of order) {
        put(n).fail(unauthorized())
        await advance(0)
      }
      await advance(LONG_AFTER)
      expect(puts, 'a 401 waits: nothing goes before the next write').toHaveLength(2)
      expect(cachedTheme(), 'the choice is held, not dropped').toBe('system')

      // The next write carries the held choice, once.
      save(writer, { prefetch: 9 })
      await advance(SETTINGS_SAVE_DEBOUNCE_MS)
      expect(puts.slice(2).map((p) => p.body)).toEqual([{ theme: 'system', prefetch: 9 }])
      put(2).arrive()
      await advance(LONG_AFTER)
      expect(puts, 'and nothing after it').toHaveLength(3)
      expect(server.theme).toBe('system')
      expect(cached(), 'and the screen agrees with the server').toEqual(server)
      expect(settingsWriterState(client)).toEqual({ pending: 0, inFlight: false, listening: false })
    },
  )

  it('re-sends with keepalive on pagehide what an ordinary request carries, and nothing more after', async () => {
    // The document may cancel an ordinary request as it goes; the same buffer
    // goes again with keepalive. Both carry the same values, so whichever the
    // server applies last, it is right: no corrective re-send.
    const writer = mountWriter()
    choose(writer, 'dark')
    await advance(SETTINGS_SAVE_DEBOUNCE_MS)

    pageHide()
    expect(put(1)).toMatchObject({ body: { theme: 'dark' }, options: { keepalive: true } })

    put(1).arrive()
    put(0).arrive()
    await advance(LONG_AFTER)
    expect(puts).toHaveLength(2)
    expect(server.theme).toBe('dark')
    expect(settingsWriterState(client)).toEqual({ pending: 0, inFlight: false, listening: false })
  })

  it('listens for unload only while something is unsent', async () => {
    const writer = mountWriter()
    expect(settingsWriterState(client).listening).toBe(false)
    choose(writer, 'dark')
    expect(settingsWriterState(client)).toMatchObject({ pending: 1, listening: true })
    await advance(SETTINGS_SAVE_DEBOUNCE_MS)
    put(0).arrive()
    await advance(0)
    expect(settingsWriterState(client)).toEqual({ pending: 0, inFlight: false, listening: false })
  })
})

describe('one writer per QueryClient', () => {
  it("keeps one client's unsent choice out of another client's state and GETs", () => {
    const writer = mountWriter()
    choose(writer, 'dark')
    const other = new QueryClient()
    other.setQueryData<Settings>(queryKeys.settings, { ...server })

    expect(settingsWriterState(client).pending).toBe(1)
    expect(settingsWriterState(other).pending).toBe(0)
    expect(withUnsavedSettings(other, { ...server }, settingsReadMark()).theme).toBe('light')
    expect(other.getQueryData<Settings>(queryKeys.settings)?.theme).toBe('light')
  })

  it("sends every client's buffer when the page goes away", () => {
    const writer = mountWriter()
    choose(writer, 'dark')
    const other = new QueryClient()
    other.setQueryData<Settings>(queryKeys.settings, { ...server })
    act(() => {
      saveSettings(other, { prefetch: 9 })
    })

    act(() => {
      window.dispatchEvent(new Event('pagehide'))
    })
    expect(puts.map((p) => p.body).sort((a, b) => (Object.keys(a)[0] ?? '').localeCompare(Object.keys(b)[0] ?? ''))).toEqual([
      { prefetch: 9 },
      { theme: 'dark' },
    ])
  })

  it('forgets a disposed client: nothing it buffered or had on the wire acts again', async () => {
    const writer = mountWriter()
    choose(writer, 'dark')
    await advance(SETTINGS_SAVE_DEBOUNCE_MS)
    choose(writer, 'system')
    const other = new QueryClient()
    act(() => {
      saveSettings(other, { prefetch: 9 })
    })

    disposeSettingsWriter(client)

    expect(settingsWriterState(client)).toMatchObject({ pending: 0, inFlight: false })
    client.setQueryData<Settings>(queryKeys.settings, { ...FIXTURE, theme: 'light' })
    put(0).fail(unavailable())
    await advance(LONG_AFTER)
    expect(cachedTheme(), 'a late answer does not touch the cache').toBe('light')
    expect(puts.map((p) => p.body), 'no retry, and the buffered system never goes').toEqual([
      { theme: 'dark' },
      { prefetch: 9 },
    ])

    // An unload sends nothing for it, and once the other client has nothing
    // unsent either, nothing is listening any more.
    put(1).arrive()
    await advance(0)
    expect(settingsWriterState(other)).toMatchObject({ pending: 0, listening: false })
    act(() => {
      window.dispatchEvent(new Event('pagehide'))
    })
    expect(puts).toHaveLength(2)
  })
})

describe('a GET meeting choices the server has not confirmed', () => {
  function mountWithQuery(): SaveSettings {
    return renderHook(
      () => {
        useSettings()
        return useSettingsWriter()
      },
      { wrapper },
    ).result.current
  }

  async function answerGet(n: number, settings: Settings): Promise<void> {
    const resolve = gets[n]
    if (resolve === undefined) throw new Error(`there is no GET #${String(n + 1)}`)
    resolve(settings)
    await advance(0)
  }

  async function refetch(): Promise<void> {
    act(() => {
      void client.invalidateQueries({ queryKey: queryKeys.settings })
    })
    await advance(0)
  }

  it('keeps a choice still to be sent, and takes the rest of the payload', async () => {
    const writer = mountWithQuery()
    await answerGet(0, { ...server })
    choose(writer, 'dark')

    server = { ...server, prefetch: 11 } // another device
    await refetch()
    await answerGet(1, { ...server })
    expect(cached()).toMatchObject({ theme: 'dark', prefetch: 11 })

    await advance(SETTINGS_SAVE_DEBOUNCE_MS)
    expect(put(0).body, 'and the choice still goes out').toEqual({ theme: 'dark' })
  })

  it('does not let a GET read before a PUT was applied undo it after it answered', async () => {
    const writer = mountWithQuery()
    await answerGet(0, { ...server })
    choose(writer, 'dark')
    await advance(SETTINGS_SAVE_DEBOUNCE_MS)
    const beforeThePut = { ...server }
    await refetch()

    put(0).arrive()
    await advance(0)
    expect(cachedTheme()).toBe('dark')
    await answerGet(1, beforeThePut)
    expect(cachedTheme(), 'the GET left the server before the PUT was applied').toBe('dark')
  })

  it('does not let a GET sent before both writes bring back a value a later complete answer replaced', async () => {
    const writer = mountWithQuery()
    await answerGet(0, { ...server })
    const beforeTheWrites = { ...server }
    await refetch() // on the wire until the end

    choose(writer, 'dark')
    await advance(SETTINGS_SAVE_DEBOUNCE_MS)
    save(writer, { prefetch: 9 }) // queued behind the theme write
    put(0).arrive() // theme acknowledged; its answer is not taken — prefetch changed since
    await advance(0)

    server = { ...server, theme: 'system' } // another device
    latest = 'system'
    await advance(SETTINGS_SAVE_DEBOUNCE_MS)
    put(1).arrive() // the whole settings object, taken: nothing changed since
    await advance(0)
    expect(cachedTheme(), "a taken answer is the server's word on every field").toBe('system')

    await answerGet(1, beforeTheWrites)
    expect(cachedTheme(), 'the GET left before both writes').toBe('system')
    expect(cached()?.prefetch).toBe(9)
  })

  it("takes a GET sent after the writer settled as the server's word (another device)", async () => {
    const writer = mountWithQuery()
    await answerGet(0, { ...server })
    choose(writer, 'dark')
    await advance(SETTINGS_SAVE_DEBOUNCE_MS)
    put(0).arrive()
    await advance(0)

    server = { ...server, theme: 'light' }
    await refetch()
    await answerGet(1, { ...server })
    expect(cachedTheme()).toBe('light')
  })

  it('does not keep a refused value over the re-read', async () => {
    const writer = mountWithQuery()
    await answerGet(0, { ...server })
    save(writer, { library_scope: 'gone' })
    expect(cached()?.library_scope).toBe('gone')
    await advance(SETTINGS_SAVE_DEBOUNCE_MS)

    put(0).fail(refused())
    await advance(0)
    expect(gets, 'the refusal re-reads the server').toHaveLength(2)
    await answerGet(1, { ...server })
    expect(cached()?.library_scope).toBe('all')
  })

  it('does not bring a refused value back on a GET that was sent before the refusal', async () => {
    // The re-read above is a new GET; one already on the wire is not always
    // cancelled for it (TanStack reuses it while the cache is empty).
    const writer = mountWriter()
    save(writer, { library_scope: 'gone' })
    await advance(SETTINGS_SAVE_DEBOUNCE_MS)
    const mark = settingsReadMark()
    put(0).fail(refused())
    await advance(0)
    expect(withUnsavedSettings(client, { ...server }, mark).library_scope).toBe('all')
  })
})
