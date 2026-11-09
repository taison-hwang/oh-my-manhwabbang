#!/usr/bin/env bash
#
# e2e-oidc.sh — the oidc round of ruling E-74 (U-E), invoked by
# `make e2e-synthetic-oidc`. The password/no-auth rounds stay in scripts/e2e/e2e.sh.
#
#   1. build dist/shelf and the fake uni-auth (tools/fakeoidc, not linked
#      into dist/shelf)
#   2. build the synthetic fixture tree and start the fake on a free loopback port
#   3. write an `auth.mode: oidc` config whose issuer is the fake (E-74 §2.19's
#      loopback http exception) and start the real binary on it
#   4. wait for the start-up scan, then curl the surface a visitor without a
#      session reaches (§2.11 cross_origin, §2.18)
#   5. Playwright: web/e2e/14-oidc.spec.ts under web/playwright.oidc.config.ts
#
# Everything lives under $SHELF_E2E_STATE (default /tmp/shelf-e2e-oidc-<pid>);
# the server listens on $SHELF_E2E_OIDC_PORT (default 8792), never 9909/9910.
#
# Options:
#   --keep             leave the state directory behind
#   --no-build         reuse dist/shelf as it is
#   --no-playwright    stop after step 4

set -uo pipefail

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$repo"

keep=0
do_build=1
do_playwright=1
while [ $# -gt 0 ]; do
  case "$1" in
    --keep) keep=1 ;;
    --no-build) do_build=0 ;;
    --no-playwright) do_playwright=0 ;;
    -h|--help) sed -n '2,24p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "e2e-oidc.sh: unknown argument $1" >&2; exit 2 ;;
  esac
  shift
done

