package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// copyExample puts the shipped 15 KB reference instance in a temporary
// directory so a test can edit it. It returns the copy's path and its original
// bytes.
func copyExample(t *testing.T) (string, []byte) {
	t.Helper()
	raw, err := os.ReadFile(examplePath)
	if err != nil {
		t.Fatalf("reading %s: %v", examplePath, err)
	}
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("writing the copy: %v", err)
	}
	return path, raw
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return raw
}

// TestRootsFile_addThenRemoveIsByteIdentical is the round trip amendment A-11
// makes mandatory, against the real `shelf.example.yaml` rather than a fixture.
//
// The file is 15 KB of which the overwhelming majority is explanatory comments,
// and it is the documentation the product ships *inside* itself. A writer that
// reformats it destroys that documentation, so the assertion is on bytes and
// nothing weaker. It is also the reason this writer splices text instead of
// re-emitting YAML: a `yaml.Unmarshal` → `yaml.Marshal` round trip of this same
// file returns 14 217 bytes from 15 281, dropping every blank line inside the
// comment blocks and re-indenting the document from two spaces to four.
func TestRootsFile_addThenRemoveIsByteIdentical(t *testing.T) {
	path, original := copyExample(t)

	if err := AddRoot(path, Root{Name: "docs", Path: "/mnt/media/docs", Label: "도서"}); err != nil {
		t.Fatalf("AddRoot: %v", err)
	}
	added := readFile(t, path)
	if string(added) == string(original) {
		t.Fatal("AddRoot changed nothing")
	}

	// The assertion is on the *written file*, never on a return value: what the
	// server will parse at the next restart is the bytes on disk.
	state, err := ReadFileState(path)
	if err != nil {
		t.Fatalf("ReadFileState after AddRoot: %v", err)
	}
	if len(state.Roots) != 2 {
		t.Fatalf("roots after AddRoot = %d, want 2: %+v", len(state.Roots), state.Roots)
	}
	got := state.Roots[1]
	if got.Name != "docs" || got.Path != "/mnt/media/docs" || got.Label != "도서" {
		t.Errorf("the added root reads back as %+v", got)
	}
	if !got.Enabled {
		t.Error("the added root reads back disabled; `enabled` is not written, so §3.2's default (true) must apply")
	}
	if strings.Contains(string(added), "enabled: true\n    label: \"도서\"") {
		t.Error("`enabled` was written; §7.4 says the key is not settable and must not appear")
	}

	if _, err := RemoveRoot(path, "docs"); err != nil {
		t.Fatalf("RemoveRoot: %v", err)
	}
	final := readFile(t, path)
	if string(final) != string(original) {
		t.Errorf("the round trip is not byte-identical: %d bytes before, %d after\n%s",
			len(original), len(final), firstDifference(string(original), string(final)))
	}
}

// firstDifference renders the first line where two documents diverge, which is
// the only part of a 15 KB diff a failure message can usefully carry.
func firstDifference(want, got string) string {
	a, b := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return "first differing line " + itoa(i+1) + ":\n  want: " + a[i] + "\n  got:  " + b[i]
		}
	}
	return "the shorter document is a prefix of the longer: " + itoa(len(a)) + " lines vs " + itoa(len(b))
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// TestRootsFile_preservesCommentsAndTheRestOfTheFile checks the half a
// byte-identical round trip cannot: that the file is still *right* while the
// added root is in it. Every line of the original except the spliced block must
// survive, in order.
func TestRootsFile_preservesCommentsAndTheRestOfTheFile(t *testing.T) {
	path, original := copyExample(t)
	if err := AddRoot(path, Root{Name: "docs", Path: "/mnt/media/docs", Label: "도서"}); err != nil {
		t.Fatalf("AddRoot: %v", err)
	}
	added := string(readFile(t, path))

	want := strings.Split(strings.TrimSuffix(string(original), "\n"), "\n")
	got := strings.Split(strings.TrimSuffix(added, "\n"), "\n")
	if len(got) != len(want)+3 {
		t.Fatalf("the file gained %d lines, want exactly 3", len(got)-len(want))
	}
	i := 0
	for _, line := range got {
		if i < len(want) && line == want[i] {
			i++
		}
	}
	if i != len(want) {
		t.Errorf("only %d of the original %d lines survived in order", i, len(want))
	}

	// The comment blocks are the thing being protected, so name one and assert
	// it survived verbatim rather than trusting the line count.
	for _, comment := range []string{
		"#  #  WARNING — `name` is an identity, not a label.                           #",
		"  # Honour X-Forwarded-Proto / X-Forwarded-For.  Leave this false unless a",
		"  allow_root_editing: false",
	} {
		if !strings.Contains(added, comment) {
			t.Errorf("the written file lost %q", comment)
		}
	}

	// And the whole thing must still load.
	cfg, err := Parse([]byte(added), path)
	if err != nil {
		t.Fatalf("the written file no longer parses: %v", err)
	}
	if len(cfg.Roots) != 2 || cfg.Roots[1].Name != "docs" {
		t.Errorf("the written file parses to roots %+v", cfg.Roots)
	}
}

