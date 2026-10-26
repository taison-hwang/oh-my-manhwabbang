import '@testing-library/jest-dom/vitest'

import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent, { type UserEvent } from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { setupServer } from 'msw/node'
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'

import {
  BOOK_ID,
  bookDetail,
  bookPrefs,
  cacheUsage,
  continueResponse,
  errorEnvelope,
  ORIGIN,
  progressExport,
  SERIES_ID,
  seriesDetail,
} from '../../api/fixtures'
import { queryKeys } from '../../api/queries'
import type { ProgressExport, ProgressExportItem, ProgressImportResult } from '../../api/types'
import { resetBasePath } from '../../api/urls'
import { failUnhandled } from '../../test/msw'
import { ProgressPanel } from './ProgressPanel'

/**
 * 읽기 기록 — ui-spec §8.6 item 4, ruling E-68 (FR-STT-004 over arch §7.11).
 *
 * Every expected string here is copied from the spec text, not from a run. The
 * load-bearing clauses:
 *
 *  * 내보내기 saves the response as `shelf-progress-YYYYMMDD.json`, the date
 *    being `exported_at` in **local** time, then says how many books it wrote.
 *  * Picking a file sends nothing. The browser refuses two shapes itself
 *    (over 32 MiB, before reading; not a JSON object with an `items` array) and
 *    leaves every other check to the server.
 *  * A file that passes replaces the button row with a confirm step; 병합 is
 *    the default on every pick and sends no `strategy`; 덮어쓰기 sends
 *    `?strategy=replace`.
 *  * Results and failures share one message slot; the envelope `message` is
 *    never shown; every refusal the server answered says the records did not
 *    change, and a request with no answer does not.
 *  * The slot's two live regions are mounted with the section and stay
 *    mounted; "no message" is both of them empty, not both of them gone.
 *  * Only the latest pick counts, and neither the saved file nor the
 *    invalidation depends on the dialog still being open.
 */

const server = setupServer()

/** Every `POST /api/progress/import` the panel sent. */
interface ImportCall {
  search: string
  body: unknown
}

let imports: ImportCall[]
let exportCalls: number
/** Blobs handed to `URL.createObjectURL`, in order; the URL is `blob:test/<index+1>`. */
let blobs: Blob[]
let revoked: string[]
/** What each temporary `<a>` looked like when it was clicked. */
let clicks: { download: string; href: string; connected: boolean }[]
/** Object URLs created and revoked and anchors clicked, in the order they happened. */
let events: string[]

const savedTz = process.env.TZ

beforeAll(() => {
  server.listen({ onUnhandledRequest: failUnhandled })
  // jsdom has no object URLs. Defined once and left in place, so a revocation
  // that lands after a test has ended still has something to call.
  Object.defineProperty(URL, 'createObjectURL', {
    configurable: true,
    writable: true,
    value: (blob: Blob): string => {
      blobs.push(blob)
      const url = `blob:test/${String(blobs.length)}`
      events.push(`create ${url}`)
      return url
    },
  })
  Object.defineProperty(URL, 'revokeObjectURL', {
    configurable: true,
    writable: true,
    value: (url: string): void => {
      revoked.push(url)
      events.push(`revoke ${url}`)
    },
  })
})
afterEach(() => {
  vi.useRealTimers()
  server.resetHandlers()
  resetBasePath()
  vi.unstubAllGlobals()
  if (savedTz === undefined) delete process.env.TZ
  else process.env.TZ = savedTz
})
afterAll(() => {
  server.close()
})

/** The import answer the default handler gives. */
let importAnswer: ProgressImportResult

beforeEach(() => {
  imports = []
  exportCalls = 0
  blobs = []
  revoked = []
  clicks = []
  events = []
  importAnswer = { imported: 2, skipped: 0, conflicts: 0 }
  // jsdom would try to navigate to the blob URL. Record instead.
  vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (
    this: HTMLAnchorElement,
  ) {
    clicks.push({ download: this.download, href: this.href, connected: this.isConnected })
    events.push(`click ${this.href}`)
    // A microtask: a revocation logged before this mark ran in the click's own
    // synchronous run. It cannot tell a later microtask from a later task; the
    // first 내보내기 test checks that one on controlled timers.
    queueMicrotask(() => {
      events.push('click task over')
    })
  })
  server.use(
    http.get(`${ORIGIN}/api/progress/export`, () => {
      exportCalls++
      return HttpResponse.json(exportDoc())
    }),
    http.post(`${ORIGIN}/api/progress/import`, async ({ request }) => {
      imports.push({ search: new URL(request.url).search, body: await request.json() })
      return HttpResponse.json(importAnswer)
    }),
  )
})

// ---------------------------------------------------------------------------
// Fixtures and helpers
// ---------------------------------------------------------------------------

/** 2026-10-25T16:30:00Z — 2026-10-26 01:30 in Seoul, 2026-10-25 09:30 in Los Angeles. */
const EXPORTED_AT = Date.UTC(2026, 9, 25, 16, 30, 0) / 1000

function item(n: number): ProgressExportItem {
  const first = progressExport.items[0]
  if (first === undefined) throw new Error('fixture has no item')
  return { ...first, book_id: n === 0 ? BOOK_ID : `book${String(n).padStart(12, '0')}` }
}

function exportDoc(count = 3, exportedAt = EXPORTED_AT): ProgressExport {
  return {
    ...progressExport,
    exported_at: exportedAt,
    items: Array.from({ length: count }, (_, n) => item(n)),
  }
}

function jsonFile(body: unknown, name = 'shelf-progress-20261020.json'): File {
  return new File([typeof body === 'string' ? body : JSON.stringify(body)], name, {
    type: 'application/json',
  })
}

function withSize(file: File, size: number): File {
  Object.defineProperty(file, 'size', { configurable: true, value: size })
  return file
}

/** Setting TZ at run time moves Date only in a forks-pool process; prove it took. */
function inZone(zone: string, offsetMinutes: number): void {
  process.env.TZ = zone
  expect(new Date(EXPORTED_AT * 1000).getTimezoneOffset(), `TZ=${zone} did not take`).toBe(
    offsetMinutes,
  )
}

function deferred(): { promise: Promise<void>; release: () => void } {
  let release = (): void => undefined
  const promise = new Promise<void>((resolve) => {
    release = resolve
  })
  return { promise, release }
}

