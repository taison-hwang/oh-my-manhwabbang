import '@testing-library/jest-dom/vitest'

import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { setupServer } from 'msw/node'
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from 'vitest'

import { errorEnvelope, ORIGIN } from '../../api/fixtures'
import type { BrowseEntry, BrowseResponse } from '../../api/types'
import { resetBasePath } from '../../api/urls'
import { FolderPicker } from './FolderPicker'

/**
 * 폴더 찾아보기 — amendment **A-12**, ruling **E-40** (ui-spec §8.6 §1).
 *
 * The load-bearing assertions, and each is a rule the ruling states rather than
 * a rendering detail:
 *
 *  * **The component never re-derives `selectable`.** It renders what the server
 *    computed from §7.4's own rules. A picker with its own opinion would drift
 *    from `POST /api/roots`, invisibly, until a user clicked a folder the server
 *    then refused (§6.5).
 *  * **Descending is always allowed; picking is gated.** An unselectable folder
 *    may still contain the wanted one.
 *  * **Picking fills the field, it does not submit** — asserted here as "the
 *    callback carries the path the server gave", and in `AddRootForm.test.tsx`
 *    as "the form is still open".
 *  * **A capped listing says so** (§6.5), and a `403` prints the key to set
 *    rather than a generic failure.
 */

function entry(over: Partial<BrowseEntry> & { path: string }): BrowseEntry {
  return {
    name: over.path.split('/').pop() ?? over.path,
    selectable: true,
    reason: null,
    ...over,
  }
}

function level(over: Partial<BrowseResponse> = {}): BrowseResponse {
  return { path: '', parent: null, self: null, entries: [], truncated: false, ...over }
}

const BASE = '/mnt/media'

/** The two levels every test below walks: the base list, then the base itself. */
function defaultHandler() {
  return http.get(`${ORIGIN}/api/browse`, ({ request }) => {
    const path = new URL(request.url).searchParams.get('path')
    if (path === null) {
      return HttpResponse.json(level({ entries: [entry({ path: BASE })] }))
    }
    if (path === BASE) {
      return HttpResponse.json(
        level({
          path: BASE,
          parent: null,
          self: entry({ path: BASE }),
          entries: [
            entry({ path: `${BASE}/만화` }),
            entry({ path: `${BASE}/이미등록`, selectable: false, reason: 'duplicate' }),
          ],
        }),
      )
    }
    return HttpResponse.json(
      level({ path, parent: BASE, self: entry({ path }), entries: [] }),
    )
  })
}

const server = setupServer(defaultHandler())

beforeAll(() => {
  server.listen({ onUnhandledRequest: 'error' })
})
afterEach(() => {
  server.resetHandlers(defaultHandler())
  resetBasePath()
})
afterAll(() => {
  server.close()
})

function renderPicker(onPick = vi.fn(), onCancel = vi.fn()) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  })
  render(
    <QueryClientProvider client={client}>
      <FolderPicker onPick={onPick} onCancel={onCancel} />
    </QueryClientProvider>,
  )
  return { onPick, onCancel }
}

