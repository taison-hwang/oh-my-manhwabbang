#!/usr/bin/env bash
#
# e2e.sh — the end-to-end run of impl-plan §6.3, invoked by `make e2e`.
#
#   scripts/e2e.sh                 the curated real-collection subset
#   scripts/e2e.sh --synthetic     the hermetic twin, no media volume needed
#
# What it does, in order:
#
#   1. build dist/shelf (the real binary, with the SPA embedded)
#   2. write test/shelf.e2e.yaml: a root pointed AT THE REAL COLLECTION,
#      narrowed to ten series by scan.include_globs. Nothing is copied — that
#      is the whole point of A-3 (D-48). data_dir and cache_dir go to a scratch
#      directory under /tmp.
#   3. record a marker so FR-CFG-005 can be proved afterwards
#   4. start the server on :8791 and wait for GET /api/health
#   5. NFR-OPS-006: the library answers before any scan has run
#   6. POST /api/scan {"full":true}; poll /api/scan/status until idle, 180 s cap
#   7. the curl assertion suite (scripts/e2e-assert.py)
#   8. NFR-PRF-004: a second scan of the same tree finishes in under 30 s
#  8b. A-11: POST /api/roots writes the configuration file — synthetic only
#   9. FR-CFG-005 / AC-001: nothing under the media root changed, and nothing
#      was written outside cache_dir and data_dir
#  10. AC-005 / AC-006: kill the server, delete index.db* and the whole cache,
#      restart, rescan — reading progress survives and covers regenerate
# 10b. A-11: that restart adopted the root added in 8b — synthetic only
#  11. Playwright, when web/e2e carries specs
# 11b. A-11: DELETE /api/roots/{name} — synthetic only
#  12. tear down and print a summary
#
# The lettered steps are lettered rather than numbered so that "step 9" and
# "step 10" keep meaning what they mean in docs/HANDOFF.md and in the comments
# throughout this file.
#
# Options:
#   --synthetic        build a ~2 MB fixture tree and test against that
#   --keep             leave the scratch directory and the config behind
#   --no-build         reuse dist/shelf as it is
#   --no-playwright    skip step 11 even if specs exist
#   --port N           override the E2E port (default 8791)

set -uo pipefail

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo"

synthetic=0
keep=0
do_build=1
do_playwright=1
port="${SHELF_E2E_PORT:-8791}"

while [ $# -gt 0 ]; do
  case "$1" in
    --synthetic) synthetic=1 ;;
    --keep) keep=1 ;;
    --no-build) do_build=0 ;;
    --no-playwright) do_playwright=0 ;;
    --port)
      [ $# -ge 2 ] || { echo "e2e.sh: --port needs a port number" >&2; exit 2; }
      case "$2" in
        ''|*[!0-9]*) echo "e2e.sh: --port takes a number, got '$2'" >&2; exit 2 ;;
      esac
      port="$2"; shift ;;
    -h|--help) sed -n '2,40p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "e2e.sh: unknown argument $1" >&2; exit 2 ;;
  esac
  shift
done

REAL_ROOT="${SHELF_E2E_ROOT:-/mnt/big-data/pds/taison-data/02. books/01. mangga}"
STATE="${SHELF_E2E_STATE:-/tmp/shelf-e2e-$$}"
FIXTURE="$STATE/fixture"
SRVTMP="$STATE/tmp"          # the server's private TMPDIR — step 9 asserts it stays empty

# Where the configuration is written — and it is NOT the same place in both
# rounds, because in the synthetic round the product itself writes to it.
#
# Amendment A-11 (ruling E-26) makes `POST /api/roots` an edit to this file,
# with a `.bak` beside it. In the real round that file is `test/shelf.e2e.yaml`
# INSIDE THE REPOSITORY, and step 9 fails the run for creating anything there —
# correctly, which is why the real round leaves `server.allow_root_editing` off
# (the shipped default) rather than being excused from the check. The synthetic
# round writes to a config under $STATE in /tmp, beside the fixture tree it
# already owns, where a real write threatens nothing.
if [ "$synthetic" -eq 1 ]; then
  CONFIG="$STATE/shelf.e2e.yaml"
else
  CONFIG="$repo/test/shelf.e2e.yaml"
fi

# The root that steps 8b / 10b / 11b add, adopt and remove (A-11).
#
# `A11_ROOT` is outside $FIXTURE and outside $STATE/data and $STATE/cache, which
# is what §7.4's `overlaps` and `contains_storage` rules require of it. It stays
# EMPTY until step 11b: step 10's AC-005 check holds the rebuilt library against
# the one before the wipe name for name, and a root carrying series would break
# that comparison for a reason that has nothing to do with AC-005.
# `A11_LABEL` is already a valid slug, so §7.4's generator produces it verbatim
# as the root's name — which is how the browser tier addresses the row.
A11_ROOT="$STATE/a11-root"
A11_LABEL="e2e-a11"
A11_STATE="$STATE/a11.json"
A11_UI_ROOT="$STATE/a11-ui"   # one directory per viewport project, for the browser tier

LOG="$STATE/server.log"
BASE="http://127.0.0.1:$port"
BIN="$repo/dist/shelf"

