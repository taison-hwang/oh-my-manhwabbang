/**
 * impl-plan §6.3 step 6, assertions 8 and 9.
 *
 *   6.8  the 미생 PDF series opens in the *same* viewer and its pages arrive as
 *        `image/jpeg` (AC-004, FR-SRV-006) — and, because **미생's** MediaBox is
 *        portrait on every page it has, this is also where "R→L puts page n on
 *        the right" is proved on the pdfium path: two decoded frames, ascending
 *        in the DOM, page n to the right of n+1 (impl-plan WP-11 acceptance 4).
 *        Measured: 306/306 portrait in the real 미생 1권, modal MediaBox
 *        378.48 × 548.40 pt (poppler `pdfinfo` over the file as it sits on the
 *        volume); 595 × 842 on all four pages of the synthetic twin
 *        (scripts/mkfixture/main.go:331, D-49). That is a property of this PDF
 *        and not of PDFs: a landscape MediaBox is exactly how a scanned
 *        two-page spread is often stored, which is the FR-VWR-004 case 군계
 *        stands for. 04-viewer 6.6b proves the same rule on the raster path.
 *   6.9  a jump to page 1 400 of the 1 540-page archive loads (AC-008) and the
 *        thumbnail strip is virtualised rather than 1 540 cells deep (D-9)
 */

import type { Page } from '@playwright/test'

import {
  booksOf,
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
  toggleStrip,
  viewer,
  wakeChrome,
  waitForPage,
} from './shelf'

/** §6.3 step 6.9's target page, clamped for the synthetic twin. */
const JUMP_TARGET = 1400
/**
 * Aiming at the page slider.
 *
 * A native `<input type="range">` — which is exactly what ui-spec §6.7 item 2
 * prescribes — keeps the thumb inside its own box, so the value under a pointer
 * is read off the **thumb centre's travel**, not the border box: `width − thumb`,
 * inset half a thumb at each end. Aiming with the border box asks the control
 * for a position it cannot occupy, and the miss is worst at the ends and worse
 * the narrower the slider.
 *
 * This file used to carry the thumb width as a pair of constants —
 * `SLIDER_THUMB_PX = 12`, `SLIDER_THUMB_TOUCH_PX = 16` — copied out of
 * `base.css`. **Ruling E-32 grew the thumb to 18px (26 below the touch
 * breakpoint) and the copy stayed at 12.** The constants did not fail; they went
 * stale, and every drag landed ~3px off, which is §6.5 exactly: the check
 * restated the thing it was supposed to be checking. A number that has to agree
 * with a stylesheet will eventually disagree with it, silently, and only on the
 * projects where the tolerance happens to be tight enough to notice — here,
 * laptop-1024 and tablet-768, while desktop-1440 stayed green.
 *
 * So nothing is restated any more. `calibrateSlider` probes the shipped control
 * at two interior points and fits the straight line through them; whatever the
 * thumb is, that line is what the slider actually does. Reading the thumb
 * directly is not an option — Chrome answers
 * `getComputedStyle(el, '::-webkit-slider-thumb')` with the *input's* box
 * (measured: 130×44 for a 130px-wide input), not the thumb's.
 */
interface SliderMapping {
  /** x of the first probe, and the page it committed. */
  originX: number
  originPage: number
  /** Pages per CSS pixel along the track, measured. */
  pagesPerPx: number
  /** The thumb centre's travel, derived — what `jumpTolerance` is a function of. */
  trackPx: number
  y: number
}

/** Press and release at one point, and report the page the control committed. */
async function commitAt(page: Page, x: number, y: number): Promise<number> {
  await page.mouse.move(x, y)
  await page.mouse.down()
  await page.mouse.move(x, y)
  await page.mouse.up()
  return currentPage(page)
}

async function sliderBox(page: Page): Promise<{ x: number; y: number; width: number; height: number }> {
  // The bars are never unmounted — they fade to `pointer-events:none`. A drag
  // aimed at a faded bar would land on the stage's tap zone and turn a page
  // instead, so the visible state is a precondition, not a nicety.
  await wakeChrome(page)
  await expect(page.locator('[data-role="viewer-bottom-bar"]')).toHaveAttribute(
    'data-visible',
    'true',
  )
  const slider = page.locator('[data-role="page-slider"] input[type="range"]')
  const box = await slider.boundingBox()
  expect(box, 'the page slider must be laid out before it can be dragged').not.toBeNull()
  return {
    x: box?.x ?? 0,
    y: (box?.y ?? 0) + (box?.height ?? 0) / 2,
    width: box?.width ?? 0,
    height: box?.height ?? 0,
  }
}

