import '@testing-library/jest-dom/vitest'

import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'

import type { ShellListCounts, ShellRoot } from '../../lib/shellData'
import { MobileDrawer } from './MobileDrawer'
import { ScanIndicator } from './ScanIndicator'
import { Sidebar } from './Sidebar'
import { TopBar } from './TopBar'

const ROOTS: ShellRoot[] = [
  { name: '01. mangga', label: '01. mangga', series_count: 21, available: true, pending: false },
  { name: '02. lanovel', label: '02. lanovel', series_count: 2, available: true, pending: false },
  {
    name: '03. scan (PDF)',
    label: '03. scan (PDF)',
    series_count: 1,
    available: true,
    pending: false,
  },
]

const COUNTS: ShellListCounts = { reading: 10, added: 11, done: 4 }

function renderSidebar(overrides: Partial<Parameters<typeof Sidebar>[0]> = {}) {
  const props = {
    roots: ROOTS,
    counts: COUNTS,
    scope: 'all',
    onScopeChange: vi.fn(),
    scanning: false,
    scanLabel: '스캔 대기 — 8분 전 완료',
    onOpenScanLog: vi.fn(),
    onOpenSettings: vi.fn(),
    onOpenShortcuts: vi.fn(),
    ...overrides,
  }
  return { ...render(<Sidebar {...props} />), props }
}

describe('Sidebar (ui-spec §4.1)', () => {
  it('lists the roots with their series counts, then the three smart lists in order', () => {
    renderSidebar()
    const nav = screen.getByRole('complementary', { name: '라이브러리 탐색' })
    const rows = within(nav).getAllByRole('button')
    // 3 roots + 읽는 중 / 최근 추가 / 완독, then the scan indicator and the
    // two footer buttons.
    expect(rows.slice(0, 6).map((r) => r.textContent)).toEqual([
      '01. mangga21',
      '02. lanovel2',
      '03. scan (PDF)1',
      '읽는 중10',
      '최근 추가11',
      '완독4',
    ])
  })

  it('uses the ui-spec §9 Korean copy verbatim for the smart lists', () => {
    renderSidebar()
    expect(screen.getByText('루트')).toBeInTheDocument()
    expect(screen.getByText('목록')).toBeInTheDocument()
    for (const label of ['읽는 중', '최근 추가', '완독']) {
      expect(screen.getByText(label)).toBeInTheDocument()
    }
  })

  it('marks the active scope with the accent bar and aria-current', () => {
    renderSidebar({ scope: 'reading' })
    const active = screen.getByRole('button', { name: /읽는 중/ })
    expect(active).toHaveAttribute('data-active', 'true')
    expect(active).toHaveAttribute('aria-current', 'page')
    expect(screen.getByRole('button', { name: /완독/ })).toHaveAttribute('data-active', 'false')
  })

  it('reports a scope change, root or smart list', async () => {
    const { props } = renderSidebar()
    await userEvent.click(screen.getByRole('button', { name: /02\. lanovel/ }))
    expect(props.onScopeChange).toHaveBeenCalledWith('02. lanovel')
    await userEvent.click(screen.getByRole('button', { name: /완독/ }))
    expect(props.onScopeChange).toHaveBeenCalledWith('done')
  })

  it('opens settings and the shortcuts dialog from the footer', async () => {
    const { props } = renderSidebar()
    await userEvent.click(screen.getByRole('button', { name: '설정' }))
    expect(props.onOpenSettings).toHaveBeenCalledOnce()
    await userEvent.click(screen.getByRole('button', { name: '키보드 단축키' }))
    expect(props.onOpenShortcuts).toHaveBeenCalledOnce()
  })

  it('collapses to icons in the 56px rail but keeps every row reachable', () => {
    // ui-spec §7, 768–1023: the labels go, the accessible names stay.
    renderSidebar({ variant: 'rail' })
    const nav = screen.getByRole('complementary', { name: '라이브러리 탐색' })
    expect(nav).toHaveClass('sidebar-rail')
    expect(within(nav).getByRole('button', { name: '읽는 중' })).toBeInTheDocument()
    expect(within(nav).getByRole('button', { name: '01. mangga' })).toBeInTheDocument()
    // The visible count text is what disappears, not the row.
    expect(within(nav).queryByText('21')).toBeNull()
  })
})

describe('ScanIndicator (ui-spec §9 #11, FR-IDX-004)', () => {
  it('is idle grey and opens the scan log on click', async () => {
    const onOpenLog = vi.fn()
    const { container } = render(
      <ScanIndicator scanning={false} label="스캔 대기 — 8분 전 완료" onOpenLog={onOpenLog} />,
    )
    expect(container.querySelector('span[aria-hidden]')).toHaveClass('bg-ink-faint')
    await userEvent.click(screen.getByRole('button', { name: '스캔 대기 — 8분 전 완료' }))
    expect(onOpenLog).toHaveBeenCalledOnce()
  })

  it('turns the dot accent while a run is in flight', () => {
    const { container } = render(
      <ScanIndicator scanning label="스캔 중 1,842 / 2,250" onOpenLog={vi.fn()} />,
    )
    expect(container.querySelector('span[aria-hidden]')).toHaveClass('bg-accent')
    expect(screen.getByText('스캔 중 1,842 / 2,250')).toBeInTheDocument()
  })
})

