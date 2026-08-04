# SHELF — build entry points.
#
# Every `go` invocation below carries GOENV. This is not decoration: on the
# development machine a bare `go build` fails with
#     go: GOPROXY list is not the empty string, but contains no entries
# because GOPROXY resolves to empty in the operator's shell. GOTOOLCHAIN=auto is
# what lets an installed go1.21 fetch the go1.25.0 toolchain go.mod asks for.
# (decisions.md, "Also settled"; arch-backend.md §1.)
#
# CGO_ENABLED=0 is CON-001: one static binary, trivial cross-compilation. The
# `test` target is the single documented exception — see the comment there.

GO       ?= go
PNPM     ?= pnpm
GOENV    := GOPROXY=https://proxy.golang.org,direct GOSUMDB=sum.golang.org GOTOOLCHAIN=auto
BUILDENV := $(GOENV) CGO_ENABLED=0
PKG      := shelf
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE     := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS  := -s -w -X $(PKG)/internal/buildinfo.Version=$(VERSION) \
                  -X $(PKG)/internal/buildinfo.Commit=$(COMMIT) \
                  -X $(PKG)/internal/buildinfo.Date=$(DATE)
# The DEFAULT build is static (ruling E-21). `internal/thumbs` → gen2brain/avif →
# ebitengine/purego emits dynamic import directives regardless of CGO_ENABLED=0,
# so a default build links against libc.so.6/libdl.so.2/libpthread.so.0 and will
# not start on the musl and old-glibc NASes that NFR-OPS-003 makes the primary
# target. CON-001 asks for "정적 단일 바이너리", not merely for the flag.
# `docs/data-survey.md` finds zero .avif files in every sample taken (two passes:
# 500 ZIPs / ~56k entries), so the default loses nothing real; `make release`
# still ships an `-avif` variant.
# TAGS is space-separated (AVIF_TAGS uses $(filter-out)).
TAGS      ?= noavif

# $(filter-out) splits on whitespace, so a comma-separated TAGS would leave
# `noavif` in the `-avif` variant and silently ship two identical builds. `go
# build` accepts both spellings; this recipe only accepts one, loudly. (A literal
# comma cannot be written inside $(findstring), hence the variable.)
comma := ,
ifneq (,$(findstring $(comma),$(TAGS)))
$(error TAGS must be space-separated, not comma-separated (got "$(TAGS)"): \
  AVIF_TAGS uses $$(filter-out), which splits on whitespace, so a comma would \
  leave noavif in the -avif release variant. Use: make TAGS="noavif nopdf")
endif

# The opt-in variant: TAGS minus noavif. Dynamically linked on linux, by design.
AVIF_TAGS := $(filter-out noavif,$(TAGS))

# How ARTIFACTS.txt describes each variant. `-tags ""` is honest but reads as a
# mistake, so an empty tag set says so in words. Single quotes, because these
# expand inside a double-quoted shell word.
TAGS_DESC      := $(if $(strip $(TAGS)),-tags '$(strip $(TAGS))',no build tags)
AVIF_TAGS_DESC := $(if $(strip $(AVIF_TAGS)),-tags '$(strip $(AVIF_TAGS))',no build tags)

# The ELF-linkage gate of E-21, run against the artefact rather than the flags.
# SHELF_STATIC_ARTEFACTS and SHELF_AVIF_ARTEFACTS are space-separated path
# lists; see internal/buildinfo/staticlink_test.go.
#
# There is deliberately NO `-run` filter. It used to carry
# `-run '^TestDefaultArtefactIsStaticallyLinked$$'`, and a one-character typo in
# that pattern makes `go test` print "[no tests to run]" and exit **0** — so
# `make build-go` would report success with the gate never having executed.
# That silent self-skip is the exact failure mode E-21 exists to prevent. The
# package is a handful of tests that only read files off disk, so running all
# of them costs nothing and cannot be neutered by a rename.
# (internal/buildinfo/staticlink_test.go asserts the filter stays absent.)
STATICCHECK := $(GOENV) CGO_ENABLED=0 $(GO) test ./internal/buildinfo -count=1

