/**
 * impl-plan §6.3 step 6, assertion 5 — series detail for 군계.
 *
 * The 원본 경로 is on screen, and the volumes FR-IDX-010 marked unreadable carry
 * the 손상 badge with a reason and **are not clickable** — structurally, not by
 * a `disabled` attribute (ui-spec §5.3).
 *
 * The second test is E-14's other half, which the same screen owns: a series
 * with books but not one the reader can open is `status:"error"`, and must say
 * so rather than presenting as healthy.
 *
 * The third is not an impl-plan assertion but a regression guard — the volume
 * grid's counterpart to 01-library.spec.ts 6.1 (overlay), and the same defect one screen
 * down: an action overlay that is a later sibling of the openable control, and
 * so wins the hit test while invisible.
 */

import {
  booksOf,
  expect,
  gotoLibrary,
  openSeries,
  resetLibraryState,
  SERIES,
  seriesFacts,
  seriesId,
  setView,
  shot,
  test,
} from './shelf'

/** `readToggleLabel` names the action in both directions (E-12, FR-VWR-012). */
const READ_TOGGLE = /읽음 표시|읽음 해제/

test.beforeEach(async ({ page }) => {
  await resetLibraryState(page)
})

test('6.5 · 군계 detail shows its path, and broken volumes are badged and dead', async ({
  page,
}, info) => {
  const sid = await seriesId(page, SERIES.gungye)
  const books = await booksOf(page, sid)
  const broken = books.filter((book) => book.status !== 'ok')
  expect(
    broken.length,
    '§6.3 row 6: 군계 carries the collection’s real truncated archives',
  ).toBeGreaterThan(0)

  await gotoLibrary(page)
  await setView(page, 'grid')
  await openSeries(page, SERIES.gungye)

  // prd §5.3 / UI-5.3: the series screen is the one place a filesystem path is
  // shown, and it is the root's absolute path plus the series' own.
  const path = page.locator('[data-role="series-source-path"]')
  await expect(path).toBeVisible()
  await expect(path).toContainText(SERIES.gungye)

  // E-5 / D-6: every duplicate is listed, no dedup magic.
  const tiles = page.locator('[data-testid="volume-grid"] > *')
  await expect(tiles).toHaveCount(books.length)

  // FR-IDX-010 on screen: badge, Korean reason, and no control of any kind.
  const brokenTiles = tiles.filter({ hasText: '손상' })
  await expect(brokenTiles).toHaveCount(broken.length)
  await expect(brokenTiles.first()).toContainText('중앙 디렉터리 손상')
  for (let i = 0; i < broken.length; i++) {
    await expect(
      brokenTiles.nth(i).getByRole('button'),
      'a volume that can never be opened is not a control (ui-spec §5.3)',
    ).toHaveCount(0)
  }

  // …and the contrast: a healthy volume is one.
  const healthyName = books.find((book) => book.status === 'ok')?.name ?? ''
  expect(healthyName).not.toBe('')
  const healthyTile = tiles.filter({ has: page.locator(`[title="${healthyName}"]`) })
  await expect(healthyTile.getByRole('button').first()).toBeEnabled()

  await shot(page, info, 'step-06-5a-series-detail-gungye')

  // The same two facts survive the list rendering of ui-spec §5.4.
  await setView(page, 'list')
  const rows = page.locator('[data-testid="volume-row"]')
  await expect(rows).toHaveCount(books.length)
  await expect(rows.filter({ hasText: '손상' })).toHaveCount(broken.length)
  await expect(rows.filter({ hasText: '손상' }).first().locator('[data-role="volume-state"]')).toHaveText(
    'ERR',
  )

  await shot(page, info, 'step-06-5b-series-detail-gungye-list')
  await setView(page, 'grid')
})

test('6.5 (E-14) · a series with nothing readable in it is shown as broken', async ({
  page,
}, info) => {
  // `비둘기.zip` opens cleanly and holds one directory entry: no page, and no
  // inner archive that could be a volume either. It is what `empty` means now
  // that a container of sub-archives is a series of volumes (D-70) — before
  // that, this test made the same point with 엔젤하트, which is now readable.
  const facts = await seriesFacts(page)
  const dove = facts.get(SERIES.dove)
  expect(dove, '§6.3 row 10b').toBeDefined()
  expect(dove?.status, 'E-14: ≥1 book, none of them ok ⇒ series error').toBe('error')

  await gotoLibrary(page)
  await openSeries(page, SERIES.dove)

  // design.md 화면 2 "오류 상태": the reason is on screen, in the accent badge
  // + reason pairing ui-spec §2.5 allows for the 손상 family.
  const banner = page.locator('[data-role="series-error"]')
  await expect(banner).toBeVisible()
  await expect(banner).toContainText('손상')

  // Both read actions are dead, because no volume can be opened.
  await expect(page.getByRole('button', { name: /읽기 시작|이어 읽기/ })).toBeDisabled()
  await expect(page.getByRole('button', { name: '처음부터 읽기' })).toBeDisabled()

  // The *book* is `empty`, not an error: the container is intact, it simply
  // holds nothing to read.
  await expect(page.locator('[data-testid="volume-grid"]')).toContainText('비어 있음')
  await expect(page.locator('[data-testid="volume-grid"]')).toContainText(
    '읽을 수 있는 페이지가 없습니다',
  )

  await shot(page, info, 'step-06-5c-series-detail-error')
})

