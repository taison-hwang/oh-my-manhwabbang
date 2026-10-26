/**
 * The viewer's **display model** — rulings E-27 and E-28, at the browser tier.
 *
 * Everything the two rulings added lived in unit tests only until this file:
 * the opening hint, the quiet page counter, the 44px screen-edge strips, the
 * `H` key, the bars' hover-hold, the auto-hide itself, and the layer order that
 * keeps the chrome above the end-of-volume scrim. Ruling E-27's central claim —
 * *reading never summons the interface* — is a claim about what a real browser
 * does with a real click, and three of the surfaces below (the strips, the
 * hover-hold, the scrim) are **hit-testing** properties that jsdom answers
 * `true` to by construction (HANDOFF §6.5, the `jsdom + css:false` row).
 *
 * Why a file of its own rather than more of 04-viewer:
 *
 *  * 04-viewer's two halves are `serial` around one book's persisted progress,
 *    and nothing here should be able to skip that chain or be skipped by it;
 *  * every helper in `shelf.ts` that touches a viewer control calls
 *    `wakeChrome`, which parks the pointer **on the bar** so that the caller is
 *    not racing a 2.6 s timer. That is the right default and this file does not
 *    change it — it uses `standBackFromChrome`, the other half added with this
 *    file, wherever the chrome has to be watched going away.
 *
 * ## The two books
 *
 * 바퀴 (a single top-level ZIP, prd §2.2 row 4) carries the three chromeless
 * tests: it is one book, so no volume-end card can appear behind an assertion
 * about the chrome, and no other spec in this directory touches it. 상처를
 * 쫓는자 (a folder of volume folders, row 2) carries the volume-end test for the
 * same reason. Both leave the server as they found it — `clearProgress` and
 * `resetBookPrefs` on the way out, shelf.ts rule 2.
 *
 * ## The three constants copied out of `web/src`
 *
 * `CHROME_AUTOHIDE_MS`, `CHROME_HINT_MS` and `EDGE_STRIP_PX` are copies on the
 * same terms as `SERIES` in shelf.ts. Two of them are used only as **floors** —
 * "still up after longer than the auto-hide", "gone within several times the
 * hint" — so a product that lengthened either would not redden this file for
 * the wrong reason; it would only weaken the test, and the unit tier pins the
 * exact numbers. The hint *sentence* is pinned exactly, because E-28 states it
 * verbatim and a viewer that opens with nothing on it is only defensible while
 * that line is on screen.
 */

import type { Locator, Page } from '@playwright/test'

import {
  booksOf,
  boxContains,
  clearProgress,
  currentPage,
  EDGE_STRIP_PX,
  expect,
  openViewerDirect,
  pageCount,
  quietPageCounter,
  resetBookPrefs,
  resetLibraryState,
  SERIES,
  seriesId,
  setViewerSeg,
  shot,
  standBackFromChrome,
  test,
  viewer,
  viewerBottomBar,
  viewerCounterText,
  viewerEdge,
  viewerTopBar,
  wakeChrome,
  waitForPage,
  waitForProgressWrite,
} from './shelf'

/** `store/viewer.ts` — the chrome goes this long after the last wake (E-27). */
const CHROME_AUTOHIDE_MS = 2600
/** `store/viewer.ts` — how long the opening hint stays up (E-27). */
const CHROME_HINT_MS = 3400
/** `ViewerPage.tsx`'s `CHROME_HINT`: the one sentence E-28 fixes verbatim. */
const CHROME_HINT = '좌·우 클릭으로 페이지 · 중앙 클릭 또는 상하 가장자리로 컨트롤'

/**
 * How much longer than the auto-hide a held chrome is watched for.
 *
 * The assertion it serves is that nothing happened, so the number only has to
 * be comfortably past the deadline it is proving was not honoured.
 */
const HOLD_SLACK_MS = 900

/**
 * The floor the hint's life is held against.
 *
 * The measurement can only **under**-count: the clock starts when Playwright
 * first observed the toast, which is already some way past the `open()` that
 * started the 3 400 ms timer. So this is not `CHROME_HINT_MS` minus a
 * tolerance — it is the answer to "was the hint on screen long enough to be
 * read, or did it merely flash", and a hint wired to 0 ms fails it.
 */
const HINT_FLOOR_MS = 1500

/** Where the `data-chrome` transition log lives while `watchChrome` is running. */
const FLIPS_KEY = '__shelfChromeFlips'

/**
 * Starts recording every `data-chrome` transition on the viewer root.
 *
 * Sampling `data-chrome` cannot express "the chrome was never summoned", and
 * this file learned it twice over — both times as HANDOFF §6.5, in a test
 * written to close §6.5:
 *
 *  * `toHaveAttribute('data-chrome', 'visible')` after a wait is not a proof
 *    that the chrome *held*. Parked where `wakeChrome` leaves the pointer, at
 *    desktop-1440 and laptop-1024 the chrome hid on the deadline and was
 *    summoned again 13 ms later by the top edge strip re-mounting underneath
 *    that same pointer — an oscillation that answers "visible" to every read
 *    that does not land inside the 13 ms.
 *  * `toHaveAttribute('data-chrome', 'hidden')` after an action is not a proof
 *    that the action did not summon it, because **`expect` retries**: a chrome
 *    that woke on the page turn goes back down 2 600 ms later on its own and
 *    the retry accepts it. Measured, not reasoned — routing `goNext`/`goPrev`
 *    back through `goTo` left every assertion in this file green and only made
 *    the turning test three times slower.
 *
 * Both claims are about a *transition list* being empty, so that is what is
 * recorded and asserted. A run in which nothing happened has nothing to wait
 * for and nothing to retry into.
 */
async function watchChrome(page: Page): Promise<void> {
  await page.evaluate((key) => {
    const win = window as unknown as Record<string, string[]>
    win[key] = []
    const root = document.querySelector('[data-role="viewer"]')
    if (root === null) throw new Error('watchChrome: there is no viewer to watch')
    new MutationObserver(() => {
      win[key]?.push(root.getAttribute('data-chrome') ?? '')
    }).observe(root, { attributes: true, attributeFilter: ['data-chrome'] })
  }, FLIPS_KEY)
}

/** Every `data-chrome` value the viewer has taken since `watchChrome`. */
async function chromeFlips(page: Page): Promise<string[]> {
  return page.evaluate(
    (key) => (window as unknown as Record<string, string[]>)[key] ?? [],
    FLIPS_KEY,
  )
}

/** The one-ZIP series: one book, and no other spec in this directory opens it. */
async function wheelBook(page: Page): Promise<{ sid: string; bid: string; total: number }> {
  const sid = await seriesId(page, SERIES.wheel)
  const books = await booksOf(page, sid)
  const book = books.find((candidate) => candidate.status === 'ok')
  expect(book, '§6.3 row 4: 바퀴.zip is a top-level ZIP that is its own book').toBeDefined()
  return { sid, bid: book?.id ?? '', total: book?.page_count ?? 0 }
}

test.beforeEach(async ({ page }) => {
  await resetLibraryState(page)
})

/**
 * E-27's first row — **열 때 크롬 없음** — the auto-hide that follows every wake,
 * and the hover-hold that suspends it.
 *
 * The auto-hide had no browser-tier coverage at all before this test, and not by
 * oversight: `wakeChrome` parks the pointer on the bar so that its callers are
 * not racing a 2.6 s timer, and that is the one place from which the timer can
 * never be watched. `standBackFromChrome` is the half added with this file.
 *
 * **The hold is asserted along the path a reader takes to a control**: pointer on
 * the page, chrome summoned, pointer moved *into* the bar. That crossing is what
 * dispatches the bar's `mouseenter`, and it is not the same as the bar arriving
 * underneath a pointer that has not moved — see the note in the report that came
 * with this file for the difference, which is a product finding and not a
 * property this test is entitled to assert either way.
 */
