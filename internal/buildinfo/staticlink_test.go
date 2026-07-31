package buildinfo_test

// The E-21 linkage gate: the shipped default artefact must be statically linked.
//
// `make build` reported CGO_ENABLED=0 in its build metadata and still produced a
// binary that asked the dynamic loader for libc.so.6, libdl.so.2 and
// libpthread.so.0 — `internal/thumbs` → github.com/gen2brain/avif →
// github.com/ebitengine/purego emits dynamic import directives whatever cgo is
// set to. On the Synology/QNAP/Alpine hosts prd NFR-OPS-003 makes primary, that
// binary does not start at all. prd CON-001 asks for "정적 단일 바이너리", i.e. the
// property, not the flag; we satisfied the flag and missed the goal.
//
// The defect survived four review passes and a full E2E cycle because every
// existing check looked at the BUILD FLAGS. This one looks at the ELF headers of
// the artefact that actually ships, which is the only thing that can tell the
// difference.
//
// Four tests, deliberately:
//
//   - TestDefaultArtefactIsStaticallyLinked inspects real files. It SKIPS when
//     `dist/` is empty, because `dist/` is gitignored and impl-plan §6.1 makes
//     this tier hermetic — linking a 27 MB binary (which first needs `pnpm build`
//     for go:embed) is not a unit test. The skip is not a hole: `make build-go`
//     and `make release` re-run this exact test with SHELF_STATIC_ARTEFACTS
//     pointing at the artefact they just produced, where a missing file is fatal.
//   - TestAVIFVariantCarriesTheDecoder is its inverse, and the reason it exists
//     is that nothing else asserted the `-avif` variant is anything but a second
//     copy of the default. `AVIF_TAGS := $(TAGS)` would emit seven artefacts that
//     SHA256SUMS lists and ARTIFACTS.txt describes as carrying the decoder, and
//     which do not. Same skip rule, same override.
//   - TestDefaultBuildIsConfiguredStatic never skips. It is the guard on the
//     configuration that makes the artefact static in the first place, so a clean
//     checkout still asserts something.
//   - TestReleaseLinkageGatesCoverEveryLinuxTarget checks the two gates above are
//     pointed at EVERY linux release target, not just the convenient one.
//
// The last two read the Makefile through `make` itself (the `print-%` target)
// rather than by pattern-matching its text. That is deliberate: a text scan that
// stops at the first `TAGS…?=` line is defeated by a plain `TAGS :=` assignment
// three lines below it, and a `$(filter linux/%,…)` narrowed to `linux/amd64`
// looks the same to a regexp either way. `make print-X` answers with what the
// build will actually use.
//
// Non-ELF artefacts (darwin Mach-O, windows PE) pass the LINKAGE check: every Go
// binary on those platforms links libSystem / kernel32, the failure mode E-21
// describes is linux-only, and NFR-OPS-003's NAS targets are all linux. The
// decoder-presence check is object-format independent and applies everywhere.

