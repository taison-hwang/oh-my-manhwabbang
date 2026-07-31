package config

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Error is one problem with the configuration, addressed to the person who has
// to fix it: which file, which line, which key, which value.
//
//	shelf.yaml:30: server.port: must be between 1 and 65535 (got 70000)
type Error struct {
	Path  string // configuration file
	Line  int    // 1-based line of the offending key, 0 when it is not in the file
	Key   string // dotted key path, e.g. "roots[1].name"
	Value string // the offending value, already rendered
	Msg   string
}

func (e *Error) Error() string {
	var b strings.Builder
	if e.Path != "" {
		b.WriteString(e.Path)
		if e.Line > 0 {
			b.WriteString(":")
			b.WriteString(strconv.Itoa(e.Line))
		}
		b.WriteString(": ")
	}
	if e.Key != "" {
		b.WriteString(e.Key)
		b.WriteString(": ")
	}
	b.WriteString(e.Msg)
	if e.Value != "" {
		b.WriteString(" (got ")
		b.WriteString(e.Value)
		b.WriteString(")")
	}
	return b.String()
}

// Errors is every problem found in one pass, ordered by line. A user with three
// mistakes in their file should learn about all three at once, not one per
// restart.
type Errors []*Error

func (es Errors) Error() string {
	switch len(es) {
	case 0:
		return "invalid configuration"
	case 1:
		return es[0].Error()
	}
	parts := make([]string, 0, len(es)+1)
	parts = append(parts, fmt.Sprintf("%d configuration errors:", len(es)))
	for _, e := range es {
		parts = append(parts, "  "+e.Error())
	}
	return strings.Join(parts, "\n")
}

// Unwrap lets errors.As find an individual *Error inside the batch.
func (es Errors) Unwrap() []error {
	out := make([]error, len(es))
	for i, e := range es {
		out[i] = e
	}
	return out
}

func sortErrors(es Errors) Errors {
	slices.SortStableFunc(es, func(a, b *Error) int {
		if d := cmp.Compare(a.Line, b.Line); d != 0 {
			return d
		}
		return cmp.Compare(a.Key, b.Key)
	})
	return es
}

// setValue attaches the offending value to an error. WP-01 acceptance 3 wants
// every rejection to carry the value the user wrote; impl-plan §5.1 forbids
// ever logging a password. For the two auth keys those rules collide and §5.1
// wins — the message keeps the key and the line, which is everything the user
// needs to find the value themselves, and the value stays out of the log.
func (e *Error) setValue(key string, v any) {
	if isSecretKey(key) {
		return
	}
	e.Value = renderValue(v)
}

// isSecretKey reports whether a key's value must never appear in a message.
//
// auth.password_hash is on the list because the only value that ever reaches a
// rejection there is one that does not look like a bcrypt hash — which is
// overwhelmingly a plaintext password typed into the line below the one that
// wanted it (the two keys are adjacent in shelf.example.yaml). `auth` itself is
// on it for the same reason one step up: `auth: hunter2` is a mapping written
// as a scalar, and the scalar is the password.
//
// auth.session_ttl and auth.session_key_file are deliberately absent: neither
// is a credential, and their values are what makes their messages useful.
func isSecretKey(key string) bool {
	switch key {
	case "auth", "auth.password", "auth.password_hash":
		return true
	}
	return false
}

func lineHoldsSecret(keys []string) bool {
	return slices.ContainsFunc(keys, isSecretKey)
}

func renderValue(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return strconv.Quote(t)
	case time.Duration:
		return strconv.Quote(t.String())
	default:
		return fmt.Sprint(v)
	}
}

// unknownKeyMessage names the key and, when it is close enough to a real one to
// be a typo, says so.
func unknownKeyMessage(name string, known []string) string {
	msg := fmt.Sprintf("unknown key %q", name)
	if best, ok := closestKey(name, known); ok {
		msg += fmt.Sprintf("; did you mean %q?", best)
	}
	return msg
}

func closestKey(name string, known []string) (string, bool) {
	slices.Sort(known) // deterministic on ties
	best, bestDist := "", 1<<30
	limit := max(1, min(3, len(name)/3))
	for _, k := range known {
		d := editDistance(strings.ToLower(name), strings.ToLower(k))
		if d < bestDist {
			best, bestDist = k, d
		}
	}
	return best, bestDist <= limit
}

