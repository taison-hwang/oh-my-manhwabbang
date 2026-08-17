import { useCallback, useSyncExternalStore } from 'react'

/**
 * Breakpoint hooks for the ui-spec §7 responsive layer.
 *
 * The layout itself is driven from CSS (`--sidebar-w`, `--grid-min` in
 * tokens.css) — these hooks exist only for the cases where the *structure*
 * changes rather than the geometry: the sidebar becoming an off-canvas drawer
 * below 768, and the icon rail dropping its labels between 768 and 1023. Reach
 * for CSS first; a React re-render on resize is the expensive way to do this.
 */

/** The four tiers of ui-spec §7, in CSS px. */
export const BREAKPOINTS = {
  /** Off-canvas drawer, 2-column grid. */
  mobile: 768,
  /** 56px icon rail, 3-column grid. */
  tablet: 1024,
  /** Fixed 240px sidebar, 4–5 columns. */
  desktop: 1440,
} as const

export type Breakpoint = 'mobile' | 'tablet' | 'laptop' | 'desktop'

/**
 * Subscribes to a media query.
 *
 * `useSyncExternalStore` rather than `useState` + `useEffect` so the first
 * render already has the right answer and there is no flash of the wrong
 * layout. The server snapshot is `false`, which is the conservative choice: it
 * makes every `min-width` query read as "narrow" during hydration.
 */
export function useMediaQuery(query: string): boolean {
  const subscribe = useCallback(
    (onChange: () => void) => {
      if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') {
        return () => undefined
      }
      const mql = window.matchMedia(query)
      mql.addEventListener('change', onChange)
      return () => {
        mql.removeEventListener('change', onChange)
      }
    },
    [query],
  )

  const getSnapshot = useCallback((): boolean => {
    if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return false
    return window.matchMedia(query).matches
  }, [query])

  return useSyncExternalStore(subscribe, getSnapshot, () => false)
}

/** True below 768px, where the sidebar is an off-canvas drawer. */
export function useIsMobile(): boolean {
  return !useMediaQuery(`(min-width: ${BREAKPOINTS.mobile.toString()}px)`)
}

/** True in 768–1023, where the sidebar collapses to a 56px icon rail. */
export function useIsRail(): boolean {
  const atLeastTablet = useMediaQuery(`(min-width: ${BREAKPOINTS.mobile.toString()}px)`)
  const atLeastLaptop = useMediaQuery(`(min-width: ${BREAKPOINTS.tablet.toString()}px)`)
  return atLeastTablet && !atLeastLaptop
}

/**
 * The three `min-width` queries the four tiers are cut from, narrowest first.
 *
 * Exported because `useGridBox` (`features/library/useLibrary.ts`) subscribes to
 * these same three queries so that it can read the tier and the grid box's width
 * in one pass. It has to be *this* array rather than a copy: `tokens.css`'s
 * media queries are the source of truth for the geometry — the drift test in
 * `useLibrary.test.ts` pins `GRID_METRICS` against them — so a second set of
 * thresholds would be a second tier rule that nothing compares against the
 * first.
 */
export const TIER_QUERIES = [
  `(min-width: ${BREAKPOINTS.mobile.toString()}px)`,
  `(min-width: ${BREAKPOINTS.tablet.toString()}px)`,
  `(min-width: ${BREAKPOINTS.desktop.toString()}px)`,
] as const

/** The one tier rule: three `min-width` answers in, one tier out. */
export function tierFor(md: boolean, lg: boolean, xl: boolean): Breakpoint {
  if (xl) return 'desktop'
  if (lg) return 'laptop'
  if (md) return 'tablet'
  return 'mobile'
}

/**
 * The tier, read synchronously and outside React.
 *
 * `useBreakpoint` is the hook you want almost everywhere. This exists for the
 * one caller that must read the tier from inside a listener — `useGridBox` —
 * where going through `useSyncExternalStore` would put the answer in a *later*
 * commit than the width it has to agree with.
 *
 * The fallback is `mobile`, which is `tierFor(false, false, false)` and so is
 * the same answer `useMediaQuery`'s `false` server snapshot produces. The two
 * cannot disagree about a environment without `matchMedia`.
 */
export function readBreakpoint(): Breakpoint {
  if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return 'mobile'
  return tierFor(
    window.matchMedia(TIER_QUERIES[0]).matches,
    window.matchMedia(TIER_QUERIES[1]).matches,
    window.matchMedia(TIER_QUERIES[2]).matches,
  )
}

/** The current tier, for the rare case that needs to branch on all four. */
export function useBreakpoint(): Breakpoint {
  return tierFor(
    useMediaQuery(TIER_QUERIES[0]),
    useMediaQuery(TIER_QUERIES[1]),
    useMediaQuery(TIER_QUERIES[2]),
  )
}
