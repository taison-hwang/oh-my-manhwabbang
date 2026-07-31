import { Dialog } from '../../components/ds/Dialog'
import { Hr } from '../../components/ds/Hr'
import { commandKeyHint } from '../../lib/platform'

/**
 * The keyboard-shortcut sheet (ui-spec §8.5, `shortcuts-dialog-1440.png`).
 *
 * Entry-for-entry from the spec, in the spec's order. The two-column grid fills
 * row-wise, so the DOM order below reproduces the screenshot's pairing:
 * `← →`/`Space`, `T`/`F`, `Esc`/`⌘K`, `1 2 3`/`?`.
 *
 * The command-palette chip is the one thing that is *not* literal: the spec
 * prints `⌘K`, but `lib/platform.ts` exists precisely so a Linux or Windows
 * reader is told `Ctrl K` instead of a key their keyboard does not have.
 */
export interface ShortcutsDialogProps {
  open: boolean
  onClose: () => void
}

interface Shortcut {
  keys: string
  label: string
}

/**
 * ui-spec §8.5, in order, extended by ruling **E-27**.
 *
 * The last three rows are the ones E-27 made load-bearing rather than merely
 * informative. After it the chrome is not summoned by moving the mouse and the
 * viewer opens without it, so this sheet is where a reader finds out that the
 * screen has a left half, a right half and a middle — and that `H` is the way
 * to the controls without touching the page at all.
 */
function shortcutEntries(): readonly Shortcut[] {
  return [
    { keys: '← →', label: '이전 / 다음 페이지' },
    { keys: 'Space', label: '다음 페이지' },
    { keys: 'T', label: '썸네일' },
    { keys: 'F', label: '전체화면' },
    { keys: 'Esc', label: '뷰어 나가기' },
    { keys: commandKeyHint(), label: '커맨드 팔레트' },
    { keys: '1 2 3', label: '단면 / 양면 / 세로' },
    { keys: 'H', label: '컨트롤 표시 / 숨기기' },
    { keys: '좌 / 우 클릭', label: '이전 / 다음 페이지' },
    { keys: '가운데 클릭', label: '컨트롤 토글' },
    { keys: '?', label: '키보드 단축키' },
  ]
}

export function ShortcutsDialog({ open, onClose }: ShortcutsDialogProps) {
  return (
    <Dialog
      open={open}
      onClose={onClose}
      width="min(560px, 100%)"
      title={
        <span className="flex items-baseline gap-3">
          키보드 단축키
          <span className="text-3xs uppercase tracking-[.12em] text-accent">뷰어</span>
        </span>
      }
    >
      <Hr className="my-0" />
      <div data-testid="shortcut-list" className="grid grid-cols-2 gap-x-6 gap-y-2">
        {shortcutEntries().map((entry) => (
          <div
            // Keyed on the chord, not the label: E-27 added a mouse row that
            // does the same thing the arrow keys do and therefore says the same
            // words, and two siblings with one key is a React bug waiting.
            key={entry.keys}
            className="flex items-center gap-3 border-b border-rule pb-[6px]"
          >
            <kbd className="min-w-[52px] bg-ink px-[7px] py-[2px] text-center text-sm font-normal text-bg">
              {entry.keys}
            </kbd>
            <span className="text-base text-ink">{entry.label}</span>
          </div>
        ))}
      </div>
    </Dialog>
  )
}
