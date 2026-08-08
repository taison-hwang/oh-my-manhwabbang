import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { describe, expect, it } from 'vitest'

import { allRules, customProperties, findRule, topLevelRules, type CssRule } from './cssRules'

/**
 * The soft-UI control contract — E-36 §3's seven completion criteria, as tests.
 *
 * ## Why this file exists
 *
 * E-36 §6 counted it: of the ~30 component rules the soft-UI skin was missing,
 * **six were visible to a gate and twenty-four were not.** Every `box-shadow`,
 * every `transition`, every `border: 0`, every `:active` press was in the second
 * group — which is why all thirty survived several sessions of a fully green
 * suite. The ruling states the consequence in one sentence: *"검사를 붙이지 않고
 * 넘기면, 다음 세션은 이 판정을 다시 발견하게 된다."*
 *
 * So the seven criteria E-36 §3 wrote "in a form a human can grep for" are
 * written here in a form that fails a build instead. The technique is not new —
 * it is `tokens.test.ts`'s: parse the stylesheet with `cssRules.ts` and assert
 * on its structure. Nothing here renders anything, because the failure being
 * guarded is a declaration that is absent, and an absent declaration is invisible
 * to `getComputedStyle` under jsdom (`css: false`) as well as to a screenshot.
 *
 * ## What lives where
 *
 *  * §3.1–§3.5 and §3.7 are below.
 *  * **§3.6 (no hard-coded hex in the stylesheet layer, no arbitrary px radius)
 *    is deliberately not re-asserted here.** It is already covered twice over
 *    and duplicating it would create a second source of truth for the same rule:
 *    `tokens.test.ts` → `keeps base.css free of hex` and `rounds nothing with a
 *    number the token scale does not name`; `hygiene.test.ts` → `confines every
 *    hex in the stylesheet layer to tokens.css` (every `.css` under `src/`
 *    except `tokens.css`) and `rounds nothing with a number that is not a token`
 *    (every `.ts`/`.tsx`/`.css` under `src/`, whitelisted off the `--radius-*`
 *    block itself). Between them the criterion is closed for the whole tree, not
 *    just for `base.css`.
 *  * §3.7's *enumeration* — "these nine tokens exist in the light block and have
 *    no dark counterpart" — lives in `tokens.test.ts`'s `leaves the absolutes
 *    alone` list, next to the five absolutes that came before them. What is here
 *    instead is the half that list cannot state: **what each of the nine is
 *    pinned to**, which is the only thing that makes freezing a value honest
 *    rather than arbitrary.
 */

const read = (rel: string): string => readFileSync(resolve(process.cwd(), rel), 'utf8')

const TOKENS = read('src/styles/tokens.css')
const BASE = read('src/styles/base.css')

const BASE_RULES: CssRule[] = allRules(BASE)

const tokenRules = topLevelRules(TOKENS)
function tokenBlock(needle: string): Map<string, string> {
  const rule = findRule(tokenRules, needle)
  if (rule === undefined) throw new Error(`tokens.css has no rule matching ${needle}`)
  return customProperties(rule.body)
}
const light = tokenBlock('[data-theme=')
const dark = tokenBlock("[data-theme='dark']")

/**
 * The rule with exactly this selector.
 *
 * Exact, not substring, for the reason `tokens.test.ts` gives: `.seg` matches
 * `.seg-opt` first, and an assertion that read the wrong rule would pass or fail
 * for a reason that has nothing to do with what it names.
 */
function rule(selector: string): string {
  const found = BASE_RULES.find((r) => r.selector === selector)
  if (found === undefined) throw new Error(`base.css has no rule for \`${selector}\``)
  return found.body
}

/** The value of one declaration in a rule body, whitespace collapsed. */
function decl(selector: string, property: string): string | undefined {
  const re = new RegExp(String.raw`(?:^|[;{]|\s)${property}\s*:\s*([^;}]+)`)
  return re.exec(rule(selector))?.[1]?.trim().replace(/\s+/g, ' ')
}

/** Every control class the seven criteria are about, and where it is asserted. */
const CONTROL_SELECTORS = [
  '.btn',
  '.btn-primary',
  '.btn-primary:active:not(:disabled)',
  '.btn-secondary',
  '.btn-secondary:hover:not(:disabled)',
  '.btn-secondary:active:not(:disabled)',
  '.btn-ghost',
  '.tag',
  '.tag-neutral',
  '.tag-outline',
  '.input',
  '.input:focus-visible',
  '.radio .dot',
  ".radio[data-checked='true'] .dot",
  '.seg',
  '.seg-opt',
  ".seg-opt[data-checked='true']",
  '.card',
]

describe('the soft-UI control contract is calibrated (E-36 §6)', () => {
  it('parsed base.css into rules, and found every control class by name', () => {
    // The calibration this whole file rests on. `allRules` returning `[]`, or
    // `@layer components` being re-indented into something the scanner reads
    // differently, would make every assertion below iterate over nothing and
    // pass — which is precisely the failure mode E-36 §6 is about: green for a
    // reason unrelated to correctness.
    expect(BASE_RULES.length).toBeGreaterThan(100)
    const selectors = new Set(BASE_RULES.map((r) => r.selector))
    const missing = CONTROL_SELECTORS.filter((s) => !selectors.has(s))
    expect(missing).toEqual([])
  })

  it('reads the stylesheet with its comments removed', () => {
    // `allRules` strips comments (`cssRules.ts`), and that is load-bearing here
    // for the same reason it is in `ds.test.tsx`: base.css explains each of these
    // rules in prose that quotes the very declarations being asserted. A
    // scanner that saw comment text could be satisfied — or tripped — by a
    // sentence. `.seg-opt + .seg-opt` below is the sharpest case: the words are
    // in a comment in this repo today, and the rule must still read as absent.
    expect(BASE_RULES.some((r) => r.body.includes('E-36'))).toBe(false)
    expect(BASE).toContain('E-36')
  })
})

