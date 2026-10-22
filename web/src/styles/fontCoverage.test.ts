import { readFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { brotliDecompressSync } from 'node:zlib'

import { describe, expect, it } from 'vitest'

/**
 * The vendored 한자/가나 faces must carry every character they were cut for.
 *
 * `scripts/mkfonts/subset.py` asks pyftsubset for the set in
 * `scripts/mkfonts/cjk-coverage.txt`, and internal/kenc's
 * fontcoverage_test.go holds that manifest to the ideographs kenc's codecs can
 * actually produce. This is the other half of the chain: the manifest is a
 * request, and pyftsubset drops what the source face lacks without a word. So
 * the check reads the artefact — each woff2 file's own cmap — rather than the
 * recipe that made it. A face that is missing one character draws that
 * character in whatever the reader's system has, inside a title set in 명조.
 *
 * The files are found through fonts.css: every `@font-face` whose
 * `unicode-range` claims the CJK Unified block is a face the browser will ask
 * for a 한자, so it is held to the manifest whatever it is called.
 */

const ROOT = resolve(process.cwd(), '..')
const STYLES = resolve(process.cwd(), 'src/styles')

function manifest(): Set<number> {
  const text = readFileSync(join(ROOT, 'scripts/mkfonts/cjk-coverage.txt'), 'utf8')
  const out = new Set<number>()
  for (const raw of text.split('\n')) {
    const line = raw.trim()
    if (line === '' || line.startsWith('#')) continue
    out.add(parseInt(line, 16))
  }
  return out
}

/** The url of every face that claims U+4E00, as written in fonts.css. */
function cjkFaces(): string[] {
  const css = readFileSync(join(STYLES, 'fonts.css'), 'utf8')
  const out: string[] = []
  for (const block of css.match(/@font-face\s*\{[^}]*\}/g) ?? []) {
    const range = /unicode-range:\s*([^;]+);/.exec(block)?.[1] ?? ''
    const claims = range.split(',').some((part) => {
      const m = /U\+([0-9A-F]+)(?:-([0-9A-F]+))?/i.exec(part.trim())
      if (!m) return false
      const lo = parseInt(m[1] ?? '', 16)
      const hi = m[2] ? parseInt(m[2], 16) : lo
      return lo <= 0x4e00 && 0x4e00 <= hi
    })
    const url = /url\('\.\.\/([^']+)'\)/.exec(block)?.[1]
    if (claims && url) out.push(url)
  }
  return out
}

/**
 * The Unicode subtables of a WOFF2 file's cmap, each with every code point it
 * maps to a glyph the font actually has.
 *
 * WOFF2 (W3C, §4–5): a 48-byte header, a table directory with UIntBase128
 * lengths, then one brotli stream holding the tables back to back in directory
 * order. cmap and maxp are never transformed, so they are read straight out of
 * the stream; the tables before them only need their stored lengths skipped.
 *
 * Subtables are returned separately, not merged: a browser draws from one of
 * them (format 12 when present, else format 4), so a character the format-4
 * table keeps cannot cover for one the format-12 table lost. A glyph id at or
 * past maxp's numGlyphs is no glyph, and is not counted.
 */