GOENV=(GOPROXY=https://proxy.golang.org,direct GOSUMDB=sum.golang.org GOTOOLCHAIN=auto)

failures=0
server_pid=""
pw_pid=""              # step 11's Playwright job …
pw_pgid=""             # … and the process group cleanup has to take down with it
cleanup_ran=0
BIN_VERSION=""
BIN_COMMIT=""
BIN_VERSION_LINE=""

# How long any port guard waits for an answer. Generous on purpose: the
# pre-flight and the post-stop check both read "no answer" as "nothing is
# there", so a foreign server that is merely slow must never be able to look
# like an absent one. One second was not enough for that.
CURL_TIMEOUT="${SHELF_E2E_CURL_TIMEOUT:-10}"
# The wall-clock budget for a server to come up and answer /api/health.
START_TIMEOUT="${SHELF_E2E_START_TIMEOUT:-30}"

step()  { printf '\n\033[1m==> %s\033[0m\n' "$*"; }
ok()    { printf '  PASS  %s\n' "$*"; }
bad()   { printf '  FAIL  %s\n' "$*"; failures=$((failures + 1)); }
die()   { printf '\ne2e.sh: %s\n' "$*" >&2; exit 1; }
have()  { command -v "$1" >/dev/null 2>&1; }

# --- who owns the port -----------------------------------------------------
#
# Ruling E-22: a health probe cannot tell whose server answered it. The kernel
# can. These three are Linux-only, which this script already is (`date +%s%3N`,
# GNU find); every one of them fails loudly rather than silently reporting
# "nobody is there".

# The listening sockets on $port, one line each, carrying `pid=` for every
# process we are allowed to see — which is every process of ours.
port_listeners() { ss -ltnpH "sport = :$port" 2>/dev/null; }

# The distinct pids holding a listening socket on $port.
port_owner_pids() { port_listeners | grep -o 'pid=[0-9]*' | cut -d= -f2 | sort -u; }

# Is ANYTHING on the port? A listening socket proves it by itself and needs no
# cooperation from whatever sits behind it; the health probe is the fallback
# for a machine without ss(8). curl exit 7 — "could not connect" — is the only
# result that means nothing is there. A timeout (28), an HTTP error (22) or a
# dropped connection (52/56) all mean a process, and treating slowness as
# absence is precisely how a foreign server gets to grade our run.
port_is_live() {
  local rc=0
  if have ss && [ -n "$(port_listeners)" ]; then return 0; fi
  curl -fsS -m "$CURL_TIMEOUT" "$BASE/api/health" >/dev/null 2>&1 || rc=$?
  [ "$rc" -ne 7 ]
}

# `shelf --version` prints:  shelf <version> (<commit>, built <ts>, <go> <os/arch>)
read_bin_version() {
  local out
  out=$("$BIN" --version 2>/dev/null) || die "\`$BIN --version\` failed — is that the shelf binary?"
  BIN_VERSION_LINE=$(printf '%s\n' "$out" | head -1)
  BIN_VERSION=$(printf '%s\n' "$BIN_VERSION_LINE" | sed -n 's/^shelf \([^ ][^ ]*\) (\([^,)]*\).*/\1/p')
  BIN_COMMIT=$(printf '%s\n' "$BIN_VERSION_LINE" | sed -n 's/^shelf \([^ ][^ ]*\) (\([^,)]*\).*/\2/p')
  [ -n "$BIN_VERSION" ] && [ -n "$BIN_COMMIT" ] \
    || die "could not parse a version out of \`$BIN --version\`: $BIN_VERSION_LINE"
}

stop_server() {
  [ -n "$server_pid" ] || return 0
  kill "$server_pid" 2>/dev/null || true
  # SIGTERM must be enough: arch §6.3 step 7 is a graceful shutdown within
  # server.shutdown_grace. Needing SIGKILL here is itself a failure.
  for _ in $(seq 1 100); do
    kill -0 "$server_pid" 2>/dev/null || { server_pid=""; break; }
    sleep 0.1
  done
  if [ -n "$server_pid" ]; then
    bad "the server did not exit within 10 s of SIGTERM"
    kill -9 "$server_pid" 2>/dev/null || true
    server_pid=""
  fi
  # Our child is gone — but that is not the same as "the port is quiet", and the
  # difference is what ruling E-22 was written about. Step 10 stops the server,
  # deletes index.db and asserts the book is 404; if a *foreign* server is bound
  # to this port, our own binary never got it, the index we deleted belonged to
  # nobody, and every assertion from here on grades a different process against a
  # different database. On 2026-07-29 that produced exactly one FAIL — "expected
  # 404 for a book with no index row, got 200" — and eleven meaningless PASSes.
  # Proving the port went silent is the cheapest place to catch it.
  local deadline
  deadline=$(( $(date +%s) + 5 ))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    port_is_live || return 0
    sleep 0.1
  done
  bad "something is STILL holding port $port 5 s after our server exited:
        a foreign process owns it, so this run has been grading it, not us.
        $(port_listeners)
        Find it with:  ss -ltnp | grep :$port    /    pgrep -af 'shelf --config'"
  return 0
}

# Step 11 is `pnpm e2e`: a pnpm → node → chromium tree that this script does not
# otherwise track. Signalling the script used to orphan the whole thing — a
# Playwright run from a killed round was found still burning CPU 9.5 minutes
# after its orchestrator died, writing into the log of a run that no longer
# existed. The job is therefore started in its own process group (setsid, or
# job control if setsid is missing) so that one kill reaches all of it.
stop_playwright() {
  [ -n "$pw_pid" ] || return 0
  local target="$pw_pid"
  [ -n "$pw_pgid" ] && target="-$pw_pgid"
  kill -TERM "$target" 2>/dev/null || true
  for _ in $(seq 1 50); do
    kill -0 "$target" 2>/dev/null || { pw_pid=""; pw_pgid=""; return 0; }
    sleep 0.1
  done
  kill -KILL "$target" 2>/dev/null || true
  pw_pid=""
  pw_pgid=""
}

cleanup() {
  # Idempotent: the EXIT trap fires after a signal handler has already run it,
  # and `rm -rf "$STATE"` twice is only harmless by accident.
  [ "$cleanup_ran" -eq 0 ] || return 0
  cleanup_ran=1
  stop_playwright
  stop_server
  if [ "$keep" -eq 1 ]; then
    printf '\nkept: %s\n      %s\n' "$STATE" "$CONFIG"
  else
    rm -rf "$STATE"
    # `.bak` too (amendment A-11): `POST /api/roots` takes one beside the file
    # it edits. The real round never writes — the gate is off — but a config
    # that is inside the repository must not depend on that for the tree to
    # come back clean.
    rm -f "$CONFIG" "$CONFIG.bak"
  fi
}

# A signal handler that only cleans up is a trap: bash resumes the script at the
# statement after the handler returns. A SIGTERM used to run cleanup — deleting
# $STATE and, with the default keep=0, $CONFIG — and then let the run carry on
# for four more steps against a state directory that no longer existed, and
# exit 0. A signalled E2E round must never be able to report success. Clean up,
# then leave with the conventional 128+signo.
on_signal() {
  local name="$1" status="$2"
  trap - EXIT HUP INT TERM
  printf '\n\033[31me2e.sh: caught SIG%s — cleaning up and aborting\033[0m\n' "$name" >&2
  cleanup
  exit "$status"
}
trap cleanup EXIT
trap 'on_signal HUP 129'  HUP
trap 'on_signal INT 130'  INT
trap 'on_signal TERM 143' TERM

# Guard 3a — ownership, and the one that must pass. Guards 1–3 all lose to a
# *freshly started* foreign server: it appears after the pre-flight, our child
# is still alive when the poll loop asks (guard 2 only proves aliveness, never
# ownership), and its uptime_ms is small enough to sail through guard 3. Ask the
# kernel instead of the process: the pid holding the listening socket either is
# our child or the run is void. "Could not determine the owner" is never allowed
# to read as "the owner is us".
assert_port_owner() {
  local pids n exe want
  if ! have ss; then
    bad "cannot prove who owns port $port: ss(8) is not installed, so the authoritative
        identity guard of ruling E-22 did not run. Install iproute2. This round's
        results are only as good as the uptime_ms heuristic below."
    return 0
  fi
  pids=$(port_owner_pids)
  if [ -z "$pids" ]; then
    if [ -z "$(port_listeners)" ]; then
      die "$BASE answered GET /api/health, but no process is listening on port $port.
  Something is answering for a socket we cannot see; refusing to grade a run against it."
    fi
    die "port $port is held by a process whose pid the kernel will not show us — it belongs
  to another user, so it is certainly not our child $server_pid. This run is void.
  $(port_listeners)"
  fi
  n=$(printf '%s\n' "$pids" | wc -l)
  if [ "$n" -ne 1 ] || [ "$pids" != "$server_pid" ]; then
    die "port $port is owned by pid(s) $(printf '%s' "$pids" | tr '\n' ' '), not by our server
  (pid $server_pid): that is a FOREIGN server and every assertion from here would grade it
  instead of us (ruling E-22).
  $(port_listeners)
  Kill it, or re-run with --port N."
  fi
  # Same question asked of the process rather than the socket: the pid that owns
  # the port must be running the binary this round built. A build that replaced
  # dist/shelf underneath a live server shows up as "… (deleted)"; the path is
  # still the one we launched, so it is stripped rather than treated as a miss.
  exe=$(readlink "/proc/$server_pid/exe" 2>/dev/null || true)
  exe=${exe% (deleted)}
  want=$(readlink -f "$BIN" 2>/dev/null || printf '%s' "$BIN")
  if [ -z "$exe" ]; then
    bad "cannot read /proc/$server_pid/exe, so the binary behind port $port was not verified"
  elif [ "$exe" != "$want" ]; then
    die "the process on port $port is running $exe, not $want"
  fi
}

# The state directory this script wipes in step 10 must be the one the server
# actually opened. Step 10 deletes index.db* from its own variable and never
# asks the server — exactly the assumption that made the 2026-07-29 incident so
# hard to read. The server logs its data_dir at startup; compare.
assert_data_dir() {
  local line
  line=$(grep -a 'msg=serving' "$LOG" 2>/dev/null | tail -1)
  [ -n "$line" ] || die "the server never logged its \`serving\` line into $LOG, so the data_dir it
  opened could not be checked (SHELF_E2E_LOG_LEVEL must leave INFO enabled)"
  case "$line" in
    *"data_dir=$STATE/data"*|*"data_dir=\"$STATE/data\""*) return 0 ;;
  esac
  die "the server is using a different data_dir than this run wipes.
  we wipe:   $STATE/data
  it logged: $line"
}

