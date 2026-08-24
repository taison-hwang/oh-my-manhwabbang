/**
 * Shared fixtures and page helpers for the browser half of impl-plan §6.3.
 *
 * The specs in this directory are step 6 of that section — the twelve browser
 * assertions — run by `scripts/e2e.sh` step 11 against the **already running**
 * server it started in step 4, i.e. the real collection narrowed to fifteen
 * series by `scan.include_globs` (amendment A-3). Nothing here starts or
 * configures a server; `PLAYWRIGHT_BASE_URL` points at the one that is up.
 *
 * Three rules travel with this file.
 *
 *  1. **No hard-coded page or volume counts.** The identical suite has to pass
 *     against `scripts/e2e.sh --synthetic`, whose fixture tree reproduces the
 *     *shapes* of §6.3 but not their sizes (D-49). Every assertion is therefore
 *     relative — "advanced five pages", "the largest series is first" — or reads
 *     its expectation from the API first.
 *
 *     What the rule does **not** say, and what a wrong sentence here cost: the
 *     synthetic tree does not carry the same fifteen *names*. D-49, as extended
 *     by D-71, adds an encrypted ZIP, a ZIP64 archive and a solid RAR the real
 *     collection has no sample of, so the synthetic library holds **eighteen**
 *     series — and this docblock claimed the two libraries were the same size
 *     until 2026-07-29, which is why `toBe(CURATED_SERIES_COUNT)` stood in
 *     01-library.spec.ts for three sessions and `make e2e-synthetic` had never
 *     been run to contradict it. The library *set* is not relative and must not
 *     be made so; it is mode-parameterised instead, below.
 *
 *  2. **Every test leaves the server as it found it.** `library_view`,
 *     `library_sort`, `theme` and the per-book `reading_direction` are all
 *     *persisted server-side* (arch §7.8, §7.6), so a test that switches to
 *     list mode changes what the next test — and the next viewport project —
 *     loads into. Helpers therefore set the state they need explicitly rather
 *     than assuming a default, and the viewer specs restore reading direction.
 *     `playwright.config.ts` runs one worker for the same reason.
 *
 *  3. **A console error fails the test.** NFR-CMP-001 (§7.3) is "loads the SPA
 *     without console errors", which is only a gate if something checks it.
 */

import { mkdir } from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

import { expect, test as base, type Locator, type Page, type TestInfo } from '@playwright/test'

const HERE = path.dirname(fileURLToPath(import.meta.url))

/** impl-plan §6.3 step 6.12 / §7.4: the reviewed screenshot deliverable. */
export const SHOTS_DIR = path.resolve(HERE, '..', '..', 'docs', 'e2e-shots')

/**
 * The curated series of impl-plan §6.3, by their exact directory names. Ten
 * until E-14 added `비둘기.zip`, and then four more in the RAR session — three
 * that D-71 made reachable (`라제폰`, `울프가이`, `사모님은 학생회장`) and one
 * that D-72 gave a name to instead of `비어 있음` (`펌프킨 시저스`, which E-51
 * then gave a reader and 104 pages). Fifteen now.
 *
 * These are a *copy*, not the source. The same strings are written out three
 * more times — `scripts/e2e-config.sh:29-43` (`CURATED`, which becomes
 * `scan.include_globs`, and is the source of truth), `scripts/e2e-assert.py:51-65`
 * (`CURATED`, the curl tier's expectation) and 26 path literals in
 * `scripts/mkfixture/main.go`, which builds the synthetic twin under the same
 * names (D-49). A **fifth** copy hides in `scripts/e2e.sh:922` (`A11_FILL`, one
 * archive by path), and a sixth in `docs/impl-plan.md` §6.3's table, which is
 * the *declared* source of truth and carries no code.
 *
 * Nothing linked the six statically until 2026-08-18, and they agreed only
 * because a disagreement failed the run — twenty minutes in, as `got 0, want
 * 15`, naming neither the file to fix nor the series. `contractcheck`'s
 * `checkCuratedSeries` now compares all six every `make lint`, in seconds and by
 * name; the doc table had already drifted when it was written. Renaming one
 * series is still a five-file edit, but the sixth file no longer decides how you
 * find out.
 *
 * All ten of the names of the day were renamed at once in 2026-08, when the
 * collection on disk lost the leading `[만화] ` every entry used to carry. That
 * is what a rename cost when nothing linked the copies: the scan matched zero of
 * ten, and `make e2e` died at step 7 with `got 0, want 10` before a browser ever
 * started. Two consequences outlived the edit. `scripts/e2e-config.sh`'s glob
 * escaping is now load-bearing for exactly one name, 배틀로얄, which is the only
 * one left holding a `[`. And `docs/e2e-shots/` still shows the old titles until
 * the next review round retakes it.
 */
export const SERIES = {
  clover: 'Clover 클로버 (총4권)',
  scars: '상처를 쫓는자 1-11 (완) 이케가미 료이치',
  suicide: '자살도114-122',
  wheel: '바퀴.zip',
  steel: '강철의 연금술사 1~27권 완결',
  gungye: '군계 1~25',
  dnangel: '디엔엔젤 1-13권 연재중',
  misaeng: '미생 1~9 (완결 pdf)',
  battleRoyale: '배틀로얄 1~15 [완결].zip',
  angelHeart: '엔젤하트 전32권 완결.zip',
  dove: '비둘기.zip',
  rahxephon: '라제폰 1-3권 완결',
  wolfGuy: '울프가이',
  madam: '사모님은 학생회장.zip',
  pumpkin: '펌프킨 시저스 1~13권',
} as const