// TestRootsFile_writeIsAtomicAndKeepsModeAndBackup pins the properties of the
// write itself (arch §7.4).
//
// # The atomicity assertion, and why it is a hard link
//
// Until 2026-07-30 this test's name claimed a property it did not check. It
// asserted the mode, the `.bak` contents and the absence of a leftover `.tmp` —
// and an in-place `os.WriteFile(path, data, mode)` satisfies all three, because
// `WriteFile` leaves an existing file's mode alone and never creates a temp file
// to leave behind. Replacing the whole temp-file → fsync → rename sequence with
// one `os.WriteFile` call was fully green.
//
// A hard link is what tells the two apart, and it is the only thing that can
// from inside the process. `os.Link` names the *inode*. A rename replaces the
// directory entry and leaves that inode alone, so the link still reads the OLD
// bytes; an in-place write truncates and rewrites that same inode, so the link
// reads the NEW ones. That distinction is exactly what atomicity buys: a reader
// holding the old file — or a crash between two write() calls — never sees a
// half-written configuration, and `internal/config.Load` never exits 2 over one.
//
// # What is deliberately NOT asserted here, rather than named in the title
//
//   - **`tmp.Sync()` before the rename.** Its failure mode is a rename that
//     beats its own data to disk across a power cut, which no in-process test
//     can produce: dropping the call leaves this test — and every other test —
//     green. It is pinned by the comment on `writeFileAtomic` and by review.
//   - **The temp file sharing the configuration's directory.** It matters only
//     when `$TMPDIR` is on a different filesystem, where `os.Rename` fails with
//     EXDEV; on a single-filesystem test machine `os.CreateTemp("", …)` would
//     pass everything below.
//
// Naming them is the point. A title that claimed them would be the same defect
// this test was fixed for.
func TestRootsFile_writeIsAtomicAndKeepsModeAndBackup(t *testing.T) {
	path, original := copyExample(t)
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	// The witness. It is taken before the write and names the inode the
	// configuration file currently occupies.
	witness := filepath.Join(filepath.Dir(path), "witness")
	if err := os.Link(path, witness); err != nil {
		t.Fatalf("hard-linking the fixture: %v", err)
	}

	if err := AddRoot(path, Root{Name: "docs", Path: "/mnt/media/docs", Label: "도서"}); err != nil {
		t.Fatalf("AddRoot: %v", err)
	}

	if got := string(readFile(t, witness)); got != string(original) {
		t.Errorf("the write was made in place: the inode the file occupied before the write now holds "+
			"the NEW contents (%d bytes, was %d).\n"+
			"§7.4 requires temp file → fsync → rename, so a reader holding the old file, or a crash "+
			"mid-write, can never see a half-written configuration.\n%s",
			len(got), len(original), firstDifference(string(original), got))
	}
	if err := os.Remove(witness); err != nil {
		t.Fatalf("removing the witness: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("mode = %v, want 0600: a configuration kept private must not become world-readable", got)
	}

	bak := readFile(t, path+BackupSuffix)
	if string(bak) != string(original) {
		t.Error(".bak does not hold the previous contents")
	}
	if info, err := os.Stat(path + BackupSuffix); err != nil {
		t.Fatalf("stat .bak: %v", err)
	} else if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf(".bak mode = %v, want 0600: the backup of a private file must not be public", got)
	}

	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("reading the directory: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("a temporary file was left behind: %s", e.Name())
		}
	}
}

// TestRootsFile_removeRefusesTheLastRoot — arch §3.2 requires at least one root
// and validate.go exits 2 without one, so a `DELETE` that emptied the list
// would tell the user to restart into a server that will not come back.
func TestRootsFile_removeRefusesTheLastRoot(t *testing.T) {
	path, original := copyExample(t)
	if _, err := RemoveRoot(path, "manga"); !errors.Is(err, ErrLastRoot) {
		t.Fatalf("RemoveRoot of the only root = %v, want ErrLastRoot", err)
	}
	if string(readFile(t, path)) != string(original) {
		t.Error("the refused removal still changed the file")
	}
	if _, err := os.Stat(path + BackupSuffix); err == nil {
		t.Error("the refused removal still took a .bak")
	}
}

