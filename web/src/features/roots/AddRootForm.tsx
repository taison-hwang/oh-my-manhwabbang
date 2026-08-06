import { FolderOpen } from 'lucide-react'
import { useId, useState, type FormEvent } from 'react'

import { useCreateRoot } from '../../api/queries'
import type { RootCreate } from '../../api/types'
import { Button } from '../../components/ds/Button'
import { Input } from '../../components/ds/Input'
import { FolderPicker } from './FolderPicker'
import { rootErrorMessage } from './rootErrors'

/**
 * `POST /api/roots` — the one thing the design did not answer: **how the user
 * supplies a path** (amendment **A-11**, ruling **E-26**; picker and hot add by
 * **A-12**, ruling **E-40**).
 *
 * A text field for an absolute path plus an optional label, validated by the
 * server — and, since E-40, a 찾아보기 button beside it.
 *
 * **The comment here used to say there was deliberately no browser.** That was
 * a ruling and not an omission: E-26 priced a browse API and left it unbought,
 * because it would hand the host's readable tree to what ruling E-8 makes an
 * unauthenticated LAN listener by default, and because browsing is a *read* and
 * would therefore be reachable before anyone granted the write privilege. E-40
 * overturns it under two limits that answer both halves — the endpoint sits
 * behind the same gate as the write, and it lists nothing outside
 * `server.browse_bases`. **The typed field stays**, and stays primary: it is
 * the only way to reach a directory outside the configured bases, and the
 * picker is an accelerator over it rather than a replacement for it.
 *
 * `name` is not a field. It is server-generated (§7.4) because it is hashed
 * into every `series_id` and `book_id`, so a client that picked it could
 * silently reattach a new directory to another root's reading progress.
 *
 * The form is shared by the settings panel and the first-run screen. Only the
 * trigger differs — secondary below the list, primary on onboarding — so the
 * trigger stays with the caller and this component is the part that must not
 * be written twice.
 */
export interface AddRootFormProps {
  /** Called once the server accepted the entry **and** the list has been re-read. */
  onDone: () => void
  onCancel: () => void
  /**
   * `Settings.server.root_editing_enabled` — the picker's gate.
   *
   * The browse endpoint refuses with `403` unless root editing is on *and*
   * `server.browse_bases` is set, and only the first of those is visible to the
   * client. So the button is rendered whenever editing is on, and a server with
   * no bases answers `no_browse_bases`, which the picker prints as the sentence
   * naming the key to set. Hiding the button on that server instead would leave
   * the operator with no way to discover the feature exists.
   */
  canBrowse?: boolean
}

export function AddRootForm({ onDone, onCancel, canBrowse = false }: AddRootFormProps) {
  const pathId = useId()
  const labelId = useId()
  const create = useCreateRoot()
  const [path, setPath] = useState('')
  const [label, setLabel] = useState('')
  const [browsing, setBrowsing] = useState(false)

  const trimmedPath = path.trim()

  const submit = (event: FormEvent<HTMLFormElement>): void => {
    event.preventDefault()
    if (trimmedPath === '' || create.isPending) return
    const trimmedLabel = label.trim()
    // `label` is optional in `RootCreate`, and an *absent* optional field is
    // not an unknown one — §7.1's strict decoding rejects extras, not absences.
    // An empty string is a value, though, and §7.4 would write it to the file
    // as the root's display name, so the key is omitted rather than blanked.
    const body: RootCreate = trimmedLabel === '' ? { path: trimmedPath } : {
      path: trimmedPath,
      label: trimmedLabel,
    }
    create.mutate(body, {
      onSuccess: () => {
        setPath('')
        setLabel('')
        onDone()
      },
    })
  }

  return (
    <form className="flex w-full flex-col items-start gap-2" onSubmit={submit} noValidate>
      {/* The `w-full` sits on a wrapper, not on the `Input` — the same shape as
          `LoginScreen`, and for the same reason the prefetch slider needs it:
          `.input`'s own `width: 100%` resolves against `.field`, and `.field` is
          a shrink-to-fit flex item under `items-start`, so a full-width class on
          the input would resolve to the width of its label text. */}
      <div className="w-full">
        <Input
          id={pathId}
          label="루트 경로"
          value={path}
          autoComplete="off"
          spellCheck={false}
          placeholder="/mnt/media/books"
          onChange={(e) => {
            setPath(e.target.value)
          }}
        />
      </div>
      <div className="flex items-center gap-2">
        <p className="flex-1 text-xs text-ink-dim">서버에서 보이는 절대 경로를 입력하세요.</p>
        {canBrowse && !browsing && (
          <Button
            variant="secondary"
            onClick={() => {
              setBrowsing(true)
            }}
            data-testid="browse-folders"
          >
            <FolderOpen size={13} aria-hidden={true} />
            찾아보기
          </Button>
        )}
      </div>

      {/* Picking fills the field rather than submitting: the label is still to
          be typed, and a picker that added the root on click would make the one
          irreversible-looking control in this form the one with no confirm. */}
      {browsing && (
        <FolderPicker
          onPick={(picked) => {
            setPath(picked)
            setBrowsing(false)
          }}
          onCancel={() => {
            setBrowsing(false)
          }}
        />
      )}
      <div className="w-full">
        <Input
          id={labelId}
          label="이름표 (선택)"
          value={label}
          autoComplete="off"
          onChange={(e) => {
            setLabel(e.target.value)
          }}
        />
      </div>

      {create.isError && (
        <p role="alert" className="text-xs text-accent-text">
          {rootErrorMessage(create.error, 'add')}
        </p>
      )}

      {/* E-40 replaced this sentence, and the replacement is deliberately weaker
          than the promise it could make. The server now opens the root and
          starts scanning it — but the adoption can fail after the file write
          (an unmounted path, a directory that stopped being readable between
          the stat and the open), and then the row falls back to A-11's
          재시작 후 적용. The row itself is the truthful answer, because it is
          computed from what the server actually did; this line must not
          contradict it in advance. */}
      <p className="text-xs text-ink-dim">추가하면 바로 읽기 시작합니다.</p>

      <div className="flex gap-2">
        {/* Disabled only while there is genuinely nothing to send, or while the
            send is in flight — both promises this control can keep. */}
        <Button type="submit" variant="primary" disabled={trimmedPath === '' || create.isPending}>
          추가
        </Button>
        <Button variant="ghost" disabled={create.isPending} onClick={onCancel}>
          취소
        </Button>
      </div>
    </form>
  )
}
