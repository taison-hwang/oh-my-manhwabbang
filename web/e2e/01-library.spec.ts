/**
 * impl-plan §6.3 step 6, assertions 1–3 — the library screen.
 *
 *   6.1  the grid renders every series `scan.include_globs` selected — the
 *        curated ten of §6.3, and in synthetic mode the two D-49 extras with
 *        them (`EXPECTED_SERIES` in shelf.ts) — covers appear (retrying the
 *        `202 queued` a lazily generated thumbnail answers with), and the
 *        screen settles with no cumulative layout shift worth the name
 *   6.2  the 그리드/리스트 toggle, sorted 용량 desc, puts the largest series first
 *   6.3  `ㄱㄱ` in the search box finds 군계 by 초성 (FR-LIB-006) and highlights
 *        the syllables the query stood for
 *
 * 6.1 (overlay) and 6.1 (overlay buttons) are not impl-plan assertions but the
 * two halves of one regression guard over the grid card's hover overlay. The
 * first pins the precondition every other spec's `openSeries()` silently depends
 * on — the overlay must not eat the cover's click. The second pins its converse
 * — the overlay's own two buttons must be pressable while it is up — which is
 * the failure the first one cannot see, and which no jsdom test can see at all.
 *
 * ## The numbering, so nobody has to guess it again
 *
 * `6.n` is impl-plan §6.3 step 6 **assertion n**, and a file may only use the
 * numbers of the assertions it implements. The two guards above are not
 * assertions, so they borrow the number of the assertion whose *screen* they
 * guard — 6.1's library grid — and say what they are in the parenthesis; that
 * is 03-series-detail.spec.ts's `6.5 (E-14)` / `6.5 (guard)` form. The first of
 * them used to be called plain `6.4`, which is assertion 4 and belongs to
 * 02-palette.spec.ts's `Ctrl+K` test: one number, two files, and a third test
 * about to take it as well. The three comments that pointed at the old name —
 * 03-series-detail.spec.ts's header and two in library.test.tsx — were updated
 * with the rename; `6.4` now occurs in exactly one spec, which is 02-palette's.
 */

import {
  expect,
  expectCuratedLibrary,
  gotoLibrary,
  openSeries,
  resetLibraryState,
  SERIES,
  seriesFacts,
  seriesTile,
  seriesTiles,
  setSort,
  setView,
  shot,
  test,
  viewer,
  walkLibrary,
} from './shelf'

test.beforeEach(async ({ page }) => {
  await resetLibraryState(page)
})

test('6.1 · the grid renders the whole curated library, with real covers and no layout shift', async ({
  page,
}, info) => {
  const facts = await seriesFacts(page)
  expectCuratedLibrary(facts.keys(), 'GET /api/series must list exactly the include_globs entries')

  await gotoLibrary(page)

  // FR-LIB-001: every curated series is rendered — but "rendered" is not "in the
  // DOM all at once", and asserting the latter was asserting the viewport rather
  // than the product. FR-LIB-007 is 필수 and `SeriesGrid` windows rows with
  // `overscan: 2`; ui-spec §7 then gives the two narrow tiers big cards
  // (`--grid-min: 224px` → 2×326px columns at 768, 2×172px at 400), so the ten
  // cards mount together at 1440 and 1024 and eight of them do at 768 and 400.
  // `walkLibrary` therefore reaches the tail the way a reader does, by scrolling,
  // and the union it returns is what "the library holds these series" means once
  // the grid is virtualised. A lost series fails the set comparison below.
  //
  // FR-LIB-008 / UI-5.3: the striped placeholder is *always* beneath the cover,
  // so a cover that is still queued costs no layout shift. Every series the
  // index says has a cover must eventually paint one — including the ones whose
  // first request answered `202 Retry-After`, which `useCoverImage` retries —
  // and the one with no cover at all must fall back to the FR-LIB-008 name
  // placeholder rather than to a broken image. Both are asserted at the stop
  // that mounted the card, while it is still there to be asserted against.
  const rendered = await walkLibrary(page, async (names) => {
    for (const name of names) {
      const hasCover = facts.get(name)?.has_cover
      const unlisted = `the grid rendered "${name}", which GET /api/series does not list`
      expect(hasCover, unlisted).toBeDefined()
      const cover = seriesTile(page, name).locator('img')
      if (hasCover === true) {
        await expect(cover, `${name} must paint its real cover`).toHaveCount(1, { timeout: 60_000 })
      } else {
        const why = `${name} has no cover and must fall back to the FR-LIB-008 placeholder`
        await expect(cover, why).toHaveCount(0)
      }
    }
  })

  expect(
    [...rendered].sort(),
    'every curated series must be reachable in the grid (§6.3 step 6.1)',
  ).toEqual([...facts.keys()].sort())
  expectCuratedLibrary(rendered, 'the grid must render exactly the curated library and no more')

  const cls = await page.evaluate(() => (window as unknown as { __shelfCls: number }).__shelfCls)
  expect(cls, 'covers arriving late must not move the grid (§6.3 step 6.1)').toBeLessThanOrEqual(
    0.01,
  )

  await shot(page, info, 'step-06-1-library-grid')
})