start_server() {
  local spawned_ms up now_ms budget_ms health hver deadline

  [ -n "$BIN_VERSION" ] || read_bin_version

  # Guard 1 — refuse to start on top of somebody else. A health probe cannot
  # tell whose server answered it, so if the port is already live there is no
  # way to run this suite honestly: our binary would fail to bind, exit, and
  # every request below would be served by the squatter (ruling E-22). This
  # check must come BEFORE the spawn, because after it the two are
  # indistinguishable.
  if port_is_live; then
    die "something is already holding port $port — refusing to run against a foreign server.
  $(port_listeners)
  Find it with:  ss -ltnp | grep :$port    /    pgrep -af 'shelf --config'
  Then kill it, or re-run with --port N."
  fi

  # TMPDIR is a private, empty directory inside the scratch tree. os.TempDir()
  # re-reads it on every call, so every temp file the product could create lands
  # somewhere step 9 can walk exhaustively — instead of somewhere under a shared
  # /tmp full of other processes' work, where the only affordable check is a
  # name filter that proves nothing.
  spawned_ms=$(date +%s%3N)
  TMPDIR="$SRVTMP" "$BIN" --config "$CONFIG" --log-level "${SHELF_E2E_LOG_LEVEL:-info}" >>"$LOG" 2>&1 &
  server_pid=$!
  deadline=$(( $(date +%s) + START_TIMEOUT ))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    # Guard 2 — liveness BEFORE health. Reversed, a child that died on
    # "address already in use" is never noticed: the loop's first act is a
    # health probe, the squatter answers it instantly, and start_server returns
    # success having started nothing. Asking "is our child alive?" first costs
    # one signal and makes the failure loud.
    if ! kill -0 "$server_pid" 2>/dev/null; then
      tail -30 "$LOG" >&2
      die "the server exited during start-up (port $port already in use?)"
    fi
    if curl -fsS -m "$CURL_TIMEOUT" "$BASE/api/health" >/dev/null 2>&1; then
      # Guard 3a — ownership. THE identity check: everything below is a
      # cross-check that costs nothing, but this is the one that must pass.
      assert_port_owner

      # Guard 3 — age. Nearly free, and it still covers the machine where
      # ss(8) is missing: a process we started moments ago cannot have been up
      # longer than we have been waiting for it. /api/health carries uptime_ms
      # for exactly this kind of question (arch §7.4). The 2026-07-29 squatter
      # had been up 22 minutes.
      health=$(curl -fsS -m "$CURL_TIMEOUT" "$BASE/api/health") || health=""
      up=$(printf '%s' "$health" | jqf 'd["uptime_ms"]' 2>/dev/null) || up=""
      now_ms=$(date +%s%3N)
      budget_ms=$(( now_ms - spawned_ms + 5000 ))
      case "$up" in
        ''|*[!0-9]*)
          die "GET $BASE/api/health did not report an integer uptime_ms (got '${up}')" ;;
      esac
      if [ "$up" -gt "$budget_ms" ]; then
        die "$BASE answered, but it has been up for ${up} ms and we spawned our server
  $(( now_ms - spawned_ms )) ms ago: that is a FOREIGN server, not ours.
  Find it with:  ss -ltnp | grep :$port    /    pgrep -af 'shelf --config'"
      fi

      # The build half of the same question: --no-build will happily launch any
      # dist/shelf, and the version string was printed but never asserted.
      hver=$(printf '%s' "$health" | jqf 'str(d["version"])+" "+str(d["commit"])' 2>/dev/null) || hver=""
      if [ "$hver" != "$BIN_VERSION $BIN_COMMIT" ]; then
        die "the server on port $port reports version '$hver', but $BIN is
  '$BIN_VERSION $BIN_COMMIT'. The binary answering is not the binary this round checked."
      fi

      assert_data_dir
      return 0
    fi
    sleep 0.2
  done
  tail -30 "$LOG" >&2
  die "the server never answered GET /api/health within ${START_TIMEOUT}s"
}

