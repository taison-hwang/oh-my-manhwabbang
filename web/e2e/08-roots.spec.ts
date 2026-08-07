/**
 * Amendment **A-11** / ruling **E-26** — 루트 추가 and 제거 on the settings screen,
 * as amended by **A-12** / ruling **E-40**, which made an addition open into the
 * running server instead of waiting for a restart.
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
 * rendered under the capability, that the folder picker walks the allowlist and
 * that the server's `selectable`/`reason` reach the screen instead of being
 * re-derived there, that an added root comes up live — no 재시작 후 적용, a
 * 재스캔 offered, and no restart notice on the panel (A-12) — that a rejection
 * reaches the user as the sentence `rootErrors.ts` writes for it, and that a
 * removal takes the row off the screen at once.
 *
 * The pre-A-12 experience this used to assert — a pending row that says what
 * makes it real and offers no 재스캔 — is now the *fallback* for an adoption
 * that failed. That path is unreachable from a passing round here, and
 * `internal/httpapi/roots_test.go` pins it instead.
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

import { mkdir, readFile } from 'node:fs/promises'
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

/**
 * `Settings.server.config_changed_on_disk` (§7.8) off the wire.
 *
 * The panel's restart notice renders from this one boolean, and both places this
 * file asserts the notice is absent read it too. An assertion that only counted
 * the element would stay green if the element were deleted — §6.5's shape — so
 * each zero is pinned to the value that has to produce it.
 */
