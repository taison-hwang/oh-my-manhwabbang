/**
 * Tap zones and swipe (FR-VWR-011, ui-spec §8.3).
 *
 * Left 32 % / right 32 % turn the page **in reading order**, centre 36 %
 * toggles the chrome, and a horizontal swipe turns the page in the direction it
 * was thrown. Under `R→L` the side zones swap, because "left" is a screen
 * position and "previous" is a book position.
 *
 * Mouse *and* touch, not Pointer Events: the spec names `mousedown`/`touchstart`
 * and jsdom's PointerEvent support is not something a page turn should depend
 * on. A touch sequence suppresses the synthesised mouse events that follow it.
 */

import { useCallback, useRef, type MouseEvent, type TouchEvent } from 'react'

import type { DisplayMode, ReadingDirection } from '../../store/viewer'

/**
 * Side zones are 32 % each, so the centre is 36 %.
 *
 * The prototype's own measurement at 1440px: 461 / 518 / 461. The centre is
 * deliberately the *smallest* of the three — it toggles the chrome, and the two
 * that turn pages are the ones a reader aims at a hundred times a volume.
 */
export const SIDE_ZONE_RATIO = 0.32
/** Below this a horizontal drag is a tap, not a swipe. */
export const SWIPE_THRESHOLD_PX = 44
/**
 * A swipe must be more horizontal than vertical.
 *
 * `|dy| > |dx|` — anything steeper is someone scrolling a webtoon or a long
 * 너비-fitted page, and turning the page out from under them is the worst
 * possible answer.
 */
export const SWIPE_MAX_VERTICAL_RATIO = 1
/**
 * A swipe has to be *thrown*. Past this the finger was resting on the page and
 * happened to drift, which is a drag, not a page turn.
 */
export const SWIPE_MAX_MS = 600
/** Synthesised mouse events arrive within ~300 ms of a touch. */
const TOUCH_MOUSE_SUPPRESS_MS = 600

export type StageZone = 'left' | 'centre' | 'right'
export type ZoneAction = 'next' | 'prev' | 'chrome'

/** Which third of the stage a point falls in. */
export function zoneAt(offsetX: number, width: number): StageZone {
  if (width <= 0) return 'centre'
  const ratio = offsetX / width
  if (ratio < SIDE_ZONE_RATIO) return 'left'
  if (ratio > 1 - SIDE_ZONE_RATIO) return 'right'
  return 'centre'
}

/**
 * Reading order, not screen order: under `R→L` the *next* page is to the left,
 * so tapping the left zone advances the book.
 */
export function zoneAction(zone: StageZone, dir: ReadingDirection): ZoneAction {
  if (zone === 'centre') return 'chrome'
  if (zone === 'left') return dir === 'rtl' ? 'next' : 'prev'
  return dir === 'rtl' ? 'prev' : 'next'
}

/**
 * A horizontal throw, resolved through the same rule as the tap zones: a swipe
 * to the left means "go to what is on the right", whichever page that is.
 *
 * `elapsedMs` is how long the finger was down. Omit it for callers that have no
 * clock (the pure zone tests); a swipe with no measured duration is judged on
 * distance and angle alone.
 */
export function swipeAction(
  dx: number,
  dy: number,
  dir: ReadingDirection,
  elapsedMs?: number,
): 'next' | 'prev' | null {
  if (elapsedMs !== undefined && elapsedMs > SWIPE_MAX_MS) return null
  if (Math.abs(dx) < SWIPE_THRESHOLD_PX) return null
  if (Math.abs(dy) > Math.abs(dx) * SWIPE_MAX_VERTICAL_RATIO) return null
  const action = zoneAction(dx < 0 ? 'right' : 'left', dir)
  return action === 'chrome' ? null : action
}

export interface TouchZonesOptions {
  dir: ReadingDirection
  mode: DisplayMode
  onNext: () => void
  onPrev: () => void
  onToggleChrome: () => void
  enabled?: boolean
}

export interface TouchZoneHandlers {
  onMouseDown: (event: MouseEvent<HTMLElement>) => void
  onMouseUp: (event: MouseEvent<HTMLElement>) => void
  onTouchStart: (event: TouchEvent<HTMLElement>) => void
  onTouchEnd: (event: TouchEvent<HTMLElement>) => void
}

interface Point {
  x: number
  y: number
  /** `Date.now()` at pointer-down, so a throw can be told from a rest. */
  t: number
}

export function useTouchZones(options: TouchZonesOptions): TouchZoneHandlers {
  const { dir, mode, onNext, onPrev, onToggleChrome, enabled = true } = options

  const start = useRef<Point | null>(null)
  const lastTouchEndAt = useRef<number>(0)

  const run = useCallback(
    (action: ZoneAction): void => {
      if (action === 'next') onNext()
      else if (action === 'prev') onPrev()
      else onToggleChrome()
    },
    [onNext, onPrev, onToggleChrome],
  )

  const resolve = useCallback(
    (element: HTMLElement, from: Point, to: Point): void => {
      const dx = to.x - from.x
      const dy = to.y - from.y
      // 세로 mode owns horizontal space for nothing and vertical space for the
      // native scroll, so swipes are off there (ui-spec §8.3).
      if (mode !== 'vertical') {
        const swipe = swipeAction(dx, dy, dir, to.t - from.t)
        if (swipe !== null) {
          run(swipe)
          return
        }
      }
      if (Math.abs(dx) >= SWIPE_THRESHOLD_PX || Math.abs(dy) >= SWIPE_THRESHOLD_PX) return
      const rect = element.getBoundingClientRect()
      run(zoneAction(zoneAt(to.x - rect.left, rect.width), dir))
    },
    [dir, mode, run],
  )

  const onMouseDown = useCallback(
    (event: MouseEvent<HTMLElement>) => {
      if (!enabled) return
      if (Date.now() - lastTouchEndAt.current < TOUCH_MOUSE_SUPPRESS_MS) return
      start.current = { x: event.clientX, y: event.clientY, t: Date.now() }
    },
    [enabled],
  )

  const onMouseUp = useCallback(
    (event: MouseEvent<HTMLElement>) => {
      if (!enabled) return
      const from = start.current
      start.current = null
      if (from === null) return
      resolve(event.currentTarget, from, { x: event.clientX, y: event.clientY, t: Date.now() })
    },
    [enabled, resolve],
  )

  const onTouchStart = useCallback(
    (event: TouchEvent<HTMLElement>) => {
      if (!enabled) return
      const touch = event.touches[0]
      if (touch === undefined) return
      start.current = { x: touch.clientX, y: touch.clientY, t: Date.now() }
    },
    [enabled],
  )

  const onTouchEnd = useCallback(
    (event: TouchEvent<HTMLElement>) => {
      if (!enabled) return
      lastTouchEndAt.current = Date.now()
      const from = start.current
      start.current = null
      const touch = event.changedTouches[0]
      if (from === null || touch === undefined) return
      resolve(event.currentTarget, from, { x: touch.clientX, y: touch.clientY, t: Date.now() })
    },
    [enabled, resolve],
  )

  return { onMouseDown, onMouseUp, onTouchStart, onTouchEnd }
}
