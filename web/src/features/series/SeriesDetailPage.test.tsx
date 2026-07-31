import '@testing-library/jest-dom/vitest'

import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { setupServer } from 'msw/node'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom'
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'

import {
  bookSummary,
  errorEnvelope,
  ORIGIN,
  root,
  rootsResponse,
  scanStatusIdle,
  seriesDetail,
  seriesSummary,
  settings,
  SERIES_ID,
} from '../../api/fixtures'
import { resetBasePath } from '../../api/urls'
import type { BookSummary, SeriesDetail } from '../../api/types'
import { useSeriesDirStore } from '../../store/seriesDir'
import { useUiStore } from '../../store/ui'
import { SeriesDetailPage } from './SeriesDetailPage'
import {
  VOLUME_ROW_COLUMNS_BASE,
  VOLUME_ROW_COLUMNS_LG,
  VOLUME_ROW_COLUMNS_MD,
  VOLUME_ROW_GAP_PX,
  VOLUME_ROW_GRID_CLASS,
} from './VolumeRow'

/**
 * Screen 2 end to end against MSW: prd UI-002 / FR-LIB-009 (the volume list and
 * its four facts per volume), FR-IDX-010 (unopenable volumes), FR-STT-002
 * (series progress), E-5 (duplicates), FR-VWR-012 (manual read/unread) and
 * WP-10 acceptance 4 (rescan, including `409 conflict`).
 */

/** `noUncheckedIndexedAccess` types an indexed query result as `T | undefined`. */
function at<T>(items: readonly T[], index: number): T {
  const item = items[index]
  if (item === undefined) throw new Error(`expected an element at index ${String(index)}`)
  return item
}

const server = setupServer()

beforeAll(() => {
  server.listen({ onUnhandledRequest: 'error' })

  // jsdom/undici interop, not a product concern: React Router v7 builds a
  // `Request` per navigation and hands it a jsdom `AbortSignal`, which undici's
  // `instanceof` check rejects. Both are native and consistent in a browser.
  const Base = globalThis.Request
  class SignallessRequest extends Base {
    constructor(input: RequestInfo | URL, init?: RequestInit) {
      super(input, init === undefined ? undefined : { ...init, signal: null })
    }
  }
  Object.defineProperty(globalThis, 'Request', {
    writable: true,
    configurable: true,
    value: SignallessRequest,
  })
})
afterEach(() => {
  server.resetHandlers()
  resetBasePath()
})
afterAll(() => {
  server.close()
})

beforeEach(() => {
  localStorage.clear()
  useUiStore.setState({ view: 'grid' })
  useSeriesDirStore.setState({ bySeries: {} })
})

// ---------------------------------------------------------------------------
// Fixtures — the real `[만화] 군계 1~25` shape from data-survey D-6 / E-5
// ---------------------------------------------------------------------------

function volume(overrides: Partial<BookSummary>): BookSummary {
  return { ...bookSummary, progress: null, ...overrides }
}

/** `01권/` (folder) *and* `01권.zip`, plus two `07권` copies, one truncated. */
const DUPLICATE_BOOKS: BookSummary[] = [
  volume({
    id: 'aaaaaaaaaaaaaaaa',
    name: '군계(軍鷄) 01권',
    kind: 'dir',
    ord: 0,
    page_count: 187,
    // arch §4.4: `file_size` is 0 for kind:"dir" — a directory has no
    // container, so its page bytes are its bytes on disk.
    total_bytes: 24_500_000,
    file_size: 0,
    progress: {
      book_id: 'aaaaaaaaaaaaaaaa',
      series_id: SERIES_ID,
      last_page: 187,
      page_count: 187,
      completed: true,
      started_at: 1,
      updated_at: 2,
      stale: false,
    },
  }),
  volume({
    id: 'bbbbbbbbbbbbbbbb',
    name: '군계(軍鷄) 01권.zip',
    kind: 'zip',
    ord: 1,
    page_count: 187,
    total_bytes: 24_500_000,
  }),
  volume({
    id: 'cccccccccccccccc',
    name: '군계(軍鷄) 07권.repair.zip',
    kind: 'zip',
    ord: 2,
    page_count: 0,
    // A truncated archive yields no page rows, so its page-byte total is 0
    // while the file is still 18.3 MB of disk.
    total_bytes: 0,
    file_size: 18_300_000,
    status: 'error',
    error: 'zip: truncated central directory at entry 812',
  }),
  volume({
    id: 'dddddddddddddddd',
    name: '군계(軍鷄) 09권.zip',
    kind: 'zip',
    ord: 3,
    page_count: 0,
    total_bytes: 0,
    file_size: 21_000_000,
    status: 'encrypted',
    error: 'zip: archive is password-protected',
  }),
  volume({
    id: 'eeeeeeeeeeeeeeee',
    name: '군계(軍鷄) 10권.pdf',
    kind: 'pdf',
    ord: 4,
    page_count: 214,
    // A PDF's pages are RENDERED, never stored, so `total_bytes` — the sum of
    // uncompressed page bytes (arch §4.4) — is 0 by construction. The 용량 the
    // reader is owed is the document on disk.
    total_bytes: 0,
    file_size: 60_000_000,
    progress: {
      book_id: 'eeeeeeeeeeeeeeee',
      series_id: SERIES_ID,
      last_page: 42,
      page_count: 214,
      completed: false,
      started_at: 1,
      updated_at: 2,
      stale: false,
    },
  }),
]

