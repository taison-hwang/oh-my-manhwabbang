import { useVirtualizer } from '@tanstack/react-virtual'
import { useEffect, useRef } from 'react'

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

/** 48px thumb + 4px gap (ui-spec §6.7). */
export const THUMB_SLOT_PX = 52

export function ThumbnailStrip({
  bookId,
  cv,
  pageCount,
  current,
  onJump,
}: ThumbnailStripProps) {
  const scrollRef = useRef<HTMLDivElement | null>(null)

  const virtualizer = useVirtualizer({
    count: Math.max(0, pageCount),
    horizontal: true,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => THUMB_SLOT_PX,
    overscan: 8,
  })

  const { scrollToIndex } = virtualizer
  useEffect(() => {
    if (pageCount <= 0) return
    scrollToIndex(Math.max(0, Math.min(pageCount - 1, current - 1)), { align: 'center' })
  }, [current, pageCount, scrollToIndex])

  return (
    <div
      ref={scrollRef}
      data-role="thumbnail-strip"
      className="overflow-x-auto overflow-y-hidden border-b border-neutral-800 px-4 py-3 [&::-webkit-scrollbar]:hidden"
      style={{ scrollbarWidth: 'none' }}
    >
      <div
        className="relative h-[72px]"
        style={{ width: `${String(virtualizer.getTotalSize())}px` }}
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
