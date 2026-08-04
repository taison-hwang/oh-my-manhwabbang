import { readFileSync, readdirSync } from 'node:fs'
import { join, resolve } from 'node:path'

import { describe, expect, it } from 'vitest'

import tailwindConfig from '../../tailwind.config'
import { allRules, customProperties, findRule, topLevelRules, type CssRule } from './cssRules'

// `import.meta.url` is an http URL under the jsdom environment, so the source
// is located from the vitest root (web/) instead.
const read = (rel: string): string => readFileSync(resolve(process.cwd(), rel), 'utf8')

const TOKENS = read('src/styles/tokens.css')
const BASE = read('src/styles/base.css')

const rules: CssRule[] = topLevelRules(TOKENS)

function ruleFor(needle: string): CssRule {
  const rule = findRule(rules, needle)
  if (rule === undefined) throw new Error(`tokens.css has no rule matching ${needle}`)
  return rule
}

const lightRule = ruleFor('[data-theme=')
const darkRule = ruleFor("[data-theme='dark']")
const light = customProperties(lightRule.body)
const dark = customProperties(darkRule.body)

// ---------------------------------------------------------------------------
// Contrast
//
// E-32 §4 rejects three of the prototype's colour choices on *measured* AA
// failures, so the floors it sets can only be checked by measuring. This is
// WCAG 2.1 relative luminance, inline because the token layer may not grow a
// module and a "checked" contrast rule that trusts a hand-written table is the
// failure mode the ruling is about. `reproduces the four ratios E-32 measured`
// below is the calibration test for everything here.
// ---------------------------------------------------------------------------

interface Rgba {
  r: number
  g: number
  b: number
  a: number
}

/** Follows `var(--x)` chains to the literal a token finally resolves to. */
function resolveToken(map: Map<string, string>, name: string): string {
  let value = map.get(name)
  if (value === undefined) throw new Error(`no token ${name} in this theme block`)
  for (let hops = 0; hops < 8; hops++) {
    const ref = /^var\((--[\w-]+)\)$/.exec(value.trim())
    if (ref === null) return value.trim()
    const next = map.get(ref[1] ?? '')
    if (next === undefined) throw new Error(`${name} points at missing ${ref[1] ?? '?'}`)
    value = next
  }
  throw new Error(`${name} is a var() cycle`)
}

/** `#rrggbb` or the space-separated `rgb(r g b / a)` the sheet uses. */
function parseColour(raw: string): Rgba {
  const value = raw.trim()
  const hex = /^#([0-9a-fA-F]{6})$/.exec(value)
  if (hex !== null) {
    const n = parseInt(hex[1] ?? '', 16)
    return { r: (n >> 16) & 255, g: (n >> 8) & 255, b: n & 255, a: 1 }
  }
  const fn = /^rgb\(\s*(\d+)\s+(\d+)\s+(\d+)\s*(?:\/\s*([\d.]+)\s*)?\)$/.exec(value)
  if (fn === null) throw new Error(`cannot parse colour ${raw}`)
  return {
    r: Number(fn[1]),
    g: Number(fn[2]),
    b: Number(fn[3]),
    a: fn[4] === undefined ? 1 : Number(fn[4]),
  }
}

function luminance({ r, g, b }: Rgba): number {
  const channel = (c: number): number => {
    const s = c / 255
    return s <= 0.04045 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4
  }
  return 0.2126 * channel(r) + 0.7152 * channel(g) + 0.0722 * channel(b)
}

/** Composites a possibly-translucent foreground onto an opaque ground. */
function over(fg: Rgba, ground: Rgba): Rgba {
  return {
    r: fg.a * fg.r + (1 - fg.a) * ground.r,
    g: fg.a * fg.g + (1 - fg.a) * ground.g,
    b: fg.a * fg.b + (1 - fg.a) * ground.b,
    a: 1,
  }
}

function contrast(fg: Rgba, ground: Rgba): number {
  const a = luminance(over(fg, ground))
  const b = luminance(ground)
  return (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05)
}

/** Contrast of one token against another, within one theme block. */
function ratio(map: Map<string, string>, token: string, ground: string): number {
  return contrast(
    parseColour(resolveToken(map, token)),
    parseColour(resolveToken(map, ground)),
  )
}

const themes: [string, Map<string, string>][] = [
  ['light', light],
  ['dark', dark],
]

/**
 * The base.css rule with exactly this selector.
 *
 * Not `findRule`, which matches on substring: `.dialog` would find
 * `.dialog-backdrop` (it is declared first and has no radius), and the corner
 * assertion would then pass because there was nothing there to fail.
 */
function exactRule(selector: string): CssRule | undefined {
  return allRules(BASE).find((r) => r.selector === selector)
}

/** Ink tokens: text at 10–14px, so the floor is AA 4.5 on both grounds. */
const INK_TOKENS = [
  '--ink',
  '--ink-muted',
  '--ink-dim',
  '--ink-faint',
  '--ink-label',
  '--ink-meta',
  '--ink-th',
  '--accent-text',
]