/**
 * The shapes D-49 asks the synthetic tree to carry that the real collection has
 * no sample of — an encrypted archive, a ZIP64 archive, and (since D-71) a solid
 * RAR, which this build refuses on purpose and which none of the collection's 14
 * real archives is.
 *
 * A *copy* on the same terms as `SERIES` above: the same strings are written out
 * in `scripts/e2e-config.sh:49-51` (`SYNTHETIC_EXTRA`, appended to
 * `scan.include_globs` in synthetic mode), `scripts/e2e-assert.py:67` (the curl
 * tier's expectation) and `scripts/mkfixture/main.go`, which builds them.
 *
 * Four copies of one list is three too many, and it drifted once: the D-71
 * series were added to the other three and this round failed here, in eight
 * screenshots across four viewports, rather than anywhere nearer the change.
 * `contractcheck`'s `checkCuratedSeries` compares these three the same way it
 * compares `SERIES`, so the next drift is a `make lint` failure by name. Still
 * worth collapsing to a generated file the next time this list moves.
 */
export const SYNTHETIC_EXTRA = {
  encrypted: '암호화 테스트.zip',
  zip64: 'ZIP64 테스트.zip',
  solidRar: '솔리드 테스트.rar',
} as const

/**
 * Which mode the server under test was configured for, from `scripts/e2e.sh`.
 *
 * The default is `real`, so a spec run by hand against a real server needs no
 * environment at all. The coupling is not unchecked — that was the objection to
 * an env signal, and `expectCuratedLibrary` below is what answers it: the
 * expectation it builds is a *set of names*, so a mode that does not match the
 * server fails immediately and by name (the three D-49 extras missing, or the
 * three of them surplus) instead of silently asserting the wrong library.
 */
export const SYNTHETIC = process.env.SHELF_E2E_MODE === 'synthetic'

/**
 * Whether `Settings.server.root_editing_enabled` must be true on the server
 * under test — amendment **A-11**, ruling **E-26**.
 *
 * It is derived from the mode for the same reason `EXPECTED_SERIES` is: the two
 * rounds run different configurations (`scripts/e2e-config.sh` emits
 * `server.allow_root_editing: true` for `--synthetic` only), and the difference
 * decides whether the 루트 추가 / 제거 controls exist at all. So it is a
 * *derived expectation*, never a switch that skips work:
 * `06-settings.spec.ts` asserts the server agrees with it before it counts a
 * single button, which is what stops `toHaveCount(0)` passing for the wrong
 * reason — the controls were absent under ruling E-3 too, and the assertion
 * could not tell "removed" from "gated off" until it read the capability.
 *
 * The real round keeps the gate SHUT, because shut is what ships (arch §3.2's
 * default) and that is the configuration most users will ever run. The write
 * path is exercised in the synthetic round, whose fixture tree and
 * configuration file both live under /tmp — see `08-roots.spec.ts`.
 */
export const ROOT_EDITING_ENABLED = SYNTHETIC

/**
 * Exactly what `scan.include_globs` puts in the library for this mode: the
 * curated set, plus the D-49 extras in synthetic mode.
 *
 * The same parameterisation `scripts/e2e-assert.py:1020` already makes —
 * `expected = CURATED + ([] if real else SYNTHETIC_EXTRA)` — green in both modes
 * since it was written. impl-plan §6.3 step 6.1's literal "10 cards" is outranked
 * by D-49 (decisions > impl-plan, §0 precedence), and §6.3's own hermetic-fallback
 * paragraph concedes the three extras in the same sentence as "the identical
 * assertion set": identical *assertions*, each against its own mode's expected set.
 */
export const EXPECTED_SERIES: readonly string[] = SYNTHETIC
  ? [...Object.values(SERIES), ...Object.values(SYNTHETIC_EXTRA)]
  : Object.values(SERIES)

/**
 * Where the fifteen names live. Printed by `expectCuratedLibrary` when they
 * disagree, because the failure it reports is almost never in this file: it is
 * one of these copies having moved without the others.
 *
 * The line numbers were true when written and are the second thing to distrust
 * (the first being this list's own count). `make lint` runs `contractcheck`,
 * whose `checkCuratedSeries` reads all six by pattern rather than by line and
 * names the file and the series in seconds.
 */
const CURATED_COPIES = [
  'scripts/e2e-config.sh:29-43  CURATED — becomes scan.include_globs. THE SOURCE OF TRUTH.',
  'scripts/e2e-assert.py:51-65  CURATED — the curl tier, unpacked positionally at :69-72',
  'web/e2e/shelf.ts:84-98       SERIES — this file, the browser tier',
  'scripts/mkfixture/main.go    26 path literals, which build the synthetic twin (D-49)',
  'scripts/e2e.sh:922           A11_FILL — one archive by path, step 11b',
  'docs/impl-plan.md §6.3       the curated-set table — the declared source of truth',
].join('\n  ')

/**
 * The library holds exactly the series this mode's `include_globs` names.
 *
 * Strictly stronger than the `toBe(10)` it replaces, which never said *which*
 * ten: an `include_globs` leak that swapped one curated series for another, or a
 * rescan that dropped one, passed a count and fails this by name. That matters
 * most in the browser tier, because `scripts/e2e.sh` step 10 deletes `index.db`
 * and rescans *after* the curl tier has run, and not every curated name is
 * referenced anywhere else in this directory.
 *
 * The message is the point of the helper as much as the assertion is. Playwright
 * would print two sorted fifteen-element arrays and leave the reader to diff
 * Korean strings by eye; what a reader needs instead is which names are missing,
 * which are surplus, and which file to edit — and the two shapes that mean
 * something specific: nothing indexed at all is a glob that matched nothing, and
 * one missing beside one surplus is a series that was renamed on disk.
 */
