/**
 * Hangul initial-consonant (초성) helper.
 *
 * **Highlighting only.** Search itself is server-side: with 963–10 000 series
 * and FR-LIB-007's virtualised list the client never holds the whole catalogue,
 * so a client-side filter is wrong by construction (C-10 / D-34). The top-bar
 * field and the command palette both call `GET /api/series?q=…`; this function
 * exists so the returned rows can show *which* part of the title the 초성 query
 * matched.
 */

const CHO = 'ㄱㄲㄴㄷㄸㄹㅁㅂㅃㅅㅆㅇㅈㅉㅊㅋㅌㅍㅎ'

const HANGUL_BASE = 0xac00
const HANGUL_COUNT = 11172
/** Syllables per initial consonant: 21 medials × 28 finals. */
const CHO_STRIDE = 588

/**
 * Maps every precomposed Hangul syllable in `s` to its initial consonant and
 * leaves every other code point untouched.
 *
 * `안녕 hi` → `ㅇㄴ hi`.
 */
export function chosung(s: string): string {
  let out = ''
  for (const ch of s) {
    const c = ch.charCodeAt(0) - HANGUL_BASE
    if (c >= 0 && c < HANGUL_COUNT) {
      out += CHO[Math.floor(c / CHO_STRIDE)] ?? ch
    } else {
      out += ch
    }
  }
  return out
}

/** True when `q` is entirely initial-consonant jamo — i.e. a 초성 query. */
export function isChosungQuery(q: string): boolean {
  if (q === '') return false
  for (const ch of q) {
    if (!CHO.includes(ch)) return false
  }
  return true
}

/**
 * Locates the span of `title` that a query matched, so the palette and the
 * result rows can highlight it.
 *
 * A 초성 query is folded through `chosung()`, which maps one code point to one
 * code point; anything else is compared case-insensitively. Offsets are
 * returned in **code points** of `title`, so `[...title].slice(start, end)`
 * reconstructs the matched span even for astral characters.
 *
 * @returns `[start, end)` or `null` when there is no match.
 */
export function matchRange(title: string, query: string): [number, number] | null {
  if (query === '') return null

  const chosungQuery = isChosungQuery(query)
  // `Array.from` rather than a spread: both iterate code points, but the
  // spread of a string is banned by lint as an easy source of surprises.
  const chars = Array.from(title)
  const folded = chosungQuery ? chars.map(chosung) : chars.map((ch) => ch.toLowerCase())
  const needle = chosungQuery ? query : query.toLowerCase()
  if (needle === '') return null

  for (let start = 0; start < folded.length; start++) {
    let consumed = 0
    let matched = 0
    while (matched < needle.length && start + consumed < folded.length) {
      matched += (folded[start + consumed] ?? '').length
      consumed += 1
    }
    if (matched < needle.length) break // not enough input left at any later start
    if (folded.slice(start, start + consumed).join('').startsWith(needle)) {
      return [start, start + consumed]
    }
  }
  return null
}