function detailOf(overrides: Partial<SeriesDetail> = {}): SeriesDetail {
  return {
    ...seriesDetail,
    books: DUPLICATE_BOOKS,
    book_count: DUPLICATE_BOOKS.length,
    total_bytes: 3_650_722_201, // 3.7 GB (E-11: decimal)
    // 6 완독 of 25 volumes, one merely started → FR-STT-002 says 24 %.
    progress: {
      books_total: 25,
      books_completed: 6,
      books_started: 1,
      // Deliberately wrong: nothing on screen may come from this field.
      percent: 99,
      last_read_at: 1_753_600_500,
      last_book_id: 'eeeeeeeeeeeeeeee',
      last_page: 42,
    },
    ...overrides,
  }
}

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

interface Recorded {
  rescans: string[]
  progressPuts: { bid: string; body: unknown }[]
  progressDeletes: string[]
  /** Every `/api/books/{bid}/thumbs/{n}` the screen asked for, in order. */
  thumbs: { bid: string; n: string; w: string | null; v: string | null }[]
}

function baseHandlers(detail: SeriesDetail, recorded: Recorded, rescanStatus = 202) {
  return [
    http.get(`${ORIGIN}/api/series/:sid`, () => HttpResponse.json(detail)),
    // prd UI-002 / design.md 화면 2: every volume carries its own thumbnail.
    http.get(`${ORIGIN}/api/books/:bid/thumbs/:n`, ({ params, request }) => {
      const url = new URL(request.url)
      recorded.thumbs.push({
        bid: String(params.bid),
        n: String(params.n),
        w: url.searchParams.get('w'),
        v: url.searchParams.get('v'),
      })
      return new HttpResponse(new Uint8Array([0xff, 0xd8, 0xff, 0xd9]), {
        headers: { 'Content-Type': 'image/jpeg' },
      })
    }),
    http.post(`${ORIGIN}/api/series/:sid/rescan`, ({ params }) => {
      recorded.rescans.push(String(params.sid))
      if (rescanStatus === 409) {
        return HttpResponse.json(
          errorEnvelope('conflict', 'a scan is already running'),
          { status: 409 },
        )
      }
      return HttpResponse.json({ run_id: 'run-1' }, { status: 202 })
    }),
    http.get(`${ORIGIN}/api/series/:sid/cover`, () =>
      HttpResponse.json(errorEnvelope('not_found', 'no cover'), { status: 404 }),
    ),
    http.get(`${ORIGIN}/api/settings`, () => HttpResponse.json(settings)),
    http.get(`${ORIGIN}/api/scan/status`, () => HttpResponse.json(scanStatusIdle)),
    // `SeriesHeader` reads `useRoots()` to build the 원본 경로 — the root's
    // absolute path with the series' root-relative path appended. Without this
    // handler the request was unmatched, MSW failed the *request* rather than
    // the test, and the header quietly fell back to the relative path: the
    // assertion below was pinning the degraded rendering, not the product's.
    http.get(`${ORIGIN}/api/roots`, () => HttpResponse.json(rootsResponse)),
    http.put(`${ORIGIN}/api/books/:bid/progress`, async ({ params, request }) => {
      recorded.progressPuts.push({ bid: String(params.bid), body: await request.json() })
      return HttpResponse.json({
        book_id: String(params.bid),
        series_id: SERIES_ID,
        last_page: 214,
        page_count: 214,
        completed: true,
        started_at: 1,
        updated_at: 2,
        stale: false,
      })
    }),
    http.delete(`${ORIGIN}/api/books/:bid/progress`, ({ params }) => {
      recorded.progressDeletes.push(String(params.bid))
      return new HttpResponse(null, { status: 204 })
    }),
  ]
}

