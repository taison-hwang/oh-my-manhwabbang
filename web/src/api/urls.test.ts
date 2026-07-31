/**
 * URL construction is half the frozen contract: a wrong `?v=`, a missing `?w=` or a
 * double base path is invisible in TypeScript and fatal at runtime.
 *
 * Covers: NFR-SEC-003 (base path), FR-SRV-007 / arch §5.3 (`?v={cv}`), impl-plan §0.4 +
 * A-1/A-6 (widths), impl-plan §4 rule 1 (1-based pages), FR-LIB-004/005/006 + A-4
 * (series list params).
 */

import { afterEach, describe, expect, it } from 'vitest'

import { BOOK_CV, BOOK_ID, COVER_CV, SERIES_ID } from './fixtures'
import {
  apiUrl,
  assertPageNumber,
  bookPrefsUrl,
  bookProgressUrl,
  bookUrl,
  cachePurgeUrl,
  cacheUsageUrl,
  continueUrl,
  encodeQuery,
  healthUrl,
  normalizeBasePath,
  pageThumbUrl,
  pageUrl,
  progressImportUrl,
  readBasePathFromDocument,
  resetBasePath,
  rootsUrl,
  rootUrl,
  scanLogUrl,
  scanStatusUrl,
  seriesCoverUrl,
  seriesListUrl,
  seriesRescanUrl,
  seriesUrl,
  setBasePath,
  settingsUrl,
  snapThumbWidth,
  THUMB_WIDTH_FOR,
  THUMB_WIDTHS,
  thumbWidthFor,
  LARGEST_THUMB_WIDTH,
} from './urls'

afterEach(() => {
  resetBasePath()
  document.head.querySelector('base')?.remove()
})

describe('base path (NFR-SEC-003)', () => {
  it.each([
    ['', ''],
    ['/', ''],
    ['   ', ''],
    ['/reader', '/reader'],
    ['reader', '/reader'],
    ['/reader/', '/reader'],
    ['reader/', '/reader'],
    ['/reader//', '/reader'],
    ['/a/b', '/a/b'],
  ])('normalizes %o to %o', (raw, want) => {
    expect(normalizeBasePath(raw)).toBe(want)
  })

  it('reads the base path from the <base href> the server injects', () => {
    const base = document.createElement('base')
    base.setAttribute('href', '/reader/')
    document.head.append(base)
    expect(readBasePathFromDocument(document)).toBe('/reader')
  })

  it('treats an absolute <base href> as its pathname', () => {
    const base = document.createElement('base')
    base.setAttribute('href', 'http://nas.local:8790/reader/')
    document.head.append(base)
    expect(readBasePathFromDocument(document)).toBe('/reader')
  })

  it('is empty with no <base> tag — a deep link is never mistaken for a base path', () => {
    window.history.pushState({}, '', '/series/gzj75n6x7rir6but')
    expect(readBasePathFromDocument(document)).toBe('')
    window.history.pushState({}, '', '/')
  })

  it('prefixes every API url exactly once', () => {
    setBasePath('/reader')
    expect(rootsUrl()).toBe('/reader/api/roots')
    expect(seriesUrl(SERIES_ID)).toBe(`/reader/api/series/${SERIES_ID}`)
    expect(pageUrl(BOOK_ID, 7, { v: BOOK_CV })).toBe(
      `/reader/api/books/${BOOK_ID}/pages/7?v=${BOOK_CV}`,
    )
  })
})

describe('query encoding', () => {
  it('drops undefined and null, keeps false and 0', () => {
    expect(encodeQuery({ a: undefined, b: null, c: false, d: 0 })).toBe('?c=false&d=0')
  })

  it('emits no question mark when everything is dropped', () => {
    expect(encodeQuery({ a: undefined })).toBe('')
  })

  it('repeats the key for array values (FR-LIB-005 root filter)', () => {
    expect(encodeQuery({ root: ['mangga', 'novel'] })).toBe('?root=mangga&root=novel')
  })

  it('percent-encodes Korean values', () => {
    expect(encodeQuery({ q: '군계' })).toBe('?q=%EA%B5%B0%EA%B3%84')
  })
})

