import {
  BookOpen,
  CheckCheck,
  Clock,
  Command,
  FolderOpen,
  Settings,
  type LucideIcon,
} from 'lucide-react'

import { cn } from '../../lib/cn'
import { formatCount } from '../../lib/format'
import type { ShellListCounts, ShellRoot } from '../../lib/shellData'
import type { Scope } from '../../store/ui'
import { Button } from '../ds/Button'
import { VisuallyHidden } from '../ds/VisuallyHidden'
import { Wordmark } from '../ds/Wordmark'
import { ScanIndicator } from './ScanIndicator'

/**
 * The application sidebar (ui-spec §4.1) with the responsive behaviour of §7.
 *
 *   >=1024   240px fixed, labels and counts
 *   768–1023 `variant="rail"` — 56px, icons only; the scope name moves into the
 *            section header (WP-09 renders that)
 *   <768     not rendered at all; `MobileDrawer` hosts the full variant
 *
 * Smart lists are fixed in order and semantics (ui-spec §4.1) and are served by
 * `GET /api/series?progress=…` (amendment A-4) rather than filtered client-side,
 * which FR-LIB-007 would break.
 */

/** Fixed order, fixed semantics. `added` is `sort=added&order=desc`. */
const SMART_LISTS = [
  { key: 'reading', label: '읽는 중', Icon: BookOpen },
  { key: 'added', label: '최근 추가', Icon: Clock },
  { key: 'done', label: '완독', Icon: CheckCheck },
] as const

/**
 * What a `pending: true` root says instead of a count (A-11 / R2, ruling E-26).
 *
 * The same words `RootsPanel` uses, deliberately: 루트 추가 makes a row appear in
 * both places at once, and two different explanations of one fact read as two
 * facts.
 */
const PENDING_NOTE = '재시작 후 적용'

export interface SidebarProps {
  roots: ShellRoot[]
  counts: ShellListCounts
  scope: Scope
  onScopeChange: (scope: Scope) => void
  scanning: boolean
  scanLabel: string
  onOpenScanLog: () => void
  onOpenSettings: () => void
  onOpenShortcuts: () => void
  variant?: 'full' | 'rail'
  className?: string
}

interface NavRowProps {
  label: string
  /**
   * `undefined` is a row with no number to show, not a row showing nothing —
   * spelled out because `exactOptionalPropertyTypes` otherwise refuses the
   * pending root's absent count at the call site.
   */
  count?: number | undefined
  active: boolean
  rail: boolean
  Icon: LucideIcon
  onSelect: () => void
  /**
   * A root the configuration file has and this server has not opened yet
   * (A-11 / R2). The row is `disabled`, and that is a promise the product keeps:
   * a restart opens the root and the row becomes an ordinary selectable one. So
   * the row has to *say* the condition and the remedy, which is what
   * `PENDING_NOTE` is — it replaces the count in the full sidebar and joins the
   * accessible name in the rail, where there is no visible text at all.
   */
  pending?: boolean
}

function NavRow({ label, count, active, rail, Icon, onSelect, pending = false }: NavRowProps) {
  const name = pending ? `${label} — ${PENDING_NOTE}` : label
  return (
    <button
      type="button"
      className="sidebar-nav-row"
      data-active={active ? 'true' : 'false'}
      data-pending={pending ? 'true' : undefined}
      aria-current={active ? 'page' : undefined}
      // Not `aria-disabled`: nothing here would still act on a click, and a
      // control that announces itself disabled and then works is worse than one
      // that cannot be reached. `disabled` is also what keeps a pending root out
      // of the tab order, where every stop is meant to lead somewhere.
      disabled={pending}
      title={rail ? name : undefined}
      onClick={onSelect}
    >
      {rail ? (
        <>
          <Icon size={16} aria-hidden={true} />
          <VisuallyHidden>{name}</VisuallyHidden>
        </>
      ) : (
        <>
          {/* The glyph is in the full row too, not only the rail: it is what
              tells 루트 rows from 목록 rows at a glance, and the two sections
              are otherwise identical stacks of name + number. */}
          <Icon size={13} aria-hidden={true} className="flex-none text-ink-dim" />
          <span className="min-w-0 flex-1 truncate whitespace-nowrap">{label}</span>
          {pending ? (
            // Not a count and not a badge: it is the answer to "why has this row
            // no number", and §7.3 fixes a pending root's counts at zero — a
            // literal `0` beside a folder full of books is the phantom row.
            <span className="flex-none whitespace-nowrap text-3xs text-accent-text">
              {PENDING_NOTE}
            </span>
          ) : (
            count !== undefined && (
              <span className="text-xs tabular-nums text-ink-dim">{formatCount(count)}</span>
            )
          )}
        </>
      )}
    </button>
  )
}

