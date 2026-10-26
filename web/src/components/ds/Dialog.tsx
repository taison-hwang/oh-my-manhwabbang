import { useId, useRef, type KeyboardEvent, type ReactNode } from 'react'
import { createPortal } from 'react-dom'

import { cn } from '../../lib/cn'
import { trapTab, useModalLayer } from '../../lib/modalLayer'

/**
 * `.dialog-backdrop` + `.dialog` (ui-spec §2.3), with the accessibility
 * contract every overlay in the product owes (impl-plan WP-10 acceptance 9):
 * `aria-modal`, a focus trap, `Esc` to close, focus restored to whatever was
 * focused before it opened — and, since the 10-23 accessibility audit, two more:
 *
 *  - **The page behind it is `inert` while it is open** (audit #8). Chrome does
 *    not take a page out of its accessibility tree for `aria-modal` alone —
 *    measured — so the panel is a layer of `lib/modalLayer.ts`, which marks
 *    everything outside the top layer inert and releases it, *before* focus goes
 *    back to the opener, when the dialog closes. A dialog opened over another
 *    (the palette over settings, settings over the drawer) inerts the one below.
 *  - **It always has a name, and the name is only the name** (audits #3, #7).
 *    Either `title`, rendered as an `<h2 class="dialog-title">` that
 *    `aria-labelledby` points at, or `ariaLabel` for a dialog with no visible
 *    title (the command palette) — never neither, which the props type refuses.
 *    Anything that sits *beside* the title — the settings dialog's `esc` button,
 *    the shortcuts sheet's `뷰어` kicker — goes in `titleAside`, outside the
 *    heading. Put inside `title`, it was read as part of the name: the settings
 *    dialog was announced as "설정 닫기".
 *
 * `align="top"` is the command palette's placement (`place-items: start
 * center` with a 12vh inset); `width` carries the per-dialog `min(Npx, 100%)`
 * so 440/560/620/760 all come from the call site rather than from variants.
 *
 * `titleLayout` is the two header rows ui-spec draws: `'spread'` (§8.6 — the
 * title `flex:1`, the aside pushed to the end, centred on each other) and
 * `'inline'` (§8.5 — the aside right after the title, on its baseline).
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
interface DialogBaseProps {
  open: boolean
  onClose: () => void
  /** e.g. `min(620px, 100%)`. Defaults to the DS's `min(440px, 100%)`. */
  width?: string
  align?: 'center' | 'top'
  /** Extra classes for the panel, e.g. the palette's zero padding. */
  panelClassName?: string
  children?: ReactNode
}

/** A visible title: the `<h2>` is the dialog's name. */
interface TitledDialogProps {
  /** Rendered as `h2.dialog-title` and wired to `aria-labelledby`. */
  title: Exclude<ReactNode, boolean | null | undefined>
  /** Beside the heading, never part of the name (the `esc` button, a kicker). */
  titleAside?: ReactNode
  titleLayout?: 'spread' | 'inline'
  ariaLabel?: never
}

/** No visible title: the label is the name. */
interface LabelledDialogProps {
  ariaLabel: string
  title?: never
  titleAside?: never
  titleLayout?: never
}

/** Exactly one of `title` or `ariaLabel` — a nameless dialog is a type error. */
export type DialogProps = DialogBaseProps & (TitledDialogProps | LabelledDialogProps)

export function Dialog({
  open,
  onClose,
  title,
  titleAside,
  titleLayout = 'spread',
  ariaLabel,
  width,
  align = 'center',
  panelClassName,
  children,
}: DialogProps) {
  const panelRef = useRef<HTMLDivElement>(null)
  const titleId = useId()

  // Focus in, page inert, and on close page released *then* focus back to the
  // opener — one layer of the stack, so a dialog over a dialog unwinds in order.
  useModalLayer(panelRef, open)

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
    const panel = panelRef.current
    if (panel) trapTab(e, panel)
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
        aria-label={ariaLabel}
        tabIndex={-1}
        className={cn('dialog focus:outline-none focus-visible:outline-none', panelClassName)}
        style={width === undefined ? undefined : { width }}
        onKeyDown={onKeyDown}
      >
        {title !== undefined && (
          <div
            className={cn(
              'flex gap-3',
              titleLayout === 'inline' ? 'items-baseline' : 'items-center',
            )}
          >
            {/* `h2` for the outline (the settings sections are its `h3`s);
                `.dialog-title` is still what it looks like, with no reset: the
                base layer's heading rules set size (32px), family, weight,
                line-height, `letter-spacing: 0` and `margin: 0`; `.dialog-title`,
                in the later components layer, overrides the first four, and the
                last two equal a div's. Measured in Chrome against the built sheet:
                same computed style, same 670×22.39 box in the settings row. */}
            <h2
              id={titleId}
              className={cn('dialog-title', titleLayout === 'spread' && 'flex-1')}
            >
              {title}
            </h2>
            {titleAside}
          </div>
        )}
        {children}
      </div>
    </div>,
    document.body,
  )
}
