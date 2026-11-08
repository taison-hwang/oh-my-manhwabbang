"""check_md_links.py 테스트. 실행: python3 -m unittest discover -s <이 폴더> -p 'test_*.py'"""

import tempfile
import unittest
from pathlib import Path

from check_md_links import check_paths, github_slugs


def write(root: Path, rel: str, text: str) -> Path:
    path = root / rel
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text, encoding="utf-8")
    return path


def messages(problems):
    return [p.message for p in problems]


class GithubSlugTest(unittest.TestCase):
    def test_slug_keeps_korean_and_drops_punctuation(self):
        self.assertEqual(github_slugs(["F3. JD 입력·파싱 (URL)"]), ["f3-jd-입력파싱-url"])

    def test_duplicate_headings_get_numbered_suffix(self):
        self.assertEqual(github_slugs(["목표", "목표", "목표"]), ["목표", "목표-1", "목표-2"])

    def test_inline_markup_is_removed_before_slugging(self):
        self.assertEqual(github_slugs(["`code` 와 **굵게** [링크](x.md)"]), ["code-와-굵게-링크"])

    def test_numbered_suffix_skips_slugs_already_taken(self):
        self.assertEqual(github_slugs(["A", "A", "A-1", "A"]), ["a", "a-1", "a-1-1", "a-2"])


class CheckPathsTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)

    def tearDown(self):
        self.tmp.cleanup()

    def test_valid_links_and_anchors_report_nothing(self):
        write(self.root, "doc/index.md", "# 색인\n\n| [a](a.md) | [b](b.md#둘째-절) |\n")
        write(self.root, "doc/a.md", "# A\n\n[b 문서](./b.md) [자기 절](#a)\n")
        write(self.root, "doc/b.md", "# B\n\n## 둘째 절\n")
        self.assertEqual(check_paths([self.root / "doc"]), [])

    def test_missing_target_file_is_reported_with_line(self):
        write(self.root, "doc/index.md", "# 색인\n\n[a](a.md)\n")
        write(self.root, "doc/a.md", "# A\n\n\n[없음](nope.md)\n")
        problems = check_paths([self.root / "doc"])
        self.assertEqual(len(problems), 1)
        self.assertEqual(problems[0].line, 4)
        self.assertIn("nope.md", problems[0].message)

    def test_missing_anchor_is_reported(self):
        write(self.root, "doc/index.md", "# 색인\n\n[a](a.md#없는-절)\n")
        write(self.root, "doc/a.md", "# A\n")
        self.assertEqual(len(check_paths([self.root / "doc"])), 1)
        self.assertIn("#없는-절", messages(check_paths([self.root / "doc"]))[0])

    def test_explicit_html_anchor_counts_as_anchor(self):
        write(self.root, "doc/index.md", '# 색인\n\n[a](a.md#keep)\n')
        write(self.root, "doc/a.md", '# A\n\n<a id="keep"></a>\n')
        self.assertEqual(check_paths([self.root / "doc"]), [])

    def test_external_links_and_code_are_ignored(self):
        text = "# 색인\n\n[웹](https://x.y/z.md) [메일](mailto:a@b.c)\n`[x](nope.md)`\n```\n[y](nope.md)\n```\n"
        write(self.root, "doc/index.md", text)
        self.assertEqual(check_paths([self.root / "doc"]), [])

    def test_document_missing_from_index_is_reported(self):
        write(self.root, "doc/index.md", "# 색인\n\n[a](a.md)\n")
        write(self.root, "doc/a.md", "# A\n")
        write(self.root, "doc/orphan.md", "# 고아\n")
        problems = check_paths([self.root / "doc"])
        self.assertEqual(len(problems), 1)
        self.assertIn("index.md", problems[0].message)
        self.assertTrue(str(problems[0].path).endswith("orphan.md"))

    def test_moved_stub_is_exempt_from_index_but_its_target_must_exist(self):
        write(self.root, "doc/index.md", "# 색인\n\n[plan](plan/index.md)\n")
        write(self.root, "doc/plan.md", "<!-- moved-to: plan/index.md -->\n# 계획 (이동됨)\n")
        write(self.root, "doc/plan/index.md", "# 계획\n")
        self.assertEqual(check_paths([self.root / "doc"]), [])

    def test_moved_stub_with_missing_target_is_reported(self):
        write(self.root, "doc/index.md", "# 색인\n")
        write(self.root, "doc/plan.md", "<!-- moved-to: plan/index.md -->\n# 계획 (이동됨)\n")
        problems = check_paths([self.root / "doc"])
        self.assertEqual(len(problems), 1)
        self.assertIn("moved-to", problems[0].message)

    def test_hidden_directories_are_skipped(self):
        write(self.root, "doc/index.md", "# 색인\n")
        write(self.root, "doc/.worktrees/x/a.md", "[없음](nope.md)\n")
        self.assertEqual(check_paths([self.root / "doc"]), [])

    def test_inline_code_in_heading_keeps_its_text_in_anchor(self):
        write(self.root, "doc/a.md", "# `code` 제목\n\n[자기](#code-제목)\n")
        self.assertEqual(check_paths([self.root / "doc/a.md"]), [])

    def test_shorter_fence_inside_longer_fence_does_not_close_it(self):
        text = "# A\n\n````\n```\n[코드 안](missing-code.md)\n```\n````\n[밖](missing-real.md)\n"
        write(self.root, "doc/a.md", text)
        problems = check_paths([self.root / "doc/a.md"])
        self.assertEqual([p.line for p in problems], [8])

    def test_parentheses_and_angle_brackets_in_destination(self):
        write(self.root, "doc/notes_(draft).md", "# N\n")
        write(self.root, "doc/my file.md", "# M\n")
        write(self.root, "doc/a.md", "[n](notes_(draft).md) [m](<my file.md>) [x](<no file.md>)\n")
        problems = check_paths([self.root / "doc/a.md"])
        self.assertEqual(len(problems), 1)
        self.assertIn("no file.md", problems[0].message)

    def test_reference_style_definitions_are_checked(self):
        write(self.root, "doc/a.md", "[문서][ref]\n\n[ref]: missing.md\n[web]: https://x.y\n")
        problems = check_paths([self.root / "doc/a.md"])
        self.assertEqual([p.line for p in problems], [3])

    def test_percent_encoded_hash_is_part_of_file_name(self):
        write(self.root, "doc/topic#draft.md", "# T\n")
        write(self.root, "doc/a.md", "[t](topic%23draft.md)\n")
        self.assertEqual(check_paths([self.root / "doc/a.md"]), [])

    def test_subfolder_bundle_missing_from_parent_index_is_reported(self):
        write(self.root, "doc/index.md", "# 색인\n")
        write(self.root, "doc/plan.md", "<!-- moved-to: plan/index.md -->\n# 계획 (이동됨)\n")
        write(self.root, "doc/plan/index.md", "# 계획\n")
        problems = check_paths([self.root / "doc"])
        self.assertEqual(len(problems), 1)
        self.assertTrue(str(problems[0].path).endswith("plan/index.md"))

    def test_old_anchor_kept_in_moved_stub_resolves(self):
        stub = '<!-- moved-to: index.md -->\n# 계획 (이동됨)\n\n| <a id="옛-절"></a>옛 절 | [새](goals.md) |\n'
        write(self.root, "doc/plan.md", stub)
        write(self.root, "doc/index.md", "# 색인\n\n[goals](goals.md)\n")
        write(self.root, "doc/goals.md", "# 목표\n")
        write(self.root, "log/2026--record.md", "[옛 링크](../doc/plan.md#옛-절)\n")
        self.assertEqual(check_paths([self.root]), [])

    def test_escaped_bracket_is_not_a_link(self):
        write(self.root, "doc/a.md", "qa\\[\\](대상 요건, 질문)\n")
        self.assertEqual(check_paths([self.root / "doc/a.md"]), [])

    def test_single_file_argument_checks_only_links(self):
        write(self.root, "doc/a.md", "# A\n\n[없음](nope.md)\n")
        write(self.root, "doc/orphan.md", "# 고아\n")
        self.assertEqual(len(check_paths([self.root / "doc/a.md"])), 1)


if __name__ == "__main__":
    unittest.main()
