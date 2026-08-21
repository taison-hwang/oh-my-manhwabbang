/**
 * The contrast scanner that reads **rendered pixels** — items `v` and `ar`.
 *
 * ## Why a second scanner exists
 *
 * `tokens.test.ts` measures colour pairs out of `tokens.css`. It is exact about
 * what it looks at and it cannot look at anything else: it reads source strings,
 * so it knows which token is declared where and has to *assume* which ground the
 * ink lands on. Five shapes live in that gap, and §5.7.10 (`ar`) records that
 * three of them turned up as **real defects** in one round:
 *
 *   ⓐ a shadow that changes the ground's actual pixels — the ⌘K chip declares
 *     5.65 and renders 4.55;
 *   ⓑ ink the user agent paints, which no stylesheet names — the search field's
 *     `::-webkit-search-cancel-button` renders 1.14;
 *   ⓒ a sibling laid over a control, painting into its box — the two marks in
 *     `TopBar`;
 *   ⓓ an ancestor between a chip and its bar that opens a stacking context
 *     (`isolate`, `transform`, `opacity < 1`, `filter`), which silently kills
 *     the grain exemption E-43 depends on — a source scan does not read
 *     ancestors;
 *   ⓔ inline `style` and className computed at runtime.
 *
 * ⓔ needs computed style. ⓓ needs computed style *up the chain*. ⓐⓑⓒ cannot be
 * reached from any style object at all — they are properties of the composite,
 * so the only instrument that sees them is the rendered bitmap. This module runs
 * both layers and keeps them attributable: the bitmap says a pair is failing,
 * the computed layer says which token to move.
 *
 * ## How the bitmap is read
 *
 * `locator.screenshot()` captures the page and clips to the element, so what
 * comes back is the real composite — shadows, the paper grain, UA-painted
 * glyphs, and anything a sibling drew on top. The PNG is handed back **into the
 * page** and decoded with `createImageBitmap` + `OffscreenCanvas`: the browser
 * already owns a correct PNG decoder, and the alternative is a decoder in this
 * repository that would need its own tests to be worth trusting.
 *
 * The analysis runs in the page too, and returns a summary rather than pixels —
 * a 200×60 box at DPR 2 is 12 000 RGBA values and there is no reason to move
 * them across the bridge.
 *
 *   * **ground** is the modal colour of the box. On any control this is the
 *     field, and it is the number ⓐ and ⓓ move.
 *   * **clusters** are the other colours populated enough to be a mark rather
 *     than antialiasing. The glyph core of any ink in the box is one of these,
 *     which is how ⓑ and ⓒ become visible: neither is named anywhere, but both
 *     put pixels on the screen.
 *
 * A cluster floor of 24 pixels **or** 0.2 % of the box, whichever is larger, is
 * what separates a mark from an antialiased edge. It is a threshold and it is
 * therefore a place this scanner can be wrong; `contrast.spec` calibrates it
 * against a known-good and a known-bad pair rather than trusting the number.
 */

import { type Locator, type Page } from '@playwright/test'
import {
  contrast,
  contrastOf,
  floorFor,
  parseColour,
  type Rgba,
} from '../src/styles/contrast'

/**
 * How far the ground the reader gets may drift from the one CSS declares before
 * the measurement stops being about the token layer at all.
 *
 * The whole point of this scanner is that the two can differ — a shadow, a dead
 * grain exemption and an overlaid sibling all move the real ground while the
 * stylesheet says nothing. But *how* they differ matters: those three shade the
 * declared surface and the measured value stays a version of it. An element
 * whose box lies over a generated cover is a different case, and reporting
 * `--ink-dim on a green` as an AA failure of the token layer is a check
 * watching the wrong thing.
 *
 * The distance is the **largest per-channel difference**, not the contrast
 * ratio, and the first cut of this got that wrong in a way worth recording:
 * contrast is a function of luminance alone, so it read the declared
 * `--fill-track` (213, 196, 164) and a generated cover's yellow-green
 * (159, 192, 63) as *nearly the same ground* — 101 units apart on the blue
 * channel and barely apart in luminance. A hue change is exactly what
 * distinguishes "artwork" from "a shadow on the declared surface", and
 * luminance is the one property that cannot see it.
 *
 * 40 is where "a shade of the declared surface" stops. It is derived rather
 * than tuned: the paper grain's own peak excursion is
 * `--paper-intensity × 0.115 × |ground − tone|`, which on the lightest surface
 * in this palette is about **23 of 255**, and a shadow lobe adds a few more.
 * The observed cover cases are 54–101 on at least one channel, so the band
 * between is empty.
 */