// editDistance is optimal string alignment over runes: Levenshtein plus
// transposition, because "prot" for "port" is the typo people actually make.
// It runs a few hundred comparisons on a path nobody takes twice.
func editDistance(a, b string) int {
	ar, br := []rune(a), []rune(b)
	d := make([][]int, len(ar)+1)
	for i := range d {
		d[i] = make([]int, len(br)+1)
		d[i][0] = i
	}
	for j := range d[0] {
		d[0][j] = j
	}
	for i := 1; i <= len(ar); i++ {
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			d[i][j] = min(d[i-1][j]+1, d[i][j-1]+1, d[i-1][j-1]+cost)
			if i > 1 && j > 1 && ar[i-1] == br[j-2] && ar[i-2] == br[j-1] {
				d[i][j] = min(d[i][j], d[i-2][j-2]+1)
			}
		}
	}
	return d[len(ar)][len(br)]
}

// rootNameRe is the identity alphabet of arch §3.2. It is deliberately narrow:
// a root name is hashed into every series_id and book_id (arch §3.4) and shows
// up in URLs and log lines.
var rootNameRe = regexp.MustCompile(`^[a-zA-Z0-9._-]{1,64}$`)

// ValidRootName reports whether s is a well-formed `roots[].name`.
//
// It is exported for amendment A-11: `DELETE /api/roots/{name}` has to split a
// syntactically invalid name (`400`) from a well-formed one that names nothing
// (`404`), exactly as arch §7.1 splits a malformed id from an unknown one — and
// it has to do it with the same alphabet startup validation enforces. Two
// implementations of one rule is how they diverge.
func ValidRootName(s string) bool { return rootNameRe.MatchString(s) }

// IsInside reports whether path is dir itself or lives under it. Both must
// already be absolute and cleaned.
//
// Exported for amendment A-11, which needs three of the rules this helper
// already enforces at startup, applied to a root that does not exist yet:
// `storage.data_dir`/`cache_dir` must not fall inside the new root
// (FR-CFG-005), the new root must not be an ancestor or a descendant of a
// configured one (§7.4's `overlaps`), and the configuration file itself must not
// sit inside a media root (§7.4's `config_inside_root`). The HTTP layer must
// apply the same test the startup guard applies, from the same code.
func IsInside(path, dir string) bool { return isInside(path, dir) }

