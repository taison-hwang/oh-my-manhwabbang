import { useState, type CSSProperties, type SyntheticEvent } from 'react'

import { pageUrl } from '../../api/urls'
import type { DisplayMode, FitMode } from '../../store/viewer'
import { pageFitStyle, pageFrameStyle } from './fit'
import { PageError } from './PageError'

/**
 * One page on the stage.
 *
 * **The stage never blanks** (design.md, ui-spec §6.3). A page turn replaces
 * `src`, and a browser is not obliged to keep the old frame painted while the
 * new one decodes — so the previous page is kept in the DOM explicitly, in
 * flow, and the incoming image loads on top of it at `opacity: 0` until its
 * `load` event fires. That is the whole trick: no white flash, no layout jump,
 * and the swap is a single re-render.
 *
 * That only works because the caller keys the frame by its **slot**, not by the
 * page number: a `key={page}` unmounts this component on every turn, taking the
 * decoded previous page with it and blanking the stage for the whole load.
 *
 * `src` is built by `urls.ts` and carries `?v={cv}`, which is what makes it
 * byte-identical to the URL `usePrefetch` warmed — the two must never drift or
 * the prefetch silently stops working (FR-SRV-007).
 */
export interface PageFrameProps {
  bookId: string
  /** 1-based. */
  page: number
  /** The book's `cv`. */
  cv: string | null
  /** Decoded entry name — the `alt` text and the failure panel's detail line. */
  name: string
  fit: FitMode
  /** Which stage the frame is on — the frame's box is sized differently in 세로. */
  mode: DisplayMode
  /** Book-level error text, shown as the failure cause when the index has one. */
  cause?: string | null
  /** Fired once per successful decode, with the natural size (FR-VWR-004). */
  onLoaded?: (page: number, width: number, height: number) => void
  /** Fired when the image fails, so the caller can stop the loading indicator. */
  onFailed?: (page: number) => void
  className?: string
}

interface FrameState {
  src: string
  status: 'loading' | 'ready' | 'error'
  /** The last `src` that decoded — held on screen while the next one loads. */
  shown: string | null
}

/**
 * `_r` busts the cache for `다시 시도` only. Unknown query params are ignored by
 * the server (impl-plan §4 rule 5), and leaving it off attempt 0 keeps the
 * normal URL identical to the prefetched one.
 */
function srcFor(bookId: string, page: number, cv: string | null, attempt: number): string {
  const base = pageUrl(bookId, page, { v: cv })
  if (attempt === 0) return base
  return `${base}${base.includes('?') ? '&' : '?'}_r=${String(attempt)}`
}

export function PageFrame({
  bookId,
  page,
  cv,
  name,
  fit,
  mode,
  cause,
  onLoaded,
  onFailed,
  className,
}: PageFrameProps) {
  // The retry counter is scoped to the page it was pressed on. The frame now
  // outlives a page turn, and carrying `_r=2` onto the next page would make its
  // URL differ from the one `usePrefetch` warmed — a silent cache miss.
  const [retry, setRetry] = useState<{ page: number; attempt: number }>({ page, attempt: 0 })
  const attempt = retry.page === page ? retry.attempt : 0
  const src = srcFor(bookId, page, cv, attempt)
  const [state, setState] = useState<FrameState>({ src, status: 'loading', shown: null })

  // Deriving state during render rather than in an effect: an effect would
  // paint one frame with the *new* src already visible and undecoded, which is
  // exactly the flash this component exists to prevent.
  if (state.src !== src) {
    setState((previous) => ({
      src,
      status: 'loading',
      shown: previous.status === 'ready' ? previous.src : previous.shown,
    }))
  }

  const fitStyle = pageFitStyle(fit)
  const ready = state.status === 'ready'
  const failed = state.status === 'error'
  // With nothing decoded yet there is no fitted page to scope the failure panel
  // to, so the frame takes the stage instead of collapsing to zero.
  const empty = failed && state.shown === null
  const frameStyle: CSSProperties = {
    ...pageFrameStyle(fit, mode),
    ...(empty ? { flex: '1 1 auto', width: '100%', height: '100%' } : {}),
  }
  const hiddenStyle: CSSProperties = {
    ...fitStyle,
    position: 'absolute',
    inset: 0,
    opacity: 0,
    pointerEvents: 'none',
  }

  const handleLoad = (event: SyntheticEvent<HTMLImageElement>): void => {
    const image = event.currentTarget
    setState({ src, status: 'ready', shown: src })
    onLoaded?.(page, image.naturalWidth, image.naturalHeight)
  }

  const handleError = (): void => {
    setState((previous) => ({ ...previous, status: 'error' }))
    onFailed?.(page)
  }

  return (
    <div
      data-role="page-frame"
      data-page={page}
      data-status={state.status}
      className={className}
      style={frameStyle}
    >
      {!ready && state.shown !== null && (
        <img
          data-role="previous-page"
          src={state.shown}
          alt=""
          aria-hidden="true"
          draggable={false}
          style={fitStyle}
        />
      )}
      <img
        key={state.src}
        data-role="page"
        data-page={page}
        src={state.src}
        alt={name}
        draggable={false}
        style={ready ? fitStyle : hiddenStyle}
        onLoad={handleLoad}
        onError={handleError}
      />
      {failed && (
        <PageError
          name={name}
          cause={cause ?? null}
          onRetry={() => {
            setRetry({ page, attempt: attempt + 1 })
          }}
        />
      )}
    </div>
  )
}