// ---------------------------------------------------------------------------
// §3.1 — a control is a recess, not a bordered box
// ---------------------------------------------------------------------------

describe('E-36 §3.1 — the control recess arrived', () => {
  /**
   * **The token name in the criterion is not the token name in the sheet.**
   *
   * E-36 §3.1 asks for `var(--shadow-inset)` to be non-zero in base.css. E-42 §3
   * then ruled that a shadow follows the surface it falls on rather than the
   * theme, which made the recess cut into a cream control an *absolute*:
   * `--shadow-control-inset`, the light block's `--shadow-inset` frozen as a
   * literal. E-42 §4 records the substitution explicitly, because counting §3.1
   * literally reads zero and looks like a shortfall.
   *
   * Both names are therefore handled here, and the pairing is what makes the
   * substitution checkable rather than merely asserted: the frozen token must be
   * **value-identical to the light `--shadow-inset`** (or it is a different
   * shadow wearing the rename), and it must be a literal rather than
   * `var(--shadow-inset)` (a reference resolves in the scope it is *used* in, so
   * it would flip with the theme — and not flipping is the only reason the token
   * exists).
   */
  it('recesses .input and the .seg track — under the name E-42 §4 substituted', () => {
    expect(decl('.input', 'box-shadow')).toBe('var(--shadow-control-inset)')
    expect(decl('.seg', 'box-shadow')).toBe('var(--shadow-control-inset)')
  })

  it('freezes that recess to the light --shadow-inset, exactly', () => {
    const frozen = light.get('--shadow-control-inset')
    expect(frozen).toBe(light.get('--shadow-inset'))
    expect(frozen).not.toContain('var(')
    // ...and the thing it was frozen *against* really does flip, or freezing it
    // bought nothing. The dark inset is black plus an accent-300 highlight: a
    // recess for a teal surface, painted onto a cream one.
    expect(dark.get('--shadow-inset')).toBeDefined()
    expect(dark.get('--shadow-inset')).not.toBe(light.get('--shadow-inset'))
  })

  /**
   * The other half of the substitution, and the one that can actually regress.
   *
   * Nothing in base.css may reach for `var(--shadow-inset)` today: every recess
   * in the sheet is cut into a cream control, and a theme-relative inset on an
   * absolute surface is the exact defect E-42 §3 names. The whitelist is empty
   * on purpose — a future rule that recesses a surface which *does* flip belongs
   * in it, with a reason, and this failure is how that conversation starts.
   * Somebody "restoring" E-36 §3.1's literal wording by writing the old name on
   * `.input` fails here.
   */
  it('never uses the theme-relative --shadow-inset in the sheet', () => {
    const recessed = (token: string): string[] =>
      BASE_RULES.filter((r) => !r.body.includes('{'))
        .filter((r) => new RegExp(String.raw`box-shadow[^;}]*var\(${token}\)`).test(r.body))
        .map((r) => r.selector)

    // **Positive control, and it is not decoration.** An assertion that a regex
    // finds nothing is satisfied just as well by a regex that can no longer find
    // anything — a renamed property, a reformat that puts the value on its own
    // line, a `box-shadow` written as a longhand. Running the same matcher
    // against the token that *is* everywhere proves the search still works
    // before its emptiness is read as good news. (`found borders to police`
    // below is the same shape; this one was missing it.)
    expect(recessed('--shadow-control-inset').length).toBeGreaterThanOrEqual(2)

    const ALLOWED_FLIPPING_RECESS: string[] = []
    expect(recessed('--shadow-inset').filter((s) => !ALLOWED_FLIPPING_RECESS.includes(s))).toEqual(
      [],
    )
  })

  it('keeps the focus marker inside the well and kills the outer outline', () => {
    // E-36 table row 22. The base layer rings every focused element with
    // `outline: 2px solid var(--color-hot)`; a recessed field draws the same
    // marker as the outer layer of its own shadow stack instead, so the ring
    // reads *in* the recess. Deleting the `outline: none` draws both rings;
    // deleting the whole rule restores the outer one and the recess disappears
    // on focus.
    const focus = rule('.input:focus-visible')
    expect(focus).toMatch(/box-shadow:\s*var\(--shadow-control-inset\),\s*0 0 0 2px var\(--color-hot\)/)
    expect(focus).toMatch(/outline:\s*none/)
  })

  it('fills the controls from the absolute cream set, not the theme surface', () => {
    // E-42 §2: cream is global, so `.btn-secondary` on the viewer's dark bar is
    // the same pill as in the light library. `--color-surface` here would make
    // it a deep-teal pill on a deep-teal bar — the divergence
    // `docs/ui-shots/README.md` measured as the largest in the set.
    expect(decl('.btn-secondary', 'background')).toBe('var(--control-fill)')
    expect(decl('.btn-secondary', 'color')).toBe('var(--on-control)')
    expect(decl('.btn-secondary:hover:not(:disabled)', 'background')).toBe(
      'var(--control-fill-hover)',
    )
    expect(decl('.btn-secondary:hover:not(:disabled)', 'color')).toBe('var(--on-control-accent)')
    expect(decl('.input', 'background')).toBe('var(--control-fill)')
    expect(decl('.input', 'color')).toBe('var(--on-control)')
    expect(decl('.input::placeholder', 'color')).toBe('var(--on-control-dim)')
    // The recessed track is the *well*, one step down from the raised fill.
    expect(decl('.seg', 'background')).toBe('var(--control-well)')
    expect(decl('.seg-opt', 'color')).toBe('var(--on-control-dim)')
  })

  /**
   * Nothing theme-relative may be painted on a surface that does not flip.
   *
   * This is E-42 §3's rule generalised, and it was written after a review
   * measured the one rule still breaking it: `.seg-opt[data-checked='false']:hover`
   * washed the option with `background: var(--hover-tint)`. The tint flips — a
   * dark wash on light, a **near-white** one on dark — while the well underneath
   * it is an absolute cream. On the dark theme that is a near-white 8 % wash on
   * near-white cream: **1.00:1**. The hover was not subtle there, it did not
   * happen. The fix was to delete it and let the ink carry the state.
   *
   * The check derives "theme-relative" from `tokens.css` rather than listing
   * tints by hand: a token is invariant if the dark block does not declare it, or
   * declares it identically. `--color-accent` passes on the second clause (it is
   * the brand constant), `--hover-tint` fails on neither. So a *new* tint token
   * cannot slip past by not being on a list, and the assertion tracks the token
   * layer if a token's status ever changes.
   *
   * **Scope, stated because it is narrow.** Four properties —
   * `background`, `background-color`, `color`, `caret-color` — and only where the
   * value is a bare `var(--token)`. Not covered, deliberately:
   *
   *  * `border-color`, `outline-color`, `fill`, `stroke` and the shadow colours.
   *    A theme-relative shadow on a frozen surface is real (it is what
   *    `--shadow-control-raised` exists for) but it is held by the §3.1/§3.5
   *    assertions by token name, which is stricter than a flip test.
   *  * Composite values — `background: linear-gradient(var(--a), var(--b))` or a
   *    shorthand with more than the colour in it. There are none in the sheet
   *    today on these four classes.
   *  * Anything reaching these controls from a Tailwind utility, which is a
   *    different layer and always wins. `tokens.test.ts` holds that half.
   */
  it('paints nothing theme-relative onto the absolute cream surfaces', () => {
    // The classes whose ground is the frozen cream set. Their descendants and
    // states inherit that ground, so `.input::placeholder` and
    // `.seg-opt[data-checked='false']:hover` are in scope by their base class.
    const CREAM_CONTROLS = new Set(['.btn-secondary', '.input', '.seg', '.seg-opt'])
    const flips = (token: string): boolean =>
      dark.has(token) && dark.get(token) !== light.get(token)

    const offenders: string[] = []
    let scanned = 0
    for (const r of BASE_RULES) {
      if (r.body.includes('{')) continue
      const base = /^\.[a-z-]+/.exec(r.selector)?.[0] ?? ''
      if (!CREAM_CONTROLS.has(base)) continue
      // The leading boundary is not cosmetic: without it `color\s*:` also matches
      // the tail of `-webkit-text-fill-color:`, `border-color:` and
      // `outline-color:`, so the scan would silently claim a coverage it does not
      // have and the count below would be inflated by properties this rule has
      // never reasoned about.
      for (const m of r.body.matchAll(
        /(?:^|[;{]|\s)(background|background-color|color|caret-color)\s*:\s*var\((--[\w-]+)\)/g,
      )) {
        scanned += 1
        const token = m[2] ?? ''
        if (flips(token)) offenders.push(`${r.selector}: ${m[1] ?? ''} is var(${token})`)
      }
    }
    // **Positive control.** `offenders` is empty both when every cream control is
    // painted from a frozen token and when the scan matched no declarations at
    // all — a selector shape it no longer recognises, a property list that drifted
    // from the sheet. The cream set carries nine such declarations today; the
    // floor is deliberately well under that, so a reformat does not fail this but
    // a scan that stopped working does.
    expect(scanned).toBeGreaterThanOrEqual(6)
    expect(offenders).toEqual([])
  })

  /**
   * The absolute cream surfaces, enumerated — and the reason the list is an
   * assertion rather than a constant.
   *
   * A frozen fill freezes what the *author* paints. It does not freeze what the
   * **UA** paints into the same box, and `color-scheme` is the only control over
   * that. `.input` is where it bit: under `[data-theme='dark']` the browser drew
   * the native search-field ✕ in its dark-scheme ink, on cream, at **1.14:1** —
   * a glyph no token in this repo can reach, painted onto a surface the whole
   * ruling exists to hold still.
   *
   * So the general rule is: *a class that pins its fill must also pin the scheme
   * the UA renders inside it.* That rule only has force on classes the UA
   * actually paints into, which is why it is asserted on `.input` and not on the
   * other three — a `<div>` track and a `<button>` pill have no UA-drawn interior
   * to lose. This enumeration is what keeps that reasoning from going stale: add
   * a fourth cream surface and this fails, and whoever adds it has to answer the
   * `color-scheme` question rather than inherit an answer nobody re-asked.
   */
  it('names every absolute cream surface, so a new one must answer for itself', () => {
    const CREAM_FILLS = /var\(--control-(fill|fill-hover|well)\)/
    const painted = BASE_RULES.filter((r) => !r.body.includes('{'))
      .filter((r) => /(background|background-color)\s*:\s*var\(--control-/.test(r.body))
      .filter((r) => CREAM_FILLS.test(r.body))
      .map((r) => r.selector)
      .sort()
    expect(painted).toEqual([
      '.btn-secondary',
      '.btn-secondary:hover:not(:disabled)',
      '.input',
      '.seg',
    ])
  })

  it('pins color-scheme on the one cream surface the UA paints into', () => {
    // Not `dark`, and not absent: `light` is the only value that makes the UA's
    // own ink agree with a fill that never flips. Deleting this line restores the
    // 1.14:1 ✕ in the dark theme, and no author-CSS check could see it — the
    // colour is not in this repo.
    expect(decl('.input', 'color-scheme')).toBe('light')
    // ...and nothing in the sheet may pin the other direction on a cream ground.
    const offenders = BASE_RULES.filter((r) => !r.body.includes('{'))
      .filter((r) => /color-scheme\s*:\s*dark/.test(r.body))
      .map((r) => r.selector)
    expect(offenders).toEqual([])
  })

  it('reads the token layer well enough to tell a flipping token from a frozen one', () => {
    // Calibration for the assertion above. If `flips()` ever answered `false` for
    // everything — a parse that came back empty, a dark block that stopped being
    // found — the scan would iterate over real rules and report nothing.
    expect(dark.get('--hover-tint')).toBeDefined()
    expect(dark.get('--hover-tint')).not.toBe(light.get('--hover-tint'))
    // ...and the two kinds the scan must *not* flag: an absolute (absent from
    // the dark block) and a constant (present, identical).
    expect(dark.has('--control-well')).toBe(false)
    expect(dark.get('--color-accent')).toBe(light.get('--color-accent'))
  })
})

// ---------------------------------------------------------------------------
// §3.2 — the four classes that were missing an elevation
// ---------------------------------------------------------------------------

describe('E-36 §3.2 — the lifted surfaces are lifted', () => {
  it('gives .btn-secondary, .tag-neutral and .tag-outline --shadow-sm and .card --shadow-md', () => {
    for (const selector of ['.btn-secondary', '.tag-neutral', '.tag-outline']) {
      expect(decl(selector, 'box-shadow'), selector).toBe('var(--shadow-sm)')
    }
    expect(decl('.card', 'box-shadow')).toBe('var(--shadow-md)')
  })

  it('lifts them with the *flipping* elevation, because they sit on the page', () => {
    // The counterpart to §3.1, and not a restatement of the line above: E-42 §3's
    // rule cuts both ways. These four are raised **on the page ground**, which
    // does flip, so they take the theme-relative token — a cream pill's shadow
    // still falls on whatever the page is made of. Only a shadow that lands on
    // an absolute surface freezes, and there is exactly one of those in the
    // sheet (`.seg-opt[data-checked='true']`, asserted in §3.5 below).
    for (const selector of ['.btn-secondary', '.tag-neutral', '.tag-outline', '.card']) {
      expect(decl(selector, 'box-shadow'), selector).not.toContain('--shadow-control-')
    }
  })

  it('animates every property the variants move, not just the background', () => {
    // E-36 table row 1, and one of the twenty-four the gates could not see. The
    // pill presses into an inset and the cream secondary swaps its ink on hover,
    // so a `background`-only transition snaps three of the four changes.
    const transition = decl('.btn', 'transition') ?? ''
    for (const property of ['background', 'box-shadow', 'color', 'transform']) {
      expect(transition, `.btn does not animate ${property}`).toContain(`${property} var(--hover-fade)`)
    }
    const segTransition = decl('.seg-opt', 'transition') ?? ''
    for (const property of ['background', 'color', 'box-shadow']) {
      expect(segTransition, `.seg-opt does not animate ${property}`).toContain(
        `${property} var(--hover-fade)`,
      )
    }
  })
})

// ---------------------------------------------------------------------------
// §3.3 — no control is defined by a border any more
// ---------------------------------------------------------------------------

/**
 * Every visible border left in base.css, keyed `[media] selector | property: value`.
 *
 * E-36 §3.3: *"컨트롤 클래스 중 `border: 1px solid var(--control-border)`만으로
 * 정의되는 것이 0개다. 테두리가 남는 곳은 마커뿐"* — the hot inset ring, the focus
 * outline, and `.radio .dot`. A whitelist rather than a pattern, because the
 * criterion is about what is *allowed to remain*: a new bordered control would
 * match any pattern narrow enough to be useful, and would be invisible to one
 * broad enough to be safe.
 *
 * `border: 0`, `border: none` and any border painted in `transparent` are not
 * boundaries and are filtered out before the comparison — a transparent border
 * is spacing, which is what E-36 table row 2 retired it as.
 *
 * **The key carries the enclosing `@media` prelude**, and that is the whole
 * mechanism behind the third category below. A border that is legitimate inside
 * `(forced-colors: active)` is the bordered box returning if it is written
 * anywhere else, so the two cannot share a whitelist entry: move the declaration
 * out of the media block and the key changes, the entry stops matching, and this
 * fails. Allowing it by selector alone would make the exception a back door into
 * the general rule.
 */
const BORDER_MARKERS = [
  // The unchecked radio marker. Named in §3.3's exemption list verbatim: the dot
  // is a 16px circle with no fill of its own, so its ring *is* the control.
  '.radio .dot | border: 1.5px solid var(--control-border)',
  // The checked marker takes the same ring in the fill's colour, so the dot
  // reads as a solid disc rather than a filled ring (E-42 §1, ruling z = C).
  ".radio[data-checked='true'] .dot | border-color: var(--accent-fill)",
  // Not a control at all: the viewer's 11px loading spinner is a rotating arc,
  // and the border *is* the arc (E-36 §5.3 says so about the same shape in
  // `PageLoadingIndicator.tsx`). Deleting it deletes the spinner.
  '.spinner | border: 2px solid var(--fill-track-2)',
  '.spinner | border-top-color: var(--accent-fill)',
]

/**
 * The other thing a border is still allowed to be: a **structural rule**.
 *
 * §3.3 retires the border as the boundary of a *control*. It does not retire the
 * hairline, which is a device of its own in this design — `--rule` /
 * `--rule-strong` / `--color-divider` exist for it, `.hr` is nothing else, and
 * ui-spec §2.3's table contract is a 2px rule under the header and a 1px rule
 * under each row. Every entry here is a **single edge** drawn in a divider
 * token: one line separating two things, never four lines enclosing one.
 *
 * That is what keeps the list from becoming an escape hatch. A control that came
 * back as a four-sided box would not qualify on either count, and neither would
 * a single edge drawn in `--control-border` (the `spends --control-border on
 * exactly one rule` assertion below is the second lock on that).
 */
const BORDER_RULES = [
  // The DS nav's underline (ui-spec §2.3), and the drawer's right edge, which is
  // the mobile stand-in for `--shadow-sidebar` — a panel abutting the scrim
  // rather than floating over it.
  '.nav | border-bottom: 2px solid var(--color-divider)',
  '.drawer-panel | border-right: 2px solid var(--rule-strong)',
  '.table th | border-bottom: 2px solid var(--color-divider)',
  '.table td | border-bottom: 1px solid var(--color-divider)',
]

/**
 * The third category: a **forced-colors fallback**, and it exists because this
 * round's own success created the hole.
 *
 * `forced-colors: active` forces `box-shadow` to `none`. Every boundary, every
 * focus marker **and every selected-state marker** in the soft-UI control set is
 * a shadow now — that is what §3.1 through §3.4 above are *for* — so in that mode
 * the product renders with **zero control borders, zero focus indication and no
 * way to tell a selected control from an unselected one**. Two reviews measured
 * it with Playwright's `forcedColors: 'active'`, which is the only way to see it:
 * a check that reads the sheet cannot know a UA stylesheet is about to delete a
 * property, and no ordinary render shows it.
 *
 * The third of those took the second look to find. The hot inset ring is
 * `.seg-opt[data-checked='true']`'s whole selected marker and it is a
 * `box-shadow`, so a checked segment and an unchecked one came out
 * **pixel-identical** — in the viewer that is no way at all to read which fit
 * mode is on.
 *
 * So these borders are not the bordered box coming back. They are the boundary
 * being restated in the one mode that cannot draw it the new way, in system
 * colour keywords (`ButtonBorder`, `Highlight`) rather than tokens — because in
 * forced colours the palette is the reader's, not ours. Cost to the ordinary
 * render: zero, the block never matches.
 *
 * Each entry is pinned **inside its media prelude**. That is the difference
 * between an exception and a loophole.
 */
const FORCED_COLORS_FALLBACK = [
  '@media (forced-colors: active) .btn-primary, .btn-secondary, .input, .seg, .tag-neutral, .tag-outline, .card | border: 1px solid ButtonBorder',
]

describe('E-36 §3.3 — the border is gone from every control', () => {
  const VISIBLE_BORDERS = ((): string[] => {
    // `border-collapse`, `border-spacing` and `border-image` are not edges — they
    // are table layout and a fill. Matching them would put `border-collapse:
    // collapse` in a list about control boundaries, which is how a whitelist
    // starts collecting entries nobody reads.
    const NOT_AN_EDGE = /^border-(radius|collapse|spacing|image)/
    const out: string[] = []
    for (const r of BASE_RULES) {
      if (r.body.includes('{')) continue // an at-rule wrapper, not a rule
      // `@layer` is dropped and `@media` is kept: the layer a rule sits in does
      // not change whether its border is legitimate, but the media query does.
      const media = r.context.filter((c) => c.startsWith('@media')).join(' ')
      const selector = r.selector.replace(/\s+/g, ' ')
      const where = media === '' ? selector : `${media} ${selector}`
      for (const m of r.body.matchAll(/(border(?:-[a-z]+)*)\s*:\s*([^;}\n]+)/g)) {
        const property = m[1] ?? ''
        if (NOT_AN_EDGE.test(property)) continue
        const value = (m[2] ?? '').trim().replace(/\s+/g, ' ')
        if (/^(0|none)$/.test(value) || value.includes('transparent')) continue
        out.push(`${where} | ${property}: ${value}`)
      }
    }
    return out.sort()
  })()

  it('found borders to police — the whitelist is not the empty set', () => {
    // Calibration. A regex that stopped matching would leave the assertion below
    // comparing `[]` to `[]`, and the criterion would be enforced by nothing.
    expect(VISIBLE_BORDERS.length).toBeGreaterThan(0)
  })

  it('leaves a visible border only on a marker, a structural rule or a forced-colors fallback', () => {
    expect(VISIBLE_BORDERS).toEqual(
      [...BORDER_MARKERS, ...BORDER_RULES, ...FORCED_COLORS_FALLBACK].sort(),
    )
  })

  it('restates the boundary, the focus ring and the selected state where box-shadow is forced off', () => {
    // The positive half of the fallback, so it cannot be deleted quietly. The
    // enumeration above only fails on a border that *appears*; this fails on one
    // that disappears — and the mode it protects is one no ordinary render and no
    // screenshot in `docs/ui-shots/` will ever show.
    const forced = BASE_RULES.filter((r) =>
      r.context.some((c) => c.includes('forced-colors: active')),
    )
    const bordered = forced.find((r) => r.body.includes('border:'))
    for (const control of [
      // `.btn-primary` is here for the same reason as the rest: its edge is
      // `--shadow-sm` and nothing else, so forced colours erased a filled button
      // down to its label.
      '.btn-primary',
      '.btn-secondary',
      '.input',
      '.seg',
      '.tag-neutral',
      '.tag-outline',
      '.card',
    ]) {
      expect(bordered?.selector.replace(/\s+/g, ' '), `${control} has no forced-colors edge`).toContain(
        control,
      )
    }
    // `Highlight` and `ButtonBorder` rather than tokens: in forced colours the
    // palette belongs to the reader, and a `var(--…)` here would be overridden
    // to the same system colour as everything else — i.e. invisible again.
    expect(bordered?.body).toMatch(/border:\s*1px solid ButtonBorder/)
    const focus = forced.find((r) => r.selector === '.input:focus-visible')
    expect(focus?.body).toMatch(/outline:\s*2px solid Highlight/)
    expect(focus?.body).toMatch(/outline-offset:\s*0/)
    // **Selected state**, the third casualty and the one that is not an edge.
    // The marker is drawn *inside* the option (`outline-offset: -2px`) so it
    // cannot collide with the neighbour 2px away, and it covers `[aria-pressed]`
    // as well — the viewer's chrome toggles say "on" the way a checked segment
    // does, and they lost their marker to the same forced `box-shadow: none`.
    const selected = forced.find((r) => r.selector.includes("[data-checked='true']"))
    expect(selected?.selector.replace(/\s+/g, ' ')).toBe(
      ".seg-opt[data-checked='true'], [aria-pressed='true']",
    )
    expect(selected?.body).toMatch(/outline:\s*2px solid Highlight/)
    expect(selected?.body).toMatch(/outline-offset:\s*-2px/)
  })

  it('spends --control-border on exactly one rule, and it is the radio dot', () => {
    // The narrower half of the same criterion, stated separately because it is
    // the one that names the token: `--control-border` is what a *bordered box*
    // is drawn in, so every use of it outside the dot marker is a control that
    // did not become a surface.
    const users = BASE_RULES.filter((r) => !r.body.includes('{'))
      .filter((r) => r.body.includes('var(--control-border)'))
      .map((r) => r.selector)
      .sort()
    expect(users).toEqual(['.radio .dot'])
  })

  it('keeps the hot marker and the focus outline, which are the two exemptions', () => {
    // The other half of §3.3's exemption list. These are *markers* — they say
    // "current / selected / focused" (E-32 §1) — and a check that only counted
    // borders downward would be satisfied by deleting them.
    expect(decl(':focus-visible', 'outline')).toBe('2px solid var(--color-hot)')
    expect(rule(".seg-opt[data-checked='true']")).toContain('inset 0 0 0 2px var(--color-hot)')
    expect(rule('.seg-opt:has(input:focus-visible)')).toContain('2px solid var(--color-hot)')
    // Ruling z = C: the checked dot keeps `--accent-fill` for its body and wears
    // hot as an inset ring — the same grammar as the segment above. Order is
    // load-bearing: the first layer of a box-shadow list paints on top, so the
    // 1.5px hot ring must precede the 4px `--color-bg` ring or the bg ring
    // covers it and the marker silently disappears.
    expect(decl(".radio[data-checked='true'] .dot", 'background')).toBe('var(--accent-fill)')
    expect(rule(".radio[data-checked='true'] .dot")).toMatch(
      /box-shadow:\s*inset 0 0 0 1\.5px var\(--color-hot\),\s*inset 0 0 0 4px var\(--color-bg\)/,
    )
  })
})

// ---------------------------------------------------------------------------
// §3.4 — a press is a recess plus a nudge
// ---------------------------------------------------------------------------

describe('E-36 §3.4 — pressed controls sink', () => {
  /**
   * `:active` rules that do **not** press, with the reason.
   *
   * `.btn-ghost` has no fill — it is ink on the page ground — so there is no
   * surface to cut a recess into and nothing to lift off. It presses with a
   * tint, which is what the prototype does too.
   */
  const NOT_A_SURFACE = ['.btn-ghost:active:not(:disabled)']

  const activeRules = BASE_RULES.filter(
    (r) => !r.body.includes('{') && r.selector.includes(':active'),
  )

  it('found the :active rules — there are some, and they are the buttons', () => {
    expect(activeRules.map((r) => r.selector).sort()).toEqual([
      '.btn-ghost:active:not(:disabled)',
      '.btn-primary:active:not(:disabled)',
      '.btn-secondary:active:not(:disabled)',
    ])
  })

  it('presses the two filled buttons with an inset and translateY(1px)', () => {
    // `--shadow-accent-inset` on the primary and `--shadow-control-inset` on the
    // secondary is E-42 §3's table read literally: the primary's recess is cut
    // into the accent fill, which is a *dark* teal in both themes and therefore
    // takes the dark-ground inset even when the app is light.
    expect(decl('.btn-primary:active:not(:disabled)', 'box-shadow')).toBe(
      'var(--shadow-accent-inset)',
    )
    expect(decl('.btn-primary:active:not(:disabled)', 'transform')).toBe('translateY(1px)')
    expect(decl('.btn-secondary:active:not(:disabled)', 'box-shadow')).toBe(
      'var(--shadow-control-inset)',
    )
    expect(decl('.btn-secondary:active:not(:disabled)', 'transform')).toBe('translateY(1px)')
  })

  it('never gives a control one half of a press without the other', () => {
    // The generic form, so a *new* pressable control cannot ship with the sink
    // and no nudge (or the nudge and no sink). Half a press is the shape the
    // twenty-four un-gated rules had: present enough to look done, absent where
    // it counts.
    const offenders: string[] = []
    for (const r of activeRules) {
      if (NOT_A_SURFACE.includes(r.selector)) continue
      const sinks = /box-shadow[^;}]*inset/.test(r.body)
      const nudges = /transform:\s*translateY\(1px\)/.test(r.body)
      if (sinks !== nudges) {
        offenders.push(`${r.selector}: inset=${String(sinks)} translateY=${String(nudges)}`)
      }
    }
    expect(offenders).toEqual([])
  })

  it('does not replace a cream fill with a tint while it is pressed', () => {
    // The `background: var(--press-tint)` that used to be here is gone rather
    // than kept alongside the inset: the tint *replaces* the fill, so a cream
    // pill would drop its cream for the duration of the press and let the
    // viewer's dark bar show through — the one thing the absolute fill exists to
    // prevent.
    expect(rule('.btn-secondary:active:not(:disabled)')).not.toContain('--press-tint')
  })
})

