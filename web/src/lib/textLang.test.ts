import { describe, expect, it } from 'vitest'

import { textLang } from './textLang'

/**
 * The rule is one regex, and every interesting case is a *name off this
 * library's disk* rather than a constructed string — the heuristic exists to
 * classify filenames, so a fixture that no filename resembles proves nothing
 * about it. The counts quoted below were measured against the 18 721 names in
 * `resources/shelf/index.db` on 2026-08-28.
 */
describe('textLang (E-55)', () => {
  it('leaves a Korean name untagged, 한자 and all', () => {
    // 668 of the 727 names carrying 한자 carry no kana, and they are Korean.
    // Tagging any of these would hand a Korean title to 본고딕.
    expect(textLang('[만화] 군계 1~25')).toBeUndefined()
    expect(textLang('20세기소년 1~22(完) +21세기소년(完)')).toBeUndefined()
    expect(textLang('Beck 1~34권 (完)')).toBeUndefined()
    expect(textLang('三國志')).toBeUndefined()
  })

  it('tags a name that carries kana', () => {
    expect(textLang('[後藤晶] カノジョは官能小說家 02 [韓].zip')).toBe('ja')
    expect(textLang('進撃の巨人')).toBe('ja')
    // Hiragana, katakana and the length mark each on their own.
    expect(textLang('ひ')).toBe('ja')
    expect(textLang('カ')).toBe('ja')
    expect(textLang('ー')).toBe('ja')
  })

  it('tags the mixed name, which is the shape most of them have here', () => {
    // 128 names carry kana and this is what they mostly look like: a Korean
    // title with a Japanese fragment inside it. The tag is still right — the
    // fragment is Japanese — and it is why `--font-ja` has to keep 고운바탕 in
    // front and why 본고딕's `unicode-range` stops short of Hangul.
    expect(textLang('나와 그녀의 연애목록 (すかぢ) 1~19화')).toBe('ja')
    expect(textLang('니노미아 히카루 - アイであそぶ 단편 모음집.zip')).toBe('ja')
    expect(textLang('바다의 무녀 (완결, 미번) (文月晃, 海の御先)')).toBe('ja')
  })

  it('does not fire on the CJK punctuation Korean names share', () => {
    // `・` U+30FB lives in the katakana *block* and is used as a separator in
    // Korean and Chinese names too, so a block-wide match would tag them all
    // Japanese. `、` and `。` are shared the same way. Matching kana letters
    // instead of kana blocks is the whole reason these come back undefined.
    expect(textLang('아이돌 마스터・신데렐라')).toBeUndefined()
    expect(textLang('제목、부제')).toBeUndefined()
    expect(textLang('제목。')).toBeUndefined()
    expect(textLang('〜〜〜')).toBeUndefined()
  })

  it('misses an all-kanji Japanese title, and that is the stated limit', () => {
    // 「攻殻機動隊」 is Japanese and comes back undefined, so it is drawn in
    // 명조 like a Korean title's 한자. The string carries nothing that
    // separates it from 「三國志」 above, and a rule that guessed from the
    // ideographs would mis-set the 668 Korean names that carry 한자 without
    // kana in order to win this one. Asserted so the limit is a decision on
    // the record rather than a surprise.
    expect(textLang('攻殻機動隊')).toBeUndefined()
  })

  /**
   * The range endpoints, checked against **numbers** rather than against more
   * characters.
   *
   * Every other case in this file feeds the rule a name and reads the verdict,
   * which cannot see a range that is subtly the wrong range — a test written
   * with the same character literals as the rule agrees with it by
   * construction. This is not hypothetical: the census behind E-55 was run
   * twice, and one run called 1 100 of 1 112 series 한자-bearing because a `豈`
   * in the analysis regex was U+8C48 rather than U+F900, turning `豈-﫿` into a
   * range that swallows all of Hangul. Two identical-looking characters, and
   * nothing in a diff to see.
   */
  it('matches exactly the kana letters, by codepoint', () => {
    const intended = (cp: number): boolean =>
      (cp >= 0x3041 && cp <= 0x3096) ||
      cp === 0x309d ||
      cp === 0x309e ||
      (cp >= 0x30a1 && cp <= 0x30fa) ||
      (cp >= 0x30fc && cp <= 0x30fe)

    const wrong: string[] = []
    for (let cp = 0; cp <= 0xffff; cp++) {
      const hit = textLang(String.fromCharCode(cp)) === 'ja'
      if (hit !== intended(cp)) wrong.push(`U+${cp.toString(16).toUpperCase()}`)
    }
    expect(wrong).toEqual([])

    // The four that decide whether a Korean name gets dragged into the
    // Japanese stack, named by number so this cannot drift into agreement with
    // the rule the way a character literal would.
    for (const cp of [0x30fb, 0x3000, 0x3001, 0x3002, 0x301c, 0xac00, 0x4e00]) {
      expect(textLang(String.fromCharCode(cp)), `U+${cp.toString(16)}`).toBeUndefined()
    }
  })

  it('returns undefined — not a language — for an absent name', () => {
    // React drops an attribute whose value is `undefined`, which is what keeps
    // a redundant `lang="ko"` off every row in the library.
    expect(textLang('')).toBeUndefined()
    expect(textLang(null)).toBeUndefined()
    expect(textLang(undefined)).toBeUndefined()
  })
})
