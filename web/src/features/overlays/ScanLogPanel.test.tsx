import '@testing-library/jest-dom/vitest'

import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { setupServer } from 'msw/node'
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'

import { ORIGIN, scanStatusIdle, scanStatusRunning } from '../../api/fixtures'
import { useScanCompletionRefresh, useScanStatus } from '../../api/queries'
import type { ScanLogEntry, ScanStatus } from '../../api/types'
import { resetBasePath } from '../../api/urls'
import { SCAN_LOG_LIMIT, ScanLogPanel } from './ScanLogPanel'
import { failUnhandled } from '../../test/msw'

/**
 * The 스캔 로그 panel follows a running scan (HANDOFF `ab`, the user's ruling).
 *
 * The backend already commits a log row per series as the scan goes; the panel
 * stood still only because nothing re-read it until the run ended. These tests
 * pin the three halves of following it, against a server that behaves like
 * `index.ListLog`: newest-first without `since_id`, **oldest-first after it**,
 * `limit` rows either way.
 *
 *  1. It polls **only** while the status poll reports a run — D-16's 1 s budget
 *     becomes two requests a second during a scan, and stays at zero outside
 *     one.
 *  2. Each poll asks for the rows after the newest one it holds, and appends
 *     exactly those — no duplicates, newest on top, capped at the panel's page.
 *  3. When the run ends it stops, and the tail written between the last poll
 *     and the end still arrives — through `useScanCompletionRefresh`, which the
 *     app shell mounts and this harness mounts the same way.
 */

const server = setupServer()

/** The server's `scan_log` table, ascending by id. */
let rows: ScanLogEntry[] = []
let status: ScanStatus = scanStatusIdle
/** `since_id` of every `/api/scan/log` request, `null` for a newest-first read. */
let logRequests: (number | null)[] = []

function entry(id: number): ScanLogEntry {
  return {
    id,
    ts: 1_753_600_000 + id,
    run_id: 'run-2026-10-18-01',
    level: id % 7 === 0 ? 'warn' : 'info',
    root_name: 'mangga',
    rel_path: `시리즈/책 ${id.toString()}.zip`,
    message: 'indexed',
  }
}

function append(from: number, to: number): void {
  for (let id = from; id <= to; id++) rows.push(entry(id))
}

/** `index.ListLog`: `since_id` ≤ 0 is no cursor; `limit` ≤ 0 is the default. */
function answerLog(url: URL): { since: number | null; items: ScanLogEntry[] } {
  const asked = Number(url.searchParams.get('limit') ?? '200')
  const limit = asked > 0 ? asked : 200
  const cursor = Number(url.searchParams.get('since_id') ?? '0')
  const since = cursor > 0 ? cursor : null
  const items =
    since === null
      ? [...rows].reverse().slice(0, limit)
      : rows.filter((row) => row.id > since).slice(0, limit)
  return { since, items }
}

beforeAll(() => {
  server.listen({ onUnhandledRequest: failUnhandled })
})
afterEach(() => {
  server.resetHandlers()
  resetBasePath()
  vi.useRealTimers()
})
afterAll(() => {
  server.close()
})

beforeEach(() => {
  rows = []
  status = scanStatusIdle
  logRequests = []
  server.use(
    http.get(`${ORIGIN}/api/scan/status`, () => HttpResponse.json(status)),
    http.get(`${ORIGIN}/api/scan/log`, ({ request }) => {
      const { since, items } = answerLog(new URL(request.url))
      logRequests.push(since)
      return HttpResponse.json({ items })
    }),
  )
})

/** What the app shell does with the status poll (`router.tsx`). */
function ShellScanWatcher() {
  useScanCompletionRefresh(useScanStatus().data)
  return null
}

function renderPanel(): void {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <ShellScanWatcher />
      <ScanLogPanel />
    </QueryClientProvider>,
  )
}

/** The ids on screen, top to bottom, read off the file column. */
function shownIds(): number[] {
  return screen
    .queryAllByTestId('scan-log-file')
    .map((el) => Number(/책 (\d+)\.zip/.exec(el.textContent ?? '')?.[1]))
}

async function tick(ms: number): Promise<void> {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms)
  })
}