RELEASE_TARGETS := linux/amd64 linux/arm64 linux/arm windows/amd64 windows/arm64 darwin/amd64 darwin/arm64

# Which artefacts the linkage gates assert on, derived from RELEASE_TARGETS by
# make itself rather than by a shell conditional inside the `release` loop.
#
# Per-target coverage is the point: the DT_NEEDED sets differ by architecture
# (linux/arm carries only libdl.so.2 where amd64 and arm64 carry three), so a
# gate narrowed to one arch would let the others ship dynamic. Deriving the list
# with $(filter linux/%,...) means "every linux target" is not a claim anyone has
# to maintain — and internal/buildinfo/staticlink_test.go expands these three
# variables through `make print-...` and checks the sets match.
LINUX_TARGETS    := $(filter linux/%,$(RELEASE_TARGETS))
STATIC_ARTEFACTS := $(foreach t,$(LINUX_TARGETS),dist/shelf-$(VERSION)-$(subst /,-,$(t)))
AVIF_ARTEFACTS   := $(foreach t,$(LINUX_TARGETS),dist/shelf-$(VERSION)-$(subst /,-,$(t))-avif)

# NFR-OPS-001's budget for the primary target, in bytes. `release` reports every
# artefact's size and fails if linux/amd64 exceeds it.
#
# 32 MiB, per ruling **E-19** (decisions.md, wave-4 E2E) as confirmed by **E-21**
# §4. The previous 20 MiB was
# impl-plan §7.3 rounding up arch §1.2's "18 MB is fine for a NAS" — an estimate
# whose base term (everything that is neither pdfium nor AVIF) was ~7 MB low,
# mostly modernc.org/sqlite, the pure-Go SQLite CON-001's CGO_ENABLED=0 forces.
# prd NFR-OPS-001 itself asks only for a single file with the SPA embedded and
# names no size at all. Measured with this recipe's own flags:
#
#     default (noavif, static)   25 833 656   SPA + SQLite + codecs + pdfium
#     + AVIF  (`-avif` variant)  27 418 916   AVIF = 1 585 260  (FR-IDX-011, 필수)
#     -tags nopdf                19 689 764   PDF  = 7 729 152  (FR-SRV-006, 필수)
#     -tags "noavif nopdf"       15 286 456   the CGO-free base
#
# (The two deltas are measured against different baselines: 7 729 152 is what PDF
# costs an AVIF-enabled build. Against the DEFAULT, dropping PDF saves
# 25 833 656 − 15 286 456 = 10 547 200.)
#
# 33 554 432 is E-19's headroom over the measured build: pdfium WASM ≈8.34 MB,
# AVIF WASM ≈1.58 MB, pure-Go SQLite (CON-001 forces modernc.org/sqlite), and the
# embedded SPA are what occupy it. The gate still cannot be satisfied by anything
# that matters and still trips on a dropped -s -w, an accidentally embedded asset
# tree, or the 4 MB end of E-7's Korean webfont. Keep this in lockstep with
# impl-plan §7.3 and README.md; internal/buildinfo/release_budget_test.go reads
# all three and fails if any of them drift.
SIZE_BUDGET := 33554432

# The arch §1.1 dependency set, pinned exactly (impl-plan WP-00 acceptance 2),
# as module:version pairs. `tidy` refuses to finish if any of them stops being
# required at exactly this version. Keep in lockstep with the first require
# block of go.mod.
FROZEN_DEPS := \
	modernc.org/sqlite:v1.54.0 \
	golang.org/x/text:v0.40.0 \
	golang.org/x/image:v0.44.0 \
	github.com/disintegration/imaging:v1.6.2 \
	github.com/gen2brain/avif:v0.6.0 \
	github.com/klippa-app/go-pdfium:v1.19.6 \
	github.com/tetratelabs/wazero:v1.12.0 \
	gopkg.in/yaml.v3:v3.0.1 \
	golang.org/x/crypto:v0.54.0

