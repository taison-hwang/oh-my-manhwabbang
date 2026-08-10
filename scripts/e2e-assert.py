#!/usr/bin/env python3
"""The curl-level assertion suite of impl-plan §6.3 step 5.

Run by scripts/e2e.sh against a live server. It speaks only HTTP — no database
access, no filesystem access — so what it checks is exactly what a browser
would see.

    scripts/e2e-assert.py --base http://127.0.0.1:8791 --mode real
    scripts/e2e-assert.py --base http://127.0.0.1:8791 --mode synthetic

`--phase` selects which suite runs. The default, `main`, is step 5 above and is
the whole of what this script used to be. The three `roots-*` phases are
amendment A-11 (ruling E-26) and are called at three different points of
scripts/e2e.sh, because what they assert is a sequence in time — write the file,
restart, adopt, remove — and no single invocation can stand at all three
moments. They run in the SYNTHETIC round only; see scripts/e2e.sh step 8b.

The A-11 phases are the one documented exception to "no filesystem access" in
the paragraph above, and it is not a shortcut: `POST /api/roots` is specified as
an edit to a file on disk that the running server does NOT adopt, so an
assertion that never looks at the file cannot tell a write from a no-op that
answered 201. They read the file at `Settings.server.config_path` — a path the
server itself publishes — and its `.bak`, and they never write either.

Every check prints one line and the script exits non-zero if any failed, so a
failing run says which requirement broke rather than "assertion error".
"""

from __future__ import annotations

import argparse
import json
import os
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

# The ten curated series of impl-plan §6.3, plus the two shapes the real
# collection has no sample of, which only the synthetic tree carries (D-49).
CURATED = [
    "Clover 클로버 (총4권)",
    "상처를 쫓는자 1-11 (완) 이케가미 료이치",
    "자살도114-122",
    "바퀴.zip",
    "강철의 연금술사 1~27권 완결",
    "군계 1~25",
    "디엔엔젤 1-13권 연재중",
    "미생 1~9 (완결 pdf)",
    "배틀로얄 1~15 [완결].zip",
    "엔젤하트 전32권 완결.zip",
]
SYNTHETIC_EXTRA = ["암호화 테스트.zip", "ZIP64 테스트.zip"]

CLOVER, WOUNDS, SUICIDE, WHEEL = CURATED[0], CURATED[1], CURATED[2], CURATED[3]
FMA, GUNGYE, DNANGEL, MISAENG = CURATED[4], CURATED[5], CURATED[6], CURATED[7]
BATTLE_ROYALE, ANGEL_HEART = CURATED[8], CURATED[9]

# arch §7.9 pins the cache-usage walk at "cached for 60 s". The bounded wait
# below has to outlast that window rather than match it, or a check that is one
# second unlucky reports a timeout where it should report a verdict.
USAGE_WINDOW_S = 60
USAGE_WAIT_S = 90

# The bound on wait_for_thumb_quiescence. It is a CEILING, not an observation:
# how long the overhang lasts has never been timed, here or anywhere. Two things
# are known and neither is a settle time.
#
# (1) docs/HANDOFF.md §5.6 항목 1 ("커버 루프 앞의 1초 sleep") records as 실측 that
#     the scan reported `idle 36/36` on the real subset with 32-33 of the 36
#     files already on disk. Cited by SECTION, never by line: that document is
#     rewritten every session, and this very citation read `:208` until the
#     3rd-session rewrite moved the paragraph. (36 is 9 covered series
#     x the 4 widths of test/shelf.e2e.yaml.tmpl:44, one Enqueue each —
#     internal/app/covers.go — at `thumbnails.workers: 4`, line 45.) That is the
#     SIZE of the gap, not its duration.
# (2) The `time.sleep(1.0)` this replaced stood in front of the same
#     `first_ready == len(covered)` assertion that is still below, and that
#     assertion was green, so one second sufficed on those rounds. That bounds
#     the overhang at roughly a second without anyone having measured it.
#
# 30 s is slack for a cold page cache on a slow disk, not a budget the suite is
# expected to approach. Exceeding it is a FAIL, not a shrug.
THUMB_SETTLE_S = 30.0
THUMB_POLL_S = 0.1


class Runner:
    def __init__(self, base: str) -> None:
        self.base = base.rstrip("/")
        self.failures: list[str] = []
        self.checks = 0

    # -- plumbing ---------------------------------------------------------
    def request(
        self, method: str, path: str, body: object = None
    ) -> tuple[int, bytes, dict[str, str]]:
        """One HTTP round trip. `body`, when given, is sent as JSON.

        `None` is not "no body" — `json.dumps(None)` is the four bytes `null`,
        which §7.1's strict decoder rejects — so the two are distinguished by
        identity against a sentinel-free default: only a non-None body is
        encoded, and `{}` (which several A-11 checks send deliberately) still
        travels as an empty JSON object.
        """
        headers = {"Accept": "application/json"}
        data = None
        if body is not None:
            data = json.dumps(body).encode()
            headers["Content-Type"] = "application/json"
        req = urllib.request.Request(self.base + path, data=data, headers=headers, method=method)
        try:
            with urllib.request.urlopen(req, timeout=60) as r:
                return r.status, r.read(), dict(r.headers)
        except urllib.error.HTTPError as e:
            return e.code, e.read(), dict(e.headers)

    def get(self, path: str) -> tuple[int, bytes, dict[str, str]]:
        return self.request("GET", path)

    def json(self, path: str) -> object:
        status, body, _ = self.get(path)
        if status != 200:
            raise AssertionError(f"GET {path} returned {status}: {body[:300]!r}")
        return json.loads(body)

    def check(self, name: str, ok: bool, detail: str = "") -> bool:
        self.checks += 1
        if ok:
            print(f"  PASS  {name}")
        else:
            print(f"  FAIL  {name}" + (f"\n          {detail}" if detail else ""))
            self.failures.append(name)
        return ok

    def eq(self, name: str, got: object, want: object) -> bool:
        return self.check(name, got == want, f"got {got!r}, want {want!r}")

    def ge(self, name: str, got: int, want: int) -> bool:
        return self.check(name, got >= want, f"got {got}, want >= {want}")


def series_by_name(r: Runner) -> dict[str, dict]:
    listing = r.json("/api/series?limit=200")
    return {item["name"]: item for item in listing["items"]}, listing


def server_clock(r: Runner) -> float:
    """The server's wall clock in Unix seconds, from arch §7.4's /api/health.

    `computed_at` below is stamped by the server, so "did that walk run after
    this moment?" is a question that can only be asked on the server's clock —
    ours may be anywhere. `started_at` is whole seconds, so this reads up to a
    second early; the caller compares with `>=` and tolerates it deliberately,
    because the walk it exists to refuse is one from before the scan, tens of
    seconds back, not one from a second ago.
    """
    h = r.json("/api/health")
    return h["started_at"] + h["uptime_ms"] / 1000.0


def check_thumb_cache(r: Runner, mark: float, want_files: int) -> None:
    """FR-THM-008 — the cache accounting sees the covers the scan wrote.

    `GET /api/cache/usage` is not a measurement, it is a memo. arch §7.9 types
    it `computed_at: Unix;  // the walk is cached for 60 s`, and §5.6
    FR-THM-008 says the same in prose: the endpoint "walks the cache once per
    60 s (cached result)". The walk is lazy (internal/thumbs/usage.go), so the
    60 s window opens on the first read by *anybody* — the settings dialog, a
    Playwright step, a stray curl. A read that lands before the covers phase
    pins `files: 0, bytes: 0` for the next minute. Reproduced on a fresh state
    directory: 36 files under <cache_dir>/thumbs and this endpoint reporting
    zero, seven seconds later, with the server behaving exactly to contract.

    Nothing forces a fresh walk: internal/httpapi/cache.go's handler reads no
    query parameter, there is no second endpoint or header, and the only other
    thing that invalidates the memo is `DELETE /api/cache`, which would destroy
    the files under test. So this states its assumption instead of making it —
    it names the moment the covers were proven servable (`mark`) and refuses
    any walk older than that, waiting a bounded USAGE_WAIT_S for one. Refusing
    is a FAIL, not a skip: a stale number is not evidence in either direction.
    """
    deadline = time.monotonic() + USAGE_WAIT_S
    t0 = time.monotonic()
    announced = False
    while True:
        usage = r.json("/api/cache/usage")
        fresh = usage["computed_at"] >= mark
        if fresh or time.monotonic() >= deadline:
            break
        if not announced:
            print(f"  note  /api/cache/usage is replaying a walk from "
                  f"{mark - usage['computed_at']:.0f}s before the covers "
                  f"(arch §7.9 caches it for {USAGE_WINDOW_S}s); waiting up to "
                  f"{USAGE_WAIT_S}s for a walk that can see them")
            announced = True
        time.sleep(2.0)

    waited = time.monotonic() - t0
    thumbs = next((e for e in usage["entries"] if e["kind"] == "thumbs"), None)
    name = "FR-THM-008 · the thumbnail cache is populated (fresh walk, not the 60 s memo)"
    if not fresh:
        r.check(name, False,
                f"gave up after {waited:.0f}s: /api/cache/usage never served a walk newer "
                f"than the covers (computed_at {usage['computed_at']}, needed >= {mark:.0f}). "
                f"What it does serve, {thumbs}, predates the thing under test and so proves "
                f"nothing either way.")
        return
    # want_files is not a guess: every one of those covers answered 200 above,
    # and a 200 is a file opened out of the cache directory (serveThumbFile) —
    # a miss is a 202. So at least that many regular files must be under
    # thumbs/, and a walk that reports fewer has caught a real absence.
    r.check(name,
            bool(thumbs) and thumbs["files"] >= want_files and thumbs["bytes"] > 0,
            f"{thumbs} (computed_at {usage['computed_at']}, {waited:.0f}s in); "
            f"want files >= {want_files} and bytes > 0")


