import { afterEach, describe, expect, it, vi } from 'vitest'

import { applyTheme, resolveTheme, watchSystemTheme } from './theme'

/**
 * NFR-CMP-003. The theme setting is light / dark / system and lands on
 * `<html data-theme>`; the viewer is dark regardless, which tokens.css handles
 * by scoping the dark ramp to a bare attribute selector (see tokens.test.ts).
 */

interface FakeMql {
  matches: boolean
  addEventListener: (type: string, cb: (e: MediaQueryListEvent) => void) => void
  removeEventListener: (type: string, cb: (e: MediaQueryListEvent) => void) => void
}

function stubMatchMedia(dark: boolean): { fire: (next: boolean) => void } {
  const listeners = new Set<(e: MediaQueryListEvent) => void>()
  const mql: FakeMql = {
    matches: dark,
    addEventListener: (_type, cb) => listeners.add(cb),
    removeEventListener: (_type, cb) => {
      listeners.delete(cb)
    },
  }
  vi.stubGlobal('matchMedia', () => mql)
  return {
    fire: (next: boolean) => {
      mql.matches = next
      for (const cb of listeners) cb({ matches: next } as MediaQueryListEvent)
    },
  }
}

afterEach(() => {
  vi.unstubAllGlobals()
  document.documentElement.removeAttribute('data-theme')
})

describe('resolveTheme', () => {
  it('passes an explicit choice straight through', () => {
    expect(resolveTheme('light', true)).toBe('light')
    expect(resolveTheme('dark', false)).toBe('dark')
  })

  it('follows the OS only for "system"', () => {
    expect(resolveTheme('system', true)).toBe('dark')
    expect(resolveTheme('system', false)).toBe('light')
  })
})

describe('applyTheme', () => {
  it('writes the resolved theme onto the document element', () => {
    stubMatchMedia(false)
    applyTheme('dark')
    expect(document.documentElement.getAttribute('data-theme')).toBe('dark')
    applyTheme('light')
    expect(document.documentElement.getAttribute('data-theme')).toBe('light')
  })

  it('resolves "system" against the OS preference, not to the literal string', () => {
    stubMatchMedia(true)
    expect(applyTheme('system')).toBe('dark')
    expect(document.documentElement.getAttribute('data-theme')).toBe('dark')
  })

  it('defaults to light where matchMedia does not exist', () => {
    vi.stubGlobal('matchMedia', undefined)
    expect(applyTheme('system')).toBe('light')
  })
})

describe('watchSystemTheme', () => {
  it('reports OS changes until it is unsubscribed', () => {
    const { fire } = stubMatchMedia(false)
    const seen: boolean[] = []
    const stop = watchSystemTheme((dark) => seen.push(dark))
    fire(true)
    fire(false)
    stop()
    fire(true)
    expect(seen).toEqual([true, false])
  })

  it('is a no-op where matchMedia does not exist', () => {
    vi.stubGlobal('matchMedia', undefined)
    expect(() => {
      watchSystemTheme(() => undefined)()
    }).not.toThrow()
  })
})