.PHONY: all web build build-go dev test test-int bench lint check-readonly contract \
        fmt tidy release e2e e2e-synthetic gates web-gates clean help

all: build

## help — the target list, which is the only documentation a Makefile owes.
help:
	@grep -hE '^## ' $(MAKEFILE_LIST) | sed 's/^## /  /'

## print-<VAR> — expand one variable and print it (`make -s print-TAGS`).
#
# internal/buildinfo/staticlink_test.go reads TAGS, RELEASE_TARGETS,
# STATIC_ARTEFACTS and AVIF_ARTEFACTS through this rather than re-implementing
# make's expander in Go. That is what makes those guards semantic instead of
# textual: a `TAGS := ` line placed AFTER the `?=` line, or a $(filter …) quietly
# narrowed to one architecture, changes what this prints and the guard fails.
print-%:
	@printf '%s\n' '$($*)'

## web — build the SPA that go:embed swallows (arch §2.1). Must precede `go build`.
web:
	cd web && $(PNPM) install --frozen-lockfile && $(PNPM) build

## build — the single static binary of NFR-OPS-001: SPA embedded, cgo-free.
build: web build-go

## build-go — relink the binary against whatever is already in web/dist.
build-go:
	@mkdir -p dist
	$(BUILDENV) $(GO) build -trimpath -tags "$(TAGS)" -ldflags "$(LDFLAGS)" -o dist/shelf ./cmd/shelf
	@printf '>> dist/shelf  %s bytes\n' "$$(wc -c < dist/shelf)"
	@SHELF_STATIC_ARTEFACTS="dist/shelf" $(STATICCHECK)

## dev — fast loop: skip the frontend build, serve whatever is already in web/dist.
dev:
	$(BUILDENV) $(GO) run ./cmd/shelf --config ./shelf.yaml --log-level debug

## test — the hermetic unit suite (impl-plan §6.1), run TWICE: untagged, then in
## the tag set that actually ships.
#
# CGO_ENABLED=1 here, and only here. impl-plan §5.1 requires `-race`, and the
# race detector is implemented in C: `go test -race` under CGO_ENABLED=0 fails
# outright with "-race requires cgo". CON-001 constrains the *shipped binary*,
# which `build` and `release` produce cgo-free; it does not constrain the test
# runner. Set RACE= to drop the flag on a machine with no C compiler.
#
# WHY TWO PASSES. Ruling E-21 made `noavif` the default, so `$(TAGS)` is the
# configuration users receive — and until this target ran it, NOTHING did. A
# single untagged run left `go test -tags noavif ./...` failing for weeks
# (testdata/golden/{health,settings}.json pinned `avif_enabled: true`, which a
# default build can no longer report) with every gate green. `make lint` only
# *vets* the shipped tags; vet does not run tests.
#
#   pass 1  untagged — the SUPERSET. It is the only pass that carries a real
#           AVIF decoder, so it is what executes the live decode path
#           (thumbs.TestGenerate_avif_decodesThroughTheSerialisedSlowPath
#           self-skips under `-tags noavif`) and the `capable ⟹ reported true`
#           half of httpapi.TestCapabilityFlags_neverExceedTheBuild.
#   pass 2  -tags "$(TAGS)" — what SHIPS. Compiles internal/thumbs/avif_off.go
#           and asserts the product behaves in the configuration users get.
#
# Both are needed: neither is a subset of the other. Dropping pass 2 is the
# defect above; dropping pass 1 would delete the AVIF decode coverage entirely.
# internal/buildinfo/staticlink_test.go asserts pass 2 stays in this recipe.
RACE ?= -race
test:
	@echo ">> test 0/2: the E2E gate's own assertions (no server, ~1 s)"
	@python3 scripts/e2e-assert-rescan-test.py
	@echo ">> test 1/2: untagged (the superset — includes the live AVIF decode path)"
	$(GOENV) CGO_ENABLED=1 $(GO) test ./... $(RACE) -count=1
	@echo ">> test 2/2: the SHIPPED tag set (-tags \"$(TAGS)\")"
	$(GOENV) CGO_ENABLED=1 $(GO) test -tags "$(TAGS)" ./... $(RACE) -count=1

