"""scripts/start.sh and scripts/stop.sh against the same test doubles as
test_books.py: fake shelf/cloudflared/systemctl/sudo, a temporary config, data_dir,
state dir and cloudflared config, free loopback ports (never 9909/9910). The
first run is interactive, so it is driven through a pseudo-terminal."""
import os
import pty
import re
import select
import signal
import subprocess
import sys
import time
import unittest

import yaml

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from test_books import (BAD_CONFIGS, CF_CONFIG, FAKE_SHELF, Env, books_config,  # noqa: E402
                        free_port, load_yaml, sha)

HERE = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.dirname(HERE))))
START = os.path.join(REPO, "scripts", "start.sh")
STOP = os.path.join(REPO, "scripts", "stop.sh")
SUB = "3f2b9c1e-7a1d-4e55-9b0a-2c6d8e1f4a70"


class Pty:
    """A child on a pseudo-terminal: expect() output, send() answers."""

    def __init__(self, cmd, env):
        self.master, slave = pty.openpty()
        self.proc = subprocess.Popen(cmd, stdin=slave, stdout=slave, stderr=slave, env=env,
                                     start_new_session=True)
        os.close(slave)
        self.buf = b""
        self.pos = 0
        self.eof = False

    def _read(self, timeout):
        if self.eof:
            return False
        r, _, _ = select.select([self.master], [], [], timeout)
        if not r:
            return True
        try:
            data = os.read(self.master, 65536)
        except OSError:
            data = b""
        if not data:
            self.eof = True
            return False
        self.buf += data
        return True

    def expect(self, pattern, timeout=90):
        rx = re.compile(pattern.encode(), re.S)
        deadline = time.time() + timeout
        while True:
            m = rx.search(self.buf, self.pos)
            if m:
                self.pos = m.end()
                return m
            if time.time() > deadline or not self._read(0.5):
                if not self.eof:
                    self.close()
                raise AssertionError("did not see %r; output:\n%s" % (pattern, self.text()))

    def send(self, line):
        os.write(self.master, (line + "\n").encode())

    def finish(self, timeout=180):
        deadline = time.time() + timeout
        while not self.eof and time.time() < deadline:
            self._read(0.5)
        try:
            code = self.proc.wait(max(1, deadline - time.time()))
        finally:
            os.close(self.master)
        return code

    def close(self):
        if self.proc.poll() is None:
            os.killpg(self.proc.pid, signal.SIGTERM)
            self.proc.wait(10)

    def text(self):
        return self.buf.decode(errors="replace").replace("\r\n", "\n")

    def rest(self):
        return self.buf[self.pos:].decode(errors="replace")


def alive(pid):
    try:
        with open("/proc/%d/stat" % pid) as f:
            return f.read().split(") ")[1].split()[0] != "Z"
    except OSError:
        return False


