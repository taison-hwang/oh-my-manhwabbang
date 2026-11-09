"""books.sh against test doubles: a fake shelf on a free loopback port (never
9909/9910), a temporary config, data_dir, state dir and cloudflared config, and
fake cloudflared/systemctl/sudo first on PATH. Nothing here reaches /etc, the
real cloudflared, uni-auth or a running shelf."""
import hashlib
import glob
import os
import shutil
import signal
import socket
import subprocess
import tempfile
import time
import unittest

import yaml

HERE = os.path.dirname(os.path.abspath(__file__))
LIB = os.path.dirname(HERE)
BOOKS = os.path.join(LIB, "books.sh")
FAKES = os.path.join(HERE, "fakes")
FAKE_SHELF = os.path.join(FAKES, "shelf")
RESERVED = {9909, 9910}

CF_CONFIG = """\
tunnel: 178647b8-b76b-49a3-8b8f-3ca06f191226
credentials-file: /etc/cloudflared/178647b8-b76b-49a3-8b8f-3ca06f191226.json
ingress:
  - hostname: auth.betterax.net
    service: https://127.0.0.1:8443
  - hostname: ohmyassets.betterax.net
    service: http://127.0.0.1:18100
  # catch-all
  - service: http_status:404
"""


def load_yaml(path):
    with open(path) as f:
        return yaml.safe_load(f)


def free_port():
    while True:
        s = socket.socket()
        s.bind(("127.0.0.1", 0))
        p = s.getsockname()[1]
        s.close()
        if p not in RESERVED:
            return p


def sha(path):
    with open(path, "rb") as f:
        return hashlib.sha256(f.read()).hexdigest()


def books_config(port, data_dir, **over):
    cfg = {
        "server": {"listen": "127.0.0.1", "port": port, "base_path": "", "trusted_proxy_headers": False},
        "roots": [{"name": "r", "path": "/nonexistent-root"}],
        "storage": {"data_dir": data_dir, "cache_dir": data_dir + "-cache"},
        "auth": {"mode": "oidc", "session_ttl": "12h", "oidc": {
            "issuer": "https://auth.betterax.net",
            "client_id": "zip-viewer",
            "audience": "https://books.betterax.net/api",
            "redirect_url": "https://books.betterax.net/api/auth/oidc/callback",
            "allowed_subs": [], "admin_subs": []}},
    }
    for dotted, value in over.items():
        cur = cfg
        keys = dotted.split("__")
        for k in keys[:-1]:
            cur = cur[k]
        cur[keys[-1]] = value
    return cfg


