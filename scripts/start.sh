#!/usr/bin/env bash
# start.sh — run zip-viewer (SHELF) at https://books.betterax.net.
#
# usage: scripts/start.sh [--restart]
#
# First run (no config, no Cloudflare route or no allowed account yet), in a terminal:
#   1. writes shelf.books.yaml from shelf.yaml (a new file; shelf.yaml is not changed)
#      and asks whether library folders may be edited from the web page
#   2. checks the config and this machine (oidc, loopback, the books registration,
#      the binary's own config check); any FAIL stops here with nothing changed
#   3. if another process (e.g. the old :9909 shelf) holds the data_dir or the
#      port, shows it and stops it only when you type its pid (TERM, never KILL),
#      then prints the command that brings it back
#   4. backs up data_dir, starts zip-viewer, checks it locally
#   5. asks once for sudo (standard sudo prompt) to add the Cloudflare rule and
#      restart cloudflared: auth.betterax.net and ohmyassets.betterax.net drop for
#      a few seconds; if the public check fails, the rule is rolled back
#   6. asks for the sub shown on the 403 page after your first sign-in, shows the
#      config change, backs the config up, checks it, restarts; on failure the
#      config is restored and zip-viewer runs again with it
# Steps already done are detected and skipped; after a stop or a "no", run it again.
#
# Later runs: no questions. Already running: one status line. Not running: check,
# data_dir backup, start, local verify, one status line.
#   --restart   restart the running instance (the new config is checked first;
#               the old process keeps running if the check fails)
#
# Related: scripts/stop.sh stops it, scripts/build.sh builds dist/shelf.
# Manual steps: scripts/deploy/library/books.sh --help.
#
# Advanced (defaults in brackets): --config FILE [shelf.books.yaml] --from FILE
# [shelf.yaml] --bin FILE [dist/shelf] --workdir DIR [repo] --state-dir DIR
# [~/.local/state/shelf-books] --cf-config FILE [/etc/cloudflared/config.yml]
# --url URL [https://books.betterax.net] --port PORT [9910, first run only] --wait SECONDS [30]
#
# exit: 0 running, 1 failed, 2 usage or no binary, 3 config refused (nothing
# changed), 4 another process holds data_dir or port, 5 start failed (stopped,
# recovery printed), 7 local verify failed, 8 public verify failed (rule rolled
# back), 10 needs a terminal or an answer was no (finished steps stay done).
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
for a in "$@"; do
  case "$a" in -h|--help) sed -n '2,/^set -euo/{/^set -euo/d;s/^# \{0,1\}//;p}' "$0"; exit 0 ;; esac
done
exec python3 -I "$here/deploy/library/books_start.py" "$@"
