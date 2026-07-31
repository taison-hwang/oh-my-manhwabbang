import '@testing-library/jest-dom/vitest'

import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { setupServer } from 'msw/node'
import { useState } from 'react'
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'

import {
  cacheUsage,
  errorEnvelope,
  ORIGIN,
  pendingRoot,
  root,
  rootEntry,
  rootsResponse,
  scanLogResponse,
  settings,
} from '../../api/fixtures'
import type { Root, ServerSettings, Settings, SettingsUpdate } from '../../api/types'
import { resetBasePath } from '../../api/urls'
import { useUiStore } from '../../store/ui'
import { SettingsDialog } from './SettingsDialog'

/**
 * 설정 (prd UI-004, ui-spec §8.6, WP-10 acceptance 6 and 9).
 *
 * The load-bearing assertions: roots are **read-only** (ruling E-3), cache
 * purge is confirmed before it fires (FR-THM-008), the reading defaults write
 * the *wire* enums (C-1/C-2), the theme switch flips `data-theme` synchronously
 * (NFR-CMP-003), the scan log colours its levels (FR-IDX-004), and the dialog
 * traps and restores focus.
 */

/** `noUncheckedIndexedAccess` types an indexed query result as `T | undefined`. */
function at<T>(items: readonly T[], index: number): T {
  const item = items[index]
  if (item === undefined) throw new Error(`expected an element at index ${String(index)}`)
  return item
}

const server = setupServer()

interface Recorded {
  scans: unknown[]
  settingsPuts: SettingsUpdate[]
  purges: (string | null)[]
  /** Bodies of `POST /api/roots` — amendment A-11. */
  rootPosts: unknown[]
  /** `{name}` of each `DELETE /api/roots/{name}` — amendment A-11. */
  rootDeletes: string[]
}

let recorded: Recorded

beforeAll(() => {
  server.listen({ onUnhandledRequest: 'error' })
})
afterEach(() => {
  server.resetHandlers()
  resetBasePath()
  document.documentElement.removeAttribute('data-theme')
})
afterAll(() => {
  server.close()
})

beforeEach(() => {
  localStorage.clear()
  useUiStore.setState({ theme: 'system', overlays: [] })
  recorded = { scans: [], settingsPuts: [], purges: [], rootPosts: [], rootDeletes: [] }
  server.use(
    http.get(`${ORIGIN}/api/roots`, () =>
      HttpResponse.json({
        items: [
          root,
          {
            ...root,
            name: 'lanovel',
            label: '02. lanovel',
            path: '/mnt/big-data/pds/taison-data/02. books/02. lanovel',
            series_count: 2,
            total_bytes: 412e9,
          },
        ],
      }),
    ),
    http.get(`${ORIGIN}/api/settings`, () => HttpResponse.json(settings)),
    http.get(`${ORIGIN}/api/cache/usage`, () => HttpResponse.json(cacheUsage)),
    http.get(`${ORIGIN}/api/scan/log`, () =>
      HttpResponse.json({
        items: [
          { ...scanLogResponse.items[0], id: 1, level: 'info', message: 'scan start' },
          { ...scanLogResponse.items[0], id: 2, level: 'warn', message: 'password required' },
          { ...scanLogResponse.items[0], id: 3, level: 'error', message: 'bad central directory' },
        ],
      }),
    ),
    http.post(`${ORIGIN}/api/scan`, async ({ request }) => {
      recorded.scans.push(await request.json())
      return HttpResponse.json({ run_id: 'run-1' }, { status: 202 })
    }),
    http.put(`${ORIGIN}/api/settings`, async ({ request }) => {
      const body = (await request.json()) as SettingsUpdate
      recorded.settingsPuts.push(body)
      return HttpResponse.json({ ...settings, ...body } satisfies Settings)
    }),
    http.delete(`${ORIGIN}/api/cache`, ({ request }) => {
      recorded.purges.push(new URL(request.url).searchParams.get('kind'))
      return HttpResponse.json({ deleted_files: 4_935, freed_bytes: 285_000_000 })
    }),
    // Amendment A-11. Registered for every test, not only the A-11 block: MSW
    // runs with `onUnhandledRequest: 'error'`, so a component that fired one of
    // these while the capability is off would fail the *gate* tests loudly
    // rather than silently no-op.
    http.post(`${ORIGIN}/api/roots`, async ({ request }) => {
      recorded.rootPosts.push(await request.json())
      return HttpResponse.json(rootEntry, {
        status: 201,
        headers: { Location: `/api/roots/${rootEntry.name}` },
      })
    }),
    http.delete(`${ORIGIN}/api/roots/:name`, ({ params }) => {
      recorded.rootDeletes.push(String(params.name))
      return new HttpResponse(null, { status: 204 })
    }),
  )
})

function Harness() {
  const [open, setOpen] = useState(false)
  return (
    <>
      <button
        type="button"
        onClick={() => {
          setOpen(true)
        }}
      >
        설정 열기
      </button>
      <SettingsDialog
        open={open}
        onClose={() => {
          setOpen(false)
        }}
      />
    </>
  )
}