test('6.2 · list mode sorted by 용량 desc puts the largest series first', async ({ page }, info) => {
  const facts = await seriesFacts(page)
  const largest = [...facts.values()].reduce((a, b) => (b.total_bytes > a.total_bytes ? b : a))

  await gotoLibrary(page)
  await setView(page, 'list')

  // FR-LIB-003/004. The top-bar select rather than the column header: ui-spec
  // §7 drops the 용량 header below 1024 and every header but 시리즈명 below 768,
  // and this assertion has to hold at all four widths.
  await setSort(page, 'size')

  // Every curated series is in the list, and the walk is how that is asked.
  // `await expect(seriesTiles(page)).toHaveCount(10)` used to stand here, and it
  // is the live-locator count all over again: FR-LIB-007 virtualises list mode
  // too — `SeriesList` windows rows with `overscan: 6` — so a count against what
  // is mounted right now asserts that this project's viewport happens to be tall
  // enough for ten 45/60 px rows. That is a fact about the run, not about the
  // library, and it is the same shape as the count that condemned
  // `clearSearch()`, alive instead of dead. The union across a walk is what "the
  // list holds every curated series" means once the rows are windowed.
  // `walkLibrary` works in either view mode (`seriesTiles` is the handle both of
  // them share) and leaves the scroller back at the top.
  //
  // The count that survived that clean-up was `facts.size === 10` — the same
  // defect one tier up, since it is `GET /api/series`'s own size read back
  // against a literal that only holds in real mode. `expectCuratedLibrary`
  // asserts the *names* instead, per mode, which is what a count could never do.
  expectCuratedLibrary(facts.keys(), 'GET /api/series must list exactly the include_globs entries')
  const rendered = await walkLibrary(page)
  expect(
    [...rendered].sort(),
    'list mode must reach every curated series (§6.3 step 6.2)',
  ).toEqual([...facts.keys()].sort())

  // `defaultOrderFor('size')` is desc (ui-spec §4.5), so one interaction is the
  // whole of "sort by 용량 desc" — and "first" is read at the top of the
  // scroller, which is where the walk left it and where the shot is taken.
  await expect(seriesTiles(page).first()).toHaveAttribute('aria-label', largest.name)

  const width = page.viewportSize()?.width ?? 0
  if (width >= 1024) {
    // The full list layout: the active column names itself and its direction.
    await expect(page.locator('[data-testid="library-list-header"]')).toContainText('용량 ↓')
  }

  await shot(page, info, 'step-06-2-library-list-size-desc')
})

test('6.3 · 초성 search finds 군계 and highlights the matched syllables', async ({ page }, info) => {
  await gotoLibrary(page)
  await setView(page, 'grid')

  await page.locator('input[name="q"]').fill('ㄱㄱ')

  // C-10: the filtering is the server's (`GET /api/series?q=`), debounced 150 ms.
  await expect(seriesTiles(page)).toHaveCount(1)
  const only = seriesTiles(page).first()
  await expect(only).toHaveAttribute('aria-label', SERIES.gungye)

  // FR-LIB-006's other half: *which* span matched. `highlightParts` splits the
  // title around it and the matched run is the card title's only child element.
  const highlight = page.locator('[data-testid="series-card-text"] [title] span').first()
  await expect(highlight).toHaveText('군계')

  await shot(page, info, 'step-06-3-library-chosung-search')
})

test('6.1 (overlay) · a hovered cover still opens the series — the overlay must not eat the click', async ({
  page,
}) => {
  await gotoLibrary(page)
  await setView(page, 'grid')

  // The order matters and is the whole test. A mouse *must* hover before it can
  // click, so the state a real click arrives in is the hovered one — the state
  // in which the action overlay is painted over the cover button. Every other
  // spec reaches the series detail through `openSeries()` and would fail here
  // with a bare 120 s timeout; this one says why.
  const tile = seriesTile(page, SERIES.gungye)
  await tile.scrollIntoViewIfNeeded()
  await tile.hover()

  // The card really is in the hovered state — otherwise the click below would
  // prove nothing. The overlay is revealed by opacity alone (ui-spec §4.5), so
  // opacity is what "revealed" means; Playwright counts an `opacity: 0` element
  // as visible, and `toBeVisible()` would pass either way.
  const card = tile.locator('xpath=..')
  const overlay = card.locator('div').filter({ has: page.getByRole('button', { name: '상세' }) })
  await expect(overlay).toHaveCSS('opacity', '1')

  // …and the cover underneath it is still the element a click at the cover's
  // centre lands on. `click()` waits for its own hit-target check, so an
  // overlay that took pointer events across `inset-0` fails right here.
  await openSeries(page, SERIES.gungye)
})

