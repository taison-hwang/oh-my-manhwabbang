import { beforeEach, describe, expect, it, vi } from 'vitest'

import { UI_STORAGE_KEY, defaultOrderFor, useUiStore } from './ui'

const reset = (): void => {
  useUiStore.setState({
    theme: 'system',
    view: 'grid',
    scope: 'all',
    sort: 'name',
    order: 'asc',
    query: '',
    paletteQuery: '',
    drawerOpen: false,
    overlays: [],
    libraryViewKnown: false,
  })
}

beforeEach(() => {
  localStorage.clear()
  reset()
})

/**
 * A page load: a new module whose `persist` reads `shelf.ui` once. Storage is
 * emptied first because `reset()` goes through `persist` and saves a `view`.
 */
const freshStore = async (saved?: Record<string, unknown>) => {
  localStorage.clear()
  if (saved !== undefined) {
    localStorage.setItem(UI_STORAGE_KEY, JSON.stringify({ state: saved, version: 0 }))
  }
  vi.resetModules()
  return (await import('./ui')).useUiStore
}

describe('sortable headers (ui-spec §4.5)', () => {
  it('sorts 시리즈명 ascending on the first click', () => {
    useUiStore.setState({ sort: 'size', order: 'desc' })
    useUiStore.getState().toggleSort('name')
    expect(useUiStore.getState()).toMatchObject({ sort: 'name', order: 'asc' })
  })

  it('sorts 권 / 용량 / 수정일 descending on the first click', () => {
    for (const key of ['books', 'size', 'mtime'] as const) {
      reset()
      useUiStore.getState().toggleSort(key)
      expect(useUiStore.getState()).toMatchObject({ sort: key, order: 'desc' })
    }
  })

  it('flips direction when the active column is clicked again', () => {
    useUiStore.getState().toggleSort('size') // -> desc
    useUiStore.getState().toggleSort('size')
    expect(useUiStore.getState().order).toBe('asc')
    useUiStore.getState().toggleSort('size')
    expect(useUiStore.getState().order).toBe('desc')
  })

  it('uses the API sort vocabulary, not the ui-spec draft (C-3)', () => {
    // `recent` and `books`, never `read` or `vols`.
    expect(defaultOrderFor('recent')).toBe('desc')
    expect(defaultOrderFor('books')).toBe('desc')
    expect(defaultOrderFor('name')).toBe('asc')
  })
})

describe('the Esc ladder (ui-spec §8.1)', () => {
  it('closes overlays newest-first, then reports empty', () => {
    const s = useUiStore.getState()
    s.openOverlay('settings')
    s.openOverlay('shortcuts')
    expect(useUiStore.getState().closeTopOverlay()).toBe('shortcuts')
    expect(useUiStore.getState().closeTopOverlay()).toBe('settings')
    // Nothing left: the caller falls through to closing the viewer.
    expect(useUiStore.getState().closeTopOverlay()).toBeNull()
  })

  it('re-opening an overlay moves it to the top rather than duplicating it', () => {
    const s = useUiStore.getState()
    s.openOverlay('palette')
    s.openOverlay('settings')
    s.openOverlay('palette')
    expect(useUiStore.getState().overlays).toEqual(['settings', 'palette'])
  })

  it('toggles', () => {
    useUiStore.getState().toggleOverlay('palette')
    expect(useUiStore.getState().overlays).toEqual(['palette'])
    useUiStore.getState().toggleOverlay('palette')
    expect(useUiStore.getState().overlays).toEqual([])
  })
})

describe('scope', () => {
  it('closes the drawer, because on a phone it covers the result', () => {
    useUiStore.getState().setDrawerOpen(true)
    useUiStore.getState().setScope('reading')
    expect(useUiStore.getState()).toMatchObject({ scope: 'reading', drawerOpen: false })
  })

  it('accepts a root name as well as a smart list', () => {
    useUiStore.getState().setScope('01. mangga')
    expect(useUiStore.getState().scope).toBe('01. mangga')
  })
})

describe('persistence', () => {
  it('persists only the sticky view preferences', () => {
    const s = useUiStore.getState()
    s.setView('list')
    s.setScope('done')
    s.setSort('size')
    s.setQuery('환타')
    s.openOverlay('palette')

    const raw = localStorage.getItem(UI_STORAGE_KEY)
    expect(raw).not.toBeNull()
    const parsed = JSON.parse(raw ?? '{}') as { state?: Record<string, unknown> }
    expect(parsed.state).toEqual({
      theme: 'system',
      view: 'list',
      scope: 'done',
      sort: 'size',
      order: 'desc',
    })
    // Transient state must not survive a reload.
    expect(parsed.state).not.toHaveProperty('query')
    expect(parsed.state).not.toHaveProperty('overlays')
  })

  it('lets the server win once /api/settings answers (A-5)', () => {
    useUiStore.getState().setView('list')
    useUiStore.getState().hydrateFromSettings({ view: 'grid', scope: 'reading', theme: 'dark' })
    expect(useUiStore.getState()).toMatchObject({
      view: 'grid',
      scope: 'reading',
      theme: 'dark',
    })
    expect(document.documentElement.getAttribute('data-theme')).toBe('dark')
  })
})

/**
 * The store's half of item bq. Keeping a stale payload away from the theme is
 * not done here: no echo older than the latest pick reaches the settings cache
 * (`api/settingsWriter.ts`), and `themeHydration.test.tsx` holds the reader's
 * side of it — first load, stale echoes, other devices, `system`, bad values.
 */
