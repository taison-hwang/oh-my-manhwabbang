import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, renderHook } from '@testing-library/react'
import { createElement, type ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { putBookPrefs, type WriteOptions } from '../../api/client'
import { ApiError } from '../../api/errors'
import { BOOK_ID, bookDetail } from '../../api/fixtures'
import { queryKeys } from '../../api/queries'
import type { BookDetail, BookPrefs, BookPrefsUpdate } from '../../api/types'
import {
  PREFS_RETRY_BASE_MS,
  PREFS_RETRY_LIMIT,
  PREFS_SAVE_DEBOUNCE_MS,
  prefsWriterState,
  resetPrefsWriter,
  usePrefsWriter,
} from './prefsWriter'

/**
 * The prefs writer without a screen (av, second cycle).
 *
 * `putBookPrefs` is replaced by a controlled promise per call, so every race
 * below is staged in the order the test writes it rather than raced.
 */

vi.mock('../../api/client', async (importOriginal) => ({
  ...(await importOriginal<object>()),
  putBookPrefs: vi.fn(),
}))

interface Call {
  bid: string
  body: BookPrefsUpdate
  options: WriteOptions | undefined
  resolve: (prefs: BookPrefs) => void
  reject: (error: unknown) => void
}

const DEFAULTS: BookPrefs = {
  reading_direction: 'ltr',
  display_mode: 'single',
  fit_mode: 'height',
  is_override: false,
}

const RESET: BookPrefsUpdate = { reading_direction: null, display_mode: null, fit_mode: null }

const prefs = (over: Partial<BookPrefs>): BookPrefs => ({ ...DEFAULTS, is_override: true, ...over })

let calls: Call[]
let client: QueryClient

beforeEach(() => {
  vi.useFakeTimers()
  calls = []
  vi.mocked(putBookPrefs).mockImplementation(
    (bid, body, options) =>
      new Promise<BookPrefs>((resolve, reject) => {
        calls.push({ bid, body, options, resolve, reject })
      }),
  )
  client = new QueryClient()
  client.setQueryData<BookDetail>(queryKeys.books.detail(BOOK_ID), {
    ...bookDetail,
    id: BOOK_ID,
    prefs: DEFAULTS,
  })
})

afterEach(() => {
  resetPrefsWriter()
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

function mountWriter() {
  const wrapper = ({ children }: { children: ReactNode }) =>
    createElement(QueryClientProvider, { client }, children)
  return renderHook(() => usePrefsWriter(BOOK_ID), { wrapper })
}

async function advance(ms: number): Promise<void> {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms)
  })
}

function call(n: number): Call {
  const found = calls[n]
  if (found === undefined) throw new Error(`there is no PUT #${String(n + 1)}`)
  return found
}

const cached = (): BookPrefs | undefined =>
  client.getQueryData<BookDetail>(queryKeys.books.detail(BOOK_ID))?.prefs

const unavailable = (): ApiError => new ApiError({ status: 503, code: 'unavailable', message: '' })

/** The tab goes to the background (`visibilitychange` → hidden), then comes back. */
function hideTab(): void {
  Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'hidden' })
  document.dispatchEvent(new Event('visibilitychange'))
  Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' })
}

