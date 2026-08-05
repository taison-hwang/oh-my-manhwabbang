/* eslint-disable react-refresh/only-export-components --
 * The rule wants a module to export components *or* values, never both, so that
 * Vite can hot-replace it. This module's export is the router itself: editing it
 * has to remount the tree whatever the components in it are called, and the two
 * declared here (`ShellDataProvider`, `RootLayout`) are private to it. Splitting
 * them out would buy a fast refresh that the router's own export already
 * prevents, at the cost of two files whose only reader is this one.
 */
import { useCallback, useMemo, type ReactNode } from 'react'
import { createBrowserRouter } from 'react-router-dom'

import { App } from './App'
import {
  useAuthStatus,
  useLogin,
  useRoots,
  useScanCompletionRefresh,
  useScanStatus,
  useSeriesList,
} from './api/queries'
import { RouteErrorScreen } from './components/shell/RouteScaffold'
import { LibraryPage } from './features/library/LibraryPage'
import { Overlays } from './features/overlays/Overlays'
import { SeriesDetailPage } from './features/series/SeriesDetailPage'
import { ViewerPage } from './features/viewer/ViewerPage'
import { ROUTER_BASENAME } from './lib/basePath'
import { DEFAULT_SHELL_DATA, ShellDataContext, type ShellData } from './lib/shellData'

/**
 * The router (impl-plan §5.2, ui-spec §3).
 *
 * `basename` is read **synchronously** from `<base href>` at module load, before
 * `createBrowserRouter` is called — a router created with the wrong basename
 * cannot be corrected without remounting the whole tree, and the server rewrites
 * that href per `base_path` (NFR-SEC-003).
 *
 * Three routes, exactly as specified. `settings`, `palette` and `shortcuts` are
 * **state, not routes** (`store/ui.ts`): they overlay whatever is beneath, and
 * making them routes would put a modal in the back-button history.
 *
 * The viewer is a **sibling** of the series route rather than a child of it.
 * Both shapes render "over the shell" — the viewer is `position: fixed; z-60`
 * either way — but keeping it flat means the series page is not forced to
 * render an `<Outlet/>` it has no other use for.
 *
 * The wave-1 placeholders are gone: the three route elements are now the real
 * screens (`features/library`, `features/series`, `features/viewer`).
 * `RouteScaffold.tsx` survives only for `RouteErrorScreen`, which was never
 * scaffolding.
 *
 * Every route is `element`, not `lazy`. The whole client is one 372 kB chunk
 * (119 kB gzipped, measured) embedded in the binary and served from localhost,
 * so splitting the viewer out would trade a real cost — a second round trip
 * between "open the volume" and the first `usePrefetch` call, i.e. exactly the
 * moment FR-VWR-007 asks to be fast — for a saving nobody can perceive on a LAN.
 * Revisit only if a heavy dependency (a PDF or zoom library) ever lands in
 * `features/viewer`.
 */

/**
 * Feeds `ShellDataContext` from the WP-06 query hooks.
 *
 * `lib/shellData.ts` declares the four pieces of server state the shell needs
 * and says "a wave-2 provider supplies it"; no wave-2 package claimed that job,
 * so it lives here — with the router, which is the one place that already knows
 * both the shell and the API exist. The context default renders a working but
 * *empty* shell (no roots, no counts, nothing scanning), which is a silent
 * degradation rather than an error, so its absence is easy to miss.
 *
 * The field names in `api/types.ts` are the wire's, which is exactly why the
 * DTOs satisfy the `Shell*` interfaces structurally with no adapter.
 *
 * All three `counts` are `total` from a `limit: 1` list call, including
 * `counts.added`: amendment **A-8** (ruling E-9) added `scope=added` precisely so
 * 최근 추가 could be counted the same way 읽는 중 and 완독 already were. Before it
 * this row reported the size of the whole library, which is the "visibly wrong
 * number" E-9 exists to fix; there is no client-side window here, because the
 * server owns `library.recently_added_days` and would disagree with one.
 */
export function ShellDataProvider({ children }: { children: ReactNode }) {
  const auth = useAuthStatus()
  const roots = useRoots()
  const scan = useScanStatus()
  const login = useLogin()

  // The one mount, and it belongs here for the same reason the poll does: this
  // provider is the single component that outlives every screen and every
  // overlay, so "a run finished" is observed once no matter what the user is
  // looking at. A copy inside the settings dialog would fire a second set of
  // refetches, and only for users who happened to have the dialog open.
  useScanCompletionRefresh(scan.data)

  // `limit: 1` — only `total` is read. One row is the smallest answer the
  // contract allows (`limit` is 1..200) and keeps these three off the payload
  // budget the library's own infinite query owns.
  const reading = useSeriesList({ progress: 'reading', limit: 1 })
  const done = useSeriesList({ progress: 'done', limit: 1 })
  const added = useSeriesList({ scope: 'added', limit: 1 })

  const { mutateAsync } = login
  const submitLogin = useCallback(
    async (password: string): Promise<void> => {
      await mutateAsync(password)
    },
    [mutateAsync],
  )

  const value = useMemo<ShellData>(
    () => ({
      auth: auth.data ?? DEFAULT_SHELL_DATA.auth,
      login: submitLogin,
      loginPending: login.isPending,
      roots: roots.data?.items ?? [],
      // `isSuccess`, not `isFetched`: a failed /api/roots must not be read as
      // "there are no roots" and replace the whole app with onboarding.
      rootsLoaded: roots.isSuccess,
      counts: {
        reading: reading.data?.total ?? 0,
        added: added.data?.total ?? 0,
        done: done.data?.total ?? 0,
      },
      scan: scan.data ?? DEFAULT_SHELL_DATA.scan,
    }),
    [
      added.data,
      auth.data,
      done.data,
      login.isPending,
      reading.data,
      roots.data,
      roots.isSuccess,
      scan.data,
      submitLogin,
    ],
  )

  return <ShellDataContext.Provider value={value}>{children}</ShellDataContext.Provider>
}

/**
 * The root route element: the shell, the overlays, and the data both need.
 *
 * The overlays are mounted here rather than inside `App` because they are not
 * chrome — they must survive the auth gate's early return and the onboarding
 * screen's chromeless branch, and `Ctrl/Cmd+K` is bound globally (ui-spec §8.1,
 * `lib/useHotkeys.ts`), so a palette that only existed inside the shell would be
 * a shortcut that silently does nothing on two of the app's screens. `ds/Dialog`
 * portals them to `document.body`, so being a sibling of `.app` rather than a
 * descendant costs nothing.
 */
function RootLayout() {
  return (
    <ShellDataProvider>
      <App />
      <Overlays />
    </ShellDataProvider>
  )
}

export const router = createBrowserRouter(
  [
    {
      path: '/',
      element: <RootLayout />,
      errorElement: <RouteErrorScreen />,
      children: [
        { index: true, element: <LibraryPage /> },
        { path: 'series/:sid', element: <SeriesDetailPage /> },
        { path: 'series/:sid/books/:bid', element: <ViewerPage /> },
      ],
    },
  ],
  { basename: ROUTER_BASENAME },
)