/** Renders the panel; `unmount` is the dialog closing. */
function mountPanel(): { client: QueryClient; unmount: () => void } {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  const { unmount } = render(
    <QueryClientProvider client={client}>
      <ProgressPanel />
    </QueryClientProvider>,
  )
  return { client, unmount }
}

function renderPanel(): QueryClient {
  return mountPanel().client
}

function section(): HTMLElement {
  const el = screen.getByRole('heading', { level: 3, name: '읽기 기록' }).closest('section')
  if (el === null) throw new Error('no section around the heading')
  return el
}

/** The slot's status region. It is there from the first render, empty until used. */
function statusRegion(): HTMLElement {
  return within(section()).getByRole('status')
}

/** The slot's alert region, likewise always there. */
function alertRegion(): HTMLElement {
  return within(section()).getByRole('alert')
}

/**
 * Waits until the status region says something. The region itself is always
 * there, so `findByRole('status')` would return at once, before the answer.
 */
async function shownStatus(): Promise<HTMLElement> {
  await waitFor(() => {
    expect(statusRegion()).not.toBeEmptyDOMElement()
  })
  return statusRegion()
}

/** Waits until the alert region says something. */
async function shownAlert(): Promise<HTMLElement> {
  await waitFor(() => {
    expect(alertRegion()).not.toBeEmptyDOMElement()
  })
  return alertRegion()
}

/**
 * A `FileReader` whose reads finish only when the test says so, in any order:
 * jsdom's own finishes each read on its own schedule, so it cannot put an
 * older pick's read after a newer one's.
 */
class HeldReader {
  result: string | null = null
  error: DOMException | null = null
  onload: (() => void) | null = null
  onerror: (() => void) | null = null

  constructor(private readonly started: HeldReader[]) {}

  readAsText(): void {
    this.started.push(this)
  }
}

/** Swaps in `HeldReader` for the rest of the test; returns its reads, in start order. */
function holdReads(): { nth: (n: number) => HeldReader; count: () => number } {
  const started: HeldReader[] = []
  vi.stubGlobal(
    'FileReader',
    class extends HeldReader {
      constructor() {
        super(started)
      }
    },
  )
  return {
    nth: (n) => {
      const reader = started[n]
      if (reader === undefined) throw new Error(`read ${String(n)} never started`)
      return reader
    },
    count: () => started.length,
  }
}

