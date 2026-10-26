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
# names no size at all. Measured on linux/amd64 with this recipe's own flags and
# go1.26.9 — these are MEASURED_* below, and `release` re-measures them:
#
#     default (noavif, static)   33 210 530   SPA + SQLite + codecs + pdfium
#     + AVIF  (`-avif` variant)  34 787 593   AVIF = 1 577 063  (FR-IDX-011, 필수)
#     -tags "noavif nopdf"       22 630 562   PDF  = 10 579 968 (FR-SRV-006, 필수)
#
# Both deltas are against the default. (E-19's table also had an AVIF-enabled
# `-tags nopdf` row, which is where arch §1.2's smaller PDF figure comes from; no
# shipped artefact is built that way, so it is no longer measured.)
#
# 33 554 432 is E-19's headroom over the build it measured, which was 7.4 MB
# smaller than the one above — most of the difference is the vendored fonts that
# E-46, E-53 and E-55 added after E-19 measured — so the margin left is about 1%,
# and that only after E-63 stopped embedding the SPA's JS and CSS uncompressed.
# pdfium WASM ≈8.34 MB, AVIF WASM ≈1.58 MB, pure-Go SQLite (CON-001 forces
# modernc.org/sqlite) and the embedded SPA are what occupy the budget. The gate
# still cannot be satisfied by anything that matters and still trips on a
# dropped -s -w, an accidentally embedded asset tree, or the 4 MB end of E-7's
# Korean webfont. Keep this in lockstep with
# impl-plan §7.3 and README.md; internal/buildinfo/release_budget_test.go reads
# all three and fails if any of them drift.
SIZE_BUDGET := 33554432

# The measured sizes the table above and README.md quote. The budget always had
# a guard; these had none, and README went on quoting E-19's measurement after the
# binary had passed 30 MB. So `release` re-measures all three — the two linux/amd64
# artefacts it ships, plus a throwaway `nopdf` build — and fails when one has
# moved by more than MEASURED_SLACK. release_budget_test.go holds the table and
# README to these variables, so a re-measurement is one place to edit plus two it
# will point at.
#
# The toolchain is part of the record because it moves every figure: the same
# commit is 303 KB smaller under go1.25.0, and GOENV's GOTOOLCHAIN=auto builds with
# any newer Go that happens to be installed. A different toolchain therefore FAILS
# the check rather than skipping it — a host upgrade is exactly the drift nobody
# saw. (`make build` and `test` are unaffected; only `release` measures.)
# MEASURED_SLACK covers the -X strings (VERSION, COMMIT), whose length varies per
# build; a 30-byte change was absorbed by section padding entirely. It must stay
# under 50 000, so that a figure within the slack is never more than one 0.1 MB
# step from the one README rounds; the test enforces that.
# A non-default TAGS skips the check (these figures describe TAGS=noavif), and
# `make release MEASURED_CHECK=0` waives it for one run. `:=`, not `?=`, so an
# exported MEASURED_CHECK in someone's environment cannot waive it for every run.
MEASURED_TOOLCHAIN := go1.26.9
MEASURED_DEFAULT   := 33210530
MEASURED_AVIF      := 34787593
MEASURED_NOPDF     := 22630562
MEASURED_SLACK     := 4096
MEASURED_CHECK     := 1

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
	github.com/nwaples/rardecode/v2:v2.3.0 \
	github.com/tetratelabs/wazero:v1.12.0 \
	gopkg.in/yaml.v3:v3.0.1 \
	golang.org/x/crypto:v0.54.0

.PHONY: all web build build-go dev test test-int test-int-full bench lint check-readonly contract \
        fmt tidy release e2e e2e-synthetic gates gates-precheck web-gates clean help

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
#
# E-63: the second line replaces every web/dist/assets/**/*.js and *.css with a
# deterministic .gz (scripts/precompress), because go:embed stores bytes as they
# are and the release would otherwise carry the SPA's text uncompressed. The
# server serves either form (internal/httpapi/spa.go: the .gz as-is to a gzip
# client, inflated once otherwise), so `web-gates`'s build, which is a check and
# not a release input, stays uncompressed. A precompressed dist is for the
# binary only: `vite preview` cannot load its assets — rerun `pnpm build`.
web:
	cd web && $(PNPM) install --frozen-lockfile && $(PNPM) build
	$(BUILDENV) $(GO) run ./scripts/precompress web/dist

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