describe('series list params (§7.5 + A-4 + A-8)', () => {
  it('emits only the documented keys, in contract order', () => {
    expect(
      seriesListUrl({
        root: ['mangga'],
        q: 'ㄱㄱ',
        status: 'ok',
        progress: 'reading',
        scope: 'added',
        sort: 'recent',
        order: 'desc',
        offset: 60,
        limit: 60,
      }),
    ).toBe(
      '/api/series?root=mangga&q=%E3%84%B1%E3%84%B1&status=ok&progress=reading&scope=added&sort=recent&order=desc&offset=60&limit=60',
    )
  })

  it('sends nothing at all when no filter is set — the server owns the defaults', () => {
    expect(seriesListUrl()).toBe('/api/series')
  })

  /**
   * Amendment A-8 / ruling E-9. `scope` is a filter, not a sort: the 최근 추가
   * count is `total` from `scope=added&limit=1`, and asking for it must not
   * silently degrade into a whole-library query.
   */
  it('serialises scope=added, so the 최근 추가 count idiom reaches the server', () => {
    expect(seriesListUrl({ scope: 'added', limit: 1 })).toBe('/api/series?scope=added&limit=1')
  })

  it('serialises scope=all explicitly when asked, and drops it when unset', () => {
    expect(seriesListUrl({ scope: 'all' })).toBe('/api/series?scope=all')
    expect(seriesListUrl({ sort: 'added', order: 'desc' })).toBe(
      '/api/series?sort=added&order=desc',
    )
  })
})

describe('image urls carry ?v={cv} (arch §5.3, impl-plan §4 rule 2)', () => {
  it('appends the cover_cv to a cover url', () => {
    expect(seriesCoverUrl(SERIES_ID, { w: 400, v: COVER_CV })).toBe(
      `/api/series/${SERIES_ID}/cover?w=400&v=${COVER_CV}`,
    )
  })

  it('omits v only when the server has none — the response is then 60 s-cacheable', () => {
    expect(seriesCoverUrl(SERIES_ID, { w: 400, v: null })).toBe(
      `/api/series/${SERIES_ID}/cover?w=400`,
    )
  })

  it('appends the book cv to page and thumb urls', () => {
    expect(pageUrl(BOOK_ID, 42, { v: BOOK_CV })).toBe(
      `/api/books/${BOOK_ID}/pages/42?v=${BOOK_CV}`,
    )
    expect(pageThumbUrl(BOOK_ID, 42, { w: 120, v: BOOK_CV })).toBe(
      `/api/books/${BOOK_ID}/thumbs/42?w=120&v=${BOOK_CV}`,
    )
  })

  it('sends ?w only for a PDF render (§7.6: ignored for zip/dir)', () => {
    expect(pageUrl(BOOK_ID, 3, { v: BOOK_CV })).not.toContain('w=')
    expect(pageUrl(BOOK_ID, 3, { v: BOOK_CV, w: 1_600 })).toBe(
      `/api/books/${BOOK_ID}/pages/3?v=${BOOK_CV}&w=1600`,
    )
  })
})

describe('thumbnail widths (A-1, A-6, impl-plan §0.4)', () => {
  it('is exactly the configured set', () => {
    expect(THUMB_WIDTHS).toEqual([120, 240, 400, 640])
    expect(LARGEST_THUMB_WIDTH).toBe(THUMB_WIDTHS[THUMB_WIDTHS.length - 1])
  })

  it.each([
    [1, 120],
    [96, 120],
    [48, 120],
    [120, 120],
    [121, 240],
    [132, 240],
    [136, 240],
    [256, 400],
    [356, 400],
    [401, 640],
    [448, 640],
    [640, 640],
    [4_000, 640],
  ])('snaps %ipx up to %i', (devicePx, want) => {
    expect(snapThumbWidth(devicePx)).toBe(want)
  })

  // The §0.4 derivation table, consumer by consumer. Each row is the URL that consumer
  // must produce; getting one wrong silently doubles bandwidth because the server snaps up.
  it.each([
    ['viewerStrip', 120],
    ['listRow', 120],
    ['continueCard', 240],
    ['sliderPreview', 240],
    ['volumeTile', 400],
    ['gridCoverWide', 400],
    ['gridCoverNarrow', 640],
    ['seriesHero', 400],
  ] as const)('%s requests w=%i', (consumer, want) => {
    expect(thumbWidthFor(consumer)).toBe(want)
    expect(seriesCoverUrl(SERIES_ID, { w: THUMB_WIDTH_FOR[consumer], v: COVER_CV })).toBe(
      `/api/series/${SERIES_ID}/cover?w=${String(want)}&v=${COVER_CV}`,
    )
    expect(pageThumbUrl(BOOK_ID, 1, { w: THUMB_WIDTH_FOR[consumer], v: BOOK_CV })).toBe(
      `/api/books/${BOOK_ID}/thumbs/1?w=${String(want)}&v=${BOOK_CV}`,
    )
  })

  it('matches the widths the server reports in settings.server.thumbnail_widths', async () => {
    const { settings } = await import('./fixtures')
    expect(settings.server.thumbnail_widths).toEqual([...THUMB_WIDTHS])
  })
})