test('E-27 · the chrome opens away, hides itself again, and holds under a pointer in the bar', async ({
  page,
}, info) => {
  const { sid, bid, total } = await wheelBook(page)
  expect(total, 'a book with pages is the premise of every viewer assertion').toBeGreaterThan(1)
  await resetBookPrefs(page, bid)

  await openViewerDirect(page, sid, bid, 1)
  await waitForPage(page)

  // ---- E-27: the first frame of a book is the page ------------------------
  // Before the ruling it was three rows of controls over the page, which is
  // design.md principle 2 broken at the one moment it matters most.
  await expect(viewer(page), 'E-27: a book opens with no chrome on it').toHaveAttribute(
    'data-chrome',
    'hidden',
  )
  await expect(viewerTopBar(page)).toHaveAttribute('data-visible', 'false')
  await expect(viewerBottomBar(page)).toHaveAttribute('data-visible', 'false')
  await expect(
    viewerEdge(page, 'top'),
    'the screen-edge strips exist *because* the chrome is away — they are how it is called back',
  ).toHaveCount(1)
  await expect(viewerEdge(page, 'bottom')).toHaveCount(1)
  await shot(page, info, 'e27-viewer-chromeless')

  // ---- E-27: 마우스 이동 → 아무 일도 하지 않는다 --------------------------
  const zones = page.locator('[data-role="stage-zones"]')
  const box = await zones.boundingBox()
  expect(box, 'the stage must be laid out before the pointer can be moved across it').not.toBeNull()
  const midY = (box?.y ?? 0) + (box?.height ?? 0) / 2
  await watchChrome(page)
  for (const fraction of [0.3, 0.5, 0.7]) {
    await page.mouse.move((box?.x ?? 0) + (box?.width ?? 0) * fraction, midY)
  }
  // A sleep, and the only kind this file allows: it is proving that something
  // has **not** happened. A wake is a synchronous store write on the mousemove
  // and React commits it in the same frame, so 250 ms is already slack — what it
  // buys is that a wake which did happen has certainly landed before the read.
  await page.waitForTimeout(250)
  expect(
    await chromeFlips(page),
    'E-27: moving the mouse over the page must not raise three rows of controls over it',
  ).toEqual([])
  await expect(viewer(page)).toHaveAttribute('data-chrome', 'hidden')

  // ---- the top screen edge is one of the three things that do -------------
  // `wakeChrome` reaches into the top 44px strip and then parks on the bar.
  await wakeChrome(page)
  await expect(viewer(page)).toHaveAttribute('data-chrome', 'visible')
  await expect(
    viewerEdge(page, 'top'),
    'with the bars up the strips are gone: a strip over the top bar would eat the first click on 뒤로',
  ).toHaveCount(0)
  await expect(viewerEdge(page, 'bottom')).toHaveCount(0)

  // ---- the auto-hide (E-27: 2 600 ms after the last wake) -----------------
  await standBackFromChrome(page)
  await expect(
    viewer(page),
    'leaving a bar re-arms the auto-hide; it does not fire it',
  ).toHaveAttribute('data-chrome', 'visible')
  await expect(
    viewer(page),
    `E-27: ${String(CHROME_AUTOHIDE_MS)}ms after the last wake, with the pointer off the chrome, the chrome goes on its own`,
  ).toHaveAttribute('data-chrome', 'hidden')
  await expect(
    viewerEdge(page, 'top'),
    'and the way back in comes back with it',
  ).toHaveCount(1)
  await expect(viewerEdge(page, 'bottom')).toHaveCount(1)

  // ---- 바 위 호버 → 자동숨김 보류 (E-27) ----------------------------------
  // Summoned with `H` so that the pointer is still on the page, and then walked
  // into the bar — which is the reader's own path to a control, and the crossing
  // is what makes the bar's `mouseenter` a real event rather than a bar
  // appearing under a pointer that never moved.
  await page.keyboard.press('h')
  await expect(viewer(page)).toHaveAttribute('data-chrome', 'visible')
  const bar = await viewerTopBar(page).boundingBox()
  expect(bar, 'the top bar must be laid out before the pointer can be walked into it').not.toBeNull()
  const parkX = (bar?.x ?? 0) + (bar?.width ?? 0) / 2
  const parkY = (bar?.y ?? 0) + (bar?.height ?? 0) - 4
  // Deeper than a strip, deliberately. Park inside the top 44px and a chrome
  // that *did* hide would be summoned straight back by the strip re-mounting
  // under the pointer, and the hold would look honoured while nothing held it.
  expect(
    parkY - (bar?.y ?? 0),
    `the parking spot has to be deeper than the ${String(EDGE_STRIP_PX)}px edge strip, or a chrome that hid would be woken again from under the pointer`,
  ).toBeGreaterThan(EDGE_STRIP_PX)

  await watchChrome(page)
  await page.mouse.move(parkX, parkY)
  const parked = await page.evaluate(
    ({ px, py }) => {
      const element = document.elementFromPoint(px, py)
      const barElement = document.querySelector('[data-role="viewer-top-bar"]')
      return barElement?.contains(element) === true
    },
    { px: parkX, py: parkY },
  )
  expect(parked, 'the pointer has to actually be inside the bar for a hover-hold to mean anything').toBe(true)

  // The wait is the assertion's whole content: the bars must still be there
  // after the deadline they would otherwise have met.
  await page.waitForTimeout(CHROME_AUTOHIDE_MS + HOLD_SLACK_MS)
  expect(
    await chromeFlips(page),
    `E-27 hover-hold: a pointer resting in the chrome pins it open — the reader is looking at the control they are about to press. Nothing may happen to it in ${String(CHROME_AUTOHIDE_MS + HOLD_SLACK_MS)}ms, which is past the ${String(CHROME_AUTOHIDE_MS)}ms auto-hide`,
  ).toEqual([])
  await expect(viewer(page)).toHaveAttribute('data-chrome', 'visible')

  await clearProgress(page, bid)
})

/**
 * The other half of the hover-hold, and the half that was **not** honoured.
 *
 * The test above walks the pointer *into* a bar, which is the reader's own path
 * to a control and the one the crossing makes an event out of. This one is the
 * path E-27 added in the same ruling: the chrome summoned from a screen-edge
 * strip. The strips exist only while the chrome is away, so a wake unmounts the
 * strip and lights the bar **in the same commit, under a pointer that has not
 * moved** — and no crossing happens at all.
 *
 * The shipped build did not hold there. Measured at all four widths: Chrome does
 * re-hit-test after the layout change and does dispatch `pointerover` on the bar
 * ~10 ms later, but React *synthesises* `onMouseEnter` out of `mouseover` and
 * drops the pair when the `relatedTarget` is a node it manages — which is every
 * crossing caused by the layout moving rather than by the pointer. So the hold
 * was never taken, and 2 600 ms later either the chrome dissolved with the
 * pointer sitting in the bar (the exact state E-27 exists to prevent), or — with
 * the pointer resting inside the 44 px the strip re-occupies — the strip
 * re-mounted underneath it, summoned the chrome again 13 ms later, and the bars
 * blinked every 2.6 s for as long as the reader left the mouse alone.
 *
 * Which is why the assertion is on `watchChrome`'s transition list and not on
 * `data-chrome`: the oscillation answers `visible` to every retrying read that
 * does not land inside those 13 ms. A run in which nothing happened has nothing
 * to retry into.
 *
 * **And the release is asserted in the same test, deliberately.** A hold taken
 * without a crossing cannot be given back by the matching crossing, so a fix
 * that pinned the chrome open for the rest of the session would satisfy the
 * first half of every paragraph above. `chromeHeld` is module-scoped and nothing
 * renders from it: a chrome stranded held has no symptom a reader could see and
 * no state they could correct. `standBackFromChrome` is the reader walking away
 * from the bar, and the chrome has to go.
 */
