import '@testing-library/jest-dom/vitest'

import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { setupServer } from 'msw/node'
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from 'vitest'

import { ORIGIN, rootEntry, rootsResponse } from '../../api/fixtures'
import { resetBasePath } from '../../api/urls'
import { AddRootForm } from './AddRootForm'

/**
 * 루트 추가 — amendment **A-11** (ruling E-26), picker and hot add by **A-12**
 * (ruling **E-40**).
 *
 * **This file exists because the two sentences E-40 changed had no test at all.**
 * The form said *"폴더 찾아보기는 제공하지 않습니다"* and *"추가한 루트는 서버를
 * 다시 시작한 뒤 읽힙니다"*, and both survived a ruling that made them false
 * without a single assertion going red. That is §6.5's pattern exactly, so the
 * replacements are pinned here rather than left to the next reader to notice.
 */

const BROWSE_BASE = '/mnt/media'

const server = setupServer(
  http.get(`${ORIGIN}/api/browse`, ({ request }) => {
    const path = new URL(request.url).searchParams.get('path')
    const at = path ?? ''
    return HttpResponse.json({
      path: at,
      parent: null,
      self: at === '' ? null : { name: 'media', path: at, selectable: true, reason: null },
      entries:
        at === ''
          ? [{ name: 'media', path: BROWSE_BASE, selectable: true, reason: null }]
          : [{ name: '만화', path: `${BROWSE_BASE}/만화`, selectable: true, reason: null }],
      truncated: false,
    })
  }),
  http.get(`${ORIGIN}/api/roots`, () => HttpResponse.json(rootsResponse)),
  http.post(`${ORIGIN}/api/roots`, () => HttpResponse.json(rootEntry, { status: 201 })),
  http.get(`${ORIGIN}/api/settings`, () => HttpResponse.json({}, { status: 200 })),
  http.get(`${ORIGIN}/api/scan/status`, () => HttpResponse.json({}, { status: 200 })),
)

beforeAll(() => {
  server.listen({ onUnhandledRequest: 'bypass' })
})
afterEach(() => {
  server.resetHandlers()
  resetBasePath()
})
afterAll(() => {
  server.close()
})

function renderForm(props: { canBrowse?: boolean } = {}) {
  const onDone = vi.fn()
  const onCancel = vi.fn()
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  render(
    <QueryClientProvider client={client}>
      <AddRootForm onDone={onDone} onCancel={onCancel} canBrowse={props.canBrowse ?? false} />
    </QueryClientProvider>,
  )
  return { onDone, onCancel }
}

describe('AddRootForm', () => {
  it('no longer says a restart is needed, and does not promise more than it can', () => {
    renderForm()

    // E-40: the add is adopted by the running server.
    expect(screen.getByText('추가하면 바로 읽기 시작합니다.')).toBeInTheDocument()
    expect(screen.queryByText(/다시 시작한 뒤 읽힙니다/)).not.toBeInTheDocument()
    // And it stops short of "it is now scanning": the adoption can fail after
    // the file write, and the row is what reports which happened.
    expect(screen.queryByText(/스캔 중/)).not.toBeInTheDocument()
  })

  it('offers 찾아보기 only where root editing is on', () => {
    const { rerender } = render(
      <QueryClientProvider client={new QueryClient()}>
        <AddRootForm onDone={vi.fn()} onCancel={vi.fn()} canBrowse={false} />
      </QueryClientProvider>,
    )
    expect(screen.queryByTestId('browse-folders')).not.toBeInTheDocument()
    // The old sentence promising there would never be one is gone with it.
    expect(screen.queryByText(/폴더 찾아보기는 제공하지 않습니다/)).not.toBeInTheDocument()

    rerender(
      <QueryClientProvider client={new QueryClient()}>
        <AddRootForm onDone={vi.fn()} onCancel={vi.fn()} canBrowse={true} />
      </QueryClientProvider>,
    )
    expect(screen.getByTestId('browse-folders')).toBeInTheDocument()
  })

  it('fills the path field from the picker instead of submitting', async () => {
    const user = userEvent.setup()
    const { onDone } = renderForm({ canBrowse: true })

    await user.click(screen.getByTestId('browse-folders'))
    await user.click(await screen.findByText(BROWSE_BASE))
    await user.click(await screen.findByTestId('folder-picker-choose-current'))

    await waitFor(() => {
      expect(screen.getByLabelText('루트 경로')).toHaveValue(BROWSE_BASE)
    })
    // The label is still to be typed, and a picker that added the root on click
    // would make the one irreversible-looking control here the one with no
    // confirm step. So: still on the form, nothing submitted.
    expect(onDone).not.toHaveBeenCalled()
    expect(screen.queryByTestId('folder-picker')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: '추가' })).toBeEnabled()
  })

  it('keeps the typed field usable — it reaches directories the picker cannot', async () => {
    const user = userEvent.setup()
    renderForm({ canBrowse: true })

    // Outside every `server.browse_bases`, which the picker refuses by design.
    await user.type(screen.getByLabelText('루트 경로'), '/srv/elsewhere')
    expect(screen.getByRole('button', { name: '추가' })).toBeEnabled()
  })
})
