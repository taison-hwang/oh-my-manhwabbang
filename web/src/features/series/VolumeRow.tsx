import { usePageThumbImage } from '../../api/queries'
import type { BookSummary } from '../../api/types'
import { THUMB_WIDTH_FOR } from '../../api/urls'
import { Button } from '../../components/ds/Button'
import { FormatBadge } from '../../components/ds/FormatBadge'
import { ProgressBar } from '../../components/ds/ProgressBar'
import { cn } from '../../lib/cn'
import { formatBytes, formatPageCount, readToggleLabel } from '../../lib/format'
import { useVolumeReadToggle } from './useVolumeActions'
import {
  isOpenable,
  volumeBadge,
  volumeBytes,
  volumeProgressRatio,
  volumeStateLabel,
  volumeTone,
} from './volume'

/**
 * One volume in list mode (ui-spec §5.4, `series-detail-volume-list-1440.png`).
 *
 * Columns `26px minmax(0,1fr) 66px 62px 132px`, plus two cells the prototype
 * does not have:
 *
 *  * **용량** — prd FR-LIB-009 (필수) requires 페이지 수·용량·형식·진행률 per
 *    volume, and the ui-spec's five columns carry no size. prd wins (§0.1).
 *  * **읽음 표시 / 읽음 해제** — FR-VWR-012, ui-spec gap #7.
 *
 * Cell 1 is ui-spec §5.4's "20×30px striped thumb", and prd UI-002 / design.md
 * 화면 2 both require that thumb to carry the volume's own picture. The stripes
 * stay beneath it as the placeholder — same rule as `FallbackCover`, so a
 * thumbnail arriving late never shifts the row — and an unopenable volume,
 * which has no page 1 to render, never issues the request.
 *
 * ## State and action are not the same kind of thing (ruling E-12)
 *
 * The row used to print `완독` and then `안읽음`, adjacent, both bare accent
 * words: a state followed by the name of the opposite state, which reads as a
 * contradiction rather than as "done" plus "undo". Two changes fix it and both
 * are ui-spec §2.5's own rules. The state is a **badge** — §2.5 lists the `완독`
 * badge and the `암호화`/`손상` badge among the handful of places red is allowed
 * to run as a solid field, and a badge cannot be mistaken for a control. The
 * action is a **bordered `.btn-secondary`** whose label is a verb in both
 * directions (`readToggleLabel`), so it cannot be mistaken for a state. `34%`
 * and `—` stay plain text in `--accent-text` / `--ink-faint`, which is what
 * §2.5 says progress percentages get.
 *
 * The row is not itself a control. The design paints the whole row as
 * clickable, but a row that is a `<button>` cannot contain the mark-read
 * button, and a `<div onClick>` is not reachable from a keyboard — so the
 * *name* is the button, spanning its cell, and the row carries the hover tint.
 *
 * ## The grid is per row, so every row must resolve to the *same* template
 *
 * ui-spec §5.4 gives the list one `grid-template-columns`, and §4.5 says it is
 * "identical on the header and every row". A trailing `auto` track breaks that:
 * `auto` is content-sized, so a row whose action reads `안읽음` produced a
 * narrower last track than one reading `읽음 표시`, and an unopenable row (empty
 * action) a zero-width one — three different templates in one list, with
 * 형식/페이지 수/용량/진행률 sliding up to 45px between neighbours. Every track
 * below is therefore a fixed length; the action cell is always rendered, empty
 * when the volume cannot be opened, and `ACTION_TRACK_PX` is sized for the
 * widest label (`읽음 표시`, ~45px at `text-2xs`) and for the 44px touch minimum
 * `.btn` picks up below 768 (ui-spec §7).
 *
 * ## Responsive (ui-spec §7, NFR-CMP-002)
 *
 * With one fixed template the fixed tracks plus gaps outgrew the row below
 * ~768px and squeezed `minmax(0,1fr)` — the volume name — to exactly 0: twelve
 * anonymous rows. That is the identical failure §7 documents for the library
 * list (`library-list-768.png`, "title column collapses to zero"), so it takes
 * §7's identical remedy, shedding the least primary metadata first:
 *
 * | width | dropped | template |
 * |---|---|---|
 * | ≥1024 | — | all seven tracks |
 * | 768–1023 | 용량 | §7's "drop 수정일 + 용량"; this list has no 수정일 |
 * | <768 | 용량, 페이지 수, the progress *bar* | the state label (`34%`/`완독`/`ERR`) carries the same fact in 46px |
 *
 * The dropped cells are hidden in CSS, not unmounted, so the DOM — and every
 * assertion about 페이지 수 / 용량 — is width-independent. Order matters: the
 * hidden cells are `display:none` and therefore skipped by auto-placement, so
 * the visible cells fall into the shorter template in DOM order.
 *
 * E-12's button chrome had to fit inside that budget, which is why the action
 * keeps `px-1` rather than `.btn`'s 14.4px inline padding: both labels are four
 * Korean glyphs (`읽음 표시` / `읽음 해제`), so 36px of text + 8px of padding +
 * a 2px border still clears the track below with room to spare, and the name
 * track stays wider than 96px down to 400px.
 */

