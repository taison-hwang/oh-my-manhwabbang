import { useQueryClient, type QueryClient } from '@tanstack/react-query'
import { useCallback } from 'react'

import { putSettings } from './client'
import { isApiError } from './errors'
import type { Settings, SettingsUpdate, UserSettings } from './types'

/**
 * `PUT /api/settings`, in order — the only way the app writes settings. The same
 * shape as the viewer's per-book writer (`features/viewer/prefsWriter.ts`), with
 * one writer per `QueryClient`: the cache it guards belongs to that client, the
 * app has one, and a test that makes its own starts from nothing.
 *
 * The screen reads the settings cache — the read-defaults controls render it and
 * `useLibrarySettingsSync` hydrates the ui store from it — so the rules are about
 * what may reach the cache:
 *
 *  - every change bumps the revision and patches the cache at once; the buffer
 *    goes out `SETTINGS_SAVE_DEBOUNCE_MS` after the last change;
 *  - one request on the wire at a time, so the server applies them in order and
 *    the last value of every field is the last one it applies;
 *  - a field leaves the buffer only when the server has answered for it, so a
 *    failed field rides along with the next write (a newer value of it wins);
 *  - the cache takes an answer only if nothing was changed after it was sent —
 *    an older echo can never undo a newer choice, not even for one frame;
 *  - transport, 5xx, 408 and 429 failures retry with backoff; after the last the
 *    fields wait for the next write. A 401 waits too (a login fixes it). Any other
 *    4xx would refuse the same body again, so what it refused is dropped unless
 *    changed since: the one field a 400 names (`detail.field`) if the body carried
 *    it, else every field the body carried. The server checks the whole body
 *    before it writes any of it and names only the first field it refuses, so the
 *    rest of a body a 400 named a field of was not applied: it goes again at once,
 *    and a body with several such fields costs a round trip each. Every final
 *    failure re-reads the settings.
 *
 * Known limit, held: a `PUT` that never settles holds the queue behind it.
 *
 * **A GET meeting unsaved choices** (`withUnsavedSettings`, applied by
 * `useSettings`): the payload wins — another device's change hydrates — except
 * for a field this client still has to send, or one a `PUT` answered for after
 * the GET was sent (the GET may have been read before that `PUT` was applied).
 * Those keep the value this client chose, or the server confirmed. A refused
 * field is neither, so the re-read after a refusal shows the server's value.
 *
 * **Leaving the page.** A `visibilitychange` to hidden is not the end — the tab
 * may come back — so its flush is an ordinary queued request that only carries
 * `keepalive`: sent at once if nothing is on the wire, else right after the
 * answer, and every request sent while hidden carries `keepalive` too. Only
 * `pagehide` (the document going away) sends without waiting: nothing if a
 * `keepalive` request on the wire already carries the whole buffer (the hide's
 * flush, normally), else the buffer beside whatever is on the wire. Fields the
 * two carry at different revisions may then be applied in either order, so once
 * **both** have answered — the page was kept, e.g. the bfcache — those fields go
 * once more with their newest values (with the next write if the last answer was
 * a 401, which waits). Nothing else is sent while either is on the
 * wire, so a later edit always reaches the server after both: it cannot be
 * overtaken by the `pagehide` write.
 *
 * `disposeSettingsWriter` drops a client's writer. The app has one `QueryClient`
 * for its whole life and never needs it; tests that make many do.
 */

/**
 * 400 ms: a sweep — arrow-key repeat, or deliberate taps a few per second — is
 * one `PUT`, and a single click still reaches the server well inside a second.
 * Correctness does not depend on it: ordering comes from one request at a time
 * and the revisions, so a slower sweep costs requests, never a wrong state.
 */
export const SETTINGS_SAVE_DEBOUNCE_MS = 400
/** Retries after a transport or 5xx failure; the delays double from `SETTINGS_RETRY_BASE_MS`. */
export const SETTINGS_RETRY_LIMIT = 3
export const SETTINGS_RETRY_BASE_MS = 250

/** The settings query key. It lives here so `queries.ts` can import the writer without a cycle. */
export const SETTINGS_QUERY_KEY = ['settings'] as const

type Field = keyof UserSettings
type Value = UserSettings[Field]
type Timer = ReturnType<typeof setTimeout>

/** Typed as a record so a field added to `UserSettings` cannot be missed here. */
const FIELD_SET: Record<Field, true> = {
  reading_direction: true,
  display_mode: true,
  fit_mode: true,
  prefetch: true,
  theme: true,
  library_view: true,
  library_sort: true,
  library_order: true,
  library_scope: true,
}
const FIELDS = Object.keys(FIELD_SET) as Field[]

