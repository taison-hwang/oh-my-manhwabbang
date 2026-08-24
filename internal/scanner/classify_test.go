package scanner

import (
	"fmt"
	"strings"
	"testing"

	"shelf/internal/config"
	"shelf/internal/index"
	"shelf/internal/testutil"
)

// prd §2.2 — the series/book classification table, every row, implemented
// literally (impl-plan WP-08 acceptance 2). Each case builds a real tree in
// t.TempDir(), runs a real scan, and asserts what the index ended up holding.
//
// The shapes are the ones data-survey actually measured, not invented ones:
// folder-of-zips is 80 % of the collection, folder-of-subfolders is the rest,
// "mixed" is always "N archives + exactly one cover image" (47 directories), and
// the duplicate `01권/` + `01권.zip` pair is real (`[만화] 군계 1~25`).
func TestClassify_everyPrdTableRow_producesTheSpecifiedSeriesAndBooks(t *testing.T) {
	t.Parallel()

	type expectBook struct {
		name   string
		kind   string
		rel    string
		pages  int64
		status string
	}
	cases := []struct {
		name   string
		row    string
		layout map[string]any
		scan   func(*config.Scan)
		// seriesRel is the root-relative path of the series under test.
		seriesRel  string
		kind       string
		status     string
		books      []expectBook
		coverKind  string
		coverRel   string
		otherCheck func(t *testing.T, h *harness)
	}{
		{
			name:      "folder of zips",
			row:       "prd §2.2 row 1 — 폴더 안에 ZIP 파일 다수 (592 real series, 80 % of the sample)",
			seriesRel: "[만화] Clover 클로버 (총4권)",
			layout: map[string]any{
				"[만화] Clover 클로버 (총4권)": map[string]any{
					"클로버 1.zip": jpegZIP(t, "001.jpg", "002.jpg"),
					"클로버 2.zip": jpegZIP(t, "001.jpg"),
					"클로버 3.zip": jpegZIP(t, "001.jpg"),
				},
			},
			kind:   SeriesFolder,
			status: StatusOK,
			books: []expectBook{
				{name: "클로버 1.zip", kind: "zip", rel: "[만화] Clover 클로버 (총4권)/클로버 1.zip", pages: 2, status: StatusOK},
				{name: "클로버 2.zip", kind: "zip", rel: "[만화] Clover 클로버 (총4권)/클로버 2.zip", pages: 1, status: StatusOK},
				{name: "클로버 3.zip", kind: "zip", rel: "[만화] Clover 클로버 (총4권)/클로버 3.zip", pages: 1, status: StatusOK},
			},
			coverKind: CoverPage,
		},
		{
			name:      "folder of image subfolders",
			row:       "prd §2.2 row 2 — 폴더 안에 하위 폴더 다수, 각 하위 폴더에 이미지",
			seriesRel: "[만화] 상처를 쫓는자 1-11 (완)",
			layout: map[string]any{
				"[만화] 상처를 쫓는자 1-11 (완)": map[string]any{
					"01권": imageDir(t, "1.jpg", "2.jpg", "10.jpg"),
					"02권": imageDir(t, "1.jpg"),
				},
			},
			kind:   SeriesFolder,
			status: StatusOK,
			books: []expectBook{
				{name: "01권", kind: "dir", rel: "[만화] 상처를 쫓는자 1-11 (완)/01권", pages: 3, status: StatusOK},
				{name: "02권", kind: "dir", rel: "[만화] 상처를 쫓는자 1-11 (완)/02권", pages: 1, status: StatusOK},
			},
			coverKind: CoverPage,
		},
		{
			name:      "images directly inside the folder",
			row:       "prd §2.2 row 3 — 폴더 안에 이미지 파일이 직접 존재; 시리즈 자체가 단일 권",
			seriesRel: "낱장 시리즈",
			layout: map[string]any{
				"낱장 시리즈": imageDir(t, "1.jpg", "2.jpg", "10.jpg", "100.jpg"),
			},
			kind:   SeriesFolder,
			status: StatusOK,
			books: []expectBook{
				{name: "낱장 시리즈", kind: "dir", rel: "낱장 시리즈", pages: 4, status: StatusOK},
			},
			coverKind: CoverPage,
		},
		{
			name:      "a single zip file",
			row:       "prd §2.2 row 4 — ZIP 파일 1개; 자기 자신이 단일 권 (291 real series)",
			seriesRel: "[만화] 바퀴.zip",
			layout: map[string]any{
				"[만화] 바퀴.zip": jpegZIP(t, "001.jpg", "002.jpg"),
			},
			kind:   SeriesZIP,
			status: StatusOK,
			books: []expectBook{
				{name: "[만화] 바퀴.zip", kind: "zip", rel: "[만화] 바퀴.zip", pages: 2, status: StatusOK},
			},
			coverKind: CoverPage,
		},
		{
			name:      "a single pdf file",
			row:       "prd §2.2 row 5 — PDF 파일 1개; 자기 자신이 단일 권",
			seriesRel: "[만화] 미생 1~9 (완결 pdf).pdf",
			layout: map[string]any{
				"[만화] 미생 1~9 (완결 pdf).pdf": "%PDF-1.4 not a real document",
			},
			kind: SeriesPDF,
			// The scanner's own suite carries no pdfium renderer, so the book is
			// 'unsupported' — which is precisely the `-tags nopdf` behaviour of
			// arch §4.11 and makes this case assert the same thing in both builds.
			status: StatusError,
			books: []expectBook{
				{name: "[만화] 미생 1~9 (완결 pdf).pdf", kind: "pdf",
					rel: "[만화] 미생 1~9 (완결 pdf).pdf", pages: 0, status: StatusUnsupported},
			},
		},
		{
			name:      "mixed: N archives and exactly one cover image",
			row:       "prd §2.2 row 6 + adjustment D-5 — the image is a COVER, not a one-page book (47 real directories)",
			seriesRel: "[만화] 강철의 연금술사 1~27권 완결",
			layout: map[string]any{
				"[만화] 강철의 연금술사 1~27권 완결": map[string]any{
					"01권.zip":               jpegZIP(t, "001.jpg"),
					"02권.zip":               jpegZIP(t, "001.jpg"),
					"강철의 연금술사 00 Cover.jpg": jpeg(t),
				},
			},
			kind:   SeriesFolder,
			status: StatusOK,
			books: []expectBook{
				{name: "01권.zip", kind: "zip", rel: "[만화] 강철의 연금술사 1~27권 완결/01권.zip", pages: 1, status: StatusOK},
				{name: "02권.zip", kind: "zip", rel: "[만화] 강철의 연금술사 1~27권 완결/02권.zip", pages: 1, status: StatusOK},
			},
			coverKind: CoverFile,
			coverRel:  "[만화] 강철의 연금술사 1~27권 완결/강철의 연금술사 00 Cover.jpg",
		},
		{
			name:      "mixed: N archives and more loose images than the cover budget",
			row:       "prd §2.2 row 6 — genuinely mixed; the loose pages become their own book",
			seriesRel: "혼재 시리즈",
			layout: map[string]any{
				"혼재 시리즈": map[string]any{
					"01권.zip": jpegZIP(t, "001.jpg"),
					"a.jpg":   jpeg(t),
					"b.jpg":   jpeg(t),
					"c.jpg":   jpeg(t),
					"d.jpg":   jpeg(t),
					"e.jpg":   jpeg(t),
				},
			},
			kind:   SeriesFolder,
			status: StatusOK,
			books: []expectBook{
				{name: "01권.zip", kind: "zip", rel: "혼재 시리즈/01권.zip", pages: 1, status: StatusOK},
				{name: "혼재 시리즈" + looseBookSuffix, kind: "dir", rel: "혼재 시리즈", pages: 5, status: StatusOK},
			},
			coverKind: CoverPage,
		},
		{
			name:      "duplicate volumes are all listed",
			row:       "ruling E-5 / D-6 — `01권/` and `01권.zip` both become books, natural-sorted",
			seriesRel: "[만화] 군계 1~25",
			layout: map[string]any{
				"[만화] 군계 1~25": map[string]any{
					"군계 01권":                imageDir(t, "001.jpg", "002.jpg"),
					"군계 01권.zip":            jpegZIP(t, "001.jpg"),
					"군계 07권.zip":            jpegZIP(t, "001.jpg"),
					"군계 07권.repair.zip":     jpegZIP(t, "001.jpg"),
					"군계 07권 (2).repair.zip": jpegZIP(t, "001.jpg"),
				},
			},
			kind:   SeriesFolder,
			status: StatusOK,
			books: []expectBook{
				{name: "군계 01권", kind: "dir", rel: "[만화] 군계 1~25/군계 01권", pages: 2, status: StatusOK},
				{name: "군계 01권.zip", kind: "zip", rel: "[만화] 군계 1~25/군계 01권.zip", pages: 1, status: StatusOK},
				{name: "군계 07권 (2).repair.zip", kind: "zip", rel: "[만화] 군계 1~25/군계 07권 (2).repair.zip", pages: 1, status: StatusOK},
				{name: "군계 07권.repair.zip", kind: "zip", rel: "[만화] 군계 1~25/군계 07권.repair.zip", pages: 1, status: StatusOK},
				{name: "군계 07권.zip", kind: "zip", rel: "[만화] 군계 1~25/군계 07권.zip", pages: 1, status: StatusOK},
			},
			coverKind: CoverPage,
		},
		{
			name:      "two-level series flattens",
			row:       "ruling E-4 / D-30 — a series is exactly one direct child of a root; sub-paths ride in the display name",
			seriesRel: "[만화] 기동전사 건담 시리즈",
			layout: map[string]any{
				"[만화] 기동전사 건담 시리즈": map[string]any{
					"건담 외전.zip": jpegZIP(t, "001.jpg"),
					"역습의 샤아":    map[string]any{"01권.zip": jpegZIP(t, "001.jpg")},
					"크로스본 건담": map[string]any{
						"크로스본 건담 01권.zip": jpegZIP(t, "001.jpg"),
						"크로스본 건담 02권.zip": jpegZIP(t, "001.jpg"),
					},
				},
			},
			kind:   SeriesFolder,
			status: StatusOK,
			books: []expectBook{
				{name: "건담 외전.zip", kind: "zip", rel: "[만화] 기동전사 건담 시리즈/건담 외전.zip", pages: 1, status: StatusOK},
				{name: "역습의 샤아 / 01권.zip", kind: "zip", rel: "[만화] 기동전사 건담 시리즈/역습의 샤아/01권.zip", pages: 1, status: StatusOK},
				{name: "크로스본 건담 / 크로스본 건담 01권.zip", kind: "zip", rel: "[만화] 기동전사 건담 시리즈/크로스본 건담/크로스본 건담 01권.zip", pages: 1, status: StatusOK},
				{name: "크로스본 건담 / 크로스본 건담 02권.zip", kind: "zip", rel: "[만화] 기동전사 건담 시리즈/크로스본 건담/크로스본 건담 02권.zip", pages: 1, status: StatusOK},
			},
			coverKind: CoverPage,
		},
		{
			name:      "three levels, the deepest real nesting in the collection",
			row:       "data-survey §7 — `[만화] 단편 만화/아다치/쇼트 프로그램 (전,후 完)`, max_depth 3",
			seriesRel: "[만화] 단편 만화",
			layout: map[string]any{
				"[만화] 단편 만화": map[string]any{
					"아다치": map[string]any{
						"쇼트 프로그램 (전,후 完)": imageDir(t, "001.jpg", "002.jpg"),
					},
				},
			},
			kind:   SeriesFolder,
			status: StatusOK,
			books: []expectBook{
				{name: "아다치 / 쇼트 프로그램 (전,후 完)", kind: "dir",
					rel: "[만화] 단편 만화/아다치/쇼트 프로그램 (전,후 完)", pages: 2, status: StatusOK},
			},
			coverKind: CoverPage,
		},
		{
			name: "a directory with no readable books",
			// The row said ".txt/.hv3" until E-51 gave HV3 a reader. A `.hv3`
			// is a container now, so a directory holding one has a book in it
			// — broken or not — and this row is about a directory that has
			// none. The half of D-7 that still bites is the `.txt` half, and
			// the rule it produced is unchanged: listed, never dropped.
			row:       "adjustment D-7 — real top-level directories hold only text novels; listed as empty, never dropped",
			seriesRel: "[만화] 엔젤릭 레이어",
			layout: map[string]any{
				"[만화] 엔젤릭 레이어": map[string]any{
					"ANGELIC LAYER 엔젤릭 레이어.txt": "",
					"목록.nfo":                    "x",
				},
			},
			kind:      SeriesFolder,
			status:    StatusEmpty,
			books:     nil,
			coverKind: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var tweaks []func(*config.Scan)
			if tc.scan != nil {
				tweaks = append(tweaks, tc.scan)
			}
			h := newHarness(t, tc.layout, tweaks...)
			h.run(Request{})

			s := h.seriesAt("manga", tc.seriesRel)
			if s.Kind != tc.kind {
				t.Errorf("%s\nseries kind = %q, want %q", tc.row, s.Kind, tc.kind)
			}
			if s.Status != tc.status {
				t.Errorf("%s\nseries status = %q, want %q (error %q)", tc.row, s.Status, tc.status, s.Error)
			}
			if s.CoverKind != tc.coverKind {
				t.Errorf("%s\ncover_kind = %q, want %q", tc.row, s.CoverKind, tc.coverKind)
			}
			if tc.coverRel != "" && s.CoverRelPath != tc.coverRel {
				t.Errorf("%s\ncover_rel_path = %q, want %q", tc.row, s.CoverRelPath, tc.coverRel)
			}

			if len(s.Books) != len(tc.books) {
				t.Fatalf("%s\nindexed %d books %v, want %d %v",
					tc.row, len(s.Books), bookNames(s.Books), len(tc.books), expectedNames(tc.books))
			}
			for i, want := range tc.books {
				got := s.Books[i]
				if got.DisplayName != want.name {
					t.Errorf("%s\nbook %d display_name = %q, want %q", tc.row, i, got.DisplayName, want.name)
				}
				if got.Kind != want.kind {
					t.Errorf("%s\nbook %d kind = %q, want %q", tc.row, i, got.Kind, want.kind)
				}
				if got.RelPath != want.rel {
					t.Errorf("%s\nbook %d rel_path = %q, want %q", tc.row, i, got.RelPath, want.rel)
				}
				if got.PageCount != want.pages {
					t.Errorf("%s\nbook %d page_count = %d, want %d", tc.row, i, got.PageCount, want.pages)
				}
				if got.Status != want.status {
					t.Errorf("%s\nbook %d status = %q, want %q (%q)", tc.row, i, got.Status, want.status, got.Error)
				}
				if got.Ord != i {
					t.Errorf("%s\nbook %d ord = %d", tc.row, i, got.Ord)
				}
			}
			if tc.otherCheck != nil {
				tc.otherCheck(t, h)
			}
		})
	}
}

