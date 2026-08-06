package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"shelf/internal/buildinfo"
	"shelf/internal/config"
)

func TestRun_version_printsTheBannerAndExitsZero(t *testing.T) {
	t.Parallel()
	var out, errOut bytes.Buffer
	if code := run([]string{"--version"}, strings.NewReader(""), &out, &errOut); code != exitOK {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, errOut.String())
	}
	if got := strings.TrimSpace(out.String()); got != buildinfo.String() {
		t.Errorf("--version printed %q, want %q", got, buildinfo.String())
	}
}

func TestRun_help_exitsZero(t *testing.T) {
	t.Parallel()
	var out, errOut bytes.Buffer
	if code := run([]string{"-h"}, strings.NewReader(""), &out, &errOut); code != exitOK {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(errOut.String(), "--rebuild-index") {
		t.Errorf("the usage text does not mention --rebuild-index:\n%s", errOut.String())
	}
}

func TestRun_unknownSubcommand_fails(t *testing.T) {
	t.Parallel()
	var out, errOut bytes.Buffer
	if code := run([]string{"migrate-root"}, strings.NewReader(""), &out, &errOut); code == exitOK {
		t.Fatal("an unknown subcommand must not exit 0")
	}
	if !strings.Contains(errOut.String(), "migrate-root") {
		t.Errorf("the error does not name the command: %s", errOut.String())
	}
}

// AC-007 — the server starts from a single binary plus a config file, so the
// failure when there is no config file must say exactly where one goes.
func TestRun_noConfigFile_exitsTwoAndNamesEveryPathItTried(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("SHELF_CONFIG", "")
	t.Chdir(t.TempDir())

	var out, errOut bytes.Buffer
	code := run(nil, strings.NewReader(""), &out, &errOut)
	if code != config.ExitCode {
		t.Fatalf("exit = %d, want %d\n%s", code, config.ExitCode, errOut.String())
	}
	msg := errOut.String()
	for _, want := range []string{"shelf.yaml", "--init-config"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the message does not mention %q:\n%s", want, msg)
		}
	}
}

func TestRun_explicitConfigMissing_isFatal(t *testing.T) {
	t.Parallel()
	var out, errOut bytes.Buffer
	missing := filepath.Join(t.TempDir(), "nope.yaml")
	code := run([]string{"--config", missing}, strings.NewReader(""), &out, &errOut)
	if code != config.ExitCode {
		t.Fatalf("exit = %d, want %d", code, config.ExitCode)
	}
	if !strings.Contains(errOut.String(), missing) {
		t.Errorf("the message does not name the file: %s", errOut.String())
	}
}

// --init-config writes the commented starter file and exits 0 (WP-13
// acceptance 3).
func TestRun_initConfig_writesAStarterFileAndRefusesToOverwriteIt(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "shelf.yaml")

	var out, errOut bytes.Buffer
	if code := run([]string{"--init-config", "--config", path}, strings.NewReader(""), &out, &errOut); code != exitOK {
		t.Fatalf("exit = %d, want 0\n%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), path) {
		t.Errorf("stdout does not name the file it wrote:\n%s", out.String())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the starter file: %v", err)
	}
	if !bytes.Equal(data, starterConfig) {
		t.Error("the file on disk is not the embedded starter")
	}

	// A config file is authored data: root names are hashed into every id in
	// user.db, so overwriting one silently orphans a reading history.
	out.Reset()
	errOut.Reset()
	if code := run([]string{"--init-config", "--config", path}, strings.NewReader(""), &out, &errOut); code == exitOK {
		t.Fatal("--init-config must refuse to overwrite an existing configuration")
	}
}

