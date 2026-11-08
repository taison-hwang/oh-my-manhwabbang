#!/usr/bin/env bash
# dns-route.sh — create the library.betterax.net CNAME on the gift4me-tunnel.
#
#   dns-route.sh [--yes]
#
# This is the step that makes the hostname resolve to Cloudflare. Run it only
# after the local verify-public.sh passes. It prints the command and asks first.
# Undo: delete the `library` CNAME in the Cloudflare dashboard (DNS records stay
# harmless while the catch-all returns 404, but delete it to be clean).
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=common.sh
. "$here/common.sh"
yes=0
[ "${1:-}" = "--yes" ] && yes=1
need cloudflared
cmd=(cloudflared tunnel route dns "$TUNNEL_NAME" "$LIB_HOST")
printf 'About to run:\n  %s\n' "${cmd[*]}"
printf 'This publishes %s in DNS (proxied through Cloudflare).\n' "$LIB_HOST"
if [ "$yes" != 1 ]; then
  read -r -p "Type the hostname to confirm: " ans
  [ "$ans" = "$LIB_HOST" ] || { echo "not confirmed; nothing changed"; exit 1; }
fi
"${cmd[@]}"
echo "done. If cloudflared reports a permission error, create the CNAME in the dashboard:"
echo "  library -> 178647b8-b76b-49a3-8b8f-3ca06f191226.cfargotunnel.com (proxied)"
