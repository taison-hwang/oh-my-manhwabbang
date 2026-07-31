import { describe, expect, it } from 'vitest'

import {
  CLEAR_READ_ACTION,
  MARK_READ_ACTION,
  formatBytes,
  formatContinueCounter,
  formatCount,
  formatDate,
  formatItemCount,
  formatPageCount,
  formatPercent,
  formatProgressLabel,
  formatScanLabel,
  formatSeriesCount,
  formatSourcePath,
  formatViewerCounter,
  formatVolumeCount,
  minutesSince,
  readToggleLabel,
  scanPercent,
} from './format'

describe('formatBytes (ui-spec §9: X.X GB at >= 1 GB, else NNN MB)', () => {
  /**
   * Ruling **E-11**. The first cut divided by 1024 and printed `MB`, so every
   * size in the product read ~4.6 % low. These four are the discriminating
   * cases: each one renders differently under the two conventions, so a
   * regression to binary maths fails here rather than in a screenshot.
   */
  it('uses decimal units, 1 MB = 1000² (E-11)', () => {
    // The escalation's own example. Binary printed `762 MB`.
    expect(formatBytes(799_000_000)).toBe('799 MB')
    // The fixture root total. Binary printed `5.1 GB`.
    expect(formatBytes(5_472_000_000)).toBe('5.5 GB')
    // The fixture thumbnail cache. Binary printed `216 MB`.
    expect(formatBytes(226_000_000)).toBe('226 MB')
    // A round terabyte. Binary printed `931.3 GB`.
    expect(formatBytes(1_000_000_000_000)).toBe('1.0 TB')
  })

  it('matches the sizes rendered in the reference screenshots', () => {
    expect(formatBytes(4.4 * 1000 ** 3)).toBe('4.4 GB')
    expect(formatBytes(8.9 * 1000 ** 3)).toBe('8.9 GB')
    expect(formatBytes(799 * 1000 ** 2)).toBe('799 MB')
    expect(formatBytes(41 * 1000 ** 2)).toBe('41 MB')
  })

  it('scales to TB, which the roots panel really shows (21 · 4.9 TB)', () => {
    expect(formatBytes(4.9 * 1000 ** 4)).toBe('4.9 TB')
  })

  it('does not round a small file down to "0 MB"', () => {
    expect(formatBytes(3 * 1000)).toBe('3 KB')
    expect(formatBytes(0)).toBe('0 KB')
  })

  it('promotes the unit when rounding carries past it', () => {
    // 1 GB − 1 B is 999.999999 MB: rounding it inside the MB branch printed
    // "1,000 MB", a unit the catalogue does not have.
    expect(formatBytes(1000 ** 3 - 1)).toBe('1.0 GB')
    expect(formatBytes(1000 ** 4 - 1)).toBe('1.0 TB')
    expect(formatBytes(1000 ** 2 - 1)).toBe('1 MB')
    // The thresholds themselves are untouched.
    expect(formatBytes(1000 ** 3)).toBe('1.0 GB')
    expect(formatBytes(999 * 1000 ** 2)).toBe('999 MB')
  })

  it('refuses to invent a size for a nonsense input', () => {
    expect(formatBytes(-1)).toBe('—')
    expect(formatBytes(Number.NaN)).toBe('—')
  })
})

describe('formatSourcePath (prd 5.2 UI-002 — 원본 경로, not the title again)', () => {
  const ROOT = '/mnt/big-data/pds/taison-data/02. books/01. mangga'

  /**
   * The defect this pins: `SeriesSummary.path` is root-relative, and prd 1.3
   * makes every series a *direct child* of a root, so the relative path equals
   * the display name for every series in the collection. The subtitle printed
   * the H1 back verbatim.
   */
  it('is not the series name again for a top-level series', () => {
    const name = '[만화] 이누야샤 01~56권 완결'
    expect(formatSourcePath(ROOT, name)).toBe(`${ROOT}/${name}`)
    expect(formatSourcePath(ROOT, name)).not.toBe(name)
  })

  it('does not double the separator when the root path carries one', () => {
    expect(formatSourcePath(`${ROOT}/`, '군계')).toBe(`${ROOT}/군계`)
  })

  it('keeps a Windows root in Windows separators (NFR-OPS-003)', () => {
    expect(formatSourcePath('D:\\books', '만화/1권')).toBe('D:\\books\\만화\\1권')
  })

  it('falls back to the relative path while the roots list is still loading', () => {
    expect(formatSourcePath(null, '군계')).toBe('군계')
    expect(formatSourcePath(undefined, '군계')).toBe('군계')
    expect(formatSourcePath('', '군계')).toBe('군계')
  })

  it('never leaves a trailing separator when the series is the root itself', () => {
    expect(formatSourcePath(ROOT, '')).toBe(ROOT)
  })
})