interface Submission {
  body: SettingsUpdate
  /** The revision of each field sent, to tell a confirmed field from one changed since. */
  revs: Map<Field, number>
  /** The revision when it was sent. */
  rev: number
  keepalive: boolean
}

interface Writer {
  client: QueryClient
  rev: number
  /** Unconfirmed fields, each with the revision it was set at. */
  pending: Map<Field, { value: Value; rev: number }>
  /** The value this client last chose, or the server last confirmed, per field. */
  known: Map<Field, Value>
  /** `clock` when the server last answered for each field. */
  answeredAt: Map<Field, number>
  debounce: Timer | null
  retry: Timer | null
  /** The debounce ran out. A newer change takes it back. */
  due: boolean
  /** A retry or a re-send asked for the buffer now. A change does not take it back. */
  urgent: boolean
  /** Requests on the wire: the queued one, and at most one `pagehide` sent beside it. */
  wire: Set<Submission>
  /** Fields two requests on the wire carried at different revisions: re-sent once the wire is clear. */
  raced: Set<Field>
  failures: number
}

let writers = new WeakMap<QueryClient, Writer>()
/** The writers with something unsent — what an unload has to send. Held strongly only while so. */
const unsent = new Set<Writer>()
/** Ticks on every answer; a GET is measured against it. Never reset, so an old mark stays comparable. */
let clock = 0

/** A writer replaced by `resetSettingsWriter` is dead: nothing it has on the wire may act. */
const live = (w: Writer): boolean => writers.get(w.client) === w

function writerFor(client: QueryClient): Writer {
  let w = writers.get(client)
  if (w === undefined) {
    w = {
      client,
      rev: 0,
      pending: new Map(),
      known: new Map(),
      answeredAt: new Map(),
      debounce: null,
      retry: null,
      due: false,
      urgent: false,
      wire: new Set(),
      raced: new Set(),
      failures: 0,
    }
    writers.set(client, w)
  }
  return w
}

/** Keeps `unsent`, and the unload listener with it, in step with `w`'s buffer. */
function track(w: Writer): void {
  if (live(w) && w.pending.size > 0) unsent.add(w)
  else unsent.delete(w)
  listenForUnload(unsent.size > 0)
}

function clearTimers(w: Writer): void {
  if (w.debounce !== null) clearTimeout(w.debounce)
  if (w.retry !== null) clearTimeout(w.retry)
  w.debounce = null
  w.retry = null
}

/** Buffers the defined fields of `update` at a new revision; returns them, or `null` if none. */
function record(w: Writer, update: SettingsUpdate): SettingsUpdate | null {
  const entries = FIELDS.flatMap((field) => {
    const value = update[field]
    return value === undefined ? [] : [[field, value] as const]
  })
  if (entries.length === 0) return null
  w.rev += 1
  for (const [field, value] of entries) {
    w.pending.set(field, { value, rev: w.rev })
    w.known.set(field, value)
  }
  clearTimers(w)
  w.due = false
  w.failures = 0
  return Object.fromEntries(entries) as SettingsUpdate
}

function snapshot(w: Writer, keepalive: boolean): Submission {
  const entries = [...w.pending]
  return {
    body: Object.fromEntries(entries.map(([field, e]) => [field, e.value])) as SettingsUpdate,
    revs: new Map(entries.map(([field, e]) => [field, e.rev])),
    rev: w.rev,
    keepalive,
  }
}

const hidden = (): boolean => document.visibilityState === 'hidden'

function answered(w: Writer, sub: Submission): void {
  clock += 1
  for (const field of sub.revs.keys()) w.answeredAt.set(field, clock)
}

/** Sends the buffer only when nothing is on the wire; `keepalive` while the page is hidden. */
function pump(w: Writer, keepalive = hidden()): void {
  if (!live(w)) return
  if (w.wire.size === 0 && (w.due || w.urgent) && w.pending.size > 0) {
    w.due = false
    w.urgent = false
    send(w, snapshot(w, keepalive))
  }
  track(w)
}

function send(w: Writer, sub: Submission): void {
  w.wire.add(sub)
  void putSettings(sub.body, sub.keepalive ? { keepalive: true } : undefined).then(
    (settings) => {
      if (landed(w, sub)) confirmed(w, sub, settings)
    },
    (error: unknown) => {
      if (landed(w, sub)) failed(w, sub, error)
    },
  )
}

/** Takes `sub` off the wire; whether its writer may still act on the answer. */
function landed(w: Writer, sub: Submission): boolean {
  w.wire.delete(sub)
  return live(w)
}

/**
 * If a `pagehide` write raced and the wire is now clear, the raced fields are
 * queued again — before an answer is taken, so one whose order against the other
 * is unknown is not taken into the cache either, but after a refusal's quarantine,
 * so a refused value is not carried past the revision it was refused at. Not
 * `now`, they wait in the buffer for the next write, as a 401 does.
 */
