/**
 * 권 표지 — ruling **E-69**, at the browser tier (U5).
 *
 * Four clauses, each asserted where a reader sees it — on the 권 card:
 *
 *  | clause | what is asserted |
 *  |---|---|
 *  | §2.2 | a landscape page 1 becomes the half the effective direction opens on; a portrait one stays whole |
 *  | §2.3 | a series direction set on the 시리즈 상세 `.seg` survives a change of the global default, and a book's own beats it |
 *  | §2.6 | 표지로 → 표지로 지정 changes the card to the region, 기본 표지로 되돌리기 puts the old URL back; a page series cover image is its first book's region |
 *  | §2.9 | a `shelf-progress/2` export merged straight back changes nothing |
 *
 * ## How a half is told from a half
 *
 * Not by colour. `coverLikeness` fetches the card's own image and page 1 in the
 * page, draws each candidate region of the page into the same small canvas as
 * the cover, and returns the mean per-channel difference of each. The expected
 * region must be the closest *and* closer than half the distance of every
 * other — so a centre crop (E-69 §1, the bug: `object-cover` showing the spine)
 * or the wrong half fails by name. The same comparison runs on both rounds:
 *
 *  * **synthetic** — `표지 테스트` (mkfixture §14): red|blue page 1s, books no
 *    other spec opens or shoots.
 *  * **real** — 군계 01권, the collection's one landscape page 1 (E-23). The
 *    guard below fails the test if that page is no longer landscape, rather
 *    than letting §2.2 pass on a page it never halves.
 *
 * ## State
 *
 * Every test puts back what it set (shelf.ts rule 2): the series direction,
 * the global default, the books' direction overrides, every designation and
 * the progress row the viewer writes when §2.6 opens it.
 * The number puts this file last in each viewport project, but the next
 * project's 01-library starts from what this one leaves.
 */
import type { ElementHandle, Locator, Page } from '@playwright/test'
import {
  SERIES,
  SYNTHETIC,
  SYNTHETIC_EXTRA,
  booksOf,
  clearProgress,
  currentPage,
  openViewerDirect,
  expect,
  seriesId,
  test,
  viewer,
  wakeChrome,
  viewerTopBar,
  type BookFact,
} from './shelf'

type Dir = 'rtl' | 'ltr'
interface Region {
  x: number
  y: number
  w: number
  h: number
}

const WHOLE: Region = { x: 0, y: 0, w: 1, h: 1 }
const LEFT: Region = { x: 0, y: 0, w: 0.5, h: 1 }
const RIGHT: Region = { x: 0.5, y: 0, w: 0.5, h: 1 }
const CENTRE: Region = { x: 0.25, y: 0, w: 0.5, h: 1 }

/** E-69 §2.2: rtl opens on the right half, ltr on the left. */
const FIRST_HALF: Record<Dir, Region> = { rtl: RIGHT, ltr: LEFT }
const other = (d: Dir): Dir => (d === 'rtl' ? 'ltr' : 'rtl')

/** The 시리즈 상세 `.seg`'s third value, 기본 = no series direction (U4a, ui-spec §5.1). */
const SEG_DEFAULT = 'default'

/**
 * The editor's accessible names (U4b, E-69 §2.8). Mirrored here, not imported:
 * the browser tier asserts what a screen reader would announce.
 */
const UI = {
  action: '표지로',
  dialog: /표지/,
  /** The 2:3 frame: focusable, arrows move it, Shift+arrows resize it. */
  frame: '표지 영역',
  set: '표지로 지정',
  reset: '기본 표지로 되돌리기',
} as const

/**
 * The books each round asserts on. `landscape` has a landscape page 1;
 * `override` is a second such book for "a book's direction beats the series"
 * (synthetic only — the real 군계 has no second landscape page 1, E-23);
 * `designate` is the book a cover is picked on, at `designatePage`.
 */
const SUBJECT = SYNTHETIC
  ? {
      series: SYNTHETIC_EXTRA.cover,
      landscape: '표지 테스트 01권.zip',
      override: '표지 테스트 02권.zip' as string | null,
      portrait: '표지 테스트 03권.zip' as string | null,
      designate: '표지 테스트 03권.zip',
      designatePage: 2,
      /** cover_kind 'page': the series card is its first book's cover (§2.6). */
      seriesFollowsFirstBook: true,
    }
  : {
      series: SERIES.gungye,
      landscape: '군계(軍鷄) 01권.zip',
      override: null,
      portrait: null,
      designate: '군계(軍鷄) 01권.zip',
      designatePage: 3,
      /** [cover].jpg — cover_kind 'file' is served as it is (§2.6). */
      seriesFollowsFirstBook: false,
    }

// ---------------------------------------------------------------------------
// Server state
// ---------------------------------------------------------------------------

interface CoverBook extends BookFact {
  ord: number
  cv: string
  cover_cv: string
  cover: (Region & { page: number }) | null
}

async function coverBooks(
  page: Page,
  sid: string,
): Promise<Map<string, CoverBook>> {
  const books = (await booksOf(page, sid)) as CoverBook[]
  return new Map(books.map((b) => [b.name, b]))
}

