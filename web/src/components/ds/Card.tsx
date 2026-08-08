import type { HTMLAttributes, ReactNode } from 'react'

import { cn } from '../../lib/cn'

/**
 * `.card` (ui-spec §2.3): surface fill, `--space-3` padding, `--radius-lg`, and
 * — since E-36 §5.7.6 #31, ruled in E-42 — **`--shadow-md`**. A card is a
 * raised surface; that is the contract now.
 *
 * ## The sentence this comment used to quote is withdrawn
 *
 * It read "zero radius, and **no shadow** — only `.dialog`, the viewer's
 * next-volume card and the `.elev-*` utilities carry one (ui-spec §0.2)". Both
 * halves were retired by E-32 (the radius scale) and E-36 (the elevation
 * pass), and neither retirement reached this file: the comment went on citing a
 * superseded §0.2 as if it were live, which is why E-36 §2 names *this
 * docstring* as an instance of the pattern it was written to stop — a rule
 * kept alive at a call site after the ruling that made it had been reversed.
 * Do not re-derive the elevation from a §0.2 quote; `base.css` is the source.
 *
 * ## Nothing on screen changes when you edit this file
 *
 * `Card` is exported and **imported nowhere in `web/src`** — not by a screen,
 * not by a test. Every surface that looks like a card (SeriesCard, VolumeTile,
 * the series-detail header, NextVolumeCard) builds its own box out of
 * utilities. So the shadow above arrives through `.card` in `base.css`, and
 * this component is the DS's unused reference implementation of it. If you came
 * here after changing `.card` and are wondering why the app looks the same:
 * that is the reason, and it is not a bug in your change.
 *
 * `elevation` therefore stacks a second shadow (`elev-*`) on a card that now
 * has one of its own. That is a real question for the first consumer, and there
 * is no first consumer, so it stays open rather than being guessed at here.
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
