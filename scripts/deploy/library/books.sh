#!/usr/bin/env bash
# books.sh — run SHELF at https://books.betterax.net (auth.mode oidc, E-74 §2.18).
#
# usage: books.sh [options] <command>
#
# commands (impact in brackets):
#   check               preflight of the config and this machine, then the binary's
#                       own config check: a throwaway copy (temporary data_dir,
#                       cache_dir and port, no scan) is started until healthy     [none to your data]
#   status              read-only: instance, port, health, ingress, DNS, public   [none]
#   init --from FILE    write a NEW --config from an existing shelf.yaml; refused
#                       if --config exists. Needs --yes. Review it before start   [new file]
#   start               preflight -> refuse if data_dir is in use -> back up
#                       data_dir -> start --bin -> health -> local verify; on
#                       failure the new instance is stopped                       [starts a process, no sudo]
#   stop                TERM the instance only if pidfile, port owner and command
#                       line (--bin --config) all match                           [stops it, no sudo]
#   restart             check of the NEW config first (preflight + the binary's
#                       config check; old process untouched if either fails),
#                       then stop + start                                         [no sudo]
#   tunnel apply        local verify must pass, then sudo tunnel-apply.sh adds the
#                       books rule to the cloudflared config. No restart          [sudo, writes /etc/cloudflared/config.yml]
#   tunnel restart [--backup F]  needs the same preflight as tunnel apply and an
#                       ingress backup without the books rule (recorded by tunnel
#                       apply, or F); then sudo systemctl restart cloudflared, then public verify;
#                       on failure the ingress backup is restored and cloudflared
#                       restarted again. auth.betterax.net and
#                       ohmyassets.betterax.net (same tunnel) drop for a few
#                       seconds on every restart                                  [sudo, outage of seconds]
#   rollback [--backup F]  sudo tunnel-rollback.sh with F (default: the backup
#                       recorded by tunnel apply), then restart cloudflared       [sudo, outage of seconds]
#   verify local|public|all   verify-public.sh against the port / the public URL  [none]
#
# options:
#   --config FILE     default <repo>/shelf.books.yaml (never written, except by init when absent).
#                     Every path option is made absolute against the directory you run this in
#   --bin FILE        default <repo>/dist/shelf
#   --workdir DIR     the server's working directory; a relative storage.data_dir
#                     resolves here. Default <repo>
#   --state-dir DIR   pidfile, server log, recorded ingress backup.
#                     Default ${XDG_STATE_HOME:-~/.local/state}/shelf-books
#   --cf-config FILE  default /etc/cloudflared/config.yml
#   --url URL         public URL for verify, default https://books.betterax.net
#   --wait SECONDS    health wait after start, default 30
#   --yes             confirm init/start/stop/restart without a prompt
#   --yes-sudo        confirm the sudo steps (tunnel apply/restart, rollback);
#                     --yes does not cover them
#   --dry-run         print what would change and change nothing
#
# exit: 0 ok, 1 failed, 2 usage, 3 config refused (nothing changed), 4 data_dir in
# use, 5 start failed (new instance stopped), 6 stop refused (no matching process),
# 7 local verify failed (tunnel refused), 8 public verify failed (ingress rolled
# back), 9 init target exists, 10 not confirmed.
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo="$(cd "$here/../../.." && pwd)"
# shellcheck source=common.sh
. "$here/common.sh"

config="$repo/shelf.books.yaml" bin="$repo/dist/shelf" workdir="$repo"
state="${XDG_STATE_HOME:-$HOME/.local/state}/shelf-books"
cf_config="$CF_CONFIG_DEFAULT" url="https://$LIB_HOST" wait_s=30
yes=0 yes_sudo=0 dry=0
args=()
while [ $# -gt 0 ]; do
  case "$1" in
    --config) config="$2"; shift 2 ;;
    --bin) bin="$2"; shift 2 ;;
    --workdir) workdir="$2"; shift 2 ;;
    --state-dir) state="$2"; shift 2 ;;
    --cf-config) cf_config="$2"; shift 2 ;;
    --url) url="$2"; shift 2 ;;
    --wait) wait_s="$2"; shift 2 ;;
    --yes) yes=1; shift ;;
    --yes-sudo) yes_sudo=1; shift ;;
    --dry-run) dry=1; shift ;;
    -h|--help) sed -n '2,/^set -euo/{/^set -euo/d;s/^# \{0,1\}//;p}' "$0"; exit 0 ;;
    *) args+=("$1"); shift ;;
  esac
