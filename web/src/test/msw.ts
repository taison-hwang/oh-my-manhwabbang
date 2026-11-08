/**
 * MSW's unhandled-request check, made to fail the test.
 *
 * `onUnhandledRequest: 'error'` fails the **request**, not the test: `msw/node`
 * rejects the fetch, React Query turns that into an error state nothing reads,
 * and the run stays green while stderr says `intercepted a request without a
 * matching request handler`. Several files used to lean on it as though it
 * were an assertion.
 *
 * `failUnhandled` keeps an HTTP(S) request failing as `'error'` did, and also
 * records it (unlike the string strategy it does not exempt `file:` URLs, which
 * nothing here fetches); `src/test/setup.ts` runs `assertNoUnhandled` after every test and
 * after every file, so a request no handler answers is a red test that names it.
 */

import { http, HttpResponse, type HttpHandler } from 'msw'

import { settings as fixtureSettings } from '../api/fixtures'
import {
  BOOK_COVER_MIN_EXTENT,
  READING_DIRS,
  type BookCover,
  type BookSummary,
  type ID,
  type ItemStatus,
  type ReadingDir,
} from '../api/types'

interface Print {
  error(): void
}

let unhandled: string[] = []
let installed = false

export function failUnhandled(request: Request, print: Print): void {
  const url = new URL(request.url)
  unhandled.push(`${request.method} ${url.pathname}${url.search}`)
  print.error()
}

/** Hands back the requests recorded so far and forgets them. */
export function drainUnhandled(): string[] {
  const out = unhandled
  unhandled = []
  return out
}

export function assertNoUnhandled(): void {
  const got = drainUnhandled()
  if (got.length > 0) {
    throw new Error(`MSW: no handler answered ${got.join(', ')}`)
  }
}

type Hook = (fn: () => void) => void

/**
 * Registers the check after every test and after every file. setup.ts calls
 * this with vitest's own hooks. afterAll as well, so a request recorded after a
 * file's last test (an unmount flush) is not dropped with the module; it does
 * not wait for work still in flight, and a request that lands during the next
 * test fails that test instead.
 */
export function installStrictMsw(hooks: { afterEach: Hook; afterAll: Hook }): void {
  hooks.afterEach(assertNoUnhandled)
  hooks.afterAll(assertNoUnhandled)
  installed = true
}

export function strictSetupInstalled(): boolean {
  return installed
}

// ---------------------------------------------------------------------------
// Ruling E-69 — a mock of the cover and series-prefs endpoints that keeps the contract
// ---------------------------------------------------------------------------

/**
 * A handler that answers every write with 200 tests nothing about the rules the server
 * keeps, so this one keeps them: strict decoding (§7.1, every field required, unknown
 * fields refused), §2.4's region rules, `409 stale_version` for a `v` that is not the
 * current `cover_cv`, `404` for a book with no pages (§2.6).
 *
 * And `cover_cv` as a function of the cover's inputs (§2.7): a default cover — no
 * choice, or one that cannot apply (§2.5) — has the **effective direction** among
 * them (§2.3: the book's own, else its series', else the global default), so moving
 * that direction turns the `cover_cv` over and moving it back restores it. A chosen
 * cover has its page and region instead (and the content cv), keeps its `cover_cv`
 * through any direction change, and the same region chosen again — twice in a row, or
 * A → B → A — is the same `cover_cv`.
 */

const COVER_FIELDS = ['page', 'x', 'y', 'w', 'h'] as const

