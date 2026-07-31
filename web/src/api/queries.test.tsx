/**
 * The query-layer policies of WP-06 acceptance 4, tested against MSW:
 * 1 s scan polling that stops at idle, `202` → `Retry-After` retries capped at 10,
 * `409 stale_version` → invalidate the book and retry once, `401` → the auth status
 * flips, and a 1 s-debounced, idempotent progress write.
 */

import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { renderHook, waitFor } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { setupServer } from 'msw/node'
import type { ReactNode } from 'react'
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'

import { listSeries } from './client'
import { isApiError, isAuthError } from './errors'
import {
  authStatus,
  BOOK_CV,
  BOOK_ID,
  bookDetail,
  bookPrefs,
  cacheUsage,
  continueResponse,
  COVER_CV,
  errorEnvelope,
  ORIGIN,
  progress,
  rootEntry,
  rootsResponse,
  scanLogResponse,
  scanStatusIdle,
  scanStatusRunning,
  SERIES_ID,
  seriesDetail,
  seriesListResponse,
  seriesSummary,
  settings,
} from './fixtures'
import {
  MAX_IMAGE_ATTEMPTS,
  queryKeys,
  SCAN_POLL_MS,
  useAuthStatus,
  useBook,
  useCacheUsage,
  useCancelScan,
  useContinue,
  useCoverImage,
  useCreateRoot,
  useDeleteProgress,
  useDeleteRoot,
  usePageThumbImage,
  usePurgeCache,
  useRescanSeries,
  useRoots,
  useSaveProgress,
  useSaveSettings,
  useScanLog,
  useScanStatus,
  useSeries,
  useSeriesList,
  useSeriesListInfinite,
  useSetPrefs,
  useSettings,
  useStartScan,
} from './queries'
import { resetBasePath } from './urls'

const server = setupServer()

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

function makeClient(): QueryClient {
  return new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
}

function makeWrapper(client: QueryClient) {
  return function Wrapper({ children }: { children: ReactNode }) {
    return <QueryClientProvider client={client}>{children}</QueryClientProvider>
  }
}

// ---------------------------------------------------------------------------
// Query keys
// ---------------------------------------------------------------------------

describe('query keys (impl-plan §5.3)', () => {
  it('are exported arrays with a stable prefix per resource', () => {
    expect(queryKeys.roots).toEqual(['roots'])
    expect(queryKeys.series.detail(SERIES_ID)).toEqual(['series', 'detail', SERIES_ID])
    expect(queryKeys.series.list({ sort: 'name' })).toEqual(['series', 'list', { sort: 'name' }])
    expect(queryKeys.books.detail(BOOK_ID)).toEqual(['books', 'detail', BOOK_ID])
    expect(queryKeys.scan.status).toEqual(['scan', 'status'])
    expect(queryKeys.image.cover(SERIES_ID, 400, COVER_CV)).toEqual([
      'image',
      'cover',
      SERIES_ID,
      400,
      COVER_CV,
    ])
  })
})

// ---------------------------------------------------------------------------
// useSeriesList
// ---------------------------------------------------------------------------

describe('useSeriesList', () => {
  it('forwards its params to the request and returns the page', async () => {
    let seenQuery = ''
    server.use(
      http.get(`${ORIGIN}/api/series`, ({ request }) => {
        seenQuery = new URL(request.url).search
        return HttpResponse.json(seriesListResponse)
      }),
    )
    const client = makeClient()
    const { result } = renderHook(
      () => useSeriesList({ root: ['mangga'], progress: 'reading', sort: 'recent' }),
      { wrapper: makeWrapper(client) },
    )

    await waitFor(() => {
      expect(result.current.data).toBeDefined()
    })
    expect(result.current.data?.total).toBe(10)
    const params = new URLSearchParams(seenQuery)
    expect(params.getAll('root')).toEqual(['mangga'])
    expect(params.get('progress')).toBe('reading')
    expect(params.get('sort')).toBe('recent')
  })
})

