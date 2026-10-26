package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const sampleTypes = `
export const KINDS = ['zip', 'dir'] as const
export type Kind = (typeof KINDS)[number]

export const CODES = [
  'a', // 409  ?v= does not match the book's current cv
  'b', // 500
] as const
export type Code = (typeof CODES)[number]

export type ID = string
export type Unix = number

export interface Inner {
  n: number
  label: string | null
}

export interface Scalars {
  count: number
  flag: boolean
  at: Unix
  ref: ID
  kind: Kind
  schema: 'shelf-id/1'
  inner: Inner
}

export interface Base {
  id: string
  kind: Kind
}

export interface Thing extends Base {
  inner: Inner
  list: Inner[]
  detail?: Record<string, unknown>
}
`

func mustParse(t *testing.T) *tsTypes {
	t.Helper()
	ts, err := parseTypes(sampleTypes)
	if err != nil {
		t.Fatalf("parseTypes: %v", err)
	}
	return ts
}

// The apostrophe in "book's" inside a trailing comment used to open a quote and
// swallow the next member. It is the reason stripLineComments exists.
func TestParseTypes_commentApostropheDoesNotEatMembers(t *testing.T) {
	t.Parallel()
	ts := mustParse(t)
	got := ts.enums["Code"]
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("CODES parsed as %q, want [a b]", got)
	}
}

func TestParseTypes_extendsIsFlattened(t *testing.T) {
	t.Parallel()
	ts := mustParse(t)
	names := map[string]bool{}
	for _, f := range ts.interfaces["Thing"] {
		names[f.name] = true
	}
	for _, want := range []string{"id", "kind", "inner", "list", "detail"} {
		if !names[want] {
			t.Errorf("Thing is missing field %q after flattening extends: %v", want, names)
		}
	}
}

func check1(t *testing.T, ts *tsTypes, body string) []string {
	t.Helper()
	return checkAs(t, ts, "Thing", body)
}

// checkAs decodes body the way checkGoldens does (UseNumber), so a number
// reaches the checker as a json.Number and not a float64.
func checkAs(t *testing.T, ts *tsTypes, typeName, body string) []string {
	t.Helper()
	var doc any
	dec := json.NewDecoder(strings.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("bad test JSON: %v", err)
	}
	c := &checker{ts: ts, file: "sample.json"}
	c.value(doc, typeRef{name: typeName}, "")
	return c.findings
}

func TestChecker_agreementProducesNoFindings(t *testing.T) {
	t.Parallel()
	got := check1(t, mustParse(t), `{
	  "id": "x", "kind": "zip",
	  "inner": {"n": 1, "label": null},
	  "list": [{"n": 2, "label": "hi"}]
	}`)
	if len(got) != 0 {
		t.Fatalf("expected no findings, got %v", got)
	}
}

func TestChecker_findsEveryKindOfDisagreement(t *testing.T) {
	t.Parallel()
	ts := mustParse(t)
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "field the client does not declare",
			body: `{"id":"x","kind":"zip","inner":{"n":1,"label":null},"list":[],"surprise":1}`,
			want: "does not declare it",
		},
		{
			name: "field the client requires and the server omits",
			body: `{"kind":"zip","inner":{"n":1,"label":null},"list":[]}`,
			want: "requires this field",
		},
		{
			name: "enum value outside the union",
			body: `{"id":"x","kind":"folder","inner":{"n":1,"label":null},"list":[]}`,
			want: `"folder" is not a member of Kind`,
		},
		{
			name: "null where the type does not admit it",
			body: `{"id":"x","kind":"zip","inner":{"n":1,"label":null},"list":null}`,
			want: "does not admit null",
		},
		{
			name: "nested field the client does not declare",
			body: `{"id":"x","kind":"zip","inner":{"n":1,"label":null,"extra":2},"list":[]}`,
			want: "inner.extra",
		},
		{
			name: "array element checked as its own object",
			body: `{"id":"x","kind":"zip","inner":{"n":1,"label":null},"list":[{"n":1}]}`,
			want: "list[0].label",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := check1(t, ts, tc.body)
			joined := strings.Join(got, "\n")
			if !strings.Contains(joined, tc.want) {
				t.Errorf("findings did not mention %q:\n%s", tc.want, joined)
			}
		})
	}
}

// A golden that sends `"2"` for a `number` field agreed with types.ts until
// the checker compared scalar kinds: only enum membership was ever looked at,
// so book_count could turn into a string and every check stayed green.
const scalarsOK = `{"count": 2, "flag": true, "at": 1700000000, "ref": "b1",
  "kind": "zip", "schema": "shelf-id/1", "inner": {"n": 1, "label": null}}`

func TestChecker_scalarKindsThatAgree(t *testing.T) {
	t.Parallel()
	if got := checkAs(t, mustParse(t), "Scalars", scalarsOK); len(got) != 0 {
		t.Fatalf("expected no findings, got %v", got)
	}
}

