import { create } from 'zustand'

/**
 * Viewer UI state (ui-spec §6, §8.2).
 *
 * Only UI state lives here — page bytes, page metadata and reading progress are
 * server data and belong to TanStack Query (impl-plan §5.2). What this store
 * owns is: which page is showing, whether the chrome is awake, and the three
 * display controls.
 *
 * Wire values are the frozen contract's, not the ui-spec's labels: `spread`
 * (not `double`, C-1) and `contain` (not `screen`, C-2). The Korean labels
 * 양면 / 화면 are a rendering concern.
 */

export type DisplayMode = 'single' | 'spread' | 'vertical'
export type FitMode = 'width' | 'height' | 'original' | 'contain'
export type ReadingDirection = 'ltr' | 'rtl'

/** Chrome auto-hides this long after the last wake (ui-spec §8.2, E-27). */
export const CHROME_AUTOHIDE_MS = 2600

/**
 * How long the opening hint stays up (**E-27**).
 *
 * The viewer now opens chromeless, so the reader is handed a black screen with
 * no visible way back. This is the one sentence that says where the controls
 * went, and it is timed rather than dismissed: a hint that needs to be closed
 * is a second thing to learn.
 */
export const CHROME_HINT_MS = 3400

/**
 * How long `파일이 변경되었습니다` stays up (**E-45 §1**).
 *
 * The viewer carries two one-line notices and only one of them had a written
 * lifetime. E-45 hands the second the first's contract: timed rather than
 * dismissible, and — because the two are the same shape on the same screen —
 * **the same 3400 ms**. The *unit* the two are armed over is not shared: the
 * hint is once per **entry**, this is once per **book** (E-45 §1 REVISION).
 *
 * **It is a separate constant on purpose, and it must stay one.** The two agree
 * because a ruling said they should, not because either is derived from the
 * other. Writing this as `= CHROME_HINT_MS` would mean a later session retuning
 * the opening hint silently retunes a warning about the reader's saved place,
 * which is a different question with a different answer.
 */
export const STALE_NOTICE_MS = 3400

/** Default fit is `height`, per C-13 — the prototype capture, not the guess. */
export const DEFAULT_FIT: FitMode = 'height'

/**
 * The fit a book is *opened* at — **every stored fit, exactly as stored**.
 *
 * The one job left is the `undefined` fallback, and it has **one** call site:
 * `open()` below, where `ViewerOpenOptions.fit` is optional (`:94`). This is
 * where "no fit was given" is turned into `DEFAULT_FIT` once, in the store, so
 * that a caller who omits it and a caller who passes `undefined` cannot drift
 * apart. `viewer.test.ts:28-32` is its only guard — the `open(10)` helper there
 * passes no `fit` — and its only exercise: the single production caller,
 * `ViewerPage`'s open effect (`web/src/features/viewer/ViewerPage.tsx:287`),
 * always has one to pass, because `BookPrefs.fit_mode` is non-nullable
 * (`web/src/api/types.ts:429`). The fallback stays regardless: the field is
 * optional in this store's own type, so the store owes an answer for it.
 *
 * **The 이 권 전용 설정 reset no longer calls this.** It used to
 * (E-33 §3), for the coercion below and only for that; with the coercion gone
 * the call was a provable identity — `useSetPrefs.onSuccess` is handed a whole
 * `BookPrefs`, never `undefined` — so `ViewerPage.tsx:474` now reads
 * `setFit(prefs.fit_mode)`, unwrapped like the `setMode` beside it.
 *
 * **The `contain` → `height` branch is gone (ruling E-44).** E-27 §1 put it
 * here because it had just deleted 화면 from `FIT_OPTIONS`
 * (`web/src/features/viewer/ViewerTopBar.tsx:146`), and a reader parked on a fit
 * with no button can neither see which one they are on nor leave it. E-44
 * restores the button, which removes the branch's entire reason and inverts its
 * effect: coercing now means a reader who deliberately chose 화면 last session
 * is opened on 높이, with the 화면 segment visibly unselected and their stored
 * preference contradicted by the very control that is meant to show it. Read
 * this together with the note on `FIT_OPTIONS` — the pair moves as a pair.
 */