def wait_for_thumb_quiescence(r: Runner, limit_s: float = THUMB_SETTLE_S) -> None:
    """Wait until no thumbnail generation is outstanding, and FAIL if it never is.

    The scan's own `covers_done == covers_total` does not mean the cover files
    exist. internal/app/covers.go derives `done` from the queue DEPTH, and says
    so in its own comment: the up-to-`thumbnails.workers` jobs being decoded
    right now are already counted as done, so the phase ends a few hundred
    milliseconds early. Measured on this subset at `thumbnails.workers: 4` —
    `idle 36/36` with 32-33 of the 36 files on disk. What used to stand here was
    `time.sleep(1.0)`, which is a guess at how long that overhang lasts.

    The four counters below are not a guess. thumbs.Stats reads them under one
    lock (internal/thumbs/thumbs.go), and their conjunction is exactly the
    predicate thumbs.Service.idle() tests: a job is held by cover_depth or
    page_depth from enqueue until a worker takes it, by active for that worker's
    whole turn, and by inflight until after the cache has done its
    write-temp-then-rename. All four zero therefore means every thumbnail the
    service was ever handed is at its final path.

    Still HTTP only, as the module docstring promises: this reads
    /api/health?verbose=1 and never looks at <cache_dir>, which scripts/e2e.sh
    does not even pass us.
    """
    keys = ("cover_depth", "page_depth", "active", "inflight")
    deadline = time.monotonic() + limit_s
    t0 = time.monotonic()
    while True:
        # A missing block and a missing field are the same failure — "this is
        # not the server this gate was written for" — so both say so. Reaching
        # `thumbs` through subscripts would have raised a bare KeyError naming
        # neither the handler nor the consequence.
        t = (r.json("/api/health?verbose=1").get("verbose") or {}).get("thumbs")
        if t is None:
            raise AssertionError(
                "/api/health?verbose=1 carried no `verbose.thumbs` block: handleHealth "
                "(internal/httpapi/health.go) no longer fills it, so there is no flush signal "
                "to wait for — only the sleep this replaced.")
        missing = [k for k in keys if k not in t]
        if missing:
            raise AssertionError(
                f"/api/health?verbose=1 has no {missing} under `thumbs`: this server predates "
                f"ThumbCounter.Active/Inflight (internal/httpapi/dto.go), and without them there "
                f"is no flush guarantee to wait for — only the sleep this replaced.")
        outstanding = {k: t[k] for k in keys if t[k] != 0}
        if not outstanding or time.monotonic() >= deadline:
            break
        time.sleep(THUMB_POLL_S)

    waited = time.monotonic() - t0
    r.check(
        f"the thumbnail service reaches idle before the covers are judged, so every cover "
        f"the scan queued is at its final path on disk (waited {waited:.1f}s)",
        not outstanding,
        f"still busy after {waited:.0f}s: {outstanding} (generated={t.get('generated')}, "
        f"failed={t.get('failed')}). Every cover assertion below would be measured against "
        f"a cache that is still moving, so this is a failure, not a warning.")


# ---------------------------------------------------------------------------
# Amendment A-11 / ruling E-26 — adding and removing a root over HTTP
# ---------------------------------------------------------------------------
#
# Three phases, called from three different points of scripts/e2e.sh, because
# what A-11 promises is a sequence in time:
#
#   roots-pre     step 8b, before the restart. AMENDMENT A-12 (ruling E-40):
#                 `POST` writes the FILE *and opens the root into the running
#                 server*, so the row is live at once — not `pending`, with real
#                 scan timestamps — and `config_changed_on_disk` goes back to
#                 false. Two of §7.4's rejections are answered here too.
#                 (Pre-A-12 this phase asserted the opposite of all three. The
#                 `pending` row it used to expect is now the *fallback* for an
#                 adoption that failed, which internal/httpapi/roots_test.go
#                 pins because it is not reachable from a passing round here.)
#   roots-post    step 10b, after the restart scripts/e2e.sh already performs.
#                 Under A-11 this was the assertion impl-plan §0.3 called "the
#                 one that stops 'restart-based' quietly becoming 'never
#                 applied'". A-12 moved adoption to the `POST`, so that is no
#                 longer this phase's job — what it grades now is DURABILITY,
#                 and step 10 is what keeps the check from being vacuous: it
#                 deletes the index database before restarting, so a live row
#                 here cannot be inherited from the `POST`'s scan.
#   roots-delete  step 11b. The root has content by now, so `DELETE` can be held
#                 to R1's exact promise: the row goes, that root's series go with
#                 it, and `user.db` is untouched — the reading progress written
#                 before the removal is still there afterwards.


def envelope(body: bytes) -> tuple[str, dict]:
    """`(code, detail)` out of the §7.2 error envelope.

    A body that is not one yields `("", {})` rather than raising. The checks
    below compare whole tuples, so a 201 that should have been a 400 must be
    reportable as one FAIL line rather than as a KeyError traceback that hides
    every check after it.
    """
    try:
        err = json.loads(body)["error"]
    except (ValueError, KeyError, TypeError):
        return "", {}
    detail = err.get("detail")
    return str(err.get("code", "")), detail if isinstance(detail, dict) else {}


def rejection(status: int, body: bytes) -> tuple:
    """A refusal as the tuple §7.4 specifies it: status, code, field, reason, conflict."""
    code, detail = envelope(body)
    return (status, code, detail.get("field"), detail.get("reason"),
            detail.get("conflicts_with"))


def roots_by_name(r: Runner) -> dict[str, dict]:
    return {item["name"]: item for item in r.json("/api/roots")["items"]}


def series_ids(r: Runner) -> set[str]:
    """Every series id in the library.

    `limit` is capped at 200 by arch §7.5, so a truncated page would silently
    turn "these series disappeared" into "these series were on page two". The
    listing states its own total; refuse to answer from a partial one.
    """
    listing = r.json("/api/series?limit=200&sort=name")
    if len(listing["items"]) != listing["total"]:
        raise AssertionError(
            f"the series listing is truncated ({len(listing['items'])} of "
            f"{listing['total']}, limit is capped at 200): every set comparison "
            f"below would be comparing prefixes")
    return {item["id"] for item in listing["items"]}


def browse(r: Runner, path: str | None = None) -> tuple[int, bytes]:
    """`GET /api/browse`, at the top level when `path` is None."""
    query = "" if path is None else "?path=" + urllib.parse.quote(path)
    status, body, _ = r.request("GET", "/api/browse" + query)
    return status, body