test('E-27 · the chrome woken from a screen edge is held under the pointer that woke it — and let go again', async ({
  page,
}) => {
  const { sid, bid, total } = await wheelBook(page)
  expect(total, 'a book with pages is the premise of every viewer assertion').toBeGreaterThan(1)
  await resetBookPrefs(page, bid)

  await openViewerDirect(page, sid, bid, 1)
  await waitForPage(page)
  await expect(viewer(page)).toHaveAttribute('data-chrome', 'hidden')

  const viewerBox = await viewer(page).boundingBox()
  expect(viewerBox, 'the viewer must be laid out before its edges can be reached for').not.toBeNull()
  const cx = (viewerBox?.x ?? 0) + (viewerBox?.width ?? 0) / 2
  const topInside = (viewerBox?.y ?? 0) + EDGE_STRIP_PX / 2
  const bottomInside = (viewerBox?.y ?? 0) + (viewerBox?.height ?? 0) - EDGE_STRIP_PX / 2

  /** Is the pixel the pointer is parked on inside the given bar? */
  const parkedIn = async (bar: 'viewer-top-bar' | 'viewer-bottom-bar', y: number) =>
    page.evaluate(
      ({ px, py, role }) => {
        const element = document.elementFromPoint(px, py)
        return document.querySelector(`[data-role="${role}"]`)?.contains(element) === true
      },
      { px: cx, py: y, role: bar },
    )

  for (const edge of ['top', 'bottom'] as const) {
    const y = edge === 'top' ? topInside : bottomInside
    const bar = edge === 'top' ? ('viewer-top-bar' as const) : ('viewer-bottom-bar' as const)

    // The pointer starts on the page — Playwright leaves it at (0, 0), which is
    // *inside* the top strip, and a move that never crosses a boundary dispatches
    // nothing. It also has to come from somewhere the chrome cannot be summoned.
    await standBackFromChrome(page)
    await expect(viewer(page)).toHaveAttribute('data-chrome', 'hidden')
    await expect(viewerEdge(page, edge)).toHaveCount(1)

    // ---- reach for the screen edge, and then do nothing at all -------------
    await page.mouse.move(cx, y)
    await expect(
      viewer(page),
      `E-27: the ${edge} edge of the screen is one of the three things that summon the chrome`,
    ).toHaveAttribute('data-chrome', 'visible')
    expect(
      await parkedIn(bar, y),
      `the bar has to have arrived under the pointer, or there is nothing here to hold: the ${edge} strip is ${String(EDGE_STRIP_PX)}px deep and the bar it summons is at least as tall`,
    ).toBe(true)

    // The strips' *click* half is not exercised here, and cannot usefully be: a
    // mouse has to be over a strip before it can press one, and by then the
    // hover has already woken the chrome and unmounted the strip — a press at
    // the same coordinate lands on the bar, on whichever control happens to be
    // under it. The hold does not care which gesture woke the chrome anyway; it
    // is a question about where the pointer *is*. `ViewerPage.test.tsx` drives
    // the click handler directly, on both strips.
    await watchChrome(page)
    // The wait is the assertion's whole content.
    await page.waitForTimeout(CHROME_AUTOHIDE_MS + HOLD_SLACK_MS)
    expect(
      await chromeFlips(page),
      `E-27 hover-hold, from the ${edge} screen edge: the bar arrives under a pointer that never moved, and the reader is looking at the control they are about to press. Nothing may happen to it in ${String(CHROME_AUTOHIDE_MS + HOLD_SLACK_MS)}ms — neither the auto-hide firing, nor the strip re-mounting underneath and summoning it back`,
    ).toEqual([])
    await expect(viewer(page)).toHaveAttribute('data-chrome', 'visible')
    await expect(
      viewerEdge(page, edge),
      'and the strips stay out of the way while it is up: one over the bars would eat the first click on 뒤로',
    ).toHaveCount(0)

    // ---- …and the hold is not a trap --------------------------------------
    // A hold taken without a crossing cannot rely on the matching crossing to
    // give it back. The reader walks away from the bar; the chrome goes.
    await standBackFromChrome(page)
    await expect(
      viewer(page),
      `E-27: a chrome held from the ${edge} edge must still be releasable — a hold nothing can end disarms the auto-hide for the rest of the session, and nothing on screen says so`,
    ).toHaveAttribute('data-chrome', 'hidden')
    await expect(viewerEdge(page, edge)).toHaveCount(1)
  }

  await clearProgress(page, bid)
})

/**
 * E-27's third row: **페이지 넘김 → 깨우지 않는다**, and the counter that stands
 * in for the bar while the reader is reading.
 *
 * This is the ruling's load-bearing sentence and the one the implementation lost
 * once already. `nextPage`/`prevPage` were committed through the store's `goTo`,
 * which wakes; the fix routes them through `turnTo`, which is `goTo` minus that
 * one line. A store test can be green about `turnTo` while the screen calls
 * `goTo` — that is exactly how the defect survived — so the assertion has to be
 * made where the key press and the tap actually are.
 */
test('E-27 · turning pages never summons the chrome, and the quiet counter is the bar’s stand-in', async ({
  page,
}) => {
  const { sid, bid, total } = await wheelBook(page)
  const TURNS = 3
  // 1 → +3 keys → +1 tap = page 5, and the last page raises the next-volume
  // card over everything this test is about. Derived, so the synthetic twin and
  // the real archive are held to the same shape rather than to a page count.
  const LAST_REACHED = TURNS + 2
  expect(
    total,
    `this test reaches page ${String(LAST_REACHED)} and must stay off the last page, where FR-VWR-010's card takes over`,
  ).toBeGreaterThan(LAST_REACHED)
  await resetBookPrefs(page, bid)

  await openViewerDirect(page, sid, bid, 1)
  await waitForPage(page)

  // The quiet counter's render gate names two modes it is suppressed in — 세로
  // scrolls through several pages at once and 너비 leaves the page taller than
  // the viewport, so in both the number on screen is not the number being read.
  // Both are asserted rather than assumed: without them a counter that is
  // present proves nothing about *why*, and the count of 0 further down proves
  // nothing at all.
  const stage = page.locator('[data-role="stage"]')
  await expect(stage).toHaveAttribute('data-mode', 'single')
  await expect(stage).toHaveAttribute('data-fit', 'height')
  await expect(viewer(page)).toHaveAttribute('data-chrome', 'hidden')
  await expect(
    quietPageCounter(page),
    'E-27: with no bar on screen, the page number is the one thing that stays',
  ).toHaveText(viewerCounterText(1, total))

  // Everything from here to the flip assertion below is one uninterrupted act of
  // reading, and what E-27 promises about it is that *nothing happens to the
  // chrome* — not that the chrome is back down by the time anyone looks. The
  // difference is the whole test: a turn routed through `goTo` raises the bars
  // and the auto-hide puts them away again 2.6 s later, which every retrying
  // assertion in this file accepted (see `watchChrome`).
  await watchChrome(page)

  // ---- the keyboard reading path (ui-spec §8.2) ---------------------------
  for (let turn = 1; turn <= TURNS; turn++) {
    await page.keyboard.press('ArrowRight')
    await expect(quietPageCounter(page)).toHaveText(viewerCounterText(1 + turn, total))
  }

  // ---- and the mouse reading path (FR-VWR-011's side zones) ---------------
  const zones = page.locator('[data-role="stage-zones"]')
  const box = await zones.boundingBox()
  expect(box, 'the tap zones must be laid out before they can be aimed at').not.toBeNull()
  const y = (box?.y ?? 0) + (box?.height ?? 0) / 2
  // 0.85 is inside the right-hand 32 % zone at every width (`useTouchZones`),
  // and under L→R the right zone is 'next'.
  const x = (box?.x ?? 0) + (box?.width ?? 0) * 0.85

  // FR-VWR-009 is *not* what E-27 took off the reading path: the page still has
  // to be recorded while the chrome sleeps. Attached before the turn that causes
  // it — `useSaveProgress` debounces by a second, so a listener attached after
  // the turn could be attached after the write it is waiting for.
  const written = waitForProgressWrite(page, bid)
  await page.mouse.click(x, y)
  await expect(quietPageCounter(page)).toHaveText(viewerCounterText(LAST_REACHED, total))
  await written

  expect(
    await chromeFlips(page),
    'E-27: reading never summons the interface — the arrow keys and FR-VWR-011’s side zones commit through the store’s `turnTo`, which is `goTo` minus the wake. `goTo` is the *control* path, where the bar must not vanish under the press',
  ).toEqual([])
  await expect(viewer(page)).toHaveAttribute('data-chrome', 'hidden')

  // ---- with a bar on screen the quiet counter stands down -----------------
  // `H` rather than `wakeChrome`: it leaves the pointer on the page, which keeps
  // the two counters the only things that change between here and the previous
  // assertion. A click on the stage does not move focus off `document.body`, so
  // `useViewerKeys` is still listening (ui-spec §8.2 / `isTypingTarget`).
  await page.keyboard.press('h')
  await expect(viewer(page)).toHaveAttribute('data-chrome', 'visible')
  await expect(
    quietPageCounter(page),
    'the quiet counter is the bar’s stand-in, not a second counter: it goes the moment the bar carries the number',
  ).toHaveCount(0)
  await expect(page.locator('[data-role="page-counter"]')).toHaveText(
    viewerCounterText(LAST_REACHED, total),
  )

  // ---- …and it stays away in 너비, for the other reason --------------------
  // Two different suppressions land on the same `toHaveCount(0)`, and a count
  // cannot tell them apart (HANDOFF §6.5). So the chrome is put back to sleep
  // *first* and asserted asleep, and the fit is asserted 너비 on the stage
  // itself: what is left is a zero that can only be the fit's.
  await setViewerSeg(page, '맞춤', 'width')
  await expect(stage).toHaveAttribute('data-fit', 'width')
  await standBackFromChrome(page)
  await expect(viewer(page)).toHaveAttribute('data-chrome', 'hidden')
  await expect(stage, 'the fit is still 너비 with the chrome away').toHaveAttribute(
    'data-fit',
    'width',
  )
  await expect(
    quietPageCounter(page),
    'E-27: 너비 leaves the page taller than the viewport, so the number on screen is not the number being read — suppressed for the fit, not for the chrome, which is asserted away above',
  ).toHaveCount(0)

  // arch §7.6: `fit_mode` is per-book server state and one worker runs all four
  // viewport projects, so 너비 would otherwise be what the next one opens.
  await resetBookPrefs(page, bid)
  await clearProgress(page, bid)
})