done
[ "${#args[@]}" -gt 0 ] || { sed -n '4p' "$0" | sed 's/^# //'; exit 2; }
need python3; need ss; need curl

# The server runs in --workdir; fix every path now so the files checked are the files run.
abs() { realpath -m -s -- "$1"; }
config="$(abs "$config")" bin="$(abs "$bin")" workdir="$(abs "$workdir")" state="$(abs "$state")" cf_config="$(abs "$cf_config")"
pidfile="$state/shelf.pid" logfile="$state/shelf.log" cf_backup_rec="$state/tunnel.backup"
proc() { python3 -I "$here/books_proc.py" "$@"; }
port() { yaml_get "$config" server.port; }
cfg_port() { local p; p="$(port)"; [ -n "$p" ] || die "server.port is not set in $config"; echo "$p"; }

confirm() { # message
  [ "$yes" = 1 ] && return 0
  if [ -t 0 ]; then
    read -r -p "$1 Type yes: " ans
    [ "$ans" = yes ] && return 0
  fi
  say "not confirmed (pass --yes); nothing changed"; exit 10
}
confirm_sudo() { # message
  [ "$yes_sudo" = 1 ] && return 0
  if [ -t 0 ]; then
    read -r -p "$1 This step uses sudo. Type sudo: " ans
    [ "$ans" = sudo ] && return 0
  fi
  say "sudo step not confirmed (pass --yes-sudo; --yes does not cover it); nothing changed"; exit 10
}
plan() { say "would: $*"; }

# instance_pid: the process listening on the config's port, if it runs --bin --config.
instance_pid() {
  local p; p="$(proc portpid "$(cfg_port)" 2>/dev/null)" || return 1
  proc match "$p" "$bin" "$config" || return 1
  echo "$p"
}
running() { [ -r "/proc/$1/stat" ] && [ "$(awk '{print $3}' "/proc/$1/stat" 2>/dev/null)" != Z ]; }
pidfile_pid() { [ -r "$pidfile" ] && tr -dc '0-9' <"$pidfile"; }

gate() { # [own-pid] — exit 3 unless preflight has no FAIL
  local extra=()
  [ -n "${1:-}" ] && extra=(--own-pid "$1")
  [ -r "$config" ] || { say "no config at $config (books.sh init --from <shelf.yaml> writes one)"; exit 3; }
  if ! "$here/preflight.sh" --config "$config" --bin "$bin" --port "$(cfg_port)" "${extra[@]}"; then
    say "REFUSED: the config or this machine fails preflight (above). Nothing was changed."
    exit 3
  fi
}

local_verify() { "$here/verify-public.sh" --local --port "$(cfg_port)"; }

data_dir() { proc datadir "$config" "$workdir" || die "storage.data_dir is not set in $config; set it explicitly"; }

refuse_if_data_in_use() { # data_dir
  local h
  h="$(proc holders "$1" || true)"
  if [ -n "$h" ]; then
    say "REFUSED: these processes have files under $1 open (another shelf, e.g. :9909?):"
    printf '   %s\n' "$h"
    say "stop them first. Nothing was changed."
    exit 4
  fi
}

backup_data() { # data_dir -> prints backup path
  local d="$1" size avail bak
  if [ ! -d "$d" ] || [ -z "$(ls -A "$d")" ]; then
    say "data_dir $d is empty or absent: nothing to back up (first start)" >&2
    return 0
  fi
  size="$(du -sb "$d" | cut -f1)"
  avail="$(df -B1 --output=avail "$(dirname "$d")" | tail -1 | tr -d ' ')"
  [ "$avail" -gt $((size * 2)) ] || die "not enough free space to back up $d ($size B, $avail B free); nothing was changed"
  bak="$d.bak-$(utc_stamp)"
  while [ -e "$bak" ]; do bak="$bak+"; done
  cp -a "$d" "$bak"
  say "backup: $bak ($size B; DB files incl. -wal/-shm, copied while no process held them)" >&2
  echo "$bak"
}

