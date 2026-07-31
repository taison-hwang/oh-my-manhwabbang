/**
 * Amendment **A-11** / ruling **E-26** — 루트 추가 and 제거 on the settings screen.
 *
 * # Why this file exists
 *
 * 06-settings.spec.ts asserted `toHaveCount(0)` over `['루트 추가', '추가',
 * '제거']` and called it "removed, not disabled". That assertion was green
 * against a build where these controls did not exist, and it stayed green after
 * A-11 shipped them — because `test/shelf.e2e.yaml.tmpl` never set
 * `server.allow_root_editing`, so the gate was shut in every round and the count
 * was zero for a reason nothing named. The write path — a `POST` that edits a
 * real YAML file, a pending row, a restart that adopts it, a `DELETE` that
 * purges an index and keeps `user.db` — had no end-to-end coverage of any kind.
 *
 * This spec is the browser half of closing that. The HTTP half is
 * `scripts/e2e-assert.py --phase roots-{pre,post,delete}`, called from three
 * points of `scripts/e2e.sh`; the two halves cover different things and neither
 * replaces the other. What only a browser can show is that the *controls* are
 * rendered under the capability, that a pending row says what makes it real and
 * offers no 재스캔, that a rejection reaches the user as the sentence
 * `rootErrors.ts` writes for it, and that a removal takes the row off the screen
 * at once.
 *
 * # Why it runs in the synthetic round only
 *
 * The round decides the configuration, and only one of the two may be written
 * to. `scripts/e2e.sh --synthetic` puts both the fixture tree and the
 * configuration file under a /tmp state directory, so `POST /api/roots` edits a
 * real file, takes a real `.bak`, and threatens nothing. The real-collection
 * round's configuration is `test/shelf.e2e.yaml` **inside the repository**,
 * where step 9 fails the run for creating anything — so that round keeps
 * `server.allow_root_editing` off, which is also the value that ships, and
 * 06-settings.spec.ts asserts the controls are absent *and* that the capability
 * is off, so "absent" is pinned to its cause there instead of being asserted
 * twice here.
 *
 * There are deliberately no `shot()` calls: `scripts/e2e.sh` step 11 requires a
 * fresh screenshot for every `shot()` call it can grep out of `web/e2e/*.spec.ts`,
 * and a call in a spec that is skipped in one of the two rounds would fail that
 * round for a screenshot nobody could have produced.
 */

import { mkdir } from 'node:fs/promises'
import path from 'node:path'

import type { Page } from '@playwright/test'

import {
  expect,
  expectConsoleError,
  gotoLibrary,
  openSettings,
  resetLibraryState,
  rootsSection,
  SYNTHETIC,
  test,
} from './shelf'

/** One row of `GET /api/roots` (arch §7.3, plus R2's `pending`). */
interface RootFact {
  name: string
  label: string
  path: string
  enabled: boolean
  available: boolean
  pending: boolean
  series_count: number
}

async function rootsFromApi(page: Page): Promise<RootFact[]> {
  const response = await page.request.get('/api/roots')
  expect(response.ok(), 'GET /api/roots').toBe(true)
  const body = (await response.json()) as { items: RootFact[] }
  return body.items
}

/**
 * Narrows away an `undefined` that has just been asserted not to be one.
 *
 * The assertion is the `expect`; the `throw` is unreachable and exists only so
 * that the caller gets a `T` instead of a `T | undefined`. Written this way
 * round — assert, then narrow — rather than as `if (x) { …assert… }`, which is
 * how a broken precondition makes coverage disappear instead of failing
 * (docs/HANDOFF.md §6.5).
 */
function must<T>(value: T | undefined, why: string): T {
  expect(value, why).toBeDefined()
  if (value === undefined) throw new Error(why)
  return value
}

/** An environment variable `scripts/e2e.sh` is required to have set. */
function requireEnv(name: string): string {
  const value = process.env[name]
  expect(
    value,
    `${name} must be set by scripts/e2e.sh (run_playwright) for the synthetic round`,
  ).toBeTruthy()
  return value ?? ''
}