func TestChecker_scalarKindDisagreements(t *testing.T) {
	t.Parallel()
	ts := mustParse(t)
	cases := []struct {
		field, value, want string
	}{
		{"count", `"2"`, "count: types.ts declares number, the server sends a string"},
		{"count", `true`, "count: types.ts declares number, the server sends a boolean"},
		{"flag", `1`, "flag: types.ts declares boolean, the server sends a number"},
		{"flag", `"true"`, "flag: types.ts declares boolean, the server sends a string"},
		{"at", `"1700000000"`, "at: types.ts declares Unix (number), the server sends a string"},
		{"ref", `42`, "ref: types.ts declares ID (string), the server sends a number"},
		{"kind", `7`, "kind: types.ts declares Kind (string), the server sends a number"},
		{"schema", `3`, "schema: types.ts declares 'shelf-id/1' (string), the server sends a number"},
		{"schema", `"shelf-id/2"`, `schema: value "shelf-id/2" is not 'shelf-id/1'`},
		{"inner", `5`, "inner: types.ts declares Inner (an object), the server sends a number"},
		{"inner", `"x"`, "inner: types.ts declares Inner (an object), the server sends a string"},
	}
	for _, tc := range cases {
		t.Run(tc.field+"="+tc.value, func(t *testing.T) {
			t.Parallel()
			var doc map[string]json.RawMessage
			if err := json.Unmarshal([]byte(scalarsOK), &doc); err != nil {
				t.Fatal(err)
			}
			doc[tc.field] = json.RawMessage(tc.value)
			body, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			got := checkAs(t, ts, "Scalars", string(body))
			if len(got) != 1 || !strings.HasSuffix(got[0], tc.want) {
				t.Errorf("want exactly one finding ending %q, got %q", tc.want, got)
			}
		})
	}
}

// A type name the checker cannot resolve must not pass a scalar silently:
// that is how a renamed alias would stop being checked without anyone noticing.
func TestChecker_unresolvedScalarTypeIsReported(t *testing.T) {
	t.Parallel()
	ts := mustParse(t)
	c := &checker{ts: ts, file: "sample.json"}
	c.value("x", typeRef{name: "Mystery"}, "f")
	if len(c.findings) != 1 || !strings.Contains(c.findings[0], "Mystery") {
		t.Fatalf("want one finding naming Mystery, got %q", c.findings)
	}
}

// ---------------------------------------------------------------------------
// The curated e2e series list
// ---------------------------------------------------------------------------

// Miniature stand-ins for the six copies, in the shapes the real files use: a
// bash array with a trailing `#` comment per entry, a Python list unpacked
// positionally, a keyed TypeScript object, Go path literals woven into a tree
// builder, one bare shell literal, and a markdown table.
const (
	sampleBashCurated = `
# The three series of impl-plan §6.3.
CURATED=(
  "가 시리즈"        # a folder of ZIPs
  "나 시리즈.zip"    # a single archive
  "다 시리즈"        # RARs
)

SYNTHETIC_EXTRA=(
  "암호화 테스트.zip"
)
`
	samplePyCurated = `
CURATED = [
    "가 시리즈",
    "나 시리즈.zip",
    "다 시리즈",
]
SYNTHETIC_EXTRA = ["암호화 테스트.zip"]

GA, NA, DA = CURATED[0], CURATED[1], CURATED[2]
`
	sampleTSCurated = `
export const SERIES = {
  ga: '가 시리즈',
  na: '나 시리즈.zip',
  da: '다 시리즈',
} as const

export const SYNTHETIC_EXTRA = {
  encrypted: '암호화 테스트.zip',
} as const
`
	sampleFixtureCurated = `
package main

func build(b *builder) {
	// ` + "`다 시리즈`" + ` is named in this comment too, which must not count as
	// building it — the real mkfixture names four of the series in prose.
	b.zipFile(fmt.Sprintf("가 시리즈/가 %02d권.zip", i), nil)
	b.zipFile("나 시리즈.zip", nil)
	b.rarFile("다 시리즈/다 1권.rar", nil)
	b.encryptedZip("암호화 테스트.zip", nil)
}
`
	sampleShellCurated = `
if [ "$synthetic" -eq 1 ]; then
  A11_FILL="$FIXTURE/나 시리즈.zip"
fi
`
	// The integration harness names each series twice — once in the slice that
	// becomes its scan.include_globs, once as a by-name handle — and one region
	// covers both. `다 시리즈` is absent on purpose: that copy is a subset.
	sampleIntegrationCurated = `
package integration

var curated = []string{
	"가 시리즈",
	"나 시리즈.zip",
}

const (
	ga = "가 시리즈"
	na = "나 시리즈.zip"
)
`
	sampleDocCurated = `
#### The curated set — 3 series, zero bytes copied

| # | ` + "`include_globs`" + ` entry (exact) | Shape | Covers |
|---|---|---|---|
| 1 | ` + "`가 시리즈`" + ` | folder + ZIPs | prd §2.2 row 1 |
| 2 | ` + "`나 시리즈.zip`" + ` | one ZIP | prd §2.2 row 4 |
| 3 | ` + "`다 시리즈`" + ` | folder of RARs | D-71 |

#### The config
`
)

type curatedSrc struct{ bash, py, ts, fixture, shell, doc, integration string }

func sampleCurated() curatedSrc {
	return curatedSrc{
		bash:        sampleBashCurated,
		py:          samplePyCurated,
		ts:          sampleTSCurated,
		fixture:     sampleFixtureCurated,
		shell:       sampleShellCurated,
		doc:         sampleDocCurated,
		integration: sampleIntegrationCurated,
	}
}

