import { useQueryClient, type QueryClient } from '@tanstack/react-query'
import { useCallback, useEffect } from 'react'

import { putBookPrefs } from '../../api/client'
import { isApiError } from '../../api/errors'
import { queryKeys } from '../../api/queries'
import type { BookDetail, BookPrefs, BookPrefsUpdate } from '../../api/types'

/**
 * `PUT /api/books/{bid}/prefs` for the viewer (av).
 *
 * Module-level, keyed by book, so the rules outlive one mounted viewer: leaving
 * a book and reopening it shares its queue. Per book:
 *
 *  - every change bumps the book's revision, and a seg change patches the cache
 *    at once; the buffer goes out `PREFS_SAVE_DEBOUNCE_MS` after the last
 *    change, or at once on a flush;
 *  - one request on the wire at a time, so the server applies them in order;
 *  - a field leaves the buffer only when the server has answered for it, so a
 *    failed field rides along with the next write (a newer value of it wins);
 *  - the cache takes an answer only if nothing was edited after it was sent;
 *  - transport, 5xx, 408 and 429 failures retry with backoff; after the last
 *    the fields wait for the next write. A 401 waits too (a login fixes it). Any
 *    other 4xx would refuse the same body again, so the fields it refused are
 *    dropped unless edited since. Every final failure re-reads the cache;
 *  - a writer with nothing left is forgotten.
 *
 * Leaving the page is the one path that does not wait for the queue: one
 * module-level `pagehide`/`visibilitychange` listener, installed while any
 * writer exists, sends every buffer with `keepalive`. Re-sending after a reload
 * from `localStorage` is deliberately not done (held).
 */

/** How long a burst of seg changes is held before its one write. */
export const PREFS_SAVE_DEBOUNCE_MS = 500
/** Retries after a transport or 5xx failure; the delays double from `PREFS_RETRY_BASE_MS`. */
export const PREFS_RETRY_LIMIT = 3
export const PREFS_RETRY_BASE_MS = 250

export type PrefField = 'reading_direction' | 'display_mode' | 'fit_mode'
const FIELDS: readonly PrefField[] = ['reading_direction', 'display_mode', 'fit_mode']

/** What a seg can write: one of the three fields, never `null`. */
export type SegPrefs = Partial<Pick<BookPrefs, PrefField>>

/**
 * A confirmed `saveNow`. `untouched` is the fields not edited since that call,
 * the only ones the caller may take from `prefs` without undoing a newer choice.
 */
export type PrefsSaved = (prefs: BookPrefs, untouched: ReadonlySet<PrefField>) => void

type PrefValue = Exclude<BookPrefsUpdate[PrefField], undefined>
type Timer = ReturnType<typeof setTimeout>

interface Submission {
  body: BookPrefsUpdate
  /** The revision of each field sent, to tell a confirmed field from one edited since. */
  revs: Map<PrefField, number>
  /** The book's revision when it was sent. */
  rev: number
}

interface BookWriter {
  /** The `generation` this writer belongs to; anything older is dead (`resetPrefsWriter`). */
  gen: number
  client: QueryClient
  rev: number
  /** Unconfirmed fields, each with the revision it was set at. */
  pending: Map<PrefField, { value: PrefValue; rev: number }>
  /** The revision each field was last edited at, confirmed or not. */
  editedAt: Map<PrefField, number>
  callbacks: { rev: number; onSaved: PrefsSaved }[]
  debounce: Timer | null
  retry: Timer | null
  /** A seg edit's debounce ran out. A newer seg edit takes it back. */
  due: boolean
  /** A flush or reset asked for the buffer now. A seg edit does not take it back. */
  urgent: boolean
  inFlight: boolean
  /** Unload writes on the wire; they bypass the queue. */
  beacons: number
  /** The revision an unload write still on the wire carries, so a second unload event does not resend it. */
  unloadRev: number | null
  failures: number
}

const books = new Map<string, BookWriter>()
let generation = 0

const live = (book: BookWriter): boolean => book.gen === generation

function writerFor(client: QueryClient, bid: string): BookWriter {
  let book = books.get(bid)
  if (book === undefined) {
    book = {
      gen: generation,
      client,
      rev: 0,
      pending: new Map(),
      editedAt: new Map(),
      callbacks: [],
      debounce: null,
      retry: null,
      due: false,
      urgent: false,
      inFlight: false,
      beacons: 0,
      unloadRev: null,
      failures: 0,
    }
    books.set(bid, book)
    listenForUnload(true)
  }
  book.client = client
  return book
}

