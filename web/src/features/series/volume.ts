/**
 * Pure derivations for the series-detail screen.
 *
 * Everything here is a function of the frozen contract's `BookSummary` /
 * `SeriesProgress` and nothing else, so the three renderers (tile, row, header)
 * cannot disagree about what "broken", "started" or "42 %" means.
 *
 * Two requirements live in this file:
 *
 *  * **FR-IDX-010** — a volume that cannot be opened is surfaced with a badge
 *    and a reason, and is not clickable. `status` is the server's verdict
 *    (arch §4.11); the UI never re-derives it from the error string.
 *  * **FR-STT-002** — series progress is aggregated over *completed volumes*,
 *    `books_completed / books_total`. It is computed here rather than read from
 *    `SeriesProgress.percent` so the screen states the requirement itself: a
 *    server that started reporting page-weighted progress would move `percent`
 *    but must not move this number.
 */

import { formatPercent, formatProgressLabel } from '../../lib/format'
import type { BookSummary, ItemStatus, SeriesProgress } from '../../api/types'

/** How a volume box is drawn (ui-spec §5.3's `X`). */
export type VolumeTone = 'broken' | 'finished' | 'started' | 'unread'

export interface VolumeBadge {
  /** `암호화` / `손상` — ui-spec §5.3. */
  label: string
  /** The Korean reason line under the badge. */
  reason: string
  /**
   * The server's own `books.error` (arch §4.11), e.g.
   * `zip: end of central directory not found`. English and diagnostic, so it
   * rides along as a `title` rather than replacing the Korean reason the
   * design specifies.
   */
  detail: string | null
}

/**
 * The two statuses the design names, plus the two the contract can produce that
 * it does not (`unsupported` is a `nopdf` build, `empty` is an archive with no
 * qualifying entries — D-10's container-of-ZIPs is the real case). Leaving
 * those two unlabelled would render a dead tile with no explanation, which is
 * the exact failure FR-IDX-010 exists to prevent.
 */
const BADGES: Record<Exclude<ItemStatus, 'ok'>, { label: string; reason: string }> = {
  encrypted: { label: '암호화', reason: '비밀번호가 필요한 ZIP' },
  error: { label: '손상', reason: '중앙 디렉터리 손상' },
  unsupported: { label: '미지원', reason: '이 빌드에서 지원하지 않는 형식' },
  empty: { label: '비어 있음', reason: '읽을 수 있는 페이지가 없습니다' },
}

/**
 * The effective status.
 *
 * `ok` with zero pages is treated as `empty`: the server should not produce it,
 * but a tile that opens a viewer with nothing in it is a worse failure than a
 * badge.
 */
export function volumeStatus(book: BookSummary): ItemStatus {
  if (book.status === 'ok' && book.page_count <= 0) return 'empty'
  return book.status
}

/** FR-IDX-010: only an `ok` volume with pages may be opened. */
export function isOpenable(book: BookSummary): boolean {
  return volumeStatus(book) === 'ok'
}

/** `null` for a healthy volume; the badge + reason otherwise. */
export function volumeBadge(book: BookSummary): VolumeBadge | null {
  const status = volumeStatus(book)
  if (status === 'ok') return null
  const canned = BADGES[status]
  return { label: canned.label, reason: canned.reason, detail: book.error }
}

/**
 * 0..1 for one volume: 1 when completed, else `last_page / book.page_count`.
 *
 * **E-45 §6 — the denominator is `book.page_count`, never
 * `progress.page_count`.** The recorded count is the *baseline* `isStale`
 * compares against (arch §3.4), and since E-45 §2 stopped every write from
 * re-baselining it, the two values legitimately disagree: a 10-page file that
 * grew to 190 still carries `progress.page_count = 10`. Dividing by the
 * baseline would call that reader 100 % done when he has seen 5 % — and the
 * `Math.min(1, …)` clamp below is exactly why nothing ever crashed over it.
 * The shrinking direction agrees: 190 → 10 with the reader clamped to page 10
 * *is* 100 %. Only the index's current length answers both.
 *
 * The `<= 0` guard therefore watches `book.page_count` too — a `status != "ok"`
 * volume has no pages, and `last_page / 0` is `Infinity`, which the clamp would
 * quietly turn into a full bar.
 */
export function volumeProgressRatio(book: BookSummary): number {
  const progress = book.progress
  if (progress === null) return 0
  if (progress.completed) return 1
  if (book.page_count <= 0) return 0
  return Math.max(0, Math.min(1, progress.last_page / book.page_count))
}

