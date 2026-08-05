import { PanelTop } from 'lucide-react'

import { Button } from '../../components/ds/Button'
import { cn } from '../../lib/cn'
import { formatViewerCounter } from '../../lib/format'
import { PageSlider } from './PageSlider'
import { ThumbnailStrip } from './ThumbnailStrip'

/**
 * The bottom overlay (ui-spec §6.7): counter · slider · `썸네일 · T`.
 *
 * The thumbnail strip sits *inside* this bar, above the control row, so opening
 * it grows one surface rather than introducing a second floating panel. Same
 * opacity/`pointer-events` fade contract as the top bar — the bars are never
 * unmounted, which is what keeps the wake instant and stops the strip from
 * re-mounting (and re-requesting every visible thumbnail) on each mouse move.
 *
 * Like the top bar, it carries **no hover-hold handlers**: E-27's hold is one
 * rule on the viewer root now, and it recognises this surface — thumbnail strip
 * included — by `data-role`. See `trackChromeHover` in `ViewerPage`.
 */
export interface ViewerBottomBarProps {
  visible: boolean
  bookId: string
  cv: string | null
  page: number
  pageCount: number
  stripOpen: boolean
  dragging: boolean
  dragPage: number | null
  onToggleStrip: () => void
  onDragStart: (page: number) => void
  onDrag: (page: number) => void
  onCommit: (page: number) => void
  onJump: (page: number) => void
}

export function ViewerBottomBar({
  visible,
  bookId,
  cv,
  page,
  pageCount,
  stripOpen,
  dragging,
  dragPage,
  onToggleStrip,
  onDragStart,
  onDrag,
  onCommit,
  onJump,
}: ViewerBottomBarProps) {
  return (
    <div
      data-role="viewer-bottom-bar"
      data-visible={visible ? 'true' : 'false'}
      className={cn(
        // `z-chrome` puts both bars above the end-of-volume scrim, which is
        // later in the DOM and would otherwise paint over them — see the note
        // on `NextVolumeCard` in `ViewerPage`.
        'z-chrome border-t-2 border-neutral-800 bg-bg transition-opacity',
        // See the note on the top bar: awake it is in the column, asleep it is
        // an overlay (E-27).
        visible
          ? 'relative order-last flex-none opacity-100'
          : 'pointer-events-none absolute inset-x-0 bottom-0 opacity-0',
      )}
      style={{ transitionDuration: 'var(--chrome-fade)' }}
    >
      {stripOpen && (
        <ThumbnailStrip
          bookId={bookId}
          cv={cv}
          pageCount={pageCount}
          current={page}
          onJump={onJump}
        />
      )}

      {/* `flex-wrap`, like the top bar: below ~520px the counter, the slider and
          썸네일 · T stop fitting on one line, and without it the slider is
          crushed to a few pixels of travel rather than moving to its own row. */}
      <div className="flex flex-wrap items-center gap-4 px-4 pb-3 pt-2">
        <span
          data-role="page-counter"
          className="min-w-[84px] text-base tabular-nums tracking-[.04em] text-ink"
        >
          {formatViewerCounter(page, pageCount)}
        </span>

        <PageSlider
          bookId={bookId}
          cv={cv}
          page={page}
          pageCount={pageCount}
          dragging={dragging}
          dragPage={dragPage}
          onDragStart={onDragStart}
          onDrag={onDrag}
          onCommit={onCommit}
        />

        <Button
          variant="secondary"
          aria-pressed={stripOpen}
          // `--accent-text` for the pressed state, not `--color-accent-400`: the
          // ramp does not flip and this bar is `--color-bg` inside
          // `data-theme="dark"`, where accent-400 is 3.76 — 3.64 with the paper
          // grain on the bar. `--accent-text` *is* the accent as ink and does
          // flip: 6.22 dry, 5.97 washed.
          className={cn('gap-[7px] border-neutral-700 text-sm', stripOpen && 'text-accent-text')}
          onClick={onToggleStrip}
        >
          <PanelTop size={13} aria-hidden={true} />
          썸네일 · T
        </Button>
      </div>
    </div>
  )
}
