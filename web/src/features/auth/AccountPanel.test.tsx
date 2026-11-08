import '@testing-library/jest-dom/vitest'

import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'

import { authStatus } from '../../api/fixtures'
import { queryKeys } from '../../api/queries'
import type { AuthStatus } from '../../api/types'
import { resetBasePath, setBasePath } from '../../api/urls'
import { AccountPanel } from './AccountPanel'

afterEach(() => {
  resetBasePath()
})

const oidc: AuthStatus = {
  ...authStatus,
  authenticated: true,
  mode: 'oidc',
  end_session_available: true,
  user: { sub: 'abcdef0123456789', is_admin: false },
}

function mount(status: AuthStatus) {
  const client = new QueryClient()
  client.setQueryData(queryKeys.auth.status, status)
  render(
    <QueryClientProvider client={client}>
      <AccountPanel />
    </QueryClientProvider>,
  )
}

function formOf(name: string): HTMLFormElement {
  const form = screen.getByRole('button', { name }).closest('form')
  if (form === null) throw new Error('no form')
  return form
}

describe('AccountPanel', () => {
  it('two logout forms: top-level urlencoded POSTs', () => {
    setBasePath('/reader')
    mount(oidc)
    const one = formOf('로그아웃')
    expect(one.getAttribute('action')).toBe('/reader/api/auth/logout')
    expect(one.method).toBe('post')
    expect(one.enctype).toBe('application/x-www-form-urlencoded')
    const two = formOf('모든 서비스에서 로그아웃')
    expect(two.getAttribute('action')).toBe('/reader/api/auth/oidc/end-session')
    expect(two.method).toBe('post')
    expect(two.enctype).toBe('application/x-www-form-urlencoded')
    expect(screen.getByText('abcdef01…')).toBeInTheDocument()
  })

  it('no second button without end_session_available', () => {
    mount({ ...oidc, end_session_available: false })
    expect(screen.getByRole('button', { name: '로그아웃' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '모든 서비스에서 로그아웃' })).toBeNull()
  })

  it('renders nothing outside oidc mode', () => {
    mount({ auth_required: true, authenticated: true, mode: 'password', end_session_available: false })
    expect(screen.queryByTestId('account-panel')).toBeNull()
  })
})
