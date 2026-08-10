import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import {
  CHROME_AUTOHIDE_MS,
  CHROME_HINT_MS,
  DEFAULT_FIT,
  STALE_NOTICE_MS,
  cancelChromeAutoHide,
  selectAtVolumeEnd,
  selectStaleAckOwed,
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

  it('opens a book stored at `contain` on 화면 — E-44 gave the fit its button back', () => {
    // The discriminator against E-27 §1's coercion, which answered `height`
    // here: that branch existed only because `FIT_OPTIONS` had no 화면 segment
    // to select (`web/src/features/viewer/ViewerTopBar.tsx:146`). With the
    // segment restored, coercing is what strands the reader — it refuses the fit
    // they chose last session while the control shows them 높이 instead.
    useViewerStore.getState().open('bk', { pageCount: 10, fit: 'contain' })
    expect(useViewerStore.getState().fit).toBe('contain')
  })

  it('opens each of the four fits exactly as stored', () => {
    for (const fit of ['width', 'height', 'original', 'contain'] as const) {
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

  /**
   * **The changed-file notice is the one thing that does *not* carry over
   * (E-45 §1 REVISION).**
   *
   * The first cut reused `continuing` here as well, so the answered warning was
   * inherited by the next volume. The two notices ask different questions: the
   * hint asks whether this *reader* has been greeted, the warning asks whether
   * this *file* changed — and the next volume is a different file. This case is
   * the discriminator, and it is deliberately in the continuation block rather
   * than beside the lifetime tests: what it pins is that one `open()` answers
   * the two with opposite judgements.
   */
  it('re-asks the changed-file question on the next volume, but not the hint', () => {
    useViewerStore.getState().open('bk', { pageCount: 10, stale: true })
    vi.advanceTimersByTime(STALE_NOTICE_MS)
    expect(useViewerStore.getState().staleVisible).toBe(false)
    expect(useViewerStore.getState().hintVisible).toBe(false)

    useViewerStore.getState().open('bk-2', { pageCount: 8, page: 1, stale: true })
    // A different file, so it is asked again — and armed under *its own* id.
    expect(useViewerStore.getState().staleVisible).toBe(true)
    expect(useViewerStore.getState().staleBookId).toBe('bk-2')
    // …while the hint, which is about the reader, stays answered.
    expect(useViewerStore.getState().hintVisible).toBe(false)

    vi.advanceTimersByTime(STALE_NOTICE_MS)
    expect(useViewerStore.getState().staleVisible).toBe(false)
    expect(selectStaleAckOwed(useViewerStore.getState(), 'bk-2')).toBe(true)
  })

  it('drops volume 1’s live timer instead of letting it latch under volume 2', () => {
    // The measured defect (E-45 §1 REVISION): resume volume 1, see the warning,
    // press 다음 권 읽기 **inside** the 3 400 ms window. The timer used to
    // survive the volume change and latch a moment later, and the screen — bound
    // to the route's `:bid` — signed it as volume 2.
    useViewerStore.getState().open('bk', { pageCount: 10, stale: true })
    vi.advanceTimersByTime(STALE_NOTICE_MS - 1)
    expect(useViewerStore.getState().staleVisible).toBe(true)

    useViewerStore.getState().open('bk-2', { pageCount: 8, page: 1, stale: false })
    // One millisecond later volume 1's timer would have fired.
    vi.advanceTimersByTime(1)
    expect(useViewerStore.getState().staleSeen).toBe(false)
    expect(useViewerStore.getState().staleBookId).toBeNull()
    vi.advanceTimersByTime(STALE_NOTICE_MS * 2)
    expect(useViewerStore.getState().staleSeen).toBe(false)
    // Neither book is signed for: volume 1 was left inside its window, and
    // volume 2 was never stale.
    expect(selectStaleAckOwed(useViewerStore.getState(), 'bk')).toBe(false)
    expect(selectStaleAckOwed(useViewerStore.getState(), 'bk-2')).toBe(false)
  })
})

/**
 * The `파일이 변경되었습니다` lifetime (**E-45 §1, §2**).
 *
 * The defect this replaces was not a wrong duration but a *derivation*: the
 * screen read `progress.stale` out of the query cache on every render, and the
 * automatic progress `PUT` — which goes out because the book loaded, not because
 * the reader did anything — answered `stale:false` and unmounted the notice
 * about a second in. The store tier's job here is the half that has no DOM: the
 * latch survives on its own clock, and the acknowledgement latches **only** when
 * the notice has been up for the whole of it.
 */
describe('the changed-file notice has a lifetime (ruling E-45)', () => {
  it('is deliberately the opening hint’s lifetime, as two constants', () => {
    // E-45 §1 makes them equal on purpose; they are separate names so that
    // retuning the opening hint cannot silently retune this.
    expect(STALE_NOTICE_MS).toBe(3_400)
    expect(STALE_NOTICE_MS).toBe(CHROME_HINT_MS)
  })

  it('stays up for its whole lifetime and then acknowledges itself', () => {
    useViewerStore.getState().open('bk', { pageCount: 10, stale: true })
    expect(useViewerStore.getState().staleVisible).toBe(true)
    expect(useViewerStore.getState().staleSeen).toBe(false)

    vi.advanceTimersByTime(STALE_NOTICE_MS - 1)
    expect(useViewerStore.getState().staleVisible).toBe(true)
    // Not owed one millisecond early — the acknowledgement is the *whole* life.
    expect(useViewerStore.getState().staleSeen).toBe(false)

    vi.advanceTimersByTime(1)
    expect(useViewerStore.getState().staleVisible).toBe(false)
    expect(useViewerStore.getState().staleSeen).toBe(true)
  })

  it('arms nothing at all for a book whose progress is current', () => {
    open(10)
    expect(useViewerStore.getState().staleVisible).toBe(false)
    vi.advanceTimersByTime(STALE_NOTICE_MS * 2)
    expect(useViewerStore.getState().staleSeen).toBe(false)
  })

  it('owes no acknowledgement when the reader leaves inside the window', () => {
    useViewerStore.getState().open('bk', { pageCount: 10, stale: true })
    vi.advanceTimersByTime(STALE_NOTICE_MS - 1)
    useViewerStore.getState().close()

    // The correct ending (E-45 §2): the baseline survives, so the *next* entry
    // warns again. A timer left running would have latched after the exit and
    // acknowledged a notice the reader never finished reading.
    vi.advanceTimersByTime(STALE_NOTICE_MS * 2)
    expect(useViewerStore.getState().staleSeen).toBe(false)
    expect(useViewerStore.getState().staleVisible).toBe(false)
  })

  it('warns again on a fresh entry into the same book', () => {
    useViewerStore.getState().open('bk', { pageCount: 10, stale: true })
    vi.advanceTimersByTime(STALE_NOTICE_MS)
    useViewerStore.getState().close()

    useViewerStore.getState().open('bk', { pageCount: 10, stale: false })
    expect(useViewerStore.getState().staleVisible).toBe(false)
    useViewerStore.getState().open('bk', { pageCount: 10, stale: true })
    expect(useViewerStore.getState().staleVisible).toBe(true)
  })

  it('consumes the latch on dismiss, so one notice cannot be acknowledged twice', () => {
    useViewerStore.getState().open('bk', { pageCount: 10, stale: true })
    vi.advanceTimersByTime(STALE_NOTICE_MS)
    expect(useViewerStore.getState().staleSeen).toBe(true)

    useViewerStore.getState().dismissStale()
    expect(useViewerStore.getState().staleSeen).toBe(false)
    expect(useViewerStore.getState().staleVisible).toBe(false)
    // The owner goes with it, or a book re-opened under the same id would find
    // a latch that says it is owed something.
    expect(useViewerStore.getState().staleBookId).toBeNull()
    vi.advanceTimersByTime(STALE_NOTICE_MS * 2)
    expect(useViewerStore.getState().staleSeen).toBe(false)
  })

  /**
   * **The acknowledgement names its own book (E-45 §1 REVISION).**
   *
   * `staleSeen` is written by a timer and spent by a screen bound to the route's
   * `:bid`, and those two disagree for the length of a volume change. The ruling
   * asks that the invariant be assertable *in one place* rather than emerging
   * from two files agreeing; this is that place.
   */
  it('owes the acknowledgement to the book it was armed for, and to no other', () => {
    useViewerStore.getState().open('bk', { pageCount: 10, stale: true })
    expect(useViewerStore.getState().staleBookId).toBe('bk')
    // Not owed before the notice has served its whole life.
    expect(selectStaleAckOwed(useViewerStore.getState(), 'bk')).toBe(false)

    vi.advanceTimersByTime(STALE_NOTICE_MS)
    expect(selectStaleAckOwed(useViewerStore.getState(), 'bk')).toBe(true)
    // The single fact the screen used to get wrong: a latch is not a licence to
    // write `stale_seen` against whatever book the route happens to name.
    expect(selectStaleAckOwed(useViewerStore.getState(), 'bk-2')).toBe(false)
    expect(selectStaleAckOwed(useViewerStore.getState(), '')).toBe(false)
  })

  /**
   * **A book with no pages is never armed (E-45 §2, the symmetric `isStale`).**
   *
   * `isStale(recorded, current)` is false when *either* side is 0, so a
   * well-behaved server cannot send `stale:true` for a broken book. This is the
   * screen's own half of that contract, not a reliance on it — and it has to be
   * here rather than at the call site, because the trap is a closed loop: the
   * screen is already showing 열 수 없는 파일, and `progressReady`'s
   * `pageCount > 0` makes the acknowledgement literally unsendable, so the
   * notice would run its whole life, latch, never be answered, and come back on
   * every single entry for ever.
   */
  it('arms nothing for a book with no pages, whatever the server said', () => {
    useViewerStore.getState().open('bk', { pageCount: 0, stale: true })
    expect(useViewerStore.getState().staleVisible).toBe(false)
    expect(useViewerStore.getState().staleBookId).toBeNull()
    vi.advanceTimersByTime(STALE_NOTICE_MS * 2)
    expect(useViewerStore.getState().staleSeen).toBe(false)
    expect(selectStaleAckOwed(useViewerStore.getState(), 'bk')).toBe(false)
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
