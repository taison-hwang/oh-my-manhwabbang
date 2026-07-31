import { useVirtualizer } from '@tanstack/react-virtual'
import { useEffect, useRef } from 'react'

import { useIsMobile } from '../../lib/useMediaQuery'
import { ThumbnailCell } from './ThumbnailCell'

/**
 * The thumbnail strip (FR-VWR-008, ui-spec §6.7).
 *
 * **Virtualised.** The prototype caps at 60 thumbs; the real collection has a
 * 1 540-page volume, and mounting 1 540 cells means 1 540 lazily generated
 * server-side thumbnails requested at once — which is precisely the stall
 * AC-008 forbids. Only the visible window plus a small overscan is mounted, so
 * a jump in a 1 540-page book costs a handful of requests.
 *
 * The current thumb is scrolled into view on every page change, including page
 * changes that came from the keyboard or a tap zone rather than from the strip.
 *
 * **No visible scrollbar.** ui-spec §6.7 gives the strip `overflow-x:auto` and
 * the reference capture shows no bar — but that capture is macOS, where
 * scrollbars are overlays that vanish when idle. `base.css` styles a permanent
 * 12px bar for every scroller, and on a 72px-tall strip it ate a sixth of the
 * height and cut a grey band across the bottom overlay (measured in Chrome on
 * Linux). The bar is suppressed here, on this one element: wheel, drag and the
 * `scrollToIndex` below all still work, and the strip is a control row whose
 * position is driven by the current page rather than something the reader has
 * to find their way around.
 */
export interface ThumbnailStripProps {
  bookId: string
  cv: string | null
  pageCount: number
  /** 1-based current page. */
  current: number
  onJump: (page: number) => void
}

/**
 * Thumb + 4px gap, at both sizes (ui-spec §6.7).
 *
 * These have to track `ThumbnailCell`'s own `56×84 / md:48×72`. The virtualizer
 * lays cells out by absolute offset and the track is sized from these numbers,
 * so a slot narrower than the cell overlaps every neighbour and a track shorter
 * than the cell clips it: below 768 the strip was drawing 56px cells into 52px
 * slots inside a 72px box, i.e. all three wrong at once.
 */
export const THUMB_SLOT_PX = 52
export const THUMB_SLOT_TOUCH_PX = 60
/** Track height — the cell's own height, which the strip's padding sits around. */
export const THUMB_TRACK_PX = 72
export const THUMB_TRACK_TOUCH_PX = 84

export function ThumbnailStrip({
  bookId,
  cv,
  pageCount,
  current,
  onJump,
}: ThumbnailStripProps) {
  const scrollRef = useRef<HTMLDivElement | null>(null)
  const touch = useIsMobile()
  const slot = touch ? THUMB_SLOT_TOUCH_PX : THUMB_SLOT_PX
  const track = touch ? THUMB_TRACK_TOUCH_PX : THUMB_TRACK_PX

  const virtualizer = useVirtualizer({
    count: Math.max(0, pageCount),
    horizontal: true,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => slot,
    overscan: 8,
  })

  const { measure, scrollToIndex } = virtualizer

  /**
   * Re-lay-out when the slot size changes — `estimateSize` alone does not.
   *
   * `virtual-core` memoises `getMeasurements` on
   * `[count, paddingStart, scrollMargin, getItemKey, enabled]` and the item-size
   * cache; **`estimateSize` is not in that key**. So handing it a new function
   * changes nothing: the cached offsets stay. Measured at 900 → 700 with the
   * strip open, the cells grew to 56px (CSS) while the pitch stayed 52px — four
   * pixels of overlap on every thumb — and the track stayed 5 044px against the
   * 5 820px the 97 pages then needed, so the last 776px were unreachable.
   * `measure()` swaps the size cache for a fresh Map, which *is* in the key.
   */
  useEffect(() => {
    measure()
  }, [measure, slot])

  // `slot` is a dependency here too: the re-measure above moves every offset,
  // so the page the reader is on has to be brought back into view against the
  // new ones.
  useEffect(() => {
    if (pageCount <= 0) return
    scrollToIndex(Math.max(0, Math.min(pageCount - 1, current - 1)), { align: 'center' })
  }, [current, pageCount, scrollToIndex, slot])

  return (
    <div
      ref={scrollRef}
      data-role="thumbnail-strip"
      className="overflow-x-auto overflow-y-hidden border-b border-neutral-800 px-4 py-3 [&::-webkit-scrollbar]:hidden"
      style={{ scrollbarWidth: 'none' }}
    >
      <div
        className="relative"
        style={{ height: `${String(track)}px`, width: `${String(virtualizer.getTotalSize())}px` }}
      >
        {virtualizer.getVirtualItems().map((item) => (
          <div
            key={item.key}
            className="absolute left-0 top-0 h-full"
            style={{ transform: `translateX(${String(item.start)}px)` }}
          >
            <ThumbnailCell
              bookId={bookId}
              page={item.index + 1}
              cv={cv}
              current={item.index + 1 === current}
              onJump={onJump}
            />
          </div>
        ))}
      </div>
    </div>
  )
}
