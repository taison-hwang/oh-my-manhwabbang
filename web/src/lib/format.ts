/**
 * User-visible formatters.
 *
 * Every string produced here is Korean copy from the ui-spec §9 catalogue, kept
 * verbatim. The formatting conventions are the ones stated at the bottom of
 * that section:
 *
 *   sizes         `X.X GB` at >= 1 GB, else `NNN MB`
 *   volume counts `NN권`
 *   page counters `N / Mp` (continue card) or `N / M` (viewer)
 *   dates         `YYYY-MM-DD`
 *   percentages   `NN%`
 *   finished      `완독`
 *   unread        `—`
 *
 * Sizes below 1 MB fall back to `NNN KB` — the catalogue's rule bottoms out at
 * MB, and `0 MB` next to a real file reads as a bug.
 */

/**
 * **Decimal** units (ruling E-11): `1 MB = 1000²`, not 1024².
 *
 * The first cut computed in binary and labelled in SI, so every size in the
 * product read ~4.6 % low — a 799 000 000-byte series printed `762 MB` where
 * the prototype (the design reference) prints `799 MB`. Both conventions are
 * defensible; silently mixing them is not, and prd 5.2 UI-002 puts 총 용량 in
 * front of the user. Decimal is what the prototype does and what a reader
 * expects of a media size, so decimal wins.
 */
const KB = 1000
const MB = KB * 1000
const GB = MB * 1000
const TB = GB * 1000

/** Thousands separators, locale-independent so tests are stable. */
export function formatCount(n: number): string {
  const sign = n < 0 ? '-' : ''
  const digits = Math.abs(Math.trunc(n)).toString()
  let out = ''
  for (let i = 0; i < digits.length; i++) {
    if (i > 0 && (digits.length - i) % 3 === 0) out += ','
    out += digits[i] ?? ''
  }
  return sign + out
}

/**
 * `4.4 GB` · `799 MB` · `4.9 TB` · `12 KB` · `0 KB`.
 *
 * Decimal units with decimal labels (E-11): 799 000 000 B is `799 MB`.
 */
export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return '—'
  // The unit is chosen from the exact value but printed from a rounded one, and
  // rounding can carry past the threshold the unit was chosen for: 1 GB − 1 B
  // is 999.999999 MB, which must read `1.0 GB`, not `1,000 MB`. So each branch
  // re-checks its own output and promotes.
  if (bytes >= TB) return `${(bytes / TB).toFixed(1)} TB`
  if (bytes >= GB) {
    const gb = (bytes / GB).toFixed(1)
    return Number(gb) >= 1000 ? `${(bytes / TB).toFixed(1)} TB` : `${gb} GB`
  }
  if (bytes >= MB) {
    const mb = Math.round(bytes / MB)
    return mb >= 1000 ? `${(bytes / GB).toFixed(1)} GB` : `${formatCount(mb)} MB`
  }
  const kb = Math.round(bytes / KB)
  return kb >= 1000 ? `${formatCount(Math.round(bytes / MB))} MB` : `${formatCount(kb)} KB`
}

/**
 * The **원본 경로** shown under the series-detail H1 (prd 5.2 UI-002,
 * design.md 화면 2, ui-spec §5.1).
 *
 * `SeriesSummary.path` is *root-relative*, and for a top-level series — which
 * is every series, because prd 1.3 defines one as a direct child of a root —
 * that string is byte-identical to `SeriesSummary.name`. Printing it alone
 * therefore renders the title twice and tells the reader nothing. The secondary
 * line is specified as the source path, so it is the root's absolute path with
 * the root-relative path appended: the one place in the product outside
 * Settings where a filesystem path appears (UI 5.3).
 *
 * The separator is taken from the *root*, so a Windows root
 * (`D:\books` + `시리즈`) does not come back as a mixed `D:\books/시리즈`.
 * Falls back to the relative path when the roots list has not loaded yet or the
 * series' root is unknown — a stale duplicate beats an empty line.
 */
export function formatSourcePath(rootPath: string | null | undefined, relPath: string): string {
  const rel = relPath.trim()
  const root = (rootPath ?? '').trim()
  if (root === '') return rel
  const sep = !root.includes('/') && root.includes('\\') ? '\\' : '/'
  const base = root.endsWith('/') || root.endsWith('\\') ? root.slice(0, -1) : root
  if (rel === '') return base
  return `${base}${sep}${rel.replaceAll('/', sep)}`
}

