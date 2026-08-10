import '@testing-library/jest-dom/vitest'

import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { setupServer } from 'msw/node'
import { useState } from 'react'
import { MemoryRouter, Route, Routes, useLocation, useParams } from 'react-router-dom'
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'

import { continueResponse, ORIGIN, seriesSummary, SERIES_ID } from '../../api/fixtures'
import type { ContinueItem, SeriesSummary } from '../../api/types'
import { resetBasePath } from '../../api/urls'
import { CommandPalette } from './CommandPalette'

/**
 * FR-LIB-011 / FR-LIB-006 (WP-10 acceptance 5).
 *
 * The assertion that matters most is that the *server* answers the query
 * (C-10 / D-34): ui-spec §8.4's client-side filter is unimplementable once the
 * list is paginated, so a palette that filtered locally would pass a naive test
 * and fail on a real library. Every result here comes from `/api/series?q=`.
 */

/** `noUncheckedIndexedAccess` types an indexed query result as `T | undefined`. */
function at<T>(items: readonly T[], index: number): T {
  const item = items[index]
  if (item === undefined) throw new Error(`expected an element at index ${String(index)}`)
  return item
}

const server = setupServer()

let seriesRequests: string[] = []

beforeAll(() => {
  server.listen({ onUnhandledRequest: 'error' })
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

const HITS: SeriesSummary[] = [
  { ...seriesSummary, id: 'aaaaaaaaaaaaaaaa', name: '군계 1~25', book_count: 25 },
  { ...seriesSummary, id: 'bbbbbbbbbbbbbbbb', name: '히트(HEAT) 1-12권', book_count: 12 },
]

beforeEach(() => {
  seriesRequests = []
  server.use(
    http.get(`${ORIGIN}/api/series`, ({ request }) => {
      const url = new URL(request.url)
      seriesRequests.push(url.search)
      const q = url.searchParams.get('q') ?? ''
      const items = HITS.filter((s) => s.name.includes(q) || q === 'ㄱㄱ' || q === 'ㅎ')
      return HttpResponse.json({ items, total: items.length, offset: 0, limit: 8 })
    }),
    http.get(`${ORIGIN}/api/continue`, () => HttpResponse.json(continueResponse)),
  )
})

function SeriesProbe() {
  const { sid } = useParams()
  const location = useLocation()
  return <p data-testid="landed">{`${sid ?? '?'}@${location.pathname}`}</p>
}

function Harness() {
  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState('')
  return (
    <>
      <button
        type="button"
        onClick={() => {
          setOpen(true)
        }}
      >
        팔레트 열기
      </button>
      <CommandPalette
        open={open}
        query={query}
        onQueryChange={setQuery}
        onClose={() => {
          setOpen(false)
        }}
      />
    </>
  )
}

function renderPalette(harness = false): void {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/']}>
        <Routes>
          <Route
            path="/"
            element={
              harness ? (
                <Harness />
              ) : (
                <ControlledPalette />
              )
            }
          />
          <Route path="/series/:sid" element={<SeriesProbe />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

/** Always-open variant, so a test does not have to open it first. */
function ControlledPalette() {
  const [query, setQuery] = useState('')
  return (
    <CommandPalette open query={query} onQueryChange={setQuery} onClose={() => undefined} />
  )
}

// ---------------------------------------------------------------------------

describe('empty query (WP-10 acceptance 5)', () => {
  it('shows /api/continue-derived recents under 최근 항목, row 0 preselected', async () => {
    renderPalette()
    expect(await screen.findByText('[만화] 군계 1~25')).toBeInTheDocument()
    expect(screen.getByText('최근 항목')).toBeInTheDocument()
    const options = screen.getAllByRole('option')
    expect(options[0]).toHaveAttribute('aria-selected', 'true')
    // The recents come from /api/continue, so no series search was issued.
    expect(seriesRequests).toEqual([])
  })

  /**
   * **E-45 §6 — the recent row's counter divides by `book.page_count`.**
   *
   * `progress.page_count` is the stale-detection baseline, which E-45 §2 stopped
   * re-baselining on every write; from then on it can be an *old* length. The
   * shared `continueResponse` fixture carries 187 in both fields, so the row
   * above renders `42 / 187p` no matter which field the palette picks. These two
   * set the fields apart locally rather than editing the shared fixture.
   */
  function recentWith(book: Partial<ContinueItem['book']>, progress: Partial<ContinueItem['progress']>): ContinueItem {
    const base = at(continueResponse.items, 0)
    return { ...base, book: { ...base.book, ...book }, progress: { ...base.progress, ...progress } }
  }

  it('counts a recent against the index length, not the stale baseline (E-45 §6)', async () => {
    // 10 pages became 190 and the reader has seen 10: `10 / 190p`, not `10 / 10p`.
    const grew = recentWith({ page_count: 190 }, { page_count: 10, last_page: 10 })
    server.use(http.get(`${ORIGIN}/api/continue`, () => HttpResponse.json({ items: [grew] })))

    renderPalette()
    expect(await screen.findByText('군계(軍鷄) 01권.zip · 10 / 190p')).toBeInTheDocument()
  })

  it('counts a recent against the index length when the file shrank too (E-45 §6)', async () => {
    const shrank = recentWith({ page_count: 10 }, { page_count: 190, last_page: 10 })
    server.use(http.get(`${ORIGIN}/api/continue`, () => HttpResponse.json({ items: [shrank] })))

    renderPalette()
    expect(await screen.findByText('군계(軍鷄) 01권.zip · 10 / 10p')).toBeInTheDocument()
  })

  it('renders the §8.4 footer hints', async () => {
    renderPalette()
    await screen.findByText('최근 항목')
    // The `↵` is an icon now, so the hint reads as one word plus a glyph the
    // accessible tree does not see.
    expect(screen.getByText('열기')).toBeInTheDocument()
    expect(screen.getByText('esc 닫기')).toBeInTheDocument()
    expect(screen.getByText('초성 검색 ㅎㅌㅂㅅㅋ')).toBeInTheDocument()
    expect(screen.getByPlaceholderText('시리즈로 이동…')).toBeInTheDocument()
  })
})

describe('search (C-10: the server answers)', () => {
  it('debounces to a single GET /api/series?q=&limit=8', async () => {
    renderPalette()
    await screen.findByText('최근 항목')
    await userEvent.type(screen.getByPlaceholderText('시리즈로 이동…'), '군계')

    await waitFor(() => {
      expect(screen.getByText('검색 결과')).toBeInTheDocument()
    })
    await waitFor(() => {
      expect(seriesRequests).toHaveLength(1)
    })
    const params = new URLSearchParams(seriesRequests[0])
    expect(params.get('q')).toBe('군계')
    expect(params.get('limit')).toBe('8')
  })

  it('renders the server rows with 권 수 and 용량', async () => {
    renderPalette()
    await screen.findByText('최근 항목')
    await userEvent.type(screen.getByPlaceholderText('시리즈로 이동…'), '히트')
    expect(await screen.findByText('검색 결과')).toBeInTheDocument()
    await waitFor(() => {
      expect(screen.getAllByRole('option')).toHaveLength(1)
    })
    const row = at(screen.getAllByRole('option'), 0)
    expect(row).toHaveTextContent('히트(HEAT) 1-12권')
    expect(within(row).getByText('12권 · 622 MB')).toBeInTheDocument()
  })

  it('highlights the 초성 span that matched (FR-LIB-006)', async () => {
    renderPalette()
    await screen.findByText('최근 항목')
    await userEvent.type(screen.getByPlaceholderText('시리즈로 이동…'), 'ㄱㄱ')
    // Wait for the *search* results; the recents list also contains 군계.
    expect(await screen.findByText('검색 결과')).toBeInTheDocument()
    const row = screen.getByRole('option', { name: /군계 1~25/ })
    const mark = within(row).getByText('군계')
    expect(mark.tagName).toBe('MARK')
  })

  it('says 검색 결과 없음 when the server returns nothing', async () => {
    renderPalette()
    await screen.findByText('최근 항목')
    await userEvent.type(screen.getByPlaceholderText('시리즈로 이동…'), 'zzz')
    expect(await screen.findByText('검색 결과 없음')).toBeInTheDocument()
  })
})

describe('keyboard (ui-spec §8.4)', () => {
  it('moves the selection with ↓ / ↑ and wraps', async () => {
    renderPalette()
    await screen.findByText('최근 항목')
    const input = screen.getByPlaceholderText('시리즈로 이동…')
    await userEvent.type(input, 'ㅎ')
    await waitFor(() => {
      expect(screen.getAllByRole('option')).toHaveLength(2)
    })

    await userEvent.keyboard('{ArrowDown}')
    expect(screen.getAllByRole('option')[1]).toHaveAttribute('aria-selected', 'true')
    await userEvent.keyboard('{ArrowDown}')
    expect(screen.getAllByRole('option')[0]).toHaveAttribute('aria-selected', 'true')
    await userEvent.keyboard('{ArrowUp}')
    expect(screen.getAllByRole('option')[1]).toHaveAttribute('aria-selected', 'true')
  })

  it('opens the selected series on ↵', async () => {
    renderPalette()
    await screen.findByText('최근 항목')
    await userEvent.type(screen.getByPlaceholderText('시리즈로 이동…'), 'ㅎ')
    await waitFor(() => {
      expect(screen.getAllByRole('option')).toHaveLength(2)
    })
    await userEvent.keyboard('{ArrowDown}{Enter}')
    expect(await screen.findByTestId('landed')).toHaveTextContent(
      'bbbbbbbbbbbbbbbb@/series/bbbbbbbbbbbbbbbb',
    )
  })

  it('opens a row on click', async () => {
    renderPalette()
    await screen.findByText('[만화] 군계 1~25')
    await userEvent.click(screen.getByRole('option', { name: /\[만화\] 군계 1~25/ }))
    expect(await screen.findByTestId('landed')).toHaveTextContent(`${SERIES_ID}@/series/${SERIES_ID}`)
  })
})

describe('the dialog contract (WP-10 acceptance 9)', () => {
  it('autofocuses the query, closes on Esc and restores focus to the opener', async () => {
    renderPalette(true)
    const trigger = screen.getByRole('button', { name: '팔레트 열기' })
    await userEvent.click(trigger)

    const input = screen.getByPlaceholderText('시리즈로 이동…')
    expect(input).toHaveFocus()
    expect(screen.getByRole('dialog')).toHaveAttribute('aria-modal', 'true')

    await userEvent.keyboard('{Escape}')
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(trigger).toHaveFocus()
  })
})