# The packages a build tag changes, DERIVED from the source rather than written
# out, so that a new tagged file joins its target without anyone remembering to.
# A hand-written `./integration/...` is how internal/archive/{zipidx,rar4,hv3}'s
# integration tests and internal/scanner's ratio test came to be compiled by no
# target at all; nothing failed, because a file no build includes cannot fail.
#
# GO_FIND walks the tree the way `./...` does: testdata/, vendor/, and names
# starting with `.` or `_` are pruned (so .worktrees/ is), and so is node_modules/,
# where pnpm keeps third-party .go files. A directory with its own go.mod is
# another module, outside `./...`: GO_MODULES (every go.mod but the root's) drops
# what lies under it. Pruning those inside find would cost a `test` process per
# directory on every make run, these being `:=`. Symlinks are taken (`-type l`,
# POSIX, so not GNU-only like -xtype): the go tool compiles a symlink to a file
# and ignores one to a directory, as `grep -d skip` does. tagged_pkgs turns the
# files whose `//go:build` line matches $(1) into a sorted `./dir` list; leading
# blanks are allowed because go/build trims each line before reading it.
#
#   INT_PKGS    the tag is REQUIRED: `integration` not preceded by `!`, so
#               `integration && !race` is in and `!integration` is out.
#   NOPDF_PKGS  the tag is MENTIONED, in either polarity: a package with only a
#               `!nopdf` file (internal/httpapi) is still a different package
#               under nopdf, and that configuration is what pass 3 runs.
#
# Both are greps, and deliberately NOT a constraint parser: `!(integration)` and
# `!(!integration)` are read wrong, and that is accepted. The semantic authority
# is internal/buildinfo/testcoverage_test.go, which holds them to go/build's own
# constraint matcher on this tree and on a synthetic one built from the shapes a
# grep gets wrong — so a misreading is a red test naming the package, never a
# silent loss (that claim is itself tested). An empty list is an error in the
# recipes that use it: `go test` with no package arguments tests the current
# directory and passes.
GO_PRUNE    := \( -name testdata -o -name vendor -o -name node_modules \
                  -o -name '.?*' -o -name '_*' \) -prune
GO_MODULES  := $(filter-out .,$(patsubst %/go.mod,%,$(shell find . $(GO_PRUNE) -o -type f -name go.mod -print)))
GO_FIND     := find . $(GO_PRUNE) -o \( -type f -o -type l \) -name '*.go'
tagged_pkgs = $(sort $(patsubst %/,%,$(dir $(filter-out $(addsuffix /%,$(GO_MODULES)),$(shell \
                  $(GO_FIND) -exec grep -d skip -lE '$(1)' {} +)))))