describe('useSeriesListInfinite (FR-LIB-007)', () => {
  it('pages by offset until total is reached', async () => {
    server.use(
      http.get(`${ORIGIN}/api/series`, ({ request }) => {
        const offset = Number(new URL(request.url).searchParams.get('offset') ?? '0')
        return HttpResponse.json({
          items: offset === 0 ? [seriesSummary] : [{ ...seriesSummary, id: 'secondpage22222' }],
          total: 2,
          offset,
          limit: 1,
        })
      }),
    )
    const client = makeClient()
    const { result } = renderHook(() => useSeriesListInfinite({ limit: 1 }), {
      wrapper: makeWrapper(client),
    })

    await waitFor(() => {
      expect(result.current.data?.pages).toHaveLength(1)
    })
    expect(result.current.hasNextPage).toBe(true)

    await result.current.fetchNextPage()

    await waitFor(() => {
      expect(result.current.data?.pages).toHaveLength(2)
    })
    expect(result.current.data?.pages[1]?.offset).toBe(1)
    expect(result.current.hasNextPage).toBe(false)
  })
})

// ---------------------------------------------------------------------------
// Read hooks
// ---------------------------------------------------------------------------

describe('read hooks resolve their endpoint', () => {
  it('useRoots / useSeries / useBook / useContinue / useSettings / useCacheUsage / useScanLog', async () => {
    server.use(
      http.get(`${ORIGIN}/api/roots`, () => HttpResponse.json(rootsResponse)),
      http.get(`${ORIGIN}/api/series/${SERIES_ID}`, () => HttpResponse.json(seriesDetail)),
      http.get(`${ORIGIN}/api/books/${BOOK_ID}`, () => HttpResponse.json(bookDetail)),
      http.get(`${ORIGIN}/api/continue`, () => HttpResponse.json(continueResponse)),
      http.get(`${ORIGIN}/api/settings`, () => HttpResponse.json(settings)),
      http.get(`${ORIGIN}/api/cache/usage`, () => HttpResponse.json(cacheUsage)),
      http.get(`${ORIGIN}/api/scan/log`, () => HttpResponse.json(scanLogResponse)),
    )
    const client = makeClient()
    const { result } = renderHook(
      () => ({
        roots: useRoots(),
        series: useSeries(SERIES_ID),
        book: useBook(BOOK_ID),
        continueReading: useContinue(20),
        settings: useSettings(),
        cache: useCacheUsage(),
        log: useScanLog({ limit: 200 }),
      }),
      { wrapper: makeWrapper(client) },
    )

    await waitFor(() => {
      expect(result.current.log.data).toBeDefined()
    })
    expect(result.current.roots.data?.items).toHaveLength(1)
    expect(result.current.series.data?.books).toHaveLength(2)
    expect(result.current.book.data?.pages).toHaveLength(3)
    expect(result.current.continueReading.data?.items[0]?.series_id).toBe(SERIES_ID)
    expect(result.current.settings.data?.library_view).toBe('grid')
    expect(result.current.cache.data?.total_bytes).toBe(285_000_000)
    expect(result.current.log.data?.items[0]?.level).toBe('warn')
  })

  it('does not fetch while disabled', async () => {
    let requests = 0
    server.use(
      http.get(`${ORIGIN}/api/roots`, () => {
        requests += 1
        return HttpResponse.json(rootsResponse)
      }),
    )
    const client = makeClient()
    renderHook(() => useRoots({ enabled: false }), { wrapper: makeWrapper(client) })
    await new Promise((resolve) => setTimeout(resolve, 30))
    expect(requests).toBe(0)
  })
})

// ---------------------------------------------------------------------------
// Scan polling (C-11 / FR-IDX-004)
// ---------------------------------------------------------------------------

describe('useScanStatus polling', () => {
  it(`polls every ${String(SCAN_POLL_MS)} ms while state !== "idle" and stops once idle`, async () => {
    let requests = 0
    server.use(
      http.get(`${ORIGIN}/api/scan/status`, () => {
        requests += 1
        return HttpResponse.json(requests < 3 ? scanStatusRunning : scanStatusIdle)
      }),
    )
    const client = makeClient()
    const { result } = renderHook(() => useScanStatus(), { wrapper: makeWrapper(client) })

    await waitFor(
      () => {
        expect(result.current.data?.state).toBe('idle')
      },
      { timeout: 5_000 },
    )
    // Two running answers, then idle: the interval fired, so it polled.
    expect(requests).toBe(3)

    const settled = requests
    await new Promise((resolve) => setTimeout(resolve, SCAN_POLL_MS * 2))
    expect(requests).toBe(settled)
  }, 12_000)
})

