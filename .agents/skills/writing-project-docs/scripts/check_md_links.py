#!/usr/bin/env python3
"""마크다운 문서 묶음의 상대 링크·앵커·색인을 검사한다.

사용: python3 check_md_links.py <폴더 또는 .md 파일>...

- 상대 링크의 대상 파일과 `#앵커`(GitHub 방식 제목 앵커, <a id|name>)가 있는지 본다.
- 폴더 인자: 하위 .md 전체를 검사하고(숨김 폴더 제외), index.md가 있는 폴더마다
  같은 폴더의 다른 .md가 index.md에서 링크되는지 본다. 옛 경로 안내 문서
  (첫 줄 `<!-- moved-to: <상대 경로> -->`)는 색인 대상에서 빼고 이동 대상이 있는지 본다.
- 문제는 `파일:줄: 메시지`로 출력하고 종료 코드 1, 없으면 0.
"""

from __future__ import annotations

import re
import sys
from dataclasses import dataclass
from pathlib import Path
from urllib.parse import unquote

INLINE_LINK_START = re.compile(r"(?<!\\)\]\(\s*")  # \]( 는 링크가 아니다
REFERENCE_DEFINITION = re.compile(r"^ {0,3}\[[^\]]+\]:\s*(<[^>]*>|\S+)")
HEADING = re.compile(r"^ {0,3}(#{1,6})\s+(.*?)\s*#*\s*$")
HTML_ANCHOR = re.compile(r"<a\s+[^>]*(?:id|name)\s*=\s*[\"']([^\"']+)[\"']", re.IGNORECASE)
FENCE = re.compile(r"^ {0,3}(`{3,}|~{3,})(.*)$")
INLINE_CODE = re.compile(r"`[^`]*`")
MOVED_TO = re.compile(r"^<!--\s*moved-to:\s*(\S+)\s*-->")
SCHEME = re.compile(r"^[a-zA-Z][a-zA-Z0-9+.-]*:")


@dataclass(frozen=True)
class Problem:
    path: Path
    line: int
    message: str

    def __str__(self) -> str:
        return f"{self.path}:{self.line}: {self.message}"


def github_slugs(headings: list[str]) -> list[str]:
    """제목 목록을 GitHub가 만드는 앵커 목록으로 바꾼다(중복은 -1, -2 접미사)."""
    occurrences: dict[str, int] = {}  # github-slugger와 같은 방식: 붙인 접미사도 사용 중으로 친다
    slugs = []
    for text in headings:
        text = re.sub(r"\[([^\]]*)\]\([^)]*\)", r"\1", text)
        base = slug = re.sub(r"[^\w\- ]", "", text.lower()).replace(" ", "-")
        while slug in occurrences:
            occurrences[base] += 1
            slug = f"{base}-{occurrences[base]}"
        occurrences[slug] = 0
        slugs.append(slug)
    return slugs


def _prose_lines(text: str):
    """코드 블록 밖의 (줄 번호, 줄). 닫는 펜스는 여는 펜스와 같은 문자로 같거나 더 길어야 한다."""
    fence = ""
    for number, line in enumerate(text.splitlines(), start=1):
        match = FENCE.match(line)
        if fence:
            if match and match.group(1)[0] == fence[0] and len(match.group(1)) >= len(fence) \
                    and not match.group(2).strip():
                fence = ""
            continue
        if match:
            fence = match.group(1)
            continue
        yield number, line


def _destinations(line: str):
    """줄 안 인라인 링크·이미지의 대상. <...>와 균형 잡힌 괄호를 허용한다."""
    for start in INLINE_LINK_START.finditer(line):
        rest = line[start.end():]
        if rest.startswith("<"):
            end = rest.find(">")
            if end > 0:
                yield rest[1:end]
            continue
        depth, chars = 0, []
        for ch in rest:
            if ch.isspace() or (ch == ")" and depth == 0):
                break
            depth += {"(": 1, ")": -1}.get(ch, 0)
            chars.append(ch)
        if chars:
            yield "".join(chars)