// TestRootsFile_removeUnknownRootIsNotFound keeps `404` and `409` apart at the
// only layer that can tell them apart.
func TestRootsFile_removeUnknownRootIsNotFound(t *testing.T) {
	path, original := copyExample(t)
	if err := AddRoot(path, Root{Name: "docs", Path: "/mnt/media/docs", Label: "도서"}); err != nil {
		t.Fatalf("AddRoot: %v", err)
	}
	after := readFile(t, path)
	if _, err := RemoveRoot(path, "nope"); !errors.Is(err, ErrRootNotFound) {
		t.Fatalf("RemoveRoot of an absent name = %v, want ErrRootNotFound", err)
	}
	if string(readFile(t, path)) != string(after) {
		t.Error("the refused removal still changed the file")
	}
	_ = original
}

// TestRootsFile_refusesADuplicateName. The HTTP layer generates unique names,
// so this fires only on a hand-edit or a lost race between the read and the
// write — which is exactly why the writer re-reads and re-checks.
func TestRootsFile_refusesADuplicateName(t *testing.T) {
	path, original := copyExample(t)
	err := AddRoot(path, Root{Name: "manga", Path: "/mnt/media/other", Label: "다른"})
	if !errors.Is(err, ErrDuplicateRootName) {
		t.Fatalf("AddRoot with a taken name = %v, want ErrDuplicateRootName", err)
	}
	if string(readFile(t, path)) != string(original) {
		t.Error("the refused add still changed the file")
	}
}

// TestRootsFile_refusesAnUnparseableFile. A writer that cannot understand the
// file cannot promise to preserve it, and overwriting it would destroy an edit
// the user is halfway through — so nothing is written and no `.bak` is taken
// (arch §7.4's `409 conflict`).
func TestRootsFile_refusesAnUnparseableFile(t *testing.T) {
	cases := map[string]string{
		"not YAML at all":     "roots:\n  - name: \"a\"\n   path: \"/x\"\n\t\tbroken\n",
		"a flow sequence":     "roots: [{name: \"a\", path: \"/x\"}]\n",
		"no roots: key":       "server:\n  port: 8080\n",
		"roots is not a list": "roots:\n  name: \"a\"\n",
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), FileName)
			if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
				t.Fatalf("writing: %v", err)
			}
			err := AddRoot(path, Root{Name: "docs", Path: "/mnt/media/docs", Label: "docs"})
			if !errors.Is(err, ErrUnparseable) {
				t.Fatalf("AddRoot = %v, want ErrUnparseable", err)
			}
			if string(readFile(t, path)) != src {
				t.Error("the refused write still changed the file")
			}
			if _, err := os.Stat(path + BackupSuffix); err == nil {
				t.Error("the refused write still took a .bak")
			}
		})
	}
}

// TestRootsFile_namesTheFlowSequenceRefusalSeparately.
//
// A flow sequence is **valid YAML and a legal SHELF configuration** —
// `ReadFileState` reads it, `Parse` starts a server from it — and this writer
// still cannot edit it, because it splices lines and re-emitting the document is
// what would destroy the file's own documentation. That is a different problem
// from a file that is not YAML at all, with a different remedy (rewrite `roots:`
// as a block list), so it is a different error.
//
// It still satisfies `errors.Is(err, ErrUnparseable)`, deliberately: every
// caller that only needs "nothing was written and no `.bak` was taken" keeps
// working, and only the caller that wants to *say* something new has to know the
// narrower name.
func TestRootsFile_namesTheFlowSequenceRefusalSeparately(t *testing.T) {
	const flow = "roots: [{name: \"a\", path: \"/x\"}]\n"
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(path, []byte(flow), 0o644); err != nil {
		t.Fatalf("writing: %v", err)
	}

	// The premise: the reader has no trouble with it at all.
	state, err := ReadFileState(path)
	if err != nil {
		t.Fatalf("ReadFileState of a flow sequence: %v", err)
	}
	if len(state.Roots) != 1 || state.Roots[0].Name != "a" {
		t.Fatalf("the reader saw %+v; this test would prove nothing", state.Roots)
	}

	for name, err := range map[string]error{
		"AddRoot":    AddRoot(path, Root{Name: "docs", Path: "/d", Label: "docs"}),
		"RemoveRoot": second(RemoveRoot(path, "a")),
	} {
		t.Run(name, func(t *testing.T) {
			if !errors.Is(err, ErrNotABlockSequence) {
				t.Errorf("%s of a flow-style file = %v, want ErrNotABlockSequence", name, err)
			}
			if !errors.Is(err, ErrUnparseable) {
				t.Errorf("%s no longer satisfies errors.Is(err, ErrUnparseable); every caller that only "+
					"needs \"nothing was written\" would silently fall through to a 500", name)
			}
		})
	}

	// And the reasons stay apart: a file that is not YAML, a `roots:` that is a
	// mapping and a file with no `roots:` at all are all still plain
	// ErrUnparseable, not this.
	for name, src := range map[string]string{
		"not YAML at all":     "roots:\n  - name: \"a\"\n   path: \"/x\"\n\t\tbroken\n",
		"no roots: key":       "server:\n  port: 8080\n",
		"roots is not a list": "roots:\n  name: \"a\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), FileName)
			if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
				t.Fatalf("writing: %v", err)
			}
			err := AddRoot(p, Root{Name: "docs", Path: "/d", Label: "docs"})
			if !errors.Is(err, ErrUnparseable) {
				t.Fatalf("AddRoot = %v, want ErrUnparseable", err)
			}
			if errors.Is(err, ErrNotABlockSequence) {
				t.Errorf("AddRoot = %v, which claims the `roots:` list is a flow sequence; it is not, and "+
					"telling the user to rewrite it as a block list would not fix this file", err)
			}
		})
	}
}