/** Widest action label (4 glyphs at `text-2xs`) plus `px-1` and the 1px border. */
const ACTION_TRACK_PX = 54
/** `w-[46px]` state label — the whole progress cell below 768. */
const STATE_TRACK_PX = 46

export const VOLUME_ROW_COLUMNS_BASE = `26px minmax(0,1fr) 66px ${String(STATE_TRACK_PX)}px ${String(ACTION_TRACK_PX)}px`
export const VOLUME_ROW_COLUMNS_MD = `26px minmax(0,1fr) 66px 62px 132px ${String(ACTION_TRACK_PX)}px`
export const VOLUME_ROW_COLUMNS_LG = `26px minmax(0,1fr) 66px 62px 78px 132px ${String(ACTION_TRACK_PX)}px`

/** ui-spec §5.4 `gap:12px` — `gap-3`. Exported so the geometry test can add it up. */
export const VOLUME_ROW_GAP_PX = 12

/**
 * The three templates as one class string.
 *
 * Written out literally rather than composed from the constants above because
 * Tailwind scans source text: a template literal would produce no CSS at all.
 * `SeriesDetailPage.test.tsx` pins the two representations against each other,
 * so the numbers the geometry test reasons about are the numbers Tailwind emits.
 */
export const VOLUME_ROW_GRID_CLASS =
  'grid-cols-[26px_minmax(0,1fr)_66px_46px_54px] ' +
  'md:grid-cols-[26px_minmax(0,1fr)_66px_62px_132px_54px] ' +
  'lg:grid-cols-[26px_minmax(0,1fr)_66px_62px_78px_132px_54px]'

export interface VolumeRowProps {
  book: BookSummary
  onOpen: (book: BookSummary) => void
}

/**
 * The two states ui-spec §2.5 lets the accent run as a solid field for: the
 * `완독` badge and the 손상/암호화 family, whose row-level stand-in is `ERR`.
 * Everything else in this cell is a number, and numbers are text.
 */
function isTerminalState(state: string): boolean {
  return state === 'ERR' || state === '완독'
}

/**
 * E-32: the thumb well is a rounded recess, not a 1px box, so the tone can no
 * longer be a border colour. Only the broken tone still needs to be visible from
 * across the row — the other three said "1px of divider" and said nothing — so
 * it becomes a **ring**, the same accent-300 ring the prototype puts on a broken
 * volume, and the rest are plain wells.
 */
const WELL_BY_TONE = {
  broken: 'ring-2 ring-inset ring-accent-300',
  started: '',
  finished: '',
  unread: '',
} as const

