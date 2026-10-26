import { describe, expect, it } from 'vitest'

import { ApiError } from '../../api/errors'
import { loginErrorMessage } from './loginErrors'

/**
 * The sentences are the server-rendered form's (`internal/httpapi/loginpage.go`,
 * `loginMessage`), so that the two login surfaces say the same thing about the same
 * failure. The envelope `message` given to each error below is the English the real
 * server sends, and none of it may reach the user.
 */
describe('loginErrorMessage', () => {
  it('says the password is wrong on a 401, whatever the envelope says', () => {
    const error = new ApiError({
      status: 401,
      code: 'unauthorized',
      message: 'authentication required',
    })
    expect(loginErrorMessage(error)).toBe('비밀번호가 올바르지 않습니다.')
  })

  it('says there were too many attempts on a 429', () => {
    const error = new ApiError({
      status: 429,
      code: 'rate_limited',
      message: 'too many login attempts',
      retryAfterMs: 60_000,
    })
    expect(loginErrorMessage(error)).toBe('로그인 시도가 너무 많습니다. 잠시 후 다시 시도하세요.')
  })

  it('falls back to a plain failure for any other status', () => {
    const error = new ApiError({ status: 500, code: 'internal', message: 'internal error' })
    expect(loginErrorMessage(error)).toBe('로그인하지 못했습니다.')
  })

  it('reports a request that got no answer from the server (transport failure or abort) as a connection failure', () => {
    expect(loginErrorMessage(new TypeError('Failed to fetch'))).toBe(
      '서버에 연결하지 못했습니다. 잠시 후 다시 시도하세요.',
    )
    expect(loginErrorMessage(new DOMException('The operation was aborted.', 'AbortError'))).toBe(
      '서버에 연결하지 못했습니다. 잠시 후 다시 시도하세요.',
    )
  })
})
