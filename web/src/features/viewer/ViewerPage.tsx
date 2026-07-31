import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type MouseEvent as ReactMouseEvent,
} from 'react'
import { useNavigate, useParams, useSearchParams } from 'react-router-dom'

import { useBook, useSeries, useSetPrefs, useSettings } from '../../api/queries'
import type { BookSummary } from '../../api/types'
import { EmptyState } from '../../components/ds/EmptyState'
import { formatViewerCounter } from '../../lib/format'
import { toggleFullscreen } from '../../lib/fullscreen'
import { DARK_MEDIA_QUERY, resolveTheme } from '../../lib/theme'
import { useMediaQuery } from '../../lib/useMediaQuery'
import { useUiStore } from '../../store/ui'
import {
  useViewerStore,
  type DisplayMode,
  type FitMode,
  type ReadingDirection,
} from '../../store/viewer'
import { NextVolumeCard } from './NextVolumeCard'
import { PageLoadingIndicator } from './PageLoadingIndicator'
import { PageStage } from './PageStage'
import { ViewerBottomBar } from './ViewerBottomBar'
import { ViewerTopBar } from './ViewerTopBar'
import { makeDimsLookup, nextPage, prevPage, stagePages, type PageDims } from './fit'
import { useDelayedFlag } from './useDelayedFlag'
import { DEFAULT_PREFETCH, usePrefetch } from './usePrefetch'
import { useProgressSync } from './useProgressSync'
import { useTouchZones } from './useTouchZones'
import { useViewerKeys } from './useViewerKeys'

/**
 * Screen 3 — the viewer (prd UI-003, FR-VWR-001..012, ui-spec §6).
 *
 * Route element for `/series/:sid/books/:bid`, and the one place the fifteen
 * pieces of this package are actually wired together. Everything below is a
 * composition decision the parts cannot make for themselves:
 *
 *  * **The dark wrapper.** `<div data-theme="dark">` re-scopes the whole token
 *    layer, so the viewer is the same near-black ground in both app themes
 *    (NFR-CMP-003) without a single hardcoded colour. `--color-bg` inside it is
 *    already the near-black reading ground, which is why the components use
 *    `bg-bg`/`text-ink` and not the ui-spec's `background: var(--color-text)` —
 *    that inversion is the prototype's way of saying the same thing from
 *    *outside* a themed scope.
 *    The next-volume card deliberately steps back out to the app theme (§6.5),
 *    which is why `appTheme` is threaded into it as a prop.
 *  * **`cursor: none`.** design.md principle 2: while reading there is no UI,
 *    and the pointer is UI. Ruling **E-27** separated it from the chrome: the
 *    cursor answers to the pointer's own idleness, the chrome answers only to
 *    the screen edges, the centre tap and `H`. Before E-27 they were one thing,
 *    which meant the smallest nudge of the mouse raised three rows of controls
 *    over the page — the state the ruling exists to remove.
 *  * **Where each page number comes from.** The store owns the current page;
 *    the *opening* page is the route's `?page=` (set by the series screen) or
 *    the server's `progress.last_page` (FR-VWR-009 resume). The guard is a ref
 *    on the book id, not a dependency list — `useSetPrefs` writes the fresh
 *    prefs into the book cache, and a re-run would throw the reader back to
 *    where they resumed on every 양면/화면 press.
 *  * **Which page turn.** `nextPage`/`prevPage`, never the store's `step`: the
 *    stride has to be however many pages are *actually* on screen, so a
 *    landscape scan (FR-VWR-004) does not put the book one page out of phase.
 *  * **What "loading" means.** A page is pending until its `<img>` has fired
 *    `load` or `error`. That, delayed by 240 ms, is the only thing that shows
 *    the indicator — the stage itself is never blanked (ui-spec §6.3).
 */

/** What `PageFrame` has told us about a page: decoded size, and that it settled. */
interface DecodeState {
  /** `bookId:cv` — a new content version invalidates every measurement. */
  key: string
  measured: Map<number, PageDims>
  /** Pages whose image has loaded *or* failed; either way it is no longer pending. */
  settled: Set<number>
}

