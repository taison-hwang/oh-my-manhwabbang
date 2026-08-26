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
such character occurs in the 18 729 names on this machine.

`--font-ui` draws the numerals and the uppercase micro-labels (E-46: 명조
numerals are proportional, so a tabular column drawn in them does not line up).
It was `ui-sans-serif, 'Helvetica Neue', Arial` — every one of them a system
font, so those labels were the last thing in the product still rendering
differently on every device. Archivo is the face the skin before this one used
and it is still a dependency, so its own latin subsets are vendored verbatim.
"""

import argparse
import os
import shutil
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
OUT = ROOT / "web/src/assets/fonts"
ARCHIVO = ROOT / (
    "web/node_modules/.pnpm/@fontsource+archivo@5.2.6"
    "/node_modules/@fontsource/archivo/files"
)
NOTO = Path("/usr/share/fonts/opentype/noto")

# CJK blocks the vendored Han/kana face answers for. Hangul is deliberately
# absent: 고운바탕 owns it, and letting this face claim it would replace the
# skin's 명조 Hangul with Noto's.
UNICODE_RANGE_BLOCKS = [
    (0x3040, 0x309F),  # Hiragana
    (0x30A0, 0x30FF),  # Katakana
    (0x3400, 0x4DBF),  # CJK Extension A
    (0x4E00, 0x9FFF),  # CJK Unified Ideographs
    (0xF900, 0xFAFF),  # CJK Compatibility Ideographs
]


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
    return {chr(c) for c in range(0x3040, 0x3100)}


def kr_font_number(ttc: Path) -> int:
    from fontTools.ttLib import TTCollection

    coll = TTCollection(str(ttc))
    for i, f in enumerate(coll.fonts):
        if "KR" in str(f["name"].getDebugName(4)):
            return i
    raise SystemExit(f"{ttc}: no Korean face inside")


def subset(src: Path, font_number: int, chars: set[str], dest: Path) -> None:
    text = ROOT / "web/src/assets/fonts/.chars.tmp"
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

    plan: list[tuple[Path, Path, int, set[str] | None]] = []
    for weight, style in (("Regular", "400"), ("Bold", "700")):
        ttc = NOTO / f"NotoSerifCJK-{weight}.ttc"
        if not ttc.exists():
            print(f"missing source: {ttc}", file=sys.stderr)
            return 2
        plan.append((ttc, OUT / f"noto-serif-kr-han-{style}.woff2", kr_font_number(ttc), chars))

    total = 0
    for src, dest, num, cs in plan:
        target = dest if not args.check else Path(str(dest) + ".check")
        assert cs is not None
        subset(src, num, cs, target)
        size = target.stat().st_size
        total += size
        if args.check:
            old = dest.read_bytes() if dest.exists() else b""
            same = old == target.read_bytes()
            target.unlink()
            print(f"  {dest.name}: {size} B {'ok' if same else 'DIFFERS'}")
            if not same:
                return 1
        else:
            print(f"  {dest.name}: {size} B")

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

    print(f"total added: {total} B ({total / 1e6:.2f} MB)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