function settleRace(w: Writer, now = true): void {
  if (w.wire.size === 0 && w.raced.size > 0) resend(w, now)
}

function confirmed(w: Writer, sub: Submission, settings: Settings): void {
  if (!live(w)) return
  settleRace(w)
  answered(w, sub)
  w.failures = 0
  for (const [field, rev] of sub.revs) {
    if (w.pending.get(field)?.rev === rev) w.pending.delete(field)
  }
  if (sub.rev === w.rev) {
    // The answer is the whole settings object, and nothing was changed since it
    // was sent: it is the server's word on every field, not only the ones sent —
    // a GET overlay must never bring back an older value of any of them. A field
    // still to be sent keeps the value chosen (none can be, as things stand).
    for (const field of FIELDS) {
      if (!w.pending.has(field)) w.known.set(field, settings[field])
    }
    w.client.setQueryData<Settings>(SETTINGS_QUERY_KEY, settings)
  }
  pump(w)
}

/** Transport, 5xx, 408 and 429: the same body may well succeed later. */
function isRecoverable(error: unknown): boolean {
  if (!isApiError(error)) return true
  return error.status >= 500 || error.status === 408 || error.status === 429
}

/**
 * The field a 400 names in `detail.field` — a value the server's validation
 * refused, a type its decoder refused, or a field it does not know — if `sub`
 * carried it; else `null`, and the whole body is held to blame.
 */
function namedField(sub: Submission, error: unknown): Field | null {
  if (!isApiError(error) || error.status !== 400) return null
  const named = error.detailString('field')
  for (const field of sub.revs.keys()) if (field === named) return field
  return null
}

function failed(w: Writer, sub: Submission, error: unknown): void {
  if (!live(w)) return
  answered(w, sub)
  const unauthorized = isApiError(error) && error.status === 401
  const named = namedField(sub, error)
  if (!isRecoverable(error) && !unauthorized) {
    // Quarantine: the same body would be refused again. Drop what was refused —
    // the field a 400 named, else all the body carried — unless changed since,
    // so it cannot poison the next write, and forget the value, so the re-read
    // below shows the server's. First: a raced re-send would move the field past
    // the revision it was refused at.
    for (const [field, rev] of sub.revs) {
      if (named !== null && field !== named) continue
      if (w.pending.get(field)?.rev !== rev) continue
      w.pending.delete(field)
      w.known.delete(field)
    }
  }
  // After a 401 the raced fields wait with the rest: no login, no new edit, no write.
  settleRace(w, !unauthorized)
  // So does a re-send asked for while this was on the wire — the rest of a body
  // a 400 named a field of, a hide's flush: it would come back 401 the same way.
  if (unauthorized) w.urgent = false
  if (isRecoverable(error) && w.failures < SETTINGS_RETRY_LIMIT) {
    if (w.retry !== null) clearTimeout(w.retry)
    const delay = SETTINGS_RETRY_BASE_MS * 2 ** w.failures
    w.failures += 1
    w.retry = setTimeout(() => {
      w.retry = null
      w.urgent = true
      pump(w)
    }, delay)
    return
  }
  w.failures = 0
  // The re-read goes now, also beside the re-sent rest below: the named field's
  // cache value must return to the server's even if the rest is refused again or
  // its answer is not taken (an edit since), and no GET can undo the rest — a
  // field is kept over the payload while unsent, and once answered, after a GET
  // sent before that answer (`withUnsavedSettings`).
  void w.client.invalidateQueries({ queryKey: SETTINGS_QUERY_KEY })
  // The rest of a body a 400 named one field of was neither refused nor applied,
  // and nothing else would send it: it goes now, with the whole retry budget.
  // Each such 400 takes a field out of the buffer unless edited since, so with
  // no new edit it ends, one round trip per refused field.
  if (named !== null && w.pending.size > 0) w.urgent = true
  pump(w)
}

/**
 * A `pagehide` write and the queued request were applied in an unknown order:
 * send the fields they disagreed on again, newest value first — the one still
 * to be sent, else the one known (chosen, or confirmed by a taken answer). A
 * refused field was forgotten and is not sent. Not `now`, they go with the next write.
 */
function resend(w: Writer, now: boolean): void {
  w.rev += 1
  for (const field of w.raced) {
    const value = w.pending.get(field)?.value ?? w.known.get(field)
    if (value !== undefined) w.pending.set(field, { value, rev: w.rev })
  }
  w.raced.clear()
  if (now) w.urgent = true
}

let listening = false

function onPageHide(): void {
  flushSettingsForUnload()
}

