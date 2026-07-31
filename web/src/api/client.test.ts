/**
 * MSW-backed tests for the single fetch boundary.
 *
 * These are the executable form of the frozen contract (impl-plan §4, §6.1 row "06"):
 * every endpoint's happy path, the `202` retry path, the `status != "ok"` book shape,
 * the §7.2 error envelope, base-path prefixing and `?v=` propagation. WP-12 is being
 * written blind against the same document, so anything ambiguous is asserted here in the
 * literal form arch §7 states it.
 */

import { http, HttpResponse, type DefaultBodyType, type StrictRequest } from 'msw'
import { setupServer } from 'msw/node'
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from 'vitest'

import {
  cancelScan,
  deleteProgress,
  exportProgress,
  fetchImage,
  getAuthStatus,
  getBook,
  getBookPrefs,
  getCacheUsage,
  getContinue,
  getHealth,
  getRoots,
  getScanLog,
  getScanStatus,
  getSeries,
  getSettings,
  importProgress,
  listSeries,
  login,
  logout,
  parseRetryAfter,
  purgeCache,
  putBookPrefs,
  putProgress,
  putSettings,
  rescanSeries,
  startScan,
  subscribeUnauthorized,
} from './client'
import { isApiError, isAuthError, type ApiError } from './errors'
import {
  authStatus,
  BOOK_CV,
  BOOK_ID,
  bookDetail,
  bookPrefs,
  brokenBookDetail,
  cacheUsage,
  continueResponse,
  COVER_CV,
  errorEnvelope,
  health,
  ORIGIN,
  progress,
  progressExport,
  rootsResponse,
  scanLogResponse,
  scanStatusRunning,
  SERIES_ID,
  seriesDetail,
  seriesListResponse,
  settings,
} from './fixtures'
import { pageThumbUrl, resetBasePath, seriesCoverUrl, setBasePath } from './urls'

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

/** Registers a handler and hands back the request it received. */
function capture(
  method: 'get' | 'post' | 'put' | 'delete',
  path: string,
  respond: () => Response,
): { current: StrictRequest<DefaultBodyType> | null } {
  const box: { current: StrictRequest<DefaultBodyType> | null } = { current: null }
  server.use(
    http[method](`${ORIGIN}${path}`, ({ request }) => {
      box.current = request.clone() as StrictRequest<DefaultBodyType>
      return respond()
    }),
  )
  return box
}

// ---------------------------------------------------------------------------
// Transport
// ---------------------------------------------------------------------------

describe('transport', () => {
  it('sends Accept: application/json and same-origin credentials', async () => {
    const original = globalThis.fetch.bind(globalThis)
    const spy = vi.spyOn(globalThis, 'fetch')
    spy.mockImplementation((input, init) => original(input, init))
    server.use(http.get(`${ORIGIN}/api/roots`, () => HttpResponse.json(rootsResponse)))

    await getRoots()

    const call = spy.mock.calls[0]
    expect(call).toBeDefined()
    const [url, init] = call as [string, RequestInit]
    expect(url).toBe(`${ORIGIN}/api/roots`)
    expect(init.credentials).toBe('same-origin')
    expect((init.headers as Record<string, string>).Accept).toBe('application/json')
  })

  it('sends a JSON content type only when there is a body', async () => {
    const withBody = capture('put', `/api/books/${BOOK_ID}/progress`, () =>
      HttpResponse.json(progress),
    )
    const withoutBody = capture('get', '/api/settings', () => HttpResponse.json(settings))

    await putProgress(BOOK_ID, { page: 42 })
    await getSettings()

    expect(withBody.current?.headers.get('Content-Type')).toBe('application/json; charset=utf-8')
    expect(withoutBody.current?.headers.get('Content-Type')).toBeNull()
  })

  it('prefixes every request with base_path (NFR-SEC-003)', async () => {
    setBasePath('/reader')
    server.use(
      http.get(`${ORIGIN}/reader/api/series/${SERIES_ID}`, () => HttpResponse.json(seriesDetail)),
      // The un-prefixed path must never be hit; MSW errors on an unhandled request.
    )
    await expect(getSeries(SERIES_ID)).resolves.toEqual(seriesDetail)
  })

  it('rejects immediately for an already-aborted signal', async () => {
    server.use(http.get(`${ORIGIN}/api/roots`, () => HttpResponse.json(rootsResponse)))
    const controller = new AbortController()
    controller.abort()
    await expect(getRoots({ signal: controller.signal })).rejects.toThrow()
  })

  it('rejects when the signal aborts mid-flight', async () => {
    server.use(
      http.get(`${ORIGIN}/api/roots`, async () => {
        await new Promise((resolve) => setTimeout(resolve, 50))
        return HttpResponse.json(rootsResponse)
      }),
    )
    const controller = new AbortController()
    const pending = getRoots({ signal: controller.signal })
    controller.abort()
    await expect(pending).rejects.toThrow()
  })

  it('never fails a request because the runtime refuses the signal implementation', async () => {
    // Under vitest+jsdom, `fetch` is Node's and the signal is jsdom's; the request must
    // still go through (TanStack Query supplies a signal to every query function).
    server.use(http.get(`${ORIGIN}/api/roots`, () => HttpResponse.json(rootsResponse)))
    const controller = new AbortController()
    await expect(getRoots({ signal: controller.signal })).resolves.toEqual(rootsResponse)
  })
})

