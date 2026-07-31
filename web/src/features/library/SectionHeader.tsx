import { formatSeriesCount } from '../../lib/format'

/**
 * The sticky section header (ui-spec §4.4).
 *
 * It carries the **scope name**, which is the only place the active sidebar
 * scope is legible between 768 and 1023 where the sidebar has collapsed to a
 * 56px icon rail (ui-spec §7), plus the result count.
 *
 * It is a sibling of the virtual scroller, not a child of it: `position:sticky`
 * inside a stack of absolutely-positioned, transformed rows sticks to nothing.
 * The opaque `--color-bg` stays anyway — it is what stops cards bleeding through
 * on the tiers where the header does scroll with the page.
 */
export interface SectionHeaderProps {
  /** 전체 시리즈 / 읽는 중 / 최근 추가 / 완독, or a root's label. */
  label: string
  /** Matches before pagination — `total`, never `items.length`. */
  count: number
}

export function SectionHeader({ label, count }: SectionHeaderProps) {
  return (
    <div className="sticky top-0 z-sticky flex flex-none items-baseline gap-3 border-b border-rule bg-bg px-4 pb-3 pt-4">
      <h4>{label}</h4>
      <span className="text-xs tabular-nums text-ink-dim">{formatSeriesCount(count)}</span>
    </div>
  )
}