describe('FolderPicker', () => {
  it('opens on the base list, which names no directory and offers no way up', async () => {
    renderPicker()

    expect(await screen.findByTestId('folder-picker-crumb')).toHaveTextContent(
      '탐색할 수 있는 폴더',
    )
    // `path: ''` and `parent: null` at the top level: there is nothing above the
    // allowlist, and a 상위 button there would be a click the server refuses.
    expect(screen.queryByRole('button', { name: /상위/ })).not.toBeInTheDocument()
    // `self` is null too, so there is no single directory to choose.
    expect(screen.queryByTestId('folder-picker-choose-current')).not.toBeInTheDocument()
    expect(await screen.findByText(BASE)).toBeInTheDocument()
  })

  it('descends into a base and prints the server’s reason on an unselectable row', async () => {
    const user = userEvent.setup()
    renderPicker()

    await user.click(await screen.findByText(BASE))

    const list = await screen.findByTestId('folder-picker-list')
    const rows = within(list).getAllByRole('listitem')
    expect(rows).toHaveLength(2)

    // The reason comes off the wire. Nothing in this component knows what
    // `duplicate` means for a path — only what it reads as.
    expect(within(at(rows, 1)).getByText('이미 등록된 루트')).toBeInTheDocument()
    expect(within(at(rows, 1)).getByRole('button', { name: '선택' })).toBeDisabled()
    expect(within(at(rows, 0)).getByRole('button', { name: '선택' })).toBeEnabled()
  })

  it('lets an unselectable folder be opened even though it cannot be chosen', async () => {
    const user = userEvent.setup()
    renderPicker()

    await user.click(await screen.findByText(BASE))
    // The row's own name button, not its 선택 button.
    await user.click(await screen.findByText('이미등록'))

    // Refusing to descend would hide every sibling underneath an existing root.
    await waitFor(() => {
      expect(screen.getByTestId('folder-picker-crumb')).toHaveTextContent(`${BASE}/이미등록`)
    })
  })

  it('hands back the path the server gave, not one it assembled', async () => {
    const user = userEvent.setup()
    const { onPick } = renderPicker()

    await user.click(await screen.findByText(BASE))
    const list = await screen.findByTestId('folder-picker-list')
    await user.click(within(at(within(list).getAllByRole('listitem'), 0)).getByRole('button', {
      name: '선택',
    }))

    expect(onPick).toHaveBeenCalledExactlyOnceWith(`${BASE}/만화`)
  })

  it('offers the directory it is standing in, which no row can reach', async () => {
    const user = userEvent.setup()
    const { onPick } = renderPicker()

    await user.click(await screen.findByText(BASE))
    await user.click(await screen.findByTestId('folder-picker-choose-current'))

    // `self` — the base's own row lives one level up, which does not exist here.
    expect(onPick).toHaveBeenCalledExactlyOnceWith(BASE)
  })

  it('walks back up to the base list', async () => {
    const user = userEvent.setup()
    renderPicker()

    await user.click(await screen.findByText(BASE))
    await user.click(await screen.findByText('만화'))
    await waitFor(() => {
      expect(screen.getByTestId('folder-picker-crumb')).toHaveTextContent(`${BASE}/만화`)
    })

    await user.click(screen.getByRole('button', { name: /상위/ }))
    await waitFor(() => {
      expect(screen.getByTestId('folder-picker-crumb')).toHaveTextContent(BASE)
    })

    await user.click(screen.getByRole('button', { name: '처음으로' }))
    await waitFor(() => {
      expect(screen.getByTestId('folder-picker-crumb')).toHaveTextContent('탐색할 수 있는 폴더')
    })
  })

  it('says a listing was capped instead of presenting it as complete', async () => {
    server.use(
      http.get(`${ORIGIN}/api/browse`, () =>
        HttpResponse.json(
          level({ path: BASE, self: entry({ path: BASE }), entries: [], truncated: true }),
        ),
      ),
    )
    renderPicker()

    // §6.5: a silent truncation reads as "this is everything".
    expect(await screen.findByTestId('folder-picker-truncated')).toHaveTextContent(
      '경로를 직접 입력하세요',
    )
  })

  it('prints the configuration key to set when the server has no bases', async () => {
    server.use(
      http.get(`${ORIGIN}/api/browse`, () =>
        HttpResponse.json(
          errorEnvelope('forbidden', 'no directories are open to the picker', {
            reason: 'no_browse_bases',
          }),
          { status: 403 },
        ),
      ),
    )
    renderPicker()

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('server.browse_bases')
    // Not a failure of the add — the path can still be typed — so the way out is
    // 닫기 rather than an invitation to retry a request that cannot succeed.
    expect(screen.getByRole('button', { name: '닫기' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /다시 시도/ })).not.toBeInTheDocument()
  })
})

/** `noUncheckedIndexedAccess` types an indexed result as `T | undefined`. */
function at<T>(items: readonly T[], index: number): T {
  const item = items[index]
  if (item === undefined) throw new Error(`expected an element at index ${String(index)}`)
  return item
}