export const GROUND_DRIFT_LIMIT = 40

/** A colour that occupies enough of a box to be something the reader sees. */
export interface Cluster {
  /** `rgb(r, g, b)`, the spelling `getComputedStyle` uses. */
  rgb: string
  count: number
  /** Fraction of the box's pixels. */
  share: number
  /** Contrast against the measured ground. */
  ratio: number
}

export interface Measurement {
  label: string
  /** `color`, as computed — inline styles and runtime classNames included (ⓔ). */
  declaredInk: string
  /** The nearest ancestor background that is not transparent. */
  declaredGround: string
  /** The pair the unit tier can already see. */
  declaredRatio: number
  /** The modal pixel of the box: the ground the reader actually gets. */
  measuredGround: string
  /** Declared ink over the measured ground. ⓐ and ⓓ live in this gap. */
  measuredRatio: number
  /** Every populated non-ground colour, worst contrast first. ⓑ and ⓒ live here. */
  clusters: Cluster[]
  fontSizePx: number
  fontWeight: number
  /** The AA floor this element's type size earns. */
  floor: number
  /** Ancestors that open a stacking context between the element and its ground. */
  isolatingAncestors: string[]
  /** Largest per-channel distance between the declared ground and the measured one. */
  groundDrift: number
  /** True when the box is dominated by something that is not the declared surface. */
  overImagery: boolean
  width: number
  height: number
}

/** One distinct (ink, ground, size, weight) combination, and where to find it. */
export interface Combination {
  key: string
  label: string
  declaredInk: string
  declaredGround: string
  fontSizePx: number
  fontWeight: number
  isolatingAncestors: string[]
  /**
   * The value stamped into `data-contrast-probe` on the representative element.
   *
   * A probe attribute rather than an assembled selector. Walking up the tree
   * building `:nth-of-type` chains gives a selector that is *usually* unique,
   * and a scanner that photographs the wrong element when it is not would
   * report a ratio for a box nobody asked about — the precise failure this
   * file exists to catch, committed by the file itself. The attribute is
   * removed by `clearProbes` and paints nothing while it is there.
   */
  probe: string
}

/**
 * Every distinct ink/ground/size/weight combination painted on the page, with
 * one representative element each.
 *
 * Enumerating *combinations* rather than nodes is deliberate. A library screen
 * has hundreds of text nodes and a handful of distinct pairs; measuring each
 * node would be the same assertion hundreds of times and slow enough that the
 * spec would end up sampling, which is how coverage claims stop being true.
 * Grouping makes the sweep exhaustive over the thing that can actually fail.
 */
