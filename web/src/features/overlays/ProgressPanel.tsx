import { useRef, useState, type ChangeEvent } from 'react'

import { useExportProgress, useImportProgress } from '../../api/queries'
import type { ImportStrategy, ProgressExport, ProgressImportResult } from '../../api/types'
import { Button } from '../../components/ds/Button'
import { Seg, type SegOption } from '../../components/ds/Seg'
import { formatDate, formatVolumeCount } from '../../lib/format'
import {
  exportErrorMessage,
  FILE_TOO_LARGE_MESSAGE,
  FILE_UNREADABLE_MESSAGE,
  importErrorMessage,
} from './progressErrors'

/**
 * 읽기 기록 — export the reading history to a file and import one back
 * (FR-STT-004, ui-spec §8.6 item 4, ruling E-68). The dialog's last section.
 *
 * Both directions go through `api/client.ts` (D-44), never a link: a link
 * would save a failure's error envelope under the backup's file name, where
 * the function turns it into a sentence (E-68 §2.4).
 *
 * Importing is confirmed first, as 캐시 `전체 삭제` is: `user.db` is the only
 * data the product authors, and nothing on screen can undo an import. Picking
 * a file therefore sends nothing; the browser only refuses the two shapes it
 * must read anyway to count the books (E-68 §2.5) and leaves every other check
 * — `format`, `id_version`, enums, unknown fields — to the server, so the two
 * cannot disagree.
 */

/** arch §8's import body cap (`maxImportBody`). A larger file is refused unread. */
const MAX_IMPORT_FILE_BYTES = 33_554_432

const STRATEGY_OPTIONS: readonly SegOption<ImportStrategy>[] = [
  { value: 'merge', label: '병합' },
  { value: 'replace', label: '덮어쓰기' },
]

const STRATEGY_NOTES: Readonly<Record<ImportStrategy, string>> = {
  merge: '같은 책은 더 최근 기록을 남깁니다. 이 서버의 책별 보기 설정은 그대로 둡니다.',
  replace: '같은 책은 파일의 기록과 보기 설정으로 바꿉니다. 파일에 없는 책은 그대로 둡니다.',
}

/** The one line under the row: a status or an alert, never both. */
interface Message {
  role: 'status' | 'alert'
  text: string
}

/** A file that passed the browser's two checks, waiting for 가져오기. */
interface PickedFile {
  name: string
  payload: ProgressExport
}

/** `shelf-progress-YYYYMMDD.json`, the date being `exported_at` in local time. */
function exportFileName(exportedAt: number): string {
  return `shelf-progress-${formatDate(exportedAt).replaceAll('-', '')}.json`
}

/** Hands `doc` to the browser as a download, through a temporary `<a download>`. */
function saveAsFile(doc: ProgressExport, fileName: string): void {
  const url = URL.createObjectURL(new Blob([JSON.stringify(doc)], { type: 'application/json' }))
  const link = document.createElement('a')
  link.href = url
  link.download = fileName
  link.hidden = true
  document.body.append(link)
  link.click()
  link.remove()
  // Released on the next task, once the download has taken the blob.
  window.setTimeout(() => {
    URL.revokeObjectURL(url)
  }, 0)
}

function readAsText(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => {
      resolve(typeof reader.result === 'string' ? reader.result : '')
    }
    reader.onerror = () => {
      reject(reader.error ?? new Error('the file could not be read'))
    }
    reader.readAsText(file)
  })
}

/**
 * The browser's half of the checks: a JSON object whose `items` is an array.
 * The object goes to the server exactly as read — nothing is dropped or
 * defaulted, so an unknown field still reaches the check that refuses it.
 */
function parseProgressFile(text: string): ProgressExport | null {
  let value: unknown
  try {
    value = JSON.parse(text)
  } catch {
    return null
  }
  if (typeof value !== 'object' || value === null || Array.isArray(value)) return null
  const items: unknown = (value as Record<string, unknown>).items
  if (!Array.isArray(items)) return null
  return value as ProgressExport
}

/** The result line. The counters describe `items` only (A-17). */
function importResultMessage({ imported, skipped, conflicts }: ProgressImportResult): string {
  if (imported + skipped === 0) return '파일에 책 기록이 없습니다.'
  const parts = [`책 ${formatVolumeCount(imported)}의 기록을 가져왔습니다.`]
  // `conflicts` counts every incoming row that already existed; the ones
  // merge kept local are `skipped`, so the rest are the ones it replaced.
  const replaced = conflicts - skipped
  if (replaced > 0) parts.push(`그중 ${formatVolumeCount(replaced)}은 이 서버에 있던 기록을 바꿨습니다.`)
  if (skipped > 0) {
    parts.push(`이 서버의 기록이 더 최근인 ${formatVolumeCount(skipped)}은 그대로 두었습니다.`)
  }
  return parts.join(' ')
}

