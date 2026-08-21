import { cn } from '../../lib/cn'
import { VisuallyHidden } from './VisuallyHidden'

/**
 * The product wordmark — the seal, the name, and the descriptor.
 *
 * One component rather than three copies, because the mark is the only place in
 * the interface where the accent runs as a field at more than a few pixels
 * (ui-spec §2.5) and the three screens that show it must not drift into three
 * different reds, three different weights, or three different names.
 *
 * ---------------------------------------------------------------------------
 * E-46 — the mark is a 낙관, not a shelf
 * ---------------------------------------------------------------------------
 * It was five uneven bars on a raised card: books standing at different heights
 * with one pulled forward, which is what a *shelf* looks like. The 서고 skin
 * replaces it with the collector's seal the prototype draws — a square outlined
 * in 인주, the 藏 of 소장 inside it, tilted four degrees off square the way a
 * stamp pressed by hand sits. The subject moves from the furniture to the act:
 * this is a thing somebody collected, and the seal is how that has been said on
 * a Korean book for centuries.
 *
 * **The tilt is `rotate(-4deg)` and it is the mark, not an effect.** A seal
 * printed exactly square reads as a logo; four degrees off reads as ink pressed
 * onto paper, which is the whole difference the skin is about.
 *
 * **藏 costs 2 148 bytes and is vendored for this one glyph.** 고운바탕 carries
 * no 한자 at all, so without that subset the mark is a tofu box on any machine
 * with no CJK serif installed — a brand element that disappears on someone
 * else's computer. fonts.css has the derivation.
 *
 * The name is Korean now (석교만화방) and the descriptor beneath stays in the
 * design's English, so the two read as one lockup rather than a translation of
 * each other. No negative tracking anywhere in the lockup: 명조 loses its
 * strokes when the letters are pulled together, which is why E-46 pins
 * `letter-spacing: 0` on every heading.
 */

/** The product name. The one string; never re-typed at a call site. */
export const BRAND_NAME = '석교만화방'
/** What it is, under the name. */
export const BRAND_TAGLINE = 'Comic Archive Reader'
/** The onboarding/login lockup adds where the library lives. */
export const BRAND_TAGLINE_LONG = 'Comic Archive Reader · Local Library'
/** The glyph in the seal — 藏, "to keep / to collect". */
export const SEAL_GLYPH = '藏'

interface MarkProps {
  hero: boolean
}

/**
 * The seal itself. Sized from the prototype: 64px square with a 2px rule and a
 * 34px glyph at hero, 30px with a 1.5px rule and a 15px glyph compact.
 *
 * `aria-hidden`, because the seal carries no text a reader needs — the name
 * beside it is what a screen reader announces, and when the name is hidden (the
 * icon rail) `VisuallyHidden` puts it back. A logo with no accessible name is a
 * navigation landmark that announces nothing.
 */
function Mark({ hero }: MarkProps) {
  return (
    <span
      aria-hidden="true"
      className={cn(
        // `accent-text`, not `accent`. The prototype is light-only and draws the
        // seal in the base accent; on the dark ground E-46 derives, that same
        // red measures **2.15** — the mark all but disappears at night, which
        // is a case the prototype never had to answer. `--accent-text` is the
        // token that exists for precisely this ("the accent when it is ink,
        // flips with the theme") and it reads 6.18 on the light ground and
        // 6.00 on the dark one. The e2e contrast sweep is what found it.
        //
        // In light this deepens the seal from accent-500 to accent-700. That is
        // a visible deviation from the prototype and it is the right one twice
        // over: the prototype's own value is 4.33 washed, under AA, and 인주
        // pressed into paper is a deeper red than the ink in the pad.
        'grid flex-none place-items-center border-accent-text font-seal font-bold text-accent-text',
        hero
          ? 'size-16 rounded-md border-2 text-[34px]'
          : 'size-[30px] rounded-sm border-[1.5px] text-[15px]',
      )}
      // The tilt is the mark. Kept inline rather than given a utility: there is
      // exactly one element in the product that is rotated, and a one-off
      // rotation utility would invite a second.
      style={{ transform: 'rotate(-4deg)' }}
    >
      {SEAL_GLYPH}
    </span>
  )
}

export interface WordmarkProps {
  /**
   * `hero` — onboarding and login, 38px name.
   * `compact` — the 240px sidebar, 15px name.
   * `mark` — the 68px rail: the seal only, name for assistive tech only.
   */
  variant?: 'hero' | 'compact' | 'mark'
  className?: string
}

export function Wordmark({ variant = 'compact', className }: WordmarkProps) {
  const hero = variant === 'hero'
  const markOnly = variant === 'mark'

  return (
    <span
      data-role="wordmark"
      className={cn('flex items-center', hero ? 'gap-3' : 'gap-2', className)}
    >
      <Mark hero={hero} />
      {markOnly ? (
        <VisuallyHidden>{BRAND_NAME}</VisuallyHidden>
      ) : (
        <span className="flex min-w-0 flex-col leading-none">
          <span
            className={cn(
              'font-heading font-bold tracking-normal',
              hero ? 'text-[38px] leading-[1.15]' : 'whitespace-nowrap text-[15px]',
            )}
          >
            {BRAND_NAME}
          </span>
          <span
            className={cn(
              'font-ui uppercase text-ink-dim',
              hero ? 'mt-2 text-3xs tracking-[.2em]' : 'mt-[3px] text-2xs tracking-[.16em]',
            )}
          >
            {hero ? BRAND_TAGLINE_LONG : BRAND_TAGLINE}
          </span>
        </span>
      )}
    </span>
  )
}
