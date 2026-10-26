/**
 * impl-plan §6.3 step 6, assertion 10 — the 설정 dialog.
 *
 * Cache usage is a real number; the theme `.seg` flips `<html data-theme>` and
 * the viewer stays dark either way (NFR-CMP-003); 루트 관리 carries the
 * add/remove controls exactly when the server says it may (C-5 / ruling E-3, as
 * amended by A-11 / ruling E-26); and the scan log carries the WARN rows
 * FR-IDX-010 produced for the collection's broken archives.
 */

import type { Locator, Page } from '@playwright/test'

import {
  booksOf,
  expect,
  gotoLibrary,
  openSettings,
  resetLibraryState,
  ROOT_EDITING_ENABLED,
  rootsSection,
  SERIES,
  seriesId,
  shot,
  test,
  viewer,
  waitForPage,
} from './shelf'

test.beforeEach(async ({ page }) => {
  await resetLibraryState(page)
})

test('6.10 · cache, theme, read-only roots and the scan log', async ({ page }, info) => {
  // arch §7.9: usage is `GET /api/cache/usage`. `/api/cache` is the *purge*
  // route and accepts DELETE only, so a GET there is a 405 envelope whose
  // `total_bytes` is `undefined` — which reads as a failure of the cache
  // rather than of the URL. Assert the response first, then the field.
  const response = await page.request.get('/api/cache/usage')
  expect(response.ok(), 'GET /api/cache/usage').toBe(true)
  const cache = (await response.json()) as { total_bytes: number }
  expect(cache.total_bytes, 'the scan and step 7 have populated the cache').toBeGreaterThan(0)

  await gotoLibrary(page)
  await openSettings(page)
  const dialog = page.getByRole('dialog', { name: '설정' })

  // ---- 캐시 (FR-THM-008) --------------------------------------------------
  const cacheSection = dialog
    .locator('section')
    .filter({ has: page.getByRole('heading', { name: '캐시', exact: true }) })
  await expect(cacheSection).toContainText(/\d/)
  await expect(cacheSection).toContainText('thumbs')
  await expect(cacheSection.getByRole('button', { name: '전체 삭제' })).toBeVisible()
  const shown = (await cacheSection.innerText()).replace(/\s+/g, ' ')
  expect(shown, 'the usage figure must not read zero').not.toMatch(/(^|\s)0 (KB|MB|GB)/)

  // ---- 루트 관리 (C-5 / E-3, as amended by A-11 / E-26) -------------------
  const roots = rootsSection(page)
  await expect(roots.getByRole('button', { name: '재스캔' }).first()).toBeVisible()
  await expect(roots).toContainText('shelf.yaml을 편집한 뒤 재시작하세요')
  // Amendment A-10 / ruling E-25 — the note has to name the file, and this is
  // the only gate that sees a real server's real config path. Same shape as the
  // cache assertion above: read the contract, then require the UI to show it.
  const settingsResponse = await page.request.get('/api/settings')
  expect(settingsResponse.ok(), 'GET /api/settings').toBe(true)
  const { server } = (await settingsResponse.json()) as {
    server: { config_path: string; root_editing_enabled: boolean }
  }
  expect(server.config_path, 'server.config_path is absolute').toMatch(/^([/]|[A-Za-z]:[\\/])/)
  await expect(roots).toContainText(server.config_path)

  // The capability FIRST, and then the controls — because the controls being
  // absent is not by itself evidence of anything.
  //
  // This block asserted `toHaveCount(0)` over three labels and called it
  // "removed, not disabled". It passed, and it would have passed just as
  // happily against a build where A-11's controls were never written, against
  // one where they were written and broken, and against one where they render
  // only when the gate is open — which is what actually ships. What made the
  // difference invisible was that `test/shelf.e2e.yaml.tmpl` never set
  // `server.allow_root_editing`, so the gate was shut in every round and the
  // count was zero for a reason the assertion never named. That is
  // docs/HANDOFF.md §6.5 exactly: a check watching something adjacent to what
  // ships.
  //
  // So: read the capability off the wire, hold it against what this round's
  // configuration is supposed to be (`ROOT_EDITING_ENABLED`, derived from
  // SHELF_E2E_MODE), and only then count buttons — with the counts derived from
  // the same fact. The real round still proves "no add/remove control", and now
  // proves *why*; the synthetic round proves the controls appear when the
  // capability is granted, which is the half nothing checked before.
  expect(
    server.root_editing_enabled,
    `A-11 · the gate this round configures: server.allow_root_editing is ` +
      `${String(ROOT_EDITING_ENABLED)} in the ${ROOT_EDITING_ENABLED ? 'synthetic' : 'real'} ` +
      `round (scripts/e2e-config.sh). Everything below reads its meaning from this.`,
  ).toBe(ROOT_EDITING_ENABLED)

  const rootRows = roots.locator('[data-testid^="root-row-"]')
  await expect(rootRows.first(), 'the panel lists at least one root').toBeVisible()
  const rowCount = await rootRows.count()
  // Substring matching is Playwright's default for `getByRole` names and is
  // wanted here: '추가' is also how '루트 추가' is spelt, so a round with the
  // gate shut cannot pass this by renaming the button. 제거 is one per row.
  const expectedControls: readonly (readonly [string, number])[] = ROOT_EDITING_ENABLED
    ? ([
        ['루트 추가', 1],
        ['추가', 1],
        ['제거', rowCount],
      ] as const)
    : ([
        ['루트 추가', 0],
        ['추가', 0],
        ['제거', 0],
      ] as const)
  expect(expectedControls, 'the control expectation must never be an empty list').toHaveLength(3)
  for (const [label, count] of expectedControls) {
    await expect(
      roots.getByRole('button', { name: label }),
      `루트 관리 · ${label} × ${String(count)} with root_editing_enabled=${String(
        ROOT_EDITING_ENABLED,
      )}`,
    ).toHaveCount(count)
  }

  // ---- 스캔 로그 (FR-IDX-004 / FR-IDX-010) --------------------------------
  const log = dialog.locator('[data-testid="scan-log-body"]')
  await expect(log).toBeVisible()
  await expect(log).toContainText('WARN')

  await shot(page, info, 'step-06-10a-settings')

  // ---- 테마 (NFR-CMP-003) -------------------------------------------------
  const themeSeg = dialog.locator('[aria-label="테마"]')
  await themeSeg.locator('label[data-value="dark"]').click()
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark')
  await shot(page, info, 'step-06-10b-settings-dark')

  await themeSeg.locator('label[data-value="light"]').click()
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'light')
  // The viewer below is a fresh load, and a fresh load takes its theme from the
  // server (ShellDataProvider hydrates it on every route). The PUT behind this
  // click is debounced (settingsWriter, 400 ms), so navigating before it lands
  // would hand the viewer the *previous* theme from GET /api/settings.
  await expect
    .poll(async () => ((await (await page.request.get('/api/settings')).json()) as { theme: string }).theme, {
      message: 'NFR-CMP-003: the light theme is saved before the next page reads it',
    })
    .toBe('light')

  await page.getByRole('dialog', { name: '설정' }).getByRole('button', { name: '닫기' }).click()
  await expect(page.getByRole('dialog', { name: '설정' })).toHaveCount(0)

  // ---- …and the viewer is dark in both app themes -------------------------
  const sid = await seriesId(page, SERIES.clover)
  const books = await booksOf(page, sid)
  const bid = books.find((book) => book.status === 'ok')?.id ?? ''
  await page.goto(`/series/${sid}/books/${bid}?page=1`)
  await expect(viewer(page)).toBeVisible()
  await waitForPage(page)
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'light')
  await expect(viewer(page)).toHaveAttribute('data-theme', 'dark')

  await shot(page, info, 'step-06-10c-viewer-dark-under-light-theme')

  await page.keyboard.press('Escape')
})