// ---------------------------------------------------------------------------
// 202 covers and thumbnails (impl-plan §4 rule 3)
// ---------------------------------------------------------------------------

describe('queued images', () => {
  it('reports "queued" on 202 and becomes "ready" after the retry', async () => {
    let requests = 0
    server.use(
      http.get(`${ORIGIN}/api/series/${SERIES_ID}/cover`, () => {
        requests += 1
        return requests < 3
          ? new HttpResponse(null, { status: 202, headers: { 'Retry-After': '0' } })
          : new HttpResponse('jpegbytes', { headers: { 'Content-Type': 'image/jpeg' } })
      }),
    )
    const client = makeClient()
    const seen: string[] = []
    const { result } = renderHook(
      () => {
        const state = useCoverImage(SERIES_ID, { w: 400, v: COVER_CV })
        seen.push(state.status)
        return state
      },
      { wrapper: makeWrapper(client) },
    )

    await waitFor(() => {
      expect(result.current.status).toBe('ready')
    })
    // The queued state is observable — it is what drives the skeleton, not an error.
    expect(seen).toContain('queued')
    expect(seen.indexOf('queued')).toBeLessThan(seen.lastIndexOf('ready'))
    expect(result.current.url).toBe(
      `/api/series/${SERIES_ID}/cover?w=400&v=${COVER_CV}`,
    )
    expect(requests).toBe(3)
  })

  it(`gives up after ${String(MAX_IMAGE_ATTEMPTS)} attempts and falls back to the placeholder`, async () => {
    let requests = 0
    server.use(
      http.get(`${ORIGIN}/api/series/${SERIES_ID}/cover`, () => {
        requests += 1
        return new HttpResponse(null, { status: 202, headers: { 'Retry-After': '0' } })
      }),
    )
    const client = makeClient()
    const { result } = renderHook(
      () => useCoverImage(SERIES_ID, { w: 240, v: COVER_CV }),
      { wrapper: makeWrapper(client) },
    )

    await waitFor(
      () => {
        expect(result.current.status).toBe('unavailable')
      },
      { timeout: 5_000 },
    )
    expect(requests).toBe(MAX_IMAGE_ATTEMPTS)
    // A 202 is not a contract error, so nothing is surfaced to the UI as one.
    expect(result.current.error).toBeNull()
  })

  it('surfaces 404 as unavailable without retrying (a series with no cover)', async () => {
    let requests = 0
    server.use(
      http.get(`${ORIGIN}/api/series/${SERIES_ID}/cover`, () => {
        requests += 1
        return HttpResponse.json(errorEnvelope('not_found', 'no cover'), { status: 404 })
      }),
    )
    const client = makeClient()
    const { result } = renderHook(
      () => useCoverImage(SERIES_ID, { w: 120, v: null }),
      { wrapper: makeWrapper(client) },
    )

    await waitFor(() => {
      expect(result.current.status).toBe('unavailable')
    })
    expect(requests).toBe(1)
    expect(result.current.error?.code).toBe('not_found')
  })
})

// ---------------------------------------------------------------------------
// 409 stale_version
// ---------------------------------------------------------------------------

describe('stale_version handling', () => {
  it('invalidates the book query and retries exactly once', async () => {
    let requests = 0
    server.use(
      http.get(`${ORIGIN}/api/books/${BOOK_ID}/thumbs/7`, () => {
        requests += 1
        return HttpResponse.json(
          errorEnvelope('stale_version', 'content version changed', { cv: 'freshcv123456789' }),
          { status: 409 },
        )
      }),
    )
    const client = makeClient()
    client.setQueryData(queryKeys.books.detail(BOOK_ID), bookDetail)

    const { result } = renderHook(
      () => usePageThumbImage(BOOK_ID, 7, { w: 120, v: BOOK_CV }),
      { wrapper: makeWrapper(client) },
    )

    await waitFor(
      () => {
        expect(result.current.status).toBe('unavailable')
      },
      { timeout: 5_000 },
    )
    expect(requests).toBe(2)
    expect(result.current.error?.code).toBe('stale_version')
    expect(result.current.error?.staleVersion).toBe('freshcv123456789')
    expect(client.getQueryState(queryKeys.books.detail(BOOK_ID))?.isInvalidated).toBe(true)
  })
})

