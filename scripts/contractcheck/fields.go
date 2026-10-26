package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// checkInterfaceFields compares the field names of every interface across the
// three places that spell them: arch §7's ```ts blocks, web/src/api/types.ts,
// and the JSON tags of the Go structs that encode or decode them.
//
// The golden diff above only reaches Go through the keys one golden instance
// happens to carry, and arch through nothing at all — so `Settings.library_scope`
// was served, typed and tested for several sessions while §7.8's interface did
// not declare it, and every check stayed green. This closes both gaps by
// comparing declarations directly, with no golden in between:
//
//   - arch ↔ types.ts, every interface arch declares: names and `?`.
//   - types.ts ↔ Go, every response type reachable from goldenType: names, and
//     `?` against `omitempty` (measured 0 disagreements when this was written).
//   - types.ts ↔ Go, the request bodies: names only. A decoder needs no
//     omitempty for a field the client may leave out (RootCreate.label).
//
// Field types are not compared here. The golden diff compares them, but only
// for the values a golden carries: null against `| null`, arrays, objects by
// interface, and scalars by kind (string, number, boolean) with enum and
// literal membership on top (checker.scalar). A field no golden sends is not
// type-checked anywhere.
func checkInterfaceFields(root string, ts *tsTypes) ([]string, error) {
	arch, gs, err := readInterfaceSources(root)
	if err != nil {
		return nil, err
	}
	tsSrc, err := os.ReadFile(filepath.Join(root, "web", "src", "api", "types.ts"))
	if err != nil {
		return nil, fmt.Errorf("reading web/src/api/types.ts: %w", err)
	}
	findings := tsSyntaxFindings(string(tsSrc))
	return append(findings, compareInterfaceFields(arch, ts, gs, repoFieldContract())...), nil
}

// tsSyntaxFindings names every line of a types.ts interface that parseFields,
// which reads one `name?: type` per line, would misread: a second declaration
// on the line (it vanishes when no other side has it), a type that continues
// onto another line (the response walk loses the names there), and an inline
// object (its members would read as the interface's own).
func tsSyntaxFindings(src string) []string {
	masked, err := maskTSCode(src)
	if err != nil {
		return []string{"web/src/api/types.ts: " + err.Error()}
	}
	var findings []string
	for _, loc := range reInterface.FindAllStringSubmatchIndex(masked, -1) {
		name := masked[loc[2]:loc[3]]
		body, ok := braceBody(masked, loc[1]-1)
		if !ok {
			findings = append(findings, fmt.Sprintf("web/src/api/types.ts: interface %s: unbalanced braces", name))
			continue
		}
		for _, line := range strings.Split(body, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			bad := ""
			if strings.HasPrefix(line, "|") || strings.HasPrefix(line, "&") {
				bad = "continues the line before it"
			} else if m := reArchField.FindStringSubmatch(line); m != nil {
				expr := strings.TrimSpace(m[3])
				switch {
				case strings.Contains(expr, "{"):
					bad = "declares an inline object"
				case strings.Contains(strings.TrimSuffix(expr, ";"), ";"):
					bad = "holds a second declaration"
				case expr == "" || strings.HasSuffix(expr, "|") || strings.HasSuffix(expr, "&"):
					bad = "continues onto the next line"
				}
			}
			if bad != "" {
				findings = append(findings, fmt.Sprintf(
					"web/src/api/types.ts: interface %s: the line %q %s, which contractcheck reads as one declaration per line",
					name, line, bad))
			}
		}
	}
	return findings
}

// fieldSourceDirs are the Go packages whose structs are the wire shapes.
// userdata is here because the progress export is encoded from its structs
// directly.
var fieldSourceDirs = []string{"internal/httpapi", "internal/userdata"}

func readInterfaceSources(root string) (map[string]fieldSet, map[string]fieldSet, error) {
	archSrc, err := os.ReadFile(filepath.Join(root, "docs", "arch-backend.md"))
	if err != nil {
		return nil, nil, fmt.Errorf("reading docs/arch-backend.md: %w", err)
	}
	arch, err := parseArchInterfaces(string(archSrc))
	if err != nil {
		return nil, nil, err
	}
	files := map[string]string{}
	for _, dir := range fieldSourceDirs {
		paths, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(dir), "*.go"))
		if err != nil {
			return nil, nil, err
		}
		for _, p := range paths {
			if strings.HasSuffix(p, "_test.go") {
				continue
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return nil, nil, err
			}
			rel, _ := filepath.Rel(root, p)
			files[filepath.ToSlash(rel)] = string(b)
		}
	}
	gs, err := parseGoJSONStructs(files)
	if err != nil {
		return nil, nil, err
	}
	return arch, gs, nil
}