describe('tokens.css — light ground (ui-spec §1.2, E-32 §1)', () => {
  it('declares the role tokens verbatim', () => {
    expect(light.get('--color-bg')).toBe('#EAE3D4')
    expect(light.get('--color-surface')).toBe('#F3EEE3')
    expect(light.get('--color-text')).toBe('#263B38')
    expect(light.get('--color-accent')).toBe('#17595B')
    expect(light.get('--color-divider')).toBe('#DDD3C0')
  })

  it('collapses the secondary accent into the accent (E-32 §1)', () => {
    expect(resolveToken(light, '--color-accent-2')).toBe(resolveToken(light, '--color-accent'))
    for (const step of [100, 200, 300, 400, 500, 600, 700, 800, 900]) {
      expect(
        resolveToken(light, `--color-accent-2-${step.toString()}`),
        `accent-2-${step.toString()} has drifted from the accent ramp`,
      ).toBe(resolveToken(light, `--color-accent-${step.toString()}`))
    }
  })

  it('keeps --color-hot as the retired brand red, not the accent (E-32 §1)', () => {
    // The whole point of the token: #EC3013 marks "current / selected /
    // focused". The moment it equals the accent it is a brand colour again and
    // E-32 has been silently undone.
    expect(light.get('--color-hot')).toBe('#EC3013')
    expect(light.get('--color-hot')).not.toBe(light.get('--color-accent'))
    // It is a marker, not a palette member, so it does not flip with the theme.
    expect(dark.has('--color-hot')).toBe(false)
  })

  it('carries the E-32 radius scale — sm 3 / md 4 / lg 6 / pill 7 / full 999', () => {
    expect(light.get('--radius-sm')).toBe('3px')
    expect(light.get('--radius-md')).toBe('4px')
    expect(light.get('--radius-lg')).toBe('6px')
    expect(light.get('--radius-pill')).toBe('7px')
    expect(light.get('--radius-full')).toBe('999px')
    // The scale is theme-invariant geometry, not paint.
    expect([...dark.keys()].filter((k) => k.startsWith('--radius-'))).toEqual([])
  })

  it('spaces on keys 1,2,3,4,6,8 — the DS has no 5 or 7', () => {
    expect([...light.keys()].filter((k) => k.startsWith('--space-')).sort()).toEqual([
      '--space-1',
      '--space-2',
      '--space-3',
      '--space-4',
      '--space-6',
      '--space-8',
    ])
  })

  it('names Archivo first and the Korean fallbacks in the E-7 order', () => {
    const stack = light.get('--font-heading') ?? ''
    const order = [
      'Archivo',
      'Pretendard',
      'Apple SD Gothic Neo',
      'Malgun Gothic',
      'Noto Sans KR',
      'system-ui',
    ]
    let cursor = -1
    for (const face of order) {
      const at = stack.indexOf(face, cursor + 1)
      expect(at, `${face} missing or out of order in ${stack}`).toBeGreaterThan(cursor)
      cursor = at
    }
  })

  it('paints elevation with both lobes of the dual light (E-32 §1)', () => {
    // Down-right ochre shadow + up-left cream highlight. Losing the second lobe
    // turns the soft skin back into the Modernist ink drop.
    for (const step of ['--shadow-sm', '--shadow-md', '--shadow-lg', '--shadow-inset']) {
      const value = light.get(step) ?? ''
      expect(value, `${step} lost its shadow lobe`).toContain('rgb(150 128 96 /')
      expect(value, `${step} lost its highlight lobe`).toContain('rgb(255 253 246 /')
    }
    expect(light.get('--shadow-inset')).toContain('inset')
  })
})

