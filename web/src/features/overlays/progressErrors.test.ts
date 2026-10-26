import { describe, expect, it } from 'vitest'

import { ApiError } from '../../api/errors'
import { exportErrorMessage, importErrorMessage } from './progressErrors'

/**
 * The failure table of ui-spec §8.6 item 4 (ruling E-68 §2.7), row by row.
 *
 * Every envelope `message` below is the English the real server sends
 * (`internal/httpapi/progress.go`, `params.go`), and none of it may reach the
 * user: the status picks the sentence. The import's refusals all end in
 * 기록은 바뀌지 않았습니다 because the import is one transaction; a request that
 * got no answer does not, since it may have landed.
 */

const UNCHANGED = '기록은 바뀌지 않았습니다.'
const NO_ANSWER = '서버에 연결하지 못했습니다. 잠시 후 다시 시도하세요.'

describe('exportErrorMessage', () => {
  it('says the server cannot read the reading history on a 503', () => {
    const error = new ApiError({
      status: 503,
      code: 'unavailable',
      message: 'user data is not available',
    })
    expect(exportErrorMessage(error)).toBe(
      '서버가 지금 읽기 기록을 읽을 수 없습니다. 서버 상태를 확인한 뒤 다시 시도하세요.',
    )
  })

  it.each([
    [500, 'internal', 'internal error'],
    [401, 'unauthorized', 'authentication required'],
    // The table has no 400 row for 내보내기 — the endpoint takes no input — so
    // one would be "any other status".
    [400, 'bad_request', 'request body is not valid JSON'],
  ] as const)('falls back to a plain failure on a %i', (status, code, message) => {
    expect(exportErrorMessage(new ApiError({ status, code, message }))).toBe(
      '내보내지 못했습니다. 잠시 후 다시 시도하세요.',
    )
  })

  it('reports a request that got no answer as a connection failure', () => {
    expect(exportErrorMessage(new TypeError('Failed to fetch'))).toBe(NO_ANSWER)
  })

  it('never shows the envelope message', () => {
    for (const status of [400, 401, 404, 500, 503]) {
      const error = new ApiError({ status, code: 'internal', message: 'ENVELOPE-MESSAGE' })
      expect(exportErrorMessage(error)).not.toContain('ENVELOPE-MESSAGE')
    }
  })
})

describe('importErrorMessage', () => {
  it('says the file comes from another SHELF on a 400 that names id_version', () => {
    const error = new ApiError({
      status: 400,
      code: 'bad_request',
      message: 'the document uses a different identifier scheme',
      detail: { id_version: 'shelf-id/2' },
    })
    expect(importErrorMessage(error)).toBe(
      `다른 버전의 SHELF에서 만든 파일이라 가져올 수 없습니다. ${UNCHANGED}`,
    )
  })

  it.each([
    ['unknown field', { field: 'note' }, 'unknown field "note"'],
    ['wrong type', { field: 'items' }, 'field "items" must be of type []userdata.ExportItem'],
    ['bad JSON', null, 'request body is not valid JSON'],
    ['oversized body', null, 'request body exceeds 33554432 bytes'],
    ['bad enum or format', null, 'userdata: export format "x", want "shelf-progress/1"'],
  ] as const)('says the file cannot be read on any other 400 (%s)', (_name, detail, message) => {
    const error = new ApiError({ status: 400, code: 'bad_request', message, detail })
    expect(importErrorMessage(error)).toBe(
      `진행률 파일로 읽을 수 없습니다. 내보내기로 만든 파일인지 확인하세요. ${UNCHANGED}`,
    )
  })

  it('says the server cannot write the reading history on a 503', () => {
    const error = new ApiError({
      status: 503,
      code: 'unavailable',
      message: 'user data is not available',
    })
    expect(importErrorMessage(error)).toBe(
      `서버가 지금 읽기 기록을 쓸 수 없습니다. ${UNCHANGED} 서버 상태를 확인한 뒤 다시 시도하세요.`,
    )
  })

  it.each([
    [500, 'internal', 'internal error'],
    [401, 'unauthorized', 'authentication required'],
    [404, 'not_found', 'not found'],
  ] as const)('falls back to a plain failure on a %i', (status, code, message) => {
    expect(importErrorMessage(new ApiError({ status, code, message }))).toBe(
      `가져오지 못했습니다. ${UNCHANGED} 잠시 후 다시 시도하세요.`,
    )
  })

  it('does not say the records are unchanged when the request got no answer', () => {
    const sentence = importErrorMessage(new TypeError('Failed to fetch'))
    expect(sentence).toBe(NO_ANSWER)
    expect(sentence).not.toContain(UNCHANGED)
  })

  it('ends every refusal the server answered with the unchanged clause', () => {
    for (const status of [400, 401, 403, 404, 409, 413, 500, 503]) {
      const error = new ApiError({ status, code: 'internal', message: 'ENVELOPE-MESSAGE' })
      const sentence = importErrorMessage(error)
      expect(sentence).toContain(UNCHANGED)
      expect(sentence).not.toContain('ENVELOPE-MESSAGE')
    }
  })
})
