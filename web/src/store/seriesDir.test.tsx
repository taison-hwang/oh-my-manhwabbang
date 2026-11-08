import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, renderHook, waitFor } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { setupServer } from 'msw/node'
import type { ReactNode } from 'react'
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '../api/errors'
import {
  BOOK_ID,
  bookDetail,
  bookPrefs,
  cacheUsage,
  continueResponse,
  ORIGIN,
  SERIES_ID,
  seriesDetail,
} from '../api/fixtures'
import { queryKeys, useSetSeriesDirection } from '../api/queries'
import type { AuthStatus, ID, ReadingDir, SeriesPrefs } from '../api/types'
import { resetBasePath } from '../api/urls'
import { createCoverMock, failUnhandled, type CoverMock } from '../test/msw'
import {
  SERIES_DIR_STORAGE_KEY,
  migrateSeriesDirSeeds,
  resetSeriesDirMigration,
  useSeriesDirSeedMigration,
  type SeedMigrationDeps,
} from './seriesDir'

/**
 * Ruling E-69 §2.3 — the series direction moved to the server, and the old
 * localStorage seed (`shelf.seriesDir`, C-9 / E-33 §2) is uploaded **once**, only
 * for a series whose server value is still `null`, and then removed.
 *
 * The key holds what zustand's `persist` wrote while the seed was a store:
 * `{"state":{"bySeries":{…}},"version":0}`. Nothing writes it any more.
 */

const A = 'aaaaaaaaaaaaaaaa'
const B = 'bbbbbbbbbbbbbbbb'
const C = 'cccccccccccccccc'

function plant(bySeries: Record<string, unknown>): void {
  localStorage.setItem(SERIES_DIR_STORAGE_KEY, JSON.stringify({ state: { bySeries }, version: 0 }))
}

function left(): Record<string, unknown> | null {
  const raw = localStorage.getItem(SERIES_DIR_STORAGE_KEY)
  if (raw === null) return null
  return (JSON.parse(raw) as { state: { bySeries: Record<string, unknown> } }).state.bySeries
}

function apiError(status: number): ApiError {
  return new ApiError({ status, code: status === 404 ? 'not_found' : 'internal', message: 'x' })
}

/** A fake server: per-series stored direction, or the error a GET/PUT answers with. */
function fakeServer(
  stored: Record<ID, ReadingDir | null | Error>,
  putErrors: Record<ID, Error> = {},
): SeedMigrationDeps & { gets: ID[]; puts: { sid: ID; prefs: SeriesPrefs }[] } {
  const gets: ID[] = []
  const puts: { sid: ID; prefs: SeriesPrefs }[] = []
  return {
    gets,
    puts,
    getSeries: vi.fn(async (sid: ID) => {
      gets.push(sid)
      await Promise.resolve()
      const value = stored[sid]
      if (value instanceof Error) throw value
      if (value === undefined) throw apiError(404)
      return { reading_direction: value }
    }),
    putSeriesPrefs: vi.fn(async (sid: ID, prefs: SeriesPrefs) => {
      puts.push({ sid, prefs })
      await Promise.resolve()
      const error = putErrors[sid]
      if (error !== undefined) throw error
      return { reading_direction: prefs.reading_direction }
    }),
  }
}

beforeEach(() => {
  localStorage.clear()
  resetSeriesDirMigration()
})

