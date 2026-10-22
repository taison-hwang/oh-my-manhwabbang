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
  })
}

beforeEach(() => {
  localStorage.clear()
  reset()
})

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