async function book(page: Page, sid: string, name: string): Promise<CoverBook> {
  const b = (await coverBooks(page, sid)).get(name)
  if (b === undefined) throw new Error(`${SUBJECT.series} must hold ${name}`)
  return b
}

async function globalDir(page: Page): Promise<Dir> {
  const response = await page.request.get('/api/settings')
  expect(response.ok(), 'GET /api/settings').toBe(true)
  return ((await response.json()) as { reading_direction: Dir })
    .reading_direction
}

async function putGlobalDir(page: Page, dir: Dir): Promise<void> {
  const response = await page.request.put('/api/settings', {
    data: { reading_direction: dir },
  })
  expect(response.ok(), 'PUT /api/settings').toBe(true)
}

async function putSeriesDir(
  page: Page,
  sid: string,
  dir: Dir | null,
): Promise<void> {
  const response = await page.request.put(`/api/series/${sid}/prefs`, {
    data: { reading_direction: dir },
  })
  expect(response.status(), `PUT /api/series/${sid}/prefs`).toBe(200)
}

async function putBookDir(
  page: Page,
  bid: string,
  dir: Dir | null,
): Promise<void> {
  const response = await page.request.put(`/api/books/${bid}/prefs`, {
    data: { reading_direction: dir },
  })
  expect(response.ok(), `PUT /api/books/${bid}/prefs`).toBe(true)
}

async function deleteCover(page: Page, bid: string): Promise<void> {
  const response = await page.request.delete(`/api/books/${bid}/cover`)
  expect(response.status(), `DELETE /api/books/${bid}/cover`).toBe(204)
}

/**
 * Everything this file can set, put back. Run first *and* last: first because
 * a failed earlier project may have left it, last for the next spec.
 *
 * Only this file's books, and a direction only where the book has an override
 * row: in the real round 군계 01권 is also 04-viewer's and 09's book, and their
 * own resets own the rest of its prefs.
 */
async function resetCoverState(
  page: Page,
  sid: string,
  global: Dir,
): Promise<void> {
  await page.goto('about:blank')
  await putSeriesDir(page, sid, null)
  const mine = new Set(
    [
      SUBJECT.landscape,
      SUBJECT.override,
      SUBJECT.portrait,
      SUBJECT.designate,
    ].filter((n): n is string => n !== null),
  )
  for (const b of (await coverBooks(page, sid)).values()) {
    if (!mine.has(b.name)) continue
    const prefs = await page.request.get(`/api/books/${b.id}/prefs`)
    expect(prefs.ok(), `GET /api/books/${b.id}/prefs`).toBe(true)
    if (((await prefs.json()) as { is_override: boolean }).is_override) {
      await putBookDir(page, b.id, null)
    }
    await deleteCover(page, b.id)
    // §2.6 opens the viewer, and useSaveProgress writes a row the next
    // viewport's 01-library would photograph.
    await clearProgress(page, b.id)
  }
  await putGlobalDir(page, global)
}

// ---------------------------------------------------------------------------
// The comparison
// ---------------------------------------------------------------------------

/**
 * The card's `<img>` for `b` on the 시리즈 상세 screen, once it is the book
 * cover at `cover_cv` (E-69 §2.8: not `/thumbs/1`) *and* the reader can see
 * it: visible and decoded, so a card left broken after a 202 fails here.
 */
async function cardImage(page: Page, b: CoverBook): Promise<Locator> {
  const img = page
    .locator('[data-testid="volume-tile"], [data-testid="volume-row"]')
    .filter({ has: page.getByText(b.name, { exact: true }) })
    .locator('img')
    .first()
  // Parsed, not matched: §2.6 leaves cover_cv's alphabet open, and a value
  // the SPA percent-encodes is still the same `v`.
  await expect
    .poll(
      async () => {
        const src = await img.getAttribute('src')
        const url = src === null ? null : new URL(src, page.url())
        return {
          book: url?.pathname.endsWith(`/api/books/${b.id}/cover`) ?? false,
          v: url?.searchParams.get('v') ?? null,
        }
      },
      {
        timeout: 15_000,
        message: `${b.name}: the card shows the book cover at cover_cv`,
      },
    )
    .toEqual({ book: true, v: b.cover_cv })
  await expect(img, `${b.name}: the card image is on screen`).toBeVisible()
  await expect
    .poll(
      () =>
        img.evaluate(
          (el: HTMLImageElement) => el.complete && el.naturalWidth > 0,
        ),
      {
        timeout: 15_000,
        message: `${b.name}: the card image decoded (complete, naturalWidth > 0)`,
      },
    )
    .toBe(true)
  return img
}

/** The card's cover URL, absolute, once `cardImage` holds. */
async function cardCoverURL(page: Page, b: CoverBook): Promise<string> {
  const src = await (await cardImage(page, b)).getAttribute('src')
  if (src === null) throw new Error(`${b.name}: the card image lost its src`)
  return new URL(src, page.url()).href
}

interface Likeness {
  /** Mean per-channel difference, 0–255, per candidate. */
  diff: Record<string, number>
  page: { w: number; h: number }
}

interface LikenessArg {
  /** The cover as the card drew it — or, when null, fetched from `coverURL`. */
  card: HTMLImageElement | null
  coverURL: string | null
  pageURL: string
  candidates: Record<string, Region>
}

