import { describe, expect, it } from 'vitest'

import { ApiError } from '../../api/errors'
import { bookErrorMessage } from './bookErrors'

/**
 * The envelope `message` given to each error below is the English the real
 * server sends (`internal/httpapi/books.go`, `params.go`), and none of it may
 * reach the user.
 */
describe('bookErrorMessage', () => {
  it.each([
    [400, 'bad_request', 'bid must be 16 characters of [a-z2-7]'],
    [404, 'not_found', 'no book with id aaaaaaaaaaaaaaaa'],
  ] as const)('says the book is not in the library on a %i', (status, code, message) => {
    expect(bookErrorMessage(new ApiError({ status, code, message }))).toBe(
      '라이브러리에 없는 책입니다. 주소가 잘못되었거나, 파일이 옮겨지거나 지워졌을 수 있습니다.',
    )
  })

  it('says the server cannot read the library on a 503', () => {
    const error = new ApiError({
      status: 503,
      code: 'unavailable',
      message: 'the catalogue is not available',
    })
    expect(bookErrorMessage(error)).toBe(
      '서버가 지금 라이브러리를 읽을 수 없습니다. 서버 상태를 확인한 뒤 다시 시도하세요.',
    )
  })

  it('falls back to a plain failure for any other status', () => {
    const error = new ApiError({ status: 500, code: 'internal', message: 'internal error' })
    expect(bookErrorMessage(error)).toBe('책을 불러오지 못했습니다. 잠시 후 다시 시도하세요.')
  })

  it('reports a request that got no answer from the server as a connection failure', () => {
    expect(bookErrorMessage(new TypeError('Failed to fetch'))).toBe(
      '서버에 연결하지 못했습니다. 잠시 후 다시 시도하세요.',
    )
  })
})
