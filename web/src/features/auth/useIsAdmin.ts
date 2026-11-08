import { useCachedAuthStatus } from '../../api/queries'

/**
 * Whether admin-only controls (scan, rescan, cache purge, root editing, scan log) are
 * shown. Only an oidc session of a non-admin hides them (E-74 §2.10); no auth and
 * password mode have one kind of user, and an unanswered status hides nothing — the
 * server refuses the call either way, so this only keeps a button from promising it.
 */
export function useIsAdmin(): boolean {
  const status = useCachedAuthStatus()
  if (status?.mode !== 'oidc') return true
  return status.user?.is_admin === true
}
