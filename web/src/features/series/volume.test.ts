/**
 * The pure derivations behind screen 2. Every case here is a requirement:
 * FR-IDX-010 (which volumes are unopenable, and what they say), FR-STT-002
 * (series progress aggregates completed volumes) and E-5 (duplicates are data,
 * not a bug).
 */

import { describe, expect, it } from 'vitest'

import { bookSummary, brokenBookSummary, seriesProgress } from '../../api/fixtures'
import type { BookSummary, SeriesProgress } from '../../api/types'
import {
  firstOpenableBook,
  hasStarted,
  isOpenable,
  resumeTarget,
  seriesProgressRatio,
  volumeBadge,
  volumeBytes,
  volumeProgressRatio,
  volumeStateLabel,
  volumeStatus,
  volumeTone,
} from './volume'

function book(overrides: Partial<BookSummary>): BookSummary {
  return { ...bookSummary, ...overrides }
}

function progressOf(overrides: Partial<SeriesProgress>): SeriesProgress {
  return { ...seriesProgress, ...overrides }
}

describe('volumeStatus / isOpenable', () => {
  it('treats an ok volume with pages as openable', () => {
    expect(isOpenable(book({ status: 'ok', page_count: 187 }))).toBe(true)
  })

  it.each(['error', 'encrypted', 'unsupported', 'empty'] as const)(
    'refuses to open a %s volume (FR-IDX-010)',
    (status) => {
      expect(isOpenable(book({ status, page_count: 0 }))).toBe(false)
    },
  )

  it('demotes an ok volume with zero pages to empty rather than opening nothing', () => {
    const zero = book({ status: 'ok', page_count: 0 })
    expect(volumeStatus(zero)).toBe('empty')
    expect(isOpenable(zero)).toBe(false)
  })
})

describe('volumeBadge (FR-IDX-010)', () => {
  it('labels an encrypted volume 암호화 with the ZIP-password reason', () => {
    const badge = volumeBadge(book({ status: 'encrypted', error: null, page_count: 0 }))
    expect(badge).toEqual({ label: '암호화', reason: '비밀번호가 필요한 ZIP', detail: null })
  })

  it('labels a truncated volume 손상 and carries the server message as detail', () => {
    const badge = volumeBadge(brokenBookSummary)
    expect(badge?.label).toBe('손상')
    expect(badge?.reason).toBe('중앙 디렉터리 손상')
    // arch §4.11's own message, kept for diagnosis rather than shown as the
    // Korean reason line.
    expect(badge?.detail).toBe('reading central directory: unexpected EOF')
  })

  it('returns null for a healthy volume', () => {
    expect(volumeBadge(bookSummary)).toBeNull()
  })
})

describe('volumeProgressRatio / volumeStateLabel', () => {
  it('is 1 for a completed volume regardless of last_page', () => {
    const done = book({
      progress: { ...seriesProgressRow(), last_page: 3, page_count: 187, completed: true },
    })
    expect(volumeProgressRatio(done)).toBe(1)
    expect(volumeStateLabel(done)).toBe('완독')
    expect(volumeTone(done)).toBe('finished')
  })

  it('is last_page / page_count while in progress', () => {
    // 42 / 187 = 22.4 % → floors to 22 %.
    expect(volumeProgressRatio(bookSummary)).toBeCloseTo(42 / 187)
    expect(volumeStateLabel(bookSummary)).toBe('22%')
    expect(volumeTone(bookSummary)).toBe('started')
  })

  it('is an em dash when the volume was never opened', () => {
    const fresh = book({ progress: null })
    expect(volumeProgressRatio(fresh)).toBe(0)
    expect(volumeStateLabel(fresh)).toBe('—')
    expect(volumeTone(fresh)).toBe('unread')
  })

  it('is ERR for an unopenable volume, never a percentage', () => {
    expect(volumeStateLabel(brokenBookSummary)).toBe('ERR')
    expect(volumeTone(brokenBookSummary)).toBe('broken')
  })
})

