package httpapi

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"shelf/internal/config"
	"shelf/internal/index"
	"shelf/internal/source"
	"shelf/internal/userdata"
)

// Amendment A-11 (ruling E-26) — `POST /api/roots` and
// `DELETE /api/roots/{name}`, and the two revisions of 2026-07-30 (R1: the
// removal purges the index and takes effect immediately; R2: the addition shows
// up at once as a pending row).

// --- the gate --------------------------------------------------------------

// TestRootEditing_isForbiddenWhenTheKeyIsOff is the first test of amendment
// A-11 and it was written before a line of A-11 existed, because a security
// gate nobody has watched refuse is not a gate.
//
// `server.allow_root_editing` defaults to **false** (arch §3.2), and ruling
// E-26's whole security argument rests on that default: the shipped `server:`
// block binds every interface and ruling E-8 omits `auth:` entirely, so an
// ungated write API would let anyone who can reach the port make this server
// open — and then serve — any directory it can read. Both verbs must therefore
// answer `403 forbidden` (§7.4), with `detail.reason` naming which of the three
// conditions failed so the UI can give the instruction that actually applies.
func TestRootEditing_isForbiddenWhenTheKeyIsOff(t *testing.T) {
	e := newEnv(t) // no `allow_root_editing` in the fixture: the default applies

	if e.cfg.Server.AllowRootEditing {
		t.Fatal("the fixture has the key on; this test is about the default being off")
	}

	t.Run("POST /api/roots", func(t *testing.T) {
		w := e.jsonBody(http.MethodPost, "/api/roots", `{"path":"/tmp"}`)
		body := errorBody(t, w, http.StatusForbidden, CodeForbidden)
		if body.Detail["reason"] != reasonDisabled {
			t.Errorf("detail.reason = %v, want %q", body.Detail["reason"], reasonDisabled)
		}
	})

	t.Run("DELETE /api/roots/{name}", func(t *testing.T) {
		w := e.do(http.MethodDelete, "/api/roots/"+rootName, nil)
		body := errorBody(t, w, http.StatusForbidden, CodeForbidden)
		if body.Detail["reason"] != reasonDisabled {
			t.Errorf("detail.reason = %v, want %q", body.Detail["reason"], reasonDisabled)
		}
	})

	t.Run("the refusal changed nothing on disk", func(t *testing.T) {
		roots := e.configFileRoots()
		if len(roots) != 1 || roots[0].Name != rootName {
			t.Errorf("the configuration file says %+v", roots)
		}
	})

	t.Run("and the capability is reported as off", func(t *testing.T) {
		s := decodeBody[Settings](t, e.get("/api/settings"), http.StatusOK)
		if s.Server.RootEditingEnabled {
			t.Error("Settings.server.root_editing_enabled is true while both verbs answer 403")
		}
	})
}

// TestRootEditing_gateNamesWhichOfTheThreeConditionsFailed. The three conditions
// are one capability but three different remedies, and a client that only saw
// the status could print none of them.
func TestRootEditing_gateNamesWhichOfTheThreeConditionsFailed(t *testing.T) {
	t.Run("config_inside_root", func(t *testing.T) {
		// The key is ON here: this is the condition that is *not* the key, and
		// it is the one that keeps FR-CFG-005 true — SHELF never writes to a
		// media volume, not even its own configuration.
		e := newEnv(t, withConfigInsideRoot())
		w := e.jsonBody(http.MethodPost, "/api/roots", `{"path":"/tmp"}`)
		body := errorBody(t, w, http.StatusForbidden, CodeForbidden)
		if body.Detail["reason"] != reasonConfigInsideRoot {
			t.Errorf("detail.reason = %v, want %q", body.Detail["reason"], reasonConfigInsideRoot)
		}
		s := decodeBody[Settings](t, e.get("/api/settings"), http.StatusOK)
		if s.Server.RootEditingEnabled {
			t.Error("the capability is reported on while the gate refuses")
		}
	})

	t.Run("no_config_file", func(t *testing.T) {
		// A-10 documents `config_path: ""` as possible: there is then no file to
		// edit, whatever the key says.
		e := newEnv(t, withRootEditing())
		e.cfg.FilePath = ""
		w := e.jsonBody(http.MethodPost, "/api/roots", `{"path":"/tmp"}`)
		body := errorBody(t, w, http.StatusForbidden, CodeForbidden)
		if body.Detail["reason"] != reasonNoConfigFile {
			t.Errorf("detail.reason = %v, want %q", body.Detail["reason"], reasonNoConfigFile)
		}
	})
}

// TestRootEditing_capabilityAndTheEndpointsAgree is impl-plan §0.3's rule for
// this feature, stated as one test: the 403 and the 201 are asserted against the
// same fixture with the key flipped, so neither can be pinned alone.
func TestRootEditing_capabilityAndTheEndpointsAgree(t *testing.T) {
	for _, on := range []bool{false, true} {
		t.Run(map[bool]string{false: "key off", true: "key on"}[on], func(t *testing.T) {
			var opts []envOption
			if on {
				opts = append(opts, withRootEditing())
			}
			e := newEnv(t, opts...)
			dir := t.TempDir()

			s := decodeBody[Settings](t, e.get("/api/settings"), http.StatusOK)
			if s.Server.RootEditingEnabled != on {
				t.Fatalf("root_editing_enabled = %v, want %v", s.Server.RootEditingEnabled, on)
			}

			w := e.jsonBody(http.MethodPost, "/api/roots", `{"path":`+quoteJSON(dir)+`}`)
			wantStatus := http.StatusForbidden
			if on {
				wantStatus = http.StatusCreated
			}
			if w.Code != wantStatus {
				t.Fatalf("POST /api/roots = %d, want %d: %s", w.Code, wantStatus, w.Body.String())
			}

			// And the file agrees with the answer, which is the only thing that
			// survives the restart.
			gotRoots := len(e.configFileRoots())
			wantRoots := 1
			if on {
				wantRoots = 2
			}
			if gotRoots != wantRoots {
				t.Errorf("the configuration file holds %d roots, want %d", gotRoots, wantRoots)
			}
		})
	}
}

// --- POST ------------------------------------------------------------------

// TestCreateRoot_writesTheFileAndAdoptsIt is the happy path, asserted on the
// written file rather than on the response, plus amendment **A-12** (ruling
// **E-40**): the server opens the root and starts a scan of it, so the row is
// live rather than pending and no restart is required.
//
// # What this test replaced, and why the replacement had to be as strong
//
// It used to be `…_writesTheFileAndReportsPending` and asserted the exact
// opposite of the three flags below: `pending: true`, `available: false`, and
// `config_changed_on_disk: true`. Those were A-11 limit (1) — "restart-based,
// not hot-reload" — and they were correct until E-40 overturned it for
// addition. Deleting them and asserting nothing would have left the new
// behaviour unpinned; the *fallback* they described still exists and is pinned
// separately by `TestCreateRoot_fallsBackToPendingWhenTheRootCannotBeOpened`.
func TestCreateRoot_writesTheFileAndAdoptsIt(t *testing.T) {
	e := newEnv(t, withRootEditing())
	dir := filepath.Join(t.TempDir(), "만화 2")
	mustMkdir(t, dir)

	w := e.jsonBody(http.MethodPost, "/api/roots", `{"path":`+quoteJSON(dir)+`,"label":"두 번째"}`)
	entry := decodeBody[RootEntry](t, w, http.StatusCreated)

	if got := w.Header().Get("Location"); got != "/api/roots/"+entry.Name {
		t.Errorf("Location = %q, want %q", got, "/api/roots/"+entry.Name)
	}
	if entry.Path != dir {
		t.Errorf("path = %q, want the cleaned path the caller sent, %q", entry.Path, dir)
	}
	if entry.Label != "두 번째" {
		t.Errorf("label = %q", entry.Label)
	}
	if !entry.Enabled {
		t.Error("enabled = false; a created root is enabled by §3.2's default")
	}
	if !config.ValidRootName(entry.Name) {
		t.Errorf("the generated name %q is not §3.2's [a-zA-Z0-9._-]{1,64}", entry.Name)
	}

	// The written file, not the return value: what the server parses at the next
	// restart is the bytes on disk.
	roots := e.configFileRoots()
	if len(roots) != 2 {
		t.Fatalf("the configuration file holds %+v", roots)
	}
	written := roots[1]
	if written.Name != entry.Name || written.Path != dir || written.Label != "두 번째" {
		t.Errorf("the file says %+v, the response said %+v", written, entry)
	}
	if !written.Enabled {
		t.Error("`enabled` was written as false; the key must not be written at all")
	}

	// A-12: the row is live. Not pending, because this process has opened it;
	// available, because the handle stats; and still carrying no counts, because
	// the scan that fills them has only just been asked to start.
	list := decodeBody[RootsResponse](t, e.get("/api/roots"), http.StatusOK)
	added := findRoot(t, list.Items, entry.Name)
	if added.Pending {
		t.Error("the adopted root is marked pending; E-40 opened it into this server")
	}
	if !added.Available {
		t.Error("the adopted root reports available: false; its os.Root was opened and stats")
	}
	if added.Path != dir || added.Label != "두 번째" {
		t.Errorf("the adopted row lost the path or the label: %+v", added)
	}
	if added.SeriesCount != 0 || added.BookCount != 0 || added.PageCount != 0 || added.TotalBytes != 0 {
		t.Errorf("the adopted root carries counts before its first scan finished: %+v", added)
	}
	if existing := findRoot(t, list.Items, rootName); existing.Pending {
		t.Error("the indexed root was marked pending")
	}

	// The scanner learned the name **before** it was asked to scan it. The other
	// order answers ErrUnknownRoot and is logged as an ordinary "no scan could be
	// started", so nothing but this assertion distinguishes the two.
	if len(e.scan.added) != 1 || e.scan.added[0].Name != entry.Name {
		t.Fatalf("AddConfigRoot calls = %+v, want exactly the new root", e.scan.added)
	}
	if e.scan.addedBeforeStart[0] != 0 {
		t.Error("the scan was started before the scanner was told the root exists")
	}
	if e.scan.starts != 1 {
		t.Fatalf("the scanner was started %d times, want 1", e.scan.starts)
	}
	if got := e.scan.lastReq.Roots; len(got) != 1 || got[0] != entry.Name {
		t.Errorf("the scan targeted %v; an empty list means EVERY root to internal/scanner, "+
			"which would re-walk the whole library for one added directory", got)
	}

	// And the restart notice is **false**: this process and the file agree again,
	// which is the whole point of E-40. Under A-11 this assertion was inverted.
	s := decodeBody[Settings](t, e.get("/api/settings"), http.StatusOK)
	if s.Server.ConfigChangedOnDisk {
		t.Error("config_changed_on_disk is true after an adopted write; " +
			"the server would be telling the user to restart for a change it has already applied")
	}
}

