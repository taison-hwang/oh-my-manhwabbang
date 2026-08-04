import { cn } from '../../lib/cn'

/**
 * `ProgressBar` (ui-spec §9 #5, as reskinned by **E-32**).
 *
 * Three variables:
 *  - height. The 3–4px square bar becomes a 5–7px **pill**: 6px in rows and
 *    cards, 7px in the series-detail stat strip, 5px over artwork;
 *  - the trough is `--fill-track` normally and `--fill-track-2` when it sits on
 *    top of a cover, where the lighter step would disappear;
 *  - the fill is the accent, except a **completed** library row, where it turns
 *    to `--ink` so 완독 reads as "finished", not "still going" (ui-spec §4.5).
 *
 * ## Why the fill is `--accent-fill` and not `bg-accent`
 *
 * E-32 made the accent a deep teal. Against `--fill-track` in the
 * **dark** theme that is **1.09:1** — a progress bar that renders as an empty
 * trough at every value, on every screen that has one. `--accent-fill` is the
 * token for "the accent when it has to read against the ground": it stays
 * `--color-accent` on light (5.78 on the trough) and moves up the ramp to accent-300 on
 * dark (3.86). Nothing else in this file changes between themes.
 *
 * The prototype fills a *completed* bar with `--color-accent-300`, which E-32 §4
 * rejects at 1.38 on the trough; `--ink` is kept.
 *
 * The trough carries `--shadow-inset` and `overflow:hidden` — the recessed lobe
 * of the dual-light set, which is what makes the pill read as a channel the fill
 * sits inside rather than two stacked bars. The over-art variant does not: it is
 * 5px of translucent rail lying on a photograph, where an inset highlight is
 * just a light smear.
 */
export interface ProgressBarProps {
  /** 0..1. Clamped; values <= 0 still render the trough. */
  value: number
  /** 6 in rows and cards, 7 in the stat strip, 5 over artwork. */
  height?: 5 | 6 | 7
  track?: 'default' | 'over-art'
  tone?: 'accent' | 'done'
  className?: string
  /** Accessible name, e.g. the series title. */
  label?: string
}

/** Written out rather than interpolated: Tailwind scans source text. */
const HEIGHT_CLASS = {
  5: 'h-[5px]',
  6: 'h-[6px]',
  7: 'h-[7px]',
} as const

export function ProgressBar({
  value,
  height = 6,
  track = 'default',
  tone = 'accent',
  className,
  label,
}: ProgressBarProps) {
  const ratio = Number.isFinite(value) ? Math.max(0, Math.min(1, value)) : 0
  const pct = Math.round(ratio * 100)
  const overArt = track === 'over-art'
  return (
    <div
      role="progressbar"
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={pct}
      aria-label={label}
      className={cn(
        'w-full overflow-hidden',
        HEIGHT_CLASS[height],
        overArt ? 'bg-fill-track-2' : 'rounded-full bg-fill-track shadow-inset',
        className,
      )}
    >
      <div
        className={cn(
          'h-full',
          overArt ? '' : 'rounded-full',
          tone === 'done' ? 'bg-ink' : 'bg-accent-fill',
        )}
        style={{ width: `${pct.toString()}%` }}
      />
    </div>
  )
}