func curatedFindings(t *testing.T, s curatedSrc) []string {
	t.Helper()
	c, err := parseCuratedCopies(s.bash, s.py, s.ts, s.fixture, s.shell, s.doc, s.integration)
	if err != nil {
		t.Fatalf("parseCuratedCopies: %v", err)
	}
	return compareCurated(c)
}

func TestCuratedSeries_sevenAgreeingCopiesProduceNoFindings(t *testing.T) {
	t.Parallel()
	if got := curatedFindings(t, sampleCurated()); len(got) != 0 {
		t.Fatalf("expected no findings, got:\n%s", strings.Join(got, "\n"))
	}
}

// The harness is a deliberate SUBSET, so CURATED holding a name it does not is
// silence, not a finding. Without this case the one-way rule could be tightened
// to a both-ways one by accident and every future addition to CURATED would
// redden `make lint` until somebody pasted it into a suite that costs minutes
// per series to run.
func TestCuratedSeries_theIntegrationSubsetIsNotAFinding(t *testing.T) {
	t.Parallel()
	s := sampleCurated()
	s.integration = `
var curated = []string{
	"가 시리즈",
}

const (
	ga = "가 시리즈"
)
`
	if got := curatedFindings(t, s); len(got) != 0 {
		t.Fatalf("a subset must be silent, got:\n%s", strings.Join(got, "\n"))
	}
}

// One case per shape a drift can take. Each asserts that the finding names both
// the file to open and the series, because a finding that named neither is the
// failure this check exists to replace.
func TestCuratedSeries_findsEveryKindOfDrift(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		mutate func(*curatedSrc)
		want   []string
	}{
		{
			name:   "a series the Python copy is missing",
			mutate: func(s *curatedSrc) { s.py = strings.Replace(s.py, "    \"다 시리즈\",\n", "", 1) },
			want:   []string{"scripts/e2e-config.sh", `"다 시리즈"`, "scripts/e2e-assert.py's CURATED does not list it"},
		},
		{
			name: "a series only the Python copy has",
			mutate: func(s *curatedSrc) {
				s.py = strings.Replace(s.py, "]\nSYNTHETIC", "    \"라 시리즈\",\n]\nSYNTHETIC", 1)
			},
			want: []string{"scripts/e2e-assert.py: CURATED lists", `"라 시리즈"`, "scripts/e2e-config.sh's CURATED does not"},
		},
		{
			name: "the two positional copies are reordered against each other",
			mutate: func(s *curatedSrc) {
				s.py = strings.Replace(s.py,
					"    \"가 시리즈\",\n    \"나 시리즈.zip\",\n",
					"    \"나 시리즈.zip\",\n    \"가 시리즈\",\n", 1)
			},
			want: []string{"scripts/e2e-assert.py", `"나 시리즈.zip" at index 0`, "order-identical", "by position"},
		},
		{
			name:   "a series the TypeScript copy is missing",
			mutate: func(s *curatedSrc) { s.ts = strings.Replace(s.ts, "  na: '나 시리즈.zip',\n", "", 1) },
			want:   []string{"scripts/e2e-config.sh", `"나 시리즈.zip"`, "web/e2e/shelf.ts's SERIES does not name it"},
		},
		{
			name: "a series only the TypeScript copy has",
			mutate: func(s *curatedSrc) {
				s.ts = strings.Replace(s.ts, "} as const", "  ma: '마 시리즈',\n} as const", 1)
			},
			want: []string{"web/e2e/shelf.ts: SERIES names", `"마 시리즈"`, "scripts/e2e-config.sh's CURATED"},
		},
		{
			name: "mkfixture builds no path under one name",
			mutate: func(s *curatedSrc) {
				s.fixture = strings.Replace(s.fixture, `b.rarFile("다 시리즈/다 1권.rar", nil)`, "", 1)
			},
			want: []string{"scripts/mkfixture/main.go", `"다 시리즈"`, "no path literal here builds it", "one series fewer"},
		},
		{
			name: "mkfixture names it in a comment only",
			mutate: func(s *curatedSrc) {
				s.fixture = strings.Replace(s.fixture, `b.zipFile("나 시리즈.zip", nil)`, `// b.zipFile("나 시리즈.zip", nil)`, 1)
			},
			want: []string{"scripts/mkfixture/main.go", `"나 시리즈.zip"`, "no path literal here builds it"},
		},
		{
			name:   "A11_FILL is not one of the curated names",
			mutate: func(s *curatedSrc) { s.shell = strings.Replace(s.shell, "나 시리즈.zip", "바 시리즈.zip", 1) },
			want:   []string{"scripts/e2e.sh: A11_FILL", `"바 시리즈.zip"`, "scripts/e2e-config.sh's CURATED does not list it"},
		},
		{
			// The real drift this copy was added for: the collection dropped a
			// `[만화] ` prefix and the harness kept it, so its include_globs
			// matched nothing and the whole suite ran against an empty library.
			name: "the integration harness keeps a name the collection renamed away",
			mutate: func(s *curatedSrc) {
				s.integration = strings.ReplaceAll(s.integration, `"가 시리즈"`, `"[만화] 가 시리즈"`)
			},
			want: []string{
				"integration/harness_test.go: the integration harness indexes",
				`"[만화] 가 시리즈"`,
				"its scan.include_globs matches nothing",
			},
		},
		{
			// Both declarations hold the names; a rename applied to only one of
			// them leaves the by-name handles pointing at nothing.
			name: "only the harness's const block drifts",
			mutate: func(s *curatedSrc) {
				s.integration = strings.Replace(s.integration, "\tna = \"나 시리즈.zip\"", "\tna = \"나 시리즈\"", 1)
			},
			want: []string{"integration/harness_test.go", `"나 시리즈"`},
		},
		{
			name: "impl-plan's table is missing a row",
			mutate: func(s *curatedSrc) {
				s.doc = strings.Replace(s.doc, "| 3 | `다 시리즈` | folder of RARs | D-71 |\n", "", 1)
			},
			want: []string{"scripts/e2e-config.sh", `"다 시리즈"`, "docs/impl-plan.md's §6.3 curated-set table has no row for it"},
		},
		{
			name: "impl-plan's table carries a name nothing else has",
			mutate: func(s *curatedSrc) {
				s.doc = strings.Replace(s.doc, "| 1 | `가 시리즈` |", "| 1 | `[만화] 가 시리즈` |", 1)
			},
			want: []string{
				"docs/impl-plan.md: the §6.3 curated-set table has a row for",
				`"[만화] 가 시리즈"`,
				"the table describes a subset the E2E round does not run",
			},
		},
		{
			name: "a D-49 extra the Python copy is missing",
			mutate: func(s *curatedSrc) {
				s.py = strings.Replace(s.py, `SYNTHETIC_EXTRA = ["암호화 테스트.zip"]`, "SYNTHETIC_EXTRA = [\"ZIP64 테스트.zip\"]", 1)
			},
			want: []string{"synthetic-only series", `"암호화 테스트.zip"`, "scripts/e2e-assert.py's SYNTHETIC_EXTRA does not list it"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			src := sampleCurated()
			tc.mutate(&src)
			got := curatedFindings(t, src)
			if len(got) == 0 {
				t.Fatalf("the drift produced no finding at all")
			}
			joined := strings.Join(got, "\n")
			for _, want := range tc.want {
				if !strings.Contains(joined, want) {
					t.Errorf("findings did not mention %q:\n%s", want, joined)
				}
			}
		})
	}
}

