/**
 * impl-plan §6.3 step 6, assertion 11 — the responsive layer (ui-spec §7,
 * NFR-CMP-002, ruling E-10, D-42).
 *
 * §6.3 words this step for 400px, but the four viewport projects exist so that
 * the *tier* each width belongs to is asserted, not only the narrowest one:
 * ≥1024 keeps the 240px sidebar, 768–1023 collapses it to the 56px icon rail
 * that E-10 confirmed against the prototype's clipped screenshot, and <768
 * removes it from the DOM in favour of an off-canvas drawer.
 *
 * "No horizontal page scroll" is checked on all three screens at every width,
 * because the prototype's captured breakage (`library-list-768.png`,
 * `library-grid-400-broken.png`, `viewer-overlay-400-broken.png`) is one per
 * screen, not one per app.
 */

import {
  booksOf,
  clearProgress,
  currentPage,
  expect,
  gotoLibrary,
  openSeries,
  openViewerDirect,
  resetBookPrefs,
  resetLibraryState,
  SERIES,
  seriesId,
  setView,
  shot,
  test,
  viewer,
  waitForPage,
} from './shelf'

/** ui-spec §7's rail width (E-10). */
const RAIL_WIDTH_PX = 56
/** ui-spec §7's fixed sidebar. */
const SIDEBAR_WIDTH_PX = 240
/** ui-spec §7's 이어보기 card in the 768–1023 tier. */
const CONTINUE_CARD_TABLET_PX = 260
/** ui-spec §7's 이어보기 card at and above 1024. */
const CONTINUE_CARD_DESKTOP_PX = 269
/** `ContinueRow`'s `gap-3`, the distance between two snap positions below 768. */
const CONTINUE_GAP_PX = 12

async function noHorizontalScroll(page: import('@playwright/test').Page): Promise<void> {
  const overflow = await page.evaluate(() => ({
    scrollWidth: document.documentElement.scrollWidth,
    clientWidth: document.documentElement.clientWidth,
  }))
  expect(
    overflow.scrollWidth,
    `NFR-CMP-002: no horizontal page scroll (${String(overflow.scrollWidth)} > ${String(overflow.clientWidth)})`,
  ).toBeLessThanOrEqual(overflow.clientWidth)
}

test.beforeEach(async ({ page }) => {
  await resetLibraryState(page)
})

test('6.11 · the sidebar tier, the drawer, and no horizontal scroll anywhere', async ({
  page,
}, info) => {
  const width = page.viewportSize()?.width ?? 0
  const sid = await seriesId(page, SERIES.clover)
  const books = await booksOf(page, sid)
  const bid = books.find((book) => book.status === 'ok')?.id ?? ''
  await resetBookPrefs(page, bid)

  await gotoLibrary(page)
  await setView(page, 'grid')

  const sidebar = page.locator('aside[aria-label="라이브러리 탐색"]')
  const hamburger = page.getByRole('button', { name: '라이브러리 탐색 열기' })

  if (width < 768) {
    // D-42: not merely hidden — not rendered, or the drawer would leave a second
    // copy of every nav row in the accessibility tree.
    await expect(sidebar).toHaveCount(0)
    await expect(hamburger).toBeVisible()
    await hamburger.click()
    const drawer = page.getByRole('dialog', { name: '라이브러리 탐색' })
    await expect(drawer).toBeVisible()
    await expect(drawer.getByRole('button', { name: '읽는 중' })).toBeVisible()
    await shot(page, info, 'step-06-11a-library-drawer')
    await page.keyboard.press('Escape')
    await expect(drawer).toHaveCount(0)
  } else if (width < 1024) {
    // E-10: the icon rail is correct and the reference screenshot is not.
    const box = await sidebar.boundingBox()
    expect(Math.round(box?.width ?? 0)).toBe(RAIL_WIDTH_PX)
    await expect(hamburger).toBeVisible()
  } else {
    const box = await sidebar.boundingBox()
    expect(Math.round(box?.width ?? 0)).toBe(SIDEBAR_WIDTH_PX)
    await expect(sidebar.getByRole('button', { name: '읽는 중' })).toBeVisible()
  }

  await noHorizontalScroll(page)
  await shot(page, info, 'step-06-11b-library')

  await setView(page, 'list')
  await noHorizontalScroll(page)
  await shot(page, info, 'step-06-11c-library-list')
  await setView(page, 'grid')

  await openSeries(page, SERIES.clover)
  await noHorizontalScroll(page)
  await shot(page, info, 'step-06-11d-series-detail')

  // ---- the viewer, and the tap zones of FR-VWR-011 ------------------------
  await page.locator(`[data-testid="volume-grid"] [title="${books[0]?.name ?? ''}"]`).click()
  await expect(viewer(page)).toBeVisible()
  await waitForPage(page)
  await noHorizontalScroll(page)
  await shot(page, info, 'step-06-11e-viewer')

  const zones = page.locator('[data-role="stage-zones"]')
  const box = await zones.boundingBox()
  expect(box).not.toBeNull()
  const y = (box?.y ?? 0) + (box?.height ?? 0) / 2
  const left = (box?.x ?? 0) + (box?.width ?? 0) * 0.15
  const right = (box?.x ?? 0) + (box?.width ?? 0) * 0.85

  const before = await currentPage(page)
  if (width < 768) {
    // Only the mobile project runs with `hasTouch`, which is the configuration
    // ui-spec §8.3's tap zones exist for.
    await page.touchscreen.tap(right, y)
    await expect.poll(async () => currentPage(page)).toBe(before + 1)
    await page.touchscreen.tap(left, y)
    await expect.poll(async () => currentPage(page)).toBe(before)
  } else {
    // The same rule, driven by the mouse: `useTouchZones` binds both.
    await page.mouse.click(right, y)
    await expect.poll(async () => currentPage(page)).toBe(before + 1)
    await page.mouse.click(left, y)
    await expect.poll(async () => currentPage(page)).toBe(before)
  }

  await page.keyboard.press('Escape')
  await expect(viewer(page)).toHaveCount(0)
})