// validate applies every rejection of arch §3.2 plus the enum and range checks
// the schema implies. It touches nothing on disk — checkFilesystem does that —
// and it collects every problem before returning.
func (c *Config) validate() error {
	s := c.src
	var errs Errors
	add := func(e *Error) { errs = append(errs, e) }

	// ---- server ----------------------------------------------------------
	if c.Server.Listen == "" {
		add(s.errf("server.listen", `must not be empty; use "0.0.0.0" to bind every interface`))
	}
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		add(s.errValue("server.port", c.Server.Port, "must be between 1 and 65535"))
	}
	if normalized, err := normalizeBasePath(c.Server.BasePath); err != nil {
		add(s.errValue("server.base_path", c.Server.BasePath, "%s", err))
	} else {
		c.Server.BasePath = normalized
	}
	if c.Server.ReadHeaderTimeout <= 0 {
		add(s.errValue("server.read_header_timeout", c.Server.ReadHeaderTimeout, "must be a positive duration"))
	}
	if c.Server.ShutdownGrace < 0 {
		add(s.errValue("server.shutdown_grace", c.Server.ShutdownGrace, "must not be negative"))
	}

	// ---- roots (FR-CFG-001) ----------------------------------------------
	if len(c.Roots) == 0 {
		add(s.errf("roots", "at least one root directory is required; add a `roots:` entry with a name and an absolute path"))
	}
	seenName := map[string]int{}
	for i, r := range c.Roots {
		key := fmt.Sprintf("roots[%d]", i)
		switch {
		case r.Name == "":
			add(s.errf(key+".name", "is required and is the stable identity of this root"))
		case !rootNameRe.MatchString(r.Name):
			add(s.errValue(key+".name", r.Name, "must be 1 to 64 characters of [a-zA-Z0-9._-]"))
		}
		if first, dup := seenName[r.Name]; dup && r.Name != "" {
			add(s.errValue(key+".name", r.Name, "duplicate root name, already used by roots[%d]", first))
		} else if r.Name != "" {
			seenName[r.Name] = i
		}
		switch {
		case r.Path == "":
			add(s.errf(key+".path", "is required and must be the absolute path of an existing directory"))
		case !filepath.IsAbs(r.Path):
			add(s.errValue(key+".path", r.Path, "must be an absolute path"))
		}
	}

	// ---- storage vs roots (FR-CFG-005 / NFR-DAT-002) ---------------------
	// SHELF never writes to a media volume. index.db and the thumbnail cache
	// are the only files it creates, so the one way a media volume could be
	// written to is a storage directory pointed inside a root. Refuse at
	// startup rather than discover it after the first thumbnail.
	for i, r := range c.Roots {
		if r.Path == "" || !filepath.IsAbs(r.Path) {
			continue // already reported above
		}
		for _, d := range []struct{ key, dir string }{
			{"storage.data_dir", c.Storage.DataDir},
			{"storage.cache_dir", c.Storage.CacheDir},
		} {
			if isInside(d.dir, r.Path) {
				add(s.errValue(d.key, d.dir,
					"must not be inside roots[%d].path (%s); SHELF never writes to a media volume", i, r.Path))
			}
		}
	}

	// ---- scan (FR-CFG-003, A-3) ------------------------------------------
	if c.Scan.Workers < 1 {
		add(s.errValue("scan.workers", c.Scan.Workers, "must be 0 (choose automatically) or a positive number"))
	}
	if c.Scan.MaxDepth < 0 {
		add(s.errValue("scan.max_depth", c.Scan.MaxDepth, "must be 0 (unlimited) or a positive number"))
	}
	if c.Scan.CoverMaxLooseImages < 0 {
		add(s.errValue("scan.cover_max_loose_images", c.Scan.CoverMaxLooseImages, "must not be negative"))
	}
	checkGlobs(s, "scan.exclude_globs", c.Scan.ExcludeGlobs, add)
	checkGlobs(s, "scan.include_globs", c.Scan.IncludeGlobs, add)

	// ---- thumbnails (FR-CFG-003, A-1) ------------------------------------
	if len(c.Thumbnails.Widths) == 0 {
		add(s.errf("thumbnails.widths", "must list at least one width, for example [120, 240, 400, 640]"))
	}
	for _, w := range c.Thumbnails.Widths {
		if w < 32 || w > 2048 {
			add(s.errValue("thumbnails.widths", w, "every width must be between 32 and 2048"))
		}
	}
	if c.Thumbnails.Quality < 1 || c.Thumbnails.Quality > 100 {
		add(s.errValue("thumbnails.quality", c.Thumbnails.Quality, "must be between 1 and 100"))
	}
	if c.Thumbnails.Format != defaultFormat {
		add(s.errValue("thumbnails.format", c.Thumbnails.Format, `must be "jpeg"; it is the only thumbnail format in v1 (CON-003)`))
	}
	if c.Thumbnails.Workers < 1 {
		add(s.errValue("thumbnails.workers", c.Thumbnails.Workers, "must be 0 (choose automatically) or a positive number"))
	}
	if c.Thumbnails.MaxSourceBytes < 1 {
		add(s.errValue("thumbnails.max_source_bytes", c.Thumbnails.MaxSourceBytes, "must be a positive number of bytes"))
	}

	// ---- pdf (FR-SRV-006) ------------------------------------------------
	if c.PDF.Workers < 1 {
		add(s.errValue("pdf.workers", c.PDF.Workers, "must be at least 1; each worker is a pdfium instance"))
	}
	if c.PDF.MaxWidth < 100 || c.PDF.MaxWidth > 10000 {
		add(s.errValue("pdf.max_width", c.PDF.MaxWidth, "must be between 100 and 10000"))
	}
	if c.PDF.DefaultWidth < 100 {
		add(s.errValue("pdf.default_width", c.PDF.DefaultWidth, "must be at least 100"))
	} else if c.PDF.DefaultWidth > c.PDF.MaxWidth {
		add(s.errValue("pdf.default_width", c.PDF.DefaultWidth, "must not exceed pdf.max_width (%d)", c.PDF.MaxWidth))
	}

	// ---- library (A-8) ---------------------------------------------------
	// 3650 days is ten years: past it the window covers any collection anyone
	// has, so a larger number is a typo rather than an intention.
	if c.Library.RecentlyAddedDays < 1 || c.Library.RecentlyAddedDays > 3650 {
		add(s.errValue("library.recently_added_days", c.Library.RecentlyAddedDays,
			`must be between 1 and 3650 days; there is no "disabled" value, because the 최근 추가 sidebar entry is always shown`))
	}

	// ---- reader (A-2) ----------------------------------------------------
	if c.Reader.Prefetch < 0 || c.Reader.Prefetch > 20 {
		add(s.errValue("reader.prefetch", c.Reader.Prefetch, "must be between 0 and 20"))
	}
	checkEnum(s, "reader.reading_direction", c.Reader.ReadingDirection, add, "ltr", "rtl")
	checkEnum(s, "reader.display_mode", c.Reader.DisplayMode, add, "single", "spread", "vertical")
	checkEnum(s, "reader.fit_mode", c.Reader.FitMode, add, "width", "height", "original", "contain")
	checkEnum(s, "reader.theme", c.Reader.Theme, add, "light", "dark", "system")

	// ---- auth (NFR-SEC-002) ----------------------------------------------
	if a := c.Auth; a != nil {
		switch {
		case a.Password != "" && a.PasswordHash != "":
			add(s.errf("auth.password", "set either auth.password or auth.password_hash, not both"))
		case a.Password == "" && a.PasswordHash == "":
			add(s.errf("auth.password", "the auth: block is present but neither auth.password nor auth.password_hash is set; set one, or delete the block to run without a password"))
		}
		if a.PasswordHash != "" && !strings.HasPrefix(a.PasswordHash, "$2") {
			// errValue, not errf: the call site stays uniform and setValue is
			// the one place that decides a value is too sensitive to print.
			add(s.errValue("auth.password_hash", a.PasswordHash,
				"is not a bcrypt hash — one starts with \"$2\"; generate it with `shelf hash-password`, "+
					"or put a plaintext password in auth.password instead (the value is not printed here in case it is one)"))
		}
		if a.SessionTTL <= 0 {
			add(s.errValue("auth.session_ttl", a.SessionTTL, "must be a positive duration"))
		}
	}

	// ---- log (NFR-OPS-005) -----------------------------------------------
	checkEnum(s, "log.level", c.Log.Level, add, "debug", "info", "warn", "error")
	checkEnum(s, "log.format", c.Log.Format, add, "text", "json")

	if len(errs) == 0 {
		return nil
	}
	return sortErrors(errs)
}

