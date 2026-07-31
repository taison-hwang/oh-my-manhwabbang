import '@testing-library/jest-dom/vitest'

import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { describe, expect, it, vi } from 'vitest'

import { formatLabel } from '../../lib/format'
import { Button } from './Button'
import { Dialog } from './Dialog'
import { EmptyState } from './EmptyState'
import { FallbackCover } from './FallbackCover'
import { FormatBadge } from './FormatBadge'
import { ProgressBar } from './ProgressBar'
import { Radio } from './Radio'
import { Seg } from './Seg'
import { Skeleton } from './Skeleton'
import { Tag } from './Tag'
import { BRAND_NAME, BRAND_TAGLINE, Wordmark } from './Wordmark'

describe('Button (ui-spec §2.3)', () => {
  it('is a real button and defaults to type="button"', () => {
    render(<Button>설정</Button>)
    const btn = screen.getByRole('button', { name: '설정' })
    expect(btn).toHaveAttribute('type', 'button')
    expect(btn).toHaveClass('btn')
  })

  it('carries .btn-block, which is the flush-left rule (ui-spec §0.3)', () => {
    // The label of a full-width button starts at the left padding edge — see
    // the two stacked buttons in library-grid-card-hover-1440.png.
    render(
      <Button variant="primary" block>
        읽기 시작
      </Button>,
    )
    expect(screen.getByRole('button', { name: '읽기 시작' })).toHaveClass('btn-block')
  })

  it('maps every variant onto its DS class', () => {
    render(
      <>
        <Button variant="primary">a</Button>
        <Button variant="secondary">b</Button>
        <Button variant="ghost">c</Button>
        <Button icon>d</Button>
      </>,
    )
    expect(screen.getByRole('button', { name: 'a' })).toHaveClass('btn-primary')
    expect(screen.getByRole('button', { name: 'b' })).toHaveClass('btn-secondary')
    expect(screen.getByRole('button', { name: 'c' })).toHaveClass('btn-ghost')
    expect(screen.getByRole('button', { name: 'd' })).toHaveClass('btn-icon')
  })

  it('does not fire when disabled', async () => {
    const onClick = vi.fn()
    render(
      <Button disabled onClick={onClick}>
        재스캔
      </Button>,
    )
    await userEvent.click(screen.getByRole('button', { name: '재스캔' }))
    expect(onClick).not.toHaveBeenCalled()
  })
})

describe('Tag / FormatBadge (ui-spec §4.5, FR-LIB-009)', () => {
  it('tones the format tag: ZIP neutral, FOLDER accent, PDF outline', () => {
    render(
      <>
        <FormatBadge format="zip" variant="tag" />
        <FormatBadge format="folder" variant="tag" />
        <FormatBadge format="pdf" variant="tag" />
      </>,
    )
    expect(screen.getByText('ZIP')).toHaveClass('tag-neutral')
    expect(screen.getByText('FOLDER')).toHaveClass('tag-accent')
    expect(screen.getByText('PDF')).toHaveClass('tag-outline')
  })

  it('prints FOLDER for both wire spellings (C-4: series `folder`, book `dir`)', () => {
    expect(formatLabel('folder')).toBe('FOLDER')
    expect(formatLabel('dir')).toBe('FOLDER')
    expect(formatLabel('zip')).toBe('ZIP')
    expect(formatLabel('pdf')).toBe('PDF')
  })

  it('renders the corner variant as an ink field pinned top-left', () => {
    render(<FormatBadge format="zip" variant="corner" />)
    const badge = screen.getByText('ZIP')
    expect(badge.className).toContain('absolute')
    expect(badge.className).toContain('left-0')
    expect(badge.className).toContain('top-0')
    expect(badge).not.toHaveClass('tag')
  })

  it('keeps the four DS tag tones available', () => {
    render(<Tag tone="accent-2">x</Tag>)
    expect(screen.getByText('x')).toHaveClass('tag', 'tag-accent-2')
  })
})