function renderDialog(harness = false): void {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      {harness ? <Harness /> : <SettingsDialog open onClose={() => undefined} />}
    </QueryClientProvider>,
  )
}

// ---------------------------------------------------------------------------
// 루트 관리 — ruling E-3 / C-5
// ---------------------------------------------------------------------------

describe('루트 관리 (prd UI-004, ruling E-3)', () => {
  it('lists each root with its path, counts and a 재스캔 button', async () => {
    renderDialog()
    expect(await screen.findByText('만화')).toBeInTheDocument()
    expect(screen.getByText('02. lanovel')).toBeInTheDocument()
    expect(
      screen.getByText('/mnt/big-data/pds/taison-data/02. books/01. mangga'),
    ).toBeInTheDocument()
    expect(screen.getByText('10 · 5.5 GB')).toBeInTheDocument()
    expect(screen.getAllByRole('button', { name: '재스캔' })).toHaveLength(2)
  })

  it('offers no way to add or remove a root, and says to edit the file instead', async () => {
    renderDialog()
    await screen.findByText('만화')
    expect(screen.queryByRole('button', { name: '제거' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '+ 루트 추가' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '루트 추가' })).not.toBeInTheDocument()
    expect(screen.getByText(/shelf\.yaml을 편집한 뒤 재시작하세요/)).toBeInTheDocument()
  })

  /**
   * Amendment A-10 / ruling E-25 — the note has to name the file.
   *
   * The config lookup order has four entries, so "edit shelf.yaml" on its own
   * sends the reader to whichever of the four they guess. The value comes down
   * `GET /api/settings` → `useSettings` → the panel: **no test here may pass
   * `configPath` as a prop**, because the prop already existed and already
   * worked — what shipped broken was that nothing supplied it.
   */
  it('names the resolved config file, sourced from GET /api/settings', async () => {
    const configPath = '/srv/shelf/etc/shelf.yaml'
    server.use(
      http.get(`${ORIGIN}/api/settings`, () =>
        HttpResponse.json({
          ...settings,
          server: { ...settings.server, config_path: configPath },
        } satisfies Settings),
      ),
    )
    renderDialog()
    expect(await screen.findByText(configPath)).toBeInTheDocument()
    expect(screen.getByText(/shelf\.yaml을 편집한 뒤 재시작하세요/)).toBeInTheDocument()
  })

  it('says only what it knows while /api/settings is still in flight', async () => {
    // The still-reachable branch of the same note: with no payload yet there is
    // no path to print, and inventing a plausible one would be worse than the
    // bug this field fixes.
    server.use(http.get(`${ORIGIN}/api/settings`, () => new Promise(() => undefined)))
    renderDialog()
    await screen.findByText('만화')
    expect(screen.getByText('shelf.yaml을 편집한 뒤 재시작하세요')).toBeInTheDocument()
    expect(screen.queryByTestId('config-path')).not.toBeInTheDocument()
  })

  it('treats an empty config_path as no path at all (arch §7.8)', async () => {
    // `""` is what §7.8 promises for a server built from a configuration with
    // no file. It is the absence of a path, not a short one, so it takes the
    // same branch as "not answered yet" — otherwise the note renders an empty
    // `<span data-testid="config-path">` and a dangling ` — `, which reads as a
    // path that failed to load, and which the assertion above would accept.
    server.use(
      http.get(`${ORIGIN}/api/settings`, () =>
        HttpResponse.json({
          ...settings,
          // `prefetch: 7` is the settle signal, and it has to be one: with
          // `config_path: ''` the note reads identically before and after the
          // payload lands, so waiting on the note would let this test pass
          // against a `/api/settings` that never answered at all — asserting
          // the loading state and calling it the empty state.
          prefetch: 7,
          server: { ...settings.server, config_path: '' },
        } satisfies Settings),
      ),
    )
    renderDialog()
    const slider = await screen.findByLabelText('프리페치 페이지')
    await waitFor(() => {
      expect(slider).toHaveValue('7')
    })

    expect(screen.getByText('shelf.yaml을 편집한 뒤 재시작하세요')).toBeInTheDocument()
    expect(screen.queryByTestId('config-path')).not.toBeInTheDocument()
    expect(screen.queryByText(/—/)).not.toBeInTheDocument()
  })

  it('rescans exactly one root via POST /api/scan', async () => {
    renderDialog()
    await screen.findByText('만화')
    const buttons = screen.getAllByRole('button', { name: '재스캔' })
    await userEvent.click(at(buttons, 1))
    await waitFor(() => {
      expect(recorded.scans).toEqual([{ roots: ['lanovel'] }])
    })
  })
})

// ---------------------------------------------------------------------------
// 루트 추가 / 제거 — amendment A-11, ruling E-26 (+ revisions R1 and R2)
// ---------------------------------------------------------------------------