/**
 * The one pixel comparison, run in the page. Self-contained on purpose:
 * `page.evaluate` serialises the function's source, so nothing it calls may
 * live outside its body. The cover is the card's own decoded `<img>` when
 * there is one, else a fetch; page 1 is always a fetch.
 */
async function likenessInPage(arg: LikenessArg): Promise<Likeness> {
  // E-69 §2.6: an uncached cover answers 202 + Retry-After while it is
  // made. Only a 200 is an image.
  const bitmap = async (url: string): Promise<ImageBitmap> => {
    for (let attempt = 0; attempt < 15; attempt++) {
      const response = await fetch(url, { cache: 'no-store' })
      if (response.status === 200)
        return createImageBitmap(await response.blob())
      if (response.status !== 202)
        throw new Error(`${url}: ${String(response.status)}`)
      const after = Number(response.headers.get('Retry-After') ?? Number.NaN)
      const seconds =
        Number.isFinite(after) && after > 0 ? Math.min(after, 2) : 1
      await new Promise((resolve) => setTimeout(resolve, seconds * 1000))
    }
    throw new Error(`${url}: still 202 after 15 tries`)
  }
  interface Source {
    image: CanvasImageSource
    width: number
    height: number
  }
  const fromBitmap = (b: ImageBitmap): Source => ({
    image: b,
    width: b.width,
    height: b.height,
  })
  let cover: Source
  if (arg.card !== null) {
    cover = {
      image: arg.card,
      width: arg.card.naturalWidth,
      height: arg.card.naturalHeight,
    }
  } else if (arg.coverURL !== null) {
    cover = fromBitmap(await bitmap(arg.coverURL))
  } else {
    throw new Error('likenessInPage needs a card or a cover URL')
  }
  const pageImage = fromBitmap(await bitmap(arg.pageURL))
  const W = 24
  const H = Math.max(1, Math.round((W * cover.height) / cover.width))
  const pixels = (src: Source, r: Region) => {
    // Direct reductions alias fine print in real scans; compare both images
    // after the same progressive low-pass reduction instead.
    let width = Math.max(W, Math.ceil((r.w * src.width) / 2))
    let height = Math.max(H, Math.ceil((r.h * src.height) / 2))
    let canvas = new OffscreenCanvas(width, height)
    let ctx = canvas.getContext('2d')
    if (ctx === null) throw new Error('no 2d context')
    ctx.imageSmoothingQuality = 'high'
    ctx.drawImage(
      src.image,
      r.x * src.width,
      r.y * src.height,
      r.w * src.width,
      r.h * src.height,
      0,
      0,
      width,
      height,
    )
    while (width > W || height > H) {
      width = Math.max(W, Math.ceil(width / 2))
      height = Math.max(H, Math.ceil(height / 2))
      const next = new OffscreenCanvas(width, height)
      const nextCtx = next.getContext('2d')
      if (nextCtx === null) throw new Error('no 2d context')
      nextCtx.imageSmoothingQuality = 'high'
      nextCtx.drawImage(canvas, 0, 0, width, height)
      canvas = next
      ctx = nextCtx
    }
    return ctx.getImageData(0, 0, W, H).data
  }
  const mine = pixels(cover, { x: 0, y: 0, w: 1, h: 1 })
  const diff: Record<string, number> = {}
  for (const [name, region] of Object.entries(arg.candidates)) {
    const theirs = pixels(pageImage, region)
    let sum = 0
    for (let i = 0; i < mine.length; i++) {
      if (i % 4 === 3) continue // alpha
      sum += Math.abs((mine[i] ?? 0) - (theirs[i] ?? 0))
    }
    diff[name] = sum / ((mine.length / 4) * 3)
  }
  return { diff, page: { w: pageImage.width, h: pageImage.height } }
}

/** A cover served at `coverURL` (the series cover endpoint) against page regions. */
async function coverLikeness(
  page: Page,
  coverURL: string,
  pageURL: string,
  candidates: Record<string, Region>,
): Promise<Likeness> {
  return page.evaluate(likenessInPage, {
    card: null,
    coverURL,
    pageURL,
    candidates,
  })
}

/** `b`'s card, the `<img>` the reader sees, against page regions. */
async function cardLikeness(
  page: Page,
  b: CoverBook,
  pageURL: string,
  candidates: Record<string, Region>,
): Promise<Likeness> {
  const card = (await (
    await cardImage(page, b)
  ).elementHandle()) as ElementHandle<HTMLImageElement>
  try {
    return await page.evaluate(likenessInPage, {
      card,
      coverURL: null,
      pageURL,
      candidates,
    })
  } finally {
    await card.dispose()
  }
}

/** `expected` is the closest candidate, by at least a factor of two. */
function expectClosest(
  likeness: Likeness,
  expected: string,
  why: string,
): void {
  const mine = likeness.diff[expected]
  const rivals = Object.entries(likeness.diff).filter(
    ([name]) => name !== expected,
  )
  const detail = `${why}: ${JSON.stringify(likeness.diff)}`
  for (const [, d] of rivals) {
    expect(mine, detail).toBeLessThan(d / 2)
  }
}

