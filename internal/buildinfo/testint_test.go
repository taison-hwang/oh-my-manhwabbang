package buildinfo_test

// Guards on how the integration suite is wired into the gates (ruling E-66).
//
// `make test-int` was in no gate, and a gate left to memory does not run
// (HANDOFF §6.5). E-66 puts it on gate 5/5 next to the real-collection round, and
// that move brings three ways to report a green nobody measured:
//
//   - the full run does not fit a gate (zipidx alone hit the 30-minute timeout
//     on the real root), so the gate runs a deterministic SAMPLE and the full run
//     moves to `make test-int-full`. The sample size and the two timeouts are
//     configuration, and configuration rots silently;
//   - without a root the tests that need one skip and `go test` exits 0, which
//     the banner would read as RAN. 5/5 therefore asks for TEST_INT_REQUIRE_ROOT=1
//     (the Go side, `testutil.MediaRoot`, turns the missing root into a failure),
//     and `make gates` refuses to start when either root is unreadable — before
//     an hour of the other gates, not after;
//   - the media root was a literal in three places. It lives in scripts/media-root
//     now, and a literal that comes back is a second source that will drift.
//
// scripts/check-gates.sh reads the same recipe from `make lint`; these tests add
// what a grep cannot: make's own expansion of the variables, and the recipe RUN
// against a stub $(MAKE), so the order and the `&&` chain are observed, not read.

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// runEnv is the environment words a goRun's line sets for the go tool, with the
// quotes shellWords already removed: `SHELF_TEST_SAMPLE="$(TEST_INT_SAMPLE)"`
// reads as SHELF_TEST_SAMPLE → $(TEST_INT_SAMPLE).
func runEnv(t *testing.T, r goRun) map[string]string {
	t.Helper()
	body := strings.TrimLeft(r.line, " \t")
	for body != "" && strings.IndexByte("@+-", body[0]) >= 0 {
		body = strings.TrimLeft(body[1:], " \t")
	}
	words, err := shellWords(body)
	if err != nil {
		t.Fatalf("re-reading a line goRuns accepted: %v\n  %s", err, r.line)
	}
	env := map[string]string{}
	for _, w := range words {
		if !envWord(w) {
			break
		}
		if k, v, ok := strings.Cut(w, "="); ok {
			env[k] = v
		}
	}
	return env
}

// integrationRun is the single `$(GO) test` a test-int-style recipe runs.
func integrationRun(t *testing.T, target string) (goRun, string) {
	t.Helper()
	recipe := commands(section(repoFile(t, "Makefile"), target+":"))
	if recipe == "" {
		t.Fatalf("Makefile has no `%s` target", target)
	}
	runs, problems := goRuns(recipe, "test")
	for _, p := range problems {
		t.Errorf("a `%s` line that mentions `$(GO) test` does not run it:\n  %s", target, p)
	}
	if len(runs) != 1 {
		t.Fatalf("the `%s` recipe runs `$(GO) test` %d times, want 1:\n%s", target, len(runs), recipe)
	}
	return runs[0], recipe
}

