import type { ChangeEvent } from 'react'

import { usePageThumbImage } from '../../api/queries'
import { thumbWidthFor } from '../../api/urls'
import type { ReadingDirection } from '../../store/viewer'
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
 * **The hit box is a stylesheet rule, not an inline height.** `styles/base.css`
 * sizes every range input — 24 px normally, `--touch-min` (44 px) below 768 —
 * because a range with no height collapses onto its 2 px track and the thumb
 * overflows a hit area two pixels tall. This slider used to set 44 px inline at
 * every width, which held ui-spec §7's touch minimum but made the bottom bar
 * 12 px taller than the design on every desktop.
 *
 * `on-dark` is the second half of the same rule: on the reading ground
 * `--color-divider` is all but the background colour, so the viewer's track is
 * lifted to `--color-neutral-600` and the thumb has something to travel along.
 *
 * **The travel follows the reading direction.** In R→L the book opens at the
 * right, so page 1 is the right end of the track and progress moves the thumb
 * leftwards — the same axis the page stage, the arrow keys and the tap zones
 * already flip (`fit.ts`, `useViewerKeys`, `useTouchZones`). This one control
 * was reading left-to-right in every book, so at 174/184 of an R→L volume the
 * thumb sat at the far right while the reader's sense of "nearly done" pointed
 * the other way.
 *
 * It is done with `dir` on the input rather than by inverting `value`, so the
 * engine owns the mirroring: the thumb geometry, the pointer mapping and the
 * arrow keys (which per spec reverse in RTL) all move together, and `value`,
 * `aria-valuetext` and the commit handlers keep saying the real page number.
 * Verified in Chromium 145 against this exact styling — `appearance:none` with
 * custom track and thumb pseudo-elements — because a mirroring the engine
 * ignored would have been invisible in jsdom.
 */

export interface PageSliderProps {
  bookId: string
  cv: string | null
  page: number
  pageCount: number
  /** R→L mirrors the track: page 1 sits at the right end. */
  dir: ReadingDirection
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
  dir,
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
        dir={dir}
        min={1}
        max={Math.max(1, pageCount)}
        step={1}
        value={value}
        aria-label="페이지"
        aria-valuetext={`${String(value)} / ${String(pageCount)}`}
        className="on-dark w-full cursor-pointer bg-transparent"
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
          // E-32 §1: the drag preview's border is a `--color-hot` marker — it
          // says "this is the page you are pointing at", which is the same
          // "current" signal the strip's cell carries. The accent it used to be
          // is a deep teal at 1.2:1 on the preview's own dark stripes.
          className="pointer-events-none absolute bottom-6 flex h-[102px] w-[68px] items-end overflow-hidden rounded-sm border-2 border-hot p-1 text-xs tabular-nums text-ink"
          // The percentage is mirrored here rather than left to `dir`, because
          // this is a `position:absolute` box on the wrapper and `left` is a
          // physical edge: `dir` on the input does not reach it, so an R→L drag
          // would have parked the preview on the opposite side of the track
          // from the thumb it belongs to.
          style={{
            left: `${String(
              dir === 'rtl'
                ? 100 - sliderPercent(previewPage, pageCount)
                : sliderPercent(previewPage, pageCount),
            )}%`,
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
