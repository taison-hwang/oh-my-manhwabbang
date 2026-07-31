import { create } from 'zustand'
import { persist } from 'zustand/middleware'

import type { ReadingDirection } from './viewer'

/**
 * The series-detail 읽기 방향 seed (C-9 / D-35).
 *
 * Persisted reading direction is **per book**, on the server, via
 * `PUT /api/books/{bid}/prefs` — prd FR-VWR-002 says 권 단위 and that is the
 * one persistence story. The `.seg` on the series-detail screen is therefore a
 * client-only convenience: it seeds the direction for books opened from that
 * screen and never writes the server. ui-spec §5.1's "manga root ⇒ rtl"
 * heuristic is dropped, because no metadata exists to key it on.
 *
 * Its own store, and its own `localStorage` key, because C-9 names the key.
 */

export const SERIES_DIR_STORAGE_KEY = 'shelf.seriesDir'

export interface SeriesDirState {
  bySeries: Record<string, ReadingDirection>
  setSeriesDir: (seriesId: string, dir: ReadingDirection) => void
  /** The seed for a series, falling back to the caller's global default. */
  seedFor: (seriesId: string, fallback: ReadingDirection) => ReadingDirection
}

export const useSeriesDirStore = create<SeriesDirState>()(
  persist(
    (set, get) => ({
      bySeries: {},

      setSeriesDir: (seriesId, dir) => {
        set((s) => ({ bySeries: { ...s.bySeries, [seriesId]: dir } }))
      },

      seedFor: (seriesId, fallback) => get().bySeries[seriesId] ?? fallback,
    }),
    { name: SERIES_DIR_STORAGE_KEY },
  ),
)
