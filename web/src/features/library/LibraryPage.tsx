import { SearchX } from 'lucide-react'
import { useCallback, useEffect } from 'react'
import { useNavigate } from 'react-router-dom'

import { useSettings } from '../../api/queries'
import type { ContinueItem, ID, SeriesSummary } from '../../api/types'
import { EmptyState } from '../../components/ds/EmptyState'
import { useUiStore } from '../../store/ui'
import { ContinueRow } from './ContinueRow'
import { GridSkeleton } from './GridSkeleton'
import { Onboarding } from './Onboarding'
import { SectionHeader } from './SectionHeader'
import { SeriesGrid } from './SeriesGrid'
import { SeriesList } from './SeriesList'
import { useLibrary, useLibrarySettingsSync } from './useLibrary'

/**
 * Screen 1 — Home / Library (ui-spec §4, FR-LIB-001 … FR-LIB-010).
 *
 * The screen is four stacked bands, only the last of which scrolls:
 *
 *   이어보기 (hidden when empty)  →  section header  →  skeleton | empty | grid | list
 *
 * Grid and list are **co-equal** (design.md principle 1): same query, same
 * virtualisation, same sort affordances, and the choice is sticky through
 * `settings.library_view` (FR-LIB-002). Neither is a fallback for the other —
 * in a collection whose filenames carry `1~23(완)`, the dense list is as much
 * the product as the covers are.
 *
 * Everything the screen reads comes from the WP-06 query hooks; everything it
 * remembers lives in `store/ui.ts`. The one place the two meet is
 * `useLibrarySettingsSync`, which is the A-5 write-back.
 *
 * The band order below the section header is deliberate: **failure first**, then
 * empty, then data. A 500 from `/api/series` produces `items: []` exactly like a
 * genuinely empty scope does, so branching on `items.length` first renders a
 * server error as a library with nothing in it — and, worse, labels it with copy
 * that asserts a search the user never ran.
 */
