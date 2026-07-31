/**
 * The viewer's layout maths (ui-spec §6.2, FR-VWR-003/-004/-005).
 *
 * Everything here is pure: no React, no DOM, no store. That is deliberate —
 * the two rules this file encodes are the two the whole screen is judged on,
 * and both are far easier to get wrong than to test:
 *
 *  1. **RTL spread.** With `R→L` the flow container is `row-reverse`, so page
 *     *n* renders on the **right** and *n+1* on the left. The DOM order stays
 *     ascending; only the flex direction flips. Reversing the array *and*
 *     using `row-reverse` cancels out and silently produces LTR — which is why
 *     `stagePages` is specified to return ascending page numbers and the flex
 *     direction is a separate function.
 *  2. **Landscape auto-split** (FR-VWR-004). A page wider than it is tall is a
 *     double-page scan and is shown alone even in 양면 mode; so is the page
 *     *before* one, because pairing a portrait page with a spread scan is what
 *     puts the whole book one page out of phase.
 *
 * The step functions follow from (2): the page turn advances by however many
 * pages are actually on screen, not by a hard-coded 2. In a book of portrait
 * pages that is exactly the "+2 in 양면" rule of ui-spec §8.2.
 */

import type { CSSProperties } from 'react'

import type { PageInfo } from '../../api/types'
import type { DisplayMode, FitMode, ReadingDirection } from '../../store/viewer'

/** Intrinsic page size, from `PageInfo.w/h` or from a loaded image. */
export interface PageDims {
  w: number | null
  h: number | null
}

/** Dimensions for a 1-based page number, or `undefined` when unknown. */
export type DimsLookup = (page: number) => PageDims | undefined

/**
 * How many pages the 세로 stage keeps live around the current one.
 *
 * Vertical mode renders every page of the book so the scrollbar is honest, but
 * only the pages inside this window carry an `<img>`; the rest are spacers.
 */
export const VERTICAL_WINDOW_AHEAD = 4
export const VERTICAL_WINDOW_BEHIND = 1

/** Stage gaps and padding, ui-spec §6.2. */
export const STAGE_GAP_PX = 2
export const VERTICAL_GAP_PX = 12
export const STAGE_PADDING_PX = 20

/**
 * FR-VWR-004: a landscape page is a two-page scan and is never paired.
 *
 * Unknown dimensions (`w`/`h` still `null` — `dims_state: "none" | "partial"`)
 * are *not* landscape: the API fills them in lazily, and guessing "wide" from
 * missing data would break spread mode for every book whose dimension pass has
 * not run yet.
 */
export function isLandscape(dims: PageDims | undefined): boolean {
  if (dims === undefined) return false
  const { w, h } = dims
  if (w === null || h === null || w <= 0 || h <= 0) return false
  return w > h
}

/** Clamps a 1-based page into `[1, pageCount]`. */
export function clampPage(page: number, pageCount: number): number {
  if (pageCount <= 0) return 1
  return Math.max(1, Math.min(pageCount, Math.trunc(page)))
}

/**
 * The pages on screen at `page`, **ascending**, for the horizontal modes.
 *
 * `[n]` in 단면; `[n, n+1]` in 양면 unless either page is landscape or `n` is
 * the last page. Vertical mode has its own window function.
 */
export function stagePages(
  page: number,
  pageCount: number,
  mode: DisplayMode,
  dims: DimsLookup,
): number[] {
  if (pageCount <= 0) return []
  const first = clampPage(page, pageCount)
  if (mode !== 'spread') return [first]
  if (first + 1 > pageCount) return [first]
  if (isLandscape(dims(first))) return [first]
  if (isLandscape(dims(first + 1))) return [first]
  return [first, first + 1]
}