jqf() { python3 -c 'import json,sys;d=json.load(sys.stdin);print(eval(sys.argv[1],{},{"d":d}))' "$1"; }

# wait_for_idle reports how long the scan took in the global WAIT_SECS and
# returns 0/1. It deliberately does NOT echo the figure: a caller writing
# `secs=$(wait_for_idle 180)` would run this in a subshell, where the `bad` on
# timeout increments a copy of `failures` that is thrown away and prints its
# FAIL line into the caller's variable. The whole run would then report
# E2E PASSED having skipped everything guarded by that `if`. Call it as a bare
# statement — `if wait_for_idle 180; then ... fi` — and read WAIT_SECS.
WAIT_SECS=0
wait_for_idle() {
  local limit="$1" started state
  started=$(date +%s)
  WAIT_SECS=0
  while :; do
    state=$(curl -fsS "$BASE/api/scan/status" | jqf 'd["state"]') || state="?"
    WAIT_SECS=$(( $(date +%s) - started ))
    [ "$state" = "idle" ] && return 0
    if [ "$WAIT_SECS" -gt "$limit" ]; then
      bad "the scan did not finish within ${limit}s (state=$state)"
      return 1
    fi
    sleep 1
  done
}

# run_playwright runs step 11's browser suite and returns its exit status.
#
# Two things it does that a bare `( cd web && pnpm e2e )` did not:
#
#   * it puts the job in its own process group, so cleanup can take down the
#     whole pnpm → node → chromium tree with one kill instead of orphaning it;
#   * it runs the job in the background and `wait`s, because bash defers a
#     trapped signal until the current FOREGROUND command finishes — a SIGTERM
#     arriving during a 10-minute browser run would have sat unhandled for the
#     rest of it. `wait` is interruptible; the trap fires immediately.
#
# It is called as a bare statement, never in a subshell: `pw_pid` has to be
# visible to cleanup running in the same shell (see wait_for_idle above for the
# other half of this lesson).
#
# `SHELF_E2E_MODE` is the same `--mode` the curl tier has taken since it was
# written (step 7 below): synthetic mode's `scan.include_globs` carries the two
# D-49 extras, so the *set* of series the browser must find differs by mode.
# `web/e2e/shelf.ts` reads it into `EXPECTED_SERIES` and asserts names, not a
# count — which is what keeps this signal from being an unchecked coupling: a
# mode that disagrees with the server under test fails by naming the series it
# expected and did not get.
#
# `SHELF_E2E_MODE` carries a second expectation now (amendment A-11): the two
# rounds differ in `server.allow_root_editing`, so the mode also says whether
# the 추가/제거 controls must be there. That coupling is checked in the same
# way and in the same place — 06-settings.spec.ts asserts
# `Settings.server.root_editing_enabled` equals what the mode implies before it
# counts a single button, so a config that stopped matching its round fails by
# name rather than turning `toHaveCount(0)` into a tautology.
#
# `SHELF_E2E_A11_ROOT` is the name of the root step 8b added and step 10b saw
# adopted; `SHELF_E2E_UI_ROOT_DIR` is a directory the browser tier may create
# its own throwaway roots under (one per viewport project). Both are only
# meaningful in the synthetic round, and 08-roots.spec.ts runs there only.
run_playwright() {
  local rc=0 self_pgid
  pw_pid=""
  pw_pgid=""
  if have setsid; then
    ( cd web && exec setsid env PLAYWRIGHT_BASE_URL="$BASE" SHELF_E2E_MODE="$mode" \
        SHELF_E2E_A11_ROOT="$A11_LABEL" SHELF_E2E_UI_ROOT_DIR="$A11_UI_ROOT" \
        PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1 pnpm e2e ) &
  else
    # No setsid: bash's job control puts a background job in its own group.
    set -m
    ( cd web && exec env PLAYWRIGHT_BASE_URL="$BASE" SHELF_E2E_MODE="$mode" \
        SHELF_E2E_A11_ROOT="$A11_LABEL" SHELF_E2E_UI_ROOT_DIR="$A11_UI_ROOT" \
        PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1 pnpm e2e ) &
    set +m
  fi
  pw_pid=$!
  pw_pgid=$(ps -o pgid= -p "$pw_pid" 2>/dev/null | tr -d ' ')
  self_pgid=$(ps -o pgid= -p $$ 2>/dev/null | tr -d ' ')
  # Never hand `kill -- -PGID` our own group: that would take out this script,
  # and make(1) with it. An unisolated job is killed by pid and said so about.
  if [ -z "$pw_pgid" ] || [ "$pw_pgid" = "$self_pgid" ]; then
    printf '  note  could not isolate the Playwright job in its own process group;\n'
    printf '        an interrupted run may leave browser processes behind\n'
    pw_pgid=""
  fi
  wait "$pw_pid"
  rc=$?
  pw_pid=""
  pw_pgid=""
  return "$rc"
}

