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

/** The current tier, for the rare case that needs to branch on all four. */
export function useBreakpoint(): Breakpoint {
  const md = useMediaQuery(`(min-width: ${BREAKPOINTS.mobile.toString()}px)`)
  const lg = useMediaQuery(`(min-width: ${BREAKPOINTS.tablet.toString()}px)`)
  const xl = useMediaQuery(`(min-width: ${BREAKPOINTS.desktop.toString()}px)`)
  if (xl) return 'desktop'
  if (lg) return 'laptop'
  if (md) return 'tablet'
  return 'mobile'
}