// A reorder must be reported as a reorder rather than as a pile of
// missing/surplus findings: the sets are equal, so only the positions differ.
func TestCuratedSeries_reorderIsReportedOnce(t *testing.T) {
	t.Parallel()
	src := sampleCurated()
	src.py = strings.Replace(src.py,
		"    \"가 시리즈\",\n    \"나 시리즈.zip\",\n",
		"    \"나 시리즈.zip\",\n    \"가 시리즈\",\n", 1)
	got := curatedFindings(t, src)
	if len(got) != 1 {
		t.Fatalf("a reorder should produce exactly one finding, got %d:\n%s", len(got), strings.Join(got, "\n"))
	}
}

// The extractors have to keep working against the real files, which is a
// different question from whether the comparison is right: a regex that stopped
// matching would report six empty lists, all agreeing.
func TestCuratedSeries_theRepositoryAgrees(t *testing.T) {
	t.Parallel()
	findings, err := checkCuratedSeries("../..")
	if err != nil {
		t.Fatalf("checkCuratedSeries: %v", err)
	}
	if len(findings) != 0 {
		t.Errorf("the curated series list has drifted:\n%s", strings.Join(findings, "\n"))
	}
}

// TestBookKinds_extractionSeesEveryKindInTheRepository pins the one thing this
// gate cannot check about itself: that its *extraction* found anything.
//
// `reGoKind` read a kind's value as `[a-z]+` until 2026-08-24. `hv3` and
// `nestedhv3` have a digit in them, so the regex matched neither, `server` came
// back without them, and `checkBookKinds` reported agreement while two kinds
// the scanner could write were missing from BOOK_KINDS — the exact defect this
// gate was added for after D-71's `nestedrar`, missed for the width of one
// character class.
//
// Counting is what closes it. A regex that stops matching some constants does
// not fail the comparison — both sides simply shrink — so the assertion has to
// be against the file, not against the other declaration.
func TestBookKinds_extractionSeesEveryKindInTheRepository(t *testing.T) {
	t.Parallel()

	src, err := os.ReadFile(filepath.Join("../..", "internal", "source", "source.go"))
	if err != nil {
		t.Fatalf("reading source.go: %v", err)
	}
	got := map[string]bool{}
	for _, m := range reGoKind.FindAllStringSubmatch(string(src), -1) {
		got[m[1]] = true
	}
	// Counted straight out of the file rather than listed here, so adding a
	// kind does not need this test edited — only a kind the regex cannot see
	// fails it.
	//
	// The counting pattern matches the *declaration* and says nothing about the
	// value, which is precisely the half of reGoKind that was wrong. A count
	// derived from the same character class would shrink with it and assert
	// nothing.
	declared := regexp.MustCompile(`(?m)^\s*Kind\w+\s+Kind\s*=`).FindAllString(string(src), -1)
	if len(declared) == 0 {
		t.Fatal("no Kind constants found in source.go at all")
	}
	if len(got) != len(declared) {
		t.Errorf("reGoKind matched %d of the %d Kind constants declared in source.go: %v",
			len(got), len(declared), got)
	}
	// Named explicitly because it is the shape that broke: a value with a
	// digit in it.
	if !got["hv3"] || !got["nestedhv3"] {
		t.Errorf("a kind whose value contains a digit was not extracted: %v", got)
	}
}