term_and_wait() { # pid
  kill -TERM "$1" 2>/dev/null || return 0
  for _ in $(seq 1 30); do
    running "$1" || return 0
    sleep 0.5
  done
  return 1
}

recovery_help() { # backup
  say "recovery:"
  say "  server log:     tail -50 $logfile"
  [ -n "${1:-}" ] && say "  data backup:    $1  (restore only if the data looks wrong: stop every shelf, then"
  [ -n "${1:-}" ] && say "                  mv $(dirname "$1")/$(basename "${1%.bak-*}") <aside> && cp -a $1 ${1%.bak-*})"
  [ -r "$state/last-good.yaml" ] && say "  last config that started cleanly: $state/last-good.yaml (books.sh --config $state/last-good.yaml start)"
  say "  nothing was published; the tunnel was not touched"
}

# binary_check: the rules only shelf knows (E-74 §2.19, e.g. session_ttl <= 24h in
# oidc mode) surface when the binary loads the file; shelf has no check-only flag.
# A copy with its own data_dir, cache_dir and port is started and stopped, so the
# real data, the real port and a running instance are never touched.
binary_check() {
  local t tp tpid code=""
  t="$(mktemp -d "${TMPDIR:-/tmp}/books-check.XXXXXX")"
  tp="$(python3 -I - "$config" "$t" <<'PY'
import socket, sys
import yaml
src, t = sys.argv[1:3]
doc = yaml.safe_load(open(src)) or {}
s = socket.socket(); s.bind(("127.0.0.1", 0)); port = s.getsockname()[1]; s.close()
doc.setdefault("server", {}).update({"listen": "127.0.0.1", "port": port})
doc.setdefault("storage", {}).update({"data_dir": t + "/data", "cache_dir": t + "/cache"})
doc.setdefault("scan", {})["on_start"] = False
with open(t + "/shelf.yaml", "w") as f:
    yaml.safe_dump(doc, f)
print(port)
PY
)"
  (cd "$workdir" && exec "$bin" --config "$t/shelf.yaml" >"$t/log" 2>&1 </dev/null) &
  tpid=$!
  for _ in $(seq 1 $((wait_s * 2))); do
    running "$tpid" || break
    code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 2 "http://127.0.0.1:$tp/api/health" || true)"
    [ "$code" = 200 ] && break
    sleep 0.5
  done
  term_and_wait "$tpid" || kill -KILL "$tpid" 2>/dev/null || true
  wait "$tpid" 2>/dev/null || true
  if [ "$code" != 200 ]; then
    say "REFUSED: $bin does not accept $config (a throwaway copy did not start):"
    tail -5 "$t/log" | sed 's/^/   /'
    rm -rf -- "$t"
    say "Nothing was changed."
    exit 3
  fi
  rm -rf -- "$t"
  say "binary config check: $bin accepts $config"
}