describe('tokens.css — dark ramp (ui-spec §1.4, NFR-CMP-003, E-32 §3)', () => {
  it('is scoped by a bare attribute selector so a nested element re-scopes it', () => {
    // This is the load-bearing detail. ui-spec §1.4 prints
    // `:root[data-theme="dark"]`, but then requires `<div data-theme="dark">`
    // to re-scope the tokens so the viewer is dark in *both* app themes.
    // `:root[…]` only ever matches <html>, so it cannot do that.
    const div = document.createElement('div')
    div.setAttribute('data-theme', 'dark')
    expect(div.matches(darkRule.selector)).toBe(true)
    expect(darkRule.selector.startsWith(':root')).toBe(false)
  })

  it('re-scopes back to light for a nested light surface (the next-volume card)', () => {
    const div = document.createElement('div')
    div.setAttribute('data-theme', 'light')
    expect(div.matches(lightRule.selector)).toBe(true)
  })

  it('wins over the light block on <html data-theme="dark"> by source order', () => {
    // Both selectors are specificity (0,1,0) against <html data-theme="dark">
    // (`:root` and `[data-theme='dark']`), so the later rule is the one that
    // applies. Reordering the file would silently break dark mode.
    expect(darkRule.start).toBeGreaterThan(lightRule.start)
  })

  it('defines a full role set of its own — it is a palette, not a tint', () => {
    // soft-ui.css ships light only, so this block is derived. If any role went
    // missing the dark theme would inherit a cream ground from the light block
    // and the viewer would be light inside a light app theme.
    for (const role of [
      '--color-bg',
      '--color-surface',
      '--color-text',
      '--color-accent',
      '--color-accent-2',
      '--color-divider',
    ]) {
      expect(dark.has(role), `${role} is missing from the dark block`).toBe(true)
    }
    expect(dark.get('--color-bg')).toBe('#263B38')
    expect(dark.get('--color-text')).toBe('#EAE3D4')
    expect(dark.get('--color-surface')).toBe('#2F4A46')
    expect(dark.get('--color-divider')).toBe('#3E5B57')
    expect(dark.get('--rule')).toBe('#3E5B57')
    expect(dark.get('--control-border')).toBe('#5A7C77')
  })

  it('swaps the ground and the ink of the light theme', () => {
    expect(dark.get('--color-bg')).toBe(light.get('--color-text'))
    expect(resolveToken(dark, '--ink')).toBe(light.get('--color-bg'))
  })

  it('keeps the accent constant and moves hover/press up the ramp', () => {
    expect(dark.get('--color-accent')).toBe(light.get('--color-accent'))
    expect(resolveToken(dark, '--color-accent-2')).toBe(resolveToken(light, '--color-accent-2'))
    expect(dark.get('--accent-text')).toBe('#9BC3C1') // accent-300
    // Lighter than the base, unlike the light theme where hover/press go down.
    const lum = (theme: Map<string, string>, token: string): number =>
      luminance(parseColour(resolveToken(theme, token)))
    expect(lum(dark, '--accent-hover')).toBeGreaterThan(lum(dark, '--color-accent'))
    expect(lum(dark, '--accent-press')).toBeGreaterThan(lum(dark, '--accent-hover'))
    expect(lum(light, '--accent-hover')).toBeLessThan(lum(light, '--color-accent'))
    expect(lum(light, '--accent-press')).toBeLessThan(lum(light, '--accent-hover'))
  })

  it('washes the active row with accent-300, not the accent (E-32 §3.2)', () => {
    // A teal tint on a teal ground is not a tint. The shipped file used the old
    // red at 14 %; the same alpha in #17595B is invisible here.
    expect(dark.get('--nav-active')).toBe('rgb(155 195 193 / 0.16)')
    expect(dark.get('--nav-active')).not.toBe(light.get('--nav-active'))
  })

  it('leaves the raw ramps untouched — they are an absolute lightness scale', () => {
    const redeclared = [...dark.keys()].filter((k) =>
      /^--color-(neutral|accent(-2)?)-\d00$/.test(k),
    )
    expect(redeclared).toEqual([])
  })

  it('flips every semantic token the light block defines', () => {
    const semantic = [
      '--ink',
      '--ink-muted',
      '--ink-dim',
      '--ink-faint',
      '--ink-label',
      '--ink-meta',
      '--ink-th',
      '--rule',
      '--rule-strong',
      '--control-border',
      '--control-border-hover',
      '--fill-subtle',
      '--fill-track',
      '--fill-track-2',
      '--hover-tint',
      '--press-tint',
      '--row-hover',
      '--row-hover-table',
      '--nav-hover',
      '--nav-active',
      '--scrim-cover',
      '--scrim-modal',
      '--accent-hover',
      '--accent-press',
      '--accent-text',
      '--accent-fill',
      '--scrollbar-thumb',
      '--scrollbar-thumb-hover',
      '--ghost-hover',
      '--ghost-press',
      '--selection-bg',
    ]
    for (const token of semantic) {
      expect(light.has(token), `${token} missing from the light block`).toBe(true)
      expect(dark.has(token), `${token} does not flip in the dark block`).toBe(true)
    }
  })

  it('leaves the absolutes alone — they paint on a ground that never flips', () => {
    // The viewer ground is #263B38 in both app themes, and the accent and the
    // hot marker are theme-invariant, so their foregrounds are too. Flipping
    // any of these would repaint the viewer's scrims when the app theme changed.
    for (const token of ['--scrim-volume-end', '--scrim-broken', '--on-accent', '--on-hot']) {
      expect(light.has(token), `${token} missing from the light block`).toBe(true)
      expect(dark.has(token), `${token} must not flip with the theme`).toBe(false)
    }
    expect(light.get('--scrim-volume-end')).toBe('rgb(38 59 56 / 0.92)')
    expect(light.get('--scrim-broken')).toBe('rgb(8 35 37 / 0.82)') // accent-900 @ 82 %
  })

  it('re-derives the accent tints off the new ramp, not the retired red', () => {
    // These carried `rgb(236 48 19 / …)` — the old accent's channels. On a teal
    // accent an unexplained red wash is what "we forgot this one" looks like.
    for (const token of ['--nav-active', '--ghost-hover', '--ghost-press', '--selection-bg']) {
      for (const [name, theme] of themes) {
        expect(theme.get(token), `${token} still carries the old red in ${name}`).not.toContain(
          '236 48 19',
        )
      }
    }
    expect(light.get('--ghost-hover')).toContain('23 89 91') // the accent
    expect(dark.get('--ghost-hover')).toContain('155 195 193') // accent-300
  })

  it('paints elevation as a hairline edge plus ambient darkness', () => {
    // The light block's highlight lobe is rgb(255 253 246 / .9). Painted on a
    // dark ground that is a white outline around every card, not a highlight —
    // E-32 §3.3 names this specifically.
    for (const step of ['--shadow-sm', '--shadow-md', '--shadow-lg', '--shadow-inset']) {
      expect(dark.get(step), `${step} kept the cream highlight lobe`).not.toContain('255 253 246')
    }
    expect(dark.get('--shadow-lg')).toContain('#3E5B57')
    expect(dark.get('--shadow-lg')).toContain('rgb(0 0 0 / 0.6)')
    expect(dark.get('--shadow-inset')).toContain('inset')
  })
})

