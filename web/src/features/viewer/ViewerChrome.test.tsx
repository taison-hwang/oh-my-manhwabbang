import '@testing-library/jest-dom/vitest'

import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import type { ReactNode } from 'react'
import { describe, expect, it, vi } from 'vitest'

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
})
