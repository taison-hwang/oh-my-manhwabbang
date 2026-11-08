/**
 * E-70 — the viewer's touch gestures: pinch zoom, one-finger pan while zoomed,
 * double-tap zoom, and the rule that a zoomed page is panned, never turned.
 *
 * Touch is only on in the `mobile-400` project (playwright.config.ts `hasTouch`),
 * and Playwright has no pinch API, so the fingers are sent through the DevTools
 * protocol (`Input.dispatchTouchEvent`). That is an emulation: a real tablet is
 * a user check, not something this file proves (decisions.md E-70 §4).
 */

import type { CDPSession, Page, Request } from "@playwright/test";

import {
  booksOf,
  clearProgress,
  currentPage,
  expect,
  openViewerDirect,
  resetBookPrefs,
  SERIES,
  seriesId,
  test,
  viewer,
  waitForPage,
  wakeChrome,
} from "./shelf";

interface Pt {
  x: number;
  y: number;
}

async function fingers(
  cdp: CDPSession,
  type: "touchStart" | "touchMove" | "touchEnd",
  pts: Pt[],
) {
  await cdp.send("Input.dispatchTouchEvent", {
    type,
    touchPoints: pts.map((p, id) => ({ x: p.x, y: p.y, id })),
  });
}

async function pinch(
  cdp: CDPSession,
  centre: Pt,
  from: number,
  to: number,
): Promise<void> {
  const at = (d: number): Pt[] => [
    { x: centre.x - d / 2, y: centre.y },
    { x: centre.x + d / 2, y: centre.y },
  ];
  await fingers(cdp, "touchStart", at(from));
  const steps = 8;
  for (let i = 1; i <= steps; i++)
    await fingers(cdp, "touchMove", at(from + ((to - from) * i) / steps));
  await fingers(cdp, "touchEnd", []);
}

async function drag(cdp: CDPSession, a: Pt, b: Pt): Promise<void> {
  await fingers(cdp, "touchStart", [a]);
  const steps = 6;
  for (let i = 1; i <= steps; i++)
    await fingers(cdp, "touchMove", [
      {
        x: a.x + ((b.x - a.x) * i) / steps,
        y: a.y + ((b.y - a.y) * i) / steps,
      },
    ]);
  await fingers(cdp, "touchEnd", []);
}

const progressWrites = new WeakMap<Page, { sent: number; done: number }>();

/** Counts every `PUT …/progress` the page sends and every one that has ended. */
function watchProgressWrites(page: Page): void {
  const tally = { sent: 0, done: 0 };
  progressWrites.set(page, tally);
  const isWrite = (r: Request): boolean =>
    r.method() === "PUT" && /\/api\/books\/[^/]+\/progress/.test(r.url());
  page.on("request", (r) => {
    if (isWrite(r)) tally.sent++;
  });
  page.on("requestfinished", (r) => {
    if (isWrite(r)) tally.done++;
  });
  page.on("requestfailed", (r) => {
    if (isWrite(r)) tally.done++;
  });
}

/**
 * shelf.ts rule 2 for a viewer that has been open: `useSaveProgress` writes
 * after a 1 s debounce and flushes on unmount, so a DELETE issued while the
 * viewer is still mounted can be undone by the write that follows it, and the
 * row then shows in the 이어보기 shelf of the library shots the next viewport
 * project takes. The viewer is left through its own 라이브러리 button (its
 * unmount flushes the pending write), and the DELETE waits until every
 * progress write the page has sent has ended — no sleep.
 */
async function leaveBook(page: Page, bookId: string): Promise<void> {
  await wakeChrome(page);
  await page.locator('[data-role="viewer-library"]').click();
  await expect(page.locator('[data-testid="library-scroller"]')).toBeVisible();
  const tally = progressWrites.get(page);
  expect(tally, "watchProgressWrites must run when the book is opened").toBeDefined();
  await expect.poll(() => (tally?.sent ?? 0) - (tally?.done ?? 0)).toBe(0);
  await clearProgress(page, bookId);
  const after = (await (await page.request.get(`/api/books/${bookId}`)).json()) as {
    progress: unknown;
  };
  expect(after.progress, "no progress row left behind").toBeNull();
}

const layer = (page: Page) => page.locator('[data-role="zoom-layer"]');
const scaleOf = async (page: Page): Promise<number> =>
  Number((await layer(page).getAttribute("data-zoom")) ?? "1");
const transformOf = async (page: Page): Promise<string> =>
  (await layer(page).evaluate((el) => (el as HTMLElement).style.transform)) ||
  "none";