function LocationProbe() {
  const location = useLocation()
  return <p data-testid="location">{`${location.pathname}${location.search}`}</p>
}

function renderPage(): void {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[`/series/${SERIES_ID}`]}>
        <Routes>
          <Route path="/series/:sid" element={<SeriesDetailPage />} />
          <Route path="/series/:sid/books/:bid" element={<LocationProbe />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

function newRecorded(): Recorded {
  return { rescans: [], progressPuts: [], progressDeletes: [], thumbs: [] }
}

async function setup(detail = detailOf(), rescanStatus = 202): Promise<Recorded> {
  const recorded = newRecorded()
  server.use(...baseHandlers(detail, recorded, rescanStatus))
  renderPage()
  await screen.findByRole('heading', { name: detail.name })
  return recorded
}

// ---------------------------------------------------------------------------
// Header — prd UI-002
// ---------------------------------------------------------------------------

describe('series header (prd UI-002, ui-spec §5.1)', () => {
  it('shows the cover, name, source path and the four stats', async () => {
    await setup(detailOf({ path: '[만화] 군계 1~25 (root-relative)' }))
    const header = screen.getByRole('banner')
    expect(within(header).getByRole('heading', { name: '[만화] 군계 1~25' })).toBeInTheDocument()
    // The one filesystem path the product shows outside settings (prd §5.3),
    // and it is the **원본 경로**: the root's absolute path from
    // `GET /api/roots`, with the series' root-relative path appended. Asserting
    // the relative half alone is what this line used to do, and it passed only
    // because `/api/roots` was unhandled.
    await waitFor(() => {
      expect(
        within(header).getByText(`${root.path}/[만화] 군계 1~25 (root-relative)`),
      ).toBeInTheDocument()
    })

    for (const label of ['권', '용량', '형식', '진행률']) {
      expect(within(header).getByText(label)).toBeInTheDocument()
    }
    expect(within(header).getByText('5')).toBeInTheDocument() // 권
    expect(within(header).getByText('3.7 GB')).toBeInTheDocument() // 용량
    expect(within(header).getByText('FOLDER')).toBeInTheDocument() // 형식 (series kind)
  })

  it('aggregates 진행률 over completed volumes, not the server percent (FR-STT-002)', async () => {
    await setup()
    // 6 완독 / 25권 = 24 %. The payload's `percent: 99` must not appear.
    expect(screen.getByText('24%')).toBeInTheDocument()
    expect(screen.queryByText('99%')).not.toBeInTheDocument()
    expect(
      screen.getByRole('progressbar', { name: '[만화] 군계 1~25 진행률' }),
    ).toHaveAttribute('aria-valuenow', '24')
  })

  it('offers 처음부터 읽기 / 이어 읽기 / 이 시리즈 재스캔', async () => {
    await setup()
    expect(screen.getByRole('button', { name: '처음부터 읽기' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '이어 읽기' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '이 시리즈 재스캔' })).toBeInTheDocument()
  })

  it('says 읽기 시작 instead of 이어 읽기 when nothing has been opened', async () => {
    await setup(
      detailOf({
        progress: {
          books_total: 25,
          books_completed: 0,
          books_started: 0,
          percent: 0,
          last_read_at: null,
          last_book_id: null,
          last_page: null,
        },
      }),
    )
    expect(screen.getByRole('button', { name: '읽기 시작' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '이어 읽기' })).not.toBeInTheDocument()
  })

  it('seeds 읽기 방향 from the global default and keeps the change client-side (C-9)', async () => {
    await setup()
    const group = screen.getByRole('radiogroup', { name: '읽기 방향' })
    // `settings.reading_direction` is `ltr` in the fixture.
    expect(within(group).getByRole('radio', { name: 'L→R' })).toBeChecked()

    await userEvent.click(within(group).getByRole('radio', { name: 'R→L' }))
    await waitFor(() => {
      expect(useSeriesDirStore.getState().bySeries[SERIES_ID]).toBe('rtl')
    })
    // Nothing was written to the server: PUT /api/settings has no handler, and
    // `onUnhandledRequest: 'error'` would have failed the test if one fired.
  })
})

// ---------------------------------------------------------------------------
// Volume list — FR-LIB-009, E-5, FR-IDX-010
// ---------------------------------------------------------------------------

describe('volume grid (FR-LIB-009, ui-spec §5.3)', () => {
  it('lists every duplicate side by side, in the server order (E-5)', async () => {
    await setup()
    const grid = screen.getByTestId('volume-grid')
    const names = within(grid)
      .getAllByTitle(/군계/)
      .map((el) => el.textContent)
    expect(names).toEqual([
      '군계(軍鷄) 01권',
      '군계(軍鷄) 01권.zip',
      '군계(軍鷄) 07권.repair.zip',
      '군계(軍鷄) 09권.zip',
      '군계(軍鷄) 10권.pdf',
    ])
    expect(screen.getByText('권 목록')).toBeInTheDocument()
    expect(screen.getByText('5권')).toBeInTheDocument()
  })

  it('shows the format badge, page count and size per volume (FR-LIB-009)', async () => {
    await setup()
    const grid = screen.getByTestId('volume-grid')
    expect(within(grid).getAllByText('FOLDER')).toHaveLength(1)
    expect(within(grid).getAllByText('ZIP')).toHaveLength(3)
    expect(within(grid).getAllByText('PDF')).toHaveLength(1)
    // Both `01권/` and `01권.zip` are 187 pages / 25 MB — E-5's duplicates.
    // One is a folder (`file_size` 0, so the page bytes are the disk bytes) and
    // one an archive (`file_size` is the container): the same number either way.
    expect(within(grid).getAllByText('187p · 25 MB')).toHaveLength(2)
    // The PDF is the regression: `total_bytes` is 0 for a rendered document, so
    // rendering it printed `0 KB` on every PDF volume in the product.
    expect(within(grid).getByText('214p · 60 MB')).toBeInTheDocument()
    // …and so do the two unreadable volumes, which have no page rows at all.
    expect(within(grid).getByText('0p · 18 MB')).toBeInTheDocument()
    expect(within(grid).getByText('0p · 21 MB')).toBeInTheDocument()
  })

  it('requests a thumbnail for every openable volume (prd UI-002, design.md 화면 2)', async () => {
    // The regression: the volume list never issued a single
    // `/api/books/{bid}/thumbs/…`, so every volume rendered the striped "no
    // thumbnail" placeholder for ever. impl-plan §0.4 budgets the width for
    // this exact consumer ("Volume tile (series detail) — 128 CSS px ⇒ w=400").
    const recorded = await setup()
    // Three openable volumes ⇒ three <img> elements inside the grid. The
    // failure this pins reported exactly one <img> on the whole screen (the
    // series hero cover) and zero /thumbs/ requests in the network log.
    await waitFor(() => {
      expect(screen.getByTestId('volume-grid').querySelectorAll('img')).toHaveLength(3)
    })
    const asked = recorded.thumbs
    expect(asked.map((t) => t.bid).sort()).toEqual(
      ['aaaaaaaaaaaaaaaa', 'bbbbbbbbbbbbbbbb', 'eeeeeeeeeeeeeeee'].sort(),
    )
    // Page 1 is the volume's cover; `w` comes from THUMB_WIDTHS (§0.4) and `v`
    // is the book's own `cv`, which is what makes the response immutable.
    for (const t of asked) {
      expect(t.n).toBe('1')
      expect(t.w).toBe('400')
      expect(t.v).toBe(bookSummary.cv)
    }
  })

  it('never asks for a thumbnail of a volume that cannot be opened (FR-IDX-010)', async () => {
    const recorded = await setup()
    await waitFor(() => {
      expect(recorded.thumbs.length).toBeGreaterThan(0)
    })
    // `cccccccccccccccc` is truncated and `dddddddddddddddd` is encrypted:
    // neither has a page 1 to render, so neither may cost a request.
    expect(recorded.thumbs.map((t) => t.bid)).not.toContain('cccccccccccccccc')
    expect(recorded.thumbs.map((t) => t.bid)).not.toContain('dddddddddddddddd')
  })

  it('badges a truncated volume 손상 with its reason and makes it unclickable (FR-IDX-010)', async () => {
    await setup()
    expect(screen.getByText('손상')).toBeInTheDocument()
    expect(screen.getByText('중앙 디렉터리 손상')).toBeInTheDocument()
    // The server's own message rides along for diagnosis (arch §4.11).
    expect(
      screen.getByTitle('zip: truncated central directory at entry 812'),
    ).toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: /군계\(軍鷄\) 07권\.repair\.zip/ }),
    ).not.toBeInTheDocument()
  })

  it('badges an encrypted volume 암호화 and makes it unclickable (FR-IDX-010)', async () => {
    await setup()
    expect(screen.getByText('암호화')).toBeInTheDocument()
    expect(screen.getByText('비밀번호가 필요한 ZIP')).toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: /군계\(軍鷄\) 09권\.zip/ }),
    ).not.toBeInTheDocument()
  })

  it('opens a healthy volume at its own last page', async () => {
    await setup()
    await userEvent.click(screen.getByRole('button', { name: /군계\(軍鷄\) 10권\.pdf/ }))
    expect(screen.getByTestId('location')).toHaveTextContent(
      `/series/${SERIES_ID}/books/eeeeeeeeeeeeeeee?page=42`,
    )
  })
})

