import { cn } from '../../lib/cn'
import { VisuallyHidden } from './VisuallyHidden'

/**
 * The product wordmark — the bar mark, the name, and the descriptor.
 *
 * One component rather than three copies, because the mark is the only place in
 * the interface where the accent runs as a solid field at more than a few
 * pixels (ui-spec §2.5) and the three screens that show it must not drift into
 * three different reds, three different weights, or three different names.
 *
 * **The bars are a picture of a shelf, not decoration.** Uneven heights with the
 * third one accented: books standing at different heights with one pulled
 * forward. They carry no text, so they are `aria-hidden` and the name beside
 * them is what a screen reader reads. When the name is hidden — the 68px rail —
 * `VisuallyHidden` puts it back, because a logo with no accessible name is a
 * navigation landmark that announces nothing.
 *
 * **Latin, in a Korean interface, deliberately.** The name is a proper noun and
 * is spelled the way the design file spells it; the descriptor beneath is the
 * part that says what the thing *is*, and stays in the design's English so the
 * two read as one lockup rather than a translation of each other.
 */

/** The product name. The one string; never re-typed at a call site. */
export const BRAND_NAME = 'Oh My Manhwa-bbang'
/** What it is, under the name. */
export const BRAND_TAGLINE = 'Comic Archive Reader'
/** The onboarding/login lockup adds where the library lives. */
export const BRAND_TAGLINE_LONG = 'Comic Archive Reader · Local Library'

/** Bar heights in px; index 2 is the accented one. Hero, then compact. */
const HERO_BARS = [48, 34, 48, 26, 40] as const
const COMPACT_BARS = [20, 14, 20, 11] as const
const ACCENT_BAR = 2

interface MarkProps {
  hero: boolean
}

function Mark({ hero }: MarkProps) {
  const bars = hero ? HERO_BARS : COMPACT_BARS
  return (
    <span
      aria-hidden="true"
      className={cn('flex flex-none items-end', hero ? 'h-[48px] gap-1' : 'h-[20px] gap-[2px]')}
    >
      {bars.map((height, i) => (
        <span
          // The bars are a fixed decorative sequence, so the position *is* the
          // identity — there is nothing else about a bar to key on.
          key={i}
          className={cn('block', hero ? 'w-[9px]' : 'w-1', i === ACCENT_BAR ? 'bg-accent' : 'bg-ink')}
          style={{ height }}
        />
      ))}
    </span>
  )
}

export interface WordmarkProps {
  /**
   * `hero` — onboarding and login, 38px name.
   * `compact` — the 240px sidebar, 15px name.
   * `mark` — the 68px rail: bars only, name for assistive tech only.
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
              'font-heading font-extrabold',
              hero
                ? 'text-[38px] leading-[1.05] tracking-[-.02em]'
                : 'whitespace-nowrap text-lg tracking-[-.01em]',
            )}
          >
            {BRAND_NAME}
          </span>
          <span
            className={cn(
              'uppercase text-ink-dim',
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
