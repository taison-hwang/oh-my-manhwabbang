import { afterEach, describe, expect, it } from 'vitest'

import { ApiError } from '../../api/errors'
import type { AuthStatus } from '../../api/types'
import { resetBasePath, setBasePath } from '../../api/urls'
import { decideAuthAction, redirectWouldLoop, shortSub } from './authFlow'

afterEach(() => {
  resetBasePath()
})

function memoryStore() {
  let value: string | null = null
  return {
    get: () => value,
    set: (v: string) => {
      value = v
    },
  }
}

const oidc: AuthStatus = {
  auth_required: true,
  authenticated: true,
  mode: 'oidc',
  end_session_available: true,
  user: { sub: 'abcdef0123456789', is_admin: false },
}
const password: AuthStatus = {
  auth_required: true,
  authenticated: true,
  mode: 'password',
  end_session_available: false,
}

function err(status: number, reason?: string): ApiError {
  return new ApiError({
    status,
    code: status === 401 ? 'unauthorized' : 'forbidden',
    message: '',
    detail: reason === undefined ? null : { reason },
  })
}

describe('401', () => {
  it('oidc: top-level redirect to start with the encoded current path', () => {
    const action = decideAuthAction(err(401, 'session_expired'), {
      status: oidc,
      returnTo: '/series/a b?page=2&x=1',
      now: 1000,
      loopStore: memoryStore(),
    })
    expect(action).toEqual({
      kind: 'redirect',
      url: '/api/auth/oidc/start?return=%2Fseries%2Fa+b%3Fpage%3D2%26x%3D1',
    })
  })

  it('keeps the base path in the start URL', () => {
    setBasePath('/reader')
    const action = decideAuthAction(err(401), {
      status: oidc,
      returnTo: '/reader/',
      now: 1,
      loopStore: memoryStore(),
    })
    expect(action).toEqual({ kind: 'redirect', url: '/reader/api/auth/oidc/start?return=%2Freader%2F' })
  })

  it('password mode and an unknown status keep the old behaviour', () => {
    for (const status of [password, undefined]) {
      expect(
        decideAuthAction(err(401), { status, returnTo: '/', now: 1, loopStore: memoryStore() }),
      ).toEqual({ kind: 'none' })
    }
  })

  it('stops a repeat redirect to the same path inside the window', () => {
    const store = memoryStore()
    const ctx = { status: oidc, returnTo: '/series/x', loopStore: store }
    expect(decideAuthAction(err(401), { ...ctx, now: 1000 }).kind).toBe('redirect')
    expect(decideAuthAction(err(401), { ...ctx, now: 5000 })).toEqual({
      kind: 'notice',
      notice: { kind: 'loop' },
    })
    expect(decideAuthAction(err(401), { ...ctx, now: 20_000 }).kind).toBe('redirect')
  })
})

describe('redirectWouldLoop', () => {
  it('a different path is not a loop; a corrupt record is no record', () => {
    const store = memoryStore()
    expect(redirectWouldLoop('/a', 0, store)).toBe(false)
    expect(redirectWouldLoop('/b', 1, store)).toBe(false)
    store.set('not json')
    expect(redirectWouldLoop('/b', 2, store)).toBe(false)
  })
})

describe('403 reasons', () => {
  const ctx = { status: oidc, returnTo: '/', now: 1, loopStore: memoryStore() }
  it('reauth_required → reauth notice', () => {
    expect(decideAuthAction(err(403, 'reauth_required'), ctx)).toEqual({
      kind: 'notice',
      notice: { kind: 'reauth' },
    })
  })
  it('admin_only → admin notice', () => {
    expect(decideAuthAction(err(403, 'admin_only'), ctx)).toEqual({
      kind: 'notice',
      notice: { kind: 'admin_only' },
    })
  })
  it('not_allowed → reload', () => {
    expect(decideAuthAction(err(403, 'not_allowed'), ctx)).toEqual({ kind: 'reload' })
  })
  it('cross_origin → notice', () => {
    expect(decideAuthAction(err(403, 'cross_origin'), ctx)).toEqual({
      kind: 'notice',
      notice: { kind: 'cross_origin' },
    })
  })
  it('other 403 reasons (root editing off, …) are left to their screens', () => {
    expect(decideAuthAction(err(403, 'disabled'), ctx)).toEqual({ kind: 'none' })
    expect(decideAuthAction(err(403), ctx)).toEqual({ kind: 'none' })
  })
})

it('shortSub keeps short ids whole', () => {
  expect(shortSub('abc')).toBe('abc')
  expect(shortSub('abcdef0123456789')).toBe('abcdef01…')
})
