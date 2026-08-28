#!/usr/bin/env python3
"""Rebuild the vendored webfont subsets in web/src/assets/fonts/.

Why this script exists
----------------------
Every face this product draws with is vendored (E-46, NFR-OPS-001/002: no
runtime CDN, one binary). Until now the subsets were produced by hand and only
described in prose, so nobody could reproduce them or check what coverage they
actually had. This is that recipe, executable.

It is **not** part of `make build`. The outputs are committed, the inputs are
system fonts and an npm package, and a font that silently changes between
builds is a worse problem than one that has to be regenerated deliberately.
Run it when the coverage rule below changes, then commit the result.

    python3 scripts/mkfonts/subset.py            # rebuild, report sizes
    python3 scripts/mkfonts/subset.py --check    # verify, change nothing

Coverage
--------
`고운바탕` carries all 11 172 modern Hangul syllables and latin but **no Han and
no kana at all**. Those are the gaps, and until they were vendored they fell
through to `Noto Serif KR` / `Apple SD Gothic Neo` / `serif` — which is to say,
to whatever the reader's device happened to have, or to a tofu box.

The Han/kana subset is not a sample of the current library. Book and series
names come from filenames, and internal/kenc decodes those as UTF-8, CP949 or
Shift_JIS, so the ideographs a *legacy-encoded* name can hold are exactly the
union of what those two encodings can represent: 4 888 + 6 356 = 7 159, plus
the two kana blocks entire. That is a closed set derived from the encodings
rather than a snapshot, so a volume added tomorrow is already covered.

The one hole left is a name stored as UTF-8 carrying an ideograph outside both
legacy sets. That still falls back exactly as everything did before, and no
such character occurs in the 18 721 names on this machine.

Two CJK faces, not one (E-55)
-----------------------------
Until E-55 that closed set was cut once, out of **Noto Serif CJK KR**, and one
family answered for every 한자 and every 가나 in the product. E-55 splits it in
two because the two scripts are no longer asked to look alike:

  * `noto-serif-tc-*` — **Noto Serif CJK TC**. The default 한자 face. 서고 is a
    명조 skin and a 한자 inside a Korean title is set in 명조; the regional cut
    moves from KR to TC because the traditional forms are the ones a Korean
    title's 한자 is written in.
  * `noto-sans-jp-*` — **Noto Sans CJK JP**. Reached only through `[lang='ja']`
    (`textLang.ts` tags a name as Japanese when it carries kana), so a Japanese
    title is set entirely in one face rather than split down the middle into
    명조 kanji and 고딕 kana.

Both carry the **same** closed set — Han ∪ kana. The JP face needs the kanji
because a Japanese title is mostly kanji; the TC face keeps the kana so that
any name this repo forgets to tag still renders rather than showing tofu, which
is what a `unicode-range` hole looks like on screen.

`--font-ui` draws the numerals and the uppercase micro-labels (E-46: 명조
numerals are proportional, so a tabular column drawn in them does not line up).
It was `ui-sans-serif, 'Helvetica Neue', Arial` — every one of them a system
font, so those labels were the last thing in the product still rendering
differently on every device. Archivo is the face the skin before this one used
and it is still a dependency, so its own latin subsets are vendored verbatim.
"""

import argparse
import shutil
import subprocess
import sys
import unicodedata
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
OUT = ROOT / "web/src/assets/fonts"
ARCHIVO = ROOT / (
    "web/node_modules/.pnpm/@fontsource+archivo@5.2.6"
    "/node_modules/@fontsource/archivo/files"
)
NOTO = Path("/usr/share/fonts/opentype/noto")

# CJK blocks the vendored Han/kana faces answer for. Hangul is deliberately
# absent: 고운바탕 owns it, and letting either face claim it would replace the
# skin's 명조 Hangul with Noto's — including inside a Japanese-tagged name,
# which on this library is usually a Korean title with a Japanese fragment in
# it rather than a Japanese one.
UNICODE_RANGE_BLOCKS = [
    (0x3040, 0x309F),  # Hiragana
    (0x30A0, 0x30FF),  # Katakana
    (0x3400, 0x4DBF),  # CJK Extension A
    (0x4E00, 0x9FFF),  # CJK Unified Ideographs
    (0xF900, 0xFAFF),  # CJK Compatibility Ideographs
]

# (collection, face name inside it, output stem). Both faces are cut from the
# same closed character set; see the module docstring for why there are two.
CJK_FACES = [
    ("NotoSerifCJK", "Noto Serif CJK TC", "noto-serif-tc"),
    ("NotoSansCJK", "Noto Sans CJK JP", "noto-sans-jp"),
]

# Cut by an earlier revision of this rule and no longer referenced by
# fonts.css. Listed so a rebuild says so out loud rather than leaving 3 MB of
# orphan in the binary for the next reader to discover.
RETIRED = ["noto-serif-kr-han-400.woff2", "noto-serif-kr-han-700.woff2"]


def encodable_ideographs() -> set[str]:
    """Every ideograph CP949 or Shift_JIS can represent.

    Derived by round-tripping the encodings rather than from a table, so it
    cannot drift from what internal/kenc will actually decode.
    """
    out: set[str] = set()
    for enc in ("cp949", "shift_jis"):
        for hi in range(0x81, 0x100):
            for lo in range(0x30, 0x100):
                try:
                    ch = bytes([hi, lo]).decode(enc)
                except (UnicodeDecodeError, LookupError):
                    continue
                if len(ch) != 1:
                    continue
                o = ord(ch)
                if (
                    0x4E00 <= o <= 0x9FFF
                    or 0x3400 <= o <= 0x4DBF
                    or 0xF900 <= o <= 0xFAFF
                ):
                    out.add(ch)
    return out


