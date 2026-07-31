import '@testing-library/jest-dom/vitest'

import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { afterEach, describe, expect, it } from 'vitest'

import { ShortcutsDialog } from './ShortcutsDialog'

/**
 * ui-spec §8.5 entry-for-entry (WP-10 acceptance 8) plus the dialog contract of
 * acceptance 9: `aria-modal`, a focus trap, `Esc`, and focus restored to the
 * opener.
 */

const ORIGINAL_UA = navigator.userAgent

function stubUserAgent(value: string): void {
  Object.defineProperty(navigator, 'userAgent', {
    configurable: true,
    get: () => value,
  })
}

afterEach(() => {
  stubUserAgent(ORIGINAL_UA)
})

/** A trigger plus the dialog, so focus restoration has somewhere to go back to. */
function Harness() {
  const [open, setOpen] = useState(false)
  return (
    <>
      <button
        type="button"
        onClick={() => {
          setOpen(true)
        }}
      >
        열기
      </button>
      <ShortcutsDialog
        open={open}
        onClose={() => {
          setOpen(false)
        }}
      />
    </>
  )
}

describe('ShortcutsDialog (ui-spec §8.5)', () => {
  it('lists every entry of §8.5, in order', () => {
    render(<ShortcutsDialog open onClose={() => undefined} />)
    const labels = [
      '이전 / 다음 페이지',
      '다음 페이지',
      '썸네일',
      '전체화면',
      '뷰어 나가기',
      '커맨드 팔레트',
      '단면 / 양면 / 세로',
      // E-27's three: the chrome is no longer summoned by the mouse moving, so
      // the sheet has to say what does summon it.
      '컨트롤 표시 / 숨기기',
      '이전 / 다음 페이지',
      '컨트롤 토글',
      '키보드 단축키',
    ]
    const rendered = within(screen.getByTestId('shortcut-list'))
      .getAllByText(new RegExp(`^(${labels.join('|')})$`))
      .map((el) => el.textContent)
    expect(rendered).toEqual(labels)

    for (const key of [
      '← →',
      'Space',
      'T',
      'F',
      'Esc',
      '1 2 3',
      'H',
      '좌 / 우 클릭',
      '가운데 클릭',
      '?',
    ]) {
      expect(screen.getByText(key)).toBeInTheDocument()
    }
  })

  it('is titled 키보드 단축키 with the 뷰어 kicker', () => {
    render(<ShortcutsDialog open onClose={() => undefined} />)
    const dialog = screen.getByRole('dialog')
    expect(dialog).toHaveAttribute('aria-modal', 'true')
    expect(dialog).toHaveAccessibleName(expect.stringContaining('키보드 단축키') as unknown as string)
    expect(screen.getByText('뷰어')).toBeInTheDocument()
  })

  it('prints ⌘K on Apple keyboards and Ctrl K everywhere else', () => {
    stubUserAgent('Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)')
    const apple = render(<ShortcutsDialog open onClose={() => undefined} />)
    expect(screen.getByText('⌘K')).toBeInTheDocument()
    apple.unmount()

    stubUserAgent('Mozilla/5.0 (X11; Linux x86_64)')
    render(<ShortcutsDialog open onClose={() => undefined} />)
    expect(screen.getByText('Ctrl K')).toBeInTheDocument()
  })

  it('traps focus, closes on Esc and restores focus to the opener', async () => {
    render(<Harness />)
    const trigger = screen.getByRole('button', { name: '열기' })
    await userEvent.click(trigger)

    const dialog = screen.getByRole('dialog')
    // Nothing inside is focusable, so the panel itself takes focus rather than
    // leaving it behind on the opener underneath the scrim.
    expect(dialog).toHaveFocus()

    // Tab cannot leave the dialog.
    await userEvent.tab()
    expect(dialog).toHaveFocus()

    await userEvent.keyboard('{Escape}')
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(trigger).toHaveFocus()
  })
})
