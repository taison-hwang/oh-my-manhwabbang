import { ChevronRight, CornerLeftUp, Folder } from 'lucide-react'
import { useState } from 'react'

import { useBrowse } from '../../api/queries'
import type { BrowseEntry } from '../../api/types'
import { Button } from '../../components/ds/Button'
import { Spinner } from '../../components/ds/Spinner'
import { textLang } from '../../lib/textLang'
import { browseReasonLabel, rootErrorMessage } from './rootErrors'

/**
 * 폴더 찾아보기 — `GET /api/browse`, amendment **A-12** (ruling **E-40**).
 *
 * **This component did not exist before E-40, and the reason it did not is worth
 * keeping.** `AddRootForm` used to say *"폴더 찾아보기는 제공하지 않습니다"*, and
 * that was a ruling rather than an omission: E-26 priced a browse API and left
 * it unbought, because it hands the host's readable tree to what ruling E-8
 * makes an unauthenticated LAN listener by default, and because browsing is a
 * *read* — reachable before anyone granted the write privilege. E-40 buys it
 * under two limits that answer both halves, and the limits live on the server
 * where they cannot be bypassed: the endpoint is behind the same gate as the
 * write, and it lists nothing outside `server.browse_bases`.
 *
 * **What this component may not do is re-implement §7.4's table.** Every row
 * arrives with `selectable` already computed by the server from the same rules
 * `POST /api/roots` applies. A picker that greyed rows out by its own reasoning
 * would eventually disagree with the endpoint, and the disagreement would be
 * invisible until a user clicked a folder the server then refused — the exact
 * shape §6.5 calls a check that watches the wrong thing.
 *
 * **It is a list, not a tree.** One level at a time with a breadcrumb, because
 * the endpoint answers one level at a time: a tree widget would have to either
 * pre-fetch depth it does not need or keep expansion state the server cannot
 * confirm. Walking back up is a cache hit — `queryKeys.browse` is keyed by path.
 */
export interface FolderPickerProps {
  /** Called with the absolute path of the chosen directory. */
  onPick: (path: string) => void
  onCancel: () => void
}

