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
 * 6.2 (keyboard) is the same kind of guard over 6.2's 그리드/리스트 toggle: its
 * ←/→ walk is the browser's radio-group behaviour, which jsdom does not have.
 * 6.2 (columns) holds the list header's tracks over the rows' — layout, which
 * jsdom does not do — and 6.1 (late 이어보기) is 6.1's layout-shift clause with
 * the `/api/continue`-after-`/api/series` order forced instead of left to the
 * server's warmth. 6.1 (header) and 6.1 (return) are the two halves of E-60
 * that a CLS sum cannot see or does not reach: the sticky section header, whose
 * movement Chrome does not report, and the trip back from the viewer. The two
 * 6.1 (⑤ …) tests hold E-60 §2-4 for a list reader: nothing grid-shaped on a
 * first visit, and the list skeleton at once when `shelf.ui` remembers it.
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

import type { Page } from '@playwright/test'

import {
  booksOf,
  clearProgress,
  expect,
  expectCuratedLibrary,
  gotoLibrary,
  openSeries,
  resetLibraryState,
  SERIES,
  seriesFacts,
  seriesId,
  seriesTile,
  seriesTiles,
  setSort,
  setView,
  shot,
  test,
  viewer,
  waitForPage,
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
  await expect(page.getByRole('heading', { level: 1, name: SERIES.gungye })).toBeVisible()
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

/**
 * HANDOFF §5.7.11 `ay` · the top bar's 그리드/리스트 toggle, by keyboard.
 *
 * Not an impl-plan assertion, so it borrows 6.2's number — 6.2 is the assertion
 * that drives this toggle by mouse — and says what it is in the parenthesis.
 *
 * `Seg` has no keydown handler (`components/ds/Seg.tsx`); its options share one
 * `name`, so the ←/→ walk is the browser's radio-group behaviour, and jsdom does
 * not implement it. The one `Seg` whose keyboard claim anything checked before
 * this was the viewer's 맞춤 (09-viewer-chrome, E-44); 06-settings and 09 now
 * carry the others, and this file the one that lives on the library screen.
 *
 * With two options the whole keyboard story is the two wraps — → off the end
 * of the group and ← off its start — so the walk takes both and one plain ←
 * between them. Every stop is checked three ways: the radio (`data-checked`,
 * and focus travelling with it, which is how a native group moves), the screen
 * (`library-list-header` present exactly in list mode — the biconditional
 * `setView` in shelf.ts documents), and FR-LIB-002's "선택 상태는 유지" — the
 * `library_view` row `useLibrarySettingsSync` writes back.
 *
 * shelf.ts rule 2: the toggle persists server-side, so the library is put back
 * on 그리드 in `finally` as well as by the next test's `beforeEach`.
 */
test('6.2 (keyboard) · ←/→ walk the 보기 방식 toggle, wrap at both ends, and persist', async ({
  page,
}) => {
  try {
    await gotoLibrary(page)
    // `beforeEach` put the server on 그리드; `setView` proves the screen agrees
    // before the walk starts from it.
    await setView(page, 'grid')

    const toggle = page.locator('[aria-label="보기 방식"]')
    const header = page.locator('[data-testid="library-list-header"]')
    const libraryView = async (): Promise<string> => {
      const response = await page.request.get('/api/settings')
      expect(response.ok(), 'GET /api/settings').toBe(true)
      return ((await response.json()) as { library_view: string }).library_view
    }

    // The premise: a click on the label focuses the radio it wraps. Without it
    // every key below goes to the document and the walk "passes" by never
    // moving. 그리드 is already checked, so the click changes nothing else.
    await toggle.locator('label[data-value="grid"]').click()
    await expect(
      toggle.locator('input[value="grid"]'),
      'FR-LIB-002: a click on the label focuses the radio, which is what makes the group one tab stop',
    ).toBeFocused()

    const walk = [
      { key: 'ArrowRight', view: 'list', why: 'ArrowRight moves 그리드 → 리스트' },
      { key: 'ArrowRight', view: 'grid', why: 'ArrowRight wraps from 리스트 back to 그리드' },
      { key: 'ArrowLeft', view: 'list', why: 'ArrowLeft wraps from 그리드 back round to 리스트' },
      { key: 'ArrowLeft', view: 'grid', why: 'ArrowLeft moves 리스트 → 그리드' },
    ] as const

    for (const step of walk) {
      await page.keyboard.press(step.key)
      const why = `FR-LIB-002: ${step.why}`
      await expect(toggle.locator(`label[data-value="${step.view}"]`), why).toHaveAttribute(
        'data-checked',
        'true',
      )
      await expect(toggle.locator(`input[value="${step.view}"]`), why).toBeFocused()
      if (step.view === 'list') {
        await expect(header, `${why} — and the library is in list mode`).toBeVisible()
      } else {
        await expect(header, `${why} — and the library is back in grid mode`).toHaveCount(0)
      }
      await expect
        .poll(libraryView, { message: `${why} — and the choice is kept (library_view)` })
        .toBe(step.view)
    }
  } finally {
    await resetLibraryState(page)
  }
})

/**
 * A guard over 6.2's list screen, like 6.2 (keyboard): the header band's
 * columns must stand over the rows' columns.
 *
 * The header and the rows are two grids with one `LIST_TEMPLATE`, in two
 * boxes — the header wrapper and the scroller — and the only thing lining them
 * up is that the boxes leave the same width for the template. They did not:
 * the wrapper's right padding was `--space-4` plus the scrollbar gutter while
 * the scroller's was `--space-2`, so the header's `1fr` track came out 8 px
 * narrower and every column right of 시리즈명 sat 8 px left of the values under
 * it. It shipped, and nothing could see it: jsdom has no layout, and the shots
 * diff each run against itself. `LIST_INSET_X` is the fix; this is the check.
 *
 * The edges are the grids' own resolved tracks (`grid-template-columns` reads
 * back in px once laid out), not the cells' text boxes — a right-aligned 권수
 * and its header would agree on their right edges with the tracks still apart
 * on the left. Every layout that has a header row is covered: `full` at 1440
 * and 1024, `compact` at 768, and `stacked` at 400, where only the cover and
 * 시리즈명 tracks are left and the gutter compensation still has to hold.
 */
test('6.2 (columns) · the list header columns stand over the row columns', async ({ page }) => {
  try {
    await gotoLibrary(page)
    await setView(page, 'list')

    const header = page.locator('[data-testid="library-list-header"]')
    const row = page.locator('[data-testid="library-scroller"] button[aria-label]').first()
    await expect(row).toBeVisible()

    const edges = (el: Element): number[] => {
      const style = getComputedStyle(el)
      const box = el.getBoundingClientRect()
      const gap = parseFloat(style.columnGap) || 0
      let x = box.left + parseFloat(style.borderLeftWidth) + parseFloat(style.paddingLeft)
      const out: number[] = []
      for (const track of style.gridTemplateColumns.split(' ')) {
        const width = parseFloat(track)
        out.push(x, x + width)
        x += width + gap
      }
      return out
    }
    const [head, body] = await Promise.all([header.evaluate(edges), row.evaluate(edges)])

    const width = page.viewportSize()?.width ?? 0
    const tracks = width >= 1024 ? 7 : width >= 768 ? 5 : 2
    expect(head.length / 2, `the header lays out ${tracks.toString()} tracks at ${width.toString()} px`).toBe(
      tracks,
    )
    expect(body.length / 2, `a row lays out ${tracks.toString()} tracks at ${width.toString()} px`).toBe(tracks)
    head.forEach((x, i) => {
      const at = `track ${Math.floor(i / 2).toString()} ${i % 2 === 0 ? 'left' : 'right'} edge`
      expect(Math.abs(x - (body[i] ?? NaN)), `${at}: header ${x.toFixed(1)} vs row ${String(body[i])}`).toBeLessThan(
        0.5,
      )
    })
  } finally {
    await resetLibraryState(page)
  }
})

/**
 * 6.1's no-layout-shift clause, with the race it lost in b2-syn1 forced.
 *
 * 6.1 measures CLS over whatever order the server happens to answer in, and on
 * a warm server `/api/continue` lands before `/api/series` and the 이어보기 band
 * is in place before the first card paints. On a just-restarted one the order
 * flips: the grid paints, the band arrives, and every card moves down by the
 * band's height — 0.135 at desktop-1440, against a budget of 0.01. That made
 * 6.1 red one round in three, which is a race, not a flake. `ContinueRow` waits
 * only for the library's own loading flag, so nothing holds the cards back.
 *
 * Here `/api/continue` is held until `/api/series` has answered, and 300 ms
 * past that, so the late order happens every run. The hold is measured from the
 * series answer rather than from the request because that is where
 * `LibraryPage`'s CONTINUE_GRACE_MS (1 s) starts: inside it the cards wait for
 * the band, past it the band is (correctly) left for the next visit — and a
 * fixed delay from the request lands on either side depending on how warm the
 * server is. The budget is 6.1's, unchanged. The band must actually appear — a
 * library with nothing in progress has no band to arrive late, and would pass
 * for that reason alone — so one volume is put in progress first when nothing
 * is, and taken out again afterwards.
 */
test('6.1 (late 이어보기) · the 이어보기 band arriving after the grid must not move it', async ({ page }) => {
  let seeded: string | null = null
  try {
    seeded = await ensureInProgress(page)

    const seriesAnswered = page.waitForResponse(
      (response) => new URL(response.url()).pathname === '/api/series',
    )
    await page.route('**/api/continue*', async (route) => {
      const response = await route.fetch()
      await seriesAnswered
      await new Promise((resolve) => setTimeout(resolve, 300))
      await route.fulfill({ response })
    })
    await gotoLibrary(page)
    await expect(page.locator('section[aria-label="이어보기"]')).toBeVisible()

    // Layout-shift entries are delivered after the frame that caused them.
    const cls = await page.evaluate(
      () =>
        new Promise<number>((resolve) => {
          requestAnimationFrame(() => {
            requestAnimationFrame(() => {
              resolve((window as unknown as { __shelfCls: number }).__shelfCls)
            })
          })
        }),
    )
    expect(cls, 'a late 이어보기 band must not move the grid (§6.3 step 6.1)').toBeLessThanOrEqual(0.01)
  } finally {
    await page.unrouteAll({ behavior: 'ignoreErrors' })
    if (seeded !== null) await clearProgress(page, seeded)
  }
})

interface ContinueEntry {
  book: { id: string }
  progress: { last_page: number; updated_at: number }
}

async function continueEntries(page: Page): Promise<ContinueEntry[]> {
  const body = (await (await page.request.get('/api/continue')).json()) as { items: ContinueEntry[] }
  return body.items
}

/** The first openable volume of 군계. */
async function gungyeVolume(page: Page): Promise<{ sid: string; bid: string }> {
  const sid = await seriesId(page, SERIES.gungye)
  const first = (await booksOf(page, sid)).find((book) => book.status === 'ok')
  expect(first, '군계 must have an openable volume').toBeDefined()
  return { sid, bid: first?.id ?? '' }
}

/**
 * Makes sure the 이어보기 band has something to show: a band that never appears
 * cannot arrive late, and a check over it passes for that reason alone. Returns
 * the volume it put in progress, for the caller's `finally`, or `null` when
 * something already was.
 */
async function ensureInProgress(page: Page): Promise<string | null> {
  if ((await continueEntries(page)).length > 0) return null
  const { bid } = await gungyeVolume(page)
  await page.request.put(`/api/books/${bid}/progress`, { data: { page: 1 } })
  await expect
    .poll(async () => (await continueEntries(page)).length, {
      message: 'FR-LIB-010: the seeded volume must reach /api/continue',
    })
    .toBeGreaterThan(0)
  return bid
}

/**
 * 6.1's late-이어보기 order seen from the one element CLS cannot see.
 *
 * The section header is `position: sticky`, and Chrome's Layout Instability API
 * does not report a sticky element that the band above pushes down: before
 * E-60 the same header node was drawn over the skeleton at y 63 and then moved
 * to y 326 when the band landed, and `layout-shift` counted 0. So the frames
 * are sampled directly — every animation frame from the first, the header's
 * top and whether the skeleton is up — and two things are asserted: the header
 * is never drawn over the skeleton (E-60), and the first y it is drawn at is
 * the y it settles at, to the pixel, as one node. `/api/series` is held 300 ms
 * so that there is a skeleton frame to be drawn over; the samples have to
 * contain one, or the run proved nothing about it.
 */
test('6.1 (header) · the section header is drawn once, where it stays, and never over the skeleton', async ({
  page,
}) => {
  let seeded: string | null = null
  try {
    seeded = await ensureInProgress(page)
    await page.addInitScript(`
      window.__shelfHeader = []
      const sample = () => {
        const skeleton = document.querySelector('[data-testid="library-skeleton"]') !== null
        const header = [...document.querySelectorAll('div.sticky')].find((el) => /shelf/i.test(el.textContent || ''))
        if (header && !header.__shelfId) header.__shelfId = (window.__shelfHeaderIds = (window.__shelfHeaderIds || 0) + 1)
        window.__shelfHeader.push({
          skeleton,
          y: header ? Math.round(header.getBoundingClientRect().top) : null,
          id: header ? header.__shelfId : null,
        })
        if (!window.__shelfHeaderStop) requestAnimationFrame(sample)
      }
      requestAnimationFrame(sample)
    `)
    await page.route(
      (url) => url.pathname === '/api/series',
      async (route) => {
        const response = await route.fetch()
        await new Promise((resolve) => setTimeout(resolve, 300))
        await route.fulfill({ response })
      },
    )
    await gotoLibrary(page)
    await expect(page.locator('section[aria-label="이어보기"]')).toBeVisible()
    // Past CONTINUE_GRACE_MS, so a band that was going to move the header has.
    await page.waitForTimeout(1_500)
    const frames = await page.evaluate(() => {
      const w = window as unknown as {
        __shelfHeaderStop: boolean
        __shelfHeader: { skeleton: boolean; y: number | null; id: number | null }[]
      }
      w.__shelfHeaderStop = true
      return w.__shelfHeader
    })

    expect(
      frames.some((f) => f.skeleton),
      'the run must have sampled the skeleton (the 300 ms hold on /api/series)',
    ).toBe(true)
    expect(
      frames.filter((f) => f.skeleton && f.y !== null).length,
      'E-60: the section header is not drawn over the skeleton',
    ).toBe(0)
    const drawn = frames.filter((f) => f.y !== null)
    expect(drawn.length, 'the section header must be drawn').toBeGreaterThan(0)
    expect(
      [...new Set(drawn.map((f) => f.y))],
      'E-60: the header is first drawn at the y it settles at (sticky, so CLS does not see this)',
    ).toEqual([drawn[drawn.length - 1]?.y])
    expect([...new Set(drawn.map((f) => f.id))], 'and it is one node throughout').toHaveLength(1)
  } finally {
    await page.unrouteAll({ behavior: 'ignoreErrors' })
    if (seeded !== null) await clearProgress(page, seeded)
  }
})

/**
 * The other path to the shift E-60 closed: coming back from the viewer.
 *
 * With nothing in progress the library caches an empty 이어보기 list. Reading a
 * volume writes progress, and on the way back the cached cards paint at once
 * while the refetch brings a band in above them — before E-60 that was 0.134 at
 * 1440 with no delay at all, every time. The band must arrive (it is the point),
 * and the layout shift counted from `history.back()` on is 6.1's budget.
 *
 * It empties 이어보기 to get there, so what was in progress is put back in
 * `finally`, oldest first, so the shelf's `updated_at` order survives.
 */
test('6.1 (return) · coming back from a volume, the new 이어보기 band does not move the grid', async ({
  page,
}) => {
  const before = await continueEntries(page)
  const { sid, bid } = await gungyeVolume(page)
  try {
    for (const entry of before) {
      await page.request.delete(`/api/books/${entry.book.id}/progress`)
    }
    await expect.poll(async () => (await continueEntries(page)).length).toBe(0)

    await gotoLibrary(page)
    await expect(seriesTiles(page).first()).toBeVisible()
    await expect(page.locator('section[aria-label="이어보기"]')).toHaveCount(0)

    // Into the viewer without a reload, so the library's cache is what comes back.
    const saved = page.waitForResponse(
      (response) =>
        response.url().includes(`/api/books/${bid}/progress`) && response.request().method() === 'PUT',
    )
    await page.evaluate(
      ({ s, b }) => {
        history.pushState(null, '', `/series/${s}/books/${b}?page=2`)
        window.dispatchEvent(new PopStateEvent('popstate'))
      },
      { s: sid, b: bid },
    )
    await expect(viewer(page)).toBeVisible()
    await waitForPage(page)
    await saved
    await expect
      .poll(async () => (await continueEntries(page)).length, {
        message: 'FR-LIB-010: the volume just read must reach /api/continue',
      })
      .toBeGreaterThan(0)

    // Counted from `history.back()` on, by an observer of its own: the page's
    // running CLS also holds whatever the viewer did while its pages decoded,
    // and a difference of two readings of it charged that to the library.
    await page.evaluate(() => {
      const w = window as unknown as { __shelfReturnCls: number }
      w.__shelfReturnCls = 0
      const t0 = performance.now()
      new PerformanceObserver((list) => {
        for (const entry of list.getEntries() as (PerformanceEntry & { value: number; hadRecentInput: boolean })[]) {
          if (!entry.hadRecentInput && entry.startTime >= t0) w.__shelfReturnCls += entry.value
        }
      }).observe({ type: 'layout-shift' })
      history.back()
    })
    await expect(page.locator('section[aria-label="이어보기"]')).toBeVisible()
    await expect(seriesTiles(page).first()).toBeVisible()
    const cls = await page.evaluate(
      () =>
        new Promise<number>((resolve) => {
          requestAnimationFrame(() => {
            requestAnimationFrame(() => {
              resolve((window as unknown as { __shelfReturnCls: number }).__shelfReturnCls)
            })
          })
        }),
    )
    expect(cls, 'E-60: the band that the read put there must not move the grid on the way back').toBeLessThanOrEqual(
      0.01,
    )
  } finally {
    await clearProgress(page, bid)
    for (const entry of [...before].sort((a, b) => a.progress.updated_at - b.progress.updated_at)) {
      await page.request.put(`/api/books/${entry.book.id}/progress`, {
        data: { page: entry.progress.last_page },
      })
    }
  }
})

/**
 * Item ⑤, E-60 §2-4: what a list reader's library looks like before it knows
 * it is a list.
 *
 * A browser with no `shelf.ui` starts the store on `grid`, and `library_view`
 * reaches it only once `/api/settings` has answered. Before ⑤ the library drew
 * whatever the store held in the meantime: the grid skeleton, swapped for the
 * list one when the settings landed (CLS 0.012–0.039), and with the settings
 * late, grid *cards* revealed and then swapped for list rows. Now nothing
 * view-shaped is drawn until the view is known — remembered in `shelf.ui`,
 * taken from the server, or picked — and a remembered one is known at once.
 *
 * So the frames are sampled from the first one, and each is classed by the
 * shape it draws. `/api/series` is held 300 ms in every run so that there is a
 * skeleton phase to draw the wrong shape in. Each Playwright test is a new
 * browser context, which is what an empty localStorage — a first visit — is.
 */
const LIBRARY_SHAPE_INIT = `
  window.__shelfShapes = []
  const sample = () => {
    const skeleton = document.querySelector('[data-testid="library-skeleton"]')
    const scroller = document.querySelector('[data-testid="library-scroller"]')
    const loading = document.querySelector('[data-testid="library-loading"]')
    let shape = 'none'
    if (skeleton) {
      shape = skeleton.querySelector('[data-testid="library-skeleton-header"]') ? 'list-skeleton' : 'grid-skeleton'
    } else if (scroller) {
      shape = document.querySelector('[data-testid="library-list-header"]') ? 'list' : 'grid'
    } else if (loading) {
      shape = 'blank'
    }
    const last = window.__shelfShapes[window.__shelfShapes.length - 1]
    if (!last || last.shape !== shape) window.__shelfShapes.push({ t: Math.round(performance.now()), shape })
    if (!window.__shelfShapesStop) requestAnimationFrame(sample)
  }
  requestAnimationFrame(sample)
`

type LibraryShape = 'none' | 'blank' | 'grid-skeleton' | 'list-skeleton' | 'grid' | 'list'

async function stopShapes(page: Page): Promise<{ t: number; shape: LibraryShape }[]> {
  return page.evaluate(() => {
    const w = window as unknown as {
      __shelfShapesStop: boolean
      __shelfShapes: { t: number; shape: LibraryShape }[]
    }
    w.__shelfShapesStop = true
    return w.__shelfShapes
  })
}

/**
 * Holds every answer to `pathname` until `ms` after its request, so the
 * server's own time is inside the hold rather than added to it.
 */
async function holdAnswers(page: Page, pathname: string, ms: number): Promise<void> {
  await page.route(
    (url) => url.pathname === pathname,
    async (route) => {
      const t0 = Date.now()
      const response = await route.fetch()
      await new Promise((resolve) => setTimeout(resolve, Math.max(0, ms - (Date.now() - t0))))
      await route.fulfill({ response })
    },
  )
}

async function putLibraryView(page: Page, view: 'grid' | 'list'): Promise<void> {
  const response = await page.request.put('/api/settings', { data: { library_view: view } })
  expect(response.ok(), 'PUT /api/settings library_view').toBe(true)
}

// The settings held 0 ms and 500 ms: both inside CONTINUE_GRACE_MS (1 s from
// mount), past which the library settles on the store's own view by design.
for (const settingsHold of [0, 500]) {
  test(`6.1 (⑤ first visit, settings +${String(settingsHold)} ms) · a list reader is never shown the grid`, async ({
    page,
  }) => {
    try {
      await putLibraryView(page, 'list')
      await page.addInitScript(LIBRARY_SHAPE_INIT)
      await holdAnswers(page, '/api/series', 300)
      if (settingsHold > 0) await holdAnswers(page, '/api/settings', settingsHold)

      await page.goto('/')
      await expect(page.locator('[data-testid="library-list-header"]')).toBeVisible()
      // Past the grace, so a late swap would have happened by now.
      await page.waitForTimeout(1_500)
      const shapes = await stopShapes(page)
      const drawn = shapes.map((s) => s.shape)

      expect(
        drawn.filter((shape) => shape === 'grid-skeleton' || shape === 'grid'),
        `⑤: a list reader's first visit draws nothing grid-shaped (frames: ${JSON.stringify(shapes)})`,
      ).toEqual([])
      expect(drawn.at(-1), 'and it ends in the list').toBe('list')
      const cls = await page.evaluate(() => (window as unknown as { __shelfCls: number }).__shelfCls)
      expect(cls, '⑤: the first visit settles with no layout shift (§6.3 step 6.1)').toBeLessThanOrEqual(0.01)
    } finally {
      await page.unrouteAll({ behavior: 'ignoreErrors' })
      await resetLibraryState(page)
    }
  })
}

/**
 * ⑤'s other half, the regression its first fix caused: a reader whose
 * `shelf.ui` remembers `list` was made to wait for `/api/settings` with an
 * empty library (1 045 ms with the settings held). Their view is known before
 * any request, so the loading area must hold the list skeleton from the first
 * frame it is drawn in. The settings are held past the grace on purpose: a
 * build that ignores the remembered view settles only when the grace runs out,
 * and shows a blank loading area until then.
 */
test('6.1 (⑤ return visit) · a remembered list view is the first thing drawn', async ({ page }) => {
  try {
    await putLibraryView(page, 'list')
    await page.addInitScript(() => {
      localStorage.setItem('shelf.ui', JSON.stringify({ state: { view: 'list' }, version: 0 }))
    })
    await page.addInitScript(LIBRARY_SHAPE_INIT)
    await holdAnswers(page, '/api/series', 300)
    let listBeforeSettings: boolean | null = null
    await page.route(
      (url) => url.pathname === '/api/settings',
      async (route) => {
        const t0 = Date.now()
        const response = await route.fetch()
        await new Promise((resolve) => setTimeout(resolve, Math.max(0, 1_500 - (Date.now() - t0))))
        listBeforeSettings = await page.evaluate(() =>
          (window as unknown as { __shelfShapes: { shape: string }[] }).__shelfShapes.some(
            (s) => s.shape === 'list-skeleton' || s.shape === 'list',
          ),
        )
        await route.fulfill({ response })
      },
    )

    await page.goto('/')
    await expect(page.locator('[data-testid="library-list-header"]')).toBeVisible()
    await expect.poll(() => listBeforeSettings, { message: 'the held /api/settings must answer' }).not.toBeNull()
    const shapes = await stopShapes(page)
    const drawn = shapes.map((s) => s.shape)

    expect(
      drawn.filter((shape) => shape === 'blank' || shape === 'grid-skeleton' || shape === 'grid'),
      `⑤: a remembered list view leaves no blank or grid frame (frames: ${JSON.stringify(shapes)})`,
    ).toEqual([])
    expect(drawn, 'the run must have drawn the list skeleton (the 300 ms hold on /api/series)').toContain(
      'list-skeleton',
    )
    expect(listBeforeSettings, 'and drew it while /api/settings was still in flight').toBe(true)
  } finally {
    await page.unrouteAll({ behavior: 'ignoreErrors' })
    await resetLibraryState(page)
  }
})