describe('migrateSeriesDirSeeds (E-69 §2.3)', () => {
  it('uploads a seed whose series has no direction on the server, then removes it', async () => {
    plant({ [A]: 'rtl' })
    const server = fakeServer({ [A]: null })
    await migrateSeriesDirSeeds(server)
    expect(server.puts).toEqual([{ sid: A, prefs: { reading_direction: 'rtl' } }])
    // The last seed gone ⇒ the key itself is gone.
    expect(localStorage.getItem(SERIES_DIR_STORAGE_KEY)).toBeNull()
  })

  it('never overwrites a direction the server already has — it only drops the seed', async () => {
    plant({ [A]: 'rtl' })
    const server = fakeServer({ [A]: 'ltr' })
    await migrateSeriesDirSeeds(server)
    expect(server.gets).toEqual([A])
    expect(server.puts).toEqual([])
    expect(localStorage.getItem(SERIES_DIR_STORAGE_KEY)).toBeNull()
  })

  it('drops the seed of a series the server no longer has (404), without a PUT', async () => {
    plant({ [A]: 'rtl' })
    const server = fakeServer({})
    await migrateSeriesDirSeeds(server)
    expect(server.puts).toEqual([])
    expect(localStorage.getItem(SERIES_DIR_STORAGE_KEY)).toBeNull()
  })

  it.each([
    ['a network failure', new TypeError('Failed to fetch')],
    ['a 500', apiError(500)],
    ['a 503', apiError(503)],
  ])('keeps the seed for the next boot after %s on the GET', async (_label, error) => {
    plant({ [A]: 'rtl' })
    const server = fakeServer({ [A]: error })
    await migrateSeriesDirSeeds(server)
    expect(server.puts).toEqual([])
    expect(left()).toEqual({ [A]: 'rtl' })
  })

  it('keeps the seed when the PUT fails on the way (5xx), drops it when the series is gone (404)', async () => {
    plant({ [A]: 'rtl', [B]: 'ltr' })
    const server = fakeServer({ [A]: null, [B]: null }, { [A]: apiError(503), [B]: apiError(404) })
    await migrateSeriesDirSeeds(server)
    expect(server.puts.map((p) => p.sid).sort()).toEqual([A, B])
    expect(left()).toEqual({ [A]: 'rtl' })
  })

  it('removes only what it settled, in the same shape it read', async () => {
    plant({ [A]: 'rtl', [B]: 'rtl', [C]: 'ltr' })
    const server = fakeServer({ [A]: null, [B]: apiError(500), [C]: 'rtl' })
    await migrateSeriesDirSeeds(server)
    expect(server.puts).toEqual([{ sid: A, prefs: { reading_direction: 'rtl' } }])
    expect(JSON.parse(localStorage.getItem(SERIES_DIR_STORAGE_KEY) ?? 'null')).toEqual({
      state: { bySeries: { [B]: 'rtl' } },
      version: 0,
    })
  })

  it('runs once: two calls at the same time make one GET and one PUT per seed', async () => {
    plant({ [A]: 'rtl', [B]: 'ltr' })
    const server = fakeServer({ [A]: null, [B]: null })
    await Promise.all([migrateSeriesDirSeeds(server), migrateSeriesDirSeeds(server)])
    await migrateSeriesDirSeeds(server)
    expect(server.gets.sort()).toEqual([A, B])
    expect(server.puts.map((p) => p.sid).sort()).toEqual([A, B])
  })

  it('asks the server nothing when there is no seed', async () => {
    const server = fakeServer({ [A]: null })
    await migrateSeriesDirSeeds(server)
    expect(server.gets).toEqual([])
  })

  it('drops an entry that is not a series id or not a direction, without asking', async () => {
    plant({ 'not-an-id': 'rtl', [A]: 'up', [B]: 'ltr' })
    const server = fakeServer({ [B]: null })
    await migrateSeriesDirSeeds(server)
    expect(server.gets).toEqual([B])
    expect(localStorage.getItem(SERIES_DIR_STORAGE_KEY)).toBeNull()
  })

  it('removes a key it cannot read at all', async () => {
    localStorage.setItem(SERIES_DIR_STORAGE_KEY, '{not json')
    const server = fakeServer({})
    await migrateSeriesDirSeeds(server)
    expect(server.gets).toEqual([])
    expect(localStorage.getItem(SERIES_DIR_STORAGE_KEY)).toBeNull()
  })

  it('survives a storage that throws, and asks nothing', async () => {
    const getItem = vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new DOMException('denied', 'SecurityError')
    })
    const server = fakeServer({ [A]: null })
    await expect(migrateSeriesDirSeeds(server)).resolves.toBeDefined()
    expect(server.gets).toEqual([])
    getItem.mockRestore()
  })

  it('survives a storage whose write throws, after the upload', async () => {
    plant({ [A]: 'rtl' })
    const removeItem = vi.spyOn(Storage.prototype, 'removeItem').mockImplementation(() => {
      throw new DOMException('denied', 'SecurityError')
    })
    const server = fakeServer({ [A]: null })
    await expect(migrateSeriesDirSeeds(server)).resolves.toBeDefined()
    expect(server.puts).toHaveLength(1)
    removeItem.mockRestore()
  })
})

// ---------------------------------------------------------------------------
// The hook ShellDataProvider mounts — through the real client, against MSW
// ---------------------------------------------------------------------------

const msw = setupServer()

beforeAll(() => {
  msw.listen({ onUnhandledRequest: failUnhandled })
})
afterEach(() => {
  msw.resetHandlers()
  resetBasePath()
})
afterAll(() => {
  msw.close()
})

