import { cn } from '../../lib/cn'
import { formatLabel, type FormatValue } from '../../lib/format'
import { Tag } from './Tag'

/**
 * `FormatBadge` (ui-spec §9 #6, FR-LIB-009).
 *
 * Two presentations of one fact:
 *  - `corner` — a **pill inset 8px** from the top-left of a cover (E-32; it used
 *    to be a hard-cornered ink field flush into the 0,0 corner). The prototype
 *    fills it `rgba(246,242,233,.9)`, i.e. `--on-accent` at 90 %; there is no
 *    token for that alpha, so it takes the opaque `--color-surface` it is a
 *    translucent version of, and the inset plus `--shadow-sm` is what lifts it
 *    off the artwork instead.
 *
 *    Its label is `--accent-text`, not `--color-accent-800`. The ramps are a
 *    theme-invariant absolute lightness scale (ui-spec §1.4), so accent-800 on
 *    the *surface* is 11.62:1 on the cream one and **1.40:1** on the dark one —
 *    a ZIP badge that is legible in one theme and a smudge in the other.
 *    `--accent-text` is the accent as ink and flips with the theme: 9.23 / 5.01,
 *    8.83 / 4.84 once the paper grain is on them;
 *  - `tag` — the list column's `.tag`, whose tone encodes the format:
 *    ZIP → neutral, FOLDER → accent, PDF → outline.
 *
 * Accepts both wire vocabularies (C-4): a *series* is `folder`, a *book* inside
 * it is `dir`, and both print `FOLDER`.
 */
export interface FormatBadgeProps {
  format: FormatValue
  variant: 'corner' | 'tag'
  className?: string
}

export function FormatBadge({ format, variant, className }: FormatBadgeProps) {
  const label = formatLabel(format)

  if (variant === 'corner') {
    return (
      <span
        className={cn(
          'absolute left-2 top-2 rounded-full bg-surface px-2 py-[3px] text-2xs font-semibold tracking-[.06em] text-accent-text shadow-sm',
          className,
        )}
      >
        {label}
      </span>
    )
  }

  const tone = format === 'pdf' ? 'outline' : format === 'zip' ? 'neutral' : 'accent'
  return (
    <Tag tone={tone} className={className}>
      {label}
    </Tag>
  )
}
