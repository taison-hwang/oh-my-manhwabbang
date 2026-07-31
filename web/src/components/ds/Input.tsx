import { forwardRef, type InputHTMLAttributes } from 'react'

import { cn } from '../../lib/cn'

/**
 * `.input` (ui-spec §2.3): 36px min-height, surface fill, 1px divider border,
 * accent caret, zero radius, and a focus ring that *replaces* the border colour
 * rather than sitting outside it (`outline-offset: 0`).
 *
 * Forwarded ref because the command palette autofocuses its query field on open
 * and the top-bar field is focused by `Ctrl/Cmd+K`'s sibling shortcut.
 */
export interface InputProps extends InputHTMLAttributes<HTMLInputElement> {
  /** Optional `<label>` text; renders the `.field` wrapper when present. */
  label?: string
}

export const Input = forwardRef<HTMLInputElement, InputProps>(function Input(
  { label, className, id, ...rest },
  ref,
) {
  const field = <input ref={ref} id={id} className={cn('input', className)} {...rest} />
  if (label === undefined) return field
  return (
    <div className="field">
      <label htmlFor={id}>{label}</label>
      {field}
    </div>
  )
})
