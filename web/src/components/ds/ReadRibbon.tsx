import { cn } from '../../lib/cn'

/**
 * The reading ribbon — a 갈피 hanging out of the top of a cover, as long as the
 * part of the book that has been read (E-46, prototype `만화방 v3 서고`).
 *
 * It replaces the 5px rail `ProgressBar` used to lay across the bottom of a grid
 * cover. The rail said the same number; the ribbon says it the way a book says
 * it — the reader's own bookmark, further down the block the further in they
 * are — and it is the mark the 서고 prototype draws on every started series.
 *
 * ## It is still a progress bar to anything that is not looking at it
 *
 * `role="progressbar"` with the same `aria-valuenow` the rail carried, because
 * the *information* did not become decorative when the shape changed: a screen
 * reader on a grid card gets "34%" here exactly as it did from the rail, and
 * `library.test.tsx` reads this element for it.
 *
 * ## Two numbers, and only one of them is the progress
 *
 * `aria-valuenow` is the true rounded percentage. The **drawn** length has a
 * 2 % floor, because the ribbon is a tail with a 5px swallowtail notch under it:
 * at 0.4 % of a 250px cover the strip is one pixel and all that renders is a
 * notch floating over the paper, which reads as a rendering fault rather than as
 * "barely started". The floor is 5px of ribbon on that cover — under the notch
 * it hangs from, and nowhere near enough to be mistaken for progress that is not
 * there.
 *
 * ## Why the fill is `--accent-fill`
 *
 * The same reason `ProgressBar` gives: `--color-accent` is 1.28 against the dark
 * theme's surface, so a ribbon painted in the base accent is invisible on
 * exactly the theme where a cover is darkest. `--accent-fill` is the accent when
 * it has to read against the ground it is on.
 *
 * The cover it hangs from must be `position: relative` and must **not** clip its
 * overflow — the 4px above the top edge is the point of it. `SeriesCard` puts
 * the clip on the inner mat instead, which is where the cover art needs it.
 */
export interface ReadRibbonProps {
  /** 0..1. Clamped; non-finite is normalised to 0, as in `ProgressBar`. */
  value: number
  /** Accessible name, e.g. the series title. */
  label?: string
  className?: string
}

/** The drawn length never goes below this, whatever `value` says. See above. */
const MIN_DRAWN_PERCENT = 2

export function ReadRibbon({ value, label, className }: ReadRibbonProps) {
  const ratio = Number.isFinite(value) ? Math.max(0, Math.min(1, value)) : 0
  const pct = Math.round(ratio * 100)
  const drawn = Math.max(pct, MIN_DRAWN_PERCENT)
  return (
    <span
      role="progressbar"
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={pct}
      aria-label={label}
      className={cn(
        'pointer-events-none absolute -top-1 right-[19px] z-[2] block w-[7px] bg-accent-fill shadow-sm',
        className,
      )}
      style={{ height: `${drawn.toString()}%` }}
    >
      {/* The swallowtail: a zero-sized box whose left and right borders are the
          ribbon's own colour and whose bottom border is transparent, i.e. two
          triangles with a V cut out between them. */}
      <span
        aria-hidden="true"
        className="absolute -bottom-[5px] left-0 border-x-[3.5px] border-b-[5px] border-x-accent-fill border-b-transparent"
      />
    </span>
  )
}