export function LibraryPage() {
  const navigate = useNavigate()
  const library = useLibrary()
  useLibrarySettingsSync()

  // `Settings.server.config_path` (amendment A-10) and
  // `.root_editing_enabled` (amendment A-11) for the onboarding screen. The
  // same query `useLibrarySettingsSync` already runs, so this costs no request;
  // it is read here rather than inside `Onboarding` because the empty state is
  // a presentational component and stays one.
  const settings = useSettings()
  const configPath = settings.data?.server.config_path
  // `false` while the payload is in flight, which is the safe direction: the
  // C-5 screen is correct for every server, and one request later the screen
  // re-renders with the capability if there is one.
  const rootEditingEnabled = settings.data?.server.root_editing_enabled === true

  const setQuery = useUiStore((s) => s.setQuery)
  const openOverlay = useUiStore((s) => s.openOverlay)

  /**
   * E-34 §2 — the viewer's 라이브러리 button leaves a series id here, and
   * whichever of the two surfaces is mounted scrolls to it and focuses it.
   *
   * The screen only forwards it. Grid and list are windowed differently (rows of
   * `n` versus one row per series), so the index arithmetic belongs to each of
   * them; what belongs here is clearing the instruction once it has been acted
   * on, which is `store/ui.ts`'s state and not theirs.
   */
  const revealSeries = useUiStore((s) => s.revealSeries)
  const setRevealSeries = useUiStore((s) => s.setRevealSeries)
  const onRevealed = useCallback(() => {
    setRevealSeries(null)
  }, [setRevealSeries])

  /**
   * **The instruction outlives the pages it needs, so the screen has to fetch
   * them back.**
   *
   * Both surfaces locate the series by its index in `items`, and `items` is
   * whatever `useSeriesListInfinite` happens to be holding. That cache is
   * **transient by construction**: `main.tsx` sets no `gcTime`, so react-query's
   * default of five minutes applies, and the library's query has no observer at
   * all while the reader is inside a book. **Any reading session longer than
   * five minutes therefore collects it**, and the library remounts holding one
   * page of 60 — measured on the real collection: parked 40 931px down with 14
   * pages loaded, five and a half minutes in the viewer, back to `scrollTop: 0`
   * with `GET /api/series?offset=0&limit=60` as the only list request and the
   * series nowhere in the document.
   *
   * The reveal did exactly what E-34 §2 tells it to in that situation — `index
   * === -1`, stay armed, steal no focus — and the reader still landed at the top
   * of the shelf. Arming the instruction (`App.tsx`, `ViewerPage.tsx`) is not
   * enough on its own: **something has to make the series reachable**, and only
   * this screen owns the pagination.
   *
   * So while the instruction is armed and unsatisfied, page forward. It
   * terminates on the two conditions that exhaust the question:
   *
   *  - the series appears in `items` — the surfaces take it from there;
   *  - `hasNextPage` goes false — the whole filtered list has now been read and
   *    the series is genuinely not in it.
   *
   * **The bound is one pass over the current filter, once per instruction**, and
   * in the ordinary case it is far less: the reader is returning to a place they
   * had already paged to, so the pass stops exactly where they were. The E-34
   * note that called this "unbounded" was reasoning about *chasing* — refetching
   * with no terminating condition — and about a reader whose `scope`/`q` exclude
   * the series, which is the `hasNextPage` case above and costs one pass of a
   * list the filter has already narrowed. `loadMore` is idempotent while a page
   * is in flight (`useLibrary`), so this cannot stack requests.
   *
   * What it deliberately does **not** do is clear the instruction when the list
   * is exhausted. E-34 §1 keeps a series that is outside the reader's filter
   * armed rather than widening the filter to find it, and that ruling is about
   * the reader's `scope`, not about how many pages have been fetched.
   */
  const { items: libraryItems, hasNextPage, loadMore } = library
  useEffect(() => {
    if (revealSeries === null || !hasNextPage) return
    if (libraryItems.some((series) => series.id === revealSeries)) return
    loadMore()
  }, [revealSeries, libraryItems, hasNextPage, loadMore])

  const openSeries = useCallback(
    (sid: ID) => {
      void navigate(`/series/${sid}`)
    },
    [navigate],
  )

  /**
   * "이어 읽기" from a card. A series knows the book to resume
   * (`progress.last_book_id`); with nothing started there is no book id to open,
   * so the detail screen — which lists the volumes — is the honest destination.
   */
  const resumeSeries = useCallback(
    (series: SeriesSummary) => {
      const bid = series.progress.last_book_id
      if (bid === null) {
        void navigate(`/series/${series.id}`)
        return
      }
      const page = series.progress.last_page ?? 1
      void navigate(`/series/${series.id}/books/${bid}?page=${page.toString()}`)
    },
    [navigate],
  )

  const resumeBook = useCallback(
    (item: ContinueItem) => {
      const page = item.progress.last_page
      void navigate(`/series/${item.series_id}/books/${item.book.id}?page=${page.toString()}`)
    },
    [navigate],
  )

  const openSettings = useCallback(() => {
    openOverlay('settings')
  }, [openOverlay])

  // "No roots" is only knowable once /api/roots has answered; onboarding
  // replaces the whole screen (the shell drops its chrome for it, ui-spec §4.6).
  if (library.rootsLoaded && library.rootCount === 0) {
    return (
      <Onboarding
        onOpenSettings={openSettings}
        rootEditingEnabled={rootEditingEnabled}
        {...(configPath === undefined ? {} : { configPath })}
      />
    )
  }

  // The *debounced* query is the one that produced the rows on screen, so it is
  // the one that decides whether "no rows" means "no matches" or "nothing here".
  const searching = library.query.trim() !== ''

  return (
    <div className="flex h-full min-h-0 flex-col">
      <ContinueRow suppressed={library.isLoading} onResume={resumeBook} />
      <SectionHeader label={library.scopeName} count={library.total} />

      {library.isLoading ? (
        <div className="min-h-0 flex-1 overflow-y-auto" style={{ scrollbarGutter: 'stable' }}>
          <GridSkeleton variant={library.view} />
        </div>
      ) : library.isError ? (
        <div className="min-h-0 flex-1 overflow-y-auto p-4" data-testid="library-error">
          {/* ui-spec §9 has no catalogue entry for a failed library fetch — the
              prototype has no error state for this screen at all — so `다시
              시도` is taken verbatim from the catalogue's `retry` and the two
              sentences around it are new copy. Flagged for the orchestrator in
              the WP-09 repair report. */}
          <EmptyState
            title="목록을 불러오지 못했습니다"
            body="서버에 연결하지 못했습니다. 잠시 후 다시 시도하세요."
            action={{ label: '다시 시도', onClick: library.retry }}
          />
        </div>
      ) : library.items.length === 0 ? (
        <div className="min-h-0 flex-1 overflow-y-auto p-4">
          {/* The band between two 2px rules *is* the design (ui-spec §4.5).
              With no query there was no search, so it cannot have had no
              results: an empty 완독 or 읽는 중 scope on a fresh library says so
              instead, and has nothing to clear. */}
          {searching ? (
            <EmptyState
              icon={<SearchX size={28} />}
              title="검색 결과 없음"
              body="초성 검색도 지원합니다. 다른 표기를 시도해 보세요."
              action={{
                label: '검색 지우기',
                onClick: () => {
                  setQuery('')
                },
              }}
            />
          ) : (
            <EmptyState title="시리즈가 없습니다" />
          )}
        </div>
      ) : library.view === 'grid' ? (
        <SeriesGrid
          items={library.items}
          query={library.query}
          revealSeries={revealSeries}
          onRevealed={onRevealed}
          onOpen={openSeries}
          onResume={resumeSeries}
          onEndReached={library.loadMore}
        />
      ) : (
        <SeriesList
          items={library.items}
          query={library.query}
          revealSeries={revealSeries}
          onRevealed={onRevealed}
          onOpen={openSeries}
          onEndReached={library.loadMore}
        />
      )}
    </div>
  )
}