// fieldSet maps a field's wire name to whether it may be absent: `name?:` in
// TypeScript, `omitempty` in Go.
type fieldSet map[string]bool

func (f fieldSet) has(name string) bool { _, ok := f[name]; return ok }

// fieldContract is every name that is not the same in all three files, each
// written down once.
type fieldContract struct {
	// archToTS maps an arch interface, or an inline object `Iface.field`, to
	// the types.ts interface that declares it, when the names differ.
	archToTS map[string]string
	// tsWithoutArch are types.ts interfaces arch §7 has no ```ts block for,
	// each with the reason.
	tsWithoutArch map[string]string
	// responseRoots are the types the server encodes at the top level; every
	// interface reachable from them is compared with its Go struct.
	responseRoots []string
	// tsToGo maps a types.ts interface to its Go struct as `pkg.Name`, when
	// that is not `httpapi.<same name>`.
	tsToGo map[string]string
	// requestPairs maps a request body's types.ts interface to its Go struct.
	requestPairs map[string]string
	// goOnly are `pkg.Struct.field` JSON fields the client deliberately does
	// not declare, each with the reason.
	goOnly map[string]string
}

func repoFieldContract() fieldContract {
	roots := []string{"ErrorResponse"}
	for _, t := range goldenType {
		if !contains(roots, t) {
			roots = append(roots, t)
		}
	}
	sort.Strings(roots)
	return fieldContract{
		archToTS: map[string]string{
			"Settings.server":      "ServerSettings",
			"CacheUsage.entries":   "CacheUsageEntry",
			"ProgressExport.items": "ProgressExportItem",
			"ProgressExport.prefs": "ProgressExportPref",
			"ErrorResponse.error":  "ErrorBody",
		},
		tsWithoutArch: map[string]string{
			"AuthStatus":           "arch §7.12 describes GET /api/auth/status in its endpoint table, not a ```ts block",
			"CachePurgeResult":     "arch §7.9 gives DELETE /api/cache's body inline in the endpoint table",
			"Health":               "arch §7.4's GET /api/health row spells the body inline (A-15 is checked by checkThumbFlushFields)",
			"LoginRequest":         "arch §7.12 gives POST /api/auth/login's body inline in the endpoint table",
			"ProgressImportResult": "arch §7.11 gives POST /api/progress/import's result inline in the endpoint table",
			"RootsResponse":        "arch §7.4's GET /api/roots row spells `{items: Root[]}` inline",
			"ScanLogParams":        "query parameters, which arch §7.10 lists in a table",
			"ScanLogResponse":      "arch §7.10's GET /api/scan/log row spells the body inline",
			"ScanRunResponse":      "arch §7.10's POST /api/scan row spells the `202` body inline",
			"ScanStartRequest":     "arch §7.10's POST /api/scan row spells the request body inline",
			"SeriesListParams":     "query parameters, which arch §7.5 lists in a table (checkSeriesParams compares them with Go)",
			"UserSettings":         "the user-mutable half of Settings: arch §7.8 declares those fields on Settings itself",
		},
		responseRoots: roots,
		tsToGo: map[string]string{
			"CacheUsageEntry":      "httpapi.CacheUsageItem",
			"CachePurgeResult":     "httpapi.PurgeResult",
			"ScanRunResponse":      "httpapi.RunAccepted",
			"ProgressExport":       "userdata.Export",
			"ProgressExportItem":   "userdata.ExportItem",
			"ProgressExportPref":   "userdata.ExportPref",
			"ProgressImportResult": "userdata.ImportResult",
		},
		requestPairs: map[string]string{
			"ProgressUpdate":   "httpapi.progressUpdateBody",
			"BookPrefsUpdate":  "httpapi.bookPrefsUpdateBody",
			"RootCreate":       "httpapi.RootCreate",
			"LoginRequest":     "httpapi.loginBody",
			"ScanStartRequest": "httpapi.scanRequestBody",
			"UserSettings":     "httpapi.settingsUpdateBody",
		},
		goOnly: map[string]string{
			"httpapi.Health.verbose": "?verbose=1 pool and queue counters are diagnostics no client reads; " +
				"A-15's four thumb fields are checked by checkThumbFlushFields",
		},
	}
}