// TestCreateRoot_fallsBackToPendingWhenTheRootCannotBeOpened pins A-11's
// behaviour as **the fallback** it became under ruling E-40.
//
// The adoption is the one part of `POST /api/roots` that can fail after the file
// has been written, and the ruling is explicit that a failure there is still a
// `201`: the entry *is* configured, and rolling the write back would discard the
// user's edit to work around the server's own inability to open a directory it
// had just stat-ed successfully. What the user gets instead is exactly the
// pre-E-40 experience — a pending row and a restart notice — which is why that
// path must stay tested rather than merely documented.
//
// # The failure is reached without a test-only seam, and the route is a real one
//
// Remove a root, then add its directory back before restarting. `DELETE` takes
// the entry out of the file and marks the name removed, but it deliberately does
// **not** close the handle — A-11 revision R1 is explicit that removal is
// honoured through the removed-set rather than by hot-swapping the open set. So
// the name is free in the file and still taken in the `RootSet`, `§7.4`'s
// generator hands out the same slug again (which it does on purpose, so reading
// progress reattaches), and `source.RootSet.Add` refuses with `ErrRootExists`.
//
// That makes this test two things at once: the fallback's pin, and the answer to
// "what happens if I undo a removal before restarting". The answer is A-11's,
// unchanged — a pending row — and it is honest, because `removedRoots` still
// shadows that name for the scanner.
func TestCreateRoot_fallsBackToPendingWhenTheRootCannotBeOpened(t *testing.T) {
	e := newEnv(t, withRootEditing(), withSecondRoot())

	dir, ok := e.srv.roots.(*source.RootSet).Path(rootName)
	if !ok {
		t.Fatalf("the harness root %q is not open; this test would prove nothing", rootName)
	}
	if w := e.do(http.MethodDelete, "/api/roots/"+rootName, nil); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE = %d: %s", w.Code, w.Body.String())
	}

	// The label is what makes the generated name collide. §7.4 slugifies the
	// label when there is one and the directory's base name otherwise, and the
	// harness root's directory is not called `manga` — so re-adding the folder
	// alone would get a fresh name and adopt cleanly. Supplying the label the
	// root had is both the realistic gesture (undoing a removal, name and all)
	// and the one that reproduces the collision.
	entry := decodeBody[RootEntry](t,
		e.jsonBody(http.MethodPost, "/api/roots",
			`{"path":`+quoteJSON(dir)+`,"label":`+quoteJSON(rootName)+`}`),
		http.StatusCreated)
	if entry.Name != rootName {
		t.Fatalf("the generated name is %q, want %q re-issued — §7.4 uniquifies against the "+
			"configuration file, where the removal freed it", entry.Name, rootName)
	}

	// The write stands. The endpoint's contract is about the file.
	if roots := e.configFileRoots(); len(roots) != 2 || roots[1].Name != entry.Name {
		t.Fatalf("the configuration file holds %+v; a failed adoption must not roll the write back", roots)
	}

	list := decodeBody[RootsResponse](t, e.get("/api/roots"), http.StatusOK)
	row := findRoot(t, list.Items, entry.Name)
	if !row.Pending {
		t.Error("the row is not pending; nothing opened this root, so a restart is what loads it")
	}
	if row.Available {
		t.Error("available: true for a root whose directory does not exist")
	}
	if e.scan.starts != 0 {
		t.Error("a scan was started for a root that could not be opened")
	}

	// And the restart notice is true again — because it is true.
	s := decodeBody[Settings](t, e.get("/api/settings"), http.StatusOK)
	if !s.Server.ConfigChangedOnDisk {
		t.Error("config_changed_on_disk is false, but the file on disk gained a root this process never opened")
	}
}

