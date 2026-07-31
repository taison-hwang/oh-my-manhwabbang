/**
 * impl-plan §6.3 step 6, assertions 6 and 7 — the viewer, driven from the
 * series screen exactly the way a reader gets there (AC-003).
 *
 *   6.6   `→` five times · `T` opens the strip · `2` 양면 · `1` 단면 ·
 *         `R→L` puts page *n* on the right · `Esc` exits
 *   6.7   reopening the same volume resumes at the saved page (FR-VWR-009)
 *   6.6b  the half of 6.6 that 군계 structurally cannot show — a 양면 spread that
 *         really is two frames, on a portrait raster book (자살도)
 *
 * 6.6 and 6.7 share one book's persisted state — 6.7's expectation *is* what
 * 6.6 left behind, which is the only way to assert a resume without inventing a
 * progress row over the API and thereby testing the API instead of the screen.
 * They are wrapped in a `serial` describe for that reason, and **only** they.
 * `serial` at file scope, which is what used to stand here, put 6.6b in the same
 * chain: one flake anywhere in 6.6 — five key presses, the strip, four seg
 * switches and a progress round-trip — would then skip the repo's only coverage
 * of the raster RTL spread, i.e. the newest assertion is the first thing lost
 * when the oldest one breaks. 6.6b shares nothing with either: its own series,
 * its own book, and its own progress row, which it deletes again on the way out.
 *
 * ## Why the keyboard block comes before the mouse block
 *
 * `useViewerKeys` ignores every key whose `event.target` is `isTypingTarget`,
 * and `ds/Seg` is built from real radio inputs — so clicking 양면 or `R→L`
 * leaves an `<input type="radio">` focused and the whole of ui-spec §8.2 inert
 * until focus moves elsewhere. Measured in Chrome 150: after clicking the
 * 읽기 방향 seg, `2` does nothing. The keyboard assertions therefore run first,
 * from a page whose focus is still the body, and the direction flip — which has
 * no key binding of its own — runs last. Reported to the orchestrator as a
 * finding; this suite asserts the product as specified, not as convenient.
 */

import {
  booksOf,
  closeViewerSheet,
  currentPage,
  expect,
  gotoLibrary,
  openSeries,
  pageCount,
  resetBookPrefs,
  resetLibraryState,
  SERIES,
  seriesId,
  setView,
  setViewerSeg,
  shot,
  test,
  viewer,
  wakeChrome,
  waitForPage,
} from './shelf'

/** `→` five times, per §6.3 step 6.6. */
const TURNS = 5

interface PageDim {
  n: number
  w: number | null
  h: number | null
}

/**
 * The book's page dimensions, **after** the server has actually recorded them.
 *
 * arch §5.8: `pages.width`/`height` start `NULL` with `dims_state:"none"`, and
 * `GET /api/books/{bid}` only *enqueues* the background pass that fills them.
 * So the first response for a freshly indexed book carries 104 pages of
 * `w: null, h: null` — and a landscape test built on that answers "portrait"
 * for a book that is landscape from cover to cover.
 *
 * That is not a detail: it is a read-time race whose outcome depends on which
 * viewport project runs first. The pass finishes in ~1 s, so the *first*
 * project derived FR-VWR-004 from nulls and demanded a spread the viewer was
 * right to refuse, while the three that followed found the dimensions already
 * on disk and agreed with it. Waiting for `dims_state:"done"` — a documented
 * field of `BookDetail` (arch §7) — makes the derivation the same on all four.
 *
 * The wait is an assertion, not a sleep: if the dimension pass never completes,
 * FR-VWR-004 cannot be evaluated at all and the test must say so rather than
 * quietly fall back to "nothing is landscape".
 */
async function recordedDims(
  page: import('@playwright/test').Page,
  bid: string,
): Promise<PageDim[]> {
  let pages: PageDim[] = []
  await expect
    .poll(
      async () => {
        const body = (await (await page.request.get(`/api/books/${bid}`)).json()) as {
          dims_state: string
          pages: PageDim[]
        }
        pages = body.pages
        return body.dims_state
      },
      {
        timeout: 30_000,
        message: 'arch §5.8: the dimension pass must finish before FR-VWR-004 can be derived',
      },
    )
    .toBe('done')
  return pages
}

