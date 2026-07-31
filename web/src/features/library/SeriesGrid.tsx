import { useVirtualizer } from '@tanstack/react-virtual'
import { useEffect, useRef } from 'react'

import type { ID, SeriesSummary } from '../../api/types'
import { useBreakpoint } from '../../lib/useMediaQuery'
import { SeriesCard } from './SeriesCard'
import {
  cardHeight,
  columnCount,
  columnWidth,
  gridCoverWidth,
  GRID_METRICS,
  useElementWidth,
} from './useLibrary'

/**
 * The virtualised cover grid (FR-LIB-001, FR-LIB-007, NFR-PRF-003).
 *
 * `@tanstack/react-virtual` windows **rows**, not cards, so the column count has
 * to exist in JavaScript. It is computed from the measured content width and the
 * `--grid-min`/`--grid-gap` values of the current tier (`GRID_METRICS`), which
 * reproduces exactly what `repeat(auto-fill, minmax(--grid-min, 1fr))` would
 * have laid out — see the drift test in `useLibrary.test.ts`.
 *
 * The ref that feeds that arithmetic is on the **grid box**, not on the padded
 * wrapper around it: `clientWidth` includes padding, and measuring the `p-4`
 * parent hands `columnCount` 32px the grid does not have — 7 columns of 151px
 * at 1440 where ui-spec §7 says 6 of ~180px, and 151px is under the 152px
 * `--grid-min` those columns exist to honour. The skeleton lays itself out with
 * the real CSS template, so getting this wrong also turns the skeleton→grid
 * transition into a 6→7 column reflow, i.e. the opposite of acceptance 9.
 *
 * Row height is a pure function of the column width (2:3 cover + a fixed 60px of
 * text, which `SeriesCard` pins rather than merely hopes for), so no card is
 * ever measured: measuring would make the scrollbar jump as rows are recycled,
 * which is the layout shift the skeleton exists to avoid.
 */
export interface SeriesGridProps {
  items: SeriesSummary[]
  /** The debounced search query, for match highlighting. */
  query: string
  onOpen: (sid: ID) => void
  onResume: (series: SeriesSummary) => void
  /** Called when the last row is rendered — the FR-LIB-007 pagination trigger. */
  onEndReached: () => void
}

export function SeriesGrid({ items, query, onOpen, onResume, onEndReached }: SeriesGridProps) {
  const scrollRef = useRef<HTMLDivElement>(null)
  const gridRef = useRef<HTMLDivElement>(null)
  const breakpoint = useBreakpoint()
  const metrics = GRID_METRICS[breakpoint]

  const width = useElementWidth(gridRef)
  const columns = columnCount(width, metrics)
  const rowHeight = cardHeight(columnWidth(width, columns, metrics.gap))
  const rowCount = Math.ceil(items.length / columns)

  const virtualizer = useVirtualizer({
    count: rowCount,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => rowHeight,
    gap: metrics.gap,
    overscan: 2,
  })

  const rows = virtualizer.getVirtualItems()
  const lastIndex = rows.at(-1)?.index ?? -1

  useEffect(() => {
    if (rowCount > 0 && lastIndex >= rowCount - 1) onEndReached()
  }, [lastIndex, rowCount, onEndReached])

  const coverWidth = gridCoverWidth(breakpoint)

  return (
    <div
      ref={scrollRef}
      className="min-h-0 flex-1 overflow-y-auto"
      // Reserve the scrollbar gutter unconditionally so the measured width —
      // and therefore the column count — does not change the moment the grid
      // becomes tall enough to scroll.
      style={{ scrollbarGutter: 'stable' }}
      data-testid="library-scroller"
    >
      <div className="p-4">
        <div
          ref={gridRef}
          className="relative w-full"
          style={{ height: `${virtualizer.getTotalSize().toString()}px` }}
        >
          {rows.map((row) => {
            const start = row.index * columns
            const slice = items.slice(start, start + columns)
            return (
              <div
                key={row.key}
                data-index={row.index}
                className="absolute left-0 top-0 grid w-full"
                style={{
                  transform: `translateY(${row.start.toString()}px)`,
                  gridTemplateColumns: `repeat(${columns.toString()}, minmax(0, 1fr))`,
                  gap: `${metrics.gap.toString()}px`,
                }}
              >
                {slice.map((series) => (
                  <SeriesCard
                    key={series.id}
                    series={series}
                    coverWidth={coverWidth}
                    query={query}
                    onOpen={() => {
                      onOpen(series.id)
                    }}
                    onResume={() => {
                      onResume(series)
                    }}
                  />
                ))}
              </div>
            )
          })}
        </div>
      </div>
    </div>
  )
}
