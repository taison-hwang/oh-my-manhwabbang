/**
 * The 페이지 로딩 indicator (ui-spec §6.3, FR-VWR-003).
 *
 * The whole point of the component is what it is *not*: it is not a stage-wide
 * spinner and it never replaces the page. "Never blank the stage — the previous
 * page stays on screen; only a small indicator appears." So this is a 11×11 px
 * ring plus one word, pinned bottom-right, over whatever is already painted.
 *
 * Mounting is gated by `useDelayedFlag`: a page served warm from the prefetch
 * cache decodes in single-digit milliseconds, and a spinner that flashes for one
 * frame on every page turn reads as jank rather than as progress. Below ~240 ms
 * nothing is shown at all.
 *
 * The ring is one of exactly two circles in the product (D-40) — the other is
 * the radio dot — which is why `rounded-full` appears here and nowhere else in
 * the viewer.
 */
export interface PageLoadingIndicatorProps {
  /** Already delayed by `useDelayedFlag`; this component does no timing. */
  visible: boolean
}

export function PageLoadingIndicator({ visible }: PageLoadingIndicatorProps) {
  if (!visible) return null
  return (
    <div
      data-role="page-loading"
      role="status"
      aria-live="polite"
      className="pointer-events-none absolute bottom-6 right-6 flex items-center gap-2 text-xs uppercase tracking-[.06em] text-ink-faint"
    >
      <span
        aria-hidden="true"
        className="h-[11px] w-[11px] animate-spin rounded-full border-2 border-neutral-700"
        style={{ borderTopColor: 'var(--color-accent-400)' }}
      />
      페이지 로딩
    </div>
  )
}