var (
	reTSBlock       = regexp.MustCompile("(?s)```ts\n(.*?)```")
	reArchInterface = regexp.MustCompile(`(?m)^(?:export )?interface ([A-Za-z0-9_]+)(?:\s+extends\s+([A-Za-z0-9_,\s]+?))?\s*\{`)
	reArchField     = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)(\??):\s*(.*)$`)
	reTSIdent       = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)
)

// parseArchInterfaces reads the interfaces of every ```ts block in arch, with
// `extends` flattened. An inline object field (`server: { … }`) is recorded as
// its own entry `Iface.field`, one level deep, and its members are not fields
// of Iface — parseFields, written for types.ts where every nested shape is
// named, would fold them in.
func parseArchInterfaces(src string) (map[string]fieldSet, error) {
	var code strings.Builder
	for _, m := range reTSBlock.FindAllStringSubmatch(src, -1) {
		code.WriteString(m[1])
		code.WriteString("\n")
	}
	ts, err := maskTSCode(code.String())
	if err != nil {
		return nil, fmt.Errorf("docs/arch-backend.md: %w", err)
	}
	own := map[string]fieldSet{}
	extends := map[string][]string{}
	for _, loc := range reArchInterface.FindAllStringSubmatchIndex(ts, -1) {
		name := ts[loc[2]:loc[3]]
		body, ok := braceBody(ts, loc[1]-1)
		if !ok {
			return nil, fmt.Errorf("docs/arch-backend.md: interface %s: unbalanced braces", name)
		}
		if loc[4] >= 0 {
			for _, e := range strings.Split(ts[loc[4]:loc[5]], ",") {
				if e = strings.TrimSpace(e); e != "" {
					extends[name] = append(extends[name], e)
				}
			}
		}
		top, nested := parseArchFields(body)
		own[name] = top
		for field, fs := range nested {
			own[name+"."+field] = fs
		}
	}
	out := map[string]fieldSet{}
	var flatten func(name string, seen map[string]bool) fieldSet
	flatten = func(name string, seen map[string]bool) fieldSet {
		fs := fieldSet{}
		if seen[name] {
			return fs
		}
		seen[name] = true
		for _, e := range extends[name] {
			for k, v := range flatten(e, seen) {
				fs[k] = v
			}
		}
		for k, v := range own[name] {
			fs[k] = v
		}
		return fs
	}
	for name := range own {
		out[name] = flatten(name, map[string]bool{})
	}
	return out, nil
}

// maskTSCode removes comments and blanks the inside of string literals, so
// that only syntax is left to count: arch's comments carry braces
// (`// 201 Created  +  Location: {base_path}/…`), and a string type may hold
// `//`, `;` or a brace. Line breaks are kept, and a comment becomes a space so
// the tokens either side of it stay apart. A template literal type with a
// `${…}` hole can nest further quotes, which this does not track, so it is
// refused rather than misread.
func maskTSCode(src string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch {
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			for i < len(src) && src[i] != '\n' {
				i++
			}
			if i < len(src) {
				b.WriteByte('\n')
			}
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			b.WriteByte(' ')
			i += 2
			for i+1 < len(src) && !(src[i] == '*' && src[i+1] == '/') {
				if src[i] == '\n' {
					b.WriteByte('\n')
				}
				i++
			}
			i++
		case c == '"' || c == '\'' || c == '`':
			b.WriteByte(c)
			for i++; i < len(src) && src[i] != c; i++ {
				switch {
				case src[i] == '\\' && i+1 < len(src):
					b.WriteString("__")
					i++
				case c == '`' && src[i] == '$' && i+1 < len(src) && src[i+1] == '{':
					return "", fmt.Errorf("a template literal type with a ${…} hole is not supported by this check")
				case src[i] == '\n':
					b.WriteByte('\n')
				default:
					b.WriteByte('_')
				}
			}
			if i < len(src) {
				b.WriteByte(c)
			}
		default:
			b.WriteByte(c)
		}
	}
	return b.String(), nil
}

