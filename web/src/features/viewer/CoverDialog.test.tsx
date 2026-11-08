import '@testing-library/jest-dom/vitest'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { bookDetail } from '../../api/fixtures'
import * as client from '../../api/client'
import { ApiError } from '../../api/errors'
import { CoverDialog } from './CoverDialog'

function show(covered = false, broken = false) {
  const close = vi.fn()
  const queryClient = new QueryClient({
    defaultOptions: { mutations: { retry: false } },
  })
  render(
    <QueryClientProvider client={queryClient}>
      <CoverDialog
        book={{
          ...bookDetail,
          ...(broken ? { status: 'error', page_count: 0, pages: [] } : {}),
          cover: covered ? { page: 2, x: 0.2, y: 0.1, w: 0.3, h: 0.675 } : null,
        }}
        pages={[2, 3]}
        onClose={close}
      />
    </QueryClientProvider>,
  )
  return close
}
function load(page = 2): void {
  const image = screen.getByAltText(`${String(page)}쪽 표지 원본`)
  Object.defineProperties(image, {
    naturalWidth: { value: 1200, configurable: true },
    naturalHeight: { value: 800, configurable: true },
  })
  fireEvent.load(image)
}
afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe('cover editor', () => {
  it('reopens the stored region, moves and resizes it, previews it, then saves the selected page', async () => {
    const put = vi.spyOn(client, 'putBookCover').mockResolvedValue({
      cover_cv: 'new',
      cover: { page: 2, x: 0, y: 0, w: 0.5, h: 0.5 },
    })
    const close = show(true)
    load()
    const frame = screen.getByRole('slider', { name: '표지 영역' })
    expect(frame).toHaveStyle({ left: '20%', width: '30%' })
    fireEvent.keyDown(frame, { key: 'ArrowRight' })
    fireEvent.keyDown(frame, { key: 'ArrowLeft', shiftKey: true })
    expect(screen.getByAltText('표지 미리보기')).toBeVisible()
    await userEvent.click(screen.getByRole('button', { name: '표지로 지정' }))
    await waitFor(() => {
      expect(close).toHaveBeenCalledOnce()
    })
    expect(put).toHaveBeenCalledOnce()
    const submitted = put.mock.calls[0]?.[1]
    expect(submitted?.page).toBe(2)
    expect(submitted?.x).toBeCloseTo(0.21)
    expect(submitted?.w).toBeCloseTo(0.29)
  })
  it('chooses either visible page and cancels without writing', async () => {
    const put = vi.spyOn(client, 'putBookCover')
    const close = show()
    load()
    await userEvent.click(screen.getByRole('button', { name: '3쪽' }))
    expect(screen.getByRole('button', { name: '표지로 지정' })).toBeDisabled()
    load(3)
    await userEvent.click(screen.getByRole('button', { name: '취소' }))
    expect(close).toHaveBeenCalledOnce()
    expect(put).not.toHaveBeenCalled()
    expect(
      screen.queryByRole('button', { name: '기본 표지로 되돌리기' }),
    ).not.toBeInTheDocument()
  })
  it('resets an existing choice even when its page image cannot load', async () => {
    const del = vi.spyOn(client, 'deleteBookCover').mockResolvedValue(undefined)
    const close = show(true)
    fireEvent.error(screen.getByAltText('2쪽 표지 원본'))
    await userEvent.click(
      screen.getByRole('button', { name: '기본 표지로 되돌리기' }),
    )
    await waitFor(() => {
      expect(close).toHaveBeenCalledOnce()
    })
    expect(del).toHaveBeenCalledWith(bookDetail.id)
  })
  it('keeps the selection and announces a refused save for retry', async () => {
    vi.spyOn(client, 'putBookCover').mockRejectedValue(
      new ApiError({
        status: 503,
        code: 'internal',
        message: 'private server text',
      }),
    )
    const close = show()
    load()
    await userEvent.click(screen.getByRole('button', { name: '표지로 지정' }))
    await waitFor(() => {
      expect(screen.getByRole('alert')).toHaveTextContent(
        '서버가 지금 표지를 저장할 수 없습니다',
      )
    })
    expect(close).not.toHaveBeenCalled()
    expect(screen.getByRole('slider', { name: '표지 영역' })).toBeVisible()
  })
})

it('can reset a stored cover on a broken book without requesting a missing page or allowing a new save', async () => {
  const del = vi.spyOn(client, 'deleteBookCover').mockResolvedValue(undefined)
  const close = show(true, true)
  expect(screen.queryByAltText('2쪽 표지 원본')).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: '표지로 지정' })).toBeDisabled()
  await userEvent.click(
    screen.getByRole('button', { name: '기본 표지로 되돌리기' }),
  )
  await waitFor(() => {
    expect(close).toHaveBeenCalledOnce()
  })
  expect(del).toHaveBeenCalledWith(bookDetail.id)
})
