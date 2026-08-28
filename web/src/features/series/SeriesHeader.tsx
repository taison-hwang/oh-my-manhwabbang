import { BookOpen, Play, RefreshCw } from 'lucide-react'
import type { ReactNode } from 'react'

import { useCoverImage, useRoots } from '../../api/queries'
import type { ReadingDir, SeriesDetail } from '../../api/types'
import { thumbWidthFor } from '../../api/urls'
import { FallbackCover } from '../../components/ds/FallbackCover'
import { Button } from '../../components/ds/Button'
import { Hr } from '../../components/ds/Hr'
import { ProgressBar } from '../../components/ds/ProgressBar'
import { Seg } from '../../components/ds/Seg'
import {
  formatBytes,
  formatCount,
  formatLabel,
  formatPercent,
  formatSourcePath,
} from '../../lib/format'
import { textLang } from '../../lib/textLang'
import { hasStarted, seriesProgressRatio } from './volume'

/**
 * Series detail header (ui-spec §5.1, prd UI-002, design.md 화면 2).
 *
 * 176×264 cover · title · path · four stats (권 · 용량 · 형식 · 진행률) ·
 * action row · 읽기 방향.
 *
 * The path line is **the only filesystem path the product shows outside the
 * settings dialog** (prd §5.3, UI 5.3). Everywhere else a series is a series.
 * It is the **원본 경로** — the root's absolute path with the series' own
 * root-relative path appended — and not `SeriesDetail.path` on its own: prd 1.3
 * makes a series exactly one direct child of a root, so that field is
 * byte-identical to `SeriesDetail.name` for every series in the collection and
 * printing it alone rendered `[만화] 이누야샤 01~56권 완결` twice, an H1 and a
 * subtitle saying the same thing (measured against
 * `ui-shots/series-detail-grid-1440.png`, which shows the absolute path).
 * `useRoots` is already in the cache — the app shell fetches it — so this costs
 * no request; until it lands the line falls back to the relative path.
 *
 * The 읽기 방향 `.seg` is a client-only seed (C-9 / D-35): persisted direction
 * is per *book*, on the server. This control never writes the API — it seeds
 * the books opened from this screen, and the parent persists it in
 * `store/seriesDir.ts`.
 */
export interface SeriesHeaderProps {
  series: SeriesDetail
  dir: ReadingDir
  onDirChange: (dir: ReadingDir) => void
  /** `이어 읽기` / `읽기 시작`. */
  onResume: () => void
  onReadFirst: () => void
  onRescan: () => void
  /** True while a scan run is in flight — the rescan button is not re-armed. */
  scanning?: boolean
  /**
   * `SeriesDetail.error`, shown whenever `status !== "ok"` (ruling E-14,
   * design.md 화면 2). Ruling E-14 exists precisely so that a series the reader
   * cannot open a single page of "must not present as healthy".
   */
  error?: string | null
  /**
   * False when no volume can be opened. The two read buttons are then
   * `disabled` rather than dead: a series whose every volume is broken used to
   * render an enabled 읽기 시작 that did nothing at all when clicked.
   */
  canRead?: boolean
  /**
   * Non-blocking notice from `POST /api/series/{sid}/rescan`, i.e. the message
   * of a `409 conflict` (WP-10 acceptance 4).
   */
  notice?: string | null
}

const DIR_OPTIONS = [
  { value: 'ltr', label: 'L→R' },
  { value: 'rtl', label: 'R→L' },
] as const satisfies readonly { value: ReadingDir; label: string }[]

interface StatProps {
  label: string
  children: ReactNode
  className?: string
}

function Stat({ label, children, className }: StatProps) {
  return (
    <div className={className ?? 'flex flex-col gap-[2px]'}>
      <span className="text-3xs uppercase tracking-[.1em] text-ink-dim">{label}</span>
      {children}
    </div>
  )
}

