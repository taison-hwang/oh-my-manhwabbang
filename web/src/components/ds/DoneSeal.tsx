import { cn } from '../../lib/cn'
import { VisuallyHidden } from './VisuallyHidden'

/**
 * The 완독 seal — the 서고 skin's replacement for the 완독 pill (E-46, prototype
 * `만화방 v3 서고`).
 *
 * A finished book is not labelled, it is **stamped**: a square of 인주 rule with
 * 完讀 inside it, tilted nine degrees off square the way a hand-pressed 낙관
 * lands. It is the same move the wordmark's 藏 makes (`Wordmark.tsx`), one size
 * down and in the opposite corner from the format badge.
 *
 * Three things are deliberate and none of them is decoration:
 *
 *  - **the glyphs are vendored.** 고운바탕 carries no Han, so 完讀 would be two
 *    tofu boxes on a machine with no CJK serif — 3 360 bytes of Noto Serif CJK
 *    KR Bold, subsetted to these two characters, behind a `unicode-range`
 *    (`fonts.css`). A mark the product draws itself does not get to fall back;
 *  - **the accessible name is Korean.** The Han is `aria-hidden` and 완독 sits
 *    in the DOM beside it, so the stamp announces the same word the pill did and
 *    nothing in the product is legible only to a reader who knows the hanja;
 *  - **the ink is `--accent-text`, not `--color-accent`.** The seal is 12px type
 *    and the base accent is 4.33 on this ground where accent-700 is 7.41 — the
 *    same measured refusal `tokens.css` makes of the prototype's own colours
 *    (E-32 §4). The rule around it takes the same ink so the two agree, and on
 *    the dark theme both flip to accent-300 together; `--color-accent` there is
 *    1.28 on the surface, i.e. a stamp nobody can see.
 *
 * The fill is opaque `--surface` where the prototype washes its cream at 55 %.
 * A translucent fill would need an alpha the token layer does not carry (the
 * Tailwind colours are bare `var(…)`, so `/55` cannot compose), and an opaque
 * stamp on hatched paper is the same reading — ink pressed onto a card that was
 * laid on the cover — for a colour that stays in the token layer.
 */

/** 完讀 — the two Han glyphs vendored for this mark (`fonts.css`). */
export const DONE_SEAL_GLYPHS = '完讀'

export interface DoneSealProps {
  className?: string
}

export function DoneSeal({ className }: DoneSealProps) {
  return (
    <span
      className={cn(
        'pointer-events-none grid h-[38px] w-[38px] -rotate-[9deg] place-items-center rounded-sm border-2 border-accent-text bg-surface text-center font-seal text-sm font-bold leading-[1.05] text-accent-text',
        className,
      )}
    >
      <span aria-hidden="true">{DONE_SEAL_GLYPHS}</span>
      <VisuallyHidden>완독</VisuallyHidden>
    </span>
  )
}
