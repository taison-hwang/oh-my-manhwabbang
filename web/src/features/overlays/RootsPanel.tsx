import { Plus, RefreshCw, Trash } from 'lucide-react'
import { useState } from 'react'

import {
  useDeleteRoot,
  useRoots,
  useScanStatus,
  useSettings,
  useStartScan,
} from '../../api/queries'
import type { Root, ScanStatus } from '../../api/types'
import { Button } from '../../components/ds/Button'
import { ProgressBar } from '../../components/ds/ProgressBar'
import { formatBytes, formatCount, formatScanLabel, scanPercent } from '../../lib/format'
import { AddRootForm } from '../roots/AddRootForm'
import { rootErrorMessage } from '../roots/rootErrors'
import { scanStartErrorMessage } from '../roots/scanErrors'

/**
 * 루트 관리 (ui-spec §8.6 §1, prd UI-004), **as amended by A-11 / ruling E-26**.
 *
 * The panel used to open by saying roots were read-only, because prd FR-CFG-001
 * makes the YAML the source of roots and prd 6.3 had no `POST /api/roots`.
 * E-26 reversed that in part: the owner of the requirement extended the
 * requirement, and the screen may now add and remove entries in the `roots:`
 * list. Everything else D-33 and E-3 decided still stands — this is not a
 * general configuration editor, `enabled`/`name`/`path`/`label` of an existing
 * root stay file-only, and there is no filesystem browser.
 *
 * Three facts shape every line below, and all three come off the wire:
 *
 *  * **The controls are gated on `Settings.server.root_editing_enabled`** and
 *    are *absent*, never disabled, when it is false. That is the same rule that
 *    removed them under E-3 — a disabled control is a promise, and this promise
 *    cannot be kept from the UI at all: no click, no wait and no later request
 *    lifts a refusal that lives in a YAML key on the server. When the gate is
 *    shut this panel is exactly what C-5 and E-3 have always described.
 *  * **A `pending` row is a root the file has and the server has not** (R2).
 *    Roots are opened once at startup, so `POST` cannot make one servable; the
 *    row says 재시작 후 적용, carries no counts (§7.3 fixes them at zero, and
 *    zero is not a count) and offers no 재스캔. 제거 stays, because a root added
 *    with the wrong path must be removable before the restart — and `DELETE`
 *    answers on it, since it *is* in the file on disk.
 *  * **A removal takes effect now** (R1). The YAML entry goes and so do that
 *    root's `index.db` rows, so the row and its series disappear at once;
 *    `user.db` is untouched, so the reading progress survives and reattaches if
 *    the same directory is added again. The confirmation says the second and
 *    must not promise the first.
 *
 * Both queries are read here rather than taken as props (ruling E-25): a
 * `useSettings()` in `SettingsDialog` or `Overlays` would fetch on every page,
 * because `Overlays` mounts the dialog permanently.
 *
 * Per-root rescan is `POST /api/scan {roots:[name]}` (§7.10); the series-level
 * `POST /api/series/{sid}/rescan` is a different endpoint and belongs to the
 * detail screen. That button owns two things beyond firing the request, both
 * added because it had neither and both invisible until it was pressed: the
 * run's **progress** (`ScanProgress` below — FR-IDX-004 is met twice already,
 * and both renderers are behind this dialog's scrim) and the request's
 * **failure** (`scanErrors.ts` — all four of §7.10's refusals used to render
 * nothing at all).
 */
interface RootRowProps {
  root: Root
  onRescan: (name: string) => void
  scanDisabled: boolean
  /** `Settings.server.root_editing_enabled` — the 제거 button exists only under it. */
  editable: boolean
}