export function openingFit(fit: FitMode | undefined): FitMode {
  return fit ?? DEFAULT_FIT
}
export const DEFAULT_MODE: DisplayMode = 'single'
export const DEFAULT_DIRECTION: ReadingDirection = 'ltr'

export interface ViewerOpenOptions {
  pageCount: number
  page?: number
  mode?: DisplayMode
  dir?: ReadingDirection
  fit?: FitMode
  /**
   * `progress.stale` **as it read at the moment this book was opened** (E-45 §1).
   *
   * Read once, here, and latched — never re-derived per render. That derivation
   * (`detail?.progress?.stale === true` in the screen) is the whole of the
   * defect E-45 names: the PUT that `useSaveProgress.onSuccess` writes back into
   * the query cache answers `stale:false`, so the notice unmounted about a
   * second after it appeared and never came back.
   *
   * **Per book, not per entry (E-45 §1 REVISION).** The opening hint asks "have
   * I already greeted this reader", which carries across 다음 권; this asks "has
   * *this file* changed", and the next volume is a different file. See `open()`.
   */
  stale?: boolean
}

export interface ViewerState {
  /** `null` when the viewer is closed. The viewer is an overlay, not a screen. */
  bookId: string | null
  /** 1-based, always clamped into `[1, pageCount]` (or 1 when empty). */
  page: number
  pageCount: number
  mode: DisplayMode
  dir: ReadingDirection
  fit: FitMode
  /** Overlays fade on opacity; they are never unmounted (ui-spec §6.6). */
  chromeVisible: boolean
  /** The opening "where did the controls go" line; shown only while chromeless. */
  hintVisible: boolean
  /**
   * The `파일이 변경되었습니다` notice (**E-45 §1**). Unlike the hint it is *not*
   * gated on the chrome (E-27) — the chrome never appears on its own.
   */
  staleVisible: boolean
  /**
   * The notice has been on screen for its **whole** `STALE_NOTICE_MS`, so the
   * acknowledgement `PUT` is owed (**E-45 §2**).
   *
   * This is the only signal that lets the server re-baseline `page_count`, and
   * it is deliberately not inferable from anything else on this store. Turning a
   * page is not an acknowledgement: `useProgressSync`'s write goes out because
   * the book loaded, with the reader having done nothing. A reader who closes
   * the tab one second in never sets this, the baseline survives, and the next
   * entry warns again — which is the correct ending.
   *
   * **It is only half an answer on its own.** This says *that* a notice ran its
   * course; `staleBookId` beside it says *whose*. Read the pair through
   * `selectStaleAckOwed`, never this flag alone (E-45 §1 REVISION).
   */
  staleSeen: boolean
  /**
   * The book `staleVisible` / `staleSeen` were armed for (**E-45 §1 REVISION**).
   *
   * `null` whenever nothing is armed. It exists because the acknowledgement is
   * born in one file and spent in another: the store latches, and the screen's
   * `useProgressSync` — bound to the route's `:bid` — puts the `PUT` on the
   * wire. Those two disagree for exactly as long as it takes a volume change to
   * reach `open()`, and in that window a latch armed on volume 1 was signed for
   * volume 2, burning a baseline over a warning that reader never saw. Carrying
   * the id makes "this acknowledgement belongs to that book" a fact one function
   * can state (`selectStaleAckOwed`) rather than an invariant two files have to
   * cooperate to keep.
   */
  staleBookId: string | null
  stripOpen: boolean
  dragging: boolean
  /** The page under the slider thumb while dragging; committed on release. */
  dragPage: number | null
  loading: boolean