import (
	"bytes"
	"debug/elf"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// envArtefacts names the space-separated path list the Makefile passes in. When
// it is set the paths are mandatory: the caller has just built them.
const envArtefacts = "SHELF_STATIC_ARTEFACTS"

// envAVIFArtefacts is the same for the opt-in `-avif` variant, which must be the
// exact opposite: it has to CONTAIN the decoder (and is therefore dynamically
// linked on linux, by design).
const envAVIFArtefacts = "SHELF_AVIF_ARTEFACTS"

// avifDecoderMarker is what an AVIF-carrying binary contains and a `-tags noavif`
// binary does not. Go keeps function names in the pclntab for tracebacks, so
// `-trimpath -ldflags "-s -w"` does not remove it; measured across linux/amd64,
// linux/arm, darwin/arm64 and windows/amd64 as 63–65 occurrences in an AVIF
// build and 0 in a default one. It is a stronger signal than linkage because it
// works on Mach-O and PE too, where "dynamic" tells you nothing.
const avifDecoderMarker = "github.com/gen2brain/avif"

var elfMagic = []byte{0x7f, 'E', 'L', 'F'}

// linkage is what an artefact asks of the loader.
type linkage struct {
	isELF     bool
	hasInterp bool     // PT_INTERP program header or a .interp section
	interp    string   // the interpreter path, when readable — for the message
	needed    []string // DT_NEEDED entries
}

// static is the predicate. Either signal alone condemns the binary: a linker
// that emitted DT_NEEDED without PT_INTERP, or the reverse, is still a binary
// that will not run where there is no libc.
func (l linkage) static() bool { return !l.hasInterp && len(l.needed) == 0 }

func (l linkage) String() string {
	if !l.isELF {
		return "not an ELF file"
	}
	var b strings.Builder
	if l.hasInterp {
		b.WriteString("interpreter ")
		if l.interp != "" {
			b.WriteString(l.interp)
		} else {
			b.WriteString("(present)")
		}
	} else {
		b.WriteString("no interpreter")
	}
	if len(l.needed) > 0 {
		fmt.Fprintf(&b, "; DT_NEEDED %s", strings.Join(l.needed, " "))
	} else {
		b.WriteString("; no DT_NEEDED")
	}
	return b.String()
}

// inspect reads the artefact's headers. It never executes it: `ldd` runs the
// loader (and on some hosts the binary), is absent from minimal images, and
// says nothing a header read cannot.
func inspect(path string) (linkage, error) {
	f, err := os.Open(path)
	if err != nil {
		return linkage{}, err
	}
	defer f.Close()

	magic := make([]byte, 4)
	if _, err := io.ReadFull(f, magic); err != nil {
		return linkage{}, fmt.Errorf("%s: reading the magic number: %w", path, err)
	}
	if !bytes.Equal(magic, elfMagic) {
		// Mach-O or PE. A different object format, not a verdict.
		return linkage{}, nil
	}

	ef, err := elf.NewFile(f)
	if err != nil {
		return linkage{}, fmt.Errorf("%s: has the ELF magic but does not parse: %w", path, err)
	}
	defer ef.Close()

	l := linkage{isELF: true}
	for _, p := range ef.Progs {
		if p.Type == elf.PT_INTERP {
			l.hasInterp = true
		}
	}
	if s := ef.Section(".interp"); s != nil {
		l.hasInterp = true
		if data, err := s.Data(); err == nil {
			l.interp = strings.TrimRight(string(data), "\x00")
		}
	}
	// DynString(DT_NEEDED) returns nil, nil when there is no .dynamic section,
	// which is precisely the static case.
	needed, err := ef.ImportedLibraries()
	if err != nil {
		return l, fmt.Errorf("%s: reading DT_NEEDED: %w", path, err)
	}
	sort.Strings(needed)
	l.needed = needed
	return l, nil
}

// carriesAVIFDecoder reports whether gen2brain/avif was linked into the artefact.
func carriesAVIFDecoder(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	return bytes.Contains(data, []byte(avifDecoderMarker)), nil
}

func TestDefaultArtefactIsStaticallyLinked(t *testing.T) {
	t.Parallel()

	paths, demanded := artefacts(t, envArtefacts, defaultArtefactGlob)
	if len(paths) == 0 {
		t.Skipf("no built artefact to inspect. This check reads ELF headers off "+
			"disk and will not link a binary itself (dist/ is gitignored; "+
			"go:embed needs `pnpm build` first). Run `make build`, or point it at "+
			"one: %s=path/to/binary go test ./internal/buildinfo -run %s",
			envArtefacts, t.Name())
	}

	root := repoRoot(t)
	for _, path := range paths {
		if !demanded {
			if _, err := os.Stat(path); err != nil {
				continue
			}
		}
		// The decoder must not be in the default artefact at all. On darwin and
		// windows this is the ONLY signal — every Mach-O and PE binary is
		// "dynamic", so a lost `noavif` would sail past the linkage check there.
		if carried, err := carriesAVIFDecoder(path); err != nil {
			t.Errorf("%s: %v", path, err)
		} else if carried {
			t.Errorf("%s contains %s — this artefact was NOT built with `-tags noavif`.\n"+
				"Ruling E-21 makes the default build static, and the decoder is what "+
				"makes it dynamic (via ebitengine/purego). AVIF ships as the separate "+
				"`-avif` release variant, never as the default.%s",
				path, avifDecoderMarker, staleness(t, root, path))
		}

		l, err := inspect(path)
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		if !l.isELF {
			t.Logf("%s: not an ELF file — linkage is a linux concern (NFR-OPS-003)", path)
			continue
		}
		if l.static() {
			t.Logf("%s: static (%s)", filepath.Base(path), l)
			continue
		}
		t.Errorf("%s is DYNAMICALLY LINKED — %s\n"+
			"CON-001 asks for a static single binary, and prd NFR-OPS-003 makes NAS\n"+
			"(Synology/QNAP/Alpine — musl, or a glibc older than this one) the primary\n"+
			"deployment target: this artefact will not start there. CGO_ENABLED=0 is\n"+
			"NOT sufficient — internal/thumbs -> github.com/gen2brain/avif ->\n"+
			"github.com/ebitengine/purego emits dynamic import directives regardless.\n"+
			"The default build carries `-tags noavif` for exactly this reason\n"+
			"(ruling E-21, docs/decisions.md). If this fired: either the Makefile's\n"+
			"TAGS default lost `noavif`, or a NEW dependency pulled purego (or cgo) in.\n"+
			"Do not fix it by deleting this test — a glibc-linked artefact is the\n"+
			"defect CON-001 exists to prevent.%s",
			path, l, staleness(t, root, path))
	}
}

// TestAVIFVariantCarriesTheDecoder is the other half of `make release`.
//
// Nothing else asserts that the `-avif` artefacts differ from the default ones.
// `AVIF_TAGS := $(TAGS)` — one character — emits seven binaries with `noavif`
// still set, writes them into SHA256SUMS, and lets ARTIFACTS.txt tell the reader
// they contain a decoder they do not contain. Every other gate stays green,
// because a static `-avif` build is not a linkage defect; it is a LIE.
//
// The linux assertion is the stated design (docs/arch-backend.md §11: "on linux,
// a DYNAMIC link ... via ebitengine/purego"), measured true on amd64, arm64 and
// arm. If purego ever stops needing the loader, this test becomes wrong and must
// be re-derived from a measurement — not deleted.
func TestAVIFVariantCarriesTheDecoder(t *testing.T) {
	t.Parallel()

	paths, demanded := artefacts(t, envAVIFArtefacts, avifArtefactGlob)
	if len(paths) == 0 {
		t.Skipf("no `-avif` artefact to inspect; `make release` builds them and "+
			"re-runs this test with %s set. Or: %s=dist/shelf-…-linux-amd64-avif "+
			"go test ./internal/buildinfo -run %s", envAVIFArtefacts, envAVIFArtefacts, t.Name())
	}

	for _, path := range paths {
		if !demanded {
			if _, err := os.Stat(path); err != nil {
				continue
			}
		}
		carried, err := carriesAVIFDecoder(path)
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		if !carried {
			t.Errorf("%s does NOT contain %s.\n"+
				"This is the opt-in variant whose entire reason to exist is the AVIF\n"+
				"decoder: dist/SHA256SUMS lists it and dist/ARTIFACTS.txt tells the\n"+
				"reader it 'adds the gen2brain/avif decoder'. Almost certainly\n"+
				"`AVIF_TAGS` stopped being `$(filter-out noavif,$(TAGS))`, so the\n"+
				"variant is a byte-identical copy of the default under another name.",
				path, avifDecoderMarker)
			continue
		}
		l, err := inspect(path)
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		if !l.isELF {
			t.Logf("%s: carries the decoder (linkage is a linux concern)", filepath.Base(path))
			continue
		}
		if l.static() {
			t.Errorf("%s carries %s yet is STATICALLY linked — %s.\n"+
				"On linux the decoder reaches libc through ebitengine/purego, which is\n"+
				"the whole basis of ruling E-21; a static build containing it contradicts\n"+
				"the measurement the ruling rests on. Re-measure before trusting either.",
				path, avifDecoderMarker, l)
			continue
		}
		t.Logf("%s: carries the decoder, dynamic as designed (%s)", filepath.Base(path), l)
	}
}

func defaultArtefactGlob(root string) []string {
	var paths []string
	if p := filepath.Join(root, "dist", "shelf"); fileExists(p) {
		paths = append(paths, p)
	}
	for _, p := range globSorted(root, "shelf-*-linux-*") {
		if strings.HasSuffix(filepath.Base(p), "-avif") {
			continue // dynamic ON PURPOSE; TestAVIFVariantCarriesTheDecoder owns it
		}
		paths = append(paths, p)
	}
	return paths
}

func avifArtefactGlob(root string) []string {
	return globSorted(root, "shelf-*-linux-*-avif")
}

func globSorted(root, pattern string) []string {
	globbed, err := filepath.Glob(filepath.Join(root, "dist", pattern))
	if err != nil {
		// The patterns above are constants; a malformed one is a programming error.
		panic(fmt.Sprintf("globbing dist/%s: %v", pattern, err))
	}
	sort.Strings(globbed)
	return globbed
}

// artefacts returns the paths to inspect and whether the caller demanded them.
//
// The Makefile passes exactly what it just built, and a missing one is then a
// failure. Without the variable this globs what a developer's `dist/` happens to
// hold.
//
// Relative paths are resolved against the repository root, not the working
// directory: `go test` runs the binary with its cwd set to the PACKAGE
// directory, so the Makefile's natural `dist/shelf` would otherwise resolve to
// `internal/buildinfo/dist/shelf` and the gate would fail for the wrong reason.
func artefacts(t *testing.T, env string, glob func(root string) []string) (paths []string, demanded bool) {
	t.Helper()
	root := repoRoot(t)
	if list := strings.Fields(os.Getenv(env)); len(list) > 0 {
		for i, p := range list {
			if !filepath.IsAbs(p) {
				list[i] = filepath.Join(root, p)
			}
		}
		return list, true
	}
	return glob(root), false
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

// staleness explains a failure that is about `dist/` being old rather than about
// the tree being wrong.
//
// `dist/` is gitignored and survives every checkout, rebase and fix. A release
// artefact built before the fix that made the default static keeps failing this
// test afterwards, and the message above then accuses a tree that is already
// correct. That is a true alarm — a stale binary is exactly what someone would
// ship by mistake — but it has to say WHICH alarm it is. `make release` now
// deletes `dist/shelf-*-*` before it builds, so this only appears when an
// artefact predates the last source change.
func staleness(t *testing.T, root, path string) string {
	t.Helper()
	if !strings.HasPrefix(filepath.Base(path), "shelf-") {
		return "" // dist/shelf: build-go's own output, relinked on every run
	}
	fi, err := os.Stat(path)
	if err != nil {
		return ""
	}
	newest, where := newestSource(root)
	if where == "" || !fi.ModTime().Before(newest) {
		return ""
	}
	return fmt.Sprintf("\n\nNOTE — THIS ARTEFACT IS STALE, which may be the whole story.\n"+
		"  %s was built %s,\n"+
		"  and %s changed %s afterwards.\n"+
		"  It is a `make release` output from a previous state of the tree, and\n"+
		"  dist/ is gitignored so it survived whatever changed. Before reading the\n"+
		"  verdict above as a defect in the CURRENT tree, clear it and rebuild:\n"+
		"      rm -f dist/shelf-*-*      # or: make clean\n"+
		"      make release\n"+
		"  If it still fails after that, the tree really is producing a dynamic\n"+
		"  artefact and the verdict above stands.",
		filepath.Base(path), fi.ModTime().Format(time.RFC3339),
		where, newest.Sub(fi.ModTime()).Round(time.Second))
}

// newestSource is the most recent modification time among the inputs a release
// artefact is built from, and the file that carries it.
func newestSource(root string) (time.Time, string) {
	var newest time.Time
	var where string
	consider := func(p string, fi fs.FileInfo) {
		if fi.ModTime().After(newest) {
			newest, where = fi.ModTime(), mustRel(root, p)
		}
	}
	for _, f := range []string{"Makefile", "go.mod", "go.sum"} {
		p := filepath.Join(root, f)
		if fi, err := os.Stat(p); err == nil {
			consider(p, fi)
		}
	}
	for _, dir := range []string{"cmd", "internal"} {
		_ = filepath.WalkDir(filepath.Join(root, dir), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") {
				return nil //nolint:nilerr // a best-effort hint, never a failure
			}
			if fi, err := d.Info(); err == nil {
				consider(p, fi)
			}
			return nil
		})
	}
	return newest, where
}

