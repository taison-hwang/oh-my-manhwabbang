package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// examplePath is the reference instance every implementer's loader has to
// parse: the fully commented file shipped at the repository root.
const examplePath = "../../shelf.example.yaml"

// minimalYAML is the smallest legal configuration — only the two required keys.
// Everything else in this package has to come out of it as a documented default.
const minimalYAML = `roots:
  - name: "manga"
    path: "/mnt/media/manga"
`

func parseYAML(t *testing.T, body string) *Config {
	t.Helper()
	cfg, err := Parse([]byte(body), "shelf.yaml")
	if err != nil {
		t.Fatalf("Parse: unexpected error:\n%v", err)
	}
	return cfg
}

func parseYAMLErr(t *testing.T, body string) error {
	t.Helper()
	cfg, err := Parse([]byte(body), "shelf.yaml")
	if err == nil {
		t.Fatalf("Parse: expected an error, got a valid configuration: %+v", cfg)
	}
	return err
}

// withRoots appends the required roots block so that a table row only has to
// carry the fragment it is actually testing.
func withRoots(body string) string { return body + "\n" + minimalYAML }

// ---------------------------------------------------------------------------
// Defaults — FR-CFG-003, amendments A-1, A-2, A-3
// ---------------------------------------------------------------------------

func TestParse_minimalFile_appliesEveryDocumentedDefault(t *testing.T) {
	t.Parallel()
	cfg := parseYAML(t, minimalYAML)

	if got, want := cfg.Server.Listen, "0.0.0.0"; got != want {
		t.Errorf("server.listen = %q, want %q", got, want)
	}
	if got, want := cfg.Server.Port, 8080; got != want {
		t.Errorf("server.port = %d, want %d", got, want)
	}
	if got, want := cfg.Server.BasePath, ""; got != want {
		t.Errorf("server.base_path = %q, want %q", got, want)
	}
	if got, want := cfg.Server.ReadHeaderTimeout, 10*time.Second; got != want {
		t.Errorf("server.read_header_timeout = %v, want %v", got, want)
	}
	if got, want := cfg.Server.ShutdownGrace, 10*time.Second; got != want {
		t.Errorf("server.shutdown_grace = %v, want %v", got, want)
	}
	if cfg.Server.TrustedProxyHeaders {
		t.Error("server.trusted_proxy_headers = true, want false")
	}

	// FR-CFG-002: a root with no `enabled` key is enabled.
	if len(cfg.Roots) != 1 || !cfg.Roots[0].Enabled {
		t.Errorf("roots = %+v, want one enabled root", cfg.Roots)
	}
	if got, want := cfg.Roots[0].DisplayName(), "manga"; got != want {
		t.Errorf("DisplayName with no label = %q, want %q", got, want)
	}

	if !cfg.Scan.OnStart {
		t.Error("scan.on_start = false, want true")
	}
	if got, want := cfg.Scan.Workers, scanWorkersFor(runtime.NumCPU()); got != want {
		t.Errorf("scan.workers = %d, want min(8, max(2, NumCPU/2)) = %d", got, want)
	}
	if got, want := cfg.Scan.MaxDepth, 3; got != want {
		t.Errorf("scan.max_depth = %d, want %d", got, want)
	}
	if cfg.Scan.FollowSymlinks {
		t.Error("scan.follow_symlinks = true, want false")
	}
	if got, want := cfg.Scan.CoverMaxLooseImages, 3; got != want {
		t.Errorf("scan.cover_max_loose_images = %d, want %d (D-5)", got, want)
	}
	if len(cfg.Scan.ExcludeGlobs) != 0 {
		t.Errorf("scan.exclude_globs = %v, want empty", cfg.Scan.ExcludeGlobs)
	}
	// A-3 / decisions E-6.
	if len(cfg.Scan.IncludeGlobs) != 0 {
		t.Errorf("scan.include_globs = %v, want empty (means: scan everything)", cfg.Scan.IncludeGlobs)
	}

	// A-1.
	if got, want := cfg.Thumbnails.Widths, []int{120, 240, 400, 640}; !slices.Equal(got, want) {
		t.Errorf("thumbnails.widths = %v, want %v (amendment A-1)", got, want)
	}
	if got, want := cfg.Thumbnails.Quality, 82; got != want {
		t.Errorf("thumbnails.quality = %d, want %d", got, want)
	}
	if got, want := cfg.Thumbnails.Format, "jpeg"; got != want {
		t.Errorf("thumbnails.format = %q, want %q", got, want)
	}
	if got, want := cfg.Thumbnails.Workers, thumbWorkersFor(runtime.NumCPU()); got != want {
		t.Errorf("thumbnails.workers = %d, want min(4, NumCPU) = %d", got, want)
	}
	if !cfg.Thumbnails.CoverFirst {
		t.Error("thumbnails.cover_first = false, want true")
	}
	if !cfg.Thumbnails.AVIFEnabled {
		t.Error("thumbnails.avif_enabled = false, want true")
	}
	if got, want := cfg.Thumbnails.MaxSourceBytes, int64(67108864); got != want {
		t.Errorf("thumbnails.max_source_bytes = %d, want %d", got, want)
	}

	if !cfg.PDF.Enabled {
		t.Error("pdf.enabled = false, want true")
	}
	if got, want := cfg.PDF.Workers, 1; got != want {
		t.Errorf("pdf.workers = %d, want %d", got, want)
	}
	if got, want := cfg.PDF.DefaultWidth, 1400; got != want {
		t.Errorf("pdf.default_width = %d, want %d", got, want)
	}
	if got, want := cfg.PDF.MaxWidth, 3000; got != want {
		t.Errorf("pdf.max_width = %d, want %d", got, want)
	}
	if !cfg.PDF.CacheRenders {
		t.Error("pdf.cache_renders = false, want true")
	}

	// A-8 (ruling E-9) — the 최근 추가 window.
	if got, want := cfg.Library.RecentlyAddedDays, 14; got != want {
		t.Errorf("library.recently_added_days = %d, want %d (amendment A-8)", got, want)
	}

	if got, want := cfg.Reader.Prefetch, 4; got != want {
		t.Errorf("reader.prefetch = %d, want %d", got, want)
	}
	if got, want := cfg.Reader.ReadingDirection, "ltr"; got != want {
		t.Errorf("reader.reading_direction = %q, want %q", got, want)
	}
	if got, want := cfg.Reader.DisplayMode, "single"; got != want {
		t.Errorf("reader.display_mode = %q, want %q", got, want)
	}
	// A-2 — the arch default was "contain"; the prototype capture says "height".
	if got, want := cfg.Reader.FitMode, "height"; got != want {
		t.Errorf("reader.fit_mode = %q, want %q (amendment A-2)", got, want)
	}
	if got, want := cfg.Reader.Theme, "system"; got != want {
		t.Errorf("reader.theme = %q, want %q", got, want)
	}

	// decisions E-8: no `auth:` block means no password.
	if cfg.Auth != nil {
		t.Errorf("auth = %+v, want nil (absent auth block = authentication disabled)", cfg.Auth)
	}
	if cfg.AuthEnabled() {
		t.Error("AuthEnabled() = true with no auth block, want false")
	}

	if got, want := cfg.Log.Level, "info"; got != want {
		t.Errorf("log.level = %q, want %q", got, want)
	}
	if got, want := cfg.Log.Format, "text"; got != want {
		t.Errorf("log.format = %q, want %q", got, want)
	}
	if !cfg.Log.HTTPRequests {
		t.Error("log.http_requests = false, want true")
	}

	if !filepath.IsAbs(cfg.Storage.DataDir) || !filepath.IsAbs(cfg.Storage.CacheDir) {
		t.Errorf("storage dirs must be absolute, got %q and %q", cfg.Storage.DataDir, cfg.Storage.CacheDir)
	}
}

