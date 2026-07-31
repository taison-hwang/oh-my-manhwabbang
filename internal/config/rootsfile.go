package config

// rootsfile.go is the one place in this program that writes a configuration
// file. AMENDMENT A-11, ruling E-26.
//
// # Why it exists at all
//
// SHELF's one-line summary of itself has been "SHELF never writes", and
// FR-CFG-005 / NFR-DAT-002 — never write to a media volume — is untouched by
// this file and by everything that calls it. What ruling E-26 bought is
// narrower: the settings screen may add and remove entries in the `roots:` list
// of the configuration file, gated by `server.allow_root_editing`, with the
// running server adopting nothing until it restarts. Only this package writes;
// the HTTP layer calls it and never touches a file itself, and
// `scripts/check-readonly.sh` greps for exactly that.
//
// # Why the edit is a text splice and not a YAML re-emit
//
// impl-plan §0.3 originally specified this writer "over `yaml.Node` rather than
// the typed structs". That was measured before it was implemented, and it does
// not hold: `yaml.Unmarshal` → `yaml.Marshal` of the shipped
// `shelf.example.yaml` returns **14 217 bytes / 252 lines from 15 281 bytes /
// 277 lines**. It drops every blank line inside the comment blocks, re-indents
// the whole document from two spaces to four, and re-anchors continuation
// comment lines to the new indent so they no longer line up with the key they
// explain. `shelf.example.yaml` is 14 KB of which the overwhelming majority is
// explanatory prose, and a writer that reformats the user's file is a writer
// that destroys the documentation the product ships *inside* the file.
//
// So `yaml.Node` is used only to **locate** the `roots:` sequence and the line
// span of each entry. The edit itself splices raw lines: everything outside the
// spliced range is copied through byte for byte, which is why "add a root, then
// remove it" is byte-identical to the original and is asserted as such against
// the real `shelf.example.yaml`.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// The failures a caller has to tell apart. arch §7.4 maps them: an unreadable
// or unparseable file is `409 conflict` (a writer that cannot understand the
// file cannot promise to preserve it, and overwriting it would destroy an edit
// the user is halfway through), a missing or duplicate name is `404`/`400`, and
// anything else is `500`.
var (
	// ErrUnparseable is a configuration file that is no longer valid YAML, or
	// whose `roots:` list is not a block sequence this writer can splice. Both
	// mean the same thing to a caller: nothing was written and no `.bak` was
	// taken.
	ErrUnparseable = errors.New("config: the configuration file cannot be edited in place")
	// ErrRootNotFound is a name that is not in the file's `roots:` list.
	ErrRootNotFound = errors.New("config: no such root in the configuration file")
	// ErrLastRoot refuses to remove the only entry. arch §3.2 requires at least
	// one root and validate.go exits 2 without one, so the restart the endpoint
	// asks for would not come back.
	ErrLastRoot = errors.New("config: the last root cannot be removed")
	// ErrDuplicateRootName is a name that is already in the file. The HTTP layer
	// generates unique names, so reaching this is a lost race or a hand-edit
	// between the read and the write — which is precisely why the writer
	// re-reads and re-checks instead of trusting what it was handed.
	ErrDuplicateRootName = errors.New("config: a root of that name is already in the configuration file")
	// ErrNotABlockSequence is the one refusal above that is NOT a broken file:
	// `roots: [{name: "a", path: "/x"}]` is valid YAML, a legal SHELF
	// configuration, and something `ReadFileState` and `Parse` both read without
	// complaint. This writer still cannot touch it, because it splices lines and
	// re-emitting the document is exactly what it exists to avoid — see the
	// header comment.
	//
	// It is a *sub-case* of ErrUnparseable rather than a sibling, and that is
	// load-bearing in both directions: every caller that only needs "nothing was
	// written and no `.bak` was taken" keeps working through errors.Is, while a
	// caller that wants to tell the user the one thing that fixes it — rewrite
	// `roots:` as a block list — can ask for the narrower name. Reporting it as
	// plain "unparseable" made the settings screen say the file could not be
	// read while it was listing that same file's roots (arch §7.4).
	ErrNotABlockSequence = fmt.Errorf("%w: the `roots:` list is a flow sequence, which this writer cannot splice a line at a time", ErrUnparseable)
)

