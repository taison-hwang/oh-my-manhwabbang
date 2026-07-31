import { useRouteError } from 'react-router-dom'

import { routeErrorMessage } from '../../lib/routeError'

/**
 * The router's `errorElement`.
 *
 * This file began as WP-05's scaffolding: four components, three of which stood
 * in for the wave-2 screens. Those three are gone — `router.tsx` now mounts
 * `features/library`, `features/series` and `features/viewer` directly — and
 * what is left was never scaffolding. A data router without an `errorElement`
 * replaces the whole app with React Router's default error page, taking the
 * shell, the theme and the Korean copy with it.
 *
 * The file keeps its name rather than becoming `RouteErrorScreen.tsx`:
 * `lib/routeError.ts` and `router.tsx` both refer to it by this path, and the
 * rename is churn in three files to save one line of explanation.
 */
export function RouteErrorScreen() {
  const error = useRouteError()
  const message = routeErrorMessage(error)
  return (
    <div className="flex h-full flex-col items-start gap-3 p-8">
      <h3>화면을 열 수 없습니다</h3>
      <p className="text-sm text-ink-muted">{message}</p>
    </div>
  )
}