// ---------------------------------------------------------------------------
// §3.5 — the segmented control is a track, not a box with dividers
// ---------------------------------------------------------------------------

describe('E-36 §3.5 — the seg divider is gone', () => {
  it('has no `.seg-opt + .seg-opt` rule', () => {
    // The options sit *in* a recessed track now, held off its edges by 3px of
    // padding and a 2px gap, each carrying its own radius. A divider between two
    // of them is the old bordered box coming back one line at a time. Comments
    // are stripped, so the sentence you are reading cannot satisfy or trip this.
    const offenders = BASE_RULES.map((r) => r.selector).filter((s) => /\.seg-opt\s*\+/.test(s))
    expect(offenders).toEqual([])
  })

  it('still has the track and the options it is meant to be about', () => {
    // Calibration for the assertion above: `.seg-opt + .seg-opt` is trivially
    // absent from a sheet that lost `.seg-opt` altogether.
    expect(decl('.seg', 'gap')).toBe('2px')
    expect(decl('.seg', 'padding')).toBe('3px')
    expect(decl('.seg-opt', 'border-radius')).toBe('var(--radius-lg)')
  })

  it('keeps the track clipping, because it was clipping elevation too', () => {
    // **This assertion used to say the opposite, and the reversal is the point.**
    //
    // `overflow: hidden` was removed with the border on ui-spec §2.3's ⟳ row,
    // which reasons that the clip existed to round the options into the box's
    // corners and the options no longer reach them. True, and incomplete: the
    // declaration was clipping the *lift* as well. The selected option carries
    // `--shadow-control-raised`, whose up-left lobe is `rgb(255 253 246 / 0.9)`
    // reaching 11px (3px offset + 8px blur), while the track holds it off by 3px
    // of padding — so unclipped, a near-white halo paints 8px out onto whatever
    // the track sits on. Measured in a real Chrome render on the viewer's dark
    // bar: **3.29:1**, i.e. over the 3:1 floor and therefore read as a shape, not
    // a glow. That is the "white outline around every card" `tokens.css` refuses
    // in its dark elevation block, arriving through the back door.
    //
    // `soft-ui.css` could not have known — it is light-only, and on cream the
    // same halo is invisible. E-36 §4's warning, a second time: a light-only
    // instruction moved into a scope the prototype never had. A stylesheet check
    // could not have known either; this one is here because a browser measured it.
    expect(decl('.seg', 'overflow')).toBe('hidden')
  })

  it('lifts the selected option with the *frozen* raised shadow', () => {
    // E-42 §3, and the one shadow in the sheet that falls on an absolute
    // surface: this lift lands on the cream track three pixels away, not on the
    // page. `--shadow-sm` would resolve to the dark block's form in a dark
    // scope — a `0 0 0 1px #3E5B57` hairline plus ambient black, which measures
    // 6.19 washed against the cream and therefore draws a hard teal box around
    // the option instead of lifting it.
    expect(rule(".seg-opt[data-checked='true']")).toContain('var(--shadow-control-raised)')
    expect(rule(".seg-opt[data-checked='true']")).not.toContain('var(--shadow-sm)')
  })
})

