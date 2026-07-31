// Command shelf is the whole product: one static binary with the SPA compiled
// in, whose only runtime inputs are a configuration file and a writable cache
// directory (NFR-OPS-001, NFR-OPS-002, AC-007).
//
//	shelf --config ./shelf.yaml       start the server
//	shelf --init-config               write a commented starter configuration
//	shelf --rebuild-index             delete index.db and rebuild it from disk
//	shelf hash-password               print a bcrypt hash for auth.password_hash
//	shelf --version                   print the build banner
//
// Everything this file does is argument handling, exit codes and signals. The
// wiring lives in internal/app, which is where arch §6.3's start-up sequence is
// written down; keeping the two apart is what lets a test bring the whole
// server up in-process without a subprocess or a signal.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"shelf/internal/app"
	"shelf/internal/buildinfo"
	"shelf/internal/config"
	"shelf/web"
)

// Exit codes. 2 is config.ExitCode, which every configuration failure uses so
// that a supervisor can tell "you typed something wrong" apart from "it
// crashed" without parsing a message.
const (
	exitOK      = 0
	exitFailure = 1
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// run is main with the process boundary removed, so the tests can drive it.
func run(argv []string, stdin io.Reader, stdout, stderr io.Writer) int {
	f, err := parseFlags(argv, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitFailure
	}

	switch {
	case f.version:
		fmt.Fprintln(stdout, buildinfo.String())
		return exitOK
	case f.subcommand == "hash-password":
		if err := runHashPassword(f.args, stdin, stdout, stderr); err != nil {
			fmt.Fprintf(stderr, "shelf: %v\n", err)
			return exitFailure
		}
		return exitOK
	case f.subcommand != "":
		fmt.Fprintf(stderr, "shelf: unknown command %q\n\n%s", f.subcommand, usageText)
		return exitFailure
	case f.initConfig:
		if err := runInitConfig(f, stdout); err != nil {
			fmt.Fprintf(stderr, "shelf: %v\n", err)
			return exitFailure
		}
		return exitOK
	}

	// SIGINT/SIGTERM cancels the root context, which cancels the scan and
	// starts the graceful shutdown (arch §6.3 step 7). A second signal restores
	// the default handler, so an operator who has waited out shutdown_grace can
	// always get the process to die with another Ctrl-C.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	code, err := serve(ctx, f, stdout, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "shelf: %v\n", err)
	}
	return code
}

// serve loads the configuration and runs the server. The exit code is returned
// separately from the error because a configuration failure is exit 2 and
// everything else is exit 1.
func serve(ctx context.Context, f *flags, stdout, stderr io.Writer) (int, error) {
	cfg, err := config.Load(config.Options{ExplicitPath: f.configPath})
	if err != nil {
		// Every configuration failure is exit 2, including "there is no file":
		// config.NotFoundError already prints every path it looked in and how
		// to write one, which is what AC-007 needs an operator to be told.
		return config.ExitCode, err
	}
	if f.port > 0 {
		if f.port > 65535 {
			return config.ExitCode, fmt.Errorf("--port %d: must be between 1 and 65535", f.port)
		}
		cfg.Server.Port = f.port
	}

	levelName := cfg.Log.Level
	if f.logLevel != "" {
		levelName = f.logLevel // the flag wins over the file (NFR-OPS-005)
	}
	level, err := app.ParseLevel(levelName)
	if err != nil {
		return config.ExitCode, err
	}
	log := app.NewLogger(stderr, cfg.Log.Format, level)

	log.Info("starting", "version", buildinfo.Version, "commit", buildinfo.Commit,
		"config", cfg.FilePath, "log_level", level.String())

	a, err := app.New(ctx, app.Options{
		Config:       cfg,
		Logger:       log,
		Static:       web.Dist(),
		RebuildIndex: f.rebuildIndex,
	})
	if err != nil {
		return exitFailure, err
	}
	defer func() {
		if cerr := a.Close(); cerr != nil {
			log.Error("shutdown was not clean", "err", cerr)
		}
	}()

	fmt.Fprintf(stdout, "SHELF is listening on %s\n", a.BaseURL())
	if err := a.Run(ctx); err != nil {
		return exitFailure, err
	}
	log.Info("stopped")
	return exitOK, nil
}