INT_PKGS    := $(call tagged_pkgs,^[[:space:]]*//go:build(.*[^!A-Za-z0-9_.])?integration([^A-Za-z0-9_.]|$$))
NOPDF_PKGS  := $(call tagged_pkgs,^[[:space:]]*//go:build(.*[^A-Za-z0-9_.])?nopdf([^A-Za-z0-9_.]|$$))

## test — the hermetic unit suite (impl-plan §6.1), run in THREE passes: untagged,
## the tag set that ships, and the nopdf build in the packages it changes.
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
#   pass 3  -tags "$(TAGS) nopdf" over $(NOPDF_PKGS) — the supported build
#           without PDFium. Its stub tests (internal/pdfium/pool_stub_test.go,
#           internal/source/{pdfsource_stub_test.go,encoding_nopdf_test.go})
#           compile under no other pass, so until this pass existed nothing ran
#           them: deleting the rounding in pdfium/pool_stub.go stayed green.
#
# Passes 1 and 2 are both needed: neither is a subset of the other. Dropping pass
# 2 is the defect above; dropping pass 1 would delete the AVIF decode coverage
# entirely. Pass 3 is NOT the whole suite a third time: a package with no nopdf
# constraint compiles to the same code under it, and `make test` is already the
# job most likely to run the machine out of memory. Every pass carries $(RACE)
# and -count=1. internal/buildinfo/staticlink_test.go asserts all three passes,
# line by line, and that RACE defaults to -race through `?=`.
RACE ?= -race
test:
	@$(if $(NOPDF_PKGS),,$(error NOPDF_PKGS is empty: no package has a nopdf build constraint. Pass 3 would test the current directory and pass. See the comment above INT_PKGS))
	@echo ">> test 0/3: the E2E gate's own assertions (no server, ~1 s)"
	@python3 scripts/e2e-assert-rescan-test.py
	@echo ">> test 1/3: untagged (the superset — includes the live AVIF decode path)"
	$(GOENV) CGO_ENABLED=1 $(GO) test ./... $(RACE) -count=1
	@echo ">> test 2/3: the SHIPPED tag set (-tags \"$(TAGS)\")"
	$(GOENV) CGO_ENABLED=1 $(GO) test -tags "$(TAGS)" ./... $(RACE) -count=1
	@echo ">> test 3/3: the nopdf build, in the packages it changes (-tags \"$(TAGS) nopdf\")"
	$(GOENV) CGO_ENABLED=1 $(GO) test -tags "$(TAGS) nopdf" $(NOPDF_PKGS) $(RACE) -count=1

## test-int — the integration suite of impl-plan §6.2, on a deterministic SAMPLE
## of the media volume (E-66). Gate 5/5 runs it; `test-int-full` is the whole set.
#
# It runs $(INT_PKGS): every package holding a `//go:build integration` file
# (see the comment above that variable), not just ./integration.
#
# THE ROOT. SHELF_TEST_ROOT defaults to the one line in scripts/media-root, which
# is the only place the media volume's path is written (E-66 §2.4); scripts/e2e.sh
# and scripts/e2e-config.sh read the same file for SHELF_E2E_ROOT. The file is
# found next to this Makefile, not in the working directory, so `make -f` from
# elsewhere reads the same default. $(file <) drops the final newline and nothing
# else — no $(strip), because the path holds spaces and a strip would be one more
# reader with its own idea of the value. The environment or the command line still
# override it through `?=`, and an explicitly empty value stays empty.
#
# Finding "next to this Makefile" without a shell: $(MAKEFILE_LIST) is a
# space-separated list, so `$(dir $(lastword …))` cuts a path with spaces in it
# in half. Here (no `include` above this line) the list is normally just this
# Makefile, so the whole list is read as ONE path first — spaces swapped for a
# marker, $(dir) taken, spaces put back. If no scripts/media-root sits there,
# the list held more than one makefile (`-f a.mk -f Makefile`, or $MAKEFILES),
# and the last word is used as before. Not covered: a tab in the path, or
# several makefiles in the list AND a space in one of them, which make itself
# cannot tell apart; the default is then empty, and the gates pre-check and
# test-int name the empty SHELF_TEST_ROOT.
#
# With the root missing, the tests that NEED it skip themselves (those that build
# their own tree, as from scripts/mkfixture, run regardless) and `go test` exits
# 0. That is fine for an operator's ad-hoc run, and wrong for a gate, where the
# skip would read as RAN:
# TEST_INT_REQUIRE_ROOT=1 (what gate 5/5 passes) reaches the Go side as
# SHELF_TEST_REQUIRE_ROOT=1, and `testutil.MediaRoot` then fails the test instead
# of skipping it (E-66 §2.6). The default is 0, so a bare `make test-int` on a
# machine without the volume behaves as it always has.
#
# THE SAMPLE. The full run does not fit a gate — zipidx alone timed out at 30
# minutes on the real root — so SHELF_TEST_SAMPLE carries TEST_INT_SAMPLE, a
# positive decimal integer, to the archive readers' integration tests, which
# pick that many files by a hash of their root-relative path plus the pinned ones
# the tests name (`testutil`; the rule is E-66 §2.2). Unset means ALL to the Go
# side, for a bare `go test`; that is why dropping the word from the line below
# would not fail, it would quietly bring the timeout back. A value make cannot
# read as a count is refused here, before INT_PKGS compiles, rather than handed
# on: `0` and `007` included, since neither is what anyone means to type.
#
# Not everything needs a root: internal/scanner's incremental-vs-cold ratio test
# (`integration && !race`) runs regardless — it lives here because it is a slow
# timing measurement that `make test`'s -race would distort. `-tags integration`
# also reruns those packages' ordinary unit tests, without -race and with cgo
# off, which is the price of compiling their tagged files in the same package.
#
# The root notice both recipes print is for a human and changes no outcome:
# whether a missing root skips or fails is TEST_INT_REQUIRE_ROOT's business.
#
# internal/buildinfo/testint_test.go pins both recipes' environment, -timeout and
# package list, the defaults below, and the refusal.
shelf_empty           :=
shelf_sp              := $(shelf_empty) $(shelf_empty)
shelf_whole_dir       := $(subst <shelf:sp>,$(shelf_sp),$(dir $(subst $(shelf_sp),<shelf:sp>,$(MAKEFILE_LIST))))
shelf_media_whole     := $(file <$(shelf_whole_dir)scripts/media-root)
SHELF_MEDIA_ROOT      := $(if $(shelf_media_whole),$(shelf_media_whole),$(file <$(dir $(lastword $(MAKEFILE_LIST)))scripts/media-root))
SHELF_TEST_ROOT       ?= $(SHELF_MEDIA_ROOT)
TEST_INT_SAMPLE       ?= 500
TEST_INT_REQUIRE_ROOT ?= 0

# is_count: non-empty iff $(1) is a positive decimal integer without a leading
# zero — one word, nothing but digits once they are all deleted, not `0…`.
strip_digits = $(subst 0,,$(subst 1,,$(subst 2,,$(subst 3,,$(subst 4,,$(subst 5,,$(subst 6,,$(subst 7,,$(subst 8,,$(subst 9,,$(1)))))))))))
is_count     = $(and $(1),$(if $(word 2,x$(1)x),,y),$(if $(call strip_digits,$(1)),,y),$(filter-out 0%,$(1)))
TEST_INT_SAMPLE_CHECK = $(if $(call is_count,$(TEST_INT_SAMPLE)),,$(error TEST_INT_SAMPLE must be a \
  positive decimal integer without a leading zero (got "$(TEST_INT_SAMPLE)"). For the whole \
  set run `make test-int-full`, which passes SHELF_TEST_SAMPLE=all (E-66 §2.2, §2.3)))

test-int:
	@$(if $(INT_PKGS),,$(error INT_PKGS is empty: no package has a //go:build integration file. go test would test the current directory and pass. See the comment above INT_PKGS))
	@$(TEST_INT_SAMPLE_CHECK)
	@if [ ! -d "$(SHELF_TEST_ROOT)" ] || [ ! -r "$(SHELF_TEST_ROOT)" ]; then \
		echo ">> SHELF_TEST_ROOT (\"$(SHELF_TEST_ROOT)\") is not a readable directory: the"; \
		echo ">> tests that need the collection will skip (fail, with TEST_INT_REQUIRE_ROOT=1)."; \
		echo ">> The default is the path in scripts/media-root. Elsewhere, run:"; \
		echo ">>   make $@ SHELF_TEST_ROOT=\"/path/to/the/collection\""; \
	fi
	$(GOENV) CGO_ENABLED=0 SHELF_TEST_ROOT="$(SHELF_TEST_ROOT)" \
		SHELF_TEST_SAMPLE="$(TEST_INT_SAMPLE)" SHELF_TEST_REQUIRE_ROOT="$(TEST_INT_REQUIRE_ROOT)" \
		$(GO) test -tags integration $(INT_PKGS) -count=1 -timeout 30m -v

## test-int-full — test-int over EVERY file of the root (SHELF_TEST_SAMPLE=all).
## Not a gate: run it before a release, ahead of the commit approval (E-66 §2.3).
#
# Same recipe as test-int but for the sample and the timeout: the whole set takes
# well over the gate's budget, so -timeout is 120m rather than 30m. It passes
# TEST_INT_REQUIRE_ROOT through like test-int does; a release run that should not
# be able to skip says `make test-int-full TEST_INT_REQUIRE_ROOT=1`.
test-int-full:
	@$(if $(INT_PKGS),,$(error INT_PKGS is empty: no package has a //go:build integration file. go test would test the current directory and pass. See the comment above INT_PKGS))
	@if [ ! -d "$(SHELF_TEST_ROOT)" ] || [ ! -r "$(SHELF_TEST_ROOT)" ]; then \
		echo ">> SHELF_TEST_ROOT (\"$(SHELF_TEST_ROOT)\") is not a readable directory: the"; \
		echo ">> tests that need the collection will skip (fail, with TEST_INT_REQUIRE_ROOT=1)."; \
		echo ">> The default is the path in scripts/media-root. Elsewhere, run:"; \
		echo ">>   make $@ SHELF_TEST_ROOT=\"/path/to/the/collection\""; \
	fi
	$(GOENV) CGO_ENABLED=0 SHELF_TEST_ROOT="$(SHELF_TEST_ROOT)" \
		SHELF_TEST_SAMPLE=all SHELF_TEST_REQUIRE_ROOT="$(TEST_INT_REQUIRE_ROOT)" \
		$(GO) test -tags integration $(INT_PKGS) -count=1 -timeout 120m -v

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
#
# Then once more for each tag-only file set, over just the packages it changes
# (INT_PKGS, NOPDF_PKGS): neither vet above can see an `integration` or `nopdf`
# file, by construction. staticcheck is NOT repeated per tag. `go vet` with the
# tag already type-checks those files, which is the rot that matters, and each
# staticcheck run is a fresh `go run …@latest` analysis of the whole dependency
# graph — the expensive step of this recipe, not worth paying twice more for
# a handful of test files.
lint:
	@scripts/check-readonly.sh
	@scripts/check-gates.sh
	$(BUILDENV) $(GO) vet ./...
	@echo ">> vet of the SHIPPED tag set (-tags \"$(TAGS)\")"
	$(BUILDENV) $(GO) vet -tags "$(TAGS)" ./...
	@echo ">> vet of the tag-only files (-tags integration; -tags \"$(TAGS) nopdf\")"
	@$(if $(INT_PKGS),,$(error INT_PKGS is empty. See the comment above it))
	@$(if $(NOPDF_PKGS),,$(error NOPDF_PKGS is empty. See the comment above it))
	$(BUILDENV) $(GO) vet -tags integration $(INT_PKGS)
	$(BUILDENV) $(GO) vet -tags "$(TAGS) nopdf" $(NOPDF_PKGS)
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
	echo "tidy: clean; all 10 frozen dependencies intact"

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
		echo "  ($$(( $(MEASURED_DEFAULT) - $(MEASURED_NOPDF) )) bytes off the measured default) and is 필수,"; \
		echo "  so it is not a free win."; \
		echo "  The budget was"; \
		echo "  set from a measurement (ruling E-19, decisions.md); if this build is"; \
		echo "  legitimately larger, move it there, in impl-plan section 7.3 and in"; \
		echo "  README.md —"; \
		echo "  'make release SIZE_BUDGET=0' waives the gate for one run only."; \
		exit 1; \
	else \
		echo ">> linux/amd64 is $$size bytes, within the $(SIZE_BUDGET) byte budget (NFR-OPS-001)"; \
	fi
# The MEASURED_* check: after the budget, so an over-budget build says that first,
# and on its own recipe line for the same reason as the E-21 gates. The toolchain
# is read off the artefact rather than `go env`, because what has to match the
# record is what built the file that ships.
	@if [ "$(MEASURED_CHECK)" = "0" ]; then \
		echo ">> measured-size check waived for this run (MEASURED_CHECK=0)"; exit 0; \
	fi; \
	if [ "$(strip $(TAGS))" != "noavif" ]; then \
		echo ">> measured-size check skipped: MEASURED_* describe TAGS=noavif, not \"$(TAGS)\""; exit 0; \
	fi; \
	probe=$$(mktemp -d) || exit 1; \
	trap 'rm -rf "$$probe"' EXIT; trap 'exit 130' INT TERM; \
	GOOS=linux GOARCH=amd64 $(BUILDENV) $(GO) build -trimpath -tags "$(TAGS) nopdf" \
	  -ldflags "$(LDFLAGS)" -o "$$probe/nopdf" ./cmd/shelf || exit 1; \
	def=dist/shelf-$(VERSION)-linux-amd64; bad=0; \
	tc=$$($(GOENV) $(GO) version "$$def" | awk '{print $$NF}'); \
	if [ "$$tc" != "$(MEASURED_TOOLCHAIN)" ]; then \
		echo "  MEASURED_TOOLCHAIN is $(MEASURED_TOOLCHAIN), but $$def was built with $$tc"; bad=1; \
	fi; \
	for row in "MEASURED_DEFAULT $(MEASURED_DEFAULT) $$def" \
	           "MEASURED_AVIF $(MEASURED_AVIF) $$def-avif" \
	           "MEASURED_NOPDF $(MEASURED_NOPDF) $$probe/nopdf"; do \
		set -- $$row; size=$$(wc -c < "$$3") || { bad=1; continue; }; \
		drift=$$(( size - $$2 )); \
		[ "$${drift#-}" -le $(MEASURED_SLACK) ] || { \
			echo "  $$1 is $$2, but the build measures $$size ($$drift)"; bad=1; }; \
	done; \
	if [ "$$bad" -ne 0 ]; then \
		echo ""; \
		echo "The sizes the Makefile and README.md quote no longer describe this build."; \
		echo "  Every artefact above and dist/SHA256SUMS were still produced."; \
		echo "  If the change is intended, copy the measured figures (and toolchain) into"; \
		echo "  MEASURED_* in the Makefile, then run the internal/buildinfo tests: they"; \
		echo "  name every line of the Makefile and README.md still quoting the old ones."; \
		echo "  If the toolchain change is not intended: GOENV sets GOTOOLCHAIN=auto, which"; \
		echo "  takes the newest Go installed — build on $(MEASURED_TOOLCHAIN) instead."; \
		echo "  'make release MEASURED_CHECK=0' waives this check for one run only."; \
		exit 1; \
	fi; \
	echo ">> default, -avif and nopdf (linux/amd64, $$tc) are within $(MEASURED_SLACK) bytes of MEASURED_*"

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
##   make gates GATES_E2E=0     skip gate 5/5 (`make e2e` + `make test-int`),
##                              which needs the media volume
##
## The five: 1/5 make lint, 2/5 make test, 3/5 web-gates, 4/5 make e2e-synthetic,
## and 5/5 the real collection — `make e2e` and then the integration suite on its
## sample (`make test-int TEST_INT_REQUIRE_ROOT=1`), one line, one record, RAN
## only when both return 0 (E-66 §2.1). Both need the media volume, so GATES_E2E
## turns them on and off together, and the count stays five.
##
## Ordered so a failure surfaces soonest — the frontend four take seconds to a
## couple of minutes, `make test` runs the Go suite three times, and the real
## rounds are the longest. That is a different order from the one HANDOFF §6
## prints; the numbering below follows §6's names so cross-references still read.
##
## The same ordering argument is why `gates-precheck` runs before gate 3/5: with
## GATES_E2E on, an unreadable SHELF_TEST_ROOT or e2e root (both default to
## scripts/media-root) would otherwise fail 5/5 after every other gate had run.
## It also refuses a TEST_INT_SAMPLE that test-int would refuse (E-66 §2.5). The
## root REQUIRED on 5/5 is the other half: there a missing root fails the
## integration tests that need it instead of skipping them, so no skip can be
## recorded RAN.
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
##     internal/buildinfo/testint_test.go runs this recipe against a stub
##     $(MAKE), so the pre-check's position and the 5/5 `&&` chain are observed
##     rather than read.
##
##     The log is written outside the repository on purpose: e2e step 9 fails on
##     new files under the tree, and that check is correct.
GATES_E2E  ?= 1
GATES_TOTAL := 5
# Whether 5/5 runs: off iff GATES_E2E is exactly `0`, on for anything else —
# empty, `0 0`, `no`. One predicate, read by gates-precheck and by gates alike:
# when the pre-check used `$(filter-out 0,…)` and the recipe `[ … = "0" ]`,
# an empty GATES_E2E skipped the sample check yet ran 5/5, so the bad sample
# surfaced after the other four gates. Same one-word test as is_count below.
gates_e2e_on = $(if $(word 2,x$(GATES_E2E)x),on,$(if $(filter x0x,x$(GATES_E2E)x),,on))
GATES_LOG   := $(shell mktemp -u "$${TMPDIR:-/tmp}/shelf-gates-XXXXXX.log")

# The pre-check of E-66 §2.5. Its own target so that it can be run — and tested
# — without running any gate; `gates` invokes it as its very first command.
# On a refusal its shell exits 1 (a bad sample is make's $(error)); make reports
# the failed recipe, so `make gates` itself exits 2, as make does for any failure.
gates-precheck:
	@$(if $(gates_e2e_on),$(TEST_INT_SAMPLE_CHECK))
	@if [ -z "$(gates_e2e_on)" ]; then exit 0; fi; \
	e2e_root="$${SHELF_E2E_ROOT:-$(SHELF_MEDIA_ROOT)}"; bad=0; \
	for pair in "SHELF_TEST_ROOT=$(SHELF_TEST_ROOT)" "SHELF_E2E_ROOT=$$e2e_root"; do \
		name=$${pair%%=*}; dir=$${pair#*=}; \
		if [ ! -d "$$dir" ] || [ ! -r "$$dir" ]; then \
			echo "gates: $$name (\"$$dir\") is not a readable directory."; bad=1; \
		fi; \
	done; \
	if [ "$$bad" -ne 0 ]; then \
		echo "  Gate 5/5 runs make e2e and make test-int against the real collection, last,"; \
		echo "  so this run would fail there after every other gate. Stopping before any."; \
		echo "  Both roots default to the path in scripts/media-root. Point them elsewhere:"; \
		echo "    make gates SHELF_TEST_ROOT=\"/path/to/the/collection\" SHELF_E2E_ROOT=\"/path/to/the/collection\""; \
		echo "  or skip gate 5/5, which the banner will report as SKIPPED:"; \
		echo "    make gates GATES_E2E=0"; \
		exit 1; \
	fi

gates:
	@$(MAKE) --no-print-directory gates-precheck
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
	@if [ -z "$(gates_e2e_on)" ]; then \
		echo '=== gate 5/5 · make e2e + test-int · SKIPPED ========================='; \
		echo '5/5  make e2e + test-int (sample)    SKIPPED (GATES_E2E=0)' >> '$(GATES_LOG)'; \
	else \
		echo '=== gate 5/5 · make e2e + test-int (sample) =========================='; \
		$(MAKE) --no-print-directory e2e && $(MAKE) --no-print-directory test-int TEST_INT_REQUIRE_ROOT=1 && echo '5/5  make e2e + test-int (sample)    RAN' >> '$(GATES_LOG)'; \
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