class StartEnv(Env):
    def setUp(self):
        super().setUp()
        self.old_port = free_port()
        self.src = os.path.join(self.tmp, "shelf.yaml")
        self.write_cfg({
            "server": {"listen": "0.0.0.0", "port": self.old_port, "allow_root_editing": True},
            "roots": [{"name": "r", "path": "/nonexistent-root"}],
            "storage": {"data_dir": self.data, "cache_dir": self.data + "-cache"},
            "scan": {"on_start": False},
        }, self.src)
        os.remove(self.cfg)

    def start_cmd(self, *args, url=None):
        return [START, "--config", self.cfg, "--from", self.src, "--port", str(self.port),
                "--bin", FAKE_SHELF, "--workdir", self.tmp, "--state-dir", self.state,
                "--cf-config", self.cf, "--url", url or "http://127.0.0.1:%d" % self.port,
                "--wait", "10"] + list(args)

    def run_start(self, *args, env=None, url=None):
        return subprocess.run(self.start_cmd(*args, url=url), stdin=subprocess.DEVNULL, capture_output=True,
                              text=True, env=env or self.env(), timeout=300)

    def pty_start(self, *args, env=None, url=None):
        p = Pty(self.start_cmd(*args, url=url), env or self.env())
        self.addCleanup(p.close)
        return p

    def run_stop(self, env=None):
        cmd = [STOP, "--config", self.cfg, "--bin", FAKE_SHELF, "--workdir", self.tmp, "--state-dir", self.state]
        return subprocess.run(cmd, stdin=subprocess.DEVNULL, capture_output=True, text=True,
                              env=env or self.env(), timeout=120)

    def ready(self, subs=(SUB,)):
        """A finished setup: config with an allowed user, ingress routing books."""
        cfg = books_config(self.port, self.data)
        cfg["auth"]["oidc"]["allowed_subs"] = list(subs)
        self.write_cfg(cfg)
        self.route_books()

    def route_books(self):
        with open(self.cf, "w") as f:
            f.write(CF_CONFIG.replace("  # catch-all\n", "  - hostname: books.betterax.net\n"
                                      "    service: http://127.0.0.1:%d\n  # catch-all\n" % self.port))

    def cf_routes_books(self):
        return any(r.get("hostname") == "books.betterax.net" for r in load_yaml(self.cf)["ingress"])

    def config_backups(self):
        d = os.path.dirname(self.cfg)
        return sorted(os.path.join(d, n) for n in os.listdir(d) if n.startswith(os.path.basename(self.cfg) + ".bak-"))

    def first_run_until_sub(self, p, root_answer="off"):
        p.expect(r"allow_root_editing.*Type on or off: ")
        p.send(root_answer)
        p.expect(r"Cloudflare.*auth\.betterax\.net.*ohmyassets\.betterax\.net.*Type sudo: ")
        p.send("sudo")
        p.expect(r"Paste the sub.*: ")


class FirstRun(StartEnv):
    def test_first_run_walks_every_step_and_leaves_a_published_instance(self):
        p = self.pty_start()
        self.first_run_until_sub(p)
        p.send(SUB)
        p.expect(r"administrator\? Type yes or no: ")
        p.send("yes")
        p.expect(r"owner of the reading progress.*Type yes or no: ")
        p.send("yes")
        p.expect(r"allowed_subs.*Apply this change\? Type yes: ")
        p.send("yes")
        self.assertEqual(p.finish(), 0, p.text())
        out = p.text()
        self.assertIn("zip-viewer: running", out)
        cfg = load_yaml(self.cfg)
        self.assertEqual(cfg["auth"]["oidc"]["allowed_subs"], [SUB])
        self.assertEqual(cfg["auth"]["oidc"]["admin_subs"], [SUB])
        self.assertEqual(cfg["auth"]["oidc"]["legacy_owner_sub"], SUB)
        self.assertIs(cfg["server"]["allow_root_editing"], False)
        self.assertEqual(cfg["server"]["port"], self.port)
        self.assertTrue(self.listening())
        self.assertTrue(self.cf_routes_books())
        calls = self.calls_text()
        self.assertIn("sudo -v", calls)
        self.assertIn("tunnel-apply.sh", calls)
        self.assertIn("sudo systemctl restart cloudflared", calls)
        self.assertEqual(len(self.backups()), 2, "one data backup per start: first start and the restart")
        self.assertNotIn("Type its pid", out)

    def test_first_run_without_a_terminal_says_what_it_needs_and_changes_nothing(self):
        cf_before = sha(self.cf)
        r = self.run_start()
        self.assertEqual(r.returncode, 10, r.stdout + r.stderr)
        self.assertIn("scripts/start.sh", r.stdout)
        self.assertIn("terminal", r.stdout)
        self.assertFalse(os.path.exists(self.cfg))
        self.assertFalse(self.listening())
        self.assertEqual(sha(self.cf), cf_before)
        self.assertEqual(self.calls_text(), "")
        self.assertEqual(self.backups(), [])

    def test_declining_cloudflare_stops_and_the_next_run_resumes_without_repeating_questions(self):
        p = self.pty_start()
        p.expect(r"Type on or off: ")
        p.send("on")
        p.expect(r"Type sudo: ")
        p.send("no")
        self.assertEqual(p.finish(), 10, p.text())
        self.assertIn("scripts/start.sh", p.rest())
        self.assertTrue(self.listening())
        self.assertFalse(self.cf_routes_books())
        self.assertEqual(self.sudo_calls(), [])
        pid = self.pidfile_pid()
        p = self.pty_start()
        p.expect(r"Type sudo: ")
        self.assertNotIn("allow_root_editing", p.text())
        p.send("sudo")
        p.expect(r"Paste the sub.*: ")
        p.send(SUB)
        p.expect(r"Type yes or no: ")
        p.send("no")
        p.expect(r"Type yes or no: ")
        p.send("no")
        p.expect(r"Apply this change\? Type yes: ")
        p.send("yes")
        self.assertEqual(p.finish(), 0, p.text())
        self.assertNotEqual(self.pidfile_pid(), pid, "restarted with the new allowed_subs")
        cfg = load_yaml(self.cfg)
        self.assertIs(cfg["server"]["allow_root_editing"], True)
        self.assertEqual(cfg["auth"]["oidc"]["admin_subs"], [])

    def test_unsafe_config_stops_before_any_change_or_question(self):
        self.write_cfg(books_config(self.port, self.data, server__listen="0.0.0.0"))
        cfg_before = sha(self.cfg)
        p = self.pty_start()
        self.assertEqual(p.finish(), 3, p.text())
        self.assertNotIn("Type", p.text())
        self.assert_untouched(cfg_before)

    def assert_untouched(self, cfg_sha):
        self.assertIsNone(self.pidfile_pid())
        self.assertFalse(self.listening())
        self.assertEqual(self.backups(), [])
        self.assertEqual(sha(self.cfg), cfg_sha)
        self.assertEqual(self.sudo_calls(), [])

    def test_public_verify_failure_rolls_the_ingress_back(self):
        bad_port = free_port()
        bad_cfg = os.path.join(self.tmp, "open.yaml")
        self.write_cfg(books_config(bad_port, self.tmp + "/open-data"), bad_cfg)
        self.spawn_fake(bad_cfg, mode="open")
        cf_before = sha(self.cf)
        p = self.pty_start(url="http://127.0.0.1:%d" % bad_port)
        p.expect(r"Type on or off: ")
        p.send("off")
        p.expect(r"Type sudo: ")
        p.send("sudo")
        self.assertEqual(p.finish(), 8, p.text())
        self.assertEqual(sha(self.cf), cf_before)
        self.assertNotIn("Paste the sub", p.text())