/** Still alive: the buffer goes now, but as the queued request (see the module comment). */
function onVisibilityChange(): void {
  if (!hidden()) return
  for (const w of [...unsent]) {
    clearTimers(w)
    w.due = false
    w.urgent = true
    pump(w, true)
  }
}

function listenForUnload(on: boolean): void {
  if (on === listening) return
  listening = on
  if (on) {
    window.addEventListener('pagehide', onPageHide)
    document.addEventListener('visibilitychange', onVisibilityChange)
  } else {
    window.removeEventListener('pagehide', onPageHide)
    document.removeEventListener('visibilitychange', onVisibilityChange)
  }
}

/** A change: buffered, cache patched, sent `SETTINGS_SAVE_DEBOUNCE_MS` after the last one. */
export function saveSettings(client: QueryClient, update: SettingsUpdate): void {
  const w = writerFor(client)
  const patch = record(w, update)
  if (patch === null) return
  client.setQueryData<Settings>(SETTINGS_QUERY_KEY, (old) =>
    old === undefined ? undefined : { ...old, ...patch },
  )
  w.debounce = setTimeout(() => {
    w.debounce = null
    w.due = true
    pump(w)
  }, SETTINGS_SAVE_DEBOUNCE_MS)
  track(w)
}

/** Whether a `keepalive` request on the wire already carries every unsent field at its revision. */
function covered(w: Writer): boolean {
  for (const [field, e] of w.pending) {
    const carried = [...w.wire].some((sub) => sub.keepalive && sub.revs.get(field) === e.rev)
    if (!carried) return false
  }
  return true
}

/**
 * The page is going away (`pagehide`): every buffer goes now with `keepalive`,
 * **without** waiting for a request already on the wire — the document may not
 * outlive it — unless a `keepalive` request on the wire already carries it.
 */
export function flushSettingsForUnload(): void {
  for (const w of [...unsent]) {
    clearTimers(w)
    w.due = false
    if (w.wire.size === 0) {
      w.urgent = true
      pump(w, true)
      continue
    }
    if (covered(w)) continue
    const sub = snapshot(w, true)
    // Every field on the wire is still unsent, so `sub` carries it too; where
    // the revisions differ, so may the values, and the server may apply either last.
    for (const other of w.wire) {
      for (const [field, rev] of other.revs) {
        if (sub.revs.get(field) !== rev) w.raced.add(field)
      }
    }
    send(w, sub)
  }
}

/** Taken when a GET is sent; `withUnsavedSettings` measures what happened after it. */
export function settingsReadMark(): number {
  return clock
}

/** A GET payload with `client`'s unsaved choices kept (see the module comment). */
export function withUnsavedSettings(client: QueryClient, fetched: Settings, mark: number): Settings {
  const w = writers.get(client)
  if (w === undefined) return fetched
  const keep: SettingsUpdate = {}
  let kept = false
  for (const field of FIELDS) {
    const unsentValue = w.pending.get(field)
    const local =
      unsentValue !== undefined
        ? unsentValue.value
        : (w.answeredAt.get(field) ?? -1) > mark
          ? w.known.get(field)
          : undefined
    if (local === undefined || local === fetched[field]) continue
    Object.assign(keep, { [field]: local })
    kept = true
  }
  return kept ? { ...fetched, ...keep } : fetched
}

/**
 * Test seam: forget every writer, its queue and timers. A request already on the
 * wire is not cancelled; whatever it settles into is a no-op.
 */
export function resetSettingsWriter(): void {
  for (const w of unsent) clearTimers(w)
  unsent.clear()
  writers = new WeakMap()
  listenForUnload(false)
}

/**
 * Drops `client`'s writer: its buffer and timers go, the unload no longer holds
 * it, and whatever it has on the wire settles into a no-op. The app's single
 * `QueryClient` lives as long as the page and never needs this; it is for tests,
 * which make many.
 */
export function disposeSettingsWriter(client: QueryClient): void {
  const w = writers.get(client)
  if (w === undefined) return
  clearTimers(w)
  w.pending.clear()
  w.raced.clear()
  writers.delete(client)
  track(w)
}

/** Test seam. */
export function settingsWriterState(client: QueryClient): {
  pending: number
  inFlight: boolean
  listening: boolean
} {
  const w = writers.get(client)
  return { pending: w?.pending.size ?? 0, inFlight: (w?.wire.size ?? 0) > 0, listening }
}

export type SaveSettings = (update: SettingsUpdate) => void

/** A stable `save` for a component; the queue itself belongs to the `QueryClient`. */
export function useSettingsWriter(): SaveSettings {
  const client = useQueryClient()
  return useCallback(
    (update: SettingsUpdate) => {
      saveSettings(client, update)
    },
    [client],
  )
}
