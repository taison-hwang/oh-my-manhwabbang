// Package config loads, defaults and validates `shelf.yaml` — the single
// runtime input SHELF needs besides its cache directory (NFR-OPS-002).
//
// The file format is arch-backend §3.2 as amended by impl-plan §0.3
// (A-1 thumbnail widths, A-2 reader fit mode, A-3 scan.include_globs), with the
// `auth:` block optional and absent by default (decisions E-8).
//
// Three rules shape everything here:
//
//   - Nothing is merged. The first file found in the lookup order of arch §3.1
//     is the whole configuration (Locate).
//   - Every key except roots[].name and roots[].path is optional, and the value
//     printed in shelf.example.yaml is the built-in default, so deleting a line
//     changes nothing. TestExampleFile_everyValueIsTheBuiltInDefault proves the
//     shipped example and the defaults in this package still agree.
//   - A bad configuration is fatal, reported once with every problem the file
//     has, and each message names the file, the line, the key and the value the
//     user must fix. Callers exit with ExitCode.
//
// Requirements: FR-CFG-001 (roots in YAML), FR-CFG-002 (per-root enable),
// FR-CFG-003 (cache dir / port / thumbnail sizes / worker counts),
// NFR-SEC-003 (base_path normalisation), NFR-OPS-002.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ExitCode is the process exit status for a configuration failure
// (impl-plan §3, WP-01 acceptance 3). Every error returned by Load and Parse is
// fatal: the caller prints it and exits with this status.
const ExitCode = 2

// FileName is the base name looked for in every directory of the lookup order.
const FileName = "shelf.yaml"

// Config is a whole `shelf.yaml`, defaulted and validated.
//
// Values are *resolved*: worker counts of 0 have become real numbers, the
// storage directories are absolute, `server.base_path` is normalised and
// `thumbnails.widths` is sorted and deduplicated. What a consumer reads here is
// what the server must do.
type Config struct {
	Server     Server     `yaml:"server"`
	Roots      []Root     `yaml:"roots"`
	Storage    Storage    `yaml:"storage"`
	Scan       Scan       `yaml:"scan"`
	Thumbnails Thumbnails `yaml:"thumbnails"`
	PDF        PDF        `yaml:"pdf"`
	Library    Library    `yaml:"library"`
	Reader     Reader     `yaml:"reader"`
	// Auth is nil when the file has no `auth:` block, which is the default and
	// means "no password" (NFR-SEC-002, decisions E-8).
	Auth *Auth `yaml:"auth"`
	Log  Log   `yaml:"log"`

	// FilePath is the configuration file this was read from. The settings
	// screen shows it (impl-plan C-5) and every error message is prefixed with
	// it. It is empty for a Config parsed from bytes with no name.
	FilePath string `yaml:"-"`

	// src carries the line of every key in the source file so that a validation
	// error can point at it. It is nil for a hand-built Config.
	src *source
}

// AbsFilePath is FilePath resolved against the working directory and cleaned:
// the file to name when telling a user which configuration to edit
// (`Settings.server.config_path`, amendment A-10 / ruling E-25).
//
// FilePath is whatever the lookup order found, and `./shelf.yaml` is entry 2 of
// that order — so a binary started from the directory holding the file records
// a bare `shelf.yaml`, which is the one answer that identifies no file. FilePath
// itself is deliberately left alone: it prefixes every message of this package's
// Error type, and those quote the path the user actually named.
//
// It returns "" for a Config parsed from bytes with no name, and the raw
// FilePath if the working directory cannot be read.
func (c *Config) AbsFilePath() string {
	if c.FilePath == "" {
		return ""
	}
	abs, err := filepath.Abs(c.FilePath)
	if err != nil {
		return c.FilePath
	}
	return abs
}

