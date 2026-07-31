import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { describe, expect, it } from 'vitest'

import { THUMB_WIDTHS } from '../../api/urls'
import type { Breakpoint } from '../../lib/useMediaQuery'
import { customProperties, topLevelRules } from '../../styles/cssRules'
import {
  cardHeight,
  columnCount,
  columnWidth,
  GRID_METRICS,
  gridCoverWidth,
  highlightParts,
  isSmartScope,
  libraryParams,
  listHeaderPadRight,
  listLayoutFor,
  LIST_TEMPLATE,
  scopeLabel,
  type GridMetrics,
} from './useLibrary'

/**
 * The rules of the library screen that are pure functions of UI state: the
 * `GET /api/series` parameter mapping (FR-LIB-004, -005, -006 + A-4/C-15), the
 * 초성 highlight span, and the grid geometry FR-LIB-007 needs in JavaScript.
 */

describe('libraryParams — scope, sort and search (FR-LIB-004, -005, -006)', () => {
  const base = { scope: 'all', sort: 'name', order: 'asc', query: '' } as const

  it('defaults to name/asc with a page limit and no filter', () => {
    expect(libraryParams(base)).toEqual({ sort: 'name', order: 'asc', limit: 60 })
  })

  it('carries every sort key of FR-LIB-004 through to the wire', () => {
    // C-3: the API's names win. `read`/`vols` do not exist in this codebase.
    for (const [sort, order] of [
      ['name', 'asc'],
      ['mtime', 'desc'],
      ['recent', 'desc'],
      ['size', 'desc'],
      ['books', 'desc'],
    ] as const) {
      expect(libraryParams({ ...base, sort, order })).toMatchObject({ sort, order })
    }
  })

  it('maps the smart lists onto progress= (amendment A-4)', () => {
    expect(libraryParams({ ...base, scope: 'reading' })).toMatchObject({ progress: 'reading' })
    expect(libraryParams({ ...base, scope: 'done' })).toMatchObject({ progress: 'done' })
    // `any` is the server default and is never sent.
    expect(libraryParams({ ...base, scope: 'all' }).progress).toBeUndefined()
  })

  it('makes 최근 추가 a scope filter *and* the sort within it (A-8 / ruling E-9)', () => {
    const params = libraryParams({ ...base, scope: 'added', sort: 'name', order: 'asc' })
    // The regression: without `scope`, `sort=added` merely re-orders the whole
    // library, so the section lists every series under a heading that promises
    // a window.
    expect(params).toMatchObject({ scope: 'added', sort: 'added', order: 'desc' })
    expect(params.progress).toBeUndefined()
    expect(params.root).toBeUndefined()
  })

  it('sends scope= for no other list (A-8: exactly `all`|`added` are legal)', () => {
    for (const scope of ['all', 'reading', 'done', 'mangga'] as const) {
      expect(libraryParams({ ...base, scope }).scope).toBeUndefined()
    }
  })

  it('turns any other scope into a root filter (FR-LIB-005)', () => {
    const params = libraryParams({ ...base, scope: 'mangga' })
    expect(params.root).toEqual(['mangga'])
    expect(params.progress).toBeUndefined()
  })

  it('sends a trimmed q and drops a blank one (FR-LIB-006)', () => {
    expect(libraryParams({ ...base, query: '  군계 ' }).q).toBe('군계')
    expect(libraryParams({ ...base, query: 'ㄱㄱ' }).q).toBe('ㄱㄱ')
    expect(libraryParams({ ...base, query: '   ' }).q).toBeUndefined()
  })

  it('knows a smart scope from a root name', () => {
    expect(isSmartScope('reading')).toBe(true)
    expect(isSmartScope('mangga')).toBe(false)
  })
})

describe('scopeLabel (ui-spec §4.4 / §9 catalogue)', () => {
  const roots = [{ name: 'mangga', label: '만화' }]

  it('uses the catalogue copy for the smart lists', () => {
    expect(scopeLabel('all', roots)).toBe('전체 시리즈')
    expect(scopeLabel('reading', roots)).toBe('읽는 중')
    expect(scopeLabel('added', roots)).toBe('최근 추가')
    expect(scopeLabel('done', roots)).toBe('완독')
  })

  it('shows a root by its display label, falling back to its name', () => {
    expect(scopeLabel('mangga', roots)).toBe('만화')
    expect(scopeLabel('lanovel', roots)).toBe('lanovel')
  })
})

describe('highlightParts — 초성 match highlighting (FR-LIB-006, C-10)', () => {
  it('locates a 초성 query inside a Korean title', () => {
    expect(highlightParts('[만화] 군계 1~25', 'ㄱㄱ')).toEqual({
      before: '[만화] ',
      match: '군계',
      after: ' 1~25',
    })
  })

  it('locates a literal substring case-insensitively', () => {
    expect(highlightParts('3X3 EYES 1~40(완)', 'eyes')).toEqual({
      before: '3X3 ',
      match: 'EYES',
      after: ' 1~40(완)',
    })
  })

  it('returns the whole title unmatched when nothing matches or the query is blank', () => {
    expect(highlightParts('[만화] 군계 1~25', 'ㅎㅌ')).toEqual({
      before: '[만화] 군계 1~25',
      match: '',
      after: '',
    })
    expect(highlightParts('[만화] 군계 1~25', '  ')).toEqual({
      before: '[만화] 군계 1~25',
      match: '',
      after: '',
    })
  })
})

