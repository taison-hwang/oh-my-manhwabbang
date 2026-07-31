import { useState } from 'react'

import { useCacheUsage, usePurgeCache } from '../../api/queries'
import type { CacheUsageEntry } from '../../api/types'
import { Button } from '../../components/ds/Button'
import { formatBytes, formatItemCount } from '../../lib/format'

/**
 * 캐시 — usage and 전체 삭제 (FR-THM-008, ui-spec §8.6 §2).
 *
 * Two deviations from the prototype, both forced by the frozen contract:
 *
 *  * The prototype prints `1.84 GB / 4.00 GB`. `CacheUsage` (arch §7.9) has no
 *    budget field — there is no cache quota in this product — so the
 *    denominator is dropped rather than invented, and the 4px bar shows the
 *    **composition** (thumbs / pdf / wazero) of the bytes that do exist.
 *  * Deleting the cache is destructive and irreversible from the UI, so
 *    `전체 삭제` arms a confirmation rather than firing on the first click.
 *    Neither `취소` nor the prompt is in the ui-spec §9 catalogue, which has no
 *    entry for a confirmation of any kind.
 */
export function CachePanel() {
  const usage = useCacheUsage()
  const purge = usePurgeCache()
  const [confirming, setConfirming] = useState(false)

  const total = usage.data?.total_bytes ?? 0
  const parts = formatBytes(total).split(' ')
  const value = parts[0] ?? '0'
  const unit = parts[1] ?? 'KB'
  const entries: readonly CacheUsageEntry[] = usage.data?.entries ?? []

  return (
    <section className="flex min-w-0 flex-1 flex-col gap-2">
      <h6>캐시</h6>

      <div className="flex items-baseline gap-2">
        <span className="font-heading text-[32px] font-extrabold tabular-nums">{value}</span>
        <span className="text-base text-ink-muted">{unit}</span>
      </div>

      <div className="flex h-[4px] w-full bg-fill-track">
        {entries.map((entry) => (
          <span
            key={entry.kind}
            data-testid={`cache-bar-${entry.kind}`}
            className={
              entry.kind === 'thumbs'
                ? 'h-full bg-accent'
                : entry.kind === 'pdf'
                  ? 'h-full bg-ink-muted'
                  : 'h-full bg-fill-track-2'
            }
            style={{ width: total <= 0 ? '0%' : `${String((entry.bytes / total) * 100)}%` }}
          />
        ))}
      </div>

      <p className="text-xs text-ink-dim">썸네일 · 압축 해제 페이지 캐시</p>

      <ul className="flex flex-col gap-[2px]">
        {entries.map((entry) => (
          <li key={entry.kind} className="flex items-baseline gap-2 text-xs text-ink-muted">
            <span className="w-[56px] flex-none uppercase tracking-[.06em] text-ink-dim">
              {entry.kind}
            </span>
            <span className="tabular-nums">
              {`${formatItemCount(entry.files)} · ${formatBytes(entry.bytes)}`}
            </span>
          </li>
        ))}
      </ul>

      {confirming ? (
        <div className="flex flex-col items-start gap-2">
          <p role="alert" className="text-xs text-accent-text">
            캐시를 모두 삭제할까요?
          </p>
          <div className="flex gap-2">
            <Button
              variant="primary"
              disabled={purge.isPending}
              onClick={() => {
                purge.mutate('all')
                setConfirming(false)
              }}
            >
              전체 삭제
            </Button>
            <Button
              variant="ghost"
              onClick={() => {
                setConfirming(false)
              }}
            >
              취소
            </Button>
          </div>
        </div>
      ) : (
        <Button
          variant="secondary"
          className="self-start"
          onClick={() => {
            setConfirming(true)
          }}
        >
          전체 삭제
        </Button>
      )}
    </section>
  )
}
