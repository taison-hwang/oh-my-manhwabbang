import { useVirtualizer } from '@tanstack/react-virtual'
import { useEffect, useLayoutEffect, useRef, useState } from 'react'

import type { ID, SeriesSummary } from '../../api/types'
import { seriesCardDomId } from '../../store/ui'
import { SeriesCard } from './SeriesCard'
import {
  cardHeight,
  columnCount,
  columnWidth,
  gridCoverWidth,
  GRID_METRICS,
  useGridBox,
} from './useLibrary'

/**
 * The virtualised cover grid (FR-LIB-001, FR-LIB-007, NFR-PRF-003).
 *
 * `@tanstack/react-virtual` windows **rows**, not cards, so the column count has
 * to exist in JavaScript. It is computed from the measured content width and the
 * `--grid-min`/`--grid-gap` values of the current tier (`GRID_METRICS`), which
 * reproduces exactly what `repeat(auto-fill, minmax(--grid-min, 1fr))` would
 * have laid out — see the drift test in `useLibrary.test.ts`.
 *
 * The ref that feeds that arithmetic is on the **grid box**, not on the padded
 * wrapper around it: `clientWidth` includes padding, and measuring the `p-4`
 * parent hands `columnCount` 32px the grid does not have — 7 columns of 151px
 * at 1440 where ui-spec §7 says 6 of ~180px, and 151px is under the 152px
 * `--grid-min` those columns exist to honour. The skeleton lays itself out with
 * the real CSS template, so getting this wrong also turns the skeleton→grid
 * transition into a 6→7 column reflow, i.e. the opposite of acceptance 9.
 *
 * Row height is a pure function of the column width (2:3 cover + a fixed 60px of
 * text, which `SeriesCard` pins rather than merely hopes for), so no card is
 * ever measured: measuring would make the scrollbar jump as rows are recycled,
 * which is the layout shift the skeleton exists to avoid.
 */
export interface SeriesGridProps {
  items: SeriesSummary[]
  /** The debounced search query, for match highlighting. */
  query: string
  /** E-34 §2 — a series to scroll to and focus, or `null`. One-shot. */
  revealSeries?: ID | null
  /** Called once the reveal has been acted on, so the instruction is not replayed. */
  onRevealed?: () => void
  onOpen: (sid: ID) => void
  onResume: (series: SeriesSummary) => void
  /** Called when the last row is rendered — the FR-LIB-007 pagination trigger. */
  onEndReached: () => void
}

