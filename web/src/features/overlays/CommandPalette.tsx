import { CornerDownLeft, Search } from 'lucide-react'
import { useEffect, useMemo, useState, type KeyboardEvent } from 'react'
import { useNavigate } from 'react-router-dom'

import { useContinue, useSeriesList } from '../../api/queries'
import { Dialog } from '../../components/ds/Dialog'
import { Input } from '../../components/ds/Input'
import { cn } from '../../lib/cn'
import { matchRange } from '../../lib/chosung'
import { formatBytes, formatContinueCounter, formatVolumeCount } from '../../lib/format'

/**
 * The command palette (FR-LIB-011, ui-spec §8.4).
 *
 * **The search is the server's** (C-10 / D-34). ui-spec §8.4 ports a
 * client-side `chosung()` filter over the loaded list, but FR-LIB-007 means the
 * client never holds all 963–10 000 series, so a client filter is wrong by
 * construction. `GET /api/series?q=&limit=8`, debounced 150 ms; `chosung()`
 * survives only to *highlight* which part of a title matched.
 *
 * An empty query lists `/api/continue`-derived recents, deduplicated by series
 * — the same book can be the most recent twice over.
 */
export const PALETTE_DEBOUNCE_MS = 150
export const PALETTE_LIMIT = 8

export interface CommandPaletteProps {
  open: boolean
  query: string
  onQueryChange: (query: string) => void
  onClose: () => void
}

interface PaletteItem {
  seriesId: string
  title: string
  sub: string
}

interface HighlightProps {
  title: string
  query: string
}

/** FR-LIB-006: shows *which* span the (possibly 초성) query matched. */
function Highlight({ title, query }: HighlightProps) {
  const range = matchRange(title, query)
  if (range === null) return <>{title}</>
  const chars = Array.from(title)
  const [start, end] = range
  return (
    <>
      {chars.slice(0, start).join('')}
      <mark className="bg-transparent text-accent-text">{chars.slice(start, end).join('')}</mark>
      {chars.slice(end).join('')}
    </>
  )
}

export function CommandPalette({ open, query, onQueryChange, onClose }: CommandPaletteProps) {
  const navigate = useNavigate()
  const [debounced, setDebounced] = useState(query)
  const [selected, setSelected] = useState(0)

  useEffect(() => {
    const timer = setTimeout(() => {
      setDebounced(query)
    }, PALETTE_DEBOUNCE_MS)
    return () => {
      clearTimeout(timer)
    }
  }, [query])

  const searching = debounced.trim() !== ''

  const search = useSeriesList(
    { q: debounced.trim(), limit: PALETTE_LIMIT },
    { enabled: open && searching },
  )
  const recents = useContinue(PALETTE_LIMIT, { enabled: open && !searching })

  const items = useMemo<readonly PaletteItem[]>(() => {
    if (searching) {
      return (search.data?.items ?? []).map((series) => ({
        seriesId: series.id,
        title: series.name,
        sub: `${formatVolumeCount(series.book_count)} · ${formatBytes(series.total_bytes)}`,
      }))
    }
    const seen = new Set<string>()
    const out: PaletteItem[] = []
    for (const item of recents.data?.items ?? []) {
      if (seen.has(item.series_id)) continue
      seen.add(item.series_id)
      out.push({
        seriesId: item.series_id,
        title: item.series_name,
        sub: `${item.book.name} · ${formatContinueCounter(item.progress.last_page, item.progress.page_count)}`,
      })
    }
    return out.slice(0, PALETTE_LIMIT)
  }, [searching, search.data, recents.data])

  // Row 0 is preselected, and stays selected as results change underneath.
  useEffect(() => {
    setSelected(0)
  }, [debounced, items.length])

  const pick = (item: PaletteItem): void => {
    onClose()
    void navigate(`/series/${item.seriesId}`)
  }

  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>): void => {
    if (items.length === 0) return
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      setSelected((i) => (i + 1) % items.length)
      return
    }
    if (e.key === 'ArrowUp') {
      e.preventDefault()
      setSelected((i) => (i - 1 + items.length) % items.length)
      return
    }
    if (e.key === 'Enter') {
      e.preventDefault()
      const item = items[selected]
      if (item !== undefined) pick(item)
    }
  }

  return (
    <Dialog
      open={open}
      onClose={onClose}
      align="top"
      width="min(620px, 100%)"
      panelClassName="gap-0 p-0"
    >
      {/* No `autoFocus`: React focuses an autofocused node during commit, i.e.
          *before* `Dialog`'s effect captures `document.activeElement` as the
          opener — which would make the palette restore focus to its own input
          on close, and the caller's trigger would never get it back. `Dialog`
          focuses the first focusable itself, which is this field. */}
      <div className="relative flex items-center">
        <span
          aria-hidden="true"
          className="pointer-events-none absolute left-4 text-ink-dim"
        >
          <Search size={17} />
        </span>
        <Input
          aria-label="시리즈로 이동…"
          placeholder="시리즈로 이동…"
          className="min-h-[56px] border-0 border-b-2 border-rule-strong bg-transparent pl-[46px] text-[17px]"
          value={query}
          onChange={(e) => {
            onQueryChange(e.target.value)
          }}
          onKeyDown={onKeyDown}
        />
      </div>

      <div className="max-h-[52vh] overflow-y-auto" role="listbox" aria-label="검색 결과">
        <div className="px-4 pb-1 pt-3 text-3xs uppercase tracking-[.12em] text-ink-dim">
          {searching ? '검색 결과' : '최근 항목'}
        </div>
        {items.length === 0 ? (
          <p className="px-4 py-6 text-ink-dim">검색 결과 없음</p>
        ) : (
          items.map((item, index) => (
            <button
              key={item.seriesId}
              type="button"
              role="option"
              aria-selected={index === selected}
              className={cn(
                'flex w-full items-center gap-3 border-l-[3px] px-4 py-[7px] text-left hover:bg-hover-tint',
                index === selected
                  ? 'border-l-accent bg-nav-active'
                  : 'border-l-transparent bg-transparent',
              )}
              onMouseEnter={() => {
                setSelected(index)
              }}
              onClick={() => {
                pick(item)
              }}
            >
              <span className="min-w-0 flex-1 truncate whitespace-nowrap text-md">
                <Highlight title={item.title} query={debounced.trim()} />
              </span>
              <span className="flex-none text-xs tabular-nums text-ink-dim">{item.sub}</span>
            </button>
          ))
        )}
      </div>

      <div className="flex flex-wrap gap-4 whitespace-nowrap border-t-2 border-rule-strong px-4 py-2 text-xs text-ink-dim">
        <span className="flex items-center gap-[5px]">
          <CornerDownLeft size={12} aria-hidden={true} />
          열기
        </span>
        <span>esc 닫기</span>
        <span>초성 검색 ㅎㅌㅂㅅㅋ</span>
      </div>
    </Dialog>
  )
}