func expectedNames[T any](books []T) []string {
	out := make([]string, 0, len(books))
	for _, b := range books {
		out = append(out, fmt.Sprint(b))
	}
	return out
}

// arch §4.2: a regular file at the top of a root that is neither an archive nor
// a PDF is not a series, and arch asks for exactly one info-level scan_log row.
//
// The `.rar` that used to stand here is a series now (D-71), so the example
// moved to `.7z` — still a real archive format, still one this build has no
// reader for, which is the property the rule is about.
func TestClassify_nonContainerRootChild_isIgnoredWithOneInfoLogRow(t *testing.T) {
	t.Parallel()
	h := newHarness(t, map[string]any{
		"[만화] 무언가.7z": "not an archive we support",
		"읽어보세요.txt":   "메모",
		".DS_Store":   "junk",
		"진짜 시리즈.zip":  jpegZIP(t, "001.jpg"),
	})
	h.run(Request{})

	got := h.series()
	if len(got) != 1 || got[0].RelPath != "진짜 시리즈.zip" {
		t.Fatalf("indexed %d series (%v), want only 진짜 시리즈.zip", len(got), seriesRels(got))
	}

	info := map[string]int{}
	for _, e := range h.logs() {
		if e.Level == index.LevelInfo {
			info[e.RelPath]++
		}
	}
	for _, want := range []string{"[만화] 무언가.7z", "읽어보세요.txt"} {
		if info[want] != 1 {
			t.Errorf("scan_log info rows for %q = %d, want exactly 1", want, info[want])
		}
	}
	// `.DS_Store` is dropped by FR-IDX-006 before it ever reaches
	// classification, so it must NOT produce a log row of its own.
	if info[".DS_Store"] != 0 {
		t.Errorf(".DS_Store produced %d info rows; FR-IDX-006 drops it silently", info[".DS_Store"])
	}
}

