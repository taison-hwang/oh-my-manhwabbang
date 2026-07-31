import type { ReactNode } from 'react'

import { cn } from '../../lib/cn'
import { Button } from './Button'

/**
 * `EmptyState` (ui-spec §9 #16).
 *
 * The no-results state is a **band between two 2px rules**, flush left — not a
 * centred illustration. design.md principle 3: empty states are common in this
 * product and must not look impoverished, and the two rules are the entire
 * design.
 *
 * `variant="hero"` drops the rules and scales the heading to 42px for the
 * first-run onboarding screen (ui-spec §4.6), which replaces the whole shell.
 */
export interface EmptyStateProps {
  title: string
  body?: string
  action?: { label: string; onClick: () => void }
  variant?: 'band' | 'hero'
  /**
   * A glyph above the heading. Decorative — the title already says what
   * happened, and a band that reads its icon aloud says it twice.
   */
  icon?: ReactNode
  children?: ReactNode
  className?: string
}

export function EmptyState({
  title,
  body,
  action,
  variant = 'band',
  icon,
  children,
  className,
}: EmptyStateProps) {
  return (
    <div
      className={cn(
        'flex flex-col items-start gap-3',
        variant === 'band'
          ? 'border-y-2 border-rule-strong py-8'
          : 'w-full max-w-[520px] gap-4',
        className,
      )}
    >
      {icon !== undefined && (
        <span aria-hidden="true" className="text-neutral-500">
          {icon}
        </span>
      )}
      {variant === 'band' ? <h3>{title}</h3> : <h1 className="text-balance">{title}</h1>}
      {body !== undefined && (
        <p className={cn('text-ink-muted', variant === 'hero' ? 'text-lg' : '')}>{body}</p>
      )}
      {children}
      {action !== undefined && (
        <Button variant={variant === 'hero' ? 'primary' : 'secondary'} onClick={action.onClick}>
          {action.label}
        </Button>
      )}
    </div>
  )
}
