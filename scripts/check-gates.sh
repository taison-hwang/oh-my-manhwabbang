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
# It asserts five things:
#   1. every target named in GATE_TARGETS is invoked by the `gates` recipe;
#   2. each invocation carries its own `>> $(GATES_LOG)` record on the SAME
#      line, so the record cannot outlive the gate by a one-line deletion;
#   3. every target the recipe invokes through $(MAKE) is in GATE_TARGETS, or is
#      the pre-check (NOT_GATES) — so a target added to the recipe, or dropped
#      from the list above, is a failure rather than an unwatched invocation;
#   4. gate 5/5 is `e2e && test-int TEST_INT_REQUIRE_ROOT=1 && <record>` on one
#      line (ruling E-66 §2.1, §2.6): both real rounds, the integration suite
#      unable to skip its way to RAN, and one record that neither outlives;
#   5. GATES_TOTAL equals the number of RAN records in the recipe, each with a
#      distinct label. Since E-66 one gate (5/5) invokes two targets, so the
#      count is of records, not targets: a sixth gate without telling the banner
#      is still a failure rather than a silent under-count. Records are counted
#      where they occur, not by line, so two on one line are two;
#   6. every RAN record is reached only through `&&` from the gate before it:
#      each one directly follows `&&`, and no line carrying one has `||`. A
#      record in a `|| { …; exit 1; }` branch is written exactly when the gate
#      FAILS, and the banner never shows it — make stops first — but the log
#      holds a RAN for a gate that did not pass.
#
set -euo pipefail

cd "$(dirname "$0")/.."
MAKEFILE=Makefile

fail() { printf 'check-gates: %s\n' "$1" >&2; exit 1; }
ok()   { printf '  check-gates: %s\n' "$1"; }

[ -f "$MAKEFILE" ] || fail "no $MAKEFILE here"

# The targets behind the five gates of HANDOFF §6. `web-gates` stands for the
# four pnpm scripts, which `make lint` and `make test` deliberately do NOT run;
# `e2e` and `test-int` are both gate 5/5 (E-66).
GATE_TARGETS="lint test web-gates e2e-synthetic e2e test-int"
# Invoked by the recipe, and deliberately not a gate: it records nothing.
NOT_GATES="gates-precheck"

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
ok "all $found targets in GATE_TARGETS are invoked by 'gates', each recording itself on its own line"

# The other direction: nothing invoked that this script does not know about.
invoked=$(printf '%s\n' "$recipe" | grep -oE '\$\(MAKE\)([[:space:]]+-[^[:space:]]+)*[[:space:]]+[A-Za-z0-9_.-]+' \
	| awk '{print $NF}' || true)
for target in $invoked; do
	case " $GATE_TARGETS $NOT_GATES " in
	*" $target "*) ;;
	*) fail "the 'gates' recipe invokes '$target', which is not in GATE_TARGETS.
  Every invocation must be a gate this script checks, or listed in NOT_GATES.
  A target dropped from GATE_TARGETS stops being checked for its record." ;;
	esac
done
ok "every target 'gates' invokes is in GATE_TARGETS or NOT_GATES"

# Gate 5/5 (E-66): e2e, then test-int with the root required, then the record —
# one line, so that neither round can be deleted while the record stays.
five='\$\(MAKE\)[^|;&]*[[:space:]]e2e[[:space:]]*&&[[:space:]]*\$\(MAKE\)[^|;&]*[[:space:]]test-int[[:space:]]+([^|;&]*[[:space:]])?TEST_INT_REQUIRE_ROOT=1([[:space:]][^|;&]*)?&&[[:space:]]*echo[[:space:]]+'"'"'5/5[^'"'"']*RAN'"'"'[[:space:]]*>>[[:space:]]*'"'"'\$\(GATES_LOG\)'"'"
printf '%s\n' "$recipe" | grep -qE "$five" || fail "gate 5/5 is not '\$(MAKE) … e2e && \$(MAKE) … test-int TEST_INT_REQUIRE_ROOT=1 && echo '5/5 … RAN' >> '\$(GATES_LOG)'' on one line.
  E-66 §2.1: the real round and the integration sample are one gate, RAN only
  when both return 0. §2.6: without TEST_INT_REQUIRE_ROOT=1 a missing root makes
  the integration tests skip, go test exits 0, and the skip is recorded RAN."
ok "gate 5/5 runs e2e && test-int TEST_INT_REQUIRE_ROOT=1 and records once, on the same line"

# One output line per record OCCURRENCE (grep -o), so two records sharing a
# line are two. The pattern takes any quoted text ending in RAN appended to the
# log, echo or not, so a record written some other way is still counted.
rec_re="'[^']*RAN'[[:space:]]*>>[[:space:]]*'?\\\$\(GATES_LOG\)"
records=$(printf '%s\n' "$recipe" | grep -oE "$rec_re" || true)
nrec=$(printf '%s' "$records" | grep -c . || true)
labels=$(printf '%s\n' "$records" | grep -oE "^'[0-9]+/[0-9]+" | sort -u | grep -c . || true)
[ "$labels" = "$nrec" ] || fail "the 'gates' recipe has $nrec RAN records but $labels distinct gate labels.
  Two records under one label make the banner count one gate twice."

declared=$(grep -E '^GATES_TOTAL[[:space:]]*:?=' "$MAKEFILE" | head -1 | sed 's/.*=[[:space:]]*//' | tr -d '[:space:]')
[ -n "$declared" ] || fail "GATES_TOTAL is not declared"
[ "$declared" = "$nrec" ] || fail "GATES_TOTAL is $declared but the recipe records $nrec gates as RAN.
  The banner refuses to report when these disagree, so this is not a silent
  wrong answer — but it means 'make gates' cannot finish, which is worse to
  discover at the end of a full round than here."
ok "GATES_TOTAL ($declared) matches the number of RAN records"

# Rule 6: a record is reachable only through `&&` after its gate.
chained=$(printf '%s\n' "$recipe" | grep -oE "&&[[:space:]]*echo[[:space:]]+$rec_re" | grep -c . || true)
[ "$chained" = "$nrec" ] || fail "only $chained of the $nrec RAN records directly follow '&&'.
  A record after ';', '{', or anything but '&&' is written whether or not the
  gate before it returned 0."
orbranch=$(printf '%s\n' "$recipe" | grep -E "$rec_re" | grep -F '||' || true)
[ -z "$orbranch" ] || fail "a line that records RAN also has '||':
  $orbranch
  On that line a failing gate can still reach a record ('gate || { echo … RAN
  >> log; exit 1; }', or 'gate || true && echo … RAN'). The log would hold a
  RAN for a gate that did not pass, and stdout would never show it."
ok "every RAN record is reached only through '&&' from its gate"