// Server is the `server:` block.
type Server struct {
	Listen string `yaml:"listen"`
	Port   int    `yaml:"port"`
	// BasePath mounts the whole application under a sub-path when it sits
	// behind a reverse proxy (NFR-SEC-003). It is normalised to either "" or a
	// "/prefix" with no trailing slash; ".." is rejected.
	BasePath          string        `yaml:"base_path"`
	ReadHeaderTimeout time.Duration `yaml:"read_header_timeout"`
	ShutdownGrace     time.Duration `yaml:"shutdown_grace"`
	// TrustedProxyHeaders honours X-Forwarded-Proto / X-Forwarded-For. Leaving
	// it false is what keeps a client from forging its own address and
	// defeating the login rate limiter.
	TrustedProxyHeaders bool `yaml:"trusted_proxy_headers"`
	// AllowRootEditing lets `POST /api/roots` and `DELETE /api/roots/{name}`
	// (arch §7.4) add and remove entries in the `roots:` list of this same file.
	// Amendment A-11, ruling E-26.
	//
	// It defaults to **false**, and that default is load-bearing rather than
	// conservative decoration. `listen` defaults to every interface and ruling
	// E-8 ships no `auth:` block, so the default deployment is an
	// unauthenticated listener on a LAN; an ungated write API there would let
	// anyone who can reach the port make this server open any directory it can
	// read, which §7.5 and §7.6 would then dutifully serve. Turning it on grants
	// no authority the person editing this file does not already hold — which is
	// exactly why the switch is a key in the file and not a control in the UI.
	//
	// There is nothing to validate about a bool, and `KnownFields(true)` already
	// rejects a typo in the key, so §3.2's startup-validation list is unchanged.
	AllowRootEditing bool `yaml:"allow_root_editing"`
}

// Root is one entry of the `roots:` list (FR-CFG-001).
//
// Name is an identity, not a label: every series_id and book_id hashes it
// (arch §3.4), so renaming a root orphans that root's reading progress. Label
// is the display name and is free to change.
type Root struct {
	Name string `yaml:"name"`
	Path string `yaml:"path"`
	// Enabled defaults to true. A disabled root stays in the index and is only
	// hidden from listings, so disabling never destroys progress (FR-CFG-002).
	Enabled bool   `yaml:"enabled"`
	Label   string `yaml:"label"`
}

// UnmarshalYAML decodes a root over the defaults rather than over the zero
// value, which is what makes `enabled` default to true when the key is absent.
func (r *Root) UnmarshalYAML(node *yaml.Node) error {
	type rootYAML Root
	tmp := rootYAML(defaultRoot())
	if err := node.Decode(&tmp); err != nil {
		return err
	}
	*r = Root(tmp)
	return nil
}

// DisplayName is Label when set and Name otherwise.
func (r Root) DisplayName() string {
	if r.Label != "" {
		return r.Label
	}
	return r.Name
}

// Storage is the `storage:` block: where SHELF keeps its own files. Both
// directories are absolute after loading and are never inside a media volume.
type Storage struct {
	// DataDir holds index.db, user.db and session.key.
	DataDir string `yaml:"data_dir"`
	// CacheDir holds thumbs/, pdf/ and wazero/ (FR-CFG-003). Deleting it while
	// the server runs is safe and costs latency only (FR-THM-007).
	CacheDir string `yaml:"cache_dir"`
}

// Scan is the `scan:` block (FR-IDX-*, FR-CFG-003).
type Scan struct {
	OnStart bool `yaml:"on_start"`
	// Workers is the number of archive readers. 0 in the file resolves to
	// min(8, max(2, NumCPU/2)).
	Workers int `yaml:"workers"`
	// MaxDepth is how deep below a series to look for books. 0 is unlimited.
	MaxDepth            int  `yaml:"max_depth"`
	FollowSymlinks      bool `yaml:"follow_symlinks"`
	CoverMaxLooseImages int  `yaml:"cover_max_loose_images"`
	// ExcludeGlobs are extra exclusions matched against the root-relative slash
	// path. The built-in rules of FR-IDX-006 are always applied on top.
	ExcludeGlobs []string `yaml:"exclude_globs"`
	// IncludeGlobs restricts scanning to the direct children of a root whose
	// base name matches at least one pattern (amendment A-3, decisions E-6).
	// Empty means everything.
	IncludeGlobs []string `yaml:"include_globs"`
}

