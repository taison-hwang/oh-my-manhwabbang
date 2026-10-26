import { X } from 'lucide-react'
import { useEffect, useRef, type KeyboardEvent, type ReactNode } from 'react'
import { createPortal } from 'react-dom'

import { trapTab, useModalLayer } from '../../lib/modalLayer'
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
 *
 * It is a modal (10-23 accessibility audit #2): while open the page under it —
 * the icon rail at 768–1023 included — is inert (`lib/modalLayer.ts`), Tab
 * wraps inside the panel, and Esc closes it wherever focus is. The backdrop and
 * the panel share one wrapper, and that wrapper is the layer: modalLayer inerts
 * the layer's siblings, so with the panel as the layer the scrim would go inert
 * and stop closing the drawer on a press.
 */
export interface MobileDrawerProps {
  open: boolean
  onClose: () => void
  children: ReactNode
}

export function MobileDrawer({ open, onClose, children }: MobileDrawerProps) {
  const layerRef = useRef<HTMLDivElement>(null)
  const panelRef = useRef<HTMLDivElement>(null)

  useModalLayer(layerRef, open, { initialFocus: 'first' })

  useEffect(() => {
    if (!open) return undefined
    // Focus can be outside the panel (the focused row unmounted → <body>), where
    // its onKeyDown never hears Esc. The global ladder runs first and marks the
    // Esc it used (an overlay opened from here); one consumed in the panel never
    // reaches window.
    const onWindowKeyDown = (e: globalThis.KeyboardEvent): void => {
      if (e.key !== 'Escape' || e.defaultPrevented) return
      e.preventDefault()
      onClose()
    }
    window.addEventListener('keydown', onWindowKeyDown)
    return () => {
      window.removeEventListener('keydown', onWindowKeyDown)
    }
  }, [open, onClose])

  if (!open) return null

  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>): void => {
    if (e.key !== 'Escape') {
      if (panelRef.current !== null) trapTab(e, panelRef.current)
      return
    }
    // Consume it: the global Esc ladder must not also close an overlay.
    e.preventDefault()
    e.stopPropagation()
    onClose()
  }

  return createPortal(
    <div ref={layerRef}>
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
    </div>,
    document.body,
  )
}