def anchors_of(path: Path) -> set[str]:
    text = path.read_text(encoding="utf-8")
    headings, explicit = [], set()
    for _, line in _prose_lines(text):  # 제목의 인라인 코드는 글자를 남긴다(GitHub와 같음)
        match = HEADING.match(line)
        if match:
            headings.append(match.group(2))
        explicit.update(HTML_ANCHOR.findall(line))
    return set(github_slugs(headings)) | explicit


def links_of(path: Path):
    """(줄 번호, 링크 대상) — 외부 링크는 뺀다."""
    for number, line in _prose_lines(path.read_text(encoding="utf-8")):
        line = INLINE_CODE.sub("", line)
        targets = list(_destinations(line))
        definition = REFERENCE_DEFINITION.match(line)
        if definition:
            targets.append(definition.group(1).strip("<>"))
        for target in targets:
            if not SCHEME.match(target):
                yield number, target


def split_target(target: str) -> tuple[str, str]:
    """(파일 부분, 앵커) — #으로 먼저 나눈 뒤 퍼센트 인코딩을 푼다(%23은 파일 이름의 #)."""
    file_part, _, anchor = target.partition("#")
    return unquote(file_part), unquote(anchor)


def moved_to(path: Path) -> str | None:
    first = path.read_text(encoding="utf-8").split("\n", 1)[0]
    match = MOVED_TO.match(first)
    return match.group(1) if match else None


def check_links(path: Path, anchor_cache: dict[Path, set[str]]) -> list[Problem]:
    problems = []
    for number, target in links_of(path):
        file_part, anchor = split_target(target)
        dest = (path.parent / file_part).resolve() if file_part else path.resolve()
        if not dest.exists():
            problems.append(Problem(path, number, f"링크 대상 없음: {target}"))
            continue
        if anchor and dest.suffix == ".md":
            if dest not in anchor_cache:
                anchor_cache[dest] = anchors_of(dest)
            if anchor not in anchor_cache[dest]:
                problems.append(Problem(path, number, f"앵커 없음: {target}"))
    target = moved_to(path)
    if target and not (path.parent / target).exists():
        problems.append(Problem(path, 1, f"moved-to 대상 없음: {target}"))
    return problems


def check_index(index: Path) -> list[Problem]:
    linked = set()
    for _, target in links_of(index):
        file_part, _ = split_target(target)
        if file_part:
            dest = (index.parent / file_part).resolve()
            linked.add(dest / "index.md" if dest.is_dir() else dest)
    problems = []
    for doc in sorted(index.parent.glob("*.md")):
        if doc.name == "index.md" or moved_to(doc) or doc.resolve() in linked:
            continue
        problems.append(Problem(doc, 1, f"{index}의 목차에 없는 문서"))
    for sub_index in sorted(index.parent.glob("*/index.md")):
        if sub_index.parent.name.startswith(".") or sub_index.resolve() in linked:
            continue
        problems.append(Problem(sub_index, 1, f"{index}의 목차에 없는 하위 문서 묶음"))
    return problems


def markdown_files(folder: Path) -> list[Path]:
    return sorted(
        p for p in folder.rglob("*.md")
        if not any(part.startswith(".") for part in p.relative_to(folder).parts)
    )


def check_paths(paths: list[Path]) -> list[Problem]:
    problems: list[Problem] = []
    anchor_cache: dict[Path, set[str]] = {}
    for given in paths:
        files = markdown_files(given) if given.is_dir() else [given]
        for doc in files:
            problems.extend(check_links(doc, anchor_cache))
            if given.is_dir() and doc.name == "index.md":
                problems.extend(check_index(doc))
    return problems


def main(argv: list[str]) -> int:
    if not argv:
        print(__doc__.strip(), file=sys.stderr)
        return 2
    problems = check_paths([Path(a) for a in argv])
    for problem in problems:
        print(problem)
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