/**
 * FR-VWR-004's predicate, closed over dimensions the server has actually
 * recorded — i.e. over the output of `recordedDims` above.
 *
 * Only ever asked about a page that exists: every caller checks the page bound
 * first, so an unknown or non-positive size here is a broken premise rather
 * than a "portrait" answer. Swallowing it is exactly what let this file derive
 * FR-VWR-004 from `null`s and demand a spread the viewer was right to refuse.
 */
function landscapeIn(recorded: PageDim[], bid: string): (n: number) => boolean {
  return (n) => {
    const d = recorded.find((p) => p.n === n)
    expect(d, `page ${String(n)} must be in GET /api/books/${bid}`).toBeDefined()
    expect(d?.w ?? 0, `page ${String(n)} must have a recorded width`).toBeGreaterThan(0)
    expect(d?.h ?? 0, `page ${String(n)} must have a recorded height`).toBeGreaterThan(0)
    return (d?.w ?? 0) > (d?.h ?? 0)
  }
}

/** The first volume of 군계 — a real `kind:"dir"` book (prd §2.2 row 2). */
async function firstVolume(page: import('@playwright/test').Page): Promise<{
  sid: string
  bid: string
  name: string
}> {
  const sid = await seriesId(page, SERIES.gungye)
  const books = await booksOf(page, sid)
  const first = books.find((book) => book.status === 'ok')
  expect(first, '군계 must have an openable volume').toBeDefined()
  return { sid, bid: first?.id ?? '', name: first?.name ?? '' }
}

/**
 * The one book, read and then resumed. `serial` because 6.7's expectation is
 * whatever 6.6 left in `user.db`, and for no wider reason: 6.6b sits outside
 * this describe on purpose, so a flake here cannot skip it.
 */
