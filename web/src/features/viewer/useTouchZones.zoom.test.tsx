import { act, renderHook } from "@testing-library/react";
import type { TouchEvent } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  DOUBLE_TAP_MS,
  DOUBLE_TAP_ZOOM,
  NO_ZOOM,
  ZOOM_MAX,
  clampPan,
  useTouchZones,
  zoomAbout,
} from "./useTouchZones";

interface P {
  x: number;
  y: number;
}

const BOX = { left: 0, top: 0, width: 400, height: 800 };

function ev(touches: P[], changed: P[] = touches): TouchEvent<HTMLElement> {
  const t = (p: P) => ({ clientX: p.x, clientY: p.y });
  return {
    touches: touches.map(t),
    changedTouches: changed.map(t),
    currentTarget: { getBoundingClientRect: () => BOX },
  } as unknown as TouchEvent<HTMLElement>;
}

function setup(mode: "single" | "vertical" = "single") {
  const calls = { next: 0, prev: 0, chrome: 0 };
  const hook = renderHook(() =>
    useTouchZones({
      dir: "ltr",
      mode,
      onNext: () => void (calls.next += 1),
      onPrev: () => void (calls.prev += 1),
      onToggleChrome: () => void (calls.chrome += 1),
      resetKey: "a",
    }),
  );
  return { calls, hook };
}

const tap = (hook: ReturnType<typeof setup>["hook"], p: P) => {
  act(() => {
    hook.result.current.onTouchStart(ev([p]));
  });
  act(() => {
    hook.result.current.onTouchEnd(ev([], [p]));
  });
};

function pinchOut(hook: ReturnType<typeof setup>["hook"]) {
  const at = (d: number): P[] => [
    { x: 200 - d / 2, y: 400 },
    { x: 200 + d / 2, y: 400 },
  ];
  act(() => {
    hook.result.current.onTouchStart(ev(at(40)));
  });
  act(() => {
    hook.result.current.onTouchMove(ev(at(120)));
  });
  act(() => {
    hook.result.current.onTouchEnd(ev([]));
  });
}

describe("zoom maths", () => {
  it("keeps the layer covering its box", () => {
    expect(clampPan(50, 400, 2)).toBe(0);
    expect(clampPan(-900, 400, 2)).toBe(-400);
    expect(clampPan(-100, 400, 2)).toBe(-100);
  });
  it("zooms about the anchor and never past the cap", () => {
    const z = zoomAbout(
      NO_ZOOM,
      99,
      { x: 200, y: 400 },
      { width: 400, height: 800 },
    );
    expect(z.scale).toBe(ZOOM_MAX);
    expect(
      zoomAbout(NO_ZOOM, 0.5, { x: 1, y: 1 }, { width: 400, height: 800 }),
    ).toEqual(NO_ZOOM);
    const two = zoomAbout(
      NO_ZOOM,
      2,
      { x: 200, y: 400 },
      { width: 400, height: 800 },
    );
    expect(two).toEqual({ scale: 2, x: -200, y: -400 });
  });
});

describe("touch zoom (E-70)", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it("a pinch zooms, and releasing it turns no page", () => {
    const { calls, hook } = setup();
    pinchOut(hook);
    expect(hook.result.current.zoom.scale).toBeCloseTo(3, 1);
    expect(calls).toEqual({ next: 0, prev: 0, chrome: 0 });
  });

  it("while zoomed a horizontal swipe pans and the side zones turn nothing", () => {
    const { calls, hook } = setup();
    pinchOut(hook);
    const before = hook.result.current.zoom;
    act(() => {
      hook.result.current.onTouchStart(ev([{ x: 100, y: 400 }]));
    });
    act(() => {
      hook.result.current.onTouchMove(ev([{ x: 180, y: 400 }]));
    });
    act(() => {
      hook.result.current.onTouchEnd(ev([], [{ x: 180, y: 400 }]));
    });
    expect(hook.result.current.zoom.x).toBeGreaterThan(before.x);
    expect(hook.result.current.zoom.scale).toBe(before.scale);
    vi.advanceTimersByTime(DOUBLE_TAP_MS * 3);
    tap(hook, { x: 380, y: 400 });
    expect(calls).toEqual({ next: 0, prev: 0, chrome: 0 });
  });

  it("a double tap in the centre zooms in, another zooms out", () => {
    const { calls, hook } = setup();
    tap(hook, { x: 200, y: 400 });
    tap(hook, { x: 200, y: 400 });
    expect(hook.result.current.zoom.scale).toBe(DOUBLE_TAP_ZOOM);
    vi.advanceTimersByTime(DOUBLE_TAP_MS * 3);
    expect(calls.chrome).toBe(0);
    tap(hook, { x: 200, y: 400 });
    tap(hook, { x: 200, y: 400 });
    expect(hook.result.current.zoom).toEqual(NO_ZOOM);
  });

  it("a single centre tap still toggles the chrome, after the double-tap window", () => {
    const { calls, hook } = setup();
    tap(hook, { x: 200, y: 400 });
    expect(calls.chrome).toBe(0);
    vi.advanceTimersByTime(DOUBLE_TAP_MS + 1);
    expect(calls.chrome).toBe(1);
  });

  it("at scale 1 side taps still turn pages at once, twice for a double tap (E-28)", () => {
    const { calls, hook } = setup();
    tap(hook, { x: 380, y: 400 });
    tap(hook, { x: 380, y: 400 });
    expect(calls.next).toBe(2);
    expect(hook.result.current.zoom).toEqual(NO_ZOOM);
  });

  it("does not zoom in 세로 mode", () => {
    const { hook } = setup("vertical");
    pinchOut(hook);
    tap(hook, { x: 200, y: 400 });
    tap(hook, { x: 200, y: 400 });
    expect(hook.result.current.zoom).toEqual(NO_ZOOM);
  });
});
