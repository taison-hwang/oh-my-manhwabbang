import { useRef, useState, type KeyboardEvent, type PointerEvent } from 'react'

import { useResetBookCover, useSetBookCover } from '../../api/queries'
import { isApiError } from '../../api/errors'
import type { BookDetail } from '../../api/types'
import { pageUrl } from '../../api/urls'
import { Button } from '../../components/ds/Button'
import { Dialog } from '../../components/ds/Dialog'
import {
  centredRegion,
  moveRegion,
  resizeRegion,
  type Region,
} from './coverGeometry'

interface Props {
  book: BookDetail
  pages: readonly number[]
  onClose: () => void
}
interface Size {
  w: number
  h: number
}
interface Drag {
  x: number
  y: number
  region: Region
  resize: boolean
  width: number
  height: number
}

function errorMessage(error: unknown): string {
  if (!isApiError(error))
    return '서버에 연결하지 못했습니다. 잠시 후 다시 시도하세요.'
  if (error.status === 404)
    return '라이브러리에 없는 책입니다. 화면을 새로 고친 뒤 다시 시도하세요.'
  if (error.status === 400)
    return '표지 영역을 저장하지 못했습니다. 쪽과 선택 영역을 확인하세요.'
  if (error.status === 503)
    return '서버가 지금 표지를 저장할 수 없습니다. 잠시 후 다시 시도하세요.'
  return '표지를 저장하지 못했습니다. 잠시 후 다시 시도하세요.'
}

