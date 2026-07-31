import type { HTMLAttributes, ReactNode } from 'react'

import { cn } from '../../lib/cn'

/**
 * `.card` (ui-spec §2.3): surface fill, `--space-3` padding, zero radius, and
 * **no shadow** — only `.dialog`, the viewer's next-volume card and the
 * `.elev-*` utilities carry one (ui-spec §0.2).
 *
 * The kicker/title/body/meta slots are props rather than sub-components so the
 * file keeps one exported component, per impl-plan §5.2.
 */
export type Elevation = 'none' | 'sm' | 'md' | 'lg'

export interface CardProps extends Omit<HTMLAttributes<HTMLDivElement>, 'title'> {
  kicker?: string
  /** `.card-title`. Shadows the DOM `title` attribute, which a card never needs. */
  title?: ReactNode
  meta?: ReactNode
  elevation?: Elevation
  children?: ReactNode
}

const ELEVATION_CLASS: Record<Elevation, string> = {
  none: '',
  sm: 'elev-sm',
  md: 'elev-md',
  lg: 'elev-lg',
}

export function Card({
  kicker,
  title,
  meta,
  elevation = 'none',
  className,
  children,
  ...rest
}: CardProps) {
  return (
    <div className={cn('card', ELEVATION_CLASS[elevation], className)} {...rest}>
      {kicker !== undefined && <div className="card-kicker">{kicker}</div>}
      {title !== undefined && <div className="card-title">{title}</div>}
      {children !== undefined && <div className="card-body">{children}</div>}
      {meta !== undefined && <div className="card-meta">{meta}</div>}
    </div>
  )
}
