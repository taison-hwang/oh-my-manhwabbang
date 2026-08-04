import { useVirtualizer } from '@tanstack/react-virtual'
import { useEffect, useRef, useState } from 'react'

import type { ID, SeriesSummary } from '../../api/types'
import { useBreakpoint } from '../../lib/useMediaQuery'
import { seriesCardDomId } from '../../store/ui'
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
  /** E-34 §2 — a series to scroll to and focus, or `null`. One-shot. */
  revealSeries?: ID | null
  /** Called once the reveal has been acted on, so the instruction is not replayed. */
  onRevealed?: () => void
  onOpen: (sid: ID) => void
  onResume: (series: SeriesSummary) => void
  /** Called when the last row is rendered — the FR-LIB-007 pagination trigger. */
  onEndReached: () => void
}

export function SeriesGrid({
  items,
  query,
  revealSeries = null,
  onRevealed,
  onOpen,
  onResume,
  onEndReached,
}: SeriesGridProps) {
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

  // -------------------------------------------------------------------------
  // The E-34 §2 reveal
  // -------------------------------------------------------------------------
  //
  // **`document.getElementById` cannot do this job here.** That is the
  // prototype's implementation and it is right for the prototype, which renders
  // every card. This grid is windowed: at 1440 it holds about five rows of six,
  // so the card for series #51 of the 60 already fetched is simply not in the
  // document, and `getElementById` returns `null` for it — silently, which is
  // the failure mode the ruling calls out. The index in `items` is the thing
  // that always exists, so the scroll goes through the virtualiser and the
  // element is looked up **after** it has had a frame to mount.
  //
  // `align: 'start'` with no offset. The prototype subtracts 96px because its
  // scroll container is the whole library screen and its header sits inside
  // that scroll; ours is the grid band alone (`scrollRef` below) — 이어보기 and
  // the section header are outside it — so the top of this container is already
  // below the chrome, and 96px would push the card that much too far down.
  //
  // A target that is not in `items` yet is left **armed**: `onRevealed` is not
  // called, so the instruction survives, and this effect runs again on every
  // page the infinite list appends. Chasing it instead — fetching pages until
  // the series turns up — is unbounded on a 10 000-series library and can never
  // terminate at all when the reader's `scope`/`q` exclude that series, which
  // E-34 §1 forbids us from clearing. Nothing is focused in the meantime, so
  // nothing is stolen.
  //
  // **`width > 0` is not defensiveness, it is the correctness of the row
  // arithmetic.** `useElementWidth` measures in a *layout* effect, and React
  // flushes a commit's pending passive effects before it runs the re-render that
  // a layout effect's `setState` scheduled — so on the commit this grid mounts,
  // this effect sees `width === 0`, and `columnCount(0)` is **1**. The reveal
  // would scroll to row 50 of a one-column grid instead of row 8 of a
  // six-column one; measured here, that put the scroller at the very bottom
  // with the card nowhere near it. An unmeasured grid has no rows yet. The
  // measurement lands one commit later and `columns`/`width` are dependencies,
  // so the reveal simply happens then.
  const [revealed, setRevealed] = useState<ID | null>(null)
  useEffect(() => {
    if (revealSeries === null || width <= 0) return
    const index = items.findIndex((series) => series.id === revealSeries)
    if (index === -1) return
    setRevealed(revealSeries)
    onRevealed?.()
    virtualizer.scrollToIndex(Math.floor(index / columns), { align: 'start' })
    requestAnimationFrame(() => {
      document.getElementById(seriesCardDomId(revealSeries))?.focus()
    })
  }, [revealSeries, items, columns, width, virtualizer, onRevealed])

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
                    revealed={series.id === revealed}
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
