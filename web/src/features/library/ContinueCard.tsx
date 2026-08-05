import { useCoverImage } from '../../api/queries'
import type { ContinueItem } from '../../api/types'
import { THUMB_WIDTH_FOR } from '../../api/urls'
import { FallbackCover } from '../../components/ds/FallbackCover'
import { ProgressBar } from '../../components/ds/ProgressBar'
import { formatContinueCounter } from '../../lib/format'

/**
 * `ContinueCard` (ui-spec §9 #3, §4.3) — one card of the 이어보기 track:
 * **218px wide below `md` (768), 269px at and above it.**
 *
 * Both numbers are the *whole* card, border-box, so what the text column gets is
 * `width − 96 cover − 12 gap − 2×12 padding` = **86px** below 768 and **137px**
 * at and above (measured in Chrome, not derived). They are the previous pair,
 * 272 / 336, narrowed by 20% at the user's request (**E-37**); the cover, the
 * gap and the padding did not move.
 *
 * 272 / 336 were **not** E-32's — that attribution was made up and then
 * repeated into ui-spec twice. They arrived in **session 5**, applied
 * `판정 없이` (HANDOFF §1.0e), are in the first commit, and E-32's own commit
 * leaves them byte-identical. **Until E-37 no ruling had ever covered the width
 * of this card**, which is how ui-spec §4.3 came to say `flex:0 0 300px` from
 * day one — a number this file has never held — with nothing to contradict it.
 * That is the defect **E-36** was raised over. So: if the flex-basis below
 * changes, this comment and ui-spec §4.3 / §7 change in the same edit, and
 * `library.test.tsx` will fail until they do.
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
      // E-32: the 1px hairline and its accent-on-hover become a raised card
      // that lifts. `hover:border-accent` could not survive the reskin anyway —
      // the accent is a deep teal and 1.2:1 against the dark surface.
      className="flex flex-[0_0_218px] cursor-pointer gap-3 rounded-lg bg-surface p-3 text-left shadow-md transition-[box-shadow,transform] duration-150 hover:-translate-y-0.5 hover:shadow-lg md:flex-[0_0_269px]"
    >
      {/* 96×144, up from 66×99: the cover is the only thing on this card that
          identifies the book at a glance, and at 66px wide a title in the art
          was unreadable. The 2:3 ratio is unchanged. */}
      <span className="relative block h-[144px] w-[96px] flex-[0_0_96px] overflow-hidden rounded-md bg-fill-track shadow-inset">
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
        {/* E-32: card titles drop from 800 to 700. Section headings do not. */}
        <span className="line-clamp-2 font-heading text-base font-bold leading-[1.2]">
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