/**
 * E-26 reversed E-3 in part: the screen may now write the `roots:` list.
 *
 * Every test below drives the capability, the list and the failure down
 * `GET /api/settings` / `GET /api/roots` / the mutation's own response — never
 * a prop. That is not style. The defect this area last shipped was a prop that
 * worked and a caller that never supplied it (ruling E-25), and a test that
 * hands `rootEditingEnabled` to the panel re-tests the half that was never
 * broken.
 *
 * Two of the assertions exist because a boolean pins `false` as happily as
 * `true` (impl-plan §0.3, A-11's golden row): the gate is asserted **on and off
 * against the same component**, and the list after a mutation is asserted to be
 * the server's answer rather than anything the client could have assembled from
 * what the user typed.
 */
describe('루트 추가/제거 (amendment A-11, ruling E-26)', () => {
  const SECOND: Root = {
    ...root,
    name: 'lanovel',
    label: '02. lanovel',
    path: '/mnt/big-data/pds/taison-data/02. books/02. lanovel',
    series_count: 2,
    total_bytes: 412e9,
  }

  /** Serves `GET /api/settings` with an explicit `server` block. */
  function serveSettings(overrides: Partial<ServerSettings>): void {
    server.use(
      http.get(`${ORIGIN}/api/settings`, () =>
        HttpResponse.json({
          ...settings,
          server: { ...settings.server, ...overrides },
        } satisfies Settings),
      ),
    )
  }

  /**
   * Serves `GET /api/roots` from a script: call 1 gets `pages[0]`, call 2 gets
   * `pages[1]`, and every later call repeats the last page. The returned
   * function reports how many times the endpoint was actually hit, which is how
   * "the list refetched" is asserted as a fact rather than inferred from the
   * DOM settling.
   */
  function serveRoots(pages: Root[][]): () => number {
    let calls = 0
    server.use(
      http.get(`${ORIGIN}/api/roots`, () => {
        const items = pages[Math.min(calls, pages.length - 1)] ?? []
        calls += 1
        return HttpResponse.json({ items })
      }),
    )
    return () => calls
  }

  // -- the gate ------------------------------------------------------------

  /**
   * `root_editing_enabled: false` is the default deployment (E-26 decision 2:
   * `server.allow_root_editing` ships false), and this project's rule is that a
   * **disabled control is a promise**. There is no promise to make here — no
   * click, no wait and no later request can lift a refusal that lives in a file
   * on the server — so the controls are absent, exactly as C-5 / E-3 left them.
   *
   * The `config_path` override is the settle signal. With the gate off the
   * panel looks identical before and after `/api/settings` lands, so an
   * assertion made without one would pass against a settings request that never
   * answered at all: it would be asserting the loading state and calling it the
   * gate.
   */
  it('renders no 추가/제거 controls — not even disabled ones — while the capability is off', async () => {
    serveSettings({ root_editing_enabled: false, config_path: '/etc/shelf/gate-off.yaml' })
    renderDialog()
    await screen.findByText('/etc/shelf/gate-off.yaml')

    expect(screen.queryByRole('button', { name: '루트 추가' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '+ 루트 추가' })).not.toBeInTheDocument()
    expect(screen.queryAllByRole('button', { name: '제거' })).toHaveLength(0)

    // What C-5 / E-3 describe is still all there, unchanged.
    expect(screen.getAllByRole('button', { name: '재스캔' })).toHaveLength(2)
    expect(screen.getByText(/shelf\.yaml을 편집한 뒤 재시작하세요/)).toBeInTheDocument()
  })

  it('renders both controls when the same payload says the capability is on', async () => {
    serveSettings({ root_editing_enabled: true, config_path: '/etc/shelf/gate-on.yaml' })
    renderDialog()
    await screen.findByText('/etc/shelf/gate-on.yaml')

    expect(screen.getByRole('button', { name: '루트 추가' })).toBeInTheDocument()
    expect(screen.getAllByRole('button', { name: '제거' })).toHaveLength(2)
    // The capability adds controls; it removes none.
    expect(screen.getAllByRole('button', { name: '재스캔' })).toHaveLength(2)
    expect(screen.getByText(/shelf\.yaml을 편집한 뒤 재시작하세요/)).toBeInTheDocument()
  })

  // -- the add flow --------------------------------------------------------

  it('adds a root through POST /api/roots and then shows the row the server reports', async () => {
    serveSettings({ root_editing_enabled: true })
    const rootCalls = serveRoots([[root], [root, pendingRoot]])
    renderDialog()
    await screen.findByText('만화')

    await userEvent.click(screen.getByRole('button', { name: '루트 추가' }))
    await userEvent.type(screen.getByLabelText('루트 경로'), pendingRoot.path)
    await userEvent.type(screen.getByLabelText('이름표 (선택)'), '02. lanovel')
    await userEvent.click(screen.getByRole('button', { name: '추가' }))

    await waitFor(() => {
      expect(recorded.rootPosts).toEqual([{ path: pendingRoot.path, label: '02. lanovel' }])
    })

    // The row that appears is the one `GET /api/roots` answered with — the
    // refetch is asserted at the endpoint, not inferred from the screen.
    expect(await screen.findByText('02. lanovel')).toBeInTheDocument()
    await waitFor(() => {
      expect(rootCalls()).toBeGreaterThan(1)
    })
  })

  it('omits label from the body entirely when the field is left blank (§7.4)', async () => {
    // `label` is `label?: string` in `RootCreate`, and §7.1 rejects an unknown
    // *field*, not an absent optional one — but an empty string is a value, and
    // §7.4 would write it to the file as the root's display name.
    serveSettings({ root_editing_enabled: true })
    serveRoots([[root]])
    renderDialog()
    await screen.findByText('만화')

    await userEvent.click(screen.getByRole('button', { name: '루트 추가' }))
    await userEvent.type(screen.getByLabelText('루트 경로'), '/srv/media/manga')
    await userEvent.click(screen.getByRole('button', { name: '추가' }))

    await waitFor(() => {
      expect(recorded.rootPosts).toEqual([{ path: '/srv/media/manga' }])
    })
  })

  /**
   * The whole point of server-side validation is that the user learns which
   * rule they broke (E-26 decision 4: "each rejection is a `400` naming the
   * rule it broke"). A single 실패했습니다 throws that away, so each `reason`
   * gets its own sentence and the two that carry `conflicts_with` name the root
   * they collided with.
   */
  it.each([
    ['not_absolute', {}, /절대 경로/],
    ['does_not_exist', {}, /경로가 없습니다/],
    ['not_a_directory', {}, /폴더가 아닙니다/],
    ['not_readable', {}, /읽을 수 없습니다/],
    ['duplicate', { conflicts_with: 'mangga' }, /이미.*mangga/],
    ['overlaps', { conflicts_with: 'mangga' }, /mangga.*(상위|하위)/],
    ['contains_storage', {}, /데이터.*캐시/],
  ])('surfaces the %s rejection as its own message', async (reason, extra, expected) => {
    serveSettings({ root_editing_enabled: true })
    serveRoots([[root]])
    server.use(
      http.post(`${ORIGIN}/api/roots`, () =>
        HttpResponse.json(
          errorEnvelope('bad_request', 'rejected', { field: 'path', reason, ...extra }),
          { status: 400 },
        ),
      ),
    )
    renderDialog()
    await screen.findByText('만화')

    await userEvent.click(screen.getByRole('button', { name: '루트 추가' }))
    await userEvent.type(screen.getByLabelText('루트 경로'), '/srv/media/manga')
    await userEvent.click(screen.getByRole('button', { name: '추가' }))

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent(expected)
    // The form stays open with the value in it: the user has to edit the path,
    // and a cleared field means retyping it to find out what was wrong.
    expect(screen.getByLabelText('루트 경로')).toHaveValue('/srv/media/manga')
  })

  it('names the switch to throw when the server answers 403 forbidden', async () => {
    // The gate can shut between the settings read and the write (someone edits
    // the file, or this tab is stale). `forbidden` must not reach the re-auth
    // path of ruling E-17 — no login lifts it — so the message names the key.
    serveSettings({ root_editing_enabled: true })
    serveRoots([[root]])
    server.use(
      http.post(`${ORIGIN}/api/roots`, () =>
        HttpResponse.json(errorEnvelope('forbidden', 'root editing is disabled', {
          reason: 'disabled',
        }), { status: 403 }),
      ),
    )
    renderDialog()
    await screen.findByText('만화')

    await userEvent.click(screen.getByRole('button', { name: '루트 추가' }))
    await userEvent.type(screen.getByLabelText('루트 경로'), '/srv/media/manga')
    await userEvent.click(screen.getByRole('button', { name: '추가' }))

    expect(await screen.findByRole('alert')).toHaveTextContent('allow_root_editing')
  })

  // -- the remove flow -----------------------------------------------------

  /**
   * E-26's REVISION (R1) changed the sentence this dialog owes the user.
   * Removal now purges the root's `index.db` rows, so the series really do go —
   * and `user.db` is untouched, so the reading progress really does stay. The
   * dialog must say the second and must not promise the first.
   */
  it('asks before DELETE and states what R1 actually keeps', async () => {
    serveSettings({ root_editing_enabled: true })
    serveRoots([[root, SECOND]])
    renderDialog()
    await screen.findByText('02. lanovel')

    const row = screen.getByTestId('root-row-lanovel')
    await userEvent.click(within(row).getByRole('button', { name: '제거' }))

    expect(recorded.rootDeletes).toEqual([])
    const alert = screen.getByRole('alert')
    expect(alert).toHaveTextContent('02. lanovel')
    expect(alert).toHaveTextContent('읽기 진행률은 유지됩니다')
    // R1: the index rows are purged, so nothing here may say they survive.
    expect(alert).not.toHaveTextContent('색인은 유지')
  })

  it('abandons the removal on 취소', async () => {
    serveSettings({ root_editing_enabled: true })
    serveRoots([[root, SECOND]])
    renderDialog()
    await screen.findByText('02. lanovel')

    const row = screen.getByTestId('root-row-lanovel')
    await userEvent.click(within(row).getByRole('button', { name: '제거' }))
    await userEvent.click(within(row).getByRole('button', { name: '취소' }))

    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(recorded.rootDeletes).toEqual([])
    expect(screen.getByText('02. lanovel')).toBeInTheDocument()
  })

  it('removes the confirmed root and re-reads the list from the server', async () => {
    serveSettings({ root_editing_enabled: true })
    const rootCalls = serveRoots([[root, SECOND], [root]])
    renderDialog()
    await screen.findByText('02. lanovel')

    await userEvent.click(
      within(screen.getByTestId('root-row-lanovel')).getByRole('button', { name: '제거' }),
    )
    await userEvent.click(
      within(screen.getByTestId('root-row-lanovel')).getByRole('button', { name: '제거' }),
    )

    await waitFor(() => {
      expect(recorded.rootDeletes).toEqual(['lanovel'])
    })
    // The row goes because the server stopped listing it, not because the
    // client spliced it out of a cached array.
    await waitFor(() => {
      expect(screen.queryByText('02. lanovel')).not.toBeInTheDocument()
    })
    expect(rootCalls()).toBeGreaterThan(1)
    expect(screen.getByText('만화')).toBeInTheDocument()
  })

  it('explains a 409 on removal instead of dropping the row', async () => {
    serveSettings({ root_editing_enabled: true })
    serveRoots([[root]])
    server.use(
      http.delete(`${ORIGIN}/api/roots/:name`, () =>
        HttpResponse.json(errorEnvelope('conflict', 'last root'), { status: 409 }),
      ),
    )
    renderDialog()
    await screen.findByText('만화')

    await userEvent.click(
      within(screen.getByTestId('root-row-mangga')).getByRole('button', { name: '제거' }),
    )
    await userEvent.click(
      within(screen.getByTestId('root-row-mangga')).getByRole('button', { name: '제거' }),
    )

    expect(await screen.findByRole('alert')).toHaveTextContent('마지막 루트')
    expect(screen.getByText('만화')).toBeInTheDocument()
  })

  /**
   * §7.4 gives every `409` a `detail.reason`, and one sentence for all of them
   * throws that away exactly as one 실패했습니다 would throw away the `400` table.
   *
   * The row that forced it is `not_a_block_sequence`: `roots: [{...}]` in YAML
   * flow style is *valid YAML*, and this very screen is reading that file —
   * `GET /api/roots` and `GET /api/settings` both answered from it — so
   * "설정 파일을 읽을 수 없습니다" is a claim the user can see is false while
   * looking at its contents, and the real remedy (rewrite `roots:` as a block
   * list) appeared nowhere at all.
   *
   * The negative half is **every other row's sentence**, and it is written that
   * way because the previous version could not fail. Two of the five rows
   * forbade `/읽을 수 없/`, and no `409` this component can print contains that
   * string: `features/roots/rootErrors.ts` answers 편집할 수 없는 상태 in the
   * fallback, and the only surviving 읽을 수 없 is the `400 not_readable`
   * message, which a `DELETE` cannot produce. Measured against the table below:
   * a mutant that makes every `409` print all five sentences concatenated turns
   * all five rows red. It could not have turned those two red under the old
   * column, because the concatenation does not contain 읽을 수 없 either — they
   * would have stayed green while the alert printed every sibling's message.
   * Read off the table, the forbidden set cannot go stale: a sixth reason gets
   * its teeth from the same list that gives it its own assertion.
   */
  const CONFLICT_MESSAGES: [reason: string, says: RegExp][] = [
    ['last_root', /마지막 루트는 제거할 수 없습니다/],
    ['not_a_block_sequence', /여러 줄 목록/],
    ['unparseable', /YAML 문법/],
    ['file_missing', /더 이상 없습니다/],
    ['duplicate', /같은 이름의 루트/],
  ]

  /** The confirmation the alert carries *before* the failure replaces it. */
  const REMOVE_CONFIRM = '읽기 진행률은 유지됩니다'

  it.each(CONFLICT_MESSAGES)('tells the %s cause of a 409 apart from the others', async (reason) => {
    serveSettings({ root_editing_enabled: true })
    serveRoots([[root, SECOND]])
    server.use(
      http.delete(`${ORIGIN}/api/roots/:name`, () =>
        HttpResponse.json(errorEnvelope('conflict', 'refused', { reason }), { status: 409 }),
      ),
    )
    renderDialog()
    await screen.findByText('02. lanovel')

    await userEvent.click(
      within(screen.getByTestId('root-row-lanovel')).getByRole('button', { name: '제거' }),
    )
    await userEvent.click(
      within(screen.getByTestId('root-row-lanovel')).getByRole('button', { name: '제거' }),
    )

    // Settle on the confirmation being replaced — a signal every row shares and
    // none of the assertions below is about. Waiting on `says` instead is what
    // made "fails on both halves" unobservable: a collapsed table threw inside
    // the `waitFor`, so the negative half was never reached even on the rows
    // that had teeth.
    const alert = await screen.findByRole('alert')
    await waitFor(() => {
      expect(alert).not.toHaveTextContent(REMOVE_CONFIRM)
    })

    // `soft`, so both halves are always evaluated and both are reported. A
    // hard `expect` on the positive half would again hide the negative one.
    for (const [otherReason, otherSays] of CONFLICT_MESSAGES) {
      if (otherReason === reason) {
        expect.soft(alert).toHaveTextContent(otherSays)
      } else {
        expect.soft(alert).not.toHaveTextContent(otherSays)
      }
    }

    // The row stays: nothing was removed, so removing it from the screen would
    // be the client inventing an outcome the server refused.
    expect(screen.getByText('02. lanovel')).toBeInTheDocument()
  })

  it('answers a flow-style roots: list with the block-list remedy, not "unreadable"', async () => {
    serveSettings({ root_editing_enabled: true })
    serveRoots([[root]])
    server.use(
      http.post(`${ORIGIN}/api/roots`, () =>
        HttpResponse.json(
          errorEnvelope('conflict', 'the roots list is not a block sequence', {
            reason: 'not_a_block_sequence',
          }),
          { status: 409 },
        ),
      ),
    )
    renderDialog()
    await screen.findByText('만화')

    await userEvent.click(screen.getByRole('button', { name: '루트 추가' }))
    await userEvent.type(screen.getByLabelText('루트 경로'), '/srv/media/manga')
    await userEvent.click(screen.getByRole('button', { name: '추가' }))

    const alert = await screen.findByRole('alert')
    expect.soft(alert).toHaveTextContent(/여러 줄 목록/)
    expect.soft(alert).toHaveTextContent(/roots:/)
    // The sentence this replaces — the generic `409` answer for 추가, told to a
    // user whose file the same screen is successfully displaying two paragraphs
    // below, and carrying no remedy at all.
    //
    // It used to forbid `/읽을 수 없/`, which is the sentence the fallback used
    // to be and no longer is; nothing this component can print in a 409 contains
    // that string any more (`features/roots/rootErrors.ts` — the only surviving
    // 읽을 수 없 is the `400 not_readable` message, unreachable from here), so
    // that assertion could not fail. This one can: drop the
    // `not_a_block_sequence` row from `CONFLICT_REASONS` and it is exactly what
    // the alert says.
    expect.soft(alert).not.toHaveTextContent(/편집할 수 없어 아무것도 쓰지 않았습니다/)
  })

  // -- the pending row (R2) ------------------------------------------------

  /**
   * R2: `POST` writes the file, and roots are opened once at startup, so the
   * new root cannot be served until the restart. The row is present — the
   * design shows it — and says so. A row that *claimed* to be loaded is the
   * failure the original "deliberately unchanged" sentence guarded against.
   */
  it('marks a pending root as awaiting the restart, with no counts and no 재스캔', async () => {
    serveSettings({ root_editing_enabled: true })
    serveRoots([[root, pendingRoot]])
    renderDialog()
    await screen.findByText('02. lanovel')

    const row = screen.getByTestId('root-row-lanovel')
    expect(within(row).getByText('재시작 후 적용')).toBeInTheDocument()
    // Zero counts are not counts. §7.3 fixes them at zero for a pending row, so
    // printing them would be printing "0 · 0 B" beside a folder full of books.
    expect(within(row).queryByText(/·/)).not.toBeInTheDocument()
    expect(within(row).queryByRole('button', { name: '재스캔' })).not.toBeInTheDocument()
    // 제거 stays: a root added with the wrong path must be removable before the
    // restart, and `DELETE` answers on it — it is in the file on disk.
    expect(within(row).getByRole('button', { name: '제거' })).toBeInTheDocument()

    // The loaded root is untouched by any of that.
    const loaded = screen.getByTestId('root-row-mangga')
    expect(within(loaded).getByRole('button', { name: '재스캔' })).toBeInTheDocument()
    expect(within(loaded).getByText('10 · 5.5 GB')).toBeInTheDocument()
  })

  // -- the restart notice (config_changed_on_disk) -------------------------

  /**
   * The notice is server state, not tab state: it is equally true when the user
   * hand-edited the file, which is the workflow C-5 has been printing all
   * along, so it survives a reload and cannot be a flag set by the mutation.
   * The copy is fixed by arch §7.8 — the field flips on a comment edit too, so
   * it says the file *changed*, never that the user *must* restart.
   */
  it('shows the restart notice when the server says the file changed', async () => {
    serveSettings({ root_editing_enabled: true, config_changed_on_disk: true })
    serveRoots([[root]])
    renderDialog()

    const notice = await screen.findByTestId('config-changed-notice')
    expect(notice).toHaveTextContent('설정 파일이 변경되었습니다')
    expect(notice).toHaveTextContent('다시 시작하면 적용됩니다')
    expect(notice).not.toHaveTextContent('다시 시작해야 합니다')
  })

  it('shows it without the capability too — a hand-edit is not a write of ours', async () => {
    serveSettings({ root_editing_enabled: false, config_changed_on_disk: true })
    serveRoots([[root]])
    renderDialog()

    expect(await screen.findByTestId('config-changed-notice')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '루트 추가' })).not.toBeInTheDocument()
  })

  it('shows no notice while the file matches what the server loaded', async () => {
    serveSettings({
      root_editing_enabled: true,
      config_changed_on_disk: false,
      config_path: '/etc/shelf/unchanged.yaml',
    })
    serveRoots([[root]])
    renderDialog()
    await screen.findByText('/etc/shelf/unchanged.yaml')

    expect(screen.queryByTestId('config-changed-notice')).not.toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// 캐시 — FR-THM-008
// ---------------------------------------------------------------------------

describe('캐시 (FR-THM-008)', () => {
  it('reports the total and the per-kind breakdown', async () => {
    renderDialog()
    expect(await screen.findByText('285')).toBeInTheDocument() // 285 000 000 B → 285 MB (E-11)
    expect(screen.getByText('MB')).toBeInTheDocument()
    expect(screen.getByText('썸네일 · 압축 해제 페이지 캐시')).toBeInTheDocument()
    expect(screen.getByText('4,812개 · 226 MB')).toBeInTheDocument()
    expect(screen.getByTestId('cache-bar-thumbs')).toBeInTheDocument()
  })

  it('does not purge on the first click — it asks first', async () => {
    renderDialog()
    await screen.findByText('썸네일 · 압축 해제 페이지 캐시')
    await userEvent.click(screen.getByRole('button', { name: '전체 삭제' }))
    expect(screen.getByRole('alert')).toHaveTextContent('캐시를 모두 삭제할까요?')
    expect(recorded.purges).toEqual([])
  })

  it('purges everything once confirmed', async () => {
    renderDialog()
    await screen.findByText('썸네일 · 압축 해제 페이지 캐시')
    await userEvent.click(screen.getByRole('button', { name: '전체 삭제' }))
    await userEvent.click(screen.getByRole('button', { name: '전체 삭제' }))
    await waitFor(() => {
      expect(recorded.purges).toEqual(['all'])
    })
  })

  it('abandons the purge on 취소', async () => {
    renderDialog()
    await screen.findByText('썸네일 · 압축 해제 페이지 캐시')
    await userEvent.click(screen.getByRole('button', { name: '전체 삭제' }))
    await userEvent.click(screen.getByRole('button', { name: '취소' }))
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(recorded.purges).toEqual([])
  })
})

// ---------------------------------------------------------------------------
// 읽기 기본값 + 테마
// ---------------------------------------------------------------------------

describe('읽기 기본값 (prd UI-004)', () => {
  it('reflects the server settings rather than the client-side fallbacks', async () => {
    // Non-default values on every row, so a component that ignored the payload
    // and rendered its own defaults would fail here.
    server.use(
      http.get(`${ORIGIN}/api/settings`, () =>
        HttpResponse.json({
          ...settings,
          reading_direction: 'rtl',
          display_mode: 'spread',
          prefetch: 7,
        } satisfies Settings),
      ),
    )
    renderDialog()
    const slider = await screen.findByLabelText('프리페치 페이지')
    await waitFor(() => {
      expect(slider).toHaveValue('7')
    })
    const dir = screen.getByRole('radiogroup', { name: '읽기 방향' })
    expect(within(dir).getByRole('radio', { name: 'R→L' })).toBeChecked()
    const mode = screen.getByRole('radiogroup', { name: '표시 모드' })
    expect(within(mode).getByRole('radio', { name: '양면' })).toBeChecked()
  })

  it('writes the wire enum, not the Korean label (C-1)', async () => {
    renderDialog()
    const mode = await screen.findByRole('radiogroup', { name: '표시 모드' })
    await userEvent.click(within(mode).getByRole('radio', { name: '양면' }))
    await waitFor(() => {
      expect(recorded.settingsPuts).toEqual([{ display_mode: 'spread' }])
    })
  })

  it('writes 읽기 방향 as ltr/rtl', async () => {
    renderDialog()
    const dir = await screen.findByRole('radiogroup', { name: '읽기 방향' })
    await userEvent.click(within(dir).getByRole('radio', { name: 'R→L' }))
    await waitFor(() => {
      expect(recorded.settingsPuts).toEqual([{ reading_direction: 'rtl' }])
    })
  })

  it('caps 프리페치 페이지 at the ui-spec range and persists the value', async () => {
    renderDialog()
    const slider = await screen.findByLabelText('프리페치 페이지')
    // ui-spec §8.6 says 0..12 even though the wire accepts 0..20.
    expect(slider).toHaveAttribute('min', '0')
    expect(slider).toHaveAttribute('max', '12')
    await waitFor(() => {
      expect(slider).toHaveValue('4')
    })
    // A range input is driven by its value, not by typing into it.
    fireEvent.change(slider, { target: { value: '9' } })
    await waitFor(() => {
      expect(recorded.settingsPuts).toEqual([{ prefetch: 9 }])
    })
  })

  it('carries the ui-spec 130px on a wrapper the base stylesheet cannot outrank', async () => {
    renderDialog()
    const slider = await screen.findByLabelText('프리페치 페이지')

    // `styles/base.css` sets `input[type='range'] { width: 100% }` at
    // specificity (0,1,1); a `w-[130px]` *on the input* is (0,1,0) and loses,
    // stretching the slider across the row and crushing the label to one
    // syllable per line. The width therefore has to sit on the parent, where
    // the input's own `width:100%` resolves to ui-spec §8.6 §2's 130px.
    expect(slider).toHaveClass('w-full')
    expect(slider).not.toHaveClass('w-[130px]')
    const track = slider.parentElement
    expect(track).not.toBeNull()
    expect(track).toHaveClass('w-[130px]', 'flex-none')

    // Same row, same failure: a `flex-1` Korean label wraps to its min-content
    // width, which is one character.
    const label = document.querySelector('label[for="settings-prefetch"]')
    expect(label).toHaveClass('whitespace-nowrap')
  })
})

describe('테마 (NFR-CMP-003)', () => {
  it('replaces the prototype static text with a real 라이트/다크/시스템 control', async () => {
    renderDialog()
    const themeGroup = await screen.findByRole('radiogroup', { name: '테마' })
    for (const label of ['라이트', '다크', '시스템']) {
      expect(within(themeGroup).getByRole('radio', { name: label })).toBeInTheDocument()
    }
    expect(screen.getByText('뷰어 고정')).toBeInTheDocument()
  })

  it('flips data-theme on <html> immediately and persists it to the server', async () => {
    renderDialog()
    const themeGroup = await screen.findByRole('radiogroup', { name: '테마' })

    await userEvent.click(within(themeGroup).getByRole('radio', { name: '다크' }))
    expect(document.documentElement).toHaveAttribute('data-theme', 'dark')
    await waitFor(() => {
      expect(recorded.settingsPuts).toEqual([{ theme: 'dark' }])
    })

    await userEvent.click(within(themeGroup).getByRole('radio', { name: '라이트' }))
    expect(document.documentElement).toHaveAttribute('data-theme', 'light')
    await waitFor(() => {
      expect(recorded.settingsPuts).toHaveLength(2)
    })
    expect(recorded.settingsPuts[1]).toEqual({ theme: 'light' })
    expect(useUiStore.getState().theme).toBe('light')
  })
})

// ---------------------------------------------------------------------------
// 스캔 로그 — FR-IDX-004
// ---------------------------------------------------------------------------

describe('스캔 로그 (FR-IDX-004)', () => {
  it('renders each entry with its level, colour-coded, and a warn/error summary', async () => {
    renderDialog()
    await screen.findByText('bad central directory')
    const body = screen.getByTestId('scan-log-body')
    expect(within(body).getByText('INFO')).toHaveClass('text-ink-dim')
    expect(within(body).getByText('WARN')).toHaveClass('text-accent-text')
    expect(within(body).getByText('ERROR')).toHaveClass('text-accent')
    expect(within(body).getByText('bad central directory')).toBeInTheDocument()
    expect(screen.getByText('경고 1 · 오류 1')).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Dialog contract — WP-10 acceptance 9
// ---------------------------------------------------------------------------

describe('the dialog contract (WP-10 acceptance 9)', () => {
  it('is aria-modal, traps Tab, closes on Esc and restores focus', async () => {
    renderDialog(true)
    const trigger = screen.getByRole('button', { name: '설정 열기' })
    await userEvent.click(trigger)

    const dialog = screen.getByRole('dialog')
    expect(dialog).toHaveAttribute('aria-modal', 'true')
    // The first focusable is the header's esc button.
    expect(screen.getByRole('button', { name: '닫기' })).toHaveFocus()

    // Shift+Tab off the first element wraps to the last, i.e. never escapes.
    await userEvent.tab({ shift: true })
    expect(dialog.contains(document.activeElement)).toBe(true)

    await userEvent.keyboard('{Escape}')
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(trigger).toHaveFocus()
  })

  it('closes from the header esc button', async () => {
    renderDialog(true)
    await userEvent.click(screen.getByRole('button', { name: '설정 열기' }))
    await userEvent.click(screen.getByRole('button', { name: '닫기' }))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })
})

/** Guards the `rootsResponse` fixture staying the shape this file assumes. */
describe('fixture sanity', () => {
  it('uses the contract shape for roots', () => {
    expect(rootsResponse.items[0]?.name).toBe('mangga')
  })
})
