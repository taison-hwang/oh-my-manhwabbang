import { useCoverImage } from '../../api/queries'
import type { SeriesSummary } from '../../api/types'
import type { ThumbWidth } from '../../api/urls'
import { Button } from '../../components/ds/Button'
import { FallbackCover } from '../../components/ds/FallbackCover'
import { FormatBadge } from '../../components/ds/FormatBadge'
import { ProgressBar } from '../../components/ds/ProgressBar'
import { cn } from '../../lib/cn'
import { formatBytes, formatVolumeCount } from '../../lib/format'
import { seriesCardDomId } from '../../store/ui'
import { CARD_TEXT_HEIGHT, highlightParts } from './useLibrary'

/**
 * `SeriesCard` (ui-spec §9 #1, §4.5 "Grid mode") — one cell of the cover grid.
 *
 * The four absolute layers over a `aspect-ratio:2/3` box, bottom to top:
 * striped fallback → cover image → badges + progress → hover/focus action
 * overlay. The fallback is **always rendered beneath the image** (FR-LIB-008),
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
        // under the pointer.
        className="group relative aspect-[2/3] overflow-hidden rounded-md bg-surface shadow-md transition-[box-shadow,transform] duration-150 hover:-translate-y-[3px] hover:shadow-lg"
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

        {/* E-32: a pill inset 8px from the corner, like the format badge
            opposite it. `--on-accent`, not `--color-bg`: the ground is the
            accent fill, and `--color-bg` on it is 1.48:1 in the dark theme. */}
        {done && (
          <span className="pointer-events-none absolute right-2 top-2 rounded-full bg-accent px-2 py-[3px] text-2xs font-semibold tracking-[.06em] text-on-accent shadow-sm">
            완독
          </span>
        )}

        {!done && ratio > 0 && (
          <ProgressBar
            value={ratio}
            height={5}
            track="over-art"
            label={series.name}
            className="pointer-events-none absolute inset-x-0 bottom-0"
          />
        )}

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
          <Button
            block
            className="pointer-events-none m-0 bg-bg text-sm text-ink group-focus-within:pointer-events-auto group-hover:pointer-events-auto"
            onClick={onOpen}
          >
            상세
          </Button>
        </div>
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
