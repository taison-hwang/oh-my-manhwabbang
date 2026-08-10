/**
 * `파일이 변경되었습니다` — ruling **E-45**, at the browser tier, and the one
 * check in this repository that stands on the `stale_seen` **seam**.
 *
 * ## Why this file is the only thing guarding that seam
 *
 * E-45 §4 counts the tiers and finds that each one guards its own half:
 *
 *  * `scripts/contractcheck/main.go:48-53` does not look at request bodies at
 *    all, on purpose — `stale_seen` lives exactly where that check cannot see;
 *  * `internal/httpapi/api_test.go` pins *"the server accepts `stale_seen`"*;
 *  * `web/src/.../ViewerPage.test.tsx` pins *"the client sends `stale_seen`"*;
 *  * **nothing compares the two.**
 *
 * So renaming the Go tag to `json:"staleSeen"` and fixing `api_test.go` beside
 * it leaves all five gates green while the real acknowledgement `PUT` is
 * refused by `DisallowUnknownFields` (`internal/httpapi/params.go`) with a
 * `400`, and the warning never goes away again. The other direction — the
 * client quietly dropping the field — answers `200` and is quieter still.
 * Two checks that each guard one side of a seam do not guard the seam; that is
 * HANDOFF §6.5 with the parties named. Hence the assertion this file exists
 * for: not "the notice appears" but **"the acknowledgement actually took"** —
 * once the notice has run its life the server must answer `stale: false` and
 * must have moved its recorded `page_count` to the book's current length.
 *
 * The two directions are told apart on the way, because a red test that cannot
 * say *which* side moved sends the next session to the wrong file:
 *
 *  | what broke | what reddens here |
 *  |---|---|
 *  | client stopped sending the field | no `PUT` body carries `stale_seen` |
 *  | wire names disagree (Go tag renamed) | the body carries it, the response is `400` |
 *  | the field arrives and does nothing | `200`, and the server still says `stale` |
 *
 * ## Why a file of its own rather than more of 04-viewer or 09-viewer-chrome
 *
 *  * **04-viewer** is `serial` around one book's *persisted progress*, and this
 *    spec fabricates a progress row through an unrelated endpoint. Nothing that
 *    rewrites `user.db` rows out from under a chain of assertions belongs
 *    inside that chain.
 *  * **09-viewer-chrome** is E-27/E-28's display model, and its docblock says so
 *    — down to naming the two books it owns and why each was chosen. E-45 is a
 *    different ruling with a different premise (a *fabricated baseline*, not a
 *    display property), and a third ruling in that file would make its opening
 *    paragraph false. This directory treats those paragraphs as load-bearing.
 *  * The number puts it **last** in every project's run, which is where a spec
 *    that rewrites progress rows should sit: every other spec has already read
 *    and shot what it needed by then.
 *
 * It borrows 09's *shape* deliberately — a mirrored lifetime constant, a floor
 * with its reasoning written down, and the disappearance measured rather than
 * slept through — because E-45 §1 hands this notice the opening hint's contract
 * and the two checks should be readable side by side.
 *
 * ## How the premise is built, and why it is not a rescan
 *
 * `stale` is `recorded page_count ≠ current page_count` (`convert.go`), so the
 * premise needs those two numbers to disagree. Touching the archive on disk and
 * rescanning would be a different test — slow, and destructive in the round
 * that runs against the reader's real collection. The user-data round trip does
 * it in two requests instead: `GET /api/progress/export` hands back the row,
 * one number in it is moved, and `POST /api/progress/import?strategy=replace`
 * writes it back verbatim (`internal/userdata/export.go` `importProgress` takes
 * `page_count` from the document without consulting the index — E-45 §3 rules
 * that path is *correct* and must stay). `replace`, never `merge`: merge
 * compares `updated_at` and would skip the row it was handed.
 *
 * Two properties of that fabrication are not incidental:
 *
 *  * the document's own `format` / `id_version` envelope is echoed back rather
 *    than written out here, so this file holds no copy of `shelf-id/1` to rot;
 *  * the drifted length is **not zero**. `isStale` is symmetric as of E-45 §2 —
 *    a `0` on *either* side means "length unknown", not "changed" — so a
 *    fabrication that zeroed the baseline would assert nothing at all.
 */

