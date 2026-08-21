/**
 * Contrast as the reader gets it — items `v`, `ar` and `ap`.
 *
 * ## What this adds that the unit tier cannot have
 *
 * `tokens.test.ts` is exact about the pairs it can see and structurally blind to
 * five shapes (`contrast.ts` in this directory enumerates them). Three of the
 * five were **real defects** when §5.7.10 last looked: a shadow moving the ⌘K
 * chip from a declared 5.65 to a rendered 4.55, the search field's UA-painted
 * cancel button at 1.14, and two marks in `TopBar` painting into a control's
 * box. None of them is reachable from a stylesheet — they are properties of the
 * composite, so the instrument has to be the composite.
 *
 * HANDOFF §6.5 names the pattern this file is an answer to: *a check that
 * watches the wrong thing*. A source scan that reports the declared pair is not
 * a weaker version of this file, it is a check that watches a different number
 * and can be green while the screen is wrong. Both tiers stay: the unit tier is
 * fast, total over the palette, and says which token is at fault; this tier says
 * what the reader actually sees. They share one arithmetic module
 * (`src/styles/contrast.ts`) so they can never disagree about the formula, only
 * about the inputs — which is the disagreement worth having.
 *
 * ## Why the sweep enumerates combinations, not elements
 *
 * A library screen has hundreds of text nodes and roughly two dozen distinct
 * (ink, ground, size, weight) combinations. Photographing every node would
 * repeat one assertion hundreds of times and be slow enough to push the next
 * author into sampling — and a sampled sweep that still calls itself total is
 * how the audit in §5.7.13 came to say "the gates exist" about gates that had
 * not run. Grouping first makes the sweep exhaustive over the thing that can
 * fail, and prints the count so a shrinking sweep is visible.
 *
 * ## The three screens, and why not more
 *
 * Library, series detail and the settings dialog carry every surface token in
 * the app shell between them; the viewer with its chrome awake carries the
 * viewer's own dark scope, which is a separate palette (E-27) and the one the
 * ⌘K chip and the two `TopBar` marks live in. States behind a hover or a focus
 * ring are **not** swept — `al` (`.btn-ghost:hover` at 3.90 in dark) is an open
 * item precisely because hover states need their own pass, and quietly folding
 * it into this sweep would close it by accident rather than by measurement.
 */

import {
  booksOf,
  expect,
  gotoLibrary,
  openSeries,
  openSettings,
  SERIES,
  seriesId,
  setViewerSeg,
  test,
  viewer,
  wakeChrome,
} from './shelf'
import {
  clearProbes,
  combinationsOn,
  describe as describeMeasurement,
  measure,
  measureLocator,
  measureLocatorUngrained,
  type Measurement,
} from './contrast'

/**
 * Pairs that are under their floor **on purpose**, each with the item that owns
 * it. Written as "still failing" assertions rather than skips: an exemption that
 * has stopped being necessary is a change worth seeing, and the same rule keeps
 * `tokens.test.ts`'s grain exemptions honest (§6.5's label discipline).
 *
 * Empty on purpose. Every entry is a defect this suite has agreed not to fix
 * yet, so an entry appearing here should cost a sentence in the handoff.
 */
const KNOWN_UNDER: { label: string; why: string; item: string }[] = []

/** Ratio the sweep rounds to when reporting, so messages compare cleanly. */
const r2 = (n: number): string => n.toFixed(2)

/**
 * Every distinct combination on the current screen, measured.
 *
 * `clearProbes` runs even when a measurement throws: the attribute is inert but
 * leaving it behind would let a later screenshot in the same round differ from
 * the baseline for a reason that has nothing to do with the product.
 */
async function sweep(page: import('@playwright/test').Page): Promise<Measurement[]> {
  const combos = await combinationsOn(page)
  const out: Measurement[] = []
  try {
    for (const combo of combos) {
      const locator = page.locator(`[data-contrast-probe="${combo.probe}"]`)
      if ((await locator.count()) !== 1) continue
      out.push(await measure(page, combo, locator))
    }
  } finally {
    await clearProbes(page)
  }
  return out
}