func TestWorkerDefaults_resolveFromNumCPU(t *testing.T) {
	t.Parallel()
	cases := []struct {
		numCPU, wantScan, wantThumb int
	}{
		{1, 2, 1},
		{2, 2, 2},
		{4, 2, 4},
		{8, 4, 4},
		{16, 8, 4},
		{64, 8, 4},
	}
	for _, c := range cases {
		if got := scanWorkersFor(c.numCPU); got != c.wantScan {
			t.Errorf("scanWorkersFor(%d) = %d, want %d", c.numCPU, got, c.wantScan)
		}
		if got := thumbWorkersFor(c.numCPU); got != c.wantThumb {
			t.Errorf("thumbWorkersFor(%d) = %d, want %d", c.numCPU, got, c.wantThumb)
		}
	}
}

func TestParse_explicitWorkerCounts_areKept(t *testing.T) {
	t.Parallel()
	cfg := parseYAML(t, withRoots("scan: {workers: 16}\nthumbnails: {workers: 3}"))
	if cfg.Scan.Workers != 16 || cfg.Thumbnails.Workers != 3 {
		t.Errorf("workers = %d/%d, want 16/3", cfg.Scan.Workers, cfg.Thumbnails.Workers)
	}
}

// Amendment A-8 (ruling E-9): the 최근 추가 window, and the one place its
// days-to-seconds arithmetic lives.
func TestParse_recentlyAddedDays_isKeptAndBecomesACutoff(t *testing.T) {
	t.Parallel()
	cfg := parseYAML(t, withRoots("library: {recently_added_days: 30}"))
	if got, want := cfg.Library.RecentlyAddedDays, 30; got != want {
		t.Errorf("library.recently_added_days = %d, want %d", got, want)
	}

	now := time.Unix(1_700_000_000, 0).UTC()
	if got, want := cfg.Library.RecentlyAddedCutoff(now), int64(1_700_000_000-30*86400); got != want {
		t.Errorf("RecentlyAddedCutoff = %d, want %d", got, want)
	}
	// The shipped default, spelled out: a series first seen exactly 14 days ago
	// is still inside the window, because the comparison is `>=` (arch §7.5).
	def := parseYAML(t, minimalYAML)
	if got, want := def.Library.RecentlyAddedCutoff(now), int64(1_700_000_000-14*86400); got != want {
		t.Errorf("default cutoff = %d, want %d", got, want)
	}
	// The bounds are usable, not just legal.
	for _, days := range []int{1, 3650} {
		c := parseYAML(t, withRoots(fmt.Sprintf("library: {recently_added_days: %d}", days)))
		if got, want := c.Library.RecentlyAddedCutoff(now), now.Unix()-int64(days)*86400; got != want {
			t.Errorf("%d-day cutoff = %d, want %d", days, got, want)
		}
	}
}

func TestParse_thumbnailWidths_areSortedAndDeduplicated(t *testing.T) {
	t.Parallel()
	cfg := parseYAML(t, withRoots("thumbnails: {widths: [640, 120, 240, 120, 400]}"))
	if got, want := cfg.Thumbnails.Widths, []int{120, 240, 400, 640}; !slices.Equal(got, want) {
		t.Errorf("widths = %v, want %v", got, want)
	}
}

// ---------------------------------------------------------------------------
// shelf.example.yaml — the shipped reference instance
// ---------------------------------------------------------------------------

func readExample(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(examplePath)
	if err != nil {
		t.Fatalf("reading %s: %v", examplePath, err)
	}
	return string(data)
}

func TestExampleFile_parses(t *testing.T) {
	t.Parallel()
	cfg, err := Parse([]byte(readExample(t)), examplePath)
	if err != nil {
		t.Fatalf("the shipped shelf.example.yaml must parse:\n%v", err)
	}
	want := []Root{{Name: "manga", Path: "/mnt/media/manga", Enabled: true, Label: "만화"}}
	if !reflect.DeepEqual(cfg.Roots, want) {
		t.Errorf("roots = %+v, want %+v", cfg.Roots, want)
	}
	if got, want := cfg.Roots[0].DisplayName(), "만화"; got != want {
		t.Errorf("DisplayName = %q, want the label %q", got, want)
	}
	if cfg.Auth != nil {
		t.Error("the example ships its auth: block commented out, so auth must be disabled (decisions E-8)")
	}
	if got, want := cfg.Thumbnails.Widths, []int{120, 240, 400, 640}; !slices.Equal(got, want) {
		t.Errorf("widths = %v, want %v (A-1)", got, want)
	}
	if got, want := cfg.Reader.FitMode, "height"; got != want {
		t.Errorf("fit_mode = %q, want %q (A-2)", got, want)
	}
	if cfg.Scan.IncludeGlobs == nil || len(cfg.Scan.IncludeGlobs) != 0 {
		t.Errorf("include_globs = %v, want an empty list (A-3 / E-6)", cfg.Scan.IncludeGlobs)
	}
}

// TestExampleFile_everyValueIsTheBuiltInDefault holds the example file to the
// promise printed in its own header: "the value shown is the built-in default,
// so deleting a line changes nothing". Parsing the whole file and parsing only
// its roots block must produce the same configuration.
func TestExampleFile_everyValueIsTheBuiltInDefault(t *testing.T) {
	t.Parallel()
	fromExample, err := Parse([]byte(readExample(t)), examplePath)
	if err != nil {
		t.Fatalf("parsing the example: %v", err)
	}
	fromDefaults := parseYAML(t, minimalYAML)

	// The roots and the provenance are the only things allowed to differ.
	fromExample.Roots, fromDefaults.Roots = nil, nil
	fromExample.FilePath, fromDefaults.FilePath = "", ""
	fromExample.src, fromDefaults.src = nil, nil

	if !reflect.DeepEqual(fromExample, fromDefaults) {
		t.Errorf("shelf.example.yaml no longer states the built-in defaults\n example:  %+v\n defaults: %+v", fromExample, fromDefaults)
	}
}

// stripComment removes the comment marker from a wholly commented-out line,
// preserving its indentation: "  #   path: x" -> "    path: x".
func stripComment(line string) (string, bool) {
	i := strings.IndexByte(line, '#')
	if i < 0 || strings.TrimSpace(line[:i]) != "" {
		return line, false
	}
	return line[:i] + strings.TrimPrefix(line[i+1:], " "), true
}