/** Finishes `reader` with `text` and lets the panel act on it. */
async function finishRead(reader: HeldReader, text: string): Promise<void> {
  await act(async () => {
    reader.result = text
    reader.onload?.()
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
}

/** Fails `reader`, as a file that vanished mid-read does, and lets the panel act on it. */
async function failRead(reader: HeldReader): Promise<void> {
  await act(async () => {
    reader.error = new DOMException('the file could not be read', 'NotReadableError')
    reader.onerror?.()
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
}

function fileInput(): HTMLInputElement {
  const el = section().querySelector('input[type="file"]')
  if (!(el instanceof HTMLInputElement)) throw new Error('no file input')
  return el
}

function readBlob(blob: Blob): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => {
      resolve(typeof reader.result === 'string' ? reader.result : '')
    }
    reader.onerror = () => {
      reject(new Error('could not read the blob'))
    }
    reader.readAsText(blob)
  })
}

const methodGroup = { name: '가져오기 방식' } as const

/** Picks `file` and waits for the confirm step. */
async function pickToConfirm(user: UserEvent, file: File): Promise<void> {
  await user.upload(fileInput(), file)
  await screen.findByRole('radiogroup', methodGroup)
}

const NOTE = '읽은 쪽, 완독 표시, 책별 보기 설정을 파일로 저장하거나 그 파일에서 되살립니다.'
const MERGE_NOTE = '같은 책은 더 최근 기록을 남깁니다. 이 서버의 책별 보기 설정은 그대로 둡니다.'
const REPLACE_NOTE = '같은 책은 파일의 기록과 보기 설정으로 바꿉니다. 파일에 없는 책은 그대로 둡니다.'
const TOO_LARGE = '파일이 너무 큽니다. 내보내기로 만든 진행률 파일인지 확인하세요.'
const UNREADABLE = '진행률 파일로 읽을 수 없습니다. 내보내기로 만든 파일인지 확인하세요.'
const UNCHANGED = '기록은 바뀌지 않았습니다.'
const NO_ANSWER = '서버에 연결하지 못했습니다. 잠시 후 다시 시도하세요.'

// ---------------------------------------------------------------------------
// Layout
// ---------------------------------------------------------------------------

describe('layout (ui-spec §8.6 item 4)', () => {
  it('is a section with the heading, the note and the two secondary buttons', () => {
    renderPanel()
    const root = section()
    expect(root).toHaveClass('flex', 'flex-col', 'gap-2')
    expect(within(root).getByText(NOTE)).toHaveClass('text-xs', 'text-ink-dim')
    expect(within(root).getByRole('button', { name: '내보내기' })).toHaveClass('btn-secondary')
    expect(within(root).getByRole('button', { name: '가져오기' })).toHaveClass('btn-secondary')
    // The message slot is there from the start, saying nothing.
    expect(within(root).getByRole('status')).toBeEmptyDOMElement()
    expect(within(root).getByRole('alert')).toBeEmptyDOMElement()
  })

  it('has a hidden JSON file input, and asks the server for nothing on mount', async () => {
    renderPanel()
    const input = fileInput()
    expect(input).toHaveAttribute('accept', '.json,application/json')
    expect(input).not.toBeVisible()
    // Give a stray mount-time request the chance to land.
    await new Promise((resolve) => setTimeout(resolve, 20))
    expect(exportCalls).toBe(0)
    expect(imports).toHaveLength(0)
  })
})

// ---------------------------------------------------------------------------
// 내보내기
// ---------------------------------------------------------------------------

describe('내보내기', () => {
  it('saves the response as shelf-progress-YYYYMMDD.json, dated in local time (Seoul)', async () => {
    inZone('Asia/Seoul', -540)
    // The answer is held so the timers can be taken over once the request is
    // out: msw and user-event run on real ones up to that point.
    const held = deferred()
    server.use(
      http.get(`${ORIGIN}/api/progress/export`, async () => {
        exportCalls++
        await held.promise
        return HttpResponse.json(exportDoc())
      }),
    )
    const user = userEvent.setup()
    renderPanel()

    await user.click(screen.getByRole('button', { name: '내보내기' }))
    await waitFor(() => {
      expect(exportCalls).toBe(1)
    })
    // Waited on with a real timer taken before the fakes go in: `vi.waitFor`
    // would advance the faked ones on every check.
    const realSetTimeout = globalThis.setTimeout.bind(globalThis)
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    await act(async () => {
      held.release()
      for (let waits = 0; clicks.length === 0 && waits < 200; waits++) {
        await new Promise((resolve) => realSetTimeout(resolve, 5))
      }
    })
    expect(clicks).toHaveLength(1)

    // Released only on a later task: a URL revoked before the download takes
    // the blob downloads nothing. Each wait above was a real task, so the
    // click and every microtask after it have run, and the URL is still live.
    expect(revoked).toEqual([])
    expect(events).toEqual(['create blob:test/1', 'click blob:test/1', 'click task over'])
    act(() => {
      vi.runOnlyPendingTimers()
    })
    expect(revoked).toEqual(['blob:test/1'])
    expect(events).toEqual([
      'create blob:test/1',
      'click blob:test/1',
      'click task over',
      'revoke blob:test/1',
    ])
    vi.useRealTimers()

    expect(await shownStatus()).toHaveTextContent(
      /^책 3권의 기록을 내보냈습니다\.$/,
    )
    expect(exportCalls).toBe(1)
    expect(clicks).toEqual([
      { download: 'shelf-progress-20261026.json', href: 'blob:test/1', connected: true },
    ])
    // What was saved is the response itself.
    const saved = blobs[0]
    if (saved === undefined) throw new Error('nothing was saved')
    expect(JSON.parse(await readBlob(saved))).toEqual(exportDoc())
    // The anchor was temporary.
    expect(document.querySelector('a[download]')).toBeNull()
  })

  it('saves the file even when the dialog closed while the request was out', async () => {
    inZone('Asia/Seoul', -540)
    const held = deferred()
    server.use(
      http.get(`${ORIGIN}/api/progress/export`, async () => {
        exportCalls++
        await held.promise
        return HttpResponse.json(exportDoc())
      }),
    )
    const errors = vi.spyOn(console, 'error')
    const warnings = vi.spyOn(console, 'warn')
    const user = userEvent.setup()
    const { unmount } = mountPanel()

    await user.click(screen.getByRole('button', { name: '내보내기' }))
    await waitFor(() => {
      expect(exportCalls).toBe(1)
    })
    unmount()
    held.release()

    // Closing the dialog is not cancelling the file the user asked for.
    await waitFor(() => {
      expect(clicks).toEqual([
        { download: 'shelf-progress-20261026.json', href: 'blob:test/1', connected: true },
      ])
    })
    const saved = blobs[0]
    if (saved === undefined) throw new Error('nothing was saved')
    expect(JSON.parse(await readBlob(saved))).toEqual(exportDoc())
    await waitFor(() => {
      expect(revoked).toEqual(['blob:test/1'])
    })
    expect(events).toEqual([
      'create blob:test/1',
      'click blob:test/1',
      'click task over',
      'revoke blob:test/1',
    ])
    // The status line it would have set has no screen left; that is not an error.
    expect(errors).not.toHaveBeenCalled()
    expect(warnings).not.toHaveBeenCalled()
  })

  it('dates the file in local time west of UTC too', async () => {
    inZone('America/Los_Angeles', 420)
    const user = userEvent.setup()
    renderPanel()

    await user.click(screen.getByRole('button', { name: '내보내기' }))

    await shownStatus()
    expect(clicks.map((c) => c.download)).toEqual(['shelf-progress-20261025.json'])
  })

  it('pads month and day, and crosses the year in local time', async () => {
    inZone('Asia/Seoul', -540)
    // 2027-01-04T15:00:00Z is the first second of 2027-01-05 in Seoul.
    const at = Date.UTC(2027, 0, 4, 15, 0, 0) / 1000
    server.use(http.get(`${ORIGIN}/api/progress/export`, () => HttpResponse.json(exportDoc(1, at))))
    const user = userEvent.setup()
    renderPanel()

    await user.click(screen.getByRole('button', { name: '내보내기' }))

    await shownStatus()
    expect(clicks.map((c) => c.download)).toEqual(['shelf-progress-20270105.json'])
  })

  it('counts the books with formatVolumeCount', async () => {
    server.use(http.get(`${ORIGIN}/api/progress/export`, () => HttpResponse.json(exportDoc(1234))))
    const user = userEvent.setup()
    renderPanel()

    await user.click(screen.getByRole('button', { name: '내보내기' }))

    expect(await shownStatus()).toHaveTextContent(
      /^책 1,234권의 기록을 내보냈습니다\.$/,
    )
  })

  it('is disabled while its request is in flight', async () => {
    const held = deferred()
    server.use(
      http.get(`${ORIGIN}/api/progress/export`, async () => {
        await held.promise
        return HttpResponse.json(exportDoc())
      }),
    )
    const user = userEvent.setup()
    renderPanel()
    const button = screen.getByRole('button', { name: '내보내기' })

    await user.click(button)
    await waitFor(() => {
      expect(button).toBeDisabled()
    })

    held.release()
    await shownStatus()
    expect(button).toBeEnabled()
  })

  it.each([
    [
      '503',
      () =>
        HttpResponse.json(errorEnvelope('unavailable', 'user data is not available'), {
          status: 503,
        }),
      '서버가 지금 읽기 기록을 읽을 수 없습니다. 서버 상태를 확인한 뒤 다시 시도하세요.',
    ],
    [
      '500',
      () => HttpResponse.json(errorEnvelope('internal', 'internal error'), { status: 500 }),
      '내보내지 못했습니다. 잠시 후 다시 시도하세요.',
    ],
    ['no answer', () => HttpResponse.error(), NO_ANSWER],
  ] satisfies [string, () => Response, string][])('says why it failed on %s, and saves nothing', async (_name, answer, sentence) => {
    server.use(http.get(`${ORIGIN}/api/progress/export`, answer))
    const user = userEvent.setup()
    renderPanel()

    await user.click(screen.getByRole('button', { name: '내보내기' }))

    const alert = await shownAlert()
    expect(alert).toHaveTextContent(exact(sentence))
    expect(alert).toHaveClass('text-xs', 'text-accent-text')
    expect(screen.queryByText(/user data is not available|internal error/)).toBeNull()
    expect(clicks).toHaveLength(0)
    expect(blobs).toHaveLength(0)
  })
})

// ---------------------------------------------------------------------------
// 가져오기 — picking
// ---------------------------------------------------------------------------

describe('가져오기 — picking a file', () => {
  it('opens the file picker, and picking sends nothing', async () => {
    const user = userEvent.setup()
    renderPanel()
    const opened = vi.spyOn(fileInput(), 'click')

    await user.click(screen.getByRole('button', { name: '가져오기' }))
    expect(opened).toHaveBeenCalledTimes(1)

    await pickToConfirm(user, jsonFile(exportDoc(2)))
    expect(imports).toHaveLength(0)
  })

  it('refuses a file over 32 MiB before reading it', async () => {
    const readText = vi.spyOn(FileReader.prototype, 'readAsText')
    const readBuffer = vi.spyOn(FileReader.prototype, 'readAsArrayBuffer')
    const user = userEvent.setup()
    renderPanel()

    await user.upload(fileInput(), withSize(jsonFile(exportDoc(2)), 33_554_433))

    expect(await shownAlert()).toHaveTextContent(exact(TOO_LARGE))
    expect(readText).not.toHaveBeenCalled()
    expect(readBuffer).not.toHaveBeenCalled()
    expect(screen.queryByRole('radiogroup', methodGroup)).toBeNull()
    expect(screen.getByRole('button', { name: '내보내기' })).toBeInTheDocument()
    expect(imports).toHaveLength(0)
  })

  it('lets a file of exactly 32 MiB through to the confirm step', async () => {
    const user = userEvent.setup()
    renderPanel()

    await pickToConfirm(user, withSize(jsonFile(exportDoc(2)), 33_554_432))

    expect(screen.getByText('책 2권의 기록')).toBeInTheDocument()
    expect(alertRegion()).toBeEmptyDOMElement()
  })

  it.each([
    ['not JSON', '{"items": ['],
    ['JSON null', 'null'],
    ['a JSON array', '[]'],
    ['a JSON string', '"shelf-progress/1"'],
    ['items missing', JSON.stringify({ format: 'shelf-progress/1', prefs: [] })],
    ['items not an array', JSON.stringify({ ...progressExport, items: {} })],
  ])('refuses a file that is %s, and sends nothing', async (_name, text) => {
    const user = userEvent.setup()
    renderPanel()

    await user.upload(fileInput(), jsonFile(text, 'notes.json'))

    expect(await shownAlert()).toHaveTextContent(
      exact(UNREADABLE),
    )
    expect(screen.queryByRole('radiogroup', methodGroup)).toBeNull()
    expect(imports).toHaveLength(0)
  })

  it('leaves format, id_version, enums and unknown fields to the server, and sends the file as read', async () => {
    const odd = {
      ...exportDoc(1),
      format: 'something-else/9',
      id_version: 'shelf-id/2',
      prefs: [{ book_id: BOOK_ID, reading_direction: 'sideways', display_mode: null, fit_mode: null }],
      note: 'an unknown field',
    }
    const user = userEvent.setup()
    renderPanel()

    await pickToConfirm(user, jsonFile(odd))
    expect(alertRegion()).toBeEmptyDOMElement()
    expect(screen.getByText('책 1권의 기록')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: '가져오기' }))
    await shownStatus()
    expect(imports).toHaveLength(1)
    expect(imports[0]?.body).toEqual(odd)
  })

  it('clears the input after every pick, so the same file can be picked again', async () => {
    const user = userEvent.setup()
    renderPanel()
    const file = jsonFile(exportDoc(2))

    await pickToConfirm(user, file)
    expect(fileInput().value).toBe('')
    await user.click(screen.getByRole('button', { name: '취소' }))
    expect(screen.queryByRole('radiogroup', methodGroup)).toBeNull()

    // The same File object again: a browser fires no change for an unchanged
    // selection, and user-event mirrors that.
    await pickToConfirm(user, file)
    expect(screen.getByText('책 2권의 기록')).toBeInTheDocument()
  })

  it('clears the input after a refused pick too', async () => {
    const user = userEvent.setup()
    renderPanel()
    const big = withSize(jsonFile(exportDoc(2)), 40_000_000)

    await user.upload(fileInput(), big)
    await shownAlert()
    expect(fileInput().value).toBe('')
  })
})

