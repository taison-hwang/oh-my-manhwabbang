#!/usr/bin/env python3
"""하네스 적용 이후 토큰 사용량 집계 (읽기 전용).

실행: python3 -I tools/usage/usage_report.py [--format text|tsv|json]
Claude 프로젝트 jsonl(메인 + subagents)과 Codex rollout(session_meta.cwd가 이 저장소인 것)만 읽는다.
기록 파일은 열어서 읽기만 하고, 두 체계(Claude/Codex)의 토큰은 합치지 않는다.
"""
import argparse
import collections
import datetime as dt
import glob
import json
import os
import re
import sys
import unicodedata

HERE = os.path.dirname(os.path.abspath(__file__))
KST = dt.timezone(dt.timedelta(hours=9))
DEFAULT_SINCE = "2026-11-08T22:13:38+09:00"
MAIN_CHECKOUT = "(본 체크아웃)"
OUTSIDE = "(저장소 밖)"
UNKNOWN_ROLE = "미상"
ROLE_RE = re.compile(r"이 세션의 역할은 ([^,\s]+), 프로젝트는")
WORKTREE_RE = re.compile(r"/\.worktrees/([^/]+)")

COST_ESTIMATE_NOTE = "API 단가 환산 추정, 구독이면 실제 청구액 아님"
UNPRICED_NOTE = "단가표에 없는 모델은 공식 단가를 확인하지 못해 비용 칸을 비우고 합계에서 제외"
CODEX_NOTE = "Codex 단가 미확인 — 비용 미산출"
CLAUDE_FIELDS = ("input", "output", "cache_5m", "cache_1h", "cache_unsplit", "cache_read")
CODEX_FIELDS = ("input", "cached", "output", "reasoning")


def parse_ts(s):
    return dt.datetime.fromisoformat(s.replace("Z", "+00:00"))


def kst_day(ts):
    return ts.astimezone(KST).strftime("%Y-%m-%d")


def kst_str(ts):
    return ts.astimezone(KST).strftime("%m-%d %H:%M")


def repo_root(path):
    i = path.find("/.worktrees/")
    return path[:i] if i >= 0 else path


def task_of(cwd, repo):
    if not cwd:
        return "(cwd 없음)"
    m = WORKTREE_RE.search(cwd)
    if m and cwd.startswith(repo):
        return m.group(1)
    if cwd == repo or cwd.startswith(repo + "/"):
        return MAIN_CHECKOUT
    return OUTSIDE


def read_jsonl(path):
    try:
        fh = open(path, encoding="utf-8")
    except OSError:
        return
    with fh:
        for line in fh:
            try:
                d = json.loads(line)
            except ValueError:
                continue
            if isinstance(d, dict):
                yield d


class Claude:
    def __init__(self):
        self.rows = []  # dict: session, role, model, day, task, sub, ts, tokens
        self.raw = 0
        self.unique = 0
        self.traces_only = []
        self.sessions = {}
        self.orphan_meta = 0
        self.outside = 0


