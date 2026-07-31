import type { ReactNode } from 'react'

/**
 * Content for assistive technology only.
 *
 * Used where the design deliberately shows a glyph instead of a word — the `?`
 * shortcuts button, the `⌘K` chip, the sidebar rail's icon-only rows — so the
 * accessible name is still the Korean label from the ui-spec §9 catalogue.
 *
 * `sr-only` rather than `display:none`: hidden content is not announced.
 */
export interface VisuallyHiddenProps {
  children: ReactNode
}

export function VisuallyHidden({ children }: VisuallyHiddenProps) {
  return <span className="sr-only">{children}</span>
}