  open: (bookId: string, options: ViewerOpenOptions) => void
  close: () => void
  setPageCount: (n: number) => void
  /**
   * The **control** path: the slider, the thumbnail strip, a direct jump.
   *
   * The only one of the three page setters that wakes the chrome, and it has to
   * — the controls live in the bars, so the bar must not fade out from under the
   * press.
   */
  goTo: (page: number) => void
  /**
   * The stage reporting which page the reader has scrolled to (세로 mode).
   *
   * Reading, so it does not wake the chrome — routing it through `goTo` made the
   * bars flash on every wheel tick. It differs from `turnTo` in the other field:
   * it sets no `loading`, because a scroll moves through pages that are already
   * painted rather than replacing what is on the stage.
   */
  syncPage: (page: number) => void
  /**
   * A page turn on the **reading** path — the arrow keys, `Space`, the side tap
   * zones and a swipe.
   *
   * Absolute, not a delta: the stride is however many pages are *actually* on
   * the stage, and only the screen can know that (FR-VWR-004 lets a landscape
   * scan hold a spread slot on its own). `fit.ts`'s `nextPage`/`prevPage` work
   * the destination out; this only commits it.
   */
  turnTo: (page: number) => void
  setMode: (mode: DisplayMode) => void
  setDirection: (dir: ReadingDirection) => void
  setFit: (fit: FitMode) => void
  /** Show the chrome and re-arm the auto-hide (unless it is being held). */
  wake: () => void
  /**
   * Pin the chrome open while the pointer is inside it, and let go again.
   *
   * Without this the bars dissolve under a pointer that is resting on them —
   * the reader is looking at the control they are about to press.
   *
   * **Both are idempotent, and that is a contract, not an optimisation.** The
   * screen no longer infers the hold from a boundary crossing — it *derives* it
   * from where the browser says the pointer is, on every crossing anywhere in
   * the viewer (`ViewerPage`'s `trackChromeHover`). That rule fires many times
   * for one journey across a bar, and a `releaseChrome` that re-armed on each
   * of them would hand the reader a chrome that never goes away as long as the
   * mouse keeps twitching over the page. So a call that does not change the
   * answer does nothing at all.
   */
  holdChrome: () => void
  releaseChrome: () => void
  hideChrome: () => void
  toggleChrome: () => void
  dismissHint: () => void
  /**
   * The stale notice has been **answered** — take it down and drop the owed
   * acknowledgement (E-45 §2).
   *
   * Same shape as `dismissHint`, and one production caller: the screen calls it
   * the moment it has put `stale_seen: true` on the wire, so the latch cannot
   * fire a second `PUT` on the next render. It clears the timer too, because the
   * notice can be answered only once and a timer still running would re-latch.
   */
  dismissStale: () => void
  toggleStrip: () => void
  setStripOpen: (open: boolean) => void
  setDragging: (dragging: boolean, page?: number) => void
  setLoading: (loading: boolean) => void
}

function clampPage(page: number, pageCount: number): number {
  if (pageCount <= 0) return 1
  return Math.max(1, Math.min(pageCount, Math.trunc(page)))
}

/**
 * The auto-hide timer.
 *
 * Module-scoped rather than in the store because it is not state: nothing
 * renders from it, and putting a timer id in the store would make every wake()
 * a re-render of the whole viewer.
 */
let autoHideTimer: ReturnType<typeof setTimeout> | null = null
let hintTimer: ReturnType<typeof setTimeout> | null = null
/** The stale notice's lifetime (E-45 §1), module-scoped for the reason above. */
let staleTimer: ReturnType<typeof setTimeout> | null = null

/**
 * True while the pointer rests inside the chrome.
 *
 * Module-scoped for the same reason as the timer: it is not state — nothing
 * renders from it, and it only ever decides whether an already-running wake
 * arms a timer.
 *
 * **Module-scoped state outlives the component that set it**, which is why
 * `open()` and `close()` below both put it back to `false`. A hold is a claim
 * about a pointer resting on a bar of *this* viewer; a viewer that has been
 * left, or a book that has been swapped underneath it, has no such pointer and
 * must not inherit the claim — the auto-hide would be disarmed for the rest of
 * the session with nothing on screen able to re-arm it.
 */