// ---------------------------------------------------------------------------
// Happy paths, endpoint by endpoint (§7.4 … §7.12)
// ---------------------------------------------------------------------------

describe('endpoints', () => {
  it('GET /api/health', async () => {
    server.use(http.get(`${ORIGIN}/api/health`, () => HttpResponse.json(health)))
    await expect(getHealth()).resolves.toEqual(health)
  })

  it('GET /api/roots', async () => {
    server.use(http.get(`${ORIGIN}/api/roots`, () => HttpResponse.json(rootsResponse)))
    const result = await getRoots()
    expect(result.items[0]?.name).toBe('mangga')
    expect(result.items[0]?.last_scan_error).toBeNull()
  })

  it('GET /api/series sends only the documented query params (rule 5)', async () => {
    const box = capture('get', '/api/series', () => HttpResponse.json(seriesListResponse))
    await listSeries({ root: ['mangga'], q: '군계', progress: 'reading', sort: 'name', limit: 60 })
    const url = new URL(box.current?.url ?? '')
    expect([...url.searchParams.keys()].sort()).toEqual([
      'limit',
      'progress',
      'q',
      'root',
      'sort',
    ])
    expect(url.searchParams.getAll('root')).toEqual(['mangga'])
  })

  it('GET /api/series returns total/offset/limit alongside items', async () => {
    server.use(http.get(`${ORIGIN}/api/series`, () => HttpResponse.json(seriesListResponse)))
    const result = await listSeries()
    expect(result.total).toBe(10)
    expect(result.offset).toBe(0)
    expect(result.limit).toBe(60)
    expect(result.items[0]?.progress.percent).toBe(24)
  })

  it('GET /api/series/{sid} carries books[] and encoding', async () => {
    server.use(
      http.get(`${ORIGIN}/api/series/${SERIES_ID}`, () => HttpResponse.json(seriesDetail)),
    )
    const detail = await getSeries(SERIES_ID)
    expect(detail.books).toHaveLength(2)
    expect(detail.encoding).toBe('cp949')
    expect(detail.kind).toBe('folder')
  })

  it('POST /api/series/{sid}/rescan returns a run id', async () => {
    const box = capture('post', `/api/series/${SERIES_ID}/rescan`, () =>
      HttpResponse.json({ run_id: 'run-1' }, { status: 202 }),
    )
    await expect(rescanSeries(SERIES_ID)).resolves.toEqual({ run_id: 'run-1' })
    expect(box.current?.method).toBe('POST')
  })

  it('GET /api/books/{bid} returns every page in one shot (AC-008)', async () => {
    server.use(http.get(`${ORIGIN}/api/books/${BOOK_ID}`, () => HttpResponse.json(bookDetail)))
    const book = await getBook(BOOK_ID)
    expect(book.pages).toHaveLength(3)
    expect(book.pages[0]?.n).toBe(1)
    expect(book.pages[1]?.w).toBeNull()
    expect(book.next_book_id).toBe('nextbook33333333')
    expect(book.prefs.display_mode).toBe('spread')
  })

  it('GET /api/continue', async () => {
    server.use(http.get(`${ORIGIN}/api/continue`, () => HttpResponse.json(continueResponse)))
    const result = await getContinue(20)
    expect(result.items[0]?.progress.completed).toBe(false)
  })

  it('GET /api/settings exposes the read-only server mirror', async () => {
    server.use(http.get(`${ORIGIN}/api/settings`, () => HttpResponse.json(settings)))
    const result = await getSettings()
    expect(result.server.thumbnail_widths).toEqual([120, 240, 400, 640])
    expect(result.library_scope).toBe('all')
    expect(result.fit_mode).toBe('height')
  })

  it('GET /api/cache/usage', async () => {
    server.use(http.get(`${ORIGIN}/api/cache/usage`, () => HttpResponse.json(cacheUsage)))
    const usage = await getCacheUsage()
    expect(usage.entries.map((e) => e.kind)).toEqual(['thumbs', 'pdf', 'wazero'])
  })

  it('DELETE /api/cache?kind=', async () => {
    const box = capture('delete', '/api/cache', () =>
      HttpResponse.json({ deleted_files: 4_812, freed_bytes: 226_000_000 }),
    )
    await expect(purgeCache('thumbs')).resolves.toEqual({
      deleted_files: 4_812,
      freed_bytes: 226_000_000,
    })
    expect(new URL(box.current?.url ?? '').searchParams.get('kind')).toBe('thumbs')
  })

  it('GET /api/scan/status', async () => {
    server.use(http.get(`${ORIGIN}/api/scan/status`, () => HttpResponse.json(scanStatusRunning)))
    const status = await getScanStatus()
    expect(status.state).toBe('indexing')
    expect(status.eta_ms).toBe(19_600)
  })

  it('GET /api/scan/log', async () => {
    const box = capture('get', '/api/scan/log', () => HttpResponse.json(scanLogResponse))
    const log = await getScanLog({ limit: 200, level: 'warn', since_id: 12 })
    expect(log.items[0]?.level).toBe('warn')
    const url = new URL(box.current?.url ?? '')
    expect(url.searchParams.get('since_id')).toBe('12')
  })

  it('POST /api/scan/cancel tolerates an empty body', async () => {
    server.use(
      http.post(`${ORIGIN}/api/scan/cancel`, () => new HttpResponse(null, { status: 204 })),
    )
    await expect(cancelScan()).resolves.toBeUndefined()
  })

  it('GET /api/progress/export round-trips into POST /api/progress/import', async () => {
    server.use(
      http.get(`${ORIGIN}/api/progress/export`, () => HttpResponse.json(progressExport)),
    )
    const box = capture('post', '/api/progress/import', () =>
      HttpResponse.json({ imported: 1, skipped: 0, conflicts: 0 }),
    )
    const exported = await exportProgress()
    expect(exported.format).toBe('shelf-progress/1')
    expect(exported.id_version).toBe('shelf-id/1')

    await expect(importProgress(exported, 'replace')).resolves.toEqual({
      imported: 1,
      skipped: 0,
      conflicts: 0,
    })
    expect(new URL(box.current?.url ?? '').searchParams.get('strategy')).toBe('replace')
  })

  it('GET /api/auth/status never 401s', async () => {
    server.use(http.get(`${ORIGIN}/api/auth/status`, () => HttpResponse.json(authStatus)))
    await expect(getAuthStatus()).resolves.toEqual({
      auth_required: true,
      authenticated: false,
    })
  })

  it('POST /api/auth/login and /logout answer 204', async () => {
    const box = capture(
      'post',
      '/api/auth/login',
      () => new HttpResponse(null, { status: 204, headers: { 'Set-Cookie': 'shelf=1' } }),
    )
    server.use(http.post(`${ORIGIN}/api/auth/logout`, () => new HttpResponse(null, { status: 204 })))

    await expect(login('hunter2')).resolves.toBeUndefined()
    await expect(logout()).resolves.toBeUndefined()
    expect(await box.current?.json()).toEqual({ password: 'hunter2' })
  })
})

