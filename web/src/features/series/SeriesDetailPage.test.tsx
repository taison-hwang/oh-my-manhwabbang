import '@testing-library/jest-dom/vitest'

import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { setupServer } from 'msw/node'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom'
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'

import {
  BOOK_COVER_CV,
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
import { ApiError } from '../../api/errors'
import { resetBasePath } from '../../api/urls'
import { queryKeys } from '../../api/queries'
import type { BookSummary, ReadingDir, SeriesDetail, Settings } from '../../api/types'
import { SERIES_DIR_STORAGE_KEY } from '../../store/seriesDir'
import { useUiStore } from '../../store/ui'
import { SeriesDetailPage } from './SeriesDetailPage'
import { rescanErrorMessage, seriesErrorMessage } from './seriesErrors'
import {
  VOLUME_ROW_COLUMNS_BASE,
  VOLUME_ROW_COLUMNS_LG,
  VOLUME_ROW_COLUMNS_MD,
  VOLUME_ROW_GAP_PX,
  VOLUME_ROW_GRID_CLASS,
} from './VolumeRow'
import { createCoverMock, failUnhandled, type CoverMock } from '../../test/msw'

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
  server.listen({ onUnhandledRequest: failUnhandled })

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

/**
 * The volume E-45 §6 newly made reachable: **ratio 1.0, `completed: false`.**
 *
 * A 190-page file shrank to 10 and the server clamped the reader to page 10, so
 * he has honestly seen all of what exists — while `progress.page_count` keeps
 * the 190 baseline that E-45 §2 stopped every write from overwriting. Before §6
 * changed the denominator the two fields were always equal and this state could
 * not occur; after it, the ratio is 1.0 and nobody has marked the volume read.
 */
const SHRUNK_VOLUME: BookSummary = volume({
  id: 'ffffffffffffffff',
  name: '군계(軍鷄) 11권.zip',
  kind: 'zip',
  ord: 5,
  page_count: 10,
  total_bytes: 1_200_000,
  file_size: 1_200_000,
  progress: {
    book_id: 'ffffffffffffffff',
    series_id: SERIES_ID,
    last_page: 10,
    page_count: 190,
    completed: false,
    started_at: 1,
    updated_at: 2,
    stale: true,
  },
})

function detailOf(overrides: Partial<SeriesDetail> = {}): SeriesDetail {
  return {
    ...seriesDetail,
    books: DUPLICATE_BOOKS,
    book_count: DUPLICATE_BOOKS.length,
    total_bytes: 3_650_722_201, // 3.7 GB (E-11: decimal)
    // 6 완독 of 25 volumes, one merely started. **The percent below deliberately
    // disagrees with those counts**, and after E-47 that is what makes the test
    // below sharp rather than what makes it wrong: `percent` is the server's
    // page-weighted answer and the screen has to take it, so a header that
    // re-derived 6/25 = 24 % locally would be caught here.
    progress: {
      books_total: 25,
      books_completed: 6,
      books_started: 1,
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
  /**
   * Every `GET /api/books/{bid}/cover` the screen asked for, in order — E-69
   * §2.8 moved the volume cards off `/thumbs/1`. A request for `/thumbs/…`
   * has no handler here and fails the test (`src/test/msw.ts`).
   */
  covers: { bid: string; w: string | null; v: string | null }[]
  /** The contract-keeping mock behind the covers and `PUT /api/series/{sid}/prefs`. */
  mock: CoverMock | null
}

function baseHandlers(detail: SeriesDetail, recorded: Recorded, rescanStatus = 202) {
  // E-69 §2.3: the series direction is the server's now, so the series GET reads
  // it back from what the PUT stored — a reload shows what was written.
  const mock = createCoverMock(ORIGIN, {
    books: detail.books,
    series: [detail.id],
    seriesDirs: { [detail.id]: detail.reading_direction },
  })
  recorded.mock = mock
  recorded.covers = mock.gets
  return [
    http.get(`${ORIGIN}/api/series/:sid`, () =>
      HttpResponse.json({
        ...detail,
        // E-69 §2.7: each volume as the server lists it now — its `cover_cv` moves
        // with the effective direction.
        books: detail.books.map((book) => mock.summaryOf(book.id)),
        reading_direction: mock.seriesDirs.get(detail.id) ?? null,
      }),
    ),
    // prd UI-002 / design.md 화면 2: every volume carries its own picture — since
    // E-69 §2.8 the book cover, not page 1's thumbnail.
    ...mock.handlers,
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

function renderPage(client = newClient()): QueryClient {
  // A fresh client each time: a second render is a reload, and reads the server again.
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
  return client
}

function newClient(): QueryClient {
  return new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
}

function newRecorded(): Recorded {
  return { rescans: [], progressPuts: [], progressDeletes: [], covers: [], mock: null }
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
  it('is a card, not a band with a rule under it (E-32)', async () => {
    await setup()
    const header = screen.getByRole('banner')
    expect(header).toHaveClass('m-4', 'rounded-pill', 'bg-surface', 'shadow-md')
    expect(header.classList.contains('border-b-2')).toBe(false)
    expect(header.classList.contains('border-rule-strong')).toBe(false)
  })

  /**
   * The outline (10-23 accessibility audit #5): the series name was an `<h2>`
   * with no `<h1>` above it and 권 목록 an `<h6>` under it, a jump of four
   * levels. The name is what the screen is, so it is the one level-1 heading;
   * 권 목록 is a section of it. The look stays the old tags' (base.css classes).
   */
  it('is the screen’s one level-1 heading, with 권 목록 at level 2 (a11y audit #5)', async () => {
    await setup()
    const titles = screen.getAllByRole('heading', { level: 1 })
    expect(titles).toHaveLength(1)
    expect(within(screen.getByRole('banner')).getByRole('heading', { level: 1 })).toBe(titles[0])
    expect(screen.getByRole('heading', { level: 2, name: '권 목록' })).toBeInTheDocument()
  })

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

  /**
   * **This assertion is E-47's, and it is the reverse of the one it replaces.**
   *
   * It used to read "aggregates over completed volumes, not the server percent":
   * the header computed `books_completed / books_total` itself, because
   * FR-STT-002 said 완독 수 기준 and deriving it on the screen made that
   * assertable. E-47 rewrote the requirement to count pages, and the parts of
   * that sum — `pages_read`, `pages_total` — are not on the wire. So the screen
   * cannot derive it, and anything it derived would be the old rule wearing the
   * new name while the library's card (which reads `percent`) said something
   * else about the same series.
   *
   * The fixture's `percent` disagrees with its book counts on purpose, so this
   * fails the moment the local formula comes back.
   */
  it('takes 진행률 from the server percent, page-weighted (FR-STT-002, E-47)', async () => {
    await setup()
    expect(screen.getByText('99%')).toBeInTheDocument()
    // 6/25 = 24 %: the pre-E-47 formula, which must not reappear.
    expect(screen.queryByText('24%')).not.toBeInTheDocument()
    expect(
      screen.getByRole('progressbar', { name: '[만화] 군계 1~25 진행률' }),
    ).toHaveAttribute('aria-valuenow', '99')
  })

  it('offers 처음부터 읽기 / 이어 읽기 / 이 시리즈 재스캔', async () => {
    await setup()
    expect(screen.getByRole('button', { name: '처음부터 읽기' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '이어 읽기' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '이 시리즈 재스캔' })).toBeInTheDocument()
  })

  it('hides 이 시리즈 재스캔 from a non-admin oidc session (E-74 §2.10)', async () => {
    const recorded = newRecorded()
    server.use(...baseHandlers(detailOf(), recorded, 202))
    const client = newClient()
    client.setQueryData(queryKeys.auth.status, {
      auth_required: true,
      authenticated: true,
      mode: 'oidc',
      end_session_available: false,
      user: { sub: 'x', is_admin: false },
    })
    renderPage(client)
    await screen.findByRole('button', { name: '처음부터 읽기' })
    expect(screen.queryByRole('button', { name: '이 시리즈 재스캔' })).toBeNull()
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

  /**
   * **E-69 §2.3 reverses what this asserted.** Under C-9 / E-33 §2 the `.seg`
   * was a client-only seed: the change went to `localStorage` and the test
   * pinned that nothing reached the server. The series' direction is the
   * server's now — the change is a `PUT /api/series/{sid}/prefs`, nothing is
   * written to `localStorage`, and a reload reads the server's value back.
   */
  it('shows 기본 for a series with none, and writes a change to the server (E-69 §2.3)', async () => {
    const recorded = await setup()
    const group = screen.getByRole('radiogroup', { name: '읽기 방향' })
    // `settings.reading_direction` is `ltr` and the series has none of its own:
    // that is 기본, not L→R — which would be the series pinned to L→R.
    await waitFor(() => {
      expect(within(group).getByRole('radio', { name: '기본 (L→R)' })).toBeChecked()
    })
    expect(within(group).getByRole('radio', { name: 'L→R' })).not.toBeChecked()

    await userEvent.click(within(group).getByRole('radio', { name: 'R→L' }))
    await waitFor(() => {
      expect(recorded.mock?.seriesPrefsPuts).toEqual([
        { sid: SERIES_ID, body: { reading_direction: 'rtl' } },
      ])
    })
    await waitFor(() => {
      expect(within(group).getByRole('radio', { name: 'R→L' })).toBeChecked()
    })
    expect(localStorage.getItem(SERIES_DIR_STORAGE_KEY)).toBeNull()
    // PUT /api/settings has no handler: the global default was not touched.
  })

  it('reads the series’ direction back from the server after a reload', async () => {
    const recorded = await setup()
    await userEvent.click(
      within(screen.getByRole('radiogroup', { name: '읽기 방향' })).getByRole('radio', { name: 'R→L' }),
    )
    await waitFor(() => {
      expect(recorded.mock?.seriesPrefsPuts).toHaveLength(1)
    })
    cleanup()
    renderPage()
    await screen.findByRole('heading', { name: seriesDetail.name })
    const group = await screen.findByRole('radiogroup', { name: '읽기 방향' })
    await waitFor(() => {
      expect(within(group).getByRole('radio', { name: 'R→L' })).toBeChecked()
    })
  })

  it.each([
    ['rtl', 'ltr', 'R→L'],
    ['ltr', 'rtl', 'L→R'],
  ] as const)(
    'shows the series’ own %s over a global %s (E-69 §2.3)',
    async (own, global, label) => {
      server.use(...baseHandlers(detailOf({ reading_direction: own }), newRecorded(), 202))
      server.use(
        http.get(`${ORIGIN}/api/settings`, () =>
          HttpResponse.json({ ...settings, reading_direction: global }),
        ),
      )
      renderPage()
      const group = await screen.findByRole('radiogroup', { name: '읽기 방향' })
      await waitFor(() => {
        expect(within(group).getByRole('radio', { name: label })).toBeChecked()
      })
      // And it stays there once the settings have landed.
      await new Promise((resolve) => setTimeout(resolve, 30))
      expect(within(group).getByRole('radio', { name: label })).toBeChecked()
    },
  )

  it.each([
    [500, '읽기 방향을 저장하지 못했습니다. 잠시 후 다시 시도하세요.'],
    [404, '라이브러리에 없는 시리즈라 읽기 방향을 저장하지 못했습니다. 화면을 새로 고친 뒤 다시 시도하세요.'],
    [503, '서버가 지금 읽기 방향을 저장할 수 없습니다. 서버 상태를 확인한 뒤 다시 시도하세요.'],
  ] as const)('says a refused write (%i) in Korean, picked by status, and goes back', async (status, sentence) => {
    await setup()
    server.use(
      http.put(`${ORIGIN}/api/series/:sid/prefs`, () =>
        HttpResponse.json(errorEnvelope('internal', 'database is locked'), { status }),
      ),
    )
    const group = screen.getByRole('radiogroup', { name: '읽기 방향' })
    await userEvent.click(within(group).getByRole('radio', { name: 'R→L' }))

    expect(await screen.findByText(sentence)).toBeInTheDocument()
    // The envelope's English message is not for the reader (seriesErrors.ts).
    expect(screen.queryByText(/database is locked/)).not.toBeInTheDocument()
    // Back on the server's value: none of its own, so 기본.
    expect(within(group).getByRole('radio', { name: '기본 (L→R)' })).toBeChecked()
  })

  it('names the global default in 기본 even when that is not the fallback', async () => {
    // The fixture's `ltr` is also what the page falls back to with no settings
    // at all, so the case above cannot tell the two apart. `rtl` can. The
    // settings are held until the page is up, so they land after its first
    // render — a page that froze the label it first drew would stay wrong.
    let release = (): void => undefined
    const held = new Promise<void>((resolve) => {
      release = resolve
    })
    server.use(...baseHandlers(detailOf(), newRecorded(), 202))
    server.use(
      http.get(`${ORIGIN}/api/settings`, async () => {
        await held
        return HttpResponse.json({ ...settings, reading_direction: 'rtl' })
      }),
    )
    renderPage()
    await screen.findByRole('heading', { name: seriesDetail.name })
    const group = await screen.findByRole('radiogroup', { name: '읽기 방향' })
    // While the settings are in flight 기본 is checked and names nothing — it
    // does not guess a global it has not read.
    expect(within(group).getByRole('radio', { name: '기본' })).toBeChecked()

    release()
    await waitFor(() => {
      expect(within(group).getByRole('radio', { name: '기본 (R→L)' })).toBeChecked()
    })
    expect(within(group).getByRole('radio', { name: 'R→L' })).not.toBeChecked()
    expect(within(group).getByRole('radio', { name: 'L→R' })).not.toBeChecked()
  })

  /**
   * **E-69 §2.3, ui-spec §5.1: three values.** With two, a series that follows
   * the global default has the matching option already checked, so the reader
   * cannot pin the series to it — re-clicking a checked radio does nothing a
   * keyboard or screen-reader user can perceive. `기본` is the series' `null`,
   * named after the global value it follows now.
   */
  describe('the three-value 읽기 방향 .seg (E-69 §2.3)', () => {
    function seg(): HTMLElement {
      const group = document.querySelector<HTMLElement>('[aria-label="읽기 방향"]')
      if (group === null) throw new Error('no 읽기 방향 group')
      return group
    }
    function option(value: 'default' | 'ltr' | 'rtl'): HTMLElement {
      const label = seg().querySelector<HTMLElement>(`label[data-value="${value}"]`)
      if (label === null) throw new Error(`no ${value} option`)
      return label
    }
    function checkedValue(): string | null {
      return seg().querySelector('label[data-checked="true"]')?.getAttribute('data-value') ?? null
    }

    it('has 기본 · L→R · R→L in that order, and 기본 checked for a series with none', async () => {
      await setup()
      await waitFor(() => {
        expect(option('default')).toHaveTextContent('기본 (L→R)')
      })
      expect([...seg().querySelectorAll('label')].map((l) => l.getAttribute('data-value'))).toEqual([
        'default',
        'ltr',
        'rtl',
      ])
      expect(option('default')).toHaveAttribute('data-checked', 'true')
      expect(option('ltr')).toHaveAttribute('data-checked', 'false')
      expect(option('rtl')).toHaveAttribute('data-checked', 'false')
    })

    it('renames 기본 when the global default changes', async () => {
      const recorded = newRecorded()
      server.use(...baseHandlers(detailOf(), recorded))
      const client = renderPage()
      await screen.findByRole('heading', { name: seriesDetail.name })
      await waitFor(() => {
        expect(option('default')).toHaveTextContent('기본 (L→R)')
      })
      // What a settings write or re-read lands in the cache.
      act(() => {
        client.setQueryData<Settings>(queryKeys.settings, { ...settings, reading_direction: 'rtl' })
      })
      // TanStack notifies its observers on a timer, not inside `act`.
      await waitFor(() => {
        expect(option('default')).toHaveTextContent('기본 (R→L)')
      })
      expect(checkedValue()).toBe('default')
    })

    it('choosing 기본 PUTs exactly {reading_direction: null}, and a reload still shows 기본', async () => {
      const recorded = await setup(detailOf({ reading_direction: 'rtl' }))
      await waitFor(() => {
        expect(checkedValue()).toBe('rtl')
      })
      await userEvent.click(within(seg()).getByRole('radio', { name: '기본 (L→R)' }))
      await waitFor(() => {
        expect(recorded.mock?.seriesPrefsPuts).toHaveLength(1)
      })
      const body = recorded.mock?.seriesPrefsPuts[0]?.body
      expect(body).toStrictEqual({ reading_direction: null })
      expect(recorded.mock?.seriesDirs.get(SERIES_ID)).toBeNull()
      await waitFor(() => {
        expect(checkedValue()).toBe('default')
      })

      cleanup()
      renderPage()
      await screen.findByRole('heading', { name: seriesDetail.name })
      await waitFor(() => {
        expect(option('default')).toHaveAttribute('data-checked', 'true')
      })
    })

    it.each([
      ['ltr', 'L→R'],
      ['rtl', 'R→L'],
    ] as const)(
      'an explicit %s is the series’ own, and stays when the global default changes',
      async (value, label) => {
        let global: ReadingDir = 'ltr'
        const recorded = newRecorded()
        server.use(...baseHandlers(detailOf(), recorded))
        server.use(
          http.get(`${ORIGIN}/api/settings`, () =>
            HttpResponse.json({ ...settings, reading_direction: global }),
          ),
        )
        const client = renderPage()
        await screen.findByRole('heading', { name: seriesDetail.name })
        await waitFor(() => {
          expect(option('default')).toHaveTextContent('기본 (L→R)')
        })
        // L→R is the global value too: only the third option makes pinning it possible.
        await userEvent.click(within(seg()).getByRole('radio', { name: label }))
        await waitFor(() => {
          expect(recorded.mock?.seriesPrefsPuts.map((p) => p.body)).toEqual([
            { reading_direction: value },
          ])
        })
        await waitFor(() => {
          expect(checkedValue()).toBe(value)
        })

        // The global default goes the other way (another tab, the settings dialog).
        global = 'rtl'
        recorded.mock?.setGlobalDirection('rtl')
        act(() => {
          client.setQueryData<Settings>(queryKeys.settings, { ...settings, reading_direction: 'rtl' })
        })
        await waitFor(() => {
          expect(option('default')).toHaveTextContent('기본 (R→L)')
        })
        expect(checkedValue()).toBe(value)

        cleanup()
        renderPage()
        await screen.findByRole('heading', { name: seriesDetail.name })
        await waitFor(() => {
          expect(option('default')).toHaveTextContent('기본 (R→L)')
        })
        expect(checkedValue()).toBe(value)
      },
    )

    it('moves between the three with the arrow keys, writing each — a native radio group', async () => {
      const recorded = await setup()
      await waitFor(() => {
        expect(checkedValue()).toBe('default')
      })
      within(seg()).getByRole('radio', { name: '기본 (L→R)' }).focus()

      await userEvent.keyboard('{ArrowRight}')
      await waitFor(() => {
        expect(checkedValue()).toBe('ltr')
      })
      await userEvent.keyboard('{ArrowRight}')
      await waitFor(() => {
        expect(checkedValue()).toBe('rtl')
      })
      await userEvent.keyboard('{ArrowRight}')
      await waitFor(() => {
        expect(checkedValue()).toBe('default')
      })
      await waitFor(() => {
        expect(recorded.mock?.seriesPrefsPuts.map((p) => p.body)).toEqual([
          { reading_direction: 'ltr' },
          { reading_direction: 'rtl' },
          { reading_direction: null },
        ])
      })
      // In order, so the last key is the server's value.
      expect(recorded.mock?.seriesDirs.get(SERIES_ID)).toBeNull()
      expect(document.activeElement).toBe(within(seg()).getByRole('radio', { name: '기본 (L→R)' }))
    })
  })
})

describe('a series the server cannot open', () => {
  /**
   * The error state replaces the whole screen, header and all, so its band is
   * the only heading left: it has to be the level-1 one, not an `<h3>` under
   * nothing (10-23 a11y audit).
   */
  it('titles the error state as the screen’s one level-1 heading', async () => {
    server.use(...baseHandlers(detailOf(), newRecorded()))
    server.use(
      http.get(`${ORIGIN}/api/series/:sid`, () =>
        HttpResponse.json(errorEnvelope('not_found', 'series not found'), { status: 404 }),
      ),
    )
    renderPage()

    const title = await screen.findByRole('heading', { level: 1, name: '화면을 열 수 없습니다' })
    expect(screen.getAllByRole('heading', { level: 1 })).toEqual([title])
    expect(screen.queryByRole('banner')).not.toBeInTheDocument()
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

  /**
   * **E-69 §2.8 changed what this asserts.** It pinned `/thumbs/1?v={cv}` —
   * page 1's thumbnail, which is what a volume showed before the reader could
   * choose a cover. The card now shows the *book cover*: `GET
   * /api/books/{bid}/cover`, versioned by `cover_cv` (which turns over with the
   * chosen region and the effective direction, §2.7), not by the content `cv`.
   * The original regression — no request at all, stripes for ever — is still
   * what the `<img>` count catches.
   */
  it('requests the book cover for every openable volume (prd UI-002, E-69 §2.8)', async () => {
    const recorded = await setup()
    // Three openable volumes ⇒ three <img> elements inside the grid.
    await waitFor(() => {
      expect(screen.getByTestId('volume-grid').querySelectorAll('img')).toHaveLength(3)
    })
    const asked = recorded.covers
    expect(asked.map((t) => t.bid).sort()).toEqual(
      ['aaaaaaaaaaaaaaaa', 'bbbbbbbbbbbbbbbb', 'eeeeeeeeeeeeeeee'].sort(),
    )
    // `w` comes from THUMB_WIDTHS (§0.4); `v` is the cover's version, not `cv`.
    expect(BOOK_COVER_CV).not.toBe(bookSummary.cv)
    for (const t of asked) {
      expect(t.w).toBe('400')
      expect(t.v).toBe(BOOK_COVER_CV)
    }
    for (const img of screen.getByTestId('volume-grid').querySelectorAll('img')) {
      expect(img.getAttribute('src')).toMatch(
        new RegExp(`^/api/books/[a-z2-7]{16}/cover\\?w=400&v=${BOOK_COVER_CV}$`),
      )
    }
  })

  /**
   * E-69 §2.7: a volume card's `?v=` is the `cover_cv` its series detail listed,
   * and that turns over on things this tab did not do — here another browser set
   * the series to R→L, so every default cover's inputs changed. The card on screen
   * asks with the old `v`, gets `409 stale_version`, and only re-reading **the
   * series** (where the card got `cover_cv`) can give it the new one; retrying the
   * same url cannot. So the card has to name its series to the image hook — a card
   * that did not would retry once, 409 again, and stay on the striped placeholder.
   */
  it('a card on a stale cover_cv re-reads its series and shows the cover at the new one (E-69 §2.7)', async () => {
    const recorded = newRecorded()
    const detail = detailOf()
    server.use(...baseHandlers(detail, recorded))
    let seriesGets = 0
    server.use(
      // Counts, then falls through to the harness's series GET.
      http.get(`${ORIGIN}/api/series/${SERIES_ID}`, () => {
        seriesGets += 1
      }),
    )
    const mock = recorded.mock
    if (mock === null) throw new Error('no mock')
    // This tab read the series a moment ago — fresh enough that it would not ask again…
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false, staleTime: 60_000 }, mutations: { retry: false } },
    })
    client.setQueryData<SeriesDetail>(queryKeys.series.detail(SERIES_ID), {
      ...detail,
      books: detail.books.map((book) => mock.summaryOf(book.id)),
    })
    const openable = ['aaaaaaaaaaaaaaaa', 'bbbbbbbbbbbbbbbb', 'eeeeeeeeeeeeeeee']
    const before = new Map(openable.map((bid) => [bid, mock.coverCvOf(bid)] as const))
    // …and then another browser set the series to R→L.
    mock.seriesDirs.set(SERIES_ID, 'rtl')
    const after = new Map(openable.map((bid) => [bid, mock.coverCvOf(bid)] as const))
    for (const bid of openable) expect(after.get(bid)).not.toBe(before.get(bid))

    renderPage(client)
    await screen.findByRole('heading', { name: detail.name })

    await waitFor(() => {
      const srcs = [...screen.getByTestId('volume-grid').querySelectorAll('img')].map((img) =>
        img.getAttribute('src'),
      )
      expect(srcs.sort()).toEqual(
        openable.map((bid) => `/api/books/${bid}/cover?w=400&v=${after.get(bid) ?? ''}`).sort(),
      )
    })
    for (const bid of openable) {
      const asked = recorded.covers.filter((g) => g.bid === bid).map((g) => g.v)
      // The old version first (409), then the new one (200).
      expect(asked[0]).toBe(before.get(bid))
      expect(asked.at(-1)).toBe(after.get(bid))
    }
    // Three stale cards: their series is re-read once or twice, not once a card. The
    // first 409 starts a read; one that comes back while that read is out began
    // before it and is owed one more read after it (see the test below) — and only one,
    // however many such 409s there are.
    expect(seriesGets).toBeGreaterThanOrEqual(1)
    expect(seriesGets).toBeLessThanOrEqual(2)
    // And the header shows what that re-read brought.
    expect(
      document.querySelector('[aria-label="읽기 방향"] label[data-checked="true"]')?.getAttribute('data-value'),
    ).toBe('rtl')
  })

  /**
   * The same change, but this tab began re-reading the series a moment **before**
   * another browser set it to R→L: that read answers the old versions. A 409 that
   * only joined it would land them again — both of each card's attempts refused,
   * nothing left to re-read the series, every card striped for good. A read that
   * starts after the 409s has to follow the one that was out, and one is enough for
   * the whole grid.
   */
  it('a card whose series re-read was already out when its 409 came still ends on the new cover_cv (E-69 §2.7)', async () => {
    const recorded = newRecorded()
    const detail = detailOf()
    server.use(...baseHandlers(detail, recorded))
    const mock = recorded.mock
    if (mock === null) throw new Error('no mock')
    const openable = ['aaaaaaaaaaaaaaaa', 'bbbbbbbbbbbbbbbb', 'eeeeeeeeeeeeeeee']
    const before = new Map(openable.map((bid) => [bid, mock.coverCvOf(bid)] as const))
    let seriesGets = 0
    let changed = (): void => undefined
    const change = new Promise<void>((resolve) => {
      changed = resolve
    })
    let release = (): void => undefined
    const held = new Promise<void>((resolve) => {
      release = resolve
    })
    server.use(
      http.get(`${ORIGIN}/api/series/${SERIES_ID}`, async () => {
        seriesGets += 1
        // Every later read falls through to the harness: the server as it is now.
        if (seriesGets > 1) return undefined
        // The first read is answered as the server stood when it was asked…
        const answer: SeriesDetail = {
          ...detail,
          books: detail.books.map((book) => mock.summaryOf(book.id)),
          reading_direction: mock.seriesDirs.get(SERIES_ID) ?? null,
        }
        // …and then another browser set the series to R→L, while it was on its way.
        mock.seriesDirs.set(SERIES_ID, 'rtl')
        changed()
        await held
        return HttpResponse.json(answer)
      }),
      // No card asks before the change; then the contract mock answers.
      http.get(`${ORIGIN}/api/books/:bid/cover`, async () => {
        await change
        return undefined
      }),
    )
    // `staleTime` 0: the series this tab already holds is re-read on mount.
    const client = newClient()
    client.setQueryData<SeriesDetail>(queryKeys.series.detail(SERIES_ID), {
      ...detail,
      books: detail.books.map((book) => mock.summaryOf(book.id)),
    })

    renderPage(client)
    await screen.findByRole('heading', { name: detail.name })
    await change
    const after = new Map(openable.map((bid) => [bid, mock.coverCvOf(bid)] as const))
    for (const bid of openable) expect(after.get(bid)).not.toBe(before.get(bid))

    // Both of every card's attempts are refused while the first read is still out.
    await waitFor(
      () => {
        for (const bid of openable) {
          const asked = recorded.covers.filter((g) => g.bid === bid).map((g) => g.v)
          expect(asked).toEqual([before.get(bid), before.get(bid)])
        }
      },
      { timeout: 5_000 },
    )
    release()

    await waitFor(
      () => {
        const srcs = [...screen.getByTestId('volume-grid').querySelectorAll('img')].map((img) =>
          img.getAttribute('src'),
        )
        expect(srcs.sort()).toEqual(
          openable.map((bid) => `/api/books/${bid}/cover?w=400&v=${after.get(bid) ?? ''}`).sort(),
        )
      },
      { timeout: 5_000 },
    )
    // The read that was out, and one after it — not one per card, nor per 409.
    expect(seriesGets).toBe(2)
   }, 15_000)

  it('never asks for the cover of a volume that cannot be opened (FR-IDX-010)', async () => {
    const recorded = await setup()
    await waitFor(() => {
      expect(recorded.covers.length).toBeGreaterThan(0)
    })
    // `cccccccccccccccc` is truncated and `dddddddddddddddd` is encrypted:
    // neither can be opened, so neither may cost a request.
    expect(recorded.covers.map((t) => t.bid)).not.toContain('cccccccccccccccc')
    expect(recorded.covers.map((t) => t.bid)).not.toContain('dddddddddddddddd')
  })

  /**
   * E-32 removed the 1px border every tile carried, which is also where the
   * volume's *tone* used to be drawn. Three of the four tones were saying
   * "1px of divider", which said nothing; the fourth — broken — has to stay
   * visible, so it becomes the accent-300 ring the prototype computes for it.
   * Without that the only mark on a broken volume is the scrim, and a raised
   * card that merely declines to lift is not a signal.
   */
  it('rings a broken volume instead of bordering it, and lifts the openable ones (E-32)', async () => {
    await setup()
    const grid = screen.getByTestId('volume-grid')
    const rings = grid.querySelectorAll('.ring-accent-300')
    // `cccccccccccccccc` truncated + `dddddddddddddddd` encrypted.
    expect(rings).toHaveLength(2)
    for (const box of grid.querySelectorAll('.fallback-cover')) {
      expect(box).toHaveClass('rounded-md', 'shadow-md')
      expect(box.classList.contains('border')).toBe(false)
      expect(box.classList.contains('group-hover:border-accent')).toBe(false)
    }
    expect(grid.querySelectorAll('.group-hover\\:shadow-lg')).toHaveLength(3)
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
    expect(screen.getByText('비밀번호가 필요한 압축 파일')).toBeInTheDocument()
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

  it('is a card of hover chips, not a ruled table (E-32)', async () => {
    await setup()
    const list = screen.getByTestId('volume-list')
    expect(list).toHaveClass('rounded-lg', 'bg-surface', 'shadow-md')
    for (const row of within(list).getAllByTestId('volume-row')) {
      expect(row).toHaveClass('row-chip')
      expect(row.classList.contains('border-b')).toBe(false)
      expect(row.classList.contains('hover:bg-row-hover')).toBe(false)
    }
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

  it('carries the book cover in the row thumb cell too (prd UI-002, E-69 §2.8)', async () => {
    const recorded = await setup()
    await waitFor(() => {
      expect(screen.getByTestId('volume-list').querySelectorAll('img')).toHaveLength(3)
    })
    // §0.4's "List-row thumb — 24 CSS px ⇒ w=120": the row is 20×30, so it must
    // not pull the tile's 400px image down to a 20px box.
    expect(recorded.covers).toHaveLength(3)
    for (const t of recorded.covers) {
      expect(t.w).toBe('120')
      expect(t.v).toBe(BOOK_COVER_CV)
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
    const busy = new ApiError({ status: 409, code: 'conflict', message: 'a scan is already running' })
    expect(notice).toHaveTextContent(rescanErrorMessage(busy))
    expect(notice, 'the envelope message is English').not.toHaveTextContent(busy.message)
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

  it('passes over volumes that cannot be opened when the list starts with them', async () => {
    // In the default order the first volume is openable, so "the first" and
    // "the first openable" are the same book there. Lead with the two that are
    // not (07권 error, 09권 encrypted).
    const [v01, v01zip, v07, v09, v10] = DUPLICATE_BOOKS
    if (!v01 || !v01zip || !v07 || !v09 || !v10) throw new Error('fixture shape changed')
    await setup(detailOf({ books: [v07, v09, v01zip, v01, v10] }))
    await userEvent.click(screen.getByRole('button', { name: '처음부터 읽기' }))
    expect(screen.getByTestId('location')).toHaveTextContent(
      `/series/${SERIES_ID}/books/bbbbbbbbbbbbbbbb?page=1`,
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

/**
 * **라벨 · 색조 · 버튼 agree on one screen** (E-45 §6, 따름정리의 따름정리).
 *
 * §6 moved the denominator to the index's current length, which made a ratio of
 * exactly 1.0 reachable for a volume nobody has marked read. The word `완독`,
 * the terminal badge and the finished ink all keyed off that ratio, so one book
 * read three ways at once: `완독` with a done-toned bar in the list, `읽는 중`
 * on the grid, and `읽음 표시` on the button of both — E-12's state/action
 * confusion, arrived at from a direction §6 had not looked in.
 *
 * The ratio itself is left alone; §6 ruled it honest. What is asserted here is
 * that the three *readings* of it cannot disagree, on **both** surfaces — the
 * list row and the grid tile — because the mismatch lived in the gap between
 * them and a check on either one alone would have missed it.
 */
describe('a shrunk volume reads the same on both surfaces (E-45 §6)', () => {
  const withShrunk = (): SeriesDetail =>
    detailOf({ books: [...DUPLICATE_BOOKS, SHRUNK_VOLUME], book_count: 6 })

  const shrunkBar = (scope: HTMLElement): HTMLElement =>
    within(scope).getByRole('progressbar', { name: '군계(軍鷄) 11권.zip 진행률' })

  it('says 100%, not 완독, and tones the row to match its button', async () => {
    useUiStore.setState({ view: 'list' })
    await setup(withShrunk())
    const row = at(screen.getAllByTestId('volume-row'), 5)

    // The label is the honest number, and it is not the terminal word…
    expect(within(row).getByText('100%')).toBeInTheDocument()
    expect(within(row).queryByText('완독')).not.toBeInTheDocument()
    // …so it is not drawn as a terminal badge either (ui-spec §2.5 permits the
    // solid accent field for 완독 and the 손상 family, and for nothing else).
    const state = within(row).getByText('100%')
    expect(state).toHaveClass('text-accent-text')
    expect(state.classList.contains('bg-accent')).toBe(false)

    // The bar is full — that is the ratio — but in the unfinished ink.
    const bar = shrunkBar(row)
    expect(bar).toHaveAttribute('aria-valuenow', '100')
    const fill = bar.firstElementChild
    expect(fill).toHaveClass('bg-accent-fill')
    expect(fill?.classList.contains('bg-ink')).toBe(false)

    // And the control, which has always answered `completed`, still does.
    expect(within(row).getByRole('button', { name: '읽음 표시' })).toBeInTheDocument()
  })

  it('draws the same volume as 읽는 중 on the grid, with the same button', async () => {
    useUiStore.setState({ view: 'grid' })
    await setup(withShrunk())
    const tile = at(screen.getAllByTestId('volume-tile'), 5)

    // `tone === 'started'` is what puts a bar on a tile at all; a finished tile
    // has none. Full, and present — the grid's way of saying 읽는 중.
    expect(shrunkBar(tile)).toHaveAttribute('aria-valuenow', '100')
    // The tile's volume number dims only for a finished volume.
    const number = within(tile).getByText('6')
    expect(number).toHaveClass('text-ink-faint')
    expect(number.classList.contains('text-ink-dim')).toBe(false)
    expect(within(tile).getByRole('button', { name: '읽음 표시' })).toBeInTheDocument()
    expect(within(tile).queryByText('완독')).not.toBeInTheDocument()
  })

  it('still says 완독 on both surfaces once the volume really is completed', async () => {
    // The discriminator: the word did not disappear, it changed what it follows.
    const shrunkProgress = SHRUNK_VOLUME.progress
    if (shrunkProgress === null) throw new Error('the fixture must carry progress')
    const completed: BookSummary = {
      ...SHRUNK_VOLUME,
      progress: { ...shrunkProgress, completed: true },
    }
    useUiStore.setState({ view: 'list' })
    await setup(detailOf({ books: [...DUPLICATE_BOOKS, completed], book_count: 6 }))

    const row = at(screen.getAllByTestId('volume-row'), 5)
    const state = within(row).getByText('완독')
    expect(state).toHaveClass('bg-accent')
    expect(shrunkBar(row).firstElementChild).toHaveClass('bg-ink')
    expect(within(row).getByRole('button', { name: '읽음 해제' })).toBeInTheDocument()
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
    const unknown = new ApiError({ status: 404, code: 'not_found', message: 'unknown series' })
    expect(await screen.findByText(seriesErrorMessage(unknown))).toBeInTheDocument()
    expect(screen.queryByText(unknown.message), 'the envelope message is English').toBeNull()
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