export function expectCuratedLibrary(names: Iterable<string>, why: string): void {
  const got = [...names]
  const expected = new Set(EXPECTED_SERIES)
  const indexed = new Set(got)
  const missing = EXPECTED_SERIES.filter((name) => !indexed.has(name))
  const surplus = got.filter((name) => !expected.has(name))

  const lines: string[] = [why]
  if (missing.length > 0 || surplus.length > 0) {
    lines.push(
      `the library holds ${String(got.length)} series; this ${SYNTHETIC ? 'synthetic' : 'real'} round expects ${String(EXPECTED_SERIES.length)}.`,
    )
    if (got.length === 0) {
      lines.push(
        'NOTHING was indexed: every scan.include_globs pattern missed. One edit to',
        "scripts/e2e-config.sh's CURATED does exactly this — a collection-wide rename did, once.",
      )
    } else if (missing.length === 1 && surplus.length === 1) {
      lines.push(
        `one name moved: expected ${JSON.stringify(missing[0])},`,
        `the server has ${JSON.stringify(surplus[0])}.`,
        'that is one series renamed on disk. Fix CURATED first, then the copies below.',
      )
    } else {
      if (missing.length > 0) {
        lines.push(`expected and NOT indexed (${String(missing.length)}):`, ...missing.map((n) => `  - ${n}`))
      }
      if (surplus.length > 0) {
        lines.push(`indexed and NOT expected (${String(surplus.length)}):`, ...surplus.map((n) => `  + ${n}`))
      }
    }
    lines.push(`the names live in six unlinked copies:\n  ${CURATED_COPIES}`)
  }
  expect(got.slice().sort(), lines.join('\n')).toEqual([...EXPECTED_SERIES].sort())
}

// ---------------------------------------------------------------------------
// The fixture
// ---------------------------------------------------------------------------

/**
 * Accumulates layout-shift values so step 6.1 can assert one, and traps console
 * errors for NFR-CMP-001.
 *
 * The observer has to be installed *before* the document exists, which is what
 * `addInitScript` is for; `buffered: true` then replays the shifts that
 * happened before the observer was constructed.
 */
const CLS_INIT = `
  window.__shelfCls = 0
  try {
    new PerformanceObserver((list) => {
      for (const entry of list.getEntries()) {
        if (!entry.hadRecentInput) window.__shelfCls += entry.value
      }
    }).observe({ type: 'layout-shift', buffered: true })
  } catch (_) {
    /* Layout Instability is Chromium-only; the assertion self-skips. */
  }
`

interface DeclaredConsoleError {
  readonly pattern: RegExp
  readonly why: string
  seen: boolean
}

/** Per-page declarations, read by the `consoleGuard` fixture below. */
const declaredConsoleErrors = new WeakMap<Page, DeclaredConsoleError[]>()

/**
 * Declares a console error this test is about to *cause on purpose*, and why.
 *
 * There is exactly one legitimate use, and it is narrow: a test that drives the
 * product into a **documented server refusal**. Chromium logs every non-2xx
 * response as `console.error: Failed to load resource: the server responded
 * with a status of …`, so `08-roots.spec.ts` asserting that a rejected
 * `POST /api/roots` reaches the user as the sentence `rootErrors.ts` writes for
 * it would otherwise fail NFR-CMP-001 for doing the thing it exists to do.
 *
 * It is not a mute button, and two properties keep it from becoming one:
 *
 *  * the `pattern` has to match, so it is written narrowly enough to name the
 *    one message — anything else the page logs still fails the test;
 *  * a declaration that **never matched** fails the test too. A stale allowance
 *    left behind by a spec that stopped triggering the refusal would otherwise
 *    sit there silencing whatever came along later, which is docs/HANDOFF.md
 *    §6.5 with the polarity reversed.
 *
 * `pageerror` — an uncaught exception in the page — is never declarable. That
 * is a defect in every case.
 */
export function expectConsoleError(page: Page, pattern: RegExp, why: string): void {
  const declared = declaredConsoleErrors.get(page) ?? []
  declared.push({ pattern, why, seen: false })
  declaredConsoleErrors.set(page, declared)
}

/**
 * A fixture that exists only for its side effect, so it has no value to declare.
 * `void` is Playwright's own idiom for that, and it is the only spelling from
 * which the fixture callback's `{ page }` and `use` still infer — `unknown`
 * makes both implicitly `any`. Hence the one disable in this directory.
 */
// eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- Playwright's side-effect-fixture idiom; see above.
export const test = base.extend<{ consoleGuard: void }>({
  consoleGuard: [
    async ({ page }, use) => {
      const problems: string[] = []
      page.on('console', (message) => {
        if (message.type() !== 'error') return
        const text = message.text()
        const declared = (declaredConsoleErrors.get(page) ?? []).find((entry) =>
          entry.pattern.test(text),
        )
        if (declared === undefined) {
          problems.push(`console.error: ${text}`)
          return
        }
        declared.seen = true
      })
      page.on('pageerror', (error) => {
        problems.push(`pageerror: ${error.message}`)
      })
      await page.addInitScript(CLS_INIT)
      await use()
      expect(problems, 'NFR-CMP-001: the SPA must load with no console errors').toEqual([])
      const undeclared = (declaredConsoleErrors.get(page) ?? [])
        .filter((entry) => !entry.seen)
        .map((entry) => entry.why)
      expect(
        undeclared,
        'a console error was declared with expectConsoleError() and never happened: the ' +
          'allowance is now silencing whatever else the page logs',
      ).toEqual([])
    },
    { auto: true },
  ],
})

export { expect } from '@playwright/test'

// ---------------------------------------------------------------------------
// Screenshots — step 6.12
// ---------------------------------------------------------------------------

/**
 * Writes `docs/e2e-shots/<name>-<project>.png`.
 *
 * Suffixed with the project name rather than the pixel width so the four
 * viewport runs of one step land side by side in a directory listing, which is
 * how §7.4's "reviewed against `docs/ui-shots/`" is actually done.
 */
export async function shot(page: Page, info: TestInfo, name: string): Promise<void> {
  await mkdir(SHOTS_DIR, { recursive: true })
  await page.screenshot({ path: path.join(SHOTS_DIR, `${name}-${info.project.name}.png`) })
}