## test-int — the integration suite of impl-plan §6.2. Needs the media volume.
#
# SHELF_TEST_ROOT must point at the collection the curated subset lives in; with
# it unset every test in ./integration skips itself and the target is a no-op
# that still proves the tag compiles.
test-int:
	@if [ -z "$(SHELF_TEST_ROOT)" ]; then \
		echo ">> SHELF_TEST_ROOT is unset: the integration tests will skip."; \
		echo ">> Run: make test-int SHELF_TEST_ROOT=\"/path/to/the/collection\""; \
	fi
	$(GOENV) CGO_ENABLED=0 SHELF_TEST_ROOT="$(SHELF_TEST_ROOT)" \
		$(GO) test -tags integration ./integration/... -count=1 -timeout 30m -v

## bench — impl-plan §6.4. A >20 % regression against the baseline fails review.
bench:
	$(BUILDENV) $(GO) test ./... -run '^$$' -bench . -benchmem

## lint — vet + staticcheck + the static guards + the frozen-contract gate.
#
# Written as one recipe rather than as prerequisites so that the four steps run
# in a fixed, useful order: the cheap greps first, then the two compilers'
# worth of analysis, then the contract diff last, where its output is the last
# thing on screen.
#
# `vet` runs twice. Untagged is the superset that type-checks the AVIF path;
# `-tags "$(TAGS)"` is what actually SHIPS (ruling E-21 made `noavif` the
# default), and nothing else in the repo ever compiles that configuration —
# internal/thumbs/avif_off.go would otherwise rot unnoticed.
lint:
	@scripts/check-readonly.sh
	@scripts/check-gates.sh
	$(BUILDENV) $(GO) vet ./...
	@echo ">> vet of the SHIPPED tag set (-tags \"$(TAGS)\")"
	$(BUILDENV) $(GO) vet -tags "$(TAGS)" ./...
	$(BUILDENV) $(GO) run honnef.co/go/tools/cmd/staticcheck@latest ./...
	@$(BUILDENV) $(GO) run ./scripts/contractcheck

## check-readonly — FR-CFG-005, the two-database rule (D-13) and A-8's
## write-once series_seen, as three greps. See scripts/check-readonly.sh.
check-readonly:
	@scripts/check-readonly.sh

## contract — impl-plan §4's reconciliation gate: the server's golden JSON
## diffed against web/src/api/types.ts, field by field and enum by enum.
contract:
	@$(BUILDENV) $(GO) run ./scripts/contractcheck

fmt:
	$(BUILDENV) $(GO) fmt ./...