/**
 * The three ways back into a viewer that will not come back on its own: the
 * sentence that says so, the `H` key, and the two screen-edge strips.
 *
 * The hint is the compensation E-27 owes the reader for opening chromeless, so
 * the assertions are about it being *readable*: on screen when the book opens,
 * carrying the ruling's own sentence, announced as a live region, and timed
 * rather than dismissed. The second entry covers the other half — E-28 §3's
 * "answered once" — which is what stops the line coming back at a reader who
 * has already acted on it.
 */
test('E-27 · the opening hint, the `H` key, and the two screen-edge strips', async ({ page }) => {
  const { sid, bid, total } = await wheelBook(page)
  expect(total, 'a book with pages is the premise of every viewer assertion').toBeGreaterThan(1)
  await resetBookPrefs(page, bid)

  // ---- entry 1: the hint arrives, and leaves on its own -------------------
  await openViewerDirect(page, sid, bid, 1)
  await watchChrome(page)
  const hint = page.locator('[data-role="viewer-chrome-hint"]')
  await expect(
    hint,
    'E-27: a viewer that opens with nothing on it owes the reader one line saying where the controls went',
  ).toBeVisible()
  await expect(hint, 'E-28 fixes this sentence verbatim').toHaveText(CHROME_HINT)
  // Announced through the viewer's one live region (ba), not by the box: the
  // box is decoration now and must not be a second region reading it twice.
  const status = page.locator('[data-role="viewer-status"]')
  await expect(status).toHaveAttribute('role', 'status')
  await expect(
    status,
    'and it is announced: a line that only exists for 3.4 s is no use to a reader who cannot see it',
  ).toContainText(CHROME_HINT)
  await expect(hint, 'the visible box is not itself a live region (ba)').toHaveAttribute('aria-hidden', 'true')

  const seenAt = Date.now()
  await expect(
    hint,
    'E-27: the hint is timed, not dismissed — a hint that has to be closed is a second thing to learn',
  ).toHaveCount(0, { timeout: CHROME_HINT_MS * 3 })
  const lived = Date.now() - seenAt
  expect(
    lived,
    `the hint must stay long enough to be read: ${String(CHROME_HINT_MS)}ms from open, and this measures ${String(lived)}ms from first sight, which can only be less`,
  ).toBeGreaterThan(HINT_FLOOR_MS)
  expect(
    await chromeFlips(page),
    'and neither the hint nor its expiry summons anything: the chrome has not moved since the book opened',
  ).toEqual([])
  await expect(viewer(page)).toHaveAttribute('data-chrome', 'hidden')

  // The pointer has not moved from where Playwright starts it, which is inside
  // the top strip's box. Nothing has been dispatched there, so nothing has
  // fired — but the assertions below are about the chrome answering the *key*,
  // so the pointer is moved out of the strips before any of them.
  await standBackFromChrome(page)

  // ---- `H` (E-27) ---------------------------------------------------------
  await page.keyboard.press('h')
  await expect(
    viewer(page),
    'E-27: `H` is how a keyboard reader who never reaches for a screen edge calls the chrome',
  ).toHaveAttribute('data-chrome', 'visible')
  await expect(viewerTopBar(page)).toHaveAttribute('data-visible', 'true')
  await expect(viewerEdge(page, 'top')).toHaveCount(0)
  await expect(viewerEdge(page, 'bottom')).toHaveCount(0)

  await page.keyboard.press('h')
  await expect(viewer(page), 'and `H` sends it away again').toHaveAttribute(
    'data-chrome',
    'hidden',
  )
  await expect(viewerEdge(page, 'top')).toHaveCount(1)
  await expect(viewerEdge(page, 'bottom')).toHaveCount(1)

  // ---- the strips are the screen's own 44px edges -------------------------
  const viewerBox = await viewer(page).boundingBox()
  expect(viewerBox, 'the viewer must be laid out before its edges can be measured').not.toBeNull()
  for (const which of ['top', 'bottom'] as const) {
    const strip = await viewerEdge(page, which).boundingBox()
    expect(strip, `the ${which} strip must be laid out`).not.toBeNull()
    expect(
      strip?.height ?? 0,
      `E-27: the ${which} strip is ${String(EDGE_STRIP_PX)}px deep — the tap target the responsive layer uses everywhere else, and roughly the height of the bar it summons`,
    ).toBe(EDGE_STRIP_PX)
    expect(
      strip?.width ?? 0,
      `the ${which} strip spans the screen: E-27 calls it a screen edge, not a hotspot to find`,
    ).toBe(viewerBox?.width ?? 0)
  }

  // ---- and hovering the bottom one summons the chrome ---------------------
  const cx = (viewerBox?.x ?? 0) + (viewerBox?.width ?? 0) / 2
  const bottomInside = (viewerBox?.y ?? 0) + (viewerBox?.height ?? 0) - EDGE_STRIP_PX / 2
  // Two moves, like `wakeChrome`: the second is what guarantees a `mouseenter`
  // even if the pointer had already been at the first coordinate.
  await page.mouse.move(cx, bottomInside - 6)
  await page.mouse.move(cx, bottomInside)
  await expect(
    viewer(page),
    'E-27: the bottom edge of the *screen* is one of the three things that summon the chrome',
  ).toHaveAttribute('data-chrome', 'visible')

  // ---- entry 2: a hint that has been answered does not come back ----------
  // `toggleChrome` dismisses it (`store/viewer.ts`), so a reader who raises the
  // chrome inside the hint's window and puts it down again inside that same
  // window is not handed the sentence a second time. E-28 §3 is the same rule
  // for 다음 권 읽기, which arrives at this screen as a second `open()`.
  await standBackFromChrome(page)
  await page.reload()
  await expect(hint, 'leaving the viewer and coming back is an entry again').toBeVisible()
  const answeredAt = Date.now()

  await page.keyboard.press('h')
  await expect(viewer(page)).toHaveAttribute('data-chrome', 'visible')
  await expect(
    hint,
    'a line about where the controls went is meaningless with the controls on screen',
  ).toHaveCount(0)
  await page.keyboard.press('h')
  await expect(viewer(page)).toHaveAttribute('data-chrome', 'hidden')
  const answeredWithin = Date.now() - answeredAt
  // The premise, asserted rather than hoped for. Past the window the hint would
  // have expired on its own and the assertion below would hold for a reason that
  // has nothing to do with `dismissHint` — green, and about nothing (§6.5).
  expect(
    answeredWithin,
    `both presses have to land inside the hint's ${String(CHROME_HINT_MS)}ms window or the next assertion proves nothing; they took ${String(answeredWithin)}ms`,
  ).toBeLessThan(CHROME_HINT_MS)
  await expect(
    hint,
    'E-27/E-28: the hint is answered once per entry — the chrome going away again does not bring it back',
  ).toHaveCount(0)

  await clearProgress(page, bid)
})

/**
 * E-28 §2: **권 끝 카드는 크롬을 덮지 않는다.**
 *
 * This was a shipped defect and a nasty one — reaching the last page dropped an
 * opaque sheet over the whole viewer, so 뒤로, the slider, 표시 모드 and the
 * thumbnail strip all went under it and the only way out of a volume was the
 * card's own two buttons. The scrim is last in the DOM and deliberately has no
 * `z-index`; the two bars carry `z-chrome` (3) and stay above it.
 *
 * The unit tier can see that the class is on the bars (HANDOFF §6.5's
 * class-list technique) and no further: jsdom does no hit testing, so
 * "which element is actually under this pixel" is a question only a browser
 * answers. That is what this test asks, twice over — `elementFromPoint` for
 * each of the four controls, and then a real click, because Playwright's
 * actionability runs its own hit test at the action point and refuses to click
 * through something else.
 */
