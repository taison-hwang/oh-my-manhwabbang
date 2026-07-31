/**
 * Real browser fullscreen (FR-VWR-007).
 *
 * The prototype stubs `F` to a chrome wake; the requirement is an actual
 * `requestFullscreen`/`exitFullscreen`, so it lives here rather than in the
 * viewer, where it would be easy to leave stubbed a second time.
 */

/** True when any element is currently fullscreen. */
export function isFullscreen(): boolean {
  if (typeof document === 'undefined') return false
  return document.fullscreenElement !== null
}

/**
 * Toggles fullscreen on `target` (defaults to the document element).
 *
 * Resolves to the resulting state. A rejected request — the API is
 * user-gesture-gated and some embeddings disallow it entirely — resolves to the
 * unchanged state rather than throwing: failing to go fullscreen must not take
 * a keystroke handler down with it.
 */
export async function toggleFullscreen(target?: Element): Promise<boolean> {
  if (typeof document === 'undefined') return false
  try {
    if (document.fullscreenElement !== null) {
      await document.exitFullscreen()
      return false
    }
    const el = target ?? document.documentElement
    if (typeof el.requestFullscreen !== 'function') return false
    await el.requestFullscreen()
    return true
  } catch {
    return isFullscreen()
  }
}