describe('prefsWriter (av)', () => {
  it('applies a late reset only to the fields nobody has edited since it was sent', async () => {
    const { result } = mountWriter()
    const onSaved = vi.fn()
    act(() => {
      result.current.saveNow(RESET, onSaved)
    })
    await advance(0)
    expect(call(0).body).toEqual(RESET)

    act(() => {
      result.current.save({ fit_mode: 'width' })
    })
    call(0).resolve(DEFAULTS)
    await advance(0)

    expect(onSaved).toHaveBeenCalledTimes(1)
    const [answered, untouched] = onSaved.mock.calls[0] as [BookPrefs, ReadonlySet<string>]
    expect(answered).toEqual(DEFAULTS)
    expect([...untouched].sort()).toEqual(['display_mode', 'reading_direction'])

    // …and the newer choice still goes out after it.
    await advance(PREFS_SAVE_DEBOUNCE_MS)
    expect(call(1).body).toEqual({ fit_mode: 'width' })
  })

  it('does not let an answer landing inside a newer debounce overwrite the cache', async () => {
    const { result } = mountWriter()
    act(() => {
      result.current.save({ fit_mode: 'width' })
    })
    await advance(PREFS_SAVE_DEBOUNCE_MS)
    expect(calls).toHaveLength(1)

    act(() => {
      result.current.save({ fit_mode: 'original' })
    })
    expect(cached()?.fit_mode).toBe('original')
    call(0).resolve(prefs({ fit_mode: 'width' }))
    await advance(0)
    expect(cached()?.fit_mode, 'the width answer is older than the original choice').toBe(
      'original',
    )

    await advance(PREFS_SAVE_DEBOUNCE_MS)
    call(1).resolve(prefs({ fit_mode: 'original' }))
    await advance(0)
    expect(cached()).toEqual(prefs({ fit_mode: 'original' }))
  })

  it('keeps a field the server did not confirm, and sends it with the next write', async () => {
    const { result } = mountWriter()
    act(() => {
      result.current.save({ fit_mode: 'width' })
    })
    await advance(PREFS_SAVE_DEBOUNCE_MS)
    act(() => {
      result.current.save({ display_mode: 'spread' })
    })
    call(0).reject(new TypeError('Failed to fetch'))
    await advance(PREFS_RETRY_BASE_MS)

    expect(call(1).body).toEqual({ fit_mode: 'width', display_mode: 'spread' })
    call(1).resolve(prefs({ fit_mode: 'width', display_mode: 'spread' }))
    await advance(PREFS_SAVE_DEBOUNCE_MS * 2)
    expect(calls, 'both fields confirmed: nothing left to send').toHaveLength(2)
  })

  it('lets a newer value of a failed field replace it', async () => {
    const { result } = mountWriter()
    act(() => {
      result.current.save({ fit_mode: 'width' })
    })
    await advance(PREFS_SAVE_DEBOUNCE_MS)
    act(() => {
      result.current.save({ fit_mode: 'original' })
    })
    call(0).reject(new TypeError('Failed to fetch'))
    await advance(PREFS_RETRY_BASE_MS)
    expect(call(1).body).toEqual({ fit_mode: 'original' })
  })

  it('retries a transient failure PREFS_RETRY_LIMIT times with backoff, then re-reads the server and keeps the field', async () => {
    const invalidate = vi.spyOn(client, 'invalidateQueries')
    const { result } = mountWriter()
    act(() => {
      result.current.save({ fit_mode: 'width' })
    })
    await advance(PREFS_SAVE_DEBOUNCE_MS)

    for (let attempt = 0; attempt < PREFS_RETRY_LIMIT; attempt += 1) {
      call(attempt).reject(unavailable())
      const delay = PREFS_RETRY_BASE_MS * 2 ** attempt
      await advance(delay - 1)
      expect(calls, `retry ${String(attempt + 1)} waits its ${String(delay)} ms`).toHaveLength(
        attempt + 1,
      )
      await advance(1)
      expect(calls).toHaveLength(attempt + 2)
      expect(call(attempt + 1).body).toEqual({ fit_mode: 'width' })
    }
    expect(invalidate).not.toHaveBeenCalled()

    call(PREFS_RETRY_LIMIT).reject(unavailable())
    await advance(PREFS_RETRY_BASE_MS * 2 ** (PREFS_RETRY_LIMIT + 2))
    expect(calls, 'no retry past the limit').toHaveLength(PREFS_RETRY_LIMIT + 1)
    expect(invalidate).toHaveBeenCalledWith({ queryKey: queryKeys.books.prefs(BOOK_ID) })
    expect(invalidate).toHaveBeenCalledWith({ queryKey: queryKeys.books.detail(BOOK_ID) })

    // Not lost: it rides along with the reader's next change.
    act(() => {
      result.current.save({ display_mode: 'spread' })
    })
    await advance(PREFS_SAVE_DEBOUNCE_MS)
    expect(call(PREFS_RETRY_LIMIT + 1).body).toEqual({ fit_mode: 'width', display_mode: 'spread' })
  })

  it('quarantines the fields a 4xx refused, so a later edit of another field goes through', async () => {
    const invalidate = vi.spyOn(client, 'invalidateQueries')
    const { result } = mountWriter()
    act(() => {
      result.current.save({ fit_mode: 'width' })
    })
    await advance(PREFS_SAVE_DEBOUNCE_MS)
    call(0).reject(new ApiError({ status: 422, code: 'unprocessable', message: '' }))
    await advance(PREFS_RETRY_BASE_MS * 2 ** (PREFS_RETRY_LIMIT + 2))
    expect(calls, 'the same body would get the same answer: no retry').toHaveLength(1)
    expect(invalidate).toHaveBeenCalledWith({ queryKey: queryKeys.books.prefs(BOOK_ID) })
    expect(invalidate).toHaveBeenCalledWith({ queryKey: queryKeys.books.detail(BOOK_ID) })
    expect(prefsWriterState().writers, 'nothing left to do for this book').toBe(0)

    act(() => {
      result.current.save({ display_mode: 'spread' })
    })
    await advance(PREFS_SAVE_DEBOUNCE_MS)
    expect(call(1).body, 'the refused field does not poison the next write').toEqual({
      display_mode: 'spread',
    })
    call(1).resolve(prefs({ display_mode: 'spread' }))
    await advance(PREFS_SAVE_DEBOUNCE_MS * 2)
    expect(calls).toHaveLength(2)
    expect(cached()).toEqual(prefs({ display_mode: 'spread' }))
  })

  it('keeps a newer edit of a field whose older value a 4xx refused', async () => {
    const { result } = mountWriter()
    act(() => {
      result.current.save({ fit_mode: 'width' })
    })
    await advance(PREFS_SAVE_DEBOUNCE_MS)
    act(() => {
      result.current.save({ fit_mode: 'original' })
    })
    call(0).reject(new ApiError({ status: 400, code: 'bad_request', message: '' }))
    await advance(PREFS_SAVE_DEBOUNCE_MS)
    expect(call(1).body).toEqual({ fit_mode: 'original' })
  })

  it.each([408, 429])('retries a %i as recoverable', async (status) => {
    const { result } = mountWriter()
    act(() => {
      result.current.save({ fit_mode: 'width' })
    })
    await advance(PREFS_SAVE_DEBOUNCE_MS)
    call(0).reject(new ApiError({ status, code: 'rate_limited', message: '' }))
    await advance(PREFS_RETRY_BASE_MS)
    expect(call(1).body).toEqual({ fit_mode: 'width' })
  })

  it('does not retry a 401, but keeps the field for after the login', async () => {
    const { result } = mountWriter()
    act(() => {
      result.current.save({ fit_mode: 'width' })
    })
    await advance(PREFS_SAVE_DEBOUNCE_MS)
    call(0).reject(new ApiError({ status: 401, code: 'unauthorized', message: '' }))
    await advance(PREFS_RETRY_BASE_MS * 2 ** (PREFS_RETRY_LIMIT + 2))
    expect(calls).toHaveLength(1)

    act(() => {
      result.current.save({ display_mode: 'spread' })
    })
    await advance(PREFS_SAVE_DEBOUNCE_MS)
    expect(call(1).body).toEqual({ fit_mode: 'width', display_mode: 'spread' })
  })

  it('forgets a writer once nothing is left, and stops listening for unload', async () => {
    const { result } = mountWriter()
    act(() => {
      result.current.save({ fit_mode: 'width' })
    })
    expect(prefsWriterState()).toEqual({ writers: 1, listening: true })
    await advance(PREFS_SAVE_DEBOUNCE_MS)
    call(0).resolve(prefs({ fit_mode: 'width' }))
    await advance(0)
    expect(prefsWriterState()).toEqual({ writers: 0, listening: false })
  })

  it('makes a seg edit wait its own debounce even if an older one already ran out', async () => {
    const { result } = mountWriter()
    act(() => {
      result.current.save({ fit_mode: 'width' })
    })
    await advance(PREFS_SAVE_DEBOUNCE_MS)
    expect(calls, 'A is on the wire').toHaveLength(1)
    act(() => {
      result.current.save({ display_mode: 'spread' })
    })
    await advance(PREFS_SAVE_DEBOUNCE_MS)
    expect(calls, "B's debounce ran out behind A").toHaveLength(1)
    act(() => {
      result.current.save({ fit_mode: 'original' })
    })
    call(0).resolve(prefs({ fit_mode: 'width' }))
    await advance(PREFS_SAVE_DEBOUNCE_MS - 1)
    expect(calls, 'C was edited after B ran out, so nothing goes before its own 500 ms').toHaveLength(
      1,
    )
    await advance(1)
    expect(call(1).body).toEqual({ display_mode: 'spread', fit_mode: 'original' })
  })

  /**
   * A request on the wire is not cancelled by `resetPrefsWriter`; the generation
   * makes whatever it settles into a no-op. Every settlement is staged because
   * each guard is reached by only one: a callback is `confirmed`'s, and the
   * re-read is `failed`'s on a refusal — a transient failure's retry is stopped
   * by `pump`'s own guard either way. The write on the wire is the `saveNow`
   * itself, so its answer is the one that owes `onSaved` — a reject never calls
   * back, so a reject-only test could not see `confirmed` firing a dead writer's callback.
   */
  it.each([
    [
      'fails',
      (c: Call) => {
        c.reject(unavailable())
      },
    ],
    [
      'is refused',
      (c: Call) => {
        c.reject(new ApiError({ status: 422, code: 'unprocessable', message: '' }))
      },
    ],
    [
      'succeeds',
      (c: Call) => {
        c.resolve(DEFAULTS)
      },
    ],
  ])('lets nothing a reset left behind send, retry, re-read or call back — the write on the wire %s', async (_, settle) => {
    const invalidate = vi.spyOn(client, 'invalidateQueries')
    const { result } = mountWriter()
    const onSaved = vi.fn()
    act(() => {
      result.current.saveNow(RESET, onSaved)
    })
    expect(calls).toHaveLength(1)
    act(() => {
      result.current.save({ fit_mode: 'width' })
    })

    resetPrefsWriter()
    settle(call(0))
    await advance(PREFS_RETRY_BASE_MS * 2 ** (PREFS_RETRY_LIMIT + 2))
    expect(calls, 'no retry and no queued edit after the writer was reset').toHaveLength(1)
    expect(onSaved).not.toHaveBeenCalled()
    expect(invalidate, "a dead writer does not re-read the server into anyone's cache").not.toHaveBeenCalled()
  })

  it('keeps one queue per book across a remount, so a reopened viewer cannot race the old write', async () => {
    const first = mountWriter()
    act(() => {
      first.result.current.save({ fit_mode: 'width' })
    })
    await advance(PREFS_SAVE_DEBOUNCE_MS)
    expect(calls).toHaveLength(1)
    first.unmount()

    const second = mountWriter()
    act(() => {
      second.result.current.save({ fit_mode: 'original' })
    })
    await advance(PREFS_SAVE_DEBOUNCE_MS * 2)
    expect(calls, 'the reopened viewer waits for the old write').toHaveLength(1)

    call(0).resolve(prefs({ fit_mode: 'width' }))
    await advance(0)
    expect(call(1).body).toEqual({ fit_mode: 'original' })
  })

  it.each([
    ['pagehide', () => window.dispatchEvent(new Event('pagehide'))],
    [
      'visibilitychange to hidden',
      () => {
        Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'hidden' })
        document.dispatchEvent(new Event('visibilitychange'))
        Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' })
      },
    ],
  ])('sends the buffer at once with keepalive on %s, without waiting for the queue', async (_, leave) => {
    const { result } = mountWriter()
    act(() => {
      result.current.save({ fit_mode: 'width' })
    })
    await advance(PREFS_SAVE_DEBOUNCE_MS)
    expect(calls, 'the first write is on the wire and unanswered').toHaveLength(1)
    act(() => {
      result.current.save({ fit_mode: 'original' })
    })

    act(leave)

    // At once: the first write is still unanswered, and this does not queue behind it.
    expect(calls).toHaveLength(2)
    expect(call(1)).toMatchObject({
      bid: BOOK_ID,
      body: { fit_mode: 'original' },
      options: { keepalive: true },
    })
    expect(call(0).options, 'a queued write is an ordinary one').toBeUndefined()
  })

  it('sends a buffer once when hidden is followed by pagehide with no edit between', async () => {
    const { result } = mountWriter()
    act(() => {
      result.current.save({ fit_mode: 'width' })
    })
    act(hideTab)
    act(() => {
      window.dispatchEvent(new Event('pagehide'))
    })
    expect(calls, 'the same buffer goes out once').toHaveLength(1)
    expect(call(0)).toMatchObject({ body: { fit_mode: 'width' }, options: { keepalive: true } })
    await advance(PREFS_SAVE_DEBOUNCE_MS * 2)
    expect(calls).toHaveLength(1)
  })

  it('sends a new value on pagehide after the tab came back and was edited', () => {
    const { result } = mountWriter()
    act(() => {
      result.current.save({ fit_mode: 'width' })
    })
    act(hideTab)
    act(() => {
      result.current.save({ fit_mode: 'original' })
    })
    act(() => {
      window.dispatchEvent(new Event('pagehide'))
    })
    expect(calls).toHaveLength(2)
    expect(call(1)).toMatchObject({ body: { fit_mode: 'original' }, options: { keepalive: true } })
  })

  it('sends the same buffer again on unload once its keepalive write has failed', async () => {
    const { result } = mountWriter()
    act(() => {
      result.current.save({ fit_mode: 'width' })
    })
    act(hideTab)
    call(0).reject(new TypeError('Failed to fetch'))
    await advance(0)
    act(() => {
      window.dispatchEvent(new Event('pagehide'))
    })
    expect(calls, 'a failed keepalive is not a sent buffer').toHaveLength(2)
    expect(call(1)).toMatchObject({ body: { fit_mode: 'width' }, options: { keepalive: true } })
  })

  it('still sends a buffer left behind by an unmounted viewer when the tab closes', async () => {
    const { result, unmount } = mountWriter()
    act(() => {
      result.current.save({ fit_mode: 'width' })
    })
    await advance(PREFS_SAVE_DEBOUNCE_MS)
    act(() => {
      result.current.save({ display_mode: 'spread' })
    })
    // Back to the library with A unanswered: B is queued behind it, not sent.
    unmount()
    expect(calls).toHaveLength(1)

    act(() => {
      window.dispatchEvent(new Event('pagehide'))
    })
    expect(calls).toHaveLength(2)
    expect(call(1)).toMatchObject({
      body: { fit_mode: 'width', display_mode: 'spread' },
      options: { keepalive: true },
    })
  })
})