async function configChangedOnDisk(page: Page): Promise<boolean> {
  const response = await page.request.get('/api/settings')
  expect(response.ok(), 'GET /api/settings').toBe(true)
  const body = (await response.json()) as { server: { config_changed_on_disk: boolean } }
  return body.server.config_changed_on_disk
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

test('A-12 · 루트 추가 → 즉시 적용 → 제거, against a real server', async ({ page }, info) => {
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
  const { server } = (await settings.json()) as {
    server: { root_editing_enabled: boolean; config_path: string }
  }
  expect(
    server.root_editing_enabled,
    'A-11 · this round configures server.allow_root_editing: true (scripts/e2e-config.sh)',
  ).toBe(true)
  // A-10 publishes the file this server loaded, which is what makes the
  // byte-for-byte comparison at the end of this test possible from here.
  expect(server.config_path, 'A-10 · the server publishes the file it loaded').toBeTruthy()
  const configBeforeAdd = await readFile(server.config_path)

  // ---- the root that outlived a restart, as the user sees it -------------
  //
  // The same fact `--phase roots-post` asserts on the wire. It is worth both:
  // that phase proves a fresh process re-read the spliced entry, this proves
  // the screen agrees — the two could disagree, and the row is what a user
  // would believe. (`scripts/e2e.sh` step 10 deletes the index database before
  // that restart, so this row is not inherited from the POST's own scan.)
  expect(
    [adopted.pending, adopted.available],
    `${adoptedName} survived the restart still live`,
  ).toEqual([false, true])
  const adoptedRow = roots.locator(`[data-testid="root-row-${adoptedName}"]`)
  await expect(adoptedRow).toBeVisible()
  await expect(adoptedRow, 'a live root is not 재시작 후 적용').not.toContainText('재시작 후 적용')
  await expect(
    adoptedRow.getByRole('button', { name: '재스캔' }),
    'an adopted root is open, so it can be rescanned',
  ).toHaveCount(1)

  // ---- 추가, with the path PICKED rather than typed ------------------------
  //
  // AMENDMENT A-12 (ruling E-40) bought `GET /api/browse` and `FolderPicker`,
  // and both shipped with no e2e coverage of any kind — no round was run against
  // them at all. Driving the add through the picker is what closes that without
  // a second root per viewport project: typed entry stays covered by the
  // rejection block below, which types the media root's path directly.
  //
  // The base comes from the endpoint rather than from the layout of
  // `SHELF_E2E_UI_ROOT_DIR`, so this test asserts what the server allows instead
  // of restating what `scripts/e2e-config.sh` was told.
  const top = await page.request.get('/api/browse')
  expect(top.ok(), 'GET /api/browse').toBe(true)
  const { entries: bases } = (await top.json()) as { entries: { path: string }[] }
  const base = must(bases[0], 'scripts/e2e-config.sh configures exactly one browse base').path

  await roots.getByRole('button', { name: '루트 추가' }).click()
  await roots.locator('[data-testid="browse-folders"]').click()
  const picker = roots.locator('[data-testid="folder-picker"]')
  await expect(picker, 'A-12 · 찾아보기 opens the picker').toBeVisible()
  // At the top level there is no directory to name, so the crumb says what the
  // list *is*: an empty crumb reads as a path that failed to load.
  await expect(picker.locator('[data-testid="folder-picker-crumb"]')).toHaveText(
    '탐색할 수 있는 폴더',
  )
  await picker.getByRole('button', { name: base }).click()

  // The row of a directory that is already a root. `selectable` is computed by
  // the server from §7.4's own rules and this component may not re-derive it —
  // so what is asserted is that the server's answer reached the screen: the
  // reason is printed, and 선택 is disabled while descending stays allowed.
  const mediaRow = picker.locator('li').filter({ hasText: path.basename(media.path) })
  await expect(
    mediaRow,
    'A-12 · the picker prints the server-computed reason rather than a bare grey row',
  ).toContainText('이미 등록된 루트')
  await expect(
    mediaRow.getByRole('button', { name: '선택' }),
    'A-12 · a configured root cannot be chosen',
  ).toBeDisabled()

  await picker.getByRole('button', { name: path.basename(scratch) }).click()
  const pickRow = picker.locator('li').filter({ hasText: name })
  await expect(
    pickRow.getByRole('button', { name: '선택' }),
    'A-12 · a directory no root holds can be chosen',
  ).toBeEnabled()
  await pickRow.getByRole('button', { name: '선택' }).click()

  // Picking fills the field instead of submitting: the label is still to be
  // typed, and a picker that added the root on click would make the one
  // irreversible-looking control in this form the one with no confirm.
  await expect(picker, 'A-12 · picking closes the picker').toHaveCount(0)
  await expect(
    roots.getByLabel('루트 경로'),
    'A-12 · the picked path landed in the field, and it is the absolute one',
  ).toHaveValue(dir)

  await roots.getByLabel('이름표').fill(name)
  await roots.getByRole('button', { name: '추가', exact: true }).click()

  // AMENDMENT A-12 (ruling E-40) rewrote every assertion in this block. Under
  // A-11 the POST touched the file and nothing else, so the row appeared saying
  // 재시작 후 적용, offered no 재스캔, and the panel carried a restart notice.
  // The POST now opens the root into the running server, so the user sees a
  // working root instead of a promise — and this is the only tier that grades
  // what they actually see. `internal/httpapi/roots_test.go` pins the fallback
  // (an adoption that fails is still a 201, and falls back to the pending row).
  const row = roots.locator(`[data-testid="root-row-${name}"]`)
  await expect(row, 'A-12 · the added root appears at once, without a restart').toBeVisible()
  await expect(row).toContainText(dir)
  await expect(
    row,
    'A-12 · the row is live, so it does not ask for the restart A-11 needed',
  ).not.toContainText('재시작 후 적용')
  await expect(
    row.getByRole('button', { name: '재스캔' }),
    'A-12 · the root is open in this server, so it can be rescanned without a restart',
  ).toHaveCount(1)
  await expect(
    roots.locator('[data-testid="config-changed-notice"]'),
    'A-12 · the POST adopted its own write, so this process and the file agree and there ' +
      'is no restart to ask for (config_changed_on_disk, §7.8)',
  ).toHaveCount(0)
  expect(
    await configChangedOnDisk(page),
    'A-12 · and the zero above is the server\'s answer, not a missing element',
  ).toBe(false)

  const added = must(
    (await rootsFromApi(page)).find((root) => root.name === name),
    `POST /api/roots created ${name}`,
  )
  expect(
    [added.pending, added.available, added.series_count, added.path],
    'A-12 · a live row carries zero counts for an empty directory, is available, and names ' +
      'the directory written',
  ).toEqual([false, true, 0, dir])

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
  // document, so add-then-remove returns the file byte for byte. Under A-11 the
  // absence of the restart notice was the proxy for that: the notice went up on
  // the add and could only come back down if the bytes matched again.
  //
  // AMENDMENT A-12 took that proxy away — the add no longer raises the notice at
  // all — so an assertion that only counts the notice would now pass without
  // ever having measured the thing it names. That is §6.5 exactly, so the bytes
  // are read instead of inferred. No unit test of ours makes this promise
  // against a *running server's own* configuration file.
  expect(
    await readFile(server.config_path),
    'add-then-remove left the configuration file exactly as this server loaded it, byte for byte',
  ).toEqual(configBeforeAdd)
  await expect(
    roots.locator('[data-testid="config-changed-notice"]'),
    'and the panel agrees: nothing on disk is waiting for a restart',
  ).toHaveCount(0)
  // AMENDMENT A-13 (ruling E-41). The count above is pinned to the value that
  // produces it, because an absent element is not evidence: deleting the notice
  // block from `RootsPanel.tsx` would leave every `toHaveCount(0)` in this file
  // green. This is the flag the panel renders from, read from the wire.
  expect(
    await configChangedOnDisk(page),
    'A-13 · the DELETE moved the adopted digest back, so the server itself says false',
  ).toBe(false)

  // ---- and the picker got the directory back (A-13) -----------------------
  //
  // Before E-41, `configuredRoots()` kept a removed root forever, so this row
  // stayed greyed out as 이미 등록된 루트 while `POST /api/roots` would have
  // accepted the very same path. That is the picker-vs-endpoint drift
  // `FolderPicker.tsx` is built to prevent, wearing the server's authority —
  // and it is only reachable through a removal, which is why it belongs here
  // and not in the add block above.
  await roots.getByRole('button', { name: '루트 추가' }).click()
  await roots.locator('[data-testid="browse-folders"]').click()
  await picker.getByRole('button', { name: base }).click()
  await picker.getByRole('button', { name: path.basename(scratch) }).click()
  const freedRow = picker.locator('li').filter({ hasText: name })
  await expect(
    freedRow,
    'A-13 · the removed directory is no longer called a root',
  ).not.toContainText('이미 등록된 루트')
  await expect(
    freedRow.getByRole('button', { name: '선택' }),
    'A-13 · and it can be chosen again, which is what POST would now answer',
  ).toBeEnabled()
  await picker.getByRole('button', { name: '취소' }).click()
  await roots.getByRole('button', { name: '취소' }).click()
})