// ---------------------------------------------------------------------------
// Server facts, read rather than assumed
// ---------------------------------------------------------------------------

export interface SeriesFact {
  id: string
  name: string
  kind: string
  status: string
  book_count: number
  total_bytes: number
  has_cover: boolean
}

export interface BookFact {
  id: string
  name: string
  kind: string
  status: string
  page_count: number
}

/** `GET /api/series` as a name → summary map. */
export async function seriesFacts(page: Page): Promise<Map<string, SeriesFact>> {
  const response = await page.request.get('/api/series?limit=200&sort=name')
  expect(response.ok(), 'GET /api/series').toBe(true)
  const body = (await response.json()) as { items: SeriesFact[] }
  return new Map(body.items.map((item) => [item.name, item]))
}

export async function seriesId(page: Page, name: string): Promise<string> {
  const facts = await seriesFacts(page)
  const fact = facts.get(name)
  expect(fact, `the curated subset must contain ${name}`).toBeDefined()
  return fact === undefined ? '' : fact.id
}

/** The volumes of one series, in server order. */
export async function booksOf(page: Page, sid: string): Promise<BookFact[]> {
  const response = await page.request.get(`/api/series/${sid}`)
  expect(response.ok(), `GET /api/series/${sid}`).toBe(true)
  const body = (await response.json()) as { books: BookFact[] }
  return body.books
}

// ---------------------------------------------------------------------------
// Baselines — rule 2 of the header comment
// ---------------------------------------------------------------------------

/**
 * Puts the *server's* sticky library state back to its defaults.
 *
 * `library_view`, `library_sort`, `library_order`, `library_scope` (A-5) and
 * `theme` are persisted in `user.db` and hydrated into the store on every load,
 * so without this each spec would inherit whatever the previous one — or the
 * previous *viewport project*, or the `PUT {"theme":"dark"}` that `e2e.sh`
 * step 10 makes — happened to leave behind. Setup, not an assertion: it is done
 * over the API precisely so that the UI half of the test still has to do the
 * switching itself.
 */
export async function resetLibraryState(page: Page): Promise<void> {
  const response = await page.request.put('/api/settings', {
    data: {
      library_view: 'grid',
      library_sort: 'name',
      library_order: 'asc',
      library_scope: 'all',
      theme: 'system',
    },
  })
  expect(response.ok(), 'PUT /api/settings').toBe(true)
}

/** The same for one book's per-book overrides (arch §7.6, FR-VWR-002). */
export async function resetBookPrefs(page: Page, bookId: string): Promise<void> {
  const response = await page.request.put(`/api/books/${bookId}/prefs`, {
    data: { reading_direction: 'ltr', display_mode: 'single', fit_mode: 'height' },
  })
  expect(response.ok(), `PUT /api/books/${bookId}/prefs`).toBe(true)
}

// ---------------------------------------------------------------------------
// Navigation
// ---------------------------------------------------------------------------

/**
 * Loads `/` and waits for the library to be interactive.
 *
 * The wait on `GET /api/settings` is not incidental. `useLibrarySettingsSync`
 * hydrates view/sort/order/scope from the server *after* the first paint, so a
 * helper that clicked 리스트 before that response landed would have its choice
 * overwritten a tick later — a flake that only appears when the machine is
 * fast enough to click first.
 */
export async function gotoLibrary(page: Page): Promise<void> {
  const settings = page.waitForResponse(
    (response) => response.url().includes('/api/settings') && response.request().method() === 'GET',
  )
  await page.goto('/')
  await settings
  await expect(page.locator('[data-testid="library-scroller"]')).toBeVisible()
}

export type ViewMode = 'grid' | 'list'

/**
 * What a screen renders once it is in one view mode.
 *
 * `shown` must be visible and `gone`, when there is one, must be absent. The
 * pair is what makes the mode *provable* rather than merely clicked at: on
 * every screen the two modes' shapes are mutually exclusive, so satisfying one
 * pair is only possible after the swap has actually happened.
 */
interface ViewShape {
  readonly shown: string
  readonly gone: string | null
}

/**
 * The screens the 보기 방식 toggle serves, and how to tell which mode each is in.
 *
 * The toggle drives **one** preference — `store/ui.ts` `view`, persisted
 * server-side as `library_view` — and both screens read it from that same store
 * (`SeriesDetailPage.tsx`: "one control, two screens"). So the control is
 * screen-agnostic, but what it swaps is not, and the difference is a spec
 * requirement rather than an accident:
 *
 * - **Library** — `SeriesGrid` / `SeriesList`. Both render `library-scroller`;
 *   only the list adds the header band, because ui-spec §5.2 list mode
 *   specifies a header row of sortable 시리즈명/권/용량/수정일 cells. Presence
 *   of that header is therefore a biconditional for list mode here.
 * - **Series detail** — `VolumeGrid` / `VolumeList`, each with its own container
 *   testid. ui-spec §5.4 is explicit that the volume list has **"No header row
 *   (volumes are naturally ordered; sorting is not offered)"**, so the library's
 *   header must never appear on this screen and must never be waited for. The
 *   volume list's own container is what proves the mode instead.
 *
 * `probe` says which screen is on top; the two are never mounted together.
 */
const VIEW_SCREENS = {
  series: {
    probe: '[data-testid="volume-grid"], [data-testid="volume-list"]',
    grid: { shown: '[data-testid="volume-grid"]', gone: '[data-testid="volume-list"]' },
    list: { shown: '[data-testid="volume-list"]', gone: '[data-testid="volume-grid"]' },
  },
  library: {
    probe: '[data-testid="library-scroller"]',
    grid: { shown: '[data-testid="library-scroller"]', gone: '[data-testid="library-list-header"]' },
    list: { shown: '[data-testid="library-list-header"]', gone: null },
  },
} as const satisfies Record<string, { probe: string } & Record<ViewMode, ViewShape>>

