package testutil_test

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"shelf/internal/testutil"
)

// volumes is "시리즈/01권.zip" … "시리즈/<n>권.zip", in name order.
func volumes(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("시리즈/%02d권.zip", i+1)
	}
	return out
}

// The draw is pinned to the rule E-66 states — the n paths whose
// sha256("shelf-sample/1" ‖ 0x00 ‖ rel) sorts first — and not to whatever the
// code happens to compute. The literals were worked out with Python's hashlib,
// so a changed seed, a hash over something other than the relative path, or
// any other "first n" (by name, evenly spaced) turns this red.
func TestSample_drawsTheFirstNBySHA256OfTheRelativePath(t *testing.T) {
	t.Parallel()
	got, missing := testutil.Sample(volumes(20), 4, nil)
	want := []string{"시리즈/07권.zip", "시리즈/08권.zip", "시리즈/09권.zip", "시리즈/11권.zip"}
	if !slices.Equal(got, want) || len(missing) != 0 {
		t.Fatalf("Sample(20 volumes, 4) = %q (missing %q), want %q", got, missing, want)
	}
}

// Same collection, same sample: the order the walk happened to produce must
// not leak into the draw.
func TestSample_isTheSameWhateverOrderThePathsArriveIn(t *testing.T) {
	t.Parallel()
	in := volumes(20)
	first, _ := testutil.Sample(in, 4, nil)
	slices.Reverse(in)
	again, _ := testutil.Sample(in, 4, nil)
	if !slices.Equal(first, again) {
		t.Fatalf("reversed input drew %q, the original order drew %q", again, first)
	}
}

// A collection grows one volume at a time, and a sample that reshuffled on
// every arrival would make two runs a week apart incomparable. The new path
// either sorts into the first n and displaces exactly one, or the sample does
// not move at all. Both cases are pinned (the ranks from the same hashlib
// computation): an evenly spaced draw over the sorted list fails the first.
func TestSample_aNewPathMovesTheSampleByAtMostOne(t *testing.T) {
	t.Parallel()
	before, _ := testutil.Sample(volumes(20), 4, nil)

	outside, _ := testutil.Sample(append(volumes(20), "시리즈/21권.zip"), 4, nil)
	if !slices.Equal(outside, before) {
		t.Errorf("adding a path that ranks 12th moved the sample from %q to %q", before, outside)
	}

	inside, _ := testutil.Sample(append(volumes(20), "시리즈/00권.zip"), 4, nil)
	want := []string{"시리즈/00권.zip", "시리즈/07권.zip", "시리즈/08권.zip", "시리즈/09권.zip"}
	if !slices.Equal(inside, want) {
		t.Errorf("adding a path that ranks 4th drew %q, want %q (11권 displaced, nothing else)", inside, want)
	}
}

// Pinned paths are the rare shapes a draw of a few hundred would usually miss.
// They are always examined, whether or not the draw reached them, counted
// once, and a pinned path the collection no longer holds is handed back rather
// than dropped without a word.
func TestSample_addsEveryPinnedPathAndReportsTheOnesThatAreGone(t *testing.T) {
	t.Parallel()
	got, missing := testutil.Sample(volumes(20), 4,
		[]string{"시리즈/20권.zip", "시리즈/08권.zip", "시리즈/99권.zip"})
	want := []string{"시리즈/07권.zip", "시리즈/08권.zip", "시리즈/09권.zip", "시리즈/11권.zip", "시리즈/20권.zip"}
	if !slices.Equal(got, want) {
		t.Errorf("Sample with pins = %q, want %q", got, want)
	}
	if !slices.Equal(missing, []string{"시리즈/99권.zip"}) {
		t.Errorf("missing = %q, want the one pin that is not in the collection", missing)
	}
}

func TestSample_aSampleAsLargeAsTheCollectionIsTheCollection(t *testing.T) {
	t.Parallel()
	for _, n := range []int{20, 21, 500} {
		if got, _ := testutil.Sample(volumes(20), n, nil); !slices.Equal(got, volumes(20)) {
			t.Errorf("Sample(20 volumes, %d) = %q, want all 20 in name order", n, got)
		}
	}
}

