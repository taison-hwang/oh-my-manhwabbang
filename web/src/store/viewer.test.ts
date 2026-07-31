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

describe('step (ui-spec §8.2)', () => {
  it('advances one page in single mode', () => {
    open(214, 12)
    useViewerStore.getState().step(1)
    expect(useViewerStore.getState().page).toBe(13)
    useViewerStore.getState().step(-1)
    expect(useViewerStore.getState().page).toBe(12)
  })

  it('advances **two** pages in spread mode', () => {
    open(214, 12)
    useViewerStore.getState().setMode('spread')
    useViewerStore.getState().step(1)
    expect(useViewerStore.getState().page).toBe(14)
    useViewerStore.getState().step(-1)
    expect(useViewerStore.getState().page).toBe(12)
  })

  it('advances one page in vertical mode', () => {
    open(214, 12)
    useViewerStore.getState().setMode('vertical')
    useViewerStore.getState().step(1)
    expect(useViewerStore.getState().page).toBe(13)
  })

  it('clamps to [1, pageCount] instead of wrapping', () => {
    open(214, 1)
    useViewerStore.getState().step(-1)
    expect(useViewerStore.getState().page).toBe(1)

    useViewerStore.getState().goTo(214)
    useViewerStore.getState().step(1)
    expect(useViewerStore.getState().page).toBe(214)
  })

  it('lands exactly on the last page from an odd position in spread mode', () => {
    // 213 + 2 would be 215; the clamp is what raises the next-volume card.
    open(214, 213)
    useViewerStore.getState().setMode('spread')
    useViewerStore.getState().step(1)
    expect(useViewerStore.getState().page).toBe(214)
    expect(selectAtVolumeEnd(useViewerStore.getState())).toBe(true)
  })

  it('marks the page as loading so the spinner timer can start', () => {
    open(214, 12)
    useViewerStore.getState().setLoading(false)
    useViewerStore.getState().step(1)
    expect(useViewerStore.getState().loading).toBe(true)
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

  it('reading never summons the chrome — neither a page turn nor a scroll (E-27)', () => {
    open(214, 12)
    useViewerStore.getState().wake()
    useViewerStore.getState().hideChrome()

    useViewerStore.getState().step(1)
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
