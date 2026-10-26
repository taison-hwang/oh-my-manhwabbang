package buildinfo_test

// Guards on WHICH test files the Makefile's targets actually compile.
//
// A `//go:build` line is a filter on what `go test` sees, so a tag no target
// passes makes its files dead weight that every gate reports green around. Two
// such sets were found by audit, not by any failure:
//
//   - `integration`. test-int ran `./integration/...` only, so the
//     `//go:build integration` files in internal/archive/{zipidx,rar4,hv3} and
//     internal/scanner's incremental-ratio test (which needs no media volume at
//     all) were compiled by nothing.
//   - `nopdf`. A supported build configuration (the release recipe measures it)
//     whose stub tests — internal/pdfium/pool_stub_test.go,
//     internal/source/{pdfsource_stub_test.go,encoding_nopdf_test.go} — no pass
//     ran. Deleting the rounding in pdfium/pool_stub.go is red ONLY there, and
//     was a surviving mutation.
//
// The Makefile now derives both package lists (INT_PKGS, NOPDF_PKGS) from the
// source, so a new tagged file joins its target without anyone remembering to.
// These tests hold the derivation to an INDEPENDENT oracle: go/build's own
// MatchFile, which applies the real constraint grammar, the file-name GOOS/GOARCH
// rules and the header-only placement rule, where the Makefile has a grep. They
// are compared twice — on the repository (is today's list complete?) and on a
// synthetic tree of the shapes a grep gets wrong (`!integration`, testdata/,
// dot-directories), because the repository contains none of
// those today and a comparison over it alone would let a broken regexp agree.

import (
	"fmt"
	"go/build"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// fileMatcher answers whether the go tool would compile dir/name in some build.
type fileMatcher func(dir, name string) (bool, error)

// compiles is MatchFile under the host GOOS/GOARCH and release tags, with exactly
// the given build tags and cgo setting. MatchFile — not a hand-rolled
// constraint.Expr evaluation — so that the oracle shares no code, and no
// simplification, with the Makefile's grep.
func compiles(dir, name string, cgo bool, tags ...string) (bool, error) {
	ctx := build.Default
	ctx.CgoEnabled = cgo
	ctx.BuildTags = tags
	return ctx.MatchFile(dir, name)
}

// requiresIntegration is the INT_PKGS rule: test-int's build (CGO_ENABLED=0,
// `-tags integration`) compiles the file and the untagged build does not. So
// `integration` and `integration && !race` are in — race is not a build tag in
// either build, test-int never sets it — and `!integration` is out. A file
// constrained `integration || foo` is in: no target sets `foo`, so test-int is
// the only build that compiles it. Untagged files are out; they are the unit
// suite, which test-int reruns as a side effect but does not exist for.
func requiresIntegration(dir, name string) (bool, error) {
	with, err := compiles(dir, name, false, "integration")
	if err != nil {
		return false, err
	}
	without, err := compiles(dir, name, false)
	return with && !without, err
}

// dependsOnNopdf is the NOPDF_PKGS rule: adding `nopdf` to a configuration pass
// 3 can be run in changes whether the file is compiled, in EITHER direction. A
// package holding a `!nopdf` file is in even if it holds no `nopdf` one
// (internal/httpapi today): under nopdf it is a different package — the PDF
// tests drop out — and that configuration is what pass 3 exists to run.
//
// The baseline is NOT the empty tag set. Pass 3 runs `-tags "$(TAGS) nopdf"
// $(RACE)` with CGO_ENABLED=1, so a file constrained `nopdf && noavif` or
// `nopdf && race` is compiled there and nowhere else, and a bare-`nopdf` oracle
// dropped exactly those. The list is a `:=` that cannot see TAGS or RACE, both
// `?=` operator knobs, so it is the union over what they reach: each of tagSets
// (the default TAGS, and AVIF_TAGS — the `-avif` release variant's set, which
// `make release TAGS="noavif nopdf"` builds with nopdf too) with and without
// `race` (`make RACE=` is the documented no-C-compiler escape hatch). Keeping the
// untagged comparison is therefore not leniency: it IS the AVIF_TAGS baseline
// today, and dropping it would make `nopdf || noavif` a package the grep lists
// and the oracle calls spurious, for a configuration the Makefile does build.
func dependsOnNopdf(tagSets ...[]string) fileMatcher {
	return func(dir, name string) (bool, error) {
		for _, tags := range tagSets {
			for _, race := range []bool{true, false} {
				base := append([]string(nil), tags...)
				if race {
					base = append(base, "race")
				}
				without, err := compiles(dir, name, true, base...)
				if err != nil {
					return false, err
				}
				with, err := compiles(dir, name, true, append(base, "nopdf")...)
				if err != nil {
					return false, err
				}
				if with != without {
					return true, nil
				}
			}
		}
		return false, nil
	}
}

// pkgsWhere walks root the way `go test ./...` does and returns, sorted, the
// `./dir` of every package directory holding a .go file that match accepts.
// Like the go tool it skips testdata/, names starting with `.` or `_`, vendor/
// and nested modules (a directory with its own go.mod is not part of `./...`);
// node_modules/ is skipped too, because pnpm puts third-party .go files there.
func pkgsWhere(root string, match fileMatcher) ([]string, error) {
	set := map[string]bool{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if p == root {
				return nil
			}
			if name == "testdata" || name == "vendor" || name == "node_modules" ||
				strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") ||
				fileExists(filepath.Join(p, "go.mod")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") {
			return nil
		}
		// The go tool compiles a symlink to a file (and skips one to a directory);
		// MatchFile opens through the link.
		if d.Type()&fs.ModeSymlink != 0 {
			fi, err := os.Stat(p)
			if err != nil || !fi.Mode().IsRegular() {
				return nil
			}
		} else if !d.Type().IsRegular() {
			return nil
		}
		dir := filepath.Dir(p)
		ok, err := match(dir, name)
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		if ok {
			rel, err := filepath.Rel(root, dir)
			if err != nil {
				return err
			}
			if rel == "." {
				set["."] = true
			} else {
				set["./"+filepath.ToSlash(rel)] = true
			}
		}
		return nil
	})
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out, err
}