describe('grid geometry (FR-LIB-007)', () => {
  it('reproduces repeat(auto-fill, minmax(min, 1fr))', () => {
    const metrics: GridMetrics = { min: 152, gap: 16 }
    // The width this must be fed is the **grid box**, not the padded wrapper
    // around it: at 1440 that is 1440 − 240 sidebar − 2 rule − 12 scrollbar −
    // 32 padding = 1156, and floor((1156+16)/168) = 6 columns of 179.3px —
    // ui-spec §7's "6 cols @1440" and library-grid-1440.png. Handing it the
    // wrapper's 1188 instead yields 7 columns of 151.4px, *below* the 152px
    // `--grid-min`; `SeriesGrid` puts its ref on the grid box for exactly this
    // reason and `library.test.tsx` asserts the rendered result.
    expect(columnCount(1156, metrics)).toBe(6)
    expect(columnCount(1188, metrics)).toBe(7)
    expect(columnCount(1154, metrics)).toBe(6)
    expect(columnCount(1160, metrics)).toBe(7)
    // The ui-spec's own numbers: 8 columns at 1760.
    expect(columnCount(1488, metrics)).toBe(8)
    expect(columnCount(0, metrics)).toBe(1)
    expect(columnCount(100, metrics)).toBe(1)
  })

  it('never resolves a column narrower than --grid-min', () => {
    // The invariant behind auto-fill: whatever width comes in, the column it
    // produces is at least `min` wide. A width measured off the wrong box
    // breaks this, which is what makes it a useful assertion.
    for (const bp of ['mobile', 'tablet', 'laptop', 'desktop'] as const) {
      const metrics = GRID_METRICS[bp]
      for (let width = metrics.min; width <= 2_000; width += 7) {
        const cols = columnCount(width, metrics)
        expect(columnWidth(width, cols, metrics.gap)).toBeGreaterThanOrEqual(metrics.min)
      }
    }
  })

  it('divides the remainder like 1fr does', () => {
    expect(columnWidth(1168, 6, 16)).toBeCloseTo((1168 - 80) / 6)
    expect(columnWidth(0, 1, 16)).toBe(0)
  })

  it('derives a row height from the 2:3 cover plus fixed text', () => {
    expect(cardHeight(180)).toBe(270 + 60)
    // Subpixel-exact: the cover is an `aspect-ratio:2/3` border box, so
    // rounding here would leave up to half a pixel of drift on every row.
    expect(cardHeight(179.328)).toBe(179.328 * 1.5 + 60)
    // Never negative or NaN when the container has not been measured yet.
    expect(cardHeight(0)).toBe(60)
    expect(cardHeight(Number.NaN)).toBe(60)
  })

  it('pads the list header by the scroller gutter it does not contain', () => {
    expect(listHeaderPadRight(12)).toBe('calc(var(--space-4) + 12px)')
    expect(listHeaderPadRight(0)).toBe('calc(var(--space-4) + 0px)')
  })

  it('requests a cover width from the configured set (impl-plan §0.4)', () => {
    expect(gridCoverWidth('tablet')).toBe(640)
    expect(gridCoverWidth('desktop')).toBe(400)
    for (const bp of ['mobile', 'tablet', 'laptop', 'desktop'] as const) {
      expect(THUMB_WIDTHS).toContain(gridCoverWidth(bp))
    }
  })

  it('never drifts from the --grid-min / --grid-gap block of tokens.css', () => {
    // The token layer stays authoritative: this reads the stylesheet and
    // rebuilds the cascade, so changing a breakpoint there fails here.
    const css = readFileSync(resolve(process.cwd(), 'src/styles/tokens.css'), 'utf8')
    const running: GridMetrics = { min: 0, gap: 0 }
    const effective: Partial<Record<Breakpoint, GridMetrics>> = {}

    for (const rule of topLevelRules(css)) {
      const props = customProperties(rule.body)
      const min = props.get('--grid-min')
      const gap = props.get('--grid-gap')
      if (min === undefined && gap === undefined) continue
      if (min !== undefined) running.min = Number.parseInt(min, 10)
      if (gap !== undefined) running.gap = Number.parseInt(gap, 10)

      const width = /min-width:\s*(\d+)px/.exec(rule.selector)?.[1]
      const tier: Breakpoint =
        width === undefined
          ? 'mobile'
          : width === '768'
            ? 'tablet'
            : width === '1024'
              ? 'laptop'
              : 'desktop'
      effective[tier] = { ...running }
    }

    expect(effective).toEqual(GRID_METRICS)
  })
})

describe('list responsive layout (ui-spec §7)', () => {
  it('drops 용량 + 수정일 on the rail tier and stacks below 768', () => {
    expect(listLayoutFor('desktop')).toBe('full')
    expect(listLayoutFor('laptop')).toBe('full')
    expect(listLayoutFor('tablet')).toBe('compact')
    expect(listLayoutFor('mobile')).toBe('stacked')
  })

  it('keeps the ui-spec §4.5 column template verbatim', () => {
    expect(LIST_TEMPLATE.full).toBe('32px minmax(0,1fr) 66px 64px 78px 100px 148px')
    expect(LIST_TEMPLATE.compact).toBe('32px minmax(0,1fr) 66px 64px 120px')
    expect(LIST_TEMPLATE.stacked).toBe('32px minmax(0,1fr)')
  })
})