/**
 * Measure the control's own pixel→page mapping from two interior probes.
 *
 * 25 % and 75 % are chosen because both are inside the thumb's travel at every
 * project width, so neither saturates against an end stop — a probe that
 * saturates would report a slope of zero and make every later aim infinite.
 * That is asserted rather than assumed.
 */
async function calibrateSlider(page: Page, total: number): Promise<SliderMapping> {
  const box = await sliderBox(page)
  const x1 = box.x + box.width * 0.25
  const x2 = box.x + box.width * 0.75
  const p1 = await commitAt(page, x1, box.y)
  const p2 = await commitAt(page, x2, box.y)
  expect(
    p2 - p1,
    'the two calibration probes must land on different pages — a slope of zero means a probe saturated against an end stop',
  ).toBeGreaterThan(0)
  const pagesPerPx = (p2 - p1) / (x2 - x1)
  return {
    originX: x1,
    originPage: p1,
    pagesPerPx,
    trackPx: (total - 1) / pagesPerPx,
    y: box.y,
  }
}
/**
 * §6.3 step 6.9's virtualisation budget. Counted in **cells**, which is the
 * number the requirement is about: un-virtualised, the strip of the 1 540-page
 * archive would mount 1 540 of them and request 1 540 lazily generated
 * thumbnails at once — the stall AC-008 forbids.
 */
const STRIP_CELL_BUDGET = 120

/**
 * How far a landing may sit from its target, **in pages**, on a `trackPx`-long
 * track. This replaces the fixed `JUMP_TOLERANCE = 4` this test used to carry.
 *
 * No document asks the slider to land on an exact page. impl-plan §6.3 step 6.9
 * is "drag the slider to page 1 400 → the page loads (AC-008)"; prd AC-008 is
 * `페이지 임의 점프 응답이 지연되지 않는다`, i.e. a jump must not be *slow*;
 * ui-spec §6.7 fixes the control's markup and its drag preview but states no
 * precision; and there is no E-ruling on the subject. The 4 was picked to match
 * a comment — "one slider pixel is ~1.5 pages" — that only holds near 1 000px
 * of track, so it was never a requirement and it was never checked against the
 * three narrower projects.
 *
 * What actually bounds a landing is geometry: a pointer can only address whole
 * pixels of a `trackPx`-long track, so one pixel is worth `(total − 1) / trackPx`
 * pages — measured here, 1.2 pages at 1440 and 9.6 at 400. **No single page
 * count can be right at both**: 4 is loose at 1440 and physically unreachable at
 * 400, where the finest jump the control can express is already ~10 pages. One
 * track pixel, plus one page for `step=1` rounding the value, is the whole
 * budget; it works out to 3 · 4 · 4 · 11 pages across the four projects, which
 * is *tighter* than the old constant everywhere it was satisfiable at all.
 */
function jumpTolerance(trackPx: number, total: number): number {
  return Math.ceil((total - 1) / trackPx) + 1
}

/**
 * Drag the page slider to `target` through a mapping `calibrateSlider` measured,
 * and report the page it committed on.
 *
 * The aim is `originX + (target − originPage) / pagesPerPx` — the line through
 * the two probes, so the thumb inset is already in it and nothing here has to
 * know what the thumb is. Everything else is the product's own contract: the
 * page is committed on pointer-**up** only (`PageSlider`), so the counter cannot
 * be read before then.
 */
