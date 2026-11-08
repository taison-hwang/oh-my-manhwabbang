/**
 * Tap zones and swipe (FR-VWR-011, ui-spec §8.3).
 *
 * Left 32 % / right 32 % turn the page **in reading order**, centre 36 %
 * toggles the chrome, and a horizontal swipe turns the page in the direction it
 * was thrown. Under `R→L` the side zones swap, because "left" is a screen
 * position and "previous" is a book position.
 *
 * Mouse *and* touch, not Pointer Events: the spec names `mousedown`/`touchstart`
 * and jsdom's PointerEvent support is not something a page turn should depend
 * on. A touch sequence suppresses the synthesised mouse events that follow it.
 *
 * ## Zoom (E-70)
 *
 * Two fingers pinch the page, one finger pans it while it is zoomed, and a
 * double tap zooms in (centre zone) or back out. While the scale is above 1 a
 * horizontal drag is a pan and never a page turn, and the side zones do not
 * turn pages; at scale 1 everything above is exactly E-28. Touch only: the
 * mouse path does not zoom. 세로 mode is excluded, its scroll is native.
 */

import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type MouseEvent,
  type TouchEvent,
} from "react";

import type { DisplayMode, ReadingDirection } from "../../store/viewer";

/**
 * Side zones are 32 % each, so the centre is 36 %.
 *
 * The prototype's own measurement at 1440px: 461 / 518 / 461. The centre is
 * deliberately the *smallest* of the three — it toggles the chrome, and the two
 * that turn pages are the ones a reader aims at a hundred times a volume.
 */
export const SIDE_ZONE_RATIO = 0.32;
/** Below this a horizontal drag is a tap, not a swipe. */
export const SWIPE_THRESHOLD_PX = 44;
/**
 * A swipe must be more horizontal than vertical.
 *
 * `|dy| > |dx|` — anything steeper is someone scrolling a webtoon or a long
 * 너비-fitted page, and turning the page out from under them is the worst
 * possible answer.
 */
export const SWIPE_MAX_VERTICAL_RATIO = 1;
/**
 * A swipe has to be *thrown*. Past this the finger was resting on the page and
 * happened to drift, which is a drag, not a page turn.
 */
export const SWIPE_MAX_MS = 600;
/** Largest pinch scale; 1 is the fit the reader chose. */
export const ZOOM_MAX = 4;
/** Where a double tap in the centre zone lands. */
export const DOUBLE_TAP_ZOOM = 2.5;
/** Two taps this close in time (and space) are a double tap. */
export const DOUBLE_TAP_MS = 300;
export const DOUBLE_TAP_SLOP_PX = 30;
/** A pinch released this close to 1 snaps to 1. */
const ZOOM_SNAP = 1.05;
/** Synthesised mouse events arrive within ~300 ms of a touch. */
const TOUCH_MOUSE_SUPPRESS_MS = 600;

export type StageZone = "left" | "centre" | "right";
export type ZoneAction = "next" | "prev" | "chrome";

/** Which third of the stage a point falls in. */
export function zoneAt(offsetX: number, width: number): StageZone {
  if (width <= 0) return "centre";
  const ratio = offsetX / width;
  if (ratio < SIDE_ZONE_RATIO) return "left";
  if (ratio > 1 - SIDE_ZONE_RATIO) return "right";
  return "centre";
}

/**
 * Reading order, not screen order: under `R→L` the *next* page is to the left,
 * so tapping the left zone advances the book.
 */
export function zoneAction(zone: StageZone, dir: ReadingDirection): ZoneAction {
  if (zone === "centre") return "chrome";
  if (zone === "left") return dir === "rtl" ? "next" : "prev";
  return dir === "rtl" ? "prev" : "next";
}

/**
 * A horizontal throw, resolved through the same rule as the tap zones: a swipe
 * to the left means "go to what is on the right", whichever page that is.
 *
 * `elapsedMs` is how long the finger was down. Omit it for callers that have no
 * clock (the pure zone tests); a swipe with no measured duration is judged on
 * distance and angle alone.
 */