port="${SHELF_E2E_OIDC_PORT:-8792}"
case "$port" in 9909|9910) echo "e2e-oidc.sh: port $port belongs to the user's servers" >&2; exit 2 ;; esac
STATE="${SHELF_E2E_STATE:-/tmp/shelf-e2e-oidc-$$}"
BASE="http://127.0.0.1:$port"
BIN="$repo/dist/shelf"
FAKE_BIN="$STATE/bin/fakeoidc"
CONFIG="$STATE/shelf.oidc.yaml"
LOG="$STATE/server.log"
FAKE_LOG="$STATE/fakeoidc.log"
GOENV=(GOPROXY=https://proxy.golang.org,direct GOSUMDB=sum.golang.org GOTOOLCHAIN=auto)

# The fake's users. Admin ⊂ allowed; mallory signs in at uni-auth but is not
# on the list (E-74 §2.8).
SUB_ALICE="a11ce000-0000-4000-8000-00000000000a"
SUB_BOB="b0b00000-0000-4000-8000-00000000000b"
SUB_MALLORY="ma11000-0000-4000-8000-00000000000c"
CLIENT_ID="shelf-e2e"

failures=0
server_pid=""
fake_pid=""
pw_pid=""

step() { printf '\n\033[1m==> %s\033[0m\n' "$*"; }
ok()   { printf '  PASS  %s\n' "$*"; }
bad()  { printf '  FAIL  %s\n' "$*"; failures=$((failures + 1)); }
die()  { printf '\ne2e-oidc.sh: %s\n' "$*" >&2; exit 1; }

stop_pid() {
  local pid="$1"
  [ -n "$pid" ] || return 0
  kill "$pid" 2>/dev/null || true
  for _ in $(seq 1 100); do kill -0 "$pid" 2>/dev/null || return 0; sleep 0.1; done
  kill -9 "$pid" 2>/dev/null || true
}

cleanup() {
  [ -n "$pw_pid" ] && { kill -TERM "-$pw_pid" 2>/dev/null || kill -TERM "$pw_pid" 2>/dev/null || true; }
  stop_pid "$server_pid"; server_pid=""
  stop_pid "$fake_pid"; fake_pid=""
  if [ "$keep" -eq 1 ]; then
    printf '\nkept: %s\n' "$STATE"
  else
    rm -rf "$STATE"
  fi
}
trap cleanup EXIT
trap 'exit 130' INT TERM

# ---------------------------------------------------------------------------
step "1 · build"
# ---------------------------------------------------------------------------
if ss -ltnH "sport = :$port" 2>/dev/null | grep -q .; then
  die "port $port is already in use: $(ss -ltnpH "sport = :$port")"
fi
mkdir -p "$STATE/bin" "$STATE/data" "$STATE/cache" "$STATE/tmp"
if [ "$do_build" -eq 1 ]; then
  make build >/dev/null || die "make build failed"
fi
[ -x "$BIN" ] || die "$BIN does not exist; run without --no-build"
env "${GOENV[@]}" CGO_ENABLED=0 go build -o "$FAKE_BIN" ./tools/fakeoidc || die "building tools/fakeoidc failed"
ok "$("$BIN" --version | head -1)"

# ---------------------------------------------------------------------------
step "2 · fixture and fake uni-auth"
# ---------------------------------------------------------------------------
env "${GOENV[@]}" CGO_ENABLED=0 go run ./tools/mkfixture -out "$STATE/fixture" >/dev/null \
  || die "building the synthetic fixture tree failed"
"$FAKE_BIN" -addr 127.0.0.1:0 -addr-file "$STATE/fakeoidc.addr" -client-id "$CLIENT_ID" \
  -audience "$BASE/api" -redirect-uri "$BASE/api/auth/oidc/callback" \
  -users "alice=$SUB_ALICE,bob=$SUB_BOB,mallory=$SUB_MALLORY" >"$FAKE_LOG" 2>&1 &
fake_pid=$!
for _ in $(seq 1 100); do [ -s "$STATE/fakeoidc.addr" ] && break; sleep 0.1; done
ISSUER=$(tr -d '\n' <"$STATE/fakeoidc.addr" 2>/dev/null)
[ -n "$ISSUER" ] || die "the fake never wrote its address: $(cat "$FAKE_LOG")"
curl -fsS -m 5 "$ISSUER/.well-known/openid-configuration" >/dev/null || die "the fake does not answer discovery"
ok "fake uni-auth at $ISSUER"

# ---------------------------------------------------------------------------
step "3 · oidc-mode server"
# ---------------------------------------------------------------------------
cat >"$CONFIG" <<EOF
server:
  listen: "127.0.0.1"
  port: $port
  base_path: ""
  shutdown_grace: "5s"
roots:
  - name: "mangga"
    label: "만화 (oidc E2E)"
    path: "$STATE/fixture"
storage:
  data_dir: "$STATE/data"
  cache_dir: "$STATE/cache"
scan:
  on_start: true
  workers: 4
  include_globs:
    - "Clover 클로버 (총4권)"
    - "바퀴.zip"
thumbnails:
  widths: [120, 240, 400, 640]
  workers: 2
log:
  level: "debug"
  format: "text"
auth:
  mode: "oidc"
  session_ttl: "12h"
  oidc:
    issuer: "$ISSUER"
    client_id: "$CLIENT_ID"
    audience: "$BASE/api"
    redirect_url: "$BASE/api/auth/oidc/callback"
    allowed_subs: ["$SUB_ALICE", "$SUB_BOB"]
    admin_subs: ["$SUB_ALICE"]
EOF
TMPDIR="$STATE/tmp" "$BIN" --config "$CONFIG" >"$LOG" 2>&1 &
server_pid=$!
up=0
for _ in $(seq 1 300); do
  kill -0 "$server_pid" 2>/dev/null || die "the server exited during start-up: $(tail -20 "$LOG")"
  curl -fsS -m 2 "$BASE/api/health" >/dev/null 2>&1 && { up=1; break; }
  sleep 0.1
done
[ "$up" -eq 1 ] || die "the server did not answer /api/health within 30 s"
owner=$(ss -ltnpH "sport = :$port" | grep -o 'pid=[0-9]*' | cut -d= -f2 | sort -u)
[ "$owner" = "$server_pid" ] || die "port $port is held by pid(s) '$owner', not our server $server_pid"
ok "listening on $BASE (pid $server_pid)"

found=0
for _ in $(seq 1 1200); do
  grep -q 'msg="scan finished"' "$LOG" && { found=1; break; }
  sleep 0.1
done
[ "$found" -eq 1 ] && ok "the start-up scan finished" || die "no 'scan finished' in $LOG within 120 s"

# ---------------------------------------------------------------------------
step "4 · the surface without a session (E-74 §2.11, §2.18)"
# ---------------------------------------------------------------------------
# reason <curl args...> — prints "<status> <detail.reason>".
reason() {
  curl -sS -m 10 -o "$STATE/body" -w '%{http_code}' "$@" >"$STATE/code" 2>/dev/null || true
  printf '%s %s' "$(cat "$STATE/code")" "$(python3 -c '
import json, sys
try:
    d = json.load(open(sys.argv[1]))
    print((d.get("error", {}).get("detail") or {}).get("reason", "-"))
except Exception:
    print("not-json")' "$STATE/body")"
}
expect_reason() {
  local want="$1" what="$2"; shift 2
  local got
  got=$(reason "$@")
  [ "$got" = "$want" ] && ok "$what → $got" || bad "$what: expected '$want', got '$got'"
}
JSON=(-H 'Content-Type: application/json' -d '{"full":false}')
expect_reason "403 cross_origin" "POST /api/scan from another Origin" \
  -X POST -H 'Origin: http://127.0.0.1.evil.example' "${JSON[@]}" "$BASE/api/scan"
expect_reason "403 cross_origin" "POST /api/scan with Sec-Fetch-Site: cross-site and no Origin" \
  -X POST -H 'Sec-Fetch-Site: cross-site' "${JSON[@]}" "$BASE/api/scan"
expect_reason "403 cross_origin" "POST /api/scan with neither Origin nor Sec-Fetch-Site" \
  -X POST "${JSON[@]}" "$BASE/api/scan"
expect_reason "403 cross_origin" "PUT /api/settings from a neighbouring port's Origin" \
  -X PUT -H "Origin: http://127.0.0.1:$((port + 1))" -H 'Content-Type: application/json' -d '{}' "$BASE/api/settings"
expect_reason "403 cross_origin" "POST /api/auth/oidc/end-session from another Origin" \
  -X POST -H 'Origin: http://evil.example' "$BASE/api/auth/oidc/end-session"
# The same request from our own origin gets past the origin check and is refused
# for having no session — so the 403s above are the origin check, not a blanket refusal.
expect_reason "401 -" "POST /api/scan from our own Origin, no session" \
  -X POST -H "Origin: $BASE" "${JSON[@]}" "$BASE/api/scan"
expect_reason "401 -" "GET /api/series without a session" "$BASE/api/series"
code=$(curl -sS -m 10 -o "$STATE/body" -w '%{http_code}' -H 'Accept: text/html' "$BASE/series/x" || true)
if [ "$code" = "401" ] && grep -q 'uni-auth로 로그인' "$STATE/body" && ! grep -qi '<script' "$STATE/body"; then
  ok "a browser visit without a session gets the server's sign-in page (401, no SPA assets)"
else
  bad "a browser visit without a session: status $code, body $(head -c 200 "$STATE/body")"
fi

# ---------------------------------------------------------------------------
step "5 · Playwright (web/e2e/14-oidc.spec.ts)"
# ---------------------------------------------------------------------------
if [ "$do_playwright" -eq 0 ]; then
  echo "  skip  --no-playwright"
else
  ( cd web && exec setsid env PLAYWRIGHT_BASE_URL="$BASE" PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1 \
      SHELF_E2E_OIDC_ISSUER="$ISSUER" SHELF_E2E_OIDC_AUTH_DB="$STATE/data/auth.db" \
      SHELF_E2E_OIDC_ALICE="$SUB_ALICE" SHELF_E2E_OIDC_BOB="$SUB_BOB" SHELF_E2E_OIDC_MALLORY="$SUB_MALLORY" \
      pnpm exec playwright test --config playwright.oidc.config.ts ) &
  pw_pid=$!
  if wait "$pw_pid"; then ok "playwright"; else bad "playwright: see web/playwright-report-oidc/"; fi
  pw_pid=""
fi

# ---------------------------------------------------------------------------
step "summary"
# ---------------------------------------------------------------------------
stop_pid "$server_pid"; server_pid=""
if grep -Eq 'id_token=|code_verifier=|[?&]code=[A-Za-z0-9_-]{10}' "$LOG"; then
  bad "server.log carries a token, a code or a verifier (E-74 §2.6)"
else
  ok "server.log carries no code, verifier or token"
fi
if [ "$failures" -eq 0 ]; then
  printf '\n\033[32mE2E PASSED\033[0m (oidc round)\n'
  exit 0
fi
printf '\n\033[31mE2E FAILED\033[0m — %d failing step(s), oidc round\nserver log: %s\n' "$failures" "$LOG"
[ "$keep" -eq 1 ] || printf '(rerun with --keep to keep it)\n'
exit 1
