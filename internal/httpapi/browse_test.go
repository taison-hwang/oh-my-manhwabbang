package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Amendment A-12 (ruling E-40) — `GET /api/browse`, the directory picker.
//
// This endpoint is the only one in the package that takes a filesystem path, so
// the tests that matter most are the ones that try to leave the allowlist. They
// come first, and they are written as *attacks* rather than as round trips:
// what has to hold is that nothing outside `server.browse_bases` is ever listed,
// by any spelling.

func browse(e *env, path string) *httptest.ResponseRecorder {
	if path == "" {
		return e.get("/api/browse")
	}
	return e.get("/api/browse?path=" + url.QueryEscape(path))
}

// reasonOf reads `detail.reason` off a refusal. §7.4's vocabulary is the
// machine-readable half of every `400`/`403` here, and asserting the status
// alone would pass for the wrong refusal.
func reasonOf(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var envelope ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("the body is not an error envelope: %v (%s)", err, w.Body.String())
	}
	reason, _ := envelope.Error.Detail["reason"].(string)
	return reason
}

// containsPath is the leak check. §8.4 keeps absolute host paths out of error
// envelopes, and this endpoint is the one with the most to leak.
func containsPath(body, path string) bool { return strings.Contains(body, path) }

// --- the two gates ---------------------------------------------------------

// TestBrowse_isBehindTheWriteGate is E-40 §3: browsing is never the first
// privilege an installation grants.
//
// The refusal is `disabled` — the write gate's own reason — and not a
// browse-specific one, because the remedy is the write gate's: set
// `server.allow_root_editing`. A server with bases configured and editing off
// must still refuse, which is what the second case pins; configuring bases is
// not consent to browsing on its own.
func TestBrowse_isBehindTheWriteGate(t *testing.T) {
	t.Run("editing off, no bases", func(t *testing.T) {
		e := newEnv(t)
		errorBody(t, browse(e, ""), http.StatusForbidden, CodeForbidden)
	})

	t.Run("editing off, bases configured", func(t *testing.T) {
		dir := t.TempDir()
		e := newEnv(t, func(c *envConfig) { c.browseBases = []string{dir} })
		w := browse(e, "")
		errorBody(t, w, http.StatusForbidden, CodeForbidden)
		if got := reasonOf(t, w); got != reasonDisabled {
			t.Errorf("reason = %q, want %q — the remedy is the write key, not the bases",
				got, reasonDisabled)
		}
	})
}

// TestBrowse_refusesWithNoBases pins the default: the key is empty unless the
// operator set it, and an empty allowlist lists nothing at all.
//
// It is a separate reason from `disabled` because it is a separate remedy, which
// is the same rule §7.4 already applies to its three `403`s.
func TestBrowse_refusesWithNoBases(t *testing.T) {
	e := newEnv(t, withRootEditing())
	w := browse(e, "")
	errorBody(t, w, http.StatusForbidden, CodeForbidden)
	if got := reasonOf(t, w); got != reasonNoBrowseBases {
		t.Errorf("reason = %q, want %q", got, reasonNoBrowseBases)
	}
}

// --- confinement -----------------------------------------------------------

// TestBrowse_refusesEverythingOutsideTheBases is the endpoint's whole security
// argument, tried by every spelling that reaches the handler.
//
// The refusal is deliberately identical for "outside the allowlist" and "does
// not exist": a caller that could tell them apart would have a filesystem
// existence oracle on an unauthenticated listener, which is most of what the
// allowlist exists to deny.
func TestBrowse_refusesEverythingOutsideTheBases(t *testing.T) {
	parent := t.TempDir()
	base := filepath.Join(parent, "base")
	sibling := filepath.Join(parent, "sibling")
	mustMkdir(t, base)
	mustMkdir(t, sibling)
	e := newEnv(t, withBrowseBases(base))

	for _, tc := range []struct {
		name string
		path string
	}{
		{"the base's parent", parent},
		{"a sibling of the base", sibling},
		{"a dot-dot climb out of the base", filepath.Join(base, "..")},
		{"a dot-dot climb to a sibling", filepath.Join(base, "..", "sibling")},
		{"the filesystem root", "/"},
		{"a path that does not exist outside the base", filepath.Join(parent, "nope")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := browse(e, tc.path)
			errorBody(t, w, http.StatusForbidden, CodeForbidden)
			if got := reasonOf(t, w); got != reasonOutsideBases {
				t.Errorf("reason = %q, want %q", got, reasonOutsideBases)
			}
			if body := w.Body.String(); containsPath(body, sibling) {
				t.Errorf("the refusal echoed a host path back: %s", body)
			}
		})
	}
}

