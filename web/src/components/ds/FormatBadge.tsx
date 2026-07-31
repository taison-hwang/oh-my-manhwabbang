import { cn } from '../../lib/cn'
import { formatLabel, type FormatValue } from '../../lib/format'
import { Tag } from './Tag'

/**
 * `FormatBadge` (ui-spec §9 #6, FR-LIB-009).
 *
 * Two presentations of one fact:
 *  - `corner` — the ink field pinned to the top-left of a cover;
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
          'absolute left-0 top-0 bg-ink px-[6px] py-[2px] text-2xs tracking-[.08em] text-bg',
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
