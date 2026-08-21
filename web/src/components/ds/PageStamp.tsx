import type { ReactNode } from 'react'

import { cn } from '../../lib/cn'

/**
 * The read-position stamp — `10 / 214p` inside a tilted 인주 rule (E-46).
 *
 * The 이어보기 card used to print its counter as plain accent text. In the 서고
 * skin the card is a filing-card, and the one number on it that says *where the
 * reader stopped* is stamped rather than typed: a 1.5px rule, five degrees off
 * square, ink and rule in the same accent as the seal on a finished cover
 * (`DoneSeal.tsx`), so the two marks read as one family.
 *
 * `font-ui` is not a preference. 명조 numerals are proportional, so `157 / 192p`
 * drawn in the body face does not line up with the same stamp on the next card —
 * the prototype hands every numeral to the sans and `tokens.css` states the rule
 * (`--font-ui`). `tabular-nums` then holds the digits to one width as the page
 * advances, which is what stops the stamp twitching under a reader who is
 * turning pages.
 *
 * The ink is `--accent-text` for the reason `DoneSeal` states: 11px type over
 * the card's kraft ground, where the base accent does not clear AA and
 * accent-700 does.
 */
export interface PageStampProps {
  children: ReactNode
  className?: string
}

export function PageStamp({ children, className }: PageStampProps) {
  return (
    <span
      className={cn(
        'inline-block -rotate-[5deg] rounded-sm border-[1.5px] border-accent-text px-[7px] py-[3px] font-ui text-xs font-bold tabular-nums text-accent-text',
        className,
      )}
    >
      {children}
    </span>
  )
}