export function swipeAction(
  dx: number,
  dy: number,
  dir: ReadingDirection,
  elapsedMs?: number,
): "next" | "prev" | null {
  if (elapsedMs !== undefined && elapsedMs > SWIPE_MAX_MS) return null;
  if (Math.abs(dx) < SWIPE_THRESHOLD_PX) return null;
  if (Math.abs(dy) > Math.abs(dx) * SWIPE_MAX_VERTICAL_RATIO) return null;
  const action = zoneAction(dx < 0 ? "right" : "left", dir);
  return action === "chrome" ? null : action;
}

export interface ZoomState {
  scale: number;
  x: number;
  y: number;
}

export const NO_ZOOM: ZoomState = { scale: 1, x: 0, y: 0 };

/** Keep a zoomed layer covering its box: `t` is the layer's offset, `size` the box. */
export function clampPan(t: number, size: number, scale: number): number {
  return Math.min(0, Math.max(size * (1 - scale), t));
}

/** The zoom that keeps the layer point under `anchor` (box coordinates) fixed. */
export function zoomAbout(
  from: ZoomState,
  nextScale: number,
  anchor: { x: number; y: number },
  box: { width: number; height: number },
): ZoomState {
  const scale = Math.min(ZOOM_MAX, Math.max(1, nextScale));
  if (scale <= 1) return NO_ZOOM;
  const px = (anchor.x - from.x) / from.scale;
  const py = (anchor.y - from.y) / from.scale;
  return {
    scale,
    x: clampPan(anchor.x - px * scale, box.width, scale),
    y: clampPan(anchor.y - py * scale, box.height, scale),
  };
}

export interface TouchZonesOptions {
  dir: ReadingDirection;
  mode: DisplayMode;
  onNext: () => void;
  onPrev: () => void;
  onToggleChrome: () => void;
  enabled?: boolean;
  /** Zoom resets to 1 whenever this changes (page turn, mode change, another book). */
  resetKey?: string | number;
}

export interface TouchZoneHandlers {
  onMouseDown: (event: MouseEvent<HTMLElement>) => void;
  onMouseUp: (event: MouseEvent<HTMLElement>) => void;
  onTouchStart: (event: TouchEvent<HTMLElement>) => void;
  onTouchMove: (event: TouchEvent<HTMLElement>) => void;
  onTouchEnd: (event: TouchEvent<HTMLElement>) => void;
  onTouchCancel: (event: TouchEvent<HTMLElement>) => void;
  zoom: ZoomState;
}

interface Point {
  x: number;
  y: number;
  /** `Date.now()` at pointer-down, so a throw can be told from a rest. */
  t: number;
}

interface PinchStart {
  distance: number;
  from: ZoomState;
}

function distanceOf(
  a: { clientX: number; clientY: number },
  b: { clientX: number; clientY: number },
) {
  return Math.hypot(a.clientX - b.clientX, a.clientY - b.clientY);
}

