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

/** The half of `BookPrefs` this rule needs; keeps the store off `api/types`. */
export interface OpeningPrefs {
  reading_direction: ReadingDirection
  /** `false` ⇒ every field is the global default (`GET /api/settings`). */
  is_override: boolean
}

/**
 * The direction a book actually opens at — the seed's **destination** (E-33 §2).
 *
 * The seed used to stop here. `store/seriesDir.ts` had exactly one consumer,
 * `SeriesDetailPage`, and it only fed the `.seg`'s own displayed state:
 * `openBook` navigated with `?page=` alone and the viewer opened on
 * `detail.prefs.reading_direction` every time. Setting R→L on the series screen
 * and opening a volume gave you L→R — while `localStorage` kept the value and
 * the segment stayed lit, so it *looked* like it had worked. C-9 promises the
 * opposite ("seeds the direction for books opened from that screen"), and E-33
 * §2 is the ruling that the promise be kept.
 *
 * **Precedence, which is the rest of the ruling.** A book override wins; the
 * seed only ever replaces the *global default*. `BookPrefs` is already the merge
 * of those two — the server fills each unset field from the default and reports
 * whether anything was overridden at all (`internal/httpapi/books.go`,
 * `mergePrefsWithDefaults`) — so `is_override` is precisely the bit that
 * separates "this reader chose it for this volume" from "nobody chose anything",
 * and it is the bit this reads. Nothing here changes the wire (E-33 §1).
 *
 * **`is_override` is a statement about the whole object.** A book that overrides
 * only its display mode reports `true` while its direction is still the global
 * default, and the seed stands down there too. Telling those apart needs a
 * per-field flag the contract does not have and E-33 §1 freezes; between a seed
 * that sometimes does not apply and a seed that sometimes overwrites a direction
 * the reader chose for this volume, the ruling names the second as the error —
 * "씨앗은 기본값을 대신하는 것이지 오버라이드를 이기는 것이 아니다".
 */
export function openingDirection(
  prefs: OpeningPrefs,
  seed: ReadingDirection | undefined,
): ReadingDirection {
  if (prefs.is_override) return prefs.reading_direction
  return seed ?? prefs.reading_direction
}