// parseArchFields returns an interface body's own fields and, for each field
// whose type is an inline object, that object's fields. The body has been
// through maskTSCode. It reads statements, not lines: arch writes
// `prefs: { book_id: ID; reading_direction: … }[]` with several members on one
// line, and an object's brace may also open on the line after its field.
func parseArchFields(body string) (fieldSet, map[string]fieldSet) {
	top := fieldSet{}
	nested := map[string]fieldSet{}
	depth := 0
	inner := ""
	lastTop := "" // the top-level field just read, if nothing has followed it
	var stmt strings.Builder
	flush := func(opensObject bool) {
		text := strings.TrimSpace(stmt.String())
		stmt.Reset()
		m := reArchField.FindStringSubmatch(text)
		if m == nil {
			if opensObject && depth == 0 && text == "" && lastTop != "" {
				inner = lastTop
				nested[inner] = fieldSet{}
			}
			if text != "" {
				lastTop = ""
			}
			return
		}
		switch depth {
		case 0:
			top[m[1]] = m[2] == "?"
			lastTop = ""
			if opensObject {
				inner = m[1]
				nested[inner] = fieldSet{}
			} else if strings.TrimSpace(m[3]) == "" {
				lastTop = m[1]
			}
		case 1:
			if inner != "" {
				nested[inner][m[1]] = m[2] == "?"
			}
		}
	}
	for _, r := range body {
		switch r {
		case '{':
			flush(true)
			depth++
		case '}':
			flush(false)
			depth--
			if depth == 0 {
				inner = ""
			}
		case ';', '\n':
			flush(false)
		default:
			stmt.WriteRune(r)
		}
	}
	flush(false)
	return top, nested
}

// parseGoJSONStructs reads every struct type of the given files as
// `pkg.Name` → its JSON fields, the way encoding/json sees them: a field
// tagged `json:"-"` or unexported is not on the wire, and an embedded struct of
// the same package promotes its fields unless a shallower field has the name.
// What this cannot resolve is an error rather than a guess — an embedded
// struct from another package anywhere under a struct that has JSON tags, a
// JSON name two fields of one struct share or two embedding paths promote at
// the same depth (encoding/json drops it), and one `pkg.Name` in two files. A
// struct with no JSON tag at all is not a wire shape (statusRecorder embeds
// http.ResponseWriter) and its embeddings are not checked.
func parseGoJSONStructs(files map[string]string) (map[string]fieldSet, error) {
	type rawStruct struct {
		fields   fieldSet
		embedded []string
		foreign  []string // embedded types from other packages
		tagged   bool     // has a json tag: a wire shape
		file     string
	}
	raw := map[string]*rawStruct{}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, path := range names {
		f, err := parser.ParseFile(token.NewFileSet(), path, files[path], parser.SkipObjectResolution)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		pkg := f.Name.Name
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				tsp := spec.(*ast.TypeSpec)
				st, ok := tsp.Type.(*ast.StructType)
				if !ok {
					continue
				}
				key := pkg + "." + tsp.Name.Name
				if prev, dup := raw[key]; dup {
					return nil, fmt.Errorf("%s: struct %s is also declared in %s; the check keys structs by package name", path, key, prev.file)
				}
				rs := &rawStruct{fields: fieldSet{}, file: path}
				for _, field := range st.Fields.List {
					tag := ""
					if field.Tag != nil {
						unq, err := strconv.Unquote(field.Tag.Value)
						if err != nil {
							return nil, fmt.Errorf("%s: %s: tag %s: %w", path, tsp.Name.Name, field.Tag.Value, err)
						}
						tag = reflect.StructTag(unq).Get("json")
						rs.tagged = rs.tagged || tag != ""
					}
					name, opts, _ := strings.Cut(tag, ",")
					omit := strings.Contains(","+opts+",", ",omitempty,")
					if len(field.Names) == 0 {
						if name == "-" {
							continue
						}
						if name != "" {
							if _, dup := rs.fields[name]; dup {
								return nil, fmt.Errorf("%s: %s has two fields named %q in JSON; encoding/json drops both", path, tsp.Name.Name, name)
							}
							rs.fields[name] = omit
							continue
						}
						id := embeddedIdent(field.Type)
						if id == "" {
							rs.foreign = append(rs.foreign, types2String(field.Type))
							continue
						}
						rs.embedded = append(rs.embedded, pkg+"."+id)
						continue
					}
					for _, n := range field.Names {
						if name == "-" || !n.IsExported() {
							continue
						}
						wire := name
						if wire == "" {
							wire = n.Name
						}
						if _, dup := rs.fields[wire]; dup {
							return nil, fmt.Errorf("%s: %s has two fields named %q in JSON; encoding/json drops both", path, tsp.Name.Name, wire)
						}
						rs.fields[wire] = omit
					}
				}
				raw[key] = rs
			}
		}
	}

	// flatten resolves promotion: own fields at depth 0, each embedded
	// struct's at depth 1 and below; the shallowest name wins, and two at the
	// same shallowest depth from different embedded structs is the conflict
	// encoding/json resolves by dropping the field.
	// via is the embedded field of the struct being flattened that a name
	// came through: two names at the same depth through two different
	// embedded fields conflict even when both lead to the same struct (a
	// diamond), exactly as in encoding/json.
	type promoted struct {
		opt   bool
		depth int
		via   string
	}
	var flatten func(name string, depth int, wire bool, seen map[string]bool) (map[string]promoted, error)
	flatten = func(name string, depth int, wire bool, seen map[string]bool) (map[string]promoted, error) {
		out := map[string]promoted{}
		rs, ok := raw[name]
		if !ok {
			return nil, fmt.Errorf("embedded struct %s is not among the compared files", name)
		}
		if seen[name] {
			return out, nil
		}
		seen[name] = true
		defer delete(seen, name)
		if wire && len(rs.foreign) > 0 {
			return nil, fmt.Errorf("%s embeds %s, which this check cannot resolve", name, strings.Join(rs.foreign, ", "))
		}
		for k, v := range rs.fields {
			out[k] = promoted{opt: v, depth: depth}
		}
		conflict := map[string]string{}
		for _, e := range rs.embedded {
			sub, err := flatten(e, depth+1, wire, seen)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			for k, v := range sub {
				v.via = e
				cur, ok := out[k]
				switch {
				case !ok || v.depth < cur.depth:
					out[k] = v
					delete(conflict, k)
				case v.depth == cur.depth && cur.depth > depth && cur.via != v.via:
					conflict[k] = cur.via + " and " + v.via
				}
			}
		}
		for k, who := range conflict {
			return nil, fmt.Errorf("%s: JSON field %q is promoted by both %s at the same depth; encoding/json drops it", name, k, who)
		}
		return out, nil
	}
	out := map[string]fieldSet{}
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, name := range keys {
		pm, err := flatten(name, 0, raw[name].tagged, map[string]bool{})
		if err != nil {
			return nil, fmt.Errorf("%s: %w", raw[name].file, err)
		}
		fs := fieldSet{}
		for k, v := range pm {
			fs[k] = v.opt
		}
		out[name] = fs
	}
	return out, nil
}