/**
 * 6.12 — the 이어보기 column of the ui-spec §7 matrix, which had never been
 * checked at any width.
 *
 * The two bottom cells of that column shipped wrong for ten sessions (218px at
 * `<768` where the spec asks for one full-width card per screen with snap
 * scroll; 269px at 768–1023 where it asks for 260) and the reason no gate said
 * so is written into the spec's own amendment note: **this file drove the
 * sidebar, the grid, the list and the viewer through all four tiers and never
 * once mentioned the shelf.** A check that does not look at the thing cannot
 * report the thing missing — §6.5 again.
 *
 * **This test has been watched red.** Against the pre-fix stylesheet it measures
 * 269 at 768 where it wants 260, and 222 in a 368px track at 400 where it wants
 * one card filling it — the two numbers §7's note predicted. Be exact about the
 * order, though: the unit tier was the half watched red before the CSS moved,
 * and this one was reproduced afterwards by restoring the old class lists and
 * **rebuilding**. The rebuild is not optional and is the trap to write down —
 * the SPA is embedded in the binary (`spa.go`), so editing the source and
 * re-running Playwright leaves the old bundle serving and any mutation survives
 * for no reason at all.
 *
 * Geometry, not class names. The unit tier already pins the class list
 * (`library.test.tsx`, "pins the card width so the spec cannot drift"), so what
 * a browser adds here is the only thing jsdom cannot answer: what the card
 * actually measures, and whether the browser really snaps. The snap half is
 * asserted as a *behaviour* — a nudge the browser has to undo — because
 * `scroll-snap-type` being present in a computed style is a declaration, and
 * this column's whole history is declarations that nothing compared against
 * the product. **That nudge is not independently proven** (§6.5's labelling
 * rule — do not read it as equivalent): deleting the snap classes reddens the
 * `scroll-snap-type` assertion first, so no mutation reaches the nudge. It is
 * carried because a declaration Chromium chose to ignore would otherwise read
 * as a met requirement, not because a mutation has shown it load-bearing.
 *
 * §7's note words the tablet case as "a test at 400 and at 900". 900 is not one
 * of the four viewport projects; **768 is, and it is the same 768–1023 tier**,
 * so that is the width this runs at. The cell is about the tier, not about 900.
 */