// TestIntegrationTargetsPassTheSampleAndTheRootSwitch pins both recipes'
// environment and timeouts. A dropped SHELF_TEST_SAMPLE is not a failure Go
// would see: unset means the FULL run (E-66 §2.2, for a bare `go test`), so the
// gate would quietly go back to timing out at 30 minutes.
func TestIntegrationTargetsPassTheSampleAndTheRootSwitch(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		target, sample, timeout string
	}{
		{"test-int", "$(TEST_INT_SAMPLE)", "30m"},
		{"test-int-full", "all", "120m"},
	} {
		run, recipe := integrationRun(t, tc.target)
		env := runEnv(t, run)
		for k, want := range map[string]string{
			"SHELF_TEST_ROOT":         "$(SHELF_TEST_ROOT)",
			"SHELF_TEST_SAMPLE":       tc.sample,
			"SHELF_TEST_REQUIRE_ROOT": "$(TEST_INT_REQUIRE_ROOT)",
			"CGO_ENABLED":             "0",
		} {
			if got, ok := env[k]; !ok || got != want {
				t.Errorf("`%s` runs the suite with %s=%q (set: %v), want %q:\n  %s",
					tc.target, k, got, ok, want, run.line)
			}
		}
		if tags, _ := run.flag("tags"); tags != "integration" || run.packages() != "$(INT_PKGS)" {
			t.Errorf("`%s` no longer runs `-tags integration $(INT_PKGS)` (tags %q, packages %q):\n  %s",
				tc.target, tags, run.packages(), run.line)
		}
		if got, _ := run.flag("timeout"); got != tc.timeout {
			t.Errorf("`%s` runs with -timeout %q, want %q (E-66 §2.1, §2.3):\n  %s",
				tc.target, got, tc.timeout, run.line)
		}
		if got, _ := run.flag("count"); got != "1" {
			t.Errorf("`%s` runs with -count %q, want 1: a cached pass is not a run:\n  %s", tc.target, got, run.line)
		}
		if !strings.Contains(recipe, "$(if $(INT_PKGS),,$(error") {
			t.Errorf("`%s` no longer stops on an empty INT_PKGS: `go test -tags integration` "+
				"with no package arguments tests the current directory and succeeds.", tc.target)
		}
	}
	phony := ""
	for _, l := range logicalLines(repoFile(t, "Makefile")) {
		if rest, ok := strings.CutPrefix(l, ".PHONY:"); ok {
			phony += " " + rest
		}
	}
	if !contains(strings.Fields(phony), "test-int-full") {
		t.Error("test-int-full is not in .PHONY: a file of that name would make it a silent no-op")
	}
}

// TestIntegrationDefaults: the sample is a positive integer, the root switch is
// off by default (a bare `make test-int` on a machine without the volume still
// skips, as it always has), and both yield to the command line through `?=`.
func TestIntegrationDefaults(t *testing.T) {
	t.Parallel()
	makefile := repoFile(t, "Makefile")
	for _, name := range []string{"TEST_INT_SAMPLE", "TEST_INT_REQUIRE_ROOT", "SHELF_TEST_ROOT"} {
		if !hasAssignment(makefile, name, "?=") {
			t.Errorf("%s is not assigned with `?=`; the environment and the command line must be able to override it", name)
		}
	}
	if s := makeVar(t, "TEST_INT_SAMPLE"); !regexp.MustCompile(`^[1-9][0-9]*$`).MatchString(s) {
		t.Errorf("`make print-TEST_INT_SAMPLE` is %q, want a positive decimal integer (E-66 §2.2)", s)
	}
	if s := makeVar(t, "TEST_INT_REQUIRE_ROOT"); s != "0" {
		t.Errorf("`make print-TEST_INT_REQUIRE_ROOT` is %q, want 0: only gate 5/5 asks for 1", s)
	}
}

// TestMediaRootLivesInOneFile: scripts/media-root is one absolute path on one
// line, and it is what SHELF_TEST_ROOT defaults to — read by make, not copied.
func TestMediaRootLivesInOneFile(t *testing.T) {
	t.Parallel()
	raw := repoFile(t, "scripts/media-root")
	root, ok := strings.CutSuffix(raw, "\n")
	switch {
	case !ok:
		t.Fatalf("scripts/media-root does not end in a newline: %q", raw)
	case root == "" || strings.ContainsAny(root, "\n\r"):
		t.Fatalf("scripts/media-root must be exactly one non-empty line, got %q", raw)
	case strings.TrimSpace(root) != root:
		t.Fatalf("scripts/media-root has surrounding whitespace, which every reader would "+
			"have to agree to strip (and the path itself holds spaces): %q", raw)
	case !strings.HasPrefix(root, "/"):
		t.Fatalf("scripts/media-root is not an absolute path: %q", root)
	}
	if got := makeVar(t, "SHELF_TEST_ROOT"); got != root {
		t.Errorf("`make print-SHELF_TEST_ROOT` with no environment is %q, want scripts/media-root's %q", got, root)
	}
	// `?=` keeps an override — including an explicitly empty one.
	for _, want := range []string{"/elsewhere/a b", ""} {
		cmd := exec.Command("make", "-s", "--no-print-directory", "print-SHELF_TEST_ROOT")
		cmd.Dir = repoRoot(t)
		cmd.Env = append(makeEnv(), "SHELF_TEST_ROOT="+want)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("make print-SHELF_TEST_ROOT: %v\n%s", err, out)
		}
		if got := strings.TrimRight(string(out), "\n"); got != want {
			t.Errorf("with SHELF_TEST_ROOT=%q in the environment, make reads %q", want, got)
		}
	}
}

