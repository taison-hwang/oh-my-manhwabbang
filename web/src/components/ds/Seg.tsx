import { useId, type ReactNode } from 'react'

import { cn } from '../../lib/cn'

/**
 * `.seg` / `.seg-opt` (ui-spec §2.3) — the segmented control.
 *
 * Built from real radio inputs so it is a single tab stop with arrow-key
 * navigation for free; the inputs are visually hidden and the checked state is
 * mirrored onto `data-checked` for styling. `:has(input:checked)` would work in
 * Chrome, but the attribute keeps the component assertable in jsdom, which is
 * where the "checked option is an accent field" rule is tested.
 *
 * Used for: 그리드/리스트, 읽기 방향, 단면/양면/세로, 너비/높이/원본/화면,
 * and the 라이트/다크/시스템 theme switch.
 */
export interface SegOption<T extends string> {
  value: T
  label: string
  /**
   * A glyph before the label. Decorative only — the label stays and remains the
   * accessible name, because an option that says only 세로 in pictures is an
   * option nobody can describe out loud. Callers pass it `aria-hidden`.
   */
  icon?: ReactNode
  disabled?: boolean
}

export interface SegProps<T extends string> {
  value: T
  options: readonly SegOption<T>[]
  onChange: (value: T) => void
  /** Accessible name for the group, e.g. `읽기 방향`. */
  'aria-label'?: string
  className?: string
}

export function Seg<T extends string>({
  value,
  options,
  onChange,
  className,
  'aria-label': ariaLabel,
}: SegProps<T>) {
  const name = useId()
  return (
    <div className={cn('seg', className)} role="radiogroup" aria-label={ariaLabel}>
      {options.map((opt) => {
        const checked = opt.value === value
        return (
          <label
            key={opt.value}
            className="seg-opt"
            data-checked={checked ? 'true' : 'false'}
            data-value={opt.value}
          >
            <input
              type="radio"
              className="sr-only"
              name={name}
              value={opt.value}
              checked={checked}
              disabled={opt.disabled ?? false}
              onChange={() => {
                onChange(opt.value)
              }}
            />
            {opt.icon !== undefined && (
              <span aria-hidden="true" className="mr-[6px] flex-none">
                {opt.icon}
              </span>
            )}
            {opt.label}
          </label>
        )
      })}
    </div>
  )
}