class LaterRun(StartEnv):
    def test_later_run_asks_nothing_backs_up_starts_and_verifies(self):
        self.ready()
        r = self.run_start()
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertTrue(self.listening())
        self.assertEqual(len(self.backups()), 1)
        self.assertIn("zip-viewer: running", r.stdout)
        self.assertEqual(self.sudo_calls(), [])
        pid = self.pidfile_pid()
        r = self.run_start()
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertEqual(self.pidfile_pid(), pid, "already running: status only")
        self.assertEqual(len(self.backups()), 1)
        r = self.run_start("--restart")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertNotEqual(self.pidfile_pid(), pid)
        self.assertTrue(self.listening())
        self.assertEqual(self.sudo_calls(), [])

    def test_later_run_refuses_every_unsafe_config_and_changes_nothing(self):
        self.route_books()
        for name, over in BAD_CONFIGS.items():
            with self.subTest(name):
                cfg = books_config(self.port, self.data, **over)
                cfg["auth"]["oidc"]["allowed_subs"] = [SUB]
                self.write_cfg(cfg)
                before = sha(self.cfg)
                r = self.run_start()
                self.assertEqual(r.returncode, 3, r.stdout + r.stderr)
                self.assertFalse(self.listening())
                self.assertEqual(self.backups(), [])
                self.assertEqual(sha(self.cfg), before)
                self.assertEqual(self.sudo_calls(), [])

    def test_start_failure_leaves_nothing_running(self):
        self.ready()
        r = self.run_start(env=self.env(FAKE_SHELF_MODE="die"))
        self.assertEqual(r.returncode, 5, r.stdout + r.stderr)
        self.assertFalse(self.listening())
        self.assertIsNone(self.pidfile_pid())
        self.assertIn("recovery", r.stdout)