do_start() {
  local d bak pid
  [ -z "$(instance_pid || true)" ] || { say "already running: pid $(instance_pid)"; exit 1; }
  gate
  d="$(data_dir)"
  refuse_if_data_in_use "$d"
  if [ "$dry" = 1 ]; then
    plan "back up $d to $d.bak-<UTC>"
    plan "(cd $workdir && nohup $bin --config $config >> $logfile &), pidfile $pidfile"
    plan "wait for http://127.0.0.1:$(cfg_port)/api/health, then verify-public.sh --local"
    return 0
  fi
  confirm "Start $bin on 127.0.0.1:$(cfg_port) with $config (data_dir $d is backed up first)?"
  mkdir -p "$state"
  bak="$(backup_data "$d")"
  (cd "$workdir" && exec nohup "$bin" --config "$config" >>"$logfile" 2>&1 </dev/null) &
  pid=$!
  echo "$pid" >"$pidfile"
  say "started pid $pid (log $logfile)"
  for _ in $(seq 1 $((wait_s * 2))); do
    running "$pid" || break
    [ "$(curl -s -o /dev/null -w '%{http_code}' --max-time 2 "http://127.0.0.1:$(cfg_port)/api/health")" = 200 ] && break
    sleep 0.5
  done
  if ! running "$pid" || [ "$(curl -s -o /dev/null -w '%{http_code}' --max-time 2 "http://127.0.0.1:$(cfg_port)/api/health")" != 200 ]; then
    say "FAIL: the new instance did not become healthy within ${wait_s}s"
    tail -5 "$logfile" | sed 's/^/   log: /'
    term_and_wait "$pid" || true
    rm -f "$pidfile"
    say "the new instance was stopped"
    recovery_help "$bak"
    exit 5
  fi
  if ! local_verify; then
    say "FAIL: local verify found an unauthenticated path (above); stopping the new instance"
    term_and_wait "$pid" || true
    rm -f "$pidfile"
    recovery_help "$bak"
    exit 5
  fi
  cp "$config" "$state/last-good.yaml"
  say "running: pid $pid on 127.0.0.1:$(cfg_port). Next: books.sh tunnel apply (sudo)"
}

matched_pid() { # -> pid, or exit 6 with the reason
  local pid owner
  pid="$(pidfile_pid || true)"
  [ -n "$pid" ] || { say "REFUSED: no pidfile at $pidfile" >&2; exit 6; }
  running "$pid" || { say "REFUSED: pidfile pid $pid is not running (stale $pidfile)" >&2; exit 6; }
  proc match "$pid" "$bin" "$config" || { say "REFUSED: pid $pid does not run $bin --config $config: $(tr '\0' ' ' <"/proc/$pid/cmdline")" >&2; exit 6; }
  owner="$(proc portpid "$(cfg_port)" 2>/dev/null || true)"
  [ "$owner" = "$pid" ] || { say "REFUSED: port $(cfg_port) is held by '${owner:-nobody}', not pid $pid" >&2; exit 6; }
  echo "$pid"
}

do_stop() {
  local pid; pid="$(matched_pid)"
  if [ "$dry" = 1 ]; then plan "kill -TERM $pid (pidfile, port $(cfg_port) and command line match)"; return 0; fi
  [ "${1:-}" = confirmed ] || confirm "Stop pid $pid (books.betterax.net goes offline: the tunnel answers 502)?"
  if term_and_wait "$pid"; then
    rm -f "$pidfile"; say "stopped pid $pid"
  else
    say "pid $pid did not exit within 15s after TERM; not killed. Inspect: tail $logfile"; exit 1
  fi
}

do_restart() {
  local pid; pid="$(matched_pid)"
  gate "$pid"
  binary_check
  if [ "$dry" = 1 ]; then plan "stop pid $pid, then start (backup, health, local verify)"; return 0; fi
  confirm "Restart pid $pid with $config?"
  do_stop confirmed
  yes=1 do_start
}

cf_has_rule() { grep -q "hostname: *$LIB_HOST" "$cf_config"; }
cf_args=()
[ "$cf_config" = "$CF_CONFIG_DEFAULT" ] || cf_args=(--config-path "$cf_config")

do_tunnel_apply() {
  local pid out
  pid="$(instance_pid || true)"
  gate "$pid"
  if cf_has_rule; then say "$cf_config already routes $LIB_HOST; nothing to do"; return 0; fi
  if ! local_verify; then say "REFUSED: local verify has FAILs (above); the ingress was not touched"; exit 7; fi
  if [ "$dry" = 1 ]; then plan "sudo $here/tunnel-apply.sh --service http://127.0.0.1:$(cfg_port) ${cf_args[*]}"; return 0; fi
  confirm_sudo "Add the $LIB_HOST rule to $cf_config (backup first, cloudflared NOT restarted)?"
  mkdir -p "$state"
  out="$(sudo "$here/tunnel-apply.sh" --service "http://127.0.0.1:$(cfg_port)" "${cf_args[@]}" 2>&1)" || { echo "$out"; say "tunnel-apply failed; it restored its backup"; exit 1; }
  echo "$out"
  sed -n 's/^>> backup: //p' <<<"$out" | head -1 >"$cf_backup_rec"
  say "ingress rule added (backup $(cat "$cf_backup_rec")). Next: books.sh tunnel restart (sudo; auth and ohmyassets drop for seconds)"
}

restart_cloudflared() {
  sudo systemctl restart cloudflared
  sleep 3
  systemctl is-active --quiet cloudflared
}

rollback_ingress() { # backup
  sudo "$here/tunnel-rollback.sh" --backup "$1" "${cf_args[@]}" && restart_cloudflared
}

do_tunnel_restart() {
  local bak
  gate "$(instance_pid || true)"
  cf_has_rule || { say "REFUSED: $cf_config has no $LIB_HOST rule; run tunnel apply first"; exit 1; }
  local_verify >/dev/null || { say "REFUSED: local verify has FAILs (books.sh verify local); cloudflared not restarted"; exit 7; }
  bak="$(cat "$cf_backup_rec" 2>/dev/null || true)"
  [ "${1:-}" = --backup ] && bak="$(abs "${2:-}")"
  if [ -z "$bak" ] || [ ! -f "$bak" ] || grep -q "hostname: *$LIB_HOST" "$bak"; then
    say "REFUSED: no ingress backup to fall back to (${bak:-none recorded}; it must exist and not route $LIB_HOST)."
    say "give the file tunnel-apply.sh made: books.sh tunnel restart --backup /etc/cloudflared/config.yml.bak-<UTC>. cloudflared not restarted"
    exit 1
  fi
  if [ "$dry" = 1 ]; then plan "sudo systemctl restart cloudflared, verify-public --public --url $url, on FAIL roll back to $bak"; return 0; fi
  confirm_sudo "Restart cloudflared? auth.betterax.net and ohmyassets.betterax.net drop for a few seconds."
  if restart_cloudflared && "$here/verify-public.sh" --public --url "$url"; then
    say "public: $url passes verify. First login: open it, copy the sub from the 403 page,"
    say "add it to auth.oidc.allowed_subs in $config yourself, then books.sh restart"
    return 0
  fi
  say "FAIL after the restart (cloudflared inactive or public verify FAILs)"
  if [ -n "$bak" ] && [ -f "$bak" ]; then
    say "rolling back the ingress to $bak"
    if rollback_ingress "$bak"; then say "rolled back; $LIB_HOST answers 404 again"; else say "ROLLBACK FAILED: run books.sh rollback --backup $bak"; fi
  else
    say "no recorded backup: run books.sh rollback --backup /etc/cloudflared/config.yml.bak-<UTC>"
  fi
  exit 8
}

do_rollback() {
  local bak; bak="$(cat "$cf_backup_rec" 2>/dev/null || true)"
  [ "${1:-}" = --backup ] && bak="${2:-}"
  [ -n "$bak" ] && [ -f "$bak" ] || die "no backup file (give --backup /etc/cloudflared/config.yml.bak-<UTC>)"
  if [ "$dry" = 1 ]; then plan "sudo tunnel-rollback.sh --backup $bak, then sudo systemctl restart cloudflared"; return 0; fi
  confirm_sudo "Restore $cf_config from $bak and restart cloudflared (auth and ohmyassets drop for seconds)?"
  rollback_ingress "$bak" && say "rolled back to $bak"
}

do_verify() {
  case "${1:-}" in
    local) local_verify ;;
    public) "$here/verify-public.sh" --public --url "$url" ;;
    all) local_verify && "$here/verify-public.sh" --public --url "$url" ;;
    *) die "verify local|public|all" ;;
  esac
}