// second drops the first return value, so a two-result call fits the table
// above.
func second(_ Root, err error) error { return err }

// TestRootsFile_handlesTheOtherBlockLayouts. The example file is one shape; a
// hand-written file is whatever the human typed. Each of these must round-trip
// byte-identically too, because each is a file somebody actually has.
func TestRootsFile_handlesTheOtherBlockLayouts(t *testing.T) {
	cases := map[string]string{
		"four-space indent": "roots:\n    - name: \"manga\"\n      path: \"/m\"\n",
		"dash on its own line": "roots:\n  -\n    name: \"manga\"\n    path: \"/m\"\n" +
			"  -\n    name: \"books\"\n    path: \"/b\"\n",
		"comments between entries": "roots:\n  - name: \"manga\"\n    path: \"/m\"\n" +
			"\n  # the second one\n  - name: \"books\"\n    path: \"/b\"\n\nstorage:\n  data_dir: \"\"\n",
		"no trailing newline": "roots:\n  - name: \"manga\"\n    path: \"/m\"",
		"an entry with no label": "roots:\n  - name: \"manga\"\n    path: \"/m\"\n    enabled: false\n" +
			"\nlog:\n  level: \"info\"\n",
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), FileName)
			if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
				t.Fatalf("writing: %v", err)
			}
			if err := AddRoot(path, Root{Name: "docs", Path: "/d", Label: "docs"}); err != nil {
				t.Fatalf("AddRoot: %v", err)
			}
			state, err := ReadFileState(path)
			if err != nil {
				t.Fatalf("ReadFileState: %v", err)
			}
			if len(state.Roots) < 2 || state.Roots[len(state.Roots)-1].Name != "docs" {
				t.Fatalf("the added root is not the last entry: %+v", state.Roots)
			}
			if _, err := RemoveRoot(path, "docs"); err != nil {
				t.Fatalf("RemoveRoot: %v", err)
			}
			if got := string(readFile(t, path)); got != src {
				t.Errorf("the round trip is not byte-identical\n%s", firstDifference(src, got))
			}
		})
	}
}

// TestRootsFile_removeTakesTheWholeEntryAndNothingElse. The entry's own lines
// go; a comment a human wrote above it stays, because this function's job is to
// remove a root and not to guess which of the user's sentences were about it.
func TestRootsFile_removeTakesTheWholeEntryAndNothingElse(t *testing.T) {
	src := "roots:\n" +
		"  - name: \"manga\"\n" +
		"    path: \"/m\"\n" +
		"\n" +
		"  # the NAS one, unplug it and this goes available:false\n" +
		"  - name: \"nas\"          # a line comment\n" +
		"    path: \"/mnt/nas\"\n" +
		"    label: \"NAS\"\n" +
		"\n" +
		"storage:\n  data_dir: \"\"\n"

	path := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatalf("writing: %v", err)
	}
	removed, err := RemoveRoot(path, "nas")
	if err != nil {
		t.Fatalf("RemoveRoot: %v", err)
	}
	if removed.Name != "nas" || removed.Label != "NAS" || removed.Path != "/mnt/nas" {
		t.Errorf("RemoveRoot returned %+v", removed)
	}

	got := string(readFile(t, path))
	for _, gone := range []string{"nas", "NAS", "/mnt/nas", "a line comment"} {
		if strings.Contains(strings.ReplaceAll(got, "the NAS one", ""), gone) {
			t.Errorf("the written file still mentions %q:\n%s", gone, got)
		}
	}
	for _, kept := range []string{"- name: \"manga\"", "path: \"/m\"", "storage:", "data_dir"} {
		if !strings.Contains(got, kept) {
			t.Errorf("the written file lost %q:\n%s", kept, got)
		}
	}
	if !strings.Contains(got, "# the NAS one") {
		t.Error("the comment above the entry was deleted; a comment is the user's prose, not part of the entry")
	}
}