class ExistingServer(StartEnv):
    def old_server(self):
        old_cfg = os.path.join(self.tmp, "old.yaml")
        self.write_cfg(books_config(self.old_port, self.data, server__listen="127.0.0.1"), old_cfg)
        return self.spawn_fake(old_cfg)

    def bystander(self):
        p = subprocess.Popen(["sleep", "300"], cwd=self.tmp)
        self.procs.append(p)
        return p

    def test_without_a_terminal_an_old_server_is_never_stopped(self):
        self.ready()
        old = self.old_server()
        r = self.run_start()
        self.assertEqual(r.returncode, 4, r.stdout + r.stderr)
        self.assertIn(str(old.pid), r.stdout)
        self.assertIsNone(old.poll())
        self.assertFalse(self.listening())

    def test_an_old_server_stays_unless_its_pid_is_typed(self):
        self.ready()
        old = self.old_server()
        p = self.pty_start()
        p.expect(r"Type its pid to stop it.*: ")
        self.assertIn(str(old.pid), p.text())
        self.assertIn(self.tmp, p.text(), "cwd is shown")
        self.assertIn(str(self.old_port), p.text(), "its port is shown")
        p.send("yes")
        self.assertEqual(p.finish(), 10, p.text())
        self.assertIsNone(old.poll())
        self.assertFalse(self.listening())

    def test_a_pid_that_is_gone_by_the_time_it_is_typed_is_not_signalled(self):
        self.ready()
        old = self.old_server()
        other = self.bystander()
        p = self.pty_start()
        p.expect(r"Type its pid to stop it.*: ")
        old.terminate()
        old.wait(5)
        p.send(str(old.pid))
        self.assertEqual(p.finish(), 1, p.text())
        self.assertIn("no longer the process shown above", p.text())
        self.assertIsNone(other.poll())
        self.assertFalse(self.listening())

    def test_typing_the_pid_stops_only_that_server_and_prints_how_to_bring_it_back(self):
        self.ready()
        old = self.old_server()
        other = self.bystander()
        p = self.pty_start()
        p.expect(r"Type its pid to stop it.*: ")
        p.send(str(old.pid))
        self.assertEqual(p.finish(), 0, p.text())
        old.wait(5)
        self.assertEqual(old.returncode, -signal.SIGTERM)
        self.assertIsNone(other.poll())
        self.assertRegex(p.text().replace("'", ""), r"cd %s && nohup (\S+ )?%s --config %s" % (
            re.escape(self.tmp), re.escape(FAKE_SHELF), re.escape(os.path.join(self.tmp, "old.yaml"))))
        self.assertTrue(self.listening())


class SubChange(StartEnv):
    def setUp(self):
        super().setUp()
        self.ready(subs=())
        r = self.run_books("--yes", "start")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.pid = self.pidfile_pid()
        self.before = load_yaml(self.cfg)
        self.before_sha = sha(self.cfg)

    def answer_sub(self, p, sub=SUB):
        p.expect(r"Paste the sub.*: ")
        p.send(sub)
        p.expect(r"administrator\? Type yes or no: ")
        p.send("yes")
        p.expect(r"Type yes or no: ")
        p.send("no")
        p.expect(r"Apply this change\? Type yes: ")
        p.send("yes")

    def test_a_running_instance_with_ingress_is_only_asked_for_the_sub(self):
        p = self.pty_start()
        for bad in ("two words", '"quoted"'):
            p.expect(r"Paste the sub.*: ")
            p.send(bad)
            p.expect(r"not a valid sub")
        self.answer_sub(p)
        self.assertEqual(p.finish(), 0, p.text())
        self.assertNotIn("Type sudo", p.text())
        self.assertNotIn("allow_root_editing", p.text())
        after = load_yaml(self.cfg)
        expected = load_yaml(self.config_backups()[0])
        self.assertEqual(expected, self.before, "the backup is the config before the change")
        expected["auth"]["oidc"]["allowed_subs"] = [SUB]
        expected["auth"]["oidc"]["admin_subs"] = [SUB]
        self.assertEqual(after, expected, "no other key or value changed")
        self.assertNotEqual(self.pidfile_pid(), self.pid)
        self.assertTrue(self.listening())

    def test_three_invalid_subs_stop_without_writing(self):
        p = self.pty_start()
        for bad in ("", "it's", "a b"):
            p.expect(r"Paste the sub.*: ")
            p.send(bad)
            p.expect(r"not a valid sub")
        self.assertEqual(p.finish(), 10, p.text())
        self.assertEqual(sha(self.cfg), self.before_sha)

    def test_declining_the_change_writes_nothing(self):
        p = self.pty_start()
        p.expect(r"Paste the sub.*: ")
        p.send(SUB)
        p.expect(r"Type yes or no: ")
        p.send("no")
        p.expect(r"Type yes or no: ")
        p.send("no")
        p.expect(r"Apply this change\? Type yes: ")
        p.send("no")
        self.assertEqual(p.finish(), 10, p.text())
        self.assertEqual(sha(self.cfg), self.before_sha)
        self.assertEqual(self.config_backups(), [])
        self.assertEqual(self.pidfile_pid(), self.pid)

    def test_a_change_the_binary_rejects_is_rolled_back_and_the_instance_kept(self):
        p = self.pty_start(env=self.env(FAKE_SHELF_MODE="die"))
        self.answer_sub(p)
        self.assertEqual(p.finish(), 3, p.text())
        self.assertEqual(sha(self.cfg), self.before_sha)
        self.assertEqual(self.pidfile_pid(), self.pid)
        self.assertTrue(alive(self.pid))
        self.assertTrue(self.listening())

    def test_a_restart_that_fails_restores_the_config_and_brings_the_instance_back(self):
        p = self.pty_start(env=self.env(FAKE_SHELF_DIE_SUB=SUB))
        self.answer_sub(p)
        self.assertEqual(p.finish(), 5, p.text())
        self.assertEqual(sha(self.cfg), self.before_sha)
        self.assertTrue(self.listening())
        self.assertTrue(alive(self.pidfile_pid()))
        self.assertIn("restored", p.text())