## tidy — `go mod tidy`, except it may not break WP-00 acceptance 2.
#
# go.mod `require`s all nine frozen dependencies before any package imports
# them, so every wave-1 package starts from one already-downloaded, immutable
# module graph. A bare `go mod tidy` run today deletes seven of the nine (only
# x/text and x/image are imported so far) plus all twelve indirect lines —
# measured, not hypothetical. Silently losing them turns acceptance 2 red and
# forces a re-resolve that may not land on the verified versions.
#
# So: snapshot, tidy, verify every pin survived at exactly the arch §1.1
# version, and restore the snapshot if any did not.
tidy:
	@cp go.mod .go.mod.bak && cp go.sum .go.sum.bak
	@echo '$(BUILDENV) $(GO) mod tidy'
	@if ! $(BUILDENV) $(GO) mod tidy; then \
		mv .go.mod.bak go.mod; mv .go.sum.bak go.sum; \
		echo "make tidy: rolled back, go mod tidy failed"; exit 1; \
	fi
	@lost=""; \
	for p in $(FROZEN_DEPS); do \
		mod=$${p%:*}; ver=$${p#*:}; \
		awk -v m="$$mod" -v v="$$ver" \
			'($$1==m && $$2==v) || ($$1=="require" && $$2==m && $$3==v) {ok=1} END{exit !ok}' \
			go.mod || lost="$$lost $$mod@$$ver"; \
	done; \
	if [ -n "$$lost" ]; then \
		mv .go.mod.bak go.mod; mv .go.sum.bak go.sum; \
		echo "make tidy: ROLLED BACK — go mod tidy would drop frozen dependencies:"; \
		for m in $$lost; do echo "      $$m"; done; \
		echo "  The arch section 1.1 set is pinned ahead of the packages that import"; \
		echo "  it so wave 1 builds against an immutable module graph (WP-00"; \
		echo "  acceptance 2). Land the importing package first, then tidy."; \
		exit 1; \
	fi; \
	rm -f .go.mod.bak .go.sum.bak; \
	echo "tidy: clean; all 9 frozen dependencies intact"

## release — the seven NFR-OPS-003 targets plus SHA256SUMS, all cgo-free.
release: web
	@mkdir -p dist
# `release` owns its whole output set. Without this, an artefact from a
# previous run under a different VERSION or a different TAGS survives into
# SHA256SUMS and into the linkage gates' fallback glob, where it fails
# `make test` for reasons that have nothing to do with the current tree.
# `dist/shelf` (build-go's output) does not match `shelf-*-*` and survives.
	@rm -f dist/shelf-*-* dist/SHA256SUMS dist/ARTIFACTS.txt
	@for t in $(RELEASE_TARGETS); do \
	  os=$${t%/*}; arch=$${t#*/}; ext=""; [ "$$os" = "windows" ] && ext=".exe"; \
	  for v in default avif; do \
	    tags="$(TAGS)"; sfx=""; \
	    if [ "$$v" = "avif" ]; then tags="$(AVIF_TAGS)"; sfx="-avif"; fi; \
	    out="dist/shelf-$(VERSION)-$$os-$$arch$$sfx$$ext"; \
	    GOOS=$$os GOARCH=$$arch GOARM=7 $(BUILDENV) $(GO) build -trimpath -tags "$$tags" \
	      -ldflags "$(LDFLAGS)" -o "$$out" ./cmd/shelf || exit 1; \
	    printf '>> %-48s %10s bytes\n' "$$out" "$$(wc -c < $$out)"; \
	  done; \
	done
# The E-21 gates, deliberately OUTSIDE the loop and on their own recipe line.
# Inside the loop they depended on a trailing `|| exit 1` for their verdict to
# matter — this recipe has no `set -e`, so the loop's status is the last
# command's, and deleting those five characters made a dynamic artefact ship
# with `make release` reporting success. On its own line, make's own error
# handling is the enforcement and there is nothing to delete.
# Both lists come from RELEASE_TARGETS, so every linux target is covered.
	@SHELF_STATIC_ARTEFACTS="$(STATIC_ARTEFACTS)" \
	 SHELF_AVIF_ARTEFACTS="$(AVIF_ARTEFACTS)" $(STATICCHECK)
	@cd dist && sha256sum shelf-$(VERSION)-* > SHA256SUMS
	@printf '%s\n' \
	  "SHELF $(VERSION) — release artefacts" \
	  "" \
	  "  shelf-$(VERSION)-<os>-<arch>[.exe]        DEFAULT. Built with $(TAGS_DESC)." \
	  "      Statically linked on linux: no libc, no dynamic loader. This is the" \
	  "      artefact for a NAS (Synology/QNAP/Alpine, musl or old glibc)." \
	  "      .avif thumbnails degrade to 422 thumb_unavailable" \
	  "      (detail.reason: \"avif_disabled\"); the original bytes still stream" \
	  "      from /pages/{n} and every target browser decodes AVIF natively." \
	  "" \
	  "  shelf-$(VERSION)-<os>-<arch>-avif[.exe]   OPT-IN. Built with $(AVIF_TAGS_DESC)." \
	  "      Adds the gen2brain/avif decoder (+~1.6 MB) and, on linux, a DYNAMIC" \
	  "      link against libc.so.6/libdl.so.2/libpthread.so.0 via ebitengine/purego." \
	  "      Use only on a glibc host. See docs/decisions.md, ruling E-21." \
	  > dist/ARTIFACTS.txt
	@echo ">> dist/SHA256SUMS  ·  dist/ARTIFACTS.txt"
	@size=$$(wc -c < dist/shelf-$(VERSION)-linux-amd64); \
	if [ "$(SIZE_BUDGET)" -le 0 ]; then \
		echo ">> linux/amd64 is $$size bytes (budget check disabled)"; \
	elif [ "$$size" -gt $(SIZE_BUDGET) ]; then \
		echo ""; \
		echo "NFR-OPS-001: linux/amd64 is $$size bytes, over the $(SIZE_BUDGET) byte budget."; \
		echo "  Every artefact above and dist/SHA256SUMS were still produced."; \
		echo "  The default already carries -tags noavif (ruling E-21, static linkage);"; \
		echo "  the one lever left is -tags nopdf, which costs FR-SRV-006 entirely"; \
		echo "  (~10.5 MB off THIS baseline) and is 필수, so it is not a free win."; \
		echo "  The budget was"; \
		echo "  set from a measurement (ruling E-19, decisions.md); if this build is"; \
		echo "  legitimately larger, move it there, in impl-plan section 7.3 and in"; \
		echo "  README.md —"; \
		echo "  'make release SIZE_BUDGET=0' waives the gate for one run only."; \
		exit 1; \
	else \
		echo ">> linux/amd64 is $$size bytes, within the $(SIZE_BUDGET) byte budget (NFR-OPS-001)"; \
	fi

## e2e — the curated real-collection subset of impl-plan §6.3.
e2e:
	@scripts/e2e.sh $(E2E_ARGS)

## e2e-synthetic — the same assertions against a ~2 MB hermetic fixture tree,
## for a machine where the media volume is not mounted (D-49).
e2e-synthetic:
	@scripts/e2e.sh --synthetic $(E2E_ARGS)

## web-gates — the four frontend gates, which live in web/package.json and are
## NOT reachable from `make lint` or `make test`: `make lint` does not run
## eslint and `make test` does not run vitest. Kept as its own target so the
## frontend loop is one word, and so `gates` below has something to name.
web-gates:
	cd web && $(PNPM) typecheck && $(PNPM) lint && $(PNPM) test && $(PNPM) build

## gates — every gate this repository has, in one target.
##
## The reason this exists: NOTHING depended on `e2e-synthetic`, and there is no
## CI, so running it was purely a matter of human memory. It sat broken through
## three consecutive sessions that each reported "all gates green" — nobody ran
## it, so nothing failed, and the summaries read that silence as agreement
## (docs/HANDOFF.md §4.3, §6.5). A gate that exists but is never run does not go
## green; it says nothing at all.
##
##   make gates                 all five
##   make gates GATES_E2E=0     skip `make e2e`, which needs the media volume
##
## Ordered so a failure surfaces soonest — the frontend four take seconds to a
## couple of minutes, `make test` runs the Go suite TWICE, and the two E2E
## rounds are minutes each. That is a different order from the one HANDOFF §6
## prints; the numbering below follows §6's names so cross-references still read.
##
## Three rules this target must keep:
##   * no pipes around a gate — `make test | tail; echo $$?` reports tail's exit
##     code, not make's (§6), and that nearly shipped an unverified green;
##   * the closing banner names EVERY gate and whether it RAN or was SKIPPED.
##     A skipped gate must never be readable as a passed one (§6.5, last line);
##   * each gate records its own name only after it returns 0, and the banner
##     prints what was recorded — so a gate that FAILS aborts before its record
##     exists, and GATES_TOTAL refuses to print a result when the count is off.
##     Both records are `&&`-joined onto the gate's own recipe line.
##
##     Read that as exactly what it is and no more. A review found the first
##     version of this comment claiming the banner was "derived, not asserted",
##     which it cannot be: the record lives in the same recipe as the gate, so
##     an edit to the recipe can drop the gate and keep the record. Splitting
##     them across two lines made a one-line deletion enough; `&&` closes that
##     accident and nothing closes the deliberate rewrite. **No bookkeeping a
##     recipe does about itself survives the recipe being rewritten.**
##
##     What actually holds the line is `scripts/check-gates.sh`, which reads
##     this Makefile as an artefact from outside — same shape as
##     check-readonly.sh and contractcheck, and for the same reason. It runs in
##     `make lint`. Its header explains what it asserts.
##
##     The log is written outside the repository on purpose: e2e step 9 fails on
##     new files under the tree, and that check is correct.
GATES_E2E  ?= 1
GATES_TOTAL := 5
GATES_LOG   := $(shell mktemp -u "$${TMPDIR:-/tmp}/shelf-gates-XXXXXX.log")

gates:
	@rm -f '$(GATES_LOG)'
	@echo ''
	@echo '=== gate 3/5 · web: typecheck · lint · test · build ==================='
	@$(MAKE) --no-print-directory web-gates && echo '3/5  web typecheck/lint/test/build   RAN' >> '$(GATES_LOG)'
	@echo ''
	@echo '=== gate 1/5 · make lint ============================================='
	@$(MAKE) --no-print-directory lint && echo '1/5  make lint                       RAN' >> '$(GATES_LOG)'
	@echo ''
	@echo '=== gate 2/5 · make test ============================================='
	@$(MAKE) --no-print-directory test && echo '2/5  make test                       RAN' >> '$(GATES_LOG)'
	@echo ''
	@echo '=== gate 4/5 · make e2e-synthetic ===================================='
	@$(MAKE) --no-print-directory e2e-synthetic && echo '4/5  make e2e-synthetic              RAN' >> '$(GATES_LOG)'
	@echo ''
	@if [ "$(GATES_E2E)" = "0" ]; then \
		echo '=== gate 5/5 · make e2e · SKIPPED ===================================='; \
		echo '5/5  make e2e                        SKIPPED (GATES_E2E=0)' >> '$(GATES_LOG)'; \
	else \
		echo '=== gate 5/5 · make e2e ============================================='; \
		$(MAKE) --no-print-directory e2e && echo '5/5  make e2e                        RAN' >> '$(GATES_LOG)'; \
	fi
	@echo ''
	@echo '  ────────────────────────────────────────────────────────────'
	@sort '$(GATES_LOG)' | sed 's/^/   /'
	@echo '  ────────────────────────────────────────────────────────────'
	@ran=$$(grep -c 'RAN$$' '$(GATES_LOG)' || true); \
	skipped=$$(grep -c 'SKIPPED' '$(GATES_LOG)' || true); \
	total=$$((ran + skipped)); \
	rm -f '$(GATES_LOG)'; \
	if [ "$$total" -ne $(GATES_TOTAL) ]; then \
		echo "   $$total gate outcomes recorded, expected $(GATES_TOTAL)."; \
		echo "   The banner is derived from what actually ran, so this means a"; \
		echo "   gate was added or removed without updating GATES_TOTAL. Refusing"; \
		echo "   to report a result — an incomplete run must not read as a green."; \
		echo ''; \
		exit 1; \
	elif [ "$$skipped" -gt 0 ]; then \
		echo "   $$ran of $(GATES_TOTAL) gates ran, $$skipped SKIPPED. This is NOT a full green."; \
	else \
		echo "   All $(GATES_TOTAL) gates ran and passed."; \
	fi
	@echo ''

clean:
	rm -rf dist web/dist/*
	@touch web/dist/.gitkeep