test('6.12 · the 이어보기 shelf obeys the §7 tier it is in', async ({ page }, info) => {
  const width = page.viewportSize()?.width ?? 0

  // **Two series, not two volumes of one.** The shelf is one card per *series*
  // — `ListContinue` filters `b.id IN (latestPerSeries)` (`internal/index/
  // books.go`) — so opening 군계 01 and 군계 02 puts one card on the shelf, not
  // two, and "one card per screen" would be asserted against a track that only
  // ever held one. The first draft of this test did exactly that and failed
  // here rather than on the geometry, which is the useful way round.
  //
  // **울프가이 is the third, and it is here for its filename.** The card's width
  // was declared and did not hold for ten sessions (see `ContinueCard`'s header)
  // because a flex item's automatic minimum floors it at min-content, and the
  // filename was `nowrap` — so the tier applied to cards whose name was short
  // and to no others. A shelf of short names cannot see that, which is what
  // this test used to be. 울프가이's volumes are
  // `Wolf_Guy_-_Wolfen_Crest_v0N_JP.{zip,rar}` in **both rounds** — the real
  // collection carries them and `scripts/mkfixture` builds the same three names
  // — so the longest of them is a long name that is also a single unbreakable
  // 34-character Latin token, which is the harder half of the same defect.
  const opened: { sid: string; bookId: string }[] = []
  for (const name of [SERIES.clover, SERIES.gungye, SERIES.wolfGuy]) {
    const sid = await seriesId(page, name)
    const readable = (await booksOf(page, sid)).filter((book) => book.status === 'ok')
    expect(readable.length, `${name} must have a readable volume to put on the shelf`).toBeGreaterThan(0)
    // The *longest* name, not the first: which volume lands on the shelf is what
    // decides whether this test can see the defect at all.
    const pick =
      name === SERIES.wolfGuy
        ? readable.reduce((a, b) => (b.name.length > a.name.length ? b : a))
        : readable[0]
    opened.push({ sid, bookId: pick?.id ?? '' })
  }

  // shelf.ts rule 2: this spec invents the progress rows that put the shelf on
  // screen, so it takes them away again whatever happens below.
  try {
    for (const { sid, bookId } of opened) {
      await openViewerDirect(page, sid, bookId, 2)
      await page.keyboard.press('Escape')
      // Wait for *this* book's row before opening the next one. `useProgressSync`
      // debounces by a second and flushes on unmount, and `openViewerDirect` is a
      // full `page.goto` — so the next iteration can tear the document down with
      // the previous flush still in flight and the shelf comes up one card short.
      // That is not hypothetical: it is what this test did on one viewport
      // project out of four before the wait was added, which is exactly the
      // shape of flake that gets "fixed" by a retry and stays wrong.
      await expect
        .poll(
          async () => {
            const body = (await (await page.request.get(`/api/books/${bookId}`)).json()) as {
              progress: { last_page: number } | null
            }
            return body.progress
          },
          { timeout: 15_000, message: `the progress row for ${bookId} must reach the server` },
        )
        .not.toBeNull()
    }
    // Wait for the *server*, not for the client that wrote it: `useProgressSync`
    // debounces its write by a second (04-viewer 6.6b), and the shelf renders
    // what `GET /api/continue` answers.
    await expect
      .poll(
        async () => {
          const body = (await (await page.request.get('/api/continue')).json()) as {
            items: unknown[]
          }
          return body.items.length
        },
        {
          timeout: 15_000,
          message: 'FR-LIB-010: all three opened series must reach /api/continue',
        },
      )
      .toBeGreaterThanOrEqual(3)

    await gotoLibrary(page)
    const track = page.locator('[data-testid="continue-track"]')
    await expect(track).toBeVisible()
    const card = track.getByRole('button').first()
    await expect(card).toBeVisible()

    const trackWidth = await track.evaluate((el) => el.clientWidth)
    const snapType = await track.evaluate((el) => getComputedStyle(el).scrollSnapType)
    const snapAlign = await card.evaluate((el) => getComputedStyle(el).scrollSnapAlign)

    // **Every card, not the first one.** The tier is a property of the shelf and
    // the old form of this assertion — one `boundingBox()` on `.first()` — is
    // what let a 413px card ship on a 269px shelf: the first card's filename was
    // short, so the measurement was of the one card that was never wrong. The
    // spread also tells the failure message which card broke, by name.
    const cards = await track.evaluate((el) =>
      [...el.querySelectorAll<HTMLElement>(':scope > button')].map((b) => ({
        aria: b.getAttribute('aria-label') ?? '',
        width: Math.round(b.getBoundingClientRect().width),
      })),
    )
    expect(cards.length, 'the shelf holds the three series this test opened').toBeGreaterThanOrEqual(3)
    const widths = [...new Set(cards.map((c) => c.width))]
    const describeCards = (): string => cards.map((c) => `${String(c.width)}px ${c.aria}`).join('\n  ')
    expect(widths, `ui-spec §7: one width per tier, not one per filename —\n  ${describeCards()}`).toHaveLength(1)
    const cardWidth = widths[0] ?? 0

    if (width < 768) {
      // "Full-width cards, one per screen, snap scroll."
      expect(
        cardWidth,
        `ui-spec §7 <768: one card fills the track (${String(cardWidth)}px in a ${String(trackWidth)}px track)`,
      ).toBe(trackWidth)
      expect(snapType, 'ui-spec §7 <768: the track snaps on the x axis').toMatch(/x mandatory/)
      expect(snapAlign, 'ui-spec §7 <768: each card is a snap position').toBe('start')

      // The declaration is not the behaviour. Nudge the track off a snap
      // position and the browser must put it back; without `mandatory` it
      // simply stays where it was put. The nudge is passed in rather than
      // closed over: `evaluate` runs in the page, where this file's constants
      // do not exist.
      await track.evaluate((el, nudge) => {
        el.scrollLeft = nudge
      }, CONTINUE_GAP_PX)
      await expect
        .poll(async () => track.evaluate((el) => Math.round(el.scrollLeft)), {
          timeout: 5_000,
          message: 'ui-spec §7 <768: a nudge inside the first card resnaps to it',
        })
        .toBe(0)
    } else if (width < 1024) {
      expect(cardWidth, 'ui-spec §7 768–1023: 260px cards').toBe(CONTINUE_CARD_TABLET_PX)
      // The snap layer belongs to `<768` only: above it the shelf is an
      // ordinary horizontal scroller and more than one card is meant to show.
      expect(snapType, 'ui-spec §7 768–1023: no snap').toBe('none')
      expect(cardWidth, 'and more than one card fits the track').toBeLessThan(trackWidth)
    } else {
      expect(cardWidth, 'ui-spec §7 ≥1024: 269px cards').toBe(CONTINUE_CARD_DESKTOP_PX)
      expect(snapType, 'ui-spec §7 ≥1024: no snap').toBe('none')
    }

    // **The long name wraps rather than widening the card.** The width assertion
    // above is only meaningful while a long name is on the shelf, and "a long
    // name is on the shelf" is a property of the fixture, not of the code under
    // test — a future rename could make every assertion above pass by making the
    // defect unreachable. So the premise is asserted too: one of these cards
    // carries a filename that needs a second line, and takes one.
    const names = await track.evaluate((el) =>
      [...el.querySelectorAll<HTMLElement>(':scope > button')].map((b) => {
        // The filename is the second child of the text column; the title is the
        // first and is clamped the same way.
        const column = b.querySelector<HTMLElement>('span.min-w-0')
        const file = column?.children[1] as HTMLElement | undefined
        if (file === undefined) return null
        const cs = getComputedStyle(file)
        const lineHeight = Number.parseFloat(cs.lineHeight)
        return {
          text: file.textContent ?? '',
          lines: Math.round(file.getBoundingClientRect().height / lineHeight),
          whiteSpace: cs.whiteSpace,
          overflowsSideways: file.scrollWidth > file.clientWidth + 1,
        }
      }),
    )
    const measuredNames = names.filter((n) => n !== null)
    expect(measuredNames.length, 'every card exposes its filename to be measured').toBe(cards.length)
    for (const name of measuredNames) {
      expect(name.whiteSpace, `the filename wraps: ${name.text}`).not.toBe('nowrap')
      expect(name.overflowsSideways, `the filename is not clipped sideways: ${name.text}`).toBe(false)
    }
    expect(
      measuredNames.some((n) => n.lines >= 2),
      `one card must carry a filename long enough to have widened the card —\n  ${measuredNames.map((n) => `${String(n.lines)} line(s) ${n.text}`).join('\n  ')}`,
    ).toBe(true)

    // The shelf is the widest thing on the library screen below 768 now, so it
    // is the most likely thing to push the page. NFR-CMP-002 either way.
    await noHorizontalScroll(page)
    await shot(page, info, 'step-06-12-continue-shelf')
  } finally {
    for (const { bookId } of opened) {
      await clearProgress(page, bookId)
    }
  }
})
