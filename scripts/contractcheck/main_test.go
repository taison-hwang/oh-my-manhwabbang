package main

import (
	"encoding/json"
	"os"
	"path/filepath"
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

export interface Inner {
  n: number
  label: string | null
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
	var doc any
	if err := json.NewDecoder(strings.NewReader(body)).Decode(&doc); err != nil {
		t.Fatalf("bad test JSON: %v", err)
	}
	c := &checker{ts: ts, file: "sample.json"}
	c.value(doc, typeRef{name: "Thing"}, "")
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

type curatedSrc struct{ bash, py, ts, fixture, shell, doc string }

func sampleCurated() curatedSrc {
	return curatedSrc{
		bash:    sampleBashCurated,
		py:      samplePyCurated,
		ts:      sampleTSCurated,
		fixture: sampleFixtureCurated,
		shell:   sampleShellCurated,
		doc:     sampleDocCurated,
	}
}

func curatedFindings(t *testing.T, s curatedSrc) []string {
	t.Helper()
	c, err := parseCuratedCopies(s.bash, s.py, s.ts, s.fixture, s.shell, s.doc)
	if err != nil {
		t.Fatalf("parseCuratedCopies: %v", err)
	}
	return compareCurated(c)
}

func TestCuratedSeries_sixAgreeingCopiesProduceNoFindings(t *testing.T) {
	t.Parallel()
	if got := curatedFindings(t, sampleCurated()); len(got) != 0 {
		t.Fatalf("expected no findings, got:\n%s", strings.Join(got, "\n"))
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
		read(curatedFixturePath), read(curatedShellPath), read(curatedDocPath))
	if err != nil {
		t.Fatalf("parseCuratedCopies: %v", err)
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
