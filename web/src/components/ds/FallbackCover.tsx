import { cn } from '../../lib/cn'
import { formatLabel, type FormatValue } from '../../lib/format'
import { textLang } from '../../lib/textLang'

/**
 * `FallbackCover` (ui-spec §9 #7) — FR-LIB-008's text placeholder.
 *
 * This is **always rendered beneath the real cover**, never swapped in when one
 * fails. That is the whole point: the striped block occupies the exact final
 * geometry from the first paint, so a cover arriving late (or a `202` while the
 * thumbnail is still queued) fades in over it with **no layout shift** (UI-5.3,
 * and the zero-CLS assertion in WP-09).
 *
 * Stripe pitch is 16px on cards and heroes, 10px on the 24×36 list-row thumb —
 * a 16px pitch inside a 24px box is a single diagonal, which reads as an
 * artefact rather than a pattern.
 */
export interface FallbackCoverProps {
  title: string
  format: FormatValue
  size: 'card' | 'row' | 'hero'
  className?: string
}

export function FallbackCover({ title, format, size, className }: FallbackCoverProps) {
  const row = size === 'row'
  return (
    <div
      aria-hidden="true"
      className={cn(
        'absolute inset-0 flex flex-col justify-end',
        row ? 'fallback-cover-row' : 'fallback-cover p-2',
        className,
      )}
    >
      {!row && (
        <>
          <div className="mb-[5px] text-2xs uppercase tracking-[.1em] text-ink-dim">
            {`${formatLabel(format)} · NO THUMBNAIL`}
          </div>
          <div
            /* E-32 takes the *card* titles from 800 to 700 while the section
               headings stay at 800; this is the placeholder cover's title. */
            className="line-clamp-4 font-heading text-sm font-bold leading-[1.15] text-ink"
            style={{ fontSize: size === 'hero' ? '15px' : '12px' }}
            lang={textLang(title)}
          >
            {title}
          </div>
        </>
      )}
    </div>
  )
}
