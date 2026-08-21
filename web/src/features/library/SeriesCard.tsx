import { useCoverImage } from '../../api/queries'
import type { SeriesSummary } from '../../api/types'
import type { ThumbWidth } from '../../api/urls'
import { Button } from '../../components/ds/Button'
import { DoneSeal } from '../../components/ds/DoneSeal'
import { FallbackCover } from '../../components/ds/FallbackCover'
import { FormatBadge } from '../../components/ds/FormatBadge'
import { ReadRibbon } from '../../components/ds/ReadRibbon'
import { cn } from '../../lib/cn'
import { formatBytes, formatVolumeCount } from '../../lib/format'
import { seriesCardDomId } from '../../store/ui'
import { CARD_TEXT_HEIGHT, highlightParts } from './useLibrary'

/**
 * `SeriesCard` (ui-spec §9 #1, §4.5 "Grid mode") — one cell of the cover grid.
 *
 * The four absolute layers over a `aspect-ratio:2/3` box, bottom to top:
 * striped fallback → cover image → badges → hover/focus action overlay, all four
 * inside the 7px cream mat E-46 wraps the cover in.
 *
 * **The progress is no longer one of those layers.** It used to be a 5px rail
 * lying across the foot of the cover; the 서고 skin hangs a 갈피 out of the top
 * of the book instead, as long as the part that has been read (`ReadRibbon`),
 * and marks a finished one with a 完讀 seal rather than a 완독 pill (`DoneSeal`).
 * Both are drawn *outside* the mat's window so the ribbon can overhang and the
 * seal can sit on the mat, which is why the `overflow-hidden` that used to be on
 * the card box is now one level in.
 *
 * The fallback is **always rendered beneath the image** (FR-LIB-008),
 * never swapped in on failure: that is what makes a cover arriving late — or a
 * `202` while the thumbnail is still queued — a cross-fade rather than a layout
 * shift (UI-5.3).
 *
 * A `202` is not an error state. `useCoverImage` owns the `Retry-After` dance
 * and reports `queued`; the card simply keeps showing the fallback, which is
 * the correct rendering either way.
 *
 * Everything below the cover is pinned to `CARD_TEXT_HEIGHT`, with the title
 * clamped to exactly two lines whether it needs them or not. The virtualiser
 * derives its row height from that constant instead of measuring cards, so a
 * one-line title that rendered 15.6px shorter would not make one card shorter —
 * it would make every row after it sit at the wrong offset, and the 16px
 * `--grid-gap` would render as 32px of dead space.
 */
export interface SeriesCardProps {
  series: SeriesSummary
  /** Requested cover width from `THUMB_WIDTHS` (impl-plan §0.4). */
  coverWidth: ThumbWidth
  /** The active search query, for match highlighting (FR-LIB-006). */
  query: string
  /** E-34 §2 — this is the card the viewer came back to; ring it. */
  revealed?: boolean
  onOpen: () => void
  onResume: () => void
}