def scan_claude(proj_dir, repo, since):
    out = Claude()
    seen = set()
    outside_seen = set()
    mains = sorted(glob.glob(os.path.join(proj_dir, "*.jsonl")))
    for path in mains:
        sid = os.path.basename(path)[:-6]
        files = [(path, False)] + [(p, True) for p in sorted(glob.glob(os.path.join(proj_dir, sid, "subagents", "*.jsonl")))]
        role = None
        for fpath, sub in files:
            if os.path.getmtime(fpath) < since.timestamp():
                continue
            last_cwd = None
            for d in read_jsonl(fpath):
                if role is None and not sub and d.get("type") == "attachment":
                    m = ROLE_RE.search(json.dumps(d.get("attachment", {}), ensure_ascii=False))
                    if m:
                        role = m.group(1)
                if d.get("cwd"):
                    last_cwd = d["cwd"]
                msg = d.get("message")
                if d.get("type") != "assistant" or not isinstance(msg, dict) or not isinstance(msg.get("usage"), dict):
                    continue
                if "timestamp" not in d:
                    continue
                ts = parse_ts(d["timestamp"])
                if ts < since:
                    continue
                task = task_of(d.get("cwd") or last_cwd, repo)
                key = msg.get("id") or d.get("uuid")
                if task == OUTSIDE:
                    outside_seen.add(key)
                    continue
                out.raw += 1
                if key in seen:
                    continue
                seen.add(key)
                out.unique += 1
                u = msg["usage"]
                cc = u.get("cache_creation") if isinstance(u.get("cache_creation"), dict) else {}
                total_cc = u.get("cache_creation_input_tokens") or 0
                c5 = cc.get("ephemeral_5m_input_tokens") or 0
                c1 = cc.get("ephemeral_1h_input_tokens") or 0
                tok = {
                    "input": u.get("input_tokens") or 0,
                    "output": u.get("output_tokens") or 0,
                    "cache_5m": c5,
                    "cache_1h": c1,
                    "cache_unsplit": max(total_cc - c5 - c1, 0),
                    "cache_read": u.get("cache_read_input_tokens") or 0,
                }
                out.rows.append({"session": sid, "role_ref": sid, "model": msg.get("model") or "(모델 없음)",
                                 "day": kst_day(ts), "task": task,
                                 "sub": sub, "ts": ts, "tok": tok})
        out.sessions[sid] = {"role": role or UNKNOWN_ROLE}
    for r in out.rows:
        r["role"] = out.sessions[r["session"]]["role"]
    for entry in sorted(os.listdir(proj_dir)) if os.path.isdir(proj_dir) else []:
        p = os.path.join(proj_dir, entry)
        if os.path.isdir(p) and entry != "memory" and not os.path.exists(p + ".jsonl"):
            if os.path.getmtime(p) >= since.timestamp():
                out.traces_only.append(entry)
        if os.path.isdir(p):
            for meta in glob.glob(os.path.join(p, "subagents", "*.meta.json")):
                if not os.path.exists(meta[: -len(".meta.json")] + ".jsonl") and os.path.getmtime(meta) >= since.timestamp():
                    out.orphan_meta += 1
    out.outside = len(outside_seen - seen)
    return out


class Codex:
    def __init__(self):
        self.rows = []
        self.raw = 0
        self.unique = 0
        self.rollouts = []  # dict: file, start, checked, match
        self.cross_checked = 0
        self.cross_match = 0
        self.outside = 0


def codex_tok(u):
    return {"input": u.get("input_tokens") or 0, "cached": u.get("cached_input_tokens") or 0,
            "output": u.get("output_tokens") or 0, "reasoning": u.get("reasoning_output_tokens") or 0}


def scan_codex(codex_dir, repo, since, role_map):
    out = Codex()
    seen = set()
    outside_seen = set()
    for path in sorted(glob.glob(os.path.join(codex_dir, "**", "rollout-*.jsonl"), recursive=True)):
        if os.path.getmtime(path) < since.timestamp():
            continue
        it = read_jsonl(path)
        first = next(it, None)
        if not first or first.get("type") != "session_meta":
            continue
        cwd0 = (first.get("payload") or {}).get("cwd") or ""
        if not (cwd0 == repo or cwd0.startswith(repo + "/")):
            continue
        sid = (first.get("payload") or {}).get("id") or os.path.basename(path)
        model, cwd = None, cwd0
        file_sum = collections.Counter()
        file_seen = set()
        last_total = None
        first_ts = None
        for d in it:
            p = d.get("payload") if isinstance(d.get("payload"), dict) else {}
            t = d.get("type")
            if t == "turn_context":
                model = p.get("model") or model
                cwd = p.get("cwd") or cwd
            elif t == "event_msg" and p.get("type") == "token_count":
                tu = (p.get("info") or {}).get("total_token_usage")
                if tu:
                    last_total = codex_tok(tu)
            elif t == "token_usage_record" and isinstance(p.get("usage"), dict):
                rid = p.get("response_id")
                tok = codex_tok(p["usage"])
                if rid not in file_seen:
                    file_seen.add(rid)
                    for k, v in tok.items():
                        file_sum[k] += v
                ts = parse_ts(d["timestamp"]) if "timestamp" in d else None
                if ts is None or ts < since:
                    continue
                task = task_of(cwd, repo)
                if task == OUTSIDE:
                    outside_seen.add(rid)
                    continue
                out.raw += 1
                if rid in seen:
                    continue
                seen.add(rid)
                out.unique += 1
                first_ts = first_ts or ts
                m = model or "(모델 없음)"
                role = role_map.get(m)
                out.rows.append({"session": sid, "role": ("추정:" + role) if role else UNKNOWN_ROLE, "model": m,
                                 "day": kst_day(ts), "task": task, "ts": ts, "tok": tok})
        if last_total is not None and file_seen:
            out.cross_checked += 1
            out.cross_match += dict(file_sum) == last_total
        out.rollouts.append({"session": sid, "first_ts": first_ts})
    out.outside = len(outside_seen - seen)
    return out


