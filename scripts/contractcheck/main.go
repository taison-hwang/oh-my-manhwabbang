// Command contractcheck is the reconciliation gate of impl-plan §4.
//
// Backend and frontend were built in parallel against `arch-backend.md` §7 plus
// the amendments in `impl-plan.md` §0.3, with no contact between them. The
// reconciliation artefact is `internal/httpapi/testdata/golden/*.json` — the
// exact bytes the server produces — diffed against `web/src/api/types.ts`, the
// exact shapes the client expects. This program is that diff, and it is a build
// gate rather than a review item because the two sides cannot be trusted to
// stay aligned by inspection.
//
//	go run ./scripts/contractcheck          # from the repository root
//	go run ./scripts/contractcheck -v       # also list what was checked
//
// It exits non-zero on any disagreement and prints one line per finding, in the
// form `<golden file>: <json path>: <what is wrong>`.
//
// # What is compared
//
//  1. **Fields.** Every key in a golden response must be declared on the
//     matching TypeScript interface, recursively through nested objects and
//     arrays. Every *required* field of that interface must be present in the
//     golden. A field the server sends and the client has never heard of is
//     dead data; a field the client requires and the server never sends is a
//     runtime `undefined` in a component.
//
//  2. **Nullability.** A field the server sends as `null` must be typed
//     `T | null` on the client. WP-06 acceptance 1 is explicit that
//     `T | undefined` is not the same thing and is not allowed.
//
//  3. **Enums.** Every string whose declared type is one of the closed unions
//     in types.ts (`SeriesKind`, `ItemStatus`, `ScanState`, `ErrorCode`, …)
//     must be a member of that union. This catches C-1…C-4 drift — a server
//     that starts sending `double` instead of `spread`, say — which a
//     field-name diff alone would miss entirely.
//
//  4. **The error-code enum, from the Go side.** The `Code*` constants in
//     `internal/httpapi/errors.go` are compared against `ERROR_CODES`. Not
//     every code has a golden file, so without this a code the server can
//     legitimately return and the client cannot name — which is exactly what
//     ruling E-13 was raised about — would go unnoticed.
//
//  5. **`GET /api/series`'s query parameters.** The golden files are
//     *responses*, so a request parameter is invisible to the field diff — and
//     `/api/series` is where the contract's live amendments land (A-4's
//     `progress`, A-8's `scope`). The keys `seriesFilter` reads are compared
//     with `SeriesListParams`, in both directions.
//
//  6. **The curated e2e series list, across its six copies.** Not an API
//     contract at all, and here for the same reason the two above are: it is a
//     declaration duplicated across languages that nothing links statically,
//     so the only thing that noticed a disagreement was `make e2e` twenty
//     minutes in. See `checkCuratedSeries`.
//
// # What is deliberately not compared
//
// Request bodies, status codes, and the query parameters of the other
// endpoints: none of them appear in a golden response, and a general extractor
// over every handler would be a parser rather than a grep.
// `internal/httpapi`'s own tests cover them.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// goldenType maps each golden file to the TypeScript type that describes it.
// A golden with no entry here is an error: an unmapped contract artefact is
// exactly the kind of thing that gets added and then never checked.
var goldenType = map[string]string{
	"auth_status":              "AuthStatus",
	"book_detail":              "BookDetail",
	"book_detail_broken":       "BookDetail",
	"book_prefs":               "BookPrefs",
	"browse":                   "BrowseResponse",
	"cache_purge":              "CachePurgeResult",
	"cache_usage":              "CacheUsage",
	"continue":                 "ContinueResponse",
	"health":                   "Health",
	"import_result":            "ProgressImportResult",
	"progress":                 "Progress",
	"progress_export":          "ProgressExport",
	"root_created":             "RootEntry",
	"roots":                    "RootsResponse",
	"scan_accepted":            "ScanRunResponse",
	"scan_log":                 "ScanLogResponse",
	"scan_status":              "ScanStatus",
	"series_detail":            "SeriesDetail",
	"series_detail_page_cover": "SeriesDetail",
	"series_list":              "SeriesListResponse",
	"series_list_count":        "SeriesListResponse",
	"series_list_scope_added":  "SeriesListResponse",
	"series_list_search":       "SeriesListResponse",
	"settings":                 "Settings",
}

// errorGoldenPrefix marks the golden files that carry the §7.2 error envelope.
const errorGoldenPrefix = "error_"

func main() {
	var (
		root    = flag.String("root", ".", "repository root")
		verbose = flag.Bool("v", false, "list every file and type checked")
	)
	flag.Parse()

	findings, checked, err := check(*root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "contractcheck: %v\n", err)
		os.Exit(2)
	}
	if *verbose {
		for _, c := range checked {
			fmt.Println("  ok  " + c)
		}
	}
	if len(findings) == 0 {
		// `checked` is not one entry per golden file: it also carries the checks
		// derived from source rather than from a golden (ERROR_CODES from
		// internal/httpapi/errors.go, SeriesListParams from series.go, and the
		// curated e2e series list, which does not involve types.ts at all).
		// Calling the total "golden files" sent a reader hunting for two files
		// that do not exist and made them doubt the docs quoting this line — so
		// say what it actually is.
		fmt.Printf("contractcheck: %d contract checks agree (web/src/api/types.ts, and the copies of the curated e2e series list)\n", len(checked))
		return
	}
	fmt.Fprintf(os.Stderr, "contractcheck: %d disagreement(s) between declarations that are supposed to agree\n\n", len(findings))
	for _, f := range findings {
		fmt.Fprintln(os.Stderr, "  "+f)
	}
	fmt.Fprintf(os.Stderr, "\nThe API contract is arch-backend.md §7 as amended by impl-plan.md §0.3.\n"+
		"The curated e2e series are scripts/e2e-config.sh's CURATED, which is what\n"+
		"scan.include_globs is built from; every other copy follows it.\n"+
		"Fix the side that disagrees; do not adjust this check.\n")
	os.Exit(1)
}