describe('readToggleLabel (FR-VWR-012, ruling E-12)', () => {
  /**
   * E-12: `완독` (state) immediately followed by `안읽음` (action) reads as a
   * contradiction. Both directions of the toggle must name what pressing it
   * *does*, never what the volume currently *is*.
   */
  it('names an action in both directions, never a state', () => {
    expect(readToggleLabel(false)).toBe('읽음 표시')
    expect(readToggleLabel(true)).toBe('읽음 해제')
    expect(readToggleLabel(true)).not.toBe('안읽음')
  })

  it('never collides with the 완독 state badge that sits beside it', () => {
    expect([MARK_READ_ACTION, CLEAR_READ_ACTION]).not.toContain(formatProgressLabel(1))
    expect([MARK_READ_ACTION, CLEAR_READ_ACTION]).not.toContain(formatProgressLabel(0))
  })
})

describe('counts and counters', () => {
  it('groups thousands the way the scan indicator does', () => {
    expect(formatCount(1842)).toBe('1,842')
    expect(formatCount(2250)).toBe('2,250')
    expect(formatCount(999)).toBe('999')
    expect(formatCount(1000000)).toBe('1,000,000')
    expect(formatCount(0)).toBe('0')
  })

  it('uses the catalogue suffixes', () => {
    expect(formatVolumeCount(22)).toBe('22권')
    expect(formatSeriesCount(24)).toBe('24개 시리즈')
    expect(formatItemCount(5)).toBe('5개')
    expect(formatPageCount(214)).toBe('214p')
  })

  it('distinguishes the continue card (N / Mp) from the viewer (N / M)', () => {
    expect(formatContinueCounter(10, 214)).toBe('10 / 214p')
    expect(formatViewerCounter(12, 214)).toBe('12 / 214')
  })
})

describe('progress', () => {
  it('floors rather than rounds, so 99.6 % never reads as 완독', () => {
    expect(formatPercent(0.996)).toBe('99%')
    expect(formatPercent(0.34)).toBe('34%')
    expect(formatPercent(1)).toBe('100%')
  })

  it('maps the three library-list states of ui-spec §4.5', () => {
    expect(formatProgressLabel(0)).toBe('—')
    expect(formatProgressLabel(0.34)).toBe('34%')
    expect(formatProgressLabel(1)).toBe('완독')
    expect(formatProgressLabel(1.2)).toBe('완독')
  })
})

describe('formatDate', () => {
  it('renders YYYY-MM-DD from a Unix seconds stamp', () => {
    // 2016-11-02T12:00:00Z, read in the local zone the app runs in.
    const unix = Date.UTC(2016, 10, 2, 12, 0, 0) / 1000
    expect(formatDate(unix)).toBe('2016-11-02')
  })

  it('zero-pads single-digit months and days', () => {
    expect(formatDate(Date.UTC(2013, 9, 8, 12) / 1000)).toBe('2013-10-08')
    expect(formatDate(Date.UTC(2015, 4, 5, 12) / 1000)).toBe('2015-05-05')
  })
})

describe('formatScanLabel (catalogue: scanIdle / scanRun)', () => {
  const now = Date.UTC(2026, 6, 28, 12, 0, 0)

  it('shows the counter while a run is in flight', () => {
    expect(
      formatScanLabel({ state: 'indexing', done: 1842, total: 2250, finished_at: null }, now),
    ).toBe('스캔 중 1,842 / 2,250')
  })

  it('omits the counter before anything has been discovered', () => {
    expect(formatScanLabel({ state: 'walking', done: 0, total: 0, finished_at: null }, now)).toBe(
      '스캔 중',
    )
  })

  it('reports how long ago the last run finished', () => {
    const finished = now / 1000 - 8 * 60
    expect(formatScanLabel({ state: 'idle', done: 0, total: 0, finished_at: finished }, now)).toBe(
      '스캔 대기 — 8분 전 완료',
    )
  })

  it('drops the suffix when no run has ever finished', () => {
    expect(formatScanLabel({ state: 'idle', done: 0, total: 0, finished_at: null }, now)).toBe(
      '스캔 대기',
    )
  })

  it('never reports negative minutes for a clock that ran backwards', () => {
    expect(minutesSince(now / 1000 + 600, now)).toBe(0)
  })
})

describe('scanPercent', () => {
  it('matches the 63 % in library-scanning-progress-1440.png', () => {
    expect(scanPercent({ done: 1418, total: 2250 })).toBe(63)
  })

  it('is 0 before the walker has counted anything, and never exceeds 100', () => {
    expect(scanPercent({ done: 0, total: 0 })).toBe(0)
    expect(scanPercent({ done: 10, total: 5 })).toBe(100)
  })
})
