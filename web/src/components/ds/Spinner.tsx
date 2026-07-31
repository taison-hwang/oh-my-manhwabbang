import { cn } from '../../lib/cn'

/**
 * The viewer's page-loading spinner (ui-spec §6.3).
 *
 * One of exactly two circles in the product. It appears only when a page
 * transition takes longer than ~240 ms — below that it is noise, and the stage
 * is never blanked while it shows: the previous page stays up.
 */
export interface SpinnerProps {
  className?: string
  /** Accessible label; omit inside an already-labelled status region. */
  label?: string
}

export function Spinner({ className, label }: SpinnerProps) {
  return (
    <span
      className={cn('spinner', className)}
      role={label === undefined ? 'presentation' : 'status'}
      aria-label={label}
    />
  )
}