// SHELF_TEST_SAMPLE (E-66): a positive count or `all`. Unset means all too: a
// `go test` typed by hand is a deliberate run, and `make test-int` always sets
// it. Anything else is an error, never a quiet fallback to some other size.
func TestParseSampleSize(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		in    string
		n     int
		all   bool
		valid bool
	}{
		{"", 0, true, true},
		{"all", 0, true, true},
		{"500", 500, false, true},
		{"1", 1, false, true},
		{"0", 0, false, false},
		{"-3", 0, false, false},
		{"ALL", 0, false, false},
		{"five", 0, false, false},
		{"1.5", 0, false, false},
		{" 500", 0, false, false},
	} {
		n, all, err := testutil.ParseSampleSize(tc.in)
		if tc.valid != (err == nil) || n != tc.n || all != tc.all {
			t.Errorf("ParseSampleSize(%q) = %d, %v, %v; want %d, %v, valid=%v", tc.in, n, all, err, tc.n, tc.all, tc.valid)
		}
	}
}

// SHELF_TEST_REQUIRE_ROOT: only "1" turns it on, "" and "0" leave it off, and
// a value that looks like it means yes is refused rather than read as no.
func TestParseRequireRoot(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		in    string
		on    bool
		valid bool
	}{
		{"", false, true},
		{"0", false, true},
		{"1", true, true},
		{"true", false, false},
		{"yes", false, false},
		{"2", false, false},
	} {
		on, err := testutil.ParseRequireRoot(tc.in)
		if tc.valid != (err == nil) || on != tc.on {
			t.Errorf("ParseRequireRoot(%q) = %v, %v; want %v, valid=%v", tc.in, on, err, tc.on, tc.valid)
		}
	}
}

// recorder stands in for the *testing.T the helpers report to, so a Fatal or
// a Skip is something this test can read instead of something that ends it.
// The helpers return straight after either, as they must for a real T.
type recorder struct {
	testing.TB
	fatal, skip string
	errs, logs  []string
}

func (r *recorder) Helper()                        {}
func (r *recorder) Fatalf(format string, a ...any) { r.fatal = fmt.Sprintf(format, a...) }
func (r *recorder) Skipf(format string, a ...any)  { r.skip = fmt.Sprintf(format, a...) }
func (r *recorder) Errorf(format string, a ...any) {
	r.errs = append(r.errs, fmt.Sprintf(format, a...))
}
func (r *recorder) Logf(format string, a ...any) { r.logs = append(r.logs, fmt.Sprintf(format, a...)) }