function clearTimers(book: BookWriter): void {
  if (book.debounce !== null) clearTimeout(book.debounce)
  if (book.retry !== null) clearTimeout(book.retry)
  book.debounce = null
  book.retry = null
}

/** Buffers `update` at a new revision, and returns it. A new change gets a fresh retry budget. */
function record(book: BookWriter, update: BookPrefsUpdate): number {
  book.rev += 1
  for (const field of FIELDS) {
    const value = update[field]
    if (value === undefined) continue
    book.pending.set(field, { value, rev: book.rev })
    book.editedAt.set(field, book.rev)
  }
  clearTimers(book)
  book.due = false
  book.failures = 0
  return book.rev
}

function snapshot(book: BookWriter): Submission {
  const entries = [...book.pending]
  return {
    body: Object.fromEntries(entries.map(([field, e]) => [field, e.value])) as BookPrefsUpdate,
    revs: new Map(entries.map(([field, e]) => [field, e.rev])),
    rev: book.rev,
  }
}

function pump(bid: string, book: BookWriter): void {
  if (!live(book)) return
  if (!book.inFlight && (book.due || book.urgent) && book.pending.size > 0) {
    book.due = false
    book.urgent = false
    book.inFlight = true
    const sub = snapshot(book)
    void putBookPrefs(bid, sub.body).then(
      (prefs) => {
        book.inFlight = false
        confirmed(bid, book, sub, prefs)
      },
      (error: unknown) => {
        book.inFlight = false
        failed(bid, book, sub, error)
      },
    )
  }
  forgetIfIdle(bid, book)
}

function confirmed(bid: string, book: BookWriter, sub: Submission, prefs: BookPrefs): void {
  if (!live(book)) return
  book.failures = 0
  for (const [field, rev] of sub.revs) {
    if (book.pending.get(field)?.rev === rev) book.pending.delete(field)
  }
  if (sub.rev === book.rev) {
    book.client.setQueryData<BookPrefs>(queryKeys.books.prefs(bid), prefs)
    book.client.setQueryData<BookDetail>(queryKeys.books.detail(bid), (old) =>
      old === undefined ? undefined : { ...old, prefs },
    )
  }
  const ready = book.callbacks.filter((c) => c.rev <= sub.rev)
  book.callbacks = book.callbacks.filter((c) => c.rev > sub.rev)
  for (const { rev, onSaved } of ready) {
    onSaved(prefs, new Set(FIELDS.filter((field) => (book.editedAt.get(field) ?? 0) <= rev)))
  }
  pump(bid, book)
}

/** Transport, 5xx, 408 and 429: the same body may well succeed later. */
function isRecoverable(error: unknown): boolean {
  if (!isApiError(error)) return true
  return error.status >= 500 || error.status === 408 || error.status === 429
}

function failed(bid: string, book: BookWriter, sub: Submission, error: unknown): void {
  if (!live(book)) return
  if (isRecoverable(error) && book.failures < PREFS_RETRY_LIMIT) {
    if (book.retry !== null) clearTimeout(book.retry)
    const delay = PREFS_RETRY_BASE_MS * 2 ** book.failures
    book.failures += 1
    book.retry = setTimeout(() => {
      book.retry = null
      book.urgent = true
      pump(bid, book)
    }, delay)
    return
  }
  book.failures = 0
  if (!isRecoverable(error) && !(isApiError(error) && error.status === 401)) {
    // Quarantine: the same body would be refused again. Drop what it carried,
    // unless edited since, so it cannot poison the next write.
    for (const [field, rev] of sub.revs) {
      if (book.pending.get(field)?.rev === rev) book.pending.delete(field)
    }
    book.callbacks = book.callbacks.filter((c) => c.rev > sub.rev)
  }
  // The server kept what it had: take its word back over the optimistic `is_override`.
  void book.client.invalidateQueries({ queryKey: queryKeys.books.prefs(bid) })
  void book.client.invalidateQueries({ queryKey: queryKeys.books.detail(bid) })
  pump(bid, book)
}

function forgetIfIdle(bid: string, book: BookWriter): void {
  const idle =
    book.pending.size === 0 &&
    !book.inFlight &&
    book.beacons === 0 &&
    book.debounce === null &&
    book.retry === null &&
    book.callbacks.length === 0
  if (!idle || books.get(bid) !== book) return
  books.delete(bid)
  if (books.size === 0) listenForUnload(false)
}