// Thumbnails is the `thumbnails:` block (FR-THM-*, FR-CFG-003).
type Thumbnails struct {
	// Widths is sorted ascending and deduplicated; a request snaps UP to the
	// nearest entry. The default is amendment A-1.
	Widths  []int  `yaml:"widths"`
	Quality int    `yaml:"quality"`
	Format  string `yaml:"format"`
	// Workers is 0 in the file resolves to min(4, NumCPU).
	Workers        int   `yaml:"workers"`
	CoverFirst     bool  `yaml:"cover_first"`
	AVIFEnabled    bool  `yaml:"avif_enabled"`
	MaxSourceBytes int64 `yaml:"max_source_bytes"`
}

// PDF is the `pdf:` block (FR-SRV-006).
type PDF struct {
	Enabled      bool `yaml:"enabled"`
	Workers      int  `yaml:"workers"`
	DefaultWidth int  `yaml:"default_width"`
	MaxWidth     int  `yaml:"max_width"`
	CacheRenders bool `yaml:"cache_renders"`
}

// Library is the `library:` block: listing policy (amendment A-8, ruling E-9).
type Library struct {
	// RecentlyAddedDays is the window behind GET /api/series?scope=added and the
	// sidebar's 최근 추가 count: a series is recently added while
	// `now - first_seen_at <= RecentlyAddedDays * 86400`.
	//
	// 0 and negatives are a configuration error rather than "disabled". An empty
	// smart list with no explanation is worse than a wrong window, and the
	// sidebar entry itself is required (prd 5.2 UI-001).
	RecentlyAddedDays int `yaml:"recently_added_days"`
}

// RecentlyAddedCutoff is the unix second at which the recently-added window
// opens, given the current time. It is the one place the days-to-seconds
// arithmetic of arch §7.5 lives, so the listing filter and anything that
// explains the window to a user cannot disagree.
func (l Library) RecentlyAddedCutoff(now time.Time) int64 {
	return now.Unix() - int64(l.RecentlyAddedDays)*86400
}

// Reader is the `reader:` block: server-side defaults a reader may override per
// book at runtime (FR-VWR-002).
type Reader struct {
	Prefetch         int    `yaml:"prefetch"`
	ReadingDirection string `yaml:"reading_direction"`
	DisplayMode      string `yaml:"display_mode"`
	FitMode          string `yaml:"fit_mode"`
	Theme            string `yaml:"theme"`
}

// Auth is the optional `auth:` block (NFR-SEC-002). Its presence enables
// authentication; its absence is the default and means no password.
type Auth struct {
	// Password is plaintext, hashed with bcrypt at startup and never stored.
	Password string `yaml:"password"`
	// PasswordHash is a bcrypt hash from `shelf hash-password`. Setting both it
	// and Password is a configuration error.
	PasswordHash string        `yaml:"password_hash"`
	SessionTTL   time.Duration `yaml:"session_ttl"`
	// SessionKeyFile defaults to <data_dir>/session.key.
	SessionKeyFile string `yaml:"session_key_file"`
}

// UnmarshalYAML decodes the block over its defaults, so an `auth:` block that
// only sets a password still gets the documented 720h session TTL.
func (a *Auth) UnmarshalYAML(node *yaml.Node) error {
	type authYAML Auth
	tmp := authYAML(defaultAuth())
	if err := node.Decode(&tmp); err != nil {
		return err
	}
	*a = Auth(tmp)
	return nil
}

// Log is the `log:` block (NFR-OPS-005).
type Log struct {
	Level        string `yaml:"level"`
	Format       string `yaml:"format"`
	HTTPRequests bool   `yaml:"http_requests"`
}

// AuthEnabled reports whether a password is required (NFR-SEC-002).
func (c *Config) AuthEnabled() bool { return c.Auth != nil }

// EnabledRoots returns the roots that take part in listings (FR-CFG-002).
// Disabled roots stay in c.Roots — and therefore in the index — on purpose.
func (c *Config) EnabledRoots() []Root {
	out := make([]Root, 0, len(c.Roots))
	for _, r := range c.Roots {
		if r.Enabled {
			out = append(out, r)
		}
	}
	return out
}

