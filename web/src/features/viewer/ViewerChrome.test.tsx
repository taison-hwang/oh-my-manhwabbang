import '@testing-library/jest-dom/vitest'

import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import type { ReactNode } from 'react'
import { describe, expect, it, vi } from 'vitest'

import type { BookSummary } from '../../api/types'
import { NextVolumeCard } from './NextVolumeCard'
import { PageSlider } from './PageSlider'
import { ThumbnailCell } from './ThumbnailCell'
import { OVERRIDE_CHIP_LABEL, ViewerTopBar } from './ViewerTopBar'

/**
 * The viewer chrome's **paint**, as distinct from its behaviour.
 *
 * `ViewerPage.test.tsx` drives this chrome end to end against MSW and asserts
 * what it *does*; nothing anywhere asserted what it *is*, and that gap is how
 * the override chip shipped at 2.83:1 through a review and a green suite. Each
 * assertion below is a case where the wrong value is not a different look but an
 * invisible or unreadable control:
 *
 *  - the override chip in outline form is `#EC3013` on `#263B38` — **2.83:1**,
 *    at 11px uppercase, which is worse than the 3.76 E-32 §4 already refused for
 *    this exact chip;
 *  - the current thumbnail and the drag preview marked in `--color-accent` are
 *    a deep teal at ~1.2:1 against the dark strip they sit in, i.e. the reader's
 *    own place in the book is the one cell that cannot be picked out.
 *
 * **The assertions are on class lists and token names, never on resolved
 * colours.** This suite runs with `css: false`, so `getComputedStyle` returns
 * the same empty string for `border-hot` and `border-accent` and a test that
 * compared colours would pass for either. The class list is real DOM state.
 */

function wrap(node: ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  })
  return <QueryClientProvider client={client}>{node}</QueryClientProvider>
}

function renderTopBar(overrides: Partial<Parameters<typeof ViewerTopBar>[0]> = {}) {
  return render(
    wrap(
      <ViewerTopBar
        visible
        seriesName="[만화] 몬스터 1~18"
        bookName="01권.zip"
        mode="single"
        dir="rtl"
        fit="width"
        isOverride={true}
        onBack={vi.fn()}
        onLibrary={vi.fn()}
        onResetPrefs={vi.fn()}
        onModeChange={vi.fn()}
        onDirChange={vi.fn()}
        onFitChange={vi.fn()}
        {...overrides}
      />,
    ),
  )
}

describe('the E-33 override chip is filled, not outlined (E-32 §4 contrast)', () => {
  it('paints --color-hot as its ground and --on-hot as its ink', () => {
    renderTopBar()
    const chip = screen.getByRole('button', { name: OVERRIDE_CHIP_LABEL })

    expect(chip).toHaveClass('bg-hot', 'text-on-hot')

    // The shipped outline form, asserted absent: a transparent ground puts the
    // hot marker on the viewer's #263B38 as *text*, which is the 2.83:1 fail.
    expect(chip.classList.contains('bg-transparent')).toBe(false)
    expect(chip.classList.contains('text-hot')).toBe(false)

    // …and not smuggled back in as an inline style either, which is how it was
    // written the first time (the token had not landed yet).
    expect(chip.style.backgroundColor).toBe('')
    expect(chip.style.color).toBe('')
    expect(chip.style.borderColor).toBe('')
  })

  it('is not rendered at all when the volume has no override', () => {
    renderTopBar({ isOverride: false })
    expect(screen.queryByRole('button', { name: OVERRIDE_CHIP_LABEL })).toBeNull()
  })
})

