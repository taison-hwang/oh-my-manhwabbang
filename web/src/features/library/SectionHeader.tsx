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
 *
 * **E-32** drops the 1px underline (the band is separated by space now, not by
 * a rule) and moves the label from `<h4>` to `<h6>`. The tag change is not
 * cosmetic and not the prototype's: the prototype makes every heading a `<div>`,
 * which E-32 §4 refuses. What this does is make the seven section headers in the
 * product agree on one tag — six already said `<h6>` and this one said `<h4>`
 * for no reason anybody recorded — and `base.css` then restyles that one tag to
 * the 16px / -0.01em / mixed-case the skin draws.
 */
export interface SectionHeaderProps {
  /** 전체 시리즈 / 읽는 중 / 최근 추가 / 완독, or a root's label. */
  label: string
  /** Matches before pagination — `total`, never `items.length`. */
  count: number
}

export function SectionHeader({ label, count }: SectionHeaderProps) {
  return (
    <div className="sticky top-0 z-sticky flex flex-none items-baseline gap-3 bg-bg px-4 pb-3 pt-4">
      <h6>{label}</h6>
      <span className="text-xs tabular-nums text-ink-dim">{formatSeriesCount(count)}</span>
    </div>
  )
}
