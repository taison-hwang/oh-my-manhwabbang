/**
 * A boolean that only turns on once it has stayed on for `delayMs`.
 *
 * ui-spec §6.3: the loading spinner "appears when a transition takes longer
 * than ~240 ms; below that, don't show it at all". A page served warm from the
 * prefetch cache resolves in single-digit milliseconds, and flashing a spinner
 * for one frame on every page turn is worse than showing nothing — it reads as
 * jank rather than as progress. Turning **off** is immediate.
 */

import { useEffect, useState } from 'react'

/** The threshold below which a page transition shows no indicator at all. */
export const LOADING_INDICATOR_DELAY_MS = 240

export function useDelayedFlag(active: boolean, delayMs = LOADING_INDICATOR_DELAY_MS): boolean {
  const [visible, setVisible] = useState(false)

  useEffect(() => {
    if (!active) {
      setVisible(false)
      return undefined
    }
    const timer = setTimeout(() => {
      setVisible(true)
    }, delayMs)
    return () => {
      clearTimeout(timer)
    }
  }, [active, delayMs])

  return visible
}
