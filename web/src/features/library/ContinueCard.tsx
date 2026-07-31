import { useCoverImage } from '../../api/queries'
import type { ContinueItem } from '../../api/types'
import { THUMB_WIDTH_FOR } from '../../api/urls'
import { FallbackCover } from '../../components/ds/FallbackCover'
import { ProgressBar } from '../../components/ds/ProgressBar'
import { formatContinueCounter } from '../../lib/format'

/**
 * `ContinueCard` (ui-spec §9 #3, §4.3) — one 300px card of the 이어보기 track.
 *
 * Clicking it resumes that **book** at its saved page, not the series: the whole
 * point of the shelf is that it is one click from where the reader stopped
 * (FR-LIB-010).
 */
export interface ContinueCardProps {
  item: ContinueItem
  onResume: () => void
}

export function ContinueCard({ item, onResume }: ContinueCardProps) {
  const cover = useCoverImage(item.series_id, {
    w: THUMB_WIDTH_FOR.continueCard,
    v: null,
    enabled: item.has_cover,
  })

  const total = item.progress.page_count
  const ratio = total > 0 ? item.progress.last_page / total : 0

  return (
    <button
      type="button"
      onClick={onResume}
      aria-label={`${item.series_name} ${item.book.name}`}
      className="flex flex-[0_0_272px] cursor-pointer gap-3 border border-rule bg-surface p-3 text-left hover:border-accent md:flex-[0_0_336px]"
    >
      {/* 96×144, up from 66×99: the cover is the only thing on this card that
          identifies the book at a glance, and at 66px wide a title in the art
          was unreadable. The 2:3 ratio is unchanged. */}
      <span className="relative block h-[144px] w-[96px] flex-[0_0_96px] overflow-hidden bg-fill-track">
        <FallbackCover title={item.series_name} format={item.book.kind} size="row" />
        {cover.status === 'ready' && (
          <img
            src={cover.url}
            alt=""
            className="absolute inset-0 h-full w-full object-cover"
            draggable={false}
          />
        )}
      </span>

      <span className="flex min-w-0 flex-1 flex-col gap-[5px]">
        <span className="line-clamp-2 font-heading text-base font-extrabold leading-[1.2]">
          {item.series_name}
        </span>
        <span className="truncate whitespace-nowrap text-xs text-ink-muted">{item.book.name}</span>
        <span className="flex-1" />
        <span className="text-sm tabular-nums text-accent-text">
          {formatContinueCounter(item.progress.last_page, total)}
        </span>
        <ProgressBar value={ratio} label={item.series_name} />
      </span>
    </button>
  )
}