function RootRow({ root, onRescan, scanDisabled, editable }: RootRowProps) {
  const remove = useDeleteRoot()
  const [confirming, setConfirming] = useState(false)

  return (
    <div className="flex flex-col gap-2 border-b border-rule py-2" data-testid={`root-row-${root.name}`}>
      <div className="flex items-center gap-3">
        <div className="flex min-w-0 flex-1 flex-col gap-[2px]">
          <span className="text-base">{root.label}</span>
          <span className="truncate whitespace-nowrap text-xs text-ink-dim" title={root.path}>
            {root.path}
          </span>
          {root.last_scan_error !== null && (
            <span className="truncate whitespace-nowrap text-xs text-accent-text">
              {root.last_scan_error}
            </span>
          )}
        </div>

        {root.pending ? (
          // Not a count and not a status badge: it is the answer to "why does
          // this row have no numbers", and the restart notice below the list is
          // what tells the reader how to make it real.
          <span className="flex-none text-xs text-accent-text">재시작 후 적용</span>
        ) : (
          <span className="flex-none text-xs tabular-nums text-ink-muted">
            {`${formatCount(root.series_count)} · ${formatBytes(root.total_bytes)}`}
          </span>
        )}

        {/* No 재스캔 for a pending root — there is nothing open to scan, and
            `POST /api/scan` for it would be a request the server cannot honour. */}
        {!root.pending && (
          <Button
            variant="secondary"
            disabled={scanDisabled || !root.available || !root.enabled}
            onClick={() => {
              onRescan(root.name)
            }}
          >
            <RefreshCw size={12} aria-hidden={true} />
            재스캔
          </Button>
        )}

        {editable && !confirming && (
          <Button
            variant="ghost"
            onClick={() => {
              setConfirming(true)
            }}
          >
            <Trash size={12} aria-hidden={true} />
            제거
          </Button>
        )}
      </div>

      {confirming && (
        <div className="flex flex-col items-start gap-2 pb-1">
          {/* One alert, not two: on failure it carries the server's reason, and
              a second live region beside it would make `role="alert"` ambiguous
              for a screen reader as well as for a test. */}
          <p role="alert" className="text-xs text-accent-text">
            {remove.isError
              ? rootErrorMessage(remove.error, 'remove')
              : // R1, stated exactly: the series go now, the progress does not.
                `‘${root.label}’ 루트를 제거할까요? 이 루트의 시리즈가 목록에서 바로 사라집니다. 읽기 진행률은 유지됩니다 — 같은 폴더를 다시 추가하면 다시 이어집니다.`}
          </p>
          <div className="flex gap-2">
            <Button
              variant="primary"
              disabled={remove.isPending}
              onClick={() => {
                remove.mutate(root.name)
              }}
            >
              제거
            </Button>
            <Button
              variant="ghost"
              disabled={remove.isPending}
              onClick={() => {
                remove.reset()
                setConfirming(false)
              }}
            >
              취소
            </Button>
          </div>
        </div>
      )}
    </div>
  )
}

/**
 * FR-IDX-004 inside the modal — 대상 수, 완료 수, 현재 항목, live.
 *
 * **Why it has to be here.** The requirement was already met twice: `TopBar`
 * draws a 96×2px bar and `ScanIndicator` prints `스캔 중 {done} / {total}` in the
 * sidebar footer. Both of them are *underneath* `.dialog-backdrop`. The 재스캔
 * button is inside it. So the one screen from which a user starts a scan was the
 * one screen that could not watch it, and pressing the button produced nothing
 * visible at all — the defect the user reported.
 *
 * **This is whole-run progress, and it says so.** `ScanStatus` has no per-root
 * breakdown: `PerRoot` exists in the scanner and is dropped at the HTTP
 * boundary, and `Root` carries no scan state. `current_root` names the root the
 * run is *inside* right now, which is why it is printed as part of the current
 * item's path rather than beside any row. Nothing here may sit in a row: a
 * progress bar on one root's line is a claim about that root that the API
 * cannot answer.
 *
 * **Numbers, not new numbers.** `formatScanLabel` and `scanPercent`
 * (`lib/format.ts`) are the sidebar's and the top bar's, tested against the
 * ui-spec §9 catalogue — reusing them is what keeps this block and the two
 * behind the scrim from disagreeing by a rounding rule. `scanPercent` floors,
 * so the printed `%` and `ProgressBar`'s `aria-valuenow` are the same integer.
 *
 * `ProgressBar` rather than `TopBar`'s hand-rolled `<span>`: it carries
 * `role="progressbar"` and the three `aria-value*`, which the top bar's does
 * not. During the `covers` phase `done`/`total` are books and sit at 100 % while
 * covers finish; that matches the sidebar exactly, and inventing a second
 * denominator here would make the two disagree.
 *
 * Rendered **only** while a run is in flight. Idle copy belongs to
 * `ScanIndicator`, which owns it, and a permanent row here would be a second
 * place to keep that sentence in step.
 */
function ScanProgress({ scan }: { scan: ScanStatus }) {
  const pct = scanPercent(scan)
  // Both are `string | null` on the wire and both are absent between phases.
  const where = [scan.current_root, scan.current_item].filter(
    (part): part is string => part !== null && part !== '',
  )

  return (
    <div className="flex flex-col gap-1 pb-1" data-testid="scan-progress">
      <div className="flex items-baseline gap-3">
        {/* Deliberately **not** a live region. This text changes on every 1 s
            poll, and `role="status"` / `aria-live="polite"` on it would queue a
            fresh announcement every second for the whole run — hostile, not
            helpful. The accessible semantics live on the `ProgressBar` below,
            which exposes the value on demand instead of announcing it. */}
        <span className="flex-1 text-xs text-accent-text">{formatScanLabel(scan)}</span>
        <span className="flex-none text-xs tabular-nums text-ink-muted">
          {`${pct.toString()}%`}
        </span>
      </div>

      <ProgressBar value={pct / 100} height={6} label="스캔 진행률" />

      {where.length > 0 && (
        <span
          className="truncate whitespace-nowrap text-xs text-ink-dim"
          title={where.join(' · ')}
          data-testid="scan-current-item"
        >
          {where.join(' · ')}
        </span>
      )}
    </div>
  )
}