// TestRootsFile_addsToAFileWhoseRootsListWasEmptied. §3.2 requires a root, so
// this file cannot start a server — but a user who emptied it by hand and then
// reaches for the 추가 button should get a root, not a refusal.
func TestRootsFile_addsToAFileWhoseRootsListWasEmptied(t *testing.T) {
	src := "server:\n  port: 8080\n\nroots:\n\nlog:\n  level: \"info\"\n"
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatalf("writing: %v", err)
	}
	if err := AddRoot(path, Root{Name: "docs", Path: "/d", Label: "docs"}); err != nil {
		t.Fatalf("AddRoot: %v", err)
	}
	state, err := ReadFileState(path)
	if err != nil {
		t.Fatalf("ReadFileState: %v", err)
	}
	if len(state.Roots) != 1 || state.Roots[0].Name != "docs" {
		t.Fatalf("roots = %+v", state.Roots)
	}
}

// TestReadFileState_reportsWhatTheFileSaysNotWhatWeLoaded. Both of A-11's
// answers about the file on disk come from this one read: the digest behind
// `Settings.server.config_changed_on_disk` and the roots list behind R2's
// pending rows. Reading it twice is how the two drift.
func TestReadFileState_reportsWhatTheFileSaysNotWhatWeLoaded(t *testing.T) {
	path, _ := copyExample(t)
	before, err := ReadFileState(path)
	if err != nil {
		t.Fatalf("ReadFileState: %v", err)
	}
	if len(before.Digest) != 64 {
		t.Errorf("digest = %q, want 64 hex characters of SHA-256", before.Digest)
	}
	if len(before.Roots) != 1 || before.Roots[0].Name != "manga" {
		t.Fatalf("roots = %+v", before.Roots)
	}
	if !before.Roots[0].Enabled {
		t.Error("`enabled` must default to true, as it does at load")
	}

	if err := AddRoot(path, Root{Name: "docs", Path: "/d", Label: "docs"}); err != nil {
		t.Fatalf("AddRoot: %v", err)
	}
	after, err := ReadFileState(path)
	if err != nil {
		t.Fatalf("ReadFileState: %v", err)
	}
	if after.Digest == before.Digest {
		t.Error("the digest did not change after a write")
	}
	if len(after.Roots) != 2 {
		t.Errorf("roots after the write = %+v", after.Roots)
	}
}

// TestReadFileState_toleratesKeysThisBuildDoesNotKnow. A newer configuration is
// not a reason to stop reporting what this server can see: the strictness of
// `KnownFields(true)` belongs at load, where it is a startup error the operator
// can act on, not on a settings-screen read.
func TestReadFileState_toleratesKeysThisBuildDoesNotKnow(t *testing.T) {
	src := "roots:\n  - name: \"manga\"\n    path: \"/m\"\n\nfrom_the_future:\n  enabled: true\n"
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatalf("writing: %v", err)
	}
	state, err := ReadFileState(path)
	if err != nil {
		t.Fatalf("ReadFileState: %v", err)
	}
	if len(state.Roots) != 1 {
		t.Errorf("roots = %+v", state.Roots)
	}
}

// TestServer_allowRootEditingDefaultsToFalse. The default is the whole security
// argument of ruling E-26, and a default is exactly the kind of thing that is
// changed by accident.
func TestServer_allowRootEditingDefaultsToFalse(t *testing.T) {
	cfg, err := Parse([]byte("roots:\n  - name: \"m\"\n    path: \"/m\"\n"), "shelf.yaml")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Server.AllowRootEditing {
		t.Error("server.allow_root_editing defaults to true; ruling E-26 requires false")
	}

	on, err := Parse([]byte("server:\n  allow_root_editing: true\nroots:\n  - name: \"m\"\n    path: \"/m\"\n"), "shelf.yaml")
	if err != nil {
		t.Fatalf("Parse with the key on: %v", err)
	}
	if !on.Server.AllowRootEditing {
		t.Error("server.allow_root_editing: true was not read")
	}
}
