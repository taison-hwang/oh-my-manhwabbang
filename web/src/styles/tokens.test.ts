import { readFileSync, readdirSync } from 'node:fs'
import { join, resolve } from 'node:path'

import { describe, expect, it } from 'vitest'

import tailwindConfig from '../../tailwind.config'
import {
  allRules,
  customProperties,
  findRule,
  stripComments,
  topLevelRules,
  type CssRule,
} from './cssRules'
import { contrast, luminance, over, parseColour, type Rgba } from './contrast'

// `import.meta.url` is an http URL under the jsdom environment, so the source
// is located from the vitest root (web/) instead.
const read = (rel: string): string => readFileSync(resolve(process.cwd(), rel), 'utf8')

const TOKENS = read('src/styles/tokens.css')
const BASE = read('src/styles/base.css')
const FONTS = read('src/styles/fonts.css')

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
// WCAG 2.1 relative luminance.
//
// The arithmetic used to be inline here, under a comment saying the token layer
// may not grow a module. That reasoning was about the *product* — `tokens.css`
// is the authority on colour and a runtime companion would give it a rival —
// and it stopped applying the moment a second tier had to measure. `web/e2e/
// contrast.ts` reads rendered pixels (items `v` and `ar`), and two tiers each
// holding their own copy of a contrast function is a way for them to disagree
// quietly: this one would go on calling the ⌘K chip 5.65 while the browser
// rendered 4.55, and nothing would say which number was the formula and which
// was the paint. One module, imported by both, leaves them able to disagree
// only about inputs.
//
// The calibration test stays here. `reproduces the four ratios E-32 measured`
// below is what makes the shared module trustworthy, and it belongs next to the
// ruling it reproduces rather than next to the code it checks.
// ---------------------------------------------------------------------------

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

/** The selectors of a rule's list, one per entry, whitespace normalised. */
function selectorList(rule: CssRule): string[] {
  return rule.selector.split(',').map((s) => s.trim().replace(/\s+/g, ' '))
}

/**
 * The at-rule preludes a rule is nested inside, outermost first, each reduced to
 * its own last line.
 *
 * `cssRules` slices a prelude from wherever the previous block ended, so the
 * outermost one in `base.css` arrives with the sheet's three `@tailwind`
 * statements glued to the front of it. The prelude proper is the last line.
 *
 * Every rule in `base.css` is inside one cascade layer, so `['@layer base']` —
 * and not the empty list — is what "nested in nothing conditional" looks like
 * here.
 */
function preludes(rule: CssRule | undefined): string[] {
  return (rule?.context ?? []).map((c) => (c.split('\n').pop() ?? '').trim())
}

/**
 * The opening JSX tag containing `at`, comments blanked.
 *
 * Reads from the `<` before `at` to the `>` that closes it at brace depth zero,
 * skipping quoted runs, then blanks block and line comments. base.css taught
 * that lesson and so did `ds.test.tsx`: prose in this repo quotes class names in
 * backticks, and `ViewerBottomBar`'s comment names `border-neutral-700` four
 * lines above the code that no longer has it.
 *
 * At module scope because two blocks ask the same question of a class list —
 * the grain exemption below and the cream-control scan further down.
 */