/** The inclusive `[start, end]` page range 세로 mode keeps loaded. */
export function verticalWindow(page: number, pageCount: number): [number, number] {
  if (pageCount <= 0) return [1, 0]
  const current = clampPage(page, pageCount)
  return [
    Math.max(1, current - VERTICAL_WINDOW_BEHIND),
    Math.min(pageCount, current + VERTICAL_WINDOW_AHEAD),
  ]
}

/** Forward one screen: `+1` in 단면/세로, `+2` in 양면 with two portrait pages. */
export function nextPage(
  page: number,
  pageCount: number,
  mode: DisplayMode,
  dims: DimsLookup,
): number {
  if (pageCount <= 0) return 1
  const shown = stagePages(page, pageCount, mode, dims)
  return clampPage(clampPage(page, pageCount) + Math.max(1, shown.length), pageCount)
}

/**
 * Back one screen.
 *
 * In 양면 the target is the *start* of the spread that ends at `page - 1`, so
 * paging backwards through a book containing a landscape scan lands on the
 * same pairs it showed on the way forward.
 */
export function prevPage(
  page: number,
  pageCount: number,
  mode: DisplayMode,
  dims: DimsLookup,
): number {
  if (pageCount <= 0) return 1
  const current = clampPage(page, pageCount)
  if (current <= 1) return 1
  if (mode !== 'spread') return current - 1
  const candidate = Math.max(1, current - 2)
  return stagePages(candidate, pageCount, mode, dims).includes(current - 1)
    ? candidate
    : current - 1
}

/** `row-reverse` under `R→L` — the one rule this screen is judged on. */
export function stageFlexDirection(
  mode: DisplayMode,
  dir: ReadingDirection,
): 'row' | 'row-reverse' | 'column' {
  if (mode === 'vertical') return 'column'
  return dir === 'rtl' ? 'row-reverse' : 'row'
}

/**
 * True when a fit lets the page exceed the stage vertically, so the stage has
 * to scroll rather than clip.
 *
 * 원본 is the case ui-spec §6.2 states outright ("stage padding drops to 0,
 * stage scrolls"). 너비 is the same situation arrived at from the other side: a
 * portrait scan fitted to the stage *width* is always taller than a landscape
 * stage, so `overflow: hidden` would cut the top and bottom off every page and
 * make 너비 unusable. The spec table's `hidden` is the horizontal-flow rule —
 * the axis 양면 needs clipped — and it is kept on that axis.
 */
export function stageScrollsY(fit: FitMode): boolean {
  return fit === 'width' || fit === 'original'
}

/** The stage container's geometry (ui-spec §6.2). */
export function stageStyle(
  mode: DisplayMode,
  fit: FitMode,
  dir: ReadingDirection,
): CSSProperties {
  if (mode === 'vertical') {
    return {
      display: 'flex',
      flexDirection: 'column',
      alignItems: 'center',
      justifyContent: 'flex-start',
      gap: `${String(VERTICAL_GAP_PX)}px`,
      padding: 0,
      overflowX: 'hidden',
      overflowY: 'auto',
    }
  }
  // 원본 keeps the intrinsic size, so the stage loses its padding and scrolls.
  const original = fit === 'original'
  const scrollsY = stageScrollsY(fit)
  return {
    display: 'flex',
    flexDirection: stageFlexDirection(mode, dir),
    // A centred flex item that overflows its scroll container puts the start of
    // the overflow out of reach — the classic "can't scroll up to the top of the
    // image". Anchor to the start whenever the stage scrolls.
    alignItems: scrollsY ? 'flex-start' : 'center',
    justifyContent: original ? 'flex-start' : 'center',
    gap: `${String(STAGE_GAP_PX)}px`,
    padding: original ? 0 : `${String(STAGE_PADDING_PX)}px`,
    overflowX: original ? 'auto' : 'hidden',
    overflowY: scrollsY ? 'auto' : 'hidden',
  }
}