describe('contrast floors (E-32 §4)', () => {
  it('reproduces the four ratios E-32 measured', () => {
    // Calibration. Every floor below is only worth what this function is worth,
    // so it is pinned against the numbers the ruling states.
    const at = (fg: string, bg: string): number =>
      contrast(parseColour(fg), parseColour(bg))
    expect(at('#857A66', '#EAE3D4')).toBeCloseTo(3.31, 2) // neutral-600 on the ground
    expect(at('#A79B84', '#F3EEE3')).toBeCloseTo(2.37, 2) // neutral-500 on the surface
    expect(at('#9BC3C1', '#E2DACA')).toBeCloseTo(1.38, 2) // accent-300 on the trough
    expect(at('#F6F2E9', '#EC3013')).toBeCloseTo(3.76, 2) // the override chip
  })

  it('clears AA for every ink token on both grounds, in both themes', () => {
    for (const [name, theme] of themes) {
      for (const token of INK_TOKENS) {
        for (const ground of ['--color-bg', '--color-surface']) {
          const value = ratio(theme, token, ground)
          expect(
            value,
            `${name} ${token} on ${ground} is ${value.toFixed(2)}:1`,
          ).toBeGreaterThanOrEqual(4.5)
        }
      }
    }
  })

  it('keeps --ink-dim off the prototype AA failure (3.31 on the cream ground)', () => {
    // E-32 §4: the prototype puts section labels and meta at --color-neutral-600
    // on the ground. This token is used at 10–12px, so 4.5 is the floor.
    expect(ratio(light, '--ink-dim', '--color-bg')).toBeGreaterThanOrEqual(4.5)
    expect(light.get('--ink-dim')).not.toBe('#857A66')
  })

  it('keeps --ink-faint off the prototype AA failure (2.37 on the surface)', () => {
    for (const [, theme] of themes) {
      expect(ratio(theme, '--ink-faint', '--color-surface')).toBeGreaterThanOrEqual(4.5)
    }
    expect(light.get('--ink-faint')).not.toBe('#A79B84')
  })

  it('keeps the progress fill visible in its trough — the 1.38 case', () => {
    // E-32 §4: the prototype's completed bar is accent-300 on the trough, which
    // is 1.38 and effectively invisible. `--accent-fill` is what a bar in the
    // accent must use, and `--ink` is the 완독 tone (ui-spec §4.5).
    for (const [name, theme] of themes) {
      for (const token of ['--accent-fill', '--ink']) {
        const value = ratio(theme, token, '--fill-track')
        expect(
          value,
          `${name} ${token} on the trough is ${value.toFixed(2)}:1`,
        ).toBeGreaterThanOrEqual(3)
      }
    }
  })

  it('reads on an accent fill in both themes — why --on-accent exists', () => {
    // `--color-bg` used to double as the accent's foreground. The teal is dark
    // in both themes, so on the dark ground that pairing is 1.48:1.
    const onAccent = parseColour(light.get('--on-accent') ?? '')
    for (const [name, theme] of themes) {
      for (const fill of ['--color-accent', '--accent-hover', '--accent-press']) {
        const value = contrast(onAccent, parseColour(resolveToken(theme, fill)))
        expect(
          value,
          `--on-accent on ${name} ${fill} is ${value.toFixed(2)}:1`,
        ).toBeGreaterThanOrEqual(4.5)
      }
    }
    expect(contrast(parseColour(dark.get('--color-bg') ?? ''), parseColour('#17595B'))).toBeLessThan(
      3,
    )
  })

  it('puts dark ink on the hot marker — no light one can clear AA', () => {
    // E-32 §4 asks for the override chip's foreground to be fixed. #F6F2E9 is
    // 3.76 and even pure white is 4.20, so the fix has to be a dark ink.
    const hot = parseColour(light.get('--color-hot') ?? '')
    expect(contrast(parseColour(light.get('--on-hot') ?? ''), hot)).toBeGreaterThanOrEqual(4.5)
    expect(contrast(parseColour('#FFFFFF'), hot)).toBeLessThan(4.5)
  })
})

