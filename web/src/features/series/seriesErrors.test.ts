import { describe, expect, it } from 'vitest'

import { ApiError } from '../../api/errors'
import { scanStartErrorMessage } from '../roots/scanErrors'
import { rescanErrorMessage, seriesErrorMessage } from './seriesErrors'

/**
 * The envelope `message` given to each error below is the English the real
 * server sends (`internal/httpapi/series.go`, `params.go`, `scan.go`), and none
 * of it may reach the user.
 */
describe('seriesErrorMessage', () => {
  it.each([
    [400, 'bad_request', 'sid must be 16 characters of [a-z2-7]'],
    [404, 'not_found', 'no series with id aaaaaaaaaaaaaaaa'],
  ] as const)('says the series is not in the library on a %i', (status, code, message) => {
    expect(seriesErrorMessage(new ApiError({ status, code, message }))).toBe(
      '라이브러리에 없는 시리즈입니다. 주소가 잘못되었거나, 폴더가 옮겨지거나 지워졌을 수 있습니다.',
    )
  })

  it('says the server cannot read the library on a 503', () => {
    const error = new ApiError({
      status: 503,
      code: 'unavailable',
      message: 'the catalogue is not available',
    })
    expect(seriesErrorMessage(error)).toBe(
      '서버가 지금 라이브러리를 읽을 수 없습니다. 서버 상태를 확인한 뒤 다시 시도하세요.',
    )
  })

  it('falls back to a plain failure for any other status', () => {
    const error = new ApiError({ status: 500, code: 'internal', message: 'internal error' })
    expect(seriesErrorMessage(error)).toBe('시리즈를 불러오지 못했습니다. 잠시 후 다시 시도하세요.')
  })

  it('reports a request that got no answer from the server as a connection failure', () => {
    expect(seriesErrorMessage(new TypeError('Failed to fetch'))).toBe(
      '서버에 연결하지 못했습니다. 잠시 후 다시 시도하세요.',
    )
  })
})

describe('rescanErrorMessage', () => {
  it.each([
    [404, 'not_found', 'no series with id aaaaaaaaaaaaaaaa', null],
    [400, 'bad_request', 'sid must be 16 characters of [a-z2-7]', { param: 'sid' }],
  ] as const)('says the series is gone on a %i about the sid, rather than naming a root', (status, code, message, detail) => {
    expect(rescanErrorMessage(new ApiError({ status, code, message, detail }))).toBe(
      '라이브러리에 없는 시리즈라 다시 스캔하지 못했습니다. 화면을 새로 고친 뒤 다시 시도하세요.',
    )
  })

  /**
   * `internal/httpapi/scan.go` `scanStartError`: a root removed from the file is
   * still in the index after a restart, so the series loads and its rescan names
   * a root the scanner no longer has. The series is not gone, and a reload would
   * not help.
   */
  it('says the series’ root is not configured on a 400 about roots', () => {
    const error = new ApiError({
      status: 400,
      code: 'bad_request',
      message: 'scanner: unknown root "manga"',
      detail: { param: 'roots' },
    })
    expect(rescanErrorMessage(error)).toBe(
      '이 시리즈의 루트가 설정에 없어 다시 스캔할 수 없습니다. 루트를 다시 추가해야 스캔할 수 있습니다.',
    )
  })

  it.each([
    [409, 'conflict', 'a scan is already running'],
    [503, 'unavailable', 'the server is shutting down'],
    [500, 'internal', 'internal error'],
  ] as const)("answers a %i in the scan panel's words", (status, code, message) => {
    const error = new ApiError({ status, code, message })
    expect(rescanErrorMessage(error)).toBe(scanStartErrorMessage(error))
    expect(rescanErrorMessage(error)).not.toContain(message)
  })
})
