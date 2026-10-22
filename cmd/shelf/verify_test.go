package main

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"shelf/internal/config"
	"shelf/internal/index"
	"shelf/internal/testutil"
	"shelf/internal/userdata"
)

// verifyFixture writes a configuration with one root "m" holding a.zip, and
// an index.db (built by the real packages, then closed) listing it as book
// B1. crcOverride, when non-nil, damages page 1's recorded CRC.
func verifyFixture(t *testing.T, crcOverride *uint32) string {
	t.Helper()
	return buildVerifyFixture(t, verifyFixtureOpts{crc: crcOverride})
}

type verifyFixtureOpts struct {
	crc *uint32
	// live leaves index.db open until the test ends, as a running server
	// holds it: the book row is then only in the -wal.
	live bool
	// offlineRoot, when set, adds an enabled root of that name, and a book O1
	// in it, whose directory exists but cannot be opened (mode 000). A path
	// that does not exist at all never gets this far: config.Load rejects it.
	offlineRoot string
	// sealedRoot, when set, adds an enabled root of that name whose only
	// indexed book P1 sits inside a directory that cannot be searched (mode
	// 000): the book's existence cannot be established, which is not the same
	// as its absence.
	sealedRoot string
	// goneRoot, when set, adds an enabled root of that name, an existing but
	// empty directory, and an indexed book G1 in it: an unmounted volume's
	// mount point.
	goneRoot string
}

