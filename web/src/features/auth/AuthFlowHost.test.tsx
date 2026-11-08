import '@testing-library/jest-dom/vitest'

import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { setupServer } from 'msw/node'
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'

import { getSettings } from '../../api/client'
import { authStatus, errorEnvelope, ORIGIN } from '../../api/fixtures'
import { queryKeys } from '../../api/queries'
import type { AuthStatus } from '../../api/types'
import { resetBasePath } from '../../api/urls'
import { failUnhandled } from '../../test/msw'
import { Dialog } from '../../components/ds/Dialog'
import { AuthFlowHost } from './AuthFlowHost'

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
beforeEach(() => {
  sessionStorage.clear()
})

const oidc: AuthStatus = {
  ...authStatus,
  authenticated: true,
  mode: 'oidc',
  user: { sub: 'u1', is_admin: true },
}

function fail(status: number, code: 'unauthorized' | 'forbidden', reason?: string) {
  server.use(
    http.get(`${ORIGIN}/api/settings`, () =>
      HttpResponse.json(errorEnvelope(code, 'x', reason === undefined ? undefined : { reason }), {
        status,
      }),
    ),
  )
}

function mount(status: AuthStatus) {
  const assign = vi.fn<(url: string) => void>()
  const reload = vi.fn<() => void>()
  const client = new QueryClient()
  client.setQueryData(queryKeys.auth.status, status)
  render(
    <QueryClientProvider client={client}>
      <AuthFlowHost assign={assign} reload={reload} returnTo={() => '/series/s1?page=3'} />
    </QueryClientProvider>,
  )
  return { assign, reload }
}

const hit = (): Promise<unknown> => getSettings().catch(() => undefined)

describe('AuthFlowHost', () => {
  it('stays reachable above an open dialog (the settings dialog)', async () => {
    fail(403, 'forbidden', 'reauth_required')
    const { assign } = mount(oidc)
    render(
      <Dialog open onClose={() => undefined} title="설정">
        <button type="button">재스캔</button>
      </Dialog>,
    )
    await hit()
    const button = await screen.findByRole('button', { name: '다시 로그인' })
    expect(button.closest('[inert]')).toBeNull()
    await userEvent.click(button)
    expect(assign).toHaveBeenCalledTimes(1)
  })

  it('oidc 401 → one top-level redirect even with several requests failing', async () => {
    fail(401, 'unauthorized', 'session_expired')
    const { assign } = mount(oidc)
    await Promise.all([hit(), hit(), hit()])
    await waitFor(() => {
      expect(assign).toHaveBeenCalledTimes(1)
    })
    expect(assign).toHaveBeenCalledWith('/api/auth/oidc/start?return=%2Fseries%2Fs1%3Fpage%3D3')
  })

  it('oidc 401 without a reason reloads the base path, once', async () => {
    fail(401, 'unauthorized')
    const { assign } = mount(oidc)
    await Promise.all([hit(), hit()])
    await waitFor(() => {
      expect(assign).toHaveBeenCalledTimes(1)
    })
    expect(assign).toHaveBeenCalledWith('/')
  })

  it('password-mode 401 does not navigate', async () => {
    fail(401, 'unauthorized')
    const { assign } = mount({ ...authStatus, authenticated: true, mode: 'password' })
    await hit()
    expect(assign).not.toHaveBeenCalled()
  })

  it('reauth_required asks first, then goes to start?reauth=1', async () => {
    fail(403, 'forbidden', 'reauth_required')
    const { assign } = mount(oidc)
    await hit()
    expect(await screen.findByText(/다시 로그인해야 합니다/)).toBeInTheDocument()
    expect(assign).not.toHaveBeenCalled()
    await userEvent.click(screen.getByRole('button', { name: '다시 로그인' }))
    expect(assign).toHaveBeenCalledWith(
      '/api/auth/oidc/start?reauth=1&return=%2Fseries%2Fs1%3Fpage%3D3',
    )
  })

  it('admin_only and cross_origin explain; not_allowed reloads', async () => {
    fail(403, 'forbidden', 'admin_only')
    const { reload } = mount(oidc)
    await hit()
    expect(await screen.findByText('관리자만 할 수 있는 작업입니다.')).toBeInTheDocument()
    fail(403, 'forbidden', 'cross_origin')
    await hit()
    expect(await screen.findByText(/요청이 거부되었습니다/)).toBeInTheDocument()
    expect(reload).not.toHaveBeenCalled()
    fail(403, 'forbidden', 'not_allowed')
    await hit()
    await waitFor(() => {
      expect(reload).toHaveBeenCalledTimes(1)
    })
  })
})
