import {
  ArrowLeft,
  ChevronsDown,
  Columns2,
  File,
  Image as ImageIcon,
  MoveHorizontal,
  MoveVertical,
} from 'lucide-react'
import { useEffect, useState } from 'react'

import { Button } from '../../components/ds/Button'
import { Seg, type SegOption } from '../../components/ds/Seg'
import { cn } from '../../lib/cn'
import { useBreakpoint } from '../../lib/useMediaQuery'
import {
  CHROME_AUTOHIDE_MS,
  useViewerStore,
  type DisplayMode,
  type FitMode,
  type ReadingDirection,
} from '../../store/viewer'

/**
 * The top overlay (ui-spec §6.6).
 *
 * Fades on **opacity**, never `display:none`: the bar stays mounted so the wake
 * is a 180 ms fade rather than a reflow of three segmented controls on every
 * mouse move. `pointer-events` is what actually turns it off.
 *
 * Responsive (ui-spec §7, and the captured breakage in
 * `viewer-overlay-400-broken.png`, where all three `.seg` groups overflow and
 * their labels break vertically):
 *
 *   ≥1024   all three groups inline
 *   768–1023 fit moves into the `⋯` sheet; mode + direction stay inline
 *   <768    only 뒤로 + the title; every control lives in the `⋯` sheet
 *
 * The overflow panel is a **bottom sheet**, which is what ui-spec §7's <768 row
 * asks for ("all controls move to a bottom sheet opened by a `⋯` button"). The
 * first cut hung it off the top bar as `absolute inset-x-0 top-full`, i.e. a
 * drawer under the header — the furthest point on the screen from the thumb
 * holding a phone, and the one place a reader's next tap is a page turn.
 * `position: fixed` resolves against the viewport, and the viewer root is
 * `fixed inset-0`, so `fixed inset-x-0 bottom-0` is the bottom of the viewer.
 * It is layered over the bottom overlay rather than pushed above it: the sheet
 * is modal-ish chrome that replaces the control row while it is open, and
 * stacking it keeps the geometry independent of that bar's height (which itself
 * changes when the thumbnail strip opens).
 *
 * ## The sheet is a child of a bar that fades itself out
 *
 * Which makes the 2 200 ms chrome auto-hide a trapdoor. The sheet lives inside
 * `[data-role=viewer-top-bar]`, so when the chrome sleeps it keeps its box and
 * its `sheetOpen` state but becomes invisible and `pointer-events: none` —
 * sitting over the left/right page-turn zones (`useTouchZones`, 30 % of the
 * width each). Measured at 400×800: 2.6 s after opening it, a tap at x = 68 hit
 * the page image and turned the page instead of changing the display mode, and
 * because `sheetOpen` was still `true` the next `⋯` press *closed* a sheet the
 * reader had never seen open.
 *
 * Two rules make that state unreachable. While the sheet is open the chrome is
 * **pinned** — `wake()` re-arms the auto-hide from inside its own window —
 * because ui-spec §7's <768 row makes this sheet the only route to 표시 모드 /
 * 읽기 방향 / 맞춤 on a phone, and a panel that dissolves 2.2 s after it opens
 * is not a route to anything. And if the chrome goes away regardless — the
 * reader tapped the centre zone, or the viewport grew until no sheet is needed —
 * the sheet closes **with** it, so it is never invisible but mounted.
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
  const breakpoint = useBreakpoint()
  const wake = useViewerStore((s) => s.wake)
  const holdChrome = useViewerStore((s) => s.holdChrome)
  const releaseChrome = useViewerStore((s) => s.releaseChrome)
  const [sheetOpen, setSheetOpen] = useState(false)

  const inlineModeAndDir = breakpoint !== 'mobile'
  const inlineFit = breakpoint === 'laptop' || breakpoint === 'desktop'
  const needsSheet = !inlineModeAndDir || !inlineFit

  // Pin the chrome for as long as the sheet is open. The first `wake()` is the
  // one that matters: the `⋯` press itself does not wake anything, so without it
  // a sheet opened 200 ms before the timer expires vanishes 200 ms later.
  useEffect(() => {
    if (!sheetOpen || !visible) return undefined
    wake()
    const timer = setInterval(wake, CHROME_AUTOHIDE_MS / 2)
    return () => {
      clearInterval(timer)
    }
  }, [sheetOpen, visible, wake])

  // …and if the chrome goes anyway, or the viewport grew past needing a sheet at
  // all, take the sheet with it rather than leaving it live over a tap zone.
  useEffect(() => {
    if (sheetOpen && (!visible || !needsSheet)) setSheetOpen(false)
  }, [needsSheet, sheetOpen, visible])

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
        // **No `z-index` on this bar.** It hosts the `⋯` bottom sheet, and a
        // positioned ancestor with a z-index is a stacking context: the sheet's
        // own `z-overlay` would then be resolved *inside* the bar instead of
        // against the viewer, and the bottom bar — later in the DOM — would
        // paint over it. Measured: at 400px the thumbnail strip swallowed every
        // click aimed at 표시 모드 in the sheet.
        'flex flex-wrap items-center gap-3 gap-y-2 border-b-2 border-neutral-800 bg-bg px-4 py-2 transition-opacity',
        // E-27: an awake bar is *in the column*, so the stage shrinks to fit
        // beside it instead of being covered by it. Asleep it goes back to
        // being an overlay, so a chromeless viewer is the full height of the
        // screen and the layout does not depend on an invisible box.
        visible
          ? 'relative order-first flex-none opacity-100'
          : 'pointer-events-none absolute inset-x-0 top-0 opacity-0',
      )}
      style={{ transitionDuration: 'var(--chrome-fade)' }}
      onMouseEnter={holdChrome}
      onMouseLeave={releaseChrome}
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

      {inlineModeAndDir && modeSeg}
      {inlineModeAndDir && dirSeg}
      {inlineFit && fitSeg}

      {needsSheet && (
        <Button
          variant="secondary"
          className="border-neutral-700 text-sm"
          aria-expanded={sheetOpen}
          onClick={() => {
            setSheetOpen((open) => !open)
          }}
        >
          ⋯<span className="sr-only">뷰어 컨트롤</span>
        </Button>
      )}

      {needsSheet && sheetOpen && (
        <div
          data-role="viewer-control-sheet"
          className="fixed inset-x-0 bottom-0 z-overlay flex flex-wrap items-center justify-center gap-2 border-t-2 border-neutral-800 bg-bg px-4 py-3"
        >
          {!inlineModeAndDir && modeSeg}
          {!inlineModeAndDir && dirSeg}
          {!inlineFit && fitSeg}
        </div>
      )}
    </div>
  )
}