describe('volumeBytes (prd FR-LIB-009 용량)', () => {
  it('uses the container size for an archive', () => {
    expect(volumeBytes(book({ kind: 'zip', file_size: 24_500_000, total_bytes: 23_900_000 }))).toBe(
      24_500_000,
    )
  })

  it('uses the page bytes for a folder, whose file_size is 0 by definition', () => {
    // arch §4.4: `file_size` is "container size; 0 for kind='dir'". A directory
    // has no container, so its pages ARE its bytes on disk.
    expect(volumeBytes(book({ kind: 'dir', file_size: 0, total_bytes: 24_500_000 }))).toBe(
      24_500_000,
    )
  })

  it('is the document size for a PDF, whose page bytes are always 0', () => {
    // The regression: a PDF's pages are rendered on demand, never stored, so
    // `total_bytes` is 0 by construction and rendering it printed `0 KB` on
    // every PDF volume, series header and library row in the product.
    expect(volumeBytes(book({ kind: 'pdf', file_size: 36_201_692, total_bytes: 0 }))).toBe(
      36_201_692,
    )
  })

  it('is the file size for an unreadable volume, which has no page rows at all', () => {
    expect(volumeBytes(brokenBookSummary)).toBe(brokenBookSummary.file_size)
    expect(brokenBookSummary.total_bytes).toBe(0)
  })

  it('is 0 only when the server knows neither number', () => {
    expect(volumeBytes(book({ file_size: 0, total_bytes: 0 }))).toBe(0)
  })
})

describe('seriesProgressRatio (FR-STT-002)', () => {
  it('aggregates over completed volumes, not started ones', () => {
    // 6 완독 of 25, plus one merely started — the started one must not count.
    expect(seriesProgressRatio(progressOf({ books_completed: 6, books_started: 1 }))).toBeCloseTo(
      6 / 25,
    )
  })

  it('ignores the server percent field, so a page-weighted server cannot move it', () => {
    const lying = progressOf({ books_total: 25, books_completed: 6, percent: 99 })
    expect(seriesProgressRatio(lying)).toBeCloseTo(0.24)
  })

  it('is 0 for an empty series rather than NaN', () => {
    expect(seriesProgressRatio(progressOf({ books_total: 0, books_completed: 0 }))).toBe(0)
  })
})

describe('firstOpenableBook / resumeTarget', () => {
  const broken = book({ id: 'aaaaaaaaaaaaaaaa', status: 'error', page_count: 0, progress: null })
  const healthy = book({ id: 'bbbbbbbbbbbbbbbb', progress: null })

  it('skips unopenable volumes when starting from the beginning', () => {
    expect(firstOpenableBook([broken, healthy])?.id).toBe('bbbbbbbbbbbbbbbb')
  })

  it('resumes into the series last_book_id at its own last_page', () => {
    const target = resumeTarget([broken, bookSummary], progressOf({ last_book_id: bookSummary.id }))
    expect(target?.book.id).toBe(bookSummary.id)
    expect(target?.page).toBe(42)
  })

  it('falls back to the first openable volume when last_book_id went broken', () => {
    const target = resumeTarget(
      [broken, healthy],
      progressOf({ last_book_id: broken.id, last_page: 12 }),
    )
    expect(target?.book.id).toBe('bbbbbbbbbbbbbbbb')
    expect(target?.page).toBe(1)
  })

  it('has no target at all when every volume is broken', () => {
    expect(resumeTarget([broken], progressOf({ last_book_id: broken.id }))).toBeNull()
  })
})

describe('hasStarted', () => {
  it('is false only when nothing has been opened', () => {
    expect(hasStarted(progressOf({ books_completed: 0, books_started: 0 }))).toBe(false)
    expect(hasStarted(progressOf({ books_completed: 0, books_started: 1 }))).toBe(true)
    expect(hasStarted(progressOf({ books_completed: 2, books_started: 0 }))).toBe(true)
  })
})

/** A `Progress` row shaped from the fixture, for the completed-volume cases. */
function seriesProgressRow() {
  return {
    book_id: bookSummary.id,
    series_id: bookSummary.series_id,
    last_page: 1,
    page_count: 187,
    completed: false,
    started_at: 1_753_600_100,
    updated_at: 1_753_600_500,
    stale: false,
  }
}