// uncommentBlock uncomments the line whose content is head and every commented
// line that follows it, stopping at the first line that is not a comment.
func uncommentBlock(t *testing.T, src, head string) string {
	t.Helper()
	lines := strings.Split(src, "\n")
	start := -1
	for i, l := range lines {
		if s, ok := stripComment(l); ok && strings.TrimSpace(s) == head {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("no commented-out line %q in %s", head, examplePath)
	}
	out := slices.Clone(lines)
	for i := start; i < len(lines); i++ {
		s, ok := stripComment(lines[i])
		if !ok {
			break
		}
		out[i] = s
	}
	return strings.Join(out, "\n")
}

func TestExampleFile_uncommentedSecondRoot_parses(t *testing.T) {
	t.Parallel()
	src := uncommentBlock(t, readExample(t), `- name: "books"`)
	cfg, err := Parse([]byte(src), examplePath)
	if err != nil {
		t.Fatalf("the commented-out second root must parse once uncommented:\n%v", err)
	}
	want := []Root{
		{Name: "manga", Path: "/mnt/media/manga", Enabled: true, Label: "만화"},
		{Name: "books", Path: "/mnt/media/books", Enabled: true, Label: "도서"},
	}
	if !reflect.DeepEqual(cfg.Roots, want) {
		t.Errorf("roots = %+v, want %+v", cfg.Roots, want)
	}
}

func TestExampleFile_uncommentedAuthBlock(t *testing.T) {
	t.Parallel()
	src := uncommentBlock(t, readExample(t), "auth:")

	t.Run("with a hash it enables auth with the documented defaults", func(t *testing.T) {
		t.Parallel()
		const hash = `$2a$12$C6UzMDM.H6dfI/f/IKcEe.q7d5b6q4b1H0mYJ2XcQ1yGkQ7bA0e6a`
		withHash := strings.Replace(src, `password_hash: ""`, `password_hash: "`+hash+`"`, 1)
		cfg, err := Parse([]byte(withHash), examplePath)
		if err != nil {
			t.Fatalf("uncommented auth block with a hash must parse:\n%v", err)
		}
		if !cfg.AuthEnabled() {
			t.Fatal("AuthEnabled() = false, want true")
		}
		if got, want := cfg.Auth.SessionTTL, 720*time.Hour; got != want {
			t.Errorf("auth.session_ttl = %v, want %v", got, want)
		}
		if got, want := cfg.Auth.PasswordHash, hash; got != want {
			t.Errorf("auth.password_hash = %q, want %q", got, want)
		}
		if want := filepath.Join(cfg.Storage.DataDir, "session.key"); cfg.Auth.SessionKeyFile != want {
			t.Errorf("auth.session_key_file = %q, want %q", cfg.Auth.SessionKeyFile, want)
		}
	})

	t.Run("with no credential at all it is rejected", func(t *testing.T) {
		t.Parallel()
		err := parseYAMLErr(t, src)
		assertMessageContains(t, err, "auth.password", "neither")
	})
}

// ---------------------------------------------------------------------------
// Lookup order — arch §3.1
// ---------------------------------------------------------------------------

type fakeEnv map[string]string

func (f fakeEnv) get(k string) string { return f[k] }

// lookupFixture lays out one candidate directory per lookup-order entry and
// writes a config into the ones named.
func lookupFixture(t *testing.T, present ...string) (Options, map[string]string) {
	t.Helper()
	base := t.TempDir()
	paths := map[string]string{
		"explicit": filepath.Join(base, "explicit", "custom.yaml"),
		"env":      filepath.Join(base, "env", "shelf.yaml"),
		"work":     filepath.Join(base, "work", FileName),
		"xdg":      filepath.Join(base, "xdg", appName, FileName),
		"etc":      filepath.Join(base, "etc", appName, FileName),
	}
	for _, name := range present {
		p, ok := paths[name]
		if !ok {
			t.Fatalf("unknown candidate %q", name)
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		body := fmt.Sprintf("# %s\nserver: {port: %d}\n%s", name, 9000+len(name), minimalYAML)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	env := fakeEnv{
		"HOME":            filepath.Join(base, "home"),
		"SHELF_CONFIG":    paths["env"],
		"XDG_CONFIG_HOME": filepath.Join(base, "xdg"),
	}
	opts := Options{
		WorkDir: filepath.Join(base, "work"),
		Getenv:  env.get,
		goos:    "linux",
		etcDir:  filepath.Join(base, "etc", appName),
	}
	return opts, paths
}

func TestLocate_lookupOrder(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		present  []string
		explicit bool
		want     string
	}{
		{"explicit beats everything", []string{"explicit", "env", "work", "xdg", "etc"}, true, "explicit"},
		{"SHELF_CONFIG beats the working directory", []string{"env", "work", "xdg", "etc"}, false, "env"},
		{"working directory beats XDG", []string{"work", "xdg", "etc"}, false, "work"},
		{"XDG beats /etc", []string{"xdg", "etc"}, false, "xdg"},
		{"/etc is the last resort", []string{"etc"}, false, "etc"},
		{"a missing SHELF_CONFIG falls through", []string{"work"}, false, "work"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			opts, paths := lookupFixture(t, c.present...)
			if c.explicit {
				opts.ExplicitPath = paths["explicit"]
			}
			got, err := Locate(opts)
			if err != nil {
				t.Fatalf("Locate: %v", err)
			}
			if got != paths[c.want] {
				t.Errorf("Locate = %q, want the %s candidate %q", got, c.want, paths[c.want])
			}
		})
	}
}

func TestLocate_missingExplicitPath_isFatalAndNeverFallsThrough(t *testing.T) {
	t.Parallel()
	opts, paths := lookupFixture(t, "env", "work", "xdg", "etc")
	opts.ExplicitPath = paths["explicit"] // written by nobody
	_, err := Locate(opts)
	if err == nil {
		t.Fatal("a --config file that does not exist must be fatal, not a fallback")
	}
	assertMessageContains(t, err, "--config", paths["explicit"])
}

func TestLocate_noFileAnywhere_reportsWhereItLookedAndWhereInitWrites(t *testing.T) {
	t.Parallel()
	opts, paths := lookupFixture(t)
	_, err := Locate(opts)
	var nf *NotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("error = %v (%T), want *NotFoundError", err, err)
	}
	for _, want := range []string{paths["env"], paths["work"], paths["xdg"], paths["etc"]} {
		if !slices.Contains(nf.Searched, want) {
			t.Errorf("Searched = %v, missing %q", nf.Searched, want)
		}
	}
	if nf.InitPath != paths["xdg"] {
		t.Errorf("InitPath = %q, want the XDG location %q", nf.InitPath, paths["xdg"])
	}
	init, err := InitPath(opts)
	if err != nil || init != paths["xdg"] {
		t.Errorf("InitPath() = %q, %v, want %q, nil", init, err, paths["xdg"])
	}
}

func TestCandidates_onWindows_hasNoEtcEntry(t *testing.T) {
	t.Parallel()
	opts, _ := lookupFixture(t)
	opts.goos = "windows"
	for _, p := range Candidates(opts) {
		if strings.Contains(p, string(filepath.Separator)+"etc"+string(filepath.Separator)) {
			t.Errorf("candidate %q must not be offered on windows", p)
		}
	}
}

// TestLoad_firstFileWins_nothingIsMerged: the file that wins is the whole
// configuration; keys set only in a losing candidate must not leak into it.
func TestLoad_firstFileWins_nothingIsMerged(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	root := filepath.Join(base, "media")
	mkdir(t, root)

	winner := filepath.Join(base, "winner.yaml")
	writeFile(t, winner, fmt.Sprintf("server: {port: 9001}\nroots:\n  - {name: manga, path: %q}\n", root))
	loser := filepath.Join(base, "work", FileName)
	writeFile(t, loser, fmt.Sprintf("server: {port: 9002}\nthumbnails: {quality: 50}\nroots:\n  - {name: other, path: %q}\n", root))

	env := fakeEnv{
		"HOME":            base,
		"SHELF_CONFIG":    winner,
		"XDG_DATA_HOME":   filepath.Join(base, "data"),
		"XDG_CACHE_HOME":  filepath.Join(base, "cache"),
		"XDG_CONFIG_HOME": filepath.Join(base, "config"),
	}
	cfg, err := Load(Options{WorkDir: filepath.Join(base, "work"), Getenv: env.get, goos: "linux", etcDir: filepath.Join(base, "etc")})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Port != 9001 {
		t.Errorf("port = %d, want 9001 from $SHELF_CONFIG", cfg.Server.Port)
	}
	if cfg.Thumbnails.Quality != 82 {
		t.Errorf("quality = %d, want the default 82: nothing from ./shelf.yaml may be merged in", cfg.Thumbnails.Quality)
	}
	if len(cfg.Roots) != 1 || cfg.Roots[0].Name != "manga" {
		t.Errorf("roots = %+v, want only the winner's root", cfg.Roots)
	}
	if cfg.FilePath != winner {
		t.Errorf("FilePath = %q, want %q", cfg.FilePath, winner)
	}
}

