import type { ChangeEvent } from 'react'

import { usePageThumbImage } from '../../api/queries'
import { thumbWidthFor } from '../../api/urls'
import { sliderPercent } from './fit'

/**
 * The page slider and its drag preview (ui-spec §6.7, FR-VWR-008).
 *
 * The page is **committed on release only**. Dragging across a 1 540-page book
 * would otherwise fire a page load — and a progress write — for every
 * intermediate value; instead the store holds `dragPage`, the preview shows
 * that page's 68×102 thumbnail, and the stage does not move until pointer-up.
 *
 * Keyboard use of the range input has no pointer-down, so a bare `change`
 * commits immediately: arrow keys on a focused slider must still work.
 *
 * The input carries an explicit **44 px** box. `styles/base.css` gives the range
 * a 2 px `::-webkit-slider-runnable-track` and no element height, so the border
 * box collapses onto the track and the thumb overflows a hit area two pixels
 * tall — unusable with a finger and below the 44×44 minimum ui-spec §7 holds at
 * every width. The height is on the input rather than on a wrapper because it is
 * the input that has to receive the touch; the track still paints as 2 px,
 * vertically centred in the taller box.
 */

/** ui-spec §7's minimum touch target — and the slider is the control a finger drags. */
export const SLIDER_HIT_HEIGHT_PX = 44

export interface PageSliderProps {
  bookId: string
  cv: string | null
  page: number
  pageCount: number
  dragging: boolean
  /** The page under the thumb while dragging; `null` when idle. */
  dragPage: number | null
  onDragStart: (page: number) => void
  onDrag: (page: number) => void
  onCommit: (page: number) => void
}

export function PageSlider({
  bookId,
  cv,
  page,
  pageCount,
  dragging,
  dragPage,
  onDragStart,
  onDrag,
  onCommit,
}: PageSliderProps) {
  const value = dragging && dragPage !== null ? dragPage : page
  const previewPage = dragPage ?? page
  const preview = usePageThumbImage(bookId, previewPage, {
    w: thumbWidthFor('sliderPreview'),
    v: cv,
    enabled: dragging && pageCount > 0,
  })

  const handleChange = (event: ChangeEvent<HTMLInputElement>): void => {
    const next = Number(event.target.value)
    if (!Number.isFinite(next)) return
    if (dragging) onDrag(next)
    else onCommit(next)
  }

  const commit = (): void => {
    if (!dragging) return
    onCommit(dragPage ?? page)
  }

  return (
    <div className="relative flex flex-1 items-center" data-role="page-slider">
      <input
        type="range"
        min={1}
        max={Math.max(1, pageCount)}
        step={1}
        value={value}
        aria-label="페이지"
        aria-valuetext={`${String(value)} / ${String(pageCount)}`}
        className="w-full cursor-pointer bg-transparent"
        style={{ height: `${String(SLIDER_HIT_HEIGHT_PX)}px` }}
        onChange={handleChange}
        onMouseDown={() => {
          onDragStart(page)
        }}
        onTouchStart={() => {
          onDragStart(page)
        }}
        onMouseUp={commit}
        onTouchEnd={commit}
        onBlur={commit}
      />
      {dragging && (
        <div
          data-role="slider-preview"
          className="pointer-events-none absolute bottom-6 flex h-[102px] w-[68px] items-end border-2 border-accent p-1 text-xs tabular-nums text-ink"
          style={{
            left: `${String(sliderPercent(previewPage, pageCount))}%`,
            transform: 'translateX(-50%)',
          }}
        >
          {preview.status === 'ready' && (
            <img
              src={preview.url}
              alt=""
              aria-hidden="true"
              className="absolute inset-0 h-full w-full object-cover"
            />
          )}
          <span className="relative">{previewPage}</span>
        </div>
      )}
    </div>
  )
}