export function CoverDialog({ book, pages, onClose }: Props) {
  const [page, setPage] = useState(pages[0] ?? 1)
  const [size, setSize] = useState<Size | null>(null)
  const [region, setRegion] = useState<Region | null>(null)
  const [imageFailed, setImageFailed] = useState(false)
  const imageBox = useRef<HTMLDivElement>(null)
  const drag = useRef<Drag | null>(null)
  const save = useSetBookCover(book.id)
  const reset = useResetBookCover(book.id)
  const busy = save.isPending || reset.isPending
  const error = save.error ?? reset.error
  const editable = book.status === 'ok' && page >= 1 && page <= book.page_count
  const previewable = editable && book.pages.some((p) => p.n === page)
  const src = pageUrl(book.id, page, { v: book.cv })

  const choosePage = (n: number): void => {
    setPage(n)
    setSize(null)
    setRegion(null)
    setImageFailed(false)
    save.reset()
    reset.reset()
  }
  const startDrag = (e: PointerEvent<HTMLDivElement>): void => {
    if (region === null || busy || !editable) return
    e.preventDefault()
    e.stopPropagation()
    e.currentTarget.focus()
    e.currentTarget.setPointerCapture(e.pointerId)
    const box = imageBox.current?.getBoundingClientRect()
    if (box === undefined) return
    drag.current = {
      x: e.clientX,
      y: e.clientY,
      region,
      resize: (e.target as HTMLElement).dataset.resize === 'true',
      width: box.width,
      height: box.height,
    }
  }
  const moveDrag = (e: PointerEvent<HTMLDivElement>): void => {
    const d = drag.current
    if (d === null || size === null) return
    const dx = (e.clientX - d.x) / d.width
    const dy = (e.clientY - d.y) / d.height
    setRegion(
      d.resize
        ? resizeRegion(
            d.region,
            Math.abs(dx) > Math.abs(dy)
              ? dx
              : (dy * (2 * size.h)) / (3 * size.w),
            size.w,
            size.h,
          )
        : moveRegion(d.region, dx, dy),
    )
  }
  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>): void => {
    if (
      !['ArrowLeft', 'ArrowRight', 'ArrowUp', 'ArrowDown'].includes(e.key) ||
      region === null ||
      size === null ||
      busy ||
      !editable
    )
      return
    e.preventDefault()
    e.stopPropagation()
    const positive = e.key === 'ArrowRight' || e.key === 'ArrowDown'
    if (e.shiftKey)
      setRegion(resizeRegion(region, positive ? 0.01 : -0.01, size.w, size.h))
    else
      setRegion(
        moveRegion(
          region,
          e.key === 'ArrowLeft' ? -0.01 : e.key === 'ArrowRight' ? 0.01 : 0,
          e.key === 'ArrowUp' ? -0.01 : e.key === 'ArrowDown' ? 0.01 : 0,
        ),
      )
  }

  return (
    <Dialog open title="표지 편집" width="min(760px, 100%)" onClose={onClose}>
      {pages.length > 1 && (
        <div className="flex gap-2" role="group" aria-label="표지 쪽 선택">
          {pages.map((n) => (
            <Button
              key={n}
              variant="secondary"
              aria-pressed={page === n}
              disabled={busy}
              onClick={() => {
                choosePage(n)
              }}
            >
              {n}쪽
            </Button>
          ))}
        </div>
      )}
      <p id="cover-help" className="text-sm text-ink-dim">
        영역을 드래그해 옮기고 오른쪽 아래 모서리로 크기를 바꾸세요. 화살표는
        이동, Shift+화살표는 크기 조절입니다.
      </p>
      <div className="flex flex-wrap items-start justify-center gap-4">
        <div
          ref={imageBox}
          className="relative max-w-full overflow-hidden touch-none"
          style={
            size === null
              ? { width: 420 }
              : { width: `min(420px, ${String((48 * size.w) / size.h)}vh)` }
          }
        >
          {previewable && (
            <img
              key={src}
              src={src}
              alt={`${String(page)}쪽 표지 원본`}
              className="block w-full"
              onError={() => {
                setImageFailed(true)
              }}
              onLoad={(e) => {
                const image = e.currentTarget
                const s = { w: image.naturalWidth, h: image.naturalHeight }
                setSize(s)
                setRegion(
                  book.cover?.page === page
                    ? book.cover
                    : centredRegion(s.w, s.h),
                )
              }}
            />
          )}
          {previewable && region !== null && (
            <div
              role="slider"
              aria-label="표지 영역"
              aria-describedby="cover-help"
              aria-valuemin={0}
              aria-valuemax={100}
              aria-valuenow={Math.round(region.w * 100)}
              aria-valuetext={`왼쪽 ${String(Math.round(region.x * 100))}%, 위 ${String(Math.round(region.y * 100))}%, 너비 ${String(Math.round(region.w * 100))}%, 높이 ${String(Math.round(region.h * 100))}%`}
              tabIndex={0}
              className="absolute cursor-move border-2 border-accent bg-transparent outline outline-1 outline-ink"
              style={{
                left: `${String(region.x * 100)}%`,
                top: `${String(region.y * 100)}%`,
                width: `${String(region.w * 100)}%`,
                height: `${String(region.h * 100)}%`,
                boxShadow: '0 0 0 999px var(--scrim-modal)',
              }}
              onKeyDown={onKeyDown}
              onPointerDown={startDrag}
              onPointerMove={moveDrag}
              onPointerUp={() => {
                drag.current = null
              }}
              onPointerCancel={() => {
                drag.current = null
              }}
              onLostPointerCapture={() => {
                drag.current = null
              }}
            >
              <span
                data-resize="true"
                aria-hidden="true"
                className="absolute bottom-0 right-0 h-5 w-5 cursor-se-resize border-2 border-ink bg-accent"
              />
            </div>
          )}
        </div>
        {previewable && region !== null && (
          <figure className="m-0">
            <figcaption className="mb-2 text-sm">미리보기</figcaption>
            <div
              className="relative w-28 overflow-hidden"
              style={{ aspectRatio: '2 / 3' }}
            >
              <img
                src={src}
                alt="표지 미리보기"
                className="absolute max-w-none"
                style={{
                  width: `${String(100 / region.w)}%`,
                  height: `${String(100 / region.h)}%`,
                  left: `${String((-100 * region.x) / region.w)}%`,
                  top: `${String((-100 * region.y) / region.h)}%`,
                }}
              />
            </div>
          </figure>
        )}
      </div>
      {!editable && (
        <p role="status">
          이 책에서는 표지를 새로 지정할 수 없습니다. 저장한 표지는 기본 표지로
          되돌릴 수 있습니다.
        </p>
      )}
      {imageFailed && (
        <p role="alert">
          쪽 이미지를 불러오지 못했습니다. 대화상자를 다시 열어 시도하세요.
        </p>
      )}
      {error !== null && <p role="alert">{errorMessage(error)}</p>}
      <div className="flex flex-wrap justify-end gap-2">
        {book.cover !== null && (
          <Button
            variant="secondary"
            disabled={busy}
            onClick={() => {
              reset.mutate(undefined, { onSuccess: onClose })
            }}
          >
            기본 표지로 되돌리기
          </Button>
        )}
        <Button variant="secondary" onClick={onClose}>
          취소
        </Button>
        <Button
          disabled={
            !editable ||
            region === null ||
            busy ||
            imageFailed ||
            region.w < 0.05 ||
            region.h < 0.05
          }
          onClick={() => {
            if (region !== null)
              save.mutate(
                {
                  page,
                  x: region.x,
                  y: region.y,
                  w: Math.min(region.w, 1 - region.x),
                  h: Math.min(region.h, 1 - region.y),
                },
                { onSuccess: onClose },
              )
          }}
        >
          표지로 지정
        </Button>
      </div>
    </Dialog>
  )
}