// `scan.max_depth` bounds how far below a series a book may live (arch §4.2
// step 3). The default is 3 because the deepest real nesting is 3.
func TestClassify_maxDepth_boundsHowDeepABookMayLive(t *testing.T) {
	t.Parallel()
	layout := map[string]any{
		"시리즈": map[string]any{
			"1단계": map[string]any{
				"2단계": imageDir(t, "001.jpg", "002.jpg"),
			},
		},
	}
	for _, tc := range []struct {
		depth int
		books int
	}{
		{depth: 1, books: 0},
		{depth: 2, books: 1},
		{depth: 3, books: 1},
		{depth: 0, books: 1}, // 0 means unlimited (arch §3.2)
	} {
		t.Run(fmt.Sprintf("max_depth=%d", tc.depth), func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, layout, func(s *config.Scan) { s.MaxDepth = tc.depth })
			h.run(Request{})
			s := h.seriesAt("manga", "시리즈")
			if len(s.Books) != tc.books {
				t.Fatalf("max_depth %d indexed %d books %v, want %d",
					tc.depth, len(s.Books), bookNames(s.Books), tc.books)
			}
			if tc.books == 0 && s.Status != StatusEmpty {
				t.Errorf("a series whose only book is out of depth has status %q, want empty", s.Status)
			}
		})
	}
}

