package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"shelf/internal/index"
	"shelf/internal/principal"
	"shelf/internal/scanner"
	"shelf/internal/userdata"
)

func asUser(sub string) context.Context {
	return principal.With(context.Background(), principal.Principal{Sub: sub})
}

func userReadTo(t *testing.T, a *App, sub string, b index.SplitVolume, rel string, page int) {
	t.Helper()
	if _, err := a.user.PutProgress(asUser(sub), userdata.ProgressUpdate{
		BookID: b.BookID, SeriesID: b.SeriesID, RootName: "fx", BookPath: rel,
		Page: page, PageCount: b.PageCount,
	}); err != nil {
		t.Fatalf("PutProgress %s %s: %v", sub, b.BookID, err)
	}
}

func userProgress(t *testing.T, a *App, sub, id string) (userdata.Progress, bool) {
	t.Helper()
	p, err := a.user.GetProgress(asUser(sub), id)
	if err != nil {
		return userdata.Progress{}, false
	}
	return p, true
}

// A rename moves every reader's place, not just the single-user table's
// (ruling E-74 §2.13: the post-scan cleanup applies the same rules per user).
func TestAfterScan_perUser_renameCarriesEveryUsersProgressAndPrefs(t *testing.T) {
	media := cleanupLibrary(t)
	a := newTestApp(t, testConfig(t, media), false)
	scanNow(t, a, false)

	old := bookAt(t, a, "낱권.zip")
	readTo(t, a, old, "낱권.zip", 2) // the single-user reader
	userReadTo(t, a, "alice", old, "낱권.zip", 3)
	userReadTo(t, a, "bob", old, "낱권.zip", 5)
	if _, err := a.user.PutPrefs(asUser("alice"), old.BookID, userdata.PrefsPatch{ReadingDir: userdata.SetPatch("rtl")}); err != nil {
		t.Fatal(err)
	}

	mustRename(t, media, "낱권.zip", "낱권 (완).zip")
	scanNow(t, a, false)
	moved := bookAt(t, a, "낱권 (완).zip")

	for sub, want := range map[string]int{"alice": 3, "bob": 5} {
		if p, ok := userProgress(t, a, sub, moved.BookID); !ok || p.LastPage != want {
			t.Errorf("%s: progress on the moved book = (%+v, %v), want page %d", sub, p, ok, want)
		}
		if _, ok := userProgress(t, a, sub, old.BookID); ok {
			t.Errorf("%s still has a row under the old id", sub)
		}
	}
	if p, ok := progressRow(t, a, moved.BookID); !ok || p.LastPage != 2 {
		t.Errorf("single-user progress on the moved book = (%+v, %v), want page 2", p, ok)
	}
	if p, err := a.user.GetPrefs(asUser("alice"), moved.BookID); err != nil || p.ReadingDir == nil || *p.ReadingDir != "rtl" {
		t.Errorf("alice's prefs did not follow the book: %+v, %v", p, err)
	}
	if p, _ := a.user.GetPrefs(asUser("bob"), moved.BookID); p.ReadingDir != nil {
		t.Errorf("bob received alice's prefs: %+v", p)
	}
}

// A vanished book is purged from each user's rows; a user's row for a book that
// is still there is kept, and one user's purge never touches another's.
func TestAfterScan_perUser_purgeIsPerUser(t *testing.T) {
	media := cleanupLibrary(t)
	a := newTestApp(t, testConfig(t, media), false)
	scanNow(t, a, false)

	gone := bookAt(t, a, "낱권.zip")
	stays := bookAt(t, a, "남을 시리즈/01권.zip")
	userReadTo(t, a, "alice", gone, "낱권.zip", 2)
	userReadTo(t, a, "bob", gone, "낱권.zip", 4)
	userReadTo(t, a, "bob", stays, "남을 시리즈/01권.zip", 1)

	mustRemove(t, media, "낱권.zip")
	scanNow(t, a, false)

	for _, sub := range []string{"alice", "bob"} {
		if _, ok := userProgress(t, a, sub, gone.BookID); ok {
			t.Errorf("%s: progress for the deleted book survived a completed walk", sub)
		}
	}
	if p, ok := userProgress(t, a, "bob", stays.BookID); !ok || p.LastPage != 1 {
		t.Errorf("bob's progress for a book that is still there = (%+v, %v)", p, ok)
	}
}

