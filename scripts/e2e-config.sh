#!/usr/bin/env bash
#
# e2e-config.sh — render test/shelf.e2e.yaml.tmpl into a usable configuration.
#
# Separated from e2e.sh so the config can be produced and inspected on its own:
#
#   scripts/e2e-config.sh > /tmp/shelf.e2e.yaml         # the real curated subset
#   scripts/e2e-config.sh --synthetic --root /tmp/fx    # the hermetic twin
#
# Environment (all optional):
#   SHELF_E2E_ROOT       media root                 (default: the real collection)
#   SHELF_E2E_PORT       listen port                (default: 8791)
#   SHELF_E2E_STATE      data_dir/cache_dir parent  (default: a /tmp directory)
#   SHELF_E2E_BASE_PATH  server.base_path           (default: "")
#   SHELF_E2E_LOG_LEVEL  log.level                  (default: debug)

set -euo pipefail

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# The ten series of impl-plan §6.3, exact names. Each covers something no other
# entry does; see the table in that section for the mapping.
CURATED=(
  "Clover 클로버 (총4권)"                    # folder + 4 ZIPs, CP949 names
  "상처를 쫓는자 1-11 (완) 이케가미 료이치"  # folder of image sub-folders
  "자살도114-122"                            # loose images, mixed padding
  "바퀴.zip"                                 # single top-level ZIP
  "강철의 연금술사 1~27권 완결"              # N archives + one cover image
  "군계 1~25"                                # duplicates + 2 truncated
  "디엔엔젤 1-13권 연재중"                   # one 0-byte archive
  "미생 1~9 (완결 pdf)"                      # PDFs — AC-004
  "배틀로얄 1~15 [완결].zip"                 # 1 540 pages — AC-008
  "엔젤하트 전32권 완결.zip"                 # container of ZIPs — volumes (D-70)
  "비둘기.zip"                               # opens, holds no page and no volume — E-14
  "라제폰 1-3권 완결"                        # a folder of RARs — D-71
  "울프가이"                                 # ZIP and RAR volumes in one series
  "사모님은 학생회장.zip"                    # container mixing ZIP, RAR and .7z
  "펌프킨 시저스 1~13권"                     # one volume is an .hv3 — D-72
)

# The synthetic tree carries the same names plus three shapes the real
# collection has no sample of (D-49, extended by D-71).
SYNTHETIC_EXTRA=(
  "암호화 테스트.zip"
  "ZIP64 테스트.zip"
  "솔리드 테스트.rar"
)

synthetic=0
root="${SHELF_E2E_ROOT:-/mnt/big-data/pds/taison-data/02. books/01. mangga}"
while [ $# -gt 0 ]; do
  case "$1" in
    --synthetic) synthetic=1 ;;
    --root) root="$2"; shift ;;
    *) echo "e2e-config.sh: unknown argument $1" >&2; exit 2 ;;
  esac
  shift
done

port="${SHELF_E2E_PORT:-8791}"
state="${SHELF_E2E_STATE:-/tmp/shelf-e2e}"
base_path="${SHELF_E2E_BASE_PATH:-}"
log_level="${SHELF_E2E_LOG_LEVEL:-debug}"

# `server.allow_root_editing` (amendment A-11, ruling E-26) — off unless this is
# the synthetic round.
#
# It is derived from --synthetic rather than exposed as an environment override,
# and that is the safety property: turning it on for the real-collection round
# would point `POST /api/roots` at `test/shelf.e2e.yaml` *inside the repository*,
# which scripts/e2e.sh step 9 fails on. The synthetic round writes to a config
# under the run's /tmp state directory instead. See the key's comment in the
# template for the full reasoning.
allow_root_editing=false
if [ "$synthetic" -eq 1 ]; then
  allow_root_editing=true
fi

# `server.browse_bases` (amendment A-12, ruling E-40) — the allowlist behind
# `GET /api/browse`. Derived from --synthetic for the same safety reason as the
# key above, and empty by default because an empty allowlist refuses every path:
# a real-collection round must not offer a picker onto the host's filesystem, and
# its gate is shut anyway.
#
# The synthetic base is the run's state directory, so every path the picker can
# reach was created by this run — the fixture tree, the directory step 8b adds,
# and data_dir/cache_dir. It is emitted in flow style because it is one entry.
browse_bases="[]"
if [ "$synthetic" -eq 1 ]; then
  browse_bases="[\"$state\"]"
fi

globs=("${CURATED[@]}")
if [ "$synthetic" -eq 1 ]; then
  globs+=("${SYNTHETIC_EXTRA[@]}")
fi

# `[` and `]` open and close a path.Match character class. `[[]` is a class
# whose only member is `[`, so it matches a literal one; a bare `]` outside a
# class is already literal and is left alone. This is the escaping impl-plan
# §6.3 prescribes.
#
# The collection lost its `[만화] ` prefix in 2026-08, which took a bracket pair
# off every name — so exactly ONE curated entry still opens a class it does not
# mean to, `배틀로얄 1~15 [완결].zip`, and it is the one this function is still
# load-bearing for. It stays generic regardless: the escaping is a property of
# the pattern language, not of what today's ten names happen to contain, and a
# pattern that compiles but matches nothing indexes an empty library.
escape_glob() {
  printf '%s' "$1" | sed -e 's/\[/[[]/g' -e 's/\*/[*]/g' -e 's/?/[?]/g'
}

include_block=""
for g in "${globs[@]}"; do
  include_block+="    - \"$(escape_glob "$g")\"
"
done
include_block="${include_block%$'\n'}"

tmpl="$repo/test/shelf.e2e.yaml.tmpl"
[ -f "$tmpl" ] || { echo "e2e-config.sh: missing $tmpl" >&2; exit 1; }

# awk rather than sed for the multi-line block, and because every value here
# can contain a slash.
awk -v root="$root" -v port="$port" -v data="$state/data" -v cache="$state/cache" \
    -v base="$base_path" -v level="$log_level" -v globs="$include_block" \
    -v rootedit="$allow_root_editing" -v browse="$browse_bases" '
  {
    gsub(/@@ROOT@@/, root)
    gsub(/@@PORT@@/, port)
    gsub(/@@DATA_DIR@@/, data)
    gsub(/@@CACHE_DIR@@/, cache)
    gsub(/@@BASE_PATH@@/, base)
    gsub(/@@LOG_LEVEL@@/, level)
    gsub(/@@ALLOW_ROOT_EDITING@@/, rootedit)
    gsub(/@@BROWSE_BASES@@/, browse)
    if ($0 ~ /@@INCLUDE_GLOBS@@/) { print globs; next }
    print
  }
' "$tmpl"