import type { Locator, Page } from '@playwright/test'

import {
  booksOf,
  clearProgress,
  expect,
  openViewerDirect,
  resetBookPrefs,
  SERIES,
  seriesId,
  shot,
  test,
  viewer,
  waitForPage,
} from './shelf'

/** `store/viewer.ts`'s `STALE_NOTICE_MS` — how long E-45 §1 gives the notice. */
const STALE_NOTICE_MS = 3400

/** `ViewerPage.tsx`: the sentence E-45 gave a lifetime to, fixed verbatim. */
const STALE_NOTICE = '파일이 변경되었습니다'

/**
 * The floor the notice's life is held against.
 *
 * The same argument as 09's `HINT_FLOOR_MS`, with one addition. The clock starts
 * when Playwright first *observed* the notice, which is already some way past
 * the `open()` that armed the 3 400 ms timer, so the measurement can only
 * under-count — this is not `STALE_NOTICE_MS` minus a tolerance, it is the
 * answer to "was it on screen long enough to be read".
 *
 * The addition is what the number has to clear. **The shipped defect measured
 * about one second**: `stale` was a per-render derivation of the React Query
 * cache, so the debounced `PUT` that the viewer sends a second after any book
 * loads unmounted the notice as its response landed (E-45 §1). 1 500 ms is
 * therefore above the defect and far below the contract. It is a second net —
 * the assertion that actually names that defect is the one below that waits for
 * that very write and then looks again.
 */
const STALE_FLOOR_MS = 1500

/**
 * How far the fabricated baseline is moved from the book's real length.
 *
 * Upward, so that the import's `clampPage(last_page, page_count)` has nothing to
 * do and the row that comes back is the row that was sent. The exact size is
 * irrelevant — only "not equal, and not zero" is (see the header).
 */
const BASELINE_DRIFT = 7

/** The FR-VWR-009 notice E-45 §1 gave a lifetime and a live region. */
function staleNotice(page: Page): Locator {
  return page.locator('[data-role="stale-progress"]')
}

/** `open()`'s other latch — the E-27 line, used here only as proof `open()` ran. */
function chromeHint(page: Page): Locator {
  return page.locator('[data-role="viewer-chrome-hint"]')
}

/** One row of `GET /api/progress/export`, arch §7.11. */
interface ExportItem {
  readonly book_id: string
  readonly series_id: string
  readonly root_name: string
  readonly book_path: string
  readonly last_page: number
  readonly page_count: number
  readonly completed: boolean
  readonly started_at: number
  readonly updated_at: number
}

/** The FR-STT-004 document itself. */
interface ProgressExport {
  readonly format: string
  readonly exported_at: number
  readonly id_version: string
  readonly items: readonly ExportItem[]
  readonly prefs: readonly unknown[]
}

/** What `GET /api/books/{bid}` says about a book's saved place (arch §7.3). */
interface ProgressFact {
  readonly last_page: number
  readonly page_count: number
  readonly completed: boolean
  readonly stale: boolean
}

/** The reader's saved place as the *server* currently sees it, or null. */
async function progressOf(page: Page, bookId: string): Promise<ProgressFact | null> {
  const response = await page.request.get(`/api/books/${bookId}`)
  expect(response.ok(), `GET /api/books/${bookId}`).toBe(true)
  const body = (await response.json()) as { progress: ProgressFact | null }
  return body.progress
}

/** The body of one `PUT …/progress`, as the client actually put it on the wire. */
interface ProgressPutBody {
  readonly page?: number
  readonly completed?: boolean
  readonly stale_seen?: boolean
}

interface ProgressPut {
  readonly body: ProgressPutBody
  readonly status: number
}

