import type { ApiError } from '../../api/errors'
import type { AuthStatus } from '../../api/types'
import { authOidcStartUrl } from '../../api/urls'

/**
 * The browser side of E-74's oidc session handling (§2.7, §2.9).
 *
 * `decideAuthAction` is pure: it turns one `401`/`403` into what the page should do,
 * so the branches are testable without a router or a DOM. `AuthFlowHost` carries it out.
 */

export type AuthNotice =
  | { kind: 'reauth' }
  | { kind: 'admin_only' }
  | { kind: 'cross_origin' }
  | { kind: 'loop' }

export type AuthAction =
  | { kind: 'none' }
  | { kind: 'redirect'; url: string }
  | { kind: 'reload' }
  | { kind: 'notice'; notice: AuthNotice }

/** Two sign-in redirects for one path inside this window mean the sign-in is not taking hold. */
export const LOOP_WINDOW_MS = 10_000

const LOOP_KEY = 'shelf.oidc-redirect'

interface LoopStore {
  get(): string | null
  set(value: string): void
}

/** `sessionStorage` is per tab and survives the navigation away; it may also throw. */
export const sessionLoopStore: LoopStore = {
  get() {
    try {
      return sessionStorage.getItem(LOOP_KEY)
    } catch {
      return null
    }
  },
  set(value) {
    try {
      sessionStorage.setItem(LOOP_KEY, value)
    } catch {
      /* without storage the guard cannot work; sign-in still does */
    }
  },
}

/**
 * Whether sending the browser to sign in from `returnTo` now would repeat a redirect
 * that was made less than `LOOP_WINDOW_MS` ago. Records the attempt when it does not.
 */
export function redirectWouldLoop(
  returnTo: string,
  now: number,
  store: LoopStore = sessionLoopStore,
): boolean {
  const raw = store.get()
  if (raw !== null) {
    try {
      const last = JSON.parse(raw) as { to?: unknown; at?: unknown }
      if (last.to === returnTo && typeof last.at === 'number' && now - last.at < LOOP_WINDOW_MS) {
        return true
      }
    } catch {
      /* a corrupt record is no record */
    }
  }
  store.set(JSON.stringify({ to: returnTo, at: now }))
  return false
}

export interface AuthContext {
  status: AuthStatus | undefined
  /** Path and query of the page, as the browser shows them (the base path included). */
  returnTo: string
  now: number
  loopStore?: LoopStore
}

export function decideAuthAction(error: ApiError, ctx: AuthContext): AuthAction {
  if (error.status === 401) {
    if (ctx.status?.mode !== 'oidc') return { kind: 'none' }
    if (redirectWouldLoop(ctx.returnTo, ctx.now, ctx.loopStore)) {
      return { kind: 'notice', notice: { kind: 'loop' } }
    }
    return { kind: 'redirect', url: authOidcStartUrl(ctx.returnTo) }
  }
  if (error.status !== 403) return { kind: 'none' }
  switch (error.reason) {
    case 'reauth_required':
      return { kind: 'notice', notice: { kind: 'reauth' } }
    case 'admin_only':
      return { kind: 'notice', notice: { kind: 'admin_only' } }
    case 'not_allowed':
      return { kind: 'reload' }
    case 'cross_origin':
      return { kind: 'notice', notice: { kind: 'cross_origin' } }
    default:
      return { kind: 'none' }
  }
}

export function reauthUrl(returnTo: string): string {
  return authOidcStartUrl(returnTo, true)
}

export function signInUrl(returnTo: string): string {
  return authOidcStartUrl(returnTo)
}

/** `sub` is an opaque id; the first characters are enough to tell accounts apart. */
export function shortSub(sub: string): string {
  return sub.length <= 8 ? sub : `${sub.slice(0, 8)}…`
}