func TestCuratedSeries_extractionFindsEveryCopyInTheRepository(t *testing.T) {
	t.Parallel()
	read := func(rel string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join("../..", filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("reading %s: %v", rel, err)
		}
		return string(b)
	}
	c, err := parseCuratedCopies(
		read(curatedBashPath), read(curatedPyPath), read(curatedTSPath),
		read(curatedFixturePath), read(curatedShellPath), read(curatedDocPath),
		read(curatedIntPath))
	if err != nil {
		t.Fatalf("parseCuratedCopies: %v", err)
	}
	// Not compared against len(c.bash): this copy is a subset by design. What
	// matters is that extraction found it at all — an empty result would make
	// the one-way check below silently vacuous.
	if len(c.integration) == 0 {
		t.Errorf("no curated names extracted from %s", curatedIntPath)
	}
	for _, got := range []struct {
		what  string
		names []string
	}{
		{curatedBashPath + " CURATED", c.bash},
		{curatedPyPath + " CURATED", c.py},
		{curatedTSPath + " SERIES", c.ts},
		{curatedDocPath + " table", c.doc},
	} {
		if len(got.names) != len(c.bash) {
			t.Errorf("%s holds %d names, %s holds %d", got.what, len(got.names), curatedBashPath, len(c.bash))
		}
	}
	if c.a11Fill == "" {
		t.Errorf("no A11_FILL extracted from %s", curatedShellPath)
	}
}

// --- the thumb flush signal (amendment A-15, ruling E-57) -------------------

const sampleFlushArch = "| `GET /api/health` | `200 {ok: true}`. **AMENDMENT A-15 (ruling E-57):** " +
	"`verbose.thumbs.{cover_depth,page_depth,active,inflight}` is contract. |\n"

// sampleFlushDTO is Go source, not text: the tags are read through go/parser.
const sampleFlushDTO = `package httpapi

type ThumbCounter struct {
	Hits       int64 ` + "`json:\"hits\"`" + `
	CoverDepth int   ` + "`json:\"cover_depth\"`" + `
	PageDepth  int   ` + "`json:\"page_depth\"`" + `
	Active     int   ` + "`json:\"active\"`" + `
	Inflight   int   ` + "`json:\"inflight\"`" + `
}

type Other struct {
	Queued int64 ` + "`json:\"queued\"`" + `
}
`

const sampleFlushPy = `
def wait_for_thumb_quiescence(r, limit_s):
    keys = ("cover_depth", "page_depth", "active", "inflight")
    while True:
        pass


def envelope(body):
    return body
`

func TestThumbFlush_agreeingCopiesProduceNoFindings(t *testing.T) {
	t.Parallel()
	c, err := parseThumbFlush(sampleFlushArch, sampleFlushDTO, sampleFlushPy)
	if err != nil {
		t.Fatalf("parseThumbFlush: %v", err)
	}
	if findings := compareThumbFlush(c); len(findings) != 0 {
		t.Errorf("agreeing copies produced findings:\n%s", strings.Join(findings, "\n"))
	}
}

// Each drift names the file that moved and the field it moved, so the finding is
// the fix. The arch line is the declaration: the Go tags must carry every field
// it names (the struct has more, which is fine), and the gate's tuple must be
// exactly those fields — one fewer and the wait ends while a cover is still
// being written, one more and it waits on a counter nobody promised.
func TestThumbFlush_findsEveryKindOfDrift(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name                string
		arch, dto, py       string
		wantFile, wantField string
	}{
		{
			name: "the gate stopped waiting on one", wantFile: "scripts/e2e-assert.py", wantField: "inflight",
			arch: sampleFlushArch, dto: sampleFlushDTO,
			py: strings.Replace(sampleFlushPy, `, "inflight"`, "", 1),
		},
		{
			name: "the gate waits on one nobody promised", wantFile: "scripts/e2e-assert.py", wantField: "queued",
			arch: sampleFlushArch, dto: sampleFlushDTO,
			py: strings.Replace(sampleFlushPy, `"inflight"`, `"inflight", "queued"`, 1),
		},
		{
			name: "the server renamed one", wantFile: "internal/httpapi/dto.go", wantField: "inflight",
			arch: sampleFlushArch, py: sampleFlushPy,
			dto: strings.Replace(sampleFlushDTO, `json:"inflight"`, `json:"in_flight"`, 1),
		},
		{
			name: "the contract lost one", wantFile: "scripts/e2e-assert.py", wantField: "active",
			dto: sampleFlushDTO, py: sampleFlushPy,
			arch: strings.Replace(sampleFlushArch, ",active", "", 1),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, err := parseThumbFlush(tc.arch, tc.dto, tc.py)
			if err != nil {
				t.Fatalf("parseThumbFlush: %v", err)
			}
			findings := compareThumbFlush(c)
			var hit bool
			for _, f := range findings {
				if strings.Contains(f, tc.wantFile) && strings.Contains(f, `"`+tc.wantField+`"`) {
					hit = true
				}
			}
			if !hit {
				t.Errorf("no finding naming %s and %q; got:\n%s", tc.wantFile, tc.wantField, strings.Join(findings, "\n"))
			}
		})
	}
}

