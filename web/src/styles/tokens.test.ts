import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { describe, expect, it } from 'vitest'

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

describe('tokens.css — light ground (ui-spec §1.2)', () => {
  it('declares the role tokens verbatim', () => {
    expect(light.get('--color-bg')).toBe('#f3f2f2')
    expect(light.get('--color-surface')).toBe('#eae9e9')
    expect(light.get('--color-text')).toBe('#201e1d')
    expect(light.get('--color-accent')).toBe('#ec3013')
    expect(light.get('--color-accent-2')).toBe('#e15b47')
    expect(light.get('--color-divider')).toBe('rgb(32 30 29 / 0.4)')
  })

  it('sets radius to zero on every step — the DS has no curves', () => {
    expect(light.get('--radius-sm')).toBe('0px')
    expect(light.get('--radius-md')).toBe('0px')
    expect(light.get('--radius-lg')).toBe('0px')
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
})

describe('tokens.css — dark ramp (ui-spec §1.4, NFR-CMP-003)', () => {
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

  it('swaps bg and text, and takes structure from the top of the neutral ramp', () => {
    expect(dark.get('--color-bg')).toBe('#201e1d')
    expect(dark.get('--color-text')).toBe('#f3f2f2')
    expect(dark.get('--color-surface')).toBe('#2d2b2b')
    expect(dark.get('--color-divider')).toBe('#444141')
    expect(dark.get('--rule')).toBe('#444141')
    expect(dark.get('--control-border')).toBe('#605d5d')
  })

  it('keeps the accent constant and moves hover/press up the ramp', () => {
    expect(dark.get('--color-accent')).toBe(light.get('--color-accent'))
    expect(dark.get('--accent-hover')).toBe('#ff563c') // accent-500
    expect(dark.get('--accent-press')).toBe('#ff9783') // accent-400
    expect(dark.get('--accent-text')).toBe('#ff9783')
    // 8 % accent is invisible on a dark ground.
    expect(dark.get('--nav-active')).toBe('rgb(236 48 19 / 0.14)')
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
      '--fill-subtle',
      '--fill-track',
      '--fill-track-2',
      '--hover-tint',
      '--press-tint',
      '--row-hover',
      '--nav-active',
      '--scrim-cover',
      '--scrim-modal',
      '--accent-hover',
      '--accent-press',
      '--accent-text',
    ]
    for (const token of semantic) {
      expect(light.has(token), `${token} missing from the light block`).toBe(true)
      expect(dark.has(token), `${token} does not flip in the dark block`).toBe(true)
    }
  })

  it('paints elevation as a hairline edge plus ambient darkness', () => {
    expect(dark.get('--shadow-lg')).toContain('#444141')
    expect(dark.get('--shadow-lg')).toContain('rgb(0 0 0 / 0.6)')
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
    // ui-spec §0.3: labels are flush left even inside full-width buttons.
    const btnBlock = findRule(allRules(BASE), '.btn-block')
    expect(btnBlock?.body).toMatch(/justify-content:\s*flex-start/)
  })

  it('themes focus rather than leaving browser defaults', () => {
    const base = allRules(BASE)
    const focusVisible = base.find((r) => r.selector === ':focus-visible')
    expect(focusVisible?.body).toMatch(/outline:\s*2px solid var\(--color-accent\)/)
    expect(focusVisible?.body).toMatch(/outline-offset:\s*2px/)
    const focus = base.find((r) => r.selector === ':focus')
    expect(focus?.body).toMatch(/outline:\s*none/)
  })

  it('keeps the two circles and nothing else round', () => {
    const round = allRules(BASE)
      .filter((r) => !r.body.includes('{'))
      .filter((r) => {
        const values = [...r.body.matchAll(/border-radius:\s*([^;}]+)/g)].map((m) =>
          (m[1] ?? '').trim(),
        )
        return values.some((v) => v !== '0' && v !== '0px')
      })
      .map((r) => r.selector)
    expect(round.sort()).toEqual(['.radio .dot', '.spinner'])
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
    // A range input with no height collapses onto its 2px track. The viewer's
    // slider used to carry 44px inline at *every* width, which held this
    // minimum but made the bottom bar 12px taller than the design on a desktop.
    expect(findRule(allRules(BASE), "input[type='range']")?.body).toMatch(/height:\s*24px/)
    expect(mobileRule("input[type='range']")).toMatch(/height:\s*var\(--touch-min\)/)
    expect(mobileRule("input[type='range']::-webkit-slider-thumb")).toMatch(/height:\s*28px/)
  })

  it('covers every control the shell puts on a phone', () => {
    // The scan row is a real control (it opens the scan log, ui-spec §4.1) but
    // it is a 7px dot plus one 12px line, so it needs the rule stated.
    for (const selector of ['.btn-icon', '.seg-opt', '.input', '.scan-indicator']) {
      expect(mobileRule(selector)).toMatch(/(min-)?height:\s*var\(--touch-min\)/)
    }
    expect(findRule(allRules(BASE), '.sidebar-nav-row')?.body).toMatch(
      /min-height:\s*var\(--touch-min\)/,
    )
  })
})