// ---------------------------------------------------------------------------
// The pairs the product actually paints
//
// Everything above measures **token pairs**, which is a different question from
// "does any component put these two together". E-32 §1 retired `--color-bg` as
// the accent's foreground and this file has asserted since then that the pair is
// 1.48:1 in the dark theme — and three components shipped still writing
// `bg-accent … text-bg`, because no check ever read a class list. A token check
// cannot see a component. This one reads the components.
//
// ## What it covers
//
//  * `.tsx` under `src/`, tests excluded.
//  * **Single-line string literals** — the shape a Tailwind class list actually
//    takes here, whether it is a `className="…"` attribute or one argument of a
//    `cn(…)` call.
//  * A literal that names **one of the solid fills below** and at least one
//    `text-*` colour utility. The foreground is measured against the fill, in
//    every theme block that file can be painted from, and must clear AA 4.5.
//
// ## What it does not cover, deliberately
//
//  1. **A pair split across two arguments.** `cn('bg-accent', cond && 'text-bg')`
//     is two literals and is invisible here. Joining them would mean resolving
//     `cn` statically, which is a different tool; the honest fix is to keep the
//     ground and its ink in one string, which every site in this repo does.
//  2. **`bg-bg` and `bg-surface`.** The page grounds, not fills — every ink token
//     is already measured against both of them by `INK_TOKENS` above, and letting
//     them in here would duplicate that at lower fidelity.
//  3. **Translucent tokens** (`--hover-tint`, the scrims, `--fill-subtle`). They
//     composite over whatever is underneath and this scanner does not know the
//     stack, so they are excluded rather than measured against a guess.
//  4. **Opacity modifiers and arbitrary values** — `bg-accent/60`, `text-[#fff]`.
//     Not matched at all.
//  5. **Colour that arrives any other way**: `base.css`, an inherited `color`
//     from an ancestor, an inline `style`, a class name built by concatenation.
//  6. **Font size.** Everything is held to AA 4.5, the normal-text floor; nothing
//     here is large enough for the 3.0 exception, and assuming otherwise is how a
//     floor gets talked down.
//  7. **It reads source text, not an AST.** Any single-line quoted or
//     backtick-quoted run is a candidate, so a comment or a non-`className`
//     string naming both utilities is scanned too. That error direction is the
//     safe one — it can only over-report, and a bad pair written out in prose is
//     worth a second look — but this is not a class-list parser.
//
// So: green here means "no single class list in this repo paints an unreadable
// ink on a solid fill". It does **not** mean the screens are AA-clean, and the
// list above is the price of it being maintainable.
// ---------------------------------------------------------------------------

/** Every `.tsx` under `src/` that ships. */
const COMPONENTS = ((): string[] => {
  const root = resolve(process.cwd(), 'src')
  const out: string[] = []
  const walk = (dir: string): void => {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      const path = join(dir, entry.name)
      if (entry.isDirectory()) walk(path)
      else if (entry.name.endsWith('.tsx') && !entry.name.endsWith('.test.tsx')) {
        out.push(path.slice(root.length + 1))
      }
    }
  }
  walk(root)
  return out.sort()
})()

/**
 * Tailwind's colour utilities, flattened to `suffix -> --token`, read out of
 * `tailwind.config.ts` itself.
 *
 * Restating the map here would let it drift: a token renamed in the config would
 * silently stop being scanned, and the check would go on passing. Nested groups
 * flatten the way Tailwind names them — `accent.DEFAULT` is `accent`,
 * `accent.100` is `accent-100`.
 */
const COLOUR_UTILITIES = ((): Map<string, string> => {
  const out = new Map<string, string>()
  const visit = (value: unknown, prefix: string): void => {
    if (typeof value === 'string') {
      const ref = /^var\((--[\w-]+)\)$/.exec(value)
      if (ref !== null) out.set(prefix, ref[1] ?? '')
      return
    }
    if (typeof value !== 'object' || value === null) return
    for (const [key, child] of Object.entries(value)) {
      visit(child, key === 'DEFAULT' ? prefix : prefix === '' ? key : `${prefix}-${key}`)
    }
  }
  visit(tailwindConfig.theme.extend.colors, '')
  return out
})()

/**
 * The solid fills a badge, pill or chip is painted on — the grounds a component
 * chooses *instead of* the page's own, and therefore the ones whose foreground
 * nothing else has already checked.
 */
const FILL = /^(accent|accent-2|accent-hover|accent-press|accent-fill|ink|hot)$|^(accent|accent-2|neutral)-\d00$/

/**
 * The dark theme **as the cascade actually resolves it**, not as the dark block
 * alone declares it.
 *
 * `[data-theme='dark']` overrides ~40 semantic tokens and deliberately leaves
 * the raw ramps alone — they are an absolute lightness scale (ui-spec §1.4, and
 * `leaves the raw ramps untouched` above asserts it) — so `--color-accent-800`,
 * `--color-hot`, `--on-accent` and `--on-hot` are only ever declared in the base
 * block and reach a dark scope by inheritance.
 *
 * Reading `dark` on its own therefore answers "not present" for every one of
 * them, and a scanner that skips what it cannot find **checks nothing while
 * staying green**: `bg-accent text-on-accent` and `bg-hot text-on-hot` were
 * measured in the light theme only until this map existed, and a bad ramp step
 * on a fill was invisible in both. Layering is what the browser does.
 */