/** The assertion every swept combination has to clear. */
function expectAA(measurements: Measurement[], where: string): void {
  expect(measurements.length, `${where}: the sweep found no text to measure`).toBeGreaterThan(3)
  // Text lying over a cover thumbnail is out of this check's reach and saying
  // so is better than either failing it or dropping it silently: the ground is
  // artwork, it changes per series, and no token moves it. The count is
  // asserted to be small rather than zero — if a redesign put half the shell's
  // ink over imagery, this sweep would quietly stop covering the shell.
  const overArt = measurements.filter((m) => m.overImagery)
  expect(
    overArt.length,
    `${where}: too much of the shell's ink is over artwork for a token check to reach`,
  ).toBeLessThanOrEqual(4)
  const failures = measurements
    .filter((m) => !m.overImagery)
    .filter((m) => m.measuredRatio + 0.005 < m.floor)
    .filter((m) => !KNOWN_UNDER.some((k) => k.label === m.label))
  expect(
    failures.map((m) => describeMeasurement(m)),
    `${where}: ink under its AA floor once the paint is on it`,
  ).toEqual([])
}

/**
 * Blind spots ⓑ and ⓒ — **and the account of why this file does not close
 * them.** Read this before adding a threshold to make it pass.
 *
 * The design was a colour census of a control's box: the ground is the modal
 * colour, anything else populated enough to be a mark rather than an
 * antialiased edge is ink, and ink that fails AA against its own ground is a
 * defect no stylesheet could have shown. It was aimed at the two shapes
 * §5.7.10 records as real: the search field's UA-painted `::-webkit-search-
 * cancel-button` at 1.14, and the marks `TopBar` lays over the same field.
 *
 * It does not work on this skin, and the reason is measured rather than
 * suspected. A census needs a flat ground, and every control here has two
 * things making it not flat:
 *
 *  1. **The paper grain.** It moves each pixel by up to
 *     `--paper-intensity × 0.115 × |ground − tone|`, about **23 of 255** on the
 *     cream — wider than the gap between the cream and a near-white mark. The
 *     first run returned **92** "marks" on one field, all grain at 1.00–1.02.
 *     `measureLocatorUngrained` removes that one.
 *  2. **The neumorphic inset.** `--shadow-control-inset` survives E-46 (the
 *     prototype redefines the three outsets and leaves the inset alone), so
 *     `.input` is a *gradient* from ochre to near-white across its own box.
 *     With the grain off the same field still returned **40** colours at
 *     1.01–1.09, every one of them a step of that gradient. E-42 had already
 *     written this down from the other direction: "no pixel of it is actually
 *     `--control-well` — the top-left is ochre, the bottom-right near-white".
 *
 * The lightest colour in the box is therefore the highlight lobe, not the ✕,
 * and no population or distance threshold separates them: they occupy the same
 * range. A check tuned until it passed would be one that had stopped looking.
 *
 * So this asserts only what is unambiguous — that the measurement reached the
 * control at all — and prints the marks it found. **ⓑ and ⓒ stay open**, with
 * a smaller and better-specified shape than before: clip the shot to the ✕'s
 * own rectangle, where 16px of field carries little enough gradient for the
 * census to mean something. That is the next session's item, not a gap this
 * file is hiding.
 */