// ---------------------------------------------------------------------------
// 401 → login screen
// ---------------------------------------------------------------------------

describe('unauthorized handling', () => {
  it('re-reads the auth status after any 401, so the shell can show the login screen', async () => {
    let statusRequests = 0
    server.use(
      http.get(`${ORIGIN}/api/auth/status`, () => {
        statusRequests += 1
        return HttpResponse.json(
          statusRequests === 1 ? { auth_required: true, authenticated: true } : authStatus,
        )
      }),
      http.get(`${ORIGIN}/api/series`, () =>
        HttpResponse.json(errorEnvelope('unauthorized', 'session expired'), { status: 401 }),
      ),
    )
    const client = makeClient()
    const { result } = renderHook(() => useAuthStatus(), { wrapper: makeWrapper(client) })

    await waitFor(() => {
      expect(result.current.data?.authenticated).toBe(true)
    })

    // Any other request expiring the session must flip the shell.
    await listSeries().catch(() => undefined)

    await waitFor(() => {
      expect(result.current.data?.authenticated).toBe(false)
    })
    expect(result.current.data?.auth_required).toBe(true)
    expect(statusRequests).toBe(2)
  })
})

// ---------------------------------------------------------------------------
// Progress (FR-STT-001, FR-VWR-009)
// ---------------------------------------------------------------------------