const DARK_CASCADE = new Map([...light, ...dark])

/** A token that is a flat opaque colour in this theme, or `null` if it is not. */
function opaque(theme: Map<string, string>, token: string): Rgba | null {
  if (!theme.has(token)) return null
  const colour = parseColour(resolveToken(theme, token))
  return colour.a === 1 ? colour : null
}

/**
 * Which theme block(s) a file's class lists can be painted from.
 *
 * Both, everywhere — every screen is themable (NFR-CMP-003) — with one
 * structural exception: `ViewerPage` wraps the whole viewer in
 * `<div data-theme="dark">`, so its package is only ever painted from the dark
 * block. `NextVolumeCard` is inside that package and steps back out to the app
 * theme (`<div data-theme={appTheme}>`, ui-spec §6.5), so it is measured against
 * both like everything else.
 *
 * Stated as the fact it is, not as a list of what happens to fail today: an
 * exemption fitted to the current failures is a hole that opens on the next
 * change.
 */
function themesFor(file: string): [string, Map<string, string>][] {
  const viewer = file.startsWith(join('features', 'viewer')) && !file.endsWith('NextVolumeCard.tsx')
  return viewer ? [['dark', DARK_CASCADE]] : [['light', light], ['dark', DARK_CASCADE]]
}

/** One class list that names a fill and at least one foreground colour. */
interface PaintedPair {
  file: string
  line: number
  bg: string
  fg: string
}

/** Utilities in a class list, variants stripped, opacity modifiers skipped. */
function utilities(list: string, kind: 'bg' | 'text'): string[] {
  const re = new RegExp(String.raw`(?:^|\s)(?:[\w-]+:)*${kind}-([a-z0-9-]+)(?![\w/-])`, 'g')
  return [...list.matchAll(re)].map((m) => m[1] ?? '')
}

const PAINTED: PaintedPair[] = COMPONENTS.flatMap((file) => {
  const source = read(join('src', file))
  const pairs: PaintedPair[] = []
  // Single-line literals only: a backtick that spans lines is almost always a
  // prose comment, and swallowing one turns a whole file into "a class list".
  for (const match of source.matchAll(/'([^'\\\n]*)'|"([^"\\\n]*)"|`([^`\\\n$]*)`/g)) {
    const list = match[1] ?? match[2] ?? match[3] ?? ''
    const fills = utilities(list, 'bg').filter((name) => FILL.test(name))
    if (fills.length === 0) continue
    const line = source.slice(0, match.index).split('\n').length
    for (const bg of fills) {
      for (const fg of utilities(list, 'text')) {
        if (COLOUR_UTILITIES.has(fg)) pairs.push({ file, line, bg, fg })
      }
    }
  }
  return pairs
})

describe('the pairs components actually paint (E-32 §1)', () => {
  it('resolved the utility map out of tailwind.config.ts', () => {
    // Without this the scan below can pass by having found nothing: an empty
    // map means every `text-*` fails `COLOUR_UTILITIES.has` and `PAINTED` is
    // `[]`. These six are the tokens the ruling turns on.
    for (const [utility, token] of [
      ['accent', '--color-accent'],
      ['on-accent', '--on-accent'],
      ['hot', '--color-hot'],
      ['on-hot', '--on-hot'],
      ['bg', '--color-bg'],
      ['ink', '--color-text'],
    ]) {
      expect(COLOUR_UTILITIES.get(utility ?? ''), `bg-/text-${utility ?? ''}`).toBe(token)
    }
  })

  it('resolves a base-block token inside the dark scope', () => {
    // The hole `DARK_CASCADE` closes, stated as a test rather than left to the
    // comment: these four are declared once, in the base block, and a dark
    // scope reaches them by inheritance. Read `dark` on its own and every one of
    // them is missing — which `opaque` reports as "not a colour I can measure",
    // so the scan skips the pair and passes. `bg-accent text-on-accent` was in
    // that blind spot, i.e. the fix for this ruling was the thing going
    // unchecked.
    for (const token of ['--on-accent', '--on-hot', '--color-hot', '--color-accent-800']) {
      expect(dark.has(token), `${token} is declared in the dark block after all`).toBe(false)
      expect(opaque(DARK_CASCADE, token), `${token} unresolvable in a dark scope`).not.toBeNull()
    }
  })

  it('found the fills the screens are known to paint', () => {
    // The other half of the calibration, and the reason it names files: a regex
    // that quietly stops matching class lists would otherwise leave the
    // assertion below iterating over nothing. Both entries are pairs that must
    // *pass* — a scan that only found the failures would be fitted to them.
    const seen = PAINTED.map((p) => `${p.file} bg-${p.bg} text-${p.fg}`)
    expect(seen).toContain(join('features', 'library', 'SeriesCard.tsx') + ' bg-accent text-on-accent')
    // `--color-bg` on `--color-ink`: the two are inverses in both themes, so
    // this is the correct pairing and the scanner must not call it a defect.
    expect(seen).toContain(join('features', 'overlays', 'ShortcutsDialog.tsx') + ' bg-ink text-bg')
    expect(PAINTED.length).toBeGreaterThanOrEqual(6)
  })

  it('reads at AA on every solid fill it paints', () => {
    const offenders: string[] = []
    for (const pair of PAINTED) {
      for (const [name, theme] of themesFor(pair.file)) {
        const ground = opaque(theme, COLOUR_UTILITIES.get(pair.bg) ?? '')
        const ink = opaque(theme, COLOUR_UTILITIES.get(pair.fg) ?? '')
        if (ground === null || ink === null) continue
        const value = contrast(ink, ground)
        if (value < 4.5) {
          offenders.push(
            `${pair.file}:${String(pair.line)} — text-${pair.fg} on bg-${pair.bg} is ${value.toFixed(2)}:1 in ${name}`,
          )
        }
      }
    }
    // `--on-accent` and `--on-hot` exist for exactly this: an accent or hot fill
    // takes its own ink, never the page ground.
    expect(offenders).toEqual([])
  })
})

