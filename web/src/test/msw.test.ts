/* eslint-disable no-restricted-globals -- this file tests how MSW intercepts fetch itself; it is not a module that talks to the API */
import { http, HttpResponse } from 'msw'
import { setupServer } from 'msw/node'
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from 'vitest'

import { deleteBookCover, fetchImage, putBookCover, putSeriesPrefs } from '../api/client'
import { isApiError } from '../api/errors'
import {
  BOOK_ID,
  ORIGIN as FIXTURE_ORIGIN,
  SERIES_ID,
  bookSummary,
  coveredBookSummary,
} from '../api/fixtures'
import { bookCoverUrl } from '../api/urls'
import {
  assertNoUnhandled,
  bookCoverViolation,
  createCoverMock,
  drainUnhandled,
  failUnhandled,
  installStrictMsw,
  strictSetupInstalled,
} from './msw'

const installedBySetup = strictSetupInstalled()
const ORIGIN = 'http://localhost'
const server = setupServer(http.get(`${ORIGIN}/api/known`, () => HttpResponse.json({ ok: true })))

beforeAll(() => {
  server.listen({ onUnhandledRequest: failUnhandled })
})
afterEach(() => {
  server.resetHandlers()
  // Each test below provokes an unhandled request on purpose and inspects it;
  // whatever is left must not reach setup.ts's afterEach and fail them.
  drainUnhandled()
})
afterAll(() => {
  server.close()
})

describe('failUnhandled', () => {
  it('records an unhandled request by method, path and query', async () => {
    await fetch(`${ORIGIN}/api/unknown?x=1`).catch(() => undefined)
    expect(drainUnhandled()).toEqual(['GET /api/unknown?x=1'])
  })

  it('still fails the request itself, as onUnhandledRequest: "error" did', async () => {
    // MSW's own refusal, not a connection error from a request let through.
    await expect(fetch(`${ORIGIN}/api/unknown`)).rejects.toThrow(/onUnhandledRequest/)
  })

  it('leaves handled requests alone', async () => {
    const res = await fetch(`${ORIGIN}/api/known`)
    expect(await res.json()).toEqual({ ok: true })
    expect(drainUnhandled()).toEqual([])
  })
})

describe('assertNoUnhandled', () => {
  it('throws naming every unhandled request, and clears them', async () => {
    await fetch(`${ORIGIN}/api/a`).catch(() => undefined)
    await fetch(`${ORIGIN}/api/b`, { method: 'PUT', body: '{}' }).catch(() => undefined)
    expect(assertNoUnhandled).toThrow(/GET \/api\/a.*PUT \/api\/b/)
    expect(assertNoUnhandled).not.toThrow()
  })

  it('is silent when nothing went unhandled', () => {
    expect(assertNoUnhandled).not.toThrow()
  })
})

describe('installStrictMsw', () => {
  it('runs the check after every test and after every file', () => {
    const afterEachHook = vi.fn()
    const afterAllHook = vi.fn()
    installStrictMsw({ afterEach: afterEachHook, afterAll: afterAllHook })
    expect(afterEachHook).toHaveBeenCalledWith(assertNoUnhandled)
    expect(afterAllHook).toHaveBeenCalledWith(assertNoUnhandled)
  })

  // The check only works because src/test/setup.ts installs it, and this is
  // what notices if it stops running: vitest.config.ts fails a run whose setup
  // file is missing, but not one whose setup file no longer calls
  // installStrictMsw. Sampled before the test above runs its own install.
  it('is installed by the shared setup file', () => {
    expect(installedBySetup).toBe(true)
  })
})

// ---------------------------------------------------------------------------
// Ruling E-69 — the cover and series-prefs mock models the contract
// ---------------------------------------------------------------------------

