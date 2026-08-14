import { useEffect } from 'react'
import { Outlet, matchPath, useLocation, useNavigate } from 'react-router-dom'

import { MobileDrawer } from './components/shell/MobileDrawer'
import { Sidebar } from './components/shell/Sidebar'
import { TopBar } from './components/shell/TopBar'
import { formatScanLabel, scanPercent } from './lib/format'
import { useShellData } from './lib/shellData'
import { applyTheme, watchSystemTheme } from './lib/theme'
import { useGlobalHotkeys } from './lib/useHotkeys'
import { useIsMobile, useIsRail } from './lib/useMediaQuery'
import { useUiStore } from './store/ui'
import { LoginScreen } from './features/auth/LoginScreen'

/**
 * The application shell (ui-spec §3).
 *
 *   auth gate  →  onboarding (chromeless)  →  sidebar + top bar + <Outlet/>
 *
 * Three things are deliberately *not* here:
 *
 *  - **Data.** WP-05 may not call the API, so the four pieces of server state
 *    the chrome needs arrive through `ShellDataContext` (`lib/shellData.ts`),
 *    whose default renders a working empty shell. A wave-2 provider supplies
 *    the real values from the WP-06 query hooks.
 *  - **Screens.** `/`, `/series/:sid` and the viewer are wave-2 packages; the
 *    router mounts placeholders until they land.
 *  - **Overlays.** The palette, settings and shortcuts dialogs are WP-10's.
 *    Their open/closed state lives in `store/ui.ts` and the Esc ladder that
 *    orders them is already wired here, so mounting them is a one-line change.
 *
 * Onboarding replaces the entire shell when no roots are registered (ui-spec
 * §4.6) — so this component decides whether there is chrome, and the route
 * decides what goes inside it.
 */