// TestCreateRoot_rejects is §7.4's validation table, one sub-test per rule.
//
// The governing rule behind all of them: this endpoint must never write a file
// the server would refuse to start from. It tells the user to restart, so
// handing them `exit 2` would be a defect of the endpoint rather than of their
// configuration.
func TestCreateRoot_rejects(t *testing.T) {
	e := newEnv(t, withRootEditing())
	parent := t.TempDir()
	file := filepath.Join(parent, "a-file")
	mustWrite(t, file, "not a directory")
	child := filepath.Join(e.media, "군계 하위")
	mustMkdir(t, child)
	holdsStorage := filepath.Dir(e.cfg.Storage.DataDir)
	// A directory whose name carries a control character. Legal on Linux, and
	// `path` is written into a YAML file this server re-parses at startup — see
	// TestCreateRoot_neverWritesAPathItCannotReadBack for what that costs.
	newlineDir := filepath.Join(t.TempDir(), "media\nb")
	mustMkdir(t, newlineDir)

	// The two ways a path can exist and still be unreadable. They are separate
	// branches of validateRootCreate — one fails `os.Stat`, one fails `os.Open`
	// — and deleting either was green before 2026-07-30.
	unopenable := filepath.Join(parent, "unopenable")
	mustMkdir(t, unopenable)
	unsearchable := filepath.Join(parent, "unsearchable")
	mustMkdir(t, filepath.Join(unsearchable, "child"))
	for _, dir := range []string{unopenable, unsearchable} {
		if err := os.Chmod(dir, 0); err != nil {
			t.Fatalf("chmod %s: %v", dir, err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	}

	cases := []struct {
		name         string
		body         string
		field        string
		reason       string
		conflictWith string
		// skipAsRoot marks the cases whose whole premise is a permission the
		// caller does not have, which root always does.
		skipAsRoot bool
	}{
		{name: "no path at all", body: `{}`, field: "path", reason: reasonMissing},
		{name: "an empty path", body: `{"path":""}`, field: "path", reason: reasonMissing},
		{name: "a relative path", body: `{"path":"media/manga"}`, field: "path", reason: reasonNotAbsolute},
		{
			name: "a path that is not there", field: "path", reason: reasonDoesNotExist,
			body: `{"path":` + quoteJSON(filepath.Join(parent, "nope")) + `}`,
		},
		{
			name: "a file", field: "path", reason: reasonNotADirectory,
			body: `{"path":` + quoteJSON(file) + `}`,
		},
		{
			// A directory the server can stat but not open would index as empty
			// and report `available: false` — a root that exists only to look
			// broken. This is validateRootCreate's `os.Open` branch.
			name: "a directory the server cannot open", field: "path", reason: reasonNotReadable,
			body: `{"path":` + quoteJSON(unopenable) + `}`, skipAsRoot: true,
		},
		{
			// And the `os.Stat` branch: the path itself may be fine, but the
			// server cannot traverse to it. `does_not_exist` would be the wrong
			// answer — it is there, and telling the user to create it is advice
			// that cannot work.
			name: "a path the server cannot stat", field: "path", reason: reasonNotReadable,
			body: `{"path":` + quoteJSON(filepath.Join(unsearchable, "child")) + `}`, skipAsRoot: true,
		},
		{
			name: "a directory that is already a root", field: "path", reason: reasonDuplicate,
			body: `{"path":` + quoteJSON(e.media) + `}`, conflictWith: rootName,
		},
		{
			// The same file would sit under two roots and get two series
			// identities, one of which the scanner would resolve first
			// arbitrarily.
			name: "a directory inside a root", field: "path", reason: reasonOverlaps,
			body: `{"path":` + quoteJSON(child) + `}`, conflictWith: rootName,
		},
		{
			name: "a directory that contains a root", field: "path", reason: reasonOverlaps,
			body: `{"path":` + quoteJSON(filepath.Dir(e.media)) + `}`, conflictWith: rootName,
		},
		{
			// FR-CFG-005's mirror: SHELF's own files must never end up under a
			// media volume.
			name: "a directory holding our own storage", field: "path", reason: reasonContainsStorage,
			body: `{"path":` + quoteJSON(holdsStorage) + `}`,
		},
		{
			name: "an over-long label", field: "label", reason: reasonTooLong,
			body: `{"path":` + quoteJSON(parent) + `,"label":"` + strings.Repeat("x", maxRootLabelLen+1) + `"}`,
		},
		{
			name: "a path with a control character", field: "path", reason: reasonControlChars,
			body: `{"path":` + quoteJSON(newlineDir) + `}`,
		},
		{
			name: "a label with a control character", field: "label", reason: reasonControlChars,
			body: `{"path":` + quoteJSON(parent) + `,"label":"a\u0007b"}`,
		},
	}

	// One `env` serves every sub-test — building fifteen would mean fifteen
	// SQLite pairs and fifteen fixture trees to assert one rejection each — so
	// the two "nothing was written" checks below are made against the state as
	// this sub-test found it, not against a constant.
	//
	// The difference only shows when something is already wrong: a case that
	// wrongly answers `201` writes into the shared file, and with `!= 1` every
	// *later* case then failed on the shared-state check instead of on its own
	// rule. The primary signal still fired first, so nothing was hidden — but a
	// table of fifteen reds in which one is the cause is a diagnosis the reader
	// has to do by hand. The assertion is the same one: this request wrote
	// nothing.
	bakPath := e.cfg.AbsFilePath() + config.BackupSuffix

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.skipAsRoot {
				skipUnlessUnprivileged(t, "root reads any directory whatever its mode says")
			}
			rootsBefore := len(e.configFileRoots())
			_, statErr := os.Stat(bakPath)
			noBakBefore := statErr != nil

			w := e.jsonBody(http.MethodPost, "/api/roots", tc.body)
			body := errorBody(t, w, http.StatusBadRequest, CodeBadRequest)
			if body.Detail["field"] != tc.field {
				t.Errorf("detail.field = %v, want %q", body.Detail["field"], tc.field)
			}
			if body.Detail["reason"] != tc.reason {
				t.Errorf("detail.reason = %v, want %q", body.Detail["reason"], tc.reason)
			}
			if tc.conflictWith != "" && body.Detail["conflicts_with"] != tc.conflictWith {
				t.Errorf("detail.conflicts_with = %v, want %q", body.Detail["conflicts_with"], tc.conflictWith)
			}
			// The rejected value is never echoed back: keeping absolute host
			// paths out of the envelope is what lets these be pinned at all.
			for key, value := range body.Detail {
				if s, ok := value.(string); ok && strings.Contains(s, parent) {
					t.Errorf("detail.%s leaks the caller's absolute path: %q", key, s)
				}
			}
			if got := len(e.configFileRoots()); got != rootsBefore {
				t.Fatalf("a rejected request changed the file: %d roots, was %d", got, rootsBefore)
			}
			if _, err := os.Stat(bakPath); err == nil && noBakBefore {
				t.Error("a rejected request still took a .bak")
			}
		})
	}
}

// TestCreateRoot_neverWritesAPathItCannotReadBack states §7.4's governing rule
// as a property instead of as a list: **this endpoint must never write a file
// the server would refuse to start from.**
//
// A directory whose name carries a newline is perfectly legal on Linux. Written
// into a double-quoted YAML scalar the line break *folds to a space*, so the
// file then names a directory that does not exist, `config.Load`'s filesystem
// check refuses it and the next start is `exit 2` — from a request this endpoint
// answered `201` to, echoing the correct path back. Either the request is
// refused, or what the file says is what the response said. There is no third
// acceptable answer, which is why this is written as a property: it holds for
// bytes nobody has thought of yet.
func TestCreateRoot_neverWritesAPathItCannotReadBack(t *testing.T) {
	cases := map[string]string{
		"a newline":         "media\nb",
		"a carriage return": "media\rb",
		"a tab":             "media\tb",
		"a bell":            "media\ab",
	}
	for name, dirName := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, withRootEditing())
			dir := filepath.Join(t.TempDir(), dirName)
			mustMkdir(t, dir)

			w := e.jsonBody(http.MethodPost, "/api/roots", `{"path":`+quoteJSON(dir)+`}`)
			if w.Code == http.StatusBadRequest {
				body := errorBody(t, w, http.StatusBadRequest, CodeBadRequest)
				if body.Detail["field"] != "path" || body.Detail["reason"] != reasonControlChars {
					t.Errorf("detail = %+v, want field=%q reason=%q", body.Detail, "path", reasonControlChars)
				}
				if got := len(e.configFileRoots()); got != 1 {
					t.Errorf("a rejected request changed the file: %d roots", got)
				}
				return
			}

			entry := decodeBody[RootEntry](t, w, http.StatusCreated)
			roots := e.configFileRoots()
			if len(roots) != 2 {
				t.Fatalf("the configuration file holds %+v", roots)
			}
			if roots[1].Path != entry.Path {
				t.Fatalf("the response said path=%q, and the file the next start reads says %q",
					entry.Path, roots[1].Path)
			}
			if _, err := os.Stat(roots[1].Path); err != nil {
				t.Fatalf("the written file names a directory the next start cannot stat: %v", err)
			}
		})
	}
}

// TestCreateRoot_rejectsADisabledRootsDirectoryToo — a disabled root is still in
// the file and still collides the moment it is re-enabled, so the overlap check
// must not skip it.
func TestCreateRoot_rejectsADisabledRootsDirectoryToo(t *testing.T) {
	e := newEnv(t, withRootEditing(), withSecondRoot())
	// Disable the second root in the file the endpoint reads.
	raw, err := os.ReadFile(e.cfg.AbsFilePath())
	if err != nil {
		t.Fatalf("reading the fixture config: %v", err)
	}
	patched := strings.Replace(string(raw),
		"- name: \""+secondRootName+"\"",
		"- name: \""+secondRootName+"\"\n    enabled: false", 1)
	mustWrite(t, e.cfg.AbsFilePath(), patched)

	w := e.jsonBody(http.MethodPost, "/api/roots", `{"path":`+quoteJSON(e.secondRoot)+`}`)
	body := errorBody(t, w, http.StatusBadRequest, CodeBadRequest)
	if body.Detail["reason"] != reasonDuplicate {
		t.Errorf("detail.reason = %v, want %q", body.Detail["reason"], reasonDuplicate)
	}
}

// TestCreateRoot_rejectsAnUnknownBodyField — §7.1's strict decoding, which is
// what stops a typo being silently dropped.
func TestCreateRoot_rejectsAnUnknownBodyField(t *testing.T) {
	e := newEnv(t, withRootEditing())
	w := e.jsonBody(http.MethodPost, "/api/roots", `{"path":"/tmp","name":"mine"}`)
	body := errorBody(t, w, http.StatusBadRequest, CodeBadRequest)
	if body.Detail["field"] != "name" {
		t.Errorf("detail.field = %v, want %q — `name` is server-generated and not settable", body.Detail["field"], "name")
	}
}