// ---------------------------------------------------------------------------
// 가져오기 — only the latest pick counts
// ---------------------------------------------------------------------------

describe('가져오기 — only the latest pick counts', () => {
  it('drops an older read that finishes after a newer pick', async () => {
    const reads = holdReads()
    const older = exportDoc(2)
    const newer = exportDoc(1)
    const user = userEvent.setup()
    renderPanel()

    await user.upload(fileInput(), jsonFile(older, 'older.json'))
    await user.upload(fileInput(), jsonFile(newer, 'newer.json'))
    expect(reads.count()).toBe(2)
    await finishRead(reads.nth(1), JSON.stringify(newer))
    await screen.findByRole('radiogroup', methodGroup)
    await user.click(screen.getByRole('radio', { name: '덮어쓰기' }))

    await finishRead(reads.nth(0), JSON.stringify(older))

    // The newer file's step, payload and option all stand.
    expect(screen.getByText('newer.json')).toBeInTheDocument()
    expect(screen.queryByText('older.json')).toBeNull()
    expect(screen.getByText('책 1권의 기록')).toBeInTheDocument()
    expect(screen.getByRole('radio', { name: '덮어쓰기' })).toBeChecked()
    await user.click(screen.getByRole('button', { name: '가져오기' }))
    await shownStatus()
    expect(imports).toEqual([{ search: '?strategy=replace', body: newer }])
  })

  it.each([
    ['is not JSON', (reader: HeldReader) => finishRead(reader, 'not json')],
    ['cannot be read', failRead],
  ])('drops an older read that %s after a newer pick, leaving the slot empty', async (_name, settle) => {
    const reads = holdReads()
    const newer = exportDoc(1)
    const user = userEvent.setup()
    renderPanel()

    await user.upload(fileInput(), jsonFile('not json', 'older.json'))
    await user.upload(fileInput(), jsonFile(newer, 'newer.json'))
    await finishRead(reads.nth(1), JSON.stringify(newer))
    await screen.findByRole('radiogroup', methodGroup)

    await settle(reads.nth(0))

    expect(alertRegion()).toBeEmptyDOMElement()
    expect(screen.getByText('newer.json')).toBeInTheDocument()
  })

  it('does not bring a cancelled step back when an older read finishes after 취소', async () => {
    const reads = holdReads()
    const older = exportDoc(2)
    const newer = exportDoc(1)
    const user = userEvent.setup()
    renderPanel()

    await user.upload(fileInput(), jsonFile(older, 'older.json'))
    await user.upload(fileInput(), jsonFile(newer, 'newer.json'))
    await finishRead(reads.nth(1), JSON.stringify(newer))
    await screen.findByRole('radiogroup', methodGroup)
    await user.click(screen.getByRole('button', { name: '취소' }))

    await finishRead(reads.nth(0), JSON.stringify(older))

    expect(screen.queryByRole('radiogroup', methodGroup)).toBeNull()
    expect(screen.queryByText('older.json')).toBeNull()
    expect(screen.getByRole('button', { name: '내보내기' })).toBeInTheDocument()
  })

  describe('a new pick closes the confirm step already on screen, before its own read', () => {
    // Reachable: A is picked while its read is slow, the row's 가져오기 opens
    // the picker again before that read lands, A's step appears behind the
    // picker, and B is chosen. The input stays mounted under the confirm step,
    // so B's pick lands there — and from then on A must not be sendable,
    // whatever becomes of B.
    const a = exportDoc(2)
    const b = exportDoc(1)

    /** Puts A's confirm step on screen, with 덮어쓰기 selected. */
    async function stepForA(): Promise<{ user: UserEvent; reads: ReturnType<typeof holdReads> }> {
      const reads = holdReads()
      const user = userEvent.setup()
      renderPanel()
      await user.upload(fileInput(), jsonFile(a, 'a.json'))
      await finishRead(reads.nth(0), JSON.stringify(a))
      await screen.findByRole('radiogroup', methodGroup)
      expect(screen.getByText('a.json')).toBeInTheDocument()
      await user.click(screen.getByRole('radio', { name: '덮어쓰기' }))
      return { user, reads }
    }

    /** A's step is gone and the row is back, so the one 가져오기 is the row's. */
    function expectNoStepForA(): void {
      expect(screen.queryByRole('radiogroup', methodGroup)).toBeNull()
      expect(screen.queryByText('a.json')).toBeNull()
      const importButtons = screen.getAllByRole('button', { name: '가져오기' })
      expect(importButtons).toHaveLength(1)
      expect(importButtons[0]).toHaveClass('btn-secondary')
      expect(screen.getByRole('button', { name: '내보내기' })).toBeInTheDocument()
    }

    /** Presses every 가져오기 on screen; not one of them sends anything. */
    async function expectNothingSent(user: UserEvent): Promise<void> {
      for (const button of screen.getAllByRole('button', { name: '가져오기' })) {
        await user.click(button)
      }
      // Give a request the chance to land.
      await new Promise((resolve) => setTimeout(resolve, 20))
      expect(imports).toEqual([])
    }

    it('closes it at once while the new file is still being read; only the new file can go', async () => {
      const { user, reads } = await stepForA()

      await user.upload(fileInput(), jsonFile(b, 'b.json'))

      expect(reads.count()).toBe(2)
      expectNoStepForA()
      await expectNothingSent(user)

      await finishRead(reads.nth(1), JSON.stringify(b))
      await screen.findByRole('radiogroup', methodGroup)
      expect(screen.getByText('b.json')).toBeInTheDocument()
      expect(screen.getByRole('radio', { name: '병합' })).toBeChecked()
      await user.click(screen.getByRole('button', { name: '가져오기' }))
      await shownStatus()
      expect(imports).toEqual([{ search: '', body: b }])
    })

    it('closes it when the new file is over 32 MiB', async () => {
      const { user, reads } = await stepForA()

      await user.upload(fileInput(), withSize(jsonFile(b, 'b.json'), 33_554_433))

      expect(await shownAlert()).toHaveTextContent(exact(TOO_LARGE))
      expect(reads.count()).toBe(1)
      expectNoStepForA()
      await expectNothingSent(user)
    })

    it.each([
      ['is not JSON', (reader: HeldReader) => finishRead(reader, 'not json')],
      ['cannot be read', failRead],
    ])('closes it from the pick on when the new file %s', async (_name, settle) => {
      const { user, reads } = await stepForA()

      await user.upload(fileInput(), jsonFile('not json', 'b.json'))
      expectNoStepForA()

      await settle(reads.nth(1))

      expect(await shownAlert()).toHaveTextContent(exact(UNREADABLE))
      expectNoStepForA()
      await expectNothingSent(user)
    })
  })
})

