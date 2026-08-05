import '@testing-library/jest-dom/vitest'

import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render } from '@testing-library/react'
import { StrictMode, type ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { THUMB_SLOT_TOUCH_PX, ThumbnailStrip } from './ThumbnailStrip'

/**
 * When the thumbnail strip's recentre may animate.
 *
 * The rule under test is one line: **smooth iff the target is within one
 * strip-width of where the strip already is.** Everything else — 다음 권 읽기, a
 * slider commit across a 1 540-page volume, a resize — is instant, not by a
 * clause naming it but because it moves further than that.
 *
 * The stake is AC-008, not polish. A smooth scroll is a scroll event per frame;
 * each frame moves the virtualiser's window and mounts cells that each fire a
 * lazily generated server-side thumbnail. Measured in Chrome on this strip's
 * configuration, the 12 → 1 200 slider commit mounts **29** distinct pages with
 * `behavior:'auto'` and **998** with `behavior:'smooth'` — same destination,
 * 34× the requests.
 *
 * ## What the harness stubs, and why each stub is load-bearing
 *
 * jsdom has no layout, so left alone every number this rule reads is 0 and the
 * rule cannot be exercised at all. Five properties are stubbed so the arithmetic
 * is the browser's:
 *
 *  - `getBoundingClientRect` — `virtual-core`'s `observeElementRect` calls it
 *    once, synchronously, on attach; it is the only route to `getSize()` here
 *    because jsdom has no `ResizeObserver`. Without it `getOffsetForIndex`
 *    centres against a zero-width viewport.
 *  - `clientWidth` — the rule's own budget.
 *  - `scrollWidth` — the clamp in `getOffsetForAlignment`.
 *  - `scrollLeft` + `scrollTo` — jsdom implements neither, so the stub records
 *    the arguments **and applies them**, which is what makes the *second* of two
 *    recentres measure a real distance from the first.
 *
 * Assertions are on the recorded arguments, never on a scroll position: the
 * behaviour is the delta, and a position assertion would be an assertion about
 * the stub.
 *
 * **Every case renders inside `<StrictMode>`, because `main.tsx:48` does.** The
 * first cut of this suite did not, and it stayed green over a defect that only
 * exists there: StrictMode's simulated unmount/remount left the one-shot "just
 * opened" ref spent, so the opening animated in the development build.
 */

const BOOK = 'b1'
const PAGES = 214
/** A 1 540-page volume — the real collection's largest, and AC-008's case. */
const BIG = 1_540
/** The strip's visible width in these cases; also the smooth/instant budget. */
const VIEWPORT = 600

interface Recorded {
  left: number
  behavior: string | undefined
}

let scrolls: Recorded[]
let scrollLeft: number
let viewportWidth = 1_440
let reduceMotion = false
let mediaListeners: (() => void)[] = []
let restore: (() => void)[] = []

/** Replace a property and register the exact undo. */
function patch(target: object, key: string, descriptor: PropertyDescriptor): void {
  const original = Object.getOwnPropertyDescriptor(target, key)
  Object.defineProperty(target, key, { configurable: true, ...descriptor })
  restore.push(() => {
    if (original === undefined) {
      Reflect.deleteProperty(target, key)
    } else {
      Object.defineProperty(target, key, original)
    }
  })
}

/**
 * `matchMedia` for the two queries this component subscribes to.
 *
 * The reduced-motion query is matched by **exact string**, and the string is
 * written out here rather than imported from the component: sharing a constant
 * would let a typo in the component change both sides at once and keep the test
 * green. A wrong query in the component makes this stub answer `false`, i.e. no
 * reduced motion, i.e. a smooth scroll where the case demands an instant one.
 */
function stubMedia(): void {
  const impl = (query: string) => {
    const min = /min-width:\s*(\d+)px/.exec(query)?.[1]
    const matches =
      query === '(prefers-reduced-motion: reduce)'
        ? reduceMotion
        : min !== undefined && viewportWidth >= Number(min)
    return {
      matches,
      media: query,
      addEventListener: (_: string, cb: () => void) => mediaListeners.push(cb),
      removeEventListener: (_: string, cb: () => void) => {
        mediaListeners = mediaListeners.filter((x) => x !== cb)
      },
    }
  }
  patch(window, 'matchMedia', { writable: true, value: impl })
}

/** Give the strip element a real geometry, and a scroll that actually sticks. */
function stubLayout({ clientWidth = VIEWPORT } = {}): void {
  patch(Element.prototype, 'clientWidth', { get: () => clientWidth })
  // **Read off the rendered track, not computed from the pitch.** A scroller's
  // `scrollWidth` is its content's width, and `getOffsetForAlignment` clamps
  // every target against it — so it has to lag exactly as the DOM lags. A stub
  // that recomputed it from the current slot would hand the clamp a length the
  // browser does not have yet, which is precisely the mistake the resize case
  // below exists to catch.
  patch(Element.prototype, 'scrollWidth', {
    get(this: Element): number {
      const rendered = this.querySelector<HTMLElement>(':scope > div')?.style.width
      const width = rendered === undefined ? Number.NaN : Number.parseFloat(rendered)
      return Number.isFinite(width) ? width : 0
    },
  })
  patch(Element.prototype, 'getBoundingClientRect', {
    writable: true,
    value: () => ({ width: clientWidth, height: 72, top: 0, left: 0, right: 0, bottom: 0 }),
  })
  patch(Element.prototype, 'scrollLeft', {
    get: () => scrollLeft,
    set: (v: number) => {
      scrollLeft = v
    },
  })
  patch(Element.prototype, 'scrollTo', {
    writable: true,
    value: (options: ScrollToOptions) => {
      const left = options.left ?? 0
      scrolls.push({ left, behavior: options.behavior })
      scrollLeft = left
    },
  })
}

/** Move the stubbed viewport and tell every `useSyncExternalStore` about it. */
function resizeViewport(width: number): void {
  viewportWidth = width
  act(() => {
    for (const cb of [...mediaListeners]) cb()
  })
}

let client: QueryClient

function wrap(node: ReactNode) {
  // `<StrictMode>` mirrors `main.tsx:48`.
  //
  // The query client is **not** ceremony: because `stubLayout` gives the strip a
  // real 600px rect, the virtualiser has a real window and this harness mounts
  // **20 cells (pages 1–20)**, each running `usePageThumbImage`. One client per
  // test rather than one per render keeps `rerender` from refetching all twenty.
  // (An earlier revision of this comment claimed no cells mount at all. That was
  // true of the first harness, which stubbed no geometry; it stopped being true
  // the moment the rect was stubbed, and it was still being asserted after.)
  return (
    <StrictMode>
      <QueryClientProvider client={client}>{node}</QueryClientProvider>
    </StrictMode>
  )
}

interface StripProps {
  bookId?: string
  pageCount?: number
  current?: number
}

function strip({ bookId = BOOK, pageCount = PAGES, current = 12 }: StripProps = {}) {
  return wrap(
    <ThumbnailStrip
      bookId={bookId}
      cv={null}
      pageCount={pageCount}
      current={current}
      onJump={vi.fn()}
    />,
  )
}

/** Mount the strip and forget the opening recentre, which has its own cases. */
function open(props: StripProps = {}) {
  const view = render(strip(props))
  scrolls = []
  return view
}

/** Every `behavior` the strip has asked the DOM for, oldest first. */
function behaviours(): (string | undefined)[] {
  return scrolls.map((s) => s.behavior)
}

/** The behaviour of the most recent scroll, and a readable failure if none. */
function lastBehaviour(): string | undefined {
  if (scrolls.length === 0) throw new Error('the strip did not scroll at all')
  return scrolls[scrolls.length - 1]?.behavior
}

beforeEach(() => {
  client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  scrolls = []
  scrollLeft = 0
  viewportWidth = 1_440
  reduceMotion = false
  mediaListeners = []
  restore = []
  stubMedia()
})

afterEach(() => {
  // `vi.restoreAllMocks()` does not undo `Object.defineProperty`, so the undos
  // are collected explicitly; a leaked `Element.prototype.scrollLeft` would
  // follow this file into every other suite the moment isolation is turned off.
  for (const undo of [...restore].reverse()) undo()
  restore = []
  vi.restoreAllMocks()
})

describe('the strip glides a short recentre and jumps a long one (AC-008)', () => {
  it('never animates a slider commit across a 1 540-page volume', () => {
    // The AC-008 line. 12 → 1 200 is 61 776px, ~103 strip-widths; animating it
    // walks the virtualiser's window across 998 distinct pages, each one a
    // lazily generated server-side thumbnail request. The slider is the path
    // that actually produces this: a drag only moves `dragPage`, and the jump
    // lands in one step on release.
    stubLayout()
    const view = open({ pageCount: BIG, current: 12 })

    view.rerender(strip({ pageCount: BIG, current: 1_200 }))
    expect(behaviours()).not.toContain('smooth')
    expect(lastBehaviour()).toBe('auto')
  })

  it('animates an arrow key, which moves one thumb', () => {
    stubLayout()
    const view = open({ current: 12 })

    view.rerender(strip({ current: 13 }))
    expect(lastBehaviour()).toBe('smooth')
  })

  it('draws the line at exactly one strip-width', () => {
    // The budget is `clientWidth` and nothing else. From page 12 (centred at
    // 298px) page 23 sits 572px away and page 24 sits 624px away, either side
    // of the 600px strip. A budget of 2×`clientWidth`, or a hard-coded pixel
    // count, moves page 24 across the line.
    stubLayout()
    const near = open({ current: 12 })
    near.rerender(strip({ current: 23 }))
    expect(lastBehaviour()).toBe('smooth')
    near.unmount()

    scrollLeft = 0
    const far = open({ current: 12 })
    far.rerender(strip({ current: 24 }))
    expect(lastBehaviour()).toBe('auto')
  })

  it('is instant while the strip has no width yet (and in jsdom, which never has)', () => {
    // The rule's safe failure direction, stated as a case rather than left to
    // be discovered: with no geometry the budget is 0, so nothing but a
    // zero-length move can qualify as short. What this case really guards is
    // the tempting repair — substituting a plausible default width when
    // `clientWidth` reads 0 — which would animate a recentre over a distance
    // nobody has measured.
    stubLayout({ clientWidth: 0 })
    const view = open({ current: 12 })

    view.rerender(strip({ current: 13 }))
    expect(behaviours()).not.toContain('smooth')
    expect(lastBehaviour()).toBe('auto')
  })

  it('keeps every glide it ever asks for inside one strip-width', () => {
    // The rule stated as an invariant over a whole session rather than case by
    // case: whatever route the page took, a `smooth` scroll never covers more
    // than the strip's own width, so no single recentre can walk the
    // virtualiser's window across more than a screenful of cells.
    stubLayout()
    const view = open({ pageCount: BIG, current: 12 })

    let glides = 0
    for (const current of [13, 14, 20, 900, 901, 1, 2, 1_540, 1_539, 1_538]) {
      const before = scrollLeft
      scrolls = []
      view.rerender(strip({ pageCount: BIG, current }))
      for (const s of scrolls) {
        if (s.behavior !== 'smooth') continue
        expect(Math.abs(s.left - before)).toBeLessThanOrEqual(VIEWPORT)
        glides += 1
      }
    }
    // …and the walk really did contain glides, or the loop proved nothing.
    expect(glides).toBeGreaterThan(0)
  })
})

describe('the cases that must not animate even though they are short', () => {
  it('opens instantly, on both of StrictMode’s two mounts', () => {
    // The element is new, so its `scrollLeft` of 0 is not a place the reader
    // was; `virtual-core`'s `_willUpdate` puts it there on attach. Page 12 of
    // 214 centres at 298px — inside the 600px budget — so the distance rule
    // alone would animate the opening. Both mounts are asserted because the
    // one-shot guard is only correct if its cleanup restores it.
    stubLayout()
    render(strip({ current: 12 }))

    expect(behaviours()).not.toContain('smooth')
    // Two `auto` recentres, one per StrictMode mount. If this drops to one, the
    // suite has stopped exercising the double mount and the case is vacuous.
    expect(behaviours().filter((b) => b === 'auto')).toHaveLength(2)
  })

  it('never animates under prefers-reduced-motion', () => {
    // `base.css`'s reduce block zeroes transitions and animations; a scroll
    // asked for in JS is neither, so the preference has to be honoured here.
    reduceMotion = true
    stubLayout()
    const view = open({ current: 12 })

    view.rerender(strip({ current: 13 }))
    expect(behaviours()).not.toContain('smooth')
    expect(lastBehaviour()).toBe('auto')
  })
})

describe('다음 권 읽기 — a volume change is two renders, not one', () => {
  it('does not scroll on the render where only bookId has changed', () => {
    // `ViewerPage` takes `bookId` straight from the route while `pageCount` and
    // `current` come from `useBook`, a plain `useQuery` with no
    // `placeholderData`. Render A therefore carries the new book's id and the
    // old book's page state; recentring there would scroll the old page against
    // the old measurements — and when this component keyed its instant case on
    // `bookId`, it also spent that case on this empty render and left the real
    // recentre, on render B, animated.
    stubLayout()
    const view = open({ pageCount: BIG, current: 1_200 })

    view.rerender(strip({ bookId: 'b2', pageCount: BIG, current: 1_200 }))
    expect(scrolls).toHaveLength(0)
  })

  it('jumps instantly on the render where the new volume’s pages arrive', () => {
    stubLayout()
    const view = open({ pageCount: BIG, current: 1_200 })
    view.rerender(strip({ bookId: 'b2', pageCount: BIG, current: 1_200 }))
    scrolls = []

    // The query lands: 97 pages, page 1. This is the recentre that matters, and
    // it is a 62 074px trip, so the one rule makes it instant.
    view.rerender(strip({ bookId: 'b2', pageCount: 97, current: 1 }))
    expect(behaviours()).not.toContain('smooth')
    expect(lastBehaviour()).toBe('auto')
  })
})

describe('the strip’s scrolling contract with the virtualiser', () => {
  it('lands on the new pitch’s offset after a resize, not the old one', () => {
    /*
     * The re-measure moves every offset, and the point of recentring is that
     * the reader's page ends up back on screen against the *new* ones.
     *
     * Keyed on `slot`, it did not. This effect then ran in the same commit as
     * `measure()`, which only empties the size cache and schedules a render;
     * `getOffsetForIndex` reads `measurementsCache` directly rather than
     * through the memo, so it still answered with the old pitch and named the
     * offset the strip was already sitting on. Measured at 1 440 → 700 on page
     * 1 200 of 1 540: track 80 080 → 92 400px, recentre `left: 62074`, correct
     * answer 71 670 — 9 596px, i.e. **160 thumbnails**, off screen. An earlier
     * revision of this file asserted that 0-pixel move as if it were the
     * intended behaviour, which fixed the defect in place.
     *
     * Keyed on `totalSize` it runs one commit later, after `getTotalSize()`
     * has missed its memo and rewritten every measurement, so the exact offset
     * is assertable — and the distance rule then classifies a 9 596px trip on
     * its own, with nothing naming the resize.
     */
    stubLayout()
    open({ pageCount: BIG, current: 1_200 })
    // 1 199 × 52 − (600 − 52) / 2: where page 1 200 sits at the desktop pitch.
    expect(scrollLeft).toBe(62_074)

    resizeViewport(700)

    // 1 199 × 60 − (600 − 60) / 2, at the touch pitch the resize selects.
    const expected = 1_199 * THUMB_SLOT_TOUCH_PX - (VIEWPORT - THUMB_SLOT_TOUCH_PX) / 2
    expect(expected).toBe(71_670)
    expect(scrolls).not.toHaveLength(0)
    expect(lastBehaviour()).toBe('auto')
    expect(scrollLeft).toBe(expected)
  })

  it('clamps that resize against the new track, not the one still on screen', () => {
    /*
     * The case that decides *how* the fix is written, measured both ways.
     *
     * At the last page the target is past the end and `getOffsetForAlignment`
     * clamps it to `scrollWidth - clientWidth`, read live off the DOM. Forcing
     * the measurements to recompute *inside* the old commit — a one-line
     * `virtualizer.getTotalSize()` at the top of the effect — makes the offsets
     * fresh but leaves the track element at its old 80 080px, so the clamp is
     * the old `79 480`:
     *
     *     one-line variant, page 1 540, 1 440 → 700:
     *       left=79480 smooth   track=80080px   (12 320px = 205 thumbs short,
     *                                            and a zero-distance `smooth`
     *                                            to the wrong place)
     *     keyed on totalSize:
     *       left=91800 auto     track=92400px   (correct)
     *
     * Keying the effect on `totalSize` runs it in the commit where the track
     * has already been re-rendered, so the clamp sees the length the browser
     * sees. This is the same conclusion the library grid reached by splitting
     * into two layout effects; here the dependency does it.
     */
    stubLayout()
    open({ pageCount: BIG, current: BIG })
    // clamp(1539×52 − 274, 0, 1540×52 − 600) = clamp(79 754, 0, 79 480).
    expect(scrollLeft).toBe(79_480)

    resizeViewport(700)
    // clamp(1539×60 − 270, 0, 1540×60 − 600) = clamp(92 070, 0, 91 800).
    expect(scrollLeft).toBe(91_800)
    expect(lastBehaviour()).toBe('auto')
  })

  it('stays in the virtualiser’s static mode, the only mode smooth is supported in', () => {
    /*
     * `virtual-core@3.13.0` refuses to promise smooth scrolling in *dynamic*
     * mode: `if (behavior === 'smooth' && this.isDynamicMode()) console.warn(…)`.
     * `isDynamicMode` is `elementsCache.size > 0`, and that cache is written
     * only by `measureElement`. This strip lays every cell out from a constant
     * `estimateSize` and never measures one, so the mode is static. Wiring
     * `measureElement` into the cells later — the obvious thing to reach for if
     * thumbs ever get variable widths — would take the smoothness with it.
     *
     * Checked two ways, because neither is sufficient alone:
     *
     *  - **behaviour** — a real page change is driven and `console.warn` must
     *    stay silent. This harness mounts 20 cells, so a `measureElement` ref
     *    on them really does reach the library. An earlier revision of this
     *    file dropped the spy on the grounds that jsdom mounts no cells; that
     *    was true of the geometry-less first harness and false of this one, and
     *    the wrong reason was being used to exclude the stronger check.
     *  - **source** — the spy only fires for a form the library notices, and it
     *    cannot speak for a config where the warning is compiled out. The scan
     *    is the belt; `tokens.test.ts` reads component sources the same way,
     *    including the `process.cwd()` root, which is there because
     *    `import.meta.url` is an http URL under jsdom.
     *
     * The scan's pattern is deliberately narrow (it would miss
     * `const m = virtualizer.measureElement; … ref={m}`), and the behavioural
     * half is what covers that hole.
     */
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => undefined)
    stubLayout()
    const view = open({ current: 12 })
    view.rerender(strip({ current: 13 }))

    expect(lastBehaviour()).toBe('smooth')
    expect(warn).not.toHaveBeenCalled()

    const source = readFileSync(
      resolve(process.cwd(), 'src/features/viewer/ThumbnailStrip.tsx'),
      'utf8',
    )
    expect(source).not.toMatch(/\bref=\{[^}]*measureElement/)
    expect(source).not.toMatch(/\bmeasureElement\(/)
  })

  it('pins scroll-behavior so a stylesheet cannot animate the instant case', () => {
    stubLayout()
    const { container } = render(strip())
    const el = container.querySelector<HTMLElement>('[data-role="thumbnail-strip"]')
    // CSSOM-View resolves `behavior:'auto'` against the element's computed
    // `scroll-behavior`. A `scroll-behavior:smooth` reaching this element would
    // animate every instant recentre above — including the 62 074px one — while
    // this suite stayed green, because no argument would have changed. Nothing
    // in the repo sets that property today; the pin is defence.
    expect(el?.style.scrollBehavior).toBe('auto')
  })
})