test.describe('6.6/6.7 · one volume, read then resumed', () => {
  test.describe.configure({ mode: 'serial' })

  test('6.6 · keys, the thumbnail strip, 단면/양면, R→L and Esc', async ({ page }, info) => {
    const { sid, bid, name } = await firstVolume(page)
    await resetLibraryState(page)
    await resetBookPrefs(page, bid)
    // Start from page 1 deterministically: the volume tile opens at
    // `progress.last_page`, and by the second viewport project there is one.
    await page.request.put(`/api/books/${bid}/progress`, { data: { page: 1 } })

    // FR-VWR-004 depends on the *scan's* page dimensions, so what 양면 must show
    // is read from the contract rather than assumed — in both modes, and for the
    // same reason in both. Measured on the real collection, every page of
    // 군계 01권 is 1072×813 or 1075×811: 104 / 104 landscape, PIL over 100 % of
    // the volume (ruling E-23). The synthetic twin reproduces that shape rather
    // than only the name — `scripts/mkfixture/main.go` builds 군계 01권 out of
    // 60×40 pages, and out of enough of them that `1 + TURNS` is not the last
    // page. Landscape throughout either way, so 양면 must never pair here.
    const landscape = landscapeIn(await recordedDims(page, bid), bid)

    await gotoLibrary(page)
    await setView(page, 'grid')
    await openSeries(page, SERIES.gungye)

    // AC-003: a folder-type series is opened through the identical UI flow.
    await page.locator(`[data-testid="volume-grid"] [title="${name}"]`).click()
    await expect(viewer(page)).toBeVisible()
    await waitForPage(page)
    const total = await pageCount(page)
    expect(await currentPage(page)).toBe(1)
    await shot(page, info, 'step-06-6a-viewer-open')

    // ---- `→` five times (ui-spec §8.2) ------------------------------------
    for (let i = 0; i < TURNS; i++) await page.keyboard.press('ArrowRight')
    await expect
      .poll(async () => currentPage(page))
      .toBe(Math.min(1 + TURNS, total))
    const landed = await currentPage(page)

    // ---- `T` opens the strip (FR-VWR-008) ---------------------------------
    await page.keyboard.press('t')
    const strip = page.locator('[data-role="thumbnail-strip"]')
    await expect(strip).toBeVisible()
    await expect(strip.locator('[data-role="thumb"][data-current="true"]')).toHaveAttribute(
      'data-page',
      String(landed),
    )
    await shot(page, info, 'step-06-6b-viewer-thumbnail-strip')

    // ---- `2` → 양면, `1` → 단면 -------------------------------------------
    const stage = page.locator('[data-role="stage"]')
    await page.keyboard.press('2')
    await expect(stage).toHaveAttribute('data-mode', 'spread')

    // FR-VWR-004 (prd §3.6; ui-spec §6.2 "Double-page auto-split"; impl-plan WP-11
    // acceptance 5): a page whose intrinsic aspect is landscape (`w > h`) is a
    // two-page scan and renders single even in 양면 — and so does the page before
    // one, or pairing would put the rest of the book out of phase. 양면 therefore
    // legitimately shows one frame on 군계 and two on a portrait book.
    //
    // Both preconditions are *asserted*, never folded into the outcome — ruling
    // E-23's corollary, and HANDOFF §6.5 in one line. `stagePages` (fit.ts)
    // returns a single frame for three different reasons: no facing page
    // (`first + 1 > pageCount`), a landscape page, or a landscape facing page.
    // `toHaveCount(1)` cannot tell them apart, so whichever reason is not
    // asserted is a reason the test will silently accept instead.
    //
    // What used to stand here did exactly that:
    //   const paired = landed + 1 <= total && !landscape(landed) && !landscape(landed + 1)
    //   expect(paired).toBe(false)
    // `&&` short-circuits, and in the synthetic twin `landed` was the last page
    // of a four-page book — so the page bound alone made `paired` false,
    // `landscapeIn` was never called at all, and an assertion labelled
    // FR-VWR-004 was really asserting the last-page clamp. Split in two, each
    // half fails by name: the fixture that lost its depth, the fixture that lost
    // its orientation, or the product that lost the auto-split.
    //
    // What this tier still cannot separate is FR-VWR-004's two landscape clauses
    // from each other. 군계 is landscape cover to cover in both modes, so with the
    // page bound asserted, `stagePages` refuses to pair via `isLandscape(first)`
    // and via `isLandscape(first + 1)` alike. Measured on 2026-07-29 by deleting
    // `if (isLandscape(dims(first))) return [first]` from fit.ts: on an all-
    // landscape book the mutant still returns exactly one page at every page of
    // the volume, so neither assertion below can move and this test, 6.7 and 6.6b
    // all stay green. Deleting both clauses does fail here (count 2 where 1 is
    // required), so what this covers is the pair, jointly. Telling the two apart
    // is fit.test.ts's job: its `dims` is landscape at exactly one page, so the
    // same mutant turns it 2-red at `stagePages(7)` and at `nextPage(7)` and each
    // clause has a test that is only about it. That is the right tier for a pure
    // function's branches; asking for it here would need a mixed-orientation 군계,
    // which the real volume contradicts (E-23: 104 / 104 landscape) — the fixture
    // would have to lie, and `make e2e` would fail on the lie.
    const frames = page.locator('[data-role="page-frame"]')
    expect(
      landed + 1,
      'a 양면 spread needs a facing page to refuse — without one, a single frame proves fit.ts’s last-page clamp and nothing about FR-VWR-004',
    ).toBeLessThanOrEqual(total)
    expect(
      landscape(landed),
      'FR-VWR-004 only fires on a landscape page, and 군계 01권 is a two-page scan cover to cover (E-23: 104 / 104 real; 60×40 pages in the synthetic twin)',
    ).toBe(true)
    await expect(
      frames,
      'FR-VWR-004: a landscape page is shown alone even in 양면',
    ).toHaveCount(1)
    // …and it is the right page: a count alone would accept a stage that dropped
    // the landed page and kept its neighbour. Read as one list rather than as the
    // `if (!paired) toHaveAttribute(…)` that used to stand here — a check written
    // for one branch silently disappears in the other, and the guard was really
    // only there because `toHaveAttribute` cannot be asked of two matches.
    await expect
      .poll(
        async () =>
          frames.evaluateAll((els) => els.map((el) => Number(el.getAttribute('data-page')))),
        {
          message:
            'FR-VWR-004 chooses how many pages 양면 shows, never which: the stage carries the landed page, and the facing page it is not allowed to pair with stays off it',
        },
      )
      .toEqual([landed])

    await page.keyboard.press('1')
    await expect(stage).toHaveAttribute('data-mode', 'single')
    await expect(page.locator('[data-role="page-frame"]')).toHaveCount(1)

    // ---- `R→L` puts page n on the right ------------------------------------
    await setViewerSeg(page, '표시 모드', 'spread')
    await expect(stage).toHaveAttribute('data-flow', 'row')
    await setViewerSeg(page, '읽기 방향', 'rtl')
    await expect(stage).toHaveAttribute('data-dir', 'rtl')
    // The rule lives entirely in `flex-direction: row-reverse` (fit.ts): the DOM
    // order stays ascending and the flow is what moves page n to the right.
    await expect(stage).toHaveAttribute('data-flow', 'row-reverse')
    // …and that attribute is as far as *this* volume can take it, because
    // `row-reverse` on a single-item flex container is a no-op and 군계's stage is
    // single-item here. That much is asserted, not assumed — it is the
    // `toHaveCount(1)` above, standing on the two preconditions asserted with it,
    // and it holds for the same reason in both modes: page `landed` is landscape
    // and FR-VWR-004 refuses to pair it. Real mode measures 104 / 104 landscape
    // on 군계 01권 (1072×813 / 1075×811, PIL over the volume as it sits on disk —
    // ruling E-23, and the source of the figures at the top of this test); the
    // synthetic twin builds the same volume out of 60×40 pages
    // (scripts/mkfixture/main.go, D-49).
    //
    // An `if (paired) { … }` x-order block used to sit here. It had never
    // run under either mode and it read as coverage, which is worse than absent
    // (HANDOFF §6.5). Where the two-frame geometry *and* the DOM order are
    // actually proved: 6.6b below on 자살도 (raster) and 05-pdf-and-large 6.8 on
    // 미생 (PDF).
    await shot(page, info, 'step-06-6c-viewer-rtl-spread')

    // Restore: reading direction and display mode are per-book server state
    // (arch §7.6), so leaving them flipped would change what the next viewport
    // project — and 6.7 — opens.
    await setViewerSeg(page, '읽기 방향', 'ltr')
    await setViewerSeg(page, '표시 모드', 'single')
    await closeViewerSheet(page)

    // ---- the progress the resume test will read back -----------------------
    await expect
      .poll(
        async () => {
          const body = (await (await page.request.get(`/api/books/${bid}`)).json()) as {
            progress: { last_page: number } | null
          }
          return body.progress?.last_page ?? 0
        },
        { timeout: 15_000 },
      )
      .toBe(landed)

    // ---- `Esc` exits --------------------------------------------------------
    await page.keyboard.press('Escape')
    await expect(viewer(page)).toHaveCount(0)
    await expect(page).toHaveURL(new RegExp(`/series/${sid}$`))
  })

  test('6.7 · reopening the volume resumes at the saved page', async ({ page }, info) => {
    const { bid, name } = await firstVolume(page)
    const body = (await (await page.request.get(`/api/books/${bid}`)).json()) as {
      progress: { last_page: number } | null
    }
    const saved = body.progress?.last_page ?? 0
    expect(saved, '6.6 must have left a progress row behind').toBeGreaterThan(1)

    await gotoLibrary(page)
    await setView(page, 'grid')
    await openSeries(page, SERIES.gungye)
    await page.locator(`[data-testid="volume-grid"] [title="${name}"]`).click()

    await expect(viewer(page)).toBeVisible()
    await waitForPage(page)
    // FR-VWR-009: the tile opens at `progress.last_page`, not at page 1.
    expect(await currentPage(page)).toBe(saved)

    await wakeChrome(page)
    await shot(page, info, 'step-06-7-viewer-resume')

    await page.keyboard.press('Escape')
    await expect(viewer(page)).toHaveCount(0)
  })
})