// TestBrowse_willNotFollowASymlinkOutOfTheBase is the case a string comparison
// cannot catch, and the reason `readSubdirs` goes through `os.Root` rather than
// through `os.ReadDir` on a path it checked itself.
//
// A symlink inside a base pointing outside it is a legitimate filesystem, and
// `filepath.Clean` on the request says nothing about it — the path *is* under
// the base. Two things stop it, and both are asserted: the listing drops
// symlinks, so it is never offered; and descending into it explicitly is refused
// by the kernel at openat(2), so guessing the name does not help either.
func TestBrowse_willNotFollowASymlinkOutOfTheBase(t *testing.T) {
	parent := t.TempDir()
	base := filepath.Join(parent, "base")
	outside := filepath.Join(parent, "outside")
	mustMkdir(t, base)
	mustMkdir(t, filepath.Join(outside, "secret"))
	link := filepath.Join(base, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("this filesystem does not support symlinks: %v", err)
	}
	e := newEnv(t, withBrowseBases(base))

	listing := decodeBody[BrowseResponse](t, browse(e, base), http.StatusOK)
	for _, entry := range listing.Entries {
		if entry.Name == "escape" {
			t.Fatalf("the listing offered a symlink out of the base: %+v", entry)
		}
	}

	// And naming it directly. The path is textually inside the base, so only the
	// os.Root handle can refuse it.
	w := browse(e, link)
	if w.Code == http.StatusOK {
		got := decodeBody[BrowseResponse](t, w, http.StatusOK)
		t.Fatalf("descending a symlink out of the base returned %d entries from %q",
			len(got.Entries), got.Path)
	}
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 — os.Root refuses the traversal and it maps to §7.4's table",
			w.Code)
	}
}

// --- the listing itself ----------------------------------------------------

// TestBrowse_topLevelIsTheBases pins the synthetic root: `path` is empty rather
// than `/`, and `parent` is null, because there is no node above the bases.
func TestBrowse_topLevelIsTheBases(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	e := newEnv(t, withBrowseBases(first, second))

	got := decodeBody[BrowseResponse](t, browse(e, ""), http.StatusOK)
	if got.Path != "" {
		t.Errorf("path = %q, want \"\" — naming a directory here would name one that is not listable", got.Path)
	}
	if got.Parent != nil {
		t.Errorf("parent = %v, want null at the top level", *got.Parent)
	}
	if got.Self != nil {
		t.Error("self is set at the top level, where there is no single directory to choose")
	}
	if len(got.Entries) != 2 || got.Entries[0].Path != first || got.Entries[1].Path != second {
		t.Errorf("entries = %+v, want the two configured bases in order", got.Entries)
	}
}

// TestBrowse_listsDirectoriesOnly: a picker chooses a root, and a root is a
// directory. Files are not entries, and the sort is the product's natural order
// rather than byte order — `10` after `9`, as everywhere else.
func TestBrowse_listsDirectoriesOnly(t *testing.T) {
	base := t.TempDir()
	for _, name := range []string{"권 10", "권 9", "가나다"} {
		mustMkdir(t, filepath.Join(base, name))
	}
	mustWrite(t, filepath.Join(base, "표지.jpg"), "not a directory")
	e := newEnv(t, withBrowseBases(base))

	got := decodeBody[BrowseResponse](t, browse(e, base), http.StatusOK)
	names := make([]string, 0, len(got.Entries))
	for _, entry := range got.Entries {
		names = append(names, entry.Name)
	}
	want := []string{"가나다", "권 9", "권 10"}
	if len(names) != len(want) {
		t.Fatalf("entries = %v, want %v — a file is not a candidate root", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("entries = %v, want %v (natural order)", names, want)
		}
	}
	if got.Truncated {
		t.Error("truncated = true for three entries")
	}
	if got.Parent != nil {
		t.Errorf("parent = %q at a base; the breadcrumb must not offer a step the endpoint refuses", *got.Parent)
	}
	if got.Self == nil || got.Self.Path != base {
		t.Fatalf("self = %+v, want the listed directory itself", got.Self)
	}
}