describe('volume list mode (ui-spec §5.4)', () => {
  beforeEach(() => {
    useUiStore.setState({ view: 'list' })
  })

  it('renders the per-volume state cell: 완독 · 19% · — · ERR', async () => {
    await setup()
    const list = screen.getByTestId('volume-list')
    expect(within(list).getByText('완독')).toBeInTheDocument()
    expect(within(list).getByText('19%')).toBeInTheDocument() // 42/214 floors to 19
    expect(within(list).getAllByText('ERR')).toHaveLength(2)
    expect(within(list).getAllByText('—').length).toBeGreaterThan(0)
  })

  it('shows the badge and reason inline for an unopenable row (FR-IDX-010)', async () => {
    await setup()
    const list = screen.getByTestId('volume-list')
    expect(within(list).getByText('손상')).toBeInTheDocument()
    expect(within(list).getByText('암호화')).toBeInTheDocument()
    expect(
      within(list).queryByRole('button', { name: '군계(軍鷄) 07권.repair.zip' }),
    ).not.toBeInTheDocument()
  })

  it('carries a per-volume thumbnail in the row thumb cell too (prd UI-002)', async () => {
    const recorded = await setup()
    await waitFor(() => {
      expect(screen.getByTestId('volume-list').querySelectorAll('img')).toHaveLength(3)
    })
    // §0.4's "List-row thumb — 24 CSS px ⇒ w=120": the row is 20×30, so it must
    // not pull the tile's 400px image down to a 20px box.
    for (const t of recorded.thumbs) {
      expect(t.n).toBe('1')
      expect(t.w).toBe('120')
    }
  })

  it('carries 페이지 수 and 용량 per row (FR-LIB-009)', async () => {
    await setup()
    const list = screen.getByTestId('volume-list')
    expect(within(list).getAllByText('187p')).toHaveLength(2)
    // The PDF row: `total_bytes` 0, `file_size` 60 MB.
    expect(within(list).getByText('60 MB')).toBeInTheDocument()
    expect(within(list).getAllByText('25 MB')).toHaveLength(2)
  })
})

