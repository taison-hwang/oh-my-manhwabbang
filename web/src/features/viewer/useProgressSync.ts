/**
 * Reading progress (FR-VWR-009, FR-STT-001, FR-VWR-012).
 *
 * A page turn is cheap; `PUT /api/books/{bid}/progress` is not. The write is
 * debounced to 1 s by `useSaveProgress` (WP-06), so flicking through twenty
 * pages costs one request instead of twenty — and the pending write is flushed
 * on unmount, on `visibilitychange` and on `pagehide`, which are the three ways
 * a reader actually leaves a book. Closing the tab mid-debounce must not lose
 * the page.
 *
 * `completed` is left off the body on a normal page turn so the server applies
 * its own rule (`page === page_count` ⇒ completed, arch §7.6) — that is
 * FR-VWR-012's automatic half. `setCompleted` is the manual half, and it
 * flushes immediately because it is an explicit user action.
 */

import { useCallback, useEffect } from 'react'

import { useSaveProgress, type SaveProgressApi } from '../../api/queries'

export interface ProgressSyncOptions {
  /** Overridable for tests; production is `PROGRESS_DEBOUNCE_MS` (1 s). */
  debounceMs?: number
  /** `false` until the book is loaded, so page 1 is not written on mount. */
  enabled?: boolean
}

export interface ProgressSyncApi {
  /** Sends the pending write now. */
  flush: () => void
  /** FR-VWR-012 manual toggle: records the current page with an explicit flag. */
  setCompleted: (completed: boolean) => void
  /**
   * **E-45 §2** — reports that `파일이 변경되었습니다` was shown for its full
   * lifetime, at whatever page the reader has reached by then.
   *
   * Note what it is *not* attached to: the effect below writes a page whenever
   * the book loads, with the reader having done nothing at all. That write is
   * why the notice looked like it lasted a second, and it can never be read as
   * consent to move the baseline.
   */
  acknowledgeStale: () => void
  mutation: SaveProgressApi['mutation']
}

export function useProgressSync(
  bookId: string,
  page: number,
  pageCount: number,
  options: ProgressSyncOptions = {},
): ProgressSyncApi {
  const { debounceMs, enabled = true } = options
  const {
    save,
    acknowledgeStale: ack,
    flush,
    mutation,
  } = useSaveProgress(bookId, debounceMs === undefined ? {} : { debounceMs })

  useEffect(() => {
    if (!enabled || pageCount <= 0 || page < 1) return
    save(page)
  }, [enabled, page, pageCount, save])

  useEffect(() => {
    if (!enabled) return undefined
    const onHide = (): void => {
      // `visibilitychange` fires on both directions; only the way out matters.
      if (document.visibilityState === 'hidden') flush()
    }
    const onPageHide = (): void => {
      flush()
    }
    document.addEventListener('visibilitychange', onHide)
    window.addEventListener('pagehide', onPageHide)
    return () => {
      document.removeEventListener('visibilitychange', onHide)
      window.removeEventListener('pagehide', onPageHide)
    }
  }, [enabled, flush])

  const setCompleted = useCallback(
    (completed: boolean) => {
      if (pageCount <= 0) return
      save(Math.max(1, Math.min(pageCount, page)), completed)
      flush()
    },
    [flush, page, pageCount, save],
  )

  const acknowledgeStale = useCallback(() => {
    if (pageCount <= 0) return
    ack(Math.max(1, Math.min(pageCount, page)))
  }, [ack, page, pageCount])

  return { flush, setCompleted, acknowledgeStale, mutation }
}