# ---------------------------------------------------------------------------
step "1 · build"
# ---------------------------------------------------------------------------
if [ "$do_build" -eq 1 ]; then
  make build >/dev/null || die "make build failed"
fi
[ -x "$BIN" ] || die "$BIN does not exist; run without --no-build"
read_bin_version
ok "$BIN_VERSION_LINE"
# --no-build launches whatever happens to be in dist/, and a stale binary is
# indistinguishable from a fresh one once it is serving. start_server asserts
# that the process on the port reports this exact version and commit; here we
# assert that this binary is not older than the sources it claims to be built
# from. With a build in step 1 that is true by construction.
if [ "$do_build" -eq 0 ]; then
  # Only what the compiler actually consumes: tests and testdata move without
  # changing the binary, and a guard that fires on a good run is worse than no
  # guard at all.
  stale=$(find cmd internal web/dist go.mod go.sum \
      \( -name '*_test.go' -o -path '*/testdata/*' \) -prune -o \
      -type f \( -name '*.go' -o -name 'go.mod' -o -name 'go.sum' -o -path 'web/dist/*' \) \
      -newer "$BIN" -print 2>/dev/null | head -5)
  if [ -z "$stale" ]; then
    ok "--no-build: $BIN is newer than every source it embeds"
  else
    bad "--no-build, but $BIN predates the sources it should have been built from:"
    printf '        %s\n' $stale
  fi
fi

# ---------------------------------------------------------------------------
step "2 · configuration"
# ---------------------------------------------------------------------------
mkdir -p "$STATE/data" "$STATE/cache" "$SRVTMP"
: >"$LOG"

if [ "$synthetic" -eq 1 ]; then
  mode=synthetic
  ROOT="$FIXTURE"
  env "${GOENV[@]}" CGO_ENABLED=0 go run ./scripts/mkfixture -out "$FIXTURE" >/dev/null \
    || die "building the synthetic fixture tree failed"
  ok "synthetic tree: $(du -sh "$FIXTURE" | cut -f1) in $FIXTURE"
  SHELF_E2E_STATE="$STATE" SHELF_E2E_PORT="$port" \
    scripts/e2e-config.sh --synthetic --root "$ROOT" >"$CONFIG" || die "emitting the config failed"
else
  mode=real
  ROOT="$REAL_ROOT"
  [ -d "$ROOT" ] || die "the media root is not mounted: $ROOT
  (run scripts/e2e.sh --synthetic for the hermetic version)"
  SHELF_E2E_STATE="$STATE" SHELF_E2E_PORT="$port" \
    scripts/e2e-config.sh --root "$ROOT" >"$CONFIG" || die "emitting the config failed"
  ok "root: $ROOT (read-only; nothing is copied — A-3 include_globs)"
fi
ok "config: $CONFIG   state: $STATE"

# ---------------------------------------------------------------------------
step "3 · FR-CFG-005 marker"
# ---------------------------------------------------------------------------
# A marker file rather than a timestamp string: `find -newer` compares mtimes
# with full filesystem resolution, and a second-granularity `-newermt` would
# miss a write that happened inside the same second we started.
MARKER="$STATE/marker"
touch "$MARKER"
sleep 1.1
ok "marker laid down at $(date -r "$MARKER" +%H:%M:%S)"

# ---------------------------------------------------------------------------
step "4 · start the server"
# ---------------------------------------------------------------------------
start_server
ok "listening on $BASE (pid $server_pid)"

# ---------------------------------------------------------------------------
step "5 · NFR-OPS-006 — the library answers before any scan"
# ---------------------------------------------------------------------------
state=$(curl -fsS "$BASE/api/scan/status" | jqf 'd["state"]')
[ "$state" = "idle" ] && ok "no scan is running (scan.on_start: false)" \
                      || bad "expected an idle scanner, got $state"
code=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/api/series")
[ "$code" = "200" ] && ok "GET /api/series answers 200 from the existing index" \
                    || bad "GET /api/series returned $code before the first scan"

# ---------------------------------------------------------------------------
step "6 · full scan"
# ---------------------------------------------------------------------------
run_id=$(curl -fsS -X POST -H 'Content-Type: application/json' -d '{"full":true}' \
  "$BASE/api/scan" | jqf 'd["run_id"]') || die "POST /api/scan failed"
ok "run $run_id started"
if wait_for_idle 180; then
  ok "the full scan finished in ${WAIT_SECS}s"
  curl -fsS "$BASE/api/scan/status" | python3 -m json.tool | sed 's/^/        /'
fi

# ---------------------------------------------------------------------------
step "7 · curl assertions (impl-plan §6.3 step 5)"
# ---------------------------------------------------------------------------
if ! python3 scripts/e2e-assert.py --base "$BASE" --mode "$mode"; then
  failures=$((failures + 1))
fi

# ---------------------------------------------------------------------------
step "8 · NFR-PRF-004 — an immediate rescan is incremental"
# ---------------------------------------------------------------------------
# Open item H (HANDOFF §5.6.1 H, §5.7). What used to be here was:
#
#   curl -fsS -X POST ... "$BASE/api/scan" >/dev/null
#   if wait_for_idle 120; then
#     if [ "$WAIT_SECS" -lt 30 ]; then ok "...finished in ${WAIT_SECS}s"
#
# and it reported PASS in two states where no rescan ran. `set -e` is off (L42),
# so the POST's exit status was discarded — a 500 printed `curl: (22)` to the
# terminal and the step still passed. And `wait_for_idle` returns the moment the
# scanner is idle, which it already was, so the 30 s budget was measured against
# a scan that never started: `WAIT_SECS` was 0 in both cases. Both were
# reproduced against a stub server before this was replaced.
#
# The assertion now lives with the other server-facing checks and watches run
# identity rather than wall clock — see phase_rescan in scripts/e2e-assert.py,
# and scripts/e2e-assert-rescan-test.py for the tests that hold it to that.
if ! python3 scripts/e2e-assert.py --base "$BASE" --mode "$mode" --phase rescan; then
  failures=$((failures + 1))
