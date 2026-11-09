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
	"unicode/utf8"
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
	//
	// Both are looked for in ONE logical recipe line, with the exit after the
	// comparison. Matched anywhere in the recipe, each gate's `exit 1` would be
	// vouched for by the other's — and the MEASURED_* line has two more of its
	// own before its comparison (mktemp, the probe build), so deleting its fatal
	// exit would still leave the string in the line. The toolchain is wanted as a
	// comparison, not as a name: its failure message names it too, and replacing
	// the `if` with `if false` left that message — and this test — intact.
	// (The MEASURED_* comparison is written `-le … ||` so that a non-numeric
	// MEASURED_SLACK on the command line fails it instead of skipping it.)
	//
	// What a text match cannot see is whether the exit is still REACHED: setting
	// the drift branch's `bad=1` to `bad=0` leaves this test green. That one is
	// left to running `release` with a wrong MEASURED_* override.
	gates := []struct {
		name, comparison string
		alsoReads        []string
	}{
		{"NFR-OPS-001's size budget", "-gt $(SIZE_BUDGET)", nil},
		{"the MEASURED_* check", "-le $(MEASURED_SLACK)",
			[]string{`!= "$(MEASURED_TOOLCHAIN)"`, "$(MEASURED_DEFAULT)", "$(MEASURED_AVIF)", "$(MEASURED_NOPDF)"}},
	}
	lines := logicalLines(release)
	for _, g := range gates {
		var line string
		for _, l := range lines {
			if strings.Contains(l, g.comparison) {
				line = l
				break
			}
		}
		if line == "" {
			t.Errorf("the `release` recipe no longer compares with %q — %s must fail the "+
				"build, not report and continue", g.comparison, g.name)
			continue
		}
		fatal := strings.LastIndex(line, "exit 1")
		if fatal < strings.Index(line, g.comparison) {
			t.Errorf("%s: no `exit 1` follows %q on its recipe line — the gate reports "+
				"and lets the build succeed", g.name, g.comparison)
			continue
		}
		for _, v := range g.alsoReads {
			if !strings.Contains(line[:fatal], v) {
				t.Errorf("%s no longer checks %s before failing — that figure is "+
					"quoted by the docs and now guarded by nothing", g.name, v)
			}
		}
	}

	// The waiver is per run. A committed 0 would disable the check for every
	// build, the way a committed SIZE_BUDGET of 0 would disable the budget.
	if got := assignment(makefile, "MEASURED_CHECK"); got != "1" {
		t.Errorf("MEASURED_CHECK = %q in the Makefile, want 1: `make release "+
			"MEASURED_CHECK=0` is the waiver, for one run", got)
	}
}

// logicalLines joins a recipe's backslash continuations, so that each element is
// one shell invocation — the unit make runs and judges by its exit status.
func logicalLines(recipe string) []string {
	var out []string
	var cur strings.Builder
	for _, line := range strings.Split(recipe, "\n") {
		trimmed := strings.TrimRight(line, " \t")
		if body, ok := strings.CutSuffix(trimmed, "\\"); ok {
			cur.WriteString(body)
			cur.WriteString(" ")
			continue
		}
		cur.WriteString(trimmed)
		out = append(out, cur.String())
		cur.Reset()
	}
	return out
}

// The measured sizes, pinned against the documents that quote them.
//
// README is held to them in its Build section only, which is where it quotes
// them. Scanning the whole file made every other section's author write sizes
// in units the scan could not see — the first draft after it did exactly that —
// and a check people write around is a check that has stopped watching.
//
// The budget test above guards the number `release` gates on. Nothing guarded
// the numbers the same documents give as measurements, and they rotted:
// `Makefile` and README.md went on saying 25 833 656 B for the default build
// after it had passed 30 MB, and the HANDOFF figure for the budget margin was
// itself measured under a toolchain this machine no longer builds with.
// `release` now re-measures the artefacts against MEASURED_* (it has to build
// them, so that half cannot be a unit test). What can — that every document
// quoting a size quotes MEASURED_* — is here.
var (
	// A byte count written in digit groups, the way the prose writes every
	// release size: "33 239 202". Seven digits at least, so that nothing but a
	// binary size is ever mistaken for one.
	groupedBytes = regexp.MustCompile(`\b\d{1,3}(?:[ \x{00a0}\x{2009},]\d{3}){2,}\b`)
	// A six-digit figure is too common in prose to read as a size on its own
	// ("50 000"), but one written with its unit is a size — and the headroom
	// left under SIZE_BUDGET is exactly that small, and the first to rot.
	unitBytes = regexp.MustCompile(`\b(\d{1,3}[ \x{00a0}\x{2009},]\d{3}) (?:B|bytes)\b`)
	// The 0.1 MB figure README gives a reader next to each byte count.
	decimalMB = regexp.MustCompile(`\b(\d+\.\d) MB\b`)
	toolchain = regexp.MustCompile(`^go\d+\.\d+(\.\d+)?$`)
)

