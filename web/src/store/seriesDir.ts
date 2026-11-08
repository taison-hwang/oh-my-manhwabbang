import { useQueryClient, type QueryClient } from '@tanstack/react-query'
import { useEffect } from 'react'

import { getSeries, putSeriesPrefs } from '../api/client'
import { isApiError } from '../api/errors'
import { inSeriesPrefsQueue, invalidateCoverFamilies, seriesDirectionAsked } from '../api/queries'
import {
  ID_PATTERN,
  READING_DIRS,
  type AuthStatus,
  type ID,
  type ReadingDir,
  type SeriesPrefs,
} from '../api/types'

/**
 * The series-detail 읽기 방향 **seed**, as a migration source only (ruling E-69 §2.3).
 *
 * Until E-69 the `.seg` on the series screen was a client-only seed (C-9, E-33 §2): a
 * zustand store persisted under `shelf.seriesDir`, which the viewer read to replace the
 * global default. E-69 reverses that — the series' direction lives on the server
 * (`SeriesDetail.reading_direction`, `PUT /api/series/{sid}/prefs`), the viewer opens on
 * the server's book → series → global ladder (`BookPrefs.reading_direction`), and the
 * store is gone. Nothing writes this key any more.
 *
 * What is left is the one-time upload: on the first boot each seed is offered to the
 * server, but only where the server has no value yet, and then removed —
 *
 *  * `GET /api/series/{sid}` is `404` (or `400`): the series left the index. Dropped.
 *  * `reading_direction` is `null`: the seed is `PUT`, and dropped once the server took
 *    it (dropped too on a `404`/`400`, which no retry would change).
 *  * `reading_direction` is set: the server's choice stands. Dropped without a `PUT`.
 *  * the reader asked, in this page load, to write that series' direction — before the
 *    upload reached it, or while its GET was on the wire: the reader's choice stands
 *    (a `null` may be their 기본). Dropped without a `PUT`.
 *  * anything else — a network failure, a `5xx`, a `401`: kept, for the next boot.
 *
 * Each series' GET → "still null?" → PUT is one step in the queue the `.seg`'s own
 * writes take (`inSeriesPrefsQueue`), so no choice can land between the read and the
 * write, and one asked for during the step goes after it. A seed the server took is
 * announced (`onWritten`) inside its own step, before the next series is asked about.
 *
 * The key goes when its last seed does. It runs once per page load: a second caller gets
 * the first one's promise, so a double mount cannot `PUT` anything twice.
 */

export const SERIES_DIR_STORAGE_KEY = 'shelf.seriesDir'

/** What zustand's `persist` wrote while this was a store. */
interface PersistedSeeds {
  state: { bySeries: Record<string, ReadingDir> }
  version: number
}

export interface SeedMigrationDeps {
  getSeries: (sid: ID) => Promise<{ reading_direction: ReadingDir | null }>
  putSeriesPrefs: (sid: ID, prefs: SeriesPrefs) => Promise<SeriesPrefs>
  /**
   * Runs one series' step in the series-direction write queue (`inSeriesPrefsQueue`).
   * Absent: at once — the unit tests of the decisions below, which have no reader.
   */
  queue?: <T>(step: () => Promise<T>) => Promise<T>
  /** Whether the reader has asked to write this series' direction (`seriesDirectionAsked`). */
  readerAsked?: (sid: ID) => boolean
  /**
   * The server took this series' seed: its books open on another effective direction now.
   * Called the moment the `PUT` answered, inside the seed's step — before the next seed's
   * GET is sent, so a slow request for another series cannot hold this one's re-read back.
   */
  onWritten?: (sid: ID) => void
}

export interface SeedMigrationResult {
  /** Seeds the server took — their series' books now open on another direction. */
  written: ID[]
  /** Seeds left for the next boot. */
  kept: ID[]
}

function isReadingDir(value: unknown): value is ReadingDir {
  return (READING_DIRS as readonly unknown[]).includes(value)
}

type ReadOutcome =
  | { kind: 'absent' }
  | { kind: 'unreadable' }
  | { kind: 'seeds'; seeds: Record<ID, ReadingDir>; dropped: boolean }

/** Every `localStorage` touch is guarded: a private window or blocked storage throws. */
function readSeeds(): ReadOutcome {
  let raw: string | null
  try {
    raw = localStorage.getItem(SERIES_DIR_STORAGE_KEY)
  } catch {
    // Unreadable storage is not "no seeds" — but there is nothing to act on either, and
    // the key cannot be removed through a storage that refuses to be read.
    return { kind: 'absent' }
  }
  if (raw === null) return { kind: 'absent' }
  let parsed: unknown
  try {
    parsed = JSON.parse(raw)
  } catch {
    return { kind: 'unreadable' }
  }
  const bySeries: unknown =
    typeof parsed === 'object' && parsed !== null
      ? (parsed as { state?: { bySeries?: unknown } }).state?.bySeries
      : undefined
  if (typeof bySeries !== 'object' || bySeries === null || Array.isArray(bySeries)) {
    return { kind: 'unreadable' }
  }
  const seeds: Record<ID, ReadingDir> = {}
  let dropped = false
  for (const [sid, dir] of Object.entries(bySeries as Record<string, unknown>)) {
    // An id the server could never have issued, or a value that is not a direction, can
    // only ever be refused: dropped without a request.
    if (ID_PATTERN.test(sid) && isReadingDir(dir)) seeds[sid] = dir
    else dropped = true
  }
  return { kind: 'seeds', seeds, dropped }
}