/**
 * The **page frame**'s own box — the flex item the `<img>` lives in.
 *
 * This is what makes `pageFitStyle` mean anything. Every fit rule in ui-spec
 * §6.2 is expressed in percentages (`height:100%`, `max-width:100%`), and a
 * percentage resolves against the *containing block*, not against the stage. A
 * frame sized by its content is not a containing block with a definite size, so
 * `height:100%` computes to `auto` and `max-height:100%` to `none`: all four
 * fits collapse to the intrinsic size and the default 높이 clips the page.
 *
 * So the frame is sized from the stage, one rule per fit:
 *
 *  * 너비 — `flex: 1 1 0` gives a definite width (the whole stage in 단면, half
 *    of it in 양면); height follows the image.
 *  * 높이 — `height: 100%` against the stage's definite height; width follows.
 *  * 화면 — both: a definite box the image is contained inside.
 *  * 원본 — neither; the frame is exactly the intrinsic page.
 */
export function pageFrameStyle(fit: FitMode, mode: DisplayMode): CSSProperties {
  const base: CSSProperties = {
    position: 'relative',
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'center',
    minWidth: 0,
    minHeight: 0,
  }
  if (mode === 'vertical') {
    // 세로 stacks full-width pages, so the width is always the stage's; only
    // 높이/화면 additionally need a definite height to resolve against.
    switch (fit) {
      case 'original':
        return { ...base, flex: '0 0 auto' }
      case 'width':
        return { ...base, flex: '0 0 auto', width: '100%' }
      case 'height':
      case 'contain':
        return { ...base, flex: '0 0 auto', width: '100%', height: '100%' }
    }
  }
  switch (fit) {
    case 'width':
      // Top-anchored: the page is taller than the stage and scrolls downwards.
      return { ...base, flex: '1 1 0', alignItems: 'flex-start' }
    case 'height':
      return { ...base, flex: '0 0 auto', height: '100%' }
    case 'contain':
      return { ...base, flex: '1 1 0', height: '100%' }
    case 'original':
      return { ...base, flex: '0 0 auto' }
  }
}

/**
 * The fit rule (FR-VWR-005), applied to the page image.
 *
 * Wire values, not labels: `contain` is 화면 (C-2). There is no `screen`.
 *
 * Only meaningful against a frame sized by `pageFrameStyle` — see there.
 */
export function pageFitStyle(fit: FitMode): CSSProperties {
  const base: CSSProperties = { display: 'block' }
  switch (fit) {
    case 'width':
      return { ...base, width: '100%', height: 'auto', maxWidth: '100%', maxHeight: 'none' }
    case 'height':
      return { ...base, height: '100%', width: 'auto', maxHeight: '100%', maxWidth: 'none' }
    case 'original':
      return { ...base, width: 'auto', height: 'auto', maxWidth: 'none', maxHeight: 'none' }
    case 'contain':
      return { ...base, maxWidth: '100%', maxHeight: '100%', width: 'auto', height: 'auto' }
  }
}

/** Aspect ratio (h / w) assumed for a page whose dimensions are not known yet. */
export const FALLBACK_PAGE_RATIO = 3 / 2

/**
 * The height a 세로 page occupies — used for **both** the live frame's
 * placeholder and the out-of-window spacer.
 *
 * The two must agree. A spacer that is taller than the page it stands in for
 * makes `scrollHeight` a lie, and every window slide then resizes the document
 * under the reader: measured at 1 440×900 the old fixed `2 / 3` spacer differed
 * from the rendered frame by 150 px *per page*, so scrolling 2 000 px shrank a
 * 214-page document by 150 px and a 1 540-page volume's scrollbar was ~12 % out.
 *
 * Deriving it from the same inputs the frame is laid out from — the page's
 * intrinsic aspect (from `PageInfo.w/h`, or the natural size once an image has
 * decoded) and the active fit — makes the estimate converge to the truth as
 * pages load instead of drifting.
 */