describe('page numbers are 1-based (impl-plan §4 rule 1)', () => {
  it.each([0, -1, 1.5, Number.NaN, Number.POSITIVE_INFINITY])('rejects %o', (n) => {
    expect(() => {
      assertPageNumber(n)
    }).toThrow(RangeError)
    expect(() => pageUrl(BOOK_ID, n, { v: BOOK_CV })).toThrow(RangeError)
    expect(() => pageThumbUrl(BOOK_ID, n, { w: 120, v: BOOK_CV })).toThrow(RangeError)
  })

  it('accepts page 1 and the last page', () => {
    expect(pageUrl(BOOK_ID, 1, { v: BOOK_CV })).toContain('/pages/1?')
    expect(pageUrl(BOOK_ID, 1_071, { v: BOOK_CV })).toContain('/pages/1071?')
  })
})

describe('every endpoint of §7.13', () => {
  it('builds the documented paths', () => {
    expect(healthUrl()).toBe('/api/health')
    expect(rootsUrl()).toBe('/api/roots')
    expect(seriesUrl(SERIES_ID)).toBe(`/api/series/${SERIES_ID}`)
    expect(seriesRescanUrl(SERIES_ID)).toBe(`/api/series/${SERIES_ID}/rescan`)
    expect(bookUrl(BOOK_ID)).toBe(`/api/books/${BOOK_ID}`)
    expect(bookProgressUrl(BOOK_ID)).toBe(`/api/books/${BOOK_ID}/progress`)
    expect(bookPrefsUrl(BOOK_ID)).toBe(`/api/books/${BOOK_ID}/prefs`)
    expect(continueUrl()).toBe('/api/continue')
    expect(continueUrl(8)).toBe('/api/continue?limit=8')
    expect(settingsUrl()).toBe('/api/settings')
    expect(cacheUsageUrl()).toBe('/api/cache/usage')
    expect(cachePurgeUrl('all')).toBe('/api/cache?kind=all')
    expect(scanStatusUrl()).toBe('/api/scan/status')
    expect(scanLogUrl({ limit: 200, level: 'warn' })).toBe('/api/scan/log?limit=200&level=warn')
    expect(progressImportUrl('replace')).toBe('/api/progress/import?strategy=replace')
    expect(progressImportUrl()).toBe('/api/progress/import')
    // Amendment A-11 (ruling E-26): `DELETE /api/roots/{name}`. `POST` reuses
    // `rootsUrl()` — the collection is where a creation goes.
    expect(rootUrl('lanovel')).toBe('/api/roots/lanovel')
  })

  it('escapes an id rather than injecting it into the path', () => {
    expect(apiUrl(`/series/${encodeURIComponent('a/../b')}`)).toBe('/api/series/a%2F..%2Fb')
  })

  /**
   * A root `name` is a *configuration* identity — §3.2's `[a-zA-Z0-9._-]{1,64}`,
   * not §7.1's opaque base32 id — so it is the one path wildcard that can carry
   * a `.` or a `-`, and the one a caller could try to walk out of. It goes
   * through `encodeURIComponent` like every other segment.
   */
  it('escapes a root name (A-11)', () => {
    expect(rootUrl('a/../b')).toBe('/api/roots/a%2F..%2Fb')
    expect(rootUrl('02.lanovel-2')).toBe('/api/roots/02.lanovel-2')
  })
})
