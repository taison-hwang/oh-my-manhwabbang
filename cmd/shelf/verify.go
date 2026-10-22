package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"shelf/internal/app"
	"shelf/internal/config"
	"shelf/internal/verify"
)

const verifyUsageText = `usage: shelf verify [--config PATH] [--root NAME]...

Read every page of every book in the index, in full, and check it against the
container's own length and CRC-32. Directories and PDFs carry no such data and
are counted as unverifiable. Nothing is written: index.db is opened read-only
(beside a running server, or immutable when it is stopped and checkpointed).

stdout: one line per defect, tab-separated,
  BROKEN  book_id  root/rel_path[!inner_path]  page_no|-  page_name|-  cause
then one line per selected root that could not be opened (the others are
still verified to the end),
  UNCHECKED  root_name  reason
then one SUMMARY line.

exit: 0    every selected root checked, no defects
      1    at least one defect
      2    configuration, index or flag error, or a root that was not checked:
           it could not be opened, or none of its indexed books exists there
           (an unmounted volume). The run is incomplete; its BROKEN lines are
           still printed
      130  interrupted

flags:
  --config PATH   configuration file (same lookup as the server)
  --root NAME     verify only this root; repeat for several. Default: every
                  enabled root.
`

// rootList is a repeatable --root.
type rootList []string

func (r *rootList) String() string     { return fmt.Sprint([]string(*r)) }
func (r *rootList) Set(v string) error { *r = append(*r, v); return nil }

// runVerify is `shelf verify` (ruling E-58). It has its own flag set because
// --root means nothing to the server, and because its exit codes differ: a
// flag error is 2 here, a configuration error, not the server's 1.
//
// Exit codes, in priority order: 130 interrupted; 2 configuration, index or
// flag error, or a root that was not checked (an incomplete run outranks
// its defects, which are printed all the same); 1 at least one defect; 0 clean.
func runVerify(argv []string, stdout, stderr io.Writer) int {
	return runVerifyContext(context.Background(), argv, stdout, stderr)
}

// exitInterrupted is 128+SIGINT, the shell's convention for "stopped by ^C".
const exitInterrupted = 130

// runVerifyContext is runVerify under a parent context, so a test can
// interrupt it the way SIGINT does.
func runVerifyContext(parent context.Context, argv []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("shelf verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, verifyUsageText) }
	var configPath string
	var roots rootList
	fs.StringVar(&configPath, "config", "", "path to shelf.yaml")
	fs.Var(&roots, "root", "root name; repeatable")
	if err := fs.Parse(argv); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return config.ExitCode
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "shelf verify: unexpected argument %q\n\n%s", fs.Arg(0), verifyUsageText)
		return config.ExitCode
	}

	cfg, err := config.Load(config.Options{ExplicitPath: configPath})
	if err != nil {
		fmt.Fprintf(stderr, "shelf: %v\n", err)
		return config.ExitCode
	}
	// Warnings only: an unreachable root is the one thing the source layer
	// logs that an operator running verify needs to see.
	log := app.NewLogger(stderr, cfg.Log.Format, slog.LevelWarn)

	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()

	sum, err := verify.Run(ctx, verify.Options{
		Config:   cfg,
		Roots:    roots,
		Out:      stdout,
		Progress: stderr,
		Logger:   log,
	})
	switch {
	case errors.Is(err, context.Canceled):
		fmt.Fprintf(stderr, "shelf verify: %v\n", err)
		return exitInterrupted
	case err != nil:
		fmt.Fprintf(stderr, "shelf verify: %v\n", err)
		return config.ExitCode
	case sum.RootsNotChecked > 0:
		fmt.Fprintf(stderr, "shelf verify: %d root(s) could not be opened and were not checked\n", sum.RootsNotChecked)
		return config.ExitCode
	case sum.DefectiveBooks > 0:
		return exitFailure
	}
	return exitOK
}
