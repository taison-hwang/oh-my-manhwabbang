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
        {/* E-46 turns the hint back into an **outlined chip**, and the reason
            the outline works now is the reason it did not before.

            E-42 made it a recess because `border-rule` was a theme-relative
            hairline drawn on a fill that no longer flipped with it: barely
            visible in light, a hard dark line in dark, so the same chip read as
            two different things in the two themes. `--control-border` is an
            absolute, like the fill it is drawn on, so that split cannot happen
            — the chip looks identical in both themes, which is what a hint
            sitting inside an absolute control should do.

            The recess also carried a defect worth not re-introducing.
            `--shadow-control-inset` is sized for a 36px control (3px offset,
            7px blur) and this chip is 14.8px tall, so the two lobes met in the
            middle and no pixel of it was actually `--control-well`: the
            top-left was ochre and the bottom-right near-white. Against the real
            top-left pixels the dim ink measured 4.55 washed — under AA — while
            the declared pair read 5.65 and looked fine. A flat 1px box has one
            ground and the declared pair is the real one, so that whole class of
            gap closes with it.

            **The ink is `--on-control-dim` and not the prototype's
            neutral-600.** On this field neutral-600 is 4.60 washed and **4.39
            at peak grain**, i.e. under AA at 11px — the same measured refusal
            E-32 §4 made of three of its own prototype's colours. The dim
            control ink is 6.37 washed with 5.97 at peak.

            `font-ui`, because a keycap is a keycap: the prototype sends `.kbd`
            to the sans for the same reason it sends every numeral there. */}
        <span
          aria-hidden="true"
          className="pointer-events-none absolute right-[9px] top-1/2 -translate-y-1/2 border border-control-border px-[5px] font-ui text-xs tracking-[.04em] text-on-control-dim"
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

      {/* **The sort group and the view toggle sit against the right edge**, and
          they are **one box** so that they stay together while doing it.

          `ml-auto` is what moves them: it eats the slack the search field leaves
          once the field has hit its 400px cap, so the controls travel with the
          right edge instead of trailing the field across an otherwise empty bar.
          The field keeps `flex-[1_1_260px]` — it still takes the space it is
          entitled to; what changes is where the *leftover* goes.

          The wrapper is not tidiness. Put `ml-auto` on the sort group alone and
          the two controls are independent flex items of a **wrapping** bar: at
          768 the sort group ends line 1 flush right and the toggle starts line 2
          flush *left*, 520px from the edge its sibling is pinned to (measured).
          Two `ml-auto`s are worse — the free space splits between them and the
          pair comes apart. One box with its own `flex-wrap` and `justify-end`
          keeps them adjacent at every width and right-aligned on whatever line
          they land on, including when the box itself wraps internally at 320.

          The scan indicator, when it is on, stays with the field on the left —
          it is a report about the library, not a control. */}
      <div className="ml-auto flex flex-wrap items-center justify-end gap-3">
        <div className="flex items-center gap-2">
          <span className="hidden items-center gap-[6px] font-ui text-3xs uppercase tracking-[.1em] text-ink-dim md:inline-flex">
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
      </div>
      <VisuallyHidden>{view === 'grid' ? '그리드 보기' : '리스트 보기'}</VisuallyHidden>
    </div>
  )
}