func newPerUserTestApp(t *testing.T, media string) *App {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	a, err := New(t.Context(), Options{
		Config: testConfig(t, media), Logger: quietLogger(), Listener: ln, PerUser: true,
	})
	if err != nil {
		_ = ln.Close()
		t.Fatalf("app.New: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return a
}

// The cover the scan pre-generates and the cover GET /api/books/{bid}/cover
// serves follow one rule (ruling E-74 §2.13). Here the single-user table holds
// a book direction that would flip the cover if the pre-generation read it: if
// it did, the image it cached would not be the one the endpoint asks for and the
// request carrying the current cover_cv would not be a cache hit.
func TestAfterScan_perUser_pregeneratedCoverIsTheOneTheEndpointServes(t *testing.T) {
	media := cleanupLibrary(t)
	a := newPerUserTestApp(t, media)
	cfg := a.cfg
	stop := runApp(t, a)
	defer stop()
	scanNow(t, a, false)

	old := bookAt(t, a, "낱권.zip")
	setDir(t, a, old.BookID, "rtl") // single-user table: a per-user server must ignore it
	mustRename(t, media, "낱권.zip", "새폴더/낱권.zip")
	scanNow(t, a, false)
	moved := bookAt(t, a, "새폴더/낱권.zip")

	deadline := time.Now().Add(5 * time.Second)
	for {
		st := a.thumb.Stats()
		if st.CoverDepth == 0 && st.PageDepth == 0 && st.Active == 0 && st.Inflight == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("thumbnail queue did not drain")
		}
		time.Sleep(10 * time.Millisecond)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/books/"+moved.BookID, nil)
	req = req.WithContext(principal.With(req.Context(), principal.Principal{Sub: "alice"}))
	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, req)
	var detail struct {
		CoverCV string `json:"cover_cv"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &detail) != nil {
		t.Fatalf("book detail = %d: %s", rec.Code, rec.Body.String())
	}
	for _, w := range cfg.Thumbnails.Widths {
		path := fmt.Sprintf("/api/books/%s/cover?w=%d&v=%s", moved.BookID, w, detail.CoverCV)
		if status := getJSON(t, a, path, nil); status != http.StatusOK {
			t.Errorf("width=%d: HTTP %d, want a pre-generated 200", w, status)
		}
	}
}

// What the scan pre-generates for a page cover ignores the single-user tables on
// a per-user server and reads them on any other: the same rule as the endpoint.
func TestCoverBridge_pageRequest_perUserIgnoresBookAndSettingsDirections(t *testing.T) {
	media := cleanupLibrary(t)
	a := newTestApp(t, testConfig(t, media), false)
	scanNow(t, a, false)
	b := bookAt(t, a, "낱권.zip")
	setDir(t, a, b.BookID, "rtl")
	if err := a.user.Settings().Put(t.Context(), "reading_direction", "rtl"); err != nil {
		t.Fatal(err)
	}
	row, err := a.idx.GetBook(t.Context(), b.BookID)
	if err != nil {
		t.Fatal(err)
	}
	global := a.cfg.Reader.ReadingDirection
	if global == "rtl" {
		t.Fatalf("the fixture's configured direction is already rtl; the test could not tell the rules apart")
	}

	for _, c := range []struct {
		perUser bool
		want    string
	}{{true, global}, {false, "rtl"}} {
		bridge := newCoverBridge(a.thumb, a.idx, a.user, global, c.perUser)
		req, err := bridge.pageRequest(t.Context(), row)
		if err != nil {
			t.Fatal(err)
		}
		if req.Cover == nil || req.Cover.Dir != c.want {
			t.Errorf("perUser=%v: cover direction = %+v, want %q", c.perUser, req.Cover, c.want)
		}
	}
}

// A scan started by a request keeps that request's principal in its context.
// The cleanup must still treat the single-user tables as its first scope, or a
// rename carried under the admin's identity would clear the move evidence while
// leaving the single-user rows behind.
func TestAfterScan_perUser_scanStartedUnderAPrincipalStillCleansTheSingleUserTables(t *testing.T) {
	media := cleanupLibrary(t)
	a := newTestApp(t, testConfig(t, media), false)
	scanNow(t, a, false)

	old := bookAt(t, a, "낱권.zip")
	readTo(t, a, old, "낱권.zip", 2)
	userReadTo(t, a, "admin", old, "낱권.zip", 4)

	mustRename(t, media, "낱권.zip", "낱권 (완).zip")
	if _, err := a.scan.Run(asUser("admin"), scanner.Request{}); err != nil {
		t.Fatal(err)
	}
	moved := bookAt(t, a, "낱권 (완).zip")

	if p, ok := progressRow(t, a, moved.BookID); !ok || p.LastPage != 2 {
		t.Errorf("single-user progress on the moved book = (%+v, %v), want page 2", p, ok)
	}
	if p, ok := userProgress(t, a, "admin", moved.BookID); !ok || p.LastPage != 4 {
		t.Errorf("admin's progress on the moved book = (%+v, %v), want page 4", p, ok)
	}
}