describe('Seg (ui-spec §2.3)', () => {
  function Harness() {
    const [value, setValue] = useState<'grid' | 'list'>('grid')
    return (
      <Seg
        value={value}
        onChange={setValue}
        aria-label="보기 방식"
        options={[
          { value: 'grid', label: '그리드' },
          { value: 'list', label: '리스트' },
        ]}
      />
    )
  }

  it('is a radiogroup of real radios, so it is one tab stop', () => {
    render(<Harness />)
    const group = screen.getByRole('radiogroup', { name: '보기 방식' })
    expect(within(group).getAllByRole('radio')).toHaveLength(2)
    expect(screen.getByRole('radio', { name: '그리드' })).toBeChecked()
  })

  it('marks the checked option so it renders as an accent field', () => {
    render(<Harness />)
    const checked = screen.getByRole('radio', { name: '그리드' }).closest('label')
    expect(checked).toHaveAttribute('data-checked', 'true')
    const other = screen.getByRole('radio', { name: '리스트' }).closest('label')
    expect(other).toHaveAttribute('data-checked', 'false')
  })

  it('reports the new value on selection', async () => {
    render(<Harness />)
    await userEvent.click(screen.getByRole('radio', { name: '리스트' }))
    expect(screen.getByRole('radio', { name: '리스트' })).toBeChecked()
  })
})

describe('Radio', () => {
  it('exposes the dot, one of the only two circles in the product', () => {
    const { container } = render(<Radio label="라이트" checked readOnly />)
    expect(container.querySelector('.dot')).not.toBeNull()
    expect(container.querySelector('.radio')).toHaveAttribute('data-checked', 'true')
  })
})

describe('ProgressBar (ui-spec §9 #5)', () => {
  it('reports its value to assistive tech and paints the fill width', () => {
    const { container } = render(<ProgressBar value={0.34} label="몬스터" />)
    const bar = screen.getByRole('progressbar', { name: '몬스터' })
    expect(bar).toHaveAttribute('aria-valuenow', '34')
    expect(container.querySelector<HTMLElement>('[role=progressbar] > div')?.style.width).toBe('34%')
  })

  it('turns the fill to ink at 100 %, so 완독 reads as finished', () => {
    const { container } = render(<ProgressBar value={1} tone="done" />)
    expect(container.querySelector('[role=progressbar] > div')).toHaveClass('bg-ink')
  })

  it('uses the heavier trough when it sits on top of a cover', () => {
    render(<ProgressBar value={0.5} height={4} track="over-art" label="x" />)
    const bar = screen.getByRole('progressbar', { name: 'x' })
    expect(bar).toHaveClass('bg-fill-track-2', 'h-[4px]')
  })

  it('clamps out-of-range values instead of overflowing the trough', () => {
    const { container } = render(<ProgressBar value={2} />)
    expect(container.querySelector<HTMLElement>('[role=progressbar] > div')?.style.width).toBe(
      '100%',
    )
  })
})

describe('FallbackCover (FR-LIB-008)', () => {
  it('renders the format kicker and the title over the stripe field', () => {
    render(<FallbackCover title="[만화] 배가본드 1~37" format="folder" size="card" />)
    expect(screen.getByText('FOLDER · NO THUMBNAIL')).toBeInTheDocument()
    expect(screen.getByText('[만화] 배가본드 1~37')).toBeInTheDocument()
  })

  it('is absolutely positioned so a late cover cannot shift the layout', () => {
    const { container } = render(<FallbackCover title="t" format="zip" size="card" />)
    const root = container.firstElementChild
    expect(root?.className).toContain('absolute')
    expect(root?.className).toContain('inset-0')
  })

  it('drops the text and tightens the stripe pitch on a 24×36 list thumb', () => {
    const { container } = render(<FallbackCover title="t" format="zip" size="row" />)
    expect(screen.queryByText('ZIP · NO THUMBNAIL')).toBeNull()
    expect(container.firstElementChild).toHaveClass('fallback-cover-row')
  })
})

describe('Skeleton (ui-spec §4.5)', () => {
  it('staggers by (i % 6) * 0.12s so the grid does not pulse in lockstep', () => {
    const { container } = render(
      <>
        {[0, 1, 5, 6, 7].map((i) => (
          <Skeleton key={i} variant="cover" index={i} />
        ))}
      </>,
    )
    const delays = [...container.querySelectorAll<HTMLElement>('div')].map(
      (el) => el.style.animationDelay,
    )
    expect(delays).toEqual(['0.00s', '0.12s', '0.60s', '0.00s', '0.12s'])
  })

  it('holds the 2:3 box so the skeleton has zero layout shift', () => {
    const { container } = render(<Skeleton variant="cover" />)
    expect(container.firstElementChild).toHaveClass('aspect-[2/3]')
  })
})