export function RootsPanel() {
  const roots = useRoots()
  const startScan = useStartScan()
  const settings = useSettings()
  const scan = useScanStatus()
  const [adding, setAdding] = useState(false)

  // `undefined` (not answered yet) and `''` — which arch §7.8 permits for a
  // server built from a configuration with no file — are one fact: no file is
  // known. Folding them here is what keeps `''` from reaching the DOM as an
  // empty `<span data-testid="config-path">` followed by a dangling ` — `,
  // which reads as a path that failed to load rather than as one that does not
  // exist, and which the testid assertions below would have accepted.
  const raw = settings.data?.server.config_path
  const configPath = raw === undefined || raw === '' ? null : raw

  // Both default to `false` while the payload is in flight, and that is the
  // safe direction for each: no controls rather than controls that vanish, and
  // no notice rather than one that flashes.
  const editable = settings.data?.server.root_editing_enabled === true
  const configChanged = settings.data?.server.config_changed_on_disk === true

  // arch §7.10 has no `"done"` state: a finished run is `idle` with
  // `finished_at` set. `state !== 'idle'` is therefore the whole test, and it is
  // the same one `useScanStatus` polls on and `SeriesDetailPage` disables on.
  const running = scan.data !== undefined && scan.data.state !== 'idle'

  return (
    <section className="flex flex-col gap-2">
      <h6>루트 관리</h6>

      {/* Above the rows, not below them: this is the answer to the button the
          user just pressed, and the dialog scrolls. */}
      {running && <ScanProgress scan={scan.data} />}

      {/* Every refusal `POST /api/scan` can give (arch §7.10) used to render
          nothing whatsoever. One alert for the panel, because the mutation is
          the panel's — `useStartScan` is held here, not per row, so a second
          live region per row would be the same message N times. */}
      {startScan.isError && (
        <p role="alert" className="text-xs text-accent-text" data-testid="scan-start-error">
          {scanStartErrorMessage(startScan.error)}
        </p>
      )}

      {roots.data?.items.map((root) => (
        <RootRow
          key={root.name}
          root={root}
          editable={editable}
          // `isPending` alone covered only the milliseconds the request is in
          // flight. A scan runs for *seconds*, and the button was live for all
          // of them — so the natural second press during a run was answered by
          // a silent `409`. A disabled control is a promise this one can keep:
          // the run ends, the poll reports idle, the button comes back.
          scanDisabled={startScan.isPending || running}
          onRescan={(name) => {
            startScan.mutate({ roots: [name] })
          }}
        />
      ))}

      {editable &&
        (adding ? (
          <AddRootForm
            onDone={() => {
              setAdding(false)
            }}
            onCancel={() => {
              setAdding(false)
            }}
          />
        ) : (
          <Button
            variant="secondary"
            className="mt-2 self-start"
            onClick={() => {
              setAdding(true)
            }}
          >
            <Plus size={13} aria-hidden={true} />
            루트 추가
          </Button>
        ))}

      {/* `config_changed_on_disk` is the server's state, not this tab's, so the
          notice survives a browser reload and is equally true when the user
          hand-edited the file — the workflow C-5 has been printing all along.
          It flips on a comment edit too, which is why it says the file changed
          and never that the user must restart. It is therefore rendered
          regardless of the gate: a hand-edit is not a write of ours. */}
      {configChanged && (
        <p className="pt-2 text-xs text-accent-text" data-testid="config-changed-notice">
          설정 파일이 변경되었습니다 — 서버를 다시 시작하면 적용됩니다.
        </p>
      )}

      <p className="pt-2 text-xs text-ink-dim">
        {configPath === null ? (
          // Nothing to name. The note alone is thin, but a *guessed* path is
          // worse than none — guessing is the bug this field closes.
          'shelf.yaml을 편집한 뒤 재시작하세요'
        ) : (
          <>
            <span className="tabular-nums" data-testid="config-path">
              {configPath}
            </span>
            {' — shelf.yaml을 편집한 뒤 재시작하세요'}
          </>
        )}
      </p>
    </section>
  )
}