test('E-28 · at the end of a volume the chrome stays above the next-volume scrim', async ({
  page,
}, info) => {
  const sid = await seriesId(page, SERIES.scars)
  const books = await booksOf(page, sid)
  const book = books.find((candidate) => candidate.status === 'ok')
  expect(book, '§6.3 row 2: 상처를 쫓는자 is a folder of volume folders').toBeDefined()
  const bid = book?.id ?? ''
  const total = book?.page_count ?? 0
  expect(total, 'the last page is where FR-VWR-010 raises the card').toBeGreaterThan(0)
  await resetBookPrefs(page, bid)

  await openViewerDirect(page, sid, bid, total)
  await waitForPage(page)

  // The premises FR-VWR-010 raises the card on, asserted rather than assumed:
  // the last page, and not 세로 — where scrolling past the end *is* the end of
  // the volume and no card is raised at all.
  const stage = page.locator('[data-role="stage"]')
  await expect(stage).toHaveAttribute('data-mode', 'single')
  expect(await pageCount(page), 'the viewer must have opened the book the API named').toBe(total)
  expect(await currentPage(page), 'the card is raised at the last page').toBe(total)

  const scrim = page.locator('[data-role="next-volume-scrim"]')
  await expect(scrim, 'FR-VWR-010: the end-of-volume scrim').toBeVisible()
  await expect(page.locator('[data-role="next-volume-card"]')).toBeVisible()

  // `H` rather than a screen edge: the strips are *also* layered against the
  // scrim, and a setup step that shares the failure mode of the assertion would
  // hide it. The strips have their own coverage in the test above.
  await page.keyboard.press('h')
  await expect(viewer(page)).toHaveAttribute('data-chrome', 'visible')
  await expect(viewerTopBar(page)).toHaveAttribute('data-visible', 'true')
  await expect(viewerBottomBar(page)).toHaveAttribute('data-visible', 'true')

  const scrimBox = await scrim.boundingBox()
  expect(scrimBox, 'the scrim must be laid out before "above it" can mean anything').not.toBeNull()

  const controls = [
    { label: '뒤로', bar: 'viewer-top-bar', at: viewerTopBar(page).getByRole('button', { name: '뒤로' }) },
    { label: '표시 모드', bar: 'viewer-top-bar', at: viewerTopBar(page).locator('[aria-label="표시 모드"]') },
    {
      label: '페이지 슬라이더',
      bar: 'viewer-bottom-bar',
      at: page.locator('[data-role="page-slider"] input[type="range"]'),
    },
    {
      label: '썸네일 · T',
      bar: 'viewer-bottom-bar',
      at: page.getByRole('button', { name: '썸네일 · T' }),
    },
  ] as const

  for (const control of controls) {
    const box = await control.at.boundingBox()
    expect(box, `${control.label} must be laid out before it can be hit-tested`).not.toBeNull()
    const x = (box?.x ?? 0) + (box?.width ?? 0) / 2
    const y = (box?.y ?? 0) + (box?.height ?? 0) / 2
    // The other half of the claim, and the half a bare hit test would leave out:
    // "the bar wins" says nothing unless the scrim is over that pixel to begin
    // with. `absolute inset-0` says it is; this asserts it.
    expect(
      boxContains(scrimBox, x, y),
      `the scrim must cover ${control.label}, or this proves nothing about the layer order`,
    ).toBe(true)
    const hit = await page.evaluate(
      ({ px, py, bar }) => {
        const element = document.elementFromPoint(px, py)
        if (element === null) return 'nothing'
        const barElement = document.querySelector(`[data-role="${bar}"]`)
        if (barElement?.contains(element) === true) return bar
        const named = element.closest('[data-role]')
        return named === null ? element.tagName : (named.getAttribute('data-role') ?? '')
      },
      { px: x, py: y, bar: control.bar },
    )
    expect(
      hit,
      `E-28: at the end of a volume ${control.label} must still be the thing under the pointer`,
    ).toBe(control.bar)
  }

  // A hit test says the pixel belongs to the bar; a click says the browser
  // agrees. Playwright's actionability check hit-tests the action point and
  // times out rather than clicking through an element that is on top, so this
  // is the same claim made a second way — and FR-VWR-008 from a place the
  // reader used to be unable to reach it from.
  const stripButton = page.getByRole('button', { name: '썸네일 · T' })
  await stripButton.click()
  await expect(
    page.locator('[data-role="thumbnail-strip"]'),
    'E-28: the thumbnail strip opens from the last page of a volume',
  ).toBeVisible()
  await stripButton.click()
  await expect(page.locator('[data-role="thumbnail-strip"]')).toHaveCount(0)

  // Item `bs`: opening on the last page completes the volume, but the write is
  // debounced (`useProgressSync`), so the card's FR-VWR-012 action read 읽음
  // 표시 or 읽음 해제 depending on whether the shot beat the flush — the five
  // archived rounds split both ways. Wait for the row the server keeps, then
  // for the card to say so, and the frame has one answer.
  await expect
    .poll(
      async () => {
        const body = (await (await page.request.get(`/api/books/${bid}`)).json()) as {
          progress: { completed: boolean } | null
        }
        return body.progress?.completed ?? false
      },
      { timeout: 15_000, message: 'FR-VWR-012: reaching the last page records the volume as read' },
    )
    .toBe(true)
  await expect(
    page.locator('[data-role="next-volume-card"]').getByRole('button', { name: '읽음 해제' }),
  ).toBeVisible()

  await shot(page, info, 'e28-viewer-volume-end-chrome')

  // …and the way out the defect took away.
  await viewerTopBar(page).getByRole('button', { name: '뒤로' }).click()
  await expect(
    viewer(page),
    'E-28: 뒤로 is reachable from the last page — the volume end is not a trap with two exits of its own choosing',
  ).toHaveCount(0)
  await expect(page).toHaveURL(new RegExp(`/series/${sid}$`))

  await clearProgress(page, bid)
})

/**
 * E-44's fourth 맞춤 option, from the two directions no other tier can look.
 *
 * **Why this belongs at the browser tier and nowhere else.** `Seg` has no
 * keydown handler at all: its options share one `name`, so the single tab stop
 * and the ←/→ walk are the *browser's* radio-group behaviour, not ours
 * (`components/ds/Seg.tsx:57-61`, intent at `:8-9`). jsdom does not implement
 * that walk, so every arrow-key claim this repo makes about 맞춤 — including the
 * three places E-44 pins the option order *because* order is the key mapping —
 * was unverified until this test. A fourth radio that no key could reach would
 * have left all five gates green.
 *
 * The second assertion answers the review finding that `noHorizontalScroll`
 * (07-responsive) **structurally cannot** see this control overflow: the viewer
 * root is `fixed inset-0 overflow-hidden`, so anything spilling out of the bar
 * is clipped and never reaches `documentElement.scrollWidth`, and 07's viewer
 * leg runs chromeless anyway. Measuring the bar's own scroll box is the same
 * question asked where the answer lives. It runs in all four viewport projects,
 * so 400 px — where the group grew from ~235 px to ~312 px against a 368 px
 * content box — is covered without a width branch in the test.
 */