func check(root string) (findings, checked []string, err error) {
	typesPath := filepath.Join(root, "web", "src", "api", "types.ts")
	src, err := os.ReadFile(typesPath)
	if err != nil {
		return nil, nil, fmt.Errorf("reading %s: %w", typesPath, err)
	}
	ts, err := parseTypes(string(src))
	if err != nil {
		return nil, nil, fmt.Errorf("parsing %s: %w", typesPath, err)
	}

	goldenDir := filepath.Join(root, "internal", "httpapi", "testdata", "golden")
	entries, err := filepath.Glob(filepath.Join(goldenDir, "*.json"))
	if err != nil {
		return nil, nil, err
	}
	if len(entries) == 0 {
		return nil, nil, fmt.Errorf("no golden files in %s", goldenDir)
	}
	sort.Strings(entries)

	for _, path := range entries {
		name := strings.TrimSuffix(filepath.Base(path), ".json")
		typeName, ok := goldenType[name]
		if !ok {
			if strings.HasPrefix(name, errorGoldenPrefix) {
				typeName = "ErrorResponse"
			} else {
				findings = append(findings, fmt.Sprintf(
					"%s.json: no TypeScript type is mapped to this golden file; add it to goldenType in scripts/contractcheck",
					name))
				continue
			}
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, nil, err
		}
		var doc any
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.UseNumber()
		if err := dec.Decode(&doc); err != nil {
			return nil, nil, fmt.Errorf("parsing %s: %w", path, err)
		}
		c := &checker{ts: ts, file: name + ".json"}
		c.value(doc, typeRef{name: typeName}, "")
		findings = append(findings, c.findings...)
		checked = append(checked, fmt.Sprintf("%-28s -> %s", name+".json", typeName))
	}

	errFindings, err := checkErrorCodes(root, ts)
	if err != nil {
		return nil, nil, err
	}
	findings = append(findings, errFindings...)
	checked = append(checked, fmt.Sprintf("%-28s -> ERROR_CODES", "internal/httpapi/errors.go"))

	kindFindings, err := checkBookKinds(root, ts)
	if err != nil {
		return nil, nil, err
	}
	findings = append(findings, kindFindings...)
	checked = append(checked, fmt.Sprintf("%-28s -> BOOK_KINDS", "internal/source/source.go"))

	paramFindings, err := checkSeriesParams(root, ts)
	if err != nil {
		return nil, nil, err
	}
	findings = append(findings, paramFindings...)
	checked = append(checked, fmt.Sprintf("%-28s -> SeriesListParams", "internal/httpapi/series.go"))

	curatedFindings, err := checkCuratedSeries(root)
	if err != nil {
		return nil, nil, err
	}
	findings = append(findings, curatedFindings...)
	checked = append(checked, fmt.Sprintf("%-28s -> the curated e2e series", "scripts/e2e-config.sh"))
	return findings, checked, nil
}

// ---------------------------------------------------------------------------
// The TypeScript side
// ---------------------------------------------------------------------------

// tsField is one property of an interface.
type tsField struct {
	name     string
	optional bool // written `name?: …`
	typ      typeRef
}

// typeRef is a parsed field type, reduced to the three things this check needs:
// the named type, whether it is an array, and whether null is allowed.
type typeRef struct {
	name     string
	array    bool
	nullable bool
	// unknown marks a type this parser deliberately does not model
	// (Record<…>, inline object literals): its members are not descended into.
	unknown bool
}

type tsTypes struct {
	// interfaces maps an interface name to its fields, with `extends` already
	// flattened.
	interfaces map[string][]tsField
	// enums maps a type alias (`SeriesKind`) to its members, via the
	// `export const X = [...] as const` + `type Y = (typeof X)[number]` idiom
	// types.ts uses throughout.
	enums map[string][]string
	// constArrays maps the const name to its members.
	constArrays map[string][]string
}