func types2String(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.SelectorExpr:
		return types2String(t.X) + "." + t.Sel.Name
	case *ast.StarExpr:
		return "*" + types2String(t.X)
	case *ast.Ident:
		return t.Name
	}
	return fmt.Sprintf("%T", e)
}

func embeddedIdent(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return embeddedIdent(t.X)
	}
	return ""
}

func tsFieldSet(ts *tsTypes, name string) (fieldSet, bool) {
	fields, ok := ts.interfaces[name]
	if !ok {
		return nil, false
	}
	fs := fieldSet{}
	for _, f := range fields {
		fs[f.name] = f.optional
	}
	return fs, true
}

// compareInterfaceFields applies fieldContract to the three parsed sources.
func compareInterfaceFields(arch map[string]fieldSet, ts *tsTypes, gs map[string]fieldSet, c fieldContract) []string {
	var findings []string
	report := func(format string, args ...any) { findings = append(findings, fmt.Sprintf(format, args...)) }

	// arch ↔ types.ts.
	declared := map[string]bool{}
	for archName, af := range arch {
		tsName := archName
		if n, ok := c.archToTS[archName]; ok {
			tsName = n
		}
		declared[tsName] = true
		tf, ok := tsFieldSet(ts, tsName)
		if !ok {
			report("docs/arch-backend.md §7: %s has no types.ts interface %s (map it in archToTS if the name differs)", archName, tsName)
			continue
		}
		where := fmt.Sprintf("arch %s / types.ts %s", archName, tsName)
		findings = append(findings, diffFieldSets(where, "arch", af, "types.ts", tf, true, nil, "")...)
	}
	tsNames := make([]string, 0, len(ts.interfaces))
	for n := range ts.interfaces {
		tsNames = append(tsNames, n)
	}
	sort.Strings(tsNames)
	for _, n := range tsNames {
		_, excused := c.tsWithoutArch[n]
		switch {
		case !declared[n] && !excused:
			report("web/src/api/types.ts: %s is declared in no ```ts block of arch §7 "+
				"(declare it there, or list it in tsWithoutArch with the reason)", n)
		case declared[n] && excused:
			report("scripts/contractcheck: %s is listed in tsWithoutArch but arch §7 declares it; the exemption is stale", n)
		}
	}
	for n := range c.tsWithoutArch {
		if _, ok := ts.interfaces[n]; !ok {
			report("scripts/contractcheck: %s is listed in tsWithoutArch but types.ts has no such interface; the exemption is stale", n)
		}
	}

	// types.ts ↔ Go, responses.
	goOnlyUsed := map[string]bool{}
	for _, n := range reachableInterfaces(ts, c.responseRoots) {
		goName := "httpapi." + n
		if g, ok := c.tsToGo[n]; ok {
			goName = g
		}
		tf, _ := tsFieldSet(ts, n)
		gf, ok := gs[goName]
		if !ok {
			report("web/src/api/types.ts: response type %s has no Go struct %s (map it in tsToGo if the name differs)", n, goName)
			continue
		}
		where := fmt.Sprintf("types.ts %s / Go %s", n, goName)
		findings = append(findings, diffFieldSets(where, "types.ts", tf, "Go", gf, true, c.goOnly, goName)...)
		for f := range gf {
			if _, ok := c.goOnly[goName+"."+f]; ok && !tf.has(f) {
				goOnlyUsed[goName+"."+f] = true
			}
		}
	}
	for k := range c.goOnly {
		if !goOnlyUsed[k] {
			report("scripts/contractcheck: %s is listed in goOnly but no compared Go struct sends it as a Go-only field; the exemption is stale", k)
		}
	}

	// types.ts ↔ Go, request bodies: names only.
	for n, goName := range c.requestPairs {
		tf, ok := tsFieldSet(ts, n)
		if !ok {
			report("scripts/contractcheck: request body %s is not a types.ts interface", n)
			continue
		}
		gf, ok := gs[goName]
		if !ok {
			report("web/src/api/types.ts: request body %s has no Go struct %s", n, goName)
			continue
		}
		where := fmt.Sprintf("types.ts %s / Go %s (request body)", n, goName)
		findings = append(findings, diffFieldSets(where, "types.ts", tf, "Go", gf, false, nil, "")...)
	}
	sort.Strings(findings)
	return findings
}

