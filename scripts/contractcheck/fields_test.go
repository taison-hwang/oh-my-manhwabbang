package main

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// --- interface field sets across arch, types.ts and the Go JSON tags --------

const sampleFieldsArch = "Prose with `interface Prose {` that is not code.\n\n" +
	"```yaml\ninterface NotTS {\n  ignored: string;\n}\n```\n\n" +
	"```ts\ninterface Base {\n  id: string;        // a comment\n  note?: string;\n}\n\n" +
	"interface Thing extends Base {\n  // a whole-line comment: fake: string;\n" +
	"  server: { port: number;\n            host?: string };\n  count: number;\n}\n```\n\n" +
	"```ts\ninterface Req {\n  page: number;\n}\n```\n"

const sampleFieldsTS = "export interface Base {\n  id: string\n  note?: string\n}\n" +
	"export interface ThingServer {\n  port: number\n  host?: string\n}\n" +
	"export interface Thing extends Base {\n  server: ThingServer\n  count: number\n}\n" +
	"export interface Req {\n  page: number\n}\n" +
	"export interface Query {\n  q?: string\n}\n"

var sampleFieldsGo = map[string]string{
	"internal/httpapi/dto.go": "package httpapi\n\n" +
		"type Base struct {\n\tID   string `json:\"id\"`\n\tNote string `json:\"note,omitempty\"`\n\tskip int\n}\n" +
		"type ThingServer struct {\n\tPort int `json:\"port\"`\n\tHost string `json:\"host,omitempty\"`\n\tSecret string `json:\"-\"`\n}\n" +
		"type Thing struct {\n\tBase\n\tServer ThingServer `json:\"server\"`\n\tCount  int `json:\"count\"`\n\tDebug  *int `json:\"debug,omitempty\"`\n}\n" +
		"type reqBody struct {\n\tPage *int `json:\"page\"`\n}\n",
	"internal/userdata/x.go": "package userdata\n\ntype Thing struct {\n\tOther int `json:\"other\"`\n}\n",
}

// sampleFieldContract is the sample's mapping: the one place each name that
// differs between the three files is written down.
func sampleFieldContract() fieldContract {
	return fieldContract{
		archToTS:      map[string]string{"Thing.server": "ThingServer"},
		tsWithoutArch: map[string]string{"Query": "a query string, described in a table"},
		responseRoots: []string{"Thing"},
		tsToGo:        map[string]string{},
		requestPairs:  map[string]string{"Req": "httpapi.reqBody"},
		goOnly:        map[string]string{"httpapi.Thing.debug": "diagnostics nobody reads"},
	}
}

func sampleFieldInputs(t *testing.T) (map[string]fieldSet, *tsTypes, map[string]fieldSet) {
	t.Helper()
	arch, err := parseArchInterfaces(sampleFieldsArch)
	if err != nil {
		t.Fatalf("parseArchInterfaces: %v", err)
	}
	ts, err := parseTypes(sampleFieldsTS)
	if err != nil {
		t.Fatalf("parseTypes: %v", err)
	}
	gs, err := parseGoJSONStructs(sampleFieldsGo)
	if err != nil {
		t.Fatalf("parseGoJSONStructs: %v", err)
	}
	return arch, ts, gs
}

func TestFields_parseArch_readsOnlyTSBlocksAndOneNestedLevel(t *testing.T) {
	t.Parallel()
	arch, err := parseArchInterfaces(sampleFieldsArch)
	if err != nil {
		t.Fatalf("parseArchInterfaces: %v", err)
	}
	want := map[string]fieldSet{
		"Base":         {"id": false, "note": true},
		"Thing":        {"id": false, "note": true, "server": false, "count": false},
		"Thing.server": {"port": false, "host": true},
		"Req":          {"page": false},
	}
	if got := describeFieldSets(arch); got != describeFieldSets(want) {
		t.Errorf("parsed\n%s\nwant\n%s", got, describeFieldSets(want))
	}
}

func TestFields_parseGo_readsJSONTagsAndEmbeddedStructsPerPackage(t *testing.T) {
	t.Parallel()
	gs, err := parseGoJSONStructs(sampleFieldsGo)
	if err != nil {
		t.Fatalf("parseGoJSONStructs: %v", err)
	}
	want := map[string]fieldSet{
		"httpapi.Base":        {"id": false, "note": true},
		"httpapi.ThingServer": {"port": false, "host": true},
		"httpapi.Thing":       {"id": false, "note": true, "server": false, "count": false, "debug": true},
		"httpapi.reqBody":     {"page": false},
		"userdata.Thing":      {"other": false},
	}
	if got := describeFieldSets(gs); got != describeFieldSets(want) {
		t.Errorf("parsed\n%s\nwant\n%s", got, describeFieldSets(want))
	}
}