var (
	reInterface = regexp.MustCompile(`(?m)^export interface ([A-Za-z0-9_]+)(?:\s+extends\s+([A-Za-z0-9_,\s]+?))?\s*\{`)
	reConstArr  = regexp.MustCompile(`(?ms)^export const ([A-Z0-9_]+)\s*=\s*\[(.*?)\]\s*as const`)
	reEnumAlias = regexp.MustCompile(`(?m)^export type ([A-Za-z0-9_]+)\s*=\s*\(typeof ([A-Z0-9_]+)\)\[number\]`)
	reMember    = regexp.MustCompile(`'([^']*)'`)
	reFieldLine = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)(\??):\s*(.+?);?$`)
)

func parseTypes(src string) (*tsTypes, error) {
	t := &tsTypes{
		interfaces:  map[string][]tsField{},
		enums:       map[string][]string{},
		constArrays: map[string][]string{},
	}

	for _, m := range reConstArr.FindAllStringSubmatch(src, -1) {
		var members []string
		// Comments first. `ERROR_CODES` documents each member with a trailing
		// `// 409 ?v= does not match the book's current cv`, and the apostrophe
		// in "book's" would otherwise open a quote and swallow the next member.
		for _, mm := range reMember.FindAllStringSubmatch(stripLineComments(m[2]), -1) {
			members = append(members, mm[1])
		}
		t.constArrays[m[1]] = members
	}
	for _, m := range reEnumAlias.FindAllStringSubmatch(src, -1) {
		members, ok := t.constArrays[m[2]]
		if !ok {
			return nil, fmt.Errorf("type %s refers to unknown const %s", m[1], m[2])
		}
		t.enums[m[1]] = members
	}

	// Interfaces, with `extends` recorded for a second pass.
	type pending struct {
		fields  []tsField
		extends []string
	}
	raw := map[string]*pending{}
	for _, loc := range reInterface.FindAllStringSubmatchIndex(src, -1) {
		name := src[loc[2]:loc[3]]
		var ext []string
		if loc[4] >= 0 {
			for _, e := range strings.Split(src[loc[4]:loc[5]], ",") {
				if e = strings.TrimSpace(e); e != "" {
					ext = append(ext, e)
				}
			}
		}
		body, ok := braceBody(src, loc[1]-1)
		if !ok {
			return nil, fmt.Errorf("interface %s: unbalanced braces", name)
		}
		raw[name] = &pending{fields: parseFields(body), extends: ext}
	}

	// Flatten `extends`. The depth is 1 in practice (SeriesDetail extends
	// SeriesSummary, BookDetail extends BookSummary, Settings extends
	// UserSettings), but resolving iteratively costs nothing and does not care.
	for range len(raw) + 1 {
		progress := false
		for name, p := range raw {
			if len(p.extends) == 0 {
				continue
			}
			ready := true
			for _, e := range p.extends {
				if ep, ok := raw[e]; ok && len(ep.extends) > 0 {
					ready = false
				}
			}
			if !ready {
				continue
			}
			var merged []tsField
			for _, e := range p.extends {
				if ep, ok := raw[e]; ok {
					merged = append(merged, ep.fields...)
				}
			}
			p.fields = append(merged, p.fields...)
			p.extends = nil
			progress = true
			_ = name
		}
		if !progress {
			break
		}
	}
	for name, p := range raw {
		t.interfaces[name] = p.fields
	}
	return t, nil
}

// braceBody returns the text between the brace at src[open] and its match.
func braceBody(src string, open int) (string, bool) {
	if open < 0 || open >= len(src) || src[open] != '{' {
		return "", false
	}
	depth := 0
	for i := open; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[open+1 : i], true
			}
		}
	}
	return "", false
}

// parseFields reads the property lines of an interface body, skipping comments
// and anything that is not a simple `name: type` declaration.
func parseFields(body string) []tsField {
	var out []tsField
	inBlockComment := false
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case inBlockComment:
			if strings.Contains(line, "*/") {
				inBlockComment = false
			}
			continue
		case strings.HasPrefix(line, "/*"):
			if !strings.Contains(line, "*/") {
				inBlockComment = true
			}
			continue
		case line == "" || strings.HasPrefix(line, "//") || strings.HasPrefix(line, "*"):
			continue
		}
		if i := strings.Index(line, " //"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		m := reFieldLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		out = append(out, tsField{
			name:     m[1],
			optional: m[2] == "?",
			typ:      parseTypeRef(strings.TrimSuffix(strings.TrimSpace(m[3]), ";")),
		})
	}
	return out
}

// parseTypeRef reduces a type expression to a typeRef.
func parseTypeRef(expr string) typeRef {
	var ref typeRef
	for _, part := range strings.Split(expr, "|") {
		part = strings.TrimSpace(part)
		switch {
		case part == "null":
			ref.nullable = true
			continue
		case part == "undefined":
			continue
		}
		if strings.HasSuffix(part, "[]") {
			ref.array = true
			part = strings.TrimSuffix(part, "[]")
		}
		part = strings.TrimSpace(strings.Trim(part, "()"))
		switch {
		case strings.HasPrefix(part, "Record<"), strings.HasPrefix(part, "{"),
			strings.HasPrefix(part, "Partial<"), strings.HasPrefix(part, "Array<"):
			ref.unknown = true
		case ref.name == "":
			ref.name = part
		default:
			// A union of two real types: nothing here needs to model it, and
			// pretending otherwise would produce confident nonsense.
			ref.unknown = true
		}
	}
	return ref
}

// ---------------------------------------------------------------------------
// The comparison
// ---------------------------------------------------------------------------

type checker struct {
	ts       *tsTypes
	file     string
	findings []string
}

func (c *checker) report(path, format string, args ...any) {
	where := path
	if where == "" {
		where = "(root)"
	}
	c.findings = append(c.findings, fmt.Sprintf("%s: %s: %s", c.file, where, fmt.Sprintf(format, args...)))
}

// value walks a JSON value against its declared type.
func (c *checker) value(v any, ref typeRef, path string) {
	if v == nil {
		if !ref.nullable && !ref.unknown && ref.name != "" {
			// WP-06 acceptance 1: a field the server sends as null is typed
			// `T | null`, never `T | undefined` and never bare `T`.
			c.report(path, "the server sends null but types.ts declares %s, which does not admit null", ref.name)
		}
		return
	}
	if ref.array {
		items, ok := v.([]any)
		if !ok {
			c.report(path, "types.ts declares an array, the server sends %s", jsonKind(v))
			return
		}
		elem := ref
		elem.array = false
		elem.nullable = false
		for i, item := range items {
			c.value(item, elem, fmt.Sprintf("%s[%d]", path, i))
		}
		return
	}

	switch tv := v.(type) {
	case map[string]any:
		if ref.unknown || ref.name == "" {
			return // Record<string, unknown> and friends: nothing to check
		}
		fields, ok := c.ts.interfaces[ref.name]
		if !ok {
			c.report(path, "types.ts has no interface %s", ref.name)
			return
		}
		c.object(tv, ref.name, fields, path)
	case string:
		if members, ok := c.ts.enums[ref.name]; ok {
			for _, m := range members {
				if m == tv {
					return
				}
			}
			c.report(path, "value %q is not a member of %s (%s)", tv, ref.name, strings.Join(members, " | "))
		}
	}
}

