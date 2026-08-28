/**
 * Which language a display name is set in — for the font stack, and nothing
 * else (E-55).
 *
 * # Why a *name* needs a language at all
 *
 * `unicode-range` splits a font stack by codepoint, and that is enough for
 * every other script this product draws: Hangul is 고운바탕, latin is 고운바탕,
 * numerals are Archivo. It is **not** enough for Han. A Japanese title's kanji
 * and a Korean title's 한자 are the same codepoints, so no `unicode-range`
 * can tell 「進撃の巨人」 from 「三國志」 — the only difference is what language
 * the surrounding *name* is in.
 *
 * Left at the codepoint level, a Japanese title comes out split down the
 * middle: 進撃 and 巨人 in the 명조 한자 face, の in the 고딕 kana face. One
 * word, two skins. Tagging the element instead lets `[lang='ja']` hand the
 * whole name to a single face (`--font-ja`, base.css).
 *
 * # The rule: kana means Japanese
 *
 * Names come from filenames, so there is no metadata to ask — the string is
 * the only evidence. Kana is script-exclusive to Japanese and appears in no
 * Korean or Chinese name, which makes "carries kana" a rule that cannot fire
 * on a Korean title. Measured against the 18 721 names on this machine: 128
 * carry kana, and every one of them is Japanese or a Korean title with a
 * Japanese fragment inside it.
 *
 * **What it deliberately does not catch** is an all-kanji Japanese title —
 * 「攻殻機動隊」 has no kana, so it is drawn in 명조 like a Korean title's 한자.
 * That is not a bug this heuristic can fix: the string carries no signal that
 * separates it from 「三國志」, and guessing from the ideographs themselves
 * would mis-set the 668 Korean names on this machine that carry 한자 without
 * kana to win the handful that do not.
 *
 * Note the mixed case is the *common* one here, not the exception:
 * `나와 그녀의 연애목록 (すかぢ) 1~19화` is tagged `ja` and is mostly Hangul.
 * That is why `--font-ja` still leads with 고운바탕 and why the Japanese face's
 * `unicode-range` stops short of Hangul — the tag changes which face draws the
 * name's 한자 and 가나, and must not touch its 한글.
 */

/**
 * Kana **letters**, and not the whole two blocks.
 *
 * `U+30FB` ・ is a katakana-block character that is used as a separator in
 * Korean and Chinese names too, and `U+3000`–`U+303F` punctuation is shared
 * across all of CJK; matching the blocks wholesale would tag those names
 * Japanese. Restricted here to hiragana/katakana letters plus their iteration
 * and length marks, which nothing but Japanese uses.
 */
const KANA =
  /[\u3041-\u3096\u309D\u309E\u30A1-\u30FA\u30FC-\u30FE]/
/*
 * Written as escapes, not as the literal `[ぁ-ゖゝゞァ-ヺー-ヾ]` it replaces.
 * A range endpoint typed as a character is a homoglyph waiting to happen: the
 * census that produced the numbers above was run twice, and one run reported
 * 1 100 of 1 112 series as carrying 한자 because a `豈` in the *analysis*
 * regex was U+8C48 (the unified ideograph) rather than U+F900 (the
 * compatibility one), which turned `豈-﫿` into a range that swallows all of
 * Hangul. Two characters that render identically, a rule that quietly means
 * something else, and nothing to see in a diff. The escapes are checkable.
 */

/**
 * `'ja'` for a name to hand to the Japanese face, `undefined` for everything
 * else.
 *
 * `undefined` rather than `'ko'` on purpose: React drops an attribute whose
 * value is `undefined`, so an untagged name inherits the document language
 * instead of carrying a redundant `lang="ko"` on every row in the library.
 */
export function textLang(text: string | null | undefined): 'ja' | undefined {
  return text != null && KANA.test(text) ? 'ja' : undefined
}
