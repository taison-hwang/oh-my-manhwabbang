import '@testing-library/jest-dom/vitest'

import { act, cleanup, render, screen } from '@testing-library/react'
import { Profiler } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { SeriesList } from './SeriesList'
import { listHeaderPadRight, LIST_TEMPLATE } from './useLibrary'

/**
 * Item bk — the list's column layout (`matchMedia`) and the header's gutter
 * padding (measured off the scroller) must never be committed from two
 * different viewports.
 *
 * The header grid is `LIST_TEMPLATE[layout]` padded by the scroller's gutter;
 * the rows are the same template inside the scroller. If one commit carries the
 * new layout beside the old gutter, the header's columns sit a scrollbar-width
 * off the rows' for as long as that commit is on screen. The gutter is constant
 * on a plain window drag, so this only shows when one resize moves both — any
 * resize that changes the scrollbar's CSS width while crossing a tier, of which
 * device emulation switching to overlay scrollbars is one.
 *
 * The model is `library.test.tsx`'s `crossTier`: the DOM has already moved when
 * the media queries fire (the layout is CSS-driven), and the `ResizeObserver` —
 * jsdom has none, so `window` `resize` stands in for it — arrives later. Every
 * commit after mount is recorded through a `Profiler` and read off the DOM.
 */

interface FakeMql {
  readonly matches: boolean
  media: string
  addEventListener: (type: string, cb: () => void) => void
  removeEventListener: (type: string, cb: () => void) => void
}

let viewportWidth = 1_440
/** The scroller's reserved gutter, i.e. `offsetWidth - clientWidth`. */
let gutterPx = 15
let mqlListeners: (() => void)[] = []
const restorers: (() => void)[] = []

function stubMatchMedia(): void {
  const original = Object.getOwnPropertyDescriptor(window, 'matchMedia')
  Object.defineProperty(window, 'matchMedia', {
    writable: true,
    configurable: true,
    value: (query: string): FakeMql => {
      const m = /min-width:\s*(\d+)px/.exec(query)
      const min = m?.[1] === undefined ? null : Number(m[1])
      return {
        get matches(): boolean {
          return min !== null && viewportWidth >= min
        },
        media: query,
        addEventListener: (_: string, cb: () => void) => mqlListeners.push(cb),
        removeEventListener: (_: string, cb: () => void) => {
          mqlListeners = mqlListeners.filter((x) => x !== cb)
        },
      }
    },
  })
  restorers.push(() => {
    if (original === undefined) Reflect.deleteProperty(window, 'matchMedia')
    else Object.defineProperty(window, 'matchMedia', original)
  })
}

function stubBox(prop: 'clientWidth' | 'offsetWidth', read: (el: HTMLElement) => number): void {
  const original = Object.getOwnPropertyDescriptor(HTMLElement.prototype, prop)
  Object.defineProperty(HTMLElement.prototype, prop, {
    configurable: true,
    get(this: HTMLElement): number {
      return read(this)
    },
  })
  restorers.push(() => {
    if (original === undefined) Reflect.deleteProperty(HTMLElement.prototype, prop)
    else Object.defineProperty(HTMLElement.prototype, prop, original)
  })
}

const isScroller = (el: HTMLElement): boolean => el.dataset.testid === 'library-scroller'

beforeEach(() => {
  viewportWidth = 1_440
  gutterPx = 15
  mqlListeners = []
  stubMatchMedia()
  stubBox('clientWidth', (el) => (isScroller(el) ? 1_000 : 0))
  stubBox('offsetWidth', (el) => (isScroller(el) ? 1_000 + gutterPx : 0))
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  for (const restore of restorers.splice(0).reverse()) restore()
})

/**
 * A `ResizeObserver` the test fires by hand. jsdom has none, which is why the
 * other tests here exercise the `window` `resize` fallback instead.
 */
class FakeResizeObserver {
  static instances: FakeResizeObserver[] = []
  readonly targets = new Set<Element>()
  constructor(private readonly callback: ResizeObserverCallback) {
    FakeResizeObserver.instances.push(this)
  }
  observe(target: Element): void {
    this.targets.add(target)
  }
  unobserve(target: Element): void {
    this.targets.delete(target)
  }
  disconnect(): void {
    this.targets.clear()
  }
  static fireAll(): void {
    for (const o of FakeResizeObserver.instances) {
      if (o.targets.size > 0) o.callback([], o as unknown as ResizeObserver)
    }
  }
  /** Targets still observed by anyone — what an unmount must bring to zero. */
  static live(): number {
    return FakeResizeObserver.instances.reduce((n, o) => n + o.targets.size, 0)
  }
}

/** Only the media queries fire; the DOM (and so the gutter) has already moved. */
function crossTier(width: number, gutter: number): void {
  viewportWidth = width
  gutterPx = gutter
  for (const cb of [...mqlListeners]) cb()
}

interface Commit {
  template: string
  pad: string
}