/** `YYYY-MM-DD` in the viewer's local zone, from a Unix **seconds** stamp. */
export function formatDate(unixSeconds: number): string {
  if (!Number.isFinite(unixSeconds)) return '—'
  const d = new Date(unixSeconds * 1000)
  const pad = (n: number): string => n.toString().padStart(2, '0')
  return `${d.getFullYear().toString().padStart(4, '0')}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
}

/** `34%` from a 0..1 ratio. Rounds toward zero so 99.6 % is not shown as 완독. */
export function formatPercent(ratio: number): string {
  if (!Number.isFinite(ratio)) return '—'
  const pct = Math.max(0, Math.min(100, Math.floor(ratio * 100)))
  return `${pct.toString()}%`
}

/** `22권` */
export function formatVolumeCount(n: number): string {
  return `${formatCount(n)}권`
}

/** `24개 시리즈` — the section header's result count. */
export function formatSeriesCount(n: number): string {
  return `${formatCount(n)}개 시리즈`
}

/** `5개` — the 이어보기 header count. */
export function formatItemCount(n: number): string {
  return `${formatCount(n)}개`
}

/** `214p` — a volume's page count. */
export function formatPageCount(n: number): string {
  return `${formatCount(n)}p`
}

/** `10 / 214p` — the continue card's counter. */
export function formatContinueCounter(page: number, total: number): string {
  return `${formatCount(page)} / ${formatCount(total)}p`
}

/** `12 / 214` — the viewer's counter. */
export function formatViewerCounter(page: number, total: number): string {
  return `${formatCount(page)} / ${formatCount(total)}`
}

/**
 * The progress cell of the library list and the volume rows.
 *
 * `완독` when finished, `34%` while in progress, `—` when untouched.
 */
export function formatProgressLabel(ratio: number): string {
  if (!Number.isFinite(ratio) || ratio <= 0) return '—'
  if (ratio >= 1) return '완독'
  return formatPercent(ratio)
}

/**
 * The FR-VWR-012 manual toggle's label — **always an action, never a state**
 * (ruling E-12).
 *
 * The first cut labelled the "clear it" direction `안읽음`, which is the name of
 * a *state*: a row already reading `완독` then printed `완독 안읽음`, two bare
 * accent words that read as a contradiction rather than as "this is done" plus
 * "undo that". Both directions are now verbs in the same shape — 표시 sets it,
 * 해제 clears it — and both are rendered with real button chrome, so the state
 * badge and the control can never be mistaken for each other.
 */
export const MARK_READ_ACTION = '읽음 표시'
export const CLEAR_READ_ACTION = '읽음 해제'

export function readToggleLabel(completed: boolean): string {
  return completed ? CLEAR_READ_ACTION : MARK_READ_ACTION
}

/** Minutes elapsed, floored at 0. Both stamps are Unix **seconds**. */
export function minutesSince(unixSeconds: number, nowMs: number = Date.now()): number {
  return Math.max(0, Math.floor((nowMs / 1000 - unixSeconds) / 60))
}

/**
 * A book or series kind, in either wire spelling.
 *
 * C-4 splits them: a *series* is `folder`, a *book* inside it is `dir`. The
 * badge text is `FOLDER` for both — the distinction matters to the API, not to
 * a reader.
 */
export type FormatValue = 'zip' | 'dir' | 'folder' | 'pdf'

/** The `ZIP` / `FOLDER` / `PDF` badge text (FR-LIB-009). */
export function formatLabel(format: FormatValue): string {
  return format === 'dir' || format === 'folder' ? 'FOLDER' : format.toUpperCase()
}

/** The subset of `ScanStatus` (arch §7.10) the sidebar indicator reads. */
export interface ScanLabelInput {
  state: 'idle' | 'walking' | 'indexing' | 'covers' | 'cancelling'
  done: number
  total: number
  finished_at: number | null
}

/**
 * The sidebar footer's scan label.
 *
 * Catalogue: `scanRun` = `스캔 중`, `scanIdle` = `스캔 대기 — {n}분 전 완료`.
 * The prototype renders the running form with the counter appended
 * (`스캔 중 1,842 / 2,250`), and the idle form without the suffix when no run
 * has ever finished.
 */
export function formatScanLabel(scan: ScanLabelInput, nowMs: number = Date.now()): string {
  if (scan.state !== 'idle') {
    if (scan.total > 0) {
      return `스캔 중 ${formatCount(scan.done)} / ${formatCount(scan.total)}`
    }
    return '스캔 중'
  }
  if (scan.finished_at === null) return '스캔 대기'
  return `스캔 대기 — ${formatCount(minutesSince(scan.finished_at, nowMs))}분 전 완료`
}

/** The 0–100 integer percentage the top-bar scan bar and its label both use. */
export function scanPercent(scan: Pick<ScanLabelInput, 'done' | 'total'>): number {
  if (scan.total <= 0) return 0
  return Math.max(0, Math.min(100, Math.floor((scan.done / scan.total) * 100)))
}