test('E-44 · 맞춤 네 옵션은 화살표로 전부 닿고, 어느 폭에서도 바 밖으로 넘치지 않는다', async ({
  page,
}, info) => {
  const sid = await seriesId(page, SERIES.scars)
  const books = await booksOf(page, sid)
  const book = books.find((candidate) => candidate.status === 'ok')
  expect(book, '§6.3 row 2: 상처를 쫓는자 is a folder of volume folders').toBeDefined()
  const bid = book?.id ?? ''
  await resetBookPrefs(page, bid)

  await openViewerDirect(page, sid, bid, 1)
  await waitForPage(page)

  // 너비 is the first option, so the walk below starts at a known end of the
  // group rather than at whatever `user.db` last held.
  await setViewerSeg(page, '맞춤', 'width')
  const fits = viewerTopBar(page).locator('[aria-label="맞춤"]')
  const stage = page.locator('[data-role="stage"]')

  // The premise the whole test rests on: clicking a label focuses the radio it
  // wraps. Asserted rather than assumed — if focus went to the label or stayed
  // on the body, every `press` below would go to the document and the walk
  // would "pass" by never moving.
  await expect(
    fits.locator('input[value="width"]'),
    'E-44: a click on the label focuses the radio, which is what makes the group one tab stop',
  ).toBeFocused()

  // Ascending DOM order *is* the key order (E-44 §1). 화면 is third, so it is
  // on the path — a reader arrowing from 너비 to 원본 passes through it.
  for (const value of ['height', 'contain', 'original']) {
    await page.keyboard.press('ArrowRight')
    await expect(
      fits.locator(`label[data-value="${value}"]`),
      `E-44: ArrowRight reaches ${value}`,
    ).toHaveAttribute('data-checked', 'true')
    await expect(
      stage,
      `E-44: ${value} is not just a checked radio — the stage takes the fit`,
    ).toHaveAttribute('data-fit', value)
  }

  // Native radio groups wrap. Worth pinning: it is the property that makes the
  // *first* option reachable from the last without a reverse walk, and it is
  // the browser's, so a future `Seg` that grew its own keydown handler would
  // have to keep it.
  await page.keyboard.press('ArrowRight')
  await expect(
    fits.locator('label[data-value="width"]'),
    'E-44: the walk wraps from 원본 back to 너비',
  ).toHaveAttribute('data-checked', 'true')

  // The overflow question, asked of the box that clips. `scrollWidth` beyond
  // `clientWidth` means a control is off the end of a bar the reader cannot
  // scroll — the fourth button's one real layout risk.
  const overflow = await viewerTopBar(page).evaluate(
    (el) => el.scrollWidth - el.clientWidth,
  )
  expect(
    overflow,
    'E-44: the wrapping bar holds all four 맞춤 options at this width — a group is `flex-none whitespace-nowrap`, so it cannot shrink its way out of trouble',
  ).toBe(0)

  // And the last option is inside the viewer, not merely inside a bar that is
  // itself off-screen — the failure `scrollWidth` alone would not catch.
  const viewerBox = await viewer(page).boundingBox()
  const lastOption = await fits.locator('label[data-value="original"]').boundingBox()
  expect(lastOption, 'the 원본 button must be laid out').not.toBeNull()
  const box = lastOption ?? { x: 0, y: 0, width: 0, height: 0 }
  expect(
    boxContains(viewerBox, box.x, box.y) &&
      boxContains(viewerBox, box.x + box.width, box.y + box.height),
    'E-44: the option that a fourth button pushes rightmost stays inside the viewer',
  ).toBe(true)

  // ui-spec §6.6 quotes the bar's wrapped heights, and E-44 invalidated every
  // one of them by widening 맞춤. Reported rather than asserted: the numbers are
  // documentation of a layout, and pinning them would redden this test for
  // every legitimate change to the bar's contents.
  const bar = await viewerTopBar(page).boundingBox()
  const measured = `viewer top bar height with four 맞춤 options: ${String(Math.round(bar?.height ?? 0))}px`
  info.annotations.push({ type: 'measured', description: measured })
  // Also to stdout, because the annotation alone is unreadable in practice: the
  // `list` reporter never prints it and the `html` one buries it in an encoded
  // blob. Whoever next has to refresh ui-spec §6.6's wrap heights needs this
  // number out of a log they already have, not a second nine-minute round.
  console.log(`[measured] ${info.project.name}: ${measured}`)

  await shot(page, info, 'e44-viewer-fit-four-options')

  await resetBookPrefs(page, bid)
  await clearProgress(page, bid)
})

// ---------------------------------------------------------------------------
// HANDOFF §5.7.11 `ay` — every other `Seg` this spec can reach, by keyboard
// ---------------------------------------------------------------------------

/** One key press of a walk across a `Seg`, and the option it has to land on. */
interface SegStep {
  readonly key: 'ArrowRight' | 'ArrowLeft'
  readonly value: string
  readonly why: string
}

/**
 * The E-44 walk above, for any option list — plus the half it does not take.
 *
 * → from the first option to the last, → once more for the wrap back to the
 * first, and then ←, which has to wrap the *other* way, round to the last. The
 * reverse wrap is the one a two-option group (읽기 방향) can be tested on at all:
 * a two-step walk has no middle, so its whole keyboard story is the two wraps.
 *
 * A copy of this lives in 06-settings.spec.ts. It belongs in shelf.ts, and was
 * not put there only because shelf.ts had another owner the day it was written;
 * a spec file cannot import another spec file without registering its tests
 * twice.
 */
function segWalk(values: readonly string[]): SegStep[] {
  const first = values[0]
  const last = values[values.length - 1]
  if (first === undefined || last === undefined || values.length < 2) {
    throw new Error('segWalk: a segmented control has at least two options')
  }
  return [
    ...values.slice(1).map((value) => ({
      key: 'ArrowRight' as const,
      value,
      why: `ArrowRight reaches ${value}`,
    })),
    { key: 'ArrowRight', value: first, why: `ArrowRight wraps from ${last} back to ${first}` },
    { key: 'ArrowLeft', value: last, why: `ArrowLeft wraps from ${first} back round to ${last}` },
  ]
}

/**
 * Walks one `Seg` by keyboard from its first option, checking each stop twice.
 *
 * Where it can, the caller has already put the control on its first option over
 * the API, as setup, and the click here changes nothing; where it cannot (the
 * series seed below) the click is what does it. Either way it is there for the
 * premise the E-44 test asserts and this one re-asserts: a click on the label
 * focuses the radio it wraps, and without that every `press` would go to the
 * document and the walk would "pass" by never moving.
 *
 * Each stop is then `data-checked` **and** whatever `landed` says the choice is
 * for. The first alone is not the claim: a radio that the browser checked and
 * nothing listened to would satisfy it, and that is exactly the failure a `Seg`
 * with no keydown handler of its own could have.
 */
async function walkSeg(
  page: Page,
  group: Locator,
  values: readonly string[],
  rule: string,
  landed: (value: string, why: string) => Promise<void>,
): Promise<void> {
  const first = values[0] ?? ''
  await group.locator(`label[data-value="${first}"]`).click()
  await expect(
    group.locator(`label[data-value="${first}"]`),
    `${rule}: the walk starts on ${first}`,
  ).toHaveAttribute('data-checked', 'true')
  await expect(
    group.locator(`input[value="${first}"]`),
    `${rule}: a click on the label focuses the radio, which is what makes the group one tab stop`,
  ).toBeFocused()

  for (const step of segWalk(values)) {
    await page.keyboard.press(step.key)
    const why = `${rule}: ${step.why}`
    await expect(group.locator(`label[data-value="${step.value}"]`), why).toHaveAttribute(
      'data-checked',
      'true',
    )
    // Focus travels with the selection in a native radio group. Asserted so a
    // walk that checked the right radio by some other route — a handler that
    // moved the value but left focus behind — cannot pass as the browser's.
    await expect(group.locator(`input[value="${step.value}"]`), why).toBeFocused()
    await landed(step.value, why)
  }
}

interface BookPrefsFact {
  reading_direction: string
  display_mode: string
  fit_mode: string
}

/** `GET /api/books/{bid}/prefs` — the per-book row a viewer `Seg` writes (arch §7.6). */
async function bookPrefs(page: Page, bid: string): Promise<BookPrefsFact> {
  const response = await page.request.get(`/api/books/${bid}/prefs`)
  expect(response.ok(), `GET /api/books/${bid}/prefs`).toBe(true)
  return (await response.json()) as BookPrefsFact
}

/**
 * The viewer's other two groups, in their `ViewerTopBar.tsx` option order.
 *
 * Order is the key mapping, as E-44 §1 says of 맞춤, so these arrays are copies
 * of `MODE_OPTIONS` / `DIR_OPTIONS` on the same terms as `SERIES` in shelf.ts —
 * and a reorder there is meant to redden this, not be absorbed by it.
 * `resetBookPrefs` puts the book on `single` / `ltr`, the first of each.
 */
const VIEWER_SEGS = [
  {
    group: '표시 모드',
    rule: 'FR-VWR-003',
    values: ['single', 'spread', 'vertical'],
    stageAttr: 'data-mode',
    field: 'display_mode',
  },
  {
    group: '읽기 방향',
    rule: 'FR-VWR-002',
    values: ['ltr', 'rtl'],
    stageAttr: 'data-dir',
    field: 'reading_direction',
  },
] as const satisfies readonly {
  group: string
  rule: string
  values: readonly string[]
  stageAttr: string
  field: keyof BookPrefsFact
}[]

/**
 * `ay` · the viewer's 표시 모드 and 읽기 방향, by keyboard.
 *
 * Same reason as the E-44 test, for the two groups it did not cover: `Seg` has
 * no keydown handler (`components/ds/Seg.tsx`), the ←/→ walk is the browser's
 * radio-group behaviour, and jsdom does not implement it — so until this test the
 * only `Seg` whose keyboard claim anything checked was 맞춤.
 *
 * In the viewer the two keys mean something else as well: ←/→ turn the page
 * (`useViewerKeys`). The walk works only because that hook drops keys whose
 * target is an `<input>` (`isTypingTarget`), and the focus assertion in
 * `walkSeg` is what proves the key went to the radio and not to the page.
 *
 * The effect is asserted on both sides of the wire: the stage re-lays itself
 * out (`data-mode` / `data-dir` on `[data-role="stage"]`), and the choice is
 * saved **for this volume** (FR-VWR-002 권 단위), which is the per-book prefs row.
 * Arrowing persists every option it passes — `ViewerPage.tsx` says so of 맞춤 and
 * leaves it alone — so every stop is a row this test reads back.
 */