// ---------------------------------------------------------------------------
// 가져오기 — the confirm step
// ---------------------------------------------------------------------------

describe('가져오기 — the confirm step', () => {
  it('replaces the button row with the file name, the count, the method and two buttons', async () => {
    const user = userEvent.setup()
    renderPanel()

    await pickToConfirm(user, jsonFile(exportDoc(2), 'backup-from-nas.json'))

    // Exactly one 가져오기 button exists, and it is the confirm step's.
    expect(screen.queryByRole('button', { name: '내보내기' })).toBeNull()
    const importButtons = screen.getAllByRole('button', { name: '가져오기' })
    expect(importButtons).toHaveLength(1)
    expect(importButtons[0]).toHaveClass('btn-primary')
    expect(screen.getByRole('button', { name: '취소' })).toHaveClass('btn-ghost')

    expect(screen.getByText('backup-from-nas.json')).toBeInTheDocument()
    expect(screen.getByText('책 2권의 기록')).toHaveClass('text-xs', 'text-ink-muted')
    const group = screen.getByRole('radiogroup', methodGroup)
    expect(within(group).getAllByRole('radio').map((r) => r.closest('label')?.textContent)).toEqual([
      '병합',
      '덮어쓰기',
    ])
    expect(within(group).getByRole('radio', { name: '병합' })).toBeChecked()
    expect(screen.getByText(MERGE_NOTE)).toHaveClass('text-xs', 'text-ink-dim')
    expect(screen.queryByText(REPLACE_NOTE)).toBeNull()
  })

  it('describes the option that is selected', async () => {
    const user = userEvent.setup()
    renderPanel()
    await pickToConfirm(user, jsonFile(exportDoc(2)))

    await user.click(screen.getByRole('radio', { name: '덮어쓰기' }))

    expect(screen.getByText(REPLACE_NOTE)).toBeInTheDocument()
    expect(screen.queryByText(MERGE_NOTE)).toBeNull()
  })

  it('selects 병합 again on every new pick', async () => {
    const user = userEvent.setup()
    renderPanel()
    await pickToConfirm(user, jsonFile(exportDoc(2)))
    await user.click(screen.getByRole('radio', { name: '덮어쓰기' }))
    await user.click(screen.getByRole('button', { name: '취소' }))

    await pickToConfirm(user, jsonFile(exportDoc(1), 'another.json'))

    expect(screen.getByRole('radio', { name: '병합' })).toBeChecked()
    expect(screen.getByText(MERGE_NOTE)).toBeInTheDocument()
  })

  it('취소 brings the button row back and sends nothing', async () => {
    const user = userEvent.setup()
    renderPanel()
    await pickToConfirm(user, jsonFile(exportDoc(2)))

    await user.click(screen.getByRole('button', { name: '취소' }))

    expect(screen.queryByRole('radiogroup', methodGroup)).toBeNull()
    expect(screen.getByRole('button', { name: '내보내기' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '가져오기' })).toHaveClass('btn-secondary')
    expect(imports).toHaveLength(0)
  })

  it('sends no strategy for 병합 — merge is the server default', async () => {
    const doc = exportDoc(2)
    const user = userEvent.setup()
    renderPanel()
    await pickToConfirm(user, jsonFile(doc))

    await user.click(screen.getByRole('button', { name: '가져오기' }))

    await shownStatus()
    expect(imports).toEqual([{ search: '', body: doc }])
  })

  it('sends ?strategy=replace for 덮어쓰기', async () => {
    const doc = exportDoc(2)
    const user = userEvent.setup()
    renderPanel()
    await pickToConfirm(user, jsonFile(doc))
    await user.click(screen.getByRole('radio', { name: '덮어쓰기' }))

    await user.click(screen.getByRole('button', { name: '가져오기' }))

    await shownStatus()
    expect(imports).toEqual([{ search: '?strategy=replace', body: doc }])
  })

  it('disables both buttons while the request is in flight, then closes on success', async () => {
    const held = deferred()
    server.use(
      http.post(`${ORIGIN}/api/progress/import`, async () => {
        await held.promise
        return HttpResponse.json({ imported: 2, skipped: 0, conflicts: 0 })
      }),
    )
    const user = userEvent.setup()
    renderPanel()
    await pickToConfirm(user, jsonFile(exportDoc(2)))
    const confirm = screen.getByRole('button', { name: '가져오기' })
    const cancel = screen.getByRole('button', { name: '취소' })

    await user.click(confirm)
    await waitFor(() => {
      expect(confirm).toBeDisabled()
    })
    expect(cancel).toBeDisabled()

    held.release()
    expect(await shownStatus()).toHaveTextContent('책 2권의 기록을 가져왔습니다.')
    expect(screen.queryByRole('radiogroup', methodGroup)).toBeNull()
    expect(screen.getByRole('button', { name: '내보내기' })).toBeEnabled()
    expect(screen.getByRole('button', { name: '가져오기' })).toHaveClass('btn-secondary')
  })

  it('closes on a refusal too', async () => {
    server.use(
      http.post(`${ORIGIN}/api/progress/import`, () =>
        HttpResponse.json(errorEnvelope('bad_request', 'unknown field "note"', { field: 'note' }), {
          status: 400,
        }),
      ),
    )
    const user = userEvent.setup()
    renderPanel()
    await pickToConfirm(user, jsonFile(exportDoc(2)))

    await user.click(screen.getByRole('button', { name: '가져오기' }))

    await shownAlert()
    expect(screen.queryByRole('radiogroup', methodGroup)).toBeNull()
    expect(screen.getByRole('button', { name: '내보내기' })).toBeInTheDocument()
    expect(screen.getAllByRole('button', { name: '가져오기' })).toHaveLength(1)
  })
})

