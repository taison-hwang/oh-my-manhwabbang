package config

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"unicode"
)

// environment is everything about the machine this process runs on that the
// per-OS directory rules need. It is a struct rather than direct calls to
// os.Getenv/runtime.GOOS so that the linux, darwin and windows branches are all
// testable on one machine.
type environment struct {
	goos    string
	getenv  func(string) string
	homeDir func() (string, error)
}

func osEnvironment() environment {
	return environment{goos: runtime.GOOS, getenv: os.Getenv, homeDir: os.UserHomeDir}
}

func (e environment) home() (string, error) {
	h, err := e.homeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine the home directory: %w", err)
	}
	if h == "" {
		return "", errors.New("the home directory is empty")
	}
	return h, nil
}

// localAppData is the windows branch of every storage location.
func (e environment) localAppData() (string, error) {
	if d := e.getenv("LOCALAPPDATA"); d != "" {
		return d, nil
	}
	return "", errors.New("%LOCALAPPDATA% is not set")
}

// configDir holds shelf.yaml for entry 4 of the lookup order:
// $XDG_CONFIG_HOME/shelf, defaulting to ~/.config/shelf (arch §3.1).
func (e environment) configDir() (string, error) {
	if d := e.getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, appName), nil
	}
	if e.goos == "windows" {
		d, err := e.localAppData()
		if err != nil {
			return "", err
		}
		return filepath.Join(d, appName), nil
	}
	h, err := e.home()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, ".config", appName), nil
}

// dataDir holds index.db, user.db and session.key (arch §3.2):
//
//	$XDG_DATA_HOME/shelf -> ~/.local/share/shelf              (linux)
//	~/Library/Application Support/shelf                       (macOS)
//	%LOCALAPPDATA%\shelf                                      (windows)
func (e environment) dataDir() (string, error) {
	if e.goos == "windows" {
		d, err := e.localAppData()
		if err != nil {
			return "", fmt.Errorf("%w; set storage.data_dir explicitly", err)
		}
		return filepath.Join(d, appName), nil
	}
	if d := e.getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, appName), nil
	}
	h, err := e.home()
	if err != nil {
		return "", fmt.Errorf("%w; set storage.data_dir or $XDG_DATA_HOME", err)
	}
	if e.goos == "darwin" {
		return filepath.Join(h, "Library", "Application Support", appName), nil
	}
	return filepath.Join(h, ".local", "share", appName), nil
}

// cacheDir holds thumbs/, pdf/ and wazero/ (FR-CFG-003, arch §3.2):
//
//	$XDG_CACHE_HOME/shelf -> ~/.cache/shelf                   (linux)
//	~/Library/Caches/shelf                                    (macOS)
//	%LOCALAPPDATA%\shelf\cache                                (windows)
func (e environment) cacheDir() (string, error) {
	if e.goos == "windows" {
		d, err := e.localAppData()
		if err != nil {
			return "", fmt.Errorf("%w; set storage.cache_dir explicitly", err)
		}
		return filepath.Join(d, appName, "cache"), nil
	}
	if d := e.getenv("XDG_CACHE_HOME"); d != "" {
		return filepath.Join(d, appName), nil
	}
	h, err := e.home()
	if err != nil {
		return "", fmt.Errorf("%w; set storage.cache_dir or $XDG_CACHE_HOME", err)
	}
	if e.goos == "darwin" {
		return filepath.Join(h, "Library", "Caches", appName), nil
	}
	return filepath.Join(h, ".cache", appName), nil
}

// normalizeBasePath implements NFR-SEC-003: the application may be mounted
// under a sub-path behind a reverse proxy. The result is either "" (mounted at
// the root) or "/prefix" with a leading slash and no trailing one.
//
// ".." is rejected outright — not cleaned away — because a base path is
// concatenated into every URL the SPA builds and into http.StripPrefix, and
// silently rewriting what the user asked for is how a mount ends up somewhere
// nobody intended.
func normalizeBasePath(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	if strings.Contains(raw, "..") {
		return "", errors.New(`must not contain ".."`)
	}
	for _, r := range raw {
		switch {
		case r < 0x20 || r == 0x7f:
			return "", errors.New("must not contain control characters")
		case unicode.IsSpace(r):
			return "", errors.New("must not contain whitespace")
		case r == '\\' || r == '?' || r == '#':
			return "", fmt.Errorf(`must not contain %q`, string(r))
		}
	}
	trimmed := strings.Trim(raw, "/")
	if trimmed == "" { // "/" and "//" both mean "mounted at the root"
		return "", nil
	}
	p := "/" + trimmed
	if p != path.Clean(p) {
		return "", errors.New(`must be a clean path: no "." segments and no empty segments`)
	}
	return p, nil
}

// ensureDir creates one of our own directories 0700 if it is missing and proves
// it is writable. Only storage.data_dir and storage.cache_dir go through here —
// nothing under roots[].path is ever created, opened for writing or probed
// (FR-CFG-005 / NFR-DAT-002).
func ensureDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("cannot create the directory: %w", err)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("cannot read the directory: %w", err)
	}
	if !fi.IsDir() {
		return errors.New("exists but is not a directory")
	}
	probe, err := os.CreateTemp(dir, ".shelf-write-probe-*")
	if err != nil {
		return fmt.Errorf("is not writable: %w", err)
	}
	name := probe.Name()
	probe.Close()
	if err := os.Remove(name); err != nil {
		return fmt.Errorf("is not writable: %w", err)
	}
	return nil
}
