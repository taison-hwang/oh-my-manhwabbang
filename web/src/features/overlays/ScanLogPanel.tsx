import { TriangleAlert } from 'lucide-react'

import { useScanLog } from '../../api/queries'
import type { LogLevel, ScanLogEntry } from '../../api/types'
import { cn } from '../../lib/cn'
import { formatCount } from '../../lib/format'

/**
 * 스캔 로그 (ui-spec §8.6 §3, FR-IDX-004).
 *
 * INFO → `--ink-dim`, WARN → `--accent-text`, ERROR → `--color-accent`. That
 * ladder is the point of the panel: FR-IDX-010 says a broken archive never
 * stops the scan, so the only way a user learns that nine of eleven thousand
 * archives were truncated is by reading this list.
 */
const LEVEL_CLASS: Record<LogLevel, string> = {
  info: 'text-ink-dim',
  warn: 'text-accent-text',
  error: 'text-accent',
}

/** Default page of the log; the wire default is 200 too (arch §7.10). */
export const SCAN_LOG_LIMIT = 200

/** `23:41:02` in the reader's local zone, from Unix **seconds**. */
function formatLogTime(unixSeconds: number): string {
  if (!Number.isFinite(unixSeconds)) return '--:--:--'
  const d = new Date(unixSeconds * 1000)
  const pad = (n: number): string => n.toString().padStart(2, '0')
  return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
}

function countLevel(items: readonly ScanLogEntry[], level: LogLevel): number {
  return items.filter((item) => item.level === level).length
}

export function ScanLogPanel() {
  const log = useScanLog({ limit: SCAN_LOG_LIMIT })
  const items: readonly ScanLogEntry[] = log.data?.items ?? []
  const warns = countLevel(items, 'warn')
  const errors = countLevel(items, 'error')

  return (
    <section className="flex min-w-0 flex-col gap-2">
      <div className="flex items-baseline gap-3">
        <h6 className="flex-1">스캔 로그</h6>
        <span className="flex items-center gap-[6px] text-xs tabular-nums text-accent-text">
          <TriangleAlert size={12} aria-hidden={true} />
          {`경고 ${formatCount(warns)} · 오류 ${formatCount(errors)}`}
        </span>
      </div>
      <div
        className="max-h-[200px] overflow-y-auto border-t-2 border-rule-strong text-sm tracking-[.01em]"
        data-testid="scan-log-body"
      >
        {items.map((item) => (
          <div key={item.id} className="flex gap-3 border-b border-rule py-[3px]">
            <span className="flex-none tabular-nums text-ink-dim">{formatLogTime(item.ts)}</span>
            <span className={cn('w-12 flex-none tracking-[.06em]', LEVEL_CLASS[item.level])}>
              {item.level.toUpperCase()}
            </span>
            <span className="min-w-0 truncate whitespace-nowrap text-ink" title={item.message}>
              {item.message}
            </span>
          </div>
        ))}
      </div>
    </section>
  )
}
