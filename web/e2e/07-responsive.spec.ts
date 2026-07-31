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
  currentPage,
  expect,
  gotoLibrary,
  openSeries,
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