test('6.1 (overlay buttons) · both overlay actions are reachable, and 상세 opens the series', async ({
  page,
}) => {
  await gotoLibrary(page)
  await setView(page, 'grid')

  // The other half of 6.1 (overlay)'s `pointer-events` gate. That test proves the
  // overlay does not take the cover's click; this proves the overlay's own
  // buttons do take theirs — a different property that breaks on its own: both ship
  // `pointer-events-none` and are turned back on only by `group-hover` /
  // `group-focus-within` (SeriesCard.tsx), so a card whose overlay is *painted*
  // is not yet a card whose buttons can be pressed.
  //
  // Only a real browser can tell those two apart, which is why this is here and
  // not in library.test.tsx. `vitest.config.ts` sets `css: false`, so Tailwind
  // never loads under jsdom, `getComputedStyle` answers `pointer-events: auto`
  // for every element, and user-event's own pointer-events guard is therefore
  // vacuous: deleting `group-hover:pointer-events-auto` from the 상세 button
  // leaves all 39 of that file's tests green (measured, not assumed). That is
  // HANDOFF §6.5 row 4, and §5.3 item 1 is the same defect having already
  // shipped once.
  const tile = seriesTile(page, SERIES.gungye)
  await tile.scrollIntoViewIfNeeded()
  await tile.hover()

  const card = tile.locator('xpath=..')
  const detail = card.getByRole('button', { name: '상세' })
  // 읽기 시작 until something has opened 군계 and 이어 읽기 afterwards. Progress
  // is persisted server-side, so which of the two this is depends on whether
  // 6.6/6.7 have already run in an earlier viewport project — a fact about the
  // suite, not about the product. Which label belongs to which state is
  // library.test.tsx's assertion; either one is the same button behind the same
  // gate, which is all this test is about.
  const primary = card.getByRole('button', { name: /이어 읽기|읽기 시작/ })

  // 6.1 (overlay)'s precondition, asserted rather than assumed for the same
  // reason it is asserted there: everything below is a statement about the *hovered* card,
  // so a card that failed to hover would let the rest pass while proving
  // nothing. The reveal is opacity alone (ui-spec §4.5) and Playwright counts an
  // `opacity: 0` element as visible, so opacity is what "revealed" has to mean.
  const overlay = card.locator('div').filter({ has: page.getByRole('button', { name: '상세' }) })
  await expect(overlay).toHaveCSS('opacity', '1')

  // Both buttons, because they carry the same gate independently and one of them
  // being live says nothing about the other.
  await expect(primary, '이어 읽기/읽기 시작 must take pointer events while hovered').toHaveCSS(
    'pointer-events',
    'auto',
  )
  await expect(detail, '상세 must take pointer events while hovered').toHaveCSS(
    'pointer-events',
    'auto',
  )

  // …and `pointer-events: auto` on the button is still one step short of "a
  // click lands on it": the scrim spans `inset-0` above the cover and the
  // buttons sit inside it, so what settles it is the browser's own hit test.
  //
  // `trial` runs Playwright's full actionability set — visible, stable,
  // *receives events*, enabled — and then still moves the real mouse to the
  // button and presses it. What `trial` changes is not that: it is passed
  // straight through as `blockAllEvents` to the hit-target interceptor, a
  // capture-phase listener that `preventDefault` / `stopPropagation` /
  // `stopImmediatePropagation`s every mousedown/mouseup/pointerdown/pointerup/
  // click before a page handler can see it. (playwright-core 1.50.1:
  // `lib/server/dom.js` `_performPointerAction` calls `action(point)`
  // unconditionally and passes `trial: !!options.trial` into
  // `setupHitTargetInterceptor`; `lib/generated/injectedScriptSource.js` is
  // where the three suppressions live. `lib/server/input.js` contains no
  // `trial` branch at all.) So the reachability is proved without `onResume`
  // running — no viewer, no progress write, shelf.ts rule 2 intact.
  //
  // That the hover survives is a property of *this* target, not of `trial`: the
  // point the mouse moves to is inside the same `.group` — the cover box, which
  // is what the overlay is a child of — so the `:hover` every assertion above
  // depends on still matches. Aim the same idiom at anything outside that box
  // (the card's title row is already outside it) and the overlay shuts under the
  // pointer before the hit test runs.
  //
  // The explicit timeout keeps a regression a 10-second failure instead of a
  // 120-second one: the card is hovered and painted by now, so there is nothing
  // legitimate left to wait for.
  await primary.click({ trial: true, timeout: 10_000 })

  // 상세 gets the real click, because a destination is what a reader is after
  // and a trial click has none. The pointer never leaves the card between the
  // hover and the press, so this is exactly the sequence a mouse performs.
  await detail.click({ timeout: 10_000 })
  await expect(page.getByRole('heading', { level: 2, name: SERIES.gungye })).toBeVisible()
  await expect(viewer(page), '상세 must not open the viewer').toHaveCount(0)

  // What that last pair does and does not catch, so nobody reads more into it:
  // `resumeSeries` only jumps into the viewer for a series that already has
  // progress (LibraryPage.tsx), so rewiring 상세 to `onResume` shows up here
  // only in the projects that run after the viewer specs have read 군계 once.
  // The unconditional destination proof is library.test.tsx's, against a
  // 34 %-read fixture; what this test owns is the reachability jsdom cannot see.
  //
  // Nothing to restore afterwards (rule 2): the only state touched is the
  // client-side route, and 그리드 is what `resetLibraryState` already set.
})
