#!/usr/bin/env bash
# verify-public.sh — bypass-path check for the books.betterax.net rollout
# (E-74 §2.18). Every request is made WITHOUT a session. Run it twice: before the
# DNS/tunnel steps against the local port, and after them against the public URL.
#
#   verify-public.sh --local  [--port 9910]
#   verify-public.sh --public [--url https://books.betterax.net]
#   verify-public.sh --local --public            # both
#   verify-public.sh --base http://127.0.0.1:8791  # any one target (testing the script)
#
# Prints a PASS/FAIL table; exit 1 if anything FAILs. The expectations are the
# contract for auth.mode "oidc": a server with no auth, or in password mode, FAILs.
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=common.sh
. "$here/common.sh"

port="$DEFAULT_PORT" do_local=0 do_public=0 url="https://$LIB_HOST" base="" iface_check=1
while [ $# -gt 0 ]; do
  case "$1" in
    --local) do_local=1; shift ;;
    --public) do_public=1; shift ;;
    --port) port="$2"; shift 2 ;;
    --url) url="$2"; shift 2 ;;
    --base) base="$2"; shift 2 ;;
    -h|--help) sed -n '2,13p' "$0"; exit 0 ;;
    *) die "unknown argument: $1" ;;
  esac
done
[ "$do_local$do_public$base" != "00" ] || die "give --local, --public or --base URL"
need curl; need python3; need ss

tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
n=0
# req <label> <curl args...> -> $tmp/<label>.{hdr,body}, sets CODE
req() {
  local label="$1"; shift
  CODE="$(curl -sS --max-time 15 -o "$tmp/$label.body" -D "$tmp/$label.hdr" -w '%{http_code}' "$@" 2>"$tmp/$label.err" || echo 000)"
}
hdr() { tr -d '\r' <"$tmp/$1.hdr" | awk -v k="$(tr 'A-Z' 'a-z' <<<"$2")" 'tolower($1)==k":"{ $1=""; sub(/^ /,""); print; exit }'; }
all_hdrs=()

check_target() { # prefix base
  local p="$1" b="$2" id
  nid() { n=$((n + 1)); id="$p$n"; }

  nid; req root -H 'Accept: text/html' "$b/"; all_hdrs+=(root)
  if [ "$CODE" = 401 ] && ! grep -qiE '<script[^>]+src=|/assets/' "$tmp/root.body"; then
    rec PASS "$id" "[$p] GET / without session -> 401 login page, no SPA assets" "401"
  else rec FAIL "$id" "[$p] GET / without session -> 401 login page, no SPA assets" "HTTP $CODE (SPA or content reachable)"; fi

  nid; req series -H 'Accept: application/json' "$b/api/series"; all_hdrs+=(series)
  [ "$CODE" = 401 ] && rec PASS "$id" "[$p] GET /api/series -> 401" "401" || rec FAIL "$id" "[$p] GET /api/series -> 401" "HTTP $CODE"

  local path
  for path in /api/books/x/pages/1 /api/books/x/thumbs/1 /api/books/x/cover /api/series/x/cover /api/continue /api/progress/export /api/settings /api/scan/log; do
    nid; req rep -H 'Accept: application/json' "$b$path"; all_hdrs+=(rep); cp "$tmp/rep.hdr" "$tmp/rep$n.hdr"; all_hdrs+=("rep$n")
    [ "$CODE" = 401 ] && rec PASS "$id" "[$p] GET $path -> 401" "401" || rec FAIL "$id" "[$p] GET $path -> 401" "HTTP $CODE"
  done

  for path in /index.html /assets/index.js /favicon.ico /manifest.webmanifest /sw.js; do
    nid; req spa "$b$path"; all_hdrs+=(spa)
    [ "$CODE" = 401 ] && rec PASS "$id" "[$p] SPA file $path -> 401" "401" || rec FAIL "$id" "[$p] SPA file $path -> 401" "HTTP $CODE"
  done

  nid; req trav -L --path-as-is -H 'Accept: application/json' "$b/api/auth/../series"
  case "$CODE" in
    400|401|404) rec PASS "$id" "[$p] /api/auth/../series does not slip past the gate" "HTTP $CODE" ;;
    *) rec FAIL "$id" "[$p] /api/auth/../series does not slip past the gate" "HTTP $CODE" ;;
  esac

  nid; req health "$b/api/health?verbose=1"
  if [ "$CODE" = 200 ] && python3 -I - "$tmp/health.body" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))
sys.exit(0 if d.get("ok") is not None and "verbose" not in d else 1)
PY
  then rec PASS "$id" "[$p] /api/health?verbose=1 without session has no verbose block" "200"
  else rec FAIL "$id" "[$p] /api/health?verbose=1 without session has no verbose block" "HTTP $CODE (verbose block or not JSON)"; fi

  nid; req status "$b/api/auth/status"
  if [ "$CODE" = 200 ] && python3 -I - "$tmp/status.body" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))
