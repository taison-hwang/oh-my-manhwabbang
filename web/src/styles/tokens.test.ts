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

// ---------------------------------------------------------------------------
// The paper wash
//
// Every pair below is painted *under* the grain, so the ratio the reader gets is
// not the ratio of the two tokens. This was found the expensive way: the texture
// shipped with a comment claiming it broke no AA floor, because the floor was
// checked by looking at the pair that *dropped* the most (9.164 → 8.880) rather
// than the pair with the least room over 4.5. Three pairs had under 0.11 of
// margin and the grain took all three under AA.
//
// A drop is not a failure and a small drop is not a small risk. What matters is
// the ratio after the wash, so that is what is measured — and the model is the
// composite the browser actually performs, with the mask's own numbers read out
// of `--paper-grain` rather than restated.
// ---------------------------------------------------------------------------

/** `feColorMatrix` row 4 of `--paper-grain`: how much noise reaches alpha. */
const GRAIN_AMPLITUDE = ((): number => {
  const values = /values='([^']+)'/.exec(light.get('--paper-grain') ?? '')?.[1] ?? ''
  return Number(values.split('%20')[15])
})()

/**
 * `fractalNoise` centres each channel on 0.5, so the mean mask alpha is half the
 * amplitude — and the peak is a fixed fraction of it too.
 *
 * Both are **derived from `--paper-grain`**, not written down. The peak used to
 * be the literal `29 / 255`, read off one Chrome render at the shipped
 * amplitude, and a literal is the wrong shape for this number: raise the
 * amplitude to 0.3 and a fixed 0.114 is *below* the mean alpha (0.150), so the
 * peak figure silently becomes the weaker of the two and every statement made
 * about it goes vacuous while staying green.
 *
 * 0.913 is the measured ratio of peak to amplitude — 0.11504 against the 0.126
 * the matrix declares, from a 1.26 M-pixel census of the mask over a white
 * board. The 29/255 literal was 1.2 % optimistic against that same census.
 */
const GRAIN_MEAN_ALPHA = GRAIN_AMPLITUDE * 0.5
const GRAIN_PEAK_ALPHA = GRAIN_AMPLITUDE * 0.913

/**
 * The three tones the grain is ever painted in — app light, app dark, viewer.
 * Every pair is held to the *worst* of them rather than to the one its scope
 * happens to use today: the scope of a token is a fact about components, this
 * file measures tokens, and a floor fitted to today's call sites is a floor that
 * moves when a component does.
 */
const GRAIN_TONES: Rgba[] = [
  resolveToken(light, '--paper-tone'),
  // The dark tone is a ramp step and the dark block does not re-declare the
  // ramps, so it only resolves through the layered cascade — the same hole
  // `DARK_CASCADE` closes for the component scanner further down. That constant
  // is declared after this one, so the layering is spelled out here.
  resolveToken(new Map([...light, ...dark]), '--paper-tone'),
  resolveToken(light, '--paper-tone-viewer'),
].map(parseColour)

/** One colour after the grain has been composited over it. */
function washed(colour: Rgba, tone: Rgba, alpha: number): Rgba {
  const a = Number(light.get('--paper-intensity')) * alpha
  return {
    r: colour.r * (1 - a) + tone.r * a,
    g: colour.g * (1 - a) + tone.g * a,
    b: colour.b * (1 - a) + tone.b * a,
    a: 1,
  }
}

/**
 * Contrast of `token` on `ground` **as the reader sees it**: the ink composites
 * onto its ground first (a translucent ink is not washed on its own — it is part
 * of what the grain lands on), then the grain washes the result and the ground
 * alike. Reported at the worst of the three tones.
 */