export async function combinationsOn(page: Page): Promise<Combination[]> {
  return page.evaluate(() => {
    const STACKING = (s: CSSStyleDeclaration): string | null => {
      if (s.isolation === 'isolate') return 'isolation:isolate'
      if (s.transform !== 'none') return `transform:${s.transform}`
      if (s.filter !== 'none') return `filter:${s.filter}`
      if (s.mixBlendMode !== 'normal') return `mix-blend-mode:${s.mixBlendMode}`
      if (s.opacity !== '' && Number(s.opacity) < 1) return `opacity:${s.opacity}`
      if (s.willChange !== 'auto' && s.willChange !== '') return `will-change:${s.willChange}`
      if (s.contain !== 'none' && s.contain !== '') return `contain:${s.contain}`
      return null
    }

    const opaque = (c: string): boolean =>
      c !== '' && c !== 'transparent' && !/rgba\([^)]*,\s*0\s*\)$/.test(c)


    /** A human-readable name: the role attribute if there is one, else the class. */
    const labelFor = (el: Element): string => {
      const role = el.getAttribute('data-role') ?? el.getAttribute('data-testid')
      if (role !== null && role !== '') return role
      const cls = (el.getAttribute('class') ?? '').split(/\s+/).filter(Boolean).slice(0, 2)
      return `${el.tagName.toLowerCase()}${cls.map((c) => `.${c}`).join('')}`
    }

    interface Found {
      key: string
      label: string
      declaredInk: string
      declaredGround: string
      fontSizePx: number
      fontWeight: number
      isolatingAncestors: string[]
      probe: string
      el: Element
    }
    const out = new Map<string, Found>()

    for (const el of document.querySelectorAll<HTMLElement>('body *')) {
      // Direct text only: a wrapper inherits its child's ink and would enter the
      // sweep as a duplicate with a bigger, emptier box.
      const text = [...el.childNodes]
        .filter((n) => n.nodeType === Node.TEXT_NODE)
        .map((n) => n.textContent ?? '')
        .join('')
        .trim()
      if (text === '') continue

      const style = getComputedStyle(el)
      if (style.visibility === 'hidden' || style.display === 'none') continue
      // Opacity and visibility have to be read **up the chain**, not on the
      // element. A series card's hover overlay is laid out at all times and
      // faded in by an ancestor's `opacity`, so its buttons report
      // `opacity: 1` while nothing of them is on screen — and a screenshot of
      // one is a photograph of the cover art behind it. The first run of this
      // sweep reported `.btn-primary` as cream ink on a **green** ground at
      // 2.83, which is a picture of a gradient thumbnail and not a defect.
      let hidden = false
      for (let a: HTMLElement | null = el; a !== null; a = a.parentElement) {
        const as = getComputedStyle(a)
        if (as.visibility === 'hidden' || as.display === 'none') { hidden = true; break }
        if (as.opacity !== '' && Number(as.opacity) === 0) { hidden = true; break }
      }
      if (hidden) continue
      const box = el.getBoundingClientRect()
      if (box.width < 4 || box.height < 4) continue
      // Off-screen: nothing to photograph.
      if (box.bottom < 0 || box.right < 0) continue
      if (box.top > window.innerHeight || box.left > window.innerWidth) continue

      // **Occlusion.** An element behind an open dialog is laid out, visible by
      // every computed-style test, and photographed as the dialog on top of it.
      // The settings sweep reported fourteen labels as "over artwork" for
      // exactly this reason — they were the library underneath.
      //
      // `elementFromPoint` at the box's centre is the browser's own answer to
      // "what would a click here hit", which is the same question as "what does
      // the reader see here". Descendants count: a label whose own text node is
      // wrapped in a `<span>` returns the span, and that is still this label.
      const cx = Math.round(box.left + box.width / 2)
      const cy = Math.round(box.top + box.height / 2)
      const top = document.elementFromPoint(cx, cy)
      if (top === null || !(el === top || el.contains(top))) continue

      // Walk to the nearest painted ancestor, noting anything that isolates on
      // the way — that walk is blind spot ⓓ.
      let ground = ''
      const isolating: string[] = []
      let node: HTMLElement | null = el
      while (node !== null) {
        const s = getComputedStyle(node)
        if (node !== el) {
          const why = STACKING(s)
          if (why !== null) isolating.push(`${labelFor(node)} {${why}}`)
        }
        if (opaque(s.backgroundColor)) {
          ground = s.backgroundColor
          break
        }
        node = node.parentElement
      }
      if (ground === '') ground = getComputedStyle(document.body).backgroundColor

      const fontSizePx = Number.parseFloat(style.fontSize)
      const fontWeight = Number(style.fontWeight === 'normal' ? '400' : style.fontWeight === 'bold' ? '700' : style.fontWeight)
      const key = `${style.color}|${ground}|${String(fontSizePx)}|${String(fontWeight)}|${isolating.join(',')}`
      if (out.has(key)) continue
      out.set(key, {
        key,
        label: labelFor(el),
        declaredInk: style.color,
        declaredGround: ground,
        fontSizePx,
        fontWeight,
        isolatingAncestors: isolating,
        probe: '',
        el,
      })
    }
    const list = [...out.values()]
    for (const [i, combo] of list.entries()) {
      const probe = `c${String(i)}`
      combo.el.setAttribute('data-contrast-probe', probe)
      combo.probe = probe
    }
    // The element reference is the one field that must not cross the bridge:
    // `page.evaluate` structured-clones its return value and a DOM node is not
    // cloneable, so returning it throws rather than degrading.
    return list.map((combo) => ({
      key: combo.key,
      label: combo.label,
      declaredInk: combo.declaredInk,
      declaredGround: combo.declaredGround,
      fontSizePx: combo.fontSizePx,
      fontWeight: combo.fontWeight,
      isolatingAncestors: combo.isolatingAncestors,
      probe: combo.probe,
    }))
  })
}

/** Takes the probe attributes back off. */
export async function clearProbes(page: Page): Promise<void> {
  await page.evaluate(() => {
    for (const el of document.querySelectorAll('[data-contrast-probe]')) {
      el.removeAttribute('data-contrast-probe')
    }
  })
}