// ---------------------------------------------------------------------------
// Request bodies — §7.1 rejects unknown fields, so we send exactly the contract
// ---------------------------------------------------------------------------

describe('request bodies', () => {
  it('PUT progress sends page alone when completed is left to the server', async () => {
    const box = capture('put', `/api/books/${BOOK_ID}/progress`, () =>
      HttpResponse.json(progress),
    )
    await putProgress(BOOK_ID, { page: 42 })
    expect(await box.current?.json()).toEqual({ page: 42 })
  })

  it('PUT progress sends completed when the UI marks it manually (FR-VWR-012)', async () => {
    const box = capture('put', `/api/books/${BOOK_ID}/progress`, () =>
      HttpResponse.json({ ...progress, completed: true }),
    )
    const result = await putProgress(BOOK_ID, { page: 187, completed: true })
    expect(await box.current?.json()).toEqual({ page: 187, completed: true })
    expect(result.completed).toBe(true)
  })

  it('DELETE progress is a 204 with no body ("안읽음")', async () => {
    server.use(
      http.delete(
        `${ORIGIN}/api/books/${BOOK_ID}/progress`,
        () => new HttpResponse(null, { status: 204 }),
      ),
    )
    await expect(deleteProgress(BOOK_ID)).resolves.toBeUndefined()
  })

  it('PUT prefs preserves an explicit null, which clears the override', async () => {
    const box = capture('put', `/api/books/${BOOK_ID}/prefs`, () => HttpResponse.json(bookPrefs))
    await putBookPrefs(BOOK_ID, { reading_direction: 'rtl', fit_mode: null })
    expect(await box.current?.json()).toEqual({ reading_direction: 'rtl', fit_mode: null })
  })

  it('GET prefs returns the effective values', async () => {
    server.use(
      http.get(`${ORIGIN}/api/books/${BOOK_ID}/prefs`, () => HttpResponse.json(bookPrefs)),
    )
    await expect(getBookPrefs(BOOK_ID)).resolves.toEqual(bookPrefs)
  })

  it('PUT settings sends only the changed user-mutable keys', async () => {
    const box = capture('put', '/api/settings', () => HttpResponse.json(settings))
    await putSettings({ theme: 'dark', prefetch: 6 })
    expect(await box.current?.json()).toEqual({ theme: 'dark', prefetch: 6 })
  })

  it('POST scan sends {roots, full}', async () => {
    const box = capture('post', '/api/scan', () =>
      HttpResponse.json({ run_id: 'run-2' }, { status: 202 }),
    )
    await startScan({ roots: ['mangga'], full: true })
    expect(await box.current?.json()).toEqual({ roots: ['mangga'], full: true })
  })
})

