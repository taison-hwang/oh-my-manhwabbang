import '@testing-library/jest-dom/vitest'

import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'

import { DoneSeal } from './DoneSeal'
import { PageStamp } from './PageStamp'
import { ReadRibbon } from './ReadRibbon'

/**
 * The three marks E-46 stamps on a book — the 완독 seal, the read-position
 * stamp and the reading ribbon.
 *
 * They live in their own file rather than in `ds.test.tsx` for one practical
 * reason: they arrived on a branch that is parallel to the one carrying the rest
 * of the 서고 skin, and a new file merges where an edit in the middle of a
 * 500-line suite does not.
 *
 * What is asserted is what each mark **means**, never how it looks: that the
 * ribbon still reports a percentage to anything that cannot see it, that the
 * seal's name is the Korean word and not the hanja it draws, that the stamp
 * hands its numerals to the sans. The geometry (the tilt, the swallowtail, the
 * 7px width) is the design's to move.
 */

describe('DoneSeal (E-46)', () => {
  it('draws 完讀 but announces 완독', () => {
    render(<DoneSeal />)

    // The catalogue word is what a screen reader gets…
    expect(screen.getByText('완독')).toBeInTheDocument()
    // …and the Han glyphs are hidden from it, so the mark is never announced as
    // two characters most Korean readers do not use.
    expect(screen.getByText('完讀')).toHaveAttribute('aria-hidden', 'true')
  })

  it('takes the vendored seal face and the accent that is ink, not the fill', () => {
    const { container } = render(<DoneSeal />)
    const seal = container.firstElementChild

    // `font-seal` is the family `fonts.css` restricts the two vendored Han
    // glyphs to; without it 完讀 is whatever the machine has, or two tofu boxes.
    expect(seal).toHaveClass('font-seal')
    // `--accent-text`, not `--color-accent`: 12px type, and the base accent is
    // 4.33 on this ground where accent-700 is 7.41 (tokens.css).
    expect(seal).toHaveClass('text-accent-text', 'border-accent-text')
  })

  it('never eats a click meant for the cover under it', () => {
    const { container } = render(<DoneSeal />)
    expect(container.firstElementChild).toHaveClass('pointer-events-none')
  })
})

describe('PageStamp (E-46)', () => {
  it('renders its counter in the sans, with tabular figures', () => {
    render(<PageStamp>10 / 214p</PageStamp>)
    const stamp = screen.getByText('10 / 214p')

    // 명조 numerals are proportional, so a column of these does not line up and
    // the digits shuffle as the reader turns pages (tokens.css, `--font-ui`).
    expect(stamp).toHaveClass('font-ui', 'tabular-nums')
  })
})

describe('ReadRibbon (E-46)', () => {
  it('is a progress bar to anything that is not looking at it', () => {
    render(<ReadRibbon value={0.34} label="몬스터" />)
    const ribbon = screen.getByRole('progressbar')

    expect(ribbon).toHaveAttribute('aria-valuenow', '34')
    expect(ribbon).toHaveAttribute('aria-valuemin', '0')
    expect(ribbon).toHaveAttribute('aria-valuemax', '100')
    expect(ribbon).toHaveAttribute('aria-label', '몬스터')
    expect(ribbon.style.height).toBe('34%')
  })

  it('hangs as long as the part that has been read', () => {
    const { unmount } = render(<ReadRibbon value={0.9} />)
    expect(screen.getByRole('progressbar').style.height).toBe('90%')
    unmount()

    render(<ReadRibbon value={0.5} />)
    expect(screen.getByRole('progressbar').style.height).toBe('50%')
  })

  /**
   * The one place the drawn length and the reported value part company, and it
   * is deliberate: a 0.4 % ribbon on a 250px cover is one pixel with a 5px notch
   * hanging off it, which reads as a rendering fault. The **reported** number
   * stays exact — the floor is on the paint, not on the fact.
   */
  it('draws a 2 % minimum without rounding the reported value up to it', () => {
    render(<ReadRibbon value={0.004} />)
    const ribbon = screen.getByRole('progressbar')

    expect(ribbon).toHaveAttribute('aria-valuenow', '0')
    expect(ribbon.style.height).toBe('2%')
  })

  /**
   * `ProgressBar`'s contract, kept here so the two agree: callers divide a page
   * by a page count and a broken volume reports `page_count: 0` (arch §4.11), so
   * `Infinity` and `NaN` arrive by the ordinary route.
   */
  it('normalises a non-finite value to an empty ribbon rather than a full one', () => {
    const { unmount } = render(<ReadRibbon value={Number.POSITIVE_INFINITY} />)
    expect(screen.getByRole('progressbar')).toHaveAttribute('aria-valuenow', '0')
    unmount()

    render(<ReadRibbon value={Number.NaN} />)
    expect(screen.getByRole('progressbar')).toHaveAttribute('aria-valuenow', '0')
  })

  it('clamps past the ends', () => {
    const { unmount } = render(<ReadRibbon value={2} />)
    expect(screen.getByRole('progressbar')).toHaveAttribute('aria-valuenow', '100')
    unmount()

    render(<ReadRibbon value={-1} />)
    expect(screen.getByRole('progressbar')).toHaveAttribute('aria-valuenow', '0')
  })

  it('is drawn in the accent that reads against the ground it hangs on', () => {
    render(<ReadRibbon value={0.34} />)
    // `--color-accent` is 1.28 against the dark theme's surface: a ribbon
    // painted in it is invisible on exactly the theme where a cover is darkest.
    expect(screen.getByRole('progressbar')).toHaveClass('bg-accent-fill')
  })
})
