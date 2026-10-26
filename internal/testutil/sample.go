package testutil

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"testing"
)

// The variables a real-collection integration test reads (ruling E-66). Each
// is read when the helper is called, never at package initialisation: this
// package is imported by every test package and by scripts/mkfixture, and none
// of those should see a side effect of a variable meant for four tests.
const (
	// RootEnv names the collection the integration tier reads (impl-plan §6.2).
	RootEnv = "SHELF_TEST_ROOT"
	// RequireRootEnv, set to "1", turns "no usable root" from a skip into a
	// failure. `make gates` sets it, so a gate run that lost its root cannot be
	// recorded as a run.
	RequireRootEnv = "SHELF_TEST_REQUIRE_ROOT"
	// SampleEnv narrows a test that walks the whole collection to a
	// deterministic sample: a positive count, or "all".
	SampleEnv = "SHELF_TEST_SAMPLE"
)

// sampleSeed versions the draw. Changing it changes every sample at once,
// which is the point of writing it down: two runs agree on what they examined
// exactly when they agree on this string and on the collection.
const sampleSeed = "shelf-sample/1"

// MediaRoot returns the collection named by SHELF_TEST_ROOT. Without a
// readable directory there it skips the test, as each real-collection test
// used to on its own, or fails it when SHELF_TEST_REQUIRE_ROOT=1.
//
// Readable means its entries can be listed, not only that it stats as a
// directory. A root that stats but cannot be listed walks as an empty
// collection — the walks log an unreadable directory and carry on — so the
// test would go on to fail on "no archives", blaming the collection for what is
// the root's fault, where this check skips it or, under
// SHELF_TEST_REQUIRE_ROOT=1, fails it naming the cause (E-66).
func MediaRoot(t testing.TB) string {
	t.Helper()
	required, err := ParseRequireRoot(os.Getenv(RequireRootEnv))
	if err != nil {
		t.Fatalf("%v", err)
		return ""
	}
	root := os.Getenv(RootEnv)
	var why string
	if root == "" {
		why = RootEnv + " is not set"
	} else if fi, err := os.Stat(root); err != nil {
		why = fmt.Sprintf("%s=%q is not readable: %v", RootEnv, root, err)
	} else if !fi.IsDir() {
		why = fmt.Sprintf("%s=%q is not a directory", RootEnv, root)
	} else if err := listable(root); err != nil {
		why = fmt.Sprintf("%s=%q cannot be listed: %v", RootEnv, root, err)
	}
	if why == "" {
		return root
	}
	if required {
		t.Fatalf("%s, and %s=1 says this run must not skip", why, RequireRootEnv)
		return ""
	}
	t.Skipf("%s", why)
	return ""
}

// listable reads one entry of dir: an empty directory is listable (io.EOF), and
// any other error is the reason it is not. One entry is enough to know, and
// costs one getdents however large the collection's top level is.
func listable(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if _, err := f.ReadDir(1); err != nil && err != io.EOF {
		return err
	}
	return nil
}

// ParseRequireRoot reads a SHELF_TEST_REQUIRE_ROOT value: "1" is on, "" and
// "0" are off, and anything else is an error rather than a guess.
func ParseRequireRoot(v string) (bool, error) {
	switch v {
	case "1":
		return true, nil
	case "", "0":
		return false, nil
	}
	return false, fmt.Errorf("%s=%q: want 1, 0 or unset", RequireRootEnv, v)
}

// ParseSampleSize reads a SHELF_TEST_SAMPLE value. "all" and "" (unset) mean
// every candidate — a `go test` typed by hand is a deliberate run, and
// `make test-int` always sets the variable. A positive integer is the size of
// the draw. Anything else is an error, never a quiet fallback to some other
// size.
func ParseSampleSize(v string) (n int, all bool, err error) {
	if v == "" || v == "all" {
		return 0, true, nil
	}
	n, err = strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, false, fmt.Errorf("%s=%q: want a positive count or \"all\"", SampleEnv, v)
	}
	return n, false, nil
}

// Sample returns, in name order, the n paths of rels whose
// sha256(sampleSeed ‖ 0x00 ‖ rel) sorts first, together with every pinned path
// rels holds; missing is the pinned paths it does not hold.
//
// rels are paths relative to the collection root, slash-separated, so the draw
// does not depend on where the volume is mounted. Ranking by hash rather than
// taking every k-th name is what keeps a sample stable as the collection grows:
// a new path changes the draw — the first n, before the pins are added — only
// when the new path itself ranks in the first n, and then it displaces exactly
// one. The pins sit on top of that and do not follow it (E-66): a displaced
// path that is pinned stays, so the result grows by one instead, and a pin that
// reappears in rels joins the result whatever its rank.
func Sample(rels []string, n int, pinned []string) (picked, missing []string) {
	type ranked struct {
		key [sha256.Size]byte
		rel string
	}
	all := make([]ranked, len(rels))
	held := make(map[string]bool, len(rels))
	for i, rel := range rels {
		all[i] = ranked{sha256.Sum256([]byte(sampleSeed + "\x00" + rel)), rel}
		held[rel] = true
	}
	slices.SortFunc(all, func(a, b ranked) int {
		if c := bytes.Compare(a.key[:], b.key[:]); c != 0 {
			return c
		}
		return cmp.Compare(a.rel, b.rel)
	})

	// Sized by what can actually be chosen, not by n: SHELF_TEST_SAMPLE is any
	// positive count, and 1e9 over twenty candidates must not reserve 1e9.
	chosen := make(map[string]bool, min(n, len(all))+len(pinned))
	for _, r := range all[:min(n, len(all))] {
		chosen[r.rel] = true
	}
	for _, p := range pinned {
		if held[p] {
			chosen[p] = true
		} else {
			missing = append(missing, p)
		}
	}
	for rel := range chosen {
		picked = append(picked, rel)
	}
	slices.Sort(picked)
	return picked, missing
}

// PickSample narrows rels — the candidates a real-collection test found, what
// names them in its log — to the ones this run examines, by SHELF_TEST_SAMPLE
// read now. It logs whether the run is a sample, so a log alone tells a partial
// run from a full one, and it refuses the two runs that would pass while
// examining less than they claim: a pinned path the collection no longer holds
// (an error naming it — the pin is a rare shape, and losing it is a decision to
// record by editing the list) and an empty pick (fatal).
func PickSample(t testing.TB, what string, rels, pinned []string) []string {
	t.Helper()
	if len(rels) == 0 {
		t.Fatalf("no %s to pick from", what)
		return nil
	}
	n, all, err := ParseSampleSize(os.Getenv(SampleEnv))
	if err != nil {
		t.Fatalf("%v", err)
		return nil
	}
	if all {
		n = len(rels)
	}
	picked, missing := Sample(rels, n, pinned)
	for _, p := range missing {
		t.Errorf("pinned %q is not under the root: the shape it stands for is no longer examined — "+
			"replace it or remove it from the list deliberately", p)
	}

	switch {
	case all && os.Getenv(SampleEnv) == "":
		t.Logf("all %d %s (%s unset)", len(rels), what, SampleEnv)
	case all:
		t.Logf("all %d %s (%s=all)", len(rels), what, SampleEnv)
	default:
		drawn := min(n, len(rels))
		t.Logf("sample %d of %d %s (sha256 order, seed %s) + %d pinned",
			drawn, len(rels), what, sampleSeed, len(picked)-drawn)
	}
	return picked
}