def load_prices(path):
    try:
        with open(path, encoding="utf-8") as fh:
            return json.load(fh)
    except (OSError, ValueError):
        return {"claude": {"models": {}}, "codex": None}


def claude_cost(model, tok, prices):
    p = (prices.get("claude") or {}).get("models", {}).get(model)
    if not p or tok["cache_unsplit"]:
        return None
    return (tok["input"] * p["input"] + tok["output"] * p["output"] + tok["cache_5m"] * p["cache_write_5m"]
            + tok["cache_1h"] * p["cache_write_1h"] + tok["cache_read"] * p["cache_read"]) / 1e6


def aggregate(rows, keyfn, fields):
    agg = {}
    for r in rows:
        k = keyfn(r)
        a = agg.setdefault(k, {"responses": 0, **{f: 0 for f in fields}})
        a["responses"] += 1
        for f in fields:
            a[f] += r["tok"][f]
    return agg


def ledger_roles(path):
    roles = {}
    try:
        with open(path, encoding="utf-8") as fh:
            for line in fh:
                cells = [c.strip() for c in line.strip().strip("|").split("|")]
                if len(cells) >= 3 and cells[0] and not cells[0].startswith(("role", "---", "(옛)")):
                    roles[cells[0]] = cells[2].split("·")[0]
    except OSError:
        pass
    return roles


def live_claude_processes(repo):
    found = []
    for env_path in glob.glob("/proc/[0-9]*/environ"):
        pid = env_path.split("/")[2]
        try:
            with open("/proc/%s/comm" % pid) as fh:
                if fh.read().strip() != "claude":
                    continue
            cwd = os.readlink("/proc/%s/cwd" % pid)
            if not (cwd == repo or cwd.startswith(repo + "/")):
                continue
            with open(env_path, "rb") as fh:
                names = {kv.split(b"=", 1)[0] for kv in fh.read().split(b"\0") if kv}
                fh.seek(0)
                role = next((kv.split(b"=", 1)[1].decode() for kv in fh.read().split(b"\0") if kv.startswith(b"HERDR_ROLE=")), "")
            start = dt.datetime.fromtimestamp(os.stat("/proc/%s" % pid).st_ctime, KST)
            found.append({"pid": int(pid), "role": role or UNKNOWN_ROLE, "start": start,
                          "child_marker": b"CLAUDE_CODE_CHILD_SESSION" in names})
        except OSError:
            continue
    return sorted(found, key=lambda x: x["pid"])