// makeVarIn is makeVar (staticlink_test.go) run in dir against the repository's
// Makefile, so the derivations — which search `.` — search dir instead. Anything
// on stderr fails it: $(shell) discards exit statuses, so a find or grep that
// complains (a symlinked directory named *.go, without `grep -d skip`) is
// otherwise noise on every make run that no list comparison can see.
func makeVarIn(t *testing.T, dir, name string) string {
	t.Helper()
	cmd := exec.Command("make", "-s", "--no-print-directory",
		"-f", filepath.Join(repoRoot(t), "Makefile"), "print-"+name)
	cmd.Dir = dir
	cmd.Env = makeEnv()
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("`make -f Makefile print-%s` in %s failed: %v\n%s%s", name, dir, err, out, stderr.String())
	}
	if stderr.Len() > 0 {
		t.Errorf("`make -f Makefile print-%s` in %s wrote to stderr:\n%s", name, dir, stderr.String())
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

func sortedFields(s string) []string {
	f := strings.Fields(s)
	sort.Strings(f)
	return f
}

type packageList struct {
	variable string
	match    fileMatcher
	why      string
}

// packageLists is what the comparisons iterate over. The nopdf baselines are
// make's own TAGS and AVIF_TAGS, not literals, for the reason makeVar gives.
func packageLists(t *testing.T) []packageList {
	t.Helper()
	return []packageList{
		{"INT_PKGS", requiresIntegration,
			"`make test-int` runs exactly this list with `-tags integration`; a package " +
				"missing from it has `//go:build integration` files that no target compiles"},
		{"NOPDF_PKGS", dependsOnNopdf(strings.Fields(makeVar(t, "TAGS")), strings.Fields(makeVar(t, "AVIF_TAGS"))),
			"pass 3 of `make test` runs exactly this list with `-tags \"$(TAGS) nopdf\"`; " +
				"a package missing from it has nopdf-only tests that no pass runs"},
	}
}

// listMismatch is the comparison every test below fails on: "" when make's list
// and the oracle's are the same set, otherwise both differences by name. It is
// the reason a grep that misreads a constraint costs a red test and never a
// silently dropped package (TestTaggedPackageListsDisagreeLoudlyWhereTheGrepIsWrong).
func listMismatch(oracle, made []string) string {
	in := func(list []string) map[string]bool {
		m := map[string]bool{}
		for _, s := range list {
			m[s] = true
		}
		return m
	}
	o, m := in(oracle), in(made)
	var missing, extra []string
	for _, s := range oracle {
		if !m[s] {
			missing = append(missing, s)
		}
	}
	for _, s := range made {
		if !o[s] {
			extra = append(extra, s)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) == 0 && len(extra) == 0 {
		return ""
	}
	return fmt.Sprintf("  want %v\n  got  %v\n  missing from make %v\n  make-only %v",
		oracle, made, missing, extra)
}

// TestTaggedPackageListsCoverTheRepository: the Makefile's lists, expanded by make,
// equal the oracle's walk of this repository.
func TestTaggedPackageListsCoverTheRepository(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, tc := range packageLists(t) {
		want, err := pkgsWhere(root, tc.match)
		if err != nil {
			t.Fatalf("walking %s: %v", root, err)
		}
		if len(want) == 0 {
			t.Errorf("the oracle finds no package for %s at all; either the tag is gone "+
				"from the tree (then retire the variable and its pass) or the walk is broken", tc.variable)
			continue
		}
		if diff := listMismatch(want, sortedFields(makeVar(t, tc.variable))); diff != "" {
			t.Errorf("`make print-%s` is not the set the go tool's own constraint matcher finds:\n"+
				"%s\n%s. Keep the list DERIVED from the build tags in the "+
				"Makefile rather than written out, so a new tagged file joins by itself.",
				tc.variable, diff, tc.why)
		}
	}
}

// TestTaggedPackageListsAgreeOnASyntheticTree runs the Makefile's derivations over
// a tree built from the shapes a text search gets wrong, and holds them to the
// oracle there. The repository has no `!integration` file, no `integrationx`
// tag and no tagged file under testdata/, so without this a derivation that
// matched all three would still pass the comparison above. The NOPDF rows are
// written against TAGS=noavif (ruling E-21), so the test refuses any other
// default rather than pass on rows that no longer mean what they say.
func TestTaggedPackageListsAgreeOnASyntheticTree(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("make"); err != nil {
		t.Fatalf("`make` is not on PATH: %v", err)
	}
	if tags := makeVar(t, "TAGS"); tags != "noavif" {
		t.Fatalf("`make print-TAGS` is %q; the synthetic nopdf rows assume noavif", tags)
	}
	tree := t.TempDir()
	files := map[string]string{
		// INT_PKGS
		"in/plain/a_test.go":             "//go:build integration\n\npackage plain\n",
		"in/norace/b_test.go":            "//go:build integration && !race\n\npackage norace\n",
		"in/either/c_test.go":            "//go:build integration || neverset\n\npackage either\n",
		"in/commented/d_test.go":         "// Copyright line first.\n\n//go:build integration\n\npackage commented\n",
		"out/negated/e_test.go":          "//go:build !integration\n\npackage negated\n",
		"out/negated/e2_test.go":         "//go:build !race && !integration\n\npackage negated\n",
		"out/unconstrained/f.go":         "package unconstrained\n",
		"out/suffix/g_test.go":           "//go:build integrationx\n\npackage suffix\n",
		"out/fixture/testdata/h_test.go": "//go:build integration\n\npackage fixture\n",
		"out/.hidden/i_test.go":          "//go:build integration\n\npackage hidden\n",
		"out/_scratch/j_test.go":         "//go:build integration\n\npackage scratch\n",
		"out/web/node_modules/m/l.go":    "//go:build integration\n\npackage m\n",
		// go/build trims each header line, so indentation does not hide a constraint.
		"in/indented/n_test.go": "\t  //go:build integration\n\npackage indented\n",
		// A nested module is not part of `./...`; the ROOT go.mod (below) is not one.
		"go.mod":                   "module synthetic\n",
		"out/nested/go.mod":        "module nested\n",
		"out/nested/sub/k_test.go": "//go:build integration\n\npackage sub\n",
		// The target of in/linked/extra_test.go (see links); _shared/ is pruned.
		"_shared/extra.txt": "//go:build integration\n\npackage linked\n",
		// NOPDF_PKGS
		"pdf/stub/s.go":          "//go:build nopdf\n\npackage stub\n",
		"pdf/real/r.go":          "//go:build !nopdf\n\npackage real\n",
		"pdf/compound/q_test.go": "//go:build !nopdf && !race\n\npackage compound\n",
		"pdf/unrelated/u.go":     "//go:build nopdfx\n\npackage unrelated\n",
		"pdf/indented/o.go":      "  //go:build nopdf\n\npackage indented\n",
		// Pass 3 runs `-tags "$(TAGS) nopdf" $(RACE)`, not bare `-tags nopdf`: these
		// two are compiled ONLY in that configuration and nowhere else.
		"pdf/withavif/v_test.go": "//go:build nopdf && noavif\n\npackage withavif\n",
		"pdf/withrace/w_test.go": "//go:build nopdf && race\n\npackage withrace\n",
		// Changed by nopdf only against AVIF_TAGS, or only with RACE= (see
		// dependsOnNopdf for why both baselines count).
		"pdf/eitheravif/x.go": "//go:build nopdf || noavif\n\npackage eitheravif\n",
		"pdf/eitherrace/y.go": "//go:build nopdf || race\n\npackage eitherrace\n",
	}
	// The go tool compiles a symlinked .go file and ignores a symlinked directory.
	links := map[string]string{
		"in/linked/extra_test.go": "../../_shared/extra.txt",
		"out/dirlink/z.go":        "../../_shared",
	}
	for rel, body := range files {
		p := filepath.Join(tree, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for rel, target := range links {
		p := filepath.Join(tree, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
	}

	// The oracle itself, pinned by hand: if THIS is wrong the comparison is moot.
	oracle := map[string][]string{
		"INT_PKGS": {"./in/commented", "./in/either", "./in/indented", "./in/linked",
			"./in/norace", "./in/plain"},
		"NOPDF_PKGS": {"./pdf/compound", "./pdf/eitheravif", "./pdf/eitherrace",
			"./pdf/indented", "./pdf/real", "./pdf/stub", "./pdf/withavif", "./pdf/withrace"},
	}
	for _, tc := range packageLists(t) {
		walked, err := pkgsWhere(tree, tc.match)
		if err != nil {
			t.Fatalf("walking the synthetic tree: %v", err)
		}
		if strings.Join(walked, " ") != strings.Join(oracle[tc.variable], " ") {
			t.Errorf("the %s oracle walks the synthetic tree to\n  %v\nwant\n  %v",
				tc.variable, walked, oracle[tc.variable])
			continue
		}
		if diff := listMismatch(walked, sortedFields(makeVarIn(t, tree, tc.variable))); diff != "" {
			t.Errorf("the Makefile's %s derivation disagrees with the go tool on a "+
				"synthetic tree:\n%s\n"+
				"The tree holds the shapes a text search gets wrong — a negated tag, a "+
				"longer tag with the same prefix, a constraint after a comment or after "+
				"indentation, testdata/, dot- and underscore-directories, node_modules/, a "+
				"nested module, a symlinked file. %s.",
				tc.variable, diff, tc.why)
		}
	}
}

// TestTaggedPackageListsDisagreeLoudlyWhereTheGrepIsWrong pins the claim the
// Makefile's derivation rests on. Its greps are not a constraint parser and must
// not become one: `!(integration)` reads to them as integration REQUIRED, and
// `!(!integration)` as negated. The guard above is the semantic authority, so
// such a file has to show up as a disagreement — here, both directions at once —
// and never as a package one side quietly drops.
func TestTaggedPackageListsDisagreeLoudlyWhereTheGrepIsWrong(t *testing.T) {
	t.Parallel()
	tree := t.TempDir()
	for rel, body := range map[string]string{
		"paren/a_test.go":  "//go:build !(integration)\n\npackage paren\n",
		"double/b_test.go": "//go:build !(!integration)\n\npackage double\n",
	} {
		p := filepath.Join(tree, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	walked, err := pkgsWhere(tree, requiresIntegration)
	if err != nil {
		t.Fatal(err)
	}
	made := sortedFields(makeVarIn(t, tree, "INT_PKGS"))
	diff := listMismatch(walked, made)
	if diff == "" {
		t.Fatalf("make and the oracle AGREE on parenthesised negations (both %v), so this "+
			"test no longer demonstrates that a grep misreading reaches the comparison; "+
			"pick a shape the grep still misreads", walked)
	}
	if !strings.Contains(diff, "missing from make [./double]") || !strings.Contains(diff, "make-only [./paren]") {
		t.Errorf("the mismatch report does not name both directions:\n%s", diff)
	}
}

// goRun is one logical recipe line (release_budget_test.go: backslash
// continuations joined) that RUNS `$(GO) <sub>`, split into words as sh would.
//
// Substrings of a line prove nothing about execution: `@echo '$(GO) test …'`
// contains every one of them, and so does a real line with `-run '^$'` appended,
// which makes `go test` exit 0 having run no test at all. goRuns therefore
// checks what the line is — one plain command whose first word starts the go
// tool — and assertions read its words, not its text.
type goRun struct {
	line string
	args []string // the words after the subcommand, quotes removed
}

// valueFlags take the next word as their value when written without `=`.
var valueFlags = map[string]bool{
	"tags": true, "timeout": true, "p": true, "parallel": true, "count": true,
	"cpu": true, "run": true, "skip": true, "list": true, "bench": true,
	"benchtime": true, "fuzz": true, "fuzztime": true, "exec": true, "o": true,
	"ldflags": true, "gcflags": true, "mod": true, "coverprofile": true,
}

// selectingFlags make `go test` execute fewer tests than it compiles, or none:
// `-run '^$'`, `-skip .` and `-list .` all exit 0. -bench and -fuzz change what
// the run is for, -short what each test does. None belongs on a gate.
var selectingFlags = map[string]bool{
	"run": true, "skip": true, "list": true, "bench": true, "fuzz": true, "short": true,
}

// parseFlag reads a word as a flag in every spelling the go tool accepts:
// `-run`, `--run`, `-run=x`, and the test binary's own `-test.run`.
func parseFlag(word string) (name, value string, hasValue, ok bool) {
	if len(word) < 2 || word[0] != '-' {
		return "", "", false, false
	}
	name, value, hasValue = strings.Cut(strings.TrimPrefix(word[1:], "-"), "=")
	return strings.TrimPrefix(name, "test."), value, hasValue, true
}

// flag returns the value of -name, written `-name v` or `-name=v`.
func (r goRun) flag(name string) (string, bool) {
	for i := 0; i < len(r.args); i++ {
		n, v, hasValue, ok := parseFlag(r.args[i])
		if !ok || n != name {
			continue
		}
		if !hasValue && i+1 < len(r.args) {
			v = r.args[i+1]
		}
		return v, true
	}
	return "", false
}

// packages is the positional words: what the go tool takes as package patterns.
// `$(RACE)` is a word make expands to a flag (`-race`, or nothing), not one.
func (r goRun) packages() string {
	var out []string
	for i := 0; i < len(r.args); i++ {
		n, _, hasValue, ok := parseFlag(r.args[i])
		if r.args[i] == "$(RACE)" {
			continue
		}
		if !ok {
			out = append(out, r.args[i])
			continue
		}
		if n == "args" {
			break
		}
		if valueFlags[n] && !hasValue {
			i++
		}
	}
	return strings.Join(out, " ")
}

func (r goRun) has(word string) bool { return contains(r.args, word) }

// shellWords splits one command line into words the way sh does for the simple
// quoting a recipe uses, and refuses anything that is not ONE command: `;`, `&&`,
// `||`, `|`, `&` and backquotes hand the line's exit status to something else
// (`… || true`), and make judges the line by that status alone.
func shellWords(s string) ([]string, error) {
	var words []string
	var cur strings.Builder
	inWord := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == ' ' || c == '\t':
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
		case c == '\'':
			j := strings.IndexByte(s[i+1:], '\'')
			if j < 0 {
				return nil, fmt.Errorf("unterminated '")
			}
			cur.WriteString(s[i+1 : i+1+j])
			i += j + 1
			inWord = true
		case c == '"':
			inWord = true
			for i++; i < len(s) && s[i] != '"'; i++ {
				if s[i] == '\\' && i+1 < len(s) {
					i++
				}
				cur.WriteByte(s[i])
			}
			if i >= len(s) {
				return nil, fmt.Errorf("unterminated \"")
			}
		case c == '\\' && i+1 < len(s):
			i++
			cur.WriteByte(s[i])
			inWord = true
		case strings.IndexByte(";&|`", c) >= 0:
			return nil, fmt.Errorf("an unquoted %q: it is not one plain command, so its "+
				"exit status need not be the go tool's", c)
		default:
			cur.WriteByte(c)
			inWord = true
		}
	}
	if inWord {
		words = append(words, cur.String())
	}
	return words, nil
}

// envWord is a word sh treats as an environment assignment for the command.
func envWord(w string) bool {
	if w == "$(GOENV)" || w == "$(BUILDENV)" {
		return true
	}
	name, _, ok := strings.Cut(w, "=")
	if !ok || name == "" {
		return false
	}
	for i, c := range name {
		if !(c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || i > 0 && c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// goRuns returns, parsed, every line of recipe (pass it commands(…)) that
// mentions `$(GO) <sub>`, and a problem for each such line that does not plainly
// run it. A problem line is not in runs, so a count check fails on it too.
func goRuns(recipe, sub string) (runs []goRun, problems []string) {
	for _, line := range logicalLines(recipe) {
		if !strings.Contains(line, "$(GO)") {
			continue
		}
		// make strips these before the shell sees the line. `-` ignores the exit
		// status, so on a gate it is a failure that cannot fail.
		body := strings.TrimLeft(line, " \t")
		ignored := false
		for body != "" && strings.IndexByte("@+-", body[0]) >= 0 {
			ignored = ignored || body[0] == '-'
			body = strings.TrimLeft(body[1:], " \t")
		}
		words, err := shellWords(body)
		mentioned := strings.Contains(line, "$(GO) "+sub)
		for i := 0; err == nil && i+1 < len(words); i++ {
			mentioned = mentioned || words[i] == "$(GO)" && words[i+1] == sub
		}
		if !mentioned {
			continue
		}
		bad := func(format string, a ...any) {
			problems = append(problems, fmt.Sprintf("%s\n    "+format, append([]any{line}, a...)...))
		}
		switch {
		case err != nil:
			bad("%v", err)
			continue
		case ignored:
			bad("carries make's `-` prefix, which ignores the command's exit status")
			continue
		case len(words) == 0 || words[0] != "$(GOENV)" && words[0] != "$(BUILDENV)" && words[0] != "$(GO)":
			first := ""
			if len(words) > 0 {
				first = words[0]
			}
			bad("starts with %q, not $(GOENV), $(BUILDENV) or $(GO): it does not run the go tool", first)
			continue
		}
		i := 0
		for i < len(words) && envWord(words[i]) {
			i++
		}
		if i+1 >= len(words) || words[i] != "$(GO)" || words[i+1] != sub {
			bad("after its environment it runs %q, not `$(GO) %s`", strings.Join(words[i:min(i+2, len(words))], " "), sub)
			continue
		}
		run := goRun{line: line, args: words[i+2:]}
		if sub == "test" {
			// GOFLAGS is read by the go tool too, so its value is searched as well.
			search := append([]string(nil), run.args...)
			for _, w := range words[:i] {
				if _, v, ok := strings.Cut(w, "="); ok {
					search = append(search, strings.Fields(v)...)
				}
			}
			selecting := ""
			for _, w := range search {
				if n, _, _, ok := parseFlag(w); ok && selectingFlags[n] {
					selecting = w
					break
				}
			}
			if selecting != "" {
				bad("selects which tests run (%s). `-run '^$'` and its kin make `go test` exit "+
					"0 having executed nothing, so a gate carrying one can be green unrun", selecting)
				continue
			}
		}
		runs = append(runs, run)
	}
	return runs, problems
}

// TestGoRunsRejectsLinesThatDoNotRunTheTests holds goRuns to the shapes that
// defeated the substring assertions it replaced: each of these mentions
// everything a pass is checked for, and runs nothing, or runs it unjudged.
func TestGoRunsRejectsLinesThatDoNotRunTheTests(t *testing.T) {
	t.Parallel()
	const real = `$(GOENV) CGO_ENABLED=1 $(GO) test -tags "$(TAGS) nopdf" $(NOPDF_PKGS) $(RACE) -count=1`
	runs, problems := goRuns("\t"+real, "test")
	if len(problems) != 0 || len(runs) != 1 {
		t.Fatalf("goRuns rejects the real pass 3: %v", problems)
	}
	if tags, _ := runs[0].flag("tags"); tags != "$(TAGS) nopdf" || runs[0].packages() != "$(NOPDF_PKGS)" ||
		!runs[0].has("$(RACE)") {
		t.Errorf("goRuns misparses the real pass 3: %#v", runs[0])
	}
	for _, line := range []string{
		real + ` -run '^$$'`,
		real + " -run=Foo",
		real + " --run Foo",
		real + " -test.run=Foo",
		real + " -skip .",
		real + " -list .",
		real + " -short",
		real + " -args -test.run=x",
		`$(GOENV) GOFLAGS=-run=x CGO_ENABLED=1 $(GO) test ./... $(RACE) -count=1`,
		"@echo '" + real + "'",
		"@printf '%s\\n' '" + real + "'",
		"true " + real,
		": " + real,
		"-" + real,
		"@-" + real,
		real + " || true",
		real + "; true",
		real + " | tee log",
		"$(GOENV) CGO_ENABLED=1 echo $(GO) test ./... $(RACE) -count=1",
	} {
		if runs, problems := goRuns("\t"+line, "test"); len(runs) != 0 || len(problems) != 1 {
			t.Errorf("goRuns accepts a line that does not plainly run the tests:\n  %s\n  runs %d, problems %v",
				line, len(runs), problems)
		}
	}
	// $(RACE) has to be a word of the go command, not text the line merely holds.
	if runs, _ := goRuns("\t$(GOENV) CGO_ENABLED=1 $(GO) test ./... -count=1 '-x$(RACE)'", "test"); len(runs) == 1 && runs[0].has("$(RACE)") {
		t.Error("goRun.has finds $(RACE) inside another word")
	}
}

// TestIntegrationTargetRunsTheDerivedList: test-int runs INT_PKGS — not the
// hand-written ./integration/... it used to — and stops if the list is empty.
func TestIntegrationTargetRunsTheDerivedList(t *testing.T) {
	t.Parallel()
	makefile := repoFile(t, "Makefile")
	recipe := commands(section(makefile, "test-int:"))
	if recipe == "" {
		t.Fatal("Makefile has no `test-int` target")
	}
	runs, problems := goRuns(recipe, "test")
	for _, p := range problems {
		t.Errorf("a `test-int` line that mentions `$(GO) test` does not run it:\n  %s", p)
	}
	if len(runs) != 1 {
		t.Fatalf("the `test-int` recipe runs `$(GO) test` %d times, want 1:\n%s", len(runs), recipe)
	}
	run := runs[0]
	if tags, _ := run.flag("tags"); tags != "integration" || run.packages() != "$(INT_PKGS)" {
		t.Errorf("`test-int` no longer runs `-tags integration $(INT_PKGS)` (tags %q, packages %q):\n  %s\n"+
			"The package list is derived from the build tags so that every "+
			"`//go:build integration` file is compiled by this target; the old "+
			"`./integration/...` left internal/archive/{zipidx,rar4,hv3} and "+
			"internal/scanner's ratio test compiled by nothing.", tags, run.packages(), run.line)
	}
	// An empty package list does not fail `go test`: it tests the package in the
	// working directory and reports ok.
	if !strings.Contains(recipe, "$(if $(INT_PKGS),,$(error") {
		t.Errorf("`test-int` no longer stops on an empty INT_PKGS. `go test -tags " +
			"integration` with no package arguments tests the current directory and " +
			"succeeds, so a derivation that broke would look like a green run.")
	}
}

// TestLintVetsTheTaggedPackages: the integration and nopdf files are type-checked
// by `make lint` too, not only compiled by the test targets that the operator
// runs less often.
func TestLintVetsTheTaggedPackages(t *testing.T) {
	t.Parallel()
	runs, problems := goRuns(commands(section(repoFile(t, "Makefile"), "lint:")), "vet")
	for _, p := range problems {
		t.Errorf("a `lint` line that mentions `$(GO) vet` does not run it:\n  %s", p)
	}
	for _, want := range []struct{ tags, pkgs string }{
		{"integration", "$(INT_PKGS)"},
		{"$(TAGS) nopdf", "$(NOPDF_PKGS)"},
	} {
		found := false
		for _, r := range runs {
			if tags, _ := r.flag("tags"); tags == want.tags && r.packages() == want.pkgs {
				found = true
			}
		}
		if !found {
			t.Errorf("`make lint` no longer runs `$(GO) vet -tags %q %s`; nothing else vets "+
				"those files (the plain and $(TAGS) vets exclude them by construction).", want.tags, want.pkgs)
		}
	}
}