function openingTag(source: string, at: number): string {
  const start = source.lastIndexOf('<', at)
  let depth = 0
  let quote: string | null = null
  let end = start
  for (; end < source.length; end++) {
    const ch = source[end]
    if (quote !== null) {
      if (ch === quote) quote = null
      continue
    }
    if (ch === '"' || ch === "'" || ch === '`') quote = ch
    else if (ch === '{') depth += 1
    else if (ch === '}') depth -= 1
    else if (ch === '>' && depth === 0) break
  }
  return source
    .slice(start, end + 1)
    .replace(/\/\*[\s\S]*?\*\//g, ' ')
    .replace(/\/\/[^\n]*/g, ' ')
}

/**
 * Tailwind `z-*` utilities in a class list — variants and negation included, so
 * `md:z-10`, `-z-10` and `z-[3]` all count.
 *
 * A `z-` utility is the one thing that can undo a stacking rule in `base.css`
 * without touching the sheet: base.css is `@layer base`, Tailwind emits
 * utilities after it, and later layers win regardless of specificity.
 */
function zUtilities(text: string): string[] {
  return [...text.matchAll(/(?:^|[\s"'`])(-?(?:[\w[\]&_.+:-]*:)?z-[\w[\].%/-]+)/g)].map(
    (m) => m[1] ?? '',
  )
}

/**
 * One `z-index` value from `base.css` as a number, or `null` if this file cannot
 * read the notation.
 *
 * **Three notations reach the same rung, and a scanner that reads one of them
 * decides by spelling rather than by value.** The ceiling check below used to
 * match `/z-index:\s*(\d+)\s*;/` — bare integers only — in a sheet that has not
 * contained a bare integer since the ladder was tokenised. It therefore scanned
 * *nothing*, and E-43's `calc(var(--z-texture) + 1)` became the first rule in the
 * sheet to outrank the texture without the check noticing. Writing the identical
 * value as the literal `91` turned it red. A gate whose verdict depends on how a
 * number is spelled is not measuring the ladder.
 *
 * `null` is returned rather than skipped, and the caller reports it: an
 * unreadable notation is the failure this function exists to stop, so it has to
 * be louder than silence.
 */
function zValue(raw: string): number | null {
  const value = raw.trim()
  if (/^-?\d+$/.test(value)) return Number(value)
  const rung = (name: string): number | null => {
    const declared = light.get(name)
    return declared === undefined || !/^-?\d+$/.test(declared.trim()) ? null : Number(declared)
  }
  const token = /^var\((--z-[\w-]+)\)$/.exec(value)
  if (token !== null) return rung(token[1] ?? '')
  const shifted = /^calc\(\s*var\((--z-[\w-]+)\)\s*([+-])\s*(\d+)\s*\)$/.exec(value)
  if (shifted !== null) {
    const base = rung(shifted[1] ?? '')
    return base === null ? null : base + (shifted[2] === '-' ? -1 : 1) * Number(shifted[3])
  }
  return null
}

// ---------------------------------------------------------------------------
// The one element E-43 lifts out of the paper wash
//
// `--on-hot` is pure black — the ceiling of this palette — so the pair it makes
// with `--color-hot` cannot be repaired by moving a token, and E-43 exempts the
// marker from the wash instead. These three names are the whole surface of that
// exemption: the rule, the file that carries the only element it applies to, and
// the grain layer it has to outrank. Two separate blocks below check it, so they
// are stated once here rather than twice.
// ---------------------------------------------------------------------------

/** The base.css rule that does the lifting. */
const GRAIN_EXEMPT_SELECTOR = "[data-role='viewer-override-chip']"

/** The only file allowed to paint the exempt pair — see the scanner note. */
const GRAIN_EXEMPT_FILE = join('features', 'viewer', 'ViewerTopBar.tsx')

/** The grain layer the lift is measured against: the chip's own bar. */
const GRAIN_EXEMPT_BAR = "[data-role='viewer-top-bar']::after"

/**
 * The same role as the attribute a `.tsx` writes, derived from the selector so
 * the two cannot be renamed apart: a rename in `base.css` alone leaves the rule
 * matching nothing, and this is what makes that loud rather than silent.
 */
const GRAIN_EXEMPT_ATTR = GRAIN_EXEMPT_SELECTOR.slice(1, -1).replace(/'/g, '"')

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
    // E-46, `만화방 v3 서고.dc.html`: document beige, warm near-black ink, 인주.
    expect(light.get('--color-bg')).toBe('#DED5C4')
    expect(light.get('--color-surface')).toBe('#EFE9DC')
    expect(light.get('--color-text')).toBe('#221E1A')
    expect(light.get('--color-accent')).toBe('#A2382A')
    expect(light.get('--color-divider')).toBe('#B7A78B')
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

  it('joins --color-hot to the accent — E-46 reverses E-32 §1 on purpose', () => {
    // E-32 §1 held these apart and this test asserted the separation: its
    // accent was a deep teal and its marker the retired brand red #EC3013, so
    // the two being equal could only mean the red had crept back into use as a
    // brand colour.
    //
    // E-46 has one red and spends it on both jobs. That is a reversal of a
    // BINDING ruling, so it is asserted rather than merely allowed — the
    // identity is now the thing that would have to break for the change to be
    // undone by accident, which is the same protection pointing the other way.
    expect(light.get('--color-hot')).toBe('#A2382A')
    expect(light.get('--color-hot')).toBe(light.get('--color-accent'))
    // Still a marker rather than a palette member, so it does not flip.
    expect(dark.has('--color-hot')).toBe(false)
  })

  it('carries the E-46 radius scale — sm 2 / md 2 / lg 3 / pill 2 / full 999', () => {
    // Letterpress geometry: the 서고 prototype writes 2px on everything and 3px
    // on the one step above it, which is most of the way back to D-40's zero
    // rule that E-32 retired. `--radius-pill` keeps its name and stops being a
    // pill — the button is square now, and renaming the token at every call
    // site would be a larger diff than the design change it describes.
    expect(light.get('--radius-sm')).toBe('2px')
    expect(light.get('--radius-md')).toBe('2px')
    expect(light.get('--radius-lg')).toBe('3px')
    expect(light.get('--radius-pill')).toBe('2px')
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

  it('names 고운바탕 first, then the 한자 fallback (E-7 as amended by E-46, E-55)', () => {
    // The order changed *and* so did what it means. Under E-7 the vendored face
    // was latin-only, so everything after Archivo was the stack that actually
    // drew the Korean and the order mattered enormously. 고운바탕 is vendored
    // with all 11 172 modern Hangul syllables, so the fallbacks now only ever
    // draw 한자 and 가나 — which is why 본명조 comes second and there is no
    // sans anywhere in the stack.
    //
    // E-55 names the regional cut. `Noto Serif TC` and the `Noto Serif KR`
    // behind it are *not* interchangeable: 본명조 draws the same codepoint
    // differently per region, so a stack that lost the TC entry would keep
    // rendering 한자 — in the wrong forms, silently. The order below is what
    // pins that.
    const stack = light.get('--font-heading') ?? ''
    const order = ['Gowun Batang', 'Noto Serif TC', 'Noto Serif KR', 'Apple SD Gothic Neo', 'serif']
    let cursor = -1
    for (const face of order) {
      const at = stack.indexOf(face, cursor + 1)
      expect(at, `${face} missing or out of order in ${stack}`).toBeGreaterThan(cursor)
      cursor = at
    }
    // 명조 is the design. A sans in the heading stack is the skin coming undone.
    expect(stack).not.toMatch(/sans-serif|system-ui/)
    // The two faces the skin keeps apart, and the one place a sans is allowed.
    expect(light.get('--font-body')).toBe('var(--font-heading)')
    expect(light.get('--font-ui') ?? '').toMatch(/sans-serif/)
    expect(light.get('--font-seal') ?? '').toContain('Gowun Batang')
    // 고운바탕 has 400 and 700 and no more; 800 would be synthesised and 명조
    // loses its strokes when it is.
    expect(light.get('--font-heading-weight')).toBe('700')
  })

  /**
   * A hand-picked glyph subset may not sit in a stack that draws *text*
   * (E-55).
   *
   * The 낙관 cuts — 藏 for the wordmark, 完讀 for the finished-series stamp —
   * were declared as `Gowun Batang` and fenced off with `unicode-range`, so
   * that they joined the text stack rather than competing with it. That is
   * safe only while nothing behind them can draw those characters. It stopped
   * being safe the moment a full 한자 face was vendored: a family at the head
   * of `--font-heading` gets first refusal on every character it claims, so a
   * two-glyph subset was answering for 한자 that *titles* contain — `完` is the
   * 완독 marker here and appears in 204 of the 727 names carrying 한자, drawn
   * from a KR cut while every other ideograph beside it came from TC, and only
   * at weight 700 because the seal has no 400.
   *
   * Stated as the rule rather than as the two families that broke it: a face
   * restricted to *individual codepoints* is a mark, not a typeface, and the
   * stacks that set prose must not be able to reach it.
   */
  it('keeps hand-picked glyph subsets out of every text stack (E-55)', () => {
    const faces = [...FONTS.matchAll(/@font-face\s*\{([^}]*)\}/g)].map((m) => m[1] ?? '')
    const handPicked = new Set<string>()
    for (const face of faces) {
      const family = /font-family:\s*'([^']+)'/.exec(face)?.[1]
      const range = /unicode-range:\s*([^;]+);/.exec(face)?.[1]
      if (family === undefined || range === undefined) continue
      // `U+3040-309F` claims a block; a bare `U+85CF` picks one character out.
      if (range.split(',').some((entry) => !entry.includes('-'))) handPicked.add(family)
    }
    expect(handPicked.size, 'no hand-picked subset found — has the seal moved?').toBeGreaterThan(0)

    const textStacks = ['heading', 'body', 'ui', 'ja'] as const
    const offenders: string[] = []
    for (const token of textStacks) {
      const stack = light.get(`--font-${token}`) ?? ''
      for (const family of handPicked) {
        if (stack.includes(family)) offenders.push(`--font-${token} can reach ${family}`)
      }
    }
    expect(offenders).toEqual([])

    // And the marks still have a stack that *does* reach them, or they render
    // as tofu — the failure this whole arrangement exists to prevent.
    const seal = light.get('--font-seal') ?? ''
    expect([...handPicked].filter((f) => !seal.includes(f))).toEqual([])
  })

  /**
   * E-55: Japanese names get a whole face, not a kana patch.
   *
   * A Japanese title's kanji and a Korean title's 한자 are the same
   * codepoints, so `unicode-range` cannot separate them and 「進撃の巨人」
   * splits into 명조 kanji around a 고딕 の. The fix is a second stack reached
   * by `[lang='ja']`, and each half of it can fail on its own — a stack with
   * no rule pointing at it is dead, and a rule pointing at a stack whose face
   * this repo does not ship falls through to the system. Both halves asserted.
   */
  it('hands a ja-tagged name to a vendored Japanese face (E-55)', () => {
    const ja = light.get('--font-ja') ?? ''
    const order = ['Gowun Batang', 'Noto Sans JP', 'sans-serif']
    let cursor = -1
    for (const face of order) {
      const at = ja.indexOf(face, cursor + 1)
      expect(at, `${face} missing or out of order in ${ja}`).toBeGreaterThan(cursor)
      cursor = at
    }

    // 고운바탕 leads this stack too, and that is not symmetry for its own sake.
    // A kana-bearing name on this library is usually a *Korean* title with a
    // Japanese fragment in it, so the tag lands on a mostly-Hangul string; if
    // 본고딕 led, one parenthesis would turn a Korean title 고딕.
    expect((ja.split(',')[0] ?? '').trim().replace(/^'|'$/g, '')).toBe('Gowun Batang')

    // The face has to be one this repo ships, and it has to claim the CJK
    // blocks without claiming Hangul — the same reason as above, stated where
    // the browser actually reads it.
    expect(FONTS).toMatch(/font-family:\s*'Noto Sans JP'/)
    const jaFaces = FONTS.split('@font-face').filter((b) => b.includes("'Noto Sans JP'"))
    expect(jaFaces).toHaveLength(2)
    for (const face of jaFaces) {
      expect(face).toMatch(/unicode-range:[^;]*U\+3040-309F/)
      expect(face).not.toMatch(/unicode-range:[^;]*U\+AC00/)
    }

    // And the rule that reaches the stack. `[lang='ja']`, never `:lang(ja)`:
    // `:lang()` matches descendants, so a page counter beside a Japanese title
    // would change face because of its neighbour.
    // Read past the comments: the paragraph above the rule *names* `:lang(ja)`
    // to say why it is not used, and a check that cannot tell a declaration
    // from the prose explaining it would fail on its own documentation.
    const declarations = stripComments(BASE)
    expect(declarations).toMatch(/\[lang='ja'\]\s*\{\s*font-family:\s*var\(--font-ja\);/)
    expect(declarations).not.toMatch(/:lang\(/)
  })

  it('drops one flat ink shadow, and keeps the wells soft (E-46)', () => {
    // soft-UI's dual light is gone from the three *outsets*: a sheet lying on a
    // desk casts one shadow, not a shadow and a highlight. The 서고 prototype
    // redefines exactly those three.
    for (const step of ['--shadow-sm', '--shadow-md', '--shadow-lg']) {
      const value = light.get(step) ?? ''
      expect(value, `${step} is not a single ink drop`).toMatch(
        /^0 \d+px \d+px rgb\(34 30 26 \/ 0\.\d+\)$/,
      )
      expect(value, `${step} kept a soft-UI highlight lobe`).not.toContain('rgb(255 253 246 /')
    }
    // The *inset* is untouched, and that is the prototype's own doing rather
    // than an oversight here: it overrides --shadow-sm/md/lg and leaves
    // --shadow-inset alone, so `.input` and the `.seg` track are still soft-UI
    // wells inside a flat skin. Recording it as an assertion is what stops the
    // next reader "finishing the job" and flattening a recess the design keeps.
    const inset = light.get('--shadow-inset') ?? ''
    expect(inset).toContain('inset')
    expect(inset).toContain('rgb(150 128 96 /')
    expect(inset).toContain('rgb(255 253 246 /')
    expect(light.get('--shadow-control-inset')).toBe(inset)
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
    expect(light.get('--scrim-cover-base')).toBe('rgb(34 30 26 / 0.92)')
    expect(light.get('--scrim-cover-mid')).toBe('rgb(34 30 26 / 0.55)')
    expect(light.get('--scrim-cover-top')).toBe('rgb(34 30 26 / 0.15)')
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
    // The swap E-32 made, carried onto the 서고 palette: the ground is the
    // light theme's ink and the ink is the light theme's ground.
    expect(dark.get('--color-bg')).toBe('#221E1A')
    expect(dark.get('--color-text')).toBe('#DED5C4')
    expect(dark.get('--color-bg')).toBe(light.get('--color-text'))
    expect(dark.get('--color-text')).toBe(light.get('--color-bg'))
    expect(dark.get('--color-surface')).toBe('#302A24')
    expect(dark.get('--color-divider')).toBe('#4A4139')
    expect(dark.get('--rule')).toBe('#4A4139')
    expect(dark.get('--control-border')).toBe('#6E6355')
  })

  it('swaps the ground and the ink of the light theme', () => {
    expect(dark.get('--color-bg')).toBe(light.get('--color-text'))
    expect(resolveToken(dark, '--ink')).toBe(light.get('--color-bg'))
  })

  it('keeps the accent constant and moves hover/press up the ramp', () => {
    expect(dark.get('--color-accent')).toBe(light.get('--color-accent'))
    expect(resolveToken(dark, '--color-accent-2')).toBe(resolveToken(light, '--color-accent-2'))
    expect(dark.get('--accent-text')).toBe('#D3A79D') // accent-300
    // Lighter than the base, unlike the light theme where hover/press go down.
    const lum = (theme: Map<string, string>, token: string): number =>
      luminance(parseColour(resolveToken(theme, token)))
    expect(lum(dark, '--accent-hover')).toBeGreaterThan(lum(dark, '--color-accent'))
    expect(lum(dark, '--accent-press')).toBeGreaterThan(lum(dark, '--accent-hover'))
    expect(lum(light, '--accent-hover')).toBeLessThan(lum(light, '--color-accent'))
    expect(lum(light, '--accent-press')).toBeLessThan(lum(light, '--accent-hover'))
  })

  it('washes the active row with accent-300, not the accent (E-32 §3.2)', () => {
    // A dark red on a near-black ground is not a tint, the same way a teal on
    // teal was not one. The repair is the same and so is the ramp step.
    expect(dark.get('--nav-active')).toBe('rgb(211 167 157 / 0.16)')
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

  /**
   * E-42's nine, and the reason they are enumerated *here*.
   *
   * E-36 §3.7 asks that a newly derived semantic token exist in both blocks.
   * These nine are the stated exception, and the exception is load-bearing:
   * "absolute" means **there is no dark counterpart**, so the moment a dark
   * block re-declares `--control-fill`, a control in the viewer is a deep-teal
   * pill on a deep-teal bar — the exact defect E-36 §4 was written to stop —
   * and every other check in this file stays green while it happens.
   *
   * Until this list existed the array below enumerated only the older
   * absolutes, so `dark.has(token) === false` was asserted for `--on-accent` and
   * `--on-hot` and for nothing E-42 added. `soft-ui.test.ts` holds the other
   * half of the same contract: that each frozen value is *the light theme's
   * value, unchanged*, which is what makes freezing it honest rather than
   * arbitrary.
   */
  const CONTROL_ABSOLUTES = [
    '--control-fill',
    '--control-fill-hover',
    '--control-well',
    '--on-control',
    '--on-control-accent',
    '--on-control-dim',
    '--shadow-control-inset',
    '--shadow-control-raised',
    '--shadow-accent-inset',
  ]

  it('leaves the absolutes alone — they paint on a ground that never flips', () => {
    // The viewer ground is #221E1A in both app themes, and the accent and the
    // seal are theme-invariant, so their foregrounds are too. Flipping any of
    // these would repaint the viewer's scrims when the app theme changed.
    for (const token of [
      '--scrim-volume-end',
      '--scrim-broken',
      '--on-accent',
      '--on-hot',
      // The grain's viewer tone belongs to the same family: the reading screen
      // is dark in both app themes, so a tone that flipped with the app would
      // repaint the viewer's texture when the library's theme changed.
      '--paper-tone-viewer',
      // E-42: a cream control is the same cream in all three scopes — app light,
      // app dark, viewer — so its fills and the three inks that sit on them join
      // the family, as do the three shadows, which follow the surface they fall
      // on rather than the theme.
      ...CONTROL_ABSOLUTES,
    ]) {
      expect(light.has(token), `${token} missing from the light block`).toBe(true)
      expect(dark.has(token), `${token} must not flip with the theme`).toBe(false)
    }
    expect(light.get('--scrim-volume-end')).toBe('rgb(34 30 26 / 0.92)')
    expect(light.get('--scrim-broken')).toBe('rgb(62 20 14 / 0.82)') // accent-900 @ 82 %
  })

  it('counts nine control absolutes — not eight, and not a family that grew', () => {
    // The list above can only fail on a token it *names*. This is the assertion
    // that fires when a tenth cream token arrives with no ruling behind it, or
    // when one of the nine is renamed and silently drops out of the check above:
    // it reads the block rather than the list.
    const declared = [...light.keys()].filter(
      (k) => /^--(control|on-control)/.test(k) || /^--shadow-(control|accent)-/.test(k),
    )
    // `--control-border` and `--control-border-hover` are deliberately *not* in
    // this family. They are the retired bordered-box tokens, they still flip,
    // and the radio dot's ring is the one consumer E-36 §3.3 leaves them.
    expect(declared.sort()).toEqual(
      [...CONTROL_ABSOLUTES, '--control-border', '--control-border-hover'].sort(),
    )
    expect(dark.has('--control-border')).toBe(true)
  })

  it('re-derives the accent tints off the new ramp, not a retired one', () => {
    // Two retired accents to check for now, and the reason is the same both
    // times: a tint whose channels belong to a palette the product has left is
    // what "we forgot this one" looks like. `236 48 19` is E-32's brand red,
    // `23 89 91` its teal.
    for (const token of ['--nav-active', '--ghost-hover', '--ghost-press', '--selection-bg']) {
      for (const [name, theme] of themes) {
        for (const retired of ['236 48 19', '23 89 91', '155 195 193']) {
          expect(
            theme.get(token),
            `${token} still carries ${retired} in ${name}`,
          ).not.toContain(retired)
        }
      }
    }
    expect(light.get('--ghost-hover')).toContain('162 56 42') // the accent
    expect(dark.get('--ghost-hover')).toContain('211 167 157') // accent-300
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
    expect(dark.get('--shadow-lg')).toContain('#4A4139')
    expect(dark.get('--shadow-lg')).toContain('rgb(0 0 0 / 0.6)')
    expect(dark.get('--shadow-inset')).toContain('inset')
    // The sidebar's dark form keeps the hairline the other three use, but only
    // on the side that shows: `1px 0 0` is the right edge of a full-height
    // panel, where `0 0 0 1px` would ring three edges that are off-screen. The
    // ochre lobe is gone for the reason the whole dark block exists — it is a
    // light-ground device.
    expect(dark.get('--shadow-sidebar')).toContain('1px 0 0 #4A4139')
    expect(dark.get('--shadow-sidebar')).not.toContain('150 128 96')
  })

  it('turns the grain to the ramp\u2019s dark end on an ink ground', () => {
    // Worth <1/255 at today's intensity — a dark ground is already most of the
    // way to a near-black tone — and declared anyway, because the tone is the
    // one part of the texture that is paint. The failure it forecloses is the
    // one this whole block is about: a value that is right in one theme because
    // nobody ever asked what it does in the other.
    // `DARK_CASCADE`, not `dark`: the dark block does not re-declare the ramps,
    // so the step this points at is reached by inheritance — the same hole the
    // scanner further down exists to close.
    expect(resolveToken(light, '--paper-tone')).toBe('#221E1A') // neutral-900
    expect(resolveToken(DARK_CASCADE, '--paper-tone')).toBe('#3E140E') // accent-900
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

  /** The tokens the grain took under AA, and the pairs they were taken on. */
  const REPAIRED: [string, Map<string, string>, string, string][] = [
    ['light meta text on the ground', light, '--ink-faint', '--color-bg'],
    ['dark card meta on the surface', dark, '--ink-meta', '--color-surface'],
    ['dark faint ink on the surface', dark, '--ink-faint', '--color-surface'],
    ['dark table headers on the surface', dark, '--ink-th', '--color-surface'],
  ]

  it('keeps the marker above the wash, now for its edges and not its legibility', () => {
    // **This test used to assert the opposite and it was right to.** Under
    // E-43's palette `--on-hot` was pure black on #EC3013, the pair washed to
    // 4.46, and the exemption in base.css was the only reason the marker
    // cleared AA at all. The comment here said, in as many words: if the wash
    // ever stops taking this pair under AA, the exemption has stopped being
    // necessary and somebody should delete it.
    //
    // E-46 is that moment. #A2382A takes a cream ink at **5.62 washed**, so the
    // pair clears AA with the grain fully on it and the lift is no longer
    // load-bearing for legibility. The first assertion below is therefore
    // reversed: it now pins the headroom rather than the failure.
    expect(washedRatio(light, '--on-hot', '--color-hot')).toBeGreaterThanOrEqual(4.5)
    expect(ratio(light, '--on-hot', '--color-hot')).toBeGreaterThanOrEqual(4.5)

    // The rule stays, and the reason it stays is now a visual one rather than a
    // contrast one: the marker is a seal, and a seal is pressed ink that sits
    // *on* the paper rather than under its grain. Everything below still holds
    // it in place, because a rule kept for a softer reason is exactly the kind
    // that gets deleted by someone who only reads the first paragraph.

    // 3. The rule exists, **unconditionally and at exactly this selector**.
    //
    //    Both halves of that sentence are load-bearing, and the first cut of this
    //    test had neither. It looked the rule up with `.includes`, so narrowing
    //    the selector to `.nope [data-role='viewer-override-chip']` — which
    //    matches no element in the product — still found it; `exactRule` is the
    //    same fix the corner assertions further down needed. And it never read
    //    `context`, which `allRules` fills with the enclosing at-rule preludes:
    //    wrapping the rule in `@media (prefers-contrast: more)` left this green
    //    while removing the exemption from every screen that has the grain on
    //    (a perfect inversion — that query is where the grain is switched *off*),
    //    and `@media print` left it green while the chip washed on screen.
    //
    //    The whole sheet is inside `@layer base`, so that one prelude is the
    //    context a top-level rule is allowed to have, and asserting the list
    //    exactly is what refuses every other at-rule — media, supports, or a
    //    nested layer that a later `@layer` statement could reorder.
    const rule = exactRule(GRAIN_EXEMPT_SELECTOR)
    expect(rule, `${GRAIN_EXEMPT_SELECTOR} must be lifted above its bar's grain`).toBeDefined()
    expect(
      preludes(rule),
      `${GRAIN_EXEMPT_SELECTOR} must not be inside an at-rule`,
    ).toEqual(['@layer base'])
    expect(rule?.body).toMatch(/position:\s*relative/)
    expect(rule?.body).toMatch(/z-index:\s*calc\(var\(--z-texture\)\s*\+\s*1\)/)

    // 4. The layer it is lifted *over* is still where it was. "One step above
    //    `--z-texture`" only clears the grain while the grain is at
    //    `--z-texture`: raising the bar's own `::after` to
    //    `calc(var(--z-texture) + 5)` puts the chip back under the wash and left
    //    every assertion above green, because none of them read the bar. The two
    //    z values are compared as numbers so the pair cannot be re-notated apart,
    //    and the textual forms are pinned as well so neither side drifts to a
    //    bare integer that no longer tracks the ladder.
    const grain = allRules(BASE).find(
      (r) => selectorList(r).includes(GRAIN_EXEMPT_BAR) && preludes(r).join('|') === '@layer base',
    )
    expect(grain, `${GRAIN_EXEMPT_BAR} must carry the bar's grain`).toBeDefined()
    expect(grain?.body).toMatch(/z-index:\s*var\(--z-texture\)\s*;/)
    const barZ = zValue(/z-index:\s*([^;]+);/.exec(grain?.body ?? '')?.[1] ?? '')
    const chipZ = zValue(/z-index:\s*([^;]+);/.exec(rule?.body ?? '')?.[1] ?? '')
    expect(barZ).toBe(Number(light.get('--z-texture')))
    expect(chipZ, 'the chip must sit exactly one step above the grain on its bar').toBe(
      (barZ ?? 0) + 1,
    )

    // 5. And no `z-` utility on the chip itself. `base.css` is `@layer base` and
    //    Tailwind emits utilities after it, so later-layer wins: a single `z-10`
    //    in the class list beats this rule outright and the lift is gone, with
    //    the whole stylesheet unchanged and everything above still green. Same
    //    technique as `a cream control carries no colour utility` below — jsdom
    //    computes no styles here, but the class list itself is real.
    const chipSource = read(join('src', GRAIN_EXEMPT_FILE))
    const at = chipSource.indexOf(GRAIN_EXEMPT_ATTR)
    expect(at, `${GRAIN_EXEMPT_FILE} no longer writes ${GRAIN_EXEMPT_ATTR}`).toBeGreaterThan(-1)
    const tag = openingTag(chipSource, at)
    // The positive control for the tag reader: an `openingTag` that stopped at
    // the first `>` inside a `{…}` expression would hand back a fragment with no
    // class list at all, and the assertion after it would pass on nothing.
    expect(tag, 'the override chip carries no className to read').toContain('className=')
    expect(zUtilities(tag), 'a z- utility on the chip outranks the base rule').toEqual([])

    // 6. And the element the rule lifts is the element that paints the pair.
    //    The scanner below exempts `bg-hot text-on-hot` **by file**, which is
    //    right for the reason written there — the lift does not travel to another
    //    screen — but a file is coarser than an element. This is the other end of
    //    that key: the lifted tag has to be the one carrying both utilities, so a
    //    second hot badge elsewhere *in this file* cannot inherit the exemption
    //    from its neighbour. (`found the fills the screens are known to paint`
    //    holds the matching half: exactly one such pair exists in the repo.)
    expect(tag, 'the lifted element is not the one painting bg-hot').toMatch(/(^|\s)bg-hot(?![\w-])/)
    expect(tag, 'the lifted element is not the one painting text-on-hot').toMatch(
      /(^|\s)text-on-hot(?![\w-])/,
    )
  })

  it('re-derives the pairs the grain took under AA', () => {
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
    // `--color-hot` by **+2.0 %** (measured: the washed pair reaches 4.533, and
    // +1 % is not enough at 4.498), and that is a change to the retired brand red
    // that E-32 §1 pinned: a ruling of its own, not a side effect of a texture.
    // The figure used to read "~4 %" here, which was the number at
    // `--paper-intensity: 0.5`; the argument is unchanged, the price is smaller.
    //
    // Today, at the shipped amplitude and `--paper-intensity: 1` (E-43):
    // light ink-faint 4.61 / 4.39, dark ink-meta 4.68 / 4.39, dark ink-faint
    // 4.71 / 4.43, dark ink-th 4.68 / 4.39 (mean / peak). **Every peak here is
    // under 4.5 and that is the price of the intensity the user chose** — the
    // means clear it and the means are the floor, for the three reasons above.
    // The override chip is no longer in this table: it does not take the wash.
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

  it('puts light ink on the hot marker — the dark one no longer clears AA', () => {
    // The exact inversion of the E-43 position, and worth stating as one. On
    // #EC3013 a light ink could not clear AA at all (#F6F2E9 was 3.76, pure
    // white 4.20) so the marker took pure black with 0.50 of dry headroom. On
    // #A2382A the palette's ceiling is at the other end: black is **3.14 dry**
    // and 2.88 washed, and the cream that used to fail is the one that works.
    const hot = parseColour(light.get('--color-hot') ?? '')
    expect(contrast(parseColour(light.get('--on-hot') ?? ''), hot)).toBeGreaterThanOrEqual(4.5)
    expect(contrast(parseColour('#000000'), hot)).toBeLessThan(4.5)
    // And it is the same cream that sits on an accent fill, because the marker
    // and the accent are one colour now.
    expect(light.get('--on-hot')).toBe(light.get('--on-accent'))
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
const FILL = /^(bg|surface|accent|accent-2|accent-hover|accent-press|accent-fill|ink|hot|control-fill|control-fill-hover|control-well)$|^(accent|accent-2|neutral)-\d00$/

// `bg` and `surface` are in that list, and used not to be. The argument for
// leaving them out was that `INK_TOKENS` already measures every ink against both
// grounds — which is true, and which is exactly why it was the wrong argument:
// what lands on those grounds is not always an ink token. `FormatBadge`'s corner
// pill painted `bg-surface text-accent-800`, a raw ramp step on a semantic
// ground, and the ramps do not flip: 11.62:1 on the cream surface, **1.40:1** on
// the dark one. The pair was skipped entirely, so the scanner was green about a
// badge that is a smudge in half the product.
//
// The three cream control fills joined for the same reason one round later, and
// it is the sharper case: E-42 made `--control-fill` / `--control-fill-hover` /
// `--control-well` **absolutes**, so a component painting `bg-control-fill
// text-on-control` is putting an ink on a ground that does not flip — the one
// configuration where "the theme will sort it out" is false by construction.
// Every such pair was skipped whole, i.e. the pairs the ruling had just created
// were the pairs outside the measurement. `TopBar`'s ⌘K hint chip
// (`bg-control-well … text-on-control`) is the one this widening picked up.
//
// **And the number this comment first carried for it was wrong, in the direction
// that matters.** It read "passes at 5.65 washed", which is `--on-control-dim` on
// `--control-well` *as the two tokens are declared*. The chip does not render
// that pair. It is 14.8px tall and carries `shadow-control-inset`, a recess
// sized for a 36px control: the two 3px/7px lobes reach ~10px in from each edge,
// so **no pixel of the chip is the declared `--control-well`** — every one of
// them is the shadow over it. Measured on the actual top-left pixel, the dim ink
// came out **4.55 washed / 4.44 peak**, i.e. under the AA floor for 11px text,
// while this scanner called it 5.65 and passed. The ink is `--on-control` now.
//
// ## The limitation that produced that false pass, stated
//
// **This scanner cannot see `box-shadow`.** It pairs a fill token with an ink
// token and measures the two declared colours. When a shadow — inset or
// otherwise — repaints the ground the ink actually sits on, the floor moves and
// nothing here knows. That is not a narrow case: every cream control in the
// product carries `--shadow-control-inset` or `--shadow-control-raised` by
// construction after E-42, so the *declared* fill is the true ground only where
// the element is large enough for the lobes not to meet in the middle. The
// smaller the box, the wronger this scan is — and small boxes are exactly where
// 11px text lives.
//
// A green pair here therefore means "the two declared tokens clear AA", not "the
// reader can read it". The second question needs a real render, which is the e2e
// tier; a reviewer measuring the top-left pixel is what caught it this time.
//
// What it also does **not** pick up is the failure E-42 §7 found: an ink utility
// on an element that is merely *over* a cream control — a sibling span
// absolutely positioned on top of an `.input`, or a `text-…` override on a
// `.btn-secondary` whose fill comes from base.css and not from the class list.
// Neither names a fill, so blind spot 2 in the header above swallows both. That
// is why E-42 §7 says to re-count by hand rather than trust a list.

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
    for (const token of [
      '--on-accent',
      '--on-hot',
      '--color-hot',
      '--color-accent-800',
      // E-42's cream set is the same shape and arrived later: declared once, in
      // the base block, absolute *by design*, and therefore invisible to `dark`.
      // The viewer paints all six inside `[data-theme='dark']` — it is the scope
      // the ruling was written for — so reading `dark` alone would skip exactly
      // the pairs the ruling created.
      '--control-fill',
      '--control-fill-hover',
      '--control-well',
      '--on-control',
      '--on-control-accent',
      '--on-control-dim',
    ]) {
      expect(dark.has(token), `${token} is declared in the dark block after all`).toBe(false)
      expect(opaque(DARK_CASCADE, token), `${token} unresolvable in a dark scope`).not.toBeNull()
    }
    // The three frozen shadows cascade the same way but are not colours, so
    // `opaque` cannot speak for them: they are checked for reachability only.
    for (const token of [
      '--shadow-control-inset',
      '--shadow-control-raised',
      '--shadow-accent-inset',
    ]) {
      expect(dark.has(token), `${token} is declared in the dark block after all`).toBe(false)
      expect(DARK_CASCADE.get(token), `${token} unresolvable in a dark scope`).toBeDefined()
    }
  })

  it('found the fills the screens are known to paint', () => {
    // The other half of the calibration, and the reason it names files: a regex
    // that quietly stops matching class lists would otherwise leave the
    // assertion below iterating over nothing. Both entries are pairs that must
    // *pass* — a scan that only found the failures would be fitted to them.
    const seen = PAINTED.map((p) => `${p.file} bg-${p.bg} text-${p.fg}`)
    // Was `SeriesCard.tsx bg-accent text-on-accent`, the 완독 pill. E-46 stamps
    // a 完讀 seal on the cover instead of labelling it, so the accent pill is
    // gone from that file and the mark it became is its own component painting
    // cream-and-accent-ink — the same pairing the format badge opposite it uses.
    // Moved rather than deleted, for the reason the paragraph above gives: an
    // entry that vanishes with the coverage it stood for leaves the scan fitted
    // to whatever is left.
    expect(seen).toContain(join('components', 'ds', 'DoneSeal.tsx') + ' bg-surface text-accent-text')
    // `--color-bg` on `--color-ink`: the two are inverses in both themes, so
    // this is the correct pairing and the scanner must not call it a defect.
    expect(seen).toContain(join('features', 'overlays', 'ShortcutsDialog.tsx') + ' bg-ink text-bg')
    // The two the semantic grounds bought. Neither was visible to this scan
    // before `bg`/`surface` joined `FILL`, and one of them was a real defect.
    expect(seen).toContain(join('components', 'ds', 'FormatBadge.tsx') + ' bg-surface text-accent-text')
    expect(seen).toContain(join('features', 'viewer', 'ViewerPage.tsx') + ' bg-bg text-neutral-400')
    // The one the cream fills bought (E-42), as E-46 leaves it. It was the ⌘K
    // hint chip painting `bg-control-well text-on-control`; the chip is an
    // outlined box now, so it paints no fill of its own and drops out of this
    // scan — its ground is the `.input` it sits on and `soft-ui.test.ts` holds
    // that. What is checked here instead is that the *other* absolute pairing
    // on the same screen is still found, so the entry did not simply vanish
    // along with the coverage it stood for.
    expect(seen).toContain(
      join('features', 'viewer', 'ViewerTopBar.tsx') + ' bg-hot text-on-hot',
    )
    // The pair the scan below exempts from the wash, and the file it is keyed to.
    // Two failure directions, both silent without this.
    //
    //  * The branch goes **dead**: a chip that stopped naming both utilities in
    //    one literal leaves the exemption unreachable, and an unreachable
    //    exemption reads exactly like a working one.
    //  * The branch goes **wide**: `base.css` lifts one element, so a second
    //    `bg-hot text-on-hot` anywhere — including elsewhere in this same file —
    //    is under a grain layer and reads 4.46. Keyed on the file, that second
    //    site inside `ViewerTopBar` would be exempted by its neighbour. Asserting
    //    the *whole repo* has exactly one such pair is what stops it, and the
    //    exemption test holds the other end: the lifted tag is the one painting
    //    it.
    expect(PAINTED.filter((p) => p.bg === 'hot' && p.fg === 'on-hot').map((p) => p.file)).toEqual([
      GRAIN_EXEMPT_FILE,
    ])
    expect(PAINTED.length).toBeGreaterThanOrEqual(7)
  })

  it('reads at AA on every solid fill it paints — under the paper grain', () => {
    // Washed, not dry, and for the same reason the ink floor above is: these
    // pairs are painted under the texture too. `bg-hot text-on-hot` is the
    // override chip, which is the pair the grain took furthest under AA, and a
    // dry scan here would have gone on calling it a pass.
    const offenders: string[] = []
    for (const pair of PAINTED) {
      // The override chip is painted above its bar's grain layer (E-43), so the
      // wash never reaches it and measuring it washed would report a defect the
      // reader cannot see. The exemption is *not* taken on trust: the test above
      // fails if `base.css` stops lifting it, and this scan still holds the pair
      // to AA dry — it is the only pair in this file allowed to be read dry.
      //
      // **And it is the only *element*, not the only colour pair.** `base.css`
      // lifts one selector, `[data-role='viewer-override-chip']`, which exists in
      // exactly one file. A second `bg-hot text-on-hot` badge anywhere else —
      // a library card, a series header — is under the global `body::after` and
      // reads **4.46**, while a pair-keyed exemption would certify it at 4.9988
      // and never mention it. Worse, the repair does not travel: the lift works
      // because the bar is a stacking context, so a step above `--z-texture`
      // lands above the bar's own grain; the global layer is a child of `body` in
      // the root stacking context, and nothing inside `#root` can be lifted over
      // it at all. So the exemption is keyed on the file as well, and a new site
      // shows up here as an offender — which is the correct answer for it.
      if (pair.bg === 'hot' && pair.fg === 'on-hot' && pair.file === GRAIN_EXEMPT_FILE) {
        const dry = contrast(
          opaque(light, COLOUR_UTILITIES.get('on-hot') ?? '') ?? { r: 0, g: 0, b: 0, a: 1 },
          opaque(light, COLOUR_UTILITIES.get('hot') ?? '') ?? { r: 0, g: 0, b: 0, a: 1 },
        )
        if (dry < 4.5) {
          offenders.push(
            `${pair.file}:${String(pair.line)} — text-on-hot on bg-hot is ${dry.toFixed(2)}:1 dry`,
          )
        }
        continue
      }
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

// ---------------------------------------------------------------------------
// Colour utilities on a cream control
//
// **The mechanism, in one sentence E-42 §7 wrote:** `base.css` lives in
// `@layer components` and Tailwind's utilities come after it, so a utility beats
// the component class **every time**. Changing a control's fill is therefore not
// a matter of editing that class — it is a matter of re-counting everything
// stacked on top of it.
//
// E-36 §5.3 listed five overrides to remove. There were seven, and three of the
// real ones were inks rather than borders: `text-accent-text` at 1.65 on cream,
// `text-ink` at 1.10, `bg-bg` overwriting the fill outright. Every one had been
// correct against the ground the control *used to* have.
//
// **Nothing stopped any of them coming back.** `soft-ui.test.ts` deliberately
// reads only `base.css`, and the scanner above only sees a class list that names
// a fill *and* an ink together — a `text-…` on a `<Button variant="secondary">`
// names neither, because the fill arrives from the stylesheet. So the seven were
// deleted by hand and nothing was watching the hole.
//
// This closes it from the other end: find the **call sites** of the cream
// controls and refuse a colour utility on them at all. Not "refuse an unreadable
// one" — refuse the category. A control whose fill is frozen has one correct ink
// set (`--on-control`, `--on-control-accent`, `--on-control-dim`) and `base.css`
// already applies it; a utility here is by construction either redundant or a
// regression, and telling the two apart needs a measurement nobody will redo.
//
// ## Scope
//
//  * Call sites are found by marker: `variant="secondary"`, `<Seg`, `<Input`,
//    and a `className` naming the `input` / `seg-opt` classes directly.
//  * The element's **opening tag** is read — from its `<` to the `>` that closes
//    it at brace depth zero — with comments stripped first. base.css taught that
//    lesson and so did `ds.test.tsx`: prose in this repo quotes class names in
//    backticks, and `ViewerBottomBar`'s comment names `border-neutral-700` four
//    lines above the code that no longer has it.
//  * A "colour utility" is `text-` / `bg-` / `border-` whose suffix is in
//    `COLOUR_UTILITIES`, i.e. resolved from `tailwind.config.ts`. `text-sm`,
//    `border-y-2` and `bg-transparent` are not colours and do not match. An
//    edge-direction segment is stripped, so `border-l-neutral-700` — the exact
//    string E-36 §5.3 removed — is caught.
//
// ## What it cannot see
//
// The failure E-42 §7 found *last*: an ink on a **sibling** absolutely
// positioned over a control (`TopBar`'s search icon and its ⌘K chip sit on the
// `.input`, not in it). Those elements are not call sites of anything and no
// class-list scan reaches them. That is a structural limit, it is the reason
// E-42 §7 says to re-count by hand, and it is not closed here.
// ---------------------------------------------------------------------------

/**
 * The colour utilities allowed to stand on a cream control, with the reason.
 *
 * Exact-match, not a floor: an entry disappearing is as much a regression as one
 * appearing, because each of these is doing a job `base.css` cannot do from a
 * class alone.
 */
const CREAM_CONTROL_UTILITIES = [
  // The viewer's thumbnail-strip toggle, pressed state. `.btn-secondary` has a
  // *hover* ink swap in the sheet but no `[aria-pressed]` state, so the pressed
  // ink has to arrive from the call site. It is `--on-control-accent`, the
  // absolute accent ink for this fill (7.06 washed) and the same ink the hover
  // swaps to, so pressed and hovered agree. The utility that used to be here was
  // `text-accent-text`, which is 1.65 on cream in the dark theme — E-42 §7's
  // first measured casualty, i.e. this seat is exactly where the defect was.
  'features/viewer/ViewerBottomBar.tsx | text-on-control-accent',
]

describe('a cream control carries no colour utility (E-42 §7)', () => {
  // `openingTag` is at module scope: the grain-exemption test asks the same
  // question of the override chip's class list.

  /** Every marker that means "this element is painted by a cream control class". */
  const MARKERS = [/variant="secondary"/g, /<Seg\b/g, /<Input\b/g, /className="input[\s"]/g]

  const sites = COMPONENTS.flatMap((file) => {
    const source = read(join('src', file))
    return MARKERS.flatMap((marker) =>
      [...source.matchAll(marker)].map((m) => ({ file, tag: openingTag(source, m.index) })),
    )
  })

  const found = [
    ...new Set(
      sites.flatMap(({ file, tag }) => {
        const out: string[] = []
        for (const literal of tag.matchAll(/'([^'\\\n]*)'|"([^"\\\n]*)"|`([^`\\\n$]*)`/g)) {
          const list = literal[1] ?? literal[2] ?? literal[3] ?? ''
          for (const u of list.matchAll(
            /(?:^|\s)(?:[\w[\]&_.+:-]*:)?(text|bg|border)-((?:[trblxy]-|top-|bottom-|left-|right-)?[a-z0-9-]+)(?![\w/-])/g,
          )) {
            const suffix = (u[2] ?? '').replace(/^(?:[trblxy]|top|bottom|left|right)-/, '')
            if (COLOUR_UTILITIES.has(suffix)) out.push(`${file} | ${u[1] ?? ''}-${suffix}`)
          }
        }
        return out
      }),
    ),
  ].sort()

  it('found the call sites it is meant to police', () => {
    // The positive control. An empty `sites` — a renamed variant, a `<Seg>` that
    // became `<Segmented>`, a tag scanner that stopped finding `>` — makes the
    // assertion below compare `[]` to a whitelist and fail loudly rather than
    // quietly, but only because the whitelist is non-empty today. This is what
    // holds when it is not.
    expect(sites.length).toBeGreaterThanOrEqual(20)
    expect(new Set(sites.map((s) => s.file)).size).toBeGreaterThanOrEqual(10)
    // And the tag reader must actually reach the class list, not stop at the
    // first `>` inside a `{…}` expression.
    expect(sites.some((s) => s.tag.includes('className'))).toBe(true)
  })

  it('paints no colour utility onto a control whose fill is frozen', () => {
    expect(found).toEqual([...CREAM_CONTROL_UTILITIES].sort())
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
    // `0` is on the list for the same reason `9999px` is: both are ends of the
    // scale rather than points on it. E-46's slider slug is square on purpose,
    // and `border-radius: 0` is what `rounded-none` compiles to — a corner
    // nobody has to look up a token for.
    const allowed = new Set(['0', '50%', '9999px'])
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

  it('gives the slider a 2px rule and a rectangular slug (E-46)', () => {
    // The 서고 slider is a ruler with a marker on it, not a pill with a disc:
    // 2px of rail and a 12×18 slug with square corners. Both engines, because
    // the two pseudo-element families are separate rules and a change made to
    // one of them silently ships half a design.
    for (const track of [
      "input[type='range']::-webkit-slider-runnable-track",
      "input[type='range']::-moz-range-track",
    ]) {
      const body = exactRule(track)?.body ?? ''
      expect(body, track).toMatch(/height:\s*2px/)
      expect(body, track).not.toMatch(/border-radius/)
    }
    for (const thumb of [
      "input[type='range']::-webkit-slider-thumb",
      "input[type='range']::-moz-range-thumb",
    ]) {
      const body = exactRule(thumb)?.body ?? ''
      expect(body, thumb).toMatch(/width:\s*12px/)
      expect(body, thumb).toMatch(/height:\s*18px/)
      expect(body, thumb).toMatch(/border-radius:\s*0/)
      // No lift on the slug: a flat drop under a 12px mark is a smudge, and the
      // slug already separates from the rail by colour.
      expect(body, thumb).not.toMatch(/box-shadow/)
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

  it('takes the app tone off the ramp, and the intensity from E-43 — not the prototype', () => {
    // `--color-neutral-900` is the 서고 ramp's dark end, and in this palette it
    // *is* the ink — so the wash and the text are the same colour and the tone
    // costs the file no literal at all.
    //
    // The intensity is no longer the prototype's either. 0.5 was carried across
    // untouched and nobody had asked whether it was right for this product;
    // E-43 rendered four amplitudes on the real screens and the user chose 1.
    expect(resolveToken(light, '--paper-tone')).toBe('#221E1A')
    // Pinned rather than ranged: the value is a user's choice between four
    // rendered options, so a drift back to 0.5 is a decision being undone, not a
    // tuning. The **two** tokens re-derived for it — dark `--ink-faint` and dark
    // `--ink-th` — are in this file, in `REPAIRED`. The third pair E-43 took
    // under AA is not re-derived at all: `--on-hot` is already pure black, so it
    // is exempted from the wash instead (`lifts the hot marker out of the wash`).
    expect(light.get('--paper-intensity')).toBe('1')
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

  it('lands −12~13/255 on the cream ground — twice the prototype, on purpose', () => {
    // The one number a reviewer can check without a browser. `fractalNoise`
    // centres each channel on 0.5, so the mean mask alpha is half the matrix
    // coefficient; at `--paper-intensity: 1` that is 0.063 of near-black over
    // the cream, and the model says 12.7/255.
    //
    // **It used to say −6~7 and cite the prototype for it.** That was true of
    // the prototype's own intensity, 0.5, and E-43 doubled it after the user
    // compared four intensities rendered on the real screens. The bound moved
    // with the decision rather than being widened to accommodate it, and the
    // render agrees: the library ground measured 227.2 → 221.0 across that
    // change, a delta of 6.2 on top of the 6.5 already there.
    const ground = parseColour(resolveToken(light, '--color-bg'))
    const tone = parseColour(resolveToken(light, '--paper-tone'))
    const meanAlpha = (matrix[15] ?? 0) * 0.5
    const intensity = Number(light.get('--paper-intensity'))
    const delta = intensity * meanAlpha * (ground.r - (ground.r * tone.r) / 255)
    expect(delta).toBeGreaterThan(12.0)
    expect(delta).toBeLessThan(13.5)
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

  it('leaves the texture at the top — nothing in base.css outranks it, but one', () => {
    // A rule that punched through the grain would be a rectangle of un-papered
    // screen, which is what a bare `z-index: 900` in a sheet with a closed
    // ladder eventually produces.
    //
    // **This check read bare integers only, and the sheet has none.** Every
    // `z-index` in base.css is `var(--z-…)` or a `calc()` on one, so the scan
    // matched zero declarations and `expect([]).toEqual([])` passed on an empty
    // hand. E-43 then added the first rule in the sheet to sit *above*
    // `--z-texture` — the hot marker's lift, 91 against a ceiling of 90 — and
    // nothing said anything; the same value written as the literal `91` failed.
    // See `zValue` for why that is the wrong shape for a gate.
    //
    // Widened, the exception is explicit rather than accidental, and stated as an
    // exact list: an entry vanishing is as much a regression as one appearing,
    // because the entry *is* the AA repair for `--on-hot` on the hot marker.
    const ceiling = Number(light.get('--z-texture'))
    const EXPECTED_ABOVE_CEILING = [
      // The one intended exception (E-43). `--on-hot` is pure black on
      // `--color-hot` — the palette's ceiling at 4.9988 — and the wash takes it
      // to 4.464, so the marker is lifted out of the wash instead of the retired
      // brand red being moved. It punches through its **bar's** grain, not the
      // global layer: the bar is a stacking context, so this rung is local to it
      // and the reading stage is untouched. Held to exactly one step so it
      // clears the grain and nothing else.
      `${GRAIN_EXEMPT_SELECTOR}: ${String(ceiling + 1)}`,
    ]

    // At-rule containers are skipped: `allRules` hands back `@layer base` with
    // the whole layer as its body, so counting its text too would report every
    // declaration twice, once under a selector that is not a selector.
    const declared = allRules(BASE)
      .filter((r) => !r.selector.startsWith('@'))
      .flatMap((r) =>
        [...r.body.matchAll(/z-index:\s*([^;]+);/g)].map((m) => ({
          selector: r.selector,
          raw: (m[1] ?? '').trim(),
          value: zValue(m[1] ?? ''),
        })),
      )

    // The positive control the old regex did not have. A scan that finds nothing
    // is not a sheet with a clean ladder, and the two are indistinguishable from
    // the assertion alone.
    expect(declared.length, 'the z-index scan found nothing to read').toBeGreaterThanOrEqual(8)
    expect(
      declared.some((d) => d.value === ceiling),
      'no rule sits at --z-texture, so the ceiling is not being exercised',
    ).toBe(true)

    // A notation this file cannot read is itself an offender. Skipping it is how
    // the bare-integer scan stayed green through a sheet it could not parse.
    expect(
      declared.filter((d) => d.value === null).map((d) => `${d.selector}: ${d.raw}`),
      'unreadable z-index notation — teach zValue or use the ladder',
    ).toEqual([])

    const above = declared
      .filter((d) => d.value !== null && d.value > ceiling)
      .map((d) => `${d.selector}: ${String(d.value ?? 0)}`)
      .sort()
    expect(above).toEqual([...EXPECTED_ABOVE_CEILING].sort())
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
    // The grain costs up to **0.96** of ratio at the shipped intensity — the pair
    // that falls furthest is light `--ink` on the surface, 10.283 → 9.321 — and
    // **four** pairs in this palette are re-derived in tokens.css because of it
    // (`REPAIRED` above), with a fifth exempted from the wash outright because it
    // had no ink left to move. Everything clears AA with the
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
    // E-46's slug is 16×28 below 768 — taller than the 26px disc it replaces,
    // so the finger target grew rather than shrank when the shape changed.
    expect(mobileRule("input[type='range']::-webkit-slider-thumb")).toMatch(/height:\s*28px/)
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
