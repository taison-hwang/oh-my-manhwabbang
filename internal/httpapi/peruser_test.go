package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"shelf/internal/principal"
	"shelf/internal/userdata"
)

// asUser puts a principal on the request the way the oidc session gate will.
func asUser(sub string) func(*http.Request) {
	return func(r *http.Request) {
		*r = *r.WithContext(principal.With(r.Context(), principal.Principal{Sub: sub}))
	}
}

func (e *env) as(sub, method, target, body string) *httptest.ResponseRecorder {
	e.t.Helper()
	var rd *strings.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	return e.do(method, target, readerOrNil(rd), asUser(sub), func(r *http.Request) {
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
	})
}

func readerOrNil(r *strings.Reader) *strings.Reader {
	if r == nil {
		return strings.NewReader("")
	}
	return r
}

// userDataEndpoints are the routes that read or write per-user data. Each must
// refuse a per-user server's request that carries no principal.
func userDataEndpoints(e *env) []struct{ method, target, body string } {
	return []struct{ method, target, body string }{
		{http.MethodGet, "/api/series", ""},
		{http.MethodGet, "/api/series/" + e.seriesFolderID, ""},
		{http.MethodGet, "/api/books/" + e.bookZipID, ""},
		{http.MethodGet, "/api/continue", ""},
		{http.MethodPut, "/api/books/" + e.bookZipID + "/progress", `{"page":1}`},
		{http.MethodDelete, "/api/books/" + e.bookZipID + "/progress", ""},
		{http.MethodGet, "/api/books/" + e.bookZipID + "/prefs", ""},
		{http.MethodPut, "/api/books/" + e.bookZipID + "/prefs", `{"reading_direction":"rtl"}`},
		{http.MethodGet, "/api/settings", ""},
		{http.MethodPut, "/api/settings", `{"theme":"dark"}`},
		{http.MethodGet, "/api/progress/export", ""},
		{http.MethodPost, "/api/progress/import", `{}`},
	}
}

// A per-user server must never fall through to the single-user tables for a
// request that has no principal: those hold the pre-E-74 owner's data.
func TestPerUser_requestWithoutPrincipal_is401_onEveryUserDataEndpoint(t *testing.T) {
	e := newEnv(t, withPerUser())
	for _, c := range userDataEndpoints(e) {
		t.Run(c.method+" "+c.target, func(t *testing.T) {
			w := e.jsonBody(c.method, c.target, c.body)
			errorBody(t, w, http.StatusUnauthorized, CodeUnauthorized)
		})
	}
}

// Without per-user mode a principal changes nothing: the server keeps reading
// the tables it always has.
func TestNotPerUser_principalIsIgnored_andLegacyTablesServe(t *testing.T) {
	e := newEnv(t)
	w := e.do(http.MethodGet, "/api/books/"+e.bookZipID, nil, asUser("alice"))
	got := decodeBody[BookDetail](t, w, http.StatusOK)
	if got.Progress == nil || got.Progress.LastPage != 2 {
		t.Fatalf("progress = %+v, want the seeded single-user page 2", got.Progress)
	}
}

// The seeded single-user progress belongs to the pre-E-74 owner. Nobody sees it
// until it is migrated to them.
func TestPerUser_legacyProgressIsInvisible(t *testing.T) {
	e := newEnv(t, withPerUser())
	got := decodeBody[BookDetail](t, e.as("alice", http.MethodGet, "/api/books/"+e.bookZipID, ""), http.StatusOK)
	if got.Progress != nil {
		t.Fatalf("alice sees %+v from the single-user table", got.Progress)
	}
	list := decodeBody[SeriesListResponse](t, e.as("alice", http.MethodGet, "/api/series", ""), http.StatusOK)
	for _, s := range list.Items {
		if s.Progress.BooksStarted != 0 || s.Progress.LastReadAt != nil {
			t.Fatalf("series %s carries single-user progress: %+v", s.ID, s.Progress)
		}
	}
	cont := decodeBody[ContinueResponse](t, e.as("alice", http.MethodGet, "/api/continue", ""), http.StatusOK)
	if len(cont.Items) != 0 {
		t.Fatalf("continue lists the single-user owner's books: %+v", cont.Items)
	}
}