func (c *checker) object(obj map[string]any, typeName string, fields []tsField, path string) {
	byName := make(map[string]tsField, len(fields))
	for _, f := range fields {
		byName[f.name] = f
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		child := k
		if path != "" {
			child = path + "." + k
		}
		f, ok := byName[k]
		if !ok {
			c.report(child, "the server sends this field; types.ts interface %s does not declare it", typeName)
			continue
		}
		c.value(obj[k], f.typ, child)
	}
	for _, f := range fields {
		if f.optional {
			continue
		}
		if _, ok := obj[f.name]; !ok {
			child := f.name
			if path != "" {
				child = path + "." + f.name
			}
			c.report(child, "types.ts interface %s requires this field; the server's response does not carry it", typeName)
		}
	}
}

func jsonKind(v any) string {
	switch v.(type) {
	case map[string]any:
		return "an object"
	case []any:
		return "an array"
	case string:
		return "a string"
	case bool:
		return "a boolean"
	default:
		return "a number"
	}
}

// ---------------------------------------------------------------------------
// The Go error-code enum
// ---------------------------------------------------------------------------

var reGoCode = regexp.MustCompile(`(?m)^\s*Code[A-Za-z0-9_]+\s*=\s*"([a-z_]+)"`)

// checkErrorCodes compares the `Code*` constants of internal/httpapi/errors.go
// with `ERROR_CODES` in types.ts. Only a handful of codes have a golden file,
// so the field diff alone cannot see a code the server can return and the
// client cannot name — the defect ruling E-13 was raised about.
func checkErrorCodes(root string, ts *tsTypes) ([]string, error) {
	path := filepath.Join(root, "internal", "httpapi", "errors.go")
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	client, ok := ts.constArrays["ERROR_CODES"]
	if !ok {
		return []string{"web/src/api/types.ts: ERROR_CODES is missing"}, nil
	}
	inClient := map[string]bool{}
	for _, c := range client {
		inClient[c] = true
	}
	server := map[string]bool{}
	for _, m := range reGoCode.FindAllStringSubmatch(string(src), -1) {
		server[m[1]] = true
	}
	if len(server) == 0 {
		return nil, fmt.Errorf("no Code* constants found in %s", path)
	}

	var findings []string
	names := make([]string, 0, len(server))
	for c := range server {
		names = append(names, c)
	}
	sort.Strings(names)
	for _, c := range names {
		if !inClient[c] {
			findings = append(findings, fmt.Sprintf(
				"internal/httpapi/errors.go: error code %q: the server can return it; types.ts ERROR_CODES does not list it", c))
		}
	}
	for _, c := range client {
		if !server[c] {
			findings = append(findings, fmt.Sprintf(
				"web/src/api/types.ts: ERROR_CODES lists %q; internal/httpapi/errors.go defines no such code", c))
		}
	}
	return findings, nil
}

// The kind's *value* may hold digits: `hv3` and `nestedhv3` do. An earlier
// `[a-z]+` here matched neither, so this gate reported "41 checks agree" while
// two kinds the scanner could write were missing from BOOK_KINDS — a check
// watching the wrong thing, which is the same failure it exists to catch.
var reGoKind = regexp.MustCompile(`(?m)^\s*Kind[A-Za-z0-9_]+\s+Kind\s*=\s*"([a-z0-9]+)"`)

// checkBookKinds compares the `Kind*` constants of internal/source/source.go
// with `BOOK_KINDS` in types.ts, for the same reason checkErrorCodes exists and
// after the same defect.
//
// The enum rule above can only judge a string that appears in a golden file,
// and every golden book is a `zip` or a `dir`. So when D-71 added `rar` and
// `nestedrar` to the server, the client enum kept its four members, tsc went on
// believing `book.kind` could not be `nestedrar`, and the badge rendered the
// raw wire value — NESTEDRAR on 8 volumes of the collection. Comparing the
// declarations rather than a sample is what closes that: a kind the scanner can
// write is a kind the client must be able to name, whether or not any fixture
// happens to contain one.
func checkBookKinds(root string, ts *tsTypes) ([]string, error) {
	path := filepath.Join(root, "internal", "source", "source.go")
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	client, ok := ts.constArrays["BOOK_KINDS"]
	if !ok {
		return []string{"web/src/api/types.ts: BOOK_KINDS is missing"}, nil
	}
	inClient := map[string]bool{}
	for _, k := range client {
		inClient[k] = true
	}
	server := map[string]bool{}
	for _, m := range reGoKind.FindAllStringSubmatch(string(src), -1) {
		server[m[1]] = true
	}
	if len(server) == 0 {
		return nil, fmt.Errorf("no Kind* constants found in %s", path)
	}

	var findings []string
	names := make([]string, 0, len(server))
	for k := range server {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		if !inClient[k] {
			findings = append(findings, fmt.Sprintf(
				"internal/source/source.go: book kind %q: the scanner can write it; types.ts BOOK_KINDS does not list it", k))
		}
	}
	for _, k := range client {
		if !server[k] {
			findings = append(findings, fmt.Sprintf(
				"web/src/api/types.ts: BOOK_KINDS lists %q; internal/source/source.go defines no such Kind", k))
		}
	}
	return findings, nil
}

// stripLineComments removes `// …` from every line. The only strings in the
// const arrays this parser reads are enum members — plain lowercase
// identifiers — so there is no string literal a `//` could legitimately be
// inside of.
func stripLineComments(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if j := strings.Index(line, "//"); j >= 0 {
			lines[i] = line[:j]
		}
	}
	return strings.Join(lines, "\n")
}