// A copy the parser cannot find is an error, not agreement: three empty lists
// agree with each other perfectly.
func TestThumbFlush_aMissingCopyIsAnErrorNotAgreement(t *testing.T) {
	t.Parallel()
	for name, args := range map[string][3]string{
		"no amendment line": {"| `GET /api/health` | no amendment here |\n", sampleFlushDTO, sampleFlushPy},
		"no ThumbCounter":   {sampleFlushArch, "package httpapi\n\ntype Other struct {\n}\n", sampleFlushPy},
		"no keys tuple":     {sampleFlushArch, sampleFlushDTO, "def f():\n    pass\n"},
	} {
		if _, err := parseThumbFlush(args[0], args[1], args[2]); err == nil {
			t.Errorf("%s: parsed without error", name)
		}
	}
}

// flushVerdict runs both halves and says whether the copies were judged to
// agree. An error and a finding are both "did not agree"; the regressions
// below only need to rule out the silent pass.
func flushVerdict(arch, dto, py string) (agreed bool, why string) {
	c, err := parseThumbFlush(arch, dto, py)
	if err != nil {
		return false, "error: " + err.Error()
	}
	findings := compareThumbFlush(c)
	return len(findings) == 0, strings.Join(findings, "\n")
}

// The gate's tuple is the one inside wait_for_thumb_quiescence, not the first
// `keys = (…)` in the file: another helper holding the same four names would
// otherwise vouch for a wait that dropped one.
func TestThumbFlush_theTupleIsTheWaitFunctionsOwn(t *testing.T) {
	t.Parallel()
	four := `    keys = ("cover_depth", "page_depth", "active", "inflight")` + "\n"
	short := strings.Replace(sampleFlushPy, `, "inflight"`, "", 1)
	for name, py := range map[string]string{
		"an earlier function holds all four":    "def earlier(r):\n" + four + "\n\n" + short,
		"a later function holds all four":       short + "\n\ndef later(r):\n" + four,
		"two tuples inside the wait":            strings.Replace(sampleFlushPy, "    while True:", four+"    while True:", 1),
		"no wait function at all":               strings.Replace(sampleFlushPy, "wait_for_thumb_quiescence", "wait_for_thumbs", 1),
		"the only tuple is in another function": strings.Replace(sampleFlushPy, four, "", 1) + "\n\ndef other(r):\n" + four,
	} {
		if agreed, _ := flushVerdict(sampleFlushArch, sampleFlushDTO, py); agreed {
			t.Errorf("%s: reported agreement", name)
		}
	}
	single := strings.ReplaceAll(sampleFlushPy, `"`, "'")
	if agreed, why := flushVerdict(sampleFlushArch, sampleFlushDTO, single); !agreed {
		t.Errorf("single-quoted tuple: %s", why)
	}
}

// ThumbCounter's tags are read as Go struct tags: a longer name that merely
// starts with a declared one is a different wire field, and a field that was
// commented out is not served at all.
func TestThumbFlush_goTagsAreTagsNotText(t *testing.T) {
	t.Parallel()
	inflightLine := "\tInflight   int   `json:\"inflight\"`\n"
	if !strings.Contains(sampleFlushDTO, inflightLine) {
		t.Fatal("sampleFlushDTO no longer holds the Inflight line this test edits")
	}
	for name, dto := range map[string]string{
		"a longer tag with the same prefix": strings.Replace(sampleFlushDTO, `json:"inflight"`, `json:"inflight2"`, 1),
		"the field commented out":           strings.Replace(sampleFlushDTO, inflightLine, "\t// "+inflightLine[1:], 1),
	} {
		agreed, why := flushVerdict(sampleFlushArch, dto, sampleFlushPy)
		if agreed || !strings.Contains(why, `"inflight"`) {
			t.Errorf("%s: want a finding naming %q; got agreed=%v %s", name, "inflight", agreed, why)
		}
	}
	omit := strings.Replace(sampleFlushDTO, `json:"inflight"`, `json:"inflight,omitempty"`, 1)
	if agreed, why := flushVerdict(sampleFlushArch, omit, sampleFlushPy); !agreed {
		t.Errorf("a tag with options: %s", why)
	}
}

// The declaration is the A-15 sentence on the GET /api/health row of §7.4 and
// nowhere else: an A-15 list on some other line — a changelog entry, an older
// draft — must not stand in for the row the contract lives on.
func TestThumbFlush_theDeclarationIsTheHealthRows(t *testing.T) {
	t.Parallel()
	elsewhere := "A changelog line: **AMENDMENT A-15** added `verbose.thumbs.{cover_depth,page_depth,active,inflight}`.\n"
	noAmendmentRow := "| `GET /api/health` | `200 {ok: true}`. |\n"
	for name, arch := range map[string]string{
		"an earlier line holds all four": elsewhere + strings.Replace(sampleFlushArch, ",active", "", 1),
		"only another line declares":     elsewhere + noAmendmentRow,
		"two health rows":                sampleFlushArch + sampleFlushArch,
		"no health row":                  elsewhere,
	} {
		if agreed, _ := flushVerdict(arch, sampleFlushDTO, sampleFlushPy); agreed {
			t.Errorf("%s: reported agreement", name)
		}
	}
}

