package main

import (
	"bufio"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"shelf/internal/auth"
	"shelf/internal/config"
)

// starterConfig is what `--init-config` writes (AC-007: "a single binary plus a
// config file"). It is embedded rather than read from the source tree because
// NFR-OPS-001 means there is no source tree next to the binary.
//
// `TestStarterConfig_parsesAndMatchesTheBuiltInDefaults` holds it to the same
// promise shelf.example.yaml carries: every value written here is the built-in
// default, so deleting a line changes nothing.
//
//go:embed starter.yaml
var starterConfig []byte

// runInitConfig writes the starter file and reports the path.
//
// It refuses to overwrite. A configuration file is authored data — it holds the
// root names that every id in user.db is derived from — and an --init-config
// typed into the wrong shell must not be able to silently orphan a reading
// history.
func runInitConfig(f *flags, stdout io.Writer) error {
	path := f.configPath
	if path == "" {
		p, err := config.InitPath(config.Options{})
		if err != nil {
			return fmt.Errorf("choosing where to write the configuration: %w", err)
		}
		path = p
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists; delete it or pass --config with another path", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("checking %s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, starterConfig, 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	fmt.Fprintf(stdout, "wrote %s\n\nEdit `roots` in that file, then run:\n\n    shelf --config %s\n", path, path)
	return nil
}

// runHashPassword is `shelf hash-password` (WP-13 acceptance 4).
//
// It exists so that a plaintext password never has to be written into
// shelf.yaml at all: `auth.password` is supported, but a string in Go is
// immutable and cannot be zeroed after hashing, so the plaintext lives in the
// process for its whole life. Pasting a hash avoids that entirely.
//
// The password is read from stdin, one line, so that
// `printf %s 'secret' | shelf hash-password` works in a script and nothing
// sensitive reaches argv — which is visible in `ps` and lands in the shell
// history. It is never logged and never appears in an error message
// (impl-plan §5.1).
//
// Echo is not suppressed. Doing so needs termios, which means golang.org/x/term,
// and the dependency set is frozen at the nine modules of arch §1.1 (D-08).
// When stdin is a terminal the prompt says so, and the recommended form is the
// pipe above, which never displays the password at all.
func runHashPassword(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) > 0 {
		return errors.New("hash-password takes no arguments; it reads the password from stdin")
	}
	if f, ok := stdin.(*os.File); ok && isTerminal(f) {
		fmt.Fprint(stderr, "password (it will be visible; pipe it in to avoid that): ")
	}
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("reading the password: %w", err)
	}
	plain := []byte(strings.TrimRight(line, "\r\n"))
	if len(plain) == 0 {
		return errors.New("the password is empty")
	}
	hash, err := auth.HashPassword(plain)
	if err != nil {
		return errors.New("hashing the password failed")
	}
	for i := range plain {
		plain[i] = 0
	}
	fmt.Fprintf(stdout, "%s\n\nPut it in shelf.yaml:\n\nauth:\n  password_hash: %q\n", hash, hash)
	return nil
}

// isTerminal reports whether f is an interactive terminal, using nothing but
// the standard library: a character device is what a tty is. It decides whether
// to print a prompt and nothing else.
func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
