package buildinfo_test

// The NFR-OPS-001 release-size gate, pinned against the documents that state it.
//
// Round 3 of the end-to-end acceptance run failed `make release` on a budget
// (`≤ 20 MB`) that the 필수 feature set makes unreachable: the linux/amd64
// binary is 27,418,916 B and the only two levers below it are `-tags nopdf`
// (FR-SRV-006, and therefore AC-004) and `-tags noavif` (FR-IDX-011). Ruling
// **E-19** re-derived the number from a measurement — 28 MiB, deliberately
// *less* than the cost of either tag — and impl-plan §7.3 now carries it.
//
// What this test protects is not the value but the two ways the fix could rot:
//
//  1. The number lives in more than one file. If `Makefile`'s `SIZE_BUDGET` and
//     the impl-plan drift apart, `make release` is once again gating on
//     something no document agrees with — which is exactly how 20 MB survived
//     three acceptance rounds. (Written for §7.3 alone, this test immediately
//     found a *third* statement of the budget in WP-13 acceptance 5, so it now
//     checks every line of the plan that mentions `make release`. A later review
//     found a *fourth* in `README.md`, guarded by nothing — changing it to
//     64 MiB left the whole suite green — so README is in the scan too, keyed on
//     `SIZE_BUDGET` because that is the word its sentence is built around.)
//  2. A red gate is easy to make green by deleting it. The comparison and the
//     `exit 1` it reaches must still be in the recipe: a budget that only
//     prints is not a budget.
//
// It is a unit test rather than part of `make release` because the failure it
// catches — a documentation/build disagreement — is textual, and re-linking
// seven binaries to discover it would be the slowest possible tier.

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// repoFile reads a path relative to the repository root.
//
// The test binary's working directory is the package directory, so the root is
// two levels up. Verified rather than assumed, so a future move of this file
// fails loudly instead of silently reading nothing.
func repoFile(t *testing.T, rel string) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolving the repository root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("%s is not the repository root (no go.mod): %v", root, err)
	}
	data, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("reading %s: %v", rel, err)
	}
	return string(data)
}

var (
	makefileBudget = regexp.MustCompile(`(?m)^SIZE_BUDGET\s*:=\s*(\d+)\s*$`)
	// Every size a document states for a release build, in whatever unit it is
	// written. The prose groups digits with thin spaces for readability, so the
	// separators come out before the number is parsed.
	//
	// Both `≤` and `=` introduce a figure. The docs like to state the same
	// budget twice on one line — "≤ **32 MiB = 33 554 432 bytes**" — and a
	// pattern that took only the `≤` half let the redundant figure drift away
	// from its own sentence: mutating just the byte count to 99 999 999 left the
	// suite green. Two statements of one number are two things to keep honest.
	planBudget = regexp.MustCompile("[≤=]\\s*\\*{0,2}([0-9\u00a0\u2009 ,.]+?)\\s*\\*{0,2}(MiB|MB|bytes)")
)

// budgetSources are the documents that restate `SIZE_BUDGET`, and the word that
// marks a line as talking about it. Every figure on such a line has to agree
// with the Makefile.
//
// README is here because a review found the fourth statement of the number
// living there behind no guard at all — changed to 64 MiB, everything stayed
// green. `docs/decisions.md` deliberately is NOT: E-19 and E-21 are the rulings
// that FIXED the value, and their tables are a record of what was decided on
// the day, not a copy that has to track the Makefile.
var budgetSources = []struct{ path, onLinesContaining string }{
	{filepath.Join("docs", "impl-plan.md"), "make release"},
	{"README.md", "SIZE_BUDGET"},
}