// RootByName returns the root with this name, enabled or not.
func (c *Config) RootByName(name string) (Root, bool) {
	for _, r := range c.Roots {
		if r.Name == name {
			return r, true
		}
	}
	return Root{}, false
}

// Options selects the configuration file (arch §3.1).
type Options struct {
	// ExplicitPath is --config. When set and missing, Load fails; the lookup
	// order is not consulted.
	ExplicitPath string
	// WorkDir is the directory ./shelf.yaml is looked for in. Empty means the
	// process working directory.
	WorkDir string
	// Getenv overrides environment lookup. Nil means os.Getenv. Supplying it
	// also redirects the home directory to the HOME (or USERPROFILE) it
	// returns, so a test never reads the developer's real one.
	Getenv func(string) string

	// goos and etcDir are test seams for the per-OS branches.
	goos   string
	etcDir string
}

func (o Options) environment() environment {
	env := osEnvironment()
	if o.Getenv != nil {
		env.getenv = o.Getenv
		env.homeDir = func() (string, error) {
			for _, k := range []string{"HOME", "USERPROFILE"} {
				if v := o.Getenv(k); v != "" {
					return v, nil
				}
			}
			return "", errors.New("$HOME is not set")
		}
	}
	if o.goos != "" {
		env.goos = o.goos
	}
	return env
}

func (o Options) systemDir() string {
	if o.etcDir != "" {
		return o.etcDir
	}
	return filepath.Join("/etc", appName)
}

// NotFoundError reports that no configuration file exists anywhere in the
// lookup order. The caller prints Searched, tells the user about
// `shelf --init-config`, which writes InitPath, and exits non-zero (arch §3.1).
type NotFoundError struct {
	Searched []string
	InitPath string
}

func (e *NotFoundError) Error() string {
	var b strings.Builder
	b.WriteString("no configuration file found; looked in:")
	for _, p := range e.Searched {
		b.WriteString("\n  ")
		b.WriteString(p)
	}
	if e.InitPath != "" {
		b.WriteString("\nrun `shelf --init-config` to write a starter file to ")
		b.WriteString(e.InitPath)
	}
	return b.String()
}

// Candidates lists the configuration paths in lookup order, first match wins.
// Entries whose environment variable is unset are omitted, so the list is
// exactly what Locate will try (arch §3.1).
func Candidates(opts Options) []string {
	env := opts.environment()
	var out []string
	if opts.ExplicitPath != "" {
		return []string{opts.ExplicitPath}
	}
	if p := env.getenv("SHELF_CONFIG"); p != "" {
		out = append(out, p)
	}
	out = append(out, filepath.Join(opts.WorkDir, FileName))
	if dir, err := env.configDir(); err == nil {
		out = append(out, filepath.Join(dir, FileName))
	}
	if env.goos != "windows" {
		out = append(out, filepath.Join(opts.systemDir(), FileName))
	}
	return out
}

// InitPath is where `shelf --init-config` writes a starter file: entry 4 of the
// lookup order, $XDG_CONFIG_HOME/shelf/shelf.yaml.
func InitPath(opts Options) (string, error) {
	dir, err := opts.environment().configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, FileName), nil
}

// Locate returns the first configuration file that exists, in lookup order.
// A missing --config file is fatal and never falls through to the next
// candidate. When nothing exists it returns a *NotFoundError.
func Locate(opts Options) (string, error) {
	if opts.ExplicitPath != "" {
		if err := statConfigFile(opts.ExplicitPath); err != nil {
			return "", &Error{Path: opts.ExplicitPath, Msg: fmt.Sprintf("%s (named by --config)", err)}
		}
		return opts.ExplicitPath, nil
	}
	candidates := Candidates(opts)
	for _, p := range candidates {
		if statConfigFile(p) == nil {
			return p, nil
		}
	}
	initPath, _ := InitPath(opts)
	return "", &NotFoundError{Searched: candidates, InitPath: initPath}
}

