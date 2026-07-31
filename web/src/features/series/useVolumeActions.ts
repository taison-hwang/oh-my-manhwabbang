/**
 * FR-VWR-012, manual half: mark a volume 읽음 / 안읽음 from the series screen.
 *
 * ui-spec gap #7 — the prototype has no such control at all, so this is built
 * rather than ported. The automatic half (reaching the last page) is the
 * viewer's, WP-11.
 *
 * `useSaveProgress` is the one writer for `PUT /api/books/{bid}/progress`
 * (WP-06). Its 1 s debounce exists for page turns; an explicit click is not a
 * page turn, so the hook is created with `debounceMs: 0` and flushed
 * immediately — the write leaves on the click, not a second later, and the row
 * cannot unmount with it still pending.
 *
 * **Cache invalidation.** The two writers are not symmetric. `useDeleteProgress`
 * invalidates `queryKeys.series.all`; `useSaveProgress` does not — it is written
 * for the viewer, where the only reader of a page turn is `books.detail`, and
 * invalidating the whole series tree on every debounced page would refetch the
 * detail payload once a second. The series screen reads `series.detail(sid)`, so
 * without the effect below a successful `읽음 표시` would leave the row showing
 * `—` until something else happened to refetch: the write lands, the screen lies.
 * The effect is here rather than in `useSaveProgress` precisely because it is the
 * *click* that needs it, not the page turn (FR-VWR-012).
 */

import { useEffect } from 'react'

import { useQueryClient } from '@tanstack/react-query'

import { queryKeys, useDeleteProgress, useSaveProgress } from '../../api/queries'
import type { BookSummary } from '../../api/types'

export interface VolumeReadToggle {
  /** True when the volume is recorded as 완독. */
  completed: boolean
  /** `읽음 표시` or `안읽음`, per the current state. */
  label: string
  /** Marks completed, or clears progress entirely when it already is. */
  toggle: () => void
  pending: boolean
}

export const MARK_READ_LABEL = '읽음 표시'
export const MARK_UNREAD_LABEL = '안읽음'

export function useVolumeReadToggle(book: BookSummary): VolumeReadToggle {
  const { save, flush, mutation } = useSaveProgress(book.id, { debounceMs: 0 })
  const remove = useDeleteProgress()
  const queryClient = useQueryClient()

  // `submittedAt` is the mutation's start timestamp: it changes on every new
  // write, so a second 읽음 표시 re-runs this even though `isSuccess` never went
  // back to false in between.
  const { isSuccess, submittedAt } = mutation
  useEffect(() => {
    if (!isSuccess || submittedAt === 0) return
    void queryClient.invalidateQueries({ queryKey: queryKeys.series.all })
  }, [isSuccess, submittedAt, queryClient])

  const completed = book.progress?.completed === true

  return {
    completed,
    label: completed ? MARK_UNREAD_LABEL : MARK_READ_LABEL,
    pending: mutation.isPending || remove.isPending,
    toggle: () => {
      if (completed) {
        remove.mutate(book.id)
        return
      }
      // `completed` is sent explicitly rather than relying on the server's
      // "page === page_count ⇒ completed" default, so a volume whose indexed
      // page count later changes stays marked read.
      save(Math.max(1, book.page_count), true)
      flush()
    },
  }
}