def check_browse(r: Runner, media_path: str, fresh_dir: str) -> None:
    """`GET /api/browse` — AMENDMENT A-12 (ruling E-40), the folder picker's endpoint.

    Called from `roots-pre`, before the `POST`, because that is the only moment
    `fresh_dir` is a directory the picker should offer: after step 8b adds it,
    the same row is `duplicate`. Both states are worth having and the second is
    the golden's (`testdata/golden/browse.json`); this tier is here for what a
    golden cannot show — the endpoint answering about *this run's* filesystem,
    through the `os.Root` opened on a base scripts/e2e-config.sh configured.
    """
    print("\n-- A-12 · GET /api/browse " + "-" * 45)

    status, body = browse(r)
    if not r.eq("A-12 · the top level lists the configured bases", status, 200):
        print(f"          body: {body[:300]!r}")
        return
    top = json.loads(body)
    # Three of the five fields are empty at the top level and that is the
    # contract, not a degenerate case: there is no directory to name, nothing
    # above the allowlist, and no single directory to offer as a choice.
    r.eq("A-12 · the top level names no directory, has no parent and offers no self",
         (top["path"], top["parent"], top["self"]), ("", None, None))
    bases = [entry["path"] for entry in top["entries"]]
    if not r.check("A-12 · scripts/e2e-config.sh configured exactly one base, and it is listed",
                   len(bases) == 1, f"entries: {bases}"):
        return
    base = bases[0]

    status, body = browse(r, base)
    if not r.eq("A-12 · a base lists one level down", status, 200):
        print(f"          body: {body[:300]!r}")
        return
    listing = json.loads(body)
    # `parent` stopping at a base is the allowlist holding: the parent of a base
    # is outside it, so offering one would be the first step out of the sandbox.
    r.eq("A-12 · a base has no parent — the allowlist is never walked out of",
         (listing["path"], listing["parent"]), (base, None))
    # The base contains the configured media root, so `POST /api/roots` would
    # refuse it. `overlaps` and not `contains_storage`, even though data_dir and
    # cache_dir are under it too: internal/httpapi/roots.go tests overlap first.
    r.eq("A-12 · the base offers itself as a choice, and says why it is not one",
         (listing["self"] or {}).get("selectable"), False)
    r.eq("A-12 · and the reason is §7.4's own vocabulary, computed by the server",
         (listing["self"] or {}).get("reason"), "overlaps")

    entries = {entry["name"]: entry for entry in listing["entries"]}
    media_name = os.path.basename(media_path.rstrip("/"))
    if r.check(f"A-12 · the configured media root is listed as a directory ({media_name!r})",
               media_name in entries, f"entries: {sorted(entries)}"):
        r.eq("A-12 · a configured root is offered but not selectable, and names the rule",
             (entries[media_name]["selectable"], entries[media_name]["reason"]),
             (False, "duplicate"))
        r.eq("A-12 · its path is absolute and cleaned — what POST /api/roots wants",
             entries[media_name]["path"], media_path)

    fresh_name = os.path.basename(fresh_dir.rstrip("/"))
    if r.check(f"A-12 · the directory step 8b is about to add is listed ({fresh_name!r})",
               fresh_name in entries, f"entries: {sorted(entries)}"):
        # The positive case. Without it every assertion above is satisfied by an
        # endpoint that refuses everything, which is §6.5's shape exactly.
        r.eq("A-12 · a directory no root holds IS selectable, with no reason to give",
             (entries[fresh_name]["selectable"], entries[fresh_name]["reason"]), (True, None))

    # A symlink is dropped from the listing rather than offered and then refused
    # (open item `ag`). The target is the base's PARENT — the one directory the
    # allowlist exists to keep out of reach — so this grades the case that
    # matters: `os.Root` would refuse to follow it at openat(2), and a picker
    # that listed it would be offering a path the user can never use.
    #
    # The link is removed again. This script otherwise only reads the filesystem,
    # and leaving an artefact inside the very base the endpoint under test lists
    # would make a re-run with SHELF_E2E_STATE pointed at an existing directory
    # grade a tree the previous run wrote.
    link = os.path.join(base, "browse-symlink")
    try:
        os.symlink(os.path.dirname(base.rstrip("/")) or "/", link)
    except OSError as err:
        r.check("A-12 · a symlink can be created to grade the listing's symlink filter",
                False, f"os.symlink({link!r}) failed: {err}")
        return
    try:
        status, body = browse(r, base)
        relisted = ({entry["name"] for entry in json.loads(body)["entries"]}
                    if status == 200 else set())
        r.check("A-12 · a symlink pointing OUT of the base is not listed, so it cannot be offered",
                status == 200 and "browse-symlink" not in relisted,
                f"status={status}, entries={sorted(relisted)}")
    finally:
        os.unlink(link)

    # The two refusals that matter, because they are what keeps this endpoint
    # from being a filesystem oracle (§7.4a).
    status, body = browse(r, "/etc")
    r.eq("A-12 · a path outside the allowlist is 403 outside_browse_bases",
         rejection(status, body)[:2] + (rejection(status, body)[3],),
         (403, "forbidden", "outside_browse_bases"))
    status, body = browse(r, "relative/path")
    r.eq("A-12 · a relative path is 400 not_absolute",
         rejection(status, body)[:2] + (rejection(status, body)[3],),
         (400, "bad_request", "not_absolute"))


def wait_for_root_scan(r: Runner, name: str, limit_s: float = 60.0) -> tuple[dict | None, float]:
    """Poll `GET /api/roots` until `name` carries a finished scan. Returns (row, seconds).

    AMENDMENT A-12 promises the `POST` *starts* a scan of the root it opened.
    The scan runs asynchronously, so a check written against `last_scan_end`
    straight after the 201 grades a race and not the contract — it passed once
    against a real server with `last_scan_start` set and `last_scan_end` still
    null. Polling for the settled row is what makes the finished state
    assertable without a `sleep` that is a guess at how long an empty directory
    takes. Returns whatever it last saw on timeout so the caller can report it.
    """
    t0 = time.monotonic()
    while True:
        row = roots_by_name(r).get(name)
        waited = time.monotonic() - t0
        if (row is not None and row["last_scan_end"] is not None) or waited >= limit_s:
            return row, waited
        time.sleep(0.25)


def wait_for_idle(r: Runner, limit_s: float = 180.0) -> tuple[str, float]:
    """Poll `/api/scan/status` until the scanner is idle. Returns (state, seconds)."""
    t0 = time.monotonic()
    while True:
        state = r.json("/api/scan/status")["state"]
        waited = time.monotonic() - t0
        if state == "idle" or waited >= limit_s:
            return state, waited
        time.sleep(0.5)


def read_bytes(path: str) -> bytes:
    with open(path, "rb") as f:
        return f.read()


def save_state(path: str, data: dict) -> None:
    with open(path, "w", encoding="utf-8") as f:
        json.dump(data, f)


def load_state(path: str) -> dict:
    # A phase that cannot read the handover file is not a crash, it is a
    # consequence: `--phase roots-pre` gave up before it created the root every
    # later phase is about. Say that, rather than a FileNotFoundError traceback
    # under whichever step happens to run next.
    if not os.path.exists(path):
        raise AssertionError(
            f"{path} does not exist: scripts/e2e.sh step 8b (--phase roots-pre) never got as "
            f"far as writing it, so the root this phase is about was never created. Fix the "
            f"failure reported by that step first — this one has nothing to grade.")
    with open(path, encoding="utf-8") as f:
        return json.load(f)