describe('EmptyState (ui-spec §4.5, §9 catalogue)', () => {
  it('renders the no-results band flush left between two 2px rules', () => {
    const onClick = vi.fn()
    render(
      <EmptyState
        title="검색 결과 없음"
        body="초성 검색도 지원합니다. 다른 표기를 시도해 보세요."
        action={{ label: '검색 지우기', onClick }}
      />,
    )
    expect(screen.getByRole('heading', { name: '검색 결과 없음' })).toBeInTheDocument()
    expect(
      screen.getByText('초성 검색도 지원합니다. 다른 표기를 시도해 보세요.'),
    ).toBeInTheDocument()
    const root = screen.getByRole('heading', { name: '검색 결과 없음' }).parentElement
    expect(root).toHaveClass('items-start', 'border-y-2', 'border-rule-strong')
  })

  it('scales to the 42px onboarding heading in the hero variant', () => {
    render(<EmptyState variant="hero" title="읽을 폴더를 등록하세요" />)
    expect(screen.getByRole('heading', { level: 1, name: '읽을 폴더를 등록하세요' })).toBeVisible()
  })
})

describe('Dialog (impl-plan WP-10 acceptance 9)', () => {
  function Harness() {
    const [open, setOpen] = useState(true)
    return (
      <>
        <button
          type="button"
          onClick={() => {
            setOpen(true)
          }}
        >
          opener
        </button>
        <Dialog
          open={open}
          onClose={() => {
            setOpen(false)
          }}
          title="키보드 단축키"
          width="min(560px, 100%)"
        >
          <button type="button">first</button>
          <button type="button">last</button>
        </Dialog>
      </>
    )
  }

  it('is an aria-modal dialog labelled by its title', () => {
    render(<Harness />)
    const dialog = screen.getByRole('dialog', { name: '키보드 단축키' })
    expect(dialog).toHaveAttribute('aria-modal', 'true')
    expect(dialog).toHaveClass('dialog')
  })

  it('moves focus into the dialog and traps Tab inside it', async () => {
    render(<Harness />)
    expect(screen.getByRole('button', { name: 'first' })).toHaveFocus()
    await userEvent.tab()
    expect(screen.getByRole('button', { name: 'last' })).toHaveFocus()
    await userEvent.tab()
    expect(screen.getByRole('button', { name: 'first' })).toHaveFocus()
    await userEvent.tab({ shift: true })
    expect(screen.getByRole('button', { name: 'last' })).toHaveFocus()
  })

  it('closes on Esc and does not let the keystroke reach the global ladder', async () => {
    const onGlobalEsc = vi.fn()
    window.addEventListener('keydown', onGlobalEsc)
    render(<Harness />)
    await userEvent.keyboard('{Escape}')
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(onGlobalEsc).not.toHaveBeenCalled()
    window.removeEventListener('keydown', onGlobalEsc)
  })

  it('renders nothing at all when closed', () => {
    render(
      <Dialog open={false} onClose={() => undefined} title="설정">
        body
      </Dialog>,
    )
    expect(screen.queryByRole('dialog')).toBeNull()
  })
})

describe('Wordmark', () => {
  it('reads the brand name in every variant, including the rail where it is invisible', () => {
    // The rail is the case worth pinning: the name is *only* in the accessible
    // layer there, so a regression that drops it leaves a landmark with no
    // name and nothing on screen changes.
    for (const variant of ['hero', 'compact', 'mark'] as const) {
      const { unmount } = render(<Wordmark variant={variant} />)
      expect(screen.getByText(BRAND_NAME)).toBeInTheDocument()
      unmount()
    }
  })

  it('shows the descriptor beside the name, and hides it in the rail', () => {
    const { unmount } = render(<Wordmark variant="compact" />)
    expect(screen.getByText(BRAND_TAGLINE)).toBeInTheDocument()
    unmount()

    render(<Wordmark variant="mark" />)
    expect(screen.queryByText(BRAND_TAGLINE)).toBeNull()
  })

  it('keeps the bars out of the accessible tree — they are a picture, not the name', () => {
    const { container } = render(<Wordmark variant="hero" />)
    const mark = container.querySelector('[aria-hidden="true"]')
    expect(mark).not.toBeNull()
    // Five bars, and exactly one of them is the accent field (ui-spec §2.5).
    expect(mark?.children).toHaveLength(5)
    expect(mark?.querySelectorAll('.bg-accent')).toHaveLength(1)
  })
})
