"""scripts/build.sh with fake go/pnpm (tests/fakes_build) writing to a temporary
output path: the repository's dist/shelf is never touched."""
import hashlib
import os
import shutil
import signal
import subprocess
import tempfile
import time
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.dirname(HERE))))
BUILD = os.path.join(REPO, "scripts", "build.sh")
FAKES = os.path.join(HERE, "fakes_build")
OLD = b"#!/bin/sh\nsleep 300\n"


def sha(path):
    with open(path, "rb") as f:
        return hashlib.sha256(f.read()).hexdigest()


class Build(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp(prefix="build-test-")
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.out = os.path.join(self.tmp, "dist", "shelf")
        os.makedirs(os.path.dirname(self.out))
        with open(self.out, "wb") as f:
            f.write(OLD)
        os.chmod(self.out, 0o755)
        self.calls = os.path.join(self.tmp, "calls.log")
        open(self.calls, "w").close()

    def calls_text(self):
        with open(self.calls) as f:
            return f.read()

    def env(self, **extra):
        e = dict(os.environ, GO=os.path.join(FAKES, "go"), PNPM=os.path.join(FAKES, "pnpm"), FAKE_CALLS=self.calls)
        e.update(extra)
        return e

    def run_build(self, *args, env=None):
        return subprocess.run([BUILD, "--out", self.out] + list(args), stdin=subprocess.DEVNULL,
                              capture_output=True, text=True, env=env or self.env(), timeout=300, cwd=self.tmp)

    def leftovers(self):
        return sorted(n for n in os.listdir(os.path.dirname(self.out)) if n != "shelf")

    def test_help_names_the_entry_points(self):
        r = subprocess.run([BUILD, "--help"], capture_output=True, text=True)
        self.assertEqual(r.returncode, 0)
        self.assertIn("usage: scripts/build.sh", r.stdout)
        self.assertIn("scripts/start.sh", r.stdout)

    def test_a_good_build_replaces_the_binary_in_one_rename_and_says_what_next(self):
        before = os.stat(self.out).st_ino
        r = self.run_build("--skip-web")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        with open(self.out, "rb") as f:
            self.assertIn(b"fake build", f.read())
        self.assertNotEqual(os.stat(self.out).st_ino, before, "renamed into place, not rewritten")
        self.assertIn("v0.0.0-test", r.stdout)
        self.assertIn("bytes", r.stdout)
        self.assertIn("scripts/start.sh", r.stdout)
        self.assertEqual(self.leftovers(), [])
        self.assertNotIn("pnpm", self.calls_text(), "--skip-web relinks only")

    def test_a_failed_compile_keeps_the_old_binary_byte_for_byte(self):
        before = sha(self.out)
        r = self.run_build("--skip-web", env=self.env(FAKE_GO_FAIL="1"))
        self.assertEqual(r.returncode, 1, r.stdout + r.stderr)
        self.assertEqual(sha(self.out), before)
        self.assertEqual(self.leftovers(), [])
        self.assertIn("kept", r.stdout)

    def test_a_failing_make_keeps_the_old_binary_even_if_the_output_works(self):
        before = sha(self.out)
        r = self.run_build("--skip-web", env=self.env(FAKE_GO_FAIL="2"))
        self.assertEqual(r.returncode, 1, r.stdout + r.stderr)
        self.assertEqual(sha(self.out), before)
        self.assertEqual(self.leftovers(), [])

    def test_a_failing_release_keeps_the_old_binary(self):
        before = sha(self.out)
        r = self.run_build("--skip-web", "--release", env=self.env(FAKE_GO_FAIL="3"))
        self.assertEqual(r.returncode, 1, r.stdout + r.stderr)
        self.assertEqual(sha(self.out), before)
        self.assertEqual(self.leftovers(), [])

    def test_a_binary_that_does_not_answer_version_is_not_installed(self):
        before = sha(self.out)
        r = self.run_build("--skip-web", env=self.env(FAKE_GO_BAD="1"))
        self.assertEqual(r.returncode, 1, r.stdout + r.stderr)
        self.assertEqual(sha(self.out), before)
        self.assertIn("--version", r.stdout)
        self.assertEqual(self.leftovers(), [])

    def test_missing_go_is_named_and_nothing_runs(self):
        before = sha(self.out)
        r = self.run_build("--skip-web", env=self.env(GO="/nonexistent/go"))
        self.assertEqual(r.returncode, 2, r.stdout + r.stderr)
        self.assertIn("go", r.stdout + r.stderr)
        self.assertEqual(sha(self.out), before)
        self.assertEqual(self.calls_text(), "")

    def test_a_running_server_is_told_to_restart_and_left_alone(self):
        p = subprocess.Popen([self.out], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                             start_new_session=True)
        self.addCleanup(p.wait, 5)
        self.addCleanup(os.killpg, p.pid, signal.SIGTERM)
        time.sleep(0.3)
        r = self.run_build("--skip-web")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertIn(str(p.pid), r.stdout)
        self.assertIn("scripts/start.sh --restart", r.stdout)
        self.assertIsNone(p.poll())

    @unittest.skipIf(os.path.isdir(os.path.join(REPO, "web", "node_modules")),
                     "web/node_modules is installed here; the missing case cannot be shown")
    def test_missing_web_modules_are_named_and_the_old_binary_kept(self):
        before = sha(self.out)
        r = self.run_build()
        self.assertEqual(r.returncode, 2, r.stdout + r.stderr)
        self.assertIn("pnpm install --frozen-lockfile", r.stdout + r.stderr)
        self.assertEqual(sha(self.out), before)


if __name__ == "__main__":
    unittest.main()