function emptyDecodeState(key: string): DecodeState {
  return { key, measured: new Map(), settled: new Set() }
}

/** The cursor goes this long after the pointer stops moving (E-27). */
export const POINTER_IDLE_MS = 1600

/**
 * How deep the screen-edge strips reach (E-27).
 *
 * 44px is the tap target the responsive layer uses everywhere else, and it is
 * also roughly the height of the bar each strip summons — so the gesture is
 * "reach for where the bar will be", not "find an invisible line".
 */
const EDGE_STRIP_PX = 44

/** The one sentence that explains a viewer which opens with nothing on it. */
const CHROME_HINT = '좌·우 클릭으로 페이지 넘김 · 가운데 클릭이나 화면 위·아래 가장자리로 컨트롤'

export function ViewerPage() {
  const { sid, bid } = useParams()
  const seriesId = sid ?? ''
  const bookId = bid ?? ''
  const navigate = useNavigate()
  const [searchParams] = useSearchParams()

  const rootRef = useRef<HTMLDivElement | null>(null)

  const book = useBook(bookId, { enabled: bookId !== '' })
  const settings = useSettings()
  // Only for the next-volume card's title/meta; the series detail is almost
  // always already in the cache because that is the screen the reader came from.
  const series = useSeries(seriesId, { enabled: seriesId !== '' })
  const setPrefs = useSetPrefs(bookId)

  const themeSetting = useUiStore((s) => s.theme)
  const systemDark = useMediaQuery(DARK_MEDIA_QUERY)
  const appTheme = resolveTheme(themeSetting, systemDark)

  const page = useViewerStore((s) => s.page)
  const pageCount = useViewerStore((s) => s.pageCount)
  const mode = useViewerStore((s) => s.mode)
  const dir = useViewerStore((s) => s.dir)
  const fit = useViewerStore((s) => s.fit)
  const chromeVisible = useViewerStore((s) => s.chromeVisible)
  const stripOpen = useViewerStore((s) => s.stripOpen)
  const dragging = useViewerStore((s) => s.dragging)
  const dragPage = useViewerStore((s) => s.dragPage)
  const open = useViewerStore((s) => s.open)
  const goTo = useViewerStore((s) => s.goTo)
  const setMode = useViewerStore((s) => s.setMode)
  const setDirection = useViewerStore((s) => s.setDirection)
  const setFit = useViewerStore((s) => s.setFit)
  const setDragging = useViewerStore((s) => s.setDragging)
  const setLoading = useViewerStore((s) => s.setLoading)
  const toggleChrome = useViewerStore((s) => s.toggleChrome)
  const toggleStrip = useViewerStore((s) => s.toggleStrip)
  const wake = useViewerStore((s) => s.wake)
  const syncPage = useViewerStore((s) => s.syncPage)
  const hintVisible = useViewerStore((s) => s.hintVisible)

  const detail = book.data
  const cv = detail?.cv ?? null
  const pages = useMemo(() => detail?.pages ?? [], [detail])
  const decodeKey = `${bookId}:${cv ?? ''}`

  const [decoded, setDecoded] = useState<DecodeState>(() => emptyDecodeState(decodeKey))
  // Derived during render rather than in an effect: an effect would let one
  // frame paint with the previous book's measurements still in force.
  if (decoded.key !== decodeKey) setDecoded(emptyDecodeState(decodeKey))

  const dims = useMemo(() => makeDimsLookup(pages, decoded.measured), [pages, decoded.measured])
  const shown = useMemo(
    () => stagePages(page, pageCount, mode, dims),
    [dims, mode, page, pageCount],
  )

  // 세로 scrolls through the whole book, so only the page the reader is on can
  // meaningfully be "the page being waited for".
  const watched = mode === 'vertical' ? [page] : shown
  const pending = pageCount > 0 && watched.some((n) => !decoded.settled.has(n))
  const showIndicator = useDelayedFlag(pending)

  useEffect(() => {
    setLoading(pending)
  }, [pending, setLoading])

  const onPageLoaded = useCallback((n: number, width: number, height: number) => {
    setDecoded((previous) => {
      const measured = new Map(previous.measured)
      measured.set(n, { w: width, h: height })
      const settled = new Set(previous.settled)
      settled.add(n)
      return { ...previous, measured, settled }
    })
  }, [])

  const onPageFailed = useCallback((n: number) => {
    setDecoded((previous) => {
      const settled = new Set(previous.settled)
      settled.add(n)
      return { ...previous, settled }
    })
  }, [])

  // -------------------------------------------------------------------------
  // Opening and closing
  // -------------------------------------------------------------------------

  const requestedPage = Number(searchParams.get('page'))
  const openedRef = useRef<string | null>(null)

  useEffect(() => {
    if (detail === undefined || bookId === '') return
    if (openedRef.current === bookId) return
    openedRef.current = bookId
    const resume =
      Number.isFinite(requestedPage) && requestedPage >= 1
        ? Math.trunc(requestedPage)
        : (detail.progress?.last_page ?? 1)
    open(bookId, {
      pageCount: detail.page_count,
      page: resume,
      mode: detail.prefs.display_mode,
      dir: detail.prefs.reading_direction,
      fit: detail.prefs.fit_mode,
    })
  }, [bookId, detail, open, requestedPage])

  // Leaving the screen must not leave a viewer open in the store — the global
  // `Esc` ladder and the shell both key off `bookId !== null`.
  useEffect(
    () => () => {
      useViewerStore.getState().close()
    },
    [],
  )

  const exit = useCallback(() => {
    useViewerStore.getState().close()
    void navigate(seriesId === '' ? '/' : `/series/${seriesId}`)
  }, [navigate, seriesId])

  // -------------------------------------------------------------------------
  // Progress (FR-VWR-009 / FR-VWR-012)
  // -------------------------------------------------------------------------

  const progressReady = detail !== undefined && pageCount > 0
  const progress = useProgressSync(bookId, page, pageCount, { enabled: progressReady })

  // -------------------------------------------------------------------------
  // Per-book preferences (FR-VWR-002 / D-35)
  // -------------------------------------------------------------------------

  const { mutate: savePrefs } = setPrefs
  const applyMode = useCallback(
    (next: DisplayMode) => {
      setMode(next)
      if (detail !== undefined) savePrefs({ display_mode: next })
    },
    [detail, savePrefs, setMode],
  )
  const applyDir = useCallback(
    (next: ReadingDirection) => {
      setDirection(next)
      if (detail !== undefined) savePrefs({ reading_direction: next })
    },
    [detail, savePrefs, setDirection],
  )
  const applyFit = useCallback(
    (next: FitMode) => {
      setFit(next)
      if (detail !== undefined) savePrefs({ fit_mode: next })
    },
    [detail, savePrefs, setFit],
  )

  // -------------------------------------------------------------------------
  // Page turns, keys, taps, prefetch
  // -------------------------------------------------------------------------

  const goNext = useCallback(() => {
    goTo(nextPage(page, pageCount, mode, dims))
  }, [dims, goTo, mode, page, pageCount])

  const goPrev = useCallback(() => {
    goTo(prevPage(page, pageCount, mode, dims))
  }, [dims, goTo, mode, page, pageCount])

  const onToggleFullscreen = useCallback(() => {
    void toggleFullscreen(rootRef.current ?? undefined)
  }, [])

  useViewerKeys({
    dir,
    onNext: goNext,
    onPrev: goPrev,
    onToggleStrip: toggleStrip,
    onToggleFullscreen,
    onToggleChrome: toggleChrome,
    onExit: exit,
    onSetMode: applyMode,
    enabled: detail !== undefined,
  })

  // -------------------------------------------------------------------------
  // The pointer (E-27)
  // -------------------------------------------------------------------------
  //
  // The cursor used to be tied to `chromeVisible`, on the reading that the
  // pointer is part of the UI and goes with it. E-27 splits them, because after
  // it the chrome is no longer summoned by moving the mouse: leaving the two
  // joined would mean a reader who nudges the mouse gets a pointer *and* three
  // rows of controls, which is exactly what the ruling set out to stop. So the
  // pointer now answers to the pointer — visible while it moves, gone 1 600 ms
  // after it stops — and the chrome answers to the screen edges, the centre tap
  // and `H`.

  const [pointerAwake, setPointerAwake] = useState(true)
  const pointerTimer = useRef<ReturnType<typeof setTimeout> | null>(null)

  const nudgePointer = useCallback(() => {
    setPointerAwake(true)
    if (pointerTimer.current !== null) clearTimeout(pointerTimer.current)
    pointerTimer.current = setTimeout(() => {
      pointerTimer.current = null
      setPointerAwake(false)
    }, POINTER_IDLE_MS)
  }, [])

  useEffect(
    () => () => {
      if (pointerTimer.current !== null) clearTimeout(pointerTimer.current)
    },
    [],
  )

  // A click on the top or bottom strip both wakes the chrome and must not reach
  // the tap zones underneath, where the same click is a page turn.
  const wakeFromEdge = useCallback(
    (event: ReactMouseEvent) => {
      event.stopPropagation()
      wake()
    },
    [wake],
  )

  const touch = useTouchZones({
    dir,
    mode,
    onNext: goNext,
    onPrev: goPrev,
    onToggleChrome: toggleChrome,
    enabled: detail !== undefined,
  })

  usePrefetch({
    bookId: detail === undefined ? null : bookId,
    cv,
    page,
    pageCount,
    shownCount: Math.max(1, shown.length),
    count: settings.data?.prefetch ?? DEFAULT_PREFETCH,
  })

  // -------------------------------------------------------------------------
  // Render
  // -------------------------------------------------------------------------

  const nextBook: BookSummary | null =
    detail?.next_book_id == null
      ? null
      : (series.data?.books.find((b) => b.id === detail.next_book_id) ?? null)

  const atVolumeEnd =
    detail !== undefined && pageCount > 0 && page >= pageCount && mode !== 'vertical'

  const stale = detail?.progress?.stale === true

  return (
    <div
      ref={rootRef}
      data-theme="dark"
      data-role="viewer"
      data-chrome={chromeVisible ? 'visible' : 'hidden'}
      className="fixed inset-0 z-viewer flex flex-col overflow-hidden bg-bg text-ink"
      style={{ cursor: pointerAwake ? 'default' : 'none' }}
      onMouseMove={nudgePointer}
    >
      {/* The screen edges (E-27). Rendered only while the chrome is away: once
          it is up, the bars themselves are what the pointer rests on, and a
          strip over them would eat the first click on 뒤로. */}
      {!chromeVisible && detail !== undefined && (
        <>
          <div
            data-role="viewer-edge-top"
            className="absolute inset-x-0 top-0 z-[2]"
            style={{ height: EDGE_STRIP_PX }}
            onMouseEnter={wake}
            onClick={wakeFromEdge}
          />
          <div
            data-role="viewer-edge-bottom"
            className="absolute inset-x-0 bottom-0 z-[2]"
            style={{ height: EDGE_STRIP_PX }}
            onMouseEnter={wake}
            onClick={wakeFromEdge}
          />
        </>
      )}
      {book.isError ? (
        <div className="flex min-h-0 flex-1 items-center justify-start p-8">
          <EmptyState
            title="책을 열 수 없습니다"
            body={book.error.message}
            action={{ label: '시리즈로', onClick: exit }}
          />
        </div>
      ) : detail === undefined ? (
        // Deliberately empty: the reading ground is the loading state. Anything
        // else here is a flash on every 다음 권.
        <div className="min-h-0 flex-1" />
      ) : pages.length === 0 ? (
        <div className="flex min-h-0 flex-1 items-center justify-start p-8">
          <EmptyState
            title="열 수 없는 파일"
            {...(detail.error === null ? {} : { body: detail.error })}
            action={{ label: '시리즈로', onClick: exit }}
          />
        </div>
      ) : (
        <div
          data-role="stage-zones"
          className="flex min-h-0 flex-1 flex-col"
          onMouseDown={touch.onMouseDown}
          onMouseUp={touch.onMouseUp}
          onTouchStart={touch.onTouchStart}
          onTouchEnd={touch.onTouchEnd}
        >
          <PageStage
            bookId={bookId}
            cv={cv}
            pages={pages}
            page={page}
            pageCount={pageCount}
            mode={mode}
            fit={fit}
            dir={dir}
            measured={decoded.measured}
            cause={detail.error}
            onPageLoaded={onPageLoaded}
            onPageFailed={onPageFailed}
            // Not `goTo`: a webtoon scroll is reading, and reading does not
            // summon the chrome any more (E-27).
            onScrollToPage={syncPage}
          />
        </div>
      )}

      <PageLoadingIndicator visible={showIndicator} />

      {/* The page number, while there is no bar to hold it. Suppressed in the
          two modes where the number on screen is not the number being read:
          세로 scrolls through several at once, and 너비 leaves the page taller
          than the viewport. */}
      {!chromeVisible && !showIndicator && mode !== 'vertical' && fit !== 'width' && pageCount > 0 && (
        <span
          data-role="quiet-page-counter"
          className="pointer-events-none absolute bottom-[10px] right-[14px] text-xs tabular-nums tracking-[.06em] text-ink-dim"
        >
          {formatViewerCounter(page, pageCount)}
        </span>
      )}

      {hintVisible && !chromeVisible && detail !== undefined && (
        <div
          data-role="viewer-chrome-hint"
          role="status"
          className="pointer-events-none absolute bottom-[22px] left-1/2 -translate-x-1/2 border border-neutral-800 bg-bg px-3 py-[7px] text-center text-xs tracking-[.04em] text-neutral-400"
        >
          {CHROME_HINT}
        </div>
      )}

      <ViewerTopBar
        visible={chromeVisible}
        seriesName={detail?.series_name ?? ''}
        bookName={detail?.name ?? ''}
        mode={mode}
        dir={dir}
        fit={fit}
        onBack={exit}
        onModeChange={applyMode}
        onDirChange={applyDir}
        onFitChange={applyFit}
      />

      {/* FR-VWR-009: the recorded page count no longer matches the file, so the
          resumed page may not be the page it was. One line, never a dialog.

          **Not gated on the chrome** — it used to be, and E-27 would have
          quietly deleted the warning: the viewer now opens chromeless and the
          chrome never appears on its own, so a notice that rides along with it
          is a notice nobody is shown. It is `pointer-events-none` and sits
          under the top bar's height either way. */}
      {stale && (
        <div
          data-role="stale-progress"
          className="pointer-events-none absolute inset-x-0 top-14 flex justify-center"
        >
          <span className="bg-accent px-[7px] py-[3px] text-3xs uppercase tracking-[.1em] text-ink">
            파일이 변경되었습니다
          </span>
        </div>
      )}

      <ViewerBottomBar
        visible={chromeVisible}
        bookId={bookId}
        cv={cv}
        page={page}
        pageCount={pageCount}
        stripOpen={stripOpen}
        dragging={dragging}
        dragPage={dragPage}
        onToggleStrip={toggleStrip}
        onDragStart={(n) => {
          setDragging(true, n)
        }}
        onDrag={(n) => {
          setDragging(true, n)
        }}
        onCommit={(n) => {
          setDragging(false)
          goTo(n)
        }}
        onJump={goTo}
      />

      {atVolumeEnd && (
        <NextVolumeCard
          nextBook={nextBook}
          completed={detail.progress?.completed ?? false}
          appTheme={appTheme}
          onNext={() => {
            if (detail.next_book_id === null) return
            progress.flush()
            void navigate(`/series/${seriesId}/books/${detail.next_book_id}?page=1`)
          }}
          onBackToSeries={exit}
          onToggleCompleted={progress.setCompleted}
        />
      )}
    </div>
  )
}