func TestMeasuredSizes_DocsQuoteTheMakefile(t *testing.T) {
	t.Parallel()

	makefile := repoFile(t, "Makefile")
	positive := func(name string) int {
		n, err := strconv.Atoi(assignment(makefile, name))
		if err != nil || n <= 0 {
			t.Fatalf("Makefile has no positive `%s := <bytes>` (got %q); `release` compares "+
				"the built artefact against it", name, assignment(makefile, name))
		}
		return n
	}
	def := positive("MEASURED_DEFAULT")
	avif := positive("MEASURED_AVIF")
	nopdf := positive("MEASURED_NOPDF")
	budget := positive("SIZE_BUDGET")

	// The slack absorbs the -X strings, not drift. Under half README's 0.1 MB
	// step, a build within the slack is at most one step from the MB its figure
	// is quoted as; at that or more it could be two, with `release` green.
	if slack, err := strconv.Atoi(assignment(makefile, "MEASURED_SLACK")); err != nil ||
		slack < 0 || slack >= 50000 {
		t.Errorf("MEASURED_SLACK = %q, want an integer in [0, 50000): it covers the "+
			"length of VERSION and COMMIT, and README rounds to 0.1 MB",
			assignment(makefile, "MEASURED_SLACK"))
	}
	tc := assignment(makefile, "MEASURED_TOOLCHAIN")
	if !toolchain.MatchString(tc) {
		t.Errorf("MEASURED_TOOLCHAIN = %q, want what `go version <file>` prints, e.g. go1.26.9", tc)
	}

	// Every figure a document may quote: the three measurements, the two deltas
	// the prose derives from them, the budget, and the headroom the default
	// build leaves under it — the figure that decides whether the next feature
	// fits, and so the one a stale copy would mislead about most.
	quotable := map[int]string{
		def:          "MEASURED_DEFAULT",
		avif:         "MEASURED_AVIF",
		nopdf:        "MEASURED_NOPDF",
		avif - def:   "MEASURED_AVIF − MEASURED_DEFAULT",
		def - nopdf:  "MEASURED_DEFAULT − MEASURED_NOPDF",
		budget:       "SIZE_BUDGET",
		budget - def: "SIZE_BUDGET − MEASURED_DEFAULT",
	}

	var comments []string
	for _, line := range strings.Split(makefile, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			comments = append(comments, line)
		}
	}
	build := markdownSection(t, repoFile(t, "README.md"), readmeSizesHeading)
	sources := []struct {
		name  string
		lines []string
	}{
		{"the Makefile's comments", comments},
		{"README.md's " + readmeSizesHeading, build},
	}

	for _, src := range sources {
		stated := map[int]bool{}
		for _, line := range src.lines {
			hits := statedSizes(line)
			for _, hit := range hits {
				n, ok := toBytes(hit, "bytes")
				if !ok {
					t.Errorf("%s: unparseable size %q on line:\n  %s", src.name, hit, strings.TrimSpace(line))
					continue
				}
				stated[n] = true
				if _, ok := quotable[n]; !ok {
					t.Errorf("%s quotes %s bytes, which is none of MEASURED_*, their deltas, "+
						"SIZE_BUDGET or the headroom under it:\n  %s\nIf the build was re-measured, this line still "+
						"carries the old figure.", src.name, hit, strings.TrimSpace(line))
				}
			}
		}
		// A document that stops quoting a figure is fine; a scan that silently
		// matches nothing is not. Say so, so the variable can go too.
		for _, n := range []int{def, avif, nopdf} {
			if !stated[n] {
				t.Errorf("%s no longer quotes %s (%d). If that is deliberate and nothing "+
					"else quotes it either, drop the variable and its re-measurement from "+
					"`release` too.", src.name, quotable[n], n)
			}
		}
		if !strings.Contains(strings.Join(src.lines, "\n"), tc) {
			t.Errorf("%s does not say which toolchain its sizes were measured with (%s); "+
				"the same commit differs by 303 KB between go1.25.0 and go1.26.9",
				src.name, tc)
		}
	}

	// README's MB figures are the same numbers rounded, so they rot with them.
	rounded := map[string]bool{}
	for n := range quotable {
		rounded[strconv.FormatFloat(float64(n)/1e6, 'f', 1, 64)] = true
	}
	for _, line := range build {
		for _, hit := range decimalMB.FindAllStringSubmatch(line, -1) {
			if !rounded[hit[1]] {
				t.Errorf("README.md's %s says %s MB, which is no quotable figure rounded to 0.1 MB:\n  %s",
					readmeSizesHeading, hit[1], strings.TrimSpace(line))
			}
		}
	}
}