// The D-5 boundary is a configured number, not a hard-coded 1: the same
// directory becomes covers or a book depending on `cover_max_loose_images`.
func TestClassify_coverMaxLooseImages_isTheBoundaryBetweenACoverAndABook(t *testing.T) {
	t.Parallel()
	layout := map[string]any{
		"시리즈": map[string]any{
			"01권.zip": jpegZIP(t, "001.jpg"),
			"a.jpg":   jpeg(t),
			"b.jpg":   jpeg(t),
			"c.jpg":   jpeg(t),
		},
	}
	for _, tc := range []struct {
		budget    int
		books     int
		coverKind string
	}{
		{budget: 3, books: 1, coverKind: ""},        // 3 loose images == the budget: covers
		{budget: 2, books: 2, coverKind: CoverPage}, // over the budget: a (loose pages) book
	} {
		t.Run(fmt.Sprintf("budget=%d", tc.budget), func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, layout, func(s *config.Scan) { s.CoverMaxLooseImages = tc.budget })
			h.run(Request{})
			s := h.seriesAt("manga", "시리즈")
			if len(s.Books) != tc.books {
				t.Fatalf("budget %d gave %d books %v, want %d",
					tc.budget, len(s.Books), bookNames(s.Books), tc.books)
			}
			if tc.books == 1 {
				// Three unnamed candidates is more than one, so rung 2 of the
				// cover ladder does not fire and the ladder falls to page 1.
				if s.CoverKind != CoverPage {
					t.Errorf("cover_kind = %q, want %q", s.CoverKind, CoverPage)
				}
				return
			}
			if !strings.HasSuffix(s.Books[1].DisplayName, looseBookSuffix) {
				t.Errorf("second book = %q, want a %q book", s.Books[1].DisplayName, looseBookSuffix)
			}
			if s.Books[1].PageCount != 3 {
				t.Errorf("loose-pages book has %d pages, want 3", s.Books[1].PageCount)
			}
		})
	}
}

