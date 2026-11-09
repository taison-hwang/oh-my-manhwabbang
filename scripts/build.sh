#!/usr/bin/env bash
# build.sh — build dist/shelf, the binary scripts/start.sh runs.
#
# usage: scripts/build.sh [--skip-web] [--release] [--out FILE]
#
# Runs `make web build-go` (the SPA, then the static binary with its linkage
# check) into a temporary file next to dist/shelf and renames it into place only
# when the build and `--version` succeed, so a failed or interrupted build keeps
# the old binary and a running server is never left with half a file. go needs
# no GOPROXY setup: the Makefile sets GOPROXY, GOSUMDB and GOTOOLCHAIN itself.
# Never stops or restarts a server: a running one keeps the old binary until you
# run scripts/start.sh --restart.
#
#   --skip-web    relink only, against the web/dist built last time
#   --release     afterwards also `make release` (seven targets and SHA256SUMS in dist/)
#   --out FILE    default <repo>/dist/shelf
#
# needs: make, go, and (unless --skip-web) pnpm, node and web/node_modules.
# GO and PNPM name other binaries, as for make.
#
# exit: 0 built, 1 build failed (the old binary is kept), 2 usage or a missing prerequisite.
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo="$(cd "$here/.." && pwd)"
out="$repo/dist/shelf" web=1 release=0
while [ $# -gt 0 ]; do
  case "$1" in
    -h|--help) sed -n '2,/^set -euo/{/^set -euo/d;s/^# \{0,1\}//;p}' "$0"; exit 0 ;;
    --skip-web) web=0; shift ;;
    --release) release=1; shift ;;
    --out) [ $# -ge 2 ] || { echo "error: --out needs a file" >&2; exit 2; }; out="$2"; shift 2 ;;
    *) echo "error: unknown argument $1 (scripts/build.sh --help)" >&2; exit 2 ;;
  esac
done
GO="${GO:-go}" PNPM="${PNPM:-pnpm}"
missing=()
command -v make >/dev/null || missing+=("make")
command -v "$GO" >/dev/null || missing+=("go ($GO not found; install Go, see go.mod for the version)")
if [ "$web" = 1 ]; then
  command -v "$PNPM" >/dev/null || missing+=("pnpm ($PNPM not found)")
  command -v node >/dev/null || missing+=("node")
  [ -d "$repo/web/node_modules" ] || missing+=("web/node_modules: run  cd web && pnpm install --frozen-lockfile")
fi
if [ "${#missing[@]}" -gt 0 ]; then
  echo ">> cannot build; missing:"
  printf '   - %s\n' "${missing[@]}"
  echo ">> nothing was built; $out is unchanged"
  exit 2
fi
out="$(realpath -m -- "$out")"
mkdir -p "$(dirname "$out")"
tmp="$(mktemp "$(dirname "$out")/.shelf.build-XXXXXX")"
trap 'rm -f -- "$tmp"' EXIT
targets=(build-go)
[ "$web" = 1 ] && targets=(web build-go)
echo ">> building $out (make ${targets[*]}) ..."
if ! make -C "$repo" "${targets[@]}" SHELF_OUT="$tmp" GO="$GO" PNPM="$PNPM"; then
  echo ">> build failed (above); $out was kept as it was"
  exit 1
fi
chmod 755 "$tmp"
if ! version="$("$tmp" --version 2>&1 | head -1)" || [ -z "$version" ]; then
  echo ">> the new binary does not answer --version; $out was kept as it was"
  exit 1
fi
users="$(python3 -I - "$out" <<'PY'
import os, sys
want = os.path.realpath(sys.argv[1])
for name in os.listdir("/proc"):
    if not name.isdigit():
        continue
    try:
        with open("/proc/%s/cmdline" % name, "rb") as f:
            argv = [a.decode(errors="replace") for a in f.read().split(b"\0") if a]
        cwd = os.readlink("/proc/%s/cwd" % name)
    except OSError:
        continue
    if any(os.path.realpath(os.path.join(cwd, a)) == want for a in argv[:2]):
        print(name)
PY
)"
if [ "$release" = 1 ]; then
  echo ">> make release ..."
  make -C "$repo" release GO="$GO" PNPM="$PNPM" || { echo ">> make release failed (above); $out was kept as it was"; exit 1; }
fi
mv -f -- "$tmp" "$out"
trap - EXIT
echo ">> built $out: $version, $(wc -c <"$out") bytes"
if [ -n "$users" ]; then
  echo ">> still running the previous binary: pid $(echo "$users" | tr '\n' ' ')"
  echo ">> restart it yourself when it suits you: scripts/start.sh --restart"
else
  echo ">> next: scripts/start.sh"
fi
