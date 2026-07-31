import { useId, useState, type FormEvent } from 'react'

import { useCreateRoot } from '../../api/queries'
import type { RootCreate } from '../../api/types'
import { Button } from '../../components/ds/Button'
import { Input } from '../../components/ds/Input'
import { rootErrorMessage } from './rootErrors'

/**
 * `POST /api/roots` — the one thing the design did not answer: **how the user
 * supplies a path** (amendment **A-11**, ruling **E-26**).
 *
 * A text field for an absolute path plus an optional label, validated by the
 * server. There is deliberately **no filesystem browser and no
 * directory-listing endpoint** — E-26 kept that part of E-3's cost estimate
 * unbought, because a browse API would hand the whole readable directory tree
 * of the host to what ruling E-8 makes an unauthenticated LAN listener by
 * default, and it would be reachable *before* anyone granted the privilege,
 * since browsing is a read. So the path is typed or pasted, and every rule it
 * has to satisfy is checked on the server, where the filesystem is.
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
}

export function AddRootForm({ onDone, onCancel }: AddRootFormProps) {
  const pathId = useId()
  const labelId = useId()
  const create = useCreateRoot()
  const [path, setPath] = useState('')
  const [label, setLabel] = useState('')

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
      <p className="text-xs text-ink-dim">
        서버에서 보이는 절대 경로를 입력하세요. 폴더 찾아보기는 제공하지 않습니다.
      </p>
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

      {/* The one thing this screen can promise before the server answers, and
          the reason the row that appears will say 재시작 후 적용: roots are
          opened once at startup (arch §7.4), so nothing is scanned until then. */}
      <p className="text-xs text-ink-dim">추가한 루트는 서버를 다시 시작한 뒤 읽힙니다.</p>

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
