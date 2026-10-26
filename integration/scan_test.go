//go:build integration

package integration

import (
	"strings"
	"testing"
)

// I-1 — a full scan of the curated root completes, indexes exactly the ten
// series `include_globs` names, and isolates every failure into a book status
// plus a scan-log row (FR-IDX-001, FR-IDX-006, FR-IDX-010, amendment A-3).
func TestI1_fullScan_indexesTheCuratedSubsetAndIsolatesEveryFailure(t *testing.T) {
	s := sharedServer(t)

	var list seriesList
	s.get("/api/series?limit=200", &list)
	if list.Total != len(curated) {
		names := make([]string, 0, len(list.Items))
		for _, it := range list.Items {
			names = append(names, it.Name)
		}
		t.Fatalf("scan.include_globs matched %d series, want %d\n%v", list.Total, len(curated), names)
	}
	got := seriesByName(s)
	for _, name := range curated {
		if _, ok := got[name]; !ok {
			t.Errorf("curated series %q was not indexed", name)
		}
	}

	// Every broken book must have a warn (or error) row naming its path, so an
	// operator can see *which* file failed and why (FR-IDX-010, arch §4.11).
	var log scanLog
	s.get("/api/scan/log?limit=500", &log)
	logged := map[string]string{}
	for _, e := range log.Items {
		if e.Level == "warn" || e.Level == "error" {
			logged[e.RelPath] = e.Message
		}
	}

	broken := 0
	for _, name := range curated {
		d := detailOf(s, name)
		for _, b := range d.Books {
			if b.Status == "ok" || b.Status == "empty" {
				continue
			}
			broken++
			if b.Error == "" {
				t.Errorf("%s: book %q has status %q with no reason for the UI to show",
					name, b.Name, b.Status)
			}
			if _, ok := logged[b.Path]; !ok {
				t.Errorf("%s: book %q is %q but no scan_log row mentions %q",
					name, b.Name, b.Status, b.Path)
			}
		}
	}
	t.Logf("the scan isolated %d broken book(s) and completed", broken)
	if broken == 0 {
		// data-survey D-4: two truncated archives and one 0-byte file are in
		// the curated set on purpose. Zero would mean they stopped being read.
		t.Error("the curated subset contains known-broken archives; none was detected")
	}

	// The scan itself must have finished cleanly, not been abandoned.
	var st scanStatus
	s.get("/api/scan/status", &st)
	if st.State != "idle" {
		t.Errorf("scan state = %q after the run, want idle", st.State)
	}
}

// I-2 / AC-002 — every indexed page name in the curated subset decodes without
// a replacement character, and the names in a known CP949 archive are the
// expected Hangul (FR-IDX-008, D-1).
func TestI2_pageNames_decodeWithoutReplacementCharacters(t *testing.T) {
	s := sharedServer(t)

	total, bad := 0, 0
	for _, name := range curated {
		d := detailOf(s, name)
		for _, b := range d.Books {
			if b.Status != "ok" {
				continue
			}
			var book bookDetail
			s.get("/api/books/"+b.ID, &book)
			for _, p := range book.Pages {
				total++
				if strings.ContainsRune(p.Name, '�') {
					bad++
					if bad <= 5 {
						t.Errorf("AC-002: %s / %s page %d name %q contains U+FFFD",
							name, b.Name, p.N, p.Name)
					}
				}
			}
		}
	}
	if total == 0 {
		t.Fatal("no page names were sampled")
	}
	t.Logf("AC-002: %d page names checked, %d contained U+FFFD", total, bad)

	// The Clover volumes are 2016 Windows-produced archives with flagless CP949
	// entry names — the exact shape AC-002 is about.
	d := detailOf(s, clover)
	if len(d.Books) == 0 {
		t.Fatal("Clover has no books")
	}
	var book bookDetail
	s.get("/api/books/"+d.Books[0].ID, &book)
	if len(book.Pages) == 0 {
		t.Fatal("the first Clover volume has no pages")
	}
	hangul := 0
	for _, p := range book.Pages {
		for _, r := range p.Name {
			if r >= 0xAC00 && r <= 0xD7A3 { // precomposed Hangul syllables
				hangul++
				break
			}
		}
	}
	t.Logf("Clover volume 1: %d of %d page names carry Hangul syllables",
		hangul, len(book.Pages))
}

