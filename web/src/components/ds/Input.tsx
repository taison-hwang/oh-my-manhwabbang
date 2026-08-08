import { forwardRef, type InputHTMLAttributes } from 'react'

import { cn } from '../../lib/cn'

/**
 * `.input` (ui-spec §2.3, as amended by E-36 / E-42): 36px min-height and a
 * **recessed cream well** — an absolute `--control-fill` that does not flip with
 * the theme, `--on-control` ink, `--radius-md`, no border at all, and the focus
 * ring drawn as the outer layer of the same `box-shadow` stack that cuts the
 * recess.
 *
 * Every clause of that sentence used to read the other way — "surface fill, 1px
 * divider border, zero radius, a focus ring that replaces the border colour".
 * Three of those died at E-32 and the rest at E-42, and the docstring went on
 * describing them. `Card.tsx` carries a long note about the same failure because
 * ruling E-36 §2 named it; this file was never named and drifted just as far,
 * which is the point — being named is not what makes a comment stale.
 *
 * Forwarded ref because the top-bar field is focused by `Ctrl/Cmd+K`'s sibling
 * shortcut. The command palette no longer uses this component: its query field
 * is a bare `<input>` (ui-spec §8.4 cancels every declaration of this class for
 * it — see the note there).
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
