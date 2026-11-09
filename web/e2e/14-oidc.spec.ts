/**
 * Ruling **E-74** in the browser: `auth.mode: oidc` against the real binary and
 * a fake uni-auth (`scripts/fakeoidc`). Run by `make e2e-synthetic-oidc`
 * (`scripts/e2e-oidc.sh`) under `playwright.oidc.config.ts`; the other rounds'
 * config ignores this file, because their servers have no oidc mode.
 *
 * Time is moved by rewriting the session row in `auth.db` (`reauth_at`,
 * `expires_at`), which is what fifteen minutes or twelve hours passing would
 * leave there; the server reads the row on every request (E-74 §2.7). The
 * server's own checks are not touched and nothing sleeps.
 */
import { execFileSync } from 'node:child_process'
import { createHash } from 'node:crypto'

import type { Browser, BrowserContext, Page } from '@playwright/test'

import {
  SERIES,
  expect,
  expectConsoleError,
  openSettings,
  openViewerDirect,
  seriesTile,
  test,
  waitForProgressWrite,
} from './shelf'

function env(name: string): string {
  const value = process.env[name]
  if (value === undefined || value === '') {
    throw new Error(`${name} is not set; run this spec through scripts/e2e-oidc.sh`)
  }
  return value
}

const BASE = env('PLAYWRIGHT_BASE_URL')
const ISSUER = env('SHELF_E2E_OIDC_ISSUER')
const AUTH_DB = env('SHELF_E2E_OIDC_AUTH_DB')
const SUB = {
  alice: env('SHELF_E2E_OIDC_ALICE'),
  bob: env('SHELF_E2E_OIDC_BOB'),
  mallory: env('SHELF_E2E_OIDC_MALLORY'),
} as const
type User = keyof typeof SUB

const SID_COOKIE = '__Host-shelf_sid'
const LOGIN_COOKIE = '__Host-shelf_login'
const WHEEL = '바퀴'

interface FakeEvent {
  endpoint: 'authorize' | 'end_session'
  prompt?: string
  outcome: string
  sub?: string
  id_token_hint?: boolean
}

async function fakeEvents(): Promise<FakeEvent[]> {
  const response = await fetch(`${ISSUER}/_fake/events`)
  return (await response.json()) as FakeEvent[]
}

/** Runs one statement against auth.db and returns how many rows it changed. */
function authDb(sql: string, ...params: string[]): number {
  const out = execFileSync(
    'python3',
    [
      '-I',
      '-c',
      'import sqlite3, sys\n' +
        'c = sqlite3.connect(sys.argv[1], timeout=10)\n' +
        'n = c.execute(sys.argv[2], sys.argv[3:]).rowcount\n' +
        'c.commit()\n' +
        'print(n)',
      AUTH_DB,
      sql,
      ...params,
    ],
    { encoding: 'utf8' },
  )
  return Number(out.trim())
}

async function sidCookie(context: BrowserContext) {
  return (await context.cookies()).find((c) => c.name === SID_COOKIE)
}

async function sessionHash(context: BrowserContext): Promise<string> {
  const cookie = await sidCookie(context)
  expect(cookie, `${SID_COOKIE} must be set`).toBeDefined()
  return createHash('sha256')
    .update(cookie?.value ?? '')
    .digest('hex')
}

/** Ages the session's last re-authentication to `minutesAgo` minutes before now. */
function setReauthAgo(hash: string, minutesAgo: number): void {
  const n = authDb(
    "UPDATE oidc_sessions SET reauth_at = CAST(strftime('%s','now') AS INTEGER) - ? WHERE id_hash = ?",
    String(minutesAgo * 60),
    hash,
  )
  expect(n, 'exactly one session row was aged').toBe(1)
}

/** The sign-in page of the fake: press the user's button. */
async function chooseUser(page: Page, user: User): Promise<void> {
  await expect(page.getByRole('heading', { name: 'fake uni-auth' })).toBeVisible()
  await page.getByRole('button', { name: `${user}로 로그인` }).click()
}