describe('bookCoverViolation (E-69 §2.4, §2.6)', () => {
  const ok = { page: 3, x: 0.5, y: 0.125, w: 0.375, h: 0.75 }
  const book = { page_count: 187, status: 'ok' } as const
  // 2^-20: exact in binary, so "just past the edge" is past it and not a rounding.
  const eps = 2 ** -20

  it('accepts a region inside the page', () => {
    expect(bookCoverViolation(ok, book)).toBeNull()
  })

  it('accepts the edges themselves: x + w = 1, y + h = 1, w = h = 0.05, page 1 and page_count', () => {
    expect(bookCoverViolation({ ...ok, x: 0.75, w: 0.25 }, book)).toBeNull()
    expect(bookCoverViolation({ ...ok, y: 0.25, h: 0.75 }, book)).toBeNull()
    expect(bookCoverViolation({ ...ok, w: 0.05, h: 0.05 }, book)).toBeNull()
    expect(bookCoverViolation({ ...ok, x: 0, y: 0 }, book)).toBeNull()
    expect(bookCoverViolation({ ...ok, page: 1 }, book)).toBeNull()
    expect(bookCoverViolation({ ...ok, page: 187 }, book)).toBeNull()
  })

  it.each([
    ['x + w > 1', { x: 0.75, w: 0.25 + eps }],
    ['y + h > 1', { y: 0.25, h: 0.75 + eps }],
    ['x < 0', { x: -eps }],
    ['y < 0', { y: -eps }],
    ['w < 0.05', { w: 0.0499 }],
    ['h < 0.05', { h: 0.0499 }],
    ['page 0', { page: 0 }],
    ['page past the end', { page: 188 }],
    ['a fractional page', { page: 1.5 }],
    ['a string', { x: '0.5' }],
    ['NaN', { w: Number.NaN }],
  ] as const)('refuses %s', (_label, patch) => {
    expect(bookCoverViolation({ ...ok, ...patch }, book)).not.toBeNull()
  })

  it('refuses a missing field — a missing x must not pass as 0', () => {
    const { x: _x, ...withoutX } = ok
    expect(bookCoverViolation(withoutX, book)).not.toBeNull()
  })

  it('refuses an unknown field (§7.1 strict decoding)', () => {
    expect(bookCoverViolation({ ...ok, aspect: 0.66 }, book)).not.toBeNull()
  })

  it('refuses a book that is not ok', () => {
    expect(bookCoverViolation(ok, { page_count: 187, status: 'error' })).not.toBeNull()
  })
})

describe('createCoverMock (E-69 §2.6)', () => {
  const books = [
    bookSummary,
    { ...bookSummary, id: 'nopagesbook22222', page_count: 0, status: 'error' as const },
  ]
  const cover = { page: 3, x: 0.5, y: 0.125, w: 0.375, h: 0.75 }

  function mount() {
    const mock = createCoverMock(FIXTURE_ORIGIN, { books, series: [SERIES_ID] })
    server.use(...mock.handlers)
    return mock
  }

  async function raw(method: string, path: string, body: unknown): Promise<Response> {
    return fetch(`${FIXTURE_ORIGIN}${path}`, {
      method,
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    })
  }

  it('answers a PUT with a new cover_cv, and the old one goes stale on GET', async () => {
    const mock = mount()
    const result = await putBookCover(BOOK_ID, cover)
    expect(result.cover).toEqual(cover)
    expect(result.cover_cv).not.toBe(bookSummary.cover_cv)
    expect(mock.covers.get(BOOK_ID)).toEqual(cover)

    const stale = await fetchImage(bookCoverUrl(BOOK_ID, { w: 400, v: bookSummary.cover_cv })).catch(
      (e: unknown) => e,
    )
    expect(isApiError(stale) && stale.code).toBe('stale_version')
    await expect(fetchImage(bookCoverUrl(BOOK_ID, { w: 400, v: result.cover_cv }))).resolves.toEqual({
      state: 'ready',
      url: bookCoverUrl(BOOK_ID, { w: 400, v: result.cover_cv }),
    })
    expect(mock.gets.map((g) => g.v)).toEqual([bookSummary.cover_cv, result.cover_cv])
  })

  it('refuses an unknown field, a missing field and a region past the edge with 400 bad_request', async () => {
    const mock = mount()
    for (const body of [
      { ...cover, aspect: 2 / 3 },
      { page: 3, y: 0.125, w: 0.375, h: 0.75 },
      { ...cover, x: 0.75, w: 0.25 + 2 ** -20 },
    ]) {
      const res = await raw('PUT', `/api/books/${BOOK_ID}/cover`, body)
      expect(res.status).toBe(400)
      expect(((await res.json()) as { error: { code: string } }).error.code).toBe('bad_request')
    }
    expect(mock.covers.get(BOOK_ID)).toBeNull()
  })

  it('refuses a book that is not ok with 400, and an unknown book with 404', async () => {
    mount()
    expect((await raw('PUT', '/api/books/nopagesbook22222/cover', { ...cover, page: 1 })).status).toBe(400)
    expect((await raw('PUT', '/api/books/unknownbook33333/cover', cover)).status).toBe(404)
  })

  it('DELETE is 204 whether or not a cover was chosen, and restores the default cover_cv', async () => {
    const mock = mount()
    await putBookCover(BOOK_ID, cover)
    await expect(deleteBookCover(BOOK_ID)).resolves.toBeUndefined()
    await expect(deleteBookCover(BOOK_ID)).resolves.toBeUndefined()
    expect(mock.covers.get(BOOK_ID)).toBeNull()
    expect(mock.coverCvOf(BOOK_ID)).toBe(bookSummary.cover_cv)
    expect(mock.deletes).toEqual([BOOK_ID, BOOK_ID])
  })

  it('GET of a book with no pages is 404', async () => {
    mount()
    const error = await fetchImage(bookCoverUrl('nopagesbook22222', { w: 120, v: null })).catch(
      (e: unknown) => e,
    )
    expect(isApiError(error) && error.status).toBe(404)
  })

  it('series prefs: stores ltr/rtl/null, and 400s a missing, unknown or bad field', async () => {
    const mock = mount()
    await expect(putSeriesPrefs(SERIES_ID, { reading_direction: 'rtl' })).resolves.toEqual({
      reading_direction: 'rtl',
    })
    expect(mock.seriesDirs.get(SERIES_ID)).toBe('rtl')
    await expect(putSeriesPrefs(SERIES_ID, { reading_direction: null })).resolves.toEqual({
      reading_direction: null,
    })
    expect(mock.seriesDirs.get(SERIES_ID)).toBeNull()
    for (const body of [{}, { reading_direction: 'up' }, { reading_direction: 'rtl', extra: 1 }]) {
      expect((await raw('PUT', `/api/series/${SERIES_ID}/prefs`, body)).status).toBe(400)
    }
    expect((await raw('PUT', '/api/series/unknownseries333/prefs', { reading_direction: 'rtl' })).status).toBe(
      404,
    )
    expect(mock.seriesPrefsPuts.map((p) => p.body)).toEqual([
      { reading_direction: 'rtl' },
      { reading_direction: null },
      {},
      { reading_direction: 'up' },
      { reading_direction: 'rtl', extra: 1 },
      { reading_direction: 'rtl' },
    ])
  })
})

