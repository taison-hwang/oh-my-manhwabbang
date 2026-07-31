import { createContext, useContext } from 'react'

/**
 * The seam between the app shell and the server.
 *
 * `App`, `Sidebar` and `TopBar` need four pieces of server state — the auth
 * gate, the root list, the smart-list counts and the scan snapshot — but WP-05
 * may not call the API: `src/api/*` is WP-06's and `fetch` outside it is an
 * ESLint error. So the shell declares the *narrow* shape it consumes (the same
 * "interfaces are declared by the consumer" rule the Go side follows, see
 * impl-plan §5.1) and a wave-2 provider supplies it from
 * `useAuthStatus` / `useRoots` / `useScanStatus` / `useSeriesList`.
 *
 * The field names are the wire's, not camelCase, exactly so that the DTOs from
 * `src/api/types.ts` satisfy these interfaces structurally with no adapter.
 *
 * The default value renders a working, empty shell: no auth, no roots loaded
 * yet, nothing scanning. That is what lets the routing, theming and responsive
 * layers be exercised today, before the provider exists.
 */

/** A subset of arch §7.4's `Root`. */
export interface ShellRoot {
  name: string
  label: string
  series_count: number
  available: boolean
  /**
   * Amendment **A-11** / revision **R2** (ruling E-26). `true` for a root that
   * `POST /api/roots` wrote to the configuration file and the running server has
   * not opened — roots are opened once, at startup.
   *
   * The shell needs it, not only the settings panel. `GET /api/roots` lists a
   * pending root alongside the loaded ones, so without this field the sidebar
   * grows an ordinary selectable row the moment 루트 추가 succeeds — one whose
   * `series_count` is §7.3's fixed zero and which therefore scopes the library
   * to nothing, silently, while the settings panel three inches away correctly
   * says 재시작 후 적용. That is the phantom row E-26 catalogues, relocated.
   */
  pending: boolean
}

/** A subset of arch §7.10's `ScanStatus`. */
export interface ShellScan {
  state: 'idle' | 'walking' | 'indexing' | 'covers' | 'cancelling'
  done: number
  total: number
  finished_at: number | null
}

/** arch §7.12's `/api/auth/status` payload. */
export interface ShellAuth {
  auth_required: boolean
  authenticated: boolean
}

/** Counts behind the sidebar's three smart lists (A-4 `progress=` queries). */
export interface ShellListCounts {
  reading: number
  added: number
  done: number
}

export interface ShellData {
  auth: ShellAuth
  /** Submits the password. Rejects with a message the login screen shows. */
  login: (password: string) => Promise<void>
  loginPending: boolean

  roots: ShellRoot[]
  /**
   * False until `/api/roots` has answered.
   *
   * The onboarding screen (ui-spec §4.6) replaces the entire shell when there
   * are no roots, so "not loaded yet" and "genuinely empty" must be
   * distinguishable — otherwise every cold start flashes onboarding.
   */
  rootsLoaded: boolean

  counts: ShellListCounts
  scan: ShellScan
}

export const DEFAULT_SHELL_DATA: ShellData = {
  auth: { auth_required: false, authenticated: true },
  login: () => Promise.reject(new Error('no auth provider mounted')),
  loginPending: false,
  roots: [],
  rootsLoaded: false,
  counts: { reading: 0, added: 0, done: 0 },
  scan: { state: 'idle', done: 0, total: 0, finished_at: null },
}

export const ShellDataContext = createContext<ShellData>(DEFAULT_SHELL_DATA)

export function useShellData(): ShellData {
  return useContext(ShellDataContext)
}