/**
 * Opens `path` without a session, which shows the server's sign-in page, and
 * signs in as `user`. Returns where the browser ended up.
 */
async function signIn(page: Page, user: User, path = '/'): Promise<URL> {
  await page.goto(path)
  await expect(page.getByText('계속하려면 로그인하세요.')).toBeVisible()
  await page.getByRole('link', { name: 'uni-auth로 로그인' }).click()
  await chooseUser(page, user)
  await page.waitForURL((url) => url.origin === new URL(BASE).origin && !url.pathname.startsWith('/api/'))
  return new URL(page.url())
}

/** A second browser for another user; its console is not guarded. */
async function otherUser(browser: Browser, user: User): Promise<{ context: BrowserContext; page: Page }> {
  const context = await browser.newContext({ baseURL: BASE })
  const page = await context.newPage()
  await signIn(page, user)
  return { context, page }
}

/** A write from this page's origin, as the SPA's own fetch sends it (E-74 §2.11). */
function sameOrigin(page: Page): Record<string, string> {
  return { Origin: new URL(page.url()).origin }
}

interface Facts {
  cloverSid: string
  cloverBook: string
  wheelSid: string
}

async function facts(page: Page): Promise<Facts> {
  const series = (await (await page.request.get('/api/series?limit=200&sort=name')).json()) as {
    items: { id: string; name: string }[]
  }
  const find = (name: string): string => {
    const hit = series.items.find((s) => s.name === name)
    expect(hit, `the oidc round's library must contain ${name}`).toBeDefined()
    return hit?.id ?? ''
  }
  const cloverSid = find(SERIES.clover)
  const detail = (await (await page.request.get(`/api/series/${cloverSid}`)).json()) as {
    books: { id: string }[]
  }
  return { cloverSid, cloverBook: detail.books[0]?.id ?? '', wheelSid: find(WHEEL) }
}

interface BookState {
  progress: { last_page: number } | null
  cover: unknown
  cover_cv: string
}

async function book(page: Page, bid: string): Promise<BookState> {
  const response = await page.request.get(`/api/books/${bid}`)
  expect(response.ok(), `GET /api/books/${bid}`).toBe(true)
  return (await response.json()) as BookState
}

test.describe.configure({ mode: 'serial' })

