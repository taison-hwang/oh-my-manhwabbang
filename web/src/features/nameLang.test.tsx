import '@testing-library/jest-dom/vitest'

import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import type { ReactElement } from 'react'
import { describe, expect, it } from 'vitest'

import { bookSummary, seriesSummary } from '../api/fixtures'
import { SeriesCard } from './library/SeriesCard'
import { SeriesRow } from './library/SeriesRow'
import { VolumeRow } from './series/VolumeRow'
import { VolumeTile } from './series/VolumeTile'

/**
 * E-55 — the `lang` tag has to reach the DOM, on every surface that paints a
 * name.
 *
 * `tokens.test.ts` proves the stacks and the `[lang='ja']` rule exist and
 * `textLang.test.ts` proves the rule classifies correctly. Both can be green
 * while **no element in the product ever carries the attribute** — the CSS
 * would then be a rule with no subject, and a Japanese title would go on
 * rendering as 명조 kanji around a 고딕 kana with nothing failing. This is the
 * join: render the surfaces with a Japanese name and look for the tag.
 *
 * Korean is asserted alongside it on every surface, because the opposite
 * mistake — tagging unconditionally — is the one that looks fine in a test
 * that only ever passes Japanese in, and would hand the whole library to 본고딕.
 */

const JA = '[後藤晶] カノジョは官能小說家 02'
const KO = '[만화] 군계 1~25'

function withQuery(node: ReactElement): ReactElement {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  })
  return <QueryClientProvider client={client}>{node}</QueryClientProvider>
}

/**
 * The `lang` of **every** element that paints the name, not the first one.
 *
 * This started as `getAllByText(name)[0]`, and a mutation run caught it: strip
 * the tag off `SeriesCard`'s title and the SeriesCard case still passed. A
 * card paints its series name *twice* — once in the placeholder cover
 * underneath and once as the title over it — and `[0]` was reading the
 * placeholder's tag, so the assertion had been checking `FallbackCover` under
 * four different names. Collecting all of them is what makes each surface
 * answer for its own markup.
 */
function langsOfNameElements(name: string): (string | null)[] {
  const elements = screen.getAllByText(name)
  expect(elements.length, `nothing rendered ${name}`).toBeGreaterThan(0)
  return elements.map((element) => element.getAttribute('lang'))
}

describe('display names carry their language (E-55)', () => {
  const surfaces: readonly (readonly [string, (name: string) => ReactElement])[] = [
    [
      'SeriesCard (grid)',
      (name) => (
        <SeriesCard
          series={{ ...seriesSummary, name, has_cover: false, cover_cv: null }}
          coverWidth={240}
          query=""
          onOpen={() => undefined}
          onResume={() => undefined}
        />
      ),
    ],
    [
      'SeriesRow (list)',
      (name) => (
        <SeriesRow
          series={{ ...seriesSummary, name, has_cover: false, cover_cv: null }}
          layout="full"
          query=""
          onOpen={() => undefined}
        />
      ),
    ],
    [
      'VolumeRow (series detail, list)',
      (name) => <VolumeRow book={{ ...bookSummary, name }} onOpen={() => undefined} />,
    ],
    [
      'VolumeTile (series detail, grid)',
      (name) => <VolumeTile book={{ ...bookSummary, name }} number={1} onOpen={() => undefined} />,
    ],
  ]

  for (const [label, surface] of surfaces) {
    it(`${label} tags a Japanese name`, () => {
      render(withQuery(surface(JA)))
      expect(langsOfNameElements(JA).filter((lang) => lang !== 'ja')).toEqual([])
    })

    it(`${label} leaves a Korean name untagged`, () => {
      render(withQuery(surface(KO)))
      expect(langsOfNameElements(KO).filter((lang) => lang !== null)).toEqual([])
    })
  }
})