// ---------------------------------------------------------------------------
// GET /api/series query parameters
// ---------------------------------------------------------------------------

// reSeriesParam finds the query keys `seriesFilter` reads: q["root"], q.Get("q")
// and the queryEnum/queryInt helpers.
var reSeriesParam = regexp.MustCompile(`q\[\"([a-z_]+)\"\]|q\.Get\(\"([a-z_]+)\"\)|query(?:Enum|Int)\(r, \"([a-z_]+)\"`)

// checkSeriesParams compares the query keys `GET /api/series` accepts with the
// keys of `SeriesListParams` in types.ts.
//
// The golden files are responses, so a *request* parameter is invisible to the
// field diff — and `GET /api/series` is where the contract's live amendments
// land (A-4's `progress`, A-8's `scope`). A parameter the server honours and
// the client cannot name is a feature the UI silently does not have, which is
// precisely the shape of the defect ruling E-9 was raised about.
//
// Only this one endpoint is checked. It is the only one with a parameter set
// large enough for a gap to hide in, and a general query-parameter extractor
// over every handler would be a parser, not a grep.
func checkSeriesParams(root string, ts *tsTypes) ([]string, error) {
	path := filepath.Join(root, "internal", "httpapi", "series.go")
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	start := strings.Index(string(src), "func (s *Server) seriesFilter(")
	if start < 0 {
		return nil, fmt.Errorf("%s: seriesFilter not found", path)
	}
	body := string(src)[start:]
	if end := strings.Index(body, "\n}\n"); end > 0 {
		body = body[:end]
	}

	server := map[string]bool{}
	for _, m := range reSeriesParam.FindAllStringSubmatch(body, -1) {
		for _, g := range m[1:] {
			if g != "" {
				server[g] = true
			}
		}
	}
	if len(server) == 0 {
		return nil, fmt.Errorf("%s: no query parameters found in seriesFilter", path)
	}

	fields, ok := ts.interfaces["SeriesListParams"]
	if !ok {
		return []string{"web/src/api/types.ts: SeriesListParams is missing"}, nil
	}
	client := map[string]bool{}
	for _, f := range fields {
		client[f.name] = true
	}

	names := make([]string, 0, len(server))
	for k := range server {
		names = append(names, k)
	}
	sort.Strings(names)

	var findings []string
	for _, k := range names {
		if !client[k] {
			findings = append(findings, fmt.Sprintf(
				"internal/httpapi/series.go: GET /api/series honours ?%s=; types.ts SeriesListParams does not declare it", k))
		}
	}
	for _, f := range fields {
		if !server[f.name] {
			findings = append(findings, fmt.Sprintf(
				"web/src/api/types.ts: SeriesListParams declares %q; GET /api/series does not read it", f.name))
		}
	}
	return findings, nil
}

// ---------------------------------------------------------------------------
// The curated e2e series list
// ---------------------------------------------------------------------------

// Where the names live. Relative to the repository root, slash-separated.
const (
	curatedBashPath    = "scripts/e2e-config.sh"
	curatedPyPath      = "scripts/e2e-assert.py"
	curatedTSPath      = "web/e2e/shelf.ts"
	curatedFixturePath = "scripts/mkfixture/main.go"
	curatedShellPath   = "scripts/e2e.sh"
	curatedDocPath     = "docs/impl-plan.md"
	curatedIntPath     = "integration/harness_test.go"
)

var (
	// `CURATED=( … )` / `SYNTHETIC_EXTRA=( … )` in scripts/e2e-config.sh, and
	// `CURATED = [ … ]` / `SYNTHETIC_EXTRA = [ … ]` in scripts/e2e-assert.py.
	// The closing delimiter is anchored at column 0 so a `)` inside a name — of
	// which there are several, `Clover 클로버 (총4권)` among them — cannot end
	// the block early.
	reBashList = regexp.MustCompile(`(?ms)^(CURATED|SYNTHETIC_EXTRA)=\((.*?)^\)`)
	// Python, in both spellings the file uses: one entry per line closed by a
	// `]` at column 0, and the three-element list written inline. The
	// multi-line form requires the `[` to end its line, so the inline list
	// cannot start a block that runs to some later `]`.
	rePyList       = regexp.MustCompile(`(?ms)^(CURATED|SYNTHETIC_EXTRA)\s*=\s*\[[ \t]*$(.*?)^\]`)
	rePyInlineList = regexp.MustCompile(`(?m)^(CURATED|SYNTHETIC_EXTRA)\s*=\s*\[(.+)\]\s*$`)
	// `export const SERIES = { … } as const` in web/e2e/shelf.ts. The existing
	// reConstArr above matches `[ … ] as const` only, and these two are keyed
	// objects — the keys (`clover`, `battleRoyale`) are what the specs import.
	reTSConstObj = regexp.MustCompile(`(?ms)^export const (SERIES|SYNTHETIC_EXTRA) = \{(.*?)^\} as const`)
	// scripts/e2e.sh step 11b's one bare literal.
	reA11Fill = regexp.MustCompile(`(?m)^\s*A11_FILL="([^"]*)"`)
	// integration/harness_test.go's two declarations of the same names: the
	// `curated` slice that becomes scan.include_globs there, and the `const`
	// block of by-name handles the acceptance tests look series up with. One
	// region covers both — the slice closes with `}` and the const block with a
	// `)` at column 0, so `^\)` cannot end this early on a name containing `)`.
	reGoCuratedDecls = regexp.MustCompile(`(?ms)^var\s+curated\s*=\s*\[\]string\{.*?^\)`)
	// A Go string literal. Applied to comment-stripped source, so a name quoted
	// in prose — mkfixture's own comments name four of the series — is not
	// mistaken for the code that builds it.
	reGoLiteral = regexp.MustCompile(`"((?:[^"\\\n]|\\.)*)"`)
	// One row of the impl-plan table whose second column is an exact name, i.e.
	// a whole code span. The header row (`` `include_globs` entry (exact) ``)
	// and the `|---|` separator both fail to match, which is how they are
	// skipped.
	reDocRow = regexp.MustCompile("^\\|[^|]*\\|\\s*`([^`]+)`\\s*\\|")
	// The members of one extracted block: `"…"` for shell and Python, `'…'` for
	// the TypeScript object's values.
	reDoubleQuoted = regexp.MustCompile(`"([^"]*)"`)
	reSingleQuoted = regexp.MustCompile(`'([^']*)'`)
)