type ViewScreen = keyof typeof VIEW_SCREENS

/**
 * Which of the two screens the toggle is about to act on.
 *
 * The wait matters: it turns "setView was called somewhere the toggle means
 * nothing" into an immediate, named failure instead of a long block on a marker
 * that screen will never render.
 */
async function viewScreen(page: Page): Promise<ViewScreen> {
  await expect(
    page.locator(`${VIEW_SCREENS.series.probe}, ${VIEW_SCREENS.library.probe}`).first(),
    'setView needs the library or a series detail screen on top',
  ).toBeVisible()
  return (await page.locator(VIEW_SCREENS.series.probe).count()) > 0 ? 'series' : 'library'
}

/**
 * The top-bar 그리드/리스트 `.seg` (FR-LIB-002), on whichever screen owns it.
 *
 * Idempotent, but never *silently* so: the post-condition runs whether or not a
 * click was needed, so the helper always returns having proved the mode it
 * claims — a no-op path that asserted nothing is how a stale view mode reaches
 * the caller's assertions disguised as a fresh one.
 */
export async function setView(page: Page, view: ViewMode): Promise<void> {
  const screen = await viewScreen(page)
  const shape: ViewShape = VIEW_SCREENS[screen][view]
  const shown = page.locator(shape.shown)
  const gone = shape.gone === null ? null : page.locator(shape.gone)

  const already = (await shown.count()) > 0 && (gone === null || (await gone.count()) === 0)
  if (!already) {
    await page.locator(`[aria-label="보기 방식"] label[data-value="${view}"]`).click()
  }

  const why = `보기 방식 → ${view} on the ${screen} screen`
  await expect(shown, why).toBeVisible()
  if (gone !== null) await expect(gone, why).toHaveCount(0)
}

/**
 * The top-bar 정렬 select (C-3 wire keys).
 *
 * Used in preference to the list's own column headers because ui-spec §7 drops
 * 용량 below 1024 and every sortable header but 시리즈명 below 768 — the select
 * is the one sort affordance that exists at all four viewport widths.
 */
export async function setSort(page: Page, key: string): Promise<void> {
  await page.locator('select[name="sort"]').selectOption(key)
}

/**
 * Every series card or row currently in the DOM, by its accessible name.
 *
 * `SeriesCard` and `SeriesRow` both put the series name on `aria-label` of the
 * one button that opens it, which is the only handle that is identical in both
 * view modes and at all four widths.
 */
export function seriesTiles(page: Page): Locator {
  return page.locator('[data-testid="library-scroller"] button[aria-label]')
}

export function seriesTile(page: Page, name: string): Locator {
  return page.locator(`[data-testid="library-scroller"] button[aria-label="${name}"]`)
}

/**
 * Walks the whole library the way a reader does — a viewport at a time from the
 * top — and returns every series name that was mounted along the way.
 *
 * `seriesTiles()` answers "what is in the DOM *now*", which under FR-LIB-007 is
 * not the same question as "what does the library hold". `SeriesGrid` windows
 * rows with `overscan: 2`, and ui-spec §7 gives the narrow tiers big cards
 * (`--grid-min: 224px` at 768–1023, two columns at both 768 and 400), so a
 * library this size is mounted all at once at 1440 and 1024 and never is at 768
 * or 400. The measurement behind that sentence was taken on 2026-07-29, when the
 * curated set was ten: 8 of 10 mounted at both narrow tiers. The set is fifteen
 * now (eighteen synthetic) and nobody has re-measured, which makes the gap wider
 * rather than narrower — the conclusion survives, the two numbers are of their
 * date. A caller that wants the whole library therefore has to page through it,
 * and one that asserts a count against the live locator instead is asserting its
 * own viewport, not the product.
 *
 * `atEachStop` runs against the names mounted at one scroll position, **before**
 * the next step can unmount them, which is the only window in which a per-card
 * assertion (does this cover paint?) is not a race. Nothing scrolls while it
 * runs, so every name it is handed is still in the DOM for the whole call.
 *
 * Related but not the same as `revealSeriesTile()` below: that one stops at the
 * first series it was asked for, this one always reaches the end. Both work in
 * either view mode, since `seriesTiles` does.
 *
 * The scroller is left back at the top, so a caller may screenshot afterwards.
 */