// ---------------------------------------------------------------------------
// Actions
// ---------------------------------------------------------------------------

describe('이 시리즈 재스캔 (WP-10 acceptance 4)', () => {
  it('calls POST /api/series/{sid}/rescan', async () => {
    const recorded = await setup()
    await userEvent.click(screen.getByRole('button', { name: '이 시리즈 재스캔' }))
    await waitFor(() => {
      expect(recorded.rescans).toEqual([SERIES_ID])
    })
  })

  it('surfaces a 409 conflict as a non-blocking notice, leaving the screen usable', async () => {
    await setup(detailOf(), 409)
    await userEvent.click(screen.getByRole('button', { name: '이 시리즈 재스캔' }))
    const notice = await screen.findByRole('status')
    expect(notice).toHaveTextContent('a scan is already running')
    // Non-blocking: the volume list is still there and still interactive.
    expect(screen.getByTestId('volume-grid')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '이 시리즈 재스캔' })).toBeEnabled()
  })
})

describe('처음부터 읽기 / 이어 읽기', () => {
  it('starts at page 1 of the first *openable* volume', async () => {
    await setup()
    await userEvent.click(screen.getByRole('button', { name: '처음부터 읽기' }))
    expect(screen.getByTestId('location')).toHaveTextContent(
      `/series/${SERIES_ID}/books/aaaaaaaaaaaaaaaa?page=1`,
    )
  })

  it('resumes into the series last_book_id at its last page', async () => {
    await setup()
    await userEvent.click(screen.getByRole('button', { name: '이어 읽기' }))
    expect(screen.getByTestId('location')).toHaveTextContent(
      `/series/${SERIES_ID}/books/eeeeeeeeeeeeeeee?page=42`,
    )
  })
})

