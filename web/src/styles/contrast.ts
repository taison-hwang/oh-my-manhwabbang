/**
 * WCAG 2.1 contrast, as one implementation two tiers share.
 *
 * This lived inline in `tokens.test.ts`, under a comment explaining that the
 * token layer may not grow a module. That reasoning was about the *product*:
 * `tokens.css` is the authority on colour and a runtime companion would give it
 * a rival. It is not an argument against a shared module for the **measuring**
 * side, and the moment a second tier had to measure — item `v`, the e2e-tier
 * scanner that reads real pixels — keeping the arithmetic inline stopped being
 * caution and started being a way for the two tiers to disagree quietly. The
 * unit tier would go on calling a pair 5.65 while the browser rendered 4.55 and
 * nothing would say which number was the formula and which was the paint.
 *
 * So: one formula, imported by both. `tokens.test.ts` keeps the calibration test
 * ("reproduces the four ratios E-32 measured") — that test is what makes this
 * module trustworthy, and it stays next to the rulings it reproduces.
 *
 * Nothing in `web/src` outside tests imports this; it is test support living
 * beside `cssRules.ts`, which is here for the same reason.
 */

export interface Rgba {
  r: number
  g: number
  b: number
  a: number
}

/**
 * `#rrggbb`, the space-separated `rgb(r g b / a)` the stylesheet uses, and the
 * comma-separated `rgb(r, g, b)` / `rgba(r, g, b, a)` that `getComputedStyle`
 * returns. The third form is why this is a module: the sheet and the browser
 * do not spell the same colour the same way, and the e2e tier only ever sees
 * the browser's spelling.
 *
 * `color(srgb …)` and the named colours are deliberately not handled — the
 * sheet uses neither, and a parser that guesses is worse than one that throws.
 */
export function parseColour(raw: string): Rgba {
  const value = raw.trim()
  const hex = /^#([0-9a-fA-F]{6})$/.exec(value)
  if (hex !== null) {
    const n = parseInt(hex[1] ?? '', 16)
    return { r: (n >> 16) & 255, g: (n >> 8) & 255, b: n & 255, a: 1 }
  }
  const spaced = /^rgba?\(\s*(\d+)\s+(\d+)\s+(\d+)\s*(?:\/\s*([\d.]+)\s*)?\)$/.exec(value)
  const commas = /^rgba?\(\s*(\d+)\s*,\s*(\d+)\s*,\s*(\d+)\s*(?:,\s*([\d.]+)\s*)?\)$/.exec(value)
  const fn = spaced ?? commas
  if (fn === null) throw new Error(`cannot parse colour ${raw}`)
  return {
    r: Number(fn[1]),
    g: Number(fn[2]),
    b: Number(fn[3]),
    a: fn[4] === undefined ? 1 : Number(fn[4]),
  }
}

/** WCAG 2.1 relative luminance. */
export function luminance({ r, g, b }: Rgba): number {
  const channel = (c: number): number => {
    const s = c / 255
    return s <= 0.04045 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4
  }
  return 0.2126 * channel(r) + 0.7152 * channel(g) + 0.0722 * channel(b)
}

/** Composites a possibly-translucent foreground onto an opaque ground. */
export function over(fg: Rgba, ground: Rgba): Rgba {
  return {
    r: fg.a * fg.r + (1 - fg.a) * ground.r,
    g: fg.a * fg.g + (1 - fg.a) * ground.g,
    b: fg.a * fg.b + (1 - fg.a) * ground.b,
    a: 1,
  }
}

/**
 * Contrast of `fg` seen on `ground`. The foreground is composited first, so a
 * translucent ink is measured as the reader gets it rather than as it is
 * written — which is the whole reason the paper wash could take three pairs
 * under AA while every declared pair still read as passing.
 */
export function contrast(fg: Rgba, ground: Rgba): number {
  const a = luminance(over(fg, ground))
  const b = luminance(ground)
  return (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05)
}

/** The same, from two strings either syntax. */
export function contrastOf(fg: string, ground: string): number {
  return contrast(parseColour(fg), parseColour(ground))
}

export const AA_NORMAL = 4.5
export const AA_LARGE = 3.0

/**
 * WCAG 2.1 §1.4.3's "large text": 18pt, or 14pt bold. In CSS px at 96dpi that
 * is 24px, or 18.66px at weight 700 and up.
 */
export function floorFor(fontSizePx: number, fontWeight: number): number {
  const large = fontSizePx >= 24 || (fontSizePx >= 18.66 && fontWeight >= 700)
  return large ? AA_LARGE : AA_NORMAL
}