// ---------------------------------------------------------------------------
// 가져오기 — the result
// ---------------------------------------------------------------------------

describe('가져오기 — the result line', () => {
  it.each([
    [{ imported: 3, skipped: 0, conflicts: 0 }, '책 3권의 기록을 가져왔습니다.'],
    [
      { imported: 3, skipped: 0, conflicts: 2 },
      '책 3권의 기록을 가져왔습니다. 그중 2권은 이 서버에 있던 기록을 바꿨습니다.',
    ],
    // conflicts − skipped = 0: the one conflict is the one that was kept.
    [
      { imported: 2, skipped: 1, conflicts: 1 },
      '책 2권의 기록을 가져왔습니다. 이 서버의 기록이 더 최근인 1권은 그대로 두었습니다.',
    ],
    [
      { imported: 2, skipped: 1, conflicts: 3 },
      '책 2권의 기록을 가져왔습니다. 그중 2권은 이 서버에 있던 기록을 바꿨습니다. 이 서버의 기록이 더 최근인 1권은 그대로 두었습니다.',
    ],
    [
      { imported: 0, skipped: 2, conflicts: 2 },
      '책 0권의 기록을 가져왔습니다. 이 서버의 기록이 더 최근인 2권은 그대로 두었습니다.',
    ],
    [
      { imported: 1234, skipped: 1000, conflicts: 2000 },
      '책 1,234권의 기록을 가져왔습니다. 그중 1,000권은 이 서버에 있던 기록을 바꿨습니다. 이 서버의 기록이 더 최근인 1,000권은 그대로 두었습니다.',
    ],
    [{ imported: 0, skipped: 0, conflicts: 0 }, '파일에 책 기록이 없습니다.'],
  ])('says %j as one status line', async (answer, sentence) => {
    importAnswer = answer
    const user = userEvent.setup()
    renderPanel()
    await pickToConfirm(user, jsonFile(exportDoc(2)))

    await user.click(screen.getByRole('button', { name: '가져오기' }))

    const status = await shownStatus()
    expect(status).toHaveTextContent(exact(sentence))
    expect(status).toHaveClass('text-xs', 'text-ink-muted')
  })

  it('invalidates the books, series and continue queries on success', async () => {
    const user = userEvent.setup()
    const client = renderPanel()
    client.setQueryData(queryKeys.books.detail(BOOK_ID), bookDetail)
    client.setQueryData(queryKeys.books.prefs(BOOK_ID), bookPrefs)
    client.setQueryData(queryKeys.series.detail(SERIES_ID), seriesDetail)
    client.setQueryData(queryKeys.continueReading.list(20), continueResponse)
    client.setQueryData(queryKeys.cache.usage, cacheUsage)
    await pickToConfirm(user, jsonFile(exportDoc(2)))

    await user.click(screen.getByRole('button', { name: '가져오기' }))

    await shownStatus()
    expect(client.getQueryState(queryKeys.books.detail(BOOK_ID))?.isInvalidated).toBe(true)
    expect(client.getQueryState(queryKeys.books.prefs(BOOK_ID))?.isInvalidated).toBe(true)
    expect(client.getQueryState(queryKeys.series.detail(SERIES_ID))?.isInvalidated).toBe(true)
    expect(client.getQueryState(queryKeys.continueReading.list(20))?.isInvalidated).toBe(true)
    expect(client.getQueryState(queryKeys.cache.usage)?.isInvalidated).toBe(false)
  })

  it('invalidates them even when the dialog closed before the answer', async () => {
    const held = deferred()
    server.use(
      http.post(`${ORIGIN}/api/progress/import`, async ({ request }) => {
        imports.push({ search: new URL(request.url).search, body: await request.json() })
        await held.promise
        return HttpResponse.json(importAnswer)
      }),
    )
    const user = userEvent.setup()
    const { client, unmount } = mountPanel()
    client.setQueryData(queryKeys.books.detail(BOOK_ID), bookDetail)
    client.setQueryData(queryKeys.series.detail(SERIES_ID), seriesDetail)
    client.setQueryData(queryKeys.continueReading.list(20), continueResponse)
    await pickToConfirm(user, jsonFile(exportDoc(2)))
    await user.click(screen.getByRole('button', { name: '가져오기' }))
    await waitFor(() => {
      expect(imports).toHaveLength(1)
    })

    unmount()
    held.release()

    await waitFor(() => {
      expect(client.getQueryState(queryKeys.continueReading.list(20))?.isInvalidated).toBe(true)
    })
    expect(client.getQueryState(queryKeys.books.detail(BOOK_ID))?.isInvalidated).toBe(true)
    expect(client.getQueryState(queryKeys.series.detail(SERIES_ID))?.isInvalidated).toBe(true)
  })

  it('invalidates nothing when the server refuses', async () => {
    server.use(
      http.post(`${ORIGIN}/api/progress/import`, () =>
        HttpResponse.json(errorEnvelope('unavailable', 'user data is not available'), {
          status: 503,
        }),
      ),
    )
    const user = userEvent.setup()
    const client = renderPanel()
    client.setQueryData(queryKeys.books.detail(BOOK_ID), bookDetail)
    client.setQueryData(queryKeys.continueReading.list(20), continueResponse)
    await pickToConfirm(user, jsonFile(exportDoc(2)))

    await user.click(screen.getByRole('button', { name: '가져오기' }))

    await shownAlert()
    expect(client.getQueryState(queryKeys.books.detail(BOOK_ID))?.isInvalidated).toBe(false)
    expect(client.getQueryState(queryKeys.continueReading.list(20))?.isInvalidated).toBe(false)
  })
})