export function App() {
  const shell = useShellData()
  const navigate = useNavigate()
  const location = useLocation()

  const theme = useUiStore((s) => s.theme)
  const view = useUiStore((s) => s.view)
  const setView = useUiStore((s) => s.setView)
  const scope = useUiStore((s) => s.scope)
  const setScope = useUiStore((s) => s.setScope)
  const sort = useUiStore((s) => s.sort)
  const setSort = useUiStore((s) => s.setSort)
  const query = useUiStore((s) => s.query)
  const setQuery = useUiStore((s) => s.setQuery)
  const drawerOpen = useUiStore((s) => s.drawerOpen)
  const setDrawerOpen = useUiStore((s) => s.setDrawerOpen)
  const openOverlay = useUiStore((s) => s.openOverlay)
  const setRevealSeries = useUiStore((s) => s.setRevealSeries)

  const isMobile = useIsMobile()
  const isRail = useIsRail()

  // `data-theme` on <html> follows the user setting; `system` follows the OS
  // and must keep following it while the app is open (NFR-CMP-003).
  useEffect(() => {
    applyTheme(theme)
    if (theme !== 'system') return undefined
    return watchSystemTheme(() => {
      applyTheme(theme)
    })
  }, [theme])

  const seriesMatch = matchPath('/series/:sid/*', location.pathname)
  const seriesId = seriesMatch?.params.sid ?? null
  const inViewer = matchPath('/series/:sid/books/:bid', location.pathname) !== null

  useGlobalHotkeys({
    onExitViewer: () => {
      void navigate(seriesId === null ? '/' : `/series/${seriesId}`)
    },
  })

  if (shell.auth.auth_required && !shell.auth.authenticated) {
    return (
      <div className="app">
        <LoginScreen onSubmit={shell.login} pending={shell.loginPending} />
      </div>
    )
  }

  // "No roots" is only knowable once /api/roots has answered; showing
  // onboarding before that would flash it on every cold start.
  const chromeless = shell.rootsLoaded && shell.roots.length === 0

  /**
   * The series detail screen's 라이브러리 button — **E-34 §2, from the other
   * screen that has one.**
   *
   * The ruling arms a *reveal*: the library scrolls the series being left into
   * view and focuses its card, so that coming back out of a book is an
   * orientation rather than a search. It was wired into the viewer's own
   * 라이브러리 button (`features/viewer/ViewerPage.tsx`, `goLibrary`) and
   * nowhere else. This button navigated bare, so the library mounted with no
   * instruction and its virtualiser started where an unscrolled virtualiser
   * starts — offset 0. A reader 3 000px into a 963-series shelf was returned to
   * the first card in the collection, every time, and this is the *commoner* of
   * the two paths: 상세화면 is where a reader who opened the wrong volume comes
   * back from.
   *
   * It arms the instruction and nothing else, which is the rest of the ruling.
   * The prototype's `goLibrary` also set `scope: 'all'` and `q: ''`; here those
   * two are A-5 write-backs (`useLibrarySettingsSync` PUTs `library_scope` to
   * `/api/settings`), so the same lines would read as "going back to the
   * library permanently unset my sidebar filter".
   *
   * The `seriesId` guard is not the claim `showBack` makes. The button only
   * renders on a series route, but `seriesId` is the value the store is handed:
   * `setRevealSeries('')` would arm a target no card can match, and — the
   * reveal being cleared only by the surface that consumes it — leave it armed.
   */
  const goLibrary = (): void => {
    if (seriesId !== null) setRevealSeries(seriesId)
    void navigate('/')
  }

  const sidebar = (variant: 'full' | 'rail') => (
    <Sidebar
      variant={variant}
      roots={shell.roots}
      counts={shell.counts}
      scope={scope}
      onScopeChange={(next) => {
        setScope(next)
        if (location.pathname !== '/') void navigate('/')
      }}
      scanning={shell.scan.state !== 'idle'}
      scanLabel={formatScanLabel(shell.scan)}
      onOpenScanLog={() => {
        openOverlay('settings')
      }}
      onOpenSettings={() => {
        openOverlay('settings')
      }}
      onOpenShortcuts={() => {
        openOverlay('shortcuts')
      }}
    />
  )

  return (
    <div className="app">
      {chromeless ? (
        <div className="flex min-h-0 flex-1 flex-col overflow-auto">
          <Outlet />
        </div>
      ) : (
        <div className="shell">
          {/* Below 768 the sidebar is not merely hidden — it is not rendered.
              CSS alone would leave a second copy of every nav row in the
              accessibility tree once the drawer opens. */}
          {!isMobile && sidebar(isRail ? 'rail' : 'full')}
          <main className="flex min-h-0 min-w-0 flex-1 flex-col">
            <TopBar
              showBack={seriesId !== null}
              onBack={goLibrary}
              query={query}
              onQueryChange={setQuery}
              scanning={shell.scan.state !== 'idle'}
              scanPercent={scanPercent(shell.scan)}
              sort={sort}
              onSortChange={(next) => {
                setSort(next)
              }}
              view={view}
              onViewChange={setView}
              onOpenDrawer={() => {
                setDrawerOpen(true)
              }}
            />
            <div className="min-h-0 min-w-0 flex-1 overflow-hidden">
              <Outlet />
            </div>
          </main>
        </div>
      )}

      {/* Portals: the viewer (z-60) is rendered by its own route element, so
          only the drawer lives here. Dialogs (z-80) arrive with WP-10.

          The drawer serves *both* sub-1024 tiers (ui-spec §7): below 768 it is
          the only sidebar there is, and at 768–1023 it is the overlay that makes
          the icon rail's root names and counts reachable at all. Mounting it on
          `isMobile` alone left the rail a dead end — the hamburger was there but
          nothing could open. */}
      {(isMobile || isRail) && !inViewer && (
        <MobileDrawer
          open={drawerOpen}
          onClose={() => {
            setDrawerOpen(false)
          }}
        >
          {sidebar('full')}
        </MobileDrawer>
      )}
    </div>
  )
}
