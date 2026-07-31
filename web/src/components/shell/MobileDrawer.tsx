import { X } from 'lucide-react'
import { useEffect, useRef, type KeyboardEvent, type ReactNode } from 'react'
import { createPortal } from 'react-dom'

import { Button } from '../ds/Button'
import { VisuallyHidden } from '../ds/VisuallyHidden'

/**
 * The off-canvas sidebar below 768px (ui-spec §7, D-42).
 *
 * The prototype has no responsive layer at all: at 400px its fixed 240px
 * sidebar eats 60 % of the viewport (`library-grid-400-broken.png`). Below the
 * mobile breakpoint the sidebar is removed from the flow entirely (see
 * `.sidebar` in base.css) and reappears here — 280px, over a `--scrim-modal`
 * backdrop, closed by default.
 *
 * Rendered into a portal so the fixed panel is not trapped by the shell's
 * `overflow: hidden`.
 */
export interface MobileDrawerProps {
  open: boolean
  onClose: () => void
  children: ReactNode
}

export function MobileDrawer({ open, onClose, children }: MobileDrawerProps) {
  const panelRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!open) return undefined
    const opener = document.activeElement
    panelRef.current?.focus()
    return () => {
      if (opener instanceof HTMLElement) opener.focus()
    }
  }, [open])

  if (!open) return null

  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>): void => {
    if (e.key !== 'Escape') return
    // Consume it: the global Esc ladder must not also close an overlay.
    e.preventDefault()
    e.stopPropagation()
    onClose()
  }

  return createPortal(
    <>
      <div className="drawer-backdrop" onMouseDown={onClose} />
      <div
        ref={panelRef}
        role="dialog"
        aria-modal="true"
        aria-label="라이브러리 탐색"
        tabIndex={-1}
        className="drawer-panel"
        onKeyDown={onKeyDown}
      >
        <div className="flex justify-end border-b-2 border-rule-strong px-2 py-2">
          <Button variant="secondary" icon onClick={onClose}>
            <X size={16} aria-hidden={true} />
            <VisuallyHidden>닫기</VisuallyHidden>
          </Button>
        </div>
        {children}
      </div>
    </>,
    document.body,
  )
}