test('6.5 (D-70) · a container of sub-archives is a series of volumes', async ({ page }, info) => {
  // The 1.4 GB `겟 벡커스 1~39완.zip` shape: a ZIP whose entries are all more
  // ZIPs. It used to be one book with `status:"empty"` and no pages at all —
  // 45 books and 623 volumes of the collection were unreachable that way.
  const facts = await seriesFacts(page)
  const angel = facts.get(SERIES.angelHeart)
  expect(angel, '§6.3 row 10').toBeDefined()
  expect(angel?.status, 'every inner volume opens, so the series is ok').toBe('ok')
  expect(angel?.book_count, 'one book per inner archive').toBeGreaterThan(1)

  await gotoLibrary(page)
  await openSeries(page, SERIES.angelHeart)

  // No 손상 banner, and the volumes are real books with page counts.
  await expect(page.locator('[data-role="series-error"]')).toHaveCount(0)
  await expect(page.getByRole('button', { name: /읽기 시작|이어 읽기/ })).toBeEnabled()

  const grid = page.locator('[data-testid="volume-grid"]')
  await expect(grid).not.toContainText('비어 있음')
  await expect(grid.locator('> *')).toHaveCount(angel!.book_count)

  await shot(page, info, 'step-06-5d-series-detail-nested-volumes')
})

test('6.5 (guard) · the 읽음 표시 toggle is a hit target only while it is visible', async ({
  page,
}) => {
  const sid = await seriesId(page, SERIES.gungye)
  const books = await booksOf(page, sid)
  const healthyName = books.find((book) => book.status === 'ok')?.name ?? ''
  expect(healthyName, '§6.3 row 6: 군계 also carries volumes that open').not.toBe('')

  await gotoLibrary(page)
  await setView(page, 'grid')
  await openSeries(page, SERIES.gungye)

  const tile = page
    .locator('[data-testid="volume-grid"] > *')
    .filter({ has: page.locator(`[title="${healthyName}"]`) })
  await tile.scrollIntoViewIfNeeded()

  const toggle = tile.getByRole('button', { name: READ_TOGGLE })
  await expect(toggle).toHaveCount(1)
  // The reveal is opacity alone (ui-spec §8.3), and Playwright counts an
  // `opacity: 0` element as visible — `toBeVisible()` would pass either way, so
  // opacity is what "visible" has to mean here.
  const overlay = tile.locator('div').filter({ has: page.getByRole('button', { name: READ_TOGGLE }) })

  // The pointer is still wherever the click that opened the series left it.
  // Park it off the tile: the state this guard is about is the resting one,
  // because a tap has no hover to precede it.
  await page.mouse.move(1, 1)

  const box = await toggle.boundingBox()
  expect(box, 'the toggle keeps its geometry while transparent — that is the trap').not.toBeNull()
  const point = {
    x: box === null ? 0 : box.x + box.width / 2,
    y: box === null ? 0 : box.y + box.height / 2,
  }

  /**
   * The control a tap at the toggle's centre would land on, named by its text.
   *
   * `document.elementFromPoint` is the browser's own hit test — the one a
   * pointer event runs — evaluated *without* the pointer movement that would
   * change the answer. That is the whole reason a click cannot express this:
   * a mouse must hover before it can click, and hovering is precisely what
   * makes the toggle a legitimate target.
   */
  const hitTest = (): Promise<string> =>
    page.evaluate((at) => {
      const el = document.elementFromPoint(at.x, at.y)
      if (el === null) return 'nothing'
      const control = el.closest('button')
      if (control === null) return `<${el.tagName.toLowerCase()}>, no control`
      return (control.textContent ?? '').trim()
    }, point)

  // `(hover: none)` is the "there is no hover to reveal it with" case, and
  // there the control is deliberately always shown rather than always hidden;
  // `hasTouch` (playwright.config.ts) makes mobile-400 report it. The invariant
  // is the same on both sides — hit-testable if and only if visible — so only
  // the resting state differs.
  const canHover = await page.evaluate(() => matchMedia('(hover: hover)').matches)

  // Which branch this project takes is itself pinned, and that is not
  // decoration. Measured against the four projects, `(hover: hover)` is true on
  // desktop-1440 / laptop-1024 / tablet-768 and false on mobile-400 — Chrome
  // derives it from `hasTouch`, which the config sets from `width < 768`. So
  // both branches below really do run, one project against three.
  //
  // The failure this guards is §6.5's shape: an `if` whose condition quietly
  // stops being true does not go red, it goes *absent*. Flip `hasTouch` to true
  // for every project and the three hover assertions would simply stop running
  // while the suite stayed green — the same way `toHaveCount(0)` cannot tell
  // "the control was removed" from "the feature gate was off". Asserting the
  // browser's answer against the config's intent makes that a failure instead.
  const hasTouch = test.info().project.use.hasTouch ?? false
  expect(
    canHover,
    'the branch below is chosen by the browser; this pins it to what the project asked for',
  ).toBe(!hasTouch)

  if (canHover) {
    await expect(overlay).toHaveCSS('opacity', '0')

    // 66×29 px pinned to the top-right of the cover, about a fifth of it.
    // Before the fix this point resolved to the toggle, and a genuine tap there
    // flipped persisted read state while the volume did not open.
    expect(await hitTest(), 'an invisible control must not be a hit target').not.toMatch(
      READ_TOGGLE,
    )
    expect(await hitTest(), 'the hit belongs to the volume underneath').toContain(healthyName)

    await tile.hover()
    await expect(overlay).toHaveCSS('opacity', '1')
  } else {
    await expect(overlay).toHaveCSS('opacity', '1')
  }

  // …and the half that gating on `:hover` alone would have broken: once it is
  // visible it really is reachable. `trial` runs Playwright's actionability and
  // hit-target checks and dispatches nothing, so this guard writes no progress
  // and leaves the server as it found it (shelf.ts rule 2).
  expect(await hitTest(), 'a visible control must take the hit').toMatch(READ_TOGGLE)
  await toggle.click({ trial: true })
})
