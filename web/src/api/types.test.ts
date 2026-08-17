/**
 * The enum values are the part of the contract that a compiler cannot defend: WP-12 is
 * writing the server against the same document, and a wire value that reads plausibly
 * (`double`, `screen`, `vols`) is exactly how the two halves drift apart.
 *
 * Every assertion here is a literal quote from arch §7.3 / impl-plan §0.1 C-1…C-4 + A-4.
 * The type-level half of the contract is checked by `tsc` through `fixtures.ts`, which
 * annotates one complete object per response type.
 */

import { describe, expect, it } from 'vitest'

import { codeForStatus } from './errors'
import { BOOK_ID, SERIES_ID, bookDetail, brokenBookDetail, settings } from './fixtures'
import {
  BOOK_KINDS,
  CACHE_KINDS,
  DIMS_STATES,
  DISPLAY_MODES,
  ERROR_CODES,
  FIT_MODES,
  ID_PATTERN,
  ITEM_STATUSES,
  LOG_LEVELS,
  PROGRESS_FILTERS,
  PURGE_KINDS,
  READING_DIRS,
  SCAN_STATES,
  SERIES_KINDS,
  SERIES_LIST_DEFAULT_LIMIT,
  SERIES_LIST_MAX_LIMIT,
  SERIES_SCOPES,
  SERIES_STATUS_FILTERS,
  SERIES_STATUSES,
  SORT_KEYS,
  SORT_ORDERS,
  THEMES,
} from './types'

describe('enum values that bite (C-1 … C-4)', () => {
  it('display mode is "spread", never "double" (C-1)', () => {
    expect([...DISPLAY_MODES]).toEqual(['single', 'spread', 'vertical'])
    expect(DISPLAY_MODES).not.toContain('double')
  })

  it('fit mode is "contain", never "screen" (C-2)', () => {
    expect([...FIT_MODES]).toEqual(['width', 'height', 'original', 'contain'])
    expect(FIT_MODES).not.toContain('screen')
  })

  it('sort keys are the API names, not the ui-spec names (C-3)', () => {
    expect([...SORT_KEYS]).toEqual(['name', 'mtime', 'recent', 'size', 'books', 'added'])
    expect(SORT_KEYS).not.toContain('read')
    expect(SORT_KEYS).not.toContain('vols')
    expect([...SORT_ORDERS]).toEqual(['asc', 'desc'])
  })

  it('books are "dir" while series are "folder" (C-4)', () => {
    // `rar`/`nestedrar` are D-71's, and were absent here while the server was
    // already sending them for 22 books of the collection.
    expect([...BOOK_KINDS]).toEqual(['zip', 'nestedzip', 'rar', 'nestedrar', 'dir', 'pdf'])
    expect([...SERIES_KINDS]).toEqual(['folder', 'zip', 'pdf'])
  })
})

describe('the rest of §7.3', () => {
  it('item status covers all five index verdicts (FR-IDX-010)', () => {
    expect([...ITEM_STATUSES]).toEqual(['ok', 'empty', 'error', 'encrypted', 'unsupported'])
  })

  it('a series status is the three-value fold, never a book-only verdict (E-14)', () => {
    expect([...SERIES_STATUSES]).toEqual(['ok', 'empty', 'error'])
    expect(SERIES_STATUSES).not.toContain('encrypted')
    expect(SERIES_STATUSES).not.toContain('unsupported')
    // Every series status is still an item status, so a component that switches
    // on `ItemStatus` handles both.
    for (const status of SERIES_STATUSES) {
      expect(ITEM_STATUSES).toContain(status)
    }
  })

  it('reading direction is ltr|rtl (FR-VWR-002)', () => {
    expect([...READING_DIRS]).toEqual(['ltr', 'rtl'])
  })

  it('scan states and log levels match §7.10', () => {
    expect([...SCAN_STATES]).toEqual(['idle', 'walking', 'indexing', 'covers', 'cancelling'])
    expect([...LOG_LEVELS]).toEqual(['info', 'warn', 'error'])
  })

  it('dims state matches arch §5.8', () => {
    expect([...DIMS_STATES]).toEqual(['none', 'partial', 'done'])
  })

  it('cache kinds match §7.9, and purge additionally accepts "all"', () => {
    expect([...CACHE_KINDS]).toEqual(['thumbs', 'pdf', 'wazero'])
    expect([...PURGE_KINDS]).toEqual(['thumbs', 'pdf', 'wazero', 'all'])
  })

  it('themes match §7.8', () => {
    expect([...THEMES]).toEqual(['light', 'dark', 'system'])
  })
})