export function VolumeRow({ book, onOpen }: VolumeRowProps) {
  const tone = volumeTone(book)
  const badge = volumeBadge(book)
  const openable = isOpenable(book)
  const ratio = volumeProgressRatio(book)
  const readToggle = useVolumeReadToggle(book)
  const state = volumeStateLabel(book)
  const thumb = usePageThumbImage(book.id, 1, {
    w: THUMB_WIDTH_FOR.listRow,
    v: book.cv,
    enabled: openable,
  })

  return (
    <div
      data-testid="volume-row"
      className={cn(
        // E-32: no divider, and the hover is a rounded chip (`.row-chip`).
        'row-chip grid items-center gap-3 px-2 py-1',
        VOLUME_ROW_GRID_CLASS,
      )}
    >
      <span
        aria-hidden="true"
        className={cn(
          'fallback-cover-row relative block h-[30px] w-[20px] overflow-hidden rounded-sm shadow-inset',
          WELL_BY_TONE[tone],
        )}
      >
        {thumb.status === 'ready' && (
          <img
            src={thumb.url}
            alt=""
            className="absolute inset-0 h-full w-full object-cover"
            draggable={false}
          />
        )}
      </span>

      <span className="flex min-w-0 items-center gap-2">
        {openable ? (
          <button
            type="button"
            className="min-w-0 truncate whitespace-nowrap text-left text-base"
            onClick={() => {
              onOpen(book)
            }}
          >
            {book.name}
          </button>
        ) : (
          <span className="min-w-0 truncate whitespace-nowrap text-base">{book.name}</span>
        )}
        {badge !== null && (
          <>
            {/* `--on-accent`, not `--color-bg` (E-32 §1). The accent is a deep
                teal that is dark in *both* themes, so the ground can no longer
                double as its foreground: `--color-bg` on it measures 1.48:1 in
                the dark theme. */}
            <span className="flex-none bg-accent px-[6px] py-[2px] text-2xs tracking-[.08em] text-on-accent">
              {badge.label}
            </span>
            <span
              className="min-w-0 truncate whitespace-nowrap text-3xs text-accent-text"
              {...(badge.detail === null ? {} : { title: badge.detail })}
            >
              {badge.reason}
            </span>
          </>
        )}
      </span>

      <FormatBadge format={book.kind} variant="tag" />

      {/* 페이지 수 — dropped below 768 (ui-spec §7). */}
      <span className="hidden text-right text-sm tabular-nums text-ink-muted md:block">
        {formatPageCount(book.page_count)}
      </span>

      {/* 용량 — dropped below 1024 (ui-spec §7's "drop 수정일 + 용량"). */}
      <span className="hidden text-right text-sm tabular-nums text-ink-muted lg:block">
        {formatBytes(volumeBytes(book))}
      </span>

      <span className="flex items-center gap-3">
        <ProgressBar
          value={ratio}
          className="hidden flex-1 md:block"
          tone={ratio >= 1 ? 'done' : 'accent'}
          label={`${book.name} 진행률`}
        />
        <span className="flex w-[46px] flex-none justify-end">
          {/* E-12: the terminal states are badges — a solid accent field, which
              ui-spec §2.5 permits for exactly 완독 and the 손상 family. The
              in-progress and untouched states stay plain text.
              `--on-accent` for the badge's ink (E-32 §1): on the teal accent
              `--color-bg` is 1.48:1 in the dark theme. */}
          <span
            data-role="volume-state"
            className={cn(
              'text-xs tabular-nums',
              isTerminalState(state)
                ? 'bg-accent px-[5px] py-px text-on-accent'
                : state === '—'
                  ? 'text-ink-faint'
                  : 'text-accent-text',
            )}
          >
            {state}
          </span>
        </span>
      </span>

      {/* Always rendered, so the last track is the same width on every row. */}
      <span className="flex justify-end">
        {openable && (
          <Button
            variant="secondary"
            className="px-1 text-2xs"
            disabled={readToggle.pending}
            onClick={readToggle.toggle}
          >
            {readToggleLabel(readToggle.completed)}
          </Button>
        )}
      </span>
    </div>
  )
}
