import { isApiError } from '../../api/errors'

/**
 * A failed **per-root** `POST /api/scan {roots:[name]}`, as a sentence the user
 * can act on — the sibling of `rootErrors.ts`, for the other button on the same
 * row (ui-spec §8.6 §1, FR-IDX-004).
 *
 * **Why this file exists.** `RootsPanel` read `startScan.isPending` and nothing
 * else, so every one of arch §7.10's four refusals produced *no UI at all*: the
 * button un-greyed and the screen was identical to a success. The commonest of
 * them is the one a user reaches by pressing 재스캔 twice, which is the most
 * natural thing to do when the first press appears to have done nothing.
 *
 * **Why the `409` may name the cause, when `rootErrors.ts`'s may not.** §7.10
 * gives that status two conditions and no `detail.reason` to tell them apart —
 * "a scan is already running", and "every configured root has been removed" —
 * but the second is raised only on the `len(requested) == 0` branch of the
 * server's `scanRoots` (`internal/httpapi/scan.go`), i.e. only for a *whole
 * library* scan. This panel always names exactly one root, so a `409` reaching
 * it can only be the busy one, and saying so is a fact rather than a guess.
 * A caller that starts sending an empty body must revisit this comment.
 *
 * As in `rootErrors.ts`, the envelope's `message` is not used: §7.2 types it
 * English and this is a Korean interface.
 */
export function scanStartErrorMessage(error: unknown): string {
  if (!isApiError(error)) {
    // Transport failure or abort — nothing reached the server.
    return '서버에 연결하지 못했습니다. 잠시 후 다시 시도하세요.'
  }

  switch (error.status) {
    case 400:
      // §7.10: a name in `roots[]` is not in the configuration. Not reachable
      // from a name this panel took out of `GET /api/roots`, so it means the
      // list on screen and the server's configuration have diverged.
      return '서버가 모르는 루트입니다. 목록이 오래되었을 수 있으니 화면을 새로 고친 뒤 다시 시도하세요.'
    case 404:
      // §7.10 / amendment A-11 R1: the root *was* configured and this process
      // has since removed it. Deliberately a different sentence from the 400 —
      // the name was right, and only a restart brings it back.
      return '설정에서 제거된 루트입니다. 서버를 다시 시작해야 다시 스캔할 수 있습니다.'
    case 409:
      return '이미 스캔이 진행 중입니다. 지금 실행 중인 스캔이 끝난 뒤에 다시 시도하세요.'
    case 503:
      // Two conditions, one remedy that is not the user's: the server is
      // shutting down, or it was built without a scanner at all.
      return '서버가 지금 스캔을 실행할 수 없습니다. 서버 상태를 확인한 뒤 다시 시도하세요.'
    default:
      return '스캔을 시작하지 못했습니다. 잠시 후 다시 시도하세요.'
  }
}