fi

# ---------------------------------------------------------------------------
step "8b · A-12 — POST /api/roots writes the file AND opens the root"
# ---------------------------------------------------------------------------
# Deliberately placed BEFORE step 9. Everything this step writes goes to $STATE
# in /tmp, and step 9 is the check that proves it: if the configuration ever
# moves back into the repository, or the writer ever puts its `.bak` somewhere
# else, the next step fails the run by name instead of this one quietly getting
# away with it.
if [ "$synthetic" -eq 1 ]; then
  if ! python3 scripts/e2e-assert.py --base "$BASE" --mode "$mode" --phase roots-pre \
      --new-root "$A11_ROOT" --label "$A11_LABEL" --state-file "$A11_STATE"; then
    failures=$((failures + 1))
  fi
else
  echo "  skip  the real round keeps server.allow_root_editing off — the shipped default,"
  echo "        and the round that must not write to test/shelf.e2e.yaml in the repository."
  echo "        Step 7 asserted the gate is shut AND that POST /api/roots refuses with"
  echo "        403 disabled, so the browser tier's 'no 추가/제거 control' has a reason."
fi

# ---------------------------------------------------------------------------
step "9 · FR-CFG-005 / AC-001 — the media volume is untouched"
# ---------------------------------------------------------------------------
# The read half of AC-001 has already happened: step 7 streamed pages out of
# every shape, including an arbitrary jump into the 1 540-page volume.
touched=$(find "$ROOT" -newer "$MARKER" 2>/dev/null | head -20)
if [ -z "$touched" ]; then
  ok "nothing under the media root was created or modified"
else
  bad "files under the media root changed:"
  printf '        %s\n' $touched
fi
# impl-plan §6.3 step 7: `find "$TMPDIR" -newer marker` contains nothing
# belonging to shelf. The server's TMPDIR is $SRVTMP and nothing else may write
# there, so the walk needs no name filter and no depth limit — every entry is a
# violation. $repo is the `$PWD` half; its build outputs and node_modules are
# pruned because they are not the product's doing.
outside=$(
  {
    find "$SRVTMP" -mindepth 1 -newer "$MARKER" 2>/dev/null
    find "$repo" -mindepth 1 \
      \( -path "$repo/.git" -o -path "$repo/dist" -o -path "$repo/web/node_modules" \
         -o -path "$repo/web/dist" -o -path "$repo/.e2e" \) -prune -o \
      -newer "$MARKER" -print 2>/dev/null
  } | head -20
)
if [ -z "$outside" ]; then
  ok "nothing was written to the server's TMPDIR or the working directory"
else
  bad "files were created outside data_dir/cache_dir:"
  printf '        %s\n' $outside
fi

# A backstop for a hardcoded path that ignores TMPDIR altogether. Names only, so
# it is weaker than the walk above by construction; the other runs' scratch
# directories are excluded because they belong to other processes, not to this
# server.
strays=$(find "${TMPDIR:-/tmp}" -maxdepth 2 -newer "$MARKER" \
  \( -name '*shelf*' -o -name '*pdfium*' -o -name '*wazero*' -o -name '*thumb*' \) 2>/dev/null \
  | grep -v -e "^$STATE" -e '/shelf-e2e-' -e '/shelf-integration-' | head -10)
if [ -z "$strays" ]; then
  ok "no shelf-named stray under ${TMPDIR:-/tmp}"
else
  bad "shelf-named files appeared under ${TMPDIR:-/tmp}:"
  printf '        %s\n' $strays
fi

# ---------------------------------------------------------------------------
step "10 · AC-005 / AC-006 — delete the index and the cache, restart"
# ---------------------------------------------------------------------------
# The library as it stands, name for name, so the rebuild below can be compared
# against it rather than merely printed. `limit` is capped at 200 by arch §7.5
# (internal/httpapi/series.go, `seriesLimitMax`); 200 outruns both modes' subset
# (ten series real, twelve synthetic — D-49 adds an encrypted ZIP and a ZIP64
# archive), and `sort=name` makes the two listings comparable line by line.
#
# `?limit=500` stood here, and 500 is not a number this endpoint accepts: every
# call returned 400, `curl -fsS` exited non-zero, python died on empty stdin and
# — `set -e` being off — the substitution quietly yielded "". Both listings were
# then "", `[ "" = "" ]` was true, and the check below announced "name for name"
# having read nothing at all. Hence the emptiness assertions: an empty-vs-empty
# comparison must never be able to pass. The truncation guard inside python is
# the same lesson one step further out — a subset that outgrew 200 would compare
# two equally truncated prefixes and agree.
series_names() {
  curl -fsS "$BASE/api/series?limit=200&sort=name" |
    python3 -c 'import json,sys
d = json.load(sys.stdin)
if len(d["items"]) != d["total"]:
    sys.exit("series listing truncated: %d of %d names (limit is capped at 200)"
             % (len(d["items"]), d["total"]))
print("\n".join(i["name"] for i in d["items"]))'
}
# `printf %s\\n ""` is one line and not zero, so an empty listing has to be
# special-cased or every count below is off by one — including the zero that the
# comparison depends on being detectable.
name_count() { [ -n "$1" ] || { printf '0\n'; return; }; printf '%s\n' "$1" | wc -l; }
before_names=$(series_names)
# Asserted, not assumed. This is the precondition the comparison at the end of
# the step rests on, so it fails here, by name, rather than turning into a
# comparison that agrees with itself.
[ -n "$before_names" ] || bad "AC-005 · could not read the library before the wipe: GET /api/series returned no usable names, so the rebuild below has nothing to be held against"

# Record something only the user could have authored, on a real book.
read -r probe_book probe_cv probe_series <<<"$(
  curl -fsS "$BASE/api/series?limit=1&sort=name" |
    jqf 'd["items"][0]["id"]' |
    xargs -I{} curl -fsS "$BASE/api/series/{}" |
    python3 -c 'import json,sys
d=json.load(sys.stdin)
b=next(b for b in d["books"] if b["status"]=="ok")
print(b["id"], b["cv"], d["id"])'
)"
curl -fsS -X PUT -H 'Content-Type: application/json' -d '{"page":2}' \
  "$BASE/api/books/$probe_book/progress" >/dev/null || bad "PUT progress failed"
