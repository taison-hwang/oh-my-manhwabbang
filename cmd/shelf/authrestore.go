package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"

	"shelf/internal/config"
	"shelf/internal/userdata"
)

const authUsageText = `usage: shelf auth restore-owner --sub SUB [--config PATH]

Copy one user's reading progress, book preferences, settings and view state
back over the single-user tables (the way back from auth.mode "oidc" to
"password", ruling E-74 §2.14). For a key both sides have, the row with the
newer updated_at wins; on a tie the single-user row stays.

Stop the server first. The tool does not check, and a running server would keep
writing the rows it is restoring over.

flags:
  --sub SUB       the user's OIDC subject, as listed in the users table
  --config PATH   configuration file (same lookup as the server)
`

// runAuth is `shelf auth <command>`. Only restore-owner exists.
func runAuth(argv []string, stdout, stderr io.Writer) int {
	if len(argv) == 0 || argv[0] != "restore-owner" {
		fmt.Fprint(stderr, authUsageText)
		return exitFailure
	}
	fs := flag.NewFlagSet("shelf auth restore-owner", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, authUsageText) }
	var sub, configPath string
	fs.StringVar(&sub, "sub", "", "OIDC subject to restore")
	fs.StringVar(&configPath, "config", "", "path to shelf.yaml")
	if err := fs.Parse(argv[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitFailure
	}
	if sub == "" || fs.NArg() > 0 {
		fmt.Fprint(stderr, authUsageText)
		return exitFailure
	}
	cfg, err := config.Load(config.Options{ExplicitPath: configPath})
	if err != nil {
		fmt.Fprintf(stderr, "shelf: %v\n", err)
		return config.ExitCode
	}
	res, err := restoreOwner(context.Background(), filepath.Join(cfg.Storage.DataDir, "user.db"), sub)
	if err != nil {
		fmt.Fprintf(stderr, "shelf: %v\n", err)
		return exitFailure
	}
	fmt.Fprintf(stdout, "restored %q: progress %d, book_prefs %d, settings %d, view_state %d\n",
		sub, res.Progress, res.Prefs, res.Settings, res.ViewState)
	return exitOK
}

func restoreOwner(ctx context.Context, userDBPath, sub string) (userdata.RestoreResult, error) {
	db, err := userdata.Open(ctx, userdata.Options{Path: userDBPath})
	if err != nil {
		return userdata.RestoreResult{}, fmt.Errorf("opening %s: %w", userDBPath, err)
	}
	defer db.Close()
	return db.RestoreOwner(ctx, sub)
}