interface CoverMockBookFacts {
  page_count: number
  status: ItemStatus
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

/**
 * Why a `PUT /api/books/{bid}/cover` body would be `400`, or `null` when the server would
 * take it. The checks are E-69 §2.4's, written as the ruling writes them.
 */
export function bookCoverViolation(body: unknown, book: CoverMockBookFacts): string | null {
  if (!isPlainObject(body)) return 'body is not an object'
  for (const key of Object.keys(body)) {
    if (!(COVER_FIELDS as readonly string[]).includes(key)) return `unknown field "${key}"`
  }
  for (const key of COVER_FIELDS) {
    if (!(key in body)) return `missing field "${key}"`
    const value = body[key]
    if (typeof value !== 'number' || !Number.isFinite(value)) return `"${key}" is not a number`
  }
  const { page, x, y, w, h } = body as Record<(typeof COVER_FIELDS)[number], number>
  if (!Number.isInteger(page) || page < 1 || page > book.page_count) return 'page out of range'
  if (!(x >= 0)) return 'x < 0'
  if (!(y >= 0)) return 'y < 0'
  if (!(w >= BOOK_COVER_MIN_EXTENT)) return 'w < 0.05'
  if (!(h >= BOOK_COVER_MIN_EXTENT)) return 'h < 0.05'
  if (!(x + w <= 1)) return 'x + w > 1'
  if (!(y + h <= 1)) return 'y + h > 1'
  if (book.status !== 'ok') return 'book is not ok'
  return null
}

/** Why a `PUT /api/series/{sid}/prefs` body would be `400` — `null` is the one "unset". */
export function seriesPrefsViolation(body: unknown): string | null {
  if (!isPlainObject(body)) return 'body is not an object'
  for (const key of Object.keys(body)) {
    if (key !== 'reading_direction') return `unknown field "${key}"`
  }
  if (!('reading_direction' in body)) return 'missing field "reading_direction"'
  const value = body.reading_direction
  if (value !== null && !(READING_DIRS as readonly unknown[]).includes(value)) {
    return 'reading_direction is not ltr, rtl or null'
  }
  return null
}

function errorJson(status: number, code: string, message: string, detail?: Record<string, unknown>) {
  return HttpResponse.json(
    { error: detail === undefined ? { code, message } : { code, message, detail } },
    { status },
  )
}

export interface CoverMockOptions {
  /**
   * The books the server knows; `cover` and `cover_cv` are their starting state — a
   * default cover's `cover_cv` is taken as the one for its starting effective direction.
   */
  books: readonly BookSummary[]
  /** The series ids `PUT /api/series/{sid}/prefs` accepts. */
  series?: readonly ID[]
  /** A series' stored direction at the start; absent means `null`. */
  seriesDirs?: Readonly<Record<ID, ReadingDir | null>>
  /** A book's own stored direction at the start (`book_prefs.reading_dir`); absent means `null`. */
  bookDirs?: Readonly<Record<ID, ReadingDir | null>>
  /** The global default (`/api/settings`); the fixtures' `settings.reading_direction` if absent. */
  globalDir?: ReadingDir
}

export interface CoverMock {
  handlers: HttpHandler[]
  /** The chosen cover per book, `null` for the default. */
  covers: Map<ID, BookCover | null>
  /** The stored series direction per series. Writing it moves `cover_cv`s as the PUT does. */
  seriesDirs: Map<ID, ReadingDir | null>
  /** The current `cover_cv` — what the series GET would list and the cover GET checks `v` against. */
  coverCvOf: (bid: ID) => string | undefined
  /** The book as the series GET would list it now: its current `cover` and `cover_cv`. */
  summaryOf: (bid: ID) => BookSummary
  /** E-69 §2.3, field one of the ladder: a `PUT /api/books/{bid}/prefs` of `reading_direction`. */
  setBookDirection: (bid: ID, dir: ReadingDir | null) => void
  /** The global default changed (`PUT /api/settings`) — this tab's or another's. */
  setGlobalDirection: (dir: ReadingDir) => void
  /** The book → series → global ladder, as the server resolves it. */
  effectiveDirectionOf: (bid: ID) => ReadingDir
  gets: { bid: ID; w: string | null; v: string | null }[]
  puts: { bid: ID; body: unknown }[]
  deletes: ID[]
  seriesPrefsPuts: { sid: ID; body: unknown }[]
}

/**
 * FNV-1a, 32 bits — a stable stand-in for §2.7's hash. The real `cover_cv` format is the
 * server's (arch §5.6) and nothing in the SPA reads it; what the mock has to keep is that
 * equal inputs give equal versions and different ones different versions.
 */
function fnv1a(text: string): string {
  let hash = 0x811c9dc5
  for (let i = 0; i < text.length; i += 1) {
    hash ^= text.charCodeAt(i)
    hash = Math.imul(hash, 0x01000193) >>> 0
  }
  return hash.toString(16).padStart(8, '0')
}

/** §2.7's key domain for a book cover; a chosen cover's version never reads as a default one's. */
const CHOSEN_CV_DOMAIN = 'shelf-cover/1'

/** A chosen cover's inputs, apart from the content cv: the book, the page and the region. */
function regionInputs(bid: ID, cover: BookCover): string {
  return [bid, cover.page, cover.x, cover.y, cover.w, cover.h].join('|')
}

/** The JPEG answer a cover `GET` gives; jsdom never decodes it. */
const JPEG_BYTES = new Uint8Array([0xff, 0xd8, 0xff, 0xd9])

export function createCoverMock(origin: string, options: CoverMockOptions): CoverMock {
  const facts = new Map(options.books.map((book) => [book.id, book] as const))
  const covers = new Map(options.books.map((book) => [book.id, book.cover] as const))
  const knownSeries = new Set(options.series ?? [])
  const seriesDirs = new Map<ID, ReadingDir | null>(
    [...knownSeries].map((sid) => [sid, options.seriesDirs?.[sid] ?? null] as const),
  )
  const bookDirs = new Map<ID, ReadingDir | null>(
    options.books.map((book) => [book.id, options.bookDirs?.[book.id] ?? null] as const),
  )
  let globalDir: ReadingDir = options.globalDir ?? fixtureSettings.reading_direction

  const effectiveDirectionOf = (bid: ID): ReadingDir => {
    const series = facts.get(bid)?.series_id
    return (
      bookDirs.get(bid) ??
      (series === undefined ? null : (seriesDirs.get(series) ?? null)) ??
      globalDir
    )
  }
  /** §2.5: a stored choice is used only while its page is in the book and the book is `ok`. */
  const applies = (bid: ID): boolean => {
    const cover = covers.get(bid) ?? null
    const book = facts.get(bid)
    return (
      cover !== null && book !== undefined && book.status === 'ok' && cover.page <= book.page_count
    )
  }

  // A chosen cover's version is a function of its inputs (§2.7: book_id ‖ page ‖ region ‖
  // content cv — no direction): the same region again is the same version, however many
  // writes came between. A book that starts on a chosen cover keeps the version it was given
  // for that region.
  const chosenCvs = new Map<string, string>()
  const chosenCvOf = (bid: ID, cover: BookCover): string => {
    const inputs = regionInputs(bid, cover)
    return (
      chosenCvs.get(inputs) ??
      `${bid.slice(0, 8)}${fnv1a(`${CHOSEN_CV_DOMAIN}|${inputs}|${facts.get(bid)?.cv ?? ''}`)}`
    )
  }
  // A default cover's version per (book, direction) — the starting one is the given one.
  const autoCvs = new Map<string, string>()
  for (const book of options.books) {
    if (applies(book.id) && book.cover !== null) {
      chosenCvs.set(regionInputs(book.id, book.cover), book.cover_cv)
    }
    else autoCvs.set(`${book.id}|${effectiveDirectionOf(book.id)}`, book.cover_cv)
  }
  const autoCvOf = (bid: ID, dir: ReadingDir): string =>
    autoCvs.get(`${bid}|${dir}`) ?? `${bid.slice(0, 8)}auto-${dir}`
  const coverCvOf = (bid: ID): string | undefined => {
    if (!facts.has(bid)) return undefined
    const cover = covers.get(bid) ?? null
    return applies(bid) && cover !== null
      ? chosenCvOf(bid, cover)
      : autoCvOf(bid, effectiveDirectionOf(bid))
  }

  const mock: CoverMock = {
    handlers: [],
    covers,
    seriesDirs,
    coverCvOf,
    summaryOf: (bid) => {
      const book = facts.get(bid)
      if (book === undefined) throw new Error(`createCoverMock: no book ${bid}`)
      return { ...book, cover: covers.get(bid) ?? null, cover_cv: coverCvOf(bid) ?? book.cover_cv }
    },
    setBookDirection: (bid, dir) => {
      bookDirs.set(bid, dir)
    },
    setGlobalDirection: (dir) => {
      globalDir = dir
    },
    effectiveDirectionOf,
    gets: [],
    puts: [],
    deletes: [],
    seriesPrefsPuts: [],
  }

  mock.handlers = [
    http.get(`${origin}/api/books/:bid/cover`, ({ params, request }) => {
      const bid = String(params.bid)
      const url = new URL(request.url)
      const v = url.searchParams.get('v')
      mock.gets.push({ bid, w: url.searchParams.get('w'), v })
      const book = facts.get(bid)
      if (book === undefined) return errorJson(404, 'not_found', 'no such book')
      // §2.6: no pages ⇒ nothing to make a cover from. A non-ok book with pages still has one.
      if (book.page_count === 0) return errorJson(404, 'not_found', 'book has no pages')
      const current = coverCvOf(bid) ?? ''
      if (v !== null && v !== current) {
        return errorJson(409, 'stale_version', 'cover version changed', { cv: current })
      }
      return new HttpResponse(JPEG_BYTES, { headers: { 'Content-Type': 'image/jpeg' } })
    }),
    http.put(`${origin}/api/books/:bid/cover`, async ({ params, request }) => {
      const bid = String(params.bid)
      const body: unknown = await request.json().catch(() => undefined)
      mock.puts.push({ bid, body })
      const book = facts.get(bid)
      if (book === undefined) return errorJson(404, 'not_found', 'no such book')
      const violation = bookCoverViolation(body, book)
      if (violation !== null) return errorJson(400, 'bad_request', violation)
      const { page, x, y, w, h } = body as BookCover
      const cover: BookCover = { page, x, y, w, h }
      covers.set(bid, cover)
      return HttpResponse.json({ cover_cv: chosenCvOf(bid, cover), cover })
    }),
    http.delete(`${origin}/api/books/:bid/cover`, ({ params }) => {
      const bid = String(params.bid)
      mock.deletes.push(bid)
      const book = facts.get(bid)
      if (book === undefined) return errorJson(404, 'not_found', 'no such book')
      // §2.6: 204 whether or not there was a choice; the default cover — of the
      // direction now in force — is back.
      covers.set(bid, null)
      return new HttpResponse(null, { status: 204 })
    }),
    http.put(`${origin}/api/series/:sid/prefs`, async ({ params, request }) => {
      const sid = String(params.sid)
      const body: unknown = await request.json().catch(() => undefined)
      mock.seriesPrefsPuts.push({ sid, body })
      const violation = seriesPrefsViolation(body)
      if (violation !== null) return errorJson(400, 'bad_request', violation)
      if (!knownSeries.has(sid)) return errorJson(404, 'not_found', 'no such series')
      const dir = (body as { reading_direction: ReadingDir | null }).reading_direction
      seriesDirs.set(sid, dir)
      return HttpResponse.json({ reading_direction: dir })
    }),
  ]
  return mock
}
