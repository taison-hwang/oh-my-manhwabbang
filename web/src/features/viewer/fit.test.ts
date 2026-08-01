import { fireEvent, render } from '@testing-library/react'
import { createElement } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import type { PageInfo } from '../../api/types'
import {
  FALLBACK_PAGE_RATIO,
  STAGE_PADDING_PX,
  clampPage,
  isLandscape,
  makeDimsLookup,
  nextPage,
  pageAtScroll,
  pageFitStyle,
  pageFrameStyle,
  prevPage,
  scrollTopForPage,
  sliderPercent,
  stageFlexDirection,
  stagePages,
  stageScrollsY,
  stageStyle,
  verticalPageHeight,
  verticalWindow,
  type PageDims,
} from './fit'
import { PageStage, type PageStageProps } from './PageStage'

/**
 * The viewer's layout maths (ui-spec §6.2, FR-VWR-003/-004/-005, impl-plan §6.1
 * rows 09–11).
 *
 * Every rule this package is judged on is one of these functions, which is the
 * point of them being pure. The two the screen lives or dies by:
 *
 *  1. **RTL spread.** Page *n* on the right comes from `row-reverse` alone; the
 *     page array stays ascending. Reversing both cancels out and silently
 *     produces LTR, so both halves are asserted separately.
 *  2. **Fit resolution.** `pageFitStyle`'s percentages are meaningless unless
 *     the frame they resolve against has a definite size, so `pageFrameStyle`
 *     is asserted to supply one for every fit that needs one.
 *
 * The last block is the exception to "everything here is pure", and it is here
 * because of what a pure test cannot see: `scrollTopForPage` was fully covered
 * while **nothing assigned its result**, so the 세로 blocker it exists for could
 * be reintroduced by deleting one line without a single test going red. Those
 * cases render the real `PageStage` over a modelled layout and assert the
 * scroller actually moved.
 */

/** `noUncheckedIndexedAccess` types an indexed read as `T | undefined`. */
function at<T>(items: readonly T[], index: number): T {
  const item = items[index]
  if (item === undefined) throw new Error(`expected an element at index ${String(index)}`)
  return item
}

const PORTRAIT: PageDims = { w: 800, h: 1_200 }
const LANDSCAPE: PageDims = { w: 1_600, h: 1_000 }

/** Page 7 is a double-page scan; everything else is portrait. */
const dims = (page: number): PageDims => (page === 7 ? LANDSCAPE : PORTRAIT)
/** Nothing measured yet — `dims_state: "none"`. */
const unknownDims = (): PageDims => ({ w: null, h: null })

describe('isLandscape', () => {
  it('is true only when the width exceeds the height', () => {
    expect(isLandscape(LANDSCAPE)).toBe(true)
    expect(isLandscape(PORTRAIT)).toBe(false)
    expect(isLandscape({ w: 1_000, h: 1_000 })).toBe(false)
  })

  it('treats unknown dimensions as portrait, never as a spread scan', () => {
    // `dims_state: "none"` must not break 양면 for a freshly indexed book.
    expect(isLandscape({ w: null, h: null })).toBe(false)
    expect(isLandscape({ w: 800, h: null })).toBe(false)
    expect(isLandscape(undefined)).toBe(false)
    expect(isLandscape({ w: 0, h: 0 })).toBe(false)
  })
})

describe('clampPage', () => {
  it('clamps into [1, pageCount]', () => {
    expect(clampPage(0, 10)).toBe(1)
    expect(clampPage(-4, 10)).toBe(1)
    expect(clampPage(11, 10)).toBe(10)
    expect(clampPage(4.7, 10)).toBe(4)
    expect(clampPage(3, 0)).toBe(1)
  })
})