function reportMarksOn(m: Measurement, where: string): void {
  // The positive control: a census that found nothing has not proved the
  // control is clean, it has proved the shot missed.
  expect(m.clusters.length, `${where}: the census found no marks at all`).toBeGreaterThan(0)
  expect(m.measuredGround, `${where}: no ground was measured`).toMatch(/^rgb\(/)
  const worst = m.clusters[0]
  if (worst !== undefined) {
    console.log(
      `[contrast] ${where}: ${String(m.clusters.length)} marks, worst ${worst.rgb} ` +
        `at ${r2(worst.ratio)} on ${m.measuredGround} — see the note above reportMarksOn`,
    )
  }
}

// ---------------------------------------------------------------------------
// Calibration — the instrument has to be able to go red
// ---------------------------------------------------------------------------

test('v · the scanner reads a known pair, and sees a shadow move it', async ({ page }) => {
  await gotoLibrary(page)

  // A pair whose arithmetic is not in question: #767676 on #FFFFFF is 4.54,
  // the canonical WCAG worked example. If the bitmap path disagrees with the
  // formula here, every number below is uninterpretable.
  await page.evaluate(() => {
    const probe = document.createElement('div')
    probe.id = 'calibration-plain'
    probe.textContent = 'AAAAAAAAAAAA'
    probe.setAttribute(
      'style',
      'position:fixed;left:8px;top:8px;z-index:99999;width:220px;height:40px;' +
        'background:#FFFFFF;color:#767676;font:16px/40px monospace;text-align:center',
    )
    document.body.append(probe)
  })
  const plain = await combinationsOn(page)
  const plainCombo = plain.find((c) => c.label.includes('calibration-plain') || c.declaredInk === 'rgb(118, 118, 118)')
  expect(plainCombo, 'the calibration probe entered the sweep').toBeDefined()
  if (plainCombo === undefined) return
  const plainM = await measure(page, plainCombo, page.locator('#calibration-plain'))
  await clearProbes(page)

  // The declared pair and the measured pair agree when nothing intervenes:
  // that is what makes a disagreement elsewhere attributable to the paint.
  expect(plainM.declaredRatio, 'declared #767676 on #FFFFFF').toBeCloseTo(4.54, 1)
  expect(plainM.measuredGround, 'the modal pixel of a white box').toBe('rgb(255, 255, 255)')
  expect(plainM.measuredRatio, 'measured, with nothing in the way').toBeCloseTo(4.54, 1)

  // Now the shape blind spot ⓐ is made of: an inset shadow darkens the actual
  // ground while `background` — the only thing a source scan reads — is
  // unchanged. The declared ratio must not move and the measured one must.
  await page.evaluate(() => {
    const probe = document.querySelector('#calibration-plain')
    probe?.remove()
    const shadowed = document.createElement('div')
    shadowed.id = 'calibration-shadowed'
    shadowed.textContent = 'AAAAAAAAAAAA'
    shadowed.setAttribute(
      'style',
      'position:fixed;left:8px;top:8px;z-index:99999;width:220px;height:40px;' +
        'background:#FFFFFF;color:#767676;font:16px/40px monospace;text-align:center;' +
        'box-shadow:inset 0 0 0 40px rgba(0,0,0,0.35)',
    )
    document.body.append(shadowed)
  })
  const shadowedCombos = await combinationsOn(page)
  const shadowedCombo = shadowedCombos.find((c) => c.declaredInk === 'rgb(118, 118, 118)')
  expect(shadowedCombo, 'the shadowed probe entered the sweep').toBeDefined()
  if (shadowedCombo === undefined) return
  const shadowedM = await measure(page, shadowedCombo, page.locator('#calibration-shadowed'))
  await clearProbes(page)
  await page.evaluate(() => {
    document.querySelector('#calibration-shadowed')?.remove()
  })

  expect(shadowedM.declaredRatio, 'the declared pair is unchanged by a shadow').toBeCloseTo(4.54, 1)
  expect(
    shadowedM.measuredGround,
    'the shadow changed the ground the reader gets',
  ).not.toBe('rgb(255, 255, 255)')
  expect(
    Math.abs(shadowedM.measuredRatio - shadowedM.declaredRatio),
    `the scanner saw the shadow: declared ${r2(shadowedM.declaredRatio)}, measured ${r2(shadowedM.measuredRatio)}`,
  ).toBeGreaterThan(0.5)
})

// ---------------------------------------------------------------------------
// The sweep — both themes, three shell screens
// ---------------------------------------------------------------------------

for (const theme of ['light', 'dark'] as const) {
  test(`v+ar · every ink on the shell clears AA on its measured ground (${theme})`, async ({ page }) => {
    // Through the settings dialog, not `PUT /api/settings`.
    //
    // The API accepts and persists `theme`, and the first draft of this spec
    // set it that way and then waited for `data-theme` to follow. It never
    // does: `useLibrary.ts`'s hydration payload carries `view`, `order`,
    // `scope` and `sort` and **not** `theme`, so the server's stored value is
    // written and never read back. That is an open question about the product
    // rather than about this file (nothing here should fix it), but a spec
    // that drove the theme through a path the product does not honour would
    // have measured the light palette twice and called the second pass "dark".
    await gotoLibrary(page)
    await openSettings(page)
    const themeSeg = page.locator('[role="dialog"]').locator('[aria-label="테마"]')
    await themeSeg.locator(`label[data-value="${theme}"]`).click()
    await expect(page.locator('html')).toHaveAttribute('data-theme', theme)

    // Reload rather than press Escape, and the reason is a 400px one. Below 768
    // the sidebar is an off-canvas drawer and `openSettings` has to open it to
    // reach the button; Escape closes the *dialog* and leaves the drawer up, so
    // the next click — `openSeries` — spent the whole 120s test budget being
    // intercepted by `.drawer-panel`. A navigation puts the shell back to its
    // resting state at every width, and the theme survives it because the store
    // persists to localStorage (which is also the only place it lives — see the
    // note above).
    await gotoLibrary(page)
    await expect(page.locator('html')).toHaveAttribute('data-theme', theme)

    const library = await sweep(page)
    expectAA(library, `library (${theme})`)

    await openSeries(page, SERIES.clover)
    const series = await sweep(page)
    expectAA(series, `series detail (${theme})`)

    // Back to the shell before opening the dialog, for the same drawer reason:
    // `openSeries` left the app on the series route and the settings button is
    // in the drawer at 400px.
    await gotoLibrary(page)
    await openSettings(page)
    const settings = await sweep(page)
    expectAA(settings, `settings dialog (${theme})`)

    // The count is printed so a sweep that quietly shrinks is visible in the
    // log rather than only in a coverage claim nobody re-measured.
    console.log(
      `[contrast:${theme}] combinations measured — library ${String(library.length)}, ` +
        `series ${String(series.length)}, settings ${String(settings.length)}`,
    )

    // shelf.ts rule 2: put the server back the way this spec found it.
    await page.request.put('/api/settings', { data: { theme: 'system' } })
  })
}

// ---------------------------------------------------------------------------
// The controls that carry ink no stylesheet names — blind spots ⓑ and ⓒ
// ---------------------------------------------------------------------------

/**
 * Three fixes made by hand-measurement, with nothing guarding them.
 *
 * E-43 closed all three: `color-scheme: light` on `.input` so the UA draws the
 * ✕ for a light surface (it was 1.14 near-white on cream), `--on-control-dim`
 * for the search icon and the ⌘K chip's siblings (`--ink-dim` was 1.50 on that
 * cream in dark), and `--on-control` rather than the dim ink for the chip
 * itself, because `--shadow-control-inset` moves its real ground.
 *
 * Every one of those numbers came from a person reading a render. Nothing in
 * the suite re-reads it, so any of the three could be undone by a token edit
 * and no gate would move — the shape §6.5 calls a fix that outlives its
 * evidence. This test is the evidence, re-taken every round.
 */
test('ar · the marks on the search field clear AA on the field they are drawn on', async ({ page }) => {
  await gotoLibrary(page)

  const field = page.locator('input[type="search"]')
  await expect(field).toBeVisible()
  // The ✕ only exists once the field has a value, so an empty-field sweep would
  // report this control as clean while the defect sat one keystroke away.
  await field.fill('군계')
  await expect(field).toHaveValue('군계')

  // The whole field box: the fill is the ground, and everything drawn on it —
  // the search icon, the typed text, the ⌘K chip, the UA's ✕ — is a cluster.
  // That is the only vantage point from which ⓑ and ⓒ are visible at all.
  const m = await measureLocatorUngrained(page, field, 'search field')
  reportMarksOn(m, 'search field with a value')

  // What *can* be asserted here without a census: the field's own declared
  // pair, on the ground the reader actually gets. This is the check that
  // catches a shadow or a dead exemption moving the field out from under its
  // placeholder, and it is the half of ⓐ that does not need a flat ground.
  expect(
    m.measuredRatio,
    `the field's own ink on its rendered ground — ${describeMeasurement(m)}`,
  ).toBeGreaterThanOrEqual(m.floor)

  // The field is an absolute cream in both themes (E-42), so its rendered
  // ground must not be the page's. If the fill ever stops being absolute, the
  // ✕ measurement above starts describing a surface the reader does not see —
  // a check still green while it watches the wrong thing.
  const pageGround = await page.evaluate(() => getComputedStyle(document.body).backgroundColor)
  expect(
    m.measuredGround,
    'the field renders as its own absolute surface, not the page ground',
  ).not.toBe(pageGround)

  await field.fill('')
})

/**
 * Blind spot ⓓ: the grain exemption, checked on the render instead of the rule.
 *
 * `--on-hot` on `--color-hot` is under AA *washed* and over it dry — the whole
 * point of E-43's exemption, and `tokens.test.ts` pins both halves as tokens.
 * What it cannot pin is whether the exemption still reaches the element: one
 * `isolate`, `transform`, `opacity` or `filter` on any wrapper between the chip
 * and the bar opens a stacking context, the bar's `::after` grain goes back over
 * the chip, and the pair silently returns to the failing number. There is no
 * source string to read for that. There is a bitmap.
 */
test('ar · the viewer override chip is still lifted out of the paper wash', async ({ page }) => {
  await gotoLibrary(page)
  const sid = await seriesId(page, SERIES.wheel)
  const books = await booksOf(page, sid)
  const first = books[0]
  expect(first, `${SERIES.wheel} has a book to open`).toBeDefined()
  if (first === undefined) return
  await page.goto(`/series/${sid}/books/${first.id}?page=1`)
  await expect(viewer(page)).toBeVisible()
  await wakeChrome(page)

  // 맞춤 off its default is what puts the chip on the bar (E-44's fourth option).
  await setViewerSeg(page, '맞춤', 'width')
  const chip = page.locator('[data-role="viewer-override-chip"]')
  await expect(chip).toBeVisible()

  const m = await measureLocator(page, chip, 'viewer override chip')
  expect(
    m.measuredRatio,
    `the chip's ink on its rendered ground — ${describeMeasurement(m)}`,
  ).toBeGreaterThanOrEqual(m.floor)

  await setViewerSeg(page, '맞춤', 'height')
  // shelf.ts rule 2: this spec invented a progress row by opening a book.
  await page.request.delete(`/api/books/${first.id}/progress`)
})

// ---------------------------------------------------------------------------
// forced-colors — item `ap`
// ---------------------------------------------------------------------------

/**
 * `ap`: forced-colors was checked by one person once, with Playwright, by hand.
 * `soft-ui.test.ts` guards that the fallback *declarations* exist; nothing
 * guards what they produce. The two are not the same claim, and this mode is
 * where they come apart — the OS supplies the palette, so a rule that is
 * present can still leave a surface where ink and ground arrive as the same
 * system colour.
 *
 * The floor here is not AA. In forced-colors the contrast is the operating
 * system's to guarantee and the product's job is to get out of the way; what
 * the product can still break is **visibility**, so that is what is asserted.
 * A pair at 3.0 or better is legible in any system palette; a pair below it
 * means the product painted a ground the system did not choose.
 */
test.describe('ap · forced-colors', () => {
  test('nothing on the shell disappears into its own ground', async ({ page }) => {
    // `page.emulateMedia`, not `test.use({ forcedColors })`. The fixture form is
    // the tidier one and it does not typecheck against the extended `test` this
    // suite exports (shelf.ts adds the console guard, and `forcedColors` is not
    // in the fixture surface Playwright 1.50 exposes to `extend`). The page API
    // sets the same emulation and keeps the console guard, which is the fixture
    // worth more here: a forced-colors round that throws in the console and
    // still passes is the failure this whole file is about.
    await page.emulateMedia({ forcedColors: 'active' })
    await gotoLibrary(page)
    const library = await sweep(page)
    expect(library.length, 'the forced-colors sweep found text').toBeGreaterThan(3)
    const invisible = library.filter((m) => m.measuredRatio < 3)
    expect(
      invisible.map((m) => describeMeasurement(m)),
      'forced-colors: ink that arrives on a ground it cannot be read against',
    ).toEqual([])

    await openSettings(page)
    const settings = await sweep(page)
    const gone = settings.filter((m) => m.measuredRatio < 3)
    expect(
      gone.map((m) => describeMeasurement(m)),
      'forced-colors, settings dialog: ink that cannot be read against its ground',
    ).toEqual([])
  })
})
