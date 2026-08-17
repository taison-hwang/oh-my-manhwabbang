import { PanelTop } from 'lucide-react'

import { Button } from '../../components/ds/Button'
import { cn } from '../../lib/cn'
import { formatViewerCounter } from '../../lib/format'
import type { ReadingDirection } from '../../store/viewer'
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
  /** Both navigators in this bar run right-to-left in an R→L volume. */
  dir: ReadingDirection
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
  dir,
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
          dir={dir}
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
          dir={dir}
          dragging={dragging}
          dragPage={dragPage}
          onDragStart={onDragStart}
          onDrag={onDrag}
          onCommit={onCommit}
        />

        <Button
          variant="secondary"
          aria-pressed={stripOpen}
          // `--on-control-accent` for the pressed state, and no border at all.
          //
          // The ground moved (E-36 §4 / E-42): this label is no longer ink on
          // the bar's `--color-bg`, it is ink on the button's own **cream**
          // fill, which is absolute and stays cream inside `data-theme="dark"`.
          // The old choice here — `--accent-text` over `--color-accent-400`,
          // 6.22 vs 3.76 as ink on the dark bar — was decided against a ground
          // this button no longer has: `--accent-text` is a pale teal in the dark
          // theme and **1.65:1** on cream, so the pressed state would have
          // erased its own label. `--on-control-accent` is the absolute accent
          // ink for that fill — 7.06 washed — and is the same ink
          // `.btn-secondary:hover` swaps to, so pressed and hovered agree.
          //
          // `border-neutral-700` is gone with it: `.btn` has `border: 0` now,
          // and a colour utility for a border that does not exist is a hairline
          // waiting to come back the day someone restores the border.
          className={cn('gap-[7px] text-sm', stripOpen && 'text-on-control-accent')}
          onClick={onToggleStrip}
        >
          <PanelTop size={13} aria-hidden={true} />
          썸네일 · T
        </Button>
      </div>
    </div>
  )
}