func buildVerifyFixture(t *testing.T, o verifyFixtureOpts) string {
	t.Helper()
	crcOverride := o.crc
	dir := t.TempDir()
	media := filepath.Join(dir, "media")
	data := filepath.Join(dir, "data")
	for _, d := range []string{media, data} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	z := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
		{Name: "01.jpg", Data: []byte("page one bytes"), CRC32Override: crcOverride},
		{Name: "02.jpg", Data: []byte("page two bytes")},
	}})
	if err := os.WriteFile(filepath.Join(media, "a.zip"), z, 0o644); err != nil {
		t.Fatal(err)
	}

	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()
	ud, err := userdata.Open(ctx, userdata.Options{Path: filepath.Join(data, "user.db"), Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	idx, err := index.Open(ctx, index.Options{
		Path: filepath.Join(data, "index.db"), UserPath: filepath.Join(data, "user.db"), Logger: quiet,
	})
	if err != nil {
		t.Fatal(err)
	}
	u := url.URL{Scheme: "file", Path: filepath.Join(data, "index.db")}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO books (id, series_id, root_name, rel_path, inner_path, display_name,
		sort_key, ord, kind, content_version, scan_gen) VALUES ('B1','S','m','a.zip','','a',x'00',0,'zip','v',1)`)
	if err == nil && o.offlineRoot != "" {
		_, err = db.Exec(`INSERT INTO books (id, series_id, root_name, rel_path, inner_path, display_name,
			sort_key, ord, kind, content_version, scan_gen) VALUES ('O1','S',?,'b.zip','','b',x'00',0,'zip','v',1)`,
			o.offlineRoot)
	}
	if err == nil && o.goneRoot != "" {
		_, err = db.Exec(`INSERT INTO books (id, series_id, root_name, rel_path, inner_path, display_name,
			sort_key, ord, kind, content_version, scan_gen) VALUES ('G1','S',?,'g.zip','','g',x'00',0,'zip','v',1)`,
			o.goneRoot)
	}
	if err == nil && o.sealedRoot != "" {
		_, err = db.Exec(`INSERT INTO books (id, series_id, root_name, rel_path, inner_path, display_name,
			sort_key, ord, kind, content_version, scan_gen) VALUES ('P1','S',?,'sealed/b.zip','','b',x'00',0,'zip','v',1)`,
			o.sealedRoot)
	}
	_ = db.Close()
	if o.live {
		t.Cleanup(func() { _ = idx.Close(); _ = ud.Close() })
	} else {
		_ = idx.Close()
		_ = ud.Close()
	}
	if err != nil {
		t.Fatal(err)
	}

	cfgPath := filepath.Join(dir, "shelf.yaml")
	body := "roots:\n  - {name: \"m\", path: " + media + "}\n"
	if o.offlineRoot != "" {
		locked := filepath.Join(dir, "locked")
		if err := os.Mkdir(locked, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
		if f, err := os.Open(locked); err == nil {
			_ = f.Close()
			t.Skip("a mode-000 directory is still readable here (running as root?)")
		}
		body += "  - {name: \"" + o.offlineRoot + "\", path: " + locked + "}\n"
	}
	if o.goneRoot != "" {
		empty := filepath.Join(dir, "mountpoint")
		if err := os.Mkdir(empty, 0o755); err != nil {
			t.Fatal(err)
		}
		body += "  - {name: \"" + o.goneRoot + "\", path: " + empty + "}\n"
	}
	if o.sealedRoot != "" {
		sealed := filepath.Join(dir, "sealedroot", "sealed")
		if err := os.MkdirAll(sealed, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(sealed, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(sealed, 0o755) })
		if _, err := os.Stat(filepath.Join(sealed, "b.zip")); !os.IsPermission(err) {
			t.Skip("a mode-000 directory can still be searched here (running as root?)")
		}
		body += "  - {name: \"" + o.sealedRoot + "\", path: " + filepath.Dir(sealed) + "}\n"
	}
	body += "storage: {data_dir: " + data + ", cache_dir: " + filepath.Join(dir, "cache") + "}\n"
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return cfgPath
}

func runVerifyCmd(argv ...string) (int, string, string) {
	var out, errOut bytes.Buffer
	code := run(append([]string{"verify"}, argv...), strings.NewReader(""), &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestRun_verify_cleanLibraryExitsZero(t *testing.T) {
	t.Parallel()
	cfg := verifyFixture(t, nil)
	code, out, errOut := runVerifyCmd("--config", cfg)
	if code != exitOK {
		t.Fatalf("exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	if !strings.Contains(out, "SUMMARY\tbooks=1\tpages=2\t") || strings.Contains(out, "BROKEN") {
		t.Errorf("stdout:\n%s", out)
	}
}

func TestRun_verify_defectExitsOne(t *testing.T) {
	t.Parallel()
	bad := uint32(0xbadc0de)
	cfg := verifyFixture(t, &bad)
	code, out, errOut := runVerifyCmd("--config", cfg, "--root", "m")
	if code != exitFailure {
		t.Fatalf("exit = %d, want 1\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	if !strings.Contains(out, "BROKEN\tB1\tm/a.zip\t1\t01.jpg\tcrc mismatch: want 0badc0de") {
		t.Errorf("stdout:\n%s", out)
	}
}

func TestRun_verify_setupFailuresExitTwo(t *testing.T) {
	t.Parallel()
	cfg := verifyFixture(t, nil)
	noIndex := verifyFixture(t, nil)
	if err := os.Remove(filepath.Join(filepath.Dir(noIndex), "data", "index.db")); err != nil {
		t.Fatal(err)
	}
	for name, argv := range map[string][]string{
		"missing config": {"--config", filepath.Join(t.TempDir(), "nope.yaml")},
		"no index":       {"--config", noIndex},
		"unknown root":   {"--config", cfg, "--root", "elsewhere"},
		"unknown flag":   {"--config", cfg, "--bogus"},
		"positional":     {"--config", cfg, "extra"},
	} {
		code, out, errOut := runVerifyCmd(argv...)
		if code != config.ExitCode {
			t.Errorf("%s: exit = %d, want %d\nstdout:\n%s\nstderr:\n%s", name, code, config.ExitCode, out, errOut)
		}
	}
}

func TestRun_verify_helpExitsZeroAndDocumentsRoot(t *testing.T) {
	t.Parallel()
	code, _, errOut := runVerifyCmd("-h")
	if code != exitOK {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(errOut, "--root") {
		t.Errorf("verify -h does not mention --root:\n%s", errOut)
	}
}

func TestUsage_listsVerify(t *testing.T) {
	t.Parallel()
	if !strings.Contains(usageText, "shelf verify") {
		t.Errorf("the usage text does not list shelf verify:\n%s", usageText)
	}
}

// cancelOnBroken is stdout that interrupts the run as soon as the first
// BROKEN line arrives — an operator pressing Ctrl-C mid-run.
type cancelOnBroken struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	cancel context.CancelFunc
}

func (w *cancelOnBroken) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if bytes.HasPrefix(p, []byte("BROKEN")) {
		w.cancel()
	}
	return w.buf.Write(p)
}

// An interrupt is 130 (128+SIGINT), never 1: 1 means "defects found", and a
// script must be able to tell an incomplete run from a damaged collection.
func TestRun_verify_interruptedMidRunExits130(t *testing.T) {
	t.Parallel()
	bad := uint32(0xbadc0de)
	cfg := verifyFixture(t, &bad)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := &cancelOnBroken{cancel: cancel}
	var errOut bytes.Buffer
	code := runVerifyContext(ctx, []string{"--config", cfg}, out, &errOut)
	if code != exitInterrupted || exitInterrupted != 130 {
		t.Fatalf("exit = %d, want 130\nstdout:\n%s\nstderr:\n%s", code, out.buf.String(), errOut.String())
	}
	if !strings.Contains(errOut.String(), "interrupted") {
		t.Errorf("stderr does not say the run was interrupted:\n%s", errOut.String())
	}
}

// The interrupt can land while index.db is being read beside a running
// server; that is still an interrupt, not an index error (2).
func TestRun_verify_interruptedDuringTheIndexReadExits130(t *testing.T) {
	t.Parallel()
	cfg := buildVerifyFixture(t, verifyFixtureOpts{live: true})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out, errOut bytes.Buffer
	if code := runVerifyContext(ctx, []string{"--config", cfg}, &out, &errOut); code != exitInterrupted {
		t.Fatalf("exit = %d, want 130\nstdout:\n%s\nstderr:\n%s", code, out.String(), errOut.String())
	}
}

// A root that cannot be opened does not stop the run: the readable root is verified and
// its defects printed, the other is named, and the exit is 2 because the run
// is incomplete — even though defects were found.
func TestRun_verify_unreadableRootExitsTwoAfterReportingTheRest(t *testing.T) {
	t.Parallel()
	bad := uint32(0xbadc0de)
	cfg := buildVerifyFixture(t, verifyFixtureOpts{crc: &bad, offlineRoot: "off"})
	code, out, errOut := runVerifyCmd("--config", cfg)
	if code != config.ExitCode {
		t.Fatalf("exit = %d, want %d\nstdout:\n%s\nstderr:\n%s", code, config.ExitCode, out, errOut)
	}
	if !strings.Contains(out, "BROKEN\tB1\tm/a.zip\t1\t01.jpg\tcrc mismatch") {
		t.Errorf("the readable root's defect is missing:\n%s\nstderr:\n%s", out, errOut)
	}
	if !strings.Contains(out, "UNCHECKED\toff\t") || !strings.Contains(out, "\troots_not_checked=1") {
		t.Errorf("the unreadable root is not named in the summary:\n%s", out)
	}
}

// A root is passed over as unmounted only when none of its indexed books
// exists. A book whose existence cannot be established — its directory cannot
// be searched — is not evidence of a missing volume: the root is checked and
// the book is reported, and nothing is UNCHECKED.
func TestRun_verify_unsearchableBookIsBrokenNotAnUnmountedRoot(t *testing.T) {
	t.Parallel()
	cfg := buildVerifyFixture(t, verifyFixtureOpts{sealedRoot: "p"})
	code, out, errOut := runVerifyCmd("--config", cfg)
	if code != exitFailure {
		t.Fatalf("exit = %d, want 1\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	if !strings.Contains(out, "BROKEN\tP1\tp/sealed/b.zip\t") || strings.Contains(out, "UNCHECKED") {
		t.Errorf("stdout:\n%s", out)
	}
}

// An empty mount point opens like any directory. With a book indexed there and
// none present, the root is named UNCHECKED — not one BROKEN line — and the
// run exits 2 even though the other root is clean.
func TestRun_verify_emptyMountPointIsUncheckedAndExitsTwo(t *testing.T) {
	t.Parallel()
	cfg := buildVerifyFixture(t, verifyFixtureOpts{goneRoot: "off"})
	code, out, errOut := runVerifyCmd("--config", cfg)
	if code != config.ExitCode {
		t.Fatalf("exit = %d, want %d\nstdout:\n%s\nstderr:\n%s", code, config.ExitCode, out, errOut)
	}
	if strings.Contains(out, "BROKEN") || !strings.Contains(out, "UNCHECKED\toff\t") || !strings.Contains(out, "mounted") {
		t.Errorf("stdout:\n%s", out)
	}
}