export function ProgressPanel() {
  const exportProgress = useExportProgress()
  const importProgress = useImportProgress()
  const fileInput = useRef<HTMLInputElement>(null)
  const [picked, setPicked] = useState<PickedFile | null>(null)
  const [strategy, setStrategy] = useState<ImportStrategy>('merge')
  const [message, setMessage] = useState<Message | null>(null)
  // Bumped by every pick and by 취소. A read started under an older value
  // answers for a file the user has since replaced or dismissed: only the
  // latest pick counts.
  const pickGeneration = useRef(0)

  const onExport = (): void => {
    setMessage(null)
    // The promise, not `mutate`'s callbacks: TanStack drops those once the
    // dialog has closed, and closing it does not cancel the file the user
    // asked for. The line set then has no screen left, which React ignores.
    void exportProgress.mutateAsync().then(
      (doc) => {
        saveAsFile(doc, exportFileName(doc.exported_at))
        setMessage({
          role: 'status',
          text: `책 ${formatVolumeCount(doc.items.length)}의 기록을 내보냈습니다.`,
        })
      },
      (error: unknown) => {
        setMessage({ role: 'alert', text: exportErrorMessage(error) })
      },
    )
  }

  const onPick = (event: ChangeEvent<HTMLInputElement>): void => {
    const input = event.currentTarget
    const file = input.files?.[0]
    // Cleared after every pick: a browser fires no change for the same file
    // picked twice in a row, and picking it again must work.
    input.value = ''
    if (file === undefined) return
    pickGeneration.current += 1
    const generation = pickGeneration.current
    const isLatest = (): boolean => generation === pickGeneration.current
    // A new pick closes the confirm step on screen at once, before its own
    // file is read: whatever becomes of the new file, the old one can no
    // longer be sent.
    setPicked(null)
    setMessage(null)
    if (file.size > MAX_IMPORT_FILE_BYTES) {
      setMessage({ role: 'alert', text: FILE_TOO_LARGE_MESSAGE })
      return
    }
    void readAsText(file).then(
      (text) => {
        if (!isLatest()) return
        const payload = parseProgressFile(text)
        if (payload === null) {
          setMessage({ role: 'alert', text: FILE_UNREADABLE_MESSAGE })
          return
        }
        setStrategy('merge')
        setPicked({ name: file.name, payload })
      },
      () => {
        if (!isLatest()) return
        setMessage({ role: 'alert', text: FILE_UNREADABLE_MESSAGE })
      },
    )
  }

  const onConfirm = (): void => {
    if (picked === null) return
    setMessage(null)
    importProgress.mutate(
      // Merge is the server's default, so only 덮어쓰기 names a strategy.
      { payload: picked.payload, ...(strategy === 'replace' ? { strategy } : {}) },
      {
        onSuccess: (result) => {
          setPicked(null)
          setMessage({ role: 'status', text: importResultMessage(result) })
        },
        onError: (error) => {
          setPicked(null)
          setMessage({ role: 'alert', text: importErrorMessage(error) })
        },
      },
    )
  }

  const onCancel = (): void => {
    pickGeneration.current += 1
    setPicked(null)
    setMessage(null)
  }

  return (
    <section className="flex flex-col gap-2">
      <h3 className="h6">읽기 기록</h3>
      <p className="text-xs text-ink-dim">
        읽은 쪽, 완독 표시, 책별 보기 설정을 파일로 저장하거나 그 파일에서 되살립니다.
      </p>

      <input
        ref={fileInput}
        type="file"
        accept=".json,application/json"
        hidden
        onChange={onPick}
      />

      {picked === null ? (
        <div className="flex flex-wrap gap-2">
          <Button variant="secondary" disabled={exportProgress.isPending} onClick={onExport}>
            내보내기
          </Button>
          <Button
            variant="secondary"
            onClick={() => {
              fileInput.current?.click()
            }}
          >
            가져오기
          </Button>
        </div>
      ) : (
        <div className="flex flex-col items-start gap-2">
          <p className="max-w-full truncate whitespace-nowrap text-base" title={picked.name}>
            {picked.name}
          </p>
          <p className="text-xs text-ink-muted">
            {`책 ${formatVolumeCount(picked.payload.items.length)}의 기록`}
          </p>
          <Seg
            aria-label="가져오기 방식"
            value={strategy}
            options={STRATEGY_OPTIONS}
            onChange={setStrategy}
          />
          <p className="text-xs text-ink-dim">{STRATEGY_NOTES[strategy]}</p>
          <div className="flex gap-2">
            <Button variant="primary" disabled={importProgress.isPending} onClick={onConfirm}>
              가져오기
            </Button>
            <Button variant="ghost" disabled={importProgress.isPending} onClick={onCancel}>
              취소
            </Button>
          </div>
        </div>
      )}

      {/* The slot's two live regions. Mounted with the section and only their
          text changes: a region inserted together with its message is often
          never announced. At most one holds text. An empty one has no height
          and `empty:-mt-2` hands back the gap above it, so the idle slot adds
          nothing under the row. */}
      <p role="status" className="text-xs text-ink-muted empty:-mt-2">
        {message?.role === 'status' ? message.text : null}
      </p>
      <p role="alert" className="text-xs text-accent-text empty:-mt-2">
        {message?.role === 'alert' ? message.text : null}
      </p>
    </section>
  )
}