/**
 * E-69 §2.7: a default cover's `cover_cv` is the hash of its inputs, and the
 * effective direction (§2.3: book → series → global) is one of them — "지정이
 * 없으면 1쪽이 가로인지 몰라도 방향을 늘 넣는다". So a direction change that moves a
 * book's effective direction turns its `cover_cv` over and the old `?v=` is a
 * `409 stale_version` whose `detail.cv` is the new one; a chosen cover's inputs
 * have no direction in them, so its `cover_cv` stays. A mock that kept every
 * `cover_cv` still would let a card that never re-reads its series pass.
 */
describe('createCoverMock — the default cover follows the effective direction (E-69 §2.3, §2.7)', () => {
  const SIBLING = 'sibl1ngbook22222'
  const CHOSEN = 'ch0senbook333333'
  const OTHER_SERIES = 'otherseries44444'
  const PINNED = 'p1nnedbook555555'
  const books = [
    bookSummary,
    { ...bookSummary, id: SIBLING, cover_cv: 'b0000000000000a1' },
    { ...coveredBookSummary, id: CHOSEN },
    { ...bookSummary, id: PINNED, series_id: OTHER_SERIES, cover_cv: 'b0000000000000a2' },
  ]

  function mount() {
    // The fixture's global default is `ltr`; the other series pinned itself to `rtl`.
    const mock = createCoverMock(FIXTURE_ORIGIN, {
      books,
      series: [SERIES_ID, OTHER_SERIES],
      seriesDirs: { [OTHER_SERIES]: 'rtl' },
    })
    server.use(...mock.handlers)
    return mock
  }

  async function staleCvOf(bid: string, v: string): Promise<string | null> {
    const error = await fetchImage(bookCoverUrl(bid, { w: 400, v })).catch((e: unknown) => e)
    if (!isApiError(error) || error.code !== 'stale_version' || error.status !== 409) {
      throw new Error(`expected 409 stale_version for v=${v}`)
    }
    return error.detailString('cv')
  }

  it('starts on the cover_cv each book was given', () => {
    const mock = mount()
    for (const book of books) expect(mock.coverCvOf(book.id)).toBe(book.cover_cv)
  })

  it('a series direction that moves the effective one turns the default covers over; the old v is 409 with the new cv', async () => {
    const mock = mount()
    const before = mock.coverCvOf(BOOK_ID) ?? ''
    await putSeriesPrefs(SERIES_ID, { reading_direction: 'rtl' })
    const after = mock.coverCvOf(BOOK_ID) ?? ''

    expect(after).not.toBe(before)
    expect(mock.coverCvOf(SIBLING)).not.toBe('b0000000000000a1')
    expect(await staleCvOf(BOOK_ID, before)).toBe(after)
    await expect(fetchImage(bookCoverUrl(BOOK_ID, { w: 400, v: after }))).resolves.toEqual({
      state: 'ready',
      url: bookCoverUrl(BOOK_ID, { w: 400, v: after }),
    })
    // A chosen region has no direction in its inputs; another series is not this one.
    expect(mock.coverCvOf(CHOSEN)).toBe(coveredBookSummary.cover_cv)
    expect(mock.coverCvOf(PINNED)).toBe('b0000000000000a2')
    // What the series GET would list is the same value.
    expect(mock.summaryOf(BOOK_ID)).toMatchObject({ cover: null, cover_cv: after })
  })

  it('a series direction equal to the effective one changes no input, so no cover_cv; going back restores the first', async () => {
    const mock = mount()
    const first = mock.coverCvOf(BOOK_ID)
    await putSeriesPrefs(SERIES_ID, { reading_direction: 'ltr' })
    expect(mock.coverCvOf(BOOK_ID)).toBe(first)
    await putSeriesPrefs(SERIES_ID, { reading_direction: 'rtl' })
    expect(mock.coverCvOf(BOOK_ID)).not.toBe(first)
    await putSeriesPrefs(SERIES_ID, { reading_direction: null })
    expect(mock.coverCvOf(BOOK_ID)).toBe(first)
  })

  it('a book’s own direction moves that book only, and beats its series’', async () => {
    const mock = mount()
    const sibling = mock.coverCvOf(SIBLING)
    mock.setBookDirection(BOOK_ID, 'rtl')
    const own = mock.coverCvOf(BOOK_ID) ?? ''
    expect(own).not.toBe(bookSummary.cover_cv)
    expect(await staleCvOf(BOOK_ID, bookSummary.cover_cv)).toBe(own)
    expect(mock.coverCvOf(SIBLING)).toBe(sibling)

    // The series going R→L too changes nothing for the book that already is.
    await putSeriesPrefs(SERIES_ID, { reading_direction: 'rtl' })
    expect(mock.coverCvOf(BOOK_ID)).toBe(own)
    // Its own L→R now differs from the series' R→L: over again.
    mock.setBookDirection(BOOK_ID, 'ltr')
    expect(mock.coverCvOf(BOOK_ID)).not.toBe(own)
  })

  it('the global direction moves every default cover whose book and series have none — and no other', async () => {
    const mock = mount()
    mock.setGlobalDirection('rtl')
    expect(mock.coverCvOf(BOOK_ID)).not.toBe(bookSummary.cover_cv)
    expect(mock.coverCvOf(SIBLING)).not.toBe('b0000000000000a1')
    expect(await staleCvOf(SIBLING, 'b0000000000000a1')).toBe(mock.coverCvOf(SIBLING))
    // E-69 §2.1: a series with its own direction stays where it is.
    expect(mock.coverCvOf(PINNED)).toBe('b0000000000000a2')
    expect(mock.coverCvOf(CHOSEN)).toBe(coveredBookSummary.cover_cv)
  })

  it('a chosen cover keeps its cover_cv through all three', async () => {
    const mock = mount()
    mock.setGlobalDirection('rtl')
    await putSeriesPrefs(SERIES_ID, { reading_direction: 'ltr' })
    mock.setBookDirection(CHOSEN, 'rtl')
    expect(mock.coverCvOf(CHOSEN)).toBe(coveredBookSummary.cover_cv)
    await expect(
      fetchImage(bookCoverUrl(CHOSEN, { w: 400, v: coveredBookSummary.cover_cv })),
    ).resolves.toMatchObject({ state: 'ready' })
  })

  it('a stored choice that cannot apply (§2.5: the book is not ok) is the default cover, and follows the direction', async () => {
    const broken = { ...coveredBookSummary, id: 'br0kenchosen6666', status: 'error' as const, cover_cv: 'b0000000000000a3' }
    const mock = createCoverMock(FIXTURE_ORIGIN, { books: [broken], series: [SERIES_ID] })
    server.use(...mock.handlers)
    await putSeriesPrefs(SERIES_ID, { reading_direction: 'rtl' })
    expect(mock.coverCvOf(broken.id)).not.toBe('b0000000000000a3')
    // §2.6: the row is still shown — it is the user's setting.
    expect(mock.summaryOf(broken.id).cover).toEqual(coveredBookSummary.cover)
  })

  /**
   * E-69 §2.7: a chosen cover's `cover_cv` is the hash of book_id ‖ page ‖ region ‖
   * content cv — the same inputs, the same version, however many writes came between.
   * A mock that numbered its writes gave the same region a new `cover_cv` each time,
   * so a card that re-reads after every write could not be told from one that keeps
   * the url it had, and a return to the first region looked like a third cover.
   */
  it('a chosen cover’s cover_cv is a function of its inputs: the same region twice is the same version', async () => {
    const mock = mount()
    const a = { page: 3, x: 0.5, y: 0.125, w: 0.375, h: 0.75 }
    const first = (await putBookCover(BOOK_ID, a)).cover_cv
    const again = (await putBookCover(BOOK_ID, a)).cover_cv
    expect(again).toBe(first)
    expect(mock.coverCvOf(BOOK_ID)).toBe(first)
    // …and a GET at that version is not stale.
    await expect(fetchImage(bookCoverUrl(BOOK_ID, { w: 400, v: first }))).resolves.toMatchObject({
      state: 'ready',
    })
  })

  it('A → B → A comes back to A’s cover_cv, and B’s is another', async () => {
    mount()
    const a = { page: 3, x: 0.5, y: 0.125, w: 0.375, h: 0.75 }
    const b = { page: 3, x: 0.25, y: 0.125, w: 0.375, h: 0.75 }
    const cvA = (await putBookCover(BOOK_ID, a)).cover_cv
    const cvB = (await putBookCover(BOOK_ID, b)).cover_cv
    const back = (await putBookCover(BOOK_ID, a)).cover_cv
    expect(cvB).not.toBe(cvA)
    expect(back).toBe(cvA)
  })

  it('every input moves it: the page, each of x, y, w, h, and the book', async () => {
    mount()
    const base = { page: 3, x: 0.5, y: 0.125, w: 0.375, h: 0.75 }
    const variants = [
      base,
      { ...base, page: 4 },
      { ...base, x: 0.25 },
      { ...base, y: 0.25 },
      { ...base, w: 0.25 },
      { ...base, h: 0.5 },
    ]
    const cvs: string[] = []
    for (const region of variants) cvs.push((await putBookCover(BOOK_ID, region)).cover_cv)
    cvs.push((await putBookCover(SIBLING, base)).cover_cv)
    expect(new Set(cvs).size).toBe(cvs.length)
  })

  it('the region a book started with, chosen again, is the cover_cv it was given', async () => {
    const mock = mount()
    await putBookCover(CHOSEN, { page: 1, x: 0, y: 0, w: 0.5, h: 0.5 })
    expect(mock.coverCvOf(CHOSEN)).not.toBe(coveredBookSummary.cover_cv)
    const started = coveredBookSummary.cover
    if (started === null) throw new Error('the fixture chose no cover')
    const back = await putBookCover(CHOSEN, { ...started })
    expect(back.cover_cv).toBe(coveredBookSummary.cover_cv)
  })

  it('a chosen cover_cv is never a default cover’s, in either direction', async () => {
    const mock = mount()
    const autoLtr = mock.coverCvOf(BOOK_ID)
    await putSeriesPrefs(SERIES_ID, { reading_direction: 'rtl' })
    const autoRtl = mock.coverCvOf(BOOK_ID)
    const chosen = (await putBookCover(BOOK_ID, { page: 1, x: 0, y: 0, w: 1, h: 1 })).cover_cv
    expect(chosen).not.toBe(autoLtr)
    expect(chosen).not.toBe(autoRtl)
    // And a direction change leaves it where it is (no direction among its inputs).
    await putSeriesPrefs(SERIES_ID, { reading_direction: 'ltr' })
    expect(mock.coverCvOf(BOOK_ID)).toBe(chosen)
  })

  it('a reset lands on the default cover of the direction now in force', async () => {
    const mock = mount()
    await putSeriesPrefs(SERIES_ID, { reading_direction: 'rtl' })
    const autoRtl = mock.coverCvOf(BOOK_ID)
    const chosen = await putBookCover(BOOK_ID, { page: 3, x: 0.5, y: 0.125, w: 0.375, h: 0.75 })
    expect(chosen.cover_cv).not.toBe(autoRtl)
    // The chosen cover does not follow a direction change…
    await putSeriesPrefs(SERIES_ID, { reading_direction: 'ltr' })
    expect(mock.coverCvOf(BOOK_ID)).toBe(chosen.cover_cv)
    await putSeriesPrefs(SERIES_ID, { reading_direction: 'rtl' })
    // …and the reset is R→L's default again.
    await deleteBookCover(BOOK_ID)
    expect(mock.coverCvOf(BOOK_ID)).toBe(autoRtl)
  })
})