function woff2Cmaps(file: Buffer): { label: string; cps: Set<number> }[] {
  if (file.readUInt32BE(0) !== 0x774f4632) throw new Error('not a WOFF2 file')
  if (file.readUInt32BE(4) === 0x74746366) throw new Error('font collections are not supported')
  const numTables = file.readUInt16BE(12)
  const compressedLength = file.readUInt32BE(20)

  let p = 48
  const base128 = (): number => {
    let v = 0
    for (let i = 0; i < 5; i++) {
      const b = file.readUInt8(p++)
      v = v * 128 + (b & 0x7f)
      if ((b & 0x80) === 0) return v
    }
    throw new Error('UIntBase128 longer than five bytes')
  }

  let offset = 0
  const at = new Map<number, { offset: number; length: number }>()
  for (let i = 0; i < numTables; i++) {
    const flags = file.readUInt8(p++)
    const known = flags & 0x3f
    if (known === 63) p += 4 // an arbitrary tag spelled out
    const version = flags >> 6
    const length = base128()
    // glyf (10) and loca (11) are transformed unless version 3; every other
    // table is transformed unless version 0. A transformed table is stored at
    // its transformLength.
    const transformed = known === 10 || known === 11 ? version !== 3 : version !== 0
    const stored = transformed ? base128() : length
    if (known !== 63) at.set(known, { offset, length: stored })
    offset += stored
  }
  const cmap = at.get(0)
  const maxp = at.get(4)
  if (!cmap || !maxp) throw new Error('no cmap or no maxp table')

  const tables = brotliDecompressSync(file.subarray(p, p + compressedLength))
  const numGlyphs = tables.readUInt16BE(maxp.offset + 4)
  const real = (glyph: number): boolean => glyph !== 0 && glyph < numGlyphs
  const t = tables.subarray(cmap.offset, cmap.offset + cmap.length)

  const out: { label: string; cps: Set<number> }[] = []
  for (let i = 0; i < t.readUInt16BE(2); i++) {
    const platform = t.readUInt16BE(4 + i * 8)
    const encoding = t.readUInt16BE(4 + i * 8 + 2)
    if (!(platform === 0 || (platform === 3 && (encoding === 1 || encoding === 10)))) continue
    const sub = t.subarray(t.readUInt32BE(4 + i * 8 + 4))
    const format = sub.readUInt16BE(0)
    const cps = new Set<number>()
    if (format === 4) {
      const segs = sub.readUInt16BE(6) / 2
      const ends = 14
      const starts = ends + segs * 2 + 2
      const deltas = starts + segs * 2
      const rangeOffsets = deltas + segs * 2
      for (let s = 0; s < segs; s++) {
        const end = sub.readUInt16BE(ends + s * 2)
        const start = sub.readUInt16BE(starts + s * 2)
        const delta = sub.readUInt16BE(deltas + s * 2)
        const ro = sub.readUInt16BE(rangeOffsets + s * 2)
        for (let c = start; c <= end && c !== 0xffff; c++) {
          let glyph: number
          if (ro === 0) {
            glyph = (c + delta) & 0xffff
          } else {
            glyph = sub.readUInt16BE(rangeOffsets + s * 2 + ro + (c - start) * 2)
            if (glyph !== 0) glyph = (glyph + delta) & 0xffff
          }
          if (real(glyph)) cps.add(c)
        }
      }
    } else if (format === 12) {
      const groups = sub.readUInt32BE(12)
      for (let g = 0; g < groups; g++) {
        const row = 16 + g * 12
        const start = sub.readUInt32BE(row)
        const end = sub.readUInt32BE(row + 4)
        const glyph = sub.readUInt32BE(row + 8)
        for (let c = start; c <= end; c++) if (real(glyph + (c - start))) cps.add(c)
      }
    } else {
      continue
    }
    out.push({ label: `(${String(platform)},${String(encoding)}) format ${String(format)}`, cps })
  }
  return out
}

describe('vendored CJK faces', () => {
  const want = manifest()
  const faces = cjkFaces()

  it('are the four E-55 cuts, and the manifest is the derived set rather than a sample', () => {
    // Two families × two weights (E-55). Fewer means the regex above stopped
    // finding them, and the loop below would then check nothing.
    expect(faces).toHaveLength(4)
    // 4 888 (CP949) ∪ 6 716 (Windows-31J) ideographs plus the assigned kana.
    // The exact figure is the Go test's business; this only refuses a
    // manifest so small that "every character present" would mean little.
    expect(want.size).toBeGreaterThan(7000)
  })

  it.each(cjkFaces())('%s carries every character in cjk-coverage.txt', (url) => {
    const cmaps = woff2Cmaps(readFileSync(join(STYLES, '..', url)))
    expect(cmaps.length, `${url} has no Unicode cmap subtable`).toBeGreaterThan(0)
    for (const { label, cps } of cmaps) {
      const missing = [...want].filter((c) => !cps.has(c))
      expect(
        missing.slice(0, 12).map((c) => `${String.fromCodePoint(c)}(U+${c.toString(16).toUpperCase()})`),
        `${String(missing.length)} requested characters have no glyph in ${url}'s ${label} cmap`,
      ).toEqual([])
    }
  })
})
