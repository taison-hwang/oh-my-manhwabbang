import { useNavigate, useParams } from 'react-router-dom'

import { useRescanSeries, useScanStatus, useSeries, useSettings } from '../../api/queries'
import type { BookSummary, ReadingDir } from '../../api/types'
import { EmptyState } from '../../components/ds/EmptyState'
import { Skeleton } from '../../components/ds/Skeleton'
import { formatVolumeCount } from '../../lib/format'
import { useSeriesDirStore } from '../../store/seriesDir'
import { useUiStore } from '../../store/ui'
import { SeriesHeader } from './SeriesHeader'
import { VolumeGrid } from './VolumeGrid'
import { VolumeList } from './VolumeList'
import { firstOpenableBook, resumeTarget } from './volume'

/**
 * Screen 2 — series detail (prd UI-002, FR-LIB-009, ui-spec §5).
 *
 * Route element for `/series/:sid`. Grid/list is the **same** top-bar toggle the
 * library uses (`store/ui.ts` `view`), because ui-spec §5 says the toggle "now
 * switches the volume list" — one control, two screens.
 */
export function SeriesDetailPage() {
  const { sid } = useParams()
  const seriesId = sid ?? ''
  const navigate = useNavigate()

  const series = useSeries(seriesId, { enabled: seriesId !== '' })
  const settings = useSettings()
  const scan = useScanStatus()
  const rescan = useRescanSeries()

  const view = useUiStore((s) => s.view)
  const dirBySeries = useSeriesDirStore((s) => s.bySeries)
  const setSeriesDir = useSeriesDirStore((s) => s.setSeriesDir)

  const openBook = (book: BookSummary): void => {
    const page = Math.max(1, book.progress?.last_page ?? 1)
    void navigate(`/series/${seriesId}/books/${book.id}?page=${String(page)}`)
  }

  if (series.isPending) {
    return (
      <div className="flex h-full flex-col gap-6 overflow-auto p-4">
        <div className="flex gap-6">
          <Skeleton variant="cover" className="h-[264px] w-[176px] flex-none" />
          <div className="flex min-w-0 flex-1 flex-col gap-3">
            <Skeleton variant="line" width="52%" />
            <Skeleton variant="line" width="78%" index={1} />
            <Skeleton variant="line" width="36%" index={2} />
          </div>
        </div>
      </div>
    )
  }

  if (series.isError) {
    return (
      <div className="flex h-full flex-col overflow-auto p-4">
        <EmptyState title="화면을 열 수 없습니다" body={series.error.message} />
      </div>
    )
  }

  const detail = series.data
  // C-9 / D-35: the `.seg` seeds books opened from this screen and is stored in
  // localStorage. The persisted direction is per book, on the server.
  const dir: ReadingDir =
    dirBySeries[detail.id] ?? settings.data?.reading_direction ?? 'ltr'

  const resume = resumeTarget(detail.books, detail.progress)
  const first = firstOpenableBook(detail.books)

  const notice = rescan.isError ? rescan.error.message : null

  return (
    <div className="flex h-full flex-col overflow-auto">
      <SeriesHeader
        series={detail}
        dir={dir}
        onDirChange={(next) => {
          setSeriesDir(detail.id, next)
        }}
        onResume={() => {
          if (resume === null) return
          void navigate(
            `/series/${detail.id}/books/${resume.book.id}?page=${String(resume.page)}`,
          )
        }}
        onReadFirst={() => {
          if (first === null) return
          void navigate(`/series/${detail.id}/books/${first.id}?page=1`)
        }}
        onRescan={() => {
          rescan.mutate(detail.id)
        }}
        scanning={scan.data !== undefined && scan.data.state !== 'idle'}
        notice={notice}
        // Ruling E-14: `error` means the series has books but not one the reader
        // can open, and `SeriesDetail.error` is non-null whenever
        // `status !== "ok"` (arch §7.3). Surfacing it here is what stops such a
        // series presenting as healthy — the whole basis of the ruling.
        // `empty` deliberately does NOT raise the banner: under E-14 it means
        // "no books at all", i.e. D-7's five text-only directories, which
        // D-29/E-5 grey out through the body's EmptyState rather than alarm
        // about.
        error={detail.status === 'error' ? detail.error : null}
        // `resumeTarget` falls back to `firstOpenableBook`, so both actions are
        // dead exactly when there is no openable volume.
        canRead={first !== null}
      />

      {/* E-32 removes the underline here as it does under the library's scope
          header: the hero above is a card now and the volume view below is
          another, so the label sits in the space between them. */}
      <div className="flex items-baseline gap-3 px-4 pb-3 pt-2">
        <h6>권 목록</h6>
        <span className="text-xs tabular-nums text-ink-dim">
          {formatVolumeCount(detail.books.length)}
        </span>
      </div>

      {detail.books.length === 0 ? (
        // D-29 / E-5: a series whose directory holds no readable book is listed,
        // not hidden. The wording matches the `empty` volume badge.
        <div className="p-4">
          <EmptyState
            title="비어 있음"
            {...(detail.error === null ? {} : { body: detail.error })}
          />
        </div>
      ) : view === 'list' ? (
        <VolumeList books={detail.books} onOpen={openBook} />
      ) : (
        <VolumeGrid books={detail.books} onOpen={openBook} />
      )}
    </div>
  )
}