// Two roots, one library. Ids hash the root NAME, so the same relative path in
// two roots is two different series (arch §3.4).
func TestClassify_twoRoots_areIndexedIndependently(t *testing.T) {
	t.Parallel()
	a := testutil.BuildTree(t, map[string]any{"공통 이름": map[string]any{"01권.zip": jpegZIP(t, "001.jpg")}})
	b := testutil.BuildTree(t, map[string]any{"공통 이름": map[string]any{"01권.zip": jpegZIP(t, "001.jpg", "002.jpg")}})
	h := newHarnessAt(t, map[string]string{"manga": a, "novel": b})
	res := h.run(Request{})

	if len(res.Roots) != 2 {
		t.Fatalf("scanned %d roots, want 2", len(res.Roots))
	}
	if got := len(h.series()); got != 2 {
		t.Fatalf("indexed %d series, want 2 — one per root", got)
	}
	if x, y := h.seriesAt("manga", "공통 이름"), h.seriesAt("novel", "공통 이름"); x.ID == y.ID {
		t.Errorf("the same relative path in two roots produced one id %q", x.ID)
	}
	if got := h.seriesAt("novel", "공통 이름").PageCount; got != 2 {
		t.Errorf("second root page_count = %d, want 2", got)
	}
	// A named run touches only the root it names.
	h.lister.reset()
	h.run(Request{Roots: []string{"novel"}, Full: true})
	for _, rel := range h.lister.listedPaths() {
		if !strings.HasPrefix(rel, "공통 이름") {
			t.Errorf("unexpected read %q", rel)
		}
	}
	if got := len(h.lister.listedPaths()); got != 1 {
		t.Errorf("a single-root run read %d books, want 1", got)
	}
}