function pageURL(b: CoverBook, n: number): string {
  return `/api/books/${b.id}/pages/${String(n)}?v=${b.cv}`
}

async function gotoSeries(page: Page, sid: string): Promise<void> {
  await page.goto(`/series/${sid}`)
  await expect(
    page.getByRole('heading', { level: 1, name: SUBJECT.series }),
  ).toBeVisible()
  await waitCoverCards(page,sid)
}

/**
 * E-23's guard: §2.2 needs page 1 to be landscape. If the sample stops being
 * one, every half assertion below would compare a portrait page with itself.
 */
async function expectLandscapePage1(page: Page, b: CoverBook): Promise<void> {
  const { page: dims } = await cardLikeness(page, b, pageURL(b, 1), {
    whole: WHOLE,
  })
  expect(
    dims.w > dims.h,
    `E-23: ${b.name} page 1 is ${String(dims.w)}×${String(dims.h)} — no longer the landscape sample E-69 §2.2 is asserted on`,
  ).toBe(true)
}

async function waitCoverCards(page: Page, sid: string): Promise<void> {
  for (const b of (await coverBooks(page, sid)).values())
    await cardCoverURL(page, b)
}

// ---------------------------------------------------------------------------
// §2.2 · §2.3
// ---------------------------------------------------------------------------

test('E-69 §2.2 · a landscape page 1 is the half the reading direction opens on', async ({
  page,
}) => {
  const sid = await seriesId(page, SUBJECT.series)
  const global = await globalDir(page)
  await resetCoverState(page, sid, global)
  try {
    await gotoSeries(page, sid)
    const a = await book(page, sid, SUBJECT.landscape)
    await expectLandscapePage1(page, a)
    expectClosest(
      await cardLikeness(page, a, pageURL(a, 1), {
        expected: FIRST_HALF[global],
        otherHalf: FIRST_HALF[other(global)],
        centre: CENTRE,
        whole: WHOLE,
      }),
      'expected',
      `${a.name} under the global ${global}`,
    )

    if (SUBJECT.portrait !== null) {
      const c = await book(page, sid, SUBJECT.portrait)
      expectClosest(
        await cardLikeness(page, c, pageURL(c, 1), {
          whole: WHOLE,
          left: LEFT,
          right: RIGHT,
        }),
        'whole',
        `${c.name}: a portrait page 1 is shown whole`,
      )
    }

    // The opposite direction, set on the book itself: the other half.
    await putBookDir(page, a.id, other(global))
    await page.reload()
    const flipped = await book(page, sid, SUBJECT.landscape)
    expect(
      flipped.cover_cv,
      'E-69 §2.7: the direction is in cover_cv',
    ).not.toBe(a.cover_cv)
    expectClosest(
      await cardLikeness(page, flipped, pageURL(flipped, 1), {
        expected: FIRST_HALF[other(global)],
        otherHalf: FIRST_HALF[global],
        centre: CENTRE,
      }),
      'expected',
      `${a.name} with its own ${other(global)}`,
    )
  } finally {
    await resetCoverState(page, sid, global)
  }
})

test('E-69 §2.3 · a series direction outlives a change of the global default', async ({
  page,
}) => {
  const sid = await seriesId(page, SUBJECT.series)
  const global = await globalDir(page)
  await resetCoverState(page, sid, global)
  try {
    await gotoSeries(page, sid)
    const seriesDir = other(global)
    const seg = (value: string) =>
      page.locator(`[aria-label="읽기 방향"] label[data-value="${value}"]`)

    // Three values (ui-spec §5.1): with no series direction stored, 기본 is
    // the one checked — not whichever of rtl/ltr the global happens to be.
    await expect(
      seg(SEG_DEFAULT),
      'no series direction: 기본 is checked',
    ).toHaveAttribute('data-checked', 'true')

    // The 시리즈 상세 `.seg` is a write now (§2.3), not a localStorage seed.
    const option = seg(seriesDir)
    const put = page.waitForResponse(
      (r) =>
        r.url().includes(`/api/series/${sid}/prefs`) &&
        r.request().method() === 'PUT',
    )
    await option.click()
    expect((await put).status(), 'the .seg writes the series direction').toBe(
      200,
    )

    const detail = await page.request.get(`/api/series/${sid}`)
    expect(
      ((await detail.json()) as { reading_direction: Dir | null })
        .reading_direction,
    ).toBe(seriesDir)

    if (SUBJECT.override !== null) {
      const b = await book(page, sid, SUBJECT.override)
      await putBookDir(page, b.id, global)
      await page.reload()
      await waitCoverCards(page, sid)
    }

    // Flip the global default to the series' side and back past it: the
    // series must not move either time.
    for (const g of [seriesDir, global]) {
      await putGlobalDir(page, g)
      await page.reload()
      await expect(seg(seriesDir)).toHaveAttribute('data-checked', 'true')
      const a = await book(page, sid, SUBJECT.landscape)
      expectClosest(
        await cardLikeness(page, a, pageURL(a, 1), {
          expected: FIRST_HALF[seriesDir],
          otherHalf: FIRST_HALF[other(seriesDir)],
          centre: CENTRE,
        }),
        'expected',
        `${a.name}: series ${seriesDir}, global ${g}`,
      )
      if (SUBJECT.override !== null) {
        const b = await book(page, sid, SUBJECT.override)
        expectClosest(
          await cardLikeness(page, b, pageURL(b, 1), {
            expected: FIRST_HALF[global],
            otherHalf: FIRST_HALF[other(global)],
          }),
          'expected',
          `${b.name}: its own ${global} beats the series ${seriesDir}`,
        )
      }
    }

    // 기본 clears the series direction with an explicit null (§2.6: "지정 없음"
    // is null only), and the card goes back to the global's half.
    const cleared = page.waitForResponse(
      (r) =>
        r.url().includes(`/api/series/${sid}/prefs`) &&
        r.request().method() === 'PUT',
    )
    await seg(SEG_DEFAULT).click()
    const clearedPut = await cleared
    expect(clearedPut.status(), '기본 writes the series direction').toBe(200)
    expect(
      clearedPut.request().postDataJSON(),
      '기본 is an explicit null',
    ).toEqual({
      reading_direction: null,
    })
    await page.reload()
    await expect(seg(SEG_DEFAULT)).toHaveAttribute('data-checked', 'true')
    const a = await book(page, sid, SUBJECT.landscape)
    expectClosest(
      await cardLikeness(page, a, pageURL(a, 1), {
        expected: FIRST_HALF[global],
        otherHalf: FIRST_HALF[other(global)],
        centre: CENTRE,
      }),
      'expected',
      `${a.name}: series 기본, global ${global}`,
    )
  } finally {
    await resetCoverState(page, sid, global)
  }
})

