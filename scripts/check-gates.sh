#!/usr/bin/env bash
#
# check-gates — the `gates` target really invokes every gate it reports on.
#
# Why this exists, and why it is not inside the Makefile
# -----------------------------------------------------
# `make gates` closes the hole that let `make e2e-synthetic` sit broken through
# three sessions: nothing depended on it, there is no CI, so running it was
# purely a matter of human memory (HANDOFF §4.3, §6.5). Its closing banner is
# built from a log each gate appends to only after it returns 0 — which reads as
# derived evidence, and up to a point it is: a gate that fails aborts the recipe
# before its record is written, and `GATES_TOTAL` refuses to print a result when
# the count does not add up.
#
# A review found the limit of that, and the limit matters. The record lives in
# the same recipe as the gate it certifies, so an edit to the recipe can remove
# the gate and keep the record. The first version did it on the next line, where
# deleting one line left the other; joining them with `&&` removes that accident
# but not the deliberate edit. **No bookkeeping a recipe does about itself can
# survive the recipe being rewritten** — claiming otherwise is the §6.5 mistake
# the banner was written to avoid, one level up.
#
# So the check is here, outside, reading the Makefile as an artefact: the same
# shape as check-readonly.sh and contractcheck, and for the same reason.
#
# It asserts three things:
#   1. every gate named in GATE_TARGETS is invoked by the `gates` recipe;
#   2. each invocation carries its own `>> $(GATES_LOG)` record on the SAME
#      line, so the record cannot outlive the gate by a one-line deletion;
#   3. GATES_TOTAL equals the number of gates found, so adding a sixth gate
#      without telling the banner is a failure rather than a silent under-count.
#
set -euo pipefail

cd "$(dirname "$0")/.."
MAKEFILE=Makefile

fail() { printf 'check-gates: %s\n' "$1" >&2; exit 1; }
ok()   { printf '  check-gates: %s\n' "$1"; }

[ -f "$MAKEFILE" ] || fail "no $MAKEFILE here"

# The five gates of HANDOFF §6. `web-gates` stands for the four pnpm scripts,
# which `make lint` and `make test` deliberately do NOT run.
GATE_TARGETS="lint test web-gates e2e-synthetic e2e"

# The recipe body: from `^gates:` to the next line that starts in column 0.
recipe=$(awk '/^gates:/{f=1;next} f&&/^[^\t]/{exit} f' "$MAKEFILE")
[ -n "$recipe" ] || fail "the 'gates' target has no recipe — was it renamed or removed?"

found=0
for target in $GATE_TARGETS; do
	# `$(MAKE) … <target>` followed on the SAME line by its log append.
	line=$(printf '%s\n' "$recipe" | grep -E "\\\$\(MAKE\)[^|;]*[[:space:]]$target([[:space:]]|\$)" || true)
	[ -n "$line" ] || fail "the 'gates' recipe never invokes '$target'.
  A gate the aggregate does not run does not go green — it says nothing at all,
  and the banner would read that silence as agreement. Either invoke it or drop
  it from GATE_TARGETS in this script, deliberately."

	printf '%s\n' "$line" | grep -q "GATES_LOG" || fail "'$target' is invoked without recording itself on the same line.
  Put '&& echo ... >> \$(GATES_LOG)' on the gate's own line. Split across two
  lines, deleting the gate leaves the record behind and the banner reports a
  green nobody measured."
	found=$((found + 1))
done
ok "all $found gates in GATE_TARGETS are invoked by 'gates', each recording itself on its own line"

declared=$(grep -E '^GATES_TOTAL[[:space:]]*:?=' "$MAKEFILE" | head -1 | sed 's/.*=[[:space:]]*//' | tr -d '[:space:]')
[ -n "$declared" ] || fail "GATES_TOTAL is not declared"
[ "$declared" = "$found" ] || fail "GATES_TOTAL is $declared but the recipe invokes $found gates.
  The banner refuses to report when these disagree, so this is not a silent
  wrong answer — but it means 'make gates' cannot finish, which is worse to
  discover at the end of a full round than here."
ok "GATES_TOTAL ($declared) matches the number of gates invoked"