// TestAbsFilePath_resolvesARelativeFilePath is amendment A-10 / ruling E-25.
//
// `./shelf.yaml` is entry 2 of the lookup order, so a user who runs the binary
// from the directory holding the file gets `FilePath == "shelf.yaml"` — and
// "shelf.yaml을 편집한 뒤 재시작하세요" beside a bare file name answers nothing.
// AbsFilePath is what the settings screen shows, so it must be absolute even
// then, and it must not do it by rewriting FilePath: every configuration error
// message is prefixed with the path the user named.
func TestAbsFilePath_resolvesARelativeFilePath(t *testing.T) {
	t.Parallel()

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}

	cfg, err := Parse([]byte(minimalYAML), filepath.Join(".", FileName))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if filepath.IsAbs(cfg.FilePath) {
		t.Fatalf("the fixture is wrong: FilePath = %q is already absolute, so this test would pass without the code under test", cfg.FilePath)
	}

	got := cfg.AbsFilePath()
	if !filepath.IsAbs(got) {
		t.Errorf("AbsFilePath() = %q, want an absolute path", got)
	}
	if want := filepath.Join(wd, FileName); got != want {
		t.Errorf("AbsFilePath() = %q, want %q", got, want)
	}
	if filepath.Base(got) != FileName {
		t.Errorf("AbsFilePath() = %q, want a path ending in %q", got, FileName)
	}
	// The raw field is the one every error message quotes (config_test's
	// TestError_message, cmd/shelf/main_test.go) — resolving must not touch it.
	if cfg.FilePath != filepath.Join(".", FileName) {
		t.Errorf("FilePath = %q after AbsFilePath(); it must be left exactly as the loader found it", cfg.FilePath)
	}
}

// TestAbsFilePath_absoluteAndEmpty: an already-absolute path is cleaned and
// returned, and a Config parsed from bytes with no name has nothing to show.
func TestAbsFilePath_absoluteAndEmpty(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfg, err := Parse([]byte(minimalYAML), filepath.Join(dir, "sub", "..", FileName))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got, want := cfg.AbsFilePath(), filepath.Join(dir, FileName); got != want {
		t.Errorf("AbsFilePath() = %q, want the cleaned %q", got, want)
	}

	anon, err := Parse([]byte(minimalYAML), "")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := anon.AbsFilePath(); got != "" {
		t.Errorf("AbsFilePath() with no FilePath = %q, want \"\" — there is no file to name", got)
	}
}

// ---------------------------------------------------------------------------
// Per-OS directories — arch §3.2, and NFR-OPS-002
// ---------------------------------------------------------------------------

func TestStorageDirs_resolvePerOS(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name                            string
		goos                            string
		env                             fakeEnv
		wantData, wantCache, wantConfig string
	}{
		{
			name: "linux without XDG", goos: "linux",
			env:        fakeEnv{"HOME": "/home/u"},
			wantData:   "/home/u/.local/share/shelf",
			wantCache:  "/home/u/.cache/shelf",
			wantConfig: "/home/u/.config/shelf",
		},
		{
			name: "linux with XDG", goos: "linux",
			env:        fakeEnv{"HOME": "/home/u", "XDG_DATA_HOME": "/d", "XDG_CACHE_HOME": "/c", "XDG_CONFIG_HOME": "/g"},
			wantData:   "/d/shelf",
			wantCache:  "/c/shelf",
			wantConfig: "/g/shelf",
		},
		{
			name: "macOS", goos: "darwin",
			env:        fakeEnv{"HOME": "/Users/u"},
			wantData:   "/Users/u/Library/Application Support/shelf",
			wantCache:  "/Users/u/Library/Caches/shelf",
			wantConfig: "/Users/u/.config/shelf",
		},
		{
			name: "windows", goos: "windows",
			env:        fakeEnv{"LOCALAPPDATA": "/localappdata"},
			wantData:   "/localappdata/shelf",
			wantCache:  "/localappdata/shelf/cache",
			wantConfig: "/localappdata/shelf",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			env := Options{Getenv: c.env.get, goos: c.goos}.environment()
			if got, err := env.dataDir(); err != nil || got != c.wantData {
				t.Errorf("dataDir = %q, %v, want %q", got, err, c.wantData)
			}
			if got, err := env.cacheDir(); err != nil || got != c.wantCache {
				t.Errorf("cacheDir = %q, %v, want %q", got, err, c.wantCache)
			}
			if got, err := env.configDir(); err != nil || got != c.wantConfig {
				t.Errorf("configDir = %q, %v, want %q", got, err, c.wantConfig)
			}
		})
	}
}

func TestStorageDirs_undeterminable_isAConfigError(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		goos string
		env  fakeEnv
	}{
		{"unix without HOME", "linux", fakeEnv{}},
		{"windows without LOCALAPPDATA", "windows", fakeEnv{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			env := Options{Getenv: c.env.get, goos: c.goos}.environment()
			if _, err := env.dataDir(); err == nil {
				t.Error("dataDir: expected an error")
			}
			cfg := defaults()
			if err := cfg.resolve(env); err == nil {
				t.Error("resolve: expected an error naming storage.data_dir")
			} else if !strings.Contains(err.Error(), "storage.data_dir") {
				t.Errorf("resolve error = %v, want it to name storage.data_dir", err)
			}
		})
	}
}

func TestParse_explicitStorageDirs_areMadeAbsolute(t *testing.T) {
	t.Parallel()
	cfg := parseYAML(t, withRoots(`storage: {data_dir: "relative/data", cache_dir: "/srv/shelf/cache"}`))
	if !filepath.IsAbs(cfg.Storage.DataDir) {
		t.Errorf("data_dir = %q, want an absolute path", cfg.Storage.DataDir)
	}
	if got, want := cfg.Storage.CacheDir, "/srv/shelf/cache"; got != want {
		t.Errorf("cache_dir = %q, want %q", got, want)
	}
}

// TestLoad_createsStorageDirs is NFR-OPS-002: a configuration file and a cache
// directory are all SHELF needs at runtime, so it creates its own directories.
func TestLoad_createsStorageDirs0700(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	root := filepath.Join(base, "media")
	mkdir(t, root)
	cfgPath := filepath.Join(base, FileName)
	dataDir := filepath.Join(base, "state", "data")
	cacheDir := filepath.Join(base, "state", "cache")
	writeFile(t, cfgPath, fmt.Sprintf("storage: {data_dir: %q, cache_dir: %q}\nroots:\n  - {name: manga, path: %q}\n", dataDir, cacheDir, root))

	cfg, err := Load(Options{ExplicitPath: cfgPath})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, dir := range []string{cfg.Storage.DataDir, cfg.Storage.CacheDir} {
		fi, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("Load must create %s: %v", dir, err)
		}
		if got, want := fi.Mode().Perm(), fs.FileMode(0o700); got != want {
			t.Errorf("%s mode = %v, want %v", dir, got, want)
		}
	}
}

// skipUnlessUnprivileged is the same opt-in `internal/httpapi/harness_test.go`
// applies to its three permission-dependent cases, and it is duplicated rather
// than shared because a test-only helper has no home both packages can import.
//
// The skip below is correct — root writes into a directory whatever its mode
// says, so the case cannot be produced — but a containerised CI running as uid 0
// loses the assertion and still reports green. `SHELF_REQUIRE_UNPRIVILEGED=1`
// lets such a CI demand the coverage it believes it has; unset, nothing changes.
// Keep the two in step: a variable honoured in one package and not the other is
// worse than no variable, because it is believed.
func skipUnlessUnprivileged(t *testing.T, why string) {
	t.Helper()
	if os.Geteuid() != 0 {
		return
	}
	if os.Getenv("SHELF_REQUIRE_UNPRIVILEGED") == "1" {
		t.Fatalf("%s, so this case cannot be produced; SHELF_REQUIRE_UNPRIVILEGED=1 asked for it to run "+
			"anyway. Run the suite as an unprivileged user, or unset the variable to go back to skipping.", why)
	}
	t.Skip(why)
}

func TestLoad_unwritableStorageDir_isRejected(t *testing.T) {
	t.Parallel()
	skipUnlessUnprivileged(t, "root can write to any directory")
	base := t.TempDir()
	root := filepath.Join(base, "media")
	mkdir(t, root)
	dataDir := filepath.Join(base, "readonly")
	mkdir(t, dataDir)
	if err := os.Chmod(dataDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dataDir, 0o700) })

	cfgPath := filepath.Join(base, FileName)
	writeFile(t, cfgPath, fmt.Sprintf("storage: {data_dir: %q, cache_dir: %q}\nroots:\n  - {name: manga, path: %q}\n",
		dataDir, filepath.Join(base, "cache"), root))

	_, err := Load(Options{ExplicitPath: cfgPath})
	if err == nil {
		t.Fatal("an unwritable data_dir must be rejected")
	}
	assertMessageContains(t, err, "storage.data_dir", dataDir, "not writable")
}