// ---------------------------------------------------------------------------
// Rule 4: a broken book is a 200, not an HTTP error
// ---------------------------------------------------------------------------

describe('books with status != "ok" (FR-IDX-010, impl-plan §4 rule 4)', () => {
  it('resolves with pages: [] and a populated error', async () => {
    server.use(
      http.get(`${ORIGIN}/api/books/${brokenBookDetail.id}`, () =>
        HttpResponse.json(brokenBookDetail),
      ),
    )
    const book = await getBook(brokenBookDetail.id)
    expect(book.status).toBe('error')
    expect(book.pages).toEqual([])
    expect(book.error).toBe('reading central directory: unexpected EOF')
    expect(book.page_count).toBe(0)
  })

  it('surfaces the same shape inside a series detail so the 손상 badge can render', async () => {
    server.use(
      http.get(`${ORIGIN}/api/series/${SERIES_ID}`, () => HttpResponse.json(seriesDetail)),
    )
    const detail = await getSeries(SERIES_ID)
    const broken = detail.books.find((b) => b.status !== 'ok')
    expect(broken?.error).toContain('unexpected EOF')
    expect(broken?.progress).toBeNull()
  })
})

// ---------------------------------------------------------------------------
// §7.2 error envelope
// ---------------------------------------------------------------------------