describe('theme (item bq)', () => {
  it('applies a switch at once and persists only the sticky slice', () => {
    useUiStore.getState().setTheme('dark')
    expect(document.documentElement.getAttribute('data-theme')).toBe('dark')
    const parsed = JSON.parse(localStorage.getItem(UI_STORAGE_KEY) ?? '{}') as {
      state?: Record<string, unknown>
    }
    expect(parsed.state).toEqual({
      theme: 'dark',
      view: 'grid',
      scope: 'all',
      sort: 'name',
      order: 'asc',
    })
  })

  it('takes the theme of every payload it is handed, a recent switch or not', () => {
    useUiStore.getState().setTheme('light')
    useUiStore.getState().hydrateFromSettings({ theme: 'dark', view: 'list' })
    expect(useUiStore.getState()).toMatchObject({ theme: 'dark', view: 'list' })
    expect(document.documentElement.getAttribute('data-theme')).toBe('dark')
  })

  it('leaves the theme alone when a payload carries none', () => {
    useUiStore.getState().setTheme('light')
    useUiStore.getState().hydrateFromSettings({ view: 'list' })
    expect(useUiStore.getState()).toMatchObject({ theme: 'light', view: 'list' })
  })

  it('restores the saved theme in a fresh store for the first paint, then takes the first server theme', async () => {
    // What `persist` writes: the sticky slice, here a `light` from last session.
    localStorage.setItem(
      UI_STORAGE_KEY,
      JSON.stringify({
        state: { theme: 'light', view: 'list', scope: 'all', sort: 'name', order: 'asc' },
        version: 0,
      }),
    )
    vi.resetModules()
    const fresh = (await import('./ui')).useUiStore
    expect(fresh.getState()).toMatchObject({ theme: 'light', view: 'list' })

    fresh.getState().hydrateFromSettings({ theme: 'dark' })
    expect(fresh.getState().theme).toBe('dark')
  })
})

/**
 * Item ⑤: the library's first paint must already have the reader's shape. A
 * fresh store says `view: 'grid'` whether or not anyone chose it, so the page
 * needs a second bit — "this `view` came from storage, the server or the reader"
 * — to know whether it may draw now or must wait for `/api/settings`.
 */
describe('libraryViewKnown (item ⑤)', () => {
  it('is false in a fresh store with nothing saved', async () => {
    const fresh = await freshStore()
    expect(fresh.getState()).toMatchObject({ view: 'grid', libraryViewKnown: false })
  })

  it('is false when the saved state carries no view', async () => {
    const themeOnly = await freshStore({ theme: 'dark' })
    expect(themeOnly.getState()).toMatchObject({ theme: 'dark', libraryViewKnown: false })

    const nullView = await freshStore({ theme: 'dark', view: null })
    expect(nullView.getState().libraryViewKnown).toBe(false)
  })

  // Storage is outside the type system: a cast does not make `'invalid'` a view.
  // An unreadable one is dropped — the store keeps its own, and is not "known".
  it.each([null, 'invalid', false, {}])('drops a saved view it cannot read (%j)', async (view) => {
    const fresh = await freshStore({ theme: 'dark', view })
    expect(fresh.getState()).toMatchObject({ theme: 'dark', view: 'grid', libraryViewKnown: false })
  })

  it('is true after rehydrating a saved view, the default one included', async () => {
    for (const view of ['list', 'grid'] as const) {
      const fresh = await freshStore({ theme: 'light', view, scope: 'all', sort: 'name', order: 'asc' })
      expect(fresh.getState()).toMatchObject({ view, libraryViewKnown: true })
    }
  })

  it('is set by a settings payload that carries a view, in the same update', () => {
    // One notification per payload: no render ever sees the new view still "unknown".
    const seen: { view: string; known: boolean }[] = []
    const unsubscribe = useUiStore.subscribe((s) => {
      seen.push({ view: s.view, known: s.libraryViewKnown })
    })
    try {
      // Without a theme. The server's `grid` equals the default: the view does not change, the flag must.
      useUiStore.getState().hydrateFromSettings({ view: 'grid' })
      expect(seen).toEqual([{ view: 'grid', known: true }])

      // With a theme, the other branch.
      useUiStore.setState({ libraryViewKnown: false })
      seen.length = 0
      useUiStore.getState().hydrateFromSettings({ theme: 'dark', view: 'list' })
      expect(seen).toEqual([{ view: 'list', known: true }])
    } finally {
      unsubscribe()
    }
  })

  it('is left alone by a payload without a view', () => {
    useUiStore.getState().hydrateFromSettings({ theme: 'dark', scope: 'reading', sort: 'size', order: 'desc' })
    expect(useUiStore.getState().libraryViewKnown).toBe(false)

    useUiStore.setState({ libraryViewKnown: true })
    useUiStore.getState().hydrateFromSettings({ theme: 'light' })
    expect(useUiStore.getState().libraryViewKnown).toBe(true)
  })

  it('is set when the reader picks a view', () => {
    useUiStore.getState().setView('grid')
    expect(useUiStore.getState().libraryViewKnown).toBe(true)
  })

  it('never goes through localStorage, in either direction', async () => {
    useUiStore.getState().setView('list')
    const parsed = JSON.parse(localStorage.getItem(UI_STORAGE_KEY) ?? '{}') as {
      state?: Record<string, unknown>
    }
    expect(parsed.state).not.toHaveProperty('libraryViewKnown')

    const fresh = await freshStore({ theme: 'dark', libraryViewKnown: true })
    expect(fresh.getState().libraryViewKnown).toBe(false)
  })
})