// ---------------------------------------------------------------------------
// §2.6
// ---------------------------------------------------------------------------

test('E-69 §2.6 · 표지로 지정 changes the card, 기본 표지로 되돌리기 puts it back', async ({
  page,
}) => {
  const sid = await seriesId(page, SUBJECT.series)
  const global = await globalDir(page)
  await resetCoverState(page, sid, global)
  try {
    const before = await book(page, sid, SUBJECT.designate)

    // §2.8: a save invalidates books·series·continue. Watched the way a reader
    // sees it — the series screen's queries are filled first, and every trip
    // stays inside the app, so a missing invalidation leaves the old card up
    // for staleTime (30 s, api/queries.ts). The round trip has to finish in
    // those 30 s; a slower one refetches and *misses* the defect, it never
    // turns a correct build red.
    await gotoSeries(page, sid)
    await cardCoverURL(page, before)

    /**
     * Into the viewer at the designation page without leaving the app.
     * Not openViewerDirect (a page.goto, which throws the query cache away),
     * and not a tile click (that opens on the progress page): a history entry
     * plus the popstate the router listens for. The URL's head is kept, so a
     * base_path survives.
     */
    const enterViewer = async (): Promise<void> => {
      await page.evaluate(
        ({ sid, bid, at }) => {
          const head = location.pathname.replace(/\/series\/.*$/, '')
          history.pushState(
            {},
            '',
            `${head}/series/${sid}/books/${bid}?page=${String(at)}`,
          )
          dispatchEvent(new PopStateEvent('popstate'))
        },
        { sid, bid: before.id, at: SUBJECT.designatePage },
      )
      await expect(viewer(page)).toBeVisible()
      await expect
        .poll(() => currentPage(page), { timeout: 15_000 })
        .toBe(SUBJECT.designatePage)
    }

    /** 뒤로, the viewer's own way out, back to 시리즈 상세 in the app. */
    const backToSeries = async (): Promise<void> => {
      await wakeChrome(page)
      await viewerTopBar(page).getByRole('button', { name: '뒤로' }).click()
      await expect(viewer(page)).toHaveCount(0)
      await expect(
        page.getByRole('heading', { level: 1, name: SUBJECT.series }),
      ).toBeVisible()
    }

    await enterViewer()
    const dialog = page.getByRole('dialog', { name: UI.dialog })

    /**
     * Keyboard only (§2.8): open the editor on this page, press `keys` on the
     * frame, 표지로 지정, and read back what the server stored. Two saves, so
     * resizing and moving are each observed on their own.
     */
    const designateWith = async (
      key: string,
      times: number,
    ): Promise<CoverBook> => {
      await wakeChrome(page)
      await viewerTopBar(page).getByRole('button', { name: UI.action }).click()
      await expect(dialog).toBeVisible()
      const frame = dialog.getByRole('slider', { name: UI.frame })
      await frame.focus()
      // The frame's place is spoken, not only drawn: the wording is U4b's, so
      // only "there is one, and the keys change it" is asserted here.
      const spoken = await frame.getAttribute('aria-valuetext')
      expect(
        spoken ?? '',
        'the frame announces its place (aria-valuetext)',
      ).not.toBe('')
      for (let i = 0; i < times; i++) await page.keyboard.press(key)
      await expect(
        frame,
        `${key} changes what the frame announces`,
      ).not.toHaveAttribute('aria-valuetext', spoken ?? '')
      const put = page.waitForResponse(
        (r) =>
          r.url().includes(`/api/books/${before.id}/cover`) &&
          r.request().method() === 'PUT',
      )
      await dialog.getByRole('button', { name: UI.set }).click()
      expect((await put).status(), 'PUT /api/books/{bid}/cover').toBe(200)
      await expect(dialog).toBeHidden()
      const saved = await book(page, sid, SUBJECT.designate)
      expect(saved.cover, 'BookSummary.cover is the designation').not.toBeNull()
      expect(saved.cover?.page).toBe(SUBJECT.designatePage)
      return saved
    }

    // (a) Shift+arrows resize, nothing else.
    const shrunk = await designateWith('Shift+ArrowLeft', 4)
    const cover1 = shrunk.cover as Region & { page: number }
    expect(cover1.w, 'Shift+ArrowLeft shrank the frame').toBeLessThan(1)
    expect(shrunk.cover_cv).not.toBe(before.cover_cv)

    // (b) The editor reopens on the stored region (§2.8); arrows move it and
    // leave its size alone.
    const after = await designateWith('ArrowRight', 3)
    const region = after.cover as Region & { page: number }
    const moved = `ArrowRight moved ${JSON.stringify(cover1)} to ${JSON.stringify(region)}`
    expect(Math.abs(region.w - cover1.w), `${moved}: same width`).toBeLessThan(
      1e-6,
    )
    expect(Math.abs(region.h - cover1.h), `${moved}: same height`).toBeLessThan(
      1e-6,
    )
    expect(region.x, `${moved}: further right`).toBeGreaterThan(cover1.x)
    expect(after.cover_cv).not.toBe(shrunk.cover_cv)

    // In-app return: the card reaching the new cover_cv is the invalidation.
    await backToSeries()
    expectClosest(
      await cardLikeness(page, after, pageURL(after, region.page), {
        region,
        wholePage: WHOLE,
      }),
      'region',
      `${after.name}: the card is the designated region of page ${String(region.page)}`,
    )

    // Back to the default, from the same dialog, the same way in and out.
    await enterViewer()
    await wakeChrome(page)
    await viewerTopBar(page).getByRole('button', { name: UI.action }).click()
    await expect(dialog).toBeVisible()
    const del = page.waitForResponse(
      (r) =>
        r.url().includes(`/api/books/${before.id}/cover`) &&
        r.request().method() === 'DELETE',
    )
    await dialog.getByRole('button', { name: UI.reset }).click()
    expect((await del).status(), 'DELETE /api/books/{bid}/cover').toBe(204)
    // §2.8 does not say whether 되돌리기 closes the dialog; close it if not.
    if (await dialog.isVisible()) await page.keyboard.press('Escape')
    await expect(dialog).toBeHidden()

    const restored = await book(page, sid, SUBJECT.designate)
    expect(restored.cover).toBeNull()
    expect(restored.cover_cv, 'the default cover has its old URL back').toBe(
      before.cover_cv,
    )
    await backToSeries()
    await cardCoverURL(page, restored)
  } finally {
    await resetCoverState(page, sid, global)
  }
})