describe('useSaveProgress', () => {
  it('debounces a burst of page turns into one idempotent write', async () => {
    const bodies: unknown[] = []
    server.use(
      http.put(`${ORIGIN}/api/books/${BOOK_ID}/progress`, async ({ request }) => {
        bodies.push(await request.json())
        return HttpResponse.json({ ...progress, last_page: 45 })
      }),
    )
    const client = makeClient()
    client.setQueryData(queryKeys.books.detail(BOOK_ID), bookDetail)
    const { result } = renderHook(() => useSaveProgress(BOOK_ID, { debounceMs: 20 }), {
      wrapper: makeWrapper(client),
    })

    result.current.save(43)
    result.current.save(44)
    result.current.save(45)

    await waitFor(() => {
      expect(bodies).toHaveLength(1)
    })
    expect(bodies[0]).toEqual({ page: 45 })

    // The book already in cache is patched, so the viewer sees its own write.
    await waitFor(() => {
      expect(
        client.getQueryData<typeof bookDetail>(queryKeys.books.detail(BOOK_ID))?.progress
          ?.last_page,
      ).toBe(45)
    })
  })

  it('flush() writes immediately — for unmount and visibilitychange', async () => {
    const bodies: unknown[] = []
    server.use(
      http.put(`${ORIGIN}/api/books/${BOOK_ID}/progress`, async ({ request }) => {
        bodies.push(await request.json())
        return HttpResponse.json(progress)
      }),
    )
    const client = makeClient()
    const { result } = renderHook(
      () => useSaveProgress(BOOK_ID, { debounceMs: 60_000 }),
      { wrapper: makeWrapper(client) },
    )

    result.current.save(187, true)
    result.current.flush()

    await waitFor(() => {
      expect(bodies).toHaveLength(1)
    })
    expect(bodies[0]).toEqual({ page: 187, completed: true })

    // Nothing is pending any more, so a second flush is a no-op.
    result.current.flush()
    await new Promise((resolve) => setTimeout(resolve, 20))
    expect(bodies).toHaveLength(1)
  })

  it('lands a pending write on the book it was made for when bid changes (FR-VWR-010)', async () => {
    // "다음 권" only changes the :bid route param, so React Router v7 keeps the viewer
    // mounted. A debounced page from book A must never be written to book B.
    const NEXT_BOOK_ID = 'zzq81kdmp3v7hs2e'
    const writes: { bid: string; body: unknown }[] = []
    server.use(
      http.put(`${ORIGIN}/api/books/:bid/progress`, async ({ params, request }) => {
        writes.push({ bid: String(params.bid), body: await request.json() })
        return HttpResponse.json({ ...progress, book_id: String(params.bid), last_page: 500 })
      }),
    )
    const client = makeClient()
    const { result, rerender } = renderHook(
      ({ bid }: { bid: string }) => useSaveProgress(bid, { debounceMs: 60_000 }),
      { wrapper: makeWrapper(client), initialProps: { bid: BOOK_ID } },
    )

    result.current.save(500)
    rerender({ bid: NEXT_BOOK_ID })

    await waitFor(() => {
      expect(writes).toHaveLength(1)
    })
    expect(writes[0]).toEqual({ bid: BOOK_ID, body: { page: 500 } })

    // The next book's own write goes to the next book, and only there.
    result.current.save(1)
    result.current.flush()

    await waitFor(() => {
      expect(writes).toHaveLength(2)
    })
    expect(writes[1]).toEqual({ bid: NEXT_BOOK_ID, body: { page: 1 } })
    expect(client.getQueryData(queryKeys.books.detail(BOOK_ID))).toBeUndefined()
  })

  it('patches the cache of the book the write was addressed to', async () => {
    const OTHER_BOOK_ID = 'kk40wr9tzc1lb6xd'
    server.use(
      http.put(`${ORIGIN}/api/books/:bid/progress`, ({ params }) =>
        HttpResponse.json({ ...progress, book_id: String(params.bid), last_page: 77 }),
      ),
    )
    const client = makeClient()
    client.setQueryData(queryKeys.books.detail(BOOK_ID), bookDetail)
    client.setQueryData(queryKeys.books.detail(OTHER_BOOK_ID), {
      ...bookDetail,
      id: OTHER_BOOK_ID,
    })
    const { result, rerender } = renderHook(
      ({ bid }: { bid: string }) => useSaveProgress(bid, { debounceMs: 60_000 }),
      { wrapper: makeWrapper(client), initialProps: { bid: BOOK_ID } },
    )

    result.current.save(77)
    rerender({ bid: OTHER_BOOK_ID })

    await waitFor(() => {
      expect(
        client.getQueryData<typeof bookDetail>(queryKeys.books.detail(BOOK_ID))?.progress
          ?.last_page,
      ).toBe(77)
    })
    expect(
      client.getQueryData<typeof bookDetail>(queryKeys.books.detail(OTHER_BOOK_ID))?.progress
        ?.last_page,
    ).toBe(bookDetail.progress?.last_page)
  })

  it('refuses a 0 or negative page — there is no page 0', () => {
    const client = makeClient()
    const { result } = renderHook(() => useSaveProgress(BOOK_ID, { debounceMs: 5 }), {
      wrapper: makeWrapper(client),
    })
    expect(() => {
      result.current.save(0)
    }).toThrow(RangeError)
  })
})

// ---------------------------------------------------------------------------
// Mutations that must re-arm other queries
// ---------------------------------------------------------------------------

