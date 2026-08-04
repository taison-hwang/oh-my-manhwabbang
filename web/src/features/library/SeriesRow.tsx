import { useCoverImage } from '../../api/queries'
import type { SeriesSummary } from '../../api/types'
import { THUMB_WIDTH_FOR } from '../../api/urls'
import { FallbackCover } from '../../components/ds/FallbackCover'
import { FormatBadge } from '../../components/ds/FormatBadge'
import { ProgressBar } from '../../components/ds/ProgressBar'
import { cn } from '../../lib/cn'
import {
  formatBytes,
  formatDate,
  formatProgressLabel,
  formatVolumeCount,
} from '../../lib/format'
import { seriesRowDomId } from '../../store/ui'
import { highlightParts, LIST_TEMPLATE, type ListLayout } from './useLibrary'

/**
 * `SeriesRow` (ui-spec §9 #2, §4.5 "List mode").
 *
 * design.md principle 1: **the list is co-equal with the grid, not a fallback.**
 * The filenames in this collection carry the metadata (`1~23(완)`), so a dense
 * row of real columns is as much a first-class reading of the library as the
 * covers are — same data, same virtualisation, same sort affordances.
 *
 * The row is a real `<button>`: it is one click target with one accessible
 * name, it takes the focus ring for free, and it keeps the whole row clickable
 * without a `div` pretending to be interactive.
 *
 * Responsive (ui-spec §7): 용량 and 수정일 drop at 768–1023 — the format tag
 * stays, because it is primary metadata — and below 768 the row becomes two
 * lines rather than a horizontally scrolling table.
 */
export interface SeriesRowProps {
  series: SeriesSummary
  layout: ListLayout
  query: string
  /** E-34 §2 — this is the row the viewer came back to; outline it. */
  revealed?: boolean
  onOpen: () => void
}

export function SeriesRow({ series, layout, query, revealed = false, onOpen }: SeriesRowProps) {
  const cover = useCoverImage(series.id, {
    w: THUMB_WIDTH_FOR.listRow,
    v: series.cover_cv,
    enabled: series.has_cover,
  })

  const ratio = series.progress.percent / 100
  const done = ratio >= 1
  const parts = highlightParts(series.name, query)

  const title = (
    <span
      className={cn(
        'min-w-0 truncate whitespace-nowrap text-left text-base',
        series.status === 'ok' ? '' : 'text-ink-dim',
      )}
    >
      {parts.before}
      {parts.match !== '' && <span className="text-accent-text">{parts.match}</span>}
      {parts.after}
    </span>
  )

  const progressCell = (
    <span className="flex items-center gap-2">
      <ProgressBar
        value={ratio}
        tone={done ? 'done' : 'accent'}
        label={series.name}
        className="flex-1"
      />
      <span
        className={cn(
          'w-[54px] text-right text-xs tabular-nums',
          ratio > 0 ? 'text-accent-text' : 'text-ink-faint',
        )}
      >
        {formatProgressLabel(ratio)}
      </span>
    </span>
  )

  return (
    <button
      type="button"
      id={seriesRowDomId(series.id)}
      aria-label={series.name}
      {...(revealed ? { 'data-revealed': 'true' } : {})}
      onClick={onOpen}
      // E-32: the 1px rule between rows is gone and the hover is a rounded chip
      // (`.row-chip`, base.css) rather than a full-bleed tint.
      className="row-chip grid w-full cursor-pointer items-center gap-3 px-2 py-1 text-left"
      // An **outline** rather than the grid's inset ring (E-34 §2): a row is
      // full-bleed inside the scroller and has no cover box to ring, and
      // `outlineOffset: -2px` keeps it off the row above.
      style={{
        gridTemplateColumns: LIST_TEMPLATE[layout],
        ...(revealed ? { outline: '2px solid var(--color-hot)', outlineOffset: '-2px' } : {}),
      }}
    >
      {/* E-32: the 1px-bordered well becomes a rounded, recessed one. */}
      <span className="relative block h-[36px] w-[24px] overflow-hidden rounded-sm shadow-inset">
        <FallbackCover title={series.name} format={series.kind} size="row" />
        {cover.status === 'ready' && (
          <img
            src={cover.url}
            alt=""
            className="absolute inset-0 h-full w-full object-cover"
            draggable={false}
          />
        )}
      </span>

      {layout === 'stacked' ? (
        <span className="flex min-w-0 flex-col gap-1">
          {title}
          <span className="flex items-center gap-2 text-xs tabular-nums text-ink-dim">
            <FormatBadge format={series.kind} variant="tag" />
            <span>{formatVolumeCount(series.book_count)}</span>
            <span>{formatBytes(series.total_bytes)}</span>
            <span className="min-w-0 flex-1">{progressCell}</span>
          </span>
        </span>
      ) : (
        <>
          {title}
          <span>
            <FormatBadge format={series.kind} variant="tag" />
          </span>
          <span className="text-right text-sm tabular-nums">
            {formatVolumeCount(series.book_count)}
          </span>
          {layout === 'full' && (
            <>
              <span className="text-right text-sm tabular-nums text-ink-muted">
                {formatBytes(series.total_bytes)}
              </span>
              <span className="text-right text-sm tabular-nums text-ink-dim">
                {formatDate(series.mtime)}
              </span>
            </>
          )}
          {progressCell}
        </>
      )}
    </button>
  )
}