export function SeriesHeader({
  series,
  dir,
  onDirChange,
  onResume,
  onReadFirst,
  onRescan,
  scanning = false,
  notice = null,
  error = null,
  canRead = true,
}: SeriesHeaderProps) {
  const cover = useCoverImage(series.id, {
    w: thumbWidthFor('seriesHero'),
    v: series.cover_cv,
    enabled: series.has_cover,
  })
  const roots = useRoots()
  const rootPath =
    roots.data?.items.find((candidate) => candidate.name === series.root_name)?.path ?? null
  const sourcePath = formatSourcePath(rootPath, series.path)
  const ratio = seriesProgressRatio(series.progress)
  const started = hasStarted(series.progress)

  return (
    /* E-32: the hero stops being a band with a 2px rule under it and becomes a
       card — `--radius-pill` (the prototype's 7px), `--color-surface`,
       `--shadow-md`, 16 of margin and 16 of padding. */
    <header className="m-4 flex flex-col gap-6 rounded-pill bg-surface p-4 shadow-md md:flex-row">
      {/* The fallback is always beneath the image, never swapped in for it, so
          a cover arriving late cannot shift the header (FR-LIB-008). */}
      <div className="relative h-[192px] w-[128px] flex-none overflow-hidden rounded-md bg-fill-track shadow-inset md:h-[264px] md:w-[176px]">
        <FallbackCover title={series.name} format={series.kind} size="hero" />
        {cover.status === 'ready' && (
          <img src={cover.url} alt="" className="absolute inset-0 h-full w-full object-cover" />
        )}
      </div>

      <div className="flex min-w-0 flex-1 flex-col gap-3">
        <h2 className="text-pretty" lang={textLang(series.name)}>
          {series.name}
        </h2>
        <p
          data-role="series-source-path"
          className="truncate whitespace-nowrap text-sm tracking-[.02em] text-ink-dim"
          title={sourcePath}
        >
          {sourcePath}
        </p>
        <Hr className="my-1" />

        {/* FR-IDX-010 / ruling E-14 / design.md 화면 2 "오류 상태": whenever the
            series status is not `ok` the reason is on screen, in the same
            solid-accent badge + reason pairing §2.5 allows for the 손상 family
            on a volume. Without it a series with nothing readable in it printed
            권 1 / 용량 0 KB / 진행률 0 % and looked perfectly healthy. */}
        {error !== null && error !== '' && (
          <p
            role="alert"
            data-role="series-error"
            className="flex min-w-0 items-center gap-2 border border-accent px-2 py-1"
          >
            {/* `--on-accent`, not `--color-bg` (E-32 §1): the teal accent is
                dark in both themes, and the ground on it is 1.48:1 in dark. */}
            <span className="flex-none bg-accent px-[6px] py-[2px] text-2xs tracking-[.08em] text-on-accent">
              손상
            </span>
            <span className="min-w-0 truncate text-xs text-accent-text" title={error}>
              {error}
            </span>
          </p>
        )}

        <div className="flex flex-wrap gap-4 gap-y-3 md:gap-8">
          <Stat label="권">
            <span className="font-heading text-[22px] font-extrabold tabular-nums">
              {formatCount(series.book_count)}
            </span>
          </Stat>
          <Stat label="용량">
            <span className="font-heading text-[22px] font-extrabold tabular-nums">
              {formatBytes(series.total_bytes)}
            </span>
          </Stat>
          <Stat label="형식">
            <span className="font-heading text-[22px] font-extrabold">
              {formatLabel(series.kind)}
            </span>
          </Stat>
          <Stat label="진행률" className="flex min-w-[200px] flex-col gap-1">
            <span className="font-heading text-[22px] font-extrabold tabular-nums text-accent-text">
              {formatPercent(ratio)}
            </span>
            {/* E-32: the stat strip's bar is the tallest of the three — 7px. */}
            <ProgressBar value={ratio} height={7} label={`${series.name} 진행률`} />
          </Stat>
        </div>

        <div className="flex-1" />

        <div className="flex flex-wrap items-center gap-2 gap-y-2">
          <Button variant="primary" className="gap-2" onClick={onResume} disabled={!canRead}>
            {/* Solid, not outlined: it is the one primary action on the screen
                and the only filled glyph, which is how it reads as one. */}
            <Play size={13} fill="currentColor" strokeWidth={0} aria-hidden={true} />
            {started ? '이어 읽기' : '읽기 시작'}
          </Button>
          <Button variant="secondary" className="gap-2" onClick={onReadFirst} disabled={!canRead}>
            <BookOpen size={13} aria-hidden={true} />
            처음부터 읽기
          </Button>
          <Button variant="secondary" className="gap-2" onClick={onRescan} disabled={scanning}>
            <RefreshCw size={13} aria-hidden={true} />
            이 시리즈 재스캔
          </Button>
          {scanning && <span className="text-xs text-accent-text">스캔 중</span>}
          <div className="flex-1" />
          <span className="text-3xs uppercase tracking-[.1em] text-ink-dim">읽기 방향</span>
          <Seg
            value={dir}
            options={DIR_OPTIONS}
            onChange={onDirChange}
            aria-label="읽기 방향"
            className="flex-none whitespace-nowrap"
          />
        </div>

        {notice !== null && notice !== '' && (
          <p role="status" className="text-xs text-accent-text">
            {notice}
          </p>
        )}
      </div>
    </header>
  )
}