export function SeriesCard({
  series,
  coverWidth,
  query,
  revealed = false,
  onOpen,
  onResume,
}: SeriesCardProps) {
  const cover = useCoverImage(series.id, {
    w: coverWidth,
    v: series.cover_cv,
    enabled: series.has_cover,
  })

  const ratio = series.progress.percent / 100
  const done = ratio >= 1
  const started = series.progress.percent > 0 || series.progress.last_book_id !== null
  const parts = highlightParts(series.name, query)

  return (
    // `tabIndex={-1}`: the root is a plain `div` and E-34 §2's reveal focuses
    // it, so it has to be programmatically focusable without entering the tab
    // order — the cover button and the two overlay buttons inside it are the
    // card's keyboard surface and stay so. `outline-none` because the ring the
    // ruling asks for is the inset one on the cover below, not the UA's.
    <div
      id={seriesCardDomId(series.id)}
      tabIndex={-1}
      {...(revealed ? { 'data-revealed': 'true' } : {})}
      className="flex flex-col outline-none"
    >
      <div
        // E-32: a 1px hairline becomes a rounded, raised cover that lifts 3px
        // under the pointer. **E-46 mats it**: the card is a 7px cream border
        // with the cover inside it, the way a print is mounted, and the clip
        // moves off this box onto that window — the ribbon below hangs 4px over
        // the top edge and `overflow-hidden` here would cut it off.
        className="group relative aspect-[2/3] rounded-md bg-surface p-[7px] shadow-md transition-[box-shadow,transform] duration-150 hover:-translate-y-[3px] hover:shadow-lg"
        // An **inset** ring (E-34 §2): the cover is `overflow-hidden` inside a
        // grid cell with no room around it, so an outset ring would be clipped
        // by the cell and overlap its neighbours.
        //
        // It has to be **composed with the elevation**, not written over it: an
        // inline `box-shadow` beats the `shadow-md` class, so ringing a card
        // used to flatten it onto the page. This is the same pair the prototype
        // computes (`var(--shadow-md), inset 0 0 0 2px var(--color-hot)`), and
        // the hover class still wins on hover because `:hover` is not what is
        // being overridden here — the reveal ends the moment the reader moves.
        {...(revealed
          ? { style: { boxShadow: 'var(--shadow-md), inset 0 0 0 2px var(--color-hot)' } }
          : {})}
      >
        {/* The window in the mat. **This** is what clips — the cover art, the
            badge and the hover scrim all live inside it, while the two marks
            that hang off the card (the ribbon, the seal) are siblings of it and
            are not cut. The hairline is the prototype's `0 0 0 1px` around the
            window, in the token the product draws hairlines in. */}
        <div className="absolute inset-[7px] border border-rule">
          <div className="absolute inset-0 overflow-hidden">
            <button
              type="button"
              className="absolute inset-0 block h-full w-full cursor-pointer"
              aria-label={series.name}
              onClick={onOpen}
            >
              <FallbackCover title={series.name} format={series.kind} size="card" />
              {cover.status === 'ready' && (
                <img
                  src={cover.url}
                  alt=""
                  className="absolute inset-0 h-full w-full object-cover"
                  draggable={false}
                />
              )}
            </button>

            <FormatBadge format={series.kind} variant="corner" className="pointer-events-none" />

        {/* Hidden by opacity, never by `display`, so the buttons keep their
            geometry and the reveal is a 120ms fade (ui-spec §4.5).

            The scrim itself stays `pointer-events-none` *permanently*: it spans
            `inset-0` and paints above the cover button (siblings, no z-index —
            DOM order is paint order), so letting it take pointer events on
            hover would make the cover unclickable by mouse forever, since a
            mouse must hover before it can click. Only the two buttons flip to
            `pointer-events-auto`, and only under the same hover/focus-within
            gate that makes them visible — never invisible-but-clickable. The
            hover state stays true throughout: a `pointer-events-none` scrim
            lets the hit fall through to the cover button, and the buttons that
            do take the hit are themselves inside the `.group`.

            ## No `(hover: none)` escape here, unlike `VolumeTile`

            A pointer that cannot hover never reveals this overlay, so on a
            touch device both buttons are inert. That costs a shortcut and no
            destination: `상세` and the cover are the *same* `onOpen` prop, and
            for a series with nothing started `onResume` is `onOpen` too
            (`LibraryPage.tsx` `resumeSeries` has no book id to open). What is
            left — reopening a *started* series' last-read volume at its saved
            page — is what the series screen's own `이어 읽기` does
            (`SeriesHeader.tsx` → `resumeTarget`), from a plain always-visible
            button behind the tap this card already answers, and what the
            이어보기 row does in one tap for anything recent (FR-LIB-010,
            `ContinueCard.tsx`, no hover gate). `VolumeTile`'s read toggle had
            no such second route on its screen, which is why it carries the
            fallback and this does not; and its overlay is 66×29 px in a corner
            (`e2e/03-series-detail.spec.ts` 6.5 (guard)), where this one is
            `--scrim-cover` (ink @ 72 %, `tokens.css`) across `inset-0` — the
            same classes here would paint every cover in the grid out on every
            touch device (`docs/ui-shots/library-grid-card-hover-1440.png` is
            one such card). Changing that is a product ruling, not a
            component's call; escalated with no ruling in decisions.md yet. */}
        {/* E-32: `.cover-scrim` (base.css) replaces the flat 72 % wash with a
            vertical gradient, and the fade goes 120ms → 140ms. */}
        <div className="cover-scrim pointer-events-none absolute inset-0 flex flex-col justify-end gap-1 p-2 opacity-0 transition-opacity duration-[140ms] group-focus-within:opacity-100 group-hover:opacity-100">
          <Button
            variant="primary"
            block
            className="pointer-events-none m-0 text-sm group-focus-within:pointer-events-auto group-hover:pointer-events-auto"
            onClick={onResume}
          >
            {started ? '이어 읽기' : '읽기 시작'}
          </Button>
          {/* E-42: a hand-rolled `bg-bg text-ink` pill used to stand here — a
              `plain` button painted to look like a secondary one, from a time
              when `.btn-secondary` was a transparent outline and could not be
              read over cover art. `.btn-secondary` is now an opaque cream pill
              (`--control-fill`, ink at 11.02), so the hand-painted pair is both
              redundant and the one control in the product that would still look
              like the retired skin — beside a sibling that is already an accent
              pill. The variant carries it now. */}
          <Button
            variant="secondary"
            block
            className="pointer-events-none m-0 text-sm group-focus-within:pointer-events-auto group-hover:pointer-events-auto"
            onClick={onOpen}
          >
            상세
          </Button>
        </div>
          </div>
        </div>

        {/* E-46 — the two marks the 서고 skin puts *on* a book rather than
            beside it. Both are siblings of the window above, so neither is cut
            by its clip: the ribbon hangs 4px over the top edge of the mat and
            the seal is pressed onto the mat's own margin at the foot.

            The ribbon replaces the 5px rail that used to lie across the bottom
            of the cover; it carries the same `role="progressbar"` and the same
            `aria-valuenow`, so nothing that was not looking at it can tell the
            difference (`ReadRibbon.tsx`). */}
        {!done && ratio > 0 && <ReadRibbon value={ratio} label={series.name} />}

        {/* 완독 is stamped now, not labelled (`DoneSeal.tsx`). The word 완독 is
            still in the DOM — it is the seal's accessible name — so the catalogue
            copy and everything that reads for it are unchanged. */}
        {done && <DoneSeal className="absolute bottom-3 right-3 z-sticky" />}
      </div>

      <div
        className="flex flex-col gap-[7px] overflow-hidden pt-[7px]"
        style={{ height: `${CARD_TEXT_HEIGHT.toString()}px` }}
        data-testid="series-card-text"
      >
        {/* `h-[2.6em]` is two 12px lines at `leading-[1.3]`: the box a
            one-line title occupies must be the box a two-line title occupies,
            or the meta row below it moves from card to card. */}
        <div
          className={cn(
            'line-clamp-2 h-[2.6em] text-sm leading-[1.3]',
            series.status === 'ok' ? '' : 'text-ink-dim',
          )}
          title={series.name}
        >
          {parts.before}
          {parts.match !== '' && <span className="text-accent-text">{parts.match}</span>}
          {parts.after}
        </div>

        <div className="flex gap-2 text-xs tabular-nums text-ink-dim">
          <span>{formatVolumeCount(series.book_count)}</span>
          <span>{formatBytes(series.total_bytes)}</span>
        </div>
      </div>
    </div>
  )
}
