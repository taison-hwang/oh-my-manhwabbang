/**
 * Page prefetch (FR-VWR-006, NFR-PRF-002).
 *
 * `settings.prefetch` pages ahead of what is on screen, plus one behind, warmed
 * through `new Image()` against **exactly the URL the `<img>` will use** —
 * same path, same `?v={cv}` — so the hit lands in the shared HTTP cache and the
 * page turn is a cache read rather than a request (impl-plan §3 WP-11 #6).
 *
 * Deliberately not a TanStack Query call: the page endpoint returns raw image
 * bytes, and pulling them through `fetch` would put a second copy in JS memory
 * and *not* populate the image cache the `<img>` reads from. `new Image()` is
 * the only mechanism that warms the right cache.
 *
 * The image factory is injectable so the request set can be asserted without a
 * network: jsdom does not load `img.src`, so a test that watched MSW would pass
 * whatever this hook did.
 */

import { useEffect, useRef } from 'react'

import { pageUrl } from '../../api/urls'

/** FR-VWR-006 asks for 3–5; impl-plan §1.1 pins the default at 4. */
export const DEFAULT_PREFETCH = 4
/** `UserSettings.prefetch` is documented as 0..20 (arch §7.8). */
export const MAX_PREFETCH = 20

/**
 * The pages to warm, in request order: ahead first (they are needed next),
 * then the one behind (for a page-back).
 *
 * `shownCount` is how many pages the stage is currently displaying, so 양면
 * prefetches from *after* the right-hand page rather than re-requesting it.
 */
export function prefetchPages(
  page: number,
  pageCount: number,
  count: number,
  shownCount: number,
): number[] {
  if (pageCount <= 0 || page < 1) return []
  const ahead = Math.max(0, Math.min(MAX_PREFETCH, Math.trunc(count)))
  const last = page + Math.max(1, shownCount) - 1
  const out: number[] = []
  for (let i = 1; i <= ahead; i++) {
    const n = last + i
    if (n > pageCount) break
    out.push(n)
  }
  if (page > 1) out.push(page - 1)
  return out
}

export interface PrefetchInput {
  /** `null` while the book is still loading — the hook then does nothing. */
  bookId: string | null
  /** The book's `cv`; part of the URL, so a change invalidates the warm set. */
  cv: string | null
  page: number
  pageCount: number
  /** Pages currently on the stage (1 in 단면, 2 in 양면). */
  shownCount: number
  count?: number
  /** Test seam. Production is `new Image()`. */
  createImage?: () => HTMLImageElement
}

function defaultCreateImage(): HTMLImageElement {
  return new Image()
}

export function usePrefetch(input: PrefetchInput): void {
  const {
    bookId,
    cv,
    page,
    pageCount,
    shownCount,
    count = DEFAULT_PREFETCH,
    createImage = defaultCreateImage,
  } = input

  // Everything already asked for, so scrubbing back and forth over the same
  // twenty pages does not re-issue twenty requests a second.
  const requested = useRef<Set<string>>(new Set())
  // Held so the elements are not collected before the response arrives.
  const inFlight = useRef<HTMLImageElement[]>([])

  useEffect(() => {
    requested.current = new Set()
    inFlight.current = []
  }, [bookId, cv])

  useEffect(() => {
    if (bookId === null || pageCount <= 0) return
    for (const n of prefetchPages(page, pageCount, count, shownCount)) {
      const url = pageUrl(bookId, n, { v: cv })
      if (requested.current.has(url)) continue
      requested.current.add(url)
      const image = createImage()
      inFlight.current.push(image)
      const done = (): void => {
        inFlight.current = inFlight.current.filter((held) => held !== image)
      }
      image.addEventListener('load', done, { once: true })
      image.addEventListener('error', done, { once: true })
      image.src = url
    }
  }, [bookId, count, createImage, cv, page, pageCount, shownCount])
}