// seriesStatus folds book statuses into the series' own. Unit-tested directly so
// every branch is covered without needing a fixture per branch.
func TestSeriesStatus_foldsBookStatusesIntoTheSeriesStatus(t *testing.T) {
	t.Parallel()
	book := func(status, msg string) bookResult { return bookResult{status: status, errMsg: msg} }
	cases := []struct {
		name    string
		in      []bookResult
		status  string
		message string
	}{
		{name: "no books at all", in: nil, status: StatusEmpty, message: "no readable books"},
		{name: "one readable book wins", in: []bookResult{
			book(StatusError, "truncated"), book(StatusOK, ""),
		}, status: StatusOK},
		{name: "every book broken", in: []bookResult{
			book(StatusError, "zip: end of central directory not found"),
		}, status: StatusError, message: "zip: end of central directory not found"},
		{name: "encrypted counts as broken", in: []bookResult{
			book(StatusEncrypted, "zip: archive is password-protected"),
		}, status: StatusError, message: "zip: archive is password-protected"},
		{
			// Ruling E-14, narrowing decision D-10: `[만화] 엔젤하트 전32권 완결.zip`
			// holds 33 nested archives and zero images. The *book* stays `empty`
			// (see TestScan_… in scanner_test.go), but a series the reader cannot
			// open a single page of must not present as healthy, so the *series*
			// is `error` carrying that book's reason. `empty` survives only for
			// the zero-book row above.
			name: "a series whose every book is empty is an error, not empty (E-14)",
			in: []bookResult{
				book(StatusEmpty, "no supported image entries"),
			},
			status: StatusError, message: "no supported image entries",
		},
		{
			name: "a hard failure names the reason ahead of an empty book's",
			in: []bookResult{
				book(StatusEmpty, "no supported image entries"),
				book(StatusError, "zip: end of central directory not found"),
			},
			status: StatusError, message: "zip: end of central directory not found",
		},
		{
			// The fold must still produce a reason when the books carry none:
			// `series.error` is non-null whenever status != "ok" (arch §7.3).
			name:   "an error series always carries a reason",
			in:     []bookResult{book(StatusEmpty, "")},
			status: StatusError, message: "no supported image entries",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			status, message := seriesStatus(tc.in)
			if status != tc.status {
				t.Errorf("status = %q, want %q", status, tc.status)
			}
			if tc.message != "" && message != tc.message {
				t.Errorf("message = %q, want %q", message, tc.message)
			}
		})
	}
}

func TestSeriesRelativeAndDisplayName(t *testing.T) {
	t.Parallel()
	cases := []struct {
		dirRel, bookRel string
		depth           int
		wantRel, wantNm string
	}{
		{dirRel: "시리즈", bookRel: "시리즈/01권.zip", depth: 0, wantRel: "01권.zip", wantNm: "01권.zip"},
		{dirRel: "시리즈", bookRel: "시리즈", depth: 0, wantRel: "시리즈", wantNm: "시리즈"},
		{dirRel: "시리즈", bookRel: "시리즈/하위/01권.zip", depth: 0,
			wantRel: "하위/01권.zip", wantNm: "하위 / 01권.zip"},
		{dirRel: "시리즈/하위", bookRel: "시리즈/하위/01권.zip", depth: 1,
			wantRel: "시리즈/하위/01권.zip", wantNm: "시리즈 / 하위 / 01권.zip"},
	}
	for _, tc := range cases {
		rel := seriesRelative(tc.dirRel, tc.bookRel, tc.depth)
		if rel != tc.wantRel {
			t.Errorf("seriesRelative(%q, %q, %d) = %q, want %q",
				tc.dirRel, tc.bookRel, tc.depth, rel, tc.wantRel)
		}
		if name := displayName(rel); name != tc.wantNm {
			t.Errorf("displayName(%q) = %q, want %q", rel, name, tc.wantNm)
		}
	}
}

func seriesRels(rows []index.SeriesRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.RelPath)
	}
	return out
}