function writeSeeds(seeds: Record<ID, ReadingDir>): void {
  try {
    if (Object.keys(seeds).length === 0) {
      localStorage.removeItem(SERIES_DIR_STORAGE_KEY)
      return
    }
    // The shape it was read in, so a rolled-back client could still read what is left.
    const value: PersistedSeeds = { state: { bySeries: seeds }, version: 0 }
    localStorage.setItem(SERIES_DIR_STORAGE_KEY, JSON.stringify(value))
  } catch {
    // Storage refused the write: the seeds stay, and the next boot meets a server that
    // already has these directions and drops them without a PUT.
  }
}

/** `400` and `404` say the series is not one this server has or can have. */
function isGone(error: unknown): boolean {
  return isApiError(error) && (error.status === 404 || error.status === 400)
}

type Settled = 'written' | 'kept' | 'dropped'

/** One seed against the server — never rejects; the outcome says what became of it. */
async function settle(deps: SeedMigrationDeps, sid: ID, dir: ReadingDir): Promise<Settled> {
  const readerAsked = deps.readerAsked ?? (() => false)
  if (readerAsked(sid)) return 'dropped'
  let current: ReadingDir | null
  try {
    current = (await deps.getSeries(sid)).reading_direction
  } catch (error) {
    return isGone(error) ? 'dropped' : 'kept'
  }
  if (current !== null) return 'dropped'
  // Asked for while the GET was on the wire: that write is queued behind this step,
  // and the seed must not be the last word before it.
  if (readerAsked(sid)) return 'dropped'
  try {
    await deps.putSeriesPrefs(sid, { reading_direction: dir })
  } catch (error) {
    return isGone(error) ? 'dropped' : 'kept'
  }
  deps.onWritten?.(sid)
  return 'written'
}

async function migrate(deps: SeedMigrationDeps): Promise<SeedMigrationResult> {
  const read = readSeeds()
  if (read.kind === 'absent') return { written: [], kept: [] }
  if (read.kind === 'unreadable') {
    writeSeeds({})
    return { written: [], kept: [] }
  }

  const queue = deps.queue ?? (<T>(step: () => Promise<T>) => step())
  const kept: Record<ID, ReadingDir> = {}
  const written: ID[] = []
  // One series at a time: a handful of seeds, and no burst against the server at boot.
  for (const [sid, dir] of Object.entries(read.seeds)) {
    const outcome = await queue(() => settle(deps, sid, dir))
    if (outcome === 'written') written.push(sid)
    else if (outcome === 'kept') kept[sid] = dir
  }

  const keptIds = Object.keys(kept)
  if (read.dropped || keptIds.length !== Object.keys(read.seeds).length) writeSeeds(kept)
  return { written, kept: keptIds }
}

let running: Promise<SeedMigrationResult> | null = null

/** Uploads the seeds once per page load; a second call shares the first one's run. */
export function migrateSeriesDirSeeds(deps: SeedMigrationDeps): Promise<SeedMigrationResult> {
  running ??= migrate(deps)
  return running
}

/** Forgets the run, so a test can boot again. */
export function resetSeriesDirMigration(): void {
  running = null
}

function clientDeps(client: QueryClient): SeedMigrationDeps {
  return {
    getSeries: (sid) => getSeries(sid),
    putSeriesPrefs: (sid, prefs) => putSeriesPrefs(sid, prefs),
    queue: (step) => inSeriesPrefsQueue(client, step),
    readerAsked: (sid) => seriesDirectionAsked(client, sid),
    // Per seed, as it is taken: the same families the `.seg`'s own write invalidates.
    onWritten: () => {
      invalidateCoverFamilies(client)
    },
  }
}

/**
 * Runs the upload from the shell — **mount it once**, in `ShellDataProvider`.
 *
 * It waits for the auth status: behind the login screen every request is a `401`, which
 * would keep every seed and spend the once-per-boot run on nothing. A seed the server
 * took changes its series' effective direction and `cover_cv`s (E-69 §2.3, §2.7), so the
 * same three families the direction mutation invalidates are invalidated — for each seed
 * the moment the server took it (`onWritten`), not once the whole upload is over: until
 * then a volume of that series read earlier would still open on the old direction, and
 * the viewer only waits for a re-read of a detail that has been invalidated.
 */
export function useSeriesDirSeedMigration(auth: AuthStatus | undefined): void {
  const queryClient = useQueryClient()
  const ready = auth !== undefined && (!auth.auth_required || auth.authenticated)
  useEffect(() => {
    if (!ready) return
    void migrateSeriesDirSeeds(clientDeps(queryClient))
  }, [queryClient, ready])
}
