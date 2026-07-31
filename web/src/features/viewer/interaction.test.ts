import { describe, expect, it } from 'vitest'

import { MAX_PREFETCH, prefetchPages } from './usePrefetch'
import {
  SIDE_ZONE_RATIO,
  SWIPE_MAX_MS,
  SWIPE_THRESHOLD_PX,
  swipeAction,
  zoneAction,
  zoneAt,
} from './useTouchZones'

/**
 * Tap zones, swipe and prefetch (FR-VWR-006, FR-VWR-011, ui-spec §8.3).
 *
 * All three are direction- or geometry-dependent decisions that look right in
 * either polarity until a reader tries them, which is exactly the kind of rule
 * that has to be pinned rather than reviewed: "left goes back" is true in a
 * novel and false in 만화.
 */

describe('zoneAt', () => {
  it('splits the stage 32 / 36 / 32 — the two page-turn zones are the big ones', () => {
    expect(SIDE_ZONE_RATIO).toBe(0.32)
    expect(zoneAt(10, 1_000)).toBe('left')
    expect(zoneAt(319, 1_000)).toBe('left')
    expect(zoneAt(320, 1_000)).toBe('centre')
    expect(zoneAt(500, 1_000)).toBe('centre')
    expect(zoneAt(679, 1_000)).toBe('centre')
    expect(zoneAt(680, 1_000)).toBe('right')
    expect(zoneAt(990, 1_000)).toBe('right')
  })

  it('is the chrome toggle when there is no stage to measure', () => {
    expect(zoneAt(10, 0)).toBe('centre')
  })
})

describe('zoneAction', () => {
  it('resolves the side zones in reading order, not screen order', () => {
    expect(zoneAction('left', 'ltr')).toBe('prev')
    expect(zoneAction('right', 'ltr')).toBe('next')
    // R→L: the next page is to the left, so the left zone advances the book.
    expect(zoneAction('left', 'rtl')).toBe('next')
    expect(zoneAction('right', 'rtl')).toBe('prev')
  })

  it('always toggles the chrome from the centre', () => {
    expect(zoneAction('centre', 'ltr')).toBe('chrome')
    expect(zoneAction('centre', 'rtl')).toBe('chrome')
  })
})

describe('swipeAction', () => {
  it('turns the page in the direction the swipe reveals', () => {
    expect(swipeAction(-120, 0, 'ltr')).toBe('next')
    expect(swipeAction(120, 0, 'ltr')).toBe('prev')
    expect(swipeAction(-120, 0, 'rtl')).toBe('prev')
    expect(swipeAction(120, 0, 'rtl')).toBe('next')
  })

  it('ignores a drag too short to be a throw', () => {
    expect(swipeAction(SWIPE_THRESHOLD_PX - 1, 0, 'ltr')).toBeNull()
  })

  it('ignores a mostly-vertical drag — that is a scroll attempt', () => {
    expect(swipeAction(-120, 200, 'ltr')).toBeNull()
    // The rule is `|dy| > |dx|`, so a 45° throw still turns the page; anything
    // steeper is someone scrolling a 너비-fitted page.
    expect(swipeAction(-120, 120, 'ltr')).toBe('next')
    expect(swipeAction(-120, 121, 'ltr')).toBeNull()
  })

  it('ignores a finger that rested on the page and drifted', () => {
    expect(swipeAction(-120, 0, 'ltr', SWIPE_MAX_MS)).toBe('next')
    expect(swipeAction(-120, 0, 'ltr', SWIPE_MAX_MS + 1)).toBeNull()
  })

  it('judges an unmeasured drag on distance and angle alone', () => {
    expect(swipeAction(-120, 0, 'ltr', undefined)).toBe('next')
  })
})

describe('prefetchPages', () => {
  it('warms `count` ahead plus the one behind (FR-VWR-006)', () => {
    expect(prefetchPages(12, 214, 4, 1)).toEqual([13, 14, 15, 16, 11])
  })

  it('counts ahead from the far side of a 양면 spread', () => {
    // Showing 12+13, so the next unseen page is 14 — re-requesting 13 is waste.
    expect(prefetchPages(12, 214, 3, 2)).toEqual([14, 15, 16, 11])
  })

  it('has nothing behind it on page 1 and nothing ahead on the last page', () => {
    expect(prefetchPages(1, 214, 2, 1)).toEqual([2, 3])
    expect(prefetchPages(214, 214, 4, 1)).toEqual([213])
  })

  it('honours a prefetch setting of 0 without dropping the page behind', () => {
    expect(prefetchPages(12, 214, 0, 1)).toEqual([11])
  })

  it('caps at the documented ceiling and rejects a nonsense page', () => {
    expect(prefetchPages(1, 1_000, 999, 1)).toHaveLength(MAX_PREFETCH)
    expect(prefetchPages(0, 214, 4, 1)).toEqual([])
    expect(prefetchPages(12, 0, 4, 1)).toEqual([])
  })
})
