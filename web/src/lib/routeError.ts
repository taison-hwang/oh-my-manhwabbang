import { isRouteErrorResponse } from 'react-router-dom'

/**
 * The human-readable message behind the router's `errorElement`.
 *
 * The most common error the shell has to render is an unmatched URL, and that
 * one is **not** an `Error`: React Router throws an `ErrorResponse`
 * (`{status, statusText, data, internal}`), whose `String()` is
 * `[object Object]` — which told the user nothing at all. Read the shape.
 *
 * It lives in `lib/` rather than next to the error screen because
 * `RouteScaffold.tsx` is deleted in wave 2 while the error screen stays.
 */
export function routeErrorMessage(error: unknown): string {
  if (isRouteErrorResponse(error)) {
    const status = `${error.status.toString()} ${error.statusText}`.trim()
    return typeof error.data === 'string' && error.data.trim() !== ''
      ? `${status} — ${error.data}`
      : status
  }
  if (error instanceof Error) return error.message
  if (typeof error === 'string' && error.trim() !== '') return error
  return '알 수 없는 오류'
}