describe('error envelope (§7.2)', () => {
  it('turns 404 not_found into a typed ApiError', async () => {
    server.use(
      http.get(`${ORIGIN}/api/books/${BOOK_ID}`, () =>
        HttpResponse.json(errorEnvelope('not_found', 'book not found'), {
          status: 404,
          headers: { 'X-Request-Id': 'req-42' },
        }),
      ),
    )
    const error = await getBook(BOOK_ID).catch((e: unknown) => e)
    expect(isApiError(error)).toBe(true)
    const apiError = error as ApiError
    expect(apiError.status).toBe(404)
    expect(apiError.code).toBe('not_found')
    expect(apiError.message).toBe('book not found')
    expect(apiError.requestId).toBe('req-42')
    expect(apiError.detail).toBeNull()
  })

  it('carries detail.cv on 409 stale_version (arch §5.3)', async () => {
    server.use(
      http.get(`${ORIGIN}/api/books/${BOOK_ID}`, () =>
        HttpResponse.json(
          errorEnvelope('stale_version', 'content version changed', { cv: 'newcv0123456789' }),
          { status: 409 },
        ),
      ),
    )
    const error = (await getBook(BOOK_ID).catch((e: unknown) => e)) as ApiError
    expect(error.code).toBe('stale_version')
    expect(error.status).toBe(409)
    expect(error.staleVersion).toBe('newcv0123456789')
  })

  it('distinguishes 409 conflict (a scan is already running) from stale_version', async () => {
    server.use(
      http.post(`${ORIGIN}/api/scan`, () =>
        HttpResponse.json(errorEnvelope('conflict', 'a scan is already running'), { status: 409 }),
      ),
    )
    const error = (await startScan({}).catch((e: unknown) => e)) as ApiError
    expect(error.code).toBe('conflict')
    expect(error.staleVersion).toBeNull()
  })

  it('rejects an unknown body field with 400 bad_request (rule 5)', async () => {
    server.use(
      http.put(`${ORIGIN}/api/settings`, async ({ request }) => {
        const body = (await request.json()) as Record<string, unknown>
        const allowed = new Set(Object.keys(settings).filter((k) => k !== 'server'))
        const unknown = Object.keys(body).filter((k) => !allowed.has(k))
        return unknown.length === 0
          ? HttpResponse.json(settings)
          : HttpResponse.json(
              errorEnvelope('bad_request', `unknown field ${String(unknown[0])}`, {
                field: unknown[0],
              }),
              { status: 400 },
            )
      }),
    )

    await expect(putSettings({ theme: 'dark' })).resolves.toEqual(settings)

    // A typo the compiler would also catch — proving the server's strict decoding surfaces.
    const error = (await putSettings({ them: 'dark' } as never).catch(
      (e: unknown) => e,
    )) as ApiError
    expect(error.status).toBe(400)
    expect(error.code).toBe('bad_request')
    expect(error.detail).toEqual({ field: 'them' })
  })

  it('surfaces 422 thumb_unavailable with its reason (arch §5.5)', async () => {
    const url = pageThumbUrl(BOOK_ID, 3, { w: 120, v: BOOK_CV })
    server.use(
      http.get(`${ORIGIN}/api/books/${BOOK_ID}/thumbs/3`, () =>
        HttpResponse.json(
          errorEnvelope('thumb_unavailable', 'cannot decode source', { reason: 'animated_webp' }),
          { status: 422 },
        ),
      ),
    )
    const error = (await fetchImage(url).catch((e: unknown) => e)) as ApiError
    expect(error.status).toBe(422)
    expect(error.code).toBe('thumb_unavailable')
    expect(error.thumbReason).toBe('animated_webp')
  })

  it('surfaces 501 unsupported for a PDF page in a nopdf build', async () => {
    server.use(
      http.get(`${ORIGIN}/api/books/${BOOK_ID}/pages/1`, () =>
        HttpResponse.json(errorEnvelope('unsupported', 'pdf support is not built in'), {
          status: 501,
        }),
      ),
    )
    const error = (await fetchImage(`/api/books/${BOOK_ID}/pages/1`).catch(
      (e: unknown) => e,
    )) as ApiError
    expect(error.status).toBe(501)
    expect(error.code).toBe('unsupported')
  })

  it('surfaces 503 unavailable when a root is unreachable', async () => {
    server.use(
      http.get(`${ORIGIN}/api/series`, () =>
        HttpResponse.json(errorEnvelope('unavailable', 'media volume unreachable'), {
          status: 503,
        }),
      ),
    )
    const error = (await listSeries().catch((e: unknown) => e)) as ApiError
    expect(error.code).toBe('unavailable')
  })

  it('falls back to a status-derived code when the body is not an envelope', async () => {
    server.use(
      http.get(`${ORIGIN}/api/roots`, () => new HttpResponse('<html>502</html>', { status: 500 })),
    )
    const error = (await getRoots().catch((e: unknown) => e)) as ApiError
    expect(error.code).toBe('internal')
    expect(error.message).toBe('HTTP 500')
  })

  /**
   * The same fallback, on the row where it decides a *screen* (amendment A-11,
   * ruling E-17). `queries.test.tsx` already pins a 403 that arrives as a proper
   * §7.2 envelope — there `error.code` comes off the body and `codeForStatus` is
   * never consulted, which is why `[403, 'forbidden']` could be repointed at
   * `unauthorized` with the whole suite still green.
   *
   * A reverse proxy in front of SHELF (NFR-SEC-003) does not speak §7.2: it
   * answers its own HTML, and then the status is the only thing the client has.
   * Fold it into `unauthorized` and `isAuthError` sends an already-authenticated
   * user — or a user of the default password-less server (ruling E-8) — to a
   * login screen that no password dismisses.
   */
  it('keeps a proxy 403 with no envelope out of the re-auth path (A-11, E-17)', async () => {
    server.use(
      http.get(`${ORIGIN}/api/roots`, () =>
        HttpResponse.text('<html><body>403 Forbidden</body></html>', { status: 403 }),
      ),
    )
    const listener = vi.fn()
    const unsubscribe = subscribeUnauthorized(listener)

    const error = (await getRoots().catch((e: unknown) => e)) as ApiError
    expect(isApiError(error)).toBe(true)
    expect(error.status).toBe(403)
    expect(error.code).toBe('forbidden')
    expect(error.code).not.toBe('unauthorized')
    // The two consequences of getting that wrong, asserted rather than implied.
    expect(isAuthError(error)).toBe(false)
    expect(listener).not.toHaveBeenCalled()

    unsubscribe()
  })

  // Amendment A-9 (ruling E-13): the login limiter's 429 is a named code now,
  // not a status the client had to interpret on its own.
  it('surfaces the 429 of the login limiter as rate_limited, with its Retry-After', async () => {
    server.use(
      http.post(`${ORIGIN}/api/auth/login`, () =>
        HttpResponse.json(
          { error: { code: 'rate_limited', message: 'too many attempts' } },
          { status: 429, headers: { 'Retry-After': '30' } },
        ),
      ),
    )
    const error = (await login('nope').catch((e: unknown) => e)) as ApiError
    expect(error.status).toBe(429)
    expect(error.code).toBe('rate_limited')
    expect(error.rawCode).toBe('rate_limited')
    expect(error.retryAfterMs).toBe(30_000)
  })

  it('keeps a genuinely unrecognised server code as rawCode and falls back on the status', async () => {
    server.use(
      http.get(`${ORIGIN}/api/roots`, () =>
        HttpResponse.json({ error: { code: 'teapot', message: 'nope' } }, { status: 418 }),
      ),
    )
    const error = (await getRoots().catch((e: unknown) => e)) as ApiError
    expect(error.status).toBe(418)
    expect(error.rawCode).toBe('teapot')
    expect(error.code).toBe('internal')
  })

  it('notifies the unauthorized listeners on 401 so the shell can show the login screen', async () => {
    server.use(
      http.get(`${ORIGIN}/api/series`, () =>
        HttpResponse.json(errorEnvelope('unauthorized', 'session missing'), { status: 401 }),
      ),
    )
    const listener = vi.fn()
    const unsubscribe = subscribeUnauthorized(listener)

    const error = (await listSeries().catch((e: unknown) => e)) as ApiError
    expect(error.code).toBe('unauthorized')
    expect(listener).toHaveBeenCalledTimes(1)

    unsubscribe()
    await listSeries().catch(() => undefined)
    expect(listener).toHaveBeenCalledTimes(1)
  })
})