func TestFields_agreementProducesNoFindings(t *testing.T) {
	t.Parallel()
	arch, ts, gs := sampleFieldInputs(t)
	if findings := compareInterfaceFields(arch, ts, gs, sampleFieldContract()); len(findings) != 0 {
		t.Errorf("agreement produced findings:\n%s", strings.Join(findings, "\n"))
	}
}

// Each direction of each pair, one field at a time: removing or renaming a
// field on any one side must be named. want is a regexp the finding must match.
func TestFields_findsEveryKindOfDrift(t *testing.T) {
	t.Parallel()
	replace := func(src, old, new string) string {
		if !strings.Contains(src, old) {
			panic("sample does not contain " + old)
		}
		return strings.Replace(src, old, new, 1)
	}
	goWith := func(file, old, new string) map[string]string {
		m := map[string]string{}
		for k, v := range sampleFieldsGo {
			m[k] = v
		}
		m[file] = replace(m[file], old, new)
		return m
	}
	const dto = "internal/httpapi/dto.go"
	cases := []struct {
		name     string
		arch, ts string
		goSrc    map[string]string
		contract func(*fieldContract)
		want     string
	}{
		{name: "a field types.ts dropped", want: `^arch Thing / types.ts Thing: field "count" is only in arch$`,
			ts: replace(sampleFieldsTS, "  count: number\n}", "}")},
		{name: "a field only in arch", want: `^arch Base / types.ts Base: field "id2" is only in arch$`,
			arch: replace(sampleFieldsArch, "  id: string;", "  id: string;\n  id2: string;")},
		{name: "a field only in types.ts", want: `^arch Req / types.ts Req: field "extra" is only in types.ts$`,
			ts: replace(sampleFieldsTS, "  page: number\n}", "  page: number\n  extra: number\n}")},
		{name: "a nested field renamed in arch", want: `^arch Thing.server / types.ts ThingServer: field "portNo" is only in arch$`,
			arch: replace(sampleFieldsArch, "{ port: number;", "{ portNo: number;")},
		{name: "optional in arch, required in types.ts", want: `^arch Req / types.ts Req: field "page" is optional in arch but required in types.ts$`,
			arch: replace(sampleFieldsArch, "  page: number;", "  page?: number;")},
		{name: "a types.ts interface arch never declares", want: `^web/src/api/types.ts: Query is declared in no `,
			contract: func(c *fieldContract) { delete(c.tsWithoutArch, "Query") }},
		{name: "a stale arch exemption", want: `^scripts/contractcheck: Base is listed in tsWithoutArch but arch §7 declares it`,
			contract: func(c *fieldContract) { c.tsWithoutArch["Base"] = "wrongly excused" }},
		{name: "a response field Go does not send", want: `^types.ts Thing / Go httpapi.Thing: field "count" is only in types.ts$`,
			goSrc: goWith(dto, "\tCount  int `json:\"count\"`\n", "")},
		{name: "a response field Go renamed", want: `^types.ts ThingServer / Go httpapi.ThingServer: field "host" is only in types.ts$`,
			goSrc: goWith(dto, "`json:\"host,omitempty\"`", "`json:\"hostname,omitempty\"`")},
		{name: "a Go-only field with no exemption", want: `^types.ts Thing / Go httpapi.Thing: field "debug" is only in Go$`,
			contract: func(c *fieldContract) { delete(c.goOnly, "httpapi.Thing.debug") }},
		{name: "a stale Go-only exemption", want: `^scripts/contractcheck: httpapi.Thing.gone is listed in goOnly .*stale$`,
			contract: func(c *fieldContract) { c.goOnly["httpapi.Thing.gone"] = "no such field" }},
		{name: "a Go-only exemption for a field types.ts now declares", want: `^scripts/contractcheck: httpapi.Thing.debug is listed in goOnly .*stale$`,
			arch: replace(sampleFieldsArch, "  count: number;\n}", "  count: number;\n  debug?: number;\n}"),
			ts:   replace(sampleFieldsTS, "  count: number\n}", "  count: number\n  debug?: number\n}")},
		{name: "optional in types.ts, no omitempty in Go", want: `^types.ts Thing / Go httpapi.Thing: field "note" is optional in types.ts but always sent \(no omitempty\) in Go$`,
			goSrc: goWith(dto, "`json:\"note,omitempty\"`", "`json:\"note\"`")},
		{name: "a request field Go does not decode", want: `^types.ts Req / Go httpapi.reqBody \(request body\): field "page" is only in types.ts$`,
			goSrc: goWith(dto, "\tPage *int `json:\"page\"`\n", "")},
		{name: "a response type with no Go struct", want: `^web/src/api/types.ts: response type ThingServer has no Go struct httpapi.Missing`,
			contract: func(c *fieldContract) { c.tsToGo["ThingServer"] = "httpapi.Missing" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			archSrc, tsSrc, goSrc := sampleFieldsArch, sampleFieldsTS, sampleFieldsGo
			if tc.arch != "" {
				archSrc = tc.arch
			}
			if tc.ts != "" {
				tsSrc = tc.ts
			}
			if tc.goSrc != nil {
				goSrc = tc.goSrc
			}
			arch, err := parseArchInterfaces(archSrc)
			if err != nil {
				t.Fatalf("parseArchInterfaces: %v", err)
			}
			ts, err := parseTypes(tsSrc)
			if err != nil {
				t.Fatalf("parseTypes: %v", err)
			}
			gs, err := parseGoJSONStructs(goSrc)
			if err != nil {
				t.Fatalf("parseGoJSONStructs: %v", err)
			}
			c := sampleFieldContract()
			if tc.contract != nil {
				tc.contract(&c)
			}
			findings := compareInterfaceFields(arch, ts, gs, c)
			re := regexp.MustCompile(tc.want)
			if !slices.ContainsFunc(findings, re.MatchString) {
				t.Errorf("no finding matching %q; got:\n%s", tc.want, strings.Join(findings, "\n"))
			}
		})
	}
}

