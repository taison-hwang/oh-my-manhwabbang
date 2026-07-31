import { useEffect } from 'react'

import { useUiStore } from '../store/ui'
import { useViewerStore } from '../store/viewer'

/**
 * The global key dispatcher (ui-spec §8.1).
 *
 *   Ctrl/Cmd + K   toggle the command palette, clearing its query.
 *                  `preventDefault()`. Works from **anywhere**, including from
 *                  inside a text field and from inside the viewer.
 *   Esc            close the topmost overlay if one is open; **else** close the
 *                  viewer. That "else" is the whole ladder.
 *   ?              open the shortcuts dialog.
 *
 * Viewer-only keys (`←`/`→`/`Space`/`T`/`F`/`1`/`2`/`3`, ui-spec §8.2) are the
 * viewer's own concern and are not bound here — they must not fire while the
 * library has focus.
 */

/** True for elements where a bare printable key means "type", not "command". */
export function isTypingTarget(target: EventTarget | null): boolean {
  if (target === null || !(target instanceof HTMLElement)) return false
  // `isContentEditable` is the right API but jsdom does not implement it, so
  // the attribute is checked too — which also covers `contenteditable=""`.
  if (target.isContentEditable) return true
  const editable = target.getAttribute('contenteditable')
  if (editable === '' || editable === 'true' || editable === 'plaintext-only') return true
  const tag = target.tagName
  return tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT'
}

export interface GlobalHotkeyOptions {
  /**
   * Called for `Esc` when no overlay is open and the viewer is on screen.
   *
   * The viewer is a route (`/series/:sid/books/:bid`), so leaving it is a
   * navigation the shell owns; the store alone cannot do it.
   */
  onExitViewer?: () => void
  /** Set false to unbind entirely (e.g. behind the login screen). */
  enabled?: boolean
}

export function useGlobalHotkeys(options: GlobalHotkeyOptions = {}): void {
  const { onExitViewer, enabled = true } = options

  useEffect(() => {
    if (!enabled) return undefined

    const onKeyDown = (e: KeyboardEvent): void => {
      // ---- Ctrl/Cmd + K --------------------------------------------------
      if ((e.metaKey || e.ctrlKey) && !e.altKey && e.key.toLowerCase() === 'k') {
        e.preventDefault()
        const ui = useUiStore.getState()
        ui.setPaletteQuery('')
        ui.toggleOverlay('palette')
        return
      }

      if (e.metaKey || e.ctrlKey || e.altKey) return

      // ---- Esc ladder ----------------------------------------------------
      if (e.key === 'Escape') {
        const closed = useUiStore.getState().closeTopOverlay()
        if (closed !== null) {
          e.preventDefault()
          return
        }
        if (useViewerStore.getState().bookId !== null) {
          e.preventDefault()
          useViewerStore.getState().close()
          onExitViewer?.()
        }
        return
      }

      // ---- ? -------------------------------------------------------------
      // A bare printable key: suppressed while the user is typing, otherwise
      // the search field could never contain a question mark.
      if (e.key === '?' && !isTypingTarget(e.target)) {
        e.preventDefault()
        useUiStore.getState().openOverlay('shortcuts')
      }
    }

    window.addEventListener('keydown', onKeyDown)
    return () => {
      window.removeEventListener('keydown', onKeyDown)
    }
  }, [enabled, onExitViewer])
}
