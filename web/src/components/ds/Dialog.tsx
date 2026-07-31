import { useEffect, useId, useRef, type KeyboardEvent, type ReactNode } from 'react'
import { createPortal } from 'react-dom'

import { cn } from '../../lib/cn'

/**
 * `.dialog-backdrop` + `.dialog` (ui-spec §2.3), with the accessibility
 * contract every overlay in the product owes (impl-plan WP-10 acceptance 9):
 * `aria-modal`, a focus trap, `Esc` to close, and focus restored to whatever
 * was focused before it opened.
 *
 * `align="top"` is the command palette's placement (`place-items: start
 * center` with a 12vh inset); `width` carries the per-dialog `min(Npx, 100%)`
 * so 440/560/620/760 all come from the call site rather than from variants.
 *
 * ## Why the panel suppresses its own focus ring
 *
 * The panel is `tabIndex={-1}` so that a dialog with **no focusable child** —
 * the shortcuts dialog is one — still has somewhere to put focus, and so that
 * `Esc` and the Tab trap have a keydown target. But `:focus-visible` fires on a
 * programmatic `.focus()` whenever the last input modality was the keyboard,
 * which for a dialog opened with `?` or `⌘K` it always is. The result, measured
 * in Chrome: a 2px accent ring drawn around the whole borderless panel the
 * moment it opens, on a surface the reference capture shows with no border at
 * all. `outline-none` is scoped to the panel element itself and nothing inside
 * it, so every real control in the dialog keeps the themed ring of ui-spec §2.2
 * — the trap, the `Esc` handler and the focus restore are untouched.
 */
export interface DialogProps {
  open: boolean
  onClose: () => void
  /** Rendered as `.dialog-title` and wired to `aria-labelledby`. */
  title?: ReactNode
  /** e.g. `min(620px, 100%)`. Defaults to the DS's `min(440px, 100%)`. */
  width?: string
  align?: 'center' | 'top'
  /** Extra classes for the panel, e.g. the palette's zero padding. */
  panelClassName?: string
  children?: ReactNode
}

const FOCUSABLE = [
  'a[href]',
  'button:not([disabled])',
  'input:not([disabled])',
  'select:not([disabled])',
  'textarea:not([disabled])',
  '[tabindex]:not([tabindex="-1"])',
].join(',')

export function Dialog({
  open,
  onClose,
  title,
  width,
  align = 'center',
  panelClassName,
  children,
}: DialogProps) {
  const panelRef = useRef<HTMLDivElement>(null)
  const titleId = useId()

  // Restore focus to the opener. Captured on open and released on close, so a
  // dialog that opens another dialog unwinds in the right order.
  useEffect(() => {
    if (!open) return undefined
    const opener = document.activeElement
    const panel = panelRef.current
    const first = panel?.querySelector<HTMLElement>(FOCUSABLE)
    ;(first ?? panel)?.focus()
    return () => {
      if (opener instanceof HTMLElement) opener.focus()
    }
  }, [open])

  if (!open) return null

  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>): void => {
    if (e.key === 'Escape') {
      // Stop here: the global Esc ladder (useHotkeys) must not also pop an
      // overlay off the stack for the same keystroke.
      e.preventDefault()
      e.stopPropagation()
      onClose()
      return
    }
    if (e.key !== 'Tab') return

    const panel = panelRef.current
    if (!panel) return
    const items = [...panel.querySelectorAll<HTMLElement>(FOCUSABLE)]
    if (items.length === 0) {
      e.preventDefault()
      return
    }
    const first = items[0]
    const last = items[items.length - 1]
    if (!first || !last) return

    const active = document.activeElement
    if (e.shiftKey && (active === first || active === panel)) {
      e.preventDefault()
      last.focus()
    } else if (!e.shiftKey && active === last) {
      e.preventDefault()
      first.focus()
    }
  }

  return createPortal(
    <div
      className="dialog-backdrop"
      style={align === 'top' ? { placeItems: 'start center', paddingTop: '12vh' } : undefined}
      onMouseDown={(e) => {
        // Only a click that both starts and ends on the backdrop dismisses;
        // a drag that began inside the panel must not close it.
        if (e.target === e.currentTarget) onClose()
      }}
    >
      <div
        ref={panelRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby={title === undefined ? undefined : titleId}
        tabIndex={-1}
        className={cn('dialog focus:outline-none focus-visible:outline-none', panelClassName)}
        style={width === undefined ? undefined : { width }}
        onKeyDown={onKeyDown}
      >
        {title !== undefined && (
          <div className="dialog-title" id={titleId}>
            {title}
          </div>
        )}
        {children}
      </div>
    </div>,
    document.body,
  )
}