for (const seg of VIEWER_SEGS) {
  test(`ay · 뷰어 ${seg.group} — 화살표로 전부 닿고, 양 끝에서 양쪽으로 감긴다`, async ({ page }) => {
    const sid = await seriesId(page, SERIES.scars)
    const books = await booksOf(page, sid)
    const book = books.find((candidate) => candidate.status === 'ok')
    expect(book, '§6.3 row 2: 상처를 쫓는자 is a folder of volume folders').toBeDefined()
    const bid = book?.id ?? ''
    await resetBookPrefs(page, bid)

    try {
      await openViewerDirect(page, sid, bid, 1)
      await waitForPage(page)
      // The chrome opens away (E-27); waking it parks the pointer on the bar,
      // which holds the auto-hide off for the whole walk.
      await wakeChrome(page)

      const group = viewerTopBar(page).locator(`[aria-label="${seg.group}"]`)
      const stage = page.locator('[data-role="stage"]')
      await walkSeg(page, group, seg.values, seg.rule, async (value, why) => {
        await expect(stage, `${why} — and the stage takes it`).toHaveAttribute(seg.stageAttr, value)
        await expect
          .poll(async () => (await bookPrefs(page, bid))[seg.field], {
            message: `${why} — and it is saved for this volume (FR-VWR-002 권 단위)`,
          })
          .toBe(value)
      })
    } finally {
      await resetBookPrefs(page, bid)
      await clearProgress(page, bid)
    }
  })
}

/** `store/seriesDir.ts`'s `SERIES_DIR_STORAGE_KEY` — the key C-9 names. A copy. */
const SERIES_DIR_STORAGE_KEY = 'shelf.seriesDir'

/**
 * `ay` · the series screen's 읽기 방향 seed, by keyboard.
 *
 * Not a viewer control, and here rather than in 03-series-detail only because
 * that spec had another owner when this was written. It is the viewer's in
 * purpose: C-9 / D-35 make it a client-only seed for the direction books opened
 * from that screen start at, and E-33 §2 is the ruling that the seed reach the
 * viewer.
 *
 * Its effect is therefore `localStorage`, not the server: the zustand `persist`
 * envelope under `shelf.seriesDir`, keyed by series id. The last assertion is
 * the other half of C-9 — the seed **never writes the server** — read as the
 * global default being exactly what it was before the walk.
 *
 * shelf.ts rule 2 holds without a `finally`: the only state written is in this
 * test's own browser context, which Playwright discards with it.
 */
test('ay · 시리즈 화면의 읽기 방향 씨앗도 화살표로 감기고, 서버에는 쓰지 않는다', async ({ page }) => {
  const sid = await seriesId(page, SERIES.scars)
  const before = (await (await page.request.get('/api/settings')).json()) as {
    reading_direction: string
  }

  await page.goto(`/series/${sid}`)
  await expect(page.getByRole('heading', { level: 1, name: SERIES.scars })).toBeVisible()

  const group = page.locator('header').locator('[aria-label="읽기 방향"]')
  const seed = (): Promise<string | null> =>
    page.evaluate(
      ([key, id]) => {
        const raw = window.localStorage.getItem(key)
        if (raw === null) return null
        const parsed = JSON.parse(raw) as { state?: { bySeries?: Record<string, string> } }
        return parsed.state?.bySeries?.[id] ?? null
      },
      [SERIES_DIR_STORAGE_KEY, sid] as const,
    )

  // No setup can put this one on L→R ahead of time — its starting value is the
  // global default until something is stored — so the walker's click does it
  // here when it has to. Either way the walk below starts on `ltr`.
  await walkSeg(page, group, ['ltr', 'rtl'], 'C-9 · 읽기 방향 seed', async (value, why) => {
    await expect.poll(seed, { message: `${why} — and the seed is stored for this series` }).toBe(value)
  })

  const after = (await (await page.request.get('/api/settings')).json()) as {
    reading_direction: string
  }
  expect(
    after.reading_direction,
    'C-9: the series-screen seed is client-only — arrowing it must not move the global default',
  ).toBe(before.reading_direction)
})

/**
 * E-62 — a chrome that is away is out of reach, and a keyboard reader inside
 * it holds it up.
 *
 * Before E-62 the bars faded on opacity and nothing else: hidden, they were
 * still in the Tab order and the accessibility tree, so a keyboard reader
 * tabbed into controls nobody could see, and one who had tabbed in while they
 * were up lost them 2 600 ms later with focus left on an invisible button.
 * Now a hidden bar is `inert`; focus put in a bar *by the keyboard* holds the
 * auto-hide off (the mouse and touch keep E-30's 2.6 s); and a chrome that
 * hides around the focus hands it to the viewer root. `H` answers from a radio
 * or the slider too, since neither takes letters.
 */
test('E-62 · a hidden chrome is out of reach, and the keyboard holds it up', async ({ page }) => {
  const { sid, bid, total } = await wheelBook(page)
  expect(total, 'a book with pages is the premise of every viewer assertion').toBeGreaterThan(1)
  await resetBookPrefs(page, bid)
  try {
    await openViewerDirect(page, sid, bid, 1)
    await waitForPage(page)
    await standBackFromChrome(page)
    await expect(viewer(page)).toHaveAttribute('data-chrome', 'hidden')

    /** Where focus is: inside one of the bars, on the viewer root, or elsewhere. */
    const focusAt = () =>
      page.evaluate(() => {
        const el = document.activeElement
        if (el === null || el === document.body) return 'body'
        if (el.closest('[data-role="viewer-top-bar"], [data-role="viewer-bottom-bar"]') !== null) {
          return `bar:${el.tagName}${el instanceof HTMLInputElement ? `/${el.type}` : ''}`
        }
        if (el.getAttribute('data-role') === 'viewer') return 'root'
        return `other:${el.tagName}`
      })
    /** Tab until focus is in a bar on a control `want` accepts; the stops it took. */
    const tabIntoBar = async (want: (where: string) => boolean): Promise<string[]> => {
      const stops: string[] = []
      for (let i = 0; i < 30; i += 1) {
        await page.keyboard.press('Tab')
        const where = await focusAt()
        stops.push(where)
        if (want(where)) return stops
      }
      return stops
    }

    // ---- (1) hidden: inert, so neither Tab nor the accessibility tree reach it
    await expect(viewerTopBar(page), 'E-62 §2.1: a hidden top bar is inert').toHaveAttribute('inert', '')
    await expect(viewerBottomBar(page), 'and so is the bottom bar').toHaveAttribute('inert', '')
    const hiddenStops: string[] = []
    for (let i = 0; i < 10; i += 1) {
      await page.keyboard.press('Tab')
      hiddenStops.push(await focusAt())
    }
    expect(
      hiddenStops.filter((where) => where.startsWith('bar:')),
      `E-62: no Tab press lands in a hidden bar (stops: ${JSON.stringify(hiddenStops)})`,
    ).toEqual([])
    // Chrome's own tree, not Playwright's role engine, which does not apply `inert`.
    const cdp = await page.context().newCDPSession(page)
    const exposed = async (name: string): Promise<boolean> => {
      const { nodes } = (await cdp.send('Accessibility.getFullAXTree')) as {
        nodes: { ignored: boolean; name?: { value?: unknown } }[]
      }
      return nodes.some((node) => !node.ignored && node.name?.value === name)
    }
    expect(await exposed('뒤로'), 'and 뒤로 is not in the accessibility tree while the chrome is away').toBe(false)

    // ---- (2) `H`, then Tab into the bar: the chrome holds past its deadline
    await page.keyboard.press('h')
    await expect(viewer(page)).toHaveAttribute('data-chrome', 'visible')
    await expect(viewerTopBar(page), 'a visible bar is not inert').not.toHaveAttribute('inert', '')
    expect(await exposed('뒤로'), 'and its 뒤로 is back in the accessibility tree').toBe(true)
    const inStops = await tabIntoBar((where) => where.startsWith('bar:'))
    expect(inStops.at(-1), `Tab must reach a control in the bar (stops: ${JSON.stringify(inStops)})`).toMatch(/^bar:/)
    await watchChrome(page)
    await page.waitForTimeout(CHROME_AUTOHIDE_MS + HOLD_SLACK_MS)
    expect(
      await chromeFlips(page),
      'E-62 §2.2: focus put in the bar by the keyboard holds the auto-hide off',
    ).toEqual([])
    await expect(viewer(page)).toHaveAttribute('data-chrome', 'visible')

    // Focus leaves the bar: the next deadline puts the chrome away.
    await page.locator('[data-role="viewer"]').focus()
    await expect(viewer(page), 'and once focus has left the bar, the chrome goes on its deadline').toHaveAttribute(
      'data-chrome',
      'hidden',
      { timeout: CHROME_AUTOHIDE_MS + HOLD_SLACK_MS },
    )

    // ---- a page turn is reading, not the keyboard working the bar: no hold
    await page.keyboard.press('h')
    await expect(viewer(page)).toHaveAttribute('data-chrome', 'visible')
    const buttonStops = await tabIntoBar((where) => where === 'bar:BUTTON')
    expect(buttonStops.at(-1), `Tab must reach a button in the bar (stops: ${JSON.stringify(buttonStops)})`).toBe(
      'bar:BUTTON',
    )
    const before = await currentPage(page)
    await page.keyboard.press('ArrowRight')
    await expect.poll(() => currentPage(page), { message: '→ on a bar button turns the page' }).not.toBe(before)
    await expect(viewer(page), 'E-62 §2.4: a page-turn key does not hold the chrome up').toHaveAttribute(
      'data-chrome',
      'hidden',
      { timeout: CHROME_AUTOHIDE_MS + HOLD_SLACK_MS },
    )
    expect(await focusAt(), 'and the focus it hid around goes to the viewer root').toBe('root')

    // ---- (3)+(5) `H` from a focused radio or the slider hides it, focus to the root
    for (const control of ['bar:INPUT/radio', 'bar:INPUT/range']) {
      await page.keyboard.press('h')
      await expect(viewer(page)).toHaveAttribute('data-chrome', 'visible')
      const stops = await tabIntoBar((where) => where === control)
      expect(stops.at(-1), `Tab must reach ${control} (stops: ${JSON.stringify(stops)})`).toBe(control)
      await page.keyboard.press('h')
      await expect(viewer(page), `E-62 §2.2: \`H\` answers from ${control}`).toHaveAttribute('data-chrome', 'hidden')
      expect(await focusAt(), 'E-62 §2.3: the chrome hid around the focus, which goes to the viewer root').toBe('root')
    }

    // ---- (4) a mouse click on a bar control: no hold, E-30's 2.6 s
    const single = viewerTopBar(page).locator('[aria-label="표시 모드"] label[data-value="single"]')
    await wakeChrome(page)
    await single.click()
    await standBackFromChrome(page)
    await expect(viewer(page), 'E-30: a pointer in the bar holds nothing once it has left').toHaveAttribute(
      'data-chrome',
      'hidden',
      { timeout: CHROME_AUTOHIDE_MS + HOLD_SLACK_MS },
    )
    expect(await focusAt(), 'and the focus the click left in the bar goes to the viewer root').toBe('root')
  } finally {
    await clearProgress(page, bid)
    await resetBookPrefs(page, bid)
  }
})