func TestLoad_rootPathMustBeAnExistingDirectory(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	file := filepath.Join(base, "not-a-dir")
	writeFile(t, file, "x")

	cases := []struct {
		name, rootPath, want string
	}{
		{"missing", filepath.Join(base, "nope"), "no such directory"},
		{"a file", file, "is not a directory"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			cfgPath := filepath.Join(t.TempDir(), FileName)
			writeFile(t, cfgPath, fmt.Sprintf("storage: {data_dir: %q, cache_dir: %q}\nroots:\n  - {name: manga, path: %q}\n",
				t.TempDir(), t.TempDir(), c.rootPath))
			_, err := Load(Options{ExplicitPath: cfgPath})
			if err == nil {
				t.Fatal("expected an error")
			}
			assertMessageContains(t, err, "roots[0].path", c.rootPath, c.want)
		})
	}
}

// ---------------------------------------------------------------------------
// base_path — NFR-SEC-003
// ---------------------------------------------------------------------------

func TestNormalizeBasePath(t *testing.T) {
	t.Parallel()
	ok := []struct{ in, want string }{
		{"", ""},
		{"/", ""},
		{"//", ""},
		{"reader", "/reader"},
		{"/reader", "/reader"},
		{"/reader/", "/reader"},
		{"reader/", "/reader"},
		{"/a/b", "/a/b"},
		{"/a/b/", "/a/b"},
		{"만화", "/만화"},
		{"/v1.2_beta-x", "/v1.2_beta-x"},
	}
	for _, c := range ok {
		got, err := normalizeBasePath(c.in)
		if err != nil {
			t.Errorf("normalizeBasePath(%q): unexpected error %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("normalizeBasePath(%q) = %q, want %q", c.in, got, c.want)
		}
	}

	bad := []string{
		"..",
		"/..",
		"../etc",
		"/reader/../admin",
		"/reader/..",
		"/rea..der",
		"/a//b",
		"/./a",
		"/a b",
		"/a\tb",
		`/a\b`,
		"/a?b",
		"/a#b",
		"/a\x00b",
	}
	for _, in := range bad {
		if got, err := normalizeBasePath(in); err == nil {
			t.Errorf("normalizeBasePath(%q) = %q, want an error", in, got)
		}
	}
}

func TestParse_basePath_isNormalisedInPlace(t *testing.T) {
	t.Parallel()
	cases := []struct{ in, want string }{
		{`""`, ""},
		{`"reader"`, "/reader"},
		{`"/reader/"`, "/reader"},
		{`"/"`, ""},
	}
	for _, c := range cases {
		cfg := parseYAML(t, withRoots(fmt.Sprintf("server: {base_path: %s}", c.in)))
		if cfg.Server.BasePath != c.want {
			t.Errorf("base_path %s normalised to %q, want %q", c.in, cfg.Server.BasePath, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Rejections — arch §3.2, one case per rejection, exit code ExitCode
// ---------------------------------------------------------------------------

// hereMarker marks the line a table row expects the message to point at.
const hereMarker = "# <<HERE"

func markedLine(t *testing.T, body string) int {
	t.Helper()
	for i, l := range strings.Split(body, "\n") {
		if strings.Contains(l, hereMarker) {
			return i + 1
		}
	}
	return 0
}

func TestValidate_rejections(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		yaml string
		want []string // substrings every message must contain, key and value first
	}{{
		name: "no roots at all",
		yaml: "server: {port: 8080}\n",
		want: []string{"roots", "at least one root"},
	}, {
		name: "duplicate root name",
		yaml: "roots:\n  - {name: manga, path: /a}\n  - {name: manga, path: /b} " + hereMarker + "\n",
		want: []string{"roots[1].name", `"manga"`, "duplicate root name"},
	}, {
		name: "malformed root name",
		yaml: "roots:\n  - {name: \"만화 root!\", path: /a} " + hereMarker + "\n",
		want: []string{"roots[0].name", `"만화 root!"`, "[a-zA-Z0-9._-]"},
	}, {
		name: "root name too long",
		yaml: "roots:\n  - {name: " + strings.Repeat("a", 65) + ", path: /a} " + hereMarker + "\n",
		want: []string{"roots[0].name", "1 to 64"},
	}, {
		name: "missing root name",
		yaml: "roots:\n  - {path: /a} " + hereMarker + "\n",
		want: []string{"roots[0].name", "required"},
	}, {
		name: "relative root path",
		yaml: "roots:\n  - {name: manga, path: media/manga} " + hereMarker + "\n",
		want: []string{"roots[0].path", `"media/manga"`, "absolute"},
	}, {
		name: "missing root path",
		yaml: "roots:\n  - {name: manga} " + hereMarker + "\n",
		want: []string{"roots[0].path", "required"},
	}, {
		name: "port out of range",
		yaml: withRoots("server:\n  port: 70000 " + hereMarker),
		want: []string{"server.port", "70000", "1 and 65535"},
	}, {
		name: "port zero",
		yaml: withRoots("server:\n  port: 0 " + hereMarker),
		want: []string{"server.port", "0", "1 and 65535"},
	}, {
		name: "empty listen",
		yaml: withRoots("server:\n  listen: \"\" " + hereMarker),
		want: []string{"server.listen", "must not be empty"},
	}, {
		// Amendment A-12 (ruling E-40). A browse base is validated like a root
		// path minus "must exist": the picker opens it on demand, so an
		// unmounted one must not stop the server booting (arch §4.9).
		name: "relative browse base",
		yaml: withRoots("server:\n  browse_bases: [\"mnt/media\"] " + hereMarker),
		want: []string{"server.browse_bases[0]", `"mnt/media"`, "absolute"},
	}, {
		name: "empty browse base",
		yaml: withRoots("server:\n  browse_bases: [\"\"] " + hereMarker),
		want: []string{"server.browse_bases[0]", "must not be empty"},
	}, {
		name: "base_path with ..",
		yaml: withRoots("server:\n  base_path: \"/reader/../admin\" " + hereMarker),
		want: []string{"server.base_path", `"/reader/../admin"`, `".."`},
	}, {
		name: "non-positive read_header_timeout",
		yaml: withRoots("server:\n  read_header_timeout: \"0s\" " + hereMarker),
		want: []string{"server.read_header_timeout", "positive"},
	}, {
		name: "negative shutdown_grace",
		yaml: withRoots("server:\n  shutdown_grace: \"-1s\" " + hereMarker),
		want: []string{"server.shutdown_grace", "negative"},
	}, {
		// FR-CFG-005: the one way a media volume could ever be written to.
		name: "cache_dir inside a media root",
		yaml: "storage:\n  cache_dir: \"/mnt/media/manga/.thumbs\" " + hereMarker + "\nroots:\n  - {name: manga, path: /mnt/media/manga}\n",
		want: []string{"storage.cache_dir", `"/mnt/media/manga/.thumbs"`, "must not be inside roots[0].path", "never writes to a media volume"},
	}, {
		name: "data_dir equal to a media root",
		yaml: "storage:\n  data_dir: \"/mnt/media/manga\" " + hereMarker + "\nroots:\n  - {name: manga, path: /mnt/media/manga}\n",
		want: []string{"storage.data_dir", "must not be inside roots[0].path"},
	}, {
		name: "negative scan workers",
		yaml: withRoots("scan:\n  workers: -1 " + hereMarker),
		want: []string{"scan.workers", "-1", "must be 0"},
	}, {
		name: "negative max_depth",
		yaml: withRoots("scan:\n  max_depth: -1 " + hereMarker),
		want: []string{"scan.max_depth", "-1", "unlimited"},
	}, {
		name: "negative cover_max_loose_images",
		yaml: withRoots("scan:\n  cover_max_loose_images: -3 " + hereMarker),
		want: []string{"scan.cover_max_loose_images", "-3"},
	}, {
		name: "malformed include glob",
		yaml: withRoots("scan:\n  include_globs:\n    - \"[unterminated\" " + hereMarker),
		want: []string{"scan.include_globs[0]", `"[unterminated"`, "not a valid pattern", "[[]"},
	}, {
		name: "malformed exclude glob",
		yaml: withRoots("scan:\n  exclude_globs:\n    - \"**/[\" " + hereMarker),
		want: []string{"scan.exclude_globs[0]", "not a valid pattern"},
	}, {
		name: "empty thumbnail widths",
		yaml: withRoots("thumbnails:\n  widths: [] " + hereMarker),
		want: []string{"thumbnails.widths", "at least one width"},
	}, {
		name: "thumbnail width too small",
		yaml: withRoots("thumbnails:\n  widths: [16, 240] " + hereMarker),
		want: []string{"thumbnails.widths", "16", "32 and 2048"},
	}, {
		name: "thumbnail width too large",
		yaml: withRoots("thumbnails:\n  widths: [240, 4096] " + hereMarker),
		want: []string{"thumbnails.widths", "4096", "32 and 2048"},
	}, {
		name: "quality out of range",
		yaml: withRoots("thumbnails:\n  quality: 0 " + hereMarker),
		want: []string{"thumbnails.quality", "0", "1 and 100"},
	}, {
		name: "format other than jpeg",
		yaml: withRoots("thumbnails:\n  format: \"webp\" " + hereMarker),
		want: []string{"thumbnails.format", `"webp"`, `must be "jpeg"`},
	}, {
		name: "negative thumbnail workers",
		yaml: withRoots("thumbnails:\n  workers: -2 " + hereMarker),
		want: []string{"thumbnails.workers", "-2"},
	}, {
		name: "zero max_source_bytes",
		yaml: withRoots("thumbnails:\n  max_source_bytes: 0 " + hereMarker),
		want: []string{"thumbnails.max_source_bytes", "0", "positive"},
	}, {
		name: "zero pdf workers",
		yaml: withRoots("pdf:\n  workers: 0 " + hereMarker),
		want: []string{"pdf.workers", "0", "at least 1"},
	}, {
		name: "pdf default_width above max_width",
		yaml: withRoots("pdf:\n  default_width: 4000 " + hereMarker),
		want: []string{"pdf.default_width", "4000", "pdf.max_width"},
	}, {
		name: "pdf max_width out of range",
		yaml: withRoots("pdf:\n  max_width: 50 " + hereMarker),
		want: []string{"pdf.max_width", "50", "100 and 10000"},
	}, {
		// A-8: there is deliberately no "disabled" value. 0 must be an error,
		// not a window of zero days — the 최근 추가 sidebar entry is always shown,
		// and an empty smart list with no explanation is worse than a wrong one.
		name: "recently_added_days zero",
		yaml: withRoots("library:\n  recently_added_days: 0 " + hereMarker),
		want: []string{"library.recently_added_days", "0", "1 and 3650"},
	}, {
		name: "recently_added_days negative",
		yaml: withRoots("library:\n  recently_added_days: -1 " + hereMarker),
		want: []string{"library.recently_added_days", "-1", "1 and 3650"},
	}, {
		name: "recently_added_days above ten years",
		yaml: withRoots("library:\n  recently_added_days: 3651 " + hereMarker),
		want: []string{"library.recently_added_days", "3651", "1 and 3650"},
	}, {
		name: "prefetch out of range",
		yaml: withRoots("reader:\n  prefetch: 21 " + hereMarker),
		want: []string{"reader.prefetch", "21", "0 and 20"},
	}, {
		name: "unknown reading direction",
		yaml: withRoots("reader:\n  reading_direction: \"rtl2\" " + hereMarker),
		want: []string{"reader.reading_direction", `"rtl2"`, `"ltr"`, `"rtl"`},
	}, {
		// C-1: the wire value is "spread"; "double" exists nowhere in the code.
		name: "display_mode double is not a wire value",
		yaml: withRoots("reader:\n  display_mode: \"double\" " + hereMarker),
		want: []string{"reader.display_mode", `"double"`, `"spread"`},
	}, {
		// C-2: the wire value is "contain", not the ui-spec's "screen".
		name: "fit_mode screen is not a wire value",
		yaml: withRoots("reader:\n  fit_mode: \"screen\" " + hereMarker),
		want: []string{"reader.fit_mode", `"screen"`, `"contain"`},
	}, {
		name: "unknown theme",
		yaml: withRoots("reader:\n  theme: \"sepia\" " + hereMarker),
		want: []string{"reader.theme", `"sepia"`, `"system"`},
	}, {
		name: "auth with both password and hash",
		yaml: withRoots("auth:\n  password: \"hunter2\" " + hereMarker + "\n  password_hash: \"$2a$12$abcdefghijklmnopqrstuv\""),
		want: []string{"auth.password", "not both"},
	}, {
		name: "auth with no credential",
		yaml: withRoots("auth: " + hereMarker + "\n  session_ttl: \"24h\""),
		want: []string{"auth.password", "neither"},
	}, {
		// The value is deliberately NOT in the message — see
		// TestErrors_neverEchoAnAuthSecret. The key and the line are what the
		// user needs, and the value is very often their password.
		name: "auth hash that is not bcrypt",
		yaml: withRoots("auth:\n  password_hash: \"not-a-hash\" " + hereMarker),
		want: []string{"auth.password_hash", "bcrypt", `"$2"`},
	}, {
		name: "auth session_ttl not positive",
		yaml: withRoots("auth:\n  password: \"hunter2\"\n  session_ttl: \"0s\" " + hereMarker),
		want: []string{"auth.session_ttl", "positive"},
	}, {
		name: "unknown log level",
		yaml: withRoots("log:\n  level: \"trace\" " + hereMarker),
		want: []string{"log.level", `"trace"`, `"debug"`},
	}, {
		name: "unknown log format",
		yaml: withRoots("log:\n  format: \"logfmt\" " + hereMarker),
		want: []string{"log.format", `"logfmt"`, `"json"`},
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			err := parseYAMLErr(t, c.yaml)
			assertMessageContains(t, err, c.want...)
			assertIsConfigError(t, err)
			if line := markedLine(t, c.yaml); line > 0 {
				assertMessageContains(t, err, fmt.Sprintf("shelf.yaml:%d:", line))
			}
		})
	}
}

func TestValidate_reportsEveryProblemAtOnce(t *testing.T) {
	t.Parallel()
	err := parseYAMLErr(t, `server:
  port: 0
  base_path: "../x"
thumbnails:
  format: "png"
roots:
  - {name: "manga", path: "relative"}
`)
	var errs Errors
	if !errors.As(err, &errs) {
		t.Fatalf("error = %T, want Errors", err)
	}
	if len(errs) != 4 {
		t.Errorf("got %d errors, want 4 (one per problem):\n%v", len(errs), err)
	}
	if !slices.IsSortedFunc(errs, func(a, b *Error) int { return a.Line - b.Line }) {
		t.Errorf("errors must be ordered by line:\n%v", err)
	}
	var single *Error
	if !errors.As(err, &single) {
		t.Error("errors.As must reach an individual *Error")
	}
}

// ---------------------------------------------------------------------------
// Typos: unknown keys, duplicates, wrong types
// ---------------------------------------------------------------------------

func TestParse_unknownKeys_areRejectedWithTheirLine(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		yaml string
		want []string
	}{{
		name: "unknown top-level key",
		yaml: withRoots("srever: " + hereMarker + "\n  port: 8080"),
		want: []string{`unknown key "srever"`, `did you mean "server"?`},
	}, {
		name: "unknown top-level key with no near match",
		yaml: withRoots("elephants: 4 " + hereMarker),
		want: []string{`unknown key "elephants"`},
	}, {
		name: "unknown nested key",
		yaml: withRoots("server:\n  prot: 8080 " + hereMarker),
		want: []string{`server.prot`, `did you mean "port"?`},
	}, {
		name: "unknown key inside a root entry",
		yaml: "roots:\n  - name: manga\n    path: /a\n    labell: x " + hereMarker,
		want: []string{`roots[0].labell`, `did you mean "label"?`},
	}, {
		name: "a key that only exists in the arch draft",
		yaml: withRoots("thumbnails:\n  cache_max_bytes: 100 " + hereMarker),
		want: []string{`unknown key "cache_max_bytes"`},
	}, {
		name: "duplicate key",
		yaml: withRoots("server:\n  port: 8080\n  port: 9090 " + hereMarker),
		want: []string{"server.port", "duplicate key"},
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			err := parseYAMLErr(t, c.yaml)
			assertMessageContains(t, err, c.want...)
			assertIsConfigError(t, err)
			if line := markedLine(t, c.yaml); line > 0 {
				assertMessageContains(t, err, fmt.Sprintf("shelf.yaml:%d:", line))
			}
		})
	}
}

func TestParse_wrongTypes_nameTheKeyAndTheLine(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		yaml string
		want []string
		// omit is what the message must NOT say. yaml.v3 reports a line and no
		// column, so on a line holding several keys the loader has to work out
		// which one it means — and naming a sibling that is perfectly correct
		// is worse than naming nobody.
		omit []string
	}{{
		name: "a word where a number belongs",
		yaml: withRoots("server:\n  port: eighty " + hereMarker),
		want: []string{"server.port", "shelf.yaml:2:"},
	}, {
		name: "an unparseable duration",
		yaml: withRoots("server:\n  read_header_timeout: \"10 apples\" " + hereMarker),
		want: []string{"server.read_header_timeout", "not a duration", `"10 apples"`},
	}, {
		name: "a duration written as a bare number",
		yaml: withRoots("server:\n  shutdown_grace: 10 " + hereMarker),
		want: []string{"server.shutdown_grace", "not a duration"},
	}, {
		name: "a mapping where a list belongs",
		yaml: withRoots("scan:\n  include_globs: {a: b} " + hereMarker),
		want: []string{"scan.include_globs"},
	}, {
		// --- flow mappings ------------------------------------------------
		// impl-plan §6.3's E2E configuration writes server:, thumbnails:, pdf:
		// and log: as flow mappings, so every one of these is a line the loader
		// will really see.
		name: "flow mapping: the bad key is not the first one on the line",
		yaml: withRoots(`server: { listen: "0.0.0.0", port: eighty } ` + hereMarker),
		want: []string{"server.port", "eighty"},
		omit: []string{"server.listen"},
	}, {
		// The regression this row exists for: read_header_timeout is a duration
		// and "10s" is a valid one, so blaming it produced a message in which
		// the key, the complaint and the value were all three false.
		name: "flow mapping: a correct duration next to a bad number",
		yaml: withRoots(`server: { read_header_timeout: "10s", port: eighty } ` + hereMarker),
		want: []string{"server.port", "eighty"},
		omit: []string{"read_header_timeout", "not a duration", `"10s"`},
	}, {
		name: "flow mapping: a bad duration next to a correct number",
		yaml: withRoots(`server: { shutdown_grace: 10, port: 8080 } ` + hereMarker),
		want: []string{"server.shutdown_grace", "not a duration", `"10"`},
		omit: []string{"server.port"},
	}, {
		name: "flow mapping: a bad bool after a good one",
		yaml: withRoots(`log: { level: "debug", http_requests: maybe } ` + hereMarker),
		want: []string{"log.http_requests", "maybe"},
		omit: []string{"log.level"},
	}, {
		name: "flow mapping: a bad int after a good bool",
		yaml: withRoots(`pdf: { enabled: true, workers: lots } ` + hereMarker),
		want: []string{"pdf.workers", "lots"},
		omit: []string{"pdf.enabled"},
	}, {
		name: "flow sequence: the bad key is inside a roots entry",
		yaml: "roots: [{name: m, path: /a, enabled: perhaps}] " + hereMarker,
		want: []string{"roots[0].enabled", "perhaps"},
		omit: []string{"roots[0].name", "roots[0].path"},
	}, {
		// Two siblings of the same type holding the same bad value cannot be
		// told apart. The message keeps the file, the line and the value and
		// names no key, rather than inventing one.
		name: "flow mapping: an undecidable line names no key at all",
		yaml: withRoots(`pdf: { workers: lots, default_width: lots } ` + hereMarker),
		want: []string{"shelf.yaml:1:", "lots"},
		omit: []string{"pdf.workers:", "pdf.default_width:"},
	}, {
		name: "the top level is not a mapping",
		yaml: "- manga\n- books\n",
		want: []string{"top level must be a mapping"},
	}, {
		name: "not YAML at all",
		yaml: "roots: [\n",
		want: []string{"shelf.yaml:"},
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			err := parseYAMLErr(t, c.yaml)
			assertMessageContains(t, err, c.want...)
			assertMessageOmits(t, err, c.omit...)
			if line := markedLine(t, c.yaml); line > 0 {
				assertMessageContains(t, err, fmt.Sprintf("shelf.yaml:%d:", line))
			}
		})
	}
}

