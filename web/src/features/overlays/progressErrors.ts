import { isApiError } from '../../api/errors'

/**
 * The sentences of the 읽기 기록 section (ui-spec §8.6 item 4, ruling E-68).
 *
 * As in `series/seriesErrors.ts`, the envelope `message` is never used: it is
 * English, and the status alone picks the sentence (E-68 §2.7).
 *
 * The import is one transaction (`internal/userdata/export.go` `Import`), and
 * an auth refusal is answered before the handler runs, so **every** refusal the
 * server answered leaves the records as they were and says so. A request that
 * got no answer may have landed, so that sentence alone does not.
 */

const NO_ANSWER = '서버에 연결하지 못했습니다. 잠시 후 다시 시도하세요.'
const UNCHANGED = '기록은 바뀌지 않았습니다.'

/** The browser's own refusal of a file over the 32 MiB import body cap (arch §8). */
export const FILE_TOO_LARGE_MESSAGE =
  '파일이 너무 큽니다. 내보내기로 만든 진행률 파일인지 확인하세요.'

/** The browser's own refusal of a file that is not a JSON object with an `items` array. */
export const FILE_UNREADABLE_MESSAGE =
  '진행률 파일로 읽을 수 없습니다. 내보내기로 만든 파일인지 확인하세요.'

/** A failed `GET /api/progress/export`. */
export function exportErrorMessage(error: unknown): string {
  if (!isApiError(error)) return NO_ANSWER
  if (error.status === 503) {
    // `s.user == nil`: the server runs without its user database.
    return '서버가 지금 읽기 기록을 읽을 수 없습니다. 서버 상태를 확인한 뒤 다시 시도하세요.'
  }
  return '내보내지 못했습니다. 잠시 후 다시 시도하세요.'
}

/** A failed `POST /api/progress/import`. */
export function importErrorMessage(error: unknown): string {
  if (!isApiError(error)) return NO_ANSWER
  switch (error.status) {
    case 400:
      // `detail.id_version` is set on one refusal only: the file's ids were
      // derived under another scheme and would point at other books.
      if (error.detailString('id_version') !== null) {
        return `다른 버전의 SHELF에서 만든 파일이라 가져올 수 없습니다. ${UNCHANGED}`
      }
      // Bad JSON, an unknown field, a wrong type, an oversized body, an enum
      // outside §7.3 or a foreign `format` — the file is not one an export made.
      return `${FILE_UNREADABLE_MESSAGE} ${UNCHANGED}`
    case 503:
      return `서버가 지금 읽기 기록을 쓸 수 없습니다. ${UNCHANGED} 서버 상태를 확인한 뒤 다시 시도하세요.`
    default:
      return `가져오지 못했습니다. ${UNCHANGED} 잠시 후 다시 시도하세요.`
  }
}