function wrapper(client: QueryClient) {
  return function Wrapper({ children }: { children: ReactNode }) {
    return <QueryClientProvider client={client}>{children}</QueryClientProvider>
  }
}

describe('useSeriesDirSeedMigration', () => {
  it('uploads through the real endpoints once the shell may talk to the server, and re-reads the series', async () => {
    plant({ [SERIES_ID]: 'rtl' })
    const puts: unknown[] = []
    msw.use(
      http.get(`${ORIGIN}/api/series/${SERIES_ID}`, () => HttpResponse.json(seriesDetail)),
      http.put(`${ORIGIN}/api/series/${SERIES_ID}/prefs`, async ({ request }) => {
        puts.push(await request.json())
        return HttpResponse.json({ reading_direction: 'rtl' })
      }),
    )
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    // One entry of each family the new direction moves (E-69 §2.3: every book's
    // effective prefs and detail; §2.7: every default cover's `cover_cv`, which the
    // volume list and 이어보기 carry), and one it does not.
    client.setQueryData(queryKeys.series.detail(SERIES_ID), seriesDetail)
    client.setQueryData(queryKeys.books.detail(BOOK_ID), bookDetail)
    client.setQueryData(queryKeys.books.prefs(BOOK_ID), bookPrefs)
    client.setQueryData(queryKeys.continueReading.list(20), continueResponse)
    client.setQueryData(queryKeys.cache.usage, cacheUsage)
    renderHook(
      () => {
        useSeriesDirSeedMigration({ auth_required: false, authenticated: false, mode: 'none', end_session_available: false })
      },
      { wrapper: wrapper(client) },
    )
    await waitFor(() => {
      expect(puts).toEqual([{ reading_direction: 'rtl' }])
    })
    const invalidated = (key: readonly unknown[]) => client.getQueryState(key)?.isInvalidated
    await waitFor(() => {
      expect(invalidated(queryKeys.series.detail(SERIES_ID)), 'series.detail').toBe(true)
    })
    expect(invalidated(queryKeys.books.detail(BOOK_ID)), 'books.detail').toBe(true)
    expect(invalidated(queryKeys.books.prefs(BOOK_ID)), 'books.prefs').toBe(true)
    expect(invalidated(queryKeys.continueReading.list(20)), 'continue').toBe(true)
    expect(invalidated(queryKeys.cache.usage), 'cache.usage').toBe(false)
    expect(localStorage.getItem(SERIES_DIR_STORAGE_KEY)).toBeNull()
  })

  /**
   * The re-read belongs to each seed the server took, at the moment it took it — not
   * to the end of the upload. A later series' request can be on the wire for as long
   * as it likes; the series already moved must not wait for it (the viewer opens a
   * cached volume of it on the old direction until its detail is invalidated).
   */
  it('re-reads a series’ families as soon as its seed is taken, while the next seed’s GET is still out', async () => {
    const OTHER = 'otherseries22222'
    plant({ [SERIES_ID]: 'rtl', [OTHER]: 'ltr' })
    let release = (): void => undefined
    const held = new Promise<void>((resolve) => {
      release = resolve
    })
    let otherAsked = false
    msw.use(
      http.get(`${ORIGIN}/api/series/${SERIES_ID}`, () => HttpResponse.json(seriesDetail)),
      http.put(`${ORIGIN}/api/series/${SERIES_ID}/prefs`, () =>
        HttpResponse.json({ reading_direction: 'rtl' }),
      ),
      http.get(`${ORIGIN}/api/series/${OTHER}`, async () => {
        otherAsked = true
        await held
        return HttpResponse.json({ ...seriesDetail, id: OTHER, reading_direction: 'ltr' })
      }),
    )
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    client.setQueryData(queryKeys.series.detail(SERIES_ID), seriesDetail)
    client.setQueryData(queryKeys.books.detail(BOOK_ID), bookDetail)
    client.setQueryData(queryKeys.books.prefs(BOOK_ID), bookPrefs)
    client.setQueryData(queryKeys.continueReading.list(20), continueResponse)
    client.setQueryData(queryKeys.cache.usage, cacheUsage)
    renderHook(
      () => {
        useSeriesDirSeedMigration({ auth_required: false, authenticated: false, mode: 'none', end_session_available: false })
      },
      { wrapper: wrapper(client) },
    )
    try {
      await waitFor(() => {
        expect(otherAsked).toBe(true)
      })
      const invalidated = (key: readonly unknown[]) => client.getQueryState(key)?.isInvalidated
      expect(invalidated(queryKeys.series.detail(SERIES_ID)), 'series.detail').toBe(true)
      expect(invalidated(queryKeys.books.detail(BOOK_ID)), 'books.detail').toBe(true)
      expect(invalidated(queryKeys.books.prefs(BOOK_ID)), 'books.prefs').toBe(true)
      expect(invalidated(queryKeys.continueReading.list(20)), 'continue').toBe(true)
      expect(invalidated(queryKeys.cache.usage), 'cache.usage').toBe(false)
    } finally {
      release()
      await waitFor(() => {
        expect(localStorage.getItem(SERIES_DIR_STORAGE_KEY)).toBeNull()
      })
    }
  })

  it('re-reads nothing when no seed was written — a dropped seed moved no direction', async () => {
    plant({ [SERIES_ID]: 'rtl' })
    msw.use(
      // The server already has a direction: the seed is dropped without a PUT.
      http.get(`${ORIGIN}/api/series/${SERIES_ID}`, () =>
        HttpResponse.json({ ...seriesDetail, reading_direction: 'ltr' }),
      ),
    )
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    client.setQueryData(queryKeys.series.detail(SERIES_ID), seriesDetail)
    client.setQueryData(queryKeys.books.detail(BOOK_ID), bookDetail)
    renderHook(
      () => {
        useSeriesDirSeedMigration({ auth_required: false, authenticated: false, mode: 'none', end_session_available: false })
      },
      { wrapper: wrapper(client) },
    )
    await waitFor(() => {
      expect(localStorage.getItem(SERIES_DIR_STORAGE_KEY)).toBeNull()
    })
    await migrateSeriesDirSeeds(fakeServer({}))
    expect(client.getQueryState(queryKeys.series.detail(SERIES_ID))?.isInvalidated).toBe(false)
    expect(client.getQueryState(queryKeys.books.detail(BOOK_ID))?.isInvalidated).toBe(false)
  })

  /**
   * E-69 §2.3 asks for the seed only where the server still has nothing — so a
   * series the reader sets while the upload runs is not one it may write. Under
   * the old code this was a race the seed won: the upload's GET read `null`, the
   * reader chose L→R (PUT, applied), and then the upload's own PUT of the seed
   * landed last and put R→L back over the reader's choice.
   */
  describe('against the reader’s own writes (one queue with the `.seg`)', () => {
    const READY: AuthStatus = { auth_required: false, authenticated: false, mode: 'none', end_session_available: false }

    /**
     * The series GET reads the stored value **when it arrives** and its answer is
     * then held in transit — "the upload already saw null" is a state the test
     * can stand in, rather than one it hopes to hit.
     */
    function heldSeriesGet(mock: CoverMock): { release: () => void; arrived: () => number } {
      let release = (): void => undefined
      const held = new Promise<void>((resolve) => {
        release = resolve
      })
      let arrived = 0
      msw.use(
        http.get(`${ORIGIN}/api/series/${SERIES_ID}`, async () => {
          arrived += 1
          const answer = { ...seriesDetail, reading_direction: mock.seriesDirs.get(SERIES_ID) ?? null }
          await held
          return HttpResponse.json(answer)
        }),
        ...mock.handlers,
      )
      return { release, arrived: () => arrived }
    }

    function bootWithSeg(client: QueryClient, auth: AuthStatus | undefined = READY) {
      return renderHook<ReturnType<typeof useSetSeriesDirection>, { auth: AuthStatus | undefined }>(
        ({ auth: status }) => {
          useSeriesDirSeedMigration(status)
          return useSetSeriesDirection()
        },
        { wrapper: wrapper(client), initialProps: { auth } },
      )
    }

    it.each([
      ['L→R', 'ltr'],
      ['기본 (null)', null],
    ] as const)(
      'a %s chosen while the upload’s GET is on its way stays — the seed is not PUT over it',
      async (_label, choice: ReadingDir | null) => {
        plant({ [SERIES_ID]: 'rtl' })
        const mock = createCoverMock(ORIGIN, { books: seriesDetail.books, series: [SERIES_ID] })
        const get = heldSeriesGet(mock)
        const client = new QueryClient({
          defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
        })
        const { result } = bootWithSeg(client)
        // The upload's GET has read the server's `null`; its answer is in transit.
        await waitFor(() => {
          expect(get.arrived()).toBe(1)
        })

        act(() => {
          result.current.mutate({ sid: SERIES_ID, reading_direction: choice })
        })
        await new Promise((resolve) => setTimeout(resolve, 30))
        get.release()

        await waitFor(() => {
          expect(result.current.isSuccess).toBe(true)
        })
        // The same run the hook started (a second caller shares it): wait it out.
        await migrateSeriesDirSeeds(fakeServer({}))
        expect(mock.seriesDirs.get(SERIES_ID)).toBe(choice)
        expect(mock.seriesPrefsPuts.map((p) => p.body)).toEqual([{ reading_direction: choice }])
        // The reader settled this series; nothing is left to offer on the next boot.
        expect(localStorage.getItem(SERIES_DIR_STORAGE_KEY)).toBeNull()
      },
    )

    it('a 기본 chosen before the upload reaches the series is not overwritten either', async () => {
      // `null` is a choice too (the `.seg`'s 기본): "the server has no value" is not
      // proof the reader did not just ask for none.
      plant({ [SERIES_ID]: 'rtl' })
      const mock = createCoverMock(ORIGIN, { books: seriesDetail.books, series: [SERIES_ID] })
      msw.use(
        http.get(`${ORIGIN}/api/series/${SERIES_ID}`, () =>
          HttpResponse.json({ ...seriesDetail, reading_direction: mock.seriesDirs.get(SERIES_ID) ?? null }),
        ),
        ...mock.handlers,
      )
      const client = new QueryClient({
        defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
      })
      const { result, rerender } = bootWithSeg(client, undefined)
      act(() => {
        result.current.mutate({ sid: SERIES_ID, reading_direction: null })
      })
      rerender({ auth: READY })

      await waitFor(() => {
        expect(result.current.isSuccess).toBe(true)
      })
      await migrateSeriesDirSeeds(fakeServer({}))
      expect(mock.seriesDirs.get(SERIES_ID)).toBeNull()
      expect(mock.seriesPrefsPuts.map((p) => p.body)).toEqual([{ reading_direction: null }])
    })

    it('a choice made while the upload’s PUT is on its way goes after it, so it is the last one', async () => {
      plant({ [SERIES_ID]: 'rtl' })
      const mock = createCoverMock(ORIGIN, { books: seriesDetail.books, series: [SERIES_ID] })
      let release = (): void => undefined
      const held = new Promise<void>((resolve) => {
        release = resolve
      })
      const order: string[] = []
      msw.use(
        http.get(`${ORIGIN}/api/series/${SERIES_ID}`, () =>
          HttpResponse.json({ ...seriesDetail, reading_direction: mock.seriesDirs.get(SERIES_ID) ?? null }),
        ),
        // Falls through to the mock once released: the seed's PUT is held before
        // the server applies it.
        http.put(`${ORIGIN}/api/series/${SERIES_ID}/prefs`, async ({ request }) => {
          const body = (await request.clone().json()) as { reading_direction: ReadingDir | null }
          order.push(`arrive ${String(body.reading_direction)}`)
          if (body.reading_direction === 'rtl') await held
        }),
        ...mock.handlers,
      )
      const client = new QueryClient({
        defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
      })
      const { result } = bootWithSeg(client)
      await waitFor(() => {
        expect(order).toEqual(['arrive rtl'])
      })

      act(() => {
        result.current.mutate({ sid: SERIES_ID, reading_direction: 'ltr' })
      })
      await new Promise((resolve) => setTimeout(resolve, 30))
      // Queued behind the seed, not beside it.
      expect(order).toEqual(['arrive rtl'])
      release()

      await waitFor(() => {
        expect(result.current.isSuccess).toBe(true)
      })
      expect(order).toEqual(['arrive rtl', 'arrive ltr'])
      expect(mock.seriesDirs.get(SERIES_ID)).toBe('ltr')
    })
  })

  it('waits while the auth status is unknown or the login screen is up — a 401 would only be kept', async () => {
    plant({ [SERIES_ID]: 'rtl' })
    const client = new QueryClient()
    const { rerender } = renderHook<null, { auth: AuthStatus | undefined }>(
      ({ auth }) => {
        useSeriesDirSeedMigration(auth)
        return null
      },
      { wrapper: wrapper(client), initialProps: { auth: undefined } },
    )
    rerender({ auth: { auth_required: true, authenticated: false, mode: 'password', end_session_available: false } })
    await new Promise((resolve) => setTimeout(resolve, 30))
    // No handler is registered: any request would fail this test (src/test/msw.ts).
    expect(left()).toEqual({ [SERIES_ID]: 'rtl' })
  })
})