// statedSizes returns every byte count line states: a figure of seven digits or
// more, grouped in threes, and a six-digit one written with its unit.
func statedSizes(line string) []string {
	hits := groupedBytes.FindAllString(line, -1)
	for _, m := range unitBytes.FindAllStringSubmatchIndex(line, -1) {
		// The tail of a longer figure ("33 444 002 B") is not one: it is
		// joined to the digit before it by exactly one group separator.
		// "in 2026, 109 000 B" is two numbers, and the second still counts.
		if sep, n := utf8.DecodeLastRuneInString(line[:m[2]]); n > 0 && strings.ContainsRune(" \u00a0\u2009,", sep) {
			if d, _ := utf8.DecodeLastRuneInString(line[:m[2]-n]); d >= '0' && d <= '9' {
				continue
			}
		}
		hits = append(hits, line[m[2]:m[3]])
	}
	return hits
}

func TestStatedSizes(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		line string
		want []string
	}{
		{"linux/amd64 (33 444 002 B, go1.26.9)", []string{"33 444 002"}},
		{"leaves **110 430 B** (0.1 MB) of it", []string{"110 430"}},
		{"leaves 110 430 bytes", []string{"110 430"}},
		{"Measured in 2026, 109 000 B remains", []string{"109 000"}},
		{"33 444 002 B and 110 430 B", []string{"33 444 002", "110 430"}},
		{"it must stay under 50 000", nil},
		{"the same commit is 303 KB smaller", nil},
	} {
		if got := statedSizes(c.line); strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("statedSizes(%q) = %q, want %q", c.line, got, c.want)
		}
	}
}

// readmeSizesHeading is the README section that quotes the measured sizes.
const readmeSizesHeading = "## Build"

// markdownSection returns the lines under heading, up to the next heading of the
// same level. Fence-aware, because a code block in that section may well hold a
// line that starts with "## " (the Makefile's own help comments do).
//
// A missing heading is fatal rather than an empty section: renaming "## Build"
// would otherwise turn the scan into one that reads nothing and passes.
func markdownSection(t *testing.T, doc, heading string) []string {
	t.Helper()
	level := heading[:strings.IndexByte(heading, ' ')+1]
	lines := strings.Split(doc, "\n")
	start := -1
	for i, line := range lines {
		if strings.TrimRight(line, " \t") == heading {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("README.md has no %q heading. It is where the measured release sizes are "+
			"quoted, so the measured-size scan reads that section; if it was renamed, "+
			"rename readmeSizesHeading with it.", heading)
	}
	fence := "" // the open fence's marker, "```" or "~~~"; "" outside one
	for i := start + 1; i < len(lines); i++ {
		if marker, ok := fenceMarker(lines[i]); ok {
			switch fence {
			case "":
				fence = marker
			case marker:
				fence = ""
			}
			continue
		}
		if fence == "" && strings.HasPrefix(lines[i], level) {
			return lines[start:i]
		}
	}
	return lines[start:]
}

// fenceMarker reports whether line opens or closes a fenced code block, the way
// CommonMark draws one: "```" or "~~~", indented by at most three spaces. A
// "~~~" block reading as prose let a "## " line inside it end the section early,
// and an old figure after that block went unscanned.
func fenceMarker(line string) (string, bool) {
	body := strings.TrimLeft(line, " ")
	if len(line)-len(body) > 3 {
		return "", false
	}
	for _, m := range []string{"```", "~~~"} {
		if strings.HasPrefix(body, m) {
			return m, true
		}
	}
	return "", false
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
