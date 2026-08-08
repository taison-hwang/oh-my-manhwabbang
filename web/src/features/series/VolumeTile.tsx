import { TriangleAlert } from 'lucide-react'

import { usePageThumbImage } from '../../api/queries'
import type { BookSummary } from '../../api/types'
import { THUMB_WIDTH_FOR } from '../../api/urls'
import { Button } from '../../components/ds/Button'
import { FormatBadge } from '../../components/ds/FormatBadge'
import { ProgressBar } from '../../components/ds/ProgressBar'
import { cn } from '../../lib/cn'
import { formatBytes, formatPageCount, readToggleLabel } from '../../lib/format'
import { useVolumeReadToggle } from './useVolumeActions'
import { isOpenable, volumeBadge, volumeBytes, volumeProgressRatio, volumeTone } from './volume'

/**
 * One volume in grid mode (ui-spec §5.3, `series-detail-grid-1440.png`).
 *
 * The broken state is the interesting one (FR-IDX-010, design.md 화면 2
 * "오류 상태"): an accent-900 @ 82 % scrim, an `암호화`/`손상` badge, the reason,
 * and — structurally, not by a `disabled` attribute — **no button at all**.
 * A disabled control still announces itself as a control the user might one day
 * press; a broken volume is never openable, so it is not one.
 *
 * ## The tile carries the volume's own thumbnail
 *
 * prd UI-002 specifies the volume list as 권 목록 (**썸네일** + 형식 배지 +
 * 페이지 수 + 진행률) and design.md 화면 2 as 권별 **썸네일** + 권 이름 + 형식
 * 배지 + 페이지 수 + 진행 바; impl-plan §0.4 budgets a width for this exact
 * consumer ("Volume tile (series detail) — 128 CSS px ⇒ w=400"). ui-spec §5.3
 * draws only the striped box, and prd wins (§0.1) — so the stripes and the
 * volume number are the *placeholder*, exactly as `FallbackCover` is for a
 * series cover: always rendered, never swapped in, with the real image fading
 * in on top at the identical geometry so a late thumbnail costs no layout
 * shift (UI-5.3). A volume that cannot be opened has no page 1 to render, so it
 * never issues the request.
 */
export interface VolumeTileProps {
  book: BookSummary
  /** 1-based volume number shown in the box; `ord` is 0-based on the wire. */
  number: number
  onOpen: (book: BookSummary) => void
}

/**
 * E-32: the 1px border becomes elevation, so the tone can no longer be a border
 * colour. A broken volume keeps a mark of its own — the accent-300 **ring** the
 * prototype computes for it — because the scrim over it is dark and a card that
 * merely fails to lift is not a signal. The other three tones said "1px of
 * divider" and said nothing, so they are plain raised covers.
 */
const RING_BY_TONE = {
  broken: 'ring-2 ring-inset ring-accent-300',
  started: '',
  finished: '',
  unread: '',
} as const