def phase_roots_pre(r: Runner, new_root: str, label: str, state_path: str) -> int:
    """`POST /api/roots` — the file changes AND the running server opens the root (A-12)."""
    print("\n-- A-12 · POST /api/roots, before the restart " + "-" * 25)

    server = r.json("/api/settings")["server"]
    # The precondition, asserted and then acted on rather than assumed: with the
    # gate shut every POST below is a 403 and each of them would "pass" a check
    # written as "not 201". This is the round where the capability is ON.
    if not r.check(
            "A-11 · the gate is open in this round, so what follows exercises the write path",
            server["root_editing_enabled"] is True,
            f"root_editing_enabled={server['root_editing_enabled']!r}; scripts/e2e-config.sh "
            f"emits server.allow_root_editing: true for --synthetic only"):
        return report(r)

    cfg_path = str(server["config_path"])
    if not r.check("A-10 · the server publishes the configuration file it loaded",
                   bool(cfg_path) and os.path.isfile(cfg_path), f"config_path={cfg_path!r}"):
        return report(r)
    backup = cfg_path + ".bak"
    before = read_bytes(cfg_path)
    r.check("nothing has written the configuration file yet, so the .bak below is this POST's",
            not os.path.exists(backup), f"{backup} already exists")

    existing = roots_by_name(r)
    if not r.eq("the configuration starts with exactly the media root", len(existing), 1):
        return report(r)
    media = next(iter(existing.values()))

    os.makedirs(new_root, exist_ok=True)
    r.check("the directory to be added is fresh, outside the media tree and outside "
            "data_dir/cache_dir",
            os.path.isdir(new_root) and os.listdir(new_root) == [],
            f"{new_root}: {os.listdir(new_root) if os.path.isdir(new_root) else 'missing'}")

    # The picker's endpoint, graded here and not in its own phase because it has
    # exactly one moment to be graded in: `new_root` exists but is not yet a
    # configured root, so the listing carries a selectable row and an
    # unselectable one at the same time.
    check_browse(r, media["path"], new_root)
    print("\n-- A-12 · POST /api/roots (continued) " + "-" * 33)

    # A-12 promises the POST *starts* a scan of the root it opened, but
    # `adoptRoot` treats "a scan is already running" as the ordinary case: the
    # root is in the scanner's table, so the next run covers it, and it logs
    # rather than failing. So "a scan started and finished" is only the server's
    # claim to keep when the scanner was idle to begin with — asserted here
    # rather than assumed, because assuming it is how a green round would come to
    # mean "the scanner happened to be busy" instead of "the adoption scanned".
    r.eq("the scanner is idle before the POST, so a scan of the new root can start now",
         r.json("/api/scan/status")["state"], "idle")

    status, body, headers = r.request("POST", "/api/roots", {"path": new_root, "label": label})
    if not r.eq("A-11 · POST /api/roots answers 201", status, 201):
        print(f"          body: {body[:300]!r}")
        return report(r)
    entry = json.loads(body)
    # `label` is already a valid slug ([a-z0-9._-]), so §7.4's generator has
    # nothing to strip and nothing to uniquify against: the name it produces is
    # the label itself. Pinning it is what lets every later phase — and the
    # browser tier — address this root by name without passing state around.
    r.eq("§7.4 · the name is server-generated from the label", entry["name"], label)
    r.eq("the 201 echoes the entry as written",
         (entry["path"], entry["label"], entry["enabled"]), (new_root, label, True))
    r.eq("the 201 carries a Location for the resource the client did not name",
         headers.get("Location"), f"/api/roots/{label}")

    after = read_bytes(cfg_path)
    r.check("the configuration file on disk ACTUALLY CHANGED — a 201 is not the evidence",
            after != before,
            f"{cfg_path} is byte-identical after a 201: {len(before)} bytes")
    r.check("the file now names the new directory in block style",
            f'path: "{new_root}"' in after.decode("utf-8", "replace"),
            f"the spliced entry is not in {cfg_path}")
    r.check("a .bak of the PREVIOUS contents was taken beside it",
            os.path.exists(backup) and read_bytes(backup) == before,
            f"{backup}: " + ("absent" if not os.path.exists(backup)
                             else f"{len(read_bytes(backup))} bytes, want the {len(before)} "
                                  f"bytes that were there before the write"))
    # AMENDMENT A-12: this was `True` under A-11, where the write and the running
    # process disagreed until a restart. Now the POST adopts what it wrote, so
    # the process and the file agree again and the UI has no restart to ask for.
    # arch-backend §7.4: "the row is then not pending, available is true, and
    # config_changed_on_disk is false — this process and the file agree again."
    r.eq("§7.8 · A-12 · the POST adopted its own write, so nothing is pending on disk",
         r.json("/api/settings")["server"]["config_changed_on_disk"], False)

    listed = roots_by_name(r)
    row = listed.get(label)
    if not r.check("R2 · the added root is listed at once, before any restart",
                   row is not None, f"GET /api/roots holds {sorted(listed)}"):
        return report(r)
    # AMENDMENT A-12 (ruling E-40) inverted this check. Under A-11 the row was
    # `pending: true` with null timestamps because the POST touched the file and
    # nothing else; now the POST opens the root into this process and scans it,
    # so the row is live. The pre-A-12 shape is not gone — it is the *fallback*
    # for an adoption that failed, which is unreachable from a passing round here
    # and is pinned by internal/httpapi/roots_test.go instead.
    #
    # These are graded on the row fetched immediately above, and the order is the
    # point: "at once" has to be measured at once. Polling first and asserting
    # afterwards would let a row that took a minute to go live satisfy a check
    # whose name says it did not — and it would report a failed adoption as a
    # scan that never finished, hiding the `pending` row that is the diagnosis.
    r.eq("A-12 · it is LIVE at once, without a restart — not pending, and available",
         (row["pending"], row["available"], row["enabled"]), (False, True, True))
    r.eq("A-12 · an empty directory opens with zero counts, which is what keeps the live row honest",
         (row["series_count"], row["book_count"], row["page_count"], row["total_bytes"]),
         (0, 0, 0, 0))
    r.eq("R2 · it carries the path and label that were written",
         (row["path"], row["label"], row["enabled"]), (new_root, label, True))

    # Only now the scan, which is asynchronous and therefore cannot be graded in
    # the same breath as the flags. No early return: `save_state` below is what
    # steps 10b and 11b need to run at all, and a scan that did not finish is not
    # a reason to take those two steps away from the operator.
    scanned, waited = wait_for_root_scan(r, label)
    r.check(f"A-12 · and the scan the POST started ran to completion ({waited:.1f}s)",
            scanned is not None and scanned["last_scan_end"] is not None,
            f"last_scan_end is still null after {waited:.1f}s: {scanned!r}")
    row = scanned or row
    r.eq("A-12 · and the adoption reports no error", row["last_scan_error"], None)
    r.eq("the media root is untouched by the addition",
         roots_by_name(r)[media["name"]]["series_count"], media["series_count"])

    # ---- §7.4's rejection table, on the two rules that protect the library --
    #
    # Both are answered by `detail.reason` and by `detail.conflicts_with`, which
    # is the only concrete thing the message can name: §7.4 deliberately does
    # not echo the rejected path back.
    status, body, _ = r.request("POST", "/api/roots", {"path": media["path"]})
    r.eq("§7.4 · re-adding a configured directory is 400 duplicate, naming the root it collides with",
         rejection(status, body), (400, "bad_request", "path", "duplicate", media["name"]))

    subdirs = sorted(name for name in os.listdir(media["path"])
                     if os.path.isdir(os.path.join(media["path"], name)))
    if r.check("the media tree has a subdirectory to offer as an overlapping root",
               bool(subdirs), f"{media['path']} holds no directory"):
        inside = os.path.join(media["path"], subdirs[0])
        status, body, _ = r.request("POST", "/api/roots", {"path": inside})
        r.eq("§7.4 · a descendant of a configured root is 400 overlaps — one file may not "
             "belong to two roots",
             rejection(status, body), (400, "bad_request", "path", "overlaps", media["name"]))

    r.check("neither refusal wrote anything: the file is exactly what the 201 left",
            read_bytes(cfg_path) == after,
            f"{cfg_path} changed while two requests were being refused")

    # The pre-restart scan timestamps travel with the handover so `roots-post`
    # can prove the row it sees is the fresh process's own work and not this
    # one's, which is the only thing that separates "reloaded" from "inherited".
    save_state(state_path, {"name": label, "path": new_root, "label": label,
                            "config_path": cfg_path, "media_root": media["name"],
                            "last_scan_start": row["last_scan_start"],
                            "last_scan_end": row["last_scan_end"]})
    return report(r)