def kana() -> set[str]:
    """Every **assigned** character in the two kana blocks.

    `U+3040`, `U+3097` and `U+3098` are reserved holes inside the hiragana
    block. Asking for them cost nothing while the request was never checked —
    pyftsubset drops what a face does not have and says so to nobody — but no
    font on earth carries them, so a literal `range(0x3040, 0x3100)` makes
    `missing_glyphs` report three phantom gaps on every cut. Filtered by
    `unicodedata` rather than by a hard-coded triple, so the next block
    revision does not need this comment rewritten.
    """
    return {
        chr(c) for c in range(0x3040, 0x3100) if unicodedata.name(chr(c), "") != ""
    }


def face_number(ttc: Path, family: str) -> int:
    """Index of `family` inside a .ttc.

    Matched on a prefix of the full name so that `NotoSerifCJK-Bold.ttc`'s
    "Noto Serif CJK TC Bold" resolves the same way `-Regular.ttc`'s
    "Noto Serif CJK TC" does — and so that TC never matches SC or HK.
    """
    from fontTools.ttLib import TTCollection

    coll = TTCollection(str(ttc))
    for i, f in enumerate(coll.fonts):
        if str(f["name"].getDebugName(4)).startswith(family):
            return i
    raise SystemExit(f"{ttc}: no face named {family!r} inside")


def subset(src: Path, font_number: int, chars: set[str], dest: Path) -> None:
    text = OUT / ".chars.tmp"
    text.write_text("".join(sorted(chars)), encoding="utf-8")
    try:
        subprocess.run(
            [
                "pyftsubset",
                str(src),
                f"--font-number={font_number}",
                f"--text-file={text}",
                "--flavor=woff2",
                "--layout-features=",
                "--no-hinting",
                "--desubroutinize",
                f"--output-file={dest}",
            ],
            check=True,
            capture_output=True,
        )
    finally:
        text.unlink(missing_ok=True)


def missing_glyphs(woff2: Path, chars: set[str]) -> set[str]:
    """Requested characters the cut file does not actually carry.

    pyftsubset drops what the source face does not have and says nothing, so a
    swap of source face (KR → TC, or Serif → Sans) could quietly open a hole
    that only shows up as tofu on a reader's screen. Read the result's cmap
    back and compare: the check has to look at the artefact, not the request.
    """
    from fontTools.ttLib import TTFont

    font = TTFont(str(woff2))
    have = set()
    for table in font["cmap"].tables:
        have |= {chr(cp) for cp in table.cmap}
    return chars - have


def unicode_range_css() -> str:
    return ", ".join(f"U+{a:04X}-{b:04X}" for a, b in UNICODE_RANGE_BLOCKS)


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--check", action="store_true", help="verify only")
    args = ap.parse_args()

    if shutil.which("pyftsubset") is None:
        print("pyftsubset not found: pip install --user fonttools brotli", file=sys.stderr)
        return 2

    chars = encodable_ideographs() | kana()
    print(f"CJK coverage: {len(chars)} codepoints")
    print(f"unicode-range: {unicode_range_css()}")

    plan: list[tuple[Path, Path, str]] = []
    for collection, family, stem in CJK_FACES:
        for weight, style in (("Regular", "400"), ("Bold", "700")):
            ttc = NOTO / f"{collection}-{weight}.ttc"
            if not ttc.exists():
                print(f"missing source: {ttc}", file=sys.stderr)
                return 2
            plan.append((ttc, OUT / f"{stem}-{style}.woff2", family))

    total = 0
    for src, dest, family in plan:
        target = dest if not args.check else Path(str(dest) + ".check")
        subset(src, face_number(src, family), chars, target)
        size = target.stat().st_size
        total += size
        gap = missing_glyphs(target, chars)
        note = "" if not gap else f"  MISSING {len(gap)}: {''.join(sorted(gap)[:8])}"
        if args.check:
            old = dest.read_bytes() if dest.exists() else b""
            same = old == target.read_bytes()
            target.unlink()
            print(f"  {dest.name}: {size} B {'ok' if same else 'DIFFERS'}{note}")
            if not same or gap:
                return 1
        else:
            print(f"  {dest.name}: {size} B{note}")
            if gap:
                return 1

    # Archivo latin, copied rather than subsetted: @fontsource already ships a
    # latin-only cut at 14 KB per weight, and re-cutting it would trade a
    # reproducible upstream artefact for one of ours for no measurable gain.
    for weight in ("400", "700"):
        src = ARCHIVO / f"archivo-latin-{weight}-normal.woff2"
        dest = OUT / f"archivo-latin-{weight}.woff2"
        if not src.exists():
            print(f"missing source: {src} (run pnpm install in web/)", file=sys.stderr)
            return 2
        data = src.read_bytes()
        total += len(data)
        if args.check:
            same = dest.exists() and dest.read_bytes() == data
            print(f"  {dest.name}: {len(data)} B {'ok' if same else 'DIFFERS'}")
            if not same:
                return 1
        else:
            dest.write_bytes(data)
            print(f"  {dest.name}: {len(data)} B")

    orphans = [n for n in RETIRED if (OUT / n).exists()]
    if orphans:
        print(f"retired, still on disk (delete them): {', '.join(orphans)}", file=sys.stderr)
        if args.check:
            return 1

    print(f"total added: {total} B ({total / 1e6:.2f} MB)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