func TestThumbFlush_theRepositoryAgrees(t *testing.T) {
	t.Parallel()
	findings, err := checkThumbFlushFields("../..")
	if err != nil {
		t.Fatalf("checkThumbFlushFields: %v", err)
	}
	if len(findings) != 0 {
		t.Errorf("the thumb flush signal has drifted:\n%s", strings.Join(findings, "\n"))
	}
}

// The extraction itself, against the real files: four names from each copy.
// Agreement between three lists the parser shrank alike would assert nothing.
func TestThumbFlush_extractionSeesTheFourFieldsInTheRepository(t *testing.T) {
	t.Parallel()
	read := func(rel string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join("../..", filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("reading %s: %v", rel, err)
		}
		return string(b)
	}
	c, err := parseThumbFlush(read("docs/arch-backend.md"), read("internal/httpapi/dto.go"), read("scripts/e2e-assert.py"))
	if err != nil {
		t.Fatalf("parseThumbFlush: %v", err)
	}
	want := []string{"cover_depth", "page_depth", "active", "inflight"}
	for name, got := range map[string][]string{"arch": c.arch, "e2e-assert.py": c.py} {
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s: extracted %v, want %v", name, got, want)
		}
	}
	for _, f := range want {
		if !contains(c.goTags, f) {
			t.Errorf("ThumbCounter: tag %q not extracted from %v", f, c.goTags)
		}
	}
}

// --- the amendment table (open item bc) ------------------------------------

const sampleAmendArch = "| `scope` | **AMENDMENT A-1.** x |\n" +
	"  stale_seen?: boolean; // AMENDMENT A-3 (ruling E-45).\n" +
	"**AMENDMENT A-3 (ruling E-45) — prose.**\n"

const sampleAmendPlan = "### 0.2 Before\n\n| **A-9** | not in §0.3 |\n\n" +
	"### 0.3 Amendments to the frozen API contract\n\n| Id | Amendment |\n|---|---|\n" +
	"| **A-1** | one |\n| **A-2** | two, never inlined in arch |\n| **A-3** | three |\n\n" +
	"### 0.4 After\n\n| **A-7** | not in §0.3 either |\n"

// sampleAmendExempt is the sample's A-7: a row that changes no arch text, so
// it carries no note there and says why.
var sampleAmendExempt = map[int]string{2: "the sample's rule about the table itself"}

func TestAmendments_agreementProducesNoFindings(t *testing.T) {
	t.Parallel()
	arch, table, err := parseAmendments(sampleAmendArch, sampleAmendPlan)
	if err != nil {
		t.Fatalf("parseAmendments: %v", err)
	}
	if strings.Join(intsToStrings(table), ",") != "1,2,3" {
		t.Errorf("table = %v, want [1 2 3] — rows outside §0.3 must not count", table)
	}
	if findings := compareAmendments(arch, table, sampleAmendExempt); len(findings) != 0 {
		t.Errorf("agreement produced findings:\n%s", strings.Join(findings, "\n"))
	}
}

// A-7 says §0.3 is changed first, and A-12 and A-13 were written into arch
// alone. Each drift is named with the number, so the finding is the fix.
func TestAmendments_findsEveryKindOfDrift(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, arch, plan, want string
	}{
		{
			name: "an arch amendment with no row", want: "A-4",
			arch: sampleAmendArch + "// AMENDMENT A-4 — new\n", plan: sampleAmendPlan,
		},
		{
			name: "a row written twice", want: "A-2",
			arch: sampleAmendArch,
			plan: strings.Replace(sampleAmendPlan, "| **A-3** |", "| **A-2** | again |\n| **A-3** |", 1),
		},
		{
			name: "a gap in the numbering", want: "A-2",
			arch: sampleAmendArch,
			plan: strings.Replace(sampleAmendPlan, "| **A-2** | two, never inlined in arch |\n", "", 1),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			arch, table, err := parseAmendments(tc.arch, tc.plan)
			if err != nil {
				t.Fatalf("parseAmendments: %v", err)
			}
			findings := compareAmendments(arch, table, sampleAmendExempt)
			var hit bool
			for _, f := range findings {
				if strings.Contains(f, tc.want+" ") || strings.HasSuffix(f, tc.want) {
					hit = true
				}
			}
			if !hit {
				t.Errorf("no finding naming %s; got:\n%s", tc.want, strings.Join(findings, "\n"))
			}
		})
	}
}

// The other direction: a row is a change to §7, so arch must say where. The
// only rows excused are the ones that change no arch text, and each excuse is
// written down — a missing note is otherwise exactly the A-5/A-6 drift, two
// amendments whose arch text was never marked (A-5's field was not even in the
// §7.8 interface).
func TestAmendments_aRowWithNoArchNoteIsDrift(t *testing.T) {
	t.Parallel()
	arch, table, err := parseAmendments(sampleAmendArch, sampleAmendPlan)
	if err != nil {
		t.Fatalf("parseAmendments: %v", err)
	}
	findings := compareAmendments(arch, table, nil)
	if !anyNames(findings, "A-2") {
		t.Errorf("a row with no arch note and no exemption produced no finding naming A-2; got:\n%s",
			strings.Join(findings, "\n"))
	}
}

