import { isApiError } from '../../api/errors'

/**
 * A failed `POST /api/auth/login`, as a sentence the user can act on (arch §7.12,
 * ruling E-17).
 *
 * **Why this file exists.** The screen used to print the rejection's `message`, which
 * for an `ApiError` is the §7.2 envelope's. §7.2 types that "English, human-readable,
 * safe to display", and this is a Korean interface — and the server's answer to a wrong
 * password is `401 {code: "unauthorized", message: "authentication required"}`, the
 * same envelope every gated endpoint sends, so it was not even the right English for
 * the situation. As in `roots/rootErrors.ts`, the envelope `message` is not used.
 *
 * **Why these sentences.** They are the server-rendered form's, word for word
 * (`internal/httpapi/loginpage.go`, `loginMessage`). That form is cold entry and this
 * screen is re-authentication, but it is the same password and the same failures, and
 * the two surfaces should not describe them differently.
 *
 * **Why the `401` is keyed on the status alone.** Arch §8.2 requires "no password sent"
 * and "incorrect password" to be indistinguishable, and the server form switches on the
 * status for the same reason. Nothing here may start reading `code` or `detail` to tell
 * them apart.
 *
 * Never returns an empty string: an alert with nothing in it is worse than no alert.
 */
export function loginErrorMessage(error: unknown): string {
  if (!isApiError(error)) {
    // No answer came back from the server: a transport failure, an abort, a dropped
    // connection. The request may still have reached it.
    return '서버에 연결하지 못했습니다. 잠시 후 다시 시도하세요.'
  }

  switch (error.status) {
    case 401:
      return '비밀번호가 올바르지 않습니다.'
    case 429:
      // Arch §8.2's limiter (5/min per IP). `Retry-After` is not echoed: the server
      // form does not name a wait either.
      return '로그인 시도가 너무 많습니다. 잠시 후 다시 시도하세요.'
    default:
      return '로그인하지 못했습니다.'
  }
}