def phase_roots_post(r: Runner, state_path: str) -> int:
    """After the restart: a fresh process re-read the spliced entry and scanned it itself."""
    print("\n-- A-12 · the addition is durable across a restart " + "-" * 20)
    state = load_state(state_path)
    name = state["name"]

    listed = roots_by_name(r)
    row = listed.get(name)
    if not r.check("the root added before the restart is still configured",
                   row is not None, f"GET /api/roots holds {sorted(listed)}"):
        return report(r)
    # Under A-11 this was THE assertion of this whole file (impl-plan §0.3): a
    # restart was what A-11 sold instead of a reload path, so a restart that
    # adopted nothing turned "restart-based" into "never applied" with every
    # other check green. AMENDMENT A-12 moved adoption to the POST, and a check
    # that keeps its old wording would now pass by inheritance — §6.5's exact
    # shape. Two things keep it honest instead:
    #   * step 10 DELETES the index database before this restart, so a live row
    #     cannot survive from the POST's scan. A fresh process that failed to
    #     read the spliced entry would report `pending` — in the file, no index
    #     row (§7.3) — which is what the flags below reject.
    #   * the timestamps are compared against the pre-restart ones, below.
    r.eq("A-12 · the fresh process re-read the spliced entry — live, not pending",
         (row["pending"], row["available"], row["enabled"]), (False, True, True))
    r.check("A-12 · and this scan is THIS process's, not the one the POST started",
            row["last_scan_start"] is not None
            and state.get("last_scan_start") is not None
            and row["last_scan_start"] != state["last_scan_start"],
            f"last_scan_start={row['last_scan_start']!r} is still the pre-restart value "
            f"{state.get('last_scan_start')!r}, so nothing here proves the restart re-read "
            f"the file")
    r.check("A-12 · and that scan ran to completion (it has both scan timestamps)",
            row["last_scan_start"] is not None and row["last_scan_end"] is not None,
            f"last_scan_start={row['last_scan_start']!r}, last_scan_end={row['last_scan_end']!r}")
    r.eq("the adopted root reports no error", row["last_scan_error"], None)
    # An empty directory indexes as empty on both sides of the restart. That is
    # not a detail: it is what lets step 8b's live row carry zeros without
    # lying, and it is why adding this root cannot disturb the AC-005 library
    # comparison in the same step.
    r.eq("the empty directory indexes as an empty root, on this side of the restart too",
         (row["series_count"], row["book_count"]), (0, 0))
    r.eq("§7.8 · the restarted server loaded the file the POST wrote, so nothing is pending on disk",
         r.json("/api/settings")["server"]["config_changed_on_disk"], False)
    r.eq("the media root survived the restart with the added one",
         state["media_root"] in listed, True)
    return report(r)


def phase_roots_delete(r: Runner, state_path: str) -> int:
    """`DELETE /api/roots/{name}` — R1: the row goes, its series go, user.db stays."""
    print("\n-- A-11 · DELETE /api/roots/{name} " + "-" * 37)
    state = load_state(state_path)
    name, media_name = state["name"], state["media_root"]

    before_ids = series_ids(r)
    if not r.check("the library has series to lose", bool(before_ids)):
        return report(r)

    # scripts/e2e.sh has just copied one archive into the added root. Scanning
    # only that root is arch §7.10's per-root rescan and is what the settings
    # screen's 재스캔 button sends.
    status, body, _ = r.request("POST", "/api/scan", {"roots": [name]})
    if not r.eq("§7.10 · a per-root rescan of the added root is accepted", status, 202):
        print(f"          body: {body[:300]!r}")
        return report(r)
    scan_state, waited = wait_for_idle(r, 120.0)
    if not r.eq(f"the rescan finished ({waited:.0f}s)", scan_state, "idle"):
        return report(r)

    grown = series_ids(r) - before_ids
    if not r.eq("the rescan indexed exactly the one series that was copied into that root",
                len(grown), 1):
        return report(r)
    sid = grown.pop()
    row = roots_by_name(r).get(name)
    if not r.check("the added root is still listed after its rescan", row is not None):
        return report(r)
    r.eq("it now reports the series it holds", (row["series_count"], row["pending"]), (1, False))

    detail = r.json(f"/api/series/{sid}")
    readable = [b for b in detail["books"] if b["status"] == "ok" and b["page_count"] >= 2]
    if not r.check("that series has a readable volume to record progress on",
                   bool(readable), f"books: {[(b['name'], b['status']) for b in detail['books']]}"):
        return report(r)
    book = readable[0]
    bid, page = book["id"], 2

    status, body, _ = r.request("PUT", f"/api/books/{bid}/progress", {"page": page})
    r.eq("FR-STT-001 · reading progress is recorded on a book of the root about to be removed",
         status, 200)
    # `progress` is null on a book nobody has opened (arch §7.6), so it is read
    # through a default rather than subscripted: a PUT that silently stored
    # nothing must fail this check by name, not with a TypeError.
    r.eq("and it reads back through the book",
         (r.json(f"/api/books/{bid}").get("progress") or {}).get("last_page"), page)
    exported = {item["book_id"]: item for item in r.json("/api/progress/export")["items"]}
    r.check("FR-STT-004 · the export carries it before the removal",
            bid in exported and exported[bid]["last_page"] == page,
            f"export holds {len(exported)} rows; ours: {exported.get(bid)}")

    status, body, _ = r.request("DELETE", f"/api/roots/{name}")
    if not r.eq("A-11 · DELETE /api/roots/{name} answers 204", status, 204):
        print(f"          body: {body[:300]!r}")
        return report(r)

    listed = roots_by_name(r)
    r.eq("the row is gone from GET /api/roots", name in listed, False)
    r.eq("and the other root is not", media_name in listed, True)
    # R1's whole point: file-only removal left the series in the library.
    after_ids = series_ids(r)
    r.eq("R1 · that root's series went with it — and nothing else did",
         after_ids, before_ids)
    r.eq("its series is a 404, not an orphan row",
         r.get(f"/api/series/{sid}")[0], 404)
    r.eq("its book is a 404 too", r.get(f"/api/books/{bid}")[0], 404)

    # The other half of R1, and the reason the removal confirmation says what it
    # says: `user.db` is never touched, so the reading progress written above is
    # still readable — over HTTP, from FR-STT-004's endpoint, which answers out
    # of `user.db` alone and joins no index row.
    exported = {item["book_id"]: item for item in r.json("/api/progress/export")["items"]}
    r.check("R1 · the reading progress written before the removal is STILL readable afterwards",
            bid in exported and exported[bid]["last_page"] == page
            and exported[bid]["root_name"] == name,
            f"the export no longer carries {bid}: {exported.get(bid)}. index.db is the "
            f"disposable half; user.db is the authored one and DELETE must not touch it.")

    # The rule that stops the endpoint writing a file the server cannot start
    # from. Asserted after the precondition, because with two roots left this
    # request would succeed and take the media root with it.
    remaining = roots_by_name(r)
    if r.eq("exactly one root is left, so removing it would leave none",
            sorted(remaining), [media_name]):
        status, body, _ = r.request("DELETE", f"/api/roots/{media_name}")
        code, detail_map = envelope(body)
        r.eq("§7.4 · the LAST root cannot be removed — validate.go would refuse the restart",
             (status, code, detail_map.get("reason")), (409, "conflict", "last_root"))
        r.eq("and it is still there", media_name in roots_by_name(r), True)
    return report(r)


# NFR-PRF-004's budget: a rescan of an unchanged tree finishes well inside this.
# How long the rescan may take before we call it a failure rather than a slow
# pass. Generous against the budget on purpose: a run that overruns must be
# reported as "took N s, over budget", not as "never finished".
#
# Both are environment-overridable so scripts/e2e-assert-rescan-test.py can
# drive the over-budget and never-published paths in under a second instead of
# in two minutes. That is safe to expose because the budget is printed INTO the
# check's own name below, so a run using anything other than 30 s says so in the
# gate output rather than passing quietly at a weakened threshold. scripts/e2e.sh
# sets neither, and the test pins both defaults.
RESCAN_BUDGET_S = float(os.environ.get("SHELF_E2E_RESCAN_BUDGET_S", "30"))
RESCAN_LIMIT_S = float(os.environ.get("SHELF_E2E_RESCAN_LIMIT_S", "120"))


def wait_for_run(r: Runner, run_id: str, limit_s: float) -> tuple[dict, float]:
    """Poll until `run_id` is the published run AND it has finished.

    Returns (last status seen, seconds waited).

    Two states have to be distinguished and the old bash could not tell them
    apart. `Scanner.Start` returns the new run id BEFORE the goroutine calls
    `progress.begin` (internal/scanner/scanner.go), so for a short window the
    published snapshot is still the PREVIOUS run, already idle. Waiting on
    `state == "idle"` alone therefore accepts that window as "the rescan
    finished" -- and accepts a rescan that never started as the same thing.
    Waiting on our own run id being the published one closes both.
    """
    t0 = time.monotonic()
    last: dict = {}
    while True:
        last = r.json("/api/scan/status")
        waited = time.monotonic() - t0
        if last.get("run_id") == run_id and last.get("state") == "idle":
            return last, waited
        if waited >= limit_s:
            return last, waited
        time.sleep(0.5)


