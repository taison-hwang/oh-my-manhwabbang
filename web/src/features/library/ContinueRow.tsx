import type { ContinueItem } from '../../api/types'
import { formatItemCount } from '../../lib/format'
import { ContinueCard } from './ContinueCard'
import { useContinueItems } from './useLibrary'

/**
 * The 이어보기 shelf (FR-LIB-010, ui-spec §4.3).
 *
 * **Hidden entirely when there is nothing in progress** — no header, no empty
 * band, no reserved height — and hidden during the skeleton state, so the first
 * paint of a cold library is not a 130px empty rectangle above the grid
 * (design.md 화면 1). An empty `items` array from `/api/continue` is the signal.
 */
export interface ContinueRowProps {
  /** True while the library is still loading; suppresses the whole row. */
  suppressed: boolean
  onResume: (item: ContinueItem) => void
}

export function ContinueRow({ suppressed, onResume }: ContinueRowProps) {
  const { items } = useContinueItems()

  if (suppressed || items.length === 0) return null

  return (
    /* E-32 removes the 2px divider under this band: the shelf is separated from
       the library below it by space and by the cards' own elevation. The
       track's bottom padding grows from 4px to 16px so the hover lift on a
       ContinueCard has somewhere to go and its shadow is not clipped by the
       scroller. */
    <section className="flex-none p-4" aria-label="이어보기">
      <div className="mb-3 flex items-baseline gap-2">
        <h6>이어보기</h6>
        <span className="text-xs tabular-nums text-ink-dim">{formatItemCount(items.length)}</span>
      </div>
      {/* Below 768 the track is a snap scroller and each card is a stop, which
          is how ui-spec §7's `<768` cell gets "one per screen" from a card that
          is simply as wide as the track. The axis and the strictness have to sit
          here and the stop positions on the cards, so the cell is only met when
          both class lists agree — `library.test.tsx` pins both, and
          07-responsive 6.12 makes the browser prove it by undoing a nudge.
          `md:snap-none` turns the whole layer off at 768, where §7 asks for an
          ordinary scroller with more than one card in view. */}
      <div
        className="flex snap-x snap-mandatory gap-3 overflow-x-auto pb-4 md:snap-none"
        data-testid="continue-track"
      >
        {items.map((item) => (
          <ContinueCard
            key={item.book.id}
            item={item}
            onResume={() => {
              onResume(item)
            }}
          />
        ))}
      </div>
    </section>
  )
}