describe('stagePages', () => {
  it('shows one page in 단면 and 세로', () => {
    expect(stagePages(12, 214, 'single', dims)).toEqual([12])
    expect(stagePages(12, 214, 'vertical', dims)).toEqual([12])
  })

  it('pairs n with n+1 in 양면, ascending', () => {
    expect(stagePages(12, 214, 'spread', dims)).toEqual([12, 13])
  })

  it('never pairs past the last page', () => {
    expect(stagePages(214, 214, 'spread', dims)).toEqual([214])
  })

  it('shows a landscape scan alone, and the page before it alone (FR-VWR-004)', () => {
    expect(stagePages(7, 214, 'spread', dims)).toEqual([7])
    // Pairing 6 with the spread scan at 7 is what puts the book out of phase.
    expect(stagePages(6, 214, 'spread', dims)).toEqual([6])
    expect(stagePages(8, 214, 'spread', dims)).toEqual([8, 9])
  })

  it('pairs when dimensions are still unknown', () => {
    expect(stagePages(12, 214, 'spread', unknownDims)).toEqual([12, 13])
  })

  it('is empty for a book with no pages', () => {
    expect(stagePages(1, 0, 'spread', dims)).toEqual([])
  })
})

describe('nextPage / prevPage', () => {
  it('steps +1 in 단면 and +2 in 양면 (ui-spec §8.2)', () => {
    expect(nextPage(12, 214, 'single', dims)).toBe(13)
    expect(nextPage(12, 214, 'spread', dims)).toBe(14)
    expect(prevPage(14, 214, 'spread', dims)).toBe(12)
    expect(prevPage(13, 214, 'single', dims)).toBe(12)
  })

  it('steps +1 in 세로 as well — only 양면 doubles', () => {
    // 세로 has a page turn even though the reader mostly scrolls; the store used
    // to own this case and no longer does, so it is asserted where the stride
    // now lives.
    expect(nextPage(12, 214, 'vertical', dims)).toBe(13)
    expect(prevPage(13, 214, 'vertical', dims)).toBe(12)
  })

  it('steps +1 across a landscape scan so the pairing stays in phase', () => {
    expect(nextPage(6, 214, 'spread', dims)).toBe(7)
    expect(nextPage(7, 214, 'spread', dims)).toBe(8)
  })

  it('clamps to [1, page_count]', () => {
    expect(nextPage(214, 214, 'spread', dims)).toBe(214)
    expect(nextPage(213, 214, 'spread', dims)).toBe(214)
    expect(prevPage(1, 214, 'spread', dims)).toBe(1)
    expect(prevPage(2, 214, 'spread', dims)).toBe(1)
  })
})

describe('verticalWindow', () => {
  it('keeps a small window of live pages around the current one', () => {
    expect(verticalWindow(12, 214)).toEqual([11, 16])
    expect(verticalWindow(1, 214)).toEqual([1, 5])
    expect(verticalWindow(214, 214)).toEqual([213, 214])
  })
})

describe('stageFlexDirection', () => {
  it('is row-reverse under R→L — the rule this screen is judged on', () => {
    expect(stageFlexDirection('spread', 'rtl')).toBe('row-reverse')
    expect(stageFlexDirection('single', 'rtl')).toBe('row-reverse')
    expect(stageFlexDirection('spread', 'ltr')).toBe('row')
    expect(stageFlexDirection('vertical', 'rtl')).toBe('column')
  })
})

describe('stageStyle', () => {
  it('lays 세로 out as a scrolling column', () => {
    const style = stageStyle('vertical', 'width', 'rtl')
    expect(style.flexDirection).toBe('column')
    expect(style.overflowY).toBe('auto')
    expect(style.padding).toBe(0)
  })

  it('pads the horizontal stage by 20px and drops the padding for 원본', () => {
    expect(stageStyle('single', 'height', 'ltr').padding).toBe(`${String(STAGE_PADDING_PX)}px`)
    expect(stageStyle('single', 'original', 'ltr').padding).toBe(0)
  })

  it('scrolls vertically exactly for the fits that overflow the stage', () => {
    expect(stageScrollsY('width')).toBe(true)
    expect(stageScrollsY('original')).toBe(true)
    expect(stageScrollsY('height')).toBe(false)
    expect(stageScrollsY('contain')).toBe(false)
    expect(stageStyle('single', 'height', 'ltr').overflowY).toBe('hidden')
    expect(stageStyle('single', 'width', 'ltr').overflowY).toBe('auto')
  })

  it('carries the reading direction into the flow', () => {
    expect(stageStyle('spread', 'height', 'rtl').flexDirection).toBe('row-reverse')
    expect(stageStyle('spread', 'height', 'ltr').flexDirection).toBe('row')
  })
})