/**
 * Records every `PUT …/progress` this book sees, with its **request body** and
 * its **status**.
 *
 * Both halves are the point. A check that only watched the server's answer could
 * not tell "the client never sent the field" from "the server refused it", and
 * those two send a reader to opposite ends of the repository. `page.request`
 * calls — the fabrication above — are not page traffic and never appear here.
 */
function recordProgressPuts(page: Page, bookId: string): ProgressPut[] {
  const seen: ProgressPut[] = []
  page.on('response', (response) => {
    const request = response.request()
    if (request.method() !== 'PUT') return
    if (!response.url().includes(`/api/books/${bookId}/progress`)) return
    const raw = request.postData()
    seen.push({
      body: raw === null ? {} : (JSON.parse(raw) as ProgressPutBody),
      status: response.status(),
    })
  })
  return seen
}

/** The acknowledgement E-45 §2 defines: the flag, spelled the way the wire spells it. */
function isAck(put: ProgressPut): boolean {
  return put.body.stale_seen === true
}

/**
 * Lets React commit whatever the response that just landed caused.
 *
 * `waitForResponse` and the poll below resolve on the *network*, which is a beat
 * ahead of the query cache being written and a beat and a half ahead of the
 * render that would have unmounted the notice under the old code. Two frames is
 * what makes "and it is still there" an assertion rather than a race won by
 * arriving early — the shipped defect unmounted synchronously in the mutation's
 * `onSuccess`, so a frame is all it ever needed.
 */
async function settleFrames(page: Page): Promise<void> {
  await page.evaluate(
    () =>
      new Promise<void>((resolve) => {
        requestAnimationFrame(() => {
          requestAnimationFrame(() => {
            resolve()
          })
        })
      }),
  )
}

/** The one-ZIP series: one book, so no volume-end card can share the screen. */
async function wheelBook(page: Page): Promise<{ sid: string; bid: string; total: number }> {
  const sid = await seriesId(page, SERIES.wheel)
  const books = await booksOf(page, sid)
  const book = books.find((candidate) => candidate.status === 'ok')
  expect(book, '§6.3 row 4: 바퀴.zip is a top-level ZIP that is its own book').toBeDefined()
  return { sid, bid: book?.id ?? '', total: book?.page_count ?? 0 }
}

/**
 * Moves the recorded baseline off the book's current length, through the
 * FR-STT-004 round trip. See the header for why this and not a rescan.
 */
async function driftBaseline(page: Page, bookId: string, recorded: number): Promise<void> {
  const exported = await page.request.get('/api/progress/export')
  expect(exported.ok(), 'GET /api/progress/export').toBe(true)
  const doc = (await exported.json()) as ProgressExport

  const item = doc.items.find((candidate) => candidate.book_id === bookId)
  expect(
    item,
    'the export must carry the row this spec just wrote — without it there is nothing to drift',
  ).toBeDefined()
  if (item === undefined) return

  // The envelope is echoed, never authored: `format` and `id_version` are the
  // server's own and `Import` refuses a mismatch outright. `prefs: []` keeps the
  // blast radius to one progress row — `importPrefs` returns early on empty.
  const imported = await page.request.post('/api/progress/import?strategy=replace', {
    data: {
      ...doc,
      items: [{ ...item, page_count: recorded }],
      prefs: [],
    },
  })
  expect(imported.ok(), 'POST /api/progress/import?strategy=replace').toBe(true)
  const result = (await imported.json()) as { imported: number }
  expect(result.imported, 'the drifted row must actually have been written').toBe(1)
}