func TestPerUser_progressIsPrivate_acrossListContinueDetailAndExport(t *testing.T) {
	e := newEnv(t, withPerUser())
	bid := e.bookZipID

	put := e.as("alice", http.MethodPut, "/api/books/"+bid+"/progress", `{"page":1}`)
	if put.Code != http.StatusOK {
		t.Fatalf("alice PUT progress = %d: %s", put.Code, put.Body.String())
	}

	// BookSummary.progress, book detail.
	a := decodeBody[BookDetail](t, e.as("alice", http.MethodGet, "/api/books/"+bid, ""), http.StatusOK)
	if a.Progress == nil || a.Progress.LastPage != 1 {
		t.Fatalf("alice's own progress = %+v, want page 1", a.Progress)
	}
	b := decodeBody[BookDetail](t, e.as("bob", http.MethodGet, "/api/books/"+bid, ""), http.StatusOK)
	if b.Progress != nil {
		t.Fatalf("bob sees alice's progress: %+v", b.Progress)
	}

	// BookSummary.progress inside the series detail, and the series rollup.
	sa := decodeBody[SeriesDetail](t, e.as("alice", http.MethodGet, "/api/series/"+e.seriesFolderID, ""), http.StatusOK)
	sb := decodeBody[SeriesDetail](t, e.as("bob", http.MethodGet, "/api/series/"+e.seriesFolderID, ""), http.StatusOK)
	if sa.Progress.BooksStarted != 1 || sa.Progress.LastBookID == nil || *sa.Progress.LastBookID != bid {
		t.Fatalf("alice's series rollup = %+v", sa.Progress)
	}
	if sb.Progress.BooksStarted != 0 || sb.Progress.LastBookID != nil || sb.Progress.LastReadAt != nil {
		t.Fatalf("bob's series rollup leaks alice: %+v", sb.Progress)
	}
	for _, bk := range sb.Books {
		if bk.Progress != nil {
			t.Fatalf("bob's series detail shows progress for %s: %+v", bk.ID, bk.Progress)
		}
	}

	// Series list: the progress field, the "reading" filter and the recent sort.
	la := decodeBody[SeriesListResponse](t, e.as("alice", http.MethodGet, "/api/series?progress=reading", ""), http.StatusOK)
	lb := decodeBody[SeriesListResponse](t, e.as("bob", http.MethodGet, "/api/series?progress=reading", ""), http.StatusOK)
	if la.Total != 1 || la.Items[0].ID != e.seriesFolderID {
		t.Fatalf("alice's reading filter = %+v", la)
	}
	if lb.Total != 0 {
		t.Fatalf("bob's reading filter matched %d series", lb.Total)
	}
	ub := decodeBody[SeriesListResponse](t, e.as("bob", http.MethodGet, "/api/series?progress=unread", ""), http.StatusOK)
	ua := decodeBody[SeriesListResponse](t, e.as("alice", http.MethodGet, "/api/series?progress=unread", ""), http.StatusOK)
	if ub.Total != ua.Total+1 {
		t.Fatalf("unread: bob %d, alice %d; bob must have exactly alice's series on top", ub.Total, ua.Total)
	}
	recent := decodeBody[SeriesListResponse](t, e.as("bob", http.MethodGet, "/api/series?sort=recent", ""), http.StatusOK)
	for _, s := range recent.Items {
		if s.Progress.LastReadAt != nil {
			t.Fatalf("bob's recent sort exposes alice's last_read_at on %s", s.ID)
		}
	}

	// /api/continue.
	ca := decodeBody[ContinueResponse](t, e.as("alice", http.MethodGet, "/api/continue", ""), http.StatusOK)
	cb := decodeBody[ContinueResponse](t, e.as("bob", http.MethodGet, "/api/continue", ""), http.StatusOK)
	if len(ca.Items) != 1 || ca.Items[0].Book.ID != bid {
		t.Fatalf("alice's continue = %+v", ca.Items)
	}
	if len(cb.Items) != 0 {
		t.Fatalf("bob's continue lists %+v", cb.Items)
	}

	// Export carries only the caller's rows; the file has no user key.
	ea := decodeBody[userdata.Export](t, e.as("alice", http.MethodGet, "/api/progress/export", ""), http.StatusOK)
	eb := decodeBody[userdata.Export](t, e.as("bob", http.MethodGet, "/api/progress/export", ""), http.StatusOK)
	if len(ea.Items) != 1 || ea.Items[0].BookID != bid {
		t.Fatalf("alice's export items = %+v", ea.Items)
	}
	if len(eb.Items) != 0 {
		t.Fatalf("bob's export items = %+v", eb.Items)
	}
	if strings.Contains(e.as("alice", http.MethodGet, "/api/progress/export", "").Body.String(), "alice") {
		t.Fatal("the export file names the user")
	}

	// Deleting is also scoped: bob's DELETE leaves alice's row alone.
	if w := e.as("bob", http.MethodDelete, "/api/books/"+bid+"/progress", ""); w.Code != http.StatusNoContent {
		t.Fatalf("bob DELETE = %d", w.Code)
	}
	a2 := decodeBody[BookDetail](t, e.as("alice", http.MethodGet, "/api/books/"+bid, ""), http.StatusOK)
	if a2.Progress == nil || a2.Progress.LastPage != 1 {
		t.Fatalf("bob's DELETE removed alice's progress: %+v", a2.Progress)
	}
}

