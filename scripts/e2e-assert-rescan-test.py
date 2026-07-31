#!/usr/bin/env python3
"""Tests for `e2e-assert.py --phase rescan` — scripts/e2e.sh step 8.

    scripts/e2e-assert-rescan-test.py

Why a test for a gate step: step 8 spent four sessions reporting PASS while no
rescan ran (open item H, HANDOFF §5.6.1 H and §5.7). The old shape watched the
wall clock -- `POST /api/scan` with its exit status discarded, then
`wait_for_idle`, which returns immediately because step 6 already left the
scanner idle. A POST that answered 500 and a POST that answered 202 without
starting anything both printed "the no-change rescan finished in 0s (< 30 s)".

So the replacement is not trusted on inspection. Every case below is a server
that is broken in one specific way, and the test asserts the phase FAILS on it.
The first two are the two states that used to pass; `test_healthy_round_passes`
is the one that must not have been made unreachable by the rest.

This is the §6.5 discipline applied to the check itself: a check nothing tests
is a check that watches whatever it happens to watch.

No server, no database and no fixture tree -- the stub is 60 lines of
http.server, so this runs in about a second and belongs in any tier.
"""

from __future__ import annotations

import json
import os
import subprocess
import sys
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer

HERE = os.path.dirname(os.path.abspath(__file__))
ASSERT = os.path.join(HERE, "e2e-assert.py")

PREV_RUN = "run-step6-full"
NEW_RUN = "run-step8-incremental"


def status_body(run_id, state="idle", full=False, finished_at=1032):
    """A §7.10 ScanStatus. Only the fields phase_rescan reads have to be real."""
    return {
        "state": state,
        "run_id": run_id,
        "full": full,
        "started_at": 1000,
        "finished_at": finished_at,
        "roots": ["e2e"],
        "current_root": None,
        "current_item": None,
        "total": 10,
        "done": 10,
        "errors": 0,
        "covers_total": 10,
        "covers_done": 10,
        "elapsed_ms": 900,
        "eta_ms": None,
        "last_error": None,
    }


class Stub:
    """A SHELF server that is wrong in exactly one way.

    `plan` is the list of status payloads handed out in order, the last one
    repeating forever, so a case can reproduce the publish window in which
    `Scanner.Start` has returned an id the snapshot does not carry yet.
    """

    def __init__(self, post_status=202, post_body=None, plan=None):
        self.post_status = post_status
        self.post_body = post_body if post_body is not None else {"run_id": NEW_RUN}
        self.plan = plan or [status_body(PREV_RUN), status_body(NEW_RUN)]
        self.polls = 0
        self.posts = 0
        stub = self

        class H(BaseHTTPRequestHandler):
            def log_message(self, *a):
                pass

            def _send(self, code, payload):
                body = json.dumps(payload).encode()
                self.send_response(code)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def do_GET(self):
                if self.path.startswith("/api/scan/status"):
                    i = min(stub.polls, len(stub.plan) - 1)
                    stub.polls += 1
                    self._send(200, stub.plan[i])
                else:
                    self._send(404, {"error": {"code": "not_found", "message": "stub"}})

            def do_POST(self):
                self.rfile.read(int(self.headers.get("Content-Length") or 0))
                if self.path == "/api/scan":
                    stub.posts += 1
                    self._send(stub.post_status, stub.post_body)
                else:
                    self._send(404, {"error": {"code": "not_found", "message": "stub"}})

        self.httpd = HTTPServer(("127.0.0.1", 0), H)
        self.base = "http://127.0.0.1:%d" % self.httpd.server_address[1]

    def __enter__(self):
        threading.Thread(target=self.httpd.serve_forever, daemon=True).start()
        return self

    def __exit__(self, *exc):
        self.httpd.shutdown()
        self.httpd.server_close()


def run_phase(base, **env_over):
    """Run the SHIPPED entry point, not an imported function."""
    env = dict(os.environ)
    # Keep the never-published and over-budget cases sub-second. Cases that do
    # not pass these get the real defaults.
    env.setdefault("SHELF_E2E_RESCAN_LIMIT_S", "2")
    env.update({k: str(v) for k, v in env_over.items()})
    p = subprocess.run(
        [sys.executable, ASSERT, "--base", base, "--mode", "synthetic", "--phase", "rescan"],
        capture_output=True, text=True, env=env,
    )
    return p.returncode, p.stdout + p.stderr


# --- the two states that used to pass ---------------------------------------

