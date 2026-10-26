import { Button } from '../../components/ds/Button'
import { Dialog } from '../../components/ds/Dialog'
import { CachePanel } from './CachePanel'
import { ProgressPanel } from './ProgressPanel'
import { ReadDefaultsPanel } from './ReadDefaultsPanel'
import { RootsPanel } from './RootsPanel'
import { ScanLogPanel } from './ScanLogPanel'

/**
 * 설정 (prd UI-004, ui-spec §8.6, `settings-dialog-1440.png`).
 *
 * Five sections, in the spec's order: 루트 관리 · 캐시 + 읽기 기본값 (two
 * columns that stack below 768) · 스캔 로그 · 읽기 기록 (ruling E-68, last).
 * Each is its own file so the dialog stays a layout and the panels stay
 * testable on their own.
 *
 * It is a *dialog*, not a route (impl-plan §5.2): a modal in the back-button
 * history is a bug, so open/closed lives in `store/ui.ts`.
 */
export interface SettingsDialogProps {
  open: boolean
  onClose: () => void
}

export function SettingsDialog({ open, onClose }: SettingsDialogProps) {
  return (
    <Dialog
      open={open}
      onClose={onClose}
      width="min(760px, 100%)"
      panelClassName="max-h-[88vh] overflow-y-auto gap-6"
      // ui-spec §8.6: `.dialog-title` `flex:1` `설정` + the `esc` button *beside*
      // it. Beside, not inside — inside, the dialog was named "설정 닫기" (audit #7).
      title="설정"
      titleAside={
        <Button variant="secondary" className="text-sm" aria-label="닫기" onClick={onClose}>
          esc
        </Button>
      }
    >
      {/* Every panel reads its own query — including `RootsPanel`'s config path
          (A-10) — and none of them is mounted until `open`, which is what keeps
          the four settings requests off a page with no dialog on it. */}
      <RootsPanel />

      <div className="flex flex-col gap-6 md:flex-row">
        <CachePanel />
        <ReadDefaultsPanel />
      </div>

      <ScanLogPanel />

      {/* Last by ruling E-68 §2.2. It reads nothing until a button is pressed. */}
      <ProgressPanel />
    </Dialog>
  )
}