describe('tokens.css — responsive layer (ui-spec §7)', () => {
  const gridMinRules = rules.filter((r) => customProperties(r.body).has('--grid-min'))

  it('drives --grid-min from one block of min-width queries', () => {
    // One base declaration plus exactly three breakpoints. If this grows, the
    // grid column count has started to be decided in more than one place.
    expect(gridMinRules).toHaveLength(4)
    const media = gridMinRules.filter((r) => r.selector.startsWith('@media'))
    expect(media.map((r) => r.selector)).toEqual([
      '@media (min-width: 768px)',
      '@media (min-width: 1024px)',
      '@media (min-width: 1440px)',
    ])
  })

  it('resolves to 152 / 150 / 224 / 150 down the four tiers', () => {
    // Cascading min-width queries: the value for a tier is the last matching
    // declaration, so read them in reverse.
    const value = (width: number): string => {
      let out = ''
      for (const rule of gridMinRules) {
        const m = /min-width:\s*(\d+)px/.exec(rule.selector)
        const min = m?.[1] === undefined ? 0 : Number(m[1])
        if (width >= min) out = customProperties(rule.body).get('--grid-min') ?? out
      }
      return out
    }
    expect(value(1440)).toBe('152px')
    expect(value(1024)).toBe('150px')
    expect(value(768)).toBe('224px')
    expect(value(400)).toBe('150px')
  })

  it('collapses the sidebar 240 → 56 → drawer and drops the gap below 768', () => {
    const at = (width: number, prop: string): string => {
      let out = ''
      for (const rule of rules) {
        if (rule.selector.startsWith('@media')) {
          const m = /min-width:\s*(\d+)px/.exec(rule.selector)
          if (m?.[1] === undefined || width < Number(m[1])) continue
        } else if (!rule.selector.includes(':root')) {
          continue
        }
        const v = customProperties(rule.body).get(prop)
        if (v !== undefined) out = v
      }
      return out
    }
    expect(at(1440, '--sidebar-w')).toBe('240px')
    expect(at(1024, '--sidebar-w')).toBe('240px')
    expect(at(768, '--sidebar-w')).toBe('56px')
    expect(at(400, '--sidebar-w')).toBe('0px')
    expect(at(400, '--grid-gap')).toBe('12px')
    expect(at(768, '--grid-gap')).toBe('16px')
    expect(at(400, '--touch-min')).toBe('44px')
  })
})