let chromeHeld = false

function clearAutoHide(): void {
  if (autoHideTimer !== null) {
    clearTimeout(autoHideTimer)
    autoHideTimer = null
  }
}

function clearHint(): void {
  if (hintTimer !== null) {
    clearTimeout(hintTimer)
    hintTimer = null
  }
}

function clearStale(): void {
  if (staleTimer !== null) {
    clearTimeout(staleTimer)
    staleTimer = null
  }
}

/** Arm the auto-hide, unless the pointer is holding the chrome open. */
function armAutoHide(): void {
  clearAutoHide()
  if (chromeHeld) return
  autoHideTimer = setTimeout(() => {
    autoHideTimer = null
    // Never steal the chrome away mid-drag: the slider preview lives in it.
    if (!useViewerStore.getState().dragging) {
      useViewerStore.setState({ chromeVisible: false })
    }
  }, CHROME_AUTOHIDE_MS)
}

export const useViewerStore = create<ViewerState>()((set, get) => ({
  bookId: null,
  page: 1,
  pageCount: 0,
  mode: DEFAULT_MODE,
  dir: DEFAULT_DIRECTION,
  fit: DEFAULT_FIT,
  chromeVisible: true,
  hintVisible: false,
  staleVisible: false,
  staleSeen: false,
  staleBookId: null,
  stripOpen: false,
  dragging: false,
  dragPage: null,
  loading: false,

  /**
   * **Opens chromeless (E-27).** design.md principle 2 — while reading there is
   * no UI — and the old behaviour contradicted it at the one moment it matters
   * most: the first frame of a book was three rows of controls over the page.
   * `hintVisible` is the compensation, not an afterthought.
   *
   * ## …but only when the viewer is *entered*, not on every volume
   *
   * 다음 권 읽기 routes to another book with this same screen mounted, so it
   * arrives here as a second `open()`. Treating that as an entry replayed the
   * whole opening ceremony on every volume: chrome the reader had deliberately
   * raised was taken back down, the thumbnail strip closed, and the "where did
   * the controls go" line — which exists to be read *once* — came back for
   * another 3.4 s. The prototype's `goNextVol` changes the volume and the page
   * and nothing else, which is the behaviour a continuation should have.
   *
   * A continuation is an `open()` that lands on a *different* book while one is
   * already open. `close()` nulls `bookId`, so leaving the viewer and coming
   * back is an entry again.
   *
   * ## …but the changed-file notice is armed per **book**, not per entry
   *
   * **E-45 §1 REVISION.** The first cut reused `continuing` for `options.stale`
   * too, on the reasoning that one word should not have two criteria. The two
   * notices ask different questions, so it cost two defects at once. The hint
   * asks *"have I already greeted this reader"* — a continuation is the same
   * reader, so it carries. The warning asks *"has this file changed"*, and the
   * next volume **is a different file**: carrying it meant volume 1's live timer
   * latched under volume 2 and the screen signed it with volume 2's id, burning
   * a baseline over a warning that reader never saw; and, the other way round, a
   * volume 2 that really had changed was never announced at all.
   *
   * So the stale timer is dropped and re-armed on every `open()`, whatever
   * `continuing` says, and `staleBookId` records which book it was armed for.
   * The house pattern for "once per book" is `ViewerPage.tsx`'s `openedRef` —
   * a *value* latch on the last id opened, not a boolean — and that is the shape
   * this follows.
   *
   * **Nothing is armed for a book with no pages (E-45 §2).** `isStale` is
   * symmetric — neither a recorded nor a current `0` is "the file changed" — so
   * a well-behaved server cannot send `stale:true` here with `pageCount: 0`.
   * This is the screen's half of that contract rather than a reliance on it: a
   * book with no pages is already showing 열 수 없는 파일, the reader has no
   * saved place to be moved, and `progressReady` would refuse the
   * acknowledgement anyway — a notice nobody can answer would sit there for its
   * whole life and re-appear on every entry, for ever.
   */
  open: (bookId, options) => {
    const previous = get().bookId
    const continuing = previous !== null && previous !== bookId
    clearAutoHide()
    if (!continuing) clearHint()
    clearStale()
    chromeHeld = false
    const stale = options.stale === true && options.pageCount > 0
    set((s) => ({
      bookId,
      pageCount: options.pageCount,
      page: clampPage(options.page ?? 1, options.pageCount),
      mode: options.mode ?? DEFAULT_MODE,
      dir: options.dir ?? DEFAULT_DIRECTION,
      fit: openingFit(options.fit),
      chromeVisible: continuing ? s.chromeVisible : false,
      hintVisible: continuing ? s.hintVisible : true,
      staleVisible: stale,
      staleSeen: false,
      staleBookId: stale ? bookId : null,
      stripOpen: continuing ? s.stripOpen : false,
      dragging: false,
      dragPage: null,
      loading: false,
    }))
    if (stale) {
      staleTimer = setTimeout(() => {
        staleTimer = null
        // Both halves in one commit: the notice has served its whole life, which
        // is precisely the fact the acknowledgement `PUT` reports (E-45 §2).
        useViewerStore.setState({ staleVisible: false, staleSeen: true })
      }, STALE_NOTICE_MS)
    }
    if (continuing) {
      // The chrome carried over; it still has to go away on its own again.
      if (get().chromeVisible) armAutoHide()
      return
    }
    hintTimer = setTimeout(() => {
      hintTimer = null
      useViewerStore.setState({ hintVisible: false })
    }, CHROME_HINT_MS)
  },

  close: () => {
    clearAutoHide()
    clearHint()
    // Leaving before the notice ran its course is *not* an acknowledgement
    // (E-45 §2): the latch never sets, no `stale_seen` goes out, and the
    // baseline survives to warn again on the next entry.
    clearStale()
    chromeHeld = false
    set({
      bookId: null,
      page: 1,
      pageCount: 0,
      chromeVisible: true,
      hintVisible: false,
      staleVisible: false,
      staleSeen: false,
      staleBookId: null,
      stripOpen: false,
      dragging: false,
      dragPage: null,
      loading: false,
    })
  },

  setPageCount: (n) => {
    const pageCount = Math.max(0, Math.trunc(n))
    set((s) => ({ pageCount, page: clampPage(s.page, pageCount) }))
  },

  goTo: (page) => {
    set((s) => {
      const next = clampPage(page, s.pageCount)
      return next === s.page ? {} : { page: next, loading: true }
    })
    get().wake()
  },

  syncPage: (page) => {
    set((s) => {
      const next = clampPage(page, s.pageCount)
      return next === s.page ? {} : { page: next }
    })
  },

  /**
   * **No wake (E-27).** Turning a page is the act of reading, and the model the
   * design settled on is that reading never summons the interface — only the
   * screen edges, the centre tap and `H` do. That one missing line is the whole
   * difference from `goTo`, which is the *control* path — the slider and the
   * thumbnail strip, where the bar must not vanish under the press.
   *
   * It does set `loading`, and that is the difference from `syncPage`: a turn
   * replaces what is on the stage, so the indicator is owed. A webtoon scroll
   * moves through pages that are already painted.
   *
   * **Absolute, and it has to stay absolute.** This was `step(delta)` and
   * computed its own stride (`mode === 'spread' ? 2 : 1`), which meant no screen
   * could call it: FR-VWR-004 lets a landscape scan take a spread slot alone, so
   * the stride belongs to `fit.ts`, not here. `ViewerPage` therefore routed its
   * turns through `goTo` instead and every arrow key woke the chrome — while a
   * store test on the uncallable `step` reported E-27 as honoured. Give this a
   * signature the screen cannot use and the defect comes straight back
   * (HANDOFF §6.5).
   */
  turnTo: (page) => {
    set((s) => {
      const next = clampPage(page, s.pageCount)
      return next === s.page ? {} : { page: next, loading: true }
    })
  },

  setMode: (mode) => {
    set({ mode })
    get().wake()
  },
  setDirection: (dir) => {
    set({ dir })
    get().wake()
  },
  setFit: (fit) => {
    set({ fit })
    get().wake()
  },

  wake: () => {
    if (!get().chromeVisible) set({ chromeVisible: true })
    armAutoHide()
  },

  holdChrome: () => {
    if (chromeHeld) return
    chromeHeld = true
    clearAutoHide()
  },

  releaseChrome: () => {
    // Not held is not a state to leave: re-arming here would push the deadline
    // back on every mouse move over the page, which is a chrome that outstays
    // its 2 600 ms for as long as the reader's hand is on the mouse.
    if (!chromeHeld) return
    chromeHeld = false
    armAutoHide()
  },

  hideChrome: () => {
    clearAutoHide()
    chromeHeld = false
    set({ chromeVisible: false })
  },

  toggleChrome: () => {
    // A deliberate toggle also answers the hint, so it does not come back when
    // the chrome goes down again inside the hint's window.
    get().dismissHint()
    if (get().chromeVisible) {
      get().hideChrome()
    } else {
      get().wake()
    }
  },

  dismissHint: () => {
    clearHint()
    if (get().hintVisible) set({ hintVisible: false })
  },

  dismissStale: () => {
    clearStale()
    const s = get()
    if (s.staleVisible || s.staleSeen || s.staleBookId !== null) {
      set({ staleVisible: false, staleSeen: false, staleBookId: null })
    }
  },

  toggleStrip: () => {
    set((s) => ({ stripOpen: !s.stripOpen }))
    get().wake()
  },

  setStripOpen: (open) => {
    set({ stripOpen: open })
    get().wake()
  },

  setDragging: (dragging, page) => {
    set((s) => ({
      dragging,
      dragPage: dragging ? clampPage(page ?? s.page, s.pageCount) : null,
    }))
    get().wake()
  },

  setLoading: (loading) => {
    set({ loading })
  },
}))

