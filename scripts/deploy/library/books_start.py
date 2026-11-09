"""The body of scripts/start.sh (its header is the usage). Brings zip-viewer up
at https://books.betterax.net and asks only what the current state leaves open.

Every start, restart and Cloudflare change goes through books.sh, so its gates
(preflight, the binary's own config check, local verify before ingress, public
verify with rollback) are the only way anything is started or published. This
file adds what books.sh does not do: deciding which steps are left, stopping an
old server the user names by pid, and writing the sign-in allow list into the
config with a backup, a diff and a rollback."""
import argparse
import copy
import difflib
import importlib.util
import os
import re
import shlex
import shutil
import signal
import subprocess
import sys
import time
from datetime import datetime, timezone

import yaml

LIB = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.dirname(os.path.dirname(os.path.dirname(LIB)))
BOOKS = os.path.join(LIB, "books.sh")
HOST = "books.betterax.net"
# uni-auth subs are opaque ids; this keeps anything that could break the YAML
# scalar or hide a second value (spaces, quotes, newlines) out of the config.
SUB_RE = re.compile(r"[A-Za-z0-9._:@|+/=-]{1,256}")

_spec = importlib.util.spec_from_file_location("books_proc", os.path.join(LIB, "books_proc.py"))
proc = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(proc)


class Stop(Exception):
    def __init__(self, code, msg=""):
        super().__init__(msg)
        self.code, self.msg = code, msg


def say(msg):
    print(">> " + msg, flush=True)


def ask(question):
    sys.stdout.write("?? " + question)
    sys.stdout.flush()
    line = sys.stdin.readline()
    if not line:
        raise Stop(10, "no answer (input closed); nothing more was changed. Run scripts/start.sh again to continue.")
    return line.strip()


def utc_stamp():
    return datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ")


def get(doc, dotted, default=None):
    cur = doc
    for k in dotted.split("."):
        if not isinstance(cur, dict) or k not in cur:
            return default
        cur = cur[k]
    return cur


def changes(old, new, prefix=""):
    keys = list(old) + [k for k in new if k not in old] if isinstance(old, dict) and isinstance(new, dict) else []
    if not keys:
        return [] if old == new else [(prefix, old, new)]
    out = []
    for k in keys:
        out += changes(old.get(k), new.get(k), prefix + ("." if prefix else "") + str(k))
    return out


