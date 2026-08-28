import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import postcss from 'postcss'
import tailwindcss from 'tailwindcss'
import { describe, expect, it } from 'vitest'

import tailwindConfig from '../../tailwind.config'

/**
 * The cascade, checked against the **compiled** stylesheet (E-55).
 *
 * `tokens.test.ts` reads base.css as text and `nameLang.test.tsx` runs under
 * `css: false`. Neither tier applies a stylesheet, so neither can see which of
 * two rules actually wins — and E-55 lost that exact bet. The `[lang='ja']`
 * rule went into `@layer components`, to beat `.card-title` and `.btn`, and in
 * a browser it lost to something nobody had looked at: `FallbackCover`,
 * `ContinueCard`, `NextVolumeCard` and `ViewerTopBar` each put `font-heading`
 * on the very element carrying the tag, Tailwind emits utilities *after*
 * `@tailwind components`, and at equal specificity the later rule wins. Four
 * tagged surfaces went on rendering 명조 with `lang="ja"` sitting on them, and
 * every tier was green.
 *
 * So this one runs the real PostCSS/Tailwind pipeline and asks the output the
 * question the browser asks: which rule comes last.
 */

const ROOT = resolve(process.cwd(), 'src')
const BASE = readFileSync(resolve(ROOT, 'styles/base.css'), 'utf8')

async function compile(): Promise<string> {
  const result = await postcss([
    tailwindcss({
      ...tailwindConfig,
      content: [resolve(ROOT, '**/*.{ts,tsx}')],
    }),
  ]).process(BASE, { from: resolve(ROOT, 'styles/base.css') })
  return result.css
}

describe('compiled cascade (E-55)', () => {
  it('puts the ja rule after every font-family utility it has to beat', async () => {
    const css = await compile()

    // Same specificity — `[lang='ja']` and `.font-heading` are both one class
    // -equivalent — so position is the whole verdict. Take the *last*
    // occurrence of each utility: Tailwind may emit a family more than once.
    const jaRule = css.search(/\[lang=['"]?ja['"]?\]/)
    expect(jaRule, 'the [lang=ja] rule is not in the compiled sheet').toBeGreaterThan(-1)

    for (const utility of ['.font-heading', '.font-sans', '.font-seal']) {
      const at = css.lastIndexOf(utility)
      if (at === -1) continue // not used by any component; nothing to beat
      expect(jaRule, `${utility} is emitted after [lang='ja'] and would win`).toBeGreaterThan(at)
    }
  }, 30_000)

  it('still loses to nothing that sets a family on the same element', async () => {
    const css = await compile()
    const jaRule = css.search(/\[lang=['"]?ja['"]?\]/)

    // Every *other* rule in the sheet that sets `font-family` and could match
    // the same element at the same specificity. A new `.card-title`-shaped
    // class added after this rule would silently take the tag's surfaces back,
    // which is the failure this file exists for — stated as a rule rather than
    // as the four class names that happened to break it.
    const later = [...css.matchAll(/\.[a-z][\w-]*\s*\{[^}]*font-family[^}]*\}/g)]
      .filter((m) => m.index > jaRule)
      .map((m) => m[0].slice(0, m[0].indexOf('{')).trim())
    expect(later).toEqual([])
  }, 30_000)
})