// ---------------------------------------------------------------------------
// Images: 202 is a normal answer (impl-plan §4 rule 3)
// ---------------------------------------------------------------------------

describe('Retry-After parsing', () => {
  it.each([
    ['1', 1_000],
    ['0', 0],
    ['30', 30_000],
    ['9999', 60_000], // clamped
  ])('reads delta-seconds %s as %ims', (header, want) => {
    expect(parseRetryAfter(header)).toBe(want)
  })

  it('returns null when the header is absent or unusable', () => {
    expect(parseRetryAfter(null)).toBeNull()
    expect(parseRetryAfter('   ')).toBeNull()
    expect(parseRetryAfter('soon')).toBeNull()
  })

  it('accepts an HTTP-date', () => {
    const at = new Date(Date.now() + 2_000).toUTCString()
    const ms = parseRetryAfter(at)
    expect(ms).not.toBeNull()
    expect(ms ?? 0).toBeGreaterThan(500)
    expect(ms ?? 0).toBeLessThanOrEqual(2_000)
  })
})

describe('fetchImage', () => {
  const coverUrl = seriesCoverUrl(SERIES_ID, { w: 400, v: COVER_CV })

  it('reports ready for 200 and hands back the versioned url for <img>', async () => {
    const box = capture(
      'get',
      `/api/series/${SERIES_ID}/cover`,
      () => new HttpResponse('jpegbytes', { headers: { 'Content-Type': 'image/jpeg' } }),
    )
    await expect(fetchImage(coverUrl)).resolves.toEqual({ state: 'ready', url: coverUrl })

    const url = new URL(box.current?.url ?? '')
    expect(url.searchParams.get('v')).toBe(COVER_CV)
    expect(url.searchParams.get('w')).toBe('400')
    expect(box.current?.headers.get('Accept')).toBe('image/*')
  })

  it('reports queued for 202 and honours Retry-After (not an error)', async () => {
    server.use(
      http.get(
        `${ORIGIN}/api/series/${SERIES_ID}/cover`,
        () => new HttpResponse(null, { status: 202, headers: { 'Retry-After': '1' } }),
      ),
    )
    await expect(fetchImage(coverUrl)).resolves.toEqual({
      state: 'queued',
      url: coverUrl,
      retryAfterMs: 1_000,
    })
  })

  it('defaults to 1 s when a 202 omits Retry-After', async () => {
    server.use(
      http.get(
        `${ORIGIN}/api/series/${SERIES_ID}/cover`,
        () => new HttpResponse(null, { status: 202 }),
      ),
    )
    const result = await fetchImage(coverUrl)
    expect(result).toEqual({ state: 'queued', url: coverUrl, retryAfterMs: 1_000 })
  })

  it('throws on 404 — a series with no cover at all (FR-LIB-008 placeholder)', async () => {
    server.use(
      http.get(`${ORIGIN}/api/series/${SERIES_ID}/cover`, () =>
        HttpResponse.json(errorEnvelope('not_found', 'no cover'), { status: 404 }),
      ),
    )
    const error = (await fetchImage(coverUrl).catch((e: unknown) => e)) as ApiError
    expect(error.status).toBe(404)
    expect(error.code).toBe('not_found')
  })
})
