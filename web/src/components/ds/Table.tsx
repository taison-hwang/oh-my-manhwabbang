import type { ReactNode, TableHTMLAttributes } from 'react'

import { cn } from '../../lib/cn'

/**
 * `.table` (ui-spec §2.3): 2px rule under the header, 1px under every row, and
 * a `text @ 4%` row hover.
 *
 * The library list is **not** built from this — it is a virtualised CSS grid
 * (FR-LIB-007), because a `<table>` cannot be windowed without losing its
 * column sizing. This is for the small tabular blocks: the roots panel and the
 * scan log.
 *
 * Any table that can outgrow its column can overflow horizontally, so the
 * wrapper scrolls on itself rather than letting `body` scroll (ui-spec §7).
 */
export interface TableProps extends TableHTMLAttributes<HTMLTableElement> {
  children?: ReactNode
}

export function Table({ className, children, ...rest }: TableProps) {
  return (
    <div className="w-full overflow-x-auto">
      <table className={cn('table', className)} {...rest}>
        {children}
      </table>
    </div>
  )
}