export function SeriesGrid({
  items,
  query,
  revealSeries = null,
  onRevealed,
  onOpen,
  onResume,
  onEndReached,
}: SeriesGridProps) {
  const scrollRef = useRef<HTMLDivElement>(null)
  const gridRef = useRef<HTMLDivElement>(null)
  // One hook, not two: the tier and the measured width have to arrive in the
  // same commit or the grid renders a layout that is neither the old one nor the
  // new one. `useGridBox` is where that is enforced, and why.
  const { width, breakpoint } = useGridBox(gridRef)
  const metrics = GRID_METRICS[breakpoint]

  const columns = columnCount(width, metrics)
  const rowHeight = cardHeight(columnWidth(width, columns, metrics.gap))
  const rowCount = Math.ceil(items.length / columns)

  const virtualizer = useVirtualizer({
    count: rowCount,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => rowHeight,
    gap: metrics.gap,
    overscan: 2,
  })

  const rows = virtualizer.getVirtualItems()
  const lastIndex = rows.at(-1)?.index ?? -1

  /**
   * Re-lay-out when the row pitch changes — `estimateSize` and `gap` alone do
   * not. This is the E-28 disease, and the derivation is written out once, in
   * `features/viewer/ThumbnailStrip.tsx`: `virtual-core` memoises
   * `getMeasurements` on `[count, paddingStart, scrollMargin, getItemKey,
   * enabled]` plus the item-size cache, **neither `estimateSize` nor `gap` is
   * in that key**, and `measure()` swaps the size cache for a fresh Map, which
   * *is*. Handing the virtualiser new numbers therefore changes nothing on its
   * own; only `measure()` makes it look at them.
   *
   * **What re-lays this grid out is not a breakpoint**, which is what makes it
   * worse here than in the strip. `rowHeight` is `cardHeight(columnWidth(…))` —
   * `measuredWidth → columnWidth → ×1.5 + 60` — so it is a *continuous*
   * function of the grid box's width, and **every** resize moves it, including
   * the ones that keep the same tier and the same column count. Both were
   * measured on the shipped build against 60 synthetic series:
   *
   *  - 1280 → 1440 (open item `m`): six columns throughout, the grid box 998 →
   *    1158px and the cards 289.5 → 329.5px tall, while the pitch stayed at the
   *    1280 value of 305.5px — 24px of card overlapping card on every row — and
   *    the track stayed 3 039px against the 3 439px ten rows then needed. A
   *    reload at 1440 gave 345.5px.
   *  - 1100 → 1200, five columns, `laptop` on both sides so nothing about the
   *    tier changes: cards 286.2 → 316.2px, pitch stuck at 302.2px against the
   *    332.2px a reload gives, i.e. 14px of overlap. Keying this effect on the
   *    breakpoint instead of on the pitch would miss exactly this case.
   *
   * `metrics.gap` is a dependency because it is subject to the same rule: it is
   * `useVirtualizer`'s `gap` option, read inside the memo body and absent from
   * the memo key exactly like `estimateSize`, so a `gap` that moved on its own
   * would go stale on its own. In *this* layout it never does move on its own —
   * `--grid-gap` only changes at 768, and measured across that boundary the
   * column count goes 2 → 4 and the card 550.5 → 318.4px, so `rowHeight` moves
   * 232px at the same instant. The dependency is therefore belt-and-braces, and
   * the test that pins it has to manufacture a transition the real layout does
   * not produce. It stays because the memo-key rule, not the current geometry,
   * is what makes it correct.
   *
   * **The re-measure moves every offset, so the reader has to be put back.**
   * `measure()` recomputes each row's `start`, but `scrollTop` is untouched, so
   * whatever was under the reader's eyes slides by `topRowIndex × Δpitch` —
   * *linear in scroll depth*, which is the part that matters: measured here at
   * 1440 → 1280 with `scrollTop` at 1500 the anchor card jumped 160px, and at
   * 1280 → 1440 on row 6 it jumped 200px (the browser's own scroll anchoring
   * absorbed 40px of 240), and a 10 000-series library 500 rows down with a
   * 40px Δpitch would jump 20 000px.
   *
   * `scrollTop === 0` re-anchors nothing at all: sitting at the top of the
   * library is the common case for a resize, row 0 is already flush whatever the
   * pitch is, and the one thing worse than a jump is a jump at the top.
   *
   * **What this effect costs, stated as the property it actually has.**
   * `align: 'start'` snaps the anchor row flush to the top, so a reader parked
   * *inside* a row is moved by exactly how far into it they were —
   * `scrollTop − anchorRow.start`. That is bounded by **the card height**
   * (289.5px at 1280, 329.5px at 1440), not by anything smaller: 118px is only
   * what the 1440 → 1280 sample below happens to produce, and parking 150px
   * into row 0 across 1280 → 1440 measures a 150px move.
   *
   * The property worth defending is that it **does not grow with scroll depth**,
   * and that is exactly the defect being fixed. The trade is real, so: a shallow
   * reader can be moved *further* than they were before this effect existed
   * (150px into row 0 — before: −54px, after: +150px), while a deep one is moved
   * far less (list rows 20 and 30 — before: +300px and +450px, after: 0 and 0).
   * The "before" numbers are displacements across a **broken** layout, with the
   * pitch stuck and the cards overlapping by 24px, so they are not a baseline
   * anyone should want to preserve.
   *
   * **The new offset is computed here, not asked for — and that is forced.**
   * The obvious shape, and the one the strip has carried since E-28, is
   * `measure()` then `scrollToIndex`. It is a **no-op that reads as working**:
   * `getOffsetForIndex` takes its offset from `measurementsCache`, a plain
   * array refreshed only as a side effect *inside the `getMeasurements` memo
   * body*, and `measure()` invalidates that memo without running it. So the
   * offset comes from the very numbers this effect has just thrown away —
   * measured, `scrollToIndex(4)` right after `measure()` scrolled to row 4's
   * *old* start, i.e. it "corrected" the reader by exactly the jump it was
   * supposed to cancel. (The viewer has since measured the same no-op in the
   * strip, where the recentre distance came out 0.)
   *
   * Deferring a frame does not fix it either, and that was measured too: the
   * re-render `measure()` notifies is scheduled, not synchronous, and in the
   * *growing* direction it landed a frame **after** the `requestAnimationFrame`
   * callback — 1280 → 1440 on row 6 still re-anchored to the stale 1833. The
   * accessor that would settle it, `getMeasurements`, is `private`.
   *
   * So this is two layout effects, and the split is the design:
   *
   *  0. **A note on this dependency list, because it has now been wrong three
   *     times.** `getTotalSize()` was here and was removed — its stated reason,
   *     that a changing total picks out the re-measured commit, is false.
   *     `columns` is here and **cannot** trigger a run that `rowHeight` would
   *     miss: `columnWidth = (W − gap(c−1))/c` is equal at two column counts
   *     only when `(c₂ − c₁)(W + gap) = 0`, i.e. only when `c₁ = c₂`, so a
   *     column change always moves `rowHeight` too. It stays because the effect
   *     *reads* it and the exhaustive-deps rule is right to insist, **not**
   *     because it is a second trigger. The list's `anchorGeneration` was
   *     described as symmetric and turned out to be load-bearing. The pattern
   *     worth carrying forward: a dependency list is a claim about *when* an
   *     effect runs, and that claim needs the same evidence as any other — the
   *     mutation that removes it must go red, or the reason written next to it
   *     must be the real one.
   *  1. **Work out which series is at the top, and where it is going.** Both
   *     halves are arithmetic on values this component owns — the virtualiser is
   *     not asked anything, and that is the second lesson of this effect.
   *
   *     The first version *did* ask it, reading `getVirtualItems().find((row) =>
   *     row.end > scrollTop)` before calling `measure()` on the theory that the
   *     offsets are still stale at that point and stale is the coordinate system
   *     `scrollTop` is in. **That theory is false whenever the column count
   *     changes**, and the column count changing is the commonest grid resize
   *     there is: `count` — the row count — *is* in the `getMeasurements` memo
   *     key, so a render that changes it has already recomputed every offset
   *     with the new pitch. `end > scrollTop` then names a row in the new layout
   *     while `scrollTop` still means the old one, and it names one far too
   *     early. Measured on the shipped build, 12 series at 871 → 800 (3 columns
   *     of 430.5px → 2 of 574.5px), parked on row 1: row 0's *new* span is
   *     [0, 574.5], it contains the old scrollTop of 447, so the anchor came out
   *     as row 0 and the reader was thrown to the very top. It was worse than
   *     the defect. Every scenario measured before that one happened to hold the
   *     column count fixed, and so did every guard here — they had been written
   *     to keep `count` constant *on purpose*, so that nothing could invalidate
   *     the memo by accident, which excluded precisely the case that broke.
   *
   *     So the previous pitch and column count are carried forward in a ref
   *     instead, and the anchor is the **series** at the top rather than the row
   *     index. Across a column-count change a row index is not an identity at
   *     all: row 1 of a 3-column grid holds series 3–5, row 1 of a 2-column grid
   *     holds series 2–3. Series 3 is what the reader is looking at, so series 3
   *     is what is kept on screen — `floor(scrollTop / prevPitch) × prevColumns`
   *     to find it, `floor(series / columns)` to place it.
   *
   *     The target offset is the same rule the virtualiser will apply: these
   *     rows are uniform by construction (the promise at the top of this file is
   *     that no card is ever measured), so `getMeasurements` can only ever
   *     produce `start_i = i × (rowHeight + gap)`. The test cross-checks that
   *     against the virtualiser's own rendered `translateY`, so a divergence
   *     fails the guard rather than reaching a reader.
   *
   *     **`anchorSeriesRef` no longer exists for the reason it was written for,
   *     and this is what it does now.** It was added because one resize was not
   *     one commit: the tier and the measured width answered on different ticks,
   *     so crossing a tier rendered an intermediate layout from the *new* metrics
   *     and the *old* width — 4 columns at 871 → 354, where the reader's series 3
   *     lands in `floor(3 / 4) = 0` and the scroll goes to the top. That is
   *     fixed upstream now: `useGridBox` reads both halves in one pass, so no
   *     such commit is rendered and this effect never sees a layout that was
   *     never on screen.
   *
   *     What it still earns its place for is the *reader-intent* question, which
   *     has nothing to do with tiers: two resizes in a row. The first one moves
   *     the reader, and on the second one `scrollTop` is a position **we** wrote,
   *     not one the reader chose. Re-deriving the anchor from it would quietly
   *     make our own correction the new intent and let the reader drift a little
   *     further on every drag of a window edge. `lastWrittenRef` is what tells
   *     the two apart, and the remembered series is what the answer is when the
   *     position turns out to be ours.
   *
   *     So the anchor is *remembered* rather than re-derived each time. The test
   *     for "did the reader move, or did we move them" is `lastWrittenRef`: if
   *     the scroller is within a pixel of the offset effect 2 last wrote, then
   *     `scrollTop` carries no reader intent and the remembered series stands.
   *     Any other position is the reader's and re-derives it. That needs no
   *     scroll listener and no suppression flag, and it degrades correctly — a
   *     write the browser clamped does not match, so the next run re-derives
   *     from where the scroller actually is.
   *
   *     **The clamp that used to eat the reader's place is closed, and a smaller
   *     one is not.** The old one was pathological: the intermediate layout was
   *     much shorter than the settled one — 871 → 354 rendered 4 columns, a
   *     1 033px track against the 1 770px it settles at — and the browser clamped
   *     `scrollTop` to a maximum belonging to a layout that was never shown,
   *     *during layout*, before this effect was called. A reader parked at 447
   *     was already at 337 by the time effect 1 first saw it, and the loss grew
   *     with depth, because the clamp target is a fixed property of that layout
   *     and the reader's position is not. `useGridBox` removes the layout, so it
   *     removes the clamp.
   *
   *     What survives is that **any** resize which genuinely shortens the track
   *     still clamps before JavaScript runs — widening from a narrow tier to a
   *     wide one takes the same items from many rows to few, and a reader parked
   *     below the new maximum is moved by the browser, not by us. Effect 1 reads
   *     `scrollTop` after React's mutation phase, so it can only ever see the
   *     post-clamp value and will faithfully anchor to it. This is smaller than
   *     what it replaces — the target is now a layout the reader is actually
   *     being taken to, so the residual is "this layout cannot hold you that
   *     deep" rather than "we clamped you to a fiction" — but it is not nothing,
   *     and it is **not measured**: the numbers above were taken against the
   *     intermediate commit and do not transfer.
   *
   *     Closing it means anchoring from a `scrollTop` captured *before* the
   *     commit — a passive `scroll` listener storing the reader's last position —
   *     rather than from the live read at the top of this effect. That is a
   *     separate change with its own test, deliberately not folded in here so
   *     that the two are attributable.
   *
   *     **That expression re-implements `virtual-core`'s layout rule, so it
   *     inherits that rule's premises**, and they are premises about the options
   *     object above rather than about anything visible here: `paddingStart` and
   *     `scrollMargin` are both absent, so they default to 0 and row 0 starts at
   *     0; `lanes` is absent, so it defaults to 1 and each row follows the one
   *     before it; and nothing calls `measureElement`, so every `size` is the
   *     `estimateSize` above rather than a measured one. Add any of those to the
   *     `useVirtualizer` call and this arithmetic goes quietly wrong. The
   *     `paddingStart`/`scrollMargin` half is pinned in the guard below —
   *     `translateY(0px)` on row 0 — because it is the one a future edit is
   *     most likely to reach for.
   *  2. **Scroll on the commit where the re-measure actually landed**, which is
   *     what `anchorGeneration` selects. Effect 1 bumps it in the same layout
   *     phase in which it calls `measure()`, and React flushes an update
   *     scheduled from a layout effect synchronously before paint — so the
   *     commit this effect runs on is the one that has already re-read
   *     `getMeasurements` and written the new track height into the DOM. That is
   *     what makes `getOffsetForAlignment`'s clamp, which reads `scrollHeight`
   *     off the **DOM**, the new one rather than the old, shorter one.
   *
   *     **`getTotalSize()` used to be in this list and has been removed, because
   *     the reason given for it was not true.** The claim was that the total
   *     changing is what picks out the re-measured commit. It is not: a
   *     re-measure can move the pitch and leave the total exactly where it was,
   *     and that state is reachable rather than theoretical — 12 series in a
   *     773px box at `tablet` is 4 rows of 430.5px, 12 series in a 312px box at
   *     `mobile` is 6 rows of 285px, and **both total 1 770px**, because the
   *     wider gap and coarser `--grid-min` above 768 trade against the extra row
   *     exactly. Those are viewports 871 and 354, both ordinary. `items` is the
   *     *filtered* list, so a twelve-result search is an everyday way to be
   *     there. On that transition `getTotalSize()` never fires this effect at
   *     all, and the generation is doing the whole job; keeping it as a "second
   *     signal" would have left a dependency whose stated purpose was fiction.
   *     The guard below is built on the 773 → 312 pair.
   *
   *     The generation only advances when there is an anchor to hand over, so
   *     the ordinary top-of-library resize costs no extra render. **That last
   *     sentence is undefended**: dropping the `anchor !== null` condition
   *     leaves every test green, because the only consequence is one wasted
   *     render — effect 2 still finds an empty ref and returns. It is a
   *     performance property with no observable in jsdom, recorded here rather
   *     than pretended about.
   *
   * That split is what removes the visible frame. Sampled every frame in Chrome
   * across each of the resizes below, no frame is ever painted with the rows
   * re-measured and the scroll not yet corrected; the shape this replaced —
   * scroll immediately, then repair in a `requestAnimationFrame` — painted
   * exactly that frame, 300px out on the list.
   *
   * **`useLayoutEffect` rather than `useEffect` is a rule, not a measurement,
   * and that is worth saying plainly.** Both effects read and write scroll
   * geometry, which is what layout effects are for. But a passive variant of
   * the second one was built and measured on all five scenarios and came out
   * *identical* — React 18 flushed the passive effect inside the same task
   * anyway — and jsdom cannot see the difference either, so the guard below
   * stays green with it swapped. It is kept because the ordering guarantee is
   * the thing being relied on, not because a browser was caught misbehaving
   * without it.
   *
   * `scrollToOffset` rather than a bare `scrollTop =`: it is the public entry
   * point, applies the clamp the browser would apply anyway, and keeps the
   * scroll visible to the virtualiser's own plumbing.
   */
  const { measure, scrollToOffset } = virtualizer
  const pendingAnchorRef = useRef<number | null>(null)
  const [anchorGeneration, setAnchorGeneration] = useState(0)
  /** The pitch and column count the layout on screen was built from. */
  const laidOutRef = useRef({ pitch: rowHeight + metrics.gap, columns })
  /** The series the reader is being kept on, and the offset we last put it at. */
  const anchorSeriesRef = useRef<number | null>(null)
  const lastWrittenRef = useRef<number | null>(null)

  useLayoutEffect(() => {
    const previous = laidOutRef.current
    laidOutRef.current = { pitch: rowHeight + metrics.gap, columns }

    const top = scrollRef.current?.scrollTop ?? 0
    // Is the scroller where *we* last put it, rather than where the reader put
    // it? Then `top` carries no new information and the remembered series is
    // the better answer — see the comment above.
    const ours = lastWrittenRef.current !== null && Math.abs(top - lastWrittenRef.current) < 1
    const series = ours
      ? anchorSeriesRef.current
      : top > 0 && previous.pitch > 0
        ? Math.floor(top / previous.pitch) * previous.columns
        : null

    anchorSeriesRef.current = series
    // Nothing to hand over means nothing to have written, and a `lastWrittenRef`
    // left over from an earlier resize would make `ours` true for a position the
    // reader chose for themselves — the reader scrolls to the top, a resize
    // finds no anchor and writes nothing, the reader later lands back on that
    // stale offset, and the next resize believes it put them there.
    if (series === null) lastWrittenRef.current = null
    pendingAnchorRef.current =
      series === null ? null : Math.floor(series / columns) * (rowHeight + metrics.gap)
    measure()
    if (series !== null) setAnchorGeneration((generation) => generation + 1)
  }, [measure, rowHeight, metrics.gap, columns])

  useLayoutEffect(() => {
    const target = pendingAnchorRef.current
    if (target === null) return
    pendingAnchorRef.current = null
    lastWrittenRef.current = target
    scrollToOffset(target, { align: 'start' })
  }, [anchorGeneration, scrollToOffset])

  useEffect(() => {
    if (rowCount > 0 && lastIndex >= rowCount - 1) onEndReached()
  }, [lastIndex, rowCount, onEndReached])

  // -------------------------------------------------------------------------
  // The E-34 §2 reveal
  // -------------------------------------------------------------------------
  //
  // **`document.getElementById` cannot do this job here.** That is the
  // prototype's implementation and it is right for the prototype, which renders
  // every card. This grid is windowed: at 1440 it holds about five rows of six,
  // so the card for series #51 of the 60 already fetched is simply not in the
  // document, and `getElementById` returns `null` for it — silently, which is
  // the failure mode the ruling calls out. The index in `items` is the thing
  // that always exists, so the scroll goes through the virtualiser and the
  // element is looked up **after** it has had a frame to mount.
  //
  // `align: 'start'` with no offset. The prototype subtracts 96px because its
  // scroll container is the whole library screen and its header sits inside
  // that scroll; ours is the grid band alone (`scrollRef` below) — 이어보기 and
  // the section header are outside it — so the top of this container is already
  // below the chrome, and 96px would push the card that much too far down.
  //
  // A target that is not in `items` yet is left **armed**: `onRevealed` is not
  // called, so the instruction survives, and this effect runs again on every
  // page the infinite list appends. Chasing it instead — fetching pages until
  // the series turns up — is unbounded on a 10 000-series library and can never
  // terminate at all when the reader's `scope`/`q` exclude that series, which
  // E-34 §1 forbids us from clearing. Nothing is focused in the meantime, so
  // nothing is stolen.
  //
  // **`width > 0` is not defensiveness, it is the correctness of the row
  // arithmetic.** `useGridBox` measures in a *layout* effect, and React
  // flushes a commit's pending passive effects before it runs the re-render that
  // a layout effect's `setState` scheduled — so on the commit this grid mounts,
  // this effect sees `width === 0`, and `columnCount(0)` is **1**. The reveal
  // would scroll to row 50 of a one-column grid instead of row 8 of a
  // six-column one; measured here, that put the scroller at the very bottom
  // with the card nowhere near it. An unmeasured grid has no rows yet. The
  // measurement lands one commit later and `columns`/`width` are dependencies,
  // so the reveal simply happens then.
  const [revealed, setRevealed] = useState<ID | null>(null)
  useEffect(() => {
    if (revealSeries === null || width <= 0) return
    const index = items.findIndex((series) => series.id === revealSeries)
    if (index === -1) return
    setRevealed(revealSeries)
    onRevealed?.()
    virtualizer.scrollToIndex(Math.floor(index / columns), { align: 'start' })
    requestAnimationFrame(() => {
      document.getElementById(seriesCardDomId(revealSeries))?.focus()
    })
  }, [revealSeries, items, columns, width, virtualizer, onRevealed])

  const coverWidth = gridCoverWidth(breakpoint)

  return (
    <div
      ref={scrollRef}
      className="min-h-0 flex-1 overflow-y-auto"
      // Reserve the scrollbar gutter unconditionally so the measured width —
      // and therefore the column count — does not change the moment the grid
      // becomes tall enough to scroll.
      style={{ scrollbarGutter: 'stable' }}
      data-testid="library-scroller"
    >
      <div className="p-4">
        <div
          ref={gridRef}
          className="relative w-full"
          style={{ height: `${virtualizer.getTotalSize().toString()}px` }}
        >
          {rows.map((row) => {
            const start = row.index * columns
            const slice = items.slice(start, start + columns)
            return (
              <div
                key={row.key}
                data-index={row.index}
                className="absolute left-0 top-0 grid w-full"
                style={{
                  transform: `translateY(${row.start.toString()}px)`,
                  gridTemplateColumns: `repeat(${columns.toString()}, minmax(0, 1fr))`,
                  gap: `${metrics.gap.toString()}px`,
                }}
              >
                {slice.map((series) => (
                  <SeriesCard
                    key={series.id}
                    series={series}
                    coverWidth={coverWidth}
                    query={query}
                    revealed={series.id === revealed}
                    onOpen={() => {
                      onOpen(series.id)
                    }}
                    onResume={() => {
                      onResume(series)
                    }}
                  />
                ))}
              </div>
            )
          })}
        </div>
      </div>
    </div>
  )
}