async function dragSliderTo(page: Page, map: SliderMapping, target: number): Promise<number> {
  // The bars are never unmounted — they fade to `pointer-events:none`. A drag
  // aimed at a faded bar would land on the stage's tap zone and turn a page
  // instead, so the visible state is a precondition, not a nicety. The chrome
  // auto-hides on a timer, so this is re-established per drag rather than once.
  const box = await sliderBox(page)

  const x = map.originX + (target - map.originPage) / map.pagesPerPx
  expect(
    x,
    'the aim must land inside the control that was calibrated — a target outside it is a bug in the maths, not a miss',
  ).toBeGreaterThanOrEqual(box.x - 1)
  expect(x).toBeLessThanOrEqual(box.x + box.width + 1)

  const landed = await commitAt(page, x, map.y)
  // The other half of ui-spec §6.7, and the half no tolerance can cover: the
  // control must commit the page it is displaying. A commit path that shipped
  // some other number would still satisfy the geometry above.
  const slider = page.locator('[data-role="page-slider"] input[type="range"]')
  expect(Number(await slider.inputValue()), 'the slider commits the page it shows').toBe(landed)
  return landed
}

test.beforeEach(async ({ page }) => {
  await resetLibraryState(page)
})

test('6.8 · a PDF volume renders image/jpeg in the same viewer, and R→L flips the spread', async ({
  page,
}, info) => {
  const sid = await seriesId(page, SERIES.misaeng)
  const books = await booksOf(page, sid)
  const book = books.find((candidate) => candidate.status === 'ok')
  expect(book, '§6.3 row 8: nine PDFs').toBeDefined()
  const bid = book?.id ?? ''
  expect(book?.kind, 'AC-004 is about a real PDF series').toBe('pdf')

  await resetBookPrefs(page, bid)
  // Open mid-book: page 1 of a manga is a cover, and 양면 needs a facing pair.
  const start = Math.max(1, Math.min(20, (book?.page_count ?? 1) - 1))
  await page.request.put(`/api/books/${bid}/progress`, { data: { page: start } })

  await gotoLibrary(page)
  await setView(page, 'grid')
  await openSeries(page, SERIES.misaeng)

  // FR-SRV-006: pdfium renders the page and the wire type is a JPEG, so the
  // viewer needs no PDF-specific code path at all — which is what AC-004 says.
  const pageResponse = page.waitForResponse(
    (response) => /\/api\/books\/[^/]+\/pages\/\d+/.test(response.url()),
    { timeout: 60_000 },
  )
  await page.locator(`[data-testid="volume-grid"] [title="${book?.name ?? ''}"]`).click()
  const response = await pageResponse
  expect(response.status()).toBe(200)
  expect(response.headers()['content-type']).toContain('image/jpeg')

  await expect(viewer(page)).toBeVisible()
  await waitForPage(page)
  expect(await currentPage(page)).toBe(start)

  // The decoded bitmap, not just a 200: a broken render would still be a 200.
  const natural = await page
    .locator('[data-role="page-frame"][data-status="ready"] img[data-role="page"]')
    .first()
    .evaluate((img) => (img as HTMLImageElement).naturalWidth)
  expect(natural).toBeGreaterThan(0)

  await shot(page, info, 'step-06-8a-viewer-pdf')

  // ---- the rendered half of §6.3 step 6.6's "R→L puts page n on the right".
  // 미생's pages are portrait, so FR-VWR-004's auto-single does not fire and the
  // spread really is two frames — the case 군계's landscape 2-page scans cannot
  // exercise. The premise is this book's and not the format's (see the header
  // for the measurement), and unlike 04-viewer 6.6b this test does not re-derive
  // it from the API at runtime — so a failure of the count below is to be read
  // as "미생 stopped being portrait" before it is read as "FR-VWR-004 broke".
  // Both halves of impl-plan WP-11 acceptance 4 are asserted below: where the
  // frames sit on screen, and what order they stand in the document.
  await setViewerSeg(page, '표시 모드', 'spread')
  const stage = page.locator('[data-role="stage"]')
  await expect(stage).toHaveAttribute('data-flow', 'row')
  const frames = page.locator('[data-role="page-frame"]')
  // A precondition, not a guard. `start` was derived from `page_count` above so
  // the facing page exists in both modes, and a portrait page *must* pair — if
  // it ever stops, FR-VWR-004 is broken and this test has to say so. An
  // `if ((await frames.count()) === 2)` used to stand here, and a condition that
  // can quietly go false takes the whole geometric proof of the RTL rule with it
  // — on this path, the only one there is (HANDOFF §6.5).
  await expect(
    frames,
    "양면 on 미생's portrait pages is two frames (FR-VWR-004)",
  ).toHaveCount(2)
  // Both frames also have to have *decoded* before a bounding box means
  // anything. A frame that is still `loading` renders its `<img>` absolutely
  // positioned at `opacity: 0` (PageFrame), so it is a zero-width, invisible
  // box — and it still sits at exactly the x the comparisons below read. That
  // is not hypothetical: in the 2026-07-29 04:47 desktop-1440 round page
  // `start + 1` had not come back from pdfium yet, and the
  // `step-06-8b-…-desktop-1440.png` that round wrote holds a single page whose
  // centre is x = 721 in a 1440 stage — one pixel right of the stage's own
  // centre, which is the half of the 2 px gap that proves the second frame was
  // mounted and empty. Every assertion below passed against a page that never
  // painted, and the reviewed screenshot of the spread had no spread in it. The
  // next clean run regenerates that file: if it still shows one page, this
  // precondition did not do its job and the run should be rejected, not the
  // precondition.
  await expect(
    page.locator('[data-role="page-frame"][data-status="ready"]'),
    'both pages of the spread must decode before their boxes can be compared',
  ).toHaveCount(2, { timeout: 30_000 })

  const before = await frames.evaluateAll((els) =>
    els.map((el) => ({ n: Number(el.getAttribute('data-page')), x: el.getBoundingClientRect().x })),
  )
  await setViewerSeg(page, '읽기 방향', 'rtl')
  await expect(stage).toHaveAttribute('data-flow', 'row-reverse')
  const after = await frames.evaluateAll((els) =>
    els.map((el) => ({ n: Number(el.getAttribute('data-page')), x: el.getBoundingClientRect().x })),
  )

  // impl-plan.md:638 (WP-11 acceptance 4) asks for this rule to be "asserted by
  // a DOM-order test **and** an E2E screenshot", and until now the DOM-order half
  // existed only in jsdom (ViewerPage.test.tsx:342). `evaluateAll` hands the
  // elements back in document order, so these two arrays *are* the DOM order.
  //
  // Stated rather than inferred, deliberately. With exactly two frames and
  // nothing but `flex-direction` steering them, `data-flow` and the x geometry
  // below already pin this: a reversed array under `row-reverse` moves page n to
  // the *left* and fails the x check, and reversing the array while flipping the
  // flow back to `row` — the cancellation fit.ts:8-13 warns about — fails the
  // `data-flow` check instead. What that inference rests on is the assumption
  // that flex direction is the only thing ordering the stage, which is exactly
  // the assumption an `order:`, a `direction: rtl` or a future grid would break.
  // This line keeps saying what the document must contain when it does, and it
  // is the order a screen reader, a text selection and a copy follow whatever
  // the CSS says.
  const ascending = [start, start + 1]
  expect(
    before.map((f) => f.n),
    'L→R: the frames stand in the document in ascending page order',
  ).toEqual(ascending)
  expect(
    after.map((f) => f.n),
    'R→L: the DOM order is unchanged — row-reverse is what moves page n right',
  ).toEqual(ascending)

  const leftFirst = before.find((f) => f.n === start)
  const rightFirst = after.find((f) => f.n === start)
  expect(leftFirst?.x ?? 0, 'L→R puts page n on the left').toBeLessThan(
    before.find((f) => f.n === start + 1)?.x ?? 0,
  )
  expect(rightFirst?.x ?? 0, 'R→L puts page n on the right').toBeGreaterThan(
    after.find((f) => f.n === start + 1)?.x ?? 0,
  )
  await shot(page, info, 'step-06-8b-viewer-pdf-rtl-spread')
  await setViewerSeg(page, '읽기 방향', 'ltr')
  await setViewerSeg(page, '표시 모드', 'single')

  await page.keyboard.press('Escape')
  await expect(viewer(page)).toHaveCount(0)
})