/**
 * shelf.ts rule 2, in a hook rather than at the end of the body — **because the
 * state this spec invents is dangerous to leave behind and a failing test never
 * reaches its own last line.**
 *
 * Every other spec here cleans up inline, and for their state that is enough: a
 * leftover progress row draws a bar on a series card. This one leaves a
 * *fabricated stale baseline* on 바퀴.zip, which 09-viewer-chrome opens three
 * times in the next viewport project — so the next project's viewer would carry
 * a notice nobody asked for, over the E-27 assertions and into their §7.4
 * screenshots, and its acknowledgement would be console noise under
 * NFR-CMP-001. Measured, not reasoned: with the cleanup inline, the mutation run
 * that proves this file's worth reddened **09-viewer-chrome in three projects**
 * as well, which is a failure that names the wrong file.
 *
 * The book is looked up again rather than closed over: a hook that depended on
 * the body having got far enough to assign a variable would be the same bet
 * that put the cleanup at the end of the body.
 */
test.afterEach(async ({ page }) => {
  const { bid } = await wheelBook(page)
  await clearProgress(page, bid)
})

/**
 * The whole of E-45 in one entry: the notice arrives, outlives the write that
 * used to kill it, leaves on its own — and the acknowledgement it leaves behind
 * reaches the server, is accepted, and moves the baseline.
 *
 * The last clause is the one no other tier can make (see the header). The four
 * before it are the ruling's own list, in the order a reader meets them.
 */