// ---------------------------------------------------------------------------
// HANDOFF §5.7.11 `ay` — the 읽기 기본값 panel's three `Seg`s, by keyboard
// ---------------------------------------------------------------------------

/** One key press of a walk across a `Seg`, and the option it has to land on. */
interface SegStep {
  readonly key: 'ArrowRight' | 'ArrowLeft'
  readonly value: string
  readonly why: string
}

/**
 * → from the first option to the last, → once more for the wrap back to the
 * first, then ← for the wrap the other way, round to the last.
 *
 * A copy of 09-viewer-chrome.spec.ts's `segWalk`, which carries the reasoning;
 * both belong in shelf.ts, which had another owner the day these were written.
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

/** A copy of 09-viewer-chrome.spec.ts's `walkSeg`; see the note on `segWalk`. */
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
    `${rule}: the walk starts on ${first}, which setup put there`,
  ).toHaveAttribute('data-checked', 'true')
  await expect(
    group.locator(`input[value="${first}"]`),
    `${rule}: a click on the label focuses the radio, or every key below goes to the document`,
  ).toBeFocused()

  for (const step of segWalk(values)) {
    await page.keyboard.press(step.key)
    const why = `${rule}: ${step.why}`
    await expect(group.locator(`label[data-value="${step.value}"]`), why).toHaveAttribute(
      'data-checked',
      'true',
    )
    await expect(group.locator(`input[value="${step.value}"]`), why).toBeFocused()
    await landed(step.value, why)
  }
}

