import '@testing-library/jest-dom/vitest'

import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { setupServer } from 'msw/node'
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'

import {
  authStatus,
  cacheUsage,
  ORIGIN,
  root,
  scanStatusIdle,
  settings,
} from '../../api/fixtures'
import { queryKeys } from '../../api/queries'
import type { AuthStatus, Settings } from '../../api/types'
import { resetBasePath } from '../../api/urls'
import { useUiStore } from '../../store/ui'
import { failUnhandled } from '../../test/msw'
import { SettingsDialog } from './SettingsDialog'

const server = setupServer()
beforeAll(() => {
  server.listen({ onUnhandledRequest: failUnhandled })
})
afterEach(() => {
  server.resetHandlers()
  resetBasePath()
})
afterAll(() => {
  server.close()
})

const wire = (s: Settings): Settings => ({
  ...s,
  server: { ...s.server, root_editing_enabled: true, auth_mode: 'oidc' },
})

beforeEach(() => {
  localStorage.clear()
  useUiStore.setState({ theme: 'system', overlays: [] })
})

function mount(user: { sub: string; is_admin: boolean } | null, paths: 'blank' | 'full'): QueryClient {
  const blank = paths === 'blank'
  server.use(
    http.get(`${ORIGIN}/api/roots`, () =>
      HttpResponse.json({
        items: [
          { ...root, path: blank ? '' : root.path, last_scan_error: blank ? '' : null },
        ],
      }),
    ),
    http.get(`${ORIGIN}/api/settings`, () =>
      HttpResponse.json(
        wire({
          ...settings,
          server: { ...settings.server, config_path: blank ? '' : settings.server.config_path },
        }),
      ),
    ),
    http.get(`${ORIGIN}/api/cache/usage`, () =>
      HttpResponse.json({ ...cacheUsage, cache_dir: blank ? '' : cacheUsage.cache_dir }),
    ),
    http.get(`${ORIGIN}/api/scan/status`, () =>
      HttpResponse.json({ ...scanStatusIdle, last_error: blank ? null : 'boom' }),
    ),
    http.get(`${ORIGIN}/api/scan/log`, () => HttpResponse.json({ items: [] })),
  )
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  if (user !== null) {
    const status: AuthStatus = {
      ...authStatus,
      authenticated: true,
      mode: 'oidc',
      end_session_available: true,
      user,
    }
    client.setQueryData(queryKeys.auth.status, status)
  }
  render(
    <QueryClientProvider client={client}>
      <SettingsDialog open onClose={() => undefined} />
    </QueryClientProvider>,
  )
  return client
}

const nonAdmin: AuthStatus = {
  ...authStatus,
  authenticated: true,
  mode: 'oidc',
  end_session_available: false,
  user: { sub: 'late', is_admin: false },
}

describe('admin-only controls in oidc mode', () => {
  it('non-admin: no rescan, add, remove, cache purge or scan log', async () => {
    mount({ sub: 'someone-else', is_admin: false }, 'blank')
    await screen.findByText('만화')
    expect(screen.queryByRole('button', { name: /재스캔/ })).toBeNull()
    expect(screen.queryByRole('button', { name: /루트 추가/ })).toBeNull()
    expect(screen.queryByRole('button', { name: /제거/ })).toBeNull()
    expect(screen.queryByRole('button', { name: '전체 삭제' })).toBeNull()
    expect(screen.queryByRole('heading', { level: 3, name: '스캔 로그' })).toBeNull()
    expect(screen.getByRole('button', { name: '로그아웃' })).toBeInTheDocument()
  })

  it('admin: every control is there', async () => {
    mount({ sub: 'boss', is_admin: true }, 'full')
    await screen.findByText('만화')
    expect(screen.getByRole('button', { name: /재스캔/ })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /루트 추가/ })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /제거/ })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '전체 삭제' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { level: 3, name: '스캔 로그' })).toBeInTheDocument()
  })

  it('blank path fields render without empty path or error lines', async () => {
    mount({ sub: 'someone-else', is_admin: false }, 'blank')
    const label = await screen.findByText('만화')
    const row = label.closest('[data-testid^="root-row-"]')
    expect(row).not.toBeNull()
    expect(row?.querySelector('[title]')).toBeNull()
    expect(screen.queryByTestId('config-path')).toBeNull()
  })
})

describe('admin rights withdrawn while a confirmation is open (auth status arrives late)', () => {
  it('closes the open 루트 제거 confirmation', async () => {
    const client = mount(null, 'full')
    await userEvent.click(await screen.findByRole('button', { name: '제거' }))
    expect(screen.getByText(/루트를 제거할까요/)).toBeInTheDocument()
    act(() => {
      client.setQueryData(queryKeys.auth.status, nonAdmin)
    })
    await screen.findByTestId('account-panel')
    expect(screen.queryByText(/루트를 제거할까요/)).toBeNull()
    expect(screen.queryByRole('button', { name: '제거' })).toBeNull()
  })

  it('closes the open 캐시 전체 삭제 confirmation', async () => {
    const client = mount(null, 'full')
    await userEvent.click(await screen.findByRole('button', { name: '전체 삭제' }))
    expect(screen.getByText('캐시를 모두 삭제할까요?')).toBeInTheDocument()
    act(() => {
      client.setQueryData(queryKeys.auth.status, nonAdmin)
    })
    await screen.findByTestId('account-panel')
    expect(screen.queryByText('캐시를 모두 삭제할까요?')).toBeNull()
    expect(screen.queryByRole('button', { name: '전체 삭제' })).toBeNull()
  })
})