export function verticalPageHeight(
  dims: PageDims | undefined,
  fit: FitMode,
  stageWidth: number,
  stageHeight: number,
): number {
  const w = dims?.w ?? null
  const h = dims?.h ?? null
  const known = w !== null && h !== null && w > 0 && h > 0
  const ratio = known ? h / w : FALLBACK_PAGE_RATIO
  const fitted = Math.round(Math.max(0, stageWidth) * ratio)
  switch (fit) {
    case 'width':
      return fitted
    case 'height':
      return Math.max(0, stageHeight)
    case 'contain':
      return Math.min(Math.max(0, stageHeight), fitted)
    case 'original':
      return known ? h : fitted
  }
}

/**
 * The 1-based page whose top edge is the last one at or above `scrollTop`.
 *
 * Pure so the webtoon scroll mapping is testable: jsdom reports every
 * `offsetTop` as 0, and a scroll handler that is only exercised in a browser is
 * a scroll handler nobody has checked. It lives here rather than beside the
 * component so `PageStage.tsx` exports components only (react-refresh).
 */
export function pageAtScroll(tops: readonly number[], scrollTop: number): number {
  let page = 1
  for (let i = 0; i < tops.length; i++) {
    const top = tops[i]
    if (top === undefined) continue
    if (top <= scrollTop + 1) page = i + 1
    else break
  }
  return page
}

/**
 * The inverse of `pageAtScroll`: where the 세로 scroller must sit for `page` to
 * be the page the reader is looking at.
 *
 * This is the whole of the blocker WP-15 was raised for. 세로 mounts every page
 * of the book, so switching to it at page 100 of a 214-page volume put that
 * page at y ≈ 90 288 in a 187 860 px document — while `scrollTop` stayed 0. The
 * stage was not blank; it was showing page 1, ~90 000 px above where the reader
 * was. Nothing in the mode switch moved the scroller, and nothing else could:
 * the scroll position *is* the current page in this mode, so entering it (or
 * resuming into it part-read, or jumping with the slider) has to set it.
 *
 * `maxScrollTop` is `scrollHeight - clientHeight`. Clamping here rather than
 * letting the browser do it keeps the result deterministic: an over-large
 * `scrollTop` is silently clamped by the DOM, which then fires a `scroll` event
 * reporting a *different* page, and the stage bounces to it. A non-positive or
 * non-finite value means "unknown" (jsdom measures every box as 0) and does not
 * clamp.
 */
export function scrollTopForPage(
  tops: readonly number[],
  page: number,
  maxScrollTop: number,
): number {
  if (tops.length === 0) return 0
  const index = Math.max(0, Math.min(tops.length - 1, Math.trunc(page) - 1))
  const top = Math.max(0, tops[index] ?? 0)
  if (!Number.isFinite(maxScrollTop) || maxScrollTop <= 0) return top
  return Math.min(top, maxScrollTop)
}

/** The slider thumb / drag-preview position, in percent (ui-spec §6.7). */
export function sliderPercent(page: number, pageCount: number): number {
  if (pageCount <= 1) return 0
  const clamped = clampPage(page, pageCount)
  return ((clamped - 1) / (pageCount - 1)) * 100
}

/**
 * A dimension lookup that prefers a measured size over the API's.
 *
 * `PageInfo.w/h` are `null` until the server's dimension pass reaches them
 * (`dims_state`), so FR-VWR-004 would not fire on a freshly indexed book. Once
 * an `<img>` has loaded we know its natural size exactly, and that wins.
 */
export function makeDimsLookup(
  pages: readonly PageInfo[],
  measured: ReadonlyMap<number, PageDims>,
): DimsLookup {
  const byNumber = new Map<number, PageDims>()
  for (const info of pages) byNumber.set(info.n, { w: info.w, h: info.h })
  return (page) => {
    const known = measured.get(page)
    if (known?.w != null && known.h != null) return known
    return byNumber.get(page)
  }
}