do_init() {
  [ "${1:-}" = --from ] && [ -r "${2:-}" ] || die "init --from <existing shelf.yaml>"
  [ ! -e "$config" ] || { say "REFUSED: $config exists; it is never overwritten"; exit 9; }
  if [ "$dry" = 1 ]; then plan "write $config from $2 (server: 127.0.0.1:$DEFAULT_PORT, auth oidc block)"; return 0; fi
  confirm "Write a new $config from $2?"
  python3 -I - "$2" "$config" "$LIB_HOST" "$DEFAULT_PORT" <<'PY'
import sys
import yaml
src, dst, host, port = sys.argv[1:5]
doc = yaml.safe_load(open(src)) or {}
server = doc.setdefault("server", {})
server.update({"listen": "127.0.0.1", "port": int(port), "base_path": "", "trusted_proxy_headers": False})
doc["auth"] = {"mode": "oidc", "session_ttl": "12h", "oidc": {
    "issuer": "https://auth.betterax.net", "client_id": "zip-viewer",
    "audience": f"https://{host}/api", "redirect_url": f"https://{host}/api/auth/oidc/callback",
    "post_logout_redirect_url": "", "allowed_subs": [], "admin_subs": [], "legacy_owner_sub": ""}}
with open(dst, "x") as f:
    f.write(f"# written by books.sh init from {src}; comments of the source are not kept\n")
    yaml.safe_dump(doc, f, allow_unicode=True, sort_keys=False)
PY
  say "wrote $config (from $2). Review server.allow_root_editing and roots, then books.sh check"
}