// The same promise shelf.example.yaml carries: every value written in the
// starter is the built-in default, so deleting a line changes nothing.
func TestStarterConfig_parsesAndStatesTheBuiltInDefaults(t *testing.T) {
	t.Parallel()
	fromStarter, err := config.Parse(starterConfig, "starter.yaml")
	if err != nil {
		t.Fatalf("the embedded starter configuration does not parse:\n%v", err)
	}
	minimal := []byte("roots:\n  - {name: \"books\", path: \"/edit/me/path/to/your/collection\"}\n")
	fromDefaults, err := config.Parse(minimal, "minimal.yaml")
	if err != nil {
		t.Fatalf("the minimal configuration does not parse: %v", err)
	}

	// Compare everything the starter actually writes. Label is deliberately
	// different (the starter shows one) and FilePath is not a setting.
	fromStarter.FilePath, fromDefaults.FilePath = "", ""
	fromStarter.Roots[0].Label = ""
	// reflect.DeepEqual rather than `!=` since amendment A-12 gave `server:` its
	// first slice field (`browse_bases`), which makes the struct incomparable.
	// The check is the same one: every value the starter writes is the default.
	if !reflect.DeepEqual(fromStarter.Server, fromDefaults.Server) {
		t.Errorf("server block drifted:\n starter:  %+v\n defaults: %+v", fromStarter.Server, fromDefaults.Server)
	}
	if fromStarter.Reader != fromDefaults.Reader {
		t.Errorf("reader block drifted:\n starter:  %+v\n defaults: %+v", fromStarter.Reader, fromDefaults.Reader)
	}
	if fromStarter.Log != fromDefaults.Log {
		t.Errorf("log block drifted:\n starter:  %+v\n defaults: %+v", fromStarter.Log, fromDefaults.Log)
	}
	if fromStarter.PDF != fromDefaults.PDF {
		t.Errorf("pdf block drifted:\n starter:  %+v\n defaults: %+v", fromStarter.PDF, fromDefaults.PDF)
	}
	if fromStarter.Scan.OnStart != fromDefaults.Scan.OnStart ||
		fromStarter.Scan.Workers != fromDefaults.Scan.Workers ||
		fromStarter.Scan.MaxDepth != fromDefaults.Scan.MaxDepth {
		t.Errorf("scan block drifted:\n starter:  %+v\n defaults: %+v", fromStarter.Scan, fromDefaults.Scan)
	}
	if fromStarter.Thumbnails.Quality != fromDefaults.Thumbnails.Quality ||
		len(fromStarter.Thumbnails.Widths) != len(fromDefaults.Thumbnails.Widths) {
		t.Errorf("thumbnails block drifted:\n starter:  %+v\n defaults: %+v",
			fromStarter.Thumbnails, fromDefaults.Thumbnails)
	}
	if fromStarter.Auth != nil {
		t.Error("the starter must ship with no auth block (ruling E-8)")
	}
}

// WP-13 acceptance 4.
func TestRun_hashPassword_printsAUsableBcryptHash(t *testing.T) {
	t.Parallel()
	var out, errOut bytes.Buffer
	if code := run([]string{"hash-password"}, strings.NewReader("hunter2\n"), &out, &errOut); code != exitOK {
		t.Fatalf("exit = %d, want 0\n%s", code, errOut.String())
	}
	line, _, _ := strings.Cut(out.String(), "\n")
	hash := strings.TrimSpace(line)
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte("hunter2")); err != nil {
		t.Fatalf("the printed hash does not verify: %v (hash %q)", err, hash)
	}
	cost, err := bcrypt.Cost([]byte(hash))
	if err != nil || cost != 12 {
		t.Errorf("bcrypt cost = %d (%v), want 12 (arch §8.2)", cost, err)
	}
	// The plaintext must never be echoed back.
	if strings.Contains(out.String(), "hunter2") || strings.Contains(errOut.String(), "hunter2") {
		t.Error("the plaintext password appeared in the output")
	}
}

func TestRun_hashPassword_rejectsAnEmptyPasswordAndArguments(t *testing.T) {
	t.Parallel()
	var out, errOut bytes.Buffer
	if code := run([]string{"hash-password"}, strings.NewReader("\n"), &out, &errOut); code == exitOK {
		t.Error("an empty password must not produce a hash")
	}
	out.Reset()
	errOut.Reset()
	if code := run([]string{"hash-password", "secret"}, strings.NewReader(""), &out, &errOut); code == exitOK {
		t.Error("a password on the command line must be refused: argv is visible in ps")
	}
}

func TestParseFlags_subcommandThenFlags(t *testing.T) {
	t.Parallel()
	f, err := parseFlags([]string{"hash-password", "--log-level", "debug"}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if f.subcommand != "hash-password" || f.logLevel != "debug" {
		t.Errorf("got %+v", f)
	}
}

func TestRun_portFlag_rejectsAnImpossiblePort(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "shelf.yaml")
	media := filepath.Join(dir, "media")
	if err := os.MkdirAll(media, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "roots:\n  - {name: \"m\", path: " + media + "}\nstorage: {data_dir: " +
		filepath.Join(dir, "data") + ", cache_dir: " + filepath.Join(dir, "cache") + "}\n"
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code := run([]string{"--config", cfgPath, "--port", "70000"}, strings.NewReader(""), &out, &errOut)
	if code != config.ExitCode {
		t.Fatalf("exit = %d, want %d\n%s", code, config.ExitCode, errOut.String())
	}
}

func TestRun_badLogLevel_exitsTwo(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "shelf.yaml")
	media := filepath.Join(dir, "media")
	if err := os.MkdirAll(media, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "roots:\n  - {name: \"m\", path: " + media + "}\nstorage: {data_dir: " +
		filepath.Join(dir, "data") + ", cache_dir: " + filepath.Join(dir, "cache") + "}\n"
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code := run([]string{"--config", cfgPath, "--log-level", "chatty"}, strings.NewReader(""), &out, &errOut)
	if code != config.ExitCode {
		t.Fatalf("exit = %d, want %d\n%s", code, config.ExitCode, errOut.String())
	}
	if !strings.Contains(errOut.String(), "chatty") {
		t.Errorf("the message does not name the offending value: %s", errOut.String())
	}
}