// curatedCopies is every copy of the list, already extracted from source text.
// Parsing is separated from IO so the comparison can be table-tested against
// small hand-written sources.
type curatedCopies struct {
	bash, bashExtra []string // scripts/e2e-config.sh — the source of truth
	py, pyExtra     []string // scripts/e2e-assert.py
	ts, tsExtra     []string // web/e2e/shelf.ts
	fixtureLiterals []string // every non-comment string literal of mkfixture
	a11Fill         string   // scripts/e2e.sh's A11_FILL, basename only
	doc             []string // docs/impl-plan.md §6.3's table
	integration     []string // integration/harness_test.go's `curated` + const block
}

// nameList is one declaration, named well enough for a finding to send the
// reader to the right block of the right file.
type nameList struct {
	where string // the file, as the reader would type it
	what  string // the declaration inside it
	names []string
}

// checkCuratedSeries compares the seven copies of the curated e2e series list.
//
// It is not an API contract, and it is here for exactly the reason
// checkBookKinds is: a declaration written out in four languages with nothing
// linking the copies, where the only thing that ever noticed a disagreement was
// a full `make e2e` — twenty minutes in, as `got 0, want 15`, naming neither
// the file to fix nor the series. Comparing the declarations moves that to
// `make lint`, in seconds, by name.
//
// Five of the six copies are load-bearing:
//
//	scripts/e2e-config.sh   CURATED becomes scan.include_globs; the source of truth
//	scripts/e2e-assert.py   CURATED is the curl tier's expectation, unpacked POSITIONALLY
//	web/e2e/shelf.ts        SERIES is the browser tier's, and every by-name helper
//	scripts/mkfixture/…     path literals that build the synthetic twin (D-49)
//	scripts/e2e.sh          A11_FILL, one archive by path (step 11b)
//
// The sixth, the §6.3 table in docs/impl-plan.md, is the *declared* source of
// truth and carries no code — which is precisely why it was the copy that had
// drifted when this check was written.
//
// The seventh, integration/harness_test.go, is compared ONE WAY: every name it
// holds must be in CURATED, but CURATED may hold names it does not, because it
// deliberately runs impl-plan §6.3's original ten rather than all fifteen. It
// was added after that copy was found carrying the `[만화] ` prefix the
// collection had dropped six sessions earlier — see the comment at the check
// itself for what that cost.
//
// The bash and Python lists are compared as SEQUENCES, not sets: e2e-assert.py
// unpacks its list positionally (`CLOVER, WOUNDS, … = CURATED[0], CURATED[1],
// …`), so a reorder that leaves both lists set-equal makes every one of those
// constants point at the wrong series while every assertion label stays right.
// The TypeScript object and the doc table are compared as sets, because nothing
// reads either by position — shelf.ts keys its names and sorts before comparing.
func checkCuratedSeries(root string) ([]string, error) {
	read := func(rel string) (string, error) {
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return "", fmt.Errorf("reading %s: %w", rel, err)
		}
		return string(b), nil
	}
	var srcs [7]string
	for i, rel := range []string{
		curatedBashPath, curatedPyPath, curatedTSPath,
		curatedFixturePath, curatedShellPath, curatedDocPath,
		curatedIntPath,
	} {
		s, err := read(rel)
		if err != nil {
			return nil, err
		}
		srcs[i] = s
	}
	copies, err := parseCuratedCopies(srcs[0], srcs[1], srcs[2], srcs[3], srcs[4], srcs[5], srcs[6])
	if err != nil {
		return nil, err
	}
	return compareCurated(copies), nil
}