// TestCreateRoot_conflictsWhenTheFileCannotBeParsed. A writer that cannot
// understand the file cannot promise to preserve it, and overwriting it would
// destroy an edit the user is halfway through — so nothing is written and no
// `.bak` is taken (§7.4's `409`).
func TestCreateRoot_conflictsWhenTheFileCannotBeParsed(t *testing.T) {
	e := newEnv(t, withRootEditing())
	broken := "roots:\n  - name: \"manga\"\n\tbroken\n"
	mustWrite(t, e.cfg.AbsFilePath(), broken)

	w := e.jsonBody(http.MethodPost, "/api/roots", `{"path":`+quoteJSON(t.TempDir())+`}`)
	body := errorBody(t, w, http.StatusConflict, CodeConflict)
	if body.Detail["reason"] != reasonUnparseable {
		t.Errorf("detail.reason = %v, want %q", body.Detail["reason"], reasonUnparseable)
	}
	if got, err := os.ReadFile(e.cfg.AbsFilePath()); err != nil || string(got) != broken {
		t.Error("the refused write still changed the file")
	}
	if _, err := os.Stat(e.cfg.AbsFilePath() + config.BackupSuffix); err == nil {
		t.Error("the refused write still took a .bak")
	}
}

// TestRootEditing_namesTheFlowSequenceRefusalSeparately.
//
// `roots: [{name: "manga", path: "/m"}]` is **valid YAML and a legal SHELF
// configuration**: `GET /api/roots` reads it, the server starts from it, and
// nothing about it is broken. The writer still has to refuse it, because it
// splices lines rather than re-emitting YAML and re-emitting is what would
// destroy the 15 KB of documentation the file ships (see `rootsfile.go`).
//
// Reporting that refusal as `unparseable` is the defect: the settings screen
// prints "this file can no longer be read" *while it is displaying that file's
// contents on the same screen*, and the user is given no way to act. It has its
// own reason so the UI can say the one thing that fixes it — rewrite `roots:` as
// a block list.
func TestRootEditing_namesTheFlowSequenceRefusalSeparately(t *testing.T) {
	newFlowEnv := func(t *testing.T) (*env, string) {
		t.Helper()
		e := newEnv(t, withRootEditing(), withSecondRoot())
		flow := "server:\n  allow_root_editing: true\nroots: [" +
			"{name: \"" + rootName + "\", path: " + quoteYAMLForTest(e.media) + "}, " +
			"{name: \"" + secondRootName + "\", path: " + quoteYAMLForTest(e.secondRoot) + ", label: \"도서\"}]\n"
		mustWrite(t, e.cfg.AbsFilePath(), flow)

		// The premise, measured rather than assumed: the *reader* is perfectly
		// happy with this file. `docs` has no index row, so R2 lists it as
		// pending — which it can only do by having read the flow sequence.
		list := decodeBody[RootsResponse](t, e.get("/api/roots"), http.StatusOK)
		if p := findRoot(t, list.Items, secondRootName); !p.Pending {
			t.Fatal("GET /api/roots could not read the flow-style file either; this test would prove nothing")
		}
		return e, flow
	}

	assertRefused := func(t *testing.T, e *env, flow string, w *httptest.ResponseRecorder) {
		t.Helper()
		body := errorBody(t, w, http.StatusConflict, CodeConflict)
		if body.Detail["reason"] != reasonNotABlockSequence {
			t.Errorf("detail.reason = %v, want %q; %q is what the UI got, and it made the screen say the file "+
				"cannot be read while it was listing that file's roots",
				body.Detail["reason"], reasonNotABlockSequence, reasonUnparseable)
		}
		if got, err := os.ReadFile(e.cfg.AbsFilePath()); err != nil || string(got) != flow {
			t.Error("the refused write still changed the file")
		}
		if _, err := os.Stat(e.cfg.AbsFilePath() + config.BackupSuffix); err == nil {
			t.Error("the refused write still took a .bak")
		}
	}

	t.Run("POST", func(t *testing.T) {
		e, flow := newFlowEnv(t)
		assertRefused(t, e, flow, e.jsonBody(http.MethodPost, "/api/roots", `{"path":`+quoteJSON(t.TempDir())+`}`))
	})

	t.Run("DELETE", func(t *testing.T) {
		e, flow := newFlowEnv(t)
		assertRefused(t, e, flow, e.do(http.MethodDelete, "/api/roots/"+secondRootName, nil))
	})

	t.Run("a genuinely unparseable file keeps the old reason", func(t *testing.T) {
		// The new reason must not swallow the old one: a file that is not YAML
		// at all is a different problem with a different remedy.
		e := newEnv(t, withRootEditing())
		mustWrite(t, e.cfg.AbsFilePath(), "roots:\n  - name: \"manga\"\n\tbroken\n")
		w := e.jsonBody(http.MethodPost, "/api/roots", `{"path":`+quoteJSON(t.TempDir())+`}`)
		body := errorBody(t, w, http.StatusConflict, CodeConflict)
		if body.Detail["reason"] != reasonUnparseable {
			t.Errorf("detail.reason = %v, want %q", body.Detail["reason"], reasonUnparseable)
		}
	})
}

// TestCreateRoot_generatesTheName pins §7.4's three steps.
//
// The name is not the caller's to choose because it is hashed into every
// series_id and book_id (§3.4, D-14 / D-51): a client that picked it could
// silently reattach a new directory to another root's reading progress.
func TestCreateRoot_generatesTheName(t *testing.T) {
	cases := []struct {
		name, label, path string
		taken             []string
		want              string
	}{
		{name: "an ASCII label", label: "Books", want: "books"},
		{name: "no label, ASCII directory", path: "/mnt/media/manga", want: "manga"},
		{name: "punctuation is kept", label: "my_root-1.0", want: "my_root-1.0"},
		{name: "spaces collapse to one dash", label: "two   words", want: "two-words"},
		{
			// The common case in this collection: a purely Korean label over a
			// purely Korean directory name slugs to nothing.
			name: "an all-Hangul label falls back", label: "만화", path: "/mnt/[만화]", want: "root",
		},
		{name: "leading and trailing dashes are trimmed", label: "[만화] 군계 1~25", want: "1-25"},
		{name: "a collision is numbered", label: "Books", taken: []string{"books"}, want: "books-2"},
		{
			name: "and numbered again", label: "Books",
			taken: []string{"books", "books-2"}, want: "books-3",
		},
		{
			// Step 3 trims the stem rather than overflowing §3.2's 64 bytes.
			name:  "a 64-byte stem makes room for the suffix",
			label: strings.Repeat("a", 70), taken: []string{strings.Repeat("a", 64)},
			want: strings.Repeat("a", 62) + "-2",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			taken := map[string]bool{}
			for _, n := range tc.taken {
				taken[n] = true
			}
			got := uniqueRootName(slugifyRootName(labelOrBase(tc.label, tc.path)), taken)
			if got != tc.want {
				t.Errorf("name = %q, want %q", got, tc.want)
			}
			if !config.ValidRootName(got) {
				t.Errorf("%q is not §3.2's [a-zA-Z0-9._-]{1,64}", got)
			}
		})
	}
}

// TestCreateRoot_uniquifiesAgainstTheConfigurationNotTheIndex is the rule R1
// made load-bearing. An ex-configured root's name is exactly the name a re-added
// directory should get back, because that is what reattaches the reading
// progress `DELETE` kept in user.db.
func TestCreateRoot_uniquifiesAgainstTheConfigurationNotTheIndex(t *testing.T) {
	e := newEnv(t, withRootEditing(), withSecondRoot())

	// Remove `manga` from the configuration. Its index rows go with it (R1), but
	// its name is now free.
	if w := e.do(http.MethodDelete, "/api/roots/"+rootName, nil); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE = %d: %s", w.Code, w.Body.String())
	}

	dir := t.TempDir()
	w := e.jsonBody(http.MethodPost, "/api/roots", `{"path":`+quoteJSON(dir)+`,"label":"manga"}`)
	entry := decodeBody[RootEntry](t, w, http.StatusCreated)
	if entry.Name != rootName {
		t.Errorf("name = %q, want %q: the retired name must be reusable, or the progress user.db kept never reattaches",
			entry.Name, rootName)
	}
}

// --- DELETE ----------------------------------------------------------------

