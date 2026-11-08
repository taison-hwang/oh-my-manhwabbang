import '@testing-library/jest-dom/vitest'

import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
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

function mount(user: { sub: string; is_admin: boolean }, paths: 'blank' | 'full') {
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
  const status: AuthStatus = {
    ...authStatus,
    authenticated: true,
    mode: 'oidc',
    end_session_available: true,
    user,
  }
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  client.setQueryData(queryKeys.auth.status, status)
  render(
    <QueryClientProvider client={client}>
      <SettingsDialog open onClose={() => undefined} />
    </QueryClientProvider>,
  )
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
