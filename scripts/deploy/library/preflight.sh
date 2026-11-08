#!/usr/bin/env bash
# preflight.sh — unprivileged checks before the library.betterax.net rollout.
#
#   preflight.sh --config shelf.library.yaml [--bin dist/shelf] [--port 9910]
#
# Reads only. Starts nothing, writes nothing, needs no sudo. Exit 1 on any FAIL.
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=common.sh
. "$here/common.sh"

config="" bin="" port=""
while [ $# -gt 0 ]; do
  case "$1" in
    --config) config="$2"; shift 2 ;;
    --bin) bin="$2"; shift 2 ;;
    --port) port="$2"; shift 2 ;;
    -h|--help) sed -n '2,7p' "$0"; exit 0 ;;
    *) die "unknown argument: $1" ;;
  esac
done
[ -n "$config" ] || die "--config is required"
[ -r "$config" ] || die "cannot read $config"
need python3; need ss

# 1. binary
if [ -n "$bin" ]; then
  if [ -x "$bin" ] && ver="$("$bin" --version 2>&1)"; then
    rec PASS P1 "binary runs: $bin" "$(printf '%s' "$ver" | head -1)"
  else
    rec FAIL P1 "binary runs: $bin" "not executable or --version failed"
  fi
else
  rec SKIP P1 "binary version" "no --bin given"
fi

# 2. configuration
mode="$(yaml_get "$config" auth.mode)"
[ "$mode" = oidc ] && rec PASS P2 "auth.mode is oidc" || rec FAIL P2 "auth.mode is oidc" "got '${mode:-<unset>}'"

listen="$(yaml_get "$config" server.listen)"
case "$listen" in
  127.*|::1|localhost) rec PASS P3 "server.listen is loopback" "$listen" ;;
  *) rec FAIL P3 "server.listen is loopback" "got '${listen:-<unset>}' (E-74 §2.18 public-ban condition)" ;;
esac

cfgport="$(yaml_get "$config" server.port)"
port="${port:-${cfgport:-$DEFAULT_PORT}}"
[ "$port" = "$cfgport" ] && rec PASS P4 "port matches the config" "$port" \
  || rec WARN P4 "port matches the config" "--port $port, config says '${cfgport:-<unset>}'"

tph="$(yaml_get "$config" server.trusted_proxy_headers)"
case "$tph" in
  ""|False|false) rec PASS P5 "trusted_proxy_headers is off" "${tph:-<unset>=default false}" ;;
  *) rec FAIL P5 "trusted_proxy_headers is off" "got '$tph' (E-74 §2.18 public-ban condition)" ;;
esac

subs="$(yaml_get "$config" auth.oidc.allowed_subs)"
case "$subs" in
  ""|"[]") rec WARN P6 "allowed_subs is not empty" "empty: nobody can sign in; fill it after the first 403 (runbook step 9)" ;;
  *) rec PASS P6 "allowed_subs is not empty" "$subs" ;;
esac
admins="$(yaml_get "$config" auth.oidc.admin_subs)"
python3 -I - "$subs" "$admins" <<'PY' && rec PASS P7 "admin_subs is a subset of allowed_subs" || rec FAIL P7 "admin_subs is a subset of allowed_subs" "startup would exit 2"
import json, sys
a = json.loads(sys.argv[1] or "[]"); b = json.loads(sys.argv[2] or "[]")
sys.exit(0 if set(b) <= set(a) else 1)
PY

# 3. registered values (uni-auth registration, E-74 §2.19)
expect() { # id key want
  local got; got="$(yaml_get "$config" "$2")"
  [ "$got" = "$3" ] && rec PASS "$1" "$2 matches the registration" "$got" \
    || rec FAIL "$1" "$2 matches the registration" "got '${got:-<unset>}', want '$3'"
}
expect P8 auth.oidc.issuer "https://auth.betterax.net"
expect P9 auth.oidc.client_id "zip-viewer"
expect P10 auth.oidc.audience "https://$LIB_HOST/api"
bp="$(yaml_get "$config" server.base_path)"
expect P11 auth.oidc.redirect_url "https://$LIB_HOST${bp}/api/auth/oidc/callback"
if [ -n "$(yaml_get "$config" auth.password)$(yaml_get "$config" auth.password_hash)" ]; then
  rec FAIL P12 "no password with mode oidc" "password/password_hash set: startup would exit 2"
else
  rec PASS P12 "no password with mode oidc" ""
fi

# 4. port and listeners
if ss -ltnH "sport = :$port" | grep -q .; then
  rec FAIL P13 "port $port is free" "$(ss -ltnH "sport = :$port" | head -1 | awk '{print $4}') is listening"
else
  rec PASS P13 "port $port is free" ""
fi
wild="$(ss -ltnpH 2>/dev/null | grep -E '"shelf"' | awk '{print $4}' | grep -E '^(0\.0\.0\.0|\*|\[::\]|:::)' || true)"
[ -z "$wild" ] && rec PASS P14 "no running shelf listens on a wildcard address" "" \
  || rec FAIL P14 "no running shelf listens on a wildcard address" "$(echo "$wild" | tr '\n' ' ')— stop it before the tunnel (E-74 §2.18)"
other="$(pgrep -a -x shelf 2>/dev/null || true)"
[ -z "$other" ] && rec PASS P15 "no other shelf process is running" "" \
  || rec WARN P15 "another shelf process is running" "$(echo "$other" | head -2 | tr '\n' ';')"

# 5. outside reachability of the registration host (read-only)
if command -v getent >/dev/null; then
  getent hosts "$LIB_HOST" >/dev/null && rec PASS P16 "$LIB_HOST resolves" "DNS already routed" \
    || rec PASS P16 "$LIB_HOST has no DNS yet" "expected before dns-route.sh"
fi

print_table "preflight $config"
[ "$FAILS" -eq 0 ]