async function seriesCoverCV(page: Page, sid: string): Promise<string | null> {
  const response = await page.request.get(`/api/series/${sid}`)
  expect(response.ok()).toBe(true)
  return ((await response.json()) as { cover_cv: string | null }).cover_cv
}

/** The series cover endpoint itself (w from THUMB_WIDTHS), not its card. */
function seriesCoverURL(sid: string, cv: string | null): string {
  const query = new URLSearchParams({ w: '240' })
  if (cv !== null) query.set('v', cv)
  return `/api/series/${sid}/cover?${query.toString()}`
}

/** The bytes `url` serves once made: a 202 (E-69 §2.6) is waited out, not read. */
async function coverBytes(page: Page, url: string): Promise<Buffer> {
  for (let attempt = 0; attempt < 15; attempt++) {
    const response = await page.request.get(url)
    if (response.status() === 200) return response.body()
    expect(response.status(), `GET ${url}`).toBe(202)
    const after = Number(response.headers()['retry-after'] ?? Number.NaN)
    const seconds = Number.isFinite(after) && after > 0 ? Math.min(after, 2) : 1
    await new Promise((resolve) => setTimeout(resolve, seconds * 1000))
  }
  throw new Error(`${url}: still 202 after 15 tries`)
}

test("E-69 §2.6 · a page series cover is its first book's cover; a cover file is not", async ({
  page,
}) => {
  const sid = await seriesId(page, SUBJECT.series)
  const global = await globalDir(page)
  await resetCoverState(page, sid, global)
  try {
    const first = await book(page, sid, SUBJECT.landscape)
    expect(first.ord, `${first.name} is the series' first book`).toBe(0)
    const seriesBefore = await seriesCoverCV(page, sid)
    if (SUBJECT.seriesFollowsFirstBook) {
      expect(
        seriesBefore,
        'cover_kind page: the series cover_cv is the first book cover_cv',
      ).toBe(first.cover_cv)
    } else {
      expect(seriesBefore, 'a cover file has no book cover_cv').toBeNull()
    }
    const bytesBefore = SUBJECT.seriesFollowsFirstBook
      ? null
      : await coverBytes(page, seriesCoverURL(sid, seriesBefore))

    // Across the red|blue boundary, a quarter red: on the synthetic page 1 it
    // is told from either half and from the whole page (a symmetric straddle
    // would be a scaled copy of the whole). 48×72 on its 120×80 page, 2:3.
    const region = { page: 1, x: 0.4, y: 0.05, w: 0.4, h: 0.9 }
    const put = await page.request.put(`/api/books/${first.id}/cover`, {
      data: region,
    })
    expect(put.status(), 'PUT /api/books/{bid}/cover').toBe(200)
    const designated = await book(page, sid, SUBJECT.landscape)
    const seriesAfter = await seriesCoverCV(page, sid)
    if (SUBJECT.seriesFollowsFirstBook) {
      expect(
        seriesAfter,
        "the series cover follows its first book's designation",
      ).toBe(designated.cover_cv)
      await gotoSeries(page, sid)
      expectClosest(
        await coverLikeness(
          page,
          seriesCoverURL(sid, seriesAfter),
          pageURL(designated, 1),
          {
            region,
            left: LEFT,
            right: RIGHT,
            whole: WHOLE,
          },
        ),
        'region',
        `the series cover image is ${designated.name}'s designated region`,
      )
    } else {
      expect(
        seriesAfter,
        "a cover file ignores the first book's designation",
      ).toBe(seriesBefore)
      const bytesAfter = await coverBytes(
        page,
        seriesCoverURL(sid, seriesAfter),
      )
      expect(
        bytesBefore !== null && bytesAfter.equals(bytesBefore),
        "a cover file's image is the same bytes after the first book's designation",
      ).toBe(true)
    }
  } finally {
    await resetCoverState(page, sid, global)
  }
})