test('E-45 · the stale-progress notice lives its 3.4 s, and its acknowledgement takes', async ({
  page,
}, info) => {
  const { sid, bid, total } = await wheelBook(page)
  expect(total, 'a book with pages is the premise of every viewer assertion').toBeGreaterThan(1)
  await resetBookPrefs(page, bid)

  // ---- a saved place to make stale ----------------------------------------
  // Written over the API rather than by reading, because what is under test
  // starts at the *next* entry: this one only has to exist.
  const wrote = await page.request.put(`/api/books/${bid}/progress`, { data: { page: 1 } })
  expect(wrote.ok(), `PUT /api/books/${bid}/progress`).toBe(true)

  // ---- entry 1: nothing has changed, so nothing is said --------------------
  // Two jobs. It proves the notice is *conditional* — a `toHaveCount(0)` after
  // the drift would otherwise be a zero that could equally mean "this locator
  // never matches anything" — and it decodes page 1, whose response is
  // `immutable` (arch §5.3), so the entry that has 3 400 ms to work with paints
  // from cache instead of racing a cold archive.
  await openViewerDirect(page, sid, bid, 1)
  await waitForPage(page)
  await expect(
    chromeHint(page),
    'the opening hint is `open()`’s other latch: while it is up, `open()` has certainly run',
  ).toBeVisible()
  await expect(
    staleNotice(page),
    'FR-VWR-009: the recorded length still matches the file, so there is nothing to warn about',
  ).toHaveCount(0)

  // ---- the premise ---------------------------------------------------------
  const drifted = total + BASELINE_DRIFT
  await driftBaseline(page, bid, drifted)
  expect(
    await progressOf(page, bid),
    'the round trip must leave a baseline that disagrees with the index and is not 0 — ' +
      '`isStale` is symmetric (E-45 §2) and a 0 on either side is not stale',
  ).toMatchObject({ page_count: drifted, stale: true })

  // ---- entry 2: the notice ------------------------------------------------
  const writes = recordProgressPuts(page, bid)
  await openViewerDirect(page, sid, bid, 1)

  const notice = staleNotice(page)
  await expect(
    notice,
    'FR-VWR-009 / E-45 §1: the file changed under the reader, so the viewer says so',
  ).toBeVisible()
  // Taken here, before anything below can block: every wait that follows
  // happens *inside* the notice's life, so this can only under-count.
  const seenAt = Date.now()
  await expect(notice, 'E-45 fixes this sentence verbatim').toHaveText(STALE_NOTICE)
  await expect(
    notice,
    'E-45 §1: a notice that removes itself and has no live region has a lifetime of zero on a screen reader',
  ).toHaveAttribute('role', 'status')

  // ---- …survives the write that used to unmount it -------------------------
  // The defect, exactly: `useProgressSync` writes the reader's page a second
  // after any book loads — nobody has to touch anything — and `useSaveProgress`'s
  // `onSuccess` overwrites the detail cache with the response. While `stale` was
  // derived from that cache per render, this response was the end of the notice.
  await expect
    .poll(() => writes.filter((put) => !isAck(put)).length, {
      timeout: 15_000,
      message:
        'FR-VWR-009: the viewer writes the reader’s page about a second after the book loads, ' +
        'and that write is the premise of the assertion after it',
    })
    .toBeGreaterThan(0)
  await settleFrames(page)
  await expect(
    notice,
    'E-45 §1: the lifetime is latched store state, not a per-render read of the query cache — ' +
      'the progress write has landed and overwritten the cache, and the notice is still on screen',
  ).toBeVisible()

  // ---- the §7.4 review shot, with the page painted under the notice --------
  await waitForPage(page)
  await shot(page, info, 'e45-viewer-stale-progress')

  // ---- …and then it goes, on a timer, with no way to dismiss it ------------
  await expect(
    notice,
    'E-45 §1: timed, not dismissible — the same contract the opening hint has carried since E-27',
  ).toHaveCount(0, { timeout: STALE_NOTICE_MS * 3 })
  const lived = Date.now() - seenAt
  expect(
    lived,
    `the notice must stay long enough to be read: ${String(STALE_NOTICE_MS)}ms from open, and this ` +
      `measures ${String(lived)}ms from first sight, which can only be less. The shipped defect ` +
      `measured about 1000ms`,
  ).toBeGreaterThan(STALE_FLOOR_MS)

  // ---- ★ the seam: the acknowledgement reached the server and was accepted --
  await expect
    .poll(() => writes.filter(isAck).length, {
      timeout: 15_000,
      message:
        'E-45 §2: a notice that has run its whole life is the one thing that may re-baseline, and ' +
        'it says so with `stale_seen: true` on the next PUT. No request body carried that field — ' +
        'either the client stopped sending it, or it is sending it under another name ' +
        '(web/src/api/types.ts `ProgressUpdate`, web/src/api/queries.ts `acknowledgeStale`)',
    })
    .toBeGreaterThan(0)
  const acks = writes.filter(isAck)
  expect(
    acks[0]?.status,
    'E-45 §4: the acknowledgement reached the server and was refused. `progressUpdateBody` is ' +
      'decoded with `DisallowUnknownFields` (internal/httpapi/params.go), so a name the Go struct ' +
      'does not carry is a 400 and the warning never goes away — while the Go tier and the vitest ' +
      'tier each stay green about their own half of the wire',
  ).toBe(200)

  // …and it did something. A 200 that changed nothing is the quiet direction.
  await expect
    .poll(
      async () => {
        const fact = await progressOf(page, bid)
        return { page_count: fact?.page_count, stale: fact?.stale }
      },
      {
        timeout: 15_000,
        message:
          'E-45 §2: an acknowledged write re-takes the baseline — `page_count` moves to the ' +
          'index’s current length and `stale` goes false. Anything else means the field crossed ' +
          'the wire and the storage layer did not act on it',
      },
    )
    .toEqual({ page_count: total, stale: false })

  expect(
    acks,
    'E-45 §1 REVISION: the latch is consumed in the same tick it is spent, so a re-render cannot ' +
      'send a second acknowledgement',
  ).toHaveLength(1)

  // ---- entry 3: the reader is not told twice -------------------------------
  await openViewerDirect(page, sid, bid, 1)
  await expect(
    viewer(page),
    'the viewer is on screen, so this is an entry and not a leftover render',
  ).toBeVisible()
  await expect(
    chromeHint(page),
    '`open()` sets `hintVisible` and `staleVisible` in the same commit: while the hint is up, the ' +
      'absence below is `open()`’s answer and not a wait that has not finished',
  ).toBeVisible()
  await expect(
    notice,
    'E-45 §2: the baseline moved when the reader acknowledged it, so the second entry has nothing to say',
  ).toHaveCount(0)

  // The row this spec invented is taken away in `afterEach`, not here — see the
  // hook for the difference that makes.
})