describe('manual read / unread (FR-VWR-012)', () => {
  it('marks an unread volume completed with an immediate PUT', async () => {
    const recorded = await setup()
    // The openable volumes in order are 01권 (완독 → 읽음 해제), 01권.zip and
    // 10권.pdf, so the first 읽음 표시 belongs to 01권.zip.
    const actions = screen.getAllByRole('button', { name: '읽음 표시' })
    await userEvent.click(at(actions, 0))
    await waitFor(() => {
      expect(recorded.progressPuts).toHaveLength(1)
    })
    expect(recorded.progressPuts[0]).toEqual({
      bid: 'bbbbbbbbbbbbbbbb',
      body: { page: 187, completed: true },
    })
  })

  it('clears progress with DELETE when the volume is already 완독', async () => {
    const recorded = await setup()
    await userEvent.click(screen.getByRole('button', { name: '읽음 해제' }))
    await waitFor(() => {
      expect(recorded.progressDeletes).toEqual(['aaaaaaaaaaaaaaaa'])
    })
  })

  it('offers no read/unread action on an unopenable volume', async () => {
    await setup()
    // Three openable volumes → three actions, one of which is 읽음 해제.
    expect(screen.getAllByRole('button', { name: '읽음 표시' })).toHaveLength(2)
    expect(screen.getAllByRole('button', { name: '읽음 해제' })).toHaveLength(1)
  })

  /**
   * In grid mode the action has nowhere of its own to stand: ui-spec §5.3 gives
   * the tile a cover and two lines of caption and nothing beside them, so the
   * control is an absolutely-positioned overlay lying **on top of** the volume's
   * own `<button>` (`VolumeTile`). Two controls occupying one 66px square is the
   * arrangement that produced the touch defect this component was fixed for, and
   * it is the arrangement where "the right thing happened" and "only the right
   * thing happened" come apart.
   *
   * The two tests above assert the write and would still pass if activating the
   * action *also* opened the volume — a toggle nested inside the volume button,
   * or an overlay that lets the click through to it, records exactly the same
   * PUT on its way to the viewer. `LocationProbe` renders only on
   * `/series/:sid/books/:bid`, so its continued absence is the assertion that
   * the reader stayed put.
   */
  it('marks the volume read without also opening it', async () => {
    const recorded = await setup()
    const grid = screen.getByTestId('volume-grid')
    // `01권.zip` — the first unread openable volume, same tile as above.
    await userEvent.click(at(within(grid).getAllByRole('button', { name: '읽음 표시' }), 0))

    await waitFor(() => {
      expect(recorded.progressPuts).toHaveLength(1)
    })
    expect(at(recorded.progressPuts, 0).bid).toBe('bbbbbbbbbbbbbbbb')
    expect(screen.queryByTestId('location')).not.toBeInTheDocument()
  })

  /**
   * Regression: the write used to land and the screen used to keep showing `—`.
   *
   * `useSaveProgress` invalidates `books.detail` + `continueReading` and nothing
   * else — correct for the viewer, wrong here, because this screen reads
   * `series.detail(sid)`. `useDeleteProgress` does invalidate `series.all`,
   * which is why only the 읽음 표시 direction was broken. Asserting the request
   * body (the test above) cannot see it; only re-reading the row can.
   */
  it('re-reads the series so the row flips to 완독 after the PUT lands', async () => {
    useUiStore.setState({ view: 'list' })
    const recorded = newRecorded()
    let current = detailOf()

    server.use(
      // Listed first so they win over `baseHandlers`' static pair.
      http.get(`${ORIGIN}/api/series/:sid`, () => HttpResponse.json(current)),
      http.put(`${ORIGIN}/api/books/:bid/progress`, async ({ params, request }) => {
        const bid = String(params.bid)
        recorded.progressPuts.push({ bid, body: await request.json() })
        const progress = {
          book_id: bid,
          series_id: SERIES_ID,
          last_page: 187,
          page_count: 187,
          completed: true,
          started_at: 1,
          updated_at: 2,
          stale: false,
        }
        current = {
          ...current,
          books: current.books.map((b) => (b.id === bid ? { ...b, progress } : b)),
        }
        return HttpResponse.json(progress)
      }),
      ...baseHandlers(current, recorded),
    )
    renderPage()
    await screen.findByRole('heading', { name: current.name })

    // `01권.zip` — unread, the first 읽음 표시 in the list.
    const before = at(screen.getAllByTestId('volume-row'), 1)
    expect(within(before).getByText('—')).toBeInTheDocument()

    await userEvent.click(within(before).getByRole('button', { name: '읽음 표시' }))
    await waitFor(() => {
      expect(recorded.progressPuts).toHaveLength(1)
    })

    await waitFor(() => {
      const after = at(screen.getAllByTestId('volume-row'), 1)
      expect(within(after).getByText('완독')).toBeInTheDocument()
      expect(within(after).getByRole('button', { name: '읽음 해제' })).toBeInTheDocument()
    })
  })
})