/** What a 1440 viewport (classic 15px scrollbar) and a 400 one (overlay) settle at. */
const WIDE: Commit = { template: LIST_TEMPLATE.full, pad: listHeaderPadRight(15) }
const NARROW: Commit = { template: LIST_TEMPLATE.stacked, pad: listHeaderPadRight(0) }

function renderList(commits: Commit[]): void {
  const record = (): void => {
    commits.push({
      template: screen.getByTestId('library-list-header').style.gridTemplateColumns,
      pad: screen.getByTestId('library-list-header-wrapper').style.paddingRight,
    })
  }
  render(
    <Profiler id="series-list" onRender={record}>
      <SeriesList items={[]} query="" onOpen={() => undefined} onEndReached={() => undefined} />
    </Profiler>,
  )
}

const disagrees = (c: Commit): boolean =>
  !(
    (c.template === WIDE.template && c.pad === WIDE.pad) ||
    (c.template === NARROW.template && c.pad === NARROW.pad)
  )

describe('SeriesList layout and gutter (item bk)', () => {
  it('commits the layout and the gutter of the same viewport, in both directions', () => {
    const commits: Commit[] = []
    renderList(commits)
    // Mount settles before paint (layout effect); the invariant is about what a
    // resize commits, so recording starts from the settled wide state.
    expect(commits.at(-1)).toEqual(WIDE)
    commits.length = 0

    act(() => {
      crossTier(400, 0)
    })
    act(() => {
      window.dispatchEvent(new Event('resize'))
    })
    expect(commits.at(-1)).toEqual(NARROW)

    act(() => {
      crossTier(1_440, 15)
    })
    act(() => {
      window.dispatchEvent(new Event('resize'))
    })
    expect(commits.at(-1)).toEqual(WIDE)

    // The claim itself: not one commit pairs a template with the other
    // viewport's gutter.
    expect(commits.filter(disagrees)).toEqual([])
  })

  it('lands the new layout on the commit the media queries cause, gutter included', () => {
    const commits: Commit[] = []
    renderList(commits)
    commits.length = 0

    act(() => {
      crossTier(400, 0)
    })
    // Before any observer callback: every commit the tier change alone caused
    // (the row-height re-measure adds one) must already be the settled pair,
    // not the layout with a stale gutter.
    expect(commits).not.toHaveLength(0)
    expect(commits.filter((c) => c.template !== NARROW.template || c.pad !== NARROW.pad)).toEqual(
      [],
    )
  })

  const pad = (): string => screen.getByTestId('library-list-header-wrapper').style.paddingRight

  it('takes a gutter-only change from the ResizeObserver, and lets go of it on unmount', () => {
    FakeResizeObserver.instances = []
    vi.stubGlobal('ResizeObserver', FakeResizeObserver)
    const { unmount } = render(
      <SeriesList items={[]} query="" onOpen={() => undefined} onEndReached={() => undefined} />,
    )
    expect(pad()).toBe(listHeaderPadRight(15))

    // No media query moves: only the scroller's box does.
    gutterPx = 12
    act(() => {
      FakeResizeObserver.fireAll()
    })
    expect(pad()).toBe(listHeaderPadRight(12))

    unmount()
    expect(FakeResizeObserver.live()).toBe(0)
    expect(mqlListeners).toHaveLength(0)
  })

  it('falls back to window resize without a ResizeObserver, and removes it on unmount', () => {
    expect(typeof ResizeObserver).toBe('undefined')
    const added = vi.spyOn(window, 'addEventListener')
    const removed = vi.spyOn(window, 'removeEventListener')
    const { unmount } = render(
      <SeriesList items={[]} query="" onOpen={() => undefined} onEndReached={() => undefined} />,
    )
    expect(pad()).toBe(listHeaderPadRight(15))

    gutterPx = 12
    act(() => {
      window.dispatchEvent(new Event('resize'))
    })
    expect(pad()).toBe(listHeaderPadRight(12))

    unmount()
    const resizeHandlers = (spy: typeof added) =>
      spy.mock.calls.filter(([type]) => type === 'resize').map(([, handler]) => handler)
    expect(resizeHandlers(added)).not.toHaveLength(0)
    expect(resizeHandlers(removed)).toEqual(resizeHandlers(added))
    expect(mqlListeners).toHaveLength(0)
  })

  it("pads the header by exactly the scroller's padding, plus the gutter on the right", () => {
    render(
      <SeriesList items={[]} query="" onOpen={() => undefined} onEndReached={() => undefined} />,
    )
    const header = screen.getByTestId('library-list-header-wrapper').style
    const scroller = screen.getByTestId('library-scroller').style
    // The row grids' inset is the scroller's padding; it has to be readable
    // here for the header to be derived from it rather than from a copy.
    expect(scroller.paddingLeft).not.toBe('')
    expect(scroller.paddingRight).toBe(scroller.paddingLeft)
    expect(header.paddingLeft).toBe(scroller.paddingLeft)
    expect(header.paddingRight).toBe(`calc(${scroller.paddingRight} + 15px)`)
  })
})
