import { cn } from '../../lib/cn'

/**
 * A single shimmer placeholder (ui-spec §4.5 "Loading — skeleton").
 *
 * The stagger is `(i % 6) * 0.12s`, which is what stops eighteen cells pulsing
 * in lockstep and reading as a broken render. Callers pass their grid index;
 * the modulo is applied here so nobody has to remember the 6.
 *
 * The `cover` variant carries the 2:3 aspect ratio rather than a height, so the
 * skeleton occupies the *exact* box the real cover will — zero layout shift is
 * an acceptance criterion, not a nicety.
 */
export interface SkeletonProps {
  variant: 'cover' | 'line'
  /** Grid position; drives the animation delay. */
  index?: number
  /** For `line`: a CSS width such as `84%`. */
  width?: string
  className?: string
}

export function Skeleton({ variant, index = 0, width, className }: SkeletonProps) {
  const delay = `${((index % 6) * 0.12).toFixed(2)}s`
  return (
    <div
      aria-hidden="true"
      className={cn(
        'animate-shimmer',
        variant === 'cover' ? 'aspect-[2/3] w-full bg-fill-track' : 'h-[10px] bg-fill-track',
        className,
      )}
      style={{ animationDelay: delay, ...(width === undefined ? {} : { width }) }}
    />
  )
}
