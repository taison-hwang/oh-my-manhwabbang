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
        // E-32: the 2px bottom rule becomes a surface + an elevation. The bar
        // has to be *positioned* and stacked for that shadow to fall on the
        // content band below it — an unpositioned sibling's box-shadow is
        // painted before the next sibling's background, so the bar would have
        // cast onto nothing.
        'relative z-sticky flex flex-none flex-wrap items-center gap-3 bg-surface px-4 py-3 shadow-md',
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
          controls sit against it instead of against the right edge.

          ## Both decorations are *inside* the field, so both take cream ink

          The icon and the ⌘K hint are siblings positioned over the `.input`,
          not children of it, so nothing about the class reaches them — but the
          reader sees them on the field's surface, and E-42 makes that surface
          an absolute cream in every theme. `--ink-dim` is a pale teal in the dark
          theme, i.e. **1.50:1** on that cream: the two marks would vanish in
          exactly the theme where the field looks most like a control.
          `--on-control-dim` is the absolute dim ink for it — 5.90 washed on the
          fill — and it is the same ink the field's own placeholder uses, which
          is what these two should agree with. The chip below takes the *full*
          absolute ink instead, for a reason measured on the render rather than
          on the tokens: see its own note. */}
      <div className="relative min-w-[220px] flex-[1_1_260px] md:max-w-[400px]">
        <span
          aria-hidden="true"
          className="pointer-events-none absolute left-[11px] top-1/2 -translate-y-1/2 text-on-control-dim"
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
        {/* The hint is a **recessed chip**, not an outlined one. `border-rule`
            was a theme-relative hairline drawn on a fill that no longer flips
            with it: barely visible in light, a hard dark line in dark, so the
            same chip read as two different things. `--control-well` is the
            recess the segmented track uses, and the well is only 1.04:1 against
            the fill — the *shadow* is what makes it a dent, so the inset comes
            with the fill or the chip is invisible. Both tokens are absolute, so
            this now looks identical in both themes, which a hint sitting inside
            an absolute control should.

            **The ink is `--on-control`, not the dim one, and the reason is that
            the token pair was the wrong thing to measure.** `--shadow-control-
            inset` is sized for a 36px control: 3px offset plus 7px blur, so it
            eats 10px in from each edge. This chip is 14.8px tall, so the lobes
            meet in the middle and no pixel of it is actually `--control-well` —
            the top-left is ochre, the bottom-right near-white. Against the real
            top-left pixels the dim ink measures **4.55 washed and 4.44 at peak
            grain**, i.e. under AA at 11px, while the declared pair reads 5.65
            and looks fine. A shadow moved the floor and a pair scanner cannot
            see shadows. The full ink clears it everywhere on the gradient
            (10.4 at the darkest pixel), and a keycap is allowed to be a keycap. */}
        <span
          aria-hidden="true"
          className="pointer-events-none absolute right-[9px] top-1/2 -translate-y-1/2 rounded-sm bg-control-well px-[5px] text-xs tracking-[.04em] text-on-control shadow-control-inset"
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
