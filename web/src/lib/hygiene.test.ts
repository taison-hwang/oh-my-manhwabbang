import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join, resolve } from 'node:path'

import { describe, expect, it } from 'vitest'

/**
 * The two design rules that a reviewer cannot be relied upon to enforce, and
 * that a compiler cannot see at all (WP-05 acceptance 2 and 4).
 *
 * ESLint already bans `rounded-*` and tailwind.config.ts already deletes the
 * utilities, but neither covers a `.css` file or a string that ESLint's
 * selector list happens not to reach — and "the build must fail" is the
 * acceptance criterion, not "the linter usually catches it".
 */

const ROOT = resolve(process.cwd(), 'src')

function walk(dir: string, out: string[] = []): string[] {
  for (const entry of readdirSync(dir)) {
    const path = join(dir, entry)
    if (statSync(path).isDirectory()) {
      walk(path, out)
    } else {
      out.push(path)
    }
  }
  return out
}

const ALL = walk(ROOT)
const rel = (p: string): string => p.slice(ROOT.length + 1)

const SOURCES = ALL.filter((p) => /\.(ts|tsx|css)$/.test(p) && !/\.test\.tsx?$/.test(p))

/** Radius utilities that tailwind.config.ts deliberately does not generate. */
const BANNED_RADIUS = /\brounded-(sm|md|lg|xl|2xl|3xl|\[)/

describe('zero corner radius (D-40)', () => {
  it('has no rounded-* utility anywhere in src/', () => {
    const offenders = SOURCES.filter((p) => BANNED_RADIUS.test(readFileSync(p, 'utf8'))).map(rel)
    expect(offenders).toEqual([])
  })

  it('rounds nothing but the radio dot and the spinner', () => {
    // The two true circles of ui-spec §0.1 are `50%`; everything else must be
    // flat. Anything that is neither is a corner someone softened.
    const allowed = new Set(['0', '0px', '50%', '9999px'])
    const offenders: string[] = []
    for (const path of SOURCES) {
      for (const m of readFileSync(path, 'utf8').matchAll(/border-radius:\s*([^;}\n]+)/g)) {
        const value = (m[1] ?? '').trim()
        if (!allowed.has(value)) offenders.push(`${rel(path)}: ${value}`)
      }
    }
    expect(offenders).toEqual([])
  })
})

describe('colour lives in the token layer (ui-spec §1.2)', () => {
  const HEX = /#[0-9a-fA-F]{3,8}\b/g

  it('has no hex literal in any component or feature', () => {
    const offenders: string[] = []
    for (const path of SOURCES) {
      if (!/[/\\](components|features)[/\\]/.test(path)) continue
      const hits = readFileSync(path, 'utf8').match(HEX)
      if (hits !== null) offenders.push(`${rel(path)}: ${hits.join(', ')}`)
    }
    expect(offenders).toEqual([])
  })

  it('confines every hex in the stylesheet layer to tokens.css', () => {
    const offenders: string[] = []
    for (const path of SOURCES) {
      if (!path.endsWith('.css') || path.endsWith('tokens.css')) continue
      const hits = readFileSync(path, 'utf8').match(HEX)
      if (hits !== null) offenders.push(`${rel(path)}: ${hits.join(', ')}`)
    }
    expect(offenders).toEqual([])
  })
})

describe('module conventions (impl-plan §5.2)', () => {
  it('uses named exports only — no default export in src/', () => {
    const offenders = SOURCES.filter(
      (p) => /\.tsx?$/.test(p) && /^\s*export default\b/m.test(readFileSync(p, 'utf8')),
    ).map(rel)
    expect(offenders).toEqual([])
  })

  it('never calls fetch outside src/api (D-44)', () => {
    const offenders = SOURCES.filter((p) => {
      if (/[/\\]api[/\\]/.test(p) || !/\.tsx?$/.test(p)) return false
      return /\bfetch\s*\(/.test(readFileSync(p, 'utf8'))
    }).map(rel)
    expect(offenders).toEqual([])
  })

  it('vendors the font — no stylesheet reaches out to a CDN (NFR-OPS-001)', () => {
    const offenders = SOURCES.filter((p) => /https?:\/\/fonts\./.test(readFileSync(p, 'utf8'))).map(
      rel,
    )
    expect(offenders).toEqual([])
  })
})
