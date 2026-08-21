import '@testing-library/jest-dom/vitest'

import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { describe, expect, it, vi } from 'vitest'

import { BOOK_KINDS } from '../../api/types'
import { formatLabel } from '../../lib/format'
import { stripComments } from '../../styles/cssRules'
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

  it('a nested volume wears its twin’s badge, and no kind keeps the prefix (D-70, D-71, D-73)', () => {
    expect(formatLabel('nestedzip')).toBe('ZIP')
    expect(formatLabel('rar')).toBe('RAR')
    expect(formatLabel('nestedrar')).toBe('RAR')
    // D-73's chapter directory. Its twin is `dir`, so the folder rule has to be
    // reached *after* the prefix comes off — the other order says DIR.
    expect(formatLabel('nesteddir')).toBe('FOLDER')

    // Driven by BOOK_KINDS rather than a hand-written list, because the defect
    // this closes was a kind the client had never been told about: the server
    // sent `nestedrar` for 8 volumes and the badge read NESTEDRAR. A kind added
    // to the enum without a badge rule fails here instead of on screen.
    for (const kind of BOOK_KINDS) {
      expect(formatLabel(kind), kind).not.toMatch(/nested/i)
    }
  })

  it('renders the corner variant as a pill inset from the top-left (E-32)', () => {
    render(<FormatBadge format="zip" variant="corner" />)
    const badge = screen.getByText('ZIP')
    expect(badge.className).toContain('absolute')
    // Inset 8px, not flush into the 0,0 corner, and rounded — the two halves of
    // E-32's badge change. `left-0`/`top-0` is the shipped state this replaces,
    // so it is asserted absent rather than merely not asserted present.
    expect(badge).toHaveClass('left-2', 'top-2', 'rounded-full')
    expect(badge.className).not.toContain('left-0')
    expect(badge.className).not.toContain('top-0')
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

/**
 * The E-32 skin rules that live in `styles/base.css` rather than in a class list.
 *
 * These are asserted against the **stylesheet source**, which is the only place
 * jsdom can see them: this suite runs with `css: false`, so `getComputedStyle`
 * reports nothing for `.seg-opt[data-checked='true']` and a test that read a
 * resolved colour would pass whatever the rule said. `tokens.test.ts` parses
 * `tokens.css` for the same reason.
 *
 * Every one of them is a case where the *shipped* value is invisible rather than
 * merely different, which is why they are pinned at all.
 */
describe('E-32 markers in the component stylesheet', () => {
  /**
   * **Comments stripped first**, and that is not tidiness.
   *
   * `block()` searches raw text for a selector. Until this line existed it found
   * selectors *inside comments*: writing `.sidebar { box-shadow:
   * var(--shadow-sidebar) }` in a comment and reverting the real rule to
   * `--shadow-md` left all six assertions below green. Every guard in this
   * describe was defeatable by a sentence — and the sentence is exactly what an
   * author explaining the rule would write. `stripComments` replaces each
   * comment with the same number of spaces, so offsets are unchanged and only
   * the hiding places go away.
   */
  const BASE_CSS = stripComments(readFileSync(resolve(process.cwd(), 'src/styles/base.css'), 'utf8'))

  /**
   * The declarations of the first rule whose selector list contains `selector`,
   * searching from `after` — the size scale writes `h6` twice, once inside the
   * `h1, … h6` group selector and once as its own rule, and it is the second
   * that carries the section-label style.
   */
  function block(selector: string, after = 0): string {
    const at = BASE_CSS.indexOf(selector, after)
    expect(at, `${selector} is not in base.css`).toBeGreaterThan(-1)
    const open = BASE_CSS.indexOf('{', at)
    const close = BASE_CSS.indexOf('}', open)
    return BASE_CSS.slice(open + 1, close)
  }

  it('reads the sheet with its comments removed', () => {
    // The calibration for the line above: base.css is heavily commented and
    // several of those comments quote the very selectors and declarations these
    // assertions look for. If stripping ever stops happening, this fails before
    // the six guards below start passing for the wrong reason.
    expect(BASE_CSS).not.toContain('E-32:')
    expect(BASE_CSS).toContain('.sidebar {')
    expect(BASE_CSS).toHaveLength(
      readFileSync(resolve(process.cwd(), 'src/styles/base.css'), 'utf8').length,
    )
  })

  it('rings the checked segment in --color-hot — without it dark has no selection', () => {
    // The fill is `--color-accent`, a deep teal, and the viewer ground it sits
    // on is #263B38: ~1.2:1. On the dark theme the ring is not decoration, it is
    // the entire difference between a selected option and an unselected one.
    expect(block(".seg-opt[data-checked='true']")).toContain(
      'inset 0 0 0 2px var(--color-hot)',
    )
  })

  /*
   * **The checked segment's raised shadow is asserted in `styles/soft-ui.test.ts`,
   * not here.**
   *
   * A pair of assertions pinning `.seg-opt[data-checked='true']` to
   * `--shadow-control-raised` (and off `--shadow-sm`) briefly lived here as well
   * as there. That is a second source of truth for one rule, which is the thing
   * `soft-ui.test.ts`'s own header refuses to do about §3.6 — and two copies of a
   * contract drift the moment one is updated, which is E-36 §2 in miniature.
   *
   * `soft-ui.test.ts` is the copy that stays: the rename is a consequence of
   * E-42 §3's shadow rule, that file holds the rule and the frozen tokens it
   * names, and the surrounding assertions there (`--shadow-control-raised` is
   * light `--shadow-sm` frozen, and is a literal rather than a `var()`) are what
   * make the pin mean anything. Isolated in this file it would be a string
   * comparison with its reasoning somewhere else.
   *
   * What stays here is the `--color-hot` ring above, which is a *marker* rule
   * from E-32 §1 rather than an elevation one.
   */

  it('rings the selected sidebar row in --color-hot, not a 3px accent rail', () => {
    const rule = block(".sidebar-nav-row[data-active='true']")
    expect(rule).toContain('inset 0 0 0 2px var(--color-hot)')
    // The rail is what the ring replaces; it must not come back, because the
    // accent it was drawn in is 1.09:1 on the dark ground.
    expect(rule).not.toContain('border-left')
  })

  it('turns the sidebar edge and the top bar rule into elevation', () => {
    // `--shadow-sidebar`, not `--shadow-md`: the panel's elevation is horizontal
    // only (`4px 0 18px`), and the card token that stood in for it while no such
    // token existed carries a 6px downward offset — a shadow under the top edge
    // of a panel that runs the full height of the viewport (open item p).
    const sidebar = block('.sidebar {')
    expect(sidebar).toContain('box-shadow: var(--shadow-sidebar)')
    expect(sidebar).not.toContain('border-right')
  })

  it('gives the row hover chip a dark-theme override, because the ramps do not flip', () => {
    // `--color-neutral-100` is #F7F3EA in *both* themes — it is an absolute
    // lightness scale (ui-spec §1.4). Hovering a row in the dark theme with it
    // paints a white bar, so the dark ground takes `--row-hover` instead.
    expect(block('.row-chip:hover')).toContain('var(--color-neutral-100)')
    expect(block("[data-theme='dark'] .row-chip:hover")).toContain('var(--row-hover)')
  })

  it('keeps the section heading a heading — 16px on the tag, not a <div>', () => {
    // E-32 §4: the prototype's `<h6>` → `<div>` swap deletes the document
    // outline and breaks `e2e/06-settings.spec.ts`'s `getByRole('heading')`.
    const h6 = block('h6 {', BASE_CSS.indexOf('h5 {'))
    expect(h6).toContain('font-size: 16px')
    // 13px + 0.08em + uppercase is the label style E-32 replaces.
    expect(h6).not.toContain('text-transform')
    expect(h6).not.toContain('13px')
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

  /**
   * `Infinity` is an ordinary input here, not a defensive afterthought.
   *
   * Callers divide a page by a page count and a `status != "ok"` volume reports
   * `page_count: 0` (arch §4.11), so `last_page / 0` reaches this component by
   * the plain route. The clamp alone would answer it with a **full** bar —
   * `Math.min(1, Infinity)` is 1 — which is the single worst reading available:
   * a volume that lost its pages drawn as finished.
   */
  it('reads a non-finite value as an empty trough, not a full bar', () => {
    for (const value of [Infinity, NaN, -Infinity]) {
      const { container, unmount } = render(<ProgressBar value={value} label="몬스터" />)
      expect(screen.getByRole('progressbar', { name: '몬스터' })).toHaveAttribute(
        'aria-valuenow',
        '0',
      )
      expect(container.querySelector<HTMLElement>('[role=progressbar] > div')?.style.width).toBe(
        '0%',
      )
      unmount()
    }
  })

  /**
   * The fill is `--accent-fill`, never `--color-accent`.
   *
   * E-32 turned the accent into #17595B, which is **1.09:1** on `--fill-track`
   * in the dark theme: a progress bar that renders as an empty trough at every
   * value, on the library list, the series hero, every continue card and every
   * volume row at once. `--accent-fill` is the token that moves up the ramp on
   * dark (3.86) and stays put on light (5.78).
   *
   * The assertion is on the **class name**, not on a resolved colour: this suite
   * runs with `css: false`, so `getComputedStyle` would report the same empty
   * string for `bg-accent` and `bg-accent-fill` and the test would pass either
   * way. Naming the token is the only thing jsdom can actually see.
   */
  it('fills with --accent-fill, which is the accent that survives the dark ramp', () => {
    const { container } = render(<ProgressBar value={0.34} />)
    const fill = container.querySelector('[role=progressbar] > div')
    expect(fill).toHaveClass('bg-accent-fill')
    // `classList.contains` matches whole tokens, so this does not trip over
    // `bg-accent-fill` containing the string `bg-accent`.
    expect(fill?.classList.contains('bg-accent')).toBe(false)
  })

  it('is a pill in a recessed channel, and 5px of flat rail over artwork (E-32)', () => {
    render(<ProgressBar value={0.5} height={5} track="over-art" label="x" />)
    const overArt = screen.getByRole('progressbar', { name: 'x' })
    expect(overArt).toHaveClass('bg-fill-track-2', 'h-[5px]', 'overflow-hidden')
    // No inset highlight on a photograph, and no radius on a full-bleed rail.
    expect(overArt.classList.contains('shadow-inset')).toBe(false)
    expect(overArt.classList.contains('rounded-full')).toBe(false)

    render(<ProgressBar value={0.5} label="y" />)
    const inRow = screen.getByRole('progressbar', { name: 'y' })
    expect(inRow).toHaveClass('h-[6px]', 'rounded-full', 'bg-fill-track', 'shadow-inset')
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

  it('wears the skin of the thing it stands in for (E-32)', () => {
    // A square-cornered shimmer next to a rounded, recessed cover well reads as
    // a rendering fault rather than as loading.
    const { container: cover } = render(<Skeleton variant="cover" />)
    expect(cover.firstElementChild).toHaveClass('rounded-md', 'shadow-inset')

    const { container: line } = render(<Skeleton variant="line" width="84%" />)
    expect(line.firstElementChild).toHaveClass('rounded-full')
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

  it('stands the bars on a small raised card, at both sizes (E-32)', () => {
    const { container: hero, unmount } = render(<Wordmark variant="hero" />)
    expect(hero.querySelector('[aria-hidden="true"]')).toHaveClass(
      'bg-surface',
      'rounded-pill',
      'shadow-md',
    )
    unmount()

    const { container: compact } = render(<Wordmark variant="compact" />)
    expect(compact.querySelector('[aria-hidden="true"]')).toHaveClass(
      'bg-surface',
      'rounded-md',
      'shadow-sm',
    )
  })
})