// ---------------------------------------------------------------------------
// §3.7 — the nine absolutes, and what each one is pinned to
// ---------------------------------------------------------------------------

describe('E-36 §3.7 / E-42 §3 — the control absolutes are pinned, not invented', () => {
  /**
   * Each frozen token beside the theme-relative token whose value it copies.
   *
   * "Absolute" is asserted in `tokens.test.ts` (`leaves the absolutes alone`):
   * present in the light block, absent from the dark one. That is necessary and
   * not sufficient — a token can be absolute and simply wrong. What makes
   * freezing legitimate is that the frozen value is *the light theme's value,
   * unchanged*, so a control in the dark app is the same cream a control in the
   * light app is. These pairs are that claim, checkable.
   *
   * `--control-fill-hover` (#F8F4EC) is the one with no partner: E-36 §4 counted
   * the prototype's hover cream zero times across both stylesheets, which is why
   * `.btn-secondary:hover` had nothing to move to. It is pinned below by value.
   */
  const PINNED: [string, Map<string, string>, string][] = [
    ['--control-fill', light, '--color-surface'],
    ['--control-well', light, '--fill-subtle'],
    ['--on-control', light, '--color-accent-800'],
    ['--on-control-accent', light, '--color-accent'],
    ['--on-control-dim', light, '--color-neutral-700'],
    ['--shadow-control-inset', light, '--shadow-inset'],
    ['--shadow-control-raised', light, '--shadow-sm'],
    // The odd one out, and on purpose: the accent is the single fill in this
    // palette that is dark in the *light* theme, so a recess cut into it is a
    // dark-ground recess in both themes. The light inset's up-left lobe is
    // rgb(255 253 246 / 0.85) — a cream smear across a teal button.
    ['--shadow-accent-inset', dark, '--shadow-inset'],
  ]

  it('copies each frozen token from the block it was frozen out of', () => {
    for (const [frozen, source, original] of PINNED) {
      const value = light.get(frozen)
      expect(value, `${frozen} is missing from the absolutes block`).toBeDefined()
      // Resolve one hop: `--fill-subtle` is `#EFE9DC` outright, but a source
      // token that pointed at a ramp step would otherwise compare unequal to the
      // literal that was pinned.
      const from = source.get(original) ?? ''
      const resolved = /^var\((--[\w-]+)\)$/.exec(from.trim())
      const expected = resolved === null ? from : (light.get(resolved[1] ?? '') ?? from)
      expect(value, `${frozen} has drifted from ${original}`).toBe(expected)
    }
  })

  it('writes them as literals — a var() reference would flip with the theme', () => {
    // This is the entire mechanism. `--shadow-control-inset: var(--shadow-inset)`
    // resolves in the scope it is *used* in, so a control in the viewer would
    // get the dark inset and the freeze would be decorative. Same for the fills:
    // `var(--color-surface)` is #2F4A46 under `[data-theme='dark']`.
    for (const [frozen] of PINNED) {
      expect(light.get(frozen), `${frozen} is a reference, not a literal`).not.toContain('var(')
    }
    expect(light.get('--control-fill-hover')).toBe('#F8F4EC')
    expect(light.get('--control-fill-hover')).not.toContain('var(')
  })

  it('pins values that genuinely move — otherwise there was nothing to freeze', () => {
    // The pairs above are only meaningful if the token each one copies really
    // does flip. If `--color-surface` stopped flipping, `--control-fill` would be
    // a duplicate rather than an absolute and every assertion here would still
    // be green.
    for (const original of ['--color-surface', '--fill-subtle', '--shadow-inset', '--shadow-sm']) {
      expect(dark.get(original), `${original} no longer flips`).toBeDefined()
      expect(dark.get(original), `${original} no longer flips`).not.toBe(light.get(original))
    }
    // The ramps are the exception and stay one: they are an absolute lightness
    // scale, so `--on-control` and `--on-control-dim` are pinned against
    // something that never moved. They are frozen anyway because the *semantic*
    // tokens that would otherwise be reached for do move — `--ink` is #EAE3D4 on
    // dark, which is 1.10 on this cream, and `--accent-text` is #9BC3C1, 1.65.
    for (const ramp of ['--color-accent-800', '--color-neutral-700']) {
      expect(dark.has(ramp), `${ramp} is a ramp step and must not be redeclared`).toBe(false)
    }
  })

  it('states the shadow rule as a closed set — three frozen, one flipping family', () => {
    // E-42 §3's table, as an exhaustiveness check. The failure it forecloses is
    // a fifth frozen shadow appearing with no ruling behind it, or one of these
    // four quietly growing a dark counterpart (which would end its absoluteness
    // — `tokens.test.ts` holds that line for all nine).
    const frozenShadows = [...light.keys()].filter((k) => /^--shadow-(control|accent)-/.test(k))
    expect(frozenShadows.sort()).toEqual([
      '--shadow-accent-inset',
      '--shadow-control-inset',
      '--shadow-control-raised',
    ])
  })
})

