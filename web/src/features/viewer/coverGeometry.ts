import type { BookCover } from '../../api/types'

export type Region = Omit<BookCover, 'page'>

export function centredRegion(width: number, height: number): Region {
  const ratio = (2 * height) / (3 * width)
  const w = Math.min(1, ratio)
  const h = w / ratio
  return { x: (1 - w) / 2, y: (1 - h) / 2, w, h }
}

export function moveRegion(r: Region, dx: number, dy: number): Region {
  const x = Math.max(0, Math.min(1 - r.w, r.x + dx))
  const y = Math.max(0, Math.min(1 - r.h, r.y + dy))
  return { x, y, w: Math.min(r.w, 1 - x), h: Math.min(r.h, 1 - y) }
}

export function resizeRegion(
  r: Region,
  dw: number,
  width: number,
  height: number,
): Region {
  const ratio = (2 * height) / (3 * width)
  const minimumW = Math.max(0.05, 0.05 * ratio)
  const minimumH = Math.max(0.05, 0.05 / ratio)
  const x = Math.max(0, Math.min(r.x, 1 - minimumW))
  const y = Math.max(0, Math.min(r.y, 1 - minimumH))
  const limit = Math.min(1 - x, (1 - y) * ratio)
  const widthInPage = Math.min(limit, Math.max(minimumW, r.w + dw))
  const heightInPage = Math.max(minimumH, widthInPage / ratio)
  return {
    x,
    y,
    w: Math.min(1 - x, Math.max(minimumW, heightInPage * ratio)),
    h: Math.min(1 - y, heightInPage),
  }
}