func parseCuratedCopies(bashSrc, pySrc, tsSrc, fixtureSrc, shellSrc, docSrc, intSrc string) (*curatedCopies, error) {
	c := &curatedCopies{}

	bash := namedBlocks(reBashList, bashSrc, quotedPerLine)
	py := namedBlocks(rePyList, pySrc, quotedPerLine)
	for name, names := range namedBlocks(rePyInlineList, pySrc, quotedPerLine) {
		if _, ok := py[name]; !ok {
			py[name] = names
		}
	}
	ts := namedBlocks(reTSConstObj, tsSrc, tsObjectValues)
	for _, want := range []struct {
		blocks map[string][]string
		key    string
		where  string
		dst    *[]string
	}{
		{bash, "CURATED", curatedBashPath, &c.bash},
		{bash, "SYNTHETIC_EXTRA", curatedBashPath, &c.bashExtra},
		{py, "CURATED", curatedPyPath, &c.py},
		{py, "SYNTHETIC_EXTRA", curatedPyPath, &c.pyExtra},
		{ts, "SERIES", curatedTSPath, &c.ts},
		{ts, "SYNTHETIC_EXTRA", curatedTSPath, &c.tsExtra},
	} {
		names, ok := want.blocks[want.key]
		if !ok || len(names) == 0 {
			return nil, fmt.Errorf("%s: no %s list found; contractcheck cannot compare the curated series", want.where, want.key)
		}
		*want.dst = names
	}

	for _, m := range reGoLiteral.FindAllStringSubmatch(stripLineComments(fixtureSrc), -1) {
		c.fixtureLiterals = append(c.fixtureLiterals, m[1])
	}
	if len(c.fixtureLiterals) == 0 {
		return nil, fmt.Errorf("%s: no string literals found", curatedFixturePath)
	}

	m := reA11Fill.FindStringSubmatch(shellSrc)
	if m == nil {
		return nil, fmt.Errorf(`%s: no A11_FILL="…" assignment found`, curatedShellPath)
	}
	c.a11Fill = m[1]
	if i := strings.LastIndex(c.a11Fill, "/"); i >= 0 {
		c.a11Fill = c.a11Fill[i+1:]
	}

	doc, err := docCuratedTable(docSrc)
	if err != nil {
		return nil, err
	}
	c.doc = doc

	decls := reGoCuratedDecls.FindString(intSrc)
	if decls == "" {
		return nil, fmt.Errorf("%s: no `var curated = []string{…}` declaration found; "+
			"contractcheck cannot compare the integration harness's copy", curatedIntPath)
	}
	seen := map[string]bool{}
	for _, m := range reGoLiteral.FindAllStringSubmatch(stripLineComments(decls), -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			c.integration = append(c.integration, m[1])
		}
	}
	if len(c.integration) == 0 {
		return nil, fmt.Errorf("%s: the `curated` declaration holds no names", curatedIntPath)
	}
	return c, nil
}

// namedBlocks runs one of the block regexes and reads each block's members with
// `values`. Submatch 1 is the declaration's name, submatch 2 its body.
func namedBlocks(re *regexp.Regexp, src string, values func(string) []string) map[string][]string {
	out := map[string][]string{}
	for _, m := range re.FindAllStringSubmatch(src, -1) {
		out[m[1]] = values(m[2])
	}
	return out
}

// quotedPerLine reads the double-quoted strings of a shell or Python list body,
// in order. Trailing `#` comments are dropped first: every entry of
// scripts/e2e-config.sh's CURATED carries one describing the shape it covers.
func quotedPerLine(block string) []string {
	var out []string
	for _, line := range strings.Split(block, "\n") {
		for _, m := range reDoubleQuoted.FindAllStringSubmatch(stripHashComment(line), -1) {
			out = append(out, m[1])
		}
	}
	return out
}

// tsObjectValues reads the single-quoted values of a `{ key: 'value', … }` body
// in source order. The keys are bare identifiers, so nothing else is quoted.
func tsObjectValues(block string) []string {
	var out []string
	for _, m := range reSingleQuoted.FindAllStringSubmatch(stripLineComments(block), -1) {
		out = append(out, m[1])
	}
	return out
}

// stripHashComment removes a `#` comment from one line of shell or Python. A
// `#` inside a quoted string is left alone — no curated name contains one
// today, and a check that silently truncated a name that did would be worse
// than useless.
func stripHashComment(line string) string {
	var inDouble, inSingle bool
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '#':
			if !inDouble && !inSingle {
				return line[:i]
			}
		}
	}
	return line
}

