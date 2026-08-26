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

  // The case the row above could not see: every status there carries zero
  // pages, so it was testing "no pages" and reading as "not healthy". A damaged
  // container that kept a readable entry list is the shape nine archives in the
  // collection actually have, and E-54 opens it — the badge and the reason stay
  // exactly as they were, only the tile becomes a link.
  it('opens a damaged volume that still has pages (E-54)', () => {
    const salvaged = book({ status: 'error', page_count: 111, error: 'central directory unreadable' })
    expect(isOpenable(salvaged)).toBe(true)
    // Still flagged: opening it does not mean pretending it is healthy.
    expect(volumeStatus(salvaged)).toBe('error')
    expect(volumeBadge(salvaged)?.label).toBe('손상')
  })

  it('keeps an encrypted volume closed even if pages were somehow listed', () => {
    expect(isOpenable(book({ status: 'encrypted', page_count: 42 }))).toBe(false)
  })
})

describe('volumeBadge (FR-IDX-010)', () => {
  it('labels an encrypted volume 암호화 with the ZIP-password reason', () => {
    const badge = volumeBadge(book({ status: 'encrypted', error: null, page_count: 0 }))
    expect(badge).toEqual({ label: '암호화', reason: '비밀번호가 필요한 압축 파일', detail: null })
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
    // 42 / 187 = 22.4 % → floors to 22 %. Note both lengths are 187 in this
    // fixture, so this case cannot say *which* one is the denominator — the
    // three E-45 §6 cases below are the ones that can.
    expect(volumeProgressRatio(bookSummary)).toBeCloseTo(42 / 187)
    expect(volumeStateLabel(bookSummary)).toBe('22%')
    expect(volumeTone(bookSummary)).toBe('started')
  })

  /**
   * **E-45 §6 — the denominator is the index's current length.**
   *
   * `progress.page_count` is the *baseline* `isStale` compares against, and
   * E-45 §2 made the server preserve it across unacknowledged writes. From that
   * moment the baseline and the current length legitimately disagree, and the
   * screen must divide by the current one.
   *
   * Every fixture in this file used to carry 187 in *both* fields, which is why
   * `42 / 187` above passes with either denominator. These three set them apart
   * on purpose: swap `book.page_count` back for `progress.page_count` in
   * `volume.ts` and all three go red.
   */
  it('divides by the index length, not the baseline, when the file grew (E-45 §6)', () => {
    // 10 pages became 190; the reader has seen 10 of them. That is 5 %, not 완독.
    const grew = book({
      page_count: 190,
      progress: { ...seriesProgressRow(), last_page: 10, page_count: 10 },
    })
    expect(volumeProgressRatio(grew)).toBeCloseTo(10 / 190)
    expect(volumeStateLabel(grew)).toBe('5%')
    expect(volumeTone(grew)).toBe('started')
  })

  /**
   * **The ratio is 1.0 and the word is not `완독`** (E-45 §6, 따름정리의 따름정리).
   *
   * This case is where §6's own correction became visible. It asserted `완독`
   * and — alone among the cases here — left the tone unasserted, which is how a
   * three-way disagreement got *recorded* by a green test rather than caught by
   * it: `완독` with a terminal badge and finished ink in the list, `읽는 중` on
   * the grid, `읽음 표시` on the button of both. The number stays 1.0; the word,
   * the tone and the control all answer `progress.completed`.
   */
  it('reads 100%, not 완독, for a shrunk volume nobody marked read', () => {
    // 190 pages became 10 and the server clamps the reader to page 10: he is at
    // the end of the file that exists. The old baseline would call that 5 %.
    const shrank = book({
      page_count: 10,
      progress: { ...seriesProgressRow(), last_page: 10, page_count: 190, completed: false },
    })
    expect(volumeProgressRatio(shrank)).toBe(1)
    expect(volumeStateLabel(shrank)).toBe('100%')
    expect(volumeTone(shrank)).toBe('started')
  })

  it('says 완독 for the same ratio once the volume really is completed', () => {
    // The discriminator against "1.0 never says 완독": the word is not gone, it
    // just answers `completed` instead of the fraction.
    const done = book({
      page_count: 10,
      progress: { ...seriesProgressRow(), last_page: 10, page_count: 190, completed: true },
    })
    expect(volumeProgressRatio(done)).toBe(1)
    expect(volumeStateLabel(done)).toBe('완독')
    expect(volumeTone(done)).toBe('finished')
  })

  it('is 0, not a full bar, for a volume with no pages left but a recorded row', () => {
    // The guard has to watch the *denominator*. `last_page / 0` is Infinity, and
    // the `Math.min(1, …)` clamp would silently render that as a finished volume.
    const gone = book({
      status: 'error',
      page_count: 0,
      progress: { ...seriesProgressRow(), last_page: 42, page_count: 187 },
    })
    expect(volumeProgressRatio(gone)).toBe(0)
    expect(volumeStateLabel(gone)).toBe('ERR')
    expect(volumeTone(gone)).toBe('broken')
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

describe('seriesProgressRatio (FR-STT-002, as amended by E-47)', () => {
  /**
   * **This block used to assert the opposite, and the reversal is the ruling.**
   *
   * Before E-47 the ratio was `books_completed / books_total`, computed here so
   * that "완독 수 기준" was assertable from the screen — and one of these cases
   * was a guard named "ignores the server percent field, so a page-weighted
   * server cannot move it". E-47 makes the server page-weighted on purpose, so
   * the guard now describes the defect rather than the requirement: the parts
   * (`pages_read`, `pages_total`) are not on the wire, so anything computed here
   * could only be the old definition under the new name, and the library's card
   * — which reads `percent` directly — would disagree with the detail header
   * about the same series.
   */
  it('follows the server percent, which is now page-weighted', () => {
    // 6 완독 of 25 volumes, but the reader is also part-way through a seventh:
    // the server says 26.4 % and the screen says 26.4 %.
    const partial = progressOf({ books_total: 25, books_completed: 6, books_started: 1, percent: 26.4 })
    expect(seriesProgressRatio(partial)).toBeCloseTo(0.264)
  })

  it('does not re-derive the old completed-count ratio', () => {
    // The pre-E-47 formula would say 6/25 = 0.24 here. If this ever passes at
    // 0.24 again, the two screens have gone back to disagreeing.
    const partial = progressOf({ books_total: 25, books_completed: 6, percent: 99 })
    expect(seriesProgressRatio(partial)).toBeCloseTo(0.99)
  })

  it('is 0 for an empty series rather than NaN', () => {
    expect(seriesProgressRatio(progressOf({ books_total: 0, books_completed: 0, percent: 0 }))).toBe(
      0,
    )
  })

  it('clamps a value from outside the contract', () => {
    expect(seriesProgressRatio(progressOf({ percent: 140 }))).toBe(1)
    expect(seriesProgressRatio(progressOf({ percent: -5 }))).toBe(0)
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