/**
 * Takes the paper grain off for the duration of a shot, and puts it back.
 *
 * Only the cluster sweep uses this, and the reason is a limit of the
 * instrument rather than a convenience. The grain moves every pixel of a
 * surface by up to `--paper-intensity × peak alpha × |ground − tone|`, which on
 * the cream control is about **23 of 255** — wider than the gap between the
 * cream and a near-white mark. So on a grained bitmap a colour census cannot
 * tell a speckle from a very-low-contrast mark: the first run of this sweep
 * returned **92** "marks" on one search field, every one of them grain at a
 * ratio of 1.00–1.02.
 *
 * That is not a threshold that needs tuning, it is two signals occupying the
 * same range, so the split is made where it can be made honestly:
 *
 *   * this tier measures **which marks exist and what they are drawn on**, with
 *     the grain suppressed so a mark is unambiguous;
 *   * `tokens.test.ts` measures **what the grain does to a pair**, exactly,
 *     from the mask's own numbers rather than from a photograph of it.
 *
 * Neither is a weaker version of the other, and the check that reads the
 * *declared* ink on the *measured* ground — the one that catches a shadow or a
 * dead grain exemption — still runs with the grain fully on.
 */
async function withoutGrain<T>(page: Page, run: () => Promise<T>): Promise<T> {
  const handle = await page.addStyleTag({
    content: 'body::after, [data-role="viewer-top-bar"]::after { display: none !important }',
  })
  try {
    return await run()
  } finally {
    await handle.evaluate((node: Element) => {
      node.remove()
    })
  }
}

/** The bitmap half: the modal ground and every populated mark in a box. */
async function readBox(
  page: Page,
  locator: Locator,
): Promise<{
  width: number
  height: number
  total: number
  ground: string
  clusters: { rgb: string; count: number; share: number }[]
}> {
  // `animations: 'disabled'` is not tidiness — it is what makes this terminate.
  // Playwright holds an element screenshot until the box has been still for two
  // consecutive frames, and the shell has running animations (the scan
  // shimmer, the skeleton pulse) that never settle, so the default waits
  // forever on anything inside one. It also removes a second source of
  // nondeterminism from the measurement: a pair sampled mid-fade is a pair
  // nobody ever sees.
  //
  // The timeout is short and deliberate. This helper runs tens of times per
  // screen, and a hang here reads as "the suite is slow" rather than as a
  // defect — which is how a check ends up deleted for being flaky.
  const png = (
    await locator.screenshot({ animations: 'disabled', timeout: 5_000 })
  ).toString('base64')
  return page.evaluate(async (b64: string) => {
    // `atob` + a typed array, **not** `fetch('data:image/png;base64,…')`.
    // The product's CSP is `default-src 'self'` with no `connect-src` of its
    // own (arch §8.4), so fetching a data: URI from inside the page is refused
    // — and refused *loudly*, as a console error, which the suite's console
    // guard then fails the test on. Decoding the base64 in JavaScript touches
    // no fetch directive at all, and `createImageBitmap` on the resulting Blob
    // still hands the PNG to the browser's own decoder, which was the point of
    // doing this in the page rather than in Node.
    const binary = atob(b64)
    const bytes = new Uint8Array(binary.length)
    for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i)
    const bmp = await createImageBitmap(new Blob([bytes], { type: 'image/png' }))
    const canvas = new OffscreenCanvas(bmp.width, bmp.height)
    const ctx = canvas.getContext('2d', { willReadFrequently: true })
    if (ctx === null) throw new Error('no 2d context')
    ctx.drawImage(bmp, 0, 0)
    const { data } = ctx.getImageData(0, 0, bmp.width, bmp.height)

    const counts = new Map<number, number>()
    const total = bmp.width * bmp.height
    for (let i = 0; i < data.length; i += 4) {
      // Screenshots are opaque; alpha is carried for shape, not blending.
      const key = ((data[i] ?? 0) << 16) | ((data[i + 1] ?? 0) << 8) | (data[i + 2] ?? 0)
      counts.set(key, (counts.get(key) ?? 0) + 1)
    }
    const spell = (k: number): string =>
      `rgb(${String((k >> 16) & 255)}, ${String((k >> 8) & 255)}, ${String(k & 255)})`

    let groundKey = 0
    let groundCount = -1
    for (const [k, n] of counts) {
      if (n > groundCount) {
        groundCount = n
        groundKey = k
      }
    }
    // A mark, not an antialiased edge: 24 pixels or 0.2 % of the box.
    const floorCount = Math.max(24, Math.round(total * 0.002))
    const clusters: { rgb: string; count: number; share: number }[] = []
    for (const [k, n] of counts) {
      if (k === groundKey || n < floorCount) continue
      clusters.push({ rgb: spell(k), count: n, share: n / total })
    }
    return { width: bmp.width, height: bmp.height, total, ground: spell(groundKey), clusters }
  }, png)
}

