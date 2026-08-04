import { useVirtualizer } from '@tanstack/react-virtual'
import { useEffect, useRef, useState } from 'react'

import type { ID, SeriesSummary } from '../../api/types'
import { cn } from '../../lib/cn'
import { useBreakpoint } from '../../lib/useMediaQuery'
import { seriesRowDomId, useUiStore, type SortKey } from '../../store/ui'
import { SeriesRow } from './SeriesRow'
import {
  listHeaderPadRight,
  listLayoutFor,
  LIST_CARD_CLASS,
  LIST_HEADER_BAND_CLASS,
  LIST_HEADER_WRAPPER_CLASS,
  LIST_ROW_HEIGHT,
  LIST_ROW_HEIGHT_STACKED,
  LIST_SORT_COLUMNS,
  LIST_TEMPLATE,
  useScrollbarGutter,
} from './useLibrary'

/**
 * The virtualised list (FR-LIB-003, FR-LIB-004, FR-LIB-007).
 *
 * A `<table>` cannot be windowed without losing its column sizing, so this is a
 * CSS grid whose template is shared, character for character, between the header
 * and every row (`LIST_TEMPLATE`).
 *
 * The header sits **outside** the scroller (impl-plan WP-09 acceptance 3), which
 * is also what makes it survive virtualisation: a sticky element inside a
 * transformed, absolutely-positioned row stack would stick to nothing.
 *
 * Sharing the template string is necessary but **not sufficient**: the rows live
 * inside the scroller and the header does not, so the scrollbar makes the row
 * grid narrower than the header grid and `minmax(0,1fr)` swallows the whole
 * difference — every column from 형식 rightwards ends up a scrollbar-width out,
 * which is exactly the misalignment a shared template was supposed to prevent.
 * The scroller therefore reserves its gutter unconditionally and the header
 * pads itself by the measured width, so both grids resolve against the same
 * number in every state.
 *
 * Sort direction follows ui-spec §4.5: first click on 시리즈명 ascends, first
 * click on 권/용량/수정일 descends, and clicking the active column flips it —
 * the rule lives in `store/ui.ts`'s `toggleSort`, once, for both this header and
 * the top bar's select.
 */
export interface SeriesListProps {
  items: SeriesSummary[]
  query: string
  /** E-34 §2 — a series to scroll to and focus, or `null`. One-shot. */
  revealSeries?: ID | null
  /** Called once the reveal has been acted on, so the instruction is not replayed. */
  onRevealed?: () => void
  onOpen: (sid: ID) => void
  onEndReached: () => void
}

export function SeriesList({
  items,
  query,
  revealSeries = null,
  onRevealed,
  onOpen,
  onEndReached,
}: SeriesListProps) {
  const scrollRef = useRef<HTMLDivElement>(null)
  const breakpoint = useBreakpoint()
  const layout = listLayoutFor(breakpoint)
  const template = LIST_TEMPLATE[layout]
  const rowHeight = layout === 'stacked' ? LIST_ROW_HEIGHT_STACKED : LIST_ROW_HEIGHT

  const sort = useUiStore((s) => s.sort)
  const order = useUiStore((s) => s.order)
  const toggleSort = useUiStore((s) => s.toggleSort)
  const gutter = useScrollbarGutter(scrollRef)

  const virtualizer = useVirtualizer({
    count: items.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => rowHeight,
    overscan: 6,
  })

  const rows = virtualizer.getVirtualItems()
  const lastIndex = rows.at(-1)?.index ?? -1

  useEffect(() => {
    if (items.length > 0 && lastIndex >= items.length - 1) onEndReached()
  }, [lastIndex, items.length, onEndReached])

  /**
   * The E-34 §2 reveal — the same rule as `SeriesGrid`, one index simpler.
   *
   * This virtualiser windows **rows one series at a time**, so the index in
   * `items` is the index it takes; the grid has to divide by its column count
   * first. Everything else is identical and the reasoning is written out there:
   * `getElementById` cannot find a row outside the window, `align: 'start'`
   * without the prototype's 96px, and a target that has not been fetched yet
   * leaves the instruction armed rather than chasing pages.
   */
  const [revealed, setRevealed] = useState<ID | null>(null)
  useEffect(() => {
    if (revealSeries === null) return
    const index = items.findIndex((series) => series.id === revealSeries)
    if (index === -1) return
    setRevealed(revealSeries)
    onRevealed?.()
    virtualizer.scrollToIndex(index, { align: 'start' })
    requestAnimationFrame(() => {
      document.getElementById(seriesRowDomId(revealSeries))?.focus()
    })
  }, [revealSeries, items, virtualizer, onRevealed])

  const sortHeader = (key: SortKey, label: string, align: 'left' | 'right') => {
    const active = sort === key
    return (
      <button
        type="button"
        className={cn(
          'cursor-pointer bg-transparent p-0 text-xs uppercase tracking-[.08em]',
          align === 'right' ? 'text-right' : 'text-left',
          active ? 'text-ink' : 'text-ink-dim',
        )}
        onClick={() => {
          toggleSort(key)
        }}
      >
        {active ? `${label} ${order === 'asc' ? '↑' : '↓'}` : label}
      </button>
    )
  }

  const columns = LIST_SORT_COLUMNS
  const [colName, colBooks, colSize, colMtime] = columns

  return (
    /* E-32: header band and rows are one raised card now, not a full-bleed
       table. `LIST_CARD_CLASS` is shared with `GridSkeleton` so the two states
       occupy the same box. */
    <div className={cn('flex min-h-0 flex-1 flex-col', LIST_CARD_CLASS)}>
      <div
        className={LIST_HEADER_WRAPPER_CLASS}
        style={{ paddingRight: listHeaderPadRight(gutter) }}
        data-testid="library-list-header-wrapper"
      >
        <div
          className={LIST_HEADER_BAND_CLASS}
          style={{ gridTemplateColumns: template }}
          data-testid="library-list-header"
        >
          <span />
          {sortHeader(colName.key, colName.label, 'left')}
          {layout !== 'stacked' && (
            <>
              <span>형식</span>
              <span className="text-right">{sortHeader(colBooks.key, colBooks.label, 'right')}</span>
              {layout === 'full' && (
                <>
                  <span className="text-right">
                    {sortHeader(colSize.key, colSize.label, 'right')}
                  </span>
                  <span className="text-right">
                    {sortHeader(colMtime.key, colMtime.label, 'right')}
                  </span>
                </>
              )}
              <span>진행률</span>
            </>
          )}
        </div>
      </div>

      <div
        ref={scrollRef}
        className="min-h-0 flex-1 overflow-y-auto px-2"
        style={{ scrollbarGutter: 'stable' }}
        data-testid="library-scroller"
      >
        <div className="relative w-full" style={{ height: `${virtualizer.getTotalSize().toString()}px` }}>
          {rows.map((row) => {
            const series = items[row.index]
            if (series === undefined) return null
            return (
              <div
                key={row.key}
                data-index={row.index}
                className="absolute left-0 top-0 w-full"
                style={{ transform: `translateY(${row.start.toString()}px)` }}
              >
                <SeriesRow
                  series={series}
                  layout={layout}
                  query={query}
                  revealed={series.id === revealed}
                  onOpen={() => {
                    onOpen(series.id)
                  }}
                />
              </div>
            )
          })}
        </div>
      </div>
    </div>
  )
}