describe('pageFitStyle', () => {
  it('implements the four sizings of ui-spec §6.2', () => {
    expect(pageFitStyle('width')).toMatchObject({ width: '100%', height: 'auto' })
    expect(pageFitStyle('height')).toMatchObject({ height: '100%', width: 'auto' })
    expect(pageFitStyle('original')).toMatchObject({ width: 'auto', maxWidth: 'none' })
    expect(pageFitStyle('contain')).toMatchObject({ maxWidth: '100%', maxHeight: '100%' })
  })
})

describe('pageFrameStyle', () => {
  it('gives the fit percentages a definite containing block', () => {
    // Without these the `height:100%` / `max-height:100%` above resolve to
    // `auto` / `none` and every page renders at its intrinsic size.
    expect(pageFrameStyle('height', 'single').height).toBe('100%')
    expect(pageFrameStyle('contain', 'single').height).toBe('100%')
    expect(pageFrameStyle('contain', 'single').flex).toBe('1 1 0')
    expect(pageFrameStyle('width', 'single').flex).toBe('1 1 0')
  })

  it('leaves 원본 at its intrinsic size', () => {
    const style = pageFrameStyle('original', 'single')
    expect(style.height).toBeUndefined()
    expect(style.flex).toBe('0 0 auto')
  })

  it('spans the stage width in 세로 and adds a height only where one is needed', () => {
    expect(pageFrameStyle('width', 'vertical')).toMatchObject({ width: '100%' })
    expect(pageFrameStyle('width', 'vertical').height).toBeUndefined()
    expect(pageFrameStyle('height', 'vertical')).toMatchObject({ width: '100%', height: '100%' })
    expect(pageFrameStyle('contain', 'vertical')).toMatchObject({ width: '100%', height: '100%' })
  })
})

describe('sliderPercent', () => {
  it('maps [1, pageCount] onto [0, 100]', () => {
    expect(sliderPercent(1, 214)).toBe(0)
    expect(sliderPercent(214, 214)).toBe(100)
    expect(sliderPercent(108, 215)).toBeCloseTo(50, 5)
  })

  it('is 0 for a book that cannot be scrubbed', () => {
    expect(sliderPercent(1, 1)).toBe(0)
    expect(sliderPercent(1, 0)).toBe(0)
  })
})

describe('verticalPageHeight', () => {
  it('matches what the live frame will actually be, per fit', () => {
    // 1440 wide, 900 tall stage; an 800×1200 page.
    expect(verticalPageHeight(PORTRAIT, 'width', 1_440, 900)).toBe(2_160)
    expect(verticalPageHeight(PORTRAIT, 'height', 1_440, 900)).toBe(900)
    expect(verticalPageHeight(PORTRAIT, 'contain', 1_440, 900)).toBe(900)
    expect(verticalPageHeight(PORTRAIT, 'original', 1_440, 900)).toBe(1_200)
  })

  it('falls back to a 2:3 page while the dimensions are unknown', () => {
    expect(verticalPageHeight(undefined, 'width', 900, 900)).toBe(
      Math.round(900 * FALLBACK_PAGE_RATIO),
    )
    expect(verticalPageHeight({ w: null, h: null }, 'width', 900, 900)).toBe(
      Math.round(900 * FALLBACK_PAGE_RATIO),
    )
  })

  it('uses the page aspect, so a landscape scan is not estimated as a portrait one', () => {
    expect(verticalPageHeight(LANDSCAPE, 'width', 1_600, 900)).toBe(1_000)
  })
})