class Stop(StartEnv):
    def test_stop_stops_the_managed_instance_and_start_brings_it_back(self):
        self.ready()
        self.assertEqual(self.run_start().returncode, 0)
        cf_before = sha(self.cf)
        r = self.run_stop()
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertFalse(self.listening())
        self.assertIsNone(self.pidfile_pid())
        self.assertIn("books.sh rollback", r.stdout)
        self.assertEqual(sha(self.cf), cf_before)
        self.assertEqual(self.calls_text(), "")
        r = self.run_start()
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertTrue(self.listening())

    def test_stop_without_a_managed_instance_stops_nothing(self):
        self.ready()
        bystander = subprocess.Popen(["sleep", "300"], cwd=self.tmp)
        self.procs.append(bystander)
        r = self.run_stop()
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertIn("nothing", r.stdout)
        self.assertIsNone(bystander.poll())

    def test_stop_never_stops_a_process_the_pidfile_does_not_own(self):
        self.ready()
        os.makedirs(self.state, exist_ok=True)
        other_cfg = os.path.join(self.tmp, "other.yaml")
        self.write_cfg(books_config(self.port, self.tmp + "/other-data"), other_cfg)
        auth_srv = os.path.join(self.tmp, "uni-auth.py")
        with open(auth_srv, "w") as f:
            f.write("import http.server,sys\nhttp.server.HTTPServer(('127.0.0.1', int(sys.argv[1])),"
                    " http.server.BaseHTTPRequestHandler).serve_forever()\n")
        cases = {
            "reused pid": lambda: subprocess.Popen(["sleep", "300"]),
            "uni-auth on the port": lambda: subprocess.Popen([sys.executable, auth_srv, str(self.port)]),
            "cloudflared": lambda: subprocess.Popen(["bash", "-c", "exec -a cloudflared sleep 300"]),
            "shelf with another config on the port": lambda: subprocess.Popen(
                [FAKE_SHELF, "--config", other_cfg], env=self.env()),
        }
        for name, spawn in cases.items():
            with self.subTest(name):
                proc = spawn()
                self.procs.append(proc)
                time.sleep(0.5)
                with open(os.path.join(self.state, "shelf.pid"), "w") as f:
                    f.write(str(proc.pid))
                r = self.run_stop()
                self.assertIn("nothing", r.stdout, r.stdout + r.stderr)
                time.sleep(0.2)
                self.assertIsNone(proc.poll(), "%s was stopped" % name)
                proc.terminate()
                proc.wait(5)


if __name__ == "__main__":
    unittest.main()