export function useTouchZones(options: TouchZonesOptions): TouchZoneHandlers {
  const {
    dir,
    mode,
    onNext,
    onPrev,
    onToggleChrome,
    enabled = true,
    resetKey,
  } = options;

  const start = useRef<Point | null>(null);
  const lastTouchEndAt = useRef<number>(0);

  const [zoom, setZoom] = useState<ZoomState>(NO_ZOOM);
  const zoomRef = useRef<ZoomState>(NO_ZOOM);
  const applyZoom = useCallback((next: ZoomState): void => {
    zoomRef.current = next;
    setZoom(next);
  }, []);
  const pinch = useRef<PinchStart | null>(null);
  /** Set by a second finger; cleared when every finger is up. Nothing resolves meanwhile. */
  const multi = useRef(false);
  const panFrom = useRef<{ touch: Point; zoom: ZoomState } | null>(null);
  const dragged = useRef(false);
  const lastTap = useRef<{ x: number; y: number; t: number } | null>(null);
  const pendingChrome = useRef<ReturnType<typeof setTimeout> | null>(null);

  const cancelPendingChrome = useCallback((): void => {
    if (pendingChrome.current !== null) {
      clearTimeout(pendingChrome.current);
      pendingChrome.current = null;
    }
  }, []);

  useEffect(() => cancelPendingChrome, [cancelPendingChrome]);

  useEffect(() => {
    cancelPendingChrome();
    lastTap.current = null;
    pinch.current = null;
    panFrom.current = null;
    applyZoom(NO_ZOOM);
  }, [resetKey, mode, applyZoom, cancelPendingChrome]);

  const run = useCallback(
    (action: ZoneAction): void => {
      if (action === "next") onNext();
      else if (action === "prev") onPrev();
      else onToggleChrome();
    },
    [onNext, onPrev, onToggleChrome],
  );

  const resolve = useCallback(
    (element: HTMLElement, from: Point, to: Point): void => {
      const dx = to.x - from.x;
      const dy = to.y - from.y;
      // 세로 mode owns horizontal space for nothing and vertical space for the
      // native scroll, so swipes are off there (ui-spec §8.3).
      if (mode !== "vertical") {
        const swipe = swipeAction(dx, dy, dir, to.t - from.t);
        if (swipe !== null) {
          run(swipe);
          return;
        }
      }
      if (
        Math.abs(dx) >= SWIPE_THRESHOLD_PX ||
        Math.abs(dy) >= SWIPE_THRESHOLD_PX
      )
        return;
      const rect = element.getBoundingClientRect();
      run(zoneAction(zoneAt(to.x - rect.left, rect.width), dir));
    },
    [dir, mode, run],
  );

  const onMouseDown = useCallback(
    (event: MouseEvent<HTMLElement>) => {
      if (!enabled) return;
      if (Date.now() - lastTouchEndAt.current < TOUCH_MOUSE_SUPPRESS_MS) return;
      start.current = { x: event.clientX, y: event.clientY, t: Date.now() };
    },
    [enabled],
  );

  const onMouseUp = useCallback(
    (event: MouseEvent<HTMLElement>) => {
      if (!enabled) return;
      const from = start.current;
      start.current = null;
      if (from === null) return;
      resolve(event.currentTarget, from, {
        x: event.clientX,
        y: event.clientY,
        t: Date.now(),
      });
    },
    [enabled, resolve],
  );

  const zoomable = enabled && mode !== "vertical";

  const onTouchStart = useCallback(
    (event: TouchEvent<HTMLElement>) => {
      if (!enabled) return;
      const touch = event.touches[0];
      if (touch === undefined) return;
      const second = event.touches[1];
      if (second !== undefined) {
        if (!zoomable) return;
        multi.current = true;
        start.current = null;
        panFrom.current = null;
        cancelPendingChrome();
        lastTap.current = null;
        pinch.current = {
          distance: distanceOf(touch, second),
          from: zoomRef.current,
        };
        return;
      }
      dragged.current = false;
      start.current = { x: touch.clientX, y: touch.clientY, t: Date.now() };
      panFrom.current =
        zoomRef.current.scale > 1
          ? { touch: start.current, zoom: zoomRef.current }
          : null;
    },
    [enabled, zoomable, cancelPendingChrome],
  );

  const onTouchMove = useCallback(
    (event: TouchEvent<HTMLElement>) => {
      if (!zoomable) return;
      const rect = event.currentTarget.getBoundingClientRect();
      const box = { width: rect.width, height: rect.height };
      const a = event.touches[0];
      const b = event.touches[1];
      if (a !== undefined && b !== undefined) {
        const p = pinch.current;
        if (p === null || p.distance <= 0) return;
        const anchor = {
          x: (a.clientX + b.clientX) / 2 - rect.left,
          y: (a.clientY + b.clientY) / 2 - rect.top,
        };
        applyZoom(
          zoomAbout(
            p.from,
            p.from.scale * (distanceOf(a, b) / p.distance),
            anchor,
            box,
          ),
        );
        return;
      }
      const pan = panFrom.current;
      if (pan !== null && a !== undefined && !multi.current) {
        const dx = a.clientX - pan.touch.x;
        const dy = a.clientY - pan.touch.y;
        if (
          Math.abs(dx) > DOUBLE_TAP_SLOP_PX / 3 ||
          Math.abs(dy) > DOUBLE_TAP_SLOP_PX / 3
        )
          dragged.current = true;
        applyZoom({
          scale: pan.zoom.scale,
          x: clampPan(pan.zoom.x + dx, box.width, pan.zoom.scale),
          y: clampPan(pan.zoom.y + dy, box.height, pan.zoom.scale),
        });
      }
    },
    [zoomable, applyZoom],
  );

  const onTouchEnd = useCallback(
    (event: TouchEvent<HTMLElement>) => {
      if (!enabled) return;
      lastTouchEndAt.current = Date.now();
      if (multi.current) {
        // A pinch in progress or just over: it is not a tap and not a swipe.
        if (event.touches.length === 0) {
          multi.current = false;
          pinch.current = null;
          start.current = null;
          panFrom.current = null;
          if (zoomRef.current.scale < ZOOM_SNAP) applyZoom(NO_ZOOM);
        } else if (event.touches.length === 1) {
          pinch.current = null;
        }
        return;
      }
      const from = start.current;
      start.current = null;
      panFrom.current = null;
      const touch = event.changedTouches[0];
      if (from === null || touch === undefined) return;
      const to = { x: touch.clientX, y: touch.clientY, t: Date.now() };
      const zoomed = zoomRef.current.scale > 1;

      if (zoomed && dragged.current) {
        dragged.current = false;
        return;
      }

      const moved = Math.hypot(to.x - from.x, to.y - from.y);
      const isTap = moved < DOUBLE_TAP_SLOP_PX && to.t - from.t < SWIPE_MAX_MS;
      const rect = event.currentTarget.getBoundingClientRect();

      if (zoomable && isTap) {
        const prev = lastTap.current;
        if (
          prev !== null &&
          to.t - prev.t < DOUBLE_TAP_MS &&
          Math.hypot(to.x - prev.x, to.y - prev.y) < DOUBLE_TAP_SLOP_PX * 2
        ) {
          cancelPendingChrome();
          lastTap.current = null;
          if (zoomed) {
            applyZoom(NO_ZOOM);
            return;
          }
          if (zoneAt(to.x - rect.left, rect.width) === "centre") {
            applyZoom(
              zoomAbout(
                NO_ZOOM,
                DOUBLE_TAP_ZOOM,
                { x: to.x - rect.left, y: to.y - rect.top },
                { width: rect.width, height: rect.height },
              ),
            );
            return;
          }
          // A double tap in a side zone is two page turns, as ever (E-28): the
          // first was already delivered, this is the second.
        } else {
          lastTap.current = { x: to.x, y: to.y, t: to.t };
          if (zoneAt(to.x - rect.left, rect.width) === "centre") {
            // Held back one double-tap window so the first tap of a double tap
            // does not flash the chrome.
            cancelPendingChrome();
            pendingChrome.current = setTimeout(() => {
              pendingChrome.current = null;
              onToggleChrome();
            }, DOUBLE_TAP_MS);
            return;
          }
        }
      }

      if (zoomed) return; // zoomed: side zones and swipes never turn pages
      resolve(event.currentTarget, from, to);
    },
    [
      enabled,
      zoomable,
      applyZoom,
      cancelPendingChrome,
      onToggleChrome,
      resolve,
    ],
  );

  const onTouchCancel = useCallback(() => {
    multi.current = false;
    pinch.current = null;
    start.current = null;
    panFrom.current = null;
    dragged.current = false;
    if (zoomRef.current.scale < ZOOM_SNAP) applyZoom(NO_ZOOM);
  }, [applyZoom]);

  return {
    onMouseDown,
    onMouseUp,
    onTouchStart,
    onTouchMove,
    onTouchEnd,
    onTouchCancel,
    zoom,
  };
}