describe('pageAtScroll', () => {
  it('is the last page whose top edge is at or above the scroll position', () => {
    const tops = [0, 1_212, 2_424, 3_636]
    expect(pageAtScroll(tops, 0)).toBe(1)
    expect(pageAtScroll(tops, 1_210)).toBe(1)
    // A 1px tolerance: sub-pixel layout must not leave the reader one page behind.
    expect(pageAtScroll(tops, 1_211)).toBe(2)
    expect(pageAtScroll(tops, 1_212)).toBe(2)
    expect(pageAtScroll(tops, 3_700)).toBe(4)
    expect(pageAtScroll([], 0)).toBe(1)
  })
})

describe('scrollTopForPage (세로 blank-stage blocker, FR-VWR-003)', () => {
  /**
   * Measured in Chrome before the fix: switching to 세로 at page 100 of a
   * 214-page volume left `scrollTop` at 0 while the virtualiser placed page 100
   * at y ≈ 90 288 of a 187 860 px document. The stage read as blank because the
   * reader was ~90 000 px above their page.
   */
  const tops = Array.from({ length: 214 }, (_, i) => i * 912)
  /** `scrollHeight - clientHeight` for a 1 200px-tall stage of 912px pages. */
  const maxScrollTop = 214 * 912 - 1_200

  it('lands on the top edge of the page the reader is on', () => {
    expect(scrollTopForPage(tops, 100, maxScrollTop)).toBe(99 * 912)
    expect(scrollTopForPage(tops, 1, maxScrollTop)).toBe(0)
  })

  it('round-trips with pageAtScroll — the two must agree or the stage bounces', () => {
    for (const page of [1, 2, 42, 100, 213]) {
      expect(pageAtScroll(tops, scrollTopForPage(tops, page, maxScrollTop))).toBe(page)
    }
  })

  it('clamps to the scrollable range rather than letting the DOM bounce it', () => {
    // The last page cannot be brought to the top of a taller-than-one-page
    // document; the browser would clamp and fire a scroll event reporting a
    // different page, which the stage would then adopt.
    expect(scrollTopForPage(tops, 214, maxScrollTop)).toBe(maxScrollTop)
  })

  it('treats an unmeasured scroller as unclamped, not as "no scrolling"', () => {
    // jsdom reports every box as 0. Clamping to 0 there would make the helper
    // silently return 0 for every page and hide the very bug it exists for.
    expect(scrollTopForPage(tops, 100, 0)).toBe(99 * 912)
    expect(scrollTopForPage(tops, 100, Number.NaN)).toBe(99 * 912)
  })

  it('survives a page outside the mounted range and an empty stage', () => {
    expect(scrollTopForPage(tops, 0, maxScrollTop)).toBe(0)
    expect(scrollTopForPage(tops, 9_999, maxScrollTop)).toBe(maxScrollTop)
    expect(scrollTopForPage([], 3, maxScrollTop)).toBe(0)
  })
})

describe('makeDimsLookup', () => {
  const pages: PageInfo[] = [
    { n: 1, name: '001.jpg', ext: '.jpg', size: 1, w: 800, h: 1_200 },
    { n: 2, name: '002.jpg', ext: '.jpg', size: 1, w: null, h: null },
  ]

  it('prefers a measured natural size over the index, and falls back to it', () => {
    const measured = new Map<number, PageDims>([[2, { w: 1_600, h: 1_000 }]])
    const lookup = makeDimsLookup(pages, measured)
    expect(lookup(1)).toEqual({ w: 800, h: 1_200 })
    expect(lookup(2)).toEqual({ w: 1_600, h: 1_000 })
    expect(lookup(3)).toBeUndefined()
  })

  it('ignores a half-measured entry rather than reporting a null dimension', () => {
    const measured = new Map<number, PageDims>([[1, { w: 800, h: null }]])
    const lookup = makeDimsLookup(pages, measured)
    expect(at([lookup(1)], 0)).toEqual({ w: 800, h: 1_200 })
  })
})

// ---------------------------------------------------------------------------
// The 세로 scroller, wired (FR-VWR-003 / FR-VWR-009)
// ---------------------------------------------------------------------------