// An exemption is a claim that the row has no arch text. When arch does mark
// it, the claim is false and the list has gone stale.
func TestAmendments_anExemptionWithANoteIsStale(t *testing.T) {
	t.Parallel()
	arch, table, err := parseAmendments(sampleAmendArch, sampleAmendPlan)
	if err != nil {
		t.Fatalf("parseAmendments: %v", err)
	}
	exempt := map[int]string{1: "wrongly excused", 2: sampleAmendExempt[2]}
	if findings := compareAmendments(arch, table, exempt); !anyNames(findings, "A-1") {
		t.Errorf("an exempt row that arch marks produced no finding naming A-1; got:\n%s",
			strings.Join(findings, "\n"))
	}
}

// A blank line ends a Markdown table. A row after it still matched the row
// pattern, so it was counted while rendering outside the table — the A-16 row
// spent a session that way and every check stayed green.
func TestAmendments_aBlankLineInsideTheTableIsABreak(t *testing.T) {
	t.Parallel()
	if breaks := amendmentTableBreaks(sampleAmendPlan); len(breaks) != 0 {
		t.Fatalf("the intact sample reported breaks:\n%s", strings.Join(breaks, "\n"))
	}
	broken := strings.Replace(sampleAmendPlan, "| **A-3** |", "\n| **A-3** |", 1)
	if breaks := amendmentTableBreaks(broken); !anyNames(breaks, "A-3") {
		t.Errorf("a blank line before A-3 produced no finding naming A-3; got:\n%s", strings.Join(breaks, "\n"))
	}
	prose := strings.Replace(sampleAmendPlan, "| **A-3** |", "A note between rows.\n| **A-3** |", 1)
	if breaks := amendmentTableBreaks(prose); !anyNames(breaks, "A-3") {
		t.Errorf("a prose line before A-3 produced no finding naming A-3; got:\n%s", strings.Join(breaks, "\n"))
	}
}

// The file-reading entry point, not only its parts: both new findings must
// reach checkAmendments' result, not stop at the helpers the tests above call.
func TestAmendments_checkAmendmentsReportsBothNewKinds(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(rel, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(rel)), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A-2 has no note and is not exempt here (only A-7 is); A-3 sits after a
	// blank line.
	write("docs/arch-backend.md", sampleAmendArch)
	write("docs/impl-plan.md", strings.Replace(sampleAmendPlan, "| **A-3** |", "\n| **A-3** |", 1))
	findings, err := checkAmendments(root)
	if err != nil {
		t.Fatalf("checkAmendments: %v", err)
	}
	if !anyNames(findings, "A-2") {
		t.Errorf("no finding for the row with no arch note; got:\n%s", strings.Join(findings, "\n"))
	}
	if !anyNames(findings, "A-3") {
		t.Errorf("no finding for the row cut off by a blank line; got:\n%s", strings.Join(findings, "\n"))
	}
}

// anyNames reports whether a finding names id as a whole number — "A-1" must
// not be satisfied by "A-16".
func anyNames(findings []string, id string) bool {
	re := regexp.MustCompile(regexp.QuoteMeta(id) + `\b`)
	for _, f := range findings {
		if re.MatchString(f) {
			return true
		}
	}
	return false
}

// Nothing found is an error, not agreement: an empty table agrees with an
// arch the extractor could not read.
func TestAmendments_aMissingSideIsAnErrorNotAgreement(t *testing.T) {
	t.Parallel()
	if _, _, err := parseAmendments("no markers here\n", sampleAmendPlan); err == nil {
		t.Error("an arch with no AMENDMENT markers parsed without error")
	}
	if _, _, err := parseAmendments(sampleAmendArch, "### 0.3 Amendments\n\nno rows\n"); err == nil {
		t.Error("a §0.3 with no rows parsed without error")
	}
	if _, _, err := parseAmendments(sampleAmendArch, "### 0.2 Elsewhere\n\n| **A-1** | x |\n"); err == nil {
		t.Error("a plan with no §0.3 parsed without error")
	}
}

func TestAmendments_theRepositoryAgrees(t *testing.T) {
	t.Parallel()
	findings, err := checkAmendments("../..")
	if err != nil {
		t.Fatalf("checkAmendments: %v", err)
	}
	if len(findings) != 0 {
		t.Errorf("the amendment table has drifted:\n%s", strings.Join(findings, "\n"))
	}
}

// The extraction against the real files, counted by a pattern that does not
// share the extractor's number class.
func TestAmendments_extractionSeesEveryMarkerAndRow(t *testing.T) {
	t.Parallel()
	read := func(rel string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join("../..", filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("reading %s: %v", rel, err)
		}
		return string(b)
	}
	archSrc, planSrc := read("docs/arch-backend.md"), read("docs/impl-plan.md")
	arch, table, err := parseAmendments(archSrc, planSrc)
	if err != nil {
		t.Fatalf("parseAmendments: %v", err)
	}
	distinct := map[string]bool{}
	for _, m := range regexp.MustCompile(`AMENDMENT (A-\S+?)\b`).FindAllStringSubmatch(archSrc, -1) {
		distinct[m[1]] = true
	}
	if len(arch) != len(distinct) {
		t.Errorf("extracted %d distinct arch amendments, the file names %d: %v", len(arch), len(distinct), distinct)
	}
	if len(table) < 15 {
		t.Errorf("extracted %d §0.3 rows, want at least the 15 (A-1…A-15) the table had when this was written", len(table))
	}
}

func intsToStrings(ns []int) []string {
	out := make([]string, len(ns))
	for i, n := range ns {
		out[i] = strconv.Itoa(n)
	}
	return out
}
