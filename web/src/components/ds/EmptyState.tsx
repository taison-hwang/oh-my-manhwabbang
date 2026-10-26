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
interface EmptyStateBaseProps {
  title: string
  body?: string
  action?: { label: string; onClick: () => void }
  /**
   * A glyph above the heading. Decorative — the title already says what
   * happened, and a band that reads its icon aloud says it twice.
   */
  icon?: ReactNode
  children?: ReactNode
  className?: string
}

export type EmptyStateProps = EmptyStateBaseProps &
  (
    | {
        variant?: 'band'
        /**
         * The band title's level in the outline (10-23 a11y audit). 3 by
         * default, for a band under a screen's own headings; 1 when the band is
         * the whole screen (an error state that replaces the header), 2 right
         * under a level-1 title. Only the tag moves: any other level carries
         * base.css's `.h3` twin, so the band looks the same at every level.
         */
        headingLevel?: 1 | 2 | 3
      }
    | {
        variant: 'hero'
        /** The hero's title is the screen's `<h1>`, always. */
        headingLevel?: never
      }
  )

const BAND_HEADING = { 1: 'h1', 2: 'h2', 3: 'h3' } as const

export function EmptyState({
  title,
  body,
  action,
  variant = 'band',
  headingLevel = 3,
  icon,
  children,
  className,
}: EmptyStateProps) {
  const BandHeading = BAND_HEADING[headingLevel]
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
      {variant === 'band' ? (
        <BandHeading className={headingLevel === 3 ? undefined : 'h3'}>{title}</BandHeading>
      ) : (
        <h1 className="text-balance">{title}</h1>
      )}
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
