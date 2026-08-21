import { TriangleAlert } from 'lucide-react'

import { Button } from '../../components/ds/Button'

/**
 * The failed-page panel (ui-spec §6.4).
 *
 * Scoped to the frame that failed, opaque over the reading ground, and
 * **flush-left** — `align-items: flex-start`, explicitly, because the natural
 * instinct is to centre it and the ui-spec calls that out as wrong. The frame
 * is the full fitted page, so centring would put the badge in the middle of a
 * 1600 px column with nothing near it.
 */
export interface PageErrorProps {
  /** The decoded entry name, e.g. `page_044.jpg`. */
  name: string
  /**
   * The cause, when one is known — the book-level `error` from the index.
   * An `<img>` failure alone reports nothing, and inventing a reason is worse
   * than showing none.
   */
  cause?: string | null
  onRetry: () => void
}

export function PageError({ name, cause, onRetry }: PageErrorProps) {
  const detail = cause === undefined || cause === null || cause === '' ? name : `${name} — ${cause}`
  return (
    // `p-6` is ui-spec §6.4's `padding: 24px`; the first cut wrote `p-4`.
    <div
      role="alert"
      data-role="page-error"
      className="absolute inset-0 flex flex-col items-start justify-center gap-2 bg-bg p-6"
    >
      {/* `text-on-accent`, not `text-ink`. ui-spec §6.4 writes
          `color: var(--color-bg)` against the prototype's manual inversion, and
          inside `data-theme="dark"` the light ink used to be near enough the
          same cream that the two were interchangeable. E-46 ends that: on the
          seal red the dark theme's `--ink` measures **4.33 washed**, an AA
          fail, while `--on-accent` — the token that exists for exactly this
          job — measures 5.62. (No hex here: hygiene.test.ts greps this file
          for colour literals and a value quoted in a comment is still a second
          place the palette is written down.) The component scanner in tokens.test.ts caught both
          call sites; `font-ui` is here because 명조 at .1em of tracking in caps
          comes apart (E-46). */}
      <span className="flex items-center gap-[6px] bg-accent px-2 py-1 font-ui text-3xs uppercase tracking-[.1em] text-on-accent">
        <TriangleAlert size={12} aria-hidden={true} />
        이미지 로드 실패
      </span>
      <span className="text-sm text-ink-faint">{detail}</span>
      {/* Neither a border nor an ink override (E-36 §5.3 / E-42). The retry
          button is a `.btn-secondary`, i.e. a raised **cream** pill in every
          scope, and this panel renders inside the viewer's `data-theme="dark"`:
          there `--color-text` is itself cream, so `text-ink` on the new
          fill is 1.10:1 — the label would be gone. The class's own
          `--on-control` (11.02 washed) is the ink that belongs on it, and the
          border died with `.btn { border: 0 }`. */}
      <Button variant="secondary" className="mt-1 text-sm" onClick={onRetry}>
        다시 시도
      </Button>
    </div>
  )
}
