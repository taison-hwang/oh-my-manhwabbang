import type { ButtonHTMLAttributes, ReactNode } from 'react'

import { cn } from '../../lib/cn'

/**
 * `.btn` (ui-spec §2.3).
 *
 * The geometry lives in `styles/base.css`; this component only picks classes.
 * `block` is the flush-left rule of ui-spec §0.3: a full-width button still
 * starts its label at the left padding edge — see the grid-card hover overlay,
 * where 읽기 시작 and 상세 are both left-aligned inside 100 %-wide buttons.
 */
export type ButtonVariant = 'plain' | 'primary' | 'secondary' | 'ghost'

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant
  /** `.btn-block`: full width, flush-left label. */
  block?: boolean
  /** `.btn-icon`: the square 36×36 form (44×44 below 768px). */
  icon?: boolean
  children?: ReactNode
}

const VARIANT_CLASS: Record<ButtonVariant, string> = {
  plain: '',
  primary: 'btn-primary',
  secondary: 'btn-secondary',
  ghost: 'btn-ghost',
}

export function Button({
  variant = 'plain',
  block = false,
  icon = false,
  className,
  type,
  children,
  ...rest
}: ButtonProps) {
  return (
    <button
      // Never let a button inside a form default to submit by accident.
      type={type ?? 'button'}
      className={cn(
        'btn',
        VARIANT_CLASS[variant],
        block && 'btn-block',
        icon && 'btn-icon',
        className,
      )}
      {...rest}
    >
      {children}
    </button>
  )
}