// ---------------------------------------------------------------------------
// §2.9
// ---------------------------------------------------------------------------

test('E-69 §2.9 · a shelf-progress/2 export merged back changes nothing', async ({
  page,
}) => {
  const sid = await seriesId(page, SUBJECT.series)
  const global = await globalDir(page)
  await resetCoverState(page, sid, global)
  try {
    const target = await book(page, sid, SUBJECT.designate)
    const region = {
      page: SUBJECT.designatePage,
      x: 0.1,
      y: 0.05,
      w: 0.5,
      h: 0.6,
    }
    const put = await page.request.put(`/api/books/${target.id}/cover`, {
      data: region,
    })
    expect(put.status(), 'PUT /api/books/{bid}/cover').toBe(200)
    await putSeriesDir(page, sid, other(global))

    const before = await book(page, sid, SUBJECT.designate)
    const exported = await page.request.get('/api/progress/export')
    expect(exported.ok(), 'GET /api/progress/export').toBe(true)
    const file = (await exported.json()) as {
      format: string
      covers: {
        book_id: string
        page: number
        page_count: number
        x: number
        y: number
        w: number
        h: number
      }[]
      series_prefs: { series_id: string; reading_direction: Dir | null }[]
    }
    expect(file.format, 'E-69 §2.9: export always writes /2').toBe(
      'shelf-progress/2',
    )
    expect(
      file.covers.find((c) => c.book_id === target.id),
      'the designation is exported, with the page count it was made against (§2.5)',
    ).toMatchObject({ ...region, page_count: target.page_count })
    expect(
      file.series_prefs.find((p) => p.series_id === sid),
      'the series direction is exported',
    ).toMatchObject({ reading_direction: other(global) })

    const imported = await page.request.post(
      '/api/progress/import?strategy=merge',
      { data: file },
    )
    expect(imported.ok(), 'POST /api/progress/import').toBe(true)

    const after = await book(page, sid, SUBJECT.designate)
    expect(after.cover, 'the merge left the designation alone').toEqual(
      before.cover,
    )
    expect(after.cover_cv, 'and its URL').toBe(before.cover_cv)
    const detail = (await (
      await page.request.get(`/api/series/${sid}`)
    ).json()) as {
      reading_direction: Dir | null
    }
    expect(detail.reading_direction, 'and the series direction').toBe(
      other(global),
    )
  } finally {
    await resetCoverState(page, sid, global)
  }
})

test('E-69 §2.8 · the palette opens the editor and the pointer moves and resizes its frame', async ({
  page,
}) => {
  const sid = await seriesId(page, SUBJECT.series)
  const global = await globalDir(page)
  await resetCoverState(page, sid, global)
  try {
    const target = await book(page, sid, SUBJECT.designate)
    await openViewerDirect(page, sid, target.id, SUBJECT.designatePage)
    await page.keyboard.press('Control+k')
    const palette = page.getByRole('dialog', { name: '커맨드 팔레트' })
    await expect(palette).toBeVisible()
    await palette.getByRole('option', { name: /표지로/ }).click()
    const dialog = page.getByRole('dialog', { name: UI.dialog })
    const frame = dialog.getByRole('slider', { name: UI.frame })
    await expect(frame).toBeVisible()
    const before = await frame.boundingBox()
    if (before === null) throw new Error('cover frame has no bounds')
    await page.mouse.move(
      before.x + before.width - 5,
      before.y + before.height - 5,
    )
    await page.mouse.down()
    await page.mouse.move(
      before.x + before.width - 25,
      before.y + before.height - 30,
      { steps: 5 },
    )
    await page.mouse.up()
    const smaller = await frame.boundingBox()
    if (smaller === null) throw new Error('resized frame has no bounds')
    expect(smaller.width).toBeLessThan(before.width)
    const spoken = await frame.getAttribute('aria-valuetext')
    await page.mouse.move(
      smaller.x + smaller.width / 2,
      smaller.y + smaller.height / 2,
    )
    await page.mouse.down()
    await page.mouse.move(
      smaller.x + smaller.width / 2 + 15,
      smaller.y + smaller.height / 2,
      { steps: 5 },
    )
    await page.mouse.up()
    await expect(frame).not.toHaveAttribute('aria-valuetext', spoken ?? '')
    await expect(dialog.getByAltText('표지 미리보기')).toBeVisible()
    await dialog.getByRole('button', { name: '취소', exact: true }).click()
    expect((await book(page, sid, SUBJECT.designate)).cover).toBeNull()
  } finally {
    await resetCoverState(page, sid, global)
  }
})