describe('the style layer holds every colour literal', () => {
  it('keeps base.css free of hex — it may only reference tokens', () => {
    const hex = BASE.match(/#[0-9a-fA-F]{3,8}\b/g) ?? []
    expect(hex).toEqual([])
  })

  it('states the flush-left rule for block buttons', () => {
    // ui-spec §0.3: labels are flush left even inside full-width buttons. The
    // prototype centres them (E-32 §4 declines that).
    const btnBlock = findRule(allRules(BASE), '.btn-block')
    expect(btnBlock?.body).toMatch(/justify-content:\s*flex-start/)
  })

  it('themes focus with the hot marker, not the accent (E-32 §1)', () => {
    const base = allRules(BASE)
    const focusVisible = base.find((r) => r.selector === ':focus-visible')
    expect(focusVisible?.body).toMatch(/outline:\s*2px solid var\(--color-hot\)/)
    expect(focusVisible?.body).toMatch(/outline-offset:\s*2px/)
    const focus = base.find((r) => r.selector === ':focus')
    expect(focus?.body).toMatch(/outline:\s*none/)
  })

  it('rounds nothing with a number the token scale does not name', () => {
    // D-40's zero-radius rule is retired but its enforcement is not (E-32 §2):
    // every corner in the sheet is a `--radius-*` token, a true circle, or the
    // pill. `src/lib/hygiene.test.ts` holds the same line across all of src/.
    const allowed = new Set(['50%', '9999px'])
    for (const key of light.keys()) if (key.startsWith('--radius-')) allowed.add(`var(${key})`)
    const offenders = allRules(BASE)
      .filter((r) => !r.body.includes('{'))
      .flatMap((r) =>
        [...r.body.matchAll(/border-radius:\s*([^;}\n]+)/g)]
          .map((m) => (m[1] ?? '').trim())
          .filter((v) => !allowed.has(v))
          .map((v) => `${r.selector}: ${v}`),
      )
    expect(offenders).toEqual([])
  })

  it('gives the primitives the E-32 corners', () => {
    const radius = (selector: string): string | undefined =>
      /border-radius:\s*([^;}\n]+)/.exec(exactRule(selector)?.body ?? '')?.[1]?.trim()
    expect(radius('.btn')).toBe('var(--radius-pill)')
    expect(radius('.tag')).toBe('var(--radius-sm)')
    expect(radius('.input')).toBe('var(--radius-md)')
    expect(radius('.seg')).toBe('var(--radius-lg)')
    expect(radius('.card')).toBe('var(--radius-lg)')
    expect(radius('.dialog')).toBe('var(--radius-lg)')
  })

  it('insets the scrollbar thumb rather than painting a 10px slab', () => {
    // A 10px gutter with a 3px transparent border and `background-clip:
    // content-box` is what leaves a 4px pill. Dropping the clip paints the
    // border area too and the inset silently disappears.
    expect(exactRule('::-webkit-scrollbar')?.body).toMatch(/width:\s*10px/)
    const thumb = exactRule('::-webkit-scrollbar-thumb')?.body ?? ''
    expect(thumb).toMatch(/border-radius:\s*var\(--radius-full\)/)
    expect(thumb).toMatch(/border:\s*3px solid transparent/)
    expect(thumb).toMatch(/background-clip:\s*content-box/)
    expect(exactRule('::-webkit-scrollbar-thumb:hover')?.body).toMatch(
      /background:\s*var\(--scrollbar-thumb-hover\)/,
    )
    expect(exactRule('::-webkit-scrollbar-corner')).toBeDefined()
    expect(exactRule('*')?.body).toMatch(/scrollbar-width:\s*thin/)
  })

  it('gives the slider a 6px pill trough and a round thumb', () => {
    for (const track of [
      "input[type='range']::-webkit-slider-runnable-track",
      "input[type='range']::-moz-range-track",
    ]) {
      const body = exactRule(track)?.body ?? ''
      expect(body, track).toMatch(/height:\s*6px/)
      expect(body, track).toMatch(/border-radius:\s*var\(--radius-full\)/)
    }
    for (const thumb of [
      "input[type='range']::-webkit-slider-thumb",
      "input[type='range']::-moz-range-thumb",
    ]) {
      const body = exactRule(thumb)?.body ?? ''
      expect(body, thumb).toMatch(/width:\s*18px/)
      expect(body, thumb).toMatch(/height:\s*18px/)
      expect(body, thumb).toMatch(/border-radius:\s*50%/)
    }
  })
})

describe('touch targets below 768 (ui-spec §7, WP-05 acceptance 9)', () => {
  /** Declarations of `.selector` inside the `max-width: 767.98px` block. */
  const mobileRule = (selector: string): string => {
    const rule = allRules(BASE).find(
      (r) =>
        r.selector === selector &&
        r.context.some((c) => c.startsWith('@media') && c.includes('767.98px')),
    )
    if (rule === undefined) throw new Error(`base.css has no <768 rule for ${selector}`)
    return rule.body
  }

  it('constrains both axes of a button — a one-glyph `?` is only ~38px wide', () => {
    const btn = mobileRule('.btn')
    expect(btn).toMatch(/min-height:\s*var\(--touch-min\)/)
    expect(btn).toMatch(/min-width:\s*var\(--touch-min\)/)
  })

  it('grows the page slider from its 24px desktop box to a finger', () => {
    // A range input with no height collapses onto its track. The viewer's
    // slider used to carry 44px inline at *every* width, which held this
    // minimum but made the bottom bar 12px taller than the design on a desktop.
    expect(findRule(allRules(BASE), "input[type='range']")?.body).toMatch(/height:\s*24px/)
    expect(mobileRule("input[type='range']")).toMatch(/height:\s*var\(--touch-min\)/)
    expect(mobileRule("input[type='range']::-webkit-slider-thumb")).toMatch(/height:\s*26px/)
  })

  it('covers every control the shell puts on a phone', () => {
    // The scan row is a real control (it opens the scan log, ui-spec §4.1) but
    // it is a 7px dot plus one 12px line, so it needs the rule stated.
    for (const selector of ['.btn-icon', '.seg-opt', '.input', '.scan-indicator']) {
      expect(mobileRule(selector)).toMatch(/(min-)?height:\s*var\(--touch-min\)/)
    }
    // NFR-CMP-002. The prototype drops this row to 42px (E-32 §4 declines it).
    expect(findRule(allRules(BASE), '.sidebar-nav-row')?.body).toMatch(
      /min-height:\s*var\(--touch-min\)/,
    )
  })
})