/**
 * A layout, modelled.
 *
 * jsdom has none: every `offsetTop`, `scrollHeight` and `clientHeight` is 0 and
 * `scrollTop` is a no-op setter, so a component that positions a scroller is
 * invisible to it — which is precisely how a fully-covered `scrollTopForPage`
 * ended up with nothing calling it. Modelling a uniform column of `PAGE_H`-tall
 * pages is enough to observe the two things that matter: that the aligner
 * assigns, and that it re-assigns when the page heights change underneath it.
 */
const STAGE_W = 1_440
const STAGE_H = 900
const VERTICAL_PAGE_COUNT = 187

/** 높이 fits a page to the stage height; 너비 gives 1 440 × (1 200/800). */
const PAGE_H_HEIGHT_FIT = verticalPageHeight(PORTRAIT, 'height', STAGE_W, STAGE_H)
const PAGE_H_WIDTH_FIT = verticalPageHeight(PORTRAIT, 'width', STAGE_W, STAGE_H)

const VERTICAL_PAGES: PageInfo[] = Array.from({ length: VERTICAL_PAGE_COUNT }, (_, i) => ({
  n: i + 1,
  name: `page_${String(i + 1).padStart(3, '0')}.jpg`,
  ext: '.jpg',
  size: 180_000,
  w: PORTRAIT.w,
  h: PORTRAIT.h,
}))

/** The height every modelled page currently occupies; the tests move it. */
let pageHeight = PAGE_H_HEIGHT_FIT
/** The stage's own measured height — a resize is a change to this. */
let stageHeight = STAGE_H

function isStage(el: Element): boolean {
  return el instanceof HTMLElement && el.dataset.role === 'stage'
}

/**
 * Counted through `previousElementSibling`, not through `parent.children[i]`:
 * jsdom rebuilds the live `HTMLCollection` on every access, which turns one
 * `childTops()` over a 187-page stage into ~35 000 tree walks.
 */
function indexInParent(el: HTMLElement): number {
  let index = 0
  let sibling = el.previousElementSibling
  while (sibling !== null) {
    index++
    sibling = sibling.previousElementSibling
  }
  return index
}

const scrollTops = new WeakMap<HTMLElement, number>()

function stubLayout(name: string, descriptor: PropertyDescriptor): () => void {
  const original = Object.getOwnPropertyDescriptor(HTMLElement.prototype, name)
  Object.defineProperty(HTMLElement.prototype, name, { configurable: true, ...descriptor })
  return () => {
    if (original === undefined) Reflect.deleteProperty(HTMLElement.prototype, name)
    else Object.defineProperty(HTMLElement.prototype, name, original)
  }
}

function installLayout(): () => void {
  const restores = [
    stubLayout('scrollTop', {
      get(this: HTMLElement): number {
        return scrollTops.get(this) ?? 0
      },
      set(this: HTMLElement, value: number) {
        scrollTops.set(this, value)
      },
    }),
    stubLayout('offsetTop', {
      get(this: HTMLElement): number {
        const parent = this.parentElement
        if (parent === null || !isStage(parent)) return 0
        return Math.max(0, indexInParent(this)) * pageHeight
      },
    }),
    stubLayout('scrollHeight', {
      get(this: HTMLElement): number {
        return isStage(this) ? this.children.length * pageHeight : 0
      },
    }),
    stubLayout('clientHeight', {
      get(this: HTMLElement): number {
        return isStage(this) ? stageHeight : 0
      },
    }),
    stubLayout('clientWidth', {
      get(this: HTMLElement): number {
        return isStage(this) ? STAGE_W : 0
      },
    }),
  ]
  return () => {
    for (const restore of restores.reverse()) restore()
  }
}

/** The child top edges the modelled layout produces, for `pageAtScroll`. */
function modelledTops(): number[] {
  return Array.from({ length: VERTICAL_PAGE_COUNT }, (_, i) => i * pageHeight)
}

function stageElement(): HTMLElement {
  const el = document.querySelector('[data-role="stage"]')
  if (!(el instanceof HTMLElement)) throw new Error('the stage is not mounted')
  return el
}

