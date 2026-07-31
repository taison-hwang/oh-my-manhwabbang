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

/** Default fit is `height`, per C-13 — the prototype capture, not the guess. */
export const DEFAULT_FIT: FitMode = 'height'

/**
 * The fit a book is *opened* at (**E-27**).
 *
 * `contain` is still a legal wire value and still round-trips through
 * `PUT /api/books/{id}/prefs` — arch §7 is unchanged, and a `user.db` written
 * before the amendment must keep loading. What it no longer has is a control,
 * so a book stored at `contain` opens at 높이 instead: a reader parked on a fit
 * with no button can otherwise neither see which one they are on nor leave it.
 */
export function openingFit(fit: FitMode | undefined): FitMode {
  if (fit === undefined) return DEFAULT_FIT
  return fit === 'contain' ? 'height' : fit
}
export const DEFAULT_MODE: DisplayMode = 'single'
export const DEFAULT_DIRECTION: ReadingDirection = 'ltr'

export interface ViewerOpenOptions {
  pageCount: number
  page?: number
  mode?: DisplayMode
  dir?: ReadingDirection
  fit?: FitMode
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
  stripOpen: boolean
  dragging: boolean
  /** The page under the slider thumb while dragging; committed on release. */
  dragPage: number | null
  loading: boolean

  open: (bookId: string, options: ViewerOpenOptions) => void
  close: () => void
  setPageCount: (n: number) => void
  goTo: (page: number) => void
  /**
   * The stage reporting which page the reader has scrolled to (세로 mode).
   *
   * Separate from `goTo` for exactly one reason: it must not wake the chrome.
   * Scrolling a webtoon is reading, not operating a control, and routing it
   * through `goTo` made the bars flash on every wheel tick.
   */
  syncPage: (page: number) => void
  /** Page turn. The increment is **2 in spread mode**, 1 otherwise. */
  step: (delta: number) => void
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
   */
  holdChrome: () => void
  releaseChrome: () => void
  hideChrome: () => void
  toggleChrome: () => void
  dismissHint: () => void
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

/**
 * True while the pointer rests inside the chrome.
 *
 * Module-scoped for the same reason as the timer: it is not state — nothing
 * renders from it, and it only ever decides whether an already-running wake
 * arms a timer.
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
   */
  open: (bookId, options) => {
    const previous = get().bookId
    const continuing = previous !== null && previous !== bookId
    clearAutoHide()
    if (!continuing) clearHint()
    chromeHeld = false
    set((s) => ({
      bookId,
      pageCount: options.pageCount,
      page: clampPage(options.page ?? 1, options.pageCount),
      mode: options.mode ?? DEFAULT_MODE,
      dir: options.dir ?? DEFAULT_DIRECTION,
      fit: openingFit(options.fit),
      chromeVisible: continuing ? s.chromeVisible : false,
      hintVisible: continuing ? s.hintVisible : true,
      stripOpen: continuing ? s.stripOpen : false,
      dragging: false,
      dragPage: null,
      loading: false,
    }))
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
    chromeHeld = false
    set({
      bookId: null,
      page: 1,
      pageCount: 0,
      chromeVisible: true,
      hintVisible: false,
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
   * screen edges, the centre tap and `H` do.
   */
  step: (delta) => {
    const { mode, page, pageCount } = get()
    const stride = mode === 'spread' ? 2 : 1
    const next = clampPage(page + delta * stride, pageCount)
    if (next !== page) set({ page: next, loading: true })
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
    chromeHeld = true
    clearAutoHide()
  },

  releaseChrome: () => {
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

/** Test seam: drops the pending auto-hide and hint timers without touching state. */
export function cancelChromeAutoHide(): void {
  clearAutoHide()
  clearHint()
  chromeHeld = false
}