do_status() {
  local p pid code
  p="$(port 2>/dev/null || true)"
  say "config: $config $( [ -r "$config" ] && sha256sum "$config" | cut -c1-12 || echo '(missing)')"
  pid="$(pidfile_pid || true)"
  if [ -n "$pid" ] && running "$pid"; then
    if proc match "$pid" "$bin" "$config"; then say "pidfile: $pid running $bin --config $config"
    else say "pidfile: $pid running something else"; fi
  else say "pidfile: ${pid:-none}${pid:+ (not running)}"; fi
  if [ -n "$p" ]; then
    say "port $p: $(ss -ltnpH "sport = :$p" | awk '{print $4, $6}' | head -2 | tr '\n' ' ')"
    code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 3 "http://127.0.0.1:$p/api/health" || true)"
    say "health http://127.0.0.1:$p/api/health: $code"
  fi
  say "shelf processes: $( (pgrep -a -x shelf || echo none) | tr '\n' ';')"
  say "listeners 9909/9910: $(ss -ltnH '( sport = :9909 or sport = :9910 )' | awk '{print $4}' | tr '\n' ' ')"
  if [ -r "$cf_config" ]; then
    if cf_has_rule; then say "ingress: $cf_config routes $LIB_HOST"; else say "ingress: no $LIB_HOST rule in $cf_config"; fi
  fi
  say "cloudflared process: $(pgrep -x cloudflared >/dev/null && echo running || echo 'not found')"
  say "DNS $LIB_HOST: $(getent hosts "$LIB_HOST" | awk '{print $1}' | head -2 | tr '\n' ' ')"
  say "public $url/: $(curl -s -o /dev/null -w '%{http_code}' --max-time 10 "$url/" || true)"
}

cmd="${args[0]}"
case "$cmd" in
  init|status) ;;
  *) [ -r "$config" ] || { say "no config at $config (books.sh init --from <shelf.yaml> writes one)"; exit 3; } ;;
esac
case "$cmd" in
  check) gate "$(instance_pid || true)"; binary_check; say "check: no FAIL" ;;
  status) do_status ;;
  init) do_init "${args[@]:1}" ;;
  start) do_start ;;
  stop) do_stop ;;
  restart) do_restart ;;
  tunnel)
    case "${args[1]:-}" in
      apply) do_tunnel_apply ;;
      restart) do_tunnel_restart "${args[@]:2}" ;;
      *) die "tunnel apply|restart" ;;
    esac ;;
  rollback) do_rollback "${args[@]:1}" ;;
  verify) do_verify "${args[1]:-}" ;;
  *) die "unknown command: $cmd (books.sh --help)" ;;
esac