func statConfigFile(path string) error {
	fi, err := os.Stat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return errors.New("no such configuration file")
	case err != nil:
		return fmt.Errorf("cannot read the configuration file: %w", err)
	case fi.IsDir():
		return errors.New("is a directory, not a configuration file")
	}
	return nil
}

// Load finds, reads, defaults and validates the configuration, and makes sure
// the storage directories exist and are writable (NFR-OPS-002: a config file
// and a cache directory are the only things SHELF needs at runtime).
//
// Every error it returns is fatal and should exit with ExitCode.
func Load(opts Options) (*Config, error) {
	path, err := Locate(opts)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, &Error{Path: path, Msg: fmt.Sprintf("cannot read the configuration file: %v", err)}
	}
	cfg, err := parse(data, path, opts.environment())
	if err != nil {
		return nil, err
	}
	if err := cfg.checkFilesystem(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Parse defaults and validates a configuration held in memory. path is only
// used to make error messages point at a file; it is never read. Nothing on
// disk is created or required, which is what makes it usable in tests.
func Parse(data []byte, path string) (*Config, error) {
	return parse(data, path, osEnvironment())
}

func parse(data []byte, path string, env environment) (*Config, error) {
	src, err := newSource(data, path)
	if err != nil {
		return nil, err
	}
	cfg := defaults()
	cfg.FilePath = path
	cfg.src = src

	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil && !errors.Is(err, io.EOF) {
		return nil, src.fromYAMLError(err)
	}
	if err := cfg.resolve(env); err != nil {
		return nil, err
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// source remembers where every key was written so that a message can send the
// user to the exact line to edit.
type source struct {
	path   string
	lines  map[string]int        // dotted key path -> line of the key
	values map[string]*yaml.Node // dotted key path -> value node
	onLine map[int][]string      // line -> every key written on it, in file order
	schema *schema
}

func newSource(data []byte, path string) (*source, error) {
	s := &source{
		path:   path,
		lines:  map[string]int{},
		values: map[string]*yaml.Node{},
		onLine: map[int][]string{},
		schema: schemaOf(reflect.TypeOf(Config{})),
	}

	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return s, nil // an empty file is legal; validation will ask for roots
		}
		return nil, s.fromYAMLError(err)
	}
	var second yaml.Node
	if err := dec.Decode(&second); err == nil {
		return nil, Errors{{Path: path, Line: second.Line, Msg: "the configuration file must hold a single YAML document (found a second one after \"---\")"}}
	}

	body := &doc
	if body.Kind == yaml.DocumentNode && len(body.Content) > 0 {
		body = body.Content[0]
	}
	if body.Kind == 0 || body.Tag == "!!null" {
		return s, nil
	}
	if body.Kind != yaml.MappingNode {
		return nil, Errors{{Path: path, Line: body.Line, Msg: `the top level must be a mapping of keys, for example "server:"`}}
	}

	var errs Errors
	s.walk(body, s.schema, "", &errs)
	if len(errs) > 0 {
		return nil, sortErrors(errs)
	}
	return s, nil
}

// walk records the position of every key and reports the ones the schema does
// not know, which is how a typo becomes a message instead of a silent no-op.
func (s *source) walk(n *yaml.Node, sch *schema, prefix string, errs *Errors) {
	if n.Kind == yaml.AliasNode && n.Alias != nil {
		n = n.Alias
	}
	switch n.Kind {
	case yaml.MappingNode:
		if sch.keys == nil {
			return // a mapping where a scalar or a list belongs: the decoder reports it
		}
		seen := map[string]int{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			name := k.Value
			key := name
			if prefix != "" {
				key = prefix + "." + name
			}
			if line, dup := seen[name]; dup {
				*errs = append(*errs, &Error{
					Path: s.path, Line: k.Line, Key: key,
					Msg: fmt.Sprintf("duplicate key, already set on line %d", line),
				})
				continue
			}
			seen[name] = k.Line
			child, known := sch.field(name)
			if !known {
				*errs = append(*errs, &Error{
					Path: s.path, Line: k.Line, Key: key,
					Msg: unknownKeyMessage(name, sch.names()),
				})
				continue
			}
			s.record(key, k, v)
			if child != nil {
				s.walk(v, child, key, errs)
			}
		}
	case yaml.SequenceNode:
		if sch.elem == nil {
			return
		}
		for i, item := range n.Content {
			key := fmt.Sprintf("%s[%d]", prefix, i)
			s.lines[key] = item.Line // a sequence item has no key node of its own
			s.values[key] = item
			s.noteLine(item.Line, key)
			s.walk(item, sch.elem, key, errs)
		}
	}
}

func (s *source) record(key string, k, v *yaml.Node) {
	s.lines[key] = k.Line
	s.values[key] = v
	s.noteLine(k.Line, key)
	if v.Kind == yaml.ScalarNode {
		s.noteLine(v.Line, key)
	}
}

// noteLine records that a key occupies a line. A line can hold more than one
// key — a flow mapping such as `server: { listen: "0.0.0.0", port: 8080 }` is
// one line with three — so this keeps all of them rather than electing a
// winner; keyAt does the choosing, and is allowed to choose nobody.
func (s *source) noteLine(line int, key string) {
	if !slices.Contains(s.onLine[line], key) {
		s.onLine[line] = append(s.onLine[line], key)
	}
}

// line reports the line a key was written on. A key that is not in the file —
// "roots[0].path" when the entry has no path at all — falls back to the line of
// its nearest present ancestor, which is still where the user has to type.
func (s *source) line(key string) int {
	if s == nil {
		return 0
	}
	for k := key; k != ""; k = parentKey(k) {
		if l, ok := s.lines[k]; ok {
			return l
		}
	}
	return 0
}

func parentKey(k string) string {
	if i := strings.LastIndexByte(k, '.'); i > 0 {
		return k[:i]
	}
	if i := strings.LastIndexByte(k, '['); i > 0 {
		return k[:i]
	}
	return ""
}

// errf builds an error naming the file, the line and the key.
func (s *source) errf(key, format string, args ...any) *Error {
	e := &Error{Key: key, Line: s.line(key), Msg: fmt.Sprintf(format, args...)}
	if s != nil {
		e.Path = s.path
	}
	return e
}

// errValue is errf plus the offending value, which acceptance 3 of WP-01 asks
// every rejection to carry — except for the keys whose value is a secret, where
// impl-plan §5.1 wins and setValue drops it.
func (s *source) errValue(key string, value any, format string, args ...any) *Error {
	e := s.errf(key, format, args...)
	e.setValue(key, value)
	return e
}

// fromYAMLError turns yaml.v3's own errors into ours: it recovers the line the
// parser complained about and names the key that lives there.
func (s *source) fromYAMLError(err error) error {
	var typeErr *yaml.TypeError
	if errors.As(err, &typeErr) {
		out := make(Errors, 0, len(typeErr.Errors))
		for _, msg := range typeErr.Errors {
			out = append(out, s.decodeMessage(msg))
		}
		return sortErrors(out)
	}
	return sortErrors(Errors{s.decodeMessage(strings.TrimPrefix(err.Error(), "yaml: "))})
}

var durationType = reflect.TypeOf(time.Duration(0))

func (s *source) decodeMessage(msg string) *Error {
	e := &Error{Msg: strings.TrimPrefix(msg, "yaml: ")}
	if s != nil {
		e.Path = s.path
	}
	line, rest, ok := splitLinePrefix(e.Msg)
	if !ok {
		return e
	}
	e.Line, e.Msg = line, rest
	if s == nil {
		return e
	}
	key, named := s.keyAt(line, e.Msg)
	// yaml quotes the offending value in its own wording, so a password written
	// where yaml expected another type would travel into the message and from
	// there into the log. Redact on the strength of the line, not the resolved
	// key: which key on the line is the secret one is exactly the question that
	// may have no answer (impl-plan §5.1, "never log passwords").
	if lineHoldsSecret(s.onLine[line]) {
		e.Msg = redactUnmarshalValue(e.Msg)
	}
	if !named {
		return e
	}
	e.Key = key
	// A duration is the one type whose yaml-level message is genuinely unhelpful.
	if sch, found := s.schema.lookup(key); found && sch.typ == durationType {
		e.Msg = `not a duration; write it as a quoted string such as "10s", "1m30s" or "720h"`
		if node := s.values[key]; node != nil {
			e.setValue(key, node.Value)
		}
	}
	return e
}

// keyAt names the key a yaml decode error reported on this line belongs to.
//
// yaml.v3 reports a line and no column, and a line can hold several keys: the
// flow mappings of impl-plan §6.3's E2E configuration put four of them on one
// line. Taking the first key on the line blames a value that is very often
// perfectly correct — `server: { read_header_timeout: "10s", port: eighty }`
// then reads "read_header_timeout: not a duration (got \"10s\")", which is
// three separate lies. So:
//
//   - a key that merely encloses another candidate ("roots[0]" alongside
//     "roots[0].name") drops out; the deepest key is the one to edit;
//   - what remains is matched against the source tag and the source value that
//     yaml quotes in its own message, then against the Go type it names;
//   - if a choice still remains, no key is named at all. A message with a file
//     and a line and no key is thin; a message naming the wrong key is false.
func (s *source) keyAt(line int, msg string) (string, bool) {
	cands := deepestKeys(s.onLine[line])
	if len(cands) == 1 {
		return cands[0], true
	}
	if len(cands) == 0 {
		return "", false
	}
	tag, value, hasValue, target, ok := splitUnmarshalMessage(msg)
	if !ok {
		return "", false
	}
	byValue := make([]string, 0, len(cands))
	for _, k := range cands {
		n := s.values[k]
		if n != nil && n.Kind == yaml.AliasNode && n.Alias != nil {
			n = n.Alias
		}
		if n == nil || n.Tag != tag || (hasValue && n.Value != value) {
			continue
		}
		byValue = append(byValue, k)
	}
	if len(byValue) == 1 {
		return byValue[0], true
	}
	if len(byValue) == 0 {
		return "", false
	}
	// Siblings holding the same value: the Go type yaml was decoding into is
	// the last thing that can tell them apart.
	byType := byValue[:0:0]
	for _, k := range byValue {
		if sch, found := s.schema.lookup(k); found && sch.typ.String() == target {
			byType = append(byType, k)
		}
	}
	if len(byType) == 1 {
		return byType[0], true
	}
	return "", false
}

// deepestKeys drops every key that encloses another key in the same set, so
// that a flow mapping reports the scalar the user has to retype and not the
// block it sits in.
func deepestKeys(keys []string) []string {
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		enclosing := false
		for _, other := range keys {
			if enclosesKey(k, other) {
				enclosing = true
				break
			}
		}
		if !enclosing {
			out = append(out, k)
		}
	}
	return out
}

