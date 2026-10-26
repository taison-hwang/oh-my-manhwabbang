/**
 * The viewer's keyboard map (FR-VWR-007, ui-spec §8.2).
 *
 *   `→`  onNext, or onPrev under R→L        `←`  the inverse
 *   `Space`  onNext, preventDefault         `T`  thumbnail panel
 *   `F`  real browser fullscreen            `Esc`  leave the viewer
 *   `H`  show / hide the chrome             `1` / `2` / `3`  단면 / 양면 / 세로
 *
 * The turn itself is the screen's: this hook never touches the store's page,
 * because the stride is however many pages are on the stage (FR-VWR-004).
 * `ViewerPage` hands in `goNext`/`goPrev`, which commit through the store's
 * `turnTo` — `goTo` minus the wake.
 *
 * `H` arrived with ruling **E-27**, which took the chrome off the mouse: page
 * turns do not wake it and the viewer opens without it, so a keyboard reader
 * who never touches the screen edges needs a key that summons it.
 * **E-62** lets it through from a radio or a range input as well — see
 * `leaveToControl`.
 *
 * These are **viewer-only** keys and are deliberately not in
 * `lib/useHotkeys.ts`: a bare `2` must not switch display mode while the
 * library has focus. `Ctrl/Cmd+K` and `?` stay global and are not touched here.
 *
 * Two collaborators have to be respected:
 *
 *  * **The overlay ladder.** The command palette, settings and shortcuts
 *    dialogs sit above the viewer. While any of them is open every key here is
 *    inert, so `Esc` closes the dialog (the global handler's job) rather than
 *    the book, and typing `2` into the palette does not switch to 양면.
 *  * **The global `Esc`.** `useGlobalHotkeys` also closes the viewer. React
 *    runs child effects before parent effects, so this listener is registered
 *    first and wins; it closes the store, after which the global handler sees
 *    `bookId === null` and does nothing. Binding `Esc` here as well is what
 *    makes the viewer work when it is rendered without the app shell.
 */

import { useEffect } from 'react'

import { isTypingTarget } from '../../lib/useHotkeys'
import { useUiStore } from '../../store/ui'
import { useViewerStore, type DisplayMode, type ReadingDirection } from '../../store/viewer'

export interface ViewerKeysOptions {
  /** Reading direction — inverts `←`/`→`. */
  dir: ReadingDirection
  /** Forward one screen, in reading order. */
  onNext: () => void
  /** Back one screen, in reading order. */
  onPrev: () => void
  onToggleStrip: () => void
  onToggleFullscreen: () => void
  /** `H` — show the chrome, or send it away again (E-27). */
  onToggleChrome: () => void
  onExit: () => void
  onSetMode: (mode: DisplayMode) => void
  /** Set `false` while the book is still loading. */
  enabled?: boolean
}

/**
 * Inputs that take no text: a key pressed on one is not being typed.
 *
 * `HTMLInputElement.type` is normalised — lower-case, and `text` for a missing
 * or unknown value — so anything not listed here is text entry and keeps its
 * keys. The list is closed on purpose: a type nobody thought about is safer
 * typed into than taken.
 */
const NON_TEXT_INPUT_TYPES: ReadonlySet<string> = new Set([
  'radio',
  'checkbox',
  'range',
  'button',
  'submit',
  'reset',
  'color',
  'file',
  'image',
])

/**
 * Whether the viewer should leave `event` to the control it was pressed on.
 *
 * `isTypingTarget` answers that for every key but `H`, exactly as before: on a
 * radio or the page slider the arrows are the control's own step or selection,
 * and `Space`, the digits, `T` and `F` stay the control's too.
 *
 * **`H` is narrower (E-62 §2.2).** It is the keyboard reader's only way to put
 * the chrome away, and the controls a keyboard reader is on when they want to
 * are a seg radio or the slider — neither takes text, and neither does anything
 * with `h`. So only real text entry (a text-like input, a textarea, a select's
 * type-ahead, contenteditable) keeps it.
 */
function leaveToControl(event: KeyboardEvent): boolean {
  if (!isTypingTarget(event.target)) return false
  if (event.key.toLowerCase() !== 'h') return true
  const target = event.target
  return !(target instanceof HTMLInputElement && NON_TEXT_INPUT_TYPES.has(target.type))
}

/** `1` / `2` / `3` → the wire display modes (C-1: `spread`, never `double`). */
const MODE_KEYS: Readonly<Record<string, DisplayMode>> = {
  '1': 'single',
  '2': 'spread',
  '3': 'vertical',
}

export function useViewerKeys(options: ViewerKeysOptions): void {
  const {
    dir,
    onNext,
    onPrev,
    onToggleStrip,
    onToggleFullscreen,
    onToggleChrome,
    onExit,
    onSetMode,
    enabled = true,
  } = options

  useEffect(() => {
    if (!enabled) return undefined

    const onKeyDown = (event: KeyboardEvent): void => {
      // Ctrl/Cmd/Alt combinations belong to the browser or to the global map.
      if (event.metaKey || event.ctrlKey || event.altKey) return
      if (leaveToControl(event)) return
      // A dialog is on top: the viewer is not the thing being driven.
      if (useUiStore.getState().overlays.length > 0) return

      switch (event.key) {
        case 'ArrowRight':
          event.preventDefault()
          if (dir === 'rtl') onPrev()
          else onNext()
          return
        case 'ArrowLeft':
          event.preventDefault()
          if (dir === 'rtl') onNext()
          else onPrev()
          return
        // `Space` is always *forward*, whatever the reading direction — it is
        // "continue", not a direction key.
        case ' ':
        case 'Spacebar':
          event.preventDefault()
          onNext()
          return
        case 'Escape':
          // Already closed by whoever ran first; do not navigate twice.
          if (useViewerStore.getState().bookId === null) return
          event.preventDefault()
          onExit()
          return
        default:
          break
      }

      const lower = event.key.toLowerCase()
      if (lower === 't') {
        event.preventDefault()
        onToggleStrip()
        return
      }
      if (lower === 'f') {
        event.preventDefault()
        onToggleFullscreen()
        return
      }
      if (lower === 'h') {
        event.preventDefault()
        onToggleChrome()
        return
      }
      const mode = MODE_KEYS[event.key]
      if (mode !== undefined) {
        event.preventDefault()
        onSetMode(mode)
      }
    }

    window.addEventListener('keydown', onKeyDown)
    return () => {
      window.removeEventListener('keydown', onKeyDown)
    }
  }, [
    dir,
    enabled,
    onExit,
    onNext,
    onPrev,
    onSetMode,
    onToggleChrome,
    onToggleFullscreen,
    onToggleStrip,
  ])
}