function washedRatio(
  map: Map<string, string>,
  token: string,
  ground: string,
  alpha: number = GRAIN_MEAN_ALPHA,
): number {
  const bg = parseColour(resolveToken(map, ground))
  const fg = over(parseColour(resolveToken(map, token)), bg)
  return Math.min(
    ...GRAIN_TONES.map((tone) => contrast(washed(fg, tone, alpha), washed(bg, tone, alpha))),
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

  it('gives the sidebar an elevation with no vertical lobe (open item p)', () => {
    // The prototype's `4px 0 18px rgba(150,128,96,.16)`, which is why it is not
    // on the sm/md/lg scale: it is horizontal only. `--shadow-md` stood in for
    // it, and that token's 6px *downward* offset drew a shadow under the top
    // edge of a panel that runs the full height of the viewport and therefore
    // casts nothing there.
    expect(light.get('--shadow-sidebar')).toBe('4px 0 18px rgb(150 128 96 / 0.16)')
    // The middle `0` is the whole difference from `--shadow-md`. A y-offset
    // here is the approximation coming back.
    expect(light.get('--shadow-sidebar')).toMatch(/^\d+px 0 /)
    expect(light.get('--shadow-sidebar')).not.toBe(light.get('--shadow-md'))
  })

  it('names all three stops of the cover gradient (open item p)', () => {
    // .92 at the bottom, .55 at 62 %, .15 at the top — the prototype's numbers.
    // They were approximated with the three tokens that happened to exist
    // (.72 / .50 / .07), so the buttons at the bottom of a hovered card sat on
    // two thirds of their ground and the top of the card had almost no wash.
    expect(light.get('--scrim-cover-base')).toBe('rgb(38 59 56 / 0.92)')
    expect(light.get('--scrim-cover-mid')).toBe('rgb(38 59 56 / 0.55)')
    expect(light.get('--scrim-cover-top')).toBe('rgb(38 59 56 / 0.15)')
    // Each must differ from the token it replaced, or nothing changed.
    expect(light.get('--scrim-cover-base')).not.toBe(light.get('--scrim-cover'))
    expect(light.get('--scrim-cover-mid')).not.toBe(light.get('--scrim-modal'))
    expect(light.get('--scrim-cover-top')).not.toBe(light.get('--hover-tint'))
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
      '--scrim-cover-base',
      '--scrim-cover-mid',
      '--scrim-cover-top',
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
    for (const token of [
      '--scrim-volume-end',
      '--scrim-broken',
      '--on-accent',
      '--on-hot',
      // The grain's viewer tone belongs to the same family: the reading screen
      // is dark in both app themes, so a tone that flipped with the app would
      // repaint the viewer's texture when the library's theme changed.
      '--paper-tone-viewer',
    ]) {
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
    for (const step of [
      '--shadow-sm',
      '--shadow-md',
      '--shadow-lg',
      '--shadow-inset',
      '--shadow-sidebar',
    ]) {
      expect(dark.get(step), `${step} kept the cream highlight lobe`).not.toContain('255 253 246')
    }
    expect(dark.get('--shadow-lg')).toContain('#3E5B57')
    expect(dark.get('--shadow-lg')).toContain('rgb(0 0 0 / 0.6)')
    expect(dark.get('--shadow-inset')).toContain('inset')
    // The sidebar's dark form keeps the hairline the other three use, but only
    // on the side that shows: `1px 0 0` is the right edge of a full-height
    // panel, where `0 0 0 1px` would ring three edges that are off-screen. The
    // ochre lobe is gone for the reason the whole dark block exists — it is a
    // light-ground device.
    expect(dark.get('--shadow-sidebar')).toContain('1px 0 0 #3E5B57')
    expect(dark.get('--shadow-sidebar')).not.toContain('150 128 96')
  })

  it('turns the grain to the cool end of the ramp on a teal ground', () => {
    // Worth <1/255 at today's intensity — a dark ground is already most of the
    // way to a near-black tone — and declared anyway, because the tone is the
    // one part of the texture that is paint. The failure it forecloses is the
    // one this whole block is about: a value that is right in one theme because
    // nobody ever asked what it does in the other.
    // `DARK_CASCADE`, not `dark`: the dark block does not re-declare the ramps,
    // so the step this points at is reached by inheritance — the same hole the
    // scanner further down exists to close.
    expect(resolveToken(light, '--paper-tone')).toBe('#23211D') // neutral-900
    expect(resolveToken(DARK_CASCADE, '--paper-tone')).toBe('#082325') // accent-900
    expect(dark.get('--paper-tone')).not.toBe(light.get('--paper-tone'))
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

  it('still clears AA once the paper grain is on it — the floor that matters', () => {
    // The assertion above measures two tokens. The reader sees them through a
    // full-viewport texture, and the texture is not free: it took three pairs
    // from a pass to a fail while every test in this file stayed green, because
    // nothing here knew the grain existed.
    //
    // The failure message carries the peak-alpha ratio as well as the mean one.
    // The floor is the mean — that is what almost every pixel of a glyph sits
    // under — but a pair that only clears at the mean is a pair whose darkest
    // grain speckles are already below AA, and that is worth seeing when this
    // fails.
    const offenders: string[] = []
    for (const [name, theme] of themes) {
      for (const token of INK_TOKENS) {
        for (const ground of ['--color-bg', '--color-surface']) {
          const wet = washedRatio(theme, token, ground)
          if (wet < 4.5) {
            const peak = washedRatio(theme, token, ground, GRAIN_PEAK_ALPHA)
            offenders.push(
              `${name} ${token} on ${ground}: dry ${ratio(theme, token, ground).toFixed(3)} → washed ${wet.toFixed(3)} (peak ${peak.toFixed(3)})`,
            )
          }
        }
      }
    }
    expect(offenders).toEqual([])
  })

  /** The three tokens the grain took under AA, and the pairs they were taken on. */
  const REPAIRED: [string, Map<string, string>, string, string][] = [
    ['the override chip', light, '--on-hot', '--color-hot'],
    ['light meta text on the ground', light, '--ink-faint', '--color-bg'],
    ['dark card meta on the surface', dark, '--ink-meta', '--color-surface'],
  ]

  it('re-derives the three pairs the grain took under AA', () => {
    // Named individually because each was a *specific* token that had to move,
    // and a floor test alone would let the next author fix a failure by lowering
    // `--paper-intensity` instead — which is the one repair that is not
    // available here. `--on-hot` needs intensity ≤ 0.12 to survive at its old
    // value, and 0.12 is the design erased rather than implemented.
    for (const [what, theme, token, ground] of REPAIRED) {
      const wet = washedRatio(theme, token, ground)
      expect(wet, `${what}: ${token} on ${ground} washed`).toBeGreaterThanOrEqual(4.5)
    }
  })

  it('reports the peak-grain margin, and holds nothing to it', () => {
    // **Why the peak is not a floor.** It was one, and it should not have been.
    //
    //  * WCAG is defined on the *specified* colours. The mean wash is already a
    //    generous reading of it; the peak is the darkest speckle of a random
    //    field, present on a small minority of pixels and never on all of a
    //    glyph at once.
    //  * The number is not stable enough to gate on. `--on-hot` on the hot
    //    marker comes out at 4.51, 4.508, 4.490 or 4.464 depending only on
    //    whether the peak alpha is the old literal, a measured point estimate,
    //    that estimate's 8-bit upper bound, or the amplitude the matrix
    //    declares. Two of those four fail. A gate whose verdict flips on the
    //    definition of its own input is not measuring the palette.
    //  * And the render does not have that precision anyway: the chip is 9px
    //    uppercase, where antialiasing dominates and the *effective* ratio is
    //    about 3.68 before any grain is applied. The argument was being had
    //    below the resolution of the thing being argued about.
    //
    // **What that leaves open.** `--on-hot` is `#000000` — the absolute ceiling
    // of this palette, 4.9988 against `#EC3013`, with no darker ink available.
    // Buying real margin back means raising the relative luminance of
    // `--color-hot` by ~4 %, and that is a change to the retired brand red that
    // E-32 §1 pinned: a ruling of its own, not a side effect of a texture.
    //
    // Today, at the shipped amplitude: chip 4.725 / 4.508, ink-faint
    // 4.740 / 4.631, ink-meta 4.853 / 4.709 (mean / peak).
    const report = REPAIRED.map(([what, theme, token, ground]) => ({
      what,
      mean: washedRatio(theme, token, ground),
      peak: washedRatio(theme, token, ground, GRAIN_PEAK_ALPHA),
    }))
    const table = report
      .map((r) => `${r.what}: mean ${r.mean.toFixed(3)}, peak ${r.peak.toFixed(3)}`)
      .join(' | ')

    // The assertions are on the *model*, so the report cannot quietly become a
    // report about nothing: a peak alpha that stopped exceeding the mean would
    // make every peak figure above the mean figure and the table meaningless.
    expect(GRAIN_PEAK_ALPHA, table).toBeGreaterThan(GRAIN_MEAN_ALPHA)
    expect(report.every((r) => r.peak < r.mean), table).toBe(true)
    // Derived, not written down — the literal is what went vacuous at
    // amplitude 0.3.
    expect(GRAIN_PEAK_ALPHA / GRAIN_AMPLITUDE).toBeCloseTo(0.913, 6)
  })

  it('reads the mask its own numbers rather than restating them', () => {
    // Calibration for the model. If the amplitude ever stops parsing, every
    // washed ratio above collapses to the dry one and the block goes green
    // while checking nothing — the exact failure this whole section exists to
    // stop happening twice.
    expect(GRAIN_AMPLITUDE).toBeCloseTo(0.126, 4)
    expect(GRAIN_MEAN_ALPHA).toBeCloseTo(0.063, 4)
    expect(GRAIN_TONES).toHaveLength(3)
    // Three genuinely different tones, or "the worst of them" is one of them.
    expect(new Set(GRAIN_TONES.map((t) => [t.r, t.g, t.b].join(','))).size).toBe(3)
    // And the wash has to actually move a ratio, or the floor is the dry floor
    // wearing a different name.
    expect(washedRatio(light, '--ink', '--color-bg')).toBeLessThan(ratio(light, '--ink', '--color-bg'))
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
//  2. **Text whose ground comes from an ancestor.** This is the big one, it is
//     structural, and widening `FILL` did nothing for it: the scan pairs a fill
//     and an ink **inside one class list**. A `<span className="text-…">` whose
//     ground is painted three elements up matches no fill at all and is skipped
//     in silence.
//
//     Two shipped defects are the evidence, both found by hand and both fixed in
//     the same change that wrote this paragraph. `ViewerTopBar` set the volume
//     name to `text-neutral-500` and `ViewerBottomBar` set the pressed thumbnail
//     button to `text-accent-400`; the ground for both is the bar's `bg-bg`,
//     inside `data-theme="dark"`, on an element neither string mentions. The
//     ramps do not flip, so the two were 4.34 and 3.76 — and 4.19 and 3.64 once
//     the bars started carrying the paper grain. Nothing here could see either.
//
//     Closing it needs the *rendered* tree, not a source string: a real browser
//     with `getComputedStyle`, i.e. the e2e tier. Until then this list is the
//     honest statement of what a green run here does not mean.
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
const FILL = /^(bg|surface|accent|accent-2|accent-hover|accent-press|accent-fill|ink|hot)$|^(accent|accent-2|neutral)-\d00$/

// `bg` and `surface` are in that list, and used not to be. The argument for
// leaving them out was that `INK_TOKENS` already measures every ink against both
// grounds — which is true, and which is exactly why it was the wrong argument:
// what lands on those grounds is not always an ink token. `FormatBadge`'s corner
// pill painted `bg-surface text-accent-800`, a raw ramp step on a semantic
// ground, and the ramps do not flip: 11.62:1 on the cream surface, **1.40:1** on
// the dark one. The pair was skipped entirely, so the scanner was green about a
// badge that is a smudge in half the product.

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
    // The two the semantic grounds bought. Neither was visible to this scan
    // before `bg`/`surface` joined `FILL`, and one of them was a real defect.
    expect(seen).toContain(join('components', 'ds', 'FormatBadge.tsx') + ' bg-surface text-accent-text')
    expect(seen).toContain(join('features', 'viewer', 'ViewerPage.tsx') + ' bg-bg text-neutral-400')
    expect(PAINTED.length).toBeGreaterThanOrEqual(6)
  })

  it('reads at AA on every solid fill it paints — under the paper grain', () => {
    // Washed, not dry, and for the same reason the ink floor above is: these
    // pairs are painted under the texture too. `bg-hot text-on-hot` is the
    // override chip, which is the pair the grain took furthest under AA, and a
    // dry scan here would have gone on calling it a pass.
    const offenders: string[] = []
    for (const pair of PAINTED) {
      for (const [name, theme] of themesFor(pair.file)) {
        const ground = opaque(theme, COLOUR_UTILITIES.get(pair.bg) ?? '')
        const ink = opaque(theme, COLOUR_UTILITIES.get(pair.fg) ?? '')
        if (ground === null || ink === null) continue
        const value = Math.min(
          ...GRAIN_TONES.map((tone) =>
            contrast(washed(ink, tone, GRAIN_MEAN_ALPHA), washed(ground, tone, GRAIN_MEAN_ALPHA)),
          ),
        )
        if (value < 4.5) {
          offenders.push(
            `${pair.file}:${String(pair.line)} — text-${pair.fg} on bg-${pair.bg} is ${value.toFixed(2)}:1 washed, in ${name}`,
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

  it('draws the cover gradient from its own three stops (open item p)', () => {
    // The approximation is gone, and the three tokens it borrowed must not come
    // back: `--scrim-cover` is still the flat chip wash on the thumbnail strip,
    // `--scrim-modal` is still the dialog backdrop, and `--hover-tint` is a
    // pointer state. Any of them appearing here again is the same reach for
    // "whatever token is nearest" that produced .72 / .50 / .07.
    const scrim = exactRule('.cover-scrim')?.body ?? ''
    expect(scrim).toContain('var(--scrim-cover-base)')
    expect(scrim).toContain('var(--scrim-cover-mid) 62%')
    expect(scrim).toContain('var(--scrim-cover-top)')
    expect(scrim).not.toMatch(/var\(--scrim-cover\)|var\(--scrim-modal\)|var\(--hover-tint\)/)
  })

  it('hangs the sidebar edge on its own token, not the card shadow', () => {
    expect(exactRule('.sidebar')?.body).toMatch(/box-shadow:\s*var\(--shadow-sidebar\)/)
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

// ---------------------------------------------------------------------------
// The paper grain
//
// A full-viewport comic-paper texture, added from the Claude Design prototype.
// What is asserted here is not "it looks like paper" — it is the four properties
// that made the prototype's implementation unusable in this product, each of
// which is invisible from a screenshot:
//
//  1. it is deterministic (the prototype re-rolls `Math.random()` every load and
//     changes 89.1 % of the pixels, which makes `docs/ui-shots/` worthless),
//  2. it fetches nothing (NFR-OPS-001: one binary, no CDN — the font is
//     vendored for the same reason),
//  3. it carries no colour of its own, so the tone is a token and re-themes,
//  4. the viewer's tone is genuinely a different colour from the app's, because
//     a switch that resolves to the same value is a switch nobody can see —
//     which is exactly what the prototype ships.
// ---------------------------------------------------------------------------

describe('the paper grain (tokens.css)', () => {
  const grain = light.get('--paper-grain') ?? ''
  /** The 20 numbers of the grain's `feColorMatrix`, in row order. */
  const matrix = (/values='([^']+)'/.exec(grain)?.[1] ?? '').split('%20').map(Number)

  it('is an inline SVG data URI — nothing is fetched to draw it', () => {
    expect(grain.startsWith('url("data:image/svg+xml,')).toBe(true)
    // The single `http://` in the value is the SVG namespace, which is an
    // identifier and not a URL any browser resolves. A *second* one would be a
    // real request, and the prototype makes two of them.
    expect(grain.match(/https?:\/\/[^'"\s]*/g)).toEqual(['http://www.w3.org/2000/svg'])
  })

  it('pins the seed and stitches the tile, so two loads are the same bytes', () => {
    // The SVG filter PRNG is specified, so `seed` is reproducible across runs
    // and engines. The prototype declares `seed="4"` too — and then never reads
    // it, because its fallback tile calls `Math.random()` directly.
    expect(grain).toContain("type='fractalNoise'")
    expect(grain).toContain("seed='4'")
    // Without `stitch` the 200px tile does not wrap and the repeat seams.
    expect(grain).toContain("stitchTiles='stitch'")
  })

  it('writes alpha and nothing else, which is what keeps the tone a token', () => {
    // Rows 1–3 zero the RGB, row 4 scales the noise's red channel into alpha.
    // The moment any RGB coefficient is non-zero the SVG carries a colour, and
    // a colour inside a data URI is a colour no theme can reach.
    expect(matrix).toHaveLength(20)
    expect(matrix.slice(0, 15)).toEqual(new Array(15).fill(0))
    expect(matrix[15]).toBeCloseTo(0.126, 4)
    expect(matrix.slice(16)).toEqual([0, 0, 0, 0])
  })

  it('has no colour literal buried in the data URI', () => {
    expect(grain).not.toMatch(/%23[0-9a-fA-F]{3}/) // an encoded hex
    expect(grain).not.toMatch(/(fill|stroke|flood-color|stop-color)=/)
  })

  it('takes the app tone off the ramp and the intensity from the prototype', () => {
    // `--color-neutral-900` is #23211D against the prototype's #201E1D: three
    // units on two channels, which at ≤0.114 alpha and half intensity is
    // 0.16/255 of difference. Same colour, one fewer literal in the file.
    expect(resolveToken(light, '--paper-tone')).toBe('#23211D')
    expect(light.get('--paper-intensity')).toBe('0.5')
  })

  it('gives the viewer a tone that is a different colour, not a different name', () => {
    // The prototype specifies #0D0C0C here and never applies it. A token that
    // resolved to the app's own tone would reproduce that bug with a passing
    // test, so the assertion is on the *values* being different.
    expect(light.get('--paper-tone-viewer')).toBe('#0D0C0C')
    expect(light.get('--paper-tone-viewer')).not.toBe(resolveToken(light, '--paper-tone'))
    expect(light.get('--paper-tone-viewer')).not.toBe(resolveToken(DARK_CASCADE, '--paper-tone'))
  })

  it('keeps the mask and the intensity out of the theme blocks', () => {
    // Geometry, not paint: the grain is the same noise in both themes and only
    // what it washes in changes. Re-declaring either in the dark block is
    // how a texture starts having two sources of truth.
    for (const token of ['--paper-grain', '--paper-intensity']) {
      expect(light.has(token), `${token} missing from the base block`).toBe(true)
      expect(dark.has(token), `${token} must not flip with the theme`).toBe(false)
    }
  })

  it('lands the −6~7/255 the prototype was measured at on the cream ground', () => {
    // The one number a reviewer can check against the prototype without a
    // browser. `fractalNoise` centres each channel on 0.5, so the mean mask
    // alpha is half the matrix coefficient — 0.063, i.e. 16.1/255, which is
    // also what the prototype's own fallback tile averages
    // (`(Math.random()*0.5 + fiber*0.12) * 52`). Derived from the matrix rather
    // than restated, so tuning the grain moves this with it.
    const ground = parseColour(resolveToken(light, '--color-bg'))
    const tone = parseColour(resolveToken(light, '--paper-tone'))
    const meanAlpha = (matrix[15] ?? 0) * 0.5
    const intensity = Number(light.get('--paper-intensity'))
    const delta = intensity * meanAlpha * (ground.r - (ground.r * tone.r) / 255)
    expect(delta).toBeGreaterThan(5.5)
    expect(delta).toBeLessThan(7.5)
  })
})

describe('the z ladder is closed, and stated in both files (ui-spec §3)', () => {
  it('rises content < sticky < viewer < overlay < texture', () => {
    const z = (name: string): number => Number(light.get(`--z-${name}`))
    expect([z('content'), z('sticky'), z('viewer'), z('overlay'), z('texture')]).toEqual([
      0, 2, 60, 80, 90,
    ])
  })

  it('agrees with tailwind.config.ts, the other half of the same ladder', () => {
    // Two files, one ladder. `chrome: 3` is the one rung with no token: it
    // orders two elements inside the viewer's own subtree and never competes
    // with anything here.
    const z = tailwindConfig.theme.extend.zIndex
    expect(z.content).toBe(light.get('--z-content'))
    expect(z.sticky).toBe(light.get('--z-sticky'))
    expect(z.viewer).toBe(light.get('--z-viewer'))
    expect(z.overlay).toBe(light.get('--z-overlay'))
    expect(z.texture).toBe(light.get('--z-texture'))
  })

  it('leaves the texture at the top — nothing in base.css outranks it', () => {
    // A rule that punched through the grain would be a rectangle of un-papered
    // screen, which is what a bare `z-index: 900` in a sheet with a closed
    // ladder eventually produces.
    const ceiling = Number(light.get('--z-texture'))
    const offenders = allRules(BASE)
      .flatMap((r) =>
        [...r.body.matchAll(/z-index:\s*(\d+)\s*;/g)].map((m) => `${r.selector}: ${m[1] ?? ''}`),
      )
      .filter((hit) => Number(hit.split(': ')[1]) > ceiling)
    expect(offenders).toEqual([])
  })
})

describe('the paper grain layer (base.css)', () => {
  /** A rule in the sheet proper, i.e. not the one inside a media query. */
  const plain = (selector: string): string =>
    allRules(BASE).find(
      (r) => r.selector === selector && !r.context.some((c) => c.startsWith('@media')),
    )?.body ?? ''

  const layer = plain('body::after')

  it('is one fixed, inert layer at the top of the ladder', () => {
    // `body::after` and not a component: no route can forget to mount it and
    // nothing has to be imported to get it. Fixed rather than absolute so it
    // does not scroll with the shell, and inert because it covers every control
    // in the product — including the dialogs, which is the point of `--z-texture`.
    expect(layer).toMatch(/position:\s*fixed/)
    expect(layer).toMatch(/inset:\s*0/)
    expect(layer).toMatch(/pointer-events:\s*none/)
    expect(layer).toMatch(/z-index:\s*var\(--z-texture\)/)
  })

  it('paints a token through the mask rather than a colour through the SVG', () => {
    expect(layer).toMatch(/background-color:\s*var\(--paper-tone\)/)
    expect(layer).toMatch(/-webkit-mask-image:\s*var\(--paper-grain\)/)
    expect(layer).toMatch(/[^-]mask-image:\s*var\(--paper-grain\)/)
    expect(layer).toMatch(/opacity:\s*var\(--paper-intensity\)/)
  })

  it('does not blend — the prototype does, and it is the whole cost', () => {
    // Measured in Chrome on real page turns (base.css carries the table):
    // `mix-blend-mode: multiply` does not make a frame slower — the median frame
    // is 16.7 ms either way — it makes 38 % of frames not arrive, taking 60 fps
    // to 28. Dropping the mask instead changes nothing. And it buys ≤2/255 at
    // this tone and amplitude.
    //
    // This is the assertion that stops it coming back by looking correct: the
    // prototype writes multiply, and copying it in is a one-word change that no
    // screenshot could ever fail.
    expect(layer).not.toMatch(/mix-blend-mode/)
  })

  it('takes the whole layer off the viewer through :has(), not through the theme', () => {
    // The layer is a sibling of #root and the viewer re-scopes `data-theme` on a
    // div *inside* it, so inheritance cannot reach here. `[data-theme]` would be
    // the wrong key anyway: the viewer is dark in both app themes, so the
    // attribute cannot tell "the reading screen" from "a dark library".
    //
    // `display: none`, not a different tone: the reading stage is not paper to
    // print on (measured — the grain moved 100 % of the artwork's pixels), one
    // `body::after` cannot have a hole cut in it, and the stage's rectangle is
    // `flex: 1` between two bars that wrap and un-wrap. The chrome puts the
    // texture back on its own boxes below.
    expect(plain("body:has([data-role='viewer'])::after")).toMatch(/display:\s*none/)
    // ...and the hook has to be the attribute the viewer actually writes.
    expect(read(join('src', 'features', 'viewer', 'ViewerPage.tsx'))).toContain(
      'data-role="viewer"',
    )
  })

  /** Every viewer surface the texture is declared on, in selector order. */
  const CHROME_SURFACES = [
    "[data-role='viewer-top-bar']",
    "[data-role='viewer-bottom-bar']",
    "[data-role='viewer-chrome-hint']",
    "[data-role='stale-progress'] > span",
    "[data-role='page-error']",
    "[data-role='next-volume-card']",
  ]

  it('re-grains every opaque viewer surface, one box at a time', () => {
    // The rule is "an opaque UI surface gets paper, the drawing does not", and
    // the first cut of it only covered the two bars and the end card. Three
    // opaque grounds sat outside those and shipped bare: the chrome hint, the
    // stale-progress notice and the page-error panel. `page-error` is the
    // interesting one — it occupies the stage, but it *replaces* the artwork
    // with a failure message, so by the rule it is a surface, not a drawing.
    const chrome = plain(CHROME_SURFACES.map((s) => `${s}::after`).join(',\n  '))
    expect(chrome).toMatch(/background-color:\s*var\(--paper-tone-viewer\)/)
    expect(chrome).toMatch(/mask-image:\s*var\(--paper-grain\)/)
    expect(chrome).toMatch(/opacity:\s*var\(--paper-intensity\)/)
    expect(chrome).toMatch(/position:\s*absolute/)
    expect(chrome).toMatch(/inset:\s*0/)
    expect(chrome).toMatch(/pointer-events:\s*none/)
    expect(chrome).not.toMatch(/mix-blend-mode/)

    const selectors = allRules(BASE).map((r) => r.selector)
    // `next-volume-scrim` is a scrim *over* the drawing, not a ground of its
    // own; papering it would paper the artwork underneath.
    expect(selectors.join('\n')).not.toContain("[data-role='next-volume-scrim']::after")
    // ...and the stale notice is matched at its span, because its wrapper is a
    // full-width transparent row: a rule there lays the wash over the drawing.
    expect(selectors.join('\n')).not.toContain("[data-role='stale-progress']::after")
  })

  it('contains the texture on the surfaces that are not stacking contexts', () => {
    // Without this the `::after` escapes to the viewer root and paints at
    // `--z-texture` over everything, the stage included — a silent, total
    // reversal of the ruling from two missing lines. The two bars need nothing:
    // `z-chrome` already makes each one a stacking context.
    const contained = plain(
      "[data-role='next-volume-card'],\n  [data-role='stale-progress'] > span,\n  [data-role='viewer-chrome-hint'],\n  [data-role='page-error']",
    )
    expect(contained).toMatch(/position:\s*relative/)
    expect(contained).toMatch(/z-index:\s*var\(--z-content\)/)
  })

  it('names only roles the viewer really writes', () => {
    // Seven `data-role` values are load-bearing across a file boundary now. A
    // renamed attribute would not break a test on either side — it would just
    // stop the texture appearing, in a build nobody screenshots.
    const sources: [string, string[]][] = [
      ['ViewerPage.tsx', ['viewer', 'viewer-chrome-hint', 'stale-progress']],
      ['ViewerTopBar.tsx', ['viewer-top-bar']],
      ['ViewerBottomBar.tsx', ['viewer-bottom-bar']],
      ['NextVolumeCard.tsx', ['next-volume-card']],
      ['PageError.tsx', ['page-error']],
    ]
    for (const [file, roles] of sources) {
      const source = read(join('src', 'features', 'viewer', file))
      for (const role of roles) {
        expect(source, `${file} no longer writes data-role="${role}"`).toContain(
          `data-role="${role}"`,
        )
      }
    }
    // The strip is inside the bottom bar, which is why it needs no rule of its
    // own — and if it ever moves out, it loses its texture silently.
    expect(read(join('src', 'features', 'viewer', 'ViewerBottomBar.tsx'))).toContain(
      '<ThumbnailStrip',
    )
    // `stale-progress` is papered at its span, so the span has to stay the
    // element that carries the ground.
    expect(read(join('src', 'features', 'viewer', 'ViewerPage.tsx'))).toMatch(
      /data-role="stale-progress"[\s\S]{0,600}?<span className="bg-accent/,
    )
  })

  it('turns every layer off under prefers-contrast: more', () => {
    // The grain costs up to 0.284 of ratio, and three pairs in this palette are
    // re-derived in tokens.css because of it. Everything clears AA with the
    // paper on, so this is a preference rather than a repair — but it has to
    // cover all four layers. Switching off the global one alone would leave the
    // viewer chrome as the only papered surface in the product, for the reader
    // least able to spare the contrast.
    const off = allRules(BASE).find(
      (r) =>
        r.selector.includes('body::after') &&
        r.context.some((c) => c.includes('prefers-contrast')),
    )
    expect(off?.context.some((c) => c.includes('more'))).toBe(true)
    expect(off?.body).toMatch(/display:\s*none/)
    // Every surface the texture is declared on, or the ones left out are the
    // only paper in the product for the reader least able to spare it.
    for (const surface of CHROME_SURFACES) {
      expect(off?.selector, `${surface} keeps its texture`).toContain(`${surface}::after`)
    }
  })
})

// ---------------------------------------------------------------------------
// Raw ramp steps painted by a CSS class
//
// The ramps are a theme-invariant absolute lightness scale (ui-spec §1.4), so a
// class that paints one paints the *same* colour in a dark scope as in a light
// one. `.row-chip:hover` was given a dark override for exactly that reason and
// says so in its comment; `.tag-accent`, `.tag-accent-2` and `.tag-neutral` were
// not, and shipped a near-white chip on the dark ground — 10.05:1 and 10.74:1
// against it.
//
// Nothing caught them. The scanner further up reads Tailwind class lists out of
// `.tsx` and never opens a stylesheet, and the token tests read tokens and never
// look at a class. This is the stylesheet's half of the same question, and it is
// deliberately blunt: **every** base.css rule that paints a ramp step must have a
// `[data-theme='dark']` counterpart. No exemption list — a ramp step that
// genuinely must not flip is one semantic token away, and that is the fix.
// ---------------------------------------------------------------------------

describe('a ramp step painted by a class flips with the theme (ui-spec §1.4)', () => {
  const RAMP_PAINT = /(background|background-color|color)\s*:\s*var\((--color-(?:neutral|accent(?:-2)?)-\d00)\)/g

  const rampRules = allRules(BASE)
    .filter((r) => !r.selector.startsWith('@'))
    .map((r) => ({
      selector: r.selector,
      paint: new Map(
        [...r.body.matchAll(RAMP_PAINT)].map((m) => [m[1] === 'color' ? 'fg' : 'bg', m[2] ?? '']),
      ),
    }))
    .filter((r) => r.paint.size > 0)

  const isDark = (selector: string): boolean => selector.startsWith("[data-theme='dark']")

  it('found the ramp users it is meant to police', () => {
    // Calibration, and it names them: a regex that quietly stopped matching
    // would leave every assertion below iterating over an empty list and the
    // whole block green while checking nothing.
    expect(rampRules.map((r) => r.selector).sort()).toEqual([
      '.row-chip:hover',
      '.tag-accent',
      '.tag-accent-2',
      '.tag-neutral',
      "[data-theme='dark'] .tag-accent",
      "[data-theme='dark'] .tag-accent-2",
      "[data-theme='dark'] .tag-neutral",
    ])
  })

  it('gives every one of them a dark counterpart', () => {
    const selectors = allRules(BASE).map((r) => r.selector)
    const offenders = rampRules
      .map((r) => r.selector)
      .filter((s) => !isDark(s))
      .filter((s) => !selectors.includes(`[data-theme='dark'] ${s}`))
    expect(offenders).toEqual([])
  })

  it('reads at AA inside every chip, in the theme that paints it', () => {
    const offenders: string[] = []
    for (const rule of rampRules) {
      const bg = rule.paint.get('bg')
      const fg = rule.paint.get('fg')
      if (bg === undefined || fg === undefined) continue
      const theme = isDark(rule.selector) ? DARK_CASCADE : light
      const value = contrast(
        parseColour(resolveToken(theme, fg)),
        parseColour(resolveToken(theme, bg)),
      )
      if (value < 4.5) offenders.push(`${rule.selector}: ${fg} on ${bg} is ${value.toFixed(2)}:1`)
    }
    expect(offenders).toEqual([])
  })

  it('keeps the chip near its ground — the defect this block exists for', () => {
    // Not a floor but a ceiling, and it is the assertion that would have caught
    // the bug: a tag is a label printed on the page, not a second page. Every
    // chip in the sheet sits between 1.01 and 1.15 against the ground of the
    // theme that paints it. The three broken ones were at 10.05 and 10.74 —
    // white slabs — so 2.0 is a wide margin that still fails them decisively.
    const offenders: string[] = []
    for (const rule of rampRules) {
      const bg = rule.paint.get('bg')
      if (bg === undefined) continue
      const theme = isDark(rule.selector) ? DARK_CASCADE : light
      const value = contrast(
        parseColour(resolveToken(theme, bg)),
        parseColour(resolveToken(theme, '--color-bg')),
      )
      if (value > 2) offenders.push(`${rule.selector}: ${bg} is ${value.toFixed(2)}:1 on the ground`)
    }
    expect(offenders).toEqual([])
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