def build_report(a):
    since = parse_ts(a.since)
    repo = a.repo
    prices = load_prices(a.prices)
    cl = scan_claude(a.claude_dir, repo, since)
    cx = scan_codex(a.codex_dir, repo, since, dict(x.split("=", 1) for x in a.codex_role))
    rep = {"since": since.astimezone(KST).isoformat(), "repo": repo}

    def claude_table(keyfn, keyname):
        agg = aggregate(cl.rows, keyfn, CLAUDE_FIELDS)
        res = []
        for k in sorted(agg):
            v = agg[k]
            cost = None
            if keyname in ("role_model", "role_model_task"):
                cost = 0.0
                for r in cl.rows:
                    if keyfn(r) == k:
                        c = claude_cost(r["model"], r["tok"], prices)
                        if c is None:
                            cost = None
                            break
                        cost += c
            res.append({"key": list(k) if isinstance(k, tuple) else [k], **v, "cost_usd": cost})
        return res

    rep["claude_by_role_model"] = claude_table(lambda r: (r["role"], r["model"]), "role_model")
    rep["claude_by_task"] = claude_table(lambda r: (r["role"], r["task"]), "task")
    rep["claude_by_day"] = claude_table(lambda r: (r["day"],), "day")
    for name, keyfn in (("codex_by_role_model", lambda r: (r["role"], r["model"])),
                        ("codex_by_task", lambda r: (r["role"], r["task"])),
                        ("codex_by_day", lambda r: (r["day"],))):
        agg = aggregate(cx.rows, keyfn, CODEX_FIELDS)
        rep[name] = [{"key": list(k), **agg[k]} for k in sorted(agg)]

    unpriced = set()
    cost_total = 0.0
    for r in cl.rows:
        c = claude_cost(r["model"], r["tok"], prices)
        if c is None:
            unpriced.add(r["model"])
        else:
            cost_total += c
    rep["claude_cost_total_usd"] = cost_total
    rep["claude_unpriced_models"] = sorted(unpriced)
    rep["claude_price_source"] = prices.get("claude", {}).get("source")
    rep["claude_price_retrieved"] = prices.get("claude", {}).get("retrieved")
    rep["cost_notice"] = {
        "claude": {
            "status": "estimate",
            "meaning": COST_ESTIMATE_NOTE,
            "price_source": rep["claude_price_source"],
            "price_retrieved": rep["claude_price_retrieved"],
            "unit": prices.get("claude", {}).get("unit"),
            "total_usd": cost_total,
            "unpriced_models": sorted(unpriced),
            "unpriced_reason": UNPRICED_NOTE if unpriced else None,
        },
        "codex": {"status": "not_computed", "reason": CODEX_NOTE},
    }

    gaps = []
    claude_roles = {r["role"] for r in cl.rows}
    first_claude = min((r["ts"] for r in cl.rows), default=None)
    if first_claude is None:
        gaps.append("기준 시점 이후 Claude 기록이 하나도 없다.")
    else:
        h = (first_claude - since).total_seconds() / 3600
        gaps.append("기준 시점 %s 이후 첫 Claude 기록은 %s — 앞 %.1f시간은 기록이 없다(전 역할)." % (
            kst_str(since), kst_str(first_claude), h))
    for role in sorted(claude_roles):
        f = min(r["ts"] for r in cl.rows if r["role"] == role)
        gaps.append("Claude %s: 첫 기록 %s (기준 시점과 %.1f시간 차)" % (role, kst_str(f), (f - since).total_seconds() / 3600))
    for role, tool in sorted(ledger_roles(a.ledger).items()):
        if tool == "claude" and role not in claude_roles:
            gaps.append("장부의 역할 %s(claude): 기준 시점 이후 jsonl 사용량 없음" % role)
        elif tool == "codex":
            gaps.append("장부의 역할 %s(codex): rollout에 역할 필드가 없어 기록 유무를 대조하지 못함" % role)
    if "orchestrator" not in claude_roles:
        gaps.append("Claude orchestrator: 기록 없음(장부에 행이 없어도 세션은 있었을 수 있다)")
    if cl.traces_only:
        gaps.append("메인 jsonl 없이 흔적(디렉터리)만 있는 Claude 세션 %d개: %s" % (
            len(cl.traces_only), ", ".join(s[:8] for s in cl.traces_only)))
    if cl.orphan_meta:
        gaps.append("jsonl 없이 meta만 있는 서브에이전트 %d개: 사용량을 알 수 없다" % cl.orphan_meta)
    if not a.no_proc:
        procs = live_claude_processes(repo)
        for p in procs:
            if p["child_marker"]:
                gaps.append("실행 중 Claude %s(pid %d, 시작 %s): CLAUDE_CODE_CHILD_SESSION이 있어 기록이 저장되지 않는 세션 — 교체 전까지 누락" % (
                    p["role"], p["pid"], p["start"].strftime("%m-%d %H:%M")))
        rep["live_claude"] = [{"pid": p["pid"], "role": p["role"], "child_marker": p["child_marker"]} for p in procs]
    if UNKNOWN_ROLE in claude_roles:
        gaps.append("Claude 역할을 SessionStart 문구에서 읽지 못한 세션의 사용량이 '미상'에 있다.")
    gaps.append("Claude 중복 제거: assistant usage 줄 %d → message.id 고유 %d" % (cl.raw, cl.unique))
    gaps.append("저장소 밖 cwd라 집계에서 제외한 응답: Claude %d, Codex %d" % (cl.outside, cx.outside))
    gaps.append("Codex 중복 제거: token_usage_record %d → response_id 고유 %d; rollout 합계 vs 마지막 total_token_usage 일치 %d/%d" % (
        cx.raw, cx.unique, cx.cross_match, cx.cross_checked))
    gaps.append("Codex rollout %d개 집계(cwd가 이 저장소)." % len(cx.rollouts)
                + (" 역할은 rollout에 없다 — '추정:'은 --codex-role 모델 대응(근거: 모델·시작 시각)이고 '미상'은 근거 없음." if cx.rows else ""))
    gaps.append("/clear는 sessionId를 바꾸고 이어 주는 필드가 없다. compact 요약 호출이 별도 응답으로 기록되는지는 확인되지 않았다.")
    gaps.append("작업 대응은 각 응답의 cwd가 .worktrees/<이름> 아래인지로 정한 근사다. 본 체크아웃 작업은 작업 ID에 붙지 않는다.")
    gaps.append("과거 누락분은 추정치로 채우지 않았다. 이 합계를 프로젝트 전체 사용량으로 읽으면 안 된다.")
    rep["gaps"] = gaps
    rep["claude_sessions"] = sorted(
        ({"session": s[:8], "role": v["role"], "responses": sum(1 for r in cl.rows if r["session"] == s),
          "first": kst_str(min(r["ts"] for r in cl.rows if r["session"] == s)),
          "last": kst_str(max(r["ts"] for r in cl.rows if r["session"] == s))}
         for s, v in cl.sessions.items() if any(r["session"] == s for r in cl.rows)),
        key=lambda x: x["first"])
    return rep