// FileState is one read of the configuration file as it currently sits on disk,
// which is not necessarily what this process loaded.
//
// It carries both answers the running server needs from that file, because it
// needs them at the same moments and reading it twice is how the two drift:
// `Settings.server.config_changed_on_disk` compares Digest against the digest
// taken at load (§7.8), and `GET /api/roots` renders the entries this process
// has no index row for as *pending* (§7.4, revision R2).
type FileState struct {
	// Digest is the SHA-256 of the file's bytes, hex-encoded.
	Digest string
	// Roots is the `roots:` list exactly as the file currently spells it,
	// defaulted the same way loading defaults it (`enabled` absent means true).
	// It is not validated: this is a report of what the file says, and the
	// caller decides what to do about it.
	Roots []Root
}

// ReadFileState reads and digests the configuration file at path.
//
// It is deliberately lenient about everything except the YAML itself: a file
// carrying keys this build does not know still yields a digest and a roots
// list, because a newer configuration is not a reason to stop reporting what
// this server can see.
func ReadFileState(path string) (FileState, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return FileState{}, fmt.Errorf("reading %s: %w", path, err)
	}
	sum := sha256.Sum256(raw)
	state := FileState{Digest: hex.EncodeToString(sum[:])}

	var doc struct {
		Roots []Root `yaml:"roots"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return FileState{}, fmt.Errorf("parsing %s: %w: %v", path, ErrUnparseable, err)
	}
	state.Roots = doc.Roots
	return state, nil
}

// AddRoot appends one entry to the `roots:` list of the file at path.
//
// It re-reads the file itself rather than trusting a list the caller read a
// moment ago, so a root added by hand between two requests is never silently
// discarded (arch §7.4). `enabled` is deliberately not written: §3.2's default
// is true, and a key the endpoint cannot change has no business appearing in
// the file. `label` is always written, even when the caller derived it from the
// directory's base name — §3.2's own fallback for a missing label is the
// `name`, and the name is a slug that dropped every Hangul character, so
// `[만화] 군계 1~25` is a better shelf label than `root-2`.
func AddRoot(path string, r Root) error {
	f, err := loadRootsFile(path)
	if err != nil {
		return err
	}
	for _, existing := range f.roots {
		if existing.Name == r.Name {
			return fmt.Errorf("%w: %q", ErrDuplicateRootName, r.Name)
		}
	}
	block := renderRootEntry(f.indent, r)
	return f.splice(path, f.insertAt, f.insertAt, block)
}

// RemoveRoot deletes the entry named name from the `roots:` list of the file at
// path, and returns the entry as the file spelled it.
//
// Only the entry's own lines go. A comment a human wrote *above* an entry is
// left where it is, deliberately: this function's job is to remove a root, not
// to guess which of the user's sentences were about it, and an orphaned comment
// is a smaller injury than a deleted one.
func RemoveRoot(path, name string) (Root, error) {
	f, err := loadRootsFile(path)
	if err != nil {
		return Root{}, err
	}
	idx := -1
	for i, existing := range f.roots {
		if existing.Name == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		return Root{}, fmt.Errorf("%w: %q", ErrRootNotFound, name)
	}
	if len(f.roots) == 1 {
		return Root{}, fmt.Errorf("%w: %q", ErrLastRoot, name)
	}
	span := f.spans[idx]
	if err := f.splice(path, span.start, span.end+1, nil); err != nil {
		return Root{}, err
	}
	return f.roots[idx], nil
}

// --- locating the block ----------------------------------------------------

// span is a half-open-free, 0-based, inclusive line range [start, end].
type span struct{ start, end int }

// rootsFile is one parsed-for-editing configuration file: its raw lines, the
// line span of every `roots:` entry, and where a new entry goes.
type rootsFile struct {
	lines []string
	// trailingNewline records whether the file ended with one, so a splice
	// cannot silently add or drop the last byte.
	trailingNewline bool
	roots           []Root
	spans           []span
	// insertAt is the 0-based line index a new entry is inserted *before*: one
	// past the last line of the last entry.
	insertAt int
	// indent is the string in front of the `-` of an existing entry, reused so a
	// file indented with four spaces stays indented with four.
	indent string
	mode   fs.FileMode
}

func loadRootsFile(path string) (*rootsFile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parsing %s: %w: %v", path, ErrUnparseable, err)
	}

	f := &rootsFile{mode: info.Mode().Perm(), indent: "  "}
	f.lines, f.trailingNewline = splitLines(raw)

	keyLine, seq, err := findRootsSequence(&doc)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	if seq == nil {
		// `roots:` is present with no list under it. That file cannot start the
		// server (§3.2 requires one root), but a user who emptied it by hand and
		// then reaches for the 추가 button should get a root, not a 409.
		f.insertAt = keyLine
		return f, nil
	}

	for _, item := range seq.Content {
		var r Root
		if err := item.Decode(&r); err != nil {
			return nil, fmt.Errorf("%s: reading a roots[] entry: %w: %v", path, ErrUnparseable, err)
		}
		f.roots = append(f.roots, r)
		f.spans = append(f.spans, f.spanOf(item))
	}
	if n := len(f.spans); n > 0 {
		f.insertAt = f.spans[n-1].end + 1
		f.indent = leadingSpace(f.lines[f.spans[n-1].start])
	} else {
		f.insertAt = keyLine
	}
	return f, nil
}

// findRootsSequence returns the 0-based line index just after the `roots:` key
// and the sequence node under it. A nil sequence with no error means the key is
// there and empty.
func findRootsSequence(doc *yaml.Node) (int, *yaml.Node, error) {
	if doc.Kind == yaml.DocumentNode {
		if len(doc.Content) == 0 {
			return 0, nil, fmt.Errorf("%w: the file holds no YAML document", ErrUnparseable)
		}
		doc = doc.Content[0]
	}
	if doc.Kind != yaml.MappingNode {
		return 0, nil, fmt.Errorf("%w: the document is not a mapping", ErrUnparseable)
	}
	for i := 0; i+1 < len(doc.Content); i += 2 {
		key, value := doc.Content[i], doc.Content[i+1]
		if key.Value != "roots" {
			continue
		}
		switch {
		case value.Kind == yaml.SequenceNode && value.Style == 0:
			return key.Line, value, nil
		case value.Kind == yaml.SequenceNode:
			// A flow sequence (`roots: [{...}]`) parses fine and is a legal
			// configuration, but it cannot be spliced a line at a time without
			// re-emitting it — and re-emitting is what this file exists to
			// avoid. Refusing is honest; reformatting the user's file behind
			// their back is not.
			//
			// It gets its own error rather than the general one because it is
			// the only refusal here that is not a broken file, and because it is
			// the only one with a remedy the user can act on: rewrite `roots:`
			// as a block list.
			return 0, nil, ErrNotABlockSequence
		case value.Kind == yaml.ScalarNode && value.Tag == "!!null":
			return key.Line, nil, nil
		default:
			// `roots:` is there but is a mapping, a string, or something else
			// that is not a list at all. That file does not start a server
			// either, so telling the user to change its *style* would be wrong.
			return 0, nil, fmt.Errorf("%w: the `roots:` list is not a sequence", ErrUnparseable)
		}
	}
	return 0, nil, fmt.Errorf("%w: the file has no `roots:` list", ErrUnparseable)
}

// spanOf is the inclusive, 0-based line range one sequence entry occupies.
//
// The first line is the one carrying the entry's `-`, which is not always the
// line the mapping node reports: `- name: x` puts them together, while a `-`
// alone on its own line puts the mapping on the next one. The last line is the
// deepest line any descendant reports, extended over any following line that is
// indented past the entry's keys — which is what a block scalar's body looks
// like. Comment lines are never absorbed: a comment after the last key may
// belong to the entry or to whatever comes next, and leaving it costs a stray
// line while taking it costs the user's prose.
func (f *rootsFile) spanOf(item *yaml.Node) span {
	// The dash is on the mapping's own line in every ordinary layout, and at
	// most a line or two above it in the `-` alone form. Beyond that we are
	// guessing, and a guess here would delete somebody else's line.
	const dashSearchDepth = 2
	first := item.Line - 1
	start := first
	for i := first; i >= 0 && i >= first-dashSearchDepth; i-- {
		trimmed := strings.TrimLeft(f.lines[i], " \t")
		if trimmed == "-" || strings.HasPrefix(trimmed, "- ") {
			start = i
			break
		}
	}

	end := maxLine(item) - 1
	if end < start {
		end = start
	}
	keyColumn := item.Column - 1
	for end+1 < len(f.lines) {
		next := f.lines[end+1]
		trimmed := strings.TrimSpace(next)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			break
		}
		if len(leadingSpace(next)) <= keyColumn {
			break
		}
		end++
	}
	return span{start: start, end: end}
}

// maxLine is the deepest 1-based line any node in the subtree reports.
func maxLine(n *yaml.Node) int {
	deepest := n.Line
	for _, child := range n.Content {
		if l := maxLine(child); l > deepest {
			deepest = l
		}
	}
	return deepest
}

// --- rendering and splicing ------------------------------------------------

// renderRootEntry is the three lines a new root becomes.
func renderRootEntry(indent string, r Root) []string {
	keyIndent := indent + "  "
	return []string{
		indent + "- name: " + quoteYAML(r.Name),
		keyIndent + "path: " + quoteYAML(r.Path),
		keyIndent + "label: " + quoteYAML(r.Label),
	}
}

// quoteYAML renders a string as a double-quoted YAML scalar.
//
// Always quoting, rather than quoting when it is necessary, is the point: the
// three values written here are a slug, an absolute path and a free-form label,
// and an unquoted path beginning `~/` or a label reading `yes` would parse back
// as something else. `\` and `"` are escaped, and Hangul is legal verbatim.
//
// **What this function does NOT do, and what the caller therefore must.** It is
// not a YAML emitter: it escapes those two bytes and copies everything else
// through. A control character copied through is not an escape-the-quotes
// problem, it is a *lost data* problem — a line break inside a double-quoted
// scalar folds to a space when the file is read back, and `\a` makes
// `yaml.Unmarshal` refuse the whole document ("control characters are not
// allowed"). Either way the file no longer says what the caller meant, and one
// of them is a server that will not start.
//
// This comment used to assert that "the caller has already refused control
// characters (§7.4's `control_characters`)". That was false for `path` until
// 2026-07-30: `internal/httpapi.validateRootCreate` applied the check to `label`
// only, and an omitted label is derived from `path` *after* it. The guarantee
// this function relies on is now stated where it can be measured — `path` and
// `label` are both checked, and `internal/httpapi.TestCreateRoot_rejects` and
// `TestCreateRoot_neverWritesAPathItCannotReadBack` watch it fail. `name` needs
// no check: it is a server-generated slug over `[a-zA-Z0-9._-]`.
func quoteYAML(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// splice replaces lines [from, to) with block and writes the result.
func (f *rootsFile) splice(path string, from, to int, block []string) error {
	out := make([]string, 0, len(f.lines)+len(block))
	out = append(out, f.lines[:from]...)
	out = append(out, block...)
	out = append(out, f.lines[to:]...)

	var buf bytes.Buffer
	for i, line := range out {
		buf.WriteString(line)
		if i < len(out)-1 || f.trailingNewline {
			buf.WriteByte('\n')
		}
	}
	return writeFileAtomic(path, buf.Bytes(), f.mode)
}

// splitLines splits on "\n" without inventing or losing a final empty line.
func splitLines(raw []byte) (lines []string, trailingNewline bool) {
	s := string(raw)
	if s == "" {
		return nil, false
	}
	if strings.HasSuffix(s, "\n") {
		return strings.Split(strings.TrimSuffix(s, "\n"), "\n"), true
	}
	return strings.Split(s, "\n"), false
}

func leadingSpace(s string) string {
	return s[:len(s)-len(strings.TrimLeft(s, " \t"))]
}

// --- the atomic write ------------------------------------------------------

// BackupSuffix is appended to the configuration file's name for the copy taken
// before every write (arch §7.4).
const BackupSuffix = ".bak"

// writeFileAtomic replaces path with data: a `.bak` of the previous contents
// first, then a temp file **in the same directory** → fsync → rename.
//
// Every clause is load-bearing. The temp file shares the directory because
// `os.Rename` is only atomic within a filesystem, and `$TMPDIR` routinely is
// not the one holding the configuration. The fsync is before the rename
// because a rename that beats its own data to disk yields a zero-length
// configuration after a power cut — and a zero-length configuration is a server
// that will not start. The mode is copied from the file being replaced, so a
// `shelf.yaml` deliberately kept 0600 does not become world-readable because a
// button was pressed. The directory is fsynced afterwards so the rename itself
// survives. The `.bak` is written first: if it fails, nothing has changed yet.
func writeFileAtomic(path string, data []byte, mode fs.FileMode) error {
	previous, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading %s before replacing it: %w", path, err)
	}
	if err := os.WriteFile(path+BackupSuffix, previous, mode); err != nil {
		return fmt.Errorf("writing %s: %w", path+BackupSuffix, err)
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("creating a temporary file next to %s: %w", path, err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // a no-op once the rename succeeded

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing %s: %w", tmpName, err)
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("setting the mode of %s: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("syncing %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replacing %s: %w", path, err)
	}
	syncDir(dir)
	return nil
}

// syncDir flushes the directory entry the rename created. It is best-effort:
// the rename has already happened, and a platform that will not open a
// directory (Windows) is not a reason to report a failed write.
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	defer func() { _ = d.Close() }()
	_ = d.Sync()
}