// Comments and string literals are not syntax: a brace in a comment must not
// end the interface, and `//` or `;` inside a string type must not end a field.
// arch §7's comments do carry braces (`// 201 Created  +  Location: {base_path}/…`).
func TestFields_parseArch_ignoresCommentsAndStrings(t *testing.T) {
	t.Parallel()
	src := "```ts\ninterface C {\n  // closes early: }\n  a: string;   // {x\n" +
		"  url: \"https://x;y{\";  b: number;\n  /* } */ c?: string;\n}\n```\n"
	arch, err := parseArchInterfaces(src)
	if err != nil {
		t.Fatalf("parseArchInterfaces: %v", err)
	}
	want := map[string]fieldSet{"C": {"a": false, "url": false, "b": false, "c": true}}
	if got := describeFieldSets(arch); got != describeFieldSets(want) {
		t.Errorf("parsed\n%s\nwant\n%s", got, describeFieldSets(want))
	}
}

// The same inline object, its brace on the next line.
func TestFields_parseArch_braceOnTheNextLine(t *testing.T) {
	t.Parallel()
	arch, err := parseArchInterfaces("```ts\ninterface S {\n  server:\n  {\n    port: number;\n  };\n}\n```\n")
	if err != nil {
		t.Fatalf("parseArchInterfaces: %v", err)
	}
	if got := describeFieldSets(arch); got != "S.server: port\nS: server" {
		t.Errorf("parsed\n%s", got)
	}
}

// A response type reached only through a union's second member, a Record's
// value or an Array<…> is still compared with its Go struct.
func TestFields_responsesReachEveryNamedType(t *testing.T) {
	t.Parallel()
	for _, expr := range []string{"Base | Leaf", "Record<string, Leaf>", "Array<Leaf>", "Leaf[] | null"} {
		t.Run(expr, func(t *testing.T) {
			t.Parallel()
			ts, err := parseTypes("export interface Base {\n  id: string\n}\n" +
				"export interface Leaf {\n  n: number\n}\n" +
				"export interface Top {\n  x: " + expr + "\n}\n")
			if err != nil {
				t.Fatal(err)
			}
			got := reachableInterfaces(ts, []string{"Top"})
			if !slices.Contains(got, "Leaf") {
				t.Errorf("reachable from Top through %q = %v, want Leaf among them", expr, got)
			}
		})
	}
}

