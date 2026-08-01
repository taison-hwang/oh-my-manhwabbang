import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import {
  CHROME_AUTOHIDE_MS,
  CHROME_HINT_MS,
  DEFAULT_FIT,
  cancelChromeAutoHide,
  selectAtVolumeEnd,
  useViewerStore,
} from './viewer'

const open = (pageCount: number, page = 1): void => {
  useViewerStore.getState().open('bk', { pageCount, page })
}

beforeEach(() => {
  vi.useFakeTimers()
  useViewerStore.getState().close()
})

afterEach(() => {
  cancelChromeAutoHide()
  vi.useRealTimers()
})

describe('defaults', () => {
  it('fits to height (C-13 — the prototype capture, not the config guess)', () => {
    expect(DEFAULT_FIT).toBe('height')
    open(10)
    expect(useViewerStore.getState().fit).toBe('height')
  })

  it('uses the frozen wire vocabulary, not the ui-spec labels', () => {
    open(10)
    useViewerStore.getState().setMode('spread') // C-1: never "double"
    useViewerStore.getState().setFit('contain') // C-2: never "screen"
    expect(useViewerStore.getState().mode).toBe('spread')
    expect(useViewerStore.getState().fit).toBe('contain')
  })
})

/**
 * `turnTo` — the reading path (ui-spec §8.2).
 *
 * The stride is **not** tested here and must not move back here. It belongs to
 * `fit.ts` (`nextPage`/`prevPage`, asserted in `fit.test.ts`) because only the
 * screen knows how many pages are on the stage — FR-VWR-004 lets a landscape
 * scan hold a spread slot alone. The predecessor of this store action worked its
 * own stride out from `mode`, which made it uncallable by the screen, which is
 * how E-27's page-turn row went four sessions unimplemented behind a green test.
 */
describe('turnTo (ui-spec §8.2)', () => {
  it('commits the page it is given, with no stride of its own', () => {
    open(214, 12)
    useViewerStore.getState().setMode('spread')
    // The discriminator against the `step(delta)` shape this replaced: a "+1"
    // in 양면 used to mean 14. `turnTo` is told where to land, and lands there.
    useViewerStore.getState().turnTo(13)
    expect(useViewerStore.getState().page).toBe(13)
    useViewerStore.getState().turnTo(12)
    expect(useViewerStore.getState().page).toBe(12)
  })

  it('clamps to [1, pageCount] instead of wrapping', () => {
    open(214, 1)
    useViewerStore.getState().turnTo(0)
    expect(useViewerStore.getState().page).toBe(1)

    useViewerStore.getState().turnTo(9_999)
    expect(useViewerStore.getState().page).toBe(214)
  })

  it('lands on the last page, which is what raises the next-volume card', () => {
    open(214, 213)
    useViewerStore.getState().setMode('spread')
    // `nextPage(213, …)` in 양면 is 214, not 215; the clamp holds either way.
    useViewerStore.getState().turnTo(215)
    expect(useViewerStore.getState().page).toBe(214)
    expect(selectAtVolumeEnd(useViewerStore.getState())).toBe(true)
  })

  it('marks the page as loading so the spinner timer can start', () => {
    open(214, 12)
    useViewerStore.getState().setLoading(false)
    useViewerStore.getState().turnTo(13)
    expect(useViewerStore.getState().loading).toBe(true)
  })

  it('does nothing at all for a turn that does not move — no spurious loading', () => {
    open(214, 12)
    useViewerStore.getState().setLoading(false)
    // Both ends of the book answer this way: `nextPage` clamps, so the last
    // page turned forward asks to go where it already is.
    useViewerStore.getState().turnTo(12)
    expect(useViewerStore.getState().page).toBe(12)
    expect(useViewerStore.getState().loading).toBe(false)
  })
})

describe('selectAtVolumeEnd (ui-spec §6.5)', () => {
  it('is true on the last page of a paged mode', () => {
    open(3, 3)
    expect(selectAtVolumeEnd(useViewerStore.getState())).toBe(true)
  })

  it('is false in vertical mode — scrolling past the end is the end', () => {
    open(3, 3)
    useViewerStore.getState().setMode('vertical')
    expect(selectAtVolumeEnd(useViewerStore.getState())).toBe(false)
  })

  it('is false when the viewer is closed', () => {
    expect(selectAtVolumeEnd(useViewerStore.getState())).toBe(false)
  })
})

