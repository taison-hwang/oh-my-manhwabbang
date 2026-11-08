import { describe, expect, it } from 'vitest'
import { centredRegion, moveRegion, resizeRegion } from './coverGeometry'

describe('cover frame geometry', () => {
  for (const [width, height] of [
    [1200, 800],
    [800, 1200],
    [300, 1500],
  ] as const) {
    it(`fits the largest centred 2:3 frame in ${String(width)}×${String(height)}`, () => {
      const r = centredRegion(width, height)
      expect((r.w * width) / (r.h * height)).toBeCloseTo(2 / 3, 12)
      expect(Math.max(r.w, r.h)).toBe(1)
      expect(r.x * 2 + r.w).toBeCloseTo(1)
      expect(r.y * 2 + r.h).toBeCloseTo(1)
    })
  }
  it('clamps movement without changing the frame size', () => {
    const r = { x: 0.1, y: 0.1, w: 0.3, h: 0.6 }
    const moved = moveRegion(r, 10, -10)
    expect(moved).toEqual({ x: 0.7, y: 0, w: 0.3, h: 0.6 })
    expect(moved.x + moved.w).toBeLessThanOrEqual(1)
  })
  it('resizes in the source aspect, keeping the minimum and the far edges valid', () => {
    const r = centredRegion(1200, 800)
    for (const dw of [-10, -0.1, 10]) {
      const next = resizeRegion(r, dw, 1200, 800)
      expect((next.w * 1200) / (next.h * 800)).toBeCloseTo(2 / 3, 12)
      expect(next.w).toBeGreaterThanOrEqual(0.05)
      expect(next.h).toBeGreaterThanOrEqual(0.05)
      expect(next.x + next.w).toBeLessThanOrEqual(1)
      expect(next.y + next.h).toBeLessThanOrEqual(1)
    }
  })
})

it('keeps both sides at or above the exact server minimum after repeated shrinking', () => {
  let r = centredRegion(500, 943)
  for (let i = 0; i < 200; i++) r = resizeRegion(r, -0.01, 500, 943)
  expect(r.w).toBeGreaterThanOrEqual(0.05)
  expect(r.h).toBeGreaterThanOrEqual(0.05)
  expect(r.x + r.w).toBeLessThanOrEqual(1)
  expect(r.y + r.h).toBeLessThanOrEqual(1)
  expect((r.w * 500) / (r.h * 943)).toBeCloseTo(2 / 3, 12)
})