/**
 * §6.3 row 9's archive is still 1.34 GB and still holds 1 540 pages, but since
 * D-73 it is the **fifteen volumes** its directories say it is rather than one
 * book of 1 540 pages. The jump this step is about — an arbitrary page out of a
 * 1.34 GB deflate stream — is therefore made in the *last* volume, whose pages
 * sit at the far end of the file.
 *
 * One claim did lose its subject here and is not quietly kept: the strip's
 * "must not mount 1 540 cells" is vacuous against a ~100-page volume, because
 * the curated subset no longer contains a book with more pages than the cell
 * budget. `이누야샤 01~56권 완결/이누야샤(1~56권)(완).zip` — 7 480 pages, 1.40 GB,
 * measured flat — is the replacement subject if the subset is ever re-based;
 * see the handoff.
 */
test('6.9 · a jump into the 1 540-page archive loads, and the strip is virtualised', async ({
  page,
}, info) => {
  const sid = await seriesId(page, SERIES.battleRoyale)
  const books = await booksOf(page, sid)
  const book = books[books.length - 1]
  expect(book, '§6.3 row 9: one 1.34 GB ZIP').toBeDefined()
  const bid = book?.id ?? ''
  const total = book?.page_count ?? 0
  expect(total, 'D-73: the last of the archive’s volumes').toBeGreaterThan(20)

  await resetBookPrefs(page, bid)
  await page.request.put(`/api/books/${bid}/progress`, { data: { page: 1 } })

  await gotoLibrary(page)
  await setView(page, 'grid')
  await openSeries(page, SERIES.battleRoyale)
  await page.locator(`[data-testid="volume-grid"] [title="${book?.name ?? ''}"]`).click()
  await expect(viewer(page)).toBeVisible()
  await waitForPage(page)
  expect(await pageCount(page)).toBe(total)

  // ---- drag the slider (ui-spec §6.7) ------------------------------------
  const target = Math.min(JUMP_TARGET, total - 1)

  // §6.3 asks for page 1 400, but a pixel→page mapping that is right at one
  // fraction and wrong at another is not a mapping. The same drag is therefore
  // made at a tenth of the book and at its mid-point first — the mid-point is
  // where the thumb inset contributes exactly nothing, so a miss there is a
  // scale error rather than an aiming one, and the tenth and 1 400 sit on
  // opposite sides of it.
  const map = await calibrateSlider(page, total)
  let landed = 0
  for (const probe of [Math.round(total * 0.1), Math.round(total * 0.5), target]) {
    landed = await dragSliderTo(page, map, probe)
    const tolerance = jumpTolerance(map.trackPx, total)
    expect(
      Math.abs(landed - probe),
      `the drag should land on ~${String(probe)}; ±${String(tolerance)} page(s) is one pixel of this viewport's measured ${String(Math.round(map.trackPx))}px track plus step=1`,
    ).toBeLessThanOrEqual(tolerance)
  }
  // `target` is the last probe, so `landed` below is §6.3 step 6.9's own jump.

  // AC-008: an arbitrary jump into a 1.34 GB deflate stream is not slow, and
  // "not slow" here means the page actually decodes rather than spinning.
  await expect(
    page.locator(`[data-role="page-frame"][data-page="${String(landed)}"][data-status="ready"]`),
  ).toBeVisible({ timeout: 20_000 })

  await shot(page, info, 'step-06-9a-viewer-1540-jump')

  // ---- FR-VWR-008 / D-9: the strip is windowed ---------------------------
  await toggleStrip(page)
  const strip = page.locator('[data-role="thumbnail-strip"]')
  await expect(strip).toBeVisible()
  const cells = strip.locator('[data-role="thumb"]')
  await expect(cells.first()).toBeVisible()
  const mounted = await cells.count()
  // Windowing is only *tested* here when the book has more pages than the
  // budget; below that this asserts nothing, and says so rather than passing
  // quietly. The synthetic round's own large book is what carries the claim
  // until the curated subset gains a book bigger than the window again.
  if (total <= STRIP_CELL_BUDGET) {
    info.annotations.push({
      type: 'coverage-gap',
      description: `strip virtualisation is untested here: the volume has ${String(total)} pages, under the ${String(STRIP_CELL_BUDGET)}-cell budget (D-73 split the 1 540-page book)`,
    })
  }
  expect(mounted, 'the strip must not mount every cell of a long book').toBeLessThan(
    STRIP_CELL_BUDGET,
  )
  expect(mounted).toBeGreaterThan(0)
  await expect(strip.locator('[data-role="thumb"][data-current="true"]')).toHaveAttribute(
    'data-page',
    String(landed),
  )

  await shot(page, info, 'step-06-9b-viewer-1540-strip')

  await page.keyboard.press('Escape')
  await expect(viewer(page)).toHaveCount(0)
})
