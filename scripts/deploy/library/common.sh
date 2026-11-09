# common.sh — shared helpers for the books.betterax.net deployment scripts.
# Sourced, never run. Nothing here writes outside the paths a script is given.

LIB_HOST="books.betterax.net"
TUNNEL_NAME="gift4me-tunnel"
DEFAULT_PORT=9910
CF_CONFIG_DEFAULT="/etc/cloudflared/config.yml"

die() { printf 'error: %s\n' "$*" >&2; exit 2; }
say() { printf '>> %s\n' "$*"; }

# Result table. rec <PASS|FAIL|WARN|SKIP> <id> <what> [detail]
RESULTS=()
FAILS=0
rec() {
  local st="$1" id="$2" what="$3" detail="${4:-}"
  RESULTS+=("$st|$id|$what|$detail")
  [ "$st" = FAIL ] && FAILS=$((FAILS + 1))
  return 0
}
print_table() {
  local title="$1" line st id what detail
  printf '\n== %s ==\n' "$title"
  printf '%-5s %-5s %-58s %s\n' RESULT ID CHECK DETAIL
  for line in "${RESULTS[@]}"; do
    IFS='|' read -r st id what detail <<<"$line"
    printf '%-5s %-5s %-58s %s\n' "$st" "$id" "$what" "$detail"
  done
  local p f w s
  p=$(printf '%s\n' "${RESULTS[@]}" | grep -c '^PASS|' || true)
  f=$(printf '%s\n' "${RESULTS[@]}" | grep -c '^FAIL|' || true)
  w=$(printf '%s\n' "${RESULTS[@]}" | grep -c '^WARN|' || true)
  s=$(printf '%s\n' "${RESULTS[@]}" | grep -c '^SKIP|' || true)
  printf '\nPASS %s  FAIL %s  WARN %s  SKIP %s\n' "$p" "$f" "$w" "$s"
}

need() { command -v "$1" >/dev/null 2>&1 || die "$1 is required but not on PATH"; }

# yaml_get FILE DOTTED.KEY — prints the scalar (or JSON for lists/maps), empty if absent.
yaml_get() {
  python3 -I - "$1" "$2" <<'PY'
import json, sys
try:
    import yaml
except ImportError:
    sys.exit("python3 PyYAML is required to read the configuration")
doc = yaml.safe_load(open(sys.argv[1])) or {}
cur = doc
for k in sys.argv[2].split("."):
    if not isinstance(cur, dict) or k not in cur:
        sys.exit(0)
    cur = cur[k]
if cur is None:
    sys.exit(0)
print(cur if isinstance(cur, (str, int, float, bool)) else json.dumps(cur, ensure_ascii=False))
PY
}

utc_stamp() { date -u +%Y%m%dT%H%M%SZ; }
