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
		// `checked` is not one entry per golden file: it also carries the two
		// checks derived from source rather than from a golden (ERROR_CODES from
		// internal/httpapi/errors.go, SeriesListParams from series.go). Calling
		// the total "golden files" sent a reader hunting for two files that do
		// not exist and made them doubt the docs quoting this line — so say what
		// it actually is.
		fmt.Printf("contractcheck: %d contract checks agree with web/src/api/types.ts\n", len(checked))
		return
	}
	fmt.Fprintf(os.Stderr, "contractcheck: %d disagreement(s) between the server's golden JSON and web/src/api/types.ts\n\n", len(findings))
	for _, f := range findings {
		fmt.Fprintln(os.Stderr, "  "+f)
	}
	fmt.Fprintf(os.Stderr, "\nThe contract is arch-backend.md §7 as amended by impl-plan.md §0.3.\n"+
		"Fix the side that disagrees with it; do not adjust this check.\n")
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

	paramFindings, err := checkSeriesParams(root, ts)
	if err != nil {
		return nil, nil, err
	}
	findings = append(findings, paramFindings...)
	checked = append(checked, fmt.Sprintf("%-28s -> SeriesListParams", "internal/httpapi/series.go"))
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
