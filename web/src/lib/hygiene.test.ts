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

/** Every first capture group of `re` in `text`, with the misses dropped. */
function captures(text: string, re: RegExp): string[] {
  const out: string[] = []
  for (const m of text.matchAll(re)) {
    const g = m[1]
    if (g !== undefined) out.push(g)
  }
  return out
}

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

  /**
   * Every family a stack *leads* with must be one this repo ships.
   *
   * Banning the CDN is only half of NFR-OPS-001. The other half is that the
   * stack has to reach a vendored face before it reaches a system one, and that
   * half went unenforced: `--font-ui` led with `ui-sans-serif` for three skins,
   * so every numeral and every uppercase micro-label in the product was drawn
   * by Helvetica on a Mac, Arial on Windows and something else again on
   * Android. Nothing failed, because nothing was looking.
   *
   * A leading family with no `@font-face` behind it is that bug, so this asserts
   * the join: read the stacks out of tokens.css, read the declared families out
   * of fonts.css, and require the first entry of each stack to appear in both —
   * with a file on disk behind it.
   */
  it('leads every font stack with a face this repo ships (NFR-OPS-001)', () => {
    const tokens = readFileSync(join(ROOT, 'styles/tokens.css'), 'utf8')
    const faces = readFileSync(join(ROOT, 'styles/fonts.css'), 'utf8')

    const declared = new Set(captures(faces, /font-family:\s*'([^']+)'/g))

    const stacks = [...tokens.matchAll(/--font-(heading|ui|seal|body):\s*([^;]+);/g)]
    expect(stacks.length).toBeGreaterThanOrEqual(3)

    const unvendored: string[] = []
    for (const stack of stacks) {
      const name = stack[1] ?? ''
      const lead = (stack[2] ?? '').split(',')[0] ?? ''
      const first = lead.trim().replace(/^'|'$/g, '')
      // `--font-body` is an alias of another token, not a stack of its own,
      // and a bare generic keyword is a legitimate stack *tail*, never a lead.
      if (first.startsWith('var(')) continue
      if (!declared.has(first)) unvendored.push(`--font-${name} leads with ${first}`)
    }
    expect(unvendored).toEqual([])

    // And each declared face must have its file on disk: a stack can lead with
    // a vendored name and still fall through if the woff2 went missing.
    const missing = captures(faces, /url\('\.\.\/([^']+)'\)/g).filter((p) => {
      try {
        return statSync(join(ROOT, p)).size === 0
      } catch {
        return true
      }
    })
    expect(missing).toEqual([])
  })
})