let listening = false

function onPageHide(): void {
  flushPrefsForUnload()
}

function onVisibilityChange(): void {
  if (document.visibilityState === 'hidden') flushPrefsForUnload()
}

/** One listener for the module, so a buffer left by an unmounted viewer is still covered. */
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

/** A seg change: buffered, cache patched, sent `PREFS_SAVE_DEBOUNCE_MS` after the last one. */
export function savePrefs(client: QueryClient, bid: string, update: SegPrefs): void {
  const book = writerFor(client, bid)
  record(book, update)
  // The chip reads `is_override`, and any seg write makes it true — the answer
  // the server will give — so the bar does not wait for the write.
  client.setQueryData<BookDetail>(queryKeys.books.detail(bid), (old) =>
    old === undefined ? undefined : { ...old, prefs: { ...old.prefs, ...update, is_override: true } },
  )
  book.debounce = setTimeout(() => {
    book.debounce = null
    book.due = true
    pump(bid, book)
  }, PREFS_SAVE_DEBOUNCE_MS)
}

/** An explicit action (이 권 전용 설정): joins the buffer and goes as soon as the queue allows. */
export function savePrefsNow(
  client: QueryClient,
  bid: string,
  update: BookPrefsUpdate,
  onSaved: PrefsSaved,
): void {
  const book = writerFor(client, bid)
  book.callbacks.push({ rev: record(book, update), onSaved })
  book.urgent = true
  pump(bid, book)
}

/** Leaving the book: what is buffered goes now, in its turn in the queue. */
export function flushPrefs(bid: string): void {
  const book = books.get(bid)
  if (book === undefined || book.pending.size === 0) return
  clearTimers(book)
  book.urgent = true
  pump(bid, book)
}

/**
 * The page is going away: every buffer goes now with `keepalive`, **without**
 * waiting for a request already on the wire — the document may not outlive it.
 */
export function flushPrefsForUnload(): void {
  for (const [bid, book] of books) {
    // Fields stay pending until the answer (a failed write must still be retried); only the duplicate send is skipped.
    if (book.pending.size === 0 || book.unloadRev === book.rev) continue
    clearTimers(book)
    book.due = false
    book.urgent = false
    const sub = snapshot(book)
    book.beacons += 1
    book.unloadRev = sub.rev
    const settle = (): void => {
      book.beacons -= 1
      if (book.unloadRev === sub.rev) book.unloadRev = null
    }
    // `keepalive` so the write outlives the document; a prefs patch is far under its 64 KiB cap.
    void putBookPrefs(bid, sub.body, { keepalive: true }).then(
      (prefs) => {
        settle()
        confirmed(bid, book, sub, prefs)
      },
      (error: unknown) => {
        settle()
        failed(bid, book, sub, error)
      },
    )
  }
}

/**
 * Test seam: forget every book's buffer, queue and timers. Requests already on
 * the wire are not cancelled; the generation makes whatever they settle into a no-op.
 */
export function resetPrefsWriter(): void {
  for (const book of books.values()) clearTimers(book)
  books.clear()
  generation += 1
  listenForUnload(false)
}

/** Test seam: how many books have a writer, and whether the unload listener is on. */
export function prefsWriterState(): { writers: number; listening: boolean } {
  return { writers: books.size, listening }
}

export interface PrefsWriter {
  save: (update: SegPrefs) => void
  saveNow: (update: BookPrefsUpdate, onSaved: PrefsSaved) => void
}

/** The viewer's handle on the writer for `bookId`. The queue itself is module-level. */
export function usePrefsWriter(bookId: string): PrefsWriter {
  const client = useQueryClient()
  const save = useCallback(
    (update: SegPrefs) => {
      savePrefs(client, bookId, update)
    },
    [bookId, client],
  )
  const saveNow = useCallback(
    (update: BookPrefsUpdate, onSaved: PrefsSaved) => {
      savePrefsNow(client, bookId, update, onSaved)
    },
    [bookId, client],
  )

  // Leaving the book — unmount, or 다음 권 changing `bookId` — sends its buffer.
  useEffect(
    () => () => {
      flushPrefs(bookId)
    },
    [bookId],
  )

  return { save, saveNow }
}
