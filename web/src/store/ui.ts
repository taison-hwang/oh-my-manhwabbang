import { create } from 'zustand'
import { persist } from 'zustand/middleware'

import { applyTheme, type ThemeSetting } from '../lib/theme'

/**
 * Application UI state (impl-plan §5.2: Zustand for UI state, Query for server
 * data — never the other way round).
 *
 * Everything here is a *view* preference. The server also persists some of it
 * (`Settings.theme`, `library_view`, `library_sort/order`, `library_scope` per
 * A-5) and the server is authoritative once it answers: `hydrateFromSettings`
 * is the one-way door for that. The local copy exists so the first paint is not
 * a flash of the wrong theme and the wrong view mode while `/api/settings` is
 * in flight.
 */

export type ViewMode = 'grid' | 'list'

/** Wire sort keys — C-3: the API's names win over the ui-spec's. */
export type SortKey = 'name' | 'mtime' | 'recent' | 'size' | 'books' | 'added'
export type SortOrder = 'asc' | 'desc'

/**
 * `'all' | 'reading' | 'added' | 'done'` or a root **name**.
 *
 * Smart lists map onto `progress=` (amendment A-4) and roots onto `root=`;
 * `added` is `sort=added&order=desc`. Deliberately a plain string because a
 * root name is user-supplied config, not an enum.
 */
export type Scope = string

/** Overlays are state, not routes (ui-spec §3). */
export type Overlay = 'palette' | 'settings' | 'shortcuts'

/** The DS default direction for a sortable column's first click (ui-spec §4.5). */
export function defaultOrderFor(key: SortKey): SortOrder {
  return key === 'name' ? 'asc' : 'desc'
}

/**
 * The DOM ids the E-34 §2 reveal focuses, beside the `revealSeries` instruction
 * that targets them — the pair is one mechanism and drifts if it is two files.
 *
 * They live here rather than in `SeriesCard`/`SeriesRow` for a second reason:
 * exporting a *function* next to a component turns off Vite's fast refresh for
 * that module (`react-refresh/only-export-components`), and these two are the
 * only non-component exports either card would have.
 *
 * The id is never the reveal *mechanism*. Both surfaces are virtualised, so the
 * element does not exist until the virtualiser has been scrolled to its index —
 * see the long note in `SeriesGrid`.
 */
export function seriesCardDomId(seriesId: string): string {
  return `series-card-${seriesId}`
}

export function seriesRowDomId(seriesId: string): string {
  return `series-row-${seriesId}`
}

export interface UiState {
  theme: ThemeSetting
  view: ViewMode
  scope: Scope
  sort: SortKey
  order: SortOrder
  /** The top-bar search field. Debounced into `GET /api/series?q=` by WP-09. */
  query: string
  /** The command palette's own query, cleared every time it is toggled open. */
  paletteQuery: string
  /** Off-canvas sidebar below 768px (ui-spec §7). Closed by default. */
  drawerOpen: boolean
  /**
   * The series the library should scroll to and focus on arrival (**E-34 §2**).
   *
   * A **one-shot instruction**, not a selection: whichever of the grid or the
   * list is mounted consumes it, clears it, and keeps its own local mark. Left
   * standing it would re-steal the focus every time the library mounted, which
   * is a different product.
   *
   * Deliberately outside `PersistedUi`. `scope`, `sort`, `order` and `view` are
   * remembered across sessions (A-5); "where I was a moment ago" is not — and
   * writing it back would put a transient into `PUT /api/settings`.
   */
  revealSeries: string | null
  /**
   * Open overlays, oldest first. A stack rather than a single slot so the `Esc`
   * ladder of ui-spec §8.1 ("close palette / shortcuts / settings if any is
   * open, **else** close the viewer") has an unambiguous top.
   */
  overlays: Overlay[]

  setTheme: (theme: ThemeSetting) => void
  setView: (view: ViewMode) => void
  setScope: (scope: Scope) => void
  /**
   * Applies the sortable-header rule: clicking a new column takes that
   * column's default direction (asc for 시리즈명, desc for 권/용량/수정일);
   * clicking the active column flips it.
   */
  toggleSort: (key: SortKey) => void
  setSort: (key: SortKey, order?: SortOrder) => void
  setQuery: (query: string) => void
  setPaletteQuery: (query: string) => void
  setDrawerOpen: (open: boolean) => void
  toggleDrawer: () => void
  /** Arms (or, with `null`, disarms) the E-34 §2 reveal. */
  setRevealSeries: (seriesId: string | null) => void
  openOverlay: (overlay: Overlay) => void
  closeOverlay: (overlay: Overlay) => void
  toggleOverlay: (overlay: Overlay) => void
  /** Pops the topmost overlay. Returns it, or `null` when none was open. */
  closeTopOverlay: () => Overlay | null
  /** Server settings win once they arrive (arch §7.8 + A-5). */
  hydrateFromSettings: (settings: Partial<PersistedUi>) => void
}

/** The slice written to `localStorage` under `shelf.ui`. */
export interface PersistedUi {
  theme: ThemeSetting
  view: ViewMode
  scope: Scope
  sort: SortKey
  order: SortOrder
}

export const UI_STORAGE_KEY = 'shelf.ui'

export const useUiStore = create<UiState>()(
  persist(
    (set, get) => ({
      theme: 'system',
      view: 'grid',
      scope: 'all',
      sort: 'name',
      order: 'asc',
      query: '',
      paletteQuery: '',
      drawerOpen: false,
      revealSeries: null,
      overlays: [],

      setTheme: (theme) => {
        set({ theme })
        applyTheme(theme)
      },

      setView: (view) => {
        set({ view })
      },

      setScope: (scope) => {
        // Changing scope closes the drawer: on a phone the drawer covers the
        // result it just navigated to.
        set({ scope, drawerOpen: false })
      },

      toggleSort: (key) => {
        const { sort, order } = get()
        if (sort === key) {
          set({ order: order === 'asc' ? 'desc' : 'asc' })
        } else {
          set({ sort: key, order: defaultOrderFor(key) })
        }
      },

      setSort: (key, order) => {
        set({ sort: key, order: order ?? defaultOrderFor(key) })
      },

      setQuery: (query) => {
        set({ query })
      },

      setPaletteQuery: (paletteQuery) => {
        set({ paletteQuery })
      },

      setDrawerOpen: (drawerOpen) => {
        set({ drawerOpen })
      },

      toggleDrawer: () => {
        set((s) => ({ drawerOpen: !s.drawerOpen }))
      },

      setRevealSeries: (revealSeries) => {
        set({ revealSeries })
      },

      openOverlay: (overlay) => {
        set((s) => ({ overlays: [...s.overlays.filter((o) => o !== overlay), overlay] }))
      },

      closeOverlay: (overlay) => {
        set((s) => ({ overlays: s.overlays.filter((o) => o !== overlay) }))
      },

      toggleOverlay: (overlay) => {
        const open = get().overlays.includes(overlay)
        if (open) {
          get().closeOverlay(overlay)
        } else {
          get().openOverlay(overlay)
        }
      },

      closeTopOverlay: () => {
        const { overlays } = get()
        const top = overlays.at(-1) ?? null
        if (top !== null) set({ overlays: overlays.slice(0, -1) })
        return top
      },

      hydrateFromSettings: (settings) => {
        set(settings)
        if (settings.theme !== undefined) applyTheme(settings.theme)
      },
    }),
    {
      name: UI_STORAGE_KEY,
      partialize: (s): PersistedUi => ({
        theme: s.theme,
        view: s.view,
        scope: s.scope,
        sort: s.sort,
        order: s.order,
      }),
    },
  ),
)