curl -fsS -X PUT -H 'Content-Type: application/json' -d '{"reading_direction":"rtl"}' \
  "$BASE/api/books/$probe_book/prefs" >/dev/null || bad "PUT prefs failed"
curl -fsS -X PUT -H 'Content-Type: application/json' -d '{"theme":"dark"}' \
  "$BASE/api/settings" >/dev/null || bad "PUT settings failed"
ok "recorded progress, a per-book preference and a setting on $probe_book"

stop_server
rm -f "$STATE"/data/index.db "$STATE"/data/index.db-wal "$STATE"/data/index.db-shm
rm -rf "$STATE/cache"
ok "deleted index.db* and the whole cache directory"
[ -f "$STATE/data/user.db" ] && ok "user.db is still on disk" || bad "user.db went missing"

start_server
ok "restarted"

# Settings need no index row and are readable immediately.
got_theme=$(curl -fsS "$BASE/api/settings" | jqf 'd["theme"]')
[ "$got_theme" = "dark" ] && ok "the user setting survived the wipe" \
                          || bad "theme is $got_theme after the wipe, want dark"
# The book is gone until the scan puts it back: /api/books/{bid} resolves an id
# through the index, and the index is what was deleted.
gone=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/api/books/$probe_book")
[ "$gone" = "404" ] && ok "with index.db deleted the book is 404, as it must be" \
                    || bad "expected 404 for a book with no index row, got $gone"

# `|| die` for the same reason step 8 grew a whole phase (open item H): `set -e`
# is off, so without it a POST that answered 500 was discarded. Unlike step 8
# this step never reported a false green — the AC-005 name comparison below
# cannot pass against the index this step just deleted, so a scan that never ran
# already failed the run. What it could not do was say WHY: the failure arrived
# as "nothing to compare" several assertions later. Now it fails by name, here.
curl -fsS -X POST -H 'Content-Type: application/json' -d '{"full":true}' \
  "$BASE/api/scan" >/dev/null || die "POST /api/scan failed (step 10 rebuild)"
if wait_for_idle 180; then
  total=$(curl -fsS "$BASE/api/series?limit=1" | jqf 'd["total"]')
  # Compared, not printed. `ok "…rebuilt to $total series"` stood here and was
  # unconditional: `$total` was never held against anything, so a rescan that
  # silently dropped a series announced the smaller number and passed. This is
  # the only place that can catch it — step 7's curl tier ran against the index
  # this step has just deleted, and of the ten curated names only six are
  # referenced anywhere in web/e2e/.
  after_names=$(series_names)
  before_n=$(name_count "$before_names")
  after_n=$(name_count "$after_names")
  if [ "$before_n" -eq 0 ] || [ "$after_n" -eq 0 ]; then
    # Two listings that could not be read are equal, and that equality means
    # nothing. It has to be its own branch: folding it into the `=` below is how
    # this check spent a whole round passing without ever seeing a series name.
    bad "AC-005 · nothing to compare: $before_n names before the wipe, $after_n after (see the curl/python error above)"
  elif [ "$after_names" = "$before_names" ]; then
    ok "AC-005 · the library rebuilt to the same $total series, name for name ($after_n names read back)"
  else
    bad "AC-005 · the rebuilt library is not the library that was deleted ($before_n names before, $after_n after, total=$total):"
    diff <(printf '%s\n' "$before_names") <(printf '%s\n' "$after_names") |
      head -20 | sed 's/^/        /'
  fi
  # The same book id must come back: identity is derived from the config and
  # the filesystem only (FR-CFG-004), which is the entire mechanism by which
  # the authored data below reattaches itself.
  again=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/api/books/$probe_book")
  [ "$again" = "200" ] && ok "FR-CFG-004 · the rebuilt index reproduced the same book id" \
                       || bad "the book id changed across a rebuild (GET returned $again)"

  # There is no GET on /progress: the contract carries it inside the book
  # (arch §7.6), which is also where the UI reads it.
  got_page=$(curl -fsS "$BASE/api/books/$probe_book" | jqf '(d["progress"] or {}).get("last_page")')
  [ "$got_page" = "2" ] && ok "AC-006 · reading progress survived (last_page=$got_page)" \
                        || bad "AC-006 · last_page is $got_page after the rebuild, want 2"
  got_dir=$(curl -fsS "$BASE/api/books/$probe_book/prefs" | jqf 'd["reading_direction"]')
  [ "$got_dir" = "rtl" ] && ok "the per-book reading direction survived" \
                         || bad "reading_direction is $got_dir after the rebuild, want rtl"

  cover_code=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/api/series/$probe_series/cover?w=240")
  for _ in $(seq 1 15); do
    [ "$cover_code" = "202" ] || break
    sleep 1
    cover_code=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/api/series/$probe_series/cover?w=240")
  done
  [ "$cover_code" = "200" ] && ok "AC-005 · covers regenerated after the cache was deleted" \
                            || bad "the cover did not regenerate (HTTP $cover_code)"
fi

# ---------------------------------------------------------------------------
step "10b · A-12 — the root added in step 8b survived the restart"
# ---------------------------------------------------------------------------
# This reuses the restart step 10 already performs rather than adding one, which
# is the point twice over. Under A-11 it was the same stop/start that AC-005 and
# AC-006 are proved across that "adopted at restart" had to survive. AMENDMENT
# A-12 (ruling E-40) moved adoption to the POST, so what this step grades now is
# that the addition is DURABLE — and step 10 is what gives that teeth, because
# it deletes the index database before restarting. A row that is live here
# cannot have been inherited from step 8b's scan; a process that failed to
# re-read the spliced entry would report the root `pending` instead.
if [ "$synthetic" -eq 1 ]; then
  if ! python3 scripts/e2e-assert.py --base "$BASE" --mode "$mode" --phase roots-post \
      --state-file "$A11_STATE"; then
    failures=$((failures + 1))
  fi
else
  echo "  skip  synthetic round only (see step 8b)"
fi