// ---------------------------------------------------------------------------
// 가져오기 — failures
// ---------------------------------------------------------------------------

describe('가져오기 — failures', () => {
  it.each([
    [
      '400 with detail.id_version',
      () =>
        HttpResponse.json(
          errorEnvelope('bad_request', 'the document uses a different identifier scheme', {
            id_version: 'shelf-id/2',
          }),
          { status: 400 },
        ),
      `다른 버전의 SHELF에서 만든 파일이라 가져올 수 없습니다. ${UNCHANGED}`,
    ],
    [
      '400 naming an unknown field',
      () =>
        HttpResponse.json(errorEnvelope('bad_request', 'unknown field "note"', { field: 'note' }), {
          status: 400,
        }),
      `${UNREADABLE} ${UNCHANGED}`,
    ],
    [
      '400 on an oversized body',
      () =>
        HttpResponse.json(errorEnvelope('bad_request', 'request body exceeds 33554432 bytes'), {
          status: 400,
        }),
      `${UNREADABLE} ${UNCHANGED}`,
    ],
    [
      '503',
      () =>
        HttpResponse.json(errorEnvelope('unavailable', 'user data is not available'), {
          status: 503,
        }),
      `서버가 지금 읽기 기록을 쓸 수 없습니다. ${UNCHANGED} 서버 상태를 확인한 뒤 다시 시도하세요.`,
    ],
    [
      '500',
      () => HttpResponse.json(errorEnvelope('internal', 'internal error'), { status: 500 }),
      `가져오지 못했습니다. ${UNCHANGED} 잠시 후 다시 시도하세요.`,
    ],
  ] as const)('says why on a %s, and that the records did not change', async (_name, answer, sentence) => {
    server.use(http.post(`${ORIGIN}/api/progress/import`, answer))
    const user = userEvent.setup()
    renderPanel()
    await pickToConfirm(user, jsonFile(exportDoc(2)))

    await user.click(screen.getByRole('button', { name: '가져오기' }))

    const alert = await shownAlert()
    expect(alert).toHaveTextContent(exact(sentence))
    expect(alert).toHaveClass('text-xs', 'text-accent-text')
    // The envelope message is English and is never shown.
    expect(
      screen.queryByText(/identifier scheme|unknown field|exceeds|not available|internal error/),
    ).toBeNull()
  })

  it('does not claim the records are unchanged when the request got no answer', async () => {
    server.use(http.post(`${ORIGIN}/api/progress/import`, () => HttpResponse.error()))
    const user = userEvent.setup()
    renderPanel()
    await pickToConfirm(user, jsonFile(exportDoc(2)))

    await user.click(screen.getByRole('button', { name: '가져오기' }))

    const alert = await shownAlert()
    expect(alert).toHaveTextContent(exact(NO_ANSWER))
    expect(alert).not.toHaveTextContent(UNCHANGED)
  })
})