// docCuratedTable reads the exact-name column of impl-plan §6.3's
// `#### The curated set` table.
func docCuratedTable(src string) ([]string, error) {
	const heading = "#### The curated set"
	start := strings.Index(src, heading)
	if start < 0 {
		return nil, fmt.Errorf("%s: no %q heading; contractcheck cannot compare the curated series", curatedDocPath, heading)
	}
	var out []string
	for _, line := range strings.Split(src[start+len(heading):], "\n") {
		if strings.HasPrefix(line, "#### ") || strings.HasPrefix(line, "### ") || strings.HasPrefix(line, "## ") {
			break
		}
		if m := reDocRow.FindStringSubmatch(line); m != nil {
			out = append(out, m[1])
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: the %q table has no rows whose second column is an exact name", curatedDocPath, heading)
	}
	return out, nil
}

// compareCurated is the whole comparison: every finding names both files and
// the series, in the voice of checkBookKinds's.
func compareCurated(c *curatedCopies) []string {
	bash := nameList{where: curatedBashPath, what: "CURATED", names: c.bash}
	py := nameList{where: curatedPyPath, what: "CURATED", names: c.py}
	ts := nameList{where: curatedTSPath, what: "SERIES", names: c.ts}
	bashX := nameList{where: curatedBashPath, what: "SYNTHETIC_EXTRA", names: c.bashExtra}
	pyX := nameList{where: curatedPyPath, what: "SYNTHETIC_EXTRA", names: c.pyExtra}
	tsX := nameList{where: curatedTSPath, what: "SYNTHETIC_EXTRA", names: c.tsExtra}

	var findings []string

	// 1 vs 2 — and in order, because the Python side unpacks positionally.
	findings = append(findings, bothWays(bash, py,
		"%s: curated series %q: scan.include_globs is built from it; %s's %s does not list it, so the curl tier will not expect it",
		"%s: %s lists %q; %s's %s does not, so scan.include_globs never matches it and the curl tier fails with `got 0, want …`")...)
	findings = append(findings, orderDiff(bash, py)...)

	// 1 vs 3.
	findings = append(findings, bothWays(bash, ts,
		"%s: curated series %q: scan.include_globs is built from it; %s's %s does not name it, so no browser assertion can reach it",
		"%s: %s names %q; %s's %s does not list it, so expectCuratedLibrary asserts a series the server was never told to index")...)

	// The D-49 extras, which only the synthetic round indexes.
	findings = append(findings, bothWays(bashX, pyX,
		"%s: synthetic-only series %q: --synthetic appends it to scan.include_globs; %s's %s does not list it",
		"%s: %s lists %q; %s's %s does not append it to scan.include_globs")...)
	findings = append(findings, bothWays(bashX, tsX,
		"%s: synthetic-only series %q: --synthetic appends it to scan.include_globs; %s's %s does not name it",
		"%s: %s names %q; %s's %s does not append it to scan.include_globs")...)

	// 4 — the synthetic tree has to build every name the synthetic round asks
	// the scanner for, or that round indexes one series fewer than it expects.
	for _, n := range append(append([]string{}, c.bash...), c.bashExtra...) {
		if !fixtureBuilds(c.fixtureLiterals, n) {
			findings = append(findings, fmt.Sprintf(
				"%s: curated series %q: %s's %s puts it in scan.include_globs, and no path literal here builds it "+
					"(neither %q nor anything under %q/) — the synthetic round would index one series fewer",
				curatedFixturePath, n, curatedBashPath, "CURATED", n, n))
		}
	}

	// 5 — step 11b copies one fixture archive into the added root, and it only
	// becomes a series there if scan.include_globs names it.
	if !contains(c.bash, c.a11Fill) && !contains(c.bashExtra, c.a11Fill) {
		findings = append(findings, fmt.Sprintf(
			"%s: A11_FILL fills the A-11 root with %q; %s's CURATED does not list it, so the per-root rescan of step 11b "+
				"gives that root no series to lose", curatedShellPath, c.a11Fill, curatedBashPath))
	}

	// 7 — the declared source of truth, which carries no code and therefore
	// nothing but this check to keep it honest.
	doc := nameList{where: curatedDocPath, what: "§6.3 curated-set table", names: c.doc}
	findings = append(findings, bothWays(bash, doc,
		"%s: curated series %q: scan.include_globs is built from it; %s's %s has no row for it",
		"%s: the %s has a row for %q; %s's %s does not list it, so the table describes a subset the E2E round does not run")...)

	// 7 — the integration harness, checked ONE WAY ONLY.
	//
	// `integration/harness_test.go` deliberately runs a subset: impl-plan §6.3's
	// original ten, not the fifteen CURATED has grown to, because the rounds it
	// drives (a full scan, a whole-volume stream, a cache wipe) cost minutes per
	// series. So a name in CURATED and not here is not a finding.
	//
	// A name HERE and not in CURATED is, and it is the one this check was added
	// for: those names build the harness's own scan.include_globs, and when the
	// collection was renamed and the dead `[만화] ` prefix stayed behind, they
	// matched nothing. The suite indexed an EMPTY library and every acceptance
	// test failed — except NFR-PRF-005, which measured the resident memory of a
	// server holding nothing and passed. It stayed that way for six sessions
	// because this file is not one of the five gates and needs SHELF_TEST_ROOT,
	// so nobody ran it, and the six copies above never compared it.
	for _, n := range c.integration {
		if !contains(c.bash, n) && !contains(c.bashExtra, n) {
			findings = append(findings, fmt.Sprintf(
				"%s: the integration harness indexes %q; %s's CURATED does not list it, "+
					"so its scan.include_globs matches nothing and the suite runs against an empty library",
				curatedIntPath, n, curatedBashPath))
		}
	}
	return findings
}

// bothWays reports every name one declaration holds and the other does not.
// Each sentence starts at the file the reader should open first, so the two
// templates take their arguments in different orders: `missing` is formatted
// with (a.where, name, b.where, b.what) — a holds the name, b is short of it —
// and `surplus` with (b.where, b.what, name, a.where, a.what). The call sites
// spell both out in full rather than sharing one vague wording.
func bothWays(a, b nameList, missing, surplus string) []string {
	var out []string
	for _, n := range onlyIn(a.names, b.names) {
		out = append(out, fmt.Sprintf(missing, a.where, n, b.where, b.what))
	}
	for _, n := range onlyIn(b.names, a.names) {
		out = append(out, fmt.Sprintf(surplus, b.where, b.what, n, a.where, a.what))
	}
	return out
}

// orderDiff reports the first position at which two set-equal lists disagree.
// Only reached when the sets already match: a missing or surplus name is
// reported by bothWays and would make every later index differ for one reason.
func orderDiff(a, b nameList) []string {
	if len(onlyIn(a.names, b.names)) > 0 || len(onlyIn(b.names, a.names)) > 0 {
		return nil
	}
	for i := range a.names {
		if a.names[i] == b.names[i] {
			continue
		}
		return []string{fmt.Sprintf(
			"%s: %s holds %q at index %d where %s's %s holds %q: the two lists must be order-identical, because "+
				"the `CLOVER, WOUNDS, … = CURATED[0], CURATED[1], …` unpacking just below reads them by position — "+
				"a reorder asserts the wrong series under the right label",
			b.where, b.what, b.names[i], i, a.where, a.what, a.names[i])}
	}
	return nil
}

// onlyIn returns the members of a that b does not hold, in a's order.
func onlyIn(a, b []string) []string {
	inB := make(map[string]bool, len(b))
	for _, n := range b {
		inB[n] = true
	}
	var out []string
	for _, n := range a {
		if !inB[n] {
			out = append(out, n)
		}
	}
	return out
}

func contains(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

// fixtureBuilds answers whether mkfixture writes anything at or under one
// curated name. A series is either a file the builder names outright
// (`바퀴.zip`) or a directory every one of whose volumes is written under it
// (`군계 1~25/군계(軍鷄) 01권.zip`), so one literal of either shape is proof the
// synthetic tree carries that series.
func fixtureBuilds(literals []string, name string) bool {
	prefix := name + "/"
	for _, lit := range literals {
		if lit == name || strings.HasPrefix(lit, prefix) {
			return true
		}
	}
	return false
}
