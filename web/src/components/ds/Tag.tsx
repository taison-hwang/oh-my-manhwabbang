import type { HTMLAttributes, ReactNode } from 'react'

import { cn } from '../../lib/cn'

/** `.tag` (ui-spec §2.3). Zero radius, 11px, `3px 10px`. */
export type TagTone = 'accent' | 'accent-2' | 'neutral' | 'outline'

export interface TagProps extends HTMLAttributes<HTMLSpanElement> {
  tone?: TagTone
  children?: ReactNode
}

const TONE_CLASS: Record<TagTone, string> = {
  accent: 'tag-accent',
  'accent-2': 'tag-accent-2',
  neutral: 'tag-neutral',
  outline: 'tag-outline',
}

export function Tag({ tone = 'neutral', className, children, ...rest }: TagProps) {
  return (
    <span className={cn('tag', TONE_CLASS[tone], className)} {...rest}>
      {children}
    </span>
  )
}