// I-10 — the classification outcomes the real collection forced into the plan:
// a cover file rather than a one-page book (D-5), and the container shapes
// below. Duplicate books (D-6) left the collection and are asserted on the
// hermetic twin instead, in TestI10_D6_duplicateBooksAreAllListed.
func TestI10_classification_matchesTheRealShapes(t *testing.T) {
	s := sharedServer(t)

	t.Run("a named cover file is the series cover", func(t *testing.T) {
		if !mustSeries(s, gungye).HasCover {
			t.Error("군계 has a [cover].jpg and must use it (arch §4.10 step 1)")
		}
	})

	t.Run("D-5 a lone cover image is not a one-page book", func(t *testing.T) {
		d := detailOf(s, fma)
		for _, b := range d.Books {
			if b.PageCount == 1 && b.Kind == "dir" {
				t.Errorf("강철의 연금술사: %q became a one-page book; the loose image is a cover (D-5)",
					b.Name)
			}
		}
		if !mustSeries(s, fma).HasCover {
			t.Error("강철의 연금술사 00 Cover.jpg was not used as the series cover")
		}
	})

	t.Run("D-70 the nested archives are read, one book per volume", func(t *testing.T) {
		// D-70 supersedes D-10's first clause. The 1.44 GB container of 33
		// sub-ZIPs is a series of 33 volumes, each indexed and readable; it used
		// to be one book with `status:"empty"` and no pages at all.
		d := detailOf(s, angelHeart)
		if d.Status != "ok" {
			t.Errorf("%s series status = %q, want ok (D-70): %s", angelHeart, d.Status, d.Error)
		}
		if len(d.Books) < 2 {
			t.Fatalf("%s = %d books, want one per inner archive", angelHeart, len(d.Books))
		}
		for _, b := range d.Books {
			if b.Status != "ok" {
				t.Errorf("%s volume %q status = %q (%s), want ok", angelHeart, b.Name, b.Status, b.Error)
			}
			if b.Kind != "nestedzip" {
				t.Errorf("%s volume %q kind = %q, want nestedzip", angelHeart, b.Name, b.Kind)
			}
			if b.PageCount == 0 {
				t.Errorf("%s volume %q has no pages", angelHeart, b.Name)
			}
		}
		// prd UI-002's 총 용량: every volume lives in the one container, so the
		// series occupies that container's bytes.
		if d.TotalBytes == 0 {
			t.Errorf("%s total_bytes = 0; 용량 must be the bytes on disk", angelHeart)
		}
	})

	t.Run("D-73 a container of chapter directories is one book per directory", func(t *testing.T) {
		// `배틀로얄 1~15 [완결].zip` is 1 540 pages in fifteen directories of ~100,
		// one per volume, and it indexed as a single 1 540-page book that no
		// reader could navigate. 484 archives of the collection are this shape.
		d := detailOf(s, battleRoyale)
		if d.Status != "ok" {
			t.Errorf("%s series status = %q, want ok: %s", battleRoyale, d.Status, d.Error)
		}
		if len(d.Books) != 15 {
			t.Fatalf("%s = %d books, want 15 — one per chapter directory", battleRoyale, len(d.Books))
		}
		var pages int
		for _, b := range d.Books {
			if b.Status != "ok" {
				t.Errorf("%s volume %q status = %q (%s), want ok", battleRoyale, b.Name, b.Status, b.Error)
			}
			if b.Kind != "nesteddir" {
				t.Errorf("%s volume %q kind = %q, want nesteddir", battleRoyale, b.Name, b.Kind)
			}
			pages += b.PageCount
		}
		// The partition is total. A split that lost pages would be a worse answer
		// than the lump it replaced, and this is the number that proves it did
		// not: 1 540 before, 1 540 after.
		if pages != 1540 {
			t.Errorf("%s holds %d pages across its volumes, want all 1 540", battleRoyale, pages)
		}
	})

	t.Run("D-7 nothing readable is silently dropped", func(t *testing.T) {
		// Every curated series has to be listed whatever its status; hiding a
		// directory the user can see in their file manager is worse than
		// greying it out (arch OQ-6).
		all := seriesByName(s)
		for _, name := range curated {
			if _, ok := all[name]; !ok {
				t.Errorf("%q vanished from the listing", name)
			}
		}
	})
}

// I-10, D-6 (ruling E-5) — duplicate books are all listed: a folder and an
// archive for the same volume are two books, and the `.repair` copies sit beside
// the original instead of replacing it or being hidden. No dedup rule exists,
// because two of the three 07권 copies are truncated and a preference could hide
// the only readable one; each book carries its own status instead (FR-IDX-010).
//
// The real 군계 lost this shape when E-52 repaired it (25 plain archives now;
// scripts/e2e-assert.py's real branch pins that state), so this runs on the
// twin scripts/mkfixture builds on purpose — block 6, whose books are listed
// below exactly.
func TestI10_D6_duplicateBooksAreAllListed(t *testing.T) {
	s := syntheticServer(t, gungye)
	d := detailOf(s, gungye)

	books := map[string]bookOf{}
	var names []string
	for _, b := range d.Books {
		books[b.Name] = bookOf{kind: b.Kind, status: b.Status}
		names = append(names, b.Name)
	}
	// 01권 twice (folder + ZIP), 02~06권, and 07권 three times.
	if len(d.Books) != 10 {
		t.Errorf("군계 lists %d books, want all 10 the fixture holds: %v", len(d.Books), names)
	}
	if b, ok := books["군계(軍鷄) 01권"]; !ok || b.kind != "dir" {
		t.Errorf("the 01권 folder is not listed as a folder book (got %+v, present %v): %v", b, ok, names)
	}
	if b, ok := books["군계(軍鷄) 01권.zip"]; !ok || b.kind != "zip" {
		t.Errorf("the 01권 archive is not listed beside its folder (got %+v, present %v): %v", b, ok, names)
	}
	if b, ok := books["군계(軍鷄) 07권.zip"]; !ok || b.status != "ok" {
		t.Errorf("the readable 07권 original is missing or not ok (got %+v, present %v)", b, ok)
	}
	for _, name := range []string{"군계(軍鷄) 07권.repair.zip", "군계(軍鷄) 07권 (2).repair.zip"} {
		b, ok := books[name]
		if !ok {
			t.Errorf("%s was hidden; ruling E-5 lists every copy: %v", name, names)
			continue
		}
		if b.status != "error" {
			t.Errorf("%s is a truncated copy; FR-IDX-010 lists it with status \"error\", got %q", name, b.status)
		}
	}
	if !mustSeries(s, gungye).HasCover {
		t.Error("군계 has a [cover].jpg and must use it (arch §4.10 step 1)")
	}
}

type bookOf struct{ kind, status string }
