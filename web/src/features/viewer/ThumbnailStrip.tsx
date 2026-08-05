import { useVirtualizer } from '@tanstack/react-virtual'
import { useEffect, useRef } from 'react'

import { useIsMobile, useMediaQuery } from '../../lib/useMediaQuery'
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
 * **Only a short recentre is animated, and the reason is AC-008, not taste.**
 * The prototype gives the strip `scroll-behavior:smooth` and recentres through
 * one `setPage()`, so every page change glides. It can afford that because it
 * caps the strip at 60 thumbs; this strip is virtualised over 1 540. A smooth
 * scroll is a scroll *event per frame*, and every frame moves the virtualiser's
 * window, mounts a new row of cells, and fires one `usePageThumbImage` per cell
 * — each a lazily generated server-side thumbnail. Measured in Chrome against
 * the installed `virtual-core` with this strip's exact configuration (1 540
 * pages, 52px slot, overscan 8, 600px strip), counting the distinct pages
 * mounted during the slider commit that jumps 12 → 1 200: **29 with
 * `behavior:'auto'`, 998 with `behavior:'smooth'`** — the same destination
 * (62 074px), 34× the thumbnail requests. That second number is the stall the
 * header above calls "precisely the stall AC-008 forbids", reintroduced by an
 * animation. So the animation is spent only where it cannot cost anything:
 *
 * > **smooth iff the target is no further than one strip-width from where the
 * > strip already is.** Otherwise instant.
 *
 * That single rule is the whole decision, and it is a rule about *cells
 * crossed*, which is what AC-008 is about. An earlier cut of this component
 * instead named three cases — mount, volume change, slot change — and two of
 * the three were wrong: the volume clause fired a render early (see below) and
 * the mount clause did not survive StrictMode. The rule replaces all three.
 * 다음 권 읽기, a slider commit and a resize are all hundreds of strip-widths and
 * come out instant with nothing naming them. What glides is what the reader is
 * actually watching: an arrow key, space, a tap zone, a click on a neighbouring
 * thumb.
 *
 * Two riders that the distance alone does not express:
 *
 *  - **`prefers-reduced-motion: reduce` is unconditional.** It has to be checked
 *    here in TS: `base.css`'s reduce block can zero a `transition-duration`, but
 *    a scroll asked for in JS with `behavior:'smooth'` is not a transition and
 *    that block does not reach it.
 *  - **The first recentre after the element appears is instant** even when it is
 *    short. `scrollLeft` is 0 there because the element is new — `_willUpdate`
 *    in `virtual-core` explicitly scrolls a newly attached element to its
 *    (unpopulated) stored offset — not because the reader is near the front of
 *    the book, so the distance the rule would measure is a fiction. Measured:
 *    opening on page 12 of 214 in a 600px strip puts the target at 298px, i.e.
 *    within one strip-width, so the distance rule alone animates the *opening*.
 *    `freshRef` is that guard, and its cleanup is what makes it survive
 *    StrictMode (see below).
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
  // The query is written out rather than pulled from a shared constant on
  // purpose: the test stubs `matchMedia` on this exact string, so a typo here
  // is a test failure instead of a hook that quietly never matches.
  const reduceMotion = useMediaQuery('(prefers-reduced-motion: reduce)')
  /** False once this element has been recentred at least once. */
  const freshRef = useRef(true)

  const virtualizer = useVirtualizer({
    count: Math.max(0, pageCount),
    horizontal: true,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => slot,
    overscan: 8,
  })

  const { getOffsetForIndex, measure, scrollToIndex } = virtualizer

  /**
   * The laid-out length of the track — and the recentre's trigger.
   *
   * Read here rather than inline in the JSX because it is the one *public*
   * value that changes exactly when the measurements do. `getTotalSize()` calls
   * `getMeasurements()` (`virtual-core:701`), which is memoised on
   * `[getMeasurementOptions(), itemSizeCache]` and, on a miss, rewrites
   * `measurementsCache` from the current `estimateSize` (`:436`). So a render
   * in which this number changed is a render in which every offset has already
   * been recomputed — see the recentre effect for why that matters.
   */
  const totalSize = virtualizer.getTotalSize()

  /**
   * Put `freshRef` back when this element goes away — including when React only
   * *pretends* it went away.
   *
   * `main.tsx` renders the app in `<StrictMode>`, which in development mounts,
   * unmounts and remounts every component, running effects twice. A one-shot
   * ref that is not restored is `false` by the second mount, so the second
   * recentre takes the distance branch — and the distance it measures is from
   * the 0 that `virtual-core`'s `_willUpdate` has just scrolled the reattached
   * element to. Recorded from `Element.prototype.scrollTo` before this cleanup
   * existed, opening the strip on page 12 of 214: `left=0 undefined`,
   * `left=298 auto`, `left=0 undefined`, **`left=298 smooth`** — the opening
   * animated in exactly the build the developers look at. With the cleanup the
   * fourth call is `left=298 auto` and the two mounts are indistinguishable.
   */
  useEffect(() => {
    return () => {
      freshRef.current = true
    }
  }, [])

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

  /**
   * Recentre the current thumb, gliding only if the trip is short.
   *
   * **`totalSize`, not `slot`, is what makes a resize recentre correctly.** The
   * re-measure moves every offset, so the reader's page has to be brought back
   * into view against the new ones — but keyed on `slot` this effect ran in the
   * *same commit* as `measure()`, and `measure()` only empties the size cache
   * and schedules a render. `getOffsetForIndex` reads `measurementsCache`
   * directly (`virtual-core:600`) rather than going through the memo, so in
   * that commit it still answered with the old pitch. Measured at 1 440 → 700
   * on page 1 200 of a 1 540-page volume: the track grew 80 080 → 92 400px
   * (pitch 52 → 60) while the recentre asked for `left: 62074`, the offset the
   * strip was already on, where the new pitch puts that page at **71 670** —
   * 9 596px, or **160 thumbnails**, off screen. "It covered no ground" was
   * true and beside the point: the effect's own purpose is position, not
   * motion.
   *
   * Keyed on `totalSize` it runs one commit later instead — the commit in which
   * `getTotalSize()` missed its memo, recomputed every measurement and rewrote
   * `measurementsCache`. The offsets are then fresh for both the distance and
   * the scroll, *and* the track element on screen is already the new length.
   * The recentre becomes `left: 71670`, error 0, and the distance rule
   * classifies it on its own: 9 596 > one strip-width, so instant.
   *
   * **Forcing the recompute in place is not enough, measured.** The cheap fix
   * is one line — `virtualizer.getTotalSize()` at the top of this effect, still
   * keyed on `slot` — and it is wrong at the end of the book, because
   * `getOffsetForAlignment` clamps to `scrollWidth - clientWidth` read live off
   * a track the render has not resized yet. Three variants in Chrome on a real
   * 600px scroller with real cells, crossing 52 → 60, reporting landed minus
   * correct:
   *
   *              | page 1 200            | page 1 540 (last)
   *   -----------+-----------------------+----------------------
   *   no fix     | -9 596px (160 thumbs) | -12 320px (205 thumbs)
   *   one line   |      0                | -12 320px (205 thumbs)
   *   totalSize  |      0                |       0
   *
   * The library grid reached the same conclusion from the same symptom and
   * expressed it by splitting into two layout effects; here the dependency is
   * the split.
   *
   * **`bookId` is deliberately not a dependency.** A volume change is *two*
   * renders, not one: the route swaps `bookId` at once (`ViewerPage`), while
   * `pageCount` and `current` come from `useBook`, a plain `useQuery` with no
   * `placeholderData`, so they arrive a network round-trip later. Scrolling on
   * the first of those renders would recentre the *old* page against the *old*
   * measurements for no reason — and, when this was written as a `bookId`
   * clause, it also spent the instant case on that empty render and left the
   * real 1 200 → 1 recentre on the second one animated. Recorded from
   * `Element.prototype.scrollTo`: render A `left=62074 auto`, render B
   * `left=0 smooth`. Keyed on `current`/`pageCount` alone, render A does not
   * scroll at all and render B is a 62 074px trip, i.e. instant by the rule.
   *
   * **`behavior:'smooth'` is legal in this configuration** — checked against the
   * installed `@tanstack/virtual-core@3.13.0`, not assumed. Its guard is
   * `if (behavior === 'smooth' && this.isDynamicMode()) console.warn(…)`, and
   * `isDynamicMode = () => this.elementsCache.size > 0`; that cache is written
   * only by `measureElement`, which this strip never calls — cells are laid out
   * from the constant `estimateSize`. The strip is therefore in static mode and
   * the warning cannot fire. (Static mode also means the library schedules no
   * follow-up correction pass in *either* behaviour: the `setTimeout` at
   * `virtual-core:668` is guarded by `behavior !== 'smooth' && isDynamicMode()`,
   * so both halves of this rule get a single exact scroll.)
   *
   * The clamp the prototype writes by hand —
   * `Math.max(0, Math.min(scrollWidth - clientWidth, target))` — is already
   * `getOffsetForAlignment`'s last line, so page 1 and the last page end up
   * off-centre here exactly as they do there. `getOffsetForIndex` below is the
   * same call `scrollToIndex` makes internally, which is the point: the
   * distance is measured against the offset the scroll will actually use, not
   * against a second copy of the arithmetic that could drift from it.
   */
  useEffect(() => {
    if (pageCount <= 0) return
    const index = Math.max(0, Math.min(pageCount - 1, current - 1))

    const el = scrollRef.current
    // `clientWidth` is the budget, and it needs no companion guard: before
    // layout — and in jsdom, which never has any — it reads 0, so the budget is
    // 0 and the only move that still qualifies as short is one of zero length,
    // which is a scroll that does not happen. Resisting the urge to substitute
    // a default width here is the whole safety property: an unanimated scroll
    // is always correct, an animated one over an unknown distance is the
    // AC-008 stall.
    const viewport = el === null ? 0 : el.clientWidth
    const target = getOffsetForIndex(index, 'center')?.[0]
    const near =
      el !== null && target !== undefined && Math.abs(target - el.scrollLeft) <= viewport

    const fresh = freshRef.current
    freshRef.current = false

    scrollToIndex(index, {
      align: 'center',
      // `'instant'` is not in the library's `ScrollBehavior` union (it is
      // `'auto' | 'smooth'`), so `'auto'` carries the instant case — which is
      // also what the prototype passes. `'auto'` defers to the element's CSS
      // `scroll-behavior`, hence the explicit `auto` pinned in `style` below.
      behavior: near && !fresh && !reduceMotion ? 'smooth' : 'auto',
    })
  }, [current, getOffsetForIndex, pageCount, reduceMotion, scrollToIndex, totalSize])

  return (
    <div
      ref={scrollRef}
      data-role="thumbnail-strip"
      className="overflow-x-auto overflow-y-hidden border-b border-neutral-800 px-4 py-3 [&::-webkit-scrollbar]:hidden"
      // `scrollBehavior:'auto'` pins the instant case, and it is load-bearing
      // rather than decorative. CSSOM-View resolves `scrollTo({behavior:'auto'})`
      // against the element's *computed* `scroll-behavior`, so a
      // `scroll-behavior:smooth` rule reaching this element would turn every
      // instant recentre above into an animation without any argument changing
      // — including the 62 074px one, i.e. AC-008 back through a stylesheet.
      //
      // Measured, not inferred from the spec. Two scrollers identical but for
      // that one property, each sent `scrollTo({left: 1500, behavior: 'auto'})`,
      // reading `scrollLeft` on the next statement and again 600ms later
      // (Chrome 150):
      //
      //   scroll-behavior: smooth  ->  immediate   0, settled 116.5  (animated)
      //   scroll-behavior: auto    ->  immediate 1500, settled 1500  (instant)
      //
      // No stylesheet in this repo sets `scroll-behavior` today, so the pin
      // guards a future edit rather than a present bug — but "guards nothing
      // observed" is not the same as "guards nothing".
      style={{ scrollbarWidth: 'none', scrollBehavior: 'auto' }}
    >
      <div
        className="relative"
        style={{ height: `${String(track)}px`, width: `${String(totalSize)}px` }}
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