test('A-11 · 루트 추가 → 재시작 후 적용 → 제거, against a real server', async ({ page }, info) => {
  test.skip(
    !SYNTHETIC,
    'A-11 write path: the synthetic round only, whose configuration file and fixture tree both ' +
      'live under /tmp. The real round keeps server.allow_root_editing off — the shipped ' +
      'default — and 06-settings.spec.ts asserts the controls are absent and why.',
  )

  // The root `scripts/e2e.sh` step 8b added before the restart and step 10b saw
  // adopted, and a scratch directory this spec may create its own roots under.
  const adoptedName = requireEnv('SHELF_E2E_A11_ROOT')
  const scratch = requireEnv('SHELF_E2E_UI_ROOT_DIR')
  // One root per viewport project, because all four run against one server and
  // one configuration file. The name is what §7.4's generator makes of the
  // label, and `ui-desktop-1440` is already a valid slug, so the two are equal
  // and the row's testid is predictable.
  const name = `ui-${info.project.name}`
  const dir = path.join(scratch, name)
  await mkdir(dir, { recursive: true })

  await resetLibraryState(page)

  // ---- the wire, before the UI touches anything --------------------------
  const before = await rootsFromApi(page)
  expect(
    before.map((root) => root.name).sort(),
    'this project starts from the two roots scripts/e2e.sh configured and added — a third ' +
      'means an earlier viewport project failed before it removed its own',
  ).toHaveLength(2)
  const adopted = must(
    before.find((root) => root.name === adoptedName),
    `scripts/e2e.sh step 8b added a root named ${adoptedName} and step 10b saw it adopted`,
  )
  const media = must(
    before.find((root) => root.name !== adoptedName),
    'the media root is still configured',
  )

  await gotoLibrary(page)
  await openSettings(page)
  const roots = rootsSection(page)

  // The capability first. Every assertion below is about a control that only
  // exists when the server grants it, so a round that did not grant it would
  // otherwise fail with "locator not found" rather than with the truth.
  const settings = await page.request.get('/api/settings')
  expect(settings.ok(), 'GET /api/settings').toBe(true)
  const { server } = (await settings.json()) as { server: { root_editing_enabled: boolean } }
  expect(
    server.root_editing_enabled,
    'A-11 · this round configures server.allow_root_editing: true (scripts/e2e-config.sh)',
  ).toBe(true)

  // ---- restart adoption, as the user sees it -----------------------------
  //
  // The same fact `--phase roots-post` asserts on the wire. It is worth both:
  // that phase proves the server adopted the root, this proves the screen stops
  // calling it pending — the two could disagree, and the row is what a user
  // would believe.
  expect([adopted.pending, adopted.available], `${adoptedName} was adopted by the restart`).toEqual(
    [false, true],
  )
  const adoptedRow = roots.locator(`[data-testid="root-row-${adoptedName}"]`)
  await expect(adoptedRow).toBeVisible()
  await expect(adoptedRow, 'an adopted root is no longer 재시작 후 적용').not.toContainText(
    '재시작 후 적용',
  )
  await expect(
    adoptedRow.getByRole('button', { name: '재스캔' }),
    'an adopted root is open, so it can be rescanned',
  ).toHaveCount(1)

  // ---- 추가 ---------------------------------------------------------------
  await roots.getByRole('button', { name: '루트 추가' }).click()
  await roots.getByLabel('루트 경로').fill(dir)
  await roots.getByLabel('이름표').fill(name)
  await roots.getByRole('button', { name: '추가', exact: true }).click()

  const row = roots.locator(`[data-testid="root-row-${name}"]`)
  await expect(row, 'R2 · the added root appears at once, without a restart').toBeVisible()
  await expect(row).toContainText(dir)
  await expect(
    row,
    'R2 · the row says what makes it real, instead of counts it does not have',
  ).toContainText('재시작 후 적용')
  await expect(
    row.getByRole('button', { name: '재스캔' }),
    'R2 · nothing is open to scan until the restart, so there is no 재스캔 to offer',
  ).toHaveCount(0)
  await expect(
    roots.locator('[data-testid="config-changed-notice"]'),
    "A-11 · the restart notice is server state (config_changed_on_disk), not this tab's",
  ).toContainText('서버를 다시 시작하면 적용됩니다')

  const added = must(
    (await rootsFromApi(page)).find((root) => root.name === name),
    `POST /api/roots created ${name}`,
  )
  expect(
    [added.pending, added.available, added.series_count, added.path],
    'R2 · a pending row carries zero counts, is unavailable, and names the directory written',
  ).toEqual([true, false, 0, dir])

  // ---- a rejection, as the user meets it ---------------------------------
  //
  // §7.4 answers every refusal with a `detail.reason` precisely so the client
  // can say which of nine rules broke; `web/src/features/roots/rootErrors.ts`
  // is the table of sentences, and this form is the only place they are ever
  // rendered. `duplicate` is the one a user reaches without help: the media
  // root's path is on the screen above, and re-adding it names the root it
  // collides with. (`overlaps` and `contains_storage` are asserted on the wire
  // by `scripts/e2e-assert.py --phase roots-pre`.)
  //
  // The refusal is a 400, and Chromium logs every non-2xx response as a console
  // error, so it is declared: NFR-CMP-001's guard still fails this test for any
  // *other* console error, and it fails it too if this one does not happen.
  expectConsoleError(
    page,
    /Failed to load resource: the server responded with a status of 400/,
    'the deliberate duplicate-root POST below is answered 400 by design (§7.4)',
  )
  await roots.getByRole('button', { name: '루트 추가' }).click()
  await roots.getByLabel('루트 경로').fill(media.path)
  await roots.getByRole('button', { name: '추가', exact: true }).click()
  await expect(
    roots.getByRole('alert'),
    'the refusal names the rule and the root it collided with, not "실패했습니다"',
  ).toContainText(`이미 ‘${media.name}’ 루트로 등록된 폴더입니다.`)
  await roots.getByRole('button', { name: '취소' }).click()
  await expect(roots.getByRole('button', { name: '루트 추가' })).toBeVisible()

  // ---- 제거 ---------------------------------------------------------------
  await row.getByRole('button', { name: '제거' }).click()
  await expect(
    row.getByRole('alert'),
    'R1 · the confirmation promises the reading progress, and must not promise the index rows',
  ).toContainText('읽기 진행률은 유지됩니다')
  await row.getByRole('button', { name: '제거' }).click()

  await expect(row, 'R1 · a removal takes effect now: the row goes at once').toHaveCount(0)
  await expect(
    roots.locator(`[data-testid="root-row-${media.name}"]`),
    'and it took nothing else with it',
  ).toBeVisible()
  expect(
    (await rootsFromApi(page)).map((root) => root.name).sort(),
    'the wire agrees with the screen',
  ).toEqual(before.map((root) => root.name).sort())

  // `internal/config/rootsfile.go` splices raw lines rather than re-emitting the
  // document, so add-then-remove returns the file byte for byte — which is the
  // only reason `config_changed_on_disk` can go back to false. A writer that
  // reformatted the user's file would leave this notice on screen forever, and
  // that is a promise no unit test of ours makes against a *running server's own*
  // configuration.
  await expect(
    roots.locator('[data-testid="config-changed-notice"]'),
    'add-then-remove left the configuration file exactly as this server loaded it',
  ).toHaveCount(0)
})