def test_post_that_fails_is_not_a_pass():
    """A 500 from POST /api/scan. The old step printed `curl: (22)` and PASSed."""
    with Stub(post_status=500,
              post_body={"error": {"code": "internal", "message": "injected"}}) as s:
        code, out = run_phase(s.base)
    assert code == 1, "a rescan that could not be started must fail the gate\n" + out
    assert "POST /api/scan is accepted" in out, out


def test_202_that_starts_nothing_is_not_a_pass():
    """A 202 carrying step 6's run id: answered, but nothing rescanned."""
    with Stub(post_body={"run_id": PREV_RUN},
              plan=[status_body(PREV_RUN)]) as s:
        code, out = run_phase(s.base)
    assert code == 1, "a 202 that re-uses the finished run's id must fail\n" + out
    assert "the rescan is a NEW run" in out, out


# --- and the states the new shape has to catch too ---------------------------

def test_run_that_never_becomes_the_published_one_fails():
    """202 with a fresh id, but the snapshot never carries it: no run began."""
    with Stub(plan=[status_body(PREV_RUN)]) as s:
        code, out = run_phase(s.base)
    assert code == 1, "a run that never reaches the status endpoint must fail\n" + out
    assert "the rescan we started is the run that finished" in out, out


def test_scanner_busy_before_the_rescan_fails():
    """Step 6 still running: the budget would time the wrong scan."""
    with Stub(plan=[status_body(PREV_RUN, state="walking", finished_at=None)]) as s:
        code, out = run_phase(s.base)
    assert code == 1, "a non-idle precondition must be asserted, not tolerated\n" + out
    assert "the scanner is idle before the rescan" in out, out


def test_missing_prior_run_fails():
    """No step 6 to compare against: 'a new id appeared' would prove nothing."""
    with Stub(plan=[status_body(None, finished_at=None)]) as s:
        code, out = run_phase(s.base)
    assert code == 1, "an absent prior run must fail rather than pass vacuously\n" + out
    assert "step 6 left a finished run" in out, out


def test_full_rescan_is_not_incremental():
    """NFR-PRF-004 is about the INCREMENTAL path; a full run meeting the budget
    would satisfy the clock for the wrong reason."""
    with Stub(plan=[status_body(PREV_RUN), status_body(NEW_RUN, full=True)]) as s:
        code, out = run_phase(s.base)
    assert code == 1, "a full rescan must not satisfy NFR-PRF-004\n" + out
    assert "incremental, not full" in out, out


def test_over_budget_rescan_fails():
    """The original check's one real assertion still has to fire."""
    plan = [status_body(PREV_RUN)] * 4 + [status_body(NEW_RUN)]
    with Stub(plan=plan) as s:
        code, out = run_phase(s.base, SHELF_E2E_RESCAN_BUDGET_S="0.1",
                              SHELF_E2E_RESCAN_LIMIT_S="10")
    assert code == 1, "a rescan over the budget must fail\n" + out
    assert "over the" in out and "budget" in out, out


def test_healthy_round_passes():
    """None of the above may have made a good round unpassable."""
    plan = [
        status_body(PREV_RUN),                                   # publish window
        status_body(NEW_RUN, state="walking", finished_at=None),  # running
        status_body(NEW_RUN),                                     # finished
    ]
    with Stub(plan=plan) as s:
        code, out = run_phase(s.base)
    assert code == 0, "a healthy incremental rescan must pass\n" + out
    assert "the rescan we started is the run that finished" in out, out


def test_shipped_budget_and_limit_are_the_documented_ones():
    """The env overrides above exist for this test. They must not have moved
    the defaults the gate actually runs at."""
    src = open(ASSERT, encoding="utf-8").read()
    assert 'os.environ.get("SHELF_E2E_RESCAN_BUDGET_S", "30")' in src, \
        "NFR-PRF-004's 30 s budget is no longer the default"
    assert 'os.environ.get("SHELF_E2E_RESCAN_LIMIT_S", "120")' in src, \
        "the 120 s limit is no longer the default"


def main() -> int:
    tests = [(n, f) for n, f in sorted(globals().items())
             if n.startswith("test_") and callable(f)]
    failed = []
    for name, fn in tests:
        try:
            fn()
            print(f"  PASS  {name}")
        except AssertionError as e:
            print(f"  FAIL  {name}\n          {e}")
            failed.append(name)
    print()
    if failed:
        print(f"-- {len(failed)} of {len(tests)} rescan-phase tests FAILED " + "-" * 20)
        return 1
    print(f"-- all {len(tests)} rescan-phase tests passed " + "-" * 24)
    return 0


if __name__ == "__main__":
    sys.exit(main())