export function FolderPicker({ onPick, onCancel }: FolderPickerProps) {
  // `undefined` is the synthetic top level — the configured bases. It is a
  // distinct query key from any real path, so starting here and coming back
  // here are the same cache entry.
  const [path, setPath] = useState<string | undefined>(undefined)
  const browse = useBrowse(path)

  if (browse.isPending) {
    return (
      <div className="flex items-center gap-2 py-3" data-testid="folder-picker-loading">
        <Spinner label="폴더를 읽는 중" />
        <span className="text-xs text-ink-dim">폴더를 읽는 중…</span>
      </div>
    )
  }

  if (browse.isError) {
    return (
      <div className="flex flex-col items-start gap-2 py-2" data-testid="folder-picker-error">
        {/* The same mapper the add form uses. A `403 no_browse_bases` is not a
            failure of the feature the user asked for — the path can still be
            typed — so the button below says 닫기 and not 다시 시도. */}
        <p role="alert" className="text-xs text-accent-text">
          {rootErrorMessage(browse.error, 'add')}
        </p>
        <Button variant="ghost" onClick={onCancel}>
          닫기
        </Button>
      </div>
    )
  }

  const { entries, parent, self, truncated } = browse.data
  const atTop = browse.data.path === ''

  return (
    <div
      className="flex w-full flex-col gap-2 rounded border border-rule p-2"
      data-testid="folder-picker"
    >
      {/* The current location. At the top level there is no directory to name,
          so it says what the list *is* rather than printing an empty path — an
          empty crumb reads as a path that failed to load. */}
      <div className="flex items-center gap-2">
        <span
          className="min-w-0 flex-1 truncate whitespace-nowrap text-xs text-ink-dim"
          title={atTop ? undefined : browse.data.path}
          data-testid="folder-picker-crumb"
        >
          {atTop ? '탐색할 수 있는 폴더' : browse.data.path}
        </span>
        {parent !== null && (
          <Button
            variant="ghost"
            onClick={() => {
              setPath(parent)
            }}
          >
            <CornerLeftUp size={12} aria-hidden={true} />
            상위
          </Button>
        )}
        {!atTop && (
          // Back to the base list. Distinct from 상위 because `parent` stops at
          // a base — it never walks out of the allowlist — so from a base there
          // is no 상위 at all and this is the only way back.
          <Button
            variant="ghost"
            onClick={() => {
              setPath(undefined)
            }}
          >
            처음으로
          </Button>
        )}
      </div>

      <ul className="flex max-h-[220px] flex-col overflow-y-auto" data-testid="folder-picker-list">
        {entries.length === 0 && (
          <li className="py-2 text-xs text-ink-dim">하위 폴더가 없습니다.</li>
        )}
        {entries.map((entry) => (
          <FolderRow
            key={entry.path}
            entry={entry}
            // At the top level a base's *name* is its last segment, and two
            // bases can share one (`/mnt/a/media`, `/srv/b/media`) — with no
            // crumb above them there is nothing else on screen to tell them
            // apart. Deeper in, the crumb carries the prefix and repeating it
            // on every row would bury the segment that actually varies.
            showFullPath={atTop}
            onOpen={() => {
              setPath(entry.path)
            }}
            onPick={() => {
              onPick(entry.path)
            }}
          />
        ))}
      </ul>

      {/* §6.5: a capped list that does not say it was capped reads as complete. */}
      {truncated && (
        <p className="text-xs text-accent-text" data-testid="folder-picker-truncated">
          폴더가 너무 많아 일부만 표시했습니다. 경로를 직접 입력하세요.
        </p>
      )}

      <div className="flex items-center gap-2 border-t border-rule pt-2">
        {/* Choosing the directory the user is *standing in*, which is not
            reachable from the row list — its own row lives one level up. Absent
            at the top level, where `self` is null because there is no single
            directory to choose. */}
        {self !== null && (
          <Button
            variant="primary"
            disabled={!self.selectable}
            onClick={() => {
              onPick(self.path)
            }}
            data-testid="folder-picker-choose-current"
          >
            이 폴더 선택
          </Button>
        )}
        <Button variant="ghost" onClick={onCancel}>
          취소
        </Button>
        {self !== null && !self.selectable && (
          <span className="text-xs text-ink-dim">{browseReasonLabel(self.reason)}</span>
        )}
      </div>
    </div>
  )
}

/**
 * One directory. Two affordances, because they are two different intentions:
 * **descend into it** and **choose it**.
 *
 * Descending is always allowed — an unselectable folder may still contain the
 * one the user wants, and refusing to open `/mnt/media` because it is already a
 * root would hide every sibling under it. Choosing is gated on `selectable`,
 * and the reason is printed rather than left to a tooltip: the whole point of
 * the server computing it is that the user learns it before the click.
 */
function FolderRow({
  entry,
  onOpen,
  onPick,
  showFullPath = false,
}: {
  entry: BrowseEntry
  onOpen: () => void
  onPick: () => void
  showFullPath?: boolean
}) {
  const reason = browseReasonLabel(entry.reason)

  return (
    <li className="flex items-center gap-2 border-b border-rule py-1 last:border-b-0">
      <button
        type="button"
        className="flex min-w-0 flex-1 items-center gap-2 text-left text-sm hover:text-accent-text"
        onClick={onOpen}
        title={entry.path}
      >
        <Folder size={13} aria-hidden={true} className="flex-none text-ink-dim" />
        <span
          className="truncate whitespace-nowrap"
          lang={textLang(showFullPath ? entry.path : entry.name)}
        >
          {showFullPath ? entry.path : entry.name}
        </span>
        <ChevronRight size={12} aria-hidden={true} className="flex-none text-ink-dim" />
      </button>

      {reason !== null && <span className="flex-none text-xs text-ink-dim">{reason}</span>}

      <Button variant="secondary" disabled={!entry.selectable} onClick={onPick}>
        선택
      </Button>
    </li>
  )
}