// enclosesKey reports whether parent contains child: "server" contains
// "server.port" and "roots[0]" contains "roots[0].name", but "scan.workers"
// does not contain "scan.workers_extra".
func enclosesKey(parent, child string) bool {
	if len(child) <= len(parent) || !strings.HasPrefix(child, parent) {
		return false
	}
	switch child[len(parent)] {
	case '.', '[':
		return true
	}
	return false
}

// splitUnmarshalMessage takes yaml.v3's own wording apart:
//
//	cannot unmarshal !!str `eighty` into int
//	cannot unmarshal !!map into []string
//
// so that the tag, the value and the target Go type can be matched against the
// candidate keys on a line. ok is false for every other kind of yaml error.
func splitUnmarshalMessage(msg string) (tag, value string, hasValue bool, target string, ok bool) {
	rest, found := strings.CutPrefix(msg, "cannot unmarshal ")
	if !found {
		return "", "", false, "", false
	}
	// A value may itself contain " into ", so split on the last occurrence.
	i := strings.LastIndex(rest, " into ")
	if i < 0 {
		return "", "", false, "", false
	}
	head, target := rest[:i], rest[i+len(" into "):]
	if !strings.HasPrefix(head, "!!") || target == "" {
		return "", "", false, "", false
	}
	tag, quoted, quotedOK := strings.Cut(head, " ")
	if !quotedOK {
		return tag, "", false, target, true // a collection: no value is quoted
	}
	if len(quoted) < 2 || quoted[0] != '`' || quoted[len(quoted)-1] != '`' {
		return "", "", false, "", false
	}
	return tag, quoted[1 : len(quoted)-1], true, target, true
}