sys.exit(0 if d.get("mode") == "oidc" and d.get("authenticated") is False and "user" not in d else 1)
PY
  then rec PASS "$id" "[$p] /api/auth/status says mode oidc, not authenticated" "200"
  else rec FAIL "$id" "[$p] /api/auth/status says mode oidc, not authenticated" "HTTP $CODE, body: $(head -c 100 "$tmp/status.body" | tr '\n' ' ')"; fi

  nid; req xo -X POST -H 'Origin: https://evil.example' -H 'Content-Type: application/json' -d '{}' "$b/api/progress/import"
  if [ "$CODE" = 403 ] && grep -q 'cross_origin' "$tmp/xo.body"; then
    rec PASS "$id" "[$p] cross-origin POST -> 403 cross_origin" "403"
  else rec FAIL "$id" "[$p] cross-origin POST -> 403 cross_origin" "HTTP $CODE"; fi

  nid; req start -o /dev/null "$b/api/auth/oidc/start?return=/"
  loc="$(hdr start location)"
  if [ "$CODE" = 302 ] && [[ "$loc" == https://auth.betterax.net/* ]]; then
    rec PASS "$id" "[$p] oidc/start redirects to the issuer" "302 -> ${loc:0:50}"
  else rec FAIL "$id" "[$p] oidc/start redirects to the issuer" "HTTP $CODE ${loc:0:60}"; fi
  nid; setck="$(hdr start set-cookie)"
  if [[ "$b" == https://* ]]; then
    if grep -qi 'secure' <<<"$setck" && grep -qi 'httponly' <<<"$setck"; then rec PASS "$id" "[$p] state cookie is Secure and HttpOnly" ""
    else rec FAIL "$id" "[$p] state cookie is Secure and HttpOnly" "Set-Cookie: ${setck:0:80}"; fi
  else rec SKIP "$id" "[$p] state cookie Secure flag" "plain http target (Secure cookies need the https URL)"; fi

  nid; local bad=""
  for h in "${all_hdrs[@]}"; do
    [ -f "$tmp/$h.hdr" ] && grep -qi '^cache-control:.*\bpublic\b' "$tmp/$h.hdr" && bad="$bad $h"
  done
  [ -z "$bad" ] && rec PASS "$id" "[$p] no response carries Cache-Control: public" "" || rec FAIL "$id" "[$p] no response carries Cache-Control: public" "in:$bad"

  nid
  if [[ "$b" == https://* ]]; then
    req again -H 'Accept: application/json' "$b/api/series"
    cfs="$(hdr again cf-cache-status)"
    if [ -z "$cfs" ]; then rec WARN "$id" "[$p] cf-cache-status is not HIT" "no cf-cache-status header (not via Cloudflare?)"
    elif [ "$cfs" = HIT ]; then rec FAIL "$id" "[$p] cf-cache-status is not HIT" "HIT"
    else rec PASS "$id" "[$p] cf-cache-status is not HIT" "$cfs"; fi
  else rec SKIP "$id" "[$p] cf-cache-status is not HIT" "not a Cloudflare target"; fi
}

if [ -n "$base" ]; then check_target X "${base%/}"; fi
if [ "$do_local" = 1 ]; then
  check_target L "http://127.0.0.1:$port"

  # Listener and LAN reachability: only the loopback address may answer.
  n=$((n + 1)); id="L$n"
  listeners="$(ss -ltnH "sport = :$port" | awk '{print $4}')"
  if [ -z "$listeners" ]; then rec FAIL "$id" "[L] something listens on :$port" "no listener"
  elif grep -qE '^(0\.0\.0\.0|\*|\[::\]|::):' <<<"$listeners"; then
    rec FAIL "$id" "[L] :$port is bound to loopback only (no 0.0.0.0 / ::)" "$(tr '\n' ' ' <<<"$listeners")"
  else rec PASS "$id" "[L] :$port is bound to loopback only (no 0.0.0.0 / ::)" "$(tr '\n' ' ' <<<"$listeners")"; fi

  n=$((n + 1)); id="L$n"
  wild="$(ss -ltnpH 2>/dev/null | grep -E '"shelf"' | awk '{print $4}' | grep -E '^(0\.0\.0\.0|\*|\[::\]|::):' || true)"
  [ -z "$wild" ] && rec PASS "$id" "[L] no shelf process listens on a wildcard address (any port)" "" \
    || rec FAIL "$id" "[L] no shelf process listens on a wildcard address (any port)" "$(tr '\n' ' ' <<<"$wild")"

  mapfile -t ips < <(ip -4 -o addr show scope global 2>/dev/null | awk '{split($4,a,"/"); print a[1]}')
  if [ "${#ips[@]}" -eq 0 ]; then
    n=$((n + 1)); rec SKIP "L$n" "[L] LAN address refuses :$port" "no non-loopback IPv4 address found"
  fi
  for ip in "${ips[@]}"; do
    n=$((n + 1)); id="L$n"
    c="$(curl -s --max-time 3 -o /dev/null -w '%{http_code}' "http://$ip:$port/api/health" 2>/dev/null || true)"
    if [ "$c" = 000 ] || [ -z "$c" ]; then rec PASS "$id" "[L] LAN $ip:$port refuses the connection" "no answer"
    else rec FAIL "$id" "[L] LAN $ip:$port refuses the connection" "HTTP $c: reachable from the LAN"; fi
  done
fi
if [ "$do_public" = 1 ]; then
  check_target P "${url%/}"
fi

print_table "verify-public"
[ "$FAILS" -eq 0 ]
