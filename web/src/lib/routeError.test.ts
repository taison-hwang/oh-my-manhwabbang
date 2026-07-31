import { describe, expect, it } from 'vitest'

import { routeErrorMessage } from './routeError'

describe('routeErrorMessage (the router errorElement, ui-spec §3)', () => {
  it('names an unmatched URL instead of printing [object Object]', () => {
    // What React Router really throws for a 404: an `ErrorResponse`, which is
    // duck-typed and is *not* an `Error`, so `String(it)` is "[object Object]".
    expect(
      routeErrorMessage({
        status: 404,
        statusText: 'Not Found',
        internal: true,
        data: 'Error: No route matches URL "/nope"',
      }),
    ).toBe('404 Not Found — Error: No route matches URL "/nope"')
  })

  it('falls back to the status line when the response carries no data', () => {
    expect(routeErrorMessage({ status: 500, statusText: 'Bad', internal: true, data: '' })).toBe(
      '500 Bad',
    )
    expect(routeErrorMessage({ status: 503, statusText: '', internal: true, data: null })).toBe(
      '503',
    )
  })

  it('still prefers a thrown Error message', () => {
    expect(routeErrorMessage(new Error('열 수 없습니다'))).toBe('열 수 없습니다')
    expect(routeErrorMessage('열 수 없습니다')).toBe('열 수 없습니다')
  })

  it('never renders an empty paragraph for an unrecognised throw', () => {
    expect(routeErrorMessage({ nope: 1 })).toBe('알 수 없는 오류')
    expect(routeErrorMessage(null)).toBe('알 수 없는 오류')
    expect(routeErrorMessage('   ')).toBe('알 수 없는 오류')
  })
})
