import contextlib
import io
import json
import os
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import usage_report as ur  # noqa: E402

REPO = "/fake/repo"
SINCE = "2026-11-08T22:13:38+09:00"
BEFORE = "2026-11-08T13:00:00Z"  # 기준 시점(13:13:38Z) 전
AFTER1 = "2026-11-08T15:30:00Z"  # KST 11-09 00:30
AFTER2 = "2026-11-09T01:00:00Z"


def write_jsonl(path, lines):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w", encoding="utf-8") as fh:
        for d in lines:
            fh.write(json.dumps(d, ensure_ascii=False) + "\n")


def role_line(role):
    return {"type": "attachment", "attachment": {"type": "hook_additional_context",
            "content": ["[herdr 역할] 이 세션의 역할은 %s, 프로젝트는 fake 다." % role]}}


def asst(mid, ts, cwd, model="claude-opus-5-5", c5=0, c1h=0, inp=1, out=10, cr=100, uuid=None):
    return {"type": "assistant", "timestamp": ts, "cwd": cwd, "uuid": uuid or mid + "-u",
            "message": {"id": mid, "model": model, "usage": {
                "input_tokens": inp, "output_tokens": out, "cache_read_input_tokens": cr,
                "cache_creation_input_tokens": c5 + c1h,
                "cache_creation": {"ephemeral_5m_input_tokens": c5, "ephemeral_1h_input_tokens": c1h}}}}


def rollout(path, cwd, model, records, total):
    lines = [{"type": "session_meta", "timestamp": AFTER1, "payload": {"id": "s-" + os.path.basename(path), "cwd": cwd}},
             {"type": "turn_context", "timestamp": AFTER1, "payload": {"model": model, "cwd": cwd}}]
    for rid, ts, u in records:
        lines.append({"type": "token_usage_record", "timestamp": ts, "payload": {"response_id": rid, "usage": {
            "input_tokens": u[0], "cached_input_tokens": u[1], "output_tokens": u[2], "reasoning_output_tokens": u[3]}}})
    lines.append({"type": "event_msg", "timestamp": AFTER2, "payload": {"type": "token_count", "info": {"total_token_usage": {
        "input_tokens": total[0], "cached_input_tokens": total[1], "output_tokens": total[2], "reasoning_output_tokens": total[3]}}}})
    write_jsonl(path, lines)


def run(argv):
    buf = io.StringIO()
    with contextlib.redirect_stdout(buf):
        ur.main(argv)
    return buf.getvalue()


class UsageReportTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        t = self.tmp.name
        self.claude = os.path.join(t, "claude")
        self.codex = os.path.join(t, "codex")
        wt = REPO + "/.worktrees/job-a"
        write_jsonl(os.path.join(self.claude, "sess1.jsonl"), [
            role_line("worker-9"),
            asst("m1", AFTER1, wt, c1h=50),
            asst("m1", AFTER1, wt, c1h=50, uuid="dup"),  # 같은 응답의 content block 반복
            asst("m2", AFTER2, REPO, model="claude-sonnet-5-5", c5=20, cr=7),
            asst("m3", AFTER2, REPO, model="claude-mystery-9"),
            asst("old", BEFORE, REPO),
            asst("far", AFTER2, "/elsewhere"),
        ])
        write_jsonl(os.path.join(self.claude, "sess1", "subagents", "agent-x.jsonl"), [asst("s1", AFTER2, wt, c5=5)])
        write_jsonl(os.path.join(self.claude, "sess2.jsonl"), [asst("n1", AFTER2, REPO)])
        os.makedirs(os.path.join(self.claude, "ghost", "tool-results"))
        rollout(os.path.join(self.codex, "2026", "11", "09", "rollout-a.jsonl"), REPO + "/", "gpt-x",
                [("r1", AFTER1, (100, 60, 10, 4)), ("r1", AFTER1, (100, 60, 10, 4)), ("r2", AFTER2, (50, 0, 5, 1)),
                 ("r0", BEFORE, (7, 7, 7, 7))], (157, 67, 22, 12))
        rollout(os.path.join(self.codex, "2026", "11", "09", "rollout-other.jsonl"), "/other/project", "gpt-x",
                [("z", AFTER1, (999, 0, 999, 0))], (999, 0, 999, 0))
        self.ledger = os.path.join(t, "ledger.md")
        with open(self.ledger, "w", encoding="utf-8") as fh:
            fh.write("| role | 터미널 | 도구·모델 |\n| --- | --- | --- |\n| worker-9 | t1 | claude·x |\n"
                     "| worker-8 | t2 | claude·x |\n| (옛) worker-3 | t3 | claude |\n")
        self.args = ["--since", SINCE, "--repo", REPO, "--claude-dir", self.claude, "--codex-dir", self.codex,
                     "--ledger", self.ledger, "--no-proc", "--format", "json"]

    def tearDown(self):
        self.tmp.cleanup()

    def report(self, extra=()):
        return json.loads(run(self.args + list(extra)))

    def test_dedup_since_and_tokens(self):
        rep = self.report()
        by = {tuple(e["key"]): e for e in rep["claude_by_role_model"]}
        opus = by[("worker-9", "claude-opus-5-5")]
        self.assertEqual(opus["responses"], 3)  # m1(중복 제거), far, s1 — old는 기준 시점 전
        self.assertEqual(opus["cache_1h"], 50)
        self.assertEqual(opus["cache_5m"], 5)
        self.assertEqual(by[("worker-9", "claude-sonnet-5-5")]["cache_read"], 7)

    def test_dedup_counts_reported(self):
        gap = next(g for g in self.report()["gaps"] if g.startswith("Claude 중복 제거"))
        self.assertEqual(gap, "Claude 중복 제거: assistant usage 줄 7 → message.id 고유 6")

    def test_task_mapping_from_cwd(self):
        rep = self.report()
        tasks = {tuple(e["key"]) for e in rep["claude_by_task"]}
        self.assertIn(("worker-9", "job-a"), tasks)
        self.assertIn(("worker-9", ur.MAIN_CHECKOUT), tasks)
        self.assertIn(("worker-9", ur.OUTSIDE), tasks)
        self.assertIn((ur.UNKNOWN_ROLE, ur.MAIN_CHECKOUT), tasks)  # 역할 문구가 없는 sess2

    def test_gaps_show_missing_roles_and_traces(self):
        text = "\n".join(self.report()["gaps"])
        self.assertIn("worker-8(claude): 기준 시점 이후 jsonl 사용량 없음", text)
        self.assertNotIn("(옛)", text)
        self.assertIn("ghost", text)
        self.assertIn("앞", text)  # 기준 시점 이후 첫 기록 전 공백
        self.assertIn("과거 누락분은 추정치로 채우지 않았다", text)
        self.assertIn(".worktrees/<이름>", text)

    def test_unpriced_model_blank_cost(self):
        rep = self.report()
        mystery = [e for e in rep["claude_by_role_model"] if e["key"][1] == "claude-mystery-9"]
        self.assertIsNone(mystery[0]["cost_usd"])
        self.assertEqual(rep["claude_unpriced_models"], ["claude-mystery-9"])
        opus = next(e for e in rep["claude_by_role_model"] if e["key"][1] == "claude-opus-5-5")
        # opus: input 3*4 + output 30*20 + 5m 5*5 + 1h 50*8 + read 300*0.2, 모두 /1e6
        self.assertAlmostEqual(opus["cost_usd"], (3 * 4 + 30 * 20 + 5 * 5 + 50 * 8 + 300 * 0.2) / 1e6)
        text = run([a for a in self.args if a not in ("json", "--format")])
        self.assertIn("claude-mystery-9", text)
        self.assertIn("API 단가 환산 추정, 구독이면 실제 청구액 아님", text)

    def test_codex_dedup_filter_crosscheck_and_no_cost(self):
        rep = self.report()
        rows = rep["codex_by_role_model"]
        self.assertEqual(len(rows), 1)  # /other/project rollout은 제외
        e = rows[0]
        self.assertEqual((e["responses"], e["input"], e["cached"], e["output"], e["reasoning"]), (2, 150, 60, 15, 5))
        self.assertEqual(e["key"], [ur.UNKNOWN_ROLE, "gpt-x"])
        self.assertNotIn("cost_usd", e)
        self.assertIn("일치 1/1", " ".join(rep["gaps"]))
        text = run([a for a in self.args if a not in ("json", "--format")])
        self.assertIn("Codex 단가 미확인 — 비용 미산출", text)

    def test_codex_role_is_marked_as_estimate(self):
        rep = self.report(["--codex-role", "gpt-x=verifier"])
        self.assertEqual(rep["codex_by_role_model"][0]["key"][0], "추정:verifier")

    def test_claude_and_codex_are_never_merged(self):
        rep = self.report()
        self.assertTrue(all("cache_read" not in e for e in rep["codex_by_day"]))
        self.assertTrue(all("cached" not in e for e in rep["claude_by_day"]))

    def test_read_only(self):
        before = {p: os.stat(p).st_mtime_ns for p in self._files()}
        run(self.args)
        self.assertEqual(before, {p: os.stat(p).st_mtime_ns for p in self._files()})
        self.assertEqual(set(before), set(self._files()))

    def _files(self):
        return [os.path.join(d, f) for d, _, fs in os.walk(self.tmp.name) for f in fs]


if __name__ == "__main__":
    unittest.main()