test.describe('E-74 oidc mode', () => {
  test('(a) a visit without a session signs in and comes back to the page it asked for', async ({
    page,
    context,
    browser,
  }) => {
    const { context: probe, page: probePage } = await otherUser(browser, 'alice')
    const { cloverSid } = await facts(probePage)
    await probe.close()

    const before = (await fakeEvents()).length
    const target = `/series/${cloverSid}?from=e2e`
    const landed = await signIn(page, 'alice', target)
    expect(landed.pathname + landed.search, 'E-74 §2.5: the callback returns to `return`').toBe(target)
    await expect(page.getByRole('heading', { name: SERIES.clover })).toBeVisible()

    const firstEntry = (await fakeEvents()).slice(before).filter((e) => e.endpoint === 'authorize')
    expect(firstEntry, 'a first entry carries no prompt (E-74 §2.5)').toEqual([
      { endpoint: 'authorize', outcome: 'page' },
      { endpoint: 'authorize', outcome: 'code', sub: SUB.alice },
    ])

    const cookie = await sidCookie(context)
    expect(cookie).toMatchObject({
      path: '/',
      domain: new URL(page.url()).hostname,
      httpOnly: true,
      secure: true,
      sameSite: 'Lax',
    })
    expect(cookie?.value).toMatch(/^[A-Za-z0-9_-]{43}$/)
    const ttl = (cookie?.expires ?? 0) - Date.now() / 1000
    expect(Math.abs(ttl - 2_592_000), 'Max-Age=2592000 (E-74 §2.4)').toBeLessThan(120)
    expect((await context.cookies()).map((c) => c.name)).not.toContain(LOGIN_COOKIE)

    const status = (await (await page.request.get('/api/auth/status')).json()) as {
      mode: string
      authenticated: boolean
      user?: { sub: string; is_admin: boolean }
      end_session_available: boolean
    }
    expect(status).toMatchObject({
      mode: 'oidc',
      authenticated: true,
      user: { sub: SUB.alice, is_admin: true },
      end_session_available: true,
    })
  })

  test('(b) progress is per user; the cover is shared', async ({ page, browser }) => {
    await signIn(page, 'alice')
    const { cloverSid, cloverBook } = await facts(page)

    const write = waitForProgressWrite(page, cloverBook)
    await openViewerDirect(page, cloverSid, cloverBook, 3)
    await write
    await expect.poll(async () => (await book(page, cloverBook)).progress?.last_page).toBe(3)
    const cover = await page.request.put(`/api/books/${cloverBook}/cover`, {
      headers: sameOrigin(page),
      data: { page: 2, x: 0, y: 0, w: 0.5, h: 0.5 },
    })
    expect(cover.status(), 'PUT …/cover is open to every allowed user (E-74 §2.10)').toBe(200)
    const aliceBook = await book(page, cloverBook)

    const bob = await otherUser(browser, 'bob')
    const bobBook = await book(bob.page, cloverBook)
    expect(bobBook.progress, "alice's progress must not reach bob (E-74 §2.13)").toBeNull()
    const cont = (await (await bob.page.request.get('/api/continue')).json()) as { items: unknown[] }
    expect(cont.items, "bob's 이어보기 is empty").toEqual([])
    expect(bobBook.cover, 'the cover is shared data').toEqual(aliceBook.cover)
    expect(bobBook.cover_cv).toBe(aliceBook.cover_cv)

    const bobWrite = await bob.page.request.put(`/api/books/${cloverBook}/progress`, {
      headers: sameOrigin(bob.page),
      data: { page: 1 },
    })
    expect(bobWrite.ok(), "bob's own progress write").toBe(true)
    expect((await book(bob.page, cloverBook)).progress?.last_page).toBe(1)
    expect((await book(page, cloverBook)).progress?.last_page, "bob's write leaves alice's row alone").toBe(3)
    await bob.context.close()
  })

  test('(c) a sub outside allowed_subs gets the 403 page and no session', async ({ page, context }) => {
    await page.goto('/')
    await page.getByRole('link', { name: 'uni-auth로 로그인' }).click()
    const refused = page.waitForResponse((r) => r.url().includes('/api/auth/oidc/callback'))
    await chooseUser(page, 'mallory')
    expect((await refused).status(), 'E-74 §2.6 step 7').toBe(403)
    await expect(page.getByText('이 계정은 이 서재에 들어올 수 없습니다', { exact: false })).toBeVisible()
    await expect(page.locator('code')).toHaveText(SUB.mallory)
    expect((await context.cookies()).map((c) => c.name)).not.toContain(SID_COOKIE)
    const status = (await (await page.request.get('/api/auth/status')).json()) as { authenticated: boolean }
    expect(status.authenticated).toBe(false)
  })

  test('(d) a non-admin sees no admin controls and is refused admin_only', async ({ page, browser }) => {
    await signIn(page, 'alice')
    const { cloverSid } = await facts(page)
    const bob = await otherUser(browser, 'bob')

    for (const [who, p, admin] of [
      ['alice', page, true],
      ['bob', bob.page, false],
    ] as const) {
      const n = admin ? 1 : 0
      await p.goto(`/series/${cloverSid}`)
      await expect(p.getByRole('heading', { name: SERIES.clover })).toBeVisible()
      await expect(p.getByRole('button', { name: '이 시리즈 재스캔' }), who).toHaveCount(n)
      await openSettings(p)
      const dialog = p.getByRole('dialog', { name: '설정' })
      await expect(dialog.getByTestId('account-panel'), who).toContainText(admin ? '관리자' : SUB.bob.slice(0, 8))
      if (!admin) await expect(dialog.getByTestId('account-panel')).not.toContainText('관리자')
      await expect(dialog.getByRole('heading', { name: '스캔 로그' }), who).toHaveCount(n)
      await expect(dialog.getByRole('button', { name: '전체 삭제' }), who).toHaveCount(n)
      await expect(dialog.getByTestId('root-row-mangga').getByRole('button', { name: '재스캔' }), who).toHaveCount(n)
      const roots = (await (await p.request.get('/api/roots')).json()) as { items: { path: string }[] }
      expect(roots.items[0]?.path !== '', `${who}: Root.path is admin information (E-74 §2.10)`).toBe(admin)
    }

    for (const [method, url] of [
      ['POST', '/api/scan'],
      ['POST', `/api/series/${cloverSid}/rescan`],
      ['DELETE', '/api/cache'],
      ['GET', '/api/scan/log'],
    ] as const) {
      const response = await bob.page.request.fetch(url, {
        method,
        headers: sameOrigin(bob.page),
        ...(method === 'POST' && url === '/api/scan' ? { data: { full: false } } : {}),
      })
      const body = (await response.json()) as { error?: { detail?: { reason?: string } } }
      expect(`${String(response.status())} ${body.error?.detail?.reason ?? '-'}`, `${method} ${url}`).toBe(
        '403 admin_only',
      )
    }
    await bob.context.close()
  })

  test('(e) an admin write after 15 minutes asks to sign in again and goes on', async ({ page, context }) => {
    expectConsoleError(page, /status of 403/, 'the refused rescan is the subject of this test')
    await signIn(page, 'alice')
    const { cloverSid } = await facts(page)
    const seriesPath = `/series/${cloverSid}`
    await page.goto(seriesPath)
    const rescan = page.getByRole('button', { name: '이 시리즈 재스캔' })
    const rescanResponse = () =>
      page.waitForResponse((r) => r.url().endsWith(`/api/series/${cloverSid}/rescan`) && r.request().method() === 'POST')
    const dialog = page.getByRole('dialog', { name: '다시 로그인' })

    // A first sign-in never counts as a re-authentication (E-74 §2.9).
    let answer = rescanResponse()
    await rescan.click()
    expect((await answer).status()).toBe(403)
    await expect(dialog).toBeVisible()

    const before = (await fakeEvents()).length
    await dialog.getByRole('button', { name: '다시 로그인' }).click()
    await chooseUser(page, 'alice')
    await page.waitForURL((url) => url.pathname === seriesPath)
    expect((await fakeEvents()).slice(before)[0], 'reauth sends prompt=login').toEqual({
      endpoint: 'authorize',
      prompt: 'login',
      outcome: 'page',
    })

    const hash = await sessionHash(context)
    setReauthAgo(hash, 14)
    await expect(rescan).toBeEnabled()
    answer = rescanResponse()
    await rescan.click()
    expect((await answer).ok(), '14 minutes after reauth the admin write goes through').toBe(true)

    setReauthAgo(hash, 16)
    await expect(rescan).toBeEnabled({ timeout: 30_000 })
    answer = rescanResponse()
    await rescan.click()
    const refused = await answer
    const body = (await refused.json()) as { error?: { detail?: { reason?: string } } }
    expect(`${String(refused.status())} ${body.error?.detail?.reason ?? '-'}`).toBe('403 reauth_required')
    await expect(dialog).toBeVisible()
  })

  test('(f) an expired session re-authorizes by itself; a reasonless 401 shows the sign-in page', async ({
    page,
    context,
    browser,
  }) => {
    expectConsoleError(page, /status of 401/, 'the expired session is the subject of this test')
    await signIn(page, 'alice')
    const { wheelSid } = await facts(page)
    await page.goto('/')
    await expect(seriesTile(page, WHEEL)).toBeVisible()

    const oldHash = await sessionHash(context)
    expect(
      authDb("UPDATE oidc_sessions SET expires_at = CAST(strftime('%s','now') AS INTEGER) - 1 WHERE id_hash = ?", oldHash),
    ).toBe(1)
    const before = (await fakeEvents()).length
    await seriesTile(page, WHEEL).click()
    await page.waitForURL((url) => url.pathname === `/series/${wheelSid}` && !url.search.includes('code='))
    await expect(page.getByRole('heading', { name: WHEEL })).toBeVisible()
    expect(
      (await fakeEvents()).slice(before),
      'session_expired → start → prompt=none, answered without the sign-in page (E-74 §2.7)',
    ).toEqual([{ endpoint: 'authorize', prompt: 'none', outcome: 'code', sub: SUB.alice }])
    expect(await sessionHash(context), 'the re-authorization rotates the session id').not.toBe(oldHash)
    expect(authDb('DELETE FROM oidc_sessions WHERE id_hash = ?', oldHash), 'the expired row was replaced').toBe(0)

    // A logout from another tab leaves this page with a session that answers 401 without a reason.
    const bob = await otherUser(browser, 'bob')
    await bob.page.goto('/')
    await expect(seriesTile(bob.page, WHEEL)).toBeVisible()
    const out = await bob.page.request.post('/api/auth/logout', { headers: sameOrigin(bob.page) })
    expect(out.status()).toBe(204)
    const quiet = (await fakeEvents()).length
    await seriesTile(bob.page, WHEEL).click()
    await expect(bob.page.getByText('계속하려면 로그인하세요.')).toBeVisible()
    expect(new URL(bob.page.url()).pathname, 'a reasonless 401 sends the page to base_path/').toBe('/')
    expect((await fakeEvents()).length, 'and nothing went to uni-auth on its own').toBe(quiet)
    await bob.context.close()
  })

  test('(g) the two logouts', async ({ page, context, browser }) => {
    await signIn(page, 'alice')
    await openSettings(page)
    await page.getByTestId('account-panel').getByRole('button', { name: '로그아웃', exact: true }).click()
    await expect(page.getByText('로그아웃했습니다. 다른 서비스에는 계속 로그인되어 있습니다.')).toBeVisible()
    const status = (await (await page.request.get('/api/auth/status')).json()) as { authenticated: boolean }
    expect(status.authenticated, 'the local logout ends this session').toBe(false)
    const cookie = await sidCookie(context)
    const left = (cookie?.expires ?? 0) - Date.now() / 1000
    expect(left, 'the cookie stays 10 minutes for 모든 서비스에서 로그아웃').toBeLessThan(660)

    const before = (await fakeEvents()).length
    await page.getByRole('button', { name: '모든 서비스에서 로그아웃' }).click()
    await expect(page.getByText('uni-auth에서 로그아웃했습니다.')).toBeVisible()
    expect((await fakeEvents()).slice(before), 'end_session carries the session id_token').toEqual([
      { endpoint: 'end_session', outcome: 'logged_out', sub: SUB.alice, id_token_hint: true },
    ])
    expect((await context.cookies()).map((c) => c.name)).not.toContain(SID_COOKIE)

    // uni-auth's own session is gone too: signing in again shows its page.
    await page.goto('/')
    await page.getByRole('link', { name: 'uni-auth로 로그인' }).click()
    await expect(page.getByRole('heading', { name: 'fake uni-auth' })).toBeVisible()

    // And straight from the SPA.
    const bob = await otherUser(browser, 'bob')
    await openSettings(bob.page)
    const bobBefore = (await fakeEvents()).length
    await bob.page.getByTestId('account-panel').getByRole('button', { name: '모든 서비스에서 로그아웃' }).click()
    await expect(bob.page.getByText('uni-auth에서 로그아웃했습니다.')).toBeVisible()
    expect((await fakeEvents()).slice(bobBefore)).toEqual([
      { endpoint: 'end_session', outcome: 'logged_out', sub: SUB.bob, id_token_hint: true },
    ])
    expect((await bob.context.cookies()).map((c) => c.name)).not.toContain(SID_COOKIE)
    await bob.context.close()
  })
})