// TestErrors_neverEchoAnAuthSecret is impl-plan §5.1: "Never log passwords, the
// session key, or absolute paths outside roots[].path". Every one of these
// inputs is a plaintext password put somewhere the loader rejects, and WP-13
// prints the rejection to stderr, where the journal keeps it.
func TestErrors_neverEchoAnAuthSecret(t *testing.T) {
	t.Parallel()
	// Seven characters on purpose: yaml.v3 abbreviates a value longer than that
	// to "hunter2..." in its own decode messages, which would let a leak of the
	// first seven characters slip past this test. A short password is printed
	// whole, so what this asserts is that nothing is printed at all.
	const secret = "hunter2"
	cases := []struct {
		name string
		yaml string
		want []string
	}{{
		// The likeliest mistake of them all: shelf.example.yaml puts password:
		// and password_hash: on adjacent lines.
		name: "a password pasted into password_hash",
		yaml: withRoots(`auth: {password_hash: "` + secret + `"}`),
		want: []string{"auth.password_hash", "bcrypt"},
	}, {
		name: "a password pasted into password_hash, block style",
		yaml: withRoots("auth:\n  password_hash: \"" + secret + "\""),
		want: []string{"auth.password_hash", "bcrypt"},
	}, {
		// `auth:` is a mapping; writing the password straight after it makes
		// yaml quote the whole scalar in its own decode message.
		name: "a password written where the auth block belongs",
		yaml: withRoots(`auth: ` + secret),
		want: []string{"shelf.yaml:1:", "auth"},
	}, {
		name: "a password alongside a hash",
		yaml: withRoots(`auth: {password: "` + secret + `", password_hash: "$2a$12$abcdefghijklmnopqrstuv"}`),
		want: []string{"auth.password", "not both"},
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			err := parseYAMLErr(t, c.yaml)
			assertMessageContains(t, err, c.want...)
			assertMessageOmits(t, err, secret)
		})
	}
}