class Env(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp(prefix="books-test-")
        self.port = free_port()
        self.data = os.path.join(self.tmp, "data")
        os.makedirs(self.data)
        for name, body in (("user.db", b"user-v4"), ("index.db", b"index" * 1000), ("user.db-wal", b"wal")):
            with open(os.path.join(self.data, name), "wb") as f:
                f.write(body)
        self.state = os.path.join(self.tmp, "state")
        self.cf = os.path.join(self.tmp, "cf", "config.yml")
        os.makedirs(os.path.dirname(self.cf))
        with open(self.cf, "w") as f:
            f.write(CF_CONFIG)
        self.calls = os.path.join(self.tmp, "calls.log")
        open(self.calls, "w").close()
        self.cfg = os.path.join(self.tmp, "shelf.books.yaml")
        self.write_cfg(books_config(self.port, self.data))
        self.procs = []
        self.addCleanup(self.cleanup)

    def cleanup(self):
        for p in self.procs:
            if p.poll() is None:
                p.terminate()
                p.wait(5)
        # Instances books.sh started outlive their pidfile when a test rewrites it.
        mine = [pid for pid in pids_with_arg(self.tmp) if pid != os.getpid()]
        for pid in mine:
            try:
                os.kill(pid, signal.SIGTERM)
            except ProcessLookupError:
                pass
        for _ in range(50):
            if not any(alive(pid) for pid in mine):
                break
            time.sleep(0.1)
        shutil.rmtree(self.tmp, ignore_errors=True)
        self.assertEqual([p for p in pids_with_arg(self.tmp) if alive(p)], [], "test left processes running")

    def write_cfg(self, cfg, path=None):
        with open(path or self.cfg, "w") as f:
            yaml.safe_dump(cfg, f)

    def env(self, **extra):
        e = dict(os.environ)
        e["PATH"] = FAKES + os.pathsep + e["PATH"]
        e["FAKE_CALLS"] = self.calls
        e.update(extra)
        return e

    def run_books(self, *args, cfg=None, url=None, env=None, timeout=120):
        cmd = [BOOKS, "--config", cfg or self.cfg, "--bin", FAKE_SHELF, "--workdir", self.tmp,
               "--state-dir", self.state, "--cf-config", self.cf,
               "--url", url or "http://127.0.0.1:%d" % self.port, "--wait", "10"] + list(args)
        return subprocess.run(cmd, stdin=subprocess.DEVNULL, capture_output=True, text=True,
                              env=env or self.env(), timeout=timeout)

    def pidfile_pid(self):
        try:
            with open(os.path.join(self.state, "shelf.pid")) as f:
                return int(f.read().strip())
        except (OSError, ValueError):
            return None

    def listening(self, port=None):
        try:
            socket.create_connection(("127.0.0.1", port or self.port), 0.5).close()
            return True
        except OSError:
            return False

    def backups(self):
        return sorted(glob.glob(self.data + ".bak-*"))

    def sudo_calls(self):
        with open(self.calls) as f:
            return [line for line in f if line.startswith("sudo ")]

    def calls_text(self):
        with open(self.calls) as f:
            return f.read()

    def spawn_fake(self, cfg_path, mode="good"):
        p = subprocess.Popen([FAKE_SHELF, "--config", cfg_path], cwd=self.tmp,
                             env=self.env(FAKE_SHELF_MODE=mode),
                             stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        self.procs.append(p)
        port = load_yaml(cfg_path)["server"]["port"]
        for _ in range(50):
            if self.listening(port):
                return p
            time.sleep(0.1)
        self.fail("fake shelf did not listen")

    def start_ok(self):
        r = self.run_books("--yes", "start")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertTrue(self.listening())
        return self.pidfile_pid()

    def assert_nothing_changed(self, cfg_sha, r):
        self.assertNotEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertIsNone(self.pidfile_pid())
        self.assertFalse(self.listening())
        self.assertEqual(self.backups(), [])
        self.assertEqual(sha(self.cfg), cfg_sha)
        self.assertEqual(self.sudo_calls(), [])


def pids_with_arg(fragment):
    for name in os.listdir("/proc"):
        if not name.isdigit():
            continue
        try:
            with open("/proc/%s/cmdline" % name, "rb") as f:
                if fragment.encode() in f.read():
                    yield int(name)
        except OSError:
            pass


def alive(pid):
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return False
    try:
        with open("/proc/%d/stat" % pid) as f:
            return f.read().split(") ", 1)[1][0] != "Z"
    except OSError:
        return False


BAD_CONFIGS = {
    "auth.mode is password": {"auth__mode": "password"},
    "listen is not loopback": {"server__listen": "0.0.0.0"},
    "issuer differs": {"auth__oidc__issuer": "https://evil.example"},
    "client_id differs": {"auth__oidc__client_id": "other"},
    "audience is the old library host": {"auth__oidc__audience": "https://library.betterax.net/api"},
    "redirect_url differs": {"auth__oidc__redirect_url": "https://books.betterax.net/x/api/auth/oidc/callback"},
}


class PublicExposureGuard(Env):
    def test_start_refuses_every_unsafe_config_and_changes_nothing(self):
        for name, over in BAD_CONFIGS.items():
            with self.subTest(name):
                self.write_cfg(books_config(self.port, self.data, **over))
                before = sha(self.cfg)
                r = self.run_books("--yes", "start")
                self.assertEqual(r.returncode, 3, r.stdout + r.stderr)
                self.assert_nothing_changed(before, r)

    def test_config_without_auth_block_is_refused(self):
        cfg = books_config(self.port, self.data)
        del cfg["auth"]
        self.write_cfg(cfg)
        before = sha(self.cfg)
        r = self.run_books("--yes", "start")
        self.assertEqual(r.returncode, 3)
        self.assert_nothing_changed(before, r)

    def test_restart_refuses_unsafe_new_config_and_keeps_the_old_process(self):
        pid = self.start_ok()
        for name, over in BAD_CONFIGS.items():
            with self.subTest(name):
                cfg = books_config(self.port, self.data, **over)
                self.write_cfg(cfg)
                r = self.run_books("--yes", "restart")
                self.assertEqual(r.returncode, 3, r.stdout + r.stderr)
                self.assertTrue(alive(pid))
                self.assertEqual(self.pidfile_pid(), pid)
                self.assertTrue(self.listening())

    def test_tunnel_apply_refuses_unsafe_config_without_sudo_or_ingress_change(self):
        self.start_ok()
        cf_before = sha(self.cf)
        self.write_cfg(books_config(self.port, self.data, auth__oidc__audience="https://library.betterax.net/api"))
        r = self.run_books("--yes-sudo", "tunnel", "apply")
        self.assertEqual(r.returncode, 3, r.stdout + r.stderr)
        self.assertEqual(sha(self.cf), cf_before)
        self.assertEqual(self.sudo_calls(), [])

    def test_tunnel_apply_needs_a_clean_local_verify(self):
        self.spawn_fake(self.cfg, mode="open")
        cf_before = sha(self.cf)
        r = self.run_books("--yes-sudo", "tunnel", "apply")
        self.assertEqual(r.returncode, 7, r.stdout + r.stderr)
        self.assertIn("FAIL", r.stdout)
        self.assertEqual(sha(self.cf), cf_before)
        self.assertEqual(self.sudo_calls(), [])

    def test_tunnel_apply_without_a_running_instance_is_refused(self):
        cf_before = sha(self.cf)
        r = self.run_books("--yes-sudo", "tunnel", "apply")
        self.assertNotEqual(r.returncode, 0)
        self.assertEqual(sha(self.cf), cf_before)
        self.assertEqual(self.sudo_calls(), [])


class DataAndConfigPreserved(Env):
    def test_start_backs_up_data_dir_with_wal_and_never_writes_the_config(self):
        originals = {n: sha(os.path.join(self.data, n)) for n in ("user.db", "index.db", "user.db-wal")}
        before = sha(self.cfg)
        self.start_ok()
        (bak,) = self.backups()
        for name, digest in originals.items():
            self.assertEqual(sha(os.path.join(bak, name)), digest, name)
        r = self.run_books("--yes", "stop")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertEqual(self.backups(), [bak])
        self.assertEqual(sha(os.path.join(self.data, "index.db")), originals["index.db"])
        self.assertEqual(sha(self.cfg), before)

    def test_start_is_refused_while_another_process_holds_data_dir_files(self):
        holder = subprocess.Popen(["python3", "-c",
                                   "import sys,time; f=open(sys.argv[1],'rb'); time.sleep(60)",
                                   os.path.join(self.data, "index.db")])
        self.procs.append(holder)
        time.sleep(0.3)
        before = sha(self.cfg)
        r = self.run_books("--yes", "start")
        self.assertEqual(r.returncode, 4, r.stdout + r.stderr)
        self.assertIn(str(holder.pid), r.stdout + r.stderr)
        self.assert_nothing_changed(before, r)

    def test_start_is_refused_while_an_old_shelf_on_another_port_uses_the_same_data_dir(self):
        old_cfg = os.path.join(self.tmp, "shelf.yaml")
        self.write_cfg(books_config(free_port(), self.data, server__listen="127.0.0.1"), old_cfg)
        old = self.spawn_fake(old_cfg)
        before = sha(self.cfg)
        r = self.run_books("--yes", "start")
        self.assertEqual(r.returncode, 4, r.stdout + r.stderr)
        self.assertIn(str(old.pid), r.stdout + r.stderr)
        self.assert_nothing_changed(before, r)
        self.assertIsNone(old.poll())

    def test_init_refuses_an_existing_target_and_leaves_it_byte_identical(self):
        before = sha(self.cfg)
        r = self.run_books("init", "--from", self.cfg)
        self.assertEqual(r.returncode, 9, r.stdout + r.stderr)
        self.assertEqual(sha(self.cfg), before)

    def test_init_writes_a_new_config_that_passes_check(self):
        old = os.path.join(self.tmp, "shelf.yaml")
        self.write_cfg({"server": {"listen": "0.0.0.0", "port": 9909, "allow_root_editing": True},
                        "roots": [{"name": "r", "path": "/nonexistent-root"}],
                        "storage": {"data_dir": "data", "cache_dir": "cache"}}, old)
        old_before = sha(old)
        new = os.path.join(self.tmp, "new.yaml")
        r = self.run_books("init", "--from", old, cfg=new)
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertEqual(sha(old), old_before)
        got = load_yaml(new)
        self.assertEqual(got["server"]["listen"], "127.0.0.1")
        self.assertEqual(got["server"]["port"], 9910)
        self.assertEqual(got["storage"], {"data_dir": "data", "cache_dir": "cache"})
        self.assertEqual(got["auth"]["mode"], "oidc")
        self.assertEqual(got["auth"]["oidc"]["audience"], "https://books.betterax.net/api")
        self.assertEqual(got["auth"]["oidc"]["allowed_subs"], [])


class SafeFailureAndRecovery(Env):
    def test_start_stops_the_new_instance_when_it_never_becomes_healthy(self):
        r = self.run_books("--yes", "start", env=self.env(FAKE_SHELF_MODE="die"))
        self.assertEqual(r.returncode, 5, r.stdout + r.stderr)
        self.assertIsNone(self.pidfile_pid())
        self.assertFalse(self.listening())
        self.assertIn("backup", r.stdout.lower())

    def test_start_stops_the_new_instance_when_local_verify_fails(self):
        r = self.run_books("--yes", "start", env=self.env(FAKE_SHELF_MODE="open"))
        self.assertEqual(r.returncode, 5, r.stdout + r.stderr)
        self.assertIn("FAIL", r.stdout)
        self.assertIsNone(self.pidfile_pid())
        for _ in range(50):
            if not self.listening():
                break
            time.sleep(0.1)
        self.assertFalse(self.listening())

    def test_stop_refuses_a_pidfile_that_points_at_an_unrelated_process(self):
        self.start_ok()
        other = subprocess.Popen(["sleep", "60"])
        self.procs.append(other)
        with open(os.path.join(self.state, "shelf.pid"), "w") as f:
            f.write(str(other.pid))
        r = self.run_books("--yes", "stop")
        self.assertEqual(r.returncode, 6, r.stdout + r.stderr)
        self.assertIsNone(other.poll())
        self.assertTrue(self.listening())

    def test_stop_refuses_when_the_process_runs_a_different_config(self):
        pid = self.start_ok()
        copy = os.path.join(self.tmp, "copy.yaml")
        shutil.copy(self.cfg, copy)
        r = self.run_books("--yes", "stop", cfg=copy)
        self.assertEqual(r.returncode, 6, r.stdout + r.stderr)
        self.assertTrue(alive(pid))

    def test_stop_refuses_a_matching_process_that_does_not_own_the_port(self):
        self.start_ok()
        stray_cfg = os.path.join(self.tmp, "stray.yaml")
        self.write_cfg(books_config(free_port(), self.tmp + "/stray-data"), stray_cfg)
        stray = self.spawn_fake(stray_cfg)
        self.write_cfg(books_config(free_port(), self.tmp + "/stray-data"), stray_cfg)
        with open(os.path.join(self.state, "shelf.pid"), "w") as f:
            f.write(str(stray.pid))
        r = self.run_books("--yes", "stop", cfg=stray_cfg)
        self.assertEqual(r.returncode, 6, r.stdout + r.stderr)
        self.assertIsNone(stray.poll())

    def test_stop_terminates_only_the_matching_instance(self):
        pid = self.start_ok()
        r = self.run_books("--yes", "stop")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertFalse(alive(pid))
        self.assertIsNone(self.pidfile_pid())

    def test_restart_with_a_valid_config_replaces_the_instance(self):
        pid = self.start_ok()
        r = self.run_books("--yes", "restart")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertFalse(alive(pid))
        self.assertNotEqual(self.pidfile_pid(), pid)
        self.assertTrue(self.listening())
        self.assertEqual(len(self.backups()), 2)

    def test_changes_need_confirmation(self):
        before = sha(self.cfg)
        r = self.run_books("start")
        self.assertEqual(r.returncode, 10, r.stdout + r.stderr)
        self.assert_nothing_changed(before, r)

    def test_yes_does_not_confirm_sudo_steps(self):
        self.start_ok()
        cf_before = sha(self.cf)
        r = self.run_books("--yes", "tunnel", "apply")
        self.assertEqual(r.returncode, 10, r.stdout + r.stderr)
        self.assertEqual(sha(self.cf), cf_before)
        self.assertEqual(self.sudo_calls(), [])

    def test_dry_run_changes_nothing(self):
        before = sha(self.cfg)
        r = self.run_books("--dry-run", "--yes", "start")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assert_nothing_changed_ok(before)
        pid = self.start_ok()
        nbak = len(self.backups())

        def dry(*args):
            cf_before, calls_before = sha(self.cf), self.calls_text()
            r = self.run_books("--dry-run", "--yes", "--yes-sudo", *args)
            self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
            self.assertIn("would:", r.stdout)
            self.assertTrue(alive(pid))
            self.assertEqual(self.pidfile_pid(), pid)
            self.assertEqual(sha(self.cf), cf_before)
            self.assertEqual(len(self.backups()), nbak)
            self.assertEqual(self.calls_text().count("sudo "), calls_before.count("sudo "))
            self.assertEqual(self.calls_text().count("systemctl restart"), calls_before.count("systemctl restart"))

        for args in (("stop",), ("restart",), ("tunnel", "apply"), ("rollback", "--backup", self.cf)):
            with self.subTest(args):
                dry(*args)
        self.assertEqual(self.run_books("--yes-sudo", "tunnel", "apply").returncode, 0)
        with self.subTest("tunnel restart"):
            dry("tunnel", "restart")

    def assert_nothing_changed_ok(self, cfg_sha):
        self.assertIsNone(self.pidfile_pid())
        self.assertFalse(self.listening())
        self.assertEqual(self.backups(), [])
        self.assertEqual(sha(self.cfg), cfg_sha)

    def test_tunnel_apply_then_restart_publishes_through_sudo_steps(self):
        self.start_ok()
        cf_before = sha(self.cf)
        r = self.run_books("--yes-sudo", "tunnel", "apply")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        doc = load_yaml(self.cf)
        self.assertIn({"hostname": "books.betterax.net", "service": "http://127.0.0.1:%d" % self.port,
                       "originRequest": {"httpHostHeader": "books.betterax.net", "connectTimeout": "10s"}},
                      doc["ingress"])
        self.assertNotIn("systemctl restart", self.calls_text())
        self.assertNotEqual(sha(self.cf), cf_before)
        r = self.run_books("--yes-sudo", "tunnel", "restart")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertIn("sudo systemctl restart cloudflared", self.calls_text())

    def test_public_verify_failure_rolls_the_ingress_back(self):
        self.start_ok()
        cf_before = sha(self.cf)
        r = self.run_books("--yes-sudo", "tunnel", "apply")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        bad_port = free_port()
        bad_cfg = os.path.join(self.tmp, "bad.yaml")
        self.write_cfg(books_config(bad_port, self.tmp + "/bad-data"), bad_cfg)
        self.spawn_fake(bad_cfg, mode="open")
        r = self.run_books("--yes-sudo", "tunnel", "restart", url="http://127.0.0.1:%d" % bad_port)
        self.assertEqual(r.returncode, 8, r.stdout + r.stderr)
        self.assertEqual(sha(self.cf), cf_before)
        self.assertEqual(self.calls_text().count("sudo systemctl restart cloudflared"), 2)

    def test_cloudflared_not_active_after_restart_rolls_back(self):
        self.start_ok()
        cf_before = sha(self.cf)
        self.assertEqual(self.run_books("--yes-sudo", "tunnel", "apply").returncode, 0)
        r = self.run_books("--yes-sudo", "tunnel", "restart", env=self.env(FAKE_CF_INACTIVE="1"))
        self.assertNotEqual(r.returncode, 0)
        self.assertEqual(sha(self.cf), cf_before)


class Help(unittest.TestCase):
    def test_help_names_sudo_steps_and_the_services_a_restart_drops(self):
        r = subprocess.run([BOOKS, "--help"], capture_output=True, text=True)
        self.assertEqual(r.returncode, 0)
        for word in ("check", "status", "start", "stop", "restart", "tunnel apply", "tunnel restart",
                     "rollback", "verify", "sudo", "auth.betterax.net", "ohmyassets.betterax.net", "--dry-run"):
            self.assertIn(word, r.stdout)


if __name__ == "__main__":
    unittest.main()