export async function walkLibrary(
  page: Page,
  atEachStop: (names: string[]) => Promise<void> = () => Promise.resolve(),
): Promise<Set<string>> {
  const scroller = page.locator('[data-testid="library-scroller"]')
  await expect(scroller).toBeVisible()
  await scroller.evaluate((el) => {
    el.scrollTop = 0
  })

  const seen = new Set<string>()
  // Bounded rather than `while (true)`: a scroller that grows faster than it is
  // walked has to fail as a named error, not as the suite's 120 s timeout. 50 is
  // slack and says so; the measurement it is slack around is five. Counted on
  // 2026-07-29 by driving this exact loop against the synthetic fixture as it
  // stood — twelve series — at all four projects' viewports: 그리드 takes 2 stops
  // at desktop-1440, 3 at laptop-1024 and 5 at both tablet-768 and mobile-400,
  // and 리스트 never exceeds 2. The fixture is eighteen series now — E-14, D-71
  // and D-72 added five curated names between them and D-71 a third synthetic
  // extra — so those stop counts are a FLOOR of that date rather than today's
  // reading; 50 is still slack around any of them. What stood here — "the
  // narrowest tier needs five stops for the curated ten" — had the wrong library
  // size and no measurement behind either number, which is the defect class of
  // HANDOFF §6.5 in a comment.
  for (let stop = 0; stop < 50; stop += 1) {
    const why = 'the library must render at least one series'
    await expect(seriesTiles(page).first(), why).toBeVisible()
    const names = await seriesTiles(page).evaluateAll((tiles) =>
      tiles.map((tile) => tile.getAttribute('aria-label') ?? ''),
    )
    for (const name of names) seen.add(name)
    await atEachStop(names)

    // One viewport per step, so consecutive windows overlap by the overscan and
    // no row can fall between two stops. A step that cannot move is the bottom.
    const moved = await scroller.evaluate((el) => {
      const before = el.scrollTop
      el.scrollTop += el.clientHeight
      return el.scrollTop !== before
    })
    if (!moved) {
      await scroller.evaluate((el) => {
        el.scrollTop = 0
      })
      return seen
    }
    // Two frames: one for the virtualiser's scroll handler to set state, one for
    // React to commit the rows it chose. Without it the read above can be a
    // frame stale — harmless for the union, but it would let `atEachStop` assert
    // against the previous window twice and never see the last one.
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
  throw new Error('walkLibrary: the library scroller never reached its end in 50 steps')
}

/**
 * Scrolls the library until one series' tile is actually mounted, and returns it.
 *
 * FR-LIB-007 virtualises **both** view modes, so a series below the fold is not
 * merely off screen — it is not in the DOM at all, and `scrollIntoViewIfNeeded`
 * has nothing to scroll *to*. That is width-dependent, which is why it surfaces
 * as a two-project failure: the tail of the library is mounted from the start at
 * 1440 and never mounted at 768 or 400 until something scrolls.
 *
 * Scrolling the scroller is also what a reader does, and what drives the
 * `onEndReached` pagination, so this reaches a series the way the product
 * intends one to be reached rather than by reaching around the virtualiser.
 */
async function revealSeriesTile(page: Page, name: string): Promise<Locator> {
  const tile = seriesTile(page, name)
  const scroller = page.locator('[data-testid="library-scroller"]')
  await expect(scroller).toBeVisible()

  await expect
    .poll(
      async () => {
        if ((await tile.count()) > 0) return true
        // A step that cannot move is the bottom of the list: stop paging and let
        // the assertion below name the series that is genuinely not there.
        return scroller.evaluate((el) => {
          const before = el.scrollTop
          el.scrollTop += el.clientHeight
          return el.scrollTop === before ? 'bottom' : false
        })
      },
      { message: `${name} must be reachable by scrolling the library` },
    )
    .not.toBe(false)

  await expect(tile, `${name} must be in the library`).toHaveCount(1)
  await tile.scrollIntoViewIfNeeded()
  return tile
}

/** Opens a series from the library the way a reader does — by clicking it. */
export async function openSeries(page: Page, name: string): Promise<void> {
  const tile = await revealSeriesTile(page, name)
  await tile.click()
  await expect(page.getByRole('heading', { level: 2, name })).toBeVisible()
}

/**
 * Opens 설정 the way each viewport tier offers it (ui-spec §7).
 *
 * Below 768 there is no sidebar in the DOM at all — only the off-canvas drawer —
 * so the route to the dialog is genuinely different rather than merely narrower.
 * Shared by 06-settings and 08-roots: two copies of this would be two chances to
 * fix a drawer change in one of them.
 */
export async function openSettings(page: Page): Promise<void> {
  const width = page.viewportSize()?.width ?? 0
  if (width < 768) {
    await page.getByRole('button', { name: '라이브러리 탐색 열기' }).click()
    await expect(page.getByRole('dialog', { name: '라이브러리 탐색' })).toBeVisible()
    await page.getByRole('dialog', { name: '라이브러리 탐색' }).getByLabel('설정').click()
  } else {
    await page.locator('aside[aria-label="라이브러리 탐색"]').getByLabel('설정').click()
  }
  await expect(page.getByRole('dialog', { name: '설정' })).toBeVisible()
}

/** The 루트 관리 section of the open 설정 dialog (ui-spec §8.6 §1). */
export function rootsSection(page: Page): Locator {
  return page
    .getByRole('dialog', { name: '설정' })
    .locator('section')
    .filter({ has: page.getByRole('heading', { name: '루트 관리' }) })
}

// ---------------------------------------------------------------------------
// The viewer
// ---------------------------------------------------------------------------

export function viewer(page: Page): Locator {
  return page.locator('[data-role="viewer"]')
}

export function viewerTopBar(page: Page): Locator {
  return page.locator('[data-role="viewer-top-bar"]')
}

export function viewerBottomBar(page: Page): Locator {
  return page.locator('[data-role="viewer-bottom-bar"]')
}

/**
 * One of the two screen-edge strips ruling **E-27** put the chrome behind.
 *
 * Rendered **only while the chrome is away** — once the bars are up they are
 * what the pointer reaches for, and a strip over them would eat the first click
 * on 뒤로. So a count of 1 and a count of 0 are both assertions about the
 * chrome, and neither means anything without `data-chrome` asserted beside it.
 */
export function viewerEdge(page: Page, which: 'top' | 'bottom'): Locator {
  return page.locator(`[data-role="viewer-edge-${which}"]`)
}

/** The page number a chromeless viewer keeps on screen (E-27). */
export function quietPageCounter(page: Page): Locator {
  return page.locator('[data-role="quiet-page-counter"]')
}

/**
 * How deep the screen-edge strips reach — `ViewerPage.tsx`'s `EDGE_STRIP_PX`.
 *
 * A copy, on the same terms as `SERIES` above, and used only as a **floor**:
 * helpers here keep the pointer further than this from an edge, and the one
 * assertion that holds a strip against it is in `09-viewer-chrome.spec.ts`,
 * where a drift is meant to be read as a change to E-27 rather than a flake.
 */
export const EDGE_STRIP_PX = 44

/** Whether a point falls inside a bounding box; `null` contains nothing. */
export function boxContains(
  box: { x: number; y: number; width: number; height: number } | null,
  x: number,
  y: number,
): boolean {
  if (box === null) return false
  return x >= box.x && x <= box.x + box.width && y >= box.y && y <= box.y + box.height
}

/**
 * `formatViewerCounter`'s rendering — what the reader actually reads.
 *
 * A copy of `web/src/lib/format.ts`'s `formatCount`, which is deliberately
 * locale-independent (`toLocaleString` would make this depend on the `ko-KR`
 * locale `playwright.config.ts` sets). Copied rather than parsed back out
 * because the two counters — the bar's and E-27's quiet one — have to render
 * the *same* string, and a regex that accepts both would not notice if they
 * stopped agreeing.
 */
export function viewerCounterText(page: number, total: number): string {
  const group = (n: number): string => {
    const digits = Math.abs(Math.trunc(n)).toString()
    let out = ''
    for (let i = 0; i < digits.length; i++) {
      if (i > 0 && (digits.length - i) % 3 === 0) out += ','
      out += digits[i] ?? ''
    }
    return out
  }
  return `${group(page)} / ${group(total)}`
}

/** `formatViewerCounter` renders `1,400 / 1,540`; the commas come back out. */
export async function currentPage(page: Page): Promise<number> {
  const text = await page.locator('[data-role="page-counter"]').innerText()
  const match = /(\d[\d,]*)\s*\/\s*(\d[\d,]*)/.exec(text)
  expect(match, `page counter should read "n / total", got ${text}`).not.toBeNull()
  return match === null ? 0 : Number(match[1]?.replace(/,/g, '') ?? '0')
}

export async function pageCount(page: Page): Promise<number> {
  const text = await page.locator('[data-role="page-counter"]').innerText()
  const match = /(\d[\d,]*)\s*\/\s*(\d[\d,]*)/.exec(text)
  return match === null ? 0 : Number(match[2]?.replace(/,/g, '') ?? '0')
}

/**
 * Brings the overlay chrome back and waits until it is actually clickable.
 *
 * The bars fade out `CHROME_AUTOHIDE_MS` after the last wake and go
 * `pointer-events: none` with it, so any helper that clicks a viewer control
 * has to wake it first — and has to wait for `data-visible`, because Playwright
 * would otherwise spend the whole actionability timeout on an invisible button.
 *
 * **Ruling E-27 changed what "wake" means.** Moving the mouse over the page no
 * longer summons anything; the chrome answers to the top and bottom 44px screen
 * edges, the centre tap and `H`. So this aims at the top strip — and the centre
 * of the stage, which is what it used to aim at, is now the *toggle*, i.e. the
 * one move that would put the chrome away again on the second call.
 *
 * It then parks the pointer **on the bar**, because hovering the chrome holds
 * the auto-hide off (E-27). That makes every caller below deterministic rather
 * than a race against a 2.6 s timer.
 */
export async function wakeChrome(page: Page): Promise<void> {
  const box = await viewer(page).boundingBox()
  const x = box === null ? 10 : box.x + box.width / 2
  const top = box === null ? 10 : box.y
  // Two moves: the first lands inside the strip, the second is what guarantees
  // a `mouseenter` even if the pointer was already at the first coordinate.
  await page.mouse.move(x, top + 30)
  await page.mouse.move(x, top + 12)
  await expect(viewerTopBar(page)).toHaveAttribute('data-visible', 'true')

  const bar = await viewerTopBar(page).boundingBox()
  if (bar !== null) {
    await page.mouse.move(bar.x + bar.width / 2, bar.y + bar.height / 2)
  }
}

/**
 * Moves the pointer off the chrome and onto the middle of the stage — the other
 * half of `wakeChrome`, and the only way a spec can watch the chrome go away.
 *
 * `wakeChrome` parks the pointer **on the bar** on purpose, because hovering the
 * chrome holds the auto-hide off (E-27) and that is what makes every caller of
 * it deterministic. The cost is that no spec built on it can ever observe the
 * auto-hide: the timer it re-arms is cleared again the moment the pointer
 * arrives. This helper releases that hold, which re-arms the timer
 * (`releaseChrome` in `store/viewer.ts`), and leaves the pointer somewhere the
 * chrome cannot be summoned from.
 *
 * Everything it needs of the destination is asserted rather than assumed. A
 * pointer that lands back inside a bar holds the auto-hide off and the caller's
 * wait then times out for a reason that has nothing to do with the timer; a
 * pointer that lands in a screen-edge strip *summons* the chrome instead, and
 * the caller would be watching the opposite of what it asked for.
 */
export async function standBackFromChrome(page: Page): Promise<void> {
  const stage = await page.locator('[data-role="stage-zones"]').boundingBox()
  expect(stage, 'standBackFromChrome needs a laid-out stage to park the pointer on').not.toBeNull()
  const x = (stage?.x ?? 0) + (stage?.width ?? 0) / 2
  const y = (stage?.y ?? 0) + (stage?.height ?? 0) / 2

  // Both bars are *never unmounted* — they fade on opacity — so both always have
  // a box, and "is the parking spot inside one" is a question with an answer at
  // every moment rather than only while the chrome is up.
  const top = await viewerTopBar(page).boundingBox()
  const bottom = await viewerBottomBar(page).boundingBox()
  expect(top, 'the top bar is never unmounted, so it always has a box').not.toBeNull()
  expect(bottom, 'the bottom bar is never unmounted, so it always has a box').not.toBeNull()
  expect(
    boxContains(top, x, y),
    'the pointer must come to rest clear of the top bar, or it holds the auto-hide off',
  ).toBe(false)
  expect(
    boxContains(bottom, x, y),
    'the pointer must come to rest clear of the bottom bar, or it holds the auto-hide off',
  ).toBe(false)

  const box = await viewer(page).boundingBox()
  expect(box, 'the viewer must be laid out before the pointer can be placed on it').not.toBeNull()
  expect(
    y - (box?.y ?? 0),
    `the pointer must come to rest clear of the top ${String(EDGE_STRIP_PX)}px edge strip, which summons the chrome`,
  ).toBeGreaterThan(EDGE_STRIP_PX)
  expect(
    (box?.y ?? 0) + (box?.height ?? 0) - y,
    `the pointer must come to rest clear of the bottom ${String(EDGE_STRIP_PX)}px edge strip, which summons the chrome`,
  ).toBeGreaterThan(EDGE_STRIP_PX)

  await page.mouse.move(x, y)
}

/**
 * Opens a book straight at its route, at a page of the caller's choosing.
 *
 * `?page=` is the product's own parameter — the series screen sets it, and
 * `NextVolumeCard` navigates with it on 다음 권 읽기 — and it outranks the saved
 * progress row (`ViewerPage`), so a caller gets the page it asked for whatever
 * the previous viewport project left in `user.db`.
 *
 * The reader's route into the viewer is a *different* assertion and it already
 * has three: 04-viewer 6.6 and 6.6b and 07-responsive 6.11 all open a volume by
 * clicking its tile (AC-003). Specs about what the viewer does once it is open
 * take the direct route instead, so that a change to the library or the series
 * screen cannot redden them.
 */
export async function openViewerDirect(
  page: Page,
  seriesIdent: string,
  bookId: string,
  atPage: number,
): Promise<void> {
  await page.goto(`/series/${seriesIdent}/books/${bookId}?page=${String(atPage)}`)
  await expect(viewer(page)).toBeVisible()
}

/**
 * Deletes a book's progress row and waits until the server agrees it is gone.
 *
 * shelf.ts rule 2: a spec that opens a book it invented state for has to take
 * that state away again. A leftover row draws a progress bar on the series card,
 * adds the series to the 이어보기 shelf and to the palette's recents, and every
 * one of those is in a §7.4 screenshot that 01-library and 02-palette take
 * *before* this file runs in the next viewport project — a diff with no product
 * behind it.
 *
 * Polled rather than fired once, for the reason 04-viewer 6.6b spells out at
 * length: `useSaveProgress` debounces its page write by a second and flushes it
 * on unmount, so a single DELETE can lose the race and put the row straight
 * back. The loop is the assertion — a DELETE that stops answering 204 fails on
 * the spot instead of being retried away.
 */
export async function clearProgress(page: Page, bookId: string): Promise<void> {
  await expect
    .poll(
      async () => {
        const deleted = await page.request.delete(`/api/books/${bookId}/progress`)
        expect(deleted.status(), 'FR-VWR-012 안읽음: DELETE …/progress answers 204').toBe(204)
        const body = (await (await page.request.get(`/api/books/${bookId}`)).json()) as {
          progress: { last_page: number } | null
        }
        return body.progress
      },
      {
        timeout: 15_000,
        message: `shelf.ts rule 2: book ${bookId} must be left without the progress row this spec invented`,
      },
    )
    .toBeNull()
}

/**
 * Sets one of the viewer's segmented controls (표시 모드 / 읽기 방향 / 맞춤).
 *
 * All three groups are inline at every width — the top bar wraps to two or
 * three rows rather than hiding controls behind a `⋯` sheet — so this is one
 * click at 400px and at 1440px alike.
 */
export async function setViewerSeg(page: Page, group: string, value: string): Promise<void> {
  await wakeChrome(page)
  const option = viewerTopBar(page).locator(`[aria-label="${group}"] label[data-value="${value}"]`)
  await option.click()
  await expect(option).toHaveAttribute('data-checked', 'true')
}

/**
 * Toggles the thumbnail strip through the bottom bar's own `썸네일 · T` button.
 *
 * Not the `T` key, for callers that have just used the mouse: `useViewerKeys`
 * drops every key whose `event.target` is `isTypingTarget`, and that includes an
 * `<input>` of any kind — so after dragging the page slider (an
 * `input[type=range]`, which keeps focus) the whole of ui-spec §8.2 is inert
 * until focus moves. The key path is asserted separately in 04-viewer.spec.ts,
 * from a page whose focus is still the body.
 */
export async function toggleStrip(page: Page): Promise<void> {
  await wakeChrome(page)
  await page.getByRole('button', { name: '썸네일 · T' }).click()
}

/** Waits until at least one page image has decoded on the stage. */
export async function waitForPage(page: Page): Promise<void> {
  await expect(page.locator('[data-role="page-frame"][data-status="ready"]').first()).toBeVisible({
    timeout: 30_000,
  })
}

/**
 * Waits for the debounced `PUT /api/books/{bid}/progress` to land.
 *
 * `useSaveProgress` buffers the page and sends it 1 s later, and it flushes that
 * buffer on unmount as well as on `visibilitychange` and `pagehide`
 * (`api/queries.ts`, and `useProgressSync`'s own docblock names the same three).
 * So leaving a book does not *lose* the write — but it does not make it instant
 * either, and a spec that navigated away and read the row straight back would be
 * racing its own client: FR-VWR-009 would fail for a reason that has nothing to
 * do with FR-VWR-009. This helper waits for the response instead.
 *
 * It had no caller for three sessions and was on notice for it — an unused
 * export is exactly how `clearSearch()` kept a viewport-dependent count
 * assertion alive here unnoticed. `09-viewer-chrome.spec.ts` is the caller that
 * closes that: E-27 took the *chrome* off the reading path and nothing else, so
 * that spec asserts a page turned with the bars asleep still reaches the server,
 * and it attaches this listener **before** the turn that causes the write for
 * the reason above. 04-viewer 6.6 keeps polling `GET /api/books/{bid}` instead,
 * which is the right shape there: it wants the row's final value, not the fact
 * that one request happened.
 */
export async function waitForProgressWrite(page: Page, bookId: string): Promise<void> {
  await page.waitForResponse(
    (response) =>
      response.url().includes(`/api/books/${bookId}/progress`) &&
      response.request().method() === 'PUT' &&
      response.ok(),
    { timeout: 15_000 },
  )
}