// redactUnmarshalValue removes the source value from a yaml decode message,
// turning "cannot unmarshal !!int `1234` into string" into
// "cannot unmarshal !!int into string". Anything it does not understand is
// returned unchanged, so it can only ever be used where a value is a secret.
func redactUnmarshalValue(msg string) string {
	tag, _, hasValue, target, ok := splitUnmarshalMessage(msg)
	if !ok || !hasValue {
		return msg
	}
	return fmt.Sprintf("cannot unmarshal %s into %s", tag, target)
}

// splitLinePrefix pulls the "line 12: " prefix yaml.v3 puts on its messages.
func splitLinePrefix(msg string) (int, string, bool) {
	const prefix = "line "
	if !strings.HasPrefix(msg, prefix) {
		return 0, msg, false
	}
	rest := msg[len(prefix):]
	i := strings.Index(rest, ":")
	if i <= 0 {
		return 0, msg, false
	}
	n := 0
	for _, r := range rest[:i] {
		if r < '0' || r > '9' {
			return 0, msg, false
		}
		n = n*10 + int(r-'0')
	}
	return n, strings.TrimSpace(rest[i+1:]), true
}

// schema mirrors the Config type: which keys exist at each level, and what type
// each one has. It is what turns an unknown key into a message with a
// suggestion instead of a silently ignored line.
type schema struct {
	typ  reflect.Type
	keys map[string]*schema // nil means "anything goes here"
	elem *schema            // element schema for a sequence
}