test('E-69 §2.8 · 양면의 두 쪽 중 선택한 쪽을 표지로 저장한다', async ({ page }, testInfo) => {
  test.skip(!SYNTHETIC, '합성 컬렉션의 고정된 세로 쪽 쌍으로 쪽 선택을 검증한다')
  const sid = await seriesId(page, SUBJECT.series)
  const global = await globalDir(page)
  await resetCoverState(page, sid, global)
  const target = await book(page, sid, SUBJECT.designate)
  try {
    const prefs = await (await page.request.get(`/api/books/${target.id}/prefs`)).json() as {is_override:boolean}
    expect(prefs.is_override, '이 검증용 책은 기존 권 설정이 없다').toBe(false)
    await openViewerDirect(page,sid,target.id,1)
    await wakeChrome(page)
    await viewerTopBar(page).locator('[aria-label="표시 모드"] label[data-value="spread"]').click()
    await viewerTopBar(page).getByRole('button',{name:UI.action,exact:true}).click()
    const dialog=page.getByRole('dialog',{name:UI.dialog})
    await expect(dialog.getByRole('button',{name:'1쪽',exact:true})).toBeVisible()
    await dialog.getByRole('button',{name:'2쪽',exact:true}).click()
    await expect(dialog.getByAltText('2쪽 표지 원본')).toBeVisible()
    await expect(dialog.getByRole('slider',{name:UI.frame})).toBeVisible()
    await dialog.screenshot({path:testInfo.outputPath('cover-editor.png')})
    const saved=page.waitForResponse(r=>r.url().includes(`/api/books/${target.id}/cover`)&&r.request().method()==='PUT')
    await dialog.getByRole('button',{name:UI.set,exact:true}).click()
    expect((await saved).status()).toBe(200)
    expect((await book(page,sid,SUBJECT.designate)).cover?.page).toBe(2)
  } finally {
    await page.goto('about:blank')
    await page.request.put(`/api/books/${target.id}/prefs`,{data:{display_mode:null}})
    await resetCoverState(page,sid,global)
  }
})

test('E-69 §2.6 · 읽을 수 없게 된 책도 저장한 표지를 상단 바와 팔레트에서 초기화한다', async ({ page }) => {
  const sid = await seriesId(page,SYNTHETIC ? SERIES.gungye : SERIES.dnangel)
  const target=(await booksOf(page,sid)).find(b=>b.status==='error'&&b.page_count===0)
  if(target===undefined) throw new Error('the broken book fixture is missing')
  const detail=await (await page.request.get(`/api/books/${target.id}`)).json() as {root_name:string;path:string;cover:Region|null}
  expect(detail.cover,'이 검증은 기존 사용자 표지를 덮어쓰지 않는다').toBeNull()
  try {
    const imported=await page.request.post('/api/progress/import',{data:{format:'shelf-progress/2',id_version:'shelf-id/1',covers:[{book_id:target.id,series_id:sid,root_name:detail.root_name,book_path:detail.path,page:1,page_count:1,x:0,y:0,w:0.5,h:0.75,updated_at:Math.floor(Date.now()/1000)}]}})
    expect(imported.status()).toBe(200)
    await openViewerDirect(page,sid,target.id,1)
    await wakeChrome(page)
    await viewerTopBar(page).getByRole('button',{name:UI.action,exact:true}).click()
    const dialog=page.getByRole('dialog',{name:UI.dialog})
    await expect(dialog.getByRole('button',{name:UI.set,exact:true})).toBeDisabled()
    await expect(dialog.getByRole('button',{name:UI.reset,exact:true})).toBeVisible()
    await dialog.getByRole('button',{name:'취소',exact:true}).click()
    await page.keyboard.press('Control+k')
    await page.getByRole('dialog',{name:'커맨드 팔레트'}).getByRole('option',{name:/표지로/}).click()
    await dialog.getByRole('button',{name:UI.reset,exact:true}).click()
    await expect(dialog).toBeHidden()
    const after=await (await page.request.get(`/api/books/${target.id}`)).json() as {cover:Region|null}
    expect(after.cover).toBeNull()
  } finally {
    await page.goto('about:blank')
    await deleteCover(page,target.id)
  }
})
