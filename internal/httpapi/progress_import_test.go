package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"shelf/internal/userdata"
)

// arch §7.11: "Import merges by book_id, keeping the row with the newer
// updated_at; ?strategy=replace overwrites unconditionally." So a POST that
// names no strategy is a merge. The prefs half follows arch §7.11 AMENDMENT
// A-17 (E-64) instead: a prefs row has no updated_at, so merge keeps the local
// override and only replace overwrites it, and the counts describe items only.
//
// The only fixture that can tell merge from replace is a book whose LOCAL row
// is newer than the document's. The harness seeds exactly one: the zip book,
// read to page 2 at fixedNow. A document about books with no local progress, or
// a re-import of the server's own export (equal timestamps), lands the same way
// under both strategies and pins nothing.
func TestProgressImport_withoutStrategy_mergesAndKeepsNewerLocalRows(t *testing.T) {
	const localPage = 2
	const localFit = "height"
	const incomingPage = 1
	const incomingFit = "original"

	// setup seeds a local fit override on the zip book (the harness already
	// seeds its progress) and returns a document, exported an hour before that
	// progress was written, that disagrees with both.
	setup := func(t *testing.T) (*env, string) {
		t.Helper()
		e := newEnv(t)
		if w := e.jsonBody(http.MethodPut, "/api/books/"+e.bookZipID+"/prefs",
			`{"fit_mode":"`+localFit+`"}`); w.Code != http.StatusOK {
			t.Fatalf("PUT prefs = %d: %s", w.Code, w.Body.String())
		}
		local, err := e.user.GetProgress(t.Context(), e.bookZipID)
		if err != nil {
			t.Fatalf("reading the seeded progress: %v", err)
		}
		if local.LastPage != localPage {
			t.Fatalf("seeded progress = page %d, want %d", local.LastPage, localPage)
		}

		older := fixedNow.Add(-time.Hour).Unix()
		fit := incomingFit
		raw, err := json.Marshal(progressExport{
			Format: "shelf-progress/1", ExportedAt: older, IDVersion: "shelf-id/1",
			Items: []userdata.ExportItem{{
				BookID: e.bookZipID, SeriesID: local.SeriesID, RootName: local.RootName,
				BookPath: local.BookPath, LastPage: incomingPage, PageCount: local.PageCount,
				StartedAt: older, UpdatedAt: older,
			}},
			Prefs: []userdata.ExportPref{{BookID: e.bookZipID, FitMode: &fit}},
		})
		if err != nil {
			t.Fatalf("encoding the document: %v", err)
		}
		return e, string(raw)
	}

	stored := func(t *testing.T, e *env) (page int, fit string) {
		t.Helper()
		p, err := e.user.GetProgress(t.Context(), e.bookZipID)
		if err != nil {
			t.Fatalf("reading progress after the import: %v", err)
		}
		prefs := decodeBody[BookPrefs](t, e.get("/api/books/"+e.bookZipID+"/prefs"), http.StatusOK)
		return p.LastPage, prefs.FitMode
	}

	cases := []struct {
		name                         string
		target                       string
		imported, skipped, conflicts int
		page                         int
		fit                          string
	}{
		{"no strategy", "/api/progress/import", 0, 1, 1, localPage, localFit},
		{"empty strategy", "/api/progress/import?strategy=", 0, 1, 1, localPage, localFit},
		{"explicit merge", "/api/progress/import?strategy=merge", 0, 1, 1, localPage, localFit},
		// The control: the same document DOES land when the caller asks for it,
		// so the rows above were kept by the strategy, not by a document that
		// could not have been written anyway.
		{"replace", "/api/progress/import?strategy=replace", 1, 0, 1, incomingPage, incomingFit},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, doc := setup(t)
			res := decodeBody[userdata.ImportResult](t,
				e.jsonBody(http.MethodPost, tc.target, doc), http.StatusOK)
			if res.Imported != tc.imported || res.Skipped != tc.skipped || res.Conflicts != tc.conflicts {
				t.Errorf("import result = %+v, want imported %d, skipped %d, conflicts %d",
					res, tc.imported, tc.skipped, tc.conflicts)
			}
			page, fit := stored(t, e)
			if page != tc.page {
				t.Errorf("last_page after the import = %d, want %d", page, tc.page)
			}
			if fit != tc.fit {
				t.Errorf("fit_mode after the import = %q, want %q", fit, tc.fit)
			}
		})
	}
}
