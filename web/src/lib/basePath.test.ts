import { describe, expect, it } from 'vitest'

import { resolveBasePath, toRouterBasename } from './basePath'

/**
 * NFR-SEC-003: the same build must serve from `/` or from any `base_path`
 * behind a reverse proxy. The server rewrites `<base href>`; everything else
 * derives from it.
 */
describe('resolveBasePath', () => {
  const origin = 'http://127.0.0.1:8790'

  it('collapses a root mount to the empty string', () => {
    expect(resolveBasePath('/', origin)).toBe('')
    expect(resolveBasePath('http://127.0.0.1:8790/', origin)).toBe('')
  })

  it('keeps a sub-path and strips the trailing slash the element requires', () => {
    expect(resolveBasePath('/reader/', origin)).toBe('/reader')
    expect(resolveBasePath('/a/b/c/', origin)).toBe('/a/b/c')
  })

  it('accepts an absolute href, which is what element.href returns', () => {
    expect(resolveBasePath('https://example.test/reader/', origin)).toBe('/reader')
  })

  it('tolerates a missing or empty href', () => {
    expect(resolveBasePath(null, origin)).toBe('')
    expect(resolveBasePath(undefined, origin)).toBe('')
    expect(resolveBasePath('', origin)).toBe('')
  })

  it('falls back to a root mount rather than crashing the boot', () => {
    expect(resolveBasePath('http://[', origin)).toBe('')
  })

  it('does not leave a double slash for a caller writing `${base}/api`', () => {
    for (const href of ['/', '/reader/', '/reader//']) {
      expect(`${resolveBasePath(href, origin)}/api/series`).not.toContain('//api')
    }
  })
})

describe('toRouterBasename', () => {
  it("gives React Router the '/' it wants for a root mount", () => {
    expect(toRouterBasename('')).toBe('/')
    expect(toRouterBasename('/reader')).toBe('/reader')
  })
})
