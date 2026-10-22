import { create } from 'zustand'
import { persist } from 'zustand/middleware'

import { LIBRARY_VIEWS, SORT_KEYS, SORT_ORDERS, THEMES } from '../api/types'
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

/**
 * What a saved scope must look like to be one at all. A root name is a
 * configuration identity of `[a-zA-Z0-9._-]{1,64}` (arch §3.2) and the four
 * smart lists are words in the same alphabet; which roots exist is the
 * server's to say, so this is a shape, not a membership test.
 */
const SCOPE_SHAPE = /^[a-zA-Z0-9._-]{1,64}$/

/** Whether `value`, read from outside the type system, is one of `list`. */
function isOneOf<T extends string>(list: readonly T[], value: unknown): value is T {
  return (list as readonly unknown[]).includes(value)
}

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
  /**
   * Whether `view` is the reader's — saved in `shelf.ui`, sent by the server or
   * picked — rather than the store's default `grid` (**item ⑤**). The library
   * holds its first paint until this is true or `/api/settings` has failed: a
   * skeleton drawn as the default grid and then swapped for a remembered list
   * is a layout shift.
   *
   * Deliberately outside `PersistedUi`. It says where *this load's* `view` came
   * from, so the persist `merge` derives it from storage on every load instead.
   */
  libraryViewKnown: boolean

  /**
   * Applies `data-theme` at once. Saving it is the caller's — the 테마 switch
   * (`ReadDefaultsPanel`) also hands it to the settings writer, which keeps every
   * echo older than it out of the cache this store hydrates from (item bq).
   */
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
  /**
   * Server settings win once they arrive (arch §7.8 + A-5). No stale payload gets
   * here: `api/settingsWriter.ts` keeps them out of the settings cache.
   */
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
      libraryViewKnown: false,

      setTheme: (theme) => {
        set({ theme })
        applyTheme(theme)
      },

      setView: (view) => {
        set({ view, libraryViewKnown: true })
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
        const { theme, ...rest } = settings
        // Item ⑤: in the same `set` as the view, so no render sees it "unknown".
        const known = rest.view === undefined ? {} : { libraryViewKnown: true }
        if (theme === undefined) {
          set({ ...rest, ...known })
          return
        }
        set({ ...rest, ...known, theme })
        applyTheme(theme)
      },
    }),
    {
      name: UI_STORAGE_KEY,
      // The default shallow merge, field by field. `persisted` is whatever storage
      // held, and all of it is read before the server answers — `theme` by
      // `main.tsx`'s `applyTheme`, the rest by the library's first request — so
      // every field is checked before it is trusted: one that is not a value of
      // its type is dropped (the store keeps its own), and a key outside
      // `PersistedUi` is never restored. The lists are the wire enums; each
      // narrows to its wire type, which must assign to `PersistedUi`.
      // Item ⑤: a saved `view` is the reader's, and a stored `libraryViewKnown`
      // is overwritten, never believed.
      merge: (persisted, current) => {
        if (typeof persisted !== 'object' || persisted === null) return current
        const saved = persisted as Record<string, unknown>
        const restored: Partial<PersistedUi> = {}
        if (isOneOf(THEMES, saved.theme)) restored.theme = saved.theme
        if (isOneOf(LIBRARY_VIEWS, saved.view)) restored.view = saved.view
        if (typeof saved.scope === 'string' && SCOPE_SHAPE.test(saved.scope)) restored.scope = saved.scope
        if (isOneOf(SORT_KEYS, saved.sort)) restored.sort = saved.sort
        if (isOneOf(SORT_ORDERS, saved.order)) restored.order = saved.order
        return {
          ...current,
          ...restored,
          libraryViewKnown: current.libraryViewKnown || restored.view !== undefined,
        }
      },
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