describe('list filters', () => {
  it('status filter adds "all" on top of the item statuses (§7.5)', () => {
    expect([...SERIES_STATUS_FILTERS]).toEqual(['ok', 'empty', 'error', 'all'])
  })

  it('progress filter is amendment A-4', () => {
    expect([...PROGRESS_FILTERS]).toEqual(['any', 'reading', 'done', 'unread'])
  })

  /**
   * Amendment A-8 / ruling E-9. Exactly two values: arch §7.5 rejects `reading`,
   * `done` and root names here on purpose, so the sidebar's three wire parameters
   * cannot be conflated into one.
   */
  it('scope filter is amendment A-8, and is exactly two values', () => {
    expect([...SERIES_SCOPES]).toEqual(['all', 'added'])
  })

  it('paging bounds are the documented ones', () => {
    expect(SERIES_LIST_DEFAULT_LIMIT).toBe(60)
    expect(SERIES_LIST_MAX_LIMIT).toBe(200)
  })
})

describe('error codes (§7.2)', () => {
  it('is exactly the frozen list, in the documented order', () => {
    expect([...ERROR_CODES]).toEqual([
      'bad_request',
      'unauthorized',
      // Amendment A-11 (ruling E-26): 403, the answer both root-editing verbs
      // give while `server.allow_root_editing` is off. Same defect and same fix
      // as A-9's `rate_limited` — a ruling mandated a status §7.2 could not name.
      'forbidden',
      'not_found',
      'conflict',
      'stale_version',
      'unprocessable',
      'thumb_unavailable',
      'rate_limited',
      'unsupported',
      'unavailable',
      'internal',
    ])
  })

  it('names the 429 of the login limiter (amendment A-9, ruling E-13)', () => {
    expect(ERROR_CODES).toContain('rate_limited')
    expect(codeForStatus(429)).toBe('rate_limited')
  })

  /**
   * `codeForStatus` is the fallback for a response whose body is **not** a §7.2
   * envelope, and the 403 row is the one that matters: a reverse proxy in front
   * of SHELF (NFR-SEC-003) answers with its own HTML, so the status is all the
   * client has. Folding it into `unauthorized` — one word, and every test in
   * the suite stayed green when it was tried — routes such a 403 into ruling
   * E-17's re-authentication path, i.e. a login screen that no login can
   * satisfy, since the refusal lives in a YAML key and not in a session.
   */
  it('keeps 403 out of the re-auth path when the body is not an envelope (A-11, E-17)', () => {
    expect(codeForStatus(403)).toBe('forbidden')
    expect(codeForStatus(403)).not.toBe('unauthorized')
    // The neighbouring rows, so a map re-ordered rather than re-pointed also fails.
    expect(codeForStatus(401)).toBe('unauthorized')
    expect(codeForStatus(404)).toBe('not_found')
  })
})

describe('ids (arch §3.4)', () => {
  it('are 16 lowercase base32 characters', () => {
    expect(ID_PATTERN.test(SERIES_ID)).toBe(true)
    expect(ID_PATTERN.test(BOOK_ID)).toBe(true)
    expect(ID_PATTERN.test('TOOSHORT')).toBe(false)
    expect(ID_PATTERN.test('gzj75n6x7rir6bu1')).toBe(false) // 1 is not in base32
  })
})

describe('shapes the UI depends on', () => {
  it('page numbers start at 1 (impl-plan §4 rule 1)', () => {
    expect(bookDetail.pages[0]?.n).toBe(1)
    expect(bookDetail.pages.map((p) => p.n)).toEqual([1, 2, 3])
  })

  it('unknown page dimensions are null, never absent (WP-06 acceptance 1)', () => {
    expect(bookDetail.pages[1]?.w).toBeNull()
    expect(bookDetail.pages[1]).toHaveProperty('h')
  })

  it('a non-ok book carries an error and no pages (rule 4)', () => {
    expect(brokenBookDetail.status).not.toBe('ok')
    expect(brokenBookDetail.pages).toEqual([])
    expect(brokenBookDetail.error).not.toBeNull()
  })

  it('settings mirror the server block read-only, including A-5 library_scope', () => {
    expect(Object.keys(settings.server)).toEqual([
      'thumbnail_widths',
      'scan_workers',
      'thumb_workers',
      'pdf_enabled',
      'avif_enabled',
      'auth_enabled',
      'base_path',
      'version',
      'recently_added_days', // amendment A-8
      'config_path', // amendment A-10
      'root_editing_enabled', // amendment A-11 — the capability, not the key
      'config_changed_on_disk', // amendment A-11 — the truthful restart notice
    ])
    expect(settings.library_scope).toBe('all')
    // A-10 is only worth having if the value is one a user can act on: the
    // lookup order has four candidates, so the fixture pins an absolute path.
    expect(settings.server.config_path.startsWith('/')).toBe(true)
  })
})