describe('opening chromeless (ruling E-27)', () => {
  it('opens with no chrome and the hint up, and lets the hint expire on its own', () => {
    open(10)
    expect(useViewerStore.getState().chromeVisible).toBe(false)
    expect(useViewerStore.getState().hintVisible).toBe(true)
    vi.advanceTimersByTime(CHROME_HINT_MS - 1)
    expect(useViewerStore.getState().hintVisible).toBe(true)
    vi.advanceTimersByTime(1)
    expect(useViewerStore.getState().hintVisible).toBe(false)
  })

  it('never arms an auto-hide it does not need — opening does not wake', () => {
    open(10)
    // If `open` woke the chrome the way it used to, this would flip it *on* and
    // then off, and the assertion below would pass for the wrong reason. The
    // point is that nothing is scheduled at all.
    vi.advanceTimersByTime(CHROME_AUTOHIDE_MS * 3)
    expect(useViewerStore.getState().chromeVisible).toBe(false)
  })

  it('a deliberate toggle answers the hint for good', () => {
    open(10)
    useViewerStore.getState().toggleChrome()
    expect(useViewerStore.getState().hintVisible).toBe(false)
    useViewerStore.getState().toggleChrome()
    // Still inside the 3 400 ms window, and it does not come back.
    expect(useViewerStore.getState().hintVisible).toBe(false)
  })

  it('opens a book stored at `contain` on 높이 — the fit E-27 removed the control for', () => {
    useViewerStore.getState().open('bk', { pageCount: 10, fit: 'contain' })
    expect(useViewerStore.getState().fit).toBe('height')
    // The wire value itself is untouched: `setFit` still round-trips it, so a
    // client that has one in `user.db` is not rewritten by being read.
    useViewerStore.getState().setFit('contain')
    expect(useViewerStore.getState().fit).toBe('contain')
  })

  it('opens the three fits that still have controls exactly as stored', () => {
    for (const fit of ['width', 'height', 'original'] as const) {
      useViewerStore.getState().open('bk', { pageCount: 10, fit })
      expect(useViewerStore.getState().fit).toBe(fit)
    }
  })
})

describe('changing volume is a continuation, not an entry', () => {
  /** Raise the chrome and open the strip, i.e. the state a reader has set up. */
  const settle = (): void => {
    useViewerStore.getState().toggleChrome()
    useViewerStore.getState().setStripOpen(true)
  }

  it('keeps the chrome, the strip and the answered hint across 다음 권', () => {
    open(10, 10)
    settle()
    expect(useViewerStore.getState().hintVisible).toBe(false)

    useViewerStore.getState().open('bk-2', { pageCount: 8, page: 1 })

    expect(useViewerStore.getState().bookId).toBe('bk-2')
    expect(useViewerStore.getState().page).toBe(1)
    expect(useViewerStore.getState().chromeVisible).toBe(true)
    expect(useViewerStore.getState().stripOpen).toBe(true)
    // The line exists to be read once. Replaying it every volume is noise.
    expect(useViewerStore.getState().hintVisible).toBe(false)
    vi.advanceTimersByTime(CHROME_HINT_MS)
    expect(useViewerStore.getState().hintVisible).toBe(false)
  })

  it('leaves a chromeless reader chromeless, and still does not arm a hide', () => {
    open(10, 10)
    useViewerStore.getState().open('bk-2', { pageCount: 8, page: 1 })
    expect(useViewerStore.getState().chromeVisible).toBe(false)
    vi.advanceTimersByTime(CHROME_AUTOHIDE_MS * 3)
    expect(useViewerStore.getState().chromeVisible).toBe(false)
  })

  it('re-arms the auto-hide for chrome that carried over', () => {
    open(10, 10)
    settle()
    useViewerStore.getState().open('bk-2', { pageCount: 8, page: 1 })
    vi.advanceTimersByTime(CHROME_AUTOHIDE_MS)
    // Carried over, not pinned: it still goes away on its own.
    expect(useViewerStore.getState().chromeVisible).toBe(false)
  })

  it('is an entry again after the viewer has been left', () => {
    open(10, 10)
    settle()
    useViewerStore.getState().close()
    open(8)
    expect(useViewerStore.getState().chromeVisible).toBe(false)
    expect(useViewerStore.getState().hintVisible).toBe(true)
    expect(useViewerStore.getState().stripOpen).toBe(false)
  })
})