function renderTopBar(overrides: Partial<Parameters<typeof TopBar>[0]> = {}) {
  const props = {
    query: '',
    onQueryChange: vi.fn(),
    scanning: false,
    scanPercent: 0,
    sort: 'name' as const,
    onSortChange: vi.fn(),
    view: 'grid' as const,
    onViewChange: vi.fn(),
    onOpenDrawer: vi.fn(),
    ...overrides,
  }
  return { ...render(<TopBar {...props} />), props }
}

describe('TopBar (ui-spec §4.2)', () => {
  it('uses the catalogue placeholder and offers the palette hint', () => {
    renderTopBar()
    expect(screen.getByPlaceholderText('시리즈 검색 (초성 가능)')).toBeInTheDocument()
    expect(screen.getByText(/⌘K|Ctrl K/)).toBeInTheDocument()
  })

  it('shows the back button only on series detail', () => {
    const { unmount } = renderTopBar()
    expect(screen.queryByRole('button', { name: '라이브러리' })).toBeNull()
    unmount()
    renderTopBar({ showBack: true })
    expect(screen.getByRole('button', { name: '라이브러리' })).toBeInTheDocument()
  })

  it('offers the five sort options with the API keys and the Korean labels', () => {
    renderTopBar()
    const select = screen.getByRole('combobox', { name: '정렬' })
    const options = within(select).getAllByRole('option')
    expect(options.map((o) => o.getAttribute('value'))).toEqual([
      'name',
      'mtime',
      'recent',
      'size',
      'books',
    ])
    expect(options.map((o) => o.textContent)).toEqual([
      '이름',
      '수정일',
      '최근 읽은 순',
      '용량',
      '권 수',
    ])
  })

  it('reports a sort change with the wire key', async () => {
    const { props } = renderTopBar()
    await userEvent.selectOptions(screen.getByRole('combobox', { name: '정렬' }), 'size')
    expect(props.onSortChange).toHaveBeenCalledWith('size')
  })

  it('toggles between 그리드 and 리스트', async () => {
    const { props } = renderTopBar()
    await userEvent.click(screen.getByRole('radio', { name: '리스트' }))
    expect(props.onViewChange).toHaveBeenCalledWith('list')
  })

  it('hides the scan bar when nothing is scanning and shows the percentage when it is', () => {
    const { unmount } = renderTopBar()
    expect(screen.queryByText('63%')).toBeNull()
    unmount()
    renderTopBar({ scanning: true, scanPercent: 63 })
    expect(screen.getByText('63%')).toBeInTheDocument()
  })

  it('offers a drawer trigger, because below 768px there is no sidebar', async () => {
    const { props } = renderTopBar()
    await userEvent.click(screen.getByRole('button', { name: '라이브러리 탐색 열기' }))
    expect(props.onOpenDrawer).toHaveBeenCalledOnce()
  })

  /**
   * The sort group and the view toggle are pinned to the **right** edge of the
   * bar, and they are one box while doing it.
   *
   * jsdom does no layout, so this reads the classes the alignment is made of
   * rather than a measured x. Measured in Chrome at 1440 / 1024 / 768 / 400 /
   * 320: the toggle's right edge is the bar's own 16px padding at every one of
   * them, and `body.scrollWidth === clientWidth` at every one of them.
   *
   * Both halves are asserted because each fails on its own:
   *
   *  - without `ml-auto` the pair trails the search field, which is capped at
   *    400px, across a bar three times that wide — where they had been sitting;
   *  - without the wrapper they are independent items of a *wrapping* bar, so at
   *    768 the sort group ends line 1 flush right and the toggle opens line 2
   *    flush left, 520px away from it.
   */
  it('pins the sort group and the view toggle to the right edge, as one box', () => {
    renderTopBar()
    const sortGroup = screen.getByRole('combobox', { name: '정렬' }).parentElement
    const controls = sortGroup?.parentElement

    expect(controls).toHaveClass('ml-auto', 'flex-wrap', 'justify-end')
    // The toggle is inside the same box, not a sibling of it.
    expect(controls?.querySelector('.seg')).not.toBeNull()
    expect(sortGroup?.nextElementSibling).toHaveClass('seg')
  })
})

describe('MobileDrawer (ui-spec §7, D-42)', () => {
  it('renders nothing until it is opened', () => {
    render(
      <MobileDrawer open={false} onClose={vi.fn()}>
        <p>sidebar</p>
      </MobileDrawer>,
    )
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('is a labelled modal that closes on Esc, the backdrop and the close button', async () => {
    const onClose = vi.fn()
    const { container } = render(
      <MobileDrawer open onClose={onClose}>
        <p>sidebar</p>
      </MobileDrawer>,
    )
    const dialog = screen.getByRole('dialog', { name: '라이브러리 탐색' })
    expect(dialog).toHaveAttribute('aria-modal', 'true')

    await userEvent.keyboard('{Escape}')
    expect(onClose).toHaveBeenCalledTimes(1)

    await userEvent.click(screen.getByRole('button', { name: '닫기' }))
    expect(onClose).toHaveBeenCalledTimes(2)

    const backdrop = document.querySelector('.drawer-backdrop')
    expect(backdrop).not.toBeNull()
    if (backdrop !== null) await userEvent.click(backdrop)
    expect(onClose).toHaveBeenCalledTimes(3)
    expect(container).toBeDefined()
  })
})
