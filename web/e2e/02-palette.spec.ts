/**
 * impl-plan §6.3 step 6, assertion 4 — the command palette (FR-LIB-011).
 *
 * `Ctrl+K` from anywhere → type the 초성 `ㅁㅅ` → `↵` opens 미생.
 */

import {
  expect,
  gotoLibrary,
  resetLibraryState,
  SERIES,
  seriesId,
  shot,
  test,
} from './shelf'

test.beforeEach(async ({ page }) => {
  await resetLibraryState(page)
})

test('6.4 · Ctrl+K → ㅁㅅ → ↵ opens the 미생 series', async ({ page }, info) => {
  const sid = await seriesId(page, SERIES.misaeng)

  await gotoLibrary(page)

  // ui-spec §8.1: the binding is global and works even from inside a text
  // field, which is why it is bound in `lib/useHotkeys.ts` and not on the input.
  await page.keyboard.press('Control+k')
  const field = page.getByLabel('시리즈로 이동…')
  await expect(field).toBeVisible()

  await shot(page, info, 'step-06-4a-palette-open')

  await field.fill('ㅁㅅ')

  // The palette's search is the server's too (C-10): `?q=ㅁㅅ&limit=8`.
  //
  // Scoped to the palette's own listbox. A bare `getByRole('option')` also
  // matches the five `<option>` elements of the top bar's 정렬 `<select>`,
  // which have the implicit ARIA role — it resolved to 6, and the extra five
  // would have made the count assertion pass for the wrong reason at some other
  // library size.
  const options = page.getByRole('listbox', { name: '검색 결과' }).getByRole('option')
  await expect(options).toHaveCount(1)
  await expect(options.first()).toContainText('미생')
  await shot(page, info, 'step-06-4b-palette-chosung')

  await field.press('Enter')

  await expect(page).toHaveURL(new RegExp(`/series/${sid}$`))
  await expect(page.getByRole('heading', { level: 2, name: SERIES.misaeng })).toBeVisible()
  await expect(page.getByLabel('시리즈로 이동…')).toHaveCount(0)

  await shot(page, info, 'step-06-4c-palette-opened-series')
})
