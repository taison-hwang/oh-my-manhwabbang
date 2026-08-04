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

/**
 * The radius scale, read out of tokens.css rather than restated here.
 *
 * E-32 retires D-40's "zero radius everywhere" but explicitly keeps its
 * enforcement: the ban is not lifted, the allowed set is *bound to the tokens*.
 * So this map is the whitelist's only source of truth — adding `--radius-xl` to
 * tokens.css legalises `border-radius: var(--radius-xl)` and the literal it
 * resolves to, and nothing else ever becomes legal.
 */
const RADIUS_TOKENS = new Map<string, string>(
  [...readFileSync(resolve(ROOT, 'styles/tokens.css'), 'utf8').matchAll(
    /(--radius-[a-z]+):\s*([^;}\n]+)/g,
  )].map((m) => [m[1] ?? '', (m[2] ?? '').trim()]),
)

/** Radius utilities that tailwind.config.ts deliberately does not generate.
 *  `borderRadius` is an override of {none, DEFAULT, sm, md, lg, pill, full}, so
 *  the xl family and every arbitrary value are still absent. The optional
 *  `-t`/`-bl`/… segment is matched too: `rounded-tl-[3px]` is the same bug. */
const BANNED_RADIUS = /\brounded(-[a-z]+)?-(2xl|3xl|xl|\[)/

describe('the radius scale is closed (D-40 as amended by E-32)', () => {
  it('reads a non-empty scale out of tokens.css', () => {
    // If this map ever comes back empty the whitelist below degenerates to
    // {0, 0px, 50%, 9999px} and the two assertions after it pass for the wrong
    // reason — a green suite that is checking nothing.
    expect([...RADIUS_TOKENS.keys()].sort()).toEqual([
      '--radius-full',
      '--radius-lg',
      '--radius-md',
      '--radius-pill',
      '--radius-sm',
    ])
  })

  it('has no radius utility outside the scale anywhere in src/', () => {
    const offenders = SOURCES.filter((p) => BANNED_RADIUS.test(readFileSync(p, 'utf8'))).map(rel)
    expect(offenders).toEqual([])
  })

  it('rounds nothing with a number that is not a token', () => {
    // `50%` is a true circle (the radio dot, the spinner, the slider thumb) and
    // `9999px` is Tailwind's pill; everything else must name a `--radius-*`
    // token or the value one resolves to. An arbitrary px is a corner someone
    // softened by hand, which is the thing D-40 existed to stop.
    const allowed = new Set(['0', '0px', '50%', '9999px'])
    for (const [name, value] of RADIUS_TOKENS) {
      allowed.add(value)
      allowed.add(`var(${name})`)
    }
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