func schemaOf(t reflect.Type) *schema {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	s := &schema{typ: t}
	switch t.Kind() {
	case reflect.Struct:
		s.keys = map[string]*schema{}
		for i := range t.NumField() {
			f := t.Field(i)
			if f.PkgPath != "" { // unexported
				continue
			}
			name := yamlFieldName(f)
			if name == "-" {
				continue
			}
			s.keys[name] = schemaOf(f.Type)
		}
	case reflect.Slice, reflect.Array:
		s.elem = schemaOf(t.Elem())
	}
	return s
}

func yamlFieldName(f reflect.StructField) string {
	tag, ok := f.Tag.Lookup("yaml")
	if !ok {
		return strings.ToLower(f.Name)
	}
	name, _, _ := strings.Cut(tag, ",")
	if name == "" {
		return strings.ToLower(f.Name)
	}
	return name
}

// field reports the schema of a child key. known is false only for a mapping
// whose keys are enumerated and does not contain this one.
func (s *schema) field(name string) (child *schema, known bool) {
	if s == nil || s.keys == nil {
		return nil, true
	}
	child, ok := s.keys[name]
	return child, ok
}

func (s *schema) names() []string {
	if s == nil || s.keys == nil {
		return nil
	}
	out := make([]string, 0, len(s.keys))
	for k := range s.keys {
		out = append(out, k)
	}
	return out
}

// lookup walks a dotted key path such as "server.port" or "roots[0].name".
func (s *schema) lookup(key string) (*schema, bool) {
	cur := s
	for _, part := range strings.Split(key, ".") {
		name, isIndexed := part, false
		if i := strings.IndexByte(part, '['); i >= 0 {
			name, isIndexed = part[:i], true
		}
		if cur == nil || cur.keys == nil {
			return nil, false
		}
		next, ok := cur.keys[name]
		if !ok {
			return nil, false
		}
		if isIndexed {
			next = next.elem
		}
		cur = next
	}
	if cur == nil {
		return nil, false
	}
	return cur, true
}