// encoding/json drops a name two embedded structs both promote at the same
// depth; a check that kept either would compare a field the server never
// sends. Shapes the extractor cannot resolve are errors, not guesses.
func TestFields_parseGo_refusesWhatItCannotResolve(t *testing.T) {
	t.Parallel()
	cases := map[string]map[string]string{
		"two embedded structs promote the same name": {"a.go": "package httpapi\n" +
			"type A struct{ X int `json:\"x\"` }\ntype B struct{ X int `json:\"x\"` }\ntype R struct {\n\tA\n\tB\n}\n"},
		"an embedded struct from another package": {"a.go": "package httpapi\nimport \"shelf/internal/userdata\"\n" +
			"type R struct {\n\t*userdata.Export\n\tN int `json:\"n\"`\n}\n"},
		"two fields of one struct with the same JSON name": {"a.go": "package httpapi\n" +
			"type R struct {\n\tX int `json:\"x\"`\n\tY int `json:\"x\"`\n}\n"},
		"one struct reached by two embedding paths": {"a.go": "package httpapi\n" +
			"type A struct{ X int `json:\"x\"` }\ntype B struct{ A }\ntype C struct{ A }\n" +
			"type R struct {\n\tB\n\tC\n\tN int `json:\"n\"`\n}\n"},
		"a foreign embedding behind an untagged carrier": {"a.go": "package httpapi\nimport \"shelf/internal/userdata\"\n" +
			"type Carrier struct{ userdata.Export }\ntype R struct {\n\tCarrier\n\tN int `json:\"n\"`\n}\n"},
		"the same pkg.Name in two files": {
			"internal/httpapi/a.go": "package httpapi\ntype R struct{ X int `json:\"x\"` }\n",
			"internal/other/b.go":   "package httpapi\ntype R struct{ Y int `json:\"y\"` }\n"},
	}
	for name, files := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := parseGoJSONStructs(files); err == nil {
				t.Error("parsed without error")
			}
		})
	}
	// A struct with no JSON tag is not a wire shape (statusRecorder embeds
	// http.ResponseWriter); its foreign embedding is not this check's business.
	if _, err := parseGoJSONStructs(map[string]string{"a.go": "package httpapi\nimport \"net/http\"\n" +
		"type rec struct {\n\thttp.ResponseWriter\n\tstatus int\n}\n"}); err != nil {
		t.Errorf("a non-wire struct with a foreign embedding: %v", err)
	}
	// An outer field of the same name wins, as in encoding/json, and is no
	// conflict.
	if gs, err := parseGoJSONStructs(map[string]string{"a.go": "package httpapi\n" +
		"type A struct{ X int `json:\"x\"` }\ntype B struct{ X int `json:\"x\"` }\n" +
		"type R struct {\n\tA\n\tB\n\tX string `json:\"x,omitempty\"`\n}\n"}); err != nil {
		t.Errorf("an outer field that shadows both: %v", err)
	} else if opt, ok := gs["httpapi.R"]["x"]; !ok || !opt {
		t.Errorf("httpapi.R = %v, want the outer x (omitempty)", gs["httpapi.R"])
	}
}

// types.ts is read by parseFields, one declaration per line. A shape it would
// misread is refused instead: a second field on the line would vanish when no
// other side has it, and a union's next line would drop the types it names
// from the response walk.
func TestFields_typesTSSyntaxTheParserCannotReadIsRefused(t *testing.T) {
	t.Parallel()
	ok := "export interface A {\n  /** a doc with `code` and it's apostrophe */\n  a: string\n  b?: B | null // {x}\n}\n"
	if f := tsSyntaxFindings(ok); len(f) != 0 {
		t.Fatalf("the supported shapes produced findings:\n%s", strings.Join(f, "\n"))
	}
	for name, src := range map[string]string{
		"two fields on a line":       "export interface A {\n  a: string; extra?: number\n}\n",
		"a union broken after a bar": "export interface A {\n  x: B |\n    C\n}\n",
		"a line that continues one":  "export interface A {\n  x: B\n    | C\n}\n",
		"an inline object":           "export interface A {\n  x: { y: number }\n}\n",
	} {
		if f := tsSyntaxFindings(src); len(f) == 0 {
			t.Errorf("%s: no finding", name)
		}
	}
}

// The file-reading entry point reports it too, not only the helper.
func TestFields_checkInterfaceFieldsReportsTypesTSSyntax(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for rel, body := range map[string]string{
		"docs/arch-backend.md":        sampleFieldsArch,
		"web/src/api/types.ts":        "export interface Req {\n  page: number; extra?: number\n}\n",
		"internal/httpapi/dto.go":     "package httpapi\n",
		"internal/userdata/export.go": "package userdata\n",
	} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ts, err := parseTypes("export interface Req {\n  page: number; extra?: number\n}\n")
	if err != nil {
		t.Fatal(err)
	}
	findings, err := checkInterfaceFields(root, ts)
	if err != nil {
		t.Fatalf("checkInterfaceFields: %v", err)
	}
	if !slices.ContainsFunc(findings, func(f string) bool { return strings.Contains(f, "holds a second declaration") }) {
		t.Errorf("no syntax finding from the entry point; got:\n%s", strings.Join(findings, "\n"))
	}
}

