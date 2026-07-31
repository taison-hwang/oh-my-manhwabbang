import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
  type RefObject,
  type UIEvent,
} from 'react'

import type { PageInfo } from '../../api/types'
import type { DisplayMode, FitMode, ReadingDirection } from '../../store/viewer'
import {
  makeDimsLookup,
  pageAtScroll,
  scrollTopForPage,
  stagePages,
  stageStyle,
  verticalPageHeight,
  verticalWindow,
  type PageDims,
} from './fit'
import { PageFrame } from './PageFrame'

/**
 * The reading stage (ui-spec §6.2) — 단면 / 양면 / 세로.
 *
 * The RTL rule lives entirely in `stageStyle`: the DOM order is **always
 * ascending**, and `flex-direction: row-reverse` is what puts page *n* on the
 * right and *n+1* on the left. Reversing the array as well would cancel out.
 *
 * The horizontal frames are keyed by **slot**, not by page number. That is what
 * makes ui-spec §6.3's "never blank the stage" reachable at all: a `key={n}`
 * unmounts the frame on every turn, so the decoded previous page it was holding
 * goes with it and the reader stares at the empty ground for the whole of the
 * next page's load. Keyed by slot, the same `PageFrame` instance receives a new
 * `page` prop and keeps the old image painted underneath the new one.
 *
 * 세로 (webtoon) mounts every page of the book so the scrollbar tells the truth
 * about a 1 540-page volume, but only the pages inside `verticalWindow` carry
 * an `<img>`; the rest are spacers **whose height comes from the same rule the
 * live frame is laid out by** (`verticalPageHeight`), so the document does not
 * resize under the reader every time the window slides.
 *
 * ## In 세로, the scroll position *is* the current page
 *
 * That symmetry is one-directional in the DOM: scrolling reports a page (the
 * `onScroll` handler below), but setting a page moves nothing. Measured in
 * Chrome, switching to 세로 at page 100 of a 214-page volume left `scrollTop` at
 * 0 while the stage placed page 100 at y ≈ 90 288 of a 187 860 px document, so
 * the reader was ~90 000 px above their own page and the screen read as blank.
 * The same hole swallowed a resume: entering the viewer part-read in 세로
 * started at page 1 whatever the progress row said.
 *
 * The aligning effect below closes it, and the only hard part is not fighting
 * the scroll handler. `alignedPageRef` records the page the scroller is *known*
 * to be showing — written both when the effect scrolls and when `onScroll`
 * reports a new page — so a page change that came from the reader's own wheel is
 * never answered by snapping them back to that page's top edge.
 *
 * ## Every offset in here is a function of the **geometry**, not just the page
 *
 * `geomKey` is book + fit + measured stage size, because that is exactly the
 * argument list of `verticalPageHeight`: change any one of them and every page
 * in the book is a different height, so the `scrollTop` that showed page 101 now
 * shows some other page. Keying the alignment on the page alone left the fit
 * controls as a trapdoor — measured at 1 440×900 in a 187-page book, switching
 * 높이 → 너비 at page 101 grew the document from 170 532 px to 402 786 px while
 * `scrollTop` stayed at 91 688, so the reader was dropped on page 43 while the
 * counter, the prefetcher and the progress PUT all still said 101.
 *
 * The stage size is in the key for a second reason: the first commit after
 * entering 세로 measures 0×0, so every spacer is 0px tall and an alignment
 * computed then is worthless. Remeasuring re-keys the effect and it aligns
 * again, which is also what makes a window resize keep the reader on their page.
 *
 * `alignedGeomRef` is then what stops the aligner and the browser from fighting.
 * A shorter document makes the browser clamp `scrollTop` and fire `scroll` on
 * its own, reporting a page the reader never navigated to; adopting it would be
 * the same teleport by another route. Scroll events are only the reader's while
 * the geometry they were produced against is the one the scroller was last
 * aligned for.
 */
export interface PageStageProps {
  bookId: string
  cv: string | null
  /** `BookDetail.pages`, natural-sorted, `n = 1..page_count`. */
  pages: readonly PageInfo[]
  page: number
  pageCount: number
  mode: DisplayMode
  fit: FitMode
  dir: ReadingDirection
  /** Natural sizes learned from decoded images (FR-VWR-004 fallback). */
  measured: ReadonlyMap<number, PageDims>
  /** The book-level index error, used as the failure cause when there is one. */
  cause?: string | null
  onPageLoaded: (page: number, width: number, height: number) => void
  /** A page image failed — the caller stops the loading indicator. */
  onPageFailed: (page: number) => void
  /** 세로 only: the reader scrolled onto another page. */
  onScrollToPage: (page: number) => void
}

interface StageSize {
  width: number
  height: number
}

/**
 * The stage's own content box, remeasured on resize.
 *
 * `ResizeObserver` would be tighter but does not exist in jsdom, and a stage
 * that throws in the test environment is worse than one that reads its size on
 * mount and on `resize` — the only thing that changes it here is the viewport.
 */
function useStageSize(ref: RefObject<HTMLDivElement | null>, active: boolean): StageSize {
  const [size, setSize] = useState<StageSize>({ width: 0, height: 0 })

  useEffect(() => {
    if (!active) return undefined
    const measure = (): void => {
      const el = ref.current
      if (el === null) return
      const width = el.clientWidth
      const height = el.clientHeight
      setSize((previous) =>
        previous.width === width && previous.height === height ? previous : { width, height },
      )
    }
    measure()
    window.addEventListener('resize', measure)
    return () => {
      window.removeEventListener('resize', measure)
    }
  }, [active, ref])

  return size
}

