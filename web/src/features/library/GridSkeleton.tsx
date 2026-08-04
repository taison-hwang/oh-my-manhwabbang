import { Skeleton } from '../../components/ds/Skeleton'
import {
  CARD_TEXT_HEIGHT,
  LIST_CARD_CLASS,
  LIST_HEADER_BAND_CLASS,
  LIST_HEADER_WRAPPER_CLASS,
  LIST_ROW_HEIGHT,
} from './useLibrary'

/**
 * `GridSkeleton` (ui-spec §9 #17, §4.5 "Loading — skeleton").
 *
 * The loading state has to occupy the **exact** geometry the loaded state will
 * (prd §5.3, WP-09 acceptance 9: layout shift < 0.01), so the cells carry the
 * cover's `aspect-ratio: 2/3` rather than a height, and the container repeats
 * the same `--grid-min`/`--grid-gap` template the real grid resolves — straight
 * from the token layer, so the two cannot disagree.
 *
 * Two things this file has to get right that a shimmer alone does not:
 *
 *  1. **The cell's text block is `CARD_TEXT_HEIGHT` tall**, not "two 10px bars
 *     and whatever that adds up to". ui-spec §4.5 specifies the bars, not the
 *     cell height; leave the block to size itself and the skeleton cell comes
 *     out 26px shorter than the card that replaces it.
 *  2. **The list skeleton carries the sort-header band.** The loaded list
 *     prepends one, so a skeleton without it drops the whole table ~33px the
 *     instant data lands. Note that the Layout Instability API scores that
 *     transition **0** — the skeleton nodes are removed and different nodes
 *     inserted rather than moved — so acceptance 9's Playwright assertion would
 *     have passed while the user watched the list jump. The band is shared with
 *     `SeriesList` as `LIST_HEADER_*_CLASS` and pinned by a test.
 */
export interface GridSkeletonProps {
  variant: 'grid' | 'list'
  /** ui-spec §4.5: 18 placeholder cells. */
  count?: number
}

export function GridSkeleton({ variant, count = 18 }: GridSkeletonProps) {
  const cells = Array.from({ length: count }, (_, i) => i)

  if (variant === 'list') {
    return (
      // `LIST_CARD_CLASS` is the E-32 card the loaded list is drawn in; the
      // skeleton has to be drawn in the same one or the whole table shifts by a
      // margin and a padding the instant data lands (see 2. above).
      <div aria-hidden="true" data-testid="library-skeleton" className={LIST_CARD_CLASS}>
        <div className={LIST_HEADER_WRAPPER_CLASS}>
          <div className={LIST_HEADER_BAND_CLASS} data-testid="library-skeleton-header">
            {/* One text-xs line box, which is what sets the real band's height
                (its tallest cell is a text-xs sort button). */}
            <span>&nbsp;</span>
          </div>
        </div>
        <div className="px-2">
          {cells.map((i) => (
            <div
              key={i}
              // No row rule: E-32 replaced the dividers with hover chips, and a
              // skeleton that still ruled its rows would be 1px per row taller
              // than the list it stands in for.
              className="flex items-center gap-3 px-2 py-1"
              style={{ height: `${LIST_ROW_HEIGHT.toString()}px` }}
            >
              <Skeleton variant="line" index={i} className="h-[36px] w-[24px]" />
              <Skeleton variant="line" index={i} width="38%" />
            </div>
          ))}
        </div>
      </div>
    )
  }

  return (
    <div
      className="grid p-4"
      style={{
        gridTemplateColumns: 'repeat(auto-fill, minmax(var(--grid-min), 1fr))',
        gap: 'var(--grid-gap)',
      }}
      aria-hidden="true"
      data-testid="library-skeleton"
    >
      {cells.map((i) => (
        <div key={i} className="flex flex-col">
          <Skeleton variant="cover" index={i} />
          <div
            className="flex flex-col gap-[7px] overflow-hidden pt-[7px]"
            style={{ height: `${CARD_TEXT_HEIGHT.toString()}px` }}
            data-testid="skeleton-cell-text"
          >
            <Skeleton variant="line" index={i} width="84%" />
            <Skeleton variant="line" index={i} width="44%" className="bg-fill-subtle" />
          </div>
        </div>
      ))}
    </div>
  )
}