describe('ScanLogPanel follows a running scan (HANDOFF ab)', () => {
  it('reads the log once and then leaves it alone while no scan runs', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    append(1, 3)
    renderPanel()
    await waitFor(() => {
      expect(shownIds()).toEqual([3, 2, 1])
    })
    expect(logRequests).toEqual([null])

    // New rows on the server are not a reason to ask: outside a run nothing
    // writes them, and the 1 s budget outside a run is zero requests.
    append(4, 5)
    await tick(5_000)
    expect(logRequests).toEqual([null])
    expect(shownIds()).toEqual([3, 2, 1])
  })

  it('asks for the rows after the newest it holds, once a second, and appends only those', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    status = scanStatusRunning
    append(1, 3)
    renderPanel()
    await waitFor(() => {
      expect(shownIds()).toEqual([3, 2, 1])
    })

    append(4, 5)
    await tick(1_000)
    await waitFor(() => {
      expect(shownIds()).toEqual([5, 4, 3, 2, 1])
    })
    expect(logRequests).toEqual([null, 3])

    // A second with nothing new costs one request and changes nothing.
    await tick(1_000)
    expect(logRequests).toEqual([null, 3, 5])
    expect(shownIds()).toEqual([5, 4, 3, 2, 1])

    append(6, 6)
    await tick(1_000)
    await waitFor(() => {
      expect(shownIds()).toEqual([6, 5, 4, 3, 2, 1])
    })
    expect(logRequests).toEqual([null, 3, 5, 5])
  })

  it('stops when the run ends, and still shows the rows written before it ended', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    status = scanStatusRunning
    append(1, 2)
    renderPanel()
    await waitFor(() => {
      expect(shownIds()).toEqual([2, 1])
    })

    // The run writes its last rows and finishes inside the same second.
    append(3, 4)
    status = { ...scanStatusIdle, finished_at: 1_753_600_100 }
    await tick(1_000)
    await waitFor(() => {
      expect(shownIds()).toEqual([4, 3, 2, 1])
    })
    const settled = logRequests.length

    append(5, 6) // a later run's rows, or anything else: not ours to follow
    await tick(5_000)
    expect(logRequests).toHaveLength(settled)
    expect(logRequests.slice(1).every((since) => since !== null)).toBe(true)
  })

  it('still gets the tail when the run ends while its first read is on the wire', async () => {
    // TanStack Query v5 does not cancel an in-flight fetch for an invalidation
    // when the query has no data yet — it hands back the same promise. So a
    // panel opened in the last moments of a run, whose first read the server
    // answered *before* the run's last rows, would keep that answer: the
    // completion refresh lands on the old request, the run is over so nothing
    // polls again, and the tail never shows. `invalidateAfterScan` cancels the
    // log read before invalidating it, so the re-read is a new request.
    //
    // Also the one test here that only the completion refresh can pass: the
    // interval's own refetches join the stuck request rather than replace it.
    vi.useFakeTimers({ shouldAdvanceTime: true })
    status = scanStatusRunning
    append(1, 2)
    let release: () => void = () => undefined
    const gate = new Promise<void>((resolve) => {
      release = resolve
    })
    let first = true
    server.use(
      http.get(`${ORIGIN}/api/scan/log`, async ({ request }) => {
        // Answered from the rows as they are when the request arrives.
        const { since, items } = answerLog(new URL(request.url))
        logRequests.push(since)
        if (first) {
          first = false
          await gate
        }
        return HttpResponse.json({ items })
      }),
    )
    renderPanel()
    await waitFor(() => {
      expect(logRequests).toEqual([null])
    })

    // The run writes its last rows and ends while that read is still out.
    append(3, 4)
    status = { ...scanStatusIdle, finished_at: 1_753_600_100 }
    await tick(1_000)
    release()

    await waitFor(() => {
      expect(shownIds()).toEqual([4, 3, 2, 1])
    })
    await tick(3_000)
    expect(shownIds()).toEqual([4, 3, 2, 1])
  })

  it('re-reads the newest page when it falls a whole page behind', async () => {
    // `since_id` returns the *oldest* rows after the cursor. A second that
    // wrote more than a page would otherwise show rows that are already a page
    // stale and fall further behind on every poll; the panel shows the newest
    // page, so that is what it asks for once it knows it is behind.
    vi.useFakeTimers({ shouldAdvanceTime: true })
    status = scanStatusRunning
    append(1, 3)
    renderPanel()
    await waitFor(() => {
      expect(shownIds()).toEqual([3, 2, 1])
    })

    append(4, 3 + SCAN_LOG_LIMIT + 50)
    await tick(1_000)
    const newest = 3 + SCAN_LOG_LIMIT + 50
    await waitFor(() => {
      expect(shownIds()[0]).toBe(newest)
    })
    const ids = shownIds()
    expect(ids).toHaveLength(SCAN_LOG_LIMIT)
    expect(ids.at(-1)).toBe(newest - SCAN_LOG_LIMIT + 1)
    expect(new Set(ids).size).toBe(ids.length)
    expect(logRequests).toEqual([null, 3, null])
  })
})

/**
 * The log scrolls inside the settings dialog, so a keyboard has to be able to
 * reach it (axe `scrollable-region-focusable`). Chrome used to make the box a
 * tab stop on its own; the dialog's Tab trap (`lib/modalLayer.ts`) counts only
 * what says it is focusable, so after it learned to wrap past implicit stops
 * the log could no longer be scrolled from the keyboard. The box says so now,
 * and is named by the section's heading. Not `role="log"`: that is a polite
 * live region, and a running scan adds rows every second (E-59).
 */
describe('the log is reachable from the keyboard', () => {
  it('is a tab stop named by its heading, and not a live region', () => {
    renderPanel()
    const region = screen.getByRole('region', { name: '스캔 로그' })

    expect(region).toBe(screen.getByTestId('scan-log-body'))
    expect(region).toHaveAttribute('tabindex', '0')
    expect(region).not.toHaveAttribute('aria-live')
  })
})