/**
 * §6.3 step 6.6's "`R→L` puts page *n* on the right", on a book that can hold a
 * spread — and on the **raster** path, where the page is a plain JPEG and no
 * pdfium render stands between the file and the frame.
 *
 * 군계 cannot show it (see 6.6) and 05-pdf-and-large 6.8 shows it on a PDF, so
 * until now the path a reader actually spends their time in — an image scan,
 * ZIP or folder, in 양면 — had never had its spread rendered by a browser in
 * this suite. 자살도 is the collection's single instance of prd §2.2 row 3
 * (images loose in the series directory, impl-plan §6.3 row 3): one book whose
 * 181 real pages measured 181 portrait and 0 landscape (PIL over the pages as
 * they sit on the volume; the page count is the one scripts/e2e-assert.py pins
 * as "자살도 holds 181 loose pages"), against eleven equally portrait pages in
 * the synthetic twin — a 40×60 JPEG each, scripts/mkfixture/main.go:80-81 and
 * :289 (D-49). So 양면 pairs here in both modes, which is what shelf.ts rule 1
 * requires of every assertion below — and none of it is taken on trust: the
 * pair's dimensions are read back from the API and asserted portrait before a
 * single box is measured.
 *
 * Two assertions, not one:
 *
 *  * the **x geometry** proves what ui-spec §6.2 promises the reader — page *n*
 *    is the one on the right;
 *  * the **DOM order** proves the invariant fit.ts:8-13 is written around — the
 *    page array stays ascending and `flex-direction` is the only thing that
 *    flips.
 *
 * The second does not catch a mutation the first misses, and it is asserted
 * anyway. With two frames and nothing but flex direction steering them, the
 * geometry plus `data-flow` already determine the DOM order — reverse the array
 * under `row-reverse` and page n moves left (the x check fails); reverse it and
 * flip the flow back to `row`, the cancellation fit.ts:8-13 warns about, and
 * `data-flow` fails instead. But that is an *inference* from "flex direction is
 * the only thing ordering this stage", impl-plan WP-11 acceptance 4 asks for a
 * DOM-order test by name, and document order is what a screen reader, a text
 * selection and a copy follow no matter what the CSS does.
 */