def phase_rescan(r: Runner) -> int:
    """NFR-PRF-004 (scripts/e2e.sh step 8) — an immediate rescan is incremental.

    This phase exists because the four lines of bash it replaces reported PASS
    in two states where NO RESCAN RAN AT ALL (open item H, HANDOFF §5.6.1 H
    and §5.7):

        curl -fsS -X POST ... "$BASE/api/scan" >/dev/null   # no `|| die`
        if wait_for_idle 120; then
          if [ "$WAIT_SECS" -lt 30 ]; then ok "...finished in ${WAIT_SECS}s"

    `set -e` is off, so a POST that answered 500 was discarded; and
    `wait_for_idle` returns immediately when the scanner is ALREADY idle,
    which it is, because step 6 left it that way. Both states printed
    "the no-change rescan finished in 0s (< 30 s)" -- measured against a scan
    that never happened. Reproduced against a stub before this was written.

    So the check no longer asks "did the scanner reach idle". It asks whether
    the run THIS phase started is the run that finished. `progress.finish`
    keeps RunID and sets FinishedAt (internal/scanner/progress.go), so an idle
    status still names the run that produced it, and `newRunID` is 8 bytes of
    crypto/rand, so a repeated id is not a collision -- it is the same run.
    """
    print("\n-- step 8 · NFR-PRF-004 incremental rescan " + "-" * 26)

    before = r.json("/api/scan/status")
    prev_run, prev_finished = before.get("run_id"), before.get("finished_at")

    # Assert the precondition, do not guard on it (HANDOFF §6.5): if step 6's
    # scan is still running, or never ran, "a new run id appeared" proves
    # nothing and the budget below would be timing the wrong thing.
    if not r.eq("the scanner is idle before the rescan", before.get("state"), "idle"):
        return report(r)
    if not r.check("step 6 left a finished run to measure against",
                   bool(prev_run) and prev_finished is not None,
                   f"run_id={prev_run!r} finished_at={prev_finished!r}"):
        return report(r)

    status, body, _ = r.request("POST", "/api/scan", {})
    if not r.eq("POST /api/scan is accepted", status, 202):
        r.check("...and the refusal body says why", False, f"{body[:300]!r}")
        return report(r)
    try:
        run_id = json.loads(body)["run_id"]
    except (ValueError, KeyError, TypeError):
        r.check("the 202 carries a run_id", False, f"body={body[:300]!r}")
        return report(r)

    # A 202 that hands back the id of the run that already finished is the
    # "answered but started nothing" state. It is indistinguishable from a
    # real rescan by timing alone, which is exactly why timing was the wrong
    # thing to watch.
    if not r.check("the rescan is a NEW run, not step 6's",
                   bool(run_id) and run_id != prev_run,
                   f"POST returned run_id={run_id!r}, step 6 was {prev_run!r}"):
        return report(r)

    final, waited = wait_for_run(r, run_id, RESCAN_LIMIT_S)
    if not r.check("the rescan we started is the run that finished",
                   final.get("run_id") == run_id and final.get("state") == "idle",
                   f"after {waited:.1f}s: run_id={final.get('run_id')!r} "
                   f"state={final.get('state')!r} (wanted {run_id!r} / 'idle')"):
        return report(r)

    r.check("it carries a finished_at", final.get("finished_at") is not None,
            f"finished_at={final.get('finished_at')!r}")
    r.check(f"the no-change rescan finished in {waited:.1f}s (< {RESCAN_BUDGET_S:.0f} s)",
            waited < RESCAN_BUDGET_S,
            f"took {waited:.1f}s, over the {RESCAN_BUDGET_S:.0f} s budget")
    # It is a rescan of an unchanged tree, so it must not be a full one: a
    # `full` rescan would meet the budget for the wrong reason.
    r.eq("and it was incremental, not full", final.get("full"), False)
    return report(r)


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--base", required=True)
    ap.add_argument("--mode", choices=["real", "synthetic"], default="real")
    ap.add_argument("--phase",
                    choices=["main", "rescan", "roots-pre", "roots-post", "roots-delete"],
                    default="main")
    ap.add_argument("--new-root", help="the directory roots-pre adds (A-11)")
    ap.add_argument("--label", help="the label roots-pre sends, which is also the generated name")
    ap.add_argument("--state-file", help="where the roots-* phases hand state to each other")
    args = ap.parse_args()

    r = Runner(args.base)
    real = args.mode == "real"
    expected = CURATED + ([] if real else SYNTHETIC_EXTRA)

    # `rescan` runs in BOTH rounds and hands nothing to a later invocation, so
    # it is dispatched before the state-file guard the A-11 phases need.
    if args.phase == "rescan":
        return phase_rescan(r)

    if args.phase != "main":
        if not args.state_file:
            ap.error(f"--phase {args.phase} needs --state-file")
        if args.phase == "roots-pre":
            if not args.new_root or not args.label:
                ap.error("--phase roots-pre needs --new-root and --label")
            return phase_roots_pre(r, args.new_root, args.label, args.state_file)
        if args.phase == "roots-post":
            return phase_roots_post(r, args.state_file)
        return phase_roots_delete(r, args.state_file)

    print(f"\n-- curl assertions ({args.mode} subset) " + "-" * 34)

    # ---- the library -----------------------------------------------------
    by_name, listing = series_by_name(r)
    r.eq("the curated subset indexes exactly the include_globs entries",
         listing["total"], len(expected))
    missing = [n for n in expected if n not in by_name]
    r.check("every curated series is present", not missing, f"missing: {missing}")
    if missing:
        return report(r)

    kinds: dict[str, int] = {}
    for item in by_name.values():
        kinds[item["kind"]] = kinds.get(item["kind"], 0) + 1
    print(f"  note  series kinds: {kinds}")

    # ---- prd §2.2 row by row --------------------------------------------
    r.eq("row 1 · folder of ZIPs is kind=folder (Clover)", by_name[CLOVER]["kind"], "folder")
    r.eq("row 4 · a top-level ZIP is its own series (바퀴)", by_name[WHEEL]["kind"], "zip")
    r.eq("row 3 · loose images become one book (자살도)", by_name[SUICIDE]["book_count"], 1)

    if real:
        r.eq("Clover has four volumes", by_name[CLOVER]["book_count"], 4)
        r.eq("상처를 쫓는자 has eleven image folders", by_name[WOUNDS]["book_count"], 11)
        r.eq("자살도 holds 181 loose pages", by_name[SUICIDE]["page_count"], 181)
        r.eq("강철의 연금술사 has 27 volumes and no one-page book (D-5)",
             by_name[FMA]["book_count"], 27)
        r.eq("배틀로얄 is one 1 540-page volume (AC-008)",
             by_name[BATTLE_ROYALE]["page_count"], 1540)
    else:
        r.ge("배틀로얄 carries the AC-008 page count",
             by_name[BATTLE_ROYALE]["page_count"], 1540)

    # ---- FR-IDX-010: broken archives are isolated, the scan completes ----
    # D-10 as narrowed by ruling E-14: the nested archives are out of scope, so
    # the BOOK is `empty`; but the reader cannot open a single page of the
    # SERIES, so it is `error` with that book's reason. `empty` is reserved for
    # "no books at all".
    ah = r.json("/api/series/" + by_name[ANGEL_HEART]["id"])
    r.eq("엔젤하트 (a container of sub-ZIPs) is a series with status=error (E-14)",
         ah["status"], "error")
    r.check("the error series carries a reason the UI can show (design.md 화면 2)",
            bool(ah["error"]), f"error field: {ah['error']!r}")
    r.eq("엔젤하트's single book stays status=empty (D-10 is unchanged at book level)",
         [b["status"] for b in ah["books"]], ["empty"])
    # prd UI-002's 총 용량 is what the series occupies on disk, so a container
    # with no page rows at all must not read 0 KB.
    r.check("엔젤하트 reports its bytes on disk, not its (zero) page bytes",
            ah["total_bytes"] >= ah["books"][0]["file_size"] > 0,
            f"total_bytes {ah['total_bytes']}, file_size {ah['books'][0]['file_size']}")

    dn = r.json("/api/series/" + by_name[DNANGEL]["id"])
    dn_errors = [b for b in dn["books"] if b["status"] == "error"]
    r.eq("디엔엔젤 has exactly one unopenable volume (0-byte archive)", len(dn_errors), 1)
    r.check("the broken volume carries a reason the UI can show",
            bool(dn_errors and dn_errors[0]["error"]),
            f"error field: {dn_errors[0]['error'] if dn_errors else None!r}")
    r.check("the rest of 디엔엔젤 is readable — one failure does not fail the series",
            any(b["status"] == "ok" for b in dn["books"]))

    gg = r.json("/api/series/" + by_name[GUNGYE]["id"])
    gg_names = [b["name"] for b in gg["books"]]
    gg_errors = [b for b in gg["books"] if b["status"] == "error"]
    if real:
        r.ge("군계 lists every volume including the duplicates (E-5)", len(gg["books"]), 27)
        r.ge("군계's two truncated archives are isolated (FR-IDX-010)", len(gg_errors), 2)
    else:
        r.ge("군계's truncated archives are isolated (FR-IDX-010)", len(gg_errors), 2)
    r.check("군계 shows both the folder and the ZIP for 01권 (D-6, ruling E-5)",
            sum(1 for n in gg_names if "01권" in n) >= 2, f"volumes: {gg_names[:8]}")
    r.check("군계 uses its named cover file (arch §4.10 step 1)",
            by_name[GUNGYE]["has_cover"] is True)

    # ---- AC-004: a PDF series reads through the same flow ----------------
    ms = r.json("/api/series/" + by_name[MISAENG]["id"])
    pdf_books = [b for b in ms["books"] if b["kind"] == "pdf"]
    r.check("미생 is a series of PDF books (AC-004, prd §2.2 row 5)",
            len(pdf_books) == len(ms["books"]) and pdf_books != [],
            f"kinds: {[b['kind'] for b in ms['books']]}")
    # prd FR-LIB-003 / FR-LIB-009 / UI-002. A PDF's pages are rendered, not
    # stored, so `book.total_bytes` (the sum of uncompressed page bytes) is 0 by
    # construction — the 용량 the UI shows is the container size, and the series
    # rolls that up.
    r.check("미생 (PDF) reports a real 용량 rather than 0 KB",
            ms["total_bytes"] > 0 and all(b["file_size"] > 0 for b in pdf_books),
            f"series total_bytes {ms['total_bytes']}, "
            f"book file_size {[b['file_size'] for b in pdf_books][:3]}")
    r.check("미생's 용량 is the sum of its volumes on disk",
            ms["total_bytes"] >= sum(b["file_size"] for b in pdf_books),
            f"series {ms['total_bytes']} vs volumes {sum(b['file_size'] for b in pdf_books)}")
    if real:
        r.eq("미생 has nine volumes", len(ms["books"]), 9)
    pdf_ok = [b for b in pdf_books if b["status"] == "ok" and b["page_count"] > 0]
    if r.check("a PDF volume reports a plausible page count", bool(pdf_ok)):
        b = pdf_ok[0]
        status, body, headers = r.get(f"/api/books/{b['id']}/pages/1?v={b['cv']}&w=800")
        r.eq("AC-004 · a PDF page renders as image/jpeg through /pages/{n}",
             (status, headers.get("Content-Type")), (200, "image/jpeg"))
        r.ge("the rendered PDF page is a real image", len(body), 1000)

    # ---- AC-003: folder-type and ZIP-type read through one flow ----------
    for label, name in (("folder-of-ZIPs", CLOVER), ("single ZIP", WHEEL),
                        ("folder-of-images", WOUNDS)):
        detail = r.json("/api/series/" + by_name[name]["id"])
        ok_books = [b for b in detail["books"] if b["status"] == "ok"]
        if not r.check(f"AC-003 · {label} exposes a readable volume", bool(ok_books)):
            continue
        b = ok_books[0]
        book = r.json(f"/api/books/{b['id']}")
        same_shape = (
            isinstance(book.get("pages"), list) and len(book["pages"]) == book["page_count"]
            and all(k in book for k in ("prev_book_id", "next_book_id", "prefs", "dims_state"))
        )
        r.check(f"AC-003 · {label} book detail is shape-identical", same_shape)
        status, body, headers = r.get(f"/api/books/{b['id']}/pages/1?v={b['cv']}")
        r.eq(f"AC-003 · {label} page 1 streams", status, 200)
        r.check(f"AC-003 · {label} page 1 is an image",
                str(headers.get("Content-Type", "")).startswith("image/"),
                str(headers.get("Content-Type")))
        r.eq(f"FR-SRV-007 · {label} page with a matching ?v= is immutable",
             headers.get("Cache-Control"), "public, max-age=31536000, immutable")

    # ---- AC-002: no U+FFFD in a 500-name sample --------------------------
    sampled, bad = 0, []
    for name in expected:
        detail = r.json("/api/series/" + by_name[name]["id"])
        for b in detail["books"]:
            if sampled >= 500:
                break
            if b["status"] != "ok":
                continue
            book = r.json(f"/api/books/{b['id']}")
            for p in book["pages"]:
                if sampled >= 500:
                    break
                sampled += 1
                if "�" in p["name"]:
                    bad.append(p["name"])
        if sampled >= 500:
            break
    r.check(f"AC-002 · none of {sampled} sampled page names contains U+FFFD",
            not bad, f"first offenders: {bad[:5]}")

    # ---- AC-008: an arbitrary jump deep into a 1 540-page volume ---------
    br = r.json("/api/series/" + by_name[BATTLE_ROYALE]["id"])
    b = br["books"][0]
    book = r.json(f"/api/books/{b['id']}")
    r.eq("AC-008 · every page is returned in one request, so a jump needs no round trip",
         len(book["pages"]), b["page_count"])
    # AC-008 (prd §4) is 페이지 임의 점프 — an ARBITRARY jump, in a 500-page-plus
    # volume. So the page under measurement has to be one the server has never
    # served, and the page count the jump set is derived from is a precondition,
    # not an assumption: assert it rather than hard-coding around it.
    pc = b["page_count"]
    r.ge("AC-008 · the volume under measurement is the 500+-page one AC-008 names", pc, 500)
    # impl-plan §6.3 step 5 (docs/impl-plan.md:966) is the binding description of
    # THIS script — scripts/e2e.sh:531 names the step `7 · curl assertions
    # (impl-plan §6.3 step 5)` — and it spells out one page by number:
    # `GET /api/books/{battle_royale}/pages/900` returns `200 image/jpeg` in
    # < 200 ms. So page 900 joins the measured set unconditionally below, and
    # "the volume is deep enough to have a page 900" becomes a precondition to
    # assert. Guarding the page instead (`{900} if pc >= 900 else set()`) would
    # make a named requirement disappear on exactly the volume that broke it.
    r.ge("impl-plan §6.3 step 5 · the volume reaches the page 900 that step names", pc, 900)

    # There is nothing per page to warm. A ZIP page is streamed straight out of
    # the archive at a stored offset (serveArchivePage, internal/httpapi/pages.go)
    # and is never cached server-side, so the only warmable state is the openpool
    # handle for the container and the index rows behind GetBook — which is
    # exactly what "warm" means in the binding measurement I-8 (impl-plan §6),
    # whose reference implementation warms page 1 three times and then measures
    # fifty pages it never touched (integration/perf_test.go). Warm page 1 the
    # same way, and then never measure page 1.
    warm = [r.get(f"/api/books/{b['id']}/pages/1?v={b['cv']}") for _ in range(3)]
    # Everything below rests on those three having actually served. A stale
    # `?v=` answers 409 (this script asserts that behaviour a few lines further
    # down) and a wrong id answers 404; either way the openpool handle is still
    # cold and the first jump would be billed for opening a 1.34 GB container,
    # failing AC-008 for a reason that has nothing to do with AC-008. State the
    # assumption rather than making it — as the page-count precondition above
    # already does.
    r.eq("AC-008 · the three page-1 warm-ups the measurement depends on really served",
         [(status, str(headers.get("Content-Type", "")).startswith("image/"))
          for status, _, headers in warm],
         [(200, True)] * 3)

    # Page 900 by name (impl-plan §6.3 step 5, see the precondition above), plus
    # five evenly spread jumps derived from the page count. De-duplicated, so a
    # volume whose fifths happen to include 900 measures it once rather than
    # twice under two names. None of them has been served in this run: pc >= 900
    # makes the smallest derived page 180, so none is the page 1 just warmed, and
    # nothing earlier has asked for 900.
    jumps = sorted({(pc * k) // 5 for k in (1, 2, 3, 4, 5)} | {900})
    worst = 0.0
    for n in jumps:
        t0 = time.monotonic()
        status, body, headers = r.get(f"/api/books/{b['id']}/pages/{n}?v={b['cv']}")
        elapsed = (time.monotonic() - t0) * 1000
        worst = max(worst, elapsed)
        # Unconditional: a check that is only recorded when it fails makes the
        # assertion count report() prints depend on the outcome, and a passing
        # run then leaves no evidence that the jumped-to pages streamed at all.
        r.check(f"AC-008 · page {n} of {pc} streams on its first request ({elapsed:.0f} ms)",
                status == 200
                and str(headers.get("Content-Type", "")).startswith("image/")
                and len(body) > 0,
                f"{status} {headers.get('Content-Type')} {len(body)} bytes")
    # Time to LAST byte, not first: Runner.get drains the body. The 200 ms is a
    # ruling, not a habit — impl-plan §6.3 step 5 (docs/impl-plan.md:966) sets it
    # for this suite, on this book, naming page 900.
    #
    # I-8 (docs/impl-plan.md:890) pins the SAME acceptance criterion at "p95 TTFB
    # < 100 ms warm". impl-plan carries both numbers, so they are two
    # measurements of AC-008 rather than a contradiction, and the difference that
    # accounts for the gap is the STATISTIC: I-8 takes the 95th percentile of 50
    # jumps (integration/perf_test.go), which lets two slow samples out of fifty
    # go unpunished; this takes the worst of a handful, which has no percentile
    # to hide behind. A worst-case budget has to be the looser of the two.
    #
    # What does NOT account for the gap, and is recorded here so nobody rederives
    # it wrongly: (a) "warm". Both measurements warm page 1 three times and then
    # ask for pages nobody has requested, because the openpool handle is the only
    # warmable state (see above). (b) TTFB. I-8 is WORDED as time-to-first-byte,
    # but its reference implementation times integration/harness_test.go's
    # `do()`, which io.ReadAll's the body (harness_test.go:231) before the clock
    # stops — so it too measures the last byte. That wording gap belongs to
    # integration/, not here. Neither number is this script's to change.
    r.check(f"AC-008 / impl-plan §6.3 step 5 · the slowest of {len(jumps)} never-before-served "
            f"jumps (page 900 among them) took {worst:.0f} ms to the last byte (< 200 ms)",
            worst < 200)

    # ---- FR-SRV-007 / the ?v= matrix ------------------------------------
    status, _, headers = r.get(f"/api/books/{b['id']}/pages/1")
    r.eq("a page without ?v= is only cacheable for 60 s",
         headers.get("Cache-Control"), "public, max-age=60, must-revalidate")
    status, body, _ = r.get(f"/api/books/{b['id']}/pages/1?v=deadbeefdeadbeef")
    r.eq("a stale ?v= is 409 stale_version", status, 409)
    r.eq("409 carries the current cv so the client can recover",
         json.loads(body)["error"]["code"], "stale_version")
    status, _, _ = r.get(f"/api/books/{b['id']}/pages/0")
    r.check("there is no page 0 anywhere in this product", status in (400, 404), str(status))
    status, _, _ = r.get(f"/api/books/{b['id']}/pages/{b['page_count'] + 1}")
    r.eq("a page past the end is 404", status, 404)

    # ---- covers and thumbnails (FR-THM-003) ------------------------------
    #
    # FR-THM-003 is *pre*-generation: "시리즈 커버는 스캔 직후 우선 생성". The
    # distinguishing observation is therefore the FIRST request for a cover —
    # if the scan really made it, that request is a 200 and no client ever had
    # to ask. Retrying a 202 until it turns 200 measures the lazy path of
    # FR-THM-004 instead, and cannot tell the two apart: a scan that silently
    # failed 28 of its 36 covers passed that check because asking for them is
    # what generated them.
    #
    # The wait below covers the covers phase's own slack — it ends when the
    # queue is empty, so up to `thumbnails.workers` decodes may still be in
    # flight — and it waits on the server saying it is idle rather than on a
    # clock. It cannot hide a failed pre-generation, which never becomes a 200
    # at all; see wait_for_thumb_quiescence for why a sleep could not do this.
    covered = [s for s in by_name.values() if s["has_cover"]]
    r.ge("most of the subset has a cover", len(covered), len(expected) - 3)
    wait_for_thumb_quiescence(r)
    first_ready = 0
    ready = 0
    for s in covered:
        suffix = f"&v={s['cover_cv']}" if s.get("cover_cv") else ""
        url = f"/api/series/{s['id']}/cover?w=400{suffix}"
        status, body, headers = r.get(url)
        if status == 200 and str(headers.get("Content-Type", "")).startswith("image/"):
            first_ready += 1
        for _ in range(12):
            if status != 202:
                break
            time.sleep(float(headers.get("Retry-After", "1")))
            status, body, headers = r.get(url)
        if status == 200 and str(headers.get("Content-Type", "")).startswith("image/"):
            ready += 1
    r.eq("FR-THM-003 · the scan itself generated every cover (first request is 200, never 202)",
         first_ready, len(covered))
    r.eq("FR-THM-003 · every cover generated during the scan is servable", ready, len(covered))

    # Every cover above answered 200, so its file is on disk *now*. Mark that
    # moment on the server's clock: it is the oldest walk this check is willing
    # to believe. See check_thumb_cache for why the endpoint needs the guard.
    check_thumb_cache(r, int(server_clock(r)), len(covered))

    # ---- the rest of the contract ---------------------------------------
    r.eq("GET /api/roots reports the configured root", len(r.json("/api/roots")["items"]), 1)
    settings = r.json("/api/settings")
    r.eq("GET /api/settings mirrors thumbnails.widths",
         settings["server"]["thumbnail_widths"], [120, 240, 400, 640])

    # ---- A-11 / ruling E-26: the gate, in the position THIS round is in ---
    #
    # Both rounds run this, and the expectation is derived from the mode rather
    # than written down twice, for the same reason `expected` above is: the
    # rounds differ in configuration (scripts/e2e-config.sh emits
    # `server.allow_root_editing: true` for --synthetic only), so a config that
    # stopped matching its round has to fail HERE, by name, instead of turning
    # some other check into a tautology. The real round's browser tier asserts
    # the 추가/제거 controls are absent; this is the same fact stated as its
    # cause, and it is the only assertion in the real round that proves the
    # endpoints exist at all rather than being 404.
    #
    # The body is `{}` on purpose. With the gate shut the request never reaches
    # validation (403 disabled); with it open it reaches validation and stops at
    # the first rule (400 missing). Neither writes a byte — which matters, since
    # in the real round the file this would edit is `test/shelf.e2e.yaml` inside
    # the repository, and step 9 fails the run for touching it.
    want_editing = not real
    r.eq("A-11 · Settings.server.root_editing_enabled matches this round's configuration",
         settings["server"]["root_editing_enabled"], want_editing)
    status, body, _ = r.request("POST", "/api/roots", {})
    code, detail_map = envelope(body)
    r.eq("A-11 · POST /api/roots answers the gate itself, with the reason §7.4 specifies",
         (status, code, detail_map.get("reason")),
         (400, "bad_request", "missing") if want_editing else (403, "forbidden", "disabled"))
    status, body, _ = r.get("/api/nope")
    r.eq("an unknown /api/ path is a JSON 404, never the SPA", status, 404)
    r.eq("the 404 uses the §7.2 envelope", json.loads(body)["error"]["code"], "not_found")
    status, _, headers = r.get("/")
    r.eq("the SPA is served from the embedded bundle (NFR-OPS-001)", status, 200)
    r.check("the SPA is HTML", "text/html" in str(headers.get("Content-Type")))
    r.check("security headers are present (arch §8.4)",
            headers.get("X-Content-Type-Options") == "nosniff", str(headers.get("X-Content-Type-Options")))
    status, _, _ = r.get("/series/" + by_name[CLOVER]["id"])
    r.eq("a deep link falls back to index.html for the client router", status, 200)

    # FR-LIB-006 — 초성 search, server-side (C-10).
    hits = r.json("/api/series?q=" + urllib.parse.quote("ㄱㄱ"))
    r.check("FR-LIB-006 · 초성 search finds 군계",
            any(i["name"] == GUNGYE for i in hits["items"]),
            f"got: {[i['name'] for i in hits['items']][:5]}")

    return report(r)


def report(r: Runner) -> int:
    print()
    if r.failures:
        print(f"-- {len(r.failures)} of {r.checks} checks FAILED " + "-" * 30)
        for f in r.failures:
            print(f"     {f}")
        return 1
    print(f"-- all {r.checks} curl assertions passed " + "-" * 30)
    return 0


if __name__ == "__main__":
    sys.exit(main())