function stageProps(overrides: Partial<PageStageProps> = {}): PageStageProps {
  return {
    bookId: 'book000000000001',
    cv: 'cv0',
    pages: VERTICAL_PAGES,
    page: 101,
    pageCount: VERTICAL_PAGE_COUNT,
    mode: 'vertical',
    fit: 'height',
    dir: 'ltr',
    measured: new Map<number, PageDims>(),
    cause: null,
    onPageLoaded: vi.fn(),
    onPageFailed: vi.fn(),
    onScrollToPage: vi.fn(),
    ...overrides,
  }
}

describe('PageStage 세로 alignment (the blocker, wired to the DOM)', () => {
  let uninstall: (() => void) | null = null

  afterEach(() => {
    uninstall?.()
    uninstall = null
    pageHeight = PAGE_H_HEIGHT_FIT
    stageHeight = STAGE_H
  })

  function mount(props: Partial<PageStageProps> = {}) {
    pageHeight = PAGE_H_HEIGHT_FIT
    stageHeight = STAGE_H
    uninstall = installLayout()
    return render(createElement(PageStage, stageProps(props)))
  }

  it('resumes into 세로 with the scroller on the reader’s page, not at 0', () => {
    // FR-VWR-009. Before the fix `scrollTop` stayed 0 while page 101 sat ~90 000 px
    // down the document, so the reader was shown page 1 and told they were on 101.
    mount()
    const stage = stageElement()
    expect(stage.scrollTop).toBe(100 * PAGE_H_HEIGHT_FIT)
    expect(pageAtScroll(modelledTops(), stage.scrollTop)).toBe(101)
  })

  it('moves the scroller when 단면 switches to 세로 mid-book', () => {
    const { rerender } = mount({ mode: 'single' })
    rerender(createElement(PageStage, stageProps({ mode: 'vertical' })))
    const stage = stageElement()
    expect(stage.scrollTop).toBe(100 * PAGE_H_HEIGHT_FIT)
  })

  it('re-aligns when the fit changes, because the fit is what the heights are', () => {
    // WP15-01. `verticalPageHeight` takes the fit, so 높이 → 너비 re-lays out
    // every page: measured in Chrome the document grew from 170 532 px to
    // 402 786 px while `scrollTop` stayed at 91 688 and the reader — still told
    // they were on 101 — was looking at page 43.
    const { rerender } = mount({ fit: 'height' })
    expect(stageElement().scrollTop).toBe(100 * PAGE_H_HEIGHT_FIT)

    pageHeight = PAGE_H_WIDTH_FIT
    rerender(createElement(PageStage, stageProps({ fit: 'width' })))

    const stage = stageElement()
    expect(PAGE_H_WIDTH_FIT).not.toBe(PAGE_H_HEIGHT_FIT)
    expect(stage.scrollTop).toBe(100 * PAGE_H_WIDTH_FIT)
    expect(pageAtScroll(modelledTops(), stage.scrollTop)).toBe(101)
  })

  it('re-aligns on a resize rather than leaving the reader where the old height put them', () => {
    mount()
    // 높이 ties the page height to the stage height, so shrinking the window is
    // the same class of re-layout a fit change is.
    stageHeight = 700
    pageHeight = 700
    fireEvent.resize(window)
    const stage = stageElement()
    expect(stage.scrollTop).toBe(100 * 700)
    expect(pageAtScroll(modelledTops(), stage.scrollTop)).toBe(101)
  })

  it('reports the reader’s own scroll and does not snap them back to it', () => {
    const onScrollToPage = vi.fn()
    const { rerender } = mount({ onScrollToPage })
    const stage = stageElement()

    stage.scrollTop = 100 * PAGE_H_HEIGHT_FIT + 1_400
    fireEvent.scroll(stage)
    expect(onScrollToPage).toHaveBeenCalledWith(102)

    // The counter follows the scroll; the aligner must not answer by pulling the
    // scroller back to page 102's top edge.
    rerender(createElement(PageStage, stageProps({ page: 102, onScrollToPage })))
    expect(stage.scrollTop).toBe(100 * PAGE_H_HEIGHT_FIT + 1_400)
  })
})