// ---------------------------------------------------------------------------
// List geometry — ui-spec §5.4 (one template) and §7 (responsive)
// ---------------------------------------------------------------------------

describe('volume list geometry (ui-spec §5.4, §7)', () => {
  beforeEach(() => {
    useUiStore.setState({ view: 'list' })
  })

  it('gives every row one template, whatever its action cell holds', async () => {
    await setup()
    const rows = screen.getAllByTestId('volume-row')
    // The fixture covers all three shapes a content-sized trailing `auto` used
    // to size differently: 읽음 해제, 읽음 표시 and an unopenable
    // row whose action cell is empty (zero) — which slid 형식/페이지 수/용량/
    // 진행률 by up to 45px between neighbouring rows.
    expect(rows).toHaveLength(5)
    const templates = new Set(rows.map((row) => row.className))
    expect(templates.size).toBe(1)
    expect(at(rows, 0)).toHaveClass(...VOLUME_ROW_GRID_CLASS.split(/\s+/))
  })

  it('sizes every track explicitly — no track may depend on its content', () => {
    for (const template of [
      VOLUME_ROW_COLUMNS_BASE,
      VOLUME_ROW_COLUMNS_MD,
      VOLUME_ROW_COLUMNS_LG,
    ]) {
      const contentSized = template
        .split(' ')
        .filter((track) => ['auto', 'min-content', 'max-content', 'fit-content'].includes(track))
      expect(contentSized).toEqual([])
    }
  })

  it('keeps the emitted classes and the measured templates in step', () => {
    const encode = (template: string): string => template.replaceAll(' ', '_')
    expect(VOLUME_ROW_GRID_CLASS).toContain(`grid-cols-[${encode(VOLUME_ROW_COLUMNS_BASE)}]`)
    expect(VOLUME_ROW_GRID_CLASS).toContain(`md:grid-cols-[${encode(VOLUME_ROW_COLUMNS_MD)}]`)
    expect(VOLUME_ROW_GRID_CLASS).toContain(`lg:grid-cols-[${encode(VOLUME_ROW_COLUMNS_LG)}]`)
  })

  /**
   * The bug this pins: at 500px the fixed tracks plus gaps (480px) exceeded the
   * 456px row, so `minmax(0,1fr)` — the volume name — resolved to exactly 0 and
   * every row went anonymous. jsdom does no layout, so the arithmetic is done
   * here against the same numbers Tailwind emits (pinned by the test above).
   */
  it('never squeezes the volume-name track to zero (NFR-CMP-002)', () => {
    /** `VolumeList` `px-4` + `VolumeRow` `px-2`. */
    const PADDING_PX = 32 + 16
    /** ui-spec §7: off-canvas below 768, a 56px rail to 1023, then 240px fixed. */
    const shellPx = (viewport: number): number =>
      viewport < 768 ? 0 : viewport < 1024 ? 56 : 240

    const nameTrackPx = (viewport: number, template: string): number => {
      const tracks = template.split(' ')
      const fixed = tracks
        .filter((track) => track.endsWith('px'))
        .reduce((sum, track) => sum + Number(track.slice(0, -2)), 0)
      const gaps = (tracks.length - 1) * VOLUME_ROW_GAP_PX
      return viewport - shellPx(viewport) - PADDING_PX - fixed - gaps
    }

    // Wide enough for a real title, not merely non-zero.
    expect(nameTrackPx(400, VOLUME_ROW_COLUMNS_BASE)).toBeGreaterThan(96)
    expect(nameTrackPx(500, VOLUME_ROW_COLUMNS_BASE)).toBeGreaterThan(96)
    expect(nameTrackPx(767, VOLUME_ROW_COLUMNS_BASE)).toBeGreaterThan(96)
    expect(nameTrackPx(768, VOLUME_ROW_COLUMNS_MD)).toBeGreaterThan(96)
    expect(nameTrackPx(1023, VOLUME_ROW_COLUMNS_MD)).toBeGreaterThan(96)
    expect(nameTrackPx(1024, VOLUME_ROW_COLUMNS_LG)).toBeGreaterThan(96)
  })

  it('still renders 페이지 수 and 용량 at every width — they are hidden, not dropped', async () => {
    await setup()
    const list = screen.getByTestId('volume-list')
    // CSS-only responsive: the DOM (and every assertion about it) is
    // width-independent, and no volume loses a fact on a narrow screen that a
    // reader could not get by widening the window.
    expect(within(list).getAllByText('187p')).toHaveLength(2)
    expect(within(list).getByText('60 MB')).toBeInTheDocument()
    expect(at(within(list).getAllByText('187p'), 0)).toHaveClass('hidden', 'md:block')
    expect(within(list).getByText('60 MB')).toHaveClass('hidden', 'lg:block')
  })
})