func checkEnum(s *source, key, value string, add func(*Error), allowed ...string) {
	if slices.Contains(allowed, value) {
		return
	}
	add(s.errValue(key, value, "must be one of %s", strings.Join(quoteAll(allowed), ", ")))
}

func quoteAll(vs []string) []string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = strconv.Quote(v)
	}
	return out
}

// checkGlobs rejects patterns path.Match cannot compile. `[` and `]` are
// character-class metacharacters, so a literal Korean directory name such as
// "[만화] 군계 1~25" has to escape the opening bracket as "[[]만화] 군계 1~25"
// to match literally — but both forms compile, and only a genuinely broken
// pattern (an unterminated class) is an error.
func checkGlobs(s *source, key string, globs []string, add func(*Error)) {
	for i, g := range globs {
		itemKey := fmt.Sprintf("%s[%d]", key, i)
		if g == "" {
			add(s.errf(itemKey, "must not be an empty pattern"))
			continue
		}
		if _, err := path.Match(g, "probe"); err != nil {
			e := s.errValue(itemKey, g,
				`is not a valid pattern: %s; "[" and "]" start a character class, so a literal bracket has to be escaped as "[[]"`, err)
			if e.Line == 0 { // sequence items have no key node of their own
				e.Line = s.line(key)
			}
			add(e)
		}
	}
}

// isInside reports whether path is dir itself or lives under it. Both are
// absolute and cleaned by the time this runs. It is a textual test — a startup
// guard rail, not the path-traversal defence, which is four other layers.
func isInside(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// checkFilesystem is the half of validation that has to look at the disk: the
// roots must exist, and our own two directories must exist and be writable.
// It runs only from Load, which keeps Parse hermetic.
func (c *Config) checkFilesystem() error {
	s := c.src
	var errs Errors

	for i, r := range c.Roots {
		if r.Path == "" || !filepath.IsAbs(r.Path) {
			continue // already reported by validate
		}
		key := fmt.Sprintf("roots[%d].path", i)
		fi, err := os.Stat(r.Path)
		switch {
		case errors.Is(err, os.ErrNotExist):
			errs = append(errs, s.errValue(key, r.Path, "no such directory"))
		case err != nil:
			errs = append(errs, s.errValue(key, r.Path, "cannot be read: %v", err))
		case !fi.IsDir():
			errs = append(errs, s.errValue(key, r.Path, "is not a directory"))
		}
	}

	for _, d := range []struct {
		key, dir string
	}{
		{"storage.data_dir", c.Storage.DataDir},
		{"storage.cache_dir", c.Storage.CacheDir},
	} {
		if err := ensureDir(d.dir); err != nil {
			errs = append(errs, s.errValue(d.key, d.dir, "%s", err))
		}
	}

	if len(errs) == 0 {
		return nil
	}
	return sortErrors(errs)
}