func TestPerUser_importWritesTheCallersRows(t *testing.T) {
	e := newEnv(t, withPerUser())
	bid := e.bookZipID
	if w := e.as("alice", http.MethodPut, "/api/books/"+bid+"/progress", `{"page":2}`); w.Code != http.StatusOK {
		t.Fatalf("seed alice: %d", w.Code)
	}
	doc := e.as("alice", http.MethodGet, "/api/progress/export", "").Body.String()

	// bob imports alice's file: it lands on bob and alice is unchanged.
	if w := e.as("bob", http.MethodPost, "/api/progress/import", doc); w.Code != http.StatusOK {
		t.Fatalf("bob import = %d: %s", w.Code, w.Body.String())
	}
	b := decodeBody[BookDetail](t, e.as("bob", http.MethodGet, "/api/books/"+bid, ""), http.StatusOK)
	if b.Progress == nil || b.Progress.LastPage != 2 {
		t.Fatalf("bob's imported progress = %+v", b.Progress)
	}
	ca := decodeBody[ContinueResponse](t, e.as("carol", http.MethodGet, "/api/continue", ""), http.StatusOK)
	if len(ca.Items) != 0 {
		t.Fatalf("carol sees imported rows: %+v", ca.Items)
	}
}

func TestPerUser_prefsAndSettingsArePrivate(t *testing.T) {
	e := newEnv(t, withPerUser())
	bid := e.bookZipID
	if w := e.as("alice", http.MethodPut, "/api/books/"+bid+"/prefs", `{"reading_direction":"rtl","fit_mode":"width"}`); w.Code != http.StatusOK {
		t.Fatalf("alice PUT prefs = %d: %s", w.Code, w.Body.String())
	}
	if w := e.as("alice", http.MethodPut, "/api/settings", `{"theme":"dark","library_sort":"mtime"}`); w.Code != http.StatusOK {
		t.Fatalf("alice PUT settings = %d: %s", w.Code, w.Body.String())
	}

	pa := decodeBody[BookPrefs](t, e.as("alice", http.MethodGet, "/api/books/"+bid+"/prefs", ""), http.StatusOK)
	pb := decodeBody[BookPrefs](t, e.as("bob", http.MethodGet, "/api/books/"+bid+"/prefs", ""), http.StatusOK)
	if !pa.IsOverride || pa.ReadingDirection != "rtl" {
		t.Fatalf("alice's prefs = %+v", pa)
	}
	if pb.IsOverride {
		t.Fatalf("bob inherits alice's book prefs: %+v", pb)
	}
	da := decodeBody[BookDetail](t, e.as("alice", http.MethodGet, "/api/books/"+bid, ""), http.StatusOK)
	db := decodeBody[BookDetail](t, e.as("bob", http.MethodGet, "/api/books/"+bid, ""), http.StatusOK)
	if da.Prefs.ReadingDirection != "rtl" || db.Prefs.ReadingDirection == "rtl" {
		t.Fatalf("detail prefs: alice %q, bob %q", da.Prefs.ReadingDirection, db.Prefs.ReadingDirection)
	}

	sa := decodeBody[Settings](t, e.as("alice", http.MethodGet, "/api/settings", ""), http.StatusOK)
	sb := decodeBody[Settings](t, e.as("bob", http.MethodGet, "/api/settings", ""), http.StatusOK)
	if sa.Theme != "dark" || sa.LibrarySort != "mtime" {
		t.Fatalf("alice's settings = %+v", sa)
	}
	if sb.Theme == "dark" || sb.LibrarySort == "mtime" {
		t.Fatalf("bob inherits alice's settings: %+v", sb)
	}
}

// A cover is one image for every user (ruling E-74 §2.13): neither a book
// preference nor the default reading direction of whoever asks may change it,
// and the series' own direction — shared data — does.
func TestPerUser_coverDoesNotDependOnTheRequester(t *testing.T) {
	e := newEnv(t, withPerUser())
	bid := e.bookCloverID
	cover := func(sub string) string {
		return decodeBody[BookDetail](t, e.as(sub, http.MethodGet, "/api/books/"+bid, ""), http.StatusOK).CoverCV
	}
	base := cover("bob")
	if got := cover("alice"); got != base {
		t.Fatalf("before any preference, alice's cover_cv %q differs from bob's %q", got, base)
	}

	// Alice flips her book's direction and her default direction.
	flip := "rtl"
	if e.cfg.Reader.ReadingDirection == "rtl" {
		flip = "ltr"
	}
	if w := e.as("alice", http.MethodPut, "/api/books/"+bid+"/prefs", `{"reading_direction":"`+flip+`"}`); w.Code != http.StatusOK {
		t.Fatalf("alice book prefs = %d", w.Code)
	}
	if w := e.as("alice", http.MethodPut, "/api/settings", `{"reading_direction":"`+flip+`"}`); w.Code != http.StatusOK {
		t.Fatalf("alice settings = %d", w.Code)
	}
	if got := cover("alice"); got != base {
		t.Fatalf("alice's own preferences moved the shared cover: %q -> %q", base, got)
	}
	if got := cover("bob"); got != base {
		t.Fatalf("alice's preferences moved bob's cover: %q -> %q", base, got)
	}

	// The series' direction is shared data and does change it, for everyone.
	w := e.as("bob", http.MethodPut, "/api/series/"+e.seriesCloverID+"/prefs", `{"reading_direction":"`+flip+`"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("series prefs = %d: %s", w.Code, w.Body.String())
	}
	if got := cover("alice"); got == base {
		t.Fatal("the series direction did not reach the cover")
	}
	if cover("alice") != cover("bob") {
		t.Fatal("the cover differs between users after a series direction change")
	}
}
