import { cn } from '../../lib/cn'

/**
 * `ProgressBar` (ui-spec §9 #5).
 *
 * Three variables, all from the screenshots:
 *  - height 3px in rows and cards, 4px over artwork and in the series-detail
 *    stat strip;
 *  - the trough is `--fill-track` normally and `--fill-track-2` when it sits on
 *    top of a cover, where the lighter step would disappear;
 *  - the fill is the accent, except a **completed** library row, where it turns
 *    to `--ink` so 완독 reads as "finished", not "still going" (ui-spec §4.5).
 */
export interface ProgressBarProps {
  /** 0..1. Clamped; values <= 0 still render the trough. */
  value: number
  height?: 3 | 4
  track?: 'default' | 'over-art'
  tone?: 'accent' | 'done'
  className?: string
  /** Accessible name, e.g. the series title. */
  label?: string
}

export function ProgressBar({
  value,
  height = 3,
  track = 'default',
  tone = 'accent',
  className,
  label,
}: ProgressBarProps) {
  const ratio = Number.isFinite(value) ? Math.max(0, Math.min(1, value)) : 0
  const pct = Math.round(ratio * 100)
  return (
    <div
      role="progressbar"
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={pct}
      aria-label={label}
      className={cn(
        'w-full',
        height === 4 ? 'h-[4px]' : 'h-[3px]',
        track === 'over-art' ? 'bg-fill-track-2' : 'bg-fill-track',
        className,
      )}
    >
      <div
        className={cn('h-full', tone === 'done' ? 'bg-ink' : 'bg-accent')}
        style={{ width: `${pct.toString()}%` }}
      />
    </div>
  )
}