// ---------------------------------------------------------------------------
// One message line
// ---------------------------------------------------------------------------

describe('one message line', () => {
  it('sits under the row: the status and alert regions are the section’s last two elements', async () => {
    const user = userEvent.setup()
    renderPanel()

    await user.click(screen.getByRole('button', { name: '내보내기' }))

    const status = await shownStatus()
    const alert = alertRegion()
    expect(section().lastElementChild).toBe(alert)
    expect(alert.previousElementSibling).toBe(status)
    expect(status.previousElementSibling).toBe(
      screen.getByRole('button', { name: '내보내기' }).parentElement,
    )
  })

  it('keeps both live regions mounted from the first render, and only changes their text', async () => {
    // A region inserted together with its message is often never announced,
    // so the nodes the screen reader watches are the ones from mount.
    const user = userEvent.setup()
    renderPanel()
    const status = statusRegion()
    const alert = alertRegion()
    // Nothing overrides the roles' implicit live semantics: an `aria-live`
    // other than the role's own value (`off` silences it), or `aria-hidden`.
    const expectLive = (): void => {
      expect([null, 'polite']).toContain(status.getAttribute('aria-live'))
      expect([null, 'assertive']).toContain(alert.getAttribute('aria-live'))
      expect(status).not.toHaveAttribute('aria-hidden')
      expect(alert).not.toHaveAttribute('aria-hidden')
    }
    expectLive()
    expect(status).toBeEmptyDOMElement()
    expect(alert).toBeEmptyDOMElement()
    expect(status).toHaveClass('text-xs', 'text-ink-muted')
    expect(alert).toHaveClass('text-xs', 'text-accent-text')

    await user.click(screen.getByRole('button', { name: '내보내기' }))
    await waitFor(() => {
      expect(status).toHaveTextContent(/^책 3권의 기록을 내보냈습니다\.$/)
    })
    expect(alert).toBeEmptyDOMElement()

    await user.upload(fileInput(), jsonFile('not json'))
    await waitFor(() => {
      expect(alert).toHaveTextContent(exact(UNREADABLE))
    })
    // Still one message at a time.
    expect(status).toBeEmptyDOMElement()

    // The confirm step replacing the row leaves the slot alone.
    await pickToConfirm(user, jsonFile(exportDoc(2)))
    expect(alert).toBeEmptyDOMElement()
    await user.click(screen.getByRole('button', { name: '가져오기' }))
    await waitFor(() => {
      expect(status).toHaveTextContent('책 2권의 기록을 가져왔습니다.')
    })

    expect(statusRegion()).toBe(status)
    expect(alertRegion()).toBe(alert)
    expectLive()
    expect(within(section()).getAllByRole('status')).toHaveLength(1)
    expect(within(section()).getAllByRole('alert')).toHaveLength(1)
  })

  it('a pick replaces the export status with its refusal', async () => {
    const user = userEvent.setup()
    renderPanel()
    await user.click(screen.getByRole('button', { name: '내보내기' }))
    await shownStatus()

    await user.upload(fileInput(), jsonFile('not json'))

    expect(await shownAlert()).toHaveTextContent(exact(UNREADABLE))
    expect(statusRegion()).toBeEmptyDOMElement()
  })

  it('a pick that passes clears the slot', async () => {
    const user = userEvent.setup()
    renderPanel()
    await user.upload(fileInput(), withSize(jsonFile(exportDoc(2)), 33_554_433))
    await shownAlert()

    await pickToConfirm(user, jsonFile(exportDoc(2)))

    expect(alertRegion()).toBeEmptyDOMElement()
    expect(statusRegion()).toBeEmptyDOMElement()
  })

  it('a 내보내기 click clears a standing alert at once, then shows its own line', async () => {
    const held = deferred()
    server.use(
      http.get(`${ORIGIN}/api/progress/export`, async () => {
        await held.promise
        return HttpResponse.json(exportDoc())
      }),
    )
    const user = userEvent.setup()
    renderPanel()
    await user.upload(fileInput(), jsonFile('[]'))
    await shownAlert()

    await user.click(screen.getByRole('button', { name: '내보내기' }))
    expect(alertRegion()).toBeEmptyDOMElement()

    held.release()
    expect(await shownStatus()).toHaveTextContent('책 3권의 기록을 내보냈습니다.')
    expect(alertRegion()).toBeEmptyDOMElement()
  })

  it('a 내보내기 click replaces an import result', async () => {
    const user = userEvent.setup()
    renderPanel()
    await pickToConfirm(user, jsonFile(exportDoc(2)))
    await user.click(screen.getByRole('button', { name: '가져오기' }))
    expect(await shownStatus()).toHaveTextContent('책 2권의 기록을 가져왔습니다.')

    await user.click(screen.getByRole('button', { name: '내보내기' }))

    await waitFor(() => {
      expect(screen.getByRole('status')).toHaveTextContent(/^책 3권의 기록을 내보냈습니다\.$/)
    })
    expect(screen.getAllByRole('status')).toHaveLength(1)
  })

  it('취소 clears whatever is in the slot', async () => {
    // The only way a line can stand while the confirm step is open: an export
    // answered after the pick.
    const held = deferred()
    server.use(
      http.get(`${ORIGIN}/api/progress/export`, async () => {
        await held.promise
        return HttpResponse.json(exportDoc())
      }),
    )
    const user = userEvent.setup()
    renderPanel()
    await user.click(screen.getByRole('button', { name: '내보내기' }))
    await pickToConfirm(user, jsonFile(exportDoc(2)))
    held.release()
    await shownStatus()

    await user.click(screen.getByRole('button', { name: '취소' }))

    expect(statusRegion()).toBeEmptyDOMElement()
    expect(alertRegion()).toBeEmptyDOMElement()
  })
})

/** The whole text and nothing else, read verbatim from the spec. */
function exact(text: string): RegExp {
  return new RegExp(`^${text.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}$`)
}
