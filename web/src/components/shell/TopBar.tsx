import { ArrowDownNarrowWide, ArrowLeft, LayoutGrid, List, Menu, Search } from 'lucide-react'

import { cn } from '../../lib/cn'
import { commandKeyHint } from '../../lib/platform'
import type { SortKey, ViewMode } from '../../store/ui'
import { Button } from '../ds/Button'
import { Input } from '../ds/Input'
import { Seg, type SegOption } from '../ds/Seg'
import { VisuallyHidden } from '../ds/VisuallyHidden'

/**
 * The top bar (ui-spec §4.2), plus the responsive rules of §7.
 *
 * Two departures from the prototype, both required:
 *  - a hamburger appears below **1024px**, because that is where the full
 *    sidebar stops being on screen: 768–1023 collapses it to a 56px icon rail
 *    whose labels and counts are only reachable through "an overlay drawer from
 *    a hamburger in the top bar" (§7), and below 768 there is no sidebar at all
 *    — it is an off-canvas drawer (D-42). One trigger serves both tiers;
 *  - the bar wraps instead of overflowing. The prototype's fixed row is what
 *    clips at 768 and 400 (`library-list-768.png`,
 *    `library-grid-400-broken.png`). Wrapping keeps `body.scrollWidth <=
 *    clientWidth` at every width from 320 to 1920, which is an acceptance
 *    criterion, and costs one extra row only where there is no room anyway.
 *
 * Sort keys are the **API's** (C-3): `name|mtime|recent|size|books`. The Korean
 * labels are unchanged from the ui-spec catalogue.
 */

const SORT_OPTIONS: readonly { value: SortKey; label: string }[] = [
  { value: 'name', label: '이름' },
  { value: 'mtime', label: '수정일' },
  { value: 'recent', label: '최근 읽은 순' },
  { value: 'size', label: '용량' },
  { value: 'books', label: '권 수' },
]

const VIEW_OPTIONS: readonly SegOption<ViewMode>[] = [
  { value: 'grid', label: '그리드', icon: <LayoutGrid size={13} /> },
  { value: 'list', label: '리스트', icon: <List size={13} /> },
]

export interface TopBarProps {
  /** Series detail only: renders the `라이브러리` button at position 1. */
  showBack?: boolean
  onBack?: () => void
  query: string
  onQueryChange: (query: string) => void
  scanning: boolean
  /** 0..100. Only rendered while `scanning`. */
  scanPercent: number
  sort: SortKey
  onSortChange: (sort: SortKey) => void
  view: ViewMode
  onViewChange: (view: ViewMode) => void
  /** Opens the overlay sidebar; the trigger exists below 1024px (rail + mobile). */
  onOpenDrawer: () => void
  className?: string
}

export function TopBar({
  showBack = false,
  onBack,
  query,
  onQueryChange,
  scanning,
  scanPercent,
  sort,
  onSortChange,
  view,
  onViewChange,
  onOpenDrawer,
  className,
}: TopBarProps) {
  return (
    <div
      className={cn(
        'flex flex-none flex-wrap items-center gap-3 border-b-2 border-rule-strong px-4 py-3',
        className,
      )}
    >
      <Button
        variant="secondary"
        icon
        className="lg:hidden"
        onClick={onOpenDrawer}
        aria-label="라이브러리 탐색 열기"
      >
        <Menu size={16} aria-hidden={true} />
      </Button>

      {showBack && (
        <Button variant="secondary" className="gap-[7px] text-sm" onClick={onBack}>
          <ArrowLeft size={13} aria-hidden={true} />
          라이브러리
        </Button>
      )}

      {/* No trailing spacer: the field itself is the elastic element now, so the
          controls sit against it instead of against the right edge. */}
      <div className="relative min-w-[220px] flex-[1_1_260px] md:max-w-[400px]">
        <span
          aria-hidden="true"
          className="pointer-events-none absolute left-[11px] top-1/2 -translate-y-1/2 text-ink-dim"
        >
          <Search size={15} />
        </span>
        <Input
          type="search"
          name="q"
          value={query}
          onChange={(e) => {
            onQueryChange(e.target.value)
          }}
          placeholder="시리즈 검색 (초성 가능)"
          aria-label="시리즈 검색 (초성 가능)"
          className="pl-[34px] pr-[52px]"
        />
        <span
          aria-hidden="true"
          className="pointer-events-none absolute right-[9px] top-1/2 -translate-y-1/2 border border-rule px-[5px] text-xs tracking-[.04em] text-ink-dim"
        >
          {commandKeyHint()}
        </span>
      </div>

      {scanning && (
        <div className="flex items-center gap-2 text-xs tabular-nums text-ink-muted">
          <span aria-hidden="true" className="h-[2px] w-[96px] bg-fill-track">
            <span className="block h-full bg-accent" style={{ width: `${scanPercent.toString()}%` }} />
          </span>
          {`${scanPercent.toString()}%`}
        </div>
      )}

      <div className="flex items-center gap-2">
        <span className="hidden items-center gap-[6px] text-3xs uppercase tracking-[.1em] text-ink-dim md:inline-flex">
          <ArrowDownNarrowWide size={13} aria-hidden={true} />
          Sort
        </span>
        <select
          className="input w-auto cursor-pointer text-base md:min-w-[132px]"
          name="sort"
          value={sort}
          aria-label="정렬"
          onChange={(e) => {
            onSortChange(e.target.value as SortKey)
          }}
        >
          {SORT_OPTIONS.map((o) => (
            <option key={o.value} value={o.value}>
              {o.label}
            </option>
          ))}
        </select>
      </div>

      <Seg
        value={view}
        options={VIEW_OPTIONS}
        onChange={onViewChange}
        aria-label="보기 방식"
        className="flex-none whitespace-nowrap"
      />
      <VisuallyHidden>{view === 'grid' ? '그리드 보기' : '리스트 보기'}</VisuallyHidden>
    </div>
  )
}