# ---------------------------------------------------------------------------
step "11 · Playwright"
# ---------------------------------------------------------------------------
if [ "$do_playwright" -eq 0 ]; then
  echo "  skip  --no-playwright (explicitly requested)"
elif ! compgen -G "web/e2e/*.spec.ts" >/dev/null; then
  # Not a skip. Rounds 1–3 of the acceptance run reported E2E PASSED while the
  # entire browser half of impl-plan §6.3 step 6 never executed, because this
  # branch printed "skip" and left `failures` alone. A missing suite is a
  # missing suite; `--no-playwright` above is the way to say so on purpose.
  bad "web/e2e/ carries no *.spec.ts — §6.3 step 6 (the twelve browser assertions) did not run"
else
  if run_playwright; then
    ok "§6.3 step 6 — the browser assertions passed at all four viewport widths"
  else
    bad "playwright: see web/playwright-report/"
  fi
  # §6.3 step 6.12 / §7.4: the screenshots are a deliverable, not a by-product —
  # and a deliverable is the shots THIS run produced, not the ones that happen to
  # be in the directory. `find docs/e2e-shots -name '*.png' | wc -l` stood here,
  # and on 2026-07-29 it printed `PASS 97 screenshots` for a run that wrote 81 of
  # them: the other 16 were left by the previous round, and the four code paths
  # that write them had aborted before the `shot()` call. That is HANDOFF §6.5
  # exactly — a check watching the thing next to what ships. $MARKER is laid down
  # in step 3, before the server starts, so every shot of this run is newer.
  fresh=$(find docs/e2e-shots -name '*.png' -newer "$MARKER" 2>/dev/null | wc -l)
  stale=$(find docs/e2e-shots -name '*.png' ! -newer "$MARKER" 2>/dev/null | wc -l)
  if [ "$fresh" -eq 0 ]; then
    bad "docs/e2e-shots/ carries nothing from this run — §7.4's screenshot deliverable was not produced"
  elif [ "$stale" -gt 0 ]; then
    bad "$stale of $((fresh + stale)) screenshots in docs/e2e-shots/ predate this run:"
    find docs/e2e-shots -name '*.png' ! -newer "$MARKER" 2>/dev/null | head -10 | sed 's/^/        /'
  else
    # `fresh` was only ever held against 0, which is a sign of life and not an
    # expectation: the 2026-07-29 run that wrote 81 of 97 was caught by `stale`
    # alone, and only because the 16 it skipped were still on disk from the round
    # before. Empty the directory first and that same run goes green. So hold the
    # run against the suite instead, name by name. The expectation is derived from
    # the source — no literal 97 to rot when a shot() is added or renamed — and
    # `shot()` writes `<name>-<project>.png`, so one match is enough to prove the
    # code path reached the call at least once.
    #
    # What no derivation of this kind can see is a shot nobody ever wrote: the
    # expectation and the calls are read out of the same file. That direction is
    # left open on purpose rather than pinned to a number no measurement backs.
    mapfile -t shot_names < <(
      grep -rhoE "shot\(page, info, '[^']+'\)" web/e2e/*.spec.ts | sed -E "s/^.*'([^']*)'\)\$/\1/"
    )
    shot_calls=$(grep -rhoF 'shot(page, info,' web/e2e/*.spec.ts | wc -l)
    if [ "$shot_calls" -eq 0 ] || [ "${#shot_names[@]}" -ne "$shot_calls" ]; then
      # The precondition, asserted rather than folded into the loop below: a
      # pattern that stopped matching would iterate zero times and pass.
      bad "could not read the screenshot names out of web/e2e/*.spec.ts (${#shot_names[@]} names for $shot_calls shot() calls) — the per-name check would have run on an empty list"
    else
      unshot=""
      for name in "${shot_names[@]}"; do
        [ -n "$(find docs/e2e-shots -name "$name-*.png" -newer "$MARKER" 2>/dev/null | head -1)" ] \
          || unshot="$unshot $name"
      done
      if [ -n "$unshot" ]; then
        bad "$fresh screenshots from this run, but these shot() calls produced none:"
        printf '        %s\n' $unshot
      else
        ok "$fresh screenshots in docs/e2e-shots/, every one from this run and every one of the $shot_calls shot() calls represented (review against docs/ui-shots/)"
      fi
    fi
  fi
fi

# ---------------------------------------------------------------------------
step "11b · A-11 — DELETE /api/roots/{name}"
# ---------------------------------------------------------------------------
# The added root has been empty until now (see A11_ROOT above), and a DELETE of
# an empty root would prove nothing about revision R1's index purge. Everything
# that reads the library *set* — the curl tier, step 10's AC-005 comparison and
# every browser spec — has finished by this point, so the root can be filled
# now: one archive, whose name is in `scan.include_globs` (it is one of the
# curated ten), copied out of the fixture tree so that a per-root rescan gives
# this root exactly one series to lose.
if [ "$synthetic" -eq 1 ]; then
  A11_FILL="$FIXTURE/[만화] 바퀴.zip"
  if [ ! -f "$A11_FILL" ]; then
    bad "A-11 · the fixture archive that fills the added root is missing: $A11_FILL
        (scripts/mkfixture builds it; if it was renamed, rename it here too — and in
        scripts/e2e-config.sh's CURATED, which is what puts it in scan.include_globs)"
  elif ! cp "$A11_FILL" "$A11_ROOT/"; then
    bad "A-11 · could not copy the fixture archive into $A11_ROOT"
  elif ! python3 scripts/e2e-assert.py --base "$BASE" --mode "$mode" --phase roots-delete \
      --state-file "$A11_STATE"; then
    failures=$((failures + 1))
  fi
else
  echo "  skip  synthetic round only (see step 8b)"
fi

# ---------------------------------------------------------------------------
step "summary"
# ---------------------------------------------------------------------------
stop_server
if [ "$failures" -eq 0 ]; then
  printf '\n\033[32mE2E PASSED\033[0m (%s subset)\n' "$mode"
  exit 0
fi
printf '\n\033[31mE2E FAILED\033[0m — %d failing step(s), %s subset\n' "$failures" "$mode"
printf 'server log: %s\n' "$LOG"
exit 1