test('6.6b · 양면 on a portrait raster book: R→L puts page n right, DOM ascending', async ({
  page,
}) => {
  const sid = await seriesId(page, SERIES.suicide)
  const books = await booksOf(page, sid)
  const book = books.find((candidate) => candidate.status === 'ok')
  expect(book, '§6.3 row 3: 자살도 is one openable book of loose images').toBeDefined()
  const bid = book?.id ?? ''
  const total = book?.page_count ?? 0
  expect(total, '양면 needs a facing page, so this volume needs at least two').toBeGreaterThan(1)

  await resetLibraryState(page)
  await resetBookPrefs(page, bid)
  // Derived, never chosen: shelf.ts rule 1 is that the identical suite passes
  // against the synthetic tree, where this series holds 11 pages instead of 181.
  // `total - 1` is what guarantees `start + 1` exists; the 20 only keeps the
  // landing off the cover when the real book is there to land in.
  const start = Math.max(1, Math.min(20, total - 1))
  await page.request.put(`/api/books/${bid}/progress`, { data: { page: start } })

  // Whether 양면 pairs at all is FR-VWR-004's decision and it takes it from the
  // recorded page dimensions, so the premise of this whole test is read from
  // the contract rather than assumed. If either page of the pair is ever
  // landscape the viewer is *right* to show one frame and this test is proving
  // nothing — which it then has to say out loud instead of passing.
  const landscape = landscapeIn(await recordedDims(page, bid), bid)
  expect(landscape(start), `page ${String(start)} must be portrait for 양면 to pair`).toBe(false)
  expect(
    landscape(start + 1),
    `page ${String(start + 1)} must be portrait for 양면 to pair`,
  ).toBe(false)

  await gotoLibrary(page)
  await setView(page, 'grid')
  await openSeries(page, SERIES.suicide)
  await page.locator(`[data-testid="volume-grid"] [title="${book?.name ?? ''}"]`).click()
  await expect(viewer(page)).toBeVisible()
  await waitForPage(page)
  // The counter is the independent witness that this is the book the API just
  // described and that the tile resumed where the progress row said.
  expect(await pageCount(page), 'the viewer must have opened the book the API named').toBe(total)
  expect(await currentPage(page)).toBe(start)

  const stage = page.locator('[data-role="stage"]')
  const frames = page.locator('[data-role="page-frame"]')
  await setViewerSeg(page, '표시 모드', 'spread')
  await expect(stage).toHaveAttribute('data-flow', 'row')
  // Unconditional. Both pages were asserted portrait above, so FR-VWR-004 does
  // not fire and 양면 is two frames by definition; wrapping what follows in an
  // `if (count === 2)` is how the block this test replaces went four viewport
  // projects without ever executing.
  await expect(frames, '양면 on two portrait pages must be two frames').toHaveCount(2)
  // Decoded, not merely mounted. A frame still `loading` renders its `<img>`
  // absolutely positioned at `opacity: 0` (PageFrame), so it is a zero-width,
  // invisible box that nonetheless carries a bounding rect the comparisons
  // below would happily accept — a spread proved against a page that never
  // painted. Not hypothetical: it happened to the 미생 PDF at desktop-1440 in
  // the 2026-07-29 04:47 round, whose step-06-8b RTL-spread screenshot came out
  // with a single page in it while every geometry assertion passed. See
  // 05-pdf-and-large 6.8 for the pixel measurement behind that.
  await expect(
    page.locator('[data-role="page-frame"][data-status="ready"]'),
    'both pages of the spread must decode before their boxes can be compared',
  ).toHaveCount(2, { timeout: 30_000 })

  const before = await frames.evaluateAll((els) =>
    els.map((el) => ({ n: Number(el.getAttribute('data-page')), x: el.getBoundingClientRect().x })),
  )
  await setViewerSeg(page, '읽기 방향', 'rtl')
  await expect(stage).toHaveAttribute('data-dir', 'rtl')
  await expect(stage).toHaveAttribute('data-flow', 'row-reverse')
  const after = await frames.evaluateAll((els) =>
    els.map((el) => ({ n: Number(el.getAttribute('data-page')), x: el.getBoundingClientRect().x })),
  )

  // `evaluateAll` returns the elements in document order, so these arrays *are*
  // the DOM order — see the header for what that adds over the geometry and
  // what it deliberately does not.
  const ascending = [start, start + 1]
  expect(
    before.map((f) => f.n),
    'L→R: the frames stand in the document in ascending page order',
  ).toEqual(ascending)
  expect(
    after.map((f) => f.n),
    'R→L: the DOM order is unchanged — row-reverse is what moves page n right',
  ).toEqual(ascending)

  expect(
    before.find((f) => f.n === start)?.x ?? 0,
    'L→R puts page n on the left (ui-spec §6.2)',
  ).toBeLessThan(before.find((f) => f.n === start + 1)?.x ?? 0)
  expect(
    after.find((f) => f.n === start)?.x ?? 0,
    'R→L puts page n on the right (ui-spec §6.2, the rule the spec calls the easiest to get wrong)',
  ).toBeGreaterThan(after.find((f) => f.n === start + 1)?.x ?? 0)

  // Restore: `reading_direction` and `display_mode` are per-book server state
  // (arch §7.6) and `playwright.config.ts` runs one worker, so leaving them
  // flipped would change what the next viewport project opens — the same reason
  // 6.6 restores them.
  await setViewerSeg(page, '읽기 방향', 'ltr')
  await setViewerSeg(page, '표시 모드', 'single')
  await closeViewerSheet(page)

  await page.keyboard.press('Escape')
  await expect(viewer(page)).toHaveCount(0)

  // …and the progress row goes with them, which is the part that has no
  // precedent in this file. 군계's, 미생's and 배틀로얄's rows are left behind on
  // purpose — 6.7's whole assertion is reading 6.6's back — but 자살도's is state
  // this test invented and nobody inherits: `SERIES.suicide` occurs in shelf.ts
  // and in this file and nowhere else in web/e2e. Left there it would draw a
  // progress bar on the 자살도 card (`SeriesCard`/`SeriesRow` derive one from
  // `series.progress.percent`) and add a 자살도 entry to the 이어보기 shelf and to
  // the palette's recents — step-06-1, step-06-2 and step-06-4a among the §7.4
  // shots that would then differ between viewport projects, since 01-library and
  // 02-palette run *before* this file inside each project and desktop-1440 runs
  // first of the four. That is a diff with no product behind it.
  // `DELETE /api/books/{bid}/progress` is FR-VWR-012's own 안읽음 endpoint: 204,
  // and idempotent on a book that has none — `userdata.DeleteProgress` says so
  // in as many words and `userdata_test.go` asserts it on an unknown book.
  //
  // Polled rather than fired once. The client's page write is debounced 1 s and
  // flushed when the viewer unmounts (`useSaveProgress`), so a PUT can still be
  // in flight as this runs; a single DELETE could lose that race and put the row
  // straight back. The loop *is* the assertion — it ends only when the server
  // answers that the row is gone, and a DELETE that stops returning 204 fails on
  // the spot instead of being retried away.
  await expect
    .poll(
      async () => {
        const deleted = await page.request.delete(`/api/books/${bid}/progress`)
        expect(deleted.status(), 'FR-VWR-012 안읽음: DELETE …/progress answers 204').toBe(204)
        const body = (await (await page.request.get(`/api/books/${bid}`)).json()) as {
          progress: { last_page: number } | null
        }
        return body.progress
      },
      {
        timeout: 15_000,
        message: 'shelf.ts rule 2: 6.6b must leave 자살도 without the progress row it invented',
      },
    )
    .toBeNull()
})