class Wizard:
    def __init__(self, a):
        self.a = a
        self.interactive = sys.stdin.isatty()
        self.checked = False

    # --- books.sh -------------------------------------------------------
    def books(self, *args):
        a = self.a
        cmd = [BOOKS, "--config", a.config, "--bin", a.bin, "--workdir", a.workdir]
        for flag, value in (("--state-dir", a.state_dir), ("--cf-config", a.cf_config), ("--url", a.url),
                            ("--wait", a.wait), ("--port", a.port)):
            if value:
                cmd += [flag, value]
        r = subprocess.run(cmd + list(args), stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                           stderr=subprocess.STDOUT, text=True)
        return r.returncode, r.stdout

    def must(self, what, *args):
        code, out = self.books(*args)
        if code != 0:
            print(out, end="")
            raise Stop(code, "%s failed (above)." % what)
        return out

    # --- facts ----------------------------------------------------------
    def cfg(self):
        with open(self.a.config) as f:
            return yaml.safe_load(f) or {}

    def port(self):
        return get(self.cfg(), "server.port")

    def managed_pid(self):
        owner = proc.port_owner(self.port()) if self.port() else None
        return owner if owner and proc.matches(owner, self.a.bin, self.a.config) else None

    def cf_config(self):
        return self.a.cf_config or "/etc/cloudflared/config.yml"

    def routes_books(self):
        r = subprocess.run([sys.executable, "-I", os.path.join(LIB, "books_proc.py"), "routes", self.cf_config(), HOST],
                           stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        return r.returncode == 0

    def public_passes(self):
        r = subprocess.run([os.path.join(LIB, "verify-public.sh"), "--public", "--url", self.url()],
                           stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        return r.returncode == 0

    def url(self):
        return self.a.url or "https://" + HOST

    def subs(self):
        return get(self.cfg(), "auth.oidc.allowed_subs") or []

    def data_dir(self):
        d = get(self.cfg(), "storage.data_dir")
        return os.path.realpath(os.path.join(self.a.workdir, str(d))) if d else None

    def state_dir(self):
        return self.a.state_dir or os.path.join(os.environ.get("XDG_STATE_HOME") or
                                                os.path.expanduser("~/.local/state"), "shelf-books")

    # --- run ------------------------------------------------------------
    def run(self):
        if not os.path.exists(self.a.bin):
            raise Stop(2, "no binary at %s: build it with scripts/build.sh" % self.a.bin)
        missing = []
        if not os.path.exists(self.a.config):
            missing.append("a config at %s (written from %s)" % (self.a.config, self.a.source))
        elif not self.subs():
            missing.append("an allowed uni-auth account (auth.oidc.allowed_subs, from your first sign-in)")
        if not self.routes_books():
            missing.append("the Cloudflare route for %s (sudo; cloudflared restarts)" % HOST)
        if not missing:
            return self.later_run()
        if not self.interactive:
            say("first run: still needed:")
            for m in missing:
                print("   - " + m)
            raise Stop(10, "these need answers, so run scripts/start.sh in a terminal. Nothing was changed.")
        return self.first_run()

    def later_run(self):
        pid = self.managed_pid()
        if pid and self.a.restart:
            say("restarting pid %d (the new config is checked first; the old process stays if it fails)" % pid)
            self.must("restart", "--yes", "restart")
        elif not pid:
            self.ensure_running()
        self.summary()
        return 0

    def first_run(self):
        created = False
        if not os.path.exists(self.a.config):
            if not os.path.exists(self.a.source):
                raise Stop(3, "no config at %s and no %s to write it from (pass --from FILE)" %
                           (self.a.config, self.a.source))
            self.must("writing the config", "--yes", "init", "--from", self.a.source)
            say("wrote %s from %s (%s is not changed)" % (self.a.config, self.a.source, self.a.source))
            created = True
        if not self.managed_pid():
            self.check()
        if created:
            self.decide_root_editing()
        if not self.managed_pid():
            self.ensure_running()
        if not self.routes_books():
            self.connect_cloudflare(apply=True)
        elif not self.public_passes():
            self.connect_cloudflare(apply=False)
        if not self.subs():
            self.add_sub()
        self.summary()
        return 0

    # --- steps ----------------------------------------------------------
    def check(self):
        if self.checked:
            return
        say("checking the config and this machine (preflight, the binary's config check) ...")
        self.must("the check", "check")
        self.checked = True
        say("check: no FAIL")

    def ensure_running(self):
        self.handle_old_servers()
        say("starting zip-viewer (data_dir is backed up first, then health and local verify) ...")
        self.must("start", "--yes", "start")
        say("started: pid %s" % self.managed_pid())

    def old_servers(self):
        found = []
        d = self.data_dir()
        if d and os.path.isdir(d):
            found += proc.holder_pids(d)
        owner = proc.port_owner(self.port()) if self.port() else None
        if owner and owner not in found:
            found.append(owner)
        me = os.getpid()
        return [p for p in found if p != me and not proc.matches(p, self.a.bin, self.a.config)]

    def describe(self, pid):
        argv = proc.argv_of(pid)
        cwd = os.readlink("/proc/%d/cwd" % pid)
        cfg = argv[argv.index("--config") + 1] if "--config" in argv[:-1] else None
        ports = sorted({p for p, _ in proc.listening_ports(pid=pid)})
        with open("/proc/%d/stat" % pid) as f:
            started = f.read().rsplit(") ", 1)[1].split()[19]
        return argv, cwd, cfg and os.path.join(cwd, cfg), ports, started

    def handle_old_servers(self):
        olds = self.old_servers()
        if not olds:
            return
        self.check()
        say("these processes hold zip-viewer's data_dir or port, so it cannot start next to them:")
        info = {}
        for pid in olds:
            try:
                info[pid] = argv, cwd, cfg, ports, _ = self.describe(pid)
            except (OSError, ValueError, IndexError):
                continue
            print("   pid %d: %s" % (pid, shlex.join(argv)))
            print("      cwd %s, config %s, listening %s" % (cwd, cfg or "-", ", ".join(map(str, ports)) or "-"))
        if not self.interactive:
            raise Stop(4, "stop them yourself, or run scripts/start.sh in a terminal to stop them there. "
                          "Nothing was changed.")
        for pid, (argv, cwd, _, _, started) in info.items():
            ans = ask("Type its pid to stop it (TERM, never KILL), or press Enter to leave it running [%d]: " % pid)
            if ans != str(pid):
                raise Stop(10, "pid %d left running, so zip-viewer was not started. Stop it, then run "
                               "scripts/start.sh again." % pid)
            try:
                same = self.describe(pid)[4] == started
            except (OSError, ValueError, IndexError):
                same = False
            if not same:
                raise Stop(1, "pid %d is no longer the process shown above; nothing was stopped" % pid)
            os.kill(pid, signal.SIGTERM)
            for _ in range(30):
                if not proc_alive(pid):
                    break
                time.sleep(0.5)
            else:
                raise Stop(1, "pid %d did not exit within 15s after TERM; not killed" % pid)
            say("stopped pid %d. To bring it back later:" % pid)
            print("   cd %s && nohup %s >/dev/null 2>&1 &" % (shlex.quote(cwd), shlex.join(argv)))

    def decide_root_editing(self):
        cur = bool(get(self.cfg(), "server.allow_root_editing", False))
        print("   server.allow_root_editing is %s (copied from %s)." % ("on" if cur else "off", self.a.source))
        print("   on:  anyone you allow to sign in can add or remove library folders from the web page,")
        print("        so a public account can open any folder this user account can read.")
        print("   off: library folders change only in the config file.")
        ans = ask("Library folder editing from the web (allow_root_editing): Type on or off: ")
        if ans not in ("on", "off"):
            raise Stop(10, "no choice made; %s keeps %s. Run scripts/start.sh again to continue."
                       % (self.a.config, "on" if cur else "off"))
        if (ans == "on") != cur:
            self.edit_config(lambda d: d.setdefault("server", {}).__setitem__("allow_root_editing", ans == "on"),
                             confirm=False)

    def connect_cloudflare(self, apply):
        print("   Cloudflare: publish %s through the tunnel. Uses sudo (the standard sudo prompt;" % self.url())
        print("   this script never reads your password):")
        if apply:
            print("   - add the %s rule to %s (backed up first)" % (HOST, self.cf_config()))
        print("   - restart cloudflared: auth.betterax.net and ohmyassets.betterax.net (same tunnel)")
        print("     drop for a few seconds")
        print("   If the public check then finds an unauthenticated path, the rule is rolled back.")
        if ask("Connect Cloudflare now? Type sudo: ") != "sudo":
            raise Stop(10, "Cloudflare not connected; zip-viewer runs on this machine only. "
                           "Run scripts/start.sh again to continue.")
        if subprocess.run(["sudo", "-v"]).returncode != 0:
            raise Stop(1, "sudo did not accept; nothing was changed in Cloudflare")
        if apply:
            say("adding the ingress rule ...")
            self.must("tunnel apply", "--yes-sudo", "tunnel", "apply")
        say("restarting cloudflared and checking %s from outside ..." % self.url())
        self.must("tunnel restart", "--yes-sudo", "tunnel", "restart")
        say("published: %s passes the public check" % self.url())

    def add_sub(self):
        print("   First sign-in: open %s/ in a browser and sign in with uni-auth." % self.url())
        print("   zip-viewer answers 403 (not allowed yet) and shows your sub on that page. Copy it.")
        for _ in range(3):
            sub = ask("Paste the sub from that page: ")
            if SUB_RE.fullmatch(sub):
                break
            say("not a valid sub (letters, digits and . _ : @ | + / = - only; no spaces or quotes)")
        else:
            raise Stop(10, "no valid sub; nothing was written. Run scripts/start.sh again to continue.")
        admin = ask("Make this account an administrator? Type yes or no: ") == "yes"
        owner = False
        if not get(self.cfg(), "auth.oidc.legacy_owner_sub"):
            owner = ask("Make it the owner of the reading progress saved before sign-in existed "
                        "(moved to it once, at the next start)? Type yes or no: ") == "yes"

        def apply(doc):
            o = doc.setdefault("auth", {}).setdefault("oidc", {})
            o["allowed_subs"] = list(o.get("allowed_subs") or []) + [sub]
            if admin:
                o["admin_subs"] = list(o.get("admin_subs") or []) + [sub]
            if owner:
                o["legacy_owner_sub"] = sub
        backup = self.edit_config(apply, confirm=True)
        self.restart_with(backup)

    # --- config changes -------------------------------------------------
    def edit_config(self, mutate, confirm):
        path = self.a.config
        with open(path) as f:
            old_text = f.read()
        old = yaml.safe_load(old_text) or {}
        new = copy.deepcopy(old)
        mutate(new)
        header = "".join(line for line in old_text.splitlines(True)[:5] if line.startswith("#"))
        new_text = header + yaml.safe_dump(new, allow_unicode=True, sort_keys=False)
        print("   change to %s:" % path)
        for key, before, after in changes(old, new):
            print("     %s: %s -> %s" % (key, before, after))
        for line in difflib.unified_diff(old_text.splitlines(), new_text.splitlines(), "before", "after", n=1,
                                         lineterm=""):
            print("     " + line)
        if confirm and ask("Apply this change? Type yes: ") != "yes":
            raise Stop(10, "nothing was written. Run scripts/start.sh again to continue.")
        backup = path + ".bak-" + utc_stamp()
        while os.path.exists(backup):
            backup += "+"
        shutil.copy2(path, backup)
        tmp = path + ".tmp-%d" % os.getpid()
        with open(tmp, "w") as f:
            f.write(new_text)
        shutil.copymode(path, tmp)
        os.replace(tmp, path)
        if self.cfg() != new:
            self.restore(backup)
            raise Stop(1, "the written config did not read back as intended; restored")
        say("wrote %s (backup %s)" % (path, backup))
        code, out = self.books("check")
        if code != 0:
            print(out, end="")
            self.restore(backup)
            raise Stop(code, "the changed config fails the check (above); the running instance was not touched")
        return backup

    def restore(self, backup):
        shutil.copy2(backup, self.a.config)
        say("restored %s from %s" % (self.a.config, backup))

    def restart_with(self, backup):
        running = self.managed_pid()
        say("%s zip-viewer with the new config ..." % ("restarting" if running else "starting"))
        code, out = self.books("--yes", "restart" if running else "start")
        if code == 0:
            return
        print(out, end="")
        self.restore(backup)
        if not self.managed_pid():
            c2, out2 = self.books("--yes", "start")
            if c2 == 0:
                say("zip-viewer runs again with the restored config (pid %s)" % self.managed_pid())
            else:
                print(out2, end="")
                say("it did not start with the restored config either; see the recovery lines above")
        raise Stop(code, "the new config did not run; see above")

    def summary(self):
        pid = self.managed_pid()
        port = self.port()
        if not pid:
            say("zip-viewer: not running (scripts/start.sh starts it)")
            return
        public = subprocess.run(["curl", "-s", "-o", "/dev/null", "-w", "%{http_code}", "--max-time", "5",
                                 self.url() + "/"], capture_output=True, text=True).stdout or "no answer"
        note = ""
        last_good = os.path.join(self.state_dir(), "last-good.yaml")
        if os.path.exists(last_good) and open(last_good, "rb").read() != open(self.a.config, "rb").read():
            note = " · the config changed since it started: scripts/start.sh --restart"
        say("zip-viewer: running pid %d on 127.0.0.1:%s · %s: %s · %d allowed account(s)%s"
            % (pid, port, self.url(), public, len(self.subs()), note))


def proc_alive(pid):
    try:
        with open("/proc/%d/stat" % pid) as f:
            return f.read().rsplit(") ", 1)[1].split()[0] != "Z"
    except OSError:
        return False


def main():
    p = argparse.ArgumentParser(add_help=False)
    p.add_argument("--restart", action="store_true")
    p.add_argument("--config", default=os.path.join(REPO, "shelf.books.yaml"))
    p.add_argument("--from", dest="source", default=os.path.join(REPO, "shelf.yaml"))
    p.add_argument("--bin", default=os.path.join(REPO, "dist", "shelf"))
    p.add_argument("--workdir", default=REPO)
    for opt in ("--port", "--state-dir", "--cf-config", "--url", "--wait"):
        p.add_argument(opt)
    a, rest = p.parse_known_args()
    if rest:
        print("error: unknown argument %s (scripts/start.sh --help)" % " ".join(rest), file=sys.stderr)
        return 2
    for name in ("config", "source", "bin", "workdir", "state_dir", "cf_config"):
        if getattr(a, name):
            setattr(a, name, os.path.abspath(getattr(a, name)))
    try:
        return Wizard(a).run()
    except Stop as s:
        if s.msg:
            say(s.msg)
        return s.code
    except KeyboardInterrupt:
        print()
        say("interrupted; finished steps stay done. Run scripts/start.sh again to continue.")
        return 10


if __name__ == "__main__":
    sys.exit(main())
