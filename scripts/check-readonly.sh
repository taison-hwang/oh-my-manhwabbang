#!/usr/bin/env bash
#
# check-readonly.sh — the four static guards `make lint` runs.
#
# Each of them protects an invariant that six parallel work packages could
# otherwise erode one convenient line at a time. None is a substitute for the
# tests that also cover them (integration I-9 walks the real collection and
# asserts nothing under it changed); they are here because a grep fails in two
# seconds on every commit and an integration run does not.
#
#   1. FR-CFG-005 / NFR-DAT-002 — no filesystem mutation primitive may appear in
#      a package that touches a media volume. SHELF opens, stats and reads; it
#      never creates, renames, deletes or re-timestamps anything under a root.
#
#   2. D-13 / NFR-DAT-001 — no transaction may span index.db and user.db. The
#      two files are joined by ATTACH on every index connection, so a write to
#      the attached `ud.` schema from an index statement would put authored data
#      inside a transaction that a rebuild is allowed to roll away.
#
#   3. Amendment A-8 (ruling E-9) — `series_seen` is write-once. `first_seen_at`
#      is what makes 최근 추가 survive `--rebuild-index`; an UPDATE, a DELETE or
#      an upsert's DO UPDATE would reset the collection to "added today", which
#      is the exact failure E-9 exists to prevent.
#
#   4. Amendment A-11 (ruling E-26) — only `internal/config` writes the
#      configuration file. E-26 bought a write path into `shelf.yaml` and
#      nothing else, and the whole safety argument for it lives in one file
#      (`internal/config/rootsfile.go`): the atomic rename, the preserved mode,
#      the `.bak`, and the refusal to touch a file it cannot parse. An HTTP
#      handler that reached for `os.WriteFile` itself would have none of that,
#      and would be reviewed as "one line" the day it was added.
#
# Usage: scripts/check-readonly.sh [repo-root]

set -euo pipefail

root="${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$root"

fail=0
note() { printf '  %s\n' "$*"; }

# ---------------------------------------------------------------------------
# 1. No write primitive in a media-reading package (FR-CFG-005, D-50)
# ---------------------------------------------------------------------------
media_pkgs=()
for d in internal/scanner internal/source internal/archive internal/openpool; do
  [ -d "$d" ] && media_pkgs+=("$d")
done

if [ ${#media_pkgs[@]} -eq 0 ]; then
  note "check-readonly: no media-reading packages exist yet, nothing to guard"
else
  # The pattern is the arch §11 list verbatim. Tests are included on purpose: a
  # fixture that writes into a root is exactly the accident this prevents.
  if hits=$(grep -rnE '\bos\.(Create|Remove|RemoveAll|Rename|Mkdir|MkdirAll|Chtimes|Chmod|Truncate|WriteFile|OpenFile)\b' \
      "${media_pkgs[@]}" 2>/dev/null); then
    echo "FR-CFG-005 violation: a filesystem mutation primitive appears in a media-reading package:"
    echo "$hits" | sed 's/^/  /'
    fail=1
  else
    note "check-readonly: no write primitives in ${media_pkgs[*]}"
  fi
fi

# ---------------------------------------------------------------------------
# 2 and 3. SQL literal guards
# ---------------------------------------------------------------------------
# Go SQL in this project is written in backtick literals, so the check works on
# whole literals rather than on lines: a multi-line statement whose INSERT and
# whose `ud.` reference sit five lines apart is precisely the shape a line-based
# grep cannot see.
sql_dirs=()
for d in internal/index internal/userdata internal/scanner; do
  [ -d "$d" ] && sql_dirs+=("$d")
done

if [ ${#sql_dirs[@]} -gt 0 ]; then
  violations=$(find "${sql_dirs[@]}" -name '*.go' -print0 | xargs -0 awk '
    # kw matches an SQL keyword as a whole word. Without the boundaries the
    # column `updated_at` — which every table in both databases has — reads as
    # the keyword UPDATE, and the guard fires on every SELECT that orders by it.
    function kw(text, word) {
      return (" " text " ") ~ ("[^A-Z_]" word "[^A-Z_]")
    }
    function flush(  upper) {
      if (lit == "") return
      upper = toupper(lit)
      writes = kw(upper, "INSERT") || kw(upper, "UPDATE") || kw(upper, "DELETE") ||
               kw(upper, "REPLACE") || kw(upper, "DROP") || kw(upper, "ALTER")

      # Guard 2: a write statement that also names the attached user database.
      if (writes && lit ~ /ud\./) {
        printf "%s:%d: cross-database write: a statement naming `ud.` also writes (D-13)\n", FILENAME, start
      }
      # Guard 3: series_seen is insert-only (amendment A-8). CREATE TABLE is
      # allowed — the migration rung is what makes the table exist.
      if (lit ~ /series_seen/ && (kw(upper, "UPDATE") || kw(upper, "DELETE"))) {
        printf "%s:%d: series_seen must be write-once: no UPDATE, DELETE or DO UPDATE (A-8, ruling E-9)\n", FILENAME, start
      }
      lit = ""
    }
    {
      line = $0
      while (length(line) > 0) {
        i = index(line, "`")
        if (i == 0) { if (inlit) lit = lit " " line; break }
        if (inlit) {
          lit = lit " " substr(line, 1, i - 1)
          inlit = 0
          flush()
        } else {
          inlit = 1
          start = FNR
          lit = ""
        }
        line = substr(line, i + 1)
      }
      if (inlit) lit = lit " "
    }
  ' || true)

  if [ -n "$violations" ]; then
    echo "SQL guard violation:"
    echo "$violations" | sed 's/^/  /'
    fail=1
  else
    note "check-readonly: SQL literals in ${sql_dirs[*]} respect the two-database and write-once rules"
  fi
fi

# ---------------------------------------------------------------------------
# 4. Only internal/config writes the configuration file (amendment A-11)
# ---------------------------------------------------------------------------
# Test files are excluded, and the exclusion is the difference between this
# guard and guard 1. Guard 1 includes tests on purpose, because a fixture that
# writes into a media root is exactly the accident it prevents. Here the
# opposite holds: `golden_test.go -update` has to write golden files and the
# harness has to build a fixture tree, so including tests would make the guard
# fire on its first run and be deleted the same afternoon. What it protects is
# the HTTP layer's *production* code, which must reach the file only through
# `internal/config`.
if [ -d internal/httpapi ]; then
  if hits=$(grep -rnE '\bos\.(Create|CreateTemp|Remove|RemoveAll|Rename|Mkdir|MkdirAll|Chtimes|Chmod|Truncate|WriteFile|OpenFile)\b' \
      internal/httpapi --include='*.go' --exclude='*_test.go' 2>/dev/null); then
    echo "A-11 violation: internal/httpapi writes to the filesystem directly."
    echo "  The HTTP layer must reach the configuration file only through internal/config,"
    echo "  which is where the atomic rename, the preserved mode and the .bak live."
    echo "$hits" | sed 's/^/  /'
    fail=1
  else
    note "check-readonly: internal/httpapi reaches the filesystem only through internal/config"
  fi
fi

if [ "$fail" -ne 0 ]; then
  exit 1
fi
echo "check-readonly: clean"
