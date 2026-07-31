import { describe, expect, it } from 'vitest'

import { chosung, isChosungQuery, matchRange } from './chosung'

describe('chosung', () => {
  it('reduces precomposed Hangul to its initial consonants', () => {
    expect(chosung('환타지스타')).toBe('ㅎㅌㅈㅅㅌ')
    expect(chosung('히스토리에')).toBe('ㅎㅅㅌㄹㅇ')
    // The palette's own hint string, from the ui-spec §9 catalogue.
    expect(chosung('회장님은메이드사마')).toBe('ㅎㅈㄴㅇㅁㅇㄷㅅㅁ')
  })

  it('covers both ends of the syllable block', () => {
    expect(chosung('가')).toBe('ㄱ') // U+AC00
    expect(chosung('힣')).toBe('ㅎ') // U+D7A3
  })

  it('passes everything else through untouched', () => {
    expect(chosung('[만화] 3X3 EYES 1~40(완)')).toBe('[ㅁㅎ] 3X3 EYES 1~40(ㅇ)')
    expect(chosung('ㄱㄴㄷ')).toBe('ㄱㄴㄷ') // bare jamo are not syllables
    expect(chosung('')).toBe('')
  })
})

describe('isChosungQuery', () => {
  it('recognises an all-jamo query', () => {
    expect(isChosungQuery('ㅎㅌ')).toBe(true)
    expect(isChosungQuery('ㅎㅌㅂㅅㅋ')).toBe(true)
  })

  it('rejects anything a user could mean literally', () => {
    expect(isChosungQuery('')).toBe(false)
    expect(isChosungQuery('환타')).toBe(false)
    expect(isChosungQuery('ㅎ타')).toBe(false)
    expect(isChosungQuery('eyes')).toBe(false)
  })
})

describe('matchRange (highlighting only — the search itself is server-side, C-10)', () => {
  it('locates a 초성 match at its syllable offsets', () => {
    expect(matchRange('환타지스타', 'ㅎㅌ')).toEqual([0, 2])
    // '[만화] ' is five code points, so the match starts at 5 — not at 4, and
    // not at the 'ㅎ' of 만화's 화, which is followed by ']'.
    expect(matchRange('[만화] 환타지스타', 'ㅎㅌㅈ')).toEqual([5, 8])
  })

  it('locates a literal match case-insensitively', () => {
    expect(matchRange('[만화] 3X3 EYES 1~40(완)', 'eyes')).toEqual([9, 13])
  })

  it('returns null when nothing matches', () => {
    expect(matchRange('환타지스타', 'ㄲ')).toBeNull()
    expect(matchRange('환타지스타', '')).toBeNull()
    expect(matchRange('', 'ㅎ')).toBeNull()
  })

  it('reports offsets in code points, so astral characters do not shift them', () => {
    const title = '🎬 환타지스타'
    const range = matchRange(title, 'ㅎㅌ')
    expect(range).toEqual([2, 4])
    const [start, end] = range ?? [0, 0]
    expect(Array.from(title).slice(start, end).join('')).toBe('환타')
  })
})