export function Sidebar({
  roots,
  counts,
  scope,
  onScopeChange,
  scanning,
  scanLabel,
  onOpenScanLog,
  onOpenSettings,
  onOpenShortcuts,
  variant = 'full',
  className,
}: SidebarProps) {
  const rail = variant === 'rail'
  const smartCounts: Record<(typeof SMART_LISTS)[number]['key'], number> = counts

  return (
    <aside
      className={cn('sidebar', rail && 'sidebar-rail', className)}
      aria-label="라이브러리 탐색"
    >
      {/* Brand — the bar mark is one of the few places red runs as a solid field
          (ui-spec §2.5). The rail drops the name to the accessible layer rather
          than shrinking it: 68px cannot hold it at any legible size. */}
      <div
        className={cn(
          'flex items-center gap-2 border-b-2 border-rule-strong',
          rail ? 'justify-center px-0 py-3' : 'px-4 py-3',
        )}
      >
        <Wordmark variant={rail ? 'mark' : 'compact'} />
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto pb-4">
        {!rail && (
          <div className="px-4 pb-1 pt-3 text-3xs uppercase tracking-[.1em] text-ink-dim">루트</div>
        )}
        {rail && <div className="h-3" />}

        {roots.map((root) => (
          <NavRow
            key={root.name}
            label={root.label === '' ? root.name : root.label}
            // A pending root has no count to show — §7.3 fixes all four at zero
            // until the restart, and zero is not a count.
            count={root.pending ? undefined : root.series_count}
            pending={root.pending}
            // It cannot be the current scope either: it is not selectable, and a
            // stale `library_scope` naming it would otherwise paint an accent bar
            // on a row the user cannot leave by clicking a neighbour it looks like.
            active={!root.pending && scope === root.name}
            rail={rail}
            Icon={FolderOpen}
            onSelect={() => {
              onScopeChange(root.name)
            }}
          />
        ))}

        {!rail && (
          <div className="px-4 pb-1 pt-4 text-3xs uppercase tracking-[.1em] text-ink-dim">목록</div>
        )}
        {rail && <div className="h-4" />}

        {SMART_LISTS.map(({ key, label, Icon }) => (
          <NavRow
            key={key}
            label={label}
            count={smartCounts[key]}
            active={scope === key}
            rail={rail}
            Icon={Icon}
            onSelect={() => {
              onScopeChange(key)
            }}
          />
        ))}
      </div>

      <div
        className={cn(
          'flex flex-col gap-2 border-t-2 border-rule-strong',
          rail ? 'items-center px-1 py-3' : 'px-4 py-3',
        )}
      >
        <ScanIndicator
          scanning={scanning}
          label={scanLabel}
          onOpenLog={onOpenScanLog}
          compact={rail}
        />
        <div className={cn('flex gap-2', rail && 'flex-col')}>
          <Button
            variant="secondary"
            icon={rail}
            className={cn('gap-[7px] text-sm', !rail && 'flex-1 justify-start')}
            aria-label="설정"
            onClick={onOpenSettings}
          >
            <Settings size={rail ? 16 : 14} aria-hidden={true} />
            {!rail && '설정'}
          </Button>
          <Button
            variant="secondary"
            icon={true}
            className="text-sm"
            aria-label="키보드 단축키"
            onClick={onOpenShortcuts}
          >
            {/* The ⌘ glyph, not a `?`: this opens the shortcut sheet, and the
                mark of a keyboard shortcut is the thing the sheet is about. */}
            <Command size={rail ? 16 : 13} aria-hidden={true} />
          </Button>
        </div>
      </div>
    </aside>
  )
}