/**
 * E-62 on a touch screen — the seam E-30's shipped defect came through.
 *
 * A tap is not a click. After it Chrome dispatches compatibility mouse events
 * (`mouseover`, `mouseenter`, `mousemove`) that do not say they came from a
 * finger, and the pointer hover-hold tells the finger apart only by
 * `pointerType`: before E-30 one tap in a bar at 400 pinned the chrome open for
 * good, with every unit test green, because jsdom cannot produce that order.
 * E-62 sits on the same seam: a tap focuses the control it lands on, so the
 * last input has to read `pointer` for the keyboard-focus hold to stay off. A
 * key is pressed first, so a build that stopped recording the tap would still
 * be holding the keyboard's answer. Only the touch project (`hasTouch`) can
 * tap.
 */
test('E-62 · a tap on a bar control holds nothing, and its focus goes to the viewer (touch)', async ({
  page,
}) => {
  test.skip(test.info().project.use.hasTouch !== true, 'a tap needs the touch project (mobile-400)')
  const { sid, bid } = await wheelBook(page)
  await resetBookPrefs(page, bid)
  try {
    await openViewerDirect(page, sid, bid, 1)
    await waitForPage(page)
    const inBar = () =>
      page.evaluate(
        () =>
          document.activeElement?.closest('[data-role="viewer-top-bar"], [data-role="viewer-bottom-bar"]') !== null &&
          document.activeElement !== null,
      )
    const onRoot = () =>
      page.evaluate(() => document.activeElement?.getAttribute('data-role') === 'viewer')
    const centre = async (): Promise<{ x: number; y: number }> => {
      const box = await page.locator('[data-role="stage-zones"]').boundingBox()
      expect(box, 'the stage must be laid out to be tapped').not.toBeNull()
      return { x: (box?.x ?? 0) + (box?.width ?? 0) / 2, y: (box?.y ?? 0) + (box?.height ?? 0) / 2 }
    }
    const tapAt = async (target: Locator): Promise<void> => {
      const box = await target.boundingBox()
      expect(box, 'the control must be laid out to be tapped').not.toBeNull()
      await page.touchscreen.tap((box?.x ?? 0) + (box?.width ?? 0) / 2, (box?.y ?? 0) + (box?.height ?? 0) / 2)
    }

    // The keyboard's answer first, so it is what a tap that went unrecorded leaves.
    await page.keyboard.press('h')
    await expect(viewer(page)).toHaveAttribute('data-chrome', 'visible')
    await page.keyboard.press('h')
    await expect(viewer(page)).toHaveAttribute('data-chrome', 'hidden')

    const controls: [string, Locator][] = [
      ['the 표시 모드 seg', viewerTopBar(page).locator('[aria-label="표시 모드"] label[data-value="single"]')],
      ['the page slider', viewerBottomBar(page).locator('input[type="range"]')],
    ]
    for (const [what, control] of controls) {
      const { x, y } = await centre()
      await page.touchscreen.tap(x, y)
      await expect(viewer(page), 'a centre tap brings the chrome').toHaveAttribute('data-chrome', 'visible')
      await tapAt(control)
      const focused = await inBar()
      await expect(viewer(page), `E-30/E-62: a tap on ${what} holds nothing — the chrome goes on its deadline`).toHaveAttribute(
        'data-chrome',
        'hidden',
        { timeout: CHROME_AUTOHIDE_MS + HOLD_SLACK_MS },
      )
      await expect(viewerTopBar(page), 'and the bars are inert again').toHaveAttribute('inert', '')
      if (focused) expect(await onRoot(), `the focus the tap left on ${what} goes to the viewer root`).toBe(true)
    }
  } finally {
    await clearProgress(page, bid)
    await resetBookPrefs(page, bid)
  }
})

/**
 * E-30 §2 at the browser tier: a finger resting on a bar holds nothing.
 *
 * The `pointerType === 'touch'` check in the hover-hold makes no difference to
 * a short tap — Chrome sends `pointerout:touch` straight after `pointerup`, so
 * the hold is let go either way (measured, Chrome 150). It does make one while
 * the finger stays down: `touchStart` dispatches `pointerover:touch` and then
 * nothing until the finger lifts, so without the check the chrome is held for
 * as long as the finger rests. The press is made on a part of the bottom bar
 * that is not a control, so no button or long-press behaviour gets in the way.
 */
test('E-30 · a finger resting on a bar does not hold the chrome (touch)', async ({ page }) => {
  test.skip(test.info().project.use.hasTouch !== true, 'a press needs the touch project (mobile-400)')
  const { sid, bid } = await wheelBook(page)
  await resetBookPrefs(page, bid)
  const cdp = await page.context().newCDPSession(page)
  let down = false
  try {
    await openViewerDirect(page, sid, bid, 1)
    await waitForPage(page)
    await page.keyboard.press('h')
    await expect(viewer(page)).toHaveAttribute('data-chrome', 'visible')

    // A point on the bottom bar with no control under it.
    const spot = await viewerBottomBar(page).evaluate((bar) => {
      const box = bar.getBoundingClientRect()
      for (let y = box.top + 3; y < box.bottom - 2; y += 4) {
        for (let x = box.left + 3; x < box.right - 2; x += 6) {
          const el = document.elementFromPoint(x, y)
          // Only a control *inside* the bar counts: the viewer root above it
          // carries a tabindex of its own.
          const hit = el?.closest('button, input, label, a, [role="slider"], [tabindex]') ?? null
          if (el !== null && bar.contains(el) && (hit === null || !bar.contains(hit))) {
            return { x, y }
          }
        }
      }
      return null
    })
    expect(spot, 'the bottom bar must have a stretch with no control under it').not.toBeNull()

    await cdp.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x: spot?.x ?? 0, y: spot?.y ?? 0 }] })
    down = true
    await expect(
      viewer(page),
      'E-30 §2: a touch never holds — the chrome goes on its deadline with the finger still down',
    ).toHaveAttribute('data-chrome', 'hidden', { timeout: CHROME_AUTOHIDE_MS + HOLD_SLACK_MS })
  } finally {
    if (down) await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] })
    await clearProgress(page, bid)
    await resetBookPrefs(page, bid)
  }
})