func TestParse_emptyFile_asksForRoots(t *testing.T) {
	t.Parallel()
	for _, body := range []string{"", "\n", "# only comments\n"} {
		err := parseYAMLErr(t, body)
		assertMessageContains(t, err, "roots", "at least one root")
	}
}

// ---------------------------------------------------------------------------
// include_globs — amendment A-3 / decisions E-6, and the E2E curated subset
// ---------------------------------------------------------------------------

// TestIncludeGlobs_koreanBracketPatterns is impl-plan §6.3: the E2E subset
// names ten series whose directories start with a literal "[만화]". Both the raw
// form and the "[[]" escaped form are valid path.Match patterns and must load.
func TestIncludeGlobs_koreanBracketPatterns(t *testing.T) {
	t.Parallel()
	cfg := parseYAML(t, withRoots(`scan:
  include_globs:
    - "[만화] Clover 클로버 (총4권)"
    - "[[]만화] 바퀴.zip"
    - "[만화] 미생 1~9 (완결 pdf)"
    - "**/*.part"`))
	want := []string{
		"[만화] Clover 클로버 (총4권)",
		"[[]만화] 바퀴.zip",
		"[만화] 미생 1~9 (완결 pdf)",
		"**/*.part",
	}
	if !slices.Equal(cfg.Scan.IncludeGlobs, want) {
		t.Errorf("include_globs = %q, want %q", cfg.Scan.IncludeGlobs, want)
	}
}