// TestMediaRootFoundNextToTheMakefile: the default is read from the
// scripts/media-root beside the Makefile, wherever make is run from — including
// when the Makefile's own path holds spaces. $(MAKEFILE_LIST) is space-separated,
// so `$(lastword …)` cut such a path in half and the default went quietly empty
// (a review reproduced it with `make -f '/tmp/project copy/Makefile'`). The copy
// lives under a directory with two spaces in a row, which a $(strip) would also
// break. The `-f other.mk -f Makefile` case is the fallback for a list of more
// than one makefile, which has no spaces to recover from.
func TestMediaRootFoundNextToTheMakefile(t *testing.T) {
	t.Parallel()
	root := strings.TrimSuffix(repoFile(t, "scripts/media-root"), "\n")
	base := t.TempDir()
	proj := filepath.Join(base, "project  copy")
	if err := os.MkdirAll(filepath.Join(proj, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"Makefile", "scripts/media-root"} {
		if err := os.WriteFile(filepath.Join(proj, rel), []byte(repoFile(t, rel)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	other := filepath.Join(base, "other.mk")
	if err := os.WriteFile(other, []byte("# empty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	elsewhere := t.TempDir()
	for _, tc := range []struct {
		name, dir string
		args      []string
	}{
		{"-f with spaces, from elsewhere", elsewhere, []string{"-f", filepath.Join(proj, "Makefile")}},
		{"-C with spaces", elsewhere, []string{"-C", proj}},
		{"-f other.mk -f the repo Makefile", elsewhere, []string{"-f", other, "-f", filepath.Join(repoRoot(t), "Makefile")}},
	} {
		cmd := exec.Command("make", append(append([]string{"-s", "--no-print-directory"}, tc.args...), "print-SHELF_TEST_ROOT")...)
		cmd.Dir = tc.dir
		cmd.Env = makeEnv()
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Errorf("%s: make print-SHELF_TEST_ROOT: %v\n%s", tc.name, err, out)
			continue
		}
		lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
		if got := lines[len(lines)-1]; got != root {
			t.Errorf("%s: SHELF_TEST_ROOT defaults to %q, want scripts/media-root's %q", tc.name, got, root)
		}
	}
}

// TestNoMediaRootLiteralOutsideItsFile: the Makefile and scripts/*.sh read
// scripts/media-root; none spells the path out (E-66 §2.4). Comment lines are
// exempt. Kept apart from the tests above so that its failure names itself:
// scripts/e2e.sh and scripts/e2e-config.sh are converted in a separate change.
func TestNoMediaRootLiteralOutsideItsFile(t *testing.T) {
	t.Parallel()
	root := strings.TrimSuffix(repoFile(t, "scripts/media-root"), "\n")
	if root == "" {
		t.Fatal("scripts/media-root is empty")
	}
	files := []string{"Makefile"}
	shells, err := filepath.Glob(filepath.Join(repoRoot(t), "scripts", "*.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if len(shells) == 0 {
		t.Fatal("no scripts/*.sh found: the glob is looking in the wrong place")
	}
	for _, p := range shells {
		files = append(files, mustRel(repoRoot(t), p))
	}
	for _, rel := range files {
		for i, line := range strings.Split(repoFile(t, rel), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			if strings.Contains(line, root) {
				t.Errorf("%s:%d spells out the media root instead of reading scripts/media-root:\n  %s",
					rel, i+1, strings.TrimSpace(line))
			}
		}
	}
}

// TestGatesRecipeChecksRootsFirstAndJoinsTheRealRounds reads the recipe: the
// pre-check is its first `$(MAKE)`, and 5/5 is ONE physical line — e2e, then
// test-int with the root required, then the record — so that neither round can
// be dropped while the record stays (the check-gates.sh argument, line for line).
func TestGatesRecipeChecksRootsFirstAndJoinsTheRealRounds(t *testing.T) {
	t.Parallel()
	recipe := commands(section(repoFile(t, "Makefile"), "gates:"))
	if recipe == "" {
		t.Fatal("Makefile has no `gates` target")
	}
	invoke := regexp.MustCompile(`\$\(MAKE\)(?:\s+-\S+)*\s+([A-Za-z0-9_.-]+)`)
	var order []string
	for _, line := range strings.Split(recipe, "\n") {
		for _, m := range invoke.FindAllStringSubmatch(line, -1) {
			order = append(order, m[1])
		}
	}
	if len(order) == 0 || order[0] != "gates-precheck" {
		t.Errorf("the first target `gates` invokes is not gates-precheck (order: %v). "+
			"E-66 §2.5: an unreadable root must stop the run before any gate, not after "+
			"the hour the others take.", order)
	}
	five := regexp.MustCompile(`\$\(MAKE\) --no-print-directory e2e && ` +
		`\$\(MAKE\) --no-print-directory test-int TEST_INT_REQUIRE_ROOT=1 && ` +
		`echo '5/5 [^']*RAN' >> '\$\(GATES_LOG\)'`)
	n := 0
	for _, line := range strings.Split(recipe, "\n") {
		if five.MatchString(line) {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d physical lines of `gates` run `e2e && test-int TEST_INT_REQUIRE_ROOT=1 && "+
			"echo '5/5 … RAN' >> '$(GATES_LOG)'`, want exactly 1 (E-66 §2.1, §2.6):\n%s", n, recipe)
	}
}

// gatesStub writes a stand-in for $(MAKE): it forwards gates-precheck to the
// real make, records every other invocation, and fails the one named by
// $STUB_FAIL. So `make gates MAKE=<stub>` runs the real recipe — order,
// pre-check, `&&` chain, records, banner — and none of the real gates.
func gatesStub(t *testing.T) (stub, invoked string) {
	t.Helper()
	realMake, err := exec.LookPath("make")
	if err != nil {
		t.Fatalf("`make` is not on PATH: %v", err)
	}
	dir := t.TempDir()
	stub = filepath.Join(dir, "make-stub")
	invoked = filepath.Join(dir, "invoked")
	script := "#!/bin/sh\n" +
		"for a in \"$@\"; do [ \"$a\" = gates-precheck ] && exec '" + realMake + "' \"$@\"; done\n" +
		"printf '%s\\n' \"$*\" >> '" + invoked + "'\n" +
		"[ -n \"$STUB_FAIL\" ] && [ \"$*\" = \"$STUB_FAIL\" ] && exit 1\n" +
		"exit 0\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return stub, invoked
}

func TestGatesRefusesUnreadableRootsBeforeAnyGate(t *testing.T) {
	t.Parallel()
	good := t.TempDir()
	missing := filepath.Join(t.TempDir(), "not-there")
	run := func(t *testing.T, fail string, env []string, args ...string) (string, []string, error) {
		t.Helper()
		stub, invoked := gatesStub(t)
		cmd := exec.Command("make", append([]string{"--no-print-directory", "gates", "MAKE=" + stub}, args...)...)
		cmd.Dir = repoRoot(t)
		cmd.Env = append(makeEnv(), append([]string{"TMPDIR=" + t.TempDir(), "STUB_FAIL=" + fail}, env...)...)
		out, err := cmd.CombinedOutput()
		var calls []string
		if data, rerr := os.ReadFile(invoked); rerr == nil {
			calls = strings.Split(strings.TrimRight(string(data), "\n"), "\n")
		}
		return string(out), calls, err
	}
	all := []string{
		"--no-print-directory web-gates",
		"--no-print-directory lint",
		"--no-print-directory test",
		"--no-print-directory e2e-synthetic",
		"--no-print-directory e2e",
		"--no-print-directory test-int TEST_INT_REQUIRE_ROOT=1",
	}

	t.Run("both roots readable: every gate, in order, and a full green", func(t *testing.T) {
		out, calls, err := run(t, "", []string{"SHELF_E2E_ROOT=" + good}, "SHELF_TEST_ROOT="+good, "GATES_E2E=1")
		if err != nil {
			t.Fatalf("make gates (stubbed) failed: %v\n%s", err, out)
		}
		if strings.Join(calls, "\n") != strings.Join(all, "\n") {
			t.Errorf("make gates invoked\n  %q\nwant\n  %q", calls, all)
		}
		if !strings.Contains(out, "All 5 gates ran and passed.") {
			t.Errorf("no full-green banner:\n%s", out)
		}
	})
	// "On" is everything but exactly `0` — for the pre-check and for 5/5 alike.
	// An empty or `0 0` GATES_E2E once switched the sample check off while the
	// recipe still ran 5/5, so the bad sample surfaced after the other four gates.
	for _, e2e := range []string{"1", "", "0 0"} {
		t.Run("GATES_E2E="+e2e+": 5/5 runs, every gate in order", func(t *testing.T) {
			out, calls, err := run(t, "", []string{"SHELF_E2E_ROOT=" + good}, "SHELF_TEST_ROOT="+good, "GATES_E2E="+e2e)
			if err != nil {
				t.Fatalf("make gates GATES_E2E=%q (stubbed) failed: %v\n%s", e2e, err, out)
			}
			if strings.Join(calls, "\n") != strings.Join(all, "\n") {
				t.Errorf("make gates GATES_E2E=%q invoked\n  %q\nwant\n  %q", e2e, calls, all)
			}
		})
		for _, tc := range []struct {
			name string
			env  []string
			args []string
		}{
			{"SHELF_TEST_ROOT missing", []string{"SHELF_E2E_ROOT=" + good}, []string{"SHELF_TEST_ROOT=" + missing}},
			{"SHELF_TEST_ROOT empty", []string{"SHELF_E2E_ROOT=" + good}, []string{"SHELF_TEST_ROOT="}},
			{"SHELF_E2E_ROOT missing", []string{"SHELF_E2E_ROOT=" + missing}, []string{"SHELF_TEST_ROOT=" + good}},
			{"TEST_INT_SAMPLE not a count", []string{"SHELF_E2E_ROOT=" + good},
				[]string{"SHELF_TEST_ROOT=" + good, "TEST_INT_SAMPLE=abc"}},
		} {
			t.Run("GATES_E2E="+e2e+", "+tc.name+": refused before the first gate", func(t *testing.T) {
				out, calls, err := run(t, "", tc.env, append(tc.args, "GATES_E2E="+e2e)...)
				if err == nil {
					t.Errorf("make gates succeeded:\n%s", out)
				}
				if len(calls) != 0 {
					t.Errorf("make gates ran %q before refusing; E-66 §2.5 wants the refusal first:\n%s", calls, out)
				}
			})
		}
	}
	t.Run("GATES_E2E=0: no root needed, 5/5 SKIPPED", func(t *testing.T) {
		out, calls, err := run(t, "", []string{"SHELF_E2E_ROOT=" + missing}, "SHELF_TEST_ROOT="+missing, "GATES_E2E=0")
		if err != nil {
			t.Fatalf("make gates GATES_E2E=0 (stubbed) failed: %v\n%s", err, out)
		}
		if strings.Join(calls, "\n") != strings.Join(all[:4], "\n") {
			t.Errorf("make gates GATES_E2E=0 invoked\n  %q\nwant\n  %q", calls, all[:4])
		}
		if !strings.Contains(out, "SKIPPED") || strings.Contains(out, "All 5 gates ran") {
			t.Errorf("a skipped 5/5 must not read as a full green:\n%s", out)
		}
	})
	// The records live in GATES_LOG, not on stdout: a record written on the way
	// out of a failure (`… || { echo '5/5 … RAN' >> log; exit 1; }`) never reaches
	// the banner, so stdout alone cannot see it. The log is pinned on the command
	// line (which beats the Makefile's `:=`) and read afterwards. A failing gate
	// aborts the recipe before its last line deletes the log, so a missing log is
	// itself the evidence that the recipe ran past the failure.
	record := regexp.MustCompile(`'?[0-9]+/[0-9]+ [^\n']*RAN`)
	for _, fail := range all[4:] {
		t.Run("5/5 is RAN only if both rounds pass: "+fail+" fails", func(t *testing.T) {
			log := filepath.Join(t.TempDir(), "gates.log")
			out, calls, err := run(t, fail, []string{"SHELF_E2E_ROOT=" + good},
				"SHELF_TEST_ROOT="+good, "GATES_E2E=1", "GATES_LOG="+log)
			if err == nil {
				t.Errorf("make gates succeeded although %q failed:\n%s", fail, out)
			}
			if fail == all[4] && contains(calls, all[5]) {
				t.Errorf("test-int ran after e2e failed: the two are not `&&`-joined:\n%q", calls)
			}
			if regexp.MustCompile(`5/5 [^\n]*RAN`).MatchString(out) {
				t.Errorf("5/5 was printed RAN although %q failed:\n%s", fail, out)
			}
			data, rerr := os.ReadFile(log)
			if rerr != nil {
				t.Fatalf("GATES_LOG is gone after %q failed (%v): the recipe went on to its "+
					"cleanup instead of stopping at the failed gate:\n%s", fail, rerr, out)
			}
			var five, others int
			for _, m := range record.FindAllString(string(data), -1) {
				if strings.HasPrefix(strings.TrimPrefix(m, "'"), "5/5") {
					five++
				} else {
					others++
				}
			}
			if five != 0 {
				t.Errorf("GATES_LOG holds %d 5/5 RAN record(s) although %q failed:\n%s", five, fail, data)
			}
			if others != 4 {
				t.Errorf("GATES_LOG holds %d RAN records for gates 1-4, want 4 — is it the log "+
					"`gates` writes?\n%s", others, data)
			}
		})
	}
}

// TestCheckGatesRejectsRecordsAFailureCanReach runs scripts/check-gates.sh on
// a copy of the Makefile with one edit each: a record that a failing gate can
// still reach, or one gate recorded twice. Each must fail the script; the
// unedited copy must pass, so a failure is the edit's and not the copy's.
func TestCheckGatesRejectsRecordsAFailureCanReach(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Fatalf("bash is not on PATH: %v", err)
	}
	makefile := repoFile(t, "Makefile")
	script := repoFile(t, "scripts/check-gates.sh")
	five := "&& echo '5/5  make e2e + test-int (sample)    RAN' >> '$(GATES_LOG)'"
	one := "lint && echo '1/5  make lint                       RAN' >> '$(GATES_LOG)'"
	for _, anchor := range []string{five, one} {
		if strings.Count(makefile, anchor) != 1 {
			t.Fatalf("the Makefile no longer holds exactly one %q; update this test's edits", anchor)
		}
	}
	for _, tc := range []struct{ name, from, to string }{
		{"unedited", "", ""},
		{"a 5/5 record in an || branch", five,
			five + " || { echo '5/5  make e2e + test-int (sample)    RAN' >> '$(GATES_LOG)'; exit 1; }"},
		{"a gate || true, then its record", one,
			"lint || true " + strings.TrimPrefix(one, "lint ")},
		{"a record after `;`, not `&&`", one,
			"lint; " + strings.TrimPrefix(one, "lint && ")},
		{"two 1/5 records on one line", one,
			one + " && echo '1/5  make lint                       RAN' >> '$(GATES_LOG)'"},
	} {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
			t.Fatal(err)
		}
		edited := makefile
		if tc.from != "" {
			edited = strings.Replace(makefile, tc.from, tc.to, 1)
		}
		if err := os.WriteFile(filepath.Join(dir, "Makefile"), []byte(edited), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "scripts", "check-gates.sh"), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command("bash", filepath.Join(dir, "scripts", "check-gates.sh")).CombinedOutput()
		switch {
		case tc.from == "" && err != nil:
			t.Fatalf("check-gates.sh fails on an unedited copy: %v\n%s", err, out)
		case tc.from != "" && err == nil:
			t.Errorf("check-gates.sh accepts %s:\n%s", tc.name, out)
		}
	}
}

// TestIntegrationSampleMustBeACount: make refuses a sample that is not a
// positive decimal integer, rather than handing it to the Go side to reject
// after INT_PKGS compiled — or, for `0`, to read as "nothing".
func TestIntegrationSampleMustBeACount(t *testing.T) {
	t.Parallel()
	dryRun := func(sample string) (string, error) {
		cmd := exec.Command("make", "-n", "--no-print-directory", "test-int", "TEST_INT_SAMPLE="+sample)
		cmd.Dir = repoRoot(t)
		cmd.Env = makeEnv()
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	for _, ok := range []string{"1", "500", "12345"} {
		out, err := dryRun(ok)
		if err != nil {
			t.Errorf("make -n test-int TEST_INT_SAMPLE=%s failed: %v\n%s", ok, err, out)
		} else if !strings.Contains(out, `SHELF_TEST_SAMPLE="`+ok+`"`) {
			t.Errorf("make -n test-int TEST_INT_SAMPLE=%s does not pass it on:\n%s", ok, out)
		}
	}
	for _, bad := range []string{"abc", "0", "007", "", "-1", "1.5", "5 0", "all"} {
		if out, err := dryRun(bad); err == nil || !strings.Contains(out, "TEST_INT_SAMPLE") {
			t.Errorf("make test-int TEST_INT_SAMPLE=%q was not refused by name (err %v):\n%s", bad, err, out)
		}
	}
}