func mustRel(root, p string) string {
	if rel, err := filepath.Rel(root, p); err == nil {
		return rel
	}
	return p
}

// repoRoot is repoFile's (release_budget_test.go) directory half.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolving the repository root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("%s is not the repository root (no go.mod): %v", root, err)
	}
	return root
}

// makeVar expands one Makefile variable through make itself.
//
// Reading the Makefile as text and stopping at the first match is how the
// previous version of this test was defeated: it took `TAGS ?= noavif` and never
// looked at the `TAGS :=` that a later line could add. make's expander has no
// such blind spot, and it costs ~40 ms.
func makeVar(t *testing.T, name string) string {
	t.Helper()
	if _, err := exec.LookPath("make"); err != nil {
		t.Fatalf("`make` is not on PATH: the E-21 guards read the build "+
			"configuration through `make -s print-%s` rather than by pattern-matching "+
			"the Makefile, because a text scan can be defeated by a later override. "+
			"There is no fallback that is worth having: %v", name, err)
	}
	cmd := exec.Command("make", "-s", "--no-print-directory", "print-"+name)
	cmd.Dir = repoRoot(t)
	cmd.Env = makeEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("`make -s print-%s` failed: %v\n%s\n"+
			"The `print-%%` target is what these guards read the build configuration "+
			"through; it must stay in the Makefile.", name, err, out)
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// makeEnv is the process environment with everything removed that would let the
// operator's shell — or a parent `make` — answer instead of the Makefile.
// Variables assigned with `?=` take their value from the environment when one
// exists, and MAKEFLAGS carries command-line overrides down into a sub-make.
func makeEnv() []string {
	drop := map[string]bool{
		"MAKEFLAGS": true, "MFLAGS": true, "GNUMAKEFLAGS": true,
		"MAKEOVERRIDES": true, "MAKELEVEL": true,
		"TAGS": true, "AVIF_TAGS": true, "VERSION": true,
		"RELEASE_TARGETS": true, "LINUX_TARGETS": true,
		"STATIC_ARTEFACTS": true, "AVIF_ARTEFACTS": true, "SIZE_BUDGET": true,
	}
	env := os.Environ()
	kept := env[:0]
	for _, kv := range env {
		if k, _, ok := strings.Cut(kv, "="); ok && drop[k] {
			continue
		}
		kept = append(kept, kv)
	}
	return kept
}

// TestDefaultBuildIsConfiguredStatic is the half that cannot skip: on a clean
// checkout with no dist/, this is what still fails if the fix is undone.
func TestDefaultBuildIsConfiguredStatic(t *testing.T) {
	t.Parallel()
	makefile := repoFile(t, "Makefile") // release_budget_test.go

	// `?=` is what keeps `make TAGS=…` working for the operator; the value the
	// build will actually use comes from make, not from this line, so that a
	// later `TAGS :=` cannot hide underneath it.
	if !hasAssignment(makefile, "TAGS", "?=") {
		t.Error("Makefile has no `TAGS ?= …` default. Ruling E-21 makes `noavif` the " +
			"default build tag, and `?=` is what lets a build ask for something else.")
	}
	tags := makeVar(t, "TAGS")
	if !contains(strings.Fields(tags), "noavif") {
		t.Errorf("`make print-TAGS` expands to %q, which does not include `noavif`.\n"+
			"Ruling E-21: gen2brain/avif -> ebitengine/purego makes the default build "+
			"dynamically linked against libc, which will not start on the NAS hosts "+
			"NFR-OPS-003 targets. AVIF ships as the opt-in `-avif` release variant.", tags)
	}

	// The gate has to still be wired into the two targets that produce artefacts;
	// TestDefaultArtefactIsStaticallyLinked skips on a bare checkout, so these
	// invocations are what make it unskippable where it matters.
	for _, target := range []string{"build-go:", "release:"} {
		recipe := commands(section(makefile, target)) // release_budget_test.go
		if recipe == "" {
			t.Errorf("Makefile has no `%s` target", target)
			continue
		}
		if !strings.Contains(recipe, envArtefacts) {
			t.Errorf("the `%s` recipe no longer runs the ELF-linkage check "+
				"(no %s). E-21 requires the ARTEFACT to be asserted on, and this "+
				"is the only place it exists at build time.", target, envArtefacts)
		}
	}
	if release := commands(section(makefile, "release:")); !strings.Contains(release, envAVIFArtefacts) {
		t.Errorf("the `release` recipe no longer runs the `-avif` check (no %s), so "+
			"nothing asserts the opt-in variant contains the decoder that is its only "+
			"reason to exist. See TestAVIFVariantCarriesTheDecoder.", envAVIFArtefacts)
	}

	// STATICCHECK must not be able to run zero tests and call that a pass.
	//
	// It used to carry `-run '^TestDefaultArtefactIsStaticallyLinked$'`. Rename
	// the test, or mistype one character of the pattern, and `go test` prints
	// "[no tests to run]" and exits 0 — `make build-go` then returns 0 with the
	// gate never having executed, which is precisely the silent self-skip E-21
	// exists to prevent. The package only reads files off disk; it runs whole.
	staticcheck := assignment(makefile, "STATICCHECK")
	if staticcheck == "" {
		t.Fatal("Makefile has no `STATICCHECK :=` definition; it is what `build-go` " +
			"and `release` invoke the E-21 gate through")
	}
	if !strings.Contains(staticcheck, "./internal/buildinfo") {
		t.Errorf("STATICCHECK no longer runs ./internal/buildinfo:\n  %s", staticcheck)
	}
	if strings.Contains(staticcheck, "-run") {
		t.Errorf("STATICCHECK has regained a `-run` filter:\n  %s\n"+
			"A `-run` pattern that matches nothing makes `go test` exit 0 with "+
			"\"[no tests to run]\", so `make build-go` succeeds without the gate having "+
			"run — a rename or a typo is enough. Run the whole package: every test in "+
			"it only reads files off disk.", staticcheck)
	}

	// `make test` has to cover the configuration that SHIPS. An untagged-only run
	// left `go test -tags noavif ./...` failing (two golden files pinned
	// `avif_enabled: true`, which a default build can no longer report) with every
	// gate green, because `make lint` only *vets* the shipped tags.
	testRecipe := commands(section(makefile, "test:"))
	if testRecipe == "" {
		t.Fatal("Makefile has no `test` target")
	}
	if !strings.Contains(testRecipe, `-tags "$(TAGS)"`) {
		t.Errorf("the `test` recipe no longer runs the suite with `-tags \"$(TAGS)\"`:\n%s\n"+
			"Ruling E-21 made `noavif` the default, so $(TAGS) is what users receive. "+
			"Without this pass nothing anywhere runs the tests in the shipped "+
			"configuration — `make lint`'s `go vet -tags` does not run tests.", testRecipe)
	}
	// …and must not do it by REPLACING the untagged pass, which is the only one
	// that carries a decoder and can therefore exercise the live AVIF path.
	if !strings.Contains(testRecipe, "$(GO) test ./...") {
		t.Errorf("the `test` recipe no longer runs the suite untagged:\n%s\n"+
			"That pass is the superset: thumbs.TestGenerate_avif_decodesThroughThe"+
			"SerialisedSlowPath self-skips under `-tags noavif`, so dropping it deletes "+
			"the only live AVIF decode coverage in the repository.", testRecipe)
	}
}

// TestReleaseLinkageGatesCoverEveryLinuxTarget closes the narrowing mutation.
//
// `[ "$$arch" = "amd64" ]` inside the old release loop turned the gate into a
// one-architecture spot check with nothing going red, and per-target coverage is
// not academic: linux/arm carries only libdl.so.2 where amd64 and arm64 carry
// three, so the arches genuinely differ in how they fail. The lists are now
// derived from RELEASE_TARGETS by make, and this asserts the derivation is still
// total by expanding it and comparing sets.
func TestReleaseLinkageGatesCoverEveryLinuxTarget(t *testing.T) {
	t.Parallel()

	version := makeVar(t, "VERSION")
	if version == "" {
		t.Fatal("`make print-VERSION` is empty; the artefact names are built from it")
	}

	var wantStatic, wantAVIF []string
	for _, target := range strings.Fields(makeVar(t, "RELEASE_TARGETS")) {
		goos, goarch, ok := strings.Cut(target, "/")
		if !ok {
			t.Fatalf("RELEASE_TARGETS entry %q is not os/arch", target)
		}
		if goos != "linux" {
			continue // Mach-O and PE are always dynamic; E-21 is a linux ruling
		}
		base := fmt.Sprintf("dist/shelf-%s-%s-%s", version, goos, goarch)
		wantStatic = append(wantStatic, base)
		wantAVIF = append(wantAVIF, base+"-avif")
	}
	if len(wantStatic) == 0 {
		t.Fatal("RELEASE_TARGETS names no linux target. NFR-OPS-003 makes NAS " +
			"(linux) the primary deployment target and E-21 is the ruling that " +
			"artefact must be static — there is nothing left for the gate to assert.")
	}

	for _, tc := range []struct {
		variable string
		want     []string
		why      string
	}{
		{"STATIC_ARTEFACTS", wantStatic,
			"every linux DEFAULT artefact must be proven static (E-21)"},
		{"AVIF_ARTEFACTS", wantAVIF,
			"every linux `-avif` artefact must be proven to contain the decoder"},
	} {
		got := strings.Fields(makeVar(t, tc.variable))
		sort.Strings(got)
		want := append([]string(nil), tc.want...)
		sort.Strings(want)
		if strings.Join(got, " ") != strings.Join(want, " ") {
			t.Errorf("`make print-%s` does not cover every linux release target:\n"+
				"  want %v\n"+
				"  got  %v\n"+
				"%s. The DT_NEEDED set differs per architecture (linux/arm carries only "+
				"libdl.so.2 where amd64 and arm64 carry three), so a gate narrowed to one "+
				"arch lets the others ship broken. Keep the list derived from "+
				"RELEASE_TARGETS with $(filter linux/%%,…) rather than written out.",
				tc.variable, want, got, tc.why)
		}
	}
}

// assignment returns the full right-hand side of a `NAME := …` (or `?=`, `+=`,
// `=`) definition, following backslash continuations.
func assignment(makefile, name string) string {
	lines := strings.Split(makefile, "\n")
	for i, line := range lines {
		rest, ok := strings.CutPrefix(line, name)
		if !ok {
			continue
		}
		rest = strings.TrimLeft(rest, " \t")
		rest = strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(rest, ":"), "?"), "+")
		if !strings.HasPrefix(rest, "=") {
			continue
		}
		value := strings.TrimPrefix(rest, "=")
		for strings.HasSuffix(strings.TrimRight(value, " \t"), "\\") && i+1 < len(lines) {
			value = strings.TrimSuffix(strings.TrimRight(value, " \t"), "\\")
			i++
			value += " " + strings.TrimSpace(lines[i])
		}
		return strings.TrimSpace(value)
	}
	return ""
}

// hasAssignment reports whether the Makefile assigns name with exactly op.
func hasAssignment(makefile, name, op string) bool {
	for _, line := range strings.Split(makefile, "\n") {
		rest, ok := strings.CutPrefix(line, name)
		if !ok {
			continue
		}
		if strings.HasPrefix(strings.TrimLeft(rest, " \t"), op) {
			return true
		}
	}
	return false
}

// commands strips comment lines from a recipe, so that an assertion about what a
// target RUNS cannot be satisfied by a comment that merely mentions it.
func commands(recipe string) string {
	var out []string
	for _, line := range strings.Split(recipe, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
