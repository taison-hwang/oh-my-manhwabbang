"""Process and path facts for books.sh. Reads /proc, ss and the YAML config;
writes nothing.

  books_proc.py datadir CFG WORKDIR       absolute storage.data_dir (exit 1 if unset)
  books_proc.py holders DIR               "pid argv" of every process with a file under DIR open
  books_proc.py portpid PORT              pid of the process listening on 127.0.0.1/any :PORT
  books_proc.py match PID BIN CFG         exit 0 if PID runs BIN --config CFG
  books_proc.py routes CFG HOST           exit 0 if the cloudflared ingress in CFG sends HOST
                                          to a service, 1 if not, 2 if CFG has no readable ingress
"""
import os
import re
import subprocess
import sys

import yaml


def argv_of(pid):
    with open("/proc/%d/cmdline" % pid, "rb") as f:
        return [a.decode(errors="replace") for a in f.read().split(b"\0") if a]


def resolve(path, cwd):
    return os.path.realpath(os.path.join(cwd, path))


def datadir(cfg, workdir):
    doc = yaml.safe_load(open(cfg)) or {}
    d = (doc.get("storage") or {}).get("data_dir")
    if not d:
        sys.exit(1)
    print(resolve(str(d), workdir))


def pids():
    for name in os.listdir("/proc"):
        if name.isdigit():
            yield int(name)


def holders(directory):
    root = os.path.realpath(directory) + os.sep
    me = os.getpid()
    for pid in pids():
        if pid == me:
            continue
        try:
            fds = os.listdir("/proc/%d/fd" % pid)
        except OSError:
            continue
        for fd in fds:
            try:
                target = os.readlink("/proc/%d/fd/%s" % (pid, fd))
            except OSError:
                continue
            if target.startswith(root):
                try:
                    print(pid, " ".join(argv_of(pid)))
                except OSError:
                    print(pid)
                break


def portpid(port):
    out = subprocess.run(["ss", "-ltnpH", "sport = :%s" % port], capture_output=True, text=True).stdout
    m = re.search(r"pid=(\d+)", out)
    if not m:
        sys.exit(1)
    print(m.group(1))


def match(pid, binary, cfg):
    pid = int(pid)
    try:
        argv = argv_of(pid)
        cwd = os.readlink("/proc/%d/cwd" % pid)
    except OSError:
        sys.exit(1)
    want_bin, want_cfg = os.path.realpath(binary), os.path.realpath(cfg)
    # Index 1 covers a BIN that is a script: the kernel puts its interpreter first.
    for i in (0, 1):
        if len(argv) >= i + 3 and resolve(argv[i], cwd) == want_bin \
                and argv[i + 1] == "--config" and resolve(argv[i + 2], cwd) == want_cfg:
            sys.exit(0)
    sys.exit(1)


def host_matches(pattern, host):
    pattern = str(pattern).strip().lower().rstrip(".")
    if pattern in ("", "*"):
        return True
    if pattern.startswith("*."):
        return host.endswith(pattern[1:])
    return pattern == host


def routes(cfg, host):
    """cloudflared takes the first rule whose hostname (exact, case-insensitive,
    or a *. wildcard; none means every host) and path match. A path rule sends
    only some requests, so it counts as routing and the search goes on."""
    host = host.lower().rstrip(".")
    try:
        doc = yaml.safe_load(open(cfg))
    except (OSError, yaml.YAMLError):
        sys.exit(2)
    rules = doc.get("ingress") if isinstance(doc, dict) else None
    if not isinstance(rules, list) or not all(isinstance(r, dict) for r in rules):
        sys.exit(2)
    for rule in rules:
        if not host_matches(rule.get("hostname") or "", host):
            continue
        serves = not str(rule.get("service", "")).strip().startswith("http_status:")
        if serves:
            sys.exit(0)
        if not rule.get("path"):
            sys.exit(1)
    sys.exit(1)


if __name__ == "__main__":
    cmd, args = sys.argv[1], sys.argv[2:]
    {"datadir": datadir, "holders": holders, "portpid": portpid, "match": match, "routes": routes}[cmd](*args)