// TestBrowse_selectableMirrorsTheCreateRules is the anti-lying test.
//
// `selectable` is computed by the server from `validateRootCreate`'s own rules,
// and the point of that is that the picker and `POST /api/roots` cannot drift.
// So each case is asserted **twice**: the flag the picker shows, and the status
// the endpoint actually gives for the same path.
func TestBrowse_selectableMirrorsTheCreateRules(t *testing.T) {
	base := t.TempDir()
	inside := filepath.Join(base, "안쪽")
	mustMkdir(t, inside)
	e := newEnv(t, withBrowseBases(base))

	// The fixture's media root, made reachable from the picker by adding its
	// parent as a base, is the `duplicate` case; its parent is `overlaps`.
	mediaParent := filepath.Dir(e.media)
	e2 := newEnv(t, withBrowseBases(mediaParent))
	listing := decodeBody[BrowseResponse](t, browse(e2, mediaParent), http.StatusOK)
	var configured *BrowseEntry
	for i, entry := range listing.Entries {
		if entry.Path == e2.media {
			configured = &listing.Entries[i]
		}
	}
	if configured == nil {
		t.Fatalf("the configured root %q is not under %q; this test would prove nothing", e2.media, mediaParent)
	}
	if configured.Selectable {
		t.Error("the picker offers a directory that is already a root")
	}
	if configured.Reason == nil || *configured.Reason != reasonDuplicate {
		t.Errorf("reason = %v, want %q", configured.Reason, reasonDuplicate)
	}
	// The same path through the endpoint the flag is predicting.
	w := e2.jsonBody(http.MethodPost, "/api/roots", `{"path":`+quoteJSON(e2.media)+`}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("POST for the same path = %d, want 400 — the picker's flag would have been a lie", w.Code)
	}

	// And a plain directory is selectable, so the flag is not simply always false.
	plain := decodeBody[BrowseResponse](t, browse(e, base), http.StatusOK)
	if len(plain.Entries) != 1 {
		t.Fatalf("entries = %+v", plain.Entries)
	}
	if !plain.Entries[0].Selectable || plain.Entries[0].Reason != nil {
		t.Errorf("a fresh directory is not selectable: %+v", plain.Entries[0])
	}
	if inside != plain.Entries[0].Path {
		t.Errorf("path = %q, want %q", plain.Entries[0].Path, inside)
	}
}

// TestBrowse_descendsAndOffersTheWayBack pins the breadcrumb's two moves.
//
// `parent` stops at the base rather than walking to `/` — a breadcrumb whose
// next click is a `403` is worse than one that ends.
func TestBrowse_descendsAndOffersTheWayBack(t *testing.T) {
	base := t.TempDir()
	mid := filepath.Join(base, "중간")
	leaf := filepath.Join(mid, "끝")
	mustMkdir(t, leaf)
	e := newEnv(t, withBrowseBases(base))

	got := decodeBody[BrowseResponse](t, browse(e, leaf), http.StatusOK)
	if got.Path != leaf {
		t.Errorf("path = %q, want %q", got.Path, leaf)
	}
	if got.Parent == nil || *got.Parent != mid {
		t.Fatalf("parent = %v, want %q", got.Parent, mid)
	}
	if len(got.Entries) != 0 {
		t.Errorf("entries = %+v, want none", got.Entries)
	}

	up := decodeBody[BrowseResponse](t, browse(e, *got.Parent), http.StatusOK)
	if up.Parent == nil || *up.Parent != base {
		t.Fatalf("parent of %q = %v, want %q", mid, up.Parent, base)
	}
	atBase := decodeBody[BrowseResponse](t, browse(e, base), http.StatusOK)
	if atBase.Parent != nil {
		t.Errorf("parent of the base = %q; the walk up must stop at the allowlist", *atBase.Parent)
	}
}

// TestBrowse_rejectsAMalformedPath keeps the two request-shape rules §7.4 already
// applies to `POST /api/roots` on this endpoint too. A relative path is the
// commonest hand-typed mistake; a control character is the one that would reach
// a YAML writer if it were ever passed on.
func TestBrowse_rejectsAMalformedPath(t *testing.T) {
	base := t.TempDir()
	e := newEnv(t, withBrowseBases(base))

	for _, tc := range []struct{ name, path, reason string }{
		{"relative", "media/books", reasonNotAbsolute},
		{"control character", "/tmp/a\nb", reasonControlChars},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := browse(e, tc.path)
			errorBody(t, w, http.StatusBadRequest, CodeBadRequest)
			if got := reasonOf(t, w); got != tc.reason {
				t.Errorf("reason = %q, want %q", got, tc.reason)
			}
		})
	}
}

// TestBrowse_reportsAFileAsNotADirectory: the picker lists only directories, so
// this is reached by a typed path rather than by a click — and it must not come
// back as `not_readable`, which is a true statement about the wrong problem.
func TestBrowse_reportsAFileAsNotADirectory(t *testing.T) {
	base := t.TempDir()
	file := filepath.Join(base, "표지.jpg")
	mustWrite(t, file, "x")
	e := newEnv(t, withBrowseBases(base))

	w := browse(e, file)
	errorBody(t, w, http.StatusBadRequest, CodeBadRequest)
	if got := reasonOf(t, w); got != reasonNotADirectory {
		t.Errorf("reason = %q, want %q", got, reasonNotADirectory)
	}
}

// TestBrowse_listsAMissingBaseAsUnselectable: an unmounted drive is listed and
// greyed out, not hidden. Hiding it would make it indistinguishable from a typo
// in the configuration — the same reasoning that makes an unreachable root
// `available: false` rather than absent (§7.3).
func TestBrowse_listsAMissingBaseAsUnselectable(t *testing.T) {
	present := t.TempDir()
	missing := filepath.Join(t.TempDir(), "unmounted")
	e := newEnv(t, withBrowseBases(present, missing))

	got := decodeBody[BrowseResponse](t, browse(e, ""), http.StatusOK)
	if len(got.Entries) != 2 {
		t.Fatalf("entries = %+v, want both bases listed", got.Entries)
	}
	var gone *BrowseEntry
	for i, entry := range got.Entries {
		if entry.Path == missing {
			gone = &got.Entries[i]
		}
	}
	if gone == nil {
		t.Fatalf("the unmounted base is not listed: %+v", got.Entries)
	}
	if gone.Selectable {
		t.Error("an unmounted base is offered as a root")
	}
	if gone.Reason == nil || *gone.Reason != reasonDoesNotExist {
		t.Errorf("reason = %v, want %q", gone.Reason, reasonDoesNotExist)
	}
}
