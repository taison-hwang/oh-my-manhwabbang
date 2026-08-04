import { usePageThumbImage } from '../../api/queries'
import { thumbWidthFor } from '../../api/urls'
import { cn } from '../../lib/cn'

/**
 * One 48×72 thumbnail in the strip (ui-spec §6.7).
 *
 * `202 queued` and `422 thumb_unavailable` are **normal** for a page thumbnail
 * — they are generated lazily (FR-THM-004) — so neither may break the row.
 * Both render the empty bordered box with its page number, which is exactly
 * what the striped placeholder looks like before the image lands. The status
 * is exposed on `data-thumb-status` so the queued path is assertable.
 *
 * Width is 120 (impl-plan §0.4: 48 CSS px at 2× DPR snaps up to 120). The
 * server snaps **up**, so sending anything else silently doubles bandwidth.
 *
 * The page number sits **inside** the tile, bottom-left, over the thumbnail —
 * ui-spec §6.7's `align-items:flex-end; justify-content:flex-start; padding:3px`
 * — and carries a `--scrim-cover` chip. The reference capture puts the number
 * over the striped placeholder, where the spec's `--color-neutral-600` is
 * perfectly legible; over a *real* manga page, which is white paper more often
 * than not, the same grey vanished. The scrim is the existing cover-caption
 * token, so the number reads at any width against any page without inventing a
 * colour or moving the number out of the tile.
 */
export interface ThumbnailCellProps {
  bookId: string
  /** 1-based. */
  page: number
  cv: string | null
  current: boolean
  onJump: (page: number) => void
}

export function ThumbnailCell({ bookId, page, cv, current, onJump }: ThumbnailCellProps) {
  const image = usePageThumbImage(bookId, page, { w: thumbWidthFor('viewerStrip'), v: cv })

  return (
    <button
      type="button"
      data-role="thumb"
      data-page={page}
      data-current={current ? 'true' : 'false'}
      data-thumb-status={image.status}
      aria-current={current ? 'true' : undefined}
      onClick={() => {
        onJump(page)
      }}
      className={cn(
        // 56×84 on a phone, 48×72 from `md` up: the strip is a touch target
        // there, and 48px is under the 44px minimum once the 2px border and the
        // gap are taken off.
        'relative flex h-[84px] w-[56px] shrink-0 items-end justify-start overflow-hidden rounded-sm border-2 p-[3px] text-3xs tabular-nums md:h-[72px] md:w-12',
        // E-32 §1: the **current** thumbnail is one of the seven things the
        // retired brand red still marks. It used to be `--color-accent`, which
        // is a deep teal now and 1.2:1 against this strip's own ground — the
        // reader's place in the book would have been the one cell you cannot
        // pick out.
        current ? 'border-hot text-ink' : 'border-neutral-800 text-neutral-600',
      )}
    >
      {image.status === 'ready' && (
        <img
          src={image.url}
          alt=""
          aria-hidden="true"
          draggable={false}
          className="absolute inset-0 h-full w-full object-cover"
        />
      )}
      <span className="relative bg-scrim-cover px-[3px] py-px">{page}</span>
    </button>
  )
}
