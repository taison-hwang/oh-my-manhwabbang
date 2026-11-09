#!/usr/bin/env bash
# stop.sh — stop the zip-viewer instance scripts/start.sh started, and nothing else.
#
# usage: scripts/stop.sh
#
# Stops it only when its pidfile, the owner of its port and its command line
# (--bin --config) all match (books.sh stop); TERM only, never KILL. uni-auth,
# cloudflared, an old :9909 shelf or anything else is never stopped. With no such
# instance it stops nothing and says so.
# The Cloudflare rule stays: https://books.betterax.net answers an error (502)
# until scripts/start.sh runs again. To take the public address down instead:
# scripts/deploy/library/books.sh rollback (sudo).
#
# Advanced: --config FILE --bin FILE --workdir DIR --state-dir DIR, as for start.sh.
#
# exit: 0 stopped or nothing to stop, 1 it did not exit within 15s (not killed), 2 usage.
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
books="$here/deploy/library/books.sh"
opts=()
while [ $# -gt 0 ]; do
  case "$1" in
    -h|--help) sed -n '2,/^set -euo/{/^set -euo/d;s/^# \{0,1\}//;p}' "$0"; exit 0 ;;
    --config|--bin|--workdir|--state-dir) [ $# -ge 2 ] || { echo "error: $1 needs a value" >&2; exit 2; }; opts+=("$1" "$2"); shift 2 ;;
    *) echo "error: unknown argument $1 (scripts/stop.sh --help)" >&2; exit 2 ;;
  esac
done
set +e
out="$("$books" "${opts[@]}" --yes stop </dev/null 2>&1)"
rc=$?
set -e
case "$rc" in
  0)
    printf '%s\n' "$out"
    echo ">> zip-viewer stopped. https://books.betterax.net answers an error (502) until scripts/start.sh;"
    echo ">> the Cloudflare rule stays. To take the public address down: scripts/deploy/library/books.sh rollback (sudo)" ;;
  3|6)
    echo ">> no zip-viewer instance started by scripts/start.sh is running; nothing was stopped."
    printf '%s\n' "$out" | sed -n 's/^>> /   /p'
    rc=0 ;;
  *) printf '%s\n' "$out"; rc=1 ;;
esac
exit "$rc"