describe('E-32 §1 — the "current" markers are --color-hot', () => {
  it('marks the current thumbnail, and only the current one', () => {
    const { container, unmount } = render(
      wrap(<ThumbnailCell bookId="b1" page={7} cv={null} current onJump={vi.fn()} />),
    )
    const current = container.querySelector('[data-role="thumb"]')
    expect(current).toHaveClass('border-hot')
    expect(current?.classList.contains('border-accent')).toBe(false)
    unmount()

    const { container: idle } = render(
      wrap(<ThumbnailCell bookId="b1" page={8} cv={null} current={false} onJump={vi.fn()} />),
    )
    const other = idle.querySelector('[data-role="thumb"]')
    expect(other?.classList.contains('border-hot')).toBe(false)
  })

  it('marks the slider drag preview', () => {
    const { container } = render(
      wrap(
        <PageSlider
          bookId="b1"
          cv={null}
          page={110}
          pageCount={362}
          dir="ltr"
          dragging
          dragPage={110}
          onDragStart={vi.fn()}
          onDrag={vi.fn()}
          onCommit={vi.fn()}
        />,
      ),
    )
    const preview = container.querySelector('[data-role="slider-preview"]')
    expect(preview).toHaveClass('border-hot')
    expect(preview?.classList.contains('border-accent')).toBe(false)
  })

  /**
   * The track and the drag preview both follow the reading direction.
   *
   * `dir` on the input is what mirrors the track, and the engine — not jsdom —
   * does the mirroring, so what is asserted here is that the attribute reaches
   * the element. The preview is the half jsdom *can* judge: it is positioned by
   * a `left` percentage this component computes, so an unmirrored preview would
   * sit on the opposite side of the track from the thumb it labels.
   */
  it('mirrors the track and the drag preview in R→L (page 1 at the right)', () => {
    const slider = (dir: 'ltr' | 'rtl') =>
      render(
        wrap(
          <PageSlider
            bookId="b1"
            cv={null}
            page={174}
            pageCount={184}
            dir={dir}
            dragging
            dragPage={174}
            onDragStart={vi.fn()}
            onDrag={vi.fn()}
            onCommit={vi.fn()}
          />,
        ),
      )

    const ltr = slider('ltr')
    const ltrInput = ltr.container.querySelector('input[type="range"]')
    expect(ltrInput).toHaveAttribute('dir', 'ltr')
    const ltrLeft = ltr.container.querySelector<HTMLElement>('[data-role="slider-preview"]')?.style
      .left
    ltr.unmount()

    const rtl = slider('rtl')
    const rtlInput = rtl.container.querySelector('input[type="range"]')
    expect(rtlInput).toHaveAttribute('dir', 'rtl')
    // The value the engine mirrors is untouched: it still names the real page,
    // which is what `aria-valuetext` and the commit handlers report.
    expect(rtlInput).toHaveValue('174')
    const rtlLeft = rtl.container.querySelector<HTMLElement>('[data-role="slider-preview"]')?.style
      .left

    // 174 of 184 is (173/183) = 94.54% along the track, so the mirror is 5.46%.
    // Both are pinned, not just their sum: two wrong percentages can still add
    // to 100, and "near the end" is the half of the claim the reader sees.
    const ltrPct = Number.parseFloat(ltrLeft ?? '')
    const rtlPct = Number.parseFloat(rtlLeft ?? '')
    expect(ltrPct).toBeCloseTo(94.54, 1)
    expect(rtlPct).toBeCloseTo(5.46, 1)
    expect(ltrPct + rtlPct).toBeCloseTo(100, 6)
  })
})

const NEXT_VOLUME: BookSummary = {
  id: 'b2',
  series_id: 's1',
  name: '02권.zip',
  path: '만화/몬스터/02권.zip',
  kind: 'zip',
  ord: 1,
  page_count: 198,
  total_bytes: 84_000_000,
  file_size: 84_000_000,
  mtime: 1_700_000_000,
  cv: 'v1',
  status: 'ok',
  error: null,
  progress: null,
}

describe('the next-volume card’s title (open item `o`, ui-spec §6.5)', () => {
  /**
   * 700, not 800.
   *
   * The Claude Design v2 prototype paints this one line
   * `font-family:var(--font-heading);font-weight:700;font-size:20px;
   * line-height:1.15`, and it is the only 헤딩 in the app that is not extrabold.
   * The previous session recorded the change and could not make it — the file
   * was outside its file list — so the card shipped at 800 against a prototype
   * that says 700. `font-extrabold` is asserted *absent* as well, because
   * Tailwind emits both weights and a class list carrying the two would resolve
   * by source order rather than by intent.
   */
  it('is font-bold, the prototype’s 700, and not font-extrabold', () => {
    const { container } = render(
      <NextVolumeCard
        nextBook={NEXT_VOLUME}
        completed={false}
        appTheme="light"
        onNext={vi.fn()}
        onBackToSeries={vi.fn()}
        onToggleCompleted={vi.fn()}
      />,
    )
    const title = screen.getByText('02권.zip')

    expect(title).toHaveClass('font-heading', 'text-h4', 'font-bold')
    expect(title.classList.contains('font-extrabold')).toBe(false)
    // …and not smuggled back in inline, which is how the weight would most
    // easily come back without touching the class list this test reads.
    expect(title.style.fontWeight).toBe('')

    // The card itself is still there and still the app theme's surface: a
    // `getByText` that matched some other node would otherwise pass quietly.
    expect(container.querySelector('[data-role="next-volume-card"]')).toContainElement(title)
  })
})