/** Both layers, for one element. */
export async function measure(
  page: Page,
  combo: Combination,
  locator: Locator,
): Promise<Measurement> {
  const box = await readBox(page, locator)
  const ground: Rgba = parseColour(box.ground)
  const ink: Rgba = parseColour(combo.declaredInk)
  const clusters: Cluster[] = box.clusters
    .map((c) => ({ ...c, ratio: contrast(parseColour(c.rgb), ground) }))
    .sort((a, b) => a.ratio - b.ratio)
  const declared = parseColour(combo.declaredGround)
  const groundDrift = Math.max(
    Math.abs(declared.r - ground.r),
    Math.abs(declared.g - ground.g),
    Math.abs(declared.b - ground.b),
  )
  return {
    label: combo.label,
    declaredInk: combo.declaredInk,
    declaredGround: combo.declaredGround,
    declaredRatio: contrastOf(combo.declaredInk, combo.declaredGround),
    measuredGround: box.ground,
    measuredRatio: contrast(ink, ground),
    clusters,
    fontSizePx: combo.fontSizePx,
    fontWeight: combo.fontWeight,
    floor: floorFor(combo.fontSizePx, combo.fontWeight),
    isolatingAncestors: combo.isolatingAncestors,
    groundDrift,
    overImagery: groundDrift > GROUND_DRIFT_LIMIT,
    width: box.width,
    height: box.height,
  }
}

/**
 * Both layers for one element named directly, rather than one the sweep found.
 *
 * The sweep enumerates elements that carry their own text, which is right for
 * ink but wrong for a *control*: an `<input>` has a value, not a text node, and
 * the marks laid over it are siblings. The three defects §5.7.10 records under
 * `ar` all live on that shape — the search field's UA-painted ✕, the two marks
 * `TopBar` puts on the field, and the ⌘K chip whose ground a shadow moves — so
 * reaching them has to be deliberate. Which controls carry unnamed ink is a
 * judgement, and this entry point is where that judgement is spent.
 */
export async function measureLocator(
  page: Page,
  locator: Locator,
  label: string,
): Promise<Measurement> {
  const facts = await locator.evaluate((el: Element) => {
    const style = getComputedStyle(el)
    const opaque = (c: string): boolean =>
      c !== '' && c !== 'transparent' && !/rgba\([^)]*,\s*0\s*\)$/.test(c)
    let ground = ''
    let node: Element | null = el
    while (node !== null) {
      const s = getComputedStyle(node)
      if (opaque(s.backgroundColor)) {
        ground = s.backgroundColor
        break
      }
      node = node.parentElement
    }
    if (ground === '') ground = getComputedStyle(document.body).backgroundColor
    return {
      color: style.color,
      ground,
      fontSizePx: Number.parseFloat(style.fontSize),
      fontWeight: Number(
        style.fontWeight === 'normal' ? '400' : style.fontWeight === 'bold' ? '700' : style.fontWeight,
      ),
    }
  })
  return measure(
    page,
    {
      key: label,
      label,
      declaredInk: facts.color,
      declaredGround: facts.ground,
      fontSizePx: facts.fontSizePx,
      fontWeight: facts.fontWeight,
      isolatingAncestors: [],
      probe: '',
    },
    locator,
  )
}

/**
 * `measureLocator` with the grain suppressed — the entry point for the cluster
 * assertions. See `withoutGrain` for why the two measurements are split.
 */
export async function measureLocatorUngrained(
  page: Page,
  locator: Locator,
  label: string,
): Promise<Measurement> {
  return withoutGrain(page, () => measureLocator(page, locator, label))
}

/** A one-line account of a measurement, for an assertion message. */
export function describe(m: Measurement): string {
  const gap =
    Math.abs(m.declaredRatio - m.measuredRatio) < 0.005
      ? ''
      : ` (declared ${m.declaredRatio.toFixed(2)} on ${m.declaredGround}, so the paint moved it ${(m.measuredRatio - m.declaredRatio).toFixed(2)})`
  const iso =
    m.isolatingAncestors.length === 0
      ? ''
      : ` · isolated by ${m.isolatingAncestors.join(', ')}`
  return `${m.label}: ${m.declaredInk} on measured ${m.measuredGround} = ${m.measuredRatio.toFixed(2)}, floor ${String(m.floor)}${gap}${iso}`
}
