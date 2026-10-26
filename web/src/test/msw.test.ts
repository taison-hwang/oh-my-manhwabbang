/* eslint-disable no-restricted-globals -- this file tests how MSW intercepts fetch itself; it is not a module that talks to the API */
import { http, HttpResponse } from 'msw'
import { setupServer } from 'msw/node'
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from 'vitest'

import {
  assertNoUnhandled,
  drainUnhandled,
  failUnhandled,
  installStrictMsw,
  strictSetupInstalled,
} from './msw'

const installedBySetup = strictSetupInstalled()
const ORIGIN = 'http://localhost'
const server = setupServer(http.get(`${ORIGIN}/api/known`, () => HttpResponse.json({ ok: true })))

beforeAll(() => {
  server.listen({ onUnhandledRequest: failUnhandled })
})
afterEach(() => {
  server.resetHandlers()
  // Each test below provokes an unhandled request on purpose and inspects it;
  // whatever is left must not reach setup.ts's afterEach and fail them.
  drainUnhandled()
})
afterAll(() => {
  server.close()
})

describe('failUnhandled', () => {
  it('records an unhandled request by method, path and query', async () => {
    await fetch(`${ORIGIN}/api/unknown?x=1`).catch(() => undefined)
    expect(drainUnhandled()).toEqual(['GET /api/unknown?x=1'])
  })

  it('still fails the request itself, as onUnhandledRequest: "error" did', async () => {
    // MSW's own refusal, not a connection error from a request let through.
    await expect(fetch(`${ORIGIN}/api/unknown`)).rejects.toThrow(/onUnhandledRequest/)
  })

  it('leaves handled requests alone', async () => {
    const res = await fetch(`${ORIGIN}/api/known`)
    expect(await res.json()).toEqual({ ok: true })
    expect(drainUnhandled()).toEqual([])
  })
})

describe('assertNoUnhandled', () => {
  it('throws naming every unhandled request, and clears them', async () => {
    await fetch(`${ORIGIN}/api/a`).catch(() => undefined)
    await fetch(`${ORIGIN}/api/b`, { method: 'PUT', body: '{}' }).catch(() => undefined)
    expect(assertNoUnhandled).toThrow(/GET \/api\/a.*PUT \/api\/b/)
    expect(assertNoUnhandled).not.toThrow()
  })

  it('is silent when nothing went unhandled', () => {
    expect(assertNoUnhandled).not.toThrow()
  })
})

describe('installStrictMsw', () => {
  it('runs the check after every test and after every file', () => {
    const afterEachHook = vi.fn()
    const afterAllHook = vi.fn()
    installStrictMsw({ afterEach: afterEachHook, afterAll: afterAllHook })
    expect(afterEachHook).toHaveBeenCalledWith(assertNoUnhandled)
    expect(afterAllHook).toHaveBeenCalledWith(assertNoUnhandled)
  })

  // The check only works because src/test/setup.ts installs it, and this is
  // what notices if it stops running: vitest.config.ts fails a run whose setup
  // file is missing, but not one whose setup file no longer calls
  // installStrictMsw. Sampled before the test above runs its own install.
  it('is installed by the shared setup file', () => {
    expect(installedBySetup).toBe(true)
  })
})
