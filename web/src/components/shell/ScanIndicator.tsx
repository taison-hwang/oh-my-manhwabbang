import { cn } from '../../lib/cn'

/**
 * `ScanIndicator` (ui-spec §9 #11, §4.1 footer) — FR-IDX-004's ambient signal.
 *
 * A 7px dot plus one line of text in the sidebar footer. The dot is the accent
 * while a run is in flight and `--ink-faint` when idle; clicking anywhere on
 * the row opens the scan log (the 스캔 로그 section of Settings).
 *
 * The label is passed in rather than derived here, because the copy templates
 * (`스캔 중 …` / `스캔 대기 — {n}분 전 완료`) belong to `lib/format.ts` where
 * they are tested against the ui-spec §9 catalogue.
 *
 * Polling itself is `useScanStatus` (WP-06: 1 s while `state !== "idle"`,
 * C-11). This component never fetches.
 */
export interface ScanIndicatorProps {
  scanning: boolean
  label: string
  onOpenLog: () => void
  /** Icon-rail mode: the dot only, with the label as the accessible name. */
  compact?: boolean
  className?: string
}

export function ScanIndicator({
  scanning,
  label,
  onOpenLog,
  compact = false,
  className,
}: ScanIndicatorProps) {
  return (
    <button
      type="button"
      onClick={onOpenLog}
      aria-label={label}
      className={cn(
        // `scan-indicator` carries the <768 44px touch minimum (base.css).
        'scan-indicator flex min-w-0 items-center gap-2 border-0 bg-transparent p-0 text-left',
        'text-xs tracking-[.02em] text-ink-muted hover:text-accent',
        compact && 'justify-center',
        className,
      )}
    >
      <span
        aria-hidden="true"
        className={cn(
          'h-[7px] w-[7px] flex-[0_0_7px]',
          scanning ? 'bg-accent' : 'bg-ink-faint',
        )}
      />
      {!compact && <span className="min-w-0 truncate">{label}</span>}
    </button>
  )
}