// TestE2EConfig_parses is the configuration impl-plan §6.3 has scripts/e2e.sh
// emit, verbatim: flow mappings, a root pointed at the real collection and ten
// literal Korean directory names as include_globs. It has to load as written.
func TestE2EConfig_parses(t *testing.T) {
	t.Parallel()
	cfg := parseYAML(t, `server: { listen: "127.0.0.1", port: 8791 }
roots:
  - name: "mangga"
    label: "만화 (E2E subset)"
    path: "/mnt/big-data/pds/taison-data/02. books/01. mangga"
storage:
  data_dir:  "/repo/.e2e/data"
  cache_dir: "/repo/.e2e/cache"
scan:
  on_start: false
  workers: 8
  include_globs:
    - "[만화] Clover 클로버 (총4권)"
    - "[만화] 상처를 쫓는자 1-11 (완) 이케가미 료이치"
    - "[만화] 자살도114-122"
    - "[만화] 바퀴.zip"
    - "[만화] 강철의 연금술사 1~27권 완결"
    - "[만화] 군계 1~25"
    - "[만화] 디엔엔젤 1-13권 연재중"
    - "[만화] 미생 1~9 (완결 pdf)"
    - "[만화] 배틀로얄 1~15 [완결].zip"
    - "[만화] 엔젤하트 전32권 완결.zip"
thumbnails: { widths: [120, 240, 400, 640], workers: 4 }
pdf: { enabled: true, workers: 1 }
log: { level: "debug", format: "text" }
`)
	if got, want := cfg.Server.Port, 8791; got != want {
		t.Errorf("port = %d, want %d", got, want)
	}
	if cfg.Scan.OnStart {
		t.Error("scan.on_start = true, want false: the script triggers the scan itself")
	}
	if got, want := len(cfg.Scan.IncludeGlobs), 10; got != want {
		t.Errorf("include_globs = %d entries, want %d", got, want)
	}
	if got, want := cfg.Roots[0].DisplayName(), "만화 (E2E subset)"; got != want {
		t.Errorf("DisplayName = %q, want %q", got, want)
	}
	if got, want := cfg.Storage.CacheDir, "/repo/.e2e/cache"; got != want {
		t.Errorf("cache_dir = %q, want %q", got, want)
	}
	if got, want := cfg.Log.Level, "debug"; got != want {
		t.Errorf("log.level = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// FR-CFG-002 — per-root enable
// ---------------------------------------------------------------------------

func TestEnabledRoots_disabledRootIsKeptButNotListed(t *testing.T) {
	t.Parallel()
	cfg := parseYAML(t, `roots:
  - {name: manga, path: /a}
  - {name: books, path: /b, enabled: false}
  - {name: doujin, path: /c, enabled: true, label: "동인지"}
`)
	if len(cfg.Roots) != 3 {
		t.Fatalf("roots = %+v, want all three kept: disabling must not drop a root", cfg.Roots)
	}
	if got := cfg.EnabledRoots(); len(got) != 2 || got[0].Name != "manga" || got[1].Name != "doujin" {
		t.Errorf("EnabledRoots() = %+v, want manga and doujin", got)
	}
	books, ok := cfg.RootByName("books")
	if !ok {
		t.Fatal("RootByName(books) must still find the disabled root")
	}
	if books.Enabled {
		t.Error("books.Enabled = true, want false")
	}
	if got, want := books.DisplayName(), "books"; got != want {
		t.Errorf("DisplayName without a label = %q, want %q", got, want)
	}
	doujin, _ := cfg.RootByName("doujin")
	if got, want := doujin.DisplayName(), "동인지"; got != want {
		t.Errorf("DisplayName with a label = %q, want %q", got, want)
	}
	if _, ok := cfg.RootByName("nope"); ok {
		t.Error("RootByName(nope) = true, want false")
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func assertMessageContains(t *testing.T, err error, want ...string) {
	t.Helper()
	msg := err.Error()
	for _, w := range want {
		if !strings.Contains(msg, w) {
			t.Errorf("error message must contain %q, got:\n%s", w, msg)
		}
	}
}

// assertMessageOmits is the other half of assertMessageContains: a message that
// names a key the user does not have to touch, or prints a value they must
// never see in a log, is a defect even though it "mentions the problem".
func assertMessageOmits(t *testing.T, err error, unwanted ...string) {
	t.Helper()
	msg := err.Error()
	for _, w := range unwanted {
		if strings.Contains(msg, w) {
			t.Errorf("error message must NOT contain %q, got:\n%s", w, msg)
		}
	}
}

// assertIsConfigError checks the error is one of ours, which is what makes it
// an exit-ExitCode condition rather than a crash.
func assertIsConfigError(t *testing.T, err error) {
	t.Helper()
	var one *Error
	var many Errors
	if !errors.As(err, &one) && !errors.As(err, &many) {
		t.Errorf("error = %v (%T), want *config.Error or config.Errors", err, err)
	}
	if ExitCode != 2 {
		t.Errorf("ExitCode = %d, want 2", ExitCode)
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	mkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestError_message keeps the shape of a message stable: file, line, key,
// what is wrong, and the value the user actually wrote.
func TestError_message(t *testing.T) {
	t.Parallel()
	e := &Error{Path: "shelf.yaml", Line: 30, Key: "server.port", Value: strconv.Itoa(70000), Msg: "must be between 1 and 65535"}
	if got, want := e.Error(), "shelf.yaml:30: server.port: must be between 1 and 65535 (got 70000)"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if got, want := (&Error{Key: "roots", Msg: "x"}).Error(), "roots: x"; got != want {
		t.Errorf("Error() without a file = %q, want %q", got, want)
	}
}

// TestBrowseBases_areCleanedInPlace is amendment A-12 (ruling E-40).
//
// `GET /api/browse` decides whether a requested path is inside a base by
// comparing cleaned strings, so an uncleaned base is a bug with a security
// shape rather than a cosmetic one: `/mnt/x/` does not contain `/mnt/x/y` under
// that comparison, and the endpoint would refuse everything under a base the
// operator did configure. Cleaning here — once, at load — is what keeps the
// endpoint from having to remember to.
func TestBrowseBases_areCleanedInPlace(t *testing.T) {
	t.Parallel()
	cfg, err := Parse([]byte(
		"server:\n  browse_bases:\n    - \"/mnt/media/\"\n    - \"/mnt/other/../media2\"\n"+
			"roots:\n  - {name: manga, path: /a}\n"), "t.yaml")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := []string{"/mnt/media", "/mnt/media2"}
	if len(cfg.Server.BrowseBases) != len(want) {
		t.Fatalf("browse_bases = %v, want %v", cfg.Server.BrowseBases, want)
	}
	for i := range want {
		if cfg.Server.BrowseBases[i] != want[i] {
			t.Errorf("browse_bases[%d] = %q, want %q", i, cfg.Server.BrowseBases[i], want[i])
		}
	}
}

// TestBrowseBases_defaultToNone pins the default, which is the whole of E-40's
// second limit: an operator who never heard of this key gets the pre-E-40
// product, where the picker refuses every request.
func TestBrowseBases_defaultToNone(t *testing.T) {
	t.Parallel()
	cfg, err := Parse([]byte("roots:\n  - {name: manga, path: /a}\n"), "t.yaml")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(cfg.Server.BrowseBases) != 0 {
		t.Errorf("browse_bases defaults to %v, want empty — the picker must be opt-in",
			cfg.Server.BrowseBases)
	}
}