export function VolumeTile({ book, number, onOpen }: VolumeTileProps) {
  const tone = volumeTone(book)
  const badge = volumeBadge(book)
  const openable = isOpenable(book)
  const ratio = volumeProgressRatio(book)
  const readToggle = useVolumeReadToggle(book)
  const thumb = usePageThumbImage(book.id, 1, {
    w: THUMB_WIDTH_FOR.volumeTile,
    v: book.cv,
    enabled: openable,
  })

  const box = (
    <div
      className={cn(
        'fallback-cover relative aspect-[2/3] overflow-hidden rounded-md shadow-md',
        RING_BY_TONE[tone],
        openable &&
          'transition-[box-shadow,transform] duration-150 group-hover:-translate-y-0.5 group-hover:shadow-lg',
      )}
    >
      <span
        className={cn(
          'absolute inset-0 flex items-center justify-center font-heading text-[26px] font-extrabold',
          tone === 'finished' ? 'text-ink-dim' : 'text-ink-faint',
        )}
      >
        {number}
      </span>

      {thumb.status === 'ready' && (
        <img
          src={thumb.url}
          alt=""
          className="absolute inset-0 h-full w-full object-cover"
          draggable={false}
        />
      )}

      {/* 형식 — FR-LIB-009 again. The `corner` variant is the same one the
          library grid card uses (ui-spec §9 #6), so the two grids agree. */}
      <FormatBadge format={book.kind} variant="corner" />

      {badge !== null && (
        <div
          className="absolute inset-0 flex flex-col items-start justify-end gap-1 bg-scrim-broken p-2"
          {...(badge.detail === null ? {} : { title: badge.detail })}
        >
          {/* E-32: the badge family becomes pills. `--on-accent` rather than
              `--color-bg`, which is 1.48:1 on the accent in the dark theme. */}
          <span className="flex items-center gap-[5px] rounded-full bg-accent px-2 py-[3px] text-2xs font-semibold tracking-[.06em] text-on-accent">
            <TriangleAlert size={11} aria-hidden={true} />
            {badge.label}
          </span>
          <span className="text-3xs leading-[1.25] text-accent-200">{badge.reason}</span>
        </div>
      )}

      {tone === 'started' && (
        <ProgressBar
          value={ratio}
          height={5}
          track="over-art"
          className="absolute inset-x-0 bottom-0"
          label={`${book.name} 진행률`}
        />
      )}
    </div>
  )

  const caption = (
    <>
      <span className="truncate whitespace-nowrap text-sm" title={book.name}>
        {book.name}
      </span>
      {/* prd FR-LIB-009 (필수) wants 페이지 수 *and* 용량 per volume; ui-spec
          §5.3 shows only the page count. prd wins, so both share the line. */}
      <span className="text-xs tabular-nums text-ink-dim">
        {`${formatPageCount(book.page_count)} · ${formatBytes(volumeBytes(book))}`}
      </span>
    </>
  )

  return (
    <div className="group relative flex flex-col gap-[6px]">
      {openable ? (
        <button
          type="button"
          className="flex flex-col gap-[6px] text-left"
          onClick={() => {
            onOpen(book)
          }}
        >
          {box}
          {caption}
        </button>
      ) : (
        <div className="flex flex-col gap-[6px]">
          {box}
          {caption}
        </div>
      )}

      {/* FR-VWR-012. Revealed on hover and mirrored on focus-within so the
          keyboard reaches it too (ui-spec §8.3). E-12: the label names the
          action in both directions — `안읽음` was the name of a state.

          ## An invisible control must not be a hit target

          This overlay is a *later sibling* of the volume's `<button>` and sits
          over the top-right of the cover, so while `opacity: 0` it still won
          the hit test for ~37 % of the box's width: `elementFromPoint` at its
          centre returned the toggle, and a genuine dispatched tap fired
          `readToggle.toggle` while the volume did not open. On a mouse that is
          harmless — hover reveals it before the click, which is the whole
          affordance — but a tap has no hover to precede it, so on a touch
          device the top-right corner of every tile silently flipped persisted
          read state. `hasTouch` is on for the `mobile-400` project
          (playwright.config.ts), so it ships. Same defect, same remedy as the
          library grid card's action overlay.

          The wrapper is therefore `pointer-events-none` *permanently* — it is
          never anything but a positioner — and the button re-enables itself
          only under the same conditions that make it visible. `pointer-events`
          does not affect the tab order, so the keyboard still reaches the
          button and `group-focus-within` then turns it back on.

          ## …and it must not become unreachable either

          Gating on `:hover` alone would trade an accidental toggle for a
          missing feature: a device that cannot hover could never reach the
          control from the tile at all. `(hover: none)` is exactly the "no
          hover to reveal it with" condition, so there the control is simply
          *always* shown and always live — a small visible button is
          discoverable and is not a trap, which is the property the invisible
          one lacked. Written as an arbitrary variant because Tailwind 3.4 has
          no built-in `pointer-coarse`/`max-hover` variant (verified against
          the generated CSS, not assumed) and one class needs no config change.

          List mode's copy of this action (`VolumeRow`) is unconditionally
          visible already, so nothing below 768 depends on this reveal. */}
      {openable && (
        <div className="pointer-events-none absolute right-0 top-0 opacity-0 transition-opacity focus-within:opacity-100 group-hover:opacity-100 [@media(hover:none)]:opacity-100">
          {/* No `bg-bg` (E-42). It was here because this control floats over
              the cover art and `.btn-secondary` used to be a *transparent*
              bordered pill — the page ground was the only thing making the
              label readable over a thumbnail. The class is an opaque cream
              fill now, so the override buys nothing and costs the ink: a
              Tailwind `bg-*` lands after `@layer components` and replaces the
              cream, leaving the class's `--on-control` on
              `--color-bg`, which is **1.13:1** in the dark app theme. Deleting
              it restores both the opacity and the pairing the class ships. */}
          <Button
            variant="secondary"
            className="pointer-events-none text-2xs group-focus-within:pointer-events-auto group-hover:pointer-events-auto [@media(hover:none)]:pointer-events-auto"
            disabled={readToggle.pending}
            onClick={readToggle.toggle}
          >
            {readToggleLabel(readToggle.completed)}
          </Button>
        </div>
      )}
    </div>
  )
}
