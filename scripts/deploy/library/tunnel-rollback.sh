#!/usr/bin/env bash
# tunnel-rollback.sh — put a backup made by tunnel-apply.sh back, validate, and
# (only with --restart) restart cloudflared.
#
#   sudo tunnel-rollback.sh --backup /etc/cloudflared/config.yml.bak-<UTC> [--restart]
#   tunnel-rollback.sh --backup B --config-path /tmp/copy/config.yml     # rehearsal
#
# The current file is itself saved as <config>.rolledback-<UTC> first, so a
# rollback can be undone.
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=common.sh
. "$here/common.sh"

cfg="$CF_CONFIG_DEFAULT" backup="" restart=0 rehearsal=0
while [ $# -gt 0 ]; do
  case "$1" in
    --backup) backup="$2"; shift 2 ;;
    --config-path) cfg="$2"; rehearsal=1; shift 2 ;;
    --restart) restart=1; shift ;;
    -h|--help) sed -n '2,10p' "$0"; exit 0 ;;
    *) die "unknown argument: $1" ;;
  esac
done
[ -n "$backup" ] && [ -f "$backup" ] || die "--backup FILE is required and must exist"
[ -f "$cfg" ] || die "no such file: $cfg"
[ "$rehearsal" = 1 ] && [ "$restart" = 1 ] && die "--restart is refused with --config-path"
if [ "$rehearsal" = 0 ] && [ "$(id -u)" -ne 0 ]; then die "run with sudo"; fi
need cloudflared

saved="$cfg.rolledback-$(utc_stamp)"
cp -p "$cfg" "$saved"
cp -p "$backup" "$cfg"
say "restored $cfg from $backup (previous file kept as $saved)"
out="$(cloudflared tunnel --config "$cfg" ingress validate 2>&1)" || true
if ! grep -qx 'OK' <<<"$out"; then
  say "validate FAILED on the restored file: $out"
  say "the previous file is at $saved; cloudflared was not restarted"
  exit 1
fi
say "ingress validate: OK"
if [ "$restart" = 1 ]; then
  systemctl restart cloudflared
  sleep 3
  systemctl is-active --quiet cloudflared && say "cloudflared is active" || { say "cloudflared is NOT active"; exit 1; }
else
  say "not restarted; cloudflared still runs whatever it loaded last. Restart with: sudo systemctl restart cloudflared"
fi