export function volumeTone(book: BookSummary): VolumeTone {
  if (!isOpenable(book)) return 'broken'
  const progress = book.progress
  if (progress === null) return 'unread'
  return progress.completed ? 'finished' : 'started'
}

/**
 * The 용량 of one volume (prd FR-LIB-009, 필수).
 *
 * **Not** `total_bytes`. The contract (arch §4.4 / §7.3) defines that field as
 * the sum of *uncompressed page* bytes, which is legitimately `0` for a PDF —
 * whose pages are rendered on demand, not stored — and `0` for any volume with
 * no readable pages. Rendering it directly printed `0 KB` on all nine 미생 PDF
 * volumes and on every unreadable one. `file_size` is the container size and is
 * `0` only for `kind:"dir"`, where the page bytes *are* the bytes on disk, so
 * the two compose into one rule. This mirrors the server's `diskBytes`
 * roll-up into `series.total_bytes` exactly, which is what keeps the header
 * total equal to the sum of the rows beneath it.
 */
export function volumeBytes(book: BookSummary): number {
  return book.file_size > 0 ? book.file_size : book.total_bytes
}

/**
 * The 5th list cell / the tile's state: `ERR` · `완독` · `34%` · `—`.
 *
 * **`완독` follows `completed`, never the ratio** (E-45 §6, 따름정리의 따름정리).
 * §6 made the denominator the index's *current* length, which newly made a ratio
 * of exactly 1.0 reachable for a volume that is **not** completed: a 190-page
 * file that shrank to 10 with the reader clamped to page 10 has honestly read
 * all of what exists. The number stays 1.0 — that is the truthful value, and §6
 * ruled on it — but the *word* is a state, and the state is
 * `progress.completed`. Letting the ratio speak it split one book across three
 * readings on two screens: `완독` in the list with a terminal badge and a
 * `tone='done'` bar, `읽는 중` on the grid, and a `읽음 표시` button on both,
 * which is exactly the state/action confusion **E-12** forbids.
 *
 * It asks `volumeTone` rather than `book.progress.completed` directly so that
 * the label and the colour cannot answer the same question differently — the
 * drift this exists to close is two surfaces reading two fields.
 */
export function volumeStateLabel(book: BookSummary): string {
  if (!isOpenable(book)) return 'ERR'
  const ratio = volumeProgressRatio(book)
  if (ratio >= 1 && volumeTone(book) !== 'finished') return formatPercent(ratio)
  return formatProgressLabel(ratio)
}

/**
 * FR-STT-002. Deliberately **not** `SeriesProgress.percent / 100`: the
 * requirement is "완독 수 기준", and computing it here is what makes that
 * assertable from the screen.
 */
export function seriesProgressRatio(progress: SeriesProgress): number {
  if (progress.books_total <= 0) return 0
  return Math.max(0, Math.min(1, progress.books_completed / progress.books_total))
}

/** `처음부터 읽기` — the first volume that can actually be opened. */
export function firstOpenableBook(books: readonly BookSummary[]): BookSummary | null {
  return books.find((book) => isOpenable(book)) ?? null
}

export interface ResumeTarget {
  book: BookSummary
  /** 1-based. */
  page: number
}

/**
 * `이어 읽기`.
 *
 * `SeriesProgress.last_book_id` is the server's answer and wins; it is checked
 * against the volume list because a book can go `error` between the progress
 * row being written and the rescan that follows, and resuming into an
 * unopenable volume is a dead end. Falls back to the first openable volume.
 */
export function resumeTarget(
  books: readonly BookSummary[],
  progress: SeriesProgress,
): ResumeTarget | null {
  const last = progress.last_book_id
  if (last !== null) {
    const book = books.find((candidate) => candidate.id === last)
    if (book !== undefined && isOpenable(book)) {
      const page = book.progress?.last_page ?? progress.last_page ?? 1
      return { book, page: Math.max(1, page) }
    }
  }
  const first = firstOpenableBook(books)
  if (first === null) return null
  return { book: first, page: Math.max(1, first.progress?.last_page ?? 1) }
}

/** True once any volume has been opened — decides 이어 읽기 vs 읽기 시작. */
export function hasStarted(progress: SeriesProgress): boolean {
  return progress.books_completed > 0 || progress.books_started > 0
}