// diffFieldSets names every field only one side has and, when withOptional,
// every field whose optionality differs. A b-side field listed in exempt as
// `exemptPrefix.field` is not reported.
func diffFieldSets(where, aName string, a fieldSet, bName string, b fieldSet, withOptional bool, exempt map[string]string, exemptPrefix string) []string {
	var out []string
	for f, aOpt := range a {
		bOpt, ok := b[f]
		switch {
		case !ok:
			out = append(out, fmt.Sprintf("%s: field %q is only in %s", where, f, aName))
		case withOptional && aOpt != bOpt:
			out = append(out, fmt.Sprintf("%s: field %q is %s in %s but %s in %s",
				where, f, optWord(aName, aOpt), aName, optWord(bName, bOpt), bName))
		}
	}
	for f := range b {
		if _, ok := a[f]; ok {
			continue
		}
		if _, ok := exempt[exemptPrefix+"."+f]; ok {
			continue
		}
		out = append(out, fmt.Sprintf("%s: field %q is only in %s", where, f, bName))
	}
	return out
}

func optWord(side string, opt bool) string {
	if side == "Go" {
		if opt {
			return "omitempty"
		}
		return "always sent (no omitempty)"
	}
	if opt {
		return "optional"
	}
	return "required"
}

// reachableInterfaces returns the roots and every interface their fields name,
// transitively, sorted.
func reachableInterfaces(ts *tsTypes, roots []string) []string {
	seen := map[string]bool{}
	var walk func(string)
	walk = func(n string) {
		fields, ok := ts.interfaces[n]
		if !ok || seen[n] {
			return
		}
		seen[n] = true
		for _, f := range fields {
			// Every name in the expression, not only the one typeRef keeps:
			// `A | B`, `Record<string, B>` and `Array<B>` all reach B.
			// String literals masked first: `kind: "Leaf"` is a value.
			expr, err := maskTSCode(f.expr)
			if err != nil {
				expr = f.expr
			}
			for _, id := range reTSIdent.FindAllString(expr, -1) {
				walk(id)
			}
		}
	}
	for _, r := range roots {
		walk(r)
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