def vw(s):
    return sum(2 if unicodedata.east_asian_width(c) in "WF" else 1 for c in s)


def fmt_table(headers, rows, num_from):
    cells = [headers] + rows
    widths = [max(vw(r[i]) for r in cells) for i in range(len(headers))]
    lines = []
    for r in cells:
        parts = []
        for i, c in enumerate(r):
            pad = " " * (widths[i] - vw(c))
            parts.append(pad + c if i >= num_from else c + pad)
        lines.append("  ".join(parts).rstrip())
    lines.insert(1, "  ".join("-" * w for w in widths))
    return "\n".join(lines)


def n(v):
    return "{:,}".format(v)


def cost_cell(c):
    return "-" if c is None else "$%.2f" % c


CL_HEAD = ["응답", "input", "output", "cache생성5m", "cache생성1h", "cache생성(미구분)", "cache읽기"]
CX_HEAD = ["응답", "input", "(cached)", "output", "(reasoning)"]


def render_text(rep):
    o = ["# 토큰 사용량 집계 (읽기 전용) — 기준 시점 %s, KST" % rep["since"], "", "## 누락·한계"]
    o += ["- " + g for g in rep["gaps"]]
    o += ["", "## Claude (cache 생성/읽기는 input과 별개 항목, Codex와 합산하지 않음)"]
    for title, key, head, withcost in (("역할·모델별", "claude_by_role_model", ["역할", "모델"], True),
                                      ("역할·작업별(cwd 근사)", "claude_by_task", ["역할", "작업"], False),
                                      ("일자별", "claude_by_day", ["일자"], False)):
        rows = []
        for e in rep[key]:
            r = e["key"] + [n(e["responses"]), n(e["input"]), n(e["output"]), n(e["cache_5m"]), n(e["cache_1h"]),
                            n(e["cache_unsplit"]), n(e["cache_read"])]
            if withcost:
                r.append(cost_cell(e["cost_usd"]))
            rows.append(r)
        o += ["", "### " + title, fmt_table(head + CL_HEAD + (["환산비용(추정)"] if withcost else []), rows, len(head))]
    o += ["", "### 세션별",
          fmt_table(["세션", "역할", "응답", "첫 기록", "마지막 기록"],
                    [[s["session"], s["role"], n(s["responses"]), s["first"], s["last"]] for s in rep["claude_sessions"]], 2)]
    o += ["", "비용: " + COST_ESTIMATE_NOTE + ". 단가 출처 %s (조회 %s)." % (
        rep["claude_price_source"], rep["claude_price_retrieved"]),
        "단가 확인된 응답의 환산 합계 $%.2f." % rep["claude_cost_total_usd"]]
    if rep["claude_unpriced_models"]:
        o.append("단가표에 없는 모델: %s — %s." % (", ".join(rep["claude_unpriced_models"]), UNPRICED_NOTE))
    o += ["", "## Codex (input에 cached 포함, output에 reasoning 포함 — OpenAI 방식, Claude와 합산하지 않음)"]
    for title, key, head in (("역할·모델별", "codex_by_role_model", ["역할", "모델"]),
                             ("역할·작업별(cwd 근사)", "codex_by_task", ["역할", "작업"]),
                             ("일자별", "codex_by_day", ["일자"])):
        rows = [e["key"] + [n(e["responses"]), n(e["input"]), n(e["cached"]), n(e["output"]), n(e["reasoning"])]
                for e in rep[key]]
        o += ["", "### " + title, fmt_table(head + CX_HEAD, rows, len(head))]
    o += ["", "비용: " + CODEX_NOTE + "."]
    return "\n".join(o) + "\n"