// MediaRoot skips without a usable root, as every real-collection test did on
// its own — unless SHELF_TEST_REQUIRE_ROOT=1, which `make gates` sets so that a
// run that lost its root cannot be recorded as one that ran (E-66).
//
// Usable means listable, not merely present: a directory that stats but whose
// entries cannot be read walks as an empty collection, and the test after it
// fails on "no archives" instead of skipping or naming the cause. It is made
// with mode 0o000 at creation (no chmod), and root ignores the mode, so those
// two cases do not run as root.
func TestMediaRoot_skipsWithoutARoot_unlessOneIsRequired(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// An empty collection is a listable one: reading it ends at io.EOF, which
	// is not a reason to skip.
	empty := filepath.Join(dir, "empty")
	if err := os.Mkdir(empty, 0o700); err != nil {
		t.Fatal(err)
	}
	// Empty, so TempDir's cleanup can still remove it: unlinking a directory
	// needs write permission on its parent, not on the directory itself.
	sealed := filepath.Join(dir, "cannot-be-listed")
	if err := os.Mkdir(sealed, 0o000); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, root, require string
		want                string // the root returned, or "" for none
		skip, fatal         bool
		says                string // a fragment the skip or fatal must carry
	}{
		{"a readable directory", dir, "", dir, false, false, ""},
		{"a readable directory, required", dir, "1", dir, false, false, ""},
		{"an empty directory, required", empty, "1", empty, false, false, ""},
		{"unset", "", "", "", true, false, ""},
		{"unset, required", "", "1", "", false, true, ""},
		{"unset, explicitly not required", "", "0", "", true, false, ""},
		{"a path that does not exist", filepath.Join(dir, "gone"), "", "", true, false, ""},
		{"a path that does not exist, required", filepath.Join(dir, "gone"), "1", "", false, true, ""},
		{"a file", file, "", "", true, false, ""},
		{"a file, required", file, "1", "", false, true, ""},
		{"a directory that cannot be listed", sealed, "", "", true, false, fs.ErrPermission.Error()},
		{"a directory that cannot be listed, required", sealed, "1", "", false, true, fs.ErrPermission.Error()},
		{"a requirement that is not 1 or 0", dir, "yes", "", false, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.root == sealed && os.Geteuid() == 0 {
				t.Skip("root lists a 0o000 directory regardless of its mode")
			}
			t.Setenv(testutil.RootEnv, tc.root)
			t.Setenv(testutil.RequireRootEnv, tc.require)
			r := &recorder{}
			got := testutil.MediaRoot(r)
			if got != tc.want || (r.skip != "") != tc.skip || (r.fatal != "") != tc.fatal {
				t.Errorf("MediaRoot = %q, skip %q, fatal %q; want %q, skip=%v, fatal=%v",
					got, r.skip, r.fatal, tc.want, tc.skip, tc.fatal)
			}
			if msg := r.skip + r.fatal; !strings.Contains(msg, tc.says) {
				t.Errorf("MediaRoot said %q; want it to name the cause, %q", msg, tc.says)
			}
		})
	}
}

// PickSample is what the integration tests call: it reads SHELF_TEST_SAMPLE
// when called, says in the log whether the run was a sample, and refuses the
// two runs that would read as a pass while examining less than they claim — a
// pin that has gone and an empty pick.
func TestPickSample_readsTheSizeWhenCalled_andSaysWhatItPicked(t *testing.T) {
	pins := []string{"시리즈/20권.zip"}
	for _, tc := range []struct {
		name, env string
		rels      []string
		pins      []string
		want      []string
		log       string // a fragment the log line must carry
		errs      int
		fatal     bool
	}{
		{"a sample", "4", volumes(20), pins,
			[]string{"시리즈/07권.zip", "시리즈/08권.zip", "시리즈/09권.zip", "시리즈/11권.zip", "시리즈/20권.zip"},
			"sample 4 of 20 volumes (sha256 order, seed shelf-sample/1) + 1 pinned", 0, false},
		{"all", "all", volumes(20), pins, volumes(20), "all 20 volumes (SHELF_TEST_SAMPLE=all)", 0, false},
		{"unset", "", volumes(20), pins, volumes(20), "all 20 volumes (SHELF_TEST_SAMPLE unset)", 0, false},
		{"a pin that has gone", "4", volumes(20), []string{"시리즈/99권.zip"},
			[]string{"시리즈/07권.zip", "시리즈/08권.zip", "시리즈/09권.zip", "시리즈/11권.zip"},
			"sample 4 of 20 volumes", 1, false},
		{"an invalid size", "0", volumes(20), pins, nil, "", 0, true},
		{"nothing to pick from", "4", nil, nil, nil, "", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(testutil.SampleEnv, tc.env)
			r := &recorder{}
			got := testutil.PickSample(r, "volumes", tc.rels, tc.pins)
			if (r.fatal != "") != tc.fatal {
				t.Fatalf("fatal = %q, want fatal=%v", r.fatal, tc.fatal)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("picked %q, want %q", got, tc.want)
			}
			if len(r.errs) != tc.errs {
				t.Errorf("errors = %q, want %d", r.errs, tc.errs)
			}
			if tc.log != "" && !strings.Contains(strings.Join(r.logs, "\n"), tc.log) {
				t.Errorf("log = %q, want it to say %q", r.logs, tc.log)
			}
		})
	}
}
