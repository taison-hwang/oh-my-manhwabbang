import { CornerDownLeft, Search } from 'lucide-react'
import { useEffect, useMemo, useState, type KeyboardEvent } from 'react'
import { useNavigate } from 'react-router-dom'

import { useContinue, useSeriesList } from '../../api/queries'
import { Dialog } from '../../components/ds/Dialog'
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
        {/* Deliberately **not** `.input` (E-42). ui-spec §8.4 describes this
            field as `border:0; border-bottom:2px; background:transparent` —
            i.e. a big underlined search line, not a field — and `.input` is now
            the opposite of that: a cream well with an inset shadow and the
            absolute cream ink. It shipped as `.input` plus four overrides that
            cancelled the class, and the `bg-transparent` one is what made it a
            defect: a Tailwind `bg-*` lands after `@layer components`, so it
            removed the well and left `--on-control` — the cream set's ink — on the dialog's
            own surface — **1.40:1** in the dark theme. Spelling out what this
            field needs is smaller than fighting a class whose every declaration
            has to be undone.
            What the class was still carrying, restated here:
              - `w-full` + `pl-[46px] pr-4` — its horizontal padding, which is
                not optional: Tailwind preflight zeroes an input's padding, and
                the vertical half is moot against `min-h-[56px]`,
              - `text-ink` — the theme-flipping ink, because the ground here is
                `--color-surface`, not cream. The icon beside it keeps
                `text-ink-dim` for the same reason,
              - `placeholder:text-ink-dim` and `caret-accent-text`,
              - the ≥44px touch target of NFR-CMP-002: `min-h-[56px]` clears it
                at every width, which `.input` only did below 768 via a media
                query that no longer applies here,
              - focus: the base layer's hot `:focus-visible` outline is what
                marks this field now, pulled **inside** the field. The field is
                the full width of a `p-0` panel and its own corners are square,
                so any outset ring — the base layer's `outline-offset: 2px`, and
                equally the `0` this used to carry — is drawn past the dialog's
                6px radius and hangs a square red corner off it. A render says
                so; the earlier note here claimed `0` was flush and it is not,
                the same mistake `.input`'s own comment made. `-2px` puts the
                ring on the field's own edge, which is the only offset that
                cannot escape the panel. */}
        <input
          type="text"
          aria-label="시리즈로 이동…"
          placeholder="시리즈로 이동…"
          className="min-h-[56px] w-full border-b-2 border-rule-strong bg-transparent pl-[46px] pr-4 text-[17px] text-ink caret-accent-text placeholder:text-ink-dim focus-visible:[outline-offset:-2px]"
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
