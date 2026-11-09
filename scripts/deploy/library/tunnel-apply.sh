#!/usr/bin/env bash
# tunnel-apply.sh — add the books.betterax.net ingress rule to the cloudflared
# configuration.
#
#   sudo tunnel-apply.sh [--service http://127.0.0.1:9910] [--restart]
#   tunnel-apply.sh --config-path /tmp/copy/config.yml          # rehearsal, no sudo
#
# Order: back up -> insert the rule (after the last hostname rule, before the
# catch-all) -> `ingress validate` -> four `ingress rule` checks. Any failure
# restores the backup and exits 1 WITHOUT restarting. cloudflared is restarted
# only with --restart, and only on the real /etc path (never with --config-path).
# Restarting drops auth.betterax.net and ohmyassets.betterax.net for a few seconds.
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=common.sh
. "$here/common.sh"

cfg="$CF_CONFIG_DEFAULT" service="http://127.0.0.1:$DEFAULT_PORT" restart=0 rehearsal=0
while [ $# -gt 0 ]; do
  case "$1" in
    --config-path) cfg="$2"; rehearsal=1; shift 2 ;;
    --service) service="$2"; shift 2 ;;
    --restart) restart=1; shift ;;
    -h|--help) sed -n '2,12p' "$0"; exit 0 ;;
    *) die "unknown argument: $1" ;;
  esac
done
[ -f "$cfg" ] || die "no such file: $cfg"
[ "$rehearsal" = 1 ] && [ "$restart" = 1 ] && die "--restart is refused with --config-path (it would restart the real service)"
if [ "$rehearsal" = 0 ] && [ "$(id -u)" -ne 0 ]; then
  die "run with sudo (it writes $cfg); use --config-path to rehearse on a copy"
fi
need cloudflared; need python3

grep -q "hostname: *$LIB_HOST" "$cfg" && die "$cfg already has a rule for $LIB_HOST; nothing changed"

backup="$cfg.bak-$(utc_stamp)"
cp -p "$cfg" "$backup"
say "backup: $backup"

restore() {
  cp -p "$backup" "$cfg"
  say "RESTORED $cfg from $backup; cloudflared was not restarted"
}

# Insert before the catch-all (the last `- service:` rule, with its leading comment).
tmp="$(mktemp "$cfg.new.XXXXXX")"
trap 'rm -f "$tmp"' EXIT
if ! python3 -I - "$cfg" "$tmp" "$LIB_HOST" "$service" <<'PY'
import re, sys
src, dst, host, service = sys.argv[1:5]
lines = open(src).read().split("\n")
idx = [i for i, l in enumerate(lines) if re.match(r"^(\s*)-\s+service:\s*\S+", l)]
if not idx:
    sys.exit("no catch-all rule (`- service: http_status:404`) found; refusing to guess")
c = idx[-1]
if "http_status" not in lines[c]:
    sys.exit("the last ingress rule is not an http_status catch-all; refusing to guess")
ind = re.match(r"^(\s*)", lines[c]).group(1)
at = c
while at > 0 and lines[at - 1].lstrip().startswith("#"):
    at -= 1
block = [
    f"{ind}# SHELF (zip-viewer) - oidc 모드 단일 인스턴스, 루프백 바인드 (E-74 §2.18)",
    f"{ind}- hostname: {host}",
    f"{ind}  service: {service}",
    f"{ind}  originRequest:",
    f"{ind}    httpHostHeader: {host}",
    f"{ind}    connectTimeout: 10s",
    "",
]
open(dst, "w").write("\n".join(lines[:at] + block + lines[at:]))
PY
then
  restore; exit 1
fi
chmod --reference="$cfg" "$tmp"
mv "$tmp" "$cfg"
trap - EXIT

cf() { cloudflared tunnel --config "$cfg" "$@"; }
fail=0
if out="$(cf ingress validate 2>&1)" && grep -qx 'OK' <<<"$out"; then
  rec PASS T1 "ingress validate" "OK"
else
  rec FAIL T1 "ingress validate" "$(tr '\n' ' ' <<<"$out" | cut -c1-120)"; fail=1
fi
rule() { # id url want-service
  local out got
  out="$(cf ingress rule "$2" 2>&1)" || true
  got="$(awk '/service:/{print $2; exit}' <<<"$out")"
  if [ "$got" = "$3" ]; then rec PASS "$1" "rule $2" "-> $3"
  else rec FAIL "$1" "rule $2" "got '${got:-none}', want '$3'"; fail=1; fi
}
rule T2 "https://$LIB_HOST/" "$service"
rule T3 "https://auth.betterax.net/" "https://127.0.0.1:8443"
rule T4 "https://ohmyassets.betterax.net/" "http://127.0.0.1:18100"
rule T5 "https://other.betterax.net/" "http_status:404"
print_table "tunnel-apply $cfg"

if [ "$fail" -ne 0 ]; then
  restore; exit 1
fi
if [ "$restart" = 1 ]; then
  say "restarting cloudflared (auth and ohmyassets drop for a few seconds)"
  systemctl restart cloudflared
  sleep 3
  systemctl is-active --quiet cloudflared || { say "cloudflared is NOT active: run tunnel-rollback.sh --backup $backup --restart"; exit 1; }
  say "cloudflared is active. Check: journalctl -u cloudflared -n 20 | grep -i 'registered tunnel connection'"
else
  say "config is in place; cloudflared is still running the OLD rules. Restart with:"
  say "  sudo systemctl restart cloudflared    (or rerun with --restart)"
fi
say "rollback: sudo $here/tunnel-rollback.sh --backup $backup --restart"
