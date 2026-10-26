import { isApiError } from '../../api/errors'

/**
 * A failed `GET /api/books/{bid}`, as the sentence under the viewer's error
 * state (arch §7.6).
 *
 * The viewer used to print the rejection's `message` — for an `ApiError` the
 * §7.2 envelope's English. As in `series/seriesErrors.ts`, the envelope
 * `message` is not used; the status picks the sentence. A book the index has
 * but cannot open is not this: that is a `200` with `pages: []` and its own
 * `error`, rendered as 열 수 없는 파일.
 *
 * Never returns an empty string: the band would read as a blank screen.
 */
export function bookErrorMessage(error: unknown): string {
  if (!isApiError(error)) {
    // Transport failure or abort — nothing reached the server.
    return '서버에 연결하지 못했습니다. 잠시 후 다시 시도하세요.'
  }

  switch (error.status) {
    case 400:
    case 404:
      // `400`: the id in the address is not one the server could have issued.
      // `404`: it was, and the book has left the index since — a scan found
      // its file moved or gone.
      return '라이브러리에 없는 책입니다. 주소가 잘못되었거나, 파일이 옮겨지거나 지워졌을 수 있습니다.'
    case 503:
      // The server has no index open to read from.
      return '서버가 지금 라이브러리를 읽을 수 없습니다. 서버 상태를 확인한 뒤 다시 시도하세요.'
    default:
      return '책을 불러오지 못했습니다. 잠시 후 다시 시도하세요.'
  }
}