// TestDeleteRoot_purgesTheIndexAndKeepsTheProgress is revision R1, and it
// asserts the two halves separately because they are two different databases
// with two different guarantees.
//
// `index.db` is derived and disposable (arch §3.5), so the removed root's rows
// go — otherwise the row the user deleted keeps appearing after the restart the
// UI demanded, with its series still in the library, which is not what 제거
// means. `user.db` is the file that must never be lost (NFR-DAT-004), so the
// reading progress stays exactly where it was.
func TestDeleteRoot_purgesTheIndexAndKeepsTheProgress(t *testing.T) {
	e := newEnv(t, withRootEditing(), withSecondRoot())
	ctx := t.Context()

	// Before: the root has rows, and one book has been part-read.
	before, err := e.idx.ListSeries(ctx, index.SeriesFilter{Roots: []string{rootName}, Limit: 50})
	if err != nil {
		t.Fatalf("listing series: %v", err)
	}
	if before.Total == 0 {
		t.Fatal("the fixture root has no series; this test would prove nothing")
	}
	progressBefore, err := e.user.GetProgress(ctx, e.bookZipID)
	if err != nil {
		t.Fatalf("reading the seeded progress: %v", err)
	}
	if progressBefore.LastPage == 0 {
		t.Fatal("the fixture has no reading progress; this test would prove nothing")
	}

	w := e.do(http.MethodDelete, "/api/roots/"+rootName, nil)
	if w.Code != http.StatusNoContent {
		t.Fatalf("DELETE = %d, want 204: %s", w.Code, w.Body.String())
	}
	if w.Body.Len() != 0 {
		t.Errorf("204 carried a body: %s", w.Body.String())
	}

	t.Run("the index rows are gone", func(t *testing.T) {
		rows, err := e.idx.ListRoots(ctx)
		if err != nil {
			t.Fatalf("listing roots: %v", err)
		}
		if slices.ContainsFunc(rows, func(r index.Root) bool { return r.Name == rootName }) {
			t.Error("the roots row survived the removal")
		}
		after, err := e.idx.ListSeries(ctx, index.SeriesFilter{Roots: []string{rootName}, Limit: 50})
		if err != nil {
			t.Fatalf("listing series: %v", err)
		}
		if after.Total != 0 {
			t.Errorf("%d series of the removed root are still in the index", after.Total)
		}
		if _, err := e.idx.GetBook(ctx, e.bookZipID); !errors.Is(err, index.ErrNotFound) {
			t.Errorf("a book of the removed root is still in the index: %v", err)
		}
	})

	t.Run("the reading progress is not", func(t *testing.T) {
		// Read straight out of user.db, not through the API: the API would
		// answer 404 for the book either way, and what R1 promises is that the
		// authored data survives.
		got, err := e.user.GetProgress(ctx, e.bookZipID)
		if err != nil {
			t.Fatalf("the progress row is gone: %v", err)
		}
		if got.LastPage != progressBefore.LastPage || got.PageCount != progressBefore.PageCount {
			t.Errorf("progress = %+v, want %+v", got, progressBefore)
		}
		export, err := e.user.Export(ctx)
		if err != nil {
			t.Fatalf("exporting user data: %v", err)
		}
		if !slices.ContainsFunc(export.Items, func(i userdata.ExportItem) bool { return i.BookID == e.bookZipID }) {
			t.Error("the progress export no longer carries the removed root's book")
		}
	})

	t.Run("and the file no longer lists it", func(t *testing.T) {
		roots := e.configFileRoots()
		if slices.ContainsFunc(roots, func(r config.Root) bool { return r.Name == rootName }) {
			t.Errorf("the configuration file still lists it: %+v", roots)
		}
		if len(roots) != 1 {
			t.Errorf("the configuration file holds %d roots, want 1", len(roots))
		}
	})
}

// TestDeleteRoot_takesEffectBeforeTheRestart is the rest of R1: the running
// server stops listing and stops scanning the root, without hot-swapping the
// open root set, the handle pool or the source factory — none of which A-11
// bought.
func TestDeleteRoot_takesEffectBeforeTheRestart(t *testing.T) {
	e := newEnv(t, withRootEditing(), withSecondRoot())
	if w := e.do(http.MethodDelete, "/api/roots/"+secondRootName, nil); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE = %d: %s", w.Code, w.Body.String())
	}

	t.Run("GET /api/roots does not list it", func(t *testing.T) {
		list := decodeBody[RootsResponse](t, e.get("/api/roots"), http.StatusOK)
		if slices.ContainsFunc(list.Items, func(r Root) bool { return r.Name == secondRootName }) {
			t.Errorf("the removed root is still listed: %+v", list.Items)
		}
		if !slices.ContainsFunc(list.Items, func(r Root) bool { return r.Name == rootName }) {
			t.Error("the other root disappeared too")
		}
	})

	t.Run("a scan of it is 404", func(t *testing.T) {
		// Not the `400` an unknown root gets: this name *was* right, and the
		// resource has since gone — the same split §7.1 draws between a
		// malformed id and an unknown one.
		w := e.jsonBody(http.MethodPost, "/api/scan", `{"roots":["`+secondRootName+`"]}`)
		errorBody(t, w, http.StatusNotFound, CodeNotFound)
		if e.scan.starts != 0 {
			t.Error("the scanner was started for a removed root")
		}
	})

	t.Run("and a full scan skips it", func(t *testing.T) {
		w := e.jsonBody(http.MethodPost, "/api/scan", `{}`)
		if w.Code != http.StatusAccepted {
			t.Fatalf("POST /api/scan = %d: %s", w.Code, w.Body.String())
		}
		if got := e.scan.lastReq.Roots; !slices.Equal(got, []string{rootName}) {
			t.Errorf("the full scan ran over %v, want only %v", got, []string{rootName})
		}
	})
}

