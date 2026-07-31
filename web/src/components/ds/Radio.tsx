import type { InputHTMLAttributes } from 'react'

import { cn } from '../../lib/cn'

/**
 * `.radio` (ui-spec §2.3).
 *
 * The 16×16 `.dot` is one of exactly two circles in the entire product — the
 * other is the viewer's loading spinner (ui-spec §0.1). Everything else has
 * zero radius.
 */
export interface RadioProps extends Omit<InputHTMLAttributes<HTMLInputElement>, 'type'> {
  label: string
}

export function Radio({ label, className, checked, ...rest }: RadioProps) {
  return (
    <label className={cn('radio', className)} data-checked={checked === true ? 'true' : 'false'}>
      <input type="radio" className="sr-only" checked={checked} {...rest} />
      <span className="dot" aria-hidden="true" />
      {label}
    </label>
  )
}