def render_tsv(rep):
    cn = rep["cost_notice"]["claude"]
    o = ["# 기준 시점 %s, KST" % rep["since"],
         "# Claude 비용(cost_usd): %s. 단가 출처 %s (조회 %s), 단위 %s" % (
             COST_ESTIMATE_NOTE, cn["price_source"], cn["price_retrieved"], cn["unit"]),
         "# 단가 없는 모델: %s" % (("%s — %s" % (", ".join(cn["unpriced_models"]), UNPRICED_NOTE)) if cn["unpriced_models"] else "없음"),
         "# Codex: " + CODEX_NOTE,
         "# Claude와 Codex 토큰은 합산하지 않는다. 한계는 맨 끝 gaps 절"]
    for sec in ("claude_by_role_model", "claude_by_task", "claude_by_day"):
        o.append("# " + sec)
        o.append("\t".join(["key", "responses", *CLAUDE_FIELDS, "cost_usd"]))
        for e in rep[sec]:
            o.append("\t".join(["/".join(e["key"]), str(e["responses"]), *[str(e[f]) for f in CLAUDE_FIELDS],
                                "" if e["cost_usd"] is None else "%.4f" % e["cost_usd"]]))
    for sec in ("codex_by_role_model", "codex_by_task", "codex_by_day"):
        o.append("# " + sec + " (비용 미산출: 단가 미확인)")
        o.append("\t".join(["key", "responses", *CODEX_FIELDS]))
        for e in rep[sec]:
            o.append("\t".join(["/".join(e["key"]), str(e["responses"]), *[str(e[f]) for f in CODEX_FIELDS]]))
    o.append("# gaps")
    o += g_lines(rep)
    return "\n".join(o) + "\n"


def g_lines(rep):
    return ["# " + g.replace("\t", " ") for g in rep["gaps"]]


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    root = repo_root(os.path.dirname(os.path.dirname(HERE)))
    ap.add_argument("--since", default=DEFAULT_SINCE, help="기준 시점(ISO 8601). 기본: 하네스 설치 커밋 0461718")
    ap.add_argument("--format", choices=("text", "tsv", "json"), default="text")
    ap.add_argument("--repo", default=root, help="본 체크아웃 경로(작업 대응·Codex cwd 필터 기준)")
    ap.add_argument("--claude-dir", default=None, help="Claude 프로젝트 기록 폴더. 기본: ~/.claude/projects/<repo 경로를 -로 바꾼 이름>")
    ap.add_argument("--codex-dir", default=os.path.expanduser("~/.codex/sessions"))
    ap.add_argument("--prices", default=os.path.join(HERE, "prices.json"))
    ap.add_argument("--ledger", default=os.path.expanduser("~/.claude/herdr/handoff/zip-viewer/session-ledger.md"))
    ap.add_argument("--codex-role", action="append", default=[], metavar="모델=역할",
                    help="Codex 역할 추정 대응(예: gpt-6.1-sol=verifier). 출력에 '추정:'으로 표시")
    ap.add_argument("--no-proc", action="store_true", help="실행 중 프로세스(/proc) 점검을 건너뜀")
    a = ap.parse_args(argv)
    a.repo = a.repo.rstrip("/")
    if a.claude_dir is None:
        a.claude_dir = os.path.expanduser("~/.claude/projects/" + a.repo.replace("/", "-"))
    rep = build_report(a)
    if a.format == "json":
        sys.stdout.write(json.dumps(rep, ensure_ascii=False, indent=2) + "\n")
    elif a.format == "tsv":
        sys.stdout.write(render_tsv(rep))
    else:
        sys.stdout.write(render_text(rep))


if __name__ == "__main__":
    main()
