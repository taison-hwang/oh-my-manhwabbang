package main

import (
	"flag"
	"fmt"
	"io"
	"strings"
)

// flags is everything the command line can say.
//
// The set is deliberately tiny (NFR-OPS-002: "a config file and a cache
// directory"). Two of them — port and logLevel — shadow a config key, and both
// are the ones an operator needs in the moment a service is misbehaving, when
// editing YAML and restarting is the wrong loop.
type flags struct {
	// configPath is --config: explicit, and fatal when missing (arch §3.1).
	configPath string
	// initConfig writes a commented starter file and exits 0 (AC-007).
	initConfig bool
	// rebuildIndex deletes index.db and forces a full scan (FR-IDX-005).
	rebuildIndex bool
	// logLevel overrides `log.level` when non-empty (NFR-OPS-005).
	logLevel string
	// port overrides `server.port` when > 0.
	port int
	// version prints the build banner and exits 0.
	version bool

	// subcommand is the first positional argument, e.g. `hash-password`.
	subcommand string
	// args are the positional arguments after the subcommand.
	args []string
}

const usageText = `shelf — a web reader for ZIP, folder and PDF comic collections.

usage:
  shelf [flags]                  start the server
  shelf hash-password [flags]    print a bcrypt hash for auth.password_hash

flags:
  --config PATH      configuration file; fatal if it does not exist.
                     Without it the lookup order is $SHELF_CONFIG, ./shelf.yaml,
                     $XDG_CONFIG_HOME/shelf/shelf.yaml, /etc/shelf/shelf.yaml.
  --init-config      write a commented starter configuration and exit
  --rebuild-index    delete index.db and rebuild it from disk. Reading progress,
                     per-book preferences and settings live in user.db and are
                     not touched.
  --log-level LEVEL  debug | info | warn | error; overrides log.level
  --port N           overrides server.port
  --version          print the version and exit
`

// parseFlags reads argv. out receives -h/--help and flag errors so that a test
// does not print to the process's stderr.
func parseFlags(argv []string, out io.Writer) (*flags, error) {
	var f flags
	fs := flag.NewFlagSet("shelf", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.Usage = func() { fmt.Fprint(out, usageText) }

	fs.StringVar(&f.configPath, "config", "", "path to shelf.yaml")
	fs.BoolVar(&f.initConfig, "init-config", false, "write a starter configuration and exit")
	fs.BoolVar(&f.rebuildIndex, "rebuild-index", false, "delete index.db and rebuild it")
	fs.StringVar(&f.logLevel, "log-level", "", "debug|info|warn|error")
	fs.IntVar(&f.port, "port", 0, "override server.port")
	fs.BoolVar(&f.version, "version", false, "print the version and exit")

	// A subcommand comes first, so that `shelf hash-password --config x` reads
	// the way every other tool does. Anything that is not a known subcommand is
	// left to the flag parser, which will reject it with the usage text.
	rest := argv
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		f.subcommand = rest[0]
		rest = rest[1:]
	}
	if err := fs.Parse(rest); err != nil {
		return nil, err
	}
	f.args = fs.Args()
	return &f, nil
}