describe('chrome auto-hide (ui-spec §8.2, widened to 2 600 ms by E-27)', () => {
  it('hides exactly CHROME_AUTOHIDE_MS after the last wake', () => {
    open(10)
    useViewerStore.getState().wake()
    expect(useViewerStore.getState().chromeVisible).toBe(true)
    vi.advanceTimersByTime(CHROME_AUTOHIDE_MS - 1)
    expect(useViewerStore.getState().chromeVisible).toBe(true)
    vi.advanceTimersByTime(1)
    expect(useViewerStore.getState().chromeVisible).toBe(false)
  })

  it('re-arms on every wake rather than accumulating timers', () => {
    open(10)
    useViewerStore.getState().wake()
    vi.advanceTimersByTime(2400)
    useViewerStore.getState().wake()
    vi.advanceTimersByTime(2400)
    expect(useViewerStore.getState().chromeVisible).toBe(true)
    vi.advanceTimersByTime(200)
    expect(useViewerStore.getState().chromeVisible).toBe(false)
  })

  it('does not steal the chrome away mid-drag — the preview lives in it', () => {
    open(214, 12)
    useViewerStore.getState().setDragging(true, 100)
    vi.advanceTimersByTime(CHROME_AUTOHIDE_MS * 2)
    expect(useViewerStore.getState().chromeVisible).toBe(true)
    expect(useViewerStore.getState().dragPage).toBe(100)
  })

  it('toggles both ways from a tap in the centre zone', () => {
    open(10)
    useViewerStore.getState().toggleChrome()
    expect(useViewerStore.getState().chromeVisible).toBe(true)
    useViewerStore.getState().toggleChrome()
    expect(useViewerStore.getState().chromeVisible).toBe(false)
  })

  it('holds the chrome open under the pointer, and releases it on the way out', () => {
    open(10)
    useViewerStore.getState().wake()
    useViewerStore.getState().holdChrome()
    vi.advanceTimersByTime(CHROME_AUTOHIDE_MS * 3)
    expect(useViewerStore.getState().chromeVisible).toBe(true)

    useViewerStore.getState().releaseChrome()
    vi.advanceTimersByTime(CHROME_AUTOHIDE_MS - 1)
    expect(useViewerStore.getState().chromeVisible).toBe(true)
    vi.advanceTimersByTime(1)
    expect(useViewerStore.getState().chromeVisible).toBe(false)
  })

  it('a wake while held does not re-arm the timer behind the hold', () => {
    open(10)
    useViewerStore.getState().holdChrome()
    useViewerStore.getState().wake()
    vi.advanceTimersByTime(CHROME_AUTOHIDE_MS * 3)
    expect(useViewerStore.getState().chromeVisible).toBe(true)
  })

  /**
   * Both halves are idempotent, and `ViewerPage` depends on it.
   *
   * The hold is no longer inferred from one crossing into a bar — it is derived
   * from what the browser says is under the pointer, re-answered on every
   * boundary crossing anywhere in the viewer and on every move over the stage.
   * That rule fires many times for one journey, so a `releaseChrome` that
   * re-armed unconditionally would push the 2 600 ms deadline back on each
   * twitch of a mouse resting over the page — a chrome that never goes away,
   * which is E-27 read backwards.
   */
  it('releasing a chrome nobody is holding does not push the deadline back', () => {
    open(10)
    useViewerStore.getState().wake()
    vi.advanceTimersByTime(CHROME_AUTOHIDE_MS - 200)
    // What a mouse moving over the page does, over and over.
    for (let i = 0; i < 5; i++) useViewerStore.getState().releaseChrome()
    vi.advanceTimersByTime(200)
    expect(useViewerStore.getState().chromeVisible).toBe(false)
  })

  it('holding twice is holding once — the second is not a second timer', () => {
    open(10)
    useViewerStore.getState().wake()
    useViewerStore.getState().holdChrome()
    useViewerStore.getState().holdChrome()
    useViewerStore.getState().releaseChrome()
    vi.advanceTimersByTime(CHROME_AUTOHIDE_MS)
    expect(useViewerStore.getState().chromeVisible).toBe(false)
  })

  /**
   * **A hold may not outlive the viewer that took it.**
   *
   * `chromeHeld` is module-scoped, so it survives every unmount — and a hold
   * left standing disarms the auto-hide for the rest of the session, with
   * nothing on screen able to re-arm it. `close()` is the guarantee: the viewer
   * unmounts through it (`ViewerPage`'s cleanup effect) and 뒤로/`Esc` call it
   * directly. Without the reset, the assertion below reads `true` forever.
   */
  it('a hold does not survive leaving the viewer', () => {
    open(10)
    useViewerStore.getState().wake()
    useViewerStore.getState().holdChrome()
    useViewerStore.getState().close()

    // **No second `open()` here, deliberately.** `open()` resets the flag as
    // well (pinned by the test below), so re-entering the viewer at this point
    // would be green whether `close()` reset it or not — the two guarantees
    // would mask each other and neither would be pinned by anything. Measured:
    // deleting `close()`'s reset left that version of this test passing.
    useViewerStore.getState().wake()
    vi.advanceTimersByTime(CHROME_AUTOHIDE_MS)
    expect(useViewerStore.getState().chromeVisible).toBe(false)
  })

  /**
   * The same guarantee for the other way a viewer changes underneath a pointer:
   * 다음 권 읽기, which arrives here as a second `open()` on a different book
   * with the screen still mounted. The bar the pointer was resting on belongs to
   * the volume that just went away.
   */
  it('a hold does not survive a change of volume', () => {
    open(10)
    useViewerStore.getState().wake()
    useViewerStore.getState().holdChrome()

    useViewerStore.getState().open('bk-2', { pageCount: 10, page: 1 })
    expect(
      useViewerStore.getState().chromeVisible,
      'E-28 §3: a continuation keeps the chrome the reader had raised',
    ).toBe(true)
    vi.advanceTimersByTime(CHROME_AUTOHIDE_MS)
    expect(useViewerStore.getState().chromeVisible).toBe(false)
  })

  // Necessary, and on its own **not sufficient** — that is the whole history of
  // this rule. Its predecessor asserted the same thing about a store action no
  // screen could call, so the shipped build woke the chrome on every arrow key
  // while this stayed green (HANDOFF §6.5). The assertion that the *screen* only
  // ever turns pages through here lives in `ViewerPage.test.tsx`; neither test
  // replaces the other.
  it('reading never summons the chrome — neither a page turn nor a scroll (E-27)', () => {
    open(214, 12)
    useViewerStore.getState().wake()
    useViewerStore.getState().hideChrome()

    useViewerStore.getState().turnTo(13)
    expect(useViewerStore.getState().page).toBe(13)
    expect(useViewerStore.getState().chromeVisible).toBe(false)

    useViewerStore.getState().syncPage(40)
    expect(useViewerStore.getState().page).toBe(40)
    expect(useViewerStore.getState().chromeVisible).toBe(false)
  })

  it('operating a control still does — the bar must not vanish under the press', () => {
    open(214, 12)
    useViewerStore.getState().hideChrome()
    useViewerStore.getState().goTo(90)
    expect(useViewerStore.getState().chromeVisible).toBe(true)
  })
})

describe('page bounds', () => {
  it('clamps the resume page into range on open', () => {
    open(50, 999)
    expect(useViewerStore.getState().page).toBe(50)
  })

  it('re-clamps when a shorter page count arrives', () => {
    open(500, 400)
    useViewerStore.getState().setPageCount(100)
    expect(useViewerStore.getState().page).toBe(100)
  })

  it('clamps the drag preview to the book', () => {
    open(50, 10)
    useViewerStore.getState().setDragging(true, 9999)
    expect(useViewerStore.getState().dragPage).toBe(50)
    useViewerStore.getState().setDragging(false)
    expect(useViewerStore.getState().dragPage).toBeNull()
  })
})