// ---------------------------------------------------------------------------
// The rows the rulings declined
//
// E-36 §2's diagnosis, in one sentence: **an un-recorded non-adoption is what
// caused this.** Thirty rules went missing and nothing said whether each was a
// decision or an oversight, so the next reader could only guess — and guessed
// that the old spec was current.
//
// E-42 §5 answers that for the four rows it declined, in prose. Prose is what
// E-36 §2 already showed to be losable: `ui-spec` §0.2 outlived the ruling that
// retired it, and `Card.tsx` cited it in good faith. So each declined row is
// pinned here too, in the direction that *fails when someone adopts it later* —
// which is the only direction that matters, because the prototype is still on
// disk and copying a line out of it looks like fixing a gap.
// ---------------------------------------------------------------------------

describe('E-42 §5 — the declined rows stay declined', () => {
  it('keeps .btn at font-weight 800 (row ▪3 declined)', () => {
    // The prototype says 600. ui-spec §2.3's `.btn` row is one of the ones the
    // 10th session did *not* revise, button labels are the heading family, and
    // E-32 kept 800. `.tag`'s 600 is a different row and *is* adopted, so the
    // two are asserted together — a blanket "weights match the prototype" would
    // be wrong in one direction and a blanket "they do not" wrong in the other.
    expect(decl('.btn', 'font-weight')).toBe('800')
    expect(decl('.tag', 'font-weight')).toBe('600')
  })

  it('keeps .seg-opt at 7px 12px (row ▪28 declined — 5px was never measured)', () => {
    // ui-spec §2.3's own ⟳ row instructs the product's padding be kept *until*
    // the prototype's is measured to still clear the touch target. 768–1023 is
    // the tier `--touch-min` resolves to 0 on and the tier most likely to be a
    // tablet; 4px off the height there is a change to a hit area.
    expect(decl('.seg-opt', 'padding')).toBe('7px 12px')
  })

  it('keeps .tag-neutral on the 100/800 ramp pair (row ⬛18: only the shadow was missing)', () => {
    // The prototype paints 200/700. What row 18 was actually missing is the
    // *shadow* — ui-spec §2.3 says so — and the shipped pair is measured at
    // 10.68 with a dark counterpart that swaps its ends. A measured pair is not
    // traded for an unmeasured one.
    expect(decl('.tag-neutral', 'background')).toBe('var(--color-neutral-100)')
    expect(decl('.tag-neutral', 'color')).toBe('var(--color-neutral-800)')
  })

  it('blurs the dialog backdrop but keeps the scrim alpha (rows ▪32·33, half declined)', () => {
    // The blur is adopted; the prototype's 0.5 → 0.34 is not. `--scrim-modal` is
    // not a dialog-only token — `.drawer-backdrop` shares it, and the drawer has
    // no blur to make up the difference because a full-viewport blur on a phone
    // is the most expensive thing on the frame. The prototype is desktop-only
    // (ui-spec §0.5) and has no drawer, so it never answered this question.
    const backdrop = rule('.dialog-backdrop')
    expect(backdrop).toMatch(/backdrop-filter:\s*blur\(2px\)/)
    expect(backdrop).toMatch(/-webkit-backdrop-filter:\s*blur\(2px\)/)
    expect(light.get('--scrim-modal')).toBe('rgb(38 59 56 / 0.5)')
    expect(dark.get('--scrim-modal')).toBe('rgb(0 0 0 / 0.6)')
    // ...and the drawer keeps a plain scrim, which is the reason the alpha stayed.
    expect(rule('.drawer-backdrop')).not.toContain('backdrop-filter')
  })
})