func TestReleaseSizeBudget_MakefileAndDocsAgree(t *testing.T) {
	t.Parallel()

	makefile := repoFile(t, "Makefile")
	m := makefileBudget.FindStringSubmatch(makefile)
	if m == nil {
		t.Fatal("Makefile has no `SIZE_BUDGET := <bytes>` assignment; " +
			"NFR-OPS-001's gate is what it configures")
	}
	budget, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("SIZE_BUDGET is not an integer: %v", err)
	}
	if budget <= 0 {
		t.Fatalf("SIZE_BUDGET = %d: a non-positive default disables the gate for every "+
			"build, which `make release SIZE_BUDGET=0` already offers per run", budget)
	}

	for _, src := range budgetSources {
		doc := repoFile(t, src.path)
		stated := 0
		for _, line := range strings.Split(doc, "\n") {
			if !strings.Contains(line, src.onLinesContaining) {
				continue
			}
			for _, hit := range planBudget.FindAllStringSubmatch(line, -1) {
				got, ok := toBytes(hit[1], hit[2])
				if !ok {
					t.Errorf("%s states an unparseable release size %q %s:\n  %s",
						src.path, hit[1], hit[2], strings.TrimSpace(line))
					continue
				}
				stated++
				if got != budget {
					t.Errorf("release-size budget disagrees with the Makefile:\n"+
						"  Makefile SIZE_BUDGET = %d\n"+
						"  %s says = %d (%q %s)\n"+
						"  on line              : %s\n"+
						"Ruling E-19 fixed one number; every place that states it has to move "+
						"together, or `make release` gates on something no document claims.",
						budget, src.path, got, hit[1], hit[2], strings.TrimSpace(line))
				}
			}
		}
		if stated == 0 {
			t.Errorf("%s no longer states a release-size budget on any line containing %q. "+
				"impl-plan §7.3's first definition-of-done checkbox is what `SIZE_BUDGET` "+
				"implements, and README is where an operator reads what `make release` will "+
				"refuse. If the figure was deliberately removed from this file, remove it "+
				"from budgetSources too — do not leave a scan that matches nothing.",
				src.path, src.onLinesContaining)
		}
	}
}

// toBytes converts one of the plan's size figures to bytes.
func toBytes(digits, unit string) (int, bool) {
	clean := strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\u00a0', '\u2009', ',', '.':
			return -1
		}
		return r
	}, digits)
	n, err := strconv.Atoi(clean)
	if err != nil {
		return 0, false
	}
	switch unit {
	case "bytes":
		return n, true
	case "MiB":
		return n * 1024 * 1024, true
	case "MB":
		return n * 1000 * 1000, true
	}
	return 0, false
}

func TestReleaseSizeBudget_GateStillFailsTheBuild(t *testing.T) {
	t.Parallel()

	makefile := repoFile(t, "Makefile")
	// commands() (staticlink_test.go) strips the recipe's comments, so a comment
	// that merely discusses `exit 1` cannot stand in for one.
	release := commands(section(makefile, "release:"))
	if release == "" {
		t.Fatal("Makefile has no `release` target")
	}

	// The comparison and the fatal exit are separate rot paths: dropping the
	// `exit 1` turns the gate into a printout, and dropping the comparison
	// turns it into nothing at all.
	for _, want := range []string{"SIZE_BUDGET", "-gt", "exit 1"} {
		if !strings.Contains(release, want) {
			t.Errorf("the `release` recipe no longer contains %q — NFR-OPS-001's size gate "+
				"must fail the build, not report and continue", want)
		}
	}
}

// section returns the recipe of the target whose line starts with prefix, up to
// the next target definition.
func section(makefile, prefix string) string {
	lines := strings.Split(makefile, "\n")
	start := -1
	for i, line := range lines {
		if strings.HasPrefix(line, prefix) {
			start = i
			break
		}
	}
	if start < 0 {
		return ""
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		line := lines[i]
		if line == "" || strings.HasPrefix(line, "\t") || strings.HasPrefix(line, "#") ||
			strings.HasPrefix(line, " ") {
			continue
		}
		if strings.Contains(line, ":") {
			end = i
			break
		}
	}
	return strings.Join(lines[start:end], "\n")
}
