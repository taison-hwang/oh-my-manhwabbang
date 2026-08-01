import {
  ArrowLeft,
  ChevronsDown,
  Columns2,
  File,
  Image as ImageIcon,
  MoveHorizontal,
  MoveVertical,
} from 'lucide-react'
import { Button } from '../../components/ds/Button'
import { Seg, type SegOption } from '../../components/ds/Seg'
import { cn } from '../../lib/cn'
import type { DisplayMode, FitMode, ReadingDirection } from '../../store/viewer'

/**
 * The top overlay (ui-spec §6.6).
 *
 * Fades on **opacity**, never `display:none`: the bar stays mounted so the wake
 * is a 180 ms fade rather than a reflow of three segmented controls on every
 * mouse move. `pointer-events` is what actually turns it off.
 *
 * **No hover-hold handlers here.** E-27's "a pointer resting in the chrome pins
 * it open" used to be `onMouseEnter`/`onMouseLeave` on this element, and that
 * only ever worked when the reader *crossed* into the bar: React synthesises
 * those two from `mouseover`/`mouseout` and drops the pair when a bar lights up
 * underneath a pointer that has not moved, which is exactly what waking from a
 * screen-edge strip does. The rule now lives once, on the viewer root, as a
 * `pointerover`/`pointerout` question about what is under the pointer — see the
 * long note on `trackChromeHover` in `ViewerPage`. This bar's only part in it is
 * its `data-role`, which is how the rule recognises it.
 *
 * ## Narrow viewports: the bar **wraps**
 *
 * All three groups are always inline, at every width, and the bar is allowed to
 * become two or three rows tall. Measured against the design prototype:
 * 55 px at 1440, 103 px at 900, 151 px at 500.
 *
 * This replaces a `⋯` bottom sheet that used to hold 맞춤 below 1024 and all
 * three groups below 768. The sheet answered a real problem — the captured
 * breakage in `viewer-overlay-400-broken.png`, where the groups overflowed and
 * their labels broke vertically — but it answered it by hiding the controls
 * behind a second press, and it cost three further mechanisms to keep upright:
 * the chrome had to be pinned while the sheet was open, the sheet had to be
 * closed when the chrome went anyway, and neither bar was allowed a `z-index`
 * because a stacking context would trap the sheet's escape hatch inside it.
 *
 * Two rules make the sheet unnecessary. `flex-wrap` on the bar, and
 * `flex-none whitespace-nowrap` on each `.seg` — a group moves to the next row
 * **whole** or not at all, which is exactly the breakage the capture shows. And
 * because the awake bar is *in the flex column* (E-27), a bar that grows to
 * three rows shrinks the stage to fit rather than covering the page with it.
 *
 * Dropping the sheet is what lets both bars carry `z-chrome`, which is what
 * keeps them above the end-of-volume scrim — see `ViewerPage`.
 */
export interface ViewerTopBarProps {
  visible: boolean
  seriesName: string
  bookName: string
  mode: DisplayMode
  dir: ReadingDirection
  fit: FitMode
  onBack: () => void
  onModeChange: (mode: DisplayMode) => void
  onDirChange: (dir: ReadingDirection) => void
  onFitChange: (fit: FitMode) => void
}

/** C-1: the wire value is `spread`; the Korean label stays 양면. */
const MODE_OPTIONS: readonly SegOption<DisplayMode>[] = [
  { value: 'single', label: '단면', icon: <File size={13} /> },
  { value: 'spread', label: '양면', icon: <Columns2 size={13} /> },
  { value: 'vertical', label: '세로', icon: <ChevronsDown size={13} /> },
]

const DIR_OPTIONS: readonly SegOption<ReadingDirection>[] = [
  { value: 'ltr', label: 'L→R' },
  { value: 'rtl', label: 'R→L' },
]

/**
 * 세 종 — 너비 · 높이 · 원본 (ruling **E-27**, prd FR-VWR-005 as amended).
 *
 * `contain` is still a value on the wire and still a value the store holds
 * (arch §7 is unchanged, so a `user.db` written before the amendment keeps
 * loading); it simply has no control any more, and `useViewerStore.open` opens
 * such a book at 높이. Read the pair together: dropping the option without the
 * coercion would leave a reader on a fit whose button does not exist, unable to
 * see which one they are on or get off it.
 */
const FIT_OPTIONS: readonly SegOption<FitMode>[] = [
  { value: 'width', label: '너비', icon: <MoveHorizontal size={13} /> },
  { value: 'height', label: '높이', icon: <MoveVertical size={13} /> },
  { value: 'original', label: '원본', icon: <ImageIcon size={13} /> },
]

/**
 * ui-spec §6.6: viewer chrome borders are neutral-700, not the divider.
 * `flex-none whitespace-nowrap` keeps a group from being squeezed into two
 * lines by the wrapping bar — it moves to the next row whole or not at all.
 */
const SEG_CLASS =
  'flex-none whitespace-nowrap border-neutral-700 [&_.seg-opt+.seg-opt]:border-l-neutral-700'

export function ViewerTopBar({
  visible,
  seriesName,
  bookName,
  mode,
  dir,
  fit,
  onBack,
  onModeChange,
  onDirChange,
  onFitChange,
}: ViewerTopBarProps) {
  const modeSeg = (
    <Seg
      aria-label="표시 모드"
      className={SEG_CLASS}
      value={mode}
      options={MODE_OPTIONS}
      onChange={onModeChange}
    />
  )
  const dirSeg = (
    <Seg
      aria-label="읽기 방향"
      className={SEG_CLASS}
      value={dir}
      options={DIR_OPTIONS}
      onChange={onDirChange}
    />
  )
  const fitSeg = (
    <Seg
      aria-label="맞춤"
      className={SEG_CLASS}
      value={fit}
      options={FIT_OPTIONS}
      onChange={onFitChange}
    />
  )

  return (
    <div
      data-role="viewer-top-bar"
      data-visible={visible ? 'true' : 'false'}
      className={cn(
        'z-chrome flex flex-wrap items-center gap-3 border-b-2 border-neutral-800 bg-bg px-4 py-2 transition-opacity',
        // E-27: an awake bar is *in the column*, so the stage shrinks to fit
        // beside it instead of being covered by it — including when it has
        // wrapped to three rows. Asleep it goes back to being an overlay, so a
        // chromeless viewer is the full height of the screen and the layout
        // does not depend on an invisible box.
        visible
          ? 'relative order-first flex-none opacity-100'
          : 'pointer-events-none absolute inset-x-0 top-0 opacity-0',
      )}
      style={{ transitionDuration: 'var(--chrome-fade)' }}
    >
      <Button
        variant="secondary"
        className="gap-[7px] border-neutral-700 text-sm"
        onClick={onBack}
      >
        <ArrowLeft size={13} aria-hidden={true} />
        뒤로
      </Button>

      <div className="flex min-w-0 flex-col">
        <span className="truncate font-heading text-base font-extrabold text-ink">
          {seriesName}
        </span>
        <span className="truncate text-xs text-neutral-500">{bookName}</span>
      </div>

      <div className="flex-1" />

      {modeSeg}
      {dirSeg}
      {fitSeg}
    </div>
  )
}