describe('mutations invalidate what they change', () => {
  it('rescanning a series re-arms the scan status poll', async () => {
    server.use(
      http.post(`${ORIGIN}/api/series/${SERIES_ID}/rescan`, () =>
        HttpResponse.json({ run_id: 'run-9' }, { status: 202 }),
      ),
    )
    const client = makeClient()
    client.setQueryData(queryKeys.scan.status, scanStatusIdle)
    const { result } = renderHook(() => useRescanSeries(), { wrapper: makeWrapper(client) })

    result.current.mutate(SERIES_ID)

    await waitFor(() => {
      expect(result.current.data?.run_id).toBe('run-9')
    })
    expect(client.getQueryState(queryKeys.scan.status)?.isInvalidated).toBe(true)
  })

  it('starting and cancelling a scan both re-arm the status poll', async () => {
    server.use(
      http.post(`${ORIGIN}/api/scan`, () => HttpResponse.json({ run_id: 'run-3' }, { status: 202 })),
      http.post(`${ORIGIN}/api/scan/cancel`, () => new HttpResponse(null, { status: 204 })),
    )
    const client = makeClient()
    client.setQueryData(queryKeys.scan.status, scanStatusIdle)
    const { result } = renderHook(
      () => ({ start: useStartScan(), cancel: useCancelScan() }),
      { wrapper: makeWrapper(client) },
    )

    result.current.start.mutate({ full: true })
    await waitFor(() => {
      expect(result.current.start.data?.run_id).toBe('run-3')
    })
    expect(client.getQueryState(queryKeys.scan.status)?.isInvalidated).toBe(true)

    client.setQueryData(queryKeys.scan.status, scanStatusRunning)
    result.current.cancel.mutate()
    await waitFor(() => {
      expect(result.current.cancel.isSuccess).toBe(true)
    })
    expect(client.getQueryState(queryKeys.scan.status)?.isInvalidated).toBe(true)
  })

  it('saving settings replaces the cached settings with the server answer', async () => {
    server.use(
      http.put(`${ORIGIN}/api/settings`, () =>
        HttpResponse.json({ ...settings, theme: 'dark' as const }),
      ),
    )
    const client = makeClient()
    const { result } = renderHook(() => useSaveSettings(), { wrapper: makeWrapper(client) })

    result.current.mutate({ theme: 'dark' })

    await waitFor(() => {
      expect(client.getQueryData<typeof settings>(queryKeys.settings)?.theme).toBe('dark')
    })
  })

  it('setting per-book prefs patches both the prefs key and the book detail (FR-VWR-002)', async () => {
    const updated = { ...bookPrefs, reading_direction: 'ltr' as const }
    server.use(
      http.put(`${ORIGIN}/api/books/${BOOK_ID}/prefs`, () => HttpResponse.json(updated)),
    )
    const client = makeClient()
    client.setQueryData(queryKeys.books.detail(BOOK_ID), bookDetail)
    const { result } = renderHook(() => useSetPrefs(BOOK_ID), { wrapper: makeWrapper(client) })

    result.current.mutate({ reading_direction: 'ltr' })

    await waitFor(() => {
      expect(client.getQueryData<typeof bookPrefs>(queryKeys.books.prefs(BOOK_ID))).toEqual(updated)
    })
    expect(
      client.getQueryData<typeof bookDetail>(queryKeys.books.detail(BOOK_ID))?.prefs
        .reading_direction,
    ).toBe('ltr')
  })

  it('marking a book unread invalidates the book, its series and 이어보기 (FR-VWR-012)', async () => {
    server.use(
      http.delete(
        `${ORIGIN}/api/books/${BOOK_ID}/progress`,
        () => new HttpResponse(null, { status: 204 }),
      ),
    )
    const client = makeClient()
    client.setQueryData(queryKeys.books.detail(BOOK_ID), bookDetail)
    client.setQueryData(queryKeys.series.detail(SERIES_ID), seriesDetail)
    client.setQueryData(queryKeys.continueReading.list(20), continueResponse)
    const { result } = renderHook(() => useDeleteProgress(), { wrapper: makeWrapper(client) })

    result.current.mutate(BOOK_ID)

    await waitFor(() => {
      expect(result.current.isSuccess).toBe(true)
    })
    expect(client.getQueryState(queryKeys.books.detail(BOOK_ID))?.isInvalidated).toBe(true)
    expect(client.getQueryState(queryKeys.series.detail(SERIES_ID))?.isInvalidated).toBe(true)
    expect(client.getQueryState(queryKeys.continueReading.list(20))?.isInvalidated).toBe(true)
  })

  it('purging the cache invalidates cache usage and every image', async () => {
    server.use(
      http.delete(`${ORIGIN}/api/cache`, () =>
        HttpResponse.json({ deleted_files: 10, freed_bytes: 1_024 }),
      ),
    )
    const client = makeClient()
    client.setQueryData(queryKeys.cache.usage, { total_bytes: 1 })
    client.setQueryData(queryKeys.image.cover(SERIES_ID, 400, COVER_CV), '/x')
    const { result } = renderHook(() => usePurgeCache(), { wrapper: makeWrapper(client) })

    result.current.mutate('all')

    await waitFor(() => {
      expect(result.current.data?.deleted_files).toBe(10)
    })
    expect(client.getQueryState(queryKeys.cache.usage)?.isInvalidated).toBe(true)
    expect(
      client.getQueryState(queryKeys.image.cover(SERIES_ID, 400, COVER_CV))?.isInvalidated,
    ).toBe(true)
  })

  /**
   * Amendment A-11 (ruling E-26). Both verbs change two server-side facts, so
   * both invalidate two queries:
   *
   *  * `roots` — the list itself. R2 makes `POST` produce a `pending` row and R1
   *    makes `DELETE` drop the row immediately, so in both cases the correct
   *    list is the one the server will answer with, never one assembled here.
   *  * `settings` — `server.config_changed_on_disk` is **always** true after a
   *    successful write (arch §7.8), and it is what drives the restart notice.
   *    A mutation that refreshed only the list would leave the screen telling
   *    the user nothing had changed on disk.
   */
  it('adding a root re-reads the roots list and the settings that describe the file', async () => {
    server.use(
      http.post(`${ORIGIN}/api/roots`, () => HttpResponse.json(rootEntry, { status: 201 })),
    )
    const client = makeClient()
    client.setQueryData(queryKeys.roots, rootsResponse)
    client.setQueryData(queryKeys.settings, settings)
    const { result } = renderHook(() => useCreateRoot(), { wrapper: makeWrapper(client) })

    result.current.mutate({ path: '/srv/media/manga' })

    await waitFor(() => {
      expect(result.current.data?.name).toBe(rootEntry.name)
    })
    expect(client.getQueryState(queryKeys.roots)?.isInvalidated).toBe(true)
    expect(client.getQueryState(queryKeys.settings)?.isInvalidated).toBe(true)
  })

  it('removing a root re-reads the same two queries', async () => {
    server.use(
      http.delete(`${ORIGIN}/api/roots/:name`, () => new HttpResponse(null, { status: 204 })),
    )
    const client = makeClient()
    client.setQueryData(queryKeys.roots, rootsResponse)
    client.setQueryData(queryKeys.settings, settings)
    const { result } = renderHook(() => useDeleteRoot(), { wrapper: makeWrapper(client) })

    result.current.mutate('lanovel')

    await waitFor(() => {
      expect(result.current.isSuccess).toBe(true)
    })
    expect(client.getQueryState(queryKeys.roots)?.isInvalidated).toBe(true)
    expect(client.getQueryState(queryKeys.settings)?.isInvalidated).toBe(true)
  })

  /**
   * A `403` must **not** reach `isAuthError` (ruling E-17): no login lifts a
   * refusal that lives in a YAML key, and sending an already-authenticated user
   * — or a user of the default password-less server (ruling E-8) — to a login
   * screen for it is the defect A-11 added the code to avoid.
   */
  it('surfaces a gated write as forbidden, and never as an auth failure', async () => {
    server.use(
      http.post(`${ORIGIN}/api/roots`, () =>
        HttpResponse.json(
          errorEnvelope('forbidden', 'root editing is disabled', { reason: 'disabled' }),
          { status: 403 },
        ),
      ),
    )
    const client = makeClient()
    client.setQueryData(queryKeys.auth.status, authStatus)
    const { result } = renderHook(() => useCreateRoot(), { wrapper: makeWrapper(client) })

    result.current.mutate({ path: '/srv/media/manga' })

    await waitFor(() => {
      expect(result.current.isError).toBe(true)
    })
    const error = result.current.error
    expect(isApiError(error)).toBe(true)
    expect(isApiError(error) ? error.code : null).toBe('forbidden')
    expect(isApiError(error) ? error.status : null).toBe(403)
    expect(isAuthError(error)).toBe(false)
    expect(client.getQueryState(queryKeys.auth.status)?.isInvalidated).toBe(false)
  })
})