interface ReadDefaultsFact {
  reading_direction: string
  display_mode: string
  theme: string
}

async function readDefaults(page: Page): Promise<ReadDefaultsFact> {
  const response = await page.request.get('/api/settings')
  expect(response.ok(), 'GET /api/settings').toBe(true)
  const body = (await response.json()) as ReadDefaultsFact
  return {
    reading_direction: body.reading_direction,
    display_mode: body.display_mode,
    theme: body.theme,
  }
}

/**
 * The panel's three groups, in `ReadDefaultsPanel.tsx` option order — copies of
 * `DIR_OPTIONS` / `MODE_OPTIONS` / `THEME_OPTIONS` on the same terms as `SERIES`
 * in shelf.ts, because order is the key mapping and a reorder is meant to redden
 * this. The first of each is what the test's setup `PUT` puts the panel on.
 */
const READ_DEFAULT_SEGS = [
  { group: '읽기 방향', rule: 'UI-004 · 읽기 방향', field: 'reading_direction', values: ['ltr', 'rtl'] },
  {
    group: '표시 모드',
    rule: 'UI-004 · 표시 모드',
    field: 'display_mode',
    values: ['single', 'spread', 'vertical'],
  },
  { group: '테마', rule: 'NFR-CMP-003 · 테마', field: 'theme', values: ['light', 'dark', 'system'] },
] as const satisfies readonly {
  group: string
  rule: string
  field: keyof ReadDefaultsFact
  values: readonly string[]
}[]

/**
 * `ay` · 읽기 방향, 표시 모드 and 테마 in the 설정 dialog, by keyboard.
 *
 * `Seg` has no keydown handler (`components/ds/Seg.tsx`); the ←/→ walk is the
 * browser's radio-group behaviour and jsdom does not implement it, so before
 * this test the only `Seg` whose keyboard claim anything checked was the
 * viewer's 맞춤 (09-viewer-chrome, E-44).
 *
 * These three have a trap the viewer's do not. `ReadDefaultsPanel` drops every
 * change while a save is in flight (`disabled = … || save.isPending`), and its
 * two read-default groups are not optimistic — the radio is controlled by the
 * settings query, so a key pressed mid-save is checked by the browser and then
 * put back by React. That is why each stop waits for the server before the next
 * key: `data-checked` on these two only moves once the `PUT` has answered, and
 * the poll below reads the row back. A walk that pressed all its keys at once
 * would be testing the guard, not the keyboard.
 *
 * Effects: the global default each group writes (`PUT /api/settings`, arch
 * §7.6), and for 테마 also the one the dialog exists to change on the spot —
 * `<html data-theme>` (NFR-CMP-003), which 6.10 above checks for clicks.
 * 시스템 resolves through `prefers-color-scheme`, so its expected value is read
 * from the page rather than assumed from Playwright's default.
 *
 * shelf.ts rule 2: `beforeEach` resets the theme but nothing resets the read
 * defaults, so all three are put back to what they were in `finally`.
 */
for (const seg of READ_DEFAULT_SEGS) {
  test(`ay · 설정 ${seg.group} — 화살표로 전부 닿고, 양 끝에서 양쪽으로 감긴다`, async ({ page }) => {
    const original = await readDefaults(page)
    try {
      // Setup, over the API, for the reason `resetLibraryState` gives: the UI
      // half of the test still has to do every switch itself.
      const first = seg.values[0]
      const put = await page.request.put('/api/settings', { data: { [seg.field]: first } })
      expect(put.ok(), 'PUT /api/settings').toBe(true)

      await gotoLibrary(page)
      await openSettings(page)
      const group = page.getByRole('dialog', { name: '설정' }).locator(`[aria-label="${seg.group}"]`)

      await walkSeg(page, group, seg.values, seg.rule, async (value, why) => {
        await expect
          .poll(async () => (await readDefaults(page))[seg.field], {
            message: `${why} — and it is the saved default`,
          })
          .toBe(value)
        if (seg.field === 'theme') {
          const resolved =
            value === 'system'
              ? await page.evaluate(() =>
                  window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light',
                )
              : value
          await expect(
            page.locator('html'),
            `${why} — and <html data-theme> follows it on the spot`,
          ).toHaveAttribute('data-theme', resolved)
        }
      })
    } finally {
      const restore = await page.request.put('/api/settings', { data: original })
      expect(restore.ok(), 'shelf.ts rule 2: PUT /api/settings back to what it was').toBe(true)
    }
  })
}