// What arch's masking still cannot read is refused, and a comment between two
// tokens keeps them apart.
func TestFields_maskTSCode_keepsTokensApartAndRefusesTemplateHoles(t *testing.T) {
	t.Parallel()
	arch, err := parseArchInterfaces("```ts\ninterface/* doc */C {\n  a: string;\n}\n```\n")
	if err != nil {
		t.Fatalf("parseArchInterfaces: %v", err)
	}
	if got := describeFieldSets(arch); got != "C: a" {
		t.Errorf("parsed %q, want \"C: a\"", got)
	}
	if _, err := parseArchInterfaces("```ts\ninterface C {\n  mode: `a${`}`}`;\n  b: string;\n}\n```\n"); err == nil {
		t.Error("a template literal type with a ${…} hole parsed without error")
	}
}

// A string literal is a value, not a reference: `kind: "Leaf"` does not reach
// the interface Leaf.
func TestFields_stringValuesAreNotTypeReferences(t *testing.T) {
	t.Parallel()
	ts, err := parseTypes("export interface Leaf {\n  n: number\n}\n" +
		"export interface Top {\n  kind: \"Leaf\"\n  m: Record<'Leaf', string>\n}\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := reachableInterfaces(ts, []string{"Top"}); slices.Contains(got, "Leaf") {
		t.Errorf("reachable = %v: a string value was taken for a reference", got)
	}
}

// A request body is compared by name only: a `?` on the client and no
// omitempty on a decoder is not a disagreement (RootCreate.label).
func TestFields_requestBodiesIgnoreOptionality(t *testing.T) {
	t.Parallel()
	arch, ts, gs := sampleFieldInputs(t)
	ts2, err := parseTypes(strings.Replace(sampleFieldsTS, "  page: number\n", "  page?: number\n", 1))
	if err != nil {
		t.Fatal(err)
	}
	arch["Req"] = fieldSet{"page": true}
	_ = ts
	if findings := compareInterfaceFields(arch, ts2, gs, sampleFieldContract()); len(findings) != 0 {
		t.Errorf("a request body's optionality produced findings:\n%s", strings.Join(findings, "\n"))
	}
}

func TestFields_theRepositoryAgrees(t *testing.T) {
	t.Parallel()
	ts := repoTypes(t)
	findings, err := checkInterfaceFields("../..", ts)
	if err != nil {
		t.Fatalf("checkInterfaceFields: %v", err)
	}
	if len(findings) != 0 {
		t.Errorf("the interface field sets have drifted:\n%s", strings.Join(findings, "\n"))
	}
}

// The extraction against the real files, so an extractor that quietly reads
// nothing cannot pass the agreement test above.
func TestFields_extractionSeesTheRealShapes(t *testing.T) {
	t.Parallel()
	arch, gs, err := readInterfaceSources("../..")
	if err != nil {
		t.Fatalf("readInterfaceSources: %v", err)
	}
	if len(arch) < 24 {
		t.Errorf("parsed %d arch interfaces, want at least the 24 arch §7 declared when this was written", len(arch))
	}
	for _, k := range []string{"Settings.server", "CacheUsage.entries", "ProgressExport.items"} {
		if _, ok := arch[k]; !ok {
			t.Errorf("arch nested object %s was not parsed", k)
		}
	}
	if s := arch["Settings"]; !s.has("library_scope") || s.has("thumbnail_widths") {
		t.Errorf("arch Settings = %v: want its own fields, not server's", s)
	}
	for _, k := range []string{"httpapi.SeriesDetail", "userdata.ExportItem", "httpapi.settingsUpdateBody"} {
		if len(gs[k]) == 0 {
			t.Errorf("Go struct %s has no JSON fields parsed", k)
		}
	}
}

func describeFieldSets(m map[string]fieldSet) string {
	var lines []string
	for name, fs := range m {
		var fields []string
		for f, opt := range fs {
			if opt {
				f += "?"
			}
			fields = append(fields, f)
		}
		slices.Sort(fields)
		lines = append(lines, name+": "+strings.Join(fields, " "))
	}
	slices.Sort(lines)
	return strings.Join(lines, "\n")
}

func repoTypes(t *testing.T) *tsTypes {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("../..", "web", "src", "api", "types.ts"))
	if err != nil {
		t.Fatal(err)
	}
	ts, err := parseTypes(string(src))
	if err != nil {
		t.Fatal(err)
	}
	return ts
}