// TestStartScan_refusesWhenEveryRootThisProcessLoadedWasRemoved is the second
// row of §7.10's `409` table, and nothing watched it: making `len(out) == 0`
// unreachable in `scanRoots` left this whole package green.
//
// The failure it prevents is R1's own defect arriving through the other door.
// An empty `roots[]` means **every enabled configured root** to the scanner
// (`internal/scanner.selectRoots`), so a full-scan request made after the last
// root this process opened has been removed would hand the scanner a list that
// says "all of them" — and re-index precisely the roots the operator just
// deleted, into an index the `DELETE` went out of its way to purge.
//
// # Ruling E-40 moved this test's subject, and the move is recorded rather than
// # papered over
//
// It used to reach `len(out) == 0` by adding a root — "which writes the file and
// opens nothing" — and then removing the two the process had loaded. E-40 makes
// the add *open* the root, so that state is no longer reachable that way and the
// request is now a correct `202`. What the test protects is unchanged and is
// asserted below: the full scan must run over **the named survivor**, never over
// an empty list, because an empty list means "every enabled configured root" to
// `internal/scanner.selectRoots` and would re-index exactly what was removed.
//
// The `409` itself is still reachable — every configured *and* adopted root
// removed — and is pinned directly by `TestScanRoots_refusesWhenNothingIsLeft`
// below, where the state can be built without three HTTP round trips.
func TestStartScan_runsOverTheAdoptedRootWhenTheLoadedOnesWereRemoved(t *testing.T) {
	e := newEnv(t, withRootEditing(), withSecondRoot())

	third := t.TempDir()
	created := decodeBody[RootEntry](t,
		e.jsonBody(http.MethodPost, "/api/roots", `{"path":`+quoteJSON(third)+`}`),
		http.StatusCreated)
	// The premise E-40 changed, measured rather than assumed.
	if len(e.scan.added) != 1 || e.scan.added[0].Name != created.Name {
		t.Fatalf("the added root was not adopted (%+v); the rest of this test assumes it was", e.scan.added)
	}
	startsAfterAdd := e.scan.starts

	for _, name := range []string{rootName, secondRootName} {
		if w := e.do(http.MethodDelete, "/api/roots/"+name, nil); w.Code != http.StatusNoContent {
			t.Fatalf("DELETE %s = %d: %s", name, w.Code, w.Body.String())
		}
	}
	for _, name := range []string{rootName, secondRootName} {
		if !e.srv.rootIsRemoved(name) {
			t.Fatalf("%q is not in the removed set; this test would prove nothing", name)
		}
	}

	w := e.jsonBody(http.MethodPost, "/api/scan", `{}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("POST /api/scan = %d: %s", w.Code, w.Body.String())
	}
	if e.scan.starts != startsAfterAdd+1 {
		t.Fatalf("the scanner ran %d times, want one more than the adoption's %d",
			e.scan.starts, startsAfterAdd)
	}
	if got := e.scan.lastReq.Roots; !slices.Equal(got, []string{created.Name}) {
		t.Errorf("the full scan ran over %v, want only %v. An empty list means \"every enabled root\" to "+
			"internal/scanner.selectRoots, so that run re-indexes exactly the roots the operator removed — "+
			"the defect R1 exists to remove, arriving through POST /api/scan instead of GET /api/roots",
			got, []string{created.Name})
	}
}

// TestScanRoots_refusesWhenNothingIsLeft is §7.10's second `409` row, pinned on
// the function that decides it.
//
// It is a unit test rather than a request because the state — every configured
// and every adopted root removed — costs three HTTP round trips to build through
// the API and is one line to build here, and because what is being pinned is one
// branch of one function. `scanRoots` is the whole of the rule: an empty result
// must become a refusal and must never be passed on as `roots: []`, which the
// scanner reads as "all of them".
func TestScanRoots_refusesWhenNothingIsLeft(t *testing.T) {
	t.Parallel()
	s := &Server{cfg: &config.Config{Roots: []config.Root{
		{Name: "a", Path: "/a", Enabled: true},
		{Name: "b", Path: "/b", Enabled: true},
	}}}
	// Adopted since startup (A-12), and removed like the rest.
	s.addedRoots = []config.Root{{Name: "c", Path: "/c", Enabled: true}}
	for _, name := range []string{"a", "b", "c"} {
		s.markRootRemoved(name)
	}

	out, err := s.scanRoots(nil)
	if err == nil {
		t.Fatalf("scanRoots returned %v and no error; an empty list means EVERY root to the scanner", out)
	}
	var apiErr *apiError
	if !errors.As(err, &apiErr) || apiErr.status != http.StatusConflict {
		t.Fatalf("scanRoots error = %v, want a 409", err)
	}

	// And the adopted root alone is enough to keep it out of the refusal.
	s.removedMu.Lock()
	delete(s.removedRoots, "c")
	s.removedMu.Unlock()
	if out, err := s.scanRoots(nil); err != nil || !slices.Equal(out, []string{"c"}) {
		t.Errorf("scanRoots = (%v, %v), want the adopted root alone. An adopted root is a root this "+
			"process opened, so leaving it out would refuse a scan the server can run", out, err)
	}
}

// TestDeleteRoot_purgesTheIndexBeforeItWritesTheFile pins the *order* of the
// two writes, which §7.4 states, explains at length — and which nothing
// enforced: swapping the two blocks in handleDeleteRoot was fully green.
//
// The order is the whole of the failure design, so it is asserted through the
// failure. §7.4: *"the index purge runs first, then the YAML. If the YAML write
// then fails the request is 500, the root is still configured, and the next scan
// re-indexes it completely — index.db is the half that is allowed to be rebuilt.
// The other order fails the other way: a root gone from the file with its rows
// orphaned in an index that App.reconcileRoots will keep forever, which is the
// exact defect R1 exists to remove."*
//
// So after a YAML write that fails: the file still lists the root, and the index
// rows are **gone**. Rows still present would mean the YAML write ran first.
func TestDeleteRoot_purgesTheIndexBeforeItWritesTheFile(t *testing.T) {
	skipUnlessUnprivileged(t, "root writes into a directory whatever its mode says")
	e := newEnv(t, withRootEditing(), withSecondRoot())
	ctx := t.Context()

	before, err := e.idx.ListSeries(ctx, index.SeriesFilter{Roots: []string{rootName}, Limit: 50})
	if err != nil {
		t.Fatalf("listing series: %v", err)
	}
	if before.Total == 0 {
		t.Fatal("the fixture root has no series; this test would prove nothing")
	}

	// Make the YAML write, and only the YAML write, impossible: `writeFileAtomic`
	// needs to create `.bak` and a temp file next to the configuration, and both
	// are creations in this directory. The databases live in a subdirectory of
	// it, which keeps its own mode, so the purge is unaffected.
	dir := filepath.Dir(e.cfg.AbsFilePath())
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod %s: %v", dir, err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	w := e.do(http.MethodDelete, "/api/roots/"+rootName, nil)
	errorBody(t, w, http.StatusInternalServerError, CodeInternal)

	if roots := e.configFileRoots(); !slices.ContainsFunc(roots, func(r config.Root) bool { return r.Name == rootName }) {
		t.Fatalf("the file changed despite the failed write: %+v", roots)
	}

	rows, err := e.idx.ListRoots(ctx)
	if err != nil {
		t.Fatalf("listing roots: %v", err)
	}
	if slices.ContainsFunc(rows, func(r index.Root) bool { return r.Name == rootName }) {
		t.Error("the index still holds the root after a failed YAML write, so the YAML write ran first. " +
			"§7.4 requires the purge first: the other order orphans rows in an index App.reconcileRoots keeps forever")
	}
	after, err := e.idx.ListSeries(ctx, index.SeriesFilter{Roots: []string{rootName}, Limit: 50})
	if err != nil {
		t.Fatalf("listing series: %v", err)
	}
	if after.Total != 0 {
		t.Errorf("%d series survived the purge that §7.4 runs before the YAML write", after.Total)
	}

	// And the state this leaves is the one §7.4 argues for and the UI can act
	// on: still configured, so the next scan rebuilds it — which `GET /api/roots`
	// reports as a pending row, not as a root that vanished.
	list := decodeBody[RootsResponse](t, e.get("/api/roots"), http.StatusOK)
	if p := findRoot(t, list.Items, rootName); !p.Pending {
		t.Error("the root is listed as loaded after its rows were purged; the next scan has not run yet")
	}
}

// TestDeleteRoot_keepsARemovedRootOutWhenSomethingReinsertsItsRows makes the
// removed-set filter in `handleRoots` observable.
//
// Before 2026-07-30 it was not: disabling `rootIsRemoved` there changed no test,
// because by the time `GET /api/roots` runs the purge has already deleted the
// rows and the YAML entry is gone, so both filters are looking at an empty set.
// The filter is still right — the purge and the listing are not atomic with
// respect to a scan that is already walking — but "right for a reason no test
// watches" is how a guard gets deleted by the next person who measures it.
//
// So the reason is produced: a scan that was mid-flight when the `DELETE` landed
// writes its `roots` row back through `index.UpsertRoot`, exactly as
// `internal/scanner` does. Nothing in `index.DeleteRoot` prevents that. The
// removed-set is what keeps the row off the settings screen anyway.
func TestDeleteRoot_keepsARemovedRootOutWhenSomethingReinsertsItsRows(t *testing.T) {
	e := newEnv(t, withRootEditing(), withSecondRoot())
	ctx := t.Context()

	if w := e.do(http.MethodDelete, "/api/roots/"+rootName, nil); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE = %d: %s", w.Code, w.Body.String())
	}

	if err := e.idx.UpsertRoot(ctx, index.Root{
		Name: rootName, Path: e.media, Label: "만화", Enabled: true,
	}); err != nil {
		t.Fatalf("re-inserting the roots row: %v", err)
	}
	rows, err := e.idx.ListRoots(ctx)
	if err != nil {
		t.Fatalf("listing roots: %v", err)
	}
	if !slices.ContainsFunc(rows, func(r index.Root) bool { return r.Name == rootName }) {
		t.Fatal("the row was not re-inserted; this test would prove nothing")
	}

	list := decodeBody[RootsResponse](t, e.get("/api/roots"), http.StatusOK)
	if slices.ContainsFunc(list.Items, func(r Root) bool { return r.Name == rootName }) {
		t.Errorf("a root removed in this process's lifetime is listed again after a concurrent scan "+
			"re-inserted its index row: %+v", list.Items)
	}
	if !slices.ContainsFunc(list.Items, func(r Root) bool { return r.Name == secondRootName }) {
		t.Error("the other root disappeared too")
	}
}

// TestCreateRoot_listsARootWhoseNameThisProcessHadRemoved is the other half of
// TestCreateRoot_uniquifiesAgainstTheConfigurationNotTheIndex, and until
// 2026-07-30 it failed.
//
// That test establishes the rule: a retired root's name must be reusable, or the
// progress `DELETE` kept in `user.db` never reattaches. It stops at the `201`.
// One line further on, `GET /api/roots` skipped the row — the R2 loop consulted
// the removed-set, and the removed-set is keyed by *name* — so the sequence an
// operator moving a library to a new disk performs (remove the old root, add the
// new directory, whose base name slugs to the same name) answered:
//
//	POST   /api/roots  → 201, Location: /api/roots/manga
//	GET    /api/roots  → the row is not there
//	POST   /api/roots  → 400 duplicate, conflicts_with: "manga"
//
// A root that is in the configuration file, that this server put there, that
// this server refuses to add twice, and that this server will not show. The R2
// row is derived from the file as it is *now*; a name in it after a `DELETE` is
// something a hand-edit, a restored `.bak` or this endpoint put back, and
// pending — in the file, not open, opened by a restart — is exactly true of it.
func TestCreateRoot_listsARootWhoseNameThisProcessHadRemoved(t *testing.T) {
	e := newEnv(t, withRootEditing(), withSecondRoot())

	if w := e.do(http.MethodDelete, "/api/roots/"+rootName, nil); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE = %d: %s", w.Code, w.Body.String())
	}

	// The same collision the name generator is designed to produce: a different
	// directory, the retired name.
	moved := t.TempDir()
	entry := decodeBody[RootEntry](t,
		e.jsonBody(http.MethodPost, "/api/roots", `{"path":`+quoteJSON(moved)+`,"label":"manga"}`),
		http.StatusCreated)
	if entry.Name != rootName {
		t.Fatalf("name = %q, want the retired %q; this test would prove nothing", entry.Name, rootName)
	}

	list := decodeBody[RootsResponse](t, e.get("/api/roots"), http.StatusOK)
	row := findRoot(t, list.Items, rootName)
	if !row.Pending {
		t.Error("the re-added root is listed as loaded; this server has not opened it and cannot until a restart")
	}
	if row.Path != moved {
		t.Errorf("path = %q, want the newly configured %q — the row must come from the file, not from the "+
			"index rows the DELETE purged", row.Path, moved)
	}
	if row.SeriesCount != 0 || row.TotalBytes != 0 {
		t.Errorf("the re-added root carries counts from the root it replaced: %+v", row)
	}

	// The scan side is unchanged and stays unchanged: this process's open handle
	// for that name is still the *old* directory, so scanning it would re-index
	// exactly what the removal purged. No control reaches this — a pending row
	// has no 재스캔 (`web/src/features/overlays/RootsPanel.tsx`) — and the full
	// scan skips it, which is what keeps the two answers consistent rather than
	// contradictory.
	errorBody(t, e.jsonBody(http.MethodPost, "/api/scan", `{"roots":["`+rootName+`"]}`),
		http.StatusNotFound, CodeNotFound)
	if e.scan.starts != 0 {
		t.Error("the scanner was started for a root this process removed")
	}
}

// TestDeleteRoot_refusesTheLastRoot. arch §3.2 requires at least one root and
// `internal/config/validate.go` exits 2 without one, so a removal that emptied
// the list would tell the user to restart into a server that never comes back.
func TestDeleteRoot_refusesTheLastRoot(t *testing.T) {
	e := newEnv(t, withRootEditing())
	ctx := t.Context()

	w := e.do(http.MethodDelete, "/api/roots/"+rootName, nil)
	body := errorBody(t, w, http.StatusConflict, CodeConflict)
	if body.Detail["reason"] != reasonLastRoot {
		t.Errorf("detail.reason = %v, want %q", body.Detail["reason"], reasonLastRoot)
	}

	// Refused means nothing happened — in the file, and in the index. A purge
	// that ran before the refusal would be a silent library wipe.
	if roots := e.configFileRoots(); len(roots) != 1 || roots[0].Name != rootName {
		t.Errorf("the configuration file says %+v", roots)
	}
	rows, err := e.idx.ListRoots(ctx)
	if err != nil {
		t.Fatalf("listing roots: %v", err)
	}
	if !slices.ContainsFunc(rows, func(r index.Root) bool { return r.Name == rootName }) {
		t.Error("the refused removal still purged the index")
	}
	list := decodeBody[RootsResponse](t, e.get("/api/roots"), http.StatusOK)
	if !slices.ContainsFunc(list.Items, func(r Root) bool { return r.Name == rootName }) {
		t.Error("the refused removal still hid the root from GET /api/roots")
	}
}

// TestDeleteRoot_splitsAMalformedNameFromAnUnknownOne — §7.1's rule, applied to
// a configuration identity rather than to an opaque id.
func TestDeleteRoot_splitsAMalformedNameFromAnUnknownOne(t *testing.T) {
	e := newEnv(t, withRootEditing(), withSecondRoot())

	t.Run("a name outside the alphabet is 400", func(t *testing.T) {
		w := e.do(http.MethodDelete, "/api/roots/not%20a%20name", nil)
		body := errorBody(t, w, http.StatusBadRequest, CodeBadRequest)
		if body.Detail["param"] != "name" {
			t.Errorf("detail.param = %v, want %q", body.Detail["param"], "name")
		}
	})

	t.Run("a well-formed name that is not in the file is 404", func(t *testing.T) {
		w := e.do(http.MethodDelete, "/api/roots/nosuchroot", nil)
		errorBody(t, w, http.StatusNotFound, CodeNotFound)
	})

	t.Run("removing the same root twice is 404 the second time", func(t *testing.T) {
		if w := e.do(http.MethodDelete, "/api/roots/"+secondRootName, nil); w.Code != http.StatusNoContent {
			t.Fatalf("first DELETE = %d: %s", w.Code, w.Body.String())
		}
		w := e.do(http.MethodDelete, "/api/roots/"+secondRootName, nil)
		errorBody(t, w, http.StatusNotFound, CodeNotFound)
	})
}

// TestDeleteRoot_isRefusedForARootThatLeftTheFileByHand. `GET /api/roots` lists
// a root the index remembers and the configuration no longer has; pressing 제거
// on that row must say so rather than report success while changing nothing.
func TestDeleteRoot_isRefusedForARootThatLeftTheFileByHand(t *testing.T) {
	e := newEnv(t, withRootEditing(), withSecondRoot())
	mustWrite(t, e.cfg.AbsFilePath(),
		"server:\n  allow_root_editing: true\nroots:\n  - name: \""+secondRootName+"\"\n    path: "+
			quoteYAMLForTest(e.secondRoot)+"\n")

	// The index still has `manga`, so the row is still on the settings screen.
	list := decodeBody[RootsResponse](t, e.get("/api/roots"), http.StatusOK)
	if !slices.ContainsFunc(list.Items, func(r Root) bool { return r.Name == rootName }) {
		t.Fatal("the index row vanished; this test would prove nothing")
	}
	w := e.do(http.MethodDelete, "/api/roots/"+rootName, nil)
	errorBody(t, w, http.StatusNotFound, CodeNotFound)
}

// --- R2, arrived at by hand editing ----------------------------------------

// TestRoots_reportsAConfiguredButUnindexedRootAsPending. R2's flag is not only
// about `POST`: a root added to the file by hand — the workflow C-5 has been
// describing all along — is in exactly the same state, and must read the same
// way.
func TestRoots_reportsAConfiguredButUnindexedRootAsPending(t *testing.T) {
	e := newEnv(t, withSecondRoot()) // note: the gate is OFF; pending is not gated
	list := decodeBody[RootsResponse](t, e.get("/api/roots"), http.StatusOK)

	pending := findRoot(t, list.Items, secondRootName)
	if !pending.Pending {
		t.Error("a configured root with no index row is not marked pending")
	}
	if pending.Label != "도서" {
		t.Errorf("label = %q, want the configured one", pending.Label)
	}
	if pending.Path != e.secondRoot {
		t.Errorf("path = %q, want %q", pending.Path, e.secondRoot)
	}
	if !pending.Enabled {
		t.Error("enabled = false for a root the file enables")
	}
}

// TestRoots_pendingSurvivesAnUnreadableConfigurationFile. `GET /api/roots` is a
// listing: a file that is missing or mid-edit contributes no pending rows, and
// refusing to list what the index *does* know would be the worse answer.
func TestRoots_pendingSurvivesAnUnreadableConfigurationFile(t *testing.T) {
	e := newEnv(t, withSecondRoot())
	if err := os.Remove(e.cfg.AbsFilePath()); err != nil {
		t.Fatalf("removing the fixture config: %v", err)
	}
	list := decodeBody[RootsResponse](t, e.get("/api/roots"), http.StatusOK)
	if !slices.ContainsFunc(list.Items, func(r Root) bool { return r.Name == rootName }) {
		t.Error("the indexed root stopped being listed because the file went away")
	}
	if slices.ContainsFunc(list.Items, func(r Root) bool { return r.Pending }) {
		t.Error("a pending row was invented from a file that is not there")
	}

	// A file that has gone missing differs from the one we loaded, and saying so
	// is more useful than pretending nothing happened.
	s := decodeBody[Settings](t, e.get("/api/settings"), http.StatusOK)
	if !s.Server.ConfigChangedOnDisk {
		t.Error("config_changed_on_disk is false for a configuration file that is no longer there")
	}
}

// TestSettings_configChangedOnDiskIsFalseUntilSomethingChanges. A permanent
// "the file changed" banner is a banner users learn to ignore, so the quiet case
// is pinned as firmly as the loud one.
func TestSettings_configChangedOnDiskIsFalseUntilSomethingChanges(t *testing.T) {
	e := newEnv(t)
	s := decodeBody[Settings](t, e.get("/api/settings"), http.StatusOK)
	if s.Server.ConfigChangedOnDisk {
		t.Fatal("config_changed_on_disk is true on an untouched file")
	}

	// It flips on a comment edit too, which is why the UI must say "the
	// configuration file changed — restart to apply it", never "you must
	// restart".
	raw, err := os.ReadFile(e.cfg.AbsFilePath())
	if err != nil {
		t.Fatalf("reading the fixture config: %v", err)
	}
	mustWrite(t, e.cfg.AbsFilePath(), string(raw)+"\n# a comment, and nothing else\n")
	s = decodeBody[Settings](t, e.get("/api/settings"), http.StatusOK)
	if !s.Server.ConfigChangedOnDisk {
		t.Error("config_changed_on_disk is false after the bytes changed")
	}
}

// AMENDMENT A-13 (ruling E-41) — `DELETE` moves the adopted digest back, so an
// add followed by its own removal leaves no restart notice behind.
//
// # Why this needs its own test, and why neither half's test caught it
//
// A-12 taught the `POST` to move `configDigest` forward, and
// `TestCreateRoot_writesTheFileAndAdoptsIt` pins that. `DELETE` had no matching
// step, and `TestSettings_configChangedOnDiskIsFalseUntilSomethingChanges` pins
// the flag only against a hand-edited file. Both were green while the *sequence*
// was broken: the digest sat at a version that no longer existed on disk, so
// `config_changed_on_disk` reported "the file is not the one this process
// loaded" about a file that was byte-for-byte the one it loaded.
//
// That is docs/HANDOFF.md §6.5 in one line — two checks either side of the seam,
// each watching its own half, and the defect living in the join. `make
// e2e-synthetic` is what found it, and it found it as a cascade: four viewport
// projects share one server, so the first project's add-then-remove left a false
// restart notice on the settings screen for the other three.
func TestDeleteRoot_movesTheAdoptedDigestBackAfterAnAddIsUndone(t *testing.T) {
	e := newEnv(t, withRootEditing())
	before, err := os.ReadFile(e.cfg.AbsFilePath())
	if err != nil {
		t.Fatalf("reading the fixture config: %v", err)
	}

	dir := filepath.Join(t.TempDir(), "잠깐 있었던 루트")
	mustMkdir(t, dir)
	entry := decodeBody[RootEntry](t,
		e.jsonBody(http.MethodPost, "/api/roots", `{"path":`+quoteJSON(dir)+`}`),
		http.StatusCreated)

	// A-12's half, restated here because it is this test's premise: if the add
	// did not move the digest, the assertion after the removal would pass for the
	// wrong reason.
	if s := decodeBody[Settings](t, e.get("/api/settings"), http.StatusOK); s.Server.ConfigChangedOnDisk {
		t.Fatal("config_changed_on_disk is true after a hot add; A-12 moves the baseline forward")
	}

	if w := e.do(http.MethodDelete, "/api/roots/"+entry.Name, nil); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE = %d: %s", w.Code, w.Body.String())
	}

	// The premise of the flag, asserted rather than assumed: `RemoveRoot` splices
	// raw lines instead of re-emitting the document, so undoing an add returns
	// the file byte for byte. Without this, a false `config_changed_on_disk` and
	// a config writer that reformats are indistinguishable from here.
	after, err := os.ReadFile(e.cfg.AbsFilePath())
	if err != nil {
		t.Fatalf("reading the config after the removal: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("add-then-remove did not restore the file:\nbefore %q\nafter  %q", before, after)
	}
	if s := decodeBody[Settings](t, e.get("/api/settings"), http.StatusOK); s.Server.ConfigChangedOnDisk {
		t.Error("config_changed_on_disk is true while the file is the one this process loaded; " +
			"the DELETE left the baseline at the digest the add moved it to")
	}
}

// AMENDMENT A-13 (ruling E-41) — `DELETE` drops the root from A-12's added set,
// so `GET /api/browse` stops calling its directory `duplicate`.
//
// The picker exists so the client never re-derives §7.4's table (arch §7.4a).
// A server answering `selectable` from a list that still holds a removed root
// produces exactly the drift that design forbids, only with the server's
// authority behind it: the row is greyed out with "이미 등록된 루트" while
// `POST /api/roots` would accept the very same path.
func TestDeleteRoot_stopsCallingTheRemovedDirectoryDuplicate(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "돌려줄 폴더")
	mustMkdir(t, dir)
	e := newEnv(t, withBrowseBases(base))

	entry := decodeBody[RootEntry](t,
		e.jsonBody(http.MethodPost, "/api/roots", `{"path":`+quoteJSON(dir)+`}`),
		http.StatusCreated)

	// The premise: while it IS a root, the endpoint refuses it. An assertion that
	// only looked at the state after the removal would pass against an endpoint
	// that called everything selectable.
	held := browseEntryFor(t, e, base, dir)
	if held.Selectable || held.Reason == nil || *held.Reason != reasonDuplicate {
		t.Fatalf("while configured, the directory browsed as %+v, want selectable=false duplicate", held)
	}

	if w := e.do(http.MethodDelete, "/api/roots/"+entry.Name, nil); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE = %d: %s", w.Code, w.Body.String())
	}

	got := browseEntryFor(t, e, base, dir)
	if !got.Selectable || got.Reason != nil {
		t.Errorf("after the removal the directory browses as %+v, want selectable with no reason; "+
			"configuredRoots() is still holding the removed root", got)
	}
	// And the two agree, which is the property the picker is built on: the flag
	// is only worth rendering if `POST` answers the same way.
	if w := e.jsonBody(http.MethodPost, "/api/roots", `{"path":`+quoteJSON(dir)+`}`); w.Code != http.StatusCreated {
		t.Errorf("POST for a directory browse called selectable = %d: %s", w.Code, w.Body.String())
	}
}