/**
 * The top edge of every direct child, in the scroller's own coordinates.
 *
 * `offsetTop` is a layout position and is unaffected by scrolling, so
 * `child.offsetTop - stage.offsetTop` is exactly the `scrollTop` that brings
 * that child to the top of the stage. `onScroll` and the aligner must derive
 * their tops the same way or they disagree by the stage's own offset.
 */
function childTops(stage: HTMLElement): number[] {
  const tops: number[] = []
  for (const child of Array.from(stage.children)) {
    tops.push((child as HTMLElement).offsetTop - stage.offsetTop)
  }
  return tops
}

export function PageStage({
  bookId,
  cv,
  pages,
  page,
  pageCount,
  mode,
  fit,
  dir,
  measured,
  cause,
  onPageLoaded,
  onPageFailed,
  onScrollToPage,
}: PageStageProps) {
  const stageRef = useRef<HTMLDivElement | null>(null)
  const stageSize = useStageSize(stageRef, mode === 'vertical')
  /** The geometry the scroller was last aligned against — see the note above. */
  const alignedGeomRef = useRef<string | null>(null)
  /** The page it was aligned to, or that `onScroll` has since reported. */
  const alignedPageRef = useRef<number | null>(null)

  /**
   * Everything `verticalPageHeight` is a function of: which book, which fit, and
   * how big the stage is. A change to any of them re-lays out every page, so
   * every offset — and therefore the current scroll position — is stale.
   */
  const geomKey = `${bookId}|${fit}|${String(stageSize.width)}x${String(stageSize.height)}`

  const dims = useMemo(() => makeDimsLookup(pages, measured), [pages, measured])
  const byNumber = useMemo(() => {
    const map = new Map<number, PageInfo>()
    for (const info of pages) map.set(info.n, info)
    return map
  }, [pages])

  const nameOf = useCallback((n: number): string => byNumber.get(n)?.name ?? String(n), [byNumber])

  const onScroll = useCallback(
    (event: UIEvent<HTMLDivElement>): void => {
      if (mode !== 'vertical') return
      // Not the reader: the geometry changed under the scroller and the effect
      // below has not re-aligned it yet, so this event is either the browser
      // clamping `scrollTop` into a shorter document or a stale position in a
      // taller one. Either way the page it reports is one nobody navigated to.
      if (alignedGeomRef.current !== geomKey) return
      const stage = event.currentTarget
      const next = pageAtScroll(childTops(stage), stage.scrollTop)
      if (next === page) return
      // The reader moved: record it as the aligned page so the effect below
      // does not answer their scroll by snapping to that page's top edge.
      alignedPageRef.current = next
      onScrollToPage(next)
    },
    [geomKey, mode, onScrollToPage, page],
  )

  // FR-VWR-003 / FR-VWR-009: in 세로 the scroll position is the current page.
  useEffect(() => {
    if (mode !== 'vertical') {
      // Leaving 세로 throws the scroller away, so coming back must re-align
      // even if the reader is on the same page of the same book.
      alignedGeomRef.current = null
      alignedPageRef.current = null
      return
    }
    const stage = stageRef.current
    if (stage === null || pageCount <= 0) return
    if (alignedGeomRef.current === geomKey && alignedPageRef.current === page) return
    alignedGeomRef.current = geomKey
    alignedPageRef.current = page
    stage.scrollTop = scrollTopForPage(
      childTops(stage),
      page,
      stage.scrollHeight - stage.clientHeight,
    )
  }, [geomKey, mode, page, pageCount])

  const style = stageStyle(mode, fit, dir)

  if (mode === 'vertical') {
    const [start, end] = verticalWindow(page, pageCount)
    const children: ReactNode[] = []
    for (let n = 1; n <= pageCount; n++) {
      if (n >= start && n <= end) {
        children.push(
          <PageFrame
            key={n}
            bookId={bookId}
            page={n}
            cv={cv}
            name={nameOf(n)}
            fit={fit}
            mode={mode}
            cause={cause ?? null}
            onLoaded={onPageLoaded}
            onFailed={onPageFailed}
            className="max-w-full"
          />,
        )
      } else {
        const height = verticalPageHeight(dims(n), fit, stageSize.width, stageSize.height)
        children.push(
          <div
            key={n}
            data-role="page-spacer"
            data-page={n}
            className="w-full"
            style={{ flex: '0 0 auto', height: `${String(height)}px` }}
          />,
        )
      }
    }
    return (
      <div
        ref={stageRef}
        data-role="stage"
        data-mode={mode}
        data-dir={dir}
        data-fit={fit}
        className="min-h-0 flex-1"
        style={style}
        onScroll={onScroll}
      >
        {children}
      </div>
    )
  }

  const shown = stagePages(page, pageCount, mode, dims)
  return (
    <div
      ref={stageRef}
      data-role="stage"
      data-mode={mode}
      data-dir={dir}
      data-fit={fit}
      data-flow={style.flexDirection}
      className="min-h-0 flex-1"
      style={style}
    >
      {shown.map((n, slot) => (
        <PageFrame
          // Slot, not page number — see the note at the top of this file.
          key={`slot-${String(slot)}`}
          bookId={bookId}
          page={n}
          cv={cv}
          name={nameOf(n)}
          fit={fit}
          mode={mode}
          cause={cause ?? null}
          onLoaded={onPageLoaded}
          onFailed={onPageFailed}
        />
      ))}
    </div>
  )
}