describe('failure and empty states', () => {
  it('renders a message instead of a blank screen when the series is unknown', async () => {
    server.use(
      http.get(`${ORIGIN}/api/series/:sid`, () =>
        HttpResponse.json(errorEnvelope('not_found', 'unknown series'), { status: 404 }),
      ),
      http.get(`${ORIGIN}/api/settings`, () => HttpResponse.json(settings)),
      http.get(`${ORIGIN}/api/scan/status`, () => HttpResponse.json(scanStatusIdle)),
    )
    renderPage()
    expect(await screen.findByText('unknown series')).toBeInTheDocument()
  })

  it('shows the reason and refuses to offer a dead 읽기 시작 when nothing is readable (E-14)', async () => {
    // `[만화] 엔젤하트 전32권 완결.zip`: one book, `empty` (33 nested ZIPs, no
    // images — D-10), so the SERIES is `error` under ruling E-14. It used to
    // render 권 1 / 용량 0 KB / 진행률 0 % with no banner and an enabled primary
    // button that did nothing at all when clicked.
    await setup(
      detailOf({
        name: '[만화] 엔젤하트 전32권 완결.zip',
        status: 'error',
        error: 'no supported image entries',
        book_count: 1,
        total_bytes: 1_550_316_560,
        progress: {
          books_total: 1,
          books_completed: 0,
          books_started: 0,
          percent: 0,
          last_read_at: null,
          last_book_id: null,
          last_page: null,
        },
        books: [
          volume({
            id: 'ffffffffffffffff',
            name: '[만화] 엔젤하트 전32권 완결.zip',
            kind: 'zip',
            ord: 0,
            page_count: 0,
            total_bytes: 0,
            file_size: 1_550_316_560,
            status: 'empty',
            error: 'no supported image entries',
          }),
        ],
      }),
    )
    const header = screen.getByRole('banner')
    const banner = within(header).getByRole('alert')
    expect(banner).toHaveTextContent('no supported image entries')
    // 용량 is the container on disk, not the (zero) page bytes.
    expect(within(header).getByText('1.6 GB')).toBeInTheDocument()
    expect(within(header).getByRole('button', { name: '읽기 시작' })).toBeDisabled()
    expect(within(header).getByRole('button', { name: '처음부터 읽기' })).toBeDisabled()
    // 재스캔 stays live: it is the one action that can fix the series.
    expect(within(header).getByRole('button', { name: '이 시리즈 재스캔' })).toBeEnabled()
  })

  it('shows no error banner on a healthy series', async () => {
    await setup()
    expect(within(screen.getByRole('banner')).queryByRole('alert')).not.toBeInTheDocument()
    expect(
      within(screen.getByRole('banner')).getByRole('button', { name: '이어 읽기' }),
    ).toBeEnabled()
  })

  it('lists an empty series rather than hiding it (D-29)', async () => {
    await setup(
      detailOf({
        ...seriesSummary,
        books: [],
        book_count: 0,
        status: 'empty',
        error: 'no supported image entries',
        progress: {
          books_total: 0,
          books_completed: 0,
          books_started: 0,
          percent: 0,
          last_read_at: null,
          last_book_id: null,
          last_page: null,
        },
      }),
    )
    expect(screen.getByText('비어 있음')).toBeInTheDocument()
    expect(screen.getByText('no supported image entries')).toBeInTheDocument()
    expect(screen.getByText('0권')).toBeInTheDocument()
  })
})