// browseEntryFor is `GET /api/browse?path=base` narrowed to one child.
func browseEntryFor(t *testing.T, e *env, base, child string) BrowseEntry {
	t.Helper()
	body := decodeBody[BrowseResponse](t, browse(e, base), http.StatusOK)
	for _, entry := range body.Entries {
		if entry.Path == child {
			return entry
		}
	}
	t.Fatalf("%q is not among the %d entries browse listed under %q", child, len(body.Entries), base)
	return BrowseEntry{}
}

// TestUnprivilegedVerdict pins the opt-in that stops a root CI reporting green
// over coverage it does not have.
//
// Three cases in this package can only be produced by a process that can be
// denied a permission, and `t.Skip` under uid 0 is the only honest answer for
// them. What is not honest is a containerised CI — uid 0 by default — losing
// three assertions and printing PASS. `SHELF_REQUIRE_UNPRIVILEGED=1` turns that
// skip into a failure for anyone who wants to demand the coverage.
//
// The decision is a pure function precisely so this test can exist: a suite that
// could exercise the uid-0 branch by running is a suite already running as root,
// which is the situation the guard is about.
func TestUnprivilegedVerdict(t *testing.T) {
	cases := []struct {
		name    string
		euid    int
		require string
		want    unprivilegedVerdict
	}{
		{name: "an ordinary user runs the case", euid: 1000, want: runCase},
		{name: "an ordinary user is unaffected by the variable", euid: 1000, require: "1", want: runCase},
		{name: "root skips by default", euid: 0, want: skipCase},
		{name: "root fails when the coverage is demanded", euid: 0, require: "1", want: failCase},
		{
			// Only `1`. An empty or accidental value must not turn every root
			// run red — the variable is an opt-in, and a CI that inherited
			// `SHELF_REQUIRE_UNPRIVILEGED=` from somewhere asked for nothing.
			name: "an empty value is not an opt-in", euid: 0, require: "", want: skipCase,
		},
		{name: "any other value is not an opt-in", euid: 0, require: "true", want: skipCase},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := verdictForEUID(tc.euid, tc.require); got != tc.want {
				t.Errorf("verdictForEUID(%d, %q) = %v, want %v", tc.euid, tc.require, got, tc.want)
			}
		})
	}
}

// --- helpers ---------------------------------------------------------------

func findRoot(t *testing.T, items []Root, name string) Root {
	t.Helper()
	for _, r := range items {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("no root named %q in %+v", name, items)
	return Root{}
}

// quoteJSON renders a string as a JSON literal, for the request bodies above.
func quoteJSON(s string) string { return `"` + jsonEscape(s) + `"` }

// quoteYAMLForTest renders a string as a double-quoted YAML scalar.
func quoteYAMLForTest(s string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
}