/**
 * True when the next-volume card should be raised (ui-spec §6.5).
 *
 * A selector rather than stored state so it cannot drift out of sync with the
 * page. Vertical (webtoon) mode never raises it — scrolling past the end is the
 * end of the volume there.
 */
export function selectAtVolumeEnd(s: ViewerState): boolean {
  return s.bookId !== null && s.pageCount > 0 && s.page >= s.pageCount && s.mode !== 'vertical'
}

/**
 * The acknowledgement `PUT` is owed **for `bookId`** (**E-45 §1 REVISION**).
 *
 * The one place the invariant is stated, and it is stated as a question the
 * caller must ask *about a book*: `staleSeen` alone answers "a notice ran its
 * course" and cannot tell whose. The screen's writer is bound to the route's
 * `:bid`, which runs ahead of this store for the length of a volume change —
 * the window in which volume 1's latch used to be signed as volume 2's, and
 * `stale_seen: true` went out against a book whose reader had been shown
 * nothing. Both halves are here so a test can pin the rule without a DOM, and
 * so the screen has nothing left to get wrong but which id it passes.
 *
 * Deliberately **not** compared against `s.bookId`: that is the store's idea of
 * the current book, which is the *other* side of the same lag. The question is
 * whether the acknowledgement about to be written matches the book it was armed
 * for, so the caller supplies the id it is about to write to.
 */
export function selectStaleAckOwed(s: ViewerState, bookId: string): boolean {
  return s.staleSeen && s.staleBookId !== null && s.staleBookId === bookId
}

/**
 * Test seam: drops the pending auto-hide, hint and stale-notice timers without
 * touching state.
 */
export function cancelChromeAutoHide(): void {
  clearAutoHide()
  clearHint()
  clearStale()
  chromeHeld = false
}