async function open(
  page: Page,
  atPage: number,
): Promise<{ bid: string; cdp: CDPSession; mid: Pt }> {
  const sid = await seriesId(page, SERIES.gungye);
  const first = (await booksOf(page, sid)).find((b) => b.status === "ok");
  expect(first, "군계 must have an openable volume").toBeDefined();
  const bid = first?.id ?? "";
  await resetBookPrefs(page, bid);
  watchProgressWrites(page);
  await openViewerDirect(page, sid, bid, atPage);
  await waitForPage(page);
  const box = await page.locator('[data-role="stage-zones"]').boundingBox();
  expect(box).not.toBeNull();
  const mid = {
    x: (box?.x ?? 0) + (box?.width ?? 0) / 2,
    y: (box?.y ?? 0) + (box?.height ?? 0) / 2,
  };
  return { bid, cdp: await page.context().newCDPSession(page), mid };
}

test.describe("E-70 · touch gestures", () => {
  test.beforeEach(({ page }, info) => {
    test.skip(
      (page.viewportSize()?.width ?? 0) >= 768,
      `touch gestures run in the touch project, not ${info.project.name}`,
    );
  });

  test("pinch zooms; a drag while zoomed pans and never turns the page", async ({
    page,
  }) => {
    const { bid, cdp, mid } = await open(page, 3);
    try {
      expect(await scaleOf(page)).toBe(1);
      await pinch(cdp, mid, 60, 180);
      expect(await scaleOf(page)).toBeGreaterThan(1.5);

      const before = await currentPage(page);
      const t0 = await transformOf(page);
      await drag(
        cdp,
        { x: mid.x - 20, y: mid.y },
        { x: mid.x + 120, y: mid.y },
      );
      expect(await currentPage(page)).toBe(before);
      expect(await transformOf(page)).not.toBe(t0);
      expect(await scaleOf(page)).toBeGreaterThan(1.5);

      // The side zones do not turn a zoomed page either.
      const box = await page.locator('[data-role="stage-zones"]').boundingBox();
      await page.touchscreen.tap(
        (box?.x ?? 0) + (box?.width ?? 0) * 0.9,
        mid.y,
      );
      await page.waitForTimeout(400);
      expect(await currentPage(page)).toBe(before);
    } finally {
      await leaveBook(page, bid);
    }
  });

  test("pinching back to 1 restores the page, and the release turns nothing", async ({
    page,
  }) => {
    const { bid, cdp, mid } = await open(page, 3);
    try {
      await pinch(cdp, mid, 60, 180);
      expect(await scaleOf(page)).toBeGreaterThan(1.5);
      await pinch(cdp, mid, 180, 60);
      await expect.poll(() => scaleOf(page)).toBe(1);
      expect(await currentPage(page)).toBe(3);
    } finally {
      await leaveBook(page, bid);
    }
  });

  test("double tap in the centre zooms in and a second double tap zooms out", async ({
    page,
  }) => {
    const { bid, mid } = await open(page, 3);
    try {
      await page.touchscreen.tap(mid.x, mid.y);
      await page.touchscreen.tap(mid.x, mid.y);
      await expect.poll(() => scaleOf(page)).toBeGreaterThan(2);
      expect(await currentPage(page)).toBe(3);
      await page.touchscreen.tap(mid.x, mid.y);
      await page.touchscreen.tap(mid.x, mid.y);
      await expect.poll(() => scaleOf(page)).toBe(1);
    } finally {
      await leaveBook(page, bid);
    }
  });

  test("at scale 1 the E-28 rules are unchanged: side tap and swipe turn the page", async ({
    page,
  }) => {
    const { bid, cdp, mid } = await open(page, 3);
    try {
      const box = await page.locator('[data-role="stage-zones"]').boundingBox();
      const x = (k: number): number => (box?.x ?? 0) + (box?.width ?? 0) * k;
      await page.touchscreen.tap(x(0.9), mid.y);
      await expect.poll(() => currentPage(page)).not.toBe(3);
      const turned = await currentPage(page);
      await drag(cdp, { x: x(0.7), y: mid.y }, { x: x(0.3), y: mid.y });
      await expect.poll(() => currentPage(page)).not.toBe(turned);
      expect(await scaleOf(page)).toBe(1);
    } finally {
      await leaveBook(page, bid);
    }
  });
});

test("a double click with the mouse never zooms (desktop input is unchanged)", async ({
  page,
}) => {
  test.skip(
    (page.viewportSize()?.width ?? 0) < 768,
    "the touch project has no mouse path",
  );
  const sid = await seriesId(page, SERIES.gungye);
  const first = (await booksOf(page, sid)).find((b) => b.status === "ok");
  const bid = first?.id ?? "";
  try {
    await resetBookPrefs(page, bid);
    watchProgressWrites(page);
    await openViewerDirect(page, sid, bid, 3);
    await waitForPage(page);
    await expect(viewer(page)).toBeVisible();
    const box = await page.locator('[data-role="stage-zones"]').boundingBox();
    await page.mouse.dblclick(
      (box?.x ?? 0) + (box?.width ?? 0) / 2,
      (box?.y ?? 0) + (box?.height ?? 0) / 2,
    );
    await page.waitForTimeout(400);
    expect(await scaleOf(page)).toBe(1);
  } finally {
    await leaveBook(page, bid);
  }
});
