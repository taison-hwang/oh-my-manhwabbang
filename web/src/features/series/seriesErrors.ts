import { isApiError } from '../../api/errors'
import { scanStartErrorMessage } from '../roots/scanErrors'

/**
 * A failed `GET /api/series/{sid}`, as the sentence under the series screen's
 * error state (arch §7.5).
 *
 * **Why this file exists.** The screen printed the rejection's `message`, which
 * for an `ApiError` is the §7.2 envelope's: English (`no series with id …`), with
 * an id in it the user never typed, on a Korean interface. As in
 * `auth/loginErrors.ts` and `roots/rootErrors.ts`, the envelope `message` is not
 * used; the status picks the sentence.
 *
 * Never returns an empty string: the band would read as a blank screen.
 */
export function seriesErrorMessage(error: unknown): string {
  if (!isApiError(error)) {
    // Transport failure or abort — nothing reached the server.
    return '서버에 연결하지 못했습니다. 잠시 후 다시 시도하세요.'
  }

  switch (error.status) {
    case 400:
    case 404:
      // `400`: the id in the address is not one the server could have issued.
      // `404`: it was, and the series has left the index since — a scan found
      // its folder moved or gone. Either way the way back is the library.
      return '라이브러리에 없는 시리즈입니다. 주소가 잘못되었거나, 폴더가 옮겨지거나 지워졌을 수 있습니다.'
    case 503:
      // The server has no index open to read from.
      return '서버가 지금 라이브러리를 읽을 수 없습니다. 서버 상태를 확인한 뒤 다시 시도하세요.'
    default:
      return '시리즈를 불러오지 못했습니다. 잠시 후 다시 시도하세요.'
  }
}

/**
 * A failed `POST /api/series/{sid}/rescan`, as the header's notice (arch §7.5).
 *
 * The run it asks for is `POST /api/scan`'s with one root named, so a `409`
 * here is the same busy scanner and the `503` the same absent one, and the
 * sentences are `scanErrors.ts`'s. Two refusals are this screen's own.
 */
export function rescanErrorMessage(error: unknown): string {
  if (!isApiError(error)) return scanStartErrorMessage(error)
  if (error.status === 400 && error.detailString('param') === 'roots') {
    // `scanner.ErrUnknownRoot`: the series is in the index, but its root has
    // left the configuration (removed from the file, then a restart — the index
    // keeps the rows). Reloading changes nothing; only the root coming back does.
    return '이 시리즈의 루트가 설정에 없어 다시 스캔할 수 없습니다. 루트를 다시 추가해야 스캔할 수 있습니다.'
  }
  if (error.status === 400 || error.status === 404) {
    // The `sid` itself: malformed (`400 {param: "sid"}`) or gone from the index.
    return '라이브러리에 없는 시리즈라 다시 스캔하지 못했습니다. 화면을 새로 고친 뒤 다시 시도하세요.'
  }
  return scanStartErrorMessage(error)
}
