import type { HTMLAttributes } from 'react'

import { cn } from '../../lib/cn'

/**
 * `.hr` (ui-spec §2.3): a **2px** divider field, not a hairline border.
 *
 * Structure in this design system is drawn with rules — 2px for section
 * boundaries, 1px for rows — never with whitespace and never with a shadow
 * (ui-spec §0.2).
 */
export type HrProps = HTMLAttributes<HTMLHRElement>

export function Hr({ className, ...rest }: HrProps) {
  return <hr className={cn('hr', className)} {...rest} />
}
